# Audit 2026-11 — Frontend: R15 speed/discoverability, R16 generated types

Implementation briefs for a coding agent. These are briefs, not code. Read
`AGENTS.md` first; where this file and `docs/plan/PLAN.md` disagree, PLAN
wins and the disagreement gets raised, not silently resolved. Audit items
R7 (freeze push / strike SMS) and R8 (`self_signup=open`) were rejected by
the owner: nothing below acts on them, and no slice here may be used as a
reason to touch push sequencing, SMS promises, or DEC-4's signup modes.

Shared rules for both items:

- Single Go binary with the SPA embedded (`go:embed`, `web/dist/`); no new
  npm or Go dependency in either item (R15 cache and R16 generator are both
  hand-rolled on existing libraries and the standard library).
- UI changes use `web/src/components/ui` controls (`Button`, `Chip`,
  `Field`/`Input`/`Select`, `EmptyState`) and role tokens from
  `web/src/index.css`. No raw controls, no palette classes, no literal
  `style=` (DEC-60). Danger colour follows DEC-63: solid `danger` only
  when destruction is the point of the dialog, `danger-outline` beside a
  main action, never colour alone for status.
- No-store discipline is load-bearing: the API sends `no-store`
  (`api.go:562`, `main.go:369`) and logout leaves nothing on screen or in
  cache (`web/src/auth.tsx:97`). The R15 cache is in-memory only and dies
  with the session; it never touches `localStorage`/`sessionStorage`.
- The entry-chunk gzip budget gate (`web/scripts/bundle-size.mjs:7`) runs
  in CI after the build (`.github/workflows/test.yml:51`) and after every
  slice that moves imports.
- Tests sit next to the code they test. Verification for every slice:
  `go build ./...`, `go vet ./...`, `make lint-go` (Go-touching slices),
  `(cd web && npx tsc --noEmit)`, `(cd web && npx knip)`,
  `cd web && npm test`, bundle gate after `npm run build`, plus the e2e
  a11y/layout specs at 390px for any user-visible slice
  (`web/e2e/a11y.spec.ts`, `web/e2e/layout.spec.ts` over
  `web/e2e/ui-pages.ts`). Prove UI changes with `.cursor/skills/verify-ui`
  and the 390px/768px/1280px screenshots (`web/e2e/screens.spec.ts`).
- Docs say "recommended", never the other word (the docs-wording guard
  fails the build). Tooling is shell or Go; no Python, including in the
  R16 generator and its check.
- Errors a person can act on follow DEC-61 (`api_errors.go` + `ERROR_COPY`
  in `web/src/lib/errors.ts`); pages never show server error text. The
  `alert()` replacements in R15 exist partly because `alert(errorText(...))`
  is the last place a failure leaves the page's own error surface.

---

## R15 — Frontend speed/discoverability pass

- Priority: P2 (audit `docs/AUDIT-2026-11-01.md:209`), effort M.
- Status: Approved 2026-11-01 (audit).
- GitHub issue: none known at brief time. The bundle-budget raise this
  item re-tightens is `web/scripts/bundle-size.mjs:6` (140 → 180 kB,
  "efficiency later", Oct 30 2026).

### 1. Goal

The first screen (signed-out Home, which is also the app's landing page for
a church shopping for software) downloads and renders without the two
heaviest detail pages in its eager path; moving between pages stops
re-fetching the same church, group and session facts; a person hunting for
a group can narrow by the facts that decide whether attendance is possible
(meeting day, time of day, remote seats, childcare); every destructive
question is asked in a dialog the person can see, use with a keyboard or
screen reader, and read in Fold's own words; a leader on a phone or tablet
can reach Messages without knowing it hides under Me; and a signed-out
visitor meets the group directory first and the account request form
second.

### 2. Current state (evidence)

**Bundle and lazy boundaries.**

- `web/src/App.tsx:4` imports `Home`, `Login` (`App.tsx:5`),
  `GroupDetail` (`App.tsx:6`), `EventDetail` (`App.tsx:7`), `MyGroups`
  (`App.tsx:8`) and `NotFound` (`App.tsx:10`) eagerly. Every other page is
  already a `lazy()` boundary with the `.then((m) => ({ default: ... }))`
  shape (`App.tsx:16`–`App.tsx:43`, 29 pages). Routes render `GroupDetail`
  at `App.tsx:94` and `EventDetail` at `App.tsx:97`; one outer `Suspense`
  with a `Skeleton kind="page"` fallback already wraps all routes
  (`App.tsx:77`), and a few routes add their own labelled fallback, e.g.
  `EventDatePage` at `App.tsx:98`.
- `GroupDetail.tsx` is 1104 lines and `EventDetail.tsx` 498, and both pull
  the heavy shared tree: `SeriesEditor`, `DateEditor`, `RsvpButtons`,
  `GroupDepartureDialog`, `GroupVisibilityReview`, `AddPeople`,
  `ImageUpload`, `GuestRegistrationForm` (imports at the top of
  `web/src/pages/GroupDetail.tsx` and `web/src/pages/EventDetail.tsx`).
  `EventDetail` already lazy-loads one child (`ManageRoster`,
  `web/src/pages/EventDetail.tsx:36`), so the pattern is proven in place.
- The gate counts only the entry chunk plus its `modulepreload` links from
  `dist/index.html` (`web/scripts/bundle-size.mjs:8`–`bundle-size.mjs:19`),
  budget 180 kB gzip (`bundle-size.mjs:7`), raised from 140 "for the
  product-audit slices … efficiency later" (`bundle-size.mjs:6`). The
  script's own test reads the budget out of the script, so lowering the
  number needs no test change (`web/scripts/bundle-size.test.mjs:12`).
  Measured on the `web/dist` in the tree at brief time: 12 eager chunks,
  176.3 kB gzip of the 180 budget — the implementer must re-measure on a
  fresh `npm run build` before touching imports, because the committed
  `dist` may not match `HEAD` source. `web/dist/README.txt` must survive
  every build (the `keep-dist-readme` plugin, `web/vite.config.ts:12`).

**Duplicate reads.**

- The one fetch client is `api()` (`web/src/api.ts:44`): it sets JSON
  headers and the CSRF header for unsafe methods (`api.ts:52`), calls
  `fetch` with no cache options (`api.ts:55`), maps 401 to the
  unauthorized handler unless `anonymousOK` (`api.ts:69`), and throws
  `ApiError` with a stable `code` (`api.ts:15`, `api.ts:75`). Nothing
  dedupes or caches a GET. The CSRF token itself is a module-level
  single-flight already (`api.ts:27`). CSV reads are a separate function
  with explicit `no-store` (`api.ts:83`) and stay out of the cache.
