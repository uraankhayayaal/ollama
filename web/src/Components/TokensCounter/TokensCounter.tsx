// Счётчик токенов проекта: накопленные вход (in) и выход (out) токены за
// время жизни проекта. Отображается в шапке рядом с кнопкой «Продолжить»,
// обновляется в реальном времени событиями WS type=tokens (см. Api.projectTokens).
import "./styles.scss";

function fmt(n: number): string {
  return n.toLocaleString("ru-RU");
}

export function TokensCounter(props: { in: number; out: number }) {
  const total = props.in + props.out;
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
    </span>
  );
}