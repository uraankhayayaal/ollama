package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	appask "ai/application/ask"
	appboard "ai/application/board"
	"ai/application/hitl"
	applogs "ai/application/logs"
	"ai/application/planner"
	apprag "ai/application/rag"
	"ai/application/usecases"
	"ai/chat"
	"ai/domain"
	"ai/infrastructure/board"
	"ai/infrastructure/rag"
	"ai/logging"
	"ai/projects"
	"ai/runctx"
	"ai/runevents"
	"ai/runmetrics"
	"ai/tokens"
	"ai/tools"
	"ai/workspace"
	"sync/atomic"
)

// Session — живая оркестрация одного проекта: один KanbanRunner на проект,
// его HITL-затворы (блокирующий runner до решения человека) и поток событий
// в шину проекта. Stop отменяет контекст runner'а.
//
// SessionRegistry гарантирует single-flight: старт нового запуска при активной
// сессии отклоняется (409), чтобы два runner'а не дрались за доску.
type Session struct {
	srv     *Server
	project string

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	// standby — режим ожидания: оркестрация запущена по доске, но брать в
	// работу нечего (нет эпиков/задач, которые можно исполнить). Сессия жива и
	// ждёт появления работы; статус в шину — «standby».
	standby bool

	// bgIndex — оркестратор фоновой индексации RAG проекта (Ф-5, Р-2): single-
	// flight по ветке (для каталогов без git — по пути), отчёты в лог и чат.
	// Разные ветки индексируются параллельно. Один на сессию.
	bgIndex *apprag.BackgroundIndexer

	// gate — HITL-затвор (подтверждение эпиков/задач): блокирует runner до
	// решения человека. Состояние и канал решения живут в application/hitl.
	gate *hitl.Gate

	// pending — активный структурированный вопрос ассистента (AskUser):
	// один на сессию, блокирует агентский цикл до ответа на все вопросы.
	pending *appask.Pending

	router *runevents.Router
	// metrics — реестр телеметрии агентского цикла проекта (Ф-1). Подписан на
	// тот же поток событий, что и роутер, поэтому метрики видят и агента, и
	// scope единицы работы, и точные моменты начала/конца вызова инструмента.
	metrics *runmetrics.Registry
	// appLog — кольцевой буфер логов рантайма приложения (Ф-2). Отдельный от
	// logs/<проект>.log: там журнал работы агентов, а здесь то, что вывело
	// само приложение. Читает REST-хвост и «рантайм»-вкладка Web UI.
	appLog  *appLogBuffer
	chat    *chat.Store
	board   *board.Store
	tok     *tokens.Store
	ctx     context.Context
	events  chan ProjectEvent // внутренняя шина событий проекта (для флашера и слушателей)
	wg      sync.WaitGroup
	stopped bool

	// runner — активный KanbanRunner оркестрации (Ф-2). Никогда не зовётся
	// напрямую из слушателей; только Wake() для выхода из standby по событию
	// board_changed. nil вне оркестрации.
	runner *planner.KanbanRunner

	// runDone закрывается, когда горутина текущего запуска (start) полностью
	// завершилась. Нужен Restart: он отменяет контекст и ждёт закрытия канала,
	// прежде чем стартовать заново (иначе single-flight start отвергнет запуск
	// как «оркестрация уже идёт»). Создаётся в start под mu, читается Restart.
	runDone chan struct{}

	// boardRev — монотонный счётчик изменений доски (инкремент на каждое
	// событие board_changed). Служит ассистенту «индикатором свежести»: доста-
	// точно увидеть, что ревизия выросла относительно прошлого ответа, чтобы
	// понять, что доска менялась. Потокобезопасен (atomic).
	boardRev atomic.Uint64

	// logCooldown — окно подавления повторного разбора того же лога (кнопка
	// «Разобрать»): два клика подряд не должны дважды звать модель и плодить в
	// чате одинаковые ответы. Состояние — в application/logs.Cooldown.
	logCooldown *applogs.Cooldown

	// rate — последняя реальная скорость генерации (вых. ток/с) из usage
	// провайдера (Ollama eval_count/eval_duration). Не сбрасывается между
	// генерациями: показывает актуальную производительность модели.
	rate float64

	// log — лог проекта (logs/<проект>.log). В режиме serve один процесс ведёт
	// много проектов, поэтому сообщения сессии пишутся в файл своего проекта,
	// а не в общий logs/server.log.
	log *logging.Logger

	// sessionInjections — инъекции уровня сессии: добавляются в работающей
	// сессии через API и применяются к следующему вызову модели. Набор под
	// mutex (наблюдается в wg/stop, персистентность пока не требуется).
	sessionInjections *domain.InjectionSet
}

