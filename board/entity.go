// Package board реализует организационный паттерн Kanban / Shared Task Board
// поверх Redis. Хранит общую доску задач проекта: эпики (крупные задачи от
// Системного архитектора для Тимлидов), задачи (декомпозиция эпиков лидами для
// рядовых специалистов) и их статусы.
//
// Связи:
//   - Epic — задача верхнего уровня из бэклога Системного архитектора.
//   - Task — подзадача эпика, создаётся лидом направления.
//   - эпик ссылается на свои задачи (Epic.Tasks), задача — на родительский
//     эпик (Task.EpicID).
//
// Статусы эпиков и задач единые: новая -> в анализе -> готова к работе ->
// [помощь человека] -> в работе -> на тестирование -> выполнена (или
// отменена). «Помощь человека» — боковая ветка (эскалации, форсмажоры),
// «на тестирование» — сдача разработчиком и приёмка тестировщиком.
// Переходы валидируются (ValidateTransition).
package board

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Status — статус эпика или задачи на общей доске.
type Status string

// Статусы, общие для эпиков и задач (Kanban-доска). Порядок колонок доски
// (Р-6 PLAN-2026-10-07-wip-harness-rework.md): new → analysis → ready →
// human_help → in_progress → testing → done → cancelled.
const (
	StatusNew        Status = "new"         // новая
	StatusAnalysis   Status = "analysis"    // в анализе
	StatusReady      Status = "ready"       // готова к работе
	StatusHumanHelp  Status = "human_help"  // помощь человека
	StatusInProgress Status = "in_progress" // в работе
	StatusTesting    Status = "testing"     // на тестирование
	StatusDone       Status = "done"        // выполнена
	StatusCancelled  Status = "cancelled"   // отменена
)

// Label возвращает человекочитаемое название статуса (для логов/UI).
func (s Status) Label() string {
	switch s {
	case StatusNew:
		return "новая"
	case StatusAnalysis:
		return "в анализе"
	case StatusReady:
		return "готова к работе"
	case StatusHumanHelp:
		return "помощь человека"
	case StatusInProgress:
		return "в работе"
	case StatusTesting:
		return "на тестирование"
	case StatusDone:
		return "выполнена"
	case StatusCancelled:
		return "отменена"
	default:
		return string(s)
	}
}

// Terminal сообщает, является ли статус завершающим (дальнейшие переходы
// запрещены).
func (s Status) Terminal() bool {
	return s == StatusDone || s == StatusCancelled
}

// Valid проверяет, что статус известен системе.
func (s Status) Valid() bool {
	switch s {
	case StatusNew, StatusAnalysis, StatusReady, StatusHumanHelp,
		StatusInProgress, StatusTesting, StatusDone, StatusCancelled:
		return true
	}
	return false
}

