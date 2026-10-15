package server

import (
	"ai/chat"
	"ai/forges"
	"ai/infrastructure/git"
	"ai/projects"
	"ai/tools"
	"ai/workspace"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"ai/application/gitflow"
	"ai/logging"
)

// --- открытие git-проектов (Ф-2-3) ---

// projectOpener собирает use-case открытия git-проекта поверх git-исполнителя и
// реестра сервера (правила — в application/gitflow/open.go).
func (s *Server) projectOpener() *gitflow.ProjectOpener {
	return &gitflow.ProjectOpener{
		Host:       serverGitOpener{s.gitExec},
		Registry:   s.reg,
		ProjectDir: projects.ProjectDir,
	}
}

// serverGitOpener — адаптер gitflow.GitOpener поверх git-исполнителя
// (срез H3c): application-слой не импортирует infrastructure/git.
type serverGitOpener struct{ ex git.Executor }

func (o serverGitOpener) Exec(ctx context.Context, dir string, argv ...string) (string, error) {
	return o.ex.Exec(ctx, dir, argv...)
}

func (o serverGitOpener) Clone(ctx context.Context, url, branch, dest string) (gitflow.ClonedRepo, error) {
	repo, err := git.Clone(ctx, o.ex, url, branch, dest)
	if err != nil {
		return gitflow.ClonedRepo{}, err
	}
	out := gitflow.ClonedRepo{Root: repo.Root, Remote: repo.Remote, Branch: repo.Branch, Base: repo.Base}
	for _, sub := range repo.Submodules {
		out.Submodules = append(out.Submodules, gitflow.ClonedSubmodule{
			Path: sub.Path, Root: sub.Root, Remote: sub.Remote,
			Branch: sub.Branch, Base: sub.Base, DefaultBranch: sub.DefaultBranch,
		})
	}
	return out, nil
}

func (o serverGitOpener) HasCommit(ctx context.Context, dir, ref string) bool {
	return git.HasCommit(ctx, o.ex, dir, ref)
}

