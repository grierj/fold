# Busy-church lists: implementation and proof

Implements the recommendations in `busy-church.md`, following the user's
“Sounds good, please implement.” The earlier document is the historical
experiment against `5c0f919`; its recommendations are no longer outstanding.

## Delivered

- **Visitor Home:** four church dates, a count naming the 90-day source window,
  and See all church events. The continuation starts with the first preview
  date and covers the last, including ongoing or distant dates. The personal
  agenda still follows DEC-94: Just mine, 60 days, at most four eligible dates.
- **Calendar:** 25-event agenda pages, a From date field, persistent filters,
  and URL-backed page/window/selected day. The church-wide filter is explicit.
  Phone Week shows the chosen day, with counts in its day picker; phone Month
  names the event count on busy dates rather than relying on three dots.
- **Group and series schedules:** six or fewer rows retain direct responses.
  Longer schedules have search, 12-row pages, date/place/status summaries and
  an expandable response area. Group summaries remain one per event/series,
  not one per generated date. Group search says when it only covers loaded
  dates; the existing Load more dates cursor follows the results. Counts do
  not present a loaded subset as a whole-calendar total.
- **People:** name search, counts and 25-row pages; member role and attendee
  status filters. Members sort by name; attendance retains sign-up order and
  full-roster waitlist positions. Add controls precede the results. Removal is
  a secondary menu action using the original confirmation and API handler.
- **Other management lists:** My groups uses URL-backed search and 12-row
  pages; admin groups/users use 25-row pages after existing search/filters.
  Rooms use building groups, search, 25-row pages, visible Edit and secondary
  menu actions. Active admin tabs reveal themselves after count/layout changes.
- **Messages:** 25-message server pages, subject search, status selection,
  date grouping, and Older/Newest navigation. There is now a real path past
  the former 200-message history cap. Counts say “on this page,” not a false
  global total. No message was sent or scheduled by the proof.

The shared `usePage`, `PageCount`, `Pagination`, `PeopleList` and `CompactDates`
components share presentation, not authorization. No new dependency, schema,
route, deployment service, or permission policy was introduced. Existing RSVP,
capacity, household/guardian/contact and per-date override handlers remain.
Exact case-insensitive **OR** directory tag semantics remain unchanged.

## Interaction safeguards

- Page changes announce counts and focus an anchor. The agenda measures its
  sticky toolbar so that anchor stays below both toolbar and app header.
- Query changes reset presentation pages. URL state survives Back where used.
  Message cursors belong to the resolved query key; a debouncing search cannot
  combine its query with the previous page's cursor.
- Compact date forms retain a stable keyed hierarchy across expansion, search,
  pages, and the small/compact threshold. Mounted drafts are not discarded.
- People rows remain mounted but `hidden` outside the visible result page, so
  real direct-contact request drafts and pending submission outcomes survive
  browsing. Hidden rows are excluded from keyboard and accessibility trees.
  Rows no longer supplied by the authorized caller are removed. This is
  intentionally bounded **presentation**, not DOM virtualization.
- Admin/room filtering and pagination pause while tracked editors are open.
  Contact details, identity distinctions and waitlist calculations remain the
  callers' responsibility, not the shared list component's.
- Confirmed rooms use `placeLine` in compact summaries and search. Selected
  segmented controls no longer float over the sticky shell. Numeric month
  counts have the same readable wording in their accessible names.

## Additive API changes

`GET /api/v1/calendar?church=1` narrows candidates to church-wide events before
visibility processing and the 500-date cap. Existing scope and visibility
checks still decide every result.

`GET /api/v1/messages` accepts `limit` (1–200, default still 200), `cursor`,
subject `search`, and a validated `status`. The response optionally includes
`nextCursor`; its ordering is `(updated_at DESC, id DESC)`. Category and author
restrictions apply on every page, independently of the cursor. Equal timestamps,
insertion between pages, more than 200 messages, literal search wildcards,
invalid inputs and cross-author/category access have API regression coverage.
The cursor is not a frozen snapshot: editing an older message moves it to the
newest part of the history, as expected for updated-time ordering.

## Measured comparison

Same synthetic fixture and 390×900 viewport as the original experiment: 100
active adults, 50 public groups, 149 event definitions/191 dates, 25 church dates,
a 40-date series, 60 members, 49 attendees, 25 personal memberships, 30 notices,
40 unsent drafts and 18 rooms. Heights are whole-page pixels, not performance
benchmarks. Each new figure is the initial result page unless noted.

| Surface | Before | After |
| --- | ---: | ---: |
| Visitor Home | 6,103 | 3,206 |
| My groups | 3,945 | 2,279 |
| Group events | 7,126 | 3,008 |
| Group events after loading all dates | 11,318 | 2,820 |
| 40-date series | 10,019 | 2,450 |
| 60-member roster | 7,940 | 3,968 |
| 49-person attendee manager | 9,806 | 4,563 |
| Calendar agenda | 22,943 | 3,595 |
| Phone Week | 10,942 | 2,386 |
| Admin users | 14,790 | 4,252 |
| Admin groups | 8,663 | 4,688 |
| Messages | 5,715 | 3,964 |
| Rooms | 5,323 | 4,351 |

