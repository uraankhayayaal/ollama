// Строка доски = эпик: заголовок (имя эпика, иконка статуса, тумблер
// сворачивания) и ячейки по всем статусам. В свёрнутом виде — компактная
// строка «иконка + название» и счётчики задач в колонках; интерактивность
// счётчиков даёт пересчёт из пропсов при каждом апдейте доски.
import type { EpicRow as Epic, TaskRow } from "@/Types";
import { STATUS_LABEL, STATUS_ORDER } from "../board";
import { Cell } from "../Cell";
import { EpicActionBar } from "../EpicActionBar";
import "./styles.scss";

export function EpicRow({
  epic,
  epicId,
  tasks,
  collapsed,
  hasBranch,
  onTaskUpdate,
  onTaskOpen,
  onEpicOpen,
  onEpicDelete,
  onEpicRelease,
  onEpicPause,
  onEpicResume,
  onEpicCancel,
  onToggle,
}: {
  epic: Epic | null;
  epicId: string;
  tasks: TaskRow[];
  collapsed: boolean;
  // Есть ли у эпика релизная ветка (по board.git — регистр-статус сервера).
  // undefined — проект не git; false — git-проект, ветки нет; true — есть.
  hasBranch?: boolean;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onTaskOpen: (t: TaskRow) => void;
  onEpicOpen: (e: Epic) => void;
  onEpicDelete: (e: Epic) => void;
  onEpicRelease: (e: Epic) => Promise<void>;
  // Ф-6: пауза/возобновление/отмена эпика (код в ветке остаётся).
  onEpicPause?: (e: Epic) => Promise<void>;
  onEpicResume?: (e: Epic) => Promise<void>;
  onEpicCancel?: (e: Epic) => Promise<void>;
  onToggle: () => void;
}) {
  const plain = epic ? "" : " plain";
  return (
    <>
      <div
        className={"epic-head" + (collapsed ? " collapsed" : "") + plain}
        onClick={() => epic && onEpicOpen(epic)}
        title={epic ? epic.title : undefined}
      >
        <button
          className="toggle"
          onClick={(e) => {
            e.stopPropagation();
            onToggle();
          }}
          aria-label={collapsed ? "Развернуть эпик" : "Свернуть эпик"}
        >
          {collapsed ? "▸" : "▾"}
        </button>
        {epic && <span className={"dot " + epic.status} />}
        <strong className="title">
          {epic ? epic.title : "Без эпика" + (epicId ? " · " + epicId : "")}
        </strong>
        {!collapsed && epic && (
          <>
            <span className="id">{epic.task_id}</span>
            <span className={"status " + epic.status}>{STATUS_LABEL[epic.status] ?? epic.status}</span>
            <EpicActionBar
              epic={epic}
              tasks={tasks}
              hasBranch={hasBranch}
              onDelete={() => onEpicDelete(epic)}
              onRelease={() => onEpicRelease(epic)}
              onPause={onEpicPause ? () => onEpicPause(epic) : undefined}
              onResume={onEpicResume ? () => onEpicResume(epic) : undefined}
              onCancel={onEpicCancel ? () => onEpicCancel(epic) : undefined}
            />
          </>
        )}
      </div>
      {STATUS_ORDER.map((s) => (
        <Cell
          key={s}
          status={s}
          epicId={epicId}
          tasks={tasks}
          collapsed={collapsed}
          onTaskUpdate={onTaskUpdate}
          onTaskOpen={onTaskOpen}
        />
      ))}
    </>
  );
}