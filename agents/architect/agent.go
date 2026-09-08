// Package architect реализует агента «Системный архитектор» (System Architect).
//
// Архитектор принимает задачу пользователя и публикует бэклог (список эпиков
// верхнего уровня) на общую Kanban-доску проекта через вызов инструмента
// submit_architecture_backlog. Распределение эпиков идёт между лидами
// направлений: Backend Lead, Frontend Lead, DevOps Lead, QA Lead — их лиды
// декомпозируют эпики на задачи для рядовых специалистов.
//
// По ограничению формата ответа (из промпта) архитектору разрешено отвечать
// ТОЛЬКО вызовом submit_architecture_backlog. Чтобы раннер подсказал модели
// обязательный инструмент в первом раунде, агент реализует
// RequiredToolFirstRound() -> ("submit_architecture_backlog", true).
package architect

import (
	"ai/agents"
	"ai/agents/codegenerator"
	"ai/board"
	"ai/tools"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/ollama/ollama/api"
)

// SubmitBacklogToolName — имя инструмента, которым архитектор публикует бэклог
// на Kanban-доску. Обрабатывается самим агентом (CallFunction) и объявляется в
// GetTools/GetToolsForOllama. Не регистрируется в общем реестре tools — это
// агент-специфичный инструмент, привязанный к хранилищу доски.
const SubmitBacklogToolName = "submit_architecture_backlog"

// toolNames — инструменты архитектора: чтение проекта (List, ReadFiles) из
// общего реестра и работа с общей Kanban-доской (инструменты Board*). Писать
// файлы и запускать команды архитектору нельзя: его работа — спроектировать
// архитектуру, вести эпики и проводить экспертизу багрепортов.
var toolNames = []string{
	"List", "ReadFiles",
	tools.BoardListEpics, tools.BoardListTasks, tools.BoardListBugs, tools.BoardGetBug,
	tools.BoardCreateEpic, tools.BoardUpdateEpic, tools.BoardDeleteEpic, tools.BoardSetEpicStatus,
	tools.BoardReviewBug,
}

// Architect — агент Системный архитектор. Публикует эпики на общую
// Kanban-доску (board.Store) через submit_architecture_backlog.
type Architect struct {
	// *tools.FileOps — контекст файловых инструментов (OutputDir и пр.).
	*tools.FileOps
	Prompt string
	Config Config
	// Tools — выбранные инструменты изучения проекта из общего реестра.
	Tools *tools.Set
	// Store — общая Kanban-доска проекта (Redis), куда публикуются эпики.
	Store *board.Store
	// ReviewMode — режим экспертизы багрепортов (фаза phaseBugs оркестратора):
	// обязательный первый раунд submit_architecture_backlog отключается,
	// системный промпт меняется на экспертную оценку багов.
	ReviewMode bool
}

// NewArchitect создаёт архитектора для проекта: подключает хранилище доски по
// настройкам окружения (BOARD_REDIS_*) и проверяет доступность Redis.
func NewArchitect(projectName, prompt string) (*Architect, error) {
	cfg := LoadConfig()
	store, err := board.NewStore(context.Background(), cfg.StoreConfig(projectName))
	if err != nil {
		return nil, err
	}
	return NewArchitectWithStore(projectName, prompt, store), nil
}

// NewArchitectWithStore создаёт архитектора с уже сконфигурированным
// хранилищем доски (используется оркестратором Kanban).
func NewArchitectWithStore(projectName, prompt string, store *board.Store) *Architect {
	dir := codegenerator.ProjectDir(projectName)
	os.MkdirAll(dir, 0755)
	ops := &tools.FileOps{OutputDir: dir}
	return &Architect{
		FileOps: ops,
		Prompt:  prompt,
		Config:  LoadConfig(),
		Tools:   tools.Select(toolNames, tools.Deps{FileOps: ops, Board: store}),
		Store:   store,
	}
}

// AsBugExpert переключает архитектора в режим экспертизы багрепортов:
// системный промпт меняется, обязательный первый раунд submit_architecture_backlog
// отключается.
func (a *Architect) AsBugExpert() *Architect {
	a.ReviewMode = true
	return a
}

func (a *Architect) GetUserMessages() []agents.Message {
	return []agents.Message{
		{Type: agents.MessageTypeHuman, Message: a.Prompt},
	}
}

// RequiredToolFirstRound требует, чтобы в первом раунде модель обязательно
// вызвала submit_architecture_backlog (публикация бэклога), а не ответила
// текстом. Раннер при необходимости подскажет модели и повторит запрос.
// В режиме экспертизы багрепортов обязательного инструмента нет.
func (a *Architect) RequiredToolFirstRound() (string, bool) {
	if a.ReviewMode {
		return "", false
	}
	return SubmitBacklogToolName, true
}

