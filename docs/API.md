# Fold API — v1 contract

Base path: `/api/v1`. Responses are JSON unless an endpoint explicitly returns CSV or a feed. Keys are camelCase.
Errors include `{"error": "message"}` and, for actionable errors, `code`; some include structured details. The SPA uses client-owned wording, never server error text.
Timestamps are RFC 3339 (`2026-10-06T19:00:00Z`).

## Auth & CSRF

Cookie sessions. Unsafe API requests (POST/PUT/DELETE) must send the CSRF
token as **`X-CSRF-Token`** (or the `csrf_token` form field). Only explicitly
registered provider hooks outside this API may be exempt.

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
POST /api/v1/auth/login    {email, password} → 200 {user, canCompose} | 401 | 429
POST /api/v1/auth/logout                         → 200 {}
GET  /api/v1/auth/me                             → 200 {user, canCompose} | 401
POST /api/v1/auth/password-reset         {email} → 200 {message}  (public)
POST /api/v1/auth/password-reset/confirm {token, password} → 200 {user} | 400  (public)
```

Password reset (M1.2): the request answers `200 {"message":"If that email
has an account, we've sent a link"}` for every address, known or not, after
one bcrypt comparison on a miss (no account enumeration). Per-IP (10/hour)
and per-address (3/hour, 10/day) throttles live in `login_throttles`; throttled
requests still answer uniformly. Tokens are 32 random bytes, stored as
SHA-256, single use, and expire after 24 hours. Confirming sets the password
(≥ 10 chars), ends every other session, and signs the browser in only if the account is active. A pending-consent teen stays signed out.

Sign-in is by email in any letter case (`users.email` is `citext`, unique).
Ordinary access requires an `active` account with a password. A teen in
`pending_consent` cannot establish a login. With a verified password, login
answers `403 consent_required` and explains the consent requirement; no
account privileges are stored in the cookie. Pending teens browse public
groups and events as signed-out visitors (DEC-85). Every other failure, whether
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
POST   /api/v1/me/email/verify  {token} → 200 {user} | 400  (public capability; CSRF required)
POST   /api/v1/me/email/cancel  {token} → 200 {cancelled: true} | 400 link_expired  (public capability; CSRF required)
GET    /api/v1/me/calendar-feed → 200 {calendarFeed: {hasToken, scope}}  (auth)
POST   /api/v1/me/calendar-feed {scope?} → 200 {calendarFeed: {hasToken, scope, url}}  (auth)
PUT    /api/v1/me/calendar-feed {scope} → 200 {calendarFeed} | 404  (auth)
DELETE /api/v1/me/calendar-feed → 200 {calendarFeed}  (auth)
DELETE /api/v1/admin/users/:id/calendar-feed → 200 {calendarFeed}  (admin)
```

Calendar feed (M1.7, plan 8.2): one secret link per person. Only the
token's SHA-256 is stored, so `url` (with the raw token) comes back only
from POST; POST also remakes the link, which revokes the old one.
`scope` is `"mine"` (default: dates in the viewer's own scope: their groups,
church-wide dates in the next 30 days, events they host, their own answered or
exact-access dates, and dates registered for household people) or `"all"`
(every authorized date, with unanswered public and church-wide discovery dates
limited to the next 30 days). All feeds export stored dates so exclusions cannot
be reintroduced by an RRULE. Every row is a standalone VEVENT whose
immutable identity is `UID:event-<eventId>-<originalDate as YYYYMMDD>@<host>`; it has no
`RECURRENCE-ID`. A series time edit therefore changes `DTSTART` without changing
the row's identity or its stored occurrence/registration. Date-only authority
(for example, one public override of a hidden series) uses that same UID and
exact-date URL, but emits no parent-series recurrence identity, description,
hidden-group fallback, childcare details, or parent video metadata. Effective remote
availability is date participation information: it is included when enabled and
the date's effective remote capacity is unlimited or positive, even if the parent
series is hidden. Zero capacity omits the marker, not the date or an independently
authorized video link; a full positive-capacity pool still advertises remote attendance.
Going is `STATUS:CONFIRMED`; Maybe, Waitlisted and Not answered are
`STATUS:TENTATIVE`; Can't go is omitted. These are the viewer's own answers:
household registrations supply scope, not an RSVP for the viewer. A household-only
Going date is therefore `TENTATIVE`, and the viewer's Can't go omits it from both
personal scopes even when a household person is Going; their registration is unchanged.
Video eligibility is checked separately and does not change this status.
Public and group dates are `CONFIRMED`.

Date-specific UIDs across all feeds are an intentional pre-release wire correction
from earlier series masters and detached `(series UID, RECURRENCE-ID)` rows.
Anyone who imported a development feed from an earlier build may see the old rows removed and the
corrected rows added once; removing and re-adding that development subscription
is the safest cleanup if a client retains stale rows. Representative external
calendar clients still need manual interoperability checks. Revoking a link that is already off still
answers 200.

Profile update body: any of `{name, displayName, phone, email, currentPassword, timezone, hideContactFromLeaders, avatarFocal: {x, y}}`.
`hideContactFromLeaders` is a boolean preference for the signed-in adult only,
off by default. A teen's submitted preference is ignored: their contact is
always hidden from leaders and hosts. Changing the adult preference writes
`contact.privacy_changed` in the same transaction (target: user; metadata:
`{hideContactFromLeaders: boolean}`). Repeating the current value is a no-op.
Limits: names ≤ 100 chars, phone ≤ 30 chars, focal clamped to 0-100,
`timezone` an IANA name (DEC-58; "" and "Local" are refused). The
email is the login: sending a different `email` starts a verified change
(M1.2, 10.1, DEC-87) only when `currentPassword` matches the signed-in
person's current password. Password proof is checked as submitted, without
trimming. A verification link goes to the new address and a cancellation
notice goes to the old address; the response carries `verificationSent: true`.
Only the newest verification/cancellation pair works, and a new request
invalidates both links from the previous pair. When the browser opens the
new-address verification page, the SPA automatically redeems the single-use
24-hour capability with a CSRF-protected `POST /me/email/verify`; an HTTP GET
alone does not change the account. Every other session then ends, while the
verifying browser stays logged in.

`POST /me/email/cancel` redeems the old-address capability while its change
is still pending. It leaves the email unchanged, returns `{cancelled: true}`,
revokes every unused account token, and advances the subject account's
credential epoch. Every cookie for that subject, including a cookie in the
browser making the request, then fails authentication. The public capability
does not sign in the caller or refresh an authentication epoch; if a different
account is signed in while following it, that different account stays signed
in. A used, expired, replaced, or post-verification cancellation token answers
`400` with `code: "link_expired"`; it can never roll back an email already
changed. Opening the cancellation page is read-only: cancellation requires
the person's explicit confirmation through its CSRF-protected POST.

Sending the current address (any case) is accepted and changes nothing. Within
the actor allowance, changing to an address another account uses answers 409.
All current-password-proven change attempts, including these account checks,
consume the actor's limit of 3/hour and 10/day. An exhausted actor receives
the normal 200 with `verificationSent: true` without any address lookup,
recipient-budget use, token change or email. Actual verification sends also
respect the recipient's 3/hour and 10/day allowance. Public sign-up, guest,
and password-recovery responses retain their existing uniform behavior.

`POST /me/password` body: `{current, password}`. `password` is at least 10
characters. The current browser stays logged in. Every other session for
that account ends. An admin password reset ends all of that account's
sessions, including the one that requested it. Setting, changing or resetting
a password, an admin password reset, a verified or admin email change,
disabling, or deleting an account atomically revokes every unused
`account_tokens` row for that user, whatever its kind.

`account_tokens.kind` accepts `password_reset`, `email_verify`, `account_setup`,
`teen_consent`, and `email_change_cancel`. Migration 40 deletes unused legacy
`email_verify` rows because those links were issued before current-password
proof was required. Its downgrade deletes all `email_change_cancel` rows and
unused `email_verify` rows before restoring the four-kind check; used
verification history remains.

`user`: `{id, name, displayName, email, phone, avatarUrl,
avatarFocalX, avatarFocalY, role, status, hasPassword, accountType, timezone,
hideContactFromLeaders, birthDate?, hasTeenConsentHistory?}`.
`hideContactFromLeaders` is always present and is the effective setting
(always true for teens, regardless of the stored preference). The full `user`
shape is restricted to self/session and admin account endpoints; it is never
a roster or attendee DTO. `hasTeenConsentHistory` is admin-list-only. `status` is
`active`, `invited` (no password yet), `pending_consent` (a teen waiting
for consent, M1.10), `guest` (a verified guest registrant with no
password and no sign-in, M1.4), or `disabled`.
`accountType` is `adult` or `teen` (DEC-8); `birthDate` (`YYYY-MM-DD`)
is present for teens. `displayName` falls back to name in the UI. `avatarUrl` is a
`/uploads/...` URL, or empty when no avatar is set. `avatarFocalX/Y` are
0-100 percentages for CSS `object-position`.

Teen accounts cannot upload public avatars (`403 teen_avatar`), and group roster responses never expose a teen avatar URL.

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
GET    /api/v1/admin/users/:id/household            → 200 {household|null} (admin; read-only)
POST   /api/v1/admin/users/:id/household            → 201 {household} {name?, address?} (admin; conflicts if already linked)
POST   /api/v1/admin/users/:id/household/people     → 201 {person} {firstName, lastName?, kind, birthDate?, careNotes?, photoConsent?, guardianUserId?} (admin)
GET    /api/v1/admin/guardian-links?status=pending  → 200 {guardianLinks, nominations}   (admin)
POST   /api/v1/admin/guardian-links/:id/confirm     → 200 {guardianLink}    {checkNote} (admin; note required; not your own link while another active admin exists, DEC-79)
POST   /api/v1/admin/guardian-links/:id/reject      → 200 {guardianLink}    {note?} (admin)
POST   /api/v1/me/guardian-claims                   → 200 {message}        {email} (active adult)
GET    /api/v1/me/guardian-teens                    → 200 {teens}          (confirmed guardian)
POST   /api/v1/me/guardian-links/:id/consents        → 200 {}               {scope, status} (confirmed guardian)
POST   /api/v1/me/guardian-links/:id/consent-email   → 200 {message}        (confirmed guardian)
GET    /api/v1/guardian-consent?token=...           → 200 {teenName, scope, status, expiresAt} | 400 (public preview)
POST   /api/v1/guardian-consent                     → 200 {} | 400          {token} (public, CSRF required)
```

`GET /me/household` never writes (Warden, #106). The household is created by
`PUT /me/household` or the first `POST /me/household/people`, and named after
the account's last name. Any adult account in the household manages it; teen
accounts get `null` from GET and 403 from the writes. Adding a `child` or
`teen` person opens a pending guardian link that only an admin can confirm,
with a note on how it was checked (DEC-32); an `adult` needs no approval.

Admin household delegation uses an existing active adult account with a password
as the holder (`:id`), never the acting admin. GET does not create anything;
POST creates the household, its adult account link, and the attributed audit
row in one transaction. An existing household link answers `409 household_exists`;
adding people before creating a household answers `409 household_required`.
Adding a child or teen requires an explicit `guardianUserId` naming an eligible
active adult account in the same household (the holder or another adult), never
the acting admin. The guardian link starts pending, with the ordinary admin
confirmation task. Missing or ineligible guardians answer `409 household_guardian`;
ineligible holders answer `409 household_holder`. Person/link/task/audit writes
are atomic. `createdBy` and the audit actor identify the admin, not the holder
or guardian. There is no household directory, search, delegated edit/delete,
or admin household UI in this slice.

Guardian claims return the same message for unknown addresses, adults,
duplicates, and throttled submissions. The IP cap is 10/hour; the target
address cap is 3/hour and 10/day. Pending links grant no access and do not
appear in the adult's teen list. Only an admin confirms them, including
the DEC-79 self-confirmation rule. The guardian endpoints require the
adult's own confirmed link. Scopes are `account`, `sms`, and `photos`;
statuses are `granted` and `revoked`.

Confirming a teen account link emails the adult an account-consent link.
The link lasts 24 hours, is signed, and can be used once. GET previews the
teen's name and the account grant without changing consent. The explicit
POST needs CSRF. Redemption rechecks the adult's active status, email,
session version, confirmed link, teen account, and latest account-consent
revision under transaction locks. A subsequent consent decision invalidates
older emails. A resend replaces emails for that link only. Token URLs use
a query parameter, which the access logger drops, and the database stores
only the token hash. Notification and audit rows contain no token.

An active adult in the household or an active adult admin can invite an
eligible household person to their own account. Admins may act across
households; unrelated members receive 404. A real birth date must prove
an adult is 18 or older on the church-local day. Children cannot convert.
Teen household conversion is enabled for ages 13 to 17 with a confirmed household guardian link. Acceptance stores a pending account request and no User. Admin approval creates a `pending_consent` account and records `AccountRequest.approved_user_id`, while keeping the household person and registrations unchanged. Fresh teen-account guardian links start pending. Account consent and a newly confirmed teen-account link to an original confirmed household guardian are both required before transfer. Admin evidence can grant consent but cannot replace this guardian check.

The household response uses `conversion.state: "awaiting_consent"` for approved staging. It has no invitation resend or cancel action and does not expire as an unanswered request. Another invitation or different-email attempt gets 409. Identity edits (name, kind, birth date) and removal get `409 household_conversion_consent`; care notes, photo settings, and household RSVP remain available. Registration transfer retains IDs, answers, timestamps, and waitlist places, without event-access or capacity rechecks. Independent admin suspension delays transfer; enabling a ready account completes it.

`conversion` is `{email, expiresAt, state: "pending"|"expired"|"awaiting_approval"|"awaiting_consent"}`.
Household people include `canConvert` and optional `conversion` metadata.
Teens become eligible after the household guardian link is confirmed and their birth date proves age 13 to 17. Metadata exposes no token or hash. Household accounts include `status`; converted people
appear only under accounts and cannot be edited or removed as household people.

Issuing twice to the same pending destination returns its state without
sending again. A different destination requires cancellation first. Resend
keeps the destination, rotates the token, and gives it 24 hours. Expired
invitations are revoked before replacement. Cancellation is idempotent and
remains available after eligibility changes. Name, kind, or birth-date edits
and deletion revoke the invitation in the same transaction. Care notes,
photo consent, and household name or address changes keep it open.

Conversion uses the existing invitation endpoints below. Adult and teen
acceptance return `{pendingApproval: true}` without a session. It stores a
pending `household_conversion` account request with the chosen password's
bcrypt hash and the household person and invitation IDs, and marks the
invitation accepted. It creates no user or household account and changes no
registrations or guardian links. The household continues managing the person
and all their sign-ups. While pending, the email is reserved and new or resent
conversion invitations for that person return 409 with
`household_conversion_approval`. The household person remains in `people`
with `conversion.state: "awaiting_approval"`.

Adult approval creates an active member account using the held password hash,
transfers every registration in place, marks the person converted, and adds
an adult account to the same household, all in the decision transaction.
IDs, answers, attendance mode, queue order, and timestamps stay unchanged;
`registeredBy` and named childcare flags are cleared. Group memberships do
not move. Approval emails a sign-in note referring to the password already
chosen. A deleted or converted person, unavailable email, or unavailable
responsible account returns 409 and leaves the request pending. Rejection
or 30-day expiry closes the request and clears the held password hash;
nothing needs undoing, and the email can be invited again.

Approval also returns 409 without moving anything or closing the request when:

- The full name or birth date differs from the accepted request snapshot:
  `household_conversion_details`.
- The person no longer meets the selected adult or teen age and guardian rules
  on the church-local date: `household_conversion_adult`.
- The original inviter is disabled, deleted, or missing:
  `household_conversion_inviter`.
- The household's lock set changed during approval:
  `household_conversion_retry`. Try approving again.

For details, adult-rule, or inviter refusals, reject the stale request before
asking an active household adult or admin to send a new invitation.

A first send suppressed by the invitation limit saves no invitation row,
but the response has the same keys as a normal send. Pending-consent teen
accounts created through other paths retain their seats and the DEC-82
roster rules. Event managers see them with a teen badge; unrelated members
do not see their names.

Creation and resend share the 20-per-rolling-day invitation limit by sender
and destination, including resends. Sender allowance is checked before address
availability; unavailable-address and idempotent live-invitation checks count as
`invitation.refused` attempts, without spending a destination's mail allowance.
These actor counters are shared with ordinary leader invitations. Counters live
in Postgres audit rows. Conversion sends over the limit return the normal acknowledgement and log
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
GET  /api/v1/groups/:id?cursor=&limit=    → {group, members, upcomingEvents, truncated, hasMore, nextCursor, canManage, isMember}
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
status, leaderId, visibility, deletionRequested, memberLimit, myRole?}`.
`myRole` (`leader` or `member`) is the viewer's own active role, sent only on
`GET /groups` and `GET /me/groups` and only for groups they belong to (DEC-94).
Those two lists run by `nextEventAt`, soonest first, with groups that have no
upcoming date last, then by name.
`memberLimit` is null
for no limit (DEC-39); `memberCount` counts active members of listed accounts. `leaderId` and `leaderName` are the
displayed primary contact only (one of the leaders); they grant nothing.

`group` (detail response): `{group: groupSummary, members: [...],
upcomingEvents: [occurrence], truncated: bool, hasMore: bool,
nextCursor: string, canManage: bool, isMember: bool}`.

Upcoming dates use a bounded, forward-only continuation, not next-date
search. Each request covers scheduled dates starting at or after now, with
an original date through the UTC date 90 days ahead. `limit` defaults to
25 and accepts integers from 1 through 100. Dates are ordered by start time,
then occurrence ID; visibility is checked before the page limit and
continuation metadata. There is no per-series cap: every visible date in the
window can be reached by paging. Hidden dates never consume a slot or imply
another page. Attendee enrichment runs only for the returned page.
`hasMore` and `truncated` are equivalent: another visible date remains in
this window. When true, `nextCursor` is an opaque last-seen occurrence key;
pass it unchanged as `cursor` to request the next page. Otherwise it is `""`.
A cursor belongs to one group, grants no access, and each page checks current
visibility again. Invalid cursors or out-of-range limits return 400
(`group_upcoming_page`). The time window is evaluated anew on each request;
this is not a frozen snapshot when dates or permissions change.

The SPA merges continuation pages by occurrence ID and resets loaded dates
and cursors when the group or viewer changes. Per-series “more dates loaded”
counts describe only those loaded, visible dates, not all dates in the
90-day window or the full series. The calendar link remains available when
the list is truncated. `/groups/:id/events` uses the same paging rules over
its requested `from`/`to` window (see Events below); the calendar has its own
truncation flag. Group detail does not accept `from`/`to` window controls.

`members` is filled only for the group's members and admins (people see each
other only inside groups they share); otherwise it is `[]`. `canManage` is
true for the group's leaders and admins. Roster member: `{id, name, avatarUrl, accountType?, role,
joinedAt, admin, email?, phone?}` (also `avatarFocalX/Y`) — `name` is the member's display name; `admin` marks church admins (DEC-38).
Contact fields follow the context-specific rules below; unauthorized keys are
omitted entirely, not returned as null or empty strings.

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

Group updates patch only supplied fields onto the current locked row; omitted
fields retain concurrent changes. `status` may also be supplied: leaders may
archive, but only admins may approve or reactivate. An unrelated pending-group
edit does not decide approval. The transaction rechecks the actor's session and
current group authority: revoked credentials return 401, a now-hidden/deleted
group returns 404, and a visible group the actor can no longer manage returns
403. Approval tasks, tightening, audits and notices commit or roll back together.

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
GET  /api/v1/groups/:id/departure-preview → 200 {departure} (auth; read-only self-leave preview)
GET  /api/v1/groups/:id/members/:userId/departure-preview → 200 {departure} (auth; leader/admin removal preview)
POST /api/v1/groups/:id/leave   → 200 {departure}    (auth; CSRF; confirmed self-leave)
     body: {snapshot, keepOccurrenceIds: [id, ...], note?}
POST /api/v1/groups/:id/members/:userId/departure → 200 {departure} (auth; CSRF; confirmed leader/admin removal)
     body: {snapshot, keepOccurrenceIds: [id, ...], note?}
POST /api/v1/groups/:id/members → 200 {membership}  (auth; leader or admin)
     body: {selectionId | email, overLimit?} — one target only; teen adds need a separate Ask to add; a full group
     answers 409 {error: "This group is full", full: true} unless overLimit is true (audited)
DELETE /api/v1/groups/:id/members/:userId → 200 {departure}  (auth; leader or admin; DEC-35; confirmed legacy alias)
     body: {snapshot, keepOccurrenceIds: [id, ...], note?}; note stays only in audit/removal records and is never shown to the removed person
PUT  /api/v1/groups/:id/members/:userId/role → 200 {members: [...]}  (auth; leader or admin)
     body: {role: "leader"|"member"} — userId must already be an active member
POST /api/v1/groups/:id/deletion-request → 200 {group}  (auth; a leader)
DELETE /api/v1/groups/:id/deletion-request → 200 {}    (auth; leader or admin)
GET  /api/v1/groups/:id/members → {members: [...]}  (auth; members and admins; 404 if you cannot see the group, 403 if you are not in it)
```

Join returns 404 when the group is not visible, and 403 when it is
invite-only and the caller is not already a member. A full group answers 409
"This group is full" (no waitlist). A disabled account re-enabled into a full
group is left out and told to ask a leader; there is no pending-return state. A deletion request is
accepted only from a leader who is the group's only active member. Adding
a member clears a pending request.

Role changes: only adults can be made leaders (teen accounts arrive in
M1.10 and are refused), and a group never loses its last leader. Demoting
the only leader is a 400; a leave/removal confirmation that is no longer
authorized is a coded 409. There is no admin
approval step for a leader promoting a co-leader (DEC-31). Instead every
change that makes or removes a leader (including leaving and admin
add/remove) writes a `membership.role_changed` audit row (actor, group,
person, old and new role, IP), and admins get a `leadership` notification.
When the primary contact stops leading, `leaderId` moves to the
earliest-joined remaining leader. `DELETE` on the
request withdraws it without deleting the group.

`membership`: `{id, userId, user: {id, name}, groupId, role: "leader"|"member",
status, joinedAt}`.

Departure preview (M1.14 T4a, DEC-74) uses one facts implementation for self-leave and authorized leader/admin removal. It is a read-only, repeatable-read GET and never changes groups, memberships, registrations, exact-date access, audit rows, or notifications. The response is:

```text
departure: {
  action: "leave" | "remove",
  groupId, groupName, subjectId, subjectName,
  affectedCount, goingCount, maybeCount, waitlistedCount,
  keepableCount, defaultKeepCount,
  dates: [{
    occurrenceId, date, title, startsAt, endsAt,
    status: "going" | "maybe" | "waitlisted",
    visibility: "public" | "private" | "invite_only",
    defaultKeep,
    keepReason: "underway" | "cutoff" | "public"
  }],
  snapshot
}
```

Only the subject account's scheduled, not-ended Going, Maybe, and Waitlisted registrations in this group are affected. Past attendance stays. Declined (already Can't go), cancelled, and leader-removed registrations are already ended choices and are excluded. Household/person and other proxy registrations are never departure registrations. A scheduled date already underway or at/inside the existing two-hour cutoff is keepable and checked by default. A later date whose effective per-date visibility is public is keepable but unchecked. Other future private/invite-only registrations count as affected but are not returned in `dates`. Zero affected dates still returns a valid confirmation context and snapshot.

