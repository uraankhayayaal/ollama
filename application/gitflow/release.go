// Политика вливания ветки задачи в релизную ветку её эпика — общая механика
// REST-эндпоинта «Влить» и авто-хука done→merge (Ф-3, Ф-4).
//
// Слияние идёт через git.MergeFeature (прогноз конфликтов через merge-tree БЕЗ
// изменения рабочей копии, затем настоящий merge-коммит во временном worktree и
// push релизной ветки). Рабочая копия клона и HEAD агента не трогаются.
package gitflow

import (
	"context"
	"fmt"

	"ai/domain"
	"ai/infrastructure/git"
)

// MergeProject — срез git-проекта, нужный слиянию: git ли, его kind (для
// диагностики) и URL push релизной ветки (токенизированный либо пустой).
type MergeProject struct {
	IsGit   bool
	Kind    string
	Base    string
	PushURL string
}

// MergeRepo — git-операции, нужные слиянию feature-ветки.
type MergeRepo interface {
	MergeFeature(ctx context.Context, base, source string, opts git.MergeFeatureOptions) (*git.MergeResult, error)
}

// MergeTaskHost — окружение политики слияния веток задачи: доступ к git-проекту,
// репозиторию, side-реестру веток, пер-проектному лок-у и RAG-инвалидации.
type MergeTaskHost interface {
	Project(project string) (MergeProject, error)
	Repo(ctx context.Context, project string) (MergeRepo, error)
	EpicBranch(project, epicID string) (BranchRef, error)
	TaskBranch(project, taskID string) (BranchRef, error)
	Locked(project string, fn func())
	DropEpicWorktreeLocked(ctx context.Context, project, epicID string)
	InvalidateDiffs(project string)
}

// TaskMerger — политика вливания ветки задачи в релизную ветку эпика.
type TaskMerger struct {
	Host MergeTaskHost
}

// MergeTaskBranch вливает ветку задачи в ветку её эпика. Возвращает результат
// слияния или *git.MergeConflictError при конфликтах. Потокобезопасно: слияния
// проекта сериализуются пер-проектным локом.
func (m TaskMerger) MergeTaskBranch(ctx context.Context, project string, t *domain.Task) (*git.MergeResult, error) {
	if t == nil || t.EpicID == "" {
		return nil, fmt.Errorf("задача без эпика")
	}
	inf, err := m.Host.Project(project)
	if err != nil {
		return nil, fmt.Errorf("проект не найден: %v", err)
	}
	if !inf.IsGit {
		return nil, fmt.Errorf("мёрдж веток доступен только git-проектам (kind=%s)", inf.Kind)
	}
	repo, err := m.Host.Repo(ctx, project)
	if err != nil {
		return nil, err
	}
	epicRef, err := m.Host.EpicBranch(project, t.EpicID)
	if err != nil {
		return nil, fmt.Errorf("сначала создайте ветку эпика %s: %v", t.EpicID, err)
	}
	taskRef, err := m.Host.TaskBranch(project, t.TaskID)
	if err != nil {
		return nil, fmt.Errorf("сначала создайте ветку задачи %s: %v", t.TaskID, err)
	}

	var res *git.MergeResult
	var mergeErr error
	m.Host.Locked(project, func() {
		// 4.1: worktree ветки эпика (архитектор) держит релизную ветку
		// checked-out — MergeFeature не смог бы поставить её во временный worktree.
		m.Host.DropEpicWorktreeLocked(ctx, project, t.EpicID)
		res, mergeErr = repo.MergeFeature(ctx, epicRef.Branch, taskRef.Branch, git.MergeFeatureOptions{
			Message: fmt.Sprintf("задача %s: влитие в релиз эпика %s", t.TaskID, t.EpicID),
			PushURL: inf.PushURL,
		})
	})
	if mergeErr == nil {
		m.Host.InvalidateDiffs(project)
	}
	return res, mergeErr
}
