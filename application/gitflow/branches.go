package gitflow

// Ветки git-workflow (Ф-1): релизная ветка эпика (база = git_base) и фича-ветка
// задачи (база = ветка её эпика). Вынесено из HTTP-слоя (`server/gitflow.go`,
// Этап 6 «Интерфейсы и адаптеры»).
//
// Здесь политика: имя ветки из ID, идемпотентное создание ref без переключения
// рабочей копии, запись в side-реестр и проставление git_branch на доске.
// Git-операции, реестр и доска подключаются портами; HTTP-контекст, разбор
// ошибок и «отпечатки» диффов остаются в `server`.

import (
	"context"
	"errors"
	"fmt"

	"ai/domain"
	"ai/logging"
)

const (
	// EpicBranchPrefix — префикс релизной ветки эпика (база = git_base проекта).
	EpicBranchPrefix = "ai/epic/"
	// TaskBranchPrefix — префикс фича-ветки задачи. Источник правды — gitops:
	// тем же префиксом проверяется принадлежность рабочей копии задаче.
	TaskBranchPrefix = domain.TaskBranchPrefix
)

// ErrEpicBranchMissing — ветка эпика ещё не создана, поэтому фича-ветка задачи
// не может быть создана (ручной путь отвечает 400 с понятным текстом).
var ErrEpicBranchMissing = errors.New("ветка эпика не создана")

// ErrBranchCreate — git-операция создания ветки не удалась (сбой git, а не
// состояние доски: ручной путь отвечает 502).
var ErrBranchCreate = errors.New("создание ветки не удалось")

// BranchRef — ветка и её база (см. finalize.go: поле Worktree там же).

// BranchRepo — git-операции над refs, нужные созданию веток.
type BranchRepo interface {
	// BranchExists — ветка уже есть в клоне.
	BranchExists(ctx context.Context, branch string) (bool, error)
	// CreateBranch — создаёт ref branch от base, не переключая рабочую копию.
	CreateBranch(ctx context.Context, branch, base string) error
}

// BranchRegistry — side-реестр веток проекта (запись переживает перезапуск).
type BranchRegistry interface {
	SetEpicBranch(epicID string, ref BranchRef) error
	EpicBranch(epicID string) (BranchRef, error)
	SetTaskBranch(taskID string, ref BranchRef) error
	DeleteEpicBranch(epicID string) error
	DeleteTaskBranch(taskID string) error
	DeleteEpicMR(epicID string) error
	DeleteTaskMR(taskID string) error
}

// Brancher — use-case управления ветками одного проекта. `Store` — тот же порт,
// что у разрешения конфликтов (`SaveEpic`/`SaveTask`).
type Brancher struct {
	Project string
	// Base — базовая ветка эпиков (git_base проекта); пусто — эпики без ветки.
	Base     string
	Repo     BranchRepo
	Registry BranchRegistry
	Store    Store
	// Changed зовётся после записи ветки в реестр (инвалидация диффов проекта).
	Changed func()
}

// EnsureBranch создаёт ветку branch от base, если её ещё нет (идемпотентно).
func (b Brancher) EnsureBranch(ctx context.Context, branch, base string) error {
	exists, err := b.Repo.BranchExists(ctx, branch)
	if err != nil {
		return fmt.Errorf("проверка ветки %s: %v", branch, err)
	}
	if exists {
		return nil
	}
	return b.Repo.CreateBranch(ctx, branch, base)
}

// CreateEpicBranch создаёт релизную ветку эпика от базовой ветки проекта,
// записывает её в реестр и проставляет epic.git_branch на доске. Идемпотентно.
func (b Brancher) CreateEpicBranch(ctx context.Context, epic *domain.Epic) (BranchRef, error) {
	if b.Base == "" {
		return BranchRef{}, fmt.Errorf("у проекта не задана базовая ветка (git_base)")
	}
	branch := EpicBranchPrefix + domain.SanitizeBranchName(epic.TaskID)
	if err := b.EnsureBranch(ctx, branch, b.Base); err != nil {
		return BranchRef{}, fmt.Errorf("%w: %v", ErrBranchCreate, err)
	}
	ref := BranchRef{Branch: branch, Base: b.Base}
	if err := b.Registry.SetEpicBranch(epic.TaskID, ref); err != nil {
		return BranchRef{}, fmt.Errorf("ошибка записи ветки в реестр: %w", err)
	}
	if b.Changed != nil {
		b.Changed()
	}
	if epic.GitBranch == "" {
		epic.GitBranch = branch
		if err := b.Store.SaveEpic(ctx, epic); err != nil {
			return BranchRef{}, fmt.Errorf("сохранение эпика: %w", err)
		}
	}
	logging.For(b.Project).Infof("gitflow: эпик %s → ветка %s (база %s)", epic.TaskID, branch, b.Base)
	return ref, nil
}

