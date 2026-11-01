## Review

**Merge verdict: OK. I approve the current directory implementation for UI/UX and correctness.** All three findings from the previous review are resolved.

No issues found.

### Fixed

- **Sticky Filters now reveals its controls.** Opening Filters scrolls the directory section into view while preserving focus on the persistent disclosure button (`web/src/components/GroupDirectory.tsx:163–168`). The regression test checks scrolling and focus (`web/src/components/GroupDirectory.test.tsx:116–123`). The updated browser script additionally opens Filters after scrolling to row 20 and asserts that a tag control returns to the viewport, for visitors and members at all three widths (`.hallmark/verify-directory.mjs:61–68`).

- **A delayed initial response no longer loses available tags.** Tag choices now have their own mount-scoped effect and cancellation flag. The initial unfiltered results share that request, while subsequent result requests have independent stale-response suppression (`GroupDirectory.tsx:62–89`). This resolves the race without adding a redundant initial GET. The updated tests assert both one initial request and the important delayed-response outcome: choices populate without replacing the filtered rows (`GroupDirectory.test.tsx:33–38, 102–114`).

- **Removing selected filters preserves keyboard focus.** Selected-chip removal, inline Clear, and the empty-state clear action transfer focus to the persistent Filters button (`GroupDirectory.tsx:138–140, 154, 175–176`). Keyboard tests cover chip removal and inline Clear (`GroupDirectory.test.tsx:125–136`). I also verified that the shared Button passes its ref through to the native button (`web/src/components/ui/Button.tsx:9–22`).

These fixes were made by the implementer; I made no edits.

### Correct

- **UI/UX:** The revised interactions retain the compact disclosure, selected-tag chips, explicit count, readable mobile directory, and full-width two-column desktop layout. I independently inspected the fresh viewport images:
  - `member-top-1280.png`
  - `member-filters-390.png`
  - `visitor-filtered-390.png`

  These support approval of the implemented layout, not merely its automated checks.

- **Correctness and accessibility:** URL-backed search/tags/expanded count, waiting for preceding content before restoration, row-position/focus restoration, and expansion/final-batch focus behavior remain intact (`GroupDirectory.tsx:29–40, 91–135, 196–220`). The changes do not weaken exact case-insensitive OR tags, the four-date agenda contract, or selective image priority.

- **Simplicity and performance:** The fix separates the lifetimes of tag discovery and filtered results without introducing another cache, endpoint, or pagination mechanism. Initial unfiltered loading still shares one request.

- **Security and session isolation:** No permission-policy changes. The tag request remains component-local, and return-position state remains in the in-memory provider under the user-keyed Layout (`web/src/lib/directoryPosition.tsx:3–11`; `web/src/App.tsx:72–75`). No new cross-signout persistence is introduced.

- **Database and deployment:** Not affected.

### Verification boundaries

This was a read-only source, test, and evidence review. I ran no commands, started no services, accessed no database, and modified no files.

I independently read the current implementation, regression tests, updated browser verification script, all 36 entries in `/tmp/fold-verify-proof/20261101T214745Z-294686/directory-new/results.json`, and the viewport images listed above. All 36 recorded entries have empty failure lists.

The **138 Vitest files / 1,388 passing tests** and fresh browser execution were **parent-run checks**, not tests I executed. Approval rests on the source-level fixes and inspected evidence together. Unrelated ongoing busy-church experiment findings are outside this approval.