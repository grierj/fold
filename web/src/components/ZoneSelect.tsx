import { zoneSelectOptions } from "../lib/eventForm";
import { useChurchZone } from "../lib/useEventZone";
import { browserTimeZone } from "../lib/wall";

// The one time zone picker for every event create and edit form. A real
// <select>, not an <input list>: a datalist only shows options that match
// what's already typed, so a filled-in zone hid every other choice,
// including "Your time zone".
export function ZoneSelect({
  id,
  value,
  onChange,
  describedBy,
  ariaLabel,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  describedBy?: string;
  ariaLabel?: string;
}) {
  const options = zoneSelectOptions(useChurchZone(), browserTimeZone(), value);
  return (
    <select
      id={id}
      className="input"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      required
      aria-describedby={describedBy}
      aria-label={ariaLabel}
    >
      {!value && <option value="">Loading…</option>}
      {options.map((z) => (
        <option key={z.value} value={z.value}>
          {z.label}
        </option>
      ))}
    </select>
  );
}