// handleOpenGitProject клонирует git-репозиторий в temp/<имя> и регистрирует
// проект (и сабмодули) вида KindGit. Открытие идемпотентно; правила клона,
// распознавание полу-клона и порядок регистрации — в
// application/gitflow.ProjectOpener.Open. Здесь только маршрутизация кодов и
// запись meta.
func (s *Server) handleOpenGitProject(w http.ResponseWriter, r *http.Request, gitURL string) {
	opened, err := s.projectOpener().Open(r.Context(), gitURL)
	if err != nil {
		switch {
		case errors.Is(err, gitflow.ErrGitURLName):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, gitflow.ErrDirNotEmpty):
			writeErr(w, http.StatusConflict, err.Error())
		case errors.Is(err, gitflow.ErrCloneFailed):
			writeErr(w, http.StatusBadGateway, err.Error())
		case errors.Is(err, gitflow.ErrRegisterFailed):
			writeErr(w, http.StatusBadRequest, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	s.writeProjectMetaFromInfo(w, opened.Info)
}

// gitProjectName — тонкая обёртка над правилом application/gitflow (оставлена
// для совместимости с тестами и другими файлами server).
func gitProjectName(url string) string { return gitflow.GitProjectName(url) }

// submoduleProjectName — тонкая обёртка над application/gitflow.
func submoduleProjectName(parent, path string) string {
	return gitflow.SubmoduleProjectName(parent, path)
}

func (s *Server) repositoriesForProject(project string) []string {
	repos := []string{project}
	for _, inf := range s.reg.List() {
		if inf.Parent == project {
			repos = append(repos, inf.Name)
		}
	}
	return repos
}

// repoOf восстанавливает git.Repo из реестра (root/remote/branch/base).
// Не-git проекты отклоняются явной ошибкой.
func (s *Server) repoOf(ctx context.Context, project string) (*git.Repo, error) {
	inf, err := s.reg.Get(project)
	if err != nil {
		return nil, err
	}
	if inf.Kind != workspace.KindGit {
		return nil, fmt.Errorf("проект %s не git (kind=%s): приёмка через MR доступна только git-проектам",
			project, inf.Kind)
	}
	if inf.Root == "" || inf.GitRemote == "" || inf.GitBranch == "" {
		return nil, fmt.Errorf("проект %s: неполные git-данные (root/remote/branch)", project)
	}
	repo := git.RepoFromState(s.gitExec, inf.Root, inf.GitRemote, inf.GitBranch, inf.GitBase)
	if inf.Parent == "" {
		subs, err := git.ListSubmodules(ctx, s.gitExec, inf.Root)
		if err != nil {
			return nil, err
		}
		for i := range subs {
			subs[i].Root = filepath.Join(inf.Root, subs[i].Path)
			child, err := s.reg.Get(submoduleProjectName(inf.Name, subs[i].Path))
			if err != nil || child.Parent != inf.Name {
				continue
			}
			subs[i].Remote, subs[i].Branch, subs[i].Base = child.GitRemote, child.GitBranch, child.GitBase
			subs[i].DefaultBranch = child.GitTarget
			repo.Submodules = append(repo.Submodules, subs[i])
		}
	}
	return repo, nil
}

// gitToken выбирает токен API форджа по remote: GitHub → GITHUB_TOKEN,
// GitLab → GITLAB_TOKEN (как в agents/codereviewer.pickToken).
func gitToken(remote string) string {
	switch forges.DetectType(remote) {
	case forges.KindGitHub:
		return os.Getenv("GITHUB_TOKEN")
	case forges.KindGitLab:
		return os.Getenv("GITLAB_TOKEN")
	default:
		return ""
	}
}

// tokenPushURL возвращает URL-адрес push с токеном для HTTPS-remotes GitHub/
// GitLab, если токен задан в окружении (https://x-access-token:<токен>@host/…);
// для остальных remote — пустая строка (push идёт штатным origin/SSH).
func tokenPushURL(remote string) string {
	if tok := gitToken(remote); tok != "" {
		if u, err := url.Parse(remote); err == nil && u.Scheme == "https" {
			u.User = url.UserPassword("x-access-token", tok)
			return u.String()
		}
	}
	return ""
}

// pushRepo пушит фича-ветку в remote. Для HTTPS-remote GitHub/GitLab с токеном
// в окружении встраивает его в URL push (https://x-access-token:<токен>@host/…):
// headless-сервер может не иметь credentialed credential-helper, и обычный
// `git push origin` падает на интерактивном запросе логина. Токен не
// сохраняется в конфиг git (см. git.Repo.PushTo). SSH-remote токеном не
// помогает — там остаётся штатный `git push origin` (SSH-ключ).
func (s *Server) pushRepo(ctx context.Context, repo *git.Repo, remote string) error {
	pushURL := tokenPushURL(remote)
	return repo.PushToWith(ctx, pushURL, func(ctx context.Context, child *git.Repo, childRemote string) error {
		if u := tokenPushURL(childRemote); u != "" {
			return child.PushTo(ctx, u)
		}
		return child.Push(ctx)
	})
}

// --- REST: дифф ---

// handleGetDiff возвращает сводку изменений проекта: для git-проектов —
// список изменённых файлов (метаданные, Ф-3) и патч конкретного файла через
// ?file=<path> (ленивая загрузка); для остальных — списки
// добавленных/изменённых/удалённых файлов относительно «точки отхода»
// (baseline-снимок каталога, снимается при первом запросе).
func (s *Server) handleGetDiff(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}

	if inf.Kind == workspace.KindGit {
		ref := strings.TrimSpace(r.URL.Query().Get("ref"))
		base := strings.TrimSpace(r.URL.Query().Get("vs"))
		if base == "" {
			base = inf.GitBase
		}
		if base != inf.GitBase {
			writeErr(w, http.StatusBadRequest, "точка сравнения должна быть основной веткой проекта: "+inf.GitBase)
			return
		}
		if ref != "" && !s.registeredDiffRef(inf, ref) {
			writeErr(w, http.StatusBadRequest, "ветка не зарегистрирована в проекте: "+ref)
			return
		}
		file := r.URL.Query().Get("file")
		c, err := s.gitProjectDiff(r.Context(), inf, ref, base, file == "")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "ошибка диффа: "+err.Error())
			return
		}
		if file != "" {
			patch, ok := c.File[file]
			if !ok {
				writeErr(w, http.StatusNotFound, "файл не найден в диффе: "+file)
				return
			}
			status := string(diffModified)
			for _, f := range c.Files {
				if f.Path == file {
					status = string(f.Status)
					break
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"kind":   "git",
				"path":   file,
				"status": status,
				"patch":  patch,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"kind":   "git",
			"branch": c.Branch,
			"base":   base,
			"remote": c.Remote,
			"files":  c.Files,
		})
		return
	}

	added, modified, removed, err := s.snapDiff(inf)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ошибка диффа: "+err.Error())
		return
	}
	// Перечисляем изменённые файлы в едином списке и генерируем per-file
	// unified-патчи для отображения line-level изменений в Diffboard.
	var allFiles []string
	allFiles = append(allFiles, added...)
	allFiles = append(allFiles, modified...)
	allFiles = append(allFiles, removed...)

	patches := make(map[string]string, len(allFiles))
	s.diffMu.Lock()
	snap := s.baselines[inf.Name]
	s.diffMu.Unlock()
	if snap != nil && len(allFiles) > 0 {
		for _, f := range allFiles {
			p, err := snap.DiffText(f)
			if err != nil {
				logging.For(inf.Name).Warnf("diff %s: патч файла %s: %v", inf.Name, f, err)
				continue
			}
			patches[f] = p
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":     "snap",
		"added":    added,
		"modified": modified,
		"removed":  removed,
		"patches":  patches,
	})
}

