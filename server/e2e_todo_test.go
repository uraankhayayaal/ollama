//go:build e2e

package server

// Живой E2E полного цикла: от сообщения пользователя до рабочего продукта.
//
// Тест задействует всё ПО: оркестратор Kanban (архитектор → лид → специалист),
// доска на miniredis, PostgreSQL (docker или TEST_DATABASE_URL), Web UI через
// виртуальный браузер (chromedp): выбор проекта, дашборд, статусы задач.
//
// Провайдер LLM — детерминированный scripted (без сети): архитектор публикует
// бэклог, лид декомпозирует эпик, разработчик пишет файлы продукта (фикстура),
// QA переводит задачи в done.
//
// Запуск:
//
//	E2E_FULL=1 go test -tags e2e -run TestE2ETodoAppFullFlow ./server/ -timeout 30m
//
// Нужны: Google Chrome, docker (или TEST_DATABASE_URL). Без них тест скипается.

import (
	"ai/agents/architect"
	"ai/agents/chatassist"
	"ai/agents/qaengineer"
	"ai/domain/services"
	"ai/infrastructure/board"
	"ai/runner"
	"ai/shared/projects"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const todoE2EProjectName = "todoapp-e2e"

// todoE2EArchitectArgs — бэклог архитектора для todo-приложения
var todoE2EArchitectArgs = map[string]any{
	"architecture_summary": "Веб-приложение TODO с REST API на Go, хранение в PostgreSQL, веб-интерфейс.",
	"tasks": []map[string]any{
		{
			"task_id":          "ARCH-01",
			"title":            "TODO веб-приложение (Go + PostgreSQL + веб)",
			"description":      "Создать полноценное веб-приложение TODO: сервер на Go с REST API, подключение к PostgreSQL, веб-интерфейс с формой добавления, списком задач, переключением статуса выполнения и удалением.",
			"assigned_role":    "Backend Lead",
			"sequence_order":   1,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
	},
}

// todoE2ELeadJSON — декомпозиция эпика лидом: две задачи (разработка + тестирование)
const todoE2ELeadJSON = `{
  "lead_summary": "Разбивка TODO-приложения на задачи разработки",
  "tasks": [
    {
      "task_id": "T-01",
      "title": "Реализовать TODO-приложение (Go + PostgreSQL + веб-интерфейс)",
      "description": "Создать веб-приложение TODO с REST API на Go, интеграцией с PostgreSQL (lib/pq), веб-интерфейсом с формой добавления задач, списком задач с возможностью отметить выполненным/невыполненным, удалением задач.",
      "assigned_role": "Go Developer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    },
    {
      "task_id": "T-02",
      "title": "Проверить и протестировать TODO-приложение",
      "description": "Провести тестирование реализованного TODO-приложения: запустить unit-тесты, проверить работу с PostgreSQL, убедиться что все функциональные возможности работают корректно.",
      "assigned_role": "QA Engineer",
      "sequence_order": 2,
      "can_run_parallel": false,
      "dependencies": ["T-01"]
    }
  ]
}`

// todoE2EProvider — детерминированный провайдер для e2e-теста.
// Имитирует модель на всех этапах оркестрации без сети.
type todoE2EProvider struct {
	t *testing.T
}

func (p *todoE2EProvider) Generate(ctx context.Context, agent services.Agent) (*runner.AgentResponse, error) {
	p.t.Helper()

	// Ассистент: запускаем оркестрацию через KanbanStart
	if _, ok := agent.(*chatassist.Assistant); ok {
		task := ""
		if us := agent.GetUserMessages(); len(us) > 0 {
			task = us[len(us)-1].Message
		}
		if a, ok := agent.(*chatassist.Assistant); ok {
			_, _ = a.CallFunction(actionKanbanStart, map[string]any{"task": task})
		}
		return &runner.AgentResponse{Content: ""}, nil
	}

	var umsg string
	if us := agent.GetUserMessages(); len(us) > 0 {
		umsg = us[0].Message
	}

	switch {
	case strings.HasPrefix(umsg, "Декомпозируй эпик"):
		// Лид: возвращаем JSON-декомпозицию (fallback-путь оркестратора)
		return &runner.AgentResponse{Content: todoE2ELeadJSON}, nil

	case strings.HasPrefix(umsg, "Ты — специалист"):
		taskID := todoETaskIDFromPrompt(umsg)
		if _, ok := agent.(*qaengineer.QAEngineer); ok {
			// QA: переводит задачу в done
			_, _ = agent.CallFunction("BoardSetTaskStatus", map[string]any{"task_id": taskID, "status": "done"})
			return &runner.AgentResponse{Content: "Тестирование пройдено."}, nil
		}
		// Разработчик: пишет файлы продукта из фикстуры, задачу → testing
		productDir := projects.ProjectDir(todoE2EProjectName)
		_ = todoEWriteFixtureFiles(productDir)
		_, _ = agent.CallFunction("BoardSetTaskStatus", map[string]any{"task_id": taskID, "status": "testing"})
		return &runner.AgentResponse{Content: "Задача выполнена."}, nil

	case strings.HasPrefix(umsg, "Проведи ревизию"):
		// Ревьюер эпиков-черновиков: в нашем сценарии черновиков нет
		return &runner.AgentResponse{Content: ""}, nil

	default:
		// Архитектор: публикует бэклог
		if a, ok := agent.(*architect.Architect); ok {
			_, _ = a.CallFunction(architect.SubmitBacklogToolName, todoE2EArchitectArgs)
		}
		return &runner.AgentResponse{Content: ""}, nil
	}
}

// todoETaskIDFromPrompt извлекает task_id из промпта специалиста
func todoETaskIDFromPrompt(umsg string) string {
	const marker = "Задача на доске: "
	i := strings.Index(umsg, marker)
	if i < 0 {
		return ""
	}
	rest := umsg[i+len(marker):]
	if j := strings.Index(rest, " — "); j >= 0 {
		rest = rest[:j]
	} else if j := strings.IndexAny(rest, " \n\t"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// todoEWriteFixtureFiles копирует файлы продукта из testdata/todoapp в productDir
func todoEWriteFixtureFiles(productDir string) error {
	fixtureDir := filepath.Join("testdata", "todoapp")
	entries := []string{"go.mod", "go.sum", "main.go", "main_test.go", "schema.sql"}
	for _, name := range entries {
		data, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(productDir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// todoEApproveGates непрерывно одобряет HITL-затворы (epics, tasks)
func todoEApproveGates(ctx context.Context, sess *Session) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		gating, gateTyp, _ := sess.gateOf().State()
		if gating && gateTyp != "" {
			_ = sess.approve(gateTyp, true, "")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// todoEWaitForBoardDone ждёт, пока доска станет done (все эпики и задачи выполнены)
func todoEWaitForBoardDone(t *testing.T, sess *Session, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		done, err := sess.board.AllDone(context.Background())
		if err == nil && done {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("доска не стала done за %v", within)
}

// startPostgres запускает контейнер postgres:15-alpine или использует TEST_DATABASE_URL.
// Возвращает DSN для подключения.
func startPostgres(t *testing.T) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")); v != "" {
		return v
	}

	containerName := fmt.Sprintf("todoapp-e2e-pg-%d", time.Now().Unix())
	cmd := exec.Command("docker", "run", "-d", "--rm",
		"--name", containerName,
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_DB=todoapp",
		"-p", "127.0.0.1:0:5432",
		"postgres:15-alpine")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("не удалось запустить postgres: %v, out=%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})

	// Получаем порт
	portCmd := exec.Command("docker", "port", containerName, "5432")
	portOut, err := portCmd.CombinedOutput()
	if err != nil {
		t.Skipf("docker port: %v", err)
	}
	portStr := strings.TrimSpace(string(portOut))
	parts := strings.Split(portStr, ":")
	port := parts[len(parts)-1]
	if port == "" {
		t.Skip("не удалось получить порт postgres")
	}

	dsn := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%s/todoapp?sslmode=disable", port)

	// Ждём готовности
	for i := 0; i < 30; i++ {
		check := exec.Command("pg_isready", "-h", "127.0.0.1", "-p", port, "-U", "postgres")
		if check.Run() == nil {
			return dsn
		}
		time.Sleep(1 * time.Second)
	}
	t.Skip("postgres не готов")
	return dsn
}

// todoEStartBrowser создаёт headless Chrome через chromedp и возвращает контекст.
// Браузер живёт до конца теста (cleanup через t.Cleanup).
func todoEStartBrowser(t *testing.T) context.Context {
	t.Helper()
	chromePath := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(chromePath); os.IsNotExist(err) {
		t.Skipf("Chrome не найден: %s", chromePath)
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.WindowSize(1280, 800),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(func() {
		cancelCtx()
		cancelAlloc()
	})
	return ctx
}

// todoENavigateWithTimeout переходит по URL и ждёт селектор с таймаутом через child-ctx.
func todoENavigateWithTimeout(ctx context.Context, url, sel string, timeout time.Duration) error {
	if err := chromedp.Do(ctx, chromedp.Navigate(url)); err != nil {
		return fmt.Errorf("navigate %s: %w", url, err)
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return chromedp.Do(wctx, chromedp.WaitVisible(sel))
}

// todoEClickIfExists кликает элемент, если он есть (короткий child-ctx, ошибки игнорируются).
func todoEClickIfExists(ctx context.Context, sel string) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = chromedp.Do(cctx, chromedp.Click(sel))
}

// todoETextOf возвращает текст элемента (пустая строка, если элемента нет).
func todoETextOf(ctx context.Context, sel string) string {
	text, _ := chromedp.Run(ctx, chromedp.EvaluateAsDevTools[string](
		fmt.Sprintf(`document.querySelector(%q)?.textContent || ""`, sel)))
	return text
}

// todoEVerifyDashboard открывает Web UI, выбирает проект, проверяет дашборд.
func todoEVerifyDashboard(t *testing.T, ctx context.Context, ts *httptest.Server, projectName, productDir string) {
	t.Helper()

	baseURL := ts.URL

	// Открываем приложение
	if err := todoENavigateWithTimeout(ctx, baseURL, ".app", 30*time.Second); err != nil {
		t.Fatalf("не удалось открыть приложение: %v", err)
	}

	// Ждём загрузки
	chromedp.Do(ctx, chromedp.Sleep(500*time.Millisecond))

	// Если есть picker — выбираем проект
	hasPicker, _ := chromedp.Run(ctx, chromedp.Evaluate[bool](`document.querySelector(".picker") !== null`))

	if hasPicker {
		// Пробуем выбрать из выпадающего списка
		selected, _ := chromedp.Run(ctx, chromedp.Evaluate[bool](fmt.Sprintf(`
			(() => {
				const sel = document.querySelector('.picker select');
				if (!sel) return false;
				for (const opt of sel.options) {
					if (opt.value === %q || opt.textContent === %q) {
						sel.value = opt.value;
						sel.dispatchEvent(new Event('change', {bubbles: true}));
						return true;
					}
				}
				return false;
			})()
		`, projectName, projectName)))

		if !selected {
			// Вводим путь в форму и отправляем
			chromedp.Do(ctx,
				chromedp.SendKeys(".picker-form input", productDir),
				chromedp.Click(".picker-form button.btn.primary"),
			)
		}

		// Ждём появления дашборда
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := chromedp.Do(wctx, chromedp.WaitVisible(".dashboard")); err != nil {
			t.Fatalf("дашборд не появился: %v", err)
		}
	} else {
		// Дашборд уже открыт (проект выбран автоматически)
		wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := chromedp.Do(wctx, chromedp.WaitVisible(".dashboard")); err != nil {
			t.Fatalf("дашборд не появился (picker не было): %v", err)
		}
	}

	// Проверяем статус — должен быть "done"
	badgeText := todoETextOf(ctx, ".statusline .badge")
	e2eCheck(t, "статус = done", strings.Contains(strings.ToLower(badgeText), "done"), badgeText)

	// Проверяем количество выполненных задач
	doneCount := todoETextOf(ctx, ".grid-head.done b")
	e2eCheck(t, "есть выполненные задачи", doneCount != "" && doneCount != "0", doneCount)

	// Раскрываем эпик (если свёрнут)
	todoEClickIfExists(ctx, ".epic-head button.toggle")
	chromedp.Do(ctx, chromedp.Sleep(200*time.Millisecond))

	// Проверяем, что задачи в done
	doneTasks, _ := chromedp.Run(ctx, chromedp.Evaluate[int](
		`document.querySelectorAll('.cell.done ul li.task').length`))
	e2eCheck(t, "задачи в колонке done", doneTasks > 0, fmt.Sprintf("done tasks: %d", doneTasks))

	// Скриншот дашборда
	screenshot, serr := chromedp.Run(ctx, chromedp.FullScreenshot(90))
	if serr == nil {
		screenshotPath := filepath.Join(t.TempDir(), "dashboard.png")
		if err := os.WriteFile(screenshotPath, screenshot, 0o644); err == nil {
			t.Logf("скриншот дашборда: %s", screenshotPath)
		}
	}
}

// todoEVerifyProduct проверяет рабочий продукт: тесты, сборка, запуск, UI.
func todoEVerifyProduct(t *testing.T, ctx context.Context, productDir, dsn string) {
	t.Helper()

	// --- go test ---
	t.Run("ProductGoTest", func(t *testing.T) {
		cmd := exec.Command("go", "test", "./...")
		cmd.Dir = productDir
		cmd.Env = append(os.Environ(), "TEST_DATABASE_URL="+dsn)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go test ./... в продукте: %v\n%s", err, out)
		}
	})

	// --- go build ---
	t.Run("ProductGoBuild", func(t *testing.T) {
		cmd := exec.Command("go", "build", "-o", "todoapp", ".")
		cmd.Dir = productDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go build в продукте: %v\n%s", err, out)
		}
	})

	// --- запуск сервера и проверка UI ---
	binPath := filepath.Join(productDir, "todoapp")

	// Свободный 	port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("не удалось найти свободный порт: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Запускаем сервер
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(),
		"DATABASE_URL="+dsn,
		"ADDR="+addr,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("не удалось запустить продукт: %v", err)
	}
	defer cmd.Process.Kill()

	// Ждём готовности
	productURL := "http://" + addr
	for i := 0; i < 30; i++ {
		resp, err := http.Get(productURL + "/")
		if err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// --- браузер: добавляем todo ---
	if err := todoENavigateWithTimeout(ctx, productURL, ".app", 15*time.Second); err != nil {
		t.Fatalf("не удалось открыть продукт: %v", err)
	}

	// Вводим текст и отправляем форму
	chromedp.Do(ctx,
		chromedp.SendKeys("form.add input[name=title]", "E2E: купить молоко"),
		chromedp.Click("form.add button.btn.primary"),
	)

	// Ждём появления todo в списке
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := chromedp.Do(wctx, chromedp.WaitVisible("ul.todos li .title")); err != nil {
		t.Fatalf("todo не появился в списке: %v", err)
	}

	// Проверяем текст
	todoText := todoETextOf(ctx, "ul.todos li .title")
	e2eCheck(t, "todo добавлен", strings.Contains(todoText, "E2E: купить молоко"), todoText)

	// Перезагружаем страницу — todo должен остаться (персистентность в БД)
	if err := todoENavigateWithTimeout(ctx, productURL, "ul.todos li .title", 15*time.Second); err != nil {
		t.Fatalf("не удалось перезагрузить продукт: %v", err)
	}

	todoText2 := todoETextOf(ctx, "ul.todos li .title")
	e2eCheck(t, "todo персистентен", strings.Contains(todoText2, "E2E: купить молоко"), todoText2)

	// Скриншот продукта
	screenshot, serr := chromedp.Run(ctx, chromedp.FullScreenshot(90))
	if serr == nil {
		screenshotPath := filepath.Join(t.TempDir(), "product.png")
		if err := os.WriteFile(screenshotPath, screenshot, 0o644); err == nil {
			t.Logf("скриншот продукта: %s", screenshotPath)
		}
	}
}

func TestE2ETodoAppFullFlow(t *testing.T) {
	// Гейт: запуск только с E2E_FULL=1
	if os.Getenv("E2E_FULL") == "" {
		t.Skip("E2E_FULL=1 не установлен")
	}

	// Детерминизм окружения
	t.Setenv("CODEGEN_SANDBOX", "host")
	t.Setenv("KANBAN_STALE_TASK_MIN", "0")
	t.Setenv("KANBAN_DONE_REQUIRES_COMMIT", "0")
	t.Setenv("CODEGEN_RUN_TIMEOUT", "180s")

	// PostgreSQL
	dsn := startPostgres(t)
	t.Setenv("TEST_DATABASE_URL", dsn)

	// Очищаем и создаём productDir
	productDir := projects.ProjectDir(todoE2EProjectName)
	if err := os.RemoveAll(productDir); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(productDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(productDir) })

	// Тестовый сервер
	srv, handler, _ := newTestServer(t)

	// Инжектим scripted провайдер
	prov := &todoE2EProvider{t: t}
	srv.prov = stubProviderResolve(prov)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// Регистрируем проект через REST API (как в e2e_model_selection_test)
	code, raw := e2eJSON(t, "POST", ts.URL+"/api/projects",
		map[string]any{"path_or_git": productDir}, 30*time.Second)
	if code != http.StatusOK {
		t.Fatalf("POST /api/projects: %d %s", code, raw)
	}
	var opened struct {
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(raw, &opened); err != nil || opened.ProjectName == "" {
		t.Fatalf("открытие проекта: %s (%v)", raw, err)
	}
	projectName := opened.ProjectName

	// Получаем сессию
	sess, _, err := srv.getOrCreate(projectName)
	if err != nil {
		t.Fatalf("сессия проекта: %v", err)
	}

	// Запускаем оркестрацию через чат-ассистента
	code, raw = e2eJSON(t, "POST", ts.URL+"/api/projects/"+projectName+"/chat",
		map[string]any{"message": "Создай и реализуй todo-приложение с веб-интерфейсом и PostgreSQL. Оно должно работать как полноценный продукт."}, 30*time.Second)
	if code != http.StatusOK {
		t.Fatalf("POST chat: %d %s", code, raw)
	}

	// Запускаем фоновое одобрение затворов
	gateCtx, gateCancel := context.WithCancel(context.Background())
	defer gateCancel()
	go todoEApproveGates(gateCtx, sess)

	// Ждём завершения оркестрации
	t.Log("оркестрация запущена, ждём завершения...")
	todoEWaitForBoardDone(t, sess, 10*time.Minute)

	// Проверяем состояние доски через store
	ctx := context.Background()
	done, _ := sess.board.AllDone(ctx)
	e2eCheck(t, "доска all done", done, fmt.Sprintf("done=%v", done))

	epics, _ := sess.board.ListEpics(ctx)
	e2eCheck(t, "есть эпики", len(epics) > 0, fmt.Sprintf("epics: %d", len(epics)))
	for _, e := range epics {
		e2eCheck(t, fmt.Sprintf("эпик %s done", e.TaskID), e.Status == board.StatusDone, string(e.Status))
	}

	tasks, _ := sess.board.ListTasks(ctx)
	e2eCheck(t, "есть задачи", len(tasks) > 0, fmt.Sprintf("tasks: %d", len(tasks)))
	for _, task := range tasks {
		e2eCheck(t, fmt.Sprintf("задача %s done", task.TaskID), task.Status == board.StatusDone, string(task.Status))
	}

	// --- Проверка через виртуальный браузер ---
	browserCtx := todoEStartBrowser(t)
	todoEVerifyDashboard(t, browserCtx, ts, projectName, productDir)

	// --- Проверка рабочего продукта ---
	todoEVerifyProduct(t, browserCtx, productDir, dsn)

	t.Log("E2E полный цикл завершён успешно")
}
