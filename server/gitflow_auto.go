// Авто-шаги git-workflow (Ф-3/Ф-4): worktree задачи, авто-коммит, авто-MR,
// синхрон релизной ветки эпика с main. Выполняются серверными хуками доски
// (см. attachGitHooks) в фоне под пер-проектным mergeLock: не блокируют цикл
// оркестрации и не трогают рабочую копию основного клона.
package server

import (
	"ai/board"
	"ai/gitops"
	"ai/logging"
	"ai/projects"
	"ai/workspace"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// taskWorktreePath возвращает каталог постоянного worktree ветки задачи —
// сосед основного клона (как MergeFeature/конфликтные worktree).
func (s *Server) taskWorktreePath(repoRoot, project, taskID string) string {
	return filepath.Join(filepath.Dir(repoRoot),
		".wt-task-"+project+"-"+gitops.SanitizeBranchName(taskID))
}

// taskWorktree — Ф-3: при переводе задачи «в работу» создаёт постоянный
// worktree её ветки (ai/task/<id>), в который специалист получает OutputDir.
// Идемпотентно: повторный in_progress при уже созданном worktree — no-op.
// Ошибки не ломают переход статуса — логируются (специалист продолжит работать
// в общем клоне temp/<проект>, авто-коммит на done пропадёт).
func (s *Server) taskWorktree(ctx context.Context, project string, task *board.Task, store *board.Store) {
	if task == nil || task.TaskID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	taskRef, err := s.reg.TaskBranch(project, task.TaskID)
	if err != nil {
		logging.For(project).Detailf("gitflow: worktree задачи %s: ветки нет: %v — пропуск", task.TaskID, err)
		return
	}
	if strings.TrimSpace(taskRef.Worktree) != "" {
		return // уже создан
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return
	}
	wtPath := s.taskWorktreePath(repo.Root, project, task.TaskID)

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	// Чистим осиротевший worktree (прерванный прошлый цикл задачи).
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: удаляю осиротевший worktree задачи %s (%s)", task.TaskID, wtPath)
		_ = repo.RemoveWorktree(ctx, wtPath)
	}
	wt, err := repo.AddWorktree(ctx, wtPath, taskRef.Branch)
	if err != nil {
		logging.For(project).Warnf("gitflow: worktree задачи %s: %v", task.TaskID, err)
		return
	}
	if subs, err := gitops.EnsureSubmodules(ctx, s.gitExec, wt.Root); err != nil {
		_ = wt.RemoveWorktree(ctx, wtPath)
		logging.For(project).Warnf("gitflow: submodule worktree задачи %s: %v", task.TaskID, err)
		return
	} else {
		for _, sub := range subs {
			childName := submoduleProjectName(project, sub.Path)
			child, err := s.reg.Get(childName)
			if err != nil || child.Parent != project {
				continue
			}
			base, err := s.gitExec.Exec(ctx, sub.Root, "git", "rev-parse", "HEAD")
			if err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: base сабмодуля %s задачи %s: %v", sub.Path, task.TaskID, err)
				return
			}
			branch := taskRef.Branch + "/submodule/" + gitops.SanitizeBranchName(filepath.ToSlash(sub.Path))
			if _, err := s.gitExec.Exec(ctx, wt.Root, "git", "submodule", "deinit", "-f", "--", sub.Path); err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: deinit сабмодуля %s для task worktree: %v", sub.Path, err)
				return
			}
			if _, err := gitops.Worktree(ctx, s.gitExec, child.Root, branch, strings.TrimSpace(base), sub.Root); err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: worktree сабмодуля %s задачи %s: %v", sub.Path, task.TaskID, err)
				return
			}
			if err := s.reg.SetTaskBranch(childName, childTaskKey(project, task.TaskID), workspace.BranchRef{
				Branch: branch, Base: strings.TrimSpace(base), Worktree: sub.Root,
			}); err != nil {
				_ = wt.RemoveWorktree(ctx, wtPath)
				logging.For(project).Warnf("gitflow: регистрация submodule task branch %s: %v", sub.Path, err)
				return
			}
		}
	}
	if err := s.reg.SetTaskBranch(project, task.TaskID, workspace.BranchRef{
		Branch: taskRef.Branch, Base: taskRef.Base, Worktree: wtPath,
	}); err != nil {
		_ = wt.RemoveWorktree(ctx, wtPath)
		logging.For(project).Warnf("gitflow: worktree задачи %s: реестр: %v", task.TaskID, err)
		return
	}
	s.invalidateDiffs(project)
	logging.For(project).Infof("gitflow: задача %s → worktree %s (%s)", task.TaskID, wtPath, taskRef.Branch)
	s.srvEmitBoard(project, "gitflow: worktree задачи")
}

