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
  forChurchSetting,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  describedBy?: string;
  ariaLabel?: string;
  // The admin's church-zone setting: the field *is* the church zone, so no
  // separate "Church time zone" entry.
  forChurchSetting?: boolean;
}) {
  const church = useChurchZone();
  const options = zoneSelectOptions(forChurchSetting ? null : church, browserTimeZone(), value);
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
