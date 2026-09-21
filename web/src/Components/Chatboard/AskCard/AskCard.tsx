// AskCard — карточка-вардин структурированного вопроса ассистента (AskUser).
//
// Порядок (Р-4 плана «спроси пользователя при неоднозначности»):
// - пачка вопросов показывается пошагово: шапка «Вопрос i из N» + «Назад»;
// - kind=single — клик по варианту сразу отправляет ответ и переходит дальше;
// - kind=multi — чекбоксы + кнопка «Подтвердить» (активируется при ≥1 выборе);
// - allow_custom — вариант «Свой ответ» с полем ввода;
// - recommended — вариант выделен рамкой и бейджем «рекомендую»;
// - когда отвечены все вопросы — карточка сворачивается в итог «Ответы получены».
//
// Ответы уходят на POST /api/projects/{id}/ask/{askID}/answer; ответ на каждый
// шаг сервер возвращает {answered, total} — при answered==total карточка
// показывает итог.

import { useState } from "react";
import type { AskAnswerBody, AskAnswerResult, AskMsg, AskQuestion } from "@/Types";
import "./styles.scss";

export function AskCard({
  ask,
  answerAsk,
}: {
  ask: AskMsg;
  answerAsk: (askID: string, body: AskAnswerBody) => Promise<AskAnswerResult>;
}) {
  const questions = ask.questions ?? [];
  const total = questions.length;
  const [step, setStep] = useState(0);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  // Выбор пользователя по каждому вопросу — сохраняем между шагами («Назад»)
  // и для итоговой сводки после ответа на все вопросы.
  const [saved, setSaved] = useState<Record<string, { selected: string[]; custom: string }>>({});

  if (total === 0) {
    return null;
  }

  const submit = async (questionID: string, value: { selected: string[]; custom: string }) => {
    if (busy) {
      return;
    }
    setBusy(true);
    setErr("");
    try {
      const res = await answerAsk(ask.id, {
        question_id: questionID,
        selected: value.selected,
        custom: value.custom || undefined,
      });
      setSaved((prev) => ({ ...prev, [questionID]: value }));
      if (res.answered >= res.total) {
        setDone(true);
      } else if (step < total - 1) {
        setStep((s) => s + 1);
      }
    } catch (e) {
      setErr(e instanceof Error ? e.message : "Не удалось отправить ответ");
    } finally {
      setBusy(false);
    }
  };

  // Итог: все вопросы отвечены.
  if (done) {
    return (
      <div className="ask done">
        <span className="ask-title">Ответы получены</span>
        <div className="ask-summary">
          {questions.map((q) => (
            <div className="ask-summary-row" key={q.id}>
              <span className="q">{q.text}</span>
              <span className="a">{answerLabel(q, saved[q.id])}</span>
            </div>
          ))}
        </div>
      </div>
    );
  }

  const q = questions[step];
  return (
    <div className="ask">
      <div className="ask-header">
        <span className="ask-title">
          Вопрос {step + 1} из {total}
        </span>
        {step > 0 && (
          <button
            type="button"
            className="back"
            onClick={() => {
              setErr("");
              setStep((s) => s - 1);
            }}
            disabled={busy}
          >
            ← Назад
          </button>
        )}
      </div>
      <AskStep
        key={q.id}
        q={q}
        initial={saved[q.id]}
        busy={busy}
        err={err}
        onAnswer={(value) => submit(q.id, value)}
      />
    </div>
  );
}

// AskStep — один шаг вардина: варианты (один клик / чекбоксы + «Подтвердить»)
// и необязательное поле «Свой ответ».
function AskStep({
  q,
  initial,
  busy,
  err,
  onAnswer,
}: {
  q: AskQuestion;
  initial?: { selected: string[]; custom: string };
  busy: boolean;
  err: string;
  onAnswer: (value: { selected: string[]; custom: string }) => void;
}) {
  const multi = q.kind === "multi";
  const [sel, setSel] = useState<string[]>(initial?.selected ?? []);
  const [custom, setCustom] = useState(initial?.custom ?? "");
  const [customMode, setCustomMode] = useState(!!initial?.custom);

  const allowCustom = q.allow_custom !== false;
  // Для single вывод «Свой ответ» активируется переключателем (поле ввода +
  // кнопка «Ответить»); выбор варианта — мгновенный ответ без подтверждения.
  const toggle = (id: string) => {
    if (busy) {
      return;
    }
    if (multi) {
      setSel((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
      return;
    }
    onAnswer({ selected: [id], custom: "" });
  };

  const canSubmit = multi
    ? sel.length > 0 || (customMode && custom.trim().length > 0)
    : customMode && custom.trim().length > 0;

  return (
    <>
      <p className="ask-qtext">{q.text}</p>
      <div className="ask-opts">
        {q.options.map((o) => {
          const picked = sel.includes(o.id);
          return (
            <button
              key={o.id}
              type="button"
              className={[
                "opt",
                picked ? "sel" : "",
                o.recommended ? "rec" : "",
                multi && picked ? "picked" : "",
              ].join(" ")}
              onClick={() => toggle(o.id)}
              disabled={busy}
              title={o.recommended ? "Рекомендуемый вариант" : undefined}
            >
              {o.recommended && <span className="rec-badge">рекомендую</span>}
              <span className="opt-label">{o.label}</span>
              {multi && <span className="opt-check">{picked ? "✓" : ""}</span>}
            </button>
          );
        })}
        {allowCustom && (
          <button
            type="button"
            className={"opt custom" + (customMode ? " sel" : "")}
            onClick={() => {
              if (!busy) {
                setCustomMode((v) => !v);
              }
            }}
            disabled={busy}
          >
            <span className="opt-label">Свой ответ</span>
            <span className="opt-check">{customMode ? "✓" : ""}</span>
          </button>
        )}
        {customMode && (
          <div className="ask-custom">
            <input
              value={custom}
              onChange={(e) => setCustom(e.target.value)}
              placeholder="Введите свой ответ…"
              disabled={busy}
              autoFocus
            />
            <button type="button" className="btn" onClick={() => onAnswer({ selected: sel, custom: custom.trim() })} disabled={busy || !custom.trim()}>
              Ответить
            </button>
          </div>
        )}
      </div>
      {multi && (
        <button type="button" className="confirm" onClick={() => onAnswer({ selected: sel, custom: customMode ? custom.trim() : "" })} disabled={busy || !canSubmit}>
          Подтвердить
        </button>
      )}
      {err && <p className="ask-err">{err}</p>}
    </>
  );
}

// answerLabel — человекочитаемый ответ пользователя для сводки «Ответы получены»
// и для последующей истории: метки выбранных вариантов (+ произвольный текст).
function answerLabel(q: AskQuestion, value?: { selected: string[]; custom: string }): string {
  if (!value) {
    return "—";
  }
  const parts = q.options
    .filter((o) => value.selected.includes(o.id))
    .map((o) => o.label);
  if (value.custom) {
    parts.push(value.custom);
  }
  return parts.length > 0 ? parts.join(", ") : "—";
}