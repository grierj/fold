# R9 — Squash migrations into one baseline (pre-live window)

| Field | Value |
|---|---|
| Audit item | R9 [CHANGE], `docs/AUDIT-2026-11-01.md:151` |
| Priority | P1 (audit sequence step 3, `docs/AUDIT-2026-11-01.md:263`) |
| Effort | L |
| Status | Approved 2026-11-01. R7 and R8 were rejected; they give this brief nothing. M7.7 claimed in `STATUS.md` Nov 3, 2026. S1 (this documentation revision) changes no migration; S2–S4 wait on the §9 squash preflight. |
| New decision | DEC-138 (draft text in §8; landed in PLAN Nov 2026 — PR #236 took DEC-137 for branding first, so the audit block runs DEC-138–DEC-145) |

This brief is the one explicit exception to "never edit an old migration"
(`AGENTS.md` Migrations rule, `docs/plan/PLAN.md` C1). It deletes the whole
migration chain and replaces it with one baseline. That is only safe before
any church holds data in Fold. DEC-138 (§8) records the exception, closes the
window at the first tagged release, and replaces the 14-day expand/contract
process with additive-first + next-release contract. If the gate in §4 cannot
be passed, this brief must not be implemented at all.

The work is four reviewed PRs, in order (§9): **S1** documents the sequence,
the owner's statements and the agreed technical corrections (documentation
only; no migration or operative rule changes); **S2** generates a
schema-equivalent baseline and activates the replacement rules; **S3** changes
the two calendar-date columns; **S4** folds exact-date provenance into
`registrations`. This text was revised in S1 on Nov 3, 2026 against the base
`f782fac72901ca365c1b8737bc9236d5d0ebf505`; where it differs from the Nov 1
version (chain size, gate wording, preflight timing, the §5–§7 technical
corrections), this text governs.

## 1. Goal

1. Replace the 63 migration files (versions 1–64; there is no 00021) with one
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

- 63 SQL files in `migrations/` at the S1 base, `00001_baseline.sql` through
  `00064_notification_delivery_outbox.sql` (00021 was never used). The two
  newest are `00063_logout_version.sql` (R5) and
  `00064_notification_delivery_outbox.sql` (R6); the baseline must contain
  their schema. The audit's "61 migrations"
  (`docs/AUDIT-2026-11-01.md:152`) is a historical snapshot: 61 files, head
  version 62, taken before R5 and R6 added schema. The current inventory is 63
  files, head version 64. Recount at freeze time (§13); anything else that
  lands first moves the number again.
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
of, and the runner does not flag it (goose ignores database-only versions
and sees version 1 already applied). §7.7 makes recreating it an explicit
documented operational step, not an enforced refusal.

## 3. Scope

### In

- DEC-138 (already in PLAN §15) and the replacement passages in §8, which
  S1 reviews and S2 activates with the squash.
- Delete the whole current chain (63 SQL files at the S1 base; the freeze
  manifest in §9 fixes the final list); add one
  `migrations/00001_baseline.sql`, generated by the §5 recipe.
  `migrations/migrations.go` unchanged.
- Two type upgrades and two CHECKs (§6), with every Go caller they touch.
- `access_*` columns on `registrations`; delete `occurrence_accesses`, its
  indexes, and `occurrence_access_provenance()`; add
  `registration_access_provenance()`; re-point `add_request_completed()`.
  Rewrite the §2.3 writers and readers onto the columns (§7.5).
- Remove `models.OccurrenceAccess` from `models.All()`
  (`models/models.go:1037`). Table count 38 → 37. `ManagerAddBlock` stays.
- Rewrite version-pinned migration tests as final-schema constraint tests
  (§9), plus the dump-diff proof as a repeatable script.
- Dev-database recreation commands, verified by running them on a scratch
  Compose project (§7.7), in the S2 PR description and in
  `docs/DEPLOY-DIGITALOCEAN.md`-adjacent dev docs only if a dev-reset passage
  already exists there; otherwise PR description only.
- S1 itself: documentation only (`STATUS.md`, PLAN 14.10, this index and this
  brief). It edits no migration, model, Go file, test, `AGENTS.md` or
  operative PLAN rule.

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
- Dropping, resetting or modifying the owner's database. The owner's rows are
  disposable (§4), but that records a fact; it does not authorize anyone else
  to act on that database. Recreating it is the owner's step.
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
fact, not a code fact, so the gate is evidence plus the owner's own
statements, with the final evidence recorded in the S2 PR description.

The gate has two timings. Items 1–5 below record what the S1 preflight
established on Nov 3, 2026; S1 only writes it down and opens nothing. The
refresh list after them must be re-run when S2 opens (§9, squash preflight),
and any failure stops the work (§12). S1 supplies no workaround and no
converter if that gate later fails.

1. **No release exists, and no image was ever published.** The owner said:
   "No Fold image has ever been published". That answer resolves the
   publication fact; it is not asked again and no credential change is
   requested. Retained evidence at the S1 base is consistent with it but does
   not prove it: git has no tags, GitHub shows no releases and no
   deployments, and the `release` workflow (added Nov 2, 2026) has no retained
   runs. Those are retained records only. They cannot show that nothing was
   ever published by hand or deployed outside GitHub. The container registry
   gave no answer: the credential available to the preflight lacks
   `read:packages`, so package listing was refused (403), a package lookup
   answered 404, an anonymous manifest request answered 401 and an anonymous
   token request answered 403. Those errors do not establish that the image
   is absent, so the registry is not the evidence here; the owner's statement
   is.
2. **No first-church checks have been run.** `docs/FIRST-CHURCH-CHECKS.md`
   lists its hand-checks as not run (audit evidence
   `docs/AUDIT-2026-11-01.md:136`). Any checked item that implies a live
   deployment closes the gate.
3. **No production database is reachable from the repo.** The repository
   configures separate development and CI URLs and a production Compose URL,
   so reading configuration alone cannot prove that every destination is a
   development database. Items 4 and 5 supply that proof. There is no hosted
   instance: "SaaS" here means many future single-tenant deployments, and none
   has been provisioned; the DigitalOcean script "never calls `doctl` and
   never creates droplets" (AGENTS, Deployment).
4. **Row-count check on every database the owner knows of.** The only
   identified durable Fold database is the owner's Compose database
   (`fold-db-1`); `SELECT count(*) FROM users;` returned 2 rows. The owner
   calls all rows disposable (item 5). The base checks ran on a separate,
   freshly created PostgreSQL 16 container with no named volumes, removed
   afterwards, never on `fold-db-1`.
5. **Owner attestation, in the owner's words.** The owner (Grier) said,
   recorded Nov 3, 2026:
   - "There are no other churches or other developers using fold at this
     time. The only known durable fold database is in my docker compose"
   - "All rows are disposable development/test data"
   - "No Fold image has ever been published" (item 1)

   The host confirmed the meaning of the first two statements. They are the
   attestation, as spoken. The Nov 1 version of this section demanded one
   exact sentence; that demand is dropped, and its wording was a drafting
   preference, never an owner quotation. **Disposable is a fact about the
   data, not permission to act on the database.** Nothing in this brief drops,
   resets, rewrites or otherwise touches the owner's database; every proof
   runs on scratch databases (§5.1, §7.7).

**Other S1 base evidence.** The base is clean at
`f782fac72901ca365c1b8737bc9236d5d0ebf505`. On the isolated PostgreSQL 16
container, `go build ./...`, `go vet ./...` and `make test` (975 root tests in
eight shards, plus the other packages) all passed. `pg_dump` 16.15 was
available in a `postgres:16-alpine` container, matching the server major. The
earlier preflight blockers about the exact attestation wording and about
missing tooling are **superseded** by this evidence and are not open.

