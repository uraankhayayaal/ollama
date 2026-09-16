import { useState } from "react";
import { GateEvent } from "@/Types";
import "./styles.scss";

// HITL-затвор: показываем решение эпиков/задач человеку. Причина
// обязательна при reject, опциональна при approve.
export function GateBanner(props: {
  gate: GateEvent;
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