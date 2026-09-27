// Metricsboard — панель «Метрики»: наблюдаемость и стоимость агентского цикла
// (Ф-1 PLAN-2026-09-19-todo-owerview-for-prom.md).
//
// Данные — снимок единого реестра телеметрии (GET /api/projects/<name>/metrics):
// сколько вызовов инструментов было, сколько упало, сколько заняли по времени,
// сколько токенов и денег ушло на текущий запуск и на проект в целом. Рядом с
// панелью — гистограмма длительности вызовов инструментов (корзины приходят из
// снимка и совпадают с экспортом Prometheus), список единиц работы с их
// расходом и именованные метрики шагов.
//
// Панель опрашивает REST раз в 5 секунд, пока открыта: WS-события несут дельты
// ответа модели, но не агрегаты, а пересчёт агрегатов на клиенте разошёлся бы
// с тем, что видит /metrics.

import { useCallback, useEffect, useRef, useState } from "react";
import { projectMetrics, resetProjectMetrics } from "@/Api";
import type { MetricsTool, ProjectMetrics } from "@/Types";
import "./styles.scss";

export interface MetricsboardProps {
  project: string;
  // Приращение счётчика, по которому понимаем, что цикл идёт: панель сама
  // перезапрашивает метрики по таймеру, а смена проекта должна перезапросить
  // их сразу.
  refreshKey?: number;
}

const BASE = "";
// Период опроса: чаще — лишняя нагрузка на сервер, реже — метрики «застывают».
const POLL_MS = 5000;

function fmt(n: number): string {
  return Math.round(n).toLocaleString("ru-RU");
}

function fmtSec(v: number): string {
  if (!Number.isFinite(v) || v <= 0) return "—";
  if (v < 1) return `${Math.round(v * 1000)} мс`;
  if (v < 60) return `${v.toFixed(1)} с`;
  return `${Math.floor(v / 60)} мин ${Math.round(v % 60)} с`;
}

