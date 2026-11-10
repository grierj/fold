# Documentation reconciliation — October 25, 2026

## Basis and limits

Reviewed tracked root documentation, `docs/`, planning/research documents and
`.cursor/skills/` verification instructions against decisions through DEC-83,
merged history through `1a3f077`, and selected implementation/tests read-only.
This is a documentation-only review, not fresh feature acceptance, vendor
research, a security audit or a production-deployment check. License notices and
the generated-bundle README are not product guidance and remain unchanged.

Precedence: the newest applicable owner decision wins. Preserve stable DEC/C/M
identifiers so existing references still work; replace superseded instructions
rather than renumbering decisions. A merged implementation is evidence of
shipped behavior, not permission to silently reverse an owner decision.

## Reconciled

| Conflict | Resolution |
|---|---|
| PLAN described SQLite, username login, old models/tooling and missing tests as current | Replaced the skeleton snapshot with PostgreSQL/email login/events and current tooling; distinguished target architecture from installed features |
| PLAN/STATUS left M1.8/M1.9 unchecked, M1.11 pending merge and M1.13 unclaimed | Synced merged scope: M1.8 #102/#105/#109; M1.9 through #111; M1.10 #114–#118; M1.11 #122; M1.13 #123 |
| M1.14 mixed delivered prerequisites with future work | Marked only the exact-date invitation prerequisite delivered; kept series answers, calendar styling and join/leave/window work deferred |
| DEC-75 excluded hidden teens from the picker | DEC-78 wins: active teens and contact-hidden adults are searchable; teen adds require subject + admin approval; adds never grant contact access |
| API described derived notices, string IDs, local dismissal and five-minute refresh | Replaced with stored-row APIs, numeric IDs, read/archive/task state, pagination and 60-second visible-tab polling |
| Older childcare, conversion and age assumptions | Consolidated DEC-79–DEC-83: self-confirmation restriction, childcare-aware promotion, church-local age evidence, approval-first conversion and preserved staged access |
| Video rule claimed going-only in every channel | DEC-70's personal-ICS exception is explicit; API/email and public/group feeds keep their stricter rules |
| Room suggestion example widened usages just because a group widened | DEC-48 and M1.13 win: each usage uses its occurrence's effective visibility |
| ChMS research proposed write-back and provider sink APIs | Removed write-back design; DEC-40 is one-way ChMS → Fold, future M6.15; ChurchTrac sample import remains M6.11 |
| Notification research had unresolved questions and obsolete styling | Consolidated decided UX, current controls and explicit future preferences/digest scope; retained dated external sources |
| Verification recipes expected a native time-zone select | Updated to the shared searchable combobox/listbox; setup remains the exception |
| Summary said no decisions were open and blurred future setup/SMS/TOTP with shipped behavior | Rebuilt summary around merged scope, future phases and the open items below |

## Unresolved product questions

1. **Public-event discovery (DEC-76).** How members find public events outside
   their own groups remains open. M1.14's own-scope rules do not settle it.
2. **People visibility and terminology (DEC-78).** Whether people may hide
   completely, and whether to rename “private” groups. Current rules stay:
   contact privacy does not hide names; private groups are visible to signed-in
   members. Do not invent an opt-out or rename stored visibility values.
3. **ChMS field locking (DEC-40/M6.15).** The direction and broad field owners
   are decided; exact local controls to lock and conflict handling need design
   before implementation. No adapter or write-back work is authorized by the
   research note.

## Implementation/documentation discrepancies needing follow-up

These were **not fixed in code or configuration** by this pass.

### Mail configuration in Compose — resolved (#146)

The app service now explicitly passes `FOLD_SMTP_URL` and `FOLD_EMAIL_FROM`
from `.env`. Recreate the app after editing them and verify delivery. Mail
provider setup and a successful real delivery are still operator checks.

### Seat-only pruning and waitlist promotion — resolved (#147, DEC-84)

Grier authorized implementation on Oct 28, 2026. Removing a going registration
now retries promotion under the occurrence lock. A waitlisted party moves up
only when its attendance pool and childcare have room, and never automatically
inside the two-hour cutoff. Removal while still at or over capacity promotes
nobody.

### Future consumers and release promises — not completed milestones

M1.13 covers existing date discovery, calendar, ICS and visibility-changing
paths. Dedicated event search, scheduled reminders and room suggestions do not
yet exist; M2.3/M3.6/M3.8 must test effective date visibility and exact-date
access when built. M1.11's existing-surface acceptance does not include a future
messaging recipient composer. The full relay is also future work: contact
privacy today is not a claim that leaders can already send arbitrary messages
through Fold.

Current deployments build locally, use the first-run server page and may use
the DigitalOcean host-cron backup helper. Published release images, encrypted
provider settings, setup CLI/SPA, private assets/S3, admin TOTP, a portable
backup container and pre-migration backup refusal are still future milestones.
The live expand/contract rule also needs its stated health/build and migration
safeguards before that workflow can be claimed implemented.

## Removed and retained records

Removed completed M1.5, M1.8 and M1.9 briefs, M1.9's judgment-call ledger and the
pre-foundation REVIEW. Their still-applicable decisions are in PLAN, the API
contract and member guidance; superseded discussion remains in git history.
M1.10 and M1.11 briefs were renamed to **VERIFICATION** records because they
contain useful source/test/reviewer evidence. M1.13's record now notes its merge.
These records describe historical runs; `/tmp` artifacts may no longer exist,
and their presence is not fresh review approval.

## Validation of this cleanup

- `git diff --check`: passed.
- `go test . -run 'Test(DocsSayRecommendedNotSupported|NodeVersionComesFromNvmrc)$' -count=1`: passed; these are documentation guards, not the feature suite.
- Checked all 35 remaining Markdown files for missing relative link targets and
  obsolete brief references: none found. External URLs and heading anchors were
  not revalidated.
- All 25 changed/new/deleted paths are Markdown. No code, migration, test,
  workflow or deployment configuration changed.
- No browser, full database suite or production deployment was run for this
  documentation-only cleanup.
