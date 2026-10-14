// Git-workflow «эпик = релизная ветка, задача = фича-ветка» (Ф-1).
//
// REST-маршруты создания веток эпиков и задач внутри единого клона проекта
// (branch-in-place, temp/<имя>): git-команды идут через git.Repo, а
// привязка epic_id/task_id → ветка хранится в side-реестре workspace. Сама
// рабочая копия и текущая ветка НЕ переключаются (CreateBranch создаёт ref),
// поэтому прежний flow приёмки «Принять → MR» не ломается.
package server

import (
	"ai/agents/architect"
	"ai/application/gitflow"
	"ai/chat"
	"ai/infrastructure/board"
	"ai/infrastructure/git"
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
	epicBranchPrefix = gitflow.EpicBranchPrefix
)

// branchRegistry — адаптер gitflow.BranchRegistry поверх side-реестра
// workspace, привязанный к проекту (Этап 6, срез F3g).
type branchRegistry struct {
	reg     *workspace.Registry
	project string
}

func (a branchRegistry) SetEpicBranch(epicID string, ref gitflow.BranchRef) error {
	return a.reg.SetEpicBranch(a.project, epicID, workspace.BranchRef{Branch: ref.Branch, Base: ref.Base})
}

func (a branchRegistry) EpicBranch(epicID string) (gitflow.BranchRef, error) {
	ref, err := a.reg.EpicBranch(a.project, epicID)
	if err != nil {
		return gitflow.BranchRef{}, err
	}
	return gitflow.BranchRef{Branch: ref.Branch, Base: ref.Base}, nil
}

func (a branchRegistry) SetTaskBranch(taskID string, ref gitflow.BranchRef) error {
	return a.reg.SetTaskBranch(a.project, taskID, workspace.BranchRef{Branch: ref.Branch, Base: ref.Base})
}

func (a branchRegistry) DeleteEpicBranch(epicID string) error {
	return a.reg.DeleteEpicBranch(a.project, epicID)
}

func (a branchRegistry) DeleteTaskBranch(taskID string) error {
	return a.reg.DeleteTaskBranch(a.project, taskID)
}

func (a branchRegistry) DeleteEpicMR(epicID string) error {
	return a.reg.DeleteEpicMR(a.project, epicID)
}

func (a branchRegistry) DeleteTaskMR(taskID string) error {
	return a.reg.DeleteTaskMR(a.project, taskID)
}

