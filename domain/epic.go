package domain

import "encoding/json"

type Epic struct {
	TaskSpec
	TokenUsage
	ProjectName        string   `json:"project_name"`
	Repositories       []string `json:"repositories,omitempty"`
	Tasks              []string `json:"tasks"`
	Status             Status   `json:"status"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
	GitBranch          string   `json:"git_branch,omitempty"`
	MergeConflictFiles []string `json:"merge_conflict_files,omitempty"`
	MergedIntoMain     bool     `json:"merged_into_main,omitempty"`
	Summary            string   `json:"architecture_summary"`
	Priority           int      `json:"priority,omitempty"`
	Revision           int      `json:"revision"`
	LeadSyncedRev      int      `json:"lead_synced_rev"`
	RequiresReview     bool     `json:"requires_review"`
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
