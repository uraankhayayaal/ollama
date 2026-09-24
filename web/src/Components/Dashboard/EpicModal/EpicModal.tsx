// Мода окно с подробной информацией об эпике (клик по названию эпика в
// заголовке строки доски). Ф-5: блок «ветка + MR» (git-workflow) — ссылка на
// ветку и MR, кнопки «Создать ветку эпика» и «Создать MR» (push → MR → main).
// Ф-6: «Пауза»/«Продолжить»/«Отменить» — эпик изолирован в своей ветке,
// поэтому приостановка/отмена не откатывают код.
import { useState } from "react";
import type { BranchDiffContext, EpicRow, GitView } from "@/Types";
import { STATUS_LABEL } from "../board";
import { Modal } from "../Modal";
import { GitBlock } from "../GitBlock";
import "./styles.scss";

export function EpicModal({
  epic,
  git,
  onCreateBranch,
  onCreateMR,
  onShowDiff,
  onPause,
  onResume,
  onCancel,
  onClose,
}: {
  epic: EpicRow;
  git?: GitView;
  onCreateBranch?: (e: EpicRow) => Promise<void>;
  onCreateMR?: (e: EpicRow) => Promise<void>;
  onShowDiff?: (context: BranchDiffContext) => void;
  onPause?: (e: EpicRow) => Promise<void>;
  onResume?: (e: EpicRow) => Promise<void>;
  onCancel?: (e: EpicRow) => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState("");
  const [err, setErr] = useState("");

  const canPause =
    !!onPause &&
    epic.status !== "done" &&
    epic.status !== "cancelled" &&
    epic.status !== "paused";
  const canResume = !!onResume && epic.status === "paused";
  const canCancel =
    !!onCancel && epic.status !== "done" && epic.status !== "cancelled";
  const epicBranch = git?.epics?.[epic.task_id]?.branch;

  const run = async (action: string, fn: () => Promise<void>) => {
    if (busy) {
      return;
    }
    setBusy(action);
    setErr("");
    try {
      await fn();
    } catch (e) {
      setErr(e instanceof Error && e.message ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  return (
    <Modal title={"Эпик · " + epic.task_id} onClose={onClose}>
      <dl className="details">
        <div>
          <dt>Название</dt>
          <dd className="name">{epic.title}</dd>
        </div>
        <div>
          <dt>Статус</dt>
          <dd className={"status " + epic.status}>{STATUS_LABEL[epic.status] ?? epic.status}</dd>
        </div>
        {(epic.merge_conflict_files ?? []).length > 0 && (
          <div className="merge-conflict-row">
            <dt>Конфликт мёрджа</dt>
            <dd className="merge-conflict">
              Релизная ветка не влилась в main: {(epic.merge_conflict_files ?? []).join(", ")}.
              <br />Нужен резолв (ResolveGitConflicts / ручной rebase), затем «Залить в main».
            </dd>
          </div>
        )}
        <div>
          <dt>Проект</dt>
          <dd>{epic.project_name}</dd>
        </div>
        <div>
          <dt>Лид</dt>
          <dd>{epic.assigned_role || "—"}</dd>
        </div>
        <div>
          <dt>Порядок</dt>
          <dd>{epic.sequence_order}</dd>
        </div>
        {(epic.dependencies ?? []).length > 0 && (
          <div>
            <dt>Зависимости</dt>
            <dd>{epic.dependencies.join(", ")}</dd>
          </div>
        )}
      </dl>

      {(canPause || canResume || canCancel) && (
        <div className="epic-status-actions">
          {canResume && (
            <button
              className="epic-resume"
              disabled={busy !== ""}
              onClick={() => void run("resume", () => onResume!(epic))}
              title="Возобновить эпик: его задачи вернутся в «готова к работе»"
            >
              {busy === "resume" ? "…" : "Продолжить"}
            </button>
          )}
          {canPause && (
            <button
              className="epic-pause"
              disabled={busy !== ""}
              onClick={() => void run("pause", () => onPause!(epic))}
              title="Поставить эпик на паузу: задачи приостановятся, код в ветке останется"
            >
              {busy === "pause" ? "…" : "Пауза"}
            </button>
          )}
          {canCancel && (
            <button
              className="epic-cancel"
              disabled={busy !== ""}
              onClick={() => void run("cancel", () => onCancel!(epic))}
              title="Отменить эпик без отката кода: запись помечается отменённой, ветка остаётся"
            >
              {busy === "cancel" ? "…" : "Отменить"}
            </button>
          )}
          {err && <span className="epic-status-err">{err}</span>}
        </div>
      )}

      {git && (
        <GitBlock
          git={git}
          kind="epic"
          id={epic.task_id}
          onCreateBranch={onCreateBranch ? () => onCreateBranch(epic) : undefined}
          onCreateMR={onCreateMR ? () => onCreateMR(epic) : undefined}
          onShowDiff={onShowDiff && epicBranch ? () => onShowDiff({
            ref: epicBranch,
            vs: git.base || "main",
            label: `Эпик · ${epic.task_id} · ${epic.title}`,
          }) : undefined}
        />
      )}

      <h4 className="section">Описание</h4>
      <p className="desc">{epic.description || "—"}</p>

      {epic.architecture_summary && (
        <>
          <h4 className="section">Архитектура</h4>
          <pre className="arch">{epic.architecture_summary}</pre>
        </>
      )}

      <div className="meta">
        создан {epic.created_at} · обновлён {epic.updated_at}
      </div>
    </Modal>
  );
}
