// Типы контракта REST/SSE Web UI. Зеркалят server/session.go и board/entity.go.

// Хранение проекта: где и как работает оркестрация.
export type ProjectKind = "dir" | "git";

export interface ProjectMeta {
  project_name: string;
  task: string;
  status: string;
  created_at: string;
  updated_at: string;
  kind?: ProjectKind;
  git_remote?: string;
  git_branch?: string;
  git_base?: string;
}

// Дифф-вью (GET /api/projects/:id/diff).
export interface DiffView {
  kind: "git" | "snap";
  branch?: string;
  base?: string;
  remote?: string;
  diff?: string;
  added?: string[];
  modified?: string[];
  removed?: string[];
  files?: DiffFile[];
  patches?: Record<string, string>;
}

export interface DiffFile {
  path: string;
  status: "added" | "modified" | "removed" | "renamed" | "submodule";
  added: number;
  deleted: number;
  patch?: string;
}

export interface DiffFileView {
  kind: "git";
  path: string;
  status: string;
  patch: string;
}

export interface BranchDiffContext {
  ref: string;
  vs: string;
  label: string;
}

// Сообщение чата (тип события chat; история — тот же формат).
export interface ChatMsg {
  id: string;
  role: string; // user | assistant | tool | status | system | ask
  content: string;
  thinking?: string; // рассуждение модели (thinking-блок)
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

// Накопленный поток строк одного лог-файла (App.tsx). Буфер ограничен, чтобы
// долгая сессия не съела память вкладки: старые строки отбрасываются из
// начала, а dropped хранит их количество — благодаря этому Logboard ведёт
// счёт по АБСОЛЮТНОМУ индексу строки и не путает отброшенный хвост с
// ещё не отрисованным.
export interface LogStream {
  lines: string[]; // последние строки файла
  dropped: number; // сколько строк отброшено из начала (индекс lines[0])
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

// Метрики агентского цикла проекта (GET /api/projects/:id/metrics, Ф-1
// PLAN-2026-09-19-todo-owerview-for-prom.md). Снимок реестра runmetrics:
// длительности и исходы вызовов инструментов по именам инструментов, единицам
// работы (scope) и агентам, плюс именованные метрики шагов.
export interface MetricsTool {
  tool: string;
  calls: number;
  errors: number;
  count: number;
  sum_sec: number;
  mean_sec: number;
  max_sec: number;
  error_count: number;
  error_sum_sec: number;
  // Корзины гистограммы длительности: buckets[i] — вызовов не длиннее
  // duration_buckets[i] секунд (значения совпадают с экспортом Prometheus).
  buckets: number[];
  error_buckets: number[];
}

export interface MetricsScope {
  scope: string;
  calls: number;
  errors: number;
  rounds: number;
  tokens_in: number;
  tokens_out: number;
  total_sec: number;
  mean_sec: number;
}

export interface MetricsAgent {
  agent: string;
  calls: number;
  errors: number;
  tokens_in: number;
  tokens_out: number;
}

export interface MetricsStep {
  scope: string;
  step: string;
  key: string;
  value: number;
}

export interface ProjectMetrics {
  project: string;
  run_id: number;
  since: string;
  uptime_sec: number;
  rounds: number;
  tokens_in: number;
  tokens_out: number;
  tokens_total: number;
  // Цены и стоимость: без заданных LLM_PRICE_IN/OUT price_* и cost равны 0,
  // а cost_known == false — тогда интерфейс не показывает денежную оценку.
  price_in: number;
  price_out: number;
  cost: number;
  cost_known: boolean;
  cost_in?: number;
  cost_out?: number;
  tps: number;
  tool_calls: number;
  tool_errors: number;
  app_log_lines: number;
  unpaired: number;
  overflow: number;
  tools: MetricsTool[];
  scopes: MetricsScope[];
  agents: MetricsAgent[];
  steps: MetricsStep[];
  // Итоги за всё время жизни проекта (Redis-счётчик tokens) в отличие от
  // tokens_in/tokens_out, которые относятся к текущему запуску.
  total_tokens_in: number;
  total_tokens_out: number;
  total_cost: number;
  total_cost_known: boolean;
  currency: string;
  duration_buckets: number[];
}

/** Строка лога рантайма приложения (Ф-2): приходит по WS type="applog"
 *  и списком через GET /api/projects/{id}/applog. */
export interface AppLogLine {
  time: string;
  source?: string;
  line: string;
}

/** Хвост логов рантайма. Пустой буфер — status "skipped" с подсказкой,
 *  а не ошибка: рантайм могли ещё не запускать. */
export interface AppLogView {
  status: "ok" | "skipped";
  lines: AppLogLine[];
  count?: number;
  hint?: string;
}
