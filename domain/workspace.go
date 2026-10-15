package domain

import "time"

// Типы реестра рабочих пространств (Этап 8, H5e): данные записей проекта
// (имя, тип, путь, git-поля, side-реестры веток/MR/резолва) вынесены в домен,
// чтобы application-слой (gitflow) не зависел от outer-пакета workspace.
// Сам реестр (Registry и его методы) остаётся в workspace (реализация порта
// OpenRegistry); домен владеет только данными.

// Kind — тип рабочего проекта.
type Kind string

const (
	KindTemp Kind = "temp" // temp/<имя> в корне модуля (агентская генерация)
	KindDir  Kind = "dir"  // произвольная локальная папка
	KindGit  Kind = "git"  // git-проект (клон/worktree)
)

// BranchRef — ветка и её база (side-реестр git-workflow).
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

// MRRef — merge request (URL + ветки + состояние).
type MRRef struct {
	URL    string `json:"url"`
	Source string `json:"source,omitempty"` // ветка-источник (ai/epic/… или ai/task/…)
	Target string `json:"target,omitempty"` // ветка-цель (main или ветка эпика)
	State  string `json:"state,omitempty"`  // open|merged|closed|"" (неизвестно)
}

// MergeRequestMap — реестр MR эпиков и задач git-проекта.
type MergeRequestMap struct {
	Project *MRRef           `json:"project,omitempty"`
	Epics   map[string]MRRef `json:"epics,omitempty"` // epic_id → MR эпика (→ main)
	Tasks   map[string]MRRef `json:"tasks,omitempty"` // task_id → MR задачи (→ ветка эпика)
}

// EpicResolve — активный процесс решения конфликтов эпика (Ф-4).
type EpicResolve struct {
	EpicID    string    `json:"epic_id"`
	Branch    string    `json:"branch"`     // релизная ветка (ai/epic/<id>)
	Worktree  string    `json:"worktree"`   // путь постоянного конфликтного worktree
	Files     []string  `json:"files"`      // «сложные» конфликтные пути, ждущие правок
	StartedAt time.Time `json:"started_at"` // время начала резолва (UTC)
}

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
