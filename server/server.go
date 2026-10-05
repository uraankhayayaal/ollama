package server

import (
	"ai/agents/architect"
	"ai/board"
	"ai/chat"
	"ai/forges"
	"ai/gitops"
	"ai/logging"
	"ai/models"
	"ai/runmetrics"
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
	"sort"
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
	mux.HandleFunc("DELETE /api/projects/{id}/epics/{eid}", s.handleDeleteEpic)
	// Git-workflow (Ф-6): кнопки «Пауза»/«Продолжить»/«Отменить» эпика — перевод
	// статуса (каскад задач + git-хуки, ветка/код остаются на месте).
	mux.HandleFunc("POST /api/projects/{id}/epics/{eid}/status", s.handleSetEpicStatus)
	mux.HandleFunc("GET /api/projects/{id}/bugs", s.handleListBugs)

	// Инъекции (Ф-8): управление сессионными промпт-инъекциями — add, remove, list.
	mux.HandleFunc("POST /api/projects/{id}/injections", s.handleAddInjection)
	mux.HandleFunc("DELETE /api/projects/{id}/injections/{injID}", s.handleRemoveInjection)
	mux.HandleFunc("GET /api/projects/{id}/injections", s.handleListInjections)
	// Инъекции задачи: привязаны к конкретной задаче доски и применяются к её
	// исполнителю со следующего обращения к модели. Полную замену списка делает
	// PUT /tasks/{tid} (поле injections), эти два эндпоинта — точечные правки.
	mux.HandleFunc("POST /api/projects/{id}/tasks/{tid}/injections", s.handleAddTaskInjection)
	mux.HandleFunc("DELETE /api/projects/{id}/tasks/{tid}/injections/{injID}", s.handleRemoveTaskInjection)

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

// handleGetProviders отдаёт список провайдеров из providers.json и ТЕКУЩИЙ
// выбор (провайдер + основная и большая модели), чтобы переключатель в UI
// показывал реально работающую модель, а не первую попавшуюся.
//
// Список провайдеров отсортирован: обход map в Go неупорядочен, и без сортировки
// UI прыгал бы между провайдерами при каждом обновлении страницы.
func (s *Server) handleGetProviders(w http.ResponseWriter, r *http.Request) {
	cfg, err := models.LoadProvidersConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "providers.json недоступен: "+err.Error())
		return
	}

	type providerInfo struct {
		Name         string   `json:"name"`
		Models       []string `json:"models"`
		DefaultModel string   `json:"default_model"`
		LargeModel   string   `json:"large_model"`
	}

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	providers := make([]providerInfo, 0, len(names))
	for _, name := range names {
		providers = append(providers, providerInfo{
			Name:         name,
			Models:       selectableModels(cfg.Providers[name]),
			DefaultModel: cfg.Providers[name].DefaultModel,
			LargeModel:   cfg.Providers[name].LargeModel,
		})
	}

	sel := s.prov.selection()
	out := map[string]any{
		"providers":           providers,
		"current_provider":    string(sel.Provider),
		"current_model":       sel.Model,
		"current_large_model": sel.LargeModel,
		"override":            s.prov.isOverride(),
	}
	// Фактические модели (после дефолтов providers.json) и ошибка текущего
	// выбора: без них UI не может показать «large=qwen3.6:35b» или «провайдер
	// не настроен».
	if res, rerr := s.prov.resolved(); res != nil {
		out["current_model"] = res.Model
		out["current_large_model"] = res.LargeModel
		if rerr != nil {
			out["error"] = rerr.Error()
		}
	} else if rerr != nil {
		out["error"] = rerr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// selectableModels — модели, доступные для выбора: объединение models,
// default_model и large_model. Последние два могут отсутствовать в models
// (особенно large_model), и без объединения действующую модель нельзя было бы
// выбрать в UI.
func selectableModels(cfg models.ProviderConfig) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(cfg.Models)+2)
	for _, m := range append(append([]string{}, cfg.Models...), cfg.DefaultModel, cfg.LargeModel) {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// handleSelectProvider переключает провайдера/модели на лету. Провайдер
// создаётся сразу — если он непригоден (битый base_url, пустая модель), клиент
// получает ошибку сейчас, а не на первом запросе к модели. Уже идущая
// оркестрация продолжает на прежней модели (провайдер переиспользуется до
// конца запуска) — об этом сказано в ответе и в логе.
func (s *Server) handleSelectProvider(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		// LargeModel — необязательная большая модель для «тяжёлых» агентов
		// (лиды, архитектор, ревьюер); "" — слой не включается.
		LargeModel string `json:"large_model"`
		// Reset=true — вернуть выбор из окружения (кнопка «сбросить»).
		Reset bool `json:"reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if body.Reset {
		res, err := s.prov.clearOverride()
		s.writeSelectionResult(w, res, err)
		return
	}

	cfg, err := models.LoadProvidersConfig()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "providers.json недоступен: "+err.Error())
		return
	}

	providerCfg, ok := cfg.Providers[strings.TrimSpace(body.Provider)]
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("провайдер %q не найден в providers.json", body.Provider))
		return
	}

	allowed := selectableModels(providerCfg)
	model := strings.TrimSpace(body.Model)
	if model == "" {
		model = providerCfg.ResolveModel("")
	}
	if !contains(allowed, model) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("модель %q недоступна у провайдера %s (доступны: %s)",
			model, body.Provider, strings.Join(allowed, ", ")))
		return
	}
	large := strings.TrimSpace(body.LargeModel)
	if large != "" && !contains(allowed, large) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("модель %q недоступна у провайдера %s (доступны: %s)",
			large, body.Provider, strings.Join(allowed, ", ")))
		return
	}

	res, err := s.prov.setOverride(models.Selection{
		Provider:   models.ProviderName(strings.TrimSpace(body.Provider)),
		Model:      model,
		LargeModel: large,
	})
	s.writeSelectionResult(w, res, err)
}

// writeSelectionResult отвечает на смену выбора: 200 с фактическими моделями
// либо 502 с причиной (провайдер не собрался — выбор уже применён, но не
// работает; UI покажет текст ошибки из лога/ответа).
func (s *Server) writeSelectionResult(w http.ResponseWriter, res *models.Resolved, err error) {
	if err != nil {
		msg := err.Error()
		// Ошибка конфигурации (providers.json, base_url, модель) — это ответ
		// на запрос «сделай так», а не отказ сервера: 400 информативнее 502.
		code := http.StatusBadGateway
		if res == nil || strings.Contains(msg, "providers.json") {
			code = http.StatusBadRequest
		}
		writeErr(w, code, "не удалось применить выбор модели: "+msg)
		return
	}
	// Идущая оркестрация держит старый провайдер до конца запуска — честно
	// предупреждаем, чтобы смена модели не выглядела «не сработавшей».
	inFlight := s.anySessionRunning()

	out := map[string]any{
		"ok":                 true,
		"provider":           string(res.Name),
		"model":              res.Model,
		"large_model":        res.LargeModel,
		"describe":           res.Describe(),
		"applies_to_running": inFlight,
	}
	if inFlight {
		out["message"] = "текущий запуск продолжится на прежней модели; выбор применится со следующего"
	}
	writeJSON(w, http.StatusOK, out)
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
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

// anySessionRunning сообщает, идёт ли где-нибудь оркестрация. Порядок
// блокировок тот же, что в sessionFlags (s.mu → sess.mu).
func (s *Server) anySessionRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		sess.mu.Lock()
		run := sess.running
		sess.mu.Unlock()
		if run {
			return true
		}
	}
	return false
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

// decodeBodyStrict — декодер с DisallowUnknownFields: опечатка в имени поля
// возвращает 400, а не «успех, параметр молча потерян». Применяется там, где
// тело задаёт поведение (инъекции).
func decodeBodyStrict(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
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
// handlePostChat принимает сообщение пользователя (message) ИЛИ запрос разбора
// логов (log_analysis — кнопка «Разобрать» в панели логов). Второй вариант не
// пишет в историю выдуманную реплику пользователя: он добавляет служебную
// строку статуса, а разбор идёт отдельным ходом ассистента с дайджестом в
// промпте (см. runChatLogAnalysis).
func (s *Server) handlePostChat(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	var body struct {
		Message     string          `json:"message"`
		LogAnalysis *logAnalysisReq `json:"log_analysis"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "поле message обязательно")
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" && body.LogAnalysis == nil {
		writeErr(w, http.StatusBadRequest, "поле message обязательно")
		return
	}

	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}

	// Ветка разбора логов. Провайдера она берёт сама и только когда до него
	// дойдёт дело: отказ (нет файла), пустой разбор и cooldown не должны
	// требовать настроенной модели.
	if body.LogAnalysis != nil {
		s.handleChatLogAnalysis(w, sess, *body.LogAnalysis)
		return
	}

	// Единый ассистент: решает по смыслу (создание эпика/задачи/бага — через
	// его board-инструменты, а не по регэкспепу).
	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}
	log.Printf("[llm] модель: %s", s.prov.describe())

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
	sess.log.Infof("[чат] маршрут: единый ассистент (действия по смыслу, без сплита)")
	sess.runChatAssistant(context.Background(), body.Message, prov)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleChatLogAnalysis обслуживает кнопку «Разобрать» в панели логов. Ответ
