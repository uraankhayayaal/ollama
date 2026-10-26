// Package board реализует организационный паттерн Kanban / Shared Task Board
// поверх Redis. Хранит общую доску задач проекта: эпики (крупные задачи верхнего
// уровня) и задачи (подзадачи эпиков) и их статусы.
//
// Связи:
//   - Epic — задача верхнего уровня.
//   - Task — подзадача эпика.
//   - эпик ссылается на свои задачи (Epic.Tasks), задача — на родительский
//     эпик (Task.EpicID).
//
// Статусы эпиков: draft → business_review → system_analysis → done (автоматически, когда все задачи done).
// Статусы задач: infra → test_cases → development → done (строго линейно, Test-First).
// Переходы валидируются (ValidateTransition).
package board

import (
	"ai/domain"
)

// Status — статус эпика или задачи на общей доске. Тип и конечный автомат
// переходов принадлежат домену (domain.Status / domain.ValidateTransition);
// здесь — алиасы для совместимости кода доски.
//
// Порядок колонок доски (эпики): draft → business_review → system_analysis → done.
// Порядок колонок доски (задачи): infra → test_cases → development → done.
type Status = domain.Status

const (
	// Эпики
	StatusDraft           = domain.StatusDraft           // черновик
	StatusBusinessReview  = domain.StatusBusinessReview  // бизнес-ревью
	StatusSystemAnalysis  = domain.StatusSystemAnalysis  // системный анализ
	// Задачи
	StatusInfra       = domain.StatusInfra       // инфраструктура
	StatusTestCases   = domain.StatusTestCases   // написание тестов
	StatusDevelopment = domain.StatusDevelopment // разработка
	// Общие
	StatusDone      = domain.StatusDone      // выполнена
	StatusCancelled = domain.StatusCancelled // отменена
)

// EntityType — тип сущности на доске (алиас доменного типа).
type EntityType = domain.EntityType

const (
	EntityTypeEpic = domain.EntityTypeEpic
	EntityTypeTask = domain.EntityTypeTask
)

// ValidateTransition делегирует доменному конечному автомату статусов
// (domain.ValidateTransition).
func ValidateTransition(from, to Status, entityType EntityType) error {
	return domain.ValidateTransition(from, to, entityType)
}

// StatusError — ошибка некорректного перехода статуса (доменный тип).
type StatusError = domain.StatusError

// TaskSpec — единая структура задачи из JSON-схем Системного архитектора и
// лидов направлений (task_id, title, description, assigned_role,
// sequence_order, can_run_parallel, dependencies). Тип принадлежит домену
// (domain.TaskSpec), здесь — алиас.
type TaskSpec = domain.TaskSpec

// Opportunity — кросс-функциональная возможность/инсайт архитектора для
// смежного направления (Ф-6 PLAN-2026-09-24-done-architect-intelligence.md): целевая роль и
// предложение. Тип принадлежит домену (domain.Opportunity), здесь — алиас.
type Opportunity = domain.Opportunity

// Backlog — аргументы функции submit_architecture_backlog Системного
// архитектора. Тип принадлежит домену (domain.Backlog), здесь — алиас.
type Backlog = domain.Backlog

// TokenUsage — учёт расхода LLM-токенов единицей доски. Тип и методы
// (Add/Set/Error) принадлежат домену (domain.TokenUsage), здесь — алиас.
type TokenUsage = domain.TokenUsage

// Epic — эпик (крупная задача верхнего уровня) на общей доске. Создаётся из
// задач Системного архитектора и передаётся Тимлиду направления для
// декомпозиции на подзадачи.
type Epic = domain.Epic

// Task — задача на общей доске. Создаётся лидом при декомпозиции эпика и
// выполняется рядовым специалистом. Несёт все свойства из JSON-схемы
// архитектора/лида (встроена TaskSpec) плюс связь с эпиком (EpicID).
type Task = domain.Task

// Требования к модели задачи (model_tier).
const (
	// ModelTierLarge — работать на большой модели (сильный слой).
	ModelTierLarge = "large"
)

// Состояния агента на доске (agent_state).
const (
	AgentStateWritingCode  = "writing_code"  // правит код
	AgentStateRunningTests = "running_tests" // гоняет проверку
	AgentStateFixingErrors = "fixing_errors" // проверка упала, разбирает вывод
	AgentStateIdle         = "idle"          // ничего не менял
)

// TaskCheckpoint — опорные точки ветки задачи для ручного отката (Ф-6).
// Заполняется по ходу прогона: base_sha — HEAD в момент создания worktree
// задачи, last_sha — последний промежуточный коммит раунда, last_good_sha —
// коммит, после которого проверка была зелёной («снести всё» и «вернуться к
// рабочему» — два разных отката).
type TaskCheckpoint = domain.TaskCheckpoint

// Meta — метаданные доски проекта: исходная задача пользователя и общий
// статус решения (done — все эпики и задачи успешно выполнены). Тип принадлежит
// домену (domain.Meta), здесь — алиас.
type Meta = domain.Meta

// FlexInt — целое число из JSON-схемы (число или строка). Тип и разбор
// принадлежат домену (domain.FlexInt), здесь — алиас.
type FlexInt = domain.FlexInt

// FlexBool — булев флаг из JSON-схемы (bool, строка или число). Тип и разбор
// принадлежат домену (domain.FlexBool), здесь — алиас.
type FlexBool = domain.FlexBool

// UnmarshalBacklog разбирает JSON-ответ/аргументы Системного архитектора
// (backlog с полем tasks). Реализация принадлежит домену (domain.UnmarshalBacklog).
func UnmarshalBacklog(data string) (*Backlog, error) { return domain.UnmarshalBacklog(data) }

// UnmarshalTasks разбирает JSON-ответ лида направления (декомпозиция с полем
// tasks) в список задач. Реализация принадлежит домену (domain.UnmarshalTasks).
func UnmarshalTasks(data string) ([]TaskSpec, error) { return domain.UnmarshalTasks(data) }
