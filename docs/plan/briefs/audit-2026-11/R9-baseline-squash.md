# R9 — Squash migrations into one baseline (pre-live window)

| Field | Value |
|---|---|
| Audit item | R9 [CHANGE], `docs/AUDIT-2026-11-01.md:151` |
| Priority | P1 (audit sequence step 3, `docs/AUDIT-2026-11-01.md:263`) |
| Effort | L |
| Status | Approved 2026-11-01. R7 and R8 were rejected; they give this brief nothing. |
| New decision | DEC-138 (draft text in §8; landed in PLAN Nov 2026 — PR #236 took DEC-137 for branding first, so the audit block runs DEC-138–DEC-145) |

This brief is the one explicit exception to "never edit an old migration"
(`AGENTS.md` Migrations rule, `docs/plan/PLAN.md` C1). It deletes the whole
migration chain and replaces it with one baseline. That is only safe before
any church holds data in Fold. DEC-138 (§8) records the exception, closes the
window at the first tagged release, and replaces the 14-day expand/contract
process with additive-first + next-release contract. If the gate in §4 cannot
be passed, this brief must not be implemented at all.

## 1. Goal

1. Replace the 61 migration files (versions 1–62; there is no 00021) with one
   `00001_baseline.sql` that creates exactly today's schema, proved by a
   `pg_dump` diff (§10), not by eyeballing.
2. Give the two text date columns that are really dates a real `date` type
   (`events.rrule_until`, `event_occurrences.original_date`). Keep the two
   wall-clock text columns text on purpose (`events.starts_local`,
   `events.tzid`), with baseline CHECKs (§6).
3. Fold `OccurrenceAccess` provenance into `registrations` as `access_*`
   columns and delete the `occurrence_accesses` table and its provenance
   trigger (§7). `ManagerAddBlock` does **not** fold; §7.6 says why and what
   happens instead. DEC-74 kept dates and DEC-91 series-cancelled dates keep
   their exact behavior.
4. Land DEC-138 and the matching `AGENTS.md` / PLAN 14.1 replacement text
   (§8), so nobody later has to reconstruct why the chain restarts.
5. No JSON, route, or UI change. A person using Fold notices nothing.

## 2. Current state, with evidence

### 2.1 The chain and its runner

- 61 SQL files in `migrations/`, `00001_baseline.sql` through
  `00062_reminder_push.sql` (00021 was never used). The audit's "61
  migrations" (`docs/AUDIT-2026-11-01.md:152`) is the file count; the head
  version is 62. Recount at freeze time (§13); R5/R6 may land first.
- The chain starts over more than once already: `00001_baseline.sql:2` is a
  "clean baseline (DEC-22)" that still creates `meetings`/`rsvps`, and
  `00005_events.sql:7` drops them again. `00001_baseline.sql:3` says "Never
  edit this file"; DEC-138 is what overrules that line, once.
- Runner: embedded `*.sql` (`migrations/migrations.go:15`), goose provider
  under the Postgres advisory lock (`migrations/migrations.go:20`,
  `migrations/migrations.go:25`). Boot applies pending migrations in
  `openDatabase`/`migrateUp` (`db.go:22`, `db.go:51`). The CLI shares the
  provider: `fold migrate [up|status|down-one]` (`cli.go:104`).
- Extensions the baseline must create: `citext`
  (`migrations/00003_email_login.sql:6`), `btree_gist`
  (`migrations/00046_room_bookings.sql:9`, for the room overlap exclusion
  constraint at `migrations/00046_room_bookings.sql:33`).
- Models: 38 tables in `models.All()` (`models/models.go:1030`). The drift
  check runs GORM AutoMigrate for real against a goose-migrated throwaway DB
  and records every CREATE/ALTER (`migrations/drift_test.go:18`, caller
  `TestNoDrift` at `migrations/migrations_test.go:662`).
- Tests pin intermediate versions. `migrations/migrations_test.go` calls
  `UpTo`/`DownTo` with concrete version numbers in dozens of places (for
  example `DownTo(..., 29)` at `migrations/migrations_test.go:169`,
  `DownTo(..., 33)` at `migrations/migrations_test.go:283`, `UpTo(..., 35)`
  at `migrations/migrations_test.go:405`). Every one of those breaks the
  day the chain becomes one file. §9 budgets the rewrite.

### 2.2 Text temporal columns: the full inventory

Only three columns hold a date or clock as text, plus one zone name.
Everything else is already `timestamptz`, `date`, or `tstzrange`; do not go
hunting for more.

| Column | Today | Go field | §6 verdict |
|---|---|---|---|
| `events.starts_local` | `text NOT NULL` (`migrations/00005_events.sql:15`) | `string`, `YYYY-MM-DDTHH:MM` (`models/models.go:313`) | Keep text + CHECK |
| `events.tzid` | `text NOT NULL` (`migrations/00005_events.sql:17`) | `string` (`models/models.go:315`) | Keep text + CHECK |
| `events.rrule_until` | `text` (`migrations/00005_events.sql:19`) | `*string`, `YYYY-MM-DD` inclusive (`models/models.go:317`) | → `date` |
| `event_occurrences.original_date` | `text NOT NULL`, unique with `event_id` (`migrations/00005_events.sql:33`) | `string`, `YYYY-MM-DD` (`models/models.go:354`) | → `date` |

Already real types, no change: `users.birth_date`
(`models/models.go:159`, `type:date`), `account_requests.birth_date`
(`models/models.go:670`), `household_people.birth_date`
(`models/models.go:740`), every `*_at` column (`timestamptz`), and
`room_bookings.padded_range` (`tstzrange`,
`migrations/00046_room_bookings.sql:19`; its Go `string` at
`models/models.go:830` is only the driver carrier). The SMS/push send window
(`08:00`–`20:00`, PLAN 5.3 `docs/plan/PLAN.md:263`) lives in `settings`
key/value rows and in `push.Window` hour fields, not in typed columns, so it
is out of scope (§3).

The wall-clock doctrine the two "keep" verdicts rest on: "The wall clock
(StartsLocal + TZID) is the source of truth; occurrences store UTC"
(`models/models.go:260`, PLAN C2/5.3 at `docs/plan/PLAN.md:310`).
`internal/events.ParseWall` refuses any offset because "the zone, not the
offset, is what stays put across DST" (`internal/events/expand.go:65`).

### 2.3 OccurrenceAccess and ManagerAddBlock today

- `models.OccurrenceAccess` (`models/models.go:997`): one row per exact-date
  grant — subject, occurrence, event, organizer (`group`|`event`), `origin`
  (`manager_add`|`departure_self`|`departure_removal`), issuer/request for
  manager adds, departure membership/actor for kept dates, `registration_id`
  (always set), `status` active|ended, granted/ended stamps and end reason.
  The struct comment (`models/models.go:989`) states the load-bearing rules:
  ending is irreversible, one active row per subject/date (partial unique
  index), no live-domain FK may cascade the history away, and departure
  alone never ends an accepted row.
- Table and trigger: created in `migrations/00031_contact_add_requests.sql:74`
  with the partial unique index at `:97` and function
  `occurrence_access_provenance()` at `:115` (trigger at `:166`); departure
  origins added by `migrations/00034_occurrence_access_departure_origin.sql:6`
  (origin CHECK at `:11`, replacement function at `:27`); final end-state
  CHECK is `occurrence_accesses_end_state` in
  `migrations/00037_departure_access_supersession.sql:7` (00035/00037 only
  widen its reason list); one more index in
  `migrations/00038_occurrence_access_active_occurrence.sql:4`.
- `models.ManagerAddBlock` (`models/models.go:1020`, table at
  `migrations/00031_contact_add_requests.sql:102`): (subject, organizer)
  pair that refuses NEW manager adds. It is organizer-scoped, not date-scoped,
  and must exist for a person who has no registration in that scope yet.
- Policy is pure and already registration-shaped:
  `policy.OccurrenceAccess` (`internal/policy/occurrence_access.go:224`),
  `ValidOccurrenceAccessOrigin` (`:244`), `OccurrenceAccessCurrent` (`:285`),
  `CanSeeOccurrence` (`:302`), `CanEndOccurrenceAccess` (`:331`),
  `CanBlockManagerAdds` (`:337`), `ManagerAddBlocked` (`:322`). DEC-74's
  boundary lives in `OccurrenceAccessCurrent`: departure origins stop at the
  occurrence end, manager adds do not (`:296`).

- Writers (the only places rows are created or ended):
  - Manager add: `recordManagerOccurrenceAdd`
    (`api_occurrence_mutations.go:577`, row built at `:589`). Same
    transaction clears `manage_token_hash` (`:596`), writes the
    `occurrence_access.added` audit row (`:599`), and sends
    `notify.OccurrenceAdded` with `SubjectType: "occurrence_access"` and
    dedupe key `occurrence-added:<access row id>` (`:610`).
  - Kept date after departure: `recordDepartureOccurrenceAccess`
    (`api_departure.go:602`); a live manager-add row on a kept date is first
    ended as `departure_superseded`, then a successor departure row is
    inserted (`api_departure.go:677`).
  - Ending: `endOccurrenceAccess` family (`api_occurrence_mutations.go:188`,
    save at `:204`, `occurrence_access.ended` audit row at `:208`, notice
    with dedupe key `occurrence-ended:<access row id>` at `:230`);
    `reconcileOccurrenceAccesses` (`:151`) finds rows to end;
    `liveOccurrenceInvitationIn` (`:236`) is the in-transaction authority
    check used by `registration_subjects.go:133` and
    `api_registrations.go:1126`/`api_registrations.go:1349`.
  - Block: created only in the recipient's Can't-go/block handler
    (`api_occurrence_mutations.go:344`); read into `AddFacts.Block` at
    `api_occurrence_mutations.go:110` and `api_add_request_reads.go:118`.
- Readers: `scopedOccurrenceReads` (`api_occurrence_access.go:51`) unions
  exact occurrence IDs for the viewer and never grants series rights;
  `occurrenceAccessPolicy` (`api_occurrence_access.go:28`) adapts the model
  to the policy struct; `loadOccurrenceInvitationFacts`/`endReason`
  (`api_occurrence_access.go:117`, `:175`) batch the same facts.
  `reminderReaders` (`reminders.go:262`) falls back per recipient: ordinary
  visibility first, then `scopedOccurrenceReads` for a live exact-date grant
  (`reminders.go:294`). Feeds, calendar, and room calendar read through the
  same helper (`api_feeds.go:198`, `api_calendar.go:123`,
  `api_room_calendar.go:176`).
- The deferred constraint trigger `add_request_completed()`
  (`migrations/00031_contact_add_requests.sql:193`, trigger at `:211`) proves
  a granted `occurrence_add` request by finding the `occurrence_accesses`
  row with `request_id = NEW.id`. It must be re-pointed, not deleted (§7.4).
- Correction to the audit's wording: there is **no dual-write trigger** in
  the schema to delete. Today's trigger functions are the two append-only
  guards (`migrations/00026_teen_consent.sql:21`,
  `migrations/00044_consents.sql:22`), `contact_grant_successful_request`
  (final form `migrations/00031_contact_add_requests.sql:58`),
  `occurrence_access_provenance`, `add_request_identity` (`:172`),
  `add_request_completed`, `guard_membership_tenure`
  (`migrations/00036_membership_tenure.sql:11`), and
  `push_devices_end_with_sessions` (`migrations/00059_push_text.sql:22`).
  What R9 retires is (a) `occurrence_access_provenance` itself, and (b) the
  old-column audit-trigger machinery the 14-day process would have required
  later. All other triggers survive verbatim (§5.3).

### 2.4 DEC-91 and other behavior that must not move

- `cancel_source` is `series` or `manual`; a series-cancelled date a later
  edit includes again is scheduled again and its registrants are told
  (DEC-91, `docs/plan/PLAN.md:2196`; column `models/models.go:373`,
  `SeriesCancelled()` at `models/models.go:396`). The baseline keeps the
  column, its CHECK, and both model helpers untouched.
- Departure kept dates (DEC-74, `docs/plan/PLAN.md:2179`): exact-date access,
  no group/roster/series rights, ends at the occurrence end. Leaving keeps
  `status=left`, removal keeps `status=removed` (DEC-86). Unchanged.
- `internal/policy` stays pure; handlers load facts and call it. The fold
  changes where one fact is stored, never who decides.

### 2.5 Dev and test databases

`internal/testdb` migrates a template once per migration set and clones it
(`internal/testdb/testdb.go:165`). The template name embeds
`migrationsHash()` over every embedded file's path and bytes
(`internal/testdb/testdb.go:287`), and stale templates are dropped when the
hash changes (`internal/testdb/testdb.go:176`). The squash therefore
invalidates test templates by itself. A long-lived dev database does not:
its goose version table will name versions the new binary has never heard
of. §7.7 makes recreating it an explicit documented step.

## 3. Scope

### In

- New decision DEC-138 in PLAN §15 + the two replacement passages in §8.
- Delete all 61 SQL files; add one `migrations/00001_baseline.sql`,
  generated by the §5 recipe. `migrations/migrations.go` unchanged.
- Two type upgrades and two CHECKs (§6), with every Go caller they touch.
- `access_*` columns on `registrations`; delete `occurrence_accesses`, its
  indexes, and `occurrence_access_provenance()`; add
  `registration_access_provenance()`; re-point `add_request_completed()`.
  Rewrite the §2.3 writers and readers onto the columns (§7.5).
- Remove `models.OccurrenceAccess` from `models.All()`
  (`models/models.go:1037`). Table count 38 → 37. `ManagerAddBlock` stays.
- Rewrite version-pinned migration tests as final-schema constraint tests
  (§9), plus the dump-diff proof as a repeatable script.
- Dev-database recreation commands, verified by running them, in the S2 PR
  description and in `docs/DEPLOY-DIGITALOCEAN.md`-adjacent dev docs only if
  a dev-reset passage already exists there; otherwise PR description only.

### Out

- Any API/JSON/UI change. `original_date` still renders as `YYYY-MM-DD` in
  JSON, URLs (`occurrenceDatePath`, `api_occurrence_access.go:218`), and ICS.
- Deleting `manager_add_blocks` or weakening any DEC-31/DEC-74 safeguard.
- Batching `reminderReaders`' per-recipient loop, incremental `Extend`,
  pool caps — that is R11. The fold makes the fallback read one table; it
  does not remove the loop. (The audit sentence at
  `docs/AUDIT-2026-11-01.md:155` names "a per-recipient fallback" as
  deleted; precisely, the second-table read inside it goes away.)
- A data-conversion path from an old-chain database to the baseline. None is
  offered, in code or in docs. §4 exists so none is ever needed.
- Renaming policy types (`policy.OccurrenceAccess` and friends keep their
  names; see §14 Q3). Renaming audit action strings
  (`internal/audit/audit.go:17` `occurrence_access.added`, `:20`
  `occurrence_access.ended` keep their values).
- SMS send-window settings, `push.Window`, quiet hours, rooms — untouched.
- Any new feature table. Schema-adding audit items wait for the freeze to
  lift (§13) and then arrive as `00002+`.

## 4. Preconditions gate (no production data exists)

The squash is lawful only while DEC-22's premise holds: "No church runs
Fold" (`docs/plan/PLAN.md:11`, DEC-22 at `docs/plan/PLAN.md:2127`; the same
premise inside `migrations/00005_events.sql:3`). That premise is an owner
fact, not a code fact, so the gate is evidence plus an explicit owner
sign-off, all recorded in the S2 PR description. The implementing agent
gathers items 1–4; the owner confirms item 5 before S2 opens.

