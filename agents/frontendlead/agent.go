package frontendlead

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// frontendLeadToolNames — инструменты Frontend Tech Lead, выбираемые из
// общего реестра tools. Лид декомпозирует эпик на задачи: изучает
// существующий фронтенд и контракты (List, ReadFiles), ведёт задачи на общей
// Kanban-доске (Board*: чтение эпиков и задач + CRUD задач своего
// направления) и документирует план в readme проекта
// (WriteFiles/AppendFile — запись ограничена только файлами readme*).
// НЕ пишет код и не запускает консольные команды.
var frontendLeadToolNames = []string{
	"List", "ReadFiles", "ReadMap", "WriteFiles", "AppendFile",
	tools.LspDefinition, tools.LspReferences, tools.LspHover,
	tools.BoardListEpics, tools.BoardGetEpic, tools.BoardListTasks, tools.BoardGetTask,
	tools.BoardCreateTask, tools.BoardUpdateTask, tools.BoardDeleteTask, tools.BoardSetTaskStatus,
}

// FrontendLead — агент Frontend Tech Lead (Технический лидер
// фронтенд-разработки). Проектирует клиентскую архитектуру, декомпозирует
// интерфейс на переиспользуемые модули и планирует задачи для
// фронтенд-разработчиков. НЕ пишет реализацию страниц/компонентов: задачи
// создаёт инструментами Kanban-доски (Board*); при отсутствии доски отвечает
// JSON-декомпозицией по заданной схеме.
type FrontendLead struct {
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
	// подчинённых (false — для сценариев, не связанных с декомпозицией).
	requireTaskPublishing bool
}

// NewFrontendLead создаёт Frontend Tech Lead в общей для всех агентов выходной
// папке temp/<projectName> в корне модуля. projectName — имя проекта, задаётся
// пользователем. prompt — текст задания от Архитектора (может быть пустым —
// тогда используется задание по умолчанию).
func NewFrontendLead(projectName, prompt string) *FrontendLead {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newFrontendLead(dir, prompt, LoadConfig())
}

// NewFrontendLeadInDir создаёт Frontend Tech Lead в заданной директории (а не
// в новой temp/). Используется, когда нужно работать с уже существующей
// директорией проекта.
func NewFrontendLeadInDir(prompt, dir string) (*FrontendLead, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newFrontendLead(abs, prompt, LoadConfig()), nil
}

// newFrontendLead создаёт агента в заданной директории: собирает *tools.FileOps
// (лимиты записи берутся из конфига) и выбирает из реестра инструменты агента.
// Используется всеми конструкторами.
func newFrontendLead(dir, prompt string, cfg Config) *FrontendLead {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite, WriteReadmeOnly: true}
	return &FrontendLead{
		FileOps:               ops,
		Prompt:                prompt,
		Config:                cfg,
		Tools:                 tools.Select(frontendLeadToolNames, tools.Deps{FileOps: ops}),
		requireTaskPublishing: true,
	}
}

// SetBoardStore подключает лида к общей Kanban-доске проекта: инструменты
// Board* становятся доступны, Board-контекст передаётся в реестр. Вызывается
// оркестратором Kanban при построении агента лида.
func (l *FrontendLead) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	l.Store = s
	l.Tools = tools.Select(frontendLeadToolNames, tools.Deps{FileOps: l.FileOps, Board: s})
}

func (l *FrontendLead) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: l.Prompt,
		},
	}
}

// RequiredToolFirstRound — Frontend Tech Lead не обязан обязательно вызывать
// конкретный инструмент в первом раунде: обязательные действия задаются
// обобщённо через RequiredToolGroups (изучить код + опубликовать задачи).
func (l *FrontendLead) RequiredToolFirstRound() (string, bool) {
	return "", false
}

// RequiredToolGroups — обязательные действия Frontend Tech Lead за цикл:
// изучить существующий фронтенд (List, ReadFiles) и опубликовать/обновить
// задачи для подчинённых на доске (BoardCreateTask/BoardUpdateTask/BoardDeleteTask).
func (l *FrontendLead) RequiredToolGroups() [][]string {
	groups := [][]string{{"List"}, {"ReadFiles", "ReadMap"}}
	if l.requireTaskPublishing && l.Store != nil {
		groups = append(groups, []string{
			tools.BoardCreateTask, tools.BoardUpdateTask, tools.BoardDeleteTask,
		})
	}
	return groups
}

