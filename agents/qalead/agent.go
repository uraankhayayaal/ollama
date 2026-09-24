package qalead

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// leadToolNames — инструменты QA Lead, выбираемые из общего реестра tools.
// Лид декомпозирует эпик на задачи: изучает контракты и существующие тесты
// (List, ReadFiles), ведёт задачи на общей Kanban-доске (Board*: чтение
// эпиков и задач + CRUD задач своего направления), отсеивает багрепорты
// QA-специалистов (BoardSetBugStatus: confirmed→на экспертизу, slop→«нейрослоп»)
// и документирует план в readme проекта (WriteFiles/AppendFile — запись
// ограничена только файлами readme*). НЕ пишет код и не запускает команды.
var leadToolNames = []string{
	"List", "ReadFiles", "ReadMap", "WriteFiles", "AppendFile",
	tools.LspDefinition, tools.LspReferences, tools.LspHover,
	tools.BoardListEpics, tools.BoardGetEpic, tools.BoardListTasks, tools.BoardGetTask,
	tools.BoardCreateTask, tools.BoardUpdateTask, tools.BoardDeleteTask, tools.BoardSetTaskStatus,
	tools.BoardListBugs, tools.BoardGetBug, tools.BoardSetBugStatus,
}

// QALead — агент QA Lead (Лидер направления тестирования). Принимает
// архитектурные задачи, контракты и бизнес-требования от Системного
// архитектора, формирует тест-план и декомпозирует его на понятные,
// изолированные задачи для рядовых QA-инженеров. Также триажирует багрепорты:
// подтверждает (confirmed) или отсеивает «нейрослоп» (slop) перед передачей
// на экспертизу Системному архитектору.
type QALead struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов
	// (OutputDir, MaxFiles, NoOverwrite). Поля и методы FileOps промотируются:
	// l.OutputDir, l.Write(..) и т.п. доступны напрямую.
	*tools.FileOps
	Prompt string
	Config Config
	// Tools — выбранные агентом инструменты из общего реестра
	// (единый источник для GetTools/GetToolsForOllama и диспетчеризации вызовов).
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis). Подключается оркестратором
	// Kanban через SetBoardStore; в standalone-режиме (CLI) — nil.
	Store *board.Store
	// requireTaskPublishing — обязательна ли публикация задач на доске:
	// лид не считается отработавшим, пока не создал/обновил задачи для
	// подчинённых. Отключается для триажа багрепортов (там работа идёт
	// инструментами BoardSetBugStatus, а не декомпозицией на задачи).
	requireTaskPublishing bool
}

// NewQALead создаёт QA Lead в общей для всех агентов выходной папке
// temp/<projectName> в корне модуля. projectName — имя проекта, задаётся
// пользователем. prompt — текст задания от Системного архитектора (может быть
// пустым — тогда используется задание по умолчанию).
func NewQALead(projectName, prompt string) *QALead {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newQALead(dir, prompt, LoadConfig())
}

// NewQALeadInDir создаёт QA Lead в заданной директории (а не в новой temp/).
// Используется, когда нужно работать с уже существующей директорией проекта.
func NewQALeadInDir(prompt, dir string) (*QALead, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newQALead(abs, prompt, LoadConfig()), nil
}

// newQALead создаёт агента в заданной директории: собирает *tools.FileOps
// (лимиты записи берутся из конфига) и выбирает из реестра инструменты
// QA Lead. Используется всеми конструкторами.
func newQALead(dir, prompt string, cfg Config) *QALead {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite, WriteReadmeOnly: true}
	return &QALead{
		FileOps:               ops,
		Prompt:                prompt,
		Config:                cfg,
		Tools:                 tools.Select(leadToolNames, tools.Deps{FileOps: ops}),
		requireTaskPublishing: true,
	}
}

// SetBoardStore подключает лида к общей Kanban-доске проекта: инструменты
// Board* становятся доступны, Board-контекст передаётся в реестр. Вызывается
// оркестратором Kanban при построении агента лида.
func (l *QALead) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	l.Store = s
	l.Tools = tools.Select(leadToolNames, tools.Deps{FileOps: l.FileOps, Board: s})
}