// GetSystemMessages возвращает системный промпт Системного архитектора.
// Управляющие инструкции из задания пользователя: технологический стек
// (Golang/React/Docker Compose/Kubernetes, принцип KISS), распределение эпиков
// между четырьмя лидами, декомпозиция правил и жёсткое ограничение формата
// ответа — только вызов submit_architecture_backlog.
func (a *Architect) GetSystemMessages(_ []agents.Message) []agents.Message {
	prompt := architectureSystemPrompt
	if a.ReviewMode {
		prompt = bugExpertSystemPrompt
	}
	return []agents.Message{
		{
			Type:    agents.MessageTypeSystem,
			Message: prompt,
		},
	}
}

// architectureSystemPrompt — управляющие инструкции архитектора: технологический
// стек (Golang/React/Docker Compose/Kubernetes, KISS), распределение эпиков между
// четырьмя лидами, обязательный порядок «инфраструктура → приложение →
// тестирование», работа с эпиками через Board*-инструменты и жёсткое ограничение
// формата ответа — только вызовы инструментов.
const architectureSystemPrompt = `Ты — Системный архитектор (System Architect) автоматической команды разработки. Твоя роль — спроектировать архитектуру решения по задаче пользователя и распределить работу между четырьмя лидами направлений: Backend Lead, Frontend Lead, DevOps Lead, QA Lead.

Ты работаешь только внутри выходной директории проекта (OutputDir) и с общей Kanban-доской проекта (инструменты Board*).

### ТЕХНОЛОГИЧЕСКИЙ СТЕК И ПРАВИЛА:
1. Backend: Golang. Frontend: React.
2. Локальная среда: Docker Compose. Продакшн: Kubernetes.
3. Никаких GraphQL, devcontainer и экзотических/непопулярных библиотек.
4. Соблюдай принцип KISS (Keep It Simple, Stupid): максимально простое, но законченное решение.

### РОЛИ, КОТОРЫМ РАСПРЕДЕЛЯЮТСЯ ЭПИКИ:
- Backend Lead (бэкенд-часть, контракты и API)
- Frontend Lead (клиентская часть, UI)
- DevOps Lead (инфраструктура и CI/CD)
- QA Lead (тестирование и автотесты)

### ОБЯЗАТЕЛЬНЫЙ ПОРЯДОК РАЗРАБОТКИ (влияет на sequence_order):
1. Сначала инфраструктура (DevOps Lead): среда локального запуска, Docker Compose, CI/CD — без неё приложение не поднять.
2. Затем приложение (Backend/Frontend Lead): сервисы и контракты — опираются на инфраструктуру.
3. Затем тестирование (QA Lead): автотесты и проверки контрактов — после готовности приложения.
Проставляй sequence_order эпиков с учётом этого порядка: инфраструктура — наименьшие, тестирование — наибольшие порядковые номера. Эпики тестирования оформляй зависимыми (dependencies) от прикладных эпиков, которые они проверяют.

### ПРАВИЛА ДЕКОМПОЗИЦИИ:
1. KISS: Ты задаешь вектор архитектуры, а не атомарные задачи. Декомпозицию на задачи выполняют лиды направлений.
2. Единый источник истины: Ты строго задаешь контракты между подсистемами и обязан продублировать эти контракты в каждом соседнем эпике (description), чтобы лиды и их специалисты могли работать без обращения друг к другу. При изменении контрактов обновляй соседние эпики инструментом BoardUpdateEpic — лиды увидят ревизию и пересмотрят задачи.
3. Уровень параллельности: can_run_parallel: false — только при явной последовательной связности по данным. По умолчанию можно параллельно (true).
4. Инфраструктуру направляй DevOps Lead, автотесты и проверки контрактов — QA Lead.
5. Для каждого эпика должен быть назначен ОДИН лид из списка выше (assigned_role).

### РАБОТА С ДОСКОЙ (CRUD ЭПИКОВ):
- Первичную публикацию бэклога делай вызовом submit_architecture_backlog.
- Дальше веди эпики инструментами доски: BoardCreateEpic (новый эпик), BoardUpdateEpic (контракты/условия/переприоритезация sequence_order), BoardSetEpicStatus, BoardDeleteEpic (только пока задачи не взяты в работу).
- Следи за доской: BoardListEpics/BoardListTasks показывают, что уже сделано и что изменилось.

### ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Отвечай ТОЛЬКО вызовами инструментов (submit_architecture_backlog и Board*). Любой текстовый ответ вместо вызова инструмента является критической ошибкой. Не пиши вступлений, пояснений или markdown-разметки.

### ТРЕБУЕМАЯ СХЕМА АРГУМЕНТОВ submit_architecture_backlog:
{
  "architecture_summary": "Краткое техническое описание архитектурного решения, позволяющее любому агенту понять его без обращения к другим.",
  "tasks": [
    {
      "task_id": "Уникальный ID эпика (например, ARCH-01)",
      "title": "Название эпика",
      "description": "Подробное техническое описание эпика, включая контракты взаимодействия, обязательные к выполнению.",
      "assigned_role": "Одна из ролей: DevOps Lead, Frontend Lead, Backend Lead, QA Lead",
      "sequence_order": 1,
      "can_run_parallel": true,
      "dependencies": []
    }
  ]
}

Твой план работы:
1. Изучи текущее состояние проекта (если проект существует): List → ReadFiles.
2. Посмотри состояние доски (BoardListEpics/BoardListTasks), чтобы понимать, что уже сделано.
3. Спроектируй архитектуру и опубликуй бэклог вызовом submit_architecture_backlog (первичный шаг) либо обнови эпики инструментами BoardUpdateEpic/BoardCreateEpic.
4. Мониторь доску (BoardListBugs) — рассматривай подтверждённые QA Lead багрепорты (см. правила экспертизы).`

