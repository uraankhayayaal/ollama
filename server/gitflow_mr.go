// MR/PR в git-workflow эпиков и задач (Ф-5): кнопка «Создать MR» в модалках
// доски + git-статус (ветки/MR) в снимке доски.
//
// MR эпика: релизная ветка a#/epic/<id> → main (релиз на ревью человеку).
// MR задачи: фича-ветка ai/task/<id> → ветка эпика. Кнопка дополняет прежний
// flow через git merge (server/gitflow.go) и не меняет его: источником правды
// о существовании MR служит side-реестр workspace (GitMergeRequests), который
// уточняется фоновой сверкой с форджем (опциональный интерфейс
// forges.MRStatusProvider) при открытии дашборда.
package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai/board"
	"ai/chat"
	"ai/forges"
	"ai/gitops"
	"ai/logging"
	"ai/workspace"
)

// gitLinkView — состояние «ветка + MR» одного эпика/задачи в снимке доски.
type gitLinkView struct {
	Branch    string `json:"branch,omitempty"`     // имя ветки (ai/epic/… или ai/task/…)
	BranchURL string `json:"branch_url,omitempty"` // web-ссылка на ветку на хостинге
	Target    string `json:"target,omitempty"`     // ветка, в которую вливается MR (main/ветка эпика)
	MRURL     string `json:"mr_url,omitempty"`     // ссылка на MR/PR (если создан)
	MRState   string `json:"mr_state,omitempty"`   // open|merged|closed|"" (неизвестно)
	// HasCommits — в ветке есть свои коммиты (Ф-2). nil — не вычислено/ошибка
	// git; false — коммитов ещё нет (кнопка «Создать MR» на фронте прячется).
	HasCommits *bool `json:"has_commits,omitempty"`
}

// gitView — git-статус всего проекта в снимке доски (заполняется только для
// git-проектов с ветками эпиков/задач). Модалки доски читают его, чтобы
// показать ссылки на ветки и MR и состояние кнопки «Создать MR».
type gitView struct {
	Base    string                 `json:"base,omitempty"`     // базовая ветка (main)
	BaseURL string                 `json:"base_url,omitempty"` // web-ссылка на базовую ветку
	Epics   map[string]gitLinkView `json:"epics,omitempty"`    // epic_id → ветка/MR
	Tasks   map[string]gitLinkView `json:"tasks,omitempty"`    // task_id → ветка/MR
}

// handleCreateEpicMR — «Создать MR» эпика: пушит релизную ветку ai/epic/<id>
// в remote и открывает MR → main через фордж. Ссылку хранит в side-реестре.
// Идемпотентно: если MR уже зарегистрирован — возвращает его ссылку.
func (s *Server) handleCreateEpicMR(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	epicID := r.PathValue("eid")

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("MR доступен только git-проектам (kind=%s)", inf.Kind))
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

	epicRef, err := s.reg.EpicBranch(project, epicID)
	if err != nil {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("сначала создайте ветку эпика %s: %v", epicID, err))
		return
	}
	// Уже есть MR — возвращаем существующий (идемпотентно).
	if mr, merr := s.reg.EpicMR(project, epicID); merr == nil && mr.URL != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"epic_id": epicID, "mr_url": mr.URL, "source": mr.Source, "target": mr.Target,
		})
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()

	repo := s.repoForBranch(inf, epicRef.Branch, epicRef.Base)
	if err := s.pushRepo(r.Context(), repo, inf.GitRemote); err != nil {
		writeErr(w, http.StatusBadGateway, "push ветки "+epicRef.Branch+": "+err.Error())
		return
	}
	mrURL, err := s.createMR(r.Context(), project, inf, forges.MergeRequestOptions{
		SourceBranch: epicRef.Branch,
		TargetBranch: epicRef.Base,
		Title:        fmt.Sprintf("Эпик %s: %s (релиз в %s)", epicID, epic.Title, epicRef.Base),
		Description:  epic.Description,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "создание MR эпика: "+err.Error())
		return
	}
	if err := s.reg.SetEpicMR(project, epicID, workspace.MRRef{
		URL: mrURL, Source: epicRef.Branch, Target: epicRef.Base, State: "open",
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка записи MR в реестр: "+err.Error())
		return
	}

	logging.For(project).Infof("gitflow: эпик %s → MR %s (%s → %s)", epicID, mrURL, epicRef.Branch, epicRef.Base)
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus, "Создан MR эпика: "+mrURL, "", "", nil)
	}
	s.srvEmitBoard(project, "gitflow: создан MR эпика")
	writeJSON(w, http.StatusOK, map[string]any{
		"epic_id": epicID, "mr_url": mrURL, "source": epicRef.Branch, "target": epicRef.Base,
	})
}

