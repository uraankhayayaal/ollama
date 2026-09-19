// Оболочка: селектор воркспейса, HITL-затворы, чат+доска 30/70. React-канон
// зеркалит server/session.go + runevents + chat/store.go (см. web/src/types.ts).
// Ф-3: аутентификация (AI_WEB_PASSWORD) — экран входа, защита 401-ответами.

import { useCallback, useEffect, useRef, useState } from "react";
import { authStatus, boardOf, chatHistory, continueProject, deleteEpic, gateDecide, listProjects, logout, openProject, postChat, projectTokens, sessionStop, updateTask } from "./Api";
import { connectLive, type LiveClient } from "./live";
import type { BoardView, ChatMsg, EpicRow, TaskRow, ProjectMeta, LogMessage, ProjectTokens } from "@/Types";
import { Dashboard } from "./Components/Dashboard";
import { Chatboard } from "./Components/Chatboard";
import { RunButton } from "./Components/RunButton";
import { TokensCounter } from "./Components/TokensCounter";
import { Diffboard } from "./Components/Diffboard";
import { Logboard } from "./Components/Logboard";
import { Login } from "./Components/Login";
import { WorkspacePicker } from "./Components/WorkspacePicker";
import { Badge } from "./Components/Badge";
import { GateBanner } from "./Components/GateBanner";
import { GateEvent } from "./Types";

const BASE = ""; // dev: Vite-прокси /api→backend; прод: embed same-origin.

type AuthPhase = "checking" | "ok" | "denied";

