import { Form } from "../components/ui/Form";
import { VisibilityField } from "../components/EventVisibility";
import { WhenFields } from "../components/WhenFields";
import { EventOptions } from "../components/EventOptions";
import { CreateRoomField } from "../components/CreateRoomField";
import { useEventCreateForm } from "../components/useEventCreateForm";
import { FormActions } from "../components/ui/FormActions";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Events } from "../api/events";
import { useAuth } from "../auth";
import { useDirty } from "../lib/useDirty";
import { useUnsavedChanges } from "../lib/unsavedChanges";
import { Field, FormError, Input, Textarea } from "../components/ui/Field";
import { Button, ButtonLink } from "../components/ui/Button";
import { Skeleton } from "../components/ui/Skeleton";

export function NewChurchEvent() {
  const navigate = useNavigate();
  const { loading, isAdmin } = useAuth();
  const f = useEventCreateForm("church");
  const [hostEmail, setHostEmail] = useState("");

  const dirty = useDirty({ ...f.snapshot, hostEmail }, !loading && f.tzid !== "");
  const { guard, markSaved } = useUnsavedChanges(dirty);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!f.begin()) return;
    try {
      const { event } = await Events.createChurch({
        ...f.body(),
        hosts: hostEmail.trim() ? [hostEmail.trim()] : [],
      });
      markSaved();
      navigate(`/events/${event.id}`);
    } catch (err) {
      f.reject(err, "We couldn't schedule the event.");
    } finally {
      f.setBusy(false);
    }
  }

  if (loading) {
    return (
      <div className="fold-page fold-page-narrow">
        <Skeleton kind="page" />
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
      <Form ref={f.form} noValidate onSubmit={submit} className="fold-form space-y-6">
        {guard}
        {f.error && <FormError>{f.error}</FormError>}
        <Field id="church-event-title" label="Title">
          <Input value={f.title} onChange={(e) => f.setTitle(e.target.value)} required />
        </Field>
        <Field id="church-event-description" label="Description" optional hint="What should people know before they come?">
          <Textarea
            rows={4}
            maxLength={5000}
            value={f.description}
            onChange={(e) => f.setDescription(e.target.value)}
          />
        </Field>
        <div className="fold-section space-y-4">
          <WhenFields
            idPrefix="church-event"
            when={f.when}
            tzid={f.tzid}
            onTzid={f.setTzid}
            zoneHint="The church's time zone. Change it if the event is held somewhere else. The clock stays put across daylight saving."
          />
          <Field id="church-event-location" label="Location" optional>
            <Input value={f.location} onChange={(e) => f.setLocation(e.target.value)} />
          </Field>
          <CreateRoomField
            id="church-event-room"
            schedule={f.schedule}
            visibility={f.visibility}
            roomId={f.roomId}
            onChange={f.setRoomId}
          />
          <Field id="church-event-host" label="Another host's email" optional hint="They must already have an account.">
            <Input type="email" value={hostEmail} onChange={(e) => setHostEmail(e.target.value)} />
          </Field>
        </div>
        <EventOptions
          idPrefix="church-event"
          open={f.moreOpen}
          onOpenChange={f.setMoreOpen}
          visibility={{
            value: f.visibility,
            field: (
              <VisibilityField
                id="church-event-visibility"
                churchWide
                value={f.visibility}
                onChange={f.setVisibility}
                reason=""
                onReason={() => {}}
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
          <ButtonLink to="/" variant="secondary" layout="w-full sm:w-auto">
            Cancel
          </ButtonLink>
        </FormActions>
      </Form>
    </div>
  );
}
