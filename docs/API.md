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
POST   /api/v1/me/avatar    → 200 {avatarUrl}  (auth; multipart, field "avatar")
DELETE /api/v1/me/avatar    → 200 {}  (auth)
```

Profile update body: any of `{name, displayName, email, phone}`. Limits:
names ≤ 100 chars, email must parse and be ≤ 254 chars (empty clears it),
phone ≤ 30 chars.

`user`: `{id, username, name, displayName, email, phone, avatarUrl, role}`.
`displayName` falls back to name, then username in the UI. `avatarUrl` is a
`/uploads/...` URL, or empty when no avatar is set.

Avatar uploads must be JPEG, PNG, GIF, or WebP (content-sniffed, not by
extension), ≤ 5MB. Uploading replaces the old avatar; the old blob is
deleted. The CSRF token goes in the `X-CSRF-Token` header for multipart
posts, same as JSON.

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
GET  /api/v1/me/groups                    → {groups: [groupSummary]}  (auth; groups I joined)
POST /api/v1/groups                       → 201 {group}   (auth; status=pending)
GET  /api/v1/groups/:id                   → {group, members, upcomingMeetings, canManage, isMember}
PUT  /api/v1/groups/:id                   → 200 {group}   (group leader or admin)
POST   /api/v1/groups/:id/cover           → 200 {imageUrl} (group leader or admin; multipart, field "cover")
DELETE /api/v1/groups/:id/cover           → 200 {}         (group leader or admin)
```

`groupSummary`: `{id, name, description, tags[], imageUrl, location,
leaderName, memberCount, nextMeetingAt, status}`

`group` (detail response): `{group: groupSummary, members: [...],
upcomingMeetings: [meetingInstance], canManage: bool, isMember: bool}`.
`members` is populated for logged-in users only. `canManage` is true for the
group's leader and admins. Roster member: `{id, name, avatarUrl, role,
joinedAt}` — `name` is the member's display name.

Create/update body: `{name, description, tags[], imageUrl, location}`.
New groups enter `pending` until approved (the approval workflow survives).

### Membership

```
POST /api/v1/groups/:id/join    → 200 {membership}  (auth; idempotent)
POST /api/v1/groups/:id/leave   → 200 {}            (auth)
GET  /api/v1/groups/:id/members → {members: [...]}  (auth)
```

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
  → {meetings: [meetingInstance]}   (default: next 90 days, max 100)
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
carried on the date.

### RSVP

```
PUT /api/v1/meetings/:id/rsvp   → 200 {rsvp}   (auth)
  body: {status: "going"|"maybe"|"declined", date: "YYYY-MM-DD"}
```

`rsvp`: `{userId, meetingId, date, status}`. One row per
(user, meeting, date); PUT is idempotent.

### Admin

```
GET  /api/v1/admin/pending                 → {groups: [groupSummary]}  (admin)
POST /api/v1/admin/groups/:id/approve      → 200 {group}               (admin)
     body: {tags: []}
POST /api/v1/admin/groups/:id/reject       → 200 {}                    (admin)
```

## Conventions

- `POST` create → `201`; `PUT` update → `200`; `DELETE` → `200 {}`.
- Auth failures → `401 {"error":"unauthorized"}`; forbidden → `403`.
- Validation failures → `400 {"error":"..."}`; missing → `404`.
- Lists are unpaginated in v1 (fine for church scale); add `?page=` later
  if needed.
