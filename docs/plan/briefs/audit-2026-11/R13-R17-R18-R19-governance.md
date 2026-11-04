# Audit 2026-11 — Governance: R13 big bets, R17 cuts, R18 maintenance tax, R19 rooms/contact freeze

Implementation briefs for a coding agent. These are briefs, not code. Read
`AGENTS.md` first; where this file and `docs/plan/PLAN.md` disagree, PLAN wins
and the disagreement gets raised in the PR, not silently resolved.

Owner rulings that bind all four items (2026-11-01):

- **R7 is rejected.** Web push is **not** frozen and SMS **stays** a headline
  promise (PLAN goal 1, `docs/plan/PLAN.md:42`). Nothing in R17 may strike SMS
  milestones (M3.7 Telnyx, M3.8, M3.9) or push milestones. R17's "one adapter"
  rule applied to SMS means: SMS v1 is **Telnyx only**; the Twilio adapter
  (M3.10) is deferred until a church demands it. The AGENTS.md SMS section gets
  reconciled to that (draft text in R17 below).
- **R8 is rejected.** DEC-4 stands (`docs/plan/PLAN.md:2109`): there is no
  open self-signup mode. Nothing here adds one.
- Docs say "recommended", never the other word (the docs-wording guard test
  fails the build). No Python anywhere: new tooling is shell or Go.

Shared rules for all four items:

- One binary, embedded SPA, Postgres only. None of these items adds a table,
  so **no new goose migration** is expected anywhere in this file. If an agent
  finds one is needed, stop and raise it in the PR instead of inventing it.
- R13 and the PLAN/AGENTS parts of R17/R19 ship as **exact draft text** quoted
  in this brief. The implementing agent applies the quoted text verbatim,
  changing only the DEC number the parent assigns. Quoted blocks are marked
  `DRAFT (apply verbatim)`.
- DEC numbering: the highest DEC in PLAN today is DEC-136
  (`docs/plan/PLAN.md:2241`). Other audit briefs may also propose new DECs, so
  this brief never hard-codes its DEC numbers (they landed as DEC-139, DEC-140, DEC-141). It proposes three DECs, in this order,
  as "next free DEC", "next free DEC + 1", "next free DEC + 2". The parent
  renumbers at apply time; titles stay fixed:
  1. **(R13)** The big bets stand; fleet tooling at about five hosted
     churches, still not multi-tenancy.
  2. **(R17)** One adapter per kind until a church demands a second;
     speculative surface cut.
  3. **(R19)** Room suggestions and direct-contact requests are frozen: no
     new cases, purposes, or schema until a first church is live.
- Verification for every slice: `go build ./...`, `go vet ./...`,
  `make lint-go`, `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`,
  then the item's own checks. CI (`test`, `web`, `provision`) must be green
  after **each** slice, never only at the end.
- Tests sit next to the code they test; no milestone-named test files.

---

## R13 — The big bets stand: reaffirm in writing, set the fleet-tooling trigger

- Priority: P1 (audit `docs/AUDIT-2026-11-01.md:190`)
- Effort: S (audit estimate; "write it down")
- Status: Approved 2026-11-01. Explicitly does **not** reverse DEC-1/DEC-24
  or the single-tenant model (`docs/AUDIT-2026-11-01.md:199`).

### Goal

PLAN already contains every bet, but scattered across goals, sections 3, 5,
11 and the DEC table, so each new agent or reviewer re-litigates them. Add
one reaffirmation DEC that names the five bets, the evidence that would
reopen each, and the fleet-tooling trigger (about five hosted churches) that
answers "what about running many churches?" **without** multi-tenancy. After
this lands, a PR proposing `tenant_id`, a second database, a split frontend
deploy, inline role checks, or derived notifications cites this DEC and
stops, instead of re-arguing from scratch.

### Current state (verified by opening each body)

