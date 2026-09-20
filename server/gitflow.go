// Git-workflow «эпик = релизная ветка, задача = фича-ветка» (Ф-1).
//
// REST-маршруты создания веток эпиков и задач внутри единого клона проекта
// (branch-in-place, temp/<имя>): git-команды идут через gitops.Repo, а
// привязка epic_id/task_id → ветка хранится в side-реестре workspace. Сама
// рабочая копия и текущая ветка НЕ переключаются (CreateBranch создаёт ref),
// поэтому прежний flow приёмки «Принять → MR» не ломается.
package server

import (
	"ai/agents/architect"
	"ai/board"
	"ai/chat"
	"ai/gitops"
	"ai/logging"
	"ai/workspace"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

const (
	// epicBranchPrefix — префикс релизной ветки эпика (база = main).
	epicBranchPrefix = "ai/epic/"
	// taskBranchPrefix — префикс фича-ветки задачи (база = ветка эпика).
	taskBranchPrefix = "ai/task/"
)

// boardStore открывает хранилище доски проекта, привязывая авто-действие
// «done → мёрдж в релизную ветку» (Ф-2).
func (s *Server) boardStore(ctx context.Context, project string) (*board.Store, error) {
	store, err := board.NewStore(ctx, architect.LoadConfig().StoreConfig(project))
	if err != nil {
		return nil, err
	}
	s.attachTaskDoneHook(project, store)
	return store, nil
}

// handleCreateEpicBranch создаёт релизную ветку эпика от базовой ветки проекта
// (main): git branch ai/epic/<id> <base> без переключения. Идемпотентно — при
// повторном вызове возвращает существующую ветку. Записывает ветку в
// side-реестр workspace и проставляет epic.git_branch на доске.
func (s *Server) handleCreateEpicBranch(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("ветки эпиков доступны только git-проектам (kind=%s)", inf.Kind))
		return
	}

	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()
	epic, err := store.GetEpic(r.Context(), epicID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "эпик не найден")
		return
	}

	branch := epicBranchPrefix + gitops.SanitizeBranchName(epicID)
	base := strings.TrimSpace(inf.GitBase)
	if base == "" {
		writeErr(w, http.StatusBadRequest, "у проекта не задана базовая ветка (git_base)")
		return
	}

	if err := ensureBranch(r.Context(), repo, branch, base); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.reg.SetEpicBranch(project, epicID, workspace.BranchRef{Branch: branch, Base: base}); err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка записи ветки в реестр: "+err.Error())
		return
	}
	if epic.GitBranch == "" {
		epic.GitBranch = branch
		if err := store.SaveEpic(r.Context(), epic); err != nil {
			writeErr(w, http.StatusInternalServerError, "сохранение эпика: "+err.Error())
			return
		}
	}

	logging.For(project).Infof("gitflow: эпик %s → ветка %s (база %s)", epicID, branch, base)
	s.kickBoard(project)
	writeJSON(w, http.StatusOK, map[string]string{"branch": branch, "base": base, "epic_id": epicID})
}

// handleCreateTaskBranch создаёт фича-ветку задачи от релизной ветки её эпика:
// git branch ai/task/<id> <epic_branch> без переключения. Требует, чтобы ветка
// эпика уже была создана (side-реестр). Идемпотентно.
func (s *Server) handleCreateTaskBranch(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("ветки задач доступны только git-проектам (kind=%s)", inf.Kind))
		return
	}

	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

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

	// Ветка задачи создаётся от релизной ветки родительского эпика.
	epicRef, err := s.reg.EpicBranch(project, task.EpicID)
	if err != nil {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("сначала создайте ветку эпика %s: %v", task.EpicID, err))
		return
	}

	branch := taskBranchPrefix + gitops.SanitizeBranchName(taskID)
	if err := ensureBranch(r.Context(), repo, branch, epicRef.Branch); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.reg.SetTaskBranch(project, taskID, workspace.BranchRef{Branch: branch, Base: epicRef.Branch}); err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка записи ветки в реестр: "+err.Error())
		return
	}
	if task.GitBranch == "" {
		task.GitBranch = branch
		if err := store.SaveTask(r.Context(), task); err != nil {
			writeErr(w, http.StatusInternalServerError, "сохранение задачи: "+err.Error())
			return
		}
	}

	logging.For(project).Infof("gitflow: задача %s → ветка %s (база %s)", taskID, branch, epicRef.Branch)
	s.kickBoard(project)
	writeJSON(w, http.StatusOK, map[string]string{"branch": branch, "base": epicRef.Branch, "task_id": taskID})
}

