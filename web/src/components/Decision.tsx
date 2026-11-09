import { formatWhen } from "../lib/when";
import { Button } from "./ui/Button";
import { decisionLabel, type DecisionState } from "../lib/status";

// One party's decision on a request that needs the teen and an admin (add
// requests and contact requests): the label and state, when it was decided,
// and Approve / Deny while that party may still decide.
export function Decision({ label, value, at, canDecide, busy, onDecide, approveLabel, denyLabel }: {
  label: string;
  value: DecisionState;
  at?: string;
  canDecide: boolean;
  busy: boolean;
  onDecide: (d: "approved" | "denied") => void;
  approveLabel: string;
  denyLabel: string;
}) {
  return <section aria-label={label} className="border-t border-border pt-3 space-y-2">
    <h3 className="font-semibold">{label}: {decisionLabel(value)}</h3>
    {at && <p className="text-sm text-text-muted">{formatWhen(at)}</p>}
    {canDecide && <div className="flex flex-wrap gap-2"><Button variant="primary" disabled={busy} onClick={() => onDecide("approved")}>{approveLabel}</Button><Button variant="danger-outline" disabled={busy} onClick={() => onDecide("denied")}>{denyLabel}</Button></div>}
  </section>;
}
