package server

// Гард «phantom done» (Ф-6, инцидент mytrip/FEL-04).
//
// Что произошло: задача FEL-04 ни разу не запускала разработчика по делу,
// её ветка осталась на коммите предыдущей задачи, worktree был чистым — но
// fallback оркестратора («в работе → выполнена (агент не сменил статус)»)
// перевёл её в done. В итоге доска показывала «выполнено», MR был пустым,
// а эпик ждал работу, которой не было. Статус done терминальный: откатить
// такой финал без ручного вмешательства нельзя, поэтому проверять надо ДО
// перехода.
//
// Правило: задача git-проекта не может стать done, пока в её ветке нет
// ни одного своего коммита относительно базы эпика И worktree не содержит
// незакоммиченных правок. Любой из двух признаков означает, что работа была.
// Ошибки git считаем «нет доказательств» только когда ветка существует;
// если ветки нет (задача без git-flow) — проверка не применяется.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai/board"
	"ai/gitops"
	"ai/logging"
	"ai/workspace"
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
	if t == nil || t.TaskID == "" || !doneRequiresCommitEnabled() {
		return nil
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return nil
	}
	taskRef, err := s.reg.TaskBranch(project, t.TaskID)
	if err != nil {
		// Ветки задачи нет — не наша забота: done у задачи без ветки
		// легально (аналитика, решение), гард не блокирует.
		return nil
	}
	base := inf.GitBase
	if epicRef, eerr := s.reg.EpicBranch(project, t.EpicID); eerr == nil && strings.TrimSpace(epicRef.Branch) != "" {
		base = epicRef.Branch
	}

	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return nil
	}

	// Признак 1: в worktree есть незакоммиченные правки агента — работа велась,
	// авто-коммит при done её зафиксирует. Служебные файлы оркестратора
	// (.gitignore, который сам пишет ensureTaskGitignore) доказательством
	// работы не считаются, иначе гард был бы слепым.
	if paths, derr := s.worktreeWorkPaths(ctx, project, t.TaskID); derr == nil {
		for _, p := range paths {
			if !isOrchestratorArtifact(p) {
				return nil
			}
		}
	}

	// Признак 2: ветка задачи ушла от базы эпика.
	n, cerr := repo.CountCommits(ctx, base, taskRef.Branch)
	if cerr != nil {
		// Не смогли доказать, что работа была, — но и не смогли доказать,
		// что её не было: блокировать переход человека опасно, логируем.
		logging.For(project).Warnf("[гард done] задача %s: не удалось посчитать коммиты (%s..%s): %v — переход разрешён",
			t.TaskID, base, taskRef.Branch, cerr)
		return nil
	}
	if n > 0 {
		return nil
	}
	return fmt.Errorf("задача %s не может стать выполненной: в ветке %s нет ни одного коммита относительно %s, и worktree пуст — работа фактически не сделана (инцидент FEL-04). Запусти задачу в работу или закрой её как отменённую; если задача закрывается без кода — KANBAN_DONE_REQUIRES_COMMIT=0",
		t.TaskID, taskRef.Branch, base)
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
	return gitops.RepoFromState(s.gitExec, dir, "", "", "").DirtyPaths(ctx)
}

// isOrchestratorArtifact — служебные пути, которые кладёт сам оркестратор.
// Неизменённый .gitignore, добавленный при создании worktree задачи, не должен
// превращать пустую задачу в «выполненную» (инцидент FEL-04).
func isOrchestratorArtifact(path string) bool {
	p := filepath.ToSlash(strings.TrimSpace(path))
	if p == "" {
		return true
	}
	return p == gitIgnoreFileName || strings.HasSuffix(p, "/"+gitIgnoreFileName)
}