// taskOutputDir возвращает каталог работы специалиста по задаче: для
// git-проектов — worktree ветки задачи (Ф-3), если он создан; иначе проектная
// копия temp/<проект>. Используется как хук SetOutputDir в KanbanRunner.
func (s *Server) taskOutputDir(project, taskID string) string {
	if ref, err := s.reg.TaskBranch(project, taskID); err == nil {
		if wt := strings.TrimSpace(ref.Worktree); wt != "" {
			return wt
		}
	}
	return projects.ProjectDir(project)
}

// commitTaskWorktree фиксирует незакоммиченные изменения специалиста в
// worktree ветки задачи (git add -A + commit). Пустой worktree (специалист
// ничего не менял/работал в общей копии) — no-op. Ошибки логируются.
func (s *Server) commitTaskWorktree(ctx context.Context, project string, task *board.Task, worktree string) {
	wt := gitops.RepoFromState(s.gitExec, worktree, "", "", "")
	message := fmt.Sprintf("задача %s: работа специалиста (авто-коммит)", task.TaskID)
	subs, err := gitops.ListSubmodules(ctx, s.gitExec, worktree)
	if err != nil {
		logging.For(project).Warnf("gitflow: чтение сабмодулей worktree задачи %s: %v", task.TaskID, err)
		return
	}
	for _, sub := range subs {
		childName := submoduleProjectName(project, sub.Path)
		child, err := s.reg.Get(childName)
		if err != nil || child.Parent != project {
			continue
		}
		ref, err := s.reg.TaskBranch(childName, childTaskKey(project, task.TaskID))
		if err != nil {
			continue
		}
		subRoot := filepath.Join(worktree, sub.Path)
		taskRepo := gitops.RepoFromState(s.gitExec, subRoot, child.GitRemote, ref.Branch, ref.Base)
		dirty, err := taskRepo.Dirty(ctx)
		if err != nil {
			logging.For(project).Warnf("gitflow: status сабмодуля %s: %v", sub.Path, err)
			return
		}
		if !dirty {
			continue
		}
		if err := taskRepo.Commit(ctx, message); err != nil {
			logging.For(project).Warnf("gitflow: commit сабмодуля %s: %v", sub.Path, err)
			return
		}
		if child.GitBranch != "" && ref.Branch != child.GitBranch {
			childRepo := gitops.RepoFromState(s.gitExec, child.Root, child.GitRemote, child.GitBranch, child.GitBase)
			if _, err := childRepo.MergeFeature(ctx, child.GitBranch, ref.Branch, gitops.MergeFeatureOptions{Message: message}); err != nil {
				logging.For(project).Warnf("gitflow: merge сабмодуля %s в %s: %v", sub.Path, child.GitBranch, err)
				return
			}
			if _, err := s.gitExec.Exec(ctx, child.Root, "git", "checkout", child.GitBranch); err != nil {
				logging.For(project).Warnf("gitflow: checkout feature branch сабмодуля %s: %v", sub.Path, err)
				return
			}
			if err := s.pushRepo(ctx, childRepo, child.GitRemote); err != nil {
				logging.For(project).Warnf("gitflow: push сабмодуля %s: %v", sub.Path, err)
				return
			}
			if _, err := s.gitExec.Exec(ctx, subRoot, "git", "reset", "--hard", child.GitBranch); err != nil {
				logging.For(project).Warnf("gitflow: обновление gitlink сабмодуля %s: %v", sub.Path, err)
				return
			}
		}
	}
	dirty, err := wt.Dirty(ctx)
	if err != nil || !dirty {
		return
	}
	if err := wt.Commit(ctx, message); err != nil {
		logging.For(project).Warnf("gitflow: авто-коммит задачи %s в %s: %v", task.TaskID, worktree, err)
		return
	}
	logging.For(project).Infof("gitflow: задача %s: авто-коммит в worktree %s", task.TaskID, worktree)
}

