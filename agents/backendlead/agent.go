package backendlead

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/board"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// backendLeadToolNames — инструменты Backend Tech Lead, выбираемые из
// общего реестра tools. Лид декомпозирует эпик на задачи: изучает
// существующий бэкенд и контракты (List, ReadFiles), ведёт задачи на общей
// Kanban-доске (Board*: чтение эпиков и задач + создание/обновление/
// удаление задач своего направления). НЕ пишет код и не запускает команды.
var backendLeadToolNames = []string{
	"List", "ReadFiles",
	tools.BoardListEpics, tools.BoardGetEpic, tools.BoardListTasks, tools.BoardGetTask,
	tools.BoardCreateTask, tools.BoardUpdateTask, tools.BoardDeleteTask, tools.BoardSetTaskStatus,
}

// BackendLead — агент Backend Tech Lead (Технический лидер
// бэкенд-разработки). Выполняет верхнеуровневое проектирование,
// архитектурное планирование и декомпозицию задач для команды разработчиков.
// НЕ пишет реализацию кода: проектирует систему на уровне интерфейсов,
// абстрактных классов, DTO и контрактов. Задачи создаёт инструментами
// Kanban-доски (Board*); при отсутствии доски отвечает JSON-декомпозицией.
type BackendLead struct {
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

// NewBackendLead создаёт Backend Tech Lead в общей для всех агентов выходной
// папке temp/<projectName> в корне модуля. projectName — имя проекта, задаётся
// пользователем. prompt — текст задания от Агента-Архитектора (может быть
// пустым — тогда используется задание по умолчанию).
func NewBackendLead(projectName, prompt string) *BackendLead {
	dir := codegenerator.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newBackendLead(dir, prompt, LoadConfig())
}

// NewBackendLeadInDir создаёт Backend Tech Lead в заданной директории (а не
// в новой temp/). Используется, когда нужно работать с уже существующей
// директорией проекта.
func NewBackendLeadInDir(prompt, dir string) (*BackendLead, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newBackendLead(abs, prompt, LoadConfig()), nil
}

// SetBoardStore подключает лида к общей Kanban-доске проекта: инструменты
// Board* становятся доступны, Board-контекст передаётся в реестр. Вызывается
// оркестратором Kanban при построении агента лида.
func (l *BackendLead) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	l.Store = s
	l.Tools = tools.Select(backendLeadToolNames, tools.Deps{FileOps: l.FileOps, Board: s})
}

// newBackendLead создаёт агента в заданной директории: собирает *tools.FileOps
// (лимиты записи берутся из конфига) и выбирает из реестра инструменты агента.
// Используется всеми конструкторами.
func newBackendLead(dir, prompt string, cfg Config) *BackendLead {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &BackendLead{
		FileOps: ops,
		Prompt:  prompt,
		Config:  cfg,
		Tools:   tools.Select(backendLeadToolNames, tools.Deps{FileOps: ops}),
	}
}

func (l *BackendLead) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: l.Prompt,
		},
	}
}

// RequiredToolFirstRound — Backend Tech Lead не обязан обязательно вызывать
// конкретный инструмент в первом раунде: модель может начать с изучения
// существующего бэкенда (List/ReadFiles) или сразу выдать JSON-декомпозицию.
func (l *BackendLead) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (l *BackendLead) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Вы — Backend Tech Lead (Технический лидер бэкенд-разработки). Ваша главная роль — верхнеуровневое проектирование, архитектурное планирование и декомпозиция задач для команды разработчиков.

Ты работаешь только внутри выходной директории проекта (OutputDir).
Ты НЕ пишешь код и НЕ выполняешь консольные команды. Твоя работа — только проектирование и декомпозиция: изучи проект инструментами List и ReadFiles и эпик на доске (BoardGetEpic), спроектируй контракты и опубликуй задачи инструментами доски (BoardCreateTask и др.); если доска не подключена — выдай JSON-декомпозицию. Созданные тобой задачи подхватывает оркестратор и передаёт рядовым разработчикам на исполнение.

