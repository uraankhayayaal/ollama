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
  | "cancelled"
  | "paused";

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
  // Релизная ветка эпика (git-workflow Ф-1, префикс ai/epic/<id>). Пусто, пока
  // ветка не создана.
  git_branch?: string;
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
  // Фича-ветка задачи (git-workflow Ф-1, префикс ai/task/<id>).
  git_branch?: string;
  // Статус, из которого задача приостановлена (пауза эпика, Ф-6):
  // возобновление возвращает её на прежнее место цепочки.
  resume_status?: Status;
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
  // snap: per-file unified-патчи (path → unified diff patch).
  patches?: Record<string, string>;
}

// Один файл в диффе git-проекта (метаданные для ленивой загрузки, Ф-3).
// patch — unified-патч файла (для lazy-loading git и snap-проектов).
export interface DiffFile {
  path: string;
  status: "added" | "modified" | "removed" | "renamed";
  added: number;
  deleted: number;
  patch?: string;
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
  // git-статус проекта (ветки/MR эпиков и задач, Ф-5): заполняется только для
  // git-проектов с созданными ветками, иначе undefined.
  git?: GitView;
}

// Один эпик/задача в git-статусе (Ф-5): ветка + опциональный MR.
export interface GitLinkView {
  branch?: string; // имя ветки (ai/epic/… или ai/task/…)
  branch_url?: string; // web-ссылка на ветку на хостинге
  target?: string; // ветка, в которую вливается MR (main/ветка эпика)
  mr_url?: string; // ссылка на MR/PR (если создан)
  mr_state?: string; // open|merged|closed|"" (неизвестно)
  // has_commits — в ветке есть свои коммиты (Ф-2). Не вычислено/ошибка git —
  // undefined; false — коммитов ещё нет, кнопку «Создать MR» скрываем.
  has_commits?: boolean;
}

// git-статус всего проекта в снимке доски (Ф-5).
export interface GitView {
  base?: string; // базовая ветка (main)
  base_url?: string; // web-ссылка на базовую ветку
  epics?: Record<string, GitLinkView>; // epic_id → ветка/MR
  tasks?: Record<string, GitLinkView>; // task_id → ветка/MR
}

// Сообщение чата (тип события chat; история — тот же формат).
export interface ChatMsg {
  id: string;
  role: string; // user | assistant | tool | status | system | ask
  content: string;
  agent?: string;
  tool?: string;
  ok?: boolean;
  ask?: AskMsg; // структурированный вопрос (role = ask)
  time: string;
}

// Структурированный вопрос ассистента (AskUser): пачка вопросов, показываемая
// пользователю пошагово в карточке-вардин.
export interface AskMsg {
  id: string; // id пачки вопросов (для ответа)
  questions: AskQuestion[];
}

export type AskKind = "single" | "multi";

export interface AskQuestion {
  id: string;
  text: string;
  kind: AskKind;
  allow_custom?: boolean; // показывать ли вариант «свой ответ» (default true)
  options: AskOption[];
}

export interface AskOption {
  id: string;
  label: string;
  recommended?: boolean;
}

// Ответ на один шаг пачки (POST ask/{askID}/answer).
export interface AskAnswerBody {
  question_id: string;
  selected: string[];
  custom?: string;
}

export interface AskAnswerResult {
  ok: boolean;
  answered: number;
  total: number;
}

// HITL-затвор (тип события gate; epics | tasks).
export interface GateEvent {
  gate: string;
  summary: string;
  ids: string[];
}

export interface StatusEvent {
  status: string; // running | waiting | standby | done | stopped | error
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

// Событие «log» (WebSocket link=live.ts): новая строка из хвоста файла.
export interface LogMessage {
  project: string; // имя проекта
  file: string;    // имя файла лога
  line: string;    // одна строка лога
}

// Счётчик токенов проекта (GET /api/projects/:id/tokens и WS type=tokens):
// накопленные за время жизни проекта входные (in) и выходные (out) токены, а
// также последняя реальная скорость генерации (tps, вых. ток/с) из usage
// провайдера. Проект может использовать разные LLM — суммы общие для всех
// раундов.
export interface ProjectTokens {
  in: number;
  out: number;
  tps?: number;
}
