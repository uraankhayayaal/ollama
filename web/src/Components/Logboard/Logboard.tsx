// Logboard — панель «Логи»: содержимое лог-файлов проекта из каталога logs/
// (GET /api/projects/<name>/logs). Выдвигается снизу по плавающей кнопке
// «Логи» (как Diffboard), внутри — заголовок и кнопка «×» сверху справа.
// При нескольких файлах лога доступен переключатель (tabs). Строки с
// уровнями WARN/ERROR/FATAL подсвечиваются. При открытии лог показывается
// с последней записи (прокрутка к концу), скролл вверх ведёт к ранним.
//
// Real-time: строки из live-через WebSocket-_connection_ актуализируются
// мгновенно. Потоковые строки приходят через props.logLines.
//
// Верхний action-bar: «Копировать» — все логи в буфер обмена, «Экспорт» —
// скачивание всех логов файлом (содержимое файлов из REST-снапшота, с учётом
// ещё не вошедших в снапшот потоковых строк).

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { projectLogs } from "@/Api";
import type { LogsView } from "@/Types";
import { downloadText, safeName, stampedName } from "@/download";
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
  // Признак «только что скопировали логи» — для краткой фидбеков надписи.
  const [copied, setCopied] = useState(false);

  const bodyRef = useRef<HTMLDivElement | null>(null);

  // props.logLines — НАКОПИТЕЛЬНЫЙ массив всех строк, присланных по WS с
  // момента открытия проекта (App.tsx только добавляет). consumed[file] —
  // сколько из них уже отрисовано. Это разные системы отсчёта, поэтому
  // сравнивать их длины напрямую нельзя (HTTP-снапшот — весь файл).
  const consumed = useRef<Map<string, number>>(new Map());
  // Свежее значение props для эффекта снапшота: не кладём logLines в deps,
  // иначе каждая строка стрима переставляла бы lines из устаревшего HTTP.
  const logLinesRef = useRef(props.logLines);
  logLinesRef.current = props.logLines;

  // Текущие отрендеренные строки (HTTP-снапшот + поток).
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

  // Смена проекта: накопитель строк App.tsx обнуляется, поэтому счётчики
  // потреблённого надо сбросить — иначе stream.length <= done заблокирует
  // добавление строк нового проекта.
  useEffect(() => {
    consumed.current.clear();
    setLines([]);
  }, [props.project]);

  useEffect(() => {
    void load();
  }, [load]);

  // Лог-файл может появиться позже открытия панели (проект только стартовал),
  // а список файлов приходит лишь из REST. Добираем его редким поллингом, но
  // только когда это правда нужно — иначе каждую строку стрима пересобирали
  // бы весь снапшот.
  useEffect(() => {
    if (props.showLogboard === false) return;
    const known = new Set((logs?.files ?? []).map((f) => f.name));
    const stale =
      known.size === 0 || [...props.logLines.keys()].some((f) => !known.has(f));
    if (!stale) return;
    const t = window.setInterval(() => void load(), 3000);
    return () => window.clearInterval(t);
  }, [props.showLogboard, logs, props.logLines, load]);

  // Синхронизируем `lines` при открытии/смене файла (HTTP-снапшот).
  useEffect(() => {
    if (!logs || !selected) return;
    const entry = logs.files.find((f) => f.name === selected);
    if (!entry) return;
    // Снапшот уже содержит строки, пришедшие по WS до него, — помечаем их
    // потреблёнными, иначе стрим-эффект ниже продублирует их в хвост.
    consumed.current.set(selected, logLinesRef.current.get(selected)?.length ?? 0);
    setLines(entry.content.split("\n"));
  }, [logs, selected]);

  // Свежий REST-снапшот включает все потоковые строки, известные на его момент:
  // помечаем их потреблёнными для КАЖДОГО файла, чтобы экспорт/копирование
  // не задвоили уже вошедшие в снапшот строки неоткрытых файлов. Эффект
  // зависит только от `logs` (приход снапшота), поэтому счётчики растут
  // синхронно с фактическим содержанием файлов на диске.
  useEffect(() => {
    if (!logs) return;
    for (const f of logs.files) {
      consumed.current.set(f.name, logLinesRef.current.get(f.name)?.length ?? 0);
    }
  }, [logs]);

  // Новые строки из потока — дописываем только невиданный хвост.
  useEffect(() => {
    if (!selected) return;
    const stream = props.logLines.get(selected);
    if (!stream) return;
    const done = consumed.current.get(selected) ?? 0;
    if (stream.length <= done) return;
    consumed.current.set(selected, stream.length);
    setLines((prev) => [...prev, ...stream.slice(done)]);
  }, [selected, props.logLines]);

  // Авто-скролл вниз при появлении/смене строк. useLayoutEffect: DOM уже
  // содержит новые строки, но браузер ещё не рисовал — scrollHeight
  // вычисляется синхронно, поэтому CSS-анимация шторки не мешает.
  useLayoutEffect(() => {
    scrollToBottom();
  }, [lines, scrollToBottom]);

  const hasLogs = !!logs && logs.files.length > 0;

  // Текущее содержимое файла лога: для выбранного — уже слитый живой буфер
  // (`lines`), для остальных — REST-снапшот + хвост, не вошедший в снапшот.
  const fileLogText = (name: string): string => {
    if (!logs) return "";
    const entry = logs.files.find((f) => f.name === name);
    if (!entry) return "";
    if (name === selected) {
      return lines.join("\n");
    }
    const snap = entry.content.split("\n");
    const stream = props.logLines.get(name) ?? [];
    const done = consumed.current.get(name) ?? 0;
    return [...snap, ...stream.slice(done)].join("\n");
  };

  // Полный дайджест «все логи»: каждый файл со своим заголовком.
  const allLogsText = (): string => {
    if (!logs) return "";
    return logs.files.map((f) => `===== ${f.name} =====\n${fileLogText(f.name)}`).join("\n\n");
  };

  const onCopyAll = async () => {
    const text = allLogsText();
    if (!text) return;
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setError("Не удалось скопировать логи в буфер обмена");
    }
  };

  const onExportAll = () => {
    const text = allLogsText();
    if (!text) return;
    downloadText(stampedName("logs", `${safeName(props.project)}.txt`), text);
  };

  return (
    <div className={"logboard" + (props.showLogboard === false ? " hidden" : "")}>
      {props.toggleLogboard && (
        <div className="head title-head">
          <p className="hint">Логи</p>
          <div className="abar">
            {hasLogs && (
              <button className="ab" onClick={() => void onCopyAll()} title="Скопировать все логи в буфер обмена">
                <IconCopy />
                {copied ? "Скопировано" : "Копировать"}
              </button>
            )}
            <button className="ab" onClick={onExportAll} disabled={!hasLogs} title="Скачать все логи файлом">
              <IconDownload />
              Экспорт
            </button>
          </div>
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

// Иконка «скачать» для кнопки экспорта (action-bar).
function IconDownload() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M12 3v12" />
      <path d="M6 11l6 6 6-6" />
      <path d="M3 21h18" />
    </svg>
  );
}

// Иконка «копировать» для кнопки копирования (action-bar).
function IconCopy() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="9" y="9" width="12" height="12" rx="2" />
      <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
    </svg>
  );
}