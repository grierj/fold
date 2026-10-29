import { Form } from "./ui/Form";
import { useState } from "react";
import { Contacts } from "../contact-api";
import type { ContactRequest } from "../types";
import { ERROR_COPY, errorText } from "../lib/errors";
import { Button, ButtonLink } from "./ui/Button";
import { Choices, Field, FormError, Radio, Textarea, showFieldErrors } from "./ui/Field";

export function ContactRequestForm({ subjectUserId, subjectName, contextType, contextId, onClose }: {
  subjectUserId: number; subjectName: string; contextType: "group" | "event"; contextId: number; onClose: () => void;
}) {
  const [fields, setFields] = useState<ContactRequest["fields"]>("email");
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<ContactRequest | null>(null);
  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (Array.from(reason).length > 1000) {
      showFieldErrors(e.currentTarget, () => setReasonError(ERROR_COPY.contact_reason_long));
      return;
    }
    setBusy(true); setError(""); setReasonError("");
    try { setCreated((await Contacts.create({ subjectUserId, contextType, contextId, fields, reason })).request); }
    catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  }
  return <section aria-label={`Request direct contact with ${subjectName}`} className="mt-3 p-4 rounded-lg border border-border bg-surface space-y-3">
    <h3 className="font-semibold">Request direct contact with {subjectName}</h3>
    {created ? <>
      <p role="status">Your request was sent. Contact details stay hidden until the required approvals are complete.</p>
      <ButtonLink variant="secondary" to={`/contact-requests/${created.id}`}>View request</ButtonLink>
      <Button variant="link" onClick={onClose}>Close</Button>
    </> : <Form noValidate onSubmit={submit} className="space-y-3">
      <p className="text-sm text-text-muted">They choose whether to share. For a teen, both the teen and an admin must approve. Requests expire after 14 days.</p>
      {error && <FormError>{error}</FormError>}
      <Choices legend="Contact details to request">{(["email", "phone", "both"] as const).map((value) => <Radio key={value} name={`contact-fields-${contextType}-${contextId}-${subjectUserId}`} value={value} checked={fields === value} onChange={() => setFields(value)} label={value === "both" ? "Email and phone" : value === "email" ? "Email" : "Phone"} />)}</Choices>
      <Field label="Reason" optional hint={`Maximum 1000 Unicode characters. ${Array.from(reason).length}/1000 characters.`} error={reasonError}>
        <Textarea value={reason} onChange={(e) => { setReason(e.target.value); setReasonError(""); }} rows={3} />
      </Field>
      <div className="flex flex-wrap gap-2"><Button variant="primary" type="submit" disabled={busy}>{busy ? "Sending…" : "Send request"}</Button><Button variant="secondary" onClick={onClose}>Cancel</Button></div>
    </Form>}
  </section>;
}
