package devopslead

import (
	"ai/agents"
	"ai/board"
	"ai/projects"
	"ai/tools"
	"os"
	"path/filepath"

	"github.com/ollama/ollama/api"
)

// leadToolNames — инструменты DevOps Lead, выбираемые из общего реестра tools.
// Лид декомпозирует эпик на задачи: изучает состояние инфраструктуры
// (List, ReadFiles), ведёт задачи на общей Kanban-доске (Board*: чтение
// эпиков и задач + CRUD задач своего направления). НЕ пишет код и не запускает
// консольные команды.
var leadToolNames = []string{
	"List", "ReadFiles",
	tools.BoardListEpics, tools.BoardGetEpic, tools.BoardListTasks, tools.BoardGetTask,
	tools.BoardCreateTask, tools.BoardUpdateTask, tools.BoardDeleteTask, tools.BoardSetTaskStatus,
}

// DevopsLead — агент DevOps Lead (Лидер инфраструктурного направления).
// Принимает архитектурные задачи от Системного архитектора, проектирует
// пайплайны и конфигурации окружений верхнего уровня и декомпозирует их на
// конкретные технические задачи для рядовых DevOps-инженеров. Работает строго
// в стеке: Docker Compose локально, Kubernetes на проде, прозрачный CI/CD,
// поддерживающий стек автотестов QA. Задачи создаёт инструментами Kanban-доски
// (Board*); при отсутствии доски отвечает JSON-декомпозицией.
type DevopsLead struct {
	// *tools.FileOps — разделяемый контекст файловых инструментов
	// (OutputDir, MaxFiles, NoOverwrite). Поля и методы FileOps промотируются:
	// d.OutputDir, d.Write(..) и т.п. доступны напрямую.
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

// NewDevopsLead создаёт DevOps Lead в общей для всех агентов выходной папке
// temp/<projectName> в корне модуля. projectName — имя проекта, задаётся
// пользователем. prompt — текст задания от Системного архитектора (может быть
// пустым — тогда используется задание по умолчанию).
func NewDevopsLead(projectName, prompt string) *DevopsLead {
	dir := projects.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	return newDevopsLead(dir, prompt, LoadConfig())
}

// NewDevopsLeadInDir создаёт DevOps Lead в заданной директории (а не в новой
// temp/). Используется, когда нужно работать с уже существующей директорией
// проекта, где уже лежит инфраструктурный код.
func NewDevopsLeadInDir(prompt, dir string) (*DevopsLead, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, err
	}
	return newDevopsLead(abs, prompt, LoadConfig()), nil
}

// newDevopsLead создаёт агента в заданной директории: собирает *tools.FileOps
// (лимиты записи берутся из конфига) и выбирает из реестра инструменты
// DevOps Lead. Используется всеми конструкторами.
func newDevopsLead(dir, prompt string, cfg Config) *DevopsLead {
	ops := &tools.FileOps{OutputDir: dir, MaxFiles: cfg.MaxFiles, NoOverwrite: cfg.NoOverwrite}
	return &DevopsLead{
		FileOps: ops,
		Prompt:  prompt,
		Config:  cfg,
		Tools:   tools.Select(leadToolNames, tools.Deps{FileOps: ops}),
	}
}

// SetBoardStore подключает лида к общей Kanban-доске проекта: инструменты
// Board* становятся доступны, Board-контекст передаётся в реестр. Вызывается
// оркестратором Kanban при построении агента лида.
func (d *DevopsLead) SetBoardStore(s *board.Store) {
	if s == nil {
		return
	}
	d.Store = s
	d.Tools = tools.Select(leadToolNames, tools.Deps{FileOps: d.FileOps, Board: s})
}

func (d *DevopsLead) GetUserMessages() []agents.Message {
	return []agents.Message{
		{
			Type:    agents.MessageTypeHuman,
			Message: d.Prompt,
		},
	}
}

// RequiredToolFirstRound — DevOps Lead не обязан обязательно вызывать
// конкретный инструмент в первом раунде: модель может начать с изучения
// существующих манифестов (List/ReadFiles) или сразу записать декомпозицию.
func (d *DevopsLead) RequiredToolFirstRound() (string, bool) {
	return "", false
}

