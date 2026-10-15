package server

// Гард «phantom done» (Ф-6, инцидент mytrip/FEL-04): перед переводом задачи в
// done проверяем, что работа реально есть (свои коммиты в ветке задачи или
// правки в worktree). Статус done терминален, поэтому fallback оркестратора не
// должен закрывать пустую задачу.
//
// Само правило живёт в application/usecases (GuardTaskDone, Этап 6); здесь —
// адаптер DoneGuardSource поверх реестра проектов и git.Repo, env-флаг
// KANBAN_DONE_REQUIRES_COMMIT и внедрение гарда в board.Store.

import (
	"context"
	"os"
	"strings"

	"ai/application/usecases"
	"ai/infrastructure/board"
	"ai/infrastructure/git"
	"ai/infrastructure/workspace"
	"ai/logging"
)

// doneRequiresCommitEnabled — гард включён по умолчанию; KANBAN_DONE_REQUIRES_COMMIT=0
// его выключает для досок, где задачи закрываются без кода (исследования,
// решения, разборы).
func doneRequiresCommitEnabled() bool {
	v := strings.TrimSpace(os.Getenv("KANBAN_DONE_REQUIRES_COMMIT"))
	if v == "" {
		return true
	}
	switch strings.ToLower(v) {
	case "0", "false", "no", "off":
		return false
	}
	return true
}

// guardTaskDone — проверка перед переводом задачи в done. Внедряется в
// board.Store.TaskDoneGuard. Не git-проект или гард выключен — пропускает.
func (s *Server) guardTaskDone(ctx context.Context, project string, t *board.Task) error {
	return usecases.GuardTaskDone(ctx, doneGuardSource{s: s}, doneRequiresCommitEnabled(), project, t)
}

// doneGuardSource — адаптер usecases.DoneGuardSource поверх сервера: реестр
// проектов, реестр веток и git.Repo проекта.
type doneGuardSource struct{ s *Server }

func (src doneGuardSource) IsGitProject(project string) bool {
	inf, err := src.s.reg.Get(project)
	return err == nil && inf.Kind == workspace.KindGit
}

func (src doneGuardSource) TaskBranch(project, taskID string) (string, bool) {
	ref, err := src.s.reg.TaskBranch(project, taskID)
	if err != nil || strings.TrimSpace(ref.Branch) == "" {
		return "", false
	}
	return ref.Branch, true
}

func (src doneGuardSource) EpicBase(project, epicID string) string {
	inf, err := src.s.reg.Get(project)
	if err != nil {
		return ""
	}
	base := inf.GitBase
	if ref, eerr := src.s.reg.EpicBranch(project, epicID); eerr == nil && strings.TrimSpace(ref.Branch) != "" {
		base = ref.Branch
	}
	return base
}

func (src doneGuardSource) CommitsAhead(ctx context.Context, project, base, branch string) (int, error) {
	repo, err := src.s.repoOf(ctx, project)
	if err != nil {
		return 0, err
	}
	return repo.CountCommits(ctx, base, branch)
}

func (src doneGuardSource) DirtyPaths(ctx context.Context, project, taskID string) ([]string, error) {
	return src.s.worktreeWorkPaths(ctx, project, taskID)
}

func (src doneGuardSource) Warn(project, format string, args ...any) {
	logging.For(project).Warnf(format, args...)
}

// worktreeWorkPaths — пути незакоммиченных изменений в worktree ветки задачи.
// Worktree может отсутствовать (задача ещё не бралась в работу) — тогда
// доказательством работы остаются только коммиты ветки.
func (s *Server) worktreeWorkPaths(ctx context.Context, project, taskID string) ([]string, error) {
	inf, err := s.reg.Get(project)
	if err != nil {
		return nil, err
	}
	dir := s.taskWorktreePath(inf.Root, project, taskID)
	if dir == "" {
		return nil, nil
	}
	if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
		return nil, nil // worktree нет
	}
	return git.RepoFromState(s.gitExec, dir, "", "", "").DirtyPaths(ctx)
}
