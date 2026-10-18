import { useId } from "react";
import { WEEKDAYS } from "../lib/eventForm";
import { Chip } from "./ui/Chip";

// The days a weekly series repeats on, as toggle chips: the same picker on
// every create and edit form (DEC-65).
export function WeekdayPicker({ days, onToggle }: { days: string[]; onToggle: (value: string) => void }) {
  const id = useId();
  return (
    <div role="group" aria-labelledby={id}>
      <p id={id} className="label">
        Repeat on
      </p>
      <div className="flex flex-wrap gap-2">
        {WEEKDAYS.map((w) => (
          <Chip key={w.value} selected={days.includes(w.value)} onClick={() => onToggle(w.value)}>
            {w.label}
          </Chip>
        ))}
      </div>
    </div>
  );
}
