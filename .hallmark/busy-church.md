# Directory improvements and busy-church experiment

## Delivered versus recommended

All five recommendations from `directory-50.md` are implemented. The subsequent
busy-church findings below are **recommendations, not additional shipped changes**.
No backend, schema, permissions, dependency, or deployment changes were made.

- Search, selected tags, and expanded result count are URL-backed. Detail → Back
  restores the originating row and its viewport position, after both the agenda
  and results settle. Position is in memory only, under the user-keyed shell;
  signing out discards it. No fetched directory data is persisted.
- A compact sticky search/Filters toolbar replaces the always-expanded tag wall.
  Selected tags stay visible/removable; counts are politely announced. Matching
  remains exact, case-insensitive **OR**. Opening Filters from deep in a list
  brings the disclosure into view. Removing chips/Clear preserves keyboard focus.
- Initially **12 groups**; explicit **Show more groups** appends 12 in existing
  order. No infinite scroll or virtualization. Nonfinal expansion keeps scroll
  and button focus; the next Tab visits the first added row. The final batch
  focuses its first added row when the button disappears.
- The four-date personal agenda is above a full-width directory. Desktop uses
  two directory columns, not two columns squeezed into the old narrow rail.
  The agenda retains DEC-94's Just mine, 60-day window and four-date maximum.
- **Browse groups** lands reliably below the sticky shell, including when the
  preceding content loads late. It is available to visitors and members.

The approved violet/Inter system, date metadata, existing routes, API filtering,
permission decisions and selective first-cover priority remain.

## Like-for-like 50-group comparison

Same synthetic 50-group dataset as the earlier experiment, before adding the
busy-church fixture. Viewport height: 900px. Older evidence is in
`20261101T210036Z-4121665/directory-50`; new evidence is in
`20261101T214745Z-294686/directory-new`, both under `/tmp/fold-verify-proof/`.

| Metric | Before | After |
| --- | ---: | ---: |
| Visitor phone, first group Y | 849px | 613px |
| Member phone, first group Y | 1,122px | 886px |
| Member phone, initial page height | 8,514px | 2,854px |
| Member desktop, initial page height | 7,744px | 1,514px |
| Desktop directory width | 631px of 1,104px | Full 1,104px, two columns |
| Phone filters at rest | Six rows / 304px of tags | Collapsed; whole toolbar 99px |
| Initially rendered groups | 50 | 12, expandable to all 50 |
| Search → detail → Back | Query lost; all 50 returned | Query, result count, focus and position retained |

Tradeoff: the desktop member's first result moves from Y=453 to Y=543 because
all four agenda dates are above the directory. The directory now uses the full
width, shows two rows across, and no longer leaves a tall empty side rail.
The phone still prioritizes four personal dates; Browse groups provides a clear
way past them rather than silently removing the agenda.

**36 screen/state checks passed** at 390/768/1280 for visitors and members:
initial view, jump, filters open, selected filters, appended results and search
return. The script also checks the 20th-row return position, opening sticky
Filters while deep in the results, focus, ordering, and URL state. No recorded
hard layout or axe failures.

## Busy fixture and method

Final clean run: **`20261101T214745Z-294686`**, served by the real embedded Go
binary with its own `fold_verify_*` Postgres database. All fixture writes went
through authenticated app APIs. Final inventory:

| Content | Actual count |
| --- | ---: |
| Active adult accounts, including normal verification users | 100 |
| Public groups, 19 unique tags | 50 |
| Event definitions | 149 |
| Generated dates | 191 |
| Upcoming church-wide events | 25 |
| Event definitions in the three busiest groups | 32, 31, 31 |
| Dates in a single recurring series | 40 |
| Members in the main roster | 60 |
| People attending one date | 49 |
| Groups joined by the verification member | 25 |
| Stored notifications in that member's feed | 30 |
| Unsent message drafts | 40 |
| Rooms, across three buildings | 18 |

The main group contains 30 new single events, the existing four-date series, and
one 40-date series. Two other groups each have 30 new events plus their existing
date. Dates are concentrated enough to put **90 events in one calendar week**.
Names, locations and longer titles are synthetic stress fixtures, not proposed
production content. All groups use existing fallback covers. No bulk message
was scheduled or sent. Browsing Alerts uses its normal seen acknowledgement;
other browser tasks only browse, search, filter and expand existing content. Seeding respects the existing 30/minute manager date-add
limit by waiting and retrying; no throttle or policy was bypassed.

The first exploratory run exposed harness issues: attendee controls belong on
the series/event management route, lazy management content must finish loading,
and pagination must be awaited by its visible result rather than an already-idle
network flag. Resuming that seed also exposed unstable array ordering; fixture
selection is now deterministic. Final evidence comes from a fresh database, not
that partially resumed dataset. These were harness corrections, not app fixes.

