// App — «Доска + Чат + Дифф» поверх HITL-оркестрации (Ф-1).
// REST: api.ts · Live (WS): live.ts · Типы: types.ts (зеркало backend).
import { 
  // useCallback,
    useEffect,
    useRef,
    useState
} from "react";
import {
  APIError,
  boardOf,
  chatHistory,
  // gateDecide,
  listProjects,
  openProject,
  postChat,
  sessionStop,
  updateTask,
  // listBugs,
} from "./api";
import {
  connectLive,
  // type LiveEvent
} from "./live";
import type {
  BoardView,
  // BugRow,
  ChatMsg,
  EpicRow,
  GateEvent,
  ProjectMeta,
  Status,
  TaskRow,
} from "./types";

const BASE = ""; // dev: Vite-прокси /api→backend; прод: тот же origin (embed).

type Tab = "board" | "chat" | "diff";

const STATUS_LABEL: Record<Status, string> = {
  todo: "todo",
  in_progress: "in_progress",
  review: "review",
  done: "done",
};

function msg(e: unknown): string {
  if (e instanceof APIError) return `${e.status}: ${e.message}`;
  if (e instanceof Error) return e.message;
  return String(e);
}

// function enc(s: string): string {
//   return encodeURIComponent(s);
// }

export function App() {
  const [projects, setProjects] = useState<ProjectMeta[]>([]);
  const [project, setProject] = useState<ProjectMeta | null>(null);
  const [board, setBoard] = useState<BoardView | null>(null);
  const [chat, setChat] = useState<ChatMsg[]>([]);
  const [_, setGate] = useState<GateEvent | null>(null);
  const [status, setStatus] = useState<string>("idle");
  const [detail, setDetail] = useState<string>("");
  const [tab, setTab] = useState<Tab>("board");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>("");

  const live = useRef<ReturnType<typeof connectLive> | null>(null);
  const endRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    listProjects(BASE)
      .then(setProjects)
      .catch((e) => setError(msg(e)));
  }, []);

  useEffect(() => {
    if (!project) return;
    live.current?.close();
    setBoard(null);
    setChat([]);
    setGate(null);
    (async () => {
      try {
        const [b, h] = await Promise.all([
          boardOf(BASE, project.project_name),
          chatHistory(BASE, project.project_name),
        ]);
        setBoard(b);
        setChat(h);
      } catch (e) {
        setError(msg(e));
      }
    })();

    const l = connectLive(project.project_name, BASE);
    live.current = l;

    const apply = (fn: () => void) => {
      try {
        fn();
      } catch {}
    };

    l.on("chat", (ev) => {
      apply(() => {
        const m = ev.payload as unknown as ChatMsg;
        setChat((prev) => [...prev, m]);
      });
    });

    l.on("board", (ev) => {
      apply(() => {
        const b = ev.payload as unknown as BoardView;
        setBoard(b);
      });
    });

    l.on("gate", (ev) => {
      apply(() => {
        const g = ev.payload as unknown as GateEvent;
        setGate(g);
        setTab("board");
      });
    });

    l.on("status", (ev) => {
      apply(() => {
        const p = ev.payload as { status?: string; detail?: string };
        setStatus(p.status ?? "running");
        setDetail(p.detail ?? "");
      });
    });

    return () => {
      l.close();
      live.current = null;
    };
  }, [project?.project_name]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [chat]);

  const onOpen = async (spec: { path_or_git?: string; git_url?: string }) => {
    setBusy(true);
    setError("");
    try {
      const p = await openProject(BASE, spec as { path_or_git?: string; git_url?: string });
      setProjects((prev) => (prev.some((x) => x.project_name === p.project_name) ? prev : [...prev, p]));
      setProject(p);
    } catch (e) {
      setError(msg(e));
    } finally {
      setBusy(false);
    }
  };

  const onSend = async (text: string) => {
    if (!project) return;
    const u: ChatMsg = {
      id: `local-${Date.now()}`,
      role: "user",
      content: text.trim(),
      agent: "вы",
      time: new Date().toISOString(),
    };
    setChat((prev) => [...prev, u]);
    try {
      await postChat(BASE, project.project_name, text.trim());
    } catch (e) {
      setError(msg(e));
    }
  };

  // const onGate = async (gateName: string, decision: { approved: boolean; reason?: string }) => {
  //   if (!project) return;
  //   try {
  //     await gateDecide(BASE, project.project_name, gateName as "epics" | "tasks", decision);
  //     setGate(null);
  //   } catch (e) {
  //     setError(msg(e));
  //   }
  // };

  const onTaskUpdate = async (t: TaskRow, patch: Partial<TaskRow>) => {
    if (!project) return;
    try {
      await updateTask(BASE, project.project_name, t.task_id, patch);
    } catch (e) {
      setError(msg(e));
    }
  };

  const onStop = async () => {
    if (!project) return;
    try {
      await sessionStop(BASE, project.project_name);
    } catch (e) {
      setError(msg(e));
    }
  };

  return (
    <div className="app">
      <header className="top">
        <ProjectPicker
          projects={projects}
          current={project}
          onOpen={onOpen}
          onPick={(name) => setProject(projects.find((x) => x.project_name === name) ?? null)}
          busy={busy}
        />
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
          {error}
          <button onClick={() => setError("")}>×</button>
        </div>
      )}

      {project ? (
        <main className="panes">
          <section className={tab === "board" ? "pane on" : "pane"}>
            <BoardPane board={board} onTaskUpdate={onTaskUpdate} />
          </section>
          <section className={tab === "chat" ? "pane on" : "pane"}>
            <ChatPane chat={chat} onSend={onSend} endRef={endRef} />
          </section>
          <section className={tab === "diff" ? "pane on" : "pane"}>
            <DiffPane project={project.project_name} />
          </section>
        </main>
      ) : (
        <div className="empty">
          <p>Доска появится здесь, когда вы откроете проект.</p>
        </div>
      )}
    </div>
  );
}

