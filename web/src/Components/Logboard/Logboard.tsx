// Logboard — панель «Логи»: содержимое лог-файлов проекта из каталога logs/
// (GET /api/projects/<name>/logs). Выдвигается снизу по плавающей кнопке
// «Логи» (как Diffboard), внутри — заголовок и кнопка «×» сверху справа.
// При нескольких файлах лога доступен переключатель (tabs). Строки с
// уровнями WARN/ERROR/FATAL подсвечиваются. При открытии лог показывается
// с последней записи (прокрутка к концу), скролл вверх ведёт к ранним.

import { useCallback, useEffect, useRef, useState } from "react";
import { projectLogs } from "@/Api";
import type { LogsView } from "@/Types";
import "./styles.scss";

export interface LogboardProps {
  project: string;
  showLogboard?: boolean;
  toggleLogboard?: () => void;
}

const BASE = "";

export function Logboard(props: LogboardProps) {
  const [logs, setLogs] = useState<LogsView | null>(null);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  // Контейнер строк лога: при открытии/смене файла прокручивается к концу,
  // чтобы сразу видеть последнюю запись («скролл наоборот»: вверх — к ранним).
  const bodyRef = useRef<HTMLDivElement | null>(null);

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

  const active = (logs?.files ?? []).find((f) => f.name === selected);

  // К концу лога при: появлении содержимого, смене выбранного файла и открытии
  // панели (showLogboard=true) — даже если выбранный файл не менялся.
  useEffect(() => {
    scrollToBottom();
  }, [active?.name, props.showLogboard, logs, scrollToBottom]);

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

          {active && (
            <div className="lgmeta">
              <span>файл: <code>{active.name}</code></span>
              <span>{fmtSize(active.size)}</span>
              <span>изменён: {active.modified}</span>
            </div>
          )}

          <div className="logbody" ref={bodyRef}>
            {(active?.content ?? "")
              .split("\n")
              .map((ln, i) => (
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