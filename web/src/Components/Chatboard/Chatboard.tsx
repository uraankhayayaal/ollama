// Chatboard — панель чата с агентом: история + поле ввода + автокролл.
// REST: postChat / chatHistory; live: "chat" (см. types.ts ChatMsg).
// Канон-архитектура — как Dashboard (Components/Chatboard/*).
//
// Верхний action-bar: кнопка «Экспорт» выгружает чат в сокращённый
// минимально-токенный формат (chatForAI) для передачи ИИ-модели на разбор.
//
// Сворачивание сообщений (foldable): tool-результаты и длинные технические
// тексты по умолчанию свёрнуты в компактную «шапку» (кто/инструмент/статус);
// клик разворачивает содержимое. Пустые сообщения показываются компактным
// плейсхолдером, чтобы не терять хронологию диалога.

import { useEffect, useState } from "react";
import type { AskAnswerBody, AskAnswerResult, ChatMsg } from "@/Types";
// import { APIError } from "@/Api";
import { downloadText, safeName, stampedName } from "@/download";
import { AskCard } from "./AskCard";
import "./styles.scss";

// Пороги «тяжёлого» сообщения: после них контент сворачивается по умолчанию.
const MAX_COLLAPSED_CHARS = 600;
const MAX_COLLAPSED_LINES = 10;
const PREVIEW_MAX = 220;

// Максимум символов одного сообщения в экспортном дайджесте: длинные тексты
// (результаты инструментов, большие ответы) обрезаются, чтобы упаковать
// разговор в минимальное число токенов для разбора сторонней ИИ-моделью.
const EXPORT_SNIPPET_MAX = 400;

export function Chatboard({
  chat,
  live,
  onSend,
  endRef,
  busy,
  collapsed = false,
  onToggleCollapse,
  thinking = false,
  onAskAnswer,
  onClearChat,
}: {
  chat: ChatMsg[];
  // live — «плавающее» потоковое сообщение модели (стриминг, Ф-3); рендерится
  // с маркером «…» до прихода финального протокола chat с ролью assistant.
  live: { id: string; agent: string; content: string } | null;
  onSend: (text: string) => void;
  endRef: React.RefObject<HTMLDivElement | null>;
  busy?: boolean;
  // Свёрнутый режим: тонкая вертикальная полоска слева с кнопкой разворота
  // и иконками статусов сообщений (доска занимает остальную ширину экрана).
  collapsed?: boolean;
  onToggleCollapse?: () => void;
  // thinking — сообщение отправлено, модель ещё не начала печатать: показываем
  // анимацию «думает» до прихода первого chat_delta, её сменяет живой пузырь.
  thinking?: boolean;
  // onAskAnswer — отправка ответа на структурированный вопрос ассистента (AskUser):
  // POST /api/projects/{id}/ask/{askID}/answer. Внедряется из App (там есть
  // имя проекта); карточка-вардин рендерится для сообщений role=ask.
  onAskAnswer?: (askID: string, body: AskAnswerBody) => Promise<AskAnswerResult>;
  // onClearChat — «кофе-брейк»: очистить диалог и на сервере (DELETE
  // /api/projects/{id}/chat), и в локальном состоянии, чтобы начать
  // общение с чистого листа. Внедряется из App (там есть имя проекта).
  onClearChat?: () => void;
}) {
  useEffect(() => {
    if (!collapsed) {
      endRef.current?.scrollIntoView({ behavior: "smooth" });
    }
  }, [chat, live, endRef, collapsed]);

  // Экспорт чата: компактный дайджест для передачи ИИ-модели на разбор
  // (роли короткими префиксами, без времени; tool-вызовы — имя+статус).
  const exportChat = () => {
    if (chat.length === 0) {
      return;
    }
    // Snippet — первые слова первого сообщения пользователя: различает
    // экспорты одного чата; timestamp в начале имени уже добавляет stampedName.
    const firstUser = chat.find((m) => {
      const role = (m.role ?? "").toLowerCase();
      return role === "user" && (m.content ?? "").trim().length > 0;
    });
    const raw = (firstUser?.content ?? "").trim().replace(/\s+/g, " ").slice(0, 20);
    const snippet = raw ? safeName(raw) : "";
    downloadText(stampedName("chat", snippet ? `${snippet}.txt` : ".txt"), chatForAI(chat));
  };

  if (collapsed) {
    const counts: Record<string, number> = {};
    for (const m of chat) {
      const role = (m.role ?? "agent").toLowerCase();
      counts[role] = (counts[role] ?? 0) + 1;
    }
    return (
      <div className="chatboard collapsed">
        <button className="expand" onClick={onToggleCollapse} title="Развернуть чат">
          »
        </button>
        <div className="cstrip">
          <div className="st user" title={`Сообщений от вас: ${counts.user ?? 0}`}>
            <IconUser />
            <b>{counts.user ?? 0}</b>
          </div>
          <div className="st agent" title={`Ответов модели: ${counts.agent ?? 0}`}>
            <IconBot />
            <b>{counts.agent ?? 0}</b>
          </div>
          <div className="st tool" title={`Вызовов инструментов: ${counts.tool ?? 0}`}>
            <IconTool />
            <b>{counts.tool ?? 0}</b>
          </div>
          {(thinking || live) && (
            <div
              className="st live"
              title={thinking && !live ? "Модель думает…" : "Модель печатает…"}
            >
              <IconLive />
            </div>
          )}
        </div>
      </div>
    );
  }

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const input = (e.target as HTMLFormElement).querySelector("input");
    const text = (input?.value ?? "").trim();
    if (!text) {
      return;
    }
    if (input) {
      input.value = "";
    }
    onSend(text);
  };

  return (
    <div className="chatboard">
      <div className="headbar">
        <span className="head-title">Чат</span>
        <div className="abar">
          {chat.length > 0 && (
            <button
              className="ab"
              onClick={exportChat}
              title="Экспорт чата в сокращённый формат для передачи ИИ-модели"
            >
              <IconDownload />
              Экспорт
            </button>
          )}
          {onClearChat && (
            <button
              className="ab"
              onClick={onClearChat}
              title="Кофе-брейк: очистить диалог и начать общение с чистого листа"
            >
              <IconCoffee />
              Кофе-брейк
            </button>
          )}
          {onToggleCollapse && (
            <button className="collapse-btn" onClick={onToggleCollapse} title="Свернуть чат влево">
              «
            </button>
          )}
        </div>
      </div>

      <ul className="history">
        {chat.filter((m) => {
          const role = (m.role ?? "").toLowerCase();
          if (role === "tool") return false;
          if (role === "assistant" && !(m.content ?? "").trim()) return false;
          return true;
        }).map((m) => (
          <Msg key={m.id} m={m} onAskAnswer={onAskAnswer} />
        ))}
        {live && (
          <li key={live.id} className="msg assistant streaming">
            <span className="who">{live.agent}</span>
            <p className="content">
              {live.content}
              <span className="cursor">…</span>
            </p>
          </li>
        )}
        {thinking && !live && (
          <li className="msg assistant thinking" aria-label="Модель думает">
            <p className="content typing">
              <span className="activity-mark" aria-hidden="true"><i /><i /><i /></span>
              <span className="hint">Думаю</span>
            </p>
          </li>
        )}
        <div ref={endRef} />
      </ul>

      <form className="send" onSubmit={submit}>
        <input
          type="text"
          placeholder="Что сделать дальше?"
          disabled={busy}
          autoComplete="off"
        />
        <button className="btn primary" disabled={busy}>
          Отправить
        </button>
      </form>
    </div>
  );
}

