import { useState } from "react";
import { ApiError, Guests } from "../api";
import type { EventOccurrence } from "../types";

// The guest door on one public event date (M1.4, 5.10): a signed-out
// adult registers with a name and an email. The server emails a
// confirmation link and nothing is registered until that link is
// redeemed, so this form only ever ends in "check your email". The
// hidden website field is the honeypot the account form also carries.
// A failed submit keeps what was typed, like every other Fold form.
export function GuestRegistrationForm({ instance }: { instance: EventOccurrence }) {
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
        website: website || undefined,
      });
      setDone(res.message);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  if (!open && !done) {
    return (
      <button type="button" onClick={() => setOpen(true)} className="text-sm text-brand font-semibold">
        Register as a guest
      </button>
    );
  }

  if (done) {
    return (
      <div className="rounded-lg border border-gray-200 bg-gray-50 p-4">
        <p className="text-sm text-gray-700">{done}</p>
        <p className="text-sm text-gray-500 mt-1">
          Your registration is confirmed when you open the link in that email.
        </p>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="rounded-lg border border-gray-200 p-4 space-y-3 max-w-md">
      <p className="text-sm font-semibold">Register as a guest</p>
      {error && <div className="text-red-600 text-sm">{error}</div>}
      <div>
        <label className="label" htmlFor={`${p}-name`}>Full name</label>
        <input id={`${p}-name`} className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} autoComplete="name" required />
        <p className="text-xs text-gray-500 mt-1">Shown on the attendee list. At most 100 characters.</p>
      </div>
      <div>
        <label className="label" htmlFor={`${p}-email`}>Email</label>
        <input id={`${p}-email`} type="email" className="input" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" required />
        <p className="text-xs text-gray-500 mt-1">
          We email you a link to confirm, and later a link to cancel. Registering again with
          the same email reuses the same guest record.
        </p>
      </div>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={over18} onChange={(e) => setOver18(e.target.checked)} />
        <span>
          I am 18 or older. Guests registering on their own must be 18 or older; a parent or
          guardian can register a younger person from their own account.
        </span>
      </label>
      <label className="flex items-start gap-2 text-sm">
        <input type="checkbox" className="mt-0.5" checked={createAccount} onChange={(e) => setCreateAccount(e.target.checked)} />
        <span>
          Also ask for a Fold account. An admin reviews the request, and this registration
          stays yours if it is approved.
        </span>
      </label>
      <div className="hidden" aria-hidden="true">
        <label>
          Website
          <input name="website" tabIndex={-1} autoComplete="off" value={website} onChange={(e) => setWebsite(e.target.value)} />
        </label>
      </div>
      <div className="flex items-center gap-4">
        <button type="submit" disabled={busy} className="btn-primary">
          {busy ? "Sending…" : "Email me a confirmation link"}
        </button>
        <button type="button" onClick={() => setOpen(false)} className="text-sm text-gray-500">
          Never mind
        </button>
      </div>
    </form>
  );
}
