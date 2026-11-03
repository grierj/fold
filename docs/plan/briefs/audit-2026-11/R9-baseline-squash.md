# R9 — Squash migrations into one baseline (pre-live window)

| Field | Value |
|---|---|
| Audit item | R9 [CHANGE], `docs/AUDIT-2026-11-01.md:151` |
| Priority | P1 (audit sequence step 3, `docs/AUDIT-2026-11-01.md:263`) |
| Effort | L |
| Status | Complete Nov 3, 2026. S1 #258, S2 #264, S3 #265 and S4 #266 merged after checks and independent review. The S5 record in section 9 contains the merge SHAs, owner attestation and verification evidence. Migration freeze released after S4 merge. |
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
4. **Row-count check on every database the owner knows of.** *Corrected in S2:*
   the S1 text said the only identified durable Fold database was the owner's
   Compose database `fold-db-1` (2 users). That inventory was incomplete. The
   S2 refresh found the development Compose container `deploy-db-1` (project
   `deploy`, volume `deploy_devpgdata`), which holds 18 Fold-named databases:
   `fold`, the `fold_e2e*` end-to-end databases, `fold_guestdev`, the
   `fold_slice2_validation*`, `fold_validation_*` and `fold_verify_*`
   validation databases and one `fold_tmpl_*` test template. Twelve carry a
   `users` table with counts between 0 and 21; six have none. The counts are
   in the retained refresh evidence (`inventory.txt`; the database and container
   listings are `contrary-database-inventory.txt` and `contrary-container.txt`
   beside it); they are counts only and no personal data is quoted here. The
   S1 base checks ran on a separate, freshly created PostgreSQL 16 container
   with no named volumes, removed afterwards, never on a database of the
   owner's.
5. **Owner attestation, in the owner's words.** The owner (Grier) said,
   recorded Nov 3, 2026:
   - "There are no other churches or other developers using fold at this
     time. The only known durable fold database is in my docker compose"
   - "All rows are disposable development/test data"
   - "No Fold image has ever been published" (item 1)

   When the refresh surfaced `deploy-db-1` and its databases, the owner
   clarified, in their words: "Yes, all are disposable development/test
   data", including `fold_guestdev`. That clarification covers every Fold
   database found in `deploy-db-1` and is not asked again; no further
   attestation, credential change or confirmation is wanted. The second S1
   statement is thereby confirmed for the larger inventory; the first stands
   as the owner's account of who uses Fold.

   The host confirmed the meaning of the first two statements. They are the
   attestation, as spoken. The Nov 1 version of this section demanded one
   exact sentence; that demand is dropped, and its wording was a drafting
   preference, never an owner quotation. **Disposable is a fact about the
   data, not permission to act on the database.** The owner's clarification
   gave no destructive authorization, and none is implied: nothing in this
   brief or in S2 drops, resets, rewrites or otherwise touches any of those
   databases; every proof runs on scratch databases (§5.1, §7.7), and
   recreating an owner database is the owner's own step.

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
   separate disposable proof databases. Never use any of the owner's development databases (the `deploy-db-1` container,
   its volume) as a proof database, and never a database that holds anything
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
7. **Further proofs in the same script** (§10 items 1 and 7), each on its own
   proof database of the same scratch server: `fold_squash_owner` (the baseline
   applied by a non-superuser role while `citext` and `btree_gist` are owned
   by another role), `fold_squash_boot` (fresh `migrate up`, server boot,
   `/healthz`, stored rows) and `fold_squash_chain` (a database migrated by
   the frozen chain, then `migrate up` by the new binary, which succeeds and
   leaves versions through 64). The script creates the two roles only for the
   owner proof and drops them afterwards; it refuses a server that holds any
   other database with objects in it.

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
- Existing dev databases (the owner's Fold databases in `deploy-db-1`, migrated by the old chain, carry versions up to 64 in their
  version tables): **the unchanged runner does not notice them.** The new
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

**Squash preflight (mandatory before S2 opens or any migration edit; met Nov 3,
2026, record below).**

- [x] §4 refresh done at S2's opening: tags, releases, deployments, workflow
      runs, first-church checklist and source ownership rechecked; nothing
      published or deployed since S1; owner statements cited, not re-asked.
- [x] Isolated proof databases: `fold_squash_old` and `fold_squash_new` on a
      scratch server, never `deploy-db-1` or any volume that matters (§5.1).
      `fold_squash_new` is empty until the candidate baseline is applied.
- [x] The migration freeze is announced and enforced, not merely stated. From
      S2 opening until S4 merges, no other PR adds, edits or renumbers a file
      in `migrations/` (§13). Record the frozen SHA, the complete migration
      manifest, its file count and the highest version; if the head moved
      since the S1 base, repeat the base checks on the frozen revision.
- [x] `pg_dump` major equals the scratch server's major (checked by the
      script, §5.1).

**S2 refresh record (Nov 3, 2026).** Retained evidence, with no personal data:

