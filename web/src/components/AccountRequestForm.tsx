import { useState } from "react";
import { type AccountRequestInput } from "../api";
import type { AgeBand } from "../types";
import { errorText } from "../lib/errors";
import { Choices, Field, FormError, Honeypot, Input, Radio, Textarea } from "./ui/Field";
import { Button } from "./ui/Button";
import { EmptyState } from "./ui/EmptyState";
import { Illustration } from "./ui/Illustration";

// The account-request form shared by self-signup (/join) and a member's
// invite-a-friend (M1.3, 5.10). It carries the age gate (DEC-8): under
// 13 stores nothing and shows the household explanation instead; 13 to
// 17 needs a birth date. The hidden website field is the honeypot —
// people never see or fill it; bots do, and the server flags the row.

export function AccountRequestForm({
  submitLabel,
  onSubmit,
  subject = "self",
}: {
  submitLabel: string;
  onSubmit: (data: AccountRequestInput) => Promise<{ message: string }>;
  // self: the person filling this in. friend: a member asking for
  // someone else, so the hints speak about them, not the member.
  subject?: "self" | "friend";
}) {
  const friend = subject === "friend";
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [phone, setPhone] = useState("");
  const [ageBand, setAgeBand] = useState<AgeBand>("adult");
  const [birthDate, setBirthDate] = useState("");
  const [guardianEmail, setGuardianEmail] = useState("");
  const [message, setMessage] = useState("");
  const [website, setWebsite] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await onSubmit({
        name, email, phone: phone || undefined,
        ageBand, birthDate: ageBand === "teen" ? birthDate : undefined,
        guardianEmail: ageBand === "teen" && !friend ? guardianEmail || undefined : undefined,
        message: message || undefined, website: website || undefined,
      });
      setDone(res.message);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <EmptyState
        illustration={<Illustration name="request-sent" />}
        title="Request sent"
        body={<>{done} An admin reviews every request. Nothing else to do for now.</>}
      />
    );
  }

  return (
    <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
      {error && <FormError>{error}</FormError>}
      <Field id="arq-name" label="Full name">
        <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} autoComplete="name" required />
      </Field>
      <Field id="arq-email" label="Email" hint={<>{friend ? "This is the address they will sign in with." : "This is the address you will sign in with."}</>}>
        <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" required />
      </Field>
      <Field id="arq-phone" label="Phone" optional>
        <Input type="tel" value={phone} onChange={(e) => setPhone(e.target.value)} maxLength={30} autoComplete="tel" />
      </Field>
      <Choices legend={friend ? "Their age" : "Your age"}>
        <Radio name="ageBand" label="18 or older" checked={ageBand === "adult"} onChange={() => setAgeBand("adult")} />
        <Radio name="ageBand" label="13 to 17" checked={ageBand === "teen"} onChange={() => setAgeBand("teen")} />
        <Radio name="ageBand" label="Under 13" checked={ageBand === "under13"} onChange={() => setAgeBand("under13")} />
      </Choices>
      {ageBand === "teen" && (
        <>
        <Field id="arq-birth" label="Birth date" hint="Needed so Fold knows when you turn 18. A teen account also needs a parent or guardian’s consent before it can sign in.">
          <Input type="date" value={birthDate} onChange={(e) => setBirthDate(e.target.value)} required />
        </Field>
        {!friend && <Field id="arq-guardian" label="Parent or guardian's email" optional hint="An admin checks every guardian link. Your guardian needs their own adult account before they can give consent.">
          <Input type="email" value={guardianEmail} onChange={(e) => setGuardianEmail(e.target.value)} maxLength={254} />
        </Field>}
        </>
      )}
      {ageBand === "under13" && (
        <div className="rounded-md bg-warning-subtle border border-warning-border p-4 text-sm text-text-muted">
          Fold accounts start at 13, so there is nothing to submit here. Ask a parent or
          guardian to add you to their household instead — they can sign you up for events
          there. If you are 13 to 17 and have your own email, pick “13 to 17” above.
        </div>
      )}
      <Field id="arq-message" label="Note for the admins" optional>
        <Textarea rows={3} value={message} onChange={(e) => setMessage(e.target.value)} maxLength={1000} />
      </Field>
      <Honeypot value={website} onChange={setWebsite} />
      {ageBand !== "under13" && (
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Sending…" : submitLabel}
        </Button>
      )}
    </form>
  );
}
