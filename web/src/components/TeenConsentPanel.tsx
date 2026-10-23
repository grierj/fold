import { useCallback, useEffect, useRef, useState } from "react";
import { AdminTeenConsents } from "../api";
import type { TeenConsent, User } from "../types";
import { errorText } from "../lib/errors";
import { formatWhen } from "../lib/when";
import { Button } from "./ui/Button";
import { Choices, Field, FormError, Radio, showFieldErrors, Textarea } from "./ui/Field";

const scopes = [
  { value: "account", label: "Account access" },
  { value: "sms", label: "SMS" },
  { value: "photos", label: "Photos" },
] satisfies { value: TeenConsent["scope"]; label: string }[];

export default function TeenConsentPanel({ user, onUpdated, onClose }: { user: User; onUpdated: () => void; onClose: () => void }) {
  const [history, setHistory] = useState<TeenConsent[]>([]);
  const [suspended, setSuspended] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [scope, setScope] = useState<TeenConsent["scope"]>("account");
  const [evidence, setEvidence] = useState("");
  const [evidenceError, setEvidenceError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [announcement, setAnnouncement] = useState("");
  const ref = useRef<HTMLDivElement>(null);
  const load = useCallback(async () => {
    const result = await AdminTeenConsents.list(user.id);
    setHistory(result.consents);
    setSuspended(result.administrativelyDisabled);
    setLoaded(true);
  }, [user.id]);
  useEffect(() => { load().catch((e) => setError(errorText(e, "We couldn't load consent history."))); }, [load]);

  async function change(status: TeenConsent["status"]) {
    if (status === "granted" && !evidence.trim()) {
      showFieldErrors(ref.current, () => setEvidenceError("Write how you received consent first."));
      return;
    }
    if (status === "revoked" && !confirm(scope === "account"
      ? `Revoke account consent for ${user.displayName || user.name}? They will be signed out and can't sign in.`
      : `Revoke ${scope === "sms" ? "SMS" : "photo"} consent for ${user.displayName || user.name}?`)) return;
    setBusy(true);
    setError(null);
    setEvidenceError(null);
    setAnnouncement("");
    try {
      await AdminTeenConsents.change(user.id, { scope, status, evidenceText: evidence.trim() });
      await load();
      setEvidence("");
      setAnnouncement(`${scopes.find((item) => item.value === scope)?.label} consent ${status}.`);
      onUpdated();
    } catch (e) {
      setError(errorText(e, "We couldn't save consent. Please try again."));
    } finally { setBusy(false); }
  }

  return (
    <div ref={ref} className="mt-4 border-t border-border pt-4 space-y-4 whitespace-normal" aria-label={`Consent for ${user.displayName || user.name}`}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="font-semibold">Teen consent</h3>
        <Button variant="ghost" onClick={onClose}>Close consent</Button>
      </div>
      {!loaded && !error && <p role="status">Loading consent…</p>}
      {loaded && <>
        <ul className="space-y-1">
          {scopes.map((item) => <li key={item.value}>
            {item.label}: <strong>{history.find((row) => row.scope === item.value)?.status === "granted" ? "Granted" : "Not granted"}</strong>
          </li>)}
        </ul>
        {user.accountType === "teen" ? <>
          {suspended && <p className="text-text-muted">An admin has disabled this account. Granting consent keeps it disabled until an admin enables it.</p>}
          {!user.hasPassword && <p className="text-text-muted">They still need to set a password before they can sign in.</p>}
          <Choices legend="Consent scope">
            {scopes.map((item) => <Radio key={item.value} name={`consent-scope-${user.id}`} label={item.label} checked={scope === item.value} onChange={() => setScope(item.value)} />)}
          </Choices>
          <Field label="Consent evidence" hint="Required to grant. For example: paper form signed by a parent on October 22. Maximum 1,000 characters. You can add a reason when revoking." error={evidenceError ?? undefined}>
            <Textarea value={evidence} onChange={(event) => { setEvidence(event.target.value); setEvidenceError(null); }} maxLength={1000} rows={3} />
          </Field>
          <p className="text-sm text-text-muted">SMS consent also needs their own SMS opt-in before any texts can be sent. Photo consent applies to galleries.</p>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" onClick={() => void change("granted")} disabled={busy}>{busy ? "Saving…" : "Grant consent"}</Button>
            <Button variant="danger-outline" onClick={() => void change("revoked")} disabled={busy}>Revoke consent</Button>
          </div>
        </> : <p className="text-text-muted">This person is now an adult. Their teen consent history stays on file.</p>}
        <details>
          <summary className="cursor-pointer py-3">Consent history ({history.length})</summary>
          {history.length ? <ol className="space-y-3 mt-2">
            {history.map((row) => <li key={row.id} className="border-l-2 border-border pl-3 break-words">
              <div>{scopes.find((item) => item.value === row.scope)?.label}: {row.status}</div>
              <div className="text-xs text-text-muted">{formatWhen(row.at)} · {row.granterRole}</div>
              {row.evidenceText && <p className="mt-1 whitespace-pre-wrap">{row.evidenceText}</p>}
            </li>)}
          </ol> : <p>No consent has been recorded. Choose a scope and record evidence to grant consent.</p>}
        </details>
      </>}
      {error && <FormError>{error}</FormError>}
      <p role="status" className="text-success empty:hidden">{announcement}</p>
    </div>
  );
}