// handleCreateTaskMR — «Создать MR» задачи: пушит фича-ветку ai/task/<id> в
// remote и открывает MR → ветку эпика. Идемпотентно.
func (s *Server) handleCreateTaskMR(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	taskID := r.PathValue("tid")

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("MR доступен только git-проектам (kind=%s)", inf.Kind))
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

	taskRef, err := s.reg.TaskBranch(project, taskID)
	if err != nil {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("сначала создайте ветку задачи %s: %v", taskID, err))
		return
	}
	// Уже есть MR — возвращаем существующий (идемпотентно).
	if mr, merr := s.reg.TaskMR(project, taskID); merr == nil && mr.URL != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"task_id": taskID, "mr_url": mr.URL, "source": mr.Source, "target": mr.Target,
		})
		return
	}

	lock := s.mergeLock(project)
	lock.Lock()
	mrURL, err := s.createTaskMRLocked(r.Context(), project, inf, taskID, task, taskRef)
	lock.Unlock()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "создание MR задачи: "+err.Error())
		return
	}

	logging.For(project).Infof("gitflow: задача %s → MR %s (%s → %s)", taskID, mrURL, taskRef.Branch, taskRef.Base)
	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus, "Создан MR задачи: "+mrURL, "", "", nil)
	}
	s.srvEmitBoard(project, "gitflow: создан MR задачи")
	writeJSON(w, http.StatusOK, map[string]any{
		"task_id": taskID, "mr_url": mrURL, "source": taskRef.Branch, "target": taskRef.Base,
	})
}

// createTaskMRLocked открывает MR задачи → ветку эпика. Выполняется под
// mergeLock проекта (вызывающий удерживает его): база MR должна существовать в
// remote, ветка задачи пушится, MR создаётся и пишется в side-реестр.
func (s *Server) createTaskMRLocked(ctx context.Context, project string, inf workspace.Info, taskID string, task *board.Task, taskRef workspace.BranchRef) (string, error) {
	// База MR (ветка эпика) должна существовать в remote: GitHub/GitLab
	// отклоняют MR с неизвестной base (422 "base invalid"), а ветка эпика
	// до первого MR задачи живёт только в локальном клоне.
	if err := s.ensureRemoteBase(ctx, inf, taskRef.Base); err != nil {
		return "", fmt.Errorf("подготовка базы MR: %w", err)
	}
	repo := s.repoForBranch(inf, taskRef.Branch, taskRef.Base)
	if err := s.pushRepo(ctx, repo, inf.GitRemote); err != nil {
		return "", fmt.Errorf("push ветки %s: %w", taskRef.Branch, err)
	}
	mrURL, err := s.createMR(ctx, project, inf, forges.MergeRequestOptions{
		SourceBranch: taskRef.Branch,
		TargetBranch: taskRef.Base,
		Title:        fmt.Sprintf("Задача %s: %s", taskID, task.Title),
		Description:  task.Description,
	})
	if err != nil {
		return "", fmt.Errorf("создание MR задачи: %w", err)
	}
	if err := s.reg.SetTaskMR(project, taskID, workspace.MRRef{
		URL: mrURL, Source: taskRef.Branch, Target: taskRef.Base, State: "open",
	}); err != nil {
		return "", fmt.Errorf("ошибка записи MR в реестр: %w", err)
	}
	return mrURL, nil
}

