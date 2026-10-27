package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	appask "ai/application/ask"
	applogs "ai/application/logs"
	apprag "ai/application/rag"
	"ai/application/usecases"
	"ai/domain"
	"ai/infrastructure/chat"
	"ai/infrastructure/kafkarelay"
	"ai/infrastructure/runevents"
	"ai/infrastructure/runmetrics"
	"ai/infrastructure/workspace"
	"ai/shared/logging"
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
	ctx     context.Context
	events  chan ProjectEvent // внутренняя шина событий проекта (для флашера и слушателей)
	wg      sync.WaitGroup
	stopped bool

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

	// relaySink — публикация шагов оркестрации в Kafka (kafkarelay, топик
	// agent-harness-metrics). Nil, если рлей выключен. Подключается к
	// sess.router, поэтому видит только события оркестрации, а не ассистента.
	relaySink runevents.Sink
	// assistantSink — то же для чат-ассистента: его отдельный параллельный
	// стабильный run_id (от сессии к сессии), а не ран эпика оркестрации.
	assistantSink runevents.Sink
}

// newSession создаёт сессию проекта (без запуска runner'а).
func (s *Server) newSession(project string) (*Session, error) {
	chatStore, err := chat.NewStore(context.Background(), chat.StoreConfig{
		Project: project,
	})
	if err != nil {
		return nil, err
	}
	logging.Attach(project)
	sess := &Session{
		srv:     s,
		project: project,
		chat:    chatStore,
		events:  make(chan ProjectEvent, 64),
		log:     logging.For(project),
	}
	// Kafka-рлей наблюдаемости (агент-харнес): шаги агентного цикла идут в
	// топик agent-harness-metrics.
	if s.relay != nil {
		sess.relaySink = s.relay.SessionSink(project, kafkarelay.ModeOrchestration, nil)
		sess.assistantSink = s.relay.SessionSink(project, kafkarelay.ModeAssistant, nil)
	}
	// Фан-out: событие сначала уходит в Kafka-рлей (быстрый non-blocking
	// enqueue с drop при переполнении), затем — в обычную маршрутизацию.
	sess.router = runevents.NewRouter(func(ev runevents.Event) {
		if sess.relaySink != nil {
			sess.relaySink(ev)
		}
		sess.routeRunEvent(ev)
	})
	// Реестр метрик — общий на проект и живущий дольше сессии (server.metricsFor).
	sess.metrics = s.metricsFor(project)
	// Буфер логов рантайма и подписка на них инструмента ReadAppLogs: строки
	// идут и в кольцевой буфер (UI/REST), и в шину (живая трансляция).
	sess.appLog = &appLogBuffer{}
	sess.registerAppLogSink(context.Background())
	// Ф-1 «спроси пользователя»: состояние активного структурированного
	// вопроса.
	sess.pending = appask.NewPending(nil)
	// Cooldown повторного разбора логов (кнопка «Разобрать»).
	sess.logCooldown = applogs.NewCooldown(logAnalysisCooldown)
	// Ф-5/Р-2: оркестратор фоновой индексации RAG (single-flight по ветке,
	// отчёты в лог/чат). Прогоны строит сервер (см. ragindex.go/ragref.go).
	sess.bgIndex = apprag.NewBackgroundIndexer(project, ragIndexSink{sess: sess}, sess.newProjectIndexer)
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
	sess.emit(ProjectEvent{Type: EventChatUpdated})
}

// listeners регистрирует внутренних слушателей событий проекта на шине (Ф-2).
func (s *Server) listeners(project string, sess *Session) {
}

// continueTaskText формирует текст задачи оркестрации.
func (sess *Session) continueTaskText(ctx context.Context) (string, error) {
	return "Продолжить работу над задачами", nil
}