- Frozen revision: `be61e582c3011263307e4db4497056f7fd70bfd9`, the merge of S1
  (PR #258) at the tip of `master` of `grierj/fold`. The migration chain is
  unchanged since the S1 base `f782fac72901ca365c1b8737bc9236d5d0ebf505`: 63
  files, versions 1–64, no `00021`. The complete SHA-256 manifest of the 63
  files is embedded in `scripts/baseline-proof.sh` (`frozen_manifest`), which
  rejects any mismatch with the archive of the frozen commit.
- Released or deployed since S1: nothing. No tags, no GitHub releases, no
  deployments, no `release` workflow runs, no open pull requests, and no
  first-church check ticked in `docs/FIRST-CHURCH-CHECKS.md`. The registry limit
  of §4 item 1 still applies; the owner's statement stays the evidence.
- Base checks on the frozen revision, on an isolated PostgreSQL 16 scratch
  container: `go build ./...`, `go vet ./...` and `make test` passed.
  `pg_dump` 16.15 matches the server major.
- Inventory: the refresh found `deploy-db-1` and its 18 Fold databases (§4
  item 4). The owner's clarification — "Yes, all are disposable
  development/test data", including `fold_guestdev` — is recorded in §4 item 5.
  It is a fact about the data and gives no authorization to modify any of them.
- Migration freeze: S2 through S4 only. No unrelated migration change belongs
  in these PRs; anything else that needs a schema change waits for the freeze to
  lift (§13).

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

**S2 operations (generator, proof and the one-time install).** Everything runs
on a scratch PostgreSQL 16 server — one with no durable volume, a superuser role
and no database of value; the script refuses any server that holds a database
with objects in it besides its own `fold_squash_*` proof databases, and it never
touches the owner's `deploy-db-1`. `scripts/baseline-proof.sh` is bound to the
frozen commit and the embedded 63-file manifest, uses a frozen `git archive`
(never a worktree), compares `pg_dump` and server majors for each database,
runs `pg_dump`/`psql` wrappers by argument (`--pg-dump`/`--psql`, one word per
use, never `eval`) and writes nothing to the repository unless `--install` is
given.

Two connection safeguards (review findings S2-R1 and S2-R2) run before the
first database change. **Route identity.** `psql`, `pg_dump` and every
administrative statement go through `--client-url`; the Fold binaries go
through `--server-url`, so the script first proves the two reach one cluster,
the database `postgres` and one superuser role. Both URLs must have the exact
shape `postgres://user[:pass]@host:port` (percent-encoded user and password,
explicit host and port, no path) and may carry only `sslmode` and
`connect_timeout`; a `dbname`, `database`, `service`, `host`, `user`, `options`
or any other key, however encoded, is refused. Every `PG*` variable in the
environment is removed for each command the script runs and `PGPASSFILE` is
pointed at `/dev/null`, so no environment or password-file fallback can route a
connection. A small Go helper, built inside the extracted frozen tree with the
pinned pgx driver (never in the repository), parses each URL with the driver
and requires it to resolve to exactly the host, port, user, password and
database the URL names, connects the way `fold migrate` and the server do,
holds a random advisory lock, and runs the wrapped `psql` against `--client-url`
to find that lock taken (advisory locks are per database and cluster). Both
routes must also report the same `session_user`, `current_user`, superuser flag,
cluster system identifier, postmaster start time and `server_version_num`. Any
failure stops the script with nothing changed. The non-secret result is saved as
`connection-identity.txt` and copied into `summary.txt`; errors are scrubbed of
URLs and passwords. In proof mode the candidate tree must pin the same pgx and
GORM Postgres driver as the frozen tree. **Boot port.** The fresh-boot port
(`--boot-port`) must have no listener before the run changes anything, and a
health answer counts only while `ss` shows the script's own server process as
the sole owner of the port, before and after the request, with the process
still alive; an early exit or a foreign listener fails the proof and the foreign
listener is left untouched.

- `generate --install` migrates `fold_squash_old` with the frozen chain,
  dumps it, applies the §5.2 cleanup mechanically, builds the frozen source
  with the candidate in place of the chain, applies it to a truly empty
  `fold_squash_new`, reads `goose_db_version` directly (exactly version 0 and
  version 1 applied), dumps, normalizes both sides and requires a zero-byte
  `diff -u`. It then proves the baseline for a non-superuser migration role
  with `citext` and `btree_gist` pre-created by another role, a fresh boot with
  `/healthz` and the stored rows (the server runs from a scratch directory that
  holds a copy of the candidate tree's `templates/`, which it reads from its
  working directory), and an old-chain database that the new
  binary's `migrate up` leaves untouched (§7.7). Only after all of that does
  `--install` swap the migration chain in this checkout for the proven file
  (two renames of the directory; nothing is hand-written or hand-edited).
- `proof --expect-generated` repeats the proof for the committed tree and also
  requires the committed baseline to equal the generator's output byte for byte;
  later slices run `proof --allow-diff` and check their hunks against §10.
  `generate` refuses to run once `migrations/` no longer holds the frozen chain,
  so it cannot overwrite S3 or S4.
- Two generator details worth stating. `pg_dump` lists `btree_gist` before
  `citext`; §5.2 asks for `citext` first, so the generator moves those two
  `CREATE EXTENSION` lines (they depend on nothing; the dumps cancel the order
  in the diff). Each `CREATE FUNCTION` statement is wrapped for goose from its
  `CREATE` to the semicolon that closes its dollar quote. The generator stops,
  with no output, on any `pg_dump` section type it does not recognize, on
  `CONCURRENTLY` and on a foreign-key cycle, instead of guessing.
- Old-chain databases are **not** automatically refused (§7.7) and no
  converter exists. A development database migrated by the old chain must be
  recreated by its owner before the new binary runs on it. The scratch Compose
  recreation commands (a distinct project name, volume and port; the resolved
  configuration inspected first) are in the S2 PR description only (Q7), never
  a permanent reset target.

**S2 evidence record (Nov 3, 2026).** The generator and the proof ran on the
scratch PostgreSQL 16 server (`pg_dump` 16.15) and the generated file is
installed. This records results only; it does not claim review, publication,
merge or completion of M7.7.

- `generate --install` and then `proof --expect-generated` both passed. The
  normalized schema diff against the frozen chain is 0 bytes, the stored
  `goose_db_version` rows are exactly `0|t` and `1|t`, and the installed file equals the generator's output byte
  for byte. `migrations/00001_baseline.sql` has SHA-256
  `e824c6fc7d497f200992cc313ee58fdf425687943d6cac8f38d8704959cea461`. The
  extension-owner proof, the fresh boot with `/healthz` and the old-chain
  untouched proof passed in both runs.
- Two fixes came out of the first full run, neither touching the baseline.
  The fresh-boot stage had no `templates/` in its scratch directory (the server
  panics without them), so it now copies them from the candidate tree in both
  modes. `TestMembershipTenureGuard` inserted its inactive membership without
  `joined_at`, which the baseline permits to be NULL while the test scans it into
  a `time.Time`; the row now carries the `joined_at` already used for the first
  row, and the tenure assertions are unchanged.
- Checks that passed on the installed tree: the migrations package including
  `TestNoDrift`, the touched root tests and the notification guards;
  `make test`; `go build ./...`; `go vet ./...`; `make lint-go`; `make lint-sh`;
  `make deadcode`; the web suite (1,434 tests in 141 files), web build,
  TypeScript, Knip, contrast and bundle size; and `make e2e` (118 passed, 7
  skipped). Tracked and untracked files were identical before and after.
- The scratch Compose recreation was run once on a distinct project, volume
  and port: the frozen binary migrated the scratch database to version 64, only
  that scratch database was dropped and recreated, `make dev` booted, `/healthz`
  answered and the stored rows were `0|t` and `1|t`. The scratch resources were
  removed. The commands belong in the S2 PR description, not in this brief.
- Reviewer findings S2-R1 (route identity) and S2-R2 (boot-port ownership) are
  fixed in `scripts/baseline-proof.sh` and verified on isolated PostgreSQL 16
  servers. Cross-cluster routes in both directions, URL overrides, mismatched
  roles, occupied ports, early server exits and listener ownership races were
  rejected. Unrelated listeners survived. Inherited `PG*` overrides were cleared;
  valid host/container routes, SCRAM authentication with percent-encoded
  passwords, and an actual IPv6 connection through `[::1]` passed. Wrong passwords
  and malformed encoding were rejected, and captured helper output passed a
  credential scan.
- With these fixes, the helper build and vet, shell syntax and lint, and full
  `proof --expect-generated` passed. The regenerated baseline still matched the
  installed hash above; the normalized schema diff was zero bytes and stored
  versions were `0|t` and `1|t`. Extension ownership, fresh boot and old-chain
  proofs passed. Build, vet, `make lint`, migrations including drift, the docs
  guard and targeted root tests also passed. Source fingerprints were unchanged
  during verification, and the new scratch resources were removed.
- Still open: independent re-review of S2, the PR checks and the merge. The
  owner's version-64 databases (`deploy-db-1`) were not touched; the runner does
  not refuse them, so recreating them remains the owner's manual step.

**S2 test disposition.** Every version-pinned call, by file. "Kept" means the
final-schema assertion now runs on a baseline-migrated database; "removed" means
version travel, a historical backfill or an assertion that belonged to a deleted
Down. Each file keeps the behavior it protects.

| File and test (old) | Calls | Disposition |
|---|---|---|
| `migrations/migrations_test.go` `TestContactRequestsConstraints` | `DownTo(29)` | Removed (down/up tail). Constraint matrix kept. |
| `TestOccurrenceAccessDepartureOriginMigration` → `TestOccurrenceAccessDepartureOrigin` | `DownTo(33)` ×2, `UpTo(34)` | Removed. Insert/refusal matrix kept. |
| `TestDepartureOccurrenceAccessExpiryMigration` → `…ExpiryEndState` | `UpTo(34)`, `UpTo(35)`, `DownTo(34)` ×2 | Removed. `occurrence_ended` accepted and malformed end states refused on the final table; ending stays irreversible. |
| `TestDepartureAccessSupersessionMigration` → `…SupersessionEndState` | `UpTo(36)`, `UpTo(37)`, `DownTo(36)` ×2 | Removed. `departure_superseded` end state and kept manager provenance asserted directly. |
| `seedMembershipTenureMigration`, `TestMembershipTenureMigration`, `…CleanRollback`, `…WaitsForReactivation` → `TestMembershipTenureGuard` | `UpTo(35)`, `UpTo(36)`, `DownTo(35)` ×3 | Backfill and the Down's lock-and-refuse were history, removed. Trigger rules (reactivation advances tenure, role changes do not, direct writes and tenure 0 refused, function and trigger present) kept on the final table. |
| `TestSeriesUndoReceiptMigrationAndRollback` → `…Constraints` | `DownTo(32)` | Removed. Receipt CHECKs kept. |
| `TestOccurrenceCancelSourceMigration` → `TestOccurrenceCancelSource` | `DownTo(42)` | Removed. Default and the three values kept (DEC-91). |
| `TestMigrateEmptyDatabase` | none (`Up`) | Kept and strengthened: version 1, stored rows exactly `0:true,1:true`, every model table present. |
| `TestSimplicityMigrations` → `TestMembershipStatusAndLeaderOpenedConstraints` | `UpTo(7)`, `UpTo(9)`, `DownTo(8)` | Backfills and Down mapping removed. Membership status values (including `left`, no `pending_return`) and the church-wide event CHECK kept. |
| `TestNotificationsBackfill` | `UpTo(9)`, `UpTo(10)`, `DownTo(9)` | Removed: backfill-only scenario with no final-schema assertion. |
| `TestMigration15DownMapsStatuses` → `TestRegistrationStatusVocabulary` | `UpTo(15)`, `DownTo(14)`, `UpTo(15)` | Down mapping removed. The six registration statuses accepted, unknown status and source refused. |
| `TestHouseholdMigration22` → `TestHouseholdTablesConstraints` | `UpTo(22)`, `DownTo(19)`, `UpTo(22)` | Removed. Constraints kept; the live guardian-link index (00027) added. |
| `TestManageTokenIndex` | none | Kept unchanged. |
| `TestChildcareMigration23` → `TestChildcareColumnDefaultsAndChecks` | `UpTo(23)`, `DownTo(22)`, `UpTo(23)` | Removed. Defaults and CHECKs kept. |
| `TestHouseholdRegistrationMigration24` → `…IdentityConstraints` | `UpTo(23)`, `UpTo(24)`, `DownTo(23)` ×3, `UpTo(24)` ×2 | Removed. Account-row defaults and the 13-case identity matrix kept. |
| `TestHouseholdConversionMigration25` → `…InvitationConstraints` | `UpTo(25)`, `DownTo(24)` ×3, `UpTo(25)` ×2 | Removed. Invitation constraints kept. |
| `TestTeenConsentMigrationAppendOnlyAndRollback` → `TestTeenConsentAppendOnly` | `UpTo(25)`, `UpTo(26)`, `DownTo(25)` ×2, `UpTo(26)` | Disabled-user backfill and Down guard removed. Append-only and CHECK matrix kept; TRUNCATE still permitted. |
| `TestContactAddRequestsMigration` → `TestContactRequestPurposeDefaultsToContact` | `UpTo(30)`, `DownTo(30)` ×2 | Data preservation across a version removed; the contact default kept. |
| `TestContactAddRequestsConstraints` | `DownTo(30)` | Tail (Down refusal) removed. The whole constraint and provenance matrix kept (S4 rewrites it onto `registrations`). |
| `TestMembershipAndRegistrationLookupPlans` | `DownTo(40)` | Removed. Both plans kept, and index existence asserted. |
| `TestPushSubscriptionsMigration` → `TestPushSubscriptionsConstraints` | `UpTo(58)`, `DownTo(57)` ×4, `UpTo(58)` | Down refusals removed. Device constraints, push consent and delivery channel, user-delete cascade kept; the 00059 trigger (devices end with sessions, one notice job) added. |
| `TestNoDrift`, `TestDriftCheckCatchesUnmigratedField`, `TestMigrationsTakeAdvisoryLock` | none | Unchanged. |
| New | | `TestBaselineDownRefusesWhileAUserExists` (`DownTo(0)` and `Down`, data, version and schema preserved), `TestBaselineDownAndUpOnAnEmptyDatabase` (drops everything, `Up` restores, no drift), `TestBaselineFileShape`. |
| `account_security_test.go` `TestCredentialTokenMigrationRevokesUnsafePendingLinks` → `TestAccountTokenKindConstraint` | `DownTo(39)`, `UpTo(40)`, `DownTo(39)` | Migration-40 revocation and Down history removed. All five token kinds accepted, an unknown kind refused. Pending-link revocation by credential changes stays covered by `TestCredentialChangesRevokeEveryPendingAccountToken`. |
| `api_departure_test.go` `TestDepartureLeftMigrationRepairsOnlyAuditedSelfLeaves` | `DownTo(38)`, `UpTo(39)`, `DownTo(38)` | Removed (00039 audit-backfill repair and its Down guard). The `left` status itself is asserted by `TestMembershipStatusAndLeaderOpenedConstraints`. |
| `api_departure_test.go` `TestDepartureSelfLeaveRejoinRejectsOldSnapshotAndNotifiesAgain` | `DownTo(35)` | The refused-downgrade block replaced by a direct tenure write that must be refused and a reload that must still show the advanced tenure. The rest of the test is unchanged. |
| `cli_test.go` `TestMigrateDownOneRefusesAdvancedMembershipTenure` → `TestMigrateDownOneRefusesBaselineWithData` | `UpTo(36)` | `down-one` still exits 1 with the `fold: migrate down-one:` prefix, now naming the baseline guard; user row, tables and version 1 are unchanged and `status --json` still works. |
| `guards_test.go` `TestNotificationCategoriesMatch` | none | One reference moved from `00010_notifications.sql` to the baseline's `notifications_category_check` (a file outside the brief's list, changed because the file it read is gone). |

