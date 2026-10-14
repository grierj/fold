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
  // Native selects clip a long label without warning; the tooltip shows the
  // whole label and the IANA id.
  const selected = options.find((z) => z.value === value);
  return (
    <select
      id={id}
      // px-3 on phones: the tagged labels need the room; the font stays 16px
      // so iOS does not zoom on focus.
      className="input !px-3 sm:!px-4 !text-base"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      required
      aria-describedby={describedBy}
      aria-label={ariaLabel}
      title={selected ? `${selected.label} · ${selected.value}` : undefined}
    >
      {!value && <option value="">Loading…</option>}
      {options.map((z) => (
        <option key={z.value} value={z.value} title={z.value}>
          {z.label}
        </option>
      ))}
    </select>
  );
}
