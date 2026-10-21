import { VisibilityField } from "../components/EventVisibility";
import type { Visibility } from "../types";
import { ZonePicker } from "../components/ui/ZonePicker";
import { WeekdayPicker } from "../components/WeekdayPicker";
import { FormActions } from "../components/ui/FormActions";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Events } from "../api";
import { useAuth } from "../auth";
import { weekdayCode } from "../lib/eventForm";
import { useEventZone } from "../lib/useEventZone";
import { errorText } from "../lib/errors";
import { VideoField, emptyVideo, type VideoValue } from "../components/VideoField";
import { Checkbox, Field, FormError, Input, Textarea } from "../components/ui/Field";
import { Button } from "../components/ui/Button";
import { Skeleton } from "../components/ui/Skeleton";

export function NewChurchEvent() {
  const navigate = useNavigate();
  const { user, loading, isAdmin } = useAuth();
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [date, setDate] = useState("");
  const [startTime, setStartTime] = useState("19:00");
  const [durationMin, setDurationMin] = useState(90);
  const { tzid, setTzid } = useEventZone();
  const [location, setLocation] = useState("");
  const [visibility, setVisibility] = useState<Visibility>("public");
  const [hostEmail, setHostEmail] = useState("");
  const [video, setVideo] = useState<VideoValue>(emptyVideo);
  const [recurring, setRecurring] = useState(false);
  const [days, setDays] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function toggleDay(v: string) {
    setDays((prev) => (prev.includes(v) ? prev.filter((d) => d !== v) : [...prev, v]));
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!title.trim()) {
      setError("Give the event a title.");
      return;
    }
    if (!date) {
      setError("Pick a date.");
      return;
    }
    if (!tzid.trim()) {
      setError("Pick a time zone.");
      return;
    }
    if (recurring && days.length === 0) {
      setError("Pick at least one day to repeat weekly.");
      return;
    }
    if (video.provider !== "none" && video.url.trim() === "") {
      setError(video.provider === "jitsi" ? "Generate a Jitsi link, or pick No video link." : "Paste the video link, or pick No video link.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { event } = await Events.createChurch({
        title: title.trim(),
        description: description.trim(),
        startsLocal: `${date}T${startTime}`,
        durationMinutes: durationMin,
        tzid: tzid.trim(),
        location: location.trim(),
        visibility,
        hosts: hostEmail.trim() ? [hostEmail.trim()] : [],
        recurrenceRule: recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "",
        videoProvider: video.provider,
        videoUrl: video.provider === "none" ? "" : video.url.trim(),
        videoReveal: video.reveal,
      });
      navigate(`/events/${event.id}`);
    } catch (err) {
      setError(errorText(err, "We couldn't schedule the event."));
    } finally {
      setBusy(false);
    }
  }

  if (loading) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12">
        <Skeleton kind="page" />
      </div>
    );
  }
  if (!user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <Link to="/login" state={{ from: "/events/new" }} className="text-primary font-semibold">
          Log in
        </Link>{" "}
        to schedule a church-wide event.
      </div>
    );
  }
  if (!isAdmin) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center text-text-muted">
        Only an admin can schedule a church-wide event.
      </div>
    );
  }

  return (
    <div className="max-w-xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold mb-2">Schedule a church-wide event</h1>
      <p className="text-text-muted mb-6">You are a host. Add another host if someone else should run it.</p>
      <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-4 sm:p-6 space-y-4">
        {error && <FormError>{error}</FormError>}
        <Field id="church-event-title" label="Title">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} required />
        </Field>
        <Field id="church-event-description" label="Description" optional hint="What should people know before they come?">
          <Textarea
            rows={4}
            maxLength={5000}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>
        <div className="grid sm:grid-cols-2 gap-4">
          <Field id="church-event-date" label="Date">
            <Input
              type="date"
              value={date}
              onChange={(e) => {
                const v = e.target.value;
                setDate(v);
                if (recurring && days.length === 0 && v) setDays([weekdayCode(v)]);
              }}
              required
            />
          </Field>
          <Field id="church-event-time" label="Start time">
            <Input
              type="time"
              value={startTime}
              onChange={(e) => setStartTime(e.target.value)}
              required
            />
          </Field>
        </div>
        <Field id="church-event-duration" label="Duration (minutes)">
          <Input
            type="number"
            min={1}
            max={10080}
            step={1}
            value={durationMin}
            onChange={(e) => setDurationMin(Number(e.target.value))}
            layout="sm:w-44"
          />
        </Field>
        <Field
          id="church-event-tz"
          label="Time zone"
          hint="The church's time zone. Change it if the event is held somewhere else. The clock stays put across daylight saving."
        >
          <ZonePicker value={tzid} onChange={setTzid} />
        </Field>
        <Field id="church-event-location" label="Location" optional>
          <Input value={location} onChange={(e) => setLocation(e.target.value)} />
        </Field>
        <VisibilityField
          id="church-event-visibility"
          churchWide
          value={visibility}
          onChange={setVisibility}
          reason=""
          onReason={() => {}}
        />
        <Field id="church-event-host" label="Another host's email" optional hint="They must already have an account.">
          <Input type="email" value={hostEmail} onChange={(e) => setHostEmail(e.target.value)} />
        </Field>
        <div className="border-t border-border pt-4">
          <VideoField idPrefix="church-event" value={video} onChange={setVideo} />
        </div>
        <div className="border-t border-border pt-4">
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
        <FormActions>
          <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
            {busy ? "Scheduling…" : "Schedule event"}
          </Button>
          <Link to="/" className="btn-secondary w-full sm:w-auto">
            Cancel
          </Link>
        </FormActions>
      </form>
    </div>
  );
}
