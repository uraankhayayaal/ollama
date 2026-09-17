package server

import (
	"ai/agents/architect"
	"ai/board"
	"ai/chat"
	"ai/forges"
	"ai/gitops"
	"ai/logging"
	"ai/models"
	"ai/tools"
	"ai/workspace"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Config — параметры HTTP-сервера Web UI.
type Config struct {
	Addr           string // по умолчанию 127.0.0.1:8090; переопределяется AI_WEB_ADDR
	WorkspacesPath string // путь к файлу workspace-реестра (пусто → DefaultPath)

	// Password — пароль Web UI (AI_WEB_PASSWORD). Пустой — аутентификация
	// отключена (сервер предполагается на 127.0.0.1). Задан — Web UI требует
	// вход (httpOnly-сессия + CSRF + rate-limit).
	Password string

	// GitExec — исполнитель git-команд (по умолчанию git CLI). Переопределяется
	// в тестах fake-исполнителем для hermetic-прогона.
	GitExec gitops.Executor

	// ForgeFactory создаёт провайдера форджа по git-remote (по умолчанию —
	// forges.NewByRemote). Переопределяется в тестах стабом без сети.
	ForgeFactory func(remoteURL, token string) (forges.Forge, error)
}

// Server — HTTP+WS сервер Web UI. Хранит реестр проектов, WebSocket-хаб
// и сессии (сингл-флайт: одна активная оркестрация на проект).
type Server struct {
	cfg  Config
	reg  *workspace.Registry
	hub  *Hub
	prov providerResolve

	gitExec      gitops.Executor
	forgeFactory func(remoteURL, token string) (forges.Forge, error)

	// auth — аутентификация Web UI (nil/отключена, если пароль не задан).
	auth *authManager
	// Лимитеры (Ф-3): общий API, строгий на вход, отдельный на чат.
	apiLim   *rateLimiter
	loginLim *rateLimiter
	chatLim  *rateLimiter

	mu       sync.Mutex
	sessions map[string]*Session

	// diffMu защищает базы «точек отхода»: baseline-снимки не-git проектов
	// (baselines) и кэш разобранных диффов git-проектов (diffs, Ф-3).
	diffMu    sync.Mutex
	baselines map[string]*tools.Snap
	diffs     map[string]*cachedDiff
}

// NewServer создаёт сервер. Реестр workspace открывается по cfg.WorkspacesPath.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:8090"
	}
	reg, err := workspace.Open(cfg.WorkspacesPath)
	if err != nil {
		return nil, fmt.Errorf("server: open workspace registry: %w", err)
	}
	s := &Server{
		cfg:       cfg,
		reg:       reg,
		hub:       NewHub(),
		sessions:  make(map[string]*Session),
		baselines: make(map[string]*tools.Snap),
		diffs:     make(map[string]*cachedDiff),
		auth:      newAuth(cfg.Password),
		// Лимиты Ф-3: 5 логинов/мин, 120 API-запросов/мин, 30 сообщений/мин.
		apiLim:   newRateLimit(120, time.Minute),
		loginLim: newRateLimit(5, time.Minute),
		chatLim:  newRateLimit(30, time.Minute),
	}
	s.gitExec = cfg.GitExec
	if s.gitExec == nil {
		s.gitExec = gitops.CLIExecutor{}
	}
	s.forgeFactory = cfg.ForgeFactory
	if s.forgeFactory == nil {
		s.forgeFactory = forges.NewByRemote
	}
	return s, nil
}

// Run запускает HTTP+WS сервер с graceful shutdown. Блокирует до завершения.
func (s *Server) Run(ctx context.Context) error {
	httpSrv := &http.Server{Addr: s.cfg.Addr, Handler: s.routes()}

	log.Printf("server: слушаю http://%s", s.cfg.Addr)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()

	select {
	case <-ctx.Done():
	case <-quit:
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	}

	log.Println("server: завершение...")
	_ = httpSrv.Shutdown(context.Background())
	s.hub.CloseAll()
	return nil
}

