# Directory experiment: 50 groups

Evaluated commit `228d989` on `design/hallmark-fold`. **Evaluation only: no production UI code changed.**

## Verdict

The compact rows and visual system remain readable at 50 groups. The directory's browsing structure does not scale as well: too many filter chips, an unbounded list, lost search state on Back, and unused desktop space. Improve discovery controls and layout before changing the theme or making cards more decorative.

## Experiment

- Started isolated verification run `20261101T210036Z-4121665`; no shared or production data touched.
- Added 48 synthetic groups to the two-group standard seed through the app's authenticated API, for **exactly 50 public groups** visible to both audiences.
- Six plausible ministry/activity families, **19 distinct tags**, long and short names, varied locations, **33 groups with upcoming dates**, 17 without. Names and dates are fixtures, not real ministries or claims.
- Kept the six existing fallback covers. The signed-in member has the normal four upcoming dates; created groups have ordinary API-created memberships, not fabricated counts.
- Captured visitor and member Home at **390, 768 and 1280px**, all 900px high, at the top, directory heading and middle of the list. Also tested search, no results, opening a group and Back, one/two tag filters, Clear and a shared tag URL.
- **30 screen/state checks passed**: no horizontal overflow, clipped controls, sub-24px targets or moderate/serious/critical axe findings. Six two-group baseline checks also passed. Automated checks did not detect the usability problems below.
- One initial driver attempt used a heading locator for an empty-state paragraph; corrected the driver, not the app. Final measurements wait for all four agenda dates and distinguish visitors without a bottom tab bar.

## Measurements

Coordinates are from the document top, with no initial scroll. “Visible groups” counts complete rows above any fixed bottom navigation.

| Audience | Width | First group starts | Entire page height | Complete rows in initial viewport |
| --- | ---: | ---: | ---: | ---: |
| Visitor | 390 | 849px | 8,161px | 0 (one partly visible) |
| Member | 390 | 1,122px | 8,514px | 0 |
| Visitor | 768 | 577px | 7,859px | 2 |
| Member | 768 | 966px | 8,257px | 0 |
| Visitor | 1280 | 525px | 7,807px | 2 |
| Member | 1280 | 453px | 7,744px | 3 |

- At 390px, the 19 tags plus All take **six rows / 304px**, before any results. Even after scrolling directly to the directory, only one complete row fits with all the filters in view.
- The phone member page is about **9.5 viewport heights** long. Rows average 146px; 13 names wrap, none clip. The individual row is not the primary problem.
- At 1280px, the member directory is **631px of a 1,104px content area**. After the agenda ends, its roughly 40%-wide left column stays empty for another **7,049px** of directory.
- Filtering can make the toolbar taller: on desktop, adding the plus markers and selected-filter summary expands the chip region from three rows to four and pushes the first result from 453px to 561px.

## Findings and recommendations

### 1. Preserve discovery state on Back — fix first

**Observed at 390 and 1280:** search for `Lakeside` → one group → open it → Back. The input becomes empty and all 50 groups return. This makes comparing groups unnecessarily repetitive.

Keep the search query in the URL, as tags already are; retain scroll/result-position state when returning. If a result limit is introduced, restore that limit too. Do not introduce persistent account data or a global cache just to solve navigation state.

Evidence: `results.json` → `tasks`, `search-one-*.png`, `after-detail-back-*.png`. Source: `web/src/pages/Home.tsx`, component-local `search`/`debounced` state versus URL-backed tags.

### 2. Replace the tag wall with a compact filter control — highest visual payoff

The six-row chip wall makes a phone visitor scroll before seeing a complete group, and a signed-in member does not see any group at first. On a one-result search, the same full chip wall still sits above the answer.

Recommended treatment:

- Search, a **Filters** disclosure and a visible result count.
- Show selected tags inline; keep all available tags in an accessible expandable panel.
- Optionally show a small, stable set of common tags when none is selected. Do not move controls around based on every response or hide selected tags.
- Keep the compact search/filter bar available while browsing the directory, without pinning a six-row block over the content.
- State the actual total, such as **“50 groups”** or **“9 groups match”**, and announce result changes politely. Currently neither the list nor the filter summary gives a count.

