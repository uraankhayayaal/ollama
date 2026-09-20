// Быстрые действия эпика под названием на доске.
//
//   - «Удалить» — видна, пока ни одна задача эпика ещё не взята в работу
//     специалистом (статусы new/analysis/ready).
//   - «Залить в main» (Ф-3) — ручная кнопка релиза: видна только у эпика со
//     статусом done и говорит серверу влить релизную ветку эпика в базовую
//     (main). Показывает процесс / ошибку / успех прямо под кнопкой.
import { useState } from "react";
import type { EpicRow, TaskRow } from "@/Types";
import "./styles.scss";

export function EpicActionBar({
  epic,
  tasks,
  onDelete,
  onRelease,
}: {
  epic: EpicRow;
  tasks: TaskRow[];
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
  const canRelease = epic.status === "done";
  if (!canDelete && !canRelease) {
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