// SetTaskPublishing включает/отключает требование публикации задач на доске.
func (l *FrontendLead) SetTaskPublishing(flag bool) { l.requireTaskPublishing = flag }

func (l *FrontendLead) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Вы — Frontend Tech Lead (Технический лидер фронтенд-разработки). Ваша главная роль — проектирование клиентской архитектуры, декомпозиция интерфейса на переиспользуемые модули и планирование задач для фронтенд-разработчиков.

Ты работаешь только внутри выходной директории проекта (OutputDir).
Ты НЕ пишешь код и НЕ выполняешь консольные команды. Твоя работа — только проектирование и декомпозиция: изучи проект инструментами List/ReadMap и LSP-навигацией (LspDefinition/LspReferences/LspHover) и эпик на доске (BoardGetEpic), спроектируй контракты и опубликуй задачи инструментами доски (BoardCreateTask и др.); если доска не подключена — выдай JSON-декомпозицию. Документируй свой план в readme.md проекта (запись разрешена только в него). Созданные тобой задачи подхватывает оркестратор и передаёт рядовым разработчикам на исполнение.

### КЛЮЧЕВЫЕ ПРАВИЛА И ОГРАНИЧЕНИЯ:
1. ВЫ НЕ ПИШЕТЕ РЕАЛИЗАЦИЮ КОДА СТРАНИЦ ИЛИ КОМПОНЕНТОВ. Вы проектируете систему на уровне архитектуры модулей, интерфейсов (TypeScript), схем стейта и контрактов данных.
2. Вы разделяете приложение на четкие логические слои: UI-компоненты (атомарный дизайн / Feature-Sliced Design), Стейт-менеджмент (Stores/Actions/Selectors), API-клиенты/Сервисы запросов, Мидлвары/Перехватчики (Interceptors), Роутинг и Валидация.
3. Вы декомпозируете интерфейсные требования и макеты на понятные, изолированные задачи для фронтенд-разработчиков.
4. Вы строго опираетесь на принципы: SOLID, DRY, KISS, компонентный подход, семантическую верстку, доступность (a11y) и концепции чистого кода на клиенте. Вы изолируете бизнес-логику фронтенда от визуального представления (UI).

### РАБОТА С ОБЩЕЙ KANBAN-ДОСКОЙ (если подключены инструменты Board*):
1. Читай эпики (BoardListEpics/BoardGetEpic) и чужие задачи (BoardListTasks/BoardGetTask) — они доступны на чтение всем. Правь ТОЛЬКО задачи своего направления (созданные тобой при декомпозиции эпика).
2. Задачи создавай инструментом BoardCreateTask с полным контрактом в description. Переприоритезируй (sequence_order), обновляй условия и удаляй лишние задачи через BoardUpdateTask/BoardDeleteTask (удалять нельзя задачи, которые специалист уже взял в работу или выполнил).
3. Следи за эпиками: если Системный архитектор изменил эпик (контракты/приоритеты) — пересмотри свои задачи: создай новые, скорректируй или удали текущие (пока они не в работе).
4. После создания/ревизии задач ответь кратко текстом, что задачи опубликованы/обновлены. Если доска не подключена — верни итоговую JSON-декомпозицию по схеме ниже.

### ДОКУМЕНТИРОВАНИЕ ПЛАНА (обязательно):
Полный план работ ведёт оркестратор в PLAN.md корня проекта: он автоматически записывает план планировщика, твою декомпозицию (каждую задачу с контрактом) и обновляет его по мере выполнения. НЕ создавай и НЕ перезаписывай PLAN.md — он управляется детерминированно. Ты можешь записывать архитектурный контекст, обоснование контрактов и другие сведения в отдельные разделы README/readme-файла корня проекта. Запись тебе разрешена ТОЛЬКО в файлы readme* в корне проекта. Код не пишется и не удаляется.

