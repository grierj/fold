import { Form } from "../components/ui/Form";
import { useCallback, useEffect, useRef, useState } from "react";
import { Navigate, useLocation, useParams } from "react-router-dom";
import { ApiError, Site } from "../api";
import { useAuth } from "../auth";
import { ContactHistory, retainedPersonId, type ContactHistoryFilters } from "../contact-history-api";
import type { ContactHistoryEntry, ContactHistoryPerson } from "../types";
import { ContactHistoryPersonPicker } from "../components/ContactHistoryPersonPicker";
import { Button, ButtonLink } from "../components/ui/Button";
import { Choices, Field, FormError, Input, Radio, Select, showFieldErrors } from "../components/ui/Field";
import { EmptyState } from "../components/ui/EmptyState";
import { errorText } from "../lib/errors";
import { formatBirthDate, formatWhen, historyDayBounds } from "../lib/when";

const emptyFilters: ContactHistoryFilters = { purpose: "", action: "", from: "", until: "" };
const actions: Record<string, string> = {
  "contact.requested": "Contact requested", "contact.decision": "Contact decision",
  "contact.withdrawn": "Contact request withdrawn", "contact.expired": "Contact request expired",
  "contact.granted": "Contact access granted", "contact.reveal": "Contact details revealed",
  "contact.refused": "Contact attempt refused", "contact.revoked": "Contact access revoked", "contact.ended": "Contact access ended",
  "add.requested": "Add requested", "add.decision": "Add decision", "add.withdrawn": "Add request withdrawn",
  "add.expired": "Add request expired", "add.completed": "Add completed", "add.refused": "Add attempt refused", "add.ended": "Add request ended",
  "occurrence_access.added": "One-date access added", "occurrence_access.ended": "One-date access ended",
  "occurrence.blocked": "One-date access blocked", "occurrence.refused": "One-date attempt refused",
  "membership.added": "Group member added", "membership.added_over_limit": "Group member added over limit", "membership.removed": "Group member removed",
  "registration.added": "Registration added", "registration.added_over_capacity": "Registration added over capacity", "registration.removed": "Registration removed",
};
const outcomes: Record<string, string> = {
  requested: "Requested", decision: "Decision recorded", approved: "Approved", denied: "Denied", withdrawn: "Withdrawn", expired: "Expired",
  granted: "Granted", revealed: "Revealed", refused: "Refused", revoked: "Revoked", ended: "Ended", added: "Added", removed: "Removed", blocked: "Blocked",
};
const purposes = [{ value: "", label: "All purposes" }, { value: "contact", label: "Direct contact" }, { value: "group_add", label: "Group add" }, { value: "occurrence_add", label: "One-date add" }];
const purposeLabels: Record<string, string> = Object.fromEntries(purposes.map((p) => [p.value, p.label]));
const fieldLabels: Record<string, string> = { email: "Email selected", phone: "Phone selected", both: "Email and phone selected" };
const safeId = (id?: string) => id && retainedPersonId(id) ? id : undefined;
const labelFor = (labels: Record<string, string>, value?: string) => value && Object.prototype.hasOwnProperty.call(labels, value) ? labels[value] : undefined;

type Selection = { personId: string; filters: ContactHistoryFilters; days: { from: string; until: string } };

export function ContactHistoryPage() {
  const { user, loading, isAdmin } = useAuth();
  const { id = "" } = useParams();
  const location = useLocation();
  if (loading) return null;
  if (!user) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  if (!isAdmin || user.status !== "active" || user.accountType === "teen") return <div className="max-w-3xl mx-auto px-4 py-8"><h1 className="text-2xl font-bold">Contact access history</h1><p>This page is for church admins only.</p></div>;
  return <HistoryView key={`${user.id}:${user.status}:${user.role}:${user.accountType}:${user.birthDate}:${id}`} initialId={id} />;
}

