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
  onContinue,
  canContinue,
  endRef,
  busy,
}: {
  chat: ChatMsg[];
  // live — «плавающее» потоковое сообщение модели (стриминг, Ф-3); рендерится
  // с маркером «…» до прихода финального протокола chat с ролью assistant.
  live: { id: string; agent: string; content: string } | null;
  onSend: (text: string) => void;
  // Продолжить: повторить текущую задачу проекта (продолжение после
  // остановки/ошибки). Кнопка неактивна, пока продолжение невозможно
  // (нет задачи или запуск активен).
  onContinue: () => void;
  canContinue: boolean;
  endRef: React.RefObject<HTMLDivElement | null>;
  busy?: boolean;
}) {
  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [chat, live, endRef]);

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

      <div className="actions">
        <button
          type="button"
          className="continue"
          onClick={onContinue}
          disabled={!canContinue}
          title={
            canContinue
              ? "Продолжить выполнение текущей задачи"
              : "Продолжение невозможно: нет задачи или запуск уже идёт"
          }
        >
          ▷ Продолжить
        </button>
      </div>

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
