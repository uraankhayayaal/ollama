// REST-клиент. Базовый URL передаётся извне (Vite dev-прокси на backend:
// /api → :8090; прод — тот же origin через embed).
//
// Все типы согласованы с web/src/types.ts — каноном, зеркалящим
// server/session.go и board/entity.go.

import type {
  AppLogView,
  AskAnswerBody,
  AskAnswerResult,
  ChatMsg,
  DiffFileView,
  DiffView,
  LogsView,
  ProjectMeta,
  ProjectMetrics,
  ProjectTokens,
} from "@/Types";

export class APIError extends Error {
  constructor(
    public status: number,
    message: string,
    public detail?: unknown,
  ) {
    super(message);
  }
}

// CSRF-токен текущей сессии (Ф-3). При включённой аутентификации
// (AI_WEB_PASSWORD) мутации (POST/PUT/DELETE) требуют X-CSRF-Token.
// Токен берётся из /api/auth при загрузке или из ответа /api/login.
let csrfToken = "";

export function setCSRF(token: string): void {
  csrfToken = token;
}

export function clearCSRF(): void {
  csrfToken = "";
}

function mutationHeaders(hasBody: boolean): Record<string, string> {
  const h: Record<string, string> = {};
  if (hasBody) {
    h["content-type"] = "application/json";
  }
  if (csrfToken) {
    h["x-csrf-token"] = csrfToken;
  }
  return h;
}

async function req<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers:
      body === undefined && !csrfToken
        ? undefined
        : mutationHeaders(body !== undefined),
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let detail: unknown;
    let text = "";
    try {
      const ct = res.headers.get("content-type") ?? "";
      if (ct.includes("application/json")) {
        const j = (await res.json()) as { message?: string };
        text = j.message ?? res.statusText;
        detail = j;
      } else {
        text = await res.text();
      }
    } catch {
      text = res.statusText;
    }
    throw new APIError(res.status, text || res.statusText, detail);
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

function enc(s: string): string {
  return encodeURIComponent(s);
}

// --- аутентификация (Ф-3) ---

export interface AuthStatus {
  ok: boolean;
  // login=false — сервер без AI_WEB_PASSWORD (защита отключена);
  // login=true  — требуется вход; ok отражает состояние текущей сессии.
  login: boolean;
  csrf: string;
}

// Статус аутентификации и CSRF-токен текущей (httpOnly) сессии.
export async function authStatus(base: string): Promise<AuthStatus> {
  const st = await req<AuthStatus>("GET", `${base}/api/auth`);
  if (st.ok && st.csrf) {
    setCSRF(st.csrf);
  }
  return st;
}

// Вход по паролю (AI_WEB_PASSWORD). Успех → httpOnly-сессия + CSRF.
export async function login(
  base: string,
  password: string,
): Promise<AuthStatus> {
  const st = await req<AuthStatus>("POST", `${base}/api/login`, { password });
  if (st.ok && st.csrf) {
    setCSRF(st.csrf);
  }
  return st;
}

// Выход: уничтожает сессию на сервере и сбрасывает локальный CSRF.
export async function logout(base: string): Promise<void> {
  try {
    await req<{ ok: boolean }>("POST", `${base}/api/logout`, {});
  } finally {
    clearCSRF();
  }
}

// --- провайдеры и модели ---

export interface ProviderInfo {
  name: string;
  models: string[];
  default_model: string;
  large_model: string;
}

export interface ProvidersResponse {
  providers: ProviderInfo[];
  current_provider: string;
  current_model: string;
  current_large_model: string;
  // override=true — выбор сделан в UI, false — взят из переменных окружения.
  override: boolean;
  // error — непусто, если текущий выбор не удалось создать (провайдер недоступен).
  error?: string;
}

export interface SelectProviderBody {
  provider: string;
  model: string;
  large_model?: string;
  reset?: boolean;
}

export interface SelectProviderResult {
  ok: boolean;
  provider: string;
  model: string;
  large_model: string;
  describe: string;
  // applies_to_running=true — оркестрация уже идёт на прежней модели.
  applies_to_running: boolean;
  message?: string;
}

