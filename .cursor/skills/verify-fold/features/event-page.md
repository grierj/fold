# Event page

A signed-in member answers each date with a Going / Maybe / Can't go segmented control. Each date shows its counts first and, when the viewer may see names, a "Show who's coming" expand. "Can't go" is never among attendees; only the event's leaders and admins see a can't-go count and names (DEC-64). On a group page a series shows only its next date plus a "See all N dates" link. Leaders see one primary action per view and a More menu for the rest.

## Sub-features

- `rsvp`: the segmented control, with the chosen answer pressed.
- `attendance`: counts first, names behind the expand, Can't go never listed.
- `series`: next date plus `See all N dates` (`See more dates` when the list is cut short) on the group page.
- `more-menu`: a leader's group header and date actions in More menus, from the keyboard.
- `manage-sign-ups`: on the event page, a date's More menu → `Manage sign-ups` opens a panel with an `Add someone` field.

## How to get to it (user POV)

- A group page (`/groups/<id>`) → `Upcoming events`.
- An event page (`/events/<id>`), from a date's title on `/` or `/calendar`, or from `See all N dates`.

## Driving it with verify-fold

Preconditions: the baseline, and these through the admin's own pages:

- Create `lee-leader@example.com` (`Lee Leader`) and `sam-member@example.com` (`Sam Member`) with password `password1234` through `/admin` → `Users` → `New user` (see [start-a-group.md](./start-a-group.md)).
- Create a public group `Sunday supper`. On its page open the `Members` tab (`getByRole("tab", { name: "Members" })`), choose `Add people`, `Use an email instead`, fill `Email address` and choose `Add member` for each person. For Lee, `More for Lee Leader` → `Make leader`, and accept the browser's confirm.
- Sign in as Lee and open `/groups/<id>/events/new`. Fill `Title (optional)` `Segmented night`, `Date` nine days ahead, `Start time` `19:00`, choose `Every week` and `Schedule event`. The form has no end date, so on the group page open the date's `More for Segmented night on <date>` → `Edit series`, fill `Repeat until (optional)` with a date 23 days ahead, and choose `Save series`. The series has three weekly dates.

- **RSVP.** Sign in as the member (390px wide) and open the event page. The first date's `page.getByRole("group", { name: "Your answer" })` holds `Going`, `Maybe` and `Can't go`, each 44px tall, `aria-pressed="false"`, and the date reads `No one yet`. Choose `Maybe`: a dialog `How far should “Maybe” apply?` offers `Just this date`, `This and following dates` and `All future dates`; choose `Just this date`. `Maybe` becomes `aria-pressed="true"` with a leading check mark, and the date reads `0 going · 1 maybe`. Save `event-page/rsvp.png`.
- **Attendance.** Choose `Show who's coming` (`aria-expanded` turns `true`, the button reads `Hide names`): the list shows `Sam Member` under `Maybe (1)`. Choose `Can't go` and `Just this date` in its dialog: the date reads `No one yet` and the name is gone. Save `event-page/cant-go.png`.
- **Series.** Open the group page. The series is one card with a `See all 3 dates` link (for three weekly dates, all loaded) that opens the event page. A series whose dates are not all loaded says `See more dates`; a long series loads 25 dates at a time (`Load more dates`).
- **More menu.** Sign in as the leader at 390px and open the group page. The header shows `Schedule event` and `More group actions`, and no `Edit group` button. Focus `More group actions` and press Enter: `aria-expanded` is `true`, focus is on `Edit group`, the menu is fully on screen. ArrowDown moves to `Email members` (then `Change cover photo` and `Leave group`); Escape closes it and focus returns to the trigger. Save `event-page/more-menu.png` with it open.
- **Manage sign-ups.** As the leader on the event page, open a date's `More for …` menu and choose `Manage sign-ups` (from the group page the same item opens `/events/<id>?manage=<date>`). The `Manage sign-ups` region lists who signed up (with Promote and Remove) and, behind `Add person` (open while the list is empty), a `Find a person by name` field and an `Add someone` email field above `Add`. Save `event-page/manage-sign-ups.png`.

## Gotchas

- Signed in as the leader or an admin, a date with a Can't go answer reads `· 1 can't go` and the expand lists the name under `Can't go`. A member never sees either; that's the rule, not a bug.

- A member of an invite-only group's opened event, or anyone outside such a group, gets counts only: no `Show who's coming` button. That is the permission rule, not a bug.
- Guests on the signed-out form register as `Going` only (DEC-56); the segmented control is for signed-in people.
- The event page lists every date; only the group page collapses a series.
- A series answer asks its scope first. A single-date event answers at once, with no dialog.
- The scope dialog's `Cancel` leaves the answer as it was.
