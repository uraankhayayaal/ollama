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

// boardStore открывает хранилище доски проекта, привязывая авто-действия
// git-workflow (Ф-1..Ф-4): создание веток, мёрдж done→релиз, worktree задачи,
// синхрон релизной ветки с main.
func (s *Server) boardStore(ctx context.Context, project string) (*board.Store, error) {
	store, err := board.NewStore(ctx, architect.LoadConfig().StoreConfig(project))
	if err != nil {
		return nil, err
	}
	s.attachGitHooks(project, store)
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
	s.invalidateDiffs(project)
	if epic.GitBranch == "" {
		epic.GitBranch = branch
		if err := store.SaveEpic(r.Context(), epic); err != nil {
			writeErr(w, http.StatusInternalServerError, "сохранение эпика: "+err.Error())
			return
		}
	}

	logging.For(project).Infof("gitflow: эпик %s → ветка %s (база %s)", epicID, branch, base)
	s.srvEmitBoard(project, "gitflow: ветка эпика")
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
	s.invalidateDiffs(project)
	if task.GitBranch == "" {
		task.GitBranch = branch
		if err := store.SaveTask(r.Context(), task); err != nil {
			writeErr(w, http.StatusInternalServerError, "сохранение задачи: "+err.Error())
			return
		}
	}

	logging.For(project).Infof("gitflow: задача %s → ветка %s (база %s)", taskID, branch, epicRef.Branch)
	s.srvEmitBoard(project, "gitflow: ветка задачи")
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
			// Конфликт на доске: задача помечается списком файлов (виден в UI,
			// снимется успешным мёрджем).
			task.MergeConflictFiles = ce.Files
			if serr := store.SaveTask(r.Context(), task); serr != nil {
				logging.For(project).Warnf("gitflow: мёрдж %s: запись конфликта: %v", taskID, serr)
			}
			s.srvEmitBoard(project, "gitflow: мёрдж задачи — конфликт")
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
	if len(task.MergeConflictFiles) > 0 {
		task.MergeConflictFiles = nil
		if serr := store.SaveTask(r.Context(), task); serr != nil {
			logging.For(project).Warnf("gitflow: мёрдж %s: очистка конфликта: %v", taskID, serr)
		}
	}
	s.srvEmitBoard(project, "gitflow: мёрдж задачи")
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

	res, source, main, err := s.releaseEpic(r.Context(), project, epicID)
	if err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ae.mce != nil {
			// Конфликт на доске: эпик помечается списком файлов (бейдж в
			// EpicModal, снимется успешным релизом/резолвом) — раньше признак
			// ставили только авто-синхрон и резолв, и ручной «Залить в main»
			// отдавал 409 с файлами, но доска оставалась пустой.
			s.setEpicMergeConflict(r.Context(), project, epicID, ae.mce.Files)
			s.srvEmitBoard(project, "gitflow: релиз эпика — конфликт")
			if sess := s.session(project); sess != nil {
				sess.append(chat.RoleStatus,
					fmt.Sprintf("Эпик %s: релиз в main упёрся в конфликт в файлах [%s]. Доступен флоу rebase/резолв (ResolveGitConflicts).",
						epicID, strings.Join(ae.mce.Files, ", ")),
					"", "", nil)
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"status":  "conflicts",
				"files":   ae.mce.Files,
				"message": ae.mce.Error(),
			})
			return
		}
		writeErr(w, ae.code, ae.msg)
		return
	}

	logging.For(project).Infof("gitflow: эпик %s → main: релизная ветка %s влита (already=%v)",
		epicID, source, res.AlreadyMerged)
	s.clearEpicMergeConflict(r.Context(), project, epicID)
	s.setEpicMergedIntoMain(r.Context(), project, epicID, true)
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Эпик %s: релизная ветку %s влита в main (%s)", epicID, source, main),
			"", "", nil)
	}
	s.srvEmitBoard(project, "gitflow: релиз эпика в main")
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"branch":         main,
		"source":         source,
		"message":        res.Message,
		"already_merged": res.AlreadyMerged,
	})
}

