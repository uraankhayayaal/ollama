package services

import (
	"ai/domain"
	"context"
)

var ErrNotFound = domain.ErrNotFound

type BoardStore interface {
	CreateTask(ctx context.Context, task *domain.Task) error
	GetTask(ctx context.Context, id string) (*domain.Task, error)
	SaveTask(ctx context.Context, task *domain.Task) error
	ListTasks(ctx context.Context) ([]*domain.Task, error)
	TasksByEpic(ctx context.Context, epicID string) ([]*domain.Task, error)
	SetTaskStatus(ctx context.Context, id string, status domain.Status) error
	PatchTask(ctx context.Context, id string, mutate func(*domain.Task) error) error
	DeleteTask(ctx context.Context, id string) error
	MoveTask(ctx context.Context, taskID, toEpicID string) error
	AddTaskComment(ctx context.Context, taskID string, comment domain.Comment) ([]domain.Comment, error)
	SetTaskComments(ctx context.Context, taskID string, comments domain.Comments) error
	SetTaskInjections(ctx context.Context, taskID string, injections []domain.Injection) error
	CreateEpic(ctx context.Context, epic *domain.Epic) error
	GetEpic(ctx context.Context, id string) (*domain.Epic, error)
	SaveEpic(ctx context.Context, epic *domain.Epic) error
	ListEpics(ctx context.Context) ([]*domain.Epic, error)
	SetEpicStatus(ctx context.Context, id string, status domain.Status) error
	DeleteEpic(ctx context.Context, id string) error
	CreateBugReport(ctx context.Context, bug *domain.BugReport) error
	GetBugReport(ctx context.Context, id string) (*domain.BugReport, error)
	SaveBugReport(ctx context.Context, bug *domain.BugReport) error
	ListBugReports(ctx context.Context) ([]*domain.BugReport, error)
	SetBugStatus(ctx context.Context, id string, status domain.BugStatus) error
	ReferenceExists(ctx context.Context, id string) error
	GetMeta(ctx context.Context) (*domain.Meta, error)
	SaveMeta(ctx context.Context, m *domain.Meta) error
	EpicCounts(ctx context.Context) (*domain.StatusCounts, error)
	TaskCounts(ctx context.Context) (*domain.StatusCounts, error)
	MarkBugsFixedForEpic(ctx context.Context, epicID string) (int, error)
	FinalizeTaskTokens(ctx context.Context, id string, in, out int64) error
	FinalizeEpicTokens(ctx context.Context, id string, in, out int64) error
	AllDone(ctx context.Context) (bool, error)
	Project() string
}
