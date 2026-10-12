package domain

import "time"

type Epic struct {
	TaskSpec
	TokenUsage
	ProjectName        string    `json:"project_name"`
	Repositories       []string  `json:"repositories,omitempty"`
	Tasks              []string  `json:"tasks"`
	Status             Status    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	GitBranch          string    `json:"git_branch,omitempty"`
	MergeConflictFiles []string  `json:"merge_conflict_files,omitempty"`
	MergedIntoMain     bool      `json:"merged_into_main,omitempty"`
	Summary            string    `json:"architecture_summary"`
	Priority           int       `json:"priority,omitempty"`
	Revision           int       `json:"revision"`
	LeadSyncedRev      int       `json:"lead_synced_rev"`
	RequiresReview     bool      `json:"requires_review"`
}
