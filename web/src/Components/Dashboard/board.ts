// Канбан-модель доски: статусы колонок, подписи и допустимые переходы.
// Статусы зеркалят board/entity.go (new -> analysis -> ready -> in_progress
// -> done; cancelled — терминальный, paused — «на паузе»). Переходы ограничены
// соседними статусами строго по серверному ValidateTransition.
import type { Status } from "@/Types";

// Порядок колонок на доске: рабочие статусы цепочки + терминальный + пауза.
export const STATUS_ORDER: Status[] = [
  "new",
  "analysis",
  "ready",
  "in_progress",
  "done",
  "cancelled",
  "paused",
];

export const STATUS_LABEL: Record<Status, string> = {
  new: "Новые",
  analysis: "В анализе",
  ready: "Готовы к работе",
  in_progress: "В работе",
  done: "Готово",
  cancelled: "Отменены",
  paused: "На паузе",
};

// Допустимые ручные переходы (только между соседними статусами, терминальные
// статусы — конечные точки). «На паузе» — не ручной статус задачи: паузу
// ставит и снимает эпик (кнопки «Пауза»/«Продолжить»), поэтому у paused-задачи
// переносов нет — она вернётся на прежнее место цепочки вместе с эпиком.
export const MOVES: Record<Status, { prev: Status | null; next: Status | null }> = {
  new: { prev: null, next: "analysis" },
  analysis: { prev: "new", next: "ready" },
  ready: { prev: "analysis", next: "in_progress" },
  in_progress: { prev: "ready", next: "done" },
  done: { prev: "in_progress", next: null },
  cancelled: { prev: null, next: null },
  paused: { prev: null, next: null },
};