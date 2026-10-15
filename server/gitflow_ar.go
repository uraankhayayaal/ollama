// ЛЛМ-разрешение конфликтов слияния через LLM (Ф-9): HTTP-обвязка.
//
// Само правило (цикл «промпт → агент-разработчик → приёмка → снятие конфликтов
// с доски») вынесено в application/gitflow (Этап 6, «Интерфейсы и адаптеры»).
// Здесь остаётся серверная обвязка: доска, LLM-провайдер, приёмка, git
// (stage/commit/push) и REST-эндпоинты.

package server

import (
	"ai/acceptor"
	"ai/application/gitflow"
	"ai/application/planner"
	"ai/application/usecases"
	"ai/domain"
	"ai/domain/services"
	"ai/infrastructure/board"
	"ai/infrastructure/git"
	"ai/infrastructure/workspace"
	"ai/logging"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// resolveDeveloper — адаптер gitflow.Developer: агент-специалист нужной роли
// (planner.SpecialistForRole) и его прогон через цикл агента сервера.
type resolveDeveloper struct{ prov usecases.FullCycle }

func (d resolveDeveloper) SpecialistForRole(project, role, prompt string) services.Agent {
	return planner.SpecialistForRole(project, role, prompt)
}

func (d resolveDeveloper) Generate(ctx context.Context, agent services.Agent) (*domain.AgentResponse, error) {
	return d.prov.Generate(ctx, agent)
}

// resolveAcceptor — адаптер gitflow.Acceptor: фиксированная приёмка worktree
// конфликта (сборка до 3 мин, формат и анализ, без ЛСП и установки
// зависимостей — агент уже правит готовое дерево).
type resolveAcceptor struct{}

func (resolveAcceptor) Accept(dir string) *domain.Report {
	return acceptor.Accept(dir, acceptor.Config{
		BuildTimeout: 3 * time.Minute,
		CheckFormat:  true,
		CheckAnalyze: true,
		CheckLSP:     false,
		InstallDeps:  false,
	})
}

// attemptLLMResolve — полный цикл авторезолвина конфликтов через разработчика.
// Специализация выбирается по роли эпика/задачи. При успехе выполняет
// stage + commit + push. Возвращает true, если конфликты разрешены.
func (s *Server) attemptLLMResolve(ctx context.Context, project string, epic *board.Epic, workdir string, hard []string, inf workspace.Info, branch, role string) bool {
	if len(hard) == 0 {
		return false
	}

	store, err := s.boardStore(ctx, project)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: board store: %v", err)
		return false
	}
	defer store.Close()

	tasks, err := store.TasksByEpic(ctx, epic.TaskID)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: tasks: %v", err)
		return false
	}

	prov, err := s.provider()
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: provider: %v", err)
		return false
	}
	if prov == nil {
		logging.For(project).Warnf("gitflow: LLM resolve: эпик %s — LLM недоступен", epic.TaskID)
		return false
	}

	resolver := gitflow.NewResolver(epic, tasks, hard, workdir, gitflow.Deps{
		Store:     store,
		Developer: resolveDeveloper{prov: prov},
		Acceptor:  resolveAcceptor{},
		Markers:   git.HasConflictMarkers,
	})

	result, err := resolver.ResolveWithLLM(ctx, project, role)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: %v", err)
		return false
	}
	if result == nil {
		logging.For(project).Warnf("gitflow: LLM resolve: эпик %s — LLM недоступен", epic.TaskID)
		return false
	}
	if !result.Cleared {
		reason := result.VerifyErr
		if reason == "" {
			reason = "неизвестная причина"
		}
		logging.For(project).Warnf("gitflow: LLM resolve: эпик %s — не удалось разрешить: %s", epic.TaskID, reason)
		return false
	}

	wt := git.RepoFromState(s.gitExec, workdir, inf.GitRemote, branch, strings.TrimSpace(inf.GitBase))
	if err := wt.Stage(ctx, hard); err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: stage: %v", err)
		return false
	}

	msg := fmt.Sprintf("эпик %s: LLM-резолв конфликтов (%d файлов)", epic.TaskID, len(hard))
	if err := wt.Commit(ctx, msg); err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve: commit: %v", err)
		return false
	}

	if pushURL := remotePushURL(inf); pushURL != "" {
		if err := wt.PushTo(ctx, pushURL); err != nil {
			logging.For(project).Warnf("gitflow: LLM resolve: push: %v", err)
			return false
		}
	}

	logging.For(project).Infof("gitflow: LLM resolve: эпик %s: %d файлов разрешено и зафиксировано", epic.TaskID, len(hard))
	return true
}

