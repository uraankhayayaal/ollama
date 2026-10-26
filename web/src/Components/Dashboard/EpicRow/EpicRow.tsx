// Строка доски = эпик: заголовок (имя эпика, иконка статуса, тумблер
// сворачивания) и ячейки по всем статусам. В свёрнутом виде — компактная
// строка «иконка + название» и счётчики задач в колонках; интерактивность
// счётчиков даёт пересчёт из пропсов при каждом апдейте доски.
import { useState } from "react";
import type { EpicRow as Epic, BugRow, TaskRow } from "@/Types";
import { STATUS_LABEL, TASK_STATUS_ORDER } from "../board";
import { tokenLabel, tokenTitle } from "../tokens";
import { Cell } from "../Cell";
import { EpicActionBar } from "../EpicActionBar";
import { BugCard } from "../BugCard";
import { getEpicDragID, setEpicDragID } from "../dnd";
import "./styles.scss";

export function EpicRow({
  epic,
  epicId,
  tasks,
  bugs,
  collapsed,
  hasBranch,
  onTaskUpdate,
  onTaskOpen,
  onBugOpen,
  onEpicOpen,
  onEpicDelete,
  onEpicRelease,
  onEpicCancel,
  onEpicReorder,
  onEpicShift,
  onTaskDelete,
  onToggle,
}: {
  epic: Epic | null;
  epicId: string;
  tasks: TaskRow[];
  bugs?: BugRow[];
  collapsed: boolean;
  // Есть ли у эпика релизная ветка (по board.git — регистр-статус сервера).
  // undefined — проект не git; false — git-проект, ветки нет; true — есть.
  hasBranch?: boolean;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onTaskOpen: (t: TaskRow) => void;
  onBugOpen: (b: BugRow) => void;
  onEpicOpen: (e: Epic) => void;
  onEpicDelete: (e: Epic) => void;
  onEpicRelease: (e: Epic) => Promise<void>;
  // Отмена эпика (код в ветке остаётся).
  onEpicCancel?: (e: Epic) => Promise<void>;
  // Пересортировка эпиков: перетащить thisId на место targetId (after=true —
  // после, false — перед). Игнорируется для строк без эпика и межфазовых
  // переносов (фаза — первичный ключ очереди).
  onEpicReorder?: (thisId: string, targetId: string, after: boolean) => void;
  // Показать стрелки поднять/опустить эпик (dir=±1 относительно
  // эффективного порядка доски).
  onEpicShift?: (e: Epic, dir: -1 | 1) => void;
  onTaskDelete?: (t: TaskRow) => void;
  onToggle: () => void;
}) {
  const plain = epic ? "" : " plain";
  // Подсветка зоны сброса при перетаскивании эпика поверх строки.
  const [dragOver, setDragOver] = useState(false);
  const canReorder = !!epic && !!onEpicReorder;

  const onRowDragOver = (e: React.DragEvent) => {
    const id = getEpicDragID();
    if (canReorder && id && id !== epicId) {
      e.preventDefault();
    }
  };
  const onRowDrop = (e: React.DragEvent) => {
    const id = getEpicDragID();
    setDragOver(false);
    if (!id) {
      return;
    }
    if (id !== epicId) {
      const rect = e.currentTarget.getBoundingClientRect();
      onEpicReorder?.(id, epicId, e.clientY > rect.top + rect.height / 2);
    }
    setEpicDragID(null);
  };

  return (
    <div
      className={"epic-row-grid" + (dragOver ? " drag-over" : "")}
      onDragOver={onRowDragOver}
      onDragEnter={() => setDragOver(true)}
      onDragLeave={() => setDragOver(false)}
      onDrop={onRowDrop}
    >
      <div
        className={"epic-head" + (collapsed ? " collapsed" : "") + plain}
        onClick={() => epic && onEpicOpen(epic)}
        title={epic ? epic.title : undefined}
      >
        {canReorder && (
          <span
            className="drag-handle"
            draggable
            onDragStart={(e) => {
              e.stopPropagation();
              setEpicDragID(epicId);
            }}
            onDragEnd={() => setEpicDragID(null)}
            title="Перетащить, чтобы изменить приоритет"
            aria-hidden="true"
          >
            ⠿
          </span>
        )}
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
        {/* Ф-4: расход токенов эпика — прогноз «≈N», факт «N» и (для
            завершённых) ошибка прогноза. Подпись компактная, полные числа —
            в title. */}
        {epic && tokenLabel(epic, epic.status === "done") && (
          <span className="tokens" title={tokenTitle(epic)}>
            {tokenLabel(epic, epic.status === "done")}
          </span>
        )}
        {!collapsed && epic && (
          <>
            <span className="id">{epic.task_id}</span>
            <span className={"status " + epic.status}>{STATUS_LABEL[epic.status] ?? epic.status}</span>
            {epic.merged_into_main && (
              <span className="merged-into-main-badge" title="Релизная ветка эпика влита в main">Слит в main</span>
            )}
            {onEpicShift && (
              <span className="epic-order" aria-label="Приоритет эпика">
                <button
                  type="button"
                  title="Поднять приоритет (выше — раньше)"
                  onClick={(e) => {
                    e.stopPropagation();
                    onEpicShift(epic, -1);
                  }}
                >
                  ↑
                </button>
                <button
                  type="button"
                  title="Опустить приоритет"
                  onClick={(e) => {
                    e.stopPropagation();
                    onEpicShift(epic, 1);
                  }}
                >
                  ↓
                </button>
              </span>
            )}
            <EpicActionBar
              epic={epic}
              tasks={tasks}
              hasBranch={hasBranch}
              onDelete={() => onEpicDelete(epic)}
              onRelease={() => onEpicRelease(epic)}
              onCancel={onEpicCancel ? () => onEpicCancel(epic) : undefined}
            />
          </>
        )}
      </div>
      {TASK_STATUS_ORDER.map((s) => (
        <Cell
          key={s}
          status={s}
          epicId={epicId}
          tasks={tasks}
          collapsed={collapsed}
          onTaskUpdate={onTaskUpdate}
          onTaskOpen={onTaskOpen}
          onTaskDelete={onTaskDelete}
        />
      ))}

      {(bugs?.length ?? 0) > 0 && (
        <section className="epic-bugs" aria-label={`Баги эпика ${epicId}`}>
          <div className="bug-row-label">Баги <span>{bugs!.length}</span></div>
          <ul>{bugs!.map((b) => <BugCard key={b.bug_id} bug={b} onOpen={() => onBugOpen(b)} />)}</ul>
        </section>
      )}

    </div>
  );
}