// ValidateTransition проверяет допустимость перехода from -> to по конечному
// автомату статусов Kanban-доски:
//
//	новая           -> в анализе, помощь человеку, отменена
//	в анализе       -> готова к работе, помощь человеку, отменена
//	готова к работе -> в работе, помощь человеку, отменена
//	помощь человека -> готова к работе (помощь получена, задача в очередь),
//	                   в работе (продолжаем), отмена
//	в работе        -> готова к работе (откат кода, см. ниже), на
//	                   тестирование, помощь человеку, выполнена, отменена
//	на тестирование -> в работе (замечания тестировщика), выполнена
//	                   (приёмка), помощь человеку, отмена
//	(терминальные: выполнена/отменена переходов не имеют)
//
// «Помощь человека» — боковая ветка (Р-6): вход из любого нетерминального
// статуса, выход только обратно в работу. Отмена — тоже доступна из любого
// нетерминального статуса. Соседность колонок: ready → human_help →
// in_progress → testing → done.
//
// Одинаковый статус не считается переходом (допускается для идемпотентности).
func ValidateTransition(from, to Status) error {
	if from == to {
		return nil
	}
	if !from.Valid() || !to.Valid() {
		return &StatusError{From: from, To: to, Reason: "неизвестный статус"}
	}
	// Боковые переходы: из любого нетерминального статуса — в «помощь
	// человека» (эскалация/форсмажор) или в «отменена».
	if !from.Terminal() && (to == StatusHumanHelp || to == StatusCancelled) {
		return nil
	}
	switch from {
	case StatusNew:
		if to == StatusAnalysis {
			return nil
		}
	case StatusAnalysis:
		if to == StatusReady {
			return nil
		}
	case StatusReady:
		if to == StatusInProgress {
			return nil
		}
	case StatusHumanHelp:
		// Помощь получена: задача возвращается в очередь на новый прогон
		// (ready) либо продолжается на месте (in_progress).
		if to == StatusReady || to == StatusInProgress {
			return nil
		}
	case StatusInProgress:
		// Возврат в ready — ручной откат кода задачи к опорной точке
		// (Ф-6, этап 3): ветка задачи переводится назад (gitops.RollbackWorktree),
		// а задача должна автоматически вернуться в очередь на новый прогон.
		// Считаем это безопасным, потому что откат делает только человек
		// (POST .../tasks/{id}/rollback) и только по явному подтверждению в UI.
		if to == StatusReady || to == StatusTesting || to == StatusDone {
			return nil
		}
	case StatusTesting:
		// Сдача разработчиком в работу (in_progress) при замечаниях
		// тестировщика либо приёмка (done) по итогам тестирования.
		if to == StatusInProgress || to == StatusDone {
			return nil
		}
	}
	return &StatusError{From: from, To: to, Reason: "переход не разрешён конечным автоматом"}
}

// StatusError — ошибка некорректного перехода статуса.
type StatusError struct {
	From   Status
	To     Status
	Reason string
}

func (e *StatusError) Error() string {
	return "переход статуса " + e.From.Label() + " -> " + e.To.Label() + " запрещён: " + e.Reason
}

