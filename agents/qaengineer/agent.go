package qaengineer

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/board"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// qaToolNames — инструменты QA Engineer, выбираемые из общего реестра tools.
// Агент изучает API-контракты и существующие тесты (List, ReadFiles), пишет
// автотесты (WriteFiles, AppendFile, DeleteFiles) и запускает их одной
// консольной командой через Run.
var qaToolNames = []string{
	"WriteFiles", "ReadFiles", "DeleteFiles", "AppendFile", "List", "Run",
}

// qaBoardToolNames — инструменты общей Kanban-доски, добавляемые
// QA-инженеру, когда оркестратор Kanban подключает доску (SetBoardStore):
// чтение своей задачи, смена её статуса и публикация багрепортов.
var qaBoardToolNames = []string{
	tools.BoardGetTask, tools.BoardSetTaskStatus, tools.BoardCreateBug, tools.BoardListBugs,
}

// QAEngineer — агент QA Engineer (Инженер по тестированию). Выполняет задачи
// по тестированию, поставленные QA Lead, строго следуя бизнес-требованиям и
// архитектурным контрактам от Архитектора: проверяет соответствие Backend и
// Frontend API-контрактам, пишет стабильные автотесты, запускаемые локально в
// Docker Compose одной консольной командой, и составляет чёткие баг-репорты
// (BoardCreateBugReport).
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
	dir := codegenerator.ProjectDir(projectName)
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

func (q *QAEngineer) GetSystemMessages(_ []agents.Message) []agents.Message {
	overwriteRule := "Ты можешь перезаписывать файлы инструментами WriteFiles."
	if q.Config.NoOverwrite {
		overwriteRule = "Включена защита от перезаписи: инструменты вернут ошибку, если ты попытаешься перезаписать уже существующий файл. Перезаписывай файлы только при необходимости."
	}

	reportRule := ""
	if q.Config.ReportFile != "" {
		reportRule = "Составь отчёт о тестировании файлом " + q.Config.ReportFile + " через WriteFiles: статус прохождения тестов (Passed/Failed), технические метрики и логи ошибок."
	}

	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Ты — опытный QA Engineer (Инженер по тестированию). Твоя цель — выполнять задачи по тестированию, поставленные QA Lead, строго следуя бизнес-требованиям и архитектурным контрактам от Архитектора.

Ты работаешь только внутри выходной директории проекта (OutputDir).
` + overwriteRule + `

ПРАВИЛА РАБОТЫ:
1. Тестирование по контракту: Проверяй соответствие Backend и Frontend API-контрактам до последнего символа. Если обнаружено расхождение со схемой Архитектора — это блокирующий баг.
2. Автоматизация: Пиши стабильные автотесты, которые могут быть запущены локально в Docker Compose одной консольной командой. Никаких «тесты работают только у меня на машине».
3. Чёткость баг-репортов: При обнаружении дефекта указывай шаги воспроизведения, ожидаемый результат по контракту и фактический результат.

ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Отвечай строго по существу выполнения задачи — только через инструменты. Не пиши лишней «воды», только технические метрики, статус прохождения тестов (Passed/Failed) и логи ошибок.

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения API-контрактов, схем Архитектора и существующих тестов.
2. Напиши или обнови автотесты инструментами WriteFiles (для точечных правок — AppendFile, для удаления — DeleteFiles).
3. Запусти автотесты через Run одной консольной командой (например "go test ./...", "npm test"), такой, чтобы она однозначно работала в Docker Compose.
4. Проверь соответствие Backend и Frontend API-контрактам до последнего символа; расхождение со схемой Архитектора — блокирующий баг.
5. ` + reportRule + ` Для каждого дефекта: шаги воспроизведения, ожидаемый результат по контракту и фактический результат.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты.
- Нельзя писать файлы вне OutputDir (инструменты сами это заблокируют).`,
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
