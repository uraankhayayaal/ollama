package board

import (
	"ai/domain"
	"ai/domain/services"
	"context"
)

type DomainStore struct {
	*Store
}

func NewDomainStore(s *Store) services.BoardStore {
	if s == nil {
		return nil
	}
	return &DomainStore{Store: s}
}

func (d *DomainStore) CreateTask(ctx context.Context, task *domain.Task) error {
	bt := boardTaskFromDomain(task)
	return d.Store.CreateTask(ctx, bt)
}

func (d *DomainStore) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	t, err := d.Store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	return domainTaskFromBoard(t), nil
}

func (d *DomainStore) SaveTask(ctx context.Context, task *domain.Task) error {
	bt := boardTaskFromDomain(task)
	return d.Store.SaveTask(ctx, bt)
}

func (d *DomainStore) ListTasks(ctx context.Context) ([]*domain.Task, error) {
	tasks, err := d.Store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Task, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, domainTaskFromBoard(t))
	}
	return out, nil
}

func (d *DomainStore) TasksByEpic(ctx context.Context, epicID string) ([]*domain.Task, error) {
	tasks, err := d.Store.TasksByEpic(ctx, epicID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Task, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, domainTaskFromBoard(t))
	}
	return out, nil
}

func (d *DomainStore) SetTaskStatus(ctx context.Context, id string, status domain.Status) error {
	return d.Store.SetTaskStatus(ctx, id, boardStatusFromDomain(status))
}

func (d *DomainStore) PatchTask(ctx context.Context, id string, mutate func(*domain.Task) error) error {
	return d.Store.PatchTask(ctx, id, func(t *Task) error {
		dt := domainTaskFromBoard(t)
		if err := mutate(dt); err != nil {
			return err
		}
		*t = *boardTaskFromDomain(dt)
		return nil
	})
}

func (d *DomainStore) DeleteTask(ctx context.Context, id string) error {
	return d.Store.DeleteTask(ctx, id)
}

func (d *DomainStore) MoveTask(ctx context.Context, taskID, toEpicID string) error {
	return d.Store.MoveTask(ctx, taskID, toEpicID)
}

func (d *DomainStore) AddTaskComment(ctx context.Context, taskID string, comment domain.Comment) ([]domain.Comment, error) {
	cm := boardCommentFromDomain(comment)
	list, err := d.Store.AddTaskComment(ctx, taskID, cm)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Comment, 0, len(list))
	for _, c := range list {
		out = append(out, domainCommentFromBoard(c))
	}
	return out, nil
}

func (d *DomainStore) SetTaskComments(ctx context.Context, taskID string, comments domain.Comments) error {
	cms := boardCommentsFromDomain(comments)
	return d.Store.SetTaskComments(ctx, taskID, cms)
}

func (d *DomainStore) SetTaskInjections(ctx context.Context, taskID string, injections []domain.Injection) error {
	injs := boardInjectionsFromDomain(injections)
	return d.Store.SetTaskInjections(ctx, taskID, injs)
}

func (d *DomainStore) CreateEpic(ctx context.Context, epic *domain.Epic) error {
	be := boardEpicFromDomain(epic)
	return d.Store.CreateEpic(ctx, be)
}

func (d *DomainStore) GetEpic(ctx context.Context, id string) (*domain.Epic, error) {
	e, err := d.Store.GetEpic(ctx, id)
	if err != nil {
		return nil, err
	}
	return domainEpicFromBoard(e), nil
}

func (d *DomainStore) SaveEpic(ctx context.Context, epic *domain.Epic) error {
	be := boardEpicFromDomain(epic)
	return d.Store.SaveEpic(ctx, be)
}

func (d *DomainStore) ListEpics(ctx context.Context) ([]*domain.Epic, error) {
	epics, err := d.Store.ListEpics(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Epic, 0, len(epics))
	for _, e := range epics {
		out = append(out, domainEpicFromBoard(e))
	}
	return out, nil
}

func (d *DomainStore) SetEpicStatus(ctx context.Context, id string, status domain.Status) error {
	return d.Store.SetEpicStatus(ctx, id, boardStatusFromDomain(status))
}

func (d *DomainStore) DeleteEpic(ctx context.Context, id string) error {
	return d.Store.DeleteEpic(ctx, id)
}

func (d *DomainStore) CreateBugReport(ctx context.Context, bug *domain.BugReport) error {
	br := boardBugReportFromDomain(bug)
	return d.Store.CreateBugReport(ctx, br)
}

func (d *DomainStore) GetBugReport(ctx context.Context, id string) (*domain.BugReport, error) {
	r, err := d.Store.GetBugReport(ctx, id)
	if err != nil {
		return nil, err
	}
	return domainBugReportFromBoard(r), nil
}

func (d *DomainStore) SaveBugReport(ctx context.Context, bug *domain.BugReport) error {
	br := boardBugReportFromDomain(bug)
	return d.Store.SaveBugReport(ctx, br)
}

func (d *DomainStore) ListBugReports(ctx context.Context) ([]*domain.BugReport, error) {
	bugs, err := d.Store.ListBugReports(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.BugReport, 0, len(bugs))
	for _, b := range bugs {
		out = append(out, domainBugReportFromBoard(b))
	}
	return out, nil
}

func (d *DomainStore) SetBugStatus(ctx context.Context, id string, status domain.BugStatus) error {
	return d.Store.SetBugStatus(ctx, id, boardBugStatusFromDomain(status))
}

func (d *DomainStore) ReferenceExists(ctx context.Context, id string) error {
	return d.Store.ReferenceExists(ctx, id)
}

func (d *DomainStore) Project() string {
	return d.Store.Project()
}

func boardStatusFromDomain(s domain.Status) Status {
	return Status(s)
}

func boardBugStatusFromDomain(s domain.BugStatus) BugStatus {
	return BugStatus(s)
}

func boardTaskFromDomain(t *domain.Task) *Task { return t }

func domainTaskFromBoard(t *Task) *domain.Task { return t }

func boardEpicFromDomain(e *domain.Epic) *Epic { return e }

func domainEpicFromBoard(e *Epic) *domain.Epic { return e }

func boardBugReportFromDomain(b *domain.BugReport) *BugReport { return b }

func domainBugReportFromBoard(b *BugReport) *domain.BugReport { return b }

func boardCommentFromDomain(c domain.Comment) Comment { return c }

func domainCommentFromBoard(c Comment) domain.Comment { return c }

func boardCommentsFromDomain(cs domain.Comments) Comments { return cs }

func domainCommentsFromBoard(cs Comments) domain.Comments { return cs }

func boardInjectionsFromDomain(injs []domain.Injection) []Injection { return injs }

func domainInjectionsFromBoard(injs []Injection) []domain.Injection { return injs }

func boardInjectionFromDomain(inj domain.Injection) Injection { return inj }

func domainInjectionFromBoard(inj Injection) domain.Injection { return inj }

func boardCheckpointFromDomain(cp *domain.TaskCheckpoint) *TaskCheckpoint { return cp }

func domainCheckpointFromBoard(cp *TaskCheckpoint) *domain.TaskCheckpoint { return cp }

var _ services.BoardStore = (*DomainStore)(nil)
