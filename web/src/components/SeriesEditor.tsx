import { Form } from "./ui/Form";
import { childcareBody, childcareFrom } from "./ChildcareField";
import { useRef, useState } from "react";
import { Events } from "../api/events";
import { bydays, isSimpleWeekly, isWeekly, weekdayCode, weeklyRule } from "../lib/rrule";
import { useDirty } from "../lib/useDirty";
import { useUnsavedChanges } from "../lib/unsavedChanges";
import { useWhenState } from "../lib/useWhenState";
import { Field, FormError, Input, Textarea, showFieldErrors } from "./ui/Field";
import { FormActions } from "./ui/FormActions";
import { Button } from "./ui/Button";
import { EventOptions } from "./EventOptions";
import { DEFAULT_REMINDER_MINUTES } from "./ReminderField";
import { WhenFields } from "./WhenFields";
import { RoomField } from "./RoomField";
import { SeriesRoomField } from "./SeriesRoomField";
import { seatsFrom } from "./SeatsField";
import { videoBody } from "./VideoField";
import { remoteBody, remoteFrom } from "./RemoteField";
import { errorText } from "../lib/errors";
import { optionFieldOfError, validateEventOptions, type EventOptionValues, type OptionErrors } from "./eventOptionValues";
import type { EventInput, VideoProvider, VideoReveal } from "../types";


export interface SeriesFields {
  eventId: number;
  startsLocal: string;
  durationMinutes: number;
  tzid: string;
  title: string;
  description: string;
  location: string;
  recurrenceRule: string;
  rruleUntil?: string;
  capacity: number | null;
  waitlistEnabled: boolean;
  guestsAllowed: number;
  // The reminder time (M3.6b); absent reads as the default.
  reminderEmailMinutes?: number;
  // The push reminder switch (M3.13d3); absent reads as on.
  reminderPush?: boolean;
  videoProvider?: VideoProvider;
  videoUrl?: string;
  videoReveal?: VideoReveal;
  childcareAvailable?: boolean;
  childcareCapacity?: number | null;
  childcareAges?: string;
  childcareNotes?: string;
  remoteAvailable?: boolean;
  remoteCapacity?: number | null;
}