// createTaskMROnce создаёт MR задачи вне mergeLock — авто-шаг done (Ф-3).
// MR, созданный вне UI, уже виден в реестре — no-op. Ветки задачи ещё нет —
// тоже no-op (создавать MR не из чего). Возвращает (url, создан ли, ошибка).
func (s *Server) createTaskMROnce(ctx context.Context, project, taskID string, task *board.Task) (string, bool, error) {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit || inf.GitRemote == "" {
		return "", false, nil
	}
	// Без токена форджа MR не создать (API GitHub/GitLab требует авторизацию) —
	// авто-шаг пропускается тихо: мёрдж в релизную ветку уже совершён,
	// ручная кнопка «Создать MR» остаётся страховкой.
	if gitToken(inf.GitRemote) == "" {
		return "", false, nil
	}
	if mr, merr := s.reg.TaskMR(project, taskID); merr == nil && mr.URL != "" {
		return mr.URL, false, nil
	}
	taskRef, err := s.reg.TaskBranch(project, taskID)
	if err != nil {
		return "", false, nil
	}
	lock := s.mergeLock(project)
	lock.Lock()
	defer lock.Unlock()
	mrURL, err := s.createTaskMRLocked(ctx, project, inf, taskID, task, taskRef)
	if err != nil {
		return "", false, err
	}
	return mrURL, true, nil
}

// createMR открывает MR/PR через фордж проекта (общая механика эпика/задачи).
func (s *Server) createMR(ctx context.Context, project string, inf workspace.Info, opts forges.MergeRequestOptions) (string, error) {
	forge, err := s.forgeFactory(inf.GitRemote, gitToken(inf.GitRemote))
	if err != nil {
		return "", err
	}
	url, err := forge.CreateMergeRequest(opts)
	if err != nil {
		return "", err
	}
	return url, nil
}

// repoForBranch собирает gitops.Repo для произвольной ветки клона (а не
// проектной фича-ветки): нужен для push веток эпиков/задач перед MR.
func (s *Server) repoForBranch(inf workspace.Info, branch, base string) *gitops.Repo {
	return gitops.RepoFromState(s.gitExec, inf.Root, inf.GitRemote, branch, base)
}

// ensureRemoteBase гарантирует, что базовая ветка (target MR) существует в
// remote: GitHub/GitLab отклоняют MR с несуществующей base (GitHub — 422
// {"field":"base","code":"invalid"}). Ветки эпиков создаются локально
// (git branch без push) и до первого MR задачи в remote не попадают — если
// там её нет, пушим локальную копию. Проверка идёт через git ls-remote
// (токен в URL для HTTPS, как в pushRepo, чтобы не требовался credential
// helper). Пустая base или локальный проект без remote — no-op.
func (s *Server) ensureRemoteBase(ctx context.Context, inf workspace.Info, base string) error {
	if strings.TrimSpace(base) == "" || inf.GitRemote == "" {
		return nil
	}
	target := "origin"
	if u := tokenPushURL(inf.GitRemote); u != "" {
		target = u
	}
	out, err := s.gitExec.Exec(ctx, inf.Root, "git", "ls-remote", "--heads", target, base)
	if err != nil {
		return fmt.Errorf("проверка ветки %s в remote: %w", base, err)
	}
	if strings.TrimSpace(out) != "" {
		return nil // ветка уже есть в remote
	}
	repo := s.repoForBranch(inf, base, "")
	if err := s.pushRepo(ctx, repo, inf.GitRemote); err != nil {
		return fmt.Errorf("push базовой ветки %s: %w", base, err)
	}
	return nil
}

