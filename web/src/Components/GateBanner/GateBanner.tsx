import { GateEvent } from "@/Types";

export function GateBanner(props: { gate: GateEvent, onDecide: (gateName: "epics" | "tasks", decision: {
    approved: boolean;
    reason?: string | undefined;
}) => Promise<void> }) {
  return (
    <div className="GateBanner">
      {props.gate.summary}
    </div>
  );
}