**S3 — Date types (§6).** Files: `migrations/00001_baseline.sql` (two
column types, two CHECKs), `models/models.go` (`RRuleUntil`,
`OriginalDate`, the boundary helpers), `internal/events/extend.go`, and every
production file the `RRuleUntil`/`OriginalDate` search and the §6.2 sink
inventory surface, with only persistence-facing fixtures, date-output
assertions and date-binding regressions changed in tests. `internal/events`
`Spec`/`When`/`Expand` keep string dates internally (§6.2). Recheck the
editing window and freeze before starting. No UI, JSON shape, policy or
error-contract change.

*S3 implementation notes (implemented; see the verification status at the end of these notes).*

- Schema: `00001_baseline.sql` changes `events.rrule_until` to nullable `date`,
  `event_occurrences.original_date` to `date NOT NULL`, and adds
  `events_starts_local_check` and `events_tzid_check` (placed where `pg_dump`
  would sort them). Index names, the 38 tables, functions and triggers are
  untouched; the baseline was edited, not regenerated.
- Helpers: `models.ParseCalendarDate` (strict `time.Parse`, plus Postgres's
  no-year-0 rule) and `models.FormatCalendarDate` (the value's own year, month
  and day, no zone conversion) in `models/calendar_date.go`. `Event.RRuleUntil`
  is `*time.Time`, `EventOccurrence.OriginalDate` is `time.Time`; `Spec`,
  `When`, DTOs and the wire stay strings, `omitempty` until stays `""`.