// start запускает оркестрацию — stub без доски и раннера.
func (sess *Session) start(ctx context.Context, taskText string, provider usecases.FullCycle, boardOnly bool) error {
	sess.mu.Lock()
	if sess.running {
		sess.mu.Unlock()
		return errf("проект %s: оркестрация уже идёт", sess.project)
	}
	sess.running = true
	sess.stopped = false
	sess.log.Infof("[llm] модель запуска: %s", sess.srv.prov.describe())
	cctx, cancel := context.WithCancel(ctx)
	cctx = runevents.WithReporter(cctx, sess.router)
	sess.ctx = cctx
	sess.cancel = func() { cancel() }
	sess.mu.Unlock()

	go func() {
		sess.log.Infof("=== Оркестрация запущена: %s", truncateText(taskText, 120))
		sess.broadcastStatus("running", "")
		defer func() {
			sess.mu.Lock()
			sess.running = false
			sess.stopped = false
			sess.mu.Unlock()
			sess.broadcastStatus("idle", "")
		}()
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
	sess.mu.Unlock()
	if !running {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	// Ждём полной остановки прежнего запуска.
	time.Sleep(50 * time.Millisecond)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	taskText, err := sess.continueTaskText(ctx)
	if err != nil {
		return err
	}
	return sess.start(ctx, taskText, provider, true)
}

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
	sess.mu.Lock()
	if tps > 0 {
		sess.rate = tps
	}
	rate := sess.rate
	sess.mu.Unlock()
	sess.srv.hub.publish(sess.project, "tokens", tokenEvent{Input: in, Output: out, TPS: rate})
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

// boardFlusher — stub: без доски не нужен.
func (sess *Session) boardFlusher(ctx context.Context) {
	defer sess.wg.Done()
	<-ctx.Done()
}

// publishBoard — stub.
func (sess *Session) publishBoard(ctx context.Context) {
}

// publishBoardNow — stub.
func (sess *Session) publishBoardNow() {
}

// broadcastSnapshot публикует клиенту снапшот текущего состояния сессии:
// status + board + tokens. Вызывается сразу после WS-подписки — клиент
// получает авторитетное состояние при подключении, а не ждёт следующего
// события (иначе открытие/рефреш проекта с уже идущей оркестрацией показали
// бы устаревший «idle» до первой смены статуса).
func (sess *Session) broadcastSnapshot() {
	sess.mu.Lock()
	running, standby := sess.running, sess.standby
	sess.mu.Unlock()
	sess.srv.hub.publish(sess.project, "status", statusEvent{
		Status: computeStatus(running),
		Detail: statusDetail(running, false, standby),
	})
	sess.mu.Lock()
	rate := sess.rate
	sess.mu.Unlock()
	sess.srv.hub.publish(sess.project, "tokens", tokenEvent{TPS: rate})
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
	if was == v {
		return
	}
	if v {
		sess.log.Infof("[оркестрация] на доске нет работы — режим ожидания")
		sess.broadcastStatus("standby", standbyReason)
	} else if running {
		sess.log.Infof("[оркестрация] работа появилась — продолжаю")
		sess.broadcastStatus("running", "")
	}
}

// broadcastStatus шлёт статус сессии в шину (type=status).
func (sess *Session) broadcastStatus(status, detail string) {
	sess.srv.hub.publish(sess.project, "status", statusEvent{
		Status: status,
		Detail: detail,
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

// truncateText обрезает длинный текст для статусных сообщений.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

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
func (s *Session) SessionInjections() []domain.Injection {
	return s.injections().List()
}

// AddSessionInjection добавляет инъекцию в сессию (hot apply): применяется
// со СЛЕДУЮЩЕГО запроса к модели в этой сессии.
func (s *Session) AddSessionInjection(inj domain.Injection) {
	s.injections().Add(inj)
}

// UpsertSessionInjection добавляет инъекцию в сессию, перезаписывая запись с
// тем же id (правило — в domain.InjectionSet).
func (s *Session) UpsertSessionInjection(inj domain.Injection) error {
	return s.injections().Upsert(inj)
}

// RemoveSessionInjection удаляет инъекцию по ID из сессии и сообщает, была ли она.
func (s *Session) RemoveSessionInjection(id string) bool {
	return s.injections().Remove(id)
}

var _ = workspace.KindTemp // связь с реестром для будущих эндпоинтов регистрации

// errf конструирует ошибку для ответов API.
func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }
