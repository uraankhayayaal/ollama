package server

import (
	"ai/chat"
	"ai/forges"
	"ai/gitops"
	"ai/projects"
	"ai/tools"
	"ai/workspace"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"ai/logging"
)

// --- открытие git-проектов (Ф-2-3) ---

// handleOpenGitProject клонирует git-репозиторий в temp/<имя>, создаёт
// фича-ветку и регистрирует проект вида KindGit. Агенты работают прямо в
// этом клоне (projects.ProjectDir(name) == рабочая директория), поэтому
// вся существующая оркестрация не меняется; приёмка (diff/accept/reject)
// идёт по git-состоянию клона.
func (s *Server) handleOpenGitProject(w http.ResponseWriter, r *http.Request, gitURL string) {
	name := gitProjectName(gitURL)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "не удалось определить имя проекта из git-URL")
		return
	}

	// Проект уже открыт — возвращаем meta (открытие идемпотентно).
	if _, err := s.reg.Get(name); err == nil {
		s.writeProjectMeta(w, r, name)
		return
	}

	branch := "ai/" + name
	dest := projects.ProjectDir(name)
	// git clone умеет клонировать в существующий ПУСТОЙ каталог; непустой —
	// конфликт (чтобы не смешать чужое содержимое с клоном).
	if st, err := os.Stat(dest); err == nil {
		if !st.IsDir() || !dirIsEmpty(dest) {
			writeErr(w, http.StatusConflict,
				fmt.Sprintf("каталог %s уже существует и не пуст: используйте другое имя или удалите его", dest))
			return
		}
	}

	repo, err := gitops.Clone(r.Context(), s.gitExec, gitURL, branch, dest)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ошибка клонирования: "+err.Error())
		return
	}

	inf, err := s.reg.Add(workspace.AddParams{
		Name:      name,
		Kind:      workspace.KindGit,
		Root:      repo.Root,
		GitRemote: repo.Remote,
		GitBranch: repo.Branch,
		GitBase:   repo.Base,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ошибка регистрации: "+err.Error())
		return
	}
	logging.For(name).Infof("git-проект %s: клон %s → ветка %s (база %s)", name, repo.Remote, repo.Branch, repo.Base)
	s.writeProjectMetaFromInfo(w, inf)
}

// gitProjectName извлекает стабильное имя проекта из git-URL:
//
//	git@gitlab.com:group/sub/proj.git    → proj
//	https://github.com/owner/repo.git    → repo
//	https://host/path/to/name            → name
func gitProjectName(url string) string {
	u := strings.TrimSpace(url)
	if u == "" {
		return ""
	}
	// Отрезаем query/fragment (https).
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimSuffix(u, "/")
	// SSH-вид git@host:group/sub/proj(.git) — начинаем с «:».
	if strings.HasPrefix(u, "git@") {
		if i := strings.IndexByte(u, ':'); i >= 0 {
			u = u[i+1:]
		}
	}
	// Последний сегмент пути для обоих видов.
	if i := strings.LastIndex(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimSpace(u)
	if u == "" || strings.ContainsAny(u, `/\`) {
		return ""
	}
	return u
}

// repoOf восстанавливает gitops.Repo из реестра (root/remote/branch/base).
// Не-git проекты отклоняются явной ошибкой.
func (s *Server) repoOf(_ context.Context, project string) (*gitops.Repo, error) {
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
	return gitops.RepoFromState(s.gitExec, inf.Root, inf.GitRemote, inf.GitBranch, inf.GitBase), nil
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
// сохраняется в конфиг git (см. gitops.Repo.PushTo). SSH-remote токеном не
// помогает — там остаётся штатный `git push origin` (SSH-ключ).
func (s *Server) pushRepo(ctx context.Context, repo *gitops.Repo, remote string) error {
	if u := tokenPushURL(remote); u != "" {
		return repo.PushTo(ctx, u)
	}
	return repo.Push(ctx)
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
		c, err := s.gitProjectDiff(r.Context(), inf)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "ошибка диффа: "+err.Error())
			return
		}
		if file := r.URL.Query().Get("file"); file != "" {
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
			"base":   c.Base,
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
		writeErr(w, http.StatusBadRequest, "в фича-ветке нет изменений для коммита")
		return
	}

	if err := repo.Commit(r.Context(), message); err != nil {
		writeErr(w, http.StatusBadGateway, "git commit: "+err.Error())
		return
	}
	if err := s.pushRepo(r.Context(), repo, inf.GitRemote); err != nil {
		writeErr(w, http.StatusBadGateway, "git push: "+err.Error())
		return
	}

	forge, err := s.forgeFactory(inf.GitRemote, gitToken(inf.GitRemote))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "создание провайдера форджа: "+err.Error())
		return
	}
	mrURL, err := forge.CreateMergeRequest(forges.MergeRequestOptions{
		SourceBranch: repo.Branch,
		TargetBranch: repo.Base,
		Title:        title,
		Description:  body.Description,
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "создание MR/PR: "+err.Error())
		return
	}

	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus, "Создан запрос на слияние: "+mrURL, "", "", nil)
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"url":    mrURL,
		"branch": repo.Branch,
		"base":   repo.Base,
	})
}

// handleRejectBranch отклоняет фича-ветку git-проекта: удаляет её на remote
// и локально, возвращая рабочую копию на базу (gitops.RejectBranch).
func (s *Server) handleRejectBranch(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("id")

	repo, err := s.repoOf(r.Context(), project)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := repo.RejectBranch(r.Context()); err != nil {
		writeErr(w, http.StatusBadGateway, "отклонение ветки: "+err.Error())
		return
	}
	// Рабочая копия сброшена на базу — кэш диффа устарел.
	s.diffMu.Lock()
	delete(s.diffs, project)
	s.diffMu.Unlock()

	if sess := s.session(project); sess != nil {
		sess.append(chat.RoleStatus,
			fmt.Sprintf("Ветка %s отклонена, рабочая копия возвращена на %s", repo.Branch, repo.Base),
			"", "", nil)
	}
	s.kickBoard(project)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// dirIsEmpty сообщает, пуст ли каталог (нет ни одного элемента).
func dirIsEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
