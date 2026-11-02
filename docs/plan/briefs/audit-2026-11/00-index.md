# Audit 2026-11 — approved briefs: index, order, rules

Source audit: [`docs/AUDIT-2026-11-01.md`](../../../AUDIT-2026-11-01.md).
Disposition (Grier, Nov 1, 2026): **approved** SEC1, R1–R6, R9–R19;
**rejected** R7 (web push is not frozen; SMS stays a headline goal) and R8
(no open signup mode). Recorded in PLAN as DEC-138–DEC-145 with milestones
in PLAN 14.10. Nothing in a brief overrides `AGENTS.md` or a DEC; where a
brief and current code disagree (line numbers drift), the code wins and the
implementer notes the drift in the PR.

## For the implementing agent

1. Claim the milestone in `STATUS.md` first (its own commit or the slice PR).
2. Work one slice per PR, in the brief's slice order. Follow the merge rules
   in `AGENTS.md` (green `test`/`web`/`provision`, four-concern review by a
   separate agent, hand-checks to `docs/FIRST-CHURCH-CHECKS.md`).
3. Sanity before every push: `go build ./...`, `go vet ./...`,
   `make lint-go`, `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`.
   Then the brief's own tests (`make test`, `cd web && npm test`,
   `make e2e` where named).
4. Every brief ends with open questions that carry a recommended default.
   Follow the default and note it in the PR; stop only if the default would
   change a DEC.
5. Briefs cite `file:line` at audit time (HEAD `3ec934e`, plus the SEC1 fix
   `28268b3`). Re-check each cited body before editing; line numbers drift.
6. Migrations: new goose file, next free number at implementation time —
   except M7.7, which is the one licensed baseline rewrite (DEC-138).

## Brief files

| File | Items | PLAN milestones | Effort |
|---|---|---|---|
| `R1-deployment.md` | R1 (verdict + image/backup/mail-gate/CI slices) | M5.1, M5.2, M5.4 | L |
| `SEC1-R5-R12-security.md` | SEC1 verify, R5 bundle, R12 rotation | M7.1, M7.2, M7.3 | M ×3 |
| `R2-R3-R4-people.md` | R2 import, R3 TOTP+export, R4 erasure | M4.5, M4.9, M7.8, M7.4 | M ×3 |
| `R6-R11-reliability-perf.md` | R6 outbox, R11 query ceilings | M7.5, M7.6 | M ×2 |
| `R9-baseline-squash.md` | R9 baseline + types + provenance fold | M7.7 | L |
| `R10-R14-features.md` | R10 attendance, R14 sign-up sheets | M6.3, M6.2 | M ×2 |
| `R15-R16-frontend.md` | R15 speed/discoverability, R16 type codegen | M7.9, M7.10 | M ×2 |
| `R13-R17-R18-R19-governance.md` | R13 bets, R17 cuts, R18 tax, R19 locks | DEC-139–141, M7.11, M7.12 | S/M |

SEC1 note: the fix already landed (`28268b3` on
`fix/secure-first-run-setup`; [issue #240](https://github.com/grierj/fold/issues/240)
closed). Its brief section is a verify-and-close checklist for the PR, not a
build spec. R13 and the wording half of R17 landed in PLAN with
DEC-139/DEC-140; their brief sections keep the exact applied text and the
remaining reference-fix list. Likewise DEC-138's row and the DEC counters
already landed; R9 slice S1's remaining work is the `AGENTS.md`
expand/contract replacement, which lands with the squash itself (the old
rule describes the tree correctly until then).

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
| DEC-141 | R19 | Room suggestions + contact machinery frozen |
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
  preconditions proof. Everything that adds tables lands after this wave
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