| Bet | Where it already lives |
|---|---|
| Single-tenant, many deployments | Goal 3, `docs/plan/PLAN.md:44`; non-goal "Multi-tenant SaaS", `docs/plan/PLAN.md:52`; "Single-tenant; 'SaaS' means many single-tenant deployments. No multi-tenant data model.", `docs/plan/PLAN.md:126`; hosted shape (one VPS per church, church owns provider accounts, no billing code, no shared control plane), `docs/plan/PLAN.md:1256` (section 11.6, bullets at 1258–1262); D2, `docs/plan/PLAN.md:77` |
| PostgreSQL only | DEC-1, `docs/plan/PLAN.md:2106` ("No SQLite in production, development, or tests. Fold builds on Postgres features (SKIP LOCKED, exclusion constraints, citext, jsonb, advisory locks). Confirmed by DEC-24."); DEC-24, `docs/plan/PLAN.md:2129`; Goal 4, `docs/plan/PLAN.md:45`; C14 with the feature list, `docs/plan/PLAN.md:180`; dev/test shape, `docs/plan/PLAN.md:1292` (11.9) |
| Single Go binary + embedded SPA | Section 3.2, `docs/plan/PLAN.md:124`; Keep list "Single Go binary with the embedded SPA, served behind Caddy", `docs/plan/PLAN.md:139`; stack note keeping Go+Gin+GORM+React, `docs/plan/PLAN.md:151`; code: `//go:embed web/dist` at `main.go:309`, SPA fallback `r.NoRoute(a.serveSPA())` at `main.go:305` (audit cited `main.go:295`, since shifted) |
| Pure-function policy package | `internal/policy/policy.go:1` ("Package policy holds every permission rule (PLAN.md section 6) as pure functions. Handlers load the facts … and ask policy; they never inline role checks or read Group.LeaderID for permissions (C12)."); M0.2 delivered note, `docs/plan/PLAN.md:1558` area (14.3) |
| Stored notifications, guest-link RSVP, safeguarding depth | Shape (stored, fan-out on write, `notify.Send` in the change's transaction), `docs/plan/PLAN.md:562`; contact-proxy safeguarding, section 5.11 at `docs/plan/PLAN.md:540` and DEC-31 at `docs/plan/PLAN.md:2136` |

Two gaps the audit found:

1. No single place says "these were reconsidered on 2026-11-01 and stand",
   so DEC-1 (Oct 9) reads as unrevisited first-draft text.
2. Section 11.6 (`docs/plan/PLAN.md:1256`) describes one hosted church and
   then stops. Nothing says what changes at five hosted churches, so
   "multi-tenancy would be easier to operate" remains an open argument with
   no written answer.

### Scope

In:

- One new DEC row in PLAN section 15.1 (next free DEC), exact text below.
- Two new bullets in PLAN section 11.6 (fleet trigger), exact text below.
- One cross-reference line at the end of the DEC-1 row and the DEC-24 row
  pointing at the new DEC. Nothing else in those rows changes.
- PLAN header bookkeeping the parent already does for new DECs (the
  "Decisions run through DEC-…" line at `docs/plan/PLAN.md:3` is already
  stale — it says DEC-134 while DEC-136 exists at `docs/plan/PLAN.md:2241`;
  fix it to the true highest DEC in the same edit, since this PR touches
  the DEC table anyway).

Out:

- No code, no schema, no `tenant_id` anywhere, no billing, no control plane.
- No fleet tooling itself. This item writes the **trigger** only; building
  fleet tooling is a future milestone that starts when the trigger fires.
- No change to DEC-2/DEC-10/DEC-11 (church-owned accounts, case-by-case
  hosting terms, MIT). The fleet trigger works inside them.
- No edit to `AGENTS.md` architecture bullets; they already state the bets
  (`AGENTS.md` "The one rule" and Architecture sections).

### Slices / steps

**Slice R13.1 — Reaffirmation DEC + 11.6 trigger (one PR, docs only)**

Files: `docs/plan/PLAN.md` only.

1. Parent assigns the DEC number (next free DEC at apply time; call it
   `DEC-N` below). Insert the row after the current last DEC row
   (DEC-136, `docs/plan/PLAN.md:2241`).

   DRAFT (apply verbatim, replacing `DEC-N` with the assigned number):

   > | DEC-N | **The big bets stand; fleet tooling at about five hosted churches, still not multi-tenancy** (Grier, Nov 1, 2026; audit R13). Reconsidered and kept: (1) single-tenant, one deployment per church, no `tenant_id` and no billing code (goal 3, 11.6, DEC-2, DEC-10) — hard isolation is the safeguarding story for teen and consent data, and one church's data can be handed over, moved or deleted as one database; (2) PostgreSQL only, in production, development and tests (DEC-1, DEC-24) — Fold builds on SKIP LOCKED, exclusion constraints, citext, jsonb and advisory locks (C14); (3) one Go binary serving the API and the embedded SPA (3.2, 5.1) — a church volunteer runs `docker compose up` and nothing else; (4) every permission check a pure function in `internal/policy` (section 6, C12); (5) stored notifications written in the change's own transaction (5.12), verified guest-link RSVP (C5), and safeguarding depth (5.10, 5.11, DEC-31) as the differentiator. Reversing any one of these before a first church is live is effort L for zero first-church value. When about five churches are hosted by the same operator, the answer is fleet tooling over separate deployments (11.6), not a multi-tenant rewrite. Reopen a bet only on evidence, recorded in a new DEC: for (1), an operator actually running about five hosted churches whose per-church upgrade, backup-check and secret-rotation work is measured and still painful after fleet tooling; for (2), a church that cannot run Docker at all, or a Postgres limit hit at real church scale; for (3), a deployment target the single binary cannot serve; for (4) or (5), a concrete rule the current shape cannot express. | 1, 3.2, 3.3, 5.1, 5.12, 6, 11.6, 11.9, C12, C14 |

2. Append to DEC-1's row (`docs/plan/PLAN.md:2106`), after "Confirmed by
   DEC-24.":

   DRAFT (apply verbatim): ` Reaffirmed by DEC-N.`

   Append the same sentence to DEC-24's row (`docs/plan/PLAN.md:2129`),
   after "Confirms DEC-1.":

   DRAFT (apply verbatim): ` Reaffirmed by DEC-N.`

3. In section 11.6, after the last bullet ("An ops repo keeps per-church
   `.env` files encrypted with SOPS or `age`. There is no shared control
   plane.", `docs/plan/PLAN.md:1262`), add:

   DRAFT (apply verbatim):

   > - **Fleet trigger (DEC-N):** when one operator hosts about five churches, build fleet tooling before hosting a sixth: one script or small Go program in the ops repo that, per church, pins and bumps `FOLD_VERSION`, runs the upgrade and reads `/healthz` plus the backup `LAST_OK` age, and reports version, health and last-backup as one table. It drives the same per-church compose stacks over SSH or the VPS provider's API. It is not part of the `fold` binary, holds no church data, and adds no church-to-church anything: no shared database, no shared login, no cross-church view. Each church still owns its accounts and can leave with its database and uploads (DEC-2).
   > - Multi-tenancy (one database serving many churches) stays out (DEC-N). Fleet pain is an operations problem and gets an operations answer; mixing teen, consent and contact data from many churches behind row filters would spend the isolation that makes the safeguarding rules simple to reason about.

4. Fix the stale header counts on the lines this PR already touches:
   `docs/plan/PLAN.md:3` and the two "through DEC-134" mentions at
   `docs/plan/PLAN.md:10` and `docs/plan/PLAN.md:98` become "through
   DEC-N" (the true highest DEC after this PR). Do not chase other stale
   references in this PR.

### Tests / checks

- `go test -run TestDocsSayRecommended -count=1 .` — note `docs/plan/` is
  skipped by that guard (`guards_test.go:681`), but run it anyway; the DEC
  text also gets quoted in the PR description, which has no skip.
- `make test` is **not** required for a PLAN-only change; `go build ./...`
  and the docs-wording guard above are the gate. If the parent batches R13
  with R17/R19 PLAN edits, run the same checks once for the batch.
- Grep proof the trigger landed once and only once:
  `grep -n "Fleet trigger" docs/plan/PLAN.md` → one hit in 11.6, plus the
  DEC-N row mention.

### Acceptance checklist

- [ ] DEC-N row present verbatim (number substituted), naming all five bets
      and the reopen evidence for each.
- [ ] DEC-1 and DEC-24 rows end with "Reaffirmed by DEC-N." and are
      otherwise unchanged.
- [ ] 11.6 has the two new bullets verbatim; "There is no shared control
      plane." above them is unchanged and still true.
- [ ] No `tenant_id`, billing, or control-plane wording appears anywhere in
      the diff except as a thing being refused.
- [ ] Header DEC counts on touched lines match the true highest DEC.

### Risks

- **DEC number collision** with a parallel brief. Mitigated by the
  "next free DEC" convention above; the parent renumbers once.
- **Trigger read as a commitment to host five churches.** The draft says
  "when one operator hosts about five", which includes operators who are
  not Grier (11.6 already allows anyone to host). It commits nobody to
  hosting.
- **Trigger number treated as exact.** The draft says "about five" in both
  places on purpose. Do not tighten it while applying.

### Dependencies

- None. R13 can land before, with, or after R17/R19. If batched, apply R17's
  DEC after R13's so the proposed order (R13, R17, R19) holds.

### Open questions (with defaults)

1. Should the fleet tool be a `fold` subcommand instead of an ops-repo
   script? **Default: no.** The binary is per-church and must never see
   another church's host or credentials (C11's lazy-config rule,
   `docs/plan/PLAN.md:177`). The ops repo already exists in 11.6 for
   encrypted per-church `.env` files.
2. Should reopening a bet require a new DEC, or is a Grier PR comment
   enough? **Default: new DEC**, as drafted. Every other reversal in
   section 15 is recorded that way.

---

## R17 — Cut speculative surface in writing; delete dead code

- Priority: P2 (audit `docs/AUDIT-2026-11-01.md:228`)
- Effort: S (audit estimate)
- Status: Approved 2026-11-01, **as bounded by the R7 rejection**: SMS
  milestones M3.7–M3.9 stay and SMS stays in goal 1. The only SMS cut is the
  scheduled **second** adapter (Twilio, M3.10), deferred until demanded.
  Reverses goal 6's "two adapters by v1.0" (`docs/AUDIT-2026-11-01.md:235`).

### Goal

Every unbuilt thing PLAN still promises costs design attention and misleads
agents about scope. Strike the named speculative items from the roadmap in
writing (one DEC), and delete the one piece of built-but-dead code: the Go
SMS counter that no production caller reaches, duplicated in TypeScript
where the composer actually uses it. SMS counting ends with exactly **one
live home** — the TypeScript counter the composer renders — until M3.7
wires a server caller and restores the Go side from git history in the same
PR that sends a text.

### Current state (verified by opening each body)

**Dead code — which side is live.** Both counters implement the same
GSM-7/UCS-2 segment math and run one shared case table:

- TypeScript is the live side. `countSms` (`web/src/lib/sms.ts:19`),
  `framedSms` (`web/src/lib/sms.ts:59`) and `isDonationAsk`
  (`web/src/lib/sms.ts:83`) are imported by the composer tab
  (`web/src/components/messages/SmsComposer.tsx:1`, counter rendered at
  `web/src/components/messages/SmsComposer.tsx:12`), which the composer page
  renders when `options.smsEnabled` is true
  (`web/src/pages/MessageComposer.tsx:485`). That flag stays false until an
  SMS provider is configured (DEC-117, `docs/plan/PLAN.md:2222`), so the tab
  is built, tested (`web/src/components/messages/SmsComposer.test.tsx`,
  `web/src/lib/sms.test.ts:26`) and hidden — but it is the only side wired
  to anything a person can reach.
- Go is the dead side. `CountSMS` (`internal/content/sms.go:42`),
  `FramedSMS` (`internal/content/sms.go:94`) and `DonationAsk`
  (`internal/content/sms.go:109`) have **zero production callers**: a repo
  grep finds them only in `internal/content/sms.go` and
  `internal/content/sms_test.go` (`TestCountSMS` at line 57, `TestDonationAsk`
  at line 76, both reading `../../web/src/lib/sms-cases.json` at line 43).
  The deadcode gate hides this with an allow line
  (`.deadcode-allow:4`, gate at `Makefile:54`, run in CI at
  `.github/workflows/test.yml:40`). The rest of `internal/content`
  (`content.go`, `email.go`, `merge.go`) is heavily used — e.g.
  `api_message_preview.go:45`, `messages_send.go:661` — so only the two SMS
  files go; the package stays.
- PLAN's wording must catch up either way: M3.4b says the counter was built
  "Go and TypeScript, one table of cases" (`docs/plan/PLAN.md:1787`), and
  M3.7's scope (`docs/plan/PLAN.md:1830`) assumes a server side exists.

**The `.deadcode-allow` file** (4 lines, opened): `internal/testdb`
helpers (line 1, test-only by design), `notify.Kinds` (line 2, registry
inspection seam), `models.All` (line 3, used by the migration drift check),
and the SMS trio (line 4). Only line 4 excuses code that nothing, test or
tool, needs as a seam.

**Speculative surface in PLAN** (each body opened):

| Item | Where | Built? |
|---|---|---|
| Render Blueprint M5.5 | Milestone body "optional, future; not committed", `docs/plan/PLAN.md:2062`; release row "Future, on request (not committed)", `docs/plan/PLAN.md:1539`; evaluation §12.5 from `docs/plan/PLAN.md:1390`; goal 2 clause, `docs/plan/PLAN.md:43`; DEC-26 | No: no `deploy/render.yaml` in the tree |
| Hash-chained durable audit M6.14 | Roadmap line `docs/plan/PLAN.md:2090`; design sketch §10.7 from `docs/plan/PLAN.md:1142`; DEC-33 at `docs/plan/PLAN.md:2138`; §13.2 row "Later", `docs/plan/PLAN.md:1484` | No: no `prev_hash` anywhere in Go/migrations; regular `AuditLog` carries these events today (§10.7 first line) |
| ChMS roster sync M6.15 | Roadmap line `docs/plan/PLAN.md:2091`; §13.2 row "Future", `docs/plan/PLAN.md:1481`; DEC-40 at `docs/plan/PLAN.md:2145`; open question in §15.2, `docs/plan/PLAN.md:2249` area | No |
| S3 storage + private assets M4.7 | Milestone body `docs/plan/PLAN.md:1992`; §5.7 at `docs/plan/PLAN.md:464`; C9 at `docs/plan/PLAN.md:175`; v0.8 release row `docs/plan/PLAN.md:1536`; M4.8 "Depends on: M4.4, M4.5, M4.6, M4.7" at `docs/plan/PLAN.md:2002` | No: no `S3Store`, no `minio-go` in `go.mod`; `storage.Store` has the local store only |
| Child check-in (QR/self check-in M6.4, kiosk) | Roadmap `docs/plan/PLAN.md:2080`; §13.2 "Later" rows at `docs/plan/PLAN.md:1465` and `docs/plan/PLAN.md:1474`; non-goal already for v1 at `docs/plan/PLAN.md:64` | No |
| Online giving / accounting | Already refused: non-goal `docs/plan/PLAN.md:53`, "Out of scope" `docs/plan/PLAN.md:1490`, reasons §13.3 `docs/plan/PLAN.md:1496` | No — cut is already written; R17 DEC just confirms it |
| Second adapters per kind | Goal 6 "By v1.0 each kind has at least two working options", `docs/plan/PLAN.md:47`; D8 "(one first, the second when needed; at least two per kind by v1.0)", `docs/plan/PLAN.md:83`; §5.6 adapter list `docs/plan/PLAN.md:446`, SMS line "then `twilio` (M3.10, before v1.0)" at `docs/plan/PLAN.md:449`; M3.10 body `docs/plan/PLAN.md:1862`; v1.0 release row "Twilio adapter, …" at `docs/plan/PLAN.md:1537`; §3.2 summary `docs/plan/PLAN.md:132`; signup step "Twilio once M3.10 lands" `docs/plan/PLAN.md:1212` | Email already one-until-asked ("Later, when a church asks: `resend`, `mailgun`", `docs/plan/PLAN.md:448`); Twilio is the only **scheduled** second adapter |
| AGENTS.md SMS section | `AGENTS.md:257` heading "(messaging Phase 3, not yet)", body at `AGENTS.md:259`: "provider adapters (Telnyx default, Twilio next) fed by `internal/notify` deliveries … broadcast-only v1, replies route to the group leader. Don't start early." | Stale twice: "Twilio next" contradicts one-adapter; "broadcast-only v1, replies route to the group leader" contradicts PLAN's relay design (replies reach the latest author, §7.8 via M3.7 acceptance `docs/plan/PLAN.md:1838`) |

### Scope

In:

- One new DEC (next free DEC + 1 in this brief's ordering), exact text below.
- PLAN wording edits in the table below: **words only**, no code, except
  where "delete now" says so.
- Delete now: `internal/content/sms.go`, `internal/content/sms_test.go`,
  and `.deadcode-allow` line 4. Keep `web/src/lib/sms.ts`,
  `web/src/lib/sms-cases.json`, `web/src/lib/sms.test.ts`, `SmsComposer`
  and its test — the one live place SMS counting exists (composer needs it
  the day M3.7 flips `smsEnabled`).
- AGENTS.md SMS section replacement, exact text below.

Out:

- SMS milestones M3.7 (Telnyx), M3.8, M3.9, M3.12, M4.6 stay. So do the
  composer's hidden SMS tab (DEC-117) and the donation warning (DEC-120).
- Web push is untouched (R7 rejected).
- Generic people/CSV import (M4.5, audit R2) stays — it is what makes
  cutting ChMS **sync** safe: a ChurchTrac export still enters as a file,
  with no vendor adapter and no field locking. M6.11's sample-gate wording
  belongs to the R2 brief, not this one.
- `storage.Store` seam and local uploads stay exactly as built; galleries
  (M6.9) stay on the roadmap, now plainly local-first.
- The regular `AuditLog` and every `audit.Write` call stay. Cutting M6.14
  removes the promised hash chain, never today's audit rows.

### Slices / steps

**Slice R17.1 — Delete the dead Go SMS counter (code, one PR)**

1. Delete `internal/content/sms.go` and `internal/content/sms_test.go`.
2. Delete `.deadcode-allow` line 4 (the `CountSMS|FramedSMS|DonationAsk`
   exemption). Lines 1–3 stay; the Makefile comment above the gate
   (`Makefile:52`) already says only test seams are allowed, and becomes
   fully true.
3. Fix the back-reference in the kept side so it no longer promises a twin:
   in `web/src/lib/sms.ts:1`, replace "It is the same logic as
   internal/content/sms.go, and both run sms-cases.json." with:

   DRAFT (apply verbatim):

   > // Texts (PLAN 7.3, M3.4b): the composer's live counter, the one place
   > // SMS segment counting lives until texting ships (M3.7 restores a Go
   > // twin wired to the send path, from git history, running sms-cases.json).

   Keep the rest of that header comment. `web/src/lib/sms-cases.json` stays
   exactly as is — it is the contract the M3.7 twin must pass on day one,
   and `web/src/lib/sms.test.ts` keeps guarding it.
4. Do **not** add an API endpoint to replace the Go side. The composer
   counts locally today (`web/src/components/messages/SmsComposer.tsx:12`);
   server-side counting has no caller until a send exists to bill, cap or
   frame. That is the whole point of deleting now.

**Slice R17.2 — The cut DEC + PLAN/AGENTS wording (docs, one PR; may ride
with R17.1 if the parent prefers one PR)**

Files: `docs/plan/PLAN.md`, `AGENTS.md`.

1. Insert the new DEC row after R13's DEC (or the then-last row).

   DRAFT (apply verbatim, `DEC-M` = assigned number):

   > | DEC-M | **One adapter per kind until a church demands a second; speculative surface cut** (Grier, Nov 1, 2026; audit R17). Goal 6's "two working options per kind by v1.0" is struck: Fold ships one real adapter per kind (plus `devoutbox` for tests) and adds a second only when a church asks for it, brings its own account (DEC-2) and cannot use the first. SMS v1 is therefore **Telnyx only** (M3.7–M3.9 unchanged); the Twilio adapter (old M3.10) is deferred until demanded — Twilio-only registration paths (sole-proprietor, toll-free) arrive with it, and "get a free EIN" stays the default advice until then (DEC-3). Render (old M5.5, DEC-26), the hash-chained durable audit log (old M6.14, DEC-33), ChMS roster sync (old M6.15, DEC-40) and the S3 storage adapter (old M4.7) are cut from the roadmap; DEC-26, DEC-33 and DEC-40 stay as the record of the reasoning, and each item re-enters only as a new milestone with a named church asking. The regular `AuditLog`, generic CSV people import (M4.5) and local-disk uploads behind `storage.Store` are the answers in the meantime. Online giving, fund accounting and contribution statements (13.3) and child check-in kiosks (old M6.4) are confirmed **out**, not later. The unused Go SMS counter is deleted; SMS counting lives in the composer's TypeScript (`web/src/lib/sms.ts`) until M3.7 wires a server caller. | 1, 2 (D8), 5.6, 5.7, 10.7, 12.5, 13.2, 14.2, M3.7, M4.8 |

2. Goal 6 (`docs/plan/PLAN.md:47`). Replace the whole numbered item with:

   DRAFT (apply verbatim):

   > 6. Every proprietary dependency sits behind an adapter interface. Build one real adapter per kind (plus the `devoutbox` for tests), and add a second only when a church demands it (DEC-M). For example SMTP and Postmark for email exist because the first churches need both a generic fallback and a deliverability default; SMS v1 is Telnyx only, and Twilio waits for a church that asks.

3. D8 (`docs/plan/PLAN.md:83`). Replace the requirement cell with:

   DRAFT (apply verbatim):

   > Proprietary deps sit behind adapters: one real adapter per kind, a second only when a church demands it (DEC-M)

4. Release table 14.2, v1.0 row (`docs/plan/PLAN.md:1537`). Replace with:

   DRAFT (apply verbatim):

   > | v1.0 | Hardening, sign-up sheets, attendance, security review | M6.2, M6.3 + fixes |

   In the same table: v0.8 row (`docs/plan/PLAN.md:1536`) becomes
   `| v0.8 CLI | fold setup | M4.8 |`; delete the "Future, on request (not
   committed)" Render row (`docs/plan/PLAN.md:1539`); the "v1.x and later"
   row (`docs/plan/PLAN.md:1538`) loses "the durable audit log (DEC-33)"
   and "including M6.14", becoming `| v1.x and later | Community features | Rest of M6 |`.
   The **v0.7 SMS row does not change** (Telnyx, M3.7–M3.9, M3.12, M4.6).

5. Milestone bodies: delete the **M3.10 Twilio adapter** block
   (`docs/plan/PLAN.md:1862`), the **M4.7** block
   (`docs/plan/PLAN.md:1992`) and the **M5.5** block
   (`docs/plan/PLAN.md:2062`) outright — DEC-M and git history keep their
   content findable. Then fix the references those bodies fed:
   - M4.8 "Depends on" (`docs/plan/PLAN.md:2002`): drop `, M4.7`.
   - §5.6 SMS adapter line (`docs/plan/PLAN.md:449`) becomes:

     DRAFT (apply verbatim):

     > - SMS: `devoutbox`, `telnyx` (default, and the only one until a church demands another, DEC-M). `twilio` and `plivo` only if a church asks.

   - §5.6 Storage line (`docs/plan/PLAN.md:452`) becomes:

     DRAFT (apply verbatim):

     > - Storage: `local`. An S3-compatible adapter (one client would cover B2, R2, Wasabi, Spaces, MinIO) only if a church asks (DEC-M). "S3-compatible" is only the protocol; none of these is Amazon.

   - §3.2 SMS summary (`docs/plan/PLAN.md:132`): "provider adapters,
     Telnyx first and Twilio second" becomes "a Telnyx adapter first;
     a second SMS adapter only when a church demands one (DEC-M)".
   - Setup step (`docs/plan/PLAN.md:1212`): "Pick a provider (Telnyx is
     preselected; Twilio once M3.10 lands)" becomes "Telnyx is the SMS
     provider (a second adapter only when a church demands one, DEC-M)".
     Leave the surrounding DEC-3 registration guidance (EIN default,
     Twilio sole-proprietor caveats in §12.2's table) — it becomes the
     record of what a future Twilio milestone must handle.
   - §3.4 (`docs/plan/PLAN.md:155`): "private assets/S3 are future work"
     becomes "private assets on local disk are future work; S3 is cut
     until a church asks (DEC-M)".
   - M3.4b (`docs/plan/PLAN.md:1787`): "and the SMS segment counter and
     donation-ask warning (Go and TypeScript, one table of cases)" becomes
     "and the SMS segment counter and donation-ask warning (TypeScript;
     the Go twin was deleted as dead code and returns with M3.7's send
     path, DEC-M)". DEC-117/DEC-120 rows stay.
   - M3.7 scope (`docs/plan/PLAN.md:1830`): append one sentence:
     "Restoring the Go SMS counter (`internal/content`, from git history)
     wired to this send path, passing `web/src/lib/sms-cases.json` like the
     composer's counter (DEC-M)."

