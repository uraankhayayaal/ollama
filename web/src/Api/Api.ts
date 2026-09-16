// REST-клиент. Базовый URL передаётся извне (Vite dev-прокси на backend:
// /api → :8090; прод — тот же origin через embed).
//
// Все типы согласованы с web/src/types.ts — каноном, зеркалящим
// server/session.go и board/entity.go.

import type {
  BoardView,
  BugRow,
  ChatMsg,
  // EpicRow,
  ProjectMeta,
  // Status,
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

async function req<T>(method: string, url: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers:
      body === undefined ? undefined : { "content-type": "application/json" },
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

// --- проекты ---

export async function listProjects(base: string): Promise<ProjectMeta[]> {
  return req<ProjectMeta[]>("GET", `${base}/api/projects`);
}

export async function openProject(
  base: string,
  spec: { path_or_git?: string; git_url?: string },
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
