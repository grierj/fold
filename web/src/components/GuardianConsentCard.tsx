import { useEffect, useState } from "react";
import { Guardians, type GuardianConsentScope } from "../guardian-api";
import type { GuardianTeen } from "../types";
import { errorText } from "../lib/errors";
import { Button } from "./ui/Button";
import { Field, FormError, Input, Select } from "./ui/Field";

function TeenConsentControls({ teen, onChanged }: { teen: GuardianTeen; onChanged: () => Promise<void> }) {
  const [scope, setScope] = useState<GuardianConsentScope>("account");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const granted = scope === "account" ? teen.accountConsent : scope === "sms" ? teen.smsConsent : teen.photosConsent;

  async function change(status: "granted" | "revoked") {
    if (status === "revoked" && !confirm(scope === "account" ? `Revoke account consent for ${teen.teenName}? They will be signed out and unable to use Fold until consent is granted again.` : `Revoke ${scope} consent for ${teen.teenName}?`)) return;
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await Guardians.change(teen.linkId, scope, status);
      await onChanged();
      setMessage(`${scope === "sms" ? "SMS" : scope === "photos" ? "Photo" : "Account"} consent ${status}.`);
    } catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  }

  async function email() {
    setBusy(true);
    setError("");
    try { setMessage((await Guardians.email(teen.linkId)).message); }
    catch (e) { setError(errorText(e)); }
    finally { setBusy(false); }
  }

  return <div className="border-t border-border pt-4 mt-4">
    <h3 className="font-semibold">{teen.teenName}</h3>
    <Field id={`guardian-scope-${teen.linkId}`} label="Consent" hint="Account consent lets your teen use Fold. SMS consent also needs their own opt-in before any texts can be sent.">
      <Select value={scope} onChange={(e) => {
        const value = e.target.value;
        if (value === "account" || value === "sms" || value === "photos") { setScope(value); setMessage(""); }
      }}>
        <option value="account">Account</option><option value="sms">SMS</option><option value="photos">Photos</option>
      </Select>
    </Field>
    <p className="text-sm text-text-muted mt-2">{granted ? "Consent granted" : "Consent not granted"}</p>
    {error && <FormError layout="mt-2">{error}</FormError>}
    {message && <p role="status" className="text-sm mt-2">{message}</p>}
    <div className="flex flex-wrap gap-3 mt-3">
      {granted ? <Button key="revoke" variant="danger-outline" disabled={busy} onClick={() => change("revoked")}>Revoke consent</Button> : <Button key="grant" variant="primary" disabled={busy} onClick={() => change("granted")}>Grant consent</Button>}
      {scope === "account" && <Button variant="secondary" disabled={busy} onClick={email}>Email me a consent link</Button>}
    </div>
  </div>;
}

export function GuardianConsentCard() {
  const [teens, setTeens] = useState<GuardianTeen[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [email, setEmail] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function load() { setTeens((await Guardians.teens()).teens); }
  useEffect(() => { load().catch((e: unknown) => setError(errorText(e))).finally(() => setLoaded(true)); }, []);

  async function claim(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try { setMessage((await Guardians.claim(email)).message); setEmail(""); }
    catch (err) { setError(errorText(err)); }
    finally { setBusy(false); }
  }

  return <section className="bg-surface rounded-lg border border-border p-6 mb-6" aria-label="Teen consent">
    <h2 className="text-xl font-bold">Teen consent</h2>
    <p className="text-sm text-text-muted mt-2">You can manage consent after an admin confirms your guardian link.</p>
    {error && <FormError layout="mt-3">{error}</FormError>}
    {!loaded && <p className="text-sm mt-3">Loading your guardian links…</p>}
    {teens.map((teen) => <TeenConsentControls key={teen.linkId} teen={teen} onChanged={load} />)}
    <form onSubmit={claim} className="border-t border-border pt-4 mt-4 space-y-3">
      <h3 className="font-semibold">Request a guardian link</h3>
      <Field id="guardian-claim-email" label="Teen's email" hint="Use the email on their Fold account. An admin checks every request before you can give consent.">
        <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} maxLength={254} required />
      </Field>
      <Button variant="secondary" type="submit" disabled={busy}>{busy ? "Sending…" : "Request guardian link"}</Button>
      {message && <p role="status" className="text-sm">{message}</p>}
    </form>
  </section>;
}
