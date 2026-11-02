package services

import (
	"ai/domain"
	"encoding/json"
	"errors"
)

const (
	CheckpointPending = "pending"
	CheckpointRunning = "running"
	CheckpointDone    = "done"
	CheckpointFailed  = "failed"
	CheckpointSkipped = "skipped"
)

var ErrCheckpointNotFound = errors.New("чекпоинт не найден")

type CheckpointSnapshot struct {
	ProjectName   string                    `json:"project_name"`
	Summary       string                    `json:"summary"`
	PlanJSON      json.RawMessage           `json:"plan"`
	Completed     map[string]bool           `json:"completed"`
	Statuses      map[string]string         `json:"statuses"`
	Rounds        map[string]int            `json:"rounds,omitempty"`
	Conversations map[string]json.RawMessage `json:"conversations,omitempty"`
	Metrics       map[string]map[string]any `json:"metrics,omitempty"`
	Waves         [][]string                `json:"waves,omitempty"`
	UpdatedAt     string                    `json:"updated_at"`
}

type Tool interface {
	Name() string
	Definition() domain.ToolDefinition
	Execute(args map[string]any) ([]byte, error)
}

type LSPDiag struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type APIEntry struct {
	Kind string
	Name string
	Sign string
}

type PublicAPISnapshot struct {
	Entries []APIEntry
}