// newSession создаёт сессию проекта (без запуска runner'а).
func (s *Server) newSession(project string) (*Session, error) {
	boardStore, err := board.NewStore(context.Background(), board.LoadConfig().StoreConfig(project))
	if err != nil {
		return nil, err
	}
	boardStore.SetRepositories(s.repositoriesForProject(project))
	// Ф-1..Ф-4: авто-действия git-workflow при изменениях доски (ветки эпиков/
	// задач при создании, мёрдж done→релиз, worktree задачи, синхрон с main).
	s.attachGitHooks(project, boardStore)
	chatStore, err := chat.NewStore(context.Background(), chat.StoreConfig{
		Addr:     board.LoadConfig().RedisAddr,
		Password: board.LoadConfig().RedisPassword,
		DB:       board.LoadConfig().RedisDB,
		Project:  project,
	})
	if err != nil {
		_ = boardStore.Close()
		return nil, err
	}
	tokStore, err := tokens.NewStore(context.Background(), tokens.StoreConfig{
		Addr:     board.LoadConfig().RedisAddr,
		Password: board.LoadConfig().RedisPassword,
		DB:       board.LoadConfig().RedisDB,
		Project:  project,
	})
	if err != nil {
		_ = boardStore.Close()
		_ = chatStore.Close()
		return nil, err
	}
	// Файл проекта открываем сразу: первая запись (и panel логов в Web UI)
	// не должна ждать ленивого создания каталога.
	logging.Attach(project)
	sess := &Session{
		srv:     s,
		project: project,
		chat:    chatStore,
		board:   boardStore,
		tok:     tokStore,
		events:  make(chan ProjectEvent, 64),
		log:     logging.For(project),
	}
	sess.router = runevents.NewRouter(sess.routeRunEvent)
	// HITL-затвор: политика в application/hitl, эффекты — адаптер сессии.
	sess.gate = hitl.NewGate(sess)
	// Реестр метрик — общий на проект и живущий дольше сессии (server.metricsFor).
	sess.metrics = s.metricsFor(project)
	// Буфер логов рантайма и подписка на них инструмента ReadAppLogs: строки
	// идут и в кольцевой буфер (UI/REST), и в шину (живая трансляция).
	sess.appLog = &appLogBuffer{}
	sess.registerAppLogSink(context.Background())
	// Ф-1 «спроси пользователя»: состояние активного структурированного
	// вопроса; публикация/статус/лог идут через адаптер askAnnouncer.
	sess.pending = appask.NewPending(askAnnouncer{sess: sess})
	// Cooldown повторного разбора логов (кнопка «Разобрать»).
	sess.logCooldown = applogs.NewCooldown(logAnalysisCooldown)
	// Ф-5/Р-2: оркестратор фоновой индексации RAG (single-flight по ветке,
	// отчёты в лог/чат). Прогоны строит сервер (см. ragindex.go/ragref.go).
	sess.bgIndex = apprag.NewBackgroundIndexer(project, ragIndexSink{sess: sess}, sess.newProjectIndexer)
	// Ф-5: прогноз расхода токенов для новых единиц доски (эпиков/задач) —
	// считается по истории завершённых единиц этой же доски. Источник внедряется
	// в хранилище, поэтому оценка появляется при любом создании записи
	// (архитектор, лид, чат-ассистент), а не только в агентском цикле.
	sess.board.TokenEstimate = sess.tokenEstimate()
	// Ф-2: событийная связь «доска ↔ чат» на слушателях шины проекта.
	// Сессия подписывает своих внутренних потребителей; другие компоненты
	// (будущие аналитики, эпики/задачи) добавляются как новые Listen-подписки
	// без правки хендлеров.
	s.listeners(project, sess)
	return sess, nil
}

// routeRunEvent — единая маршрутизация событий агентского цикла сессии: трансляция
// в чат плюс реакция на изменение доски. Её используют ВСЕ репортёры проекта — и
// оркестрации (sess.router), и idle-чат-ассистента (runChatAssistant). Раньше
// ассистент собирал отдельный роутер с голым sess.chatEvent и терял emitBoard:
// в idle-сессии boardFlusher не крутится, поэтому снимок доски после Board* не
// публиковался и созданный чатом эпик/баг не появлялся в Web UI до ручного
// обновления страницы.
func (sess *Session) routeRunEvent(ev runevents.Event) {
	// Реестр метрик — первый потребитель события: он считает длительности
	// вызовов по моментам начала/конца, поэтому должен увидеть событие раньше
	// любых медленных потребителей (чат, трансляция).
	sess.metrics.Record(ev)
	sess.chatEvent(ev)
	// Потоковые фрагменты не меняют доску — не дёргаем флашера на каждый токен.
	// Логи рантайма — тоже поток: приложение может писать сотни строк в
	// секунду, и каждая из них не должна проходить через emit чата.
	if ev.Type == runevents.TypeMessageDelta || ev.Type == runevents.TypeAppLog {
		return
	}
	// Инструменты доски (Board*: создание/правка эпиков, задач, багов)
	// меняют доску — бамп снимка. Остальные события цикла (текст модели,
	// файловые инструменты) доску не меняют — только чат.
	if (ev.Type == runevents.TypeToolStart || ev.Type == runevents.TypeToolResult) && tools.IsBoardTool(ev.Tool) {
		// emitBoard, а не голый emit: вне оркестрации (idle-чат) boardFlusher
		// не крутится, и обычный тик никто бы не дренул — созданные чатом
		// эпики/задачи/баги не появились бы на доске до ручного обновления
		// страницы. emitBoard публикует снимок сразу, когда флашера нет.
		sess.emitBoard("ассистент: инструмент доски")
		return
	}
	sess.emit(ProjectEvent{Type: EventChatUpdated})
}

