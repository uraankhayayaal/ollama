// Package workspace — реестр рабочих проектов Web UI. Связывает удобное
// имя с абсолютным путём, по которому агенты читают/пишут код:
//   - temp — проекты агентской генерации (temp/<имя> в корне модуля);
//   - dir — произвольная локальная папка;
//   - git — git-проект (клон/worktree) с известным remote.
//
// Реестр хранится в ~/.ai-workspaces.json (переопределяется AI_WORKSPACES).
// Пакет НЕ вызывает git CLI: детекция remote и git-операции — ответственность
// пакета gitops (Ф-2). Здесь только безопасная регистрация путей.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai/projects"
)

// Kind — тип рабочего проекта.
type Kind string

const (
	KindTemp Kind = "temp" // temp/<имя> в корне модуля (агентская генерация)
	KindDir  Kind = "dir"  // произвольная локальная папка
	KindGit  Kind = "git"  // git-проект (клон/worktree)
)

// Info — запись реестра: имя проекта → абсолютный путь Root.
type Info struct {
	Name      string `json:"name"`
	Kind      Kind   `json:"kind"`
	Root      string `json:"root"`
	GitRemote string `json:"git_remote,omitempty"`
	GitBranch string `json:"git_branch,omitempty"` // фича-ветка (KindGit)
	GitBase   string `json:"git_base,omitempty"`   // точка отхода (ветка по умолчанию)
	GitTarget string `json:"git_target,omitempty"` // целевая branch для MR (submodule)
	Parent    string `json:"parent,omitempty"`     // родитель KindGit для git submodule
	// GitBranches — side-реестр веток эпиков/задач git-workflow (Ф-1):
	// epic_id/task_id → имя ветки + точка отхода. Пустой (nil) — workflow
	// не активирован, работает прежняя приёмка «Принять → MR».
	GitBranches *GitBranchMap `json:"git_branches,omitempty"`
	// GitResolving — активный процесс решения конфликтов эпика (Ф-4):
	// «main → релизная ветка» выполняется в постоянном конфликтном worktree,
	// файлы правит модель/instrument, финализирует POST .../epics/:eid/resolve.
	// Наличие записи блокирует повторный rebase и отмечает состояние в UI.
	GitResolving *EpicResolve `json:"git_resolving,omitempty"`
	// GitMergeRequests — side-реестр MR/PR эпиков и задач (Ф-5): epic_id/task_id
	// → {url, source, target, state}. Заполняется при создании MR кнопкой
	// модалки и уточняется фоновой сверкой с форджем. Пустой (nil) — MR ещё
	// нет ни у кого.
	GitMergeRequests *MergeRequestMap `json:"git_merge_requests,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
}

// AddParams — параметры регистрации нового проекта.
type AddParams struct {
	Name      string // имя проекта (без разделителей пути)
	Kind      Kind   // temp / dir / git
	Root      string // исходный путь (для temp игнорируется — берётся ProjectDir)
	GitRemote string // обязателен для KindGit
	GitBranch string // фича-ветка git-проекта (KindGit)
	GitBase   string // точка отхода/базовая ветка git-проекта (KindGit)
	GitTarget string // базовая ветка MR, если отличается от точки отхода
	Parent    string // имя родительского KindGit для вложенного submodule
	Confirm   bool   // явное подтверждение для KindDir (не из temp/)
}

var (
	ErrEmptyName   = errors.New("имя проекта не может быть пустым")
	ErrBadName     = errors.New("имя проекта не может содержать разделители пути")
	ErrExists      = errors.New("проект с таким именем уже зарегистрирован")
	ErrNotFound    = errors.New("проект не найден в реестре")
	ErrRootMissing = errors.New("каталог проекта не существует")
	ErrRootNotDir  = errors.New("путь проекта не является каталогом")
	ErrForbidden   = errors.New("каталог проекта запрещён (корневой/системный каталог)")
	ErrTempRoot    = errors.New("целевой каталог — корень temp/, а не проект внутри него")
	ErrNested      = errors.New("каталог вложен в другой зарегистрированный проект")
	ErrContains    = errors.New("каталог содержит другой зарегистрированный проект")
	ErrNeedConfirm = errors.New("для регистрации произвольной папки требуется явное подтверждение")
	ErrGitRemote   = errors.New("git-проект требует непустой git_remote")
)

// Registry — потокобезопасный реестр проектов, сохраняемый в JSON-файл.
type Registry struct {
	mu       sync.RWMutex
	path     string
	projects map[string]Info
}

// DefaultPath возвращает путь к файлу реестра: AI_WORKSPACES или
// ~/.ai-workspaces.json.
func DefaultPath() string {
	if p := os.Getenv("AI_WORKSPACES"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".ai-workspaces.json"
	}
	return filepath.Join(home, ".ai-workspaces.json")
}

// Open загружает реестр из path (пустой путь → DefaultPath). Отсутствие файла
// не ошибка — реестр стартует пустым.
func Open(path string) (*Registry, error) {
	if path == "" {
		path = DefaultPath()
	}
	r := &Registry{path: path, projects: map[string]Info{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, fmt.Errorf("workspace: чтение реестра %s: %w", path, err)
	}
	var loaded map[string]Info
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("workspace: разбор реестра %s: %w", path, err)
	}
	for name, in := range loaded {
		if in.Root != "" {
			in.Root = filepath.Clean(in.Root)
		}
		r.projects[name] = in
	}
	return r, nil
}

// Path возвращает путь к файлу реестра.
func (r *Registry) Path() string { return r.path }

// Add регистрирует новый проект. Для KindTemp корень форсируется в
// projects.ProjectDir(name) — путь, с которым работают все агенты.
func (r *Registry) Add(p AddParams) (Info, error) {
	if err := validateName(p.Name); err != nil {
		return Info{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.projects[p.Name]; exists {
		return Info{}, fmt.Errorf("%w: %s", ErrExists, p.Name)
	}

	root, err := r.resolveRoot(p)
	if err != nil {
		return Info{}, err
	}
	if p.Parent != "" {
		parent, ok := r.projects[p.Parent]
		if !ok || parent.Kind != KindGit || p.Kind != KindGit {
			return Info{}, fmt.Errorf("workspace: родитель сабмодуля %q не является зарегистрированным git-проектом", p.Parent)
		}
		if !hasPrefix(root, parent.Root) {
			return Info{}, fmt.Errorf("workspace: сабмодуль %s должен находиться внутри %s", root, parent.Root)
		}
	}
	for _, other := range r.projects {
		if p.Parent == other.Name && hasPrefix(root, other.Root) {
			continue
		}
		if err := checkNesting(other.Root, root); err != nil {
			return Info{}, err
		}
	}

	in := Info{
		Name:      p.Name,
		Kind:      p.Kind,
		Root:      root,
		GitRemote: p.GitRemote,
		GitBranch: p.GitBranch,
		GitBase:   p.GitBase,
		GitTarget: p.GitTarget,
		Parent:    p.Parent,
		CreatedAt: time.Now().UTC(),
	}
	r.projects[p.Name] = in
	if err := r.saveLocked(); err != nil {
		delete(r.projects, p.Name)
		return Info{}, err
	}
	return in, nil
}

// resolveRoot валидирует исходный путь и приводит к абсолютному. Для KindTemp
// путь всегда projects.ProjectDir(name) (директория создаётся при записи).
// Forbidden-каталоги (корень ФС, домашний/модульный/корень temp/) отклоняются.
func (r *Registry) resolveRoot(p AddParams) (string, error) {
	switch p.Kind {
	case KindTemp:
		root := projects.ProjectDir(p.Name)
		if err := forbidRoots(root); err != nil {
			return "", err
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", fmt.Errorf("workspace: создание temp-проекта %s: %w", p.Name, err)
		}
		return root, nil

	case KindDir:
		if !p.Confirm {
			return "", ErrNeedConfirm
		}
		return resolveExistingDir(p.Root)

	case KindGit:
		if strings.TrimSpace(p.GitRemote) == "" {
			return "", ErrGitRemote
		}
		return resolveExistingDir(p.Root)

	default:
		return "", fmt.Errorf("workspace: неизвестный тип проекта %q", p.Kind)
	}
}

// resolveExistingDir нормализует существующий каталог до абсолютного пути с
// применением безопасности (forbidden-корни).
func resolveExistingDir(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", ErrRootMissing
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("workspace: абсолютный путь %s: %w", root, err)
	}
	abs = filepath.Clean(abs)

	if err := forbidRoots(abs); err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrRootMissing, abs)
		}
		return "", fmt.Errorf("workspace: stat %s: %w", abs, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrRootNotDir, abs)
	}
	return abs, nil
}

// forbidRoots отклоняет опасные для записи корни: корень ФС, домашний
// каталог, корень модуля (репозиторий) и сам корень temp/ (внутрь нельзя
// писать файлы, только подкаталоги-проекты).
func forbidRoots(abs string) error {
	switch abs {
	case "/":
		return ErrForbidden
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && abs == filepath.Clean(home) {
		return ErrForbidden
	}
	switch abs {
	case projects.ModuleRoot(), projects.ProjectDir(""):
		return ErrForbidden
	}
	return nil
}

// checkNesting отклоняет вложенность корней: newRoot внутри otherRoot
// (ErrNested) или otherRoot внутри newRoot (ErrContains).
func checkNesting(otherRoot, newRoot string) error {
	if hasPrefix(newRoot, otherRoot) {
		return fmt.Errorf("%w: %s вложен в %s", ErrNested, newRoot, otherRoot)
	}
	if hasPrefix(otherRoot, newRoot) {
		return fmt.Errorf("%w: %s содержит %s", ErrContains, newRoot, otherRoot)
	}
	return nil
}

// hasPrefix проверяет, что child лежит строго внутри parent (по границам
// элементов пути).
func hasPrefix(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Get возвращает запись по имени.
func (r *Registry) Get(name string) (Info, error) {
	if err := validateName(name); err != nil {
		return Info{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	in, ok := r.projects[name]
	if !ok {
		return Info{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return in, nil
}

// Root возвращает абсолютный путь проекта по имени.
func (r *Registry) Root(name string) (string, error) {
	in, err := r.Get(name)
	if err != nil {
		return "", err
	}
	return in.Root, nil
}

// FindByRoot ищет запись по абсолютному пути корня.
func (r *Registry) FindByRoot(root string) (Info, bool) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Info{}, false
	}
	abs = filepath.Clean(abs)
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, in := range r.projects {
		if in.Root == abs {
			return in, true
		}
	}
	return Info{}, false
}

// List возвращает записи, отсортированные по имени.
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.projects))
	for _, in := range r.projects {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Remove удаляет запись из реестра (без удаления файлов на диске).
func (r *Registry) Remove(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.projects[name]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	delete(r.projects, name)
	return r.saveLocked()
}

// saveLocked атомарно записывает реестр (tmp + rename, 0600). Вызывается под
// блокировкой записи.
func (r *Registry) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return fmt.Errorf("workspace: создание каталога реестра: %w", err)
	}
	data, err := json.MarshalIndent(r.projects, "", "  ")
	if err != nil {
		return fmt.Errorf("workspace: сериализация реестра: %w", err)
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("workspace: запись реестра: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		return fmt.Errorf("workspace: сохранение реестра: %w", err)
	}
	return nil
}

// validateName проверяет корректность имени проекта (непустое, без
// разделителей пути и "..").
func validateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrEmptyName
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: %s", ErrBadName, name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("%w: %s", ErrBadName, name)
	}
	return nil
}
