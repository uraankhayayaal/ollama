// Оболочка: селектор воркспейса, HITL-затворы, чат+доска 30/70. React-канон
// зеркалит server/session.go + runevents + chat/store.go (см. web/src/types.ts).
// Ф-3: аутентификация (AI_WEB_PASSWORD) — экран входа, защита 401-ответами.

import { useCallback, useEffect, useRef, useState } from "react";
import { authStatus, answerAsk, boardOf, chatHistory, clearChat, continueProject, createEpicBranch, createEpicMR, createTaskBranch, createTaskMR, deleteEpic, gateDecide, indexProject, listProjects, logout, openProject, postChat, projectTokens, releaseEpic, sessionStop, setEpicStatus, updateTask } from "./Api";
import { connectLive, type LiveClient } from "./live";
import type { AskAnswerBody, AskAnswerResult, BoardView, BranchDiffContext, ChatMsg, EpicRow, TaskRow, ProjectMeta, LogMessage, ProjectTokens, Status } from "@/Types";
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

// Последний выбранный проект: при рефреше страницы доска не «теряется» —
// приложение само переоткрывает сохранённый проект.
const LAST_PROJECT_KEY = "ollama.last-project";

// Раскладка панелей: пропорция чат/доска (доля ширины у чата) и схлопнутая
// панель переживают рефреш страницы (как LAST_PROJECT_KEY).
const PANE_RATIO_KEY = "ollama.pane-ratio";
const PANE_COLLAPSED_KEY = "ollama.pane-collapsed";
const CHAT_DEFAULT_RATIO = 0.3;

