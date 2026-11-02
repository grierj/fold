# Audit 2026-11 — Reliability and performance: R6 outbox, R11 query shapes

Implementation briefs for a coding agent. These are briefs, not code. Read
`AGENTS.md` first; where this file and `docs/plan/PLAN.md` disagree, PLAN
wins and the disagreement gets raised, not silently resolved.

Shared rules for both items:

- Single Go binary with the SPA embedded; Postgres only; schema changes are
  new goose files in `migrations/` (next free number at implementation time;
  last seen `00062_reminder_push.sql`), never edits to old files. The GORM
  model change and `models.All()` update (`models/models.go:1030`) land in
  the same PR when a model changes.
- Background work goes through `internal/jobs`: register a `jobs.Kind`
  (`internal/jobs/jobs.go:32`), queue with `jobs.Enqueue`
  (`internal/jobs/jobs.go:55`) and a `unique_key` when the work must happen
  once. Handlers return their error so the worker retries, and every
  handler must be safe to run twice. Never start a goroutine ticker.
- Bell notices are `notify.Send` rows written in the same transaction as
  the change and its `audit.Write` row. The registry in
  `internal/notify/kinds.go` never looks anything up, so the caller decides
  what the recipient may see.
- A message's own status, counts, settling, reaper and Try again read its
  email rows only: any new query over a message's `message_recipients`
  rows filters `channel = 'email'` (the shape at
  `messages_send.go:271` and `messages_send.go:389`).
- Permission checks stay pure functions in `internal/policy` with table
  tests. Handlers load facts and call policy; no inline role checks.
- Tests sit next to the code they test. Verification for every slice:
  `go build ./...`, `go vet ./...`, `make lint-go`,
  `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`, `make test`,
  `cd web && npm test`, and `make e2e` where a user flow changes.
- Docs say "recommended", never the other word (the docs-wording guard
  test fails the build). No Python anywhere: any script below is shell
  or Go.
- Rejected audit items stay out: R7 (freeze web push, strike SMS from the
  goals) and R8 (open self-signup) were rejected. Nothing here spends
  less on push, changes the push switch's meaning, or opens signup. Push
  behaviour specified in DEC-121 to DEC-129 and DEC-134 is preserved
  exactly where R6 touches its queueing.

---

## R6 — Notification delivery becomes a transactional outbox

- ID: R6 (audit `docs/AUDIT-2026-11-01.md:117`)
- Title: Notification delivery becomes a transactional outbox
- Priority: P0, before a first church goes live (audit build order
  step 2, `docs/AUDIT-2026-11-01.md:261`)
- Effort: M (audit estimate)
- Status: Approved 2026-11-01 (owner, via the audit). No GitHub issue
  known at brief time.

### 1. Goal

When Fold writes a bell notice, the duty to email and push it is written
in the same transaction, so a crash, deploy or mail outage can delay a
notice but never silently drop it. Delivery retries with backoff, a
delivery that gives up tells the admins once in the bell and waits on
Admin, System like any dead job, and a password-reset email gets the
same treatment: request one and the link either goes out or an admin
hears that it could not. Domain callers stop carrying lists of
notification IDs out of their transactions to a post-commit send call
they must remember to make.

### 2. Current state (evidence)

The break, precisely:

- `notify.Send` (`internal/notify/notify.go:71`) runs inside the caller's
  transaction, writes one `notifications` row per recipient and returns
  the row IDs "so the caller can deliver email for AllowEmail kinds after
  the transaction commits" (`internal/notify/notify.go:66`). A dedupe hit
  returns no ID (`internal/notify/notify.go:69`). It writes no durable
  delivery intent of any kind.
- Delivery is `deliverNotificationEmails`
  (`api_notifications.go:821`): first `queuePushDeliveries`
  (`api_notifications.go:822`, body at `push_deliver.go:135`), then the
  emails, synchronously in the requesting worker. Its own comment states
  the contract: "Delivery failures are logged, never returned: the
  notification row is already committed" (`api_notifications.go:813`).
  When the recipients read fails it records nothing, "so a later delivery
  pass can still send them" (`api_notifications.go:859`) — and no such
  pass exists anywhere. The test for that path,
  `TestDeliveryRecipientsReadFailureLeavesRowsPending`
  (`api_notifications_test.go:634`), re-calls the same function by hand;
  production never does.
- Recounted at brief time, there are 46 production call sites of
  `deliverNotificationEmails` (the audit counted 51 at `3ec934e`;
  `docs/AUDIT-2026-11-01.md:118`). Each collects IDs out of a transaction
  by hand, for example group creation (`api_groups.go:154` declares
  `emailed`, `api_groups.go:167` appends inside the transaction,
  `api_groups.go:177` sends after it) and the daily date extender
  (`jobs_daily.go:36`, sends at `jobs_daily.go:39` after
  `events.ExtendLocked` commits per event — a job retry adds no new
  dates, so those IDs are never produced again). Names vary by file
  (`emailed`, `mailed`, `notices` at `main.go:147`). A site that forgets,
  and any crash between commit and the call, loses email and push while
  the bell row looks fine. Nine more calls are test-only.
- Push queueing has the same hole one layer down:
  `queuePushDeliveries` runs its own post-commit transaction to enqueue
  one `push.deliver` per device (`push_deliver.go:154`,
  `enqueuePushDelivery` at `push_deliver.go:55`). Once enqueued those
  jobs are durable and retry; the enqueue itself is what a crash drops,
  and "a failure is logged, never returned" (`push_deliver.go:133`).

What already exists to build on:

- `notification_deliveries` is one row per (notification, channel) with
  a unique index on that pair (`migrations/00011_m1_2_email.sql:60`,
  `models/models.go:628`). Its status vocabulary already includes
  `queued` and `deferred` (`migrations/00011_m1_2_email.sql:65`,
  `models/models.go:619`), and it carries an unused `scheduled_for`
  column (`models/models.go:635`). The channel check already allows
  `email`, `sms` and `push`
  (`migrations/00058_push_subscriptions.sql:25`). Today the row is only
  an outcome record: email writes it terminal-only with
  `ON CONFLICT DO NOTHING` (`api_notifications.go:1002`), push writes one
  aggregate row at first outcome with upgrade-to-sent semantics
  (`push_deliver.go:552`), and provider webhooks find rows by
  `provider_message_id` (`api_email_webhooks.go:105`). The model has no
  `updated_at`.
- `internal/jobs` is a complete claim/lease/backoff/dead engine. The
  claim is one `UPDATE ... FOR UPDATE SKIP LOCKED` that also takes back
  a running job whose lease ran out (`internal/jobs/jobs.go:218`, lease
  default 15 minutes at `internal/jobs/jobs.go:46`). Backoff is 30
  seconds doubling to an hour, jittered (`internal/jobs/jobs.go:129`).
  A job that uses its last attempt becomes dead
  (`internal/jobs/jobs.go:286`), and `finish` writes the outcome only
  while the worker still holds the lease, reporting via `RowsAffected`
  (`internal/jobs/jobs.go:297`). Dead currently means one log line
  (`internal/jobs/jobs.go:288`) plus the Admin, System list
  (`api_admin_jobs.go:58`), with audited retry and dismiss
  (`api_admin_jobs.go:127`). Nobody is told. Enqueue dedupes on
  `unique_key` in any status (`internal/jobs/jobs.go:52`, the
  `ON CONFLICT (unique_key) DO NOTHING` insert at
  `internal/jobs/jobs.go:115`), and finished jobs are pruned after 30
  days (`jobs_daily.go:23`). `jobs.LastAttempt` lets a handler record
  its own terminal state before returning the error
  (`internal/jobs/jobs.go:248`; the pattern at `messages_send.go:546`
  and `push_deliver.go:501`), and `jobs.CleanError` strips URLs, tokens
  and email addresses from stored error text (`internal/jobs/jobs.go:321`).
- Announcements already solved durable email for `message_recipients`:
  rows are snapshotted `queued` in the send transaction
  (`messages_send.go:224`) and batch jobs are enqueued in that same
  transaction (`messages_send.go:268`). R6 brings stored notifications
  up to that bar. It must not disturb the rule that a message's status
  and counts read its email rows only (`messages_send.go:389`).