// bugExpertSystemPrompt — режим экспертизы багрепортов: QA Lead подтвердил
// проблему, архитектор решает, чинить ли, на какой стороне, и создаёт эпик
// исправления (или отклоняет как фичу).
const bugExpertSystemPrompt = `Ты — Системный архитектор (System Architect) в режиме экспертизы багрепортов. QA Lead уже отфильтровал «нейрослоп» и передал тебе подтверждённые проблемы (статус confirmed). Твоя задача — решить, чинить ли проблему, на какой стороне это чинить и стоит ли вообще.

Ты работаешь с общей Kanban-доской проекта (инструменты Board*).

### ПРАВИЛА ЭКСПЕРТИЗЫ:
1. Сначала прочитай багрепорт: BoardGetBug, а для контекста — связанные эпик (BoardGetEpic) и задачу (BoardListTasks), контракты.
2. Оцени строго по фактам:
   - Проблема действительно описана и воспроизводима — это БАГ (вердикт fix).
   - Ожидаемое поведение не зафиксировано контрактом, либо описанное поведение — намеренное — это ФИЧА (вердикт feature).
   - Проблема реальна, но чинить её дороже, чем польза, либо вне рамок текущей задачи пользователя — НЕ ИСПРАВЛЯЕМ (вердикт wont_fix).
3. Порядок разработки сохраняется: сначала инфраструктура, затем приложение, затем тестирование — учитывай это при создании эпика исправления.
4. Для вердикта fix создавай отдельный эпик исправления: определи сторону (assigned_role лида направления, на которой чинить), title, description с контрактом, sequence_order и зависимости. Это делается В ОДНОМ вызове BoardReviewBugReport с правилом "verdict": "fix".

### ОГРАНИЧЕНИЕ НА ФОРМАТ ОТВЕТА:
Отвечай ТОЛЬКО вызовом инструмента BoardReviewBugReport для каждого багрепорта (или сопровождающими чтениями BoardGetBug/BoardGetEpic/BoardListTasks). Любой текстовый ответ вместо вызова инструмента — критическая ошибка.

Твой план работы:
1. Прочитай список подтверждённых багрепортов (BoardListBugs, status=confirmed).
2. Для каждого: изучи контекст и вынеси вердикт вызовом BoardReviewBugReport.
3. При вердикте fix — в том же вызове укажи параметры эпика исправления (epic_task_id, epic_title, epic_description, epic_assigned_role, epic_sequence_order, epic_dependencies).`

// GetTools возвращает определения выбранных инструментов (List, ReadFiles) и
// агент-специфичного submit_architecture_backlog для публикации бэклога.
func (a *Architect) GetTools() []tools.ToolDefinition {
	defs := a.Tools.Definitions()
	return append(defs, submitBacklogDefinition())
}

// GetToolsForOllama конвертирует определения инструментов в формат Ollama
// через единый конвертер tools.ToOllama.
func (a *Architect) GetToolsForOllama() []api.Tool {
	return tools.ToOllama(a.GetTools())
}

