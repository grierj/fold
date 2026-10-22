import { useCallback, useEffect, useRef, useState } from "react";
import { Households, type HouseholdPersonInput } from "../api";
import type { Household, HouseholdAccount, HouseholdPerson } from "../types";
import { errorText } from "../lib/errors";
import { formatBirthDate, formatWhen } from "../lib/when";
import { Button } from "./ui/Button";
import { Checkbox, Choices, Field, FormError, Input, Radio, showFieldErrors, Textarea } from "./ui/Field";
import { EmptyState } from "./ui/EmptyState";
import { ErrorNotice } from "./ErrorNotice";
import { HouseholdSkeleton } from "./HouseholdSkeleton";

// "My household" on the Profile page (M1.9 slice 1). GET never creates the
// household: it is null until the first person is added (Warden, PR 106).
// Children and teens wait for an admin to confirm the link (DEC-32).

const KIND_LABEL: Record<HouseholdPerson["kind"], string> = { child: "Child", teen: "Teen", adult: "Adult" };
const LINK_LABEL: Partial<Record<HouseholdPerson["linkStatus"], string>> = {
  pending: "Waiting for an admin to check",
  confirmed: "Confirmed",
  rejected: "Not confirmed",
  revoked: "Link ended",
};
const STONE = "inline-block rounded-full bg-sunken px-2 py-0.5 text-xs font-semibold text-text-muted";
const VIOLET = "inline-block rounded-full bg-primary-subtle px-2 py-0.5 text-xs font-semibold text-primary-on-subtle";

type Draft = { firstName: string; lastName: string; kind: HouseholdPerson["kind"]; birthDate: string; careNotes: string; photoConsent: boolean };
const emptyDraft: Draft = { firstName: "", lastName: "", kind: "child", birthDate: "", careNotes: "", photoConsent: false };

// personProblems checks a draft before it is sent. today is YYYY-MM-DD.
export function personProblems(d: Draft, today: string): { firstName?: string; birthDate?: string } {
  const out: { firstName?: string; birthDate?: string } = {};
  if (!d.firstName.trim()) out.firstName = "Add a first name.";
  if (d.kind !== "adult" && !d.birthDate) out.birthDate = "Add a birth date for a child or teen.";
  else if (d.birthDate && d.birthDate > today) out.birthDate = "A birth date can't be in the future.";
  return out;
}

