# Time zone field

The shared time zone select shows a short, readable label ("Church · Boise (Mountain)") that fits a 390px field on every form that uses it.

## Sub-features
- `schedule-event`: a leader's "Schedule event" form.
- `church-event`: the admin's church-wide event form.
- `admin-setting`: the admin's "Church time zone".

## How to get to it (user POV)
- Group page → "Schedule event".
- Admin → "Schedule a church-wide event".
- Admin → "Church time zone".

## Driving it with verify-ui
Preconditions: a baseline instance.
- **Run:** `UI_PAGES=schedule-event,church-event,admin UI_WIDTHS=390 node .cursor/skills/verify-ui/drive-ui.mjs`. Expect no `clipped select` failure. The screenshots show the whole label.

## Gotchas
- The browser zone decides the "Yours" tag. `drive-ui.mjs` pins `America/Boise`, so the church zone (also Boise) shows "Yours & church · Boise (Mountain)", the longest common label.
- A native select's open list can't be screenshotted headless. The check measures the closed field.