function HistoryView({ initialId }: { initialId: string }) {
  const location = useLocation();
  const [person, setPerson] = useState(initialId);
  const [draft, setDraft] = useState(emptyFilters);
  const [names, setNames] = useState<Record<string, string>>({});
  const [zone, setZone] = useState("");
  const [zoneError, setZoneError] = useState("");
  const [zoneRevision, setZoneRevision] = useState(0);
  const [dateError, setDateError] = useState("");
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [selection, setSelection] = useState<Selection | null>(() => {
    const personId = retainedPersonId(initialId);
    return personId ? { personId, filters: emptyFilters, days: { from: "", until: "" } } : null;
  });
  const [entries, setEntries] = useState<ContactHistoryEntry[]>([]);
  const [nextBefore, setNextBefore] = useState<string>();
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState("");
  const [personError, setPersonError] = useState("");
  const [notice, setNotice] = useState("");
  const [authority, setAuthority] = useState<401 | 403>();
  const [revision, setRevision] = useState(0);
  const generation = useRef(0);
  const mounted = useRef(false);
  const paging = useRef(false);
  const downloading = useRef(false);
  const urls = useRef(new Map<string, ReturnType<typeof setTimeout>>());

  const revokeURLs = useCallback(() => {
    for (const [url, timer] of urls.current) { clearTimeout(timer); URL.revokeObjectURL(url); }
    urls.current.clear();
  }, []);
  const clear = useCallback(() => {
    generation.current++;
    revokeURLs(); paging.current = false; downloading.current = false;
    setEntries([]); setNextBefore(undefined); setLoaded(false); setBusy(false); setExporting(false); setNotice(""); setError("");
  }, [revokeURLs]);
  const refresh = useCallback(() => { clear(); setRevision((n) => n + 1); }, [clear]);
  const failure = useCallback((e: unknown, current: number, clearOnFailure = false) => {
    if (!mounted.current) return;
    if (e instanceof ApiError && (e.status === 401 || e.status === 403)) {
      clear(); setNames({}); setAuthority(e.status); return;
    }
    if (generation.current !== current) return;
    if (clearOnFailure) clear();
    setError(errorText(e));
  }, [clear]);

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; generation.current++; revokeURLs(); };
  }, [revokeURLs]);
  const knownPerson = useCallback((person: ContactHistoryPerson) => {
    setNames((old) => old[person.id] === person.name ? old : { ...old, [person.id]: person.name });
  }, []);
  const pickerFailure = useCallback((e: unknown) => failure(e, generation.current), [failure]);
  useEffect(() => {
    let active = true;
    setZone(""); setZoneError("");
    Site.get().then((site) => {
      if (!active) return;
      if (!site.timezone || !historyDayBounds("", "", site.timezone)) {
        setZoneError("We couldn't read the church timezone. Try again, or ask your church office for help.");
        return;
      }
      setZone(site.timezone);
    }).catch((e: unknown) => {
      if (!active) return;
      if (e instanceof ApiError && (e.status === 401 || e.status === 403)) pickerFailure(e);
      else setZoneError("We couldn't load the church timezone. Try again.");
    });
    return () => { active = false; };
  }, [zoneRevision, pickerFailure]);
  useEffect(() => {
    if (authority) return;
    window.addEventListener("focus", refresh);
    return () => window.removeEventListener("focus", refresh);
  }, [refresh, authority]);
  useEffect(() => {
    if (!selection || authority) return;
    const current = ++generation.current;
    setBusy(true);
    ContactHistory.list(selection.personId, selection.filters).then((data) => {
      if (generation.current !== current) return;
      setEntries(data.entries); setNextBefore(data.nextBefore); setLoaded(true);
    }).catch((e: unknown) => failure(e, current)).finally(() => { if (generation.current === current) setBusy(false); });
    return () => { generation.current++; };
  }, [selection, revision, authority, failure]);

  async function more() {
    if (!selection || !nextBefore || paging.current || busy) return;
    const current = generation.current;
    paging.current = true; setBusy(true); setError("");
    try {
      const data = await ContactHistory.list(selection.personId, selection.filters, nextBefore);
      if (generation.current !== current) return;
      setEntries((old) => {
        const seen = new Set(old.map((entry) => entry.id));
        return [...old, ...data.entries.filter((entry) => {
          if (seen.has(entry.id)) return false;
          seen.add(entry.id); return true;
        })];
      });
      setNextBefore(data.nextBefore);
    } catch (e) { failure(e, current); }
    finally { if (generation.current === current) { paging.current = false; setBusy(false); } }
  }
  async function download() {
    if (!selection || downloading.current || !loaded) return;
    const current = generation.current;
    downloading.current = true; setExporting(true); setError(""); setNotice("");
    try {
      const blob = await ContactHistory.csv(selection.personId, selection.filters);
      if (!mounted.current || generation.current !== current) return;
      const url = URL.createObjectURL(blob);
      urls.current.set(url, setTimeout(() => { URL.revokeObjectURL(url); urls.current.delete(url); }, 1000));
      const anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `contact-history-${selection.personId}.csv`;
      document.body.append(anchor);
      try { anchor.click(); } finally { anchor.remove(); }
      setNotice("Your export was prepared. The audit records preparation, not confirmed receipt of the download.");
    } catch (e) { failure(e, current, true); }
    finally { if (generation.current === current) { downloading.current = false; setExporting(false); } }
  }

  // Shows the history for a person with the filters as drafted. Picking a person
  // calls this at once; "Apply filters" and the deleted-account lookup submit the
  // form and call it too. Dates need the church zone; without dates they don't.
  function show(raw: string, form: HTMLFormElement | null) {
    const personId = retainedPersonId(raw);
    if (!personId) {
      clear(); setSelection(null);
      const message = () => setPersonError("Check the saved person ID. Use the digits saved before the account was deleted.");
      if (form) showFieldErrors(form, message); else message();
      return;
    }
    const dated = Boolean(draft.from || draft.until);
    if (dated && !zone) return;
    const bounds = dated ? historyDayBounds(draft.from, draft.until, zone) : { from: "", until: "" };
    if (!bounds) {
      setFiltersOpen(true);
      const message = () => setDateError("Choose valid dates, with Until on or after From.");
      if (form) showFieldErrors(form, message); else message();
      return;
    }
    clear(); setPersonError(""); setDateError("");
    setSelection({ personId, filters: { ...draft, ...bounds }, days: { from: draft.from, until: draft.until } });
  }

  if (authority === 401) return <Navigate to="/login" replace state={{ from: location.pathname }} />;
  if (authority === 403) return <div className="max-w-3xl mx-auto px-4 py-8"><h1 className="text-2xl font-bold">Contact access history</h1><p>This history is no longer available to you. Ask an active adult church admin for help.</p></div>;
  return <div className="max-w-3xl mx-auto px-4 py-6 space-y-5">
    <ButtonLink variant="link" to="/admin?tab=users">Back to users</ButtonLink>
    <h1 className="text-2xl font-bold">Contact access history</h1>
    <p className="text-text-muted">Choose a person to review when contact access was requested, shared or ended. History records what happened, not the contact details themselves.</p>
    <Form noValidate aria-label="History filters" className="space-y-4" onSubmit={(event) => {
      event.preventDefault();
      show(person, event.currentTarget);
    }}>
      <ContactHistoryPersonPicker value={person} name={names[person]} error={personError} onChange={(id) => { setPerson(id); setPersonError(""); }} onPick={(id) => show(id, null)} onKnown={knownPerson} onFailure={pickerFailure} />
      <div className="space-y-4">
        <Button variant="secondary" aria-expanded={filtersOpen} aria-controls="history-filters" onClick={() => setFiltersOpen((open) => !open)}>Filters</Button>
        {filtersOpen && <div id="history-filters" className="space-y-4 rounded-lg border border-border bg-surface p-4">
          <Choices legend="Purpose" optional><div className="grid sm:grid-cols-2 gap-x-4">{purposes.map((p) => <Radio key={p.value} name="history-purpose" label={p.label} value={p.value} checked={draft.purpose === p.value} onChange={() => setDraft({ ...draft, purpose: p.value })} />)}</div></Choices>
          <Field label="Action" optional><Select value={draft.action} options={[{ value: "", label: "All actions" }, ...Object.entries(actions).map(([value, label]) => ({ value, label }))]} onChange={(e) => setDraft({ ...draft, action: e.target.value })} /></Field>
          <div className="grid sm:grid-cols-2 gap-4">
            <Field label="From" optional hint="In church time. This whole date is included."><Input type="date" value={draft.from} onChange={(e) => { setDraft({ ...draft, from: e.target.value }); setDateError(""); }} /></Field>
            <Field label="Until" optional hint="In church time. This whole date is included; choose From or a later date." error={dateError}><Input type="date" min={draft.from || undefined} value={draft.until} onChange={(e) => { setDraft({ ...draft, until: e.target.value }); setDateError(""); }} /></Field>
          </div>
          <p className="text-sm text-text-muted">{zone ? `Dates use church time (${zone}).` : zoneError ? "Church time isn't available yet." : "Loading church timezone…"}</p>
          {zoneError && <div className="space-y-2"><FormError>{zoneError}</FormError><Button variant="secondary" onClick={() => setZoneRevision((n) => n + 1)}>Try loading church time again</Button></div>}
          <Button variant="primary" type="submit" disabled={!zone}>Apply filters</Button>
        </div>}
      </div>
    </Form>
    {selection && <section aria-label="Submitted history" className="space-y-3">
      <h2 className="text-xl font-bold wrap-anywhere">History for {names[selection.personId] || `person #${selection.personId}`}</h2>
      <p className="text-sm text-text-muted wrap-anywhere">Applied: {purposeLabels[selection.filters.purpose] || "All purposes"} · {actions[selection.filters.action] || "All actions"}{selection.days.from && ` · From ${formatBirthDate(selection.days.from)}`}{selection.days.until && ` · Through ${formatBirthDate(selection.days.until)}`}</p>
      <div className="flex flex-wrap gap-2"><Button variant="secondary" onClick={refresh}>Refresh history</Button><Button variant="secondary" disabled={!loaded || exporting} onClick={() => void download()}>{exporting ? "Preparing CSV…" : "Export all matching CSV"}</Button></div>
      <p className="text-sm text-text-muted">Export includes all matching entries, not just the loaded page. Each page is a fresh read; refresh to see newer entries.</p>
      {busy && <p role="status">Loading history…</p>}
      {loaded && !entries.length && <EmptyState title="No matching history" body="Retained activity for this person will appear here. Try fewer filters or check the person ID with your church office." />}
      <ol className="space-y-3">{entries.map((entry) => <HistoryEntry key={entry.id} entry={entry} />)}</ol>
      {nextBefore && <Button variant="secondary" disabled={busy} onClick={() => void more()}>Load older entries</Button>}
    </section>}
    {!selection && <EmptyState title="Choose a person" body="Choose a person above to review their contact access history." />}
    {error && <FormError>{error}</FormError>}
    {notice && <p role="status">{notice}</p>}
  </div>;
}

