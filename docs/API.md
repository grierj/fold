# Fold API — v1 contract

Base path: `/api/v1`. All responses are JSON. Keys are camelCase.
Errors: `{"error": "message"}` with the appropriate status code.
Timestamps are RFC 3339 (`2026-10-06T19:00:00Z`).

## Auth & CSRF

Cookie sessions (same as Phase 0). Every unsafe request (POST/PUT/DELETE)
must send the CSRF token as the **`X-CSRF-Token`** header.

```
GET /api/v1/auth/csrf            → {csrfToken}   (also sets the session cookie)
```

SPA boot flow: `GET /auth/csrf` → store token → send as header on all
writes. 401 responses are JSON `{"error":"unauthorized"}` — the SPA
redirects to login itself (no server redirects except `/setup`).

Every API response carries `Cache-Control: no-store` (so does the SPA
shell), so neither the HTTP cache nor back/forward replays a session
after logout. `POST /auth/logout` expires the session cookie; the SPA then
clears local/session storage and reloads `/`.

## Endpoints

### Auth

```
POST /api/v1/auth/login    {email, password} → 200 {user} | 401 | 429
POST /api/v1/auth/logout                         → 200 {}
GET  /api/v1/auth/me                             → 200 {user} | 401
```

Sign-in is by email in any letter case (`users.email` is `citext`, unique).
Only an `active` account with a password signs in. Every failure, whether
the email is unknown, the password is wrong, or the account is invited or
disabled, answers `401 {"error":"invalid email or password"}` after one
bcrypt comparison, so the response doesn't reveal whether an account
exists. Throttling lives in the `login_throttles` table: 10 failures for
one email within 15 minutes lock that email from every IP, and 50 failures
from one client IP lock that IP, each for 15 minutes. A locked email or IP
gets `429` (checked before the password, so it reveals nothing either). A
successful sign-in clears the email's counter but not the IP's.

### Profile

```
PUT    /api/v1/me           → 200 {user}  (auth; partial update)
POST   /api/v1/me/password  → 200 {}  (auth)
POST   /api/v1/me/avatar    → 200 {avatarUrl}  (auth; multipart, field "avatar")
POST   /api/v1/me/avatar/url → 200 {avatarUrl} (auth)
     body: {url} — the server downloads the image and stores it like an upload
DELETE /api/v1/me/avatar    → 200 {}  (auth)
```