1. **No release exists.** `git tag` prints nothing (true at audit HEAD
   `3ec934e`; re-run at freeze). No prebuilt image has been published: the
   GHCR image is R1 work and does not exist yet
   (`docs/AUDIT-2026-11-01.md:69`). If R1's image slice lands first, this
   item must be re-proved against the registry, and any published image
   that a church could have pulled closes the gate.
2. **No first-church checks have been run.** `docs/FIRST-CHURCH-CHECKS.md`
   still lists its hand-checks as not run (audit evidence
   `docs/AUDIT-2026-11-01.md:136`). Any checked item that implies a live
   deployment closes the gate.
3. **No production database is reachable from the repo.** Every
   `DATABASE_URL` the repo knows names a dev/test database (dev compose,
   CI service, `internal/testdb`). There is no hosted instance: "SaaS"
   here means many future single-tenant deployments, and none has been
   provisioned; the DigitalOcean script "never calls `doctl` and never
   creates droplets" (AGENTS, Deployment), so it cannot have produced a
   hidden live install by itself.
4. **Row-count spot check on every database the operator can reach.**
   For each non-throwaway database the owner knows of (dev machines
   included), record `SELECT count(*) FROM users;` in the PR. Expected:
   only dev/seed rows the owner recognizes as disposable. This does not
   prove a negative about machines nobody lists; item 5 covers that.