// ensureBranch создаёт ветку branch от base, если её ещё нет в клоне.
func ensureBranch(ctx context.Context, repo *gitops.Repo, branch, base string) error {
	exists, err := repo.BranchExists(ctx, branch)
	if err != nil {
		return fmt.Errorf("проверка ветки %s: %v", branch, err)
	}
	if exists {
		return nil
	}
	if err := repo.CreateBranch(ctx, branch, base); err != nil {
		return err
	}
	return nil
}

// handleMergeTask вливает фича-ветку задачи в релизную ветку её эпика
// (Ф-2): REST-путь для ручного/программного «мёрджа». Само слияние делает
// gitops.MergeFeature во временном worktree — рабочая копия клона не трогается.
//
// Ответы:
//   - 200 {status:"ok", branch, already_merged} — влита (или уже была влита);
//   - 409 {status:"conflicts", files:[...]} — merge-tree выявил конфликты
//     (вход для инструмента резолва Ф-4), ветки не тронуты;
//   - 400 — нет ветки эпика/задачи, проект не git и т.п.
func (s *Server) handleMergeTask(w http.ResponseWriter, r *http.Request) {
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

	res, err := s.mergeTaskBranch(r.Context(), project, task)
	if err != nil {
		var ce *gitops.MergeConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":  "conflicts",
				"files":   ce.Files,
				"message": ce.Error(),
			})
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	logging.For(project).Infof("gitflow: мёрдж задачи %s: %s (already=%v)",
		taskID, res.Message, res.AlreadyMerged)
	s.kickBoard(project)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"branch":         s.epicBranch(project, task),
		"message":        res.Message,
		"already_merged": res.AlreadyMerged,
	})
}

// handleReleaseEpic — кнопка «Залить в main» (Ф-3): вливает релизную ветку
// эпика в базовую (main) с быстрым путём без конфликтов. Эпик должен быть
// в статусе done (ручной затвор человека); сама операция необратима.
//
// Слияние выполняется через gitops.MergeFeature: прогноз конфликтов через
// merge-tree БЕЗ изменения рабочей копии, затем настоящий merge-коммит
// во временном worktree и push main на remote. Рабочая копия клона и HEAD
// агента (ai/<имя>) не трогаются.
//
// Ответы:
//   - 200 {status:"ok", branch:main, source, already_merged} — влита (или уже);
//   - 409 {status:"conflicts", files:[...]} — merge-tree выявил конфликты main
//     ↔ релизная ветка (вход инструмента резолва Ф-4), ветки не тронуты;
//   - 400 — эпик не done, нет ветки эпика, проект не git и т.п.
func (s *Server) handleReleaseEpic(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("«Залить в main» доступно только git-проектам (kind=%s)", inf.Kind))
		return
	}
	main := strings.TrimSpace(inf.GitBase)
	if main == "" {
		writeErr(w, http.StatusBadRequest, "у проекта не задана базовая ветка (git_base)")
		return
	}

	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	store, err := s.boardStore(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "доска недоступна: "+err.Error())
		return
	}
	defer store.Close()
	epic, err := store.GetEpic(r.Context(), epicID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "эпик не найден")
		return
	}
	if epic.Status != board.StatusDone {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("«Залить в main» доступен только эпику со статусом done (сейчас %s)", epic.Status))
		return
	}

	epicRef, err := s.reg.EpicBranch(project, epicID)
	if err != nil {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("сначала создайте ветку эпика %s: %v", epicID, err))
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	res, err := repo.MergeFeature(r.Context(), main, epicRef.Branch, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: релиз в main из %s", epicID, epicRef.Branch),
		PushURL: remotePushURL(inf),
	})
	if err != nil {
		var ce *gitops.MergeConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":  "conflicts",
				"files":   ce.Files,
				"message": ce.Error(),
			})
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	logging.For(project).Infof("gitflow: эпик %s → main: релизная ветка %s влита (already=%v)",
		epicID, epicRef.Branch, res.AlreadyMerged)
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Эпик %s: релизная ветка %s влита в main (%s)", epicID, epicRef.Branch, main),
			"", "", nil)
	}
	s.kickBoard(project)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"branch":         main,
		"source":         epicRef.Branch,
		"message":        res.Message,
		"already_merged": res.AlreadyMerged,
	})
}

// epicBranch возвращает имя релизной ветки эпика задачи из side-реестра
// (пустая строка, если ветки ещё нет).
func (s *Server) epicBranch(project string, task *board.Task) string {
	if task == nil {
		return ""
	}
	ref, err := s.reg.EpicBranch(project, task.EpicID)
	if err != nil {
		return ""
	}
	return ref.Branch
}