func (s *Server) registeredDiffRef(inf workspace.Info, ref string) bool {
	if inf.GitBranches == nil {
		return false
	}
	for _, branch := range inf.GitBranches.Epics {
		if branch.Branch == ref {
			return true
		}
	}
	for _, branch := range inf.GitBranches.Tasks {
		if branch.Branch == ref {
			return true
		}
	}
	return false
}

// snapDiff считает изменение каталога не-git проекта через tools.Snap.Diff
// относительно baseline-снимка «точки отхода». Обычно снимок уже снят при
// открытии проекта (ensureBaseline); ленивая фиксация остаётся страховкой
// для уже открытых проектов после перезапуска сервера.
func (s *Server) snapDiff(inf workspace.Info) ([]string, []string, []string, error) {
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	snap, ok := s.baselines[inf.Name]
	if !ok {
		snap, err := tools.NewSnap(inf.Root)
		if err != nil {
			return nil, nil, nil, err
		}
		s.baselines[inf.Name] = snap
		logging.For(inf.Name).Infof("diff %s: baseline-снимок зафиксирован (лениво)", inf.Name)
		return []string{}, []string{}, []string{}, nil
	}
	return snap.Diff()
}

// ensureBaseline фиксирует baseline-снимок («точку отхода») локального проекта,
// если его ещё нет. Вызывается при открытии/переоткрытии проекта, чтобы дифф
// показывал изменения с момента открытия, а не с первого запроса диффа —
// иначе изменения, сделанные до открытия Diffboard, были бы приняты за базу.
func (s *Server) ensureBaseline(inf workspace.Info) {
	if inf.Root == "" {
		return
	}
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	if _, ok := s.baselines[inf.Name]; ok {
		return
	}
	snap, err := tools.NewSnap(inf.Root)
	if err != nil {
		logging.For(inf.Name).Warnf("diff %s: baseline-снимок не зафиксирован: %v", inf.Name, err)
		return
	}
	s.baselines[inf.Name] = snap
	logging.For(inf.Name).Infof("diff %s: baseline-снимок зафиксирован при открытии", inf.Name)
}

// --- REST: приёмка «Принять → MR» ---

