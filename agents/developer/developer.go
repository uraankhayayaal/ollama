// Package developer — агенты «Backend разработчик» и «Frontend разработчик».
// Выполняют задачи разработки кода в монорепозитории temp/<проект>: создают
// недостающую структуру с нуля и дорабатывают существующий код. В Kanban
// подключаются к доске (BoardGetTask/BoardSetTaskStatus) и сами отмечают свою
// задачу как выполненную по завершении работы.
package developer

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"fmt"
	"os"

	"github.com/ollama/ollama/api"
)

// devToolNames — инструменты разработчика, выбираемые из общего реестра tools.
// Агент изучает проект (List, ReadFiles), создаёт и правит файлы (WriteFiles,
// AppendFile, DeleteFiles), вносит точечные правки (SearchReplace — универсальный
// SEARCH/REPLACE для любого языка, PatchGoFunction — семантическая замена одной
// Go-функции через go/ast) и проверяет сборку (Run).
var devToolNames = []string{
	"WriteFiles", "ReadFiles", "ReadMap", "DeleteFiles", "Run", "List", "AppendFile",
	"SearchReplace", "PatchGoFunction",
}

// devBoardToolNames — инструменты общей Kanban-доски, добавляемые разработчику,
// когда оркестратор Kanban подключает доску (SetBoardStore): чтение своей задачи
// и смена её статуса (по завершении работы разработчик сам переводит задачу в
// выполнена через BoardSetTaskStatus).
var devBoardToolNames = []string{
	tools.BoardGetTask, tools.BoardSetTaskStatus,
}

// base — общая часть агентов-разработчиков (backend/frontend): разделяемый
// файловый контекст, конфиг, выбранные инструменты и подключение к Kanban-доске.
// Встраивается в BackendDeveloper и FrontendDeveloper; различие между
// специализациями — только в инструкции роли (roleDesc) и префиксе логов (label).
type base struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов
	// (OutputDir, MaxFiles, NoOverwrite). Поля и методы FileOps промотируются:
	// d.OutputDir, d.Write(..) и т.п. доступны напрямую.
	*tools.FileOps
	Prompt string
	Config projects.Config
	// Tools — выбранные агентом инструменты из общего реестра
	// (единый источник для GetTools/GetToolsForOllama и диспетчеризации вызовов).
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis). Подключается оркестратором
	// Kanban через SetBoardStore; в standalone-режиме (CLI) — nil.
	Store *board.Store
	// label — префикс логов агента ("backend-разработчик"/"frontend-разработчик").
	label string
	// roleDesc — инструкция роли для системного промпта (область работы, стек,
	// запреты). Задаётся конструктором специализации.
	roleDesc string
	// langDesc — стек/язык специализации для заголовка системного промпта
	// ("Ты — опытный разработчик на ... и архитектор"). Пусто — используется
	// Config.Language (глобальный CODEGEN_LANG).
	langDesc string
}

// BackendDeveloper — агент «Backend разработчик». Выполняет задачи разработки
// серверной части монорепозитория (обычно подкаталог server/): API, бизнес-
// логика, работа с БД. Если подпроекта ещё нет — создаёт базовую структуру.
type BackendDeveloper struct {
	*base
}

// FrontendDeveloper — агент «Frontend разработчик». Выполняет задачи разработки
// клиентской части монорепозитория (обычно подкаталог frontend/): UI-компоненты,
// страницы, стили, клиентская логика. Если подпроекта ещё нет — создаёт его
// базовую структуру.
type FrontendDeveloper struct {
	*base
}

// backendRoleDesc — инструкция роли бэкенд-разработчика для системного промпта.
const backendRoleDesc = `Твоя роль — БЭКЕНД-РАЗРАБОТЧИК. Ты работаешь ТОЛЬКО с серверной частью
приложения (бэкендом) — обычно это подкаталог server/backend/ (Go/Python/Node-сервер, API, БД).
Твоя область:
- Только файлы бэкенда: server/, backend/, cmd/, internal/, api/ (серверная часть) и т.п.
- API-роуты, бизнес-логика, работа с БД, модели данных, сервисы.
- go.mod / requirements.txt / package.json серверной части.
Запрещено:
- Менять фронтенд: UI-компоненты, страницы, стили, клиентскую логику.
- Переписывать публичный интерфейс API без согласования с фронтендом.
Если задача требует правок на фронтенде — сообщи это и работай только со своей частью.`

