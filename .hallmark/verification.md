# Fold redesign verification

Branch: `design/hallmark-fold`. Baseline: `82323bf`. User approved proceeding after pre-flight.
No backend, schema, deployment, API, route or permission-policy changes. Initial redesign through `e71a8e7` changed no test source. The user then explicitly requested fixing the three deferred failures and thumbnail performance: that follow-up updates two exact copy expectations and adds nine regression cases without weakening behavior assertions.
`ui-ratchet.json` allowances only decreased. No project dependency added.

## Delivered direction

- **Shape:** working app, not a marketing page. Compact date-led agenda beside the group directory on desktop.
- **Theme:** retained Fold Violet, self-hosted Inter, warm surfaces, existing group assets and illustrations.
- **Chrome:** existing desktop destinations and five mobile tabs. Existing buttons and fields retain their APIs.
- **Signature:** date, event and RSVP read together. Group covers are smaller, not replaced with invented material.
- **Sections:** less nested framing across groups, events, account, messages and organizer views.
- **Corrections:** per-date forms stack within their narrower column; tablet profile navigation matches the grid breakpoint; Alerts empty content has inset spacing; calendar toolbar and selected-day spacing are compact.
- **Sweep:** rendered baseline **40 FAIL / 9 REFLEX** → initially **3 FAIL / 0 REFLEX** → follow-up **0 FAIL / 0 REFLEX / 0 WARN**. One retained brand waiver (7b, nine theme-role occurrences), six false positives dismissed and 22 Finish answers. These are the scanner's results, not a claim that automated checks cover every state.

## Follow-up resolution and retained decisions

The user explicitly authorized resolving the deferred failures. None was hidden or waived as a Floor pass:

| Finding | Resolution |
| --- | --- |
| Gate 19, `web/src/components/ImageUpload.tsx:127` | “Tap or click the image to move the focal point — faces stay centered when cropped.” → “Tap or click the image to move the focal point. Faces stay centered when cropped.” Upload, Clear and focal-point tests remain. |
| Gate 19, `web/src/components/calendar/EventRow.tsx:74` | A range such as “7:00 PM – 8:30 PM” → “7:00 PM to 8:30 PM”. Both independently formatted endpoints and browser time zone remain. Added same-period, noon and midnight regression cases. |
| Gate 19, `web/src/components/calendar/WeekView.tsx:29` | The decorative dash plus hidden “No events” → visible “No events”. This is also the recommended clarity improvement; empty columns stay compact. The initial report incorrectly described this flag as week-range formatting. |
| Gate 28a, `web/src/components/GroupCard.tsx` | Only the first card in Home, My groups or its mutually exclusive MeetingSoon list gets eager/high-priority loading; later cards stay lazy/auto with async decoding. Explicit dimensions preserve the existing square crop and focal point. Tests cover uploaded/pattern covers, signed-in/out Home, filtering and both My groups lists. |
| Judged gate 63 | Existing test-contracted date metadata separators remain. Calendar dots convey named RSVP states and have a text key; they are not decoration. |
| Reflex 7b | One recorded waiver retains the approved white/black semantic role values and contrast pairs. |
| WARN 70 (six occurrences) | Dismissed after inspecting the existing awaited API/onSubmit paths. These are real requests; the scanner cannot follow imported functions. |
| F1 and F24 (22 occurrences) | Answered: retain existing prose/test contractions and modal boundary/elevation treatments. |

The retained group-cover scrim and inverse action are also an existing contrast/test contract, not a newly invented image treatment.

## Independent review

Both reviewers were read-only and used supplied source diffs and browser captures. Their test-pass statements refer to the parent run, not independent test execution.

- **UI initial review:** `bc926511-5719-431f-9ba6-b7ab68343524`, verdict `fix`. Found tablet profile hierarchy, empty Alerts padding and phone calendar density.
- **UI scoring review:** `9609df2a-c2b8-4b6d-867c-3676ed43156e`, verdict **`ship`**, **APPROVED WITH NOTES**. Two findings resolved; calendar density partial/non-blocking. The parent then removed extra calendar-header and selected-day spacing; the final 390px recapture shows “Today” and its result above the fixed navigation. This last spacing correction was parent-verified, not rescored by a second broad review.
- **Correctness review:** `026b5565-40e3-408a-9949-89c86651c47b`, **“Merge verdict: OK — correctness approved for the reviewed current state.”** Found real date/time clipping that automatic checks missed. Stacking DateEditor fields widened them from 105.6–131.2px to 340.8–417.6px at 640/768/1280. Reviewer confirmed the correction, permission gates, RSVP handlers, editors and profile children. No remaining correctness issue identified.

- **Follow-up UI/correctness/performance review:** `fa9fe2c6-bf85-4e2e-a0c1-aa51439ccc43`, verdict **`ship`**, explicit **OK**, no blockers. Checked visible empty-day labels, both time endpoints, selective image loading, meaningful test changes and uploaded-image browser evidence. Full report: `.hallmark/reviews/followup.md`.

Full reports are retained in `.hallmark/reviews/`. Older reviews describe their historical snapshot. Review approval is not CI or permission to merge; nothing was pushed or merged.

