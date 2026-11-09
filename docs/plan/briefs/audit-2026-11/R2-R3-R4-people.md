# Audit 2026-11, Cluster C — People: R2 import, R3 TOTP + export, R4 erasure

Implementation briefs for a coding agent. These are briefs, not code. Read
`AGENTS.md` first; where this file and `docs/plan/PLAN.md` disagree, PLAN wins
and the disagreement gets raised, not silently resolved.

Shared rules for all three items:

- Single Go binary with the SPA embedded; Postgres only; schema changes are
  new goose files in `migrations/` (next free number at implementation time;
  last seen `00062_reminder_push.sql`), never edits to old files. The GORM
  model change and `models.All()` update land in the same PR.
- Permissions are pure functions in `internal/policy` with table tests.
  Handlers load facts and call policy; no inline role checks.
- Sensitive actions write `audit.Write` rows in the same transaction as the
  change. Bell notices are `notify.Send` rows in that transaction too.
- JSON is camelCase under `/api/v1`. A new error a person can act on gets an
  declared `apiErr` value (`api_error_values.go`; it replaced the `errorCodes` map) plus a line in
  `ERROR_COPY` in `web/src/lib/errors.ts`. New response types get a pair in
  `tsContracts` so `web/src/types.ts` stays matched to the Go structs.
- UI uses `web/src/components/ui` controls and role tokens. Prove UI changes
  with `.cursor/skills/verify-ui` and the 390px screenshots.
- Background work goes through `internal/jobs` (`jobs.Kind` in
  `internal/jobs/jobs.go:32`, `jobs.Enqueue` in `internal/jobs/jobs.go:55`).
  Handlers must be safe to run twice.
- Tests sit next to the code they test. Verification for every slice:
  `go build ./...`, `go vet ./...`, `make lint-go`,
  `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`, `make test`,
  `cd web && npm test`, and `make e2e` where a user flow changes.
- Docs say "recommended", never the other word (the docs-wording guard test
  fails the build). No Python anywhere.
- Rejected audit items stay out: nothing here freezes web push, strikes SMS
  from the goals (R7), or adds an open self-signup mode (R8; DEC-4 stands,
  so import below is an admin door, never a public one).

---

## R2 — Generic people CSV import

- Priority: first-church readiness (audit build order step 4,
  `docs/AUDIT-2026-11-01.md:265`)
- Effort: M (audit estimate)
- Status: Approved 2026-11-01 (audit)
- GitHub issue: none known at brief time. PLAN milestone M4.5
  (`docs/plan/PLAN.md:1971`). The audit records this as reversing the
  DEC-15 import gate (`docs/plan/PLAN.md:2120`): the generic pipeline no
  longer waits for a ChurchTrac sample. A ChurchTrac column preset still
  wants a real sample when it is built later.

### 1. Goal / user outcome

An admin uploads a spreadsheet of people (name, email, phone, groups),
reviews a dry-run preview that says what will happen to every row, confirms,
and Fold creates or matches the accounts in the background. New adults get
the existing invitation email to set their own password. Nobody is typed in
one by one, and nothing is created before the admin has seen the preview.

### 2. Current state (evidence)

- People arrive one at a time. Admins create accounts singly in
  `apiAdminCreateUser` (`api_admin.go:486`), reached at
  `POST /api/v1/admin/users` (`api.go:800`). Without a password the account
  starts `invited` (`api_admin.go:530`); that handler sends no email.
- Invitations are the mailed door: `apiCreateInvitation`
  (`api_invitations.go:101`) stores an `Invitation` row
  (`models/models.go:577`) and emails a single-use link that expires in 24
  hours (`api_invitations.go:262`, mail text at `api_invitations.go:273`).
  Acceptance (`apiAcceptInvitation`, `api_invitations.go:305`) sets the
  password, activates the account, and joins at most one named group (the
  row has a single `GroupID`, `models/models.go:585`).
- Refusal rules to reuse: an invitation refuses a live existing account
  (`api_invitations.go:202`), but an existing guest row does not refuse.
  Email handling is `normalizeEmail` + `validEmail` (`api_auth.go:95`,
  `api_auth.go:98`) over a `citext` unique column (`models/models.go:135`).
- Adding anyone to a group goes through `changeRoleIn`
  (`api_groups.go:764`), which refuses a target whose status is not
  `active` (`api_groups.go:793`), checks `policy.CanAddToGroup`
  (`internal/policy/policy.go:428`) and `policy.GroupHasRoom`
  (`internal/policy/policy.go:420`), and writes the membership audit rows.
  Consequence: an imported person cannot be joined to groups until their
  account is active. Group joins must wait for acceptance (below).
- The age gate is shared: `classifyAccountInput` (`api_age_access.go:15`)
  over `accounts.Classify` (`internal/accounts/accounts.go:46`). Under 13
  never gets an account by any door, import included
  (`docs/plan/PLAN.md:1066`); teens start `pending_consent`
  (`api_admin.go:541`).
- The only CSV in the codebase today is the contact-history export
  (`api_contact_history.go:404`), which streams `text/csv` with CRLF and
  audits inside its transaction (`api_contact_history.go:470`). There is no
  CSV reader to reuse; `encoding/csv` from the standard library is the
  reader.
- The Admin people surface is the Users tab in `web/src/pages/Admin.tsx`
  (`UsersTab` at line 899; tab list at line 1603).
- Imported phones are never opted in by default
  (`docs/plan/PLAN.md:1084`): import writes no `Consent` rows
  (`models/models.go:889`). PLAN M4.5 acceptance also requires: under-13
  rows refused, teens `pending_consent`, set-password email rather than a
  password in the file (`docs/plan/PLAN.md:1975`).

### 3. Scope

In:

- Admin-only CSV upload, dry-run preview, confirm, background processing
  with per-row results, invitation email for new adults, audit rows.
- Columns: `name`, `email` (required); `phone`, `groups`, `birthDate`
  (optional). Generic spreadsheet shape first.
- Matching an existing account by normalized email: groups are added
  (person already active), blank name/phone fields are filled, nothing
  non-blank is overwritten.

Out:

- ChurchTrac (or any vendor) column preset. Later, as a header-mapping on
  this same pipeline, when a real sample exists (DEC-15, DEC-40).
- Household creation and household people. Rows that share one email are
  flagged in the preview; the admin adds household people by hand after.
  PLAN M4.5 imagined an offer to add them (`docs/plan/PLAN.md:1976`);
  bulk household mutation is deliberately deferred (open question 1).
- Leader assignment from the file. Everyone joins as `member`; leaders are
  promoted with the existing controls, one deliberate act at a time.
- Editing existing accounts beyond filling blanks, changing emails, setting
  passwords, marking phone consent, creating groups named in the file.
- Import for leaders or members. Admins only (`policy` decision below).

### 4. Slices (in order, each shippable on its own)

Slice R2-1 — database: import batches, rows, pending joins.

- New goose migration (next free number): tables `people_imports`
  (`id`, `created_by`, `status` preview/confirmed/running/done/failed,
  `file_name` for display only, `row_count`, counters, `created_at`,
  `confirmed_at`, `finished_at`), `people_import_rows` (`import_id`,
  `line` (1-based data line), `name`, `email` (citext), `phone`,
  `groups_text`, `birth_date`, `action`, `reason`, `user_id` nullable,
  `status`), and `people_import_joins` (`user_id`, `group_id`,
  `import_id`, unique user+group) for groups to join when an invited
  account activates. Matching GORM models in `models/models.go` near
  `Invitation` (line 577) and entries in `models.All()`
  (`models/models.go:1030`).
- No raw file is stored. The parsed rows are the record; the preview the
  admin confirmed is exactly what runs.

Slice R2-2 — policy and classification (pure, table-tested).

- `internal/policy`: `CanImportPeople(p *Person) bool` (admin only).
- New `import_classify.go` (root package, next to the handlers that use
  it): given a parsed row plus loaded facts (existing user by email and
  its status, group name resolutions, duplicate-in-file set), return one
  action: `invite` (new adult), `inviteTeen` (new teen, pending consent),
  `match` (existing active account), `inviteGuest` (existing
  `status=guest` row, invitation path, no group joins yet), `skip` (with
  reason), `error` (with reason). Age uses `accounts.Classify` with the
  church-day clock the other doors use (`api_age_access.go:21`).

Slice R2-3 — preview API (no people written).

- `POST /api/v1/admin/people-imports/preview` (multipart, field `file`,
  CSRF form field like every upload): admin-gated by route group
  (`a.apiAdmin()`, as at `api.go:799`). Caps: 2 MiB file, 2000 data rows,
  UTF-8 (a leading BOM is tolerated), comma delimiter. Header names are
  matched case-insensitively after trimming; unknown columns are ignored
  and named in the response; a missing `name` or `email` column fails the
  whole file. Writes one `people_imports` row in `preview` status plus
  its classified `people_import_rows`, and returns them paged (see
  shapes). One audit row `people_import.previewed` in the same
  transaction (meta: rowCount only, never file contents or emails).
- `GET /api/v1/admin/people-imports/:id` and
  `GET /api/v1/admin/people-imports/:id/rows?page=` for the preview and
  results screens. `DELETE /api/v1/admin/people-imports/:id` discards an
  unconfirmed preview (audit `people_import.discarded`).

Slice R2-4 — confirm and background processing.

- `POST /api/v1/admin/people-imports/:id/confirm`: in one transaction,
  flip the batch to `confirmed`, audit `people_import.confirmed`, and
  `jobs.Enqueue` kind `people.import` with payload `{"importId":N}` and
  unique key `people.import:<id>`, so a double-confirm queues nothing.
- Worker in new `people_import.go`, registered alongside the other job
  kinds (pattern: `jobs_daily.go:28`). It claims the batch (`running`),
  then processes rows in line order, one transaction per row through the
  existing contact-mutation gate, marking each row done/failed with its
  reason. Re-running a row that already finished changes nothing.
  - `invite`: create the `Invitation` the same way `apiCreateInvitation`
    does (extract a shared `createInvitationIn` helper rather than
    copying its transaction body), audit as it does, write
    `people_import_joins` for every resolved group, then send the
    invitation email after commit (current post-commit pattern,
    `api_invitations.go:255`). If R6's outbox has landed by then, queue
    through it instead; do not build new mail reliability here.
  - `inviteTeen`: same, with `AccountType` teen, so acceptance lands in
    `pending_consent`; joins stay pending until consent activates the
    account (slice R2-5).
  - `match`: fill blank `name`/`phone` only; join each resolved group now
    via `changeRoleIn` as `member`, never over a full group's limit
    (a full group is a per-group row note, not a row failure).
  - `inviteGuest`: invitation as above (the guest-upgrade path already
    tolerates an existing guest row, `api_invitations.go:202`); joins
    pending until the account is real. Never load a guest row as an
    actor.
- On finish: batch `done` (or `failed` if the batch claim itself broke),
  audit `people_import.finished` with counts, and a bell notice to the
  importing admin via `notify.Send` (new kind in
  `internal/notify/kinds.go`, admin-only text, link to the results
  screen; counts only, no names or emails in the body).

Slice R2-5 — pending joins applied at activation.

- Wherever an invited account becomes active (invitation acceptance in
  `api_invitations.go:305`, account-setup confirm, and teen activation
  when consent is granted), after the account is active and inside the
  same transaction, drain that user's `people_import_joins`: for each,
  re-load the group, require `policy.CanInviteIntoGroup`-grade facts
  (group still active) and `policy.GroupHasRoom`, then join as `member`
  through the same locked path `changeRoleIn` uses. A group that is full
  or no longer active drops the join and writes one bell notice to the
  import's admin (counts, group name is fine for an admin). Delete join
  rows as they are applied or dropped.

Slice R2-6 — Admin UI.