// tryTaskLLMResolve — авторезолвинг конфликта мёрджа задачи в релиз эпика.
// Создаёт worktree релизной ветки, вливает задачу, собирает конфликты,
// пробует TrivialResolve, затем LLM. Возвращает true при успехе.
func (s *Server) tryTaskLLMResolve(ctx context.Context, project string, task *board.Task, conflictFiles []string) bool {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return false
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return false
	}
	epicRef, err := s.reg.EpicBranch(project, task.EpicID)
	if err != nil {
		return false
	}
	taskRef, err := s.reg.TaskBranch(project, task.TaskID)
	if err != nil {
		return false
	}

	wtPath := filepath.Join(filepath.Dir(repo.Root), ".resolve-"+project+"-"+git.SanitizeBranchName(task.TaskID))
	// 4.1: worktree ветки эпика (архитектор) держит релизную ветку
	// checked-out — резолвный worktree не смог бы её получить. Вызов под
	// mergeLock: оба вызывателя (handleTaskLLMResolve, авто-мёрдж) берут лок.
	s.dropEpicWorktreeLocked(ctx, project, task.EpicID)
	if _, err := os.Stat(wtPath); err == nil {
		_ = repo.RemoveWorktree(ctx, wtPath)
	}
	wt, err := repo.AddWorktree(ctx, wtPath, epicRef.Branch)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: worktree: %v", task.TaskID, err)
		return false
	}
	defer func() { _ = repo.RemoveWorktree(ctx, wtPath) }()

	_, _ = wt.MergeBranch(ctx, taskRef.Branch,
		fmt.Sprintf("задача %s: влитие в релиз эпика %s (LLM-резолв)", task.TaskID, task.EpicID))

	unmerged, err := wt.UnmergedFiles(ctx)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: ls-files -u: %v", task.TaskID, err)
		return false
	}

	resolved, hard, err := git.TrivialResolve(wtPath, unmerged)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: авто-резолв: %v", task.TaskID, err)
		return false
	}

	if len(hard) == 0 {
		if err := wt.Stage(ctx, resolved); err != nil {
			logging.For(project).Warnf("gitflow: LLM resolve задачи %s: stage: %v", task.TaskID, err)
			return false
		}
		msg := fmt.Sprintf("задача %s: авто-резолв в релизе эпика %s", task.TaskID, task.EpicID)
		if err := wt.Commit(ctx, msg); err != nil {
			logging.For(project).Warnf("gitflow: LLM resolve задачи %s: commit: %v", task.TaskID, err)
			return false
		}
		if pushURL := remotePushURL(inf); pushURL != "" {
			if err := wt.PushTo(ctx, pushURL); err != nil {
				logging.For(project).Warnf("gitflow: LLM resolve задачи %s: push: %v", task.TaskID, err)
				return false
			}
		}
		logging.For(project).Infof("gitflow: LLM resolve задачи %s: тривиальные конфликты авто-разрешены", task.TaskID)
		return true
	}

	store, err := s.boardStore(ctx, project)
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: board store: %v", task.TaskID, err)
		return false
	}
	epic, err := store.GetEpic(ctx, task.EpicID)
	store.Close()
	if err != nil {
		logging.For(project).Warnf("gitflow: LLM resolve задачи %s: epic: %v", task.TaskID, err)
		return false
	}

	if len(resolved) > 0 {
		_ = wt.Stage(ctx, resolved)
	}

	return s.attemptLLMResolve(ctx, project, epic, wtPath, hard, inf, epicRef.Branch, task.AssignedRole)
}