Profile update body: any of `{name, displayName, phone, avatarFocal: {x, y}}`.
Limits: names ≤ 100 chars, phone ≤ 30 chars, focal clamped to 0-100. The
email is the login: sending a different `email` answers 400 ("ask an admin
to change your sign-in email") until verified email changes arrive (M1.2).
Sending the current address (any case) is accepted and changes nothing.

`POST /me/password` body: `{current, password}`. `password` is at least 10
characters. The current browser stays logged in. Every other session for
that account ends. An admin password reset ends all of that account's
sessions, including the one that requested it.

`user`: `{id, name, displayName, email, phone, avatarUrl,
avatarFocalX, avatarFocalY, role, status, hasPassword}`. `status` is
`active`, `invited` (no password yet), or `disabled` (`guest` and
`pending_consent` come later). `displayName` falls back to name in the UI. `avatarUrl` is a
`/uploads/...` URL, or empty when no avatar is set. `avatarFocalX/Y` are
0-100 percentages for CSS `object-position`.

Avatar uploads must be JPEG, PNG, GIF, or WebP (content-sniffed, not by
extension), ≤ 5MB. Uploading replaces the old avatar; the old blob is
deleted. `POST /me/avatar/url` fetches an `http` or `https` URL and applies
the same checks. Private, link-local, and loopback addresses are refused
(loopback is allowed only when `APP_ENV=dev`). The CSRF token goes in the
`X-CSRF-Token` header for multipart posts, same as JSON.

### Uploads

```
GET /uploads/<key>  → the stored image (public; nosniff + 1-day cache)
```

Keys are server-generated (`avatars/<userId>/<id>.<ext>`,
`covers/<groupId>/<id>.<ext>`); path traversal is rejected. Local storage
lives under `UPLOAD_DIR` (`./data/uploads` default, `/data/uploads` in the
compose stack, persisted on the `/data` volume). See AGENTS.md for the
S3-replacement seam.

### Groups

```
GET  /api/v1/groups?search=&tag=          → {groups: [groupSummary]}
GET  /api/v1/me/groups                    → {groups: [groupSummary]}  (auth; groups I belong to, any status)
POST /api/v1/groups                       → 201 {group}   (auth, not teens; pending, or active when an admin creates it)
GET  /api/v1/groups/:id                   → {group, members, upcomingEvents, canManage, isMember}
PUT  /api/v1/groups/:id                   → 200 {group}   (group leader or admin)
POST   /api/v1/groups/:id/cover           → 200 {imageUrl} (group leader or admin; multipart, field "cover")
DELETE /api/v1/groups/:id/cover           → 200 {}         (group leader or admin)
```

`tag` may be repeated (`tag=choir&tag=youth`). A group matches if it has
*any* of the given tags (OR); `search` still narrows the result. Tags match
exactly and case-insensitively (`men` never matches `Women`). A blank `tag`
is ignored. The list respects visibility, so it never reveals groups (or
their tags) the viewer can't see.

Every permission check lives in `internal/policy` (PLAN.md section 6).
Leadership comes only from membership roles (`role: "leader"`); a group
may have several leaders. "Leader" below means any of them.

The directory lists active groups the caller is allowed to see. Logged-out
visitors see `public` groups. Signed-in users also see `private` groups.
`invite_only` groups are left out unless the caller is a member (leaders
are members) or an admin. Opening a group follows the same rule, and so
does every route under `/groups/:id`: anyone else gets 404, never 403.
Pending and archived groups stay limited to their leaders and admins.

`groupSummary`: `{id, name, description, tags[], imageUrl, coverFocalX,
coverFocalY, location, leaderName, memberCount, nextEventAt, nextEventLocal,
nextEventTzid, status, leaderId, visibility, deletionRequested, memberLimit}`.
`memberLimit` is null
for no limit (DEC-39); `memberCount` counts active members of listed accounts. `leaderId` and `leaderName` are the
displayed primary contact only (one of the leaders); they grant nothing.

`group` (detail response): `{group: groupSummary, members: [...],
upcomingEvents: [occurrence], canManage: bool, isMember: bool,
pendingReturns: [member]}`. `pendingReturns` (leaders and admins only, else `[]`)
lists re-enabled accounts waiting because the group was full (DEC-39).
`members` is filled only for the group's members and admins (people see each
other only inside groups they share); otherwise it is `[]`. `canManage` is
true for the group's leaders and admins. Roster member: `{id, name, avatarUrl, role,
joinedAt, admin}` — `name` is the member's display name; `admin` marks church admins (DEC-38).

Create/update body: `{name, description, tags[], imageUrl, location,
coverFocal: {x, y}, visibility, memberLimit}`.
`memberLimit` is a positive whole number, or null for no limit (the default);
on update, omit it to leave it unchanged. Lowering it below the count keeps
everyone and blocks joins until the count drops (DEC-39).
`visibility` is `public` (the default), `private`, or `invite_only`;
anything else is rejected with 400. On update, omit it to leave it
unchanged.
New groups enter `pending` until an admin approves them (the approval
workflow survives); groups an admin creates start `active`.
Name ≤ 200 characters, description ≤ 5000, location ≤ 300, joined tags ≤ 500.
Update rejects an empty name. `status` is only `pending`, `active`, or
`archived`; a leader may set `archived`, and only an admin may set another
status.

### Membership

```
POST /api/v1/groups/:id/join    → 200 {membership}  (auth; idempotent)
POST /api/v1/groups/:id/leave   → 200 {}            (auth; 400 if you are the only leader)
POST /api/v1/groups/:id/members → 200 {membership}  (auth; leader or admin)
     body: {email, overLimit?} — adds that account as a member; a full group
     answers 409 {error: "This group is full", full: true} unless overLimit is true (audited)