// handleAccept приёмки git-проекта: фиксирует изменения в фича-ветке,
// пушит её в remote и открывает Merge/Pull Request через фордж (GitHub/
// GitLab, определяется по remote). Возвращает ссылку на созданный MR/PR.
func (s *Server) handleAccept(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Message     string `json:"message"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	inf, err := s.reg.Get(project)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	if inf.Kind != workspace.KindGit {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("принять через MR можно только git-проект (kind=%s)", inf.Kind))
		return
	}

	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = "Результат оркестрации HITL (проект " + project + ")"
	}
	message := strings.TrimSpace(body.Message)
	if message == "" {
		message = title
	}

	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	dirty, err := repo.Dirty(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "проверка изменений: "+err.Error())
		return
	}
	if !dirty {
		committedDiff, err := repo.Diff(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, "проверка закоммиченных изменений: "+err.Error())
			return
		}
		if strings.TrimSpace(committedDiff) == "" {
			writeErr(w, http.StatusBadRequest, "в фича-ветке нет изменений для коммита")
			return
		}
	} else if err := repo.Commit(r.Context(), message); err != nil {
		writeErr(w, http.StatusBadGateway, "git commit: "+err.Error())
		return
	}
	// Дочерние MR создаются до родительского: их коммиты должны быть доступны
	// до публикации gitlink родителя.
	repositories := map[string]map[string]string{}
	for _, sub := range repo.Submodules {
		childName := submoduleProjectName(project, sub.Path)
		child, err := s.reg.Get(childName)
		if err != nil {
			continue
		}
		childRepo := git.RepoFromState(s.gitExec, child.Root, child.GitRemote, child.GitBranch, child.GitBase)
		childDiff, err := childRepo.Diff(r.Context())
		if err != nil {
			writeErr(w, http.StatusBadGateway, "diff сабмодуля "+sub.Path+": "+err.Error())
			return
		}
		if strings.TrimSpace(childDiff) == "" {
			continue
		}
		if err := s.pushRepo(r.Context(), childRepo, child.GitRemote); err != nil {
			writeErr(w, http.StatusBadGateway, "git push сабмодуля "+sub.Path+": "+err.Error())
			return
		}
		target := child.GitTarget
		if target == "" {
			target = child.GitBase
		}
		mrURL := ""
		if prior, err := s.reg.ProjectMR(childName); err == nil && prior.State != "closed" && prior.State != "merged" {
			mrURL = prior.URL
		} else {
			childForge, err := s.forgeFactory(child.GitRemote, gitToken(child.GitRemote))
			if err != nil {
				writeErr(w, http.StatusBadGateway, "фордж сабмодуля "+sub.Path+": "+err.Error())
				return
			}
			mrURL, err = childForge.CreateMergeRequest(forges.MergeRequestOptions{
				SourceBranch: childRepo.Branch, TargetBranch: target,
				Title: title + " [submodule " + sub.Path + "]", Description: body.Description,
			})
			if err != nil {
				writeErr(w, http.StatusBadGateway, "создание MR сабмодуля "+sub.Path+": "+err.Error())
				return
			}
		}
		repositories[childName] = map[string]string{"url": mrURL, "branch": childRepo.Branch, "base": target}
		_ = s.reg.SetProjectMR(childName, workspace.MRRef{URL: mrURL, Source: childRepo.Branch, Target: target, State: "open"})
	}
	repo.Submodules = nil // сабмодули уже отправлены и опубликованы отдельно выше
	if err := s.pushRepo(r.Context(), repo, inf.GitRemote); err != nil {
		writeErr(w, http.StatusBadGateway, "git push: "+err.Error())
		return
	}

	mrURL := ""
	if prior, err := s.reg.ProjectMR(project); err == nil && prior.State != "closed" && prior.State != "merged" {
		mrURL = prior.URL
	} else {
		forge, err := s.forgeFactory(inf.GitRemote, gitToken(inf.GitRemote))
		if err != nil {
			writeErr(w, http.StatusBadGateway, "создание провайдера форджа: "+err.Error())
			return
		}
		mrURL, err = forge.CreateMergeRequest(forges.MergeRequestOptions{
			SourceBranch: repo.Branch, TargetBranch: repo.Base, Title: title, Description: body.Description,
		})
		if err != nil {
			writeErr(w, http.StatusBadGateway, "создание MR/PR: "+err.Error())
			return
		}
	}
	s.invalidateDiffs(project)
	repositories[project] = map[string]string{"url": mrURL, "branch": repo.Branch, "base": repo.Base}
	_ = s.reg.SetProjectMR(project, workspace.MRRef{URL: mrURL, Source: repo.Branch, Target: repo.Base, State: "open"})

	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus, "Создан запрос на слияние: "+mrURL, "", "", nil)
	}
	if len(repositories) == 1 {
		writeJSON(w, http.StatusOK, map[string]string{"url": mrURL, "branch": repo.Branch, "base": repo.Base})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url": mrURL, "branch": repo.Branch, "base": repo.Base, "repositories": repositories,
	})
}

// handleRejectBranch отклоняет фича-ветку git-проекта: удаляет её на remote
// и локально, возвращая рабочую копию на базу (git.RejectBranch).
func (s *Server) handleRejectBranch(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")

	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, sub := range repo.Submodules {
		childName := submoduleProjectName(project, sub.Path)
		child, err := s.reg.Get(childName)
		if err != nil {
			continue
		}
		childRepo := git.RepoFromState(s.gitExec, child.Root, child.GitRemote, child.GitBranch, child.GitBase)
		if err := childRepo.RejectBranch(r.Context()); err != nil {
			writeErr(w, http.StatusBadGateway, "отклонение сабмодуля "+sub.Path+": "+err.Error())
			return
		}
		if _, err := s.gitExec.Exec(r.Context(), child.Root, "git", "checkout", "-b", child.GitBranch, child.GitBase); err != nil {
			writeErr(w, http.StatusBadGateway, "повторное создание ветки сабмодуля "+sub.Path+": "+err.Error())
			return
		}
		if _, err := s.gitExec.Exec(r.Context(), child.Root, "git", "checkout", "--detach", child.GitBranch); err != nil {
			writeErr(w, http.StatusBadGateway, "возврат сабмодуля к detached HEAD "+sub.Path+": "+err.Error())
			return
		}
		s.invalidateDiffs(childName)
	}
	if err := repo.RejectBranch(r.Context()); err != nil {
		writeErr(w, http.StatusBadGateway, "отклонение ветки: "+err.Error())
		return
	}
	if _, err := s.gitExec.Exec(r.Context(), repo.Root, "git", "checkout", "-b", repo.Branch, repo.Base); err != nil {
		writeErr(w, http.StatusBadGateway, "повторное создание feature-ветки: "+err.Error())
		return
	}
	// Рабочая копия сброшена на базу — кэш диффа устарел.
	s.invalidateDiffs(project)

	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Ветка %s отклонена, рабочая копия возвращена на %s", repo.Branch, repo.Base),
			"", "", nil)
	}
	s.srvEmitBoard(project, "git: ветка отклонена")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
