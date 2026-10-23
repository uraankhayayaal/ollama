//go:build e2e

package server

// Живой E2E полного цикла: от сообщения пользователя до рабочего продукта.
//
// Тест задействует всё ПО: оркестратор Kanban (архитектор → лид → специалист),
// доска на miniredis, PostgreSQL, Web UI через виртуальный браузер (chromedp):
// выбор проекта, дашборд, статусы задач.
//
// Провайдер LLM — детерминированный scripted (без сети): архитектор публикует
// бэклог (инфраструктурный эпик DevOps + прикладной эпик), лиды декомпозируют
// эпики, DevOps-инженер пишет инфраструктуру продукта (compose.yaml+Dockerfile),
// разработчик — код продукта (фикстура), QA переводит задачи в done. Харнес
// поднимает PostgreSQL по compose.yaml, написанному DevOps-инженером.
//
// Запуск:
//
//	E2E_FULL=1 go test -tags e2e -run TestE2ETodoAppFullFlow ./server/ -timeout 30m
//
// Нужны: Google Chrome, docker. Без них тест скипается.

import (
	"ai/agents/architect"
	"ai/agents/chatassist"
	"ai/agents/devops"
	"ai/agents/devopslead"
	"ai/agents/qaengineer"
	"ai/domain"
	"ai/domain/services"
	"ai/infrastructure/board"
	"ai/runner"
	"ai/shared/logging"
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
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const todoE2EProjectName = "todoapp-e2e"

// todoE2EArchitectArgs — бэклог архитектора для todo-приложения: два эпика —
// инфраструктурный (DevOps Lead, публикуется первым) и прикладной (Backend
// Lead). Порядок «инфраструктура → приложение» задан sequence_order и очередью
// лидов; прикладной эпик в декомпозиции зависит от инфраструктурного.
var todoE2EArchitectArgs = map[string]any{
	"architecture_summary": "Веб-приложение TODO с REST API на Go, хранение в PostgreSQL, веб-интерфейс. Локальная инфраструктура — PostgreSQL через Docker Compose.",
	"tasks": []map[string]any{
		{
			"task_id":          "ARCH-01",
			"title":            "Инфраструктура: PostgreSQL (Docker Compose) и Dockerfile",
			"description":      "Подготовить локальную инфраструктуру продукта: сервис PostgreSQL в compose.yaml (образ postgres:16-alpine, DATABASE_URL, healthcheck) и многостадийный Dockerfile для сборки приложения. Инфраструктуру пишет DevOps-инженер.",
			"assigned_role":    "DevOps Lead",
			"sequence_order":   1,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
		{
			"task_id":          "ARCH-02",
			"title":            "TODO веб-приложение (Go + PostgreSQL + веб)",
			"description":      "Создать полноценное веб-приложение TODO: сервер на Go с REST API, подключение к PostgreSQL через DATABASE_URL, веб-интерфейс с формой добавления, списком задач, переключением статуса выполнения и удалением.",
			"assigned_role":    "Backend Lead",
			"sequence_order":   2,
			"can_run_parallel": true,
			"dependencies":     []string{},
		},
	},
}

// todoE2EInfraLeadJSON — декомпозиция инфраструктурного эпика DevOps-лидом:
// одна задача DevOps-инженеру на compose.yaml + Dockerfile.
const todoE2EInfraLeadJSON = `{
  "lead_summary": "Инфраструктура TODO: PostgreSQL через Docker Compose и многостадийный Dockerfile",
  "tasks": [
    {
      "task_id": "INFRA-01",
      "title": "Подготовить PostgreSQL через Docker Compose",
      "description": "Создать compose.yaml с сервисом db (postgres:16-alpine, публикация на 127.0.0.1 со случайным портом, healthcheck pg_isready) и многостадийный Dockerfile для продукта. Приложение получает строку подключения через DATABASE_URL.",
      "assigned_role": "DevOps Engineer",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}`

// todoE2ELeadJSON — декомпозиция прикладного эпика Backend-лидом: две задачи
// (разработка + тестирование). Разработка ждёт готовности инфраструктурного
// эпика ARCH-01.
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
      "dependencies": ["ARCH-01"]
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

// Режимы поведения scripted-провайдера. Пусто — штатный happy-path (см.
// TestE2ETodoAppFullFlow). Остальные включают намеренную «поломку» модели,
// чтобы e2e проверял защиту оркестратора: зацикливание, зависшее тестирование,
// галлюцинацию (текстовый ответ вместо инструментов).
const (
	todoEModeHappy       = ""         // штатное поведение
	todoEModeDevLoop     = "dev-loop" // разработчик «зацикливается» (Looped)
	todoEModeQAStuck     = "qa-stuck" // QA не завершает задачу (остаётся testing)
	todoEModeDevTextOnly = "dev-text" // разработчик отвечает текстом без инструментов
)

// todoE2EProvider — детерминированный провайдер для e2e-теста.
// Имитирует модель на всех этапах оркестрации без сети.
type todoE2EProvider struct {
	t          *testing.T
	mode       string
	projectDir string
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
		// Лид направления: инфраструктурный эпик декомпозирует DevOps-лид,
		// прикладной — Backend-лид. Различаем по типу агента (JSON-fallback путь
		// оркестратора: лид не создаёт задачи инструментами доски).
		if _, ok := agent.(*devopslead.DevopsLead); ok {
			return &runner.AgentResponse{Content: todoE2EInfraLeadJSON}, nil
		}
		return &runner.AgentResponse{Content: todoE2ELeadJSON}, nil

	case strings.HasPrefix(umsg, "Ты — специалист"):
		taskID := todoETaskIDFromPrompt(umsg)
		switch a := agent.(type) {
		case *qaengineer.QAEngineer:
			// Режим «QA завис»: тестировщик отвечает текстом и НЕ меняет статус —
			// задача остаётся в testing. Оркестратор обязан заметить это и
			// эскалировать «работа не сделана» (вплоть до human_help).
			if p.mode == todoEModeQAStuck {
				return &runner.AgentResponse{Content: "Похоже, всё в порядке."}, nil
			}
			// QA: переводит задачу в done
			_, _ = a.CallFunction("BoardSetTaskStatus", map[string]any{"task_id": taskID, "status": "done"})
			return &runner.AgentResponse{Content: "Тестирование пройдено."}, nil
		case *devops.Devops:
			// DevOps-инженер: пишет инфраструктуру продукта (compose.yaml +
			// Dockerfile) инструментом WriteFiles, задачу → testing (продукт
			// приёмки поднимет БД по этому compose).
			if err := todoEWriteInfraFiles(a); err != nil {
				return nil, err
			}
			_, _ = a.CallFunction("BoardSetTaskStatus", map[string]any{"task_id": taskID, "status": "testing"})
			return &runner.AgentResponse{Content: "Инфраструктура подготовлена."}, nil
		default:
			// Режим «разработчик зациклился»: оркестратор должен прервать петлю
			// (escalateLoop → «зациклилась») и при исчерпании бюджета отдать
			// задачу человеку, а не крутиться вечно.
			if p.mode == todoEModeDevLoop {
				return &runner.AgentResponse{
					Looped:     true,
					LoopReason: "повторный вызов BoardSetTaskStatus с теми же аргументами",
				}, nil
			}
			// Режим «галлюцинация»: разработчик утверждает, что всё готово, но
			// НЕ пишет файлы и НЕ меняет статус. Оркестратор не должен верить
			// тексту — он обязан направить задачу на верификацию (testing).
			if p.mode == todoEModeDevTextOnly {
				return &runner.AgentResponse{Content: "Готово, задача реализована и проверена."}, nil
			}
			// Разработчик: пишет файлы продукта из фикстуры, задачу → testing
			productDir := p.projectDir
			if productDir == "" {
				productDir = projects.ProjectDir(todoE2EProjectName)
			}
			_ = todoEWriteFixtureFiles(productDir)
			_, _ = agent.CallFunction("BoardSetTaskStatus", map[string]any{"task_id": taskID, "status": "testing"})
			return &runner.AgentResponse{Content: "Задача выполнена."}, nil
		}

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

// todoEWriteInfraFiles заставляет DevOps-инженера записать инфраструктуру
// продукта (compose.yaml + Dockerfile) инструментом WriteFiles. Содержимое
// берётся из фикстур testdata/todoapp — scripted-провайдер играет роль модели,
// которая «сгенерировала» эти файлы. Агент пишет в свой OutputDir (каталог
// продукта), поэтому харнес затем поднимает БД ровно по этому compose.yaml.
func todoEWriteInfraFiles(d *devops.Devops) error {
	fixtureDir := filepath.Join("testdata", "todoapp")
	files := make([]map[string]string, 0, 2)
	for _, name := range []string{"compose.yaml", "Dockerfile"} {
		data, err := os.ReadFile(filepath.Join(fixtureDir, name))
		if err != nil {
			return err
		}
		files = append(files, map[string]string{"filename": name, "content": string(data)})
	}
	if _, err := d.CallFunction("WriteFiles", map[string]any{"files": files}); err != nil {
		return err
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

// Параметры детектора застоя. Опрос раз в todoEPollInterval, застой — когда
// снимок доски не меняется todoEStallRounds подряд И нет активных задач
// (~20 с). Это на порядок быстрее «глухого» дедлайна в 10 минут: залипшая
// (зациклившаяся/остановившаяся) оркестрация падает с диагнозом, а не молча
// выжигает бюджет.
const (
	todoEPollInterval = 500 * time.Millisecond
	todoEStallRounds  = 40
)

// todoEWaitForBoardDone ждёт, пока доска станет done, и падает быстро, если
// оркестрация застопорилась. В сообщении об ошибке — снимок доски с причинами.
func todoEWaitForBoardDone(t *testing.T, sess *Session, within time.Duration) {
	t.Helper()
	todoEWaitFor(t, sess, within, "доска полностью done", func(s *Session) bool {
		done, err := s.board.AllDone(context.Background())
		return err == nil && done
	})
}

// todoEWaitFor ждёт, пока pred не станет истиной, и падает быстро при застое
// (снимок доски не меняется todoEStallRounds подряд и нет активных задач) или
// по истечении within. Оба отказа печатают диагноз (pause_reason/last_error/
// эскалации), чтобы причина была видна на упавшем тесте.
func todoEWaitFor(t *testing.T, sess *Session, within time.Duration, desc string, pred func(*Session) bool) {
	t.Helper()
	if ok, diag := todoEPollUntil(sess, within, todoEStallRounds, pred); !ok {
		t.Fatalf("не дождались %q: %s", desc, diag)
	}
}

// todoEPollUntil опрашивает доску, пока pred не станет истиной. Возвращает
// ok=true, когда pred выполнен. Иначе — ok=false и диагноз: «застой» (снимок
// доски не меняется stallRounds подряд И нет активных задач) либо «таймаут»
// (within истёк). Вынесено из todoEWaitFor, чтобы поведение детектора можно
// было проверить тестом, не роняя тестовый процесс через t.Fatalf.
func todoEPollUntil(sess *Session, within time.Duration, stallRounds int, pred func(*Session) bool) (bool, string) {
	deadline := time.Now().Add(within)
	lastFP := ""
	frozen := 0
	for time.Now().Before(deadline) {
		if pred(sess) {
			return true, ""
		}
		fp := todoEBoardFingerprint(sess)
		if fp == lastFP && !todoEAnyActive(sess) {
			frozen++
		} else {
			frozen = 0
		}
		lastFP = fp
		if frozen >= stallRounds {
			return false, fmt.Sprintf("застой: доска не менялась %v и нет активных задач —\n%s",
				time.Duration(frozen)*todoEPollInterval, todoEStallReason(sess))
		}
		time.Sleep(todoEPollInterval)
	}
	return false, fmt.Sprintf("доска не изменилась за %v —\n%s", within, todoEStallReason(sess))
}

// todoEBoardFingerprint — стабильный снимок состояния доски: статусы эпиков и
// (статус, попытки, эскалации) задач. Любой прогресс меняет снимок.
func todoEBoardFingerprint(sess *Session) string {
	ctx := context.Background()
	epics, _ := sess.board.ListEpics(ctx)
	tasks, _ := sess.board.ListTasks(ctx)
	sort.Slice(epics, func(i, j int) bool { return epics[i].TaskID < epics[j].TaskID })
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	var b strings.Builder
	for _, e := range epics {
		fmt.Fprintf(&b, "E:%s=%s;", e.TaskID, e.Status)
	}
	for _, tk := range tasks {
		fmt.Fprintf(&b, "T:%s=%s/a%d/e%d;", tk.TaskID, tk.Status, tk.Attempts, tk.Escalations)
	}
	return b.String()
}

// todoEAnyActive — есть ли на доске задача, над которой прямо сейчас идёт
// работа (анализ архитектора / исполнение / тестирование). Пока такая задача
// есть, застой не объявляем: оркестратор занят.
func todoEAnyActive(sess *Session) bool {
	tasks, _ := sess.board.ListTasks(context.Background())
	for _, tk := range tasks {
		switch tk.Status {
		case board.StatusAnalysis, board.StatusInProgress, board.StatusTesting:
			return true
		}
	}
	return false
}

// todoEStallReason — человекочитаемый диагноз остановки: строки задач с
// попытками/эскалациями/причинами, с отдельным акцентом на human_help.
func todoEStallReason(sess *Session) string {
	tasks, _ := sess.board.ListTasks(context.Background())
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	var b strings.Builder
	var human []string
	for _, tk := range tasks {
		fmt.Fprintf(&b, "  %-10s %-12s attempts=%d escalations=%d", tk.TaskID, tk.Status, tk.Attempts, tk.Escalations)
		if tk.PauseReason != "" {
			fmt.Fprintf(&b, " pause=%q", todoEClip(tk.PauseReason, 200))
			if tk.Status == board.StatusHumanHelp {
				human = append(human, tk.TaskID+": "+tk.PauseReason)
			}
		}
		if tk.LastError != "" {
			fmt.Fprintf(&b, " err=%q", todoEClip(tk.LastError, 200))
		}
		b.WriteString("\n")
	}
	if len(human) > 0 {
		return "нужен человек (" + strings.Join(human, "; ") + "):\n" + b.String()
	}
	return b.String()
}

// todoEClip обрезает строку до n рун и схлопывает переводы строк (для логов).
func todoEClip(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ⏎ ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// todoEDumpBoard печатает полный post-mortem доски и хвост лога проекта. Зовётся
// из t.Cleanup при провалившемся тесте, чтобы причина была видна даже когда
// падение случилось не в ожидании доски.
func todoEDumpBoard(t *testing.T, sess *Session, logPath string) {
	t.Helper()
	ctx := context.Background()
	epics, _ := sess.board.ListEpics(ctx)
	tasks, _ := sess.board.ListTasks(ctx)
	sort.Slice(epics, func(i, j int) bool { return epics[i].TaskID < epics[j].TaskID })
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	t.Logf("=== POST-MORTEM доски: %d эпиков, %d задач ===", len(epics), len(tasks))
	for _, e := range epics {
		t.Logf("  эпик  %-10s %-12s %s", e.TaskID, e.Status, todoEClip(e.Title, 48))
	}
	for _, tk := range tasks {
		t.Logf("  задача %-10s %-12s assignee=%-12s attempts=%d escalations=%d",
			tk.TaskID, tk.Status, tk.Assignee, tk.Attempts, tk.Escalations)
		if tk.PauseReason != "" {
			t.Logf("        pause_reason: %s", todoEClip(tk.PauseReason, 300))
		}
		if tk.LastError != "" {
			t.Logf("        last_error: %s", todoEClip(tk.LastError, 300))
		}
		for _, c := range tk.Comments {
			if c.Type == domain.CommentTypeSystem || c.Type == domain.CommentTypeQA {
				t.Logf("        [%s/%s] %s", c.Author, c.Type, todoEClip(c.Body, 200))
			}
		}
	}
	todoEDumpLogTail(t, logPath)
}

// todoEDumpLogTail печатает последние строки лога проекта (если он есть).
func todoEDumpLogTail(t *testing.T, logPath string) {
	t.Helper()
	if logPath == "" {
		return
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Logf("--- лог проекта недоступен: %v ---", err)
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	const tail = 60
	start := 0
	if len(lines) > tail {
		start = len(lines) - tail
	}
	t.Logf("--- хвост лога проекта (%d строк, показываю %d) ---", len(lines), len(lines)-start)
	for _, ln := range lines[start:] {
		t.Logf("  %s", ln)
	}
}

// --- PostgreSQL harness -----------------------------------------------------
// Работает по паттерну miniredis.RunT(t): owned DSN, t.Cleanup снимает стек.
// БД поднимается по compose.yaml, который на прогоне написал DevOps-инженер
// (инфраструктура продукта), а не хардкодом теста.

const (
	// todoE2EComposeProject — имя compose-проекта (изолирует контейнеры прогона
	// от других стеков на машине).
	todoE2EComposeProject = "todoapp-e2e"
	// todoE2EComposeService — имя сервиса БД в compose.yaml продукта.
	todoE2EComposeService = "db"
	// todoE2EComposePort — внутренний порт postgres внутри контейнера.
	todoE2EComposePort = "5432"

	pgPostgresUser     = "postgres"
	pgPostgresPassword = "postgres"
	pgPostgresDatabase = "todoapp"
)

// postgresHarness — PostgreSQL, поднятый `docker compose up -d` по инфраструктуре
// продукта. DSN указывает на опубликованный compose-порт на 127.0.0.1.
type postgresHarness struct {
	dsn         string
	composeFile string
	projectName string
}

// newPostgres поднимает БД по compose.yaml продукта и возвращает харнес с DSN.
// Cleanup через t.Cleanup останавливает стек (`compose down -v`).
func newPostgres(t *testing.T, productDir string) *postgresHarness {
	t.Helper()

	composeFile := filepath.Join(productDir, "compose.yaml")
	if _, err := os.Stat(composeFile); err != nil {
		t.Fatalf("DevOps-инженер не создал инфраструктуру (%s): %v", composeFile, err)
	}
	if !dockerAvailable() {
		t.Skip("docker недоступен — приёмка продукта с реальным PostgreSQL пропущена")
	}

	pg := &postgresHarness{composeFile: composeFile, projectName: todoE2EComposeProject}
	t.Cleanup(func() { pg.close(t) })

	// Поднимаем только сервис БД: web-сервис продукта собирается и запускается
	// на хосте (см. todoEVerifyProduct), compose-образ ему не нужен.
	if out, err := pg.compose(t, "up", "-d", todoE2EComposeService); err != nil {
		t.Fatalf("docker compose up %s: %v\n%s", todoE2EComposeService, err, out)
	}

	port := pg.waitPort(t)
	pg.waitReady(t)

	pg.dsn = fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s?sslmode=disable",
		pgPostgresUser, pgPostgresPassword, port, pgPostgresDatabase)
	return pg
}

// waitPort опрашивает `compose port`, пока контейнер не опубликует порт БД.
// Порт публикуется случайным (см. compose.yaml) — уже занятый 5432 не мешает.
func (pg *postgresHarness) waitPort(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if out, err := pg.compose(t, "port", todoE2EComposeService, todoE2EComposePort); err == nil {
			if p := parseComposePort(string(out)); p > 0 {
				return p
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("не удалось определить опубликованный порт postgres за 60 с")
	return 0
}

// waitReady ждёт готовности postgres через pg_isready ВНУТРИ контейнера — это не
// требует pg_isready на хосте.
func (pg *postgresHarness) waitReady(t *testing.T) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if _, err := pg.compose(t, "exec", "-T", todoE2EComposeService,
			"pg_isready", "-U", pgPostgresUser, "-d", pgPostgresDatabase); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("postgres не стал готов за 60 с")
}

// DSN возвращает connection string поднятой БД.
func (pg *postgresHarness) DSN() string { return pg.dsn }

// close останавливает и удаляет стек БД (teardown: ошибки игнорируются).
func (pg *postgresHarness) close(t *testing.T) {
	_, _ = pg.compose(t, "down", "-v", "--remove-orphans")
}

// compose запускает `docker compose` для файла продукта в изолированном проекте.
func (pg *postgresHarness) compose(t *testing.T, args ...string) (string, error) {
	t.Helper()
	base := []string{"compose", "-f", pg.composeFile, "-p", pg.projectName}
	out, err := exec.Command("docker", append(base, args...)...).CombinedOutput()
	return string(out), err
}

// parseComposePort достаёт номер порта из вывода `docker compose port` (строки
// вида "127.0.0.1:54321", "0.0.0.0:54321", "[::]:54321").
func parseComposePort(out string) int {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		i := strings.LastIndex(line, ":")
		if i < 0 {
			continue
		}
		if p, err := strconv.Atoi(strings.TrimSpace(line[i+1:])); err == nil && p > 0 {
			return p
		}
	}
	return 0
}

// dockerAvailable сообщает, отвечает ли docker-демон.
func dockerAvailable() bool {
	return exec.Command("docker", "info").Run() == nil
}

// chromePath — путь к Chrome для chromedp: явный CHROME_PATH → бинарь в PATH
// (google-chrome/chromium — так в CI на Linux) → macOS-путь. Без Chrome
// интеграционный прогон не состоялся: это деграйд окружения, поэтому
// пропуск должен быть виден (make e2e помечает его INFRA_DEGRADED).
func chromePath() string {
	if p := strings.TrimSpace(os.Getenv("CHROME_PATH")); p != "" {
		return p
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	const darwinPath = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(darwinPath); err == nil {
		return darwinPath
	}
	return ""
}

// todoEStartBrowser создаёт headless Chrome через chromedp и возвращает контекст.
// Браузер живёт до конца теста (cleanup через t.Cleanup).
func todoEStartBrowser(t *testing.T) context.Context {
	t.Helper()
	chromePath := chromePath()
	if chromePath == "" {
		t.Skip("Chrome не найден (CHROME_PATH, google-chrome в PATH или macOS-путь) — интеграционный прогон пропущен")
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
		cmd.Env = append(os.Environ(), "DATABASE_URL="+dsn)
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

// todoEOrch — запущенная оркестрация на тестовом сервере со scripted-провайдером.
type todoEOrch struct {
	sess        *Session
	ts          *httptest.Server
	projectName string
	projectDir  string
	logDir      string
	projectLog  string
}

// todoEStartOrchestration поднимает тестовый сервер со scripted-провайдером,
// регистрирует проект и запускает оркестрацию через чат-ассистента. Одобрение
// HITL-затворов, post-mortem доски и очистка каталога продукта — на t.Cleanup.
// Вызывающий ДО этого должен выставить детерминизм окружения (логи, бюджеты,
// гейты) через t.Setenv.
func todoEStartOrchestration(t *testing.T, prov *todoE2EProvider, projectName string) *todoEOrch {
	t.Helper()
	logDir := t.TempDir()
	t.Setenv("LOG_DIR", logDir)
	logging.Setup("server")
	t.Cleanup(logging.Close)

	productDir := projects.ProjectDir(projectName)
	if err := os.RemoveAll(productDir); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(productDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(productDir) })
	prov.projectDir = productDir

	srv, handler, _ := newTestServer(t)
	srv.prov = stubProviderResolve(prov)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

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

	sess, _, err := srv.getOrCreate(opened.ProjectName)
	if err != nil {
		t.Fatalf("сессия проекта: %v", err)
	}

	o := &todoEOrch{
		sess:        sess,
		ts:          ts,
		projectName: opened.ProjectName,
		projectDir:  productDir,
		logDir:      logDir,
		projectLog:  filepath.Join(logDir, logging.ProjectLogName(opened.ProjectName)+".log"),
	}

	// Post-mortem: если тест упадёт на любой стадии, печатаем состояние доски
	// (статусы, попытки, эскалации, pause_reason/last_error) и хвост лога —
	// чтобы упавший тест сам показывал причину (зацикливание/застой/галлюцинацию).
	t.Cleanup(func() {
		if t.Failed() {
			todoEDumpBoard(t, sess, o.projectLog)
		}
	})

	// Запускаем оркестрацию через чат-ассистента
	code, raw = e2eJSON(t, "POST", ts.URL+"/api/projects/"+o.projectName+"/chat",
		map[string]any{"message": "Создай и реализуй todo-приложение с веб-интерфейсом и PostgreSQL. Оно должно работать как полноценный продукт."}, 30*time.Second)
	if code != http.StatusOK {
		t.Fatalf("POST chat: %d %s", code, raw)
	}

	// Фоновое одобрение HITL-затворов (эпики/задачи)
	gateCtx, gateCancel := context.WithCancel(context.Background())
	t.Cleanup(gateCancel)
	go todoEApproveGates(gateCtx, sess)

	return o
}

// todoETask находит задачу по ID на доске (nil — не найдена).
func todoETask(o *todoEOrch, id string) *domain.Task {
	tasks, _ := o.sess.board.ListTasks(context.Background())
	for _, tk := range tasks {
		if tk.TaskID == id {
			return tk
		}
	}
	return nil
}

// todoEHasTaskComment — есть ли у задачи комментарий (автор/тип), содержащий фрагмент.
func todoEHasTaskComment(o *todoEOrch, id, substr string) bool {
	tk := todoETask(o, id)
	if tk == nil {
		return false
	}
	for _, c := range tk.Comments {
		if strings.Contains(c.Body, substr) {
			return true
		}
	}
	return false
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

	// Логи, бюджеты и гейты оркестрации выставляются в bootstrap
	// (t.Setenv) внутри todoEStartOrchestration: там же включается файловый
	// логгер, чтобы панель «Логи» в Web UI не осталась пустой.
	o := todoEStartOrchestration(t, &todoE2EProvider{t: t, mode: todoEModeHappy}, todoE2EProjectName)
	sess, ts, projectName, productDir, projectLog := o.sess, o.ts, o.projectName, o.projectDir, o.projectLog

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

	// --- Логи работы оркестрации (панель «Логи») ---
	// logging.Setup выше направляет logging.For(project) в logs/<проект>.log;
	// здесь проверяем, что файл действительно заполнен работой харнеса, иначе
	// панель логов в Web UI осталась бы пустой.
	t.Run("OrchestrationLogs", func(t *testing.T) {
		data, err := os.ReadFile(projectLog)
		if err != nil {
			t.Fatalf("лог-файл проекта не создан: %v", err)
		}
		content := string(data)
		e2eCheck(t, "лог-файл не пуст", len(content) > 0, fmt.Sprintf("%d bytes", len(content)))
		for _, marker := range []string{"доска", "эпик"} {
			if !strings.Contains(content, marker) {
				t.Errorf("в логе нет оркестрационной строки %q", marker)
			}
		}
		t.Logf("лог проекта (%d байт) содержит работу оркестрации", len(content))
	})

	// --- PostgreSQL: поднимаем по инфраструктуре, которую написал DevOps ---
	// Инфраструктурный эпик завершён на предыдущем шаге, значит compose.yaml
	// продукта уже создан. Харнес поднимает БД строго по нему.
	pg := newPostgres(t, productDir)
	dsn := pg.DSN()

	// --- Проверка через виртуальный браузер ---
	browserCtx := todoEStartBrowser(t)
	todoEVerifyDashboard(t, browserCtx, ts, projectName, productDir)

	// --- Проверка рабочего продукта ---
	todoEVerifyProduct(t, browserCtx, productDir, dsn)

	t.Log("E2E полный цикл завершён успешно")
}

// TestE2EOrchestrationGuards проверяет, что оркестратор переживает «нездоровую»
// модель и останавливает работу с понятной причиной, а не зацикливается или
// не замирает. Каждый сценарий поднимает оркестрацию на отдельном проекте с
// низким бюджетом автономии (KANBAN_MAX_ESCALATIONS), чтобы защита срабатывала
// за секунды, и ассертирует вердикт доски. При застое любой сценарий падает
// быстро с диагнозом (todoEWaitFor + post-mortem), а не ждёт глобальный таймаут.
//
// Режимы провайдера (см. todoEMode*): dev-loop — разработчик зациклился;
// qa-stuck — QA не завершает задачу; dev-text — разработчик отвечает текстом
// без инструментов (галлюцинация), оркестратор не должен верить тексту.
func TestE2EOrchestrationGuards(t *testing.T) {
	if os.Getenv("E2E_FULL") == "" {
		t.Skip("E2E_FULL=1 не установлен")
	}
	budget := 1

	setEnv := func(t *testing.T) {
		t.Setenv("CODEGEN_SANDBOX", "host")
		t.Setenv("KANBAN_STALE_TASK_MIN", "0")
		t.Setenv("KANBAN_DONE_REQUIRES_COMMIT", "0")
		t.Setenv("CODEGEN_RUN_TIMEOUT", "60s")
		t.Setenv("KANBAN_MAX_ESCALATIONS", strconv.Itoa(budget))
	}

	scenarios := []struct {
		name       string
		dir        string
		mode       string
		waitWithin time.Duration
		waitDesc   string
		waitPred   func(*Session) bool
		assert     func(t *testing.T, o *todoEOrch)
	}{
		{
			name:       "Зацикливание разработчика",
			dir:        "todoapp-guard-loop",
			mode:       todoEModeDevLoop,
			waitWithin: 3 * time.Minute,
			waitDesc:   "задача T-01 ушла в human_help (зациклилась)",
			waitPred: func(s *Session) bool {
				return todoEStatus(s, "T-01") == board.StatusHumanHelp
			},
			assert: func(t *testing.T, o *todoEOrch) {
				tk := todoETask(o, "T-01")
				if tk == nil {
					t.Fatalf("задача T-01 не найдена")
				}
				e2eCheck(t, "T-01 human_help", tk.Status == board.StatusHumanHelp, string(tk.Status))
				e2eCheck(t, "T-01 причина «зациклилась»", strings.Contains(tk.PauseReason, "зациклилась"), tk.PauseReason)
				e2eCheck(t, "T-01 эскалации исчерпаны", tk.Escalations >= budget, fmt.Sprintf("%d", tk.Escalations))
				e2eCheck(t, "T-01 системный комментарий «Форсмажор»", todoEHasTaskComment(o, "T-01", "Форсмажор"), "")
			},
		},
		{
			name:       "QA завис на тестировании",
			dir:        "todoapp-guard-qa",
			mode:       todoEModeQAStuck,
			waitWithin: 3 * time.Minute,
			waitDesc:   "задача INFRA-01 ушла в human_help (работа не сделана)",
			waitPred: func(s *Session) bool {
				return todoEStatus(s, "INFRA-01") == board.StatusHumanHelp
			},
			assert: func(t *testing.T, o *todoEOrch) {
				tk := todoETask(o, "INFRA-01")
				if tk == nil {
					t.Fatalf("задача INFRA-01 не найдена")
				}
				e2eCheck(t, "INFRA-01 human_help", tk.Status == board.StatusHumanHelp, string(tk.Status))
				e2eCheck(t, "INFRA-01 причина «работа не сделана»", strings.Contains(tk.PauseReason, "работа не сделана"), tk.PauseReason)
				e2eCheck(t, "INFRA-01 эскалации исчерпаны", tk.Escalations >= budget, fmt.Sprintf("%d", tk.Escalations))
			},
		},
		{
			name:       "Разработчик галлюцинирует (текст без инструментов)",
			dir:        "todoapp-guard-text",
			mode:       todoEModeDevTextOnly,
			waitWithin: 3 * time.Minute,
			waitDesc:   "доска дошла до done (оркестратор не поверил тексту, провёл верификацию)",
			waitPred: func(s *Session) bool {
				done, err := s.board.AllDone(context.Background())
				return err == nil && done
			},
			assert: func(t *testing.T, o *todoEOrch) {
				// Разработчик лишь «заявил» готовность текстом, не вызвав
				// инструментов и не сменив статус. Оркестратор обязан не
				// поверить тексту и довести задачу через верификацию (testing →
				// QA) до done. Если бы фолбэк убрали, T-01 завис бы в работе и
				// сценарий провалился бы на waitPred со стойким застоем.
				done, err := o.sess.board.AllDone(context.Background())
				e2eCheck(t, "доска all done", err == nil && done, fmt.Sprintf("err=%v", err))
				tk := todoETask(o, "T-01")
				if tk == nil {
					t.Fatalf("задача T-01 не найдена")
				}
				e2eCheck(t, "T-01 done (через верификацию, не по тексту)", tk.Status == board.StatusDone, string(tk.Status))
			},
		},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			setEnv(t)
			o := todoEStartOrchestration(t, &todoE2EProvider{t: t, mode: sc.mode}, sc.dir)
			todoEWaitFor(t, o.sess, sc.waitWithin, sc.waitDesc, sc.waitPred)
			sc.assert(t, o)
		})
	}

	// Явная проверка самого детектора застоя: доводим доску до замороженного
	// состояния (зацикливание → human_help, активных задач нет) и ждём заведомо
	// недостижимое условие с малым бюджетом застоя. Детектор обязан вернуть
	// причину «застой» быстро, а не выжигать полный таймаут.
	t.Run("Детектор застоя срабатывает на замороженной доске", func(t *testing.T) {
		setEnv(t)
		o := todoEStartOrchestration(t, &todoE2EProvider{t: t, mode: todoEModeDevLoop}, "todoapp-guard-stall")
		todoEWaitFor(t, o.sess, 2*time.Minute, "доска заморожена (T-01 human_help)", func(s *Session) bool {
			return todoEStatus(s, "T-01") == board.StatusHumanHelp
		})

		start := time.Now()
		ok, diag := todoEPollUntil(o.sess, 30*time.Second, 4, func(s *Session) bool {
			done, err := s.board.AllDone(context.Background())
			return err == nil && done
		})
		elapsed := time.Since(start)

		e2eCheck(t, "детектор не дождался недостижимого done", !ok, "")
		e2eCheck(t, "диагноз содержит «застой»", strings.Contains(diag, "застой"), diag)
		e2eCheck(t, "детектор сработал раньше таймаута 30s", elapsed < 30*time.Second, elapsed.String())
		t.Logf("детектор застоя сработал за %v: %s", elapsed, strings.SplitN(diag, "\n", 2)[0])
	})
}

// todoEStatus — статус задачи по ID (пусто, если задача не найдена).
func todoEStatus(s *Session, id string) board.Status {
	tasks, _ := s.board.ListTasks(context.Background())
	for _, tk := range tasks {
		if tk.TaskID == id {
			return tk.Status
		}
	}
	return ""
}