// TaskSpec — единая структура задачи из JSON-схем Системного архитектора и
// лидов направлений (task_id, title, description, assigned_role,
// sequence_order, can_run_parallel, dependencies). Используется для эпиков
// (задачи архитектора) и задач (декомпозиция лидов).
type TaskSpec struct {
	TaskID         string   `json:"task_id"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	AssignedRole   string   `json:"assigned_role"`
	SequenceOrder  FlexInt  `json:"sequence_order"`
	CanRunParallel FlexBool `json:"can_run_parallel"`
	Dependencies   []string `json:"dependencies"`
	Contracts      []string `json:"contracts,omitempty"`
}

// Opportunity — кросс-функциональная возможность/инсайт архитектора для
// смежного направления (Ф-6 PLAN-2026-09-24-done-architect-intelligence.md): целевая роль и
// предложение. Опциональное поле бэклога — не эпик, а рекомендация лидам.
type Opportunity struct {
	TargetRole string `json:"target_role"`
	Suggestion string `json:"suggestion"`
}

// Backlog — аргументы функции submit_architecture_backlog Системного
// архитектора. Каждая задача бэклога становится эпиком на доске.
// Opportunities — опциональные кросс-функциональные возможности (Ф-6):
// фиксируются в Summary эпиков и видны лидам направлений.
type Backlog struct {
	ArchitectureSummary string        `json:"architecture_summary"`
	Tasks               []TaskSpec    `json:"tasks"`
	Opportunities       []Opportunity `json:"opportunities,omitempty"`
}

// TokenUsage — учёт расхода LLM-токенов единицей доски (Ф-1
// PLAN-2026-09-19-done-epic-task-token.md). Встроен в Epic и Task, поэтому
// поля сериализуются в те же JSON-объекты верхнего уровня.
//
// Факт (TokensInput/Output/Total) накапливается по scope задачи/эпика, пока
// единица в работе, и фиксируется при переводе в терминальный статус.
// Прогноз (TokenEstimate) ставится в момент создания единицы по истории
// завершённых (см. tokens.Predictor). Нулевые значения не сериализуются —
// старая доска читается без изменений.
type TokenUsage struct {
	// TokensInput — входные токены, потраченные на единицу работы.
	TokensInput int64 `json:"tokens_in,omitempty"`
	// TokensOutput — выходные токены, потраченные на единицу работы.
	TokensOutput int64 `json:"tokens_out,omitempty"`
	// TokensTotal — сумма входа и выхода (денормализована для UI и прогноза).
	TokensTotal int64 `json:"tokens_total,omitempty"`
	// TokenEstimate — прогноз расхода токенов (0 = прогноза нет: история
	// слишком мала или прогноз не считался).
	TokenEstimate int64 `json:"token_estimate,omitempty"`
}

// Add прибавляет порцию токенов к накопленному факту.
func (u *TokenUsage) Add(in, out int64) {
	u.TokensInput += in
	u.TokensOutput += out
	u.TokensTotal = u.TokensInput + u.TokensOutput
}

// Set записывает факт расхода (финализация: итог перезаписывает накопленное).
func (u *TokenUsage) Set(in, out int64) {
	u.TokensInput = in
	u.TokensOutput = out
	u.TokensTotal = in + out
}

// Error возвращает ошибку прогноза в процентах от оценки. Оценка нулевая —
// ошибку посчитать нельзя (ok=false).
func (u TokenUsage) Error() (pct float64, ok bool) {
	if u.TokenEstimate <= 0 {
		return 0, false
	}
	diff := float64(u.TokensTotal - u.TokenEstimate)
	if diff < 0 {
		diff = -diff
	}
	return diff / float64(u.TokenEstimate) * 100, true
}

// Epic — эпик (крупная задача верхнего уровня) на общей доске. Создаётся из
// задач Системного архитектора и передаётся Тимлиду направления для
// декомпозиции на подзадачи.
type Epic struct {
	TaskSpec
	TokenUsage
	ProjectName  string   `json:"project_name"`
	Repositories []string `json:"repositories,omitempty"`
	Tasks        []string `json:"tasks"` // ID подзадач (задачи лидов)
	Status       Status   `json:"status"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	// GitBranch — релизная ветка эпика (git-workflow Ф-1, префикс
	// ai/epic/<id>). Пусто, пока ветка не создана.
	GitBranch string `json:"git_branch,omitempty"`
	// MergeConflictFiles — файлы, в которых вливание релизной ветки эпика в
	// main (или авто-синхрон с main) упёрлось в конфликт (Ф-4). Непустой
	// список = «ветка не влилась, ждёт резолва». Очищается успешным релизом/
	// резолвом. Пусто — конфликта нет (поле не сериализуется).
	MergeConflictFiles []string `json:"merge_conflict_files,omitempty"`
	// MergedIntoMain — релизная ветка эпика влита в main (успешный релиз или
	// резолв). Показывается в карточке/модалке эпика как статус «Слит в main».
	MergedIntoMain bool `json:"merged_into_main,omitempty"`
	// Summary — сводка архитектурного решения из бэклога Системного
	// архитектора (architecture_summary); передаётся лиду направления для
	// декомпозиции эпика.
	Summary string `json:"architecture_summary"`
	// Revision — номер версии содержания эпика (растёт при изменении
	// содержания инструментами доски: описание, контракты, приоритет).
	// Статусные переходы оркестратора ревизию НЕ увеличивают.
	Revision int `json:"revision"`
	// LeadSyncedRev — ревизия, при которой лид направления последний раз
	// декомпозировал/реконсилил этот эпик. Если Revision > LeadSyncedRev —
	// содержание эпика изменилось, лиду нужна повторная ревизия задач.
	LeadSyncedRev int `json:"lead_synced_rev"`
	// RequiresReview — требуется ли ревизия Системного архитектора перед
	// декомпозицией лидом (Ф-8 PLAN-2026-09-24-done-architect-intelligence.md). Эпики,
	// созданные в чате ассистентом, — черновики: им обязательна ревизия
	// (безопасный дефолт true для новых записей). Эпики бэклога самого
	// архитектора (submit_architecture_backlog) — явный false: свой план
	// архитектор не ревьюит собой. Пока флаг true, nextLeadEpic не выдаёт
	// эпик лиду, а фаза phaseArchitectReview прогоняет его через архитектора,
	// который по итогам ревизии снимает флаг (BoardUpdateEpic/ить).
	RequiresReview bool `json:"requires_review"`
}

