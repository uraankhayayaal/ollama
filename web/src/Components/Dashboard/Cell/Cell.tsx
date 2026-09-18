// Ячейка доски (эпик × статус): зона drop для карточек. Допустим перенос
// только в соседние статусы (ValidateTransition) и только в пределах своего
// эпика — epic_id у задачи неизменен. В свёрнутом режиме вместо карточек
// показывает интерактивный счётчик задач статуса.
import type { Status, TaskRow } from "@/Types";
import { MOVES } from "../board";
import { clearDragID, getDragID } from "../dnd";
import { TaskCard } from "../TaskCard";
import "./styles.scss";

export function Cell({
  status,
  epicId,
  tasks,
  collapsed = false,
  onTaskUpdate,
  onTaskOpen,
}: {
  status: Status;
  epicId: string;
  tasks: TaskRow[];
  collapsed?: boolean;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onTaskOpen: (t: TaskRow) => void;
}) {
  const items = tasks
    .filter((t) => t.status === status)
    .sort((a, b) => a.sequence_order - b.sequence_order);

  const onDrop = () => {
    const dragID = getDragID();
    clearDragID();
    if (!dragID) {
      return;
    }
    const t = tasks.find((x) => x.task_id === dragID);
    if (!t || t.epic_id !== epicId || t.status === status) {
      return;
    }
    const m = MOVES[t.status];
    if (m.prev !== status && m.next !== status) {
      return;
    }
    onTaskUpdate(t, { status });
  };

  return (
    <div
      className={"cell " + status + (collapsed ? " collapsed" : "")}
      onDragOver={(e) => e.preventDefault()}
      onDrop={onDrop}
    >
      {collapsed ? (
        <div className={"cell-count" + (items.length > 0 ? " has" : "")}>
          {items.length}
        </div>
      ) : (
        <ul>
          {items.map((t) => (
            <TaskCard
              key={t.task_id}
              task={t}
              onTaskUpdate={onTaskUpdate}
              onOpen={() => onTaskOpen(t)}
            />
          ))}
        </ul>
      )}
    </div>
  );
}