func (d *DevopsLead) GetSystemMessages(_ []agents.Message) []agents.Message {
	return []agents.Message{
		{
			Type: agents.MessageTypeSystem,
			Message: `Ты — высококвалифицированный DevOps Lead (Лидер инфраструктурного направления). Твоя цель — принимать архитектурные задачи от Системного архитектора, проектировать пайплайны и конфигурации окружений верхнего уровня и декомпозировать их на конкретные технические задачи для рядовых DevOps-инженеров.

Ты работаешь только внутри выходной директории проекта (OutputDir).
Ты НЕ пишешь код и НЕ выполняешь консольные команды. Твоя работа — только проектирование инфраструктуры и декомпозиция: изучи состояние проекта инструментами List и ReadFiles и эпик на доске (BoardGetEpic), спроектируй верхнеуровневые конфигурации окружений и опубликуй задачи инструментами доски (BoardCreateTask и др.); если доска не подключена — выдай JSON-декомпозицию. Созданные тобой задачи подхватывает оркестратор и передаёт DevOps-инженерам на исполнение.

ТЕХНОЛОГИЧЕСКИЙ СТЕК И ПРАВИЛА:
1. Локально: только Docker Compose. На проде: только Kubernetes.
2. Мы против devcontainer и усложнения локального окружения. Всё должно подниматься стандартными манифестами.
3. Локальное окружение в Docker Compose должно в обязательном порядке поддерживать стек автоматизации тестирования, переданный от QA-команды.

### РАБОТА С ОБЩЕЙ KANBAN-ДОСКОЙ (если подключены инструменты Board*):
1. Читай эпики (BoardListEpics/BoardGetEpic) и чужие задачи (BoardListTasks/BoardGetTask) — они доступны на чтение всем. Правь ТОЛЬКО задачи своего направления (созданные тобой при декомпозиции эпика).
2. Задачи создавай инструментом BoardCreateTask с полным контрактом в description. Переприоритезируй (sequence_order), обновляй условия и удаляй лишние задачи через BoardUpdateTask/BoardDeleteTask (удалять нельзя задачи, которые специалист уже взял в работу или выполнил).
3. Следи за эпиками: если Системный архитектор изменил эпик (контракты/приоритеты) — пересмотри свои задачи: создай новые, скорректируй или удали текущие (пока они не в работе).
4. После создания/ревизии задач ответь кратко текстом, что задачи опубликованы/обновлены. Если доска не подключена — верни итоговую JSON-декомпозицию по схеме ниже.

### ТРЕБУЕМЫЙ ФОРМАТ ВЫХОДНЫХ ДАННЫХ (JSON SCHEMA):
{
  "devops_lead_summary": "Краткое техническое описание инфраструктурного решения и его архитектурных особенностей.",
  "tasks": [
    {
      "task_id": "Уникальный ID задачи (например, DOL-01, DOL-02)",
      "title": "Название задачи",
      "description": "Детальное техническое описание задачи для DevOps-инженера. Укажите окружение (локально/прод), требования к конфигурации, требования SOLID/DRY и принцип KISS. ОБЯЗАТЕЛЬНО включите требования по поддержке стека автотестов QA.",
      "assigned_role": "Роль DevOps-специалиста (например, DevOps Engineer, Platform Engineer)",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}

ПРАВИЛА ДЕКОМПОЗИЦИИ:
1. Изоляция инфраструктурных блоков: Декомпозируй задачу Архитектора на понятные этапы: обновление Docker Compose (локально), обновление манифестов Kubernetes (прод), настройка CI/CD пайплайнов.
2. Интеграция автотестов: Создай задачу на внедрение консольной команды тестирования (предоставленной QA) в CI/CD пайплайн. Автотесты должны блокировать деплой при падении.
3. KISS в инфраструктуре: Требуй от инженеров максимально простых конфигураций без избыточной вложенности и кастомных bash-скриптов там, где можно обойтись стандартными средствами инструментов.

Твой план работы:
1. Изучи текущее состояние проекта: используй List для просмотра структуры, затем ReadFiles для чтения существующих манифестов и конфигов (Docker Compose, K8s, CI/CD).
2. Декомпозируй эпик Архитектора на этапы (локальный Docker Compose → прод Kubernetes → CI/CD с интеграцией автотестов QA) по описанным правилам.
3. Верни итоговую JSON-декомпозицию.

Правила:
- Нельзя отвечать текстом-рассуждением вместо действий. Используй инструменты (Board* и List/ReadFiles).
- Итоговый ответ — либо подтверждение публикации задач на доске, либо (без доски) ТОЛЬКО JSON по схеме, без markdown-обёрток.
- Нельзя писать файлы и запускать консольные команды (доступны только List, ReadFiles и инструменты доски Board*).`,
		},
	}
}

// GetTools возвращает определения выбранных инструментов (единый JSON Schema
// формат для OpenAI/Yandex). Источник схем — реестр tools.
func (d *DevopsLead) GetTools() []tools.ToolDefinition {
	return d.Tools.Definitions()
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (d *DevopsLead) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(d.GetTools())
}

func (d *DevopsLead) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	return d.Tools.Execute(functionName, functionArgs)
}

// Обёртки доступных инструментов. Наблюдаемые снаружи сигнатуры сохранены
// и просто делегируют в реестр (единый источник вызовов).

func (d *DevopsLead) ReadFiles(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("ReadFiles", args)
}
func (d *DevopsLead) List(args map[string]any) ([]byte, error) {
	return d.Tools.Execute("List", args)
}
