## Review

**Verdict: CHANGES REQUESTED — BLOCK for the reviewed scope pending the three P1 corrections below.**

Reviewed the available uncommitted diff and new components, their callers, API/type contracts, and associated tests. The diff tool compares against reviewer-launch HEAD; I did not independently verify that HEAD is `5c0f919` or inspect a committed range. No files were changed, and no tests, servers, browsers, or database operations were run.

### Correct

- **Message pagination preserves authorization boundaries.** Every request reapplies category and non-admin author restrictions after accepting the cursor (`api_messages.go:703–745`). The cursor supplies only an ordering position. The default remains 200, and `(updated_at, id)` supplies a deterministic tie-breaker. The added test covers equal timestamps, insertion between pages, history beyond 200, literal `%` search, and author/category isolation (`api_messages_test.go:386–465`).
- **Church-wide filtering does not widen calendar visibility.** `group_id IS NULL` narrows the candidate query before visibility processing and the response cap (`api_calendar.go:100–105`). That query also constrains scoped occurrence reads; the existing visibility and calendar-scope checks remain in place (`api_calendar.go:115–198`). The added test exercises public versus private church events with more than 500 group dates preceding them (`api_calendar_test.go:367–393`).
- **The new response contract is paired correctly.** Go’s optional `nextCursor` matches `MessageListResponse`, and the contract guard includes the pair (`api_messages.go:690–693`, `web/src/types.ts:1131–1134`, `guards_test.go:151`).
- **Presentation sharing retains important mutation distinctions.** Attendance uses `sort={false}`, and waitlist positions still derive from the full roster, not the filtered page (`web/src/components/ManageRoster.tsx:98–139`). Existing capacity, approval, removal, and restoration handlers were not replaced. Admin and room lists disable their new browsing controls while their tracked editors are open.

### Finding: P1 — Searching messages can attach an old query’s cursor to the new query

**Location:** `web/src/pages/Messages.tsx:37–48, 58–65, 140–142`

Search updates the URL immediately, but the fetch uses a value debounced by 300 ms. During that interval, the previous results and their **Older messages** button remain active. Clicking it writes the previous query’s cursor into the new search URL. When the debounce completes, the request combines the new search with that old cursor, skipping matching messages newer than the cursor.

**Reproduction:**
1. Load an initial page with an older-page cursor.
2. Enter a subject search whose matches include recent messages.
3. Before the 300 ms debounce expires, click **Older messages**.
4. The new search starts at the old list’s position instead of its first page; it can incorrectly report no matches.

**Smallest correction:** Disable pagination while search is pending or results are loading, and only accept a cursor belonging to results for the current filter/search key. Add a fake-timer test that enters a search and immediately attempts pagination.

### Finding: P1 — Crossing the compact-list threshold discards existing date-form drafts

**Location:** `web/src/components/CompactDates.tsx:21, 37–59`; reachable from `web/src/pages/GroupDetail.tsx:917–938`

The small and dense modes render different React trees. At six dates, children are directly inside keyed wrappers; at seven, they move underneath a new container and detail wrappers. Crossing that threshold remounts the existing children. Additionally, dense mode mounts only the open or visited children, so previously direct small-list forms are not all retained.

**Reproduction:**
1. Have a group’s first cursor page contain six distinct event summaries, with more distinct events on the next page.
2. Enter an unsaved guest-registration, RSVP, or event-editor draft.
3. Click **Load more dates**, bringing the distinct-event count above six.
4. Reopen the original event: its local draft has reset.

The reverse transition can occur after cancelling/removing a date from a seven-date event.

**Smallest correction:** Keep one stable keyed child hierarchy across both modes and preserve children that were already mounted in small mode. Change visibility/presentation rather than component identity. Add draft-preservation tests for both six-to-seven and seven-to-six transitions.

### Finding: P1 — People-list navigation silently loses direct-contact request drafts

**Location:** `web/src/components/PeopleList.tsx:23–29`; callers at `web/src/pages/GroupDetail.tsx:1054` and `web/src/components/ManageRoster.tsx:106`

`PeopleList` renders only `paging.rows`, so changing its page, search, or filter unmounts rows that leave the result set. Those rows can contain `RequestContactButton`, which owns an open `ContactRequestForm`. The form keeps its selected contact fields and reason in local state (`web/src/components/ContactRequestForm.tsx:12–17`).

**Reproduction:**
1. Open **Request direct contact** for a member or attendee.
2. Select Phone/Both and enter a reason.
3. Page away or search for another person.
4. Return and reopen the request: the reason and selection are gone without warning.

Paging away during submission also removes the form that would show success or failure.

**Smallest correction:** Keep the active contact-request form outside the paged rows, preserve its state by person, or disable list navigation/filtering while it is open. Add an integration test with the real request form, not only stateless list items.

### Finding: P2 — “See all church events” opens a shorter window than its preview

**Location:** `web/src/pages/Home.tsx:75–77`; `web/src/pages/Calendar.tsx:27, 46–47, 198–214`

Home’s preview and count come from `apiListOpenEvents`, whose `upcomingWindow` covers roughly 90 days (`api_event_access.go:197–198, 246–255`). The new link opens the calendar’s default 30-day range.

**Reproduction:** Put five public church events 45–50 days ahead, with none in the first 30 days. Home shows four dates and counts five. **See all church events** opens an empty calendar, and the empty branch has no **Show more dates** control. The fifth event is therefore not exposed by the advertised continuation without manually changing the date.

**Smallest correction:** Pass a matching range in the Home link, and label the count’s window explicitly. Add a test with church events beyond day 30, including an initially empty calendar range.

### Finding: P2 — Compact date summaries omit confirmed rooms

**Location:** `web/src/components/CompactDates.tsx:47–49`

The new summary displays only `d.location`. Existing expanded date/event rows use `placeLine(instance.room, instance.location)`, which puts a confirmed room and building first. `web/src/lib/place.ts:8–20` explicitly defines this shared presentation rule.

**Reproduction:** Assign a confirmed room to an event whose typed location is empty or merely an instruction such as “north door.” In a collapsed compact row, the actual room/building disappears; only expansion reveals it.

**Smallest correction:** Use `placeLine(d.room, d.location)` in the compact summary, and include that value in search if location matching is retained. Add a room-backed summary test.

### Fixed

None — read-only review.

### Merge verdict

**BLOCK / CHANGES REQUESTED.** The backend pagination/filtering changes have no identified authorization-widening issue, and the bounded-list approach is appropriately simple. Fix the three P1 state regressions before approval; the two P2 corrections are small, concrete discovery/presentation improvements. Test execution and isolated runtime proof remain with the parent.