import { ApiError } from "../api/core";
import { ERROR_COPY, errorCode, errorText } from "./errors";
import { LEGACY_NOT_ALLOWED, LEGACY_NO_ACCOUNT } from "./legacyReview";

// What each answer to saving a deleted account's reasons means (T07B-D4
// R4-C08, T-302). Pure: the page reads these, so every row of the table can be
// tested without a screen. The deletion flow's lines say "delete", so the save
// keeps its own wording and never reads ERROR_COPY.reason_review_changed,
// reason_review_not_allowed or attempt_expired.
//
// The rule underneath: only an answer that proves the server ended the attempt
// without saving is definitive. Anything that does not say what happened is
// "uncertain": the same attempt id is settled, and "not saved" is never
// claimed before settle answers not_applied.

export const LEGACY_SAVED = "Saved.";
const CHANGED = "Some reasons changed since you loaded them. Check the marked ones, then save again.";
const TOO_OLD = "This save was too old to run, so Fold didn't run it. Fold is checking what happened.";
export const LEGACY_CHECKING = "Checking what happened to this save…";
const STILL = "Still checking. Fold can't tell yet whether your choices were saved. Check again in a moment.";
const NOT_APPLIED = "These choices weren't saved. Reload to see what's left.";
const HISTORY = "Fold can't tell whether your earlier save finished. Reload to see what's left.";
const DIDNT_WORK = "That didn't work.";

// What a failed save means.
// - uncertain: the server may have saved; settle the same attempt id.
// - retry: nothing was saved, the choices stay; the next press mints a fresh id.
// - changed: the reasons differ from what was shown; reload and keep what still matches.
// - locked: nothing more can be done from this page. `reloadToUnlock` is a
//   restore the server noticed: a reload that starts afterwards lifts it.
export type LegacyFailure =
  | { kind: "uncertain"; text: string }
  | { kind: "retry"; text: string }
  | { kind: "changed"; text: string }
  | { kind: "locked"; text: string; reloadToUnlock?: true };

// A failure that ended the attempt without a save.
export type LegacyRefusal = Exclude<LegacyFailure, { kind: "uncertain" }>;

const UNCERTAIN: LegacyFailure = { kind: "uncertain", text: LEGACY_CHECKING };
const BUSY_CODES = ["legacy_save_timed_out", "legacy_save_busy", "admin_account_busy", "attempt_busy"] as const;

// Answers that end the attempt whichever call they come on: a restore was
// seen, the session ended, or this admin may not review.
function ending(e: ApiError): LegacyRefusal | null {
  const code = errorCode(e);
  if (code === "restore_detected") return { kind: "locked", text: ERROR_COPY.restore_detected, reloadToUnlock: true };
  if (e.status === 401) return { kind: "locked", text: ERROR_COPY.signed_out };
  if (e.status === 403) return { kind: "locked", text: code === "reason_review_not_allowed" ? LEGACY_NOT_ALLOWED : ERROR_COPY.not_allowed };
  return null;
}

// The PUT. Not an ApiError (a dropped connection, a 2xx that is not the
// counts) or a failure status whose body is not JSON (a gateway page) is not
// the server's refusal: it may have saved.
export function legacySaveFailure(e: unknown): LegacyFailure {
  if (!(e instanceof ApiError) || e.unreadable) return UNCERTAIN;
  const code = errorCode(e);
  if (code === "attempt_expired") return { kind: "uncertain", text: TOO_OLD };
  if (code === "attempt_closed" || code === "legacy_save_outcome_unknown") return { kind: "uncertain", text: ERROR_COPY[code] };
  if (code === "reason_review_changed") return { kind: "changed", text: CHANGED };
  const end = ending(e);
  if (end) return end;
  if (e.status === 404) return { kind: "locked", text: LEGACY_NO_ACCOUNT };
  for (const busy of BUSY_CODES) if (code === busy) return { kind: "retry", text: ERROR_COPY[busy] };
  // The server's own validation answers; only a client bug sends a bad body.
  if (e.status === 400) return { kind: "retry", text: errorText(e, DIDNT_WORK) };
  // A 5xx with no code and any status or code not named above: the server may have saved.
  return UNCERTAIN;
}

// Minting the attempt id. It reads no database and the PUT has not been sent,
// so whatever fails here, nothing was saved. A failure that is not an
// ApiError includes an account id the mint cannot carry (the id travels as a
// JSON number and mintAttempt refuses one that would round): the same line,
// and nothing is sent.
export function legacyMintFailure(e: unknown): LegacyRefusal {
  if (e instanceof ApiError && !e.unreadable) {
    const end = ending(e);
    if (end) return end;
    return { kind: "retry", text: errorText(e, DIDNT_WORK) };
  }
  return { kind: "retry", text: DIDNT_WORK };
}

// What settling the attempt id says. `text` is what to show.
export type LegacySettleStep =
  | { kind: "applied"; text: string }
  | { kind: "not_applied"; text: string }
  | { kind: "history"; text: string }
  // Not decided yet: Check again asks the same id. `restore` is a restore the server noticed.
  | { kind: "still"; text: string; restore?: true }
  | { kind: "locked"; text: string };

const STILL_STEP: LegacySettleStep = { kind: "still", text: STILL };

// A settle answer's body. The wrapper does not validate it, so only the three
// exact results count; anything else (a missing, unknown or non-string result,
// "checking") is "still checking". subjectDeleted is ignored: it is never sent
// for a save.
export function legacySettleStep(res: unknown): LegacySettleStep {
  const result = typeof res === "object" && res !== null ? (res as { result?: unknown }).result : undefined;
  if (result === "applied") return { kind: "applied", text: LEGACY_SAVED };
  if (result === "not_applied") return { kind: "not_applied", text: NOT_APPLIED };
  if (result === "history_unavailable") return { kind: "history", text: HISTORY };
  return STILL_STEP;
}

// A settle request that failed. Only a sign-out or a refusal ends the attempt;
// every other failure leaves it open to Check again.
export function legacySettleFailure(e: unknown): LegacySettleStep {
  if (!(e instanceof ApiError) || e.unreadable) return STILL_STEP;
  const end = ending(e);
  if (!end) return STILL_STEP;
  // Checking again after the restart answers history_unavailable.
  return end.kind === "locked" && end.reloadToUnlock ? { kind: "still", text: end.text, restore: true } : { kind: "locked", text: end.text };
}
