// Быстрые действия эпика под названием на доске.
//
//   - «Удалить» — видна, пока ни одна задача эпика ещё не взята в работу
//     специалистом (статусы new/analysis/ready).
//   - «Залить в main» (Ф-3) — ручная кнопка релиза: видна только у эпика со
//     статусом done И с созданной релизной веткой (hasBranch по board.git —
//     регистр-статус сервера). Без ветки кнопки нет, вместо неё — подсказка:
//     релиз невозможен, пока у эпика нет ветки (Ф-5). Показывает процесс /
//     ошибку / успех прямо под кнопкой.
import { useState } from "react";
import type { EpicRow, TaskRow } from "@/Types";
import "./styles.scss";

export function EpicActionBar({
  epic,
  tasks,
  hasBranch,
  onDelete,
  onRelease,
}: {
  epic: EpicRow;
  tasks: TaskRow[];
  hasBranch?: boolean;
  onDelete: () => void;
  onRelease: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [releaseErr, setReleaseErr] = useState("");
  const [released, setReleased] = useState(false);

  const canDelete = tasks.every(
    (t) =>
      t.status !== "in_progress" &&
      t.status !== "done" &&
      t.status !== "cancelled",
  );
  // done-эпик без релизной ветки релизить нечем: «Залить в main» не показываем.
  const canRelease = epic.status === "done" && hasBranch === true;
  // Подсказка вместо кнопки — только для git-проектов (hasBranch === false),
  // где ветка ещё не создана; для не-git проектов hasBranch === undefined.
  const noBranchHint = epic.status === "done" && hasBranch === false;
  if (!canDelete && !canRelease && !noBranchHint) {
    return null;
  }

  const release = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (busy) {
      return;
    }
    setBusy(true);
    setReleaseErr("");
    setReleased(false);
    try {
      await onRelease();
      setReleased(true);
    } catch (err) {
      setReleaseErr(fmtErr(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="epic-actions">
      {noBranchHint && (
        <span
          className="epic-release-hint"
          title="У эпика нет релизной ветки (ai/epic/…). Создайте ветку эпика, чтобы можно было влить его в main."
        >
          нет релизной ветки
        </span>
      )}
      {canRelease && (
        <>
          <button
            className="epic-release"
            onClick={(e) => void release(e)}
            disabled={busy}
            title="Влить релизную ветку эпика в main (только для done-эпика; при конфликтах случится авто-резолв)"
            aria-label={"Залить в main"}
          >
            {busy ? "Вливаю…" : released ? "Влито" : "Залить в main"}
          </button>
          {releaseErr && <span className="epic-release-err">{releaseErr}</span>}
          {released && !releaseErr && (
            <span className="epic-release-ok">релиз создан</span>
          )}
        </>
      )}
      {canDelete && (
        <button
          className="epic-delete"
          onClick={(e) => {
            e.stopPropagation();
            onDelete();
          }}
          title="Удалить эпик вместе с задачами (нельзя, если задачи уже взяты в работу)"
          aria-label={"Удалить эпик"}
        >
          Удалить
        </button>
      )}
    </div>
  );
}

function fmtErr(err: unknown): string {
  if (err && typeof err === "object" && "message" in err) {
    const m = (err as { message?: unknown }).message;
    if (typeof m === "string" && m) {
      return m;
    }
  }
  return String(err);
}
