// Авто-шаги git-workflow (Ф-3/Ф-4): worktree задачи, авто-коммит, авто-MR,
// синхрон релизной ветки эпика с main. Выполняются серверными хуками доски
// (см. attachGitHooks) в фоне под пер-проектным mergeLock: не блокируют цикл
// оркестрации и не трогают рабочую копию основного клона.
package server

import (
	"ai/application/gitflow"
	"ai/domain"
	"ai/infrastructure/board"
	"ai/infrastructure/chat"
	"ai/infrastructure/git"
	"ai/infrastructure/workspace"
	"ai/shared/logging"
	"ai/shared/projects"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// taskWorktreePath — обёртка над gitflow.TaskWorktreePath (Этап 6): правило
// раскладки worktree переехало в application/gitflow.
func (s *Server) taskWorktreePath(repoRoot, project, taskID string) string {
	return gitflow.TaskWorktreePath(repoRoot, project, taskID)
}

// taskGitignoreHeader — алиас маркера блока оркестратора (gitflow.GitignoreHeader).
const taskGitignoreHeader = gitflow.GitignoreHeader

// taskGitignore — обёртка над gitflow.TaskGitignore.
func taskGitignore() string { return gitflow.TaskGitignore() }

// ensureTaskGitignore — обёртка над gitflow.EnsureTaskGitignore (идемпотентное
// создание/дополнение .gitignore worktree задачи).
func ensureTaskGitignore(project, taskID, root string) {
	gitflow.EnsureTaskGitignore(project, taskID, root)
}

// taskWorktree — обёртка над gitflow.WorktreeManager (Этап 6, срез F3r):
// политика создания worktree задачи переехала в application/gitflow, здесь
// остаётся адаптер хоста (git, реестр веток, RAG-хуки, merge-lock, доска).
func (s *Server) taskWorktree(ctx context.Context, project string, task *board.Task, store *board.Store) {
	if task == nil {
		return
	}
	m := gitflow.WorktreeManager{Host: &worktreeHost{s: s, store: store}}
	m.TaskWorktree(ctx, project, gitflow.WorktreeTask{ID: task.TaskID, EpicID: task.EpicID})
}

// recordTaskBaseSHA запоминает HEAD worktree задачи как checkpoint.base_sha —
// точку ручного отката «снести всё» (Ф-6, этап 2). Значение не перетирается,
// если уже есть: база задачи — это код на момент её старта, а не текущий HEAD
// очередного прогона (после отката кода и повторного запуска).
func (s *Server) recordTaskBaseSHA(ctx context.Context, project, taskID, worktree string, store *board.Store) {
	if store == nil {
		return
	}
	head, err := git.RepoFromState(s.gitExec, worktree, "", "", "").HeadSHA(ctx)
	if err != nil {
		logging.For(project).Detailf("gitflow: base_sha задачи %s: %v", taskID, err)
		return
	}
	if err := store.PatchTask(ctx, taskID, func(t *board.Task) error {
		if t.Checkpoint == nil {
			t.Checkpoint = &board.TaskCheckpoint{}
		}
		if t.Checkpoint.BaseSHA == "" {
			t.Checkpoint.BaseSHA = head
		}
		return nil
	}); err != nil {
		logging.For(project).Warnf("gitflow: запись base_sha задачи %s: %v", taskID, err)
	}
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

// epicWorktreePath — обёртка над gitflow.EpicWorktreePath (раскладка worktree
// релизной ветки эпика).
func (s *Server) epicWorktreePath(repoRoot, project, epicID string) string {
	return gitflow.EpicWorktreePath(repoRoot, project, epicID)
}

// epicWorktree — обёртка над gitflow.WorktreeManager (Этап 6, срез F3r):
// политика ленивого создания worktree релизной ветки эпика переехала в
// application/gitflow.
func (s *Server) epicWorktree(ctx context.Context, project, epicID string) string {
	m := gitflow.WorktreeManager{Host: &worktreeHost{s: s}}
	return m.EpicWorktree(ctx, project, epicID)
}

// dropEpicWorktreeLocked — обёртка над gitflow.WorktreeManager (Этап 6, срез
// F3r): политика снятия worktree эпика переехала в application/gitflow.
func (s *Server) dropEpicWorktreeLocked(ctx context.Context, project, epicID string) {
	m := gitflow.WorktreeManager{Host: &worktreeHost{s: s}}
	m.DropEpicWorktreeLocked(ctx, project, epicID)
}

// commitTaskWorktree фиксирует незакоммиченные изменения специалиста в
// worktree ветки задачи (git add -A + commit) и публикует ветки worktree'ов
// сабмодулей (merge в базовую ветку сабмодуля + push + обновление gitlink).
// С Ф-6 промежуточными коммитами дерево на `done` обычно ЧИСТОЕ — тогда коммита
// нет (пустой коммит не создаём), но публикация веток сабмодулей всё равно
// выполняется. Ошибки логируются.
func (s *Server) commitTaskWorktree(ctx context.Context, project string, task *board.Task, worktree string) {
	wt := git.RepoFromState(s.gitExec, worktree, "", "", "")
	message := fmt.Sprintf("задача %s: работа специалиста (авто-коммит)", task.TaskID)
	subs, err := git.ListSubmodules(ctx, s.gitExec, worktree)
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
		taskRepo := git.RepoFromState(s.gitExec, subRoot, child.GitRemote, ref.Branch, ref.Base)
		dirty, err := taskRepo.Dirty(ctx)
		if err != nil {
			logging.For(project).Warnf("gitflow: status сабмодуля %s: %v", sub.Path, err)
			return
		}
		// Коммит — только по грязному дереву. А вот публикация ветки сабмодуля
		// (merge в его базовую ветку + push + обновление gitlink) нужна ВСЕГДА:
		// промежуточные коммиты (Ф-6) оставляют worktree сабмодуля чистым, хотя
		// ветка задачи уехала вперёд. Раньше чистое дерево давало continue, и
		// gitlink родителя указывал бы на коммит, которого нет в origin
		// сабмодуля, — MR с битым сабмодулем.
		if dirty {
			if err := taskRepo.Commit(ctx, message); err != nil {
				logging.For(project).Warnf("gitflow: commit сабмодуля %s: %v", sub.Path, err)
				return
			}
			logging.For(project).Infof("gitflow: задача %s: авто-коммит сабмодуля %s в %s", task.TaskID, sub.Path, subRoot)
		}
		if child.GitBranch != "" && ref.Branch != child.GitBranch {
			childRepo := git.RepoFromState(s.gitExec, child.Root, child.GitRemote, child.GitBranch, child.GitBase)
			if _, err := childRepo.MergeFeature(ctx, child.GitBranch, ref.Branch, git.MergeFeatureOptions{Message: message}); err != nil {
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

func childTaskKey(project, taskID string) string { return gitflow.ChildTaskKey(project, taskID) }

// gitflowErrBoard — обёртка над gitflow.GitflowErrBoard.
func gitflowErrBoard(prefix string, err error) string { return gitflow.GitflowErrBoard(prefix, err) }

// autoCommitAndMergeTask — обёртка над gitflow.TaskFinalizer (Этап 6, срез F3e):
// политика авто-шагов «задача → done» переехала в application/gitflow, здесь
// остаётся адаптер хоста (git, реестр веток, доска, сессия, шина доски).
//
// Порядок шагов (см. gitflow.TaskFinalizer.Finalize): авто-коммит worktree →
// авто-MR задачи ДО мёрджа в релиз эпика → штатный мёрдж done→релиз → снятие
// worktree. Ошибки не ломают переход статуса (хук) — логируются, ручные кнопки
// остаются страховкой.
func (s *Server) autoCommitAndMergeTask(ctx context.Context, project string, task *board.Task, store *board.Store) {
	f := &gitflow.TaskFinalizer{Host: &taskFinalizerHost{s: s, project: project, store: store}}
	f.Finalize(ctx, project, task)
}

// taskFinalizerHost — адаптер gitflow.TaskFinalizerHost поверх серверных
// помощников (git-операции, side-реестр веток, доска, сессия, шина доски).
type taskFinalizerHost struct {
	s       *Server
	project string
	store   *board.Store
}

func (h *taskFinalizerHost) Project() gitflow.Project {
	inf, err := h.s.reg.Get(h.project)
	if err != nil {
		return gitflow.Project{}
	}
	return gitflow.Project{IsGit: inf.Kind == workspace.KindGit, HasRemote: inf.GitRemote != ""}
}

func (h *taskFinalizerHost) TaskBranch(taskID string) (gitflow.BranchRef, bool) {
	ref, err := h.s.reg.TaskBranch(h.project, taskID)
	if err != nil {
		return gitflow.BranchRef{}, false
	}
	return gitflow.BranchRef{Branch: ref.Branch, Base: ref.Base, Worktree: ref.Worktree}, true
}

func (h *taskFinalizerHost) CommitWorktree(ctx context.Context, task *domain.Task, worktree string) {
	h.s.commitTaskWorktree(ctx, h.project, task, worktree)
}

func (h *taskFinalizerHost) CreateTaskMR(ctx context.Context, task *domain.Task) (string, bool, error) {
	return h.s.createTaskMROnce(ctx, h.project, task.TaskID, task)
}

func (h *taskFinalizerHost) BranchAlreadyMerged(ctx context.Context, epicID, taskBranch string) (bool, bool) {
	epicRef, err := h.s.reg.EpicBranch(h.project, epicID)
	if err != nil {
		return false, false
	}
	repo, err := h.s.repoOf(ctx, h.project)
	if err != nil {
		return false, false
	}
	merged, _ := repo.MergedInto(ctx, epicRef.Branch, taskBranch)
	return merged, true
}

func (h *taskFinalizerHost) MergeTaskBranch(ctx context.Context, task *domain.Task) (gitflow.MergeOutcome, error) {
	res, err := h.s.mergeTaskBranch(ctx, h.project, task)
	if err != nil {
		var ce *git.MergeConflictError
		if errors.As(err, &ce) {
			return gitflow.MergeOutcome{ConflictFiles: ce.Files}, gitflow.ErrMergeConflict
		}
		return gitflow.MergeOutcome{}, err
	}
	out := gitflow.MergeOutcome{}
	if res != nil {
		out.AlreadyMerged = res.AlreadyMerged
	}
	return out, nil
}

func (h *taskFinalizerHost) ResolveTaskConflict(ctx context.Context, task *domain.Task, files []string) bool {
	lock := h.s.mergeLock(h.project)
	lock.Lock()
	defer lock.Unlock()
	return h.s.tryTaskLLMResolve(ctx, h.project, task, files)
}

func (h *taskFinalizerHost) SaveTask(ctx context.Context, task *domain.Task) error {
	if h.store == nil {
		return nil
	}
	return h.store.SaveTask(ctx, task)
}

func (h *taskFinalizerHost) RemoveTaskWorktree(taskID, worktree string) {
	h.s.removeTaskWorktree(h.project, taskID, worktree)
}

func (h *taskFinalizerHost) EmitBoard(msg string) {
	h.s.srvEmitBoard(h.project, msg)
}

func (h *taskFinalizerHost) AppendStatus(msg string) {
	if sess := h.s.session(h.project); sess != nil {
		sess.append(chat.RoleStatus, msg, "", "", nil)
	}
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

// syncEpicMainOnce — обёртка над gitflow.EpicMainSyncer (Этап 6, срез F3f):
// политика синхронизации релизной ветки эпика с main переехала в
// application/gitflow, здесь остаётся адаптер хоста (git, реестр, доска,
// сессия, шина).
func (s *Server) syncEpicMainOnce(ctx context.Context, project string, epic *board.Epic) {
	sync := &gitflow.EpicMainSyncer{Host: &epicMainHost{s: s, project: project}}
	sync.SyncEpicMain(ctx, project, epic)
}

// syncEpicMainForTask — обёртка над gitflow.EpicMainSyncer (Этап 6, срез F3f):
// синхрон релизной ветки эпика с main перед стартом задачи (вызывающий уже
// держит mergeLock — Locked внутри не берём).
func (s *Server) syncEpicMainForTask(ctx context.Context, project, epicID string, inf workspace.Info, repo *git.Repo, epicRef workspace.BranchRef) {
	sync := &gitflow.EpicMainSyncer{Host: &epicMainHost{s: s, project: project}}
	sync.SyncMainForTask(ctx, project, epicID)
}

// clearEpicMergeConflict снимает признак конфликта мёрджа с эпика на доске
// (успешный синхрон с main / релиз / резолв). Идемпотентно; ошибки логируются.
func (s *Server) clearEpicMergeConflict(ctx context.Context, project, epicID string) {
	s.setEpicMergeConflict(ctx, project, epicID, nil)
}

// setEpicMergeConflict ставит (files != nil) или снимает (files == nil) признак
// конфликта мёрджа релизной ветки эпика. Идемпотентно; ошибки логируются, но
// не ломают вызывающий поток: признак — витрина для UI, источник истины —
// результат следующего merge.
func (s *Server) setEpicMergeConflict(ctx context.Context, project, epicID string, files []string) {
	store, err := s.boardStore(ctx, project)
	if err != nil {
		return
	}
	defer store.Close()
	epic, err := store.GetEpic(ctx, epicID)
	if err != nil {
		return
	}
	if len(files) == 0 && len(epic.MergeConflictFiles) == 0 {
		return
	}
	epic.MergeConflictFiles = files
	if err := store.SaveEpic(ctx, epic); err != nil {
		logging.For(project).Warnf("gitflow: запись конфликта эпика %s: %v", epicID, err)
	}
}

// setEpicMergedIntoMain ставит или снимает признак «релизная ветка эпика влита
// в main» (merged_into_main). Идемпотентно; ошибки логируются.
func (s *Server) setEpicMergedIntoMain(ctx context.Context, project, epicID string, merged bool) {
	store, err := s.boardStore(ctx, project)
	if err != nil {
		return
	}
	defer store.Close()
	epic, err := store.GetEpic(ctx, epicID)
	if err != nil {
		return
	}
	if epic.MergedIntoMain == merged {
		return
	}
	epic.MergedIntoMain = merged
	if err := store.SaveEpic(ctx, epic); err != nil {
		logging.For(project).Warnf("gitflow: запись merged_into_main эпика %s: %v", epicID, err)
	}
}

// worktreeHost — адаптер gitflow.WorktreeHost поверх server (Этап 6, срез F3r):
// git-репозиторий, side-реестр веток, RAG-хуки, merge-lock, доска, точка отката.
type worktreeHost struct {
	s     *Server
	store *board.Store
}

func (h *worktreeHost) Info(project string) (gitflow.WorktreeProject, error) {
	inf, err := h.s.reg.Get(project)
	if err != nil {
		return gitflow.WorktreeProject{}, err
	}
	return gitflow.WorktreeProject{
		IsGit:     inf.Kind == workspace.KindGit,
		Root:      inf.Root,
		GitBranch: inf.GitBranch,
		GitBase:   inf.GitBase,
	}, nil
}

func (h *worktreeHost) Repo(ctx context.Context, project string) (gitflow.WorktreeRepo, error) {
	repo, err := h.s.repoOf(ctx, project)
	if err != nil {
		return nil, err
	}
	return serverWorktreeRepo{repo}, nil
}

func (h *worktreeHost) EnsureSubmodules(ctx context.Context, dir string) ([]gitflow.Submodule, error) {
	subs, err := git.EnsureSubmodules(ctx, h.s.gitExec, dir)
	if err != nil {
		return nil, err
	}
	out := make([]gitflow.Submodule, 0, len(subs))
	for _, sub := range subs {
		out = append(out, gitflow.Submodule{Path: sub.Path, Root: sub.Root})
	}
	return out, nil
}

func (h *worktreeHost) SubmoduleBase(ctx context.Context, dir string) (string, error) {
	return h.s.gitExec.Exec(ctx, dir, "git", "rev-parse", "HEAD")
}

func (h *worktreeHost) DeinitSubmodule(ctx context.Context, dir, path string) error {
	_, err := h.s.gitExec.Exec(ctx, dir, "git", "submodule", "deinit", "-f", "--", path)
	return err
}

func (h *worktreeHost) SubmoduleWorktree(ctx context.Context, dir, branch, base, dest string) error {
	_, err := git.Worktree(ctx, h.s.gitExec, dir, branch, base, dest)
	return err
}

func (h *worktreeHost) CommitWorktree(ctx context.Context, dir, msg string) error {
	repo := git.RepoFromState(h.s.gitExec, dir, "", "", "")
	_, _, err := repo.CommitIfDirty(ctx, msg)
	return err
}

// serverWorktreeRepo — адаптер gitflow.WorktreeRepo поверх *git.Repo (срез H3c):
// application-слой не импортирует infrastructure/git.
type serverWorktreeRepo struct{ r *git.Repo }

func (w serverWorktreeRepo) Root() string { return w.r.Root }

func (w serverWorktreeRepo) WorktreeList(ctx context.Context) ([]gitflow.WorktreeEntry, error) {
	list, err := w.r.WorktreeList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]gitflow.WorktreeEntry, 0, len(list))
	for _, wt := range list {
		out = append(out, gitflow.WorktreeEntry{Branch: wt.Branch, Path: wt.Path})
	}
	return out, nil
}

func (w serverWorktreeRepo) RemoveWorktree(ctx context.Context, path string) error {
	return w.r.RemoveWorktree(ctx, path)
}

func (w serverWorktreeRepo) Rebase(ctx context.Context, branch, onto string) error {
	return w.r.Rebase(ctx, branch, onto)
}

func (w serverWorktreeRepo) Checkout(ctx context.Context, branch string) error {
	return w.r.Checkout(ctx, branch)
}

func (w serverWorktreeRepo) AddWorktree(ctx context.Context, path, branch string) (gitflow.WorktreeRepo, error) {
	wt, err := w.r.AddWorktree(ctx, path, branch)
	if err != nil {
		return nil, err
	}
	return serverWorktreeRepo{wt}, nil
}

func (h *worktreeHost) TaskBranch(project, taskID string) (gitflow.BranchRef, error) {
	ref, err := h.s.reg.TaskBranch(project, taskID)
	if err != nil {
		return gitflow.BranchRef{}, err
	}
	return gitflow.BranchRef{Branch: ref.Branch, Base: ref.Base, Worktree: ref.Worktree}, nil
}

func (h *worktreeHost) EpicBranch(project, epicID string) (gitflow.BranchRef, error) {
	ref, err := h.s.reg.EpicBranch(project, epicID)
	if err != nil {
		return gitflow.BranchRef{}, err
	}
	return gitflow.BranchRef{Branch: ref.Branch, Base: ref.Base, Worktree: ref.Worktree}, nil
}

func (h *worktreeHost) SetTaskBranch(project, taskID string, ref gitflow.BranchRef) error {
	return h.s.reg.SetTaskBranch(project, taskID, workspace.BranchRef{Branch: ref.Branch, Base: ref.Base, Worktree: ref.Worktree})
}

func (h *worktreeHost) SetEpicBranch(project, epicID string, ref gitflow.BranchRef) error {
	return h.s.reg.SetEpicBranch(project, epicID, workspace.BranchRef{Branch: ref.Branch, Base: ref.Base, Worktree: ref.Worktree})
}

func (h *worktreeHost) ChildProject(project, subPath string) (string, string, bool) {
	childName := submoduleProjectName(project, subPath)
	child, err := h.s.reg.Get(childName)
	if err != nil || child.Parent != project {
		return "", "", false
	}
	return childName, child.Root, true
}

func (h *worktreeHost) Locked(project string, fn func()) {
	lock := h.s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()
	fn()
}

func (h *worktreeHost) RefreshRagIndexBranch(project, base string) {
	h.s.refreshRagIndexBranch(project, base)
}

func (h *worktreeHost) RefreshRagTaskBranch(project, branch, base string) {
	h.s.refreshRagTaskBranch(project, branch, base)
}

func (h *worktreeHost) SyncEpicMainForTask(ctx context.Context, project, epicID string) {
	h.s.syncEpicMainForTask(ctx, project, epicID, workspace.Info{}, nil, workspace.BranchRef{})
}

func (h *worktreeHost) InvalidateDiffs(project string) { h.s.invalidateDiffs(project) }

func (h *worktreeHost) RecordTaskBaseSHA(ctx context.Context, project, taskID, worktree string) {
	h.s.recordTaskBaseSHA(ctx, project, taskID, worktree, h.store)
}

func (h *worktreeHost) EmitBoard(project, msg string) { h.s.srvEmitBoard(project, msg) }