// submitBacklogDefinition описывает схему инструмента submit_architecture_backlog
// (единый JSON Schema формат для OpenAI/Yandex/Ollama).
func submitBacklogDefinition() tools.ToolDefinition {
	return tools.ToolDefinition{
		Name: SubmitBacklogToolName,
		Description: "Опубликовать бэклог (список эпиков верхнего уровня) на общую Kanban-доску проекта. " +
			"Каждый эпик будет распределён между лидами направлений (Backend Lead, Frontend Lead, DevOps Lead, QA Lead) " +
			"и декомпозирован ими на задачи для рядовых специалистов. Единственный инструмент, которым Системный архитектор " +
			"завершает свою работу.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"architecture_summary": map[string]any{
					"type":        "string",
					"description": "Краткое техническое описание архитектурного решения, позволяющее любому агенту понять его без обращения к другим агентам.",
				},
				"tasks": map[string]any{
					"type":        "array",
					"description": "Список эпиков (крупных задач верхнего уровня) для распределения между лидами направлений.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"task_id":          map[string]any{"type": "string", "description": "Уникальный ID эпика (например ARCH-01)"},
							"title":            map[string]any{"type": "string", "description": "Название эпика"},
							"description":      map[string]any{"type": "string", "description": "Подробное техническое описание эпика: цели, контракты, ограничения."},
							"assigned_role":    map[string]any{"type": "string", "description": "Одна из ролей: Backend Lead, Frontend Lead, DevOps Lead, QA Lead"},
							"sequence_order":   map[string]any{"type": "integer", "description": "Порядок выполнения эпика"},
							"can_run_parallel": map[string]any{"type": "boolean", "description": "Может ли эпик выполняться параллельно с соседними эпиками"},
							"dependencies":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "ID эпиков, от которых зависит этот эпик"},
						},
						"required": []string{"task_id", "title", "description", "assigned_role", "sequence_order", "can_run_parallel", "dependencies"},
					},
				},
			},
			"required": []string{"architecture_summary", "tasks"},
		},
	}
}

// CallFunction диспетчеризует вызов модели: submit_architecture_backlog
// обрабатывается самим агентом (публикация эпиков на доску), остальные
// инструменты делегируются в общий реестр.
func (a *Architect) CallFunction(functionName string, functionArgs map[string]any) ([]byte, error) {
	if functionName == SubmitBacklogToolName {
		return a.submitBacklog(functionArgs)
	}
	return a.Tools.Execute(functionName, functionArgs)
}

// submitBacklog разбирает аргументы вызова, публикует эпики на доску и
// возвращает модели JSON-результат. Повторный вызов с теми же task_id
// идемпотентен (дубликаты пропускаются).
func (a *Architect) submitBacklog(args map[string]any) ([]byte, error) {
	if a.Store == nil {
		return json.Marshal(map[string]string{
			"status":  "error",
			"message": "хранилище доски не подключено (board.Store отсутствует)",
		})
	}

	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("submit_architecture_backlog: сериализация аргументов: %w", err)
	}
	backlog, err := board.UnmarshalBacklog(string(raw))
	if err != nil {
		return json.Marshal(map[string]string{
			"status":  "error",
			"message": "неверный формат бэклога: " + err.Error(),
		})
	}
	if len(backlog.Tasks) == 0 {
		return json.Marshal(map[string]string{
			"status":  "error",
			"message": "бэклог не содержит задач (tasks пуст)",
		})
	}

	ctx := context.Background()
	var created []string
	var skipped []string
	for _, ts := range backlog.Tasks {
		epic := &board.Epic{
			TaskSpec: ts,
			Summary:  backlog.ArchitectureSummary,
		}
		if err := a.Store.CreateEpic(ctx, epic); err != nil {
			if errors.Is(err, board.ErrExists) {
				skipped = append(skipped, ts.TaskID)
				continue
			}
			return json.Marshal(map[string]string{
				"status":  "error",
				"message": "публикация эпика " + ts.TaskID + ": " + err.Error(),
			})
		}
		created = append(created, ts.TaskID)
	}

	res, _ := json.Marshal(map[string]any{
		"status":               "success",
		"created_epics":        created,
		"skipped_dups":         skipped,
		"total_epics":          len(backlog.Tasks),
		"architecture_summary": backlog.ArchitectureSummary,
	})
	return res, nil
}

// Обёртки доступных инструментов. Наблюдаемые снаружи сигнатуры сохранены
// и просто делегируют в реестр (единый источник вызовов).

func (a *Architect) ReadFiles(args map[string]any) ([]byte, error) {
	return a.Tools.Execute("ReadFiles", args)
}

func (a *Architect) List(args map[string]any) ([]byte, error) {
	return a.Tools.Execute("List", args)
}
