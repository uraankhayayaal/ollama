package main

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/agents/architect"
	"ai/agents/backendlead"
	"ai/agents/codereviewer"
	"ai/agents/developer"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/frontendlead"
	"ai/agents/planner"
	"ai/agents/qaengineer"
	"ai/agents/qalead"
	"ai/board"
	"ai/checkpoint"
	// Blank-import регистрирует все встроенные провайдеры систем ревью
	// (init() в forges/github и forges/gitlab) в фабрике forges.New.
	_ "ai/forges/all"
	"ai/logging"
	"ai/models"
	"ai/projects"
	"ai/services/mrlistener"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	// Имя проекта из аргументов командной строки — по нему именуется
	// подробный лог-файл (logs/<проект>.log).
	logging.Setup(projectFromArgs(os.Args))

	// Load the .env file. Отсутствие файла не фатально: критичные настройки
	// (провайдер, токены) всё равно проверяются ниже по ходу выполнения.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		logging.Warnf("Предупреждение: .env не загружен (%v)", err)
	}

	// Сервис мониторинга новых MR — не требует провайдера модели.
	if len(os.Args) > 1 && os.Args[1] == "listen" {
		if err := mrlistener.Listen(context.Background()); err != nil {
			logging.Fatalf("%v", err)
		}
		return
	}

	// Приёмка собранного приложения: сборка, запуск, проверка логов.
	// Полностью детерминирована (без LLM) — провайдер модели не нужен.
	// go run . accept <имя_проекта>
	if len(os.Args) > 1 && os.Args[1] == "accept" {
		if len(os.Args) < 3 || os.Args[2] == "" {
			logging.Fatalf("Использование: go run . accept <имя_проекта>\nПример: go run . accept storageService")
		}
		runAcceptCommand(os.Args[2])
		os.Exit(0)
	}

	// Таймаут цикла агента берётся из окружения REVIEW_TIMEOUT, иначе 10 минут.
	// Дефолт выбран заведомо выше таймаута отдельного HTTP-запроса провайдера
	// (5 минут), чтобы один долгий ответ модели не съедал весь бюджет цикла.
	timeout := parseTimeout(os.Getenv("REVIEW_TIMEOUT"))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	providerType := os.Getenv("LLM_PROVIDER") // "ollama", "yandex" или "trim"

	var (
		provider models.LLMProvider
		err      error
	)

	switch providerType {
	case "ollama":
		model := os.Getenv("OLLAMA_MODEL") // например, "llama3"
		if model == "" {
			model = "llama3"
		}
		provider, err = models.NewOllamaProvider(model)

	case "yandex":
		provider = models.NewAlisaProvider()

	case "trim":
		provider, err = models.NewTrimProvider()

	default:
		logging.Fatalf("Unknown provider: %s. Use 'ollama', 'yandex' or 'trim'", providerType)
	}

	if err != nil {
		logging.Fatalf("Failed to init provider: %v", err)
	}

	// Провайдеры с одним раундом (например, trim) не умеют цикл NextChunk
	// у ревьювера — дифф передаём целиком без разбиения на части.
	noChunk := providerType == "trim"

	// Выбор агента по первому аргументу:
	// go run . generate <имя> [промпт] | backend <имя> [промпт] | frontend <имя> [промпт] | review <URL> | listen
	agentName, agentArgs := agentCommand(os.Args)

	var agent agents.Agent
	var planMode bool
	var planProject string
	var planPrompt string
	switch agentName {
	case "generate", "backend":
		// "generate" оставлен как алиас для обратной совместимости: новое
		// имя — backend (агент «Backend разработчик»).
		if len(agentArgs) < 1 {
			logging.Fatalf("Использование: go run . backend <имя_проекта> [промпт]\n" +
				"Пример: go run . backend storageService \"Напиши микросервис для хранения файлов\"")
		}
		projectName := agentArgs[0]
		prompt := defaultPrompt(agentArgs[1:])
		agent = developer.NewBackendDeveloper(projectName, prompt)
	case "frontend":
		if len(agentArgs) < 1 {
			logging.Fatalf("Использование: go run . frontend <имя_проекта> [промпт]\n" +
				"Пример: go run . frontend storageService \"Напиши веб-интерфейс для просмотра файлов\"")
		}
		projectName := agentArgs[0]
		prompt := defaultPrompt(agentArgs[1:])
		agent = developer.NewFrontendDeveloper(projectName, prompt)
	case "plan":
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . plan <имя_проекта> <промпт> [--resume]\n" +
				"Пример: go run . plan storageService \"Создай микросервис хранения файлов и ревью кода\"\n" +
				"Пример: go run . plan storageService --resume")
		}
		planMode = true
		planProject = agentArgs[0]
		planPrompt = strings.Join(agentArgs[1:], " ")
	case "devops":
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . devops <имя_проекта> <промпт>\n" +
				"Пример: go run . devops billingService \"Подними Docker Compose с моками для QA и подготовь манифесты Kubernetes\"")
		}
		projectName := agentArgs[0]
		prompt := strings.Join(agentArgs[1:], " ")
		agent = devops.NewDevops(projectName, prompt)
	case "devops-lead":
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . devops-lead <имя_проекта> <промпт>\n" +
				"Пример: go run . devops-lead billingService \"Декомпозируй инфраструктуру: Docker Compose локально, Kubernetes на проде, CI/CD с автотестами QA\"")
		}
		projectName := agentArgs[0]
		prompt := strings.Join(agentArgs[1:], " ")
		agent = devopslead.NewDevopsLead(projectName, prompt)
	case "qa":
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . qa <имя_проекта> <промпт>\n" +
				"Пример: go run . qa billingService \"Проверь соответствие Backend и Frontend API-контрактам и напиши автотесты\"")
		}
		projectName := agentArgs[0]
		prompt := strings.Join(agentArgs[1:], " ")
		agent = qaengineer.NewQAEngineer(projectName, prompt)
	case "qalead":
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . qalead <имя_проекта> <промпт>\n" +
				"Пример: go run . qalead billingService \"Сформируй тест-план по контрактам Архитектора и декомпозируй его на задачи для QA-инженеров\"")
		}
		projectName := agentArgs[0]
		prompt := strings.Join(agentArgs[1:], " ")
		agent = qalead.NewQALead(projectName, prompt)
	case "frontendlead":
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . frontendlead <имя_проекта> <промпт>\n" +
				"Пример: go run . frontendlead billingService \"Декомпозируй интерфейс на UI-модули, спроектируй стейт и API-контракты\"")
		}
		projectName := agentArgs[0]
		prompt := strings.Join(agentArgs[1:], " ")
		agent = frontendlead.NewFrontendLead(projectName, prompt)
	case "backendlead":
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . backendlead <имя_проекта> <промпт>\n" +
				"Пример: go run . backendlead billingService \"Декомпозируй сервисную часть на модули, спроектируй контракты API\"")
		}
		projectName := agentArgs[0]
		prompt := strings.Join(agentArgs[1:], " ")
		agent = backendlead.NewBackendLead(projectName, prompt)
	case "kanban":
		// Kanban-оркестрация: архитектор публикует эпики на Redis-доску,
		// лиды декомпозируют их на JSON-задачи, специалисты выполняют —
		// до полного решения задачи пользователя.
		// go run . kanban <имя_проекта> <промпт>
		if len(agentArgs) < 2 {
			logging.Fatalf("Использование: go run . kanban <имя_проекта> <промпт>\n" +
				"Пример: go run . kanban billingService \"Спроектируй и собери сервис: архитектура, контракты, DevOps, тесты\"")
		}
		kanbanProject := agentArgs[0]
		kanbanPrompt := strings.Join(agentArgs[1:], " ")
		store, err := board.NewStore(ctx, architect.LoadConfig().StoreConfig(kanbanProject))
		if err != nil {
			logging.Fatalf("Ошибка доски проекта %s: %v", kanbanProject, err)
		}
		if err := planner.NewKanbanRunner(provider, store).Run(ctx, kanbanProject, kanbanPrompt); err != nil {
			_ = store.Close()
			logging.Fatalf("Kanban: %v", err)
		}
		_ = store.Close()
		os.Exit(0)
	case "review":
		agent = codereviewer.NewCodereviewer(agentArgs)
		if noChunk {
			if cw, ok := agent.(*codereviewer.Codereviewer); ok {
				cw.NoChunk = true
			}
		}
	default:
		logging.Fatalf("Неизвестный агент %q. Используйте 'go run . generate <имя> [промпт]', 'go run . backend <имя> [промпт]', 'go run . frontend <имя> [промпт]', 'go run . devops <имя> <промпт>', 'go run . devops-lead <имя> <промпт>', 'go run . qa <имя> <промпт>', 'go run . qalead <имя> <промпт>', 'go run . frontendlead <имя> <промпт>', 'go run . backendlead <имя> <промпт>', 'go run . plan <имя> <промпт>', 'go run . kanban <имя> <промпт>', 'go run . review <URL>', 'go run . accept <имя>' или 'go run . listen'", agentName)
	}

	// Режим планировщика обрабатывается отдельно и до общего прогона:
	// при resume план восстанавливается из чекпоинта без повторного вызова
	// планировщика (экономим токены), иначе планировщик строит план,
	// а затем исполнитель выполняет шаги по волнам параллельности.
	if planMode {
		runPlanMode(ctx, provider, planProject, planPrompt)
		os.Exit(0)
	}

	resp, err := provider.Generate(ctx, agent)
	if err != nil {
		logging.Fatalf("Error: %v", err)
	}

	if resp.Truncated {
		logging.Warnf("Внимание: цикл агента остановлен по лимиту раундов, результат может быть неполным")
	}

	// 6b. Запасной путь: если модель вернула ревью текстом, а не вызовами
	// инструментов (характерно для YandexGPT), а замечаний ещё не
	// опубликовано — пытаемся распарсить текст в комментарии и опубликовать.
	if r, ok := agent.(reviewParser); ok {
		n := r.PublishParsedReview(resp.Content)
		if n > 0 {
			logging.Infof("Опубликовано замечаний из текстового ответа модели: %d", n)
		}
	}

	// 7. Итоговый отчёт-сводка в тред MR/PR, если агент его поддерживает.
	if r, ok := agent.(summarizer); ok {
		if serr := r.PostSummaryToPR(); serr != nil {
			logging.Warnf("Не удалось опубликовать итоговую сводку: %v", serr)
		}
	}

	// 8. Финальный отчёт агента (например, SUMMARY.md у агента-разработчика).
	if r, ok := agent.(finalizer); ok {
		r.Finalize()
	}

	logging.Infof("Response Message: %s", resp.Content)
	logging.Infof("Response Tools: %v", resp.ToolCalls)
}

