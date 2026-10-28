import { Form } from "./ui/Form";
import { useState } from "react";
import { Adds, type AddRequestInput } from "../add-api";
import type { AddRequest, PersonSearchResult } from "../types";
import { ERROR_COPY, errorText } from "../lib/errors";
import { Button, ButtonLink } from "./ui/Button";
import { Checkbox, Field, FormError, Textarea, showFieldErrors } from "./ui/Field";

export function AddRequestForm({ person, scope, full = false, onClose }: {
  person: PersonSearchResult;
  scope: Omit<Extract<AddRequestInput, { contextType: "group" }>, "selectionId"> | Omit<Extract<AddRequestInput, { contextType: "event" }>, "selectionId">;
  full?: boolean;
  onClose: () => void;
}) {
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<AddRequest | null>(null);
  const group = scope.contextType === "group";
  const [over, setOver] = useState(group ? scope.overLimit === true : scope.overCapacity === true);
  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (Array.from(reason).length > 1000) {
      showFieldErrors(e.currentTarget, () => setReasonError(ERROR_COPY.add_reason_long));
      return;
    }
    if (over && !confirm(group ? "Allow this ask to add someone over the group's member limit? This choice is logged and cannot be changed on this request." : "Allow this ask to add someone over this date's capacity? This choice is logged and cannot be changed on this request.")) return;
    setBusy(true); setError(""); setReasonError("");
    const body: AddRequestInput = scope.contextType === "group"
      ? { ...scope, selectionId: person.selectionId, reason, overLimit: over }
      : { ...scope, selectionId: person.selectionId, reason, overCapacity: over };
    try { setCreated((await Adds.create(body)).request); }
    catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  }
  return <section aria-label="Ask to add" className="mt-3 p-4 rounded-lg border border-border bg-surface space-y-3">
    <h3 className="font-semibold">Ask to add {person.displayName}</h3>
    {created ? <>
      <p role="status">Your ask was sent. They have not been added yet. {person.teen ? "Both the teen and an admin must approve." : "Their approval is needed."}</p>
      <ButtonLink variant="secondary" to={`/add-requests/${created.id}`}>View ask</ButtonLink>
      <Button variant="link" onClick={onClose}>Close</Button>
    </> : <Form noValidate onSubmit={submit} className="space-y-3">
      <p>{group ? "This asks to add them as a group member." : "This asks to add them to this date only, not the group or other dates."} It does not ask for or share contact details.</p>
      <p className="text-sm text-text-muted">{person.teen ? "Both the teen and an admin must approve, even when an admin sends the ask. Either can deny it." : "They choose whether to accept."} The ask expires after 14 days. Approval does not reserve a place. Options and expiry cannot be changed by retrying.</p>
      {error && <FormError>{error}</FormError>}
      <Field label="Reason" optional hint={`Maximum 1000 Unicode characters. ${Array.from(reason).length}/1000 characters.`} error={reasonError}>
        <Textarea value={reason} onChange={(e) => { setReason(e.target.value); setReasonError(""); }} rows={3} disabled={busy} />
      </Field>
      {(full || over) && <Checkbox label={group ? "Allow adding over the member limit" : "Allow adding over capacity for this date"} description="Only if you explicitly allow it here. This choice is logged." checked={over} disabled={busy} onChange={(e) => setOver(e.target.checked)} />}
      <div className="flex flex-wrap gap-2"><Button variant="primary" type="submit" disabled={busy}>{busy ? "Sending…" : "Send ask to add"}</Button><Button variant="secondary" disabled={busy} onClick={onClose}>Cancel</Button></div>
    </Form>}
  </section>;
}
