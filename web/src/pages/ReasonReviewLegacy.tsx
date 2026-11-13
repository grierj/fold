import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { AdminUsers, isCanonicalAccountRef } from "../api/adminUsers";
import { ApiError } from "../api/core";
import { useAuth } from "../auth";
import { ReasonReviewList, deletedAccountLabel, type ReasonChoice } from "../components/ReasonReviewList";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { FormError } from "../components/ui/Field";
import { requireRestoreReload } from "../lib/deleteReload";
import { LEGACY_REVIEW_PATH, LEGACY_REVIEW_TITLE, LEGACY_SEARCH_LIMIT, LEGACY_NO_ACCOUNT, legacyPageFailure, legacyReadFailure } from "../lib/legacyReview";
import { ERROR_COPY } from "../lib/errors";
import {
  LEGACY_CHECKING,
  LEGACY_SAVED,
  legacyMintFailure,
  legacySaveFailure,
  legacySettleFailure,
  legacySettleStep,
  type LegacyRefusal,
  type LegacySettleStep,
} from "../lib/legacySaveOutcome";
import { useLoad } from "../lib/useLoad";
import type { ErasureReasonItem, ErasureReasonPage, LegacyDiscoveryAccount, LegacyReasonDecision, LegacyReasonDiscovery } from "../types";

// The deleted accounts whose contact-request reasons nobody has decided
// (T07B-3b). One route: the list, and ?account=<id> for one account, so the
// discovery walk survives opening an account and coming back.

const INTRO = "These deleted accounts still have contact-request reasons nobody has reviewed. Choose for each whether Fold keeps the text or blanks it.";
const CHECKING = "Checking…";
const STOPPED = "You stopped checking, so this list may not show every deleted account.";
const NO_ACCOUNTS = "No deleted accounts have reasons left to review.";
const ACCOUNT_EMPTY = "This account has no reasons left to review.";
const PRE_SAVE = "Saving blanks the text you chose right away. It can't be brought back in Fold. Backups made before now still contain it.";
const ADMINS_ONLY = "This page is for church admins only.";

type Walk =
  | { kind: "walking" }
  | { kind: "stopped" }
  | { kind: "failed"; text: string }
  | { kind: "locked"; text: string }
  | { kind: "done" };

// Accounts are kept as their decimal-string users ids: an id past 2^53 must
// never go through a number. An account can continue on a later unit, so units
// merge by id in the order accounts were first seen. The discovery name is
// ignored: the label comes from the id alone.
function mergeUnit(known: readonly string[], unit: readonly LegacyDiscoveryAccount[]): string[] {
  const merged = known.slice();
  const seen = new Set(merged);
  for (const { userId } of unit) {
    if (seen.has(userId)) continue;
    seen.add(userId);
    merged.push(userId);
  }
  return merged;
}

// Walks the list one unit at a time. A unit is a request-atomic page; an empty
// one with a next proves nothing, so the walk ends only at next === null.
// `cursor` moves only after a unit is accepted: a failed, stopped or dropped
// unit is asked for again from the same cursor, so no account is skipped.
function useDiscovery() {
  const [accounts, setAccounts] = useState<string[]>([]);
  const [walk, setWalk] = useState<Walk>({ kind: "walking" });
  const run = useRef(0);
  const cursor = useRef<string | null>(null);
  const requested = useRef(new Set<string>());

  const go = useCallback(async () => {
    const mine = ++run.current;
    setWalk({ kind: "walking" });
    for (;;) {
      const at = cursor.current;
      let unit: LegacyReasonDiscovery;
      try {
        unit = await AdminUsers.legacyReasons(at);
      } catch (e) {
        if (run.current === mine) {
          const failure = legacyReadFailure(e);
          setWalk({ kind: failure.kind === "locked" ? "locked" : "failed", text: failure.text });
        }
        return;
      }
      if (run.current !== mine) return;
      // A cursor already asked for would loop for ever: fail closed, keeping what was found.
      if (unit.next !== null && requested.current.has(unit.next)) {
        setWalk({ kind: "failed", text: ERROR_COPY.reason_review_busy });
        return;
      }
      setAccounts((prev) => mergeUnit(prev, unit.accounts));
      cursor.current = unit.next;
      if (unit.next === null) {
        setWalk({ kind: "done" });
        return;
      }
      requested.current.add(unit.next);
    }
  }, []);

  // An account whose page turned out to have nothing left leaves the list. This
  // says nothing about any other account.
  const forget = useCallback((userId: string) => {
    setAccounts((prev) => (prev.includes(userId) ? prev.filter((id) => id !== userId) : prev));
  }, []);

  const stop = useCallback(() => {
    run.current++; // the unit in flight is dropped; Try again asks for it again
    setWalk({ kind: "stopped" });
  }, []);

  useEffect(() => {
    void go();
    return () => {
      run.current++;
    };
  }, [go]);

  return { accounts, walk, go, stop, forget };
}