// removeTaskWorktree снимает worktree задачи (`git worktree remove --force` +
// удаление каталога) и очищает Worktree в side-реестре. Пустой worktree —
// no-op. Ошибки логируются (осиротевший каталог уберёт следующий цикл).
func (s *Server) removeTaskWorktree(project, taskID, worktree string) {
	if strings.TrimSpace(worktree) == "" {
		return
	}
	for _, child := range s.repositoriesForProject(project) {
		if child == project {
			continue
		}
		key := childTaskKey(project, taskID)
		if ref, err := s.reg.TaskBranch(child, key); err == nil && ref.Worktree != "" {
			if repo, err := s.repoOf(context.Background(), child); err == nil {
				_ = repo.RemoveWorktree(context.Background(), ref.Worktree)
			}
			_ = s.reg.DeleteTaskBranch(child, key)
			s.invalidateDiffs(child)
		}
	}
	if repo, err := s.repoOf(context.Background(), project); err == nil {
		if err := repo.RemoveWorktree(context.Background(), worktree); err != nil {
			logging.For(project).Warnf("gitflow: снятие worktree задачи %s: %v", taskID, err)
		}
	}
	if ref, err := s.reg.TaskBranch(project, taskID); err == nil {
		_ = s.reg.SetTaskBranch(project, taskID, workspace.BranchRef{Branch: ref.Branch, Base: ref.Base})
	}
	s.invalidateDiffs(project)
}

func childTaskKey(project, taskID string) string { return project + "::" + taskID }

// autoCommitAndMergeTask — Ф-2/Ф-3: авто-действия при переводе задачи в done:
//
//  1. авто-коммит незакоммиченных изменений worktree в ветку задачи;
//  2. авто-MR задачи (если remote/фордж доступны и MR ещё не создан);
//  3. штатный мёрдж ветки задачи в релизную ветку эпика (mergeTaskBranch);
//  4. снятие worktree задачи.
//
// Авто-MR создаётся ДО мёрджа в релиз эпика: после влития ветки между ней и
// релизной веткой не остаётся коммитов, и GitHub/GitLab отклоняют такой MR
// (422 «No commits between …»). Ветку задачи и базу MR пушит сам
// createTaskMROnce (ensureRemoteBase), поэтому порядок выполнения безопасен.
//
// Ошибки не ломают сам переход статуса (хук) — логируются, ручные кнопки
// остаются страховкой.
func (s *Server) autoCommitAndMergeTask(ctx context.Context, project string, task *board.Task, store *board.Store) {
	if task == nil || task.TaskID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	taskRef, err := s.reg.TaskBranch(project, task.TaskID)
	if err != nil {
		logging.For(project).Detailf("gitflow: авто-шаги %s: ветки нет: %v — пропуск", task.TaskID, err)
		return
	}

	// 1) Коммит в worktree ветки задачи (если специалист оставил изменения).
	worktree := strings.TrimSpace(taskRef.Worktree)
	if worktree != "" {
		s.commitTaskWorktree(ctx, project, task, worktree)
	}

	// 2) Авто-MR задачи ДО мёрджа в релиз эпика (см. комментарий функции):
	// пока ветка задачи несёт свои коммиты, фордж может открыть MR.
	if inf.GitRemote != "" {
		if mrURL, created, merr := s.createTaskMROnce(ctx, project, task.TaskID, task); merr != nil {
			logging.For(project).Warnf("gitflow: авто-MR задачи %s: %v", task.TaskID, merr)
		} else if created {
			logging.For(project).Infof("gitflow: задача %s → авто-MR %s", task.TaskID, mrURL)
		}
	}

	// 3) Штатный мёрдж done→релиз.
	res, err := s.mergeTaskBranch(ctx, project, task)
	if err != nil {
		var ce *gitops.MergeConflictError
		if errors.As(err, &ce) {
			logging.For(project).Warnf("gitflow: авто-мёрдж %s: конфликт в %s — требуется резолв (Ф-4)",
				task.TaskID, strings.Join(ce.Files, ", "))
		} else {
			logging.For(project).Warnf("gitflow: авто-мёрдж %s: %v", task.TaskID, err)
		}
		// Worktree задачи снимаем в любом случае: правки уже в ветке (авто-
		// коммит выше), работа специалиста завершена. Релизная ветка не тронута.
		if worktree != "" {
			s.removeTaskWorktree(project, task.TaskID, worktree)
		}
		s.srvEmitBoard(project, "gitflow: авто-мёрдж задачи завершён с ошибкой")
		return
	}
	logging.For(project).Infof("gitflow: задача %s → done: авто-мёрдж в релиз эпика %s (already=%v)",
		task.TaskID, task.EpicID, res.AlreadyMerged)

	// 4) Снимаем worktree задачи.
	if worktree != "" {
		s.removeTaskWorktree(project, task.TaskID, worktree)
	}
	s.srvEmitBoard(project, "gitflow: готовность задачи завершена (done → релиз)")
}

