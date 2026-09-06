package main

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/agents/codereviewer"
	"ai/agents/planner"
	"ai/agents/refactor"
	"ai/checkpoint"
	// Blank-import регистрирует все встроенные провайдеры систем ревью
	// (init() в forges/github и forges/gitlab) в фабрике forges.New.
	"ai/forges"
	_ "ai/forges/all"
	"ai/models"
	"ai/services/mrlistener"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	// Load the .env file. Отсутствие файла не фатально: критичные настройки
	// (провайдер, токены) всё равно проверяются ниже по ходу выполнения.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("Предупреждение: .env не загружен (%v)", err)
	}

	// Сервис мониторинга новых MR — не требует провайдера модели.
	if len(os.Args) > 1 && os.Args[1] == "listen" {
		if err := mrlistener.Listen(context.Background()); err != nil {
			log.Fatal(err)
		}
		return
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
		log.Fatalf("Unknown provider: %s. Use 'ollama', 'yandex' or 'trim'", providerType)
	}

	if err != nil {
		log.Fatalf("Failed to init provider: %v", err)
	}

	// Провайдеры с одним раундом (например, trim) не умеют цикл NextChunk
	// у ревьювера — дифф передаём целиком без разбиения на части.
	noChunk := providerType == "trim"

	// Выбор агента по первому аргументу:
	// go run . generate <имя> [промпт] | refactor <имя> <промпт> | review <URL> | listen
	agentName, agentArgs := agentCommand(os.Args)

	var agent agents.Agent
	var planMode bool
	var planProject string
	var planPrompt string
	switch agentName {
	case "generate":
		if len(agentArgs) < 1 {
			log.Fatal("Использование: go run . generate <имя_проекта> [промпт]\n" +
				"Пример: go run . generate storageService \"Напиши микросервис для хранения файлов\"")
		}
		projectName := agentArgs[0]
		prompt := defaultPrompt(agentArgs[1:])
		agent = codegenerator.NewCodegenerator(projectName, prompt)
	case "plan":
		if len(agentArgs) < 2 {
			log.Fatal("Использование: go run . plan <имя_проекта> <промпт> [--resume]\n" +
				"Пример: go run . plan storageService \"Создай микросервис хранения файлов и ревью кода\"\n" +
				"Пример: go run . plan storageService --resume")
		}
		planMode = true
		planProject = agentArgs[0]
		planPrompt = strings.Join(agentArgs[1:], " ")
	case "refactor":
		if len(agentArgs) < 2 {
			log.Fatal("Использование: go run . refactor <имя_проекта> <промпт>\n" +
				"Пример: go run . refactor storageService \"Добавить эндпоинт /health\"")
		}
		projectName := agentArgs[0]
		prompt := strings.Join(agentArgs[1:], " ")
		agent, err = refactor.NewRefactorAgent(prompt, projectName)
		if err != nil {
			log.Fatalf("Ошибка: %v", err)
		}
	case "review":
		agent = codereviewer.NewCodereviewer(agentArgs)
		if noChunk {
			if cw, ok := agent.(*codereviewer.Codereviewer); ok {
				cw.NoChunk = true
			}
		}
	default:
		log.Fatalf("Неизвестный агент %q. Используйте 'go run . generate <имя> [промпт]', 'go run . refactor <имя> <промпт>', 'go run . plan <имя> <промпт>', 'go run . review <URL>' или 'go run . listen'", agentName)
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
		log.Fatalf("Error: %v", err)
	}

	if resp.Truncated {
		log.Println("Внимание: цикл агента остановлен по лимиту раундов, результат может быть неполным")
	}

	// 5. Цикл генерации/само-ревью для агента-генератора: после первого
	// прогона код проверяется локально (LocalForge), найденные замечания
	// передаются модели, и она исправляет код в той же OutputDir.
	if sr, ok := agent.(selfReviewer); ok {
		doSelfReview(ctx, provider, sr, "", defaultPrompt(agentArgs), noChunk)
	}

	// 6b. Запасной путь: если модель вернула ревью текстом, а не вызовами
	// инструментов (характерно для YandexGPT), а замечаний ещё не
	// опубликовано — пытаемся распарсить текст в комментарии и опубликовать.
	if r, ok := agent.(reviewParser); ok {
		n := r.PublishParsedReview(resp.Content)
		if n > 0 {
			log.Printf("Опубликовано замечаний из текстового ответа модели: %d", n)
		}
	}

	// 7. Итоговый отчёт-сводка в тред MR/PR, если агент его поддерживает.
	if r, ok := agent.(summarizer); ok {
		if serr := r.PostSummaryToPR(); serr != nil {
			log.Printf("Не удалось опубликовать итоговую сводку: %v", serr)
		}
	}

	// 8. Финальный отчёт агента (например, SUMMARY.md у генератора кода).
	if r, ok := agent.(finalizer); ok {
		r.Finalize()
	}

	fmt.Println("Response Message:", resp.Content)
	fmt.Println("Response Tools:", resp.ToolCalls)
}

