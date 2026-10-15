package gitflow

// Авто-синхрон релизной ветки эпика с main (Ф-4), вынесенный из HTTP-слоя
// (`server/gitflow_auto.go`, Этап 6 «Интерфейсы и адаптеры»).
//
// Два входа:
//   - SyncEpicMain — хук эпика в done: под mergeLock, с авто-резолвом;
//   - SyncMainForTask — перед стартом задачи (вызывающий уже держит mergeLock).
//
// Без конфликтов main вливается в релизную ветку быстрым путь (MergeFeature).
// С конфликтами — настоящий merge main во временном worktree релизной ветки,
// авто-резолв тривиальных блоков, закрытие merge-коммита и push. Сложные
// конфликты НЕ продвигают ветку: работает интерактивный флоу
// handleEpicRebase/handleEpicResolve. Все побочные эффекты (git, доска,
// сессия, шина) подключаются портом EpicMainHost.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai/domain"
	"ai/logging"
)

// EpicRepo — операции с репозиторием проекта и его worktree.
type EpicRepo interface {
	Root() string
	MergeFeature(ctx context.Context, release, feature string, opts domain.MergeFeatureOptions) (*domain.MergeResult, error)
	AddWorktree(ctx context.Context, worktreePath, branch string) (WorktreeOps, error)
	RemoveWorktree(ctx context.Context, worktreePath string) error
}

// WorktreeOps — операции с временным worktree релизной ветки эпика.
type WorktreeOps interface {
	MergeBranch(ctx context.Context, branch, message string) (*domain.MergeResult, error)
	UnmergedFiles(ctx context.Context) ([]string, error)
	Stage(ctx context.Context, paths []string) error
	CommitAllowEmpty(ctx context.Context, message string) error
	PushTo(ctx context.Context, remoteURL string) error
}

// EpicMainHost — операции хоста (реализуется сервером: git, реестр веток,
// доска, сессия, шина доски), нужные авто-синхрону релизной ветки эпика с main.
type EpicMainHost interface {
	// ProjectInfo — GitBase и push-URL проекта; ok=false — не git/не найден.
	ProjectInfo() (base, pushURL string, ok bool)
	// EpicBranch — релизная ветка эпика; ok=false — ветки нет.
	EpicBranch(epicID string) (BranchRef, bool)
	// Repo — репозиторий проекта; ok=false — клон недоступен.
	Repo(ctx context.Context) (EpicRepo, bool)
	// DropEpicWorktreeLocked снимает worktree архитектора; требует
	// удерживаемого mergeLock проекта.
	DropEpicWorktreeLocked(ctx context.Context, epicID string)
	// Locked выполняет fn под mergeLock проекта.
	Locked(fn func())
	// AttemptLLMResolve — авторезолв конфликта через LLM (контекст эпика).
	AttemptLLMResolve(ctx context.Context, epic *domain.Epic, worktreePath string, files []string, role string) bool
	// TrivialResolve — авто-резолв тривиальных блоков в worktree; возвращает
	// разрешённые и «сложные» файлы.
	TrivialResolve(wtPath string, unmerged []string) (resolved, hard []string, err error)
	// SetEpicMergeConflict ставит (files != nil) или снимает (files == nil)
	// признак конфликта на доске.
	SetEpicMergeConflict(ctx context.Context, epicID string, files []string)
	// Notify — сообщение на доску.
	Notify(msg string)
	// Status — статусное сообщение в чат проекта.
	Status(msg string)
}

// EpicMainSyncer — политика синхронизации релизной ветки эпика с main.
type EpicMainSyncer struct {
	Host EpicMainHost
}

// SyncEpicMain — авто-синхрон релизной ветки эпика с main (вызывается хуком
// эпика в done; под mergeLock проекта).
func (s *EpicMainSyncer) SyncEpicMain(ctx context.Context, project string, epic *domain.Epic) {
	s.Host.Locked(func() { s.syncOnceLocked(ctx, project, epic) })
}

