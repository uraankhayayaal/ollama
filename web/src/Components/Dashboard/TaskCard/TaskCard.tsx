// Карточка задачи: drag&drop, ручные переходы по соседним статусам (←/→).
// Клик по карточке → модалка с подробностями; кнопки управления помечают
// событие, чтобы клик не всплывал.
import type { Status, TaskRow } from "@/Types";
import { TASK_MOVES, STATUS_LABEL } from "../board";
import { tokenLabel, tokenTitle } from "../tokens";
import { setDragID } from "../dnd";
import "./styles.scss";

export function TaskCard({
  task,
  onTaskUpdate,
  onOpen,
  onDelete,
}: {
  task: TaskRow;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onOpen: () => void;
  onDelete?: (t: TaskRow) => void;
}) {
  const m = TASK_MOVES[task.status] ?? { prev: [], next: [] };
  // Удаление: draft, infra — ещё не взяты в работу; done — завершённую можно убрать.
  const canDelete = onDelete && (task.status === "draft" || task.status === "infra" || task.status === "done");
  // На узкой карточке кнопки-стрелки без подписей — цель подсказкой (title).
  const moveBtn = (s: Status, arrow: string) => (
    <button
      key={s}
      title={STATUS_LABEL[s]}
      onClick={(e) => {
        e.stopPropagation();
        onTaskUpdate(task, { status: s });
      }}
    >
      {arrow}
    </button>
  );

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
        {m.prev.map((s) => moveBtn(s, "←"))}
        {m.next.map((s) => moveBtn(s, "→"))}
        {canDelete && (
          <button
            className="task-delete"
            title="Удалить задачу"
            onClick={(e) => {
              e.stopPropagation();
              onDelete!(task);
            }}
          >
            ×
          </button>
        )}
      </div>
    </li>
  );
}