- Binding rule as implemented: validated ISO strings stay as binds
  (`original_date = ?`, `>= ? AND <= ?`, `IN ?` and `rrule_until >= ?`). Where
  a date input could reach SQL without a real-date check, `isCalendarDate` or
  `firstScheduledOn` (`api_occurrence_access.go`) gates it first: the
  registration, guest and per-date visibility lookups, and stored `dates` in
  `push_deliver.go`. The existing `time.Parse` checks (event window, cancel,
  date edit, date page, room date, series scope, room calendar day) now call
  `ParseCalendarDate`, so `0000-01-01` stays a 400/404 too. `events.Expand`
  uses it for `rruleUntil`.
- Text projections use `to_char(..., 'YYYY-MM-DD')`, never `date::text`:
  `extend.go` (dedupe), `restoreSeriesDates` (audit `dates`),
  `api_messages.go` (reminder and composer dates), `api_room_series.go`
  (needs-room dates), `api_room_requests.go` (first date). `messages_send.go`
  scans a `time.Time` and formats it. The room calendar no longer selects its
  unused `original_date`.
- Dynamic sinks formatted at the write: notification `Data` in
  `api_registrations.go` (promoted, cutoff, changed, removed, not restored) and
  `api_notifications.go` (cancelled, restored); audit `Meta` in
  `api_room_bookings.go` (booked, released) and `api_occurrence_visibility.go`;
  room-lost `dates` lists (`api_room_series.go`, `api_room_requests.go`,
  `api_room_bookings.go`); series results, refusals and undo items;
  departure facts and previews; date and impact titles; every `DateURL`, feed
  and calendar URL.
