# Selected states

A selected filter chip is visibly stronger than an unselected one: violet edge and fill, and a ✓. The unselected chip has a grey edge and a white fill. Group cards no longer carry tag buttons: a card shows its tags as quiet text, and the directory's `Filters` panel owns tag filtering.

## Sub-features
- `home-chips`: the "All" and tag filter chips in the `Filters` panel on `/`, and the selected-filter row above the list.
- `notification-filters`: All, Unread, Waiting and Archived on `/notifications`. `Waiting` shows to admins and to anyone with something waiting.

## How to get to it (user POV)
- On the home page choose `Filters`, then a tag chip.
- Open `/notifications` and choose a filter.

## Driving it with verify-ui
Preconditions: a baseline instance, signed in as the member (the seed has tags `bible-study`, `adults`, `music` and `youth`).
- **Select a tag:** in a Playwright script with the `verify-fold env`, open `/`, click `page.getByRole("button", { name: "Filters", exact: true })`, then `page.getByRole("group", { name: "Group tags", exact: true }).getByRole("button", { name: "adults", exact: true })`. Wait for `aria-pressed="true"` (`getByRole("button", { name: "adults", pressed: true })`); the URL becomes `/?tag=adults` and a `Selected group filters` row shows `Remove adults filter` and `Clear`. Capture `selected-states/home-chip-selected-390.png`, and the unselected panel first as `home-chip-unselected-390.png`.
- **Compare edges:** read `getComputedStyle(el).borderColor` and `backgroundColor` for a selected and an unselected chip (the unselected `music` chip is the control). The selected chip has the primary-border violet edge, a tinted fill and `::before` content `✓`; the unselected one has the grey `border-strong` edge, a white fill and no mark. Run `npm run contrast` in `web/` and confirm the `.chip edge` and `.chip[aria-pressed="true"]` lines pass at 3:1 or more.
- **Notification filters:** open `/notifications` at 390, choose `Archived`: `getByRole("button", { name: "Archived", exact: true, pressed: true })` appears, `All` is no longer pressed and the URL has `filter=archived`. For a member the `Waiting` chip is absent; for an admin it is there. Capture `selected-states/notification-filter-selected-390.png`.

## Gotchas
- At 6cdcf06 the selected chip used `primary-border` violet-300 (1.79:1), weaker than the unselected `border-strong` (3.64:1). `node web/scripts/token-contrast.mjs <that index.css>` fails on that commit's `index.css`.
- `aria-pressed` follows the URL one render later. Waiting on `waitForURL` and then reading the attribute can see the old value; wait on the pressed role instead.
- Both chip states are semibold, so weight is not the difference: edge, fill and ✓ are.
- The chip panel starts closed. A tag button that is not in the `Group tags` group does not exist on the directory.
