package qaengineer

import (
	"ai/agents"
	"ai/agents/acceptor"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/api"
)

// qaToolNames — инструменты QA Engineer, выбираемые из общего реестра tools.
// Агент изучает API-контракты и существующие тесты (List, ReadFiles), пишет
// автотесты (WriteFiles, AppendFile, DeleteFiles) и запускает их одной
// консольной командой через Run.
var qaToolNames = []string{
	"WriteFiles", "ReadFiles", "DeleteFiles", "AppendFile", "List", "Run",
	tools.LspDefinition, tools.LspReferences, tools.LspHover, tools.ReadAppLogs,
}

// qaBoardToolNames — инструменты общей Kanban-доски, добавляемые
// QA-инженеру, когда оркестратор Kanban подключает доску (SetBoardStore):
// чтение своей задачи, смена её статуса и публикация багрепортов.
var qaBoardToolNames = []string{
	tools.BoardGetTask, tools.BoardSetTaskStatus, tools.BoardCreateBug, tools.BoardListBugs,
}

// QAEngineer — агент QA Engineer и приёмки (объединение QA и acceptor).
// Выполняет задачи по тестированию, поставленные QA Lead, строго следуя
// бизнес-требованиям и архитектурным контрактам от Архитектора: проверяет
// соответствие Backend и Frontend API-контрактам, пишет стабильные автотесты,
// запускаемые локально в Docker Compose одной консольной командой, а затем
// собирает проект и проводит приёмку собранного приложения.
type QAEngineer struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов
	// (OutputDir, MaxFiles, NoOverwrite). Поля и методы FileOps промотируются:
	// q.OutputDir, q.Write(..) и т.п. доступны напрямую.
	*tools.FileOps
	Prompt string
	Config Config
	// Tools — выбранные агентом инструменты из общего реестра
	// (единый источник для GetTools/GetToolsForOllama и диспетчеризации вызовов).
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis). Подключается оркестратором
	// Kanban через SetBoardStore; в standalone-режиме (CLI) — nil.
	Store *board.Store
}

// SetBoardStore подключает QA-инженера к общей Kanban-доске проекта: добавляет
// инструменты статуса задачи и публикации багрепортов (Board*), передаёт
// Board-контекст в реестр. Вызывается оркестратором Kanban при построении
// агента специалиста.
func (q *QAEngineer) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	q.Store = s
	names := append(append([]string{}, qaToolNames...), qaBoardToolNames...)
	q.Tools = tools.Select(names, tools.Deps{FileOps: q.FileOps, Board: s})
}

// NewQAEngineer создаёт QA-инженера в общей для всех агентов выходной папке
// temp/<projectName> в корне модуля. projectName — имя проекта, задаётся
// пользователем. prompt — текст задания от QA Lead (может быть пустым — тогда
// используется задание по умолчанию).
func NewQAEngineer(projectName, prompt string) *QAEngineer {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newQAEngineer(dir, prompt, LoadConfig())
}

// NewQAEngineerInDir создаёт QA-инженера в заданной директории (а не в новой
// temp/). Используется, когда нужно работать с уже существующей директорией
// проекта, где уже лежит код.
func NewQAEngineerInDir(prompt, dir string) (*QAEngineer, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newQAEngineer(abs, prompt, LoadConfig()), nil
}

// newQAEngineer создаёт агента в заданной директории: собирает *tools.FileOps
// (лимиты записи берутся из конфига) и выбирает из реестра инструменты
// QA-инженера. Используется всеми конструкторами.
func newQAEngineer(dir, prompt string, cfg Config) *QAEngineer {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &QAEngineer{
		FileOps: ops,
		Prompt:  prompt,
		Config:  cfg,
		Tools:   tools.Select(qaToolNames, tools.Deps{FileOps: ops}),
	}
}

func (q *QAEngineer) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: q.Prompt,
		},
	}
}

// RequiredToolFirstRound — QA-инженер не обязан обязательно вызывать
// конкретный инструмент в первом раунде: модель может начать с изучения
// API-контрактов и тестов (List/ReadFiles) или сразу записать автотесты.
func (q *QAEngineer) RequiredToolFirstRound() (string, bool) {
	return "", false
}

// verifyPlan — команды проверки, вычисленные тем же кодом, что и приёмка
// (agents/acceptor): env ACCEPT_* → корневой Makefile → автодетект по типу
// проекта. Раньше эти команды были описаны в промпте прозой, и QA н��ходил
// их сам: правитель забывал оговорку — тесты писались не той командой, какой
// проверяет приёмка, и «зелёные тесты» ничего не значили.
func (q *QAEngineer) verifyPlan() acceptor.VerifyPlan {
	root := q.OutputDir
	if root == "" {
		root = "."
	}
	return acceptor.VerifyPlanFor(root, root, acceptor.LoadConfig())
}

