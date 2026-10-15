// Авто-резолв конфликтов «main ↔ релизная ветка эпика» (Ф-4): жизненный цикл
// конфликтного worktree, вынесенный из HTTP-слоя (Этап 6, срез F3j).
//
// StartRebase разворачивает настоящий merge main → релизной ветки в постоянном
// worktree (.conflict-<проект>-<эпик>), авто-разрешает тривиальные блоки и
// сохраняет «сложные» файлы модели (инструмент ResolveGitConflicts).
// FinishResolve доводит процесс до конца: повторный авто-резолв, обязательная
// приёмка (acceptor), коммит резолва, финальный merge main ← релизной ветки и
// push. Побочные эффекты подключаются через порт ConflictHost.
package gitflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ai/domain"
	"ai/logging"
)

// Доменные ошибки ошибок резолва (маппятся адаптером в ответы).
var (
	// ErrResolveBusy — другой эпик проекта уже в процессе резолва (409 busy).
	ErrResolveBusy = errors.New("идёт резолв другого эпика")
	// ErrNoResolve — активного процесса резолва нет (409 no_resolve).
	ErrNoResolve = errors.New("активного процесса резолва нет")
	// ErrResolveStateSave — запись процесса резолва в реестр не удалась (500).
	ErrResolveStateSave = errors.New("запись процесса резолва")
)

// ConflictRepo — git-операции конфликтного резолва.
type ConflictRepo interface {
	MergeFeature(ctx context.Context, base, source string, opts domain.MergeFeatureOptions) (*domain.MergeResult, error)
	MergeBase(ctx context.Context, a, b string) (string, error)
	MergeTree(ctx context.Context, base, ours, theirs string) ([]string, error)
	Root() string
	AddWorktree(ctx context.Context, worktreePath, branch string) (ConflictWorktree, error)
	RemoveWorktree(ctx context.Context, worktreePath string) error
}

// ConflictWorktree — операции в конфликтном worktree.
type ConflictWorktree interface {
	MergeBranch(ctx context.Context, branch, message string) (*domain.MergeResult, error)
	UnmergedFiles(ctx context.Context) ([]string, error)
	Stage(ctx context.Context, paths []string) error
	Dirty(ctx context.Context) (bool, error)
	Commit(ctx context.Context, message string) error
	CommitAllowEmpty(ctx context.Context, message string) error
}

// ResolveState — состояние активного процесса резолва (side-реестр).
type ResolveState struct {
	EpicID    string
	Branch    string
	Worktree  string
	Files     []string
	StartedAt time.Time
}

// ConflictHost — окружение конфликтного резолва: git-проект/репозиторий/статус,
// side-реестр процесса резолва, конфликтный worktree, пер-проектный лок,
// авто-резолв тривиальных блоков, маркеры, приёмка и запись конфликта на доску.
type ConflictHost interface {
	Project(project string) (MergeProject, error)
	Repo(ctx context.Context, project string) (ConflictRepo, error)
	EpicStatus(ctx context.Context, project, epicID string) (domain.Status, bool, error)
	EpicBranch(project, epicID string) (BranchRef, error)
	ResolveState(project string) (*ResolveState, bool)
	SetResolveState(project string, st *ResolveState) error
	ClearResolveState(project string) error
	Worktree(project, worktree, branch string) (ConflictWorktree, error)
	Locked(project string, fn func())
	DropEpicWorktreeLocked(ctx context.Context, project, epicID string)
	TrivialResolve(dir string, files []string) (resolved, hard []string, err error)
	Markers(dir string, files []string) ([]string, error)
	Accept(dir string) *domain.Report
	SetEpicMergeConflict(ctx context.Context, project, epicID string, files []string)
}

// RebaseOutcome — результат старта резолва.
type RebaseOutcome struct {
	Status   string   // "resolving" | "ok" | "busy"
	EpicID   string   // busy
	Repeat   bool     // идемпотентное продолжение уже открытого процесса
	All      []string // все конфликтные файлы по прогнозу merge-tree
	Files    []string // «сложные» файлы, ждущие правок модели
	Resolved []string
	Branch   string
	Worktree string
	Message  string
}

