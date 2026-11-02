// Package chatassist — единый агент проекта: чтение, анализ и правка кода.
// Интегрирует возможности chatassist (LSP-навигация, RAG, WebSearch) и
// developer (WriteFiles, SearchReplace, Run, тесты). Один агент для CLI
// и веб-чата — без разделения на read-only и write-capable.
package chatassist

import (
	"ai/agents"
	"ai/domain"
	"ai/shared/projects"
	"ai/tools"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// assistantToolNames — полный набор инструментов: чтение, навигация,
// правка, выполнение команд, поиск.
var assistantToolNames = []string{
	// Чтение и навигация
	"List", "ReadFiles", "ReadMap",
	tools.LspDefinition, tools.LspReferences, tools.LspHover,
	tools.CodeSearch,
	tools.WebSearch,
	// Правка файлов
	"WriteFiles", "AppendFile", "DeleteFiles",
	"SearchReplace", "PatchFunction", "PatchGoFunction",
	// Проверки и выполнение
	"Run", tools.LspCheck, tools.ReadAppLogs,
}

// assistantSystemPrompt — системный промпт агента.
const assistantSystemPrompt = `Ты — технический ассистент проекта. Общаешься с пользователем в чате по-русски, кратко и по делу.

=== ТВОИ ИНСТРУМЕНТЫ ===
Чтение и анализ:
- List, ReadFiles, ReadMap — просмотр структуры и содержимого файлов
- LspDefinition, LspReferences, LspHover — навигация по коду (определения, ссылки, документация)
- CodeSearch — семантический поиск по коду проекта (RAG)
- WebSearch — поиск информации в интернете (актуальные данные, документация)

Правка файлов:
- WriteFiles — создание/перезапись файлов
- AppendFile — добавление в конец файла
- DeleteFiles — удаление файлов
- SearchReplace — точечная замена фрагмента в существующем файле
- PatchFunction, PatchGoFunction — замена одной функции через AST

Проверки:
- Run — выполнение команд (сборка, тесты, lint)
- LspCheck — анализ кода на ошибки
- ReadAppLogs — запуск приложения и сбор логов

=== ЧТО ТЫ ДЕЛАЕШЬ ===
- отвечаешь на вопросы о коде, структуре проекта, архитектуре
- ищешь код, анализируешь файлы, находишь баги и проблемы
- правíšшь файлы проекта: создаёшь новые, исправляешь существующие
- запускаешь проверки (сборку, тесты) после каждого изменения
- ищешь актуальную информацию в интернете (WebSearch) при необходимости
- предлагаешь решения и рекомендации по найденным проблемам
- при неоднозначности задаёшь ОДИН уточняющий вопрос текстом

=== ПРАВИЛА РАБОТЫ ===
1. Изучай текущее состояние проекта перед правками: List → ReadMap → точечное чтение (ReadFiles с lines).
2. Вноси ТОЧЕЧНЫЕ изменения: используй SearchReplace для правок в существующих файлах, WriteFiles для новых.
3. После каждого набора изменений проверяй сборку и тесты через Run.
4. Если сборка упала — LspCheck для точных ошибок → исправь → повтори.
5. Сохраняй существующую архитектуру и стиль кода.
6. ЗАВЕРШАЙ задачу в одном цикле: правки → проверки → тесты → итог. Не останавливайся после первого шага.

НЕ ПОВТОРЯЙ ОДИН И ТОТ ЖЕ ВЫЗОВ ИНСТРУМЕНТА. Если инструмент уже вернул status=skipped — прекрати повторять вызов и ответь пользователю по тем данным, которые уже есть.`

// Assistant — единый агент: чтение, анализ и правка кода.
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
	// Injections — промпт-инъекции уровня Assistant: применяются к работающей
	// модели в рамках всех задач/сессий этого ассистента. Нужны, например,
	// для деактивации ChatRAG (напрямую добавляя инструкцию в системное
	// сообщение ассистента), ограничений или других прикладных нужд.
	// При пустом списке не влияют на работу (fast-path в ApplyInjections).
	Injections []domain.Injection
	// Correction — разовая корректирующая инструкция в конец системного
	// промпта на один вызов (не персистентная, в отличие от Injections).
	Correction string
	// seenCalls — счётчик повторов одинаковых вызовов инструментов
	// (антизацикливание по инструментам). Ключ — сигнатура вызова
	// (имя инструмента + аргументы).
	seenCalls map[string]int
}

// NewAssistantWithStore создаёт агента в рабочей директории проекта
// temp/<projectName>. projectName — имя проекта, prompt — сообщение пользователя,
// ragClient — векторная память (nil — RAG отключён, деградация).
func NewAssistantWithStore(projectName, prompt string, _ any, ragClient tools.RAGSearcher) *Assistant {
	dir := projects.ProjectDir(projectName)
	return newAssistant(dir, projectName, prompt, ragClient)
}

// NewAssistantInDir создаёт агента в заданной директории (вместо новой
// temp/<имя>). Используется сервером, когда проект уже зарегистрирован
// (KindDir/KindGit/KindTemp) и у него есть рабочий каталог Root — тогда имя
// проекта передаётся явно (sess.project), чтобы RAG-поиск шёл по индексу
// именно этого проекта.
func NewAssistantInDir(dir, projectName, prompt string, _ any, ragClient tools.RAGSearcher) *Assistant {
	return newAssistant(dir, projectName, prompt, ragClient)
}

