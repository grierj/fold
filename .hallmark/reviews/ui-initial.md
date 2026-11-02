fix

## Review

- **Finding — P1, tablet task hierarchy:** `web/src/pages/Profile.tsx:108–115` shows the entire navigation from `sm`, but `.fold-settings-grid` only becomes two-column at 1024px (`web/src/index.css:264–269`); final `ui/profile-details-768.png` pushes the page heading to approximately y=770 and the first text field below y=1000, whereas the before screenshot exposes the complete form immediately; align sidebar/back-link visibility with the grid breakpoint (`lg:block` / `lg:hidden`).
- **Finding — P2, cramped empty state / gate 72:** Removing horizontal padding from unframed `EmptyState` (`web/src/components/ui/EmptyState.tsx:26`) leaves Alerts’ heading, body and button flush against its enclosing card in final `ui/notifications-390.png`; `FeedEmpty` uses this variant at `web/src/components/Notifications.tsx:185–194`; add horizontal padding around that empty-state instance, retaining the new left alignment.
- **Finding — P2, calendar density regression:** The full-width Group filter and additional toolbar spacing (`web/src/pages/Calendar.tsx:123–165`) push the selected-day result below the initial phone viewport in final `extra/calendar-month-390.png`, unlike the before capture; restore the compact “Just mine”/Group row where it fits and reduce toolbar-only spacing without shrinking targets.

### Correct

The redesign reads as a working church app, not a marketing template. The strongest improvement is signed-in Home: at 1280px, upcoming dates and the group directory are genuinely alongside each other; at 390px, the agenda is shorter and group search arrives sooner. Date, event title and RSVP remain legible and consistently ordered on Group and Event screens. Evidence: final `home-{390,1280}.png`, `group-member-390.png`, `group-leader-390.png`, and `event-390.png`; implementation in `ComingUp.tsx:85–101` and `OccurrenceCard.tsx:30–44`.

Fold Violet and self-hosted Inter remain intact. Shared layout measures, meaningful active navigation, restrained surfaces and visible menu focus are improvements. The open Group menu is readable and stays within the phone width.

**What the app is about:** finding your church’s groups and dates, responding to invitations, and organizing the people involved.
**What someone would remember:** prominent date numerals, straightforward RSVP choices, and violet actions.

**Signature verdict:** “Date-led agenda with clear RSVP and groups alongside” describes the main screens accurately. No wholesale idea/theme gap. The largest execution gap is the tablet Profile navigation overwhelming the task itself.

**What the old version did better:** it put tablet profile editing immediately beside navigation, fit the phone month calendar and selected-day answer into one screen, and gave the Alerts empty state proper inset spacing. The findings above preserve those advantages without restoring the old card-heavy treatment.

### Exact visual coverage

Paths below are relative to `/tmp/fold-verify-proof/20261101T191847Z-3327239/`.

**Final 390px screenshots inspected — all 25 `ui` variants:**

`home-signed-out`, `login`, `join`, `home`, `my-groups`, `group-member`, `event`, `profile`, `profile-details`, `profile-privacy`, `profile-security-email`, `profile-security-password`, `profile-calendar`, `profile-household`, `profile-guardian`, `profile-invite`, `notifications`, `group-leader`, `schedule-event`, `messages`, `compose`, `admin`, `admin-users`, `new-group`, `church-event`.

**Final 390px `extra` variants inspected:**

`calendar-agenda`, `calendar-week`, `calendar-month`, `group-menu`, `group-about`, `group-members`, `event-edit`, `admin-settings`, `profile-notifications`, `rooms`, `not-found`.

**Additional final captures inspected:**

- 768px: `ui/home`, `ui/profile-details`.
- 1280px: `ui/home`, `ui/admin-users`, `extra/event-edit`, `extra/calendar-month`.
- `.hallmark/shots/127.0.0.1-32851-*`: public Home at 375px and 1440-full; public Group, Event and Calendar at 1440-full.

**Before comparisons inspected:** Home member/public, Group member, Admin users, composer, Alerts and tablet Profile details from the first supplied baseline; month Calendar and Event edit from the second baseline.

The Week capture represents an **empty week**, and Rooms represents **unavailable rooms**, not populated calendars. Fixed/sticky bars appearing within full-page captures were not treated as proof of permanent content obstruction.

### Evidence and exceptions

I read the supplied complete `.hallmark/review.diff` against `82323bf`, including working changes and added CSS/design files, plus current `index.css`, `base.css`, and `tokens.css`. No files, app state or data were changed; no builds or tests were run.

The supplied results record zero hard screen-width failures. The **1,367 Vitest passes are reported evidence**, not tests I independently executed.

Hallmark is **not clean**: `.hallmark/after-lint.log` records **3 FAIL / 0 REFLEX**, down from 40 FAIL / 9 REFLEX. The remaining punctuation at `ImageUpload.tsx:127`, `calendar/EventRow.tsx:74`, and `calendar/WeekView.tsx:29` is retained, pre-existing, test-contracted material—not a new usability defect or a reason to alter semantic tests. Retained Inter, brand colors and existing assets are likewise not redesign defects. Seed content demonstrates presentation, not production adoption.

- **Fixed:** None; read-only review.
- **Independent approval:** Not yet granted for final UI finish; resolve the tablet regression and address the two bounded finish notes.
- **Merge verdict:** **BLOCK** pending the tablet correction. Backend/API, database and security sign-off are outside this finish review.

**Do not dilute the compact date-led agenda, explicit RSVP choices, retained Fold identity, or existing content and wiring while fixing these issues.**