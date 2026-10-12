package domain

import (
	"strconv"
	"strings"
)

type FlexInt int

func (f *FlexInt) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(strings.Trim(string(b), `"`))
	if i, err := strconv.Atoi(s); err == nil {
		*f = FlexInt(i)
		return nil
	}
	*f = 0
	return nil
}

func (f FlexInt) Int() int { return int(f) }

type FlexBool bool

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

func (b FlexBool) Bool() bool { return bool(b) }

type BugStatus string

const (
	BugStatusNew       BugStatus = "new"
	BugStatusConfirmed BugStatus = "confirmed"
	BugStatusSlop      BugStatus = "slop"
	BugStatusFix       BugStatus = "fix"
	BugStatusFeature   BugStatus = "feature"
	BugStatusWontFix   BugStatus = "wont_fix"
	BugStatusFixed     BugStatus = "fixed"
)

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

func (s BugStatus) Terminal() bool {
	switch s {
	case BugStatusSlop, BugStatusFeature, BugStatusWontFix, BugStatusFixed:
		return true
	}
	return false
}

func (s BugStatus) Valid() bool {
	switch s {
	case BugStatusNew, BugStatusConfirmed, BugStatusSlop,
		BugStatusFix, BugStatusFeature, BugStatusWontFix, BugStatusFixed:
		return true
	}
	return false
}

type BugReport struct {
	BugID        string    `json:"bug_id"`
	ProjectName  string    `json:"project_name"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	ReporterRole string    `json:"reporter_role"`
	TaskID       string    `json:"task_id"`
	EpicID       string    `json:"epic_id"`
	Status       BugStatus `json:"status"`
	Verdict      string    `json:"verdict"`
	FixEpicID    string    `json:"fix_epic_id"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
}

type Meta struct {
	ProjectName string `json:"project_name"`
	Task        string `json:"task"`
	Status      Status `json:"status"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Opportunity struct {
	TargetRole string `json:"target_role"`
	Suggestion string `json:"suggestion"`
}

type Backlog struct {
	ArchitectureSummary string        `json:"architecture_summary"`
	Tasks               []TaskSpec    `json:"tasks"`
	Opportunities       []Opportunity `json:"opportunities,omitempty"`
}

const (
	ModelTierLarge = "large"
)

const (
	AgentStateWritingCode  = "writing_code"
	AgentStateRunningTests = "running_tests"
	AgentStateFixingErrors = "fixing_errors"
	AgentStateIdle         = "idle"
)
