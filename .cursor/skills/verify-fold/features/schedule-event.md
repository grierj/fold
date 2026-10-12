# Schedule an event

Schedule an event lets a group leader add a dated event. The group page then shows that event's title and time.

## Sub-features

- `schedule-open` opens the new-event form from the group page.
- `schedule-save` saves a title, date, and start time and returns to the group.
- `schedule-visible` shows the saved event on the group page.

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

- **Open the form.** From `/`, choose the `Room 201 class` heading, then `Schedule event`. Run `page.getByRole("heading", { name: "Room 201 class" }).click()` and `page.getByRole("link", { name: "Schedule event" }).click()`. The URL matches `/groups/<id>/events/new`. `Time zone` is `America/New_York` when the browser zone is that zone, not the church zone `America/Boise`.
- **Enter the event.** Fill `Title (optional)` with `Wednesday class`, `Date` with a `YYYY-MM-DD` at least one day ahead, `Start time` with `19:00`, and `Time zone` with `America/Boise`. Run `page.getByLabel("Title (optional)").fill("Wednesday class")`, `page.getByLabel("Date").fill("<ymd>")`, `page.getByLabel("Start time").fill("19:00")`, and `page.getByLabel("Time zone").fill("America/Boise")`.
- **Save.** Choose `Schedule event`. Run `page.getByRole("button", { name: "Schedule event" }).click()`. The URL returns to `/groups/<id>`. The text `Wednesday class` is visible, and the time reads `7:00 PM` followed by `MDT` or `MST`.
- **Proof.** `schedule-event/group.png` shows the group heading `Room 201 class` and the text `Wednesday class`. `schedule-event/result.txt` names `schedule-save` and the `Schedule event` link as the entry point. Reload the group page and confirm `Wednesday class` is still there.

## Gotchas

- `Schedule event` is a link on the group page and the submit button on the form. Choose the button only after the fields are filled.
- The form's initial time zone is the browser's zone. A proof that assumes `America/Boise` before the field is edited is wrong.
- `Title (optional)`, `Date`, `Start time`, and `Time zone` are labeled. Use `getByLabel`.
- A member who does not lead the group does not see the `Schedule event` link. Sign in as the leader.
- The directory's `Coming up` list is a different entry point. Seeing the event only there does not prove the group page.
