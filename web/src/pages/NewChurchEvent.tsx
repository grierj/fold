import { Form } from "../components/ui/Form";
import { childcareBody, childcareProblem, emptyChildcare, focusChildcareCapacity, isChildcareCapacityError, type ChildcareValue } from "../components/ChildcareField";
import { VisibilityField } from "../components/EventVisibility";
import type { Visibility } from "../types";
import { WhenFields } from "../components/WhenFields";
import { EventOptions } from "../components/EventOptions";
import { DEFAULT_REMINDER_MINUTES } from "../components/ReminderField";
import { CreateRoomField, scheduleFields, scheduleReady } from "../components/CreateRoomField";
import { emptySeats, focusSeatLimit, isSeatsError, seatsBody, seatsProblem, type SeatsValue } from "../components/SeatsField";
import { FormActions } from "../components/ui/FormActions";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Events } from "../api";
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

export function NewChurchEvent() {
  const navigate = useNavigate();
  const { user, loading, isAdmin } = useAuth();
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const when = useWhenState({ date: "", time: "19:00", duration: 90, recurring: false, days: [] });
  const { date, time: startTime, duration: durationMin, recurring, days } = when;
  const { tzid, setTzid } = useEventZone();
  const [location, setLocation] = useState("");
  const [visibility, setVisibility] = useState<Visibility>("public");
  const [hostEmail, setHostEmail] = useState("");
  const [video, setVideo] = useState<VideoValue>(emptyVideo);
  const [childcare, setChildcare] = useState<ChildcareValue>(emptyChildcare);
  const [remote, setRemote] = useState<RemoteValue>(emptyRemote);
  const [seats, setSeats] = useState<SeatsValue>(emptySeats);
  const [reminder, setReminder] = useState(DEFAULT_REMINDER_MINUTES);
  const [reminderPush, setReminderPush] = useState(true);
  // The room for every date; the event page lists any dates it is busy on.
  const [roomId, setRoomId] = useState<number | null>(null);
  // Closed on create; a problem inside it opens it (DEC-95).
  const [moreOpen, setMoreOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [seatsError, setSeatsError] = useState<string | null>(null);
  const [videoError, setVideoError] = useState<string | null>(null);
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const [remoteError, setRemoteError] = useState<string | null>(null);

  const dirty = useDirty(
    { title, description, date, startTime, durationMin, recurring, days, tzid, location, visibility, hostEmail, video, childcare, remote, seats, roomId, reminder, reminderPush },
    !loading && tzid !== "",
  );
  const { guard, markSaved } = useUnsavedChanges(dirty);

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
    const seatsMsg = seatsProblem(seats);
    setSeatsError(seatsMsg);
    if (seatsMsg) {
      setError(seatsMsg);
      setMoreOpen(true);
      focusSeatLimit("church-event");
      return;
    }
    const videoMsg = videoProblem(video);
    setVideoError(videoMsg);
    if (videoMsg) {
      setError(videoMsg);
      setMoreOpen(true);
      focusVideoUrl("church-event");
      return;
    }
    const childcareMsg = childcareProblem(childcare);
    setChildcareError(childcareMsg);
    if (childcareMsg) {
      setError(childcareMsg);
      setMoreOpen(true);
      focusChildcareCapacity("church-event");
      return;
    }
    const remoteMsg = remoteProblem(remote);
    setRemoteError(remoteMsg);
    if (remoteMsg) {
      setError(remoteMsg);
      setMoreOpen(true);
      focusRemoteCapacity("church-event");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const { event } = await Events.createChurch({
        title: title.trim(),
        description: description.trim(),
        ...scheduleFields(when, tzid, seats),
        location: location.trim(),
        visibility,
        hosts: hostEmail.trim() ? [hostEmail.trim()] : [],
        videoProvider: video.provider,
        videoUrl: video.provider === "none" ? "" : video.url.trim(),
        videoReveal: video.reveal,
        ...seatsBody(seats),
        ...remoteBody(remote),
        ...childcareBody(childcare),
        reminderEmailMinutes: reminder,
        reminderPush,
        ...(roomId !== null ? { roomId } : {}),
      });
      markSaved();
      navigate(`/events/${event.id}`);
    } catch (err) {
      const msg = errorText(err, "We couldn't schedule the event.");
      setError(msg);
      if (isSeatsError(err) || isChildcareCapacityError(err) || isRemoteError(err) || isVideoError(err)) setMoreOpen(true);
      if (isSeatsError(err)) { setSeatsError(msg); focusSeatLimit("church-event"); }
      if (isChildcareCapacityError(err)) { setChildcareError(msg); focusChildcareCapacity("church-event"); }
      if (isRemoteError(err)) {
        setRemoteError(msg);
        focusRemoteCapacity("church-event");
      }
      if (isVideoError(err)) {
        setVideoError(msg);
        focusVideoUrl("church-event");
      }
    } finally {
      setBusy(false);
    }
  }

  if (loading) {
    return (
      <div className="fold-page fold-page-narrow">
        <Skeleton kind="page" />
      </div>
    );
  }
  if (!user) {
    return (
      <div className="fold-page fold-page-narrow">
        <h1 className="text-3xl font-bold mb-6">Schedule a church-wide event</h1>
        <Link to="/login" state={{ from: "/events/new" }} className="text-primary font-semibold">
          Log in
        </Link>{" "}
        to schedule a church-wide event.
      </div>
    );
  }
  if (!isAdmin) {
    return (
      <div className="fold-page fold-page-narrow text-text-muted">
        <h1 className="text-3xl font-bold mb-6">Schedule a church-wide event</h1>
        Only an admin can schedule a church-wide event.
      </div>
    );
  }

  return (
    <div className="fold-page fold-page-narrow">
      <header className="fold-page-header">
        <div>
          <h1 className="text-3xl font-bold mb-2">Schedule a church-wide event</h1>
          <p className="text-text-muted">You are a host. Add another host if someone else should run it.</p>
        </div>
      </header>
      <Form noValidate onSubmit={submit} className="fold-form space-y-6">
        {guard}
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
        <div className="fold-section space-y-4">
          <WhenFields
            idPrefix="church-event"
            when={when}
            tzid={tzid}
            onTzid={setTzid}
            zoneHint="The church's time zone. Change it if the event is held somewhere else. The clock stays put across daylight saving."
          />
          <Field id="church-event-location" label="Location" optional>
            <Input value={location} onChange={(e) => setLocation(e.target.value)} />
          </Field>
          <CreateRoomField
            id="church-event-room"
            schedule={scheduleReady(when, tzid, seats) ? scheduleFields(when, tzid, seats) : null}
            visibility={visibility}
            roomId={roomId}
            onChange={setRoomId}
          />
          <Field id="church-event-host" label="Another host's email" optional hint="They must already have an account.">
            <Input type="email" value={hostEmail} onChange={(e) => setHostEmail(e.target.value)} />
          </Field>
        </div>
        <EventOptions
          idPrefix="church-event"
          open={moreOpen}
          onOpenChange={setMoreOpen}
          visibility={{
            value: visibility,
            field: (
              <VisibilityField
                id="church-event-visibility"
                churchWide
                value={visibility}
                onChange={setVisibility}
                reason=""
                onReason={() => {}}
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
          reminder={reminder}
          onReminder={setReminder}
          reminderPush={reminderPush}
          onReminderPush={setReminderPush}
        />
        <FormActions>
          <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
            {busy ? "Scheduling…" : "Schedule event"}
          </Button>
          <ButtonLink to="/" variant="secondary" layout="w-full sm:w-auto">
            Cancel
          </ButtonLink>
        </FormActions>
      </Form>
    </div>
  );
}