// reviewParser — опциональный интерфейс агента, умеющего опубликовать ревью,
// которое модель написала текстом (без вызова инструментов).
type reviewParser interface {
	PublishParsedReview(content string) int
}

// summarizer — опциональный интерфейс агента, умеющего публиковать
// итоговую сводку ревью в тред MR/PR после завершения цикла.
type summarizer interface {
	PostSummaryToPR() error
}

// finalizer — опциональный интерфейс агента, выполняющего финализацию
// после завершения цикла (например, агент-разработчик пишет SUMMARY.md).
type finalizer interface {
	Finalize()
}

// defaultPrompt возвращает текст задания для генератора. Если промпт не
// передан в командной строке, используется заданное по умолчанию значение.
func defaultPrompt(args []string) string {
	if len(args) >= 1 && args[0] != "" {
		return args[0]
	}
	return "Напиши микросервис для расчета квадратного уровнения, придумай формат аргументов для передачи в код."
}

// projectFromArgs определяет имя проекта (имя лог-файла) по аргументам:
// generate/backend/frontend/plan/accept <имя> → имя; review → "review"; listen → "mrlistener".
func projectFromArgs(args []string) string {
	if len(args) < 2 {
		return "unnamed"
	}
	switch args[1] {
	case "generate", "backend", "frontend", "devops", "devops-lead", "qa", "qalead", "frontendlead", "backendlead", "plan", "kanban", "accept":
		if len(args) >= 3 && args[2] != "" {
			return args[2]
		}
	case "review":
		return "review"
	case "listen":
		return "mrlistener"
	}
	return "unnamed"
}

