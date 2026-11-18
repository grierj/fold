# Page checks

Every main page renders at phone, tablet and desktop widths without sideways scroll, clipped selects, controls under 24px, or moderate, serious or critical axe issues, and with a screenshot for review.

## Sub-features
- `overflow`: no horizontal scroll at 390/768/1280.
- `targets`: no control under 24×24 px; under-44px controls at 390 are listed.
- `axe`: no moderate, serious or critical axe violations.
- `screens`: a full-page PNG per page and width.

## How to get to it (user POV)
- Signed out: `/`, `/login`, `/join`.
- Member: `/`, `/my-groups`, a group page, an event page, `/profile` and its sections (`details`, `privacy`, `security`, `calendar`, `household`, `guardian`, `invite`, `requests`), `/notifications`.
- Leader: the group page, "Schedule event", `/messages` and the message composer (`/messages/new?group=<id>`).
- Admin: `/admin`, the Users tab, `/groups/new`, `/events/new`.

## Driving it with verify-ui
Preconditions: a baseline instance (the seed runs automatically).
- **Run all:** `node .cursor/skills/verify-ui/drive-ui.mjs`. Every line reads `ok`, and the exit code is 0. The run covers the 27 page ids in `SKILL.md`'s table, each at 390, 768 and 1280, so 81 lines.
- **One page:** `UI_PAGES=profile node .cursor/skills/verify-ui/drive-ui.mjs`. This produces `profile-390.png`, `profile-768.png` and `profile-1280.png`. Each id in `UI_PAGES` is an exact page id from the table (`profile-household`, `compose-long-name`, `admin-users`).
- **Read the page, not just the line:** open the 390 screenshots in `$VERIFY_EVIDENCE/ui/` for any page the change touched. The sticky header is part of a full-page capture and can sit in the middle of a long page.

## Gotchas
- A page that waits forever usually means a missing seed precondition. Report it; don't remove the page.
- The axe page list here is wider than `web/e2e/a11y.spec.ts`. A failure on a page that spec doesn't cover is real.
- The target check counts a checkbox or radio's `<label for>` as part of its target. An unlabeled 13px radio fails.
- The pages wait on a visible handle, not on a time. The time zone pages wait for the `Change time zone` button, since the picker itself stays closed until it is chosen.
- `drive-ui.mjs` seeds `verify-leader@example.com` and `verify-member@example.com`. A verify-fold recipe that created other people (`sam-member@example.com`) on the same instance leaves them there; they do not affect the checks.
