// Модальное окно с подробной информацией о задаче (клик по задаче — ячейка
// доски). Переходы статуса — рядом с эпиком в заголовке: минус/плюс.
import type { TaskRow } from "@/Types";
import { MOVES, STATUS_LABEL } from "../board";
import { Modal } from "../Modal";
import "./styles.scss";

export function TaskModal({
  task,
  onTaskUpdate,
  onClose,
}: {
  task: TaskRow;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onClose: () => void;
}) {
  const m = MOVES[task.status] ?? { prev: null, next: null };

  return (
    <Modal title={"Задача · " + task.task_id} onClose={onClose}>
      <dl className="details">
        <div>
          <dt>Название</dt>
          <dd className="name">{task.title}</dd>
        </div>
        <div>
          <dt>Эпик</dt>
          <dd>{task.epic_id || "—"}</dd>
        </div>
        <div>
          <dt>Статус</dt>
          <dd className={"status " + task.status}>{STATUS_LABEL[task.status] ?? task.status}</dd>
        </div>
        <div>
          <dt>Исполнитель</dt>
          <dd>{task.assignee || "—"}</dd>
        </div>
        <div>
          <dt>Порядок</dt>
          <dd>{task.sequence_order}</dd>
        </div>
        {(task.dependencies ?? []).length > 0 && (
          <div>
            <dt>Зависимости</dt>
            <dd>{task.dependencies.join(", ")}</dd>
          </div>
        )}
      </dl>

      <h4 className="section">Описание</h4>
      <p className="desc">{task.description || "—"}</p>

      <div className="actions">
        {m.prev && <button onClick={() => onTaskUpdate(task, { status: m.prev! })}>← {STATUS_LABEL[m.prev]}</button>}
        {m.next && <button onClick={() => onTaskUpdate(task, { status: m.next! })}>{STATUS_LABEL[m.next]} →</button>}
      </div>

      <div className="meta">
        создан {task.created_at} · обновлён {task.updated_at}
      </div>
    </Modal>
  );
}