import { Link } from "react-router-dom";
import { ChildcareRequestField, childcareCountProblem } from "./ChildcareRequestField";
import { Button } from "./ui/Button";
import { useEffect, useId, useRef, useState } from "react";
import { Events } from "../api";
import type { AttendanceMode, EventOccurrence, RegistrationBatch, RegistrationCommand, HouseholdAnswer, RegistrationResponse, HouseholdRegistration, MyHouseholdRegistration, RegistrationStatus, HouseholdPerson, SeriesRegistrationResponse } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { Checkbox, Choices, Field, FormError, Input, Radio, Select, showFieldErrors } from "./ui/Field";
import { SeriesRsvpDialog, type SeriesRsvpScope } from "./SeriesRsvpDialog";
import { SegmentButton } from "./ui/SegmentButton";

const OPTIONS: { value: RegistrationStatus; label: string }[] = [
  { value: "going", label: "Going" },
  { value: "maybe", label: "Maybe" },
  { value: "declined", label: "Can't go" },
];

const MODES: { value: AttendanceMode; label: string }[] = [
  { value: "in_person", label: "In person" },
  { value: "remote", label: "Remote" },
];

// Registration for one event date, as a Going / Maybe / Can't go
// segmented control. Knows the M1.5
// states: going, waitlisted with position, full with no waitlist, and
// removed. The guest count input appears when the event allows guests.
function SingleRsvpButtons({
  instance,
  onChange,
  seriesFeedbackDate,
  onSeriesFeedback,
}: {
  instance: EventOccurrence;
  onChange: (reg: RegistrationBatch) => void;
  seriesFeedbackDate?: string | null;
  onSeriesFeedback?: (date: string) => void;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [guests, setGuests] = useState(instance.myGuestCount ?? 0);
  const offersRemote = instance.remoteAvailable && instance.remoteCapacity !== 0;
  const [pickedMode, setMode] = useState<AttendanceMode>(instance.myAttendanceMode ?? "in_person");
  const mode = offersRemote ? pickedMode : "in_person";
  const [statusMessage, setStatusMessage] = useState("");
  const modeName = useId();
  const root = useRef<HTMLDivElement>(null);
  const savedStatus = useRef<HTMLDivElement>(null);
  const [children, setChildren] = useState(String(instance.myChildcareCount ?? 0));
  const [ages, setAges] = useState(instance.myChildcareAgesNote ?? "");
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const [childcareFullError, setChildcareFullError] = useState(false);
  const [pendingSeries, setPendingSeries] = useState<null | { status: RegistrationStatus; attendanceMode: AttendanceMode; childcare?: { childcareCount: number; childcareAgesNote: string } }>(null);
  const [undo, setUndo] = useState<null | { token: string; expiresAt: number }>(null);

  const mine = instance.myRegistration;
  const removed = mine === "removed";
  const waitlisted = mine === "waitlisted";
  // Full is judged in the pool you picked: remote seats are their own.
  const myMode = instance.myAttendanceMode ?? "in_person";
  const poolFull =
    mode === "remote"
      ? instance.remoteCapacity !== null && (instance.remoteSpotsLeft ?? 1) <= 0
      : instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
  const fullNoWaitlist =
    poolFull &&
    !instance.waitlistEnabled &&
    !(mine === "going" && myMode === mode) &&
    mine !== "waitlisted";

  const remoteFull = mode === "remote" && poolFull && !(mine === "going" && myMode === mode);
  const inPersonFull = instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
  const remoteFullMessage = inPersonFull
    ? `Remote seats are full.${instance.waitlistEnabled ? " You can join the remote waitlist." : ""}`
    : instance.waitlistEnabled
      ? "Remote seats are full. You can still come in person, or join the remote waitlist."
      : "Remote seats are full. You can still come in person.";

  useEffect(() => {
    if (!undo) return;
    const timer = window.setTimeout(() => {
      setUndo(null);
      setStatusMessage((message) => `${message} · The 10-second Undo time has ended. You can change your answers again from the event.`);
    }, Math.max(0, undo.expiresAt - Date.now()));
    return () => window.clearTimeout(timer);
  }, [undo]);

  useEffect(() => {
    if (seriesFeedbackDate && seriesFeedbackDate !== instance.date) {
      setStatusMessage("");
      setUndo(null);
    }
  }, [instance.date, seriesFeedbackDate]);

  const answerLabel = (status: RegistrationStatus) => status === "going" ? "Going" : status === "maybe" ? "Maybe" : "Can't go";
  const resultSummary = (result: SeriesRegistrationResponse, prefix = "") => {
    const going = result.results.filter((row) => row.after.status === "going").length;
    const maybe = result.results.filter((row) => row.after.status === "maybe").length;
    const declined = result.results.filter((row) => row.after.status === "declined").length;
    const waitlisted = result.results.filter((row) => row.after.status === "waitlisted").map((row) => row.date);
    const parts = [going ? `Going to ${going} ${going === 1 ? "date" : "dates"}` : "", maybe ? `Maybe for ${maybe} ${maybe === 1 ? "date" : "dates"}` : "", declined ? `Can't go to ${declined} ${declined === 1 ? "date" : "dates"}` : ""].filter(Boolean);
    if (waitlisted.length) parts.push(`waitlisted for ${waitlisted.join(" and ")}`);
    if (result.refusals.length) parts.push(`${result.refusals.length} ${result.refusals.length === 1 ? "date was" : "dates were"} left unchanged`);
    if (result.capacityLoss?.length) parts.push(`a seat was no longer available for ${result.capacityLoss.join(" and ")}`);
    const fallback = prefix && result.results.length ? `Restored previous answers for ${result.results.length} ${result.results.length === 1 ? "date" : "dates"}.` : "No dates changed.";
    return `${prefix}${parts.join(" · ") || fallback}`;
  };

  async function applySeries(status: RegistrationStatus, scope: Exclude<SeriesRsvpScope, "just_this_date">, attendanceMode = mode,
    childcare?: { childcareCount: number; childcareAgesNote: string }) {
    if (status === "declined" && !window.confirm("Can't go to these dates? They will be removed from your calendar and personal feed.")) return;
    setBusy(status); setError(null); setStatusMessage(""); setUndo(null);
    try {
      const result = await Events.registerSeries(instance.eventId, {
        status: status as "going" | "maybe" | "declined", scope,
        ...(scope === "this_and_following" ? { date: instance.date } : {}),
        guestCount: status === "going" ? guests : 0,
        attendanceMode: instance.remoteAvailable ? attendanceMode : undefined,
        ...(status === "going" && instance.childcareAvailable ? childcare ?? (mine !== "going" ? { childcareCount: 0, childcareAgesNote: "" } : {}) : {}),
      });
      setStatusMessage(resultSummary(result));
      if (result.undoToken && result.undoExpiresAt) setUndo({ token: result.undoToken, expiresAt: new Date(result.undoExpiresAt).getTime() });
      onSeriesFeedback?.(instance.date);
      onChange({ registration: null, people: [] });
      queueMicrotask(() => savedStatus.current?.focus());
    } catch (e) {
      setError(errorText(e, "We couldn't save your series answer."));
    } finally { setBusy(null); }
  }

  async function undoSeries() {
    if (!undo) return;
    setBusy("undo"); setError(null);
    try {
      const result = await Events.undoSeriesRegistration(instance.eventId, undo.token);
      setUndo(null); setStatusMessage(resultSummary(result, "Undo: "));
      onSeriesFeedback?.(instance.date);
      onChange({ registration: null, people: [] });
      queueMicrotask(() => savedStatus.current?.focus());
    } catch (e) { setError(errorText(e, "We couldn't undo that answer.")); }
    finally { setBusy(null); }
  }

  async function choose(status: RegistrationStatus, attendanceMode = mode, switching = false,
    childcare?: { childcareCount: number; childcareAgesNote: string }, singleDate = false) {
    if (instance.recurrenceRule && !switching && !singleDate) {
      setPendingSeries({ status, attendanceMode, childcare });
      return;
    }
    setBusy(status);
    setError(null);
    setChildcareError(null);
    setChildcareFullError(false);
    setStatusMessage("");
    try {
      const guestCount = status === "going" ? guests : 0;
      const result = await Events.register(instance.eventId, {
        status, date: instance.date, guestCount,
        attendanceMode: instance.remoteAvailable ? attendanceMode : undefined,
        ...(status === "going" && instance.childcareAvailable
          ? childcare ?? (mine !== "going" ? { childcareCount: 0, childcareAgesNote: "" } : undefined)
          : undefined),
      });
      const registration: RegistrationResponse | null = result.registration;
      if (!registration) return;
      onChange(result);
      if (switching) {
        setStatusMessage(registration.status === "waitlisted"
          ? `You're on the ${attendanceMode === "remote" ? "remote" : "in-person"} waitlist.`
          : `Switched to ${attendanceMode === "remote" ? "remote" : "in person"}`);
      } else if (instance.childcareAvailable && status === "going") {
        setChildren(String(registration.childcareCount));
        setAges(registration.childcareAgesNote);
        setStatusMessage(registration.status === "waitlisted"
          ? `You're on the ${attendanceMode === "remote" ? "remote" : "in-person"} waitlist.`
          : `Saved: going, ${registration.childcareCount > 0
            ? `${registration.childcareCount} ${registration.childcareCount === 1 ? "child" : "children"} in childcare`
            : "no childcare"}`);
        savedStatus.current?.focus();
      }
    } catch (e) {
      if (switching) setMode(myMode);
      if (errorCode(e) === "childcare_full") {
        showFieldErrors(root.current, () => {
          setBusy(null);
          setChildcareError(errorText(e, "We couldn't save your childcare request."));
          setChildcareFullError(true);
        });
        return;
      }
      setError(attendanceMode === "remote" && errorCode(e) === "event_full"
        ? remoteFullMessage
        : errorText(e, "We couldn't save your answer."));
    } finally {
      setBusy(null);
    }
  }

  function saveChildcare() {
    const problem = childcareCountProblem(children);
    if (problem) {
      showFieldErrors(root.current, () => setChildcareError(problem));
      return;
    }
    void choose("going", mode, false, { childcareCount: Number(children), childcareAgesNote: ages.trim() });
  }

  if (removed) {
    return (
      <div className="text-sm text-text-muted">
        A leader or host removed your registration for this date. Ask them to add you back.
      </div>
    );
  }

  return (
    <div ref={root}>
      {waitlisted && (
        <div className="text-sm font-semibold text-text-muted mb-2">
          Waitlisted{instance.myWaitlistPosition != null ? ` — position ${instance.myWaitlistPosition}` : ""}. We&apos;ll email you if a spot opens.
        </div>
      )}
      {remoteFull ? (
        <div className="text-sm font-semibold text-text-muted mb-2">
          {remoteFullMessage}
        </div>
      ) : fullNoWaitlist && <div className="text-sm font-semibold text-text-muted mb-2">This event is full.</div>}
      {offersRemote && (
        <div className="mb-2">
          <Choices legend="How you'll attend">
            {MODES.map((m) => (
              <Radio
                key={m.value}
                name={modeName}
                label={m.label}
                checked={mode === m.value}
                disabled={busy !== null}
                onChange={() => {
                  setMode(m.value);
                  if (mine === "going") void choose("going", m.value, true);
                }}
              />
            ))}
          </Choices>
          <div aria-live="polite" className="text-sm text-text-muted mt-2">
            {mine !== "going" && `Tap Going to attend ${mode === "remote" ? "remotely" : "in person"}.`}
          </div>
        </div>
      )}
      <div ref={savedStatus} role="status" tabIndex={-1} className="text-sm text-text-muted mb-2">
        {statusMessage}{undo && <Button variant="link" layout="ml-2" disabled={busy !== null} onClick={() => void undoSeries()}>Undo</Button>}
      </div>
      <div className="flex gap-2 flex-wrap items-center">
        {/* Segmented control (DEC-63): one joined row, the chosen answer filled. */}
        <div role="group" aria-label="Your answer" className="flex w-full sm:max-w-sm">
        {OPTIONS.map((o) => {
          const active = mine === o.value || (o.value === "going" && mine === "waitlisted");
          const disabled = busy !== null || (o.value === "going" && fullNoWaitlist);
          return (
            <SegmentButton
              key={o.value}
              pressed={active}
              disabled={disabled}
              onClick={() => void choose(o.value)}
            >
              {busy === o.value ? "…" : o.label}
            </SegmentButton>
          );
        })}
        </div>
        {instance.guestsAllowed > 0 && (
          <Field label="Extra guests" hint={`Up to ${instance.guestsAllowed}.`}>
            <Input
              type="number"
              min={0}
              max={instance.guestsAllowed}
              value={guests}
              onChange={(e) => setGuests(Math.max(0, Math.min(instance.guestsAllowed, Number(e.target.value))))}
              layout="w-28"
            />
          </Field>
        )}
      </div>
      {instance.recurrenceRule && <div className="mt-3 border-t border-border pt-3">
        <p className="text-sm font-semibold mb-2">Answer the whole series</p>
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" disabled={busy !== null} onClick={() => void applySeries("going", "all_future")}>Going to all dates</Button>
          <Button variant="secondary" disabled={busy !== null} onClick={() => void applySeries("maybe", "all_future")}>Maybe for all</Button>
          <Button variant="danger-outline" disabled={busy !== null} onClick={() => void applySeries("declined", "all_future")}>Can&apos;t go to any</Button>
        </div>
        <p className="text-sm text-text-muted mt-2">New dates added later won&apos;t be answered for you.</p>
      </div>}
      {instance.childcareAvailable && mine === "going" && (
        <div className="mt-3 space-y-3">
          <ChildcareRequestField count={children} ages={ages} onCount={setChildren} onAges={setAges}
            error={childcareError} disabled={busy !== null} full={instance.childcareFull} />
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" disabled={busy !== null} onClick={saveChildcare}>
              {busy === "going" ? "Saving…" : "Save"}
            </Button>
            {childcareFullError && <Button variant="secondary" disabled={busy !== null}
              onClick={() => void choose("going", mode, false, { childcareCount: 0, childcareAgesNote: "" })}>
              Clear childcare request
            </Button>}
          </div>
        </div>
      )}
      {error && <FormError layout="mt-1">{error}</FormError>}
      {pendingSeries && <SeriesRsvpDialog answer={answerLabel(pendingSeries.status)} onCancel={() => setPendingSeries(null)} onChoose={(scope) => {
        const pending = pendingSeries;
        setPendingSeries(null);
        if (scope === "just_this_date") void choose(pending.status, pending.attendanceMode, false, pending.childcare, true);
        else void applySeries(pending.status, scope, pending.attendanceMode, pending.childcare);
      }} />}
    </div>
  );
}

// The household is loaded once by the page and shared across date editors.
export function RsvpButtons({ instance, people = [], onChange, seriesFeedbackDate, onSeriesFeedback }: {
  instance: EventOccurrence;
  people?: HouseholdPerson[];
  onChange: (result: RegistrationBatch) => void;
  seriesFeedbackDate?: string | null;
  onSeriesFeedback?: (date: string) => void;
}) {
  if (!people.length) return <SingleRsvpButtons instance={instance} onChange={onChange} seriesFeedbackDate={seriesFeedbackDate} onSeriesFeedback={onSeriesFeedback} />;
  return <HouseholdRsvp instance={instance} people={people} onChange={onChange} />;
}

type Answer = { selected: boolean; status: "going" | "maybe" | "declined"; mode: AttendanceMode; care: boolean };
const draftAnswer = (status: string | null | undefined, mode: AttendanceMode = "in_person", care = false): Answer => ({ selected: status === "going" || status === "waitlisted" || status === "maybe", status: status === "maybe" ? "maybe" : "going", mode, care });
const ELIGIBILITY: Record<HouseholdPerson["registrationEligibility"], string> = {
  eligible: "", link_pending: "Waiting for an admin to confirm", link_unconfirmed: "Ask your church office to confirm this household link.",
  birth_date_required: "Add a birth date in Profile before signing them up.", age_mismatch: "Check their birth date and kind in Profile.",
  converted: "This person now has an account.", registration_holder_unavailable: "Ask your church office to check the responsible account.",
};

function HouseholdRsvp({ instance, people, onChange }: { instance: EventOccurrence; people: HouseholdPerson[]; onChange: (result: RegistrationBatch) => void }) {
  const root = useRef<HTMLFormElement>(null);
  const own: MyHouseholdRegistration[] = instance.myHouseholdRegistrations ?? [];
  const [me, setMe] = useState(() => draftAnswer(instance.myRegistration, instance.myAttendanceMode));
  const [drafts, setDrafts] = useState<Record<number, Answer>>(() => Object.fromEntries(people.map((p) => {
    const saved = own.find((r) => r.personId === p.id);
    return [p.id, draftAnswer(saved?.status, saved?.attendanceMode, saved?.childcareRequested)];
  })));
  const [guests, setGuests] = useState(instance.myGuestCount ?? 0);
  const [children, setChildren] = useState(String(instance.myChildcareCount ?? 0));
  const [ages, setAges] = useState(instance.myChildcareAgesNote ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [message, setMessage] = useState("");
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const group = useId();
  const patch = (id: number, values: Partial<Answer>) => setDrafts((d) => ({ ...d, [id]: { ...d[id], ...values } }));
  const offersRemote = instance.remoteAvailable && instance.remoteCapacity !== 0;

  async function save(e: React.FormEvent) {
    e.preventDefault(); setError(null); setMessage("");
    const problem = childcareCountProblem(children);
    if (problem) { showFieldErrors(root.current, () => setChildcareError(problem)); return; }
    setChildcareError(null);
    const answers: HouseholdAnswer[] = [];
    const command: RegistrationCommand = { date: instance.date, people: answers };
    if (instance.myRegistration !== "removed" && (me.selected || ["going", "waitlisted", "maybe"].includes(instance.myRegistration ?? ""))) {
      command.status = me.selected ? me.status : "declined";
      command.attendanceMode = me.mode;
      command.guestCount = guests;
      if (instance.childcareAvailable) { command.childcareCount = Number(children); command.childcareAgesNote = ages.trim(); }
    }
    for (const person of people) {
      const d = drafts[person.id];
      const saved = own.find((r) => r.personId === person.id);
      if (person.registrationEligibility !== "eligible" || saved?.status === "removed" || saved?.unavailable) continue;
      if (d.selected || ["going", "waitlisted", "maybe"].includes(saved?.status ?? "")) answers.push({ personId: person.id, status: d.selected ? d.status : "declined", attendanceMode: d.mode, childcareRequested: d.care });
    }
    if (!command.status && !command.people?.length) { setError("Choose at least one person to save an answer."); return; }
    setBusy(true);
    try {
      const result = await Events.register(instance.eventId, command);
      const describe = (name: string, status: string, mode: AttendanceMode, position: number | null) => status === "waitlisted" ? name + ": waitlist position " + position : name + ": " + (status === "going" ? "going " + (mode === "remote" ? "remotely" : "in person") : status === "declined" ? "can't go" : "maybe");
      const outcomes = result.people.map((r: HouseholdRegistration) => describe(people.find((p) => p.id === r.personId)?.name ?? "Household member", r.status, r.attendanceMode, r.waitlistPosition));
      if (result.registration) { const r = result.registration; outcomes.unshift(describe("You", r.status, r.attendanceMode, r.waitlistPosition)); }
      setMessage(outcomes.join("; ")); onChange(result);
    } catch (e) {
      setError(errorText(e, "We couldn't save your answers."));
      const personId = e && typeof e === "object" && "data" in e && e.data && typeof e.data === "object" && "personId" in e.data ? e.data.personId : undefined;
      if (typeof personId === "number") {
        showFieldErrors(root.current, () => setBusy(false));
        root.current?.querySelector<HTMLElement>(`[data-household-person="${personId}"] input`)?.focus();
      }
    } finally { setBusy(false); }
  }

  const controls = (name: string, d: Answer, change: (patch: Partial<Answer>) => void, child = false) => <div className="space-y-3 mt-2">
    <Field label={"Answer for " + name}><Select value={d.status} disabled={busy} onChange={(e) => change({ status: e.target.value as Answer["status"] })}>
      <option value="going">Going</option><option value="maybe">Maybe</option><option value="declined">Can't go</option>
    </Select></Field>
    {offersRemote && <Choices legend={"How " + name + " will attend"}>{MODES.map((m) => <Radio key={m.value} name={group + name} label={m.label} checked={d.mode === m.value} disabled={busy} onChange={() => change({ mode: m.value, ...(m.value === "remote" ? { care: false } : {}) })} />)}</Choices>}
    {child && d.mode === "in_person" && (instance.childcareAvailable || d.care) && <Checkbox label={"Childcare for " + name} checked={d.care} disabled={busy || (!instance.childcareAvailable && !d.care)} onChange={(e) => change({ care: e.target.checked })} />}
    {child && d.mode === "remote" && <p className="text-sm text-text-muted">Remote attendees do not use childcare.</p>}
  </div>;

  return <form ref={root} onSubmit={save} className="space-y-4" aria-label="Household answers">
    <h4 className="font-semibold">Who will attend?</h4>
    <div className="rounded-lg border border-border p-3">
      {instance.myRegistration === "removed" ? <p>A leader or host removed your registration for this date. Ask them to add you back.</p> : <>
        <Checkbox label="Me" checked={me.selected} disabled={busy} onChange={(e) => setMe({ ...me, selected: e.target.checked })} />
        {me.selected && <>{controls("you", me, (values) => setMe({ ...me, ...values }))}
          {instance.guestsAllowed > 0 && <Field label="Extra guests" hint={"Up to " + instance.guestsAllowed + "."}><Input type="number" min={0} max={instance.guestsAllowed} value={guests} onChange={(e) => setGuests(Number(e.target.value))} /></Field>}
          {instance.childcareAvailable && <ChildcareRequestField count={children} ages={ages} onCount={setChildren} onAges={setAges} error={childcareError} disabled={busy} full={instance.childcareFull} named />}
        </>}
      </>}
    </div>
    {people.map((p) => {
      const d = drafts[p.id]; const saved = own.find((r) => r.personId === p.id);
      const reason = saved?.status === "removed" ? "A leader removed this registration. Ask them to add this person back." : saved?.unavailable ? "The responsible account is unavailable. Ask your church office to check it." : ELIGIBILITY[p.registrationEligibility];
      return <div key={p.id} data-household-person={p.id} className="rounded-lg border border-border p-3">
        <Checkbox label={p.name} checked={d.selected} disabled={busy || !!reason} onChange={(e) => patch(p.id, { selected: e.target.checked })} />
        <p className="text-sm text-text-muted">{p.kind === "child" ? "Child" : p.kind === "teen" ? "Teen" : "Adult"}{p.age !== undefined ? " · Age " + p.age : ""}</p>
        {saved && <p className="text-sm text-text-muted">Signed up by {saved.holderName}</p>}
        {saved && !saved.unavailable && <p className="text-sm text-text-muted">{saved.status === "waitlisted" ? `Waitlist position ${saved.waitlistPosition}` : saved.status === "going" ? `Going ${saved.attendanceMode === "remote" ? "remotely" : "in person"}` : saved.status === "maybe" ? "Maybe" : saved.status === "declined" ? "Can’t go" : "Removed"}</p>}
        {reason && <p className="text-sm text-text-muted">{reason} <Link className="inline-flex min-h-11 items-center text-primary underline" to="/profile">Edit household in Profile</Link></p>}
        {!reason && d.selected && controls(p.name, d, (values) => patch(p.id, values), p.kind === "child")}
      </div>;
    })}
    {instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0 && <p>In-person seats are full.{instance.waitlistEnabled ? " New answers may be waitlisted." : " Ask a leader for help."}</p>}
    {instance.childcareFull && <p>Childcare is full for this date.</p>}
    {error && <FormError>{error}</FormError>}
    <p role="status" className="text-sm text-text-muted">{message}</p>
    <Button variant="primary" type="submit" disabled={busy}>{busy ? "Saving…" : "Save answers"}</Button>
  </form>;
}
