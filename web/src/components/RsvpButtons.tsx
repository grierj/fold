import { Form } from "./ui/Form";
import { CheckIcon } from "./icons";
import { Link } from "react-router-dom";
import { ChildcareRequestField, childcareCountProblem } from "./ChildcareRequestField";
import { Button } from "./ui/Button";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Events } from "../api";
import type { AttendanceMode, EventOccurrence, RegistrationBatch, RegistrationCommand, HouseholdAnswer, RegistrationResponse, HouseholdRegistration, MyHouseholdRegistration, RegistrationStatus, HouseholdPerson, SeriesRegistrationResponse, SeriesRegistrationState } from "../types";
import { errorCode, errorText } from "../lib/errors";
import { Checkbox, Choices, Field, focusFirstInvalid, FormError, Input, Radio, Select, showFieldErrors } from "./ui/Field";
import { SeriesRsvpDialog, type SeriesRsvpScope } from "./SeriesRsvpDialog";
import { SegmentButton } from "./ui/SegmentButton";
import { formatCalendarDay } from "../lib/when";
import { actorResultFromRegistration, type ActorRsvpResult, type SeriesFeedbackSignal } from "../lib/rsvpFeedback";
import { useRsvpMutationGate, type RsvpMutationGate } from "../lib/useRsvpMutationGate";

export type { SeriesFeedbackSignal } from "../lib/rsvpFeedback";

const OPTIONS: { value: RegistrationStatus; label: string }[] = [
  { value: "going", label: "Going" },
  { value: "maybe", label: "Maybe" },
  { value: "declined", label: "Can't go" },
];

const MODES: { value: AttendanceMode; label: string }[] = [
  { value: "in_person", label: "In person" },
  { value: "remote", label: "Remote" },
];

type SeriesAnswer = {
  status: RegistrationStatus;
  attendanceMode: AttendanceMode;
  guestCount: number;
  childcare?: { childcareCount: number; childcareAgesNote: string };
};
type SeriesFeedbackReporter = (eventId: number, date: string, actorResults: ActorRsvpResult[]) => void;

const answerLabel = (status: RegistrationStatus) => status === "going" ? "Going" : status === "maybe" ? "Maybe" : "Can't go";
const humanDays = (days: string[]) => days.map(formatCalendarDay).join(" and ");

function resultSummary(result: SeriesRegistrationResponse, prefix = "") {
  const going = result.results.filter((row) => row.after.status === "going").length;
  const maybe = result.results.filter((row) => row.after.status === "maybe").length;
  const declined = result.results.filter((row) => row.after.status === "declined").length;
  const waitlisted = result.results.filter((row) => row.after.status === "waitlisted").map((row) => row.date);
  const parts = [going ? `Going to ${going} ${going === 1 ? "date" : "dates"}` : "", maybe ? `Maybe for ${maybe} ${maybe === 1 ? "date" : "dates"}` : "", declined ? `Can't go to ${declined} ${declined === 1 ? "date" : "dates"}` : ""].filter(Boolean);
  if (waitlisted.length) parts.push(`waitlisted for ${humanDays(waitlisted)}`);
  if (result.refusals.length) parts.push(`${result.refusals.length} ${result.refusals.length === 1 ? "date was" : "dates were"} left unchanged`);
  if (result.capacityLoss?.length) parts.push(`a seat was no longer available for ${humanDays(result.capacityLoss)}`);
  const fallback = prefix && result.results.length ? `Restored previous answers for ${result.results.length} ${result.results.length === 1 ? "date" : "dates"}.` : "No dates changed.";
  return `${prefix}${parts.join(" · ") || fallback}`;
}

const TERMINAL_UNDO_CODES = new Set([
  "undo_changed", "undo_authority_revoked", "undo_date_ended", "undo_not_found", "undo_expired", "undo_used",
]);
const REFRESH_UNDO_CODES = new Set([
  "undo_changed", "undo_authority_revoked", "undo_date_ended", "undo_not_found", "undo_used",
]);