// ResolveOutcome — результат завершения резолва.
type ResolveOutcome struct {
	Status        string // "ok" | "still_conflicts" | "acceptance_failed" | "no_resolve" | "busy" | "conflicts"
	EpicID        string // busy
	Files         []string
	Branch        string // main
	Source        string // релизная ветка
	Message       string
	AlreadyMerged bool
	Verdict       domain.Verdict
	Summary       string
	Issues        []domain.Issue
}

// ConflictResolver — политика конфликтного резолва эпика.
type ConflictResolver struct {
	Host ConflictHost
}

// StartRebase — вход Ф-4: разворачивает merge main в релизную ветку эпика в
// постоянном конфликтном worktree для авто/модельного резолва. Идемпотентно.
func (r ConflictResolver) StartRebase(ctx context.Context, project, epicID string) (*RebaseOutcome, error) {
	inf, err := r.Host.Project(project)
	if err != nil {
		return nil, ErrProjectNotFound
	}
	if !inf.IsGit {
		return nil, fmt.Errorf("%w (kind=%s)", ErrNotGitProject, inf.Kind)
	}
	main := inf.Base
	if main == "" {
		return nil, ErrNoBaseBranch
	}
	repo, err := r.Host.Repo(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRepoUnavailable, err)
	}
	status, found, err := r.Host.EpicStatus(ctx, project, epicID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBoardUnavailable, err)
	}
	if !found {
		return nil, ErrEpicNotFound
	}
	if status != domain.StatusDone {
		return nil, fmt.Errorf("%w (сейчас %s)", ErrEpicNotDone, status)
	}
	epicRef, err := r.Host.EpicBranch(project, epicID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEpicBranchMissing, err)
	}

	// Резолв процесса — идемпотентное продолжение работы над готовым состоянием.
	if rs, ok := r.Host.ResolveState(project); ok {
		if rs.EpicID != epicID {
			return &RebaseOutcome{Status: "busy", EpicID: rs.EpicID}, nil
		}
		logging.For(project).Infof("gitflow: rebase %s повторный — продолжаем резолв (%d файлов)", epicID, len(rs.Files))
		return &RebaseOutcome{
			Status:   "resolving",
			Repeat:   true,
			Files:    rs.Files,
			Branch:   rs.Branch,
			Worktree: rs.Worktree,
		}, nil
	}

	var out *RebaseOutcome
	var opErr error
	r.Host.Locked(project, func() {
		out, opErr = r.startRebaseLocked(ctx, project, epicID, main, repo, epicRef)
	})
	if opErr != nil {
		return nil, opErr
	}
	return out, nil
}

