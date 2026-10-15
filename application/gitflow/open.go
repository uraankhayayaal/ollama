// Открытие git-проекта: клонирование в temp/<имя>, регистрация проекта и его
// сабмодулей, распознавание «полу-клона» от неудачной попытки (срез F3l Этапа 6).
//
// Логика вынесена из HTTP-хендлера server/git.go: сервер оставляет маршрутизацию
// и запись ответа, а правила имени/адреса, авточистка мусора и порядок клона+
// регистрации живут здесь. Реестр проектов передаётся портом OpenRegistry —
// *workspace.Registry удовлетворяет ему напрямую.
package gitflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai/domain"
	"ai/logging"
)

// ClonedSubmodule — сабмодуль клонированного проекта.
type ClonedSubmodule struct {
	Path          string
	Root          string
	Remote        string
	Branch        string
	Base          string
	DefaultBranch string
}

// ClonedRepo — результат клонирования git-проекта.
type ClonedRepo struct {
	Root       string
	Remote     string
	Branch     string
	Base       string
	Submodules []ClonedSubmodule
}

// GitOpener — git-операции открытия проекта (клон, проверка коммита, вывод
// команд). Реализуется адаптером поверх infrastructure/git (срез H3c).
type GitOpener interface {
	// Exec — вывод git-команды в каталоге dir.
	Exec(ctx context.Context, dir string, argv ...string) (string, error)
	// Clone — клонировать url в dest (ветка branch).
	Clone(ctx context.Context, url, branch, dest string) (ClonedRepo, error)
	// HasCommit — есть ли коммит на ref.
	HasCommit(ctx context.Context, dir, ref string) bool
}

// ErrGitURLName — из git-URL не удалось выделить имя проекта.
var ErrGitURLName = errors.New("не удалось определить имя проекта из git-URL")

// ErrDirNotEmpty — каталог проекта существует и не пуст (и это не наш
// полу-клон): открытие отклоняется, чтобы не смешать чужое содержимое.
var ErrDirNotEmpty = errors.New("каталог уже существует и не пуст")

// ErrCloneFailed — git clone не удался (полу-клон убран).
var ErrCloneFailed = errors.New("ошибка клонирования")

// ErrRegisterFailed — не удалось записать проект (или сабмодуль) в реестр.
var ErrRegisterFailed = errors.New("ошибка регистрации")

// OpenRegistry — реестр проектов: поиск и добавление. Реализуется
// *workspace.Registry напрямую.
type OpenRegistry interface {
	Get(name string) (domain.Info, error)
	Add(p domain.AddParams) (domain.Info, error)
}

// OpenedProject — результат открытия: имя проекта и его запись в реестре.
type OpenedProject struct {
	Name string
	Info domain.Info
}

// ProjectOpener клонирует и регистрирует git-проект. Host, Registry и
// ProjectDir обязательны; RemoveAll подменяется в тестах.
type ProjectOpener struct {
	Host       GitOpener
	Registry   OpenRegistry
	ProjectDir func(name string) string
	RemoveAll  func(dir string) error
}

// Open идемпотентно открывает git-проект по URL. Возвращает ErrGitURLName,
// ErrDirNotEmpty (тип *DirNotEmptyError), ErrCloneFailed, ErrRegisterFailed;
// прочие ошибки оборачиваются внутрь перечисленных.
func (o *ProjectOpener) Open(ctx context.Context, gitURL string) (*OpenedProject, error) {
	name := GitProjectName(gitURL)
	if name == "" {
		return nil, ErrGitURLName
	}
	// Проект уже открыт — возвращаем meta (открытие идемпотентно).
	if inf, err := o.Registry.Get(name); err == nil {
		return &OpenedProject{Name: name, Info: inf}, nil
	}

	branch := "ai/" + name
	dest := o.ProjectDir(name)
	// git clone умеет клонировать в существующий ПУСТОЙ каталог; непустой —
	// конфликт (чтобы не смешать чужое содержимое с клоном).
	if st, err := os.Stat(dest); err == nil {
		if !st.IsDir() || !dirIsEmpty(dest) {
			// Неудачный клон того же репозитория (мусор прошлой попытки) —
			// убираем сами, иначе открыть проект второй раз невозможно.
			if !o.discardHalfClone(ctx, name, dest, gitURL) {
				return nil, &DirNotEmptyError{Dir: dest}
			}
		}
	}

	repo, err := o.Host.Clone(ctx, gitURL, branch, dest)
	if err != nil {
		// Полу-клон оставлять нельзя: следующая попытка упрётся в 409, и
		// пользователь уже не сможет открыть проект без ручной чистки temp/.
		if rmErr := o.removeAll(dest); rmErr != nil {
			logging.Warnf("git-проект %s: не удалось убрать неудачный клон %s: %v", name, dest, rmErr)
		}
		return nil, fmt.Errorf("%w: %v", ErrCloneFailed, err)
	}

	inf, err := o.Registry.Add(domain.AddParams{
		Name:      name,
		Kind:      domain.KindGit,
		Root:      repo.Root,
		GitRemote: repo.Remote,
		GitBranch: repo.Branch,
		GitBase:   repo.Base,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRegisterFailed, err)
	}
	logging.For(name).Infof("git-проект %s: клон %s → ветка %s (база %s)", name, repo.Remote, repo.Branch, repo.Base)
	for _, sub := range repo.Submodules {
		childName := SubmoduleProjectName(name, sub.Path)
		if _, err := o.Registry.Add(domain.AddParams{
			Name: childName, Kind: domain.KindGit, Root: sub.Root,
			GitRemote: sub.Remote, GitBranch: sub.Branch, GitBase: sub.Base,
			GitTarget: sub.DefaultBranch,
			Parent:    name,
		}); err != nil {
			return nil, fmt.Errorf("%w сабмодуля %s: %v", ErrRegisterFailed, sub.Path, err)
		}
	}
	return &OpenedProject{Name: name, Info: inf}, nil
}

