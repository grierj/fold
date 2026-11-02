## Review

**Scope:** Read-only review of the current uncommitted directory implementation and named tests. No files modified, commands run, services started, or database accessed.

### Correct

- **The visual direction meets the goal.** The supplied viewport screenshots show a compact four-date desktop agenda above a full-width, two-column directory, readable mobile rows, and successful Browse groups landing positions. I independently inspected all four named `busy-church` viewport images; I did not treat full-page sticky-header artifacts as defects.
- **URL-backed state and ordinary Back restoration are implemented coherently.** Search, tags, and expanded count come from URL parameters; unrelated parameters survive updates. Return position records a row and viewport offset, waits for results and preceding content, then restores focus (`web/src/components/GroupDirectory.tsx:29–40, 82–103, 119–137, 182–185`).
- **Expansion handles its special focus cases.** Nonfinal expansion retains the button and routes the next Tab to the first added result; final expansion focuses that result when the button disappears. Scroll preservation runs in a layout effect (`GroupDirectory.tsx:105–116, 190–202`). The new unit test explicitly covers both cases.
- **Existing contracts remain intact.**
  - Tags are deduplicated case-insensitively and passed to the existing exact-match OR filtering API (`GroupDirectory.tsx:18–25, 64`; `api_groups.go:29–59`).
  - Coming up retains `mine: true`, excludes ended/declined dates, and caps at four (`web/src/components/ComingUp.tsx:14–40`).
  - Only the first displayed group receives image priority (`GroupDirectory.tsx:186`).
  - The return-position provider stores only an in-memory position, inside the user-keyed Layout; signout discards it (`web/src/lib/directoryPosition.tsx:3–11`; `web/src/components/Layout.tsx:131`; `web/src/App.tsx:74–78`).
- **Simplicity/security/database:** The extraction leaves Home focused on its introductory content and agenda. No schema, server permission, or deployment changes are introduced. Show more limits rendering rather than adding server pagination; the existing authorized list endpoint remains authoritative.

### Finding: P1 — Sticky Filters can open its controls entirely offscreen

**Location:** `web/src/components/GroupDirectory.tsx:147–153, 163–169`; `web/src/index.css:270`.

The Filters button remains visible in the sticky toolbar, but its disclosure is ordinary document content beneath the toolbar’s original position. Opening it only changes `filtersOpen`; it does not scroll to or focus the panel.

**Concrete repro:**
1. Load the 50-group directory with the initial 12 results.
2. Scroll several rows down until the toolbar sticks.
3. Click **Filters**.

The panel opens above the viewport. At sufficient scroll depth, none of the newly exposed controls is visible, despite the visible button reporting expanded. Disabling scroll anchoring does not bring that panel into view. The supplied browser checks open Filters near the directory start, so they do not cover this case.

**Smallest fix:** When opening the disclosure, bring the directory/panel into view beneath the shell header, with predictable keyboard focus. Add a browser check that opens Filters after scrolling deep into the results.

### Finding: P1 — Searching before the initial response completes loses available tags

**Location:** `web/src/components/GroupDirectory.tsx:44, 60–80`.

On an initially unfiltered mount, `separateChoices.current` is false, so available tags depend entirely on the initial result request. If search changes before that request resolves, effect cleanup marks it inactive. Its eventual response returns at line 66, before populating choices at line 68. The separate tag-source effect also returns immediately because `separateChoices` remains false.

**Concrete repro:**
1. Open `/` with the initial unfiltered groups request delayed.
2. Type a search and allow the 300 ms debounce to start the filtered request.
3. Resolve the filtered request, then the initial request.
4. Open Filters.

Filtered rows display, but the available-tag panel contains only **All**, even though the authorized directory has tags. It stays incomplete until an unfiltered fetch succeeds or the page remounts.

The new stale-response test already supplies essentially this ordering, but asserts only that stale rows do not replace current rows (`web/src/components/GroupDirectory.test.tsx`, final test).

**Smallest fix:** Give the unfiltered tag source a mount-scoped lifetime independent of the current result query, or initiate a fallback tag-source request when that initial request is superseded. Preserve stale-result suppression. Extend the delayed-response test to assert available tags.

### Finding: P2 — Removing a selected chip drops keyboard focus

**Location:** `web/src/components/GroupDirectory.tsx:156–161`.

Each selected chip removes itself on activation, without transferring focus. This is a newly introduced disappearing control, unlike toggling the persistent chips inside the disclosure.

**Concrete repro:** Open `/?tag=music&tag=study`, keyboard-focus **Remove music filter**, and press Enter. The focused button is removed and focus falls back to the document rather than another useful filter control.

**Smallest fix:** After removal, focus a neighboring selected chip, or the persistent Filters button when none remain. Add a keyboard assertion. The adjacent Clear action also merits the same handling when it removes its own selected-filter group.

### Verification and limits

- **My independent checks:** Exact working-tree diff, new source files, named tests, relevant shared components/API seams, all 36 entries in the supplied `directory-new/results.json`, and the four named viewport screenshots.
- **Parent-run evidence, not tests I ran:** 138 Vitest files / 1,386 tests and 36 directory browser checks at 390/768/1280. The supplied browser JSON contains no recorded failures.
- The findings above are source-proven interaction paths with concrete repro steps; I did not execute those repros.
- No unrelated busy-church page issues are included.

**Merge verdict: BLOCK.** I approve the implemented layout direction and the core URL/restoration/expansion approach, but the two filter-interaction defects should be corrected before approving this implementation for merge.