Keepable rows are date-only and contain no attendees, location, hidden parent-group data, recurrence rule, sibling dates, roster, or management metadata. `visibility` is the effective date value (the override when present). The opaque snapshot is authenticated with a key derived from `SESSION_SECRET` and bound to the current actor, subject, and group. Its digest covers current actor/subject and membership facts, leader count, the complete affected set, displayed date clocks/status/effective visibility, the subject's own registration, and active exact-date access/provenance. Other attendees and capacity, waitlist, guest, remote, and childcare configuration are deliberately excluded: confirmation uses current resource math under occurrence locks. T4b must parse it with `parseDepartureSnapshot`, recompute `loadDepartureFacts` under its locks, and require `departureSnapshotMatches` before any write; client-supplied counts, date facts, or snapshots are never authority. The snapshot has no observation timestamp: ordinary elapsed seconds do not stale it, while crossing the cutoff/start/end boundary changes derived facts and does.

A missing/hidden group is 404, a visible group the actor cannot manage is 403, and changed membership/leadership authority is 409 `departure_unavailable`.

Departure confirmation (M1.14 T4b) sends the preview's opaque `snapshot` plus only occurrence IDs selected from its keepable `dates`. The response is the safe summary `{departure: {action, groupId, subjectId, keptCount, endedCount}}` for self-leave and removal alike; it never includes a roster. Clients refetch the normal authorized group or member GET after removal. Under one transaction the server takes the contact gate, locks the group, actor/subject accounts in ID order, and every affected occurrence in ID order; it then reloads and compares the entire preview snapshot before writing. Missing, malformed, tampered, actor/subject/group-mismatched, expired-by-fact-change snapshots and forged/nonkeepable `keepOccurrenceIds` answer coded 409 `departure_stale` with no domain writes. Legacy leave and DELETE removal routes use this same contract and cannot bypass confirmation.

A selected date is retained with DEC-74 departure provenance tied to the locked membership, actor, and current registration. An already-correct departure-origin row is retained. If an active `manager_add` row supplied the date, it is irreversibly ended as `departure_superseded` without a per-date notice and a new departure-origin row is inserted; the manager issuer/request/origin remain immutable history and the successor expires at the occurrence's exact end. Every other current, not-ended registration belonging to the subject account is ended through the normal registration batch, preserving capacity/childcare release, waitlist promotion and the two-hour cutoff. Proxy registrations are untouched. The membership row remains: self-leave marks it `left`, while leader/admin removal marks it `removed` (DEC-86). A voluntary leaver may self-rejoin under the normal visibility and member-limit rules; a removed person still needs a leader/admin to add them back. Reactivation preserves the first-ever join date and advances tenure, invalidating old departure previews. Contact grants/requests are ended through their normal lifecycle, primary-contact and leadership audits/notices are maintained, and all writes roll back together on failure. Self-leave tells current leaders once; removal sends the removed account and confirmed teen guardians one privacy-safe summary with kept/ended counts. Departure does not emit per-date update notices, and email delivery starts only after commit.

### Invitations

```
POST /api/v1/invitations          → 201 {invitation}  (auth)
GET  /api/v1/invitations/:token   → 200 {invitation, inviterName, siteName, site} | 404  (public)
POST /api/v1/invitations/accept   → 200 {user?, pendingConsent, pendingApproval} | 400 | 409  (public)
```

Invitations (M1.2, 5.10, DEC-4): admins invite into any group or none;
leaders invite only into groups they lead or none (403 otherwise); members,
teens, and guests get 403, signed-out visitors 401. There is no approval
step for adults: the person confirms their age and sets a password on acceptance. Teens start `pending_consent`.
Create body: `{email, name?, phone?, groupId?, overLimit?}`. The invitee chooses their age when accepting; the inviter cannot activate a teen. 409 when the email already has
an account or a pending invitation. Leaders are limited to 20 valid invitation
attempts per rolling day, including refused existing-account and duplicate-invitation checks. Refusals are durable `invitation.refused` actor counters, not sent-mail counters; they do not consume a recipient's mail allowance. The actor allowance is checked under its transaction gate before the address lookup. The 21st attempt gets 429 and is audited
(`invitation.rate_limited`).

Tokens are 32 random bytes, stored as SHA-256, single use, and expire after
24 hours. Ordinary accept body: `{token, name, password, ageBand: "adult"|"teen"|"under13", birthDate?}` (password ≥ 10 chars). A teen needs a real birth date aged 13 to 17 on the church-local date. Missing or inconsistent ages answer 400. Under-13 input is not stored and the token remains usable. Adults start active; teens start pending consent without a login and may browse public groups and events as signed-out visitors. Household conversion acceptance still uses its saved adult birth date. Accepting into a full group joins over
`member_limit` only when the inviter set `overLimit` (an explicit, audited
choice, DEC-39); otherwise it answers `409 {"error":"This group is
full","full":true}` without consuming the token. The over-limit join is
audited as `membership.added_over_limit`.

`invitation`: `{id, email, name, groupId?, groupName?, expiresAt, conversion, accountType}`.
Ordinary invitations have `conversion: false`. Conversion invitations have
no group and explain the transfer on the accept page. Household adult or teen acceptance waits for approval without a session. Teen transfer also waits for consent and a fresh guardian check.
Ordinary acceptance signs in active accounts only; pending teens get the public consent explanation without a login.

### Account requests

```
POST /api/v1/account-requests              → 200 {message} | 400 | 403  (public)
POST /api/v1/account-requests/invite-friend → 200 {message} | 400 | 403 | 409  (auth)
POST /api/v1/account-requests/confirm      {token, password} → 200 {user, pendingConsent?} | 400  (public)
GET  /api/v1/admin/account-requests?status=pending → {requests: [accountRequest]}  (admin)
POST /api/v1/admin/account-requests/:id/approve → 200 {request, userId} | 409  (admin)
     body: {note?, fromNotification?, accountConsentEvidenceText?}
POST /api/v1/admin/account-requests/:id/reject  → 200 {request, userId: 0}  (admin)
     body: {note?, fromNotification?} — rejection is never inline (403 with fromNotification)
PUT  /api/v1/admin/self-signup             {mode: "request"|"off"} → 200 {selfSignup}  (admin)
```

Self-signup and invite-a-friend (M1.3, 5.10, DEC-4) create a pending
`AccountRequest`; only an admin approves. There is no open mode: the
`self_signup` setting is `request` (default) or `off`, and `off` refuses
the public door with 403 (the member door still works). Create body:
`{name, email, phone?, ageBand: "adult"|"teen"|"under13", birthDate?,
message?, website?, guardianEmail?}`. `website` is a honeypot people never fill in.

For a teen, `guardianEmail` nominates an adult. The request retains it for
admin review. Public submission creates neither a guardian link nor a User.
Approval creates a pending link if the nominated adult has an active account.
Otherwise, the nomination stays in the admin queue with instructions to help
the adult join and request a guardian link from Profile. `approved_user_id`
keeps this nomination attached when the teen later changes their email.

The age gate is on every door (DEC-8): `under13` stores nothing and
answers 400 with the household explanation; `teen` needs a `birthDate`
aged 13–17 and creates a teen request; `adult` needs no date (adults
only confirm 18+, and any date sent is dropped). For ordinary requests, approval creates the
account (`invited` for adults, `pending_consent` for teens unless account
consent is granted with approval). A staged household conversion also waits for
its fresh original-household guardian link. Ordinary approval emails a single-use
set-password link (24 hours) to
`/set-password`; approval creates the account only, never a group
membership. Confirming sets the password and preserves consent or suspension
decisions made since approval. Adults and active teens sign in; teens still
awaiting consent or household conversion prerequisites stay signed out
(`pendingConsent: true` signals the public consent explanation only).
Disabled accounts cannot complete setup. Rejection emails
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

For a teen request, `accountConsentEvidenceText` grants account consent in
the approval transaction. The evidence must be nonblank and at most 1,000
characters. Invalid consent rolls back the approval, user creation, audit,
and notifications. A guest can become the teen account on the same user
row. An email belonging to another existing account answers 409 without
granting consent. Without this field, approval leaves the teen waiting for
consent. Granting consent does not set a password.

`accountRequest`: `{id, name, email, phone, accountType, birthDate?,
message, source, requestedBy?, requestedByName?, groupId?, status,
hasWarnings, warnings: {duplicate, honeypot, rateLimited},
decisionNote, expiresAt, createdAt, decidedAt?}`.

### Site

```
GET /api/v1/site → {name, timezone, selfSignup, roomsEnabled, pushEnabled}   (public)
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
GET  /api/v1/groups/:id/events?from=2026-10-06&to=2027-01-06&cursor=&limit=
  → {events: [occurrence], truncated, hasMore, nextCursor}   (default: next 90 days, max 366-day window; 25 dates per page)
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
     body: any of {startsLocal, durationMinutes, location, title, capacityOverride, visibility, reason, confirm} — this date only; see Per-date visibility below
GET  /api/v1/calendar?from=&to=&tz=&mine=1&group=&deleted=1&remote=1&childcare=1
  → {occurrences: [calendarOccurrence], truncated}   (slim rows, no attendance;
     each row has only the viewer's myStatus and an ownScope presentation flag;
     mine=1 omits Can't go and unanswered outsider-public dates; unanswered
     church-wide dates are limited to 30 days; Everything keeps Can't go and
     limits unanswered public/church-wide discovery dates to 30 days, including
     for signed-out viewers; signed-out mine=1 is ignored but does not bypass
     that window; remote=1 keeps only events with remoteAvailable;
     childcare=1 keeps only events with childcareAvailable)
POST /api/v1/events/video/jitsi             → 200 {url}   (leader, host, or admin)
```

`GET /groups/:id/events` pages all visible scheduled dates within its original-date
window, with no per-series cap. Like group detail, `limit` defaults to 25
and accepts 1..100; `truncated` and `hasMore` both mean another visible date
remains, and `nextCursor` is an opaque, group-bound continuation (otherwise
`""`). Pass the cursor unchanged and repeat the same `from`/`to` on each
page to traverse a requested window; omitted bounds are recomputed on each
request. Visibility is checked before the limit and metadata, and attendee
enrichment only runs for returned dates. Invalid cursors or limits return
400 (`group_upcoming_page`). This is not a frozen snapshot.

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
Promotion runs again when a going registration lowers its guest or childcare
count, switches attendance mode (freeing a seat in its previous pool), cancels,
declines, moves to maybe, is removed, or its account is disabled. This includes
manager re-adds that reduce a count or switch modes. Editing a registration
that remains waitlisted also retries promotion using its updated request (for
example, after lowering its guest or childcare count). Increasing effective
attendance capacity (in-person or remote, including by clearing a limit or
changing a per-date override), raising `childcareCapacity`, clearing its limit,
or turning childcare off also retries promotion for future scheduled dates.
Each promotion still requires attendance seats and follows the existing
two-hour cutoff.
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

A recurring series in any feed is expanded to one standalone VEVENT per
included date. Video eligibility is therefore evaluated per date. A viewer who
has only date-level authority to an opened hidden series receives no video link,
even if that event's general reveal mode is `always`; series-authorized viewers
continue to receive links on dates allowed by DEC-70.

A series edit (`PUT /events/:id`) applies to every future date. Future
means `startsAt` is strictly after now; a date already underway stays.
Future dates without a per-date override move onto the new times and keep
their registrations (same occurrence row). A future date the new rule no
longer contains is cancelled, and people who registered going or waitlisted
receive a stored cancellation notification. A date whose time moves sends `event.changed`
(variant `time`) to going and waitlisted people; a changed event place sends
`event.changed` (variant `place`) on every future date. Raising the capacity
promotes waitlisted parties in line order on each future date whose
effective capacity grew; lowering it removes nobody. Per-date overrides,
including a location-only override, are left alone. Dates the new rule adds
are inserted. The per-date update (`PUT /events/:id/occurrences/:date`)
takes `startsLocal?`, `durationMinutes?`, `location?`, `title?`, and
`capacityOverride?` (number or null, like `capacity`); time or place changes
fan out `event.changed`, and a raised override promotes. It also accepts the
per-date visibility contract documented below; a visibility-only override does
not pin the date's time/place. Series visibility preserves customized dates by
default, with explicit `resetOverrides` and impact confirmation to clear future
visibility overrides.

Occurrences are returned in chronological order. `from`/`to` are inclusive
`YYYY-MM-DD` and at most 366 days apart. Cancelled dates are left out of
the list. The group detail's `upcomingEvents` instead use the bounded
90-day continuation described under Groups (25 dates per page by default).

### Registration

```
PUT /api/v1/events/:id/registrations   → 200 {registration, people}   (auth)
  body: {date: "YYYY-MM-DD", status?: "going"|"maybe"|"declined", guestCount?,
         attendanceMode?, childcareCount?, childcareAgesNote?,
         people?: [{personId, status, attendanceMode?, childcareRequested?}]}
POST /api/v1/events/:id/series-registrations  → 200 {eventId, scope, date?, status, results, refusals, undoToken?, undoExpiresAt?}   (auth)
  body: {scope: "all_future"|"this_and_following", date?,
         status: "going"|"maybe"|"declined", guestCount?, attendanceMode?,
         childcareCount?, childcareAgesNote?}
  The server freezes the selected occurrence IDs and their before/after states in
  an actor-bound receipt. Only the latest receipt remains live for an actor. When
  Undo is returned, `undoExpiresAt` is 10 seconds after response preparation;
  post-commit notification delivery and lock waits do not consume that interval.
  A newer series action may supersede an older receipt before its response is ready,
  in which case the older response has no Undo fields.
POST /api/v1/events/:id/series-registrations/undo  → 200 {eventId, results, refusals, capacityLoss}   (auth)
  body: {token}
  The receipt is single-use and expires after 10 seconds. Undo refuses if answers,
  authority, or date availability changed. Restoring Going uses current capacity;
  capacityLoss lists only the signed-in person's dates that could not regain a seat.
POST /api/v1/events/:id/registrations/remove  → 200 {} | 403 | 404  (leader/host)
  body: {date: "YYYY-MM-DD", registrationId, note?}
POST /api/v1/events/:id/registrations/promote → 200 {} | 403 | 404 | 409  (leader/host)
  body: {date: "YYYY-MM-DD", registrationId}
POST /api/v1/events/:id/registrations/add     → 200 {} | 403 | 404 | 409  (leader/host)
  body: {date: "YYYY-MM-DD", selectionId | userId | email | (personId + registeredBy),
         guestCount?, attendanceMode?, childcareRequested?, overCapacity?}
```

