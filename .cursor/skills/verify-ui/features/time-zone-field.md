# Time zone field

The shared `ZonePicker` is a searchable combobox with a listbox (DEC-65), not a
native select. Every form except server-rendered setup uses it. The selected
label and the open list must remain usable at 390px. The event forms show the
zone as one line (`Mountain time · Change`) and open the picker from `Change
time zone` (DEC-95); the admin setting and Profile show the picker at rest.

## Sub-features
- `schedule-event`: a leader's Schedule event form.
- `church-event`: the admin's church-wide event form.
- `admin-setting`: the admin's Church time zone (Admin → Settings).
- `profile`: the person's Time zone for emails (Profile → Profile details), using the same control.

## How to get to it (user POV)
- Group page → Schedule event → Change.
- Admin → Schedule a church-wide event (`/events/new`) → Change.
- Admin → Settings → Church time zone.
- Profile → Profile details → Time zone for emails.

## Driving it with verify-ui

Preconditions: an isolated baseline instance from verify-fold. Follow the skill's
working-directory/env instructions; do not use the shared dev server.

- Run `UI_PAGES=schedule-event,church-event UI_WIDTHS=390 node .cursor/skills/verify-ui/drive-ui.mjs` from the repo root with its environment loaded. These two pages load with the `Change time zone` button visible; the combobox is closed, so the generic native-select clipping check alone does not prove it fits. The admin and Profile settings are not in the page list: drive them by hand with the steps below.
- Inspect the closed line (`Mountain time · Change`) in the 390px screenshots. Then, on a form, choose `page.getByRole("button", { name: "Change time zone", exact: true })`.
- In Playwright, use `page.getByRole('combobox', {name: 'Time zone', exact: true})` (`Church time zone` on `/admin?tab=settings`, `Time zone for emails` on `/profile/details`). Click to open; type a city or zone
  name to filter; choose `getByRole('option', {name: /New York/})` from the
  `Time zones` listbox (`getByRole("listbox", { name: "Time zones" })`). Do not use `selectOption`.
- Prove arrow keys and Enter choose an option, Escape closes the list and keeps the current value, and unmatched text announces `No time zone matches “<text>”.`
- Capture the open list and the selected state at 390px; check no sideways scroll, a listbox inside the viewport, and 44px option rows. On the admin setting choose `Save time zone`, wait for `Saved`, reload and confirm the saved value is shown; then restore the church zone.

## Gotchas

- Event creation defaults to the church zone, not the browser zone (DEC-50).
- The browser zone determines the Yours tag. The driver pins America/Boise, which is also the seeded church zone, so the first option is `Boise (Mountain)` tagged `Yours & church` (with another browser zone, `Yours` and `Church` are two separate options). Inspect the actual option labels rather than hard-coding them. The combobox's value is the short form (`Boise (Mountain)`).
- On a form, `getByLabel("Time zone")` also matches the `Change time zone` button by its accessible name. Use the role locators above.
- The listbox is DOM-rendered and **can** be captured headlessly. Setup's native
  select is the explicit exception to the shared control.
- The church zone is a site setting: restore it after a run that changed it.