// listeners регистрирует внутренних слушателей событий проекта на шине (Ф-2).
// Порядок вызова важен: слушатели зовутся в порядке подписки.
func (s *Server) listeners(project string, sess *Session) {
	// Доска изменилась → ревизия растёт: ассистент в следующем ответе видит
	// свежесть доски (chatAssistantPrompt) и может сообщить, что изменилось.
	s.hub.Listen(project, "chatassist-board-rev", func(ev ProjectEvent) {
		if ev.Type == EventBoardChanged {
			sess.boardRev.Add(1)
		}
	})
	// Доска изменилась во время ожидания работы (standby) → будим runner'а:
	// он тут же пересчитает hasWork вместо ожидания 5-секундного опроса.
	s.hub.Listen(project, "standby-wake", func(ev ProjectEvent) {
		if ev.Type != EventBoardChanged {
			return
		}
		sess.mu.Lock()
		standby, runner := sess.standby, sess.runner
		sess.mu.Unlock()
		if standby && runner != nil {
			runner.Wake()
		}
	})
}

// continueTaskText формирует текст задачи оркестрации (общая механика
// handleContinue и моста-инструмента KanbanStart): приоритет meta-задачи
// проекта; если её нет — обобщённое описание «продолжить работу по доске».
// Пустая доска не ошибка: запуск по кнопке берёт в работу только то, что уже
// есть на доске, а при отсутствии работы переходит в режим ожидания.
func (sess *Session) continueTaskText(ctx context.Context) (string, error) {
	if meta, merr := sess.board.GetMeta(ctx); merr == nil && meta != nil && meta.Task != "" {
		return meta.Task, nil
	}
	return "Продолжить работу над задачами доски", nil
}