- `web/src/pages/Admin.tsx`, Users tab: an "Import people" section above
  the user list (one secondary action; the tab's primary action stays
  as it is). File picker using `Field`/`Button` from `components/ui`,
  the limits stated next to the field ("CSV with name and email columns,
  up to 2,000 rows"), then the preview: counts per action, a paged table
  of rows with reason text, `Chip` per action (status never by color
  alone), and Confirm / Discard. After confirm, the same screen polls
  the batch and shows per-row results plus a "Resend invite" action per
  invited row (reuses the invitation resend path the single-invite UI
  has; if none exists, it re-creates the invitation and revokes the old
  one, audited). Empty state via `EmptyState`. API client functions in
  `web/src/api.ts`; types in `web/src/types.ts` with `tsContracts` pairs.

### 5. Classification rules (the preview is the contract)

- Email is normalized with `normalizeEmail` and checked with
  `validEmail`. An invalid email is a row `error`, reason
  `invalidEmail`. A name over 100 characters or phone over 30 matches
  the invitation limits (`api_invitations.go:119`) as row errors.
- Duplicate email inside the file: the first occurrence classifies
  normally; later ones are `skip`, reason `duplicateInFile`, with the
  line of the first. Shared-email households are served by that flag,
  not by merging rows.
- Existing account by email (including soft-deleted check via
  `Unscoped`, as invitations do): `active` -> `match`; `invited` ->
  `skip` reason `alreadyInvited` (the results screen offers resend);
  `guest` -> `inviteGuest`; `pending_consent` -> `skip` reason
  `waitingForConsent`; `disabled` -> `skip` reason `accountDisabled`;
  a soft-deleted address -> `invite` (the address was freed by erasure;
  see R4).
- `groups` cell: semicolon-separated group names, trimmed, matched
  case-insensitively against group names. An unknown name is a row
  `error` reason `unknownGroup` naming it (never auto-create). Two live
  groups with the same name is `error` reason `ambiguousGroup`. A
  pending or archived group is `error` reason `groupNotActive`.
- `birthDate` cell (`YYYY-MM-DD`): under 13 on the church day -> row
  `error` reason `under13` (no account by any door); 13-17 ->
  `inviteTeen`; 18+ or blank -> adult (adults only confirm 18+, so a
  blank date imports as adult; open question 2).
- Confirm runs the stored preview; it does not reclassify. Facts that
  changed in between (account created by hand, group archived) surface
  as per-row results, never as a different silent action.

### 6. Data / API shapes

Preview response (`peopleImportJSON`):

```json
{
  "import": {
    "id": 12, "status": "preview", "fileName": "members.csv",
    "rowCount": 200, "ignoredColumns": ["address"],
    "counts": {"invite": 150, "inviteTeen": 4, "match": 30,
               "inviteGuest": 2, "skip": 9, "error": 5},
    "createdAt": "2026-11-02T10:00:00Z"
  },
  "rows": [{
    "line": 2, "name": "Sam Lee", "email": "sam@example.org",
    "phone": "", "groups": ["Tuesday Group"], "birthDate": "",
    "action": "invite", "reason": "", "userId": null, "status": "pending"
  }]
}
```

- `reason` values are data for the results screen, each with a line in
  the SPA's reason copy (plain words, what to do next):
  `invalidEmail`, `nameTooLong`, `phoneTooLong`, `duplicateInFile`,
  `alreadyInvited`, `waitingForConsent`, `accountDisabled`,
  `unknownGroup`, `ambiguousGroup`, `groupNotActive`, `groupFull`,
  `under13`, `badBirthDate`, `alreadyMember`, `failed`.
- Row `status`: `pending`, `done`, `failed`. Batch `status`: `preview`,
  `confirmed`, `running`, `done`, `failed`.
- New actionable API errors (code in `api_errors.go` + `ERROR_COPY`):
  `import_file_required` "choose a CSV file to import",
  `import_file_type` "that file isn't a CSV spreadsheet",
  `import_too_large` "that file is too large; keep it under 2 MB",
  `import_too_many_rows` "that file has more than 2,000 rows; split it
  and import in parts", `import_missing_columns` "the file needs name
  and email columns", `import_not_preview` "this import was already
  confirmed or discarded; upload the file again to start over".
- Audit actions (new constants in `internal/audit/audit.go`, next to
  `UserCreated` at line 60): `people_import.previewed` (target the
  batch; meta rowCount), `people_import.confirmed`,
  `people_import.discarded`, `people_import.finished` (meta counts).
  Per-person effects keep their existing audit rows (invitation created,
  membership added) so a person's history reads the same however they
  arrived; add meta `viaImportId` to those rows.
- Cells are text. A cell starting with `=`, `+`, `-`, or `@` is stored
  as typed (Fold never evaluates it); the R3 export side guards its own
  output. No cell is ever rendered as HTML.

### 7. Tests

Extend: `api_invitations_test.go` (shared invitation helper keeps its
behavior), `api_admin_test.go` (route gating), `internal/policy`
table tests in `internal/policy/policy_test.go`.

New, next to their source:

- `people_import_test.go`: `TestImportPreviewClassifiesRows` (one row
  per action and reason), `TestImportPreviewWritesNoPeople`,
  `TestImportConfirmInvitesAndMatches` (job run synchronously in the
  test; invitation in the outbox; match joined; blanks filled, values
  not overwritten), `TestImportDuplicateEmailFirstRowWins`,
  `TestImportUnder13RefusedTeenPendingConsent`,
  `TestImportUnknownAndAmbiguousGroups`,
  `TestImportJobRerunIsIdempotent`, `TestImportCapsEnforced`
  (size, rows, missing columns, non-CSV), `TestImportAdminOnly`.
- `import_classify_test.go`: table test over every status/reason pair.
- Activation joins: `TestImportJoinsAppliedOnAcceptInvite`,
  `TestImportJoinDroppedWhenGroupFills` in `people_import_test.go`.
- Web: `Admin.test.tsx` cases for preview rendering per action; e2e
  `web/e2e/people-import.spec.ts`: upload a small CSV, see the preview,
  confirm, see results, accept one invite from the dev outbox, land in
  the named group. Add the page to `web/e2e/ui-pages.ts` if it becomes
  its own route.

### 8. Acceptance checklist

- [ ] Previewing a file creates no user, invitation, membership, or email.
- [ ] Confirming runs exactly the stored preview; a second confirm is a
      no-op answer, and re-running the job changes nothing.
- [ ] A new adult receives the ordinary invitation email and sets their
      own password; no password column is read or stored.
- [ ] A matched account keeps its name, phone, and email unless blank.
- [ ] Under-13 rows create nothing; teen rows wait for consent before
      any group join; no phone is opted in.
- [ ] Group joins respect member limits; a full group is a row note.
- [ ] Every batch step and per-person effect is audited; the importing
      admin gets one bell notice when the batch finishes.
- [ ] Non-admins get 403 on every import route; CSRF guard test still
      names only the allowed exemptions.
- [ ] `make test`, `cd web && npm test`, `make e2e`, and the quick
      checks (`go vet`, `make lint-go`, `tsc --noEmit`, `knip`) pass.

### 9. Risks, rollback, security/privacy

- Duplicate semantics are the risk (audit). The preview is the control:
  nothing merges silently, and matching never overwrites. Rollback of a
  bad batch is manual (disable accounts it created; the batch record
  names them) — state this in the confirm dialog copy.
- The file holds personal data: it is parsed in memory, never written to
  disk or the blob store, and never logged. Stored rows hold name and
  email; they are erased with the person under R4 (its inventory covers
  the new tables) and previews left unconfirmed are deleted by a daily
  cleanup job after 7 days (add to `jobs_daily.go` kinds).
- Invitation links expire in 24 hours, so a 200-person import will see
  late clickers. The results screen's resend action is the remedy; do
  not invent a longer-lived token (open question 3).
- Rollback of the feature: routes and job kind can be removed; the three
  tables are inert without them. No existing table changes shape.

### 10. Dependencies

- R4 first is recommended: its inventory must cover the new tables, and
  its rule "a freed email can be invited again" is classification input.
  If R2 lands first, R4's inventory adds the import tables.
- R3's people export should round-trip this import's columns (same
  header names), so land R2's column names before R3 export, or agree
  them in review.
- R6 (outbox) if landed first: invitation mail queues through it.

### 11. Open questions for Grier (each with a default that unblocks work)

1. Should shared-email rows create household people automatically?
   Recommended default: no for v1; flag them and link to the household
   screen. Bulk-creating children and guardian links needs its own
   review (DEC-32).
2. A blank `birthDate` imports as an adult. Recommended default: yes —
   adults only confirm 18+ at every other door; churches' spreadsheets
   rarely carry dates of birth. The preview counts blank-date rows so
   the admin sees the assumption.
3. Invitation lifetime for imports. Recommended default: keep the
   existing 24 hours plus one-click resend per row and "resend all
   expired" on the batch. A longer import-only lifetime is a token
   policy change (PLAN 10.1 caps tokens at 24 hours).

---

## R3 — Admin TOTP + people/own-data export

- Priority: first-church readiness (audit build order step 4,
  `docs/AUDIT-2026-11-01.md:265`)
- Effort: M (audit estimate; TOTP is most of it, both exports are small)
- Status: Approved 2026-11-01 (audit)
- GitHub issue: none known at brief time. PLAN milestone M4.9
  (`docs/plan/PLAN.md:2010`); the export half is PLAN section 10
  (`docs/plan/PLAN.md:1053`). The audit records no decision reversed.

### 1. Goal / user outcome

Two outcomes, one item:

1. An admin signs in with a password, then with a 6-digit code from an
   authenticator app (or a one-time recovery code). An admin without a
   second factor can reach only the enrolment page. Fold holds minors'
   data on the public internet; a reused admin password stops being
   enough on its own.
2. A church is not locked in. An admin downloads everyone as a CSV that
   the R2 import can read back, and any person downloads their own data
   as JSON.

### 2. Current state (evidence)

- Sign-in is password-only, one step: `apiLogin` (`api_auth.go:31`)
  reserves throttle attempts, checks the password, then signs the
  session in and answers the user (`api_auth.go:75`, `api_auth.go:90`).
  There is no second step and no pending state. Login is
  `POST /api/v1/auth/login` (`api.go:566`).
- Sessions carry two values, written by `signInSession`
  (`api_auth.go:562`): `userID` and `sessionVersion`. Every request
  reloads the user and refuses a stale `SessionVersion` or a person
  who cannot sign in (`api.go:347`, check at `api.go:365`). Credential
  changes bump `SessionVersion` in `replacePasswordIn`
  (`account_security.go:129`), and `apiChangePassword` shows the
  keep-this-browser pattern: rewrite the cookie with the new epoch
  (`api_auth.go:276`).
- Passwords are at most 72 bytes and at least 10 characters, checked
  with `checkNewPassword` in `password_rules.go:31` (rule text at
  `password_rules.go:18`). TOTP changes nothing there.
- Signed-in password proof already exists: `reauthenticate`
  (`api_auth.go:201`) reserves `user:<id>` + IP throttle keys before
  bcrypt, and `lockCurrentCredentials` (`account_security.go:19`)
  binds that proof to the locked row inside the write transaction.
  Enrolment, disabling, and own-data export reuse both, never a fresh
  password check.
- Throttling is shared machinery, not login-only: `reserveLoginAttempt`
  (`api_auth.go:126`) counts each key atomically before the credential
  is checked, `lockLoginKeysAtLimit` (`api_auth.go:184`) locks keys at
  their limit, and `failureLimit` ( `api_auth.go:114`) gives any
  non-`ip:` key the 10-per-15-minutes limit (`api_auth.go:108`). A
  `totp:user:<id>` key therefore throttles like an email key with no
  new code. Daily pruning is `pruneLoginThrottles` (`api_auth.go:176`).
- The encryption pattern to copy is `push_keys.go`: the VAPID private
  key sits in `settings` sealed with AES-GCM under a SHA-256 of
  `"fold:vapid:v1:"` + `SESSION_SECRET` (`vapidCipher`,
  `push_keys.go:48`), with an AAD binding the ciphertext to its purpose
  (`push_keys.go:41`), `sealVAPIDKey` at `push_keys.go:61` and
  `openVAPIDKey` at `push_keys.go:78`. When the secret no longer opens
  it, the boot path regenerates and tells admins in the same
  transaction (`push_keys.go:149`). PLAN already names the TOTP columns
  as future work: `totp_secret_enc` and `totp_enabled_at`
  (`docs/plan/PLAN.md:278`), "encrypted like provider secrets (C8)"
  (`docs/plan/PLAN.md:2012`). `SESSION_SECRET` is at least 32 bytes
  outside dev (`config.go:136`, check at `config.go:150`).
- The `User` row has no TOTP fields today (`models/models.go:132`):
  identity at `models/models.go:135` to `models/models.go:165`,
  `SessionVersion` at `models/models.go:162`, soft delete at
  `models/models.go:168`. `CanSignIn` is active + has a password
  (`models/models.go:223`).
- Single-use secrets are hashed, never stored raw: `hashToken` is
  SHA-256 (`tokens.go:39`), account tokens expire in 24 hours
  (`tokens.go:27`) and are claimed atomically in
  `lockAndConsumeAccountToken` (`account_security.go:69`). Recovery
  codes differ (they live for years and a person types them), so they
  get bcrypt instead — see slice R3-1.
- The CLI shape is `cli.go`: `--json` everywhere (`cli.go:47`), each
  subcommand loads only what it needs (`cli.go:40`), `push keys rotate`
  is the precedent for a database-writing maintenance command
  (`cli.go:155` handler, `cli.go:188` opens the database without
  migrating). PLAN names the command: `fold user reset-2fa`
  (`docs/plan/PLAN.md:2012`).
- The only outgoing CSV is the contact-history export at
  `GET /api/v1/admin/users/:id/contact-history.csv` (`api.go:589`). Its
  shape is the one to copy: materialize to a private temp file inside a
  repeatable-read transaction, re-lock and re-check the actor, audit,
  then stream (`api_contact_history.go:399`, audit at
  `api_contact_history.go:470`, stream at `api_contact_history.go:477`).
  Its formula guard `contactHistoryCSVCell`
  (`api_contact_history.go:365`) prefixes an apostrophe when a cell
  could be read as a formula; R2 noted the export side needs that guard
  (§6 of the R2 brief in this file).
- PLAN M4.9 is acceptance-ready and this brief follows it: RFC 6238
  for every admin from v0.5, ten one-time recovery codes stored hashed,
  an unenrolled admin reaching only the enrolment page, another admin
  resets a lost device, a sole admin uses a recovery code or the CLI
  (`docs/plan/PLAN.md:2010`); enrol and reset end other sessions;
  attempts are throttled like passwords (`docs/plan/PLAN.md:2016`).
  Section 10 repeats the requirement (`docs/plan/PLAN.md:1032`) and
  the hardening checklist tracks it (`docs/plan/PLAN.md:1108`).
- Neither a TOTP nor a QR library is present: `go.mod` (`go.mod:7`)
  has no OTP package, and `web/package.json:2` lists no QR package.
  `CGO_ENABLED=0` rules nothing out here either way; the choice is made
  in slice R3-2.

### 3. Scope

In:

- TOTP enrolment, login step-up, and recovery codes for admins
  (required) with the same engine reachable by any active account the
  policy later names; admin reset of another admin's device; the
  `fold user reset-2fa` break-glass command; audit rows throughout.
- Admin people CSV export using exactly R2's import columns, so an
  export re-imports into a fresh Fold.
- Own-data JSON export for the signed-in person only, after password
  re-proof.

Out:

- WebAuthn/passkeys, SMS or email codes, and "remember this device"
  cookies. PLAN names TOTP only; a trusted-device cookie would soften
  the very control this item adds.
- Required TOTP for members, leaders, or teens. The `MustUseTOTP`
  policy decision is one line; widening it later is a product call,
  not a rebuild. Member self-service enrolment UI is also out of this
  slice set unless it comes free from the admin screens (open
  question 2).
- Groups and events CSV export. PLAN names them on the same line
  (`docs/plan/PLAN.md:1053`) but the audit item is people/own-data; the
  round-trip people file is the lock-in risk. Groups/events export is
  a later brief that reuses the people pattern.
- An admin downloading another person's own-data JSON. Admins already
  have the audited contact-history export for one person
  (`api.go:589`); a takeout of someone else's whole record is the
  separate legal-export design in PLAN 10.7
  (`docs/plan/PLAN.md:1152`), not this item.
- A ZIP of the avatar bytes in the own-data export. The JSON carries
  the avatar URL; bundling bytes is a later nicety.

### 4. Slices (in order, each shippable on its own)

Slice R3-1 — database: user TOTP columns and recovery codes.

- New goose migration (next free number): on `users`, add
  `totp_secret_enc text NOT NULL DEFAULT ''` (sealed, never the raw
  secret), `totp_pending_secret_enc text NOT NULL DEFAULT ''` (an
  enrolment that is not yet confirmed by a first code),
  `totp_enabled_at timestamptz`, and
  `totp_last_step bigint NOT NULL DEFAULT -1` (the last accepted
  30-second counter, so one code cannot be replayed inside its
  window). New table `user_totp_recovery_codes` (`id`, `user_id`
  references `users(id)` ON DELETE CASCADE, `code_hash`, `used_at`,
  `created_at`; index on `user_id` where `used_at IS NULL`). Matching
  fields on `User` after `SessionVersion` (`models/models.go:162`), a
  `TOTPRecoveryCode` model near `AccountToken`
  (`models/models.go:560`), and an entry in `models.All()`
  (`models/models.go:1030`).
- Recovery codes are bcrypt-hashed at the same cost as passwords
  (`PasswordCost`, `models/models.go:185`), not `hashToken`: a typed
  code has far less entropy than a 256-bit token (`tokens.go:30`), so a
  plain SHA-256 would fall to an offline guess against a leaked
  database. Format: ten codes, each 8 characters from a 32-symbol
  alphabet shown as `XXXX-XXXX`; the dash and case are ignored at
  entry. Checking one compares against the unused hashes in id order
  and claims the match atomically
  (`UPDATE ... WHERE id = ? AND used_at IS NULL`, the shape in
  `account_security.go:91`).

Slice R3-2 — the TOTP engine (pure, fake-clock tested).

- Decision: write it on the standard library. RFC 4226/6238 over
  `crypto/hmac` + `crypto/sha1`, Base32 secrets from `encoding/base32`,
  constant-time compare from `crypto/subtle`, is roughly 80 lines and
  pinned by the RFC test vectors. A library (such as `pquerna/otp`)
  adds a dependency chain and its own QR image code for the same math;
  nothing in `go.mod` pulls it today, and the stdlib path keeps
  `CGO_ENABLED=0` trivially true. If review prefers the library, it is
  a one-package swap behind this slice's functions.
- New `internal/totp/totp.go`: `NewSecret() (string, error)` (20 random
  bytes, Base32 without padding), `Code(secret string, at time.Time)
  (string, int64, error)`, `Validate(secret, code string, at
  time.Time, lastStep int64) (step int64, ok bool)`. The clock is a
  parameter (PLAN asks for fake-clock tests,
  `docs/plan/PLAN.md:2018`); skew is one step either side (±30
  seconds); a step at or below `lastStep` never validates again.
- Sealing reuses the `push_keys.go` pattern with its own domain: a
  `totpCipher` built from SHA-256 of `"fold:totp:v1:"` +
  `SESSION_SECRET` (compare `push_keys.go:52`) and a per-user AAD of
  `totp-secret:<userID>` (compare `push_keys.go:41`), so a ciphertext
  lifted from one row cannot be opened as another person's. Extract
  the shared seal/open helpers rather than copying them; the VAPID
  label and AAD stay untouched. An undecryptable secret (the
  `SESSION_SECRET` changed) is the `errVAPIDUnreadable` shape
  (`push_keys.go:46`): login treats that account as unenrolled, writes
  one audit row and an admin bell notice, and the person re-enrols or
  uses a recovery code (bcrypt hashes do not depend on the secret).

Slice R3-3 — policy and the session state machine.

- `internal/policy`, next to `IsAdmin` (`internal/policy/policy.go:367`):
  `MustUseTOTP(p *Person) bool` (admins today),
  `CanEnrollTOTP(p *Person) bool` (any signed-in person the handler has
  loaded as active), `CanResetTOTP(p, target *Person) bool` (an admin,
  never themselves — PLAN gives the self case to recovery codes and
  the CLI, `docs/plan/PLAN.md:2012`), `CanExportPeople(p *Person)
  bool` (admin), and `CanExportOwnData(p *Person, subjectID uint)
  bool` (self only; deliberately no admin branch, see scope).
- Sessions gain one value, `totpState`, beside the two `signInSession`
  writes today (`api_auth.go:562`): absent/`"full"` means a completed
  sign-in, `"pending"` means the password passed and a code is owed
  (with `totpPendingAt`, a 5-minute deadline), `"enroll"` means an
  admin who owes enrolment. Fail closed in the loader:
  `authenticatedSessionUser` (`api.go:347`) returns not-signed-in for
  `"pending"` and `"enroll"`, and two small new middlewares accept
  those states for exactly the step-up and enrolment routes. Existing
  routes need no changes because a half-finished login is nobody as
  far as they can tell. The pending/enrol states still carry `userID`
  + `sessionVersion`, revalidated fresh at every step
  (`api.go:365`), so a password change or disable in between kills the
  attempt.
- Enforcement is the PLAN rule with no calendar grace: an admin who
  has not enrolled lands in `"enroll"` and cannot reach any other API
  route (`docs/plan/PLAN.md:2012`, `docs/plan/PLAN.md:2014`). There is
  no "skip for 14 days" copy and no admin switch to turn the
  requirement off. Promoting someone to admin already ends their other
  sessions through the credential epoch (`account_security.go:129` is
  the password case; the role-change path in `apiAdminUpdateUser`
  must be checked and pinned by a test).

Slice R3-4 — enrolment and step-up API.

- `apiLogin` (`api_auth.go:31`): after the password passes and the
  counters are settled (`api_auth.go:75` to `api_auth.go:79`), branch
  before `signInSession`. TOTP enabled: write a `"pending"` session
  and answer `{"totpRequired": true}` (no user object). Admin without
  TOTP where `MustUseTOTP` holds: write an `"enroll"` session and
  answer `{"totpEnrollRequired": true}`. Otherwise today's answer
  (`api_auth.go:90`). The pending-teen path (`api_auth.go:66`) is
  unchanged; only a full `CanSignIn` reaches this branch.
- `POST /api/v1/auth/login/totp` with the pending-state middleware and
  body `{"code": "123456"}` or `{"recoveryCode": "XXXX-XXXX"}`. It
  reserves `totp:user:<id>` + IP attempts with `reserveLoginAttempt`
  before any code is read (the `reauthenticate` shape at
  `api_auth.go:201`), locks the user row under the contact gate
  (`lockCurrentCredentials`-grade freshness without the password),
  validates against the running clock and `totp_last_step`, and in one
  transaction stores the new `totp_last_step` (or claims the recovery
  code and audits `totp.recovery_used`), then completes the session
  exactly as login does. A wrong code is one uniform 401 — sign-in
  keeps its lockout message, which is the documented exception to the
  no-visible-rate-limit rule; nothing here reveals whether the code
  was stale, replayed, or simply wrong.
- Enrolment, signed-in (full or `"enroll"` state):
  - `GET /api/v1/me/totp` → `{enabled, enabledAt,
    recoveryCodesRemaining, required}` (see shapes).
  - `POST /api/v1/me/totp/enroll` `{current}`: after `reauthenticate`,
    generate a secret, seal it into `totp_pending_secret_enc`, and
    answer the secret and otpauth URI once (issuer is the site name,
    account is the email). Nothing is enabled yet. Starting again
    replaces the pending secret and kills the earlier one.
  - `POST /api/v1/me/totp/confirm` `{code}`: validate against the
    pending secret, move it to `totp_secret_enc`, set
    `totp_enabled_at`, mint ten recovery codes, bump
    `session_version`, audit `totp.enrolled` — one transaction, the
    `apiChangePassword` layout (`api_auth.go:253` to
    `api_auth.go:287`), including rewriting this browser's cookie to
    the new epoch so only the other sessions end. The plaintext codes
    appear in this one response and nowhere else ever.
  - `POST /api/v1/me/totp/recovery-codes` `{current, code}`: password
    plus a current TOTP code, then replace the whole unused set and
    show the new ten once. Audit `totp.recovery_regenerated`.
  - `POST /api/v1/me/totp/disable` `{current, code}`: allowed only
    when `MustUseTOTP` is false for the person; an admin gets the
    actionable refusal in §6 — they re-enrol, or another admin or the
    CLI resets them. Disabling clears the secret, pending secret,
    codes, and `totp_last_step`, bumps `session_version`, and audits
    `totp.disabled`.
- Admin reset: `POST /api/v1/admin/users/:id/totp/reset` `{current}`
  (the actor's own password, via `reauthenticate`), gated by
  `policy.CanResetTOTP`, under the admin routes near
  `POST /admin/users/:id/password` (`api.go:806`, whose handler
  `apiAdminResetPassword` at `api_admin.go:916` is the layout to
  copy, including `lockConsentUsers` and the `SessionVersion` recheck
  at `api_admin.go:942`). It clears the same fields as disable, bumps
  the target's `session_version`, audits `totp.reset` with
  `byAdmin: true`, and writes the target a bell notice and the admins
  an admin notice in the same transaction. The target's next sign-in
  lands in enrolment.

Slice R3-5 — CLI break-glass.

- `fold user reset-2fa <email>` in `cli.go`: a `user` case in `run`
  (`cli.go:50`) and a usage line (`cli.go:25`). It needs only
  `DATABASE_URL` (clearing columns never decrypts anything, so no
  `SESSION_SECRET` — the same load-only-what-it-needs rule as
  `cli.go:40`), opens the database the `runPush` way (`cli.go:188`),
  and in one transaction finds the user by normalized email with
  ordinary scoping (so a soft-deleted address answers "no such
  account" rather than reviving anything), clears the four TOTP fields and the
  recovery-code rows, bumps `session_version`, and audits
  `totp.reset` with a nil actor and meta `viaCli: true`. Unknown
  email exits 1 with a plain message; `--json` answers
  `{"email": "...", "totpCleared": true}`. It works for any account
  carrying TOTP, prints the same line either way, and never prints a
  secret or a recovery code. This is the sole-admin door
  (`docs/plan/PLAN.md:2012`); the docs line that names it goes in the
  operator docs with the first-church checks (§10).

Slice R3-6 — people CSV export (admin, synchronous).

- Decision: synchronous, on the contact-history pattern, not a job. A
  church's people list is hundreds to a few thousand rows; the
  temp-file materialization at `api_contact_history.go:399` already
  bounds memory, and a stored download would add an expiring-link
  capability holding everyone's phone numbers for no gain at this
  scale.
- `GET /api/v1/admin/people.csv`, `apiAuth()` + `apiAdmin()` and
  `policy.CanExportPeople`, next to the contact-history export route
  (`api.go:589`). Columns are exactly R2's import header
  (`name`, `email`, `phone`, `groups`, `birthDate` — R2 §3 and §6 in
  this file), in that order, CRLF via `encoding/csv` as at
  `api_contact_history.go:414`. `groups` is the semicolon-joined names
  of the groups the person actively belongs to (the same join the
  import writes), `birthDate` is `YYYY-MM-DD`, and every cell passes
  the formula guard — rename `contactHistoryCSVCell`
  (`api_contact_history.go:365`) to a shared `csvCell` and use it in
  both places rather than copying it.
- Rows: every non-deleted account that is not a guest
  (`active`, `invited`, `pending_consent`, `disabled` —
  `models/models.go:102`). Guests are event visitors, not church
  people, and carry no account. One repeatable-read transaction pages
  users by id, writes the file, re-locks the actor and re-checks
  `CanExportPeople` + `SessionVersion` (the linearization at
  `api_contact_history.go:455` to `api_contact_history.go:471`), and
  audits `people.exported` with the row count only — never names,
  emails, or the file contents. `Content-Disposition` filename
  `fold-people-<church date>.csv`, streamed as at
  `api_contact_history.go:477`.

Slice R3-7 — own-data JSON export (self, synchronous).

- Decision: synchronous JSON from one repeatable-read snapshot, for
  the same reason as R3-6, with one addition — the payload is a single
  person's record, built from indexed per-user queries. No job kind,
  no stored file, no expiring link. If profiling ever contradicts
  that, the job version is a new slice behind the same route, not a
  reason to build the machinery now.
- `POST /api/v1/me/export` `{current}`: `reauthenticate` first (a
  borrowed open session must not take out the whole record), then the
  hourly/daily caps through `targetAllow` under key
  `export:user:<id>` (`api_auth.go:383`; over the cap is the
  actionable refusal in §6 — this is a signed-in personal action, so
  it is shown, never the silent public-form treatment). The handler
  calls `policy.CanExportOwnData` with the subject id from the session
  user, so the rule is table-tested even though the route is `/me`.
  Response: `Content-Disposition: attachment; filename="fold-my-data-<church date>.json"`,
  `application/json`, `no-store` already set for the API group.
  Audit `own_data.exported` with section counts.
- Contents (allow-list of sections; anything not listed stays out):
  - `account`: id, email, `PublicName()` source fields (name,
    displayName), phone, role, status, accountType, birthDate,
    timezone, hide-contact flag, avatar URL, createdAt. Never the
    password hash, TOTP secrets, or `SessionVersion`.
  - `memberships`: group id + name, role, status, joinedAt.
  - `registrations`: occurrence id, event title, group name where the
    event has one the person may see about themselves (their own
    registration, so it may), start instant, status, attendanceMode,
    guest/childcare counts, source. Never `ManageTokenHash`.
  - `notifications`: the person's own rows, title/body included —
    they were written for this recipient.
  - `consents` and (for a teen) their `teenConsents`; `guardianLinks`
    in both directions with the other side named by id + public name.
  - `household`: their household row and its household people when the
    person is a guardian/adult of it — care notes included, since a
    guardian authored them. This is the one section holding other
    people's data; the copy on the download button says so.
  - `accountRequests` where they are the requester or the approved
    user; `contactRequests`/`contactGrants` where they are requester
    or subject.
  - `messages`: authored message subjects/bodies; `messageRecipients`
    rows for themselves without the stored `Address`.
  - `calendarFeed`: scope and timestamps only, never `TokenHash` or a
    reconstructed link. `pushDevices`: label and timestamps only,
    never endpoint or keys.
  - `audit`: rows where they are the actor or the `user` target,
    projected to action, time, and target — `MetaJSON` stays out whole,
    because meta can name other people (an invitation row records the
    invitee's email, `internal/audit/audit.go:171`).

Slice R3-8 — UI.

- Login page (`web/src/pages/Login.tsx`): on `totpRequired`, swap the
  form to a code field (6 digits, `inputMode="numeric"`,
  autocomplete `one-time-code`) with a "use a recovery code instead"
  toggle; on `totpEnrollRequired`, route to the enrolment page. Copy
  says what is needed and where the codes live ("the sheet you saved
  when you turned this on"); a wrong code keeps the typed value out of
  the DOM after submit and shows the `ERROR_COPY` line.
- Enrolment and status live on the profile page as a "Two-step
  sign-in" section (`web/src/pages/Profile.tsx`, routed
  `/profile/:section?` at `web/src/App.tsx:102`), also reachable in
  the restricted state (the SPA renders just this section plus sign
  out when `/auth/me` reports the enrol-required state; `GET /me/totp`
  is on the enrolment allow-list). Steps shown next to the controls:
  enter your password → type the secret into your app (the Base32 key
  shown once in a copyable field, plus the otpauth link for phones
  that offer "add account from link") → enter the app's code → save
  the ten recovery codes (download/print button, a checkbox "I have
  saved these" before Done). No QR in this slice — no QR package is in
  the bundle (`web/package.json:2`) and the entry-chunk budget guard
  applies; a bundled QR renderer is open question 1, not a silent
  addition. Manual entry is the path every authenticator has.
- Admin Users tab (`web/src/pages/Admin.tsx`, `UsersTab` at line 899):
  a per-admin "Reset two-step sign-in" action behind a confirm that
  asks for the actor's own password and says the person signs in with
  a password only until they enrol again; the users list gains a
  two-step state chip for admins (On / Not set up — words, never color
  alone). An "Export people (CSV)" secondary action sits with the R2
  import section and downloads the file; its dialog copy names what
  is in the file and that the export is audited.
- Own-data: a "Download your data" secondary action on the profile
  page, password field in the confirm dialog, copy listing the
  sections and the household note from slice R3-7.

### 5. Step-up and export rules (the decisions in one place)

- Who must: admins only, from the day this lands — no calendar grace,
  no per-church off switch (`policy.MustUseTOTP`). Unenrolled admins
  get the enrol-only session, never a dismissible warning.
- Who may: any active account may enrol once the card renders for
  them (open question 2); whoever enrols, member or admin, steps up
  the same way. Guests, invited, disabled, and pending-consent
  accounts never enrol — they cannot complete a sign-in to step up
  from (`models/models.go:223`).
- Who resets whom: yourself via re-enrol or a recovery code; another
  admin via `CanResetTOTP` (never self); the server operator via the
  CLI. A reset target is told, and every path audits.
- Who exports whom: people CSV is admin-only (`CanExportPeople`);
  own-data JSON is the account themself only (`CanExportOwnData`
  has no admin branch). Nobody exports a third person's record in
  this item.

### 6. Data / API shapes

Step-up and enrolment answers:

```json
POST /auth/login  ->  {"totpRequired": true}
                  |   {"totpEnrollRequired": true}
                  |   {"user": { ... }, "canCompose": true}
POST /auth/login/totp {"code": "123456"}
                  ->  {"user": { ... }, "canCompose": true}
GET  /me/totp     ->  {"enabled": true,
                       "enabledAt": "2026-11-02T10:00:00Z",
                       "recoveryCodesRemaining": 7,
                       "required": true}
POST /me/totp/enroll {"current": "..."}
                  ->  {"secret": "JBSWY3DPEHPK3PXP",
                       "otpauthUri": "otpauth://totp/...",
                       "issuer": "Grace Church", "account": "a@b.org"}
POST /me/totp/confirm {"code": "123456"}
                  ->  {"recoveryCodes": ["ABCD-EFGH", "..."]}
```

- The otpauth URI: `otpauth://totp/<issuer>:<email>?secret=...&issuer=...&algorithm=SHA1&digits=6&period=30`,
  both labels URL-escaped. The raw secret is returned exactly once, by
  `/me/totp/enroll`; it is never logged (`jobs.CleanError`-grade care
  is not enough — it simply never enters an error or audit row), never
  stored unsealed, and never shown again after confirm.
- New actionable API errors (code in `api_errors.go` + `ERROR_COPY`):
  `totp_code` "that code doesn't match; wait for a fresh code and try
  again", `totp_expired` "that sign-in took too long; sign in again",
  `totp_required` "your church keeps admin accounts behind two-step
  sign-in, so you can't turn this off; an admin can reset it for you",
  `totp_not_started` "start setup again to get a new key",
  `export_limit` "you've downloaded your data a few times today; try
  again tomorrow". Wrong-code and throttle answers at the login step
  reuse the existing 401/429 messages.
- Audit actions (new constants in `internal/audit/audit.go`, near the
  password group at `internal/audit/audit.go:190`): `totp.enrolled`,
  `totp.disabled`, `totp.reset` (meta `byAdmin` or `viaCli`),
  `totp.recovery_used`, `totp.recovery_regenerated` (meta
  `remaining`), `people.exported` (meta `count`), `own_data.exported`
  (meta section counts). No secrets, codes, emails, or file names in
  any meta.
- Own-data document header: `{"format": "fold-own-data",
  "formatVersion": 1, "exportedAt": ..., "church": "<site name>",
  "account": {...}, ...}`. Adding a section later is a
  `formatVersion` bump, never a silent new key.
- New SPA types `TOTPStatus`, `TOTPEnrollStart` get `tsContracts`
  pairs; the login response becomes a named Go struct if it is not one
  already, so the two new flags are typed on both sides.

### 7. Tests

Extend: `api_auth_test.go` (login state machine), `api_admin_test.go`
(admin reset route), `cli_test.go` (the `runPush` cases there, e.g.
`cli_test.go:154`, are the shape), `internal/policy` table tests,
`api_contact_history_test.go` (the renamed `csvCell` keeps its cases).

New, next to their source:

- `internal/totp/totp_test.go`: RFC 4226/6238 vectors with a fake
  clock; skew boundaries (the step before/after passes, two steps away
  fails); replay (an accepted step fails at every later time);
  malformed secrets and codes never panic; generated secrets validate.
- `totp_test.go` (root package): `TestTOTPEnrollConfirmFlow` (pending
  secret unreachable for login until confirmed; codes returned once;
  second enrol start kills the first secret),
  `TestLoginTOTPRequiredPendingSessionSeesNothing` (a pending cookie
  gets 401 from an ordinary route — the fail-closed loader),
  `TestLoginTOTPWrongCodeThrottled` (ten wrong codes lock like
  passwords; timing tests call `realPasswordCost(t)` first, per the
  repo rules), `TestLoginRecoveryCodeWorksOnce` (parallel double-spend
  of one code succeeds once), `TestAdminWithoutTOTPEnrollOnly`,
  `TestAdminResetTOTPNotSelf`, `TestTOTPChangesEndOtherSessions`
  (enrol, disable, admin reset, CLI reset),
  `TestTOTPSecretUnreadableFallsToEnroll` (rotate the test
  `SESSION_SECRET`: no panic, enrol-state login, recovery code still
  works), `TestPromoteToAdminEndsSessions` (pins the role-change
  epoch checked in slice R3-3).
- `export_people_test.go`: `TestPeopleExportRoundTripsR2Columns`
  (header matched against the import's accepted set; a seeded church
  exported, parsed by the R2 reader path, classifies every row as a
  clean invite/match), `TestPeopleExportGuardAndFormulaCells`,
  `TestPeopleExportAdminOnly`, `TestPeopleExportExcludesGuestsAndDeleted`.
- `export_own_test.go`: `TestOwnDataExportSections` (seeded maximal
  person; decode and assert each section, and assert no password hash,
  token hash, push endpoint, or TOTP material appears anywhere in the
  bytes), `TestOwnDataExportNeedsPassword`, `TestOwnDataExportRateLimited`,
  `TestOwnDataExportOmitsOthersMeta` (an audit row naming another
  person's email in meta reaches the export without the meta).
- CLI `TestUserReset2FAClearsAndAudits` in `cli_test.go`, including
  `--json` output and the unknown-address exit.
- Web: `Login.test.tsx` (code step, recovery toggle, enrol redirect),
  `Profile.test.tsx` (enrol steps and code-save gate); e2e
  `web/e2e/totp.spec.ts`: enrol as the seeded admin (the spec computes
  codes from the displayed secret itself), sign out, sign in with
  password + code, spend one recovery code, see it refused twice.
  People export downloads in the admin e2e; own-data download in a
  profile e2e or the same spec.

### 8. Acceptance checklist

- [ ] An unenrolled admin reaches only enrolment and sign-out; every
      other API route answers 401 to the enrol-state cookie, and a
      pending-state cookie is treated as signed out everywhere but the
      step-up route.
- [ ] The right code signs in (including ±30 seconds of clock skew);
      the same code a second time, a stale code, and an 11th wrong
      code in 15 minutes all fail without telling the caller which.
- [ ] The stored secret is sealed; a database dump alone yields
      neither a usable secret nor usable recovery codes, and neither
      value ever appears in a log, an audit row, or any response after
      its one showing.
- [ ] Enrolling, regenerating codes, disabling, an admin reset, and
      the CLI reset each end the subject's other sessions and write an
      audit row; a recovery-code sign-in is audited too.
- [ ] A sole admin locked out of their authenticator gets back in with
      a recovery code or `fold user reset-2fa`, with the server never
      stopped for it.
- [ ] People export round-trips: feeding the file to the R2 import
      preview classifies every row cleanly, and a name beginning with
      `=` cannot run a formula in a spreadsheet.
- [ ] Own-data export needs the person's own password, names nobody's
      secrets, and cannot be fetched for someone else by any account,
      admins included.
- [ ] `make test`, `cd web && npm test`, `make e2e`, and the quick
      checks (`go vet`, `make lint-go`, `tsc --noEmit`, `knip`) pass.
- [ ] PLAN 10.1/10.6 and M4.9 checkboxes and the hardening list are
      ticked in the same PR; the real-device authenticator check is a
      line in `docs/FIRST-CHURCH-CHECKS.md`.

### 9. Risks, rollback, security/privacy

- Lockout is the operational risk, not bypass. Three doors back in, in
  order: a recovery code, another admin's reset, `fold user reset-2fa`
  on the server. The enrolment copy pushes saving the codes before
  finishing, and the PR adds the locked-out-admin runbook line to the
  operator docs. Staff phones with drifting clocks are handled by the
  ±1-step window; wider drift is a reset, not a wider window.
- The pending session is a half-credential: it is signed-cookie state
  with a 5-minute deadline and no API reach, and the step-up route
  throttles on the account itself, so a stolen pending cookie is worth
  at most ten online guesses.
- A `SESSION_SECRET` change already signs everyone out once
  (`config.go:132`); from this item it also makes every TOTP secret
  unreadable. Recovery codes and re-enrolment still work, admins are
  told once, and the rotation runbook (its own audit item) should
  warn that admins will re-enrol.
- The people CSV is the church's address book in one file. It is
  admin-only, audited with its row count, `no-store`, and never
  written anywhere but the scratch temp file. The confirm dialog
  copy says to keep it somewhere private.
- Rollback: the TOTP columns and table are inert if the login branch
  is reverted; an enrolled admin on a rolled-back build signs in with
  a password only (name this in the release notes — it weakens the
  control but never locks anyone out). Exports are additive routes;
  removing them changes no stored data.

### 10. Dependencies

- R2's column names are the people export's contract; land R2's header
  set first, or agree them in review (R2 §10 in this file asks the
  same). The two items can otherwise land in either order.
- R4 must erase what this item adds: the four `users` columns and
  `user_totp_recovery_codes` belong in R4's inventory if R3 lands
  first. Deleting an account must also revoke its calendar feed and
  devices, which is R4's table, not repeated here.
- The first-church list gains: enroll with a real phone authenticator
  (the one the church staff already carry), and open a people export
  in the spreadsheet the church actually uses.

### 11. Open questions for Grier (each with a default that unblocks work)

1. Should enrolment show a QR code? Recommended default: not in the
   first slice — no QR package is bundled today, manual key entry is
   available in every authenticator, and the otpauth link covers taps.
   Add a small lazy-loaded renderer in a follow-up if the first
   church's admins stumble; the API already returns everything it
   needs.
2. Should members see the enrolment card too? Recommended default: the
   profile section renders for everyone signed in (the engine is
   general and the requirement flag comes from `MustUseTOTP`), but
   only admins are required, nudged, or chip-labelled in Admin. If
   review wants admins only, gate the card on `required` and the
   member rollout becomes its own decision later.
3. Should the people export carry columns the import cannot read back
   (role, status, joined date)? Recommended default: no — one column
   set that round-trips exactly. A reporting export is a different
   file with its own brief.
4. Should a guardian be able to export a child's or teen's record?
   Recommended default: not in this item. A teen exports their own; a
   household child's data reaches the guardian through the household
   section of the guardian's own export. A per-child takeout is a
   safeguarding-shaped decision for later.

---

## R4 — Make account deletion actually erase personal data

- Priority: first-church readiness (audit build order step 4,
  `docs/AUDIT-2026-11-01.md:265`)
- Effort: M (audit estimate; the inventory is a full slice of it)
- Status: Approved 2026-11-01 (audit). M7.4 claimed Nov 3, 2026; slice
  R4-0 (the inventory) requires independent privacy and safeguarding review
  and a reviewed R4-0 PR merge before implementation; the milestone is incomplete.
- GitHub issue: none known at brief time. PLAN section 10.1 states the
  promise being repaired (`docs/plan/PLAN.md:1054`). The audit records
  no decision reversed.

> **R4-0 update (Nov 3, 2026).** The R4-0 inventory is
> [`docs/plan/M7.4-ERASURE-INVENTORY.md`](../../M7.4-ERASURE-INVENTORY.md). It is a
> **inventory requiring independent privacy and safeguarding sign-off in the
> R4-0 pull request and a reviewed PR merge before implementation**; no erasure
> code or later slice starts before those conditions are met. Priority: PLAN, `AGENTS.md`
> and the owner decisions recorded below come first. Where this brief's audit-time
> counts or citations disagree with the source tree at this commit, the source fact
> is used and the inventory records the correction; code never overrides a PLAN rule
> or an owner decision. This note changes no decision and leaves the text below as
> written at audit time. What drifted, and what the inventory proposes (the
> conditional review record and remaining gates are in its section 15):
>
> - **37 tables, not 38.** R9 S4 folded `occurrence_accesses` into `registrations`
>   (`access_*` columns, DEC-138). `models.All()` and the baseline both list 37.
>   Wherever this brief says 38 (sections 2, 4, 7, 8) read 37; the DEC-144 wording
>   stays historical.
> - **Line numbers are stale.** `apiAdminDeleteUser` is `api_admin.go:828`, the
>   baseline replaced the 63-file chain, and the `migrations/0000N_*.sql` citations
>   in section 2 now live in `migrations/00001_baseline.sql` (the inventory cites
>   them). `occurrence_accesses` rows no longer exist; exact-date access
>   provenance is on `registrations` and its audit snapshots.
> - **Authorization snapshots keep their metadata.** The audit rows
>   `occurrence_access.added` and `occurrence_access.ended` are read by the
>   `registration_access_provenance()` trigger and by the Go access readers;
>   `occurrence.blocked` is written the same way and shown in the admin contact
>   history, but no trigger or authority check reads it. Section 4's R4-4 must
>   leave all three rows' `meta_json` byte-identical. The `ip` column is read by
>   nothing, so the draft proposes blanking only the erased person's own address
>   there (inventory 6.5 to 6.7, O-5), pending review.
> - **Memberships are hard-deleted today** (`api_handoff.go:179`); section 5's "A /
>   none left active" is not what the code does. `AGENTS.md` says to preserve it.
> - **Contact requests cannot be scrubbed by UPDATE** for terminal add-purpose rows
>   (trigger `add_request_identity`), and DEC-141 froze that table (amended Nov 3, 2026, below). Section 4's
>   "`ContactRequest.Reason` and add-request childcare notes are cleared where this
>   person is requester or subject" splits into separate cells by field, purpose,
>   status and party (inventory T07): `reason` can be blanked on `contact` rows and
>   on a *pending* add row before the reconciliation ends it; a terminal add row's
>   `reason` and every add row's `add_childcare_ages_note` cannot be updated at all.
>   Those cells were an **owner-only gate** (inventory O-7). *The owner answered on
>   Nov 3, 2026* (verbatim): "Allow a narrow exception to scrub only the erased person’s
>   identifying text while preserving request decisions and provenance (Recommended)".
>   `PLAN.md` DEC-141 carries the dated amendment (no new DEC number). That settles
>   authority for one erasure-only exception to the `add_request_identity` trigger and
>   nothing else: it is not a redesign of the contact system, does not lift the rest of
>   the freeze, and does not authorize clearing non-identifying data, another person's
>   evidence, or text only because its author was erased. Which fields and rows count
>   as the erased person's identifying text (the terminal add-request `reason`, and the
>   add-request ages note about the subject's children, each decided on field-specific
>   evidence), and the trigger and migration shape, are proposals in inventory T07
>   (G-7a to G-7d) that need privacy/security and schema (DEC-138) review and exact tests
>   first. Until that review and the R4-0 sign-off are recorded, the `contact_requests`
>   statements of R4-2a do not start and nothing is implemented under O-7. The older
>   wording that no answer is recorded and a reviewer alone cannot waive the freeze is
>   historical.
> - **Conversions that have not completed.** Section 4's R4-3 (and the "never deletes
>   the HouseholdPerson it was converted from" bullet) assumed `converted_user_id`
>   marks the duplicate. An approved teen conversion that has not completed has
>   `converted_user_id` NULL but the same individual as `account_requests.approved_user_id`
>   + `household_person_id`; admin delete reaches it. The inventory (3.1 `PERS`, T15,
>   T12, O-18) treats it as the same person under owner decision 2 without a new question.
> - **Households.** Section 4's "the household and its people are not deleted" stays true
>   for people. The household's own `name` and `address` are anonymized only when the
>   erased individual is all it describes (no other seat of any status and no other
>   person row, soft-deleted included); a shared or ambiguous household keeps them
>   (inventory T16, O-9). A household request already past authorization when the erasure
>   commits (household update, person add/update/delete, first-use creation) could restore
>   a name, address, person or seat; R4-3 revalidates the acting account, session and seat
>   under the user lock in those writers, with real preloaded-request barrier tests in both
>   orders (inventory 9.6, F-10).
> - **Address-keyed selectors need id links first.** Section 4's address-owned rows
>   (invitations, requests) are reached by the address held at erasure *and* by retained
>   id links (`invitation.accepted` `meta.userId`, `approved_user_id`,
>   `household_person_id`), so an address change before erasure does not hide them, and
>   a row accepted by another account is never matched by address (inventory 3 rule 6,
>   3.1, T17). `guardian:addr:<address>` throttle keys name the address a guardian
>   *searched for*, not the guardian's (T19).
> - **Stored IP addresses belong to whoever made the request** (inventory 3.2, 6.5, T01),
>   not to `actor_id` or `requested_by`. The approving admin of a household conversion is
>   retained as `account_requests.decided_by`, so the addresses written in that approval
>   are that admin's. Two of them need no audit history: `account.approved` and a teen's
>   `guardian_link.requested` with `source = household_conversion`. The adult person's
>   transfer rows are **two different shapes**: `household.person_converted` stores
>   `accountType` and `transferCount`; each `registration.changed` stores only `personId`,
>   `userId`, `formerRegisteredBy`, `clearedChildcareRequested` and `invitationId`. They
>   are reached by the exact tuple `(personId, invitationId, userId)` read from the erased
>   admin's own request. The `household.person_converted` row must be unique, `accountType =
>   adult` and have no age-rollover marker (`household.person_updated`, `target_type =
>   household_person`), the `registration.changed` rows must number its `transferCount`, and
>   the audit history must be established (gate G-8) to have been written by binaries that
>   have the staged teen conversion and the marker, since no row records the writing binary.
>   Uniqueness is a property of the writers, not of `audit_logs`, so R4-4 verifies it at
>   erasure time, and it does so in two steps: first **discovery** by text-equal identity
>   keys and a same-household fallback for missing or non-number identity keys, then
>   **shape validation** of every discovered row. A
>   malformed duplicate (for example a second `household.person_converted` whose
>   `householdId` disagrees with its target, or a `registration.changed` missing a shape key)
>   therefore retains the entire discovered group instead of being skipped while the valid
>   rows are scrubbed. The report gives the actual discovered, retained and blanked counts,
>   never an assumed `1 + transferCount`, and claims only what those bounded rules find.
>   Rows without a matching key or household fallback, and rows excluded by the action or
>   target filters, can remain unreachable. Ordinary event-registration audit rows stay
>   outside the conversion candidates even when their ids match; a malformed transfer row
>   with that target type is likewise unreachable residue. Anything not proven is
>   retained and reported with a reason code; by default (G-8 undispositioned; neither
>   proposed route is verified or approved) the whole discovered group is retained, which
>   leaves an erased approving admin's address on it and is not complete erasure. The selectors never read
>   `household_people.kind`, which the age job changes before completion. A source-guard test
>   keeps the writers and key sets true. The rows written when a staged teen conversion
>   completes *later* (before the birthday by a link-deciding or enabling admin, or a
>   consenting admin or guardian; after it by the job with an empty address, or by the admin
>   who enables a suspended account) have no stored caller. They are retained and reported
>   (O-19); the draft does not claim that every address of an erased
>   administrator is scrubbed. **That residue is a completion gate, not an accepted
>   limitation (inventory 6.5.2, gate G-9, O-20).** Slices may merge as staged progress, but
>   M7.4 is not complete while those personal IP addresses can remain unless the owner
>   authorizes an exact, named PLAN disposition (DEC-144 allows only an anonymous tombstone;
>   a reviewer's acceptance cannot waive it); **G-9 is open**. A forward-only caller key is not shown to be
>   the only remedy: the inventory lists other routes (a named retention disposition,
>   erasure-triggered or population-wide minimization of the unread `ip` field, coarsening,
>   a forward key), with whose data each would change and what each lacks. *Owner scope
>   (Nov 3, 2026, evening, inventory section 1 decision 5):* the owner answered "Yes, clear
>   those IPs (Recommended)" to clearing the IP on identified household-conversion audit
>   entries when the erased person may have caused the conversion, even if it might belong
>   to another living participant, provided it is not essential safeguarding evidence, with
>   decisions, actors, dates and provenance preserved. That authorizes only the concept of
>   option P2 in exactly that scope: only the `ip` field, nothing wider. It is not an
>   approved design; it needs the safeguarding reviewer's per-action judgment and an
>   independent review of the R4-4 design and tests; it does not authorize blanket deletion
>   of audit IPs or any change to the separate step rows of living people; it does not claim
>   the candidate set, history or action-filter approach is complete or reaches malformed or
>   unreachable historical rows; and it does not establish G-8. The other routes stay
>   unapproved, and R4-4's default (retain and report the whole discovered group) is unchanged
>   until a reviewed design exists.
>   R4-4 builds its primary fixtures through the real handlers
>   with distinct inviter, approving admin, enabling admin, subject and guardian, including
>   birthday completion direct and suspended then enabled, plus labelled fallback fixtures
>   for missing, duplicate, count-mismatched, malformed-duplicate (wrong-household C, T
>   missing a shape key, missing or mistyped identity metadata including all three
>   wrong-typed keys at the household, unrelated rows and the action/target limits) and
>   older-binary history, with exact before and after comparisons of selector metadata and
>   the R9 authorization snapshots.
> - **A consent write can arrive after the tombstone** from a request that loaded the
>   user first (old unsubscribe link, signed settings, push toggles); closing it is part
>   of R4-2d, in `setConsent` (inventory 7.2, F-9). Section 4's R4-2 "Consent rows with IP,
>   user agent, and evidence text cleared" is not complete without it.
> - **Slice list as planned (accepted by the autoflow plan review, nothing
>   implemented):** R4-0, R4-N (notice locks), R4-H
>   (sole-host replacement before deletion, owner decision), R4-5 (durable
>   `avatar.cleanup` job, replacing this brief's `erasure.blob_cleanup`), R4-S then
>   conditional R4-SC (suppression hashing), R4-1, R4-2a, R4-2d (ordinary consent
>   scrub and opt-out), R4-3, R4-4, R4-6. One PR per slice; the inventory's section
>   12 maps rows to slices.
> - **Owner decisions recorded Nov 3, 2026, verbatim:** (1) "Scrub personal details where
>   technically safe, while retaining the decision and essential safeguarding evidence
>   (Recommended)"; (2) "Anonymize the duplicate person’s details (Recommended)", under
>   which the accepted plan keeps `converted_user_id` (superseding section 4's R4-3 "clear
>   `converted_user_id`") and applies the answer to the staged case too; (3) "Require the
>   admin to choose a replacement before deleting the account" (the only host of a
>   church-wide event). These three answers are not re-asked.
> - **PLAN 10.1 outranks this brief.** Section 4's R4-2 default of keeping plaintext
>   suppression meanwhile is a disclosed temporary stage, not approval; opt-out
>   records also keep a hashed address under PLAN 10.1, and section 6's "must not
>   reserve, suppress, or throttle a returning address" does not replace that. The
>   conflict and its OPEN dispositions are inventory sections 7 and 13.
> - **Audit keys and notification locks are traced in the inventory.** Section 6
>   there lists every audit writer (113 call sites, 119 action strings) with its
>   personal keys, and section 9.2 classifies all 33 `notify.Send` callers; they
>   replace this brief's sketches, and both are drafts until signed.
> - **Migrations:** new numbered files follow PLAN 14.1 while the baseline guard
>   `TestBaselineFileShape` is updated by the first slice that adds one; see
>   inventory section 10.

### 1. Goal / user outcome

When an admin deletes an account, the person is gone from Fold: no
name, phone, birth date, photo, or credential remains anywhere in the
live database, their sign-in links and feeds stop at once, and what
stays — an anonymous head-count on past dates and the safeguarding
record — stays deliberately, named in an inventory a reviewer signed
off before any of the erasing code was written.

### 2. Current state (evidence)

- The whole door is `apiAdminDeleteUser` (`api_admin.go:842`), reached
  at `DELETE /api/v1/admin/users/:id` (`api.go:805`). In order it:
  checks `policy.CanDemoteOrDelete` (`api_admin.go:848`; the rule at
  `internal/policy/policy.go:412` — admins only, never yourself, never
  the last admin), takes the contact gate through `contactMutation`
  (`api_admin.go:867`; `contact_lifecycle.go:49`), locks the user row
  FOR UPDATE (`api_admin.go:868`), revokes unused account tokens
  (`api_admin.go:871`; `revokeAccountTokens`,
  `account_security.go:125`), hands off sole-led groups
  (`api_admin.go:874`; `handOffLeadership`, `api_handoff.go:112`),
  rewrites the email to `deleted-<id>@deleted.invalid` and bumps
  `session_version` (`api_admin.go:879`), audits `user.deleted`
  (`api_admin.go:885`), deletes the person's notification rows
  (`api_admin.go:890`), soft-deletes the user (`api_admin.go:893`),
  and promotes waitlisted people into the freed seats
  (`api_admin.go:898`; `promoteOnDisable`, `api_registrations.go:1764`).
- Everything else on the row survives the rewrite, because the update
  names only `email` and `session_version` (`api_admin.go:879`):
  `Name` and `DisplayName` (`models/models.go:138`,
  `models/models.go:139`), `Phone` (`models/models.go:140`),
  `PasswordHash` (`models/models.go:137`), `AvatarKey`
  (`models/models.go:143`) and the blob behind it, `BirthDate`
  (`models/models.go:159`), `Timezone` (`models/models.go:165`). The
  status even stays whatever it was — an `active` row with a live
  password hash, hidden only by GORM's soft-delete scoping
  (`DeletedAt`, `models/models.go:168`). Any `Unscoped` read (two
  already exist for admin history views, `api_admin.go:218`,
  `api_admin.go:225`) sees the person whole. That is the gap the
  audit names (`docs/AUDIT-2026-11-01.md:96`).
- The blob store can delete; nothing on this path calls it.
  `storage.Store.Delete` is part of the interface
  (`storage/storage.go:28`), the local store treats a missing key as
  success (`storage/storage.go:112`), and the avatar paths already
  delete on replace and remove (`api_profile.go:500`,
  `api_profile.go:518`). Keys are server-generated and unguessable
  (`storage/storage.go:36`), which bounds — but does not close — the
  leak: the file is still served at its URL while the key is known.
- The lone-leader handoff, waitlist promotion, and the contact
  reconciliation inside `contactMutation`
  (`contact_lifecycle.go:68`) already run in this transaction and are
  not this item's to rebuild. What this item adds is every row and
  file the current code never touches.
- The universe to inventory is the 38 models in `models.All()`
  (`models/models.go:1030` to `models/models.go:1040`), plus blobs,
  plus two things that are not models: `login_throttles` keys, which
  embed the raw address (`"email:<address>"`, `api_auth.go:41`; the
  model at `models/models.go:519` is in `All()` but keyed by text, so
  no foreign key finds it), and rendered copies of the person's name
  inside other people's rows — a notification's rendered title/body
  (`models/models.go:1058`), a message body
  (`models/models.go:1145`), mail bodies in the dev outbox
  (`models/models.go:604`).
- Foreign keys decide what "delete" can even mean. The user row is
  only ever soft-deleted, so the ON DELETE CASCADEs never fire:
  notifications (`migrations/00010_notifications.sql:9`), account
  tokens (`migrations/00011_m1_2_email.sql:30`), calendar tokens
  (`migrations/00017_calendar_tokens.sql:8`), and push subscriptions
  (`migrations/00058_push_subscriptions.sql:8`) all sit live today
  until something deletes them row by row — which, notifications
  aside (`api_admin.go:890`), nothing does. A hard delete is not the
  shortcut either: memberships reference users with no cascade
  (`migrations/00001_baseline.sql:69`), as do households,
  household people, and guardian links
  (`migrations/00022_households.sql:15`,
  `migrations/00022_households.sql:41`,
  `migrations/00022_households.sql:52`) and teen consent
  (`migrations/00026_teen_consent.sql:7`). Anonymise-in-place plus the
  existing soft delete is the only shape that fits both the foreign
  keys and the PLAN promise to keep anonymous registrations for
  attendance counts (`docs/plan/PLAN.md:1054`).
- Audit rows are built to outlive people: `AuditLog` has no foreign
  keys on purpose (`models/models.go:502`, comment at
  `models/models.go:503`), and the delete path already relies on that
  ("Notifications go with the person (5.3); the audit log stays",
  `api_admin.go:889`). There is no audit pruning anywhere — retention
  exists for notifications (a daily prune in `internal/notify`,
  `internal/notify/notify.go:359`) — so retained audit data is
  retained indefinitely unless this item's inventory says otherwise.
  But meta can hold personal text: email-change rows record both
  addresses (`internal/audit/audit.go:201`), invitation rows the
  invitee's email (`internal/audit/audit.go:171`), member-removal rows
  a free-text note (`internal/audit/audit.go:51`).
- Safeguarding rows are the deliberate exceptions. `TeenConsent` is
  immutable by database rule — updates and deletes are rejected,
  including hard deletes (`models/models.go:789`, fields at
  `models/models.go:791`). `Consent` rows carry evidence text, IP, and
  user agent (`models/models.go:889`). `Suppression` keeps the mailing
  address it must never mail again — in plaintext today
  (`models/models.go:1119`; `migrations/00052_suppressions.sql:12`),
  while PLAN says such records keep a hashed address
  (`docs/plan/PLAN.md:1054`); the inventory must call that difference
  out rather than paper over it.
- Households make erasure relational. A guardian's account links to
  household children and teens through `HouseholdAccount`
  (`models/models.go:720`) and `GuardianLink`
  (`models/models.go:766`, the admin's `CheckNote` at
  `models/models.go:773`); the children themselves are
  `HouseholdPerson` rows with names, birth dates, and care notes
  (`models/models.go:734`) that belong to the household, not to the
  account being deleted. A teen account sits in the same web from the
  other side, and a converted household person points back at the
  account (`ConvertedUserID`, `models/models.go:744`) with SET NULL
  only on hard delete (`migrations/00022_households.sql:42`) — which
  never happens.

### 3. Scope

In:

- The inventory (slice R4-0) and its review gate. No erasing code
  until the table is signed off.
- Erasure through the existing admin door and route
  (`DELETE /api/v1/admin/users/:id`): the tombstone rewrite, the
  row-by-row deletions and scrubs the inventory settles, avatar blob
  deletion, and the notices households and admins are owed.
- A one-time cleanup of accounts already deleted under the old code,
  so churches that deleted someone before this lands get the same
  outcome.

Out:

- A new erasure route, role, or request workflow. The door, the
  successor picker, and `policy.CanDemoteOrDelete` stay exactly as
  they are; this item changes what the door does, not who holds it.
- Self-service account deletion. A person leaving has the departure
  flow; asking for full erasure stays an admin act for now (open
  question 4), so the safeguarding trade-offs are made by staff, not
  by a button.
- Deleting `HouseholdPerson` rows whose only tie is the deleted
  guardian. Children are the household's record; slice R4-3 ends the
  links and tasks an admin, it does not delete children. (Removing a
  household person is its own existing door; that door's soft-deleted
  rows keeping names and care notes — `models/models.go:734` — is an
  inventory finding to file as follow-up, not to fix inside this
  item.)
- Editing the words of messages the person authored or notifications
  others received about them. A church announcement stands as sent;
  the inventory decides (§5) and slice R4-4 tests the decision, but
  rewriting other people's records by string-matching a name is out.
- Reaching into backups (§9 states the honest note instead), provider
  systems, or mailboxes. Erasure covers the live database and the
  blob store.

### 4. Slices (in order; R4-0 is a hard gate)

Slice R4-0 — the inventory, reviewed before any code.

- Deliverable: a table in the PR description (and, once settled, as
  §5 of this brief or a short `docs/` note the PR links) with one row
  per model in `models.All()` (38 rows, `models/models.go:1030`),
  plus rows for avatar blobs, group-cover blobs, `login_throttles`
  key text, job payloads, and backups. Columns: model/table; how it
  references the person (the user column(s) and the migration's
  delete rule); the personal fields it holds (including free text and
  rendered names, not just contact columns); the decision — exactly
  one of **deleted**, **anonymised** (row kept, named fields cleared
  or replaced), **retained** (with the reason in one line); and the
  mechanism (which transaction step, or "none — retained"). Every row
  cites the migration file:line for its foreign key, found by reading
  `migrations/*.sql` for `REFERENCES users`, not by trusting GORM tags
  (several models note they deliberately carry no association, e.g.
  `Registration.RemovedBy`, `models/models.go:460`).
- §5 below is the starting draft with the decisions this brief
  recommends. The agent's job in R4-0 is to verify it against the
  models and migrations, find what it missed (a field, a copy, a
  trigger), and get the contested rows — `TeenConsent`, `Consent`
  evidence, audit meta, `Suppression`, authored message text —
  explicitly agreed in review. The inventory also settles two
  mechanical questions with evidence: whether any database trigger
  fires on the current soft delete (the push device cleanup is
  trigger-driven when sessions are revoked — the inventory states,
  from the migration text, whether this path fires it, and the
  erasure deletes push rows explicitly regardless); and what ends a
  membership today on this path (the handler comment says memberships
  end, `api_admin.go:862` — the inventory names the exact write).
- Gate: no later slice starts until a reviewer has signed the table
  off in the PR. Additions found mid-work go back into the table
  first, then into code — never the other way round.

Slice R4-1 — the tombstone rewrite and the credential sweep.

- In `apiAdminDeleteUser` (`api_admin.go:842`), capture the person's
  email and `AvatarKey` from the locked row before any rewrite. Then
  widen the single update at `api_admin.go:879` into the full
  tombstone: `email` keeps today's `deleted-<id>@deleted.invalid`;
  `name` and `display_name` become `""` (so every retained row that
  renders this account shows `PublicName()`'s fallback "Member",
  `models/models.go:173`); `phone` `""`; `password_hash` NULL;
  `birth_date` NULL; `avatar_key` `""` with focals reset to 50;
  `timezone` `""`; `hide_contact_from_leaders` false; `status`
  becomes `disabled` with `administratively_disabled` true (a
  scrubbed-but-"active" row must never read as signable to any future
  `Unscoped` writer); `session_version + 1` as today. When R3 has
  landed, the same update clears `totp_secret_enc`,
  `totp_pending_secret_enc`, `totp_enabled_at`, and `totp_last_step`,
  and deletes the recovery-code rows.
- The tombstone is the point: the row stays, soft-deleted, as the
  id-only referent that attendance counts, audit rows, and
  safeguarding history point at. It holds nothing personal — an id,
  a synthetic address, a disabled status, and timestamps.
- Delete every live credential and device in the same transaction,
  each row named here because soft delete fires no cascade (§2):
  all `AccountToken` rows for the user (today `revokeAccountTokens`
  takes only the unused ones, `account_security.go:125` — used rows
  have no history worth keeping for an erased person, and their
  payloads can carry an email, `models/models.go:566`); the
  `CalendarToken` row (`models/models.go:537`), which kills the feed
  link at once even though only its hash was stored; every
  `PushSubscription` row (`models/models.go:917`) explicitly, never
  depending on a trigger; and the `login_throttles` rows keyed
  `email:<captured address>` and `user:<id>` (built at
  `api_auth.go:41` and `api_auth.go:202`). The email key is why the
  address is captured first: after the rewrite it is unfindable.

Slice R4-2 — per-table outcomes from the inventory.

- Implement §5's table exactly, inside the same `contactMutation`
  transaction as slice R4-1, ordered so the `user.deleted` audit row
  still commits with everything it describes. The shape of it:
  - **Deleted** (row goes): `Notification` (as today,
    `api_admin.go:890`) and its `NotificationDelivery` rows by user;
    `SeriesUndoReceipt` by actor; `ManagerAddBlock` by subject;
    pending `GuestSignup` rows matching the captured email, plus
    name/email/IP cleared on confirmed ones; `AccountRequest` rows
    for this account have `name`, `email`, `phone`, `message`,
    `guardian_email`, `birth_date`, and `password_hash` cleared and
    any still-pending one withdrawn; `Invitation` rows addressed to
    the captured email get `email`/`name`/`phone` cleared and pending
    ones revoked (rows the person merely sent keep the tombstoned
    `invited_by`); `people_import_rows`/`people_import_joins` if R2
    has landed (R2 §9 already promises this); dev `MailOutbox` rows
    addressed to the person.
  - **Anonymised** (row stays, personal text goes): `Membership` and
    `Registration` stay for history and capacity, but a registration
    loses `manage_token_hash`, `childcare_ages_note`, and
    `removal_note`, and no membership may remain `active` (the
    inventory names the exact current write; after this slice the
    assertion is "none active, none rendering a name");
    `MessageRecipient.Address` is cleared; `ContactRequest.Reason`
    and add-request childcare notes are cleared where this person is
    requester or subject; `OccurrenceAccess`/`ContactGrant` actives
    are ended by the contact reconciliation already in the
    transaction (`contact_lifecycle.go:68`) — the slice asserts that,
    it does not add a second ending path.
  - **Retained on purpose** (id-only once the tombstone hides the
    name): `AuditLog` (with the meta treatment in slice R4-4),
    `TeenConsent`, `Consent` rows with IP, user agent, and evidence
    text cleared, `GuardianLink` (status moves in slice R4-3),
    room bookings/requests and feed tokens authored by the person
    (church fabric, user ids only), authored `Message` text,
    `Job` rows (payloads hold ids), `Group`/`Event` content (the
    `LeaderID` display column at `models/models.go:242` must be
    repointed wherever the handoff leaves it naming the tombstone —
    the inventory checks this against `handOffLeadership`), and
    `Suppression` (below).
  - `Suppression`: keep mailing protection, drop the plaintext. PLAN
    promises a hashed address there (`docs/plan/PLAN.md:1054`) while
    the table stores text (`models/models.go:1119`). This slice does
    not redesign sending; the inventory either (a) keeps the address
    for erased people as a named, reviewed exception — the address
    exists in the table so the person is never mailed again,
    independent of the account — or (b) files hashing suppressions
    as its own item and keeps (a) meanwhile. Recommended: (b), stated
    in the PR, never silently.

Slice R4-3 — households, guardians, and teens.

- Deleting a guardian/adult account: delete its `HouseholdAccount`
  row; move every `GuardianLink` where they are the guardian to
  `revoked` with an audit row per the existing link actions
  (`internal/audit/audit.go:346`); clear `converted_user_id` on any
  `HouseholdPerson` pointing at the account (the SET NULL that soft
  delete never triggers, `migrations/00022_households.sql:42`). The
  household and its people are not deleted. If that leaves a
  household child or teen with no confirmed guardian, write the
  remaining household adults a bell notice and the admins a waiting
  task (`notify.Send` in this transaction; `notify.Resolve` closes it
  when a link is next confirmed), so "nobody can consent for this
  teen" is a task, not a discovery made months later. Deletion is
  never blocked by this — staff may be erasing precisely because the
  person is gone (open question 2).
- Deleting a teen account: revoke its `GuardianLink` rows the same
  way; `TeenConsent` stays untouched (the database refuses edits
  anyway, `models/models.go:789`) as the record that consent was
  handled lawfully, now naming only ids; confirmed guardians get a
  bell notice that the account was removed by an admin — no reason
  text, which the admin never typed anywhere in the first place.
- Deleting an account never deletes the `HouseholdPerson` it was
  converted from or is guardian to, and `Household.CreatedBy`
  (`models/models.go:713`) may keep naming the tombstone id — it
  renders through `PublicName()`, which now says "Member".

Slice R4-4 — audit meta and the tombstone in history.

- Before the `user.deleted` row is written, scrub the person's own
  clear-text identifiers out of retained audit meta in the same
  transaction: for rows where the person is the `user` target or the
  actor, drop the meta keys that carry addresses or typed text about
  them — `email`, `oldEmail`, `newEmail` (`internal/audit/audit.go:201`,
  `internal/audit/audit.go:171`), and the removal `note`
  (`internal/audit/audit.go:51`) where they are its subject. What
  remains — actor id, action, target, time, counts, ids — is the
  safeguarding and accountability record, and it now resolves to a
  tombstone. Rows about other people that this person merely acted
  on keep their subjects' data; this person's erasure is not license
  to gut someone else's history. The scrub is one `UPDATE` per key
  set, listed in the inventory; the `user.deleted` row written after
  it (actor the admin, target the tombstone) is itself the record
  that a scrub happened, and its meta carries the counts scrubbed,
  never their contents.
- This is a deliberate, named exception to audit append-only
  practice, called out in the PR description and the code comment:
  an accountability log that keeps a deleted child's parent's email
  in plain text forever is not protecting anyone. The alternative —
  retaining meta untouched — is open question 1, and either way the
  choice is recorded, not drifted into.

Slice R4-5 — the avatar blob.

- After the erasure transaction commits, call
  `a.Uploads.Delete(ctx, capturedKey)` (the call shape at
  `api_profile.go:518`). Order matters and is the whole design: the
  database must not roll an erasure back because a file was already
  missing (`storage/storage.go:117` treats that as success), and a
  blob must not vanish for an erasure that later rolls back. So:
  database first, blob second. A delete that still fails after commit
  enqueues one retry job — kind `erasure.blob_cleanup` through
  `jobs.Enqueue` (`internal/jobs/jobs.go:55`) with unique key
  `erasure.blob:<sha256 of the key>`, payload the key only, handler
  safe to run twice (deleting a gone blob is success). The key is a
  random server-generated path (`storage/storage.go:36`), safe to log
  where the person's name never is. Group covers are the group's
  record, not the person's, and are untouched even when the person
  uploaded them.

Slice R4-6 — accounts deleted before this landed, and the UI copy.

- A startup-enqueued, idempotent cleanup job (kind
  `erasure.backfill`, unique key `erasure.backfill:v1`, queued the
  daily-jobs way and removed from the codebase once it has had its
  release to run — or kept as a cheap daily no-op sweep; open
  question 3): find soft-deleted users whose email matches
  `deleted-%@deleted.invalid` yet who still carry a name, phone,
  hash, birth date, or avatar key, and run the same per-user erasure
  function slices R4-1 to R4-5 build (extract it as
  `eraseUserIn(tx, actorID, user, capturedEmail)` so the handler and
  the job share one body — the job's actor is nil and its audit meta
  says `backfill: true`). Processing is per-user transactional and
  safe to re-run; already-scrubbed tombstones match no field and are
  skipped.
- UI: the Admin delete dialog (`web/src/pages/Admin.tsx`, `UsersTab`
  at line 899) states the outcome in plain words before the
  successor picker: the person's name, contact details, photo, and
  sign-in are removed; past dates keep an anonymous count; consent
  and safeguarding records are kept without the name; copies already
  in the church's backups age out with those backups. No route or
  request-shape change, so no error-code or `tsContracts` work is
  expected; if the response grows a scrub summary for the dialog's
  result line, that struct gets its pair.

### 5. Inventory (the R4-0 deliverable, starting draft)

The agent verifies and completes this table — every row gains its
migration citation and its exact mechanism — and a reviewer signs it
before slice R4-1 starts. Decisions: **D** deleted, **A** anonymised,
**R** retained, **—** holds no personal data about the account.

| Model | Decision | What goes or stays, in short |
|---|---|---|
| `User` (`models/models.go:132`) | A | Tombstone rewrite (slice R4-1), then soft delete as today |
| `Group` (`models/models.go:229`) | R | Church record; display `LeaderID` repointed by handoff, never left on the tombstone |
| `Event` (`models/models.go:261`) | R | Church record; authorship columns keep the tombstone id |
| `EventHost` (`models/models.go:336`) | D | Hosting is a live role, not history; rows for this user go |
| `EventOccurrence` (`models/models.go:350`) | — | Dates only |
| `Membership` (`models/models.go:402`) | A | Row stays for history; none left active; renders as "Member" |
| `Registration` (`models/models.go:435`) | A | Row stays as the anonymous count PLAN keeps; manage-token hash, childcare ages note, removal note cleared; `RegisteredBy`/`RemovedBy` keep tombstone ids |
| `SeriesUndoReceipt` (`models/models.go:424`) | D | Short-lived actor capability; rows by actor go |
| `Setting` (`models/models.go:497`) | — | Church config (VAPID material is the church's, not a person's) |
| `AuditLog` (`models/models.go:505`) | A | Retained for accountability; the person's own addresses/typed notes scrubbed from meta (slice R4-4) |
| `LoginThrottle` (`models/models.go:519`) | D | `email:<address>` embeds the address; `user:<id>` row goes too |
| `Notification` (`models/models.go:1046`) | D | Already deleted today (`api_admin.go:890`); rows where the person is only `ActorID` on someone else's notice stay — the rendered name in another person's bell is that person's record, and retention prunes it in time |
| `AccountToken` (`models/models.go:560`) | D | All rows, used included; payloads can carry an address |
| `Invitation` (`models/models.go:577`) | A | Rows to their address: email/name/phone cleared, pending revoked. Rows they sent: kept, `invited_by` tombstoned |
| `MailOutbox` (`models/models.go:599`) | D | Dev/test store; rows addressed to them go (bodies name them) |
| `NotificationDelivery` (`models/models.go:628`) | D | Rows by user go with their notifications |
| `AccountRequest` (`models/models.go:659`) | A | Name, email, phone, message, guardian email, birth date, staged password hash cleared; pending ones withdrawn; decision provenance kept by id |
| `GuestSignup` (`models/models.go:475`) | A/D | Pending rows for the address deleted (the link must die); confirmed rows lose name/email/IP |
| `CalendarToken` (`models/models.go:537`) | D | The feed is a live credential; row goes even though only a hash was stored |
| `Household` (`models/models.go:709`) | R | The household is other people's record too; `CreatedBy` keeps the tombstone id |
| `HouseholdAccount` (`models/models.go:720`) | D | The deleted account's own seat goes |
| `HouseholdPerson` (`models/models.go:734`) | A | `ConvertedUserID` nulled; the person's own row, names, birth date, and care notes are unchanged — they are household records (their own removal door is the follow-up in §3) |
| `GuardianLink` (`models/models.go:766`) | A | Live links move to revoked with audit rows; decided history and check notes kept by id (safeguarding) |
| `TeenConsent` (`models/models.go:791`) | R | Immutable at the database; kept whole, including evidence text — it is the proof consent was lawful |
| `ContactRequest` (`models/models.go:935`) | A | Live ones end through the contact reconciliation; reason and add-note text cleared; decided history kept by id |
| `ContactGrant` (`models/models.go:975`) | A | Live grants ended by the reconciliation; history kept by id |
| `OccurrenceAccess` (`models/models.go:997`) | A | Live access ended by the reconciliation; provenance kept by id |
| `ManagerAddBlock` (`models/models.go:1020`) | D | A live preference about a live account; pointless once erased |
| `Consent` (`models/models.go:889`) | A | Row kept (the opt-in/out decision); IP, user agent, evidence text cleared |
| `Room` (`models/models.go:804`) | — | Church fabric |
| `RoomBooking` (`models/models.go:822`) | R | Church fabric; `CreatedBy` keeps the tombstone id |
| `RoomRequest` (`models/models.go:845`) | R | As bookings; reject-reason text is about the room, not the person |
| `RoomFeedToken` (`models/models.go:861`) | R | The room's credential, not the person's; `CreatedBy` tombstoned |
| `Job` (`models/models.go:1088`) | R | Payloads carry ids; finished rows prune in time. The inventory lists any live job kind whose payload carries a name or address instead |
| `Suppression` (`models/models.go:1116`) | R | The never-mail-again record; address stays in plaintext only as the named exception in slice R4-2 |
| `Message` (`models/models.go:1138`) | R | Sent church announcements stand; `AuthorID` keeps the tombstone id |
| `MessageRecipient` (`models/models.go:1191`) | A | Row kept for send counts; the stored `Address` is cleared |
| `PushSubscription` (`models/models.go:917`) | D | Live device capability; rows deleted explicitly |

Beyond `models.All()`: avatar blobs go **D** (slice R4-5); group-cover
blobs stay **R** (the group's record); the R2 import tables, when they
exist, are **A/D** per R2 §9; the R3 TOTP columns and recovery codes
are **A/D** in slice R4-1; backups are **R until they age out** (§9).

### 6. Data / API shapes

- The route and its request do not change: `DELETE
  /api/v1/admin/users/:id` (`api.go:805`) with `{"successors": {"<groupId>": <userId>}}`
  (`api_admin.go:859`) and the same `needSuccessors` 409 when a
  sole-led group lacks one. What the admin is told changes (dialog
  copy, slice R4-6), not what they send.
- The tombstone row, after erasure, reads (via `Unscoped`) as: `email
  = deleted-<id>@deleted.invalid`, `name = display_name = phone =
  avatar_key = timezone = ""`, `password_hash` and `birth_date` NULL,
  `status = disabled`, `administratively_disabled = true`,
  `deleted_at` set. Nothing else on the row may carry personal text;
  that sentence is the R4-1 test's assertion list.
- Freed-email behavior is unchanged, and is a contract other items
  rest on: the rewritten address means the real one can be invited or
  imported again at once (R2's classification counts on this, §5 of
  the R2 brief). Erasure must not reserve, suppress, or throttle a
  returning address — the throttle-row deletion in slice R4-1 keeps
  even a locked-out address clean for its next account.
- Audit: `user.deleted` as today (`internal/audit/audit.go:73`),
  its meta extended with counts only — `credentialsRevoked`,
  `rowsScrubbed`, `metaScrubbed`, `backfill` when the job ran it.
  Guardian-link revocations keep their existing action
  (`internal/audit/audit.go:349`) with meta `reason: "account_deleted"`.
  A failed-then-retried blob delete is a new `user.blob_cleanup_failed`
  only if review wants it visible to admins; otherwise the job row is
  the record. No audit meta anywhere in this item holds a name,
  address, or key.

### 7. Tests

The centerpiece is one adversarial test that no personal data remains,
built to fail when a future model or field is added without an
inventory row — not a checklist of the tables this slice happened to
touch.

Extend: `api_admin_test.go` (the delete door and its guards keep
their behavior), `api_handoff_test.go` (successor flow untouched),
`storage/storage_test.go` only if the store contract changes (it
should not).

New, next to their source (`erasure_test.go` beside the extracted
`eraseUserIn`; handler-level cases go in `api_admin_test.go`):

- `TestEraseLeavesNoPersonalData`: seed one maximal person carrying
  sentinel strings in every field the inventory names — a distinctive
  name, display name, email, phone, birth date, avatar key, and typed
  notes (`sentinel-name-zz`, `sentinel-phone-zz`, and so on) — touching
  as many of the 38 models as a seeded account can reach (membership,
  registrations with a manage-token hash and childcare note,
  notifications and a delivery, an account token, a calendar token, a
  push device, an account request, an invitation, a household with a
  child and a confirmed guardian link, teen consent on a second seeded
  teen subject, consent rows, a suppression, an authored message and
  a received one, audit rows carrying the address in meta). Delete
  through the real route. Then re-read with `Unscoped` and scan: the
  test walks every model in `models.All()`, reads every row for the
  user where a user column exists (and every row whose id it seeded),
  serializes the rows, and fails if any sentinel string — or the
  person's password hash prefix, birth date, or avatar key — appears
  outside the retained-by-decision set, which the test names
  explicitly (teen-consent evidence is seeded with neutral text, so
  the set stays: the suppression address and the authored message
  body). A future field that no one inventoried fails here first.
- `TestEraseTombstoneShape`: the §6 assertion list against the
  `Unscoped` row, plus the old password no longer verifying and the
  account reading as unable to sign in by any loader.
- `TestEraseKillsLiveCredentials`: the old calendar-feed token, a
  fresh password-reset token, and an open session cookie each stop
  working after deletion; the push rows are gone; the email-keyed
  throttle row is gone (a same-address new account signs in at once).
- `TestEraseKeepsAnonymousCounts`: a past date's going count is
  unchanged by erasure, no name renders on any roster or attendee
  list afterward, and waitlist promotion still fires for a freed
  future seat (`api_admin.go:898` keeps its behavior).
- `TestEraseAvatarBlobGone`: through the route with a real
  `LocalStore` in a temp dir, the blob exists before and
  `Exists` answers false after; a store whose `Delete` fails once
  leaves exactly one `erasure.blob_cleanup` job, which succeeds on
  its run and is harmless on a second.
- `TestEraseGuardianLeavesTaskNotOrphans`: deleting the only confirmed
  guardian revokes the links (audited), deletes no household person,
  and leaves the admins exactly one waiting task, resolved when a
  new link is confirmed. `TestEraseTeenKeepsConsentHistory`: the teen
  is tombstoned, consents are byte-identical, and confirmed guardians
  are notified with no contact detail in the notice.
- `TestEraseAuditMetaScrubbedHistoryKept`: rows about the person lose
  their address keys and keep action/time/ids; rows the person acted
  on about others keep their subjects' meta; the `user.deleted` row's
  counts match what was scrubbed.
- `TestErasureBackfillCleansOldTombstones`: a row deleted the old way
  (email rewritten, everything else intact — built by writing that
  state directly) is fully scrubbed by the job, blob included, and a
  second run writes nothing.
- `TestDeleteAlreadyDeletedIs404NotErasure`: deleting twice never
  errors into a half-state and never resurrects the freed address.

### 8. Acceptance checklist

- [ ] The inventory table is in the PR, signed off before the erasing
      code was reviewed, and every one of the 38 models plus blobs,
      throttles, jobs, and backups has a row with a decision and a
      mechanism.
- [ ] After deletion, an `Unscoped` read of the whole database finds
      none of the person's name, display name, email, phone, birth
      date, avatar key, or password hash outside the retained rows
      the inventory names.
- [ ] The avatar file is gone from the blob store (or one cleanup
      job exists that makes it gone), the calendar link is dead, no
      account token, push device, or session for the person works,
      and the same email can hold a brand-new account the same day.
- [ ] Attendance counts on past dates are unchanged, safeguarding
      rows (teen consent, guardian links, audit history) still
      resolve by id, and no screen renders the person's name —
      including group display leadership and rosters.
- [ ] A child left without a confirmed guardian becomes an admin
      task in the same moment; no household person was deleted.
- [ ] Accounts deleted under the old code are scrubbed by the
      backfill, provably, with the same sentinel test shape.
- [ ] The delete dialog says what is removed, what stays without a
      name, and the backups sentence — before the admin commits.
- [ ] `make test`, `cd web && npm test`, `make e2e`, and the quick
      checks (`go vet`, `make lint-go`, `tsc --noEmit`, `knip`) pass.

### 9. Risks, rollback, security/privacy

- Erasure is irreversible by design; that is the feature and the
  risk. The existing guards stay the protection against a wrong
  click: admin-only, never yourself, never the last admin
  (`internal/policy/policy.go:412`), plus the successor step. The
  dialog copy now names the permanence. There is no undo; a church
  that deleted the wrong person re-invites the address (which the
  freed-email rule allows at once) and takes the history loss.
- Backups are the honest limit, stated in the dialog and here:
  erasure reaches the live database and blob store only. Copies
  already taken age out under the church's backup retention (the
  deployed stack's nightly host backups today; the portable
  container's retention rule when M5.2 lands,
  `docs/plan/PLAN.md:2033`). Two operational notes go in the operator
  docs with this PR: restoring a backup older than an erasure brings
  the person's rows back — re-run the deletion after any restore,
  and the backfill sweep (if kept, open question 3) catches
  restored tombstones automatically; and anyone given database or
  backup access had the person's data all along, which is an access
  decision, not something an in-app delete can undo.
- Over-scrubbing is a safeguarding risk in the other direction:
  consent and guardian history erased in a privacy pass would leave
  a church unable to show it acted lawfully around a minor. The
  inventory's retained rows exist for exactly that, and they are
  id-only once the tombstone carries no name. Review of those rows
  should include whoever owns safeguarding judgement for the
  church, not only the code reviewers.
- A partially-failed erasure must fail whole: everything except the
  blob delete is one transaction under the contact gate, and the
  blob's post-commit step has a retried job behind it. Nothing here
  may run as a best-effort loop of separate statements.
- Rollback of the feature: reverting the handler changes leaves
  already-erased tombstones erased (they read as ordinary disabled
  soft-deleted rows to an older build) and new deletions return to
  the old leaky behavior — name that in any rollback note so nobody
  keeps deleting under the old assumption.

### 10. Dependencies

- Land this before R2 if the order is free: R2's new tables then
  arrive with their inventory rows written against this item's
  finished table, and R2's "a freed address can be imported again"
  rule is classification input to the import preview (R2 §10 asks
  the same). If R2 or R3 has already landed, their tables and
  columns are rows in this inventory from the start (§5).
- The sentinel test becomes the standing guard for every future
  model: adding a thirty-ninth entry to `models.All()` without an
  erasure decision should fail in review, and fails mechanically
  the moment it can hold a sentinel.
- The first-church list gains one line: delete a throwaway account
  on the live install and confirm the person's photo URL and
  calendar link answer "not found" from a signed-out browser.

### 11. Open questions for Grier (each with a default that unblocks work)

1. Should audit meta be scrubbed, or is the audit log untouchable
   whole? Recommended default: scrub the erased person's own clear
   addresses and typed notes (slice R4-4), keep everything else.
   "The log shows an admin changed an email" survives; the two
   addresses in plain text, forever, do not need to. If the answer
   is untouchable, slice R4-4 becomes a documented retention row in
   the inventory instead.
2. Should deletion be refused while the person is the only confirmed
   guardian of a child or teen, the way sole-led groups force a
   successor? Recommended default: no — erasure often happens because
   the person has died or left, and a hard refusal strands the church
   with a record it must keep but a person it cannot remove. The
   waiting admin task (slice R4-3) carries the consent gap instead.
   If review wants the guard, it mirrors `needSuccessors` with a
   per-minor replacement guardian, and the shape is named here so it
   can be added without redesign.
3. Should the backfill sweep stay forever, or ship once and be
   removed next release? Recommended default: keep it as a daily
   no-op sweep. It is one indexed query, and it is the only thing
   that makes "restore an old backup, then the erased come back
   erased" true without a human remembering (§9). Name it as a
   permanent retention job in PLAN 5.5 terms if kept.
4. Does a person get a self-service erase button later? Recommended
   default: not in this item. The departure flow covers leaving; a
   self-pressed button would also need the guardian-gap and audit
   decisions made by someone with no safeguarding view of them.
   Revisit when a church asks for it by name.

