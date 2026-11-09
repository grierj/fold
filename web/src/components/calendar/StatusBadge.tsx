import type { CalendarState } from "../../lib/calendarState";
import { registrationStatusLabel } from "../../lib/status";
import { CheckIcon, ClockIcon } from "../icons";

// The viewer's answer as quiet text, not a pill (DEC-99; DEC-71 keeps the
// words, and the dashed card shape on ghosted rows carries the state too, so
// it is never color alone). Going is the one answer with a check and the
// success tone. Going cards use a quiet tint, never a solid primary fill.
export function StatusBadge({ state }: { state: CalendarState }) {
  if (!state) return null;
  const label = registrationStatusLabel(state);
  const tone = state === "going" ? "text-success-on-subtle" : "text-text-muted";
  return (
    <span className={`inline-flex items-center gap-1 text-xs font-semibold ${tone}`} aria-label={label}>
      {state === "going" && <CheckIcon className="h-3 w-3" />}
      {state === "waitlisted" && <ClockIcon className="h-3 w-3" />}
      {label}
    </span>
  );
}
