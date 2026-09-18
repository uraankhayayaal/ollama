// Diffboard — панель «Дифф»: разница предложенного и текущего состояния
// (GET /api/projects/<name>/diff), а для git-проектов — приёмка:
// «Принять → MR» (commit+push+MR/PR через фордж) и «Отклонить ветку».
// Ф-3: для git-проектов дифф загружается лениво — список файлов (метаданные),
// патч конкретного файла подтягивается при раскрытии (projectDiffFile) и
// показывается side-by-side «до → после» в стиле JetBrains (см. sidebyside.ts).
// Пропс kind приходит из ProjectMeta (workspace.Info.Kind).
//
// Панель скрыта по умолчанию (showDiffboard=false): выдвигается снизу по
// плавающей кнопке «Дифф», а внутри — кнопкой «×» сверху справа
// (toggleDiffboard) сворачивается обратно.

import { useCallback, useEffect, useState } from "react";
import { acceptProject, projectDiff, projectDiffFile, rejectBranch } from "@/Api";
import type { DiffFileView, DiffView, ProjectKind } from "@/Types";
import { sideBySide, type SideRow } from "./sidebyside";
import "./styles.scss";

export interface DiffboardProps {
  project: string;
  kind?: ProjectKind;
  showDiffboard?: boolean;
  toggleDiffboard?: () => void;
}

const BASE = "";

export function Diffboard(props: DiffboardProps) {
  const [diff, setDiff] = useState<DiffView | null>(null);
  // патчи по файлам: path → DiffFileView (ленивая загрузка, запоминаем).
  const [patches, setPatches] = useState<Record<string, DiffFileView | undefined>>({});
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [title, setTitle] = useState("");
  const [mr, setMr] = useState<{ url: string; branch: string; base: string } | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    setPatches({});
    setOpen({});
    try {
      setDiff(await projectDiff(BASE, props.project));
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setLoading(false);
    }
  }, [props.project]);

  useEffect(() => {
    void load();
  }, [load]);

  // Раскрытие файла: подтягиваем патч (если ещё не загружен) — лениво.
  const toggle = async (path: string) => {
    setOpen((prev) => ({ ...prev, [path]: !prev[path] }));
    if (!patches[path]) {
      try {
        const pv = await projectDiffFile(BASE, props.project, path);
        setPatches((prev) => ({ ...prev, [path]: pv }));
      } catch (e) {
        setError(fmtErr(e));
      }
    }
  };

  const onAccept = async () => {
    if (!props.kind) return;
    setBusy(true);
    setError("");
    try {
      const res = await acceptProject(BASE, props.project, {
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
      await rejectBranch(BASE, props.project);
      await load();
    } catch (e) {
      setError(fmtErr(e));
    } finally {
      setBusy(false);
    }
  };

  const isGit = props.kind === "git";
  const gitFiles = diff?.files ?? [];

  return (
    <div className={"diffboard" + (props.showDiffboard === false ? " hidden" : "")}>
      {props.toggleDiffboard && (
        <div className="head title-head">
          <p className="hint">Дифф</p>
          <button className="btn close" onClick={props.toggleDiffboard} title="Свернуть окно">
            ×
          </button>
        </div>
      )}

      {loading && <p className="hint">Загружаю дифф…</p>}

      {error && <p className="err">{error}</p>}

      {!loading && !error && diff?.kind === "git" && (
        <>
          <div className="head">
            <p className="hint">
              Ветка <strong>{diff.branch}</strong> → <strong>{diff.base}</strong> (base) · remote{" "}
              <code>{diff.remote}</code> · файлов: {gitFiles.length}
            </p>
          </div>
          {gitFiles.length > 0 ? (
            <div className="filelist">
              {gitFiles.map((f) => (
                <div className="fentry" key={f.path}>
                  <button className={"frow " + f.status} onClick={() => void toggle(f.path)}>
                    <span className="fpath">{f.path}</span>
                    <span className="fstat">
                      <i className="badge">{statusWord(f.status)}</i>
                      <b className="add">+{f.added}</b>
                      <b className="del">-{f.deleted}</b>
                    </span>
                  </button>
                  {open[f.path] && (
                    <div className="fpatch">
                      {patches[f.path] ? (
                        <SideDiff patch={patches[f.path]!.patch} />
                      ) : (
                        <p className="hint">Гружу патч…</p>
                      )}
                    </div>
                  )}
                </div>
              ))}
            </div>
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

// SideDiff — двухколоночный дифф одного файла «до → после» (JetBrains-стиль):
// слева красным — удалённые строки, справа зелёным — добавленные.
function SideDiff({ patch }: { patch: string }) {
  const s = sideBySide(patch);

  if (s.binary) {
    return <p className="hint">Бинарный файл: содержимое в диффе недоступно.</p>;
  }
  if (s.rows.length === 0) {
    return (
      <pre className="rawpatch">
        {patch}
      </pre>
    );
  }

  const cell = (row: SideRow, side: "left" | "right", key: string) => {
    const line = side === "left" ? row.left : row.right;
    return (
      <div key={key} className={"sd-line" + (line ? " " + line.kind : " empty")}>
        <span className="sd-no">{line ? line.no : ""}</span>
        <span className="sd-tx">{line ? line.text : "\u00a0"}</span>
      </div>
    );
  };

  return (
    <div className="sdiff">
      <div className="sdiff-head">
        <span className="win old" title={s.oldPath ?? undefined}>
          <span className="dot red" /> До · {s.oldPath ?? "—"}
        </span>
        <span className="win new" title={s.newPath ?? undefined}>
          <span className="dot green" /> После · {s.newPath ?? "—"}
        </span>
      </div>
      <div className="sdiff-cols">
        <div className="col old">
          {s.rows.map((r, idx) => cell(r, "left", "l" + idx))}
        </div>
        <div className="col new">
          {s.rows.map((r, idx) => cell(r, "right", "r" + idx))}
        </div>
      </div>
    </div>
  );
}

function statusWord(s: string): string {
  switch (s) {
    case "added":
      return "новый";
    case "removed":
      return "удалён";
    case "renamed":
      return "переименован";
    default:
      return "изменён";
  }
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