# PR #225: review and CI follow-up

## Review fixes

- **#226 — heading order:** single-date cards use h2, with attendance, household
  RSVP and management sections below them. Series dates keep h3 under Upcoming
  dates. Full-page Alerts buckets use h2; bell buckets remain h3 under its h2.
  Explicit axe `heading-order` coverage also found and corrected skipped levels
  in group About and room-building headings. WCAG-tagged scans remain intact:
  heading order runs separately because `withRules` replaces `withTags`.
- **#227 — phone targets:** `.text-link` is inline-flex with a 44px minimum in
  both dimensions; the signed-out Calendar login uses it. `.btn-link` also has
  a 44px minimum width. At 390px, Request an account measures **137×44**, Join
  and Calendar login **44×44**, and Share **44×44** across public, member,
  nonmember, leader and admin views.
- **#228 — Going and status placement:** Going uses `bg-primary-subtle`, a solid
  primary accent, readable text, and its explicit label/check. Agenda answer
  labels always stack below time; all five states measure a 4px gap with the
  same left edge at every tested width. Week chips and Home's shared agenda
  receive the quiet treatment too. PLAN DEC-71 and its references record
  Grier's approved amendment; scope, permissions and ICS semantics are unchanged.
- **#229 — Archive affordance:** shared secondary Button, an outlined **44×44**
  target and a larger, heavier ×. Probe points near all four edges hit the
  archive control, not its neighboring link. A near-edge click archives without
  navigating; Undo restores the row. Before, archived and restored states are
  captured at every width. The accessible name and offline behavior remain.

## Browser test repairs

The original failed GitHub run was **41365386379**. Its `test` and `provision`
checks passed; `web` reported eleven failing browser tests:

- Account approvals and three series-RSVP cases relied on deleted CSS card
  classes. Tests now use named account-request groups and occurrence articles.
  The same approval locator is corrected in the teen-consent and optional
  household-conversion scenarios.
- Household removal and room-feed management moved into More menus. Tests use
  those controls and still check removal/restoration, ICS invalidation and focus.
- The church-event test assumed every date appeared on Home. It now creates
  five earlier dates itself, verifies the four-date preview, then reaches the
  new date through See all church events and the church-only calendar filter.
- Three exact-date cases asserted attendance mode and guest count as one line
  with a separator. They now assert both displayed values individually; the
  authority, retained-date, decline/block/removal and RSVP checks remain.

A complete unsharded run additionally exposed five tests that assumed an item
was on a first page or that every date was expanded. They now use search,
Show more or Details and RSVP, while keeping their timezone, role-action,
logout/cache, tag-OR and pending-teen assertions. The logout test also follows
Back to the filtered private-group listing after the session ends.

## Verification

- **140 Vitest files / 1,414 tests passed.** Added coverage for context-specific
  headings, all five stacked answer states, quiet Going in both calendar
  components, and the visible Archive control/action.
- TypeScript, Knip, SPA and Go builds, Go vet/staticcheck, selected Go source
  guards, token contrast, script syntax and diff checks passed.
- Eager bundle: **175.7KB gzip / 180KB budget**.
- **69 UI checks passed:** 27 verify-ui baseline checks plus 42 targeted checks
  in `.hallmark/verify-pr225.mjs`, at **390/768/1280px**. No recorded overflow,
  clipped-control, under-24px, moderate/serious/critical WCAG or targeted
  heading-order failures. The affected 390px screenshots were inspected.
- The complete unsharded Playwright rerun finished with **111 passed, 7
  conditional skips and one tag-test failure**. The remaining failure was a
  test synchronization race: after Clear, it could read the old two-group count
  before the unfiltered response arrived. The final test waits for that response
  and each appended count. Its isolated rerun, with **30 extra public groups**
  forcing multiple batches, passed. Thus all **112 non-skipped scenarios** were
  verified, with the final tag case rerun separately rather than claiming a
  clean full local run after that last test-only correction.
- The seven skips were five dev-outbox-dependent email/invitation/conversion
  cases and two opt-in screenshot captures. They were not exercised by this
  production-mode browser run; the separate 69 UI captures are listed above.

Final UI evidence is retained locally at
`/tmp/fold-verify-proof/20261102T013125Z-2019772/{ui,pr225}/`.
`pr225/results.json` includes measured targets, status placement and archive
hit-testing. Full-suite evidence is under `20261102T014121Z-2104669/`, and the
final populated-directory rerun under `20261102T015059Z-2186979/`, in the same
proof root. All verification processes were stopped and their isolated databases
removed; proof files remain. Screenshots are local proof, not claimed as PR attachments.

The earlier exploratory full-suite launch used `VERIFY_URL`, which makes some
specs choose the verify-ui admin instead of the standard e2e seed's admin. That
launch was stopped, not counted as a passing run. The corrected temporary
Playwright config imports the repository config, points at a fresh isolated
verify-fold server, disables its shared `webServer`, and uses
`ISOLATED_E2E_URL` instead. No shared `fold` or e2e database was driven.

No backend, database schema, deployment or permission-policy change is part of
this follow-up. The full Go database suite was not rerun for this UI/test-only
change; prior branch evidence and GitHub's passing Go check remain historical.
These checks do not establish production load or deployment behavior.

## Reproduce the targeted UI proof

```bash
mise exec node@24 -- .cursor/skills/verify-fold/verify-fold up
.cursor/skills/verify-fold/verify-fold doctor <run-id>
eval "$(.cursor/skills/verify-fold/verify-fold env <run-id>)"
UI_PAGES=login,join,home,my-groups,group-member,event,notifications,group-leader,admin \
  mise exec node@24 -- node .cursor/skills/verify-ui/drive-ui.mjs
mise exec node@24 -- node .hallmark/verify-pr225.mjs
.cursor/skills/verify-fold/verify-fold down <run-id>
```

The targeted script seeds synthetic public dates with ordinary authenticated
APIs, including a real waitlist. Role changes produce stored alert fixtures and
restore the member role before proof. It never changes production data.