**Refresh when S2 opens (mandatory, not re-asking the owner).** Re-check tags,
releases, deployments, workflow runs and the first-church checklist and
source ownership at S2's opening; record the frozen SHA, the complete
migration manifest, its file count and the highest version; and confirm
nothing was published or deployed since S1. Cite the owner statements above
without requesting them again. Anything that contradicts them, a published
image or a deployment, closes the gate.

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

1. Start a scratch PostgreSQL 16 server with no durable volume and create two
   empty databases on it: `fold_squash_old`, `fold_squash_new`. These are
   separate disposable proof databases. Never use the owner's `fold-db-1` or
   its volume as a proof database, and never a database that holds anything
   the owner might want.
2. **Generation mode: migrate and dump only `fold_squash_old`.** Run the
   frozen pre-squash tree against it:
   `DATABASE_URL=...fold_squash_old go run . migrate up`. Confirm head
   version with `go run . migrate status` and record it in the PR.
   `fold_squash_new` stays genuinely empty (no goose table, no schema
   objects): migrating it with the old chain first would leave version 1
   recorded, and the candidate baseline would then be silently skipped (§7.7).
3. Dump `fold_squash_old` (generation mode):
   `pg_dump --schema-only --no-owner --no-privileges`.
   The script accepts an explicit `pg_dump` executable or argument override
   (for example a command that runs the `pg_dump` inside a PostgreSQL 16
   container) and never builds that command with `eval`. For each source and
   destination it compares the client's major version with the server's
   `server_version_num` and refuses a mismatch: a different `pg_dump` major
   rewords defaults and breaks the §10 diff without any schema change. Before
   generating, it also checks that the database is a proof database, that the
   source is the frozen one and that the migration manifest matches.
4. Apply the §5.2 transforms — mechanically, in the script, so the file is
   regenerable. Generation and proof are separate modes of the script. After
   S2 merges, the script must not regenerate over S3 and S4 edits; it
   remains as the §10 proof tool only.
5. **Apply and prove.** Check that `fold_squash_new` is still empty, then
   apply the generated candidate to it through goose (swap the files in a
   scratch checkout, `go run . migrate up`). Before dumping, read its actual
   bookkeeping, not `migrate status`:
   `SELECT version_id, is_applied FROM goose_db_version ORDER BY id`. It
   must hold exactly goose's initial version 0 row and version 1 applied,
   and no version above 1. Anything else (including a missing table) stops
   the proof. Only then dump `fold_squash_new`, normalize both dumps and run
   the §10 diff. Retain the raw dumps, the normalized dumps, the source
   manifest, the stored-version rows and the full diff as evidence bound to
   the tested revision.
6. **Proof-only mode** (later slices and reruns, no regeneration): migrate a
   fresh `fold_squash_old` with the frozen tree, and a separate genuinely
   empty `fold_squash_new` with the committed tree; run the stored-version
   check of step 5 on `fold_squash_new`; then dump, normalize and diff.

### 5.2 Cleanup rules (the complete list; anything else stays byte-identical)

- Exclude the `goose_db_version` bookkeeping table and its dependent
  objects. Goose creates and owns its version table at run time; a baseline
  that also creates it fails on a fresh database. The script verifies this
  against the pinned goose provider, and the §10 diff strips the table from
  both sides.
- Delete session boilerplate: `\restrict`/`\unrestrict` lines, `SET ...`
  lines, `SELECT pg_catalog.set_config(...)`, and the `-- Dumped from` /
  `-- Dumped by` header comments (they carry version noise).
- Keep both `CREATE EXTENSION` lines, normalized to `IF NOT EXISTS` form,
  at the top of the Up section (`citext`, then `btree_gist`).
