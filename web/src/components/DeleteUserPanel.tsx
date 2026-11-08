import { useEffect, useRef, useState, useSyncExternalStore, type ReactNode, type RefObject } from "react";
import { AdminUsers, ApiError } from "../api";
import { useAuth } from "../auth";
import type { ErasureReasonItem, ErasureReasonPage, ErasureReport, HandoffCandidate, SoleHostedEvent, SoleLedGroup, User } from "../types";
import { ERROR_COPY, errorCode, errorText } from "../lib/errors";
import { needsReload, needsRestoreReload, requireReload, requireRestoreReload } from "../lib/deleteReload";
import { formatEventDay } from "../lib/when";
import { Button } from "./ui/Button";
import { Field, FormError, Select } from "./ui/Field";
import { ReasonProgress, ReasonReviewSection, ReasonSummary } from "./ReasonReviewSection";
import { ReasonReviewStore } from "../lib/reasonReviewStore";
import { DigestUnavailableError, reasonReviewBody, type ReasonReviewBody } from "../lib/reasonSetDigest";

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
//
// Contact-request reasons (T07B-2c, scaled in T07B-2): before Delete turns on,
// the panel walks the server's review pages to the end into a store
// (lib/reasonReviewStore.ts) and the admin chooses Keep or Blank for every
// reason, one by one (nothing is preselected and there is no choose-all), 50 to
// a display page. The choices live in that store, in this page's memory only.
// A press freezes the store, hashes the reviewed set (lib/reasonSetDigest.ts)
// and sends the count, the digest and the two-bit choices; the server
// recomputes the set under its own locks and answers 409 when it differs, and
// the panel then reloads the review and keeps only the choices whose item is
// unchanged.

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

// The reason review (T07B-D4 R4-C07). The first two lines are the disclosures
// the owner decisions require; the rest says where the admin stands.
const REVIEW_INTRO =
  "Before you delete this account, choose for each contact-request reason below whether Fold keeps the text or blanks it. Reasons can mention other people. Blanking removes only the text; the request and its decisions stay.";
const REVIEW_LIMIT =
  "Fold lists only requests this person made or was asked about. Reasons in other people's requests that mention them aren't listed here.";
const REVIEW_MEMORY = "Your choices stay on this page only until you delete or close it.";
const REVIEW_NONE = "No reasons to review.";
const REVIEW_CHECKING = "Checking their contact-request reasons…";
const REVIEW_STOPPED = "You stopped checking their contact-request reasons. Delete stays off until Fold has checked all of them.";
const HASH_PREPARING = "Preparing your choices…";
const HASH_TRANSIENT = "Fold couldn't prepare your choices just now. Try again.";
const HASH_PERMANENT = "This browser can't prepare the review safely. Open this page in a current browser.";
const STORE_CHANGED = "The reasons changed while Fold was preparing your choices. Check them and try again.";
const REVIEW_IRREVERSIBLE = "Blanked text can't be brought back in Fold. Backups made before now still contain it.";
// Shown when a DELETE answers needs_reason_review: the panel has already
// reloaded the review, so ERROR_COPY's "reload this page" would be wrong here.
const REVIEW_NEEDED = "This person has contact-request reasons to review before the account can be deleted. They are listed below.";

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
  // This page was made by an older version of Fold (reason_review_outdated):
  // nothing can be sent from it; reloading the page is the way forward.
  | { kind: "outdated" }
  | { kind: "done"; erasure: ErasureReport | null };

// What a failed DELETE means. "uncertain" settles the same id; the rest are
// definitive (the server ended the id before answering, or never saw it).
type Failure =
  | { kind: "uncertain"; text: string }
  | { kind: "retry"; text: string }
  // The reasons changed or were never reviewed: reload the review, keep what still matches.
  | { kind: "review"; text: string }
  | { kind: "outdated"; text: string }
  | { kind: "locked"; text: string; reload: boolean };

// Where the walk through the review pages stands. "idle" is a panel that
// never asks (a disable, or Delete held off until a reload).
type ReviewState = {
  phase: "idle" | "loading" | "stopped" | "failed" | "done" | "blocked";
  failure: string | null;
  // Walks that reached their end. A counter, not the phase, so a reload that
  // ends in "done" again (the renders can coalesce) is still seen.
  finished: number;
};

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
  // The reason review's three refusals are definitive (the server ended the id first).
  if (code === "needs_reason_review") return { kind: "review", text: REVIEW_NEEDED };
  if (code === "reason_review_changed") return { kind: "review", text: ERROR_COPY.reason_review_changed };
  if (code === "reason_review_outdated") return { kind: "outdated", text: ERROR_COPY.reason_review_outdated };
  if (code === "reason_review_not_allowed") return { kind: "locked", text: ERROR_COPY.reason_review_not_allowed, reload: false };
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

