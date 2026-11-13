import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { AdminUsers, isCanonicalAccountRef } from "../api/adminUsers";
import { useAuth } from "../auth";
import { deletedAccountLabel } from "../components/ReasonReviewList";
import { Button, ButtonLink } from "../components/ui/Button";
import { EmptyState } from "../components/ui/EmptyState";
import { FormError } from "../components/ui/Field";
import { LEGACY_REVIEW_PATH, LEGACY_REVIEW_TITLE, LEGACY_SEARCH_LIMIT, legacyReadFailure } from "../lib/legacyReview";
import { ERROR_COPY } from "../lib/errors";
import type { LegacyDiscoveryAccount, LegacyReasonDiscovery } from "../types";

// The deleted accounts whose contact-request reasons nobody has decided
// (T07B-3b). One route: the list, and ?account=<id> for one account, so the
// discovery walk survives opening an account and coming back.

const INTRO = "These deleted accounts still have contact-request reasons nobody has reviewed. Choose for each whether Fold keeps the text or blanks it.";
const CHECKING = "Checking…";
const STOPPED = "You stopped checking, so this list may not show every deleted account.";
const NO_ACCOUNTS = "No deleted accounts have reasons left to review.";
const NO_ACCOUNT = "This account no longer exists.";
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

  return { accounts, walk, go, stop };
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
        <p>{NO_ACCOUNT}</p>
      </div>
    );
  }
  return <LegacyReviewWalk account={account} />;
}

function LegacyReviewWalk({ account }: { account: string | null }) {
  const discovery = useDiscovery();
  if (account === null) return <LegacyList {...discovery} />;
  return <LegacyAccountView key={account} account={account} />;
}

// One account's reasons. Keyed by the validated account, so nothing chosen for
// one account can carry over to another that lists the same request.
function LegacyAccountView({ account }: { account: string }) {
  return (
    <div className="fold-page-narrow space-y-5">
      <ButtonLink variant="link" to={LEGACY_REVIEW_PATH}>{LEGACY_REVIEW_TITLE}</ButtonLink>
      <h1 className="text-2xl font-bold">{deletedAccountLabel(account)}</h1>
      <p className="text-text-muted">{LEGACY_SEARCH_LIMIT}</p>
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