- Strip `COMMENT ON EXTENSION` for those two extensions. The old chain never
  issues them (`CREATE EXTENSION` supplies each extension's default comment),
  and a `COMMENT ON EXTENSION` fails when another role owns an extension that
  was created beforehand. Only these two generated lines go; no other comment
  is removed. The default extension comments still appear in both comparison
  dumps and so cancel in the §10 diff.
- Wrap the body in goose annotations: `-- +goose Up` first, then the file
  header comment naming DEC-138 and the frozen head version it replaces;
  `-- +goose Down` before §5.4. No `-- +goose NO TRANSACTION`: plain DDL
  and `CREATE EXTENSION` run inside goose's transaction, and no
  `CREATE INDEX CONCURRENTLY` exists in the dump. The script refuses a
  concurrent index or any other construct it does not recognize, rather than
  letting anyone hand-edit the output.
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
matter.** Down is a tested migration operation, not application rollback.
Shape, generated from the object inventory:

1. A guard first: a statement-wrapped (`StatementBegin`/`StatementEnd`) `DO`
   block that raises an exception when `users` holds any row ("baseline down
   would destroy data; refusing"). This turns an absent-minded
   `fold migrate down-one` (`cli.go:114`) on a real install into an error
   instead of a dropped church. The empty scratch database in the down/up
   test passes the guard.
2. Then explicit `DROP TABLE` lines in reverse dependency order, generated by
   the script from the dump's object list (reviewable; never
   `DROP SCHEMA public CASCADE`), then `DROP FUNCTION` lines, then
   `DROP EXTENSION btree_gist`, `DROP EXTENSION citext`.

There is no down path from the baseline to the old chain, and none from an
old-chain database to the baseline. Rollback before the window closes means
closing the PR or reversing the slice merges (§12), not a goose down.

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
  (dereference + format); the `extend.go:35` query (a validated ISO string
  binding already works; see the binding rule in 6.2); every API read/write
  of `rruleUntil` JSON — wire format stays `YYYY-MM-DD`, parsed once at the
  handler edge with the same rejection the editor returns today. An empty
  until stays `nil`. `events.Spec.Until` stays a `string`
  (`internal/events/expand.go:51`); formatting happens at the persistence
  boundary so `Expand` and its tests keep their shape.

### 6.2 `event_occurrences.original_date`: `text` → `date`

- Baseline: `original_date date NOT NULL`, unique index
  `idx_event_original_date (event_id, original_date)` unchanged in name.
  Model: `time.Time` with `gorm:"type:date;not null;uniqueIndex:idx_event_original_date"`
  (`models/models.go:354`); `EventID` keeps its matching unique-index tag.
  The model declarations are `Event.RRuleUntil *time.Time` and
  `EventOccurrence.OriginalDate time.Time`; no custom `Date` type is
  invented.
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
  helpers next to the model (a `time.Parse("2006-01-02")` wrapper and a
  `Format("2006-01-02")` wrapper that reads the calendar fields without any
  UTC or local conversion) and use
  them in the handlers that build `EventOccurrence` rows from `When` values
  and render rows out, including event row construction, series
  reconciliation, extension deduplication and the `AfterEnsure` `[]string`
  callback. JSON output formats with `2006-01-02`; the wire never changes.
- Binding rule: do not mechanically parse every existing query binding. A
  validated ISO `YYYY-MM-DD` string already works as a pgx text parameter
  (`loadOccurrenceDate` does so today). What needs work is input
  validation: a shape-only check lets an impossible date such as `2026-02-30`
  reach SQL, where it fails with SQLSTATE 22008 and a 500 instead of today's
  400/404. S3 strengthens the validation before SQL so impossible dates keep
  their current status codes and public answers, and inventories every
  equality, range and `IN` binding in the scoped files.
- Scan and output discipline: a Postgres `date` scanned by pgx/GORM arrives
  as a `time.Time` at midnight in the session zone. Replace untyped date scan
  destinations with `time.Time` carriers formatted by the helper, or project
  the value with an explicit `to_char(..., 'YYYY-MM-DD')`. Never use
  `date::text` for an outward string, because the session `DateStyle`
  changes it. The format helper reads year/month/day fields, never converts
  zones, so a session `TimeZone` can never shift the date. This covers
  `extend`'s `Pluck`, `restoreSeriesDates`, the `api_messages` projections,
  `messages_send`, and the `api_room_calendar`, `api_room_series` and
  `api_room_requests` aggregates.
- Dynamic sinks the compiler cannot flag must be formatted too:
  notification `Data` dates (`api_registrations.go`, `api_notifications.go`),
  audit `Meta` dates (`api_room_bookings.go`, `api_occurrence_visibility.go`),
  series results and undo snapshots, departure previews, compose and reminder
  data, URLs and feeds. API DTO declarations, wire field types, omission
  behavior and `web/src/types.ts` stay unchanged.
- Tests: open separate PostgreSQL pools whose DSNs set `Pacific/Kiritimati`
  and `America/Los_Angeles`, and exercise writes, equality/range/`IN`
  bindings and reads in each, including an alternate `DateStyle`; every
  outward value stays exactly `YYYY-MM-DD`. A helper test uses a non-UTC
  value whose UTC conversion crosses a date boundary. Also cover leap dates,
  impossible dates, null until, inclusive final recurrence dates and
  extension idempotence, and assert exact strings in each dynamic
  notification and audit family.
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
  malformed values without the footgun. The CHECK validates shape only;
  `ParseWall` and `LoadZone` keep the semantic validation. DEC-89's caps live in `Expand`,
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
only programmatic readers of ended history are the repeat-add and
stale-request rules, and §7.8 makes those read validated, immutable audit
snapshots rather than the mutable row (a registration row can be replaced
or deleted, so a stamp kept only on the row would be lost with it), and
(b) audit rows are never purged. §7.5 and §7.8 require the audit `Meta` to
carry the full provenance so nothing else is lost. DEC-138 records this
trade.

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
`AccessEndReason string`, `AccessEndedBy *uint`. The string columns carry
`type:text;not null;default:''` tags and the pointer fields stay nullable.
The positive-ID and provenance CHECKs, the request foreign key and the two
partial indexes are SQL-only: tests assert them directly rather than adding
inaccurate GORM relationship or partial-index tags. One partial unique index
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
  no reason added by 00035/00037 is dropped by transcription. Port the
  dumped positive-ID constraints (access issuer, request, departure pair and
  ended-by IDs must be positive when set) with them.

### 7.4 Triggers

- **Delete** `occurrence_access_provenance()` and its trigger with the
  table (origin: `migrations/00031_contact_add_requests.sql:115`, final
  form `migrations/00034_occurrence_access_departure_origin.sql:27`).
- **Add** `registration_access_provenance()`, declared `BEFORE INSERT OR
  UPDATE OR DELETE ON registrations`. It acts only on `access_*` columns
  and on the identity of a provenance-bearing row; every other column of
  the row stays as mutable as today (answers, capacity and guest-count
  updates with unchanged provenance, waitlist stamps must never be frozen by
  this trigger). Rules, ported from the deleted function with the S1
  corrections:
  1. *Immutability.* Once granted (`access_granted_at` set), the grant
     fields (`access_origin`, issuer, request, departure pair,
     `access_granted_at`) cannot change, with one exception: the
     grant-replacement transition in rule 3. Subject and occurrence
     identity are frozen for as long as provenance exists. Once ended,
     `access_ended_at`, `access_end_reason`, `access_ended_by` cannot change
     except through rule 3.
  2. *One-way end.* `access_ended_at` may move null → set once per grant,
     with reason/actor satisfying the end-state CHECK. Set → null is
     refused outside rule 3. This is the old "ending is irreversible".
  3. *New grant overwrites an ended one.* Only an **ended** grant can be
     replaced, and a replacement (like a first grant) must itself start live:
     new `access_origin` + `access_granted_at`, `access_ended_at` back to
     null, reason/`by` cleared, in a single UPDATE. Before the old values are
     overwritten, the matching immutable `occurrence_access.added` and
     `occurrence_access.ended` snapshots for the old grant must exist
     (§7.8); otherwise the replacement is refused. Validate the new grant as
     the old INSERT path did: live account/event/group/occurrence facts;
     `going` required for `manager_add`, `going|maybe|waitlisted` for
     departure origins; manager-add block refusal; teen approval; teen or
     repeat direct adds refused unless `access_request_id` names an exact
     granted `occurrence_add` request. "Repeat" and the stale-request
     boundary are **audit-backed** (§7.8), not `OLD.access_ended_at IS NOT
     NULL`: a repeat exists when the row has ever held a grant, or a
     validated `occurrence_access.added` snapshot for the same subject and
     occurrence exists on any registration. An approved request must not
     predate the latest end across those snapshots and the row's own end;
     equality at the boundary stays allowed (this is
     `AddFacts.LatestAccessEndedAt`,
     `internal/policy/occurrence_access.go:107`, and the old trigger's
     `old_access.ended_at > contact_requests.created_at` refusal at
     `migrations/00034_occurrence_access_departure_origin.sql:76`). Exact
     requester, subject, event, occurrence and completed-target checks stay.
     Departure grants re-prove membership authority against the still-active
     membership, as `migrations/00034_occurrence_access_departure_origin.sql:84`
     and `:94` do today, and skip the manager-block and repeat-approval
     clauses. A first grant on a fresh row (INSERT with `access_origin <>
     ''`, or UPDATE from `''`) takes the same validation, including the
     audit-backed repeat and stale-request checks: a fresh row does not prove
     the subject has never held a grant for that date. Timestamps follow the
     same-row ordering rule in §7.8 when replacing an ended grant:
     `NEW.access_granted_at > OLD.access_granted_at` and
     `NEW.access_granted_at >= OLD.access_ended_at`.
  4. Ordinary answer changes that leave every `access_*` column untouched
     pass without any provenance validation — including on live-grant
     rows, whose reconciliation (ending the grant when the answer stops
     qualifying) stays application logic, as today.
  5. *Delete.* A registration row without provenance deletes freely. A row
     that carries provenance may be deleted only when the grant has ended
     and the matching `occurrence_access.added` and `occurrence_access.ended`
     snapshots exist, keyed by the registration ID and the persisted
     `access_granted_at` stamp, with the `ended` snapshot's end stamp
     matching the row. A live grant can never be deleted. **This deviates
     from the Nov 1 version of this section, which said registration-row
     `DELETE` is not blocked.** The deviation is deliberate: with history
     living in `audit_logs`, an unaudited delete would erase the only record
     the repeat and stale-request rules read. Details, the hard-delete paths
     and why existing operations still work are in §7.8 and §7.9.
- **Re-point** the occurrence branch of `add_request_completed()`
  (`migrations/00031_contact_add_requests.sql:193`) to `registrations`. It
  stays a deferred constraint trigger (`DEFERRABLE INITIALLY DEFERRED`); only
  the occurrence branch changes, and the group branch is untouched. At
  commit it requires a `registrations` row with `id =
  NEW.completed_target_id AND access_request_id = NEW.id AND user_id =
  NEW.subject_user_id AND access_issuer_id = NEW.requester_id AND
  occurrence_id = NEW.occurrence_id`, and checks the event through
  `event_occurrences` joined to `events`. It validates the *completion
  transaction*, not every historical granted request each time a
  registration is later updated. `add_request_identity` already refuses
  changes to a terminal request and stays byte-for-byte as it is, so ending
  and re-granting in a later transaction cannot retrigger an old completion
  by replacing `access_request_id`. The S4 diff shows one function body
  changed (the occurrence branch).

### 7.5 Writers and readers (file-by-file)

Policy package: no signature changes. `policy.OccurrenceAccess`,
`ValidOccurrenceAccessOrigin`, `OccurrenceAccessCurrent`,
`CanSeeOccurrence`, `CanSeeOccurrenceVideo`, `CanEndOccurrenceAccess`,
`CanBlockManagerAdds`, `CanRetainOccurrenceAfterDeparture` keep their
bodies; `internal/policy/occurrence_access_test.go` should pass unedited —
that is a required S4 proof, not a hope. Only fact-loading changes:
`AddFacts.ExistingAccessActive` ← the §7.2 live test on the subject's
**current** registration for the date, never an audit row;
`AddFacts.LatestAccessEndedAt` ← the maximum of that row's `access_ended_at`
and the validated `occurrence_access.ended` snapshots for the same subject
and occurrence across all former registration IDs (§7.8; null when none ever
granted); a repeat exists when the row has ever held a grant or a validated
`occurrence_access.added` snapshot targets a registration for that subject
and occurrence; `AddFacts.Block` unchanged (§7.6). A direct manager re-add
after an end needs approval; an approved request must not predate the latest
end, with equality at the existing stale-request boundary still allowed.

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
  the same row and are built only from persisted values:
  `occurrence-added:<registrationID>:<grantedAtUnixMicro>` and
  `occurrence-ended:<registrationID>:<grantedAtUnixMicro>:<endedAtUnixMicro>`
  (today's keys at `:610` and `:230` embed the access row ID, which no
  longer exists). A notified grant is identified by registration ID plus
  grant stamp wherever notification existence or in-memory suppression is
  checked, because `SubjectID` alone no longer identifies a grant: an old
  notice for the same registration must not suppress a new grant's notice.
  Retry keys, recipient and guardian behavior, `notify.Send` delivery intent
  and rollback on an enqueue failure are unchanged. Other access-ID notice
  references port to registration IDs; the promotion key keeps its own
  RFC3339Nano time component. Audit `Meta` on both add and end is the
  immutable snapshot described in §7.8 — `origin`, `issuerId`, `requestId`,
  `departureMembershipId`, `departureActorId` as applicable, plus
  `subjectUserId`/`eventId`/`occurrenceId`, organizer, registration and the
  integer grant and end stamps — and this is what makes §7.1's history trade
  lossless. `endOccurrenceAccess` (`:188`) sets the
  `access_ended_*` columns; `reconcileOccurrenceAccesses` (`:151`) and
  `occurrenceNoticeAccesses`/`sendOccurrenceNotice` (`:613`) read live
  grants from `registrations`. The block handler (`:310`–`:378`) reads
  provenance from the actor's registration; block creation at `:344` is
  untouched. `liveOccurrenceInvitationIn` (`:236`) takes the registration.
- `api_departure.go`: `recordDepartureOccurrenceAccess` (`:602`) becomes
  the same kind of UPDATE with departure provenance. Supersession (`:677`) on
  one row takes **two UPDATEs** under the existing contact/domain locks while
  the membership is active: first end the live manager grant as
  `departure_superseded` through the no-notice path and write its full
  terminal snapshot; then replace the now-ended grant with the departure
  grant and write its added snapshot. This corrects the Nov 1 sketch of one
  overwriting UPDATE: the trigger never accepts a direct overwrite of live
  provenance, and no live-grant overwrite exception is added. Use one
  normalized operation timestamp for the end and the new grant when it
  satisfies the same-row ordering rule (§7.8). No per-date supersession
  notice is sent and the existing departure summary is unchanged; an
  existing live departure grant is left untouched; audit or notification
  failure rolls back the whole mutation. The `api_departure.go:326` loader
  and `applyDeparture`'s per-date access lookup (`:673`) read the
  registration already loaded at `:670`.
- `api_add_request_reads.go:118`, `registration_subjects.go:133`,
  `api_registrations.go:1126`, `api_registrations.go:1349`: same swaps.
- `reminders.go:294`, `api_feeds.go:198`, `api_calendar.go:123`,
  `api_room_calendar.go:176`: call sites unchanged; they inherit the new
  `scopedOccurrenceReads`.
- Hard deletes of registrations end any live grant first, with an explicit
  reason, so the DELETE rule in §7.4 finds an ended grant and its snapshots:
  `deleteEventRows` (`api_events.go`, also used by group deletion) ends
  remaining live grants as `event_deleted` without notices; `restoreRegistrations`
  (`api_registrations.go`) and series undo (`api_series_registrations.go`)
  end still-live grants as `context_changed` through the normal notice path.
  Do not infer an impending delete from `endReason` against still-live facts,
  and do not rely on reconciliation after the delete to find rows that are
  gone. Already-ended rows keep their original terminal reason. The existing
  pre-mutation expiry reconciliation stays, so moving an expired date cannot
  resurrect departure access.
- Contact-history presenters that render `occurrence_access` targets
  follow the `TargetType` rename and read the §7.8 snapshots instead of the
  old table (tests at `api_contact_history_test.go:241` show the surface).
  Pre-live, no stored rows carry the old label, so no back-compat branch is
  wanted.

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
- Existing dev database (the owner's `fold-db-1` sits at version 64 in its
  version table): **the unchanged runner does not notice it.** The new
  binary ships only version 1, and the old chain already recorded version 1,
  so goose sees nothing to apply. Verified against the pinned module
  (`go.mod`: `github.com/pressly/goose/v3 v3.29.0`, source at
  `$(go env GOMODCACHE)/github.com/pressly/goose/v3@v3.29.0`, here
  `/home/grier/godev/pkg/mod/...`): `Provider.status` (`provider.go:619`)
  walks only the embedded files, so it lists 00001 as applied;
  `Provider.hasPending` (`provider.go:545`) asks
  `gooseutil.UpVersions` (`internal/gooseutil/resolve.go`), which treats a
  version already stored as applied and ignores versions that exist only in
  the database; `Provider.Up` (`provider.go:249`) returns success when
  nothing is pending. `db.go:51` and `migrations/migrations.go:25` add no
  compatibility check. So `migrate status` and `migrate up` succeed against
  the old-chain database without applying the replacement, and the server
  would boot on the old schema. **Recreating that database is a mandatory
  operational precondition, not an enforced automatic refusal.** S2 neither
  claims nor tests an automatic refusal; adding a version or hash guard
  would be a runtime change outside the accepted unchanged-runner scope and
  needs the owner's separate decision. **Recreating the owner's database is
  the owner's step; S2 does not perform it.** Because status output cannot
  tell a recreated database from an old-chain one, S2's proof reads the
  stored `goose_db_version` rows themselves (§10 item 7). What S2 does is
  prove the commands on scratch: its PR description
  carries the exact recreation commands and the owner-adaptable identifiers
  (project, service and volume names), run by the author before the PR opens
  on a scratch Compose project with a distinct project name, database volume
  and ports. Inspect the resolved Compose configuration before any
  destructive command, including explicit container or volume names that
  could defeat project isolation. Recreate only that scratch database, then
  run `make dev` against it and check `/healthz`. No permanent reset target
  is added (§14 Q7). Test databases need nothing (§2.5): the hash change
  retires old templates automatically. CI starts from an empty service
  database each run.
- Numbering after the squash: the next schema change is
  `00002_<name>.sql`. The baseline header comment records the replaced
  head version so `00002` never looks like the project's second-ever
  migration. Until the DEC-138 window closes, S3/S4 edit `00001`
  directly; after it closes, `00001` joins the never-edit rule forever
  (§8 text says so in `AGENTS.md` itself).

### 7.8 History snapshots as the authority for repeat and stale checks

These are the agreed S1 corrections to how §7.1's "history lives in the
audit log" works in practice. They preserve the product rules (a manager
cannot silently re-add someone who left or was removed, and an approved
request cannot predate the latest end); they are not owner policy questions.

