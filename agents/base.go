package agents

import (
	"ai/tools"

	"github.com/ollama/ollama/api"
)

// Base — общая часть агентов-специализаций: разделяемый файловый контекст,
// промпт задания и выбранные инструменты. Встраивается в специализацию
// (*agents.Base) и снимает дублирование механических методов Agent
// (GetUserMessages/GetTools/GetToolsForOllama/CallFunction и обёрток
// ReadFiles/List). Специфика роли — системный промпт, конкретный набор
// инструментов, доска — остаётся в специализации (см. Этап 5,
// PLAN-2026-10-12-todo-clean-architecture.md).
type Base struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов (OutputDir,
	// MaxFiles, NoOverwrite). Поля и методы FileOps промотируются через Base:
	// агент.OutputDir, агент.Write(..) и т.п. доступны напрямую.
	*tools.FileOps
	// Prompt — текст задания пользователя (одно human-сообщение).
	Prompt string
	// Tools — выбранные агентом инструменты из общего реестра (единый источник
	// для GetTools/GetToolsForOllama и диспетчеризации вызовов).
	Tools *tools.Set
}

// NewBase собирает общую часть агента: FileOps с заданным контекстом и выборку
// инструментов. deps.FileOps перезаписывается переданным ops — источник истины
// файлового контекста сам Base (deps несёт только остальные зависимости: RAG,
// Board и т.п.).
func NewBase(prompt string, ops *tools.FileOps, toolNames []string, deps tools.Deps) *Base {
	deps.FileOps = ops
	return &Base{FileOps: ops, Prompt: prompt, Tools: tools.Select(toolNames, deps)}
}

// SetTools пересобирает набор инструментов (например, при подключении доски:
// добавляются Board*-инструменты). FileOps-контекст берётся из Base.
func (b *Base) SetTools(toolNames []string, deps tools.Deps) {
	deps.FileOps = b.FileOps
	b.Tools = tools.Select(toolNames, deps)
}

// GetUserMessages возвращает задание пользователя как одно human-сообщение.
func (b *Base) GetUserMessages() []Message {
	return []Message{{Type: MessageTypeHuman, Message: b.Prompt}}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (b *Base) GetTools() []tools.ToolDefinition { return b.Tools.Definitions() }

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (b *Base) GetToolsForOllama() []api.Tool { return tools.ToOllama(b.GetTools()) }

// CallFunction диспетчеризует вызов инструмента по имени через общий реестр.
func (b *Base) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return b.Tools.Execute(functionName, functionArgs)
}

// ReadFiles — обёртка инструмента ReadFiles (делегирует в реестр).
func (b *Base) ReadFiles(args map[string]any) ([]byte, error) {
	return b.Tools.Execute("ReadFiles", args)
}

// List — обёртка инструмента List (делегирует в реестр).
func (b *Base) List(args map[string]any) ([]byte, error) {
	return b.Tools.Execute("List", args)
}
