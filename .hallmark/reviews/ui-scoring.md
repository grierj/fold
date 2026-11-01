ship

## Review

**Scoring: 2 resolved, 1 partial; no remaining UI blocker.**

| Original finding | Score | Recapture evidence |
|---|---|---|
| **P1 — Tablet Profile navigation overwhelms editing** | **Resolved** | `ui/profile-details-768.png` now starts with the back link, heading and editable profile. `web/src/pages/Profile.tsx:109–114` correctly matches sidebar/back-link visibility to the grid’s `lg` breakpoint. |
| **P2 — Alerts empty state touches its enclosing card** | **Resolved** | `ui/notifications-390.png` shows consistent inset spacing around the heading, description and action. The `px-4` wrapper at `web/src/components/Notifications.tsx:187–194` fixes the affected instance without adding another surface. |
| **P2 — Phone month-calendar density** | **Partial, non-blocking** | `extra/calendar-month-390.png` confirms “Just mine” and Group share a row and the calendar moves upward. However, “Today” remains immediately above the bottom navigation and its result text is not readable in the initial viewport. The changes at `web/src/pages/Calendar.tsx:123–165` materially improve density but do not fully restore the old one-screen result. Retain this as a report-only note, not a release blocker. |

### Additional requested correction

I inspected the per-date editor at **390, 640, 768 and 1280px**. The single-column fields fit their available column; date/time values, labels, visibility choices and Save/Close controls are readable without lateral clipping in these captures. This supports the separate correctness reviewer’s layout fix; it is not an independent behavioral test. <!-- wording-ok: verbatim independent review about corroborating evidence, not a product compatibility promise -->

### Exact recaptures inspected

Under `/tmp/fold-verify-proof/20261101T192757Z-3404058/`:

- `ui/profile-details-768.png`
- `ui/notifications-390.png`
- `extra/calendar-month-390.png`
- `extra/date-edit-390.png`
- `date-editor/date-edit-640.png`
- `date-editor/date-edit-768.png`
- `date-editor/date-edit-1280.png`

No second broad review was performed. No files or data were changed, and no tests were run.

**Independent final UI approval: APPROVED WITH NOTES.**
**Merge verdict for this UI review: OK with notes.**

This is not a clean Hallmark lint certification: the **three existing copy FAILs remain disclosed and unwaived**. They are distinct from the corrected UI defects. The reported 1,367 Vitest passes and 134 browser checks were not independently rerun.

Preserve the compact date-led agenda, explicit RSVP choices, Fold Violet/Inter identity, and existing content and wiring.