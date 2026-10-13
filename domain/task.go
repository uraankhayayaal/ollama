package domain

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

// TaskCheckpoint — опорные точки git-истории ветки задачи для ручного отката.
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
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	GitBranch          string          `json:"git_branch,omitempty"`
	MergeConflictFiles []string        `json:"merge_conflict_files,omitempty"`
	ResumeStatus       Status          `json:"resume_status,omitempty"`
	Injections         []Injection     `json:"injections,omitempty"`
	Comments           Comments        `json:"comments,omitempty"`
	AgentState         string          `json:"agent_state,omitempty"`
	ActiveAgent        string          `json:"active_agent,omitempty"`
	Checkpoint         *TaskCheckpoint `json:"checkpoint,omitempty"`
	Attempts           int             `json:"attempts,omitempty"`
	LastError          string          `json:"last_error,omitempty"`
	PauseReason        string          `json:"pause_reason,omitempty"`
	HeartbeatAt        string          `json:"heartbeat_at,omitempty"`
	ModelTier          string          `json:"model_tier,omitempty"`
	Escalations        int             `json:"escalations,omitempty"`
}