// Task — задача на общей доске. Создаётся лидом при декомпозиции эпика и
// выполняется рядовым специалистом. Несёт все свойства из JSON-схемы
// архитектора/лида (встроена TaskSpec) плюс связь с эпиком (EpicID).
type Task struct {
	TaskSpec
	TokenUsage
	ProjectName  string   `json:"project_name"`
	Repositories []string `json:"repositories,omitempty"`
	EpicID       string   `json:"epic_id"` // связь с родительским эпиком
	Status       Status   `json:"status"`
	Assignee     string   `json:"assignee"` // специалист, назначенный на задачу (одна задача на одного специалиста)
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	// GitBranch — фича-ветка задачи (git-workflow Ф-1, префикс
	// ai/task/<id>, база = ветка эпика). Пусто, пока ветка не создана.
	GitBranch string `json:"git_branch,omitempty"`
	// MergeConflictFiles — файлы, в которых вливание ветки задачи в релизную
	// ветку эпика упёрлось в конфликт (done→релиз или TaskMerge). Непустой
	// список = «ветка не влилась, ждёт резолва». Очищается успешным мёрджем.
	// Пусто — конфликта нет (поле не сериализуется).
	MergeConflictFiles []string `json:"merge_conflict_files,omitempty"`
	// ResumeStatus — статус, из которого задача была остановлена каскадом
	// остановки эпика («помощь человека», Ф-6). Возобновление возвращает
	// задачу именно в него: оркестратор заново проверит зависимости и
	// фазовые гейты на прежнем месте цепочки. Пусто, если каскадной
	// остановки не было (в т.ч. «помощь человека» по эскалации/форсмажору —
	// такая задача возвращается в работу только вручную).
	ResumeStatus Status `json:"resume_status,omitempty"`
	// Injections — промпт-инъекции, привязанные к задаче. Применяются к
	// работающей модели в рамках этой задачи (runtime injections): правка
	// списка видна модели со СЛЕДУЮЩЕГО запроса — в том числе в середине уже
	// идущего агентского цикла. Тип и правила — board/injection.go.
	Injections []Injection `json:"injections,omitempty"`
	// Comments — комментарии к задаче (замечания тестировщика, пользовательские, системные).
	Comments Comments `json:"comments,omitempty"`
	// Ф-6 State Tracking: живое состояние задачи. Заполняется по ходу раундов
	// агента (runner → доска), поэтому переживает рестарт сервера: по полям
	// видно, чем занят прогон, где последняя рабочая точка и каким проверочным
	// выводом закончился последний раунд.
	AgentState string `json:"agent_state,omitempty"`
	// ActiveAgent — специалист, работающий с задачей сейчас (роль/имя).
	ActiveAgent string `json:"active_agent,omitempty"`
	// Checkpoint — опорные точки git-истории ветки задачи для ручного отката
	// (см. POST .../tasks/{id}/rollback). nil — откатываться не к чему.
	Checkpoint *TaskCheckpoint `json:"checkpoint,omitempty"`
	// Attempts — сколько раз задача бралась в работу (in_progress).
	Attempts int `json:"attempts,omitempty"`
	// LastError — стадия и усечённый нормализованный вывод последней
	// упавшей проверки (для диалога с человеком и разбора в UI).
	LastError string `json:"last_error,omitempty"`
	// PauseReason — причина остановки, указанная специалистом, когда он
	// переводит задачу в human_help (задача невыполнима: нет исходного кода,
	// зависимостей, есть блокер) либо оркестратором при исчерпании бюджета
	// автономии. Обязательный аргумент reason инструмента BoardSetTaskStatus;
	// очищается при выходе из human_help. Человек видит её в карточке задачи —
	// иначе остановка выглядела бы как загадочный простой.
	PauseReason string `json:"pause_reason,omitempty"`
	// HeartbeatAt — время последнего раунда агента. Watchdog по нему, а не по
	// догадкам, понимает, жива ли задача после рестарта сервера.
	HeartbeatAt string `json:"heartbeat_at,omitempty"`
	// ModelTier — требование к модели, накопленное по ходу прогона. Сейчас
	// единственное значение — ModelTierLarge: «эта задача не осиливается на
	// дешёвой модели». Требование живёт в записи задачи, а не внутри одного
	// вызова Generate: после эскалации следующий прогон (и любой рестарт
	// сервера) продолжает на сильной модели, а не начинает заново с дешёвой.
	ModelTier string `json:"model_tier,omitempty"`
	// Escalations — сколько раз задача уже получила эскалацию (сильная модель
	// + инъекция с диагнозом) после зацикливания. Служит бюджетом автономии:
	// исчерпанный бюджет — это остановка и эскалация человеку, а не бесконечные
	// повторы (см. агент-оркестратор, KANBAN_MAX_ESCALATIONS).
	Escalations int `json:"escalations,omitempty"`
}

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
type TaskCheckpoint struct {
	BaseSHA     string `json:"base_sha"`
	LastSHA     string `json:"last_sha,omitempty"`
	LastGoodSHA string `json:"last_good_sha,omitempty"`
}

