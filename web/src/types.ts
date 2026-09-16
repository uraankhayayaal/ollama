// Типы контракта REST/SSE Web UI. Зеркалят server/session.go и board/entity.go.

export type Status = "todo" | "in_progress" | "review" | "done";

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
  deps: string[];
  order: number;
  assigned_lead: string;
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
  deps: string[];
  order: number;
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

export interface ProjectMeta {
  project_name: string;
  task: string;
  status: string;
  created_at: string;
  updated_at: string;
}

// Снимок доски из GET /api/projects/:id и события board (type=board).
export interface BoardView {
  meta?: ProjectMeta;
  epics: EpicRow[];
  tasks: TaskRow[];
  bugs: BugRow[];
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