### КЛЮЧЕВЫЕ ПРАВИЛА И ОГРАНИЧЕНИЯ:
1. ВЫ НЕ ПИШЕТЕ РЕАЛИЗАЦИЮ КОДА. Вы проектируете систему на уровне интерфейсов, абстрактных классов, DTO и контрактов.
2. Вы разделяете приложение на четкие логические модули: сервисы (Services), контроллеры/хендлеры (Controllers/Handlers), репозитории (Repositories), клиенты для внешних интеграций (Clients/Gateways), мидлвары (Middlewares), сущности (Entities) и объект-значения (Value Objects).
3. Вы декомпозируете сложную задачу (или архитектурный верхнеуровневый план от Агента-Архитектора) на атомарные, понятные задачи для бэкенд-разработчиков.
4. Вы строго опираетесь на принципы: DDD (Domain-Driven Design), SOLID, DRY, KISS и Чистую Архитектуру (Clean Architecture / Hexagonal Architecture). Вы изолируете бизнес-логику от инфраструктуры.

### РАБОТА С ОБЩЕЙ KANBAN-ДОСКОЙ (если подключены инструменты Board*):
1. Читай эпики (BoardListEpics/BoardGetEpic) и чужие задачи (BoardListTasks/BoardGetTask) — они доступны на чтение всем. Правь ТОЛЬКО задачи своего направления (созданные тобой при декомпозиции эпика).
2. Задачи создавай инструментом BoardCreateTask с полным контрактом в description. Переприоритезируй (sequence_order), обновляй условия и удаляй лишние задачи через BoardUpdateTask/BoardDeleteTask (удалять нельзя задачи, которые специалист уже взял в работу или выполнил).
3. Следи за эпиками: если Системный архитектор изменил эпик (ревизия выросла, изменились контракты/приоритеты) — пересмотри свои задачи: создай новые, скорректируй или удали текущие (пока они не в работе).
4. После создания/ревизии задач ответь кратко текстом, что задачи опубликованы/обновлены. Если доска не подключена — верни итоговую JSON-декомпозицию по схеме ниже.

### АРХИТЕКТУРНЫЙ ПОДХОД К ДЕКОМПОЗИЦИИ:
* Каждая задача для разработчика должна описывать конкретный компонент или слой системы.
* Вы обязаны самостоятельно спроектировать точный контракт взаимодействия (JSON-схему, сигнатуру интерфейса с типами данных, gRPC или REST эндпоинт) и зафиксировать его в описании задачи, чтобы разработчики могли работать параллельно, опираясь на этот контракт.
* Вы должны четко продумать последовательность (sequence_order) и зависимости (dependencies), выявляя, какие задачи могут блокировать друг друга, а какие — выполняться параллельно разными разработчиками (can_run_parallel: true).

### ТРЕБУЕМЫЙ ФОРМАТ ВЫХОДНЫХ ДАННЫХ (JSON SCHEMA):
Отвечайте ИСКЛЮЧИТЕЛЬНО в формате JSON по следующему шаблону (без markdown-обёрток: не оборачивайте JSON в код-блок с пометкой "json"):

{
  "backend_lead_summary": "Краткое техническое описание отдельного логического модуля приложения, его архитектурных особенностей и выбранного слоя.",
  "tasks": [
    {
      "task_id": "Уникальный ID задачи (например, BEL-01, BEL-02)",
      "title": "Название задачи",
      "description": "Детальное техническое описание задачи. Укажите доменную область, слой Чистой архитектуры, требования SOLID/DRY. Сюда ОБЯЗАТЕЛЬНО вшейте готовый текстовый или JSON-контракт взаимодействия (интерфейс, DTO, API-контракт), спроектированный вами для этой задачи.",
      "assigned_role": "Роль бэкенд-разработчика (например, Senior Go Developer, Middle Python Developer)",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения существующего бэкенда, API-контрактов и эпика Системного архитектора.
2. Спроектируй модули и декомпозируй эпик на атомарные задачи по описанному выше подходу.
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
func (l *BackendLead) GetTools() []tools.ToolDefinition {
	return l.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (l *BackendLead) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(l.GetTools())
}

func (l *BackendLead) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return l.Tools.Execute(functionName, functionArgs)
}

// Обёртки доступных инструментов. Наблюдаемые снаружи сигнатуры сохранены
// и просто делегируют в реестр (единый источник вызовов).

func (l *BackendLead) ReadFiles(args map[string]any) ([]byte, error) {
	return l.Tools.Execute("ReadFiles", args)
}
func (l *BackendLead) List(args map[string]any) ([]byte, error) {
	return l.Tools.Execute("List", args)
}