// releaseEpic вливает релизную ветку эпика в базовую (main) — общая механика
// REST-эндпоинта «Залить в main» и моста-инструмента ассистента EpicRelease
// (Ф-3). Ошибки возвращаются с HTTP-кодом (apiError), чтобы оба потребителя
// отвечали одинаково; конфликт слияния дополнительно несёт *MergeConflictError
// для ответа status=conflicts с файлами.
func (s *Server) releaseEpic(ctx context.Context, project, epicID string) (*gitops.MergeResult, string, string, error) {
	inf, err := s.reg.Get(project)
	if err != nil {
		return nil, "", "", &apiError{code: http.StatusNotFound, msg: "проект не найден"}
	}
	if inf.Kind != workspace.KindGit {
		return nil, "", "", &apiError{
			code: http.StatusBadRequest,
			msg:  fmt.Sprintf("«Залить в main» доступно только git-проектам (kind=%s)", inf.Kind),
		}
	}
	main := strings.TrimSpace(inf.GitBase)
	if main == "" {
		return nil, "", "", &apiError{code: http.StatusBadRequest, msg: "у проекта не задана базовая ветка (git_base)"}
	}

	repo, err := s.repoOf(ctx, project)
	if err != nil {
		return nil, "", "", &apiError{code: http.StatusBadRequest, msg: err.Error()}
	}

	store, err := s.boardStore(ctx, project)
	if err != nil {
		return nil, "", "", &apiError{code: http.StatusServiceUnavailable, msg: "доска недоступна: " + err.Error()}
	}
	defer store.Close()
	epic, err := store.GetEpic(ctx, epicID)
	if err != nil {
		return nil, "", "", &apiError{code: http.StatusNotFound, msg: "эпик не найден"}
	}
	if epic.Status != board.StatusDone {
		return nil, "", "", &apiError{
			code: http.StatusBadRequest,
			msg:  fmt.Sprintf("«Залить в main» доступен только эпику со статусом done (сейчас %s)", epic.Status),
		}
	}

	epicRef, err := s.reg.EpicBranch(project, epicID)
	if err != nil {
		return nil, "", "", &apiError{
			code: http.StatusBadRequest,
			msg:  fmt.Sprintf("у эпика %s нет релизной ветки — «Залить в main» невозможно (создайте ветку эпика %s)", epicID, epicBranchPrefix+gitops.SanitizeBranchName(epicID)),
		}
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	res, err := repo.MergeFeature(ctx, main, epicRef.Branch, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: релиз в main из %s", epicID, epicRef.Branch),
		PushURL: remotePushURL(inf),
	})
	if err != nil {
		var ce *gitops.MergeConflictError
		if errors.As(err, &ce) {
			return nil, "", "", &apiError{code: http.StatusConflict, msg: ce.Error(), mce: ce}
		}
		return nil, "", "", &apiError{code: http.StatusBadGateway, msg: err.Error()}
	}
	s.invalidateDiffs(project)
	return res, epicRef.Branch, main, nil
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

	res, err := repo.MergeFeature(ctx, epicRef.Branch, taskRef.Branch, gitops.MergeFeatureOptions{
		Message: fmt.Sprintf("задача %s: влитие в релиз эпика %s", t.TaskID, t.EpicID),
		PushURL: remotePushURL(inf),
	})
	if err == nil {
		s.invalidateDiffs(project)
	}
	return res, err
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

// attachGitHooks связывает авто-действия git-workflow с хранилищем доски:
//
//   - EpicCreatedHook — авто-создание релизной ветки эпика (от git_base, Ф-1);
//   - TaskCreatedHook — авто-создание фича-ветки задачи (от ветки её эпика, Ф-1);
//   - TaskInProgressHook — worktree ветки задачи, в который специалист получает
//     OutputDir (Ф-3);
//   - TaskDoneHook — авто-коммит worktree + авто-мёрдж done→релиз + авто-MR
//     задачи (Ф-2/Ф-3);
//   - EpicDoneHook — авто-синхрон релизной ветки эпика с main (Ф-4).
//
// Все ошибки НЕ ломают переход статуса/сохранение записи: логируются, на доске
// остаются ручные кнопки (ветка/MR/мёрдж) как страховка от сбоев.
func (s *Server) attachGitHooks(project string, store *board.Store) {
	if store == nil {
		return
	}

	// Ф-2/Ф-3: задача перешла в done — авто-коммит worktree, авто-мёрдж её ветки
	// в релизную ветку эпика и авто-MR задачи.
	store.TaskDoneHook = func(ctx context.Context, task *board.Task, from board.Status) {
		s.autoCommitAndMergeTask(ctx, project, task, store)
	}

	// Ф-1: эпик добавлен на доску — авто-создание релизной ветки.
	store.EpicCreatedHook = func(ctx context.Context, epic *board.Epic) {
		s.autoCreateEpicBranch(ctx, project, epic, store)
	}

	// Ф-1: задача добавлена на доску — авто-создание фича-ветки (база — ветка
	// эпика). Требует уже существующей ветки эпика (появляется через
	// EpicCreatedHook или ручной кнопкой).
	store.TaskCreatedHook = func(ctx context.Context, task *board.Task) {
		s.autoCreateTaskBranch(ctx, project, task, store)
	}

	// Ф-3: задача пошла «в работу» — worktree её ветки для специалиста.
	store.TaskInProgressHook = func(ctx context.Context, task *board.Task, _ board.Status) {
		s.taskWorktree(ctx, project, task, store)
	}

	// Ф-4: эпик переведён в done — авто-синхрон релизной ветки с main.
	store.EpicDoneHook = func(ctx context.Context, epic *board.Epic, _ board.Status) {
		s.syncEpicWithMain(ctx, project, epic)
	}
}

// autoCreateEpicBranch — Ф-1: авто-создание релизной ветки эпика при добавлении
// эпика на доску git-проекта. База — git_base (main). Идемпотентно; ошибки
// не ломают сохранение эпика — логируются, на доске остаётся кнопка
// «Создать ветку эпика» как ручная страховка.
func (s *Server) autoCreateEpicBranch(ctx context.Context, project string, epic *board.Epic, store *board.Store) {
	if epic == nil || epic.TaskID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	base := strings.TrimSpace(inf.GitBase)
	if base == "" {
		logging.For(project).Detailf("gitflow: авто-ветка эпика %s: у проекта нет git_base — пропуск", epic.TaskID)
		return
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		logging.For(project).Warnf("gitflow: авто-ветка эпика %s: %v", epic.TaskID, err)
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	branch := epicBranchPrefix + gitops.SanitizeBranchName(epic.TaskID)
	if err := ensureBranch(ctx, repo, branch, base); err != nil {
		logging.For(project).Warnf("gitflow: авто-ветка эпика %s: %v", epic.TaskID, err)
		return
	}
	if err := s.reg.SetEpicBranch(project, epic.TaskID, workspace.BranchRef{Branch: branch, Base: base}); err != nil {
		logging.For(project).Warnf("gitflow: авто-ветка эпика %s: реестр: %v", epic.TaskID, err)
		return
	}
	s.invalidateDiffs(project)
	if epic.GitBranch == "" {
		epic.GitBranch = branch
		if err := store.SaveEpic(ctx, epic); err != nil {
			logging.For(project).Warnf("gitflow: авто-ветка эпика %s: сохранение git_branch: %v", epic.TaskID, err)
		}
	}
	logging.For(project).Infof("gitflow: эпик %s → авто-ветка %s (база %s)", epic.TaskID, branch, base)
	s.srvEmitBoard(project, "gitflow: авто-ветка эпика")
}

// autoCreateTaskBranch — Ф-1: авто-создание фича-ветки задачи при её добавлении
// на доску git-проекта. База — релизная ветка эпика задачи (должна уже
// существовать в side-реестре). Идемпотентно; ошибки не ломают сохранение
// задачи — логируются, на доске остаётся кнопка «Создать ветку задачи».
func (s *Server) autoCreateTaskBranch(ctx context.Context, project string, task *board.Task, store *board.Store) {
	if task == nil || task.TaskID == "" || task.EpicID == "" {
		return
	}
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	epicRef, err := s.reg.EpicBranch(project, task.EpicID)
	if err != nil {
		// Ветки эпика ещё нет (не-git/не создана) — авто-ветка задачи невозможна.
		// Кнопка «Создать ветку задачи» остаётся ручной страховкой.
		logging.For(project).Detailf("gitflow: авто-ветка задачи %s: ветка эпика %s отсутствует: %v",
			task.TaskID, task.EpicID, err)
		return
	}
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		logging.For(project).Warnf("gitflow: авто-ветка задачи %s: %v", task.TaskID, err)
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	branch := taskBranchPrefix + gitops.SanitizeBranchName(task.TaskID)
	if err := ensureBranch(ctx, repo, branch, epicRef.Branch); err != nil {
		logging.For(project).Warnf("gitflow: авто-ветка задачи %s: %v", task.TaskID, err)
		return
	}
	if err := s.reg.SetTaskBranch(project, task.TaskID, workspace.BranchRef{Branch: branch, Base: epicRef.Branch}); err != nil {
		logging.For(project).Warnf("gitflow: авто-ветка задачи %s: реестр: %v", task.TaskID, err)
		return
	}
	s.invalidateDiffs(project)
	if task.GitBranch == "" {
		task.GitBranch = branch
		if err := store.SaveTask(ctx, task); err != nil {
			logging.For(project).Warnf("gitflow: авто-ветка задачи %s: сохранение git_branch: %v", task.TaskID, err)
		}
	}
	logging.For(project).Infof("gitflow: задача %s → авто-ветка %s (база %s)", task.TaskID, branch, epicRef.Branch)
	s.srvEmitBoard(project, "gitflow: авто-ветка задачи")
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
	if err := s.reg.DeleteEpicMR(project, epicID); err != nil {
		logging.For(project).Warnf("gitflow: снятие MR эпика %s: %v", epicID, err)
	}
	for _, tid := range epicTasks {
		if err := s.reg.DeleteTaskBranch(project, tid); err != nil {
			logging.For(project).Warnf("gitflow: снятие ветки задачи %s: %v", tid, err)
		}
		if err := s.reg.DeleteTaskMR(project, tid); err != nil {
			logging.For(project).Warnf("gitflow: снятие MR задачи %s: %v", tid, err)
		}
	}
	s.invalidateDiffs(project)
}

// apiError — ошибка REST-хендлера с HTTP-кодом: единый носитель «каким кодом
// ответить» для общих ядер REST и мостов-инструментов ассистента (Ф-3).
// mce заполняется только при конфликте слияния — REST-потребитель отдаёт
// тогда status=conflicts с перечнем файлов (вход инструмента резолва Ф-4).
type apiError struct {
	code int
	msg  string
	mce  *gitops.MergeConflictError
}

func (e *apiError) Error() string { return e.msg }
