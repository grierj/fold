import { describe, expect, it } from "vitest";
import { ApiError } from "../api/core";
import { ERROR_COPY } from "./errors";
import { legacyMintFailure, legacySaveFailure, legacySettleFailure, legacySettleStep } from "./legacySaveOutcome";

// Every row of the save's outcome table (T07B-D4 R4-C08), with no screen. The
// strings are written out here, not imported: they are the wording the admin
// reads, and a change to one is a change to the product.

const coded = (status: number, code: string) => new ApiError(status, "private server text", { code });
const NOT_ALLOWED = "Only an adult admin can review these reasons. Ask another adult admin to review them.";
const CHECKING = "Checking what happened to this save…";
const TOO_OLD = "This save was too old to run, so Fold didn't run it. Fold is checking what happened.";
const STILL = "Still checking. Fold can't tell yet whether your choices were saved. Check again in a moment.";
const DELETION_WORDING = /delete again|delete this account|delete request|Ask another admin to delete/;

describe("legacySaveFailure: a failed PUT", () => {
  it.each([
    ["legacy_save_timed_out", coded(503, "legacy_save_timed_out"), ERROR_COPY.legacy_save_timed_out],
    ["legacy_save_busy", coded(409, "legacy_save_busy"), ERROR_COPY.legacy_save_busy],
    ["admin_account_busy", coded(409, "admin_account_busy"), ERROR_COPY.admin_account_busy],
    ["attempt_busy", coded(503, "attempt_busy"), ERROR_COPY.attempt_busy],
    ["a 400 with a message only", new ApiError(400, "invalid decisions"), "That didn't work. Please try again."],
  ])("%s is definitive: nothing saved, the same choices can be sent with a fresh id", (_name, e, text) => {
    expect(legacySaveFailure(e)).toEqual({ kind: "retry", text });
  });

  it("answers a changed set with the save's own wording, never the deletion's", () => {
    const failure = legacySaveFailure(coded(409, "reason_review_changed"));
    expect(failure).toEqual({ kind: "changed", text: "Some reasons changed since you loaded them. Check the marked ones, then save again." });
    expect(failure.text).not.toMatch(DELETION_WORDING);
    expect(failure.text).not.toBe(ERROR_COPY.reason_review_changed);
  });

  it.each([
    ["a coded 403", coded(403, "reason_review_not_allowed"), NOT_ALLOWED],
    ["a plain 403", new ApiError(403, "forbidden"), ERROR_COPY.not_allowed],
    ["a 401", new ApiError(401, "signed out"), ERROR_COPY.signed_out],
    ["a 404", new ApiError(404, "user not found"), "This account no longer exists."],
  ])("%s locks the page", (_name, e, text) => {
    const failure = legacySaveFailure(e);
    expect(failure).toEqual({ kind: "locked", text });
    expect(failure.text).not.toMatch(DELETION_WORDING);
  });

  it("locks on a restore until a reload succeeds", () => {
    expect(legacySaveFailure(coded(503, "restore_detected"))).toEqual({ kind: "locked", text: ERROR_COPY.restore_detected, reloadToUnlock: true });
  });

  it.each([
    ["legacy_save_outcome_unknown", coded(500, "legacy_save_outcome_unknown"), ERROR_COPY.legacy_save_outcome_unknown],
    ["attempt_closed", coded(409, "attempt_closed"), ERROR_COPY.attempt_closed],
    ["attempt_expired, in the save's words", coded(409, "attempt_expired"), TOO_OLD],
    ["a 2xx that is not the counts (a plain Error)", new Error("unexpected answer to a save"), CHECKING],
    ["a dropped connection", new TypeError("Failed to fetch"), CHECKING],
    ["a gateway page (unreadable body)", new ApiError(502, "<html>", {}, true), CHECKING],
    ["an unreadable 401", new ApiError(401, "<html>", {}, true), CHECKING],
    ["an unreadable 403", new ApiError(403, "<html>", {}, true), CHECKING],
    ["a 500 with no code", new ApiError(500, "could not save"), CHECKING],
    ["an unlisted status", new ApiError(418, "teapot"), CHECKING],
    ["an unlisted code", coded(409, "something_new"), CHECKING],
    ["a thrown string", "boom", CHECKING],
  ])("%s is uncertain: the same id is settled, and nothing says it was not saved", (_name, e, text) => {
    const failure = legacySaveFailure(e);
    expect(failure).toEqual({ kind: "uncertain", text });
    expect(failure.text).not.toMatch(/weren't saved|not saved|nothing was saved/i);
  });
});

describe("legacyMintFailure: minting the attempt id", () => {
  it.each([
    ["a 401", new ApiError(401, "signed out"), { kind: "locked", text: ERROR_COPY.signed_out }],
    ["a plain 403", new ApiError(403, "forbidden"), { kind: "locked", text: ERROR_COPY.not_allowed }],
    ["a coded 403", coded(403, "reason_review_not_allowed"), { kind: "locked", text: NOT_ALLOWED }],
    ["a restore", coded(503, "restore_detected"), { kind: "locked", text: ERROR_COPY.restore_detected, reloadToUnlock: true }],
    ["a busy server", coded(503, "attempt_busy"), { kind: "retry", text: ERROR_COPY.attempt_busy }],
    ["a malformed mint (a 502 from the client)", new ApiError(502, "unexpected answer to a mint"), { kind: "retry", text: "That didn't work. Please try again in a minute." }],
    ["an id the mint refuses to carry (a plain Error)", new Error("not a usable account id"), { kind: "retry", text: "That didn't work." }],
    ["a dropped connection", new TypeError("Failed to fetch"), { kind: "retry", text: "That didn't work." }],
    ["a gateway page", new ApiError(502, "<html>", {}, true), { kind: "retry", text: "That didn't work." }],
  ])("%s never says to settle: no PUT was sent", (_name, e, expected) => {
    expect(legacyMintFailure(e)).toEqual(expected);
  });
});

describe("legacySettleStep: a settle answer", () => {
  it.each([
    ["applied", { result: "applied" }, { kind: "applied", text: "Saved." }],
    ["applied, whatever else it carries", { result: "applied", subjectDeleted: true, extra: 1 }, { kind: "applied", text: "Saved." }],
    ["not_applied", { result: "not_applied" }, { kind: "not_applied", text: "These choices weren't saved. Reload to see what's left." }],
    ["not_applied with subjectDeleted, which a save never gets and is ignored", { result: "not_applied", subjectDeleted: true }, { kind: "not_applied", text: "These choices weren't saved. Reload to see what's left." }],
    ["history_unavailable", { result: "history_unavailable" }, { kind: "history", text: "Fold can't tell whether your earlier save finished. Reload to see what's left." }],
    ["checking", { result: "checking" }, { kind: "still", text: STILL }],
  ])("%s", (_name, body, expected) => {
    expect(legacySettleStep(body)).toEqual(expected);
  });

  it.each([
    ["an unknown result", { result: "done" }],
    ["a result of the wrong type", { result: 1 }],
    ["a result in the wrong case", { result: "APPLIED" }],
    ["a result with whitespace", { result: "applied " }],
    ["no result", {}],
    ["an array", ["applied"]],
    ["null", null],
    ["undefined", undefined],
    ["a string", "applied"],
    ["a number", 200],
  ])("%s is never a result: still checking", (_name, body) => {
    expect(legacySettleStep(body)).toEqual({ kind: "still", text: STILL });
  });
});

describe("legacySettleFailure: a settle request that failed", () => {
  it.each([
    ["a dropped connection", new TypeError("Failed to fetch")],
    ["a plain Error", new Error("boom")],
    ["a 502 from the client", new ApiError(502, "unexpected answer")],
    ["a gateway page", new ApiError(503, "<html>", {}, true)],
    ["an unreadable 401", new ApiError(401, "<html>", {}, true)],
    ["a 500", new ApiError(500, "oops")],
    ["a 404", new ApiError(404, "not found")],
    ["a 400", new ApiError(400, "bad")],
    ["a busy answer", coded(503, "attempt_busy")],
  ])("%s leaves the attempt open: still checking", (_name, e) => {
    expect(legacySettleFailure(e)).toEqual({ kind: "still", text: STILL });
  });

  it.each([
    ["a 401", new ApiError(401, "signed out"), ERROR_COPY.signed_out],
    ["a coded 403", coded(403, "reason_review_not_allowed"), NOT_ALLOWED],
    ["a plain 403", new ApiError(403, "forbidden"), ERROR_COPY.not_allowed],
  ])("%s ends the attempt and locks", (_name, e, text) => {
    expect(legacySettleFailure(e)).toEqual({ kind: "locked", text });
  });

  it("keeps the attempt open after a restore, with its own wording", () => {
    expect(legacySettleFailure(coded(503, "restore_detected"))).toEqual({ kind: "still", text: ERROR_COPY.restore_detected, restore: true });
  });
});