5. **Owner attestation, verbatim in the PR:** "No church or other person
   outside development has a Fold database whose rows must survive."
   Without this sentence from the owner, stop (§12).

**Abort path.** If any item fails — a church has started, an image was
pulled by anyone outside development, a database turns up whose rows
matter — R9 stops entirely. The type upgrades and the fold may still be
worth doing, but then as ordinary new migrations under the additive-first
rule (§8 text still lands, minus the baseline-editing window), with a data
backfill the owner designs separately. Do not "squash anyway and convert
their data": no converter is in scope, and inventing one inside this PR is
a stop condition (§12).

The window also closes by itself: DEC-138 ends baseline editing at the
first tagged release or first church deployment, whichever comes first.

## 5. Baseline generation: dump, don't hand-write

**Decision: `pg_dump --schema-only` from a fully migrated scratch database,
then a fixed cleanup list. Never hand-write the baseline.** A hand-written
3,000-line file cannot be proved equal to the chain; a dump can (§10). The
S2 baseline changes no schema at all — §6 and §7 land as later edits to the
still-unshipped baseline (S3, S4), so each step's diff stays reviewable.

### 5.1 Recipe (script it; commit the script under `scripts/` as shell)

1. Start a scratch Postgres 16 (the dev compose service is fine) and create
   two empty databases: `fold_squash_old`, `fold_squash_new`.
2. Migrate `fold_squash_old` with the pre-squash tree:
   `DATABASE_URL=...fold_squash_old go run . migrate up`. Confirm head
   version with `go run . migrate status` and record it in the PR.
3. Dump: `pg_dump --schema-only --no-owner --no-privileges fold_squash_old`.
   Pin `pg_dump` major to the server major (16). A different `pg_dump`
   major rewords defaults and breaks the §10 diff without any schema change.
4. Apply the §5.2 transforms — mechanically, in the script, so the file is
   regenerable. After S2 merges, nobody regenerates; the script remains as
   the §10 proof tool only.
5. Apply the result to `fold_squash_new` through goose (swap the files in a
   scratch checkout, `go run . migrate up`), then run the §10 diff.

### 5.2 Cleanup rules (the complete list; anything else stays byte-identical)

- Delete the `goose_db_version` table DDL. Goose creates and owns its
  version table at run time; a baseline that also creates it fails on a
  fresh database. (Verify once against the vendored goose version when
  writing the script; if it did not self-create, keep the DDL — the §10
  diff strips this table from both sides either way.)
- Delete session boilerplate: `\restrict`/`\unrestrict` lines, `SET ...`
  lines, `SELECT pg_catalog.set_config(...)`, and the `-- Dumped from` /
  `-- Dumped by` header comments (they carry version noise).
- Keep both `CREATE EXTENSION` lines, normalized to `IF NOT EXISTS` form,
  at the top of the Up section (`citext`, then `btree_gist`).
- Wrap the body in goose annotations: `-- +goose Up` first, then the file
  header comment naming DEC-138 and the frozen head version it replaces;
  `-- +goose Down` before §5.4. No `-- +goose NO TRANSACTION`: plain DDL
  and `CREATE EXTENSION` run inside goose's transaction, and no
  `CREATE INDEX CONCURRENTLY` exists in the dump (fail the script if one
  ever appears).
- Wrap every `CREATE FUNCTION` in `-- +goose StatementBegin` /
  `-- +goose StatementEnd`, as the current files do
  (`migrations/00031_contact_add_requests.sql:57`). Goose otherwise splits
  function bodies at semicolons.
- Keep `pg_dump`'s emission order untouched (extensions, functions, tables,
  sequences, defaults, constraints, indexes, triggers). Keep its
  `CREATE SEQUENCE ... OWNED BY` form; do not rewrite to `bigserial`.
  Deterministic order is a property of this recipe, not of hand tidying —
  any manual reorder voids the proof and is a stop condition (§12).
- Change nothing else in S2: no renamed constraints, no "while we're here"
  index, no dropped dead column. GORM-name alignment is already in the
  dump because the old files were written to match it
  (`migrations/00001_baseline.sql:3`).

### 5.3 What survives: functions and triggers

After **S2**, every function and trigger in §2.3's list survives verbatim.
After **S4**, exactly one function is deleted
(`occurrence_access_provenance()`, with its trigger and table), one is
added (`registration_access_provenance()`, §7.4), and one is re-pointed
(`add_request_completed()`, §7.4). The append-only guards
(`teen_consents`, `consents`), `contact_grant_successful_request`,
`add_request_identity`, `guard_membership_tenure`, and
`push_devices_end_with_sessions` are never edited in any slice. The §10
diff for S4 must show hunks in no other function.

### 5.4 The baseline Down

State it plainly: **the baseline Down destroys the schema. It exists for
scratch databases and the down/up test only, and must never run where rows
matter.** Shape:

1. A guard first: a `DO` block that raises an exception when `users` holds
   any row ("baseline down would destroy data; refusing"). This turns an
   absent-minded `fold migrate down-one` (`cli.go:114`) on a real install
   into an error instead of a dropped church. The empty scratch database
   in the down/up test passes the guard.
2. Then explicit `DROP TABLE ... CASCADE` lines in reverse dependency
   order, generated by the script from the dump's object list (reviewable;
   no `DROP SCHEMA public CASCADE`), then `DROP FUNCTION` lines, then
   `DROP EXTENSION btree_gist`, `DROP EXTENSION citext`.

There is no down path from the baseline to the old chain, and none from an
old-chain database to the baseline. Rollback is a git revert (§12), not a
goose down.

## 6. Type upgrades, column by column

### 6.1 `events.rrule_until`: `text` → `date`

- Baseline: `rrule_until date`. Model: `*time.Time` with
  `gorm:"column:rrule_until;type:date"`, matching the three `BirthDate`
  fields (`models/models.go:159`).
- Why safe: the value is a bare calendar date, inclusive, with no zone of
  its own; comparisons today are lexicographic string compares that `date`
  makes exact (`internal/events/extend.go:35` filters
  `rrule_until >= <YYYY-MM-DD>`; `internal/events.Expand` compares
  `w.OriginalDate > until` at `internal/events/expand.go:130` and `:168`).
