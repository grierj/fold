import { useEffect, useRef, useState } from "react";
import { Contacts } from "../contact-api";
import type { ContactGrant, ContactReveal } from "../types";
import { errorText } from "../lib/errors";
import { formatWhen } from "../lib/when";
import { Button, ButtonLink } from "./ui/Button";
import { Choices, FormError, Radio } from "./ui/Field";

export function contactFieldsLabel(fields: ContactGrant["fields"]) {
  return fields === "both" ? "Email and phone" : fields === "email" ? "Email" : "Phone";
}

const endLabels: Record<ContactGrant["endReason"], string> = {
  "": "", revoked_by_subject: "Revoked by the person", revoked_by_admin: "Revoked by an admin", revoked_by_guardian: "Revoked by a guardian",
  left_role: "The requester no longer manages this context", left_context: "The person left this context", context_ended: "The group or event ended",
  turned_18: "The teen turned 18", account_disabled: "An account is no longer active", event_ended: "The event's contact access period ended",
};

export function ContactContextLink({ contextType, contextId }: Pick<ContactGrant, "contextType" | "contextId">) {
  return contextId !== undefined && Number.isSafeInteger(contextId) && contextId > 0
    ? <ButtonLink variant="link" to={`/${contextType === "group" ? "groups" : "events"}/${contextId}`}>View {contextType}</ButtonLink>
    : <p className="text-sm text-text-muted">This {contextType} is not currently visible to you.</p>;
}

export function ContactGrantCard({ grant, onChanged, revealVariant = "primary" }: { grant: ContactGrant; onChanged: () => void; revealVariant?: "primary" | "secondary" }) {
  const [revealed, setRevealed] = useState<ContactReveal | null>(null);
  const [fields, setFields] = useState(grant.fields);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const canReveal = grant.canReveal && !grant.endedAt && grant.contextId !== undefined && Number.isSafeInteger(grant.contextId) && grant.contextId > 0;
  useEffect(() => {
    // A hidden tab, changed grant or unmount discards both the values and any
    // in-flight reveal response. Nothing is written to storage or shared data.
    const clear = () => { generation.current++; setRevealed(null); setBusy(false); };
    document.addEventListener("visibilitychange", clear);
    clear();
    return () => { generation.current++; document.removeEventListener("visibilitychange", clear); };
  }, [grant.id, grant.endedAt, grant.canReveal, grant.contextId]);

  async function reveal() {
    if (!canReveal || grant.contextId === undefined) return;
    const current = ++generation.current;
    setBusy(true); setError(""); setRevealed(null);
    try {
      const values = await Contacts.reveal(grant.id, { subjectUserId: grant.subjectUserId, contextType: grant.contextType, contextId: grant.contextId, fields });
      if (generation.current === current) setRevealed(values);
    } catch (e) { if (generation.current === current) setError(errorText(e)); }
    finally { if (generation.current === current) setBusy(false); }
  }
  function close() { generation.current++; setRevealed(null); setBusy(false); }
  async function revoke() {
    close();
    if (!confirm(`Revoke ${grant.requesterName}'s direct contact access to ${grant.subjectName}? This prevents future reveals, but cannot recall details already copied.`)) return;
    setBusy(true); setError("");
    try { await Contacts.revoke(grant.id); onChanged(); }
    catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  }
  return <section aria-label={`Contact grant for ${grant.subjectName}`} className="rounded-lg border border-border p-4 bg-surface space-y-3">
    <h3 className="font-semibold wrap-anywhere">{grant.requesterName} → {grant.subjectName}</h3>
    <p>{contactFieldsLabel(grant.fields)} · {grant.endedAt ? "Ended" : "Active grant"}</p>
    <p className="text-sm text-text-muted">Granted {formatWhen(grant.grantedAt)}{grant.endedAt && <> · Ended {formatWhen(grant.endedAt)}</>}</p>
    {grant.endReason && <p>{endLabels[grant.endReason]}</p>}
    <ContactContextLink {...grant} />
    {error && <FormError>{error}</FormError>}
    {canReveal && <>
      {!revealed && grant.fields === "both" && <Choices legend="Details to reveal">{(["email", "phone", "both"] as const).map((value) => <Radio key={value} name={`reveal-fields-${grant.id}`} checked={fields === value} onChange={() => setFields(value)} label={contactFieldsLabel(value)} />)}</Choices>}
      <p className="text-sm text-text-muted">Each reveal is recorded. Keep these details private. Closing clears them from this page.</p>
      {revealed ? <div role="region" aria-label="Revealed contact details" className="wrap-anywhere">
        {revealed.email !== undefined && <p>Email: {revealed.email || "Not provided"}</p>}
        {revealed.phone !== undefined && <p>Phone: {revealed.phone || "Not provided"}</p>}
      </div> : <Button variant={revealVariant} disabled={busy} onClick={() => void reveal()}>{busy ? "Opening…" : "Reveal contact details"}</Button>}
      {(revealed || busy) && <Button variant="secondary" onClick={close}>Close contact details</Button>}
    </>}
    {grant.canRevoke && !grant.endedAt && <Button variant="danger-outline" disabled={busy} onClick={() => void revoke()}>Revoke access</Button>}
  </section>;
}
