package server

import (
	"context"
	"net/http"
	"os"
	"strings"

	"ai/agents/architect"
	"ai/infrastructure/board"
	"ai/workspace"
)

// handleListProjects возвращает список зарегистрированных проектов.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out []map[string]any

	for _, inf := range s.reg.List() {
		// Пытаемся прочитать мета доски (если Redis доступен).
		meta, _ := func() (*board.Meta, error) {
			store, err := board.NewStore(ctx, architect.LoadConfig().StoreConfig(inf.Name))
			if err != nil {
				return nil, err
			}
			defer store.Close()
			return store.GetMeta(ctx)
		}()

		running, gating, standby := s.sessionFlags(inf.Name)
		out = append(out, projectMeta(inf, meta, running, gating, standby))
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOpenProject открывает/регистрирует проект по пути или git-URL.
// Единое поле path_or_git: git-ссылка (схема http(s)/ssh/git или scp-форма
// git@host:path) уходит в клон, локальный путь — в обычную регистрацию.
func (s *Server) handleOpenProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PathOrGit string `json:"path_or_git"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "невалидный JSON")
		return
	}

	path := strings.TrimSpace(body.PathOrGit)
	if path == "" {
		writeErr(w, http.StatusBadRequest, "укажите путь к папке или git-URL")
		return
	}

	// git-URL → клон в temp/<имя> с фича-веткой (Ф-2-3).
	if isGitURL(path) {
		s.handleOpenGitProject(w, r, path)
		return
	}

	// Проверяем: может, проект уже зарегистрирован?
	if inf, err := s.reg.Get(path); err == nil {
		// Уже есть — возвращаем meta (база диффа фиксируется при переоткрытии).
		s.ensureBaseline(inf)
		s.writeProjectMeta(w, r, inf.Name)
		return
	}

	// Пробуем определить тип: существующий каталог → KindDir; иначе → KindTemp.
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		// Каталог существует → dir.
		name := dirBase(path)
		if name == "" || name == "." || name == ".." {
			writeErr(w, http.StatusBadRequest, "невозможно определить имя проекта из пути")
			return
		}
		// Если имя занято — генерируем суффикс.
		if _, gerr := s.reg.Get(name); gerr == nil {
			name = name + "-dir"
		}
		inf, err := s.reg.Add(workspace.AddParams{
			Name:    name,
			Kind:    workspace.KindDir,
			Root:    path,
			Confirm: true,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, "ошибка регистрации: "+err.Error())
			return
		}
		// «Точка отхода» фиксируется сразу при открытии — изменения,
		// сделанные агентом до первого просмотра Diffboard, иначе были бы
		// приняты за базу и не показались бы в диффе.
		s.ensureBaseline(inf)
		s.writeProjectMetaFromInfo(w, inf)
		return
	}

	// Иначе — temp-проект (имя = path).
	name := path
	inf, err := s.reg.Add(workspace.AddParams{
		Name: name,
		Kind: workspace.KindTemp,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "ошибка регистрации: "+err.Error())
		return
	}
	s.writeProjectMetaFromInfo(w, inf)
}

// writeProjectMeta возвращает meta проекта по имени.
func (s *Server) writeProjectMeta(w http.ResponseWriter, r *http.Request, name string) {
	inf, err := s.reg.Get(name)
	if err != nil {
		writeErr(w, http.StatusNotFound, "проект не найден")
		return
	}
	s.writeProjectMetaFromInfo(w, inf)
}

// writeProjectMetaFromInfo возвращает ProjectMeta из Info.
func (s *Server) writeProjectMetaFromInfo(w http.ResponseWriter, inf workspace.Info) {
	// Пытаемся прочитать meta доски.
	meta, _ := func() (*board.Meta, error) {
		store, err := board.NewStore(context.Background(), architect.LoadConfig().StoreConfig(inf.Name))
		if err != nil {
			return nil, err
		}
		defer store.Close()
		return store.GetMeta(context.Background())
	}()
	sess := s.session(inf.Name)
	running, gating, standby := false, false, false
	if sess != nil {
		running, gating, standby = s.sessionFlags(inf.Name)
	}
	writeJSON(w, http.StatusOK, projectMeta(inf, meta, running, gating, standby))
}

// isGitURL определяет, что ввод — git-ссылка, а не путь к локальной папке:
// HTTP(S)/SSH/git-схема или scp-форма git@host:path.
func isGitURL(s string) bool {
	low := strings.ToLower(s)
	for _, p := range []string{"http://", "https://", "ssh://", "git://", "git@"} {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// dirBase возвращает последний компонент пути.
func dirBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}

// --- REST: доска ---