// gitStatus собирает «ветки + MR» эпиков и задач из side-реестров workspace
// для снимка доски. Быстрый путь (без сети): только локальные реестры; признак
// «есть коммиты» (has_commits) считается локально через gitops.CountCommits
// (ошибка git — nil, кнопка MR остаётся). nil — проект не git или ветки не
// создавались.
func (s *Server) gitStatus(ctx context.Context, project string, epics []*board.Epic, tasks []*board.Task) *gitView {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return nil
	}
	repo, _ := s.repoOf(ctx, project)
	count := func(base, branch string) *bool {
		if repo == nil || base == "" || branch == "" {
			return nil
		}
		n, cerr := repo.CountCommits(ctx, base, branch)
		if cerr != nil {
			return nil
		}
		v := n > 0
		return &v
	}
	v := &gitView{
		Base:    inf.GitBase,
		BaseURL: branchWebURL(inf.GitRemote, inf.GitBase),
		Epics:   make(map[string]gitLinkView, len(epics)),
		Tasks:   make(map[string]gitLinkView, len(tasks)),
	}
	for _, e := range epics {
		ref, rerr := s.reg.EpicBranch(project, e.TaskID)
		if rerr != nil {
			continue
		}
		lv := gitLinkView{
			Branch:     ref.Branch,
			BranchURL:  branchWebURL(inf.GitRemote, ref.Branch),
			Target:     ref.Base,
			HasCommits: count(ref.Base, ref.Branch),
		}
		if mr, merr := s.reg.EpicMR(project, e.TaskID); merr == nil {
			lv.MRURL, lv.MRState = mr.URL, mr.State
		}
		v.Epics[e.TaskID] = lv
	}
	for _, t := range tasks {
		ref, rerr := s.reg.TaskBranch(project, t.TaskID)
		if rerr != nil {
			continue
		}
		lv := gitLinkView{
			Branch:     ref.Branch,
			BranchURL:  branchWebURL(inf.GitRemote, ref.Branch),
			Target:     ref.Base,
			HasCommits: count(ref.Base, ref.Branch),
		}
		if mr, merr := s.reg.TaskMR(project, t.TaskID); merr == nil {
			lv.MRURL, lv.MRState = mr.URL, mr.State
		}
		v.Tasks[t.TaskID] = lv
	}
	return v
}

// branchWebURL строит web-ссылку на ветку в интерфейсе хостинга по git-remote:
//
//	github: https://github.com/o/r/tree/<branch>
//	gitlab: https://gitlab.com/o/r/-/tree/<branch>
//
// SSH-remote (git@host:o/r.git) превращается в https. Пустая строка — remote
// не распознан или ветка пустая.
func branchWebURL(remote, branch string) string {
	remote = strings.TrimSpace(remote)
	branch = strings.TrimSpace(branch)
	if remote == "" || branch == "" {
		return ""
	}
	scheme := "https"
	host, path := "", ""
	if strings.HasPrefix(remote, "git@") {
		scp := strings.TrimPrefix(remote, "git@")
		if i := strings.IndexByte(scp, ':'); i > 0 {
			host = scp[:i]
			path = strings.TrimSuffix(scp[i+1:], ".git")
		}
	} else if u, err := url.Parse(remote); err == nil && u.Host != "" {
		if u.Scheme != "" {
			scheme = u.Scheme
		}
		host = u.Host
		path = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	}
	if host == "" || path == "" {
		return ""
	}
	sep := "/tree/"
	if strings.Contains(strings.ToLower(host), "gitlab") {
		sep = "/-/tree/"
	}
	// Ветка с "/" (ai/epic/…) в web-ссылке остаётся литеральной (GitHub/GitLab
	// принимают оба варианта), поэтому %2F обратно заменяем на "/".
	esc := strings.ReplaceAll(url.PathEscape(branch), "%2F", "/")
	return scheme + "://" + host + "/" + path + sep + esc
}

