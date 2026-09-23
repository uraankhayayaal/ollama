// Карточка бага: аналог TaskCard для багрепортов. Клик → модалка. Статусы
// багов идут по своей цепочке (new/confirmed/slop/fix/fixed) и не миксуются
// со статусами задач. Не draggable — баги переключаются только из модалки.
import type { BugRow } from "@/Types";
import "./styles.scss";

export function BugCard({
  bug,
  onOpen,
}: {
  bug: BugRow;
  onOpen: () => void;
}) {
  const badgeColor = (() => {
    switch (bug.status) {
      case "new": return "--st-todo";
      case "confirmed": return "--st-analysis";
      case "slop": return "--muted";
      case "fix": return "--st-ready";
      case "fixed": return "--st-done";
      default: return "--muted";
    }
  })();

  return (
    <li
      className="bug"
      onClick={onOpen}
    >
      <div className="title">{bug.title}</div>
      <div className={`dot ${badgeColor}`} />
      <span className={"tag " + bug.status}>{bug.status}</span>
    </li>
  );
}
