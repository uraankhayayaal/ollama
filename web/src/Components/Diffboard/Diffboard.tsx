// Diffboard — вкладка «Дифф»: разница предложенного и текущего состояния.
// Канон пропсов зеркалит App:185 (project: string). REST предоставит
// diff-эндпоинт в api.ts (Ф-2) — пока панель-заглушка с подсказкой.
// Папка-канон: Components/Diffboard/{index,Diffboard,styles}.

export function Diffboard(props: { project: string }) {
  void props.project; // REST diff-канон подключится в Ф-2; пропс уже в каноне.
  return (
    <div className="diffboard">
      <p className="hint">
        Дифф предложенных изменений относительно текущего состояния появится
        здесь после того, как backend отдаст /api/projects/&lt;name&gt;/diff.
      </p>
    </div>
  );
}
