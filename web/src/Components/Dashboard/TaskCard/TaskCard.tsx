// Карточка задачи: drag&drop, ручные переходы по соседним статусам (←/→).
// Клик по карточке → модалка с подробностями; кнопки управления помечают
// событие, чтобы клик не всплывал.
import type { Status, TaskRow } from "@/Types";
import { MOVES, STATUS_LABEL } from "../board";
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
  const m = MOVES[task.status] ?? { prev: [], next: [] };
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
      </div>
    </li>
  );
}