// Logboard — панель «Логи»: содержимое лог-файлов проекта из каталога logs/
// (GET /api/projects/<name>/logs). Выдвигается снизу по плавающей кнопке
// «Логи» (как Diffboard), внутри — заголовок и кнопка «×» сверху справа.
// При нескольких файлах лога доступен переключатель (tabs). Строки с
// уровнями WARN/ERROR/FATAL подсвечиваются.
//
// Скролл: панель по умолчанию ПРИЖАТА к хвосту — новые строки всегда
// появляются снизу и вид остаётся на последней записи. Уйти вверх можно
// только осознанно (скролл пользователем) — тогда появляется кнопка
// «К новым строкам». Скролл-контейнер .logbody не размонтируется никогда:
// переустановка сбрасывала scrollTop в 0, и любое фоновое обновление
// возвращало лог наверх. Обновление файла накладывается на буфер по общему
// префиксу, поэтому неизменившиеся строки сохраняют DOM-узлы — выделение
// текста и позиция скролла переживают и добавление хвоста, и фильтры.
//
// Real-time: строки из live-через WebSocket-_connection_ актуализируются
// мгновенно. Потоковые строки приходят через props.logLines.
//
// Верхний action-bar: «Копировать» — все логи в буфер обмена, «Экспорт» —
// скачивание всех логов файлом (содержимое файлов из REST-снапшота, с учётом
// ещё не вошедших в снапшот потоковых строк), «Очистить» — обрезка файлов логов
// на сервере (необратимо, поэтому за подтверждением).

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { clearProjectLogs, projectLogs } from "@/Api";
import type { LogStream, LogsView } from "@/Types";
import { downloadText, safeName, stampedName } from "@/download";
import "./styles.scss";

export interface LogboardProps {
  project: string;
  showLogboard?: boolean;
  toggleLogboard?: () => void;
  // Потоковые строки (из App.tsx): «имя файла → накопленный хвост».
  logLines: Map<string, LogStream>;
}

const BASE = "";

// Граница буфера отрисовки. Хвост прижат — держим последние строки; если
// пользователь ушёл вверх и читает историю, верх срезается мягче, чтобы не
// выкидывать текст, который он как раз читает. Без границы DOM лога растёт
// весь сеанс (поток + снапшот до 1 МБ) и скролл начинает заметно подтормаживать.
const MAX_LINES = 5000;
const MAX_LINES_SOFT = 20000;
// Порог, ниже которого считаем, что пользователь у хвоста (px).
const NEAR_BOTTOM_PX = 48;

// Строка буфера отрисовки. id стабилен в пределах жизни строки: React не
// пересоздаёт DOM-узел, поэтому выделение текста переживает и добавление
// хвоста, и смену фильтра. fresh — «только что пришла», по нему один раз
// проигрывается анимация появления.
interface Row {
  text: string;
  id: number;
  fresh: boolean;
}

