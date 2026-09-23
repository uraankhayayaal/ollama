import type { BugRow } from "@/Types";
import { Modal } from "../Modal";
import "./styles.scss";

const BUG_STATUS_LABEL: Record<string, string> = {
  new: "Новый",
  confirmed: "Подтверждён",
  slop: "Отклонён",
  fix: "Требует исправления",
  fixed: "Исправлен",
  feature: "Фича",
  wont_fix: "Не исправлять",
};

export function BugModal({ bug, onClose }: { bug: BugRow; onClose: () => void }) {
  return (
    <Modal title={`Баг · ${bug.bug_id}`} onClose={onClose}>
      <dl className="bug-details">
        <div><dt>Название</dt><dd className="name">{bug.title || "—"}</dd></div>
        <div><dt>Эпик</dt><dd>{bug.epic_id || "—"}</dd></div>
        <div><dt>Статус</dt><dd>{BUG_STATUS_LABEL[bug.status] ?? bug.status}</dd></div>
        <div><dt>Автор</dt><dd>{bug.reporter_role || "—"}</dd></div>
        {bug.task_id && <div><dt>Задача</dt><dd>{bug.task_id}</dd></div>}
        {bug.verdict && <div><dt>Вердикт</dt><dd>{bug.verdict}</dd></div>}
        {bug.fix_epic_id && <div><dt>Эпик исправления</dt><dd>{bug.fix_epic_id}</dd></div>}
      </dl>
      <h4 className="bug-section">Описание</h4>
      <p className="bug-description">{bug.description || "—"}</p>
      <div className="bug-meta">Создан {bug.created_at || "—"} · обновлён {bug.updated_at || "—"}</div>
    </Modal>
  );
}
