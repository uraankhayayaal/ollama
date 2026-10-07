// Канбан-модель доски: статусы колонок, подписи и допустимые переходы.
// Статусы зеркалят board/entity.go (new -> analysis -> ready -> in_progress
// -> testing -> done; human_help — боковая «помощь человека», cancelled —
// терминальный). Переходы ограничены соседними статусами строго по серверному
// ValidateTransition.
import type { Status } from "@/Types";

// Порядок колонок на доске: цепочка Р-6 (ready -> human_help -> in_progress
// -> testing -> done) + терминальные cancelled.
export const STATUS_ORDER: Status[] = [
  "new",
  "analysis",
  "ready",
  "human_help",
  "in_progress",
  "testing",
  "done",
  "cancelled",
];

export const STATUS_LABEL: Record<Status, string> = {
  new: "Новые",
  analysis: "В анализе",
  ready: "Готовы к работе",
  human_help: "Помощь человека",
  in_progress: "В работе",
  testing: "На тестировании",
  done: "Готово",
  cancelled: "Отменены",
};

// Допустимые ручные переходы — списки prev/next (назад/вперёд по цепочке),
// зеркало серверного ValidateTransition без бокового cancelled (отмена —
// кнопкой эпика, как и раньше). human_help — боковая ветка: вход из любого
// нетерминального статуса, выход только обратно в работу. Терминальные
// статусы (done, cancelled) — конечные точки, переносов нет.
// Порядок в списках — chain-first: основной сосед цепочки первым.
export const MOVES: Record<Status, { prev: Status[]; next: Status[] }> = {
  new: { prev: [], next: ["analysis", "human_help"] },
  analysis: { prev: [], next: ["ready", "human_help"] },
  ready: { prev: [], next: ["in_progress", "human_help"] },
  human_help: { prev: ["ready"], next: ["in_progress"] },
  in_progress: { prev: ["ready", "human_help"], next: ["testing", "done"] },
  testing: { prev: ["in_progress", "human_help"], next: ["done"] },
  done: { prev: [], next: [] },
  cancelled: { prev: [], next: [] },
};
