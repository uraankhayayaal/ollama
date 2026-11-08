import { useCallback, useEffect, useRef, useState } from "react";
import {
  analyzeProjectLogs,
  authStatus,
  answerAsk,
  chatHistory,
  chatStop,
  clearChat,
  listProjects,
  logout,
  openProject,
  postChat,
  projectTokens,
  indexProject,
} from "./Api";
import { connectLive, type LiveClient } from "./live";
import type {
  AppLogLine,
  AskAnswerBody,
  AskAnswerResult,
  BranchDiffContext,
  ChatMsg,
  LogMessage,
  LogStream,
  ProjectMeta,
  ProjectTokens,
} from "@/Types";
import { Chatboard } from "./Components/Chatboard";
import { TokensCounter } from "./Components/TokensCounter";
import { Diffboard } from "./Components/Diffboard";
import { Logboard } from "./Components/Logboard";
import { Metricsboard } from "./Components/Metricsboard";
import { Runtimes } from "./Components/Runtimes";
import { Login } from "./Components/Login";
import { WorkspacePicker } from "./Components/WorkspacePicker";
import { Badge } from "./Components/Badge";
import { ModelSelector } from "./Components/ModelSelector";

const BASE = "";
const LAST_PROJECT_KEY = "ollama.last-project";

type AuthPhase = "checking" | "ok" | "denied";

