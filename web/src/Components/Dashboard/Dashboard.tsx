// Матрица доски: строки = эпики, колонки = статусы, ячейки = задачи.
// Общий заголовок колонок, у каждой строки — эпик с названием эпика
// (клик → модалка). Клик по задаче → модалка задачи. DnD переносит задачи
// между соседними статусами внутри своего эпика.

import { useState } from "react";
import type { BoardView, EpicRow, TaskRow } from "@/Types";
import { STATUS_LABEL, STATUS_ORDER } from "./board";
import { EpicRow as EpicRowView } from "./EpicRow";
import { EpicModal } from "./EpicModal";
import { TaskModal } from "./TaskModal";
import "./styles.scss";

// Строка матрицы: эпик (или null для задач без эпика) + его задачи.
type Row = { epic: EpicRow | null; epicId: string; tasks: TaskRow[] };

export function Dashboard({
  board,
  onTaskUpdate,
  onEpicDelete,
  onEpicRelease,
}: {
  board: BoardView | null;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onEpicDelete: (e: EpicRow) => void;
  onEpicRelease: (e: EpicRow) => Promise<void>;
}) {
  const [epic, setEpic] = useState<EpicRow | null>(null);
  const [task, setTask] = useState<TaskRow | null>(null);
  // Свёрнутость эпиков: явный выбор пользователя (toggle[epicId]) перекрывает
  // значение по умолчанию (неактивные свёрнуты, активные развёрнуты).
  const [collapseToggle, setCollapseToggle] = useState<Record<string, boolean>>({});

  if (!board) {
    return <div className="dashboard">Совет ещё не загружен…</div>;
  }

  // Группируем задачи по эпику; задачи с неизвестным epic_id попадают в
  // отдельные строки «без эпика», чтобы не теряться с доски.
  const byEpic = new Map<string, TaskRow[]>();
  for (const t of board.tasks) {
    const list = byEpic.get(t.epic_id) ?? [];
    list.push(t);
    byEpic.set(t.epic_id, list);
  }
  const rows: Row[] = board.epics.map((e) => ({
    epic: e,
    epicId: e.task_id,
    tasks: byEpic.get(e.task_id) ?? [],
  }));
  for (const [epicId, tasks] of byEpic) {
    if (!board.epics.some((e) => e.task_id === epicId)) {
      rows.push({ epic: null, epicId, tasks });
    }
  }

  const statusCounts = STATUS_ORDER.map((s) => ({
    status: s,
    count: board.tasks.filter((t) => t.status === s).length,
  }));

  // Эпик активен, если у него есть незавершённые задачи (new → in_progress);
  // только такие по умолчанию развёрнуты полностью.
  const isActive = (tasks: TaskRow[]) =>
    tasks.some((t) => t.status !== "done" && t.status !== "cancelled");

  const isCollapsed = (epicId: string, tasks: TaskRow[]) =>
    collapseToggle[epicId] ?? !isActive(tasks);

  const toggleCollapse = (epicId: string, tasks: TaskRow[]) =>
    setCollapseToggle((prev) => ({
      ...prev,
      [epicId]: !isCollapsed(epicId, tasks),
    }));

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

      <div className="boardgrid">
        <div className="corner">Эпик</div>
        {statusCounts.map(({ status, count }) => (
          <div key={status} className={"grid-head " + status}>
            {STATUS_LABEL[status]} <b>{count}</b>
          </div>
        ))}
        {rows.map((r) => (
          <EpicRowView
            key={r.epicId}
            epic={r.epic}
            epicId={r.epicId}
            tasks={r.tasks}
            collapsed={isCollapsed(r.epicId, r.tasks)}
            onTaskUpdate={onTaskUpdate}
            onTaskOpen={setTask}
            onEpicOpen={setEpic}
            onEpicDelete={onEpicDelete}
            onEpicRelease={onEpicRelease}
            onToggle={() => toggleCollapse(r.epicId, r.tasks)}
          />
        ))}
      </div>

      {epic && <EpicModal epic={epic} onClose={() => setEpic(null)} />}
      {task && <TaskModal task={task} onTaskUpdate={onTaskUpdate} onClose={() => setTask(null)} />}
    </div>
  );
}