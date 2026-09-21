// Умная кнопка управления оркестрацией. Показывает текущий статус работы и
// выполняет одно из двух действий в зависимости от него:
//   running/waiting/standby — «Стоп» (оркестрация идёт, в т.ч. ждёт HITL-затвора
//                             или работы на доске);
//   остальное                — «Продолжить» (возобновить выполнение текущей задачи).
import "./styles.scss";

// Человекочитаемые статусы сессии (зеркалят StatusEvent сервера).
const STATUS_WORD: Record<string, string> = {
  running: "выполняется",
  waiting: "ждёт решения",
  standby: "ожидает работу",
  done: "выполнено",
  stopped: "остановлено",
  error: "ошибка",
};

export function RunButton({
  status,
  canContinue,
  busy,
  onRun,
  onStop,
}: {
  status: string;
  canContinue: boolean;
  busy: boolean;
  onRun: () => void;
  onStop: () => void;
}) {
  const active = status === "running" || status === "waiting" || status === "standby";

  if (active) {
    const waiting = status === "waiting";
    const standby = status === "standby";
    return (
      <button
        className={"btn run danger " + status}
        onClick={onStop}
        title={
          waiting
            ? "Оркестрация ждёт подтверждения — остановить"
            : standby
              ? "Оркестрация ждёт появления работы — остановить"
              : "Остановить оркестрацию"
        }
      >
        <span className="pulse" />
        Стоп · {STATUS_WORD[status] ?? status}
      </button>
    );
  }

  const word = STATUS_WORD[status];
  return (
    <button
      className="btn run"
      onClick={onRun}
      disabled={!canContinue || busy}
      title={
        busy
          ? "Запуск оркестрации…"
          : canContinue
            ? "Возобновить выполнение текущей задачи"
            : "Продолжение невозможно: нет задачи или оркестрация уже идёт"
      }
    >
      {busy ? "Продолжение…" : "▷ Продолжить" + (word ? " · " + word : "")}
    </button>
  );
}