POST /api/v1/groups/:id/members/:userId/return → 200 {group, members, pendingReturns}  (auth; leader or admin; DEC-39)
     body: {decision: "approve"|"decline"} — approve makes a pending_return membership
     active (over the limit by explicit choice); decline sets it removed; both audited
DELETE /api/v1/groups/:id/members/:userId → 200 {members: [...]}  (auth; leader or admin; DEC-35)
     body (optional): {note} — kept only in the audit row, never shown to the removed person
PUT  /api/v1/groups/:id/members/:userId/role → 200 {members: [...]}  (auth; leader or admin)
     body: {role: "leader"|"member"} — userId must already be an active member
POST /api/v1/groups/:id/deletion-request → 200 {group}  (auth; a leader)
DELETE /api/v1/groups/:id/deletion-request → 200 {}    (auth; leader or admin)
GET  /api/v1/groups/:id/members → {members: [...]}  (auth; members and admins; 404 if you cannot see the group, 403 if you are not in it)
```

Join returns 404 when the group is not visible, and 403 when it is
invite-only and the caller is not already a member. A full group answers 409
"This group is full" (no waitlist), and someone whose return is pending gets 409. A deletion request is
accepted only from a leader who is the group's only active member. Adding
a member clears a pending request.

Role changes: only adults can be made leaders (teen accounts arrive in
M1.10 and are refused), and a group never loses its last leader: demoting,
removing, or leaving as the only leader is a 400. There is no admin
approval step for a leader promoting a co-leader (DEC-31). Instead every
change that makes or removes a leader (including leaving and admin
add/remove) writes a `membership.role_changed` audit row (actor, group,
person, old and new role, IP), and admins get a `leadership` notification.
When the primary contact stops leading, `leaderId` moves to the
earliest-joined remaining leader. `DELETE` on the
request withdraws it without deleting the group.

`membership`: `{id, userId, user: {id, name}, groupId, role: "leader"|"member",
status, joinedAt}`

### Site

```
GET /api/v1/site → {name, timezone}   (public)
PUT /api/v1/admin/timezone → 200 {timezone}   (admin)
    body: {timezone} — an IANA name, for example America/Denver
```

`timezone` is the church time zone. The schedule form defaults a new series
to the browser's time zone. A create request that omits `tzid` uses the
church time zone, or UTC when that setting is missing. An unknown name is 400.

### Events

Events belong to groups. A series stores a wall clock (`startsLocal`,
`YYYY-MM-DDTHH:MM` with no offset) and an IANA `tzid`. Occurrences are
stored up to 18 months ahead and extended daily. A weekly 7:00 pm
America/Boise event stays at 7:00 pm local on both sides of a
daylight-saving change. `BYDAY` is the weekday in that zone.

Each date is addressed as `{eventId, date}` where `date` is the series
date `YYYY-MM-DD`. A per-date time change does not change `date`.

```
GET  /api/v1/groups/:id/events?from=2026-10-06&to=2027-01-06
  → {events: [occurrence]}   (default: next 90 days, max 366-day window, max 100 per event)
POST /api/v1/groups/:id/events              → 201 {event}   (group leader or admin)
PUT  /api/v1/events/:id                     → 200 {event}   (group leader or admin; series edit)
DELETE /api/v1/events/:id                   → 200 {}        (group leader or admin)
POST /api/v1/events/:id/cancel              → 200 {}        (group leader or admin)
     body: {date: "YYYY-MM-DD"} — cancels that stored date
PUT  /api/v1/events/:id/occurrences/:date   → 200 {occurrence}  (group leader or admin)
     body: any of {startsLocal, durationMinutes, location, title} — this date only
