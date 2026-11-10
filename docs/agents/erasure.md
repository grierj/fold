# Account erasure, deletion outcomes and the restore barrier

Read this before touching account deletion, the tombstone sweep, `contact_requests` text,
household erasure, the restore fence or admin operation attempts. Rationale and residue
are in `docs/plan/M7.4-ERASURE-INVENTORY.md` (section numbers below point there).

## One erasure pipeline

- The admin deletion and the tombstone sweep both run `a.eraseAccountIn(tx, subject, opts)`
  (`account_erasure.go`). Its doc comment states the one step order and why: sets, PERS check,
  household locks, credentials, hand-off, the users row, avatar, requests then notifications,
  the soft delete, the three trigger-licensed scrubs, households, promotion, dependent rows,
  audit details, throttles last. Add an erasure step there, never in a caller.
- `erasureOptions` carries only what differs: `Actor` (nil for the system sweep), `IP`, and
  `Live` (the admin path's locked row, successors, host picks and reason plan).
- Deleting an account scrubs the user row to a tombstone and deletes its tokens, calendar link,
  devices and owned throttle keys.
- Legacy tombstones: `erasure.tombstone_sweep` starts a daily keyset page chain;
  `erasure.tombstone_page` continues it, at most 200 subjects plus one continuation per
  transaction; `erasure.tombstone` scrubs one exact soft-deleted `deleted-<id>@deleted.invalid`
  account under the contact gate and user lock. The numeric `user.erasure_swept` version
  committed with the scrub decides whether work remains, never a retained job. Keep the original
  church date in page payloads and daily keys. No tombstone address is reconstructed; address-only
  residue is unknown per subject (inventory 12.1 to 12.3).

## Contact-request text

`contact_requests` text is immutable except for the trigger-licensed blanks:

- Erasing an `occurrence_add` subject clears their `add_childcare_ages_note` through
  `eraseContactRequestTextIn` (`account_erasure.go`), which the two request triggers allow only
  for a soft-deleted subject (`00004_ages_note_erasure_scrub.sql`).
- `reason` is blanked only by `eraseContactRequestReasonsIn` (`account_erasure_reasons.go`),
  which `eraseAccountIn` calls right after it and which has work only on the admin path. The
  admin's keep or remove decisions (`planReasonReviewIn`, the `set-v1` envelope) write one signed
  audit row each, then the removed reasons are blanked in batches inside the deletion's
  transaction. `00007` licenses that blank in the two request triggers for non-contact,
  non-pending rows with a soft-deleted party; contact and pending rows keep the baseline, which
  still lets `reason` change on `contact` rows and pending add rows, and the source guard stops
  new writers. A legacy `{"items": [...]}` body is answered `reason_review_outdated` without
  reading its entries.
- The two creation functions stay in `api_contact.go`: the contact-text guard pins them.
- Enforced by `TestContactRequestTextChangesStayInTheirHelpers`,
  `TestContactRequestTextGuardCatchesBypasses`, `TestContactRequestTextGuardReadsTheRealTree`
  (`guards_contact_text_test.go`, whose `ctgLimits` states exactly what the guard pins and what it
  cannot see) and `TestAgesNoteMigrationFileHasNoTriggerBypass`. In short: the scrubs are called
  once each, inside `eraseAccountIn` after the soft delete, and `eraseAccountIn` is called
  exactly twice (`apiAdminDeleteUser` inside `a.fencedContactMutation`, `eraseTombstoneOnce`
  inside `a.contactMutationCtx`). No production code disables or replaces the request triggers or
  sets `session_replication_role`; only `internal/testdb` may, and nothing else imports it.

## Households

`eraseHouseholdRecordsIn` (`account_erasure_households.go`) runs under household rows taken
`FOR UPDATE NOWAIT`.

- It deletes the account's seat, anonymizes the person row(s) that are the same individual
  (`PERS`, read unscoped: `converted_user_id = U`, or the person of the approved
  `household_conversion` request with `approved_user_id = U`) and soft-deletes them.
- It **never writes `converted_user_id`** or sets a `user_id` on a staged person's registrations
  (that would record a completion that never happened).
- `validatePersIn` aborts the whole erasure before any write when the identity links contradict
  each other; never widen `PERS` by household, kind, birth date or surname.
- It revokes pending and confirmed guardian links of `PERS`, of the account as teen and as
  guardian (a guardian's links to surviving children or teens are revoked; those people and their
  household are not anonymized), one `GuardianLinkRevoked` audit row each, and leaves
  `check_note`, `teen_consents` and their audit mirrors untouched (O-4).
- A household's name and address become `Removed household` and `''` only when
  `householdErasedOwnerIn` proves it sole-subject (no seat of any user, every person in `PERS`,
  soft-deleted included); any other household is another family's record, retained and counted
  in the `erasure` report the delete answers (`householdsKept`, `householdsScrubbed`,
  `peopleAnonymized`, `guardianLinksRevoked`; counts only).
- Households are locked only by `lockErasureHouseholdsIn` (`NOWAIT`, ascending, before the first
  write). `TestErasureHouseholdLockCensus` pins the names of the `lockHousehold` callers and fails
  on a new one; their classifications and lock orders are inventory 9.6.1 and are kept by review.
  The erasure is not proven deadlock-free (inventory 9.5).

## Deletion outcomes (`apiAdminDeleteUser`)

- The whole erasure runs in `a.fencedContactMutation(ctx, ...)` (a `contactMutationCtx` with the
  restore ticket) with one `adminDeleteBudget` (40 s) context made from `context.Background()`,
  never the request's, so a disconnect cannot abort a commit.
- Classify its error only with `classifyContactErr` (`contact_lifecycle.go`), **commit phase
  first**:
  - Any COMMIT error (`*contactCommitError`) is unknown, whatever it wraps: 500
    `delete_outcome_unknown` ("couldn't confirm", never "nothing was deleted"), never retried.
  - A spent budget, the commit reserve or SQLSTATE 57014: 503 `delete_timed_out`.
  - Retry **only SQLSTATE 55P03 with `rollbackConfirmed(err)` true** (the ROLLBACK was
    acknowledged, so the server released the gate and the user lock), at most three attempts
    within `busyRetryWindow` (2 s; `deleteRetry` on the shared `busyRetry` policy in
    `busy_retry.go`), each rebuilt from the target's id alone.
  - A 55P03 inside a `*contactRollbackError` stops with no further BEGIN; it and exhaustion both
    answer 409 `delete_household_busy`. Nothing was deleted because COMMIT was never sent, which
    is not the same as the locks being released.
  - 40P01 is never retried.
- The three codes are declared values (`errDeleteOutcomeUnknown`, `errDeleteTimedOut`,
  `errDeleteHouseholdBusy`), real copy on a 5xx on purpose, answered with `fail(c, ...)`, each
  with an `ERROR_COPY` line.
- Return is not release: after a client deadline the server may hold the gate until D +
  `contactServerCap` + the idle timeout, and no test equates the two.
- `internal/testdb/pgproxy.go` is the test-only fault proxy for these cases; production code may
  not import it.
- Enforced by `TestContactMutationCtx*`, `TestAdminDeleteCommitPhaseAnswersUnknown`,
  `TestAdminDeleteHouseholdBusyThenCommitUnknown`,
  `TestAdminDeleteHouseholdBusyRollbackNotAcknowledged`,
  `TestHouseholdRetryOnlyRepeatsAConfirmedBusy`, the pgproxy tests and
  `TestWebErrorCopyCoversEveryCode`.

## Restore barrier

The barrier and the recovery level ship together. The binary's `recoveryPolicyLevel`
(`restore_fence.go`) equals `deploy/recovery-policy` (`TestRecoveryPolicyFileMatchesConstant`)
and `fold version --json` reports it. **Never publish a release at `recoveryPolicy >= 1` before
R4-6c-R's current-release restore tooling and operator procedure are verified** (review only;
see `deploy.md`).

