# Audit 2026-11 — approved briefs: index, order, rules

Source audit: [`docs/AUDIT-2026-11-01.md`](../../../AUDIT-2026-11-01.md).
Disposition (Grier, Nov 1, 2026): **approved** SEC1, R1–R6, R9–R19;
**rejected** R7 (web push is not frozen; SMS stays a headline goal) and R8
(no open signup mode). Recorded in PLAN as DEC-138–DEC-145 with milestones
in PLAN 14.10. Nothing in a brief overrides `AGENTS.md` or a DEC; where a
brief and current code disagree (line numbers drift), the code wins and the
implementer notes the drift in the PR.

## For the implementing agent

1. Check the milestone's row in `STATUS.md` and its open issues; update the row in the slice PR.
2. Work one slice per PR, in the brief's slice order. Follow the merge rules
   in `AGENTS.md` (green `test`/`web`/`provision`, a separate-agent review of the
   concerns listed there, hand-checks to `docs/FIRST-CHURCH-CHECKS.md`).
3. Sanity before every push: the checks under "Before every push" in `AGENTS.md`.
   Then the brief's own tests (`make test`, `cd web && npm test`,
   `make e2e` where named).
4. Every brief ends with open questions that carry a recommended default.
   Follow the default and note it in the PR; stop only if the default would
   change a DEC.
5. Briefs cite `file:line` at audit time (HEAD `3ec934e`, plus the SEC1 fix
   `28268b3`). Re-check each cited body before editing; line numbers drift.
6. Migrations: new goose file, next free number at implementation time —
   except M7.7, which was the one licensed baseline rewrite (DEC-138); it is
   complete and its squash preflight was met (R9 §9). *Corrected Nov 3, 2026:*
   the older wording "M7.7 only" is superseded by PLAN 14.1, DEC-138 and
   `AGENTS.md`, which allow editing the single baseline directly until the first
   tagged release or first church deployment closes the DEC-138 window (PLAN
   14.10 only orders M7.7 before schema-adding briefs, "parts of M7.4/M7.5"
   included, "unless its table folds into the baseline"; that wave is done).
   A baseline edit still needs freshly refreshed, independently reviewed
   evidence that the window is open, a disposable database, and no owner
   database reset; otherwise add a numbered migration (the first one updates
   `TestBaselineFileShape`, never deletes it). See
   `docs/plan/M7.4-ERASURE-INVENTORY.md` section 10 (a draft awaiting review;
   it grants no authority until signed).

## Brief files

| File | Items | PLAN milestones | Effort |
|---|---|---|---|
| `R1-deployment.md` | R1 (verdict + image/backup/mail-gate/CI slices) | M5.1, M5.2, M5.4 | L |
| `SEC1-R5-R12-security.md` | SEC1 verify, R5 bundle, R12 rotation | M7.1, M7.2, M7.3 | M ×3 |
| `R2-R3-R4-people.md` | R2 import, R3 TOTP+export, R4 erasure | M4.5, M4.9, M7.8, M7.4 | M ×3 |
| `R6-R11-reliability-perf.md` | R6 outbox, R11 query ceilings | M7.5, M7.6 | M ×2 |
| `R9-baseline-squash.md` | R9 baseline + types + provenance fold; four PRs (S1 docs, S2 baseline, S3 dates, S4 provenance) | M7.7 | L |
| `R10-R14-features.md` | R10 attendance, R14 sign-up sheets | M6.3, M6.2 | M ×2 |
| `R15-R16-frontend.md` | R15 speed/discoverability, R16 type codegen | M7.9, M7.10 | M ×2 |
| `R13-R17-R18-R19-governance.md` | R13 bets, R17 cuts, R18 tax, R19 locks | DEC-139–141, M7.11, M7.12 | S/M |