// reviewParser — опциональный интерфейс агента, умеющего опубликовать ревью,
// которое модель написала текстом (без вызова инструментов).
type reviewParser interface {
	PublishParsedReview(content string) int
}

// selfReviewer — опциональный интерфейс агента-генератора, поддерживающего
// цикл self-repair: после первой генерации код прогоняется через локальное
// ревью, а найденные замечания передаются модели для исправления.
type selfReviewer interface {
	// SelfReviewDir возвращает путь к сгенерированному коду для ревью.
	SelfReviewDir() string
	// NewReviewAgentFor создаёт агента код-ревью для директории и возвращает
	// и агента, и его forge (чтобы извлечь собранные замечания).
	NewReviewAgentFor(dir string, focus string) (agents.Agent, forges.Forge, error)
	// FixPromptFor собирает текст задания для исправления кода на основе
	// замечаний ревью. Первый аргумент — текущее задание, второй — замечания.
	FixPromptFor(original string, comments []forges.ReviewComment) string
}

// summarizer — опциональный интерфейс агента, умеющего публиковать
// итоговую сводку ревью в тред MR/PR после завершения цикла.
type summarizer interface {
	PostSummaryToPR() error
}

// finalizer — опциональный интерфейс агента, выполняющего финализацию
// после завершения цикла (например, генератор кода пишет SUMMARY.md).
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

// doSelfReview запускает цикл self-repair для агента-генератора: генерация →
// локальное ревью сгенерированного кода → исправление по замечаниям ревью →
// повтор до схождения (нет замечаний) или исчерпания бюджета раундов.
// focus — цель ревью (может быть "" для общего обзора).
func doSelfReview(ctx context.Context, provider models.LLMProvider, sr selfReviewer, focus string, originalPrompt string, noChunk bool) {
	dir := sr.SelfReviewDir()

	// Узнаём бюджет раундов исправления из конфига генератора.
	maxRounds := codegenerator.LoadConfig().MaxRepairRounds
	if maxRounds <= 0 {
		log.Println("Self-review: исправление отключено (CODEGEN_MAX_REPAIR_ROUNDS<=0)")
		return
	}
	log.Printf("Self-review: ревью кода в %s (бюджет исправлений: %d)", dir, maxRounds)

	reviewAgent, forge, err := sr.NewReviewAgentFor(dir, focus)
	if err != nil {
		log.Printf("Self-review: не удалось создать агента ревью: %v", err)
		return
	}
	if noChunk {
		if cw, ok := reviewAgent.(*codereviewer.Codereviewer); ok {
			cw.NoChunk = true
		}
	}

	if _, err := provider.Generate(ctx, reviewAgent); err != nil {
		log.Printf("Self-review: ошибка ревью: %v", err)
	}

	lf, ok := forge.(*forges.LocalForge)
	if !ok {
		log.Printf("Self-review: неожиданный тип forge, пропускаю исправление")
		return
	}

	if len(lf.Published) == 0 {
		log.Println("Self-review: замечаний не найдено, исправление не требуется")
		return
	}

	// Цикл исправления: исправить → снова проверить ревью → повторять,
	// пока остаются замечания и не исчерпан бюджет раундов.
	pending := lf.Published
	for round := 1; round <= maxRounds && len(pending) > 0; round++ {
		log.Printf("Self-review: раунд %d/%d, замечаний: %d. Запускаю исправление.", round, maxRounds, len(pending))

		fixPrompt := sr.FixPromptFor(originalPrompt, pending)

		fixAgent, ferr := codegenerator.NewCodegeneratorInDir(fixPrompt, dir)
		if ferr != nil {
			log.Printf("Self-review: не удалось создать агента исправления: %v", ferr)
			break
		}

		if _, err := provider.Generate(ctx, fixAgent); err != nil {
			log.Printf("Self-review: ошибка на этапе исправления: %v", err)
			break
		}

		// После исправления финализируем: обновляем SUMMARY и гарантируем README.md.
		fixAgent.Finalize()

		if round >= maxRounds {
			break
		}

		// Перечитываем код после правок и смотрим, остались ли замечания.
		next, rerr := reReview(ctx, provider, sr, dir, focus, noChunk)
		if rerr != nil {
			log.Printf("Self-review: ошибка повторного ревью: %v", rerr)
			break
		}
		pending = next
	}

	if len(pending) > 0 {
		log.Printf("Self-review: цикл завершён, осталось неисправленных замечаний: %d", len(pending))
	} else {
		log.Printf("Self-review: цикл завершён, замечаний больше нет")
	}
}