- Callers to update in S3 (find all with a search for `RRuleUntil`; the
  known ones): model field (`models/models.go:317`); `extend.go:74–75`
  (dereference + format); the `extend.go:35` query (bind a parsed date,
  not a formatted string); every API read/write of `rruleUntil` JSON —
  wire format stays `YYYY-MM-DD`, parsed once at the handler edge with the
  same rejection the editor returns today. `events.Spec.Until` stays a
  `string` (`internal/events/expand.go:51`); formatting happens at the
  persistence boundary so `Expand` and its tests keep their shape.

### 6.2 `event_occurrences.original_date`: `text` → `date`

- Baseline: `original_date date NOT NULL`, unique index
  `idx_event_original_date (event_id, original_date)` unchanged in name.
  Model: `time.Time` with `type:date` (`models/models.go:354`).
- This is the wide one. `OriginalDate` is a map key and a string compare
  across `internal/events` (`When.OriginalDate`
  `internal/events/expand.go:56`; series reconciliation
  `internal/events/series.go:56`; extension dedupe
  `internal/events/extend.go:90`; ICS ordering `internal/events/ics.go:67`),
  the URL date segment (`api_occurrence_access.go:218`, parsed at `:340`),
  the per-date query (`api_occurrence_access.go:350`), and JSON `date`
  fields (`api_occurrence_access.go:228`, `:271`).
- Boundary rule for S3: `internal/events` keeps working in `YYYY-MM-DD`
  strings internally (`Spec`, `When`, `series.go`, `ics.go` unchanged).
  Conversion happens exactly where rows are written or read: add two small
  helpers next to the model (parse `2006-01-02` → `time.Time` at UTC
  midnight; format back) and use them in the handlers that build
  `EventOccurrence` rows from `When` values and render rows out. The URL
  handler parses its `:date` segment once (it already validates the format
  at `api_occurrence_access.go:340`) and queries with the parsed value.
  JSON output formats with `2006-01-02`; the wire never changes.
- Scan discipline: a Postgres `date` scanned by pgx/GORM arrives as a
  `time.Time` at midnight in the session zone. The format helper must read
  year/month/day fields, never convert zones, so a session `TimeZone`
  setting can never shift the date. Add a unit test that round-trips the
  helper under a non-UTC location value.
- Why it is worth the churn: the database now refuses impossible dates,
  range filters and the unique index order by real dates, and the
  lexicographic-compare invariant (true only while every writer formats
  perfectly) stops being load-bearing.

### 6.3 Keep text on purpose: `starts_local`, `tzid`

- `starts_local` stays `text NOT NULL`, plus a baseline CHECK
  `starts_local ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}$'`.
  Justification: it is a wall clock, not an instant (§2.2). `timestamptz`
  would pin an offset and move the local time across DST — the exact bug
  C2 removed. `timestamp without time zone` is the only candidate type,
  and it buys database validation at the price of a session-zone footgun
  on every scan (the driver returns a zoned `time.Time` whose clock fields
  the code must trust and whose zone it must ignore), plus churn in
  `ParseWall` and the RRULE `DTSTART;TZID=` formatting
  (`internal/events/expand.go:138`). The CHECK gives the refusal of
  malformed values without the footgun. DEC-89's caps live in `Expand`,
  not in the column type, and stay there (`docs/plan/PLAN.md:2194`).
- `tzid` stays `text NOT NULL`, plus CHECK `char_length(tzid) BETWEEN 1
  AND 64`. Justification: Postgres has no IANA-name type; validity is
  `LoadZone`'s job (`internal/events/expand.go:79`, which already refuses
  `""`, `Local`, and names over 64 chars — the CHECK mirrors that bound).
  The same reasoning covers `users.timezone` and `guest_signups.timezone`
  (`migrations/00016_user_timezone.sql:6`): zone names stay text, no
  CHECK added there because `''` is a meaningful value ("church zone").
- Both CHECKs are named in the baseline (`events_starts_local_check`,
  `events_tzid_check`) and appear as intended hunks in the S3 diff.

## 7. Fold exact-date provenance into `registrations`

### 7.1 Why this fold is sound — and its one honest loss

Every `occurrence_accesses` row names exactly one registration
(`registration_id NOT NULL`, subject always that registration's account
user), and at most one row per (subject, occurrence) can be active.
`registrations` is already unique per (occurrence, user)
(`idx_registration`, `migrations/00005_events.sql:58`). So the *live*
grant is a property of the registration row, and the separate table's
remaining job is retaining *ended* grants as queryable history.

The fold keeps the live grant on the row and moves history to the
`AuditLog`, which already records every grant and end in the same
transaction (`api_occurrence_mutations.go:599`, `:208`;
`api_departure.go:613`). The honest loss: ended grants stop being a table
you can `SELECT` from; they become audit rows you read by
`target_id = registration id`. That is acceptable pre-live because (a) the
only programmatic reader of ended history is the repeat-add rule, which
needs just the latest end stamp (§7.3 keeps it on the row), and (b) audit
rows are never purged. §7.5 requires the audit `Meta` to carry the full
provenance so nothing else is lost. DEC-138 records this trade.

### 7.2 New columns on `registrations` (baseline, S4)

| Column | Type | Meaning |
|---|---|---|
| `access_origin` | `text NOT NULL DEFAULT ''` | `''` = ordinary answer, no exact-date provenance. Else `manager_add` \| `departure_self` \| `departure_removal` |
| `access_issuer_id` | `bigint` | Manager-add issuer; null otherwise |
| `access_request_id` | `bigint REFERENCES contact_requests(id)` | Granted add request; null for direct/departure grants |
| `access_departure_membership_id` | `bigint` | Departure grants only |
| `access_departure_actor_id` | `bigint` | Departure grants only |
| `access_granted_at` | `timestamptz` | Set with the grant; null while `access_origin = ''` |
| `access_ended_at` | `timestamptz` | Null = grant live. Set once, never cleared by ordinary answer changes |
| `access_end_reason` | `text NOT NULL DEFAULT ''` | The existing reason vocabulary |
| `access_ended_by` | `bigint` | Who ended it, where the end-state CHECK requires one |

Model fields mirror them on `models.Registration` (`models/models.go:435`)
as `AccessOrigin string`, `AccessIssuerID *uint`, `AccessRequestID *uint`,
`AccessDepartureMembershipID *uint`, `AccessDepartureActorID *uint`,
`AccessGrantedAt *time.Time`, `AccessEndedAt *time.Time`,
`AccessEndReason string`, `AccessEndedBy *uint`. One partial unique index
replaces both old access indexes' live-side job:
`idx_registrations_access_request UNIQUE (access_request_id) WHERE
access_request_id IS NOT NULL`, plus
`idx_registrations_live_access (occurrence_id) WHERE access_origin <> ''
AND access_ended_at IS NULL` (the successor of
`migrations/00038_occurrence_access_active_occurrence.sql:4`). The
one-active-per-subject/date index needs no successor: the registration
unique index already gives it.

**Live test** (used by every reader): `access_origin <> '' AND
access_granted_at IS NOT NULL AND access_ended_at IS NULL`. There is no
`access_status` column; `policy.OccurrenceAccessCurrent` already requires
`EndedAt == nil` beside `Status == active`
(`internal/policy/occurrence_access.go:288`), so the adapter synthesizes
`Status` from `access_ended_at` (§7.5). Subject, occurrence, event, and
organizer are no longer stored twice: subject is `registrations.user_id`
(provenance exists only on account rows — §7.3 CHECK), occurrence is
`occurrence_id`, and event/organizer are derived live exactly as
`occurrenceInvitationReadsIn` already derives them
(`api_occurrence_access.go:92`).