function useActorSeriesRsvp({
  instance,
  onChange,
  seriesFeedback,
  onSeriesFeedback,
  onTerminalRefresh,
  mutationGate,
}: {
  instance: EventOccurrence;
  onChange: (reg: RegistrationBatch) => void;
  seriesFeedback?: SeriesFeedbackSignal | null;
  onSeriesFeedback?: SeriesFeedbackReporter;
  onTerminalRefresh?: () => void;
  mutationGate: RsvpMutationGate;
}) {
  const [error, setError] = useState<string | null>(null);
  const [statusMessage, setStatusMessage] = useState("");
  const [pending, setPending] = useState<SeriesAnswer | null>(null);
  const [undo, setUndo] = useState<null | { token: string; expiresAt: number }>(null);
  const statusRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!undo) return;
    const timer = window.setTimeout(() => {
      setUndo(null);
      setStatusMessage((message) => `${message} · The 10-second Undo time has ended. You can change your answers again from the event.`);
    }, Math.max(0, undo.expiresAt - Date.now()));
    return () => window.clearTimeout(timer);
  }, [undo]);

  useEffect(() => {
    if (seriesFeedback?.eventId === instance.eventId && seriesFeedback.date !== instance.date) {
      setStatusMessage("");
      setUndo(null);
    }
  }, [instance.date, instance.eventId, seriesFeedback]);

  function reportDateMutation(registration: RegistrationResponse, message = "") {
    setStatusMessage(message);
    setUndo(null);
    setError(null);
    onSeriesFeedback?.(instance.eventId, instance.date, [actorResultFromRegistration(registration)]);
  }

  async function applySeries(answer: SeriesAnswer, scope: Exclude<SeriesRsvpScope, "just_this_date">): Promise<boolean> {
    if (answer.status === "declined" && !window.confirm("Can't go to these dates? They will be removed from your calendar and personal feed.")) return false;
    const release = mutationGate.acquire();
    if (!release) return false;
    setError(null);
    try {
      const result = await Events.registerSeries(instance.eventId, {
        status: answer.status as "going" | "maybe" | "declined",
        scope,
        ...(scope === "this_and_following" ? { date: instance.date } : {}),
        guestCount: answer.status === "going" ? answer.guestCount : 0,
        attendanceMode: instance.remoteAvailable ? answer.attendanceMode : undefined,
        ...(answer.status === "going" && instance.childcareAvailable ? answer.childcare ?? (instance.myRegistration !== "going" ? { childcareCount: 0, childcareAgesNote: "" } : {}) : {}),
      });
      setStatusMessage(resultSummary(result));
      setUndo(result.undoToken && result.undoExpiresAt ? { token: result.undoToken, expiresAt: new Date(result.undoExpiresAt).getTime() } : null);
      onSeriesFeedback?.(instance.eventId, instance.date, result.results.map(({ date, after }) => ({ date, after })));
      onChange({ registration: null, people: [] });
      queueMicrotask(() => statusRef.current?.focus());
      return true;
    } catch (e) {
      setError(errorText(e, "We couldn't save your series answer."));
      return false;
    } finally {
      release();
    }
  }

  async function undoSeries() {
    if (!undo) return;
    const release = mutationGate.acquire();
    if (!release) return;
    setError(null);
    try {
      const result = await Events.undoSeriesRegistration(instance.eventId, undo.token);
      setUndo(null);
      setStatusMessage(resultSummary(result, "Undo: "));
      onSeriesFeedback?.(instance.eventId, instance.date, result.results.map(({ date, after }) => ({ date, after })));
      onChange({ registration: null, people: [] });
      queueMicrotask(() => statusRef.current?.focus());
    } catch (e) {
      const code = errorCode(e);
      if (TERMINAL_UNDO_CODES.has(code)) {
        setUndo(null);
        setStatusMessage("");
        queueMicrotask(() => statusRef.current?.focus());
      }
      setError(errorText(e, "We couldn't undo that answer."));
      if (REFRESH_UNDO_CODES.has(code)) onTerminalRefresh?.();
    } finally {
      release();
    }
  }

  return {
    error, mutationBusy: mutationGate.busy, pending, setPending, statusMessage, statusRef, undo,
    applySeries, undoSeries, reportDateMutation,
    announce(message: string) { setStatusMessage(message); queueMicrotask(() => statusRef.current?.focus()); },
  };
}

type SeriesController = ReturnType<typeof useActorSeriesRsvp>;

// The one status region. A series result or Undo message wins; otherwise
// `fallback` is the standing line for the viewer's own answer (DEC-92), so
// screen readers hear it here and there is no second live region.
function SeriesFeedback({ series, fallback }: { series: SeriesController; fallback?: ReactNode }) {
  return <>
    <div ref={series.statusRef} role="status" tabIndex={-1} className="text-sm text-text-muted mt-2 empty:mt-0 mb-2 empty:mb-0">
      {series.statusMessage || fallback}{series.undo && <Button variant="link" layout="ml-2" disabled={series.mutationBusy} onClick={() => void series.undoSeries()}>Undo</Button>}
    </div>
    {series.error && <FormError layout="mt-1">{series.error}</FormError>}
  </>;
}

