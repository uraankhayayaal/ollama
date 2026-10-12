package domain

import "time"

type TaskSpec struct {
	TaskID         string   `json:"task_id"`
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	AssignedRole   string   `json:"assigned_role"`
	SequenceOrder  int      `json:"sequence_order"`
	CanRunParallel bool     `json:"can_run_parallel"`
	Dependencies   []string `json:"dependencies"`
	Contracts      []string `json:"contracts,omitempty"`
}

type TokenUsage struct {
	TokensInput   int64 `json:"tokens_in,omitempty"`
	TokensOutput  int64 `json:"tokens_out,omitempty"`
	TokensTotal   int64 `json:"tokens_total,omitempty"`
	TokenEstimate int64 `json:"token_estimate,omitempty"`
}

type TaskCheckpoint struct {
	BaseSHA     string `json:"base_sha"`
	LastSHA     string `json:"last_sha,omitempty"`
	LastGoodSHA string `json:"last_good_sha,omitempty"`
}

type Task struct {
	TaskSpec
	TokenUsage
	ProjectName        string          `json:"project_name"`
	Repositories       []string        `json:"repositories,omitempty"`
	EpicID             string          `json:"epic_id"`
	Status             Status          `json:"status"`
	Assignee           string          `json:"assignee"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	GitBranch          string          `json:"git_branch,omitempty"`
	MergeConflictFiles []string        `json:"merge_conflict_files,omitempty"`
	ResumeStatus       Status          `json:"resume_status,omitempty"`
	AgentState         string          `json:"agent_state,omitempty"`
	ActiveAgent        string          `json:"active_agent,omitempty"`
	Checkpoint         *TaskCheckpoint `json:"checkpoint,omitempty"`
	Attempts           int             `json:"attempts,omitempty"`
	LastError          string          `json:"last_error,omitempty"`
	PauseReason        string          `json:"pause_reason,omitempty"`
	HeartbeatAt        time.Time       `json:"heartbeat_at,omitempty"`
	ModelTier          string          `json:"model_tier,omitempty"`
	Escalations        int             `json:"escalations,omitempty"`
	Injections         []Injection     `json:"injections,omitempty"`
	Comments           Comments        `json:"comments,omitempty"`
}