// UnmarshalJSON для Epic с безопасным дефолтом Ф-8: записи, где поле
// requires_review отсутствует (старая схема, черновики), интерпретируются как
// требующие ревизии архитектора. Явные false (бэклог submit_architecture_backlog)
// и true (чат-эпики) сохраняются как есть.
func (e *Epic) UnmarshalJSON(data []byte) error {
	type epicAlias Epic
	var a epicAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*e = Epic(a)
	// Отсутствие ключа в сохранённой записи = требуется ревизия (безопасный
	// дефолт Ф-8). Проверяем по сырым полям объекта.
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err == nil {
		if _, present := fields["requires_review"]; !present {
			e.RequiresReview = true
		}
	}
	return nil
}

// BugStatus — статус багрепорта на общей доске.
type BugStatus string

// Статусы багрепортов: цепочка ревью QA Lead -> экспертиза Системного
// архитектора. Терминальные статусы переходов не имеют.
const (
	BugStatusNew       BugStatus = "new"       // создан QA-специалистом, ждёт ревью
	BugStatusConfirmed BugStatus = "confirmed" // принят QA Lead, передан архитектору
	BugStatusSlop      BugStatus = "slop"      // отклонён QA Lead: «нейрослоп»/не проблема
	BugStatusFix       BugStatus = "fix"       // архитектор: чинить, создан эпик исправления
	BugStatusFeature   BugStatus = "feature"   // архитектор: это фича, не баг
	BugStatusWontFix   BugStatus = "wont_fix"  // архитектор: исправлять не будем
	BugStatusFixed     BugStatus = "fixed"     // эпик исправления выполнен
)

// Label возвращает человекочитаемое название статуса багрепорта.
func (s BugStatus) Label() string {
	switch s {
	case BugStatusNew:
		return "не рассмотрен"
	case BugStatusConfirmed:
		return "подтверждён"
	case BugStatusSlop:
		return "нейрослоп"
	case BugStatusFix:
		return "требует исправления"
	case BugStatusFeature:
		return "это фича"
	case BugStatusWontFix:
		return "не исправляем"
	case BugStatusFixed:
		return "исправлен"
	default:
		return string(s)
	}
}

// Terminal сообщает, является ли статус багрепорта завершающим.
func (s BugStatus) Terminal() bool {
	switch s {
	case BugStatusSlop, BugStatusFeature, BugStatusWontFix, BugStatusFixed:
		return true
	}
	return false
}

