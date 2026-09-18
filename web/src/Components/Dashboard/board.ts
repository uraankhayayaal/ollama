// Канбан-модель доски: статусы колонок, подписи и допустимые переходы.
// Статусы зеркалят board/entity.go (new -> analysis -> ready -> in_progress
// -> done; cancelled — терминальный). Переходы ограничены соседними
// статусами строго по серверному ValidateTransition.
import type { Status } from "@/Types";

// Порядок колонок на доске: рабочие статусы цепочки + терминальный.
export const STATUS_ORDER: Status[] = [
  "new",
  "analysis",
  "ready",
  "in_progress",
  "done",
  "cancelled",
];

export const STATUS_LABEL: Record<Status, string> = {
  new: "Новые",
  analysis: "В анализе",
  ready: "Готовы к работе",
  in_progress: "В работе",
  done: "Готово",
  cancelled: "Отменены",
};

// Допустимые ручные переходы (только между соседними статусами, терминальные
// статусы — конечные точки).
export const MOVES: Record<Status, { prev: Status | null; next: Status | null }> = {
  new: { prev: null, next: "analysis" },
  analysis: { prev: "new", next: "ready" },
  ready: { prev: "analysis", next: "in_progress" },
  in_progress: { prev: "ready", next: "done" },
  done: { prev: "in_progress", next: null },
  cancelled: { prev: null, next: null },
};