// start запускает оркестрацию в отдельной горутине (single-flight).
// boardOnly — режим «только доска»: новые эпики не создаются, зато уже
// записанные (включая эпики без задач — их декомпозируют лиды) берутся в
// работу, а при отсутствии работы раннер уходит в standby. Так стартует
// кнопка «Продолжить» (handleContinue) и KanbanStart без текста задачи.
func (sess *Session) start(ctx context.Context, taskText string, provider usecases.FullCycle, boardOnly bool) error {
	sess.mu.Lock()
	if sess.running {
		sess.mu.Unlock()
		return errf("проект %s: оркестрация уже идёт", sess.project)
	}
	sess.running = true
	sess.stopped = false
	// Модель, на которой реально пойдёт этот запуск: в лог проекта. Без этой
	// строки в логах не видно, какая модель отвечает (и тем более — что она
	// сменилась в Web UI).
	sess.log.Infof("[llm] модель запуска: %s", sess.srv.prov.describe())
	cctx, cancel := context.WithCancel(ctx)
	// Репортёр в контексте оркестрации: runner.Generate по нему транслирует
	// текст модели, вызовы инструментов и потребление токенов в живую шину
	// (type=chat / type=tool / type=tokens). Без этого Web UI не видит ни
	// логов раундов, ни счётчика токенов (всё стоит на нулях).
	cctx = runevents.WithReporter(cctx, sess.router)
	// Расширенное сжатие агентских циклов фаз (Ф-6..Ф-11): RAG-вытеснение/
	// ранжирование и LSP-оглавления — каждый call к Generate в оркестрации
	// (архитектор, лиды, специалисты) получает CompressionClient из контекста.
	// Флаги CODEGEN_HISTORY_* из окружения; выключено по умолчанию.
	dir := projects.ProjectDir(sess.project)
	if inf, err := sess.srv.reg.Get(sess.project); err == nil {
		dir = inf.Root
	}
	ragClient := rag.NewClientSafe(rag.Config{})
	cctx = runctx.WithCompression(cctx, sess.project, dir, ragClient)
	sess.ctx = cctx
	sess.cancel = func() { cancel() }
	// Канал завершения запуска: Restart ждёт его после отмены контекста.
	done := make(chan struct{})
	sess.runDone = done
	sess.mu.Unlock()

	// Сессионная песочница (Этап 1, п. 1.5): контейнер поднимается ДО первого
	// агента и снимается defer'ом горутины-раннера. Блокирующий шаг (pull
	// образа) — вне sess.mu. Ошибка старта = отказ запуска: при
	// CODEGEN_SANDBOX=session тихий переход на хост запрещён (п. 1.3).
	if err := sess.srv.startProjectSandbox(cctx, sess.project); err != nil {
		cancel()
		sess.mu.Lock()
		sess.running = false
		sess.stopped = false
		sess.mu.Unlock()
		sess.log.Warnf("=== Оркестрация не началась: %v", err)
		sess.append(chat.RoleStatus, "Ошибка: "+err.Error(), "", "", nil)
		sess.broadcastStatus("error", err.Error())
		return err
	}

	sess.log.Infof("=== Оркестрация запущена: %s", truncateText(taskText, 120))
	sess.broadcastStatus("running", "")

	// Git-проект: перед первым агентом подтягиваем рабочую копию проекта
	// (temp/<имя> на ветке ai/<имя>) к актуальному main и пересобираем её RAG-
	// индекс. Иначе анализ и архитектура нового эпика читали бы и искали код
	// до релизов предыдущих (см. server/ragref.go). Фоном: старт не ждёт git.
	sess.srv.syncAgentBranchAsync(sess.project)

	// Флашер доски: раз в 500 мс публикует снимок доски, если были изменения.
	sess.wg.Add(1)
	go sess.boardFlusher(cctx)

	// Ф-2: сброс накопленных строк логов рантайма в лог проекта. Поштучная
	// запись на каждую строку запускала бы в лог тысячи строк в минуту от
	// одного «go build».
	sess.wg.Add(1)
	go sess.appLogFlusherLoop(cctx)

	runner := planner.NewKanbanRunner(provider, board.NewDomainStore(sess.board))
	runner.SetGate(sess)
	// Ф-4/Р-6: архитектор получает RAG (CodeSearch + блок «релевантный код»).
	runner.SetRAG(ragClient)
	// Ф-4/Р-5: архитектор может задать уточняющий вопрос пользователю (AskUser)
	// ДО публикации бэклога при противоречивом/неполном ТЗ. Мост-инструмент
	// блокирует фазу до ответа; в консольном CLI его нет — архитектор автономен.
	// Ф-5/Р-2: рядом — безопасный мост фоновой индексации RAG (предложить
	// пользователю построить индекс, не прерывая проектирование).
	runner.SetArchitectExtras(&askTool{b: sess}, newIndexBackgroundTool(sess))
	// Ф-2/Ф-3: учёт расхода токенов по задачам и эпикам. Оркестратор помечает
	// раунды scope'ом единицы работы, а по завершении записывает факт в доску
	// (board.TokenUsage). В консольном CLI (main.go) счётчика нет — вызовы
	// идут без атрибуции, поведение прежнее.
	runner.SetTokens(sess.tok)
	// Режим запуска: boardOnly — работа только с доской (кнопка «Продолжить»
	// и KanbanStart без текста задачи); иначе полный конвейер с проектированием
	// архитектором (KanbanStart с текстом задачи).
	runner.SetBoardOnly(boardOnly)
	runner.SetStandbyNotifier(sess.setStandby)
	// Ф-6 (этап 4.6): аудит решений оркестратора — эскалация модели, исчерпание
	// бюджета автономии, возврат зависших задач — в чат проекта, а не в Redis:
	// это разговор с человеком, а не состояние доски.
	runner.SetStatusNotifier(func(msg string) {
		sess.append(chat.RoleStatus, msg, "", "", nil)
	})
	// Ф-3: git-проекты — специалист работает в своём worktree ветки задачи
	// (OutputDir = worktree), поэтому авто-коммит на done соберёт его правки.
	runner.SetOutputDir(sess.srv.taskOutputDir)
	// 4.1: worktree ветки эпика для системного архитектора — лениво создаётся
	// при публикации бэклога (резолвер вызывается архитектором внутри
	// submit_architecture_backlog, после EpicCreatedHook) и снимается перед
	// merge-операциями, которым нужен свободный checkout релизной ветки.
	runner.SetEpicOutputDir(func(project, epicID string) string {
		return sess.srv.epicWorktree(cctx, project, epicID)
	})
	sess.mu.Lock()
	sess.runner = runner
	sess.mu.Unlock()

	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		defer close(done)
		// П. 1.7: контейнер песочницы живёт ровно столько, сколько оркестрация
		// (defer срабатывает и при отмене стопом — StopSession отвязывает
		// контекст сам).
		defer sess.srv.stopProjectSandbox(sess.project)
		err := runner.Run(cctx, sess.project, taskText)
		sess.mu.Lock()
		sess.running = false
		sess.standby = false
		sess.runner = nil
		sess.mu.Unlock()
		sess.gateOf().Reset()

		// Завершение: публикуем финальный статус и снимок доски.
		switch {
		case err == nil:
			sess.log.Infof("=== Оркестрация завершена: задача решена (все эпики и задачи выполнены)")
			sess.append(chat.RoleStatus, "Задача решена: все эпики и задачи выполнены.", "", "", nil)
			sess.broadcastStatus("done", "")
		case errors.Is(err, context.Canceled):
			sess.log.Infof("=== Оркестрация остановлена пользователем")
			sess.append(chat.RoleStatus, "Оркестрация остановлена пользователем.", "", "", nil)
			sess.broadcastStatus("stopped", "")
		default:
			sess.log.Warnf("=== Оркестрация прервана ошибкой: %v", err)
			sess.append(chat.RoleStatus, "Ошибка: "+err.Error(), "", "", nil)
			sess.broadcastStatus("error", err.Error())
		}
		sess.publishBoard(cctx)
		sess.cancel()
	}()
	return nil
}

