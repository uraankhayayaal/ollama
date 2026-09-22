package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"ai/agents/architect"
	"ai/agents/planner"
	"ai/board"
	"ai/chat"
	"ai/logging"
	"ai/models"
	"ai/runevents"
	"ai/tokens"
	"ai/workspace"
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
	gating  bool
	gateTyp string
	// standby — режим ожидания: оркестрация запущена по доске, но брать в
	// работу нечего (нет эпиков/задач, которые можно исполнить). Сессия жива и
	// ждёт появления работы; статус в шину — «standby».
	standby bool

	// decide — решение человека по текущему HITL-затвору (буфер 1 позволяет
	// принять решение ДО того, как runner дошёл до затвора).
	decide chan planner.GateDecision

	// pendingAsk — активный структурированный вопрос ассистента (AskUser):
	// один на сессию, блокирует агентский цикл до ответа на все вопросы.
	pendingAsk *pendingAsk

	router  *runevents.Router
	chat    *chat.Store
	board   *board.Store
	tok     *tokens.Store
	ctx     context.Context
	ticks   chan struct{} // сигнал «доска могла измениться» для флашера
	wg      sync.WaitGroup
	stopped bool

	// rate — последняя реальная скорость генерации (вых. ток/с) из usage
	// провайдера (Ollama eval_count/eval_duration). Не сбрасывается между
	// генерациями: показывает актуальную производительность модели.
	rate float64

	// log — лог проекта (logs/<проект>.log). В режиме serve один процесс ведёт
	// много проектов, поэтому сообщения сессии пишутся в файл своего проекта,
	// а не в общий logs/server.log.
	log *logging.Logger
}