// syncEpicWithMain — Ф-4: авто-синхрон релизной ветки эпика с main. Запускается
// серверным хук'ом при переводе эпика в done, выполняется в фоне (не блокирует
// цикл оркестрации и статусный переход). После успешного синхрона клик
// «Залить в main» проходит без конфликтов («быстрый путь» handleReleaseEpic).
func (s *Server) syncEpicWithMain(ctx context.Context, project string, epic *board.Epic) {
	go func() {
		ctxi, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s.syncEpicMainOnce(ctxi, project, epic)
	}()
}

// syncEpicMainOnce — собственно синхрон (под mergeLock). Если конфликтов с main
// нет — main вливается в релизную ветку быстрым путём (MergeFeature). Если есть
// — авто-резолв тривиальных блоков; сложные конфликты НЕ продвигают ветку,
// оставляя рабочую поверхность флоу rebase/резолва (интерактивному процессу
// ничего не ломаем).
func (s *Server) syncEpicMainOnce(ctx context.Context, project string, epic *board.Epic) {
	if epic == nil || epic.TaskID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	main := strings.TrimSpace(inf.GitBase)
	if main == "" {
		return
	}
	epicRef, err := s.reg.EpicBranch(project, epic.TaskID)
	if err != nil {
		return
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	res, err := repo.MergeFeature(ctx, epicRef.Branch, main, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: синхрон релизной ветки %s с main", epic.TaskID, epicRef.Branch),
		PushURL: remotePushURL(inf),
	})
	if err == nil {
		logging.For(project).Infof("gitflow: эпик %s: авто-синхрон с main (already=%v)", epic.TaskID, res.AlreadyMerged)
		s.srvEmitBoard(project, "gitflow: авто-синхрон эпика с main")
		return
	}
	var ce *gitops.MergeConflictError
	if !errors.As(err, &ce) {
		if errors.Is(err, context.Canceled) {
			return
		}
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: %v", epic.TaskID, err)
		return
	}
	s.autoResolveMainSync(ctx, project, epic, inf, repo, epicRef)
}

// autoResolveMainSync — продолжение авто-синхрона при конфликтах: настоящий
// merge main в постоянном worktree релизной ветки, авто-резолв тривиальных
// блоков, закрытие merge-коммита и push (всё на релизной ветке эпика). Сложные
// конфликты — отказ от продвижения: ветка остаётся нетронутой, работает
// интерактивный флоу handleEpicRebase/handleEpicResolve.
func (s *Server) autoResolveMainSync(ctx context.Context, project string, epic *board.Epic, inf workspace.Info, repo *gitops.Repo, epicRef workspace.BranchRef) {
	wtPath := filepath.Join(filepath.Dir(repo.Root),
		".conflict-"+project+"-"+gitops.SanitizeBranchName(epic.TaskID))
	if _, err := os.Stat(wtPath); err == nil {
		logging.For(project).Warnf("gitflow: удаляю осиротевший конфликтный worktree %s", wtPath)
		_ = repo.RemoveWorktree(ctx, wtPath)
	}
	wt, err := repo.AddWorktree(ctx, wtPath, epicRef.Branch)
	if err != nil {
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: worktree: %v", epic.TaskID, err)
		return
	}
	removeWT := func() { _ = repo.RemoveWorktree(ctx, wtPath) }

	_, _ = wt.MergeBranch(ctx, inf.GitBase,
		fmt.Sprintf("эпик %s: влитие main (авто-резолв)", epic.TaskID))

	unmerged, err := wt.UnmergedFiles(ctx)
	if err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: ls-files -u: %v", epic.TaskID, err)
		return
	}
	resolved, hard, err := gitops.TrivialResolve(wtPath, unmerged)
	if err != nil {
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: авто-резолв: %v", epic.TaskID, err)
		return
	}
	if len(hard) > 0 {
		// Сложные конфликты без модели не решаются: релизную ветку не трогаем,
		// доступен ручной/модельный rebase + резолв + release (Ф-4).
		removeWT()
		logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: сложные конфликты [%s] — флоу rebase/резолв",
			epic.TaskID, strings.Join(hard, ", "))
		s.srvEmitBoard(project, "gitflow: авто-синхрон эпика: сложные конфликты")
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
	if pushURL := remotePushURL(inf); pushURL != "" {
		if err := wt.PushTo(ctx, pushURL); err != nil {
			removeWT()
			logging.For(project).Warnf("gitflow: авто-синхрон эпика %s: push: %v", epic.TaskID, err)
			return
		}
	}
	removeWT()
	logging.For(project).Infof("gitflow: эпик %s: авто-синхрон с main: тривиальные конфликты авто-разрешены (%d файлов), ветка продвинута",
		epic.TaskID, len(resolved))
	s.srvEmitBoard(project, "gitflow: авто-синхрон эпика: конфликты разрешены")
}