// stop отменяет текущую оркестрацию.
func (sess *Session) stop() {
	sess.mu.Lock()
	cancel := sess.cancel
	running := sess.running
	sess.mu.Unlock()
	if cancel != nil && running {
		cancel()
	}
}

// isRunning сообщает, идёт ли сейчас оркестрация (для решений «рестартовать»).
func (sess *Session) isRunning() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.running
}

// Restart мягко перезапускает оркестрацию (стоп → старт), чтобы изменения
// доски — в первую очередь ручная пересортировка эпиков — применились
// немедленно, а не после ручного «Продолжить». Если оркестрация не идёт
// (running=false), возвращает nil без действий: порядок применится при
// следующем запуске. boardOnly всегда true: пересортировка не создаёт новых
// эпиков, работает ровно с тем, что уже на доске.
func (sess *Session) Restart(ctx context.Context, provider usecases.FullCycle) error {
	sess.mu.Lock()
	running := sess.running
	cancel := sess.cancel
	done := sess.runDone
	sess.mu.Unlock()
	if !running {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	// Ждём полной остановки прежнего запуска: single-flight start отклонит
	// повторный старт, пока running=true. Запас 30с на деструктивное завершение
	// (дроп песочницы, финальный снимок доски).
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Second):
			return fmt.Errorf("оркестрация не остановилась за 30с")
		}
	}
	taskText, err := sess.continueTaskText(ctx)
	if err != nil {
		return err
	}
	return sess.start(ctx, taskText, provider, true)
}

// --- HITL-затворы (реализация planner.HumanGate) ---

// --- HITL-затворы (реализация planner.HumanGate) ---
//
// Политика затвора живёт в application/hitl; здесь — вычисление ids события,
// адаптер сессии (эффекты в шину/статус) и тонкие обёртки.

func (sess *Session) Epics(ctx context.Context, epics []*board.Epic) (planner.GateDecision, error) {
	ids := make([]string, 0, len(epics))
	for _, e := range epics {
		ids = append(ids, e.TaskID)
	}
	return sess.gateOf().Wait(ctx, "epics", "Ожидается подтверждение эпиков архитектора", ids)
}

func (sess *Session) Tasks(ctx context.Context, tasks []*board.Task) (planner.GateDecision, error) {
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.TaskID)
	}
	return sess.gateOf().Wait(ctx, "tasks", "Ожидается подтверждение задач, готовых к работе", ids)
}

// gateEvent — payload активного затвора (алиас hitl.GateEvent для шины/тестов).
type gateEvent = hitl.GateEvent

// --- адаптер hitl.GateHost ---

// Stopped сообщает, остановлена ли сессия.
func (sess *Session) Stopped() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.stopped
}

// Running сообщает, идёт ли сейчас оркестрация.
func (sess *Session) Running() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.running
}

// BroadcastStatus — реализация hitl.GateHost (делегирует broadcastStatus).
func (sess *Session) BroadcastStatus(status, detail string) { sess.broadcastStatus(status, detail) }

// PublishGate — реализация hitl.GateHost: событие затвора в шину.
func (sess *Session) PublishGate(ev hitl.GateEvent) {
	sess.srv.hub.publish(sess.project, "gate", ev)
}

// gateOf лениво создаёт затвор: нулевая сессия (её собирают тесты) остаётся
// работоспособной.
func (sess *Session) gateOf() *hitl.Gate {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.gate == nil {
		sess.gate = hitl.NewGate(sess)
	}
	return sess.gate
}

// approve разрешает текущий затвор: approved + reason → решение в канал.
func (sess *Session) approve(typ string, approved bool, reason string) error {
	if err := sess.gateOf().Approve(typ, approved, reason); err != nil {
		return errf("проект %s: %v", sess.project, err)
	}
	return nil
}

// --- событийность ---

