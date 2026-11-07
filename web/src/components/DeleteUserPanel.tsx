import { useEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { AdminUsers, ApiError } from "../api";
import { useAuth } from "../auth";
import type { ErasureReport, HandoffCandidate, SoleHostedEvent, SoleLedGroup, User } from "../types";
import { ERROR_COPY, errorCode, errorText } from "../lib/errors";
import { needsReload, needsRestoreReload, requireReload, requireRestoreReload } from "../lib/deleteReload";
import { formatEventDay } from "../lib/when";
import { Button } from "./ui/Button";
import { Field, FormError, Select } from "./ui/Field";

// Confirms deleting or disabling an account. For every group the person
// leads alone, the admin picks a new leader first: a member of that group or any admin, with
// the deleting admin preselected (DEC-36). Deleting also needs a new host for
// every church-wide event the person hosts alone, and that one is never
// preselected: nobody becomes a host without being chosen (M7.4, R4-H).
//
// Every press of Delete mints its own attempt id (T07B-1c) and sends it with
// the DELETE. The id lives in this component's memory only and is dropped when
// the panel closes. An answer that does not say what happened (a lost
// connection, delete_outcome_unknown, attempt_closed, attempt_expired, a
// response that is not JSON) never reads as "nothing was deleted": the panel
// settles the same id and says what the server answers. Only a definitive
// refusal, or a settle answer of not_applied, lets the next press mint a fresh id.

type HostPicks = Record<string, number>;

// What the settle answers say (T07B-D4 R4-C08). None of these names or implies
// another person.
const APPLIED_COPY = "The account was deleted.";
const NOT_APPLIED_COPY = "Nothing was deleted by this attempt.";
const ALREADY_DELETED_COPY = "The account is already deleted; this attempt did not delete it.";
const HISTORY_COPY = "Fold can't tell whether an earlier delete finished. Reload this person's page to check.";
const STILL_CHECKING_COPY = "Still checking. Fold can't tell yet whether the account was deleted. Check again in a moment.";
const CHECKING_COPY = "Checking what happened to this delete request…";
const NO_ACCOUNT_COPY = "This account no longer exists.";

// What a delete does not reach (R4-6c, DEC-144, O-12). Shown before the press
// and again once the account is gone. The IP line stays for as long as landed
// code can leave a personal address in the activity log (RETAIN_* rows and the
// conversion groups, inventory 6.5.2 IR-1 to IR-4, gate G-9); take it out only
// when that is no longer true, never because the count came back as 0.
const REACH_LEAD = "Some traces can remain after a delete, so you know what to expect:";
const REACH_BACKUPS = "Deleting reaches Fold's backups only as they expire, so their details can stay in an older backup until then.";
const REACH_OUTSIDE = "Copies already sent to mailboxes, mail providers and push services are outside Fold, and Fold can't take them back.";
const REACH_IP = "Some network (IP) addresses can remain in the activity log, and Fold can't always tell whose address an entry holds.";
// The two counts the delete answers with, in words (account_erasure_report.go;
// inventory "Report semantics": a count of entries, never proof of ownership).
const KEPT_HOUSEHOLDS = "Fold left these households as they were, because other people still share them or it couldn't tell whose they were.";
const KEPT_ACTIVITY =
  "These are activity-log entries linked to this person that Fold kept. Some may have no network address. Fold counts entries, not addresses, and a 0 doesn't promise none remain.";

// Where the delete stands after a press. "idle" and "notApplied" leave Delete
// on; every other phase either waits for an answer or ends the attempt.
type Phase =
  | { kind: "idle" }
  | { kind: "checking"; text: string }
  | { kind: "still"; text: string }
  | { kind: "notApplied" }
  | { kind: "already" }
  | { kind: "history" }
  // A definitive refusal that no press can fix (signed out, not allowed, the
  // account is gone, a restore was seen). `reload` closes with a list reload.
  | { kind: "locked"; reload: boolean }
  | { kind: "done"; erasure: ErasureReport | null };

// What a failed DELETE means. "uncertain" settles the same id; the rest are
// definitive (the server ended the id before answering, or never saw it).
type Failure =
  | { kind: "uncertain"; text: string }
  | { kind: "retry"; text: string }
  | { kind: "locked"; text: string; reload: boolean };

const UNCERTAIN: Failure = { kind: "uncertain", text: CHECKING_COPY };
const BUSY_CODES = ["delete_timed_out", "delete_household_busy", "delete_reason_busy", "admin_account_busy", "attempt_busy"] as const;

// Answers that end the attempt on any of the three calls: a restore was seen,
// the session ended, or this admin may not do it.
function endingCopy(e: unknown): string | null {
  if (!(e instanceof ApiError)) return null;
  if (errorCode(e) === "restore_detected") return ERROR_COPY.restore_detected;
  if (e.status === 401) return ERROR_COPY.signed_out;
  if (e.status === 403) return ERROR_COPY.not_allowed;
  return null;
}

// A restore was seen: the whole server, not one person, waits for a fresh list.
function noteRestore(e: unknown): void {
  if (errorCode(e) === "restore_detected") requireRestoreReload();
}

function failureOf(e: unknown): Failure {
  if (!(e instanceof ApiError)) return UNCERTAIN; // a dropped connection or an aborted fetch
  // A failure status whose body is not JSON (a gateway page, nothing at all) is
  // not the server's refusal, whatever its status: it may have deleted.
  if (e.unreadable) return UNCERTAIN;
  const code = errorCode(e);
  if (code === "attempt_closed" || code === "attempt_expired") return { kind: "uncertain", text: ERROR_COPY[code] };
  if (code === "delete_outcome_unknown") return UNCERTAIN;
  const ending = endingCopy(e);
  if (ending !== null) return { kind: "locked", text: ending, reload: code === "restore_detected" };
  if (e.status === 404) return { kind: "locked", text: NO_ACCOUNT_COPY, reload: true };
  for (const busy of BUSY_CODES) if (code === busy) return { kind: "retry", text: ERROR_COPY[busy] };
  // The deletion's own validation answers (last admin, your own account,
  // successors, hosts) are 400s the server sent before it changed anything.
  if (e.status === 400) return { kind: "retry", text: errorText(e, "That didn't work.") };
  // A 5xx with no code, a page from a proxy, any status or code not named
  // above: the server may have deleted the account.
  return UNCERTAIN;
}

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null;

function isCandidate(v: unknown): v is HandoffCandidate {
  return isRecord(v) && typeof v.id === "number" && typeof v.name === "string" && typeof v.admin === "boolean";
}

// An event row from a 400 body; the date is dropped unless it is a string.
function soleEventFrom(v: unknown): SoleHostedEvent | null {
  if (!isRecord(v) || typeof v.id !== "number" || typeof v.title !== "string") return null;
  return typeof v.nextStartsAt === "string" ? { id: v.id, title: v.title, nextStartsAt: v.nextStartsAt } : { id: v.id, title: v.title };
}

function isSoleLedGroup(v: unknown): v is SoleLedGroup {
  return isRecord(v) && typeof v.id === "number" && typeof v.name === "string" && typeof v.status === "string"
    && Array.isArray(v.candidates) && v.candidates.every(isCandidate);
}

// The admin is the default for a group, else the first person listed.
function groupPick(g: SoleLedGroup, meId: number | undefined): number | undefined {
  return (g.candidates.find((c) => c.id === meId) ?? g.candidates[0])?.id;
}

function initialGroupPicks(groups: SoleLedGroup[], meId: number | undefined): Record<string, number> {
  const picks: Record<string, number> = {};
  for (const g of groups) {
    const pick = groupPick(g, meId);
    if (pick !== undefined) picks[String(g.id)] = pick;
  }
  return picks;
}

const candidateLabel = (c: HandoffCandidate, meId: number | undefined) =>
  `${c.id === meId ? `Me (${c.name})` : c.name}${c.admin && c.id !== meId ? " · admin" : ""}`;

type PanelProps = {
  target: User;
  action: "delete" | "disable";
  onDone: (done: boolean) => void;
};

// A new person or a new action is a new panel: its state, its attempt id and
// every request still in flight belong to the old one and are dropped with it
// (the key remounts the body, whose cleanup spends the old presses).
export function DeleteUserPanel(props: PanelProps) {
  return <PanelBody key={`${props.target.id}:${props.action}`} {...props} />;
}

function PanelBody({ target, action, onDone }: PanelProps) {
  const { user: me } = useAuth();
  const meId = me?.id;
  const deleting = action === "delete";
  const [groups, setGroups] = useState<SoleLedGroup[] | null>(null);
  const [events, setEvents] = useState<SoleHostedEvent[] | null>(null);
  const [candidates, setCandidates] = useState<HandoffCandidate[]>([]);
  const [picks, setPicks] = useState<Record<string, number>>({});
  const [hostPicks, setHostPicks] = useState<HostPicks>({});
  const [everyHost, setEveryHost] = useState("");
  const [busy, setBusy] = useState(false);
  // After history_unavailable (this person) or restore_detected (everyone),
  // Delete stays off until the users list has loaded afresh, even in a later
  // panel (lib/deleteReload.ts).
  const heldFor = action === "delete" ? (needsReload(target.id) ? "history" : needsRestoreReload() ? "restore" : null) : null;
  const [error, setError] = useState<string | null>(() => (heldFor === "restore" ? ERROR_COPY.restore_detected : null));
  const [phase, setPhase] = useState<Phase>(() =>
    heldFor === "history" ? { kind: "history" } : heldFor === "restore" ? { kind: "locked", reload: true } : { kind: "idle" },
  );
  const [focusTick, setFocusTick] = useState(0);
  const panel = useRef<HTMLDivElement>(null);
  const alertBox = useRef<HTMLDivElement>(null);
  const statusBox = useRef<HTMLDivElement>(null);
  // The attempt id of the press in flight, memory only. `presses` counts
  // presses and is spent when the panel closes, so a late answer for a dropped
  // id finds no matching press and is ignored.
  const mounted = useRef(true);
  const presses = useRef(0);
  const attempt = useRef<string | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      attempt.current = null;
    };
  }, []);
  const isCurrent = (press: number) => mounted.current && presses.current === press;

  useEffect(() => {
    let live = true;
    // Disabling needs no host: their events keep their host row (DEC-38).
    const hostRequest = deleting ? AdminUsers.soleHostEvents(target.id) : Promise.resolve(null);
    Promise.all([AdminUsers.soleLeaderGroups(target.id), hostRequest])
      .then(([led, hostedEvents]) => {
        if (!live) return;
        setGroups(led.groups);
        setPicks(initialGroupPicks(led.groups, meId));
        if (hostedEvents) {
          setEvents(hostedEvents.events);
          setCandidates(hostedEvents.candidates);
        }
      })
      .catch((e) => {
        if (live) setError(errorText(e, deleting ? "We couldn't load their groups and events." : "We couldn't load the groups."));
      });
    return () => {
      live = false;
    };
  }, [target.id, deleting, meId]);

  // After a stale answer has rendered: the first event still without a host,
  // else the message.
  useEffect(() => {
    if (focusTick === 0) return;
    const open = [...(panel.current?.querySelectorAll<HTMLSelectElement>("select[data-host-event]") ?? [])].find((s) => s.value === "");
    (open ?? alertBox.current)?.focus();
  }, [focusTick]);

  // Once Delete is off or gone, focus would be lost: move it to the answer.
  useEffect(() => {
    if (phase.kind === "locked") alertBox.current?.focus();
    else if (["still", "already", "history", "done"].includes(phase.kind)) statusBox.current?.focus();
  }, [phase]);

  // The server's list of events and candidates changed under us. Keep a pick
  // only while its event and its person both still appear.
  function applyStaleHosts(data: Record<string, unknown>) {
    if (!Array.isArray(data.events) || !Array.isArray(data.candidates)) return;
    const nextEvents = data.events.flatMap((v) => soleEventFrom(v) ?? []);
    const nextCandidates = data.candidates.filter(isCandidate);
    const kept: HostPicks = {};
    for (const ev of nextEvents) {
      const pick = hostPicks[String(ev.id)];
      if (pick !== undefined && nextCandidates.some((c) => c.id === pick)) kept[String(ev.id)] = pick;
    }
    setEvents(nextEvents);
    setCandidates(nextCandidates);
    setHostPicks(kept);
    setEveryHost("");
    setFocusTick((n) => n + 1);
  }

  function applyStaleGroups(data: Record<string, unknown>) {
    if (!Array.isArray(data.groups)) return;
    const next = data.groups.filter(isSoleLedGroup);
    const kept: Record<string, number> = {};
    for (const g of next) {
      const old = picks[String(g.id)];
      const pick = old !== undefined && g.candidates.some((c) => c.id === old) ? old : groupPick(g, meId);
      if (pick !== undefined) kept[String(g.id)] = pick;
    }
    setGroups(next);
    setPicks(kept);
  }

  function chooseEvery(value: string) {
    setEveryHost(value);
    if (value === "") return;
    const all: HostPicks = {};
    for (const ev of events ?? []) all[String(ev.id)] = Number(value);
    setHostPicks(all);
  }

  function chooseHost(eventId: number, value: string) {
    setEveryHost("");
    setHostPicks((p) => {
      const next = { ...p };
      if (value === "") delete next[String(eventId)];
      else next[String(eventId)] = Number(value);
      return next;
    });
  }

  // Settles the id of press `press` and shows what the server answers.
  async function settle(id: string, press: number, text = CHECKING_COPY) {
    setPhase({ kind: "checking", text });
    try {
      const res = await AdminUsers.settleAttempt(id, target.id);
      if (!isCurrent(press)) return;
      if (res.result === "applied") {
        attempt.current = null;
        setPhase({ kind: "done", erasure: null });
      } else if (res.result === "not_applied") {
        attempt.current = null;
        setPhase(res.subjectDeleted === true ? { kind: "already" } : { kind: "notApplied" });
      } else if (res.result === "history_unavailable") {
        attempt.current = null;
        requireReload(target.id);
        setPhase({ kind: "history" });
      } else {
        setPhase({ kind: "still", text: STILL_CHECKING_COPY });
      }
    } catch (e) {
      if (!isCurrent(press)) return;
      const ending = endingCopy(e);
      if (ending === null) {
        setPhase({ kind: "still", text: STILL_CHECKING_COPY });
      } else if (errorCode(e) === "restore_detected") {
        // Checking again after the restart answers history_unavailable.
        noteRestore(e);
        setPhase({ kind: "still", text: ending });
      } else {
        attempt.current = null;
        setError(ending);
        setPhase({ kind: "locked", reload: false });
      }
    }
  }

  async function checkAgain() {
    if (attempt.current !== null) await settle(attempt.current, presses.current);
  }

  function applyStale(e: unknown) {
    if (!(e instanceof ApiError)) return;
    const code = errorCode(e);
    if (code === "needs_host_successor") applyStaleHosts(e.data);
    else if (code === "needs_successor") applyStaleGroups(e.data);
  }

  async function disable() {
    const press = ++presses.current;
    setBusy(true);
    setError(null);
    try {
      await AdminUsers.update(target.id, { status: "disabled", successors: picks });
      if (isCurrent(press)) onDone(true);
    } catch (e) {
      if (!isCurrent(press)) return;
      applyStale(e);
      setError(errorText(e, "That didn't work."));
    } finally {
      if (isCurrent(press)) setBusy(false);
    }
  }

  async function remove() {
    if (!deleting) return disable();
    const press = ++presses.current;
    attempt.current = null;
    setBusy(true);
    setError(null);
    setPhase({ kind: "idle" });
    try {
      // One fresh id per press. The mint reads no database; if it fails, no
      // DELETE has been sent, so nothing can have happened.
      let id: string;
      try {
        id = (await AdminUsers.mintAttempt(target.id)).attemptId;
        if (!id) throw new Error("no attempt id"); // never send a DELETE without one
      } catch (e) {
        if (!isCurrent(press)) return;
        const ending = endingCopy(e);
        setError(ending ?? errorText(e, "That didn't work."));
        if (ending !== null) {
          noteRestore(e);
          setPhase({ kind: "locked", reload: errorCode(e) === "restore_detected" });
        }
        return;
      }
      if (!isCurrent(press)) return;
      attempt.current = id;
      const res = await AdminUsers.remove(target.id, picks, hostPicks, id);
      if (!isCurrent(press)) return;
      attempt.current = null;
      setPhase({ kind: "done", erasure: res.erasure ?? null });
    } catch (e) {
      if (!isCurrent(press)) return;
      const failure = failureOf(e);
      if (failure.kind === "uncertain") {
        // Never a not-deleted message before the settle answer.
        if (attempt.current !== null) void settle(attempt.current, press, failure.text);
        return;
      }
      attempt.current = null;
      noteRestore(e);
      applyStale(e);
      setError(failure.text);
      if (failure.kind === "locked") setPhase({ kind: "locked", reload: failure.reload });
    } finally {
      if (isCurrent(press)) setBusy(false);
    }
  }

  const name = target.displayName || target.name;
  // Delete stays off while an answer is unresolved or the attempt has ended it.
  const canPress = phase.kind === "idle" || phase.kind === "notApplied";
  // Closing reloads the list when the account is gone or may be.
  // The answer is in: the questions above it no longer apply.
  const finished = phase.kind === "done" || phase.kind === "already";
  const closeReloads = phase.kind === "already" || phase.kind === "history" || (phase.kind === "locked" && phase.reload);
  const ready = groups !== null && (!deleting || events !== null);
  const hosted = events ?? [];
  const missing = hosted.filter((ev) => hostPicks[String(ev.id)] === undefined).length;
  const hostsDone = hosted.length === 0 || (candidates.length > 0 && missing === 0);
  return (
    <div ref={panel} className="mt-3 rounded-md border border-danger-border bg-danger-subtle p-4 text-sm space-y-3">
      {!finished && (
        <p className="font-semibold text-danger-on-subtle">
          {deleting ? "Delete" : "Disable"} {name}? They will be signed out and can't sign in.
        </p>
      )}
      {action === "disable" && (
        <p className="text-text-muted">
          Their groups and registrations are kept but hidden until you enable them again; they come back as a member, not a
          leader.
        </p>
      )}
      {!finished && !ready && !error && (
        <p role="status" className="text-text-muted">
          {deleting ? "Checking their groups and events…" : "Checking their groups…"}
        </p>
      )}
      {!finished && groups && groups.length > 0 && (
        <div className="space-y-2">
          <p className="text-text-muted">They are the only leader of these groups. Choose who leads each one next:</p>
          {groups.map((g) => (
            <Field key={g.id} label={`Next leader of ${g.name}`}>
              <Select
                value={picks[String(g.id)] ?? ""}
                onChange={(e) => setPicks((p) => ({ ...p, [String(g.id)]: Number(e.target.value) }))}
                layout="sm:max-w-xs"
              >
                {g.candidates.map((c) => (
                  <option key={c.id} value={c.id}>
                    {candidateLabel(c, meId)}
                  </option>
                ))}
              </Select>
            </Field>
          ))}
        </div>
      )}
      {!finished && deleting && hosted.length > 0 && (
        <div className="space-y-3">
          <p className="text-text-muted">They are the only host of these church-wide events. Choose who hosts each one next:</p>
          {candidates.length === 0 ? (
            <>
              <ul className="list-disc space-y-1 pl-5">
                {hosted.map((ev) => (
                  <li key={ev.id}>
                    <span className="wrap-anywhere">{ev.title}</span>
                    <span className="text-text-muted"> · {nextDate(ev)}</span>
                  </li>
                ))}
              </ul>
              <p className="text-text-muted">No one else can host these events yet. Add or enable an adult account, then come back.</p>
            </>
          ) : (
            <>
              {hosted.length > 1 && (
                <Field label="Host for every event" optional>
                  <Select value={everyHost} onChange={(e) => chooseEvery(e.target.value)} layout="sm:max-w-xs">
                    <option value="">Choose for each event</option>
                    {candidates.map((c) => (
                      <option key={c.id} value={c.id}>
                        {candidateLabel(c, meId)}
                      </option>
                    ))}
                  </Select>
                </Field>
              )}
              {hosted.map((ev) => (
                <Field key={ev.id} label={`New host for ${ev.title}`} hint={nextDate(ev)}>
                  <Select
                    value={hostPicks[String(ev.id)] ?? ""}
                    onChange={(e) => chooseHost(ev.id, e.target.value)}
                    layout="sm:max-w-xs"
                    data-host-event={ev.id}
                  >
                    <option value="" disabled>
                      Choose a host
                    </option>
                    {candidates.map((c) => (
                      <option key={c.id} value={c.id}>
                        {candidateLabel(c, meId)}
                      </option>
                    ))}
                  </Select>
                </Field>
              ))}
              <p aria-live="polite" className="text-text-muted">
                {missing === 0 ? "" : `Choose a new host for ${missing === 1 ? "1 more event" : `${missing} more events`}.`}
              </p>
            </>
          )}
        </div>
      )}
      {error && (
        <div ref={alertBox} tabIndex={-1}>
          <FormError>{error}</FormError>
        </div>
      )}
      <Outcome phase={phase} statusBox={statusBox} />
      {phase.kind === "done" && phase.erasure && <KeptNotes />}
      {deleting && <Reach />}
      <div className="flex flex-wrap gap-3">
        {phase.kind === "done" ? (
          <Button variant="primary" onClick={() => onDone(true)}>
            Done
          </Button>
        ) : (
          <>
            {phase.kind === "still" && (
              <Button variant="secondary" onClick={checkAgain}>
                Check again
              </Button>
            )}
            {phase.kind !== "already" && (
              <Button variant="danger" onClick={remove} disabled={busy || !ready || (deleting && !hostsDone) || !canPress}>
                {busy ? "Working…" : deleting ? "Delete account" : "Disable account"}
              </Button>
            )}
            <Button variant="secondary" onClick={() => onDone(closeReloads)}>
              {phase.kind === "idle" || phase.kind === "notApplied" || phase.kind === "checking" || phase.kind === "still" ? "Cancel" : "Close"}
            </Button>
          </>
        )}
      </div>
    </div>
  );
}