- One consequence of the new CHECK: `time.Parse` accepts a one-digit hour, so
  `specFrom` pads it (`2026-10-20T9:00` is stored as `T09:00`) and the edit
  keeps answering 200 instead of failing at the insert.
- Tests: `models/calendar_date_test.go` (leap and impossible dates, year 0, the
  UTC-boundary formatting); `internal/events/extend_test.go` (separate pools in
  Pacific/Kiritimati with ISO, America/Los_Angeles with SQL DMY and
  Pacific/Kiritimati with German DMY, each checked to be in force; writes from
  a zone-shifted `time.Time`, reads, `=`/range/`IN` binds, extension
  idempotence across pools, inclusive and null `rrule_until`, the
  yesterday boundary, a leap-day series, and the database refusing impossible
  dates and bad `starts_local`/`tzid`); `api_events_test.go` (impossible dates
  keep their answers, leap until and padded hour, restored-date audit and
  notice strings, stored until); `api_occurrence_visibility_test.go` and
  `api_room_bookings_test.go` (audit `Meta` dates); `api_registrations_test.go`,
  `api_guest_test.go`, `api_teen_consent_test.go`, `api_notifications_test.go`
  (notification `Data` dates via `noticeDataDate`). Fixtures that wrote string
  dates now use `calDate` (`testhelpers_test.go`).