function isReasonItem(v: unknown): v is ErasureReasonItem {
  return isRecord(v) && ["requestId", "reviewHash", "role", "purpose", "status", "createdAt", "counterpartName", "reason"].every((k) => typeof v[k] === "string")
    && typeof v.counterpartDeleted === "boolean" && typeof v.reviewerIsParty === "boolean";
}

// A page the panel can rely on: anything else is a failed page, retried from
// the same cursor, never read as "no reasons".
function isReasonPage(v: unknown): v is ErasureReasonPage {
  return isRecord(v) && Array.isArray(v.items) && v.items.every(isReasonItem) && (v.next === null || typeof v.next === "string");
}

// What a failed review page means. Only a failure that retrying cannot fix
// locks the panel; every other one offers Try again from the same cursor.
function pageFailureOf(e: unknown): Failure {
  if (e instanceof ApiError && !e.unreadable) {
    const code = errorCode(e);
    if (code === "reason_review_not_allowed") return { kind: "locked", text: ERROR_COPY.reason_review_not_allowed, reload: false };
    const ending = endingCopy(e);
    if (ending !== null) return { kind: "locked", text: ending, reload: code === "restore_detected" };
    if (e.status === 404) return { kind: "locked", text: NO_ACCOUNT_COPY, reload: true };
    // The page route repeats the deletion's own refusals (your own account, the last admin).
    if (e.status === 400) return { kind: "locked", text: errorText(e, "That didn't work."), reload: false };
  }
  return { kind: "retry", text: ERROR_COPY.reason_review_busy };
}

const numbers = new Intl.NumberFormat("en-US");
const reasonsWord = (n: number) => `${numbers.format(n)} ${n === 1 ? "reason" : "reasons"}`;

