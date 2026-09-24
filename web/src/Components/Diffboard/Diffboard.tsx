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
import type { DiffFile, DiffFileView, DiffView, ProjectKind } from "@/Types";
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
  const [mr, setMr] = useState<{ url: string; branch: string; base: string; repositories?: Record<string, { url: string; branch: string; base: string }> } | null>(null);
  const [fileQuery, setFileQuery] = useState("");
  const [fileStatus, setFileStatus] = useState("all");

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
    if (diff?.kind === "git" && !patches[path]) {
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
  const snapFiles: DiffFile[] = diff?.kind === "snap" ? [
    ...(diff.added ?? []).map((f) => ({ path: f, status: "added" as const, added: 0, deleted: 0 })),
    ...(diff.modified ?? []).map((f) => ({ path: f, status: "modified" as const, added: 0, deleted: 0 })),
    ...(diff.removed ?? []).map((f) => ({ path: f, status: "removed" as const, added: 0, deleted: 0 })),
  ] : [];
  const allFiles = diff?.kind === "git" ? gitFiles : snapFiles;
  const filteredFiles = allFiles.filter((f) =>
    (fileStatus === "all" || f.status === fileStatus) &&
    f.path.toLocaleLowerCase().includes(fileQuery.trim().toLocaleLowerCase()),
  );
  const countFiles = (status: string) => allFiles.filter((f) => f.status === status).length;

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
        <div className="diff-summary">
          <span><b>{allFiles.length}</b> файлов</span>
          <span className="added">+{allFiles.reduce((n, f) => n + f.added, 0)}</span>
          <span className="removed">−{allFiles.reduce((n, f) => n + f.deleted, 0)}</span>
          <span className="branch">{diff.branch} → {diff.base}</span>
        </div>
      )}

      {!loading && !error && diff?.kind === "snap" && (
        <p className="hint snap-hint">Локальные изменения относительно baseline-снимка</p>
      )}

      {!loading && !error && diff && allFiles.length > 0 && (
        <>
          <div className="diff-tools">
            <label className="file-search">
              <IconSearch />
              <input value={fileQuery} onChange={(e) => setFileQuery(e.target.value)} placeholder="Найти файл" aria-label="Найти файл" />
              {fileQuery && <button type="button" onClick={() => setFileQuery("")}>×</button>}
            </label>
            <div className="file-filters" aria-label="Фильтр файлов">
              {([ ["all", "Все", allFiles.length], ["modified", "Изменены", countFiles("modified")], ["added", "Добавлены", countFiles("added")], ["removed", "Удалены", countFiles("removed")], ["renamed", "Переименованы", countFiles("renamed")], ["submodule", "Сабмодули", countFiles("submodule")] ] as const)
                .filter(([value, , count]) => value === "all" || count > 0)
                .map(([value, label, count]) => (
                  <button key={value} className={fileStatus === value ? "active" : ""} onClick={() => setFileStatus(value)}>
                    {label}<span>{count}</span>
                  </button>
                ))}
            </div>
          </div>
          <div className="filelist">
            {filteredFiles.map((f) => {
              const snapPatch = diff.kind === "snap" ? diff.patches?.[f.path] : undefined;
              const gitPatch = patches[f.path]?.patch;
              return (
                <div className={"fentry" + (open[f.path] ? " expanded" : "")} key={f.path}>
                  <button className={"frow " + f.status} onClick={() => void toggle(f.path)} aria-expanded={!!open[f.path]}>
                    <span className={"file-icon " + f.status}>{fileIcon(f.status)}</span>
                    <span className="fpath">{f.path}</span>
                    <span className="fstat">
                      <i className="badge">{statusWord(f.status)}</i>
                      {diff.kind === "git" && <><b className="add">+{f.added}</b><b className="del">−{f.deleted}</b></>}
                    </span>
                    <span className={"chevron" + (open[f.path] ? " open" : "")}>›</span>
                  </button>
                  {open[f.path] && (
                    <div className="fpatch">
                      {diff.kind === "snap" && snapPatch ? <SideDiff patch={snapPatch} status={f.status} /> :
                        diff.kind === "git" && gitPatch ? <SideDiff patch={gitPatch} status={f.status} /> :
                          diff.kind === "git" ? <p className="hint">Загружаю патч…</p> : <p className="hint">Текстовый diff недоступен.</p>}
                    </div>
                  )}
                </div>
              );
            })}
            {filteredFiles.length === 0 && <p className="no-files">По этому фильтру файлов нет.</p>}
          </div>
        </>
      )}

      {!loading && !error && diff && allFiles.length === 0 && (
        <p className="empty-diff">Изменений относительно точки отхода нет.</p>
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
            <div className="ok">
              <p>Созданы запросы на слияние:</p>
              {Object.entries(mr.repositories ?? { [props.project]: mr }).map(([name, result]) => (
                <p key={name}>
                  {name}: <a href={result.url} target="_blank" rel="noreferrer">{result.url}</a>
                </p>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// SideDiff — двухколоночный дифф одного файла «до → после» (JetBrains-стиль):
// слева красным — удалённые строки, справа зелёным — добавленные.
function SideDiff({ patch, status }: { patch: string; status?: string }) {
  const s = sideBySide(patch);
  const onlyOld = status === "removed" || s.newPath === "/dev/null";
  const onlyNew = status === "added" || s.oldPath === "/dev/null";

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
    <div className={"sdiff" + (onlyOld ? " only-old" : onlyNew ? " only-new" : "")}>
      <div className="sdiff-head">
        {!onlyNew && <span className="win old" title={s.oldPath ?? undefined}>
          <span className="dot red" /> До · {s.oldPath ?? "—"}
        </span>}
        {!onlyOld && <span className="win new" title={s.newPath ?? undefined}>
          <span className="dot green" /> После · {s.newPath ?? "—"}
        </span>}
      </div>
      <div className="sdiff-cols">
        {!onlyNew && <div className="col old">{s.rows.map((r, idx) => cell(r, "left", "l" + idx))}</div>}
        {!onlyOld && <div className="col new">{s.rows.map((r, idx) => cell(r, "right", "r" + idx))}</div>}
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
    case "submodule":
      return "сабмодуль";
    default:
      return "изменён";
  }
}

function fileIcon(status: string): string {
  switch (status) {
    case "added": return "+";
    case "removed": return "−";
    case "renamed": return "↗";
    case "submodule": return "↳";
    default: return "•";
  }
}

function IconSearch() {
  return <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><circle cx="11" cy="11" r="7" /><path d="m20 20-4-4" /></svg>;
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
