// Быстрые действия эпика под названием на доске. Сейчас это удаление эпика:
// кнопка видна только если ни одна задача эпика ещё не взята в работу
// специалистом (статусы new/analysis/ready).
import type { TaskRow } from "@/Types";
import "./styles.scss";

export function EpicActionBar({
  tasks,
  onDelete,
}: {
  tasks: TaskRow[];
  onDelete: () => void;
}) {
  const canDelete = tasks.every(
    (t) =>
      t.status !== "in_progress" &&
      t.status !== "done" &&
      t.status !== "cancelled",
  );
  if (!canDelete) {
    return null;
  }
  return (
    <div className="epic-actions">
      <button
        className="epic-delete"
        onClick={(e) => {
          e.stopPropagation();
          onDelete();
        }}
        title="Удалить эпик вместе с задачами (нельзя, если задачи уже взяты в работу)"
        aria-label={"Удалить эпик"}
      >
        Удалить
      </button>
    </div>
  );
}