// startRebaseLocked — тело резолва под пер-проектным локом.
func (r ConflictResolver) startRebaseLocked(ctx context.Context, project, epicID, main string, repo ConflictRepo, epicRef BranchRef) (*RebaseOutcome, error) {
	// Прогноз конфликтов merge-tree БЕЗ изменения рабочей копии: если чисто —
	// резолв не нужен (идите POST .../release).
	base, err := repo.MergeBase(ctx, main, epicRef.Branch)
	if err != nil {
		return nil, fmt.Errorf("%w: merge-base: %v", ErrMergeFailed, err)
	}
	forecast, err := repo.MergeTree(ctx, base, main, epicRef.Branch)
	if err != nil {
		return nil, fmt.Errorf("%w: merge-tree: %v", ErrMergeFailed, err)
	}
	if len(forecast) == 0 {
		logging.For(project).Infof("gitflow: rebase %s: конфликтов нет, идите POST /release", epicID)
		return &RebaseOutcome{
			Status:  "ok",
			Message: "конфликтов не найдено — используйте POST /api/projects/" + project + "/epics/" + epicID + "/release",
			Files:   []string{},
		}, nil
	}

	wtPath := filepath.Join(filepath.Dir(repo.Root()), ".conflict-"+project+"-"+domain.SanitizeBranchName(epicID))

	// 4.1: worktree ветки эпика (архитектор) держит релизную ветку checked-out —
	// конфликтный worktree не смог бы её получить.
	r.Host.DropEpicWorktreeLocked(ctx, project, epicID)

	// Чистим остатки прерванного процесса (каталог существует, но реестр пуст).
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: удаляю осиротевший конфликтный worktree %s", wtPath)
		_ = repo.RemoveWorktree(ctx, wtPath)
	}

	wt, err := repo.AddWorktree(ctx, wtPath, epicRef.Branch)
	if err != nil {
		return nil, fmt.Errorf("%w: создание конфликтного worktree: %v", ErrMergeFailed, err)
	}
	cleanup := func() {
		_ = repo.RemoveWorktree(ctx, wtPath)
		_ = r.Host.ClearResolveState(project)
	}

	// Настоящий merge main → релизная ветка прямо в worktree. Ошибка НЕ фатальна:
	// конфликты оставляют маркеры в файлах и stage-записи в индексе worktree.
	_, _ = wt.MergeBranch(ctx, main,
		fmt.Sprintf("эпик %s: влитие main в релизную ветку (резолв конфликтов)", epicID))

	unmerged, err := wt.UnmergedFiles(ctx)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: git ls-files -u: %v", ErrMergeFailed, err)
	}
	resolved, hard, err := r.Host.TrivialResolve(wtPath, unmerged)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: авто-резолв тривиальных конфликтов: %v", ErrMergeFailed, err)
	}
	if err := wt.Stage(ctx, resolved); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: git add резолвов: %v", ErrMergeFailed, err)
	}

	if err := r.Host.SetResolveState(project, &ResolveState{
		EpicID:    epicID,
		Branch:    epicRef.Branch,
		Worktree:  wtPath,
		Files:     hard,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: %v", ErrResolveStateSave, err)
	}

	logging.For(project).Infof("gitflow: rebase %s: конфликты в %s, авто-резолв %d, на модель %d (%s)",
		epicID, strings.Join(forecast, ", "), len(resolved), len(hard), wtPath)
	// Конфликт main ↔ релизная ветка виден на доске.
	r.Host.SetEpicMergeConflict(ctx, project, epicID, forecast)

	if hard == nil {
		hard = []string{}
	}
	if resolved == nil {
		resolved = []string{}
	}
	return &RebaseOutcome{
		Status:   "resolving",
		All:      forecast,
		Files:    hard,
		Resolved: resolved,
		Branch:   epicRef.Branch,
		Worktree: wtPath,
		Message:  "правьте сложные файлы инструментом ResolveGitConflicts и завершите POST /api/projects/" + project + "/epics/" + epicID + "/resolve",
	}, nil
}

// FinishResolve — финализация резолва Ф-4: обязательная приёмка (acceptor) в
// конфликтном worktree, коммит резолва, финальный merge main ← релизной ветки и
// push. Значимо атомарно через пер-проектный лок: реестр очищается только после
// успешного merge.
func (r ConflictResolver) FinishResolve(ctx context.Context, project, epicID string) (*ResolveOutcome, error) {
	inf, err := r.Host.Project(project)
	if err != nil {
		return nil, ErrProjectNotFound
	}
	if !inf.IsGit {
		return nil, fmt.Errorf("%w (kind=%s)", ErrNotGitProject, inf.Kind)
	}
	main := inf.Base
	if main == "" {
		return nil, ErrNoBaseBranch
	}
	repo, err := r.Host.Repo(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRepoUnavailable, err)
	}

	rs, ok := r.Host.ResolveState(project)
	if !ok {
		return &ResolveOutcome{Status: "no_resolve"}, nil
	}
	if rs.EpicID != epicID {
		return &ResolveOutcome{Status: "busy", EpicID: rs.EpicID}, nil
	}
	if _, err := os.Stat(rs.Worktree); err != nil {
		return &ResolveOutcome{
			Status:  "no_resolve",
			Message: "конфликтный worktree " + rs.Worktree + " не существует — начните заново POST /api/projects/" + project + "/epics/" + epicID + "/rebase",
		}, nil
	}

	var out *ResolveOutcome
	var opErr error
	r.Host.Locked(project, func() {
		out, opErr = r.finishResolveLocked(ctx, project, epicID, main, inf.PushURL, repo, rs)
	})
	if opErr != nil {
		return nil, opErr
	}
	return out, nil
}