// --- селектор проекта ---

function ProjectPicker(props: {
  projects: ProjectMeta[];
  current: ProjectMeta | null;
  onPick: (name: string) => void;
  onOpen: (spec: { path_or_git?: string; git_url?: string }) => void;
  busy: boolean;
}) {
  const [mode, setMode] = useState<"pick" | "new">("pick");
  const [path, setPath] = useState("");
  const [url, setUrl] = useState("");
  const [err, setErr] = useState("");

  const submit = () => {
    const spec =
      mode === "new"
        ? url.trim()
          ? { git_url: url.trim() }
          : { path_or_git: path.trim() }
        : undefined;
    if (!spec || (!spec.path_or_git && !spec.git_url)) {
      setErr("Укажите путь к папке или git-URL");
      return;
    }
    setErr("");
    props.onOpen(spec);
  };

  return (
    <div className="picker">
      <select
        value={props.current?.project_name ?? ""}
        onChange={(e) => e.target.value && props.onPick(e.target.value)}
        disabled={props.busy}
      >
        <option value="">— выбрать —</option>
        {props.projects.map((p) => (
          <option key={p.project_name} value={p.project_name}>
            {p.project_name}
          </option>
        ))}
      </select>
      <button className="btn" onClick={() => setMode((m) => (m === "pick" ? "new" : "pick"))}>
        {mode === "pick" ? "Новый проект" : "Выбрать"}
      </button>
      {mode === "new" && (
        <span className="newrow">
          <input placeholder="путь ↵" value={path} onChange={(e) => setPath(e.target.value)} onKeyDown={(e) => e.key === "Enter" && submit()} />
          <span className="sep">или</span>
          <input placeholder="git-url" value={url} onChange={(e) => setUrl(e.target.value)} onKeyDown={(e) => e.key === "Enter" && submit()} />
          <button className="btn primary" onClick={submit} disabled={props.busy}>
            Открыть
          </button>
          {err && <span className="err">{err}</span>}
        </span>
      )}
    </div>
  );
}

// --- вкладки ---

function Tabs(props: { tab: Tab; setTab: (t: Tab) => void }) {
  const tabs: { id: Tab; label: string }[] = [
    { id: "board", label: "Доска" },
    { id: "chat", label: "Чат" },
    { id: "diff", label: "Дифф" },
  ];
  return (
    <nav className="tabs">
      {tabs.map((t) => (
        <button key={t.id} className={props.tab === t.id ? "tab on" : "tab"} onClick={() => props.setTab(t.id)}>
          {t.label}
        </button>
      ))}
    </nav>
  );
}

