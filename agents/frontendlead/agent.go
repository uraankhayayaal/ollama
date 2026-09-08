package frontendlead

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/board"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// frontendLeadToolNames — инструменты Frontend Tech Lead, выбираемые из
// общего реестра tools. Лид декомпозирует эпик на задачи: изучает
// существующий фронтенд и контракты (List, ReadFiles), ведёт задачи на общей
// Kanban-доске (Board*: чтение эпиков и задач + CRUD задач своего
// направления). НЕ пишет код и не запускает консольные команды.
var frontendLeadToolNames = []string{
	"List", "ReadFiles",
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
}

// NewFrontendLead создаёт Frontend Tech Lead в общей для всех агентов выходной
// папке temp/<projectName> в корне модуля. projectName — имя проекта, задаётся
// пользователем. prompt — текст задания от Архитектора (может быть пустым —
// тогда используется задание по умолчанию).
func NewFrontendLead(projectName, prompt string) *FrontendLead {
	dir := codegenerator.ProjectDir(projectName)
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
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &FrontendLead{
		FileOps: ops,
		Prompt:  prompt,
		Config:  cfg,
		Tools:   tools.Select(frontendLeadToolNames, tools.Deps{FileOps: ops}),
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
// конкретный инструмент в первом раунде: модель может начать с изучения
// существующего фронтенда (List/ReadFiles) или сразу выдать JSON-декомпозицию.
func (l *FrontendLead) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (l *FrontendLead) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Вы — Frontend Tech Lead (Технический лидер фронтенд-разработки). Ваша главная роль — проектирование клиентской архитектуры, декомпозиция интерфейса на переиспользуемые модули и планирование задач для фронтенд-разработчиков.

Ты работаешь только внутри выходной директории проекта (OutputDir).
Ты НЕ пишешь код и НЕ выполняешь консольные команды. Твоя работа — только проектирование и декомпозиция: изучи проект инструментами List и ReadFiles и эпик на доске (BoardGetEpic), спроектируй контракты и опубликуй задачи инструментами доски (BoardCreateTask и др.); если доска не подключена — выдай JSON-декомпозицию. Созданные тобой задачи подхватывает оркестратор и передаёт рядовым разработчикам на исполнение.

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

### АРХИТЕКТУРНЫЙ ПОДХОД К ДЕКОМПОЗИЦИИ:
* Каждая задача для разработчика должна описывать конкретную UI-фичу, модуль стейта или интеграционный слой.
* Вы обязаны самостоятельно спроектировать точный контракт взаимодействия внутри фронтенда (например, структуру данных в Store, TypeScript-интерфейсы для API, props-контракты для ключевых компонентов) и зафиксировать его в описании задачи, чтобы разработчики могли кодить параллельно.
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
      "dependencies": []
    }
  ]
}

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения существующего фронтенда, API-контрактов и эпика Системного архитектора.
2. Спроектируй клиентскую архитектуру и декомпозируй эпик на задачи по описанному выше подходу.
3. Верни итоговую JSON-декомпозицию.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты (Board* и List/ReadFiles).
- Итоговый ответ — либо подтверждение публикации задач на доске, либо (без доски) ТОЛЬКО JSON по схеме, без markdown-обёрток.
- Нельзя писать файлы вне OutputDir и запускать консольные команды (доступны только List, ReadFiles и инструменты доски Board*).`,
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
