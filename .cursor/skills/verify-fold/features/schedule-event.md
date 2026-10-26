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

- **Open the form.** From `/`, choose the `Room 201 class` heading, then `Schedule event`. Run `page.getByRole("heading", { name: "Room 201 class" }).click()` and `page.getByRole("link", { name: "Schedule event" }).click()`. The URL matches `/groups/<id>/events/new`. `Time zone` is a searchable combobox that starts at the church zone `America/Boise` (DEC-50); the list also marks the browser zone.
- **Enter the event.** Fill `Title (optional)` with `Wednesday class`, `Date` with a `YYYY-MM-DD` at least one day ahead, `Start time` with `19:00`. Leave `Time zone` at the church zone. Run `page.getByLabel("Title (optional)").fill("Wednesday class")`, `page.getByLabel("Date").fill("<ymd>")`, `page.getByLabel("Start time").fill("19:00")` (to pick another zone, fill `page.getByRole("combobox", { name: "Time zone", exact: true })` with a city and choose the matching `getByRole("option")`).
- **Save.** Choose `Schedule event`. Run `page.getByRole("button", { name: "Schedule event" }).click()`. The URL returns to `/groups/<id>`. The text `Wednesday class` is visible, and the time reads `9:00 PM` (7 PM in Boise, converted to New York) with no zone label.
- **Local time on the event page.** Choose `Wednesday class` to open `/events/<id>`. The date card reads `9:00 PM` and, under it, `Held in Mountain time (7:00 PM MDT)` (or `MST` after early November). Open the date card's `More` menu and choose `Edit this date`: the form says `Times are in Mountain time.` and `Start time` is `19:00`, the event's own clock. Save `schedule-event/event-page.png`.
- **Proof.** `schedule-event/group.png` shows the group heading `Room 201 class` and the text `Wednesday class`. `schedule-event/result.txt` names `schedule-save` and the `Schedule event` link as the entry point. Reload the group page and confirm `Wednesday class` is still there.

## Gotchas

- `Schedule event` is a link on the group page and the submit button on the form. Choose the button only after the fields are filled.
- The form's initial time zone is the church's zone, not the browser's (DEC-50). `Time zone` is the shared ZonePicker combobox: filter with `fill`, then select an option or use arrow keys and Enter. Do not use `selectOption`.
- `Title (optional)`, `Date`, `Start time`, and `Time zone` are labeled. Use `getByLabel`.
- A member who does not lead the group does not see the `Schedule event` link. Sign in as the leader.
- A browser in the event's zone (or one with the same clock, like Denver for Boise) shows no "Held in" line. Use a different zone to see it.
- The directory's `Church Events` list is a different entry point. Seeing the event only there does not prove the group page.
