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

	// decide — решение человека по текущему HITL-затвору (буфер 1 позволяет
	// принять решение ДО того, как runner дошёл до затвора).
	decide chan planner.GateDecision

	router  *runevents.Router
	chat    *chat.Store
	board   *board.Store
	ctx     context.Context
	ticks   chan struct{} // сигнал «доска могла измениться» для флашера
	wg      sync.WaitGroup
	stopped bool

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
	// Файл проекта открываем сразу: первая запись (и panel логов в Web UI)
	// не должна ждать ленивого создания каталога.
	logging.Attach(project)
	sess := &Session{
		srv:     s,
		project: project,
		decide:  make(chan planner.GateDecision, 1),
		chat:    chatStore,
		board:   boardStore,
		ticks:   make(chan struct{}, 64),
		log:     logging.For(project),
	}
	sess.router = runevents.NewRouter(func(ev runevents.Event) {
		sess.chatEvent(ev)
		// Потоковые фрагменты не меняют доску — не дёргаем флашер на каждый токен.
		if ev.Type != runevents.TypeMessageDelta {
			sess.kickBoard()
		}
	})
	return sess, nil
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
	sess.ctx = cctx
	sess.cancel = func() { cancel() }
	sess.mu.Unlock()

	sess.log.Infof("=== Оркестрация запущена: %s", truncateText(taskText, 120))
	sess.append(chat.RoleStatus, "Оркестрация запущена: "+truncateText(taskText, 120), "", "", nil)
	sess.broadcastStatus("running", "")

	// Флашер доски: раз в 500 мс публикует снимок доски, если были изменения.
	sess.wg.Add(1)
	go sess.boardFlusher(cctx)

	runner := planner.NewKanbanRunner(provider, sess.board)
	runner.SetGate(sess)

	sess.wg.Add(1)
	go func() {
		defer sess.wg.Done()
		err := runner.Run(cctx, sess.project, taskText)
		sess.mu.Lock()
		sess.running = false
		sess.gating = false
		sess.gateTyp = ""
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
	}
}

// append пишет сообщение в чат (стяжку) и транслирует в шину (type=chat).
func (sess *Session) append(role chat.Role, content, agent, tool string, ok *bool) {
	m := chat.Message{Role: role, Content: content, Agent: agent, Tool: tool, OK: ok}
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
func (sess *Session) boardFlusher(ctx context.Context) {
	defer sess.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
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
		}
	}
}

// publishBoard шлёт снимок доски в шину (type=board).
func (sess *Session) publishBoard(ctx context.Context) {
	v, err := boardView(ctx, sess.board)
	if err != nil {
		return
	}
	sess.srv.hub.publish(sess.project, "board", v)
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
	Status string `json:"status"`           // running|waiting|done|stopped|error
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
	Meta  *board.Meta       `json:"meta,omitempty"`
	Epics []*board.Epic     `json:"epics"`
	Tasks []*board.Task     `json:"tasks"`
	Bugs  []*board.BugReport `json:"bugs"`
	Total *boardTotal       `json:"total,omitempty"` // счётчики всех элементов (Ф-3: пагинация)
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