// frontendRoleDesc — инструкция роли фронтенд-разработчика для системного промпта.
const frontendRoleDesc = `Твоя роль — ФРОНТЕНД-РАЗРАБОТЧИК. Ты работаешь ТОЛЬКО с клиентской частью
приложения (фронтендом) — обычно это подкаталог frontend/ (папка Node.js/React/Vue/etc).
Твоя область:
- Только файлы фронтенда: frontend/, client/, ui/ и т.п. Не трогай код бэкенда (server/, backend/, internal/).
- UI-компоненты, страницы, стили, клиентская логика, обращение к API.
- package.json и зависимости фронтенда (Vite/React/Vue/Next.js и т.п.).
Запрещено:
- Менять бэкенд: серверную логику, API-роуты, базы данных, Go/Python-код серверной части.
- Создавать серверные точки входа или переносить серверный код во фронтенд.
Если задача требует правок на бэкенде — сообщи это и работай только со своей частью.`

// NewBackendDeveloper создаёт бэкенд-разработчика в общей для всех агентов
// выходной папке temp/<projectName> в корне модуля. Работает и с новым, и с
// существующим проектом: если нужного кода ещё нет — создаёт его с нуля.
// projectName — имя проекта, задаётся пользователем. prompt — текст задания.
func NewBackendDeveloper(projectName, prompt string) *BackendDeveloper {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newBackendDeveloperInDir(prompt, dir)
}

// NewFrontendDeveloper создаёт фронтенд-разработчика в общей для всех агентов
// выходной папке temp/<projectName> в корне модуля. Работает и с новым, и с
// существующим проектом: если нужного кода ещё нет — создаёт его с нуля.
// projectName — имя проекта, задаётся пользователем. prompt — текст задания.
func NewFrontendDeveloper(projectName, prompt string) *FrontendDeveloper {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newFrontendDeveloperInDir(prompt, dir)
}

// newBackendDeveloperInDir создаёт бэкенд-разработчика в заданной директории.
// Заголовок системного промпта берёт язык из Config.Language (CODEGEN_LANG).
func newBackendDeveloperInDir(prompt, dir string) *BackendDeveloper {
	return &BackendDeveloper{base: newBase(prompt, dir, projects.LoadConfig(),
		"backend-разработчик", backendRoleDesc)}
}

// newFrontendDeveloperInDir создаёт фронтенд-разработчика в заданной директории.
// Заголовок системного промпта фиксирует стек фронтенда, а не глобальный
// CODEGEN_LANG (который по умолчанию Go).
func newFrontendDeveloperInDir(prompt, dir string) *FrontendDeveloper {
	return &FrontendDeveloper{base: newBase(prompt, dir, projects.LoadConfig(),
		"frontend-разработчик", frontendRoleDesc).withLangDesc("TypeScript/JavaScript (React, Node.js)")}
}

// newBase собирает общую часть агента-разработчика в заданной директории:
// *tools.FileOps с лимитами из конфига и выборку инструментов из реестра.
func newBase(prompt, dir string, cfg projects.Config, label, roleDesc string) *base {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &base{
		FileOps:  ops,
		Prompt:   prompt,
		Config:   cfg,
		Tools:    tools.Select(devToolNames, tools.Deps{FileOps: ops}),
		label:    label,
		roleDesc: roleDesc,
	}
}

// withLangDesc задаёт язык/стек специализации для заголовка системного промпта.
func (b *base) withLangDesc(desc string) *base {
	b.langDesc = desc
	return b
}

// SetBoardStore подключает разработчика к общей Kanban-доске проекта: добавляет
// инструменты статуса задачи (BoardGetTask/BoardSetTaskStatus), передаёт
// Board-контекст в реестр. Вызывается оркестратором Kanban при построении
// агента специалиста.
func (d *base) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	d.Store = s
	names := append(append([]string{}, devToolNames...), devBoardToolNames...)
	d.Tools = tools.Select(names, tools.Deps{FileOps: d.FileOps, Board: s})
}

func (d *base) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: d.Prompt,
		},
	}
}

// RequiredToolFirstRound — разработчик обязан вызвать WriteFiles в первом
// раунде, чтобы гарантированно создать/обновить файлы. Если модель ответила
// текстом-описанием без вызова инструмента, runner подскажет и повторит запрос.
func (d *base) RequiredToolFirstRound() (string, bool) {
	return "WriteFiles", true
}

