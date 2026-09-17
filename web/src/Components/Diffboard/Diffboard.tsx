// Diffboard — вкладка «Дифф»: разница предложенного и текущего состояния
// (GET /api/projects/<name>/diff), а для git-проектов — приёмка:
// «Принять → MR» (commit+push+MR/PR через фордж) и «Отклонить ветку».
// Пропс kind приходит из ProjectMeta (workspace.Info.Kind).

import { useCallback, useEffect, useState } from "react";
import { acceptProject, projectDiff, rejectBranch } from "@/Api";
import type { DiffView, ProjectKind } from "@/Types";
import "./styles.scss";

export interface DiffboardProps {
  project: string;
  kind?: ProjectKind;
}

export function Diffboard(props: DiffboardProps) {
  const [diff, setDiff] = useState<DiffView | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [title, setTitle] = useState("");
  const [mr, setMr] = useState<{ url: string; branch: string; base: string } | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setDiff(await projectDiff("", props.project));
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setLoading(false);
    }
  }, [props.project]);

  useEffect(() => {
    void load();
  }, [load]);

  const onAccept = async () => {
    if (!props.kind) return;
    setBusy(true);
    setError("");
    try {
      const res = await acceptProject("", props.project, {
        title: title.trim() || undefined,
      });
      setMr(res);
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setBusy(false);
    }
  };

  const onReject = async () => {
    if (!props.kind) return;
    if (!window.confirm("Удалить фича-ветку на remote и вернуть рабочую копию на базу?")) {
      return;
    }
    setBusy(true);
    setError("");
    setMr(null);
    try {
      await rejectBranch("", props.project);
      await load();
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setBusy(false);
    }
  };

  const isGit = props.kind === "git";

  return (
    <div className="diffboard">
      {loading && <p className="hint">Загружаю дифф…</p>}

      {error && <p className="err">{error}</p>}

      {!loading && !error && diff?.kind === "git" && (
        <>
          <p className="hint">
            Ветка <strong>{diff.branch}</strong> → <strong>{diff.base}</strong> (base) · remote{" "}
            <code>{diff.remote}</code>
          </p>
          {diff.diff ? (
            <pre className="diff">{diff.diff}</pre>
          ) : (
            <p className="hint">Изменений от точки отхода нет.</p>
          )}
        </>
      )}

      {!loading && !error && diff?.kind === "snap" && (
        <>
          <p className="hint">
            Локальный проект: изменения файлов относительно точки отхода (baseline-снимка).
          </p>
          {(diff.added?.length || diff.modified?.length || diff.removed?.length) ? (
            <div className="tree">
              {diff.added?.map((f) => (
                <div className="entry added" key={f}>
                  <span>+ {f}</span>
                </div>
              ))}
              {diff.modified?.map((f) => (
                <div className="entry modified" key={f}>
                  <span>~ {f}</span>
                </div>
              ))}
              {diff.removed?.map((f) => (
                <div className="entry removed" key={f}>
                  <span>− {f}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="hint">Изменений относительно точки отхода нет.</p>
          )}
        </>
      )}

      {!loading && !error && diff && !isGit && (
        <p className="hint">Приёмка через MR доступна только git-проектам (открытым по git-URL).</p>
      )}

      {isGit && (
        <div className="actions">
          <input
            type="text"
            placeholder="Заголовок MR/PR (пусто — по умолчанию)"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            disabled={busy}
          />
          <div className="row">
            <button className="btn accept" onClick={() => void onAccept()} disabled={busy}>
              {busy ? "Работаю…" : "Принять → MR"}
            </button>
            <button className="btn reject" onClick={() => void onReject()} disabled={busy}>
              Отклонить ветку
            </button>
          </div>
          {mr && (
            <p className="ok">
              Создан запрос на слияние:{" "}
              <a href={mr.url} target="_blank" rel="noreferrer">
                {mr.url}
              </a>
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function fmtErr(err: unknown): string {
  if (err instanceof Error) {
    return err.message;
  }
  if (err && typeof err === "object" && "message" in err) {
    return String((err as { message: unknown }).message);
  }
  return String(err ?? "Ошибка");
}