`registration`: `{userId, eventId, occurrenceId, date, status, guestCount,
waitlistPosition}`. `waitlistPosition` is null unless the person is
waitlisted. One row per (user, occurrence); PUT is idempotent. The date must
be a scheduled occurrence. The caller must be allowed to see the event;
otherwise the response is 404. Seeing an opened event does not require
seeing its group, and registering does not add a membership. A disabled
account's registration is kept and hidden from the counts.

The series endpoint is actor-only: it never accepts household people, proxy
subjects, or manager additions. `this_and_following` requires `date`; `all_future`
means every currently generated scheduled occurrence whose `startsAt` is strictly
after the transaction clock. Candidate IDs are frozen and locked in
`original_date, id` order, so dates generated later remain unanswered. Every
authorized date uses the normal registration, capacity, waitlist, promotion,
audit, and notification path. `results` contains only the caller's own before and
after registration state. A date hidden by its effective visibility is omitted;
a removed date is returned in `refusals` with `registration_removed`, and ordinary
capacity/attendance/childcare refusals use their existing stable codes. One
opened or explicitly invited date never grants series authority. The lock order
is contact gate, actor, group (when present), event, then occurrence rows. That
keeps membership and event authority stable, follows the existing group-first and
event-first mutation protocols, and retains the actor/occurrence order shared by
single-date registration. A database failure rolls back every affected date.

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

Account attendee rows may include `email` and `phone`, under the same contact
rules as roster rows below. A removed registration alone gives an event
manager no contact right. Attendee contact never comes from a household
person's account holder.

**Contact fields (M1.11 s1 backend).** In an otherwise authorized roster or
attendee response, self, active admins, and a teen's currently confirmed
guardians retain contact access. Pending, rejected and revoked guardian links
confer none. Otherwise only the presented group's leaders see unhidden adult
members' contact, and only the presented event's managers (group leaders or
church-wide hosts) see unhidden adult registrants' contact. Leadership or
hosting elsewhere confers nothing here. Teens are always hidden from those
management roles; adults can opt out using `hideContactFromLeaders`.
Unauthorized `email` and `phone` keys are absent; authorized keys are strings,
including an empty string if the person has no phone. These rights never
widen roster/attendee identity visibility. Host-management rows (including
host email) remain admin-only; public event output has no host-management
rows. Teen avatars remain absent from rosters. No directory, recipient-list
messaging, contact requests or grant-based reveal endpoint ships in s1.

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
     body: {message?} — optional, at most 500 characters, carried on the leaders' group.rejected notice and the
     audit row (DEC-96); 400 too_long, or message_has_contact when it holds an email address or phone number
POST /api/v1/admin/groups/:id/reject-deletion → 200 {}               (admin; clears the request)
DELETE /api/v1/admin/groups/:id            → 200 {}                    (admin; deletes the group)
GET  /api/v1/admin/groups                  → {groups: [groupSummary]}  (admin; all statuses)
POST /api/v1/admin/groups/:id/members      → 200 {membership}          (admin)
     body: {selectionId | userId, role: "member"|"leader", overLimit?} — one target only; role=leader adds a (co-)leader; audited;
     a full group needs overLimit (audited as membership.added_over_limit)
DELETE /api/v1/admin/groups/:id/members/:userId → 200 {departure} (admin; same confirmed body and transaction as the leader route)
GET  /api/v1/admin/users                   → {users: [user]}           (admin; each may carry emailSuppression: {reason, since})
POST /api/v1/admin/users/:id/email-suppression/clear → 200 {cleared} (admin; audited email.suppression_cleared; M3.2c)
GET  /api/v1/admin/users/:id/teen-consents  → {consents: [teenConsent], administrativelyDisabled} (admin)
POST /api/v1/admin/users/:id/teen-consents → 200 {} | 400 | 403 | 409  (admin)
     body: {scope: "account"|"sms"|"photos", status: "granted"|"revoked", evidenceText?}
GET  /api/v1/admin/users/:id/sole-leader-groups → {groups: [{id, name, status, candidates: [{id, name, admin}]}]}  (admin; DEC-36)
POST /api/v1/admin/users                    → 201 {user}                (admin)
     body: {email, name, password? (≥10 chars), role, displayName?, phone?}
     — without a password the account is `invited` and can't sign in; 409 if the email is taken (any case)
     — rechecks the acting admin's session and authority at insertion; 401 if revoked while the request was in flight; creation and its audit commit together, with no notification or email
PUT  /api/v1/admin/users/:id               → 200 {user}                (admin; profile fields, email, role, status)
     body: any of {name, displayName, email, phone, role, status: "active"|"disabled", successors}
POST /api/v1/admin/users/:id/password      → 200 {}                    (admin; also activates an invited account)
     body: {password (≥10 chars)}
DELETE /api/v1/admin/users/:id             → 200 {}                    (admin; soft delete, frees the email)
     body: {successors: {"<groupId>": <userId>}} — required for every group they lead alone
```

`emailSuppression` says Fold stopped mailing the person's address (DEC-112, DEC-113): `hard_bounce` and `manual` stop all mail, `complaint` stops optional mail while account mail still goes; with several open reasons the stronger one shows, and `since` is when the first began. Clearing ends every open suppression on the address (kept as history) and answers how many; clearing none is 200 `{cleared: 0}` with no audit row. Postmark keeps its own list, so a hard-bounced address may also need reactivating in Postmark.

Guards: cannot demote, disable or delete the last active admin, or
yourself. Changing someone's password, email, role, or status ends all of
their sessions (`session_version`). A disabled admin has no admin powers. `PUT /api/v1/groups/:id` already allows admins to edit any group.

Teen consent history is append-only. The latest row per teen and scope
wins, ordered by `at` and then `id`. An admin grant requires evidence.
Revocation may include a reason. Both write consent history, audit, and
notifications in one transaction. Sensitive notification emails name nobody
and link into Fold. Evidence appears only in the admin history and audit.
`teenConsent` is `{id, teenUserId, scope, status, grantedBy, granterRole,
evidenceText, at}`. The admin user list includes `hasTeenConsentHistory:
true` when history exists, including after the person becomes an adult.
The history remains readable then. New consent changes require a teen.

Account revocation disables the teen and ends their sessions. Their kept
memberships and registrations stop counting, and freed event seats promote
waiters. A later grant restores places only when they fit. Initial
`pending_consent` teens already count on rosters, so their first grant does
not restore or recount their places. After consent is granted, they can log
in normally; there is no restricted waiting session (DEC-85).

An administrator's suspension is separate from revoked consent. Granting
consent keeps an independently suspended account disabled. Admin **Enable**
clears that suspension. A teen becomes `active` only when the latest account
consent is granted, otherwise `pending_consent`, following DEC-69. SMS and
photo consent do not activate an account. SMS also requires the teen's own
opt-in before any texts can be sent.


Rosters (`members` in group detail, `GET /groups/:id/members`, admin rosters) include `admin: true` for church admins, so the SPA can label them and hide Remove (DEC-38).

Disabling (DEC-38): the account's memberships and registrations are kept, hidden from rosters, and left out of member counts, registration counts, and leader counts; any leadership becomes plain membership (with `membership.role_changed`, reason `account_disabled`). Disabling frees every seat the account held on future dates, promoting waitlisted parties into them. Enabling it again restores them as plain member, writing `membership.restored` per membership and `user.enabled`. Going registrations are restored when they fit; one that would overfill is deleted instead (DEC-47): `registration.not_restored` is written and the person gets an ask-a-leader notice. Waitlisted rows keep their place. In a group at its member limit the membership is dropped instead (DEC-47): `membership.not_restored` (meta `userId`, `count`, `limit`) is written and the person gets a notification ("You're not back in … yet", "Ask a leader to add you.", stored notification with a numeric ID and standard notification retention). A leader or admin can add them with `overLimit: true`. Deleting still ends memberships.

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
### Rooms (M2.1a)

Rooms are optional (PLAN 9.1). While the `rooms_enabled` setting is off, every room route answers 404, for anyone, and `GET /api/v1/site` reports `roomsEnabled: false`.

```
PUT  /api/v1/admin/settings/rooms {enabled: boolean} → 200 {roomsEnabled}   (admin; audited setting.rooms_enabled_changed)
GET  /api/v1/admin/rooms            → 200 {rooms: [room]}                 (admin; active first, then building and name)
POST /api/v1/admin/rooms            → 201 {room}                          (admin; audited room.created)
PUT  /api/v1/admin/rooms/:id        → 200 {room}                          (admin; absent fields keep their value; audited room.updated with the changed fields)
```

`room` is `{id, name, building, capacity, features, notes, active, restricted, defaultSetupMinutes, defaultTeardownMinutes}`. Bodies take the same fields (all optional on edit); a new room is always active, so `active` only matters on edit. `name` is required, at most 100 characters, and unique per building whatever the letter case (409 `room_name_taken`); `building` at most 100, `features` 500, `notes` 2000 (notes are for admins). `capacity` is a whole number from 1 to 100000, or null for no limit. Setup and teardown minutes are 0 to 240 and are copied into each booking (DEC-101). There is no delete: set `active: false`, and bookings keep their history.

### Room bookings (M2.1b)

While rooms are on (`roomsEnabled`), whoever runs an event (its group's leaders, a church-wide event's hosts, admins) books a room for one date. Every route here is 404 while rooms are off.

```
GET /api/v1/events/:id/dates/:date/rooms → 200 {rooms: [roomPickerItem], booking: roomBooking | null, suggestions}
PUT /api/v1/events/:id/dates/:date/room {roomId: number | null, overlapReason?, setupMinutes?, teardownMinutes?}
    → 200 {booking: roomBooking | null} | 403 room_restricted | 404 | 409 room_busy
```

`:date` is the occurrence's original `YYYY-MM-DD`. `roomPickerItem` is `{id, name, building, capacity, features, restricted, busy, tooSmall}`: active rooms the caller may book (restricted rooms only for admins). `busy` means another confirmed booking holds the room for this date's padded time and never says whose; `tooSmall` means the date's seat cap or going count is above the room's capacity, a warning only. `roomBooking` is `{roomId, roomName, building, status, setupMinutes, teardownMinutes, doubleBooked, tooSmall}`.

A booking copies the room's setup and teardown minutes (DEC-101); only an admin may send `setupMinutes`/`teardownMinutes` (0 to 240) to override them for this booking. `roomId: null` releases the date's room; booking another room replaces it. Confirmed bookings of one room never overlap once padded: the database refuses it, so of two concurrent bookings exactly one wins and the other gets 409 `room_busy`. An admin may book over a conflict by sending `overlapReason` (at most 500 characters); that booking is `doubleBooked`, audited as `room_booking.overlap` with the reason, and still counts as busy for everyone else. Booking and releasing are audited (`room_booking.booked`, `room_booking.released`).

Cancelling a date releases its room. Moving a date (a per-date reschedule or a series edit) moves its booking in the same transaction; if the room is taken at the new time, by a confirmed booking or an admin's double-booking, the booking becomes `requested`, holds nothing, and the event's leaders or hosts get one `room.lost` notice for that edit, the person who moved it included. A date a series edit cancelled lost its room then; a DEC-91 restore books it in the series room if there is one, and otherwise it comes back without one. A booking only an admin could make (a restricted room or a double-booking) can only be changed or released by an admin (403). Re-booking the same room keeps an admin's padding override. Bookings are written against the date as it is under its row lock, so a date cancelled or moved meanwhile answers 404 or books the new time. Event and date JSON carry `room?: {id, name, building}` for a confirmed booking, which the page shows as the place (typed location after it), and so do calendar files and feeds: the room and its building lead the ICS `LOCATION`, then any typed location.

### Series rooms (M2.1c)

```
GET  /api/v1/events/:id/rooms → 200 {rooms: [seriesRoom], dates, defaultRoom: {id, name, building} | null, needsRoom: [{date, roomName}], suggestions}
PUT  /api/v1/events/:id/room {roomId: number | null, overlapReason?}
     → 200 {defaultRoom, booked, lost: [date], kept} | 403 room_restricted | 404
POST /api/v1/rooms/check {groupId?, startsLocal, durationMinutes, tzid?, recurrenceRule?, rruleUntil?, capacity?, visibility?}
     → 200 {rooms: [seriesRoom], dates, suggestions}
