import { useEffect, useState } from "react";
import { ContactHistory, retainedPersonId } from "../contact-history-api";
import type { ContactHistoryPerson } from "../types";
import { Field, Input, Select } from "./ui/Field";

export function ContactHistoryPersonPicker({ value, name, error, onChange, onKnown, onFailure }: {
  value: string;
  name?: string;
  error: string;
  onChange: (id: string) => void;
  onKnown: (person: ContactHistoryPerson) => void;
  onFailure: (error: unknown) => void;
}) {
  const [query, setQuery] = useState("");
  const [people, setPeople] = useState<ContactHistoryPerson[]>([]);
  const [resolved, setResolved] = useState<ContactHistoryPerson | null>(null);
  const [busy, setBusy] = useState(true);
  const [advanced, setAdvanced] = useState(false);

  useEffect(() => {
    let active = true;
    setBusy(true);
    setPeople([]);
    const timer = window.setTimeout(() => {
      ContactHistory.people({ q: query.trim() }).then((data) => {
        if (active) setPeople(data.people);
      }).catch((e: unknown) => {
        if (active) onFailure(e);
      }).finally(() => {
        if (active) setBusy(false);
      });
    }, query.trim() ? 250 : 0);
    return () => { active = false; window.clearTimeout(timer); };
  }, [query, onFailure]);

  useEffect(() => {
    const id = retainedPersonId(value);
    if (!id || name) return;
    let active = true;
    const timer = window.setTimeout(() => {
      ContactHistory.people({ id }).then((data) => {
        if (active && data.people[0]) {
          setResolved(data.people[0]);
          onKnown(data.people[0]);
        }
      }).catch((e: unknown) => {
        if (active) onFailure(e);
      });
    }, 250);
    return () => { active = false; window.clearTimeout(timer); };
  }, [value, name, onKnown, onFailure]);

  const selected = people.find((p) => p.id === value) ?? (resolved?.id === value ? resolved : null);
  const selectedLabel = selected || name ? "Selected person" : "Saved person ID";
  const options = [{ value: "", label: busy ? "Searching…" : "Choose a person" }, ...people.map((p) => ({ value: p.id, label: p.id === value ? selectedLabel : `${p.name} — ${p.email}` }))];
  if (value && !people.some((p) => p.id === value)) options.splice(1, 0, { value, label: selectedLabel });
  const selectedHint = value && <span className="block wrap-anywhere">
    {selected ? <><span className="block font-medium text-text">{selected.name}</span>{" "}<span className="block">{selected.email}</span></> : name ? <span className="block font-medium text-text">{name}</span> : `Saved person ID: ${value}`}
  </span>;

  return <div className="space-y-3">
    <Field id="history-person-search" label="Find a person" hint="Search by name or email, then choose a person below.">
      <Input type="search" value={query} onChange={(e) => setQuery(e.target.value)} autoComplete="off" />
    </Field>
    <Field id="history-person" label="Person" hint={selectedHint}>
      <Select required value={value} options={options} onChange={(e) => {
        const id = e.target.value;
        const person = people.find((p) => p.id === id);
        if (person) {
          setResolved(person);
          onKnown(person);
        }
        onChange(id);
      }} />
    </Field>
    {!busy && !people.length && query.trim() && <p className="text-sm text-text-muted">No matching people. For a deleted account, use its saved person ID below.</p>}
    <details open={advanced || Boolean(error)} onToggle={(e) => setAdvanced(e.currentTarget.open)}>
      <summary className="min-h-11 cursor-pointer py-2 text-sm text-text-muted">Use a saved person ID</summary>
      <Field id="history-retained-person-id" label="Saved person ID" hint="For a deleted account, enter the ID saved before it was deleted. Use digits only." error={error}>
        <Input inputMode="numeric" value={value} onChange={(e) => onChange(e.target.value)} autoComplete="off" />
      </Field>
    </details>
  </div>;
}
