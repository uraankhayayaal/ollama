// Модальное окно с подробной информацией о задаче (клик по задаче — ячейка
// доски). Переходы статуса — рядом с эпиком в заголовке: минус/плюс.
// Ф-5: блок «ветка + MR» — ветка/MR, кнопки «Создать ветку задачи» (от ветки
// эпика) и «Создать MR» (push → MR в ветку эпика).
import { useState } from "react";
import type { AgentState, BranchDiffContext, GitView, TaskRow } from "@/Types";
import { MOVES, STATUS_LABEL } from "../board";
import { fmtTokens, tokenTitle } from "../tokens";
import { Modal } from "../Modal";
import { GitBlock } from "../GitBlock";
import "./styles.scss";

// Ф-6 State Tracking: чем агент занят прямо сейчас.
const AGENT_STATE_LABEL: Record<AgentState, string> = {
  writing_code: "пишет код",
  running_tests: "гоняет проверки",
  fixing_errors: "чинит ошибки",
  idle: "без изменений",
};

// SHA в UI показываем укороченно — полный в подсказке по наведению.
const shortSha = (sha?: string) => (sha ? sha.slice(0, 7) : "—");

export function TaskModal({
  task,
  git,
  onCreateBranch,
  onCreateMR,
  onShowDiff,
  onAutoResolve,
  onTaskUpdate,
  onRollback,
  onClose,
}: {
  task: TaskRow;
  git?: GitView;
  onCreateBranch?: (t: TaskRow) => Promise<void>;
  onCreateMR?: (t: TaskRow) => Promise<void>;
  onShowDiff?: (context: BranchDiffContext) => void;
  onAutoResolve?: (t: TaskRow) => Promise<void>;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onRollback?: (t: TaskRow, to: string) => Promise<void>;
  onClose: () => void;
}) {
  const m = MOVES[task.status] ?? { prev: null, next: null };
  const taskGit = git?.tasks?.[task.task_id];
  const taskBranch = taskGit?.branch;
  // Ф-6 (5.3): откат — только пока рабочая копия задачи жива; сабмодули
  // возвращаются к состоянию на момент откатываемого коммита, и это стоит
  // сказать ДО подтверждения, а не после.
  const canRollback = !!taskGit?.worktree;
  const [resolving, setResolving] = useState(false);
  const [resolveErr, setResolveErr] = useState("");
  // Ф-6 (этап 3): ручной откат кода. Двухшаговое подтверждение вместо
  // window.confirm — откат необратим в рабочем дереве, и решение должен
  // принимать человек, глядя на задачу.
  const [rollbackTo, setRollbackTo] = useState("");
  const [rolling, setRolling] = useState(false);
  const [rollbackErr, setRollbackErr] = useState("");

  const rollback = (to: string) => {
    setRolling(true);
    setRollbackErr("");
    onRollback!(task, to)
      .then(() => {
        setRollbackTo("");
        onClose();
      })
      .catch((e) => {
        setRollbackErr(e instanceof Error && e.message ? e.message : String(e));
        setRolling(false);
      });
  };

  return (
    <Modal title={"Задача · " + task.task_id} onClose={onClose}>
      <dl className="details">
        <div>
          <dt>Название</dt>
          <dd className="name">{task.title}</dd>
        </div>
        <div>
          <dt>Эпик</dt>
          <dd>{task.epic_id || "—"}</dd>
        </div>
        <div>
          <dt>Статус</dt>
          <dd className={"status " + task.status}>{STATUS_LABEL[task.status] ?? task.status}</dd>
        </div>
        {(task.merge_conflict_files ?? []).length > 0 && (
          <div className="merge-conflict-row">
            <dt>Конфликт мёрджа</dt>
            <dd className="merge-conflict">
              Ветка не влилась в релиз эпика: {(task.merge_conflict_files ?? []).join(", ")}.
              <br />Нужен резолв (ResolveGitConflicts / ручной rebase), затем повторить мёрдж.
              {onAutoResolve && (
                <>
                  <br />
                  <button
                    className="merge-resolve-btn"
                    disabled={resolving}
                    onClick={() => {
                      setResolving(true);
                      setResolveErr("");
                      onAutoResolve(task)
                        .then(() => {
                          onClose();
                        })
                        .catch((e) => {
                          setResolveErr(e instanceof Error && e.message ? e.message : String(e));
                        })
                        .finally(() => setResolving(false));
                    }}
                  >
                    {resolving ? "Решаю…" : "Авто-резолв (LLM)"}
                  </button>
                  {resolveErr && <span className="merge-resolve-err">{resolveErr}</span>}
                </>
              )}
            </dd>
          </div>
        )}
        {/* Ф-4: расход токенов задачи — прогноз и (после выполнения) факт. */}
        {(task.tokens_total || task.token_estimate) && (
          <div>
            <dt>Токены</dt>
            <dd title={tokenTitle(task)}>
              {fmtTokens(task.tokens_total ?? 0)} факт
              {task.token_estimate ? ` · ≈${fmtTokens(task.token_estimate)} прогноз` : ""}
            </dd>
          </div>
        )}
        <div>
          <dt>Исполнитель</dt>
          <dd>{task.assignee || "—"}</dd>
        </div>
        {/* Ф-6 State Tracking: живое состояние прогона переживает рестарт
            сервера (данные на доске), поэтому видно, что агент делает и когда
            он последний раз работал. */}
        {(task.agent_state || task.checkpoint || task.heartbeat_at) && (
          <div>
            <dt>Состояние прогона</dt>
            <dd>
              {task.agent_state
                ? AGENT_STATE_LABEL[task.agent_state] ?? task.agent_state
                : "—"}
              {task.active_agent ? ` · ${task.active_agent}` : ""}
              {task.attempts ? ` · попыток: ${task.attempts}` : ""}
              {task.heartbeat_at ? ` · пульс ${task.heartbeat_at}` : ""}
              {task.checkpoint && (
                <div className="checkpoint" title="base / последний коммит / последняя рабочая точка">
                  base {shortSha(task.checkpoint.base_sha)} · last{" "}
                  {shortSha(task.checkpoint.last_sha)} · good {shortSha(task.checkpoint.last_good_sha)}
                </div>
              )}
            </dd>
          </div>
        )}
        {task.last_error && (
          <div>
            <dt>Ошибка проверки</dt>
            <dd className="last-error">{task.last_error}</dd>
          </div>
        )}
        {/* Ф-6 (этап 3): откат кода задачи к опорной точке git-истории.
            Кнопки есть только когда есть что откатывать (чекпойнт снят при
            старте задачи) и есть куда (worktree = ветка задачи). */}
        {onRollback && task.checkpoint && !canRollback && (
          <div>
            <dt>Откат кода</dt>
            <dd className="rollback-note">
              Недоступен: рабочая копия задачи удалена (задача выполнена или среда
              пересоздана). Откат возможен только у задачи в работе.
            </dd>
          </div>
        )}
        {onRollback && task.checkpoint && canRollback && (
          <div>
            <dt>Откат кода</dt>
            <dd className="rollback">
              {rollbackTo ? (
                <>
                  <span className="rollback-warn">
                    Откатить {task.task_id} к {rollbackTo === "base" ? shortSha(task.checkpoint.base_sha)
                      : rollbackTo === "last_good" ? shortSha(task.checkpoint.last_good_sha)
                      : rollbackTo}? Незакоммиченные правки будут потеряны, задача вернётся в очередь
                    {(taskGit?.submodules ?? []).length > 0 &&
                      `, а вложенные сабмодули (${(taskGit?.submodules ?? []).join(", ")}) вернутся к состоянию на этом коммите`}
                    .
                  </span>
                  <span className="rollback-confirm">
                    <button disabled={rolling} onClick={() => rollback(rollbackTo)}>
                      {rolling ? "Откатываю…" : "Да, откатить"}
                    </button>
                    <button className="rollback-cancel" disabled={rolling} onClick={() => setRollbackTo("")}>
                      Отмена
                    </button>
                  </span>
                </>
              ) : (
                <span className="rollback-choices">
                  <button
                    disabled={!task.checkpoint.last_good_sha || rolling}
                    title={task.checkpoint.last_good_sha
                      ? `Вернуться к последнему раунду, прошедшему проверку (${shortSha(task.checkpoint.last_good_sha)})`
                      : "Не было раунда, прошедшего проверку"}
                    onClick={() => setRollbackTo("last_good")}
                  >
                    К последней рабочей
                  </button>
                  <button
                    disabled={!task.checkpoint.base_sha || rolling}
                    title={task.checkpoint.base_sha
                      ? `Снести все правки задачи: вернуться к состоянию на старте (${shortSha(task.checkpoint.base_sha)})`
                      : "База задачи неизвестна"}
                    onClick={() => setRollbackTo("base")}
                  >
                    Снести всё
                  </button>
                </span>
              )}
              {rollbackErr && <span className="rollback-err">{rollbackErr}</span>}
            </dd>
          </div>
        )}
        <div>
          <dt>Порядок</dt>
          <dd>{task.sequence_order}</dd>
        </div>
        {(task.dependencies ?? []).length > 0 && (
          <div>
            <dt>Зависимости</dt>
            <dd>{task.dependencies.join(", ")}</dd>
          </div>
        )}
      </dl>

      {git && (
        <GitBlock
          git={git}
          kind="task"
          id={task.task_id}
          epicId={task.epic_id}
          onCreateBranch={onCreateBranch ? () => onCreateBranch(task) : undefined}
          onCreateMR={onCreateMR ? () => onCreateMR(task) : undefined}
          onShowDiff={onShowDiff && taskBranch ? () => onShowDiff({
            ref: taskBranch,
            vs: git.base || "main",
            label: `Задача · ${task.task_id} · ${task.title}`,
          }) : undefined}
        />
      )}

      <h4 className="section">Описание</h4>
      <p className="desc">{task.description || "—"}</p>

      <div className="actions">
        {m.prev && <button onClick={() => onTaskUpdate(task, { status: m.prev! })}>← {STATUS_LABEL[m.prev]}</button>}
        {m.next && <button onClick={() => onTaskUpdate(task, { status: m.next! })}>{STATUS_LABEL[m.next]} →</button>}
      </div>

      <div className="meta">
        создан {task.created_at} · обновлён {task.updated_at}
      </div>
    </Modal>
  );
}