export function App() {
  const [auth, setAuth] = useState<AuthPhase>("checking");
  const [protectedMode, setProtectedMode] = useState(false);
  const [projects, setProjects] = useState<ProjectMeta[]>([]);
  // Потоковые строки логов: «имя файла → актуальный список строк».
  // Обновляется событиями WS type="log"; Logboard объединяет с HTTP-данными.
  const [logLines, setLogLines] = useState<Map<string, string[]>>(new Map());
  const [project, setProject] = useState<ProjectMeta | null>(null);
  const [board, setBoard] = useState<BoardView | null>(null);
  const [chat, setChat] = useState<ChatMsg[]>([]);
  // Счётчик токенов проекта (вход/выход): инициализируется REST-запросом при
  // открытии проекта, далее обновляется событиями WS type=tokens в реальном
  // времени (каждый раунд модели прибавляет порцию).
  const [tokens, setTokens] = useState<ProjectTokens>({ in: 0, out: 0 });
  // live — «плавающее» потоковое сообщение модели (стриминг, Ф-3): заполняется
  // событиями chat_delta и схлопывается в историю при финальном chat.
  const [live, setLive] = useState<{ id: string; agent: string; content: string } | null>(null);
  const [gate, setGate] = useState<GateEvent | null>(null);
  const [status, setStatus] = useState<string>("idle");
  const [detail, setDetail] = useState<string>("");
  // Чат и доска видны всегда (чат 30% / доска 70%); чат можно свернуть
  // в тонкую вертикальную полоску слева.
  const [chatCollapsed, setChatCollapsed] = useState(false);
  // Diffboard/Logboard по умолчанию скрыты; открываются плавающими кнопками
  // справа внизу (взаимоисключающе).
  const [showDiffboard, setShowDiffboard] = useState(false);
  const [showLogboard, setShowLogboard] = useState(false);
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

  // Обработчик событий «log» — каждая новая строкаappend к списку файла.
  const handleLog = useCallback((ev: LogMessage) => {
    setLogLines((prev) => {
      const entry = prev.get(ev.file) || [];
      return new Map(prev).set(ev.file, [...entry, ev.line]);
    });
  }, []);

  const open = async (spec: { path_or_git?: string }) => {
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
    // Счётчик токенов принадлежит прошлому проекту — обнуляем, иначе
    // счётчик подмешает чужие значения до прихода свежего REST-ответа.
    setTokens({ in: 0, out: 0 });
    // Накопитель строк логов принадлежит прошлому проекту — обнуляем, иначе
    // Logboard подмешает чужие строки и заблокирует стрим нового проекта.
    setLogLines(new Map());
    (async () => {
      try {
        const [b, h, tk] = await Promise.all([
          boardOf(BASE, project.project_name),
          chatHistory(BASE, project.project_name),
          projectTokens(BASE, project.project_name),
        ]);
        setBoard(b);
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
        // Затвор мог прийти раньше снимка доски — фоново перечитаем её,
        // чтобы GateBanner показал полные карточки утверждаемых эпиков/задач.
        boardOf(BASE, project.project_name)
          .then(setBoard)
          .catch(() => {});
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

    return () => l.close();
  }, [project?.project_name]);

  const onSend = async (text: string) => {
    if (!project) {
      return;
    }
    // Пользовательское сообщение не добавляем локально: сервер сам публикует
    // его в шину (type=chat, role=user) ещё до запуска оркестрации, и оно
    // прилетает через WS. Локальная вставка дублировала бы сообщение (дважды).
    try {
      await postChat(BASE, project.project_name, text);
    } catch (e) {
      fail(e);
    }
  };

  // «Продолжить»: запускает/возобновляет Kanban-оркестрацию на текущей доске
  // (кнопка ⏵). В чат ничего не отправляется и не дублируется: раннер работает
  // над эпиками и задачами доски своим циклом. Чат остаётся независимым —
  // вопросы и новые задачи для планировщика можно писать параллельно.
  // Возможно, когда на доске есть работа (исходная задача проекта meta.task
  // или хоть один эпик), а оркестрация не идёт (stopped/error/done/idle).
  const [continuing, setContinuing] = useState(false);
  // Синхронный флаг: иначе два быстрых клика до ре-рендера прошли бы оба
  // (canContinue читается из замыкания) и запустили бы раннер дважды.
  const continuingRef = useRef(false);
  const canContinue =
    !!project &&
    !!board &&
    (!!board.meta?.task || board.epics.length > 0) &&
    status !== "running" &&
    status !== "waiting" &&
    !continuing;

  const onContinue = async () => {
    if (continuingRef.current || !canContinue || !project) {
      return;
    }
    continuingRef.current = true;
    setContinuing(true);
    try {
      await continueProject(BASE, project.project_name);
    } catch (e) {
      fail(e);
    } finally {
      continuingRef.current = false;
      setContinuing(false);
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

  const onEpicDelete = async (epic: EpicRow) => {
    if (!project) {
      return;
    }
    if (!window.confirm(`Удалить эпик «${epic.title}» вместе с его задачами?`)) {
      return;
    }
    try {
      await deleteEpic(BASE, project.project_name, epic.task_id);
      // Локально убираем эпик и его задачи; WS-событие board подтвердит сверкой.
      setBoard((prev) =>
        prev
          ? {
              ...prev,
              epics: prev.epics.filter((e) => e.task_id !== epic.task_id),
              tasks: prev.tasks.filter((t) => t.epic_id !== epic.task_id),
            }
          : prev,
      );
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
        <div className="head-actions">
          {project && <TokensCounter in={tokens.in} out={tokens.out} />}
          <RunButton
            status={status}
            canContinue={canContinue}
            busy={continuing}
            onRun={onContinue}
            onStop={onStop}
          />
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

      {gate && <GateBanner gate={gate} board={board} onDecide={onGate} />}

      {project ? (
        <main className="panes">
          {!chatCollapsed && (
            <section className="chat-pane">
              <Chatboard
                chat={chat}
                live={live}
                onSend={onSend}
                endRef={chatEnd}
                collapsed={false}
                onToggleCollapse={() => setChatCollapsed(true)}
              />
            </section>
          )}
          {chatCollapsed && (
            <section className="chat-strip">
              <Chatboard
                chat={chat}
                live={live}
                onSend={onSend}
                endRef={chatEnd}
                collapsed
                onToggleCollapse={() => setChatCollapsed(false)}
              />
            </section>
          )}
          <section className="dash-pane">
            <Dashboard board={board} onTaskUpdate={onTaskUpdate} onEpicDelete={onEpicDelete} />
          </section>
        </main>
      ) : (
        <div className="empty">
          <p>Откройте или создайте проект (путь к папке или git-URL).</p>
        </div>
      )}

      {project && !showDiffboard && !showLogboard && (
        <div className="fabs">
          <button className="fab" onClick={() => setShowDiffboard(true)} title="Показать дифф проекта">
            Дифф
          </button>
          <button className="fab" onClick={() => setShowLogboard(true)} title="Показать логи проекта">
            Логи
          </button>
        </div>
      )}

      {project && (
        <div className={"diff-drawer" + (showDiffboard || showLogboard ? " open" : "")}>
          <Diffboard
            project={project.project_name}
            kind={project.kind}
            showDiffboard={showDiffboard}
            toggleDiffboard={() => {
              setShowDiffboard(false);
            }}
          />
          <Logboard
            project={project.project_name}
            showLogboard={showLogboard}
            logLines={logLines}
            toggleLogboard={() => {
              setShowLogboard(false);
            }}
          />
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