import { useState } from "react";
import type { AskAnswerBody, AskAnswerResult, ChatMsg } from "@/Types";
import { AskCard } from "@/Components/Chatboard/AskCard";

interface Props {
  msg: ChatMsg;
  isStreaming?: boolean;
  onAskAnswer?: (askID: string, body: AskAnswerBody) => Promise<AskAnswerResult>;
}

export function AssistantResponse({ msg, isStreaming, onAskAnswer }: Props) {
  const [thoughtOpen, setThoughtOpen] = useState(false);
  const hasThinking = !!(msg.thinking ?? "").trim();
  const isAsk = msg.role === "ask";
  const content = msg.content ?? "";
  const empty = content.trim().length === 0;

  const toggleThought = () => setThoughtOpen((o) => !o);

  return (
    <div
      className={`msg-wrapped assistant${isStreaming ? " working" : ""}${isAsk ? " ask" : ""}`}
    >
      <div className="wrapper-ring" />
      <div className="wrapper-inner">
        {isStreaming && <div className="streaming-indicator">thinking...</div>}

        <div className="head-row">
          <span className="who">{msg.agent ?? "assistant"}</span>
          <span className="when">{formatTime(msg.time)}</span>
        </div>

        {hasThinking && (
          <details
            className={`thinking-block${thoughtOpen ? " open" : ""}`}
            onToggle={(e) =>
              setThoughtOpen((e.target as HTMLDetailsElement).open)
            }
          >
            <summary onClick={toggleThought}>
              <span className="chev" />
              <span className="thinking-label">thinking</span>
              {!thoughtOpen && (
                <span className="thinking-preview">
                  {(msg.thinking ?? "").slice(0, 80)}…
                </span>
              )}
            </summary>
            <div className="thinking-content">
              {msg.thinking}
            </div>
          </details>
        )}

        {isAsk && msg.ask ? (
          <AskCard ask={msg.ask} onAskAnswer={onAskAnswer!} />
        ) : empty ? (
          <div className="content empty-placeholder">∅</div>
        ) : (
          <div className="content">{content}</div>
        )}
      </div>
    </div>
  );
}

function formatTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}
