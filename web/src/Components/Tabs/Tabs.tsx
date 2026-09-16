import { Dispatch, ReactNode, SetStateAction } from "react";

export function Tabs(props: { tab: ReactNode, setTab: Dispatch<SetStateAction<"board" | "chat" | "diff">> }) {
  return (
    <div className="tabs">
      {props.tab}
    </div>
  );
}