// brancher собирает use-case управления ветками проекта: git-репозиторий,
// side-реестр, доска и инвалидация диффов (Ф-1).
func (s *Server) brancher(project string, store *board.Store, repo *git.Repo, base string) gitflow.Brancher {
	return gitflow.Brancher{
		Project:  project,
		Base:     base,
		Repo:     repo,
		Registry: branchRegistry{reg: s.reg, project: project},
		Store:    store,
		Changed:  func() { s.invalidateDiffs(project) },
	}
}

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

	base := strings.TrimSpace(inf.GitBase)
	if base == "" {
		writeErr(w, http.StatusBadRequest, "у проекта не задана базовая ветка (git_base)")
		return
	}

	ref, err := s.brancher(project, store, repo, base).CreateEpicBranch(r.Context(), epic)
	if err != nil {
		if errors.Is(err, gitflow.ErrBranchCreate) {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.srvEmitBoard(project, "gitflow: ветка эпика")
	writeJSON(w, http.StatusOK, map[string]string{"branch": ref.Branch, "base": ref.Base, "epic_id": epicID})
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
	ref, err := s.brancher(project, store, repo, strings.TrimSpace(inf.GitBase)).CreateTaskBranch(r.Context(), task)
	if err != nil {
		switch {
		case errors.Is(err, gitflow.ErrEpicBranchMissing):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, gitflow.ErrBranchCreate):
			writeErr(w, http.StatusBadGateway, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	s.srvEmitBoard(project, "gitflow: ветка задачи")
	writeJSON(w, http.StatusOK, map[string]string{"branch": ref.Branch, "base": ref.Base, "task_id": taskID})
}

// handleMergeTask вливает фича-ветку задачи в релизную ветку её эпика
// (Ф-2): REST-путь для ручного/программного «мёрджа». Само слияние делает
// git.MergeFeature во временном worktree — рабочая копия клона не трогается.
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
		var ce *git.MergeConflictError
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
// Слияние выполняется через git.MergeFeature: прогноз конфликтов через
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
func (s *Server) releaseEpic(ctx context.Context, project, epicID string) (*git.MergeResult, string, string, error) {
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
			msg:  fmt.Sprintf("у эпика %s нет релизной ветки — «Залить в main» невозможно (создайте ветку эпика %s)", epicID, epicBranchPrefix+git.SanitizeBranchName(epicID)),
		}
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	res, err := repo.MergeFeature(ctx, main, epicRef.Branch, git.MergeFeatureOptions{
		Message: fmt.Sprintf("эпик %s: релиз в main из %s", epicID, epicRef.Branch),
		PushURL: remotePushURL(inf),
	})
	if err != nil {
		var ce *git.MergeConflictError
		if errors.As(err, &ce) {
			return nil, "", "", &apiError{code: http.StatusConflict, msg: ce.Error(), mce: ce}
		}
		return nil, "", "", &apiError{code: http.StatusBadGateway, msg: err.Error()}
	}
	s.invalidateDiffs(project)
	// main изменился: переиндексировать его по ref и подтянуть рабочую копию
	// агентов. Иначе следующий эпик (и его анализ) видел бы в RAG и в файлах
	// код до этого релиза — см. server/ragref.go.
	//
	// Только когда ветка действительно слита: при already=true main не менялся,
	// и лишние git-вызовы/индексация ничего не дают (тест «уже слитая ветка не
	// должна мутировать git»). Рабочая копия подтягивается и на старте сессии —
	// см. syncAgentBranchAsync.
	if res.AlreadyMerged {
		s.refreshRagIndexBranch(project, main)
	} else {
		s.afterMainChanged(project, main)
	}
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

// mergeTaskHost — адаптер gitflow.MergeTaskHost поверх server (срез F3h).
type mergeTaskHost struct {
	s *Server
}

func (h mergeTaskHost) Project(project string) (gitflow.MergeProject, error) {
	inf, err := h.s.reg.Get(project)
	if err != nil {
		return gitflow.MergeProject{}, err
	}
	return gitflow.MergeProject{
		IsGit:   inf.Kind == workspace.KindGit,
		Kind:    string(inf.Kind),
		Base:    strings.TrimSpace(inf.GitBase),
		PushURL: remotePushURL(inf),
	}, nil
}

func (h mergeTaskHost) Repo(ctx context.Context, project string) (gitflow.MergeRepo, error) {
	return h.s.repoOf(ctx, project)
}

func (h mergeTaskHost) EpicBranch(project, epicID string) (gitflow.BranchRef, error) {
	ref, err := h.s.reg.EpicBranch(project, epicID)
	if err != nil {
		return gitflow.BranchRef{}, err
	}
	return gitflow.BranchRef{Branch: ref.Branch, Base: ref.Base}, nil
}

func (h mergeTaskHost) TaskBranch(project, taskID string) (gitflow.BranchRef, error) {
	ref, err := h.s.reg.TaskBranch(project, taskID)
	if err != nil {
		return gitflow.BranchRef{}, err
	}
	return gitflow.BranchRef{Branch: ref.Branch, Base: ref.Base}, nil
}

func (h mergeTaskHost) Locked(project string, fn func()) {
	lock := h.s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()
	fn()
}

func (h mergeTaskHost) DropEpicWorktreeLocked(ctx context.Context, project, epicID string) {
	h.s.dropEpicWorktreeLocked(ctx, project, epicID)
}

func (h mergeTaskHost) InvalidateDiffs(project string) { h.s.invalidateDiffs(project) }

// mergeTaskBranch вливает ветку задачи в ветку её эпика (общая механика для
// REST-эндпоинта и авто-хука done→merge). Политика — в
// application/gitflow.TaskMerger; адаптер — mergeTaskHost.
func (s *Server) mergeTaskBranch(ctx context.Context, project string, t *board.Task) (*git.MergeResult, error) {
	return gitflow.TaskMerger{Host: mergeTaskHost{s: s}}.MergeTaskBranch(ctx, project, t)
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

	// Ф-6 (инцидент FEL-04): до перехода в done — проверка, что работа реально
	// есть (свои коммиты в ветке задачи или правки в worktree). Иначе fallback
	// оркестратора закрывает пустую задачу, а статус done терминален.
	store.TaskDoneGuard = func(ctx context.Context, task *board.Task, from board.Status) error {
		return s.guardTaskDone(ctx, project, task)
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

	// Ф-6 (этап 2): State Tracking пишет состояние раунда точечными
	// обновлениями (PatchTask). Без этого снимок доски в UI обновлялся бы
	// только по смене статусов, а «чем занят агент» осталось бы невидимым
	// ровно тогда, когда это и нужно видеть.
	store.TaskStateHook = func(_ context.Context, task *board.Task) {
		s.srvEmitBoard(project, "состояние задачи: "+task.TaskID)
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

	if s.brancher(project, store, repo, base).AutoCreateEpicBranch(ctx, epic) {
		s.srvEmitBoard(project, "gitflow: авто-ветка эпика")
	}
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
	repo, err := s.repoOf(ctx, project)
	if err != nil {
		logging.For(project).Warnf("gitflow: авто-ветка задачи %s: %v", task.TaskID, err)
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	if s.brancher(project, store, repo, strings.TrimSpace(inf.GitBase)).AutoCreateTaskBranch(ctx, task) {
		s.srvEmitBoard(project, "gitflow: авто-ветка задачи")
	}
}

// deleteEpicBranches снимает из side-реестра ветки эпика и всех его задач
// (вызывается при удалении эпика с доски).
func (s *Server) deleteEpicBranches(project, epicID string, epicTasks []string) {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return
	}
	gitflow.Brancher{
		Project:  project,
		Registry: branchRegistry{reg: s.reg, project: project},
		Changed:  func() { s.invalidateDiffs(project) },
	}.RemoveEpicBranches(epicID, epicTasks)
}

// apiError — ошибка REST-хендлера с HTTP-кодом: единый носитель «каким кодом
// ответить» для общих ядер REST и мостов-инструментов ассистента (Ф-3).
// mce заполняется только при конфликте слияния — REST-потребитель отдаёт
// тогда status=conflicts с перечнем файлов (вход инструмента резолва Ф-4).
type apiError struct {
	code int
	msg  string
	mce  *git.MergeConflictError
}

func (e *apiError) Error() string { return e.msg }