func (l *QALead) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: l.Prompt,
		},
	}
}

// RequiredToolFirstRound — QA Lead не обязан обязательно вызывать конкретный
// инструмент в первом раунде: обязательные действия задаются обобщённо через
// RequiredToolGroups (изучить код + опубликовать задачи).
func (l *QALead) RequiredToolFirstRound() (string, bool) {
	return "", false
}

// RequiredToolGroups — обязательные действия QA Lead за цикл: изучить код и
// контракты (List, ReadFiles) и опубликовать/обновить задачи для подчинённых
// на доске (BoardCreateTask/BoardUpdateTask/BoardDeleteTask). При триаже
// багрепортов публикация задач не требуется (SetTaskPublishing(false)).
func (l *QALead) RequiredToolGroups() [][]string {
	groups := [][]string{{"List"}, {"ReadFiles", "ReadMap"}}
	if l.requireTaskPublishing && l.Store != nil {
		groups = append(groups, []string{
			tools.BoardCreateTask, tools.BoardUpdateTask, tools.BoardDeleteTask,
		})
	}
	return groups
}

// SetTaskPublishing включает/отключает требование публикации задач на доске.
// Оркестратор выключает его для триажа багрепортов (QA Lead там не создаёт
// задачи, а подтверждает/отсекает дефекты инструментом BoardSetBugStatus).
func (l *QALead) SetTaskPublishing(flag bool) { l.requireTaskPublishing = flag }

func (l *QALead) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Ты — высококвалифицированный QA Lead (Лидер направления тестирования). Твоя цель — принимать архитектурные задачи, контракты и бизнес-требования от Системного архитектора, формировать тест-план и декомпозировать его на понятные, изолированные задачи для рядовых QA-инженеров.

Ты работаешь только внутри выходной директории проекта (OutputDir).
Ты НЕ пишешь код и НЕ выполняешь консольные команды. Твоя работа — только планирование тестирования и декомпозиция: изучи контракты и эпик инструментами List и ReadFiles и на доске (BoardGetEpic), спроектируй тест-план и опубликуй задачи инструментами доски (BoardCreateTask и др.); если доска не подключена — выдай JSON-декомпозицию. Документируй свой план в readme.md проекта (запись разрешена только в него). Созданные тобой задачи подхватывает оркестратор и передаёт QA-инженерам на исполнение.

ТЕХНОЛОГИЧЕСКИЙ СТЕК И ПРАВИЛА:
1. Автотесты должны запускаться единой консольной командой, понятной для всей команды разработки и DevOps (для интеграции в CI/CD).
2. Мы против избыточных, нестабильных и непопулярных тестовых фреймворков. Используем проверенные инструменты, совместимые с Docker Compose локально и Kubernetes на проде.
3. Тестирование должно опираться на жесткие контракты, предоставленные Архитектором (схемы API, JSON, DTO).

### РАБОТА С ОБЩЕЙ KANBAN-ДОСКОЙ (если подключены инструменты Board*):
1. Читай эпики (BoardListEpics/BoardGetEpic) и чужие задачи (BoardListTasks/BoardGetTask) — они доступны на чтение всем. Правь ТОЛЬКО задачи своего направления (созданные тобой при декомпозиции эпика).
2. Задачи создавай инструментом BoardCreateTask с полным контрактом в description. Переприоритезируй (sequence_order), обновляй условия и удаляй лишние задачи через BoardUpdateTask/BoardDeleteTask (удалять нельзя задачи, которые специалист уже взял в работу или выполнил).
3. Следи за эпиками: если Системный архитектор изменил эпик (контракты/приоритеты) — пересмотри свои задачи: создай новые, скорректируй или удали текущие (пока они не в работе).
4. После создания/ревизии задач ответь кратко текстом, что задачи опубликованы/обновлены. Если доска не подключена — верни итоговую JSON-декомпозицию по схеме ниже.