// Valid проверяет, что статус багрепорта известен системе.
func (s BugStatus) Valid() bool {
	switch s {
	case BugStatusNew, BugStatusConfirmed, BugStatusSlop,
		BugStatusFix, BugStatusFeature, BugStatusWontFix, BugStatusFixed:
		return true
	}
	return false
}

// ValidateBugTransition проверяет допустимость перехода статуса багрепорта:
//
//	новый       -> подтверждён | нейрослоп
//	подтверждён -> фича | не исправляем | требует исправления
//	требует исправления -> исправлен
//	(терминальные: нейрослоп/фича/не исправляем/исправлен переходов не имеют)
//
// Одинаковый статус не считается переходом (идемпотентность).
func ValidateBugTransition(from, to BugStatus) error {
	if from == to {
		return nil
	}
	if !from.Valid() || !to.Valid() {
		return &BugStatusError{From: from, To: to, Reason: "неизвестный статус багрепорта"}
	}
	switch from {
	case BugStatusNew:
		if to == BugStatusConfirmed || to == BugStatusSlop {
			return nil
		}
	case BugStatusConfirmed:
		if to == BugStatusFix || to == BugStatusFeature || to == BugStatusWontFix {
			return nil
		}
	case BugStatusFix:
		if to == BugStatusFixed {
			return nil
		}
	}
	return &BugStatusError{From: from, To: to, Reason: "переход не разрешён цепочкой ревью багрепорта"}
}

// BugStatusError — ошибка некорректного перехода статуса багрепорта.
type BugStatusError struct {
	From   BugStatus
	To     BugStatus
	Reason string
}

func (e *BugStatusError) Error() string {
	return "переход статуса багрепорта " + e.From.Label() + " -> " + e.To.Label() + " запрещён: " + e.Reason
}

// BugReport — багрепорт с общей доски. QA-специалисты создают его при
// обнаружении проблемы, QA Lead фильтрует «нейрослоп», Системный архитектор
// проводит экспертизу (это фича/не исправляем/чинить) и при необходимости
// создаёт эпик исправления (FixEpicID), после выполнения которого багрепорт
// закрывается как «исправлен».
type BugReport struct {
	BugID        string    `json:"bug_id"`
	ProjectName  string    `json:"project_name"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	ReporterRole string    `json:"reporter_role"` // специалист, обнаруживший проблему
	TaskID       string    `json:"task_id"`       // задача, в которой найден баг (источник)
	EpicID       string    `json:"epic_id"`       // эпик задачи-источника
	Status       BugStatus `json:"status"`
	Verdict      string    `json:"verdict"`     // решение архитектора (fix/feature/wont_fix)
	FixEpicID    string    `json:"fix_epic_id"` // эпик, созданный для исправления
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
}

// Meta — метаданные доски проекта: исходная задача пользователя и общий
// статус решения (done — все эпики и задачи успешно выполнены).
type Meta struct {
	ProjectName string `json:"project_name"`
	Task        string `json:"task"`
	Status      Status `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// flexInt — целое число из JSON-схемы, которое модель может прислать либо
// числом, либо строкой ("1"). Парсится толерантно.
type FlexInt int

// UnmarshalJSON принимает число или строку (в т.ч. с пробелами).
func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if i, err := strconv.Atoi(s); err == nil {
		*f = FlexInt(i)
		return nil
	}
	*f = 0
	return nil
}

// Int возвращает значение как int.
func (f FlexInt) Int() int { return int(f) }

// flexBool — булев флаг из JSON-схемы: модель может прислать true/false,
// строку "true"/"false" или 1/0.
type FlexBool bool

// UnmarshalJSON принимает bool, строку или число.
func (b *FlexBool) UnmarshalJSON(raw []byte) error {
	switch strings.ToLower(strings.TrimSpace(strings.Trim(string(raw), `"`))) {
	case "1", "true", "yes", "да":
		*b = true
	case "0", "false", "no", "нет", "":
		*b = false
	default:
		*b = false
	}
	return nil
}

// Bool возвращает значение как bool.
func (b FlexBool) Bool() bool { return bool(b) }