SEC1 note: the fix already landed (`28268b3` on
`fix/secure-first-run-setup`; [issue #240](https://github.com/grierj/fold/issues/240)
closed). Its brief section is a verify-and-close checklist for the PR, not a
build spec. R13 and the wording half of R17 landed in PLAN with
DEC-139/DEC-140; their brief sections keep the exact applied text and the
remaining reference-fix list. Likewise DEC-138's row and the DEC counters
already landed. R9's S1 is a documentation-only PR: it claims M7.7, records
the four-PR sequence, the owner's statements and the agreed technical
corrections, and changes no migration and no operative rule. The
`AGENTS.md` expand/contract replacement and the matching PLAN 14.1, C1 and
"Done means" edits (reviewed in R9 §8) activate in S2, together with the
squash itself; the old rule describes the tree correctly until then. R9
§9 separates the documentation preflight (met for S1) from the squash
preflight (refreshed release and deployment evidence, isolated proof
databases and an announced migration freeze), which stays mandatory before
S2 opens or any migration is edited.

## R1 verdict (DEC-142)

Droplet-class VPS + Compose wins; Cloudflare is supporting-only (DNS-only
app record, R2 as an encrypted backup target, Tunnel later for
church-owned boxes; never cache authenticated responses). Cloudflare
cannot host Fold as shipped: Workers/D1 rule out the Go binary and
PostgreSQL, and Containers would strand the database on snapshot disk or
add a second vendor. So R1 proceeds on the DigitalOcean path (Hetzner in
the EU): prebuilt GHCR image first, then compose-pull, backup service in
root compose with a restore proof, mail gate, and a CI job that boots the
shipped stack. Full evidence: `R1-deployment.md` Part 1.

## DEC map (PLAN 15.1)

| DEC | Item | Meaning in one line |
|---|---|---|
| DEC-138 | R9 | One generated baseline pre-live; additive-first after |
| DEC-139 | R13 | Big bets stand; fleet tooling at ~5 hosted churches |
| DEC-140 | R17 | One adapter per kind until demanded; speculative cuts |
| DEC-141 | R19 | Room suggestions + contact machinery frozen; *amended Nov 3, 2026:* one narrow erasure-only exception lets account erasure (R4) scrub an erased person's identifying text in `contact_requests` while decisions and provenance stay (owner decision; design unreviewed, M7.4 R4-2a) |
| DEC-142 | R1 | VPS + Compose recommended; Cloudflare supports only |
| DEC-143 | R2 | Generic CSV import first (reverses DEC-15's gate) |
| DEC-144 | R4 | Deleting an account erases the person |
| DEC-145 | all | Disposition; R7/R8 rejected; surface freeze till live |

## Implementation waves

Parallel-safe means mostly disjoint files; anything touching `api.go`,
`models/models.go`, `web/src/App.tsx`, `web/src/types.ts` or migrations is
serial with its wave-mates — coordinate in STATUS claims.

- **Wave 0 (now):** M7.1 (SEC1 PR/merge + verify checklist). Independent.
- **Wave 1 (reliability floor, parallel-safe in pairs):** M5.1 image + CI
  compose smoke (R1 slices 1–2); M7.2 (R5, one PR per leak); M7.5 (R6
  outbox slices). M7.2 and M7.5 both touch auth/notify code — run R5's
  session slices before R6 moves reset mail.
- **Wave 2 (schema window):** M7.7 baseline (R9). Hard gate: its
  preconditions proof, refreshed when S2 opens (S1 is docs only and may
  proceed before that). Everything that adds tables lands after this wave
  (M4.5 import runs, M4.9 TOTP secrets, M6.3/M6.2 tables, M7.4 any new
  columns, M7.5 leftovers) or folds its table into the baseline in the
  same PR series. M7.6 (R11) code-only fixes may ride Wave 1; its index
  migration waits for the baseline.
- **Wave 3 (first-church readiness):** M4.5 (R2), M4.9 TOTP + M7.8 export
  (R3), M7.4 erasure (R4, inventory review first), M5.2 backups + mail
  gate (R1 remaining slices), M7.3 rotation (R12).
- **Wave 4 (weekly value):** M6.3 attendance (R10), then M6.2 sign-up
  sheets (R14). Both need the Wave 2 baseline.
- **Wave 5 (any time after Wave 1; mostly parallel-safe):** M7.9 (R15),
  M7.10 (R16), M7.11 (R19 locks), M7.12 (R17.1 + R18). M7.10 changes
  `types.ts` for every page — land it between feature waves, not during
  M7.9's page edits.

## Not in these briefs (audit named gaps — schedule separately)

- DST-transition instant rule (`ParseWall` gap/ambiguity) — small DEC + tests.
- Exhaustive handler-authorisation pass + `docs/API.md` endpoint diff.
- Uploads restore proof (one full DB+/data restore into a scratch stack);
  R1's CI restore test covers the DB half.
- Token-page a11y/390px coverage (`/invite`, `/set-password`, `/guest/*`).
- Church time-zone change semantics + audit row.
- Setup page time-zone list (9 zones) widening.
- `govulncheck` and a live compose boot were not run by the audit; M5.1's
  CI smoke closes the second for every later change.