// --- доска ---

function BoardPane(props: { board: BoardView | null; onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void }) {
  const { board } = props;
  if (!board) {
    return <div className="placeholder">Доска загружается…</div>;
  }

  const statuses: Status[] = ["todo", "in_progress", "review", "done"];

  return (
    <div className="board">
      <div className="cols">
        {statuses.map((s) => (
          <div className="col" key={s}>
            <h3>{STATUS_LABEL[s]}</h3>
            {board.tasks
              .filter((t) => t.status === s)
              .sort((a, b) => a.order - b.order)
              .map((t) => (
                <TaskCard key={t.task_id} task={t} epic={board.epics.find((e) => e.task_id === t.epic_id)} onChange={props.onTaskUpdate} />
              ))}
          </div>
        ))}
      </div>
      <div className="bugs">
        <h3>Баг-репорты ({board.bugs.length})</h3>
        {board.bugs.map((b) => (
          <div className="bug" key={b.bug_id}>
            <span className={b.verdict ? "ok" : "warn"}>[{b.status}]</span> {b.title}
          </div>
        ))}
      </div>
    </div>
  );
}

function TaskCard(props: {
  task: TaskRow;
  epic?: EpicRow;
  onChange: (t: TaskRow, patch: Partial<TaskRow>) => void;
}) {
  const { task, epic } = props;
  const [title, setTitle] = useState(task.title);
  const [_, setSaving] = useState(false);

  useEffect(() => setTitle(task.title), [task.title]);

  const save = () => {
    if (title === task.title) return;
    setSaving(true);
    props.onChange(task, { title });
    setSaving(false);
  };

  return (
    <div className={`task ${task.status}`}>
      <div className="thead">
        <span className="tid">{task.task_id.slice(0, 8)}</span>
        <select value={task.status} onChange={(e) => props.onChange(task, { status: e.target.value as Status })}>
          {(["todo", "in_progress", "review", "done"] as Status[]).map((s) => (
            <option key={s} value={s}>
              {STATUS_LABEL[s]}
            </option>
          ))}
        </select>
      </div>
      <input className="ttitle" value={title} onChange={(e) => setTitle(e.target.value)} onBlur={save} onKeyDown={(e) => e.key === "Enter" && save()} />
      <div className="tmeta">
        {epic && <span className="epic">{epic.title.slice(0, 24)}</span>}
        {task.assignee && <span className="who">{task.assignee}</span>}
        {task.deps.length > 0 && <span className="deps">↳ {task.deps.length}</span>}
      </div>
    </div>
  );
}

// --- чат ---

function ChatPane(props: { chat: ChatMsg[]; onSend: (t: string) => void; endRef: React.RefObject<HTMLDivElement | null> }) {
  const [text, setText] = useState("");

  return (
    <div className="chat">
      <div className="lines">
        {props.chat.map((m) => (
          <div key={m.id} className={`line ${m.role}`}>
            <span className="who">{m.role === "user" ? "вы" : m.role === "assistant" ? "агент" : m.agent || m.role}</span>
            <div className="body">{m.content}</div>
            <time>{fmt(m.time)}</time>
          </div>
        ))}
        <div ref={props.endRef} />
      </div>
      <div className="input">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
              props.onSend(text);
              setText("");
            }
          }}
          placeholder="Сообщение. ⌘-Enter — отправить."
        />
        <button
          className="btn primary"
          onClick={() => {
            props.onSend(text);
            setText("");
          }}
        >
          Отправить
        </button>
      </div>
    </div>
  );
}

// --- дифф (Ф-2: полный вью на ветке) ---

function DiffPane(props: { project: string }) {
  return (
    <div className="diff">
      <div className="placeholder">
        Дифф появится здесь, когда первый эпик будет принят и задача выполнена (Ф-2). Проект: <code>{props.project}</code>.
      </div>
    </div>
  );
}

// --- мелочи ---

function Badge(props: { status: string }) {
  const map: Record<string, string> = {
    running: "выполняется",
    waiting: "ожидает решения",
    done: "готово",
    stopped: "остановлено",
    error: "ошибка",
  };
  return <span className={`badge st-${props.status}`}>{map[props.status] ?? props.status}</span>;
}

function fmt(iso: string) {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}
