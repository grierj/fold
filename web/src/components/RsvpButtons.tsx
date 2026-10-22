import { Link } from "react-router-dom";
import { ChildcareRequestField, childcareCountProblem } from "./ChildcareRequestField";
import { Button } from "./ui/Button";
import { useId, useRef, useState } from "react";
import { Events } from "../api";
import type { AttendanceMode, EventOccurrence, RegistrationBatch, RegistrationCommand, RegistrationResponse, HouseholdRegistration, MyHouseholdRegistration, RegistrationStatus, HouseholdPerson } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { Checkbox, Choices, Field, FormError, Input, Radio, Select, showFieldErrors } from "./ui/Field";

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
}: {
  instance: EventOccurrence;
  onChange: (reg: RegistrationBatch) => void;
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

  async function choose(status: RegistrationStatus, attendanceMode = mode, switching = false,
    childcare?: { childcareCount: number; childcareAgesNote: string }) {
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
      {waitlisted && instance.myWaitlistPosition != null && (
        <div className="text-sm font-semibold text-text-muted mb-2">
          You&apos;re on the waitlist — position {instance.myWaitlistPosition}. We&apos;ll email you if a spot
          opens.
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
      <div ref={savedStatus} role="status" tabIndex={-1} className="text-sm text-text-muted mb-2">{statusMessage}</div>
      <div className="flex gap-2 flex-wrap items-center">
        {/* Segmented control (DEC-63): one joined row, the chosen answer filled. */}
        <div role="group" aria-label="Your answer" className="flex w-full sm:max-w-sm">
        {OPTIONS.map((o) => {
          const active = mine === o.value;
          const disabled = busy !== null || (o.value === "going" && fullNoWaitlist);
          return (
            <button
              key={o.value}
              type="button"
              aria-pressed={active}
              disabled={disabled}
              onClick={() => void choose(o.value)}
              className="segment"
            >
              {busy === o.value ? "…" : o.label}
            </button>
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
    </div>
  );
}

// The household is loaded once by the page and shared across date editors.
export function RsvpButtons({ instance, people = [], onChange }: { instance: EventOccurrence; people?: HouseholdPerson[]; onChange: (result: RegistrationBatch) => void }) {
  if (!people.length) return <SingleRsvpButtons instance={instance} onChange={onChange} />;
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
  const group = useId();
  const patch = (id: number, values: Partial<Answer>) => setDrafts((d) => ({ ...d, [id]: { ...d[id], ...values } }));
  const offersRemote = instance.remoteAvailable && instance.remoteCapacity !== 0;

  async function save(e: React.FormEvent) {
    e.preventDefault(); setError(null); setMessage("");
    const problem = childcareCountProblem(children);
    if (problem) { setError(problem); return; }
    const command: RegistrationCommand = { date: instance.date, people: [] };
    if (instance.myRegistration !== "removed" && (me.selected || ["going", "waitlisted", "maybe"].includes(instance.myRegistration ?? ""))) {
      command.status = me.selected ? me.status : "declined";
      command.attendanceMode = me.mode;
      command.guestCount = guests;
      command.childcareCount = Number(children);
      command.childcareAgesNote = ages.trim();
    }
    for (const person of people) {
      const d = drafts[person.id];
      const saved = own.find((r) => r.personId === person.id);
      if (person.registrationEligibility !== "eligible" || saved?.status === "removed" || saved?.unavailable) continue;
      if (d.selected || ["going", "waitlisted", "maybe"].includes(saved?.status ?? "")) command.people?.push({ personId: person.id, status: d.selected ? d.status : "declined", attendanceMode: d.mode, childcareRequested: d.care });
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
          {instance.childcareAvailable && <ChildcareRequestField count={children} ages={ages} onCount={setChildren} onAges={setAges} disabled={busy} full={instance.childcareFull} named />}
        </>}
      </>}
    </div>
    {people.map((p) => {
      const d = drafts[p.id]; const saved = own.find((r) => r.personId === p.id);
      const reason = saved?.status === "removed" ? "A leader removed this registration. Ask them to add this person back." : saved?.unavailable ? "The responsible account is unavailable. Ask your church office to check it." : ELIGIBILITY[p.registrationEligibility];
      return <div key={p.id} data-household-person={p.id} className="rounded-lg border border-border p-3">
        <Checkbox label={p.name} checked={d.selected} disabled={busy || !!reason} onChange={(e) => patch(p.id, { selected: e.target.checked })} />
        <p className="text-sm text-text-muted">{p.kind === "child" ? "Child" : p.kind === "teen" ? "Teen" : "Adult"}{p.age !== undefined ? " · Age " + p.age : ""}{saved ? " (via " + saved.holderName + ")" : ""}</p>
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
