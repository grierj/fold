# Event page

A signed-in member answers each date with a Going / Maybe / Can't go segmented control. Each date shows its counts first and, when the viewer may see names, a "Show who's coming" expand. "Can't go" is never counted or listed. On a group page a series shows only its next date plus "N more dates". Leaders see one primary action per view and a More menu for the rest.

## Sub-features

- `rsvp`: the segmented control, with the chosen answer pressed.
- `attendance`: counts first, names behind the expand, Can't go never listed.
- `series`: next date plus `N more dates` on the group page.
- `more-menu`: a leader's group header and date actions in More menus, from the keyboard.

## How to get to it (user POV)

- A group page (`/groups/<id>`) → `Upcoming events`.
- An event page (`/events/<id>`), from a date's title or `N more dates`.

## Driving it with verify-fold

Preconditions: the baseline, a group led by `verify-leader@example.com` with a weekly series of at least three dates, and `verify-member@example.com` as a member.

- **RSVP.** Sign in as the member and open the event page. The first date's `page.getByRole("group", { name: "Your answer" })` holds `Going`, `Maybe` and `Can't go`, each 44px tall, `aria-pressed="false"`, and the date reads `No one yet`. Choose `Maybe`: it becomes `aria-pressed="true"` and the date reads `0 going · 1 maybe`. Save `event-page/rsvp.png`.
- **Attendance.** Choose `Show who's coming` (`aria-expanded` turns `true`): the list shows `Sam Member (maybe)`. Choose `Can't go`: the date reads `No one yet` and the name is gone. Save `event-page/cant-go.png`.
- **Series.** Open the group page. The series is one card with a `2 more dates` link (for three dates) that opens the event page with every date.
- **More menu.** Sign in as the leader at 390px and open the group page. The header shows `Schedule event` and `More group actions`, and no `Edit group` button. Focus `More group actions` and press Enter: `aria-expanded` is `true`, focus is on `Edit group`, the menu is fully on screen. ArrowDown moves to `Change cover photo`; Escape closes it and focus returns to the trigger. Save `event-page/more-menu.png` with it open.

## Gotchas

- A member of an invite-only group's opened event, or anyone outside such a group, gets counts only: no `Show who's coming` button. That is the permission rule, not a bug.
- Guests on the signed-out form register as `Going` only (DEC-56); the segmented control is for signed-in people.
- The event page lists every date; only the group page collapses a series.