This retains every tag and the existing OR semantics. It is not a suggestion to shrink touch targets.

### 3. Bound the initial list

Start with **12 groups** and an explicit **Show more groups** action, with “Showing 12 of 50.” Keep the existing next-meeting sort and append the next batch without moving focus or scrolling the page unexpectedly.

At this scale, client-side presentation of the already-returned array is enough to test the UX. No new dependency, virtual scroller or server pagination is justified by this experiment alone. Test keyboard access and Back restoration before shipping it.

### 4. Use desktop width more deliberately

The side-by-side agenda/directory works near the top but leaves an almost empty half-page throughout most of a 50-group list. Visitors also get very wide, single-column rows.

Prototype a compact four-date agenda followed by a full-width directory with **two columns of compact rows on desktop**, retaining one column on phones. Preserve chronological ordering, readable names and the current four-date Home behavior (PLAN DEC-94). Compare it against the current composition before adopting it; do not squeeze two columns into the existing 631px rail.

A smaller desktop thumbnail is a secondary density option, not the first fix. Keep the current row's name → next meeting → members/place hierarchy.

### 5. Make the directory easy to reach on a phone

A short **Browse groups** jump near the greeting would serve people who came to find a group without removing their upcoming dates. Ensure the anchor remains correctly positioned after agenda loading and that the first result is visible after applying a filter. In the shared-tag capture, the first result is still below the phone viewport despite the `#groups` link.

## What works; what I would not change

- Long names and locations wrap; controls remain readable and accessible.
- Search finds the intended group. No-result copy is understandable. A Bible-study tag yields nine groups, adding Outdoors yields 17 with OR semantics, and Clear returns all 50.
- Every directory has **one eager/high-priority cover and 49 lazy covers** when unfiltered. Do not make all 50 eager to address a browsing problem.
- The 50-group response is about **21KB**, and the page has fewer than 700 DOM elements in this fixture. Local API measurements were single-digit milliseconds in the final top captures. This is not a production benchmark; there is no demonstrated need for virtualization here.
- Keep Fold Violet, the typography, compact rows and explicit meeting information. A carousel, smaller text, more badges or decorative cards would not solve the observed problems.

## Limits

All groups were public deliberately, so this is not a new permission-policy test. Fallback images are reused data URLs; their decoded state does not demonstrate real photo bandwidth or network deferral. Uploaded-photo priority was checked separately in the previous follow-up. No slow-network, field-user or production-database benchmark was performed. Recommendations are evaluated findings, not implemented changes or an independently reviewed new design.

## Evidence and reproduction

Evidence directory: `/tmp/fold-verify-proof/20261101T210036Z-4121665/directory-50/`.

Useful captures:

- [Phone visitor opening](/tmp/fold-verify-proof/20261101T210036Z-4121665/directory-50/visitor-top-390.png)
- [Phone member opening](/tmp/fold-verify-proof/20261101T210036Z-4121665/directory-50/member-top-390.png)
- [Phone directory and filter wall](/tmp/fold-verify-proof/20261101T210036Z-4121665/directory-50/member-directory-390.png)
- [Desktop opening](/tmp/fold-verify-proof/20261101T210036Z-4121665/directory-50/member-top-1280.png)
- [Desktop halfway through the list](/tmp/fold-verify-proof/20261101T210036Z-4121665/directory-50/member-middle-1280.png)

The directory also contains full-page captures, `inventory.json`, `results.json` and `result.txt`. The test instance is disposable; the proof survives cleanup.

Reproduce using the verification skills and Node from `.nvmrc`:

1. Run `.cursor/skills/verify-fold/verify-fold up`, then `doctor` and `env` for its run ID.
2. Run `UI_PAGES=home-signed-out,home UI_WIDTHS=390,768,1280 node .cursor/skills/verify-ui/drive-ui.mjs` to seed and capture the two-group baseline.
3. Run `node .hallmark/evaluate-directory.mjs`. It adds only the named fixtures and refuses unexpected groups. Re-running it does not create duplicates.
4. Use `verify-fold down <run-id>` after inspection; it drops only that isolated database and keeps the evidence.