// UnmarshalBacklog разбирает JSON-ответ/аргументы Системного архитектора
// (backlog с полем tasks) в Backlog. Устойчив к markdown-обёрткам и типовому
// мусору модели (экранирование строк внутри текста).
func UnmarshalBacklog(data string) (*Backlog, error) {
	data = extractJSON(data)
	var b Backlog
	if err := json.Unmarshal([]byte(sanitizeJSON(data)), &b); err != nil {
		return nil, err
	}
	return &b, nil
}

// UnmarshalTasks разбирает JSON-ответ лида направления (декомпозиция с полем
// tasks) в список задач. Лиды используют разные корневые ключи
// (frontend_lead_summary, backend_lead_summary и т.п.), поэтому разбираем их
// толерантно, извлекая только массив tasks.
func UnmarshalTasks(data string) ([]TaskSpec, error) {
	root := struct {
		Tasks []TaskSpec `json:"tasks"`
	}{}
	// Объект-обёртка проходит только если в нём есть реальные задачи: объекты
	// без ключа "tasks" (например, элемент декомпозиции без обёртки) парсятся
	// lenient'но в пустой список, и такой ответ не должен считаться успехом.
	if err := json.Unmarshal([]byte(sanitizeJSON(extractJSON(data))), &root); err == nil && len(root.Tasks) > 0 {
		return root.Tasks, nil
	}
	// Некоторые модели возвращают голый массив задач вместо объекта
	// {"tasks":[...]} — пробуем извлечь JSON-массив от первой "[" до
	// последней "]" и разобрать задачи напрямую.
	if arr := extractJSONArray(data); arr != nil {
		var tasks []TaskSpec
		if err := json.Unmarshal(arr, &tasks); err == nil && len(tasks) > 0 {
			return tasks, nil
		}
	}
	// Оба варианта не прошли — возвращаем ошибку разбора объекта
	// (для диагностики модели), а не отвлечённую ошибку второго шага.
	rootErr := json.Unmarshal([]byte(sanitizeJSON(extractJSON(data))), &root)
	return nil, rootErr
}

// extractJSON вырезает из текста модели первый JSON-объект (открывающаяся
// '{' до последней '}'), отбрасывая markdown-обёртки и лишний текст.
func extractJSON(s string) string {
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return s
	}
	end := -1
	for i := len(s) - 1; i >= start; i-- {
		if s[i] == '}' {
			end = i
			break
		}
	}
	if end < 0 {
		return s
	}
	return s[start : end+1]
}

// extractJSONArray вырезает из текста модели первый JSON-массив (от первой
// '[' до последней ']'), отбрасывая markdown-обёртки и лишний текст. Возвращает
// nil, если в тексте нет символа '['.
func extractJSONArray(s string) []byte {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '[' {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := -1
	for i := len(s) - 1; i >= start; i-- {
		if s[i] == ']' {
			end = i
			break
		}
	}
	if end < 0 {
		return nil
	}
	return []byte(sanitizeJSON(s[start : end+1]))
}

// sanitizeJSON чинит типовые ошибки модели: реальные управляющие символы внутри
// JSON-строк (\n, \r) экранируются, вне строк \r убирается (аналог логики
// планировщика).
func sanitizeJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false

	for _, r := range s {
		if inString {
			switch {
			case escaped:
				b.WriteRune(r)
				escaped = false
			case r == '\\':
				b.WriteRune(r)
				escaped = true
			case r == '"':
				b.WriteRune(r)
				inString = false
			case r < 0x20:
				writeUnicodeEscape(&b, r)
			default:
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '"':
			b.WriteRune(r)
			inString = true
		case '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// writeUnicodeEscape записывает r в \uXXXX-форме.
func writeUnicodeEscape(b *strings.Builder, r rune) {
	const hex = "0123456789abcdef"
	b.WriteString(`\u`)
	for shift := 12; shift >= 0; shift -= 4 {
		b.WriteByte(hex[(r>>shift)&0xf])
	}
}