```

`seriesRoom` is `{id, name, building, capacity, features, restricted, busyDates, tooSmall}`: `busyDates` counts how many of the event's scheduled future dates (`dates`) another confirmed booking holds the room on, never saying whose; the event's own bookings don't count. `tooSmall` compares the event's seat cap only.

`PUT …/room` books the room for the whole series (DEC-103): it is stored as `events.default_room_id` and booked on every scheduled future date. Dates the room is busy on keep a `requested` booking that holds nothing and are returned in `lost` and listed in `needsRoom`; an admin's `overlapReason` double-books them instead. A date booked in another room on its own keeps it (`kept`), and so does a booking only an admin could make unless an admin is booking; bookings in the old series room, and requested ones, are replaced. `roomId: null` stops the series room and releases its bookings. Audited as `room_booking.series`. Dates added later, by the daily extender or a series edit, or restored by one, are booked in the series room; a busy one is `requested` and the leaders or hosts get one `room.lost` notice. A deactivated series room books nothing new. A series room an admin chose keeps booking new dates even if it is restricted (it was an admin's choice); a leader may still change or stop it, which leaves admin-only bookings where they are. A date booked on its own in the same room as the series room counts as the series room's booking when the series room changes or stops. The daily extender adds each event's dates under the event's lock; a failure booking their rooms is logged and never stops the dates being added.

The create routes (`POST /groups/:id/events`, `POST /events`) accept `roomId` and book it for every date in the creating transaction, the same way; a room the creator can't book refuses the whole create (403 `room_restricted`, 404). `POST /rooms/check` gives the create form its busy counts: with `groupId` the caller must run that group; without it, the caller must be an admin (church-wide events).

### Room suggestions (M2.3, DEC-110)

The three room pickers above (one date, the series, and the create forms' `POST /rooms/check`) each return `suggestions: {rooms: [{roomId, uses, reason, busyDates}], otherKinds, dates}` with their room list (PLAN 9.3). `rooms` is at most 5, best first, and only rooms the same response lists. `reason` is ready to show ("Used by this group 6 times", "Used by this event once", "Used by similar groups 2 times", "Used 4 times") and `uses` is the count it names. `busyDates` counts how many of the first `dates` dates (at most 8) another confirmed booking holds the room on, with the room's padding; busy rooms come last. The pickers show their own busy note, which covers every date being booked. `otherKinds: true` means no room had been used by a group sharing a tag with this event's group, so these are labelled "Used by other kinds of events".

A usage is a confirmed booking on a scheduled date from the last 12 months or still to come. Whether it counts depends on that date's own visibility, never its group's: public and private events count public and private usages; an invite-only event also counts its own group's; an event's own dates always count. The visibility used is the date's (one date), the event's (series), or the one picked on the create form (`visibility`, else the group's, else public for a church-wide event). Restricted rooms are suggested to admins only. Suggestions name no other event, group or date. They are a shortcut, never a gate: every listed room can still be chosen, and a failed suggestion read leaves the picker working without them.

### Room booking approval (M2.1d)

```
PUT  /api/v1/admin/settings/room-approval {enabled} → 200 {needApproval}
GET  /api/v1/admin/room-requests → 200 {requests: [roomRequest]}
POST /api/v1/admin/room-requests/:id/confirm {overlapReason?} → 200 {confirmed, overlapped, lost: [date]} | 404 | 409
POST /api/v1/admin/room-requests/:id/reject {reason?} → 200 {} | 404 | 409
```

Admins only, and 404 while rooms are off. `GET /admin/rooms` also returns `needApproval`. With "Room bookings need an admin's approval" on (DEC-102), a leader's or host's booking (one date, a whole series, or a room picked on a create form) opens one request (DEC-106): its free dates become `requested` bookings that hold nothing and point at it, and every admin gets a `room.requested` task. A date the room is busy on is still refused (one date, 409) or lost (a series, DEC-103), not requested. An admin's own bookings always confirm at once. Booking JSON says `pending: true` for a date waiting on approval; the series result counts `pending` dates, and `GET /events/:id/rooms` counts them as `pending` and leaves them out of `needsRoom` (DEC-105).

`roomRequest` is `{id, eventId, eventTitle, groupName, roomId, roomName, building, series, requestedBy, dates, busyDates, firstDate, firstStartsAt, createdAt}`; `busyDates` counts its dates another confirmed booking now holds the room on. Confirming makes the free dates confirmed; a busy date is double-booked with `overlapReason`, or without one goes back to the leaders as a date that lost its room (`lost`). Rejecting makes its dates `rejected` (listed in `needsRoom` with `status: "rejected"`), and a series request stops being the series room. Either way the request's task closes, the leaders or hosts get a `room.request_decided` notice, and the decision is audited (`room_request.confirmed`, `room_request.rejected`). A request already decided answers 409 with who handled it. Re-picking, releasing or cancelling every date of a request withdraws it and closes its task. A series room a leader or host picked stays unapproved (`events.default_room_pending`) until an admin confirms a request for it: while approval is on, the daily extender and series edits put its new dates on the waiting series request, or open a new one if that was withdrawn, and never confirm them directly. Rejecting a series request stops that series room. Deleting an event withdraws its requests. Changing a date's or a series' room under approval releases the old booking at once (a date has one booking); the new room holds nothing until it is approved.

### Matching old locations (M2.1e)

```
GET  /api/v1/admin/rooms/locations → 200 {locations: [{location, dates, events}]}
POST /api/v1/admin/rooms/locations/match {location, roomId} → 200 {booked, busy: [{eventId, eventTitle, date}], busyTotal, series} | 400 | 404
```

Admins only, and 404 while rooms are off (9.1). The list counts the scheduled upcoming dates with no booking by their place as pages show it (the date's own, else the event's, else the group's), most dates first, at most 200; spellings that differ only in case or outer spaces are one place, shown in its most common spelling. Matching books the room, as the admin, on every such date whose place matches; dates the room is taken on are left alone and listed in `busy` (at most 100; `busyTotal` counts them all). A series with upcoming dates whose own place (the event's, else the group's) matches and that has no series room gets this one (M2.1c), so its later dates are booked too (`series` counts them); a series whose room was rejected or stopped can get one again this way, since this is an admin's choice. Typed locations are never changed: the place line shows the room and leaves out a typed place that only repeats the room's name. Dates that already have a booking are skipped, so matching again books nothing twice; taken dates stay unbooked, so their place stays in the list for another try. Matching compares case-insensitively in the database, on both sides. Audited as `room.location_matched`.

### Room feeds (M2.2b)

```
GET    /api/v1/admin/rooms/:id/feed → 200 {feed: {hasToken, createdAt}}
POST   /api/v1/admin/rooms/:id/feed → 200 {feed: {hasToken: true, createdAt, url}}   (audited room_feed.created)
DELETE /api/v1/admin/rooms/:id/feed → 200 {feed: {hasToken: false, createdAt: null}}  (audited room_feed.revoked)
```

Admins only, and 404 while rooms are off or for a room that doesn't exist; POST is also 404 for a deactivated room. A room has at most one working link. `POST` makes it, or a new one, which turns the old one off at once; the raw link is in that response only (32 random bytes, base64url; only the SHA-256 is stored, as for personal feeds). Turned-off links stay in `room_feed_tokens` as history. The feed itself is `GET /cal/rooms/<roomId>/<token>.ics` (ICS feeds below).

### By-room view (M2.2a)

```
GET /api/v1/calendar/rooms?view=day|week&date=YYYY-MM-DD → 200 {timezone, from, to, rooms: [roomCalendarRoom], truncated}
```

Signed-in adults only (401 signed out); teens get 404, and so does everyone while rooms are off. The window is in the church's time zone (`timezone`): one day, or the Sunday-to-Saturday week holding `date` (`view` defaults to `day`, `date` to today). `from` and `to` are the first and last day, inclusive. `roomCalendarRoom` is `{id, name, building, capacity, restricted, bookings: [roomSlot]}`, active rooms ordered by building and name; restricted rooms are listed for admins only (DEC-104). A `roomSlot` is one booking overlapping the window, by its event's own time (padding not included), in start order:

- a date the viewer can see: `{startsAt, endsAt, eventId, date, title, churchWide?, dateUrl?, groupId?, groupName?, hostedBy?}`. `dateUrl` is set when only that date is open to the viewer (the series isn't). The group appears only when the viewer may see it; an opened event of a hidden group says `hostedBy` the church. An exact-date invitation gives the title and `dateUrl`, never the group. Like the date page, an invitation stops showing a date once that date has ended, so a past invited date is Reserved.
- any other date: exactly `{startsAt, endsAt, reserved: true}`, with nothing that says whose it is.

Only confirmed bookings show. Admins also see bookings waiting for approval on a pending request, marked `waiting: true` (DEC-109); they hold nothing yet. A booking that lost its room isn't shown. At most 2000 bookings; past that, `truncated` is true.

### Background jobs: the System page (M3.1b, DEC-111)

```
GET  /api/v1/admin/jobs             → 200 {jobs: [adminJob], more}
POST /api/v1/admin/jobs/:id/retry   → 200 {status: "queued"} | 404 | 409   (audited job.retried)
POST /api/v1/admin/jobs/:id/dismiss → 200 {status: "dismissed"} | 404 | 409 (audited job.dismissed)
```

Admins only (403 otherwise). The list holds background jobs that ran out of attempts (`dead`), latest failure first, at most 100 (`more` says there are more). `adminJob` is `{id, kind, label, runsDaily, attempts, lastError, failedAt}`: `label` says what the job does in plain words (its kind when the server doesn't know it), `runsDaily` means tomorrow's run tries again by itself, and `lastError` is the job's last error, one line, without email addresses. A job's payload is never shown. Retry queues the job again from its first attempt; Dismiss sets it aside, and it is deleted with finished jobs after 30 days. Retrying or dismissing a job that is no longer dead answers 409 `job_handled`. Admins get no notice for dead jobs yet (DEC-111).

### Messages: the composer (M3.4a)

```
GET  /api/v1/messages/compose[?event=<id>] → 200 {canCompose, canMessageEveryone, smsEnabled, pushEnabled, groups: [{id, name}], events: [{id, title, groupName?, churchWide, dates: [{occurrenceId, date, startsAt}]}]}
POST /api/v1/messages/audience-preview   → 200 {summary} | 400 | 403 | 404   body: {audience}
GET  /api/v1/messages[?category=announcement|reminder] → 200 {messages: [message]} | 400   (yours; an admin's has everyone's; M3.6b)
POST /api/v1/messages                    → 201 {message} | 400 | 403 | 404   body: {channel?, subject, bodyHtml, audience, pushAlert?}
GET  /api/v1/messages/:id                → 200 {message} | 404               (its author or an admin; a reminder also its event's managers, M3.6b)
PUT  /api/v1/messages/:id                → 200 {message} | 400 | 403 | 404 | 409
POST /api/v1/messages/:id/schedule       → 200 {message, summary} | 400 | 409   body: {scheduledAt?} (absent: now)
POST /api/v1/messages/:id/cancel         → 200 {message} | 409               (audited message.cancelled)
```

`smsEnabled` shows the composer's text tab; it is always false until texting can be set up (M3.7, DEC-117). `?event=<id>` narrows the answer to that one event by the same rules (no groups; no events when the viewer may not write to it; a malformed id is 400), for the date page's "Email people going". `GET /auth/me` and `POST /auth/login` also answer `canCompose` (true for admins without reading anything), so the nav offers Messages only to people who can write (M3.4c).

Leaders, hosts and admins write announcements (`policy.CanComposeMessages`): admins to anyone, a group's leaders to their own active group and its events' registrants, a church-wide event's hosts only to that event's registrants (`policy.CanMessageAudience`, checked on every save and again at send time in M3.5). `compose` lists only what the author may target; any other target is 403 `message_not_yours`, and so is one that no longer exists, except that an admin gets 404 `message_audience_gone` (so ids can't be probed). The stored audience keeps only the fields its type uses. `audience` is a PLAN 7.2 shape: `occurrence` (one date), `event_series` (an event's upcoming dates), `events` (up to 50 events' upcoming dates), each with registrant `statuses` (`going`, `maybe`, `waitlisted`; default Going and waitlisted); `group` with `roles` (default members and leaders); `all_members` and `users` (admins only). A household person's registration reaches the account holder who registered them (DEC-8).

`summary` is `{people, eligible, teens, skipped: {reason: count}, recipients: [{name, teen, skipped?}] (first 100 by name), more}`: names and counts only, never addresses (DEC-31), with a teen count and Teen badges (DEC-20). People are skipped when they are guests (they get only transactional mail and reminders until they ask for updates, PLAN 7.4), not active, have no address, have email that isn't working or marked our email as spam (suppressions), or turned off messages from leaders (the latest `category:messages` email consent, which "Turn off all optional email" and the one-click link also write, DEC-115). The counts by reason are for everyone; each person's own reason is shown only to an admin, and a leader or host sees "won't get this". Each read goes in chunks of ids.

`message` is `{id, channel, status, subject, bodyHtml, audience, scheduledAt, sentAt, authorName, editable, updatedAt, error, failedCount, category, reminder?}` (`category`, `reminder` and `displaySubject`: M3.6b, below). `status` is `draft`, `scheduled`, `sending`, `sent`, `failed` or `cancelled`; `error` says why it failed, or why some people didn't get it (M3.5), and `failedCount` is how many people the send failed for (one count per message, read for the whole list in one query), which the list shows as "Partly sent". Only email is accepted for now (`message_channel`): texting stays hidden until an SMS provider is set up (DEC-117). `bodyHtml` is sanitized with bluemonday before it is stored (paragraphs, bold, italic, underline, strike, two heading sizes, lists, quotes, rules, http/https/mailto links and `fold-button` links; scripts, styles, `on*` attributes and `javascript:` links are removed) and a plain-text part is made from it. Scheduling needs a subject, a body and at least one eligible person (`message_nobody`), and a time from now to a year ahead (`message_when`); it is audited `message.scheduled` and stores the eligible count. Drafts and scheduled messages can be edited or cancelled. Editing a scheduled message checks it could still go out (as scheduling does) and refreshes its count; that edit, and an admin editing someone else's message, are audited `message.edited`. The body is limited to 100,000 characters of sanitized HTML and 50,000 of plain text (`message_body`); once a message is sending or sent, changes are 409 `message_locked`. Sending arrives with M3.5.

#### Merge fields, preview and test sends (M3.4b)

```
GET  /api/v1/messages/:id/preview        → 200 {preview: {subject, text}} | 404
GET  /api/v1/messages/:id/preview.html   → 200 text/html | 404              (for an iframe)
POST /api/v1/messages/:id/test           → 200 {ok} | 400 | 404 | 502
```

Merge fields (PLAN 7.3) are `{{first_name}}` and, in a message to one event's people (an `occurrence` or `event_series` audience), `{{event_title}}`, `{{event_time}}`, `{{event_link}}` and `{{manage_link}}`; a link's address can be `{{event_link}}` or `{{manage_link}}` and survives sanitizing. Saving refuses any other field (`message_merge_unknown`) and event fields for any other audience (`message_merge_event`). Values are HTML-escaped in the HTML part.

A preview or test first checks again that the viewer may write to the audience (roles and events change after a draft is saved), then renders the saved message as the viewer would get it: merge fields with the viewer's own values (their first name; the event's title, the audience's date, else its next upcoming one, else its latest, in the event's time zone, and its page for both links; M3.5 gives a guest their own manage link), the email layout (`internal/content/email.html`: one 600px column of tables with inline styles, the church name above the body and, below, "You're getting messages from leaders at …", "To stop them or change which emails you get, visit your email settings" (linking the viewer's own settings page, as the plain-text footer does), and the church's mailing address), and the plain-text part with the same footer as optional notices. `preview.html` is served with its own Content-Security-Policy (`default-src 'none'`, inline styles and https images only, `sandbox` with no scripts) and `no-store`; the dev outbox shows each stored email's HTML the same way at `/dev/outbox/<id>/html`.

`test` sends the message to the viewer's own address on the broadcast stream with its one-click unsubscribe header, subject `[Test] …`. It needs a subject and body. It is audited `message.test_sent` before sending, under a lock on the viewer's row, and those rows limit it to 10 tries an hour (a try that fails still counts) (`message_test_limit`, a 400: it is the author's own inbox, so not a DEC-52 429). An address that is suppressed answers `message_test_email`; a provider failure is 502 `message_test_failed`.

Every email adapter now sends an HTML part when there is one: SMTP as `multipart/alternative` with quoted-printable text and HTML parts, Postmark as `HtmlBody`, devoutbox in `mail_outboxes.html_body` (migration 00055).

The SMS counter (`internal/content/sms.go`, `web/src/lib/sms.ts`, one table of cases in `web/src/lib/sms-cases.json`) counts GSM-7 septets (160 per text, 153 per part, extended characters count 2) or UCS-2 UTF-16 units (70, 67 per part), never splitting an escape or surrogate pair, after adding the `<church>: ` prefix and the `Reply STOP to opt out` footer. The donation-ask check (DEC-3) looks for give, donate, tithe and offering words and for links to giving pages (a giving word in the address, or a giving service such as Tithe.ly, Pushpay or Givelify). Both wait for the SMS tab (DEC-117).

#### Sending (M3.5a)

Scheduling queues the `message.send` job for the message's time; there is no new endpoint. At that time the job:

1. Checks again that the author is active and may still write to the audience (`resolveAudience`, the same check the handlers make). If not, the message is `failed`, its `error` says why ("Pat can't write to these people anymore, so it wasn't sent."), it is audited `message.refused`, and nothing is sent.
2. Snapshots who it reaches into `message_recipients`, one row per person, each once even when they are in two chosen groups or events. People skipped for the reasons the preview shows (guest, not active, which covers disabled accounts and teens waiting for account consent, no address, email not working, marked as spam, turned off messages from leaders) get a `suppressed` row with that reason.
3. Adds guardian copies when the church setting `guardian_copies` is `true` (default off; DEC-19). Each confirmed guardian of a teen recipient gets one row with `copy_of_user_id` set, filtered like anyone else, by email only (DEC-29).
4. Marks the message `sending` with the queued count, and queues `message.deliver_batch` jobs of 50.

Each batch sends its queued rows one at a time. Each email is rendered for its reader: their first name, and for one event's people their own date and date page for `{{event_time}}` and `{{manage_link}}`. A guardian copy carries the teen's values, the subject "Copy: …", and the line "Copy: sent to Sam because your church sends guardian copies." Every email:

- is From "<author> via <church>" over the configured From address (PLAN 7.8)
- goes out with Reply-To set to the church office address in the `reply_to_address` setting, when it holds one plain address (DEC-130; the relay replaces it in M3.11)
- is on the broadcast stream, with the reader's footer and one-click unsubscribe

Right before sending, each reader's suppressions, account status and messages-from-leaders switch are read again, so someone who bounced, was disabled or opted out since the snapshot is skipped, with the same reason words the snapshot uses.

Each row is sent under a row lock (`FOR UPDATE SKIP LOCKED`) and recorded as `sent`, with the provider's id, in the same transaction, so two workers never send the same row. Delivery is at least once: a crash between the provider accepting an email and that commit can send it again on the retry. A batch stops after 10 minutes, inside the jobs lease, and its retry carries on.

How a send error is handled depends on whether a retry can help (`mail.IsPermanent`):

- **Permanent errors fail that row at once.** These are a message refused before sending, a provider refusal (`SendError.Permanent`), and an SMTP 5xx such as "no such user".
- **No email service at all fails that row at once** (M3.5b), with "No email service is set up yet, so it couldn't go out. Once an admin sets one up, try again." Retrying in the same process can't help; once a provider is set and the server restarted, Try again sends it. Before M3.5b it retried for an hour, which recovered on its own only if a provider was set and the server restarted within that hour; failing at once with plain words and Try again is the better experience (Grier, Oct 31, 2026).
- **Any other error retries the batch.** It stops and retries with the jobs backoff, 8 attempts (about an hour). On the last attempt, each such row fails with its error and the rest are still tried.
- **An error that isn't about one send fails the batch's queued rows** on the last attempt. That covers a database error and a render failure.

A `message.send` that never succeeds fails the message: "We couldn't get this ready to send. Try again to send it." (Try again prepares it again from the start, M3.5b) Its claim holds only if the message hasn't been edited or rescheduled since the job read it.

When no row is left queued, the message is `sent`, or `failed` when nobody was reached and some sends failed. Either way it carries the first error and is audited `message.sent` with the counts.

A daily `messages.reap` settles any send that stopped with no job left, such as a process that died on a last attempt. A message still scheduled an hour past its time fails. A sending one fails its queued rows ("Sending stopped before these went out.") and settles.

Postmark delivery, bounce and spam reports move a row to `delivered`, `bounced` or `complained`, as they do for notification email.

A leader or host may schedule at most 5 messages a day to one group's people (a group audience or its events) or to one church-wide event's, counted in the church's day. The check runs at scheduling and when a scheduled message is edited. A 6th is 400 `message_daily_limit`. Admins have no limit.

#### History and Try again (M3.5b)

```
GET  /api/v1/messages/:id/recipients     → 200 {counts, skippedBy, recipients, more, canRetry} | 404   (its author or an admin; a reminder also its event's managers)
POST /api/v1/messages/:id/retry          → 200 {message} | 403 message_retry_refused | 403 reminder_retry_refused | 404 | 409 message_retry_sending | 409 message_nothing_to_retry
GET  /api/v1/admin/settings/guardian-copies → 200 {on}                              (admin)
PUT  /api/v1/admin/settings/guardian-copies → 200 {on} | 400                         (admin; audited setting.guardian_copies_changed)
     body: {on}
GET  /api/v1/admin/settings/reply-to     → 200 {address}                              (admin)
PUT  /api/v1/admin/settings/reply-to     → 200 {address} | 400 reply_to_invalid       (admin; audited setting.reply_to_changed)
     body: {address} — one plain address (`validEmail`), or empty for none
```

`counts` is `{sent, delivered, bounced, complained, failed, skipped, queued, teens, guardianCopies, guardianCopiesMissed, guardianCopiesFailed}`: the people the message was for, by status (`skipped` is the `suppressed` rows, `queued` also covers deferred texts; someone who marked it as spam got it, so the page counts them as sent to), `teens` the teens it was for and didn't skip (DEC-20), and the guardian copies counted apart, with those that didn't go out (skipped, bounced or failed) and the failed ones Try again would send. `skippedBy` counts the skipped by reason, in the preview's words. `recipients` is the first 200 rows by name, `[{name, teen, status, reason?, copyOf?}]`; `more` says there are others. Guardian copies are in an admin's list only, each with `copyOf`, the teen's name (DEC-19); a leader or host never sees a guardian's name and reads "2 guardian copies sent" instead (DEC-132). Two queries read it all, whatever the size.

The privacy rule is the composer preview's. The counts, by status and by reason, are for everyone who may see the message. That includes how many marked our email as spam and the "marked our email as spam" count in `skippedBy`: by design, since the composer's preview already shows leaders counts by reason (PLAN 7.3), and a count names no one. Each person's `status` is `sent`, `delivered`, `waiting` (queued) or `missed`, which covers skipped, bounced, marked as spam and failed alike, so a leader or host can't tell one person's consent, bounce or complaint. Only an admin gets each person's `reason` ("turned off messages from leaders", "bounced: the address doesn't take email", "couldn't send: …"). No response carries an address: `message_recipients.address` never leaves the worker (DEC-31), and send errors are stored with addresses replaced by `[email]`.

A message's `error` follows DEC-52. Fold's own plain texts reach everyone as they are: the send-time refusals, "We couldn't get this ready to send. Try again to send it.", "Sending stopped before these went out." and "No email service is set up yet…". One table, `ownMessageErrors`, lists them, with M3.5a's older wording of the not-ready text ("…Schedule it again to try once more."), which stored rows still carry and which Try again also accepts; a test keeps every `why*` text in it. Any other stored error is a provider's own words, which a leader or host can't act on, so they read "The email service didn't take it. Try again in a little while. If it keeps happening, ask an admin to open this message to see what went wrong." Admins read the provider's text.

`canRetry` shows "Try again" (DEC-131): the message is `sent` or `failed` with failed rows (guardian copies included), or failed before anyone was chosen because it couldn't be got ready, and its author is active and may still write to its audience. `retry` checks the same, then in one transaction sets only the `failed` rows back to `queued` (clearing their error), the message to `sending` (clearing its error), queues `message.deliver_batch` jobs for those rows under fresh keys, and audits `message.retried` with the count. Rows that were sent, delivered, bounced or skipped are never queued again, so nobody it reached gets it twice. A message that never got ready goes back to `scheduled` for now and `message.send` prepares it from the start. Whoever presses it, an admin included, it goes out in the author's name, so the author's rights are what is checked: an author who can no longer write to these people is 403 `message_retry_refused` and nothing changes. A message that is already going out again (someone else pressed it first) is 409 `message_retry_sending`, "It's already sending again."; one with nothing to retry is 409 `message_nothing_to_retry`. A retry isn't a new message, so it doesn't count toward the daily limit of 5 (Grier, Oct 31, 2026).

The message's page shows the counts as a line ("Sent to 132 people · 3 bounced · 9 skipped (6 turned off messages from leaders, 3 have no email address)"), the guardian copies as a count ("1 guardian copy didn't go out"), a failed send's problem under the title as an alert ("This message didn't go out", or for a partly sent one "1 person didn't get this", people and guardian copies counted apart as in the list), Try again outside the alert as the view's one primary action, with a confirm in the same words ("Send again to the 1 person and 1 guardian copy it didn't reach?"), and "See who got it". While a message is sending, or due to send (including a scheduled time that arrives while the page is open), the page reads the message and its counts every 5 seconds ("Sending… 40 of 132 so far", "Sending again…" after Try again). It waits while the tab is hidden, stops when the message settles or the page closes, and after 30 minutes with no change stops and offers "Refresh to check again". A hidden status says once that it settled, not on every check.

`guardian_copies` is `true` or `false` (default off): each confirmed guardian of a teen recipient gets an email copy (DEC-19). `reply_to_address` is where replies to announcements go until the M3.11 relay (DEC-130). Both are on Admin → Settings; each change writes its audit row in the same transaction.

#### Push notification too (M3.13d2)

```
GET  /api/v1/messages/:id/read           → 200 {message: {id, subject, from, sentAt, bodyHtml}} | 404
```

An announcement always goes by email; `pushAlert` (on `POST` and `PUT /messages`, and on `message`) adds a short push for the people who have push on (DEC-123). A new message that leaves it out has it on; an update that leaves it out keeps the stored value. `channel: "push"` (any case) is 400 `message_push_only` ("an announcement always goes by email"); push is never a channel of its own. `compose` answers `pushEnabled`, the church's switch (DEC-124): off, the composer hides "Push notification too", the stored value stays, and nothing pushes.

Push follows push consent, not the email row (DEC-122). `summary` gains `push`, how many active people the audience names have push on for messages from leaders (their latest `(push, all)` consent is opted in and their `(push, category:messages)` consent isn't opted out) and a device, whether or not the email reaches them (0 with the church's switch off), and each such recipient `push: true`. Having no device isn't a skip. The composer marks them "Push too", or "Push only" for someone the email won't reach (DEC-135 B).

`message.send` snapshots one `message_recipients` row with `channel: "push"` for each active direct recipient with push on, whatever their email row says: someone skipped for an email reason (turned off email from leaders, a bounce, no address) still gets the push, and someone not active (disabled, waiting for consent) gets neither. Never a guardian copy, a guest or a household person; only with the switch and `push_alert` on; announcements only. In the same transaction it queues one `push.deliver` per device (`unique_key` `push:mr:<row>:d:<device>`, ids only, in batched INSERTs, `jobs.EnqueueMany`); a row with no device left by then is "device gone" at once. Each job checks again, in order: the church's switch ("push turned off by the church"), the account ("recipient not active"), push consent ("push not turned on", "push turned off for messages from leaders"; email consent plays no part), the device (with none left, "device gone"), that the message is still sending or sent ("no longer current"), quiet hours (the row stays queued with "outside quiet hours · sending at 8:00 AM" on the recipient's clock), and the hourly limit of 20, which counts announcement and notification pushes together ("hourly limit"). The row is `sent` once any device takes it. The lock screen shows the subject as the reader's email says it, and the first 120 characters of the plain text then "Open to read."; no link address, URL, link field, email address or phone number (DEC-124). A tap opens `/messages/:id/read`. Push rows never change a message's status, counts or `failedCount`, and Try again never sends a push again. The daily reaper stops a push row with no job left after an hour ("didn't go out").

History `counts` gains `push: {sent, waiting: [{until, n}], missed: {reason: n}}` ("Push notifications: 38 pushed · 2 waiting until 8:00 AM · 1 device gone"). The list stays one row a person: a person's row gains `push: {status, until?, reason?}` ("Push sent", "Push waiting until 8:00 AM", "Push didn't go out"), so "And N more" counts people. Leaders and hosts see each person's push status and when it waits until, as the counts say it (DEC-135 B); its reason is an admin's only.

`read` is the page a push opens (`policy.CanReadMessage`, DEC-135 A): whoever may see the message's history, and anyone an announcement was sent to, meaning a direct row (any channel, a skipped email included, never a guardian copy) on a message that is sending or sent. It answers only the subject and body with the reader's merge values, sanitized again, `from` ("Lee Leader via First Church") and when it went out; never the audience, counts or other recipients. A link to `{{event_link}}` or `{{manage_link}}` with no value for the reader keeps only its text. A recipient still gets 404 from `GET /messages/:id` and `/recipients`. Anyone else gets 404.

#### Event reminders (M3.6a)

Every event has `reminderEmailMinutes`, which says how long before each date its email reminder goes: `0` (off), `120`, `1440` (the default), `2880` or `10080` (DEC-133). It is set on create and update like the other event fields. Any other value is 400 `event_reminder_time`.

A reminder is a message the pipeline sends (`category` `reminder`, no author, `occurrenceId` set). It is never a bell row (DEC-44).

- **Scheduling.** An hourly `reminders.sweep` (queued as `reminders.sweep:<UTC hour>`) queues one `reminder.send` job at the exact reminder time. This happens for each scheduled date whose reminder falls between 6 hours ago and 2 hours ahead. A reminder missed by a few hours of downtime, or one for an event created after its reminder time, goes as soon as it can, before the date starts. The job's key names the date, its start and the reminder time. Whether anyone is going is checked when the job runs, so the first person to sign up shortly before the reminder time still gets it.
- **Stale jobs do nothing.** That covers a cancelled, moved or deleted date, a reminder time that was changed or turned off (the new time queues a new job), a date that has started, and a date with nobody of that status. A reminder already sent whose time falls inside the new reminder's lead covers the new one, so a date moved an hour later sends no second email. A date moved a week later gets a new reminder, and so does a changed reminder time: a 1-day reminder shortened to 2 hours also sends the 2-hour one.
- **Two messages per date.** The job writes the going reminder ("Reminder: Book night on Tue, Nov 3 · 7:00 PM MST") and, when the event has a waitlist, the waitlist note ("Still on the waitlist: …"). Inside 2 hours the note doesn't promise an email about a spot, since the waitlist no longer moves on its own (DEC-80). Each is written once (`reminder_key`) and goes through `message.send`; the daily reaper leaves reminders to their job.
- **Who gets it.**
  - Recipients are the date's registrants with that status (a household person's reaches the account holder). Guests are included, because reminders for their event are mail they get (PLAN 7.4).
  - Each one is checked again for whether they may still see the date: the date's effective visibility, or a live exact-date invitation. Those who can't are skipped: "can't see this date anymore".
  - Then the usual filters apply, with the **Event updates** switch (`category:events`) instead of Messages from leaders. Someone who turned it off is skipped: "turned off event updates".
  - Guardian copies follow `guardian_copies` as for any non-transactional email (DEC-19).
- **What it looks like.** A reminder is From the church name with no author, and replies go to the office address. The footer says "You're getting event updates from …".
- **What it says.** The text is the reader's own: first name, the date's title (its own when it has one), time and place, and a button to their date page, where they can change their RSVP.

#### The reminder controls (M3.6b)

- **The forms.** Every event form (create a group event, create a church-wide event, Edit series on the group and event pages) has a "Reminder email" select under More options: Off, 2 hours, 1 day (the default), 2 days or 1 week before; the closed More options says "Reminder 1 day before" or "No reminder". It sends `reminderEmailMinutes`; Edit series sends it only when changed. It is a series setting: editing one date doesn't change it. Group date rows (`occurrenceJSON`) carry the series' `reminderEmailMinutes` for that form.
- **The date page.** `GET /api/v1/events/:id/dates/:date` answers `reminders` only to the event's managers (`policy.CanManageEvent`: an admin, a leader of its group, a host of a church-wide event); for anyone else, and for an exact-date invitation read, the field is absent. It is `{minutes, dueAt, covered, messages: [{messageId, kind, status, sentAt, reached, failed, skipped}]}`: the event's reminder time (`0` off), when this date's reminder goes (`null` when off), whether a reminder already written covers that one so it won't go (the same reminder, or another sent one less than the reminder time before it: `reminderAlreadyCovers`' rule), and every reminder written for the date, oldest first, read in one query. `kind` is `going` (the reminder) or `waitlist` (the waitlist note). The people are counted as the M3.5b history counts them, without guardian copies (DEC-132): `reached` got it (sent or delivered), `failed` didn't (bounced, marked as spam, or failed), `skipped` weren't sent it. The page says "Reminder emailed to 12 people · yesterday at 7:00 PM · 1 didn't get it · 2 skipped" and "Waitlist note emailed to 3 people · …", each linking to the reminder's page; then "Reminder goes out Tue, Nov 3 at 7:00 PM" before it goes (not when `covered`), or "Reminders are off for this event". Times read on the church's clock, as message times do.
- **Who may read a reminder.** `GET /messages/:id` and `/messages/:id/recipients` let in the author, an admin, and for a reminder anyone who manages its event (`policy.CanSeeMessageHistory`, by the facts the event page uses). Anyone else gets 404. A leader or host reads the history under the leader rules: no reasons, guardian copies as a count (DEC-132), a provider's error in plain words (DEC-52).
- **Read only.** A reminder is never `editable`. Edit, schedule, cancel, preview and test answer 409 `message_reminder` for everyone, admins included (`policy.CanEditMessage`).
- **Try again.** `retry` works for a failed reminder for an admin or a manager of its event. A reminder has no author, so what is checked is the date and the event: the date still scheduled and not started, and the event's reminders still on, else 403 `reminder_retry_refused` (and `canRetry` is false). Each failed row is checked again, as the first send would (a guardian copy follows its teen): someone no longer going (for the waitlist note, no longer on the waitlist) is skipped with "no longer going" ("no longer on the waitlist"), and someone who can no longer see the date with "can't see this date anymore", instead of sent. It is audited `message.retried` with the presser.
- **Messages.** `message` gains `category` (`announcement` or `reminder`) and, for a reminder whose date still exists, `reminder: {kind, occurrenceId, eventId, eventTitle, date, startsAt, tzid}` (the date's own title when it has one, and the event's zone; one more query for the whole list). `GET /messages?category=announcement` or `?category=reminder` lists one kind, each with its own 200 (any other value is 400); without it both share the 200. An admin's Messages page has two chips, "Announcements" (the default) and "Automatic reminders", so reminders can't crowd announcements out; a reminder reads "Automatic reminder · Book night, Tue, Nov 3" (or "Automatic waitlist note"), its date in the event's zone, with its status in words. A leader's page has no chips and keeps only what they wrote. A reminder also has `displaySubject`, its subject with `{{event_title}}` and `{{event_time}}` filled in as its emails said them (the date's title, its time on the event's clock: "Reminder: Book night on Tue, Nov 3 · 7:00 PM MST"); it is "" for an announcement. A reminder's page uses it as the heading, says "Automatic reminder for Book night, Tue, Nov 3 at 7:00 PM", linking to the date, in place of the author line, and shows its body with merge fields as written.

### Notifications

Stored per-recipient rows, never a derived feed or localStorage dismissal list.
All routes require ordinary authentication; another user's row returns 404.

```
GET    /api/v1/notifications?filter=all|unread|waiting|archived&before=<cursor>&limit=20
       → 200 {waiting, waitingTotal, notifications, next}
GET    /api/v1/notifications/badge → 200 {unread, waiting} | 304 (ETag)
POST   /api/v1/notifications/seen {ids: number[]} → 200 {} (at most 100)
POST   /api/v1/notifications/read-all → 200 {}
POST   /api/v1/notifications/archive-read → 200 {}
POST   /api/v1/notifications/:id/read → 200 {notification}
DELETE /api/v1/notifications/:id/read → 200 {notification}
POST   /api/v1/notifications/:id/archive → 200 {notification}
DELETE /api/v1/notifications/:id/archive → 200 {notification}
GET    /api/v1/me/notification-settings → 200 {settings: {optionalEmailOff, kinds, push}}
PUT    /api/v1/me/notification-settings → 200 {settings}   (bodies: Email settings, Web push)
```

"Turn off all optional email" (DEC-97) appends one `consents` row (channel
`email`, scope `category:<name>`, source `web_form`) per optional category whose
state changes; repeating a value writes nothing. Optional email is every
notification email except required kinds: safety kinds (minors, consent, contact
access) and tasks waiting on the recipient. A suppressed send is recorded in
`notification_deliveries` as `suppressed` with the reason "optional email turned
off". In-app notifications are unaffected. Transactional mail (password reset,
invitations, verification) is not notification email and always sends.

A notification is `{id, kind, subjectId?, category, title, body, link, actions,
batchCount, createdAt, updatedAt, read, archived, waiting, resolution?}`. IDs are
numeric. Resolution is `{outcome, by, byMe, at}`. Feed cursors are opaque; invalid
filters/cursors return 400. `waiting` is the separate task section, with its
full count in `waitingTotal`.

The badge counts unread ordinary rows plus unresolved waiting rows without
double counting; opening the panel does not clear it. Read-all leaves waiting
tasks counted. Seen is reserved for future delivery escalation. Event expiry
archives rather than deletes. Retention is 90 days for read/archived rows and
180 for unread rows (DEC-45). Reminders do not create bell rows.

The SPA keeps the feed in memory only and refreshes the badge every 60 seconds
while visible, on focus/navigation and after actions. Logout clears it. Inline
actions use domain endpoints, checked against notification ownership, kind and
subject. Only group approval and eligible clean-adult account requests are
inline; sensitive requests use navigation and explicit destination decisions.
Resolved races return structured already-handled details. No contact data or
hidden-group metadata may enter feed text; sensitive email is names-free.

### ICS feeds (outside `/api/v1`)

```
GET /cal/public.ics        → 200 text/calendar  (public)
GET /cal/groups/<id>.ics   → 200 text/calendar | 404  (public; active public groups only)
GET /cal/u/<token>.ics     → 200 text/calendar | 404  (the token is the credential)
GET /cal/rooms/<roomId>/<token>.ics → 200 text/calendar | 404  (an admin-issued room link, M2.2b)
```

Feeds cover 30 days back to 12 months ahead, carry `ETag` and
`Cache-Control: private, max-age=900` (304 on `If-None-Match`), and
never carry attendee data. Contents follow the viewer's permissions at
fetch time; a disabled or deleted owner's token answers the same plain
404 as an unknown one. All feeds are finite snapshots of authorized scheduled
occurrences selected by their actual start in the half-open export window.
They contain no VEVENT `RRULE`, `EXDATE`, `RDATE`, or `RECURRENCE-ID`: clients
never reconstruct dates beyond the loaded authorization facts. Cancellation
omits a date on the next refresh; a reschedule keeps its original-date UID and
updates its effective clock, including moves into or out of the window.
Standalone identity and date-only privacy are described with the calendar-feed
token contract above. Timezone coverage follows the window and emitted clocks,
not the age of the series anchor. Calendar clients choose when to refresh and
may retain old cached data; no immediate remote deletion is promised.

The room feed lists the room's confirmed bookings (never ones waiting for approval, DEC-109) whose date starts in the window, at the event's own time without setup and teardown, in the church's time zone. It is titled with the room and its building. A date whose own visibility is invite-only (`policy.EffectiveVisibility`: the date's override, else the event's), or whose group was deleted, is `SUMMARY:Reserved` with only the room as its place: no description, link or event identity. Every other date has its title, its place (the room, then any typed location), its date link and, unless its series is invite-only, its description. Video links, remote and childcare details are never in a room feed. Each entry's `UID` is `room-<roomId>-booking-<bookingId>@<host>`, so it names no event. A wrong, turned-off or other room's token, rooms being off, and a deactivated room all answer the same plain 404. A deactivated room keeps its link, which works again if the room is turned back on. The access log keeps the room id and never the token.

### Email delivery reports (outside `/api/v1`, M3.2b)

```
POST /hooks/postmark/email → 200 | 400 | 401 | 404 | 413
```

Postmark posts one JSON record per request (Delivery, Bounce, SpamComplaint; others are ignored with 200). Postmark doesn't sign webhooks: the request must carry HTTP Basic credentials `postmark:<FOLD_POSTMARK_WEBHOOK_SECRET>` (at least 24 characters, or the server won't start) from the webhook URL, compared in constant time; anything else is 401 and changes nothing. With no secret set the route is 404. The route is outside CSRF (it is in `csrfExemptRoutes`). A malformed body is 400; over 1 MB, 413. A verified report is queued as a `webhook.process` job keyed by the record's own identity (record type, id, message id and time), so a redelivered request is handled once, and answered 200 at once; a failure to queue is 500, and Postmark retries. The job matches the report to `notification_deliveries.provider_message_id`: a delivery moves `sent` to `delivered`; a bounce or complaint moves it to `bounced` with `hard bounce`, `soft bounce` or `marked as spam`. A hard bounce (or an address Postmark deactivated) adds a `hard_bounce` suppression and a spam complaint a `complaint` one; a soft bounce is only recorded (DEC-112). Every send checks suppressions first: a hard bounce or a manual one stops all mail to the address, a complaint stops optional notifications and broadcasts, and the delivery is recorded `suppressed` with the reason. Postmark also keeps its own list: it refuses an address that hard-bounced until it is reactivated in Postmark, and one that complained for good, whatever Fold allows. A report that arrives before its delivery row is written is retried for up to two minutes; after that a report about mail Fold has no row for (account mail, or another sender on the same Postmark server) still suppresses the address.

### Email settings (M3.3b, DEC-115, DEC-116)

```
GET  /api/v1/email-settings/:token    → 200 {emailSettings, email, fromCategory} | 404   (no sign-in)
PUT  /api/v1/email-settings/:token    → 200 {emailSettings} | 400 | 404             (no sign-in; CSRF token as usual)
     body: {category, off} for one kind, or {off} for every kind listed
GET  /api/v1/me/notification-settings → 200 {settings: {optionalEmailOff, kinds}}
PUT  /api/v1/me/notification-settings → 200 {settings}
     body: {category, off}, or {optionalEmailOff} (DEC-97) for every kind
GET  /api/v1/admin/settings/church-address → 200 {address}                          (admin)
PUT  /api/v1/admin/settings/church-address → 200 {address} | 400 church_address_long (admin; audited setting.church_address_changed)
     body: {address} — up to 6 lines and 300 characters; empty clears it
```

`emailSettings` is `{kinds: [{category, off}], optionalEmailOff}`: the optional kinds the person can get, in order (`groups`, `events`, `messages`, and `admin` for admins only), each off when its latest email consent opts out, and `optionalEmailOff` when every listed kind is off. Account and safety email can't be turned off and isn't listed. The token is an email's unsubscribe token (M3.3a); `email` is the address masked (`s•••@example.com`) and `fromCategory` the kind the link came from. A kind the person doesn't get is 400 `email_kind_unknown`. Each change appends a `consents` row for every kind whose state changes (source `unsubscribe_link` from the token page, `web_form` from Me), with IP and user agent, and nothing for a kind already in that state. The church address is shown, line by line, at the bottom of optional email (PLAN 10.5).

### Web push (M3.13b, DEC-121 to DEC-129, DEC-134)

```
GET    /api/v1/push/config        → 200 {enabled, publicKey}
PUT    /api/v1/push/subscriptions → 200 {saved: true, device} | 200 {saved: false, message} | 400 | 403
       body: {endpoint, keys: {p256dh, auth}, expirationTime?}   (PushSubscription.toJSON())
DELETE /api/v1/push/subscriptions → 200 {}                     body: {endpoint}
POST   /api/v1/push/turn-off      → 200 {push}
POST   /api/v1/push/test          → 200 {sent: true} | 200 {sent: false, message} | 403 | 404
       body: {endpoint}
GET    /api/v1/me/notification-settings → 200 {settings: {optionalEmailOff, kinds, push}}
PUT    /api/v1/me/notification-settings → 200 {settings}
       body: {channel: "push", category, off} for a Push switch; the email shapes above are unchanged
POST   /api/v1/auth/logout        → 200 {}                     body (optional): {pushEndpoint}
```

All but logout need a signed-in, active account and the CSRF token as usual (`csrfExemptRoutes` is unchanged). A device is one browser installation; the browser names it by sending its endpoint.

- **Config.** `enabled` is the church's `push_enabled` setting (default on). `publicKey` is the VAPID public key, the `applicationServerKey`.
- **The church's switch (M3.13c2, DEC-124).** `GET /api/v1/admin/settings/push-enabled → 200 {on}` and `PUT` with `{on}` → `200 {on} | 400` (admin; audited `setting.push_enabled_changed` with `on`), shown as "Notifications on devices" in Admin → Settings. Off stops every `push.deliver` before it sends, refuses turning on and tests (403 `push_church_off`), hides the card on Me → Notifications (`/push/config` `enabled: false`) and the public directions page (`GET /site` `pushEnabled: false`); devices are kept for when it comes back on.
- **Directions page (M3.13c2).** `/notifications/setup` is a public SPA route (the in-app list stays `/notifications`): the iPhone/iPad, Android, computer and Mac Safari steps with their undo steps, this device's first, plus "already on" and "blocked" lines for this browser. With `pushEnabled` false it renders the ordinary "Page not found".
- **Turning on.** `PUT /push/subscriptions` upserts by endpoint and binds the device to the signed-in person. An endpoint another account holds is rebound to this one only when `p256dh` and `auth` match the stored row too (compared in constant time): the same browser subscription on a shared browser that changed hands, and the other account hears nothing. With other keys the row is left alone and the answer is the same as any save (DEC-52; audited with `refused`). Under the person's row lock the account is checked again against the session: revoked or disabled meanwhile is 401 and saves nothing. The endpoint must be an https URL on an allow-listed push service (`push.DefaultHosts` plus `FOLD_PUSH_HOSTS`), else 400 `push_service_unknown`; keys that aren't a P-256 key and a 16-byte secret are 400. The label is made on the server from the User-Agent ("iPhone · Home Screen app", "Chrome on Windows"), never from the client's text. When the latest `(push, all)` consent isn't `opted_in` (the first device, or the first after turning off everywhere), it appends `(push, all, opted_in, web_form)` with evidence such as "Turned on notifications for iPhone (Home Screen app)" (DEC-122). Each save is audited `push.device_on` (`deviceId`, `label`, `rebound`). At most 10 devices: the 11th replaces the least recently used. `device` is `{id, label, createdAt, lastUsedAt}`.
- **Refusals (403).** A guest `push_guest`, a teen whose consent isn't complete `push_consent_required` (`policy.PushRefusal`; neither can hold a session today, so these are a backstop), and push turned off by the church `push_church_off` (also for the test).
- **Turning off.** `DELETE /push/subscriptions` ("Turn off notifications on this device") deletes the device if it is the person's own and answers the same either way. `POST /push/turn-off` ("Turn off notifications everywhere") appends `(push, all, opted_out)` ("Turned off notifications everywhere"), which overrides every per-category row, deletes every device, and answers the push settings.
- **Test.** `POST /push/test` sends "Notifications are on. This is a test from <church>." to that device at once, through the same client and allow-list as `push.deliver` (TTL 10 minutes, high urgency), and opens Me → Notifications when tapped. An endpoint that isn't one of the person's devices is 404 `push_device_unknown`. A device the push service says is gone is deleted (`sent: false`, "This device stopped taking notifications. Turn them on again."); any other failure is `sent: false` with "The notification service didn't take the test. Try again in a few minutes.". Audited `push.test_sent`.
- **Limits (DEC-129).** 30 saves an hour per person: past that it saves nothing and answers 200 `{saved: false, message: "Try again in a few minutes."}`, so the card never shows "on" without a subscription. 10 tests an hour: past that 200 `{sent: false, message: "You just sent a few tests. Try again in a few minutes."}`. Neither is a 429 (`TestOnlyLoginAndInvitesAnswer429`): it is the person's own device and reveals nothing, so DEC-52 doesn't apply.
- **Settings.** `push` is `{on, devices, kinds}`: whether the latest `(push, all)` consent opts in, the devices newest first, and a Push switch per optional category (`groups`, `events`, `messages`, `admin` for admins: it covers leadership changes, the one optional admin kind that pushes), each off when its latest `(push, category:<cat>)` row opts out. With a device and no row, every category is on. The Push switches don't follow the email ones. Required kinds (safety, consent, contact, tasks waiting on you) always push to a device with push on and aren't listed; `{channel: "push"}` with any other category is 400 `push_kind_locked`. The signed-out email settings page has no push section.
- **Sessions.** The logout request's `{pushEndpoint}` deletes that browser's device in the same request (a separate call after logout would have no CSRF token), whichever account holds it, since the browser presenting the endpoint is the device. Disabling an account, waiting for consent, and anything that revokes every session (a password change or reset, an email or role change, deletion) delete every device of the account: a trigger on `users.session_version` and `users.status` (migration 00059). When it removed any device, the trigger queues a `push.devices_off` job in the same transaction, and the job writes the person one bell notice (`push.devices_off`, never emailed, pushed or required): "Notifications on your devices were turned off" / "Your password, email or account changed, so we turned off notifications on your phones and computers. Turn them on again in Me → Notifications.", linking to `/profile/notifications` (DEC-134 C). Turning devices off yourself (this device, everywhere, logout) writes none.
- **Privacy.** No response, log line, audit row, consent row or job carries an endpoint, `p256dh` or `auth` (`TestPushLogsKeepSecretsOut`, `TestPushSubscribeSavesBindsAndConsents`).
- **The browser side (M3.13c1).** Two public routes outside `/api/v1`. `GET /sw.js` is the service worker (`web/src/sw.ts`, built to `web/dist/sw.js`), served `Cache-Control: no-cache` with a JavaScript type, and a 404 when the bundle has none (never the SPA shell). It handles `install` (`skipWaiting`) and `activate` (`clients.claim`), so a new worker takes over at once and controls open tabs, plus `push`, `notificationclick` (same-origin links only) and `pushsubscriptionchange` (CSRF token, `/push/config`, subscribe, `PUT /push/subscriptions`); it has no `fetch` handler and caches nothing, so it can never serve a stale page. Only the card's "Turn on notifications" registers it. `GET /manifest.webmanifest` is made by the server (`application/manifest+json`, `no-cache`): `name` is `site_name`, `short_name` the same when it is 12 characters or fewer, else "Fold"; `id` `/`, `start_url` `/?source=homescreen` (the SPA sends a signed-out Home Screen app to Log in), `scope` `/`, `display` `standalone`, and the 192, 512 and 512 maskable icons under `/assets/brand/`. The CSP names `worker-src 'self'; manifest-src 'self'`.

**Delivery.** A kind with `AllowPush` gets its own short push text (`PushRender`), rendered with the bell's `Vars` when the row is written and stored as `notifications.push_title`/`push_body`. After commit, `deliverNotificationEmails` queues one `push.deliver` per device of each recipient (`push:n:<id>:d:<device>`), with no delay; the job checks the church switch, the account, consent and the per-category switch, as M3.13a built it. A lock screen is public (DEC-124): push text never carries a contact detail, teen badge, person's name, note, place, signed or video link, or `Data`, and a hidden group's name reaches it only as the bell's (the caller's `canSeeGroupIn` decision). What pushes:

| Kind | Push title | Push body |
|---|---|---|
| `event.cancelled` | Cancelled: *event* | *when* |
| `event.restored` | Back on: *event* | *when* |
| `event.changed` | Updated: *event* | The time (place, time and place) changed. *when* |
| `registration.promoted` | You're in: *event* | *when* |
| `registration.removed` | Registration removed: *event* | A leader or host removed your registration. |
| `group.approved` | *group* was approved | It's live now. |
| `group.rejected` | *group* wasn't approved | Open Fold to see more. |
| `group.pending` | A new group is waiting for approval | |
| `group.deletion` | A request to delete a group is waiting | |
| `account.requested` | A new account request is waiting | |
| `household.link_pending` | A household link is waiting to be confirmed | |
| `room.requested` | A room request is waiting for approval | |
| `group.leadership_changed` (to the other admins) | New leader in *group* / Leader change in *group* | Open Fold to see who. |
| `group.your_role` (to the person whose role changed; not when they changed it themselves) | You're now a leader of *group* / You're no longer a leader of *group* | You can manage its members and events in Fold. / Thanks for leading. You're still a member. |
| `registration.cancelled` | Your spot was cancelled: *event* | Open Fold to see why. |
| `room.lost` | Room booking lost: *event* | Pick another room for *1 date / 3 dates* in Fold. |
| every `safety` kind (contact, ask to add, date invitations, consent, turning 13 or 18, household accounts) | *the church's name* | Open Fold to review an update. |

A promotion or removal about someone in the household ("Sam is in") uses the safety line. The safety line is titled with the church's name (`site_name`) when it is sent, since it can't say what it is about (DEC-134 B). `waitlist.cutoff` doesn't push. TTL and urgency (research 2.4): a cancellation, restoration or change to a date starting within 24 hours is 6 hours, `high`; a task waiting on someone 72 hours, `low`; anything else 24 hours, `normal`. The topic is `n<notification id>`, which only collapses retries of the same notification at the push service.

### Unsubscribe links (outside `/api/v1`, M3.3a)

```
POST /u/unsubscribe/:token → 200 text "You're unsubscribed." | 404
```

Every optional email (a notification whose kind isn't required) ends with a footer saying what it is ("You're getting group updates from …"), a link to `/email-settings/<token>` (the email settings page, M3.3b) and the church's postal address when the `church_address` setting is set (PLAN 10.5). It also carries `List-Unsubscribe: <SITE_URL/u/unsubscribe/<token>>` and `List-Unsubscribe-Post: List-Unsubscribe=One-Click` (RFC 8058), written by every adapter (SMTP headers, Postmark `Headers`, the devoutbox's `list_unsubscribe` column). Required mail (account, safety and waiting tasks) never carries either.

The token signs a person and the email's kind (`category:<admin|groups|events|messages>`) with a key derived from `SESSION_SECRET`; it doesn't expire. The one-click POST is outside CSRF (in `csrfExemptRoutes`) and needs no cookie: the token is the only authority. It turns off that kind of email at once (DEC-114) by appending a `consents` row (`opted_out`, source `unsubscribe_link`, with IP, user agent and time) unless the latest row already says so. A token that doesn't verify, names a scope Fold doesn't use, or names a deleted account answers 404 and changes nothing. Tokens in these paths never reach the access log.

## Conventions

- `POST` create → `201`; `PUT` update → `200`; `DELETE` → `200 {}`.
- Auth failures → `401 {"error":"unauthorized"}`; forbidden → `403`.
- Validation failures → `400 {"error":"..."}`; missing → `404`.
- Pagination is endpoint-specific: notifications and request/history APIs have
  bounded pages/cursors; ordinary group lists remain unpaginated.
- Every response carries the app's security headers (`securityHeaders` in
  `main.go`, DEC-37): a `Content-Security-Policy` allowing only same-origin
  scripts, styles, and connections (images may also be `data:`, `blob:`, or
  `https:`), `X-Content-Type-Options: nosniff`, `Referrer-Policy:
  same-origin`, `X-Frame-Options: SAMEORIGIN`, and `Permissions-Policy:
  camera=(), microphone=(), geolocation=()`. `Strict-Transport-Security` is
  added when `COOKIE_SECURE` is on.

### Teen age and login boundaries (M1.10, DEC-85)

Initial setup requires `age_band=adult` to confirm the first administrator is 18 or older. Admin `POST /admin/users` additionally requires `ageBand` and accepts `birthDate` as on ordinary invitation acceptance. Under-13 or inconsistent ages create no account. Teen accounts cannot be admins, and always start `pending_consent`, even when the admin supplies a password. Adults keep the active-with-password or invited-without-password behavior.

A pending-consent teen cannot establish an authenticated session (DEC-85). Correct credentials answer `403 consent_required` with a consent explanation; wrong credentials keep the usual `401 bad_credentials` response. Password setup, password reset and invitation acceptance never log a pending teen in. Any existing pending cookie is treated as anonymous: public site, group, event and calendar reads work normally, while account-only routes answer `401`. The public consent explanation stays inside the normal church-branded shell and links to public events and login; it does not intercept other routes. Responses use `Cache-Control: no-store`. After account consent activates an approved teen account, they can log in. A staged household conversion also needs its confirmed household guardian relationship before activation and transfer. Age checks, guest-registration eligibility, private-data access and guardian authority are unchanged.

### Church-local age rollover

The birthday job runs at startup and each church-local midnight. The next
midnight uses the church's IANA time zone, including daylight saving changes.
A February 29 birthday reaches the next whole year on March 1 in a non-leap
year, matching the shared age gate.

At 18, a teen becomes an adult and retains all consent and guardian history.
Pending accounts with a password become active. Passwordless accounts still
need setup. An independent administrator suspension remains in force. A
consent-disabled account restores memberships and registrations through the
existing capacity checks, with an ask-a-leader notice for places that cannot
fit. Already listed accounts keep their places without restoration.

An existing household account's `teen` relation becomes `adult`, which allows
the new adult to manage the household. This grants no confirmed guardianship.
Pending links to the former teen account are rejected as no longer needed,
their admin tasks close as withdrawn, and system audit records retain the
reason `turned_18`. Confirmed links remain unchanged.

At 18, an approved household conversion can finish without teen consent or
guardian requirements. While independently suspended, its original household
person becomes an adult household person and keeps all household-owned
registrations until admin Enable completes the transfer. The automatic kind
and relation changes are audited.

An inactive original registration holder still prevents transfer. The entire
person's rollover transaction rolls back, including the account-type change,
preserving the staged account,
guardian tasks, and registrations. Other birthdays and child-age notices
continue. Startup and the next midnight retry after the holder is enabled.
The job logs the affected user ID and the existing unavailable-holder error.
This refusal prevents hidden seats from becoming active without capacity
checks.

Confirmed, active guardians receive one notice when a household child is 13
or older. The notice asks them to review the birth date and age group. The
person keeps their kind and sign-ups until the household edits them. No login
is created. A permanent audit record prevents duplicate notices after bell
notification retention runs. Catch-up notices also use this age-neutral
guidance for older child records.

Group rosters omit teen names and IDs for ordinary viewers while retaining the total member count. Group leaders and admins receive `member.accountType: "teen"` and a Teen badge. Event leaders, hosts, and admins receive minor identities and Teen/Child badges. Other ordinary attendees receive counts only for minors. Existing household adults retain access to their own household attendees under DEC-82; this does not grant a teen account badge. No roster response exposes minor contact details or a teen's public avatar.


For staged teen conversion, `approved_user_id` is the authoritative account association; changing an email never changes it. `converted_user_id` and the household-account row are set only when the transfer completes. User locks are acquired in ID order before the household and affected occurrences. Consent grants, teen-account guardian confirmation, and admin enable all run the same completion operation when the last prerequisite is ready. At eighteen an unsuspended account completes as an adult without teen-consent or guardian requirements; a suspended account stays with its household until enabled.

### Direct-contact requests and grants (M1.11 s2 backend)

These authenticated endpoints use the normal cookie session, CSRF header for
POSTs, and `Cache-Control: no-store`. They do not grant membership, registration,
series visibility, or permission to add a person. Ask to add and one-date access
use the separate purpose-safe group/occurrence APIs documented below. GET never approves or reveals a contact field.

```
POST /api/v1/contact-requests
  {subjectUserId, contextType: "group"|"event", contextId,
   fields: "email"|"phone"|"both", reason?: string}
  → 201 {request: ContactRequest}
GET /api/v1/contact-requests?before=<requestId>
  → 200 {requests: ContactRequest[]}
GET /api/v1/contact-requests/:id
  → 200 {request: ContactRequest} | 404
POST /api/v1/contact-requests/:id/decision
  {party: "subject"|"admin", decision: "approved"|"denied"}
  → 200 {request: ContactRequest}
POST /api/v1/contact-requests/:id/withdraw
  → 200 {}
GET /api/v1/contact-grants?before=<grantId>
  → 200 {grants: ContactGrant[]}
POST /api/v1/contact-grants/:id/revoke
  → 200 {}
POST /api/v1/contact-grants/:id/reveal
  {subjectUserId, contextType: "group"|"event", contextId,
   fields: "email"|"phone"|"both"}
  → 200 ContactReveal
```

Lists return at most 100 rows, newest ID first; `before` is an exclusive numeric
cursor. Requester, subject and current adult admin can review request history.
A currently confirmed active adult guardian can see only active teen-approved
grants (not the pending request or ended grant history), and can revoke, never
approve. Requester/subject/admin can also review their authorized ended grants.
`contextId` is omitted if the viewer cannot currently see that context. No group
name, place, contact value, birth date or avatar is included in these DTOs.

`ContactRequest` (all fields required except those marked `?`):

```
id: number
requesterId: number
requesterName: string
subjectUserId: number
subjectName: string
contextType: "group" | "event"
contextId?: number
fields: "email" | "phone" | "both"
reason: string
needsAdmin: boolean
subjectDecision: "pending" | "approved" | "denied"
subjectDecidedBy?: number
subjectDecidedAt?: string
adminDecision: "pending" | "approved" | "denied" | "not_needed"
adminDecidedBy?: number
adminDecidedAt?: string
status: "pending" | "granted" | "denied" | "withdrawn" | "expired" | "ended"
createdAt: string
expiresAt: string
completedAt?: string
endReason: "" | "left_role" | "left_context" | "context_ended" |
           "turned_18" | "account_disabled" | "event_ended"
canSubjectDecide: boolean
canAdminDecide: boolean
canWithdraw: boolean
```

`ContactGrant`:

```
id: number
requestId: number
requesterId: number
requesterName: string
subjectUserId: number
subjectName: string
contextType: "group" | "event"
contextId?: number
fields: "email" | "phone" | "both"
needsAdmin: boolean
grantedAt: string
endedAt?: string
endReason: "" | "revoked_by_subject" | "revoked_by_admin" |
           "revoked_by_guardian" | "left_role" | "left_context" |
           "context_ended" | "turned_18" | "account_disabled" | "event_ended"
endedBy?: number
canReveal: boolean
canRevoke: boolean
```

`ContactReveal` is `{email?: string, phone?: string}`. A successful reveal
contains exactly the selected authorized fields, including an empty string if
that authorized value is empty; an unselected field is omitted, not null. The
requester must echo the exact subject/context and a field selection within the
grant. A `both` grant can reveal either field separately. Even admins do not use
someone else's grant. Every successful reveal commits `contact.reveal` before
writing the response. Grants never make roster/list contact fields visible;
ordinary self/admin/guardian contact presentation remains independent.

An optional reason is at most **1000 Unicode characters**, validated before
trimming; blank means no reason. Reasons stay on authorized request reviews,
never in audit metadata, notification text/data or email. There are at most ten
pending requests per requester, serialized transactionally. Expired/invalid
pending requests are closed before enforcing the cap. The fixed expiry is 14
24-hour days from creation; exact expiry is too late. An adult subject decides
alone. Teen requests need independent subject and admin approvals in either
order; an admin requester cannot skip the teen. Either denial terminates.

Rejected contact operations commit a `contact.refused` audit even though no
business change succeeds. A failed audit fails closed and returns 500 with no
contact fields. Common actionable error codes (no 429):

- `contact_reason_long` (400): shorten the reason to 1000 characters.
- `request_open_limit` (409): withdraw an open request or wait for a decision.
  The ten-open cap includes contact and add requests.
- `contact_unavailable` (403): refresh the request; it may have ended, changed or
  no longer be available to you. Unauthorized request reviews answer 404.

Mutations end pending requests (`status=ended` with a reason) and active grants
inside the same transaction when role, current subject relationship, context or
either active account is lost. This includes group leadership changes/handoffs,
host removal, registration pruning, visibility cancellations, group/event
removal, admin disable/deletion and teen-consent disable. A successful request
stays historically `granted` after its grant ends. Regaining a role, re-enabling
an account or rejoining a context never clears an end. Actual church-local
adulthood ends teen-approved access even if staged DEC-83 account conversion is
still waiting; it never becomes an adult approval automatically. Event grants
end at the exact final stored occurrence end plus 30 24-hour days. Current
eligibility is checked again under the gate before decision/reveal. The existing
startup/daily scheduler runs an idempotent advisory-locked expiry/end sweep;
the existing birthday scheduler also ends actual-age access independently of
staged conversion rollback.

Audit actions are `contact.requested`, `contact.decision` (party and outcome),
`contact.withdrawn`, `contact.expired`, `contact.granted`, `contact.reveal`,
`contact.refused`, `contact.revoked` and `contact.ended`. Metadata holds numeric
party/context IDs, validated context/field vocabulary and machine outcomes,
never contact values or the free-text reason. These retained rows and outcome
records survive live account/context deletion. Admin CSV export is not part of
this backend component.

Notifications are navigation-only, unbatched safety kinds:
`contact.subject_review`, `contact.admin_review` (separate waiting tasks), and
`contact.updated`. Each party's decision resolves only that party's task; a
terminal outcome resolves both. Confirmed active guardians get grant/end
updates for teen-approved grants. Email names nobody and links to the same
sign-in-protected `/contact-requests/:id` review path; a guardian opens that path
using active grant facts, not permission to read the request reason/decisions.
The client must use the grants endpoint for the guardian view. Email links
never approve or reveal by themselves.

**Lock protocol:** one transaction-scoped PostgreSQL advisory gate serializes
contact operations. Contact-affecting domain transactions acquire it at entry,
before their existing user-first or group-first locks and before notification
writes. New contact operations/sweeps acquire it before loading current facts
and locking outcome rows. Lifecycle cleanup is a tail step in the original
mutation transaction, not a nested transaction. The gate remains held through
audit, task resolution, notification inserts and commit. This intentionally
coarse protocol avoids inverting the existing domain prefixes. Ordinary RSVP
answers (`going`, `maybe`, `declined`) retain an actual registration relationship
and do not take the contact gate; registration pruning/cancellation does.

Pre-domain refusals on the exact POST routes above are also audited once:
CSRF failure and missing/invalid authentication (including legacy pending-consent
cookies, which are treated as signed out). The existing abort sites call a narrow hook **before** writing their
401/403 response. No CSRF exemption or auth bypass is added. These rows use
`target_type=contact_endpoint`, no claimed target ID, and only generic reason,
HTTP method and route template metadata. The actor is nil unless established
by the trusted session. Body, query, token and claimed URL IDs are never parsed
or retained by this hook. Its audit failure returns generic 500 without running
the handler or disclosing fields. Ordinary list omissions, unrelated endpoints
and generic auth failures elsewhere do not create contact audit rows.

### Name picker (M1.11, DEC-78)

`GET /api/v1/people/search?q=` requires an authenticated active adult admin,
current group leader, or host of an undeleted church-wide event. Ordinary
members and teens cannot search. Search matches name/display name only, with
literal SQL wildcard characters, not email. Active teens and contact-hidden
adults are included; guests, invited, disabled, deleted and pending-consent
accounts are not. No per-keystroke audit is written.

Response: `{state, people}`. `state` is `short_query` (fewer than two Unicode
characters after trimming), `throttled`, `no_results`, or `results`. `people` is
always an array, at most ten results. Queries longer than 100 characters return
`no_results`. Throttling is 30 qualifying searches per actor per minute, persisted
in the existing `login_throttles` table with a separate key namespace. Throttling
is a successful **200** response with an empty array, not a 429 error.

Each result has exactly `selectionId` (opaque string), `displayName` (public-name
fallback), `teen` (boolean), `sharedGroups` and `otherGroups` (arrays of labels).
Adults with a profile image additionally have `avatarUrl`, `avatarFocalX`,
`avatarFocalY`. Teens never have any of these image fields. There are no user IDs,
email, phone, birth date or group IDs. At most ten visible group labels per
person are returned, shared groups first, then other groups, ordered by name.
Labels use current group visibility policy; hidden invite-only groups never
appear. Related labels are loaded in one bounded batch, not per result. Search database
work shares a two-second request deadline.

`selectionId` uses authenticated encryption with a domain-separated key derived
from `SESSION_SECRET`, expires after ten minutes, and is bound to the searching
actor and ordinary add purpose. It is not authority, a contact grant, or an event
visibility override. The add rechecks current actor/subject and scope facts.
Malformed, expired, wrong-actor or unavailable selections fail with 400 code
`people_selection_unavailable`; the response does not explain account state.

Existing add endpoints accept `selectionId` in place of the legacy target:

- `POST /groups/:id/members`: exactly one of `selectionId`, `email`; retain
  `overLimit` and existing group management/capacity rules.
- `POST /admin/groups/:id/members`: exactly one of `selectionId`, `userId`;
  retain `role`, `overLimit` and admin authorization.
- `POST /events/:id/registrations/add`: `selectionId` instead of `userId`,
  `email` or `personId`; retain `date`, attendance, childcare, guests and explicit
  `overCapacity`. Ordinary selection rechecks adult eligibility. Explicit exact-date invitations
  use the separate one-date contract below, never a series-visibility override.

Actual manager-initiated ordinary membership additions write `membership.added`
in the locked membership transaction (including email/admin siblings). Existing
leadership/over-limit and registration audit rows remain; no-op adds do not log a
new addition. Audit failure rolls back the membership change.

Every ordinary direct manager-initiated teen account add through these
endpoints (including email, raw-ID and admin paths) fails closed with 409 code
`teen_add_approval_required`. Use a separate Ask to add; direct endpoints do not
auto-create asks. Teen self-RSVP, household, proxy, consent conversion and
invitation flows retain their own rules.

Picker copy (client-owned):

- short query: “Type at least 2 letters”
- throttled: “Too many searches. Try again in a minute.”
- no results: “No one found. An admin can add people who aren't listed.”
- indistinguishable same names: “More than one match? Ask an admin.”

The UI must omit teen direct-add, use an accessible debounced combobox, announce
result count and discard stale responses. Group Ask-to-add has the API below;
occurrence Ask-to-add and its UI use the final s4 Add contract further below.
New error-copy entries: `people_selection_unavailable`: “That selection is no
longer available. Search for the person again.”; `teen_add_approval_required`:
“Adding a teen needs their approval and an admin's approval. Ask your church
office for help.”

### Group Ask to add (M1.11 s4)

All routes require the normal current cookie session, CSRF on POST and no-store.
This section describes `group_add`; the same routes also dispatch
`occurrence_add` as documented below, never contact authority.
The contact endpoints above now explicitly filter persisted `purpose=contact`
before adaptation, decisions, withdrawal, grants and reveals. These add rows
never appear in a contact DTO, contact reason, contact grant or contact lifecycle.

```
POST /api/v1/add-requests
  {selectionId: string, contextType: "group", contextId: number,
   reason?: string, overLimit?: boolean}
  → 201 {request: AddRequest}
GET /api/v1/add-requests?before=<requestId>
  → 200 {requests: AddRequest[]}
GET /api/v1/add-requests/:id
  → 200 {request: AddRequest} | 404
POST /api/v1/add-requests/:id/decision
  {party: "subject"|"admin", decision: "approved"|"denied"}
  → 200 {request: AddRequest}
POST /api/v1/add-requests/:id/withdraw
  → 200 {request: AddRequest}
POST /api/v1/add-requests/:id/complete
  → 200 {request: AddRequest}
```

Creation accepts only the opaque, actor-bound, unexpired search `selectionId`,
not a target email or raw subject ID. It reloads current requester and subject,
current age, active account and group, and group management authority under the
shared transaction gate. A teen **always** needs their own approval and an adult
admin's approval, including when the requester is an admin. Guardians never
approve for the teen. The typed policy also handles subject-only adult asks when
ordinary addition is unavailable; currently all active eligible adult group
accounts use the existing direct-add route, not an unnecessary ask. Contact
hiding is not a membership restriction. Occurrence creation/finalization and
the out-of-visibility date contract are documented separately below.

The optional reason is limited to 1000 Unicode characters before trimming. The
explicit `overLimit` choice defaults false, is immutable and never inferred from
approval. Ten open requests per requester **across all purposes** and a fixed
14-day expiry are serialized by the shared gate.

Group-purpose `AddRequest` projection (optional fields marked `?`; the
occurrence-purpose additions are documented below):

```
id: number
purpose: "group_add"
requesterName?: string
subjectName?: string
contextId?: number
groupName?: string
reason: string
overLimit: boolean
needsAdmin: boolean
subjectDecision: "pending"|"approved"|"denied"
adminDecision: "pending"|"approved"|"denied"|"not_needed"
status: "pending"|"granted"|"denied"|"withdrawn"|"expired"|"ended"
createdAt: string
expiresAt: string
completedAt?: string
endReason: string
canSubjectDecide: boolean
canAdminDecide: boolean
canWithdraw: boolean
canComplete: boolean
```

Lists return at most 100 rows in descending ID order with an exclusive `before`
cursor. Only the requester, subject and current active adult admins can review or
retry; a guardian alone has no request-history access. Group identity and both
preview names are omitted unless the viewer may already see the group. There
are no raw person IDs, membership IDs, email, phone, fields, avatars, birth dates,
rosters or locations in this DTO. Reviewing or approving a pending ask grants no
ordinary group read/roster permission. Free-text reason stays within this
purpose-specific review, never contact review, audit metadata or notices.

Either party declining ends the request. Each valid decision is audited and its
own waiting task resolved in the same commit. When all approvals are present,
completion calls the original group-first locked membership helper within that
same transaction. The membership retains an existing role; otherwise it is added
as member. `granted` is written only after the live active membership effect,
with `completed_target_id` and `completed_at` committed atomically. No
`ContactGrant` is created. Ordinary self-join, consent and household flows are
unchanged; email/raw-ID/admin/direct selection teen-add paths stay closed.

**Capacity is not denial:** when the group filled after the ask, a valid final
approval returns **200, `pending`, both approvals retained**, no completion time
or membership. `canComplete=true` means completion is authorized, not that room
is available. Display that the approvals are recorded but the group needs space;
retry `/complete` after space is available. A retry never repeats either party's
decision audit or extends expiry. An explicit original `overLimit=true` follows
the existing over-limit audit path. All completion attempts refresh current
actor/session, requester/subject eligibility, age, authority, context and expiry;
no stale approval or terminal request can be replayed.

Role loss, account disable/deletion, adulthood and context loss end pending group
asks in the affecting gated domain transaction; expiry uses the daily sweep and
mutation reconciliation. Repeated sweeps do not repeat terminal audit/notices.
Historic `granted` asks stay granted after later membership removal or role loss.
Contact grants retain their existing, separate automatic-end rules. The sweep
now dispatches occurrence-add rows through their own current-facts lifecycle;
it never guesses from contact registration or last-date facts. Occurrence create,
review, decision and completion use the separate occurrence-purpose checks below.

Audit actions: `add.requested`, `add.decision` (including either denial),
`add.withdrawn`, `add.expired`, `add.ended`, `add.completed`, `add.refused`;
`target_type=add_request`. Current group/member IDs and outcomes are audited,
never contact values or free-text reason. A capacity refusal is audited as
`group_full` while the valid decision remains committed. CSRF/session refusals
use `add.refused`, `target_type=add_endpoint` and no claimed IDs/body. Audit or
notification failure rolls back decisions, membership and task changes together.

Notices: `add.subject_review` and `add.admin_review` are **separate waiting
kinds**; `add.updated` is not waiting. Terminal transitions resolve both task
kinds; approvals resolve only the deciding party's task. All are safety-category,
unbatched, navigation-only (`/add-requests/:id`) and names/contact/reason-free in
both feed and email. Confirmed active adult guardians receive a safe completion
notice for a teen, not pending-request approval authority.

Actionable errors (no 429):

- `add_request_unavailable` (403): “This ask to add is no longer available to you.
  Refresh the page to see its current status.” Invalid/wrong-purpose selections,
  current authority loss, invalid/replayed decisions and terminal retries fail
  uniformly, without explaining account state.
- `add_reason_long` (400): “Your reason is too long. Use 1000 characters or fewer,
  then try again.”
- `request_open_limit` (409): “You have 10 open requests. Withdraw one or wait for
  a decision before sending another.”

Profile's Add request history opens the purpose-aware review UI; group and
single-date Ask controls use the shared name picker and separate add client.

### Exact-date invitation reads (M1.11 s4)

`GET /api/v1/events/:id/dates/:date` permits ordinary effective-date visibility
(including signed-out public dates in M1.13), or a current authenticated
account with a live exact-date invitation.
`:date` is the occurrence's original `YYYY-MM-DD` key, not its rescheduled
start day. It returns `{occurrence: EventDate}` for ordinary event visibility
or a current explicit invitation to **that stable occurrence only**. Missing,
invalid, cancelled, deleted, or unauthorized dates return 404; signed-out
requests for nonpublic dates return 404. GET has no side effects and does not approve an ask.
Pending asks and ordinary registration rows alone confer no extra visibility.

`GET /api/v1/events/:id/dates/:date/calendar.ics` (DEC-93) downloads that date
as a one-event iCalendar file (`text/calendar`, `Content-Disposition:
attachment; filename="event-YYYY-MM-DD.ics"`). It uses the same load as the
JSON read above, so the same requests get 404 (JSON) and the file carries only
the date's own title, place, times, remote line, exact-date URL and, when the
JSON would show it, the video link. `STATUS` is `CONFIRMED` for your Going,
`TENTATIVE` for Maybe or Waitlisted, and absent otherwise. The `UID` matches
the feeds', so a calendar that also subscribes keeps one copy. No calendar
name, description, hosts or attendees.

`EventDate` has exactly these fields (optional keys marked `?`):

```text
id, eventId, date, dateUrl, title, location,
startsAt, endsAt, startsLocal, endsLocal, tzid, durationMinutes,
remoteAvailable, guestsAllowed,
myRegistration, myAttendanceMode?, myGuestCount?,
videoProvider, videoUrl?, videoHidden,
canSeeVideo, canSeeAttendees, canDecline, canBlock,
visibility?, hostedBy?, groupId?, groupName?, canRegister?
```

Times/title/place are effective for that date. Only ordinary-visibility reads
include `visibility` and organizer presentation: `groupId`/`groupName` when the
viewer may independently see the group, otherwise `hostedBy` with the church
name (including the privacy-safe fallback for an opened hidden-group date).
This does not authorize the parent series. Invitation-only responses omit all
of these organizer/ordinary-visibility keys and never fall back to a hidden
group's place. Neither response includes host identities, attendee/contact
details, counts, capacities, series title, series start, RRULE/until, sibling
dates, or management controls. `myRegistration`
is the viewer's own status or null. Video is gated by that date's own RSVP and
current policy; an invitation does not inherit manager video authority.
`canSeeAttendees` is always false for invitation-only access, and this endpoint
never includes attendees even for ordinary viewers. `canDecline`/`canBlock`
describe current recipient invitation policy for the mutations below. Ordinary
voluntary RSVP continues to use its existing authority.

Calendar rows for invitation-only dates retain the slim `CalendarOccurrence`
shape, with `dateUrl` set to `/events/:id/dates/:date`, the recipient's own Going
status, and no hidden group/host fields or attendance counts. They appear in
Just mine. Group filters still require visibility of the named group and cannot
be bypassed by an invitation. Remote/childcare/window filters still apply.
Personal feeds, including `scope=all`, export such dates as standalone VEVENTs
with an absolute exact-date URL: never RRULE, sibling EXDATEs, series metadata,
or a bare series URL. Public and group feeds remain ordinary-visibility only.
The UI owner must follow `dateUrl` for scoped rows rather than synthesizing a
parent-event link. The exact-date SPA page uses this sanitized contract.

`CanSeeEvent`, `loadVisibleEvent`, ordinary `/events` discovery, and rich
occurrence lists retain their existing series-only semantics. Explicit
invitation parent event IDs are never added to their visible event sets. Scoped
access is reachable through exact-date/calendar/personal-feed/notice links,
not an expansion of ordinary discovery. Group pages/rosters,
sibling dates, series detail, contact and management remain separately gated.

Invitation reads reload current account/teen account consent, active invitation,
matching registration provenance, scheduled occurrence, live event and active
organizing group. Disable/consent loss, cancellation/deletion, removal/revocation,
or organizer mismatch fails closed before cleanup. Rescheduling the same
occurrence preserves its original exact-date link; a new occurrence never
copies access. Accepted invitations do not depend on the issuer retaining a
role, and a new organizer block does not revoke other accepted dates. Mutation/lifecycle hooks persist irreversible ends on disable, consent loss,
cancellation and deletion: a read alone cannot detect a historical disable
followed by re-enable if no end was recorded.
Downloaded calendar data cannot be recalled from another application.

### Occurrence recipient controls and lifecycle (M1.11 s4)

Occurrence Ask creation/review/completion and ordinary adult manager-add
provenance are enabled as described in the final Add contract below.

Authenticated exact-date mutations use normal CSRF and API no-store:

- `POST /events/:id/dates/:date/decline`: no body. Ends your selected invitation
  and declines its registration, releasing/promoting seats.
- `POST /events/:id/dates/:date/block`: no body. Also blocks future manager
  account adds by the organizing group, or that event when church-wide. Hidden
  organizer IDs are never inputs or outputs. Conflicting pending occurrence asks
  end and their tasks resolve; other accepted dates and memberships stay intact.
- `PUT /events/:id/dates/:date/registration`: `{status, guestCount?,
  attendanceMode?, childcareCount?, childcareAgesNote?}`. Status is going,
  maybe or declined; omitted options preserve your registration. No household,
  proxy, other-person or management options are accepted. Declined ends access
  immediately. Maybe retains only this date and uses per-date video policy.
  Going uses original capacity/childcare/waitlist rules, never an override.

POST success is `{}`. PUT success is `{registration: Registration}`; no hidden
organizer, host, series or contact data. Invalid, ended, unrelated, stale-session
or currently ineligible invitations return opaque 404. Existing capacity and
childcare errors retain their codes. Recipient refusals are audited, including
rolled-back domain errors; an audit failure fails closed. Replay cannot reopen
retained access.

Recipient mutations take the contact gate FIRST, then account/occurrence locks,
reloading current session, consent, exact provenance and live context through
that transaction. Current-manager removal uses `registrations/remove` with fresh
session/management checks. Explicit invitation removal ends access and releases/
promotes seats; its notice and audit omit private management notes. Ordinary
legacy pruning keeps its existing seat reservation. Legacy account-add selection,
email and ID siblings honor blocks and retained endings; eligible direct adult
adds mint explicit provenance as described below.

Ordinary RSVP detects active provenance in its existing registration row query.
If present, it rolls back before any writes and retries with the gate FIRST;
unrelated ordinary/household RSVP takes no extra query or gate. Ordinary decline
therefore immediately ends the invitation too. Independently eligible voluntary
RSVP remains possible, but never writes or reopens invitation access.

Visibility previews and committed cancellations share the same current explicit
invitation test. A live invitation preserves exactly its date while ordinary-
ineligible siblings are cancelled. Pending requests, ended/invalid-consent or
cancelled/deleted/mismatched provenance cannot preserve dates. Preview fact
loading errors fail closed, not as a zero impact.

Shared gated lifecycle/sweep dispatch separately handles pending occurrence asks
and accepted access. Disable/deletion/consent loss, date/event/group deletion or
cancellation, and registration cancellation/removal persist irreversible endings.
Re-enable, consent regrant and restoring dates never revive access. Issuer role
loss ends pending asks, not accepted attendance; later blocks preserve other
accepted dates; stable occurrence rescheduling preserves access.

`occurrence_access.ended` audit retains subject/event/occurrence IDs and outcome,
not contact values or internal end reasons. Navigation-only, unbatched
`occurrence.updated` notices use exact-date links without personal, hidden
organizer or series values, and names-free email. Deleted dates receive no new
invalid navigation notice. Effects, audit, tasks and notices commit together;
mail runs only after commit.

### Final s4 Add contract

Occurrence requests are enabled on the same authenticated `/add-requests`
routes as group asks. M1.11 was accepted and merged in #122; historical evidence
is in [M1.11-VERIFICATION.md](plan/M1.11-VERIFICATION.md).

`POST /add-requests` takes an actor-bound `selectionId`, `contextType` (`group`
or `event`), `contextId`, optional `reason` (1000 characters). Group accepts
`overLimit`. Event requires exact stable `occurrenceId` and accepts
`overCapacity`, `attendanceMode`, `guestCount`, `childcareCount`,
`childcareAgesNote`; omitted options resolve from the current registration, or
in-person/zero for a new registration. Resolved final options are immutable.
Raw person IDs/emails cannot replace the selection on Ask creation.

Existing `GET /add-requests[/:id]`, `POST /:id/decision` with
`{party:"subject"|"admin",decision:"approved"|"denied"}`, `POST /:id/withdraw`
and `POST /:id/complete` now dispatch both Add purposes, never contact authority.
Lists retain exclusive `before` pagination and limit 100. Actors are requester,
subject or current adult admin; a guardian is not an approver. Shared cap remains
10 pending requests across all purposes and expiry remains 14 days.

Every teen requires both subject and admin decisions, including admin requesters.
Adults need subject approval when ordinarily ineligible or after a retained end.
Direct selection/email/raw-ID siblings return 409 `date_add_approval_required`
instead of auto-creating requests or bypassing approval. Blocks refuse additions.
The UI must explicitly create an Ask; contact grants never authorize these adds.

Occurrence `AddRequest` previews omit names, context/group IDs, group name,
submitted reason, location, hosts, siblings, recurrence and contact data. They add
only optional `eventTitle`, `date`, `startsAt`, `endsAt`, `tzid` to the checked
shared request/decision/status/capability envelope. Pending request possession
confers no read authority. No parent-event link is returned by the preview.

A valid final decision commits its own audit and resolves its waiting task even
when capacity/childcare/current capabilities prevent completion. Response stays
200 with `status=pending`, `canComplete=true`, no completed target, and optional
`completionProblem` (`capacity`, `childcare`, `options`, `throttled`). Retry with
`/complete`; no retry extends expiry or infers an override. `options` requires a
new Ask with currently valid options. A read may omit this transient problem;
readiness flags never promise room. Group pending-capacity behavior is unchanged.

Completion reloads current facts under gate FIRST and original holder/occurrence
locks. The real registration, granted request/completed target, new exact access,
audit, task resolution and safe notices commit atomically. Capacity/childcare
checks are before registration writes. New approved requests after an ending
append provenance; old approvals and direct additions cannot reopen old records.

Ordinary eligible adult direct account adds now create the same immutable
exact-date provenance and notice atomically. Duplicate active invitations may
update ordinarily permitted registration options without duplicate notices/access
or rewriting original issuer/request provenance. Guest/household/proxy paths keep
their existing rules and gain no override.

A shared persisted per-requester limit of 30 qualifying single-person date-add
attempts per minute covers account-add selection/email/ID, occurrence Ask create
and completion attempts. Decisions are still recorded if completion is throttled.
Direct/create throttling returns 409 `date_add_throttled`, never a new public 429:
“You have made several date additions. Wait a minute, then try again.” Approval
needed copy is “This date needs their approval. Send an ask to add instead.”

The added person and a teen's currently confirmed active guardians receive a
names-free, navigation-only, unbatched `occurrence.added` notice to the exact date.
Invitation changes/promotions/cancellations also use names-free exact-date links,
not hidden group or series links. Managers alone receive optional
`Attendee.addedByLeader`, computed from current explicit access, never inferred
from Registration source; other viewers have no such key.

Manual promotion services existing voluntary waitlist responses, teen included;
it is not a new invitation and mints no access. Fresh manager/session/destination
checks apply, but organizer add-blocks and previous special-access endings do
not obstruct an independently eligible voluntary waiter. Invitation-only waiters
need current exact access; stale/ended access never promotes. Automatic and
household/proxy promotion rules otherwise remain unchanged. Manager removal also
ends conflicting pending exact-date asks, preventing stale approvals after prune.

### Administrative contact/access history (M1.11 s5)

```
GET /api/v1/admin/contact-history/people?q=<literal name or email search>
GET /api/v1/admin/contact-history/people?id=<exact decimal ID>
GET /api/v1/admin/contact-history/people?email=<exact email>
  → 200 {people: [{id, name, email}]}
GET /api/v1/admin/users/:id/contact-history
  → 200 {entries: ContactHistoryEntry[], nextBefore?: string}
GET /api/v1/admin/users/:id/contact-history.csv
  → 200 text/csv; charset=utf-8
```

The people endpoint is an administrative picker, not a directory export. Lookup
mode precedence is `id`, then `email`, then `q`; parameters from lower-precedence
modes are ignored. `id` is an exact positive decimal string, `email` is an exact
case-insensitive match, and `q` is a literal case-insensitive contains search of
name, display name or email. Results include non-deleted accounts only, including
inactive accounts, return only `id`, public `name` and `email`, and are capped at
50 with no pagination. Narrow searches by full name or email. IDs remain exact
decimal strings.

These endpoints require a current active adult admin session. A requester, subject,
guardian, leader or host has no administrative history/export right, including
for their own ID. Existing private `/contact-requests`, `/contact-grants` and
`/add-requests` review/list permissions are unchanged. Session version, current
role and disabled/deleted state are checked on each request. Adult eligibility
uses the church-local calendar day, never the UTC day; teen accounts never
qualify. Responses use `Cache-Control: no-store`. GET export follows ordinary authenticated read
routing; it adds an audit row, not a new CSRF exemption.

`:id` is a positive retained account ID (up to signed bigint), not a live user
lookup. A deleted requester, deleted subject or absent history returns 200;
absent records give `entries: []` or a header-only CSV, not 404. History uses
retained AuditLog and request/occurrence-access provenance, independent of live
accounts, groups, events, registrations and notification cleanup.

JSON pagination is descending **audit ID**, regardless of timestamp. Optional
`limit` is 1–100 (default 50); `before` is an exclusive positive audit ID. When
`nextBefore` exists, pass it as `before` with the same filters to continue.
Timestamp ties cannot duplicate or skip IDs. JSON pages are fresh reads, not a
cross-request snapshot; newly committed higher IDs appear on a fresh first page.

Shared optional filters (ANDed with the person's actor/requester/subject
involvement):

- `purpose=contact|group_add|occurrence_add`
- `action`: one exact listed action below, not a prefix or arbitrary audit action
- `contextType=group|event`, optionally `contextId=<positive ID>`; an ID needs a type
- `occurrenceId=<positive ID>`
- `from=<RFC3339 timestamp>` inclusive, `until=<RFC3339 timestamp>` exclusive;
  when both are given, `from` must be earlier. The admin UI accepts inclusive
  **calendar days** in the church timezone from `GET /site`, converts From to
  that local day's start, and converts Until to the next local calendar day's
  start. It does not add 24 hours, so DST-short and DST-long days keep the right
  boundary, and it does not fall back to the browser timezone or UTC. The HTTP
  API itself continues to use RFC3339 instants and an exclusive `until`.
  Explicit Go zero timestamps (`0001-01-01T00:00:00Z`, including equivalent
  offset representations) are rejected, not interpreted as absent bounds.
  URL-encode offsets containing `+`.

Unknown, repeated, empty, malformed or overlong parameters return 400
`contact_history_filters`. Each value is at most 64 bytes; the total query is
at most 1024 bytes. CSV accepts these same shared filters but **rejects**
`before` and `limit`: it exports **all** matching retained entries, not a JSON
page and not a silently capped result.

`ContactHistoryEntry` is a closed projection:

```
{id, at, action, actorId?, requesterId?, subjectUserId?, purpose?,
 requestId?, accessId?, contextType?, contextId?, occurrenceId?, fields?,
 outcome, party?, decision?}
```

Every JSON ID is an exact **decimal string**, not a JavaScript number:
`id`, `actorId`, `requesterId`, `subjectUserId`, `requestId`, `accessId`,
`contextId`, `occurrenceId`, and page `nextBefore`. Optional IDs are omitted,
never null. `entries` is always an array. Positive IDs through
`"9223372036854775807"` round-trip exactly, including distinct values
`"9007199254740992"` and `"9007199254740993"`. Keep these as strings in
frontend state, URLSearchParams, cursors, comparisons and download paths;
**never use Number(), parseInt(), unary + or numeric sorting on history IDs**.
This does not change numeric IDs in other APIs or database/internal ID types.
`at` is RFC3339; actor absence denotes a system action. IDs denote retained
provenance, not permission to open a current context. Requester and subject are
loaded from the known retained request/access relation. Ordinary group/date add
and removal audits use only their typed numeric `userId` (and date
`occurrenceId`) metadata. No substring matching or live-person joins occur.
Malformed/missing metadata omits the unproven fields; it does not fail open or
panic. Generic endpoint refusals and request refusals without retained request
provenance remain actor-only rows. Submitted hidden person/context IDs,
selections or guessed request IDs are **not** recovered to invent a subject or
improve filtering. Such rows therefore do not appear under an alleged subject
or a purpose/context filter they cannot establish.

Included action families:

- `contact.requested`, `.decision`, `.withdrawn`, `.expired`, `.granted`,
  `.reveal`, `.refused`, `.revoked`, `.ended`
- `add.requested`, `.decision`, `.withdrawn`, `.expired`, `.completed`,
  `.refused`, `.ended`
- `occurrence_access.added`, `.ended`, `occurrence.blocked`, `.refused`
- `membership.added`, `.added_over_limit`, `.removed`
- `registration.added`, `.added_over_capacity`, `.removed`

This is not an unrestricted general audit viewer. Every action must also match
its known target shape. `outcome` is `requested`, `decision`, `approved`,
`denied`, `withdrawn`, `expired`, `granted`, `revealed`, `refused`, `revoked`,
`ended`, `added`, `removed` or `blocked`. For a valid decision audit only,
`party=subject|admin` and `decision=approved|denied` preserve the historical
party decision rather than the request's current final status. `fields` is the
contact field **selection** (`email|phone|both`), never its value; add rows have
no contact fields or grants. There are no names, email/phone values, submitted
reasons/notes, IPs, arbitrary metadata, request bodies, tokens, or video URLs.

CSV filename is deterministic: `contact-history-<personId>.csv`. Its fixed
column order is:

```
auditId,at,action,actorId,requesterId,subjectUserId,purpose,requestId,accessId,contextType,contextId,occurrenceId,fields,outcome,party,decision
```

Absent values are empty cells; timestamps are UTC RFC3339 with fractional
precision when present. The Go CSV encoder handles Unicode, commas, double
quotes and CRLF record endings. Every cell is spreadsheet-escaped: a leading
formula operator (`=`, `+`, `-`, `@`), including after whitespace, or a leading
control/format character gets a literal apostrophe prefix. Quoting alone is not
treated as formula protection.

Export reads a repeatable-read snapshot in internal pages of 100 into a private
0600 temporary file, then flushes/syncs/rewinds/stats it. Immediately before
the export audit, a `FOR SHARE` locking read rechecks the current actor, session
version, active/deleted state, admin role and church-local adult eligibility.
Under PostgreSQL REPEATABLE READ, an actor changed after the snapshot causes
this locking read to fail serialization; it is not trusted as stale authority.
No actor lock is held during lengthy page materialization. The checked row lock
is held through audit commit, the authorization linearization point. Revocation
after that point cannot retract a prepared export. The transaction commits
`contact_history.exported` (target type `user`, retained person ID; actor admin;
metadata contains count and the explicit safe filters). Only after that commit
are CSV headers/body emitted. Auth, query, disk or audit failures return an error
without partial CSV masquerading as a complete export. Temporary files are
closed and removed on normal success/error returns (not process crashes); no
blob service or persistent export store is introduced. Network interruption can
still interrupt a download; Content-Length
allows a client to detect it. The audit denotes a prepared authorized export,
not confirmed network receipt. Go memory is page-bounded, but aggregate temporary
disk, concurrent export count and transaction duration have no added budget.
The export audit is retained but is not itself an entry in this contact/add
action projection. History reads do not take the
contact mutation gate and do not reconcile lifecycle state. Existing lifecycle
mutations/sweeps own their actual audit entries. DEC-33 hash chaining, write-once
offsite storage and retention hardening remain deferred.

Verification evidence for the broader M1.11 milestone is tracked in
[M1.11-VERIFICATION.md](plan/M1.11-VERIFICATION.md).

### Per-date visibility (M1.13)

A date inherits `Event.visibility` unless its nullable `visibility_override`
column is set. `visibility` on occurrence lists is the effective value;
`visibilityOverride` is present for explicit date values. Managers also get
`seriesVisibility` (the parent value, not the date value) and, when the group
is visible, `groupVisibility` for the shared visibility editor.

```
PUT /api/v1/events/:id/occurrences/:date
    {visibility?: "public"|"private"|"invite_only"|"series"|null,
     reason?: string, confirm?: boolean, ...existing date edits}
GET /api/v1/events/:id/occurrences/:date/visibility-impact?visibility=series
GET /api/v1/events/:id/dates/:date
```

Omitting `visibility` leaves it unchanged. JSON null and `"series"` clear
only the visibility override. Explicit equal-to-series values remain explicit.
The usual event visibility policy applies: group leaders/admins may set any
value; church-wide hosts/admins may use public/private only. Reasons are
optional, at most 500 characters. Tightening, including clearing an open date,
previews newly inaccessible registrations on that date. PUT returns 409 with
`needsDecision` and `impact` until `confirm:true` acknowledges cancellation.
Active explicit exact-date invitations preserve their own registrations and
never grant series or group authority.

The exact-date GET permits signed-out readers only when the effective date
is public. It uses the restricted date projection, not an event/series DTO.
An opened hidden-series date is discoverable with a `dateUrl`; the unauthorized
series and sibling dates remain 404. Hidden group names, IDs, places, leaders,
attendee names, and recurrence metadata are not included. Public exact-date
pages use the ordinary verified guest form; verification rechecks current
visibility and cancellation under the occurrence lock.

Series visibility PUT accepts `resetOverrides?:boolean` (default false).
Its visibility-impact GET accepts `resetOverrides=1` and returns
`futureOverrides`, counted across all future stored dates, not the list limit.
Without reset, explicit dates keep their visibility and are excluded from the
series cancellations. With reset, each future override is cleared and audited
atomically, including when the series value itself stays the same. Past dates
and unrelated date customizations are unchanged.

Group visibility review includes explicit dates wider than the new group value
in `leaderOpened`, independently of the event's `leaderOpened` marker. Date
rows include `{id: eventId, occurrenceId, date, decisionKey: "date:<occurrenceId>",
visibility, title, registrants}`. Send keep/tighten decisions in `openedEvents`
using `decisionKey` for dates and the existing numeric event-ID keys for series.
All required decisions are rechecked under locks; missing ones roll back the
whole change, including inherited-series tightening.

Each actual date change/clear writes an audit row with `original_date`, old/new
values, group visibility when applicable and optional reason. A wider-than-group
date uses `event.visibility_override` and notifies other leaders/admins, naming
the date; unchanged saves do neither. Keeping an opened date during a group
review is audited and notified too. Registrations, audit and stored notices are
transactional; email delivery follows commit. Tightening notices identify the
affected date and never include hidden group details.

Date lists and calendar candidates are filtered per occurrence before limits.
Feeds with any visibility-customized dates use authorized standalone entries
with exact-date URLs, rather than an RRULE that could regenerate hidden dates.
Ordinary unchanged full-series feed behavior remains intact. Visibility changes
therefore alter feed bodies/ETags as well as API output.

Implemented consumers: group/upcoming/open-event lists, event/date details,
calendar, public/group/personal feeds, account/household RSVP, guest submission
and redemption, manager-add eligibility and waitlist promotion. Dedicated event
search, reminder scheduling and room-suggestion infrastructure do not exist at
this base. Their future consumers must resolve `policy.EffectiveVisibility`
per date and apply the same ordinary policy separately from exact-date grants;
no reminder/room/search integration pass is claimed by M1.13's current tests.