// Standing confirmation under the control (DEC-92). Waitlisted, full and
// remote-pool states keep their own copy above the control. The date page
// shares it (EventDate.tsx).
export function answerLine(status: string | null | undefined): ReactNode {
  if (status === "going") return <><CheckIcon className="inline h-4 w-4 mr-1 align-text-bottom text-success-on-subtle" />You&apos;re going. See you there!</>;
  if (status === "maybe") return "You said maybe. Change it anytime.";
  if (status === "declined") return "Got it. Only the leaders see this.";
  return null;
}

// "Childcare for 2 (ages 3 and 7)": one child reads "1 child".
const childcareSummary = (count: number, ages: string) =>
  `Childcare for ${count === 1 ? "1 child" : count}${ages.trim() ? ` (ages ${ages.trim()})` : ""}`;

function SeriesScopeDialog({ series, onJustThisDate, onApplied }: {
  series: SeriesController;
  onJustThisDate: (answer: SeriesAnswer) => void;
  onApplied?: () => void;
}) {
  if (!series.pending) return null;
  return <SeriesRsvpDialog answer={answerLabel(series.pending.status)} disabled={series.mutationBusy} onCancel={() => series.setPending(null)} onChoose={(scope) => {
    const answer = series.pending;
    series.setPending(null);
    if (!answer) return;
    if (scope === "just_this_date") onJustThisDate(answer);
    else void series.applySeries(answer, scope).then((applied) => { if (applied) onApplied?.(); });
  }} />;
}

type SavedActorAnswer = { status: string | null | undefined; mode: AttendanceMode; guests: number; children: number; ages: string };
const savedActorAnswer = (instance: EventOccurrence): SavedActorAnswer => ({
  status: instance.myRegistration,
  mode: instance.myAttendanceMode ?? "in_person",
  guests: instance.myGuestCount ?? 0,
  children: instance.myChildcareCount ?? 0,
  ages: instance.myChildcareAgesNote ?? "",
});
const savedActorAnswerFromState = (state: SeriesRegistrationState): SavedActorAnswer => ({
  status: state.status ?? null,
  mode: state.status ? state.attendanceMode ?? "in_person" : "in_person",
  guests: state.status ? state.guestCount ?? 0 : 0,
  children: state.status ? state.childcareCount ?? 0 : 0,
  ages: state.status ? state.childcareAgesNote ?? "" : "",
});
const sameSavedActorAnswer = (left: SavedActorAnswer, right: SavedActorAnswer) => left.status === right.status
  && left.mode === right.mode && left.guests === right.guests && left.children === right.children && left.ages === right.ages;

function useActorDraftReconciliation(instance: EventOccurrence, seriesFeedback: SeriesFeedbackSignal | null | undefined,
  reconcile: (saved: SavedActorAnswer) => void) {
  const savedActor = useRef(savedActorAnswer(instance));
  const actorRefreshRevision = useRef(seriesFeedback?.revision ?? 0);
  const reconcileRef = useRef(reconcile);
  reconcileRef.current = reconcile;
  useEffect(() => {
    const feedbackChanged = seriesFeedback?.revision !== undefined && seriesFeedback.revision !== actorRefreshRevision.current;
    const actorResult = seriesFeedback?.eventId === instance.eventId
      ? seriesFeedback.actorResults.find((row) => row.date === instance.date)
      : undefined;
    const actorRefresh = !!actorResult && feedbackChanged;
    const nextSaved = actorRefresh ? savedActorAnswerFromState(actorResult.after) : savedActorAnswer(instance);
    const savedChanged = !sameSavedActorAnswer(savedActor.current, nextSaved);
    savedActor.current = nextSaved;
    if (seriesFeedback?.revision !== undefined) actorRefreshRevision.current = seriesFeedback.revision;
    if (savedChanged || actorRefresh) reconcileRef.current(nextSaved);
  }, [instance.date, instance.eventId, instance.myAttendanceMode, instance.myChildcareAgesNote, instance.myChildcareCount,
    instance.myGuestCount, instance.myRegistration, seriesFeedback]);
}