- `Site.get()` (`api.ts:295`, read-only church facts: name, timezone,
  `selfSignup`, `roomsEnabled`, `pushEnabled`) is called from 15
  non-test sites:
  `web/src/lib/useSiteName.ts:8`, `web/src/lib/useEventZone.ts:16`,
  `web/src/lib/usePushEnabled.ts:11`, `web/src/pages/Home.tsx:26`,
  `web/src/pages/Join.tsx:16`, `web/src/pages/NotificationsSetup.tsx:111`,
  `web/src/pages/ContactHistory.tsx:111`, `web/src/pages/Admin.tsx:1189`,
  `Admin.tsx:1335`, `Admin.tsx:1569`,
  `web/src/components/SeriesRoomField.tsx:113`,
  `web/src/components/RoomField.tsx:73`,
  `web/src/components/NeedsRoomList.tsx:45`,
  `web/src/components/CreateRoomField.tsx:96`,
  `web/src/components/calendar/CalendarViews.tsx:20`. One page load of
  Home issues it at least twice (`Layout` → `useSiteName`, and `Home`
  itself), and pages with room fields add more. Only `usePushEnabled`
  shares its read, with a private module-level promise
  (`usePushEnabled.ts:6`) that this item replaces with the shared cache
  (slice R15-2 keeps that hook's synchronous `known` shortcut).
- `Groups.list()` (`api.ts:191`, `GET /groups` with `search`/`tag` params)
  is called from `web/src/components/GroupDirectory.tsx:67` (tag choices,
  unfiltered), `GroupDirectory.tsx:78` (results, debounced),
  `web/src/pages/Calendar.tsx:76` (the Group filter's options),
  `web/src/pages/MyGroups.tsx:27` ("Groups meeting soon") and
  `web/src/pages/Admin.tsx:1254`. `GroupDirectory` already hand-shares its
  first unfiltered request between the tag choices and the first results
  through a ref (`GroupDirectory.tsx:44`, `GroupDirectory.tsx:65`,
  `GroupDirectory.tsx:77`) and deliberately does not cache later queries
  (comment at `GroupDirectory.tsx:62`). `Groups.mine()` (`api.ts:206`) has
  one caller (`MyGroups.tsx:75`); `AdminGroups.list()` (`api.ts:730`,
  `GET /admin/groups`) is a different endpoint and is not deduplicated
  with `Groups.list()`.
- `Auth.me()` (`api.ts:171`) has exactly one production caller: the auth
  provider's `refresh` (`web/src/auth.tsx:58`, call at `auth.tsx:61`,
  preceded every time by `ensureCsrf()` at `auth.tsx:60`). Components read
  the result through `useAuth()` (`auth.tsx:126`), 60+ call sites. So for
  `/auth/me` the win is in-flight dedupe of overlapping refreshes (several
  token pages call `refresh` after their action, e.g.
  `web/src/pages/VerifyEmail.tsx:12`), never a TTL: serving a stale user
  after login, consent, or a role change would be a correctness bug in
  exactly the shape `App.tsx:72`'s keyed remount exists to prevent.
- Logout is a full reload: `auth.tsx:97` ends the server session, drops
  client storage with `clearClientData()` (`auth.tsx:10`) and calls
  `window.location.replace("/")` (`auth.tsx:107`), so any module-level
  cache in `api.ts` dies with the document. The cache must still be
  cleared explicitly there (and on login), because tests and any future
  non-reload path must not leak the previous person's reads.

**Filters: Calendar has them, the directory does not, and the data is not
there yet.**

- Calendar keeps its filter state in the URL (`web/src/pages/Calendar.tsx:37`
  `mine`, `Calendar.tsx:38` `remote`, `Calendar.tsx:39` `childcare`,
  `Calendar.tsx:40` `group`, `Calendar.tsx:41` `church`, `Calendar.tsx:43`
  admin-only `deleted`; date/view/days at `Calendar.tsx:35`–`Calendar.tsx:47`)
  and passes it to `Calendar.list` (`Calendar.tsx:86`; client wrapper at
  `api.ts:341`). The filter panel (`Calendar.tsx:176`) is `Chip`s plus a
  `Field`/`Select` for the group (`Calendar.tsx:196`); the Remote and
  Childcare chips appear only when something in view offers them, and a
  selected chip always stays visible so it can be switched off
  (`Calendar.tsx:124`–`Calendar.tsx:130`); a one-line summary names the
  active filters when the panel is closed (`Calendar.tsx:133`,
  `Calendar.tsx:174`). Note for the slice: despite the audit's wording,
  Calendar has no weekday or time-of-day filter today — `remote` and
  `childcare` exist, `day`/`time` are new in both places.
- The directory narrows by text and tags only: `search` and repeated `tag`
  params (`GroupDirectory.tsx:36`, `GroupDirectory.tsx:37`), a `SearchBox`
  (`GroupDirectory.tsx:162`), tag `Chip`s in a collapsible panel
  (`GroupDirectory.tsx:179`) and selected-tag chips above the results
  (`GroupDirectory.tsx:172`). The endpoint matches: `apiListGroups` reads
  only `search` and `tag` (`api_groups.go:29`, tag handling at
  `api_groups.go:46`) over `directoryQuery` (`api_groups.go:33`).
- The row the directory renders, `GroupSummary` (`web/src/types.ts:43`)
  from `groupSummaryJSON` (`api.go:109`), carries one schedule fact:
  `nextEventAt` (`types.ts:54`, `api.go:120`), computed as the minimum
  upcoming scheduled `starts_at` per group in `groupSummaries`
  (`api_events.go:444`, the aggregate at `api_events.go:469`). Remote and
  childcare facts live on occurrences (`CalendarOccurrence`,
  `types.ts:519`, fields at `types.ts:532`; fuller shape on
  `EventOccurrence`, `types.ts:214`). A weekday derived from the single
  next date would lie for a group with events on several days, so the
  shared-filter slice needs a small backend facet, not just a hook —
  see slice R15-3 and open question 3. Privacy gate: the date a facet is
  derived from must be one the viewer may see; `dateCandidates` loads
  candidate series facts without granting series rights
  (`api_event_access.go:650`) and every date is then filtered through
  `visibleSet.canSeeDate`, which applies the occurrence's
  `visibility_override` before asking `policy.CanSeeEvent`
  (`api_occurrence_visibility.go:18`, override applied at
  `api_occurrence_visibility.go:31`). `groupSummaries`' existing `min(starts_at)`
  query does not itself apply per-date visibility
  (`api_events.go:473`); do not copy that shape blindly for facets
  (a hidden date must not make a group match "Tuesdays, with childcare").

**Native confirm/alert.**

- Count at brief time: 43 real sites — 39 `confirm(`/`window.confirm(`
  and 4 `alert(` — from
  `rg -n '\b(window\.)?(confirm|alert)\s*\(' web/src --glob '!*.test.*'`,
  excluding API methods that happen to be named `confirm`
  (`AccountRequests.confirm`, `Guests.confirm`, `RoomRequests.confirm`)
  and test fixtures. Grouped by page/component, every site the migration
  slice must clear:
  - Pages — Admin (`web/src/pages/Admin.tsx:144` delete group,
    `Admin.tsx:207` reject account request, `Admin.tsx:481` add over a
    full group, `Admin.tsx:627` delete group, `Admin.tsx:920` revoke a
    calendar link, `Admin.tsx:936` disable a person, `Admin.tsx:1001`
    dynamic `ask`); GroupDetail (`web/src/pages/GroupDetail.tsx:96` cancel
    a date, `GroupDetail.tsx:102` `alert` on cancel failure,
    `GroupDetail.tsx:586` `alert("That didn't work.")`,
    `GroupDetail.tsx:690` dynamic `question`, `GroupDetail.tsx:710` ask an
    admin to delete, `GroupDetail.tsx:743` `alert` on cover-upload
    failure); MessageComposer (`web/src/pages/MessageComposer.tsx:302`
    cancel message, `MessageComposer.tsx:318` retry question,
    `MessageComposer.tsx:334` discard draft, `MessageComposer.tsx:350`
    stop editing); EventDetail (`web/src/pages/EventDetail.tsx:144` delete
    event, `EventDetail.tsx:331` cancel a date, `EventDetail.tsx:337`
    `alert` on cancel failure); EventDate (`web/src/pages/EventDate.tsx:77`
    dynamic `copy`); GuestManage (`web/src/pages/GuestManage.tsx:36`
    cancel own registration); Profile (`web/src/pages/Profile.tsx:196`
    remove profile picture).
  - Components — ManageRoster ×5 (`web/src/components/ManageRoster.tsx:37`
    remove registration, `ManageRoster.tsx:39`, `ManageRoster.tsx:50`,
    `ManageRoster.tsx:66`, `ManageRoster.tsx:71` the four over-capacity
    variants, two of them inside a catch-and-retry once pattern);
    CalendarFeedCard (`web/src/components/CalendarFeedCard.tsx:64` remake
    link, `CalendarFeedCard.tsx:69` turn off link); RoomFeedPanel
    (`web/src/components/admin/RoomFeedPanel.tsx:75`,
    `RoomFeedPanel.tsx:80`, the same two questions for a room);
    AddPeople (`web/src/components/AddPeople.tsx:70` group full);
    AddRequestForm (`web/src/components/AddRequestForm.tsx:28` allow
    over-limit ask); ContactGrantCard
    (`web/src/components/ContactGrantCard.tsx:54` revoke contact access);
    DateEditor (`web/src/components/DateEditor.tsx:90` save cancels and
    notifies registrants); GuardianConsentCard
    (`web/src/components/GuardianConsentCard.tsx:21` revoke consent);
    GuardianLinkCard (`web/src/components/GuardianLinkCard.tsx:46` do not
    confirm a guardian); HouseholdCard
    (`web/src/components/HouseholdCard.tsx:161` remove household person);
    PushDeviceCard (`web/src/components/PushDeviceCard.tsx:204` turn off
    all devices); RsvpButtons (`web/src/components/RsvpButtons.tsx:106`
    decline removes dates from the person's calendar; the function
    returns a boolean, so this site needs the promise form);
    TeenConsentPanel (`web/src/components/TeenConsentPanel.tsx:39` revoke
    consent); and the app-wide navigation guard
    (`web/src/lib/unsavedChanges.tsx:58`, inside `NavigationGuard`; the
    tab-close path at `unsavedChanges.tsx:36` is `beforeunload` and
    stays native — browsers do not let a page replace that prompt).
- The 4 `alert(` sites are all failure paths (`GroupDetail.tsx:102`,
  `GroupDetail.tsx:586`, `GroupDetail.tsx:743`, `EventDetail.tsx:337`).
  They are not dialogs waiting to happen; they are errors that must land
  on the page's own surface (`ErrorNotice`, `web/src/components/ErrorNotice.tsx`,
  or `FormError` via `components/ui/Field`), next to the control that
  failed, with a retry where the page already knows how.
- `components/ui` has no dialog today. Inventory at brief time: `Badge`,
  `Button`, `Chip`, `DayButton`, `EmptyState`, `Field` (+ `Form`,
  `FormContext`, `FormActions`), `Illustration`, `Menu`, `Pagination`,
  `PeoplePicker`, `SearchBox`, `SegmentButton`, `Skeleton`, `Tabs`,
  `ToolbarButton`, `ViewSwitch`, `ZonePicker` — every one with a sibling
  `.test.tsx` except `Button`'s helpers, `FormActions`, `FormContext` and
  `Pagination`. `Button` variants (`web/src/components/ui/Button.tsx:7`)
  include `danger` and `danger-outline`, backed by `.btn-danger` and
  `.btn-danger-outline` (`web/src/index.css:205`, `index.css:207`).
- Three hand-built modals already duplicate the same shell and are the
  base to extract, not a library: `SeriesRsvpDialog`
  (`web/src/components/SeriesRsvpDialog.tsx:7`) does portal + inert +
  initial focus + focus return + Tab trap + Escape + overlay-click
  (`SeriesRsvpDialog.tsx:20`–`SeriesRsvpDialog.tsx:54`);
  `RejectGroupDialog` (`web/src/components/admin/RejectGroupDialog.tsx:15`)
  repeats it with a busy state and the DEC-63 note that solid danger is
  right because destruction is the dialog's whole point
  (`RejectGroupDialog.tsx:8`); `GroupDepartureDialog`
  (`web/src/components/GroupDepartureDialog.tsx:25`) is a third variant —
  no portal, no inert, `tabIndex={-1}` panel, focus fallback when the
  opener is gone (`GroupDepartureDialog.tsx:75`,
  `GroupDepartureDialog.tsx:111`, `GroupDepartureDialog.tsx:206`).
  All three use `role="dialog" aria-modal="true"` (never `alertdialog`).
  `GroupVisibilityReview` (`web/src/components/GroupVisibilityReview.tsx:45`)
  is a non-modal `role="dialog"` region, not a fourth shell to extract.
- Native dialogs are why several e2e specs must install a browser dialog
  handler (`web/e2e/exact-date.spec.ts:200`, `web/e2e/returns.spec.ts:8`,
  `web/e2e/hosts.spec.ts:112`, plus `contact-requests`, `contact-history`,
  `household-registration`, `name-search`, `teen-consent` specs) and why
  Vitest files spy on `window.confirm`
  (`web/src/pages/NewGroup.test.tsx:110`,
  `web/src/pages/GroupDetail.test.tsx:594`,
  `web/src/pages/NewEvent.test.tsx:182`,
  `web/src/pages/MessageComposer.test.tsx:191`,
  `web/src/pages/ContactRequests.test.tsx:23`). Those tests migrate to
  role queries in the same slice as their page.

**Messages on phone and tablet; signed-out Home.**

- Top nav shows Messages only from `lg` up
  (`web/src/components/Layout.tsx:89`), with the reason recorded at
  `Layout.tsx:86`: at 768px one more link squeezed the church name (to
  13px for an admin) in `layout.spec.ts`/`name-search.spec.ts`. The phone
  bottom bar (`Layout.tsx:132`, `sm:hidden`) has five tabs — Home,
  Calendar, My groups, Notifications (badge), Me (`Layout.tsx:137`–
  `Layout.tsx:153`) — and no Messages. The only sub-`lg` route in is a
  link rendered with `lg:hidden` inside the Me/profile page
  (`web/src/pages/Profile.tsx:92`), next to `MailIcon`
  (`web/src/components/icons/index.tsx:171`). All of it is gated on
  `canCompose` (`web/src/auth.tsx:28`, from `/auth/me` at `auth.tsx:61`):
  leaders of approved groups, church-event hosts and admins only.
- Signed-out Home (`web/src/pages/Home.tsx:58`) leads with the hero and
  three actions: primary "Request an account" (`Home.tsx:67`, shown
  unless `selfSignup` is `off`), secondary "Browse public events"
  (`Home.tsx:68`), and a "Browse groups" text link (`Home.tsx:69`) that
  scrolls to the directory. The church-events preview comes next
  (`Home.tsx:79`, next 4 public dates), and `GroupDirectory` renders last
  (`Home.tsx:100`). The signed-in branch (`Home.tsx:53`) is a greeting
  header plus `ComingUp` plus the directory and does not change. The
  header already carries Sign up and Log in for signed-out visitors
  (`Layout.tsx:120`, `Layout.tsx:125`), so the account door is present
  twice before a visitor has seen a single group.

### 3. Scope

In:

- `GroupDetail` and `EventDetail` become route-level lazy chunks; the
  gzip budget is re-measured and re-tightened to just above the new
  eager size.
- A tiny in-memory cache inside `web/src/api.ts`: GET in-flight dedupe
  plus short TTL where stated, allowlisted endpoints only, invalidated
  by every successful mutation and destroyed on logout/401/login. No new
  dependency, no state library, no re-architecture of call sites.
