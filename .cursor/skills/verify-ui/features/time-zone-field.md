# Time zone field

The shared `ZonePicker` is a searchable combobox with a listbox (DEC-65), not a
native select. Every form except server-rendered setup uses it. The selected
label and the open list must remain usable at 390px.

## Sub-features
- `schedule-event`: a leader's Schedule event form.
- `church-event`: the admin's church-wide event form.
- `admin-setting`: the admin's Church time zone.
- Profile: the person's time zone, using the same control.

## How to get to it (user POV)
- Group page → Schedule event.
- Admin → Schedule a church-wide event.
- Admin → Church time zone.
- Profile → Time zone.

## Driving it with verify-ui

Preconditions: an isolated baseline instance from verify-fold. Follow the skill's
working-directory/env instructions; do not use the shared dev server.

- Run `UI_PAGES=schedule-event,church-event,admin UI_WIDTHS=390 node .cursor/skills/verify-ui/drive-ui.mjs` from the repo root with its environment loaded.
- Inspect the closed control in the 390px screenshots; the generic native-select
  clipping check alone does not prove this combobox fits.
- In Playwright, use `page.getByRole('combobox', {name: 'Time zone', exact: true})`
  (Church time zone for the admin setting). Click to open; type a city or zone
  name to filter; choose `getByRole('option', {name: /New York/})` from the
  Time zones listbox. Do not use `selectOption`.
- Prove arrow keys and Enter choose an option, Escape dismisses without changing
  the saved value, and unmatched text announces No time zone matches.
- Capture the open list and the selected state at 390px; check no sideways scroll,
  clipped option or sub-44px option row. Save/reopen to prove persistence.

## Gotchas

- Event creation defaults to the church zone, not the browser zone (DEC-50).
- The browser zone determines the Yours tag. The driver pins America/Boise;
  inspect the actual option labels rather than hard-coding an old native-select
  label such as Yours & church.
- The listbox is DOM-rendered and **can** be captured headlessly. Setup's native
  select is the explicit exception to the shared control.
