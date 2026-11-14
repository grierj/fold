# Fold milestones: Phase 7

### 14.10 Phase 7: First-church hardening (Audit 2026-11, DEC-145)

Approved Nov 1, 2026. Every milestone below has a coding-agent brief in
[`briefs/audit-2026-11/`](../briefs/audit-2026-11/00-index.md). Slices, files, API shapes, tests
and acceptance checklists live there; this section is the list and the order. Status is in
[STATUS.md](../../../STATUS.md), and the old status and receipt text is in
[`provenance/delivered-phase-7.md`](../provenance/delivered-phase-7.md). Work ships one reviewable
PR per slice (14.1).

Order: M7.1 first; then M7.2 and M7.5 with the M5.1 image work; M7.7 (the baseline squash) before
any brief that adds schema; then the rest. No new surface beyond these briefs and existing
milestones until a first church is live (DEC-145).

**M7.1 Setup takeover: verify and close (SEC1)**
- Done (#241): first-run setup needs the setup token, and the first admin is created in one locked
  transaction ([issue #240](https://github.com/grierj/fold/issues/240) closed).
- Brief: [`SEC1-R5-R12-security.md`](../briefs/audit-2026-11/SEC1-R5-R12-security.md) (SEC1).

**M7.2 Privacy and session hardening bundle (R5)**
- Done (#243–#250), one slice per leak: people search no longer reveals rosters; logout ends every
  session and sign-in rotates the CSRF token; uploads are re-encoded, which drops EXIF and GPS
  data; room feeds show "Reserved" for dates that aren't public; production refuses a `/0` proxy
  range, a plain-http `SITE_URL` and insecure cookies; invite-a-friend answers the same whether or
  not the person has an account; the HTTP server has timeouts; the password minimum counts
  characters, not bytes.
- **Planned (M4.2b):** the church logo upload goes through the same re-encode.
- Brief: [`SEC1-R5-R12-security.md`](../briefs/audit-2026-11/SEC1-R5-R12-security.md) (R5).

**M7.3 Secret rotation and expiring email links (R12)**
- Not started. Scope: dual-key verification for unsubscribe tokens only, scoped expiring
  email-settings tokens, and a `SESSION_SECRET` rotation runbook. Sessions never get dual-key
  acceptance. Today unsubscribe tokens never expire, and changing `SESSION_SECRET` ends them along
  with every session (M3.3a).
- Brief: [`SEC1-R5-R12-security.md`](../briefs/audit-2026-11/SEC1-R5-R12-security.md) (R12).

**M7.4 Account erasure (R4, DEC-144)**
- Scope: an independently reviewed per-table inventory first, then anonymise or delete so that
  deleting an account erases the person, with a backups note in the admin screen. Opt-out and
  suppression records keep a hashed address (10.1); plaintext suppression addresses are a
  disclosed temporary stage, not approval.
- Delivered so far (how it works: [`docs/agents/erasure.md`](../../agents/erasure.md)):
  - The inventory, [`M7.4-ERASURE-INVENTORY.md`](../M7.4-ERASURE-INVENTORY.md), written over the
    37 tables of the M7.7 baseline. Migrations `00005` and `00006` have since added
    `email_address_optouts` and `admin_operation_attempts`.
  - One erasure pipeline, `eraseAccountIn`, run by the admin delete and by the daily tombstone
    sweep (`erasure.tombstone_sweep`, `erasure.tombstone_page`, `erasure.tombstone`), which
    reaches accounts deleted before the pipeline existed.
  - Deleting an account scrubs the user row to a tombstone and deletes the account's tokens,
    calendar link, push devices and owned throttle keys. The `avatar.cleanup` job removes the
    photo file.
  - It anonymises or deletes the account's dependent records: account requests, invitations,
    guest signups, development-outbox mail, scheduled announcements, announcement recipient rows,
    undo receipts, delivery rows and registration free text. It also blanks consent evidence (IP,
    user agent, evidence text).
  - Contact-request text: the childcare ages note is cleared, and each `reason` is kept or
    blanked by the admin's choice in a review step inside the delete panel (owner decision 6).
  - Households: the account's seat is removed, person rows for the same individual are
    anonymised, their guardian links are revoked, and a household's name and address are scrubbed
    only when nobody else is left in it. Shared households are kept and counted.
  - Audit rows: attributable IPs and subject metadata are scrubbed, and the delete answers with
    counts of what it kept (`householdsKept`, `activityEntriesKept`).
  - Network addresses in the activity log (#305, #304; PR-F): deleting also clears the `ip` of the
    two household-conversion actions in an identified group when the erased person may have caused
    the conversion (owner decision 5), and of rows whose action no current writer uses when the
    erased person is the actor (S5, owner decision 12). The daily tombstone sweep runs this once
    more for earlier deletions (version 2). The rest may stay by the owner's statements (S2 and S3,
    owner decisions 9 and 10; DEC-148, an exception to DEC-144's wording), and the C3 table in
    inventory 6.5.2 maps every registered action to one arm.
  - Deleting the only host of a church-wide event needs a replacement host the admin picks. Notice
    fan-outs skip erased recipients.
  - Suppressions store an address hash beside the plaintext, the expand step (`00002`). An
    erased person's optional email choices survive as address-hash records (`00005`, owner
    decision 8).
  - Deletion outcomes: attempt ids with a settle route, the restore barrier, the actor seal and
    the delete panel's outcome states. The panel tells the admin what a delete cannot reach
    (backups until they expire, copies already sent, some activity-log IP addresses).
  - Deleted accounts' reasons (owner decision 7, #383): Admin → Users always links "Deleted accounts
    with reasons to review", with a "Needs review" badge when a probe finds work. The page lists
    each account as "Deleted account · ref N", never a former name. The admin picks Keep or Blank
    for every reason and saves up to 100 at a time under a `legacy_reason_save` attempt id. There is
    no default and no bulk choice.
  - The T07B aggregate (1a, 1b, 1c, 2a, 2b, 2c, 3a and 3b; not T07B-6) is on master: 1a to 2c in
    #299 (`9e8bddb4`), 3a and 3b in #383 (`d93d0825`). That is the T07B half of C4's merge
    condition; R4-T07A (#282) is the other. The inventory's section 15.12 holds the evidence and
    its limits. It does not sign C4 or M7.4: #311's audit does that.
  - Operator recovery: `deploy/digitalocean/restore.sh` and `compose.recovery.yml` restore a
    nightly dump into a private recovery instance. Scripted provision, upgrade and rollback refuse
    a lower recovery level. The tree is at recovery level 2 (`deploy/recovery-policy`).
  - A CI gate refuses a contract migration whose expand step is not in an earlier tagged release
    (`scripts/contract-gate.sh`).
- Done means conditions C1–C5 hold on master ([#311](https://github.com/grierj/fold/issues/311)).
  The personal-IP gate (G-9) and the owner's Nov 3 answer on conversion IPs are recorded in
  inventory section 6.5.2 and owner decision 5; PR-F adds owner decisions 9 to 12 and DEC-148.
  G-9 and C3 close only when the checklist at the end of 6.5.2 is ticked by PR-F's final-head
  review, and #311 audits C3 on master. Nothing here claims either.
- Open work:
  - [#301](https://github.com/grierj/fold/issues/301) and
    [#302](https://github.com/grierj/fold/issues/302) the one-time review list for reasons of
    accounts deleted before the review step existed (owner decision 7): the API and page are built
    in #383. Still unmeasured or unchecked: the QC4/5 scale measurements ([#292](https://github.com/grierj/fold/issues/292)),
    and the real-phone and real-restore hand checks in `docs/FIRST-CHURCH-CHECKS.md`.
  - [#303](https://github.com/grierj/fold/issues/303) and
    [#305](https://github.com/grierj/fold/issues/305) design, review and build conversion-IP
    minimisation (R4-4b): built in PR-F with S5 and sweep version 2. Built is not closed: G-9 stays
    open until the 6.5.2 checklist is ticked.
  - [#304](https://github.com/grierj/fold/issues/304) the owner's disposition for audit IP
    residue no retained key reaches (G-9, Q3): recorded in owner decisions 9 to 12 and DEC-148,
    carried by PR-F with the C3 table. It closes with that checklist, not before.
  - [#306](https://github.com/grierj/fold/issues/306) contract suppressions to hash-only, after a
    tagged release that holds `00002` (R4-SC).
  - [#307](https://github.com/grierj/fold/issues/307) and
    [#308](https://github.com/grierj/fold/issues/308) the final restore procedure (owner question
    Q5), delete-dialog disclosure and restore docs.
  - [#309](https://github.com/grierj/fold/issues/309) an end-to-end sentinel proof that erasure
    leaves only the named residue.
  - [#310](https://github.com/grierj/fold/issues/310) exact-head recovery drills and real-droplet
    checks. No release at `recoveryPolicy >= 1` is published before they pass.
  - [#311](https://github.com/grierj/fold/issues/311) milestone sign-off against C1–C5.
  - Follow-ups: [#312](https://github.com/grierj/fold/issues/312) (deletion e2e helper),
    [#313](https://github.com/grierj/fold/issues/313) (recovery level docs), and the
    reason-review test follow-ups [#292](https://github.com/grierj/fold/issues/292)–[#298](https://github.com/grierj/fold/issues/298).
- Brief: [`R2-R3-R4-people.md`](../briefs/audit-2026-11/R2-R3-R4-people.md) (R4).

**M7.5 Notification delivery outbox (R6)**
- Done (#253–#256): `notify.Send` writes a queued email delivery row and a `notify.deliver` job in
  the notice's own transaction, and the job sends with retries, backoff and a dead state. An
  hourly `notify.reconcile` sweep re-queues orphaned deliveries. A dead job raises one bell alert
  per admin (`system.job_dead`), and Admin → System shows delivery counts. Password reset and the
  other account and guest mail run as id-only jobs; the email-change notice and the guest
  confirmation stay synchronous on purpose (`docs/agents/mail.md`).
- Left for later: rechecking visibility when stored notice email is sent, and whether dismissing a
  dead job also recalls queued pushes, are owner questions. A `notifications(created_at)` index
  waits on M7.6's EXPLAIN gate.
- Brief: [`R6-R11-reliability-perf.md`](../briefs/audit-2026-11/R6-R11-reliability-perf.md) (R6).

**M7.6 Query ceilings and pool cap (R11)**
- Not started. Scope: incremental occurrence `Extend` (DEC-91 preserved), batched reminder
  readers, a jobs lease index, SQL fast paths only where visibility semantics are identical, and
  an explicit database pool cap. Acceptance needs the seeded EXPLAIN proof in the brief; today's
  numbers are estimates.
- Brief: [`R6-R11-reliability-perf.md`](../briefs/audit-2026-11/R6-R11-reliability-perf.md) (R11).

**M7.7 Baseline squash (R9, DEC-138)**
- Done (#258, #264 to #267): one generated baseline, `00001_baseline.sql` with 37 tables,
  replaced the old migration chain. Calendar dates use real `date` columns, exact-date provenance
  lives on `registrations`, and the additive-first rule replaced expand/contract (14.1). The
  close-out is in the brief, R9 section 9.
- Brief: [`R9-baseline-squash.md`](../briefs/audit-2026-11/R9-baseline-squash.md).

**M7.8 People and own-data export (R3 export half)**
- Not started. Scope: an admin people CSV export that round-trips the M4.5 import columns, and a
  person's own-data export, both audited. TOTP itself is M4.9.
- Brief: [`R2-R3-R4-people.md`](../briefs/audit-2026-11/R2-R3-R4-people.md) (R3).

**M7.9 Frontend speed and discoverability (R15)**
- Not started. Scope: lazy group and event detail pages with a tighter bundle budget, a small
  query cache in `web/src/api/core.ts` that dies on logout, shared directory and calendar filters,
  `ConfirmDialog` in place of every `window.confirm`, Messages in the phone and tablet nav, and a
  directory-first signed-out Home.
- Brief: [`R15-R16-frontend.md`](../briefs/audit-2026-11/R15-R16-frontend.md) (R15).

**M7.10 Generated web types (R16)**
- Not started. Scope: a Go generator emits `web/src/types.ts`, CI checks the diff, and the
  contract guard (`TestWebTypesMatchGoJSON`) shrinks to auto-discovery.
- Brief: [`R15-R16-frontend.md`](../briefs/audit-2026-11/R15-R16-frontend.md) (R16).

**M7.11 Contact locks inside the freeze (R19, DEC-141)**
- Not started. Re-scope before starting: R19 was written before M7.4, and account erasure now
  depends on the contact gate (`fencedContactMutation`, the restore ticket's turn and
  `eraseTombstoneOnce` run under it; `docs/agents/erasure.md`). The re-scoped milestone keeps
  that gate for erasure and restore and moves only the other contact writers to per-request row
  locks, with concurrency tests; no API, schema or policy change.
- Brief: [`R13-R17-R18-R19-governance.md`](../briefs/audit-2026-11/R13-R17-R18-R19-governance.md) (R19).

**M7.12 Cut execution and maintenance tax (R17 code half, R18)**
- Scope: delete the dead Go SMS counter (TS stays the one live counter), tier the guard tests
  without weakening security, privacy or contract guards, finish the UI ratchet, `go:embed` the
  templates, and shrink STATUS and SUMMARY. The wording half of R17 landed with DEC-140.
- In progress. Done: the Go SMS counter is gone, the guards are split by concern into
  `guards_*_test.go`, the templates are embedded, `STATUS.md` is one short table, and `SUMMARY.md`
  is in `provenance/`. Remaining: the guard tiers (the brief's tier header and
  `scripts/lint-repo.sh`) and the UI ratchet down to zero (`ui-ratchet.json` still has counts).
- Brief: [`R13-R17-R18-R19-governance.md`](../briefs/audit-2026-11/R13-R17-R18-R19-governance.md) (R17.1, R18).
