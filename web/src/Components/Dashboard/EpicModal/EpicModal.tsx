// Мода окно с подробной информацией об эпике (клик по названию эпика в
// заголовке строки доски).
import type { EpicRow } from "@/Types";
import { STATUS_LABEL } from "../board";
import { Modal } from "../Modal";
import "./styles.scss";

export function EpicModal({ epic, onClose }: { epic: EpicRow; onClose: () => void }) {
  return (
    <Modal title={"Эпик · " + epic.task_id} onClose={onClose}>
      <dl className="details">
        <div>
          <dt>Название</dt>
          <dd className="name">{epic.title}</dd>
        </div>
        <div>
          <dt>Статус</dt>
          <dd className={"status " + epic.status}>{STATUS_LABEL[epic.status] ?? epic.status}</dd>
        </div>
        <div>
          <dt>Проект</dt>
          <dd>{epic.project_name}</dd>
        </div>
        <div>
          <dt>Лид</dt>
          <dd>{epic.assigned_role || "—"}</dd>
        </div>
        <div>
          <dt>Порядок</dt>
          <dd>{epic.sequence_order}</dd>
        </div>
        {(epic.dependencies ?? []).length > 0 && (
          <div>
            <dt>Зависимости</dt>
            <dd>{epic.dependencies.join(", ")}</dd>
          </div>
        )}
      </dl>

      <h4 className="section">Описание</h4>
      <p className="desc">{epic.description || "—"}</p>

      {epic.architecture_summary && (
        <>
          <h4 className="section">Архитектура</h4>
          <pre className="arch">{epic.architecture_summary}</pre>
        </>
      )}

      <div className="meta">
        создан {epic.created_at} · обновлён {epic.updated_at}
      </div>
    </Modal>
  );
}