- **Scope.** Authority reads use only the `audit_logs` actions
  `occurrence_access.added` and `occurrence_access.ended` with `target_type
  = 'registration'`, through the existing `idx_audit_logs_action`. A third
  action, `occurrence.blocked`, is snapshotted for contact history. Other
  registration-targeted actions (for example `RegistrationChanged`) never
  count as access actions. No replacement history table and no second
  timestamp representation is added.
- **Contents.** Each snapshot is written in the same transaction as its
  transition and is complete: subject, occurrence, event, organizer,
  registration, origin, issuer, request, departure pair, the persisted
  grant stamp and, for an end, the end stamp, reason and ending actor.
  Replacing or deleting provenance requires its snapshots to exist first
  (§7.4 rules 3 and 5).
- **Canonical stamps.** Operation time is normalized to UTC microseconds
  before it is persisted. Snapshots carry integers `grantedAtMicro` and, for
  ended snapshots, `endedAtMicro`, derived from the persisted UTC microsecond
  timestamps. These are the only timestamp fields read for authority.
- **One corruption predicate, in Go and in SQL.** For each audit row in the
  scope above, `subjectUserId`, `occurrenceId` and `grantedAtMicro` are
  required, and an `occurrence_access.ended` row also requires
  `endedAtMicro`. Each required field must satisfy the `historyMetaID`
  predicate: `jsonb_typeof` is `'number'`, its `->>` text matches
  `^[1-9][0-9]{0,18}$`, and its numeric value is at most
  `9223372036854775807`. A missing, null or otherwise invalid required field
  makes the row corrupt. The Go loader uses guarded SQL projections so both
  paths evaluate the same JSONB representation and exact integer bounds,
  with no float conversion. Any corrupt row in this action and target scope
  fails the grant closed with an internal error, even when its subject and
  date cannot be determined or would not match. Valid rows are filtered by
  `subjectUserId` and `occurrenceId` only after that check. Contact-history
  presentation keeps its current behavior of omitting malformed fields.