function fmtUptime(v: number): string {
  const total = Math.max(0, Math.floor(v));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h} ч ${m} мин`;
  if (m > 0) return `${m} мин ${s} с`;
  return `${s} с`;
}

// money печатает денежную оценку. Без заданных цен (LLM_PRICE_IN/OUT) сервер
// отдаёт cost_known=false — тогда показываем прочерк, а не «0.000»: нулевая
// цена и неизвестная цена — разные вещи, и путать их нельзя.
function money(v: number, known: boolean, currency: string): string {
  if (!known) return "—";
  const digits = v !== 0 && Math.abs(v) < 0.01 ? 4 : 2;
  return `${v.toFixed(digits)} ${currency || "у.е."}`;
}

export function Metricsboard(props: MetricsboardProps) {
  const [data, setData] = useState<ProjectMetrics | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [live, setLive] = useState(false);
  const projectRef = useRef(props.project);
  projectRef.current = props.project;

  const load = useCallback(async () => {
    const want = projectRef.current;
    try {
      const m = await projectMetrics(BASE, want);
      // Ответ на закрытый проект не должен перетирать текущий снимок.
      if (want !== projectRef.current) return;
      setData(m);
      setError("");
    } catch (e) {
      if (want !== projectRef.current) return;
      setError(String(e));
    }
  }, []);

  useEffect(() => {
    setData(null);
    setError("");
    void load();
  }, [props.project, props.refreshKey, load]);

  useEffect(() => {
    if (!live) return;
    const t = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(t);
  }, [live, load]);

  if (error && !data) {
    return (
      <div className="mb">
        <div className="mb-head">
          <span className="mb-title">Метрики</span>
        </div>
        <div className="mb-body">
          <p className="mb-err">Метрики недоступны: {error}</p>
        </div>
      </div>
    );
  }

  return (
    <div className="mb">
      <div className="mb-head">
        <span className="mb-title">Метрики запуска</span>
        <span className="mb-actions">
          <button
            className={"btn small" + (live ? " on" : "")}
            onClick={() => setLive((v) => !v)}
            title="Опрашивать метрики каждые 5 секунд"
          >
            {live ? "Live" : "Обновить разово"}
          </button>
          <button
            className="btn small"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              void resetProjectMetrics(BASE, props.project)
                .then(() => load())
                .catch((e) => setError(String(e)))
                .finally(() => setBusy(false));
            }}
            title="Обнулить метрики текущего запуска (накопленные токены сохранятся)"
          >
            Сбросить
          </button>
        </span>
      </div>

      <div className="mb-body">
        {!data && <p className="mb-note">Загружаю метрики…</p>}
        {data && (
          <>
            <div className="mb-cards">
              <Card
                title="Токены запуска"
                value={fmt(data.tokens_total)}
                hint={`вх. ${fmt(data.tokens_in)} · вых. ${fmt(data.tokens_out)} · раундов ${fmt(data.rounds)}`}
              />
              <Card
                title="Стоимость запуска"
                value={money(data.cost, data.cost_known, data.currency)}
                hint={
                  data.cost_known
                    ? `вх. ${money(data.cost_in ?? 0, true, data.currency)} · вых. ${money(data.cost_out ?? 0, true, data.currency)}`
                    : "цены не заданы (LLM_PRICE_IN/OUT)"
                }
              />
              <Card
                title="Всего по проекту"
                value={fmt(data.total_tokens_in + data.total_tokens_out)}
                hint={`вх. ${fmt(data.total_tokens_in)} · вых. ${fmt(data.total_tokens_out)}`}
              />
              <Card
                title="Стоимость по проекту"
                value={money(data.total_cost, data.total_cost_known, data.currency)}
                hint="за все перезапуски сервера"
              />
              <Card
                title="Инструменты"
                value={fmt(data.tool_calls)}
                hint={`ошибок ${fmt(data.tool_errors)} · раундов ${fmt(data.rounds)}`}
              />
              <Card
                title="В работе"
                value={fmtUptime(data.uptime_sec)}
                hint={data.tps > 0 ? `генерация ${data.tps.toFixed(1)} ток/с` : "скорость неизвестна"}
              />
            </div>

            {data.app_log_lines > 0 && (
              <Section title="Логи рантайма">
                <p className="mb-note">
                  Прочитано строк логов запущенного приложения:{" "}
                  <b>{fmt(data.app_log_lines)}</b>
                </p>
              </Section>
            )}

            <Section title="Инструменты по времени">
              {data.tools.length === 0 ? (
                <p className="mb-note">Вызовов инструментов ещё не было.</p>
              ) : (
                <table className="mb-table">
                  <thead>
                    <tr>
                      <th>Инструмент</th>
                      <th>Вызовы</th>
                      <th>Ошибки</th>
                      <th>Среднее</th>
                      <th>Макс.</th>
                      <th className="mb-hist">Распределение длительности</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.tools.map((t) => (
                      <ToolRow
                        key={t.tool}
                        t={t}
                        buckets={data.duration_buckets}
                      />
                    ))}
                  </tbody>
                </table>
              )}
            </Section>

            <Section title="Единицы работы">
              {data.scopes.length === 0 ? (
                <p className="mb-note">Работа по задачам и эпикам ещё не началась.</p>
              ) : (
                <table className="mb-table">
                  <thead>
                    <tr>
                      <th>Единица</th>
                      <th>Вызовы</th>
                      <th>Ошибки</th>
                      <th>Токены</th>
                      <th>Время</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.scopes.map((s) => (
                      <tr key={s.scope}>
                        <td className="mb-scope">{s.scope}</td>
                        <td>{fmt(s.calls)}</td>
                        <td className={s.errors > 0 ? "mb-bad" : ""}>{fmt(s.errors)}</td>
                        <td>
                          {fmt(s.tokens_in + s.tokens_out)}
                          <span className="mb-dim"> ({fmt(s.rounds)} р.)</span>
                        </td>
                        <td>{fmtSec(s.total_sec)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Section>

            {data.agents.length > 0 && (
              <Section title="Агенты">
                <table className="mb-table">
                  <thead>
                    <tr>
                      <th>Агент</th>
                      <th>Вызовы</th>
                      <th>Ошибки</th>
                      <th>Токены</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.agents.map((a) => (
                      <tr key={a.agent}>
                        <td className="mb-scope">{a.agent}</td>
                        <td>{fmt(a.calls)}</td>
                        <td className={a.errors > 0 ? "mb-bad" : ""}>{fmt(a.errors)}</td>
                        <td>{fmt(a.tokens_in + a.tokens_out)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Section>
            )}

            {data.steps.length > 0 && (
              <Section title="Метрики шагов">
                <table className="mb-table">
                  <thead>
                    <tr>
                      <th>Шаг</th>
                      <th>Метрика</th>
                      <th>Значение</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.steps.map((s, i) => (
                      <tr key={`${s.step}/${s.key}/${i}`}>
                        <td className="mb-scope">
                          {s.step}
                          {s.scope ? <span className="mb-dim"> · {s.scope}</span> : null}
                        </td>
                        <td>{s.key}</td>
                        <td>{fmt(s.value)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Section>
            )}

            {(data.unpaired > 0 || data.overflow > 0) && (
              <p className="mb-note">
                {data.unpaired > 0 &&
                  `Результатов инструментов без парного начала: ${fmt(data.unpaired)} (потерянные или повторно проигранные старты). `}
                {data.overflow > 0 &&
                  `Единиц работы схлопнуто в «other»: ${fmt(data.overflow)} (лимит кардинальности рядов).`}
              </p>
            )}

            <p className="mb-note mb-export">
              Полный экспорт в формате Prometheus:{" "}
              <code>/api/metrics</code>
            </p>
          </>
        )}
      </div>
    </div>
  );
}

function Card(props: { title: string; value: string; hint: string }) {
  return (
    <div className="mb-card">
      <div className="mb-card-title">{props.title}</div>
      <div className="mb-card-value">{props.value}</div>
      <div className="mb-card-hint">{props.hint}</div>
    </div>
  );
}

function Section(props: { title: string; children: React.ReactNode }) {
  return (
    <div className="mb-section">
      <div className="mb-section-title">{props.title}</div>
      {props.children}
    </div>
  );
}

// ToolRow — строка инструмента с гистограммой длительности. Гистограмма
// рисуется делением: ширина полосы — доля вызовов не длиннее соответствующей
// границы. Самая частая корзина подсвечивается, чтобы «зависшие» вызовы
// (последняя корзина) было видно глазом.
function ToolRow(props: { t: MetricsTool; buckets: number[] }) {
  const { t, buckets } = props;
  const total = Math.max(1, t.count);
  // Корзины кумулятивные (buckets[i] — «не длиннее границы i»), поэтому
  // отдельный столбец получается разностью соседних. Пустые корзины (инструмент
  // ни разу не вызывался) дают delta 0 и просто не рисуются.
  const delta = (i: number): number => {
    if (i > 0) {
      return Math.max(0, (t.buckets[i] ?? 0) - (t.buckets[i - 1] ?? 0));
    }
    return t.buckets[0] ?? 0;
  };
  let best = 0;
  for (let i = 0; i < buckets.length; i++) {
    if (delta(i) > delta(best)) best = i;
  }
  return (
    <tr>
      <td className="mb-scope">{t.tool}</td>
      <td>{fmt(t.calls)}</td>
      <td className={t.errors > 0 ? "mb-bad" : ""}>{fmt(t.errors)}</td>
      <td>{fmtSec(t.mean_sec)}</td>
      <td>{fmtSec(t.max_sec)}</td>
      <td className="mb-hist">
        <span className="mb-bar" title={`${fmt(t.count)} вызовов, среднее ${fmtSec(t.mean_sec)}`}>
          {buckets.map((_b, i) => {
            const n = delta(i);
            if (n <= 0) return null;
            return (
              <i
                key={i}
                className={"mb-seg" + (i === best ? " best" : "")}
                style={{ width: `${(n / total) * 100}%` }}
                title={`≤ ${buckets[i]} с: ${fmt(n)} вызовов`}
              />
            );
          })}
        </span>
        <span className="mb-dim">
          ≤{buckets[0] ?? 0}с…≤{buckets[buckets.length - 1] ?? 0}с
        </span>
      </td>
    </tr>
  );
}