- Kinds decide channels in the registry: `AllowEmail` and `AllowPush`
  with a per-kind `PushRender` (`internal/notify/kinds.go:120`,
  `internal/notify/kinds.go:134`). Every push kind in production also
  allows email. `register` sets `Required` for safety and waiting kinds
  (`internal/notify/kinds.go:191`), and required kinds ignore the
  optional-email opt-out at send (`api_notifications.go:940`).
- Email outcome classification already exists: `mail.IsPermanent`,
  `mail.IsSuppressed`, the pre-send `mail.Blocked` suppression check
  and `mail.NotConfigured` for a church with no sender set up
  (`api_notifications.go:968`, `internal/mail/mail.go:78`,
  `internal/mail/mail.go:399`). Announcements treat not-configured as
  an immediate terminal failure with its own words
  (`messages_send.go:544`); notification email today records a generic
  `failed` (`api_notifications.go:983`).
- Password reset is the worst single case. The token transaction
  commits (`api_auth.go:433`, token minted at `api_auth.go:447`), then
  the mail rides an untracked goroutine (`runInBackground` at
  `api_auth.go:461`, body at `api_auth.go:478`) whose failure is one log
  line (`api_auth.go:466`). A crash after the commit, or any send
  error, loses the only copy of the link; the raw token exists nowhere
  else, by design — only its hash is stored (`tokens.go:44`). The
  uniform public answer must stay uniform either way (DEC-52,
  `api_auth.go:354`), and the response must not wait on mail (#93,
  `api_auth.go:424`). The same goroutine pattern serves guest mail
  (`api_guest.go:193`) and account-request mail
  (`api_account_requests.go:246`).
- Direct mail outside `notify` is seven more `a.Mailer.Send` files:
  invitations (`api_invitations.go:262`), account requests (five
  senders, `api_account_requests.go:798` onward), email change
  (`api_profile.go:264`), guest verification and confirmation
  (`api_guest.go:732`), guardian consent (`api_guardian_email.go:172`),
  household conversion (`api_household_conversion.go:434`), and the
  composer test send (`api_message_preview.go:229`). Most carry a raw
  single-use token minted beside a stored hash, the same constraint as
  reset.
- Dead jobs alert nobody, and the System page never shows a job payload
  (`api_admin_jobs.go:18`). There is no admin view of stuck or failed
  notification deliveries at all.
- Batching: no production kind sets `BatchWindow` (registry search:
  only `internal/notify/notify_test.go:273` uses one), so `merge`
  (`internal/notify/notify.go:156`) is inert today. The design below
  keeps its semantics defined anyway, because a future kind will use it.

### 3. Key decision (made here, do not relitigate in the PR)

Extend `notification_deliveries`; do not add a second outbox table, and
do not make the deliveries table its own queue. The split is:

- `notification_deliveries` is the intent and outcome ledger, one row
  per (notification, channel) as today. The email row is created
  `queued` inside the `notify.Send` transaction. Its final states stay
  the vocabulary the table already has: `sent`, `suppressed`, `failed`
  (permanent), `deferred` (attempts exhausted, an admin may retry),
  plus webhook-written `delivered` and `bounced`.
- `internal/jobs` is the retry engine, as it is for everything else:
  one `notify.deliver` job per notification, enqueued in the same
  transaction with unique key `notify.deliver:<notificationId>`. Lease,
  backoff, dead state and the System page come from the runner; the
  delivery row carries no attempt counter of its own (the job row is
  the single source for attempts).
- Push keeps its shape exactly, minus the loss window: the deliver job
  does the device fan-out (`queuePushDeliveries`'s work) as its first
  step, creating the same per-device `push.deliver` jobs with their
  existing unique keys (`push_deliver.go:56`). The aggregate push
  delivery row is still written by `recordPushIn` at first outcome;
  pre-creating a `queued` push row is rejected because its
  conflict clause only upgrades toward `sent` (`push_deliver.go:560`)
  and the hourly-limit and church-switch checks already run inside
  `deliverPush`, not at queue time.
- A daily-to-hourly reconcile sweep is the backstop, modelled on the
  message reaper (`messages_send.go:733`) and the push reaper
  (`messages_push.go:405`): it re-enqueues a deliver job for any email
  delivery still `queued` with no live job, and it backfills intent
  for recent notifications that predate the outbox.

Why not a new table: the unique (notification, channel) index, the
provider-id lookup webhooks depend on, the push aggregate row, and
every test that reads delivery rows already point at this one. A
second table would need the same columns and a join in the webhook
path for no gain. Why not claim deliveries directly with
`FOR UPDATE SKIP LOCKED`: that duplicates the runner's tested claim,
lease and dead machinery (`internal/jobs/jobs_test.go`,
`TestExpiredLeases`, `TestTwoWorkersNeverRunAJobTwice`).

Import direction: `internal/notify` will import `internal/jobs` to call
`jobs.Enqueue` inside `Send`. That is cycle-free — `internal/jobs`
imports only `fold/models` (`internal/jobs/jobs.go:23`) — and it is
what makes "the caller cannot forget" structural instead of a new
helper each domain file must remember, which is the bug being fixed.

### 4. Scope

In:

- Delivery intent (email row plus deliver job) written inside
  `notify.Send`, same transaction as the bell row and the caller's
  audit row, for every kind with `AllowEmail` or `AllowPush`.
- A `notify.deliver` job handler that re-reads every fact at run time
  (recipient status, opt-outs, suppressions, church push switch) and
  reproduces today's send decisions, with transient failures retried
  by the runner and permanent ones recorded terminal.
- A reconcile sweep, an admin delivery summary on Admin, System, and
  one bell alert to admins when any job dies.
- Password-reset mail moved onto a job with handler-minted tokens.
- Removal of every post-commit delivery call site and of the raw-token
  goroutines named in slice 4.

Out:

- Announcement and reminder sending (`messages_send.go`,
  `reminders.go`, `reminders_push.go`). Already durable through jobs.
  Touch nothing there except where a shared helper moves.
- SMS delivery rows. The channel value exists; no sender does. When
  SMS is built it writes its intent the same way; do not build it here.
- The 15-minute "email if not seen" escalation (`notify.Seen`,
  `internal/notify/notify.go:349`). This brief makes the immediate
  send durable; delaying it is a separate product call. The unused
  `scheduled_for` column stays unused.
- The composer test send (`api_message_preview.go:229`). A person is
  waiting on it and its result is the response; it stays synchronous.
  Say so in a comment there so the exemption is deliberate.
- Guest, guardian, invitation, email-change and account-request mail
  beyond slice 4's table. Each moves only if its job handler can mint
  or rebuild what it sends from stored IDs alone (slice 4); anything
  that would need a raw token in a job payload waits for its own brief.

### 5. Slices (in order, each shippable on its own)

Slice R6-1 — schema plus worker, dormant.

- New goose migration (next free number; last seen `00062`):
  `notification_deliveries` gains `updated_at timestamptz NOT NULL
  DEFAULT now()` and a partial index for the reconcile scan and the
  admin counts, `idx_notification_deliveries_queued ON
  notification_deliveries (created_at) WHERE status = 'queued'`. No
  status or channel constraint change is needed (see section 2).
  Matching field on `models.NotificationDelivery`
  (`models/models.go:628`); the drift test in
  `migrations/migrations_test.go` is the gate.
- New file `notify_deliver.go` (root package): kind name constant
  shared with `internal/notify` (see slice 2), payload
  `notifyDeliverJob{NotificationID uint}`, the `jobs.Kind` list
  `func (a *App) notifyKinds() []jobs.Kind` registered into
  `backgroundKinds` beside the push kinds (`messages_send.go:85`),
  and the handler `deliverOneNotification` extracted from
  `deliverNotificationEmails` / `deliverNotificationEmail`
  (`api_notifications.go:821`, `api_notifications.go:935`) with the
  semantics in section 6. Label: "Sending a notice by email and to
  devices". Max attempts 8, matching the message batch rationale
  (`messages_send.go:38` rides out roughly an hour of provider outage;
  with the runner's capped backoff eight attempts span about two
  hours — estimate, not a measured promise).
- The handler is dormant in this slice: nothing enqueues it yet, and
  the post-commit path still delivers. Prove it by running it directly
  from tests (section 7).

Slice R6-2 — intent in `Send`, then flip the callers off.

- In `internal/notify/notify.go`, `Send` (both the plain create at
  `internal/notify/notify.go:140` and the merge branch at
  `internal/notify/notify.go:100`, which returns merged IDs through
  the same list) writes, in the same `tx`, for each
  row it creates: one email delivery row `queued` when the kind
  allows email (`INSERT ... ON CONFLICT (notification_id, channel)
  DO NOTHING`), and one `jobs.Enqueue` when the kind allows email or
  push, unique key `notify.deliver:<id>`, run at now. Signature,
  returned IDs, dedupe and actor-skip semantics do not change; the
  comment at `internal/notify/notify.go:66` is rewritten to say the
  IDs remain for callers that need to know what was written, and
  delivery no longer needs them. A merged row enqueues nothing new —
  the unique key collides with the first send's job in any status, so
  a still-queued delivery sends the row's latest stored text (the
  handler reads `email_title`/`email_body` at run time) and an
  already-sent one sends nothing, matching the dedupe rule at
  `internal/notify/notify.go:69`. Add the kind-name constant to
  `internal/notify` (for example `notify.DeliverKind`) and a small
  writer helper there; `internal/notify` stays free of any policy or
  rendering change.
