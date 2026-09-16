import type { BoardView, Status, TaskRow } from "@/Types";
import "./styles.scss";

const STATUS_ORDER: Status[] = ["todo", "in_progress", "review", "done"];

const STATUS_LABEL: Record<Status, string> = {
  todo: "К залу",
  in_progress: "В работе",
  review: "Проверка",
  done: "Готово",
};

export function Dashboard({
  board,
  onTaskUpdate,
}: {
  board: BoardView | null;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
}) {
  if (!board) {
    return <div className="dashboard empty">Совет ещё не загружен…</div>;
  }

  return (
    <div className="dashboard">
      <div className="banner meta">
        <span className="project">{board.meta?.project_name ?? "—"}</span>
        <span className="counts">
          эпиков {board.epics.length} · задач {board.tasks.length} · багов {board.bugs.length}
        </span>
      </div>

      <div className="columns">
        {STATUS_ORDER.map((status) => (
          <Column key={status} status={status} tasks={board.tasks} onTaskUpdate={onTaskUpdate} />
        ))}
      </div>

      <section className="epics">
        <h3>Эпики</h3>
        <ul>
          {board.epics.map((e) => (
            <li key={e.task_id}>
              <strong>{e.title}</strong>
              <span className={e.status}>{STATUS_LABEL[e.status]}</span>
              <span className="lead">{e.assigned_lead}</span>
            </li>
          ))}
        </ul>
      </section>
    </div>
  );
}

function Column({
  status,
  tasks,
  onTaskUpdate,
}: {
  status: Status;
  tasks: TaskRow[];
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
}) {
  const items = tasks.filter((t) => t.status === status).sort((a, b) => a.order - b.order);
  return (
    <div className={"col " + status}>
      <header>
        {STATUS_LABEL[status]} <b>{items.length}</b>
      </header>
      <ul>
        {items.map((t) => (
          <TaskCard key={t.task_id} task={t} onTaskUpdate={onTaskUpdate} />
        ))}
      </ul>
    </div>
  );
}

function TaskCard({
  task,
  onTaskUpdate,
}: {
  task: TaskRow;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
}) {
  const next: Status | null =
    STATUS_ORDER[STATUS_ORDER.indexOf(task.status) + 1] ?? null;
  return (
    <li className="task">
      <div className="title">{task.title}</div>
      <div className="sub">
        <span className="epic">{task.epic_id}</span>
        <span className="as">{task.assignee || "—"}</span>
      </div>
      <div className="controls">
        {next && (
          <button onClick={() => onTaskUpdate(task, { status: next })}>→ {STATUS_LABEL[next]}</button>
        )}
      </div>
    </li>
  );
}