// chatEvent транслирует событие агентного цикла: в чат (role=tool) и в шину
// (type=tool) для live-таймлайна инструментов.
func (sess *Session) chatEvent(ev runevents.Event) {
	sess.toolTrace(ev)
	switch ev.Type {
	case runevents.TypeMessage:
		sess.append(chat.RoleAssistant, ev.Content, ev.Agent, "", nil)
	case runevents.TypeMessageDelta:
		// Потоковый фрагмент — только live-трансляция (type=chat_delta),
		// в историю чата не пишется: финал проходит обычным TypeMessage.
		sess.srv.hub.publish(sess.project, "chat_delta", ev)
	case runevents.TypeToolStart:
		sess.srv.hub.publish(sess.project, "tool", ev)
	case runevents.TypeToolResult:
		ok := ev.OK
		sess.append(chat.RoleTool, ev.Result, ev.Agent, ev.Tool, &ok)
		sess.srv.hub.publish(sess.project, "tool", ev)
	case runevents.TypeAppLog:
		// Строка лога рантайма из репортёра цикла: тот же буфер и та же
		// трансляция, что у прямой подписки инструмента (Ф-2).
		sess.onRunAppLog(ev)
	case runevents.TypeTokenCount:
		// Потребление токенов раунда: накапливаем в Redis (за время жизни
		// проекта) и транслируем новые тоталы в шину — фронт обновляет
		// счётчик рядом с кнопкой «Продолжить» в реальном времени. Scope
		// раунда (Ф-2) дополнительно копит расход по задаче/эпику — по нему
		// оркестратор запишет факт в сущность доски при завершении.
		sess.addTokens(ev.In, ev.Out, ev.TPS, ev.Scope)
	}
}

// toolTrace пишет выбор инструмента моделью и его результат в лог проекта
// (logs/<проект>.log), чтобы по файлу было видно, какие инструменты модель
// вызывала и с каким исходом. Остальные типы событий (текст, дельты, токены)
// в лог не попадают — их место в чате/шине.
func (sess *Session) toolTrace(ev runevents.Event) {
	who := ev.Agent
	if who == "" {
		who = "?"
	}
	switch ev.Type {
	case runevents.TypeToolStart:
		if ev.Arguments != "" {
			sess.log.Detailf("[инструмент] модель (%s) вызывает %s: %s", who, ev.Tool, ev.Arguments)
		} else {
			sess.log.Detailf("[инструмент] модель (%s) вызывает %s", who, ev.Tool)
		}
	case runevents.TypeToolResult:
		status := "ok"
		if !ev.OK {
			status = "ошибка"
		}
		result := ev.Result
		if len(result) > 500 {
			result = result[:500] + fmt.Sprintf("…(%d байт всего)", len(ev.Result))
		}
		sess.log.Detailf("[инструмент] результат %s (%s, %s): %s", ev.Tool, who, status, result)
	}
}

// addTokens прибавляет порцию токенов раунда к счётчику проекта, запоминает
// последнюю реальную скорость генерации и публикует новые итоговые суммы (+
// скорость) в шину (type=tokens). Непустой scope (Ф-2) — порция дополнительно
// копится в счётчике единицы работы (tokens:<project>:scoped:<scope>), откуда
// оркестратор возьмёт факт при фиксации итогов задачи/эпика.
func (sess *Session) addTokens(in, out int64, tps float64, scope string) {
	totIn, totOut, err := sess.tok.Add(context.Background(), in, out)
	if err != nil {
		sess.log.Warnf("server: счётчик токенов %s: %v", sess.project, err)
		return
	}
	if scope != "" {
		if err := sess.tok.AddScoped(context.Background(), scope, in, out); err != nil {
			sess.log.Warnf("server: счётчик токенов %s (%s): %v", sess.project, scope, err)
		}
	}
	sess.mu.Lock()
	if tps > 0 {
		sess.rate = tps
	}
	rate := sess.rate
	sess.mu.Unlock()
	sess.srv.hub.publish(sess.project, "tokens", tokenEvent{Input: totIn, Output: totOut, TPS: rate})
}

// tokenEvent — текущие накопленные токены проекта (вход/выход) и последняя
// скорость генерации (вых. ток/с), когда провайдер её сообщает.
type tokenEvent struct {
	Input  int64   `json:"in"`
	Output int64   `json:"out"`
	TPS    float64 `json:"tps,omitempty"`
}

// append пишет сообщение в чат (стяжку) и транслирует в шину (type=chat).
func (sess *Session) append(role chat.Role, content, agent, tool string, ok *bool) {
	sess.appendMsg(chat.Message{Role: role, Content: content, Agent: agent, Tool: tool, OK: ok})
}

// appendMsg пишет произвольное сообщение (включая payload Ask для role=ask)
// в чат и транслирует в шину (type=chat).
func (sess *Session) appendMsg(m chat.Message) {
	if _, err := sess.chat.Append(context.Background(), m); err != nil {
		sess.log.Warnf("server: запись в чат %s: %v", sess.project, err)
	}
	sess.srv.hub.publish(sess.project, "chat", m)
}