// defaultAnswerAsk — заглушка на случай, когда карточка вопроса рендерится без
// live-колбэка (история после перезагрузки, активная сессия уже закрыта):
// повторно ответить на закрытый вопрос нельзя.
function defaultAnswerAsk(): Promise<AskAnswerResult> {
  return Promise.reject(new Error("Вопрос уже закрыт — ответ изменить нельзя"));
}

// Одно сообщение истории. tool-результаты и объёмные/технические тексты
// сворачиваются по умолчанию в кликабельную шапку; разворачиваются по клику.
// role=ask рендерит карточку-вардин структурированного вопроса (AskCard).
function Msg({
  m,
  onAskAnswer,
}: {
  m: ChatMsg;
  onAskAnswer?: (askID: string, body: AskAnswerBody) => Promise<AskAnswerResult>;
}) {
  const role = (m.role ?? "agent").toLowerCase();
  const text = m.content ?? "";
  const empty = text.trim().length === 0;
  const isTool = role === "tool";
  const isAsk = role === "ask";
  // «Тяжёлое» сообщение: слишком много символов или строк — скрываем по умолчанию.
  // Пустые сообщения не сворачиваются (разворачивать нечего).
  const long = text.length > MAX_COLLAPSED_CHARS || text.split("\n").length > MAX_COLLAPSED_LINES;
  const foldable = !empty && (isTool || long);

  const [open, setOpen] = useState(!foldable);

  const who = isTool ? m.agent || "tool" : m.agent || (role === "assistant" ? "assistant" : role);

  const cls = ["msg", role, foldable ? "foldable" : "", open ? "open" : "closed", empty ? "empty-msg" : ""]
    .filter(Boolean)
    .join(" ");

  // Сворачиваемое сообщение кликабельно целиком (шапка и выжимка), чтобы
  // наведение подсвечивало и клик раскрывал любой фрагмент, а не только шапку.
  const toggle = () => {
    if (foldable) {
      setOpen((v) => !v);
    }
  };
  const onKey = (e: React.KeyboardEvent) => {
    if (foldable && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      toggle();
    }
  };

  const liProps: React.HTMLAttributes<HTMLLIElement> = foldable
    ? {
        role: "button",
        tabIndex: 0,
        "aria-expanded": open,
        title: open ? "Свернуть" : "Развернуть",
        onClick: toggle,
        onKeyDown: onKey,
      }
    : {};

  return (
    <li className={cls} {...liProps}>
      <div className="head">
        <Chevron open={open} show={foldable} />
        <span className="who">{who}</span>
        {isTool && <ToolBadge name={m.tool} ok={m.ok} />}
        <time className="when">{fmtTime(m.time)}</time>
      </div>

      {isAsk && m.ask ? (
        <AskCard ask={m.ask} onAskAnswer={onAskAnswer ?? defaultAnswerAsk} />
      ) : empty ? (
        <p className="content nil" title={`Сообщение без текста (${who})`}>
          ∅
        </p>
      ) : !foldable || open ? (
        <p className={"content" + (isTool ? " mono" : "")}>{text}</p>
      ) : (
        <p className="preview" title="Развернуть">
          {previewOf(text)}
        </p>
      )}
    </li>
  );
}

