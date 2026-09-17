import "./styles.scss";

export function ToolBar(props: { 
  showDiffboard: boolean;
  onToggleDiffboard: () => void;
}) {
  return (
    <div className="toolbar">
      <button 
        className="btn toggle-diffboard" 
        onClick={props.onToggleDiffboard}
      >
        {props.showDiffboard ? "Скрыть дифф" : "Показать дифф"}
      </button>
    </div>
  );
}