// reReview создаёт свежий LocalForge для той же директории, прогоняет ревью
// и возвращает новые замечания. Используется между раундами исправления.
func reReview(ctx context.Context, provider models.LLMProvider, sr selfReviewer, dir string, focus string, noChunk bool) ([]forges.ReviewComment, error) {
	newAgent, newForge, err := sr.NewReviewAgentFor(dir, focus)
	if err != nil {
		return nil, err
	}
	if noChunk {
		if cw, ok := newAgent.(*codereviewer.Codereviewer); ok {
			cw.NoChunk = true
		}
	}
	if _, err := provider.Generate(ctx, newAgent); err != nil {
		return nil, err
	}
	lf, ok := newForge.(*forges.LocalForge)
	if !ok {
		return nil, fmt.Errorf("неожиданный тип forge при повторном ревью")
	}
	return lf.Published, nil
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
		log.Printf("Неверный REVIEW_TIMEOUT=%q, использую %v", raw, def)
		return def
	}
	return d
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
				log.Printf("Resume: план восстановлен из чекпоинта, завершено шагов: %d", len(snap.Completed))
			} else if perr != nil {
				log.Printf("Чекпоинт существует, но план в нём повреждён (%v), перепланирую", perr)
			}
		} else if err != checkpoint.ErrNotFound {
			log.Printf("Не удалось прочитать чекпоинт: %v", err)
		}
	}

	// Нет чекпоинта / resume не запрошен — строим план планировщиком.
	if plan == nil {
		plannerAgent := planner.NewPlanner(projectName, originalPrompt)
		resp, err := provider.Generate(ctx, plannerAgent)
		if err != nil {
			log.Fatalf("Получение плана: %v", err)
		}
		plan, err = planner.ParsePlan(resp.Content)
		if err != nil {
			log.Printf("Не удалось разобрать план из ответа планировщика: %v", err)
			log.Printf("Ответ планировщика:\n%s", resp.Content)
			peer := planner.NewPlanner(projectName, originalPrompt+"\n\nВерни план строго в формате JSON, без markdown-обёрток и лишнего текста.")
			planResp, perr := provider.Generate(ctx, peer)
			if perr != nil {
				log.Fatalf("Получение плана (повтор): %v", perr)
			}
			plan, err = planner.ParsePlan(planResp.Content)
			if err != nil {
				log.Fatalf("Повторный ответ планировщика не содержит корректного JSON-плана: %v", err)
			}
		}
	}

	log.Printf("План получен: %q (%d шагов)", plan.Summary, len(plan.Steps))
	for i, s := range plan.Steps {
		log.Printf("  %d. [%s] %s", i+1, s.Agent, s.Description)
	}

	exec := planner.NewExecutor(provider, plan)
	exec.SetCheckpoint(store, resume)
	if err := exec.Run(ctx); err != nil {
		log.Fatalf("Ошибка выполнения плана: %v", err)
	}

	log.Printf("План выполнен успешно. Проект: %q", plan.ProjectName)
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
			log.Fatalf("Resume невозможен: Redis недоступен (%v)", err)
		}
		log.Printf("Внимание: Redis недоступен (%v), работаю без чекпоинтов", err)
		return nil, resume
	}
	log.Printf("Контрольные точки включены: %s (чекпоинт: %s)", addr, store.Key())
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
