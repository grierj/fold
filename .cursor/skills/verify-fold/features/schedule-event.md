# Schedule an event

Schedule an event lets a group leader add a dated event. The group page then shows that event's title and its time in the viewer's own zone (DEC-57).

## Sub-features

- `schedule-open` opens the new-event form from the group page.
- `schedule-save` saves a title, date, and start time and returns to the group.
- `schedule-visible` shows the saved event on the group page.
- `schedule-local-time` shows the time converted to the browser's zone, and the event page adds one "Held in" line naming the event's zone.

## How to get to it (user POV)

- Open a group you lead and choose `Schedule event`.
- Open `/groups/<id>/events/new` for that group.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- `Verify Admin` is signed in and leads a group named `Room 201 class` (create it with the directory recipe if it is missing).
- The browser context time zone is `America/New_York`, matching `web/e2e/events.spec.ts`.
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/schedule-event/`.

- **Open the form.** From `/`, search and choose the `Room 201 class` heading, then `Schedule event`. Run `page.getByLabel("Search groups").fill("Room 201 class")`, `page.getByRole("heading", { name: "Room 201 class" }).click()` and `page.getByRole("link", { name: "Schedule event" }).first().click()`. The URL matches `/groups/<id>/events/new` and the heading reads `Schedule an event`. The form names the zone on one line, `Mountain time · Change`: the church zone `America/Boise` (DEC-50). Choose `page.getByRole("button", { name: "Change time zone", exact: true })` to open `Time zone`, a searchable combobox that starts at `Boise (Mountain)`. Its list puts the browser zone first, tagged `Yours`, and Boise second, tagged `Church`.
- **Enter the event.** Fill `Title (optional)` with `Wednesday class`, `Date` with a `YYYY-MM-DD` at least one day ahead, `Start time` with `19:00`. Leave the zone at the church zone. Run `page.getByLabel("Title (optional)").fill("Wednesday class")`, `page.getByLabel("Date", { exact: true }).fill("<ymd>")`, `page.getByLabel("Start time").fill("19:00")` (to pick another zone, open `Change time zone`, fill `page.getByRole("combobox", { name: "Time zone", exact: true })` with a city and choose the matching `getByRole("option")`).
- **Save.** Choose `Schedule event`. Run `page.getByRole("button", { name: "Schedule event" }).click()`. The URL returns to `/groups/<id>`. The card `page.getByRole("article", { name: "Wednesday class", exact: true })` is visible, and its time reads `9:00 PM` (7 PM in Boise, converted to New York) with no zone label. When the page has many dates a card can show `Details and RSVP: Wednesday class, …` first; choose it to see the time.
- **Local time on the event page.** A one-date event has no link from the group page. Open `/`, where `Coming up for you` lists it, and choose `page.getByRole("link", { name: /Wednesday class/ })` (the calendar at `/calendar` lists it too). The page shows `Saturday, Dec 5 · 9:00–10:30 PM` in the header line and, under the title, `Held in Mountain time (7:00 PM MST)` (`MDT` before early November). The line gains a weekday, as in `(Sat, 10:30 PM MDT)`, when the event's date differs in the viewer's zone. Open the date's `More for <date>` menu and choose `Edit this date`: the form says `Times are in Mountain time.` and `Start time` is `19:00`, the event's own clock. Save `schedule-event/event-page.png`.
- **Proof.** `schedule-event/group.png` shows the group heading `Room 201 class` and the text `Wednesday class`. `schedule-event/result.txt` names `schedule-save` and the `Schedule event` link as the entry point. Reload the group page and confirm `Wednesday class` is still there.

## Gotchas

- `Schedule event` is a link on the group page and the submit button on the form. Choose the button only after the fields are filled. A group with no upcoming dates shows two links named `Schedule event` (the header and the empty state's action); use `.first()`.
- The form's initial time zone is the church's zone, not the browser's (DEC-50). `Time zone` is the shared ZonePicker combobox and only exists after `Change time zone` is chosen: filter with `fill`, then select an option or use arrow keys and Enter. Do not use `selectOption`.
- `Title (optional)`, `Date`, `Start time`, and `Time zone` are labeled. Use `getByLabel`, with `{ exact: true }` for `Date`.
- A member who does not lead the group does not see the `Schedule event` link. Sign in as the leader.
- A browser in the event's zone (or one with the same clock, like Denver for Boise) shows no "Held in" line. Use a different zone to see it.
- The form has no end date for a weekly series (`Every week`, then `Repeat on`). A leader sets `Repeat until` afterwards, in `Edit series`.
- The directory's `Coming up for you` list and the calendar are different entry points. Seeing the event only there does not prove the group page.
