# Notifications

Signed-in people get stored notifications in the bell, called `Alerts`. The badge is one count: unread items plus items still waiting on you. An admin sees each member's new group as a waiting item with an inline Approve; approving resolves every admin's copy. The × archives an item on the server, with an Undo toast, and the Archived filter on `/notifications` brings it back after signing out.

## Sub-features

- `notif-badge` the bell's accessible name carries the counts (`1 Alerts, 1 waiting on you`); the badge shows nothing at zero and `9+` above nine.
- `notif-approve` an admin approves a pending group from the bell; the row then reads `You approved this` and the bell drops `waiting on you`.
- `notif-archive` × archives a row, `Undo` restores it, and an archived row stays archived after logging out and back in, under `Archived` on `/notifications`.
- `notif-mobile` below 640 px wide the header has no bell; the `Alerts` tab opens `/notifications` instead of the popover.

## How to get to it (user POV)

- The bell button in the header (between `Admin` and the profile link), named `Alerts…`.
- The panel's `See all alerts` link, or `/notifications` directly, with the chips `All`, `Unread`, `Waiting` (shown to admins and to anyone with something waiting) and `Archived`.
- On a phone-width window, the `Alerts` tab in the bottom tab bar.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`, and the sign-in recipe has created `Verify Admin`.
- As `Verify Admin`, create a member with a password the way [start-a-group.md](./start-a-group.md) `Member create` does (`sam-member@example.com`, `Sam Member`, `password1234`), so there is someone to start a group.
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/notifications/`.

- **A waiting item.** Sign in as the member, open `/groups/new`, fill `Group name` and choose `Create group`; the page says `Thanks! An admin will review “<name>”.` Log out and sign in as `Verify Admin`. `page.getByRole("button", { name: /Alerts/ })` has the accessible name matching `/1 waiting on you/` (the name is `1 Alerts, 1 waiting on you`) and the badge shows `1`. The name settles a moment after the page loads, so wait for it (`getByRole("button", { name: /1 waiting on you/ })`). Click it; the panel `#notif-panel` has the heading `Alerts`, a section `Waiting on you` and a row `<member> started a group: <name>`.
- **Approve inline.** In that row choose `getByRole("button", { name: "Approve" })`. The row reads `You approved this`; the bell's name no longer matches `/waiting on you/`. `/admin?tab=approvals` no longer lists the group.
- **Archive and Undo.** A resolved item is ordinary, so the approved row now has `Archive: <title>`. Choose it; the row disappears and a toast `Archived · Undo` appears. Choose `Undo`; the row is back. Archive it again, choose `Log out`, sign in again, open `/notifications`, choose `Archived`; the row is listed and the URL has `filter=archived`. `Restore: <title>` moves it back to `All`.
- **Phone width.** `page.setViewportSize({ width: 390, height: 800 })`, open `/`, choose `page.getByRole("navigation", { name: "Tabs" }).getByRole("link", { name: /Alerts/ })`; the URL is `/notifications` and `All` is pressed (`getByRole("button", { name: "All", pressed: true })`).
- **Proof.** `notifications/waiting.png` (panel with the waiting row and the badge), `notifications/approved.png` (the row after Approve), `notifications/archived-after-relogin.png` (the Archived filter after signing in again), `notifications/badge.json` (the body of `GET /api/v1/notifications/badge` from the same browser), `notifications/storage.txt` (`localStorage.length + sessionStorage.length`, expected `0`), and `notifications/result.txt` naming each sub-feature and entry point.

## Gotchas

- `Mark all as read` is `aria-disabled` when only waiting items are unread; Playwright treats that as disabled and a click waits forever.
- The creator of a group never gets its approval item, and an admin's own actions never notify that admin. Use two accounts.
- A public group a member leads shows that member's name on the signed-out home page; use a private group for scratch data if a later recipe checks the signed-out page.
- The bell refreshes on focus, on navigation and every 60 seconds; after an API-side change, navigate (or focus the window) before asserting the badge.
- The `Waiting` chip is absent for a member with nothing waiting.
- `aria-pressed` on a filter chip follows the URL one render later: use `getByRole("button", { name: "Archived", pressed: true })` to wait for it.
