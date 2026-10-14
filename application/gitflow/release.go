// Политика вливания ветки задачи в релизную ветку её эпика — общая механика
// REST-эндпоинта «Влить» и авто-хука done→merge (Ф-3, Ф-4).
//
// Слияние идёт через git.MergeFeature (прогноз конфликтов через merge-tree БЕЗ
// изменения рабочей копии, затем настоящий merge-коммит во временном worktree и
// push релизной ветки). Рабочая копия клона и HEAD агента не трогаются.
package gitflow

import (
	"context"
	"errors"
	"fmt"

	"ai/domain"
	"ai/infrastructure/git"
)

// Доменные ошибки релиза эпика в базу (маппятся адаптером в HTTP-коды).
var (
	ErrProjectNotFound  = errors.New("проект не найден")
	ErrNotGitProject    = errors.New("операция доступна только git-проектам")
	ErrNoBaseBranch     = errors.New("у проекта не задана базовая ветка (git_base)")
	ErrEpicNotFound     = errors.New("эпик не найден")
	ErrEpicNotDone      = errors.New("операция доступна только эпику со статусом done")
	ErrRepoUnavailable  = errors.New("репозиторий недоступен")
	ErrBoardUnavailable = errors.New("доска недоступна")
	ErrMergeFailed      = errors.New("ошибка слияния")
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

// ReleaseHost — окружение релиза эпика: доступ к git-проекту, репозиторию,
// статусу эпика на доске, side-реестру веток, пер-проектному лок-у,
// инвалидации диффов и хуку «main изменился» (RAG).
type ReleaseHost interface {
	Project(project string) (MergeProject, error)
	Repo(ctx context.Context, project string) (MergeRepo, error)
	// EpicStatus возвращает статус эпика; found=false — эпика нет,
	// err!=nil — инфраструктурный сбой (доска недоступна).
	EpicStatus(ctx context.Context, project, epicID string) (status domain.Status, found bool, err error)
	EpicBranch(project, epicID string) (BranchRef, error)
	Locked(project string, fn func())
	InvalidateDiffs(project string)
	MainChanged(project, main string, alreadyMerged bool)
}

// Releaser — политика релиза релизной ветки эпика в базовую (main).
type Releaser struct {
	Host ReleaseHost
}

// ReleaseEpic вливает релизную ветку эпика в базовую (main) — общая механика
// REST-эндпоинта «Залить в main» и моста-инструмента ассистента EpicRelease.
// Эпик должен быть в статусе done (ручной затвор человека); сама операция
// необратима. Конфликт слияния возвращается как *git.MergeConflictError.
func (r Releaser) ReleaseEpic(ctx context.Context, project, epicID string) (*git.MergeResult, string, string, error) {
	inf, err := r.Host.Project(project)
	if err != nil {
		return nil, "", "", ErrProjectNotFound
	}
	if !inf.IsGit {
		return nil, "", "", fmt.Errorf("%w (kind=%s)", ErrNotGitProject, inf.Kind)
	}
	main := inf.Base
	if main == "" {
		return nil, "", "", ErrNoBaseBranch
	}

	repo, err := r.Host.Repo(ctx, project)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrRepoUnavailable, err)
	}

	status, found, err := r.Host.EpicStatus(ctx, project, epicID)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrBoardUnavailable, err)
	}
	if !found {
		return nil, "", "", ErrEpicNotFound
	}
	if status != domain.StatusDone {
		return nil, "", "", fmt.Errorf("%w (сейчас %s)", ErrEpicNotDone, status)
	}

	epicRef, err := r.Host.EpicBranch(project, epicID)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrEpicBranchMissing, err)
	}

	var res *git.MergeResult
	var mergeErr error
	r.Host.Locked(project, func() {
		res, mergeErr = repo.MergeFeature(ctx, main, epicRef.Branch, git.MergeFeatureOptions{
			Message: fmt.Sprintf("эпик %s: релиз в main из %s", epicID, epicRef.Branch),
			PushURL: inf.PushURL,
		})
	})
	if mergeErr != nil {
		var ce *git.MergeConflictError
		if errors.As(mergeErr, &ce) {
			return nil, "", "", mergeErr
		}
		return nil, "", "", fmt.Errorf("%w: %v", ErrMergeFailed, mergeErr)
	}
	r.Host.InvalidateDiffs(project)
	// main изменился: переиндексировать его по ref и подтянуть рабочую копию
	// агентов. Только когда ветка действительно слита: при already=true main не
	// менялся, и лишние git-вызовы/индексация ничего не дают.
	r.Host.MainChanged(project, main, res.AlreadyMerged)
	return res, epicRef.Branch, main, nil
}
