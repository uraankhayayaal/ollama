package main

import (
	"ai/agents"
	"ai/agents/codereviewer"
	"ai/agents/developer"
	"ai/application/usecases"

	apprag "ai/application/rag"

	// Blank-import регистрирует все встроенные провайдеры систем ревью
	// (init() в forges/github и forges/gitlab) в фабрике forges.New.
	_ "ai/infrastructure/forges/all"
	"ai/infrastructure/kafkarelay"
	"ai/infrastructure/llm"
	"ai/infrastructure/mrlistener"
	"ai/infrastructure/rag"
	"ai/internal/runctx"
	"ai/server"
	"ai/shared/logging"
	"ai/shared/projects"
	"context"
	"os"
	"path/filepath"
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

	// RAG-инъекции application-слою: фоновому индексатору — обходчик рабочей копии.
	apprag.SetProjectWalker(rag.WalkProject)

	// Сервис мониторинга новых MR — не требует провайдера модели.
	if len(os.Args) > 1 && os.Args[1] == "listen" {
		if err := mrlistener.Listen(context.Background()); err != nil {
			logging.Fatalf("%v", err)
		}
		return
	}

	// Индексация кодовой базы проекта в Qdrant (RAG).
	// Не требует провайдера модели — только эмбеддинги Ollama и Qdrant.
	// go run . index <имя_проекта>
	if len(os.Args) > 1 && os.Args[1] == "index" {
		if len(os.Args) < 3 || os.Args[2] == "" {
			logging.Fatalf("Использование: go run . index <имя_проекта>\nПример: go run . index storageService")
		}
		runIndexCommand(os.Args[2])
		os.Exit(0)
	}

	// Web UI: HTTP+WS сервер с Kanban-доской, чатом и HITL-контролем.
	// go run . serve [addr]  (по умолчанию 127.0.0.1:8090 или AI_WEB_ADDR)
	// Пароль Web UI задаётся AI_WEB_PASSWORD — при нём включается логин,
	// httpOnly-сессия, CSRF и rate-limit (см. server/auth.go).
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		addr := os.Getenv("AI_WEB_ADDR")
		if addr == "" {
			addr = "127.0.0.1:8090"
		}
		if len(os.Args) > 2 && os.Args[2] != "" {
			addr = os.Args[2]
		}
		srv, err := server.NewServer(server.Config{
			Addr:     addr,
			Password: os.Getenv("AI_WEB_PASSWORD"),
			// Наблюдаемость агентного цикла (agent-harness-metrics): без
			// KAFKA_BROKERS рлей выключен (nil) и ничего не публикуется.
			Relay: kafkarelay.FromEnv(),
		})
		if err != nil {
			logging.Fatalf("server: %v", err)
		}
		if os.Getenv("AI_WEB_PASSWORD") != "" {
			logging.Infof("server: включена аутентификация Web UI (AI_WEB_PASSWORD)")
		}
		if err := srv.Run(context.Background()); err != nil {
			logging.Fatalf("server: %v", err)
		}
		return
	}

	// Таймаут цикла агента берётся из окружения REVIEW_TIMEOUT, иначе 10 минут.
	// Дефолт выбран заведомо выше таймаута отдельного HTTP-запроса провайдера
	// (5 минут), чтобы один долгий ответ модели не съедал весь бюджет цикла.
	timeout := parseTimeout(os.Getenv("REVIEW_TIMEOUT"))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Провайдер LLM создаётся общей функцией (CLI и Web UI используют одну
	// логику выбора модели по окружению; подробности в llm.ResolveProvider).
	// Полный агентский цикл с эскалацией собирает application-слой из слоёв,
	// которые вернула инфраструктура.
	res, providerType, err := llm.ResolveProvider()
	if err != nil {
		logging.Fatalf("Failed to init provider: %v", err)
	}
	provider := usecases.NewEscalating(res.Small, res.Large)

	// Провайдеры с одним раундом (например, trim) не умеют цикл NextChunk
	// у ревьювера — дифф передаём целиком без разбиения на части.
	noChunk := providerType == "trim"

	// Выбор агента по первому аргументу:
	// go run . generate <имя> [промпт] | backend <имя> [промпт] | frontend <имя> [промпт] | review <URL> | listen
	agentName, agentArgs := agentCommand(os.Args)

	var agent agents.Agent
	var projName string
	switch agentName {
	case "generate", "backend":
		// "generate" оставлен как алиас для обратной совместимости: новое
		// имя — backend (агент «Backend разработчик»).
		if len(agentArgs) < 1 {
			logging.Fatalf("Использование: go run . backend <имя_проекта> [промпт]\n" +
				"Пример: go run . backend storageService \"Напиши микросервис для хранения файлов\"")
		}
		projectName := agentArgs[0]
		projName = projectName
		prompt := defaultPrompt(agentArgs[1:])
		agent = developer.NewBackendDeveloper(projectName, prompt)
	case "frontend":
		if len(agentArgs) < 1 {
			logging.Fatalf("Использование: go run . frontend <имя_проекта> [промпт]\n" +
				"Пример: go run . frontend storageService \"Напиши веб-интерфейс для просмотра файлов\"")
		}
		projectName := agentArgs[0]
		projName = projectName
		prompt := defaultPrompt(agentArgs[1:])
		agent = developer.NewFrontendDeveloper(projectName, prompt)
	case "plan":
		logging.Fatalf("Команда plan удалена — планирование теперь через чат-ассистент")
	case "devops":
		logging.Fatalf("Команда devops удалена — функции перенесены в Developer-агента")
	case "qa":
		logging.Fatalf("Команда qa удалена — функции перенесены в Developer-агента")
	case "kanban":
		logging.Fatalf("Команда kanban удалена — оркестрация через чат-ассистент")
	case "review":
		agent = codereviewer.NewCodereviewer(agentArgs)
		if noChunk {
			if cw, ok := agent.(*codereviewer.Codereviewer); ok {
				cw.NoChunk = true
			}
		}
	default:
		logging.Fatalf("Неизвестный агент %q. Используйте 'go run . generate <имя> [промпт]', 'go run . backend <имя> [промпт]', 'go run . frontend <имя> [промпт]', 'go run . review <URL>', 'go run . accept <имя>' или 'go run . listen'", agentName)
	}

	// Расширенное сжатие истории агентского цикла (Ф-6..Ф-11) для
	// standalone-агентов с проектным каталогом (RAG-вытеснение/ранжирование и
	// LSP-оглавления). У ревьювера проекта нет — контекст не трогаем.
	if projName != "" {
		ctx = runctx.WithCompression(ctx, projName, projects.ProjectDir(projName), rag.NewClientSafe(rag.Config{}))
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

// clipText обрезает длинный текст до n символов для читаемых логов.
func clipText(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
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
// generate/backend/frontend/plan/accept/index <имя> → имя; review → "review";
// listen → "mrlistener"; serve → "server" (в режиме Web UI один процесс ведёт
// много проектов, поэтому здесь именуется лог самого процесса, а лог каждого
// проекта — logs/<проект>.log через logging.For(project)).
func projectFromArgs(args []string) string {
	if len(args) < 2 {
		return "unnamed"
	}
	switch args[1] {
	case "generate", "backend", "frontend", "index":
		if len(args) >= 3 && args[2] != "" {
			return args[2]
		}
	case "review":
		return "review"
	case "listen":
		return "mrlistener"
	case "serve":
		return "server"
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

// runIndexCommand индексирует кодовую базу temp/<projectName> в Qdrant
// (см. PLAN-2026-09-19-done-qdrant.md, Ф-2): обходит дерево (игнор .git/node_modules/бинарных/
// .gitignore), нарезает файлы на структурные чанки и пакетно грузит векторы.
// Коллекция создаётся при первом запуске (create-if-not-exists).
func runIndexCommand(projectName string) {
	dir := projects.ProjectDir(projectName)

	client, err := rag.NewClient(rag.Config{})
	if err != nil {
		logging.Fatalf("index: %v", err)
	}
	defer client.Close()

	ctx := context.Background()
	dim, err := client.EnsureCollection(ctx)
	if err != nil {
		logging.Fatalf("index: %v", err)
	}

	files, err := rag.WalkProject(dir)
	if err != nil {
		logging.Fatalf("index: обход %s: %v", dir, err)
	}
	if len(files) == 0 {
		logging.Warnf("index: в %s не найдено индексируемых файлов", dir)
	}

	items := make([]rag.IndexItem, 0, len(files))
	for _, rel := range files {
		content, rerr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if rerr != nil {
			logging.Warnf("index: не прочитан %s: %v", rel, rerr)
			continue
		}
		items = append(items, rag.IndexItem{Path: rel, Scope: rag.ScopeForPath(rel), Content: string(content)})
	}

	opts := rag.DetectIndexOptions(dir)
	logging.Infof("index: ветка %s, коммит %s", opts.Branch, orDash(opts.CommitSHA))

	res, err := client.IndexProject(ctx, projectName, items, opts)
	if err != nil {
		logging.Fatalf("index: %v", err)
	}

	logging.Infof("Индексация %q завершена: файлов %d, чанков %d, размерность %d, ветка %s, коллекция %s",
		projectName, res.Files, res.Chunks, dim, opts.Branch, client.Collection())
	for _, e := range res.Errors {
		logging.Warnf("index: %s", e)
	}
}

// orDash — печать пустого значения коммита как «-».
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