export async function getProviders(base: string): Promise<ProvidersResponse> {
  return req<ProvidersResponse>("GET", `${base}/api/providers`);
}

export async function selectProvider(
  base: string,
  body: SelectProviderBody,
): Promise<SelectProviderResult> {
  return req<SelectProviderResult>("POST", `${base}/api/providers/select`, body);
}

// --- проекты ---

export async function listProjects(base: string): Promise<ProjectMeta[]> {
  return req<ProjectMeta[]>("GET", `${base}/api/projects`);
}

export async function openProject(
  base: string,
  spec: { path_or_git?: string },
): Promise<ProjectMeta> {
  return req<ProjectMeta>("POST", `${base}/api/projects`, spec);
}

// --- чат / HITL ---

export async function postChat(
  base: string,
  project: string,
  message: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "POST",
    `${base}/api/projects/${enc(project)}/chat`,
    { message },
  );
}

// «Продолжить»: запускает/возобновляет Kanban-оркестрацию на текущей доске.
// В чат ничего не отправляется — раннер работает над эпиками/задачами доски
// своим циклом (параллельно чату).
export async function continueProject(
  base: string,
  project: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "POST",
    `${base}/api/projects/${enc(project)}/continue`,
    {},
  );
}

// Фоновая индексация RAG-памяти проекта (кнопка «Индекс RAG», Ф-5). Запускает
// горутину на сервере — цикл оркестрации не блокируется; результат (файлы/
// чанки) приходит статусом в чат.
export async function indexProject(
  base: string,
  project: string,
): Promise<{ ok: boolean; message: string }> {
  return req<{ ok: boolean; message: string }>(
    "POST",
    `${base}/api/projects/${enc(project)}/index`,
    {},
  );
}

export async function chatHistory(
  base: string,
  project: string,
  limit = 200,
): Promise<ChatMsg[]> {
  return req<ChatMsg[]>(
    "GET",
    `${base}/api/projects/${enc(project)}/chat?limit=${limit}`,
  );
}

// «Кофе-брейк»: очищает историю диалога на сервере, чтобы UI и модель
// начали общение с чистого листа.
export async function clearChat(
  base: string,
  project: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "DELETE",
    `${base}/api/projects/${enc(project)}/chat`,
  );
}

// Накопленные токены проекта (вход/выход).
export async function projectTokens(
  base: string,
  project: string,
): Promise<ProjectTokens> {
  return req<ProjectTokens>(
    "GET",
    `${base}/api/projects/${enc(project)}/tokens`,
  );
}

// Метрики агентского цикла проекта (Ф-1): снимок для дашборда «Метрики».
/** Хвост логов рантайма приложения (Ф-2). Пустой буфер приходит как
 *  status:"skipped" — это не ошибка, а «рантайм ещё не запускали». */
export async function projectAppLogs(base: string, project: string, lines = 300): Promise<AppLogView> {
  const res = await fetch(`${base}/api/projects/${encodeURIComponent(project)}/applog?lines=${lines}`);
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return (await res.json()) as AppLogView;
}

export async function projectMetrics(
  base: string,
  project: string,
): Promise<ProjectMetrics> {
  return req<ProjectMetrics>(
    "GET",
    `${base}/api/projects/${enc(project)}/metrics`,
  );
}

// Сброс телеметрии текущего запуска (накопленные токены не затрагиваются).
export async function resetProjectMetrics(
  base: string,
  project: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "POST",
    `${base}/api/projects/${enc(project)}/metrics/reset`,
    {},
  );
}

// Решение по затвору (эпик или задача).
export async function gateDecide(
  base: string,
  project: string,
  gate: "epics" | "tasks",
  body: { approved: boolean; reason?: string },
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "POST",
    `${base}/api/projects/${enc(project)}/${gate}/decide`,
    body,
  );
}

