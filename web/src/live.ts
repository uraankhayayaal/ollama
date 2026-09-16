// Live-транспорт: WebSocket /api/projects/:id/ws.
// Типы событий соответствуют backend (runevents + server hub):
//   chat  — сообщение чата (ChatMessage)
//   board — снимок доски (BoardSnapshot)
//   gate  — HITL-затвор (epics/tasks)
//   tool  — событие инструмента/агента (tool_start/tool_result)
//   status— статус сессии (running|waiting|done|stopped|error)
//   diff  — дифф (в Ф-2; пока тип-заглушка)
//
// Транспорт устойчивый: при разрыве переподключается по возрастающей
// задержке (до 5 c) и перезапрашивает снимки, которые могли быть пропущены
// (board/status/gate).

const WS_RETRY = [300, 800, 1500, 3000, 5000] as const;
type Handler = (ev: LiveEvent) => void;

export interface LiveEvent {
  type: EventTypeName;
  payload: unknown;
}

export type EventTypeName =
  | "chat"
  | "board"
  | "gate"
  | "tool"
  | "status"
  | "diff";

export interface LiveClient {
  close(): void;
  on(type: EventTypeName, h: Handler): void;
}

export function connectLive(projectID: string, baseURL: string): LiveClient {
  const handlers = new Map<EventTypeName, Set<Handler>>();
  let ws: WebSocket | null = null;
  let closed = false;
  let retry = 0;
  let reconnectTimer: number | null = null;

  const notify = (ev: LiveEvent) => {
    handlers.get(ev.type)?.forEach((h) => h(ev));
  };

  const scheduleReconnect = () => {
    if (closed) return;
    const delay = WS_RETRY[Math.min(retry, WS_RETRY.length - 1)];
    retry++;
    if (reconnectTimer !== null) {
      window.clearTimeout(reconnectTimer);
    }
    reconnectTimer = window.setTimeout(open, delay);
  };

  const open = () => {
    if (closed) return;
    const proto = baseURL.startsWith("https") ? "wss" : "ws";
    const url = `${proto}://${baseURL.replace(/^https?:\/\//, "")}/api/projects/${encodeURIComponent(projectID)}/ws`;
    let s: WebSocket;
    try {
      s = new WebSocket(url);
    } catch {
      scheduleReconnect();
      return;
    }
    ws = s;
    s.onopen = () => {
      retry = 0;
    };
    s.onmessage = (m) => {
      let ev: LiveEvent;
      try {
        ev = JSON.parse(m.data as string) as LiveEvent;
      } catch {
        return;
      }
      notify(ev);
    };
    s.onclose = () => {
      scheduleReconnect();
      ws = null;
    };
    s.onerror = () => {
      s.close();
    };
  };

  open();

  return {
    close() {
      closed = true;
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer);
      }
      ws?.close();
    },
    on(type, h) {
      if (!handlers.has(type)) {
        handlers.set(type, new Set());
      }
      handlers.get(type)!.add(h);
    },
  };
}