function HistoryEntry({ entry: e }: { entry: ContactHistoryEntry }) {
  const provenance = [
    ["Requester", e.requesterId], ["Subject", e.subjectUserId], ["Request", e.requestId], ["Access", e.accessId],
    [e.contextType === "group" ? "Group" : e.contextType === "event" ? "Event" : "Context", e.contextId], ["Occurrence", e.occurrenceId],
  ].filter(([, id]) => safeId(id));
  return <li className="border border-border rounded-lg p-4 bg-surface space-y-2 wrap-anywhere">
    <h3 className="font-semibold">{labelFor(actions, e.action) || "Recorded activity"}</h3>
    <p>{labelFor(outcomes, e.outcome) || "Outcome not available"} · <time dateTime={e.at}>{formatWhen(e.at)}</time></p>
    <p className="text-sm">Audit #{safeId(e.id) || "unavailable"} · {safeId(e.actorId) ? `Actor #${e.actorId}` : "System action"}</p>
    {labelFor(purposeLabels, e.purpose) && <p>{labelFor(purposeLabels, e.purpose)}</p>}
    {(e.party === "subject" || e.party === "admin") && (e.decision === "approved" || e.decision === "denied") && <p>{e.party === "subject" ? "Person's" : "Admin's"} decision: {outcomes[e.decision]}</p>}
    {labelFor(fieldLabels, e.fields) && <p>{labelFor(fieldLabels, e.fields)} (selection only)</p>}
    {!!provenance.length && <p className="text-sm text-text-muted">{provenance.map(([label, id]) => `${label} #${id}`).join(" · ")}</p>}
  </li>;
}
