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

## Endpoints

### Auth

```
POST /api/v1/auth/login    {username, password} → 200 {user} | 401
POST /api/v1/auth/logout                         → 200 {}
GET  /api/v1/auth/me                             → 200 {user} | 401
```

### Profile

```
PUT    /api/v1/me           → 200 {user}  (auth; partial update)
POST   /api/v1/me/password  → 200 {}  (auth)
POST   /api/v1/me/avatar    → 200 {avatarUrl}  (auth; multipart, field "avatar")
POST   /api/v1/me/avatar/url → 200 {avatarUrl} (auth)
     body: {url} — the server downloads the image and stores it like an upload
DELETE /api/v1/me/avatar    → 200 {}  (auth)
```

Profile update body: any of `{name, displayName, email, phone,
avatarFocal: {x, y}}`. Limits:
names ≤ 100 chars, email must parse and be ≤ 254 chars (empty clears it),
phone ≤ 30 chars, focal clamped to 0-100.

`POST /me/password` body: `{current, password}`. `password` is at least 10
characters. The current browser stays logged in. Every other session for
that account ends. An admin password reset ends all of that account's
sessions, including the one that requested it.

`user`: `{id, username, name, displayName, email, phone, avatarUrl,
avatarFocalX, avatarFocalY, role}`.
`displayName` falls back to name, then username in the UI. `avatarUrl` is a
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
POST /api/v1/groups                       → 201 {group}   (auth; status=pending)
GET  /api/v1/groups/:id                   → {group, members, upcomingMeetings, canManage, isMember}
PUT  /api/v1/groups/:id                   → 200 {group}   (group leader or admin)
POST   /api/v1/groups/:id/cover           → 200 {imageUrl} (group leader or admin; multipart, field "cover")
DELETE /api/v1/groups/:id/cover           → 200 {}         (group leader or admin)
```

`tag` may be repeated (`tag=choir&tag=youth`). Every value must match, so
the filters narrow together. A blank `tag` is ignored.

The directory lists active groups the caller is allowed to see. Logged-out
visitors see public groups. Signed-in users also see private groups.
Invite-only groups are left out unless the caller leads the group or is
already a member. Opening a group follows the same rule; anyone else gets
404. Pending and archived groups stay limited to the leader and admins.
An admin can open an invite-only group by id, but it is not added to their
home list just because they are an admin.

`groupSummary`: `{id, name, description, tags[], imageUrl, coverFocalX,
coverFocalY, location, leaderName, memberCount, nextMeetingAt, status,
leaderId, visibility, inviteOnly, deletionRequested}`

`group` (detail response): `{group: groupSummary, members: [...],
upcomingMeetings: [meetingInstance], canManage: bool, isMember: bool}`.
`members` is populated for logged-in users only. `canManage` is true for the
group's leader and admins. Roster member: `{id, name, avatarUrl, role,
joinedAt}` — `name` is the member's display name.

Create/update body: `{name, description, tags[], imageUrl, location,
coverFocal: {x, y}, visibility, inviteOnly}`.
`visibility` is `public` (the default) or `private`. `inviteOnly` is only
valid when visibility is `private`; a public invite-only group is rejected
with 400. On update, omit either field to leave it unchanged. Sending
`visibility: "public"` while the group is still invite-only fails until
`inviteOnly` is also false.
New groups enter `pending` until approved (the approval workflow survives).
Name ≤ 200 characters, description ≤ 5000, location ≤ 300, joined tags ≤ 500.
Update rejects an empty name. `status` is only `pending`, `active`, or
`archived`; a leader may set `archived`, and only an admin may set another
status.

### Membership

```
POST /api/v1/groups/:id/join    → 200 {membership}  (auth; idempotent)
POST /api/v1/groups/:id/leave   → 200 {}            (auth; 400 if you are the leader)
POST /api/v1/groups/:id/members → 200 {membership}  (auth; leader or admin)
     body: {username} — adds that account as a member
POST /api/v1/groups/:id/leader  → 200 {group}      (auth; leader or admin)
     body: {userId} — userId must already be an active member