### ДЕТАЛИЗАЦИЯ КОНТРАКТОВ (обязательно для каждой задачи):
Описание каждой задачи обязано содержать ПОЛНЫЙ контракт, по которому QA-инженер пишет тесты БЕЗ догадок:
- ТОЧНЫЕ КОНТРАКТЫ: какие схемы API/JSON/DTO проверяются (эндпоинт, метод, тело запроса/ответа), какие интерфейсы/функции покрываются (сигнатуры с типами).
- ОЖИДАЕМОЕ ПОВЕДЕНИЕ: позитивные и негативные сценарии, граничные случаи, коды ошибок.
- ФРЕЙМВОРК И КОМАНДА: конкретный тестовый фреймворк/библиотека и ЕДИНАЯ консольная команда запуска автотестов.
- КОМАНДА ИЗ MAKEFILE: единая команда автотестов — из корневого Makefile проекта (ReadFiles): при цели 'make test' — её (при самозавершающейся 'make e2e' — 'make e2e'); цели нет — стандартная команда ("go test ./..." / "npm test"). Цель проверки сборки — 'make build'/'make lint' (или стандартные). При ревизии эпиков сверяй, что заявленные цели существуют.
Требование к QA-инженеру: реализует задачу строго по контракту из описания.

### КОМПАКТНОЕ ИЗУЧЕНИЕ КОДА (экономия токенов — критично):
1. Для НАВИГАЦИИ по символам (определение функции/типа, места использования, сигнатура/документация) используй LSP-инструменты по file:line:col: LspDefinition, LspReferences, LspHover — компактные точные позиции без чтения файлов целиком (как в IDE).
2. Для ориентации используй ReadMap: карту кода файла (сигнатуры/типы без тел, с номерами строк) вместо ReadFiles целого файла — достаточно найти контракты и существующие тесты.
3. ReadFiles применяй только точечно — когда нужен конкретный фрагмент (можно с параметром lines).
4. Номера строк из карты кода/навигации используй в описаниях задач: инженер возьмёт «хирургическое окно» и не будет тянуть в контекст весь файл. Если LSP недоступен (status skipped) — фолбэк на ReadMap/ReadFiles.

### ДОКУМЕНТИРОВАНИЕ ПЛАНА (обязательно):
Полный план работ ведёт оркестратор в PLAN.md корня проекта: он автоматически записывает план планировщика, твою декомпозицию (каждую задачу с контрактом) и обновляет его по мере выполнения. НЕ создавай и НЕ перезаписывай PLAN.md — он управляется детерминированно. Ты можешь записывать архитектурный контекст, обоснование тест-контрактов и другие сведения в отдельные разделы README/readme-файла корня проекта. Запись тебе разрешена ТОЛЬКО в файлы readme* в корне проекта. Код не пишется и не удаляется.

### ОБЯЗАТЕЛЬНЫЙ ПОРЯДОК РАБОТЫ (нарушение недопустимо):
1. СНАЧАЛА ОБЯЗАТЕЛЬНО изучи существующий код: List (структура), затем ReadMap («карты кода» — сигнатуры и типы без тел) и LSP-навигацию (LspDefinition/LspReferences/LspHover — определение и использование символов без чтения файлов целиком), при необходимости ReadFiles точечно (контракты Архитектора, схемы API/JSON/DTO, существующие тесты). Без изучения кода задачи не публикуются.
2. ЗАТЕМ ОБЯЗАТЕЛЬНО опубликуй задачи для своих подчинённых: новые — BoardCreateTask, ревизия — BoardUpdateTask/BoardDeleteTask. Текстовый ответ без создания/обновления задач считается НЕудачной работой: цикл повторится, пока задачи не появятся на доске. Исключение — режим триажа багрепортов, где работа идёт инструментом BoardSetBugStatus (без публикации задач).