### 7.3 Constraints (port the dumped final text; do not re-derive)

- `registrations_access_provenance` CHECK: the origin/provenance shape of
  `migrations/00034_occurrence_access_departure_origin.sql:11`, rewritten
  onto the new column names, plus: provenance implies an account row
  (`access_origin = '' OR (user_id IS NOT NULL AND household_person_id IS
  NULL)`); `access_origin = ''` implies every other `access_*` column is
  at its empty value. Departure origins no longer need the
  `organizer_type = 'group'` clause — the trigger (§7.4) proves the event
  belongs to the departure group, which is strictly stronger.
- `registrations_access_end_state` CHECK: the reason/actor pairing of
  `occurrence_accesses_end_state`
  (`migrations/00037_departure_access_supersession.sql:7`) with
  `subject_user_id` read as `user_id`, `status='active'` read as
  `access_ended_at IS NULL`, and `ended_at >= granted_at` kept. An
  ordinary row (`access_origin = ''`) satisfies it vacuously.
- Take both CHECK bodies from the S2 dump text (which is Postgres's own
  rendering of the final constraints), not from the historical files, so
  no reason added by 00035/00037 is dropped by transcription.

### 7.4 Triggers

- **Delete** `occurrence_access_provenance()` and its trigger with the
  table (origin: `migrations/00031_contact_add_requests.sql:115`, final
  form `migrations/00034_occurrence_access_departure_origin.sql:27`).
- **Add** `registration_access_provenance()`, `BEFORE INSERT OR UPDATE ON
  registrations`. It acts only on `access_*` columns; every other column
  of the row stays as mutable as today (answers, waitlist stamps, guest
  counts must never be frozen by this trigger). Rules, ported one-for-one
  from the deleted function:
  1. *Immutability.* Once granted (`access_granted_at` set), the grant
     fields (`access_origin`, issuer, request, departure pair,
     `access_granted_at`) cannot change, with one exception: the
     grant-replacement transition in rule 3. Once ended, `access_ended_at`,
     `access_end_reason`, `access_ended_by` cannot change except through
     rule 3. (Registration-row `DELETE` is not blocked: the old
     no-delete rule protected a standalone history table; history now
     lives in `audit_logs`. S4 must still inventory hard deletes of
     registrations and confirm each path writes its audit row first.)
  2. *One-way end.* `access_ended_at` may move null → set once per grant,
     with reason/actor satisfying the end-state CHECK. Set → null is
     refused outside rule 3. This is the old "ending is irreversible".
  3. *New grant overwrites an ended one.* When a row whose grant has ended
     receives a fresh grant (new `access_origin` + `access_granted_at`,
     `access_ended_at` back to null, reason/`by` cleared) in a single
     UPDATE, validate it exactly as the old INSERT path did: live
     registration/occurrence/event/subject facts; `going` required for
     `manager_add`, `going|maybe|waitlisted` for departure origins;
     manager-add block refusal; teen or repeat direct adds refused — where
     "repeat" now means `OLD.access_ended_at IS NOT NULL` — unless
     `access_request_id` names an exact granted `occurrence_add` request;
     that request's `created_at` must not predate `OLD.access_ended_at`
     (this is `AddFacts.LatestAccessEndedAt`,
     `internal/policy/occurrence_access.go:107`, and the old trigger's
     `old_access.ended_at > contact_requests.created_at` refusal at
     `migrations/00034_occurrence_access_departure_origin.sql:76`);
     departure grants re-prove membership authority against the still-
     active membership, as `migrations/00034_occurrence_access_departure_origin.sql:84`
     and `:94` do today. A first grant on a fresh row (INSERT with
     `access_origin <> ''`, or UPDATE from `''`) takes the same validation
     minus the repeat clauses.
  4. Ordinary answer changes that leave every `access_*` column untouched
     pass without any provenance validation — including on live-grant
     rows, whose reconciliation (ending the grant when the answer stops
     qualifying) stays application logic, as today.
- **Re-point** `add_request_completed()`
  (`migrations/00031_contact_add_requests.sql:193`): the `occurrence_add`
  branch now requires a `registrations` row with `id =
  NEW.completed_target_id AND access_request_id = NEW.id AND user_id =
  NEW.subject_user_id AND access_issuer_id = NEW.requester_id AND
  occurrence_id = NEW.occurrence_id`. (Event equality follows from the
  occurrence; state it via a join if the port keeps the old shape — either
  way the S4 diff shows one function body changed and the group branch
  untouched.)

### 7.5 Writers and readers (file-by-file)

Policy package: no signature changes. `policy.OccurrenceAccess`,
`ValidOccurrenceAccessOrigin`, `OccurrenceAccessCurrent`,
`CanSeeOccurrence`, `CanSeeOccurrenceVideo`, `CanEndOccurrenceAccess`,
`CanBlockManagerAdds`, `CanRetainOccurrenceAfterDeparture` keep their
bodies; `internal/policy/occurrence_access_test.go` should pass unedited —
that is a required S4 proof, not a hope. Only fact-loading changes:
`AddFacts.ExistingAccessActive` ← the §7.2 live test on the subject's
registration for the date; `AddFacts.LatestAccessEndedAt` ← that row's
`access_ended_at` when a grant has ended (null when none ever granted);
`AddFacts.Block` unchanged (§7.6).

- `api_occurrence_access.go`: `occurrenceAccessPolicy` (`:28`) takes a
  `models.Registration` (plus occurrence/event/organizer derived by the
  caller) and synthesizes `Status` from `AccessEndedAt`.
  `scopedOccurrenceReads` (`:51`) queries `registrations` by
  `user_id = viewer AND access_origin <> '' AND access_ended_at IS NULL
  AND occurrence_id IN (q)`; keys stay occurrence IDs, never event IDs.
  `occurrenceInvitationReadsIn`/`loadOccurrenceInvitationFacts` (`:77`,
  `:117`) collapse: the registration that carries provenance is the same
  row facts.registrations used to fetch by `registration_id` — load it
  once, keep the live status filter (`going|maybe|waitlisted`, `:162`)
  and every identity cross-check. `endReason` (`:175`) keeps its
  precedence order, reading provenance from the registration; the
  organizer-mismatch branch (`:198`) now compares the live-derived
  organizer against the facts built from the same row, so it reduces to
  the group/event liveness checks around it — keep the deleted/context-
  changed reasons reachable via those checks, and say so in the PR.
  `presentOccurrenceDate` (`:262`) and `loadOccurrenceDate` (`:337`) need
  no logic change beyond the new adapter.
- `api_occurrence_mutations.go`: `recordManagerOccurrenceAdd` (`:577`)
  becomes an UPDATE of the registration's `access_*` columns in the same
  transaction (no second insert; the `existing.ID != 0` early return at
  `:582` becomes the live test on the row already in hand). Keep the
  `manage_token_hash` clear (`:596`). Audit/notify identity changes:
  `TargetType`/`SubjectType` `"occurrence_access"` → `"registration"`,
  IDs become the registration ID. Dedupe keys must survive a re-grant on
  the same row: `occurrence-added:<registrationID>:<access_granted_at as
  RFC3339>` and `occurrence-ended:<registrationID>:<access_ended_at as
  RFC3339>` (today's keys at `:610` and `:230` embed the access row ID,
  which no longer exists). Audit `Meta` on both add and end must carry
  `origin`, `issuerId`, `requestId`, `departureMembershipId`,
  `departureActorId` as applicable, plus today's
  `subjectUserId`/`eventId`/`occurrenceId` — this is what makes §7.1's
  history trade lossless. `endOccurrenceAccess` (`:188`) sets the
  `access_ended_*` columns; `reconcileOccurrenceAccesses` (`:151`) and
  `occurrenceNoticeAccesses`/`sendOccurrenceNotice` (`:613`) read live
  grants from `registrations`. The block handler (`:310`–`:378`) reads
  provenance from the actor's registration; block creation at `:344` is
  untouched. `liveOccurrenceInvitationIn` (`:236`) takes the registration.