// finishResolveLocked — тело финализации под пер-проектным локом.
func (r ConflictResolver) finishResolveLocked(ctx context.Context, project, epicID, main, pushURL string, repo ConflictRepo, rs *ResolveState) (*ResolveOutcome, error) {
	wt, err := r.Host.Worktree(project, rs.Worktree, rs.Branch)
	if err != nil {
		return nil, fmt.Errorf("%w: конфликтный worktree: %v", ErrMergeFailed, err)
	}

	// 1) Закрываем всё, что модель успела почистить/не трогала: повторный
	// авто-резолв + stage всех разрешённых + проверка «остались маркеры».
	resolved, _, err := r.Host.TrivialResolve(rs.Worktree, rs.Files)
	if err != nil {
		return nil, fmt.Errorf("%w: авто-резолв: %v", ErrMergeFailed, err)
	}
	if err := wt.Stage(ctx, resolved); err != nil {
		return nil, fmt.Errorf("%w: git add резолвов: %v", ErrMergeFailed, err)
	}
	unmerged, err := wt.UnmergedFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: git ls-files -u: %v", ErrMergeFailed, err)
	}
	markers, err := r.Host.Markers(rs.Worktree, rs.Files)
	if err != nil {
		return nil, fmt.Errorf("%w: проверка маркеров: %v", ErrMergeFailed, err)
	}
	if remain := unionPaths(unmerged, markers); len(remain) > 0 {
		logging.For(project).Infof("gitflow: resolve %s: ещё конфликты в %s — резолв не завершён",
			epicID, strings.Join(remain, ", "))
		return &ResolveOutcome{Status: "still_conflicts", Files: remain}, nil
	}

	// 2) Обязательная приёмка собранного worktree (безопасность релиза).
	report := r.Host.Accept(rs.Worktree)
	if report.Verdict != domain.VerdictApprove {
		logging.For(project).Warnf("gitflow: resolve %s: приёмка не прошла — %s", epicID, report.Summary)
		return &ResolveOutcome{
			Status:  "acceptance_failed",
			Verdict: report.Verdict,
			Summary: report.Summary,
			Issues:  report.Issues,
		}, nil
	}

	// 3) Коммит резолва в worktree (завершает merge main → релизная ветка).
	// CommitAllowEmpty, когда дерево вернулось на HEAD-содержимое.
	dirty, err := wt.Dirty(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: git status: %v", ErrMergeFailed, err)
	}
	msg := fmt.Sprintf("эпик %s: резолв конфликтов main ↔ %s", epicID, rs.Branch)
	if dirty {
		if err := wt.Commit(ctx, msg); err != nil {
			return nil, fmt.Errorf("%w: git commit резолва: %v", ErrMergeFailed, err)
		}
	} else if err := wt.CommitAllowEmpty(ctx, msg); err != nil {
		return nil, fmt.Errorf("%w: git commit --allow-empty резолва: %v", ErrMergeFailed, err)
	}

	// 4) Финальный merge main ← релизной ветки (после резолва merge-tree чист).
	res, mergeErr := repo.MergeFeature(ctx, main, rs.Branch, domain.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: релиз в main после резолва конфликтов", epicID),
		PushURL: pushURL,
	})
	if mergeErr != nil {
		var ce *domain.MergeConflictError
		if errors.As(mergeErr, &ce) {
			return &ResolveOutcome{
				Status:  "conflicts",
				Files:   ce.Files,
				Message: "неожиданные конфликты при финальном merge: " + ce.Error(),
			}, nil
		}
		return nil, fmt.Errorf("%w: %v", ErrMergeFailed, mergeErr)
	}

	// 5) Cleanup: снять worktree, закрыть процесс.
	if err := repo.RemoveWorktree(ctx, rs.Worktree); err != nil {
		logging.For(project).Warnf("gitflow: снятие конфликтного worktree %s: %v", rs.Worktree, err)
	}
	if err := r.Host.ClearResolveState(project); err != nil {
		logging.For(project).Warnf("gitflow: снятие процесса резолва %s: %v", epicID, err)
	}

	logging.For(project).Infof("gitflow: resolve %s: релиз в main выполнен (%s, already=%v)",
		epicID, res.Message, res.AlreadyMerged)
	return &ResolveOutcome{
		Status:        "ok",
		Branch:        main,
		Source:        rs.Branch,
		Message:       res.Message,
		AlreadyMerged: res.AlreadyMerged,
		Verdict:       report.Verdict,
		Summary:       report.Summary,
	}, nil
}

// unionPaths склеивает пути без дубликатов.
func unionPaths(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var out []string
	for _, p := range append(a, b...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
