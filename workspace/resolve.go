// Side-реестр активного процесса решения конфликтов эпика (Ф-4).
//
// Запись появляется на POST .../epics/:eid/rebase, когда «main → релизная
// ветка» выявила конфликты: конфликтный worktree больше не снимается, а
// остаётся как «рабочая доска» для модели/инструмента ResolveGitConflicts.
// После обязательной приёмки и финального merge в main (POST .../resolve)
// запись снимается. Сохраняется в том же JSON реестра (поле Info.GitResolving)
// через штатный saveLocked — переживает перезапуск сервера.
package workspace

import (
	"errors"
	"fmt"
	"time"
)

// EpicResolve — активный процесс решения конфликтов релизной ветки эпика.
type EpicResolve struct {
	EpicID    string    `json:"epic_id"`
	Branch    string    `json:"branch"`     // релизная ветка (ai/epic/<id>)
	Worktree  string    `json:"worktree"`   // путь постоянного конфликтного worktree
	Files     []string  `json:"files"`      // «сложные» конфликтные пути, ждущие правок
	StartedAt time.Time `json:"started_at"` // время начала резолва (UTC)
}

// ErrNoResolve — активного процесса решения конфликтов нет.
var ErrNoResolve = errors.New("нет активного процесса решения конфликтов")

// SetEpicResolve сохраняет активный процесс резолва эпика проекта (идемпотентно
// перезаписывает). nil снимает запись.
func (r *Registry) SetEpicResolve(name string, rs *EpicResolve) error {
	if err := validateName(name); err != nil {
		return err
	}
	if rs != nil && rs.EpicID == "" {
		return fmt.Errorf("workspace: пустой ID эпика для резолва")
	}
	return r.update(name, func(in *Info) { in.GitResolving = rs })
}

// EpicResolve возвращает активный процесс резолва проекта или ErrNoResolve.
func (r *Registry) EpicResolve(name string) (*EpicResolve, error) {
	inf, err := r.Get(name)
	if err != nil {
		return nil, err
	}
	if inf.GitResolving == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoResolve, name)
	}
	return inf.GitResolving, nil
}

// ClearEpicResolve снимает процесс резолва (после успешного завершения).
func (r *Registry) ClearEpicResolve(name string) error {
	return r.SetEpicResolve(name, nil)
}