// уходит в чат (и в шину), в REST — короткий статус.
func (s *Server) handleChatLogAnalysis(w http.ResponseWriter, sess *Session, req logAnalysisReq) {
	ctx := context.Background()
	a, err := sess.buildLogAnalysis(req)
	if err != nil {
		sess.append(chat.RoleStatus, "Разбор логов не удался: "+err.Error(), "", "", nil)
		writeErr(w, http.StatusBadRequest, "разбор логов: "+err.Error())
		return
	}

	// Служебная строка в чате: пользователь нажал кнопку, а не что-то написал.
	status := "Разбор логов: " + a.FilesLabel()
	if a.Note != "" {
		status += " (" + a.Note + ")"
	}
	sess.log.Infof("[чат] %s", status)
	// Именно append, а не sess.chat.Append + отдельный hub.Publish: appendMsg
	// сам публикует в шину (type=chat), а вторая публикация отправила бы ту же
	// строку в UI дважды — фронт добавляет каждое событие чата в состояние
	// без дедупликации, и в панели появлялись два «Разбор логов: …».
	sess.append(chat.RoleStatus, status, "", "", nil)

	// Пустой разбор: критичных проблем нет. Отвечаем детерминированно, без
	// вызова модели — тратить токены на «всё хорошо» незачем, а модель на
	// пустом дайджесте склонна выдумать проблему.
	if a.Digest.empty() {
		msg := fmt.Sprintf("В логе %s критичных проблем не найдено: ни одной записи уровня WARN/ERROR/FATAL%s.",
			a.FilesLabel(), logAnalysisWhere(a.Note))
		sess.append(chat.RoleAssistant, msg, "assistant", "", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "empty": true})
		return
	}

	sess.log.Infof("[чат] маршрут: разбор логов (%d строк, %d находок)",
		a.Digest.Lines, len(a.Digest.Findings))
	if sess.markAnalyzed(a.key()) {
		// Тот же лог только что разобран: модель второй раз не зовём, ответ
		// уже выше в чате.
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Лог %s не изменился с прошлого разбора — повторно не запускаю, ответ выше в чате.", a.FilesLabel()),
			"", "", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cached": true})
		return
	}

	prov, err := s.provider()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "LLM-провайдер не настроен: "+err.Error())
		return
	}
	log.Printf("[llm] модель: %s", s.prov.describe())
	sess.runChatLogAnalysis(ctx, a, prov)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "findings": len(a.Digest.Findings)})
}

