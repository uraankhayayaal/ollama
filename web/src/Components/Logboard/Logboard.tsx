// Logboard — панель «Логи»: содержимое лог-файлов проекта из каталога logs/
// (GET /api/projects/<name>/logs). Выдвигается снизу по плавающей кнопке
// «Логи» (как Diffboard), внутри — заголовок и кнопка «×» сверху справа.
// При нескольких файлах лога доступен переключатель (tabs). Строки с
// уровнями WARN/ERROR/FATAL подсвечиваются. При открытии лог показывается
// с последней записи (прокрутка к концу), скролл вверх ведёт к ранним.
//
// Real-time: строки из live-через WebSocket-_connection_ актуализируются
// мгновенно. Потоковые строки приходят через props.logLines.

import { useCallback, useEffect, useRef, useState } from "react";
import { projectLogs } from "@/Api";
import type { LogsView } from "@/Types";
import "./styles.scss";

export interface LogboardProps {
  project: string;
  showLogboard?: boolean;
  toggleLogboard?: () => void;
  // Потоковые строки (из App.tsx): «имя файла → массив новых строк».
  logLines: Map<string, string[]>;
}

const BASE = "";

export function Logboard(props: LogboardProps) {
  const [logs, setLogs] = useState<LogsView | null>(null);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const bodyRef = useRef<HTMLDivElement | null>(null);

  // Счётчик примонтированой строки на файл: при открытии/переключении =
  // длина HTTP-содержимого, при получении новой строки — увеличивается.
  const idx = useRef<Map<string, number>>(new Map());
  // Текущие отрендеренные строки (HTTP + stream). Обновляется через state,
  // чтобы React корректно ре-рендерал.
  const [lines, setLines] = useState<string[]>([]);

  const scrollToBottom = useCallback(() => {
    const el = bodyRef.current;
    if (el) {
      el.scrollTo({ top: el.scrollHeight, behavior: "auto" });
    }
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const v = await projectLogs(BASE, props.project);
      setLogs(v);
      setSelected(v.selected);
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setLoading(false);
    }
  }, [props.project]);

  useEffect(() => {
    void load();
  }, [load]);

  // Синхронизируем `lines` при открытии/смене файла.
  useEffect(() => {
    if (!logs || !selected) return;
    const entry = logs.files.find((f) => f.name === selected);
    if (!entry) return;
    const arr = entry.content.split("\n");
    idx.current.set(selected, arr.length - 1);
    setLines(arr);
  }, [logs, selected]);

  // Приход новой строки из stream — append к текущему файлу.
  useEffect(() => {
    if (!selected) return;
    const cur = selected; // capture для сужения типа внутри callback
    const stream = props.logLines.get(cur);
    // На вход приходят ВСЕ потоковые строки всех файлов — берём только свой.
    if (!stream) return;
    setLines((prev) => {
      const last = idx.current.get(selected) ?? prev.length - 1;
      // Если stream меньше предыдущего — сброс (новый заход на тот же файл).
      if (stream.length <= last) return prev;
      const next = [...prev];
      for (let i = last; i < stream.length; i++) {
        const ln = stream[i]!;
        next.push(ln);
      }
      idx.current.set(cur, stream.length - 1);
      return next;
    });
  }, [selected, props.logLines]);

  // Разрешить отложенный скролл (CSS-анимация drawer сдвигает layout —
  // нужно дождаться обновления DOM, которое произойдёт после setState).
  const [scrollPending, setScrollPending] = useState(false);

  useEffect(() => {
    if (lines.length > 0) {
      // lines обновлены — откладываем скролл на следующий макет, когда
      // container уже имеет вычисленную высоту.
      setScrollPending(true);
    }
  }, [lines]);

  useEffect(() => {
    if (scrollPending) {
      setScrollPending(false);
      scrollToBottom();
    }
  }, [scrollPending, scrollToBottom]);

  return (
    <div className={"logboard" + (props.showLogboard === false ? " hidden" : "")}>
      {props.toggleLogboard && (
        <div className="head title-head">
          <p className="hint">Логи</p>
          <button className="btn close" onClick={props.toggleLogboard} title="Свернуть окно">
            ×
          </button>
        </div>
      )}

      {logs && logs.dir && (
        <p className="hint cat" title={logs.dir}>
          {logs.dir}
          {logs.truncated ? " · лог обрезан до последних записей" : ""}
        </p>
      )}

      {loading && <p className="hint">Загружаю логи…</p>}

      {error && <p className="err">{error}</p>}

      {!loading && !error && logs && logs.files.length === 0 && (
        <p className="hint">Лог-файлов в каталоге logs/ пока нет.</p>
      )}

      {!loading && !error && logs && logs.files.length > 0 && (
        <>
          {logs.files.length > 1 && (
            <div className="lgtabs">
              {logs.files.map((f) => (
                <button
                  key={f.name}
                  className={"lgtab" + (f.name === selected ? " active" : "")}
                  onClick={() => setSelected(f.name)}
                  title={f.name + " · " + f.size + " байт · " + f.modified}
                >
                  {f.name}
                </button>
              ))}
            </div>
          )}

          {selected && (() => {
            const active = logs.files.find((f) => f.name === selected);
            if (!active) return null;
            return (
              <div className="lgmeta">
                <span>
                  файл: <code>{active.name}</code>
                </span>
                <span>{fmtSize(active.size)}</span>
                <span>изменён: {active.modified}</span>
              </div>
            );
          })()}

          <div className="logbody" ref={bodyRef}>
            {lines.map((ln, i) => (
              <div key={i} className={lineCls(ln)}>
                {ln || "\u00a0"}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

// lineCls — класс строки лога по уровню: WARN/ERROR/FATAL подсвечиваются.
function lineCls(line: string): string {
  const t = line.trim();
  if (/^(?:ERROR|FATAL)\b/.test(t) || t.includes(" ERROR ") || t.includes(" FATAL ")) {
    return "lg err";
  }
  if (/^WARN\b/.test(t) || t.includes(" WARN ")) {
    return "lg warn";
  }
  if (/^\d{4}-\d{2}-\d{2}/.test(t)) {
    return "lg ts";
  }
  return "lg";
}

function fmtSize(n: number): string {
  if (n >= 1024 * 1024) {
    return (n / 1024 / 1024).toFixed(1) + " МБ";
  }
  if (n >= 1024) {
    return (n / 1024).toFixed(1) + " КБ";
  }
  return n + " Б";
}

function fmtErr(err: unknown): string {
  if (err instanceof Error) {
    return err.message;
  }
  if (err && typeof err === "object" && "message" in err) {
    return String((err as { message: unknown }).message);
  }
  return String(err ?? "Ошибка");
}