export function ReasonReviewLegacyPage() {
  const { user, loading, isAdmin } = useAuth();
  if (loading) return null;
  if (!user) return null;
  // The server alone decides who is an adult; this only keeps the page from
  // opening for someone the server is bound to refuse.
  if (!isAdmin || user.status !== "active" || user.accountType === "teen") {
    return (
      <div className="fold-page-narrow">
        <h1 className="text-2xl font-bold">{LEGACY_REVIEW_TITLE}</h1>
        <p>{ADMINS_ONLY}</p>
      </div>
    );
  }
  return <LegacyReview key={`${user.id}:${user.status}:${user.role}:${user.accountType}:${user.birthDate}`} />;
}

function LegacyReview() {
  const [params] = useSearchParams();
  const account = params.get("account");

  if (account !== null && !isCanonicalAccountRef(account)) {
    return (
      <div className="fold-page-narrow space-y-5">
        <ButtonLink variant="link" to={LEGACY_REVIEW_PATH}>{LEGACY_REVIEW_TITLE}</ButtonLink>
        <h1 className="text-2xl font-bold">{deletedAccountLabel()}</h1>
        <p>{LEGACY_NO_ACCOUNT}</p>
      </div>
    );
  }
  return <LegacyReviewWalk account={account} />;
}

function LegacyReviewWalk({ account }: { account: string | null }) {
  const discovery = useDiscovery();
  if (account === null) return <LegacyList {...discovery} />;
  return <LegacyAccountView key={account} account={account} onEmpty={discovery.forget} />;
}

// One page of a deleted account's undecided reasons, with the admin's choices.
// The save is bounded to the page that was loaded (at most 100): once those are
// decided they leave, and the next reload brings the next ones. Keyed by the
// validated account in LegacyReviewWalk, so nothing chosen, marked or in flight
// for one account can reach another that lists the same request.
//
// Every press of Save mints its own attempt id and sends it with the PUT. The
// id lives in a ref only and is dropped when the page is left. An answer that
// does not say what happened never reads as "not saved": the same id is
// settled (lib/legacySaveOutcome.ts has the table), and only a definitive
// refusal or a settle answer of not_applied lets the next press mint a new one.

const MAX_PAGE_ITEMS = 100;

type Phase =
  | { kind: "idle" }
  // Minting the id, or the PUT is in flight.
  | { kind: "saving" }
  // A settle request is in flight.
  | { kind: "checking" }
  // Settle could not say yet: Check again asks the same id.
  | { kind: "still" }
  // Fold cannot tell whether the earlier save finished: nothing more until a reload succeeds.
  | { kind: "history" }
  // Nothing more can be done from here; `reload` is a restore that a reload lifts.
  | { kind: "locked"; reload: boolean };
type Note = { text: string; alert: boolean };
type Prior = ReadonlyMap<string, { hash: string; choice: ReasonChoice | undefined }>;
type Choices = Readonly<Record<string, ReasonChoice | undefined>>;

const IDLE: Phase = { kind: "idle" };