// CreateTaskBranch создаёт фича-ветку задачи от релизной ветки её эпика,
// записывает её в реестр и проставляет task.git_branch на доске. Требует уже
// созданной ветки эпика (side-реестр). Идемпотентно.
func (b Brancher) CreateTaskBranch(ctx context.Context, task *domain.Task) (BranchRef, error) {
	// Ветка задачи создаётся от релизной ветки родительского эпика.
	epicRef, err := b.Registry.EpicBranch(task.EpicID)
	if err != nil {
		return BranchRef{}, fmt.Errorf("%w %s: %v", ErrEpicBranchMissing, task.EpicID, err)
	}

	branch := TaskBranchPrefix + domain.SanitizeBranchName(task.TaskID)
	if err := b.EnsureBranch(ctx, branch, epicRef.Branch); err != nil {
		return BranchRef{}, fmt.Errorf("%w: %v", ErrBranchCreate, err)
	}
	ref := BranchRef{Branch: branch, Base: epicRef.Branch}
	if err := b.Registry.SetTaskBranch(task.TaskID, ref); err != nil {
		return BranchRef{}, fmt.Errorf("ошибка записи ветки в реестр: %w", err)
	}
	if b.Changed != nil {
		b.Changed()
	}
	if task.GitBranch == "" {
		task.GitBranch = branch
		if err := b.Store.SaveTask(ctx, task); err != nil {
			return BranchRef{}, fmt.Errorf("сохранение задачи: %w", err)
		}
	}
	logging.For(b.Project).Infof("gitflow: задача %s → ветка %s (база %s)", task.TaskID, branch, epicRef.Branch)
	return ref, nil
}

// AutoCreateEpicBranch — авто-ветка эпика при добавлении на доску: та же
// политика, но ошибки НЕ ломают сохранение эпика — логируются (на доске
// остаётся ручная кнопка как страховка). true — ветка создана/уже была.
func (b Brancher) AutoCreateEpicBranch(ctx context.Context, epic *domain.Epic) bool {
	if epic == nil || epic.TaskID == "" {
		return false
	}
	if _, err := b.CreateEpicBranch(ctx, epic); err != nil {
		logging.For(b.Project).Warnf("gitflow: авто-ветка эпика %s: %v", epic.TaskID, err)
		return false
	}
	return true
}

// AutoCreateTaskBranch — авто-ветка задачи при добавлении на доску: та же
// политика, ошибки не ломают сохранение задачи. Отсутствие ветки эпика —
// штатное состояние (Detailf), прочие сбои — Warnf. true — ветка создана.
func (b Brancher) AutoCreateTaskBranch(ctx context.Context, task *domain.Task) bool {
	if task == nil || task.TaskID == "" || task.EpicID == "" {
		return false
	}
	if _, err := b.CreateTaskBranch(ctx, task); err != nil {
		if errors.Is(err, ErrEpicBranchMissing) {
			logging.For(b.Project).Detailf("gitflow: авто-ветка задачи %s: %v", task.TaskID, err)
		} else {
			logging.For(b.Project).Warnf("gitflow: авто-ветка задачи %s: %v", task.TaskID, err)
		}
		return false
	}
	return true
}

// RemoveEpicBranches снимает из side-реестра ветки эпика и всех его задач
// (вызывается при удалении эпика с доски). Best-effort: ошибки логируются.
func (b Brancher) RemoveEpicBranches(epicID string, taskIDs []string) {
	if err := b.Registry.DeleteEpicBranch(epicID); err != nil {
		logging.For(b.Project).Warnf("gitflow: снятие ветки эпика %s: %v", epicID, err)
	}
	if err := b.Registry.DeleteEpicMR(epicID); err != nil {
		logging.For(b.Project).Warnf("gitflow: снятие MR эпика %s: %v", epicID, err)
	}
	for _, tid := range taskIDs {
		if err := b.Registry.DeleteTaskBranch(tid); err != nil {
			logging.For(b.Project).Warnf("gitflow: снятие ветки задачи %s: %v", tid, err)
		}
		if err := b.Registry.DeleteTaskMR(tid); err != nil {
			logging.For(b.Project).Warnf("gitflow: снятие MR задачи %s: %v", tid, err)
		}
	}
	if b.Changed != nil {
		b.Changed()
	}
}
