// Карточка задачи: drag&drop, ручные переходы по соседним статусам (←/→).
// Клик по карточке → модалка с подробностями; кнопки управления помечают
// событие, чтобы клик не всплывал.
import type { TaskRow } from "@/Types";
import { MOVES } from "../board";
import { tokenLabel, tokenTitle } from "../tokens";
import { setDragID } from "../dnd";
import "./styles.scss";

export function TaskCard({
  task,
  onTaskUpdate,
  onOpen,
}: {
  task: TaskRow;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onOpen: () => void;
}) {
  const m = MOVES[task.status] ?? { prev: null, next: null };

  return (
    <li
      className="task"
      draggable
      onDragStart={() => setDragID(task.task_id)}
      onClick={onOpen}
    >
      <div className="title">{task.title}</div>
      {/* Ф-4: расход токенов задачи — тот же формат, что у эпика. */}
      {tokenLabel(task, task.status === "done") && (
        <div className="tokens" title={tokenTitle(task)}>
          {tokenLabel(task, task.status === "done")}
        </div>
      )}
      <div className="controls">
        {m.prev && (
          <button
            onClick={(e) => {
              e.stopPropagation();
              onTaskUpdate(task, { status: m.prev! });
            }}
          >
            ←
          </button>
        )}
        {m.next && (
          <button
            onClick={(e) => {
              e.stopPropagation();
              onTaskUpdate(task, { status: m.next! });
            }}
          >
            →
          </button>
        )}
      </div>
    </li>
  );
}