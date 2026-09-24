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
	"strings"
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
	// (baselines) и кэш разобраных диффов git-проектов (diffs, Ф-3).
	diffMu    sync.Mutex
	baselines map[string]*tools.Snap
	diffs     map[string]*cachedDiff

	// logBroker — перематывает лог-файлы проектов: новые линии шлются
	// в шину проекта (type="log"). Запускается в Run().
	logBroker *logBroker

	// syncMetaMu защищает map syncLocks: по одному мьютексу git-слияний на
	// проект (Ф-2). Слияния веток задач в релизную ветку сериализуются, чтобы
	// параллельные done→merge и явные REST-мёрджи не конкурировали за ветки.
	syncMetaMu sync.Mutex
	mergeLocks map[string]*sync.Mutex
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
	h := NewHub()
	s := &Server{
		cfg:       cfg,
		reg:       reg,
		hub:       h,
		sessions:  make(map[string]*Session),
		baselines: make(map[string]*tools.Snap),
		diffs:     make(map[string]*cachedDiff),
		auth:      newAuth(cfg.Password),
		logBroker: newLogBroker(h),
		// Лимиты Ф-3: 5 логинов/мин, 120 API-запросов/мин, 30 сообщений/мин.
		apiLim:   newRateLimit(120, time.Minute),
		loginLim: newRateLimit(5, time.Minute),
		chatLim:  newRateLimit(30, time.Minute),
		// Ф-2: локи git-слияний — один на проект.
		mergeLocks: make(map[string]*sync.Mutex),
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

	// Фоновый goroutine logBroker: сканирует файлы логов проектов.
	// Обязательно в горутине: Run блокирует до ctx.Done, иначе select ниже
	// (сигналы SIGINT/SIGTERM и ошибка ListenAndServe) недостижим.
	go s.logBroker.Run(ctx)

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
	// Закрываем лог-файлы процесса и всех проектов, чтобы хвосты не потерялись.
	logging.Close()
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
	mux.HandleFunc("DELETE /api/projects/{id}/chat", s.handleClearChat)
	mux.HandleFunc("POST /api/projects/{id}/continue", s.handleContinue)
	mux.HandleFunc("POST /api/projects/{id}/index", s.handleProjectIndex)
	mux.HandleFunc("GET /api/projects/{id}/tokens", s.handleGetTokens)
	mux.HandleFunc("POST /api/projects/{id}/{gate}/decide", s.handleGateDecide)
	mux.HandleFunc("POST /api/projects/{id}/session/stop", s.handleStop)
	// REST — структурированный вопрос ассистента (AskUser, Ф-1 «спроси
	// пользователя»): ответ на один шаг пачки.
	mux.HandleFunc("POST /api/projects/{id}/ask/{askID}/answer", s.handleAskAnswer)
	mux.HandleFunc("PUT /api/projects/{id}/tasks/{tid}", s.handleUpdateTask)
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{tid}", s.handleDeleteTask)
	mux.HandleFunc("DELETE /api/projects/{id}/epics/{eid}", s.handleDeleteEpic)
	// Git-workflow (Ф-6): кнопки «Пауза»/«Продолжить»/«Отменить» эпика — перевод
	// статуса (каскад задач + git-хуки, ветка/код остаются на месте).
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/status", s.handleSetEpicStatus)
	mux.HandleFunc("GET /api/projects/{id}/bugs", s.handleListBugs)

	// Git (Ф-2-3): дифф, приёмка «Принять → MR», отклонение ветки.
	mux.HandleFunc("GET /api/projects/{id}/diff", s.handleGetDiff)
	mux.HandleFunc("POST /api/projects/{id}/accept", s.handleAccept)
	mux.HandleFunc("POST /api/projects/{id}/reject-branch", s.handleRejectBranch)

	// Git-workflow (Ф-1): ветки эпиков и задач.
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/branch", s.handleCreateEpicBranch)
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/branch", s.handleCreateTaskBranch)
	// Git-workflow (Ф-2): мёрдж фича-ветки задачи в релизную ветку эпика.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/merge", s.handleMergeTask)
	// Git-workflow (Ф-3): кнопка «Залить в main» — релизная ветка эпика → main.
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/release", s.handleReleaseEpic)
	// Git-workflow (Ф-4): авто-резолв конфликтов «main ↔ релизная ветка».
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/rebase", s.handleEpicRebase)
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/resolve", s.handleEpicResolve)
	mux.HandleFunc("GET /api/projects/{id}/epics/{eid}/resolve", s.handleResolveStatus)
	// Git-workflow (Ф-5): «Создать MR» — push ветки эпика/задачи в remote и MR.
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/mr", s.handleCreateEpicMR)
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/mr", s.handleCreateTaskMR)

	// Логи проекта (панель «Логи», см. logs.go).
	mux.HandleFunc("GET /api/projects/{id}/logs", s.handleGetLogs)

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

// computeStatus собирает статус проекта из состояния оркестрации и доски.
// Единая точка правды: используется и в projectMeta (REST), и в снапшоте
// сессии при WS-подключении (server/session.go), чтобы клиент всегда видел
// одно и то же состояние.
// standby — режим ожидания работы (запуск по кнопке, на доске ничего нет):
// сессия жива, но ничего не исполняет, пока не появится работа. Не перекрывает
// gating (HITL-затвор важнее ожидания работы).
func computeStatus(running, gating, standby bool, meta *board.Meta) string {
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
	if standby && running && !gating {
		status = "standby"
	}
	return status
}

// standbyReason — причина режима ожидания оркестрации. Единая строка для
// broadcastStatus, снапшота при WS-подключении и REST-меты: фронт должен видеть
// одно объяснение, откуда бы статус ни пришёл (Ф-3, PLAN-done-dashboard-events).
const standbyReason = "нет работы на доске — жду эпики и задачи"

// statusDetail — человекочитаемая причина текущего состояния (поле detail
// рядом со status). Сейчас единственный случай, где компоненты-потребители
// (кнопка Стоп/Продолжить, список проектов) нуждаются в причине — standby:
// сессия активна, но не исполняет работу, и UI объясняет это пользователю.
func statusDetail(running, gating, standby bool) string {
	if standby && running && !gating {
		return standbyReason
	}
	return ""
}

// projectMeta собирает ProjectMeta для REST API.
func projectMeta(inf workspace.Info, meta *board.Meta, running, gating, standby bool) map[string]any {
	status := computeStatus(running, gating, standby, meta)
	out := map[string]any{
		"project_name": inf.Name,
		"kind":         inf.Kind,
		"task":         "",
		"status":       status,
		"created_at":   "",
		"updated_at":   "",
	}
	if detail := statusDetail(running, gating, standby); detail != "" {
		out["status_detail"] = detail
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

// sessionFlags возвращает флаги состояния сессии проекта (running/gating/
// standby). Используется там, где статус читается из projectMeta без снапшота:
// list/meta проектов по REST.
func (s *Server) sessionFlags(project string) (running, gating, standby bool) {
	sess := s.session(project)
	if sess == nil {
		return false, false, false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.running, sess.gating, sess.standby
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

		running, gating, standby := s.sessionFlags(inf.Name)
		out = append(out, projectMeta(inf, meta, running, gating, standby))
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOpenProject открывает/регистрирует проект по пути или git-URL.
// Единое поле path_or_git: git-ссылка (схема http(s)/ssh/git или scp-форма
// git@host:path) уходит в клон, локальный путь — в обычную регистрацию.
func (s *Server) handleOpenProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PathOrGit string `json:"path_or_git"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	path := strings.TrimSpace(body.PathOrGit)
	if path == "" {
		writeErr(w, http.StatusBadRequest, "укажите путь к папке или git-URL")
		return
	}

	// git-URL → клон в temp/<имя> с фича-веткой (Ф-2-3).
	if isGitURL(path) {
		s.handleOpenGitProject(w, r, path)
		return
	}

	// Проверяем: может, проект уже зарегистрирован?
	if inf, err := s.reg.Get(path); err == nil {
		// Уже есть — возвращаем meta (база диффа фиксируется при переоткрытии).
		s.ensureBaseline(inf)
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
		// «Точка отхода» фиксируется сразу при открытии — изменения,
		// сделанные агентом до первого просмотра Diffboard, иначе были бы
		// приняты за базу и не показались бы в диффе.
		s.ensureBaseline(inf)
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
	running, gating, standby := false, false, false
	if sess != nil {
		running, gating, standby = s.sessionFlags(inf.Name)
	}
	writeJSON(w, http.StatusOK, projectMeta(inf, meta, running, gating, standby))
}

// isGitURL определяет, что ввод — git-ссылка, а не путь к локальной папке:
// HTTP(S)/SSH/git-схема или scp-форма git@host:path.
func isGitURL(s string) bool {
	low := strings.ToLower(s)
	for _, p := range []string{"http://", "https://", "ssh://", "git://", "git@"} {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
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
			snap.Git = s.gitStatus(ctx, project, snap.Epics, snap.Tasks)
			s.reconcileMRsAsync(project)
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
	snap.Git = s.gitStatus(ctx, project, snap.Epics, snap.Tasks)
	s.reconcileMRsAsync(project)
	writeJSON(w, http.StatusOK, snap)
}

// --- REST: чат ---

// handlePostChat принимает сообщение. Чат работает ПАРАЛЛЕЛЬНО доске: сообщение
// идёт единому ассистенту (Ф-1), который сам по смыслу решает — создать
// эпик/задачу/баг инструментами Board* или ответить текстом. Бинарный сплит
// по ключевым словам убран: оркестрацию ассистент не запускает (берёт кнопка
// «Продолжить»).
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

	// Пользовательское сообщение → чат (для истории) и в лог проекта.
	sess.log.Infof("[чат] пользователь: %s", truncateText(body.Message, 300))
	if _, err := sess.chat.Append(context.Background(), chat.Message{
		Role:    chat.RoleUser,
		Content: body.Message,
		Agent:   "user",
	}); err != nil {
		sess.log.Warnf("server: append user msg %s: %v", project, err)
	}

	// Публикуем в шину для live-таймлайна.
	sess.srv.hub.Publish(project, "chat", chat.Message{
		Role:    chat.RoleUser,
		Content: body.Message,
		Agent:   "user",
		Time:    time.Now().UTC(),
	})

	// Единый ассистент: решает по смыслу (создание эпика/задачи/бага — через
	// его board-инструменты, а не по регэкспепу).
	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}
	sess.log.Infof("[чат] маршрут: единый ассистент (действия по смыслу, без сплита)")
	sess.runChatAssistant(context.Background(), body.Message, prov)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleContinue запускает/возобновляет оркестрацию на текущей доске (кнопка
// «Продолжить»). Раннер работает в board-only режиме: в чат ничего не
// отправляется, новые эпики не создаются — берутся в работу записи, которые уже
// лежат на доске (эпики без задач декомпозируются лидами). Если брать в работу
// нечего, оркестрация переходит в режим ожидания и ждёт появления работы (см.
// Session.standby).
func (s *Server) handleContinue(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")

	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}

	ctx := context.Background()
	// Текст задачи для раннера: приоритет meta-задачи проекта; если её нет
	// (задачи добавлены чатом/вручную до первого запуска) — берём обобщённое
	// описание доски. Пустая доска не ошибка: запуск уходит в режим ожидания.
	taskText, err := sess.continueTaskText(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "чтение доски: "+err.Error())
		return
	}

	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}

	if err := sess.start(ctx, taskText, prov); err != nil {
		sess.log.Warnf("[continue] запуск оркестрации отклонён: %v", err)
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleProjectIndex — фоновая индексация RAG-памяти проекта по кнопке в Web
// UI (опциональный нюанс Ф-5, PLAN-2026-09-24-done-architect-intelligence.md,
// Р-2). Вызов не блокирует цикл: Session.IndexBackground запускает горутину;
// результат (файлы/чанки) уходит в лог и chat.RoleStatus. Повтор при уже
// идущей индексации — 409; недоступный клиент RAG (нет Qdrant/модели) — 503.
func (s *Server) handleProjectIndex(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}
	if err := sess.IndexBackground(r.Context()); err != nil {
		sess.log.Warnf("[index] фоновая индексация %s: %v", project, err)
		msg := err.Error()
		if strings.Contains(msg, "уже запущена") {
			writeErr(w, http.StatusConflict, msg)
			return
		}
		writeErr(w, http.StatusServiceUnavailable, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "Индексация RAG-индекса запущена в фоне",
	})
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

// handleClearChat («кофе-брейк») стирает историю диалога, чтобы UI и модель
// начали общение с чистого листа. Стрим удаляется целиком; клиентам шлётся
// событие chat_clear (они очищают локальный массив сообщений), а в свежий
// стрим пишется системная пометка о начале нового диалога. Она не попадает
// в контекст модели (chatDialogueHistory игнорирует роль system).
func (s *Server) handleClearChat(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := sess.chat.Clear(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, "очистка чата: "+err.Error())
		return
	}
	// Знак для остальных клиентов: стрим пуст, стирайте локальный массив.
	sess.srv.hub.Publish(project, "chat_clear", map[string]bool{"ok": true})
	sess.append(chat.RoleSystem, "Кофе-брейк: диалог очищен, начинаем общение с чистого листа.", "system", "", nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetTokens возвращает накопленные токены проекта (вход/выход) и
// последнюю скорость генерации (вых. ток/с).
func (s *Server) handleGetTokens(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}
	in, out, err := sess.tok.Get(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sess.mu.Lock()
	rate := sess.rate
	sess.mu.Unlock()
	writeJSON(w, http.StatusOK, tokenEvent{Input: in, Output: out, TPS: rate})
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

// handleAskAnswer принимает ответ пользователя на один шаг пачки вопросов
// ассистента (AskUser): {question_id, selected, custom} → {ok, answered, total}.
// Когда отвечены все вопросы, AnswerAsk разблокирует агентский цикл.
func (s *Server) handleAskAnswer(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	askID := r.PathValue("askID")

	var body struct {
		QuestionID string   `json:"question_id"`
		Selected   []string `json:"selected"`
		Custom     string   `json:"custom"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}
	if body.QuestionID == "" {
		writeErr(w, http.StatusBadRequest, "question_id обязателен")
		return
	}

	sess := s.session(project)
	if sess == nil {
		writeErr(w, http.StatusNotFound, "сессия не найдена")
		return
	}
	answered, total, err := sess.AnswerAsk(askID, body.QuestionID, body.Selected, body.Custom)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "answered": answered, "total": total})
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
	// Ф-1..Ф-4: авто-действия git-workflow при изменениях доски (ветки при
	// создании, мёрдж done→релиз, worktree задачи, синхрон с main).
	s.attachGitHooks(project, store)

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
	s.srvEmitBoard(project, "REST: задача обновлена")
	writeJSON(w, http.StatusOK, t)
}

// handleDeleteTask удаляет задачу с доски (Ф-3). Задачи в работе/done/cancelled
// удалять нельзя — Store.DeleteTask вернёт ошибку. Для git-проектов снимается
// и запись ветки задачи из side-реестра.
func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	store, err := board.NewStore(r.Context(), architect.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	if err := store.DeleteTask(r.Context(), taskID); err != nil {
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "задача не найдена")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Git-workflow (Ф-1): снимаем из side-реестра ветку задачи.
	s.removeTaskBranch(project, taskID)

	// Публикуем обновлённую доску.
	s.srvEmitBoard(project, "REST: задача удалена")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// removeTaskBranch снимает из side-реестра ветку задачи git-проекта (Ф-3).
// No-op для не-git проектов и незаведённых веток — несуществующую ветку
// реестр и так не возвращает.
func (s *Server) removeTaskBranch(project, taskID string) {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	if err := s.reg.DeleteTaskBranch(project, taskID); err != nil {
		logging.For(project).Warnf("gitflow: снятие ветки задачи %s: %v", taskID, err)
	}
	if err := s.reg.DeleteTaskMR(project, taskID); err != nil {
		logging.For(project).Warnf("gitflow: снятие MR задачи %s: %v", taskID, err)
	}
	s.invalidateDiffs(project)
}

// handleSetEpicStatus — REST-перевод эпика в новый статус (кнопки «Пауза»/
// «Продолжить»/«Отменить», Ф-6). Эпики изолированы в своих git-ветках,
// поэтому пауза/отмена не откатывает код: работа просто приостанавливается
// (статус paused, задачи каскадом на паузу) либо запись помечается отменённой,
// а ветка остаётся как артефакт. Работает через Store.SetEpicStatus (FSM +
// каскад) с подключёнными git-хуками (done эпика по-прежнему синхронит
// релизную ветку с main).
func (s *Server) handleSetEpicStatus(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	var body struct {
		Status string `json:"status"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}
	st := board.Status(strings.TrimSpace(body.Status))
	if !st.Valid() {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("неизвестный статус %q", string(body.Status)))
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()

	if err := store.SetEpicStatus(r.Context(), epicID, st); err != nil {
		var stErr *board.StatusError
		if errors.As(err, &stErr) {
			writeErr(w, http.StatusBadRequest, stErr.Error())
			return
		}
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "эпик не найден")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	epic, _ := store.GetEpic(r.Context(), epicID)
	logging.For(project).Infof("доска: эпик %s → %s (через REST)", epicID, st)
	s.srvEmitBoard(project, "REST: эпик сменил статус")
	writeJSON(w, http.StatusOK, epic)
}

// handleDeleteEpic удаляет эпик вместе с его задачами. Допустимо только для
// эпиков, задачи которых ещё не взяты в работу специалистами (статусы
// new/analysis/ready); иначе Store.DeleteEpic вернёт ошибку.
func (s *Server) handleDeleteEpic(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	store, err := board.NewStore(r.Context(), architect.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	// Список задач эпика нужен до удаления — после DeleteEpic их уже нет.
	var epicTasks []string
	if e, gerr := store.GetEpic(r.Context(), epicID); gerr == nil {
		epicTasks = e.Tasks
	}

	if err := store.DeleteEpic(r.Context(), epicID); err != nil {
		if errors.Is(err, board.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "эпик не найден")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Git-workflow (Ф-1): снимаем из side-реестра ветки эпика и его задач.
	s.deleteEpicBranches(project, epicID, epicTasks)

	// Публикуем обновлённую доску.
	s.srvEmitBoard(project, "REST: эпик удалён")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

// --- WebSocket ---

// handleWS выполняет WS-upgrade и подписывает клиента на проект.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	conn, err := WsUpgrade(w, r)
	if err != nil {
		return // ошибка уже отправлена клиенту
	}
	s.hub.Subscribe(project, conn)
	// Снапшот текущего состояния: клиент сразу видит актуальные status/board/
	// tokens (а не устаревший idle), даже если оркестрация уже идёт.
	// getOrCreate: сессия обязана существовать, пока открыт дашборд — иначе
	// srvEmitBoard (кнопки «ветка/MR/релиз» в UI) некому публиковать после
	// перезапуска сервера, и доска в браузере останется устаревшей (Ф-5).
	if sess, _, err := s.getOrCreate(project); err == nil {
		sess.broadcastSnapshot()
	}
	// Ф-5: при открытии дашборда асинхронно сверяем MR с форджем (ветки,
	// созданные вне UI, и статусы merged/closed у отслеживаемых).
	s.reconcileMRsAsync(project)
}