- Sink-to-assertion inventory (every place a stored date becomes outward text,
  and the test that pins the exact `YYYY-MM-DD`). Expected values come from the
  fixture's own start time or a literal, not from `FormatCalendarDate`, except
  where noted (some older assertions still derive the expected text from the
  formatter on a fixture row; the departure, composer, series, room-request and feed rows below use independent values).

  | Family | Production site | Assertion |
  |---|---|---|
  | Notification `Data` promoted | `api_registrations.go` (`notifyRegistrationPromoted`), guest path | `TestPromoteActionSeatsWaitlisted` (hand promotion) and `api_guest_test.go` (guest promotion, `guestDate`) via `noticeDataDate` |
  | Notification `Data` waitlist cutoff | `api_registrations.go` (`notifyWaitlistCutoff`) | `TestSeriesRegistrationRefreshesClockAfterContactAndOccurrenceLocks`, cutoff subtest |
  | Notification `Data` changed | `api_registrations.go` (`notifyEventChanged`) | `TestHouseholdEventNoticesDeduplicateByHolder` |
  | Notification `Data` removed / not restored | `api_registrations.go` | `api_registrations_test.go` (removed), `api_teen_consent_test.go` (not restored) |
  | Notification `Data` cancelled / restored | `api_notifications.go` | `api_notifications_test.go`; series restore notice dates in `api_events_test.go` |
  | Notification `Data` room lost `dates` | `api_room_bookings.go` (`notifyRoomLost`) | `api_room_bookings_test.go` (decoded as `[]string`) |
  | Audit `Meta` booked / released | `api_room_bookings.go` | `api_room_bookings_test.go` (`meta_json->>'date'`) |
  | Audit `Meta` date visibility | `api_occurrence_visibility.go` | `TestDateVisibilityAuditAndPreviewUseExactDates` |
  | Audit `Meta` series restored | `api_events.go` `restoreSeriesDates` (`to_char` Pluck) | `api_events_test.go` (exact ordered `dates`) |
  | Audit `Meta` room request `lost` | `api_room_requests.go` | `TestRoomApprovalSeries` (response and `meta_json->'lost'`) |
  | Series results, refusals, undo items | `api_series_registrations.go` | `TestSeriesRegistrationThisAndFollowingPreservesEarlierAndRemoved` (results, refusal and the persisted `series_undo_receipts.snapshot`), `TestSeriesUndoRestoresNoAnswerAndIsActorBoundSingleUse` |
  | Departure preview | `api_departure.go` | `TestDeparturePreviewDateRulesParityAndReadOnly` (`date` per row) |
  | Composer options | `api_messages.go` (`to_char`) | `TestComposerOffersOnlyWhatTheAuthorMayMessage` |
  | Reminder projection | `api_messages.go` (`to_char`) | `TestReminderMessageReadableByEventManagers` and the push-reminder read in `reminders_test.go` |
  | Message `manage_link` | `messages_send.go` (scan `time.Time`, format) | `messages_send_test.go` (date and series readers) |
  | Room request `firstDate` | `api_room_requests.go` (`to_char(min(...))`) | `TestRoomApprovalSeries` |
  | Room needs-room dates, series `lost` | `api_room_series.go` (`to_char`, format) | `api_room_series_test.go` |
  | Room calendar slots, `DateURL` | `api_room_calendar.go` | `api_room_calendar_test.go` |
  | Room-location busy date | `api_room_locations.go` | `TestRoomLocationMatching` (exact busy date) |
  | Room feed event link | `api_room_feeds.go` | `TestRoomFeedContents` (link in the unfolded description) |
  | Personal feed UID and link | `api_feeds.go` | `TestPersonalFeedOpenedHiddenSeriesDateStaysStandaloneAndPrivate` |
  | Calendar rows, `DateURL`, occurrence links | `api_calendar.go`, `api_occurrence_access.go`, `api_occurrence_mutations.go` | `api_calendar_test.go`, `api_occurrence_mutations_test.go` |
  | Push reminder navigate link | `reminders_push.go` | `reminders_push_test.go` (lock-screen payload) |
  | Add-request and contact previews | `api_add_request_reads.go`, `api_contact.go` | `TestAddRequestPageReadBudgetAndScalarParity` (each row's `date`); `api_occurrence_mutations_test.go` (contact-style date) |
  | Group visibility impact (date entries) | `api_group_visibility.go`, `api_occurrence_visibility.go` | `TestGroupTighteningRequiresDateDecision` (`date`); title suffix in `api_occurrence_visibility_test.go` |
  | Date cancellation notice Title and EmailTitle | `api_group_visibility.go` | `TestDateTighteningCancelsOnlyThatDateAndRechecksGuestRedemption` (complete title with independent date) |
  | Date visibility-override notice Title and EmailTitle | `api_occurrence_visibility.go` | `TestDateVisibilityAuditAndPreviewUseExactDates` (eligible recipient, complete title) |
  | Event DTO `rruleUntil`, instance dates | `api_events.go` | `api_events_test.go` (leap-day and null `until`, stored until) |
  | Extension dedupe and writes | `internal/events/extend.go` | `internal/events/extend_test.go` |

  Raw SQL that produces a date string: `to_char(..., 'YYYY-MM-DD')` in
  `internal/events/extend.go`, `api_events.go` (`restoreSeriesDates`),
  `api_messages.go` (two), `api_room_series.go` and `api_room_requests.go`;
  each appears above. Raw SQL that scans a `date` into `time.Time` is
  `messages_send.go` only. Date inputs that only bind (`original_date = ?`,
  range, `IN ?`) produce no outward text and are covered by `extend_test.go`
  and the existing 400/404 tests: `api_event_access.go`, `api_registrations.go`,
  `api_guest.go`, `api_occurrence_*`, `api_room_bookings.go`, `api_events.go`,
  `api_series_registrations.go` and `push_deliver.go`. No site was found that
  needed a production change.
- Verification status. The date-output assertions and full Go root suite
  (977 tests) passed, with build, vet, lint, deadcode, shell, TypeScript,
  Knip and docs checks. Earlier web results (1,434 tests), end-to-end results
  (118 passed, 7 skipped) and schema proof (two type changes and two CHECKs,
  38 tables) remain applicable to the subsequent test/documentation additions.
  Independent review found four bounded issues: year-zero event starts reached
  persistence, an add-request assertion skipped empty dates, two notification
  titles lacked exact assertions, and the inventory/status needed reconciliation.
  The fixes validate generated dates before persistence, assert all 80
  occurrence-add rows and complete notice titles, and update this record.
  All five targeted regression tests passed with caching disabled. The full Go
  suite, including jobs, and the build, vet, lint, deadcode, shell, TypeScript,
  Knip and docs checks also passed against the unchanged candidate. The fixes
  await independent re-review; publication is pending.
  One earlier
  `TestNewJobStartsWithinTwoSeconds` timeout in `internal/jobs` did not
  reproduce (five focused runs, the full package and the full `make test` all
  passed); its cause is unknown and no code was changed for it.

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

*S4 implementation notes (implemented on `autoflow/m77-s4-20261103`; checks, schema review and additional regressions passed).*
The verification record below describes the completed checks and remaining
limitations. Final review passed and S4 merged as #266; the S5 record below
closes M7.7 and records release of the migration freeze.

- Schema (`00001_baseline.sql`, edited, not regenerated; no new migration):
  `registrations` gains the nine `access_*` columns of §7.2, the CHECKs
  `registrations_access_provenance` (origin/provenance shape, account rows only,
  empty origin means every other column empty), `registrations_access_end_state`
  and five positive-ID checks, the foreign key `registrations_access_request_id_fkey`,
  and the indexes `idx_registrations_access_request` (unique, partial) and
  `idx_registrations_live_access` (partial). `occurrence_accesses`, its sequence,
  indexes, key, foreign key and `occurrence_access_provenance()` are gone;
  `registration_access_provenance()` is added (`BEFORE INSERT OR DELETE OR UPDATE`)
  and the occurrence branch of `add_request_completed()` now reads `registrations`
  (still a deferred constraint trigger; the group branch is untouched). The
  baseline Down drops the new function and no longer names the old table.
  `models.All()` lists 37 tables; `ManagerAddBlock` stays.
- One deliberate tightening over the old CHECK text: a `departure_self` grant
  requires a non-null departure actor equal to the subject (the old CHECK let a
  null actor through, because `NULL = id` is not false).
- Trigger rules as built: a row without provenance deletes and updates freely
  (identity moves are refused only while provenance exists); a live grant is
  immutable apart from a one-way end; an ended grant can be replaced only by a
  new live grant whose stamp is later than the old stamp and no earlier than the
  old end, and only when the matching `occurrence_access.added` and `.ended`
  snapshots exist (subject, date, registration, origin, issuer, request,
  departure pair, grant stamp, and for the end the end stamp, reason and actor,
  compared exactly); a delete needs the same snapshots and an ended grant. Every
  new grant first checks the whole access-snapshot scope for a corrupt row with the
  shared guarded integer predicate (SQLSTATE `XX001`), then re-proves live facts,
  the manager-add block, teen and repeat rules (a repeat is a row that ever held a
  grant or a validated added snapshot for the subject and date), the exact granted
  request (requester, subject, event, date, completed target, completion no later
  than the grant, creation no earlier than the latest end across the row and the
  snapshots, equality allowed) and departure authority against the active
  membership. A request can ground one grant only: an added snapshot already
  naming it refuses a second use. Corruption is checked for grants, not for
  deletes, so one malformed unrelated audit row cannot freeze hard deletes.
- Go: `registration_access.go` is the only writer of the `access_*` columns and
  of the snapshots. `grantRegistrationAccess` and `endRegistrationAccess` normalize
  time to UTC microseconds, write the snapshot in the caller's transaction and
  reload the persisted row first; a replacement that does not move forward fails
  with `errAccessClockOrder` (nothing advances the clock). `loadAccessHistory`
  evaluates `historyMetaID`, the same guarded predicate the baseline holds
  (`TestRegistrationAccessCorruptionPredicateMatchesSQL` compares the text), and a
  corrupt row fails the load before any filtering. Ordinary registration saves
  omit the `access_*` columns, so a stale struct can never revert a grant or an end.
  `policy.OccurrenceAccess` is built from the registration row with its status
  synthesized from the end stamp; policy functions and
  `internal/policy/occurrence_access_test.go` are untouched.
- Dedupe keys: `occurrence-added:<registrationID>:<grantedAtMicro>` and
  `occurrence-ended:<registrationID>:<grantedAtMicro>:<endedAtMicro>`; notices and
  audit rows use `registration` as their subject and target type; the promotion
  key keeps its RFC3339Nano component on the registration ID.
- Departure supersession is two updates under the existing locks with one
  normalized instant (end the manager grant as `departure_superseded` without a
  notice, then replace it with the departure grant); a live departure grant is
  left untouched; audit or notice failure rolls the whole confirmation back.
- Hard deletes: `deleteEventRows` ends live grants as `event_deleted` without
  notices; `restoreRegistrations` and series undo end them as `context_changed`
  through the normal notice path; already-ended grants keep their reason.
- Contact history reads the three registration-targeted snapshots; `accessId`
  carries the registration ID; the JSON keys and CSV header are unchanged.
- Reasoned deviations and judgement calls: (1) the organizer is no longer stored,
  so the old organizer-mismatch end reason has no source; `context_changed` stays
  reachable through an inactive group or a registration that stopped being a
  current answer, and the "organizer changed" scenario in
  `api_occurrence_access_test.go` now moves the event to an archived group.
  (2) The corruption scope check guards grants only. (3) `audit_logs` keeps no
  immutability trigger (that would be a diff hunk outside §10's S4 allow-list);
  snapshot immutability is the application's write-once rule plus the exact
  matching the trigger demands before anything is overwritten or deleted.
  (4) `departureAccessFact` keeps its fields for the preview digest, with the
  registration ID in both ID slots.
- Tests: `migrations/migrations_test.go` ports the departure-origin, expiry,
  supersession and contact-add matrices onto `registrations` and adds delete,
  replacement, ordering, repeat/stale survival, single-use request, household,
  sole-leader and trigger-shape cases; `registration_access_test.go` holds the
  fixture helpers and the snapshot, ordering, dedupe, hard-delete and departure
  rollback tests; `api_occurrence_access_test.go` holds the table that runs the
  corruption predicate through the Go loader and the trigger;
  `api_contact_history_test.go` covers retention after re-grant and deletion,
  large IDs, malformed metadata, misclassification and the unchanged JSON keys.
- Verification record: at base `695cf7d9f9e30afc684f2beb7653d8467deb0598`,
  candidate baseline SHA-256
  `09edf918eabf3ca43cd29997caf6884a01f590909d6ae17d1253bdaa59181376`,
  the full Go suite (988 root tests), migrations including strict drift,
  unedited policy tests, focused regressions, Node 24 frontend and end-to-end
  suites, builds, vet, lint, deadcode, TypeScript, Knip, contrast and bundle
  checks passed. The batch-read budget measured 9 reads for one request and
  11 for 100, within the unchanged ceiling.
  The scratch proof recorded versions 0 and 1, extension-owner application,
  fresh-boot health and unchanged old-chain behavior. Independent review
  examined every normalized schema hunk against S3 and the frozen chain:
  only the §10 changes remain, with 37 tables, unrelated functions and
  triggers unchanged, and the group completion branch byte-identical.
  The reviewed content manifest was
  `05a3a7bdfaf2f367ea6c2eb693c7f2df669613f4804998ffc9d8987554003c69`.
- That review covered all four concerns and found three missing combined
  lifecycle proofs. Additional tests now exercise kept-date reminder delivery
  and suppression, production deletion/recreation followed by repeat/stale
  refusal and fresh approval, and contact-history attribution across two grants
  on one registration before and after deletion. All four test functions
  passed together with caching disabled, including both departure reminder
  cases. The full Go suite, docs guard and static checks also passed.
  The reminder fixture uses invite-only dates inside the two-hour departure
  cutoff and runs the permitted 120-minute reminder as a delayed job after
  the grant exists. It asserts ordinary visibility loss, one delivery through
  retained access and suppression after the grant ends.
  The stale-request fixture deliberately inserts an otherwise unreachable
  pending request; the series-undo fixture uses the production grant writer
  because the real manager-add endpoint changes the undo revision. Those
  fixture limits were accepted in independent re-review as defensive-boundary
  evidence, not claims that those endpoint sequences are reachable.
  S4 merged as #266 after all four CI jobs passed. The earlier S3 jobs timeout has not recurred
  in later runs; its cause remains unknown.

**S5 — Close-out (rides in S4's PR unless it grows).** Update `docs/API.md`
storage and migration references (JSON itself is unchanged). Close the
`STATUS.md` M7.7 row, which S1 claimed, with actual evidence; close-out does
need a milestone entry. After merge, the host records the four merge SHAs
and the §4 attestation evidence and releases the freeze.

**Completed Nov 3, 2026.** All four slices merged with normal merge commits:

| Slice | PR | Merge commit |
|---|---|---|
| S1 | [#258](https://github.com/grierj/fold/pull/258) | `be61e582c3011263307e4db4497056f7fd70bfd9` |
| S2 | [#264](https://github.com/grierj/fold/pull/264) | `ecffd58ac4af11e172c87fda7cf0e8037feddf4c` |
| S3 | [#265](https://github.com/grierj/fold/pull/265) | `695cf7d9f9e30afc684f2beb7653d8467deb0598` |
| S4 | [#266](https://github.com/grierj/fold/pull/266) | `c52540432342ab577c68c9046b35384be581ab38` |

The owner statements in §4 remain the gate evidence: no other churches or
developers use Fold, no Fold image has ever been published, and all known
database rows, including those in `deploy-db-1`, are disposable development/test
data. The S4 merge refresh found no tags, releases, deployments or retained
release-workflow runs. Registry authorization failures did not establish absence.
No owner database was reset. The host released the S2–S4 migration freeze after
recording these merge commits and attestation evidence. DEC-138's pre-live
baseline window still closes at the first tagged release or church deployment.

Final verification includes 991 root Go tests, all four added lifecycle tests,
1,434 frontend tests under Node 24, 118 e2e passes with seven skips, strict model
drift and the complete 37-table schema allowlist review. Independent review
closed all findings across correctness, security, database/performance and
unaffected UI/UX. The `test`, `web`, `provision` and `compose` CI jobs passed on
S4 head `08a8b2cc6cb481c2a962ba49dfea04cbffceea24`. The final status reconciliation
is a documentation-only follow-up; it changes no runtime or schema behavior.

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
   makes no claim of automatic refusal. The owner's `deploy-db-1` databases are never
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
      untouched), so recreating the owner's version-64 databases (`deploy-db-1`) is a
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
`deploy-db-1` databases need recreating, that is the owner's step. After the window
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