// agentCommand возвращает имя агента (первый аргумент) и оставшиеся
// аргументы, переданные ему.
func agentCommand(args []string) (name string, rest []string) {
	if len(args) < 2 {
		return "", nil
	}
	return args[1], args[2:]
}

// parseTimeout разбирает длительность таймаута из строки (например "10m").
// При пустой строке или ошибке разбора возвращается значение по умолчанию.
func parseTimeout(raw string) time.Duration {
	const def = 10 * time.Minute
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		logging.Warnf("Неверный REVIEW_TIMEOUT=%q, использую %v", raw, def)
		return def
	}
	return d
}

// runAcceptCommand выполняет приёмку собранного приложения в temp/<projectName>:
// определяет тип проекта, собирает, запускает на короткое время и проверяет
// логи на ошибки. Печатает отчёт и завершается с кодом 0 при успехе, 1 — при
// обнаруженных ошибках.
func runAcceptCommand(projectName string) {
	dir := projects.ProjectDir(projectName)
	rep := acceptor.Accept(dir, acceptor.LoadConfig())
	logging.Infof("%s", acceptReportText(rep))
	if rep.Verdict == acceptor.VerdictReject {
		os.Exit(1)
	}
}

// acceptReportText собирает человекочитаемый отчёт приёмки.
func acceptReportText(rep *acceptor.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Приёмка проекта %q (тип: %s)\n", rep.Project, rep.Tool)
	fmt.Fprintf(&b, "Вердикт: %s\n", rep.Verdict)
	fmt.Fprintf(&b, "Сводка: %s\n\n", rep.Summary)

	// Монорепозиторий (frontend+backend): каждый подпроект принимался отдельно
	// — печатаем его отчёт целиком, чтобы было видно, где что упало.
	if len(rep.Projects) > 0 {
		for i, pr := range rep.Projects {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString("=== Подпроект: ")
			b.WriteString(pr.Project)
			b.WriteString(" (")
			b.WriteString(pr.Tool)
			b.WriteString(") ===\n")
			b.WriteString(acceptReportText(pr))
			b.WriteString("\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}

	if rep.Install != nil {
		fmt.Fprintf(&b, "Установка зависимостей: %s\n", installStatusWord(rep.Install))
		fmt.Fprintf(&b, "  команда: %s\n", rep.Install.Command)
		if rep.Install.Output != "" {
			fmt.Fprintf(&b, "  вывод:\n%s\n", rep.Install.Output)
		}
	}
	if !rep.Build.Skipped {
		fmt.Fprintf(&b, "Сборка: %v\n", rep.Build.OK)
		fmt.Fprintf(&b, "  команда: %s\n", rep.Build.Command)
		if rep.Build.Output != "" {
			fmt.Fprintf(&b, "  вывод:\n%s\n", rep.Build.Output)
		}
	}
	if rep.Run != nil {
		fmt.Fprintf(&b, "Запуск: %v (код выхода: %d, серверный режим: %v)\n",
			rep.Run.OK, rep.Run.ExitCode, rep.Run.ServerMode)
		fmt.Fprintf(&b, "  команда: %s\n", rep.Run.Command)
		if rep.Run.Output != "" {
			fmt.Fprintf(&b, "  вывод последней проверки:\n%s\n", rep.Run.Output)
		}
	}
	if rep.Format != nil {
		fmt.Fprintf(&b, "Стилизатор: %s\n", formatStatusWord(rep.Format))
		fmt.Fprintf(&b, "  команда: %s\n", rep.Format.Command)
		if rep.Format.Output != "" {
			fmt.Fprintf(&b, "  вывод:\n%s\n", rep.Format.Output)
		}
	}
	if rep.Analyze != nil {
		fmt.Fprintf(&b, "Анализатор: %s\n", formatStatusWord(rep.Analyze))
		fmt.Fprintf(&b, "  команда: %s\n", rep.Analyze.Command)
		if rep.Analyze.Output != "" {
			fmt.Fprintf(&b, "  вывод:\n%s\n", rep.Analyze.Output)
		}
	}
	if text := rep.IssuesText(); text != "замечаний не обнаружено" {
		fmt.Fprintf(&b, "\nЗамечания:\n%s\n", text)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatStatusWord(res *acceptor.CheckResult) string {
	switch {
	case res == nil:
		return "<нет>"
	case res.Skipped:
		return "пропущен (инструмент не найден)"
	case res.OK:
		return "OK"
	default:
		return "ЕСТЬ ЗАМЕЧАНИЯ"
	}
}

func installStatusWord(res *acceptor.InstallResult) string {
	switch {
	case res == nil:
		return "<нет>"
	case res.Skipped:
		return "не требовалась"
	case res.OK:
		return "OK"
	default:
		return "ОШИБКА"
	}
}

// runPlanMode выполняет план с чекпоинтами. Если запрошен resume и в Redis
// есть чекпоинт — план и состояние восстанавливаются без повторного вызова
// планировщика. Иначе планировщик строит план (с повторной попыткой при
// некорректном JSON), и каждый шаг выполняется отдельным агентом с чистым
// контекстом (экономия токенов).
func runPlanMode(ctx context.Context, provider models.LLMProvider, projectName, originalPrompt string) {
	store, resume := plannerStore(ctx, projectName)
	if store != nil {
		defer store.Close()
	}

	var plan *planner.Plan

	// Resume: пытаемся восстановить план и состояние из чекпоинта.
	if resume && store != nil {
		snap, err := store.Load(ctx)
		if err == nil {
			if p, perr := planner.ParsePlan(string(snap.PlanJSON)); perr == nil && p != nil {
				plan = p
				logging.Infof("Resume: план восстановлен из чекпоинта, завершено шагов: %d", len(snap.Completed))
			} else if perr != nil {
				logging.Warnf("Чекпоинт существует, но план в нём повреждён (%v), перепланирую", perr)
			}
		} else if err != checkpoint.ErrNotFound {
			logging.Warnf("Не удалось прочитать чекпоинт: %v", err)
		}
	}

	// Нет чекпоинта / resume не запрошен — строим план планировщиком.
	if plan == nil {
		plannerAgent := planner.NewPlanner(projectName, originalPrompt)
		resp, err := provider.Generate(ctx, plannerAgent)
		if err != nil {
			logging.Fatalf("Получение плана: %v", err)
		}
		plan, err = planner.ParsePlan(resp.Content)
		if err != nil {
			logging.Warnf("Не удалось разобрать план из ответа планировщика: %v", err)
			logging.Detailf("Ответ планировщика:\n%s", resp.Content)
			peer := planner.NewPlanner(projectName, originalPrompt+"\n\nВерни план строго в формате JSON, без markdown-обёрток и лишнего текста.")
			planResp, perr := provider.Generate(ctx, peer)
			if perr != nil {
				logging.Fatalf("Получение плана (повтор): %v", perr)
			}
			plan, err = planner.ParsePlan(planResp.Content)
			if err != nil {
				logging.Fatalf("Повторный ответ планировщика не содержит корректного JSON-плана: %v", err)
			}
		}
	}

	logging.Infof("План получен: %q (%d шагов)", plan.Summary, len(plan.Steps))
	for i, s := range plan.Steps {
		logging.Infof("  %d. [%s] %s", i+1, s.Agent, s.Description)
	}

	exec := planner.NewExecutor(provider, plan)
	exec.SetCheckpoint(store, resume)
	if err := exec.Run(ctx); err != nil {
		logging.Fatalf("Ошибка выполнения плана: %v", err)
	}

	logging.Infof("План выполнен успешно. Проект: %q", plan.ProjectName)
}

// plannerStore создаёт чекпоинт-хранилище для плана проекта. Контрольные
// точки включаются автоматически, если в настройках задан REDIS_ADDR, либо
// явно при PLAN_CHECKPOINT=1 или запросе resume (--resume / PLAN_RESUME=1).
// Адрес Redis берётся из REDIS_ADDR (по умолчанию localhost:6379). Если Redis
// недоступен, а resume не запрошен — работаем без чекпоинтов (предупреждение),
// при resume — завершаемся с ошибкой.
func plannerStore(ctx context.Context, projectName string) (*checkpoint.Store, bool) {
	resume := envBool("PLAN_RESUME") || containsArg("--resume")

	// REDIS_ADDR явно задан → включаем чекпоинты автоматически.
	explicitRedis := strings.TrimSpace(os.Getenv("REDIS_ADDR")) != ""
	enabled := envBool("PLAN_CHECKPOINT") || explicitRedis || resume
	if !enabled {
		return nil, resume
	}

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	cfg := checkpoint.StoreConfig{
		Addr:     addr,
		Password: os.Getenv("REDIS_PASSWORD"),
		DB:       envInt("REDIS_DB", 0),
		Key:      "checkpoint:" + projectName,
		TTL:      envDuration("PLAN_CHECKPOINT_TTL", 0),
	}
	store, err := checkpoint.NewStore(ctx, cfg)
	if err != nil {
		if resume {
			logging.Fatalf("Resume невозможен: Redis недоступен (%v)", err)
		}
		logging.Warnf("Внимание: Redis недоступен (%v), работаю без чекпоинтов", err)
		return nil, resume
	}
	logging.Infof("Контрольные точки включены: %s (чекпоинт: %s)", addr, store.Key())
	return store, resume
}

// envBool возвращает true, если переменная окружения установлена в 1/true/yes.
func envBool(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return v == "1" || v == "true" || v == "yes"
}

// containsArg проверяет наличие флага среди аргументов командной строки.
func containsArg(flag string) bool {
	for _, a := range os.Args {
		if a == flag {
			return true
		}
	}
	return false
}

// envInt парсит целочисленную переменную окружения с запасным значением.
func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envDuration парсит длительность (например "24h") с запасным значением.
func envDuration(name string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
