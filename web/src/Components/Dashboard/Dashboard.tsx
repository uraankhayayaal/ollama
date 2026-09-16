// Полный контроль доски: DnD между колонками, ручные статусы (←/→),
// inline-редактирование assignee, approve/reject эпиков (HITL-затвор).
// Данные — BoardView (зеркалит server/session.go boardSnapshot).

import { useState } from "react";
import type { BoardView, Status, TaskRow } from "@/Types";
import "./styles.scss";

const STATUS_ORDER: Status[] = ["todo", "in_progress", "review", "done"];
const STATUS_LABEL: Record<Status, string> = {
  todo: "К залу",
  in_progress: "В работе",
  review: "Проверка",
  done: "Готово",
};

let dragID: string | null = null;

export function Dashboard({
  board,
  onTaskUpdate,
  onGateDecide,
}: {
  board: BoardView | null;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onGateDecide: (gate: "epics" | "tasks", d: { approved: boolean; reason?: string }) => void;
}) {
  if (!board) {
    return <div className="dashboard">Совет ещё не загружен…</div>;
  }

  const move = (task: TaskRow, status: Status) => {
    if (task.status !== status) {
      onTaskUpdate(task, { status });
    }
  };

  return (
    <div className="dashboard" onDragOver={(e) => e.preventDefault()}>
      <div className="banner">
        <span className="project">{board.meta?.project_name ?? "—"}</span>
        <span className="counts">
          эпиков {board.epics.length} · задач {board.tasks.length} · багов {board.bugs.length}
        </span>
      </div>

      <div className="columns">
        {STATUS_ORDER.map((status) => (
          <Column
            key={status}
            status={status}
            tasks={board.tasks}
            onTaskUpdate={onTaskUpdate}
            setTaskStatus={(t, s) => move(t, s)}
          />
        ))}
      </div>

      <section className="epics">
        <h3>Эпики</h3>
        <ul>
          {board.epics.map((e) => (
            <li key={e.task_id}>
              <strong>{e.title}</strong>
              <span className={e.status}>{STATUS_LABEL[e.status]}</span>
              {e.status === "review" && (
                <button onClick={() => onGateDecide("epics", { approved: true })}>✓ принять</button>
              )}
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
  setTaskStatus,
}: {
  status: Status;
  tasks: TaskRow[];
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  setTaskStatus: (t: TaskRow, s: Status) => void;
}) {
  const items = tasks.filter((t) => t.status === status).sort((a, b) => a.order - b.order);

  const onDrop = () => {
    if (!dragID) {
      return;
    }
    const t = tasks.find((x) => x.task_id === dragID);
    dragID = null;
    if (t && t.status !== status) {
      onTaskUpdate(t, { status });
    }
  };

  return (
    <div
      className={"col " + status}
      onDragOver={(e) => e.preventDefault()}
      onDrop={onDrop}
    >
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
  const idx = STATUS_ORDER.indexOf(task.status);
  const prev: Status | null = STATUS_ORDER[idx - 1] ?? null;
  const next: Status | null = STATUS_ORDER[idx + 1] ?? nullRate;
  const [editing, setEditing] = useState(false);
  const [assignee, setAssignee] = useState(task.assignee ?? "");

  const save = () => {
    setEditing(false);
    if (assignee !== (task.assignee ?? "")) {
      onTaskUpdate(task, { assignee });
    }
  };

  return (
    <li
      className="task"
      draggable
      onDragStart={() => (dragID = task.task_id)}
    >
      <div className="title">{task.title}</div>
      <div className="sub">
        <span className="epic">{task.epic_id}</span>
        {editing ? (
          <input
            className="assignee-input"
            autoFocus
            value={assignee}
            onChange={(e) => setAssignee(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && save()}
            onBlur={save}
          />
        ) : (
          <button className="assignee" onClick={() => setEditing(true)}>
            {task.assignee || "— + исполнитель"}
          </button>
        )}
      </div>
      <div className="controls">
        {prev && <button onClick={() => onTaskUpdate(task, { status: prev })}>←</button>}
        {next && <button onClick={() => onTaskUpdate(task, { status: next })}>→</button>}
      </div>
    </li>
  );
}