// reconcileMRs уточняет side-реестр MR сверкой с форджем (Ф-5, Гибрид):
// фордж, поддерживающий forges.MRStatusProvider, опрашивается по каждой
// ветке эпика/задачи. Находит MR, созданные вне UI, и обновляет состояние
// (open/merged/closed) у отслеживаемых. Возвращает true, если реестр
// изменился — вызывающему стоит перепубликовать снимок доски.
func (s *Server) reconcileMRs(ctx context.Context, project string) (bool, error) {
	inf, err := s.reg.Get(project)
	if err != nil || inf.Kind != workspace.KindGit {
		return false, nil
	}
	// Локальный проект без remote — фордж не нужен (MR всё равно не создать).
	if inf.GitRemote == "" {
		return false, nil
	}
	forge, err := s.forgeFactory(inf.GitRemote, gitToken(inf.GitRemote))
	if err != nil {
		return false, err
	}
	prov, ok := forge.(forges.MRStatusProvider)
	if !ok {
		return false, nil // фордж не умеет искать MR по ветке
	}

	changed := false
	target := inf.GitTarget
	if target == "" {
		target = inf.GitBase
	}
	if inf.GitBranch != "" && target != "" && reconcileOne(project, prov, inf.GitBranch, target, project,
		func(_ string, mr workspace.MRRef) error { return s.reg.SetProjectMR(project, mr) },
		func(_ string) (workspace.MRRef, error) { return s.reg.ProjectMR(project) }) {
		changed = true
	}
	if inf.GitBranches != nil {
		for epicID, ref := range inf.GitBranches.Epics {
			set := reconcileOne(project, prov, ref.Branch, ref.Base, epicID,
				func(id string, mr workspace.MRRef) error { return s.reg.SetEpicMR(project, id, mr) },
				func(id string) (workspace.MRRef, error) { return s.reg.EpicMR(project, id) })
			if set {
				changed = true
			}
		}
		for taskID, ref := range inf.GitBranches.Tasks {
			set := reconcileOne(project, prov, ref.Branch, ref.Base, taskID,
				func(id string, mr workspace.MRRef) error { return s.reg.SetTaskMR(project, id, mr) },
				func(id string) (workspace.MRRef, error) { return s.reg.TaskMR(project, id) })
			if set {
				changed = true
			}
		}
	}
	if inf.Parent == "" {
		for _, child := range s.repositoriesForProject(project) {
			if child == project {
				continue
			}
			childChanged, err := s.reconcileMRs(ctx, child)
			if err != nil {
				logging.For(project).Detailf("gitflow: сверка MR сабмодуля %s: %v", child, err)
			}
			changed = changed || childChanged
		}
	}
	return changed, nil
}

// reconcileOne сверяет один элемент (эпик/задачу): опрашивает фордж и
// записывает MR, если по ветке он есть, а в реестре его ещё нет или состояние
// изменилось. Возвращает true, если реестр обновлён. Ошибки сети и «нет MR»
// реестр не трогают (локальный статус остаётся источником).
func reconcileOne(
	project string, prov forges.MRStatusProvider,
	source, target, id string,
	setMR func(id string, mr workspace.MRRef) error,
	getMR func(id string) (workspace.MRRef, error),
) bool {
	info, err := prov.FindMergeRequest(source, target)
	if err != nil {
		if err != forges.ErrNoMergeRequest {
			logging.For(project).Detailf("gitflow: сверка MR %s: %v", id, err)
		}
		return false
	}
	if existing, gerr := getMR(id); gerr == nil && existing.URL == info.URL && existing.State == info.State {
		return false // уже актуально
	}
	if err := setMR(id, workspace.MRRef{
		URL: info.URL, Source: source, Target: target, State: info.State,
	}); err != nil {
		logging.For(project).Warnf("gitflow: запись MR %s: %v", id, err)
		return false
	}
	return true
}

// reconcileMRsAsync запускает фоновую сверку MR с форджем (гибрид: быстрый
// локальный статус сразу, сетевой опрос — после). При изменении реестра
// перепубликовывает доску в WS. Вызывается при открытии дашборда.
func (s *Server) reconcileMRsAsync(project string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	go func() {
		defer cancel()
		changed, err := s.reconcileMRs(ctx, project)
		if err != nil {
			logging.For(project).Detailf("gitflow: фоновая сверка MR: %v", err)
			return
		}
		if changed {
			logging.For(project).Infof("gitflow: фоновая сверка MR обновила статусы")
			if sess := s.session(project); sess != nil {
				sess.emitBoard("gitflow: фоновая сверка MR обновила статусы")
			}
		}
	}()
}