- `api_departure.go`: `recordDepartureOccurrenceAccess` (`:602`) becomes
  the same UPDATE with departure provenance. Supersession (`:677`) on one
  row is: audit the old grant's end (`departure_superseded`, via the
  no-notice end helper), then overwrite with the departure grant in the
  same UPDATE the trigger validates — two audit rows, one row write, in
  that order, inside the existing locks. The `api_departure.go:326` loader
  and `applyDeparture`'s per-date access lookup (`:673`) read the
  registration already loaded at `:670`.
- `api_add_request_reads.go:118`, `registration_subjects.go:133`,
  `api_registrations.go:1126`, `api_registrations.go:1349`: same swaps.
- `reminders.go:294`, `api_feeds.go:198`, `api_calendar.go:123`,
  `api_room_calendar.go:176`: call sites unchanged; they inherit the new
  `scopedOccurrenceReads`.
- Contact-history presenters that render `occurrence_access` targets
  follow the `TargetType` rename (tests at `api_contact_history_test.go:241`
  show the surface). Pre-live, no stored rows carry the old label, so no
  back-compat branch is wanted.

### 7.6 ManagerAddBlock does not fold — deviation, stated

The audit line says fold both tables into `registrations`
(`docs/AUDIT-2026-11-01.md:153`). For `ManagerAddBlock` that is not
possible without losing the safeguard: a block is keyed by (subject,
organizer) (`migrations/00031_contact_add_requests.sql:102`) and must
refuse adds to *future* dates for which the subject has no registration
row yet. There is no registration column that can hold it. **Decision:
`manager_add_blocks` survives the squash unchanged** (model, table,
unique triple, `policy.ManagerAddBlocked`), and DEC-138 records that the
fold covers `OccurrenceAccess` only. Deleting the block concept instead
would silently drop a DEC-31-family control; any reviewer who wants that
must escalate as a product decision, not smuggle it into S4 (§14 Q1).

### 7.7 Goose bookkeeping after the squash

- Fresh database: goose creates its version table and applies `00001`
  once. Boot path unchanged (`db.go:51` under the advisory lock,
  `migrations/migrations.go:25`); `go:embed *.sql`
  (`migrations/migrations.go:15`) unchanged.
- Existing dev database (version 62 in its version table): the new binary
  knows only version 1, so `migrate status`/`up` against it error. That
  is expected and final — **recreate it.** S2's PR description must carry
  the exact commands, run by the author before the PR opens: drop the dev
  database (dev compose volume / `DROP DATABASE` per the Makefile's dev
  setup), then `make dev`. Test databases need nothing (§2.5): the hash
  change retires old templates automatically. CI starts from an empty
  service database each run.
- Numbering after the squash: the next schema change is
  `00002_<name>.sql`. The baseline header comment records the replaced
  head version so `00002` never looks like the project's second-ever
  migration. Until the DEC-138 window closes, S3/S4 edit `00001`
  directly; after it closes, `00001` joins the never-edit rule forever
  (§8 text says so in `AGENTS.md` itself).

## 8. Exact replacement texts (land verbatim in S1)

### 8.1 `AGENTS.md`, "Phasing high-complexity work"