// Registration for one event date, as a Going / Maybe / Can't go
// segmented control. Knows the M1.5
// states: going, waitlisted with position, full with no waitlist, and
// removed. The guest count input appears when the event allows guests.
function SingleRsvpButtons({
  instance,
  onChange,
  seriesFeedback,
  onSeriesFeedback,
  onTerminalRefresh,
  mutationGate,
}: {
  instance: EventOccurrence;
  onChange: (reg: RegistrationBatch) => void;
  seriesFeedback?: SeriesFeedbackSignal | null;
  onSeriesFeedback?: SeriesFeedbackReporter;
  onTerminalRefresh?: () => void;
  mutationGate: RsvpMutationGate;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [guests, setGuests] = useState(instance.myGuestCount ?? 0);
  const offersRemote = instance.remoteAvailable && instance.remoteCapacity !== 0;
  const [pickedMode, setMode] = useState<AttendanceMode>(instance.myAttendanceMode ?? "in_person");
  const mode = offersRemote ? pickedMode : "in_person";
  const modeName = useId();
  const root = useRef<HTMLDivElement>(null);
  const [children, setChildren] = useState(String(instance.myChildcareCount ?? 0));
  const [ages, setAges] = useState(instance.myChildcareAgesNote ?? "");
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const [childcareFullError, setChildcareFullError] = useState(false);
  // Childcare stays one link until used (DEC-92). `savedCare` is what the
  // server holds; `children`/`ages` are the draft while the fields are open.
  const [savedCare, setSavedCare] = useState({ count: instance.myChildcareCount ?? 0, ages: instance.myChildcareAgesNote ?? "" });
  const [editingCare, setEditingCare] = useState(false);
  const careRef = useRef<HTMLDivElement>(null);
  const careFocus = useRef<"field" | "toggle" | null>(null);
  useEffect(() => {
    const target = careFocus.current;
    careFocus.current = null;
    if (target === "field") careRef.current?.querySelector<HTMLElement>("input")?.focus();
    else if (target === "toggle") careRef.current?.querySelector<HTMLElement>("button")?.focus();
  }, [editingCare]);
  useActorDraftReconciliation(instance, seriesFeedback, (saved) => {
    setGuests(saved.guests);
    setMode(saved.mode);
    setChildren(String(saved.children));
    setAges(saved.ages);
    setSavedCare({ count: saved.children, ages: saved.ages });
  });
  const series = useActorSeriesRsvp({ instance, onChange, seriesFeedback, onSeriesFeedback, onTerminalRefresh, mutationGate });

  const mine = instance.myRegistration;
  const removed = mine === "removed";
  const waitlisted = mine === "waitlisted";
  const myMode = instance.myAttendanceMode ?? "in_person";
  const poolFull = mode === "remote"
    ? instance.remoteCapacity !== null && (instance.remoteSpotsLeft ?? 1) <= 0
    : instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
  const fullNoWaitlist = poolFull && !instance.waitlistEnabled && !(mine === "going" && myMode === mode) && mine !== "waitlisted";
  const remoteFull = mode === "remote" && poolFull && !(mine === "going" && myMode === mode);
  const inPersonFull = instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0;
  const remoteFullMessage = inPersonFull
    ? `Remote seats are full.${instance.waitlistEnabled ? " You can join the remote waitlist." : ""}`
    : instance.waitlistEnabled
      ? "Remote seats are full. You can still come in person, or join the remote waitlist."
      : "Remote seats are full. You can still come in person.";
  const disabled = mutationGate.busy;

  const answerFor = (status: RegistrationStatus, attendanceMode = mode,
    childcare?: { childcareCount: number; childcareAgesNote: string }): SeriesAnswer => ({
      status, attendanceMode, guestCount: status === "going" ? guests : 0, childcare,
    });

  async function choose(answer: SeriesAnswer, switching = false, singleDate = false) {
    if (instance.recurrenceRule && !switching && !singleDate) {
      series.setPending(answer);
      return;
    }
    const release = mutationGate.acquire();
    if (!release) return;
    setBusy(answer.status);
    setError(null);
    setChildcareError(null);
    setChildcareFullError(false);
    let focusChildcareError = false;
    try {
      const result = await Events.register(instance.eventId, {
        status: answer.status, date: instance.date, guestCount: answer.status === "going" ? answer.guestCount : 0,
        attendanceMode: instance.remoteAvailable ? answer.attendanceMode : undefined,
        ...(answer.status === "going" && instance.childcareAvailable
          ? answer.childcare ?? (mine !== "going" ? { childcareCount: 0, childcareAgesNote: "" } : undefined)
          : undefined),
      });
      const registration: RegistrationResponse | null = result.registration;
      if (!registration) return;
      series.reportDateMutation(registration);
      onChange(result);
      setEditingCare(false);
      if (switching) {
        series.announce(registration.status === "waitlisted"
          ? `You're on the ${answer.attendanceMode === "remote" ? "remote" : "in-person"} waitlist.`
          : `Switched to ${answer.attendanceMode === "remote" ? "remote" : "in person"}`);
      } else if (instance.childcareAvailable && answer.status === "going") {
        setChildren(String(registration.childcareCount));
        setAges(registration.childcareAgesNote);
        setSavedCare({ count: registration.childcareCount, ages: registration.childcareAgesNote });
        // A saved going answer is confirmed by the standing line (DEC-92);
        // the childcare summary below it carries the count.
        series.announce(registration.status === "waitlisted"
          ? `You're on the ${answer.attendanceMode === "remote" ? "remote" : "in-person"} waitlist.`
          : "");
      }
    } catch (e) {
      if (switching) setMode(myMode);
      if (errorCode(e) === "childcare_full") {
        showFieldErrors(root.current, () => {
          setBusy(null);
          setChildcareError(errorText(e, "We couldn't save your childcare request."));
          setChildcareFullError(true);
        });
        focusChildcareError = true;
        return;
      }
      setError(answer.attendanceMode === "remote" && errorCode(e) === "event_full"
        ? remoteFullMessage
        : errorText(e, "We couldn't save your answer."));
    } finally {
      setBusy(null);
      release();
      if (focusChildcareError) queueMicrotask(() => focusFirstInvalid(root.current));
    }
  }

  function saveChildcare() {
    const problem = childcareCountProblem(children);
    if (problem) {
      showFieldErrors(root.current, () => setChildcareError(problem));
      return;
    }
    void choose(answerFor("going", mode, { childcareCount: Number(children), childcareAgesNote: ages.trim() }));
  }

  function openChildcare() {
    careFocus.current = "field";
    setEditingCare(true);
  }

  function cancelChildcare() {
    careFocus.current = "toggle";
    setChildren(String(savedCare.count));
    setAges(savedCare.ages);
    setChildcareError(null);
    setChildcareFullError(false);
    setEditingCare(false);
  }

  if (removed) {
    return <div className="text-sm text-text-muted">A leader or host removed your registration for this date. Ask them to add you back.</div>;
  }

  return <div ref={root}>
    {waitlisted && <div className="text-sm font-semibold text-text-muted mb-2">
      Waitlisted{instance.myWaitlistPosition != null ? ` — position ${instance.myWaitlistPosition}` : ""}. We&apos;ll email you if a spot opens.
    </div>}
    {remoteFull ? <div className="text-sm font-semibold text-text-muted mb-2">{remoteFullMessage}</div>
      : fullNoWaitlist && <div className="text-sm font-semibold text-text-muted mb-2">This event is full.</div>}
    {offersRemote && <div className="mb-2">
      <Choices legend="How you'll attend">
        {MODES.map((m) => <Radio key={m.value} name={modeName} label={m.label} checked={mode === m.value} disabled={disabled}
          onChange={() => { setMode(m.value); if (mine === "going") void choose(answerFor("going", m.value), true); }} />)}
      </Choices>
      <div aria-live="polite" className="text-sm text-text-muted mt-2">
        {mine !== "going" && `Tap Going to attend ${mode === "remote" ? "remotely" : "in person"}.`}
      </div>
    </div>}
    <div className="flex gap-2 flex-wrap items-center">
      <div role="group" aria-label="Your answer" className="flex w-full sm:max-w-sm">
        {OPTIONS.map((o) => {
          const active = mine === o.value || (o.value === "going" && mine === "waitlisted");
          return <SegmentButton key={o.value} pressed={active} disabled={disabled || (o.value === "going" && fullNoWaitlist)}
            onClick={() => void choose(answerFor(o.value))}>{busy === o.value ? "…" : o.label}</SegmentButton>;
        })}
      </div>
      {instance.guestsAllowed > 0 && <Field label="Extra guests" hint={`Up to ${instance.guestsAllowed}.`}>
        <Input type="number" min={0} max={instance.guestsAllowed} value={guests} disabled={disabled}
          onChange={(e) => setGuests(Math.max(0, Math.min(instance.guestsAllowed, Number(e.target.value))))} layout="w-28" />
      </Field>}
    </div>
    <SeriesFeedback series={series} fallback={answerLine(mine)} />
    {instance.childcareAvailable && mine === "going" && (editingCare
      ? <div ref={careRef} className="mt-3 space-y-3">
        <ChildcareRequestField count={children} ages={ages} onCount={setChildren} onAges={setAges}
          error={childcareError} disabled={disabled} full={instance.childcareFull} />
        <div className="flex flex-wrap gap-2">
          <Button variant="primary" disabled={disabled} onClick={saveChildcare}>{busy === "going" ? "Saving…" : "Save childcare"}</Button>
          <Button variant="secondary" disabled={disabled} onClick={cancelChildcare}>Cancel</Button>
          {childcareFullError && <Button variant="secondary" disabled={disabled}
            onClick={() => void choose(answerFor("going", mode, { childcareCount: 0, childcareAgesNote: "" }))}>Clear childcare request</Button>}
        </div>
      </div>
      : <div ref={careRef} className="mt-3 text-sm">
        {savedCare.count > 0
          ? <>{childcareSummary(savedCare.count, savedCare.ages)} · <Button variant="link" aria-label="Change childcare request" onClick={openChildcare}>Change</Button></>
          : <Button variant="link" onClick={openChildcare}>Bringing children? Request childcare</Button>}
      </div>)}
    {error && <FormError layout="mt-1">{error}</FormError>}
    <SeriesScopeDialog series={series} onJustThisDate={(answer) => void choose(answer, false, true)} onApplied={() => setEditingCare(false)} />
  </div>;
}

// The household is loaded once by the page and shared across date editors.
export function RsvpButtons({ instance, people = [], onChange, seriesFeedback, onSeriesFeedback, onTerminalRefresh, mutationGate }: {
  instance: EventOccurrence;
  people?: HouseholdPerson[];
  onChange: (result: RegistrationBatch) => void;
  seriesFeedback?: SeriesFeedbackSignal | null;
  onSeriesFeedback?: SeriesFeedbackReporter;
  onTerminalRefresh?: () => void;
  mutationGate?: RsvpMutationGate;
}) {
  const localMutationGate = useRsvpMutationGate();
  const activeMutationGate = mutationGate ?? localMutationGate;
  if (!people.length) return <SingleRsvpButtons instance={instance} onChange={onChange} seriesFeedback={seriesFeedback} onSeriesFeedback={onSeriesFeedback} onTerminalRefresh={onTerminalRefresh} mutationGate={activeMutationGate} />;
  return <HouseholdRsvp instance={instance} people={people} onChange={onChange} seriesFeedback={seriesFeedback} onSeriesFeedback={onSeriesFeedback} onTerminalRefresh={onTerminalRefresh} mutationGate={activeMutationGate} />;
}

type Answer = { selected: boolean; status: "going" | "maybe" | "declined"; mode: AttendanceMode; care: boolean };
const draftAnswer = (status: string | null | undefined, mode: AttendanceMode = "in_person", care = false): Answer => ({ selected: status === "going" || status === "waitlisted" || status === "maybe", status: status === "maybe" ? "maybe" : "going", mode, care });
const ELIGIBILITY: Record<HouseholdPerson["registrationEligibility"], string> = {
  eligible: "", link_pending: "Waiting for an admin to confirm", link_unconfirmed: "Ask your church office to confirm this household link.",
  birth_date_required: "Add a birth date in Profile before signing them up.", age_mismatch: "Check their birth date and kind in Profile.",
  converted: "This person now has an account.", registration_holder_unavailable: "Ask your church office to check the responsible account.",
};

function HouseholdRsvp({ instance, people, onChange, seriesFeedback, onSeriesFeedback, onTerminalRefresh, mutationGate }: {
  instance: EventOccurrence;
  people: HouseholdPerson[];
  onChange: (result: RegistrationBatch) => void;
  seriesFeedback?: SeriesFeedbackSignal | null;
  onSeriesFeedback?: SeriesFeedbackReporter;
  onTerminalRefresh?: () => void;
  mutationGate: RsvpMutationGate;
}) {
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
  useActorDraftReconciliation(instance, seriesFeedback, (saved) => {
    setMe(draftAnswer(saved.status, saved.mode));
    setGuests(saved.guests);
    setChildren(String(saved.children));
    setAges(saved.ages);
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [message, setMessage] = useState("");
  const [childcareError, setChildcareError] = useState<string | null>(null);
  const group = useId();
  const patch = (id: number, values: Partial<Answer>) => setDrafts((d) => ({ ...d, [id]: { ...d[id], ...values } }));
  const offersRemote = instance.remoteAvailable && instance.remoteCapacity !== 0;
  const series = useActorSeriesRsvp({ instance, onChange, seriesFeedback, onSeriesFeedback, onTerminalRefresh, mutationGate });
  const disabled = mutationGate.busy;
  const answerFor = (status: RegistrationStatus = me.selected ? me.status : "declined"): SeriesAnswer => ({
    status,
    attendanceMode: me.mode,
    guestCount: status === "going" ? guests : 0,
    ...(status === "going" && instance.childcareAvailable
      ? { childcare: { childcareCount: Number(children), childcareAgesNote: ages.trim() } }
      : {}),
  });

  function openActorScope() {
    if (me.selected && me.status === "going" && instance.childcareAvailable) {
      const problem = childcareCountProblem(children);
      if (problem) {
        showFieldErrors(root.current, () => setChildcareError(problem));
        return;
      }
    }
    setChildcareError(null);
    series.setPending(answerFor());
  }

  async function saveActorDate(answer: SeriesAnswer) {
    const release = mutationGate.acquire();
    if (!release) return;
    setBusy(true);
    setError(null);
    try {
      const result = await Events.register(instance.eventId, {
        status: answer.status,
        date: instance.date,
        guestCount: answer.status === "going" ? answer.guestCount : 0,
        attendanceMode: instance.remoteAvailable ? answer.attendanceMode : undefined,
        ...(answer.status === "going" && instance.childcareAvailable ? answer.childcare : {}),
      });
      if (!result.registration) return;
      const registration = result.registration;
      series.reportDateMutation(registration);
      setMessage(registration.status === "waitlisted" ? `You: waitlist position ${registration.waitlistPosition}`
        : `You: ${registration.status === "going" ? `going ${registration.attendanceMode === "remote" ? "remotely" : "in person"}` : registration.status === "declined" ? "can't go" : "maybe"}`);
      onChange(result);
    } catch (e) {
      setError(errorText(e, "We couldn't save your answer."));
    } finally {
      setBusy(false);
      release();
    }
  }

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
    const release = mutationGate.acquire();
    if (!release) return;
    setBusy(true);
    let personToFocus: number | undefined;
    try {
      const result = await Events.register(instance.eventId, command);
      const describe = (name: string, status: string, mode: AttendanceMode, position: number | null) => status === "waitlisted" ? name + ": waitlist position " + position : name + ": " + (status === "going" ? "going " + (mode === "remote" ? "remotely" : "in person") : status === "declined" ? "can't go" : "maybe");
      const outcomes = result.people.map((r: HouseholdRegistration) => describe(people.find((p) => p.id === r.personId)?.name ?? "Household member", r.status, r.attendanceMode, r.waitlistPosition));
      if (result.registration) { const r = result.registration; outcomes.unshift(describe("You", r.status, r.attendanceMode, r.waitlistPosition)); }
      if (command.status && result.registration) series.reportDateMutation(result.registration);
      setMessage(outcomes.join("; ")); onChange(result);
    } catch (e) {
      setError(errorText(e, "We couldn't save your answers."));
      const personId = e && typeof e === "object" && "data" in e && e.data && typeof e.data === "object" && "personId" in e.data ? e.data.personId : undefined;
      if (typeof personId === "number") {
        showFieldErrors(root.current, () => setBusy(false));
        personToFocus = personId;
      }
    } finally {
      setBusy(false);
      release();
      if (personToFocus !== undefined) queueMicrotask(() => root.current?.querySelector<HTMLElement>(`[data-household-person="${personToFocus}"] input`)?.focus());
    }
  }

  const controls = (name: string, d: Answer, change: (patch: Partial<Answer>) => void, child = false) => <div className="space-y-3 mt-2">
    <Field label={"Answer for " + name}><Select value={d.status} disabled={disabled} onChange={(e) => change({ status: e.target.value as Answer["status"] })}>
      <option value="going">Going</option><option value="maybe">Maybe</option><option value="declined">Can't go</option>
    </Select></Field>
    {offersRemote && <Choices legend={"How " + name + " will attend"}>{MODES.map((m) => <Radio key={m.value} name={group + name} label={m.label} checked={d.mode === m.value} disabled={disabled} onChange={() => change({ mode: m.value, ...(m.value === "remote" ? { care: false } : {}) })} />)}</Choices>}
    {child && d.mode === "in_person" && (instance.childcareAvailable || d.care) && <Checkbox label={"Childcare for " + name} checked={d.care} disabled={disabled || (!instance.childcareAvailable && !d.care)} onChange={(e) => change({ care: e.target.checked })} />}
    {child && d.mode === "remote" && <p className="text-sm text-text-muted">Remote attendees do not use childcare.</p>}
  </div>;

  return <Form noValidate ref={root} onSubmit={save} className="space-y-4" aria-label="Household answers">
    <h4 className="font-semibold">Who will attend?</h4>
    <div className="rounded-lg border border-border p-3">
      {instance.myRegistration === "removed" ? <p>A leader or host removed your registration for this date. Ask them to add you back.</p> : <>
        <Checkbox label="Me" checked={me.selected} disabled={disabled} onChange={(e) => setMe({ ...me, selected: e.target.checked })} />
        {me.selected && <>{controls("you", me, (values) => setMe({ ...me, ...values }))}
          {instance.guestsAllowed > 0 && <Field label="Extra guests" hint={"Up to " + instance.guestsAllowed + "."}><Input type="number" min={0} max={instance.guestsAllowed} value={guests} disabled={disabled} onChange={(e) => setGuests(Number(e.target.value))} /></Field>}
          {instance.childcareAvailable && <ChildcareRequestField count={children} ages={ages} onCount={setChildren} onAges={setAges} error={childcareError} disabled={disabled} full={instance.childcareFull} named />}
        </>}
        {instance.recurrenceRule && <div className="mt-3">
          <Button variant="secondary" disabled={disabled || !me.selected} onClick={openActorScope}>Choose dates for my answer</Button>
          <p className="text-sm text-text-muted mt-1">{me.selected ? "Only your account changes. Household answers stay on this date." : "Select Me and choose an answer first."}</p>
        </div>}
      </>}
    </div>
    {people.map((p) => {
      const d = drafts[p.id]; const saved = own.find((r) => r.personId === p.id);
      const reason = saved?.status === "removed" ? "A leader removed this registration. Ask them to add this person back." : saved?.unavailable ? "The responsible account is unavailable. Ask your church office to check it." : ELIGIBILITY[p.registrationEligibility];
      return <div key={p.id} data-household-person={p.id} className="rounded-lg border border-border p-3">
        <Checkbox label={p.name} checked={d.selected} disabled={disabled || !!reason} onChange={(e) => patch(p.id, { selected: e.target.checked })} />
        <p className="text-sm text-text-muted">{p.kind === "child" ? "Child" : p.kind === "teen" ? "Teen" : "Adult"}{p.age !== undefined ? " · Age " + p.age : ""}</p>
        {saved && <p className="text-sm text-text-muted">Signed up by {saved.holderName}</p>}
        {saved && !saved.unavailable && <p className="text-sm text-text-muted">{saved.status === "waitlisted" ? `Waitlist position ${saved.waitlistPosition}` : saved.status === "going" ? `Going ${saved.attendanceMode === "remote" ? "remotely" : "in person"}` : saved.status === "maybe" ? "Maybe" : saved.status === "declined" ? "Can’t go" : "Removed"}</p>}
        {reason && <p className="text-sm text-text-muted">{reason} <Link className="inline-flex min-h-11 items-center text-primary underline" to="/profile">Edit household in Profile</Link></p>}
        {!reason && d.selected && controls(p.name, d, (values) => patch(p.id, values), p.kind === "child")}
      </div>;
    })}
    {instance.capacity !== null && (instance.spotsLeft ?? 1) <= 0 && <p>In-person seats are full.{instance.waitlistEnabled ? " New answers may be waitlisted." : " Ask a leader for help."}</p>}
    {instance.childcareFull && <p>Childcare is full for this date.</p>}
    {instance.recurrenceRule && <SeriesFeedback series={series} />}
    {error && <FormError>{error}</FormError>}
    <p role="status" className="text-sm text-text-muted">{message}</p>
    <Button variant="primary" type="submit" disabled={disabled}>{busy ? "Saving…" : "Save answers"}</Button>
    <SeriesScopeDialog series={series} onJustThisDate={(answer) => void saveActorDate(answer)} />
  </Form>;
}