// newAssistant создаёт агента в заданной директории: собирает *tools.FileOps
// и выбирает из реестра инструменты (чтение + правка + проверки).
// Используется конструкторами.
func newAssistant(dir, projectName, prompt string, ragClient tools.RAGSearcher) *Assistant {
	os.MkdirAll(dir, 0755)
	ops := &tools.FileOps{OutputDir: dir, Project: projectName}
	return &Assistant{
		FileOps:     ops,
		Prompt:      prompt,
		ProjectName: projectName,
		RAG:         ragClient,
		Tools:       tools.Select(assistantToolNames, tools.Deps{FileOps: ops, RAG: ragClient}),
		seenCalls:   make(map[string]int),
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

// RequiredToolFirstRound — агент не обязан вызывать конкретный инструмент:
// на простые вопросы отвечает сразу по контексту из системного промпта.
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
	// Общие фрагменты о токено-эффективной работе с Run и LSP-фолбэком
	// добавляются агентам, у которых есть Run + LSP-инструменты.
	sys += agents.RunTokenEconomy
	sys += agents.LSPGrepFallback
	if c := strings.TrimSpace(a.Correction); c != "" {
		sys += "\n\n" + c
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
// контекст. Отвечать нужно на последнюю реплику пользователя, но историю
// обязательно сверяй: она хранит память о том, ЧТО ТЫ УЖЕ СПРАШИВАЛ и на что
// пользователь уже ответил — без неё ассистент переспрашивает по кругу.
func historyPromptBlock(history string) string {
	return "История диалога с пользователем. Отвечай на последнюю реплику, но обязательно " +
		"сверься с историей: не задавай вопрос, который ты уже задавал и на который " +
		"пользователь уже ответил, и не переспрашивай подтверждение, если «да» уже было.\n" +
		history
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

// GetInjections возвращает инъекции уровня Assistant (пусто — fast-path).
func (a *Assistant) GetInjections() []domain.Injection {
	return a.Injections
}

// GetInjectionRole — роль агента для условий when промпт-инъекций.
func (a *Assistant) GetInjectionRole() string { return "assistant" }

// CallFunction диспетчеризует вызов модели к инструменту. Антизацикливание:
// тот же вызов (инструмент + аргументы) допускается limitedToolRepeats раз;
// дальше инструмент не выполняется, модели возвращается status=skipped с
// требованием закончить ход — ответить пользователю.
func (a *Assistant) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	if !a.Tools.Has(functionName) {
		return a.toolRefusal(functionName), nil
	}
	sig := toolCallSignature(functionName, functionArgs)
	a.seenCalls[sig]++
	if a.seenCalls[sig] > limitedToolRepeats {
		return a.repeatCallResult(functionName, a.seenCalls[sig]), nil
	}
	return a.Tools.Execute(functionName, functionArgs)
}

// limitedToolRepeats — сколько раз агенту разрешено вызвать один и тот же
// инструмент с теми же аргументами в рамках одного хода. Два повтора обычно
// осмысленны (повторный дорез после уточнения диапазона строк), третий и
// последующие — почти всегда зацикливание.
const limitedToolRepeats = 2

// toolCallSignature — стабильный ключ вызова инструмента: имя +
// канонический JSON аргументов. Порядок ключей в map нормализован
// сортировкой (encoding/json сортирует ключи map), поэтому порядок полей в
// аргументах от модели на подпись не влияет.
func toolCallSignature(name string, args map[string]any) string {
	if len(args) == 0 {
		return name
	}
	b, err := json.Marshal(args)
	if err != nil {
		// Не сериализуется — считаем вызов уникальным, защита не срабатывает,
		// но и ломать нормальный вызов нельзя.
		return name + "|unserializable"
	}
	return name + "|" + string(b)
}

// toolRefusal — отказ на неизвестный инструмент: не ошибка (иначе модель
// воспримет это как сбой и попробует другой инструмент), а status=skipped
// с сообщением, что инструмент недоступен.
func (a *Assistant) toolRefusal(name string) []byte {
	data, _ := json.Marshal(map[string]any{
		"status": "skipped",
		"tool":   name,
		"message": fmt.Sprintf("Инструмент %s недоступен.", name),
	})
	return data
}

// repeatCallResult — ответ на N+1-й одинаковый вызов: инструмент не
// выполняется повторно (результат не изменится), модели предписывается закончить
// ход — ответить пользователю по тому, что уже выяснил.
func (a *Assistant) repeatCallResult(name string, n int) []byte {
	data, _ := json.Marshal(map[string]any{
		"status": "skipped",
		"tool":   name,
		"message": fmt.Sprintf("Инструмент %s уже вызван %d раз(а) с теми же аргументами в этом ходе — "+
			"повторный вызов не даст нового результата, поэтому он не выполнен. Не перечитывай один и тот же "+
			"файл и не повторяй тот же вызов по кругу. Ответь пользователю по тем данным, которые уже есть.", name, n-1),
	})
	return data
}

// AddTools дополняет набор инструментов агента. Используется сервером
// для инъекции мостов-инструментов.
func (a *Assistant) AddTools(extra ...tools.Tool) {
	a.Tools.Add(extra...)
}