- In the same PR, `deliverNotificationEmails` becomes a no-op that
  only documents why it remains, so no deploy can double-send (single
  binary: there is no mixed-version window to bridge). Then delete
  the call sites in file groups, one commit each group, dropping the
  now-dead `emailed`/`mailed` plumbing where the signature lets it go
  cleanly: groups and roster (`api_groups.go`, `api_departure.go`,
  `api_admin.go`, `api_admin_household.go`); events and registrations
  (`api_events.go`, `api_registrations.go`,
  `api_series_registrations.go`, `api_occurrence_mutations.go`,
  `api_event_access.go`); people and safeguarding
  (`api_account_requests.go`, `api_profile.go`, `api_guest.go`,
  `api_teen_consent.go`, `api_teen_guardians.go`,
  `api_guardian_email.go`, `api_household_conversion.go`,
  `api_contact.go`, `contact_lifecycle.go`, `age_rollover.go`); rooms
  (`api_room_requests.go`, `api_room_bookings.go`,
  `api_room_series.go`); and the job-internal sites (`jobs_daily.go:39`,
  `main.go:147`, `push_keys.go` via `main.go`). Writers that return
  IDs only for delivery (for example `notifyGroupPending`,
  `api_notifications.go:418`) keep returning them this slice;
  simplifying those signatures is a later tidy, not this flip.
- Last in the slice, delete `deliverNotificationEmails`,
  `deliverNotificationEmail` and the old `recordDelivery`
  (`api_notifications.go:992`) — the handler from slice 1 now owns the
  extracted code. A guard test lands with this slice (section 7) so
  the post-commit pattern cannot come back.

Slice R6-3 — reconcile sweep, dead-job alert, admin visibility.

- Reconcile: job kind `notify.reconcile`, label "Checking for notices
  that still need sending", registered with the hourly kinds
  (`hourlyKinds` at `reminders.go:83`, queued by `queueHourly` at
  `jobs_daily.go:147` under key `notify.reconcile:<UTC hour>`). Its
  handler, in `notify_deliver.go`: (a) finds email deliveries still
  `queued`, older than 10 minutes, with no live `notify.deliver` job
  (the `NOT EXISTS` shape at `messages_send.go:736`, matching on
  payload notification id), in bounded batches of 500 ordered by
  `created_at`, and re-enqueues each with the same unique key — a key
  still present in any status adds nothing, which correctly leaves
  dead ones to the alert path below; (b) backfills intent for
  notifications created in the last 24 hours whose kind allows email
  or push and which have no email delivery row, by calling the same
  writer helper slice 2 put in `internal/notify` (the kind check must
  go through `notify.Lookup`, never a copied SQL list of kinds).
  Returning its error retries the sweep like any hourly kind. Rows
  older than the backfill window stay bell-only forever; say so in
  the code comment and in the PR description.
- Dead-job alert: `internal/jobs` learns one optional hook, a field on
  `Runner` next to `Lease` (`internal/jobs/jobs.go:142`), for example
  `OnDead func(jobID uint, kind string, attempts int, errText string)`.
  `finish` calls it only in the dead branch
  (`internal/jobs/jobs.go:286`) and only when the outcome update
  actually landed (`RowsAffected` at `internal/jobs/jobs.go:298` is
  the guard — a worker that lost its lease alerts nobody). The hook
  must not return an error into `finish`; failures inside it are the
  callback's to log. `internal/jobs` still imports nothing new.
- New notify kind, registered in `internal/notify/kinds.go` with its
  key constant in the block at `internal/notify/kinds.go:26`:
  `SystemJobDead = "system.job_dead"`, category `CatAdmin`,
  `Waiting: true` (so it counts until an admin acts, and `Required`
  follows from `register` at `internal/notify/kinds.go:191`),
  `AllowEmail: false`, `AllowPush: false`. Bell-only is deliberate:
  the commonest cause of a dead delivery is the mail or push service
  being down, and an alert that itself needs mail would chain dead
  jobs forever; the bell needs only the database. Render from `Vars`:
  title "A background task stopped: " plus `v.Change` (the kind's
  plain label from `jobKinds()`, falling back to the kind name, as
  `api_admin_jobs.go:73` does), body "It could not finish after
  `<Count>` tries. Open Admin, then System to try it again or set it
  aside." Never render the job's payload or raw error — the bell text
  follows the registry privacy rule, and `jobs.CleanError` text stays
  on the System page where it is today (`api_admin_jobs.go:31`).
  The wiring lives in `startJobs` (`jobs_daily.go:89`): set `OnDead`
  to a closure that writes, in its own fresh transaction,
  `notify.Send(tx, ...)` to `adminIDs(tx)` with subject type `job`,
  subject id the job id, link `/admin?tab=system`, `Change` the
  label. No dedupe key: the hook fires once per death transition, and
  a job an admin retries that dies again earns a second alert.
- Resolution: `decideDeadJob` (`api_admin_jobs.go:88`) calls
  `notify.Resolve` for `system.job_dead` and the job's id in the same
  transaction as its audit row, resolution `retried` or `dismissed`
  matching the action, so every admin's copy closes together — the
  `audit.JobRetried` / `audit.JobDismissed` constants already exist
  (`internal/audit/audit.go:271`).
- Admin visibility: extend `apiAdminDeadJobs`
  (`api_admin_jobs.go:58`) response with one summary object,
  `deliveries: {queued, deferred, failed, oldestQueuedAt}`, counted
  over `channel = 'email'` notification deliveries (`failed` over the
  last 7 days by `updated_at`). Add the type to `web/src/types.ts`
  beside `AdminJob` and a `tsContracts` pair; show one calm line on
  the System tab (`web/src/components/admin/SystemTab.tsx`, reached
  from `web/src/pages/Admin.tsx:1658`) in the existing component and
  token style: counts only, never a recipient or an error string, and
  nothing when all are zero except the queued count. No new route, no
  per-delivery retry button in this slice: retrying the dead
  `notify.deliver` job from the existing list is the retry path, and
  the handler accepts a `deferred` row (section 6).

Slice R6-4 — password reset, then the direct-mail table.

- New job kind `account.password_reset_mail`, label "Sending a
  password reset link", payload `{"userId": N}` and nothing else — no
  address, no token, ever (payloads sit in the jobs table; the System
  page's no-payload rule at `api_admin_jobs.go:18` is a second lock,
  not the only one). Registered beside the notify kinds.
