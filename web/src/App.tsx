// Оболочка: селектор воркспейса, HITL-затворы, три вкладки. React-канон
// зеркалит server/session.go + runevents + chat/store.go (см. web/src/types.ts).

import { useEffect, useRef, useState } from "react";
import { boardOf, chatHistory, gateDecide, listProjects, openProject, postChat, sessionStop, updateTask } from "./Api";
import { connectLive, type LiveClient } from "./live";
import type { BoardView, ChatMsg, TaskRow, ProjectMeta } from "@/Types";
import { Dashboard } from "./Components/Dashboard";
import { Chatboard } from "./Components/Chatboard";
import { Diffboard } from "./Components/Diffboard";
import { WorkspacePicker } from "./Components/WorkspacePicker";
import { Tabs } from "./Components/Tabs";
import { Badge } from "./Components/Badge";
import { GateBanner } from "./Components/GateBanner";
import { GateEvent } from "./Types";

const BASE = ""; // dev: Vite-прокси /api→backend; прод: embed same-origin.

export function App() {
  const [projects, setProjects] = useState<ProjectMeta[]>([]);
  const [project, setProject] = useState<ProjectMeta | null>(null);
  const [board, setBoard] = useState<BoardView | null>(null);
  const [chat, setChat] = useState<ChatMsg[]>([]);
  const [gate, setGate] = useState<GateEvent | null>(null);
  const [status, setStatus] = useState<string>("idle");
  const [detail, setDetail] = useState<string>("");
  const [tab, setTab] = useState<"board" | "chat" | "diff">("board");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const live = useRef<LiveClient | null>(null);
  const chatEnd = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    listProjects(BASE)
      .then(setProjects)
      .catch((e) => setError(fmtErr(e)));
  }, []);

  const open = async (spec: { path_or_git?: string; git_url?: string }) => {
    setBusy(true);
    setError("");
    try {
      const p = await openProject(BASE, spec);
      setProject(p);
      setProjects((prev) => (prev.some((x) => x.project_name === p.project_name) ? prev : [...prev, p]));
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    if (!project) return;
    live.current?.close();
    setGate(null);
    setChat([]);
    setBoard(null);
    (async () => {
      try {
        const [b, h] = await Promise.all([boardOf(BASE, project.project_name), chatHistory(BASE, project.project_name)]);
        setBoard(b);
        setChat(h);
      } catch (e) {
        setError(fmtErr(e));
      }
    })();

    const l = connectLive(project.project_name, BASE);
    live.current = l;

    l.on("chat", (ev) => {
      try {
        setChat((prev) => [...prev, ev.payload as ChatMsg]);
      } catch {}
    });
    l.on("board", (ev) => {
      try {
        setBoard(ev.payload as BoardView);
      } catch {}
    });
    l.on("gate", (ev) => {
      try {
        setGate(ev.payload as GateEvent);
        setTab("board");
      } catch {}
    });
    l.on("status", (ev) => {
      try {
        const p = ev.payload as { status?: string; detail?: string };
        setStatus(p.status ?? "running");
        setDetail(p.detail ?? "");
      } catch {}
    });

    return () => l.close();
  }, [project?.project_name]);

  const onSend = async (text: string) => {
    if (!project) {
      return;
    }
    const user: ChatMsg = {
      id: `local-${Date.now()}`,
      role: "user",
      content: text,
      agent: "user",
      time: new Date().toISOString(),
    };
    setChat((prev) => [...prev, user]);
    try {
      await postChat(BASE, project.project_name, text);
    } catch (e) {
      setError(fmtErr(e));
    }
  };

  const onGate = async (gateName: "epics" | "tasks", decision: { approved: boolean; reason?: string }) => {
    if (!project) {
      return;
    }
    try {
      await gateDecide(BASE, project.project_name, gateName, decision);
      setGate(null);
    } catch (e) {
      setError(fmtErr(e));
    }
  };

  const onTaskUpdate = async (t: TaskRow, patch: Partial<TaskRow>) => {
    if (!project) {
      return;
    }
    try {
      const next = await updateTask(BASE, project.project_name, t.task_id, patch);
      setBoard((prev) => (prev ? { ...prev, tasks: prev.tasks.map((x) => (x.task_id === next.task_id ? next : x)) } : prev));
    } catch (e) {
      setError(fmtErr(e));
    }
  };

  const onStop = async () => {
    if (!project) {
      return;
    }
    try {
      await sessionStop(BASE, project.project_name);
    } catch (e) {
      setError(fmtErr(e));
    }
  };

  return (
    <div className="app">
      <header className="top">
        <WorkspacePicker projects={projects} current={project} onOpen={open} busy={busy} />
        <Tabs tab={tab} setTab={setTab} />
        <button className="btn danger" onClick={onStop} disabled={!project || status !== "running"}>
          Стоп
        </button>
      </header>

      {project && (
        <div className="statusline">
          <Badge status={status} />
          <span className="detail">{detail}</span>
        </div>
      )}

      {error && (
        <div className="banner err">
          <span>{error}</span>
          <button onClick={() => setError("")}>×</button>
        </div>
      )}

      {gate && <GateBanner gate={gate} onDecide={onGate} />}

      {project ? (
        <main className="panes">
          <section className={tab === "board" ? "pane active" : "pane"}>
            <Dashboard board={board} onTaskUpdate={onTaskUpdate} />
          </section>
          <section className={tab === "chat" ? "pane active" : "pane"}>
            <Chatboard chat={chat} onSend={onSend} endRef={chatEnd} />
          </section>
          <section className={tab === "diff" ? "pane active" : "pane"}>
            <Diffboard project={project.project_name} />
          </section>
        </main>
      ) : (
        <div className="empty">
          <p>Откройте или создайте проект (путь к папке или git-URL).</p>
        </div>
      )}
    </div>
  );
}

/**
 * Безопасно приводит ошибку к строковому виду для пользователя
 */
export function fmtErr(err: unknown): string {
  // 1. Если это стандартный объект Error (или его наследник)
  if (err instanceof Error) {
    return err.message;
  }

  // 2. Если это объект ошибки от Axios или API-запроса (пример структуры)
  if (err && typeof err === 'object' && 'message' in err) {
    return String((err as { message: unknown }).message);
  }

  // 3. Если ошибка пришла в виде обычной строки
  if (typeof err === 'string') {
    return err;
  }

  // 4. Фолбек на случай совсем неизвестной структуры
  return 'Произошла непредвиденная ошибка';
}
