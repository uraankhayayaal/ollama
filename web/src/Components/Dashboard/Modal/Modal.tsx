// Универсальная модалка: оверлей + панель + закрытие (Esc / клик по фону /
// кнопка ×). Детали контента — у вызывающих компонентов (EpicModal,
// TaskModal).
import { useEffect } from "react";
import type { ReactNode } from "react";
import "./styles.scss";

export function Modal({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onClose();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true">
        <header>
          <h3>{title}</h3>
          <button className="close" onClick={onClose} aria-label="Закрыть">
            ×
          </button>
        </header>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}