- `apiRequestPasswordReset` (`api_auth.go:397`) keeps its throttles,
  its uniform answer and its timing shape. The transaction at
  `api_auth.go:433` no longer mints a token; inside `lockContacts` and
  the locked recheck (`api_auth.go:440`) it writes the same
  `audit.PasswordResetRequested` row and enqueues the job. No unique
  key: each accepted request is distinct work, and a per-user key
  would block later requests until job pruning (the any-status rule
  at `internal/jobs/jobs.go:52`).
- The handler mints at send time: lock the user row, return nil (done,
  no mail) unless the account is still active, then
  `createAccountToken` (`tokens.go:50`) in that transaction and commit
  before sending. Minting revokes every unused reset token for the
  user (`tokens.go:47`), so a retry simply mints again — the only
  live link is the newest sent one, and its 24 hours start at the
  actual send. Recipient is the account's current address read under
  the lock, so a reset asked for an address the account has since
  left reaches nobody wrong. Send with the existing text
  (`sendPasswordResetEmail`, `api_auth.go:495`) under a bounded
  context (keep `resetMailTimeout`, `api_auth.go:474`). Outcome rules
  match section 6: suppression handling as for required notification
  mail; `mail.NotConfigured` and permanent refusals end the job done
  (log the `jobs.CleanError` text); a transient error is returned for
  retry; last-attempt exhaustion makes the job dead, which is what
  raises the slice 3 alert. A retried attempt mints a fresh token, so
  a mail that was accepted just before a crash leaves its link revoked
  by the retry's newer mail — the safe direction, and the same
  at-least-once gap as section 6 step 8.
- Delete `runInBackground` (`api_auth.go:478`) once its three callers
  are gone (`api_auth.go:461`, `api_guest.go:193`,
  `api_account_requests.go:246`); untracked goroutines for mail are
  the anti-pattern this brief removes.
