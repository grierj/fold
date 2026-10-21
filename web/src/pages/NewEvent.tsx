import { VisibilityField } from "../components/EventVisibility";
import type { Visibility } from "../types";
import { ZonePicker } from "../components/ui/ZonePicker";
import { WeekdayPicker } from "../components/WeekdayPicker";
import { FormActions } from "../components/ui/FormActions";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Groups } from "../api";
import { useAuth } from "../auth";
import { weekdayCode } from "../lib/eventForm";
import { useEventZone } from "../lib/useEventZone";
import { errorText } from "../lib/errors";
import { VideoField, focusVideoUrl, isVideoError, videoProblem, emptyVideo, type VideoValue } from "../components/VideoField";
import { RemoteField, emptyRemote, remoteBody, remoteProblem, type RemoteValue } from "../components/RemoteField";
import { Checkbox, Field, FormError, Input, Textarea } from "../components/ui/Field";
import { Button } from "../components/ui/Button";
import { Skeleton } from "../components/ui/Skeleton";

export function NewEvent() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { user, loading: authLoading } = useAuth();
  const [groupName, setGroupName] = useState("");
  const [canManage, setCanManage] = useState(false);
  const [loading, setLoading] = useState(true);

  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [date, setDate] = useState("");
  const [startTime, setStartTime] = useState("19:00");
  const [durationMin, setDurationMin] = useState(90);
  const { tzid, setTzid } = useEventZone();
  const [location, setLocation] = useState("");
  // Asked at creation, defaulting to the group's value (DEC-51).
  const [groupVisibility, setGroupVisibility] = useState<Visibility>("public");
  const [visibility, setVisibility] = useState<Visibility>("public");
  const [visReason, setVisReason] = useState("");
  const [video, setVideo] = useState<VideoValue>(emptyVideo);
  const [remote, setRemote] = useState<RemoteValue>(emptyRemote);
  const [recurring, setRecurring] = useState(false);
  const [days, setDays] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [videoError, setVideoError] = useState<string | null>(null);
  const [remoteError, setRemoteError] = useState<string | null>(null);

  useEffect(() => {
    if (!id) return;
    let alive = true;
    Groups.get(id)
      .then((d) => {
        if (!alive) return;
        setGroupName(d.group.name);
        setCanManage(d.canManage);
        setLocation(d.group.location);
        setGroupVisibility(d.group.visibility);
        setVisibility(d.group.visibility);
      })
      .catch(() => {
        if (alive) setCanManage(false);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [id]);

  function toggleDay(v: string) {
    setDays((prev) => (prev.includes(v) ? prev.filter((d) => d !== v) : [...prev, v]));
  }

  function onRecurring(checked: boolean) {
    setRecurring(checked);
    if (checked && days.length === 0 && date) setDays([weekdayCode(date)]);
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
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
    const videoMsg = videoProblem(video);
    setVideoError(videoMsg);
    if (videoMsg) {
      setError(videoMsg);
      focusVideoUrl("event");
      return;
    }
    const remoteMsg = remoteProblem(remote);
    setRemoteError(remoteMsg);
    if (remoteMsg) {
      setError(remoteMsg);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await Groups.createEvent(id!, {
        title: title.trim(),
        description: description.trim(),
        startsLocal: `${date}T${startTime}`,
        durationMinutes: durationMin,
        tzid: tzid.trim(),
        location: location.trim(),
        recurrenceRule: recurring ? `FREQ=WEEKLY;BYDAY=${days.join(",")}` : "",
        visibility,
        ...(visReason.trim() ? { visibilityReason: visReason.trim() } : {}),
        videoProvider: video.provider,
        videoUrl: video.provider === "none" ? "" : video.url.trim(),
        videoReveal: video.reveal,
        ...remoteBody(remote),
      });
      navigate(`/groups/${id}`);
    } catch (err) {
      const msg = errorText(err, "We couldn't schedule the event.");
      setError(msg);
      if (isVideoError(msg)) {
        setVideoError(msg);
        focusVideoUrl("event");
      }
    } finally {
      setBusy(false);
    }
  }

  if (loading || authLoading) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12">
        <Skeleton kind="page" />
      </div>
    );
  }

  if (!user) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <Link to="/login" state={{ from: `/groups/${id}/events/new` }} className="text-primary font-semibold">
          Log in
        </Link>{" "}
        to schedule an event.
      </div>
    );
  }

  if (!canManage) {
    return (
      <div className="max-w-2xl mx-auto px-4 py-12 text-center">
        <p className="text-text-muted">
          Only the group leader or an admin can schedule events for{" "}
          <span className="font-semibold">{groupName || "this group"}</span>.
        </p>
        <Link to={`/groups/${id}`} className="text-primary font-semibold">
          Back to group
        </Link>
      </div>
    );
  }

  return (
    <div className="max-w-xl mx-auto px-4 py-8">
      <h1 className="text-3xl font-bold mb-2">Schedule an event</h1>
      <p className="text-text-muted mb-6">
        for <span className="font-semibold">{groupName}</span>
      </p>
      <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-4 sm:p-6 space-y-4">
        {error && <FormError>{error}</FormError>}
        <Field id="event-title" label="Title" optional hint="Leave it blank to use the group's name.">
          <Input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={groupName || "Event name"}
          />
        </Field>
        <Field id="event-description" label="Description" optional hint="What should people know before they come?">
          <Textarea
            rows={4}
            maxLength={5000}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>
        <div className="grid sm:grid-cols-2 gap-4">
          <Field id="event-date" label="Date">
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
          <Field id="event-time" label="Start time">
            <Input
              type="time"
              value={startTime}
              onChange={(e) => setStartTime(e.target.value)}
              required
            />
          </Field>
        </div>
        <div>
          <Field id="event-duration" label="Duration (minutes)">
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
        </div>
        <Field
          id="event-tz"
          label="Time zone"
          hint="The church's time zone. Change it if the event is held somewhere else. The clock stays put across daylight saving."
        >
          <ZonePicker value={tzid} onChange={setTzid} />
        </Field>
        <Field id="event-location" label="Location" optional hint="Leave it blank to use the group's location.">
          <Input
            value={location}
            onChange={(e) => setLocation(e.target.value)}
            placeholder="e.g. Room 201, Main Campus"
          />
        </Field>
        <VisibilityField
          id="event-visibility"
          value={visibility}
          onChange={setVisibility}
          groupName={groupName}
          groupVisibility={groupVisibility}
          reason={visReason}
          onReason={setVisReason}
        />
        <div className="border-t border-border pt-4">
          <VideoField idPrefix="event" value={video} onChange={setVideo} error={videoError} />
        </div>
        <div className="border-t border-border pt-4">
          <RemoteField idPrefix="event" value={remote} onChange={setRemote} error={remoteError} />
        </div>
        <div className="border-t border-border pt-4">
          <Checkbox label="Repeat weekly" checked={recurring} onChange={(e) => onRecurring(e.target.checked)} />
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
          <Link to={`/groups/${id}`} className="btn-secondary w-full sm:w-auto">
            Cancel
          </Link>
        </FormActions>
      </form>
    </div>
  );
}
