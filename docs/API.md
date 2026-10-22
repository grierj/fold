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
POST /api/v1/auth/password-reset         {email} → 200 {message}  (public)
POST /api/v1/auth/password-reset/confirm {token, password} → 200 {user} | 400  (public)
```

Password reset (M1.2): the request answers `200 {"message":"If that email
has an account, we've sent a link"}` for every address, known or not, after
one bcrypt comparison on a miss (no account enumeration). Per-IP (10/hour)
and per-address (5/hour) throttles live in `login_throttles`; throttled
requests still answer uniformly. Tokens are 32 random bytes, stored as
SHA-256, single use, and expire after 24 hours. Confirming sets the password
(≥ 10 chars), ends every other session, and signs the browser in.

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
PUT    /api/v1/me           → 200 {user, verificationSent?}  (auth; partial update)
POST   /api/v1/me/password  → 200 {}  (auth)
POST   /api/v1/me/avatar    → 200 {avatarUrl}  (auth; multipart, field "avatar")
DELETE /api/v1/me/avatar    → 200 {}  (auth)
POST   /api/v1/me/email/verify  {token} → 200 {user} | 400  (public)
GET    /api/v1/me/calendar-feed → 200 {calendarFeed: {hasToken, scope}}  (auth)
POST   /api/v1/me/calendar-feed {scope?} → 200 {calendarFeed: {hasToken, scope, url}}  (auth)
PUT    /api/v1/me/calendar-feed {scope} → 200 {calendarFeed} | 404  (auth)
DELETE /api/v1/me/calendar-feed → 200 {calendarFeed}  (auth)
DELETE /api/v1/admin/users/:id/calendar-feed → 200 {calendarFeed}  (admin)
```

Calendar feed (M1.7, plan 8.2): one secret link per person. Only the
token's SHA-256 is stored, so `url` (with the raw token) comes back only
from POST; POST also remakes the link, which revokes the old one.
`scope` is `"mine"` (default: my groups' events, events I host, and the
dates I'm going or waitlisted for) or `"all"` (everything I can see).
Revoking a link that is already off still answers 200.

Profile update body: any of `{name, displayName, phone, email, timezone, avatarFocal: {x, y}}`.
Limits: names ≤ 100 chars, phone ≤ 30 chars, focal clamped to 0-100,
`timezone` an IANA name (DEC-58; "" and "Local" are refused). The
email is the login: sending a different `email` starts a verified change
(M1.2, 10.1) — a verification link goes to the new address and a notice to
the old one, and the response carries `verificationSent: true`. The change
applies only after `POST /me/email/verify` redeems the (single-use,
24-hour) token; every other session then ends, while the verifying browser
stays logged in. Sending the current address (any case) is accepted and
changes nothing. Changing to an address another account uses answers 409.

`POST /me/password` body: `{current, password}`. `password` is at least 10
characters. The current browser stays logged in. Every other session for
that account ends. An admin password reset ends all of that account's
sessions, including the one that requested it.

`user`: `{id, name, displayName, email, phone, avatarUrl,
avatarFocalX, avatarFocalY, role, status, hasPassword, accountType, timezone,
birthDate?}`. `status` is
`active`, `invited` (no password yet), `pending_consent` (a teen waiting
for consent, M1.10), `guest` (a verified guest registrant with no
password and no sign-in, M1.4), or `disabled`.
`accountType` is `adult` or `teen` (DEC-8); `birthDate` (`YYYY-MM-DD`)
is present for teens. `displayName` falls back to name in the UI. `avatarUrl` is a
`/uploads/...` URL, or empty when no avatar is set. `avatarFocalX/Y` are
0-100 percentages for CSS `object-position`.

Avatar uploads must be JPEG, PNG, GIF, or WebP (content-sniffed, not by
extension), ≤ 5MB. Uploading replaces the old avatar; the old blob is
deleted. Pictures are upload-only; the server never fetches a URL (Oct 14
decision). The CSRF token goes in the
`X-CSRF-Token` header for multipart posts, same as JSON.


### Household (M1.9, DEC-8, DEC-32)

```
GET    /api/v1/me/household                 → 200 {household|null}  (read-only; null until created)
PUT    /api/v1/me/household                 → 200 {household}  {name?, address?}  (creates on first use)
POST   /api/v1/me/household/people          → 201 {person}  {firstName, lastName, kind, birthDate?, careNotes?, photoConsent?}  (creates the household on first use)
PUT    /api/v1/me/household/people/:id      → 200 {person}
DELETE /api/v1/me/household/people/:id      → 200 {}
POST   /api/v1/me/household/people/:id/convert → 201 {conversion}  {email}
POST   /api/v1/me/household/people/:id/convert/resend → 200 {conversion}
DELETE /api/v1/me/household/people/:id/convert → 200 {}
GET    /api/v1/admin/guardian-links?status=pending  → 200 {guardianLinks}   (admin)
POST   /api/v1/admin/guardian-links/:id/confirm     → 200 {guardianLink}    {checkNote} (admin; note required; not your own link while another active admin exists, DEC-79)
POST   /api/v1/admin/guardian-links/:id/reject      → 200 {guardianLink}    {note?} (admin)
```

`GET /me/household` never writes (Warden, #106). The household is created by
`PUT /me/household` or the first `POST /me/household/people`, and named after
the account's last name. Any adult account in the household manages it; teen
accounts get `null` from GET and 403 from the writes. Adding a `child` or
`teen` person opens a pending guardian link that only an admin can confirm,
with a note on how it was checked (DEC-32); an `adult` needs no approval.

An active adult in the household or an active adult admin can invite an
eligible household person to their own account. Admins may act across
households; unrelated members receive 404. A real birth date must prove
an adult is 18 or older, or a teen is 13 to 17 on the church-local day.
A teen also needs a pending or confirmed guardian link to an active adult
account in the same household. Children cannot convert, even after turning 13
until their saved type is corrected. Disabled responsible accounts block conversion.

`conversion` is `{email, expiresAt, state: "pending"|"expired"|"awaiting_approval"}`.
Household people include `canConvert` and optional `conversion` metadata,
never a token or hash. Household accounts include `status`; converted people
appear only under accounts and cannot be edited or removed as household people.

Issuing twice to the same pending destination returns its state without
sending again. A different destination requires cancellation first. Resend
keeps the destination, rotates the token, and gives it 24 hours. Expired
invitations are revoked before replacement. Cancellation is idempotent and
remains available after eligibility changes. Name, kind, or birth-date edits
and deletion revoke the invitation in the same transaction. Care notes,
photo consent, and household name or address changes keep it open.

Conversion uses the existing invitation endpoints below. For an adult,
acceptance returns `{pendingApproval: true}` without a session. It stores a
pending `household_conversion` account request with the chosen password's
bcrypt hash and the household person and invitation IDs, and marks the
invitation accepted. It creates no user or household account and changes no
registrations or guardian links. The household continues managing the person
and all their sign-ups. While pending, the email is reserved and new or resent
conversion invitations for that person return 409 with
`household_conversion_approval`. The household person remains in `people`
with `conversion.state: "awaiting_approval"`.

Approval creates an active member account using the held password hash,
transfers every registration in place, marks the person converted, and adds
an adult account to the same household, all in the decision transaction.
IDs, answers, attendance mode, queue order, and timestamps stay unchanged;
`registeredBy` and named childcare flags are cleared. Group memberships do
not move. Approval emails a sign-in note referring to the password already
chosen. A deleted or converted person, unavailable email, or unavailable
responsible account returns 409 and leaves the request pending. Rejection
or 30-day expiry closes the request and clears the held password hash;
nothing needs undoing, and the email can be invited again.

A first send suppressed by the invitation limit saves no invitation row,
but the response has the same keys as a normal send. Teens still convert
at acceptance: their member account, registrations, household account and
fresh guardian links are created then. A teen receives `{pendingConsent: true}`
without a user profile or a new session. Their sign-ups still count toward
capacity and remain visible to event managers. Fresh account-targeted guardian
links wait for admin checks; confirming them never activates the teen.
Unrelated members do not see pending teen names in attendee lists.

Creation and resend share the 20-per-rolling-day invitation limit by sender
and destination, including resends. Counters live in Postgres audit rows.
Conversion sends over the limit return the normal acknowledgement and log
the suppression. A suppressed resend keeps the previous link live. Delivery
failure keeps a retryable invitation. Errors use `household_conversion_email`
for any unavailable destination, `household_conversion_open` for a different
pending destination, `household_conversion_unavailable` for eligibility or
inviter authority, and `household_conversion_holder` for an unavailable
responsible account discovered during acceptance. Invalid, revoked, expired,
or consumed links share `link_expired` copy.

`GET /help/members` serves the embedded `docs/MEMBERS.md` as UTF-8 plain text
with `nosniff`, at the same origin as the app. It needs no session.

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
coverFocalY, location, leaderName, memberCount, nextEventAt (UTC; the SPA shows it in the browser's zone, DEC-57),
status, leaderId, visibility, deletionRequested, memberLimit}`.
`memberLimit` is null
for no limit (DEC-39); `memberCount` counts active members of listed accounts. `leaderId` and `leaderName` are the
displayed primary contact only (one of the leaders); they grant nothing.

`group` (detail response): `{group: groupSummary, members: [...],
upcomingEvents: [occurrence], canManage: bool, isMember: bool}`.
`members` is filled only for the group's members and admins (people see each
other only inside groups they share); otherwise it is `[]`. `canManage` is
true for the group's leaders and admins. Roster member: `{id, name, avatarUrl, role,
joinedAt, admin}` — `name` is the member's display name; `admin` marks church admins (DEC-38).

Create/update body: `{name, description, tags[], location,
coverFocal: {x, y}, visibility, memberLimit}`. Covers are upload-only: set
one with `POST /groups/:id/cover`, clear it with `DELETE /groups/:id/cover`;
an `imageUrl` in this body is ignored, so `imageUrl` is always empty or an
`/uploads/...` path.
`memberLimit` is a positive whole number, or null for no limit (the default);
on update, omit it to leave it unchanged. Lowering it below the count keeps
everyone and blocks joins until the count drops (DEC-39).
`visibility` is `public` (the default), `private`, or `invite_only`;
anything else is rejected with 400. On update, omit it to leave it
unchanged.

Changing a group's visibility (DEC-48). Making it more open never changes
its events. Making it more private, in the same transaction:
- events more open than the new value that aren't `leaderOpened` become the
  new value (`event.visibility_changed`, reason `group_more_private`);
- registrants of a tightened event who could no longer see it (on
  `invite_only`: not a member of the group and not an admin) lose their
  registrations on upcoming dates; each gets a `registration.cancelled`
  audit row (meta `userId`, `dates`) and a notification ("Registration
  cancelled: <event title>", id `registration-cancelled:<audit id>`, link
  `/`; it never names the group or its place);
- each `leaderOpened` event more open than the new value needs a decision
  in `openedEvents: {"<eventId>": "keep"|"tighten"}`. `keep` writes an
  `event.visibility_override` (with the usual notice); `tighten` works like
  the inherited case. If any decision is missing or invalid, nothing is
  saved and the answer is `409 {error, needsDecision: true, impact}`.

```
GET /api/v1/groups/:id/visibility-impact?visibility=invite_only → 200 impact  (leader or admin)
```

`impact`: `{visibility, tightened: [impactEvent], leaderOpened:
[impactEvent]}`, with `impactEvent` `{id, title, visibility, registrants}`.
`registrants` counts people registered for upcoming dates who would lose
access if that event is tightened. Both lists are empty unless the change
is more private. The SPA calls this before saving and shows the list.

One event works the same way (DEC-48):

```
GET /api/v1/events/:id/visibility-impact?visibility=invite_only → 200 impact  (leader, host, or admin)
PUT /api/v1/events/:id/visibility  body {visibility, reason?, confirm?}
```

The impact lists only that event, under `tightened`, when the change is more
private. If it would cancel registrations, the PUT answers 409
`{needsDecision: true, impact}` unless `confirm` is true.
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

### Invitations

```
POST /api/v1/invitations          → 201 {invitation}  (auth)
GET  /api/v1/invitations/:token   → 200 {invitation, inviterName, siteName, site} | 404  (public)
POST /api/v1/invitations/accept   → 200 {user?, pendingConsent, pendingApproval} | 400 | 409  (public)
```

Invitations (M1.2, 5.10, DEC-4): admins invite into any group or none;
leaders invite only into groups they lead or none (403 otherwise); members,
teens, and guests get 403, signed-out visitors 401. There is no approval
step: the person sets a password on acceptance and gets an active account.
Create body: `{email, name?, phone?, groupId?, role? ("member"|"leader"),
accountType? ("adult"|"teen"), overLimit?}`. 409 when the email already has
an account or a pending invitation. Leaders are limited to 20 invitations
per rolling day; the 21st gets 429 and is audited
(`invitation.rate_limited`).

Tokens are 32 random bytes, stored as SHA-256, single use, and expire after
24 hours. Accept body: `{token, name, password}` (password ≥ 10 chars);
accepting signs the browser in. Accepting into a full group joins over
`member_limit` only when the inviter set `overLimit` (an explicit, audited
choice, DEC-39); otherwise it answers `409 {"error":"This group is
full","full":true}` without consuming the token. The over-limit join is
audited as `membership.added_over_limit`.

`invitation`: `{id, email, name, groupId?, groupName?, expiresAt, conversion, accountType}`.
Ordinary invitations have `conversion: false`. Conversion invitations have
no group and explain the transfer on the accept page. Adult acceptance
creates a session only when the resulting account can sign in.

### Account requests

```
POST /api/v1/account-requests              → 200 {message} | 400 | 403  (public)
POST /api/v1/account-requests/invite-friend → 200 {message} | 400 | 403 | 409  (auth)
POST /api/v1/account-requests/confirm      {token, password} → 200 {user, pendingConsent?} | 400  (public)
GET  /api/v1/admin/account-requests?status=pending → {requests: [accountRequest]}  (admin)
POST /api/v1/admin/account-requests/:id/approve → 200 {request, userId}  (admin)
     body: {note?, fromNotification?}
POST /api/v1/admin/account-requests/:id/reject  → 200 {request, userId: 0}  (admin)
     body: {note?, fromNotification?} — rejection is never inline (403 with fromNotification)
PUT  /api/v1/admin/self-signup             {mode: "request"|"off"} → 200 {selfSignup}  (admin)
```

Self-signup and invite-a-friend (M1.3, 5.10, DEC-4) create a pending
`AccountRequest`; only an admin approves. There is no open mode: the
`self_signup` setting is `request` (default) or `off`, and `off` refuses
the public door with 403 (the member door still works). Create body:
`{name, email, phone?, ageBand: "adult"|"teen"|"under13", birthDate?,
message?, website?}`. `website` is a honeypot people never fill in.

The age gate is on every door (DEC-8): `under13` stores nothing and
answers 400 with the household explanation; `teen` needs a `birthDate`
aged 13–17 and creates a teen request; `adult` needs no date (adults
only confirm 18+, and any date sent is dropped). Approval creates the
account (`invited` for adults, `pending_consent` for teens until M1.10
consent) and emails a single-use set-password link (24 hours) to
`/set-password`; approval creates the account only, never a group
membership. Confirming sets the password: adults sign in, teens stay
`pending_consent` (`pendingConsent: true`, no session). Rejection emails
the person too, with the admin's optional note. Requests expire after
30 days (checked lazily in the queue and daily); deciding an expired
request answers 409.

No enumeration (10.1): a self-signup for an email that already has an
account stores nothing, answers the same `200 {"message":"Thanks —
check your email for next steps."}`, and emails that address a sign-in
and reset hint instead. A member inviting an address that already has
an account gets 409, like an invitation. Public forms allow 10 submits
per IP per hour (past that, the usual 200 message, nothing stored, logged server-side; DEC-52); a duplicate pending request, a filled
honeypot, or 5+ submits in the hour flags the stored row.

Admins get a waiting bell item per request. Only a clean adult request
carries inline Approve (DEC-44): a teen or flagged request shows Review
only, and approving it with `fromNotification` answers 403 whatever the
client sends. A lost inline race answers 409 with `resolvedBy`.

`accountRequest`: `{id, name, email, phone, accountType, birthDate?,
message, source, requestedBy?, requestedByName?, groupId?, status,
hasWarnings, warnings: {duplicate, honeypot, rateLimited},
decisionNote, expiresAt, createdAt, decidedAt?}`.

### Site

```
GET /api/v1/site → {name, timezone, selfSignup}   (public)
PUT /api/v1/admin/timezone → 200 {timezone}   (admin)
    body: {timezone} — an IANA name, for example America/Denver
```

`timezone` is the church time zone. The schedule form defaults a new series
to the browser's time zone. A create request that omits `tzid` uses the
church time zone, or UTC when that setting is missing. An unknown name is 400.

### Events

An event belongs to a group, or to no group (a church-wide event). A series
stores a wall clock (`startsLocal`, `YYYY-MM-DDTHH:MM` with no offset) and
an IANA `tzid`. Occurrences are stored up to 18 months ahead and extended
daily. A weekly 7:00 pm America/Boise event stays at 7:00 pm local on both
sides of a daylight-saving change. `BYDAY` is the weekday in that zone.

Each date is addressed as `{eventId, date}` where `date` is the series
date `YYYY-MM-DD`. A per-date time change does not change `date`.

`visibility` is `public`, `private`, or `invite_only`: the event's own
value (DEC-48). A group event copies the group's visibility when it is
created and does not follow later group changes, except that making the
group more private tightens it (see Groups below). Church-wide events are
`public` or `private`. A leader of the event's group, or an admin, may set
a group event to any of the three. A value wider than the group sets
`leaderOpened: true` and writes an audit row `event.visibility_override`
(reason optional) and a derived in-app notice to the group's other leaders
and the admins. Any other visibility change clears `leaderOpened` and is
`event.visibility_changed`, with no notice.

An invite-only group's event that is `public` or `private` does not name
or link the group to people outside it. The page says `hostedBy` is the
church. The group still 404s. Registering does not join the group. People
outside that group see a registration count; members, leaders, and admins
see names (`attendeesVisible`).

```
GET  /api/v1/events
  → {events: [occurrence]}   (church-wide events, and group events the viewer
     can see but whose group they cannot; from yesterday through 90 days)
POST /api/v1/events                         → 201 {event}   (admin; church-wide)
GET  /api/v1/events/:id                     → 200 {event, occurrences, canManage, canSetVisibility}
GET  /api/v1/groups/:id/events?from=2026-10-06&to=2027-01-06
  → {events: [occurrence]}   (default: next 90 days, max 366-day window, max 100 per event)
POST /api/v1/groups/:id/events              → 201 {event}   (group leader or admin)
     body adds optional {visibility, visibilityReason} (DEC-51): defaults to the
     group's value; wider than the group is saved leader-opened with an
     event.visibility_override audit row and notice, as with PUT .../visibility
PUT  /api/v1/events/:id                     → 200 {event}   (leader, host, or admin; series edit)
PUT  /api/v1/events/:id/visibility          → 200 {event}   (leader, host, or admin)
     body: {visibility, reason?}
DELETE /api/v1/events/:id                   → 200 {}        (leader, host, or admin)
POST /api/v1/events/:id/hosts               → 200 {hosts}   (admin; church-wide only)
     body: {email}
DELETE /api/v1/events/:id/hosts/:userId     → 200 {hosts}   (admin; refuses the last host)
POST /api/v1/events/:id/cancel              → 200 {}        (leader, host, or admin)
     body: {date: "YYYY-MM-DD"} — cancels that stored date
PUT  /api/v1/events/:id/occurrences/:date   → 200 {occurrence}  (leader, host, or admin)
     body: any of {startsLocal, durationMinutes, location, title} — this date only
GET  /api/v1/calendar?from=&to=&tz=&mine=1&group=&deleted=1&remote=1&childcare=1
  → {occurrences: [calendarOccurrence], truncated}   (slim rows, no attendance;
     remote=1 keeps only events with remoteAvailable;
     childcare=1 keeps only events with childcareAvailable)
POST /api/v1/events/video/jitsi             → 200 {url}   (leader, host, or admin)
```

`event`: `{id, groupId?, groupName?, groupVisibility?, hostedBy?, title,
description, startsLocal, durationMinutes, tzid, startsAt, endsAt, location,
recurrenceRule, rruleUntil, visibility, leaderOpened, churchWide,
hosts?, videoProvider, videoUrl?, videoReveal, videoHidden?}`. `groupId` is omitted for a church-wide event and for an opened
event whose group the viewer cannot see; `hostedBy` is the church name
then. `hosts` (`{userId, name, email}`) is present for an admin reading a
church-wide event. `startsAt` and `endsAt` are UTC. `startsLocal` is the
wall clock.

`occurrence`: `{id, eventId, date, startsAt, endsAt, startsLocal, endsLocal,
tzid, durationMinutes, location, title, description, recurrenceRule, rruleUntil,
seriesStartsLocal, seriesTitle, seriesLocation, seriesDurationMinutes,
overridden, registrations: {going, maybe, declined, waitlisted},
myRegistration: "going"|"maybe"|"declined"|"waitlisted"|"removed"|null,
myGuestCount?, myWaitlistPosition?, visibility,
leaderOpened, churchWide, groupId?, groupName?, hostedBy?,
attendeesVisible, attendees?, capacity, seatsUsed, spotsLeft,
waitlistEnabled, guestsAllowed, videoProvider, videoUrl?, videoReveal,
videoHidden?}`. `startsAt`/`endsAt` are UTC.
`startsLocal`/`endsLocal` are the wall clock in `tzid`, including a
per-date override. `overridden` is true when this date has its own time,
title, or location. `attendees` (`{userId, name, status, guestCount}`) is
omitted when `attendeesVisible` is false. Counts stay either way.
`capacity` is the effective seat cap for the date (per-date override wins);
null means unlimited. `seatsUsed` counts seats (1 + `guestCount` per going
party of a listed account) while `registrations` counts rows — keep the two
meanings apart. `spotsLeft` is null when unlimited and never below 0.
`myWaitlistPosition` is set only when the viewer is waitlisted.

Create body: `{title?, description?, startsLocal, durationMinutes, tzid?,
location?, recurrenceRule?, rruleUntil?, capacity?, waitlistEnabled?,
guestsAllowed?, videoProvider?, videoUrl?, videoReveal?}`. `description` is plain text, at
most 5000 characters, the same limit as a group description. `startsLocal`
and `durationMinutes` (1 to 10080) are required. Omit `tzid` to use the
church time zone. The schedule
form sends the browser's time zone. An empty
`recurrenceRule` is one date. The rule must not contain `DTSTART` or a
newline; `rruleUntil` is a separate inclusive `YYYY-MM-DD`, not an `UNTIL`
inside the rule. A rule that does not parse is 400. `capacity` is a
positive whole number or null (null clears to unlimited); `waitlistEnabled`
holds overflow sign-ups on a waitlist when a capacity is set;
`guestsAllowed` is 0–5 (null clears to 0), capping extra unnamed guests on
one registration.

Childcare (M1.8 slice 3): event create and series update accept
`childcareAvailable` (default false), `childcareNotes` (up to 500
characters), `childcareAges` (up to 100 characters), and
`childcareCapacity` (1–100000 children per date, or null for unlimited).
Omitting a field on update preserves it. A zero limit answers 400,
`childcare_capacity_zero`: "Uncheck childcare instead of a 0 childcare
limit". Turning childcare off keeps registration requests stored and
ignores them until childcare is offered again.

Event and occurrence JSON include these four public childcare fields.
Only admins, the event's group leaders, and church-event hosts receive
`childcareUsed`, `childcareSpotsLeft`, and each attendee's
`childcareCount` and `childcareAgesNote` (`policy.CanSeeChildcare`).
Totals are omitted when childcare is off. Unlimited childcare omits
`childcareSpotsLeft`. A registrant sees their own `myChildcareCount`
and `myChildcareAgesNote` regardless of manager permissions. The slim
calendar row includes only `childcareAvailable`; `childcare=1` filters
without changing visibility. ICS descriptions say "Childcare available",
with the age range when set. Feeds never contain registration childcare
requests or totals.

`PUT /events/:id/registrations` and `POST /events/:id/registrations/add`
accept optional `childcareCount` (0–20) and `childcareAgesNote` (up to
200 characters). Omitted fields preserve an existing request. The PUT
response's `registration` includes both fields. A positive explicit
count when childcare is off answers 400, `childcare_not_available`:
"This event doesn't offer childcare". Under the occurrence lock, only
going requests use childcare places. Disabled accounts never count
(DEC-38), and changing a request excludes its old count. A request that
would exceed the childcare limit answers 409, `childcare_full`:
"Childcare is full for this date". The transaction leaves the registration
unchanged. Setting the count to 0 lets the person attend without childcare.
An attendance over-capacity add cannot bypass the childcare limit.

Maybe, declined, and waitlisted registrations can keep requests without
using places. There is no separate childcare waitlist. Promotion skips a
waitlisted party whose childcare request exceeds the remaining spots when
childcare is available and its capacity is set (DEC-80). The party keeps
its request, age note, and `waitlisted_at`. The next party that fits its
attendance pool and childcare promotes. Earlier picks in the same pass
consume childcare spots. A party needing no childcare can still promote
when childcare is full. Hand-promoting a specific person returns 409
`childcare_full` if their childcare request does not fit.
Promotion runs again when a going registration lowers its childcare count,
cancels, declines, moves to maybe, is removed, or its account is disabled.
Raising `childcareCapacity`, clearing its limit, or turning childcare off
also retries promotion for future scheduled dates. Each promotion still
requires attendance seats and follows the existing two-hour cutoff.
On account re-enable, a going registration that would overfill childcare
is dropped through the same DEC-47 audit and ask-a-leader notice as a full
attendance pool, including when attendance seats are unlimited.

Video (5.8, M1.8) is a link out, never an embed (DEC-16). `videoProvider`
is `none`, `link`, or `jitsi` (default `none`, which clears the URL);
`videoUrl` must be a full `https://` address of at most 2048 characters
when a provider is set; `videoReveal` is `always` or `registered_only`
(the default). `POST /events/video/jitsi` mints a new
`https://<jitsi_domain>/<church-slug>-<12 random chars>` locally (no API
call). A Jitsi link is validated like a pasted one (https, a host, no
userinfo such as `https://meet.jit.si@evil.example`); its host is not pinned
to `jitsi_domain`, since a leader may edit it. The link reaches a response only when the viewer may see it
(`policy.CanSeeVideoLink`): under `registered_only`, people going on that
date plus the event's leaders or hosts and admins; otherwise `videoUrl`
is omitted and `videoHidden` is true. The same rule governs the ICS
feeds (the public feeds carry `always` links only) and the waitlist
promotion email, with one widening for the owner of a personal ICS feed
(DEC-70): under `registered_only` the feed also carries the link for a
date they said maybe to, and for a date they haven't answered when they
are a member of the event's group or a host. Waitlisted, declined,
cancelled and removed dates, outsiders who haven't answered, and
signed-out viewers never get it (`policy.CanSeeVideoLinkInFeed`).

A recurring series in a personal feed is one VEVENT, so if any in-window
date qualifies, the link rides on the whole series entry. This
series-wide link is accepted for now; a strict per-date reveal would emit
RECURRENCE-ID overrides for the qualifying dates and leave the link off
the master VEVENT.

A series edit (`PUT /events/:id`) applies to every future date. Future
means `startsAt` is strictly after now; a date already underway stays.
Future dates without a per-date override move onto the new times and keep
their registrations (same occurrence row). A future date the new rule no
longer contains is cancelled, and people who registered going or waitlisted
see a derived notice. A date whose time moves sends `event.changed`
(variant `time`) to going and waitlisted people; a changed event place sends
`event.changed` (variant `place`) on every future date. Raising the capacity
promotes waitlisted parties in line order on each future date whose
effective capacity grew; lowering it removes nobody. Per-date overrides,
including a location-only override, are left alone. Dates the new rule adds
are inserted. The per-date update (`PUT /events/:id/occurrences/:date`)
takes `startsLocal?`, `durationMinutes?`, `location?`, `title?`, and
`capacityOverride?` (number or null, like `capacity`); time or place changes
fan out `event.changed`, and a raised override promotes.

Occurrences are returned in chronological order. `from`/`to` are inclusive
`YYYY-MM-DD` and at most 366 days apart. Cancelled dates are left out of
the list. The group detail's `upcomingEvents` are the scheduled dates from
now through 90 days.

### Registration

```
PUT /api/v1/events/:id/registrations   → 200 {registration, people}   (auth)
  body: {date: "YYYY-MM-DD", status?: "going"|"maybe"|"declined", guestCount?,
         attendanceMode?, childcareCount?, childcareAgesNote?,
         people?: [{personId, status, attendanceMode?, childcareRequested?}]}
POST /api/v1/events/:id/registrations/remove  → 200 {} | 403 | 404  (leader/host)
  body: {date: "YYYY-MM-DD", registrationId, note?}
POST /api/v1/events/:id/registrations/promote → 200 {} | 403 | 404 | 409  (leader/host)
  body: {date: "YYYY-MM-DD", registrationId}
POST /api/v1/events/:id/registrations/add     → 200 {} | 403 | 404 | 409  (leader/host)
  body: {date: "YYYY-MM-DD", userId? | email? | (personId + registeredBy),
         guestCount?, childcareRequested?, overCapacity?}
```

`registration`: `{userId, eventId, occurrenceId, date, status, guestCount,
waitlistPosition}`. `waitlistPosition` is null unless the person is
waitlisted. One row per (user, occurrence); PUT is idempotent. The date must
be a scheduled occurrence. The caller must be allowed to see the event;
otherwise the response is 404. Seeing an opened event does not require
seeing its group, and registering does not add a membership. A disabled
account's registration is kept and hidden from the counts.

Capacity (M1.5): one seat bucket per date. `guestCount` (0 to the event's
`guestsAllowed`) counts extra seats with the party. Going past a set
capacity waitlists when the event's `waitlistEnabled` is on (409 when off).
A removed person cannot re-register alone (409). `overCapacity` is refused
on self-registration (400). Cancelling, declining, or moving going→maybe
promotes the earliest waitlisted party that fits; inside 2 hours of the
start nobody is auto-promoted and leaders/hosts get `waitlist.cutoff`.

Household people (M1.9 slice 2). `people` registers confirmed people from
the caller's household by name, one registration and one seat each, in that
person's `attendanceMode` pool, under the same occurrence lock and waitlist
order as anyone. The holder's own fields are optional: an omitted holder
`status` leaves the holder unchanged, and unmentioned people are unchanged.
Any failed answer rolls back the whole batch. The response's `registration`
is the holder's row (null if never answered), and `people` is
`[{registrationId, personId, registeredBy, status, attendanceMode,
waitlistPosition, childcareRequested}]`. Errors (409): `household_link_pending`
("Waiting for an admin to confirm"), `household_link_unconfirmed`,
`household_birth_date_required` and `household_age_mismatch` (an `adult`
person needs a birth date proving 18 or older; the kind alone isn't trusted),
`household_remote_childcare`, `household_registration_holder_unavailable`.

Childcare with household people: `childcareRequested` is an explicit
per-child choice for an in-person named child, one childcare spot each.
Remote people never use childcare. `childcareCount` stays the holder's
count of unnamed children. DEC-80 promotion applies to both.

Attendee lists give each row a `registrationId`. A household row has no
`userId` and carries `householdPerson: {personId, name, householdName, kind,
age?, registeredBy: {userId, name}}`; the UI shows "Kit · Leader household
(via John Smith)". Age and care notes follow section 6 (care notes to the
event's leaders and hosts from 24 h before to 24 h after the date). Notices
about a household person go to the holder. When a holder is disabled, their
household people's rows are hidden and uncounted with theirs (DEC-38).

Leaders and hosts can remove anyone's registration (sets `removed`, tells
the person with the note, audited), hand-promote one waitlisted party
(409 when it doesn't fit, even in the cutoff), or add someone directly —
over capacity only with explicit `overCapacity: true` (audited).

Guest registration (M1.4, C5) is the signed-out door for public events:

```
POST /api/v1/events/:id/guest-registrations        → 200 {message} | 400 | 404  (public)
  body: {name, email, date, over18, createAccount?, timezone?, website?}  (timezone: the browser's IANA zone for the guest's emails, DEC-58)
POST /api/v1/guest-registrations/confirm           {token} → 200 {registration, manageToken} | 400 | 409  (public)
GET  /api/v1/guest-registrations/manage/:token     → 200 {registration} | 404  (public, token is the credential)
POST /api/v1/guest-registrations/manage/:token/cancel → 200 {registration} | 404  (public)
```

Submitting stores only an unverified signup and emails a single-use
confirmation link (24 hours) to `/guest/confirm`; nothing is an
attendee until the link is redeemed. Confirming creates or reuses the
guest `User` row (`status: guest`, no password, cannot sign in) and an
ordinary `going` registration against it, then emails a confirmation
with the manage link. The manage token is stored hashed; using it to
cancel sets the registration `cancelled` (out of every count and name
list) and writes `registration.cancelled`. It stops working after the
date has passed.

The door is public events only (`policy.CanRegisterGuest`); anything
narrower answers 404. Without `over18: true` nothing is stored and the
answer is 400 with the guardian wording. An email that already has an
account gets the same `200 {message}` as any other submit, and a
sign-in hint by email (no enumeration); an email that already has a
guest row merges into that row. Verification emails stop after 3 per
address per hour and 10 per day, whatever the IP, with the same
uniform answer; the per-IP ceiling matches the other public forms.
With `createAccount: true`, confirming also opens a `guest_upgrade`
account request; approval upgrades the same row (as does accepting an
invitation), so guest registrations stay the person's. A guest cannot
hold a group membership until that upgrade happens.

`guest registration` (the confirm and manage responses): `{eventId,
occurrenceId, date, title, hostedBy?, groupName?, status, startsLocal,
tzid, location, waitlistPosition?}` — the event fitted for a signed-out viewer, so an
opened invite-only event never names its group. `waitlistPosition` is set
only when the guest is waitlisted. Confirming on a full event with the
waitlist on waitlists the guest; with the waitlist off it answers 409
`{full: true}`, creates nothing, and the link dies.

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

Disabling (DEC-38): the account's memberships and registrations are kept, hidden from rosters, and left out of member counts, registration counts, and leader counts; any leadership becomes plain membership (with `membership.role_changed`, reason `account_disabled`). Disabling frees every seat the account held on future dates, promoting waitlisted parties into them. Enabling it again restores them as plain member, writing `membership.restored` per membership and `user.enabled`. Going registrations are restored when they fit; one that would overfill is deleted instead (DEC-47): `registration.not_restored` is written and the person gets an ask-a-leader notice. Waitlisted rows keep their place. In a group at its member limit the membership is dropped instead (DEC-47): `membership.not_restored` (meta `userId`, `count`, `limit`) is written and the person gets a notification ("You're not back in … yet", "Ask a leader to add you.", id `not-restored:<audit id>`, kept 30 days). A leader or admin can add them with `overLimit: true`. Deleting still ends memberships.

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

### ICS feeds (outside `/api/v1`)

```
GET /cal/public.ics        → 200 text/calendar  (public)
GET /cal/groups/<id>.ics   → 200 text/calendar | 404  (public; active public groups only)
GET /cal/u/<token>.ics     → 200 text/calendar | 404  (the token is the credential)
```

Feeds cover 30 days back to 12 months ahead, carry `ETag` and
`Cache-Control: private, max-age=900` (304 on `If-None-Match`), and
never carry attendee data. Contents follow the viewer's permissions at
fetch time; a disabled or deleted owner's token answers the same plain
404 as an unknown one.

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
