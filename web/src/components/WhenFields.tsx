import { useId, useState } from "react";
import type { WhenState } from "../lib/useWhenState";
import { zoneTimeName } from "../lib/when";
import { Button } from "./ui/Button";
import { Field, Input } from "./ui/Field";
import { SegmentButton } from "./ui/SegmentButton";
import { ZonePicker } from "./ui/ZonePicker";
import { WeekdayPicker } from "./WeekdayPicker";

const PRESETS = [
  { minutes: 60, label: "1 hr" },
  { minutes: 90, label: "1½ hr" },
  { minutes: 120, label: "2 hr" },
];

const isPreset = (minutes: number) => PRESETS.some((p) => p.minutes === minutes);

// The When group on every event form (DEC-95, consistency rule): date, start
// time, duration presets, Just once / Every week with weekday chips, and the
// time zone as one line that opens the shared ZonePicker. The state comes from
// useWhenState; the zone stays with the form. An edit form passes the event's
// own zone, so it always names it (DEC-57).
export function WhenFields({
  idPrefix,
  when,
  tzid,
  onTzid,
  zoneHint,
  dateLabel = "Date",
  customRule,
  until,
}: {
  idPrefix: string;
  when: WhenState;
  tzid: string;
  onTzid: (v: string) => void;
  zoneHint: string;
  dateLabel?: string;
  // A series whose rule the weekday chips can't express keeps its raw rule.
  customRule?: { value: string; onChange: (v: string) => void };
  // "Repeat until", offered once the event repeats.
  until?: { value: string; onChange: (v: string) => void };
}) {
  const { date, setDate, time, setTime, duration, setDuration, recurring, setRecurring, days, toggleDay } = when;
  const headingId = useId();
  const durationId = useId();
  const repeatId = useId();
  // A length that isn't one of the presets opens on Other with its minutes.
  const [other, setOther] = useState(() => !isPreset(duration));
  const [zoneOpen, setZoneOpen] = useState(false);
  const minutesId = `${idPrefix}-duration`;
  const zoneId = `${idPrefix}-tz`;

  return (
    <div role="group" aria-labelledby={headingId} className="space-y-4">
      <h2 id={headingId} className="text-lg font-semibold">
        When
      </h2>
      <div className="grid sm:grid-cols-2 gap-4">
        <Field id={`${idPrefix}-date`} label={dateLabel}>
          <Input type="date" value={date} onChange={(e) => setDate(e.target.value)} required />
        </Field>
        <Field id={`${idPrefix}-time`} label="Start time">
          <Input type="time" value={time} onChange={(e) => setTime(e.target.value)} required />
        </Field>
      </div>
      <div role="group" aria-labelledby={durationId}>
        <p id={durationId} className="label">
          Duration
        </p>
        <div className="flex">
          {PRESETS.map((p) => (
            <SegmentButton
              key={p.minutes}
              pressed={!other && duration === p.minutes}
              onClick={() => {
                setOther(false);
                setDuration(p.minutes);
              }}
            >
              {p.label}
            </SegmentButton>
          ))}
          <SegmentButton
            pressed={other}
            onClick={() => {
              setOther(true);
              setTimeout(() => document.getElementById(minutesId)?.focus(), 0);
            }}
          >
            Other
          </SegmentButton>
        </div>
        {other && (
          <div className="mt-2">
            <Field id={minutesId} label="Duration (minutes)">
              <Input
                type="number"
                min={1}
                max={10080}
                step={1}
                value={duration}
                onChange={(e) => setDuration(Number(e.target.value))}
                layout="sm:w-44"
              />
            </Field>
          </div>
        )}
      </div>
      <div role="group" aria-labelledby={repeatId}>
        <p id={repeatId} className="label">
          Repeats
        </p>
        {customRule ? (
          <Field id={`${idPrefix}-rule`} label="Recurrence rule">
            <Input value={customRule.value} onChange={(e) => customRule.onChange(e.target.value)} />
          </Field>
        ) : (
          <div className="flex">
            <SegmentButton pressed={!recurring} onClick={() => setRecurring(false)}>
              Just once
            </SegmentButton>
            <SegmentButton pressed={recurring} onClick={() => setRecurring(true)}>
              Every week
            </SegmentButton>
          </div>
        )}
        {recurring && !customRule && (
          <div className="mt-2">
            <WeekdayPicker days={days} onToggle={toggleDay} />
          </div>
        )}
        {until && (recurring || customRule) && (
          <div className="mt-3">
            <Field id={`${idPrefix}-until`} label="Repeat until" optional>
              <Input type="date" value={until.value} onChange={(e) => until.onChange(e.target.value)} />
            </Field>
          </div>
        )}
      </div>
      {zoneOpen ? (
        <Field id={zoneId} label="Time zone" hint={zoneHint}>
          <ZonePicker value={tzid} onChange={onTzid} />
        </Field>
      ) : (
        <p className="flex items-center gap-2 text-sm">
          <span>{zoneTimeName(tzid) || "Time zone not set"}</span>
          <span aria-hidden="true" className="text-text-muted">
            ·
          </span>
          <Button
            variant="link"
            onClick={() => {
              setZoneOpen(true);
              setTimeout(() => document.getElementById(zoneId)?.focus(), 0);
            }}
          >
            Change
          </Button>
        </p>
      )}
    </div>
  );
}
