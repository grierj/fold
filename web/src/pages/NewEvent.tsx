import { Form } from "../components/ui/Form";
import { childcareBody, childcareProblem, emptyChildcare, focusChildcareCapacity, isChildcareCapacityError, type ChildcareValue } from "../components/ChildcareField";
import { VisibilityField } from "../components/EventVisibility";
import type { Visibility } from "../types";
import { WhenFields } from "../components/WhenFields";
import { EventOptions } from "../components/EventOptions";
import { emptySeats, focusSeatLimit, isSeatsError, seatsBody, seatsProblem, type SeatsValue } from "../components/SeatsField";
import { FormActions } from "../components/ui/FormActions";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Groups } from "../api";
import { useAuth } from "../auth";
import { useEventZone } from "../lib/useEventZone";
import { useWhenState } from "../lib/useWhenState";
import { useDirty } from "../lib/useDirty";
import { useUnsavedChanges } from "../lib/unsavedChanges";
import { errorText } from "../lib/errors";
import { focusVideoUrl, isVideoError, videoProblem, emptyVideo, type VideoValue } from "../components/VideoField";
import { focusRemoteCapacity, isRemoteError, emptyRemote, remoteBody, remoteProblem, type RemoteValue } from "../components/RemoteField";
import { Field, FormError, Input, Textarea } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
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
  const when = useWhenState({ date: "", time: "19:00", duration: 90, recurring: false, days: [] });
  const { date, time: startTime, duration: durationMin, recurring, days } = when;
  const { tzid, setTzid } = useEventZone();
  const [location, setLocation] = useState("");
  // Asked at creation, defaulting to the group's value (DEC-51).
  const [groupVisibility, setGroupVisibility] = useState<Visibility>("public");
  const [visibility, setVisibility] = useState<Visibility>("public");
  const [visReason, setVisReason] = useState("");
  const [video, setVideo] = useState<VideoValue>(emptyVideo);
  const [childcare, setChildcare] = useState<ChildcareValue>(emptyChildcare);
  const [remote, setRemote] = useState<RemoteValue>(emptyRemote);
  const [seats, setSeats] = useState<SeatsValue>(emptySeats);
  // Closed on create; a problem inside it opens it (DEC-95).
  const [moreOpen, setMoreOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [seatsError, setSeatsError] = useState<string | null>(null);
  const [videoError, setVideoError] = useState<string | null>(null);
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const [remoteError, setRemoteError] = useState<string | null>(null);

  const dirty = useDirty(
    { title, description, date, startTime, durationMin, recurring, days, tzid, location, visibility, visReason, video, childcare, remote, seats },
    !loading && !authLoading && tzid !== "",
  );
  const { guard, markSaved } = useUnsavedChanges(dirty);

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
    const seatsMsg = seatsProblem(seats);
    setSeatsError(seatsMsg);
    if (seatsMsg) {
      setError(seatsMsg);
      setMoreOpen(true);
      focusSeatLimit("event");
      return;
    }
    const videoMsg = videoProblem(video);
    setVideoError(videoMsg);
    if (videoMsg) {
      setError(videoMsg);
      setMoreOpen(true);
      focusVideoUrl("event");
      return;
    }
    const childcareMsg = childcareProblem(childcare);
    setChildcareError(childcareMsg);
    if (childcareMsg) {
      setError(childcareMsg);
      setMoreOpen(true);
      focusChildcareCapacity("event");
      return;
    }
    const remoteMsg = remoteProblem(remote);
    setRemoteError(remoteMsg);
    if (remoteMsg) {
      setError(remoteMsg);
      setMoreOpen(true);
      focusRemoteCapacity("event");
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
        ...seatsBody(seats),
        ...remoteBody(remote),
        ...childcareBody(childcare),
      });
      markSaved();
      navigate(`/groups/${id}`);
    } catch (err) {
      const msg = errorText(err, "We couldn't schedule the event.");
      setError(msg);
      if (isSeatsError(err) || isChildcareCapacityError(err) || isRemoteError(err) || isVideoError(err)) setMoreOpen(true);
      if (isSeatsError(err)) { setSeatsError(msg); focusSeatLimit("event"); }
      if (isChildcareCapacityError(err)) { setChildcareError(msg); focusChildcareCapacity("event"); }
      if (isRemoteError(err)) {
        setRemoteError(msg);
        focusRemoteCapacity("event");
      }
      if (isVideoError(err)) {
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
        <h1 className="text-3xl font-bold mb-6">Schedule an event</h1>
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
        <h1 className="text-3xl font-bold mb-6">Schedule an event</h1>
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
      <Form noValidate onSubmit={submit} className="bg-surface rounded-lg border border-border p-4 sm:p-6 space-y-4">
        {guard}
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
        <WhenFields
          idPrefix="event"
          when={when}
          tzid={tzid}
          onTzid={setTzid}
          zoneHint="The church's time zone. Change it if the event is held somewhere else. The clock stays put across daylight saving."
        />
        <Field id="event-location" label="Location" optional hint="Leave it blank to use the group's location.">
          <Input
            value={location}
            onChange={(e) => setLocation(e.target.value)}
            placeholder="e.g. Room 201, Main Campus"
          />
        </Field>
        <EventOptions
          idPrefix="event"
          open={moreOpen}
          onOpenChange={setMoreOpen}
          visibility={{
            value: visibility,
            field: (
              <VisibilityField
                id="event-visibility"
                value={visibility}
                onChange={setVisibility}
                groupName={groupName}
                groupVisibility={groupVisibility}
                reason={visReason}
                onReason={setVisReason}
              />
            ),
          }}
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
        />
        <FormActions>
          <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
            {busy ? "Scheduling…" : "Schedule event"}
          </Button>
          <ButtonLink to={`/groups/${id}`} variant="secondary" layout="w-full sm:w-auto">
            Cancel
          </ButtonLink>
        </FormActions>
      </Form>
    </div>
  );
}
