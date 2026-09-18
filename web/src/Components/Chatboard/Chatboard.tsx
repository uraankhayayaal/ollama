// Chatboard — панель чата с агентом: история + поле ввода + автокролл.
// REST: postChat / chatHistory; live: "chat" (см. types.ts ChatMsg).
// Канон-архитектура — как Dashboard (Components/Chatboard/*).

import { useEffect } from "react";
import type { ChatMsg } from "@/Types";
// import { APIError } from "@/Api";
import "./styles.scss";

export function Chatboard({
  chat,
  live,
  onSend,
  endRef,
  busy,
  collapsed = false,
  onToggleCollapse,
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
}) {
  useEffect(() => {
    if (!collapsed) {
      endRef.current?.scrollIntoView({ behavior: "smooth" });
    }
  }, [chat, live, endRef, collapsed]);

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
          {live && (
            <div className="st live" title="Модель печатает…">
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
        {onToggleCollapse && (
          <button className="collapse-btn" onClick={onToggleCollapse} title="Свернуть чат влево">
            «
          </button>
        )}
      </div>

      <ul className="history">
        {chat.map((m) => (
          <li key={m.id} className={"msg " + (m.role ?? "agent").toLowerCase()}>
            <span className="who">{m.agent ?? m.role ?? "agent"}</span>
            <p className="content">{m.content}</p>
            <time className="when">{fmtTime(m.time)}</time>
          </li>
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

function fmtTime(iso?: string): string {
  if (!iso) {
    return "";
  }
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
}
