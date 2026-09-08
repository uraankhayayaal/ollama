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
// в работе -> выполнена (или отменена). Переходы валидируются (ValidateTransition).
package board

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Status — статус эпика или задачи на общей доске.
type Status string

// Статусы, общие для эпиков и задач (Kanban-доска).
const (
	StatusNew        Status = "new"         // новая
	StatusAnalysis   Status = "analysis"    // в анализе
	StatusReady      Status = "ready"       // готова к работе
	StatusInProgress Status = "in_progress" // в работе
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
	case StatusInProgress:
		return "в работе"
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
	case StatusNew, StatusAnalysis, StatusReady, StatusInProgress, StatusDone, StatusCancelled:
		return true
	}
	return false
}

// ValidateTransition проверяет допустимость перехода from -> to по конечному
// автомату статусов Kanban-доски:
//
//	новая         -> в анализе, отменена
//	в анализе     -> готова к работе, отменена
//	готова к работе -> в работе, отменена
//	в работе      -> выполнена, отменена
//	(терминальные: выполнена/отменена переходов не имеют)
//
// Одинаковый статус не считается переходом (допускается для идемпотентности).
func ValidateTransition(from, to Status) error {
	if from == to {
		return nil
	}
	if !from.Valid() || !to.Valid() {
		return &StatusError{From: from, To: to, Reason: "неизвестный статус"}
	}
	switch from {
	case StatusNew:
		if to == StatusAnalysis || to == StatusCancelled {
			return nil
		}
	case StatusAnalysis:
		if to == StatusReady || to == StatusCancelled {
			return nil
		}
	case StatusReady:
		if to == StatusInProgress || to == StatusCancelled {
			return nil
		}
	case StatusInProgress:
		if to == StatusDone || to == StatusCancelled {
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
}

// Backlog — аргументы функции submit_architecture_backlog Системного
// архитектора. Каждая задача бэклога становится эпиком на доске.
type Backlog struct {
	ArchitectureSummary string     `json:"architecture_summary"`
	Tasks               []TaskSpec `json:"tasks"`
}

// Epic — эпик (крупная задача верхнего уровня) на общей доске. Создаётся из
// задач Системного архитектора и передаётся Тимлиду направления для
// декомпозиции на подзадачи.
type Epic struct {
	TaskSpec
	ProjectName string   `json:"project_name"`
	Tasks       []string `json:"tasks"` // ID подзадач (задачи лидов)
	Status      Status   `json:"status"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
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
}

// Task — задача на общей доске. Создаётся лидом при декомпозиции эпика и
// выполняется рядовым специалистом. Несёт все свойства из JSON-схемы
// архитектора/лида (встроена TaskSpec) плюс связь с эпиком (EpicID).
type Task struct {
	TaskSpec
	ProjectName string `json:"project_name"`
	EpicID      string `json:"epic_id"` // связь с родительским эпиком
	Status      Status `json:"status"`
	Assignee    string `json:"assignee"` // специалист, назначенный на задачу (одна задача на одного специалиста)
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
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
	data = extractJSON(data)
	var root struct {
		Tasks []TaskSpec `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(sanitizeJSON(data)), &root); err != nil {
		return nil, err
	}
	return root.Tasks, nil
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