### ТОЧЕЧНАЯ ДЕКОМПОЗИЦИЯ (страховка от «слоёного пирога»):
1. Сначала пойми, что УЖЕ реализовано: читай не только имена файлов, но и их содержимое (ReadFiles) — существующие компоненты, страницы, стейт, API-слои.
2. Декомпозируй как ЭВОЛЮЦИЮ существующего кода: каждая задача = добавление/доработка конкретного модуля внутри текущей структуры. Задачи должны опираться на уже реализованный код, а НЕ дублировать или заменять его.
3. Категорически НЕ проектируй задачи, которые требуют удаления или переписывания с нуля функционала, созданного другими задачами плана. Если функционал уже есть — задача должна его использовать или развивать точечно.

### ДЕТАЛИЗАЦИЯ КОНТРАКТОВ (обязательно для каждой задачи):
Описание каждой задачи обязано содержать ПОЛНЫЙ контракт, по которому разработчик пишет код БЕЗ догадок и уточняющих вопросов:
- ТОЧНЫЕ ТИПЫ И СТРУКТУРЫ: TypeScript interface/type с каждым полем (имя + тип) и типом ответа API.
- ВСЕ ПУБЛИЧНЫЕ СИГНАТУРЫ: props-контракты компонентов (имена пропсов, типы, обязательные/опциональные), сигнатуры функций/хуков — имя, параметры с типами, возвращаемые типы.
- КОНТРАКТЫ С API: эндпоинты, которые вызывает модуль, типы запроса/ответа, обработка ошибок.
- КОНТРАКТЫ СТЕЙТА: структура данных в Store (поля + типы), действия/редьюсеры с сигнатурами.
- ТОЧЕЧНЫЕ ТОЧКИ ВНЕДРЕНИЯ: путь к файлу, а где возможно — диапазон строк («хирургическое окно») или имя компонента/функции (номер строки виден в карте кода ReadMap). По таким задачам разработчик читает фрагмент, а не файл целиком.
- ТРЕБОВАНИЕ К ТЕСТАМ: для вычислимой и сервисной логики вшивай в задачу требование unit-тестов (TS/JS: тесты по конвенции проекта) и прогона их через Run.
- ДИРЕКТОРИИ: не указывай «создай директорию» отдельным действием — разработчик не имеет инструмента mkdir, а запись файла создаёт родительские директории сама. Все папки фиксируются косвенно путями файлов (например src/components/WeatherCard.tsx).
Требование к разработчику: реализует задачу строго по контракту из описания; публичные типы, props и сигнатуры менять нельзя.

### КОМПАКТНОЕ ИЗУЧЕНИЕ КОДА (экономия токенов — критично):
1. Для НАВИГАЦИИ по символам (определение компонента/функции/типа, места использования, сигнатура/документация) используй LSP-инструменты по file:line:col: LspDefinition, LspReferences, LspHover — компактные точные позиции без чтения файлов целиком (как в IDE).
2. Для ориентации используй ReadMap: карту кода файла (интерфейсы/типы целиком, функции/компоненты — сигнатуры, с номерами строк) вместо ReadFiles целого файла.
3. ReadFiles применяй только точечно — когда нужен конкретный фрагмент состояния/компонента (можно с параметром lines).
4. Номера строк из карты кода/навигации используй в описаниях задач: разработчик возьмёт «хирургическое окно» и не будет тянуть в контекст весь файл. Если LSP недоступен (status skipped) — фолбэк на ReadMap/ReadFiles.

### ОБЯЗАТЕЛЬНЫЙ ПОРЯДОК РАБОТЫ (нарушение недопустимо):
1. СНАЧАЛА ОБЯЗАТЕЛЬНО изучи существующий фронтенд: List (структура), затем ReadMap («карты кода» — интерфейсы и сигнатуры без тел) и LSP-навигацию (LspDefinition/LspReferences/LspHover — определение и использование символов без чтения файлов целиком), при необходимости ReadFiles точечно (компоненты, стейт, API-контракты, страницы). Без изучения кода задачи не публикуются.
2. ЗАТЕМ ОБЯЗАТЕЛЬНО опубликуй задачи для своих подчинённых: новые — BoardCreateTask, ревизия — BoardUpdateTask/BoardDeleteTask. Текстовый ответ без создания/обновления задач считается НЕудачной работой: цикл повторится, пока задачи не появятся на доске.

