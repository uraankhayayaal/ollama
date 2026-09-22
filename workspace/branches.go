// Side-реестр веток эпиков и задач git-проектов (Ф-1 git-workflow).
//
// Реестр хранится в том же JSON-файле, что и остальные записи workspace
// (поле Info.GitBranches): сохраняется через штатный saveLocked с атомарной
// заменой, переживает перезапуск сервера. Ключ — project -> epic/task ID ->
// {ветка, база}.
//
// Пока у эпика/задачи нет ветки — работает прежний flow приёмки («Принять →
// MR» от проектной фича-ветки); новый flow включается с появлением записи.
package workspace

import (
	"errors"
	"fmt"
)

// BranchRef — привязка git-ветки: имя ветки и точка отхода (база), от которой
// ветка создана. Worktree — каталог постоянного worktree задачи (Ф-3): задаётся
// сервером при переводе задачи «в работу» и снимается на done; пусто — worktree
// не создан (или уже снят).
type BranchRef struct {
	Branch   string `json:"branch"`
	Base     string `json:"base,omitempty"`
	Worktree string `json:"worktree,omitempty"`
}

// GitBranchMap — реестр веток эпиков (релизные ветки, база = main) и задач
// (фича-ветки, база = ветка эпика) git-проекта.
type GitBranchMap struct {
	Epics map[string]BranchRef `json:"epics,omitempty"` // epic_id → ветка эпика
	Tasks map[string]BranchRef `json:"tasks,omitempty"` // task_id → ветка задачи
}

var (
	// ErrEpicBranchMissing — ветка эпика ещё не создана (задача не может
	// получить ветку без релизной ветки эпика).
	ErrEpicBranchMissing = errors.New("ветка эпика не создана")
	// ErrTaskBranchMissing — ветка задачи ещё не создана.
	ErrTaskBranchMissing = errors.New("ветка задачи не создана")
)

// SetEpicBranch записывает ветку эпика в side-реестр проекта (идемпотентно).
func (r *Registry) SetEpicBranch(name, epicID string, ref BranchRef) error {
	if err := validateName(name); err != nil {
		return err
	}
	if epicID == "" {
		return fmt.Errorf("workspace: пустой ID эпика")
	}
	return r.update(name, func(in *Info) {
		if in.GitBranches == nil {
			in.GitBranches = &GitBranchMap{}
		}
		if in.GitBranches.Epics == nil {
			in.GitBranches.Epics = map[string]BranchRef{}
		}
		in.GitBranches.Epics[epicID] = ref
	})
}

// EpicBranch возвращает зарегистрированную ветку эпика проекта.
func (r *Registry) EpicBranch(name, epicID string) (BranchRef, error) {
	inf, err := r.Get(name)
	if err != nil {
		return BranchRef{}, err
	}
	if inf.GitBranches == nil || inf.GitBranches.Epics == nil {
		return BranchRef{}, fmt.Errorf("%w: %s/%s", ErrEpicBranchMissing, name, epicID)
	}
	ref, ok := inf.GitBranches.Epics[epicID]
	if !ok {
		return BranchRef{}, fmt.Errorf("%w: %s/%s", ErrEpicBranchMissing, name, epicID)
	}
	return ref, nil
}

// DeleteEpicBranch снимает запись о ветке эпика из side-реестра. Ветки задач
// эпика чистятся отдельно (DeleteTaskBranch), т.к. реестр не хранит связь
// «задача → эпик»; сервер удаляет их по списку задач эпика доски.
func (r *Registry) DeleteEpicBranch(name, epicID string) error {
	return r.update(name, func(in *Info) {
		if in.GitBranches == nil || in.GitBranches.Epics == nil {
			return
		}
		delete(in.GitBranches.Epics, epicID)
	})
}

// SetTaskBranch записывает ветку задачи в side-реестр проекта (идемпотентно).
func (r *Registry) SetTaskBranch(name, taskID string, ref BranchRef) error {
	if err := validateName(name); err != nil {
		return err
	}
	if taskID == "" {
		return fmt.Errorf("workspace: пустой ID задачи")
	}
	return r.update(name, func(in *Info) {
		if in.GitBranches == nil {
			in.GitBranches = &GitBranchMap{}
		}
		if in.GitBranches.Tasks == nil {
			in.GitBranches.Tasks = map[string]BranchRef{}
		}
		in.GitBranches.Tasks[taskID] = ref
	})
}

// TaskBranch возвращает зарегистрированную ветку задачи проекта.
func (r *Registry) TaskBranch(name, taskID string) (BranchRef, error) {
	inf, err := r.Get(name)
	if err != nil {
		return BranchRef{}, err
	}
	if inf.GitBranches == nil || inf.GitBranches.Tasks == nil {
		return BranchRef{}, fmt.Errorf("%w: %s/%s", ErrTaskBranchMissing, name, taskID)
	}
	ref, ok := inf.GitBranches.Tasks[taskID]
	if !ok {
		return BranchRef{}, fmt.Errorf("%w: %s/%s", ErrTaskBranchMissing, name, taskID)
	}
	return ref, nil
}

// DeleteTaskBranch снимает запись о ветке задачи.
func (r *Registry) DeleteTaskBranch(name, taskID string) error {
	return r.update(name, func(in *Info) {
		if in.GitBranches == nil || in.GitBranches.Tasks == nil {
			return
		}
		delete(in.GitBranches.Tasks, taskID)
	})
}

// update мутирует запись проекта под блокировкой и сохраняет реестр.
func (r *Registry) update(name string, fn func(*Info)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	in, ok := r.projects[name]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	fn(&in)
	r.projects[name] = in
	return r.saveLocked()
}
