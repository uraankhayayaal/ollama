// WorkspacePicker — folder-компонент (канон Dashboard): index.ts резолвит,
// styles.scss рядом. REST канона api.ts: listProjects(64)/openProject(68);
// типы ProjectMeta — экспорт api.ts. Пропсы зеркалят App:153
// (projects/current/onOpen/busy). BASE/fmtErr — локальный канон-артефакт.

import { useEffect, useState, type FormEvent } from "react";
import { listProjects } from "../../Api";
import "./styles.scss";
import { ProjectMeta } from "../../Types";

const BASE = ""; // dev: Vite-прокси /api→backend; прод: embed same-origin.

export function WorkspacePicker(props: {
  projects: ProjectMeta[];
  current: ProjectMeta | null;
  onOpen: (spec: { path_or_git?: string; git_url?: string }) => Promise<void>;
  busy: boolean;
}) {
  const [mode, setMode] = useState<"browse" | "new">("browse");
  const [path, setPath] = useState("");
  const [url, setUrl] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    if (props.projects.length === 0) {
      void listProjects(BASE)
        .then(() => undefined)
        .catch((e) => setError(fmtErr(e)));
    }
  }, [props.projects.length]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError("");
    void props
      .onOpen({
        path_or_git: path.trim() || undefined,
        git_url: url.trim() || undefined,
      })
      .then(() => {
        setMode("browse");
        setPath("");
        setUrl("");
      })
      .catch((err) => setError(fmtErr(err)));
  };

  return (
    <div className="picker">
      <select
        value={props.current?.project_name ?? ""}
        onChange={(e) => {
          const name = e.target.value;
          const hit = props.projects.find((p) => p.project_name === name);
          if (hit) {
            void props.onOpen({ path_or_git: hit.project_name });
          }
        }}
      >
        <option value="">— выбрать —</option>
        {props.projects.map((p) => (
          <option key={p.project_name} value={p.project_name}>
            {p.project_name}
          </option>
        ))}
      </select>

      {mode === "new" ? (
        <form className="picker-form" onSubmit={submit}>
          <input
            placeholder="Путь к папке или git-URL"
            value={path}
            onChange={(e) => setPath(e.target.value)}
          />
          <input
            placeholder="git_url (опц.)"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
          />
          <button className="btn primary" disabled={props.busy || (!path && !url)}>
            {props.busy ? "…" : "Открыть"}
          </button>
          <button type="button" className="btn" onClick={() => setMode("browse")}>
            Назад
          </button>
        </form>
      ) : (
        <button className="btn" onClick={() => setMode("new")}>
          + Новый
        </button>
      )}

      {error && <div className="err">{error}</div>}
    </div>
  );
}

function fmtErr(e: unknown): string {
  if (e instanceof Error) {
    const msg = e.message || String(e);
    return msg.replace(/^.*?: /, "");
  }
  return String(e);
}