// What the server said about the attempt, in words (never color alone). The
// box takes focus when Delete goes away, so the answer is read.
function Outcome({ phase, statusBox }: { phase: Phase; statusBox: RefObject<HTMLDivElement | null> }) {
  let body: ReactNode = null;
  switch (phase.kind) {
    case "checking":
    case "still":
      body = <p>{phase.text}</p>;
      break;
    case "notApplied":
      body = <p>{NOT_APPLIED_COPY}</p>;
      break;
    case "already":
      body = <p>{ALREADY_DELETED_COPY}</p>;
      break;
    case "history":
      body = <p>{HISTORY_COPY}</p>;
      break;
    case "done":
      body = (
        <>
          <p className="font-semibold">{APPLIED_COPY}</p>
          {phase.erasure && (
            <dl className="grid grid-cols-[auto_auto] justify-start gap-x-4 gap-y-1 text-text-muted">
              <dt>Households kept</dt>
              <dd>{phase.erasure.householdsKept}</dd>
              <dt>Activity entries kept</dt>
              <dd>{phase.erasure.activityEntriesKept}</dd>
            </dl>
          )}
        </>
      );
      break;
    default:
      return null;
  }
  return (
    <div ref={statusBox} role="status" tabIndex={-1} className="space-y-1">
      {body}
    </div>
  );
}

// What the two counts mean, in plain words and no further than the server says.
function KeptNotes() {
  return (
    <ul className="list-disc space-y-1 pl-5 text-text-muted">
      <li>
        <span className="font-semibold">Households kept:</span> {KEPT_HOUSEHOLDS}
      </li>
      <li>
        <span className="font-semibold">Activity entries kept:</span> {KEPT_ACTIVITY}
      </li>
    </ul>
  );
}

function Reach() {
  return (
    <div className="space-y-1 text-text-muted">
      <p>{REACH_LEAD}</p>
      <ul className="list-disc space-y-1 pl-5">
        <li>
          <span className="font-semibold">Backups.</span> {REACH_BACKUPS}
        </li>
        <li>
          <span className="font-semibold">Email and notifications.</span> {REACH_OUTSIDE}
        </li>
        <li>
          <span className="font-semibold">Activity log.</span> {REACH_IP}
        </li>
      </ul>
    </div>
  );
}

function nextDate(ev: SoleHostedEvent): string {
  return ev.nextStartsAt ? `Next date: ${formatEventDay(ev.nextStartsAt)}` : "No upcoming dates";
}