## What the larger church revealed

Page heights below are measured at **390 × 900**. Height alone is not a failure;
combined with absent search, distant actions and repeated controls it identifies
where everyday tasks become cumbersome. All 57 screen/state checks still passed
horizontal-overflow, clipped-control, sub-24px-target and moderate/serious/critical
axe checks. Automated layout success does not mean a list is pleasant to use.

| Surface | Observation | Recommended next pattern |
| --- | --- | --- |
| Visitor Home | 25 church dates precede the directory; first group Y=4,215, page 6,103px | Show a **four-date church preview** plus **See all church events**, with a truthful total. Keep Browse groups. Do not turn Home into the whole public calendar. |
| Calendar agenda | Initial 30-day range renders 180 event dates; page **22,943px**; Show more dates is at Y=22,811 | Render a bounded agenda by day/event count, with date jump and compact persistent filters. Do not require scrolling through a month to reach navigation. Preserve chronological order and URL/Back state. |
| Group events | First 25 dates collapse to 19 series rows but still make **7,126px**. Loading all three cursor pages yields 32 series and **11,318px** | Use compact event summaries with date/place/status and an explicit details/response affordance. Add date or event search. Keep API cursor paging; place load-more after the results, not only above them. Do not confuse loaded dates with a series total. |
| One 40-date series | **10,019px**, 201 visible main-content controls | A compact date list with bounded batches and an expanded selected date. Keep per-date RSVP and overrides, and the existing whole-series response path; do not repeat every full response form by default. |
| 60-member group roster | **7,940px**, no roster search; add-person workflow follows the list | Put name search, count and Add people above results. Use alphabetic compact rows, role filters and 25-row batches. Keep private contact details and management actions behind their current policies. |
| 49-person sign-up manager | **9,806px**; repeated attendee details and removal controls | Search and Going/Maybe/Waitlist filters, compact rows and 25-row batches; keep Add person reachable at the top. Use a secondary row menu for infrequent removal. Preserve capacity/approval/guardian rules. |
| Admin users | All 100 accounts rendered; **14,790px** on phone, 7,427px desktop | Keep the existing searchable table/cards, add visible totals and **25-row pagination**. Search already works: an exact email reduced 100 to one at every width. A later server cursor is useful when payloads grow, not a prerequisite here. |
| Admin groups | 50 management rows; **8,663px** | Reuse the bounded management-list pattern, retaining current search/status filters and row actions. |
| My groups | 25 groups; **3,945px**, no search or batching | Reuse directory search/count/batching; default to the viewer's groups, not discovery tags or an unrelated presentation. |
| Messages | 40 drafts; **5,715px**, no subject search or state filter | Add subject search, Draft/Scheduled/Sent filters, and date-grouped bounded results. The source currently caps lists at 200 with no older-page path: design a real older-page path before that cap hides history. This 40-draft experiment did not exercise the cap. |
| Rooms | 18 cards; **5,323px**, each repeats three actions | Group by building; add name search and a compact desktop table/mobile list with Edit and a secondary action menu. Bookings/conflicts were not seeded, so this does not assess room-calendar contention. |

### Keep these working patterns

- **Home directory:** still 12 initial rows with 50 total groups; four personal
  dates remain. At busy-church scale, Browse groups places the first result at
  **Y=216** for both audiences at all three widths, even with 25 preceding
  church events. The signed-in phone page is 3,082px; desktop is 1,593px.
- **Notification paging:** 20 initial rows; Show more reached all 30 at each
  width. Existing paging is a good model, not a list to redesign indiscriminately.
- **Group-date API paging:** three pages worked without losing the 32 series.
  The remaining issue is the amount of presentation per event and button placement.
- **Month view:** the bounded grid with selected-day results is more manageable
  than the long agenda. At 390px, add a numeric busy-day count or overflow cue:
  three dots do not distinguish a lightly booked day from a heavily booked one.
- **Week view:** 90 dates produce 1,816px on desktop but 10,942px when stacked on
  phone. Use day navigation/selection on phone instead of rendering the entire
  busy week as one long stack; retain a clear week overview.
- **Admin navigation:** at 390px the selected Users tab is partially outside the
  scroll strip when counts widen labels. Scroll the active tab into view, or use
  a compact section selector on phone. This is not page overflow, so the hard
  layout check correctly does not flag it.

### Recommended order

1. **Bound event browsing:** visitor church preview, calendar agenda, and long
   group/series lists. This removes the most scrolling and repeated controls.
