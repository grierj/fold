# Fold design: 8. Calendar and ICS feeds

## 8. Calendar and ICS feeds (F2)

### 8.1 In-app views

- `/calendar` (M1.6, DEC-67) shows every date the viewer may see, as Agenda (the default, a list grouped by day that loads 30 days at a time from a "From date"), Week or Month. Filters: "Just mine", a group (or church-wide events), and Remote seats and Childcare chips, shown only when something in view has them (DEC-99). Admins can also show deleted groups' events (DEC-68). Times are in the viewer's zone (DEC-57). The URL holds the state. It reads `GET /api/v1/calendar`, which returns slim rows with no attendance (`api_calendar.go`).
- Answers on the calendar (M1.14): the words are "Going", "Maybe", "Can't go", "Waitlisted" and "Not answered"; `declined` never appears in copy, and a waitlisted date shows as Going with a "Waitlisted" label. Dates you haven't answered are drawn with a dashed border and a text label, never by fading the text, and each row's accessible name carries its status. On phones, Month view marks days with hollow (unanswered) and filled (answered) dots. Status is never color alone.
- By room (`/calendar/rooms`, signed-in adults, when rooms are on; M2.2a): one day as a column per room on a time grid, or one week as rooms by days (DEC-108); phones show one room at a time as a day list. Bookings the viewer cannot see show as grey "Reserved" blocks with no title. Restricted rooms are listed for admins only (DEC-104), and only admins see bookings waiting for approval (DEC-109).

### 8.2 Feeds

| Feed | URL | Auth | Contents |
|---|---|---|---|
| Public church feed | `/cal/public.ics` | none | The dates a signed-out visitor may see. No attendee data. Video links only if `video_reveal=always`. |
| Public group feed | `/cal/groups/<id>.ics` | none; active public groups only | That group's dates a signed-out visitor may see |
| Personal feed | `/cal/u/<token>.ics` | secret token in the URL | "My events" (`mine`, the default) or "Everything I can see" (`all`), set on the profile (DEC-72, DEC-73); see below. Video links when the user may see them (DEC-70). |
| One date (DEC-93) | `/api/v1/events/<id>/dates/<date>/calendar.ics` | the date page's own access (signed out for public dates) | A one-time download of that date, with exactly what the date page shows; Going is `CONFIRMED`, Maybe and Waitlisted `TENTATIVE`. Not a subscription. |
| Room feed (M2.2b) | `/cal/rooms/<roomId>/<token>.ics` | admin-issued token | Confirmed bookings for the room on scheduled dates. A booking's details show only when a signed-out viewer could see that date, the same rule as the public feed (`policy.CanSeeEvent` with no viewer): a church-wide event, or an active, non-deleted group, and a public effective visibility. Every other confirmed booking, including one whose group was archived or set back to pending after it was made (DEC-147), is titled "Reserved". Times are the event's, without setup and teardown. |

Personal-feed scope (DEC-71, DEC-73, DEC-77; `policy.DecideCalendar`):

- `mine` holds dates the user answered, every date of groups they belong to, church-wide dates, events they host (DEC-7), dates their household people are registered for (DEC-8), and dates opened to them on their own. An unanswered church-wide date appears only if it starts in the next 30 days.
- `all` adds every other date the user may see. An unanswered public date of a group they are not in appears only if it starts in the next 30 days.
- Can't go dates are omitted from both.
- Status follows the **viewer's own answer**: Going is `CONFIRMED`; Maybe, Waitlisted and Not answered are `TENTATIVE`. Household registrations supply scope, not the viewer's RSVP: a household-only Going date is `TENTATIVE` when the viewer has not answered. The viewer's Can't go omits the date even when a household person is Going, without changing that person's registration. Video eligibility is separate from this status.

Token rules:

- 32 random bytes, base64url, shown once. Only the SHA-256 hash is stored.
- Users can regenerate their token (which revokes the old one), change its scope, or turn it off from their profile (`/api/v1/me/calendar-feed`). Admins can revoke anyone's. Each change is audited.
- Feed contents follow the user's current permissions at fetch time. If someone leaves an invite-only group, its events drop out of their feed on the next refresh.
- Tokens of disabled or deleted users stop working, with the same 404 as an unknown token.
- Responses carry an `ETag` (and answer `304` to a matching `If-None-Match`) and `Cache-Control: private, max-age=900`. Feeds cover 30 days back to 12 months ahead.

ICS details (`internal/events/ics.go`, `date_projector.go`):

- Each authorized scheduled date whose start falls in the half-open feed window is one standalone `VEVENT`. Fold emits no `RRULE`, `EXDATE`, `RDATE`, or `RECURRENCE-ID`: the app expands recurrence, so a calendar client can never bring back a hidden, declined or out-of-window date. Parse and golden-file tests check the output.
- `UID` = `event-<id>-<originalDate without hyphens>@<SITE_URL host>`. A rescheduled date keeps its UID and shows its new time; a date that moves out of the window or is cancelled drops out on the next refresh.
- `DESCRIPTION` holds the plain-text event `description` when the viewer may see the series, the date's page URL, "Remote attendance available", "Childcare available" (with ages), and "Join by video" with the link when allowed. `LOCATION` holds only places the viewer may see (a booked room's name first); an explicitly empty date location stays empty. A date opened on its own carries only what its date page shows: no parent description or childcare details, and a video link only when that page shows one. Remote attendance is mentioned when the date offers it: remote on with unlimited or positive remote capacity, even when its remote seats are full. Registration care notes never appear.
- `VTIMEZONE` blocks cover the export window and the emitted dates' clocks. Unchanged data produces the same bytes and ETag. Clients control refresh timing and may keep dates they cached earlier.
