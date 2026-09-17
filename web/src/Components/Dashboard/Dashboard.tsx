// Полный контроль доски: DnD между колонками, ручные статусы (←/→),
// inline-редактирование assignee. Данные — BoardView (зеркалит
// server/session.go boardSnapshot). Статусы колонок совпадают с
// board.entity.go: new -> analysis -> ready -> in_progress -> done.
// Общая концепция HITL-затворов (одобрение эпиков/задач) — в GateBanner.

import { useState } from "react";
import type { BoardView, Status, TaskRow } from "@/Types";
import "./styles.scss";

// Колонки доски: рабочие статусы цепочки + терминальные (cancelled).
const STATUS_ORDER: Status[] = [
  "new",
  "analysis",
  "ready",
  "in_progress",
  "done",
  "cancelled",
];

const STATUS_LABEL: Record<Status, string> = {
  new: "Новые",
  analysis: "В анализе",
  ready: "Готовы к работе",
  in_progress: "В работе",
  done: "Готово",
  cancelled: "Отменены",
};

// Допустимые ручные переходы (строго по ValidateTransition: только между
// соседними статусами, терминальные — конечные).
const MOVES: Record<Status, { prev: Status | null; next: Status | null }> = {
  new: { prev: null, next: "analysis" },
  analysis: { prev: "new", next: "ready" },
  ready: { prev: "analysis", next: "in_progress" },
  in_progress: { prev: "ready", next: "done" },
  done: { prev: "in_progress", next: null },
  cancelled: { prev: null, next: null },
};

let dragID: string | null = null;

export function Dashboard({
  board,
  onTaskUpdate,
}: {
  board: BoardView | null;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
}) {
  if (!board) {
    return <div className="dashboard">Совет ещё не загружен…</div>;
  }

  return (
    <div className="dashboard" onDragOver={(e) => e.preventDefault()}>
      <div className="banner">
        <span className="project">{board.meta?.project_name ?? "—"}</span>
        <span className="counts">
          эпиков {board.epics.length} · задач {board.tasks.length} · багов {board.bugs.length}
          {board.total && board.total.tasks > board.tasks.length && (
            <em> (всего задач: {board.total.tasks}, доска ограничена)</em>
          )}
        </span>
      </div>

      <div className="columns">
        {STATUS_ORDER.map((status) => (
          <Column
            key={status}
            status={status}
            tasks={board.tasks}
            onTaskUpdate={onTaskUpdate}
          />
        ))}
      </div>

      <section className="epics">
        <h3>Эпики</h3>
        <ul>
          {board.epics.map((e) => (
            <li key={e.task_id} className="epic">
              <div className="head">
                <span className="id">{e.task_id}</span>
                <strong className="title">{e.title}</strong>
                <span className={"status " + e.status}>{STATUS_LABEL[e.status] ?? e.status}</span>
              </div>
              {e.assigned_lead && <div className="lead">Лид: {e.assigned_lead}</div>}
              {e.description && <p className="desc">{e.description}</p>}
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

  const onDrop = () => {
    if (!dragID) {
      return;
    }
    const t = tasks.find((x) => x.task_id === dragID);
    dragID = null;
    if (!t || t.status === status) {
      return;
    }
    // Только соседние статусы: нарушение переходов сервер отклонит 400.
    const m = MOVES[t.status];
    if (m.prev !== status && m.next !== status) {
      return;
    }
    onTaskUpdate(t, { status });
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
  const m = MOVES[task.status] ?? { prev: null, next: null };
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
        {m.prev && <button onClick={() => m.prev && onTaskUpdate(task, { status: m.prev })}>←</button>}
        {m.next && <button onClick={() => m.next && onTaskUpdate(task, { status: m.next })}>→</button>}
      </div>
    </li>
  );
}
