import { Form } from "../components/ui/Form";
import { VisibilityField } from "../components/EventVisibility";
import type { Visibility } from "../types";
import { WhenFields } from "../components/WhenFields";
import { EventOptions } from "../components/EventOptions";
import { CreateRoomField } from "../components/CreateRoomField";
import { useEventCreateForm } from "../components/useEventCreateForm";
import { FormActions } from "../components/ui/FormActions";
import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Groups } from "../api/groups";
import { useAuth } from "../auth";
import { useDirty } from "../lib/useDirty";
import { useUnsavedChanges } from "../lib/unsavedChanges";
import { Field, FormError, Input, Textarea } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
import { Skeleton } from "../components/ui/Skeleton";

export function NewEvent() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const { loading: authLoading } = useAuth();
  const [groupName, setGroupName] = useState("");
  const [canManage, setCanManage] = useState(false);
  const [loading, setLoading] = useState(true);
  const f = useEventCreateForm("group");
  const { when } = f;
  // Asked at creation, defaulting to the group's value (DEC-51).
  const [groupVisibility, setGroupVisibility] = useState<Visibility>("public");
  const [visReason, setVisReason] = useState("");

  const dirty = useDirty({ ...f.snapshot, visReason }, !loading && !authLoading && f.tzid !== "");
  const { guard, markSaved } = useUnsavedChanges(dirty);

  const { setLocation, setVisibility } = f;
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
  }, [id, setLocation, setVisibility]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!f.begin()) return;
    try {
      const { event } = await Groups.createEvent(id!, {
        ...f.body(),
        ...(visReason.trim() ? { visibilityReason: visReason.trim() } : {}),
      });
      markSaved();
      // A room may be busy on some dates, which then need one; the event page lists them. A check can be stale
      // or still pending at submit, so any chosen room goes there.
      navigate(f.roomId !== null ? `/events/${event.id}` : `/groups/${id}`);
    } catch (err) {
      f.reject(err, "We couldn't schedule the event.");
    } finally {
      f.setBusy(false);
    }
  }

  if (loading || authLoading) {
    return (
      <div className="fold-page fold-page-narrow">
        <Skeleton kind="page" />
      </div>
    );
  }

  if (!canManage) {
    return (
      <div className="fold-page fold-page-narrow">
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
    <div className="fold-page fold-page-narrow">
      <header className="fold-page-header">
        <div>
          <h1 className="text-3xl font-bold mb-2">Schedule an event</h1>
          <p className="text-text-muted">
            for <span className="font-semibold">{groupName}</span>
          </p>
        </div>
      </header>
      <Form ref={f.form} noValidate onSubmit={submit} className="fold-form space-y-6">
        {guard}
        {f.error && <FormError>{f.error}</FormError>}
        <Field id="event-title" label="Title" optional hint="Leave it blank to use the group's name.">
          <Input
            value={f.title}
            onChange={(e) => f.setTitle(e.target.value)}
            placeholder={groupName || "Event name"}
          />
        </Field>
        <Field id="event-description" label="Description" optional hint="What should people know before they come?">
          <Textarea
            rows={4}
            maxLength={5000}
            value={f.description}
            onChange={(e) => f.setDescription(e.target.value)}
          />
        </Field>
        <div className="fold-section space-y-4">
          <WhenFields
            idPrefix="event"
            when={when}
            tzid={f.tzid}
            onTzid={f.setTzid}
            zoneHint="The church's time zone. Change it if the event is held somewhere else. The clock stays put across daylight saving."
          />
          <Field id="event-location" label="Location" optional hint="Leave it blank to use the group's location.">
            <Input
              value={f.location}
              onChange={(e) => f.setLocation(e.target.value)}
              placeholder="e.g. Room 201, Main Campus"
            />
          </Field>
          <CreateRoomField
            id="event-room"
            schedule={f.schedule}
            groupId={Number(id)}
            visibility={f.visibility}
            roomId={f.roomId}
            onChange={f.setRoomId}
          />
        </div>
        <EventOptions
          idPrefix="event"
          open={f.moreOpen}
          onOpenChange={f.setMoreOpen}
          visibility={{
            value: f.visibility,
            field: (
              <VisibilityField
                id="event-visibility"
                value={f.visibility}
                onChange={f.setVisibility}
                groupName={groupName}
                groupVisibility={groupVisibility}
                reason={visReason}
                onReason={setVisReason}
              />
            ),
          }}
          values={f.options}
          onChange={f.setOptions}
          errors={f.optionErrors}
        />
        <FormActions>
          <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={f.busy}>
            {f.busy ? "Scheduling…" : "Schedule event"}
          </Button>
          <ButtonLink to={`/groups/${id}`} variant="secondary" layout="w-full sm:w-auto">
            Cancel
          </ButtonLink>
        </FormActions>
      </Form>
    </div>
  );
}
