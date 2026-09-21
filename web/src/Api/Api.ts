// REST-клиент. Базовый URL передаётся извне (Vite dev-прокси на backend:
// /api → :8090; прод — тот же origin через embed).
//
// Все типы согласованы с web/src/types.ts — каноном, зеркалящим
// server/session.go и board/entity.go.

import type {
  AskAnswerBody,
  AskAnswerResult,
  BoardView,
  BugRow,
  ChatMsg,
  DiffFileView,
  DiffView,
  LogsView,
  ProjectMeta,
  ProjectTokens,
  TaskRow,
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

export async function boardOf(
  base: string,
  project: string,
): Promise<BoardView> {
  return req<BoardView>("GET", `${base}/api/projects/${enc(project)}`);
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

// --- редактирование (Ф-2) ---

export async function updateTask(
  base: string,
  project: string,
  taskID: string,
  patch: Partial<TaskRow>,
): Promise<TaskRow> {
  return req<TaskRow>(
    "PUT",
    `${base}/api/projects/${enc(project)}/tasks/${enc(taskID)}`,
    patch,
  );
}

export async function listBugs(
  base: string,
  project: string,
): Promise<BugRow[]> {
  return req<BugRow[]>("GET", `${base}/api/projects/${enc(project)}/bugs`);
}

// Удаление эпика вместе с его задачами. Допустимо, пока ни одна задача эпика
// не взята в работу специалистом (иначе сервер вернёт 400).
export async function deleteEpic(
  base: string,
  project: string,
  epicID: string,
): Promise<{ ok: boolean }> {
  return req<{ ok: boolean }>(
    "DELETE",
    `${base}/api/projects/${enc(project)}/epics/${enc(epicID)}`,
  );
}

// «Залить в main» (Ф-3): вливает релизную ветку эпика в базовую ветку проекта
// (git_base) без конфликтов — merge-коммит + push. Только для done-эпиков;
// при конфликтах сервер вернёт 409 (вход инструмента авто-резолва Ф-4).
export async function releaseEpic(
  base: string,
  project: string,
  epicID: string,
): Promise<{
  status: string;
  branch: string;
  source: string;
  already_merged: boolean;
}> {
  return req(
    "POST",
    `${base}/api/projects/${enc(project)}/epics/${enc(epicID)}/release`,
    {},
  );
}

// Создание релизной ветки эпика ai/epic/<id> от базовой ветки проекта
// (git-workflow, Ф-5). Идемпотентно. Кнопка «Создать ветку эпика» в модалке.
export async function createEpicBranch(
  base: string,
  project: string,
  epicID: string,
): Promise<{ epic_id: string; branch: string; base: string }> {
  return req(
    "POST",
    `${base}/api/projects/${enc(project)}/epics/${enc(epicID)}/branch`,
    {},
  );
}

// Создание фича-ветки задачи ai/task/<id> от ветки её эпика. Требует, чтобы
// ветка эпика уже была создана. Идемпотентно. Кнопка «Создать ветку задачи».
export async function createTaskBranch(
  base: string,
  project: string,
  taskID: string,
): Promise<{ task_id: string; branch: string; base: string }> {
  return req(
    "POST",
    `${base}/api/projects/${enc(project)}/tasks/${enc(taskID)}/branch`,
    {},
  );
}

// «Создать MR» эпика (Ф-5): пушит релизную ветку ai/epic/<id> в remote и
// открывает MR → main через фордж. Идемпотентно: повторный вызов возвращает
// существующую ссылку. Блок «ветка/MR» в модалке эпика.
export async function createEpicMR(
  base: string,
  project: string,
  epicID: string,
): Promise<{ epic_id: string; mr_url: string; source: string; target: string }> {
  return req(
    "POST",
    `${base}/api/projects/${enc(project)}/epics/${enc(epicID)}/mr`,
    {},
  );
}

// «Создать MR» задачи (Ф-5): пушит фича-ветку ai/task/<id> и открывает MR →
// ветку эпика. Идемпотентно (см. createEpicMR). Блок «ветка/MR» в модалке задачи.
export async function createTaskMR(
  base: string,
  project: string,
  taskID: string,
): Promise<{ task_id: string; mr_url: string; source: string; target: string }> {
  return req(
    "POST",
    `${base}/api/projects/${enc(project)}/tasks/${enc(taskID)}/mr`,
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
): Promise<DiffView> {
  return req<DiffView>("GET", `${base}/api/projects/${enc(project)}/diff`);
}

// Патч конкретного файла git-диффа (ленивая загрузка, Ф-3).
export async function projectDiffFile(
  base: string,
  project: string,
  path: string,
): Promise<DiffFileView> {
  return req<DiffFileView>(
    "GET",
    `${base}/api/projects/${enc(project)}/diff?file=${enc(path)}`,
  );
}

// Принятие git-проекта: коммит + push + создание MR/PR через фордж.
export async function acceptProject(
  base: string,
  project: string,
  body?: { title?: string; description?: string; message?: string },
): Promise<{ url: string; branch: string; base: string }> {
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
