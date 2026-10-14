# Event visibility

Each event has its own visibility, copied from its group when it is scheduled. A leader can make one event narrower or wider than the group. When the group becomes more private, events that only inherited its visibility tighten, and the leader must decide whether each event they opened wider stays open or tightens.

## Sub-features

- `event-visibility-narrow` sets one event narrower than its group; people outside the new visibility no longer see it on the group page.
- `event-visibility-open` sets an event wider than its group; the page says a leader opened it.
- `group-tighten-review` making the group more private shows a review dialog; inherited events are listed to match, and the confirm button stays disabled until every leader-opened event has `Keep` or `Tighten`.

## How to get to it (user POV)

- On a group page you lead, each event series shows `Who can see this event` with the `Event visibility` select and `Save visibility`.
- On `/events/<id>`, the same control appears for a leader.
- On a group page you lead, `Edit group`, pick a more private option under `Who can see this group`, and `Save changes` opens the review dialog.

## Driving it with verify-fold

Preconditions:

- `verify-fold doctor "$RUN_ID"` prints `ok`.
- `Verify Admin` is signed in and leads a public group with one scheduled event (the schedule-event recipe leaves `Room 201 class` with `Wednesday class`).
- Eval `verify-fold env "$RUN_ID"`. Load Playwright with `createRequire` from `$FOLD_ROOT/web/package.json`.
- Save files under `$VERIFY_EVIDENCE/event-visibility/`.

- **Narrow one event.** On the group page run `page.getByLabel("Event visibility").selectOption("invite_only")` and `page.getByRole("button", { name: "Save visibility" }).click()`. A dialog `Making this event invite-only` lists the event and any registrations it cancels; nothing is saved yet. Choose `page.getByRole("dialog").getByRole("button", { name: "Make this event invite-only" })`. The select reads `invite_only`. Choose `Log out` and open the same group URL. The level-1 group heading is visible and `Wednesday class` is not.
- **Open one event wider.** Create a group, schedule an event, then `Edit group`, check `page.getByRole("radio", { name: /^Private/ })`, and `Save changes`. A dialog lists the event to match the group; confirm with `page.getByRole("dialog").getByRole("button", { name: /Make the group/ })`. Set `Event visibility` to `public` and save. The text `A leader opened this event wider than the group.` appears.
- **Review on tightening.** `Edit group`, check `/^Invite only/`, `Save changes`. The dialog text `Making the group invite-only changes some events` is visible and `Make the group invite-only` is disabled. Check `page.getByRole("dialog").getByRole("radio", { name: /Keep it public/ })`. The button enables; choose it. The dialog closes and the event's select still reads `public`.
- **Proof.** `event-visibility/signed-out-hidden.png` shows the group heading without the narrowed event. `event-visibility/review.png` shows the dialog with the disabled confirm. `event-visibility/kept-open.png` shows the kept-open event. `event-visibility/result.txt` names the sub-feature and entry point.

## Gotchas

- Public to private with an inherited event also opens the dialog (that event will match the group). Wait for the dialog with `waitFor()`; a `count()` check races it.
- An event set equal to its group's visibility is not leader-opened, so tightening the group tightens it with no question.
- Registrants who would lose access are counted in the dialog (`N person registered will be cancelled and notified`); they get a `Registration cancelled: <title>` notice that names neither the group nor its place.
