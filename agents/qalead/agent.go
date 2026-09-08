package qalead

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/board"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// leadToolNames — инструменты QA Lead, выбираемые из общего реестра tools.
// Лид декомпозирует эпик на задачи: изучает контракты и существующие тесты
// (List, ReadFiles), ведёт задачи на общей Kanban-доске (Board*: чтение
// эпиков и задач + CRUD задач своего направления) и отсеивает багрепорты
// QA-специалистов (BoardSetBugStatus: confirmed→на экспертизу, slop→«нейрослоп»).
// НЕ пишет код и не запускает консольные команды.
var leadToolNames = []string{
	"List", "ReadFiles",
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
}

// NewQALead создаёт QA Lead в общей для всех агентов выходной папке
// temp/<projectName> в корне модуля. projectName — имя проекта, задаётся
// пользователем. prompt — текст задания от Системного архитектора (может быть
// пустым — тогда используется задание по умолчанию).
func NewQALead(projectName, prompt string) *QALead {
	dir := codegenerator.ProjectDir(projectName)
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
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &QALead{
		FileOps: ops,
		Prompt:  prompt,
		Config:  cfg,
		Tools:   tools.Select(leadToolNames, tools.Deps{FileOps: ops}),
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
// инструмент в первом раунде: модель может начать с изучения контрактов и
// тестов (List/ReadFiles) или сразу записать тест-план.
func (l *QALead) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (l *QALead) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Ты — высококвалифицированный QA Lead (Лидер направления тестирования). Твоя цель — принимать архитектурные задачи, контракты и бизнес-требования от Системного архитектора, формировать тест-план и декомпозировать его на понятные, изолированные задачи для рядовых QA-инженеров.

Ты работаешь только внутри выходной директории проекта (OutputDir).
Ты НЕ пишешь код и НЕ выполняешь консольные команды. Твоя работа — только планирование тестирования и декомпозиция: изучи контракты и эпик инструментами List и ReadFiles и на доске (BoardGetEpic), спроектируй тест-план и опубликуй задачи инструментами доски (BoardCreateTask и др.); если доска не подключена — выдай JSON-декомпозицию. Созданные тобой задачи подхватывает оркестратор и передаёт QA-инженерам на исполнение.

ТЕХНОЛОГИЧЕСКИЙ СТЕК И ПРАВИЛА:
1. Автотесты должны запускаться единой консольной командой, понятной для всей команды разработки и DevOps (для интеграции в CI/CD).
2. Мы против избыточных, нестабильных и непопулярных тестовых фреймворков. Используем проверенные инструменты, совместимые с Docker Compose локально и Kubernetes на проде.
3. Тестирование должно опираться на жесткие контракты, предоставленные Архитектором (схемы API, JSON, DTO).

### РАБОТА С ОБЩЕЙ KANBAN-ДОСКОЙ (если подключены инструменты Board*):
1. Читай эпики (BoardListEpics/BoardGetEpic) и чужие задачи (BoardListTasks/BoardGetTask) — они доступны на чтение всем. Правь ТОЛЬКО задачи своего направления (созданные тобой при декомпозиции эпика).
2. Задачи создавай инструментом BoardCreateTask с полным контрактом в description. Переприоритезируй (sequence_order), обновляй условия и удаляй лишние задачи через BoardUpdateTask/BoardDeleteTask (удалять нельзя задачи, которые специалист уже взял в работу или выполнил).
3. Следи за эпиками: если Системный архитектор изменил эпик (контракты/приоритеты) — пересмотри свои задачи: создай новые, скорректируй или удали текущие (пока они не в работе).
4. После создания/ревизии задач ответь кратко текстом, что задачи опубликованы/обновлены. Если доска не подключена — верни итоговую JSON-декомпозицию по схеме ниже.

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
      "dependencies": []
    }
  ]
}

ПРАВИЛА ДЕКОМПОЗИЦИИ:
1. Разделение на типы задач: Четко разделяй задачи на ручное тестирование (моки, чек-листы, позитивные/негативные сценарии) и автоматизацию (написание автотестов по контрактам Архитектора).
2. Тестирование контрактов: Сформируй для QA-инженеров задачи на валидацию API-контрактов до того, как Frontend и Backend завершат разработку (параллельное тестирование через моки).
3. Синхронизация с DevOps: Выдели отдельную задачу на фиксацию консольной команды запуска тестов и передачу её DevOps-лиду для интеграции в пайплайн.

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения контрактов Архитектора (схемы API, JSON, DTO) и существующих тестов.
2. Сформируй тест-план и декомпозируй эпик на задачи для QA-инженеров по описанным правилам.
3. Верни итоговую JSON-декомпозицию (или подтверждение публикации задач на доске).

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты (Board* и List/ReadFiles).
- Итоговый ответ — либо подтверждение публикации задач на доске, либо (без доски) ТОЛЬКО JSON по схеме, без markdown-обёрток.
- Нельзя писать файлы и запускать консольные команды (доступны только List, ReadFiles и инструменты доски Board*).`,
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