// Ответ на один шаг структурированного вопроса ассистента (AskUser).
export async function answerAsk(
  base: string,
  project: string,
  askID: string,
  body: AskAnswerBody,
): Promise<AskAnswerResult> {
  return req<AskAnswerResult>(
    "POST",
    `${base}/api/projects/${enc(project)}/ask/${enc(askID)}/answer`,
    body,
  );
}

export async function sessionStop(
  base: string,
  project: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "POST",
    `${base}/api/projects/${enc(project)}/session/stop`,
    {},
  );
}

// Остановка генерации чат-ассистента (кнопка «Стоп»).
export async function chatStop(
  base: string,
  project: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "POST",
    `${base}/api/projects/${enc(project)}/chat/stop`,
    {},
  );
}

// --- приёмка (Ф-2-3) ---

// Дифф предложенных изменений: для git-проектов — список файлов (метаданные,
// Ф-3: ленивая загрузка; патч файла — через projectDiffFile); для остальных —
// списки добавленных/изменённых/удалённых файлов.
export async function projectDiff(
  base: string,
  project: string,
  ref?: string,
  vs?: string,
): Promise<DiffView> {
  const query = new URLSearchParams();
  if (ref) query.set("ref", ref);
  if (vs) query.set("vs", vs);
  const suffix = query.size ? `?${query.toString()}` : "";
  return req<DiffView>("GET", `${base}/api/projects/${enc(project)}/diff${suffix}`);
}

// Патч конкретного файла git-диффа (ленивая загрузка, Ф-3).
export async function projectDiffFile(
  base: string,
  project: string,
  path: string,
  ref?: string,
  vs?: string,
): Promise<DiffFileView> {
  const query = new URLSearchParams({ file: path });
  if (ref) query.set("ref", ref);
  if (vs) query.set("vs", vs);
  return req<DiffFileView>(
    "GET",
    `${base}/api/projects/${enc(project)}/diff?${query.toString()}`,
  );
}

// Принятие git-проекта: коммит + push + создание MR/PR через фордж.
export async function acceptProject(
  base: string,
  project: string,
  body?: { title?: string; description?: string; message?: string },
): Promise<{ url: string; branch: string; base: string; repositories?: Record<string, { url: string; branch: string; base: string }> }> {
  return req(
    "POST",
    `${base}/api/projects/${enc(project)}/accept`,
    body ?? {},
  );
}

// Отклонение фича-ветки git-проекта: деплой на remote + возврат на базу.
export async function rejectBranch(
  base: string,
  project: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "POST",
    `${base}/api/projects/${enc(project)}/reject-branch`,
    {},
  );
}

// --- логи (панель «Логи») ---

// Лог-файлы проекта из каталога logs/ (глобального и внутри проекта).
export async function projectLogs(
  base: string,
  project: string,
): Promise<LogsView> {
  return req<LogsView>("GET", `${base}/api/projects/${enc(project)}/logs`);
}

// Очистка логов проекта: сервер обрезает те же файлы, что отдаёт projectLogs.
// Действие необратимое, поэтому подтверждение остаётся на стороне UI.
export async function clearProjectLogs(
  base: string,
  project: string,
): Promise<{ ok: boolean; cleared: string[]; count: number }> {
  return req<{ ok: boolean; cleared: string[]; count: number }>(
    "DELETE",
    `${base}/api/projects/${enc(project)}/logs`,
  );
}

// Разбор логов ассистентом (кнопка «Разобрать» в панели логов). Отдельный
// ход вместо обычного сообщения: сервер собирает детерминированный дайджест
// (сырой лог в промпт не идёт) и отвечает в чат. Пустой разбор (empty) и
// повтор того же лога (cached) НЕ зовут модель — об этом сообщает сервер.
export interface LogAnalysisReq {
  file?: string;
  level?: string;
  query?: string;
}

export async function analyzeProjectLogs(
  base: string,
  project: string,
  args: LogAnalysisReq,
): Promise<{ ok: boolean; empty?: boolean; cached?: boolean; findings?: number }> {
  return req<{ ok: boolean; empty?: boolean; cached?: boolean; findings?: number }>(
    "POST",
    `${base}/api/projects/${enc(project)}/chat`,
    { log_analysis: args },
  );
}