func (q *QAEngineer) GetSystemMessages(_ []agents.Message) []agents.Message {
	overwriteRule := "Ты можешь перезаписывать файлы инструментами WriteFiles."
	if q.Config.NoOverwrite {
		overwriteRule = "Включена защита от перезаписи: инструменты вернут ошибку, если ты попытаешься перезаписать уже существующий файл. Перезаписывай файлы только при необходимости."
	}

	// Команды проверки — из приёмки, не из прозы промпта. Помечаем тем, что
	// план пуст: это разные ситуации (нечего запускать vs. не определили).
	plan := q.verifyPlan()
	planRule := "КОМАНДЫ ПРОВЕРКИ (вычислены приёмкой, запускай именно их):\n" +
		"- сборка: " + orNone(plan.Build) + "\n" +
		"- анализ/линт: " + orNone(plan.Analyze) + "\n" +
		"- стиль: " + orNone(plan.Lint) + "\n" +
		"- запуск сервиса: " + orNone(plan.Start)
	if plan.Test != "" {
		// Подсказка нужна только когда команда есть: иначе «не определилась»
		// дублировалось бы в строке команды и в ВНИМАНИИ ниже.
		planRule += "\n- автотесты: " + plan.Test + " — " + plan.TestHint()
	}
	if plan.MakefileDir != "" {
		planRule += "\n- make-цели запускай из каталога " + plan.MakefileDir + " (монорепо: цели сами делают cd)"
		if plan.HasTarget("e2e") {
			planRule += "; в корневом Makefile есть самозавершающаяся цель 'make e2e' (up → проверки → down) — она предпочтительнее отдельных команд: одна команда, ноль зависших сервисов"
		}
	}
	if plan.Test == "" {
		planRule += "\n- автотесты: (не определена — выведи команду по стеку сам; в отчёте обязательно укажи, что автоопределение не сработало. Молча пропустить тесты нельзя)"
	}

	reportRule := ""
	if q.Config.ReportFile != "" {
		reportRule = "Составь отчёт о тестировании файлом " + q.Config.ReportFile + " через WriteFiles: статус прохождения тестов (Passed/Failed), технические метрики и логи ошибок."
	}

	// Правило Kanban-доски добавляется только когда доска подключена
	// (SetBoardStore): в standalone-режиме board-инструментов нет, и
	// упоминание их в промпте заставляет модель вызывать несуществующие
	// функции (как в developer.kanbanStep). Без этого правила QA никогда не
	// отмечал задачу сам и вся полагалась на fallback оркестратора.
	kanbanRule := ""
	if q.Store != nil {
		kanbanRule = `KANBAN-ДОСКА (инструменты Board* подключены):
- когда задача выполнена и проверки зелёные — переведи её в статус done инструментом BoardSetTaskStatus;
- если выполнить задачу невозможно (нужного кода или зависимостей в проекте нет, есть блокер) — переведи её в статус human_help с обязательной причиной (reason): это остановит задачу и передаст её человеку вместо повторов без результата. Остановка с объяснением — честнее, чем тесты «на то, чего нет в проекте».

`
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Ты — опытный QA Engineer и специалист по приёмке. Твоя цель — выполнять задачи по тестированию, поставленные QA Lead, строго следуя бизнес-требованиям и архитектурным контрактам от Архитектора.

Ты работаешь только внутри выходной директории проекта (OutputDir).
` + overwriteRule + `

` + planRule + `

` + kanbanRule + `СОСТАВ АВТОТЕСТОВ (пиши все три уровня, где они применимы к задаче):
- unit-тесты — бизнес-логика целевого модуля, без сети и без БД: границы, пустые значения, ошибочные входы. Для каждого теста, который ты пишешь на исправление дефекта, проверь, что он ПАДАЕТ на текущем коде и ПРОХОДИТ после исправления (иначе тест ничего не защищает);
- интеграционные — проверка API-контракта целиком: реальный HTTP-клиент против поднятого сервиса, настоящая БД в compose (Postgres/MySQL/Redis — по стеку проекта). Не мокай собственную логику: мокнутый тест проверяет твои ожидания, а не систему. Внешние зависимости (сторонние API) — мок или sandbox;
- E2E для веба — Playwright: конфигурация playwright.config.ts в репозитории, сценарии по пользовательским сценариям из ТЗ, проверки доступности (роль/текст кнопки), никаких фиксированных пауз sleep вместо ожидания готовности — только явные ожидания состояния.
Синхронизация и детерминизм: не полагайся на порядок тестов и на «случайный» порт — используй динамические порты/фикстуры, готовность сервиса проверяй опросом health, а не sleep.
Если инструмент недоступен в окружении (Playwright без браузеров, нет compose, нет БД) — это degrade: тесты и конфигурацию всё равно запиши, а в отчёте прямо укажи, что запуск невозможен в этом окружении и какой шаг не выполнен. Отсутствие запуска — не «Passed».

ПРАВИЛА РАБОТЫ:
1. Тестирование по контракту: Проверяй соответствие Backend и Frontend API-контрактам до последнего символа. Если обнаружено расхождение со схемой Архитектора — это блокирующий дефект.
2. Автоматизация: Пиши стабильные автотесты, которые могут быть запущены локально в Docker Compose одной консольной командой. Никаких «тесты работают только у меня на машине».
3. Приёмка: После успешного прохождения тестов собери проект согласно инструкциям и проведи приёмку (acceptance): определи тип проекта, собери его, запусти и проверь на корректность работы.

ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Отвечай строго по существу выполнения задачи — только через инструменты. Не пиши лишней «воды», только технические метрики, статус прохождения тестов (Passed/Failed) и логи ошибок.

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения API-контрактов, схем Архитектора и существующих тестов. Не читай большие файлы целиком ради одного контракта: ReadFiles с параметром lines ("lines": "40-70") открывает ровно нужный диапазон строк.
2. Напиши или обнови автотесты по СОСТАВУ АВТОТЕСТОВ инструментами WriteFiles (для точечных правок — AppendFile, для удаления — DeleteFiles); тесты пиши на языке и в структуре каталогов, принятых в проекте (существующие тесты — образец: прочитай их ReadFiles перед написанием).
3. Запусти автотесты через Run командой из раздела КОМАНДЫ ПРОВЕРКИ — она вычислена из Makefile и типа проекта тем же кодом, что и приёмка, поэтому твои тесты будут запущены ровно так же. Прежде чем писать тесты, прочитай корневой Makefile (ReadFiles) и package.json/go.mod: если там есть своя test-команда, она приоритетнее и её надо соблюстить. При наличии самозавершающейся цели 'make e2e' запускай её.
4. Проверь соответствие Backend и Frontend API-контрактам до последнего символа; расхождение со схемой Архитектора — блокирующий дефект.
5. Если тестирование прошло успешно, собери приложение и проведи приёмку командами из раздела КОМАНДЫ ПРОВЕРКИ (сборка, анализ/линт, стиль). НЕ запускай приложение через Run — ни бэкенд ("go run server/main.go"), ни фронтенд ("npm run dev", "npm start", "yarn dev", "pnpm dev" и т.п.) — такие процессы не завершаются сами и зависают до таймаута; для проверки поведения есть п.6.
6. Проверь поведение в рантайме инструментом ReadAppLogs: он запустит приложение, соберёт его stdout+stderr и остановит. Это единственный способ поймать дефекты, которых нет в юнит-тестах: «connection refused», «address already in use», падение миграции, отсутствующая колонка, 500 на живом endpoint. Запускай его на тех сценариях, ради которых писались тесты; способ запуска задаётся явно, если автоопределение не подошло ({"command": "npm run dev"}). Пустой результат со status skipped — это «логов нет», а не «дефектов нет»: проверь, что приложение вообще что-то печатает.
7. ` + reportRule + ` Для каждого дефекта: шаги воспроизведения, ожидаемый результат по контракту и фактический результат.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).` + agents.RunTokenEconomy,
		},
	}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (q *QAEngineer) GetTools() []tools.ToolDefinition {
	return q.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (q *QAEngineer) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(q.GetTools())
}

func (q *QAEngineer) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return q.Tools.Execute(functionName, functionArgs)
}

// Обёртки выбранных файловых инструментов. Наблюдаемые снаружи сигнатуры
// сохранены и просто делегируют в реестр (единый источник вызовов).

func (q *QAEngineer) WriteFiles(args map[string]any) ([]byte, error) {
	return q.Tools.Execute("WriteFiles", args)
}
func (q *QAEngineer) ReadFiles(args map[string]any) ([]byte, error) {
	return q.Tools.Execute("ReadFiles", args)
}
func (q *QAEngineer) DeleteFiles(args map[string]any) ([]byte, error) {
	return q.Tools.Execute("DeleteFiles", args)
}
func (q *QAEngineer) AppendFile(args map[string]any) ([]byte, error) {
	return q.Tools.Execute("AppendFile", args)
}
func (q *QAEngineer) List(args map[string]any) ([]byte, error) {
	return q.Tools.Execute("List", args)
}
func (q *QAEngineer) Run(args map[string]any) ([]byte, error) {
	return q.Tools.Execute("Run", args)
}

// orNone — печать команды в отчёте/промпте: пустая команда означает «нечего
// запускать», и явная пометка читается лучше пустой строки.
func orNone(cmd string) string {
	if strings.TrimSpace(cmd) == "" {
		return "(не определена — запускать нечего)"
	}
	return cmd
}

// Accept выполняет приёмку собранного приложения в директории dir:
// определяет тип проекта, собирает его, запускает на cfg.RunTimeout,
// анализирует вывод на предмет ошибок и возвращает структурированный отчёт.
// Приёмка полностью детерминирована: не требует вызова LLM.
func (q *QAEngineer) Accept(dir string) *acceptor.Report {
	return acceptor.Accept(dir, acceptor.LoadConfig())
}