// logAnalysisWhere — хвост фразы про объём выборки для ответа «проблем нет».
func logAnalysisWhere(note string) string {
	if note == "" {
		return ""
	}
	return " в выборке (" + note + ")"
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

	// Кнопка «Продолжить» — работа только с доской: новые эпики не создаются,
	// текст задачи лишь описывает контекст для раннера.
	if err := sess.start(ctx, taskText, prov, true); err != nil {
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
// Необязательный query-параметр branch задаёт ветку индексации (пусто — текущая
// ветка каталога проекта), см. PLAN-2026-09-27-done-branch-aware-rag.md.
func (s *Server) handleProjectIndex(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка создания сессии: "+err.Error())
		return
	}
	branch := strings.TrimSpace(r.URL.Query().Get("branch"))
	if err := sess.IndexBackground(r.Context(), branch); err != nil {
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
		"message": "Индексация RAG-индекса запущена в фоне" + branchSuffix(branch),
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

	// Промпт-инъекции задачи: список заменяется целиком, нормализуется
	// (выдаётся id) и валидируется. Раньше поле было только для чтения — задать
	// инъекции задачи было нечем, кроме прямой правки Redis.
	injectionsChanged := false
	if raw, ok := patch["injections"]; ok {
		list, err := decodeInjections(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "injections: "+err.Error())
			return
		}
		if err := board.ValidateInjections(list); err != nil {
			writeErr(w, http.StatusBadRequest, "injections: "+err.Error())
			return
		}
		t.Injections = list
		injectionsChanged = true
	}

	if err := store.SaveTask(r.Context(), t); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if injectionsChanged {
		// Инъекции видны модели со следующего запроса: агентский цикл читает
		// их из записи задачи перед каждым обращением (board.InjectionSource).
		logging.For(project).Infof("[задача %s] промпт-инъекции обновлены: %d", taskID, len(t.Injections))
	}

	// Публикуем обновённую доску.
	s.srvEmitBoard(project, "REST: задача обновлена")
	writeJSON(w, http.StatusOK, t)
}

// decodeInjections разбирает поле injections из JSON-патча: массив объектов
// или одиночный объект (удобно для точечных правок из UI). Общая реализация
// живёт в board, чтобы ею пользовался и инструмент доски.
func decodeInjections(raw any) ([]board.Injection, error) {
	return board.DecodeInjections(raw)
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

// handleAddInjection добавляет сессионную инъекцию. Идентификатор выдаётся
// ДО сохранения: раньше он присваивался после AddSessionInjection, поэтому
// клиент получал id, которого в сессии не было, а DELETE по нему молча
// ничего не удалял.
func (s *Server) handleAddInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	var body board.Injection
	if err := decodeBodyStrict(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	// Инъекция сессии по умолчанию принадлежит scope session.
	if body.Scope == "" {
		body.Scope = board.InjectionScopeSession
	}
	body.Normalize()
	if err := body.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ID == "" {
		body.ID = randomID()
	}
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка: "+err.Error())
		return
	}
	if err := sess.UpsertSessionInjection(body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Ответ отдаёт id той записи, которая реально лежит в сессии.
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":  body.ID,
		"msg": "инъекция добавлена; применяется со следующего запроса к модели",
	})
}