// Минимальные рабочие ширины панелей (px): ниже них ширма не останавливается.
const CHAT_MIN_PX = 240;
const DASH_MIN_PX = 280;
// Пороги «умного» схлопывания: панель сужена настолько, что ресайз превращается
// в скрытие в статусную полоску.
const CHAT_COLLAPSE_PX = 190;
const DASH_COLLAPSE_PX = 220;
// Скорость «броска» ширмы (px/мс): резкий рывок к краю схлопывает панель
// даже чуть раньше жёсткого порога.
const FLING_PX_MS = 0.5;

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
  // Счётчик токенов проекта (вход/выход + скорость генерации): инициализируется
  // REST-запросом при открытии проекта, далее обновляется событиями WS
  // type=tokens в реальном времени (каждый раунд модели прибавляет порцию;
  // tps — последняя реальная скорость из usage провайдера).
  const [tokens, setTokens] = useState<ProjectTokens>({ in: 0, out: 0 });
  // live — «плавающее» потоковое сообщение модели (стриминг, Ф-3): заполняется
  // событиями chat_delta и схлопывается в историю при финальном chat.
  const [live, setLive] = useState<{ id: string; agent: string; content: string } | null>(null);
  const [gate, setGate] = useState<GateEvent | null>(null);
  const [status, setStatus] = useState<string>("idle");
  const [detail, setDetail] = useState<string>("");
  // Раскладка панелей: обе панели можно ресайзить ширмой (dash-pane занимает
  // остаток); схлопнутость взаимоисключающая — одна полоска слева (чат) или
  // справа (доска), вторая панель при этом занимает всю ширину.
  const [collapsedSide, setCollapsedSide] = useState<"" | "chat" | "dash">(() => {
    const saved = localStorage.getItem(PANE_COLLAPSED_KEY);
    return saved === "chat" || saved === "dash" ? saved : "";
  });
  // Пропорция чата (доля ширины экрана, 0..1): живёт в localStorage, меняется
  // перетаскиванием ширмы и восстанавливается при развороте колонки.
  const [chatRatio, setChatRatio] = useState<number>(() => {
    const saved = localStorage.getItem(PANE_RATIO_KEY);
    const v = saved ? parseFloat(saved) : CHAT_DEFAULT_RATIO;
    return Number.isFinite(v) && v > 0 && v < 1 ? v : CHAT_DEFAULT_RATIO;
  });
  // Контекстный Diffboard открывается из модалки конкретной ветки;
  // Logboard остаётся доступен отдельной плавающей кнопкой.
  const [diffContext, setDiffContext] = useState<BranchDiffContext | null>(null);
  const [showLogboard, setShowLogboard] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // thinking — пользователь отправил сообщение, ассистент ещё не начал печатать:
  // показываем анимацию «модель думает», пока не придёт первый chat_delta
  // (живой пузырь) или финальный chat (ответ/статус-ошибка).
  const [thinking, setThinking] = useState(false);

  const liveClient = useRef<LiveClient | null>(null);
  const chatEnd = useRef<HTMLDivElement | null>(null);

  // --- разделительная ширма между чатом и доской ---
  // Перетаскивание меняет пропорцию в реальном времени, мутируя ширину chat-pane
  // напрямую по DOM (без ререндера истории чата на каждый pixel-move); на
  // отпускании «умное» схлопывание: бросок к краю или слишком узкая панель
  // прячет её в статусную полоску.
  const panesRef = useRef<HTMLElement | null>(null);
  const chatPaneRef = useRef<HTMLElement | null>(null);
  const [dragging, setDragging] = useState(false);
  const drag = useRef<{ lastX: number; lastT: number; vel: number; ratio: number } | null>(null);

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
        if (!pr) return;
        setProjects(pr);
        // Рефреш страницы: автооткрываем последний выбранный проект, если он
        // ещё зарегистрирован на сервере. open() сам восстановит статус и
        // подключит live-поток.
        const saved = localStorage.getItem(LAST_PROJECT_KEY);
        if (saved && pr.some((x) => x.project_name === saved)) {
          return open({ path_or_git: saved });
        }
        return undefined;
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
      // Инициализируем статус из ответа сервера, а не из локального "idle":
      // иначе у проекта с уже идущей оркестрацией кнопка показывает
      // «Продолжить», пока не придёт первое WS-событие статуса.
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
      // После входа также восстанавливаем последний открытый проект.
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

  useEffect(() => {
    if (!project) return;
    liveClient.current?.close();
    setGate(null);
    setChat([]);
    setLive(null);
    setThinking(false);
    setBoard(null);
    setDiffContext(null);
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
          setThinking(false);
        }
        // Статус-строка (например, ошибка ассистента) тоже снимает «думаю».
        if (m.role === "status") {
          setThinking(false);
        }
        setChat((prev) => [...prev, m]);
      } catch {}
    });
    l.on("chat_delta", (ev) => {
      try {
        const p = ev.payload as { content?: string; agent?: string; stream_id?: string };
        // Модель начала печатать — живой пузырь заменяет анимацию «думаю».
        setThinking(false);
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
    // «Кофе-брейк»: диалог стёрт на сервере (у другого клиента или в этой
    // вкладке) — чистим локальный массив и снимаем «модель думает». Следом
    // прилетит системная пометка role=chat/system из свежего стрима.
    l.on("chat_clear", () => {
      setChat([]);
      setLive(null);
      setThinking(false);
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
    setThinking(true);
    try {
      await postChat(BASE, project.project_name, text);
    } catch (e) {
      setThinking(false);
      fail(e);
    }
  };

  // Ответ на структурированный вопрос ассистента (AskUser): POST
  // /api/projects/{id}/ask/{askID}/answer. Передаётся в Chatboard → AskCard.
  const onAskAnswer = useCallback(
    async (askID: string, body: AskAnswerBody): Promise<AskAnswerResult> => {
      if (!project) {
        throw new Error("проект не выбран");
      }
      return answerAsk(BASE, project.project_name, askID, body);
    },
    [project],
  );

  // «Кофе-брейк»: очищает диалог и на сервере (стирается стрим — модель
  // «забывает» разговор и общается с чистого листа), и в локальном состоянии.
  // Сервер сам шлёт chat_clear в шину, но здесь очищаем и свою вкладку, чтобы
  // не ждать круга по WS.
  const onClearChat = async () => {
    if (!project) {
      return;
    }
    if (!window.confirm("Кофе-брейк: очистить диалог и начать общение с чистого листа?")) {
      return;
    }
    setChat([]);
    setLive(null);
    setThinking(false);
    try {
      await clearChat(BASE, project.project_name);
    } catch (e) {
      fail(e);
    }
  };

  // «Продолжить»: запускает/возобновляет Kanban-оркестрацию на текущей доске
  // (кнопка ⏵). В чат ничего не отправляется и не дублируется: раннер работает
  // в board-only режиме — берёт в работу эпики и задачи, которые уже есть на
  // доске, новые не создаёт. Если брать нечего, оркестрация уходит в режим
  // ожидания (standby) и ждёт появления работы — поэтому пустая доска кнопку
  // не блокирует.
  // Возможно, когда оркестрация не идёт (stopped/error/done/idle).
  const [continuing, setContinuing] = useState(false);
  // Синхронный флаг: иначе два быстрых клика до ре-рендера прошли бы оба
  // (canContinue читается из замыкания) и запустили бы раннер дважды.
  const continuingRef = useRef(false);
  const canContinue =
    !!project &&
    !!board &&
    status !== "running" &&
    status !== "waiting" &&
    status !== "standby" &&
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

  // «Индекс RAG»: фоновая индексация векторной памяти проекта (Ф-5). Вызов не
  // блокирует UI; повторный запуск при идущей индексации отклоняется сервером.
  const [indexing, setIndexing] = useState(false);
  const indexingRef = useRef(false);
  const onIndex = async () => {
    if (!project || indexingRef.current) {
      return;
    }
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

  const onEpicRelease = async (epic: EpicRow) => {
    if (!project) {
      return;
    }
    await releaseEpic(BASE, project.project_name, epic.task_id);
  };

  // Перевод эпика в новый статус (кнопки «Пауза»/«Продолжить»/«Отменить»,
  // Ф-6): эпики изолированы в своих git-ветках, поэтому пауза/отмена НЕ
  // откатывают код — ветка остаётся, работа просто приостанавливается.
  // Сервер каскадом переводит и задачи эпика (пауза/отмена/возобновление) —
  // зеркалим каскад локально, WS-событие board сверит окончательно.
  const applyEpicStatus = async (epic: EpicRow, status: Status) => {
    if (!project) {
      return;
    }
    const next = await setEpicStatus(BASE, project.project_name, epic.task_id, status);
    setBoard((prev) => {
      if (!prev) {
        return prev;
      }
      let tasks = prev.tasks;
      if (status === "paused" || status === "cancelled") {
        // Каскад сервера: не взятые в работу задачи уходят вместе с эпиком;
        // для паузы запоминаем исходный статус (возобновление вернёт туда же).
        tasks = tasks.map((t) =>
          t.epic_id === next.task_id &&
          (t.status === "new" || t.status === "analysis" || t.status === "ready")
            ? {
                ...t,
                status,
                resume_status: status === "paused" ? t.status : undefined,
              }
            : t,
        );
      } else if (status === "ready") {
        tasks = tasks.map((t) =>
          t.epic_id === next.task_id && t.status === "paused"
            ? { ...t, status: t.resume_status ?? "ready", resume_status: undefined }
            : t,
        );
      }
      return {
        ...prev,
        epics: prev.epics.map((e) => (e.task_id === next.task_id ? next : e)),
        tasks,
      };
    });
  };

  const onEpicPause = async (epic: EpicRow) => {
    try {
      await applyEpicStatus(epic, "paused");
    } catch (e) {
      fail(e);
    }
  };

  const onEpicResume = async (epic: EpicRow) => {
    try {
      await applyEpicStatus(epic, "ready");
    } catch (e) {
      fail(e);
    }
  };

  const onEpicCancel = async (epic: EpicRow) => {
    if (!window.confirm(
      `Отменить эпик «${epic.title}»? Код в его ветке (${epic.git_branch || "ai/epic/" + epic.task_id}) останется — отката не будет.`,
    )) {
      return;
    }
    try {
      await applyEpicStatus(epic, "cancelled");
    } catch (e) {
      fail(e);
    }
  };

  const onEpicMR = async (epic: EpicRow) => {
    if (!project) {
      return;
    }
    await createEpicMR(BASE, project.project_name, epic.task_id);
  };

  const onTaskMR = async (task: TaskRow) => {
    if (!project) {
      return;
    }
    await createTaskMR(BASE, project.project_name, task.task_id);
  };

  const onEpicBranch = async (epic: EpicRow) => {
    if (!project) {
      return;
    }
    await createEpicBranch(BASE, project.project_name, epic.task_id);
  };

  const onTaskBranch = async (task: TaskRow) => {
    if (!project) {
      return;
    }
    await createTaskBranch(BASE, project.project_name, task.task_id);
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

  // Кламп пропорции чата в рабочий диапазон [CHAT_MIN_PX, ширина − DASH_MIN_PX].
  const clampChatRatio = (ratio: number, width: number) => {
    let min = CHAT_MIN_PX / width;
    let max = 1 - DASH_MIN_PX / width;
    if (max < min) max = min;
    return Math.min(Math.max(ratio, min), max);
  };

  const commitRatio = (ratio: number, width: number) => {
    const r = clampChatRatio(ratio, width);
    setChatRatio(r);
    localStorage.setItem(PANE_RATIO_KEY, String(r));
    localStorage.setItem(PANE_COLLAPSED_KEY, "");
  };

  const onSplitStart = (e: React.PointerEvent<HTMLDivElement>) => {
    const panes = panesRef.current;
    if (!panes) {
      return;
    }
    e.preventDefault();
    const rect = panes.getBoundingClientRect();
    drag.current = {
      lastX: e.clientX,
      lastT: performance.now(),
      vel: 0,
      ratio: (e.clientX - rect.left) / rect.width,
    };
    e.currentTarget.setPointerCapture(e.pointerId);
    setDragging(true);
  };

  const onSplitMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    const panes = panesRef.current;
    if (!d || !panes) {
      return;
    }
    const rect = panes.getBoundingClientRect();
    const now = performance.now();
    const dt = now - d.lastT;
    if (dt > 0) {
      const v = (e.clientX - d.lastX) / dt;
      d.vel = d.vel * 0.6 + v * 0.4;
    }
    d.lastX = e.clientX;
    d.lastT = now;
    const ratio = Math.min(0.98, Math.max(0.02, (e.clientX - rect.left) / rect.width));
    d.ratio = ratio;
    if (chatPaneRef.current) {
      chatPaneRef.current.style.width = `${ratio * 100}%`;
    }
  };

  // Отпускание ширмы: «умное» схлопывание. Резкий бросок к краю или слишком
  // узкая панель прячет её в полоску (взаимоисключающе — вторая разворачивается
  // автоматически); иначе фиксируем пропорцию в localStorage.
  const onSplitEnd = () => {
    const d = drag.current;
    const panes = panesRef.current;
    drag.current = null;
    setDragging(false);
    if (!d || !panes) {
      return;
    }
    const w = panes.getBoundingClientRect().width;
    const chatPx = d.ratio * w;
    const dashPx = w - chatPx;
    const strong = Math.abs(d.vel) > FLING_PX_MS;
    const collapseChat =
      chatPx <= CHAT_COLLAPSE_PX ||
      (strong && d.vel < 0 && chatPx <= CHAT_MIN_PX + 60);
    const collapseDash =
      dashPx <= DASH_COLLAPSE_PX ||
      (strong && d.vel > 0 && dashPx <= DASH_MIN_PX + 60);
    if (collapseChat) {
      setCollapsedSide("chat");
      localStorage.setItem(PANE_COLLAPSED_KEY, "chat");
    } else if (collapseDash) {
      setCollapsedSide("dash");
      localStorage.setItem(PANE_COLLAPSED_KEY, "dash");
    } else {
      commitRatio(d.ratio, w);
    }
  };

  // Отмена (напр. уход с жеста): фиксируем пропорцию без схлопывания.
  const onSplitCancel = () => {
    const d = drag.current;
    const panes = panesRef.current;
    drag.current = null;
    setDragging(false);
    if (!d || !panes) {
      return;
    }
    commitRatio(d.ratio, panes.getBoundingClientRect().width);
  };

  // Схлопывание/разворот статусной колонки (кнопка в полоске или в шапке чата).
  const toggleCollapse = (side: "chat" | "dash") => {
    const next = collapsedSide === side ? "" : side;
    setCollapsedSide(next);
    // При содержимом раскрытии пропорция уже в диапазоне; кламп подстрахует
    // от узких экранов при отдаче в localStorage.
    localStorage.setItem(PANE_COLLAPSED_KEY, next);
    if (next === "" && panesRef.current) {
      commitRatio(chatRatio, panesRef.current.getBoundingClientRect().width);
    }
  };

  // Ширина чата для рендера: хранимая пропорция, клампится в рабочий диапазон
  // текущей ширины панелей (защита от «вечно узкого» чата/доски после рефреша
  // или ресайза окна).
  const panesWidth = panesRef.current?.getBoundingClientRect().width ?? window.innerWidth;
  const chatRenderPct = clampChatRatio(chatRatio, panesWidth) * 100;

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
          {project && <TokensCounter in={tokens.in} out={tokens.out} tps={tokens.tps} />}
          {project && (
            <button
              className="btn"
              onClick={() => void onIndex()}
              disabled={indexing}
              title="Построить RAG-индекс проекта в фоне (не блокирует оркестрацию)"
            >
              {indexing ? "Индексирую…" : "Индекс RAG"}
            </button>
          )}
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
        <main className={"panes" + (dragging ? " dragging" : "")} ref={panesRef}>
          {collapsedSide === "chat" ? (
            <section className="chat-strip">
              <Chatboard
                chat={chat}
                live={live}
                onSend={onSend}
                endRef={chatEnd}
                thinking={thinking}
                collapsed
                onAskAnswer={onAskAnswer}
                onClearChat={onClearChat}
                onToggleCollapse={() => toggleCollapse("chat")}
              />
            </section>
          ) : (
            <section
              className="chat-pane"
              ref={chatPaneRef}
              style={collapsedSide === "dash" ? { flex: "1 1 auto" } : { width: `${chatRenderPct}%` }}
            >
              <Chatboard
                chat={chat}
                live={live}
                onSend={onSend}
                endRef={chatEnd}
                thinking={thinking}
                onAskAnswer={onAskAnswer}
                onClearChat={onClearChat}
                onToggleCollapse={() => toggleCollapse("chat")}
              />
            </section>
          )}
          {collapsedSide === "" && (
            <div
              className={"splitter" + (dragging ? " active" : "")}
              onPointerDown={onSplitStart}
              onPointerMove={onSplitMove}
              onPointerUp={onSplitEnd}
              onPointerCancel={onSplitCancel}
              title="Изменить ширину панелей"
            />
          )}
          {collapsedSide === "dash" ? (
            <section className="dash-strip">
              <Dashboard
                board={board}
                onTaskUpdate={onTaskUpdate}
                onEpicDelete={onEpicDelete}
                onEpicRelease={onEpicRelease}
                onEpicPause={onEpicPause}
                onEpicResume={onEpicResume}
                onEpicCancel={onEpicCancel}
                onEpicBranch={onEpicBranch}
                onTaskBranch={onTaskBranch}
                onEpicMR={onEpicMR}
                onTaskMR={onTaskMR}
                onShowDiff={setDiffContext}
                collapsed
                onToggleCollapse={() => toggleCollapse("dash")}
              />
            </section>
          ) : (
            <section className="dash-pane">
              <Dashboard
                board={board}
                onTaskUpdate={onTaskUpdate}
                onEpicDelete={onEpicDelete}
                onEpicRelease={onEpicRelease}
                onEpicPause={onEpicPause}
                onEpicResume={onEpicResume}
                onEpicCancel={onEpicCancel}
                onEpicBranch={onEpicBranch}
                onTaskBranch={onTaskBranch}
                onEpicMR={onEpicMR}
                onTaskMR={onTaskMR}
                onShowDiff={setDiffContext}
                collapsed={false}
                onToggleCollapse={() => toggleCollapse("dash")}
              />
            </section>
          )}
        </main>
      ) : (
        <div className="empty">
          <p>Откройте или создайте проект (путь к папке или git-URL).</p>
        </div>
      )}

      {project && !showLogboard && (
        <div className="fabs">
          <button className="fab" onClick={() => setShowLogboard(true)} title="Показать логи проекта">
            Логи
          </button>
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