// handleTaskLLMResolve — REST-эндпоинт авторезолвина конфликта задачи через LLM.
// POST /api/projects/{id}/tasks/{tid}/resolve
func (s *Server) handleTaskLLMResolve(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()

	task, err := store.GetTask(r.Context(), taskID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "задача не найдена")
		return
	}
	if len(task.MergeConflictFiles) == 0 {
		writeErr(w, http.StatusConflict, "у задачи нет неразрешённых конфликтов")
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	resolved := s.tryTaskLLMResolve(r.Context(), project, task, task.MergeConflictFiles)
	if resolved {
		if len(task.MergeConflictFiles) > 0 {
			task.MergeConflictFiles = nil
			_ = store.SaveTask(r.Context(), task)
		}
		s.srvEmitBoard(project, "gitflow: LLM-резолв задачи")
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resolved": true})
		return
	}

	writeJSON(w, http.StatusConflict, map[string]any{
		"status":  "failed",
		"files":   task.MergeConflictFiles,
		"message": "LLM не удалось разрешить конфликты",
	})
}

// handleEpicLLMResolve — REST-эндпоинт авторезолвина конфликта эпика через LLM.
// POST /api/projects/{id}/epics/{eid}/auto-resolve
func (s *Server) handleEpicLLMResolve(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	epic, err := store.GetEpic(r.Context(), epicID)
	store.Close()
	if err != nil {
		writeErr(w, http.StatusNotFound, "эпик не найден")
		return
	}
	if len(epic.MergeConflictFiles) == 0 {
		writeErr(w, http.StatusConflict, "у эпика нет неразрешённых конфликтов")
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest, "проект не найден или не git")
		return
	}
	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	epicRef, err := s.reg.EpicBranch(project, epicID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ветка эпика не найдена")
		return
	}

	wtPath := filepath.Join(filepath.Dir(repo.Root), ".conflict-"+project+"-"+git.SanitizeBranchName(epicID))
	// 4.1: worktree ветки эпика (архитектор) держит релизную ветку
	// checked-out — резолвный worktree не смог бы её получить (лок взят выше).
	s.dropEpicWorktreeLocked(r.Context(), project, epicID)
	if _, err := os.Stat(wtPath); err == nil {
		_ = repo.RemoveWorktree(r.Context(), wtPath)
	}
	wt, err := repo.AddWorktree(r.Context(), wtPath, epicRef.Branch)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "worktree: "+err.Error())
		return
	}
	defer func() { _ = repo.RemoveWorktree(r.Context(), wtPath) }()

	_, _ = wt.MergeBranch(r.Context(), inf.GitBase,
		fmt.Sprintf("эпик %s: LLM-резолв с main", epicID))

	unmerged, err := wt.UnmergedFiles(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ls-files -u: "+err.Error())
		return
	}

	resolved, hard, err := git.TrivialResolve(wtPath, unmerged)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "TrivialResolve: "+err.Error())
		return
	}

	if len(hard) == 0 {
		_ = wt.Stage(r.Context(), resolved)
		_ = wt.Commit(r.Context(), fmt.Sprintf("эпик %s: авто-резолв с main", epicID))
		if pushURL := remotePushURL(inf); pushURL != "" {
			_ = wt.PushTo(r.Context(), pushURL)
		}
		s.clearEpicMergeConflict(r.Context(), project, epicID)
		s.srvEmitBoard(project, "gitflow: LLM-резолв эпика")
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resolved": true})
		return
	}

	if len(resolved) > 0 {
		_ = wt.Stage(r.Context(), resolved)
	}

	ok := s.attemptLLMResolve(r.Context(), project, epic, wtPath, hard, inf, epicRef.Branch, epic.AssignedRole)
	if ok {
		s.clearEpicMergeConflict(r.Context(), project, epicID)
		s.srvEmitBoard(project, "gitflow: LLM-резолв эпика")
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "resolved": true})
		return
	}

	writeJSON(w, http.StatusConflict, map[string]any{
		"status":  "failed",
		"files":   hard,
		"message": "LLM не удалось разрешить конфликты",
	})
}