// handleRemoveInjection удаляет сессионную инъекцию по ID. Отсутствующий id —
// 404, а не 200: иначе клиент считает удаление успешным, а инъекция продолжает
// применяться.
func (s *Server) handleRemoveInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	injID := r.PathValue("injID")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка: "+err.Error())
		return
	}
	if !sess.RemoveSessionInjection(injID) {
		writeErr(w, http.StatusNotFound, "инъекция не найдена: "+injID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"msg": "инъекция удалена"})
}

// handleListInjections возвращает список сессионных инъекций.
func (s *Server) handleListInjections(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	sess, _, err := s.getOrCreate(project)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка: "+err.Error())
		return
	}
	injs := sess.SessionInjections()
	if injs == nil {
		injs = []board.Injection{}
	}
	json.NewEncoder(w).Encode(injs)
}

// handleAddTaskInjection добавляет промпт-инъекцию к конкретной задаче доски
// (или перезаписывает запись с тем же id). Инъекция применяется к исполнителю
// этой задачи со СЛЕДУЮЩЕГО обращения к модели — в том числе если её цикл уже
// идёт: агентский цикл перечитывает задачу перед каждым запросом.
func (s *Server) handleAddTaskInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	var inj board.Injection
	if err := decodeBodyStrict(r, &inj); err != nil {
		writeErr(w, http.StatusBadRequest, "некорректный JSON: "+err.Error())
		return
	}
	inj.Normalize()
	if err := inj.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if inj.ID == "" {
		inj.ID = randomID()
	}

	store, err := board.NewStore(r.Context(), architect.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	list, err := store.AddTaskInjection(r.Context(), taskID, inj)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "задача "+taskID+": "+err.Error())
		return
	}
	logging.For(project).Infof("[задача %s] добавлена промпт-инъекция %s (%d всего); применится со следующего запроса к модели",
		taskID, inj.Name, len(list))
	s.srvEmitBoard(project, "REST: инъекция задачи добавлена")
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         inj.ID,
		"injections": list,
	})
}

// handleRemoveTaskInjection удаляет инъекцию задачи по id; отсутствующий id —
// 404 (иначе клиент считал бы удаление успешным, а текст продолжал бы уходить
// в модель).
func (s *Server) handleRemoveTaskInjection(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")
	injID := r.PathValue("injID")

	store, err := board.NewStore(r.Context(), architect.LoadConfig().StoreConfig(project))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	defer store.Close()

	list, found, err := store.RemoveTaskInjection(r.Context(), taskID, injID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "задача "+taskID+": "+err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "инъекция не найдена: "+injID)
		return
	}
	logging.For(project).Infof("[задача %s] удалена промпт-инъекция %s (осталось %d)", taskID, injID, len(list))
	s.srvEmitBoard(project, "REST: инъекция задачи удалена")
	writeJSON(w, http.StatusOK, map[string]any{"injections": list})
}