// The sentence under the success line, from the 200 body only.
function reasonCounts(erasure: ErasureReport | null): string | null {
  if (!erasure || erasure.reasonsKept + erasure.reasonsRemoved === 0) return null;
  const { reasonsKept: k, reasonsRemoved: r } = erasure;
  return `${r === 1 ? "1 reason was" : `${r} reasons were`} blanked and ${k} kept.`;
}

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
  // The reason review. `choices` and `changed` are keyed by request id. They
  // are memory only: closing the panel drops them (REVIEW_MEMORY says so).
  const [review, setReview] = useState<ReviewState>({
    phase: deleting && heldFor === null ? "loading" : "idle", failure: null, finished: 0,
  });
  // The walk's result and every choice: a store, not React state, so a click
  // re-renders one row (lib/reasonReviewStore.ts). The panel reads only the few
  // facts it needs, as primitives, so it re-renders when they change.
  const [store] = useState(() => new ReasonReviewStore());
  const reasonTotal = useSyncExternalStore(store.subscribe, () => store.total);
  const allChosen = useSyncExternalStore(store.subscribe, () => store.getSnapshot().unchosen === 0);
  // Where the display should jump (after a reload), and the cursor to continue
  // from (null: from the start), the walk that may still write, and whether
  // focus should move to the first changed item when a reload ends.
  const [jump, setJump] = useState<{ ordinal: number; tick: number } | null>(null);
  const cursor = useRef<string | null>(null);
  const walkRun = useRef(0);
  const refocus = useRef(false);
  // Preparing the digest of a press, and a browser that cannot (permanent).
  const [preparing, setPreparing] = useState(false);
  const [hashBroken, setHashBroken] = useState(false);
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
      walkRun.current++; // a page still in flight is dropped with the panel
    };
  }, []);
  const isCurrent = (press: number) => mounted.current && presses.current === press;

  // Walks the review pages from `from` to the end. One unit per request; a
  // failed unit stops here and "Try again" continues from the same cursor, so
  // nothing already read is read again. `fresh` starts a reload: the choices
  // made so far are set aside, and an item keeps its choice only when its hash
  // is unchanged (a new or different item is marked and must be chosen again).
  async function walk(from: string | null, fresh = false) {
    const run = ++walkRun.current;
    const live = () => mounted.current && walkRun.current === run;
    if (from === null) {
      // A reload sets the choices aside; an item keeps its one only when its hash is unchanged.
      store.reset(fresh ? store.carry() : null);
      cursor.current = null;
    }
    setReview((r) => ({ ...r, phase: "loading", failure: null }));
    const seen = new Set<string>();
    let at = from;
    for (;;) {
      let page: ErasureReasonPage;
      try {
        page = await AdminUsers.erasureReasonsPage(target.id, at);
      } catch (e) {
        if (live()) reviewFailed(e);
        return;
      }
      if (!live()) return;
      if (!isReasonPage(page)) {
        reviewFailed(new Error("unexpected answer to a reason page"));
        return;
      }
      try {
        store.appendPage(page.items);
      } catch (e) {
        // A page out of order, with a repeated id or a malformed hash cannot be
        // trusted: it fails like any unexpected answer and Delete stays off.
        reviewFailed(e);
        return;
      }
      cursor.current = page.next;
      if (page.next === null) {
        setReview((r) => ({ ...r, phase: "done", finished: r.finished + 1 }));
        return;
      }
      // A cursor that does not move would loop for ever.
      if (page.next === at || seen.has(page.next)) {
        reviewFailed(new Error("the review cursor did not advance"));
        return;
      }
      seen.add(page.next);
      at = page.next;
    }
  }

  function reviewFailed(e: unknown) {
    const failure = pageFailureOf(e);
    if (failure.kind === "locked") {
      noteRestore(e);
      setError(failure.text);
      setPhase({ kind: "locked", reload: failure.reload });
      setReview((r) => ({ ...r, phase: "blocked", failure: null }));
    } else {
      setReview((r) => ({ ...r, phase: "failed", failure: failure.kind === "retry" ? failure.text : ERROR_COPY.reason_review_busy }));
    }
  }

  function stopReview() {
    walkRun.current++; // the page in flight is dropped; Continue asks for it again
    setReview((r) => ({ ...r, phase: "stopped" }));
  }

  // The walk starts with the panel (Delete only; a disable has no reasons, and
  // a Delete held off until a reload sends no request).
  useEffect(() => {
    if (deleting && heldFor === null) void walk(null);
    // Once per panel: the body remounts per person and action.
  }, []);

  // After a reload that followed a 409: the first item to choose again, else the message.
  useEffect(() => {
    if (review.finished === 0 || !refocus.current) return;
    refocus.current = false;
    const first = store.nextChanged(-1);
    if (first >= 0) setJump((j) => ({ ordinal: first, tick: (j?.tick ?? 0) + 1 }));
    else alertBox.current?.focus();
  }, [review.finished]);

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
    // Nothing is minted or sent while a reason still has no choice.
    if (review.phase !== "done" || store.getSnapshot().unchosen !== 0 || hashBroken) return;
    const press = ++presses.current;
    attempt.current = null;
    // 1. Freeze: every radio is disabled (busy) and the store is read once.
    setBusy(true);
    setError(null);
    setPhase({ kind: "idle" });
    const frozen = store.freeze();
    let reasonReview: ReasonReviewBody | undefined;
    if (frozen.count > 0) {
      // 2. Hash the reviewed set. This runs before any id is minted: a failure
      // here sends nothing and leaves the choices as they are.
      setPreparing(true);
      try {
        reasonReview = await reasonReviewBody(frozen.ids, frozen.hashes, frozen.choices, frozen.count);
      } catch (e) {
        if (!isCurrent(press)) return;
        setPreparing(false);
        setBusy(false);
        if (e instanceof DigestUnavailableError) setHashBroken(true);
        setError(e instanceof DigestUnavailableError ? HASH_PERMANENT : HASH_TRANSIENT);
        return;
      }
      if (!isCurrent(press)) return;
      setPreparing(false);
      // 3. Nothing may have changed while the digest was made.
      if (store.getVersion() !== frozen.version) {
        setBusy(false);
        setError(STORE_CHANGED);
        return;
      }
    }
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
      // The count, the set digest and every choice travel in the review. No
      // reasons loaded: no review in the body, and the server ignores one anyway.
      const res = reasonReview
        ? await AdminUsers.remove(target.id, picks, hostPicks, id, reasonReview)
        : await AdminUsers.remove(target.id, picks, hostPicks, id);
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
      if (failure.kind === "outdated") setPhase({ kind: "outdated" });
      // The server ended the id before it answered, so the next press mints a
      // fresh one; first the review is read again and the admin chooses again
      // where it changed.
      if (failure.kind === "review") {
        refocus.current = true;
        void walk(null, true);
      }
    } finally {
      if (isCurrent(press)) setBusy(false);
    }
  }

  const name = target.displayName || target.name;
  // Delete stays off while an answer is unresolved or the attempt has ended it.
  const canPress = (phase.kind === "idle" || phase.kind === "notApplied") && !hashBroken;
  // Closing reloads the list when the account is gone or may be.
  // The answer is in: the questions above it no longer apply.
  const finished = phase.kind === "done" || phase.kind === "already";
  const closeReloads = phase.kind === "already" || phase.kind === "history" || (phase.kind === "locked" && phase.reload);
  const ready = groups !== null && (!deleting || events !== null);
  const hosted = events ?? [];
  const missing = hosted.filter((ev) => hostPicks[String(ev.id)] === undefined).length;
  const hostsDone = hosted.length === 0 || (candidates.length > 0 && missing === 0);
  // Delete needs the whole review read and every reason chosen. `unchosen`
  // counts what is left; `kept` and `removed` are what the press would do.
  const reviewDone = !deleting || (review.phase === "done" && allChosen);
  const hasReasons = reasonTotal > 0;
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
      {!finished && deleting && review.phase !== "idle" && (
        <section aria-label="Reasons to review" className="space-y-3">
          {review.phase === "loading" && (
            <div className="space-y-2">
              <p aria-live="polite" className="text-text-muted">
                {REVIEW_CHECKING}
                {hasReasons ? ` ${reasonsWord(reasonTotal)} so far.` : ""}
              </p>
              <Button variant="secondary" onClick={stopReview}>
                Stop
              </Button>
            </div>
          )}
          {review.phase === "stopped" && (
            <div className="space-y-2">
              <p className="text-text-muted">{REVIEW_STOPPED}</p>
              <Button variant="secondary" onClick={() => void walk(cursor.current)}>
                Keep checking
              </Button>
            </div>
          )}
          {review.phase === "failed" && (
            <div className="space-y-2">
              <FormError>{review.failure}</FormError>
              <Button variant="secondary" onClick={() => void walk(cursor.current)}>
                Try again
              </Button>
            </div>
          )}
          {review.phase === "done" && !hasReasons && <p className="text-text-muted">{REVIEW_NONE}</p>}
          {hasReasons && (
            <>
              <div className="space-y-1 text-text-muted">
                <p>{REVIEW_INTRO}</p>
                <p>{REVIEW_LIMIT}</p>
                <p>{REVIEW_MEMORY}</p>
              </div>
              <ReasonProgress store={store} done={review.phase === "done"} />
              <ReasonReviewSection store={store} disabled={busy || phase.kind === "locked" || phase.kind === "outdated"} loading={review.phase !== "done"} jump={jump} />
            </>
          )}
        </section>
      )}
      {error && (
        <div ref={alertBox} tabIndex={-1}>
          <FormError>{error}</FormError>
        </div>
      )}
      {!finished && deleting && hasReasons && reviewDone && canPress && (
        <div className="space-y-1">
          <ReasonSummary store={store} />
          <Irreversible store={store} />
        </div>
      )}
      {preparing && (
        <p role="status" className="text-text-muted">
          {HASH_PREPARING}
        </p>
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
            {phase.kind === "outdated" && (
              <Button variant="secondary" onClick={() => window.location.reload()}>
                Reload page
              </Button>
            )}
            {phase.kind !== "already" && (
              <Button variant="danger" onClick={remove} disabled={busy || !ready || (deleting && !hostsDone) || !reviewDone || !canPress}>
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

// The note that blanked text cannot come back, only when a reason is to be blanked.
function Irreversible({ store }: { store: ReasonReviewStore }) {
  const remove = useSyncExternalStore(store.subscribe, () => store.getSnapshot().remove);
  return remove > 0 ? <p className="text-text-muted">{REVIEW_IRREVERSIBLE}</p> : null;
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
          <p className="font-semibold">{reasonCounts(phase.erasure) ? `${APPLIED_COPY} ${reasonCounts(phase.erasure)}` : APPLIED_COPY}</p>
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
