// Side-реестр Merge/Pull Request эпиков и задач git-проектов (Ф-5).
//
// Реестр хранится в том же JSON-файле, что и остальные записи workspace
// (поле Info.GitMergeRequests): сохраняется через штатный saveLocked с
// атомарной заменой, переживает перезапуск сервера. Ключ — project ->
// epic/task ID -> {url, source, target, state}.
//
// MR для эпика: ai/epic/<id> → main (релиз). MR для задачи:
// ai/task/<id> → ai/epic/<родительский эпик>. Состояние State заполняется
// локально ("open" при создании) и уточняется фоновой сверкой с форджем
// (открыт/слит/закрыт).
package workspace

import (
	"errors"
	"fmt"
)

// MRRef — привязка Merge/Pull Request: ссылка, ветка-источник и ветка-цель.
type MRRef struct {
	URL    string `json:"url"`
	Source string `json:"source,omitempty"` // ветка-источник (ai/epic/… или ai/task/…)
	Target string `json:"target,omitempty"` // ветка-цель (main или ветка эпика)
	State  string `json:"state,omitempty"`  // open|merged|closed|"" (неизвестно)
}

// MergeRequestMap — реестр MR эпиков и задач git-проекта.
type MergeRequestMap struct {
	Epics map[string]MRRef `json:"epics,omitempty"` // epic_id → MR эпика (→ main)
	Tasks map[string]MRRef `json:"tasks,omitempty"` // task_id → MR задачи (→ ветка эпика)
}

var (
	// ErrEpicMRMissing — MR эпика не зарегистрирован.
	ErrEpicMRMissing = errors.New("MR эпика не создан")
	// ErrTaskMRMissing — MR задачи не зарегистрирован.
	ErrTaskMRMissing = errors.New("MR задачи не создан")
)

// SetEpicMR записывает MR эпика в side-реестр проекта (идемпотентно).
func (r *Registry) SetEpicMR(name, epicID string, ref MRRef) error {
	if err := validateName(name); err != nil {
		return err
	}
	if epicID == "" {
		return fmt.Errorf("workspace: пустой ID эпика")
	}
	return r.update(name, func(in *Info) {
		if in.GitMergeRequests == nil {
			in.GitMergeRequests = &MergeRequestMap{}
		}
		if in.GitMergeRequests.Epics == nil {
			in.GitMergeRequests.Epics = map[string]MRRef{}
		}
		in.GitMergeRequests.Epics[epicID] = ref
	})
}

// EpicMR возвращает зарегистрированный MR эпика проекта.
func (r *Registry) EpicMR(name, epicID string) (MRRef, error) {
	inf, err := r.Get(name)
	if err != nil {
		return MRRef{}, err
	}
	if inf.GitMergeRequests == nil || inf.GitMergeRequests.Epics == nil {
		return MRRef{}, fmt.Errorf("%w: %s/%s", ErrEpicMRMissing, name, epicID)
	}
	ref, ok := inf.GitMergeRequests.Epics[epicID]
	if !ok {
		return MRRef{}, fmt.Errorf("%w: %s/%s", ErrEpicMRMissing, name, epicID)
	}
	return ref, nil
}

// DeleteEpicMR снимает запись о MR эпика.
func (r *Registry) DeleteEpicMR(name, epicID string) error {
	return r.update(name, func(in *Info) {
		if in.GitMergeRequests == nil || in.GitMergeRequests.Epics == nil {
			return
		}
		delete(in.GitMergeRequests.Epics, epicID)
	})
}

// SetTaskMR записывает MR задачи в side-реестр проекта (идемпотентно).
func (r *Registry) SetTaskMR(name, taskID string, ref MRRef) error {
	if err := validateName(name); err != nil {
		return err
	}
	if taskID == "" {
		return fmt.Errorf("workspace: пустой ID задачи")
	}
	return r.update(name, func(in *Info) {
		if in.GitMergeRequests == nil {
			in.GitMergeRequests = &MergeRequestMap{}
		}
		if in.GitMergeRequests.Tasks == nil {
			in.GitMergeRequests.Tasks = map[string]MRRef{}
		}
		in.GitMergeRequests.Tasks[taskID] = ref
	})
}

// TaskMR возвращает зарегистрированный MR задачи проекта.
func (r *Registry) TaskMR(name, taskID string) (MRRef, error) {
	inf, err := r.Get(name)
	if err != nil {
		return MRRef{}, err
	}
	if inf.GitMergeRequests == nil || inf.GitMergeRequests.Tasks == nil {
		return MRRef{}, fmt.Errorf("%w: %s/%s", ErrTaskMRMissing, name, taskID)
	}
	ref, ok := inf.GitMergeRequests.Tasks[taskID]
	if !ok {
		return MRRef{}, fmt.Errorf("%w: %s/%s", ErrTaskMRMissing, name, taskID)
	}
	return ref, nil
}

// DeleteTaskMR снимает запись о MR задачи.
func (r *Registry) DeleteTaskMR(name, taskID string) error {
	return r.update(name, func(in *Info) {
		if in.GitMergeRequests == nil || in.GitMergeRequests.Tasks == nil {
			return
		}
		delete(in.GitMergeRequests.Tasks, taskID)
	})
}
