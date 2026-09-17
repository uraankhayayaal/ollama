// ToolBar — панель управления интерфейсом в верхней части сайта. Здесь живут
// кнопки навигации по вкладкам (доска/чат) и переключатели панелей, которые
// не относятся к контенту конкретного воркспейса (показ/скрытие Diffboard).
// Новые кнопки управления добавляются сюда, не засоряя header.

import "./styles.scss";

export type ToolBarView = "board" | "chat" | "diff";

export interface ToolBarProps {
  // Кнопки навигации доступны только когда открыт проект.
  hasProject: boolean;
  activeView: ToolBarView;
  onSelectView: (v: ToolBarView) => void;
  // Показ/скрытие Diffboard.
  diffVisible: boolean;
  onToggleDiff: () => void;
}

export function ToolBar(props: ToolBarProps) {
  const go = (v: ToolBarView) => () => props.onSelectView(v);

  return (
    <div className="toolbar">
      <div className="tb-group" role="group" aria-label="Вкладки">
        <button
          className={"tb-btn" + (props.activeView === "board" ? " active" : "")}
          onClick={go("board")}
          disabled={!props.hasProject}
          title="Доска задач"
        >
          Доска
        </button>
        <button
          className={"tb-btn" + (props.activeView === "chat" ? " active" : "")}
          onClick={go("chat")}
          disabled={!props.hasProject}
          title="Чат с моделью"
        >
          Чат
        </button>
      </div>

      <div className="tb-sep" />

      <div className="tb-group" role="group" aria-label="Панели">
        <button
          className={"tb-btn toggle-diff" + (props.diffVisible ? " active" : "")}
          onClick={props.onToggleDiff}
          disabled={!props.hasProject}
          title="Показать или скрыть дифф-панель"
        >
          {props.diffVisible ? "Скрыть дифф" : "Показать дифф"}
        </button>
      </div>
    </div>
  );
}