```

`event`: `{id, groupId, title, startsLocal, durationMinutes, tzid, startsAt,
endsAt, location, recurrenceRule, rruleUntil}`. `startsAt` and `endsAt` are
UTC. `startsLocal` is the wall clock.

`occurrence`: `{id, eventId, date, startsAt, endsAt, startsLocal, endsLocal,
tzid, durationMinutes, location, title, recurrenceRule, rruleUntil,
seriesStartsLocal, seriesTitle, seriesLocation, seriesDurationMinutes,
overridden, registrations: {going, maybe, declined},
myRegistration: "going"|"maybe"|"declined"|null}`. `startsAt`/`endsAt` are
UTC. `startsLocal`/`endsLocal` are the wall clock in `tzid`, including a
per-date override. `overridden` is true when this date has its own time,
title, or location.

Create body: `{title?, startsLocal, durationMinutes, tzid?, location?,
recurrenceRule?, rruleUntil?}`. `startsLocal` and `durationMinutes` (1 to
10080) are required. Omit `tzid` to use the church time zone. The schedule
form sends the browser's time zone. An empty
`recurrenceRule` is one date. The rule must not contain `DTSTART` or a
newline; `rruleUntil` is a separate inclusive `YYYY-MM-DD`, not an `UNTIL`
inside the rule. A rule that does not parse is 400.

A series edit (`PUT /events/:id`) applies to every future date. Future
means `startsAt` is strictly after now; a date already underway stays.
Future dates without a per-date override move onto the new times and keep
their registrations (same occurrence row). A future date the new rule no
longer contains is cancelled, and people who registered going or maybe see
a derived notice. Per-date overrides, including a location-only override,
are left alone. Dates the new rule adds are inserted.

Occurrences are returned in chronological order. `from`/`to` are inclusive
`YYYY-MM-DD` and at most 366 days apart. Cancelled dates are left out of
the list. The group detail's `upcomingEvents` are the scheduled dates from
now through 90 days.

### Registration

```
PUT /api/v1/events/:id/registrations   → 200 {registration}   (auth)
  body: {status: "going"|"maybe"|"declined", date: "YYYY-MM-DD"}
```

`registration`: `{userId, eventId, occurrenceId, date, status}`. One row per
(user, occurrence); PUT is idempotent. The date must be a scheduled
occurrence. The caller must be allowed to see the event's group; otherwise
the response is 404. A disabled account's registration is kept and hidden
from the counts.

### Admin

```
GET  /api/v1/admin/pending                 → {groups: [groupSummary]}  (admin)
GET  /api/v1/admin/deletions               → {groups: [groupSummary]}  (admin; deletion_requested)
POST /api/v1/admin/groups/:id/approve      → 200 {group}               (admin)
     body: {tags: []}
POST /api/v1/admin/groups/:id/reject       → 200 {}                    (admin; archives the group)
POST /api/v1/admin/groups/:id/reject-deletion → 200 {}               (admin; clears the request)
DELETE /api/v1/admin/groups/:id            → 200 {}                    (admin; deletes the group)
GET  /api/v1/admin/groups                  → {groups: [groupSummary]}  (admin; all statuses)
POST /api/v1/admin/groups/:id/members      → 200 {membership}          (admin)
     body: {userId, role: "member"|"leader", overLimit?} — role=leader adds a (co-)leader; audited;
     a full group needs overLimit (audited as membership.added_over_limit)
GET  /api/v1/admin/pending-returns         → {returns: [{group: groupSummary, member}]}  (admin; DEC-39)
DELETE /api/v1/admin/groups/:id/members/:userId → 200 {members}      (admin; same rules as the leader route)
GET  /api/v1/admin/users                   → {users: [user]}           (admin)
GET  /api/v1/admin/users/:id/sole-leader-groups → {groups: [{id, name, status, candidates: [{id, name, admin}]}]}  (admin; DEC-36)
POST /api/v1/admin/users                    → 201 {user}                (admin)
     body: {email, name, password? (≥10 chars), role, displayName?, phone?}
     — without a password the account is `invited` and can't sign in; 409 if the email is taken (any case)
PUT  /api/v1/admin/users/:id               → 200 {user}                (admin; profile fields, email, role, status)
     body: any of {name, displayName, email, phone, role, status: "active"|"disabled", successors}
