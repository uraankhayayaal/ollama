// Оболочка: селектор воркспейса, HITL-затворы, три вкладки. React-канон
// зеркалит server/session.go + runevents + chat/store.go (см. web/src/types.ts).
// Ф-3: аутентификация (AI_WEB_PASSWORD) — экран входа, защита 401-ответами.

import { useEffect, useRef, useState } from "react";
import { authStatus, boardOf, chatHistory, gateDecide, listProjects, logout, openProject, postChat, sessionStop, updateTask } from "./Api";
import { connectLive, type LiveClient } from "./live";
import type { BoardView, ChatMsg, TaskRow, ProjectMeta } from "@/Types";
import { Dashboard } from "./Components/Dashboard";
import { Chatboard } from "./Components/Chatboard";
import { Diffboard } from "./Components/Diffboard";
import { Login } from "./Components/Login";
import { WorkspacePicker } from "./Components/WorkspacePicker";
import { Tabs } from "./Components/Tabs";
import { Badge } from "./Components/Badge";
import { GateBanner } from "./Components/GateBanner";
import { ToolBar } from "./Components/ToolBar";
import { GateEvent } from "./Types";

const BASE = ""; // dev: Vite-прокси /api→backend; прод: embed same-origin.

type AuthPhase = "checking" | "ok" | "denied";

export function App() {
  const [auth, setAuth] = useState<AuthPhase>("checking");
  const [protectedMode, setProtectedMode] = useState(false);
  const [projects, setProjects] = useState<ProjectMeta[]>([]);
  const [project, setProject] = useState<ProjectMeta | null>(null);
  const [board, setBoard] = useState<BoardView | null>(null);
  const [chat, setChat] = useState<ChatMsg[]>([]);
  // live — «плавающее» потоковое сообщение модели (стриминг, Ф-3): заполняется
  // событиями chat_delta и схлопывается в историю при финальном chat.
  const [live, setLive] = useState<{ id: string; agent: string; content: string } | null>(null);
  const [gate, setGate] = useState<GateEvent | null>(null);
  const [status, setStatus] = useState<string>("idle");
  const [detail, setDetail] = useState<string>("");
  const [tab, setTab] = useState<"board" | "chat" | "diff">("board");
  const [showDiffboard, setShowDiffboard] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const liveClient = useRef<LiveClient | null>(null);
  const chatEnd = useRef<HTMLDivElement | null>(null);

  // Начальная проверка аутентификации: /api/auth отдаёт статус и CSRF
  // текущей httpOnly-сессии; если сервер без пароля — сразу ok.
  useEffect(() => {
    authStatus(BASE)
      .then((st) => {
        setProtectedMode(st.login);
        setAuth(st.ok ? "ok" : "denied");
        return st.ok ? listProjects(BASE) : null;
      })
      .then((pr) => {
        if (pr) setProjects(pr);
      })
      .catch(fail);
  }, []);

  // fail — единая обработка ошибок: протухшая сессия (401) → экран входа.
  const fail = (e: unknown) => {
    if (e && typeof e === "object" && (e as { status?: number }).status === 401) {
      setAuth("denied");
      return;
    }
    setError(fmtErr(e));
  };

  const open = async (spec: { path_or_git?: string; git_url?: string }) => {
    setBusy(true);
    setError("");
    try {
      const p = await openProject(BASE, spec);
      setProject(p);
      setProjects((prev) => (prev.some((x) => x.project_name === p.project_name) ? prev : [...prev, p]));
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };

  const onLoggedIn = async () => {
    setAuth("ok");
    try {
      setProjects(await listProjects(BASE));
    } catch (e) {
      fail(e);
    }
  };

  const onLogout = async () => {
    await logout(BASE);
    setAuth("denied");
  };

  useEffect(() => {
    if (!project) return;
    liveClient.current?.close();
    setGate(null);
    setChat([]);
    setLive(null);
    setBoard(null);
    (async () => {
      try {
        const [b, h] = await Promise.all([boardOf(BASE, project.project_name), chatHistory(BASE, project.project_name)]);
        setBoard(b);
        setChat(h);
      } catch (e) {
        fail(e);
      }
    })();

    const l = connectLive(project.project_name, BASE);
    liveClient.current = l;

    l.on("chat", (ev) => {
      try {
        const m = ev.payload as ChatMsg;
        // Финальное сообщение модели закрывает потоковый «плавающий» пузырь.
        if (m.role === "assistant") {
          setLive(null);
        }
        setChat((prev) => [...prev, m]);
      } catch {}
    });
    l.on("chat_delta", (ev) => {
      try {
        const p = ev.payload as { content?: string; agent?: string; stream_id?: string };
        setLive({ id: p.stream_id ?? "live", agent: p.agent ?? "assistant", content: p.content ?? "" });
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
      fail(e);
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
      fail(e);
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
      fail(e);
    }
  };

  const onStop = async () => {
    if (!project) {
      return;
    }
    try {
      await sessionStop(BASE, project.project_name);
    } catch (e) {
      fail(e);
    }
  };

  if (auth === "checking") {
    return (
      <div className="app">
        <p className="hint">Подключаюсь…</p>
      </div>
    );
  }

  if (auth === "denied") {
    return <Login base={BASE} onLoggedIn={() => void onLoggedIn()} />;
  }

  return (
    <div className="app">
      <header className="top">
        <WorkspacePicker projects={projects} current={project} onOpen={open} busy={busy} />
        <Tabs tab={tab} setTab={setTab} />
        <div className="head-actions">
          <button className="btn danger" onClick={onStop} disabled={!project || status !== "running"}>
            Стоп
          </button>
          {protectedMode && (
            <button className="btn" onClick={() => void onLogout()}>
              Выход
            </button>
          )}
        </div>
      </header>

      <ToolBar 
        showDiffboard={showDiffboard} 
        onToggleDiffboard={() => setShowDiffboard(!showDiffboard)} 
      />

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
            <Dashboard board={board} onTaskUpdate={onTaskUpdate} onGateDecide={onGate} />
          </section>
          <section className={tab === "chat" ? "pane active" : "pane"}>
            <Chatboard chat={chat} live={live} onSend={onSend} endRef={chatEnd} />
          </section>
          <section className={tab === "diff" ? "pane active" : "pane"}>
            <Diffboard project={project.project_name} kind={project.kind} showDiffboard={showDiffboard} toggleDiffboard={() => setShowDiffboard(!showDiffboard)} />
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