// A page that is not this account's, is too long or repeats a request is not
// an answer: it fails closed like any unreadable one.
function checkedPage(page: ErasureReasonPage, account: string): ErasureReasonPage {
  const seen = new Set<string>();
  let ok = page.items.length <= MAX_PAGE_ITEMS;
  for (const item of page.items) {
    if (item.partyRef !== account || seen.has(item.requestId)) ok = false;
    seen.add(item.requestId);
  }
  if (!ok) throw new ApiError(502, "unexpected answer to a deleted account's reasons");
  return page;
}

function LegacyAccountView({ account, onEmpty }: { account: string; onEmpty: (userId: string) => void }) {
  const load = useLoad(async () => checkedPage(await AdminUsers.legacyReasonsPage(account, null), account), [account]);
  const [choices, setChoices] = useState<Choices>({});
  const [changed, setChanged] = useState<ReadonlySet<string>>(() => new Set());
  const [phase, setPhase] = useState<Phase>(IDLE);
  const [note, setNote] = useState<Note | null>(null);
  const box = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLDivElement>(null);
  // `presses` counts presses and is spent when the page is left, so a late
  // answer for a dropped id finds no matching press and is ignored. `working`
  // is true from a press until its id is dropped, and `settling` while a settle
  // is in flight: each is read before the screen re-renders, so a double click
  // cannot mint twice or settle twice.
  const mounted = useRef(true);
  const presses = useRef(0);
  const working = useRef(false);
  const settling = useRef(false);
  const attempt = useRef<string | null>(null);
  // The choices the next loaded page may keep, set before a reload and used once.
  const carry = useRef<Prior | null>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      attempt.current = null;
    };
  }, []);
  const isCurrent = (press: number) => mounted.current && presses.current === press;

  const page = load.data;
  const items = page?.items ?? [];

  // A reload after a 409 or a not_applied keeps the choice of an item whose
  // reviewHash is unchanged. Any other item (a different hash, or one that was
  // not on the page before) has no choice and is marked. Items that went are gone.
  useEffect(() => {
    if (!page) return;
    const prior = carry.current;
    carry.current = null;
    if (!prior) return;
    const kept: Record<string, ReasonChoice | undefined> = {};
    const marked = new Set<string>();
    for (const item of page.items) {
      const before = prior.get(item.requestId);
      if (before?.hash === item.reviewHash && before.choice) kept[item.requestId] = before.choice;
      else marked.add(item.requestId);
    }
    setChoices(kept);
    setChanged(marked);
    const first = page.items.findIndex((item) => marked.has(item.requestId));
    (first >= 0 ? list.current?.querySelector<HTMLElement>(`[data-reason-heading="${first}"]`) : box.current)?.focus();
  }, [page]);

  // The answer moves focus to the message: the control that was pressed is off.
  useEffect(() => {
    if (note) box.current?.focus();
  }, [note]);
  useEffect(() => {
    if (load.error) box.current?.focus();
  }, [load.error]);

  // A page with nothing left (decided elsewhere, or just saved) leaves the list.
  const emptied = page !== undefined && items.length === 0;
  useEffect(() => {
    if (emptied) onEmpty(account);
  }, [emptied, account, onEmpty]);

  const onChoose = useCallback((item: ErasureReasonItem, choice: ReasonChoice) => {
    setChoices((c) => ({ ...c, [item.requestId]: choice }));
  }, []);

  function priorChoices(): Prior | null {
    return page ? new Map(page.items.map((item) => [item.requestId, { hash: item.reviewHash, choice: choices[item.requestId] }])) : null;
  }

  // Asks for the page again, failing closed. `prior` undefined keeps the carry
  // already set (a retry of a reload that failed); null keeps nothing.
  function reload(prior?: Prior | null) {
    if (prior !== undefined) carry.current = prior;
    setChoices({});
    setChanged(new Set());
    return load.retry();
  }

  async function unlock() {
    const outcome = await reload(null);
    if (outcome !== "loaded" || !mounted.current) return;
    setPhase(IDLE);
    setNote(null);
    box.current?.focus();
  }

  const unlocksOnReload = phase.kind === "history" || (phase.kind === "locked" && phase.reload);

  // Ends the attempt without a save. The server ended the id before it answered,
  // or never saw it, so the next press mints a fresh one.
  function refuse(failure: LegacyRefusal) {
    attempt.current = null;
    working.current = false;
    if (failure.kind === "locked") {
      if (failure.reloadToUnlock) requireRestoreReload();
      setPhase({ kind: "locked", reload: failure.reloadToUnlock === true });
    } else {
      setPhase(IDLE);
    }
    setNote({ text: failure.text, alert: true });
    if (failure.kind === "changed") void reload(priorChoices());
  }

  function decided(text: string) {
    attempt.current = null;
    working.current = false;
    setPhase(IDLE);
    setNote({ text, alert: false });
  }

  async function settle(id: string, press: number, text: string) {
    if (settling.current) return;
    settling.current = true;
    setPhase({ kind: "checking" });
    setNote({ text, alert: false });
    let step: LegacySettleStep;
    try {
      step = legacySettleStep(await AdminUsers.settleAttempt(id, account, "legacy_reason_save"));
    } catch (e) {
      step = legacySettleFailure(e);
    }
    settling.current = false;
    if (!isCurrent(press)) return;
    switch (step.kind) {
      case "applied":
        decided(step.text);
        void reload(null);
        break;
      case "not_applied":
        decided(step.text);
        void reload(priorChoices());
        break;
      case "history":
        attempt.current = null;
        working.current = false;
        setPhase({ kind: "history" });
        setNote({ text: step.text, alert: false });
        break;
      case "still":
        if (step.restore) requireRestoreReload();
        setPhase({ kind: "still" });
        setNote({ text: step.text, alert: step.restore === true });
        break;
      case "locked":
        refuse({ kind: "locked", text: step.text });
        break;
    }
  }

  function checkAgain() {
    if (attempt.current !== null && phase.kind === "still") void settle(attempt.current, presses.current, LEGACY_CHECKING);
  }

  async function save() {
    if (working.current || !page || phase.kind !== "idle" || items.length === 0 || items.some((item) => choices[item.requestId] === undefined)) return;
    working.current = true;
    const press = ++presses.current;
    attempt.current = null;
    setPhase({ kind: "saving" });
    setNote(null);
    box.current?.focus();
    // Exactly the loaded items, in page order, with the choice made for each.
    const decisions: LegacyReasonDecision[] = items.map((item) => ({
      requestId: item.requestId,
      reviewHash: item.reviewHash,
      choice: choices[item.requestId] as ReasonChoice,
    }));
    let id: string;
    try {
      // One fresh id per press. If the mint fails no PUT has been sent, so nothing can have happened.
      id = (await AdminUsers.mintAttempt(account, "legacy_reason_save")).attemptId;
    } catch (e) {
      if (isCurrent(press)) refuse(legacyMintFailure(e));
      return;
    }
    if (!isCurrent(press)) return;
    attempt.current = id;
    try {
      await AdminUsers.saveLegacyReasons(account, id, decisions);
    } catch (e) {
      if (!isCurrent(press)) return;
      const failure = legacySaveFailure(e);
      // Never a not-saved message before the settle answer.
      if (failure.kind === "uncertain") void settle(id, press, failure.text);
      else refuse(failure);
      return;
    }
    if (!isCurrent(press)) return;
    // The counts the server answers with are checked and deliberately not shown.
    decided(LEGACY_SAVED);
    void reload(null);
  }

  const failure = load.error ? legacyPageFailure(load.error) : null;
  const unchosen = items.filter((item) => choices[item.requestId] === undefined).length;
  const blanking = items.some((item) => choices[item.requestId] === "remove");
  // A refusal that no press can fix shows no reasons: the session, the right
  // to review or the account itself is gone. A restore keeps them for the reload.
  const shown = !(phase.kind === "locked" && !phase.reload) && failure === null;
  const canSave = phase.kind === "idle" && items.length > 0 && unchosen === 0;

  return (
    <div className="fold-page-narrow space-y-5">
      <ButtonLink variant="link" to={LEGACY_REVIEW_PATH}>{LEGACY_REVIEW_TITLE}</ButtonLink>
      <h1 className="text-2xl font-bold">{deletedAccountLabel(account)}</h1>
      <p className="text-text-muted">{LEGACY_SEARCH_LIMIT}</p>
      <div ref={box} tabIndex={-1} className="space-y-3">
        <p role="status" className="empty:hidden">{load.loading ? CHECKING : ""}</p>
        <p role="status" className="empty:hidden">{note && !note.alert ? note.text : ""}</p>
        {note?.alert && <FormError>{note.text}</FormError>}
        {failure?.kind === "gone" && <p>{failure.text}</p>}
        {(failure?.kind === "locked" || failure?.kind === "retry") && <FormError>{failure.text}</FormError>}
        {failure?.kind === "retry" && (
          <Button variant="secondary" onClick={() => void (unlocksOnReload ? unlock() : reload())}>Try again</Button>
        )}
        {phase.kind === "still" && <Button variant="secondary" onClick={checkAgain}>Check again</Button>}
        {unlocksOnReload && !load.loading && failure === null && <Button variant="secondary" onClick={() => void unlock()}>Reload</Button>}
      </div>
      {shown && page && items.length === 0 && <EmptyState title={ACCOUNT_EMPTY} />}
      {shown && items.length > 0 && (
        <>
          <div ref={list}>
            <ReasonReviewList items={items} choices={choices} changed={changed} onChoose={onChoose} disabled={phase.kind !== "idle"} />
          </div>
          {unchosen > 0 && (
            <p className="text-text-muted">{`Choose Keep text or Blank text for ${unchosen === 1 ? "1 more reason" : `${unchosen} more reasons`}.`}</p>
          )}
          {blanking && <p className="text-text-muted">{PRE_SAVE}</p>}
          <Button variant="primary" disabled={!canSave} onClick={() => void save()}>Save</Button>
        </>
      )}
    </div>
  );
}

