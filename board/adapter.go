package board

import (
	"ai/domain"
	"ai/domain/services"
	"context"
	"time"
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

func boardTaskFromDomain(t *domain.Task) *Task {
	return &Task{
		TaskSpec: TaskSpec{
			TaskID:         t.TaskID,
			Title:          t.Title,
			Description:    t.Description,
			AssignedRole:   t.AssignedRole,
			SequenceOrder:  FlexInt(t.SequenceOrder),
			CanRunParallel: FlexBool(t.CanRunParallel),
			Dependencies:   t.Dependencies,
			Contracts:      t.Contracts,
		},
		TokenUsage: TokenUsage{
			TokensInput:   t.TokensInput,
			TokensOutput:  t.TokensOutput,
			TokensTotal:   t.TokensTotal,
			TokenEstimate: t.TokenEstimate,
		},
		ProjectName:        t.ProjectName,
		Repositories:       t.Repositories,
		EpicID:             t.EpicID,
		Status:             boardStatusFromDomain(t.Status),
		Assignee:           t.Assignee,
		CreatedAt:          formatTime(t.CreatedAt),
		UpdatedAt:          formatTime(t.UpdatedAt),
		GitBranch:          t.GitBranch,
		MergeConflictFiles: t.MergeConflictFiles,
		ResumeStatus:       boardStatusFromDomain(t.ResumeStatus),
		AgentState:         t.AgentState,
		ActiveAgent:        t.ActiveAgent,
		Checkpoint:         boardCheckpointFromDomain(t.Checkpoint),
		Attempts:           t.Attempts,
		LastError:          t.LastError,
		PauseReason:        t.PauseReason,
		HeartbeatAt:        formatTime(t.HeartbeatAt),
		ModelTier:          t.ModelTier,
		Escalations:        t.Escalations,
		Injections:         boardInjectionsFromDomain(t.Injections),
		Comments:           boardCommentsFromDomain(t.Comments),
	}
}

func domainTaskFromBoard(t *Task) *domain.Task {
	if t == nil {
		return nil
	}
	return &domain.Task{
		TaskSpec: domain.TaskSpec{
			TaskID:         t.TaskID,
			Title:          t.Title,
			Description:    t.Description,
			AssignedRole:   t.AssignedRole,
			SequenceOrder:  t.SequenceOrder.Int(),
			CanRunParallel: t.CanRunParallel.Bool(),
			Dependencies:   t.Dependencies,
			Contracts:      t.Contracts,
		},
		TokenUsage: domain.TokenUsage{
			TokensInput:   t.TokensInput,
			TokensOutput:  t.TokensOutput,
			TokensTotal:   t.TokensTotal,
			TokenEstimate: t.TokenEstimate,
		},
		ProjectName:        t.ProjectName,
		Repositories:       t.Repositories,
		EpicID:             t.EpicID,
		Status:             domain.Status(t.Status),
		Assignee:           t.Assignee,
		CreatedAt:          parseTime(t.CreatedAt),
		UpdatedAt:          parseTime(t.UpdatedAt),
		GitBranch:          t.GitBranch,
		MergeConflictFiles: t.MergeConflictFiles,
		ResumeStatus:       domain.Status(t.ResumeStatus),
		AgentState:         t.AgentState,
		ActiveAgent:        t.ActiveAgent,
		Checkpoint:         domainCheckpointFromBoard(t.Checkpoint),
		Attempts:           t.Attempts,
		LastError:          t.LastError,
		PauseReason:        t.PauseReason,
		HeartbeatAt:        parseTime(t.HeartbeatAt),
		ModelTier:          t.ModelTier,
		Escalations:        t.Escalations,
		Injections:         domainInjectionsFromBoard(t.Injections),
		Comments:           domainCommentsFromBoard(t.Comments),
	}
}

func boardEpicFromDomain(e *domain.Epic) *Epic {
	return &Epic{
		TaskSpec: TaskSpec{
			TaskID:         e.TaskID,
			Title:          e.Title,
			Description:    e.Description,
			AssignedRole:   e.AssignedRole,
			SequenceOrder:  FlexInt(e.SequenceOrder),
			CanRunParallel: FlexBool(e.CanRunParallel),
			Dependencies:   e.Dependencies,
			Contracts:      e.Contracts,
		},
		TokenUsage: TokenUsage{
			TokensInput:   e.TokensInput,
			TokensOutput:  e.TokensOutput,
			TokensTotal:   e.TokensTotal,
			TokenEstimate: e.TokenEstimate,
		},
		ProjectName:        e.ProjectName,
		Repositories:       e.Repositories,
		Tasks:              e.Tasks,
		Status:             boardStatusFromDomain(e.Status),
		CreatedAt:          formatTime(e.CreatedAt),
		UpdatedAt:          formatTime(e.UpdatedAt),
		GitBranch:          e.GitBranch,
		MergeConflictFiles: e.MergeConflictFiles,
		MergedIntoMain:     e.MergedIntoMain,
		Summary:            e.Summary,
		Priority:           e.Priority,
		Revision:           e.Revision,
		LeadSyncedRev:      e.LeadSyncedRev,
		RequiresReview:     e.RequiresReview,
	}
}

func domainEpicFromBoard(e *Epic) *domain.Epic {
	if e == nil {
		return nil
	}
	return &domain.Epic{
		TaskSpec: domain.TaskSpec{
			TaskID:         e.TaskID,
			Title:          e.Title,
			Description:    e.Description,
			AssignedRole:   e.AssignedRole,
			SequenceOrder:  e.SequenceOrder.Int(),
			CanRunParallel: e.CanRunParallel.Bool(),
			Dependencies:   e.Dependencies,
			Contracts:      e.Contracts,
		},
		TokenUsage: domain.TokenUsage{
			TokensInput:   e.TokensInput,
			TokensOutput:  e.TokensOutput,
			TokensTotal:   e.TokensTotal,
			TokenEstimate: e.TokenEstimate,
		},
		ProjectName:        e.ProjectName,
		Repositories:       e.Repositories,
		Tasks:              e.Tasks,
		Status:             domain.Status(e.Status),
		CreatedAt:          parseTime(e.CreatedAt),
		UpdatedAt:          parseTime(e.UpdatedAt),
		GitBranch:          e.GitBranch,
		MergeConflictFiles: e.MergeConflictFiles,
		MergedIntoMain:     e.MergedIntoMain,
		Summary:            e.Summary,
		Priority:           e.Priority,
		Revision:           e.Revision,
		LeadSyncedRev:      e.LeadSyncedRev,
		RequiresReview:     e.RequiresReview,
	}
}

func boardBugReportFromDomain(b *domain.BugReport) *BugReport {
	return &BugReport{
		BugID:        b.BugID,
		ProjectName:  b.ProjectName,
		Title:        b.Title,
		Description:  b.Description,
		ReporterRole: b.ReporterRole,
		TaskID:       b.TaskID,
		EpicID:       b.EpicID,
		Status:       boardBugStatusFromDomain(b.Status),
		Verdict:      b.Verdict,
		FixEpicID:    b.FixEpicID,
		CreatedAt:    b.CreatedAt,
		UpdatedAt:    b.UpdatedAt,
	}
}

func domainBugReportFromBoard(b *BugReport) *domain.BugReport {
	if b == nil {
		return nil
	}
	return &domain.BugReport{
		BugID:        b.BugID,
		ProjectName:  b.ProjectName,
		Title:        b.Title,
		Description:  b.Description,
		ReporterRole: b.ReporterRole,
		TaskID:       b.TaskID,
		EpicID:       b.EpicID,
		Status:       domain.BugStatus(b.Status),
		Verdict:      b.Verdict,
		FixEpicID:    b.FixEpicID,
		CreatedAt:    b.CreatedAt,
		UpdatedAt:    b.UpdatedAt,
	}
}

func boardCommentFromDomain(c domain.Comment) Comment {
	return Comment{
		ID:        c.ID,
		TaskID:    c.TaskID,
		Author:    c.Author,
		Type:      c.Type,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
		Accounted: c.Accounted,
	}
}

func domainCommentFromBoard(c Comment) domain.Comment {
	return domain.Comment{
		ID:        c.ID,
		TaskID:    c.TaskID,
		Author:    c.Author,
		Type:      c.Type,
		Body:      c.Body,
		CreatedAt: c.CreatedAt,
		Accounted: c.Accounted,
	}
}

func boardCommentsFromDomain(cs domain.Comments) Comments {
	out := make(Comments, 0, len(cs))
	for _, c := range cs {
		out = append(out, boardCommentFromDomain(c))
	}
	return out
}

func domainCommentsFromBoard(cs Comments) domain.Comments {
	out := make(domain.Comments, 0, len(cs))
	for _, c := range cs {
		out = append(out, domainCommentFromBoard(c))
	}
	return out
}

func boardInjectionsFromDomain(injs []domain.Injection) []Injection {
	out := make([]Injection, 0, len(injs))
	for _, inj := range injs {
		out = append(out, boardInjectionFromDomain(inj))
	}
	return out
}

func domainInjectionsFromBoard(injs []Injection) []domain.Injection {
	out := make([]domain.Injection, 0, len(injs))
	for _, inj := range injs {
		out = append(out, domainInjectionFromBoard(inj))
	}
	return out
}

func boardInjectionFromDomain(inj domain.Injection) Injection {
	return Injection{
		ID:       inj.ID,
		Name:     inj.Name,
		Scope:    inj.Scope,
		Target:   inj.Target,
		Position: inj.Position,
		Index:    inj.Index,
		Content:  inj.Content,
		When:     inj.When,
		Enabled:  inj.Enabled,
		Priority: inj.Priority,
		Vars:     inj.Vars,
		Tags:     inj.Tags,
	}
}

func domainInjectionFromBoard(inj Injection) domain.Injection {
	return domain.Injection{
		ID:       inj.ID,
		Name:     inj.Name,
		Scope:    inj.Scope,
		Target:   inj.Target,
		Position: inj.Position,
		Index:    inj.Index,
		Content:  inj.Content,
		When:     inj.When,
		Enabled:  inj.Enabled,
		Priority: inj.Priority,
		Vars:     inj.Vars,
		Tags:     inj.Tags,
	}
}

func boardCheckpointFromDomain(cp *domain.TaskCheckpoint) *TaskCheckpoint {
	if cp == nil {
		return nil
	}
	return &TaskCheckpoint{
		BaseSHA:     cp.BaseSHA,
		LastSHA:     cp.LastSHA,
		LastGoodSHA: cp.LastGoodSHA,
	}
}

func domainCheckpointFromBoard(cp *TaskCheckpoint) *domain.TaskCheckpoint {
	if cp == nil {
		return nil
	}
	return &domain.TaskCheckpoint{
		BaseSHA:     cp.BaseSHA,
		LastSHA:     cp.LastSHA,
		LastGoodSHA: cp.LastGoodSHA,
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

var _ services.BoardStore = (*DomainStore)(nil)
