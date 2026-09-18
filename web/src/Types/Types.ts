// Типы контракта REST/SSE Web UI. Зеркалят server/session.go и board/entity.go.

// Статусы зеркалят board/entity.go: единая цепочка для эпиков и задач
// (new -> analysis -> ready -> in_progress -> done; cancelled — терминальный).
// Важно: сервер валидирует переходы (ValidateTransition), поэтому в UI
// переводы между колонками ограничены соседними статусами.
export type Status =
  | "new"
  | "analysis"
  | "ready"
  | "in_progress"
  | "done"
  | "cancelled";

// Совместимые алиасы (ретро): некоторые файлы импортируют Epic/Task/
// BoardSnapshot вместо EpicRow/TaskRow/BoardView — это одно и то же.
export type Epic = EpicRow;
export type Task = TaskRow;
export type BoardSnapshot = BoardView;
export interface GateDecisionBody {
  approved: boolean;
  reason?: string;
}

export interface EpicRow {
  task_id: string;
  project_name: string;
  title: string;
  description: string;
  status: Status;
  dependencies: string[];
  sequence_order: number;
  assigned_role: string;
  architecture_summary: string;
  created_at: string;
  updated_at: string;
}

export interface TaskRow {
  task_id: string;
  project_name: string;
  epic_id: string;
  title: string;
  description: string;
  status: Status;
  dependencies: string[];
  sequence_order: number;
  assignee: string;
  created_at: string;
  updated_at: string;
}

export interface BugRow {
  bug_id: string;
  project_name: string;
  title: string;
  description: string;
  reporter_role: string;
  task_id: string;
  epic_id: string;
  status: string;
  verdict?: string;
  fix_epic_id?: string;
  created_at: string;
  updated_at: string;
}

// Хранение проекта: где и как работает оркестрация.
export type ProjectKind = "dir" | "git";

export interface ProjectMeta {
  project_name: string;
  task: string;
  status: string;
  created_at: string;
  updated_at: string;
  // Ф-2-3: для git-проектов (открытых по git-URL) сервер клонирует репозиторий
  // и ведёт приёмку через MR. Поля дублируют workspace.Info.
  kind?: ProjectKind;
  git_remote?: string;
  git_branch?: string;
  git_base?: string;
}

// Дифф-вью (GET /api/projects/:id/diff). Для git-проектов — unified-дифф
// рабочего каталога от точки отхода (git diff <base>); для остальных —
// списки файлов относительно baseline-снимка.
export interface DiffView {
  kind: "git" | "snap";
  branch?: string;
  base?: string;
  remote?: string;
  diff?: string;
  added?: string[];
  modified?: string[];
  removed?: string[];
  // Ф-3 (ленивая загрузка): для git-проектов вместо полного diff — список файлов;
  // патч конкретного файла отдаётся GET /api/projects/:id/diff?file=<path>.
  files?: DiffFile[];
}

// Один файл в диффе git-проекта (метаданные для ленивой загрузки, Ф-3).
export interface DiffFile {
  path: string;
  status: "added" | "modified" | "removed" | "renamed";
  added: number;
  deleted: number;
}

// Патч конкретного файла git-диффа (GET /api/projects/:id/diff?file=<path>).
export interface DiffFileView {
  kind: "git";
  path: string;
  status: string;
  patch: string;
}

// Снимок доски из GET /api/projects/:id и события board (type=board).
export interface BoardView {
  meta?: ProjectMeta;
  epics: EpicRow[];
  tasks: TaskRow[];
  bugs: BugRow[];
  // Полные счётчики (Ф-3): заполняются при пагинации (limit/offset) или всегда.
  total?: { epics: number; tasks: number; bugs: number };
}

// Сообщение чата (тип события chat; история — тот же формат).
export interface ChatMsg {
  id: string;
  role: string; // user | assistant | tool | status | system
  content: string;
  agent?: string;
  tool?: string;
  ok?: boolean;
  time: string;
}

// HITL-затвор (тип события gate; epics | tasks).
export interface GateEvent {
  gate: string;
  summary: string;
  ids: string[];
}

export interface StatusEvent {
  status: string; // running | waiting | done | stopped | error
  detail?: string;
  gating?: boolean;
  gate?: string;
}

export interface ToolEvent {
  type: string;
  agent?: string;
  tool?: string;
  args?: string;
  result?: string;
  ok?: boolean;
  time: string;
}

// Лог-файл проекта (GET /api/projects/<name>/logs).
export interface LogFileEntry {
  name: string;
  path?: string;
  size: number;
  modified: string;
  content: string;
}

// Ответ панели «Логи».
export interface LogsView {
  dir: string;
  files: LogFileEntry[];
  selected: string;
  truncated?: boolean;
}
