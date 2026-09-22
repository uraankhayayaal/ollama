// Быстрые действия эпика под названием на доске.
//
//   - «Пауза»/«Продолжить» (Ф-6) — приостановка/возобновление эпика. Эпики и
//     задачи изолированы в своих git-ветках, поэтому пауза НЕ откатывает код:
//     задачи эпика каскадом уходят «на паузу», оркестратор их не берёт, ветка
//     остаётся на месте. «Продолжить» возвращает задачи в «готова к работе».
//   - «Отменить» (Ф-6) — эпик и его незавершённые задачи помечаются отменёнными
//     БЕЗ отката кода: запись и ветка остаются как артефакт (хронология).
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
  onPause,
  onResume,
  onCancel,
}: {
  epic: EpicRow;
  tasks: TaskRow[];
  hasBranch?: boolean;
  onDelete: () => void;
  onRelease: () => Promise<void>;
  onPause?: () => Promise<void>;
  onResume?: () => Promise<void>;
  onCancel?: () => Promise<void>;
}) {
  // busyAction — какая кнопка сейчас выполняется (одна на полоску): disables
  // остальные и показывает «…» на активной.
  const [busyAction, setBusyAction] = useState("");
  const [actionErr, setActionErr] = useState("");
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
  // Пауза — у активного (не терминального, не приостановленного) эпика;
  // возобновление — только у эпика «на паузе».
  const canPause =
    !!onPause &&
    epic.status !== "done" &&
    epic.status !== "cancelled" &&
    epic.status !== "paused";
  const canResume = !!onResume && epic.status === "paused";
  // Отмена — у любого не завершённого эпика (в т.ч. приостановленного).
  const canCancel =
    !!onCancel && epic.status !== "done" && epic.status !== "cancelled";
  const busy = busyAction !== "";
  if (
    !canDelete &&
    !canRelease &&
    !noBranchHint &&
    !canPause &&
    !canResume &&
    !canCancel
  ) {
    return null;
  }

  const release = async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (busy) {
      return;
    }
    setBusyAction("release");
    setReleaseErr("");
    setReleased(false);
    try {
      await onRelease();
      setReleased(true);
    } catch (err) {
      setReleaseErr(fmtErr(err));
    } finally {
      setBusyAction("");
    }
  };

  // runStatus — общий исполнитель «Пауза»/«Продолжить»/«Отменить»: клик не
  // всплывает до строки (иначе открылась бы модалка), одна операция за раз,
  // ошибка показывается под кнопками.
  const runStatus = async (
    e: React.MouseEvent,
    action: "pause" | "resume" | "cancel",
    fn: () => Promise<void>,
  ) => {
    e.stopPropagation();
    if (busy) {
      return;
    }
    setBusyAction(action);
    setActionErr("");
    try {
      await fn();
    } catch (err) {
      setActionErr(fmtErr(err));
    } finally {
      setBusyAction("");
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
      {canResume && (
        <button
          className="epic-resume"
          onClick={(e) => void runStatus(e, "resume", onResume!)}
          disabled={busy}
          title="Возобновить эпик: его задачи вернутся в «готова к работе» (код в ветке не тронут)"
          aria-label={"Продолжить эпик"}
        >
          {busyAction === "resume" ? "…" : "Продолжить"}
        </button>
      )}
      {canPause && (
        <button
          className="epic-pause"
          onClick={(e) => void runStatus(e, "pause", onPause!)}
          disabled={busy}
          title="Поставить эпик на паузу: задачи приостановятся, оркестратор их не берёт; код в ветке ai/epic/… остаётся"
          aria-label={"Поставить эпик на паузу"}
        >
          {busyAction === "pause" ? "…" : "Пауза"}
        </button>
      )}
      {canCancel && (
        <button
          className="epic-cancel"
          onClick={(e) => void runStatus(e, "cancel", onCancel!)}
          disabled={busy}
          title="Отменить эпик без отката кода: запись помечается отменённой, ветка остаётся"
          aria-label={"Отменить эпик"}
        >
          {busyAction === "cancel" ? "…" : "Отменить"}
        </button>
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
          disabled={busy}
          title="Удалить эпик вместе с задачами (нельзя, если задачи уже взяты в работу)"
          aria-label={"Удалить эпик"}
        >
          Удалить
        </button>
      )}
      {actionErr && <span className="epic-release-err">{actionErr}</span>}
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