func (d *base) GetSystemMessages(_ []agents.Message) []agents.Message {
	// Язык/стек заголовка системного промпта: для фронтенда — специализация
	// агента (langDesc), для остальных — глобальный Config.Language/CodenGen.
	lang := d.langDesc
	if lang == "" {
		lang = d.Config.Language
	}
	if lang == "" {
		lang = "Go"
	}

	var moduleInstruction string
	if d.Config.Module != "" {
		moduleInstruction = fmt.Sprintf("Используй имя модуля Go %q в файле go.mod.", d.Config.Module)
	}

	overwriteRule := "Ты можешь перезаписывать файлы инструментами WriteFiles."
	if d.Config.NoOverwrite {
		overwriteRule = "Включена защита от перезаписи: инструменты вернут ошибку, если ты попытаешься перезаписать уже существующий файл. Перезаписывай файлы только при необходимости."
	}

	// Инструкция про Kanban-доску добавляется только когда доска подключена
	// (SetBoardStore). В standalone-режиме (plan/qa CLI) инструментов доски
	// в наборе разработчика нет, и упоминание их в промпте заставляет модель
	// вызывать отсутствующие функции (BoardSetTaskStatus not in tool set → фатал).
	kanbanStep := ""
	if d.Store != nil {
		kanbanStep = "6. При работе с Kanban-доской: когда задача полностью выполнена и проверки зелёные — переведи её в статус done инструментом BoardSetTaskStatus.\n"
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: fmt.Sprintf(`Ты — опытный разработчик на языке %s и архитектор, который пишет аккуратный, рабочий код и соблюдает архитектурные слои и обязанности каждого участка кода.
%s
%s
%s

Ты работаешь только внутри выходной директории проекта (OutputDir).

Твой план работы:
1. Сначала изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadMap для чтения «карт кода» (декларации без тел: сигнатуры, поля структур, типы — с номерами строк). Если в README проекта есть раздел «План работ» — прочитай его: там описано, какой функционал уже реализован другими задачами плана и на каком этапе находится проект. НЕ дублируй и НЕ удаляй уже сделанное.
2. Изучай НЕ ТОЛЬКО имена файлов, но и их содержимое: определи, что уже реализовано (модули, компоненты, эндпоинты, контракты), прежде чем что-то создавать. Если нужного кода или подпроекта ещё нет — создай его (включая go.mod, package.json, requirements.txt, если требуется) инструментом WriteFiles.
3. Вноси ТОЧЕЧНЫЕ изменения, развивая существующий код: добавляй и правь только то, что относится к твоей задаче. Для точечных правок внутри существующих файлов НЕ возвращай файл целиком — используй блоки SEARCH/REPLACE инструментом SearchReplace (для любого языка: Go, TS/TSX, CSS) или для Go заменяй одну функцию инструментом PatchGoFunction (передаёшь только тело функции в body). Для больших единовременных добавлений новых файлов используй WriteFiles, для добавления в конец — AppendFile, для удаления — DeleteFiles.
4. КАТЕГОРИЧЕСКИ НЕ удаляй и НЕ переписывай с нуля функционал, созданный другими задачами плана: он может понадобиться следующим шагам. Если похожий функционал уже есть — используй его и адаптируй под свою задачу, а не создавай альтернативу с нуля.
5. После каждого набора изменений проверяй, что проект компилируется и проходит проверки: запускай только сборку и автотесты через Run (для Go — "go build ./...", "go vet ./...", дополнительно "gofmt -l ." для форматирования по эталону, при наличии тестов "go test ./..."; для стеков с npm/pnpm/yarn — "npm run build" / "yarn build", а также "npm test" / "yarn test" по аналогии). НЕ запускай через Run приложение/сервер — ни для бэкенда ("go run server/main.go"), ни для фронтенда ("npm run dev", "npm start", "yarn start", "pnpm dev" и т.п.) — такие процессы не завершаются сами и зависают до таймаута; если запуск нужен для проверки, опиши команду в README, а проверяй код тестами.
6. Если компиляция или проверки падают — исправляй код и запускай проверки снова, пока всё не станет зелёным.
%s7. В конце приведи или обнови файл readme.md: инструкцию запуска и использования, а в разделе «План работ» отметь свою задачу как выполненную.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Передавай все файлы списком в свойстве files в одном вызове инструмента WriteFiles.
- Сохраняй существующую архитектуру и стиль кода проекта; не ломай функционал, не затрагиваемый заданием.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).
- Завершай ВСЮ задачу в ОДНОМ ответе: один вызов инструмента — это только начало цикла; не останавливайся после первого шага и не присылай промежуточных итогов. Продолжай цикл (правки → проверки → тесты), пока цель не выполнена, и только затем пиши итоговый ответ.
- Пиши unit-тесты для вычислимой и сервисной логики (Go: *_test.go в соответствующем пакете; TS/JS: тесты по конвенции проекта), прогоняй их через Run и доводи до зелёного результата.

### КОМПАКТНЫЙ КОНТЕКСТ (экономия токенов модели — критично):
1. Для ОРИЕНТАЦИИ читай карты кода (ReadMap): интерфейсы, типы, сигнатуры и номера строк без тел функций. Не читай файл целиком ради поиска одного контракта.
2. Для ХИРУРГИЧЕСКОГО ЧТЕНИЯ используй ReadFiles с параметром lines (например "lines": "40-70") — читай ровно тот диапазон строк, куда вносишь правку. Перед SEARCH/REPLACE читай только фрагмент, а не весь файл.
3. Если нужна сигнатура функции/метода из другого пакета — получи её картой (ReadMap) или диапазоном строк (ReadFiles lines), НЕ угадывай имена и типы. Точный SEARCH-блок можно составлять только по реальному коду из ReadFiles/Run.
4. Если понял, что контекста не хватает для точечной задачи — вместо догадок запроси недостающее одной строкой: "NEED_CONTEXT: путь/к/файлу.go", "NEED_CONTEXT: путь/к/файлу.go:20-45" или "NEED_SIGNATURE: путь/к/файлу.go". Оркестратор дозаправит контекст компактно и продолжит цикл.

### ТОЧЕЧНЫЕ ПРАВКИ СУЩЕСТВУЮЩЕГО КОДА (критично для командной работы):
Твою задачу выполняют параллельно другие разработчики-агенты: они уже реализовали свой функционал в тех же файлах. ИЗОЛИРУЙ правку, чтобы не стереть чужое:
1. В блоке SEARCH инструмента SearchReplace указывай ТОЛЬКО точный фрагмент, который реально правишь, — дословно, с теми же отступами, что в файле (сначала прочитай файл через ReadFiles и скопируй фрагмент оттуда). REPLACE — этот же фрагмент с изменениями. Инструмент применяет замену к СТРОГО первому совпадению; если фрагмента в файле нет — вернётся ошибка и файл не изменится. Это твоя защита от случайного переписывания: если код поменял другой агент, подстройся под новую версию, а не переписывай файл целиком.
2. Для Go, если меняешь целиком одну функцию/метод, используй PatchGoFunction: верни в body полный исходник функции (с сигнатурой-близнецом), укажи target_file и function_name (+ receiver для методов). Инструмент заменит ТОЛЬКО узел этой функции через go/ast и отформатирует файл — другие функции и импорты не пострадают. Если новому телу нужны неимпортированные пакеты — перечисли их пути в imports (они добавятся, существующие импорты не удаляются).
3. Только после таких точечных правок, если инструменты вернули ошибку «SEARCH не найден» или «функция не найдена», перечитай файл (ReadFiles), сверься с актуальным кодом и повтори. Не обходи защиту перезаписью файла целиком через WriteFiles.`, lang, moduleInstruction, d.roleDesc, overwriteRule, kanbanStep),
		},
	}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (d *base) GetTools() []tools.ToolDefinition {
	return d.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (d *base) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(d.GetTools())
}

func (d *base) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return d.Tools.Execute(functionName, functionArgs)
}

// Обёртки выбранных файловых инструментов. Тело живёт в tools.FileOps;
// наблюдаемые снаружи сигнатуры сохранены (используются тестами и
// программными вызовами) и просто делегируют в реестр.

func (d *base) WriteFiles(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("WriteFiles", args)
}
func (d *base) ReadFiles(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("ReadFiles", args)
}
func (d *base) DeleteFiles(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("DeleteFiles", args)
}
func (d *base) Run(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("Run", args)
}
func (d *base) List(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("List", args)
}
func (d *base) AppendFile(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("AppendFile", args)
}
