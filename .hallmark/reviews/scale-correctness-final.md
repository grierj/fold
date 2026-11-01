## Review

**APPROVED for the reviewed correctness/security/simplicity scope. All five previous findings are resolved.**

No issues found.

### Fixed — verified in current source

1. **Message search/cursor race**
   - `web/src/pages/Messages.tsx:39–69, 146` associates resolved results with their category/search/status/cursor key. Pagination requires that key to match the current query and that the debounce has settled.
   - The Older control is disabled when those conditions fail; `move` independently guards nonempty cursors. Returning to Newest remains available for recovery.
   - `web/src/pages/Messages.test.tsx:72–97` covers the debounce race and invalid-cursor recovery.

2. **Date drafts lost at the compact-list threshold**
   - `web/src/components/CompactDates.tsx:31–33, 45–74` now preserves the same keyed row/detail hierarchy in both modes. Previously rendered small-list children are retained when compact mode begins.
   - `web/src/components/CompactDates.test.tsx:29–39` verifies a sixth-row draft survives **6 → 7 → 6**, including another edit while compact.

3. **Contact-request drafts and submission results lost during roster browsing**
   - `web/src/components/PeopleList.tsx:18–32` retains keyed, authorized rows and applies `hidden` outside the visible page/filter rather than unmounting them. Rows removed from the caller’s authorized input are still removed.
   - Current callers supply keyed `<li>` elements. Tailwind’s hidden rule uses `display: none !important`, so row flex styling does not expose hidden controls.
   - `web/src/components/PeopleList.test.tsx:40–64` uses the real lazy contact-request form and verifies Phone selection, reason text, and an asynchronous submission error survive pagination/search. Hidden fields and alerts are excluded from role queries.
   - This is intentionally bounded **presentation**, not DOM virtualization; it preserves state without changing authorization or mutation policy.

4. **Home continuation omitted distant preview dates**
   - `web/src/pages/Home.tsx:46–49, 79–81` derives the continuation’s start from the first returned date and its inclusive duration through the last, with a 30-day minimum. The count explicitly names the 90-day API horizon.
   - Calendar consumes those `date` and `days` parameters (`web/src/pages/Calendar.tsx:36, 46–56`).
   - `web/src/pages/Home.test.tsx:222–239` covers the four-date preview and the January 15–March 15 continuation requiring 60 inclusive days.

5. **Confirmed rooms missing from compact summaries**
   - `web/src/components/CompactDates.tsx:23–27, 54–60` uses `placeLine(d.room, d.location)` for both the summary and search.
   - Date/place labels are memoized by the input dates rather than reformatted on each local search change.
   - `web/src/components/CompactDates.test.tsx:42–48` covers confirmed room, building, typed directions, and room-name search.

### Correct

- Backend changes remain unchanged from the initial review: message cursors do not replace author/category restrictions, the default limit remains 200, and church-wide filtering narrows candidates before the existing visibility/scope checks and calendar cap (`api_messages.go:703–745`; `api_calendar.go:100–105, 115–198`).
- The follow-up CSS change keeps selected segments above adjacent borders without the former `z-10` elevation (`web/src/index.css:243–249`). The month control now separates its visible day/count text and places “today” after the count in its accessible label (`web/src/components/calendar/MonthView.tsx:93, 112–114`). No additional source-level blocker was identified in these corrections.

### Verification limits

This was a read-only source/test re-review. I performed no edits, test execution, database operations, server starts, or browser checks.

The reported Vitest and isolated Go suite results are parent-provided evidence, not independently executed here. The newest distant-date test and final rebuilt browser/accessibility proof remain pending as stated by the parent.

### Merge verdict

**OK with notes — explicit approval for this review scope.** No remaining corrective changes are requested. Complete the pending final runtime proof and normal CI requirements before merge.