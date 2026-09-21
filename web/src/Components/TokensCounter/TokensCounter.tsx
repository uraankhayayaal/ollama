// Счётчик токенов проекта: накопленные вход (in) и выход (out) токены за
// время жизни проекта и последняя реальная скорость генерации (tps, вых.
// ток/с — из usage провайдера, Ollama eval_count/eval_duration). Отображается
// в шапке рядом с кнопкой «Продолжить», обновляется событиями WS type=tokens
// (см. Api.projectTokens). Скорость не сбрасывается между генерациями.
import "./styles.scss";

function fmt(n: number): string {
  return n.toLocaleString("ru-RU");
}

export function TokensCounter(props: { in: number; out: number; tps?: number | null }) {
  const total = props.in + props.out;
  const speed =
    props.tps != null && Number.isFinite(props.tps) && props.tps > 0
      ? `${props.tps.toFixed(1)} ток/с`
      : null;
  return (
    <span
      className="tokens"
      title={`Токены проекта: ${fmt(props.in)} вход · ${fmt(props.out)} выход`}
    >
      <span className="tokens-in" title="Входные токены">
        вх. {fmt(props.in)}
      </span>
      <span className="tokens-out" title="Выходные токены">
        вых. {fmt(props.out)}
      </span>
      <span className="tokens-total">итого {fmt(total)}</span>
      {speed && (
        <span className="tokens-speed" title="Скорость генерации (реальная)">
          {speed}
        </span>
      )}
    </span>
  );
}