// SeriesEditor is the one "Edit series" / "Edit event" form, used on the
// group page and the event page (consistency rule, AGENTS.md).
export function SeriesEditor({
  series: instance,
  idPrefix,
  locationHint,
  roomDate,
  onSaved,
  onClose,
  onRoomChanged,
}: {
  series: SeriesFields;
  idPrefix: string;
  locationHint?: string;
  // The original date of a single-date event's one occurrence. It turns on the
  // one-date Room control (M2.1b); a series gets the whole-series one (M2.1c).
  roomDate?: string;
  onSaved: () => void;
  onClose?: () => void;
  // The room was booked or released (its own action, not part of Save).
  onRoomChanged?: () => void;
}) {
  const simple = isSimpleWeekly(instance.recurrenceRule);
  const when = useWhenState({
    date: instance.startsLocal.slice(0, 10),
    time: instance.startsLocal.slice(11, 16) || "19:00",
    duration: instance.durationMinutes,
    recurring: instance.recurrenceRule !== "",
    days: (() => {
      const picked = bydays(instance.recurrenceRule);
      if (picked.length > 0) return picked;
      if (isWeekly(instance.recurrenceRule)) return [weekdayCode(instance.startsLocal.slice(0, 10))];
      return [];
    })(),
  });
  const { date, time, duration, recurring, days } = when;
  const [tzid, setTzid] = useState(instance.tzid);
  const [title, setTitle] = useState(instance.title);
  const [description, setDescription] = useState(instance.description);
  const [location, setLocation] = useState(instance.location);
  const [rule, setRule] = useState(instance.recurrenceRule);
  const [until, setUntil] = useState(instance.rruleUntil ?? "");
  const initial: EventOptionValues = {
    seats: seatsFrom(instance.capacity, instance.waitlistEnabled, instance.guestsAllowed),
    video: {
      provider: instance.videoProvider ?? "none",
      url: instance.videoUrl ?? "",
      reveal: instance.videoReveal ?? "registered_only",
    },
    childcare: childcareFrom(instance.childcareAvailable, instance.childcareCapacity, instance.childcareAges, instance.childcareNotes),
    remote: remoteFrom(instance.remoteAvailable, instance.remoteCapacity),
    reminder: instance.reminderEmailMinutes ?? DEFAULT_REMINDER_MINUTES,
    reminderPush: instance.reminderPush ?? true,
  };
  const [options, setOptions] = useState(initial);
  // Open on edit, so what is already set is in view (DEC-95).
  const [moreOpen, setMoreOpen] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [optionErrors, setOptionErrors] = useState<OptionErrors>({});
  const root = useRef<HTMLFormElement>(null);

  const dirty = useDirty({ date, time, duration, recurring, days, tzid, title, description, location, rule, until, options });
  const { guard, markSaved } = useUnsavedChanges(dirty);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (simple && recurring && days.length === 0) {
      setError("Pick at least one day to repeat weekly.");
      return;
    }
    const problem = validateEventOptions(options);
    if (problem) {
      showFieldErrors(root.current, () => {
        setError(problem.message);
        setOptionErrors({ [problem.field]: problem.message });
        setMoreOpen(true);
      });
      return;
    }
    setOptionErrors({});
    setBusy(true);
    setError(null);
    const recurrenceRule = simple ? (recurring ? weeklyRule(days) : "") : rule.trim();
    const { seats, video, childcare, remote, reminder, reminderPush } = options;
    const capTrimmed = seats.capacity.trim();
    const capInitial = initial.seats.capacity;
    // Only send capacity fields that changed: on the group page the
    // initial capacity is the date's effective value, and an untouched
    // field must not clobber a per-date override.
    const body: EventInput = {
      title: title.trim(),
      description: description.trim(),
      startsLocal: `${date}T${time}`,
      durationMinutes: duration,
      tzid: tzid.trim(),
      location: location.trim(),
      recurrenceRule,
      rruleUntil: until,
    };
    if (JSON.stringify(childcare) !== JSON.stringify(initial.childcare)) Object.assign(body, childcareBody(childcare));
    if (capTrimmed !== capInitial) body.capacity = capTrimmed === "" ? null : Number(capTrimmed);
    if (seats.waitlist !== instance.waitlistEnabled) body.waitlistEnabled = seats.waitlist;
    if (seats.guests !== instance.guestsAllowed) body.guestsAllowed = seats.guests;
    if (reminder !== initial.reminder) body.reminderEmailMinutes = reminder;
    if (reminderPush !== initial.reminderPush) body.reminderPush = reminderPush;
    // Same only-when-changed rule as capacity: an untouched remote cap
    // on the group page must not clobber a per-date override.
    if (remote.available !== initial.remote.available || remote.capacity.trim() !== initial.remote.capacity) {
      Object.assign(body, remoteBody(remote));
    }
    if (
      video.provider !== initial.video.provider ||
      video.url.trim() !== initial.video.url ||
      video.reveal !== initial.video.reveal
    ) {
      Object.assign(body, videoBody(video));
    }
    try {
      await Events.update(instance.eventId, body);
      markSaved();
      onSaved();
    } catch (err) {
      const msg = errorText(err, "We couldn't update the series.");
      const field = optionFieldOfError(err);
      if (field) {
        showFieldErrors(root.current, () => {
          setError(msg);
          setOptionErrors({ [field]: msg });
          setMoreOpen(true);
        });
      } else {
        setError(msg);
      }
      setBusy(false);
    }
  }

  return (
    <Form ref={root} noValidate onSubmit={save} className="mt-3 space-y-4 border-t border-border pt-3">
      <p className="text-sm text-text-muted">
        This changes every future date that has not been edited on its own. Customized dates stay as they are. Dates
        the new schedule drops are cancelled, and people who were going or maybe are told.
      </p>
      {guard}
      {error && <FormError>{error}</FormError>}
      <Field id={`s-title-${idPrefix}`} label="Title">
        <Input value={title} onChange={(e) => setTitle(e.target.value)} />
      </Field>
      <Field id={`s-desc-${idPrefix}`} label="Description" optional>
        <Textarea
          rows={3}
          maxLength={5000}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
      <WhenFields
        idPrefix={`s-${idPrefix}`}
        when={when}
        tzid={tzid}
        onTzid={setTzid}
        zoneHint="The time zone the times above are in. The clock stays put across daylight saving."
        dateLabel="First date"
        customRule={simple ? undefined : { value: rule, onChange: setRule }}
        until={{ value: until, onChange: setUntil }}
      />
      <Field id={`s-loc-${idPrefix}`} label="Location" optional hint={locationHint}>
        <Input value={location} onChange={(e) => setLocation(e.target.value)} />
      </Field>
      {instance.recurrenceRule !== "" && <SeriesRoomField eventId={instance.eventId} idPrefix={idPrefix} onChanged={onRoomChanged} />}
      {roomDate && instance.recurrenceRule === "" && (
        <RoomField eventId={instance.eventId} date={roomDate} idPrefix={idPrefix} onChanged={onRoomChanged} />
      )}
      <EventOptions
        idPrefix={idPrefix}
        open={moreOpen}
        onOpenChange={setMoreOpen}
        values={options}
        onChange={setOptions}
        errors={optionErrors}
      />
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Saving…" : "Save series"}
        </Button>
        {onClose && (
          <Button variant="secondary" layout="w-full sm:w-auto" onClick={onClose}>
            Close
          </Button>
        )}
      </FormActions>
    </Form>
  );
}