6. Sections that described cut designs shrink to pointers, so agents stop
   designing against them:
   - §10.7 (`docs/plan/PLAN.md:1142`): keep the heading and the first
     sentence about the regular `AuditLog`; replace the sketch bullets
     with: "Cut Nov 1, 2026 (DEC-M). The sketch that was here is in git
     history and DEC-33; reopen only for a named church with a legal
     retention need, as a new milestone with acceptance written first."
   - §5.7 (`docs/plan/PLAN.md:464`): keep the private-asset rule (private
     keys served through an auth-checked handler on local disk when
     galleries need it); delete the `S3Store`/`minio-go` bullet and the
     `fold storage migrate` bullet; add: "No S3 adapter until a church
     asks (DEC-M); `storage.Store` is the seam it would land behind."
     C9 (`docs/plan/PLAN.md:175`) loses "or by short-lived presigned URLs
     on S3" and its milestone cell becomes "M6.9 (local); S3 cut, DEC-M".
   - §12.5 Render evaluation (from `docs/plan/PLAN.md:1390`): keep at most
     the one-paragraph pricing summary under a new first sentence,
     "Cut Nov 1, 2026 (DEC-M); kept as a reference evaluation only — there
     is no Render milestone." Goal 2's Render clause
     (`docs/plan/PLAN.md:43`) becomes "A VPS with Compose is the only
     recommended deployment (12.5, DEC-26, DEC-M)."
   - Roadmap Phase 6 list: delete the M6.4 line
     (`docs/plan/PLAN.md:2080`), the M6.14 line
     (`docs/plan/PLAN.md:2090`) and the M6.15 line
     (`docs/plan/PLAN.md:2091`). Renumber **nothing**; gaps are fine (the
     list already carries "moved"/"dropped" lines). §13.2 rows: QR/self
     check-in and child check-in kiosk tags become **Out of scope**,
     citing DEC-M; the ChMS row's tag becomes **Out of scope** with "cut
     (DEC-M); generic CSV import (M4.5) is the path"; the durable-audit
     row's tag becomes **Out of scope**, citing DEC-M. §15.2's
     "DEC-40/M6.15" open item is deleted (answered by cutting it).
   - `docs/plan/SUMMARY.md` mentions in passing only — its ChMS line
     (`docs/plan/SUMMARY.md:82`) is handled by R18's shrink; do not
     edit SUMMARY in this slice.

7. `AGENTS.md` SMS section (`AGENTS.md:257`). Replace heading and body with:

   DRAFT (apply verbatim):

   > ## SMS (messaging Phase 3, not yet)
   >
   > Not built, and not required to run Fold. SMS stays a headline promise
   > (PLAN goal 1); it starts at M3.7, not early. When you build it: one
   > provider adapter — **Telnyx** (DEC-M: a second adapter such as Twilio
   > only when a church demands one) — fed by `internal/notify` deliveries
   > (PLAN 5.12, 5.6), with keyword opt-outs handled at once (DEC-27) and
   > replies relayed through Fold to the author of the person's latest
   > message (PLAN 7.8, M3.7). SMS segment counting currently lives in the
   > composer only (`web/src/lib/sms.ts`); restore the Go counter from git
   > history in the same PR that wires the send path (DEC-M).

### Tests / checks

R17.1:

- `make deadcode` — the gate itself proves nothing else went unreachable
  and that allow line 4 is gone (`Makefile:54`).
- `go build ./...` and `go vet ./...` — `internal/content` still compiles
  for its many live callers.
- `make test` — full Go suite; the deleted tests' cases still run on the
  kept side: `(cd web && npm test -- sms)` covers `web/src/lib/sms.test.ts`
  and `SmsComposer.test.tsx` against `sms-cases.json`.
- `(cd web && npx knip)` — no orphaned exports on the kept side.
- Negative grep: `grep -rn "CountSMS\|DonationAsk\|FramedSMS" --include="*.go" .`
  returns nothing.

R17.2:

