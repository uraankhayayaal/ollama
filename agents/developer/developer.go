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
// AppendFile, DeleteFiles) и проверяет сборку (Run).
var devToolNames = []string{
	"WriteFiles", "ReadFiles", "DeleteFiles", "Run", "List", "AppendFile",
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
func newBackendDeveloperInDir(prompt, dir string) *BackendDeveloper {
	return &BackendDeveloper{base: newBase(prompt, dir, projects.LoadConfig(),
		"backend-разработчик", backendRoleDesc)}
}

// newFrontendDeveloperInDir создаёт фронтенд-разработчика в заданной директории.
func newFrontendDeveloperInDir(prompt, dir string) *FrontendDeveloper {
	return &FrontendDeveloper{base: newBase(prompt, dir, projects.LoadConfig(),
		"frontend-разработчик", frontendRoleDesc)}
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

// RequiredToolFirstRound — разработчик не обязан вызывать конкретный инструмент
// в первом раунде: модель может начать с изучения существующего кода (List,
// ReadFiles) или сразу записать недостающие файлы.
func (d *base) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (d *base) GetSystemMessages(_ []agents.Message) []agents.Message {
	lang := d.Config.Language
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

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: fmt.Sprintf(`Ты — опытный разработчик на языке %s и архитектор, который пишет аккуратный, рабочий код и соблюдает архитектурные слои и обязанности каждого участка кода.
%s
%s
%s

Ты работаешь только внутри выходной директории проекта (OutputDir).

Твой план работы:
1. Сначала изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения ключевых файлов.
2. Если нужного кода или подпроекта ещё нет — создай его (включая go.mod, package.json, requirements.txt, если требуется) инструментом WriteFiles.
3. Вноси изменения: для больших файлов используй WriteFiles (полная перезапись), для точечных правок — AppendFile, для удаления — DeleteFiles.
4. После каждого набора изменений проверяй, что проект компилируется и проходит проверки: запускай подходящие команды через Run (для Go — "go build ./...", "go vet ./...", при наличии тестов "go test ./..."; для других стеков — их аналоги).
5. Если компиляция или проверки падают — исправляй код и запускай проверки снова, пока всё не станет зелёным.
6. При работе с Kanban-доской: когда задача полностью выполнена и проверки зелёные — переведи её в статус done инструментом BoardSetTaskStatus.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Передавай все файлы списком в свойстве files в одном вызове инструмента WriteFiles.
- Сохраняй существующую архитектуру и стиль кода проекта; не ломай функционал, не затрагиваемый заданием.
- В конце приведи или обнови файл readme.md с инструкцией запуска и использования.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).`, lang, moduleInstruction, d.roleDesc, overwriteRule),
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
