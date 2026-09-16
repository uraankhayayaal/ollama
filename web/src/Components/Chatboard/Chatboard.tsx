// Chatboard — панель чата с агентом: история + поле ввода + автокролл.
// REST: postChat / chatHistory; live: "chat" (см. types.ts ChatMsg).
// Канон-архитектура — как Dashboard (Components/Chatboard/*).

import { useEffect } from "react";
import type { ChatMsg } from "@/Types";
// import { APIError } from "@/Api";
import "./styles.scss";

export function Chatboard({
  chat,
  onSend,
  endRef,
  busy,
}: {
  chat: ChatMsg[];
  onSend: (text: string) => void;
  endRef: React.RefObject<HTMLDivElement | null>;
  busy?: boolean;
}) {
  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [chat, endRef]);

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
      <ul className="history">
        {chat.map((m) => (
          <li key={m.id} className={"msg " + (m.role ?? "agent").toLowerCase()}>
            <span className="who">{m.agent ?? m.role ?? "agent"}</span>
            <p className="content">{m.content}</p>
            <time className="when">{fmtTime(m.time)}</time>
          </li>
        ))}
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

function fmtTime(iso?: string): string {
  if (!iso) {
    return "";
  }
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" });
}
