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
