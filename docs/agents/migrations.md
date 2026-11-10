# Migrations

Read this before adding or changing a file in `migrations/`.

## Rules

- Every schema change is a **new** goose file `NNNNN_name.sql`, with Up and Down. Never edit a
  migration any tagged release has applied. The only exception is the pre-live baseline window in
  DEC-138, which closes at the first tagged release or first church deployment, whichever comes
  first.
- The model change and `models.All()` go in the same PR. The migration files are the source of
  truth; GORM models must match them (the drift check in `migrations/migrations_test.go`).
- **Expand, then contract (DEC-138).** A release may add tables, columns, indexes and constraints
  and backfill data, but never drops or renames anything the previous release still reads or
  writes. Code in that release writes the new shape and reads new-then-old. The next release may
  drop the old shape: its migration has a `-- contracts: NNNNN_name.sql` header naming the expand
  it contracts, and CI fails when that expand is not in an earlier tagged release. There are no
  audit triggers on old columns and no waiting period: one binary runs at a time, so the previous
  release is the only old version that can still be running.
- Contract gate: `scripts/contract-gate.sh` in the test job checks the tag and file half (a `v*`
  tag merged into HEAD holds the expand and not the contract; the expand unchanged since).
  `TestCIRunsTheContractGate` keeps it in CI and `TestContractGateScript` proves it. The
  publication half is review only: a `release.yml` run, the finalize step summary, GHCR digest,
  release section and attestation naming one digest. A tag alone does not satisfy it.

## Two PRs with the same number

Two open PRs can pick the same next number; git merges both without a conflict and goose then
refuses to boot. Master's ruleset requires a PR to be up to date before it merges (DEC-146), so the
later PR merges master, fails `TestBaselineFileShape` with "duplicate version" and renumbers its
file. `TestDuplicateVersionNeverBoots` proves a duplicated tree cannot boot, migrate or pass the
release smoke.

If a duplicate reaches master anyway, renumber the file that merged later, in a new PR (never a
file a tagged release holds), and recreate every database that applied it under the old number:
goose tracks versions by number alone, so it would count the other file as applied and skip it.

## The chain

The chain is the generated baseline `00001_baseline.sql` (M7.7, DEC-138) plus additive numbered
files. `TestBaselineFileShape` checks it through `migrationFileShapeProblems` (baseline first,
then consecutive `NNNNN_name.sql`, each with Up and Down). Each numbered file's header says what
it is for.

- The baseline's Down destroys the schema and refuses while any user exists; it is for scratch
  databases and the down/up test, never application rollback.
- A development database migrated by the pre-squash chain must be recreated before a new binary
  runs on it; there is no converter.
- `scripts/baseline-proof.sh proof` re-proves a tree against the frozen pre-squash chain with a
  normalized `pg_dump` diff, on a scratch server only. Its `--candidate-tree` must hold only
  `migrations/00001_baseline.sql` (for example a `git archive` of a commit before `00002`).
  `--help` lists the arguments. Background: `docs/plan/briefs/audit-2026-11/R9-baseline-squash.md`.