// mergeTaskBranch вливает ветку задачи в ветку её эпика (общая механика для
// REST-эндпоинта и авто-хука done→merge). Возвращает результат слияния или
// *gitops.MergeConflictError при конфликтах. Потокобезопасно: слияния проекта
// сериализуются пер-проектным локом.
func (s *Server) mergeTaskBranch(ctx context.Context, project string, t *board.Task) (*gitops.MergeResult, error) {
	if t == nil || t.EpicID == "" {
		return nil, fmt.Errorf("задача без эпика")
	}
	inf, err := s.reg.Get(project)
	if err != nil {
		return nil, fmt.Errorf("проект не найден: %v", err)
	}
	if inf.Kind != workspace.KindGit {
		return nil, fmt.Errorf("мёрдж веток доступен только git-проектам (kind=%s)", inf.Kind)
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return nil, err
	}
	epicRef, err := s.reg.EpicBranch(project, t.EpicID)
	if err != nil {
		return nil, fmt.Errorf("сначала создайте ветку эпика %s: %v", t.EpicID, err)
	}
	taskRef, err := s.reg.TaskBranch(project, t.TaskID)
	if err != nil {
		return nil, fmt.Errorf("сначала создайте ветку задачи %s: %v", t.TaskID, err)
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	return repo.MergeFeature(ctx, epicRef.Branch, taskRef.Branch, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("задача %s: влитие в релиз эпика %s", t.TaskID, t.EpicID),
		PushURL: remotePushURL(inf),
	})
}

// remotePushURL выбирает URL push релизной ветки: токенизированный HTTPS-URL
// для GitHub/GitLab (токен в URL), иначе сам remote. Пустая строка — локальный
// клон без remote (push не нужен).
func remotePushURL(inf workspace.Info) string {
	if inf.GitRemote == "" {
		return ""
	}
	if u := tokenPushURL(inf.GitRemote); u != "" {
		return u
	}
	return inf.GitRemote
}

// mergeLock возвращает пер-проектный мьютекс слияний (создаёт при отсутствии).
func (s *Server) mergeLock(project string) *sync.Mutex {
	s.syncMetaMu.Lock()
	defer s.syncMetaMu.Unlock()
	mu := s.mergeLocks[project]
	if mu == nil {
		mu = &sync.Mutex{}
		s.mergeLocks[project] = mu
	}
	return mu
}

// attachTaskDoneHook связывает авто-действие «done → мёрдж» с хранилищем
// доски: когда задача переходит в done, её ветка автоматически вливается в
// релизную ветку эпика (gitops.MergeFeature). Ошибки не ломают сам переход
// статуса — логируются; конфликты требуют инструмента резолва Ф-4.
func (s *Server) attachTaskDoneHook(project string, store *board.Store) {
	if store == nil {
		return
	}
	store.TaskDoneHook = func(ctx context.Context, task *board.Task, from board.Status) {
		inf, err := s.reg.Get(project)
		if err != nil || inf.Kind != workspace.KindGit {
			return // проект не git — веток нет, мёрджить нечего
		}
		if _, err := s.reg.EpicBranch(project, task.EpicID); err != nil {
			logging.For(project).Detailf("gitflow: авто-мёрдж %s: ветка эпика %s не создана, пропуск",
				task.TaskID, task.EpicID)
			return
		}
		res, err := s.mergeTaskBranch(ctx, project, task)
		if err != nil {
			var ce *gitops.MergeConflictError
			if errors.As(err, &ce) {
				logging.For(project).Warnf("gitflow: авто-мёрдж %s: конфликт в %s — требуется резолв (Ф-4)",
					task.TaskID, strings.Join(ce.Files, ", "))
				if sess := s.session(project); sess != nil {
					sess.kickBoard()
				}
				return
			}
			logging.For(project).Warnf("gitflow: авто-мёрдж %s: %v", task.TaskID, err)
			return
		}
		logging.For(project).Infof("gitflow: задача %s → done: авто-мёрдж в релиз эпика %s (already=%v)",
			task.TaskID, task.EpicID, res.AlreadyMerged)
		if sess := s.session(project); sess != nil {
			sess.kickBoard()
		}
	}
}

// deleteEpicBranches снимает из side-реестра ветки эпика и всех его задач
// (вызывается при удалении эпика с доски).
func (s *Server) deleteEpicBranches(project, epicID string, epicTasks []string) {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	if err := s.reg.DeleteEpicBranch(project, epicID); err != nil {
		logging.For(project).Warnf("gitflow: снятие ветки эпика %s: %v", epicID, err)
	}
	for _, tid := range epicTasks {
		if err := s.reg.DeleteTaskBranch(project, tid); err != nil {
			logging.For(project).Warnf("gitflow: снятие ветки задачи %s: %v", tid, err)
		}
	}
}
