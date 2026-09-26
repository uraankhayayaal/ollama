// Формат расхода токенов для доски (Ф-4
// PLAN-2026-09-19-done-epic-task-token.md). Общий модуль для строк эпиков,
// карточек задач и сводки в шапке: единый формат чисел и подписей «факт /
// прогноз» с ошибкой прогноза.

export type TokenUsage = {
  tokens_in?: number;
  tokens_out?: number;
  tokens_total?: number;
  token_estimate?: number;
};

// fmtTokens — компактное число токенов: 950 → «950», 12 345 → «12.3k»,
// 2 400 000 → «2.4M».
export function fmtTokens(n: number): string {
  const v = Math.round(n);
  if (!Number.isFinite(v) || v <= 0) return "0";
  if (v < 1000) return String(v);
  if (v < 1_000_000) {
    const k = v / 1000;
    return (k >= 100 ? Math.round(k) : Math.round(k * 10) / 10) + "k";
  }
  const m = v / 1_000_000;
  return (m >= 100 ? Math.round(m) : Math.round(m * 10) / 10) + "M";
}

// tokenLabel — подпись расхода одной единицы доски:
//   - факт есть, оценки нет  → «12.3k»;
//   - оценка есть, факта нет → «≈12.3k» (прогноз);
//   - оба есть              → «14k / ≈12.3k», у завершённых единиц с отклонением
//     добавляется ошибка прогноза: «14k / ≈12.3k (+14%)».
// Пусто (нет ни факта, ни прогноза) — пустая строка: подпись не рисуется.
export function tokenLabel(u: TokenUsage, done = false): string {
  const fact = u.tokens_total ?? 0;
  const est = u.token_estimate ?? 0;
  if (fact <= 0 && est <= 0) return "";
  if (fact <= 0) return "≈" + fmtTokens(est);
  if (est <= 0) return fmtTokens(fact);
  const pct = Math.round((Math.abs(fact - est) / est) * 100);
  const sign = fact > est ? "+" : "−";
  const err = done && pct > 0 ? ` (${sign}${pct}%)` : "";
  return `${fmtTokens(fact)} / ≈${fmtTokens(est)}${err}`;
}

// tokenTitle — подробная подсказка (title) с полными числами входа/выхода.
export function tokenTitle(u: TokenUsage): string {
  const parts: string[] = [];
  if ((u.tokens_total ?? 0) > 0) {
    parts.push(
      `Факт расхода: ${fmtTokens(u.tokens_in ?? 0)} вход + ${fmtTokens(u.tokens_out ?? 0)} выход = ${fmtTokens(u.tokens_total ?? 0)} токенов`,
    );
  }
  if ((u.token_estimate ?? 0) > 0) {
    parts.push(`Прогноз: ≈${fmtTokens(u.token_estimate ?? 0)} токенов (по истории завершённых единиц)`);
  }
  return parts.join(" · ");
}

// tokensSummary — сводка по доске: факт, сумма прогнозов и средняя ошибка
// прогноза в процентах (только по единицам, где есть и факт, и прогноз).
export function tokensSummary(units: TokenUsage[]): {
  spent: number;
  estimated: number;
  measured: number;
  errorPct: number | null;
} {
  let spent = 0;
  let estimated = 0;
  let measured = 0;
  let measuredSpent = 0;
  let measuredEstimated = 0;
  for (const u of units) {
    const f = u.tokens_total ?? 0;
    const e = u.token_estimate ?? 0;
    spent += f;
    estimated += e;
    if (f > 0 && e > 0) {
      measured++;
      measuredSpent += f;
      measuredEstimated += e;
    }
  }
  // Ошибка считается только по замеренным единицам (где есть и факт, и
  // прогноз) — иначе несчитанные прогнозы искажали бы её.
  const errorPct =
    measured > 0 && measuredEstimated > 0
      ? Math.round((Math.abs(measuredSpent - measuredEstimated) / measuredEstimated) * 100)
      : null;
  return { spent, estimated, measured, errorPct };
}
