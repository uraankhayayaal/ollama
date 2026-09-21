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
  onEpicBranch,
  onTaskBranch,
  onEpicMR,
  onTaskMR,
  collapsed = false,
  onToggleCollapse,
}: {
  board: BoardView | null;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onEpicDelete: (e: EpicRow) => void;
  onEpicRelease: (e: EpicRow) => Promise<void>;
  // Ф-5: создание ветки эпика/задачи и «Создать MR» (пушит ветку и открывает
  // MR через фордж). Кнопки в блоке «ветка + MR» модалок.
  onEpicBranch?: (e: EpicRow) => Promise<void>;
  onTaskBranch?: (t: TaskRow) => Promise<void>;
  onEpicMR?: (e: EpicRow) => Promise<void>;
  onTaskMR?: (t: TaskRow) => Promise<void>;
  // Свёрнутый режим: тонкая вертикальная полоска справа с кнопкой разворота
  // и метриками (эпики/задачи/баги + разбивка по статусам).
  collapsed?: boolean;
  onToggleCollapse?: () => void;
}) {
  // Открытая модалка хранится по ID, а строка/гит-статус выводятся из свежего
  // board: WS-событие (git/MR обновились) перерисует модалку без переоткрытия.
  const [epicId, setEpicId] = useState<string | null>(null);
  const [taskId, setTaskId] = useState<string | null>(null);
  // Свёрнутость эпиков: явный выбор пользователя (toggle[epicId]) перекрывает
  // значение по умолчанию (неактивные свёрнуты, активные развёрнуты).
  const [collapseToggle, setCollapseToggle] = useState<Record<string, boolean>>({});

  if (!board) {
    if (collapsed) {
      return (
        <div className="dashboard collapsed">
          <button className="expand" onClick={onToggleCollapse} title="Развернуть доску">
            «
          </button>
          <div className="dstrip hint">…</div>
        </div>
      );
    }
    return <div className="dashboard">Совет ещё не загружен…</div>;
  }

  // Свёрнутый режим: только метрики в статусной колонке, доска не рендерится.
  if (collapsed) {
    const byStatus = STATUS_ORDER.map((s) => ({
      status: s,
      count: board.tasks.filter((t) => t.status === s).length,
    }));
    return (
      <div className="dashboard collapsed">
        <button className="expand" onClick={onToggleCollapse} title="Развернуть доску">
          «
        </button>
        <div className="dstrip">
          <div className="st epic" title={`Эпиков: ${board.epics.length}`}>
            <IconEpic />
            <b>{board.epics.length}</b>
          </div>
          <div className="st task" title={`Задач: ${board.tasks.length}`}>
            <IconTask />
            <b>{board.tasks.length}</b>
          </div>
          <div className="st bug" title={`Багов: ${board.bugs.length}`}>
            <IconBug />
            <b>{board.bugs.length}</b>
          </div>
          {byStatus.map(({ status, count }) => (
            <div
              key={status}
              className={"st " + status}
              title={`${STATUS_LABEL[status]}: ${count}`}
            >
              <b>{count}</b>
            </div>
          ))}
        </div>
      </div>
    );
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

  // Строка открытой модалки — из актуального снимка доски (см. выше): если
  // строка исчезла (удалили), модалка просто не рендерится.
  const epic = epicId ? board.epics.find((e) => e.task_id === epicId) ?? null : null;
  const task = taskId ? board.tasks.find((t) => t.task_id === taskId) ?? null : null;

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
            hasBranch={board.git ? !!board.git.epics?.[r.epicId]?.branch : undefined}
            onTaskUpdate={onTaskUpdate}
            onTaskOpen={(t) => setTaskId(t.task_id)}
            onEpicOpen={(e) => setEpicId(e.task_id)}
            onEpicDelete={onEpicDelete}
            onEpicRelease={onEpicRelease}
            onToggle={() => toggleCollapse(r.epicId, r.tasks)}
          />
        ))}
      </div>

      {epic && (
        <EpicModal
          epic={epic}
          git={board.git}
          onCreateBranch={onEpicBranch}
          onCreateMR={onEpicMR}
          onClose={() => setEpicId(null)}
        />
      )}
      {task && (
        <TaskModal
          task={task}
          git={board.git}
          onCreateBranch={onTaskBranch}
          onCreateMR={onTaskMR}
          onTaskUpdate={onTaskUpdate}
          onClose={() => setTaskId(null)}
        />
      )}
    </div>
  );
}

// Иконки метрик для свёрнутой статусной колонки (inline-SVG, без эмодзи).
function IconEpic() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 5a2 2 0 0 1 2-2h4l2 3h8a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </svg>
  );
}

function IconTask() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M9 6l3-3 3 3" />
      <path d="M5 4h2.5l3.5 3.5H12" />
      <path d="M12 9l4-2 3 3v8a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V9l3-2" />
    </svg>
  );
}

function IconBug() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="8" y="8" width="8" height="11" rx="4" />
      <path d="M12 8V5" />
      <path d="M2.5 13h4M17.5 13h4M3 20l3.5-2M21 20l-3.5-2M7.5 13a4.5 4.5 0 0 0 9 0" />
    </svg>
  );
}