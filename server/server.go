package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"ai/infrastructure/forges"
	"ai/infrastructure/git"
	"ai/infrastructure/kafkarelay"
	"ai/infrastructure/runmetrics"
	"ai/infrastructure/workspace"
	"ai/shared/logging"
	"ai/tools"
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
	GitExec git.Executor

	// ForgeFactory создаёт провайдера форджа по git-remote (по умолчанию —
	// forges.NewByRemote). Переопределяется в тестах стабом без сети.
	ForgeFactory func(remoteURL, token string) (forges.Forge, error)

	// Relay — публикация шагов агентного цикла в Kafka-топик наблюдаемости
	// (kafkarelay, топик agent-harness-metrics). nil/выключен — шаги не
	// публикуются. Создаётся в main через kafkarelay.FromEnv().
	Relay *kafkarelay.Relay
}

// Server — HTTP+WS сервер Web UI. Хранит реестр проектов, WebSocket-хаб
// и сессии (сингл-флайт: одна активная оркестрация на проект).
type Server struct {
	cfg  Config
	reg  *workspace.Registry
	hub  *Hub
	prov providerResolve

	gitExec      git.Executor
	forgeFactory func(remoteURL, token string) (forges.Forge, error)

	// auth — аутентификация Web UI (nil/отключена, если пароль не задан).
	auth *authManager

	// relay — Kafka-рлей шагов агентного цикла (nil, если выключен). Сессии
	// берут из него SessionSink; Close дренирует очередь при завершении.
	relay *kafkarelay.Relay
	// Лимитеры (Ф-3): общий API, строгий на вход, отдельный на чат.
	apiLim   *rateLimiter
	loginLim *rateLimiter
	chatLim  *rateLimiter

	mu       sync.Mutex
	sessions map[string]*Session

	// metrics — реестры телеметрии агентского цикла по проектам (Ф-1). Живут
	// отдельно от сессий: пересоздание сессии (переподключение WebSocket,
	// рестарт доски) не должно обнулять длительности и счётчики вызовов.
	//
	// Свой мьютекс, а не mu: сессия создаётся под mu (getOrCreate), и она же
	// берёт реестр — общий мьютекс дал бы взаимоблокировку.
	metricsMu sync.Mutex
	metrics   map[string]*runmetrics.Registry

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
		relay:     cfg.Relay,
		sessions:  make(map[string]*Session),
		metrics:   make(map[string]*runmetrics.Registry),
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
		s.gitExec = git.CLIExecutor{}
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

	// Какая модель работает — видно сразу при старте, а не только после
	// первого запроса. Провайдер создаётся лениво, но здесь резолвим принудительно:
	// сама строка `server: LLM: <провайдер>/<модель> (large=…)` пишется в
	// logs/server.log в resolveLocked. При непригодном выборе сервер всё равно
	// поднимается (доска и чат работают без LLM) — предупреждаем отдельно.
	if _, err := s.prov.resolved(); err != nil {
		logging.Warnf("server: LLM пока не настроен — чат и оркестрация будут недоступны до выбора модели: %v", err)
	}

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
	// Дренаж Kafka-рлея: очередь шагов дописывается в топик (если включён).
	if s.relay != nil {
		s.relay.Close()
	}
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

	// REST — провайдеры и модели (для выбора в WebUI)
	mux.HandleFunc("GET /api/providers", s.handleGetProviders)
	mux.HandleFunc("POST /api/providers/select", s.handleSelectProvider)

	// REST — доска/чат/сессия проекта
	mux.HandleFunc("GET /api/projects/{id}", s.handleGetBoard)
	mux.HandleFunc("POST /api/projects/{id}/chat", s.handlePostChat)
	mux.HandleFunc("GET /api/projects/{id}/chat", s.handleChatHistory)
	mux.HandleFunc("DELETE /api/projects/{id}/chat", s.handleClearChat)
	mux.HandleFunc("POST /api/projects/{id}/continue", s.handleContinue)
	mux.HandleFunc("POST /api/projects/{id}/index", s.handleProjectIndex)
	mux.HandleFunc("GET /api/projects/{id}/tokens", s.handleGetTokens)
	// Метрики и стоимость запуска (Ф-1): снимок для дашборда и экспорт Prometheus.
	mux.HandleFunc("GET /api/projects/{id}/metrics", s.handleGetMetrics)
	mux.HandleFunc("POST /api/projects/{id}/metrics/reset", s.handleResetMetrics)
	mux.HandleFunc("GET /api/metrics", s.handleGetPrometheus)
	// Ф-2: хвост логов рантайма приложения (пустой буфер — status skipped,
	// а не 404: рантайм могли ещё не запускать).
	mux.HandleFunc("GET /api/projects/{id}/applog", s.handleAppLog)
	mux.HandleFunc("POST /api/projects/{id}/{gate}/decide", s.handleGateDecide)
	mux.HandleFunc("POST /api/projects/{id}/session/stop", s.handleStop)
	// REST — структурированный вопрос ассистента (AskUser, Ф-1 «спроси
	// пользователя»): ответ на один шаг пачки.
	mux.HandleFunc("POST /api/projects/{id}/ask/{askID}/answer", s.handleAskAnswer)
	mux.HandleFunc("PUT /api/projects/{id}/tasks/{tid}", s.handleUpdateTask)
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{tid}", s.handleDeleteTask)
	// Переоткрытие выполненной задачи на доработку (done -> ready прежнему
	// исполнителю) с обязательным комментарием-обоснованием.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/reopen", s.handleReopenTask)
	mux.HandleFunc("DELETE /api/projects/{id}/epics/{eid}", s.handleDeleteEpic)
	// Git-workflow (Ф-6): кнопки «Пауза»/«Продолжить»/«Отменить» эпика — перевод
	// статуса (каскад задач + git-хуки, ветка/код остаются на месте).
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/status", s.handleSetEpicStatus)
	// Ручная пересортировка эпиков (Web UI): первый ID в списке получает
	// наибольший приоритет; если оркестрация идёт, она рестартуется.
	mux.HandleFunc("POST /api/projects/{id}/epics/reorder", s.handleReorderEpics)
	mux.HandleFunc("GET /api/projects/{id}/bugs", s.handleListBugs)
	// Ручная правка багрепорта оператором: смена статуса (любого, кроме выхода
	// из заблокированного slop) и приоритета на доске.
	mux.HandleFunc("PATCH /api/projects/{id}/bugs/{bid}", s.handleUpdateBug)

	// Инъекции (Ф-8): управление сессионными промпт-инъекциями — add, remove, list.
	mux.HandleFunc("POST /api/projects/{id}/injections", s.handleAddInjection)
	mux.HandleFunc("DELETE /api/projects/{id}/injections/{injID}", s.handleRemoveInjection)
	mux.HandleFunc("GET /api/projects/{id}/injections", s.handleListInjections)
	// Инъекции задачи: привязаны к конкретной задаче доски и применяются к её
	// исполнителю со следующего обращения к модели. Полную замену списка делает
	// PUT /tasks/{tid} (поле injections), эти два эндпоинта — точечные правки.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/injections", s.handleAddTaskInjection)
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{tid}/injections/{injID}", s.handleRemoveTaskInjection)

	// Комментарии задачи: привязаны к задаче доски (замечания QA, пользовательские, системные).
	mux.HandleFunc("GET /api/projects/{id}/tasks/{tid}/comments", s.handleListTaskComments)
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/comments", s.handleAddTaskComment)
	mux.HandleFunc("PUT /api/projects/{id}/tasks/{tid}/comments/{cmID}", s.handleUpdateTaskComment)
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{tid}/comments/{cmID}", s.handleRemoveTaskComment)

	// Git (Ф-2-3): дифф, приёмка «Принять → MR», отклонение ветки.
	mux.HandleFunc("GET /api/projects/{id}/diff", s.handleGetDiff)
	mux.HandleFunc("POST /api/projects/{id}/accept", s.handleAccept)
	mux.HandleFunc("POST /api/projects/{id}/reject-branch", s.handleRejectBranch)

	// Git-workflow (Ф-1): ветки эпиков и задач.
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/branch", s.handleCreateEpicBranch)
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/branch", s.handleCreateTaskBranch)
	// Git-workflow (Ф-2): мёрдж фича-ветки задачи в релизную ветку эпика.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/merge", s.handleMergeTask)
	// Ф-6 (этап 3): ручной откат кода задачи к опорной точке git-истории.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/rollback", s.handleRollbackTask)
	// Git-workflow (Ф-3): кнопка «Залить в main» — релизная ветка эпика → main.
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/release", s.handleReleaseEpic)
	// Git-workflow (Ф-4): авто-резолв конфликтов «main ↔ релизная ветка».
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/rebase", s.handleEpicRebase)
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/resolve", s.handleEpicResolve)
	mux.HandleFunc("GET /api/projects/{id}/epics/{eid}/resolve", s.handleResolveStatus)
	// Git-workflow (Ф-9): авторезолвинг конфликтов через LLM.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/resolve", s.handleTaskLLMResolve)
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/auto-resolve", s.handleEpicLLMResolve)
	// Git-workflow (Ф-5): «Создать MR» — push ветки эпика/задачи в remote и MR.
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/mr", s.handleCreateEpicMR)
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/mr", s.handleCreateTaskMR)

	// Логи проекта (панель «Логи», см. logs.go).
	mux.HandleFunc("GET /api/projects/{id}/logs", s.handleGetLogs)
	mux.HandleFunc("DELETE /api/projects/{id}/logs", s.handleDeleteLogs)

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