// newSession создаёт сессию проекта (без запуска runner'а).
func (s *Server) newSession(project string) (*Session, error) {
	boardStore, err := board.NewStore(context.Background(), architect.LoadConfig().StoreConfig(project))
	if err != nil {
		return nil, err
	}
	// Ф-1..Ф-4: авто-действия git-workflow при изменениях доски (ветки эпиков/
	// задач при создании, мёрдж done→релиз, worktree задачи, синхрон с main).
	s.attachGitHooks(project, boardStore)
	chatStore, err := chat.NewStore(context.Background(), chat.StoreConfig{
		Addr:     architect.LoadConfig().RedisAddr,
		Password: architect.LoadConfig().RedisPassword,
		DB:       architect.LoadConfig().RedisDB,
		Project:  project,
	})
	if err != nil {
		_ = boardStore.Close()
		return nil, err
	}
	tokStore, err := tokens.NewStore(context.Background(), tokens.StoreConfig{
		Addr:     architect.LoadConfig().RedisAddr,
		Password: architect.LoadConfig().RedisPassword,
		DB:       architect.LoadConfig().RedisDB,
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
		decide:  make(chan planner.GateDecision, 1),
		chat:    chatStore,
		board:   boardStore,
		tok:     tokStore,
		ticks:   make(chan struct{}, 64),
		log:     logging.For(project),
	}
	sess.router = runevents.NewRouter(func(ev runevents.Event) {
		sess.chatEvent(ev)
		// Потоковые фрагменты не меняют доску — не дёргаем флашер на каждый токен.
		if ev.Type != runevents.TypeMessageDelta {
			// Через Server.kickBoard, а не sess.kickBoard: вне оркестрации
			// (idle-чат) boardFlusher не крутится, и обычный тик никто бы не
			// дренул — созданные чатом эпики/задачи/баги не появились бы на
			// доске до ручного обновления страницы.
			sess.srv.kickBoard(sess.project)
		}
	})
	return sess, nil
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
func (sess *Session) start(ctx context.Context, taskText string, provider models.LLMProvider) error {
	sess.mu.Lock()
	if sess.running {
		sess.mu.Unlock()
		return errf("проект %s: оркестрация уже идёт", sess.project)
	}
	sess.running = true
	sess.stopped = false
	cctx, cancel := context.WithCancel(ctx)
	// Репортёр в контексте оркестрации: runner.Generate по нему транслирует
	// текст модели, вызовы инструментов и потребление токенов в живую шину
	// (type=chat / type=tool / type=tokens). Без этого Web UI не видит ни
	// логов раундов, ни счётчика токенов (всё стоит на нулях).
	cctx = runevents.WithReporter(cctx, sess.router)
	sess.ctx = cctx
	sess.cancel = func() { cancel() }
	sess.mu.Unlock()

	sess.log.Infof("=== Оркестрация запущена: %s", truncateText(taskText, 120))
	sess.broadcastStatus("running", "")

	// Флашер доски: раз в 500 мс публикует снимок доски, если были изменения.
	sess.wg.Add(1)
	go sess.boardFlusher(cctx)

	runner := planner.NewKanbanRunner(provider, sess.board)
	runner.SetGate(sess)
	// Запуск по кнопке — board-only: новые эпики не создаются, но записи доски
	// (включая эпики без задач — их декомпозируют лиды) берутся в работу; при
	// отсутствии работы раннер сообщает сессии (standby) и ждёт эпиков/задач.
	runner.SetBoardOnly(true)
	runner.SetStandbyNotifier(sess.setStandby)
	// Ф-3: git-проекты — специалист работает в своём worktree ветки задачи
	// (OutputDir = worktree), поэтому авто-коммит на done соберёт его правки.
	runner.SetOutputDir(sess.srv.taskOutputDir)

	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		err := runner.Run(cctx, sess.project, taskText)
		sess.mu.Lock()
		sess.running = false
		sess.gating = false
		sess.gateTyp = ""
		sess.standby = false
		sess.mu.Unlock()

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

// --- HITL-затворы (реализация planner.HumanGate) ---

func (sess *Session) Epics(ctx context.Context, epics []*board.Epic) (planner.GateDecision, error) {
	return sess.waitGate(ctx, "epics", func() {
		ids := make([]string, 0, len(epics))
		for _, e := range epics {
			ids = append(ids, e.TaskID)
		}
		sess.srv.hub.publish(sess.project, "gate", gateEvent{
			Gate:    "epics",
			Summary: "Ожидается подтверждение эпиков архитектора",
			IDs:     ids,
		})
	})
}

func (sess *Session) Tasks(ctx context.Context, tasks []*board.Task) (planner.GateDecision, error) {
	return sess.waitGate(ctx, "tasks", func() {
		ids := make([]string, 0, len(tasks))
		for _, t := range tasks {
			ids = append(ids, t.TaskID)
		}
		sess.srv.hub.publish(sess.project, "gate", gateEvent{
			Gate:    "tasks",
			Summary: "Ожидается подтверждение задач, готовых к работе",
			IDs:     ids,
		})
	})
}

// gateEvent — событие «нужно подтверждение человека».
type gateEvent struct {
	Gate    string   `json:"gate"` // epics | tasks
	Summary string   `json:"summary"`
	IDs     []string `json:"ids"`
}

// waitGate блокирует runner до решения (approve/reject) или отмены контекста.
// Передаёт статус «ждёт подтверждения» и публикует событие затвора.
func (sess *Session) waitGate(ctx context.Context, typ string, notify func()) (planner.GateDecision, error) {
	sess.mu.Lock()
	if sess.stopped {
		sess.mu.Unlock()
		return planner.GateDecision{}, context.Canceled
	}
	sess.gating = true
	sess.gateTyp = typ
	sess.mu.Unlock()

	// Снимаем возможное решение, присланное ДО затвора (буфер 1), и сообщаем кем.
	select {
	case <-sess.decide:
	default:
	}
	notify()
	sess.broadcastStatus("waiting", typ)

	defer func() {
		sess.mu.Lock()
		sess.gating = false
		sess.gateTyp = ""
		sess.mu.Unlock()
	}()

	select {
	case d := <-sess.decide:
		// approve() уже сбросил gating/gateTyp и положил решение в канал.
		// Возвращаем статус «running»: затвор разрешён, оркестрация идёт.
		sess.broadcastStatus("running", "")
		return d, nil
	case <-ctx.Done():
		return planner.GateDecision{}, ctx.Err()
	}
}

// approve разрешает текущий затвор: approved + reason → решение в канал.
func (sess *Session) approve(typ string, approved bool, reason string) error {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if !sess.running || !sess.gating {
		return errf("проект %s: нет активного затвора (%s)", sess.project, typ)
	}
	if sess.gateTyp != typ {
		return errf("проект %s: ожидается затвор «%s», пришло «%s»", sess.project, sess.gateTyp, typ)
	}
	sess.decide <- planner.GateDecision{Approved: approved, Reason: reason}
	sess.gating = false
	sess.gateTyp = ""
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
	case runevents.TypeTokenCount:
		// Потребление токенов раунда: накапливаем в Redis (за время жизни
		// проекта) и транслируем новые тоталы в шину — фронт обновляет
		// счётчик рядом с кнопкой «Продолжить» в реальном времени.
		sess.addTokens(ev.In, ev.Out, ev.TPS)
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
// скорость) в шину (type=tokens).
func (sess *Session) addTokens(in, out int64, tps float64) {
	totIn, totOut, err := sess.tok.Add(context.Background(), in, out)
	if err != nil {
		sess.log.Warnf("server: счётчик токенов %s: %v", sess.project, err)
		return
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

// kickBoard помечает доску «изменилась» — флашер вскоре опубликует снимок.
func (sess *Session) kickBoard() {
	select {
	case sess.ticks <- struct{}{}:
	default:
	}
}

// boardFlusher раз в 500 мс публикует снимок доски, если были изменения.
// Параллельно, раз в минуту (Ф-5, гибрид), фоном сверяет MR с форджем,
// чтобы состояние MR-кнопок обновлялось и при активной оркестрации без
// перезахода на дашборд.
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
			select {
			case <-sess.ticks:
				sess.publishBoard(ctx)
			default:
			}
		case <-sess.ticks:
			sess.publishBoard(ctx)
		case <-mrTicker.C:
			changed, err := sess.srv.reconcileMRs(ctx, sess.project)
			if err != nil {
				continue
			}
			if changed {
				sess.kickBoard()
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
	running, gating, gateTyp, standby := sess.running, sess.gating, sess.gateTyp, sess.standby
	sess.mu.Unlock()
	meta, _ := sess.board.GetMeta(ctx)
	sess.srv.hub.publish(sess.project, "status", statusEvent{
		Status: computeStatus(running, gating, standby, meta),
		Gating: gating,
		Gate:   gateTyp,
	})
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
	gating := sess.gating
	sess.mu.Unlock()
	if was == v {
		return
	}
	if v {
		sess.log.Infof("[оркестрация] на доске нет работы — режим ожидания")
		sess.broadcastStatus("standby", "нет работы на доске — жду эпики и задачи")
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
	sess.mu.Lock()
	gating := sess.gating
	gateTyp := sess.gateTyp
	sess.mu.Unlock()
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
	Gating bool   `json:"gating,omitempty"`
	Gate   string `json:"gate,omitempty"`
}

// boardView собирает снимок доски (доска проекта или ErrNotFound-доска пуста).
func boardView(ctx context.Context, store *board.Store) (boardSnapshot, error) {
	epics, err := store.ListEpics(ctx)
	if err != nil {
		return boardSnapshot{}, err
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		return boardSnapshot{}, err
	}
	bugs, err := store.ListBugReports(ctx)
	if err != nil {
		return boardSnapshot{}, err
	}
	meta, _ := store.GetMeta(ctx)
	return boardSnapshot{Meta: meta, Epics: epics, Tasks: tasks, Bugs: bugs}, nil
}

// boardSnapshot — полный снимок доски для REST/SSE.
type boardSnapshot struct {
	Meta  *board.Meta        `json:"meta,omitempty"`
	Epics []*board.Epic      `json:"epics"`
	Tasks []*board.Task      `json:"tasks"`
	Bugs  []*board.BugReport `json:"bugs"`
	Total *boardTotal        `json:"total,omitempty"` // счётчики всех элементов (Ф-3: пагинация)
	// Git — git-статус проекта (ветки/MR эпиков и задач, Ф-5). Только для
	// git-проектов с созданными ветками; иначе nil.
	Git *gitView `json:"git,omitempty"`
}

// boardTotal — полные счётчики доски (когда снимок ограничен limit/offset).
type boardTotal struct {
	Epics int `json:"epics"`
	Tasks int `json:"tasks"`
	Bugs  int `json:"bugs"`
}

// boardViewPage ограничивает снимок доски страницей (limit/limit+offset=0 —
// полный снимок) и дополняет его полными счётчиками.
func boardViewPage(ctx context.Context, store *board.Store, limit, offset int) (boardSnapshot, error) {
	v, err := boardView(ctx, store)
	if err != nil {
		return boardSnapshot{}, err
	}
	total := &boardTotal{Epics: len(v.Epics), Tasks: len(v.Tasks), Bugs: len(v.Bugs)}
	if limit <= 0 {
		v.Total = total
		return v, nil
	}
	if offset < 0 {
		offset = 0
	}
	v.Epics = slicePage(v.Epics, offset, limit)
	v.Tasks = slicePage(v.Tasks, offset, limit)
	v.Bugs = slicePage(v.Bugs, offset, limit)
	v.Total = total
	return v, nil
}

// slicePage возвращает подмножество [offset, offset+limit) из слайса любого типа.
func slicePage[T any](in []T, offset, limit int) []T {
	if offset >= len(in) {
		return []T{}
	}
	end := offset + limit
	if end > len(in) {
		end = len(in)
	}
	return in[offset:end]
}

// truncateText обрезает длинный текст для статусных сообщений.
func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = workspace.KindTemp // связь с реестром для будущих эндпоинтов регистрации

// errf конструирует ошибку для ответов API.
func errf(format string, args ...any) error { return fmt.Errorf(format, args...) }