- **Same-row ordering only.** When a grant replaces an ended one on the same
  row, `NEW.access_granted_at > OLD.access_granted_at` and
  `NEW.access_granted_at >= OLD.access_ended_at` must hold, in the writer and
  in the trigger. An equal-microsecond or backward-clock replacement that
  violates them fails and rolls back; the code does not advance authorization
  time to manufacture success. No ordering is required across a deleted
  registration and a newly created one: registration IDs come from a
  `bigserial` sequence and the three production hard-delete paths (§7.9) do
  not recreate explicit IDs, so a grant's identity remains registration ID
  plus grant stamp, and cross-row history only supplies the repeat and
  latest-end facts.
- **Contact history.** `contactHistoryQuery` replaces the `occurrence_accesses`
  join with validated snapshot projections keyed by the three actions and
  `target_type = 'registration'`: `issuerId` as `requester_id`,
  `subjectUserId`, `requestId`, `eventId` as `context_id`, `occurrenceId` and
  the audit `target_id` as `access_id`; purpose stays `occurrence_add`. Filters,
  pagination, the closed field allow-list, exact decimal-string IDs, CSV
  escaping and header and the `accessId` wire key are unchanged, and there is
  no legacy-target branch.

### 7.9 Hard deletes, foreign keys, test clearing and Down

Making the trigger fire on `DELETE` (§7.4 rule 5) is compatible with the
operations that exist today:

- `fk_registrations_occurrence` and `fk_registrations_user`
  (`migrations/00005_events.sql:51`) are declared without `ON DELETE CASCADE`,
  so no foreign-key cascade deletes registrations behind the trigger's back.
- Exactly three code paths delete registration rows: `deleteEventRows` in
  `api_events.go`, `restoreRegistrations` in `api_registrations.go` and series
  undo in `api_series_registrations.go`. Each first ends live grants and
  persists the snapshots (§7.5), then deletes.
- `internal/testdb/testdb.go` clears tables with
  `session_replication_role = replica` or `TRUNCATE`; neither fires a row
  trigger, so test clearing is unaffected.
- The generated baseline Down refuses when any user exists and otherwise
  uses `DROP TABLE`, which also fires no row `DELETE` trigger (§5.4).

S4 asserts each of these: the trigger includes INSERT, UPDATE and DELETE; the
two foreign keys still have no delete cascade; and testdb clearing and the
baseline Down keep this trigger-independent behavior.

## 8. Exact replacement texts (reviewed in S1; activated in S2)

S1 reviews this wording and changes none of the operative files. The old
expand/contract rule in `AGENTS.md` and PLAN 14.1 describes the tree
correctly until the squash exists, so **S2 activates every replacement below
together with the actual squash** (§9). No `AGENTS.md` edit lands in S1.
DEC-138 and the decision counters are already in PLAN and are not touched
again.

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

All of 8.2 lands in S2, with the squash, not in S1. The bounded baseline
exception in each passage is tied to S2's gate evidence (§4) and ends at the
first tagged release or first church deployment, as DEC-138 says. S2 also
re-checks that no operative text in `AGENTS.md` or PLAN still contradicts
DEC-138 (§11).

### 8.3 DEC-138 (landed in PLAN §15, after DEC-137 branding)

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
decision) and stays as written: its "61-file" figure is the audit snapshot
(§2.1; the chain is 63 files at the S1 base), which is why this brief, not
the decision, carries the current count. §8.1/§8.2's `AGENTS.md` and PLAN
14.1, C1 and "Done means" texts are the parts that still land with the squash
itself, in S2.

## 9. Steps / slices

High-complexity phasing applies (database + Go, 3+ tables touched). Each
slice is one PR, merged in order; no slice ships UI, so the usual
database → API → UI order collapses to database+Go per slice (the drift
test couples them and must be green at every step). S1 touches no database
or Go at all.

**Two preflights, not one.** The Nov 1 text put every preflight item before
S1 opened. That is split here:

- The **documentation preflight** is what S1 needs. It is met (below), so S1
  may proceed now. S1 can describe the gate and the freeze; it cannot start
  either.
- The **squash preflight** is what S2 needs. It is mandatory before S2 opens
  and before anyone edits a file in `migrations/`. Passing the documentation
  preflight authorizes none of it.

**Documentation preflight (S1; met Nov 3, 2026).**

- [x] §4 gate items 1–5 recorded: the owner's statements verbatim, the one
      identified durable database and its row count, and the registry
      evidence limits.
- [x] `git status` clean on the branch base; base SHA
      `f782fac72901ca365c1b8737bc9236d5d0ebf505`; migration inventory 63 SQL
      files through `00064` (`ls migrations/*.sql | wc -l`, highest number).
- [x] `pg_dump` 16.15 available (matching a PostgreSQL 16 server).
- [x] Full green on the base, on an isolated PostgreSQL 16 container:
      `go build ./...`, `go vet ./...`, `make test` — so later reds are
      attributable to the squash.

**Squash preflight (mandatory before S2 opens or any migration edit).**

- [ ] §4 refresh done at S2's opening: tags, releases, deployments, workflow
      runs, first-church checklist and source ownership rechecked; nothing
      published or deployed since S1; owner statements cited, not re-asked.
- [ ] Isolated proof databases: `fold_squash_old` and `fold_squash_new` on a
      scratch server, never `fold-db-1` or any volume that matters (§5.1).
      `fold_squash_new` is empty until the candidate baseline is applied.
- [ ] The migration freeze is announced and enforced, not merely stated. From
      S2 opening until S4 merges, no other PR adds, edits or renumbers a file
      in `migrations/` (§13). Record the frozen SHA, the complete migration
      manifest, its file count and the highest version; if the head moved
      since the S1 base, repeat the base checks on the frozen revision.
- [ ] `pg_dump` major equals the scratch server's major (checked by the
      script, §5.1).

**S1 — Decision, sequence and technical corrections (docs only).** Files:
`STATUS.md` (the M7.7 claim), `docs/plan/PLAN.md` (the 14.10 M7.7 entry),
`docs/plan/briefs/audit-2026-11/00-index.md` and this brief. It records the
four-PR sequence, the §4 owner statements and evidence, the corrected chain
inventory (§2.1) and the agreed technical corrections in §5–§7. It does not
edit `AGENTS.md`, DEC-138's row, the decision counters, PLAN 14.1, C1 or
"Done means", any migration, model, Go file or test, and it does not claim
that baseline generation, migration editing or completion has begun. S1
records the current-chain schema; it does not claim a one-migration fresh
boot. Proof: the docs wording guard passes and the diff is confined to the
four files. If the production-data gate later fails, S2 stops; S1 provides no
workaround or converter.