export function Logboard(props: LogboardProps) {
  const [logs, setLogs] = useState<LogsView | null>(null);
  const [selected, setSelected] = useState("");
  // true с самого рендера: первый кадр панели должен показывать «Загружаю
  // логи…», а не мигнуть «Лог-файлов пока нет».
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  // Признак «только что скопировали логи» — для краткой фидбеков надписи.
  const [copied, setCopied] = useState(false);
  // Идёт обрезка файлов логов на сервере — кнопка ждёт ответа.
  const [clearing, setClearing] = useState(false);
  const [query, setQuery] = useState("");
  const [level, setLevel] = useState<"all" | "warn" | "err">("all");
  // followingRef — намерение «держим хвост», following — его отражение в UI.
  // Включено с самого открытия: лог всегда показывает последнюю строку.
  const followingRef = useRef(true);
  const [following, setFollowing] = useState(true);
  // deferred — прижатие к хвосту отложено из-за выделения текста: строки
  // копятся снизу, но дёргать выделение нельзя. Кнопка «К новым строкам»
  // при этом остаётся доступной.
  const [deferred, setDeferred] = useState(false);

  const bodyRef = useRef<HTMLDivElement | null>(null);

  // props.logLines — НАКОПИТЕЛЬНЫЙ хвост строк, присланных по WS с момента
  // открытия проекта (App.tsx только добавляет, из начала отбрасывает).
  // consumed[file] — АБСОЛУТНЫЙ индекс первой ещё не отрисованной строки
  // потока. Сравнивать consumed с lines.length нельзя: это разные системы
  // отсчёта (HTTP-снапшот — весь файл), а нумерация сдвинута на dropped.
  const consumed = useRef<Map<string, number>>(new Map());
  // Граница потока на момент чтения последнего снапшота каждого файла: строки
  // после неё ещё не попали в файл на диске. Нужна копированию/экспорту.
  const snapAt = useRef<Map<string, number>>(new Map());
  // Свежее значение props для эффекта снапшота: не кладём logLines в deps,
  // иначе каждая строка стрима переставляла бы строки из устаревшего HTTP.
  const logLinesRef = useRef(props.logLines);
  logLinesRef.current = props.logLines;

  // Текущие отрендеренные строки (HTTP-снапшот + поток).
  const [rows, setRows] = useState<Row[]>([]);
  const nextLineId = useRef(0);
  // Содержимое последнего применённого снапшота по каждому файлу: поллинг
  // каждые 3с не должен пересобирать буфер, если файл на диске не менялся.
  const applied = useRef<Map<string, string>>(new Map());
  // Файл, чьи строки сейчас лежат в буфере. Отличие от applied[file]
  // отличает «открытие файла» (пересобрать и прижать к хвосту) от «файл
  // обновился» (уважаем текущий скролл пользователя).
  const rendered = useRef("");

  // scrollEnd — прижать контейнер к последней строке (безусловно).
  const scrollEnd = useCallback(() => {
    const el = bodyRef.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.clientHeight;
    if (el.scrollTop !== bottom) el.scrollTop = bottom;
  }, []);

  // pin — прижать к хвосту, но только если пользователь этого хочет и это не
  // сломает выделение. Выделение ОТКЛАДЫВАЕТ прижатие, а не выключает follow:
  // стоит снять выделение, и хвост снова догоняется сам.
  const pin = useCallback(() => {
    const el = bodyRef.current;
    if (!el || !followingRef.current) return;
    if (hasSelectionIn(el)) {
      setDeferred(true);
      return;
    }
    setDeferred(false);
    scrollEnd();
  }, [scrollEnd]);

  const jumpToBottom = () => {
    // Явное «показать хвост»: снимаем выделение, иначе прижатие не сработает.
    const sel = document.getSelection();
    if (sel && !sel.isCollapsed) sel.removeAllRanges();
    followingRef.current = true;
    setFollowing(true);
    setDeferred(false);
    scrollEnd();
  };

  const handleLogScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    // Собственный прижим тоже вызывает scroll, но приводит к nearBottom —
    // выключить follow он не может. Поэтому ручной скролл вверх — единственное,
    // что гасит слежение за хвостом.
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
    followingRef.current = nearBottom;
    setFollowing(nearBottom);
  };

  // Можно ли подрезать буфер сверху: хвост прижат и пользователь ничего не
  // выделил (иначе обрезка порвала бы выделение).
  const canTrim = useCallback(() => {
    const el = bodyRef.current;
    return !!el && followingRef.current && !hasSelectionIn(el);
  }, []);

  const newRow = useCallback((text: string, fresh: boolean): Row => ({ text, id: nextLineId.current++, fresh }), []);

  // silent — фоновое обновление: не мигаем «Загружаю логи…» и не стираем уже
  // показанный лог из-за одной неудачи.
  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const v = await projectLogs(BASE, props.project);
      setLogs(v);
      // Файл, выбранный пользователем, сохраняем, пока он есть в ответе:
      // иначе поллинг каждые 3с возвращал бы панель на серверный выбор.
      setSelected((prev) => (prev && v.files.some((f) => f.name === prev) ? prev : v.selected));
      setError("");
    } catch (e) {
      if (!silent) setError(fmtErr(e));
    } finally {
      if (!silent) setLoading(false);
    }
  }, [props.project]);

  // Смена проекта: накопитель строк App.tsx обнуляется, поэтому счётчики
  // потреблённого и применённых снапшотов надо сбросить — иначе
  // stream.length <= done заблокирует добавление строк нового проекта.
  useEffect(() => {
    consumed.current.clear();
    snapAt.current.clear();
    applied.current.clear();
    rendered.current = "";
    nextLineId.current = 0;
    followingRef.current = true;
    setFollowing(true);
    setRows([]);
  }, [props.project]);

  useEffect(() => {
    void load();
  }, [load]);

  // Список лог-файлов приходит только из REST: файл может появиться позже
  // открытия панели (проект только стартовал). Добираем его редким поллингом,
  // но только когда это правда нужно — иначе каждые 3с пересобирали бы
  // снапшот впустую. В deps — только факт появления НОВОГО файла в потоке,
  // а не каждая строка: иначе таймер всё время пересоздавался бы заново.
  const streamFiles = props.logLines.size;
  useEffect(() => {
    if (props.showLogboard === false) return;
    const known = new Set((logs?.files ?? []).map((f) => f.name));
    const stale = known.size === 0 || [...logLinesRef.current.keys()].some((f) => !known.has(f));
    if (!stale) return;
    const t = window.setInterval(() => void load(true), 3000);
    return () => window.clearInterval(t);
  }, [props.showLogboard, logs, load, streamFiles]);

  // Накладываем содержимое файла на буфер по общему префиксу. Неизменившаяся
  // часть сохраняет id, а значит и DOM-узлы: выделение текста и позиция
  // скролла переживают обновление. Разошедшийся хвост (ротация файла, обрезка
  // снапшота) заменяется целиком.
  useEffect(() => {
    if (!logs || !selected) return;
    const entry = logs.files.find((f) => f.name === selected);
    // Файл исчез (ротация/удаление) — в буфере не должно остаться его строк.
    if (!entry) {
      rendered.current = "";
      setRows([]);
      return;
    }
    // Пересобирать имеет смысл только когда в буфере другой файл или файл на
    // диске реально изменился: иначе поллинг каждые 3с дёргал бы буфер впустую
    // (и, что важнее, ронял бы скролл при возврате к прежнему файлу).
    if (rendered.current === selected && applied.current.get(selected) === entry.content) return;
    applied.current.set(selected, entry.content);
    // Смена файла (или проекта) — открытие: всегда с последней строки и без
    // общих строк с прежним файлом.
    const opening = rendered.current !== selected;
    rendered.current = selected;
    if (opening) {
      followingRef.current = true;
      setFollowing(true);
      setDeferred(false);
      setRows([]);
    }
    // Снапшот уже содержит строки, пришедшие по WS до него, — помечаем их
    // потреблёнными, иначе стрим-эффект ниже продублирует их в хвост.
    consumed.current.set(selected, streamEnd(logLinesRef.current.get(selected)));
    const trim = canTrim();
    setRows((prev) =>
      mergeRows(prev, entry.content.split("\n"), !opening, trim ? MAX_LINES : MAX_LINES_SOFT, newRow),
    );
  }, [logs, selected, canTrim, newRow]);

  // Свежий REST-снапшот включает все потоковые строки, известные на его момент:
  // помечаем их потреблёнными для КАЖДОГО файла, чтобы стрим-эффект ниже их не
  // продублировал в хвост. Эффект зависит только от `logs` (приход снапшота),
  // поэтому счётчики растут синхронно с фактическим содержанием файлов.
  // snapAt — та же граница, но для копирования/экспорта: буфер отрисовки
  // ограничен, а снапшот файла — полный, и строки, пришедшие по WS после его
  // чтения, в копирование должны попасть.
  useEffect(() => {
    if (!logs) return;
    for (const f of logs.files) {
      const end = streamEnd(logLinesRef.current.get(f.name));
      consumed.current.set(f.name, end);
      snapAt.current.set(f.name, end);
    }
  }, [logs]);

  // Новые строки из потока — дописываем только невиданный хвост. Позиция
  // считается по абсолютному индексу (consumed), потому что App.tsx отбрасывает
  // старые строки из буфера и сдвигает нумерацию на dropped.
  useEffect(() => {
    if (!selected) return;
    const stream = props.logLines.get(selected);
    if (!stream) return;
    const end = streamEnd(stream);
    const done = consumed.current.get(selected) ?? 0;
    if (end <= done) return;
    consumed.current.set(selected, end);
    const tail = stream.lines.slice(Math.max(0, done - stream.dropped));
    const trim = canTrim();
    setRows((prev) => appendRows(prev, tail, trim ? MAX_LINES : MAX_LINES_SOFT, newRow));
  }, [selected, props.logLines, canTrim, newRow]);

  // Прижимаем к хвосту в useLayoutEffect: DOM уже содержит новые строки, но
  // ещё не отрисован, поэтому scrollHeight актуален и подвижки не видно.
  // Повтор на следующем кадре — на случай, если высоты строк уточнились
  // после лейаута. Своего scroll это не ломает: он приводит к nearBottom.
  useLayoutEffect(() => {
    pin();
    const raf = requestAnimationFrame(() => pin());
    return () => cancelAnimationFrame(raf);
  }, [rows, pin]);

  // Шторка выезжает снизу: высота контейнера меняется после анимации, поэтому
  // первый кадр открытой панели измеряет ещё неверную геометрию. Открытие —
  // осознанное действие, поэтому здесь прижимаем к хвосту безусловно.
  useLayoutEffect(() => {
    if (props.showLogboard === false) return;
    followingRef.current = true;
    setFollowing(true);
    setDeferred(false);
    scrollEnd();
    const raf = requestAnimationFrame(() => scrollEnd());
    const t = window.setTimeout(() => scrollEnd(), 320);
    return () => {
      cancelAnimationFrame(raf);
      window.clearTimeout(t);
    };
  }, [props.showLogboard, scrollEnd]);

  const hasLogs = !!logs && logs.files.length > 0;
  const active = hasLogs ? logs!.files.find((f) => f.name === selected) : undefined;

  const visibleRows = useMemo(() => {
    const q = query.trim().toLocaleLowerCase();
    return rows.filter((row) => {
      const kind = lineKind(row.text);
      if (level !== "all" && kind !== level) return false;
      return !q || row.text.toLocaleLowerCase().includes(q);
    });
  }, [rows, level, query]);

  // Содержимое файла лога для копирования/экспорта: снапшот файла целиком
  // плюс хвост потока, который в снапшот ещё не вошёл. Буфер отрисовки для
  // этого не годится — он ограничен по размеру.
  const fileLogText = (name: string): string => {
    if (!logs) return "";
    const entry = logs.files.find((f) => f.name === name);
    if (!entry) return "";
    const snap = entry.content.split("\n");
    const stream = props.logLines.get(name);
    const tail = stream ? stream.lines.slice(Math.max(0, (snapAt.current.get(name) ?? 0) - stream.dropped)) : [];
    return [...snap, ...tail].join("\n");
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

  // Очистка обрезает файлы логов НА СЕРВЕРЕ: сбросить только буфер панели
  // бессмысленно, его через 3с вернёт поллинг из файла. После обрезки
  // перечитываем снимок — иначе в буфере остались бы старые строки (поллинг
  // включается только при появлении НОВОГО файла, а файл остался тем же).
  // Перечитываем НЕ тихо: снимок уже загружен, поэтому надписи «Загружаю
  // логи…» не будет, а вот ошибка перечитывания покажется пользователю —
  // иначе после обрезки он увидел бы пустую панель и не знал почему.
  const onClearAll = async () => {
    if (!window.confirm("Очистить логи проекта? Файлы логов будут обрезаны без возможности восстановить.")) {
      return;
    }
    setClearing(true);
    setError("");
    try {
      await clearProjectLogs(BASE, props.project);
      await load();
      // Обрезка меняет файл целиком — снимок сходится с буфером только по
      // заголовку очистки, поэтому прежние строки надо убрать явно.
      applied.current.clear();
      consumed.current.clear();
      snapAt.current.clear();
      rendered.current = "";
      setRows([]);
      jumpToBottom();
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setClearing(false);
    }
  };

  // Чем занят пустой контейнер: ошибка, первая загрузка, пустой каталог логов
  // или отсутствие совпадений фильтра.
  const emptyText = error
    ? "Не удалось загрузить логи: " + error
    : loading && !logs
      ? "Загружаю логи…"
      : !hasLogs
        ? "Лог-файлов в каталоге logs/ пока нет."
        : "Совпадений нет";

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
            <button
              className="ab danger"
              onClick={() => void onClearAll()}
              disabled={!hasLogs || clearing}
              title="Обрезать файлы логов на сервере (необратимо)"
            >
              <IconTrash />
              {clearing ? "Очистка…" : "Очистить"}
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

      {hasLogs && logs!.files.length > 1 && (
        <div className="lgtabs">
          {logs!.files.map((f) => (
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
          <span>
            файл: <code>{active.name}</code>
          </span>
          <span>{fmtSize(active.size)}</span>
          <span>изменён: {active.modified}</span>
        </div>
      )}

      {/* Ошибка действия (например, отказ копирования) — над логом: строки
          на месте, и прятать их под сообщение нельзя. Ошибку загрузки при
          пустом логе показывает сам контейнер ниже. */}
      {error && hasLogs && <p className="err">{error}</p>}

      {hasLogs && (
        <div className="log-tools">
          <label className="log-search">
            <IconSearch />
            <input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Найти в логах"
              aria-label="Найти в логах"
            />
            {!!query && <button type="button" onClick={() => setQuery("")} title="Очистить поиск">×</button>}
          </label>
          <div className="level-filters" aria-label="Фильтр уровня логов">
            {([ ["all", "Все"], ["warn", "Warn"], ["err", "Error"] ] as const).map(([value, label]) => (
              <button key={value} className={level === value ? "active" : ""} onClick={() => setLevel(value)}>
                {label}
              </button>
            ))}
          </div>
          <span className="line-count">
            {visibleRows.length}
            {visibleRows.length !== rows.length ? ` / ${rows.length}` : " строк"}
          </span>
        </div>
      )}

      {/* Контейнер лога НИКОГДА не размонтируется: переустановка обнуляла
          scrollTop, из-за чего любое фоновое обновление возвращало лог наверх. */}
      <div className="logbody" ref={bodyRef} onScroll={handleLogScroll}>
        {visibleRows.map((row) => {
          const kind = lineKind(row.text);
          return (
            <div
              key={row.id}
              className={"lg" + (kind ? " " + kind : "") + (row.fresh ? " fresh" : "")}
            >
              {row.text || "\u00a0"}
            </div>
          );
        })}
        {visibleRows.length === 0 && <div className={"log-empty" + (error ? " err" : "")}>{emptyText}</div>}
      </div>

      {hasLogs && (deferred || !following) && (
        <button className="log-follow" onClick={jumpToBottom}>
          <IconDown /> К новым строкам
        </button>
      )}
    </div>
  );
}

// hasSelectionIn — внутри контейнера есть непустое выделение текста.
function hasSelectionIn(el: HTMLElement): boolean {
  const sel = document.getSelection();
  return !!sel && !sel.isCollapsed && el.contains(sel.anchorNode);
}

// streamEnd — абсолютный индекс следующей строки потока за файлом.
function streamEnd(stream: LogStream | undefined): number {
  return stream ? stream.dropped + stream.lines.length : 0;
}

// appendRows — дописать строки в хвост буфера, при необходимости срезав верх
// до limit. Срез сверху безопасен только у прижатого хвоста, поэтому limit
// выбирает вызывающий (см. canTrim).
function appendRows(prev: Row[], texts: string[], limit: number, newRow: (t: string, fresh: boolean) => Row): Row[] {
  if (!texts.length) return prev;
  const add = texts.slice(Math.max(0, texts.length - limit)).map((t) => newRow(t, true));
  const room = Math.max(0, limit - add.length);
  return [...(prev.length > room ? prev.slice(prev.length - room) : prev), ...add];
}

// mergeRows — наложение ПОЛНОГО содержимого файла на буфер по общему префиксу
// (в отличие от appendRows, texts — весь файл, а не его хвост). Общий префикс
// сохраняет id и DOM-узлы; всё после первого расхождения (ротация файла,
// обрезка снапшота) заменяется целиком.
function mergeRows(
  prev: Row[],
  texts: string[],
  fresh: boolean,
  limit: number,
  newRow: (t: string, fresh: boolean) => Row,
): Row[] {
  const n = Math.min(prev.length, texts.length);
  let k = 0;
  while (k < n && prev[k]?.text === texts[k]) k++;
  const head = prev.slice(0, k);
  const add = texts.slice(Math.max(k, texts.length - limit)).map((t) => newRow(t, fresh));
  const room = Math.max(0, limit - add.length);
  return [...(head.length > room ? head.slice(head.length - room) : head), ...add];
}

// lineKind — суффикс класса строки лога по уровню: WARN/ERROR/FATAL
// подсвечиваются, строки с таймстампом приглушаются.
function lineKind(line: string): string {
  const t = line.trim();
  if (/^(?:ERROR|FATAL)\b/.test(t) || t.includes(" ERROR ") || t.includes(" FATAL ")) {
    return "err";
  }
  if (/^WARN\b/.test(t) || t.includes(" WARN ")) {
    return "warn";
  }
  if (/^\d{4}-\d{2}-\d{2}/.test(t)) {
    return "ts";
  }
  return "";
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

// Иконка «удалить» для кнопки очистки логов (action-bar).
function IconTrash() {
  return (
    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 6h18" />
      <path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2" />
      <path d="M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6" />
      <path d="M10 11v6M14 11v6" />
    </svg>
  );
}

function IconSearch() {
  return <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><circle cx="11" cy="11" r="7" /><path d="m20 20-4-4" /></svg>;
}

function IconDown() {
  return <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M12 4v15M6 13l6 6 6-6" /></svg>;
}