POST /api/v1/groups/:id/deletion-request → 200 {group}  (auth; the leader only)
DELETE /api/v1/groups/:id/deletion-request → 200 {}    (auth; leader or admin)
GET  /api/v1/groups/:id/members → {members: [...]}  (auth; hidden when you cannot see the group)
```

Join returns 404 when the group is not visible, and 403 when it is
invite-only and the caller is not already a member. A deletion request is
accepted only when the leader has no other active members. The leader is
always a member, so “no members” means nobody else is left. Adding a member
or transferring leadership clears a pending request. `DELETE` on the
request withdraws it without deleting the group.

`membership`: `{id, userId, user: {id, name}, groupId, role: "leader"|"member",
status, joinedAt}`

### Meetings

Meetings belong to groups. A meeting may carry an `recurrenceRule`
(RFC 5545 RRULE, e.g. `FREQ=WEEKLY;BYDAY=WE`); instances are expanded
server-side. Each instance is addressed as `{meetingId, date}` where date
is `YYYY-MM-DD`.

**Timezone note:** expansion runs on the UTC timeline, so `BYDAY` values in
the rule refer to the **UTC** day of `startsAt`. When a client converts a
local wall-clock time to UTC for `startsAt`, it must derive `BYDAY` from
the UTC-normalized day, not the local day — otherwise the series lands on
the wrong local weekday. (A bare `FREQ=WEEKLY` with no `BYDAY` repeats on
the DTSTART weekday and is always safe.)

```
GET  /api/v1/groups/:id/meetings?from=2026-10-06&to=2027-01-06
  → {meetings: [meetingInstance]}   (default: next 90 days, max 366-day window, max 100 per meeting)
POST /api/v1/groups/:id/meetings    → 201 {meeting}   (group leader or admin)
PUT  /api/v1/meetings/:id           → 200 {meeting}   (group leader or admin)
DELETE /api/v1/meetings/:id         → 200 {}          (group leader or admin)
POST /api/v1/meetings/:id/cancel    → 200 {}          (group leader or admin)
     body: {date: "YYYY-MM-DD"} — cancels one recurring instance
```

`meeting`: `{id, groupId, startsAt, endsAt, location, recurrenceRule,
cancelledDates[]}`

`meetingInstance`: `{meetingId, date, startsAt, endsAt, location,
rsvps: {going, maybe, declined}, myRsvp: "going"|"maybe"|"declined"|null}`

Meeting body: `{startsAt, endsAt, location, recurrenceRule?}`.
`startsAt` is the first occurrence; times are per-instance local times
carried on the date. An `recurrenceRule` that does not parse is rejected
with 400. Instances are returned in chronological order. `from`/`to` must
be in order and at most 366 days apart.

### RSVP

```
PUT /api/v1/meetings/:id/rsvp   → 200 {rsvp}   (auth)
  body: {status: "going"|"maybe"|"declined", date: "YYYY-MM-DD"}
```

`rsvp`: `{userId, meetingId, date, status}`. One row per
(user, meeting, date); PUT is idempotent. The caller must be allowed to
see the meeting's group.

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
     body: {userId, role: "member"|"leader"} — role=leader reassigns leadership
DELETE /api/v1/admin/groups/:id/members/:userId → 200 {}               (admin; not the leader)
GET  /api/v1/admin/users                   → {users: [user]}           (admin)
POST /api/v1/admin/users                    → 201 {user}                (admin)
     body: {username, password (≥10 chars), role, name?, displayName?, email?, phone?}
PUT  /api/v1/admin/users/:id               → 200 {user}                (admin; profile fields + role)
POST /api/v1/admin/users/:id/password      → 200 {}                    (admin)
     body: {password (≥10 chars)}
DELETE /api/v1/admin/users/:id             → 200 {}                    (admin; soft delete)
```

Guards: cannot demote/delete the last admin, cannot delete or demote
yourself. `PUT /api/v1/groups/:id` already allows admins to edit any group.

### Notifications

```
GET /api/v1/notifications → {notifications: [{type, title, body, link}]}  (auth)
```

Derived, not stored: `approval` (admins, when groups await approval or a
leader has asked for a group to be deleted) and `event` (meetings in your
groups in the next 7 days, max 10). The client tracks dismissed items
locally. Admin `DELETE /admin/groups/:id` removes a group immediately,
with or without a deletion request, and whether or not it still has members.

## Conventions

- `POST` create → `201`; `PUT` update → `200`; `DELETE` → `200 {}`.
- Auth failures → `401 {"error":"unauthorized"}`; forbidden → `403`.
- Validation failures → `400 {"error":"..."}`; missing → `404`.
- Lists are unpaginated in v1 (fine for church scale); add `?page=` later
  if needed.
