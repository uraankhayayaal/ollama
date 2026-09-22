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
	"strings"

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
	tools.WebSearch,
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
- вопрос, которому нужна СВЕЖАЯ информация из интернета (новости, текущие события, факты, точечная справка, погода, актуальные данные) — используй WebSearch: инструмент вернёт топ ссылок (заголовок/URL/сниппет), из них извлеки ответ сам. Если WebSearch вернул status degraded (сеть/инструмент недоступны) — отвечай из знаний модели и честно предупреди пользователя, что данные не живые/могут быть устаревшими.
- «запусти канбан», «продолжи работу», «продолжи выполнение» — запусти/возобнови оркестрацию по доске (KanbanStart). Безопасно, подтверждения не требует.
- «залей в main», «релиз эпика», «откати ветку», «замерджь задачу» — git-действия (EpicRelease/BranchReject/TaskMerge): они ДЕСТРУКТИВНЫЕ, выполняй только после явного «да» в чате (см. правило подтверждения).
- при неоднозначности — задай уточняющий вопрос, а не угадывай. Если вопрос выбора (варианты, предпочтения, уточнение параметров) — используй структурированный инструмент AskUser: он покажет пользователю карточку с вариантами (один выбор или несколько) и необязательным полем «свой ответ». Так удобнее, чем спрашивать текстом. Можешь задать несколько вопросов сразу пачкой — пользователь ответит пошагово. Если считаешь какой-то вариант оптимальным, пометь его recommended=true (в UI он будет выделен как «рекомендую», но решать будет пользователь). Инструмент блокирует твою генерацию до ответа — ответы придут в результате вызова.

Когда создаёшь эпик/задачу/баг, НЕ вызывай инструмент доски, пока не собрана сводка всех обязательных параметров. Критичные поля не угадывай: при неоднозначности — СПРОСИ пользователя (текстом или инструментом AskUser), покажи собранную сводку в чате и только после подтверждения создавай запись.

СВОДКА ДЛЯ ЭПИКА (BoardCreateEpic): task_id (уникальный на доске, например CHAT-01), title, description, assigned_role (лид направления) и решение «требуется ли ревью архитектора». Если ревью архитектора НЕ требуется — обязательно определи лида направления (инфраструктура/бэкенд/фронтенд/QA); лид не ясен из контекста — спроси. Если решение требует архитектора — зафиксируй это в сводке.

СВОДКА ДЛЯ ЗАДАЧИ (BoardCreateTask): task_id, epic_id (задача ВСЕГДА внутри эпика: эпик бери из контекста разговора, при сомнении — спроси, а не выдумывай), title, description, assigned_role (конкретный специалист направления этого эпика). Если задача затрагивает несколько направлений — не создавай её как задачу: предложи оформить эпиком, архитектор раздаст лидам.

Пример сводки, которую выводишь в чат перед вызовом инструмента:
   Эпик CHAT-01 «Поддержка WebSocket»; ревью архитектора: не требуется; лид: backend.
   Название, описание и специалист собраны — подтвердите создание? (да/нет)
Деструктивные действия (удаление эпика/задачи, перевод статуса в cancelled, мёрдж ветки задачи TaskMerge, релиз эпика в main EpicRelease, откат фича-ветки BranchReject) выполняй ТОЛЬКО после явного подтверждения пользователем в чате: сначала спроси «Подтвердите: ...? (да/нет)», затем выполни действие по «да/подтверждаю/ок/делай». Запуск канбана (KanbanStart) безопасен — выполняется без подтверждения.
Файлы проекта ассистент не правит — за разработку отвечают агенты через доску.`

// Assistant — ассистент проекта: отвечает текстом и выполняет действия на
// Kanban-доске по смыслу сообщения (создание эпиков/задач/багов и статусы).
type Assistant struct {
	// *tools.FileOps — доступ к файлам проекта (OutputDir ограничивает
	// инструменты директорией проекта), поля и методы промотируются.
	*tools.FileOps
	// Prompt — сообщение пользователя (текст message из чата).
	Prompt string
	// History — компактная история диалога (предыдущие реплики пользователя и
	// ассистента), подмешивается в системный промпт, чтобы модель отвечала
	// не только на текущий вопрос, но и с учётом предыдущего контекста чата
	// («что я писал минуту назад»). Пустая строка — история не передаётся.
	History string
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
	if hist := strings.TrimSpace(a.History); hist != "" {
		sys = historyPromptBlock(hist) + "\n\n" + sys
	}
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

// historyPromptBlock — блок истории диалога для системного промпта: ставится
// ПЕРЕД правилами ассистента, связка «история + новый вопрос» образует полный
// контекст, при этом модель отвечает только на последнюю реплику пользователя.
func historyPromptBlock(history string) string {
	return "История диалога с пользователем (только для контекста — отвечай " +
		"только на последнюю реплику пользователя):\n" + history
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

// AddTools дополняет набор инструментов ассистента. Используется сервером
// (Ф-3) для инъекции мостов-инструментов к серверным git/канбан-действиям
// (KanbanStart/TaskMerge/EpicRelease/BranchReject), которые не могут жить в
// общем реестре tools из-за цикла импортов (tools → server).
func (a *Assistant) AddTools(extra ...tools.Tool) {
	a.Tools.Add(extra...)
}