Phone Month grows from 2,561 to 2,623px because busy dates now show numeric
counts. Signed-in Home and Notifications remain the same height. The group
page gets shorter after all dates are fetched because loaded-only guidance
and continuation controls disappear; its 32 event summaries remain paged.

The tests exercise all 40 series dates, all three group-date fetches, roster
next/search states, message history (25→15), filters and empty results, room
building/search/menus, church-event continuation, and directory Back/focus.
Server-side message tests separately reach all 205 older drafts. The browser
fixture itself has 40 drafts and does not establish >200-row UI behavior.

## Verification and review

- **140 Vitest files / 1,404 tests** pass, including draft retention, cursor
  races, pagination/filtering and distant church-event continuation.
- **Full `go test ./...`** passes against isolated Postgres. Root package took
  about 68 seconds locally; this is a test runtime, not an app benchmark.
- **172 final browser screen/state checks**, all passing at 390/768/1280:
  27 small-fixture baseline, 109 busy-list, 36 directory. Gates include overflow,
  clipped controls, sub-24px targets and moderate/serious/critical axe findings.
  Additional assertions check sticky-toolbar focus clearance and pagination.
- TypeScript, Knip, build, Go vet/staticcheck, source guards and contrast pass.
  Eager JavaScript is **175.7KB gzip**, within the **180KB** budget.
- Rendered Hallmark: **0 FAIL, 0 WARN, 9 WAIVED, 22 ANSWERED, 6 DISMISSED** over
  144 production files and five routes. Nine waiver occurrences retain the
  previously approved brand roles. The final 320/375px scan checks compact
  control labels, not only the standard 390px screenshots.
- Independent read-only correctness/security/simplicity review approved after
  five findings were fixed: message cursor race, date threshold remounting,
  contact-form unmounting, preview continuation range, and room summaries.
  Reports: `reviews/scale-correctness-{initial,final}.md`.
- Independent UI review approved the full implementation and final compact
  button refinement: `reviews/scale-ui.md`, `reviews/scale-ui-final.md`.
  Reviewers inspected source/screenshots/results; parent executed the tests.

Final evidence: `/tmp/fold-verify-proof/20261101T232904Z-1083168/`, with
`ui/`, `busy-church/manifest.json`, `busy-lists/` and `directory-new/`.
Historical comparison: `20261101T214745Z-294686/busy-church/` in the same parent
directory. Preliminary runs exposed and corrected overlapping selected segments,
month accessible-name spacing, a hidden page-count focus anchor, a clipped
church-wide select, and compact-button wrapping. They are not outstanding
failures. The final proof is from a fresh database and rebuilt binary.

Logs: `/tmp/fold-scale-release-{ui,browser,directory,tests,hallmark}.log`,
`/tmp/fold-scale-go-final.log`, `/tmp/fold-scale-lint-{go,web}.log`.
Verification instances are stopped after retaining evidence. Nothing was pushed
or merged.

## Reproduction

Use Node 24 and the isolated verification skills; never the shared `fold`
database or an `_e2e` database. Build Vite before Go compilation.

```sh
mise exec node@24 -- .cursor/skills/verify-fold/verify-fold up
.cursor/skills/verify-fold/verify-fold doctor <run-id>
eval "$(.cursor/skills/verify-fold/verify-fold env <run-id>)"
UI_PAGES=home-signed-out,home,my-groups,group-member,event,group-leader,messages,admin,admin-users \
  mise exec node@24 -- node .cursor/skills/verify-ui/drive-ui.mjs
SEED_ONLY=1 mise exec node@24 -- node .hallmark/evaluate-directory.mjs
mise exec node@24 -- node .hallmark/seed-busy-church.mjs
mise exec node@24 -- node .hallmark/evaluate-busy.mjs
mise exec node@24 -- node .hallmark/verify-directory.mjs
.cursor/skills/verify-fold/verify-fold down <run-id>
```

The busy seed uses ordinary authenticated APIs and respects the date-add rate
limit; two 61-second pauses are expected. It writes its manifest to
`busy-church/`; the updated evaluator writes new evidence to `busy-lists/`.
Alerts browsing performs its normal seen acknowledgement. Screenshots and raw
logs remain local, while this report, reproducible scripts and review evidence
are committed.

## Limits

These synthetic public/adult fixtures do not establish teen, guest, consent,
private/secret or mixed-permission behavior at scale. No policies were changed;
unit/API regression coverage is separate from the scale fixture. This is not
field research, a production load benchmark or a photo/network performance
test. Rooms have no bookings/conflicts seeded. Long row content still takes
space; pagination does not make every management task fit one viewport.
Full Playwright and production/deployment verification were not run.