- Directory filters for meeting day, time of day, remote seats and
  childcare, sharing one filter hook and vocabulary with Calendar, with
  the minimal backend facet those filters need.
- `components/ui/Dialog` (modal shell) + `components/ui/ConfirmDialog`,
  migrating all 43 native sites and the tests that accept native
  dialogs; a guard so none come back.
- Messages reachable from the primary navigation on phones and tablets
  for people who `canCompose`; signed-out Home reordered so the
  directory leads.

Out:

- Replacing `api.ts` with a query library, global client state, offline
  support, HTTP caching headers (the server stays `no-store`,
  `api.go:562`), service-worker fetch caching (the worker deliberately
  has no `fetch` handler so it cannot serve a stale shell after logout).
- Preloading, prefetching or server hints for detail chunks beyond the
  focused-link intent prefetch in slice R15-1; no `manualChunks` surgery
  in `web/vite.config.ts`.
- Changing `/groups` ordering (`viewerGroupList`, `api_groups.go:90`),
  the tag model, the Calendar endpoint's own filters, or any visibility
  rule. Filters narrow what the viewer may already see, never widen it.
- Redesigning the three existing rich dialogs' content; they may adopt
  the extracted shell, but their fields, copy and flows stay put.
- Any push, SMS or signup-mode change (rejected R7/R8).

### 4. Slices (in order; each lands and verifies on its own)

Slice R15-1 — lazy detail pages and a measured budget.

- First, on a clean tree: `cd web && npm run build && npm run bundle-size`
  and record the baseline in the PR (brief-time dist measured 176.3 kB;
  do not trust that number for the new budget).
- In `web/src/App.tsx`, delete the eager imports at `App.tsx:6`–`App.tsx:7`
  and add `GroupDetail`/`EventDetail` to the lazy block with the exact
  `.then((m) => ({ default: m.GroupDetail }))` shape used at
  `App.tsx:16`. `Home`, `Login`, `MyGroups` and `NotFound` stay eager:
  Home is the landing page, Login is the 401 destination, MyGroups is a
  bottom-tab root. Give the two routes their own labelled fallback like
  `App.tsx:98` ("Loading this group…", "Loading this event…", `Skeleton
  kind="page"`), so the shared outer fallback at `App.tsx:77` stops being
  the message for every slow chunk.
