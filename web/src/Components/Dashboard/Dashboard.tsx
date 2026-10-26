// Матрица доски: строки = эпики, колонки = статусы, ячейки = задачи.
// Общий заголовок колонок, у каждой строки — эпик с названием эпика
// (клик → модалка). Клик по задаче → модалка задачи. DnD переносит задачи
// между соседними статусами внутри своего эпика.

import { useState } from "react";
import type { BoardView, BranchDiffContext, BugRow, EpicRow, TaskRow } from "@/Types";
import { STATUS_LABEL, TASK_STATUS_ORDER } from "./board";
import { fmtTokens, tokensSummary } from "./tokens";
import { EpicRow as EpicRowView } from "./EpicRow";
import { EpicModal } from "./EpicModal";
import { TaskModal } from "./TaskModal";
import { BugModal } from "./BugModal";
import "./styles.scss";

// Строка матрицы: эпик (или null для задач без эпика) + его задачи.
type Row = { epic: EpicRow | null; epicId: string; tasks: TaskRow[]; bugs: BugRow[] };

export function Dashboard({
  board,
  onTaskUpdate,
  onTaskDelete,
  onEpicDelete,
  onEpicRelease,
  onEpicCancel,
  onEpicReorder,
  onEpicBranch,
  onTaskBranch,
  onEpicMR,
  onTaskMR,
  onTaskRollback,
  onTaskReopen,
  onShowDiff,
  onTaskAutoResolve,
  onEpicAutoResolve,
  collapsed = false,
  onToggleCollapse,
}: {
  board: BoardView | null;
  onTaskUpdate: (t: TaskRow, patch: Partial<TaskRow>) => void;
  onTaskDelete?: (t: TaskRow) => void;
  onEpicDelete: (e: EpicRow) => void;
  onEpicRelease: (e: EpicRow) => Promise<void>;
  // Пересортировка эпиков (перетаскивание строк/стрелки): новый полный порядок
  // ID эпиков, первый — самый приоритетный. Сервер сохраняет приоритеты и при
  // идущей оркестрации мягко перезапускает её.
  onEpicReorder: (order: string[]) => void;
  // Отмена эпика (без отката кода — ветка остаётся).
  onEpicCancel?: (e: EpicRow) => Promise<void>;
  // Ф-5: создание ветки эпика/задачи и «Создать MR» (пушит ветку и открывает
  // MR через фордж). Кнопки в блоке «ветка + MR» модалок.
  onEpicBranch?: (e: EpicRow) => Promise<void>;
  onTaskBranch?: (t: TaskRow) => Promise<void>;
  onEpicMR?: (e: EpicRow) => Promise<void>;
  onTaskMR?: (t: TaskRow) => Promise<void>;
  // Ф-6 (этап 3): ручной откат кода задачи к "last_good"/"base"/SHA.
  onTaskRollback?: (t: TaskRow, to: string) => Promise<void>;
  // Переоткрытие выполненной задачи на доработку (done -> development) с
  // обязательным комментарием-обоснованием.
  onTaskReopen?: (t: TaskRow, body: string) => Promise<void>;
  onShowDiff?: (context: BranchDiffContext) => void;
  // Ф-9: авторезолвинг конфликтов мёрджа через LLM. Кнопка в блоке конфликта.
  onTaskAutoResolve?: (t: TaskRow) => Promise<void>;
  onEpicAutoResolve?: (e: EpicRow) => Promise<void>;
  // Свёрнутый режим: тонкая вертикальная полоска справа с кнопкой разворота
  // и метриками (эпики/задачи/баги + разбивка по статусам).
  collapsed?: boolean;
  onToggleCollapse?: () => void;
}) {
  // Открытая модалка хранится по ID, а строка/гит-статус выводятся из свежего
  // board: WS-событие (git/MR обновились) перерисует модалку без переоткрытия.
  const [epicId, setEpicId] = useState<string | null>(null);
  const [taskId, setTaskId] = useState<string | null>(null);
  const [bugId, setBugId] = useState<string | null>(null);
  // Свёрнутость эпиков: явный выбор пользователя (toggle[epicId]) перекрывает
  // значение по умолчанию (неактивные свёрнуты, активные развёрнуты).
  const [collapseToggle, setCollapseToggle] = useState<Record<string, boolean>>({});
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<string>("all");

  if (!board) {
    if (collapsed) {
      return (
        <div className="dashboard collapsed">
          <button className="expand" onClick={onToggleCollapse} title="Развернуть доску">
            «
          </button>
          <div className="dstrip hint">…</div>
        </div>
      );
    }
    return <div className="dashboard">Совет ещё не загружен…</div>;
  }

  // Свёрнутый режим: только метрики в статусной колонке, доска не рендерится.
  if (collapsed) {
    const byStatus = TASK_STATUS_ORDER.map((s) => ({
      status: s,
      count: board.tasks.filter((t) => t.status === s).length,
    }));
    // Ф-4: расход токенов доски (факт и ошибка прогноза) — одной строкой.
    const tokens = tokensSummary([...board.epics, ...board.tasks]);
    return (
      <div className="dashboard collapsed">
        <button className="expand" onClick={onToggleCollapse} title="Развернуть доску">
          «
        </button>
        <div className="dstrip">
          {tokens.spent > 0 && (
            <div
              className="st tokens"
              title={
                tokens.errorPct != null
                  ? `Токены доски: факт ${fmtTokens(tokens.spent)} · ошибка прогноза ${tokens.errorPct}% по ${tokens.measured} ед.`
                  : `Токены доски: факт ${fmtTokens(tokens.spent)}`
              }
            >
              <b>{fmtTokens(tokens.spent)}</b>
            </div>
          )}
          <div className="st epic" title={`Эпиков: ${board.epics.length}`}>
            <IconEpic />
            <b>{board.epics.length}</b>
          </div>
          <div className="st task" title={`Задач: ${board.tasks.length}`}>
            <IconTask />
            <b>{board.tasks.length}</b>
          </div>
          <div className="st bug" title={`Багов: ${board.bugs?.length ?? 0}`}>
            <IconBug />
            <b>{board.bugs?.length ?? 0}</b>
          </div>
          {byStatus.map(({ status, count }) => (
            <div
              key={status}
              className={"st " + status}
              title={`${STATUS_LABEL[status]}: ${count}`}
            >
              <b>{count}</b>
            </div>
          ))}
        </div>
      </div>
    );
  }

  // Группируем задачи по эпику; задачи с неизвестным epic_id попадают в
  // отдельные строки «без эпика», чтобы не теряться с доски.
  const byEpic = new Map<string, TaskRow[]>();
  for (const t of board.tasks) {
    const list = byEpic.get(t.epic_id) ?? [];
    list.push(t);
    byEpic.set(t.epic_id, list);
  }
  // Группируем баги по эпику, сохраняя строки багов без известного эпика.
  const byEpicBug = new Map<string, BugRow[]>();
  for (const b of board.bugs ?? []) {
    const bugList = byEpicBug.get(b.epic_id) ?? [];
    bugList.push(b);
    byEpicBug.set(b.epic_id, bugList);
  }

  const rows: Row[] = board.epics.map((e) => ({
    epic: e,
    epicId: e.task_id,
    tasks: byEpic.get(e.task_id) ?? [],
    bugs: byEpicBug.get(e.task_id) ?? [],
  }));
  for (const [epicId, tasks] of byEpic) {
    if (!board.epics.some((e) => e.task_id === epicId)) {
      rows.push({ epic: null, epicId, tasks, bugs: byEpicBug.get(epicId) ?? [] });
    }
  }
  // Баги принадлежат только существующему эпику. Не создаём для них
  // синтетические строки «без эпика».

  const normalizedQuery = query.trim().toLocaleLowerCase();
  const visibleRows = rows.map((r) => {
    const epicMatches = !!normalizedQuery && (r.epic?.title ?? "").toLocaleLowerCase().includes(normalizedQuery);
    const tasks = r.tasks.filter((t) =>
      (statusFilter === "all" || t.status === statusFilter) &&
      (!normalizedQuery || epicMatches || `${t.title} ${t.task_id} ${t.description}`.toLocaleLowerCase().includes(normalizedQuery)),
    );
    const bugs = r.bugs.filter((b) =>
      !normalizedQuery || epicMatches || `${b.title} ${b.bug_id} ${b.description}`.toLocaleLowerCase().includes(normalizedQuery),
    );
    return { ...r, tasks, bugs, epicMatches };
  }).filter((r) => {
    if (!normalizedQuery && statusFilter === "all") return true;
    return r.epicMatches || r.tasks.length > 0 || r.bugs.length > 0;
  });

  const visibleTasks = visibleRows.flatMap((r) => r.tasks);
  const statusCounts = TASK_STATUS_ORDER.map((s) => ({
    status: s,
    count: visibleTasks.filter((t) => t.status === s).length,
  }));

  // Ф-4: сводка расхода токенов по всей доске: факт, сумма прогнозов и ошибка
  // прогноза по измеренным единицам. Считается по всем эпикам и задачам, а не
  // по видимым строкам: фильтры доски не должны «прятать» расход.
  const tokens = tokensSummary([...board.epics, ...board.tasks]);

  // Эпик активен, если у него есть незавершённые задачи (draft → development);
  // только такие по умолчанию развёрнуты полностью.
  const isActive = (tasks: TaskRow[]) =>
    tasks.some(
      (t) =>
        t.status !== "done" &&
        t.status !== "cancelled",
    );

  const isCollapsed = (epicId: string, tasks: TaskRow[]) =>
    collapseToggle[epicId] ?? !isActive(tasks);

  const toggleCollapse = (epicId: string, tasks: TaskRow[]) =>
    setCollapseToggle((prev) => ({
      ...prev,
      [epicId]: !isCollapsed(epicId, tasks),
    }));

  // Пересортировка эпиков: переносим thisId на место targetId (after=false —
  // перед ним, after=true — после). Фаза остаётся первичным ключом очереди
  // лидов, поэтому перенос между фазами игнорируется (после сохранения доска
  // вернётся в эффективный порядок: фаза → приоритет → sequence_order).
  const moveEpic = (thisId: string, targetId: string, after: boolean) => {
    const eps = board.epics;
    const from = eps.findIndex((e) => e.task_id === thisId);
    const to = eps.findIndex((e) => e.task_id === targetId);
    if (from < 0 || to < 0 || from === to) {
      return;
    }
    const next = [...eps];
    next.splice(from, 1);
    const insert = next.findIndex((e) => e.task_id === targetId);
    if (insert < 0) {
      return;
    }
    next.splice(after ? insert + 1 : insert, 0, eps[from]!);
    onEpicReorder(next.map((e) => e.task_id));
  };

  // Стрелки ↑/↓: сдвиг на одну строку вверх/вниз.
  const shiftEpic = (e: EpicRow, dir: -1 | 1) => {
    const idx = board.epics.findIndex((x) => x.task_id === e.task_id);
    if (idx < 0) {
      return;
    }
    const target = board.epics[idx + dir];
    if (!target || target.task_id === e.task_id) {
      return;
    }
    moveEpic(e.task_id, target.task_id, dir === 1);
  };

  // Строка открытой модалки — из актуального снимка доски (см. выше): если
  // строка исчезла (удалили), модалка просто не рендерится.
  const epic = epicId ? board.epics.find((e) => e.task_id === epicId) ?? null : null;
  const task = taskId ? board.tasks.find((t) => t.task_id === taskId) ?? null : null;
  const bug = bugId ? (board.bugs ?? []).find((b) => b.bug_id === bugId) ?? null : null;

  return (
    <div className="dashboard" onDragOver={(e) => e.preventDefault()}>
      <div className="banner">
        <span className="project">{board.meta?.project_name ?? "—"}</span>
        {(tokens.spent > 0 || tokens.estimated > 0) && (
          <span
            className="tokens"
            title={
              tokens.errorPct != null
                ? `Факт ${fmtTokens(tokens.spent)} токенов · прогноз ${fmtTokens(tokens.estimated)} · ошибка ${tokens.errorPct}% по ${tokens.measured} ед. с записанным прогнозом`
                : `Факт ${fmtTokens(tokens.spent)} токенов · прогноз ${fmtTokens(tokens.estimated)} · ошибка прогноза пока не измерялась`
            }
          >
            токены: {fmtTokens(tokens.spent)}
            {tokens.estimated > 0 && ` (прогноз ${fmtTokens(tokens.estimated)}`}
            {tokens.estimated > 0 && tokens.errorPct != null && `, ошибка ${tokens.errorPct}%`}
            {tokens.estimated > 0 && ")"}
          </span>
        )}
        <span className="counts">
          эпиков {board.epics.length} · задач {board.tasks.length} · багов {board.bugs?.length ?? 0}
          {board.total && board.total.tasks > board.tasks.length && (
            <em> (всего задач: {board.total.tasks}, доска ограничена)</em>
          )}
        </span>
        {board.epics.length > 1 && (
          <span className="sort-hint" title="Порядок строк задаёт приоритет эпиков: выше — раньше в очереди лидов. Перенос работает внутри фазы (инфраструктура → приложение → тестирование).">
            ⟶ тащите строки (=приоритет)
          </span>
        )}
      </div>

      <div className="board-tools">
        <label className="board-search">
          <IconSearch />
          <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Найти эпик, задачу или баг" aria-label="Найти на доске" />
          {query && <button type="button" onClick={() => setQuery("")} title="Очистить поиск">×</button>}
        </label>
        <div className="status-filters" aria-label="Фильтр статуса задач">
          <button className={statusFilter === "all" ? "active" : ""} onClick={() => setStatusFilter("all")}>Все</button>
          {TASK_STATUS_ORDER.map((s) => (
            <button key={s} className={statusFilter === s ? `active ${s}` : ""} onClick={() => setStatusFilter(s)}>
              {STATUS_LABEL[s]} <span>{board.tasks.filter((t) => t.status === s).length}</span>
            </button>
          ))}
        </div>
        {(query || statusFilter !== "all") && <span className="match-count">{visibleTasks.length} задач · {visibleRows.length} эпиков</span>}
      </div>

      <div className="boardgrid">
        <div className="corner">Эпик</div>
        {statusCounts.map(({ status, count }) => (
          <div key={status} className={"grid-head " + status}>
            {STATUS_LABEL[status]} <b>{count}</b>
          </div>
        ))}
        {visibleRows.map((r) => (
          <EpicRowView
            key={r.epicId}
            epic={r.epic}
            epicId={r.epicId}
            tasks={r.tasks}
            bugs={r.bugs}
            collapsed={isCollapsed(r.epicId, r.tasks)}
            hasBranch={board.git ? !!board.git.epics?.[r.epicId]?.branch : undefined}
            onTaskUpdate={onTaskUpdate}
            onTaskOpen={(t) => setTaskId(t.task_id)}
            onBugOpen={(b) => setBugId(b.bug_id)}
            onEpicOpen={(e) => setEpicId(e.task_id)}
            onEpicDelete={onEpicDelete}
            onEpicRelease={onEpicRelease}
            onEpicCancel={onEpicCancel}
            onEpicReorder={moveEpic}
            onEpicShift={shiftEpic}
            onTaskDelete={onTaskDelete}
            onToggle={() => toggleCollapse(r.epicId, r.tasks)}
          />
        ))}
        {visibleRows.length === 0 && <div className="board-empty">Ничего не найдено. Измените запрос или фильтр.</div>}
      </div>

      {epic && (
        <EpicModal
          epic={epic}
          git={board.git}
          onCreateBranch={onEpicBranch}
          onCreateMR={onEpicMR}
          onShowDiff={onShowDiff}
          onAutoResolve={onEpicAutoResolve}
          onCancel={onEpicCancel}
          onClose={() => setEpicId(null)}
        />
      )}
      {task && (
        <TaskModal
          task={task}
          git={board.git}
          onCreateBranch={onTaskBranch}
          onCreateMR={onTaskMR}
          onShowDiff={onShowDiff}
          onAutoResolve={onTaskAutoResolve}
          onTaskUpdate={onTaskUpdate}
          onRollback={onTaskRollback}
          onReopen={onTaskReopen}
          onDelete={onTaskDelete}
          onClose={() => setTaskId(null)}
        />
      )}
      {bug && <BugModal bug={bug} onClose={() => setBugId(null)} />}
    </div>
  );
}

// Иконки метрик для свёрнутой статусной колонки (inline-SVG, без эмодзи).
function IconEpic() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 5a2 2 0 0 1 2-2h4l2 3h8a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </svg>
  );
}

function IconTask() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M9 6l3-3 3 3" />
      <path d="M5 4h2.5l3.5 3.5H12" />
      <path d="M12 9l4-2 3 3v8a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V9l3-2" />
    </svg>
  );
}

function IconBug() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <rect x="8" y="8" width="8" height="11" rx="4" />
      <path d="M12 8V5" />
      <path d="M2.5 13h4M17.5 13h4M3 20l3.5-2M21 20l-3.5-2M7.5 13a4.5 4.5 0 0 0 9 0" />
    </svg>
  );
}

function IconSearch() {
  return <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><circle cx="11" cy="11" r="7" /><path d="m20 20-4-4" /></svg>;
}
