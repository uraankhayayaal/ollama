// Diffboard — панель веточного diff. Список файлов загружается отдельно,
// патч конкретного файла подтягивается при раскрытии (projectDiffFile) и
// показывается side-by-side «до → после» в стиле JetBrains (см. sidebyside.ts).

import { useCallback, useEffect, useRef, useState } from "react";
import { projectDiff, projectDiffFile } from "@/Api";
import type { BranchDiffContext, DiffFile, DiffFileView, DiffView } from "@/Types";
import { sideBySide, type SideRow } from "./sidebyside";
import "./styles.scss";

export interface DiffboardProps {
  project: string;
  context: BranchDiffContext;
  onClose: () => void;
}

const BASE = "";

export function Diffboard(props: DiffboardProps) {
  const [diff, setDiff] = useState<DiffView | null>(null);
  // патчи по файлам: path → DiffFileView (ленивая загрузка, запоминаем).
  const [patches, setPatches] = useState<Record<string, DiffFileView | undefined>>({});
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [fileQuery, setFileQuery] = useState("");
  const [fileStatus, setFileStatus] = useState("all");
  const requestID = useRef(0);
  const contextKey = `${props.context?.ref ?? ""}\0${props.context?.vs ?? ""}`;
  const currentContextKey = useRef(contextKey);
  currentContextKey.current = contextKey;

  const load = useCallback(async () => {
    const id = ++requestID.current;
    const key = `${props.context?.ref ?? ""}\0${props.context?.vs ?? ""}`;
    setLoading(true);
    setError("");
    setDiff(null);
    setPatches({});
    setOpen({});
    setFileQuery("");
    setFileStatus("all");
    try {
      const next = await projectDiff(BASE, props.project, props.context?.ref, props.context?.vs);
      if (id === requestID.current && key === currentContextKey.current) setDiff(next);
    } catch (e) {
      if (id === requestID.current && key === currentContextKey.current) setError(fmtErr(e));
    } finally {
      if (id === requestID.current) setLoading(false);
    }
  }, [props.project, props.context?.ref, props.context?.vs]);

  useEffect(() => {
    void load();
  }, [load]);

  // Esc закрывает верхнюю панель diff, не закрывая модалку под ней.
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        props.onClose();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [props.onClose]);

  // Раскрытие файла: подтягиваем патч (если ещё не загружен) — лениво.
  const toggle = async (path: string) => {
    setOpen((prev) => ({ ...prev, [path]: !prev[path] }));
    if (diff?.kind === "git" && !patches[path]) {
      try {
        const key = contextKey;
        const pv = await projectDiffFile(BASE, props.project, path, props.context?.ref, props.context?.vs);
        if (key === currentContextKey.current) setPatches((prev) => ({ ...prev, [path]: pv }));
      } catch (e) {
        if (contextKey === currentContextKey.current) setError(fmtErr(e));
      }
    }
  };

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
    <div className="diffboard" role="dialog" aria-modal="true" aria-label={props.context.label}>
      <div className="head title-head">
        <p className="hint">{props.context.label}</p>
        <button className="btn close" onClick={props.onClose} title="Закрыть дифф">
          ×
        </button>
      </div>

      {loading && <p className="hint">Загружаю дифф…</p>}

      {error && <p className="err">{error}</p>}

      {!loading && !error && diff?.kind === "git" && (
        <div className="diff-summary">
          <span><b>{allFiles.length}</b> файлов</span>
          <span className="added">+{allFiles.reduce((n, f) => n + f.added, 0)}</span>
          <span className="removed">−{allFiles.reduce((n, f) => n + f.deleted, 0)}</span>
          <span className="branch">{diff.branch} → {diff.base || props.context.vs}</span>
        </div>
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
        <p className="empty-diff">Изменений в этой ветке относительно {props.context.vs} нет.</p>
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