2. **Make people findable:** shared roster search/count/filters plus 25-row
   paging for members, attendance and admin users. Preserve policy distinctions;
   sharing a presentation must not unify their authorization rules.
3. **Apply the proven browse pattern to remaining lists:** My groups, admin
   groups, message history and rooms; then refine phone week/month/admin navigation.

No virtualization library is warranted by this fixture. The largest observed
screen had fewer than 1,900 DOM nodes. Directory JSON was about 21KB; users about
27KB; calendar and roster responses were tens of KB. Those are decoded payload
sizes, not transfer sizes or evidence that all future churches fit a client-only
strategy. Local request timings are not production benchmarks. Start with bounded
rendering and task-oriented controls; add server cursors where actual data growth
and history access justify them. No database migration is required for the first
presentation improvements.

## Verification and review

- **138 Vitest files / 1,388 tests passed.** Twelve new directory cases, with
  existing Home/tag tests updated for the new disclosure and selected-chip UI.
  Delayed responses, exact OR matching, URL state, Back, batches, final-batch
  focus, disappearing controls and selective image priority remain covered.
- **99 final browser checks:** six standard baseline + 36 directory + 57 busy
  church screen/state checks, all without recorded hard failures. Busy tasks
  also verify group-date pagination, user search, notification expansion and
  Browse groups at all three widths.
- TypeScript, Knip, SPA build, Go build/vet/staticcheck, selected Go source guards,
  token contrast and bundle budget passed. Eager JS: **173.6KB gzip / 180KB**.
- Rendered Hallmark scan: **0 FAIL, 0 WARN**, with the existing nine brand-waiver
  occurrences, 22 answered finish questions and six dismissed false positives.
  The old kept-content scan in `verification.md` is historical; this change
  deliberately changes which result links are initially rendered. Access to all
  results is covered by Show more, navigation and filter tests instead.
- Independent read-only UI/correctness review initially found three filter edge
  cases. All were fixed and regression-tested. Final review **explicitly approves**
  the implementation: `reviews/directory-final.md`. The reviewer inspected source
  and supplied captures; test execution was the parent's. Busy-church follow-on
  recommendations were not independently approved as implemented designs.
- **Not run:** full Go database tests, the full Playwright suite, production
  deployment, slow-network/large-photo or load testing. The revised E2E tag spec
  was type-checked, not executed as a suite. This all-adult public-group fixture
  does not establish private/secret-group, teen, guest, consent or mixed-permission
  behavior at scale. Those policies were not changed.

## Evidence and reproduction

Final evidence: `/tmp/fold-verify-proof/20261101T214745Z-294686/`

- `directory-new/results.json`, viewport PNGs and `-full.png` counterparts.
- `busy-church/manifest.json`, `results.json`, viewport PNGs and full-page captures.
- Useful phone captures: `visitor-home-jump-390.png`, `calendar-agenda-390.png`,
  `calendar-month-390.png`, `group-events-390.png`, `group-roster-390.png`,
  `attendee-roster-390.png`, `admin-users-390.png`, `messages-390.png`.
- Test/static logs: `/tmp/fold-busy-{tests,final-guards,final-lint-go,final-web}.log`.
- Browser/seed logs: `/tmp/fold-busy-final-{ui,directory,seed,browser}.log`.
- Rendered scanner: `/tmp/fold-busy-hallmark.log`.

Read both verification skills; use Node from `.nvmrc`. Never target the shared
`fold` or `_e2e` databases. Keep Vite build ahead of Go compilation.

```bash
mise exec node@24 -- .cursor/skills/verify-fold/verify-fold up
# Substitute the returned isolated run id below.
.cursor/skills/verify-fold/verify-fold doctor <run-id>
eval "$(.cursor/skills/verify-fold/verify-fold env <run-id>)"
UI_PAGES=home-signed-out,home UI_WIDTHS=390,768,1280 \
  mise exec node@24 -- node .cursor/skills/verify-ui/drive-ui.mjs
SEED_ONLY=1 mise exec node@24 -- node .hallmark/evaluate-directory.mjs
mise exec node@24 -- node .hallmark/verify-directory.mjs
mise exec node@24 -- node .hallmark/seed-busy-church.mjs
mise exec node@24 -- node .hallmark/evaluate-busy.mjs
.cursor/skills/verify-fold/verify-fold down <run-id>
```

`evaluate-directory.mjs` retains the historical pre-implementation evaluator;
use `SEED_ONLY=1` on the current layout, then `verify-directory.mjs`. The busy seed
is deterministic on a fresh instance and resumes existing named fixtures without
recreating events; rate-limit waits are expected. Results are synthetic evidence,
not field-user research. Both completed verification instances were stopped and
their isolated databases removed after preserving evidence. Nothing was pushed
or merged.
