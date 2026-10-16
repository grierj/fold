# Selected states

A selected filter chip or card tag is visibly stronger than an unselected one: violet edge or fill, semibold, and a ✓.

## Sub-features
- `home-chips`: the "All" and tag filter chips on `/`.
- `card-tags`: tag buttons inside group cards (`aria-pressed`).
- `notification-filters`: All, Unread, Waiting and Archived on `/notifications`.

## How to get to it (user POV)
- Click a tag chip under the search box on the home page.
- Click a tag inside a group card.
- Open `/notifications` and choose a filter.

## Driving it with verify-ui
Preconditions: a baseline instance, signed in as the member (the seed has tags `bible-study` and `adults`).
- **Select a tag:** in a Playwright script with the `verify-fold env`, click `page.getByRole("button", { name: "adults" }).first()`. Expect `aria-pressed="true"`. Capture `selected-states/home-chip-selected-390.png`.
- **Compare edges:** read `getComputedStyle(el).borderColor` for a selected and an unselected chip. Run `npm run contrast` in `web/` and confirm the `primary-border` and `.chip[aria-pressed="true"]` lines pass at 3:1 or more.

## Gotchas
- At 6cdcf06 the selected chip used `primary-border` violet-300 (1.79:1), weaker than the unselected `border-strong` (3.64:1). `node web/scripts/token-contrast.mjs <that index.css>` fails on that commit's `index.css`.