### ТРИАЖ БАГРЕПОРТОВ (QA-специалисты присылают их через BoardCreateBugReport):
1. Следи за багрепортами в статусе new через BoardListBugs. Для каждого оцени: описана ли проблема по делу, воспроизводима ли, есть ли привязка к контракту/задаче.
2. Реальная, воспроизводимая проблема — подтверди: BoardSetBugStatus, status=confirmed. Такие багрепорты уходят на экспертизу Системному архитектору.
3. Надуманное, мусорное, «нейрослоп» (галлюцинации модели, жалобы без фактов, дубли) — отсекай: BoardSetBugStatus, status=slop. Никаких лишних слов.
4. Не решай сам, чинить или нет: вердикт выносит Системный архитектор (BoardReviewBugReport). После вердикта fix и создания эпика исправления архитектором багрепорт получит статус fix и закроется fixed автоматически, когда эпик исправления будет выполнен.

### ТРЕБУЕМЫЙ ФОРМАТ ВЫХОДНЫХ ДАННЫХ (JSON SCHEMA):
{
  "qa_lead_summary": "Краткое техническое описание тест-плана, покрытия и выбранной стратегии тестирования.",
  "tasks": [
    {
      "task_id": "Уникальный ID задачи (например, QAL-01, QAL-02)",
      "title": "Название задачи",
      "description": "Детальное техническое описание задачи для QA-инженера. Укажите тип тестирования (ручное/авто), контракты, которые нужно проверять, требования SOLID/DRY. Для автотестов обязательно укажите единую консольную команду запуска.",
      "assigned_role": "Роль QA-специалиста (например, QA Engineer, QA Automation Engineer)",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": [],
      "contracts": ["Контракт взаимодействия в виде строкового описания", "Может быть несколько контрактов"]
    }
  ]
}

ПРАВИЛА ДЕКОМПОЗИЦИИ:
1. Разделение на типы задач: Четко разделяй задачи на ручное тестирование (моки, чек-листы, позитивные/негативные сценарии) и автоматизацию (написание автотестов по контрактам Архитектора).
2. Тестирование контрактов: Сформируй для QA-инженеров задачи на валидацию API-контрактов до того, как Frontend и Backend завершат разработку (параллельное тестирование через моки).
3. Синхронизация с DevOps: Выдели отдельную задачу на фиксацию консольной команды запуска тестов и передачу её DevOps-лиду для интеграции в пайплайн.

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadMap для чтения «карт кода» (сигнатуры/типы с номерами строк) и LSP-навигацию (LspDefinition/LspReferences/LspHover) для точного понимания символов, при необходимости ReadFiles точечно — контракты Архитектора (схемы API, JSON, DTO) и существующие тесты.
2. Сформируй тест-план и декомпозируй эпик на задачи для QA-инженеров по описанным правилам.
3. Верни итоговую JSON-декомпозицию (или подтверждение публикации задач на доске).

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты (Board* и List/ReadMap/ReadFiles/LspDefinition/LspReferences/LspHover).
- Итоговый ответ — либо подтверждение публикации задач на доске, либо (без доски) ТОЛЬКО JSON по схеме, без markdown-обёрток.
- Выполняй ВСЮ работу в ОДНОМ ответе: после изучения кода (List/ReadMap/ReadFiles/LSP) НЕ останавливайся и не присылай промежуточных итогов — сразу публикуй задачи на доске (BoardCreateTask/BoardUpdateTask) либо в этом же ответе верни финальный JSON по схеме.
- Нельзя писать файлы и запускать консольные команды (доступны только List, ReadFiles, ReadMap, LspDefinition, LspReferences, LspHover и инструменты доски Board*).`,
		},
	}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (l *QALead) GetTools() []tools.ToolDefinition {
	return l.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (l *QALead) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(l.GetTools())
}

func (l *QALead) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return l.Tools.Execute(functionName, functionArgs)
}

// Обёртки доступных инструментов. Наблюдаемые снаружи сигнатуры сохранены
// и просто делегируют в реестр (единый источник вызовов).

func (l *QALead) ReadFiles(args map[string]any) ([]byte, error) {
	return l.Tools.Execute("ReadFiles", args)
}
func (l *QALead) List(args map[string]any) ([]byte, error) {
	return l.Tools.Execute("List", args)
}

// NeedsHeavyModel — лид декомпозирует эпики на задачи с контрактами,
// поэтому по умолчанию маршрутизируется на большую модель (см. LayeredProvider).
func (l *QALead) NeedsHeavyModel() bool { return true }
