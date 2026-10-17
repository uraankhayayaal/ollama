// Канбан-модель доски: статусы колонок, подписи и допустимые переходы.
// Статусы зеркалят board/entity.go (new -> analysis -> ready -> in_progress
// -> testing -> done; human_help — боковая «помощь человека», cancelled —
// терминальный). Переходы ограничены соседними статусами строго по серверному
// ValidateTransition.
import type { Status } from "@/Types";

// Порядок колонок на доске: цепочка Р-6 (human_help -> ready -> in_progress
// -> testing -> done) + терминальные cancelled.
export const STATUS_ORDER: Status[] = [
  "new",
  "analysis",
  "human_help",
  "ready",
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
  analysis: { prev: [], next: ["human_help", "ready"] },
  human_help: { prev: ["analysis"], next: ["ready", "in_progress"] },
  ready: { prev: ["human_help"], next: ["in_progress"] },
  in_progress: { prev: ["ready", "human_help"], next: ["testing", "done"] },
  testing: { prev: ["in_progress", "human_help"], next: ["done"] },
  done: { prev: [], next: [] },
  cancelled: { prev: [], next: [] },
};

// Фаза эпика (зеркало board/order.go EpicPhase): перетаскивание строк доски
// разрешено только внутри одной фазы — фаза остаётся первичным ключом очереди
// лидов (инфраструктура → приложение → тестирование).
export function epicPhase(e: { assigned_role?: string }): "infra" | "app" | "test" {
  const role = (e.assigned_role ?? "").toLowerCase();
  const has = (...markers: string[]) => markers.some((m) => role.includes(m));
  if (has("qa", "тест", "testing")) return "test";
  if (has("devops", "инфра", "infra", "sre", "docker", "k8s", "ci")) return "infra";
  return "app";
}
