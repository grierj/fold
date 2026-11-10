# Fold design: 8. Calendar and ICS feeds

## 8. Calendar and ICS feeds (F2)

### 8.1 In-app views

- Timeline (`/calendar`): an agenda list grouped by day showing every occurrence the viewer may see. Filters: group, tag, "my events", visibility, childcare, remote. Scrolls forward; a "past" toggle shows history.
- Week and month grid views (a small calendar component; no heavy dependency needed).
- By room (`/calendar/rooms`, when rooms are on): one column per room, rows for time, one day or one week at a time. Bookings the viewer cannot see show as grey "Reserved" blocks with no title.

### 8.2 Feeds

| Feed | URL | Auth | Contents |
|---|---|---|---|
| Public church feed | `/cal/public.ics` | none | Published public events. No attendee data. Video links only if `video_reveal=always`. |
| Public group feed | `/cal/groups/<id>.ics` | none; public groups only | That group's public events |
| Personal feed | `/cal/u/<token>.ics` | secret token in the URL | Option A, "My events" (`mine`, default): the viewer's own scope (DEC-72/73) — occurrences the user is `going`, `maybe` or `waitlisted` for, all events of groups they belong to, church-wide dates, events they host (DEC-7), and events their household people are registered for (DEC-8). Option B, "Everything I can see" (`all`): every date the user may see. Both scopes follow DEC-71/77 from M1.14: Going is `CONFIRMED`, Maybe, Not answered and Waitlisted are `TENTATIVE`, Can't go dates are omitted, an unanswered public date of another group appears only in `all` and only within the 30-day window, and an unanswered church-wide date appears only within the window. Video links included when the user may see them. |
| One date (DEC-93) | `/api/v1/events/<id>/dates/<date>/calendar.ics` | the date page's own access (signed out for public dates) | A one-time download of that date, with exactly what the date page shows; Going is `CONFIRMED`, Maybe and Waitlisted `TENTATIVE`. Not a subscription. |
| Room feed | `/cal/rooms/<roomId>/<token>.ics` | admin-issued token | Confirmed bookings for the room. Invite-only and hidden-date bookings are titled "Reserved". |

Personal-feed status follows the **viewer’s own answer** (DEC-71). Household
registrations supply scope, not the viewer’s RSVP: a household-only Going date is
`TENTATIVE` when the viewer has not answered. The viewer’s Can't go omits the date
from both `mine` and `all` even when a household person is Going, without changing
that person’s registration. Video eligibility is separate from this status.

Token rules:

- 32 random bytes, base64url, shown once. Only the SHA-256 hash is stored.
- Users can regenerate their token (which revokes the old one) from their profile. Admins can revoke anyone's.
- Feed contents follow the user's current permissions at fetch time. If someone leaves an invite-only group, its events drop out of their feed on the next refresh.
- Tokens of disabled or deleted users stop working.
- Responses carry an `ETag` and `Cache-Control: private, max-age=900`. Feeds cover 30 days back to 12 months ahead.

ICS details:

- Export one standalone `VEVENT` per authorized scheduled stored occurrence whose actual start falls in the half-open feed window. Do not emit VEVENT `RRULE`, `EXDATE`, `RDATE`, or `RECURRENCE-ID`: recurrence is expanded by the application, never reconstructed by calendar clients beyond loaded visibility facts. Output must pass parse and golden-file tests.
- `UID` = `event-<id>-<originalDate without hyphens>@<SITE_URL host>`. Rescheduling keeps that identity while updating the effective date clock; moving outside the window or cancellation omits the date on the next refresh. This intentionally replaces historical series UIDs before release; there is no compatibility mode.
- `DESCRIPTION` = the plain-text event `description` when series-authorized, plus the authorized exact-date URL. `LOCATION` includes only location fields the viewer may see (room names once rooms exist); an explicitly empty date location stays empty. A hidden parent contributes no description, video metadata or childcare details. Effective remote availability is authorized date participation information: mention it when remote is enabled and the effective remote capacity is unlimited or positive, even on an opened date of a hidden series. Zero capacity hides only the marker; full positive capacity still offers remote attendance. Authorized childcare flags are mentioned; registration care notes never are.
- `VTIMEZONE` coverage follows the export window and emitted clocks, not the series anchor. Unchanged data produces stable bytes and ETags. Clients control refresh timing and may retain previously cached dates.

