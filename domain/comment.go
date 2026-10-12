package domain

import "time"

type Comment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	Author    string    `json:"author"`
	Type      string    `json:"type"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Accounted bool      `json:"accounted,omitempty"`
}

type Comments []Comment

func (cs Comments) Len() int           { return len(cs) }
func (cs Comments) Swap(i, j int)      { cs[i], cs[j] = cs[j], cs[i] }
func (cs Comments) Less(i, j int) bool { return cs[i].CreatedAt.Before(cs[j].CreatedAt) }