## Executed checks

- **137 Vitest files, 1,376 tests passed**, rerun after the follow-up (nine added cases).
- TypeScript, Knip, SPA production build, `go build ./...`, `go vet ./...`, `make lint-go` passed.
- Selected Go source guards passed: theme tokens, UI ratchets, React form validation and documentation wording.
- Theme contrast passed. Follow-up eager bundle **172.7 kB gzip**, budget 180 kB.
- Initial integrated verification: **75 standard + 56 supplemental + 3 targeted date-editor checks = 134 checks**, zero hard failures. The targeted set repeats 1280px intentionally. Standard widths 390/768/1280; supplemental 390/1280; date editor 640/768/1280.
- Initial calendar-spacing correction: **9 calendar checks** at 390/768/1280 and one signed-out Home check, zero hard failures.
- Follow-up: **39 browser checks** at 390/768/1280, zero hard failures: 15 standard, nine calendar and 15 uploaded-image/editor/populated-week checks. The custom driver needed an explicit browser context for axe; after that harness correction, it completed successfully.
- Real uploaded PNG fixtures (the existing Fold icon, not proposed photography) exercised both cover requests and avatar editing. Cold-cache CDP records first-cover requests at **High** priority on Home and My groups at all three widths, with later covers still lazy/auto (recorded request priority Low). This verifies scheduling, **not a quantified production LCP improvement**. Native lazy loading may fetch nearby offscreen images. Fixtures were removed from group covers before the final public-page comparison.
- Browser checks include axe moderate/serious/critical findings, horizontal overflow, clipped controls and sub-24px targets. Main 390px checks found no sub-44px controls. These checks are not proof of every possible state; the date-editor issue required visual review.
- Hallmark rendered five public routes at 1280, 1440 and 1920 folds plus 1440/375 full pages. Kept-content check: **98/98 links, 19/19 fields, 547/547 handlers, 1/1 test hooks, 13/13 headings, 3/3 figures**.
- All verification used isolated `fold_verify_*` databases; never the shared development database.
- **Not run:** the full Go database suite, full Playwright end-to-end suite, production deployment or production-network performance benchmarks.

The standard verifier has a stale profile-requests heading locator. It was excluded there and covered by the supplemental runner without changing tests or adding duplicate headings.

## Evidence on this machine

Screenshots and raw logs are intentionally untracked. Verification instances can be stopped without deleting evidence.

| Evidence | Path |
| --- | --- |
| Original 75-screen baseline | `/tmp/fold-verify-proof/20261101T185403Z-3129426/ui/` |
| Rendered/supplemental baseline | `/tmp/fold-verify-proof/20261101T190552Z-3221574/` |
| Integrated corrected UI, supplemental states, per-date editor | `/tmp/fold-verify-proof/20261101T192757Z-3404058/{ui,extra,date-editor}/` |
| Final calendar correction and LCP measurements | `/tmp/fold-verify-proof/20261101T193330Z-3450234/{extra,lcp.json}` |
| Follow-up checks, uploaded images, network priorities and populated week | `/tmp/fold-verify-proof/20261101T204553Z-3995714/{ui,extra,thumbnails}/` |
| Hallmark visual comparison (updated through follow-up) | `.hallmark/compare.html` and `.hallmark/{before,shots}/` |
| Hallmark scan | `.hallmark/after-lint.log` |
| Initial test/static logs | `/tmp/fold-hallmark-{vitest,guards,lint-go,contrast,bundle}.log` |
| Follow-up logs | `/tmp/fold-hallmark-followup-{tests,guards,lint-go,web,ui,extra,thumbnails}.log` |

`compare-origins.mjs` normalizes only isolated-server origins and screenshot filenames for comparison; it preserves the original inventory and captured contents.

Reproduce with Node from `.nvmrc`, `.cursor/skills/verify-fold/verify-fold up`, then its `env` command and `.cursor/skills/verify-ui/drive-ui.mjs`. Supplemental coverage is `.hallmark/verify-extra.mjs` (optional `UI_PAGES`, `UI_WIDTHS`, `VERIFY_EXTRA_DIR`). It requires the standard runner's seeded `ui/results.json`. The follow-up upload/network proof is `.hallmark/verify-thumbnails.mjs`, using the same isolated environment and seed. Read both verification skills first. Run Vite builds before Go compilation, not concurrently with it, because Go embeds the output files.

## Judged finish and material

The app-specific clauses govern: there is no landing-page hero, arbitrary section count or word-budget deletion. Existing working forms are not fake sends. No invented metric, person, testimonial, photo, success state or decorative browser frame. No new animation/reveal; content is not hidden by animation, although the existing SPA still requires JavaScript. Tokenized colors/fonts, capped container, visible focus and reduced-motion foundations remain. Different pages share compositions intentionally rather than acquiring unrelated themes.

The strongest material is already present: actual dates, groups and explicit RSVP choices. No new photo slot was introduced. The most useful optional material is genuine group photography for the existing covers, cropped through Fold's existing focal-point control; no stock-photo substitution is needed.