**S2 — Pure squash (no schema change, no Go logic change).** Opens only
after the squash preflight. Files: delete
`migrations/00001_*.sql` … `migrations/00064_*.sql` (all 63, with 00021
absent); add `migrations/00001_baseline.sql` from §5, including the `00063`
and `00064` schema; add the generator/proof script `scripts/baseline-proof.sh`;
rewrite the version-pinned tests. `migrations/migrations.go`, `db.go`, `cli.go`
and `models/models.go` stay unchanged. Nothing in `models/` changes, so
`TestNoDrift` (`migrations/migrations_test.go:662`) is the coupling proof.
Test rewrite rule: inventory every `UpTo`/`DownTo` in
`migrations/migrations_test.go`, `api_departure_test.go`,
`account_security_test.go` and `cli_test.go`, and give each a disposition.
Keep every constraint or refusal assertion that describes the *final* schema
and run it against a baseline-migrated DB; remove only obsolete version
travel, historical backfill-only scenarios and assertions that belonged to a
deleted Down. Preserve current token, tenure, departure, consent,
household, `cancel_source`, lookup-index, push and CLI refusal behavior.
Named examples: `TestContactRequestsConstraints` keeps its constraint matrix,
loses the `DownTo(29)` tail (`migrations/migrations_test.go:164`);
`TestOccurrenceAccessDepartureOriginMigration` (`:177`),
`TestDepartureOccurrenceAccessExpiryMigration` (`:301`),
`TestDepartureAccessSupersessionMigration` (`:345`), and the tenure tests
(`:452`, `:477`) keep their insert/refusal matrices against the final
tables and lose their version travel. Add tests for the baseline Down:
`DownTo` 0 refuses while a user row exists and preserves data and version,
succeeds on an empty scratch DB, and `Up` restores; the CLI `down-one`
refusal stays nonzero and nondestructive. S2 also **activates the §8
replacement texts**: `AGENTS.md`, PLAN 14.1, C1 and "Done means" (keeping
thin end-to-end slicing, replacing the obsolete 14-day machinery, and bounding
the exception by S2's evidence and the first release or church deployment),
and records the verified scratch-Compose recreation commands in its PR
(§7.7). Test files stay beside their source, per the repo rule — no
milestone-named test file.

**S3 — Date types (§6).** Files: `migrations/00001_baseline.sql` (two
column types, two CHECKs), `models/models.go` (`RRuleUntil`,
`OriginalDate`, the boundary helpers), `internal/events/extend.go`, and every
production file the `RRuleUntil`/`OriginalDate` search and the §6.2 sink
inventory surface, with only persistence-facing fixtures, date-output
assertions and date-binding regressions changed in tests. `internal/events`
`Spec`/`When`/`Expand` keep string dates internally (§6.2). Recheck the
editing window and freeze before starting. No UI, JSON shape, policy or
error-contract change.

**S4 — Provenance fold (§7).** Files: `migrations/00001_baseline.sql`
(`access_*` columns, CHECKs, indexes, `registration_access_provenance()`,
re-pointed `add_request_completed()`, `occurrence_accesses` and
`occurrence_access_provenance()` removed), `models/models.go`
(`Registration` fields; `OccurrenceAccess` struct and its `All()` entry
deleted), and the §7.5 writer/reader files, plus the three hard-delete paths
(§7.9). `internal/policy` function bodies and
`internal/policy/occurrence_access_test.go` stay unedited. Recheck the gate
and freeze before starting. Migration tests from S2 that inserted into
`occurrence_accesses` are rewritten onto `registrations` `access_*` with the
same refusal matrix (including: blocked manager add, direct teen add, repeated
direct add, forged issuer/request/completed target/event/subject, stale
approval, wrong departure actor/membership, sole-leader departure, household
provenance, malformed end states, identity transfer, immutable terminal
fields), plus the DELETE and replacement cases in §10. Handler/occurrence/
departure test suites keep their scenarios; only their storage assertions
move to the registration row (for example `savedOccurrenceAccess` in
`api_occurrence_mutations_test.go:86` becomes a registration reload).

**S5 — Close-out (rides in S4's PR unless it grows).** Update `docs/API.md`
storage and migration references (JSON itself is unchanged). Close the
`STATUS.md` M7.7 row, which S1 claimed, with actual evidence; close-out does
need a milestone entry. After merge, the host records the four merge SHAs
and the §4 attestation evidence and releases the freeze.

## 10. Verification proof

Run per slice; quote outputs in each PR, not summaries. S1 is documentation
only: its proof is that the docs wording guard passes and the diff is
confined to its four files; the items below apply from S2 on. Retain
command outputs bound to the tested revision.

1. **Dump diff (the load-bearing proof).** Script from §5.1, in the order
   it states (old database migrated and dumped alone; candidate generated;
   candidate applied to a genuinely empty `fold_squash_new`; its stored
   `goose_db_version` rows read and found to be only version 0 and version 1;
   then both dumped):
   `pg_dump --schema-only --no-owner --no-privileges` for
   `fold_squash_old` (old chain, frozen base) and `fold_squash_new`
   (candidate baseline), normalize both identically by the §5.2 rules
   (including stripping the goose bookkeeping table from both sides), then
   `diff -u`. An empty diff counts only with the stored-version check
   passed: two old-chain databases also diff empty. Function and trigger bodies must be unchanged except as listed.
   - S2: normalized diff must be **empty** (zero bytes). Any hunk stops the
     slice (§12).
   - S3: against S2 and against the cumulative frozen source, hunks only for
     `rrule_until`, `original_date` and the two named CHECKs
     (`events_starts_local_check`, `events_tzid_check`).
   - S4: against S3, hunks only for `registrations` (+ its CHECKs, indexes and
     trigger), `occurrence_accesses` removal, `occurrence_access_provenance`
     removal, `registration_access_provenance` addition and the occurrence
     branch of `add_request_completed`. Audit history uses the existing audit
     table and action index. The cumulative diff adds only S3's date hunks;
     all unrelated function bodies are unchanged.
   Paste the full diff (or, for S2's empty case, its byte count 0) into the PR.
   Also apply the baseline with `citext` and `btree_gist` pre-created by
   another role and an otherwise appropriately privileged non-superuser
   migration role (S2).
2. **Drift:** `go test ./migrations/ -run TestNoDrift` → no recorded
   CREATE/ALTER. Also run the whole `go test ./migrations/` with no relaxed
   checks and no disabled triggers.
3. **Full suites:** `make test` (real Postgres, sharded root package),
   `cd web && npm test`, `make e2e`. E2e must be green with zero spec
   edits — the wire never changed, so a spec edit in these PRs is a smell
   to explain, not a fix to slip in.
4. **Quick sanity (AGENTS, before every push):** `go build ./...`,
   `go vet ./...`, `make lint-go`, `(cd web && npx tsc --noEmit)`,
   `(cd web && npx knip)`, plus `make deadcode` and `make lint-sh`.
5. **Behavior proofs for S3** (§6.2): the two-zone pool test with alternate
   `DateStyle`, the cross-boundary helper test, exact strings in each dynamic
   notification and audit family and each raw-query family, leap and
   impossible dates, null until, inclusive final recurrence dates, extension
   idempotence, and `TestSeriesEditRestoresSeriesCancelledDates` (DEC-91).
6. **Behavior proofs for S4** (existing tests, named so a reviewer can
   re-run them): the departure suite (`api_departure_test.go`, including
   the boundary reads at `:703`), the occurrence-access read suite
   (`api_occurrence_access_test.go`, `scopedOccurrenceReads` cases at
   `:273`), DEC-91 series tests (series-cancelled date returns and tells
   registrants — find via `CancelSourceSeries`), and one reminder test
   proving a kept-date registrant still gets the reminder through
   `reminderReaders` (`reminders.go:262`). New proofs for the §7 corrections:
   - A direct-SQL DELETE test: a live grant cannot be deleted; an ended
     grant missing its matching added or ended snapshot cannot be deleted;
     a row without provenance deletes freely; an ended grant with matching
     snapshots deletes while its audit history remains. Destructive
     replacement of provenance without matching history is refused.
   - Repeat and stale-request checks survive deletion: disable, re-enable,
     delete without restoration, then direct re-add is refused and an
     approved fresh request succeeds; likewise series-undo deletion and
     recreation. Use newly allocated registration IDs; no same-ID recreation
     fixtures and no cross-row timestamp ordering tests.
   - Same-row ordering with a frozen clock and equal-microsecond
     replacements: a violation leaves no committed grant, audit or notice;
     a later valid replacement succeeds. A real-clock grant/end/re-grant/end
     sequence inside one second produces two added and two ended notices,
     a retry of a committed transition adds none, and an old notice for the
     same registration does not suppress a new one.
   - One table-driven test beside `api_occurrence_access.go` runs the §7.8
     corruption cases (missing or null fields, wrong JSON types, fractional,
     zero and negative numbers, bigint overflow, valid boundary integers for
     the three IDs and the stamps) through the Go loader and the direct-SQL
     trigger; corrupt rows fail closed even when they cannot match, valid
     unrelated rows are filtered normally, and rows outside the two actions
     and the registration target type do not participate.
   - Departure supersession keeps the old full end snapshot and the new
     added snapshot, sends no per-date notice and leaves live departure
     grants untouched; audit or enqueue failure rolls back the mutation.
   - Deferred completion fails at commit without an exact effect (including
     a wrong event context) and succeeds with a valid grant; a granted
     request cannot be modified; later end and re-grant does not invalidate
     earlier committed requests.
   - Explicit-reason endings: `deleteEventRows` keeps its `event_deleted`
     no-notice assertions; restoration and undo end live grants as
     `context_changed`; already-ended rows keep their reason; audit history
     survives event deletion.
   - Contact history keeps issuer, request and origin after re-grant and
     after registration or event deletion, block actions stay discoverable
     by person, `RegistrationChanged` is not misclassified, and large IDs,
     malformed metadata, filters, pagination, CSV safety and the JSON
     shape are unchanged.
7. **Fresh-boot proof:** on an empty database, `go run . migrate up`, then
   boot the server and hit `/healthz`; then `fold migrate status` shows
   exactly version 1 applied **and** `SELECT version_id, is_applied FROM
   goose_db_version ORDER BY id` returns only goose's version 0 row and
   version 1 applied (status alone is not proof: it also reports version 1
   applied on an old-chain database, §7.7). Repeat on the recreated scratch
   Compose development database using the PR's documented commands, reading
   the stored rows there too, and confirm the baseline Down refuses with a
   user row (data and version preserved) and succeeds when empty. Also show,
   on a scratch database migrated with the frozen old chain, that the new
   binary's `migrate up` succeeds without applying anything (stored versions
   still reach 64), so the PR documents recreation as a precondition and
   makes no claim of automatic refusal. The owner's `fold-db-1` is never
   used for any proof.

## 11. Acceptance checklist

**S1 (documentation only).**

- [ ] The diff is confined to `STATUS.md`, PLAN 14.10's M7.7 entry, the index
      and this brief. No migration, model, Go file, test or `AGENTS.md` is
      edited; DEC-138's row and the decision counters are unchanged.
- [ ] M7.7 is claimed in `STATUS.md`; nothing says baseline generation,
      migration editing or completion has begun.
- [ ] §9 separates the documentation preflight (met) from the squash
      preflight (mandatory before S2 opens); §4, §8–§12 and the index agree.
- [ ] The owner's statements appear verbatim and attributed in §4; no exact
      phrase attestation, inventory, tooling or clean-base request is left
      open; the registry limits are stated as limits, not as proof.
- [ ] The current chain is stated as 63 files through `00064` (no `00021`),
      distinguished from the 61-file audit snapshot; S1 records the
      current-chain schema, not a one-migration fresh boot.
- [ ] House review rule met before merge: CI green and a reviewer separate
      from the implementer covers security, database/performance and
      correctness, with UI/UX explicitly not affected.

**S2 (squash).**

- [ ] §4 refresh evidence, the owner statements and the freeze record
      (frozen SHA, manifest, count, highest version) are in the S2 PR
      description. The refreshed gate has no published image, release or
      deployment.
- [ ] `migrations/` holds exactly one SQL file, `00001_baseline.sql`, whose
      §10 dump diff against the frozen old chain is empty (zero bytes)
      after the §5.2 normalization. The extension-comment removal is
      generated and justified, not a hand edit. `migrations/migrations.go`
      is unedited and `models.All()` still lists 38 tables.
- [ ] `TestNoDrift` records no CREATE/ALTER and the whole migrations suite
      passes without relaxed checks; every `UpTo`/`DownTo` call has a recorded
      disposition.
- [ ] Baseline Down refuses on a database with a user row (data and version
      preserved) and succeeds on an empty one; down/up passes; CLI
      `down-one` refuses nonzero and nondestructively. No converter exists.
- [ ] Fresh `migrate up`, boot, `/healthz` and `migrate status` prove exactly
      version 1, and the stored `goose_db_version` rows (read directly) hold
      only goose's version 0 row and version 1 applied; repeated on the
      recreated scratch Compose database with the documented commands. No
      owner database or general-purpose volume was dropped, reset or
      modified. The PR states that the runner does not reject an old-chain
      database (a scratch version-64 database was shown to pass `migrate up`
      untouched), so recreating the owner's version-64 `fold-db-1` is a
      mandatory step for the owner before running the new binary on it, not
      an enforced refusal (§7.7).
- [ ] The §8 replacement texts are active in `AGENTS.md`, PLAN 14.1, C1 and
      "Done means", and a search for "expand" in both files finds only the new
      DEC-138 passages and `events.Expand`: no contradictory operative
      expand/contract text remains.
- [ ] Before each push: `go build ./...`, `go vet ./...`, `make lint-go`,
      `tsc`, `knip`, `make deadcode`, `make lint-sh`; then `make test`,
      `cd web && npm test` and `make e2e` with zero spec edits.

**S3 (dates).**

- [ ] `events.rrule_until` and `event_occurrences.original_date` are
      `date`; `starts_local`/`tzid` are text with the §6.3 CHECKs; the S3
      dump hunks are exactly those two columns and the two named CHECKs,
      against S2 and cumulatively. JSON and URL date rendering are
      unchanged (`make e2e` green, zero spec edits; no UI, JSON-shape,
      policy or error-contract change).
- [ ] The §10 item 5 proofs pass, including the two-zone pools and exact
      strings in every dynamic sink.

**S4 (provenance fold).**

- [ ] Exactly one SQL file remains; `models.All()` lists 37 tables;
      `models.OccurrenceAccess` no longer exists; `ManagerAddBlock` does.
      The trigger is declared `BEFORE INSERT OR UPDATE OR DELETE`, the
      registration foreign keys still have no delete cascade, and testdb
      clearing and baseline Down keep their trigger-independent behavior.
- [ ] Exact-date grants live on `registrations.access_*`; the refusal matrix
      (blocked, teen direct, repeated direct, stale approval, forged
      issuer/request/completed target/event/subject, wrong departure
      actor/membership, sole-leader departure, household provenance,
      malformed end states, identity transfer, immutable terminal fields) is
      refused by CHECK or trigger with the same outcomes as the deleted
      function.
- [ ] `internal/policy/occurrence_access_test.go` passes **unedited**;
      DEC-74 boundary (departure grant ends at occurrence end), DEC-91
      (series-cancelled date returns, registrants told) and the reminder
      regression pass; no group, roster or series rights are granted and
      manager blocks cover future dates.
- [ ] Added, ended and blocked snapshots carry full provenance with canonical
      integer stamps; the Go loader and the SQL trigger agree on the
      corruption predicate; dedupe keys use registration ID plus grant (and
      end) stamp; history presenters use `"registration"`.
- [ ] The §10 item 6 proofs pass: DELETE and replacement need matching
      snapshots, repeat and stale checks survive deletion, same-row ordering,
      two-update departure supersession, deferred completion, explicit-reason
      endings and contact-history retention.
- [ ] `make test`, `cd web && npm test`, `make e2e`, `make lint-go`, `tsc`,
      `knip`, `make deadcode` and `make lint-sh` all green; each slice PR
      quotes its §10 outputs.
- [ ] House review rule met before any merge: CI green (including the
      `compose` job), separate-agent review of UI/UX (expected: not affected
      — say so explicitly), database/performance, security, and correctness,
      findings kept with each PR.

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
  default could shift a scanned `date` if any code converts zones, and an
  impossible date that reaches SQL would turn a 400/404 into a 500.
  Mitigated by the field-wise format helper, validation before SQL, explicit
  `to_char` or carrier formatting, and separate pools in
  `Pacific/Kiritimati` and `America/Los_Angeles` with an alternate
  `DateStyle` (§6.2). CI/dev run UTC, so a zone-converting bug would hide
  until a church VPS differs — those tests are the real guard; reviewers
  must not waive them.
- *History depends on a mutable row*: if repeat and stale-request checks read
  only the registration row, a delete or replacement erases the evidence.
  Mitigated by immutable snapshots required before overwrite or delete,
  the shared Go/SQL corruption predicate that fails closed, and the DELETE
  branch of the trigger (§7.4, §7.8).
- *Freeze friction*: four PRs holding `migrations/` hostage stall other
  audit work. Mitigated by §13 ordering and by S2 being mergeable on its
  own (pure squash is a complete, shippable state; S3/S4 could stop there
  with the window still open).
- *Someone runs baseline Down on a real install later*: mitigated by the
  §5.4 users-row guard. After go-live the guard, not operator care, is
  the protection.

**Rollback is bounded, and there is no data rollback.** Before merge: close
the PR. After merge but before the first tagged release / first church
deployment (the window is still open): reverse the slice merges in reverse
order and recreate authorized scratch databases; no other database holds
anything that matters (§4). **Destroying the owner's database stays
unauthorized** even though its rows are disposable: if the owner's
`fold-db-1` needs recreating, that is the owner's step. After the window
closes, revert is forbidden — a live church database sits at baseline
version 1, and going back would strand it. From then on, remaining fixes are
ordinary new migrations under the additive-first rule; never revert to the
old chain and never use goose Down as application rollback.

**Stop conditions — halt and escalate to the owner, do not improvise:**

1. Any §4 gate item fails at the S2 refresh or any later recheck (a
   published image, release or deployment since S1, a contradicting
   database), or the owner's statements
   are withdrawn or contradicted. S1 provides no workaround or converter.
2. The S2 dump diff is non-empty for any reason not on the §5.2 list, or
   any slice's diff shows a hunk outside its §10 allow-list, or generation
   would need a transformation that is not listed.
3. `TestNoDrift` reports a CREATE/ALTER that is not one of the §6/§7
   intended changes.
4. The baseline needs a hand edit beyond §5.2 to apply cleanly (for
   example goose will not split a dumped construct). Fix the generator
   script or stop; never hand-massage the output file or hand-patch an
   omitted migration into the dump.
5. A migration-adding PR merged during the freeze, or the head version
   moved after the dump. Re-freeze at the new head and regenerate; never
   patch the baseline to "also include" it by hand.
6. Preserving a refusal from the old provenance trigger seems to require
   weakening a policy function or widening who can see/attend a date, or a
   historical refusal is lost, history becomes dependent on a mutable row, a
   notification is silently lost, or a policy is weakened.
7. Any e2e spec needs an edit to pass, or any JSON field changes shape.
8. The fold appears to require deleting `manager_add_blocks` or letting
   provenance attach to a household-person registration.
9. A tagged release appears, or anybody reports a Fold deployment outside
   development, at any point before S4 merges. The window has closed;
   remaining slices convert to ordinary migrations under owner direction.
10. S2 is about to open, or a migration is about to be edited, before the
    squash preflight (§9) is met; or a proof would run against the owner's
    database.

## 13. Dependencies and sequencing

- **SEC1 first.** The setup-takeover fix (audit §Security summary, sequence
  step 1) precedes any public deployment regardless; it needs no schema
  change expected here, but if it adds one (for example a setup-token
  table), it lands before the freeze or after S4 as `00002`.
- **R5/R6 may precede the freeze** (audit sequence step 2,
  `docs/AUDIT-2026-11-01.md:262`). R6's queued delivery table already
  exists (`docs/AUDIT-2026-11-01.md:125`); if R5 or R6 still adds a
  migration, the dump simply includes it. That already happened: `00063`
  (R5) and `00064` (R6) landed after the audit's 61-file snapshot, so the
  S1 base has 63 files. Recount files and head version at freeze (§9 squash
  preflight). Once S2 opens, the freeze binds them too.
- **Schema-adding audit items wait: R2** (CSV import — import staging, if
  any), **R3** (TOTP secret columns on `users`), **R6** (any outbox
  columns beyond today's tables), **R10** (new `Attendance` table, PLAN
  5.3 `docs/plan/PLAN.md:325`), **R14** (sign-up sheet tables, PLAN 5.3
  "Later" `docs/plan/PLAN.md:372`). Rule: they merge before the freeze —
  moving the dump base — or after S4 as `00002+`. The audit's own order
  puts R9 before R2/R10 (`docs/AUDIT-2026-11-01.md:263`); follow it unless
  the owner reorders, in which case the pre-flight recount is the only
  change needed here.
- **R1 interacts with the gate, not the schema.** Sequence R1's first image
  publish after S4. The §4 precondition rests on the owner's statement that
  no image was ever published, and registry errors cannot show otherwise
  either way, so an image published after S1 is a §4 gate failure whoever
  might have pulled it: stop and escalate to the owner (§12 stop 1). Do not
  assess who could have pulled it, do not re-ask and continue, and do not
  open or continue a slice on that basis. This publication gate is separate
  from DEC-138's baseline-editing window, which closes at the first tagged
  release or first church deployment (§4, §12 stop 9); a publication alone
  neither opens nor closes that window, and the owner decides what follows.
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
- **Q4 (resolved).** DEC number: **138**, landed. PR #236 took DEC-137
  (branding) first; this brief and PLAN already use 138 throughout. Do
  not renumber again.
- **Q5 (before S4).** Audit/notify `TargetType`/`SubjectType` rename
  `"occurrence_access"` → `"registration"` (this brief) vs keeping the old
  label pointing at registration IDs? **Default: rename.** Pre-live there
  are no stored rows to stay compatible with, and a label that names a
  deleted table will mislead every future reader of contact history.
- **Q6 (resolved).** Four PRs (S1 docs, S2 squash, S3 types, S4 fold) rather
  than folding S1 into S2. S1 is reviewable in minutes and makes the
  sequence and corrections explicit before any migration file is touched.
  S1 is documentation only: the operative replacement texts (§8) activate in
  S2, with the squash.
- **Q7 (before S2).** Where do the dev-recreate commands live long-term —
  PR description only (this brief, §3) or also a `Makefile` target?
  **Default: PR description only.** A permanent "reset my dev database"
  target is handy but is a separate small decision; do not grow S2 for it.
  The commands are verified on a scratch Compose project (§7.7), never on
  the owner's database.