// emitBoard помечает доску «изменилась» с причиной (Ф-3): кладёт событие
// board_changed в шину (слушатели + флашер) и, если оркестрация не идёт
// (boardFlusher не крутится), публикует снимок сразу — иначе правки REST/чата
// не увидит Web UI до ручного обновления страницы или старта оркестрации.
func (sess *Session) emitBoard(detail string) {
	sess.emit(ProjectEvent{Type: EventBoardChanged, Detail: detail})
	sess.mu.Lock()
	running := sess.running
	sess.mu.Unlock()
	if !running {
		sess.publishBoardNow()
	}
}

// srvEmitBoard — emitBoard по имени проекта (для REST-хендлеров и git-хуков,
// у которых нет ссылки на Session, только project).
func (s *Server) srvEmitBoard(project, detail string) {
	if sess := s.session(project); sess != nil {
		sess.emitBoard(detail)
	}
}

// emit кладёт событие проекта во внутреннюю шину сессии (Ф-1/Ф-2): доставляет
// его внутренним слушателям (hub.Emit, например ассистент/standby-wake) и
// флашеру доски. Колесо неблокирующее: флашер/слушатели сработают по
// ближайшему циклу, а переполнение при массовой рассылке (например, пачка
// задач с каскадом статусов) не блокирует хендлер.
func (sess *Session) emit(ev ProjectEvent) {
	sess.srv.hub.Emit(sess.project, ev)
	select {
	case sess.events <- ev:
	default:
	}
}

// boardFlusher раз в 500 мс публикует снимок доски, если был событийный тик
// (EventBoardChanged). Параллельно, раз в минуту (Ф-5, гибрид), фоном сверяет
// MR с форджем, чтобы состояние MR-кнопок обновлялось и при активной
// оркестрации без перезахода на дашборд.
func (sess *Session) boardFlusher(ctx context.Context) {
	defer sess.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	mrTicker := time.NewTicker(60 * time.Second)
	defer mrTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Бэтчинг: пачка бампов доски за 500 мс публикуется одним снимком.
			select {
			case ev := <-sess.events:
				if ev.Type == EventBoardChanged {
					sess.publishBoard(ctx)
				}
			default:
			}
		case ev := <-sess.events:
			if ev.Type == EventBoardChanged {
				sess.publishBoard(ctx)
			}
		case <-mrTicker.C:
			changed, err := sess.srv.reconcileMRs(ctx, sess.project)
			if err != nil {
				continue
			}
			if changed {
				sess.emit(ProjectEvent{Type: EventBoardChanged, Detail: "MR-сверка нашла изменения"})
			}
		}
	}
}

// publishBoard шлёт снимок доски в шину (type=board).
func (sess *Session) publishBoard(ctx context.Context) {
	v, err := boardView(ctx, sess.board)
	if err != nil {
		return
	}
	v.Git = sess.srv.gitStatus(ctx, sess.project, v.Epics, v.Tasks)
	sess.srv.hub.publish(sess.project, "board", v)
}

// publishBoardNow публикует снимок доски в шину немедленно — для idle-сессий
// (boardFlusher живёт только на время оркестрации и тик бы не дренул).
func (sess *Session) publishBoardNow() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess.publishBoard(ctx)
}

// broadcastSnapshot публикует клиенту снапшот текущего состояния сессии:
// status + board + tokens. Вызывается сразу после WS-подписки — клиент
// получает авторитетное состояние при подключении, а не ждёт следующего
// события (иначе открытие/рефреш проекта с уже идущей оркестрацией показали
// бы устаревший «idle» до первой смены статуса).
func (sess *Session) broadcastSnapshot() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sess.mu.Lock()
	running, standby := sess.running, sess.standby
	sess.mu.Unlock()
	gating, gateTyp, gateMsg := sess.gateOf().State()
	meta, _ := sess.board.GetMeta(ctx)
	sess.srv.hub.publish(sess.project, "status", statusEvent{
		Status: computeStatus(running, gating, standby, meta),
		Detail: statusDetail(running, gating, standby),
		Gating: gating,
		Gate:   gateTyp,
	})
	// Событие gate одноразовое: переживший перезагрузку страницы клиент его не
	// получал и показывал пустую доску при живом затворе. Переигрываем payload
	// активного затвора — баннер восстанавливается без действий пользователя.
	if gating && gateMsg != nil {
		sess.srv.hub.publish(sess.project, "gate", *gateMsg)
	}
	if v, err := boardView(ctx, sess.board); err == nil {
		v.Git = sess.srv.gitStatus(ctx, sess.project, v.Epics, v.Tasks)
		sess.srv.hub.publish(sess.project, "board", v)
	}
	if in, out, err := sess.tok.Get(ctx); err == nil {
		sess.srv.hub.publish(sess.project, "tokens", tokenEvent{Input: in, Output: out})
	}
}