function PersonForm({ initial, onSave, onCancel, heading, careVisible = true }: {
  careVisible?: boolean;
  initial: Draft;
  onSave: (input: HouseholdPersonInput) => Promise<void>;
  onCancel: () => void;
  heading: string;
}) {
  const [d, setD] = useState(initial);
  const [careTouched, setCareTouched] = useState(false);
  const [shown, setShown] = useState<ReturnType<typeof personProblems>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const formRef = useRef<HTMLFormElement>(null);
  const set = (patch: Partial<Draft>) => setD((prev) => ({ ...prev, ...patch }));

  // N5: focus First name when the form opens.
  useEffect(() => {
    formRef.current?.querySelector<HTMLInputElement>("input")?.focus();
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const problems = personProblems(d, new Date().toISOString().slice(0, 10));
    if (problems.firstName || problems.birthDate) {
      showFieldErrors(formRef.current, () => setShown(problems));
      return;
    }
    setShown({});
    setBusy(true);
    setError(null);
    try {
      await onSave({
        firstName: d.firstName.trim(), lastName: d.lastName.trim(), kind: d.kind,
        birthDate: d.birthDate, careNotes: careVisible || careTouched ? d.careNotes.trim() : undefined, photoConsent: d.photoConsent,
      });
    } catch (err) {
      setError(errorText(err, "We couldn't save that person."));
      setBusy(false);
    }
  }

  return (
    <form ref={formRef} onSubmit={submit} noValidate aria-label={heading} className="space-y-4 mt-4 rounded-lg border border-border p-4">
      <h3 className="font-semibold">{heading}</h3>
      <Field label="First name" error={shown.firstName}>
        <Input value={d.firstName} onChange={(e) => set({ firstName: e.target.value })} autoComplete="off" />
      </Field>
      <Field label="Last name" optional hint="Optional. Leave empty if they go by one name.">
        <Input value={d.lastName} onChange={(e) => set({ lastName: e.target.value })} autoComplete="off" />
      </Field>
      <Choices legend="Who are they?" hint="Children and teens are checked by an admin before the link is confirmed.">
        {(["child", "teen", "adult"] as const).map((k) => (
          <Radio key={k} name="household-kind" label={KIND_LABEL[k]} checked={d.kind === k} onChange={() => set({ kind: k })} />
        ))}
      </Choices>
      <Field label="Birth date" optional={d.kind === "adult"} hint={d.kind === "adult" ? "A birth date is needed before you can sign this adult up for an event." : undefined} error={shown.birthDate}>
        <Input type="date" value={d.birthDate} onChange={(e) => set({ birthDate: e.target.value })} layout="w-48" />
      </Field>
      <Field label="Care notes" optional hint="Admins and confirmed guardians can read these notes. Their event’s leaders and hosts can read them from 24 hours before it starts until 24 hours after it ends, while they are going.">
        <Textarea value={d.careNotes} onChange={(e) => { setCareTouched(true); set({ careNotes: e.target.value }); }} rows={3} maxLength={5000} />
      </Field>
      <Checkbox label="OK to appear in church photos" checked={d.photoConsent} onChange={(e) => set({ photoConsent: e.target.checked })} />
      {error && <FormError>{error}</FormError>}
      <div className="flex gap-3">
        <Button variant="primary" type="submit" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
        <Button variant="secondary" onClick={onCancel} disabled={busy}>Cancel</Button>
      </div>
    </form>
  );
}

function toDraft(p: HouseholdPerson): Draft {
  return { firstName: p.firstName, lastName: p.lastName, kind: p.kind, birthDate: p.birthDate ?? "", careNotes: p.careNotes ?? "", photoConsent: p.photoConsent };
}

export function HouseholdCard() {
  const [household, setHousehold] = useState<Household | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [status, setStatus] = useState("");
  const [metadata, setMetadata] = useState(false);
  const [converting, setConverting] = useState<number | null>(null);
  const metadataInvoker = useRef<HTMLButtonElement | null>(null);
  // After a save or removal, where focus goes once the list re-renders (M1).
  const focusNext = useRef<number | "add" | "heading" | null>(null);
  const cardRef = useRef<HTMLElement>(null);

  const load = useCallback(async () => {
    setLoadError(null);
    try {
      setHousehold((await Households.mine()).household);
    } catch (e) {
      setLoadError(errorText(e, "We couldn't load your household."));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { load(); }, [load]);

  useEffect(() => {
    const target = focusNext.current;
    if (target === null || editing !== null) return;
    focusNext.current = null;
    const root = cardRef.current;
    const el =
      typeof target === "number"
        ? root?.querySelector<HTMLElement>(`[data-person="${target}"] button`)
        : target === "add"
          ? root?.querySelector<HTMLElement>("[data-add]")
          : root?.querySelector<HTMLElement>("h2");
    (el ?? root?.querySelector<HTMLElement>("h2"))?.focus();
  }, [household, editing, converting]);

  async function remove(p: HouseholdPerson) {
    if (!confirm(`Remove ${p.name} from your household?${p.conversion ? " This also cancels their account invitation." : ""}`)) return;
    setError(null);
    try {
      await Households.removePerson(p.id);
      setStatus(`Removed ${p.name}.`);
      focusNext.current = "heading";
      await load();
    } catch (e) {
      setError(errorText(e, "We couldn't remove that person."));
    }
  }

  const people = household?.people ?? [];
  const addButton = (
    <span data-add>
      <Button variant={people.length ? "secondary" : "primary"} layout="mt-3" onClick={() => { setMetadata(false); setEditing("new"); }}>Add a household member</Button>
    </span>
  );

  if (loading) return <HouseholdSkeleton />;

  return (
    <section ref={cardRef} className="mt-6" aria-labelledby="household-heading">
      <h2 id="household-heading" tabIndex={-1} className="text-xl font-bold mb-1 focus:outline-none">My household</h2>
      <p className="text-sm mb-2"><a className="inline-flex min-h-11 items-center text-primary underline" href="/help/members">Accounts and household help</a></p>
      <p role="status" aria-live="polite" className="sr-only">{status}</p>
      {!loading && loadError && <ErrorNotice message={loadError} onRetry={() => { setLoading(true); load(); }} />}
      {!loading && !loadError && people.length === 0 && (household?.accounts.length ?? 0) <= 1 && editing !== "new" && !metadata && (
        <EmptyState
          level={2}
          title="No one in your household yet"
          body="Add your children, teens or other family members so you can sign them up for events together."
          action={<span data-add><Button variant="primary" onClick={() => { setMetadata(false); setEditing("new"); }}>Add a household member</Button></span>}
        />
      )}
      {household && (
        <div className="my-3">
          <div className="flex flex-wrap items-center gap-3">
            <h3 className="font-semibold break-words min-w-0">{household.name}</h3>
            <Button variant="secondary" disabled={metadata} onClick={(e) => { metadataInvoker.current = e.currentTarget; setEditing(null); setMetadata(true); }}>Rename</Button>
          </div>
          <p className="text-sm text-text-muted whitespace-pre-wrap break-words mt-2">{household.address || "No household address yet."}</p>
          <Button variant="secondary" layout="mt-2" disabled={metadata} onClick={(e) => { metadataInvoker.current = e.currentTarget; setEditing(null); setMetadata(true); }}>Edit address</Button>
          {metadata && <HouseholdForm household={household} onCancel={() => { setMetadata(false); requestAnimationFrame(() => metadataInvoker.current?.focus()); }} onSave={async (name, address) => {
            const result = await Households.update({ name, address });
            setHousehold(result.household); setMetadata(false); setStatus("Saved your household name and address.");
            requestAnimationFrame(() => metadataInvoker.current?.focus());
          }} />}
        </div>
      )}
      {household && (
        <>
          {household.accounts.length > 1 && (
            <p className="text-sm text-text-muted mb-3">
              Accounts: {household.accounts.map((a: HouseholdAccount) => `${a.name}${a.status === "pending_consent" ? " (Teen, waiting for consent)" : ""}`).join(", ")}
            </p>
          )}
          <ul className="space-y-3">
            {people.map((p) => (
              <li key={p.id} data-person={p.id} className="rounded-lg border border-border p-4">
                {editing === p.id ? (
                  <PersonForm
                    initial={toDraft(p)}
                    careVisible={p.careNotes !== undefined}
                    heading={`Edit ${p.name}`}
                    onCancel={() => { focusNext.current = p.id; setEditing(null); }}
                    onSave={async (input) => {
                      await Households.updatePerson(p.id, input);
                      setStatus(`Saved ${input.firstName}.`);
                      focusNext.current = p.id;
                      await load();
                      setEditing(null);
                    }}
                  />
                ) : (
                  <div className="flex items-start gap-3 flex-wrap">
                    <div className="grow min-w-0">
                      <div className="font-semibold">{p.name}</div>
                      <div className="mt-1 flex flex-wrap gap-2">
                        <span className={STONE}>{KIND_LABEL[p.kind]}</span>
                        {LINK_LABEL[p.linkStatus] && (
                          <span className={p.linkStatus === "confirmed" ? VIOLET : STONE}>{LINK_LABEL[p.linkStatus]}</span>
                        )}
                      </div>
                      {p.birthDate && <div className="text-sm text-text-muted mt-1">Born {formatBirthDate(p.birthDate)}</div>}
                      {p.careNotes && <div className="text-sm text-text-muted whitespace-pre-wrap wrap-anywhere min-w-0">Care notes: {p.careNotes}</div>}
                    </div>
                    <div className="flex flex-wrap gap-2">
                      <Button variant="secondary" aria-label={`Edit ${p.name}`} onClick={() => { setMetadata(false); setEditing(p.id); }}>Edit</Button>
                      <Button variant="danger-outline" aria-label={`Remove ${p.name}`} onClick={() => remove(p)}>Remove</Button>
                    </div>
                  </div>
                )}
                {p.conversion && <div className="mt-3 space-y-2">
                  <p className="text-sm text-text-muted break-words">{p.conversion.state === "expired" ? "Invitation expired" : "Invitation pending"}: {p.conversion.email}. Expires {formatWhen(p.conversion.expiresAt)}.</p>
                  <p className="text-sm text-text-muted">Changing their name, type or birth date cancels this invitation. Their sign-ups stay as they are until they accept.</p>
                  <ConversionActions person={p} onChanged={async (message) => { setStatus(message); focusNext.current = p.id; await load(); }} />
                </div>}
                {!p.conversion && p.canConvert && converting !== p.id && <Button variant="secondary" layout="mt-3" onClick={() => setConverting(p.id)}>Create their own account</Button>}
                {!p.conversion && converting === p.id && <ConversionForm person={p} onCancel={() => { focusNext.current = p.id; setConverting(null); }} onSent={async () => { focusNext.current = p.id; setConverting(null); setStatus(`Invitation sent for ${p.name}. Their sign-ups will move when they accept.`); await load(); }} />}
              </li>
            ))}
          </ul>
        </>
      )}
      {error && <FormError layout="mt-2">{error}</FormError>}
      {!loading && !loadError && editing === "new" && (
        <PersonForm
          initial={emptyDraft}
          heading="Add a household member"
          onCancel={() => { focusNext.current = "add"; setEditing(null); }}
          onSave={async (input) => {
            const { person } = await Households.addPerson(input);
            setStatus(`Added ${input.firstName}.`);
            focusNext.current = person.id;
            await load();
            setEditing(null);
          }}
        />
      )}
      {!loading && !loadError && (people.length > 0 || (household?.accounts.length ?? 0) > 1) && editing !== "new" && addButton}
    </section>
  );
}

function ConversionForm({ person, onCancel, onSent }: { person: HouseholdPerson; onCancel: () => void; onSent: () => Promise<void> }) {
  const [email, setEmail] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const ref = useRef<HTMLFormElement>(null);
  useEffect(() => { ref.current?.querySelector("input")?.focus(); }, []);
  async function submit(e: React.FormEvent) {
    e.preventDefault(); setBusy(true); setError(null);
    try { await Households.convert(person.id, email.trim()); await onSent(); }
    catch (e) { showFieldErrors(ref.current, () => setError(errorText(e, "We couldn't send this invitation. Try again."))); }
    finally { setBusy(false); }
  }
  return <form ref={ref} onSubmit={submit} className="mt-3 space-y-3" aria-label={`Create an account for ${person.name}`}>
    <p className="text-sm">Use their own email address. All past and upcoming sign-ups move to their new account only when they accept. Group memberships do not move.</p>
    {person.kind === "teen" && <p className="text-sm">Their account will wait for consent and a fresh guardian check. They cannot sign in yet.</p>}
    <Field label="Their email address" hint="Required. Use an email that has no account or open invitation." error={error ?? undefined}><Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoComplete="off" /></Field>
    <div className="flex flex-wrap gap-3"><Button variant="primary" type="submit" disabled={busy}>{busy ? "Sending…" : "Send account invitation"}</Button><Button variant="secondary" onClick={onCancel} disabled={busy}>Cancel</Button></div>
  </form>;
}

function ConversionActions({ person, onChanged }: { person: HouseholdPerson; onChanged: (message: string) => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function act(cancel: boolean) {
    setBusy(true); setError(null);
    try {
      if (cancel) await Households.cancelConversion(person.id); else await Households.resendConversion(person.id);
      await onChanged(cancel ? "Invitation cancelled. Their sign-ups are unchanged." : "Invitation resent. The previous link can no longer be used.");
    } catch (e) { setError(errorText(e, "We couldn't update the invitation. Try again.")); }
    finally { setBusy(false); }
  }
  return <div>
    <div className="flex flex-wrap gap-3"><Button variant="secondary" disabled={busy || !person.canConvert} onClick={() => void act(false)}>Resend</Button><Button variant="danger-outline" disabled={busy} onClick={() => void act(true)}>Cancel invitation</Button></div>
    {error && <ErrorNotice message={error} />}
  </div>;
}

function HouseholdForm({ household, onSave, onCancel }: { household: Household; onSave: (name: string, address: string) => Promise<void>; onCancel: () => void }) {
  const [name, setName] = useState(household.name);
  const [address, setAddress] = useState(household.address);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | undefined>();
  const root = useRef<HTMLFormElement>(null);
  useEffect(() => { root.current?.querySelector("input")?.focus(); }, []);
  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim() || [...name.trim()].length > 100) { showFieldErrors(root.current, () => setNameError("Enter a household name of 1 to 100 characters.")); return; }
    setBusy(true); setError(null); setNameError(undefined);
    try { await onSave(name.trim(), address.trim()); }
    catch (e) { setError(errorText(e, "We couldn't save your household.")); setBusy(false); }
  }
  return <form ref={root} aria-label="Edit household" onSubmit={save} className="mt-3 space-y-3 rounded-lg border border-border p-4">
    <Field label="Household name" hint="Required. Up to 100 characters." error={nameError}><Input value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} /></Field>
    <Field label="Address" optional hint="Optional. Up to 500 characters."><Textarea value={address} onChange={(e) => setAddress(e.target.value)} rows={3} maxLength={500} /></Field>
    {error && <FormError>{error}</FormError>}
    <div className="flex flex-wrap gap-3"><Button variant="primary" type="submit" disabled={busy}>{busy ? "Saving…" : "Save household"}</Button><Button variant="secondary" onClick={onCancel} disabled={busy}>Cancel</Button></div>
  </form>;
}
