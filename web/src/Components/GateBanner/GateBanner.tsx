import { useState } from "react";
import { BoardView, GateEvent } from "@/Types";
import "./styles.scss";

const STATUS_LABEL: Record<string, string> = {
  new: "Новые",
  analysis: "В анализе",
  ready: "Готовы к работе",
  in_progress: "В работе",
  done: "Готово",
  cancelled: "Отменены",
};

// HITL-затвор: показываем решение эпиков/задач человеку. Причина
// обязательна при reject, опциональна при approve. Помимо сводки выводим
// сам список утверждаемых объектов (из текущего снимка доски): заголовок,
// исполнитель/лид и описание — чтобы человек понимал, что подтверждает.
export function GateBanner(props: {
  gate: GateEvent;
  board: BoardView | null;
  onDecide: (
    gateName: "epics" | "tasks",
    decision: { approved: boolean; reason?: string },
  ) => void;
}) {
  const [reason, setReason] = useState("");
  const [show, setShow] = useState(false);

  const decided = (approved: boolean) => {
    if (!approved && !reason.trim()) {
      setShow(true);
      return;
    }
    props.onDecide(props.gate.gate as "epics" | "tasks", {
      approved,
      reason: reason.trim() || undefined,
    });
  };

  const b = props.board;
  const epics =
    b && props.gate.gate === "epics"
      ? b.epics.filter((e) => props.gate.ids.includes(e.task_id))
      : [];
  const tasks =
    b && props.gate.gate === "tasks"
      ? b.tasks.filter((t) => props.gate.ids.includes(t.task_id))
      : [];
  const hasItems = epics.length > 0 || tasks.length > 0;

  return (
    <div className="GateBanner">
      {show ? (
        <div className="reject-panel">
          <textarea
            autoFocus
            placeholder="Почему отклоняем? (обязательно)"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
          />
          <div className="row">
            <button className="ok" disabled={!reason.trim()} onClick={() => decided(false)}>
              Отклонить
            </button>
            <button className="ghost" onClick={() => setShow(false)}>
              Назад
            </button>
          </div>
        </div>
      ) : (
        <>
          <div className="summary">{props.gate.summary}</div>
          {hasItems && (
            <ul className="items">
              {epics.map((e) => (
                <li key={e.task_id} className="item">
                  <div className="head">
                    <span className="id">{e.task_id}</span>
                    <strong>{e.title}</strong>
                    <span className={"status " + e.status}>
                      {STATUS_LABEL[e.status] ?? e.status}
                    </span>
                  </div>
                  {e.assigned_lead && <div className="sub">Лид: {e.assigned_lead}</div>}
                  {e.description && <p className="desc">{e.description}</p>}
                </li>
              ))}
              {tasks.map((t) => (
                <li key={t.task_id} className="item">
                  <div className="head">
                    <span className="id">{t.task_id}</span>
                    <strong>{t.title}</strong>
                    <span className={"status " + t.status}>
                      {STATUS_LABEL[t.status] ?? t.status}
                    </span>
                  </div>
                  <div className="sub">
                    {t.assignee ? `Исполнитель: ${t.assignee}` : "Исполнитель: —"}
                    {t.epic_id ? ` · эпик ${t.epic_id}` : ""}
                  </div>
                  {t.description && <p className="desc">{t.description}</p>}
                </li>
              ))}
            </ul>
          )}
          <div className="row">
            <button className="ok" onClick={() => decided(true)}>
              ✓ Одобрить
            </button>
            <button className="no" onClick={() => decided(false)}>
              ✗ Отклонить
            </button>
          </div>
        </>
      )}
    </div>
  );
}