- The remaining direct senders move in this slice only where the
  handler can rebuild or re-mint from IDs. One job kind each, payload
  IDs only, same labels and outcome rules:

  | Sender today | Job payload | Handler rebuilds or re-mints |
  |---|---|---|
  | Account approved (`api_account_requests.go:826`) | request id | Mints a fresh account-setup token for the approved user (`tokens.go:50`), then sends |
  | Account rejected, received, already-have-account, conversion approved (`api_account_requests.go:798`) | request id (plus a case word) | Rebuilds text from the request row; the rejection note must come from the stored decision note, never the payload |
  | Invitation (`api_invitations.go:262`), household conversion invite (`api_household_conversion.go:434`) | invitation id | Rotates the invitation's token hash and expiry in a transaction, then sends the new raw value |
  | Email change verify and notice (`api_profile.go:264`) | user id plus a case word | Mints fresh verify/cancel tokens (`api_profile.go:233` shows today's pair) |
  | Guardian consent (`api_guardian_email.go:172`) | guardian link id | Re-creates the consent token via `createGuardianEmailToken` (`api_guardian_email.go:60`), rechecking the link is still live |
  | Guest verification and confirmation (`api_guest.go:732`) | guest signup / registration id | Follows the guest rules in `AGENTS.md` (C5): never load a guest row as an actor; re-mint only if the stored flow permits it, else leave for a follow-up brief and say which |

  Anything in the table that turns out to need request-scoped data
  not durably stored is descoped in the PR description with its name,
  not forced. The composer test send stays synchronous (section 4).

### 6. Handler semantics and data shapes

`deliverOneNotification(ctx, id)` (slices 1–2), in order. One run must
finish well inside the 15-minute job lease, so sends run under a
60-second context timeout.

1. Load the notification by id. Gone (pruned or rolled back): return
   nil. The delivery row is deleted by its foreign key cascade, so
   nothing is left dangling.
2. Look up the kind. Unknown or neither channel allowed: return nil.
3. Push fan-out first, exactly as `deliverNotificationEmails` does
   push beside email today (`api_notifications.go:822`): the
   single-notification form of `queuePushDeliveries`
   (`push_deliver.go:135`) — same device query, same per-device
   unique keys, so a retry after a partial fan-out enqueues nothing
   twice. A fan-out write error is returned (retry), never swallowed:
   the log-only swallow at `push_deliver.go:165` is the behaviour
   being fixed. Everything past enqueue — quiet hours, hourly limit,
   consent, staleness — stays inside `deliverPush` untouched
   (`push_deliver.go:337`).
4. Email, when the kind allows it: claim the ledger row with one
   conditional update — `UPDATE notification_deliveries SET
   updated_at = now() WHERE notification_id = ? AND channel = 'email'
   AND status IN ('queued', 'deferred')`. Zero rows means another run
   finished it (`sent`, `suppressed`, `failed`, or a webhook state):
   return nil without sending. This, the job's own lease, and the
   job's unique key are the idempotency for (notification, channel).
5. Re-read the recipient facts at send time with the same single
   recipient shape the batch uses today (account, email opt-out
   scopes, open suppressions, site name and church address;
   `api_notifications.go:848`): optional-kind opt-out and an inactive
   recipient record `suppressed` with today's reason strings
   (`api_notifications.go:940`) and end the run successfully. A fact
   read that errors returns the error with nothing recorded — the
   `queued` row is now the durable "pending" the old comment hoped
   for (`api_notifications.go:859`), and the retry or the reconcile
   sweep is the later pass.
6. Build the message exactly as today (stored email title/body, the
   link, the optional-mail footer and one-click unsubscribe;
   `api_notifications.go:949`). Check `mail.Blocked` with the
   suppressions read in step 5, then send through
   `providers.EmailSender` when available for the provider id.
7. Outcomes write the ledger with conditional updates
   (`status IN ('queued','deferred')`) and stamp `updated_at`:
   sent → `sent`, `sent_at`, provider id, reason cleared;
   `mail.IsSuppressed` → `suppressed` with the stripped reason;
   `mail.IsPermanent` or `mail.NotConfigured` (or a nil mailer,
   today's guard at `api_notifications.go:823`) → `failed` with
   `jobs.CleanError` text, and return nil so the job ends done — a
   church with no mail set up must not grow a dead job and an admin
   alert per notice; transient → update the reason with
   `jobs.CleanError` and return the error; when `jobs.LastAttempt`
   says this was the final try, first set the row `deferred`, then
   return the error so the job dies and slice 3 alerts once. An admin
   retry of that dead job re-enters at step 4, which accepts
   `deferred`.
8. Delivery is at least once at the provider boundary — a crash
   between the provider accepting mail and step 7 recording it sends
   that one email again on retry, the same accepted gap announcement
   batches document (`messages_send.go:433`). State it in the
   handler's comment; do not invent a two-phase send.

Job payloads (camelCase JSON, IDs only):

```json
{"notificationId": 4812}   // notify.deliver
{"userId": 73}             // account.password_reset_mail
{"requestId": 19, "case": "approved"}   // direct-mail kinds, slice 4
```

Delivery state machine (email channel): `queued` at `Send` →
`sent` | `suppressed` | `failed`, or → `deferred` on last-attempt
transient failure → (admin retries the dead job) → `queued`-equivalent
re-entry → a terminal state. Webhooks keep moving `sent` rows to
`delivered` and `bounced` as today. No new status values, no new
columns beyond `updated_at`.

Admin JSON addition (slice 3), beside the existing dead-jobs list:

```json
"deliveries": {"queued": 3, "deferred": 1, "failed": 2,
               "oldestQueuedAt": "2026-11-02T09:14:00Z"}
```

New kind registry entry (slice 3): key `system.job_dead`, category
`admin`, waiting, bell-only, no actions, rendered only from
`Vars.Change` (a plain-words job label) and `Vars.Count` (attempts).
It must pass the existing registry-wide tests (no contact details,
lock-screen safety — trivially, it never pushes).

### 7. Tests

Tests sit beside their source. The dev-outbox sender is the observable
mailbox (`testhelpers_test.go:97` wires `mail.NewDevOutbox` through the
suppression wrapper); a small failing `mail.Sender` test double
(configurable error, call counter) goes beside the delivery tests.
Job-driven flows run the real runner over the real tables
(`internal/jobs/jobs_test.go` shows the pattern), draining with
`RunOnce` instead of sleeping.

New, in `notify_deliver_test.go` (root) and
`internal/notify/notify_test.go`:

- `TestSendWritesDeliveryIntentInTransaction`: a committed `Send`
  leaves one `queued` email delivery and one `notify.deliver` job with
  the unique key; a rolled-back transaction leaves neither; a kind
  with neither channel writes neither; a dedupe second `Send` adds
  neither.
- `TestNotifyDeliverSendsOnce`: run the job twice and run two copies
  of the handler in parallel; exactly one mail in the outbox, one
  `sent` row with provider id, and one push job per device.
- `TestNotifyDeliverFailureModes`, the failure-injection table:
  transient mailer error → delivery stays `queued` with a cleaned
  reason (no address, no token in the stored text), job retries with
  the runner's backoff; permanent error → `failed`, job done, no
  alert; `mail.NotConfigured` → `failed`, job done, no alert;
  suppressed address → `suppressed`; optional opt-out toggled between
  `Send` and the run → `suppressed` (facts are re-read); transient
  error on every attempt through a 1-attempt test kind → delivery
  `deferred`, job `dead`, exactly one `system.job_dead` bell row per
  admin, and an admin retry of the dead job with a healed mailer →
  `sent`, alert resolved. This is the mailer error → retry → dead →
  alert-once chain, end to end.
- `TestNotifyDeliverPushFanOutSurvivesEmailFailure`: email fails
  permanently, push jobs still exist once per device; rerun enqueues
  no duplicates.
- `TestNotifyReconcileRequeuesOrphans`: a hand-made `queued` delivery
  with no job gets one; a dead job's `deferred` row is left alone; a
  pre-outbox notification inside the window is backfilled, one
  outside is not.
- `TestDeadJobAlertFiresOnceAndResolves` in `api_admin_jobs_test.go`
  style: kill a generic job to dead, see one waiting alert per admin
  naming no payload and no error text, retry it from the API, alert
  resolved, audit rows present. Plus a lease-loss case in
  `internal/jobs/jobs_test.go`: a worker whose finish lands zero rows
  never calls the hook.
- `TestPasswordResetMailIsDurable` in `api_auth_test.go`: request a
  reset with a failing mailer — response is still the uniform message,
  no token row exists yet, one job is queued; heal the mailer, drain
  jobs, the mailed link resets the password exactly once; a second
  request before draining leaves only the second job's link live;
  disabling the account between request and run sends nothing;
  unknown and guest addresses enqueue nothing and answer identically
  (keep `TestPasswordResetUniformAndSingleUse` and
  `TestPasswordResetSkipsGuests` green).
- Guard: extend `guards_test.go` with a source scan in the existing
  style — outside `notify_deliver.go`, the push delivery file and an
  allow-list naming the composer test send, no production file calls
  a post-commit notification delivery function or `a.Mailer.Send` for
  notification mail. Name it so a failure says what to do instead
  (write the notice with `notify.Send`; delivery is automatic).

Existing tests that must keep passing, re-pointed where they call the
deleted function directly (9 test call sites): delivery outcome
assertions in `api_notifications_test.go` (including
`TestDeliveryRecipientsReadFailureLeavesRowsPending`, rewritten so the
second pass is a job drain plus reconcile, its original intent),
`api_push_test.go`, `push_deliver_test.go` (fan-out cases now enter
through `Send` plus a drained deliver job), `api_event_access_test.go`,
`email_unsubscribe_test.go`, `api_email_webhooks_test.go` (provider-id
row updates unchanged), and the registry suites in
`internal/notify/notify_test.go` and `internal/notify/kinds_test.go`
with the new kind included. Tests that measure timing keep calling
`realPasswordCost(t)` first, per `AGENTS.md`.

### 8. Acceptance checklist

- [ ] Rolling back any domain transaction leaves no bell row, no
      delivery row and no job; committing leaves all three or the
      job alone (push-only shape), with no caller-side send call.
- [ ] Killing the process between a commit and delivery loses
      nothing: on restart the email and the per-device pushes go out.
- [ ] A mailer outage produces retries with backoff and exactly one
      delivered copy when it heals; a permanent refusal ends `failed`
      with no retry and no admin alert; exhausted attempts end
      `deferred`, the job dead, and one bell alert per admin.
- [ ] Opt-outs, suppressions, account status and the church push
      switch are honoured as they stand when the job runs, not when
      the notice was written.
- [ ] Push text, quiet hours, hourly limit and lock-screen rules are
      byte-identical in behaviour to before (push suites green).
- [ ] No raw token, address, endpoint or push key appears in any job
      payload, delivery reason, bell text or log line added here.
- [ ] A reset requested during a mail outage still arrives after it,
      its link works once, and the request endpoint's answers and
      timing stay uniform.
- [ ] Admin, System shows the delivery counts; retrying a dead
      `notify.deliver` job from it sends the notice and resolves the
      alert.
- [ ] `grep` finds no production `deliverNotificationEmails` call and
      no `runInBackground`; the new guard test enforces both.
- [ ] `make test`, `cd web && npm test`, `make e2e`, and the quick
      checks (`go vet`, `make lint-go`, `tsc --noEmit`, `knip`) pass.

### 9. Risks, rollback, security and privacy

- Job volume grows by one row per emailed or pushed notice. At church
  scale that is small next to the existing per-device push jobs, and
  finished jobs prune after 30 days; R11 slice c (claim index) is the
  paired fix if claim cost ever shows. Batching regresses from two
  reads per batch (`api_notifications.go:827`) to the same reads per
  notice; accepted deliberately for per-notice retry granularity —
  the same reasoning push already uses for one job per device
  (`push_deliver.go:21`). If a fan-out ever proves hot, the reconcile
  index and the per-notice reads are the measured places to start.
- The flip slice touches 46 call sites. Keep it mechanical: signature
  first (no-op body), deletions after, behaviour only in slice 1's
  handler. Any slice can be reverted alone in reverse order; reverting
  slice 2 restores the post-commit calls and leaves `queued` delivery
  rows inert (nothing reads them without the kind registered — an
  older build leaves unknown-kind jobs waiting, they neither run nor
  die for a week by the runner's own rule,
  `internal/jobs/jobs.go:206`). The migration's new column is additive
  and its down migration drops it and the index.
- Alert storms: one root cause (mail down) can kill many deliver jobs,
  each writing one waiting bell row per admin. That is loud but true,
  and each names a retryable job. Do not add alert throttling in this
  brief; if the bell floods in practice, batching the alert kind is a
  registry change with its own decision (open question 2).
- Token safety in slice 4 is the hard rule: payloads carry IDs, the
  handler mints under a lock after rechecking state, and the raw
  value goes only into the mail text, exactly as today. The access
  log redaction for token links is untouched.
- A `deferred` delivery retried long after the event it announces can
  mail stale news. The bell row may already be read or expired; mail
  still sends, matching today's behaviour where a slow post-commit
  call sends whatever the row says. Push already has its own
  staleness rule (`push_deliver.go:283`); email gains none here (open
  question 3).

### 10. Dependencies

- R11 slice c (running-lease index) is independent but recommended in
  the same wave: R6 increases job volume and claim traffic.
- Sibling briefs add mail of their own (people import invitations in
  `R2-R3-R4-people.md` slice R2-4 already defers to this outbox if it
  has landed). Land R6 slices 1–2 before any new mail-sending feature
  merges, so new code is born on `notify.Send` plus jobs.
- Migration numbering is shared with the sibling briefs (all cite
  last seen `00062`): take the next free number at merge time, expect
  renumbering on rebase-free master merges.

### 11. Open questions for Grier (each with a default that unblocks work)

1. Backfill depth for notices written before the outbox: how far back
   should reconcile mail what the old path may have dropped?
   Recommended default: 24 hours. Older lost mail is stale news; the
   bell still holds every row.
2. Should many simultaneous dead jobs collapse into one admin alert?
   Recommended default: no, one waiting alert per dead job, matching
   the System list one-for-one. Revisit only if a real outage floods
   the bell.
3. Should email skip a notice the person already read in the bell
   (the push staleness rule, by email)? Recommended default: no —
   email today sends regardless, and changing who gets mail is a
   product call beyond reliability. The queued-row design makes it a
   one-line handler check later.
4. Does `account.password_reset_mail` with no sender configured end
   quietly (section 6 rule), or should reset specifically stay loud
   because setup depends on it? Recommended default: quiet and
   consistent; the deployment brief's mail gate is the place that
   refuses to call a mail-less install ready.

---

## R11 — Fix the query-shape ceilings plus pool cap

- ID: R11 (audit `docs/AUDIT-2026-11-01.md:171`)
- Title: Fix the three query-shape ceilings plus pool cap
- Priority: P1 (audit section order), sequenced with the reliability
  floor in build-order step 6 (`docs/AUDIT-2026-11-01.md:267`);
  slice c is recommended beside R6 (see Dependencies)
- Effort: M (audit estimate)
- Status: Approved 2026-11-01 (owner, via the audit)

**Estimates warning, read first.** Every scale number in the audit's
R11 and repeated below is arithmetic from query shapes. No `EXPLAIN`
was run, no large database was seeded, nothing was executed (the audit
says so at `docs/AUDIT-2026-11-01.md:175` and
`docs/AUDIT-2026-11-01.md:271`). Numbers such as "about 2,000 queries
for one 500-person reminder" are estimates until slice R11-0 measures
them. Each slice below keeps behaviour identical even where its
saving turns out small; if the gate shows a slice buys nothing, say
so in the PR and stop rather than shipping the change on faith.

### 1. Goal

Five known spots grow worse than the data does: a daily job that
re-walks every series from its first date, a reminder check that pays
several queries per person, a job claim whose lease branch scans, a
date listing that reads a whole window to show a page, and a database
pool nobody capped. Fix each shape while holding behaviour — same
dates, same visibility answers, same sends — and prove the saving
with a seeded database, `EXPLAIN` output, and query-count tests. The
five fixes are separate briefs (slices a–e); they share only the
measurement gate in slice 0 and can land in any order after it.

### 2. Slice R11-0 — the measurement gate (lands first, changes no behaviour)

- New Go test seeder in the root package, `perf_seed_test.go`,
  skipped unless `FOLD_PERF=1` (skipped tests count as not run — say
  so in its skip message). It builds, through the models with
  `CreateInBatches`-style bulk inserts: 5,000 member accounts, 300
  groups with leaders and rosters drawn from those accounts, one
  weekly series per group at varied ages (some years old) with its
  stored occurrences, one 500-person occurrence with going
  registrations across those groups, and a jobs table holding a
  realistic backlog (tens of thousands of finished rows, a live
  queue, some running rows with old `locked_at`). Test passwords use
  the cheapest bcrypt cost per the repo rule. The seeder runs
  against the dev database the normal test harness gives it; it is
  never part of `make test`'s default run, and it must refuse to run
  when the database it was handed is not visibly a throwaway one
  (require an empty `users` table before seeding).
- `TestPerfExplainPack` in the same file runs
  `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)` via `db.Raw` over the
  seeded data for exactly five statements, printing each plan with a
  label: (1) the `ensure` frontier read, old form and new
  (`internal/events/extend.go:89` today); (2) one
  `reminderReaders` batch's heaviest query (old per-user form noted
  as ×500, estimate); (3) the claim statement copied verbatim from
  `internal/jobs/jobs.go:218`; (4) the date-listing occurrence fetch
  (`api_events.go:102`) for a member-heavy group page window; (5) the
  reconcile scan R6 adds, once that lands. A shell wrapper
  `scripts/perf-explain.sh` runs the test with `-v` and says where
  the output went. Shell or Go only.
- A query counter helper for the count assertions in slices a, b and
  d: the GORM callback registration pattern already used at
  `api_notifications_test.go:645`, turned into a counting variant
  beside its first user. `TestCancelDateNoticeReadsStayBounded`
  (`api_notifications_test.go:536`) is the existing precedent for a
  bounded-reads test.
- Acceptance for this slice alone: the pack runs end to end on a dev
  machine and its printed plans are quoted in the PR description,
  confirming or replacing every estimate the later slices cite. If a
  plan already uses an index where a slice assumed a scan, that
  slice's premise is re-stated before it starts.

### 3. Slice R11-a — incremental Extend from the stored frontier

Current state. Daily (and at every start, `jobs_daily.go:100`), the
extender loads every still-open series (`internal/events/extend.go:35`)
and per event `ensure` re-expands the rule from its very first date to
the 18-month horizon (`internal/events/extend.go:81`,
`events.Expand` at `internal/events/expand.go:112`, the iterator walk
at `internal/events/expand.go:156`), plucks every stored
`original_date` (`internal/events/extend.go:89`), and diffs in memory.
Cost per series per run is proportional to the series' whole life, not
to the day that passed (estimate from the shape). Rows that exist —
cancelled, overridden — are preserved because the diff only ever
inserts (`internal/events/extend.go:27`).

Change, in `internal/events` only:

- Add `ExpandWindow(spec Spec, after string, horizonEnd time.Time)`
  beside `Expand`: same parsing, same caps (`MaxOccurrences`,
  `MaxExpandSteps` at `internal/events/expand.go:32`), same `until`
  and sub-daily rules, returning only occurrences with
  `OriginalDate > after` (`after` empty means from the start, so the
  function degenerates to `Expand`). Implement it over the existing
  iterator, skipping without appending: memory and the dense-window
  count stop growing with the skipped past, while step counts and
  error behaviour stay provably the same. Do not switch to the rrule
  library's bounded-between form in this slice; the current code
  rejected it for a stated reason (`internal/events/expand.go:152`)
  and equality with `Expand` is this slice's whole claim.
- `ensure` (`internal/events/extend.go:72`) reads the frontier with
  one indexed aggregate — `SELECT max(original_date) FROM
  event_occurrences WHERE event_id = ?`, served by the unique index
  on (event, original date) (`models/models.go:352`) — passes it to
  `ExpandWindow`, and inserts what comes back with the existing
  `ON CONFLICT DO NOTHING` batch (`internal/events/extend.go:115`)
  kept as the race belt. The pluck-all and the `have` map go.
- Why the frontier is sound, to be restated in the code comment:
  stored dates for a series are prefix-contiguous by construction —
  every generation path writes from the series start through its
  horizon — so any rule date on or before the stored maximum either
  exists as a row or was already past when a series edit ran (edits
  only add future dates, `internal/events/series.go:85`). Taking the
  maximum over all rows including cancelled ones preserves DEC-91's
  `cancel_source` exactly: `ensure` never restored a series-cancelled
  date and still does not (restoring is the series edit's `restore`
  action at `internal/events/series.go:72`, fed by the full `Expand`
  in the edit path, untouched here); a person's manual cancellation
  and every per-date override stay untouched because insertion
  remains the only write. `AfterEnsure` still receives exactly the
  added dates (`internal/events/extend.go:25`), so room booking on
  new dates (`api_room_series.go:541`) is unchanged.
- Accepted leftover: the iterator still steps through the skipped
  past, bounded by `MaxExpandSteps`. A truly ancient daily series
  pays step time, not memory or database rows. Note it; do not add a
  seek.

Scope out: series edits, `Expand` itself, the startup `Extend` call,
the advisory lock (`internal/events/extend.go:124`).

Tests (`internal/events/expand_test.go` and the extend tests beside
`extend.go`): a property table over specs — weekly and daily across a
DST change, monthly, an `until`-bounded rule, a one-time event — where
for several cut points, seeding the rows `Expand` would have produced
up to the cut and then running incremental `ensure` yields a stored
set (`original_date`, `starts_at`, `ends_at`) identical to a fresh
full `ensure` from empty; a case where the newest rows are cancelled
with both `cancel_source` values (`models/models.go:95`) and one
carries a title override, all untouched after; an empty-frontier
case. A reads-bounded case with the slice 0 counter: a no-op `ensure`
for an old series issues the same small query count as for a young
one. Existing `TestExpand*` behaviour is untouched.

Acceptance: property cases byte-equal; the gate's statement 1 shows
the frontier read as an index scan returning one row where the old
pluck returned the series' whole life; `make test` green.

### 4. Slice R11-b — batched reminderReaders

Current state. `reminderReaders` (`reminders.go:262`) decides,
per recipient, who may still see a reminder's date. Per user it pays
one `dateCandidates` load (`reminders.go:283`) — the events fetch
plus groups, rels and hosted lookups inside `eventCandidates`
(`api_event_access.go:662`) — and, for anyone who fails the ordinary
check, one `scopedOccurrenceReads` (`reminders.go:294`), itself a
handful of batched queries (`api_occurrence_access.go:57`). Estimated
at the audit as roughly 2,000 queries for one 500-person reminder;
the exact multiple is the gate's to confirm. Callers: the email
reminder snapshot (`messages_send.go:182`) and, one user at a time,
the push reminder recheck (`reminders_push.go:351`).

Change: same signature, same callers, one batched body in
`reminders.go`:

- Load the occurrence, its event and (when present) its group once.
  The occurrence-missing answer stays `errAudienceGone`
  (`reminders.go:265`).
- Load all users once (`reminders.go:270` already does), all active
  memberships of those users in the event's group in one query (the
  multi-user form of `relsFor`, `api_event_access.go:57`, reduced with
  `membershipRel` at `api.go:468` — leaders included as members,
  `internal/policy/policy.go:45`), and all host rows for a church-wide
  event in one query (the form of `hostedEventIDs`,
  `api_event_access.go:76`).
