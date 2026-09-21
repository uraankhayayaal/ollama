// Модальное окно с подробной информацией о задаче (клик по задаче — ячейка
// доски). Переходы статуса — рядом с эпиком в заголовке: минус/плюс.
// Ф-5: блок «ветка + MR» — ветка/MR, кнопки «Создать ветку задачи» (от ветки
// эпика) и «Создать MR» (push → MR в ветку эпика).
import type { GitView, TaskRow } from "@/Types";
import { MOVES, STATUS_LABEL } from "../board";
import { Modal } from "../Modal";
import { GitBlock } from "../GitBlock";
import "./styles.scss";

export function TaskModal({
  task,
  git,
  onCreateBranch,
  onCreateMR,
  onTaskUpdate,
  onClose,
}: {
  task: TaskRow;
  git?: GitView;
  onCreateBranch?: (t: TaskRow) => Promise<void>;
  onCreateMR?: (t: TaskRow) => Promise<void>;
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

      {git && (
        <GitBlock
          git={git}
          kind="task"
          id={task.task_id}
          epicId={task.epic_id}
          onCreateBranch={onCreateBranch ? () => onCreateBranch(task) : undefined}
          onCreateMR={onCreateMR ? () => onCreateMR(task) : undefined}
        />
      )}

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