Replace the whole expand/contract bullet ("Once live, schema changes use
expand and contract: 1. … 2. … 3. …") **and** the following line ("Don't
rename columns at expand; … contract can follow right away") with:

> - Schema changes are additive first (DEC-138). A release may add tables,
>   columns, indexes and constraints and backfill data, but never drops or
>   renames anything the previous release still reads or writes. Code in
>   that release writes the new shape and reads new-then-old. The next
>   release may drop the old shape (the contract step): its migration names
>   the expand migration it contracts, and CI fails when that expand is not
>   in an earlier tagged release. There are no audit triggers on old
>   columns and no waiting period: one binary runs at a time
>   (`docker compose up`), so the previous release is the only old version
>   that can still be running.
> - Never edit a migration any tagged release has applied. Before the first
>   church, DEC-138 allows editing the baseline migration directly; that
>   window closes at the first tagged release or first church deployment,
>   whichever comes first.

Also update the one-line Migrations rule in Conventions ("every schema
change is a new goose file; never edit an old one") to end with: "; the one
exception is the pre-live baseline window in DEC-138."

### 8.2 PLAN 14.1 (the expand/contract bullet, `docs/plan/PLAN.md:1515`)

Replace that bullet with:

> - Schema changes are additive first, then contracted in the next release
>   (DEC-138, which replaced the Oct 22 14-day expand/contract rule). An
>   expand migration only adds and backfills; the same release reads
>   new-then-old. A contract migration names its expand migration, and CI
>   fails unless that expand shipped in an earlier tagged release. Before
>   the first church, DEC-22 and DEC-138 together allow editing the single
>   baseline migration directly; the window closes at the first tagged
>   release or first church deployment. After that, migrations are never
>   edited again.

And in the same PLAN edit: the "Done means" line "Schema changes are new
goose migrations, never edits to old ones" gains the same DEC-138 exception
clause; C1 (`docs/plan/PLAN.md:167`) gains one sentence: "The baseline was
regenerated once before the first church (DEC-138); it is frozen like any
other applied migration from the first tagged release on." Update the
three stale decision counters (`docs/plan/PLAN.md:3`, `:10`, `:98`, today
"through DEC-134" although DEC-136 exists): already updated to DEC-145 when the audit DECs landed; keep them at the true highest DEC.

### 8.3 DEC-138 (new row in PLAN §15, after DEC-136)

> | DEC-138 | **One baseline before the first church; additive-first after**
> (Grier, Nov 1, 2026; audit R9). While DEC-22 holds and no church has data
> in Fold, the 61-file migration chain is replaced by a single generated
> baseline, proved equal to the chain by a `pg_dump` diff. In the same
> window, `events.rrule_until` and `event_occurrences.original_date` become
> real `date` columns, while `events.starts_local` and `events.tzid` stay
> wall-clock text with CHECKs (the wall clock is the truth, C2), and
> exact-date access provenance moves from `occurrence_accesses` onto
> `registrations` (`access_*` columns; ended grants live in the audit log;
> `manager_add_blocks` stays, being organizer-scoped; DEC-74 and DEC-91
> behavior unchanged). Editing the baseline is allowed only until the first
> tagged release or first church deployment, whichever comes first. From
> then on, schema changes are additive first and contracted in the next
> release, replacing the Oct 22 14-day expand/contract rule: one binary
> runs at a time, so no audit triggers or waiting period are needed. No
> conversion from a pre-squash database is offered; dev databases are
> recreated. | C1, C2, 5.3, 14.1, M0.1, M1.1a, M1.11, M1.14 |

This row landed in PLAN as DEC-138 (DEC-137 went to PR #236's branding
decision); §8.1/§8.2's `AGENTS.md` and PLAN 14.1 texts are the parts of S1
that still land with the squash itself.

## 9. Steps / slices

High-complexity phasing applies (database + Go, 3+ tables touched). Each
slice is one PR, merged in order; no slice ships UI, so the usual
database → API → UI order collapses to database+Go per slice (the drift
test couples them and must be green at every step).

**Pre-flight checklist (before S1 opens):**

- [ ] §4 gate items 1–4 gathered; item 5 requested from the owner.
- [ ] `git status` clean on the branch base; record base SHA and head
      migration version (`ls migrations/*.sql | wc -l`, highest number).
- [ ] `pg_dump --version` major = 16, matching the scratch server.
- [ ] Freeze announced: from S2 opening until S4 merges, no other PR adds,
      edits, or renumbers a file in `migrations/` (§13).
- [ ] Scratch databases `fold_squash_old`/`fold_squash_new` created outside
      any dev volume that matters.
- [ ] Full local green on the base: `go build ./...`, `go vet ./...`,
      `make test` — so later reds are attributable to the squash.

**S1 — Decision and rules (docs only).** Files: `docs/plan/PLAN.md` (DEC-138
row §8.3, 14.1 and C1 and counters §8.2), `AGENTS.md` (§8.1). State in the
PR that the baseline-editing window opens only when S2's gate evidence is
in its description; S1 alone authorizes nothing on its own. Proof:
the docs wording guard still passes; PLAN/AGENTS contain no
contradictory expand/contract text (search both files for "expand" after
the edit — the only hits may be the new DEC-138 passages and `events.Expand`
the function).

**S2 — Pure squash (no schema change, no Go logic change).** Files: delete
`migrations/00001_*.sql` … `migrations/00062_*.sql` (all 61); add
`migrations/00001_baseline.sql` from §5; add the dump/diff script under
`scripts/`; rewrite `migrations/migrations_test.go` version-pinned tests
(see below). Nothing in `models/` changes, so `TestNoDrift`
(`migrations/migrations_test.go:662`) is the coupling proof.
Test rewrite rule: keep every constraint assertion that describes the
*final* schema and run it against a baseline-migrated DB; delete only the
intermediate-version machinery (`UpTo`/`DownTo` to numbers that no longer
exist, and downgrade-refusal cases whose refusal lived in a deleted Down).
Named examples: `TestContactRequestsConstraints` keeps its constraint
matrix, loses the `DownTo(29)` tail (`migrations/migrations_test.go:164`);
`TestOccurrenceAccessDepartureOriginMigration` (`:177`),
`TestDepartureOccurrenceAccessExpiryMigration` (`:301`),
`TestDepartureAccessSupersessionMigration` (`:345`), and the tenure tests
(`:452`, `:477`) keep their insert/refusal matrices against the final
tables and lose their version travel. Add one new test: baseline `DownTo`
0 refuses while a user row exists, succeeds on an empty DB, and `Up`
restores (§5.4). Test files stay beside their source, per the repo rule —
no milestone-named test file.

**S3 — Date types (§6).** Files: `migrations/00001_baseline.sql` (two
column types, two CHECKs), `models/models.go` (`RRuleUntil`,
`OriginalDate`, the two boundary helpers), `internal/events/extend.go`,
and every handler the `RRuleUntil`/`OriginalDate` search surfaces
(event create/edit reads, occurrence row building, date-page query at
`api_occurrence_access.go:350`, JSON rendering). `internal/events`
`Spec`/`When`/`Expand` keep string dates internally (§6.2). New unit test
for the format helper's zone discipline (§6.2).

**S4 — Provenance fold (§7).** Files: `migrations/00001_baseline.sql`
(`access_*` columns, CHECKs, indexes, `registration_access_provenance()`,
re-pointed `add_request_completed()`, `occurrence_accesses` and
`occurrence_access_provenance()` removed), `models/models.go`
(`Registration` fields; `OccurrenceAccess` struct and its `All()` entry
deleted), and the §7.5 writer/reader files. `internal/policy` function
bodies and `internal/policy/occurrence_access_test.go` stay unedited.
Migration tests from S2 that inserted into `occurrence_accesses` are
rewritten onto `registrations` `access_*` with the same refusal matrix
(including: blocked manager add, direct teen add, forged issuer/request,
wrong departure actor/membership, repeat direct add after an end, request
predating the latest end). Handler/occurrence/departure test suites keep
their scenarios; only their storage assertions move to the registration
row (for example `savedOccurrenceAccess` in
`api_occurrence_mutations_test.go:86` becomes a registration reload).

**S5 — Close-out (rides in S4's PR unless it grows).** `docs/API.md` only if
it names `occurrence_accesses` or migration numbers (search it; JSON itself
is unchanged, so expect at most a storage note). Confirm `STATUS.md` needs
no milestone line for this (it is an audit change, not a milestone);
record the four merge SHAs and the §4 attestation link in the S4 PR.

## 10. Verification proof

Run per slice; quote outputs in each PR, not summaries.

1. **Dump diff (the load-bearing proof).** Script from §5.1:
   `pg_dump --schema-only --no-owner --no-privileges` for
   `fold_squash_old` (old chain, frozen base) and `fold_squash_new`
   (candidate baseline), normalize both by the §5.2 deletions (including
   stripping `goose_db_version` DDL from both sides), then `diff -u`.
   - S2: diff must be **empty**. Any hunk stops the slice (§12).
   - S3: hunks only for `rrule_until`, `original_date`, the two new CHECKs.
   - S4: hunks only for `registrations` (+ its CHECKs/indexes/trigger),
     `occurrence_accesses` removal, `occurrence_access_provenance` removal,
     `registration_access_provenance` addition, `add_request_completed`.
   Paste the full diff (or its hunk headers + line count when huge in S2's
   empty case: `diff` byte count 0) into the PR.
2. **Drift:** `go test ./migrations/ -run TestNoDrift` → no recorded
   CREATE/ALTER. Also run the whole `go test ./migrations/`.
3. **Full suites:** `make test` (real Postgres, sharded root package),
   `cd web && npm test`, `make e2e`. E2e must be green with zero spec
   edits — the wire never changed, so a spec edit in these PRs is a smell
   to explain, not a fix to slip in.
4. **Quick sanity (AGENTS, before every push):** `go build ./...`,
   `go vet ./...`, `make lint-go`, `(cd web && npx tsc --noEmit)`,
   `(cd web && npx knip)`.
5. **Behavior spot-proofs for S4** (existing tests, named so a reviewer can
   re-run them): the departure suite (`api_departure_test.go`, including
   the boundary reads at `:703`), the occurrence-access read suite
   (`api_occurrence_access_test.go`, `scopedOccurrenceReads` cases at
   `:273`), DEC-91 series tests (series-cancelled date returns and tells
   registrants — find via `CancelSourceSeries`), and one reminder test
   proving a kept-date registrant still gets the reminder through
   `reminderReaders` (`reminders.go:262`).
6. **Fresh-boot proof:** on an empty database, `go run . migrate up`, then
   boot the server and hit `/healthz`; then `fold migrate status` shows
   exactly version 1 applied. Repeat the status check on a recreated dev
   database using the PR's documented commands.

## 11. Acceptance checklist

- [ ] §4 gate evidence and the owner's attestation sentence are in the S2
      PR description; DEC-138 (§8.3) is in PLAN §15 with counters updated.
- [ ] `migrations/` holds exactly one SQL file, `00001_baseline.sql`, whose
      §10 dump diff against the frozen old chain is empty (S2) and then
      intended-hunks-only (S3, S4). `migrations/migrations.go` unedited.
- [ ] `TestNoDrift` passes; `models.All()` lists 37 tables;
      `models.OccurrenceAccess` no longer exists; `ManagerAddBlock` does.
- [ ] `events.rrule_until` and `event_occurrences.original_date` are
      `date`; `starts_local`/`tzid` are text with the §6.3 CHECKs; JSON and
      URL date rendering unchanged (`make e2e` green, zero spec edits).
- [ ] Exact-date grants live on `registrations.access_*`; the refusal
      matrix (blocked, teen direct, forged provenance, repeat,
      stale request, wrong departure authority) is refused by CHECK or
      trigger with the same outcomes as the deleted function.
- [ ] `internal/policy/occurrence_access_test.go` passes **unedited**;
      DEC-74 boundary (departure grant ends at occurrence end) and DEC-91
      (series-cancelled date returns, registrants told) tests pass.
- [ ] Audit add/end rows carry full provenance `Meta` (§7.5); dedupe keys
      include the grant/end stamp; history presenters use `"registration"`.
- [ ] Baseline Down refuses on a database with a user row and succeeds on
      an empty one; down/up test passes. No converter exists anywhere.
- [ ] `make test`, `cd web && npm test`, `make e2e`, `make lint-go`, `tsc`,
      `knip` all green on S4; the four slice PRs each quote their §10
      outputs.
- [ ] Dev-database recreation commands were run by the S2 author and are in
      the S2 PR description; a fresh `make dev` reaches `/healthz`.
- [ ] House review rule met before any merge: CI green, separate-agent
      review of UI/UX (expected: not affected — say so explicitly),
      database/performance, security, and correctness, findings kept with
      each PR.

## 12. Risks, rollback, stop conditions

**Risks.**

- *Silent schema drift in the dump* (the big one): a missed function body
  or a `pg_dump` version quirk changes behavior invisibly. Mitigated by the
  empty-diff rule, `TestNoDrift`, and keeping trigger/function bodies
  byte-identical except the §5.3 list. Residual risk: a divergence that
  dumps identically but behaves differently is not possible for DDL; data
  defaults and sequence starts are schema, and both sides start empty.
- *Fold changes a safeguarding outcome*: the provenance trigger is the
  database's last word behind `internal/policy` for teen/block/repeat
  rules. Mitigated by porting its refusal matrix into tests before
  deleting it (S4 rewrites S2's ported tests, never writes from memory)
  and by keeping policy bodies and their tests unedited.
- *`date` scan shifts a day*: a session `TimeZone` other than the server
  default could shift a scanned `date` if any code converts zones.
  Mitigated by the field-wise format helper + its non-UTC test (§6.2).
  CI/dev run UTC, so a zone-converting bug would hide until a church VPS
  differs — the unit test is the real guard; reviewers must not waive it.
- *Freeze friction*: four PRs holding `migrations/` hostage stall other
  audit work. Mitigated by §13 ordering and by S2 being mergeable on its
  own (pure squash is a complete, shippable state; S3/S4 could stop there
  with the window still open).
- *Someone runs baseline Down on a real install later*: mitigated by the
  §5.4 users-row guard. After go-live the guard, not operator care, is
  the protection.

**Rollback = revert; there is no data rollback.** Before merge: close the
PR. After merge but before the first tagged release / first church:
`git revert` the slice merges in reverse order; dev databases are
recreated either way, and no other database exists (§4). After the window
closes, revert is forbidden — a live church database sits at baseline
version 1, and going back would strand it. From then on, fixes are new
migrations under the additive-first rule. Never use goose down as
rollback.

**Stop conditions — halt and escalate to the owner, do not improvise:**

1. Any §4 gate item fails, or the attestation is not given.
2. The S2 dump diff is non-empty for any reason not on the §5.2 list, or
   any slice's diff shows a hunk outside its §10 allow-list.
3. `TestNoDrift` reports a CREATE/ALTER that is not one of the §6/§7
   intended changes.
4. The baseline needs a hand edit beyond §5.2 to apply cleanly (for
   example goose will not split a dumped construct). Fix the generator
   script or stop; never hand-massage the output file.
5. A migration-adding PR merged during the freeze, or the head version
   moved after the dump. Re-freeze at the new head and regenerate; never
   patch the baseline to "also include" it by hand.
6. Preserving a refusal from the old provenance trigger seems to require
   weakening a policy function or widening who can see/attend a date.
7. Any e2e spec needs an edit to pass, or any JSON field changes shape.
8. The fold appears to require deleting `manager_add_blocks` or letting
   provenance attach to a household-person registration.
9. A tagged release appears, or anybody reports a Fold deployment outside
   development, at any point before S4 merges. The window has closed;
   remaining slices convert to ordinary migrations under owner direction.

## 13. Dependencies and sequencing

- **SEC1 first.** The setup-takeover fix (audit §Security summary, sequence
  step 1) precedes any public deployment regardless; it needs no schema
  change expected here, but if it adds one (for example a setup-token
  table), it lands before the freeze or after S4 as `00002`.
- **R5/R6 may precede the freeze** (audit sequence step 2,
  `docs/AUDIT-2026-11-01.md:262`). R6's queued delivery table already
  exists (`docs/AUDIT-2026-11-01.md:125`); if R5 or R6 still adds a
  migration, the dump simply includes it — recount files and head version
  at freeze (pre-flight). Once S2 opens, the freeze binds them too.
- **Schema-adding audit items wait: R2** (CSV import — import staging, if
  any), **R3** (TOTP secret columns on `users`), **R6** (any outbox
  columns beyond today's tables), **R10** (new `Attendance` table, PLAN
  5.3 `docs/plan/PLAN.md:325`), **R14** (sign-up sheet tables, PLAN 5.3
  "Later" `docs/plan/PLAN.md:372`). Rule: they merge before the freeze —
  moving the dump base — or after S4 as `00002+`. The audit's own order
  puts R9 before R2/R10 (`docs/AUDIT-2026-11-01.md:263`); follow it unless
  the owner reorders, in which case the pre-flight recount is the only
  change needed here.
- **R1 interacts with the gate, not the schema.** Publishing the first
  GHCR image does not itself close the window, but it destroys §4 item 1's
  easy proof. Sequence R1's image publish after S4, or re-prove item 1
  against the registry at freeze time.
- **R11 is independent** (query shapes, no schema except possibly
  `idx_jobs_running_lease`). If R11 wants that index during the freeze, it
  waits and lands as `00002`; do not fold it into the baseline "since the
  file is open".
- **R4** (erasure) reads the final model list; it should inventory 37
  tables, so R4's inventory is easier after S4. No hard ordering.

## 14. Open questions, with defaults

Defaults govern unless the owner answers differently before the named
slice opens. None blocks S1.

- **Q1 (before S4).** Keep `manager_add_blocks` as its own table (§7.6),
  accepting that the fold covers `OccurrenceAccess` only? **Default: yes,
  keep it.** The alternative — deleting blocks — drops a safeguarding
  control and needs an explicit product decision, not a schema PR.
- **Q2 (before S3).** `starts_local` as text + CHECK (this brief) or
  `timestamp without time zone`? **Default: text + CHECK** (§6.3
  reasoning). Switching later remains possible as an ordinary migration.
- **Q3 (before S4).** Rename `policy.OccurrenceAccess` and friends to
  registration-flavored names in S4, or keep names and record the debt?
  **Default: keep names** — smallest diff, policy tests pass unedited as a
  required proof; a rename can ride a later no-behavior-change PR.
- **Q4 (before S1 merges).** DEC number: 137 assumes no sibling brief
  takes it first. **Default: 137;** renumber at merge if taken, updating
  §8 texts and this brief's mentions in the same PR.
- **Q5 (before S4).** Audit/notify `TargetType`/`SubjectType` rename
  `"occurrence_access"` → `"registration"` (this brief) vs keeping the old
  label pointing at registration IDs? **Default: rename.** Pre-live there
  are no stored rows to stay compatible with, and a label that names a
  deleted table will mislead every future reader of contact history.
- **Q6 (before S2).** Four PRs (S1 docs, S2 squash, S3 types, S4 fold) or
  fold S1 into S2? **Default: four.** S1 is reviewable in minutes and
  makes the exception explicit before any migration file is touched; if
  the owner prefers three, S1's texts ride in S2 unchanged.
- **Q7 (before S2).** Where do the dev-recreate commands live long-term —
  PR description only (this brief, §3) or also a `Makefile` target?
  **Default: PR description only.** A permanent "reset my dev database"
  target is handy but is a separate small decision; do not grow S2 for it.
