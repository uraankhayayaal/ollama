// Блок «ветка + MR» в модалках эпика и задачи (Ф-5). Показывается только для
// git-проектов (модалка передаёт BoardView.git). Виден всегда для git-проекта:
//
//   - ветка создана — web-ссылка на ветку на хостинге (branch_url);
//   - ветки нет — кнопка «Создать ветку эпика/задачи» (задача требует ветки её
//     эпика как базы — тогда показывается подсказка с именем эпика);
//   - MR есть — ссылка со состоянием (open/merged/closed);
//   - MR нет, но ветка есть — кнопка «Создать MR» (сервер пушит ветку и
//     открывает MR через фордж).
//
// После успешного создания сервер шлёт WS-событие board — блок сам
// перерисовывается в ссылки без перезагрузки модалки.
import { useState } from "react";
import type { GitLinkView, GitView } from "@/Types";
import "./styles.scss";

const MR_STATE_LABEL: Record<string, string> = {
  open: "открыт",
  merged: "слит",
  closed: "закрыт",
};

type Busy = "branch" | "mr" | null;

export function GitBlock({
  git,
  kind,
  id,
  epicId,
  onCreateBranch,
  onCreateMR,
}: {
  git: GitView;
  kind: "epic" | "task";
  id: string;
  epicId?: string; // для задачи — ветка её эпика нужна как база фича-ветки
  onCreateBranch?: () => Promise<void>;
  onCreateMR?: () => Promise<void>;
}) {
  const [busy, setBusy] = useState<Busy>(null);
  const [err, setErr] = useState("");

  const link: GitLinkView | undefined =
    kind === "epic" ? git.epics?.[id] : git.tasks?.[id];
  // Ветка эпика (для задачи: условие создания фича-ветки), по git-статусу.
  const epicBranch = kind === "task" && epicId ? git.epics?.[epicId]?.branch : "";

  const act = async (fn: (() => Promise<void>) | undefined, busyKind: Busy) => {
    if (!fn || busy) {
      return;
    }
    setBusy(busyKind);
    setErr("");
    try {
      await fn();
    } catch (e) {
      setErr(fmtErr(e));
    } finally {
      setBusy(null);
    }
  };

  const stateLabel = link?.mr_state ? MR_STATE_LABEL[link.mr_state] ?? link.mr_state : "";

  const branchCell = link?.branch ? (
    <>
      {link.branch_url ? (
        <a href={link.branch_url} target="_blank" rel="noreferrer">
          {link.branch}
        </a>
      ) : (
        link.branch
      )}
      {link.target && <span className="target">→ {link.target}</span>}
    </>
  ) : kind === "epic" ? (
    <>
      {onCreateBranch ? (
        <button
          className="branch-create"
          onClick={() => void act(onCreateBranch, "branch")}
          disabled={busy !== null}
        >
          {busy === "branch" ? "Создаю…" : "Создать ветку эпика"}
        </button>
      ) : (
        <span className="muted">ветка не создана</span>
      )}
    </>
  ) : epicBranch ? (
    <>
      {onCreateBranch ? (
        <button
          className="branch-create"
          onClick={() => void act(onCreateBranch, "branch")}
          disabled={busy !== null}
        >
          {busy === "branch" ? "Создаю…" : "Создать ветку задачи"}
        </button>
      ) : (
        <span className="muted">ветка не создана</span>
      )}
    </>
  ) : (
    <>
      <span className="muted">сначала создайте ветку эпика {epicId}</span>
    </>
  );

  return (
    <div className="gitblock">
      <div className="row">
        <dt>Ветка</dt>
        <dd>{branchCell}</dd>
      </div>
      {link?.branch && (
        <div className="row">
          <dt>MR</dt>
          <dd>
            {link.mr_url ? (
              <>
                <a href={link.mr_url} target="_blank" rel="noreferrer">
                  {link.mr_url}
                </a>
                {stateLabel && (
                  <span className={"mr-state " + link.mr_state}>{stateLabel}</span>
                )}
              </>
            ) : link.has_commits === false ? (
              // Ф-2: в ветке ещё нет коммитов — MR нечего открывать;
              // кнопку «Создать MR» прячем, пока специалист не поработал.
              <span className="muted">коммитов ещё нет</span>
            ) : (
              <>
                {onCreateMR ? (
                  <button
                    className="mr-create"
                    onClick={() => void act(onCreateMR, "mr")}
                    disabled={busy !== null}
                  >
                    {busy === "mr" ? "Создаю…" : "Создать MR"}
                  </button>
                ) : (
                  <span className="muted">нет MR</span>
                )}
              </>
            )}
          </dd>
        </div>
      )}
      {err && <div className="gitblock-err">{err}</div>}
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