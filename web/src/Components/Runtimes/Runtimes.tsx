// Runtimes — панель «Рантайм»: логи ЗАПУЩЕННОГО ПРИЛОЖЕНИЯ (Ф-2).
//
// Чем отличается от Logboard: там журнал работы агентов (какие инструменты
// модель звала, с каким исходом) из файлов logs/. Здесь — то, что вывело само
// приложение, когда его запустили через ReadAppLogs. Именно эти строки
// показывают «listen tcp :8080: address already in use» или падение миграции,
// которые агентский журнал зафиксировать не может.
//
// Данные приходят двумя путями (как в Logboard):
//   • HTTP-снапшот — GET /api/projects/<name>/applog (хвост на момент запроса);
//   • живой поток — WS type="applog" через props.appLogLines.
//
// Счётчики разные (снапшот уже включает строки, пришедшие по WS до него), поэтому
// потреблённые строки считаются отдельно, по образцу Logboard.

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { projectAppLogs } from "@/Api";
import type { AppLogLine, AppLogView } from "@/Types";
import "./styles.scss";

export interface RuntimesProps {
  project: string;
  // НАКОПИТЕЛЬНЫЙ массив строк рантайма, пришедших по WS с момента открытия
  // проекта (App.tsx только добавляет).
  appLogLines: AppLogLine[];
}

const BASE = "";
const MAX_LINES = 2000;

export function Runtimes(props: RuntimesProps) {
  const [view, setView] = useState<AppLogView | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [following, setFollowing] = useState(true);

  const bodyRef = useRef<HTMLDivElement | null>(null);
  const followingRef = useRef(true);
  // Сколько строк потока уже учтено в отрисованном буфере.
  const consumed = useRef(0);
  // Свежий props без пересборки эффекта снапшота: каждый приход строки иначе
  // перезапрашивал бы REST.
  const streamRef = useRef(props.appLogLines);
  streamRef.current = props.appLogLines;
  // Отрисованные строки: снапшот + хвост потока.
  const [lines, setLines] = useState<AppLogLine[]>([]);
  const [streamCount, setStreamCount] = useState(0);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const v = await projectAppLogs(BASE, props.project);
      setView(v);
      // Строки, пришедшие по WS до снапшота, уже входят в его хвост —
      // помечаем их потреблёнными, иначе стрим-эффект продублирует их.
      consumed.current = streamRef.current.length;
      setLines(v.lines ?? []);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [props.project]);

  // Смена проекта: поток от предыдущего обнулён, поэтому и счётчик, и строки.
  useEffect(() => {
    consumed.current = 0;
    setLines([]);
    setStreamCount(0);
  }, [props.project]);

  useEffect(() => {
    void load();
  }, [load]);

  // Рантайм появляется не сразу (агент запускает приложение позже), а список
  // файлов из REST без строк бесполезен: подтягиваем хвост редким поллингом,
  // пока строк нет.
  useEffect(() => {
    if ((view?.lines?.length ?? 0) > 0) return;
    const t = window.setInterval(() => void load(), 4000);
    return () => window.clearInterval(t);
  }, [view, load]);

  // Хвост потока дописываем ровно один раз: consumed.current — это «уже учтено»,
  // а длина потока — счётчик с нуля. Ограничиваем буфер, иначе приложение с
  // подробным логом (HTTP-запросы, прогресс) съест память вкладки.
  useEffect(() => {
    const stream = props.appLogLines;
    if (stream.length <= consumed.current) return;
    const fresh = stream.slice(consumed.current);
    consumed.current = stream.length;
    setStreamCount(stream.length);
    setLines((prev) => [...prev, ...fresh].slice(-MAX_LINES));
  }, [props.appLogLines]);

  const scrollToBottom = useCallback(() => {
    const el = bodyRef.current;
    if (el && followingRef.current) {
      el.scrollTo({ top: el.scrollHeight, behavior: "auto" });
    }
  }, []);

  useLayoutEffect(() => {
    scrollToBottom();
  }, [lines, scrollToBottom]);

  const onScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    followingRef.current = nearBottom;
    setFollowing(nearBottom);
  };

  const jumpToBottom = () => {
    followingRef.current = true;
    setFollowing(true);
    const el = bodyRef.current;
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
  };

  const needle = query.trim().toLocaleLowerCase();
  const visible = lines.filter((l) => !needle || l.line.toLocaleLowerCase().includes(needle));

  const empty = !loading && !error && (view?.status === "skipped" || lines.length === 0);

  return (
    <div className="runtimes">
      <div className="head title-head">
        <p className="hint">Рантайм — логи запущенного приложения</p>
        <div className="abar">
          <button className="ab" onClick={jumpToBottom} disabled={!following} title="Перейти к последним строкам">
            Вниз
          </button>
          <button className="ab" onClick={() => void load()} title="Перечитать хвост логов">
            Обновить
          </button>
        </div>
      </div>

      <div className="log-tools">
        <label className="log-search">
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Найти в логах рантайма"
            aria-label="Найти в логах рантайма"
          />
          {!!query && (
            <button type="button" onClick={() => setQuery("")} title="Очистить поиск">
              ×
            </button>
          )}
        </label>
      </div>

      {loading && <p className="hint">Загружаю логи рантайма…</p>}
      {error && <p className="err">{error}</p>}

      {empty && (
        <p className="hint">
          {view?.hint ??
            "Логов рантайма пока нет. Они появятся, когда агент прочитает логи приложения (ReadAppLogs) или запустит его."}
        </p>
      )}

      {!loading && !error && lines.length > 0 && (
        <p className="hint">
          строк: {visible.length} из {lines.length}
          {streamCount > 0 ? ` · в потоке: ${streamCount}` : ""}
        </p>
      )}

      <div className="runtimes-body" ref={bodyRef} onScroll={onScroll}>
        {visible.map((l, i) => (
          <div key={`${l.time}-${i}`} className={lineCls(l.line)}>
            <span className="t">{fmtTime(l.time)}</span>
            {l.source && <span className="src">{l.source}</span>}
            <span className="txt">{l.line}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// lineCls подсвечивает тревожные строки: стектрейсы и ошибки в рантайме —
// ровно то, ради чего панель и сделана.
function lineCls(text: string): string {
  const t = text.toLocaleLowerCase();
  if (/\b(panic|fatal|traceback|uncaught|segmentation fault)\b/.test(t)) return "line err";
  if (/\b(error|exception|failed|failure|refused|denied|not found|cannot|unable)\b/.test(t)) return "line warn";
  return "line";
}

function fmtTime(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString("ru-RU", { hour12: false });
}