- Ordinary visibility becomes pure per-user evaluation over those
  facts, identical to `visibleSet.canSeeDate`
  (`api_occurrence_visibility.go:18`): build the view with
  `policy.EffectiveVisibility(ev.Visibility, occ.VisibilityOverride)`
  (`internal/policy/policy.go:755`) and answer
  `policy.CanSeeEvent(asPerson(viewer), view, group, rel)`
  (`internal/policy/policy.go:224`). A guest user is never a viewer
  (nil, signed-out eyes only — `reminders.go:279`), and a missing
  group row for a group event still means nobody ordinary, matching
  `canSeeDate`'s nil-group refusal.
- Exact-date invitations: one `OccurrenceAccess` query for the whole
  recipient set on this occurrence (active, not ended — the
  predicates at `api_occurrence_access.go:57`), then one call to the
  already-batched `occurrenceInvitationReadsIn`
  (`api_occurrence_access.go:77`), whose access-id keying guarantees
  one person's grant never leaks to another. Map each surviving read
  back to its subject. Guests skip this half entirely, as today
  (`reminders.go:291`). Total work is a fixed list of reads (state
  the list in the function comment; about eight, independent of
  recipient count — estimate until the count test pins it).
- Untouched by design: push reminder planning. `planPushReminder`
  and `push.Window.LastAllowed` (`reminders_push.go:98`,
  `internal/push/window.go:93`), the sweep's own one-query candidate
  read (`reminders_push.go:131`), and `deliverReminderPush`'s
  send-time rechecks including its 15-minute grace
  (`reminders_push.go:358`) all stay byte-for-byte behaviour; the
  push recheck simply gets cheaper through the same function.