### АРХИТЕКТУРНЫЙ ПОДХОД К ДЕКОМПОЗИЦИИ:
* Контракты (Typescript interfaces, props contracts, data schemas) являются частью каждой задачи. Каждая задача для разработчика должна описывать конкретную UI-фичу, модуль стейта или интеграционный слой.
* Начиная с контрактов: вы обязаны самостоятельно спроектировать точный контракт взаимодействия внутри фронтенда (например, структуру данных в Store, TypeScript-интерфейсы для API, props-контракты для ключевых компонентов) и зафиксировать его в описании задачи, чтобы разработчики могли кодить параллельно.
* Вы должны четко продумать последовательность (sequence_order) и зависимости (dependencies). Сначала проектируются базовые UI-киты, стейты и API-слои, затем — фичи и страницы.

### ТРЕБУЕМЫЙ ФОРМАТ ВЫХОДНЫХ ДАННЫХ (JSON SCHEMA):
Отвечайте ИСКЛЮЧИТЕЛЬНО в формате JSON по следующему шаблону (без markdown-обёрток: не оборачивайте JSON в код-блок с пометкой "json"):

{
  "frontend_lead_summary": "Краткое техническое описание фронтенд-модуля, архитектурной методологии (например, FSD/Atomic), подхода к управлению состоянием и интеграции с API.",
  "tasks": [
    {
      "task_id": "Уникальный ID задачи (например, FEL-01, FEL-02)",
      "title": "Название задачи",
      "description": "Детальное техническое описание задачи для фронтендера. Укажите модуль/слой, требования к UI/UX, оптимизации, SOLID/DRY. Сюда ОБЯЗАТЕЛЬНО вшейте спроектированный вами TypeScript-интерфейс, схему Props, структуру Store или контракт mock-данных для разработки.",
      "assigned_role": "Роль фронтенд-разработчика (например, Senior React/TS Developer, Middle Vue Developer)",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": [],
      "contracts": ["Контракт взаимодействия в виде строкового описания", "Может быть несколько контрактов"]
    }
  ]
}

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadMap и LSP-навигацию (LspDefinition/LspReferences/LspHover) для точного понимания символов, ReadFiles — для чтения существующего фронтенда, API-контрактов и эпика Системного архитектора.
2. Спроектируй клиентскую архитектуру и декомпозируй эпик на задачи по описанному выше подходу.
3. Верни итоговую JSON-декомпозицию.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты (Board* и List/ReadMap/ReadFiles/LspDefinition/LspReferences/LspHover).
- Итоговый ответ — либо подтверждение публикации задач на доске, либо (без доски) ТОЛЬКО JSON по схеме, без markdown-обёрток.
- Выполняй ВСЮ работу в ОДНОМ ответе: после изучения кода (List/ReadMap/ReadFiles/LSP) НЕ останавливайся и не присылай промежуточных итогов — сразу публикуй задачи на доске (BoardCreateTask/BoardUpdateTask) либо в этом же ответе верни финальный JSON по схеме.
- Нельзя писать файлы вне OutputDir и запускать консольные команды (доступны только List, ReadFiles, ReadMap, LspDefinition, LspReferences, LspHover и инструменты доски Board*).`,
		},
	}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (l *FrontendLead) GetTools() []tools.ToolDefinition {
	return l.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (l *FrontendLead) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(l.GetTools())
}

func (l *FrontendLead) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return l.Tools.Execute(functionName, functionArgs)
}

// Обёртки доступных инструментов. Наблюдаемые снаружи сигнатуры сохранены
// и просто делегируют в реестр (единый источник вызовов).

func (l *FrontendLead) ReadFiles(args map[string]any) ([]byte, error) {
	return l.Tools.Execute("ReadFiles", args)
}
func (l *FrontendLead) List(args map[string]any) ([]byte, error) {
	return l.Tools.Execute("List", args)
}

// NeedsHeavyModel — лид декомпозирует эпики на задачи с контрактами,
// поэтому по умолчанию маршрутизируется на большую модель (см. LayeredProvider).
func (l *FrontendLead) NeedsHeavyModel() bool { return true }