function LegacyList({ accounts, walk, go, stop }: ReturnType<typeof useDiscovery>) {
  const box = useRef<HTMLDivElement>(null);
  const before = useRef(walk.kind);
  // The Stop or Try again that had focus is gone after a change: keep focus on the message.
  useEffect(() => {
    if (before.current === walk.kind) return;
    before.current = walk.kind;
    if (walk.kind !== "done") box.current?.focus();
  }, [walk.kind]);

  const status = walk.kind === "walking" ? CHECKING : walk.kind === "stopped" ? STOPPED : "";
  // A locked list shows nothing: the answers that built it no longer hold.
  const shown = walk.kind === "locked" ? [] : accounts;
  return (
    <div className="fold-page-narrow space-y-5">
      <h1 className="text-2xl font-bold">{LEGACY_REVIEW_TITLE}</h1>
      <p>{INTRO}</p>
      <p className="text-text-muted">{LEGACY_SEARCH_LIMIT}</p>
      <div ref={box} tabIndex={-1} className="space-y-3">
        <p role="status" className="empty:hidden">{status}</p>
        {(walk.kind === "failed" || walk.kind === "locked") && <FormError>{walk.text}</FormError>}
        {walk.kind === "walking" && <Button variant="secondary" onClick={stop}>Stop</Button>}
        {(walk.kind === "stopped" || walk.kind === "failed") && <Button variant="secondary" onClick={() => void go()}>Try again</Button>}
      </div>
      {shown.length > 0 && (
        <ul aria-label="Deleted accounts" className="fold-list">
          {shown.map((userId) => (
            <li key={userId} className="fold-list-row">
              <Link to={`${LEGACY_REVIEW_PATH}?account=${userId}`} className="inline-flex min-h-11 items-center font-semibold text-primary underline">
                {deletedAccountLabel(userId)}
              </Link>
            </li>
          ))}
        </ul>
      )}
      {walk.kind === "done" && accounts.length === 0 && <EmptyState title={NO_ACCOUNTS} />}
    </div>
  );
}