- **Boot step.** `bootRestoreFence` runs in `serve` after migrations and before anything serves.
  One transaction under the migrations advisory lock creates and advances the `settings` row
  `restore_fence`; only an acknowledged COMMIT sets the in-memory floor, and any failure exits
  non-zero. A restart is the restart boundary: ids and the floor belong to one process.
  (`TestRestoreFenceBoot`; the call's position in `serve` is checked by review.)
- **Commit-aware fence.** A fenced transaction (`apiAdminDeleteUser`, the settle route) runs only
  through `a.fencedContactMutation(ctx, change, fn)`, the one place the ticket protocol is
  written. Its callback takes `a.takeRestoreTicket(tx)` first (the one-slot turn under the
  contact gate, then the counter) and then runs `fn`, which returns the actor seal the wrapper
  hands to `contactMutationCtx` (the only place a `contactChange.Seal` is set). It defers
  `a.abandonRestoreTicket(&tk)` before the call and runs `a.acknowledgeRestoreTicket(&tk, err)`
  as the next statement. The floor is raised only when the call returned nil (an acknowledged
  COMMIT), and only then is the turn released.
- A ticket at or below the floor, or a missing row, is 503 `restore_detected`, and
  `requestRestart` makes `serve` exit 1 for the supervisor.
- Only the boot step and the ticket write the row (`TestRestoreFenceHasTwoWriters`);
  `setSettingIn` calls `refuseRestoreFenceKey`.
- **Detection limit.** Only the in-memory floor detects anything, and only when a restore loses a
  commit this process saw acknowledged. A restore of a dump taken after the last acknowledged
  fenced commit, one that loses only an unknown commit, and one made while the process was
  stopped are not detected; `TestRestoreFenceUndetectedCases` pins that the first two never answer
  "not applied" for a deletion that ran.
- Enforced by `TestFencedTransactionsTakeTheTicketFirst`,
  `TestFencedTransactionsGuardCatchesBypasses`, `TestSettingsWritersRefuseTheFenceKey`,
  `TestRestoreFenceDelayedAcknowledgment`, `TestRestoreFenceNoFalseDetection`,
  `TestRestoreFenceSameLifetime`, `TestVersionJSONReportsRecoveryPolicy`.

## Attempt ids (`admin_operation_attempts.go`)

- `POST /api/v1/admin/operation-attempts` mints an id under a per-process key; only that process
  mints or verifies it, and it runs at most once, in that process, if first submitted within 10
  minutes.
- The ledger is memory only (its mutex never waits: `TestLedgerMutexHoldsNoWait`) and is checked
  before an id's age.
- The receipt row (`admin_operation_attempts`) is inserted right after the ticket in the
  deletion's own transaction; a daily `admin_attempts.sweep` job deletes receipts older than 24
  hours.
- `POST .../settle` answers `applied`, `not_applied` (`subjectDeleted: true` only when the
  account is already gone), `history_unavailable` or `checking`. `not_applied` comes only from the
  ledger in memory. `attempt_expired` means only that this submission ran nothing; its copy never
  says "nothing was deleted". After a restart or 60 minutes an id settles `history_unavailable`.
- Enforced by `TestAttemptIDCapability`, `TestAttemptLedgerRules`, `TestAttemptLifecycle`,
  `TestDefinitiveResponsesEndTheAttempt`, `TestOutcomeClassification`, `TestSettleAuthorization`,
  `TestSettleUnknownCommit`, `TestRestoreFenceFencedIdAcrossARestart`.

## Actor seal

`contactChange.Seal` runs `currentAdminIn` (`FOR SHARE NOWAIT` on the actor's own row, decided by
`sessionCurrent`) as the last locking statement before COMMIT, with or without an attempt id. A
busy seal retries at most three times and answers 409 `admin_account_busy`; settle makes at most
three sealed tries in two seconds and then answers `checking`. (`TestActorSealIsLast`,
`TestCurrentAdminInFacts`, `TestActorSealBusy`, `TestActorSealVsConversion`.)

## Delete panel (web)

`DeleteUserPanel` mints an id per press and keeps it in memory. A lost, unreadable or ambiguous
answer settles the same id with Delete off and shows no "nothing was deleted" copy first;
definitive refusals discard the id; a `restore_detected` or `history_unavailable` answer keeps
Delete off until the users list reloads successfully (`lib/deleteReload.ts`).

The test-only attempt clock is described in `testing.md`.
