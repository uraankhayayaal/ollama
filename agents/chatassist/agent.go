// Package chatassist — ассистент проекта: отвечает на вопросы пользователя в
// чате о состоянии проекта (Kanban-доска), его файлах и коде, а по смыслу
// сообщения создаёт эпики/задачи/баги инструментами доски (Ф-1). В отличие от
// оркестратора, ассистент не пишет файлы проекта — только доска (Board* write)
// и чтение/поиск (List/ReadFiles/ReadMap, LSP, CodeSearch/RAG).
package chatassist

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"

	"github.com/ollama/ollama/api"
)

// assistantToolNames — инструменты ассистента: чтение файлов (List/ReadFiles/
// ReadMap), LSP-навигация по символам, семантический поиск по коду (CodeSearch,
// RAG) и Kanban-доска — чтение (Board* read: эпики/задачи/баги) и запись
// (Board* write: создание эпиков/задач/багов, смена статусов) — по решению
// модели, а не по ключевым словам.
var assistantToolNames = []string{
	"List", "ReadFiles", "ReadMap",
	tools.LspDefinition, tools.LspReferences, tools.LspHover,
	tools.CodeSearch,
	tools.BoardListEpics, tools.BoardGetEpic,
	tools.BoardListTasks, tools.BoardGetTask,
	tools.BoardListBugs, tools.BoardGetBug,
	tools.BoardCreateEpic, tools.BoardUpdateEpic, tools.BoardDeleteEpic, tools.BoardSetEpicStatus,
	tools.BoardCreateTask, tools.BoardUpdateTask, tools.BoardDeleteTask, tools.BoardSetTaskStatus,
	tools.BoardCreateBug, tools.BoardSetBugStatus, tools.BoardReviewBug,
}

// assistantSystemPrompt — системный промпт ассистента-исполнителя (Ф-1): по
// смыслу сообщения сам решает — создать эпик/задачу/баг, ответить по
// RAG/доске/файлам или задать уточняющий вопрос.
const assistantSystemPrompt = `Ты — ассистент проекта и собеседник. Общаешься с пользователем в чате по-русски, кратко и по делу.

Пойми намерение пользователя ПО СМЫСЛУ, а не по ключевым словам:
- «заведи эпик ...», «хочу канбан на рефакторинг», «создай задачу на тесты», «заведи баг про тормоза» — это запрос на ДЕЙСТВИЕ: создай эпик/задачу/баг инструментом доски (BoardCreateEpic/BoardCreateTask/BoardCreateBug). Формулировка может быть любой.
- вопрос о проекте/коде/состоянии — ответь по состоянию: файлы проекта (List, ReadFiles, ReadMap), LSP-навигация по символам (LspDefinition/LspReferences/LspHover), семантический поиск по коду (CodeSearch) и Kanban-доска (инструменты Board* — чтение: эпики, задачи, баги). Если данных в промпте недостаточно — изучи файлы и доску инструментами.
- обычное общение — отвечай сам из знаний; если данных не хватает, честно скажи об этом.
- при неоднозначности — задай уточняющий вопрос, а не угадывай.

Когда создаёшь эпик/задачу/баг, заполни обязательные поля (task_id — уникальный на доске, например CHAT-01, CHAT-02; title, description и assigned_role — роль исполнителя/лида направления).
Деструктивные действия (удаление эпика/задачи, перевод статуса в cancelled) выполняй ТОЛЬКО после явного подтверждения пользователем в чате: сначала спроси «Подтвердите: ...? (да/нет)», затем выполни действие по «да/подтверждаю/ок/делай».
Файлы проекта ассистент не правит — за разработку отвечают агенты через доску.`

// Assistant — ассистент проекта: отвечает текстом и выполняет действия на
// Kanban-доске по смыслу сообщения (создание эпиков/задач/багов и статусы).
type Assistant struct {
	// *tools.FileOps — доступ к файлам проекта (OutputDir ограничивает
	// инструменты директорией проекта), поля и методы промотируются.
	*tools.FileOps
	// Prompt — сообщение пользователя (текст message из чата).
	Prompt string
	// ProjectName — имя проекта (фильтр RAG-поиска, совпадает с реестром).
	ProjectName string
	// RAG — клиент векторной памяти (CodeSearch + блок «релевантный код» в
	// системном промпте). Опционален: nil — промпт без блока, CodeSearch
	// деградирует в skipped. Интерфейс (Ping+Search) вместо *rag.Client
	// позволяет hermetic-тестам подменять поисковик фейком.
	RAG tools.RAGSearcher
	// Tools — выбранные инструменты из общего реестра.
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis); nil — доска недоступна.
	Store *board.Store
}

// NewAssistantWithStore создаёт ассистента в рабочей директории проекта
// temp/<projectName> с подключённой Kanban-доской. projectName — имя проекта,
// prompt — сообщение пользователя, ragClient — векторная память (nil — RAG
// отключён, деградация).
func NewAssistantWithStore(projectName, prompt string, store *board.Store, ragClient tools.RAGSearcher) *Assistant {
	dir := projects.ProjectDir(projectName)
	return newAssistant(dir, projectName, prompt, store, ragClient)
}

// NewAssistantInDir создаёт ассистента в заданной директории (вместо новой
// temp/<имя>). Используется сервером, когда проект уже зарегистрирован
// (KindDir/KindGit/KindTemp) и у него есть рабочий каталог Root — тогда имя
// проекта передаётся явно (sess.project), чтобы RAG-поиск шёл по индексу
// именно этого проекта.
func NewAssistantInDir(dir, projectName, prompt string, store *board.Store, ragClient tools.RAGSearcher) *Assistant {
	return newAssistant(dir, projectName, prompt, store, ragClient)
}

// newAssistant создаёт ассистента в заданной директории: собирает *tools.FileOps
// и выбирает из реестра инструменты (чтение + доска-запись + CodeSearch).
// Используется конструкторами.
func newAssistant(dir, projectName, prompt string, store *board.Store, ragClient tools.RAGSearcher) *Assistant {
	os.MkdirAll(dir, 0755)
	ops := &tools.FileOps{OutputDir: dir}
	return &Assistant{
		FileOps:     ops,
		Prompt:      prompt,
		ProjectName: projectName,
		RAG:         ragClient,
		Tools:       tools.Select(assistantToolNames, tools.Deps{FileOps: ops, Board: store, RAG: ragClient}),
		Store:       store,
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
	sys := assistantSystemPrompt
	if blk := a.ragContextBlock(); blk != "" {
		sys += "\n\n" + blk
	}
	return []agents.Message{
		{
			Type:    agents.MessageTypeSystem,
			Message: sys,
		},
	}
}

// ragContextBlock — «релевантный код по вопросу»: семантическая выборка из
// векторной памяти (RAG) по тексту сообщения пользователя, подмешивается в
// системный промпт, чтобы ассистент «знал» релевантный код не только через
// CodeSearch, но и из контекста первого ответа (Ф-1). Пустой проект/RAG
// выключен/nil-клиент/неудача поиска — пустая строка (degrade).
func (a *Assistant) ragContextBlock() string {
	if a.ProjectName == "" {
		return ""
	}
	return assistantRAGBlock(a.ProjectName, a.Prompt, a.RAG)
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