// routes собирает HTTP-маршруты: REST, WebSocket, статика Web UI.
// При включённой аутентификации (AI_WEB_PASSWORD) всё /api/* защищается
// middleware'ом authHandler (сессии + CSRF + rate-limit).
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Аутентификация (Ф-3): вход/выход/статус.
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth", s.handleAuthStatus)

	// REST — проекты
	mux.HandleFunc("GET /api/projects", s.handleListProjects)
	mux.HandleFunc("POST /api/projects", s.handleOpenProject)

	// REST — доска/чат/сессия проекта
	mux.HandleFunc("GET /api/projects/{id}", s.handleGetBoard)
	mux.HandleFunc("POST /api/projects/{id}/chat", s.handlePostChat)
	mux.HandleFunc("GET /api/projects/{id}/chat", s.handleChatHistory)
	mux.HandleFunc("POST /api/projects/{id}/{gate}/decide", s.handleGateDecide)
	mux.HandleFunc("POST /api/projects/{id}/session/stop", s.handleStop)
	mux.HandleFunc("PUT /api/projects/{id}/tasks/{tid}", s.handleUpdateTask)
	mux.HandleFunc("GET /api/projects/{id}/bugs", s.handleListBugs)

	// Git (Ф-2-3): дифф, приёмка «Принять → MR», отклонение ветки.
	mux.HandleFunc("GET /api/projects/{id}/diff", s.handleGetDiff)
	mux.HandleFunc("POST /api/projects/{id}/accept", s.handleAccept)
	mux.HandleFunc("POST /api/projects/{id}/reject-branch", s.handleRejectBranch)

	// WebSocket
	mux.HandleFunc("GET /api/projects/{id}/ws", s.handleWS)

	// Статика Web UI (embed web/dist)
	mux.Handle("/", staticHandler())

	return &authHandler{
		next:     mux,
		auth:     s.auth,
		apiLim:   s.apiLim,
		loginLim: s.loginLim,
		chatLim:  s.chatLim,
	}
}

// --- SessionRegistry (single-flight) ---

// getOrCreate возвращает сессию проекта. Если сессия уже есть — возвращает её.
// Если нет — создаёт новую (без запуска runner'а). Возвращает true, если
// сессия была создана.
func (s *Server) getOrCreate(project string) (*Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[project]; ok {
		return sess, false, nil
	}
	sess, err := s.newSession(project)
	if err != nil {
		return nil, false, err
	}
	s.sessions[project] = sess
	return sess, true, nil
}

// session возвращает сессию проекта или nil, если не создана.
func (s *Server) session(project string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[project]
}

// --- утилиты ---

// projectMeta собирает ProjectMeta для REST API.
func projectMeta(inf workspace.Info, meta *board.Meta, running, gating bool) map[string]any {
	status := "idle"
	if running {
		status = "running"
	}
	if gating {
		status = "waiting"
	}
	if meta != nil {
		switch meta.Status {
		case board.StatusDone:
			status = "done"
		case board.StatusInProgress:
			if !running {
				status = "in_progress"
			}
		}
	}
	out := map[string]any{
		"project_name": inf.Name,
		"kind":         inf.Kind,
		"task":         "",
		"status":       status,
		"created_at":   "",
		"updated_at":   "",
	}
	if inf.GitRemote != "" {
		out["git_remote"] = inf.GitRemote
	}
	if inf.GitBranch != "" {
		out["git_branch"] = inf.GitBranch
	}
	if inf.GitBase != "" {
		out["git_base"] = inf.GitBase
	}
	if meta != nil {
		out["task"] = meta.Task
		out["created_at"] = meta.CreatedAt
		out["updated_at"] = meta.UpdatedAt
	}
	return out
}

// provider возвращает LLM-провайдер (ленивая инициализация). Возвращает
// ошибку, если провайдер не настроен (LLM_PROVIDER).
func (s *Server) provider() (models.LLMProvider, error) {
	p, _, err := s.prov.get()
	if err != nil {
		return nil, err
	}
	return p, nil
}

// writeJSON записывает JSON-ответ.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr записывает ошибку в формате {message}.
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"message": msg})
}

// decodeBody декодирует JSON из тела запроса.
func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// --- REST: проекты ---