// Бейдж инструмента с цветом статуса: успех — зелёный, ошибка — красный.
function ToolBadge({ name, ok }: { name?: string; ok?: boolean }) {
  return <span className={"tool" + (ok === false ? " err" : ok === true ? " ok" : "")}>{name ?? "tool"}</span>;
}

// Шеврон раскрытия (вращается при открытии); для несворачиваемых — пусто.
function Chevron({ open, show }: { open: boolean; show: boolean }) {
  if (!show) {
    return null;
  }
  return (
    <svg
      className={"chev" + (open ? " open" : "")}
      width="10"
      height="10"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
    >
      <path d="M6 4l4 4-4 4" />
    </svg>
  );
}

// Краткая выжимка свёрнутого текста: первая непустая строка, усечённая.
function previewOf(text: string): string {
  const line = text.split("\n").find((l) => l.trim().length > 0) ?? "";
  const t = line.trim();
  return t.length > PREVIEW_MAX ? t.slice(0, PREVIEW_MAX) + "…" : t;
}

// Компактное представление чата для передачи ИИ-модели на разбор. Формат
// минимизирует токены: короткие префиксы ролей, без времени и служебных полей,
// tool-вызовы — одной строкой «имя + статус», длинные сообщения обрезаются.
function chatForAI(msgs: ChatMsg[]): string {
  const out: string[] = ["== чат =="];
  for (const m of msgs) {
    const role = (m.role ?? "").toLowerCase();
    const text = (m.content ?? "").trim();
    if (role === "tool") {
      out.push(`T:${m.tool ?? "tool"}${m.ok === false ? " err" : m.ok === true ? " ok" : ""}`);
      continue;
    }
    if (!text) {
      continue;
    }
    const tag =
      role === "user"
        ? "U"
        : role === "assistant"
          ? "A"
          : role === "status"
            ? "S"
            : role === "system"
              ? "SY"
              : "M";
    const body = text.length > EXPORT_SNIPPET_MAX ? text.slice(0, EXPORT_SNIPPET_MAX) + "…" : text;
    out.push(`${tag}: ${body}`);
  }
  return out.join("\n");
}

// Иконки статусов для свёрнутой полоски (inline-SVG, без эмодзи).
function IconUser() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <circle cx="12" cy="8" r="4" />
      <path d="M4 20c.5-4 3.6-6 8-6s7.5 2 8 6" />
    </svg>
  );
}

function IconBot() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
      <rect x="4" y="8" width="16" height="11" rx="3" />
      <path d="M12 8V5" />
      <path d="M2 13v3M22 13v3" />
      <circle cx="9" cy="13.5" r="1.3" fill="currentColor" stroke="none" />
      <circle cx="15" cy="13.5" r="1.3" fill="currentColor" stroke="none" />
    </svg>
  );
}

function IconTool() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" />
    </svg>
  );
}

function IconLive() {
  return (
    <svg className="dots" width="16" height="8" viewBox="0 0 24 8" fill="currentColor">
      <circle cx="4" cy="4" r="3" opacity="0.4" />
      <circle cx="12" cy="4" r="3" opacity="0.7" />
      <circle cx="20" cy="4" r="3" />
    </svg>
  );
}

function IconDownload() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 3v12" />
      <path d="M6 11l6 6 6-6" />
      <path d="M3 21h18" />
    </svg>
  );
}

// Иконка «кофе-брейк» (чашка с паром, как для паузы/перезагрузки диалога).
function IconCoffee() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M4 8h13v6a4 4 0 0 1-4 4H8a4 4 0 0 1-4-4V8z" />
      <path d="M17 9h2a2 2 0 0 1 0 4h-2" />
      <path d="M6 1v3M10 1v3M14 1v3" />
    </svg>
  );
}

function fmtTime(iso?: string): string {
  if (!iso) {
    return "";
  }
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
}
