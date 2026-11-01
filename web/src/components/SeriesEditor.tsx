import { Form } from "./ui/Form";
import { childcareBody, childcareFrom, childcareProblem, focusChildcareCapacity, isChildcareCapacityError, type ChildcareValue } from "./ChildcareField";
import { useState } from "react";
import { Events } from "../api";
import { bydays, isSimpleWeekly, weekdayCode } from "../lib/eventForm";
import { errorText } from "../lib/errors";
import { useDirty } from "../lib/useDirty";
import { useUnsavedChanges } from "../lib/unsavedChanges";
import { useWhenState } from "../lib/useWhenState";
import { Field, FormError, Input, Textarea } from "./ui/Field";
import { FormActions } from "./ui/FormActions";
import { Button } from "./ui/Button";
import { EventOptions } from "./EventOptions";
import { DEFAULT_REMINDER_MINUTES } from "./ReminderField";
import { WhenFields } from "./WhenFields";
import { RoomField } from "./RoomField";
import { SeriesRoomField } from "./SeriesRoomField";
import { focusSeatLimit, isSeatsError, seatsFrom, seatsProblem, type SeatsValue } from "./SeatsField";
import { focusVideoUrl, isVideoError, videoProblem, type VideoValue } from "./VideoField";
import { focusRemoteCapacity, isRemoteError, remoteBody, remoteFrom, remoteProblem, type RemoteValue } from "./RemoteField";
import type { VideoProvider, VideoReveal } from "../types";


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
      if (instance.recurrenceRule.startsWith("FREQ=WEEKLY")) return [weekdayCode(instance.startsLocal.slice(0, 10))];
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
  const initialSeats = seatsFrom(instance.capacity, instance.waitlistEnabled, instance.guestsAllowed);
  const [seats, setSeats] = useState<SeatsValue>(initialSeats);
  const initialReminder = instance.reminderEmailMinutes ?? DEFAULT_REMINDER_MINUTES;
  const [reminder, setReminder] = useState(initialReminder);
  const initialReminderPush = instance.reminderPush ?? true;
  const [reminderPush, setReminderPush] = useState(initialReminderPush);
  const initialVideo: VideoValue = {
    provider: instance.videoProvider ?? "none",
    url: instance.videoUrl ?? "",
    reveal: instance.videoReveal ?? "registered_only",
  };
  const [video, setVideo] = useState<VideoValue>(initialVideo);
  const initialChildcare = childcareFrom(instance.childcareAvailable, instance.childcareCapacity, instance.childcareAges, instance.childcareNotes);
  const [childcare, setChildcare] = useState<ChildcareValue>(initialChildcare);
  const initialRemote = remoteFrom(instance.remoteAvailable, instance.remoteCapacity);
  const [remote, setRemote] = useState<RemoteValue>(initialRemote);
  // Open on edit, so what is already set is in view (DEC-95).
  const [moreOpen, setMoreOpen] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [seatsError, setSeatsError] = useState<string | null>(null);
  const [videoError, setVideoError] = useState<string | null>(null);
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const [remoteError, setRemoteError] = useState<string | null>(null);

  const dirty = useDirty({ date, time, duration, recurring, days, tzid, title, description, location, rule, until, seats, video, childcare, remote, reminder, reminderPush });
  const { guard, markSaved } = useUnsavedChanges(dirty);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (simple && recurring && days.length === 0) {
      setError("Pick at least one day to repeat weekly.");
      return;
    }
    const seatsMsg = seatsProblem(seats);
    setSeatsError(seatsMsg);
    if (seatsMsg) {
      setError(seatsMsg);
      setMoreOpen(true);
      focusSeatLimit(idPrefix);
      return;
    }
    const videoMsg = videoProblem(video);
    setVideoError(videoMsg);
    if (videoMsg) {
      setError(videoMsg);
      setMoreOpen(true);
      focusVideoUrl(idPrefix);
      return;
    }
    const childcareMsg = childcareProblem(childcare);
    setChildcareError(childcareMsg);
    if (childcareMsg) {
      setError(childcareMsg);
      setMoreOpen(true);
      focusChildcareCapacity(idPrefix);
      return;
    }
    const remoteMsg = remoteProblem(remote);
    setRemoteError(remoteMsg);
    if (remoteMsg) {
      setError(remoteMsg);
      setMoreOpen(true);
      focusRemoteCapacity(idPrefix);
      return;
    }
    setBusy(true);
    setError(null);
    const recurrenceRule = simple ? (recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "") : rule.trim();
    const capTrimmed = seats.capacity.trim();
    const capInitial = initialSeats.capacity;
    // Only send capacity fields that changed: on the group page the
    // initial capacity is the date's effective value, and an untouched
    // field must not clobber a per-date override.
    const body: {
      title: string;
      description: string;
      startsLocal: string;
      durationMinutes: number;
      tzid: string;
      location: string;
      recurrenceRule: string;
      rruleUntil: string;
      capacity?: number | null;
      waitlistEnabled?: boolean;
      guestsAllowed?: number;
      childcareAvailable?: boolean;
      childcareCapacity?: number | null;
      childcareAges?: string;
      childcareNotes?: string;
      remoteAvailable?: boolean;
      remoteCapacity?: number | null;
      videoProvider?: VideoProvider;
      videoUrl?: string;
      videoReveal?: VideoReveal;
      reminderEmailMinutes?: number;
      reminderPush?: boolean;
    } = {
      title: title.trim(),
      description: description.trim(),
      startsLocal: `${date}T${time}`,
      durationMinutes: duration,
      tzid: tzid.trim(),
      location: location.trim(),
      recurrenceRule,
      rruleUntil: until,
    };
    if (JSON.stringify(childcare) !== JSON.stringify(initialChildcare)) Object.assign(body, childcareBody(childcare));
    if (capTrimmed !== capInitial) body.capacity = capTrimmed === "" ? null : Number(capTrimmed);
    if (seats.waitlist !== instance.waitlistEnabled) body.waitlistEnabled = seats.waitlist;
    if (seats.guests !== instance.guestsAllowed) body.guestsAllowed = seats.guests;
    if (reminder !== initialReminder) body.reminderEmailMinutes = reminder;
    if (reminderPush !== initialReminderPush) body.reminderPush = reminderPush;
    // Same only-when-changed rule as capacity: an untouched remote cap
    // on the group page must not clobber a per-date override.
    if (remote.available !== initialRemote.available || remote.capacity.trim() !== initialRemote.capacity) {
      Object.assign(body, remoteBody(remote));
    }
    if (
      video.provider !== initialVideo.provider ||
      video.url.trim() !== initialVideo.url ||
      video.reveal !== initialVideo.reveal
    ) {
      body.videoProvider = video.provider;
      body.videoUrl = video.provider === "none" ? "" : video.url.trim();
      body.videoReveal = video.reveal;
    }
    try {
      await Events.update(instance.eventId, body);
      markSaved();
      onSaved();
    } catch (err) {
      const msg = errorText(err, "We couldn't update the series.");
      setError(msg);
      if (isSeatsError(err) || isChildcareCapacityError(err) || isRemoteError(err) || isVideoError(err)) setMoreOpen(true);
      if (isSeatsError(err)) { setSeatsError(msg); focusSeatLimit(idPrefix); }
      if (isChildcareCapacityError(err)) { setChildcareError(msg); focusChildcareCapacity(idPrefix); }
      if (isRemoteError(err)) {
        setRemoteError(msg);
        focusRemoteCapacity(idPrefix);
      }
      if (isVideoError(err)) {
        setVideoError(msg);
        focusVideoUrl(idPrefix);
      }
      setBusy(false);
    }
  }

  return (
    <Form noValidate onSubmit={save} className="mt-3 space-y-4 border-t border-border pt-3">
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
        seats={seats}
        onSeats={setSeats}
        seatsError={seatsError}
        video={video}
        onVideo={setVideo}
        videoError={videoError}
        remote={remote}
        onRemote={setRemote}
        remoteError={remoteError}
        childcare={childcare}
        onChildcare={setChildcare}
        childcareError={childcareError}
        reminder={reminder}
        onReminder={setReminder}
        reminderPush={reminderPush}
        onReminderPush={setReminderPush}
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