Tests: the existing characterization must pass unedited —
`TestReminderRechecksWhoCanSeeTheDate`,
`TestReminderGuestOnlyForPublicDates` and
`TestReminderGuestsEmptyDatesAndMoves` in `reminders_test.go`,
`TestPushReminderRechecksWhoCanSeeTheDate` and
`TestPlanPushReminder` in `reminders_push_test.go`. Add a visibility
matrix test in `reminders_test.go` (series and override visibility ×
admin / leader / member / signed-in outsider / guest / exact-date
invitee, church-wide and group events, inactive group) asserting the
returned set, and a count test with the slice 0 counter: the query
count for 1, 10 and 200 recipients is identical. Before/after counts
go in the PR description.

Acceptance: matrix and characterization green; count constant;
`make test` green; no change to any reminder text, key or schedule.

### 5. Slice R11-c — running-lease index and claim check

Current state. The claim's due branch is indexed
(`idx_jobs_due`, `migrations/00051_jobs.sql:26`); its lease-takeover
branch (`status = running AND locked_at` older than the lease,
`internal/jobs/jobs.go:220`) is a scan by the migration's own
admission that the table "holds a few hundred rows at most"
(`migrations/00051_jobs.sql:24`). Push bursts (per-device jobs) and
30 days of finished rows (`jobs_daily.go:23`) make that assumption
the first to age out — and R6 adds a claim per emailed notice on top.

Change:

- New goose migration (next free number): `CREATE INDEX
  idx_jobs_running_lease ON jobs (locked_at, id) WHERE status =
  'running'`. No model change (indexes the drift test does not model
  need none; confirm against `migrations/migrations_test.go`).
- Run the gate's statement 3 before and after. If the planner uses
  the partial index for the takeover branch, the slice ends there.
  Only if the `OR` keeps it from the index, rewrite the claim's inner
  select as two indexed candidate selects combined and ordered by
  (`run_at`, `id`) with `LIMIT 1` — semantics frozen: same status
  set, same lease interval, attempts incremented once, `SKIP LOCKED`
  preserved, earliest-due-first preserved across both branches.
  Keep the one-statement claim; do not split it into a select and an
  update, which would reopen the double-run race the single
  statement closes.

Tests: `internal/jobs/jobs_test.go` keeps passing, above all
`TestExpiredLeases`, `TestTwoWorkersNeverRunAJobTwice` and
`TestEnqueueDedupes`. Add a claim-order case: one expired-lease
running job with an earlier `run_at` wins over a due queued job, and
the reverse ordering also holds, so the two-branch merge cannot drift.

Acceptance: before/after plans quoted in the PR; the after plan shows
the takeover branch on `idx_jobs_running_lease` (or the PR states the
measured plan and why the index still earns its place at burst
sizes); concurrent-claim tests green.

### 6. Slice R11-d — SQL visibility and LIMIT fast paths, only where identical

Current state. Every date list funnels through
`occurrencesForEventsAfterErr` (`api_events.go:85`): it loads
candidate facts, fetches every scheduled occurrence in the window
with no `LIMIT` (`api_events.go:102`), filters each row through
`visibleSet.canSeeDate` in Go (`api_events.go:115`), and only then
applies the caller's cap (`api_events.go:121`). The calendar does the
same shape at `api_calendar.go:132`, capping at 500 after merging
exact-date invitations (`api_calendar.go:196`). `canSeeDate` is one
call: `policy.CanSeeEvent` over `policy.EffectiveVisibility`
(`api_occurrence_visibility.go:31`), and `CanSeeEvent` has exactly
four inputs — the person, the event's church-wide flag and effective
visibility, the group's status, and the viewer's rel
(`internal/policy/policy.go:224`). A SQL fast path is lawful only
where those inputs make the answer identical for every row it would
shorten.

Qualifying cases (the complete list; everything else stays on
today's path unchanged):

1. The viewer is an admin. `CanSeeEvent` is unconditionally true
   (`internal/policy/policy.go:225`, with `asPerson`'s active-admin
   rule at `api.go:449`). `LIMIT` and the cursor tuple may move into
   SQL in full.
2. Signed-out viewer, `GET /events` (`apiListOpenEvents`,
   `api_event_access.go:246`): visible exactly when the effective
   visibility is public and the event is church-wide or its group is
   active. SQL: join events (and groups where bound), predicate
   `COALESCE(o.visibility_override, e.visibility) = 'public' AND
   (e.group_id IS NULL OR g.status = 'active')`. There is no cap on
   this endpoint, so the gain is rows not fetched, not a `LIMIT`.
3. One group's upcoming page (`apiGetGroup` via
   `api_groups.go:257`, cap and cursor at `api_groups.go:181`) when
   the viewer is an admin (case 1) or an active member — leaders
   included by `Rel.Member` (`internal/policy/policy.go:45`) — of
   that same active group: every effective visibility then answers
   true (public, private when signed in, invite-only for a member).
   `LIMIT limit + 1` and `(starts_at, id)` greater than the cursor
   move into SQL; the `truncated`/`hasMore` meaning is unchanged
   (`api_events.go:121`).

