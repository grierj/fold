import { useState } from "react";
import { Guests } from "../api";
import type { EventOccurrence } from "../types";
import { browserTimeZone } from "../lib/when";
import { errorText } from "../lib/errors";
import { Checkbox, Field, FormError, Honeypot, Input } from "./ui/Field";
import { FormActions } from "./ui/FormActions";
import { Button } from "./ui/Button";

// The guest door on one public event date (M1.4, 5.10): a signed-out
// adult registers with a name and an email. The server emails a
// confirmation link and nothing is registered until that link is
// redeemed, so this form only ever ends in "check your email". The
// hidden website field is the honeypot the account form also carries.
// A failed submit keeps what was typed, like every other Fold form.
export function GuestRegistrationForm({ instance }: { instance: Pick<EventOccurrence, "id" | "eventId" | "date"> }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [over18, setOver18] = useState(false);
  const [createAccount, setCreateAccount] = useState(false);
  const [website, setWebsite] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const p = `guest-${instance.id}`;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await Guests.register(instance.eventId, {
        name, email, date: instance.date, over18,
        createAccount: createAccount || undefined,
        timezone: browserTimeZone(),
        website: website || undefined,
      });
      setDone(res.message);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  }

  if (!open && !done) {
    return (
      <Button variant="secondary" onClick={() => setOpen(true)}>
        Register as a guest
      </Button>
    );
  }

  if (done) {
    return (
      <div className="rounded-md border border-border bg-page p-4">
        <p className="text-sm text-text-muted">{done}</p>
        <p className="text-sm text-text-muted mt-1">
          Your registration is confirmed when you open the link in that email.
        </p>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="rounded-md border border-border p-4 space-y-3 max-w-md">
      <p className="text-sm font-semibold">Register as a guest</p>
      {error && <FormError>{error}</FormError>}
      <Field id={`${p}-name`} label="Full name" hint="Shown on the attendee list.">
        <Input value={name} onChange={(e) => setName(e.target.value)} maxLength={100} autoComplete="name" required />
      </Field>
      <Field id={`${p}-email`} label="Email" hint="We'll email you a link to confirm. Use the same email next time to keep your sign-ups together.">
        <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" required />
      </Field>
      <div>
        <Checkbox
          label="I am 18 or older"
          description="Guests registering on their own must be 18 or older; a parent or guardian can register a younger person from their own account."
          checked={over18}
          onChange={(e) => setOver18(e.target.checked)}
        />
        <Checkbox
          label="Also ask for a Fold account"
          description="An admin reviews the request, and this registration stays yours if it is approved."
          checked={createAccount}
          onChange={(e) => setCreateAccount(e.target.checked)}
        />
      </div>
      <Honeypot value={website} onChange={setWebsite} />
      <FormActions>
        <Button variant="primary" layout="w-full sm:w-auto" type="submit" disabled={busy}>
          {busy ? "Sending…" : "Email me a confirmation link"}
        </Button>
        <Button variant="secondary" layout="w-full sm:w-auto" onClick={() => setOpen(false)}>
          Never mind
        </Button>
      </FormActions>
    </form>
  );
}
