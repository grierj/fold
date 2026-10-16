import { useState } from "react";
import { ApiError, type AccountRequestInput } from "../api";
import type { AgeBand } from "../types";

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
        message: message || undefined, website: website || undefined,
      });
      setDone(res.message);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : err instanceof Error ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  }

  if (done) {
    return (
      <div className="bg-surface rounded-lg border border-border p-6">
        <p className="text-sm text-text-muted">{done}</p>
        <p className="text-sm text-text-muted mt-2">
          An admin reviews every request. Nothing else to do for now.
        </p>
      </div>
    );
  }

  return (
    <form onSubmit={submit} className="bg-surface rounded-lg border border-border p-6 space-y-4">
      {error && <div className="text-danger text-sm">{error}</div>}
      <div>
        <label className="label" htmlFor="arq-name">Full name</label>
        <input id="arq-name" className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} autoComplete="name" required />
      </div>
      <div>
        <label className="label" htmlFor="arq-email">Email</label>
        <input id="arq-email" type="email" className="input" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" required />
        <p className="text-xs text-text-muted mt-1">
          {friend ? "This is the address they will sign in with." : "This is the address you will sign in with."}
        </p>
      </div>
      <div>
        <label className="label" htmlFor="arq-phone">Phone (optional)</label>
        <input id="arq-phone" type="tel" className="input" value={phone} onChange={(e) => setPhone(e.target.value)} maxLength={30} autoComplete="tel" />
      </div>
      <fieldset>
        <legend className="label">{friend ? "Their age" : "Your age"}</legend>
        <div className="space-y-2 text-sm">
          <label className="flex items-center gap-2">
            <input type="radio" name="ageBand" checked={ageBand === "adult"} onChange={() => setAgeBand("adult")} />
            18 or older
          </label>
          <label className="flex items-center gap-2">
            <input type="radio" name="ageBand" checked={ageBand === "teen"} onChange={() => setAgeBand("teen")} />
            13 to 17
          </label>
          <label className="flex items-center gap-2">
            <input type="radio" name="ageBand" checked={ageBand === "under13"} onChange={() => setAgeBand("under13")} />
            Under 13
          </label>
        </div>
      </fieldset>
      {ageBand === "teen" && (
        <div>
          <label className="label" htmlFor="arq-birth">Birth date</label>
          <input id="arq-birth" type="date" className="input" value={birthDate} onChange={(e) => setBirthDate(e.target.value)} required />
          <p className="text-xs text-text-muted mt-1">
            Needed so Fold knows when you turn 18. A teen account also needs a parent or
            guardian&rsquo;s consent before it can sign in.
          </p>
        </div>
      )}
      {ageBand === "under13" && (
        <div className="rounded-md bg-warning-subtle border border-warning-border p-4 text-sm text-text-muted">
          Fold accounts start at 13, so there is nothing to submit here. Ask a parent or
          guardian to add you to their household instead — they can sign you up for events
          there. If you are 13 to 17 and have your own email, pick “13 to 17” above.
        </div>
      )}
      <div>
        <label className="label" htmlFor="arq-message">Note for the admins (optional)</label>
        <textarea id="arq-message" className="input" rows={3} value={message} onChange={(e) => setMessage(e.target.value)} maxLength={1000} />
      </div>
      <div className="hidden" aria-hidden="true">
        <label>
          Website
          <input name="website" tabIndex={-1} autoComplete="off" value={website} onChange={(e) => setWebsite(e.target.value)} />
        </label>
      </div>
      {ageBand !== "under13" && (
        <button type="submit" disabled={busy} className="btn-primary w-full">
          {busy ? "Sending…" : submitLabel}
        </button>
      )}
    </form>
  );
}