POST /api/v1/admin/users/:id/password      → 200 {}                    (admin; also activates an invited account)
     body: {password (≥10 chars)}
DELETE /api/v1/admin/users/:id             → 200 {}                    (admin; soft delete, frees the email)
     body: {successors: {"<groupId>": <userId>}} — required for every group they lead alone
```

Guards: cannot demote, disable or delete the last active admin, or
yourself. Changing someone's password, email, role, or status ends all of
their sessions (`session_version`). A disabled admin has no admin powers. `PUT /api/v1/groups/:id` already allows admins to edit any group.


Rosters (`members` in group detail, `GET /groups/:id/members`, admin rosters) include `admin: true` for church admins, so the SPA can label them and hide Remove (DEC-38).

Disabling (DEC-38): the account's memberships and registrations are kept, hidden from rosters, and left out of member counts, registration counts, and leader counts; any leadership becomes plain membership (with `membership.role_changed`, reason `account_disabled`). Enabling it again restores them as plain member, or as `pending_return` in a group at its member limit (DEC-39), writing `membership.restored` (with the resulting status) per membership and `user.enabled`. Deleting still ends memberships.

Removing a member (DEC-35): leaders remove people from groups they lead,
co-leaders included, but never an admin (they can't demote an admin
co-leader either) or the last leader; admins remove anyone but the last
leader. Removing yourself is `leave`. The membership becomes `removed`: the
person can't rejoin on their own (join answers 403), invite-only groups are
404 to them, and a leader or admin adding them back makes it active again.
Each removal writes a `membership.removed` audit row; removing a leader also
shows admins a `leadership` notice.

Sole-leader handoff (DEC-36): deleting or disabling the only leader of any
group (any status) needs `successors`, a new leader per group: an active
adult member of that group or any active admin (the SPA preselects the
acting admin). A missing or invalid choice answers 400 with
`{error, groups}` in the `sole-leader-groups` shape. On success, in one
transaction: each successor becomes a leader (added to the group if needed)
and the primary contact, the account is deleted (memberships end) or disabled (memberships kept
as plain member). Audit: `membership.role_changed` per group
with `reason` `account_deleted` or `account_disabled` and
`departingUserId`, plus `user.deleted` or `user.disabled`.
### Notifications

```
GET /api/v1/notifications → {notifications: [{id, type, title, body, link}]}  (auth)
```

`id` names the exact notice and changes when it is about something new
(e.g. another pending group), so a dismissal never hides a later notice.
The SPA refetches on sign-in, navigation, window focus and every 5 minutes.

Derived, not stored: `approval` (admins, when groups await approval or a
leader has asked for a group to be deleted), `event` (scheduled occurrences
in your groups in the next 7 days, max 10, and a notice when a date you
registered going or maybe is cancelled), and `leadership` (admins: someone else
made or removed a group leader in the last 14 days, from the audit log). The client tracks dismissed items
locally. Admin `DELETE /admin/groups/:id` removes a group immediately,
with or without a deletion request, and whether or not it still has members.

## Conventions

- `POST` create → `201`; `PUT` update → `200`; `DELETE` → `200 {}`.
- Auth failures → `401 {"error":"unauthorized"}`; forbidden → `403`.
- Validation failures → `400 {"error":"..."}`; missing → `404`.
- Lists are unpaginated in v1 (fine for church scale); add `?page=` later
  if needed.
- Every response carries the app's security headers (`securityHeaders` in
  `main.go`, DEC-37): a `Content-Security-Policy` allowing only same-origin
  scripts, styles, and connections (images may also be `data:`, `blob:`, or
  `https:`), `X-Content-Type-Options: nosniff`, `Referrer-Policy:
  same-origin`, `X-Frame-Options: SAMEORIGIN`, and `Permissions-Policy:
  camera=(), microphone=(), geolocation=()`. `Strict-Transport-Security` is
  added when `COOKIE_SECURE` is on.
