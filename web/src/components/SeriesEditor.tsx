import { useState } from "react";
import { Events } from "../api";
import { bydays, isSimpleWeekly, weekdayCode } from "../lib/eventForm";
import { errorText } from "../lib/errors";
import { Checkbox, Field, FormError, Input, Textarea } from "./ui/Field";
import { FormActions } from "./ui/FormActions";
import { ZonePicker } from "./ui/ZonePicker";
import { WeekdayPicker } from "./WeekdayPicker";
import { Button } from "./ui/Button";
import { VideoField, focusVideoUrl, isVideoError, videoProblem, type VideoValue } from "./VideoField";
import { RemoteField, focusRemoteCapacity, isRemoteError, remoteBody, remoteFrom, remoteProblem, type RemoteValue } from "./RemoteField";
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
  videoProvider?: VideoProvider;
  videoUrl?: string;
  videoReveal?: VideoReveal;
  remoteAvailable?: boolean;
  remoteCapacity?: number | null;
}

// SeriesEditor is the one "Edit series" / "Edit event" form, used on the
// group page and the event page (consistency rule, AGENTS.md).
export function SeriesEditor({
  series: instance,
  idPrefix,
  locationHint,
  onSaved,
  onClose,
}: {
  series: SeriesFields;
  idPrefix: string;
  locationHint?: string;
  onSaved: () => void;
  onClose?: () => void;
}) {
  const simple = isSimpleWeekly(instance.recurrenceRule);
  const [date, setDate] = useState(instance.startsLocal.slice(0, 10));
  const [time, setTime] = useState(instance.startsLocal.slice(11, 16) || "19:00");
  const [duration, setDuration] = useState(instance.durationMinutes);
  const [tzid, setTzid] = useState(instance.tzid);
  const [title, setTitle] = useState(instance.title);
  const [description, setDescription] = useState(instance.description);
  const [location, setLocation] = useState(instance.location);
  const [recurring, setRecurring] = useState(instance.recurrenceRule !== "");
  const [days, setDays] = useState<string[]>(() => {
    const picked = bydays(instance.recurrenceRule);
    if (picked.length > 0) return picked;
    if (instance.recurrenceRule.startsWith("FREQ=WEEKLY")) return [weekdayCode(instance.startsLocal.slice(0, 10))];
    return [];
  });
  const [rule, setRule] = useState(instance.recurrenceRule);
  const [until, setUntil] = useState(instance.rruleUntil ?? "");
  const [capacity, setCapacity] = useState(instance.capacity === null ? "" : String(instance.capacity));
  const [waitlistEnabled, setWaitlistEnabled] = useState(instance.waitlistEnabled);
  const [guestsAllowed, setGuestsAllowed] = useState(instance.guestsAllowed);
  const initialVideo: VideoValue = {
    provider: instance.videoProvider ?? "none",
    url: instance.videoUrl ?? "",
    reveal: instance.videoReveal ?? "registered_only",
  };
  const [video, setVideo] = useState<VideoValue>(initialVideo);
  const initialRemote = remoteFrom(instance.remoteAvailable, instance.remoteCapacity);
  const [remote, setRemote] = useState<RemoteValue>(initialRemote);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [videoError, setVideoError] = useState<string | null>(null);
  const [remoteError, setRemoteError] = useState<string | null>(null);

  function toggleDay(v: string) {
    setDays((prev) => (prev.includes(v) ? prev.filter((d) => d !== v) : [...prev, v]));
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (simple && recurring && days.length === 0) {
      setError("Pick at least one day to repeat weekly.");
      return;
    }
    const videoMsg = videoProblem(video);
    setVideoError(videoMsg);
    if (videoMsg) {
      setError(videoMsg);
      focusVideoUrl(idPrefix);
      return;
    }
    const remoteMsg = remoteProblem(remote);
    setRemoteError(remoteMsg);
    if (remoteMsg) {
      setError(remoteMsg);
      focusRemoteCapacity(idPrefix);
      return;
    }
    setBusy(true);
    setError(null);
    const recurrenceRule = simple ? (recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "") : rule.trim();
    const capTrimmed = capacity.trim();
    const capInitial = instance.capacity === null ? "" : String(instance.capacity);
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
      remoteAvailable?: boolean;
      remoteCapacity?: number | null;
      videoProvider?: VideoProvider;
      videoUrl?: string;
      videoReveal?: VideoReveal;
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
    if (capTrimmed !== capInitial) body.capacity = capTrimmed === "" ? null : Number(capTrimmed);
    if (waitlistEnabled !== instance.waitlistEnabled) body.waitlistEnabled = waitlistEnabled;
    if (guestsAllowed !== instance.guestsAllowed) body.guestsAllowed = guestsAllowed;
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
      onSaved();
    } catch (err) {
      const msg = errorText(err, "We couldn't update the series.");
      setError(msg);
      if (isRemoteError(msg)) {
        setRemoteError(msg);
        focusRemoteCapacity(idPrefix);
      }
      if (isVideoError(msg)) {
        setVideoError(msg);
        focusVideoUrl(idPrefix);
      }
      setBusy(false);
    }
  }

  return (
    <form onSubmit={save} className="mt-3 space-y-3 border-t border-border pt-3">
      <p className="text-sm text-text-muted">
        This changes every future date that has not been edited on its own. Customized dates stay as they are. Dates
        the new schedule drops are cancelled, and people who were going or maybe are told.
      </p>
      {error && <FormError>{error}</FormError>}
      <div className="grid sm:grid-cols-2 gap-3">
        <Field id={`s-date-${idPrefix}`} label="First date">
          <Input type="date" value={date} onChange={(e) => setDate(e.target.value)} required />
        </Field>
        <Field id={`s-time-${idPrefix}`} label="Start time">
          <Input type="time" value={time} onChange={(e) => setTime(e.target.value)} required />
        </Field>
      </div>
      <div>
        <Field id={`s-dur-${idPrefix}`} label="Duration (minutes)">
          <Input
            type="number"
            min={1}
            max={10080}
            value={duration}
            onChange={(e) => setDuration(Number(e.target.value))}
          />
        </Field>
      </div>
      <Field id={`s-tz-${idPrefix}`} label="Time zone">
        <ZonePicker value={tzid} onChange={setTzid} />
      </Field>
      <div className="grid sm:grid-cols-2 gap-3">
        <Field id={`s-title-${idPrefix}`} label="Title">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field id={`s-loc-${idPrefix}`} label="Location" optional hint={locationHint}>
          <Input value={location} onChange={(e) => setLocation(e.target.value)} />
        </Field>
      </div>
      <Field id={`s-desc-${idPrefix}`} label="Description" optional>
        <Textarea
          rows={3}
          maxLength={5000}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
      </Field>
      {simple ? (
        <div>
          <Checkbox
            label="Repeat weekly"
            checked={recurring}
            onChange={(e) => {
              setRecurring(e.target.checked);
              if (e.target.checked && days.length === 0 && date) setDays([weekdayCode(date)]);
            }}
          />
          {recurring && (
            <div className="mt-2">
              <WeekdayPicker days={days} onToggle={toggleDay} />
            </div>
          )}
        </div>
      ) : (
        <Field id={`s-rule-${idPrefix}`} label="Recurrence rule">
          <Input value={rule} onChange={(e) => setRule(e.target.value)} />
        </Field>
      )}
      <Field id={`s-until-${idPrefix}`} label="Repeat until" optional>
        <Input type="date" value={until} onChange={(e) => setUntil(e.target.value)} />
      </Field>
      <div className="grid sm:grid-cols-2 gap-3">
        <Field id={`s-cap-${idPrefix}`} label="Capacity" optional hint="Seats per date. Leave it blank for no limit.">
          <Input
            type="number"
            min={1}
            placeholder="No limit"
            value={capacity}
            onChange={(e) => setCapacity(e.target.value)}
          />
        </Field>
        <Field id={`s-guests-${idPrefix}`} label="Guests allowed" hint="Extra guests one registration may bring (0–5).">
          <Input
            type="number"
            min={0}
            max={5}
            value={guestsAllowed}
            onChange={(e) => setGuestsAllowed(Math.max(0, Math.min(5, Number(e.target.value))))}
          />
        </Field>
      </div>
      <Checkbox
        label="Waitlist when full"
        description="When the waitlist is off, a full date refuses new registrations. When it is on, extra people join the waitlist in order and move up automatically as spots open."
        checked={waitlistEnabled}
        onChange={(e) => setWaitlistEnabled(e.target.checked)}
      />
      <RemoteField idPrefix={idPrefix} value={remote} onChange={setRemote} error={remoteError} />
      <VideoField idPrefix={idPrefix} value={video} onChange={setVideo} error={videoError} />
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
    </form>
  );
}