Explicitly not qualifying: the calendar (`api_calendar.go:57`),
because exact-date reads merge after the ordinary filter and the cap
sits after scope decisions; mixed sets of many groups' events for a
signed-in non-member, where invite-only rows need the per-group rel;
any path where a date can be authorized per date rather than by
event facts — date candidates never authorize the series, and this
slice must not blur that (`api_event_access.go:648`). The
single-event page (`api_event_access.go:258`) may use case 1 only; it
carries no cap and gains little — include it only if it falls out of
the shared helper for free.

Implementation shape, all inside `occurrencesForEventsAfterErr`:

- One decision helper beside it in `api_events.go`, for example
  `datesDecidableInSQL(me *models.User, vs visibleSet) bool` plus,
  for case 3, passing the single group's id, status and the viewer's
  rel down from the group handler as an explicit fast-path hint —
  computed from loaded facts, never from request input. The helper's
  whole job is restating cases 1–3; it is table-tested (below).
- When the helper says yes, add the predicate and, where a cap
  exists, `LIMIT limit + 1` and the cursor comparison to the query
  at `api_events.go:102`, ordering by (`starts_at`, `id`) as today.
  Keep the `canSeeDate` loop running over the fetched rows as a belt:
  on a limited page it is cheap, and it turns any helper mistake
  into a test failure instead of a leak. When the helper says no,
  the query and loop are exactly today's.
- Enrichment after the kept list (counts, seats, names) is untouched;
  it already runs only over kept rows.

Tests: an equivalence table in `api_events_test.go` forcing both
paths (helper overridden per case) over the visibility matrix —
series × override ∈ {public, private, invite-only}, church-wide and
active/pending/archived groups, viewers admin / leader / member /
signed-in outsider / signed-out — asserting identical occurrence id
lists, identical `hasMore`, and identical next cursor. Existing
visibility suites stay green, including the group-details guard
(`TestGroupDetailsGoThroughGroupShownTo` — the SQL predicate reads
visibility columns only, never a group name, place or id for output)
and `TestNarrowEventHiddenFromGroupLists`. A count case with the
slice 0 counter: a capped page over a window holding many times the
cap fetches at most cap + 1 occurrence rows on the fast path. The
gate's statement 4 carries the plan before/after.

Acceptance: equivalence green with the belt on; a deliberately broken
predicate in a scratch run fails the equivalence test (note the
result in the PR, do not commit the break); plans quoted.

### 7. Slice R11-e — database pool cap 20/5, configurable

Current state. `openDatabase` (`db.go:22`) opens GORM and migrates;
nothing anywhere sets the pool sizes, so the database/sql defaults
rule (unlimited open connections; idle default left to the driver).
One Postgres serves the whole deployment, and HTTP handlers, job
workers and the migration advisory-lock connection all draw from this
one pool.

Change:

- `Config` gains `DBMaxOpenConns` and `DBMaxIdleConns`
  (`config.go:19`), read as `FOLD_DB_MAX_OPEN` and `FOLD_DB_MAX_IDLE`
  in `LoadConfig` with defaults 20 and 5 (the audit's numbers,
  estimates until the gate or a load report says otherwise) and
  validation in the `FOLD_WORKERS` style (`config.go:188`): whole
  numbers, open 1–200, idle 0 up to open; a bad value fails startup
  with the setting named, never silently clamps.
- `openDatabase` takes the two numbers (its one production caller is
  `main.go:111`) and applies `SetMaxOpenConns` / `SetMaxIdleConns`
  on the `sql.DB` it already fetches at `db.go:29`, before migrating.
  No connection-lifetime setting in this slice; the audit asked for
  the caps only.
- `.env.example` gains both variables with one-line comments in its
  existing style; the compose default story is unchanged (defaults
  in code, `docker compose up` untouched).

Tests: `config_test.go` beside `TestJobWorkerSettings` for defaults,
overrides and each refusal; `db_test.go` asserts
`sqlDB.Stats().MaxOpenConnections` after open. `make test` and the
full e2e sharded run double as the starvation check — 20 open must
feed the suite's parallel root-package shards
(`scripts/go-test.sh`) without new flakes; say explicitly in the PR
that the e2e run was the loaded-pool evidence, since no dedicated
load test exists yet.

Acceptance: caps live by default, refusal messages name the broken
variable, default `make test` / `make e2e` green at 20/5.

### 8. Acceptance checklist (all slices)

- [ ] The perf pack runs with `FOLD_PERF=1`, refuses a non-empty
      database, and every quoted audit number in the PRs is labelled
      measured or estimate.
- [ ] Extend: incremental output byte-identical to full re-expand
      across the property table, cancelled and overridden dates
      untouched, DEC-91 series restore still owned by series edits.
- [ ] Reminders: `reminderReaders` query count constant in recipient
      count; visibility matrix and push recheck behaviour unchanged;
      `Window.LastAllowed` planning untouched.
- [ ] Jobs: claim stays one statement, never runs a job twice across
      workers, lease takeover order preserved; index plan quoted.
- [ ] Listings: fast and slow paths return identical ids, ordering,
      `hasMore` and cursors across the matrix; non-qualifying paths
      provably unmodified; no hidden group detail reaches output.
- [ ] Pool: 20/5 by default, env-refusals clear, suites green under
      the caps.
- [ ] `go build ./...`, `go vet ./...`, `make lint-go`, `make test`,
      and (d touches a user flow) `make e2e` pass per slice.

### 9. Risks and rollback

- Each slice is independently revertible; slice c's migration rolls
  back by dropping one partial index, slice e by removing two setter
  calls (the env vars then refuse nothing — keep the config parse or
  drop it with the revert, say which).
- The dangerous slice is d: a wrong predicate is a privacy leak, not
  a slow page. The belt (keeping `canSeeDate`), the equivalence
  table, and keeping `policy` the only place visibility is decided
  are non-negotiable; the SQL restates settled facts, it never
  becomes a second policy. If the gate shows the window fetch is
  already cheap at 300 groups, cut slice d to cases 1 and 3 or drop
  it and say so.
- Slice a's frontier rests on prefix-contiguity. A hand-edited or
  partially restored database with a mid-series gap would keep that
  gap (today's full diff would fill it). The reconcile-style safety
  net is deliberate: none in this slice. Note the restore caveat in
  the PR; a future admin "rebuild dates" would use the retained full
  `Expand` path.
- Slice b concentrates several queries into few wider ones; keep the
  `IN` lists chunked where recipient counts can exceed a few thousand
  (the batching precedent at `api_notifications.go:245`).

### 10. Dependencies

- Slice 0 before a–e (nothing else here is measurable without it).
- Slice c pairs with R6 (above): either order works, same wave
  recommended.
- No slice here depends on the migration-squash or feature briefs;
  slice c shares the next-free-number queue with all siblings.

### 11. Open questions for Grier (each with a default that unblocks work)

1. If the gate shows a ceiling is not yet real at 5,000 members and
   300 groups, should that slice still land? Recommended default: a
   and b yes (strictly less work per run, behaviour pinned by
   equivalence tests), d only where the plan improves, c yes (one
   partial index is nearly free), e yes (a cap is a safety default,
   not a speed claim).
2. Are 20 open / 5 idle the right defaults for the smallest
   recommended droplet, whose Postgres default connection budget the
   app must share with its own workers? Recommended default: ship
   20/5 (the audit's figures) behind the env override; revisit from
   the first church's `pg_stat_activity`, not from further estimates.
3. Should the perf pack join CI on a schedule, or stay a manual dev
   tool? Recommended default: manual (`scripts/perf-explain.sh`)
   with plans quoted in PRs; a scheduled job is its own ops decision
   once a church's data makes the numbers matter weekly.