- Intent prefetch: expose the two dynamic `import()`s as named loaders
  from `App.tsx` (e.g. `loadGroupDetail`), and fire the matching loader
  once on `onPointerEnter`/`onFocus` of links that actually target
  those routes: the directory row link (`web/src/components/GroupCard.tsx`,
  to `/groups/:id`) and the Home church-event rows that fall back to
  `/events/:id` (`web/src/pages/Home.tsx:88`; rows with a `dateUrl` go
  to the already-lazy date page and are not this slice's business). A
  fired prefetch is fire-and-forget; failure is silent, the navigation
  itself retries through `Suspense`. No route prefetching on page load,
  no `<link rel="prefetch">`.
- Chunk-failure path: `lazy()` rejects when a redeploy retires an old
  hash while a tab is open. Confirm what the user sees today (there is
  no error boundary around `Suspense` at `App.tsx:77`); add
  `web/src/components/RouteErrorBoundary.tsx` (a class boundary, with
  its sibling test) around the `Routes` in `App.tsx`, offering
  `window.location.reload()` beside plain copy ("Fold was updated just
  now. Reload to get the new version."), laid out with `EmptyState` and
  a `primary` `Button`. This boundary is in scope because moving two
  more pages behind chunks widens exactly this failure.
- Rebuild, record the new eager total and the two lazy chunk sizes, and
  set `BUDGET_GZIP_KB` in `web/scripts/bundle-size.mjs:7` to the measured
  eager total rounded up to the next whole 5 kB (headroom for normal
  drift, never a guessed round number; the PR states both measurements).
  If the saving lands under 15 kB gzip, stop and report instead of
  tightening: something else is pulling the detail tree into the eager
  path (check `GroupCard`'s and `Layout`'s transitive imports first).

Slice R15-2 — the tiny cache in `api.ts`.

Files: `web/src/api.ts` (cache + opt-ins), `web/src/api.test.ts`,
`web/src/auth.tsx` (clear on login/logout), then deletion of the private
sharers in `web/src/lib/usePushEnabled.ts` and
`web/src/components/GroupDirectory.tsx` once the cache subsumes them.

- Contract (the whole design; do not grow it):
  - Only `GET` requests through `api()` (`api.ts:44`) may be cached, and
    only when the wrapper opts in. `authenticatedCSV` (`api.ts:83`),
    uploads, and every `POST`/`PUT`/`DELETE` never enter the cache.
  - Key: the full path including query string as built by the wrapper
    (so `/groups`, `/groups?search=men` and `/groups/3?cursor=…` are
    distinct entries). No normalization beyond what the wrappers already
    do at `api.ts:191`–`api.ts:205`.
  - Two mechanisms, independently switchable per opt-in: in-flight
    dedupe (a second identical call while the first is pending returns
    the same work, one `fetch`), and a TTL after success (`0` allows
    dedupe only). A failed request is never stored; its rejection is
    shared only while in flight.
  - Served values are `structuredClone`d on every hand-out. Callers
    keep these objects in component state and treat them as their own
    (Calendar already copies before sorting, `Calendar.tsx:76`); the
    cache must not become the one place where that ownership quietly
    stops being true. The clone cost on these payloads is not the
    bottleneck the slice exists for.
  - Mutations: any successful non-GET through `api()` clears the whole
    cache before returning. Coarse on purpose — prefix rules would need
    a second source of truth next to every wrapper, and at this app's
    size a full clear after a write is cheap. A mutation that fails
    clears nothing. Clearing also bumps an internal epoch counter: a
    GET that was already in flight when the write landed still answers
    its own waiters, but its result is never stored for the TTL —
    otherwise a slow pre-write read could repopulate the cache with
    pre-write data after the clear. The same epoch rule applies to the
    identity-change clears below.
  - Identity changes: export a clear function from `api.ts`; `logout`
    (`auth.tsx:97`) calls it alongside `clearClientData()`
    (`auth.tsx:105`), `login` (`auth.tsx:76`) calls it on success, and
    the 401 path inside `api()` (`api.ts:69`) clears before invoking
    `onUnauthorized` (a deduped 401 must trigger the handler once, not
    once per waiting caller; `anonymousOK` callers still just reject).
  - No persistence: nothing writes to browser storage, nothing survives
    the document, and the in-flight map holds data only as long as the
    TTL map would. The `no-store` headers server-side are untouched.
- Opt-in table for the first tranche (anything not listed is not
  cached; adding a row later is a one-line change with a test):

  | Wrapper | Path | Dedupe | TTL | Why |
  |---|---|---|---|---|
  | `Site.get` (`api.ts:295`) | `/site` | yes | session (until any mutation/identity change) | church facts change only through a write, which clears the cache |
  | `Groups.list("", [])` only (the no-arg form, `api.ts:191`) | `/groups` | yes | 30s | directory choices, Calendar group options, "meeting soon" overlap; search/tag variants keep fresh fetches |
  | `Groups.mine` (`api.ts:206`) | `/me/groups` | yes | 30s | tab root, revisits are common; membership writes clear it |
  | `Auth.me` (`api.ts:171`) | `/auth/me` | yes | 0 (dedupe only) | collapses overlapping `refresh()` calls; a settled answer is never reused |

- Then simplify the existing private sharers onto it: in
  `usePushEnabled.ts:6` delete the `shared` promise (the cache dedupes)
  but keep `known` — it gives later mounts the answer synchronously at
  first render (`usePushEnabled.ts:23`), which an async cache hit
  cannot; `known` is now set from the cached `Site.get()` result, and
  `forgetPushEnabled` keeps resetting it for tests
  (`usePushEnabled.ts:38`);
  delete the `allGroups`/`firstResults` hand-off in `GroupDirectory.tsx:44`
  and let the two effects (`GroupDirectory.tsx:65`, `GroupDirectory.tsx:73`)
  call `Groups.list()` plainly. `useSiteName` and `useEventZone` need no
  edit — they inherit the dedupe through `Site.get()`. Watch for tests
  that stub `Site.get` per mount and now see one shared answer
  (`web/src/lib/usePushEnabled.test.ts`, `web/src/lib/useSiteName.test.ts`):
  the clear function gets a test-visible name for suites to reset between
  cases, exactly the role `forgetPushEnabled` plays today.
- Explicitly out of the cache, with the reason in a comment at the opt-in
  table: notifications and the badge (polled for freshness,
  `web/src/lib/notifications.ts`), calendar lists (range- and
  person-specific, pagination cursors), `Groups.get(id, cursor)` pages
  (`api.ts:200`), people search (each keystroke is a new question, and
  results are capability-bearing `selectionId`s).

Slice R15-3 — shared filters for Calendar and the directory (backend
facet first).

- Backend (`api_groups.go`, `api.go`, `docs/API.md`): `apiListGroups`
  (`api_groups.go:29`) learns four optional params with Calendar's exact
  wire shape (`api.ts:341`): `day` (repeatable, `mon`..`sun` in the
  event's own `tzid`, wall-clock truth per AGENTS.md), `time`
  (`morning` before 12:00, `afternoon` 12:00–16:59, `evening` from
  17:00, by `starts_local`), `remote=1`, `childcare=1`. A group matches
  when at least one single occurrence the viewer may see, within the
  next 90 days from the church day, satisfies every chosen facet (days
  OR within `day`, buckets OR within `time`, facets AND across kinds —
  remote on one date and childcare on a different date is not a match
  for `remote=1&childcare=1`, because the person needs both on the day
  they attend). "Remote" and "childcare" mean the effective per-date
  facts the calendar candidates already carry (including a per-date
  zero limit turning remote off, `web/src/types.ts:531`), never the raw
  series flags.
  Implementation route: load viewer-visible date candidates the way
  other date lists do — `dateCandidates` (`api_event_access.go:650`)
  over a query restricted to events of directory-visible groups, each
  candidate occurrence passed through `visibleSet.canSeeDate`
  (`api_occurrence_visibility.go:18`) before its facets are read, and
  all filtering before any counting or limiting — then collect matching `group_id`s, intersect
  with `directoryQuery` (`api_groups.go:33`) before `viewerGroupList`
  (`api_groups.go:60`) orders the result. One batched candidate load,
  not a query per group. Invalid facet values are a `400` with a
  DEC-61 code created in this slice (`api_errors.go` + `ERROR_COPY`),
  not silent ignoring — a mistyped link should tell the person so.
  Church-wide events have `group_id` null and never make a group match.
  Cancelled and previously-started occurrences do not count; a series
  whose next visible date is beyond 90 days does not match, and the UI
  empty state says the window out loud (below).
- Client wrapper: `Groups.list` (`api.ts:191`) grows the four optional
  params. Note the cache interaction: only the bare `/groups` entry is
  cached (R15-2), so faceted calls stay fresh by construction. If R16
  has landed, `MeetingDay`/`DayPart` string unions live in the manual
  types file; otherwise add them to `web/src/types.ts` where R16's
  migration will pick them up.
- Shared hook, new file `web/src/lib/useFacets.ts` (+ sibling
  `useFacets.test.ts`): the one parser of the facet URL params (`day`,
  `time`, `remote`, `childcare`) over the caller's `URLSearchParams`,
  returning the parsed sets, `count`, a one-line `summary` ("Tuesdays ·
  Evenings · Childcare"), and patch builders (`toggle…` → the param
  changes to apply). It never navigates itself: each page applies the
  patch with its own history rule — the directory's `change(...)`
  replaces and drops `shown` (`GroupDirectory.tsx:128`), Calendar's
  `go(...)` pushes and drops `page` (`Calendar.tsx:111`). Calendar adopts
  the parser for its existing `remote`/`childcare` reads
  (`Calendar.tsx:38`) without changing Calendar's endpoint call or any
  param name; Calendar does not
  gain day/time filters in this item (its date navigation already covers
  "when"). The chip row is a shared presentational component,
  `web/src/components/FacetChips.tsx`, built only from `Chip`
  (`components/ui/Chip`): "Any day" + seven weekday chips, then
  Morning/Afternoon/Evening, then Remote seats and Childcare — the last
  two labelled and ordered exactly as Calendar has them
  (`Calendar.tsx:183`), including Calendar's rule that a selected chip
  never disappears (`Calendar.tsx:124`). Directory-specific rule: unlike
  Calendar, the directory always shows the Remote/Childcare chips — the
  directory has no loaded occurrence list to derive availability from.
- Directory UI (`web/src/components/GroupDirectory.tsx`): the filters
  button count (`GroupDirectory.tsx:167`) becomes tags + active facets;
  the panel (`GroupDirectory.tsx:179`) gains the facet section above the
  tag chips under its own labelled group; the selected-filters strip
  (`GroupDirectory.tsx:172`) shows facet chips alongside tag chips with
  the same remove-and-refocus behaviour; any filter change keeps going
  through `change(...)` (`GroupDirectory.tsx:128`) so `shown` resets and
  Back/restore keeps working (`GroupDirectory.tsx:93`). Empty states
  extend the existing matrix (`GroupDirectory.tsx:189`): facet-only
  wording names the window and offers the next step in the voice of the
  page — "No groups meet on Tuesday evenings in the next 90 days. Try
  another day, or clear the filters." — through `EmptyState`, never red.
  The result count line (`GroupDirectory.tsx:170`) is unchanged.
- My groups keeps its client-side text search only
  (`web/src/pages/MyGroups.tsx:90`); faceted browsing of one's own
  handful of groups is not a need this item has evidence for.

Slice R15-4 — `Dialog` shell + `ConfirmDialog`, then migrate.

- R15-4a, the shell. New `web/src/components/ui/Dialog.tsx` extracted
  from `SeriesRsvpDialog`/`RejectGroupDialog`: portal to `document.body`,
  everything behind set `inert` for the open lifetime, focus moved in on
  open and returned to the opener on close (with the
  opener-may-be-gone guard `RejectGroupDialog.tsx:40` already knows,
  plus an optional `fallbackFocus` like
  `GroupDepartureDialog.tsx:32`), Tab cycled inside the panel, Escape
  closes unless busy, overlay mousedown on the backdrop itself closes
  unless busy, `role="dialog" aria-modal="true" aria-labelledby` (and
  `aria-describedby` when a description is given), phone-bottom-sheet /
  desktop-centred panel classes copied from `SeriesRsvpDialog.tsx:51`,
  width capped so the 390px layout gate has nothing to clip. Props hold
  shell facts only (`onCancel`, `busy`, `initialFocus`, children); the
  shell knows nothing about confirming. `Dialog.test.tsx` sibling, in
  the house style of `web/src/components/SeriesRsvpDialog.test.tsx`,
  covers: inert behind, focus in/return, Escape, Tab wrap, busy blocks
  dismissal, labelled-by association.
- R15-4b, `ConfirmDialog` on the shell
  (`web/src/components/ui/ConfirmDialog.tsx` + test): props `title`,
  `body` (React node), `confirmLabel`, `cancelLabel`, `tone:
  "danger" | "normal"`, `onConfirm: () => void | Promise<void>`,
  `onCancel`. The dialog owns busy and error state around the
  `onConfirm` promise, exactly as `RejectGroupDialog` does today
  (`RejectGroupDialog.tsx:64`): while pending, both buttons disable and
  dismissal is blocked; a rejection renders its `errorText` in a
  `FormError` inside the dialog (callers pass the fallback sentence via
  `errorFallback`), the dialog stays open, and focus returns to the
  confirm button; only a resolved `onConfirm` closes it (the caller
  then unmounts or navigates). Title is a question in the page's voice; labels are the
  verb ("Delete group", "Remove Sam"), never "OK"/"Yes". Tone mapping
  is the DEC-63 rule, applied per site in migration and not left to
  taste: solid `danger` when the dialog's whole point is the destructive
  act (delete, revoke, remove, disable, discard, cancel a date or a
  registration); `primary` when the act is an override the person may
  legitimately take (add over capacity / over the member limit, remake
  a calendar link, decline dates, leave without saving). Initial focus:
  the cancel button for `danger`, the confirm button otherwise. One
  primary-coloured action per dialog; cancel is always `secondary`.
  Where "Cancel" as a button collides with the act's own name (cancelling
  a date, cancelling a message), the cancel label is "Keep this date" /
  "Keep editing" style copy, decided per site and recorded in the PR.
- Two call shapes, because the 39 sites split that way: declarative
  (`<ConfirmDialog>` rendered from local state — the default, and the
  shape for async sites where the dialog owns busy/error around the
  action) and a promise form for question-only sites, where the action
  runs in the caller after `ask` resolves `true` and the page's own
  busy/error handling stays where it already lives. The promise
  helper `useConfirm()` from the same file returning
  `{ ask(opts): Promise<boolean>, dialog }`, where `opts` carries the
  same title/body/labels/tone as the component, for the inline-guard
  sites (`if (!confirm(...)) return;`); `dialog` must be rendered by
  the caller, a second `ask` while one is open is a programming error
  the hook surfaces by resolving `false` and leaving the open dialog
  alone, and unmounting mid-ask resolves `false`. No app-wide provider: the dialogs at
  `unsavedChanges.tsx:58` and inside `ManageRoster`'s catch-retry loop
  (`ManageRoster.tsx:50`) are the proof a global singleton would be
  fought immediately. `NavigationGuard` is rewritten to render the
  promise dialog while blocked and call `blocker.proceed()`/`reset()`
  from its outcome; `beforeunload` at `unsavedChanges.tsx:36` stays as
  is, with the comment at `unsavedChanges.tsx:4` updated to name the
  dialog rather than "a confirm".
- Migration order (each a reviewable PR chunk, tests in the same chunk):
  1. Shell + `ConfirmDialog` + the four `alert(` sites moved to
     `ErrorNotice`/`FormError` in `GroupDetail.tsx`/`EventDetail.tsx`
     (pure replacement, easiest review).
  2. The sagas: `Admin.tsx` (7 sites), then `GroupDetail.tsx` and
     `EventDetail.tsx` remaining sites, then `MessageComposer.tsx` (4).
  3. Leaf cards in one pass: `CalendarFeedCard`, `RoomFeedPanel`,
     `AddPeople`, `AddRequestForm`, `ContactGrantCard`, `DateEditor`,
     `GuardianConsentCard`, `GuardianLinkCard`, `HouseholdCard`,
     `PushDeviceCard`, `TeenConsentPanel`, `Profile.tsx`,
     `GuestManage.tsx`, `EventDate.tsx`.
  4. The boolean-shaped sites last: `ManageRoster` (5), `RsvpButtons`
     (its `choose` path returns false on refusal,
     `RsvpButtons.tsx:106`), `unsavedChanges`.
  5. Adopt-or-not for the three rich modals: move `SeriesRsvpDialog`
     and `RejectGroupDialog` onto `Dialog` (they are the extraction
     source; their tests pin behaviour). Leave `GroupDepartureDialog`
     alone in this item — it is mid-flight safeguarding UI with its own
     focus choreography (`GroupDepartureDialog.tsx:75`); record the
     follow-up in the PR description instead of doing it silently.
- Guard so it sticks: when the count reaches zero, add a ratchet to
  `TestUIRatchets` (`guards_test.go:878`, counts in `ui-ratchet.json`)
  for `\b(window\.)?(confirm|alert)\(` in `web/src` production files,
  set to 0, with the failure text naming `ConfirmDialog`. Until then
  the count in `ui-ratchet.json` only ever goes down, PR by PR.

Slice R15-5 — Messages in the phone/tablet nav; signed-out Home leads
with the directory.

- Phone (`web/src/components/Layout.tsx:132`): when `canCompose`, the
  bottom bar gains a Messages tab (`MailIcon`,
  `web/src/components/icons/index.tsx:171`) between My groups and
  Notifications, making six tabs for composers and leaving five for
  everyone else. Tab styling is the existing `tab` class
  (`Layout.tsx:24`); at 390px six labels must still meet the layout
  gate's no-clipping and 44px-ish target rules — "My groups" is the
  widest label and the one to watch in screenshots.
- Tablet (`sm` to below `lg`, top nav at `Layout.tsx:82`): show Messages
  at every width and reclaim the room by hiding the "Home" top link in
  the `sm`–`lg` band only when Messages is actually rendered (that is,
  when `canCompose`); a non-composer's tablet nav is pixel-identical to
  today's. The church name above it is already the home
  link (`Layout.tsx:57`, aria-labelled at `Layout.tsx:59`), and the
  phone bottom bar keeps its Home tab. Do not shorten "My groups" or
  "New group" to buy width; the squeeze evidence at `Layout.tsx:86`
  names the church name as the thing that must not shrink, so the
  layout spec's minimum-name-width assertions stay untouched and get a
  768px composer case instead. Delete the stale comment at
  `Layout.tsx:86`–`Layout.tsx:88` and replace it with one line
  explaining the Home-link trade.
- Profile keeps its Messages entry (`web/src/pages/Profile.tsx:92`) but
  drops the `lg:hidden` reason to exist: it becomes an ordinary entry
  in the Me list at every width (a second way in is consistent with how
  other Me entries duplicate top-nav destinations).
- Home (`web/src/pages/Home.tsx`), signed-out branch only: hero keeps
  the `h1` and church welcome (`Home.tsx:62`) but compacts — one action
  row in which "Browse groups" (the existing jump mechanism,
  `Home.tsx:69`, which scrolls and focuses the directory heading) is
  the primary button, "Browse public events" stays secondary
  (`Home.tsx:68`), and "Request an account" moves out of the hero to a
  short closing section after the directory ("New here? Request an
  account to join a group.", primary there — it is that section's whole
  point; omitted entirely when `selfSignup` is `off`). `GroupDirectory`
  moves directly under the hero; the church-events preview
  (`Home.tsx:79`) moves below the directory, keeping its "See all
  church events" link (`Home.tsx:83`) and its ready gating
  (`Home.tsx:45`) so the directory's scroll-restore math
  (`GroupDirectory.tsx:93`) still measures against settled content.
  The signed-in branch (`Home.tsx:53`) and `HomeScreen` sign-in
  redirect (`App.tsx:69`) do not change. One primary action per view
  stays true on first paint: the desktop viewport shows exactly one
  `primary` control above the directory.

### 5. Tests

Per slice (each next to the code it tests; fake timers/`fetch` stubs in
the existing `api.test.ts` style, `web/src/api.test.ts:4`):

- R15-1: evidence is the two recorded `bundle-size` runs
  (before/after) in the PR plus `RouteErrorBoundary.test.tsx` beside
  the boundary (render a throwing lazy child, assert the reload
  offer; render a healthy child, assert passthrough). e2e: `web/e2e/smoke.spec.ts` and
  `web/e2e/deeplink.spec.ts` already deep-link `/groups/:id` and
  `/events/:id` — they must pass unchanged, proving the lazy routes
  resolve cold; add one case to `web/e2e/nav-shell.spec.ts` that loads
  the group page with its chunk delayed and sees the labelled skeleton,
  then the page. a11y/layout: `group-member`, `group-leader` and `event`
  pages in `web/e2e/ui-pages.ts` at 390px unchanged. Bundle gate runs.
- R15-2 (`web/src/api.test.ts`): two concurrent `Site.get()` calls cause
  one `fetch`; a third after resolution still causes one (session TTL);
  a mutating wrapper's success makes the next `Site.get()` fetch again;
  a mutation failure does not; `Auth.me()` twice concurrently = one
  fetch, twice after settlement = two; `Groups.list()` inside 30s (fake
  timers) = one fetch, after expiry = two; `Groups.list("men")` is
  never deduped across param changes; a 500 is not cached (retry
  fetches); a deduped 401 fires `onUnauthorized` exactly once and
  clears the cache; a caller mutating its handed-out object does not
  change the next caller's value; a GET held in flight across a
  successful mutation (deferred `fetch` stub) answers its waiter but
  the next identical call fetches again (the epoch rule). Existing 401 test
  (`api.test.ts:10`) stays green. Page-level suites that stub `fetch`
  and count calls are the regression net; `GroupDirectory.test.tsx`
  keeps asserting fresh results for search/tag queries. e2e:
  `web/e2e/session.spec.ts` (logout leaves nothing) and
  `web/e2e/returns.spec.ts` unchanged; add one `session.spec.ts` case:
  sign in, open the directory, sign out, sign in as someone else, and
  the directory shows their roles, not the previous person's.
- R15-3: Go — extend `api_groups_test.go` beside
  `TestGroupListCarriesNextEventAt` (`api_groups_test.go:880`):
  day/time/remote/childcare matches and non-matches, facets AND'd across
  kinds and OR'd inside `day`/`time`, a group whose only Tuesday date
  carries a `visibility_override` the viewer may not see does not match
  `day=tue` (the privacy gate, the most important test in the slice),
  an invite-only group's dates never match for outsiders, cancelled and
  past dates do not count, the 90-day edge (day 90 yes, day 91 no),
  church-wide events ignored, invalid facet = 400 with the new code.
  Web — `useFacets.test.ts` (round-trip params incl. repeated `day`,
  count, summary text), `GroupDirectory.test.tsx` cases (facet chips
  write params and refetch with them, empty-state facet wording, filter
  count), `Calendar.test.tsx` green unchanged after hook adoption.
  e2e — one spec addition to `web/e2e/tags.spec.ts` or a new
  `web/e2e/directory-filters.spec.ts`: seed a Tuesday-evening group with
  childcare and a Wednesday-morning one without, filter to Tuesdays +
  evenings, see only the first, share the URL into a fresh page and see
  the same. a11y/layout 390px: home pages in `ui-pages.ts` with the
  filters panel opened via `prepare`.
- R15-4: `Dialog.test.tsx` and `ConfirmDialog.test.tsx` as listed in the
  slice; each migrated page's existing suite is rewritten from
  `window.confirm` spies to role queries ("dialog" role, button by its
  verb label) in the same commit as its page; `unsavedChanges.test.tsx`
  covers proceed/reset from the dialog. e2e: the specs that install
  `page.on("dialog", …)` handlers lose the handler per page as it
  migrates (accept instead with
  `getByRole("dialog")` + button text; keep asserting the message text
  where a spec already does, e.g. `exact-date.spec.ts:200`). a11y at
  390px: add a `prepare`-opened `ConfirmDialog` to one `ui-pages.ts`
  page so axe covers the open dialog; layout spec gets the bottom-sheet
  width assertion for free through the same case. Ratchet: lowering
  `ui-ratchet.json` is part of every migration PR;
  `TestUIRatchetsCatchPastMistakes` (`guards_test.go:912`) keeps guard
  honesty.
- R15-5: `Layout.test.tsx` (`web/src/components/Layout.test.tsx`) gains
  composer/non-composer tab assertions and the `sm`–`lg` Home-link
  absence; `Home.test.tsx` signed-out cases assert order (directory
  heading before "Church events", account request once, absent when
  `selfSignup` is `off`). e2e: `nav-shell.spec.ts` at 390 (six tabs for a
  leader, five for a member, Messages tab lands on `/messages`), 768
  (Messages in the top nav for a leader; church-name width assertions
  untouched) and `layout.spec.ts` across widths; screenshots at
  390/768/1280 for `home-signed-out` and the leader shell.
- Every slice: full quick checks from the shared rules, `make test`
  when Go moved (R15-3, R15-4's ratchet), and `make e2e` where user
  flows changed (R15-3, R15-4 chunks, R15-5).

### 6. Acceptance checklist

- [ ] `npm run build && npm run bundle-size` passes with the new,
      measured `BUDGET_GZIP_KB`; before/after numbers and the two lazy
      chunk sizes are in the PR. `/groups/:id` and `/events/:id` open
      correctly on a cold load (deep link), on Back, and after a chunk
      404 the person is offered a reload instead of a blank page.
- [ ] Loading Home issues `GET /api/v1/site` once; switching
      Home → Calendar → My groups reuses the cached reads inside their
      TTL (assert fetch counts in `api.test.ts`); any write refetches.
- [ ] After logout or a 401, no cached read from the previous session
      is servable (`session.spec.ts` case above); nothing cache-related
      is written to browser storage.
- [ ] `GET /groups?day=tue&time=evening&childcare=1` returns only groups
      with a viewer-visible matching date in the next 90 days; hidden
      dates never produce a match; bad facet values answer 400 with
      wording the UI owns. A filtered directory URL restores exactly
      (reload and shared link).
- [ ] `rg '\b(window\.)?(confirm|alert)\(' web/src` finds zero
      production sites; the ratchet is 0. Every former confirmation is
      reachable by keyboard, labelled for a screen reader, dismissable
      with Escape, and returns focus to its opener (axe clean at 390px
      with a dialog open). Errors from the four `alert` sites appear on
      the page, with retry where the page already has one.
- [ ] A leader on a 390px phone gets a Messages tab; on a 768px tablet
      Messages sits in the top nav and the church name is not squeezed
      (layout spec green); a plain member's nav is unchanged everywhere.
- [ ] Signed-out Home: directory before church events, one primary
      action in the hero, account request exactly once (closing section)
      and absent when `selfSignup` is `off`.
- [ ] `go vet`/`make lint-go`, `tsc --noEmit`, `knip`, Vitest, e2e
      a11y+layout at 390px and the bundle gate all pass per slice.

### 7. Risks, rollback

- **Stale-read risk is the cache's whole danger.** Mitigations are
  structural, not carefulness: allowlist of four wrappers, whole-cache
  clear on any successful write, zero TTL on `/auth/me`, cloned
  hand-outs, clear on identity change. The failure mode to watch in
  review is a write path that does not go through `api()` (uploads and
  multipart posts live elsewhere in `web/src/api.ts` and sibling files
  such as `web/src/messages-api.ts`, which wraps the same `api()`) —
  grep for raw `fetch(` in `web/src` in the R15-2 PR and either route
  hits through `api()` or have them call the clear function explicitly.
  Rollback of R15-2 is removing the four opt-ins (the cache becomes
  unreachable; the hand-sharers were deleted against it, so roll back
  the whole PR, not parts).
- **Privacy is the facet filter's danger.** A match bit derived from a
  date the viewer may not see leaks that a hidden series meets on a
  given weekday with childcare. The slice's Go test for the overridden
  date is the control; if the candidate-loading route cannot be made
  to filter before aggregating without leaning on the series-level
  visibility, stop and raise it rather than approximating with
  `groupSummaries`' aggregate shape (`api_events.go:473`).
- **Chunk-rot after deploy** hits more pages once detail routes are
  lazy (R15-1's error boundary). Assets are `immutable`-cached
  (`main.go:423`) while the shell is `no-store` (`main.go:413`), so a
  long-open tab after an upgrade is the exact case; the reload offer
  must not appear for an ordinary 500 from an API call (boundary wraps
  route rendering only).
- **Dialog migration** changes timing: native `confirm` is synchronous
  and several handlers assume it (notably `RsvpButtons.tsx:106`'s
  boolean return and the router blocker). The promise helper keeps the
  shape, but a handler that also disables controls must now span the
  await; the per-page test rewrite is the control, and the migration
  order puts the boolean-shaped sites last. Rollback per chunk is
  ordinary revert — slices are ordered so no chunk needs another to
  make sense.
- **Nav change** can regress the church-name squeeze that caused the
  `lg:` gate (`Layout.tsx:86`). The 768px layout assertions are the
  control; if hiding Home in-band is judged a discoverability loss,
  the slice's fallback is Messages as an icon-only top-nav item in that
  band (`MailIcon` + visually-hidden text), not label shortening.
- **Home reorder** can shift the directory's scroll-restore offsets
  (they depend on settled content above, `GroupDirectory.tsx:93`);
  `returns.spec.ts` is the control. A church with `selfSignup=off`
  plus no groups gets `EmptyState` copy that must not point at the
  removed account link.
- Feature-level rollback is per slice; no schema, no URL (other than
  new optional `/groups` params, additive), and no stored state are
  involved, so any slice reverts cleanly on its own.

### 8. Dependencies

- Slices run in the written order. R15-2 before R15-3 (the facet slice
  relies on faceted `/groups` calls staying uncached beside the cached
  bare one). R15-4 is independent and may run in parallel with R15-2/3
  if separate agents take them; they share no files except
  `GroupDetail.tsx`/`EventDetail.tsx` (coordinate the error-surface
  chunk with R15-1's route split — same files, no shared lines).
- R16 (below) is independent but touches `web/src/api.ts`'s import list
  (`api.ts:103`) and `web/src/types.ts`; if both land in the same
  period, R15-3's new unions go wherever R16 has got to (noted in the
  slice), and R16's generated-file flip should not be scheduled inside
  an R15 migration PR.
- No migration, no config, no environment variable is needed anywhere
  in R15; the bundle budget change rides the ordinary release build
  (`npm run build` before `go build`, `web/package.json:32`).

### 9. Open questions for Grier (each with a default that unblocks work)

1. Budget headroom after re-measuring: measured eager total rounded up
   to the next 5 kB. Recommended default: as written in R15-1. A
   tighter rule (measured + 2 kB) makes normal page growth trip the
   gate weekly; a looser one (+10 kB) repeats the 140 → 180 drift
   (`bundle-size.mjs:6`).
2. How long is `/site` good for? Recommended default: the session —
   it changes only via writes, and every write clears the cache. The
   one gap: an admin flipping rooms, notifications on devices or
   signup in another tab leaves this tab showing the old switch until
   its own next write or a reload. A 5-minute TTL bounds that at the
   cost of one tiny read per five minutes per open tab.
3. Facet window and buckets: next 90 days, morning/afternoon/evening
   cut at 12:00 and 17:00 by the event's wall clock. Recommended
   default: as written (90 days matches the signed-out Home preview
   horizon, `Home.tsx:85`). A longer window flatters groups that seat
   into next season; the empty state must state whichever window ships.
4. Tablet nav trade: hide the "Home" top link between `sm` and `lg` to
   seat Messages. Recommended default: as written (the church name is
   Home). Alternative: icon-only Messages in that band; never squeeze
   the church name — that regression is documented at `Layout.tsx:86`.
5. Does the signed-out closing "Request an account" section belong on a
   church whose groups are mostly private/invite-only (the directory a
   visitor sees is nearly empty)? Recommended default: keep it — with
   `selfSignup=off` it already disappears, and a thin public directory
   is the church's own visibility choice, not a reason to restore the
   paperwork-first hero.

---

## R16 — Generate `web/src/types.ts` from the Go structs

- Priority: P2 (audit `docs/AUDIT-2026-11-01.md:221`), effort M.
- Status: Approved 2026-11-01 (audit). The audit allowed either full
  generation or auto-discovery for the guard; this brief does generation
  and shrinks the guard to auto-discovery alongside it, in that order.
- GitHub issue: none known at brief time. The guard this replaces was
  added after a Go JSON rename missed the hand-written types and broke
  the web tests post-merge (recorded at `guards_test.go:63`).

### 1. Goal

One source of truth for the `/api/v1` JSON shapes the SPA renders: the Go
`*JSON` structs. A Go change to a field's name, JSON type, optionality or
closed set of values either regenerates the TypeScript and shows up as a
`tsc` error at every call site that disagrees, or fails CI for a stale
generated file — never passes green and breaks a page at runtime. The
hand-maintained pair list and the open-ended `string` fields that stand
for closed role/status sets go away; nobody edits a type in two
languages for one API change again.

### 2. Current state (evidence)

**Three sources, one name-only check.**

- `web/src/types.ts` is 1365 lines holding 118 exported interfaces and
  type aliases ("TypeScript mirrors of the /api/v1 JSON contract",
  `web/src/types.ts:1`), imported across the SPA (the biggest consumer's
  import list is `web/src/api.ts:103`–`api.ts:168`).
- The guard is `tsContracts`, a hand-kept list of exactly 100
  `{GoStruct, "TSName"}` pairs (`guards_test.go:66`–`guards_test.go:170`),
  checked by `TestWebTypesMatchGoJSON` (`guards_test.go:219`). The check
  compares field-name sets only: Go names come from reflection over the
  `json` tags (`goFields`, `guards_test.go:192`), TS names from a line
  regex over the interface body (`tsFieldRe`, `guards_test.go:172`,
  `tsFields` at `guards_test.go:174`). Changing `int` to `string`,
  `*int` to `int`, or dropping `omitempty` in Go passes the guard and
  `tsc` alike, then misrenders. Nested inline objects are invisible to
  it: `SoleLedGroup.candidates: { id, name, admin }[]` (`types.ts:421`)
  is hand-inlined and the regex sees only the outer field.
- Coverage has already drifted past the list. There are 96 non-test
  Go types named `*JSON` in the root package
  (`rg 'type \w+JSON struct' api*.go reminders.go messages_audience.go
  registration_subjects.go --glob '!*_test.go'`); 93 appear in
  `tsContracts` and 3 do not: `emailSettingsJSON`
  (`api_email_settings.go:29`, no TS interface at all — its endpoint is
  consumed without a named shape), `soleLedGroupJSON`
  (`api_handoff.go:28`, TS mirror at `types.ts:421` but never paired)
  and its nested `successorJSON` (`api_handoff.go:22`, no named TS
  type). Conversely 7 pairs guard structs that are not `*JSON`-named,
  so a naive `*JSON` auto-discovery would silently drop them:
  `seriesRegistrationState` (`api_series_registrations.go:39`),
  `seriesRegistrationResult` (`api_series_registrations.go:49`),
  `seriesRegistrationRefusal` (`api_series_registrations.go:56`),
  `seriesRegistrationResponse` (`api_series_registrations.go:62`),
  `householdAnswer` (`api_registrations.go:880`),
  `registrationBatchInput` (`api_registrations.go:933`, a *request*
  body — the client composes `RegistrationCommand` from it), and
  `renderedMessage` (`api_message_preview.go:57`, which carries two
  `json:"-"` fields, `api_message_preview.go:59`, that must never
  generate).
- 18 exports in `types.ts` have no pair at all. They split into closed
  string unions kept by hand (`Visibility` `types.ts:67`,
  `AttendanceMode` `types.ts:71`, `VideoProvider`/`VideoReveal`
  `types.ts:75`–`types.ts:76`, `RegistrationStatus` `types.ts:173`,
  `AgeBand` `types.ts:463`, `AccountRequestStatus` `types.ts:484`,
  `OpenedDecision` `types.ts:448`, `AudienceStatus`/`AudienceRole`
  `types.ts:1116`, `HistoryStatus` `types.ts:1266`, `ReminderKind`
  `types.ts:1196`, client-only `NotificationFilter` `types.ts:164`),
  one nested struct that lives in `models` rather than beside its API
  struct (`RequestWarnings` `types.ts:487` mirrors
  `models.RequestFlags`, `models/models.go:644`, reached through
  `accountRequestJSON.Warnings` at `api_account_requests.go:93`), and
  envelope wrappers that exist only as `gin.H` literals server-side
  (`EventDetailResponse` `types.ts:378`, `NotificationFeed`
  `types.ts:166`, `DepartureConfirmationResponse` `types.ts:131`,
  and by contrast `GroupDetailResponse` *does* have a struct,
  `groupDetailJSON` at `api_groups.go:191`). Envelopes are numerous —
  about 170 one-line `c.JSON(…, gin.H{…})` sites across `api*.go` at
  brief time, more in multi-line form — and the generator deliberately
  does not chase them (scope, below).
- Role/status fields are open today even where the server set is
  closed: `User.role: string` (`types.ts:24`) beside the closed
  `User.status` union (`types.ts:25`); `Member.role: "leader" | "member"
  | string` (`types.ts:87`), a union widened back into `string` by its
  own last arm; `GroupSummary.status: string` (`types.ts:55`);
  `GuestRegistration.status: string` (`types.ts:186`, whose real set is
  the three values named in the comment at `types.ts:175`);
  `Notification.kind: string` (`types.ts:140`). The server values are
  constants in `models/models.go`: account roles (`models.go:12`),
  membership roles (`models.go:18`), group statuses (`models.go:29`),
  visibilities (`models.go:37`), registration statuses
  (`models.go:51`), attendance modes (`models.go:71`), video values
  (`models.go:78`), account statuses (`models.go:102`), account types
  (`models.go:111`), request sources/statuses (`models.go:117`). The
  Go structs themselves type every one of these as plain `string`
  (e.g. `userJSON.Role` at `api.go:62`, `Status` at `api.go:63`;
  `occurrenceJSON.Visibility` at `api.go:192`, whose closed set is only
  a comment on the sibling at `api.go:123`), so no type-driven
  generator can recover the unions from the field types alone — the
  union mapping has to be stated once, in generator config, and the
  values must be read out of `models` rather than copied.

**What the generator must handle (survey of the 96 + 7).**

- Scalars: `string`, `bool`, `int`/`int64`/`uint`/`float64` (e.g.
  `api.go:54`–`api.go:61`), present everywhere.
- Time: `time.Time` (e.g. `emailSuppressionJSON.Since`, `api.go:83`;
  `occurrenceJSON.StartsAt`, `api.go:174`) and `*time.Time`
  (`groupSummaryJSON.NextEventAt`, `api.go:120`), all serialised as
  RFC 3339 strings (`docs/API.md` states the timestamp format).
- Pointers with two distinct wire meanings: `*T` without `omitempty`
  serialises a null (`memberLimit`, `api.go:126`; `myRegistration`,
  `api.go:191`), while `*T` with `omitempty` disappears when nil
  (`emailSuppression`, `api.go:76`; `subjectDecidedAt`,
  `api_contact.go:97`). Plain values with `omitempty` disappear at the
  zero value (`birthDate`, `api.go:66`; `avatarUrl`, `api.go:41`).
- Collections: `[]string` (`tags`, `api.go:113`), slices of other
  `*JSON` structs (`attendees`, `api.go:203`; `candidates`,
  `api_handoff.go:31`), `map[string]int64` (`registrations`,
  `api.go:190`), `map[string]int` (`skipped`, `messages_audience.go:271`;
  `missed`, `api_message_history.go:60`). A nil slice serialises as
  `null`, which exactly one hand type records today:
  `GroupDetailResponse.members: Member[] | null` (`types.ts:296`)
  against `Members []memberJSON` (`api_groups.go:194`). At brief time
  that `| null` is already wrong: the handler seeds `memberList` with
  an empty slice (`api_groups.go:246`) and `rosterFor` builds its
  result with `make` (`api_groups.go:281`), so `members` is always a
  list on the wire, while the group page keeps paying for the phantom
  null at `web/src/pages/GroupDetail.tsx:561` and
  `GroupDetail.tsx:1012`. It is the worked example for the flip's
  adjudication step (slice R16-3).
- Cross-package and excluded shapes: `models.RequestFlags`
  (`api_account_requests.go:93`) is the one selector-typed field beside
  `time.Time`; unexported and `json:"-"` fields exist inside generated
  candidates (`occurrenceJSON.hideSeries`, `api.go:166`;
  `renderedMessage`, `api_message_preview.go:59`) and must be skipped.
  `json.RawMessage` appears only in request-body structs outside the
  set (`api_events.go:524`, `api_groups.go:120`); anonymous embedded
  fields do not occur in the set at brief time (verify in the first
  slice and then fail closed on them, rather than guessing a flattening
  rule no caller needs).

**Tooling facts.**

- Tooling is shell or Go (`Makefile:1`); there is no `go:generate`
  anywhere and no `cmd/` directory. The shipped binary is the root
  package only (`Dockerfile:18`, `go build .`), so a generator under a
  new `cmd/` cannot leak into the artifact. The guards already parse Go
  source with the standard library (`go/ast`, `go/parser`, `go/token`
  imports at `guards_test.go:6`) and use reflection where a live value
  exists (`guards_test.go:11`) — precedent for both generator designs.
- `go.mod` pins the toolchain (`go 1.26.0` / `go1.27.3`); nothing in the
  module today needs `golang.org/x/tools` at build time (it is invoked
  version-pinned via `go run` for lint/deadcode, `Makefile:50`,
  `Makefile:56`), and this item adds no dependency in any case.
- Web build order: `npm run build` is `tsc --noEmit && vite build`
  (`web/package.json:32`); `knip` audits `src/**/*.{ts,tsx}` for orphan
  files (`web/knip.json`), which an unused alongside-generated file
  would trip (migration slice handles this explicitly). CI's `test`
  job already runs Go lint, `make test`, then the web build and bundle
  gate (`.github/workflows/test.yml:40`–`.github/workflows/test.yml:51`),
  and `make lint-go` runs `go vet`, `gofmt` and staticcheck
  (`Makefile:47`), with the `deadcode` gate beside it (`Makefile:54`).

### 3. Scope

In:

- A Go generator (standard library only) that emits the interfaces for
  every `*JSON` response struct in the root package plus the 7 named
  non-`*JSON` pairs, with the field mapping, pointer/`omitempty`,
  `time.Time`, slice/map and cross-reference rules in slice R16-1, and
  closed unions whose *values* are parsed from `models` constants.
- Deterministic output with a generated-file header, committed to the
  repo, checked in CI by regenerating and diffing; a `Makefile` target
  to regenerate locally.
- The guard shrinks to auto-discovery: every new `*JSON` struct is
  covered the day it is written, and the 100-row hand list disappears.
- Closing the open role/status strings named in section 2 to unions,
  fixing the `tsc` fallout at call sites by narrowing code, never by
  casting through `string`.
- The hand section's unavoidable remainder (envelopes, client-only
  types) moves to a small, clearly-named manual file; the generated
  file ends up as `web/src/types.ts` itself.

Out:

- Generating anything from `gin.H` envelope literals, request-body
  structs other than the one already paired
  (`registrationBatchInput`), GORM models, or `docs/API.md`. Envelopes
  like `{groups: [...]}` keep their inline generics at the wrapper
  (`api.ts:198`) and their few named shapes stay in the manual file.
- Changing any wire shape, field name, or server value to make
  generation easier. Where the Go and TS sides disagree today (the
  flip's diff will list them), the Go side is the behaviour the server
  actually has; changing behaviour is a separate decision, raised in
  the flip PR, never smuggled in.
- Runtime validation of API responses in the browser (types are erased;
  the server stays the authority), OpenAPI/spec generation, and any
  TypeScript-side code generator, formatter-as-dependency (the
  generator emits final formatting itself), or npm package.
- Typing Go role/status fields as named Go string types to make unions
  inferable. That is a Go-wide refactor touching `internal/policy` and
  every comparison site; the generator's config table states the
  mapping once instead (open question 2 records the trade).

### 4. Slices (in order; each lands and verifies on its own)

Slice R16-1 — the generator, unused by the app.

Decision, decided here so the implementer does not re-litigate it:
**AST-walking with `go/parser`/`go/ast`, not reflection and not
`go/types`.** Reflection cannot enumerate the unexported types of
`package main` from outside it, and a generator compiled *into* the app
(or run as a test that writes files) would either ship in the binary or
make `make test` mutate the tree. `go/types` would resolve cross-package
fields for free but needs full type-checking setup for the whole root
package to answer questions the syntax already answers (every field in
the set is written as a bare identifier, a `*`, a `[]`, a `map[string]…`
or one of two selector forms). The guards already walk this AST
successfully (`guards_test.go:6`). If the first fixture sweep finds a
shape the syntax cannot decide, that shape is reported back against
this decision rather than handled by quietly switching engines.

Layout: logic and the exported config (rename table, `extra` list,
union table — slice R16-4's guard imports it, so config is data in the
package, not literals buried in `main`) in `internal/tsgen` (package
`tsgen`, table-tested against inline Go source fixtures), a thin `cmd/tsgen/main.go` whose
only job is flags and file IO: `-out <file>` writes there, no flag
writes to stdout. Invoke as `go run ./cmd/tsgen` from the repo root.
`gofmt`/`go vet`/staticcheck
cover it through `make lint-go`; confirm the `deadcode` gate
(`Makefile:54`) accepts `cmd/tsgen` as a root rather than flagging its
package (adjust `.deadcode-allow` minimally only if the tool demands,
and say so in the PR).

Input set, discovered not listed: parse every non-test `*.go` file in
the repo root (the `package main` files), collect every top-level
struct type whose name ends in `JSON`, plus an explicit `extra` list in
generator config carrying the 7 non-`*JSON` structs from section 2
(each with a one-line comment saying why it is in the client contract;
`registrationBatchInput`'s line says "request body the client
composes"). Parse `models/models.go` for its `const` blocks (values
for unions) and for `RequestFlags`-style referenced structs: a
`models.X` selector field resolves into `models` and generates an
interface for `X` too, under its config-given TS name
(`RequestWarnings`). Any other selector package, any embedded field,
any field whose type expression is not in the mapping table below is a
hard error naming file and line — fail closed, never emit `any`.

Naming: a rename table in config, seeded from all 100 existing
`tsContracts` pairs (`guards_test.go:66`) so not one imported TS name
changes in the flip (e.g. `occurrenceJSON → EventOccurrence`,
`eventJSON → EventSeries`, `hostJSON → EventHostRow`,
`renderedMessage → MessagePreview`). New structs default to: strip the
`JSON` suffix, capitalise the first letter (`successorJSON →
Successor`); a name collision after defaults is a generator error, not
a silent overwrite. Output order is the rename-table order for seeded
names, then alphabetical for the rest, so diffs stay local.

Field mapping (complete; anything outside errors):

| Go shape | TypeScript |
|---|---|
| `string` (no union configured) | `string` |
| `string` with a configured union (below) | the union type name |
| `bool` | `boolean` |
| `int`, `int64`, `uint`, `uint64`, `float64` | `number` |
| `time.Time` | `string` (RFC 3339 on the wire) |
| other named struct in the set (incl. resolved `models.X`) | its generated interface name |
| `[]T` | `T[]`; `[][]T` nested likewise |
| `map[string]T` | `Record<string, T>` |
| `*T` without `omitempty` | `field: T \| null` |
| `*T` with `omitempty`, or plain `T` with `omitempty` | `field?: T` (absent, never null, when unset) |
| exported field tagged `json:"-"`, or any unexported field | skipped |
| exported field with no `json` tag | generator error naming the field (`hideSeries` at `api.go:166` carries its `json:"-"`; keep that discipline — silence is how a field leaks into the contract unreviewed) |
| config-listed nullable slice (mechanism only; the list starts empty — slice R16-3) | `T[] \| null` |

Unions: config maps `StructName.FieldName → {tsName, valuesFrom}` where
`valuesFrom` is either a named `models` const block (values extracted
by the generator from the parsed `models/models.go`, in source order —
touching `models` then changes the generated file and CI notices) or an
explicit literal list for deliberate subsets (each such entry needs a
comment citing where the server restricts the set). Seed it with, at
minimum: visibilities (`models.go:37`) for every `visibility` /
`*Visibility*` field; account roles (`models.go:12`) for
`userJSON.role`; membership roles (`models.go:18`) for `memberJSON.role`
and `groupSummaryJSON.myRole`; group statuses (`models.go:29`);
account statuses (`models.go:102`) and types (`models.go:111`);
registration statuses (`models.go:51`) as `RegistrationStatus`, with
`guestRegistrationJSON.status` the documented subset
`"going" | "waitlisted" | "cancelled"` (`types.ts:175`); attendance
modes (`models.go:71`); video provider/reveal (`models.go:78`); request
statuses (`models.go:125`) and the existing hand unions
(`AccountRequestStatus`, `AgeBand`, `OpenedDecision`, `AudienceStatus`,
`AudienceRole`, `HistoryStatus`, `ReminderKind`) wherever their home
struct fields can be found — fields behind them that live only in
envelopes keep the union in the manual file and the generator does not
emit a duplicate. `Notification.kind` (`types.ts:140`) stays `string`:
kinds are an open registry (`internal/notify/kinds.go`), and closing it
would make every new notice kind a breaking type event for old tabs.
Emitted union declarations sit at the top of the generated file.

Emission format is fixed by the generator, in the style `types.ts`
already has (2-space indent, semicolons, `export interface`, field
order = Go field order, a trailing newline; no prettier step exists to
borrow — `web/package.json` has none). The file header, exactly:

```ts
// Code generated by cmd/tsgen from the Go *JSON structs. DO NOT EDIT.
// Regenerate with: make types
```

Comments from Go fields are not copied (they would drift into a second
documentation source; `docs/API.md` remains the prose contract). The
generator is byte-deterministic: two runs on the same tree produce the
same bytes, proven by a test, not by hope.

Slice R16-2 — generate alongside and review the diff.

- Generate to `web/src/types.generated.ts` (same header) and commit it,
  unused by the app. `knip` (`web/knip.json`) will call it an orphan:
  add an entry-ignore for that one path in `web/knip.json` with a
  comment-free config change whose reason lives in the PR, and remove
  the ignore at the flip.
- The PR's substance is the diff review, done with a scratch comparison
  the implementer runs and pastes (reuse the guard's field-set idea):
  per type, fields generated-vs-hand — name, JSON type, `?` vs
  `| null`. Every disagreement is adjudicated in the PR description
  into exactly one bucket: (a) hand type was wrong (fix `types.ts` in
  this slice, with the page impact checked — `members: Member[] | null`
  is the known first resident, section 2), (b) generator rule was
  wrong (fix the rule), (c) deliberate difference (record it as a
  config override with its reason). The hand file is still the live one, so (a)
  fixes are behaviour-visible today and get the page tests they touch.
- The three uncovered structs surface here: `Successor` appears as its
  own interface, `SoleLedGroup.candidates` stops being inline,
  `EmailSettings` gets its interface and its consuming wrapper in
  `web/src/api.ts` gets the named return type at the flip.

Slice R16-3 — the flip.

- `web/src/types.ts` becomes the generated file (the generator's `-out`
  target changes; `types.generated.ts` is deleted and its knip ignore
  removed). Everything in today's `types.ts` that generation does not
  produce moves, untouched in shape, to `web/src/types-manual.ts`:
  envelope wrappers (`EventDetailResponse`, `NotificationFeed`,
  `DepartureConfirmationResponse`), client-only types
  (`NotificationFilter`), and any union whose field lives only in an
  envelope. `types.ts` (generated) re-exports the manual names through
  one closing block the generator always emits
  (`export type { … } from "./types-manual";`, the name list itself
  kept in config) so the SPA's ~118-name import surface does not move
  and no caller's import line changes. `types-manual.ts` imports
  generated names from `./types`; the re-export pair must not create a
  value cycle (type-only on both sides — `tsc` and `knip` prove it).
- The one knowable asymmetry is decided here with eyes open:
  `members: Member[] | null` (`types.ts:296`). The server always sends
  a list — `memberList` starts as an empty slice (`api_groups.go:246`)
  and `rosterFor` returns a `make`-built slice (`api_groups.go:281`) —
  so the generated `Member[]` stands, the nullable-slice config list
  starts *empty*, and the flip deletes the page's dead null handling
  (`members ?? []` at `web/src/pages/GroupDetail.tsx:561` and
  `GroupDetail.tsx:1012`), with an `api_groups_test.go` case asserting
  `"members":[]` (not `null`) for a viewer who may not see the roster,
  so a future nil-producing refactor fails in Go rather than in the
  browser. Do not widen every slice to `T[] | null` to dodge such
  questions — that taxes all readers for one field.
- Closed unions now bind: remove the `| string` arm from member roles,
  close `User.role`, `GroupSummary.status`, guest statuses. `tsc`
  errors are the work queue; fix each by narrowing the consumer
  (exhaustive switch, comparison against the union's values), never by
  `as string` casts or by re-widening config silently — a widening
  needs its reason written into the config comment so the diff review
  in slice R16-2 remains the record.
- Wire the permanence: a `make types` target (`.PHONY` list,
  `Makefile:6`; shell only, `go run ./cmd/tsgen -out
  web/src/types.ts`), and a CI step in the `test` job right after
  `make lint-go` (`.github/workflows/test.yml:40`):
  `go run ./cmd/tsgen -out web/src/types.ts && git diff --exit-code --
  web/src/types.ts`. Regenerating in place (rather than to a temp file)
  makes the failure message itself show the fix. The `web` job's
  `npm run build` (`test.yml:51`) then type-checks the guaranteed-fresh
  file. `docs/API.md` gains two lines under its header: shapes are
  generated from the Go structs, regenerate with `make types`.

Slice R16-4 — the guard shrinks to auto-discovery.

- Delete `tsContracts` (`guards_test.go:66`), `tsFields`/`goFields`/
  `contractDiff` (`guards_test.go:172`–`guards_test.go:217`) and
  `TestWebTypesMatchGoJSON` (`guards_test.go:219`) once the CI step in
  R16-3 is green on `master` (same PR is fine; the two protections must
  overlap for zero time only if CI runs both — order the commits so the
  generate+diff step lands before the deletion commit).
- What replaces them, in `guards_test.go` (it remains the repo-guards
  home): `TestEveryJSONStructIsGenerated` walks the same root-package
  AST as the generator, collects every `*JSON` struct name, maps each
  through the rename table it imports from `internal/tsgen` (default
  name when unlisted), and asserts an `export interface` of that name
  exists in the committed `web/src/types.ts` — so a new struct is
  caught by `make test` even before CI's diff runs, with a failure
  message that says to run `make types`. It also asserts the other
  direction for config hygiene: every rename-table and `extra` entry
  names a struct that still exists in the AST, so the tables cannot
  rot into a second hand-kept list of ghosts. The guard reads source
  and the committed file only; byte equality with fresh generator
  output remains the CI step's job, so `make test` never writes the
  tree.
- `TestWebTypesGuardCatchesFieldRename` (`guards_test.go:239`) is
  superseded inside `internal/tsgen` by fixture tests (a renamed tag in
  fixture source changes the emitted interface — see Tests). One
  honest-history line stays in the guard's comment: the M1.1a rename
  (`guards_test.go:238`) is now caught by regeneration + `tsc`.
- AGENTS.md's rule table row ("`web/src/types.ts` matches the Go JSON
  structs field for field") is updated in this slice to name `make
  types` + the CI diff + the auto-discovery test as enforcers, so the
  table keeps telling the truth about what runs.

### 5. Tests

- `internal/tsgen` table tests (`internal/tsgen/*_test.go`, beside the
  code): one fixture package of Go source per mapping row in R16-1 —
  scalars, `time.Time`, `*T` with/without `omitempty`, plain
  `omitempty`, `[]T`, `[][]T`, `map[string]T`, cross-references between
  generated structs, a `models.X`-style selector resolved across two
  fixture files, `json:"-"` and unexported fields skipped, missing json
  tag errors, an embedded field errors, an unknown type expression
  errors with its file:line in the message, a name collision after
  defaults errors. Rename and union config: seeded renames win over
  defaults; union values come from a fixture const block in source
  order; a subset-literal union emits as written; a config entry naming
  a missing struct or field errors. Determinism: generate twice over
  the fixture set, byte-equal. Rename-catching: the old guard's case —
  fixture tag `nextEventAt` vs a stale expected `nextMeetingAt` —
  produces differing output (this replaces
  `TestWebTypesGuardCatchesFieldRename`'s intent).
- Golden sweep in R16-2, not a committed golden file: the alongside
  `types.generated.ts` is reviewed in the PR with the
  disagreement-bucket table; after the flip the committed `types.ts`
  *is* the golden output and CI's diff is its test.
- Guard tests: `TestEveryJSONStructIsGenerated` (R16-4) plus negative
  fixtures beside it proving the check has teeth both ways — a fixture
  source tree whose `*JSON` struct has no interface in a fixture
  types file fails, and a fixture config naming a vanished struct
  fails (guards keep the house habit of testing their own teeth,
  `guards_test.go:56`).
- Web gates, in the flip slice: `tsc --noEmit` (the union fallout is
  the point), `npm test` (fixture objects across the 100+ page and
  component suites double as wire-shape fixtures — a fixture missing a now-required
  field or carrying one the server dropped fails here, which is the
  feature), `knip` (manual/generated file wiring proven), `npm run
  build` + bundle gate (types are erased, so the eager total must not
  move; if it does, something imported a value from the types files —
  investigate, do not re-tighten around it).
- e2e: `make e2e` for the flip slice. Types cannot change runtime
  behaviour, so a green run whose only purpose is proving the flip's
  accompanying page edits (null-handling removal, union narrowing
  fallout) did not alter rendered output is the safety net; a11y and
  layout suites inside that run cover the touched pages at 390px. No
  new e2e spec is warranted by generation itself.

### 6. Acceptance checklist

- [ ] `go run ./cmd/tsgen -out web/src/types.ts` on a clean tree
      produces a zero `git diff`; the CI step enforces it. Running it
      twice is byte-identical (generator test).
- [ ] `grep -c 'export interface' web/src/types.ts` accounts for all
      96 `*JSON` structs + 7 extras + `RequestWarnings`; adding a new
      dummy `*JSON` struct in a scratch branch makes the next
      generation include it with no config edit, keeps
      `TestEveryJSONStructIsGenerated` green, and makes the CI diff
      step red until the regenerated file is committed.
- [ ] No generated field is `any` or `unknown`; the generator exits
      non-zero (naming file:line) on a type expression outside its
      table — shown by the negative fixtures.
- [ ] `User.role`, `Member.role`, `GroupSummary.status`,
      `GuestRegistration.status` are closed unions; removing a value
      from the matching `models` const block changes the regenerated
      file (demonstrated once in the flip PR and reverted).
- [ ] `tsContracts` is gone from `guards_test.go`; no hand-kept pair
      list remains anywhere (the rename table in generator config is
      the only name mapping, and it only carries *names*).
- [ ] The SPA's imports did not move: callers still import from
      `../types` / `./types`, and `knip` is clean over
      `types.ts` + `types-manual.ts` with no leftover ignore.
- [ ] `make lint-go`, `make deadcode`, `make test`, `tsc --noEmit`,
      `npm test`, `make e2e` (flip slice) and the bundle gate pass;
      eager gzip total is unchanged by the flip.
- [ ] The generated header is present and `docs/API.md` points at
      `make types`.

### 7. Risks, rollback

- **The generator is a permanent tool, not a one-off.** Its upkeep is
  the price; the mapping table is deliberately small and fail-closed
  so upkeep means *adding a row with a test* the first time a new Go
  shape appears, which the error message will demand by name. A
  maintainer who fights that in a release week is the risk: rollback
  below exists for exactly them.
- **Silent wrongness beats loud failure as a hazard.** The dangerous
  mappings are `omitempty` and nil-slice semantics, because neither
  the tag nor the Go type tells the whole story (`types.ts:296`).
  R16-2's adjudicated diff is the control; after the flip, a new
  `omitempty` or a presenter that starts returning a nil slice is a
  behaviour review the regenerated diff makes visible — reviewers of
  such PRs read the `types.ts` diff as part of the change (name this
  in the generated header's neighbourhood by keeping diffs small:
  field order follows Go order precisely so semantic diffs do not
  hide in reorders).
- **Union config can drift from the handlers** (a field is generated
  closed while a presenter starts emitting a new value). The server
  wins at runtime; TypeScript then believes something false until
  config catches up. This is still strictly better than today's open
  `string`, and values come from `models` constants — the drift
  window is a presenter inventing a literal outside `models`, which
  the string-constants discipline already discourages.
- **Chicken-and-egg in CI**: the diff step runs the generator, which
  needs only the Go toolchain (present, `test.yml:35`) and the source
  tree — no database, no `npm ci`. Keep it before `make test`
  (`test.yml:44`) so a stale types file fails fast and cheap.
- Rollback is the committed generated file: types are erased, so the
  rollback PR deletes the CI step, the `make types` target's
  authority, and swaps the header for a hand-maintained note — the
  file on disk stays valid TypeScript and the app builds unchanged.
  `internal/tsgen`/`cmd/tsgen` can be removed in the same PR or left
  dormant; nothing at runtime reaches them (`Dockerfile:18` builds
  `.` only). Rollback per slice before the flip is trivially "do not
  merge the next slice" — until R16-3 the live file is untouched.

### 8. Dependencies

- Slices run in the written order; R16-2 must not merge until its diff
  table is adjudicated, and R16-4 waits for R16-3's CI step to have run
  green once on the PR that introduces it.
- R15 independence: R15-2 edits `api.ts` wrappers and R15-3 adds
  facet params plus possibly two new union names; both consume types
  but define none of the 118. Sequence the R16-3 flip outside any R15
  PR, and if R15-3 lands first, `MeetingDay`/`DayPart` arrive in
  `types.ts` by hand and move to `types-manual.ts` at the flip (they
  are request-vocabulary, not a response struct's field).
- Future response types (any later audit item) stop adding a
  `tsContracts` pair the day R16-4 lands; briefs that still say "add a
  pair to `tsContracts`" (e.g. the shared rules in
  `docs/plan/briefs/audit-2026-11/R2-R3-R4-people.md:19`) mean, after
  this item: rename-table entry only if the default name is wrong, and
  commit the regenerated file.
- No schema, no new dependency, no build-image change: `go build .`
  never compiles the generator into the shipped binary, and the web
  build's first step (`tsc --noEmit`, `web/package.json:32`) is the
  consumer the whole item exists for.

### 9. Open questions for Grier (each with a default that unblocks work)

1. Nullable-slice policy beyond the one known field: keep the
   default `[]T → T[]` and name exceptions one by one, or invert it
   (`T[] | null` everywhere)? Recommended default: exceptions one by
   one, as written — presenters in this codebase build slices with
   `make(...)`-or-append out of habit, and taxing every consumer with
   null checks would spawn the defensive-code sprawl the guard tests
   exist to prevent.
2. Should Go grow named string types for roles/statuses later (making
   unions inferable and the config table deletable)? Recommended
   default: not in this item. It is the cleaner end state but a
   codebase-wide refactor; the config table records exactly the fields
   such a refactor would convert first, so this item is its map rather
   than its competitor.
3. Generated file as `types.ts` with a manual sibling (chosen here),
   or generated `types.generated.ts` permanently with `types.ts`
   staying the hand file that re-exports it? Recommended default: as
   written — one canonical import path and the do-not-edit header on
   the file people actually open; the `types-manual.ts` remainder is
   small and shrinks over time as envelopes gain structs opportunistically.
4. `registrationBatchInput` is a request body; keep generating request
   shapes the client composes, or limit generation to responses and
   hand-keep `RegistrationCommand`? Recommended default: keep it in
   the `extra` list — it is one of the few payloads where a field
   rename silently changes what the server accepts, which is the same
   failure this item exists to catch.
5. Regeneration UX: CI fails and the developer runs `make types`
   (chosen), or CI auto-commits the regenerated file to the PR?
   Recommended default: fail-and-run. The regenerated diff is part of
   the change being reviewed (section 7), and auto-commit hides
   precisely the signal the reviewer is meant to read.