func (o *ProjectOpener) removeAll(dir string) error {
	if o.RemoveAll != nil {
		return o.RemoveAll(dir)
	}
	return os.RemoveAll(dir)
}

// DirNotEmptyError — каталог проекта существует и не пуст (не наш полу-клон).
type DirNotEmptyError struct{ Dir string }

func (e *DirNotEmptyError) Error() string {
	return fmt.Sprintf("каталог %s уже существует и не пуст: используйте другое имя или удалите его", e.Dir)
}

// Is позволяет errors.Is(err, ErrDirNotEmpty).
func (e *DirNotEmptyError) Is(target error) bool { return target == ErrDirNotEmpty }

// discardHalfClone убирает каталог, оставшийся от НЕУДАЧНОГО клона того же
// репозитория, и сообщает, можно ли продолжать клонирование.
//
// Такой мусор появляется, когда клон упал уже после создания каталога
// (например, раньше — на определении ветки в пустом репозитории), и блокирует
// все следующие попытки открыть проект (409 «каталог не пуст»).
//
// Удаляем только заведомо свой мусор — все четыре признака обязательны:
//   - обычный клон (каталог .git, не worktree-указатель);
//   - origin совпадает с запрошенным URL;
//   - ни одного коммита (unborn HEAD) — истории, которую жалко, нет;
//   - чистое состояние — нет ни правок, ни неотслеживаемых файлов.
//
// Всё остальное (чужой клон, наша работа с коммитами, незакоммиченные правки)
// не трогаем: возвращаем false и вызывающий отдаёт 409.
func (o *ProjectOpener) discardHalfClone(ctx context.Context, name, dir, gitURL string) bool {
	out, err := o.Host.Exec(ctx, dir, "git", "rev-parse", "--git-dir")
	if err != nil || strings.TrimSpace(out) != ".git" {
		return false
	}
	origin, err := o.Host.Exec(ctx, dir, "git", "remote", "get-url", "origin")
	if err != nil || !SameGitRemote(strings.TrimSpace(origin), gitURL) {
		return false
	}
	if o.Host.HasCommit(ctx, dir, "HEAD") {
		return false // есть история — это не мусор
	}
	if status, err := o.Host.Exec(ctx, dir, "git", "status", "--porcelain"); err != nil || strings.TrimSpace(status) != "" {
		return false
	}
	if err := o.removeAll(dir); err != nil {
		logging.Warnf("git-проект %s: не удалось убрать неудачный клон %s: %v", name, dir, err)
		return false
	}
	logging.For(name).Infof("убран неудачный клон %s (%s) — повторное клонирование", dir, gitURL)
	return true
}

// GitProjectName извлекает стабильное имя проекта из git-URL:
//
//	git@gitlab.com:group/sub/proj.git    → proj
//	https://github.com/owner/repo.git    → repo
//	https://host/path/to/name            → name
func GitProjectName(url string) string {
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

// SubmoduleProjectName — имя проекта-сабмодуля: родитель и путь в реестре и на
// диске, где «/» и «\» заменены на «~s», а «~» — на «~t» (имя должно быть
// плоским: реестр — плоский список).
func SubmoduleProjectName(parent, path string) string {
	path = strings.Trim(filepath.ToSlash(path), "/")
	path = strings.NewReplacer("~", "~t", "/", "~s", "\\", "~s").Replace(path)
	return parent + "--" + path
}

// SameGitRemote сравнивает URL remote, игнорируя формы записи одного адреса:
// с суффиксом .git и без, со слешем и без. Схему/хост/путь не нормализует —
// git@github.com:u/r и https://github.com/u/r это разные адреса.
func SameGitRemote(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimSuffix(s, "/")
		s = strings.TrimSuffix(s, ".git")
		s = strings.TrimSuffix(s, "/")
		return s
	}
	return norm(a) == norm(b)
}

// dirIsEmpty сообщает, пуст ли каталог (нет ни одного элемента).
func dirIsEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(entries) == 0
}