// setStandby переключает сессию в/из режима ожидания (нотификатор planner'а).
// Он вызывает runner, когда board-only раннеру нечего взять в работу (правда)
// или работа появилась (ложь). На фронте статус «standby» — активный: кнопка
// показывает «Стоп», оркестрация жива и ждёт записи доски.
func (sess *Session) setStandby(v bool) {
	sess.mu.Lock()
	was := sess.standby
	sess.standby = v
	running := sess.running
	sess.mu.Unlock()
	gating, _, _ := sess.gateOf().State()
	if was == v {
		return
	}
	if v {
		sess.log.Infof("[оркестрация] на доске нет работы — режим ожидания")
		sess.broadcastStatus("standby", standbyReason)
	} else {
		// Выход из ожидания: снова running (если оркестрация ещё идёт и нет
		// HITL-затвора — его статус приоритетнее и выставит waitGate).
		if running && !gating {
			sess.log.Infof("[оркестрация] на доске появилась работа — продолжаю")
			sess.broadcastStatus("running", "")
		}
	}
}

// broadcastStatus шлёт статус сессии в шину (type=status).
func (sess *Session) broadcastStatus(status, detail string) {
	gating, gateTyp, _ := sess.gateOf().State()
	sess.srv.hub.publish(sess.project, "status", statusEvent{
		Status: status,
		Detail: detail,
		Gating: gating,
		Gate:   gateTyp,
	})
}

// statusEvent — текущее состояние сессии.
type statusEvent struct {
	Status string `json:"status"`           // running|waiting|standby|done|stopped|error
	Detail string `json:"detail,omitempty"` // причина (например, текст ошибки)
	// Gating сериализуется всегда (без omitempty): клиент должен отличать
	// «затвор снят» (gating=false) от «поля нет» и убирать баннер, если
	// решение приняли в другой вкладке/клиенте.
	Gating bool   `json:"gating"`
	Gate   string `json:"gate,omitempty"`
}

// boardSnapshot — полный снимок доски для REST/SSE: read-model из
// application/board плюс git-статус проекта (презентация git-flow, завязанная
// на реестр проектов, поэтому остаётся на сервере).
type boardSnapshot struct {
	appboard.Snapshot
	// Git — git-статус проекта (ветки/MR эпиков и задач, Ф-5). Только для
	// git-проектов с созданными ветками; иначе nil.
	Git *gitView `json:"git,omitempty"`
}

// boardView собирает снимок доски (read-model — application/board).
func boardView(ctx context.Context, store *board.Store) (boardSnapshot, error) {
	snap, err := appboard.View(ctx, store)
	if err != nil {
		return boardSnapshot{}, err
	}
	return boardSnapshot{Snapshot: snap}, nil
}

// boardViewPage ограничивает снимок доски страницей (read-model — application/board).
func boardViewPage(ctx context.Context, store *board.Store, limit, offset int) (boardSnapshot, error) {
	snap, err := appboard.ViewPage(ctx, store, limit, offset)
	if err != nil {
		return boardSnapshot{}, err
	}
	return boardSnapshot{Snapshot: snap}, nil
}

// truncateText обрезает длинный текст для статусных сообщений (обёртка над
// application/board.TruncateText).
func truncateText(s string, n int) string { return appboard.TruncateText(s, n) }

// injections лениво создаёт набор сессионных инъекций (нулевая сессия тоже
// работоспособна: тесты и ранние стадии конструируют Session без newSession).
func (s *Session) injections() *domain.InjectionSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionInjections == nil {
		s.sessionInjections = &domain.InjectionSet{}
	}
	return s.sessionInjections
}

// SessionInjections возвращает сессионные инъекции (копия для потокобезопасности).
func (s *Session) SessionInjections() []board.Injection {
	return s.injections().List()
}

// AddSessionInjection добавляет инъекцию в сессию (hot apply): применяется
// со СЛЕДУЮЩЕГО запроса к модели в этой сессии.
func (s *Session) AddSessionInjection(inj board.Injection) {
	s.injections().Add(inj)
}

// UpsertSessionInjection добавляет инъекцию в сессию, перезаписывая запись с
// тем же id (правило — в domain.InjectionSet).
func (s *Session) UpsertSessionInjection(inj board.Injection) error {
	return s.injections().Upsert(inj)
}

// RemoveSessionInjection удаляет инъекцию по ID из сессии и сообщает, была ли она.
func (s *Session) RemoveSessionInjection(id string) bool {
	return s.injections().Remove(id)
}

var _ = workspace.KindTemp // связь с реестром для будущих эндпоинтов регистрации

// errf конструирует ошибку для ответов API.
func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }
