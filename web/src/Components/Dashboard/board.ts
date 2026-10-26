// Канбан-модель доски: статусы колонок, подписи и допустимые переходы.
// Статусы зеркалят domain/status.go.
// Эпики: draft -> business_review -> system_analysis -> done
// Задачи: draft -> infra -> test_cases -> development -> done
// Общие терминалы: done, cancelled
// Переходы ограничены соседними статусами строго по серверному ValidateTransition.
import type { Status } from "@/Types";

// Порядок колонок на доске для задач (основная сетка).
export const TASK_STATUS_ORDER: Status[] = [
  "draft",
  "infra",
  "test_cases",
  "development",
  "done",
  "cancelled",
];

// Порядок колонок для эпиков (заголовок строки).
export const EPIC_STATUS_ORDER: Status[] = [
  "draft",
  "business_review",
  "system_analysis",
  "done",
  "cancelled",
];

export const STATUS_LABEL: Record<Status, string> = {
  draft: "Черновик",
  business_review: "Бизнес-ревью",
  system_analysis: "Системный анализ",
  infra: "Инфраструктура",
  test_cases: "Тесты",
  development: "Разработка",
  done: "Готово",
  cancelled: "Отменены",
};

// Допустимые ручные переходы задач (строго линейно, без возвратов).
// Отмена доступна из любого нетерминального статуса.
export const TASK_MOVES: Record<Status, { prev: Status[]; next: Status[] }> = {
  draft: { prev: [], next: ["infra", "cancelled"] },
  infra: { prev: [], next: ["test_cases", "cancelled"] },
  test_cases: { prev: [], next: ["development", "cancelled"] },
  development: { prev: [], next: ["done", "cancelled"] },
  done: { prev: [], next: [] },
  cancelled: { prev: [], next: [] },
  // Epic statuses — not used for task transitions
  business_review: { prev: [], next: [] },
  system_analysis: { prev: [], next: [] },
};

// Допустимые ручные переходы эпиков.
// business_review может вернуться в draft (единственный переход назад).
export const EPIC_MOVES: Record<Status, { prev: Status[]; next: Status[] }> = {
  draft: { prev: [], next: ["business_review", "cancelled"] },
  business_review: { prev: ["draft"], next: ["system_analysis", "cancelled"] },
  system_analysis: { prev: [], next: ["done", "cancelled"] },
  done: { prev: [], next: [] },
  cancelled: { prev: [], next: [] },
  // Task statuses — not used for epic transitions
  infra: { prev: [], next: [] },
  test_cases: { prev: [], next: [] },
  development: { prev: [], next: [] },
};
