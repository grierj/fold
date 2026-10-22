import { useCallback, useEffect, useState } from "react";
import { Households, type HouseholdPersonInput } from "../api";
import type { Household, HouseholdAccount, HouseholdPerson } from "../types";
import { errorText } from "../lib/errors";
import { Button } from "./ui/Button";
import { Checkbox, Choices, Field, FormError, Input, Radio, Textarea } from "./ui/Field";
import { Skeleton } from "./ui/Skeleton";

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

function PersonForm({ initial, onSave, onCancel, submitLabel }: {
  initial: Draft;
  onSave: (input: HouseholdPersonInput) => Promise<void>;
  onCancel: () => void;
  submitLabel: string;
}) {
  const [d, setD] = useState(initial);
  const [shown, setShown] = useState<ReturnType<typeof personProblems>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const set = (patch: Partial<Draft>) => setD((prev) => ({ ...prev, ...patch }));

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const problems = personProblems(d, new Date().toISOString().slice(0, 10));
    setShown(problems);
    if (problems.firstName || problems.birthDate) return;
    setBusy(true);
    setError(null);
    try {
      await onSave({
        firstName: d.firstName.trim(), lastName: d.lastName.trim() || undefined, kind: d.kind,
        birthDate: d.birthDate || undefined, careNotes: d.careNotes.trim() || undefined, photoConsent: d.photoConsent,
      });
    } catch (err) {
      setError(errorText(err, "We couldn't save that person."));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} noValidate className="space-y-4 mt-4 rounded-lg border border-border p-4">
      <Field label="First name" error={shown.firstName}>
        <Input value={d.firstName} onChange={(e) => set({ firstName: e.target.value })} autoComplete="off" />
      </Field>
      <Field label="Last name" optional hint="Leave empty to use the household's name.">
        <Input value={d.lastName} onChange={(e) => set({ lastName: e.target.value })} autoComplete="off" />
      </Field>
      <Choices legend="Who are they?" hint="Children and teens are checked by an admin before the link is confirmed.">
        {(["child", "teen", "adult"] as const).map((k) => (
          <Radio key={k} name="household-kind" label={KIND_LABEL[k]} checked={d.kind === k} onChange={() => set({ kind: k })} />
        ))}
      </Choices>
      <Field label="Birth date" optional={d.kind === "adult"} error={shown.birthDate}>
        <Input type="date" value={d.birthDate} onChange={(e) => set({ birthDate: e.target.value })} layout="w-48" />
      </Field>
      <Field label="Care notes" optional hint="Allergies or anything leaders should know. Only leaders of events they attend see this.">
        <Textarea value={d.careNotes} onChange={(e) => set({ careNotes: e.target.value })} rows={3} maxLength={1000} />
      </Field>
      <Checkbox label="OK to appear in church photos" checked={d.photoConsent} onChange={(e) => set({ photoConsent: e.target.checked })} />
      {error && <FormError>{error}</FormError>}
      <div className="flex gap-3">
        <Button variant="primary" type="submit" disabled={busy}>{busy ? "Saving…" : submitLabel}</Button>
        <Button variant="secondary" onClick={onCancel} disabled={busy}>Cancel</Button>
      </div>
    </form>
  );
}

function toDraft(p: HouseholdPerson): Draft {
  return { firstName: p.firstName, lastName: p.lastName, kind: p.kind, birthDate: p.birthDate ?? "", careNotes: p.careNotes, photoConsent: p.photoConsent };
}

export function HouseholdCard() {
  const [household, setHousehold] = useState<Household | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [status, setStatus] = useState("");

  const load = useCallback(async () => {
    setError(null);
    try {
      setHousehold((await Households.mine()).household);
    } catch (e) {
      setError(errorText(e, "We couldn't load your household."));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { load(); }, [load]);

  async function remove(p: HouseholdPerson) {
    if (!confirm(`Remove ${p.name} from your household?`)) return;
    try {
      await Households.removePerson(p.id);
      setStatus(`Removed ${p.name}.`);
      await load();
    } catch (e) {
      setError(errorText(e, "We couldn't remove that person."));
    }
  }

  return (
    <section className="mt-6" aria-labelledby="household-heading">
      <h2 id="household-heading" className="text-xl font-bold mb-1">My household</h2>
      <p role="status" aria-live="polite" className="sr-only">{status}</p>
      {loading && <Skeleton kind="lines" />}
      {!loading && error && <FormError>{error}</FormError>}
      {!loading && !household && (
        <p className="text-sm text-text-muted mb-3">
          Add your children, teens or other family members so you can sign them up for events together.
        </p>
      )}
      {household && (
        <>
          <p className="text-sm text-text-muted mb-3">{household.name}</p>
          {household.accounts.length > 1 && (
            <p className="text-sm text-text-muted mb-3">
              Accounts: {household.accounts.map((a: HouseholdAccount) => a.name).join(", ")}
            </p>
          )}
          <ul className="space-y-3">
            {household.people.map((p) => (
              <li key={p.id} className="rounded-lg border border-border p-4">
                {editing === p.id ? (
                  <PersonForm
                    initial={toDraft(p)}
                    submitLabel="Save"
                    onCancel={() => setEditing(null)}
                    onSave={async (input) => {
                      await Households.updatePerson(p.id, input);
                      setEditing(null);
                      setStatus(`Saved ${input.firstName}.`);
                      await load();
                    }}
                  />
                ) : (
                  <div className="flex items-start gap-3 flex-wrap">
                    <div className="grow min-w-0">
                      <span className="font-semibold">{p.name}</span>
                      <span className="ml-2 inline-block rounded-full bg-info-subtle px-2 py-0.5 text-xs font-semibold text-info-on-subtle">{KIND_LABEL[p.kind]}</span>
                      {LINK_LABEL[p.linkStatus] && (
                        <span className="ml-2 inline-block rounded-full bg-sunken px-2 py-0.5 text-xs font-semibold text-text-muted">{LINK_LABEL[p.linkStatus]}</span>
                      )}
                      {p.birthDate && <div className="text-sm text-text-muted mt-0.5">Born {p.birthDate}</div>}
                      {p.careNotes && <div className="text-sm text-text-muted">Care notes: {p.careNotes}</div>}
                    </div>
                    <div className="flex gap-2">
                      <Button variant="secondary" size="sm" onClick={() => setEditing(p.id)}>Edit<span className="sr-only"> {p.name}</span></Button>
                      <Button variant="danger-outline" size="sm" onClick={() => remove(p)}>Remove<span className="sr-only"> {p.name}</span></Button>
                    </div>
                  </div>
                )}
              </li>
            ))}
          </ul>
        </>
      )}
      {!loading && editing === "new" && (
        <PersonForm
          initial={emptyDraft}
          submitLabel="Add to household"
          onCancel={() => setEditing(null)}
          onSave={async (input) => {
            await Households.addPerson(input);
            setEditing(null);
            setStatus(`Added ${input.firstName}.`);
            await load();
          }}
        />
      )}
      {!loading && editing !== "new" && (
        <Button variant="secondary" layout="mt-3" onClick={() => setEditing("new")}>Add a household member</Button>
      )}
    </section>
  );
}
