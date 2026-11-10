# Email: sending, choices, suppressions and direct account mail

Read this before touching anything that sends email, reads or writes an email choice, or touches
suppressions. The guards named here are parse-only; each test file states its own limits.

## How mail reaches a provider

- Application mail reaches a provider only through `sendTracked` (`mail_send.go`), which
  classifies a send as sent, suppressed, not configured, permanent or transient; each ledger
  (notices, announcements, direct mail, the composer test send) decides what that means for
  itself.
- `mail.Sender` is `providers.EmailSender`: one interface (`SendEmail`, `Capabilities`) for every
  adapter, the suppression wrapper and `NoSender`. A nil `App.Mailer` counts as `NoSender`
  everywhere.
- Suppression reasons rank in one place, `mail.StrongestReason` (hard bounce, then manual, then
  complaint; an unknown reason counts as manual).
- Suppression SQL lives only in `mail.OpenSuppressionReasons`, the admin helpers and the webhook
  writer, and matches by Go-computed hash (`TestSuppressionSQLStaysInItsHelpers`,
  `TestSuppressionSQLGuardCatchesBypasses`). Matching
  assumes a UTF8 database with a deterministic default collation (`TestSuppressionMatchingBasis`).
- Notices are delivered by the `notify.deliver` job that `notify.Send` queues, never by their
  callers (`TestNoticesAreDeliveredBySendNotByCallers`).

## Optional email choices

- One effective-choice reader, `readEmailOff` (`email_optouts.go`): the account's latest email
  choice or the actual destination address's retained opt-out can turn a category off. Settings
  expose only effective choices.
- `email_address_optouts` is written only by `exportEmailOptOutsIn` during erasure with a held
  address, and deleted only by the current holder's affirmative turn-on under the user lock. Keep
  the original choice time; never reconstruct a legacy tombstone address.
- Version 2 links bind the destination address for turn-on; legacy and moved-address links still
  allow turn-off, and erased-account links stay invalid. A session older than an address change
  cannot turn email on.
- Every optional or broadcast email checks the effective choice at its send boundary:
  - Batch rows recheck `message_recipients.address` inside each row's claim transaction, after
    its claim and before rendering and dispatch; optional notifications check inside their claim
    too.
  - At READ COMMITTED, a choice committed before the choice statement starts is honored. A later
    change may not stop that one in-flight send; every later row and retry rechecks.
- Required mail bypasses choices; provider suppressions remain separate.
- Enforced by `TestEmailChoiceSQLStaysInOneHelper`, `TestEmailChoiceGuardCatchesBypasses`,
  `TestReadEmailOffCombinesAccountAndDestination`, `TestEmailSettingsHideChoiceSource`,
  `TestReusedAddressKeepsErasedChoicesUntilHolderTurnsOn`,
  `TestOptionalMailChecksTheEffectiveChoice`, `TestOptionalMailGuardCatchesBypasses`,
  `TestBatchRechecksTheChoiceAtEachRow`, `TestTestSendHonorsTheEffectiveChoice`,
  `TestTestSendInFlightWindow`.

## Direct account mail (M7.5, R6-4)

- Reconstructible account mail uses `mailKinds` on the existing job runner, with eight attempts.
  Enqueue with the domain change and audit in one transaction; standalone hints and guardian
  resends must durably enqueue before answering. Keep public answers uniform.
- Payloads contain only `userId` (reset and existing-account hints), `requestId` plus a fixed
  `case` (received, approved, exists, conversion_approved, rejected), `invitationId`,
  `accountTokenId` (email verification), `guardianLinkId`, or `guestSignupId`. Never queue an
  address, note, rendered message or raw capability.
- Prepare from current stored facts in a transaction, mint or rotate the capability there,
  commit, then send through the suppression-aware mailer without holding database locks. Match
  redemption's lock order: contact gate then user/link/token for account flows; invitation and
  guest mail each lock only their own row and read authority facts without earlier-order locks.
  Claims recheck the submitted hash, unused state and wall-clock expiry after locking.
- Delivery is at least once at the provider boundary. A retry may replace an already accepted
  email's link; only the newest capability works. Do not promise exactly-once email delivery.
- Direct account mail finishes quietly (logged) for a suppressed address, a permanent refusal or
  no email service (`NoSender`, warned at startup); only transient errors retry.
- Delivery jobs use the 60-second `deliverSendTimeout`. Failure logs use `jobs.CleanError` and
  never add a recipient address or capability.

## Synchronous exceptions

Only these send outside a job:

- `App.sendEmailChangeNotice` (`api_profile.go`) needs the old address and cancellation
  capability, which cannot be reconstructed after verification.
- `App.sendGuestConfirmationEmail` (`api_guest.go`) shares its manage capability with the
  browser, so delayed rotation would invalidate the displayed link.
- Both use the 20-second `syncMailTimeout`, leaving 40 seconds of the server's 60-second write
  budget for transaction work and the response.
- The composer's test send (`api_message_preview.go`) is synchronous by design. It checks the
  author's session and effective email choice under the user row lock, records the attempt,
  commits, then calls the provider with a 30-second context and no user lock held. A change
  committed after that commit may not stop that one test send; the next request rechecks.

## The mail guard (`TestMailGoesThroughJobs`)

It parses the production tree; only `internal/mail` (which owns the sender implementations) is
exempt from the send checks. Sends are allowed only in the delivery files (`mailerSendFiles` in
`guards_mail_test.go`) and as immediate calls in the named synchronous methods, and calls to those
methods must be immediate too (never in a closure, `go`, `defer` or a method value). `api_dev.go`
may unwrap and read the outbox, never send. **When you add a sender type, add it to the guard's
list in the same change.** Its reach and limits are documented above `TestMailGoesThroughJobs`.