// SyncMainForTask — синхрон релизной ветки эпика с main перед стартом задачи (без
// требования done). Вызывается под mergeLock (из taskWorktree): Locked НЕ берём.
func (s *EpicMainSyncer) SyncMainForTask(ctx context.Context, project, epicID string) {
	s.syncForTaskLocked(ctx, project, epicID)
}

func (s *EpicMainSyncer) syncOnceLocked(ctx context.Context, project string, epic *domain.Epic) {
	if epic == nil || epic.TaskID == "" {
		return
	}
	base, pushURL, ok := s.Host.ProjectInfo()
	if !ok || strings.TrimSpace(base) == "" {
		return
	}
	epicRef, ok := s.Host.EpicBranch(epic.TaskID)
	if !ok {
		return
	}
	repo, ok := s.Host.Repo(ctx)
	if !ok {
		return
	}

	// 4.1: worktree ветки эпика (архитектор) держит релизную ветку
	// checked-out — MergeFeature не смог бы поставить её во временный worktree.
	s.Host.DropEpicWorktreeLocked(ctx, epic.TaskID)

	res, err := repo.MergeFeature(ctx, epicRef.Branch, base, domain.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: синхрон релизной ветки %s с main", epic.TaskID, epicRef.Branch),
		PushURL: pushURL,
	})
	if err == nil {
		logging.For(project).Infof("gitflow: эпик %s: авто-синхрон с main (already=%v)", epic.TaskID, res.AlreadyMerged)
		s.Host.SetEpicMergeConflict(ctx, epic.TaskID, nil)
		s.Host.Notify("gitflow: авто-синхрон эпика с main")
		return
	}
	var ce *domain.MergeConflictError
	if !errors.As(err, &ce) {
		if errors.Is(err, context.Canceled) {
			return
		}
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: %v", epic.TaskID, err)
		return
	}
	s.autoResolve(ctx, project, epic, base, pushURL, repo, epicRef)
}

func (s *EpicMainSyncer) syncForTaskLocked(ctx context.Context, project, epicID string) {
	base, pushURL, ok := s.Host.ProjectInfo()
	if !ok || strings.TrimSpace(base) == "" {
		return
	}
	epicRef, ok := s.Host.EpicBranch(epicID)
	if !ok {
		return
	}
	repo, ok := s.Host.Repo(ctx)
	if !ok {
		return
	}

	// 4.1: worktree ветки эпика (архитектор) мешает MergeFeature поставить
	// релизную ветку во временный worktree — снимаем (автокоммит правок).
	s.Host.DropEpicWorktreeLocked(ctx, epicID)
	res, err := repo.MergeFeature(ctx, epicRef.Branch, base, domain.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: синхрон с main перед стартом задачи", epicID),
		PushURL: pushURL,
	})
	if err == nil {
		logging.For(project).Infof("gitflow: эпик %s: синхрон с main перед задачей (already=%v)", epicID, res.AlreadyMerged)
		s.Host.SetEpicMergeConflict(ctx, epicID, nil)
		return
	}

	var ce *domain.MergeConflictError
	if !errors.As(err, &ce) {
		logging.For(project).Warnf("gitflow: синхрон эпика %s с main перед задачей: %v", epicID, err)
		return
	}

	// Конфликт: пробуем авто-резолв тривиальных блоков.
	s.autoResolve(ctx, project, &domain.Epic{TaskSpec: domain.TaskSpec{TaskID: epicID}}, base, pushURL, repo, epicRef)
}