// handleListProjects возвращает список зарегистрированных проектов.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []map[string]any

	for _, inf := range s.reg.List() {
		// Пытаемся прочитать мета доски (если Redis доступен).
		meta, _ := func() (*board.Meta, error) {
			store, err := board.NewStore(ctx, architect.LoadConfig().StoreConfig(inf.Name))
			if err != nil {
				return nil, err
			}
			defer store.Close()
			return store.GetMeta(ctx)
		}()

		sess := s.session(inf.Name)
		running, gating := false, false
		if sess != nil {
			sess.mu.Lock()
			running = sess.running
			gating = sess.gating
			sess.mu.Unlock()
		}
		out = append(out, projectMeta(inf, meta, running, gating))
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOpenProject открывает/регистрирует проект по пути или git-URL.
func (s *Server) handleOpenProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PathOrGit string `json:"path_or_git"`
		GitURL    string `json:"git_url"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	// git-URL → клон в temp/<имя> с фича-веткой (Ф-2-3).
	if body.GitURL != "" {
		s.handleOpenGitProject(w, r, body.GitURL)
		return
	}

	path := body.PathOrGit
	if path == "" {
		writeErr(w, http.StatusBadRequest, "укажите path_or_git или git_url")
		return
	}

	// Проверяем: может, проект уже зарегистрирован?
	if inf, err := s.reg.Get(path); err == nil {
		// Уже есть — возвращаем meta.
		s.writeProjectMeta(w, r, inf.Name)
		return
	}

	// Пробуем определить тип: существующий каталог → KindDir; иначе → KindTemp.
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		// Каталог существует → dir.
		name := dirBase(path)
		if name == "" || name == "." || name == ".." {
			writeErr(w, http.StatusBadRequest, "невозможно определить имя проекта из пути")
			return
		}
		// Если имя занято — генерируем суффикс.
		if _, gerr := s.reg.Get(name); gerr == nil {
			name = name + "-dir"
		}
		inf, err := s.reg.Add(workspace.AddParams{
			Name:    name,
			Kind:    workspace.KindDir,
			Root:    path,
			Confirm: true,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, "ошибка регистрации: "+err.Error())
			return
		}
		s.writeProjectMetaFromInfo(w, inf)
		return
	}

	// Иначе — temp-проект (имя = path).
	name := path
	inf, err := s.reg.Add(workspace.AddParams{
		Name: name,
		Kind: workspace.KindTemp,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ошибка регистрации: "+err.Error())
		return
	}
	s.writeProjectMetaFromInfo(w, inf)
}

// writeProjectMeta возвращает meta проекта по имени.
func (s *Server) writeProjectMeta(w http.ResponseWriter, r *http.Request, name string) {
	inf, err := s.reg.Get(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	s.writeProjectMetaFromInfo(w, inf)
}

// writeProjectMetaFromInfo возвращает ProjectMeta из Info.
func (s *Server) writeProjectMetaFromInfo(w http.ResponseWriter, inf workspace.Info) {
	// Пытаемся прочитать meta доски.
	meta, _ := func() (*board.Meta, error) {
		store, err := board.NewStore(context.Background(), architect.LoadConfig().StoreConfig(inf.Name))
		if err != nil {
			return nil, err
		}
		defer store.Close()
		return store.GetMeta(context.Background())
	}()
	sess := s.session(inf.Name)
	running, gating := false, false
	if sess != nil {
		sess.mu.Lock()
		running = sess.running
		gating = sess.gating
		sess.mu.Unlock()
	}
	writeJSON(w, http.StatusOK, projectMeta(inf, meta, running, gating))
}

// dirBase возвращает последний компонент пути.
func dirBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

// --- REST: доска ---

// handleGetBoard возвращает снимок доски (meta + epics + tasks + bugs).
// Ф-3: пагинация через ?limit=&offset= — ограничивает каждую секцию и
// добавляет полные счётчики в total.
func (s *Server) handleGetBoard(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	ctx := r.Context()

	limit, offset := 0, 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if intval, err := strconv.Atoi(l); err == nil && intval > 0 {
			limit = intval
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if intval, err := strconv.Atoi(o); err == nil && intval > 0 {
			offset = intval
		}
	}

	// Сначала пробуем сессию (board store уже открыт).
	if sess := s.session(project); sess != nil {
		snap, err := boardViewPage(ctx, sess.board, limit, offset)
		if err == nil {
			writeJSON(w, http.StatusOK, snap)
			return
		}
	}

	// Фолбек: открываем store на лету.
	store, err := board.NewStore(ctx, architect.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()
	snap, err := boardViewPage(ctx, store, limit, offset)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// --- REST: чат ---

// handlePostChat принимает сообщение и запускает/продолжает оркестрацию.
func (s *Server) handlePostChat(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeBody(r, &body); err != nil || body.Message == "" {
		writeErr(w, http.StatusBadRequest, "поле message обязательно")
		return
	}

	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}

	// Пользовательское сообщение → чат (для истории).
	if _, err := sess.chat.Append(context.Background(), chat.Message{
		Role:    chat.RoleUser,
		Content: body.Message,
		Agent:   "user",
	}); err != nil {
		logging.Warnf("server: append user msg %s: %v", project, err)
	}

	// Публикуем в шину для live-таймлайна.
	sess.srv.hub.Publish(project, "chat", chat.Message{
		Role:    chat.RoleUser,
		Content: body.Message,
		Agent:   "user",
		Time:    time.Now().UTC(),
	})

	// LLM-провайдер обязателен для запуска.
	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}

	// Запускаем runner.
	if err := sess.start(context.Background(), body.Message, prov); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChatHistory возвращает историю диалога.
func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	limit := int64(200)
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	msgs, err := sess.chat.History(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, msgs)
}

// --- REST: HITL ---

// handleGateDecide принимает решение человека по затвору (epics/tasks).
func (s *Server) handleGateDecide(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	gateName := r.PathValue("gate")

	if gateName != "epics" && gateName != "tasks" {
		writeErr(w, http.StatusBadRequest, "gate: epics или tasks")
		return
	}

	var body struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	sess := s.session(project)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "сессия не найдена")
		return
	}
	if err := sess.approve(gateName, body.Approved, body.Reason); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleStop останавливает текущую оркестрацию.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess := s.session(project)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "сессия не найдена")
		return
	}
	sess.stop()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- REST: задачи (Ф-2, минимум для фронта) ---

// handleUpdateTask обновляет поля задачи (PATCH через PUT).
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	var patch map[string]any
	if err := decodeBody(r, &patch); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	store, err := board.NewStore(r.Context(), architect.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	t, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "задача не найдена")
		return
	}

	// Применяем патч.
	if v, ok := patch["status"].(string); ok {
		if err := store.SetTaskStatus(r.Context(), taskID, board.Status(v)); err != nil {
			var stErr *board.StatusError
			if errors.As(err, &stErr) {
				writeErr(w, http.StatusBadRequest, stErr.Error())
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Перечитываем.
		t, _ = store.GetTask(r.Context(), taskID)
	}
	if v, ok := patch["title"].(string); ok {
		t.Title = v
	}
	if v, ok := patch["description"].(string); ok {
		t.Description = v
	}
	if v, ok := patch["assignee"].(string); ok {
		t.Assignee = v
	}
	if v, ok := patch["epic_id"].(string); ok {
		if v != t.EpicID {
			if err := store.MoveTask(r.Context(), taskID, v); err != nil {
				writeErr(w, http.StatusBadRequest, "перенос в эпик: "+err.Error())
				return
			}
		}
	}

	if err := store.SaveTask(r.Context(), t); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Публикуем обновлённую доску.
	s.kickBoard(project)
	writeJSON(w, http.StatusOK, t)
}

// handleListBugs возвращает список багрепортов проекта.
func (s *Server) handleListBugs(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	store, err := board.NewStore(r.Context(), architect.LoadConfig().StoreConfig(project))
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	defer store.Close()
	bugs, err := store.ListBugReports(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, bugs)
}

// kickBoard публикует снимок доски (если есть сессия).
func (s *Server) kickBoard(project string) {
	sess := s.session(project)
	if sess != nil {
		sess.kickBoard()
	}
}

// --- WebSocket ---

// handleWS выполняет WS-upgrade и подписывает клиента на проект.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	conn, err := WsUpgrade(w, r)
	if err != nil {
		return // ошибка уже отправлена клиенту
	}
	s.hub.Subscribe(project, conn)
}