- Docs-wording guard as in R13.
- Consistency grep after editing, each must return only DEC-M-refusal or
  historical-research hits, never a live promise:
  `grep -n "M3.10\|M5.5\|M6.14\|M6.15\|M4.7" docs/plan/PLAN.md` — expected
  survivors: DEC-26/DEC-33/DEC-40 rows, C10's Render mention is **not**
  expected — check it (`docs/plan/PLAN.md:176` says "Render can run the
  same image (12.5)"; reword to "and any container host can run the same
  image").
- `grep -n "two working options\|at least two" docs/plan/PLAN.md` → only
  inside DEC-M's record of what was struck.

### Acceptance checklist

- [ ] `internal/content/sms.go`/`sms_test.go` gone; `.deadcode-allow` is
      3 lines; `make deadcode` green.
- [ ] One live SMS counter: `web/src/lib/sms.ts`; its header comment says
      where the Go twin returns (M3.7) and names `sms-cases.json`.
- [ ] DEC-M row verbatim; goal 6, D8, 14.2 (v0.8/v1.0/v1.x rows), §5.6,
      §3.2, §3.4, setup step, M3.4b, M3.7 all match the drafts.
- [ ] M3.10, M4.7, M5.5 bodies deleted; M6.4/M6.14/M6.15 roadmap lines
      deleted; §10.7, §5.7, §12.5 shrunk to pointers; §13.2 tags updated;
      §15.2 ChMS item gone.
- [ ] v0.7 SMS release row and M3.7–M3.9 bodies **unchanged** except the
      one appended M3.7 sentence. Goal 1 still names SMS.
- [ ] AGENTS.md SMS section verbatim; it no longer says "Twilio next" or
      "broadcast-only".
- [ ] No milestone renumbering anywhere.

### Risks

- **Deleting the Go counter reads as un-building M3.4b.** It is preserved
  in git history and the case table stays guarded by the TS tests, so
  M3.7 restores proven code instead of rewriting it. The DEC and the
  M3.7 scope sentence make the restore an explicit step, not a memory.
- **§12.2's Twilio-heavy registration research now describes a provider
  with no milestone.** Intentional: it is the demand-triggered milestone's
  starting brief (sole-proprietor limits, review timelines). Deleting
  research would make the future work more expensive, not cheaper.
- **A church that can only register via Twilio's sole-proprietor path has
  no SMS on day one.** True and accepted by DEC-M: the default answer is
  "get a free EIN" (DEC-3). Such a church asking *is* the demand trigger.
- **Private assets without S3 cap gallery storage at local disk.** Fine at
  church scale; the `storage.Store` seam keeps the future adapter a
  drop-in, and backups already tar `/data`.

### Dependencies

- DEC ordering with R13 (R13's DEC first). Otherwise independent.
- R2 (generic CSV import) is the reason cutting M6.15 is safe; if R2 were
  rejected later, revisit this cut — note it in the R2 PR, not here.
- M3.7's own brief must include the Go-counter restore (sentence added to
  its scope by this item).

### Open questions (with defaults)

1. Keep the Go counter but mark it build-tagged/test-only instead of
   deleting? **Default: delete.** A test-only copy still drifts against
   the live TS one every time the case table changes, which is the exact
   failure the audit named; git history is the archive.
2. Does cutting M4.7 also cut *private* assets entirely? **Default: no.**
   Galleries (M6.9) still need non-public photos of minors; the local
   auth-checked handler is small and stays future work under M6.9. Only
   the S3 adapter and `fold storage migrate` are cut.
3. Should DEC-26/33/40 rows be edited to say "cut"? **Default: no.**
   DEC rows are history; DEC-M cites and supersedes them, following the
   section-15 convention that later amendments win
   (`docs/plan/PLAN.md:10`).

---

## R18 — Reduce maintenance tax: tier the guards, finish the ratchet, embed templates, shrink status docs

- Priority: P2 (audit `docs/AUDIT-2026-11-01.md:238`)
- Effort: S (audit estimate; several small PRs, not one)
- Status: Approved 2026-11-01. Explicit non-goals from the audit: no
  big-bang `package main` split, no framework churn, no ESLint
  mass-reformat (`docs/AUDIT-2026-11-01.md:243`).

### Goal

`guards_test.go` (1,172 lines, 28 tests) mixes two different jobs: security,
privacy and contract tripwires that belong in `go test`, and repo-hygiene
greps (wording, Python, Node version, CI config) that every contributor
pays for in the root test package. Tier every guard deliberately, move the
hygiene greps to one shell lint with equivalent coverage wired **before**
the Go tests come out, finish `web/src/components/Notifications.tsx` so
the UI ratchet can become plain zero-tolerance guards, embed the HTML
templates so the binary stops reading its working directory, point guard
failure messages at files that exist, and shrink the two stale status
docs to what a reader actually needs. CI is green after each slice.

### Current state (verified by opening each body)

Every test in `guards_test.go`, tiered. "Catcher" tests exist to prove a
guard fires on the past mistake it was written for; a catcher follows its
guard's tier (kept catchers stay, moved guards lose their catcher to the
lint's self-test in slice R18.1 step 3).

**Keep in Go — security / privacy / contract:**

| Test | Line | Why it stays |
|---|---|---|
| `TestGroupDetailsGoThroughGroupShownTo` + catcher `TestGroupDetailGuardCatchesPR41Leak` | `guards_test.go:42`, `:56` | Invite-only group name/place leak (privacy). Non-negotiable (audit "What not to do", `docs/AUDIT-2026-11-01.md:302`) |
| `TestOnlyLoginAndInvitesAnswer429` | `guards_test.go:368` | DEC-52: public forms never reveal account existence (security). AST walk over handlers; no cheap lint equivalent |
| `TestOneEventVisibilityFilter` | `guards_test.go:1089` | Event-visibility filtering discipline (privacy); AST loop detection |
| `TestRegistrationQueriesUseSubjectScope` | `guards_test.go:1155` | Registration subject scoping (privacy of household people) |
| `TestPagesNeverShowServerErrorText` + catcher `TestErrorTextGuardCatchesRawMessages` | `guards_test.go:552`, `:577` | DEC-61 error hygiene; keeps server internals off screens. Keep in Go: it shares the `web/src` walk helpers with neighboring guards, and its regex has named false-positive cases the catcher pins (`guards_test.go:587`) |
| `TestWebTypesMatchGoJSON` + catcher `TestWebTypesGuardCatchesFieldRename` | `guards_test.go:219`, `:239` | Go↔TS field contract over the ~100 pairs in `tsContracts` (`guards_test.go:66`). Stays **until R16** (type generation) lands; deleting it is R16's job, in R16's PR, not here |
| `TestNotificationCategoriesMatch` | `guards_test.go:1052` | Three-way contract: `notify.Categories`, the SQL CHECK (`migrations/00010_notifications.sql`, named at `guards_test.go:1073`), the TS union |
| `TestWorkflowActionsPinnedAndReadOnly` | `guards_test.go:1131` | Supply-chain security (full-SHA pins, read-only token). It reads YAML, but the property is security, so it keeps Go-test weight; a shell grep for `uses:` lines would need the same pin regex (`guards_test.go:1136`) re-implemented worse |
| `TestColorsComeFromThemeTokens` + catcher `TestColorGuardCatchesHardCodedColors` | `guards_test.go:476`, `:512` | DEC-60 theme contract. The `@theme` block state machine (`guards_test.go:448`) is real parsing, not a grep; keep in Go and say so in the tier comment |
| `TestReactFormsUseCustomValidation` + catcher `TestReactFormValidationGuard` | `guards_test.go:967`, `:991` | DEC-65 form contract: no browser bubbles, first-invalid focus. Behavioral, not cosmetic |
| `TestFormControlsComeFromComponentsUI` + catcher `TestFormControlGuardCatchesPlaceholderLabel` | `guards_test.go:1029`, `:1035` | DEC-65 labelled-controls contract (a11y). Already zero-tolerance — this is the shape the ratchet converts to |
| `TestUIRatchets` + catcher `TestUIRatchetsCatchPastMistakes` | `guards_test.go:878`, `:912` | **Converted**, not deleted outright: see slice R18.2. The five rules (`guards_test.go:785`) become one zero-tolerance guard once the remainder (below) reaches zero |
| `TestTestFilesSitNextToTheirSource` + catcher `TestTestLayoutGuardCatchesMilestoneFiles` | `guards_test.go:316`, `:345` | Keep in Go with written justification: the stem/extension logic (`guards_test.go:282`) is the most intricate of the hygiene checks, its failure message tells the author exactly where to move the test, and it guards the discoverability the other guards rely on. Moving it buys nothing — it must run before `go test` compiles a misplaced `_test.go` anyway. (This is the brief's one "keep, though a lint could do it" call; see Open questions.) |

**Move to lint — wording / Python / Node / misc config** (replacement named
per move; all four land in **one new script**, `scripts/lint-repo.sh`,
wired as `make lint-repo`, added to `make lint` (`Makefile:45`) and to
CI's existing lint step (`.github/workflows/test.yml:40`) in the same PR
that deletes the Go tests):

| Test | Line | Replacement check (exact) |
|---|---|---|
| `TestNoPython` + catcher | `guards_test.go:636`, `:648` | In `lint-repo.sh`: fail on any `*.py` outside `.git`/`node_modules`, plus a grep for the interpreter pattern from `guards_test.go:594` over the same file set `scriptFiles` walks (`guards_test.go:610`: `*.sh`, `*.yml`, `*.yaml`, `*.mjs`, `Makefile`, `Dockerfile`, `.cursor/skills` non-md). Message keeps "Fold tooling is shell or Go only" |
| Docs-wording test + catcher | `guards_test.go:676`, `:701` | In `lint-repo.sh`: case-insensitive word grep for the banned word family from `guards_test.go:661` over `*.md`, with the same three behaviors as today: `docs/plan` tree skipped (`guards_test.go:681`), `wording-ok:` line escape, and the quoted-JSON-key exception (`guards_test.go:666`). Message keeps 'say "recommended"'. The script builds the pattern from pieces (split the literal across shell quotes) so the script does not trip its own check if the scan set ever widens past `*.md` |
| `TestNodeVersionComesFromNvmrc` + catcher | `guards_test.go:740`, `:758` | In `lint-repo.sh`: read `.nvmrc`; assert `Dockerfile` matches `^FROM node:<major>`; assert no `node-version:` (as opposed to `node-version-file:`) in `.github/workflows/*.yml`; assert any `Node <n> LTS` in `README.md`/`AGENTS.md` equals the `.nvmrc` major. Same three rules as `nodeVersionViolations` (`guards_test.go:714`) |
| `TestDependabotCoversEveryEcosystem` | `guards_test.go:247` | In `lint-repo.sh`: grep `.github/dependabot.yml` for the four `package-ecosystem:` values (`guards_test.go:252`) and `.github/workflows/test.yml` for the three step strings (`guards_test.go:261`). Deliberately a substring lint: that is all the Go test ever did |
| `TestNoVideoEmbed` | `guards_test.go:403` | In `lint-repo.sh`: case-insensitive grep for `<iframe` under `web/src`, allowing only `web/src/pages/MessageComposer.tsx` hits immediately preceded by an `iframe-ok:` line — the exact rule at `guards_test.go:415`. DEC-16 is product, but the check is a pure grep with one named exception |

**Drop (no replacement):** none on day one. The ratchet *mechanism*
(`ui-ratchet.json`, `UPDATE_UI_RATCHET` rewrite at `guards_test.go:880`,
the "fell from N, lower it" branch at `guards_test.go:869`) is dropped in
R18.2 once every count is zero, replaced by the zero-tolerance guard
above. There is no other drop candidate with a clean conscience: every
remaining guard names the past PR mistake it catches (file header,
`guards_test.go:21`).

**UI ratchet remainder** (`ui-ratchet.json`, opened; 21 counted hits in 11
file/rule cells):

- `rawControls` — `web/src/components/Notifications.tsx`: 7;
  `web/src/components/Layout.tsx`: 1. In Notifications.tsx (438 lines) the
  raw buttons are: Restore (`:107`), toast Undo (`:150`), "Try again"
  (`:166`), "Show more" (`:266`), the bell (`:362`), "Mark all as read"
  (`:394`), plus the `btn-secondary` class on the Review link (`:99`).
  The bell is the hard one: it needs `bellRef` focus return
  (`web/src/components/Notifications.tsx:308`, restored at `:330`), and
  `Button` today spreads props but declares no ref
  (`web/src/components/ui/Button.tsx:20`) — React 19 takes `ref` as a
  plain prop, so `Button` gains an optional `ref` and passes it through.
  `ButtonLink` already exists for the Review link
  (`web/src/components/ui/Button.tsx:24`).
- `danger` — `DeleteUserPanel.tsx`: 3, `GroupVisibilityReview.tsx`: 2,
  `components/Notifications.tsx`: 1 (the row error at `:104`, a genuine
  error), `EventDetail.tsx`: 1. Each cell is either a real error or a
  destructive action (annotate `// danger-ok: <reason>` — the rule's own
  escape at `guards_test.go:798`) or a neutral state wearing red (restyle
  to `text-text-muted` / `EmptyState` per the fix text at
  `guards_test.go:798`). Decide per line while doing it; do not blanket-
  annotate.
- `dateFormatting` — `components/Notifications.tsx`: 2 (title attribute
  `toLocaleString` at `:73`, offline clock `toLocaleTimeString` at
  `:158`); `web/src/lib/notifications.ts`: 1 (`Intl.DateTimeFormat` in
  `relativeTime`, `:253`); `web/src/lib/eventForm.ts`: 1 (zone-name parts,
  `:43`); `web/src/pages/Admin.tsx`: 1 (`toLocaleDateString` at `:248`).
  The rule's fix is "format with `lib/when.ts`" (`guards_test.go:811`),
  which already exports the needed shapes (`formatTime`,
  `web/src/lib/when.ts:30`; `formatWhen`, `:37`; `zoneOffset`, `:119`).
  The two `lib/` cells move their formatting primitive **into**
  `lib/when.ts` (or `lib/wall.ts`, the rule's two exempt homes,
  `guards_test.go:813`) and import it back, so behavior is unchanged.
- `importantOverrides` — `web/src/pages/Profile.tsx`: 1 (`!` class; pick a
  Button/Chip size instead, per `guards_test.go:793`).
- `styleLiterals` — already `{}`. The catcher case at `guards_test.go:922`
  proves the guard once caught `web/src/pages/Notifications.tsx`; that
  page (76 lines) is **already clean** — the unfinished file is the
  components one. Do not "finish" the wrong Notifications.tsx.

**Templates:** the binary still reads its working directory:
`r.LoadHTMLGlob("templates/*.html")` (`main.go:257`) over setup, unlock
and outbox templates; the image compensates with a pinned `WORKDIR` and a
`COPY … /src/templates /templates` (`Dockerfile:26`, `Dockerfile:30`), and
`serve` logs the working directory because relative paths are otherwise
invisible (`main.go:99`). The SPA and member help are already embedded
(`main.go:309`, `main.go:312`).

**Guard messages naming files that do not exist** (grep run): the color
guard sends people to "TOKENS.md" (`guards_test.go:470`) and the ratchet
to "ENFORCEMENT.md" (`guards_test.go:771`, `guards_test.go:788`); neither
file exists anywhere in the tree. `web/src/components/ui/Button.tsx:4`
also cites ENFORCEMENT.md. Live homes for those rules: the `@theme` block
in `web/src/index.css` (already the color guard's `themeFile`,
`guards_test.go:443`) and DEC-60–66 as summarized in AGENTS.md's UI
controls bullet.

**Status docs:** `STATUS.md` (101 lines) opens "Reconciled against merged
history through PR #163 … Product decisions run through DEC-87"
(`STATUS.md:5`) yet its table runs through the M3.13 push rows (~PR #223)
and DEC-136 exists — the header is stale by roughly 60 PRs, and most of
the file is a per-PR receipt ledger git already keeps. Its living part is
"Remaining follow-ups" (`STATUS.md:85` area). `docs/plan/SUMMARY.md` (121
lines) is "Reconciled October 27, 2026 against merged work through M1.14"
(`docs/plan/SUMMARY.md:3`); its "Next phases" table still lists Phase 2
rooms and Phase 3 messaging as unshipped (both since merged), and ~30
lines re-litigate the M1.14 publication stack that
`docs/plan/M1.14-VERIFICATION.md` already records.

### Scope

In: the five slices below. Out (explicit, per the audit): splitting
`package main`, any framework or router change, introducing ESLint or a
formatter sweep, touching `migrations/`, or restructuring `web/src`
beyond the ratchet cells named. R16 owns the `types.ts` contract guard's
future; this item only tiers it.

### Slices / steps (in this order; CI green after each)

**Slice R18.0 — Tier header + fix guard messages (comments/strings only)**

1. Replace the file header comment (`guards_test.go:21`) with the tier
   statement, so the next author tiers before adding:

   DRAFT (apply verbatim):

   > // Repo guards. Two tiers: (a) security, privacy and API/TS/SQL
   > // contract tripwires live here in `go test`; (b) repo-hygiene greps
   > // (wording, interpreter, tool versions, CI config strings) live in
   > // scripts/lint-repo.sh (`make lint-repo`). A new guard joins tier (a)
   > // only with a written justification naming the past mistake or the
   > // security/privacy/contract risk it fences; everything else starts
   > // in tier (b). Each guard fails on a real past mistake; see the rule
   > // table in AGENTS.md.

2. Repoint the dead file references: `guards_test.go:470` "Use a role from
   TOKENS.md" → "Use a role token from the @theme block in
   web/src/index.css"; the section comment at `guards_test.go:771` and the
   fix string at `guards_test.go:788` drop "(ENFORCEMENT.md …)" in favor
   of "(DEC-60 to DEC-66; AGENTS.md, UI controls)";
   `web/src/components/ui/Button.tsx:4` "(ENFORCEMENT.md A)" → "(DEC-60)".
   Then run the completion grep in Tests/checks: **every** `*.md` filename
   mentioned in `guards_test.go` failure strings and comments must exist
   (`grep -oE '[A-Za-z0-9_./-]+\.md' guards_test.go | sort -u`, then `ls`
   each). Fix whatever else it surfaces in this slice.

**Slice R18.1 — Move the five hygiene guards to `scripts/lint-repo.sh`**

1. Write `scripts/lint-repo.sh` implementing the five "move to lint"
   replacements exactly as tabled above. Shell only, `set -eu`, no
   Python, must pass `make lint-sh` (shellcheck covers `scripts/*.sh`,
   `Makefile:67` area). Port the **messages**, not just the checks —
   including the wording guard's 'say "recommended" (or "tested", "works
   with")' guidance (`guards_test.go:670`) and the Node guard's
   per-file fixes (`guards_test.go:722`).
2. Wire it before removing anything: new `lint-repo` target next to
   `lint-sh` in the Makefile (`.PHONY` at `Makefile:6`), add `lint-repo`
   to `make lint` (`Makefile:45`), and add it to the CI lint step
   (`.github/workflows/test.yml:40` becomes
   `make lint-sh lint-go lint-repo deadcode`).
   `TestDependabotCoversEveryEcosystem` asserts three CI step strings
   (`guards_test.go:261: "SCREENS_DIR="` is one) — `lint-repo.sh`'s
   dependabot section keeps asserting exactly those strings, so the
   wiring edit cannot quietly drop them.
3. Self-test proving the lint catches what the Go catchers caught
   (they are deleted in step 4, so this replaces them): a
   `scripts/lint-repo.test.sh` is **not** the house style — instead give
   `lint-repo.sh` a `--self-test` mode that stages a temp tree containing
   one violation per check (a `.py` file, an interpreter line, the banned
   word in a doc outside `docs/plan`, a `FROM node:22` Dockerfile against
   a `24` `.nvmrc`, a dependabot file missing an ecosystem, a bare
   `<iframe>` in a fake `web/src` page) and asserts each is reported,
   plus one clean tree that passes. Run `--self-test` in CI in the same
   step. Keep it small; it replaces five catcher tests, not a test suite.
4. Delete the five guards and their catchers/helpers from
   `guards_test.go`: the Python block (`guards_test.go:592`–`657`,
   including `scriptFiles` if nothing else uses it — check first), the
   wording block (`guards_test.go:659`–`706`), the Node block
   (`guards_test.go:708`–`769`), `TestDependabotCoversEveryEcosystem`
   (`guards_test.go:247`), `TestNoVideoEmbed` (`guards_test.go:403`).
   Update the AGENTS.md rule-table rows for the moved checks ("Docs say
   recommended", "No Python", "One Node version") to name
   `scripts/lint-repo.sh` as the enforcer instead of the test names.
5. Prove both directions in the PR: with the branch applied,
   `make lint-repo` passes on the clean tree and fails on each seeded
   violation (`--self-test`); `make test` passes with the guards gone.
   Parity note for the reviewer: the Go versions ran inside `make test`
   on every machine; the lint runs in `make lint` and CI. That is the
   audit's deliberate trade, accepted here.

**Slice R18.2 — Finish `components/Notifications.tsx`; end the ratchet**

1. `web/src/components/ui/Button.tsx`: accept `ref` as a prop and pass it
   to the `<button>` (React 19 style; `Button.tsx:20`). Component test:
   focus via ref lands on the DOM button (add beside existing Button
   tests in `web/src/components/ui/`).
2. Convert Notifications.tsx's seven raw controls (lines in Current
   state) to `Button`/`ButtonLink`, preserving every `aria-label`,
   `aria-expanded`/`aria-controls` (`:367`), `min-h-11` targets, the
   icon-only archive shape (`:117` already shows the intended pattern)
   and the bell's focus return. Convert the Layout.tsx raw control too.
3. Clear the other rules: `danger` cells per the per-line rule above;
   `dateFormatting` cells via `when.ts` (new small exports in
   `lib/when.ts` for the two `lib/` primitives, with their existing unit
   tests moving/updated so coverage follows the code); Profile.tsx `!`
   via a Button/Chip size. Lower `ui-ratchet.json` in the same commits
   (`UPDATE_UI_RATCHET=1 go test -run TestUIRatchets .`) — the ratchet
   only turns one way (`guards_test.go:775`).
4. When `ui-ratchet.json` is all zeros: replace `TestUIRatchets`
   (`guards_test.go:878`) with a zero-tolerance guard reusing `uiRules`
   and `uiRuleCounts` (`guards_test.go:785`, `:819`) — any nonzero count
   in any `uiSourceFiles` file fails with the rule's existing fix text.
   Keep the catcher's cases (`guards_test.go:912`) pointed at the new
   guard. Delete `ui-ratchet.json`, the `UPDATE_UI_RATCHET` branch and
   `uiRatchetViolations`' ratchet-map parameter. Update the AGENTS.md
   ratchet row to describe zero tolerance.
5. UI proof per house rules: Vitest for Notifications behavior stays
   green; run `.cursor/skills/verify-ui` with 390px screenshots of the
   bell panel and `/notifications` (the page is in the a11y/layout page
   list, so `make e2e` covers axe + targets for the converted controls).

**Slice R18.3 — Embed the templates**

1. In `main.go` beside `distFS` (`main.go:309`): embed the templates
   (`//go:embed templates/*.html`). In `setupRouter` replace the
   `SetFuncMap` + `LoadHTMLGlob` pair (`main.go:253`) with a parsed FS
   template carrying the same func map (`split`, `stylesheet`) via
   Gin's `SetHTMLTemplate` — funcs must be registered before parsing;
   `template.Must` so a bad template fails boot, not first request.
2. Delete the working-directory dependency trail: the `COPY` of
   `/templates` and its comment (`Dockerfile:26`, `Dockerfile:30`), and
   the "log the working directory" block's template rationale
   (`main.go:99`) — keep or drop the log line itself only after grepping
   that nothing else resolves paths relative to it. Keep `WORKDIR /` and
   the `/data` volume handling exactly as they are (AGENTS.md Docker
   notes); only the templates stop needing the filesystem.
3. `templates/` source files stay in the repo (the color guard still
   scans them, `guards_test.go:441`). Proof: build the binary, run it
   from an empty directory (no `templates/` beside it), and load `/setup`
   on a fresh DB and `/dev/outbox` in dev; existing setup/outbox handler
   tests pass unchanged.

**Slice R18.4 — Shrink STATUS.md and docs/plan/SUMMARY.md**

Target structure (apply as written; lengths are acceptance caps):

- `STATUS.md` — **≤60 lines**, exactly: title + one-line "claim a
  milestone here before starting it; PLAN.md is the product record, git
  history is the delivery record"; `## In flight` table (Milestone |
  Owner | Branch/PR) holding **only claimed, unfinished** work — empty
  means one row "none claimed"; `## Remaining follow-ups` carrying over
  today's list verbatim (it is the living part; audit each item's "still
  true?" only by checking the file it names, and drop an item only when
  its named code proves it done, with the commit SHA noted in the PR);
  `## Done` = one line linking the merged-PR list on GitHub. Delete the
  reconciled-through header, the per-milestone done table, the "Other
  merged work" table, and the "#163 checkup"/"post-#163 follow-ups"
  evidence sections — all reconstructible from git, none needed to start
  work.
- `docs/plan/SUMMARY.md` — **≤50 lines**, exactly: title; `## Product`
  (keep today's three short paragraphs, `docs/plan/SUMMARY.md:11` area);
  `## Merged today` as one line per phase (M0, M1, M2 rooms, M3
  messaging-to-date) naming capabilities, no PR numbers, no SHAs;
  `## Next, not shipped` as one short table (Phase | Remaining scope)
  reconciled to PLAN 14.2 **as edited by R17** (no M4.7, no M3.10, no
  ChMS row; SMS present); `## Privacy decisions that must survive` kept
  but with the ChMS bullet replaced by R17's outcome if R17 landed
  first; `## Deployment reality` (keep, shortened to today's two
  paragraphs); `## Still to decide` = pointer to PLAN 15.2 only, no list
  copy. Delete both M1.14 publication sections (one line remains in
  Merged today; the record lives in `docs/plan/M1.14-VERIFICATION.md`).
- Both files state their own update rule in one line: "update in the PR
  that merges the work; if this file and git disagree, git wins" —
  which is what stops the next 60-PR stale header.

### Tests / checks

- Per slice: `go build ./...`, `go vet ./...`, `make lint-go`,
  `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`, then
  `make test` (every slice but R18.4), `cd web && npm test` (R18.2),
  `make e2e` (R18.2, R18.3 — setup page is in the e2e seed path),
  `make lint` (R18.1 proves the new target rides along).
- R18.0 grep: every `.md` named in `guards_test.go` exists (step 2).
- R18.1: `scripts/lint-repo.sh --self-test`; seed-one-violation proof per
  check recorded in the PR body.
- R18.2: `go test -run 'TestUIRatchets|TestFormControls' .` during the
  lowering, and after conversion a seeded raw `<button>` in a scratch
  file fails the new guard (catcher covers this — run it).
- R18.3: run-from-empty-directory proof (step 3) recorded in the PR body;
  `docker compose up --build` smoke if the parent wants the image-level
  proof (only a live compose run checks the Dockerfile edit for real).
- R18.4: docs-wording runs through the **new** lint by now
  (`make lint-repo`); `wc -l STATUS.md docs/plan/SUMMARY.md` inside caps.

### Acceptance checklist

- [ ] Tier header comment in place; zero references to TOKENS.md /
      ENFORCEMENT.md remain in `guards_test.go` or `Button.tsx`.
- [ ] `scripts/lint-repo.sh` + `--self-test` green in CI; the five Go
      guards and their catchers deleted; `make lint` includes the new
      target; AGENTS.md rule rows name the script.
- [ ] `components/Notifications.tsx` has zero raw controls; `Button`
      forwards `ref` (tested); all ratchet counts reached zero before
      conversion; `ui-ratchet.json` and the `UPDATE_UI_RATCHET` path are
      deleted; the zero-tolerance guard + catcher pass.
- [ ] Binary serves `/setup` and `/dev/outbox` with no `templates/`
      directory present; Dockerfile no longer copies templates.
- [ ] `STATUS.md` ≤60 lines and `docs/plan/SUMMARY.md` ≤50 lines, in the
      target structures, follow-ups preserved, no stale
      reconciled-through claims.
- [ ] Diff contains no package split, framework change, ESLint/format
      sweep, or migration.

### Risks

- **Lint parity drift**: a shell grep is blunter than the Go checks
  (e.g. the wording check's quoted-key exception). The `--self-test`
  fixtures pin today's behaviors; any behavior the script cannot
  reproduce faithfully is a slice-R18.1 stop condition — report it and
  keep that one guard in Go rather than shipping a weaker check silently.
- **Checks stop running locally** for people who only run `make test`.
  Accepted audit trade; `make lint` and CI both run them, and branch
  protection requires CI.
- **Bell refactor regresses focus/aria behavior** (focus return at
  `Notifications.tsx:330`, outside-click close at `:339`, tab-past close
  at `:359`). The component's existing tests plus the e2e a11y pass are
  the fence; convert the bell **last** within R18.2 so a problem there
  cannot block the other six conversions.
- **Template parse difference**: `LoadHTMLGlob` names templates by base
  file name; `ParseFS` with a directory pattern does the same only if
  the pattern matches how handlers render. Setup/outbox handler tests
  catch a mismatch; the run-from-/tmp proof catches the rest.

### Dependencies

- R17 before R18.4's SUMMARY "Next" table (so it reflects the cuts); if
  R18 goes first, R17's PR updates that table instead — pick one owner.
- R16 (types generation) must not be in flight simultaneously with
  R18.0/R18.1 edits to `guards_test.go` — same file, guaranteed conflict.
  Sequence R18.1 and R16, do not parallelize.
- R18.2 is independent of R18.1 (different files) and may land first.

### Open questions (with defaults)

1. Move `TestTestFilesSitNextToTheirSource` to the lint too, for tier
   purity? **Default: keep in Go** (justification in the tier table).
   Revisit only if the root test package's runtime measurably improves
   without it.
2. Keep `TestWorkflowActionsPinnedAndReadOnly` in Go or adopt a dedicated
   workflow linter? **Default: keep in Go.** A new external tool to check
   three workflow files is more maintenance tax, not less.
3. One `lint-repo.sh` or one script per check? **Default: one script**,
   one target, one CI mention — five tiny scripts is exactly the sprawl
   this item removes.

---

## R19 — Freeze rooms-suggestion and direct-contact expansion; per-request locks replace the global contact gate

- Priority: P2 (audit `docs/AUDIT-2026-11-01.md:248`)
- Effort: M (audit estimate; risk medium, safeguarding-adjacent)
- Status: Approved 2026-11-01. Freeze means **documented
  no-expansion**, not deletion: shipped suggestion behavior, the contact
  lifecycle, room conflicts/booking and the safeguarding audit all stay.
- *Amended Nov 3, 2026 (owner decision during M7.4 R4-0; the audit-era text
  below stays as written and is historical where it conflicts).* The
  `contact_requests` schema freeze is relaxed in exactly one respect: account
  erasure (DEC-144) may scrub only an erased person's identifying text from the
  relevant `contact_requests` rows, through one erasure-only exception to the
  `add_request_identity` trigger, preserving the original request decision,
  purpose, subjects, actors, provenance, consent and every other protection.
  Owner's answer, verbatim: "Allow a narrow exception to scrub only the erased
  person’s identifying text while preserving request decisions and provenance
  (Recommended)". `PLAN.md` DEC-141 carries the amendment (no new DEC). No new
  purpose, field or lifecycle state, no broad `UPDATE`/`DELETE` allowance, and
  the rest of the freeze is unchanged. The trigger and migration shape is
  implemented later in M7.4 R4-2a after independent review; see
  `docs/plan/M7.4-ERASURE-INVENTORY.md` T07 and O-7. R19.2's frozen-surface
  proof ("no `migrations/` or `models/` changes") is about R19.2's own diff
  and is not affected.

### Goal

Two parts. (1) Freeze the two clearest over-builds relative to the group
slice: the room-suggestion rule gains no new cases, signals or ranking
inputs, and the direct-contact machinery gains no new request purposes or
schema, until a first church is live and Grier lifts the freeze in a DEC.
(2) Remove the one global serialization point they introduced: every
contact-affecting transaction in the app queues on a single advisory lock,
so unrelated groups' role changes, registrations and deletions wait on
each other. Replace it with the per-request row locks the code already
half-uses, under one written lock order, and prove with concurrency tests
that each guarantee the gate provides still holds — and that unrelated
work no longer queues.

### Current state (verified by opening each body)

**Room suggestions.** The engine is `internal/rooms/suggest.go` (336
lines): one counting-usage SQL predicate (`CountingUsagePredicate`,
`internal/rooms/suggest.go:98`), tag/type filter, busy-marking over the
next 8 dates (`markBusy`, `internal/rooms/suggest.go:225`; cap
`MaxBusyDates` at `:26`), six-step ranking (`ranksBefore`,
`internal/rooms/suggest.go:285`), top 5 (`MaxSuggestions`, `:23`).
Serving: `api_room_suggestions.go` (123 lines) folds suggestions into
each picker's room list (DEC-110). The spec is PLAN 9.3
(`docs/plan/PLAN.md:958`, five rules plus the 13-case table ending at
`docs/plan/PLAN.md:1001`); the tests mirror it case-for-case:
`TestSuggestionCases` runs cases 1–13 by number plus three per-date
variants (`internal/rooms/suggest_test.go:122`, subtests to `:297`), with
`TestSuggestionRanking` (`:316`), `TestSuggestionsIgnoreWhatIsGone`
(`:356`), `TestSuggestionsAreFast` (`:377`) and `TestSuggestionBusyCheck`
(`:473`) around them (517-line test file), and API coverage in
`api_room_suggestions_test.go` (180 lines). Rule 5 (never leak another
event's title/group/date) is structural: `Suggestion` carries only room
id, counts and reason (`internal/rooms/suggest.go:63`). Conflicts and
booking proper are separate and stay live product: exclusion constraint
and move semantics in PLAN 9.2 (`docs/plan/PLAN.md:952` area).

**Direct contact.** One table, three purposes: `models.ContactRequest`
(`models/models.go:935`; header comment at `:930` — "Dispatch by Purpose
BEFORE constructing contact-only policy") stores `contact`, `group_add`
and `occurrence_add` asks, with captured add options
(`models/models.go:947`); `models.ContactGrant` (`models/models.go:975`)
is unique per successful request and backed by a trigger requiring a
granted request (`migrations/00030_contact_requests.sql:80`). Policy
splits the families by type so an add can never become contact authority
(`internal/policy/contact.go:96`); the simple roster rule is
`policy.CanSeeContact` (`internal/policy/policy.go:390`) — self/admin/
confirmed-guardian, else unhidden adults to their group's leaders or
event's managers, teens never. Reveal is deliberately separate:
`CanRevealContactGrant` (`internal/policy/contact.go:283`) runs only in
the audited endpoint. The lifecycle code is `api_contact.go` (1,099
lines) + `contact_lifecycle.go` (510) + `internal/policy/contact.go`
(308), matching the audit's "~2200 lines" once reads/history are counted.

**The global gate.** `contactLockID` and `lockContacts` /
`lockContactsShared` (`contact_lifecycle.go:19`): the comment is candid —
"one deliberately coarse transaction advisory gate. Every
contact-affecting domain transaction takes it FIRST, before its existing
user/group/occurrence/notification locks." It is taken in
`contactMutation` (`contact_lifecycle.go:49`, the wrapper that runs a
domain mutation then `reconcileContacts` in the same transaction), in
every contact handler via `runContactOperation`
(`api_contact.go:158`; writes take it exclusive, `api_contact.go:149`;
reads shared, `api_contact.go:155`), and directly at ~20 further sites
(grep: `api_occurrence_mutations.go:278`, `api_series_registrations.go:213`,
`api_registrations.go:1037`, `account_security.go:20`,
`api_admin.go:546`, `api_auth.go:434`, `api_guest.go:620`,
`api_invitations.go:342`, `api_teen_conversion.go:89`,
`api_admin_household.go:50`, `api_contact.go:627`, and more). The daily
sweep takes it non-blocking (`sweepContacts`,
`contact_lifecycle.go:410`).

**Per-request row locks already exist underneath the gate:** decide
locks the request `FOR UPDATE` before checking policy
(`api_contact.go:394`), withdraw likewise (`api_contact.go:472`), reveal
locks request then grant (`api_contact.go:554`), reconcile locks its
candidate requests ordered by id (`contact_lifecycle.go:344`) and their
unended grants ordered by id (`contact_lifecycle.go:391`). Policy already
documents the intended finer grain for the one count-then-insert:
"Serialize openCount under the requester's account lock"
(`internal/policy/contact.go:165`).

**The exemplar to copy.** `changeRoleIn` (`api_groups.go:764`, header at
`:762`) shows the house lock pattern: caller-owned transaction, actor and
target user rows locked `NO KEY UPDATE` first (`api_groups.go:776`,
`:789`), then the group row `FOR UPDATE` (just after, in the same
function), then policy re-checks (`GroupHasRoom`, `CanAddToGroup`) and
the write — all facts re-read under the locks. It runs inside
`contactMutation` today (`api_groups.go:753`), so it is also proof the
domain order is users → group → (contact rows last).

**What the gate actually guarantees today** (this is the contract the
replacement must keep):

1. **Open cap.** At most `ContactOpenLimit` = 10 pending requests per
   requester (`internal/policy/contact.go:35`): count-then-insert in
   create (`api_contact.go:287`), proven under contention by
   `TestContactConcurrentOpenCap` (`contact_lifecycle_test.go:161`:
   16 concurrent creates → 10×201, 6×409).
2. **Authority change beats reveal.** A concurrent revoke / account
   disable or delete / role or membership loss commits together with
   ending the grant (`reconcileContacts` inside `contactMutation`,
   `contact_lifecycle.go:68`), so a reveal that loses the race is refused
   and audited: `TestContactAuthorityMutationWinsReveal`
   (`contact_lifecycle_test.go:187`, seven mutations).
3. **One terminal outcome per request** — decision, withdraw, expiry and
   reconciliation cannot double-finish a request or double-create its
   grant (request lock + the grant's unique `request_id`,
   `models/models.go:977`).
4. **Clock consistency**: eligibility facts are loaded after the lock
   with the operation's `now` (`contact_lifecycle.go:128` comment;
   `api_contact.go:164` sets `op.now` after locking).

The existing tests stage these races on the advisory gate itself:
`holdContactGate` / `waitContactWaiters` watch `pg_stat_activity` for
`pg_advisory_xact_lock` waiters (`contact_lifecycle_test.go:22`). Those
helpers are rewritten in slice R19.2 — the scenarios and assertions stay.

### Scope

In:

- Freeze DEC (next free DEC + 2 in this brief's ordering) + one freeze
  paragraph at the end of PLAN 9.3 and one at the end of PLAN 5.11,
  exact draft text below.
- Contact locking rework: remove `lockContacts`/`lockContactsShared` and
  the `contactLockID` gate; serialize on request/grant/user/domain rows
  under the written lock order in slice R19.2. No schema change, no new
  migration, no API or JSON change, no policy-function signature change.
- Concurrency tests proving the four guarantees above plus one new one:
  unrelated contact work (different requester, different group) does not
  queue behind a held lock.

Out:

- Deleting or simplifying away: suggestion Rules 1–5 and cases 1–13,
  any `ContactRequest` purpose, `CanSeeContact`, the reveal endpoint,
  `contactAudit` rows, admin history/CSV, room conflicts/double-booking
  (PLAN 9.2), the daily `contact.sweep` job's existence.
- Splitting `ContactRequest` into per-purpose tables, or folding it into
  anything else. Frozen means the shape stops growing, not that it gets
  a redesign. (If R9's baseline squash folds contact-adjacent provenance
  elsewhere, that is R9's call under its own brief; R19 does not depend
  on it either way.)
- Any new suggestion input (distance, equipment, leader preference
  learning) and any new contact purpose or field — the freeze DEC is the
  text agents cite to refuse them.
- Performance work beyond removing the global queue (R11 owns query
  shapes).

### Slices / steps

**Slice R19.1 — The freeze, in writing (docs only, one PR)**

Files: `docs/plan/PLAN.md` only.

1. Insert the DEC row after R17's DEC (or the then-last row).

   DRAFT (apply verbatim, `DEC-K` = assigned number):

   > | DEC-K | **Room suggestions and direct-contact requests are frozen: no new cases, purposes or schema until a first church is live** (Grier, Nov 1, 2026; audit R19). The room-suggestion rule (9.3) is complete as shipped: Rules 1–5 and cases 1–13 are the whole rule, and it gains no new cases, signals, ranking inputs or `Room`/booking schema for suggestions. The direct-contact machinery (5.11) is likewise complete: `ContactRequest` keeps exactly its three purposes (`contact`, `group_add`, `occurrence_add`), no new purpose, field or lifecycle state is added, and `contact_requests`/`contact_grants` get no schema changes. Bug fixes that keep all 13 suggestion cases and every shipped safeguarding behavior are always in bounds; anything that would add a case, a purpose or a column waits until a first church is live and Grier lifts this freeze in a new DEC. Room conflicts and booking (9.2), `CanSeeContact`, the audited reveal and the admin contact history are not frozen — they are shipped safeguarding and stay maintained. The one structural change allowed inside the freeze is internal: per-request row locks replace the global contact advisory lock (R19), with no API, JSON, policy or schema change. | 5.11, 9.2, 9.3, M2.3, M1.11 |

2. At the end of PLAN 9.3, after the 13-case table
   (`docs/plan/PLAN.md:1001`):

   DRAFT (apply verbatim):

   > **Frozen (DEC-K).** This rule is complete as shipped. Do not add cases to the table above, new ranking inputs, or new suggestion signals; fix bugs against the 13 cases as written. Suggestions stay a shortcut, never a gate (Rule 5). Booking and conflicts (9.2) are separate and stay live.

3. At the end of PLAN 5.11 (`docs/plan/PLAN.md:540` section), as its last
   bullet or paragraph:

   DRAFT (apply verbatim):

   > **Frozen (DEC-K).** The request machinery above is complete as shipped: three purposes, the two-party decision rule, grants, the audited reveal and the admin history. Do not add a purpose, a contact field, or a lifecycle state, and do not change the `contact_requests`/`contact_grants` schema, until a first church is live and a new DEC lifts the freeze. *(Amended Nov 3, 2026, in DEC-141 itself: the one narrow erasure-only exception for scrubbing an erased person's identifying text, with request decisions and provenance preserved. Everything else here stands.)* Every attempt stays audited either way; refusal audit rows commit even when the request does not.

**Slice R19.2 — Per-request locks replace the global gate (code, one PR;
safeguarding-adjacent, review accordingly)**

Files: `contact_lifecycle.go`, `api_contact.go`, every file that calls
`lockContacts`/`contactMutation` today (list in Current state, refresh by
grep at start), `contact_lifecycle_test.go`, `api_contact_test.go`, plus
small comment updates in `internal/policy/contact.go` (no signature
changes). No migrations, no model changes.

*The design, in full, so the implementer does not re-derive it:*

**Lock order (the contract, written into `contact_lifecycle.go`'s header
comment in place of today's gate comment at `:19`):**

   DRAFT comment (apply verbatim):

   > // Contact locking order (R19, replaces the old global advisory gate).
   > // Within one transaction, take locks only in this order:
   > //   1. the requester's user row (NO KEY UPDATE) — create paths only,
   > //      serializes the per-requester open-request count;
   > //   2. domain rows the mutation already locks (user rows in id order,
   > //      then group / event / occurrence rows, as changeRoleIn does);
   > //   3. contact_requests rows FOR UPDATE, ordered by id;
   > //   4. contact_grants rows FOR UPDATE, ordered by id.
   > // Contact-only operations (create, decide, withdraw, revoke, reveal)
   > // take 3–4, then reload every policy fact with plain reads and never
   > // take a domain row lock afterwards. A group/occurrence ask whose
   > // approval completes a domain add pre-locks its target group or
   > // occurrence row (2) before the request row (3), so its order matches
   > // a plain membership/registration mutation; the unlocked peek that
   > // finds the target chooses only what to lock — all facts are
   > // reloaded and re-checked after locking, and a target that changed
   > // underneath fails closed through the usual refusal path.
   > // Domain mutations keep reconcileContacts in the same transaction as
   > // the change: reconcile locks every affected request (3) and grant
   > // (4) before ending anything, so a reveal or decision on the same
   > // request serializes either wholly before or wholly after the
   > // authority change — never in between.

Why each guarantee survives (state this in the PR body; reviewers will
ask):

- **Open cap** — create locks the requester's user row (step 1), then
  counts pending requests and inserts, exactly as
  `internal/policy/contact.go:165` prescribes. Two creates by the same
  requester serialize; different requesters never touch the same row.
  The add-request create path (`api_contact.go:843` area) gets the same
  treatment — the cap is shared across purposes today and stays shared.
- **Authority change vs reveal** — the domain mutation and its
  `reconcileContacts` are one transaction, and reconcile already locks
  precisely the affected requests/grants (`contact_lifecycle.go:344`,
  `:391`), scoped by user/event/group (`contact_lifecycle.go:335`).
  Reveal already locks its request and grant first
  (`api_contact.go:554`) and only then loads facts and re-checks
  `CanRevealContactGrant`. So if the demotion wins, reveal blocks on the
  request row, then re-reads a world where the membership change has
  committed and the grant is ended → refused + audited. If reveal wins,
  the demotion's reconcile blocks until reveal commits, then ends the
  grant — the same two legal outcomes the gate gives today. The one
  thing that must **not** change: fact-loading stays *after* the row
  locks in reveal (`api_contact.go:558` onward) and `op.now` is sampled
  after the first row lock in each operation, preserving guarantee 4.
- **One terminal outcome** — decide/withdraw/reconcile/sweep all finish
  requests only while holding that request's `FOR UPDATE` lock (already
  true at `api_contact.go:394`, `:472`, `contact_lifecycle.go:344`), and
  grant creation stays inside the decision transaction with the unique
  `request_id` as backstop.
- **Reads** — `contactRead`'s shared gate goes away; reads run in a plain
  transaction with no contact locks. A list racing a decision can show a
  request one moment stale, like every other list in the app; the
  decision endpoint re-validates under the row lock, which is where
  correctness lives. Refusal-audit-only reads ("append-only rows", the
  shared gate's stated use at `contact_lifecycle.go:33`) are unaffected:
  audit rows are inserts.

*Steps:*

1. `contact_lifecycle.go`: write the lock-order comment (draft above)
   where the gate comment sits (`:19`); delete `contactLockID`,
   `lockContacts`, `lockContactsShared` (`:27`); in `contactMutation`
   (`:49`) remove only the `lockContacts` call — the
   reconcile-before (event edits, `:58`) and reconcile-after (`:68`)
   structure is unchanged. In `sweepContacts` (`:410`) replace the
   try-advisory-lock with batched candidate locking: select pending /
   grant-open request ids `FOR UPDATE SKIP LOCKED` (bounded batch,
   ordered by id), reconcile, commit; the daily job caller and cadence
   do not change. A skipped row is being decided right now and the next
   sweep — or that decision's own reconcile — gets it.
2. `api_contact.go`: `runContactOperation` (`:158`) drops its lock
   parameter; `contactOperation`/`contactRead` keep their names and call
   sites, so handler bodies do not churn. Inside each handler, ensure
   the row-lock-then-facts shape holds (create: requester user lock
   before the count at `:287` and before the reconcile at `:278`;
   decide/withdraw/reveal already lock first — verify, don't rewrite).
   For add-request decisions, implement the pre-lock of the target group
   / occurrence row before the request lock, per the comment contract.
3. Every direct `lockContacts(tx)` site outside those two files: remove
   the call, one site at a time, after confirming the transaction either
   (a) runs a `reconcileContacts` (directly or via `contactMutation`)
   covering the requests its change affects, or (b) never affected
   contact eligibility and took the gate defensively — say which, per
   site, in the PR body. The refresh list: `api_occurrence_mutations.go`,
   `api_series_registrations.go`, `api_registrations.go`,
   `account_security.go`, `api_admin.go`, `api_auth.go`, `api_guest.go`,
   `api_invitations.go`, `api_teen_conversion.go`,
   `api_admin_household.go`, `api_guardian_email.go`,
   `api_account_requests.go`, `api_teen_consent.go`,
   `api_teen_guardians.go`, `api_departure.go`,
   `api_event_access.go`, `api_events.go`, `api_groups.go`,
   `age_rollover.go`, `jobs_daily.go` (via `contactMutation` /
   `sweepContacts`). Completion grep:
   `grep -rn "lockContacts\|pg_advisory_xact_lock" --include="*.go" .`
   returns nothing mentioning the contact gate (unrelated advisory locks
   — migrations, push keys — stay and are not hits for `lockContacts`).
4. Tests: rewrite `holdContactGate`/`waitContactWaiters`
   (`contact_lifecycle_test.go:22`) into row-lock staging helpers with
   the same ergonomics — e.g. `holdRequestRow(t, app, requestID)` opens
   a tx that takes that `contact_requests` row `FOR UPDATE`, and the
   waiter helper polls `pg_locks`/`pg_stat_activity` for a transaction
   waiting on that row's lock (no sleeps as synchronization, same as
   today). Keep `awaitContactResponse`'s deadlock timeout
   (`contact_lifecycle_test.go:48`) — it is the deadlock tripwire for
   the new order. Then:
   - `TestContactConcurrentOpenCap` unchanged in scenario and assert
     (16 creates, same requester → 10 created, rest 409,
     `contact_lifecycle_test.go:161`), now serialized on the requester
     user row.
   - **New** `TestContactUnrelatedWorkDoesNotQueue` in
     `contact_lifecycle_test.go`: hold requester A's create open (A's
     user-row lock held in a staging tx), prove requester B's create and
     an unrelated group's `changeRole` both complete without waiting for
     A's release. This is the test the whole slice exists for; under the
     old gate it deadlocks until release.
   - `TestContactAuthorityMutationWinsReveal` unchanged scenarios (all
     seven mutations, `contact_lifecycle_test.go:187`), staged so the
     mutation's transaction reaches its reconcile lock before the reveal
     proceeds; assert reveal is refused (not 200, not 500) and the
     refusal audit row exists. Add the reverse-order companion for at
     least `revoke` and `role-loss`: reveal wins and returns the field,
     then the mutation commits and the grant is ended — both orders are
     legal, and the pair of tests says so.
   - **New** decision-vs-withdraw race: subject-approve and
     requester-withdraw race on one request; exactly one terminal
     status results, a grant exists iff the decision won, both audit
     rows present. Place beside the decide tests in
     `api_contact_test.go`.
   - Full suites around it: `make test` (root shards include
     `api_contact_test.go`, `contact_lifecycle_test.go`,
     `api_contact_reads_test.go`, `api_contact_history_test.go`), plus
     repeated runs of the four concurrency tests
     (`go test -run 'TestContactConcurrent|TestContactAuthority|TestContactUnrelated' -count=10 .`)
     to shake out order-dependent flakes before asking review.
5. Review requirement (house rule): this PR touches identity-of-teen
   data paths, so it gets the four-concern separate-agent review before
   merge, with the security reviewer specifically signing the
   guarantee-by-guarantee argument in the PR body. No DEC-31 semantics
   change is being made; if review finds one is needed, stop — the
   freeze DEC forbids smuggling it in here.
6. *Inputs from M7.4 (added Nov 3, 2026 with the erasure inventory, R4-0;
   facts only, no change to this slice's scope).* Account erasure
   (`docs/plan/M7.4-ERASURE-INVENTORY.md` section 9) leans on today's gate in
   ways R19.2 must keep true or replace with tests:
   - `apiAdminDeleteUser` runs inside `contactMutation` (gate first, then the
     target row `FOR UPDATE`) and decides on a count read before the gate
     (`api_admin.go:834,852-853`). M7.4's R4-1 (planned, not yet implemented) adds its own sorted user-row
     acquisition and recount; when the gate goes, those row locks are what keep
     two admin reductions from emptying the admin set.
   - Notification writers and the recipient's `users` row (`internal/notify/notify.go`,
     inventory 9.2.1). *As of M7.4 R4-N (implemented on `autoflow/m74-r4-n-20261103`,
     not yet re-reviewed or merged; the lock behavior is from reading the source and
     PostgreSQL's documentation, with tests written but not yet run at the time of
     writing).* `Send` now de-duplicates its recipients, drops zero and actor IDs,
     and locks the live ones with one bulk statement per 500
     (`SELECT id FROM users WHERE id IN (...) AND deleted_at IS NULL ORDER BY id
     FOR KEY SHARE`, ascending id across batches) before the **merge** or the
     **insert**, in the caller's transaction, and keeps them to commit. A deleted or missing recipient is skipped; a disabled one is not.
     The delivery-repair paths (`reconcileNotifications` pass 1 and pass 2) now
     repair in a transaction that calls `notify.Protect` first: lock the live owners
     in ascending user id, then lock and reload the notices that still exist
     (ascending notice id) and keep only those whose owner is unchanged; the
     delivery rows and jobs are written only for that set (`notify.QueueDelivery` for
     pass 2, one acquisition for both groups). `noticeDevicesOff` now runs its check,
     `Send` and intent in one transaction. A caller that sends one notice per person
     in a loop takes the bulk lock once with `notify.WithRecipients` and passes the
     opaque `Scope` it is handed as `Msg.Scope`; `Send` refuses (`ErrBadScope`) a scope
     that is forged, closed, from another transaction or short of a recipient
     (`notifyOccurrences`, `notifyEventChanged` and `notifyWaitlistCutoff` do so). **M7.4 adds and removes no gate, and adds no gate
     acquisition**; `notify` takes only row locks.
     **Handed to R19.2, not done and not proven by M7.4's helper tests:** when the
     gate goes, `Send`'s recipient `FOR KEY SHARE`, `Protect`'s user-then-notice
     order and the merge order (user, notification, delivery row) must be re-tested
     with the real domain callers (inventory 9.2.2: 17 sites have a plain entry and 25 a
     gated one) under R19.2's replacement order, because the key-share lock conflicts
     with `FOR UPDATE` on a user and with an `email` change (`uni_users_email` is a
     key). M7.4's tests (inventory 9.2.5) run one real plain entry for each of the 17
     sites against a plain erasure in both orders, plus the gated leadership notices and
     the invitee-decline retry against the real admin delete, plus test-owned
     transactions for the repair passes and the event-change and cancellation fan-outs.
     They are today's acceptance for those entries and are **not** the future gate-removal
     proof: the other entries of each site, the later locks inside the ~45 entries and the
     real erasure under R19.2's order were not tested. One hazard found on the way (a person
     who is both an admin with an open room-request task and a leader of the event, in
     `apiConfirmRoomRequest`/`apiRejectRoomRequest`, inventory 9.2.5) was fixed in R4-N by
     sending to the leaders before resolving the admins' tasks (`settleRoomDecision`); the
     other `Resolve`-then-`Send` handlers were not re-traced for it and the decision's
     earlier domain locks versus an erasure's later ones remain R4-1 and R19.2 questions.
     R19.2 must also keep every `gorm.DB` opened with `txguard.Plugin`: the recipient scope
     relies on it to notice a savepoint rollback that releases its locks.
   - Invitation acceptance takes the gate, then the group row, then the inviter
     row (`api_invitations.go:318-360`). M7.4 does not reorder it and R4-N left it
     untouched. R19.2 owns redesigning and testing that order once the gate is gone,
     including the recipient locks `changeRole`'s notices then take (admins and
     group leaders, key share) while the inviter row is held `FOR UPDATE`.
   - The recipient KEY SHARE lock vs `FOR UPDATE` (erasure) or unique-email
     changes is a conflict R19.2's lock order must name. R4-N's `notify` tests show
     the two orders for the email change and the erasure wait for each other without
     a deadlock in isolation.
   - Consent writers (inventory 7.2, F-9). Every append to `consents` goes through
     `setConsent` (`email_unsubscribe.go:133-151`), which locks the owner's `users`
     row `FOR NO KEY UPDATE` by id without testing `deleted_at`; one-click
     unsubscribe, the signed settings link, signed-in settings and the push toggles
     load the user *before* their transaction. M7.4's R4-2d (planned, not yet
     implemented) re-reads the lifecycle state under that lock inside `setConsent`
     and tests it with a real preloaded request. R19.2 keeps that re-check when it
     replaces the gate, since the gate does not protect these routes (they take none).
   - Household self-service writers (inventory 9.6, F-10). Household update, person
     add/update/delete and first-use creation take no gate and today only
     `lockHousehold`. M7.4's R4-3 (planned, not yet implemented) adds a user-row lock
     (`FOR NO KEY UPDATE`) with a lifecycle, session-version and seat re-check before
     `lockHousehold`: the user-then-household order every conversion path already uses.
     R19.2 keeps that order when it replaces the gate; it must not make any household
     writer take a household before a user.

### Tests / checks

- Slice R19.1: docs-wording guard as in R13; no code checks.
- Slice R19.2: `go build ./...`, `go vet ./...`, `make lint-go`,
  `make test`, the `-count=10` concurrency loop above, and
  `go test ./internal/policy/ -run Contact` untouched-green (proves no
  policy semantics moved).
- Negative greps after R19.2:
  `grep -rn "lockContacts" --include="*.go" .` → nothing;
  `grep -rn "contactLockID" .` → nothing.
- Frozen-surface proof for both slices:
  `go test ./internal/rooms/` passes with cases 1–13 unchanged
  (diff shows no edit to `internal/rooms/suggest_test.go` expectations),
  and `git diff --stat` shows no `migrations/`, `models/`, or
  `internal/policy/contact.go` logic changes.

### Acceptance checklist

- [ ] DEC-K row + both freeze paragraphs verbatim; PLAN 9.2 untouched.
- [ ] `lockContacts`, `lockContactsShared`, `contactLockID` deleted; the
      lock-order comment is at the head of `contact_lifecycle.go`.
- [ ] Create serializes the open count on the requester's user row;
      decide/withdraw/reveal/reconcile lock request then grant rows, ids
      ascending, facts reloaded after locking.
- [ ] `TestContactConcurrentOpenCap`,
      `TestContactAuthorityMutationWinsReveal` (+ reverse-order cases),
      the decision-vs-withdraw race and
      `TestContactUnrelatedWorkDoesNotQueue` all pass, `-count=10` clean.
- [ ] `contactMutation` still reconciles inside the domain transaction;
      every former gate site is accounted for in the PR body as (a) or
      (b) per step 3.
- [ ] No schema, API, JSON, purpose-set or policy-signature change;
      suggestions' 13 cases byte-identical in test expectations.
- [ ] Safeguarding audit preserved: refusal rows still commit on refused
      operations (the `contactOperation` contract at
      `api_contact.go:146`).

### Risks

- **Deadlock where the gate hid an order inversion.** The global lock
  made every other order safe. The known inversion candidate is
  add-ask completion (contact decision → `changeRoleIn`), handled by
  pre-locking the domain row; the tests' deadlock timeout
  (`contact_lifecycle_test.go:48`) plus `-count=10` runs are the hunt
  for unknown ones. Any deadlock found after merge is a freeze-legal bug
  fix: align that path to the written order, never reintroduce a
  coarser gate.
- **A gate site classified (b) "defensive" that actually mattered.** The
  per-site accounting in the PR body is the mitigation, and the
  four-concern review re-checks each classification against
  `reconcileContacts`' scoping (`contact_lifecycle.go:335`). When in
  doubt, treat as (a) and make reconcile cover it.
- **Reveal linearizing before a demotion** can return a teen's field
  moments before authority ends. That is already true under the gate
  (first transaction wins wholesale) and matches DEC-31: the grant was
  current at reveal time, and the reveal is audited. The paired
  both-orders tests keep this explicit instead of accidental.
- **Freeze read as "rooms/contact are unloved".** The DEC names bug
  fixes as always in bounds and keeps 9.2/conflicts fully live; the
  freeze is on *expansion*, and says so in its first line.

### Dependencies

- R19.1 should land **before** R19.2 is reviewed, so the freeze text
  authorizes exactly one internal change and reviewers can cite it.
- Independent of R9 (baseline squash): R19.2 touches no schema. If R9
  lands first and renames contact-adjacent provenance, rebase the grep
  list; the lock design is unchanged.
- R11 (query shapes) is separate; do not fold `loadContactFacts`
  batching into this PR.

### Open questions (with defaults)

1. Serialize the open cap on the requester's **user row** or on a
   per-requester advisory key (`pg_advisory_xact_lock(hash(requester))`)?
   **Default: user row.** It matches `changeRoleIn`'s user-first order
   (`api_groups.go:776`) and the policy comment's own words
   (`internal/policy/contact.go:165`); advisory keys would be a second
   locking idiom living only here.
2. Should reads keep a shared gate for perfect snapshot consistency?
   **Default: no.** Decision endpoints re-validate under row locks;
   list staleness of one moment matches the rest of the app and buys
   back all read/write parallelism the shared gate still blocks.
3. Does the freeze need an end date as well as the first-church trigger?
   **Default: trigger only.** A date would invite deadline-driven scope
   exactly when the first church needs stability; the DEC lift is the
   deliberate act either way.