// autoResolve — продолжение авто-синхрона при конфликтах: настоящий merge main в
// постоянном worktree релизной ветки, авто-резолв тривиальных блоков, закрытие
// merge-коммита и push (всё на релизной ветке эпика). Сложные конфликты — отказ
// от продвижения: ветка остаётся нетронутой, работает интерактивный флоу
// handleEpicRebase/handleEpicResolve.
func (s *EpicMainSyncer) autoResolve(ctx context.Context, project string, epic *domain.Epic, base, pushURL string, repo EpicRepo, epicRef BranchRef) {
	wtPath := filepath.Join(filepath.Dir(repo.Root()),
		".conflict-"+project+"-"+domain.SanitizeBranchName(epic.TaskID))
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: удаляю осиротевший конфликтный worktree %s", wtPath)
		_ = repo.RemoveWorktree(ctx, wtPath)
	}
	// 4.1: релизную ветку эпика перед checkout освобождаем от worktree
	// архитектора (см. dropEpicWorktreeLocked).
	s.Host.DropEpicWorktreeLocked(ctx, epic.TaskID)
	wt, err := repo.AddWorktree(ctx, wtPath, epicRef.Branch)
	if err != nil {
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: worktree: %v", epic.TaskID, err)
		return
	}
	removeWT := func() { _ = repo.RemoveWorktree(ctx, wtPath) }

	_, _ = wt.MergeBranch(ctx, base,
		fmt.Sprintf("эпик %s: влитие main (авто-резолв)", epic.TaskID))

	unmerged, err := wt.UnmergedFiles(ctx)
	if err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: ls-files -u: %v", epic.TaskID, err)
		return
	}
	resolved, hard, err := s.Host.TrivialResolve(wtPath, unmerged)
	if err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: авто-резолв: %v", epic.TaskID, err)
		return
	}
	if len(hard) > 0 {
		// Попытка авторезолвина через LLM: контекст эпика + задачи + файлы.
		if s.Host.AttemptLLMResolve(ctx, epic, wtPath, hard, epic.AssignedRole) {
			removeWT()
			s.Host.SetEpicMergeConflict(ctx, epic.TaskID, nil)
			logging.For(project).Infof("gitflow: авто-синхрон эпика %s: сложные конфликты разрешены LLM", epic.TaskID)
			s.Host.Notify("gitflow: авто-синхрон эпика: конфликты разрешены LLM")
			return
		}
		// LLM не справился — релизную ветку не трогаем, доступен ручной/модельный rebase + резолв + release (Ф-4).
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: сложные конфликты [%s] — флоу rebase/резолв",
			epic.TaskID, strings.Join(hard, ", "))
		// Помечаем эпик на доске: конфликт main ↔ релизная ветка виден в UI и
		// ассистенту (флоу rebase/resolve остаётся точкой входа для резолва).
		s.Host.SetEpicMergeConflict(ctx, epic.TaskID, hard)
		s.Host.Status(fmt.Sprintf("Эпик %s: авто-синхрон релизной ветки с main упёрся в конфликт в файлах [%s]. Доступен флоу rebase/резолв (ResolveGitConflicts).",
			epic.TaskID, strings.Join(hard, ", ")))
		s.Host.Notify("gitflow: авто-синхрон эпика: сложные конфликты")
		return
	}
	if err := wt.Stage(ctx, resolved); err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: git add резолвов: %v", epic.TaskID, err)
		return
	}
	if err := wt.CommitAllowEmpty(ctx,
		fmt.Sprintf("эпик %s: синхрон с main (авто-резолв %d файлов)", epic.TaskID, len(resolved))); err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: merge-коммит: %v", epic.TaskID, err)
		return
	}
	if pushURL != "" {
		if err := wt.PushTo(ctx, pushURL); err != nil {
			removeWT()
			logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: push: %v", epic.TaskID, err)
			return
		}
	}
	removeWT()
	logging.For(project).Infof("gitflow: эпик %s: авто-синхрон с main: тривиальные конфликты авто-разрешены (%d файлов), ветка продвинута",
		epic.TaskID, len(resolved))
	s.Host.SetEpicMergeConflict(ctx, epic.TaskID, nil)
	s.Host.Notify("gitflow: авто-синхрон эпика: конфликты разрешены")
}
