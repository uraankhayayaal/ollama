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
  onTaskUpdate,
  onTaskOpen,
  onEpicOpen,
  onEpicDelete,
  onEpicRelease,
  onToggle,
}: {
  epic: Epic | null;
  epicId: string;
  tasks: TaskRow[];
  collapsed: boolean;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onTaskOpen: (t: TaskRow) => void;
  onEpicOpen: (e: Epic) => void;
  onEpicDelete: (e: Epic) => void;
  onEpicRelease: (e: Epic) => Promise<void>;
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
            <EpicActionBar epic={epic} tasks={tasks} onDelete={() => onEpicDelete(epic)} onRelease={() => onEpicRelease(epic)} />
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