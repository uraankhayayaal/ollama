// Package chatassist — Q&A-ассистент проекта: отвечает на вопросы пользователя
// в чате о состоянии проекта (Kanban-доска), его файлах и коде. В отличие от
// оркестратора, ассистент ничего не создаёт и не меняет: только читает
// файлы (List/ReadFiles/ReadMap) и доску (Board* — read-only) и отвечает
// текстом на русском.
package chatassist

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"

	"github.com/ollama/ollama/api"
)

// assistantToolNames — read-only инструменты ассистента: файлы проекта для
// изучения кода/структуры, LSP-навигация по символам (компактные позиции
// без чтения файлов целиком) и чтение Kanban-доски для ответа о состоянии.
var assistantToolNames = []string{
	"List", "ReadFiles", "ReadMap",
	tools.LspDefinition, tools.LspReferences, tools.LspHover,
	tools.BoardListEpics, tools.BoardGetEpic,
	tools.BoardListTasks, tools.BoardGetTask,
	tools.BoardListBugs, tools.BoardGetBug,
}

// assistantSystemPrompt — системный промпт ассистента: свободный диалог с
// пользователем по-русски (вопросы о проекте, статусы, общие темы), строго
// read-only: ничего не создаёт и не меняет на доске и в файлах.
const assistantSystemPrompt = `Ты — ассистент проекта и просто собеседник. Общаешься с пользователем в чате по-русски, кратко и по делу.
Вопросы о проекте и коде отвечай по состоянию: файлы проекта (List, ReadFiles, ReadMap), LSP-навигация по символам (LspDefinition/LspReferences/LspHover) и Kanban-доска (инструменты Board* — только чтение: эпики, задачи, баги). Если данных в промпте недостаточно — изучи файлы и доску инструментами.
Любые другие темы (общие вопросы, «подскажи погоду», обсуждение идей) — отвечай сам из своих знаний; если данных не хватает, честно скажи об этом.
НИКОГДА не создавай и не меняй: эпики, задачи, баги, статусы и файлы. Для добавления задачи на доску есть отдельное действие пользователя — ты только болтаешь и отвечаешь на вопросы.`

// Assistant — Q&A-ассистент проекта: отвечает текстом на вопросы пользователя,
// используя read-only инструменты (файлы проекта + Kanban-доска).
type Assistant struct {
	// *tools.FileOps — доступ к файлам проекта (OutputDir ограничивает
	// инструменты директорией проекта), поля и методы промотируются.
	*tools.FileOps
	// Prompt — вопрос пользователя (текст message из чата).
	Prompt string
	// Tools — выбранные read-only инструменты из общего реестра.
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis); nil — доска недоступна.
	Store *board.Store
}

// NewAssistantWithStore создаёт ассистента в рабочей директории проекта
// temp/<projectName> с подключённой Kanban-доской. projectName — имя проекта,
// prompt — вопрос пользователя.
func NewAssistantWithStore(projectName, prompt string, store *board.Store) *Assistant {
	dir := projects.ProjectDir(projectName)
	return newAssistant(dir, prompt, store)
}

// NewAssistantInDir создаёт ассистента в заданной директории (вместо новой
// temp/<имя>). Используется сервером, когда проект уже зарегистрирован
// (KindDir/KindGit/KindTemp) и у него есть рабочий каталог Root.
func NewAssistantInDir(dir, prompt string, store *board.Store) *Assistant {
	return newAssistant(dir, prompt, store)
}

// newAssistant создаёт ассистента в заданной директории: собирает *tools.FileOps
// и выбирает из реестра read-only инструменты. Используется конструкторами.
func newAssistant(dir, prompt string, store *board.Store) *Assistant {
	os.MkdirAll(dir, 0755)
	ops := &tools.FileOps{OutputDir: dir}
	return &Assistant{
		FileOps: ops,
		Prompt:  prompt,
		Tools:   tools.Select(assistantToolNames, tools.Deps{FileOps: ops, Board: store}),
		Store:   store,
	}
}

func (a *Assistant) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: a.Prompt,
		},
	}
}

// RequiredToolFirstRound — ассистент не обязан вызывать конкретный инструмент:
// на простые вопросы (например, «что сейчас делает проект») отвечает сразу
// по контексту из системного промпта.
func (a *Assistant) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (a *Assistant) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeSystem,
			Message: assistantSystemPrompt,
		},
	}
}

// GetTools возвращает определения инструментов в OpenAI/Yandex формате.
func (a *Assistant) GetTools() []tools.ToolDefinition {
	return a.Tools.Definitions()
}

// GetToolsForOllama возвращает определения инструментов в формате Ollama.
func (a *Assistant) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(a.GetTools())
}

// CallFunction диспетчеризует вызов модели к инструменту.
func (a *Assistant) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return a.Tools.Execute(functionName, functionArgs)
}