export function App() {
  const [auth, setAuth] = useState<AuthPhase>("checking");
  const [protectedMode, setProtectedMode] = useState(false);
  const [projects, setProjects] = useState<ProjectMeta[]>([]);
  const [logLines, setLogLines] = useState<Map<string, LogStream>>(new Map());
  const [appLogLines, setAppLogLines] = useState<AppLogLine[]>([]);
  const [showRuntimes, setShowRuntimes] = useState(false);
  const [project, setProject] = useState<ProjectMeta | null>(null);
  const [chat, setChat] = useState<ChatMsg[]>([]);
  const [tokens, setTokens] = useState<ProjectTokens>({ in: 0, out: 0 });
  const [live, setLive] = useState<{ id: string; agent: string; content: string } | null>(null);
  const [liveThinking, setLiveThinking] = useState<{ id: string; agent: string; content: string } | null>(null);
  const [status, setStatus] = useState<string>("idle");
  const [detail, setDetail] = useState<string>("");
  const [diffContext, setDiffContext] = useState<BranchDiffContext | null>(null);
  const [showLogboard, setShowLogboard] = useState(false);
  const [showMetrics, setShowMetrics] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [thinking, setThinking] = useState(false);

  const liveClient = useRef<LiveClient | null>(null);
  const chatEnd = useRef<HTMLDivElement | null>(null);

  // Auth check + projects
  useEffect(() => {
    authStatus(BASE)
      .then((st) => {
        setProtectedMode(st.login);
        setAuth(st.ok ? "ok" : "denied");
        return st.ok ? listProjects(BASE) : null;
      })
      .then((pr) => {
        if (!pr) return;
        setProjects(pr);
        const saved = localStorage.getItem(LAST_PROJECT_KEY);
        if (saved && pr.some((x) => x.project_name === saved)) {
          return open({ path_or_git: saved });
        }
      })
      .catch(fail);
  }, []);

  const fail = (e: unknown) => {
    if (e && typeof e === "object" && (e as { status?: number }).status === 401) {
      setAuth("denied");
      return;
    }
    setError(fmtErr(e));
  };

  // App logs
  const APP_LOG_MAX = 2000;
  const handleAppLog = useCallback((ev: { time?: string; source?: string; content?: string; line?: string }) => {
    const line = ev.line ?? ev.content ?? "";
    if (!line) return;
    setAppLogLines((prev) => {
      const next = [...prev, { time: ev.time ?? new Date().toISOString(), source: ev.source, line }];
      return next.length > APP_LOG_MAX ? next.slice(next.length - APP_LOG_MAX) : next;
    });
  }, []);

  // Project logs
  const LOG_MAX = 4000;
  const handleLog = useCallback((ev: LogMessage) => {
    setLogLines((prev) => {
      const cur = prev.get(ev.file);
      const kept = cur && cur.lines.length >= LOG_MAX ? cur.lines.slice(cur.lines.length - LOG_MAX + 1) : cur?.lines;
      const next: LogStream = {
        lines: [...(kept ?? []), ev.line],
        dropped: (cur?.dropped ?? 0) + (cur?.lines.length ?? 0) - (kept?.length ?? 0),
      };
      return new Map(prev).set(ev.file, next);
    });
  }, []);

  const open = async (spec: { path_or_git?: string }) => {
    setBusy(true);
    setError("");
    try {
      const p = await openProject(BASE, spec);
      setProject(p);
      setStatus(p.status ?? "idle");
      setDetail("");
      localStorage.setItem(LAST_PROJECT_KEY, p.project_name);
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
      const pr = await listProjects(BASE);
      setProjects(pr);
      const saved = localStorage.getItem(LAST_PROJECT_KEY);
      if (saved && pr.some((x) => x.project_name === saved)) {
        return open({ path_or_git: saved });
      }
    } catch (e) {
      fail(e);
    }
  };

  const onLogout = async () => {
    await logout(BASE);
    setAuth("denied");
  };

  // Connect live when project changes
  useEffect(() => {
    if (!project) return;
    liveClient.current?.close();
    setChat([]);
    setLive(null);
    setLiveThinking(null);
    setThinking(false);
    setTokens({ in: 0, out: 0 });
    setLogLines(new Map());
    (async () => {
      try {
        const [h, tk] = await Promise.all([
          chatHistory(BASE, project.project_name),
          projectTokens(BASE, project.project_name),
        ]);
        setChat(h);
        setTokens(tk);
      } catch (e) {
        fail(e);
      }
    })();

    const l = connectLive(project.project_name, BASE);
    liveClient.current = l;

    l.on("chat", (ev) => {
      try {
        const m = ev.payload as ChatMsg;
        if (m.role === "assistant" || m.role === "status") {
          setThinking(false);
          // Сохраняем рассуждение к сообщению ассистента при завершении стрима.
          setLiveThinking((prev) => {
            if (prev && m.role === "assistant" && (prev.content ?? "").trim()) {
              setChat((h) => [...h, { ...m, thinking: prev.content }]);
            }
            return null;
          });
          setLive((prev) => {
            if (prev && m.role === "assistant" && !(m.content ?? "").trim()) {
              const think = liveThinking?.content;
              setChat((h) => [...h, { id: prev.id, role: "assistant", content: prev.content, agent: prev.agent, time: new Date().toISOString(), thinking: think ?? undefined }]);
            }
            return null;
          });
        }
        setChat((prev) => {
          const filtered = m.role && (m.role === "assistant" || m.role === "status")
            ? prev.filter((x) => x.id !== m.id)
            : prev;
          return [...filtered, m];
        });
      } catch {}
    });
    l.on("chat_delta", (ev) => {
      try {
        const p = ev.payload as { content?: string; agent?: string; stream_id?: string };
        setThinking(false);
        setLive({ id: p.stream_id ?? "live", agent: p.agent ?? "assistant", content: p.content ?? "" });
      } catch {}
    });
    l.on("thinking_delta", (ev) => {
      try {
        const p = ev.payload as { thinking?: string; agent?: string; stream_id?: string };
        setThinking(false);
        setLiveThinking({ id: p.stream_id ?? "live-thinking", agent: p.agent ?? "assistant", content: p.thinking ?? "" });
      } catch {}
    });
    l.on("status", (ev) => {
      try {
        const p = ev.payload as { status?: string; detail?: string };
        setStatus(p.status ?? "running");
        setDetail(p.detail ?? "");
      } catch {}
    });
    l.on("tokens", (ev) => {
      try {
        setTokens(ev.payload as ProjectTokens);
      } catch {}
    });
    l.on("log", (ev) => {
      try {
        handleLog(ev.payload as LogMessage);
      } catch {}
    });
    l.on("applog", (ev) => {
      try {
        handleAppLog(ev.payload as { time?: string; source?: string; content?: string });
      } catch {}
    });
    l.on("chat_clear", () => {
      setChat([]);
      setLive(null);
      setLiveThinking(null);
      setThinking(false);
    });

    return () => l.close();
  }, [project?.project_name]);

  const onSend = async (text: string) => {
    if (!project) return;
    setThinking(true);
    try {
      await postChat(BASE, project.project_name, text);
    } catch (e) {
      setThinking(false);
      fail(e);
    }
  };

  const onStop = async () => {
    if (!project) return;
    setThinking(false);
    setLive(null);
    setLiveThinking(null);
    try {
      await chatStop(BASE, project.project_name);
    } catch (e) {
      fail(e);
    }
  };

  const onAnalyzeLogs = async (args: { file?: string; level?: string; query?: string }) => {
    if (!project) return;
    setThinking(true);
    try {
      await analyzeProjectLogs(BASE, project.project_name, args);
    } catch (e) {
      setThinking(false);
      fail(e);
    }
  };

  const onAskAnswer = useCallback(
    async (askID: string, body: AskAnswerBody): Promise<AskAnswerResult> => {
      if (!project) throw new Error("проект не выбран");
      return answerAsk(BASE, project.project_name, askID, body);
    },
    [project],
  );

  const onClearChat = async () => {
    if (!project) return;
    if (!window.confirm("Очистить диалог и начать с чистого листа?")) return;
    setChat([]);
    setLive(null);
    setLiveThinking(null);
    setThinking(false);
    try {
      await clearChat(BASE, project.project_name);
    } catch (e) {
      fail(e);
    }
  };

  const [indexing, setIndexing] = useState(false);
  const indexingRef = useRef(false);
  const onIndex = async () => {
    if (!project || indexingRef.current) return;
    indexingRef.current = true;
    setIndexing(true);
    try {
      await indexProject(BASE, project.project_name);
    } catch (e) {
      fail(e);
    } finally {
      indexingRef.current = false;
      setIndexing(false);
    }
  };

  if (auth === "checking") {
    return <div className="app"><p className="hint">Подключаюсь…</p></div>;
  }

  if (auth === "denied") {
    return <Login base={BASE} onLoggedIn={() => void onLoggedIn()} />;
  }

  return (
    <div className="app">
      <header className="top">
        <WorkspacePicker projects={projects} current={project} onOpen={open} busy={busy} />
        <div className="head-actions">
          {project && <TokensCounter in={tokens.in} out={tokens.out} tps={tokens.tps} />}
          {project && (
            <button
              className="btn"
              onClick={() => void onIndex()}
              disabled={indexing}
              title="Построить RAG-индекс"
            >
              {indexing ? "Индексирую…" : "Индекс RAG"}
            </button>
          )}
          <ModelSelector />
          {protectedMode && (
            <button className="btn" onClick={() => void onLogout()}>
              Выход
            </button>
          )}
        </div>
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

      {project ? (
        <main className="chat-only">
          <Chatboard
            chat={chat}
            live={live}
            liveThinking={liveThinking}
            onSend={onSend}
            onStop={onStop}
            endRef={chatEnd}
            thinking={thinking}
            onAskAnswer={onAskAnswer}
            onClearChat={onClearChat}
          />
        </main>
      ) : (
        <div className="empty">
          <p>Откройте или создайте проект (путь к папке или git-URL).</p>
        </div>
      )}

      {project && !showLogboard && !showMetrics && !showRuntimes && (
        <div className="fabs">
          <button className="fab" onClick={() => setShowLogboard(true)} title="Логи проекта">
            Логи
          </button>
          <button className="fab" onClick={() => setShowRuntimes(true)} title="Логи приложения">
            Рантайм
          </button>
          <button className="fab" onClick={() => setShowMetrics(true)} title="Метрики">
            Метрики
          </button>
        </div>
      )}

      {project && showRuntimes && (
        <div className="runtimes-drawer open">
          <Runtimes project={project.project_name} appLogLines={appLogLines} />
          <button className="btn close" onClick={() => setShowRuntimes(false)}>×</button>
        </div>
      )}

      {project && showMetrics && (
        <div className="metrics-drawer open">
          <Metricsboard project={project.project_name} />
          <button className="btn close" onClick={() => setShowMetrics(false)}>×</button>
        </div>
      )}

      {project && (
        <div className={"diff-drawer" + (diffContext || showLogboard ? " open" : "")}>
          {diffContext && (
            <Diffboard
              project={project.project_name}
              context={diffContext}
              onClose={() => setDiffContext(null)}
            />
          )}
          <Logboard
            project={project.project_name}
            showLogboard={showLogboard}
            logLines={logLines}
            onAnalyze={(args) => void onAnalyzeLogs(args)}
            toggleLogboard={() => setShowLogboard(false)}
          />
        </div>
      )}
    </div>
  );
}

export function fmtErr(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (err && typeof err === "object" && "message" in err) return String((err as { message: unknown }).message);
  if (typeof err === "string") return err;
  return "Произошла непредвиденная ошибка";
}
