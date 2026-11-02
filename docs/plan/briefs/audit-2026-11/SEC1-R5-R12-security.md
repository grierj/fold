# Audit 2026-11 briefs — SEC1, R5, R12 (security)

Source: [docs/AUDIT-2026-11-01.md](/home/grier/git/fold/docs/AUDIT-2026-11-01.md)
(SEC1, R5, R12), approved by the owner on 2026-11-01. R7 and R8 were
rejected; nothing here depends on them. Conventions for every brief:
[AGENTS.md](/home/grier/git/fold/AGENTS.md) — pure rules in
`internal/policy` with table tests, `audit.Write` + `notify.Send` in the
same transaction as a sensitive change, a code in
[api_errors.go](/home/grier/git/fold/api_errors.go:1) plus a line in
`ERROR_COPY` ([web/src/lib/errors.ts](/home/grier/git/fold/web/src/lib/errors.ts:13))
for every new error a person can act on, CSRF on every state-changing
route, DEC-52 uniform answers on public forms, tests beside the source
they cover. Schema changes are new goose files named with the next free
number at coding time (highest today:
[migrations/00062_reminder_push.sql](/home/grier/git/fold/migrations/00062_reminder_push.sql));
never edit an old migration.

Verify before every push (AGENTS.md):

```bash
go build ./...
go vet ./...
make lint-go
(cd web && npx tsc --noEmit)
(cd web && npx knip)
make test   # slice by slice while developing; full run before the PR
make e2e    # only where a brief names an e2e spec
```

Status note that shapes SEC1: the fix already landed as commit `28268b3`
("Protect first-run setup from takeover", on top of the audited HEAD
`3ec934e`). The SEC1 brief below is therefore a verify-and-close brief:
it records the approved design as built, with file:line evidence, and
lists the checks a coding agent runs to close issue #240. Do not
re-implement it. R5 and R12 are unbuilt.

---

# SEC1 — First-run setup takeover / double-admin race

| Field | Value |
|---|---|
| ID | SEC1 (audit Security summary) |
| Priority | P0 — security-critical; before any public deployment |
| Effort | M as audited; S to verify and close (built in `28268b3`) |
| Approved | 2026-11-01 |
| Issue | [#240](https://github.com/grierj/fold/issues/240) |

## Goal

Nobody except the operator who started the server can create the first
admin account, and two racing requests can never create two. A fresh
deployment is public the moment Caddy starts, so an unauthenticated
internet user must not be able to POST `/setup` before the volunteer
finishes, and concurrent POSTs must not both pass the "no users yet"
check.

## Current state (as built in `28268b3`; verify, do not rebuild)

- At the audited HEAD, `handleSetup` was check-then-Create with no
  transaction, lock, or token (audit SEC1 evidence). That is fixed:
- Token source: optional `FOLD_SETUP_TOKEN` env, exactly 64 hex chars
  (32 bytes), validated in `LoadConfig`
  ([config.go](/home/grier/git/fold/config.go:166)); invalid values fail
  startup with a fix-it message naming `openssl rand -hex 32`
  ([config.go](/home/grier/git/fold/config.go:170)). Stored on
  `Config.SetupToken` ([config.go](/home/grier/git/fold/config.go:27)).
- When the env is unset and no user exists, `serve` generates a token
  and logs it once: `setupComplete` gate, then `newSetupToken`, then
  one log line ([main.go](/home/grier/git/fold/main.go:128)).
  The generator is 32 bytes of `crypto/rand`, hex-encoded
  ([setup.go](/home/grier/git/fold/setup.go:28)).
- `GET /setup` is gated: without an authorized session it renders the
  code-entry page `setup-unlock.html`, not the wizard
  ([main.go](/home/grier/git/fold/main.go:559), `showSetup`;
  [templates/setup-unlock.html](/home/grier/git/fold/templates/setup-unlock.html:13)).
  `POST /setup` answers 403 without the authorized session
  ([main.go](/home/grier/git/fold/main.go:581), `handleSetup`).
- Unlock is a form POST, never a query parameter:
  `POST /setup/unlock` reads form field `setup_token`, compares with
  `subtle.ConstantTimeCompare`, and stores only the SHA-256 hash of the
  token in the session under `setupAuthorization`
  ([setup.go](/home/grier/git/fold/setup.go:36),
  [setup.go](/home/grier/git/fold/setup.go:53)). Authorization re-hashes
  the configured token and compares in constant time, so a changed
  token invalidates old setup sessions
  ([setup.go](/home/grier/git/fold/setup.go:41)).
- Both POSTs ride the normal middleware: routes register after
  `setupRequired` and `csrfMiddleware`
  ([main.go](/home/grier/git/fold/main.go:280)); neither is in
  `csrfExemptRoutes`. The unlock form carries `{{.csrfField}}`
  ([templates/setup-unlock.html](/home/grier/git/fold/templates/setup-unlock.html:16)).
- Creation is transactional: `createFirstAdmin` takes
  `pg_advisory_xact_lock` on a setup-specific lock id, re-counts users
  inside the lock, returns `errSetupComplete` when the count is
  non-zero, and writes the admin plus the `site_name` and timezone
  settings in the same transaction
  ([setup.go](/home/grier/git/fold/setup.go:79)). This mirrors the
  advisory-lock pattern in `lockVAPID`
  ([push_keys.go](/home/grier/git/fold/push_keys.go:115)) but uses its
  own lock id (`setupLockID`,
  [setup.go](/home/grier/git/fold/setup.go:23)), so setup never waits on
  a migration/push-key transaction.
- `setupComplete` caches "a user exists" for the life of the process
  ([main.go](/home/grier/git/fold/main.go:492)); `setupRequired`
  redirects everything else to `/setup` until then, exempting
  `/setup`, `/setup/unlock`, `/healthz`, and `/assets/`
  ([main.go](/home/grier/git/fold/main.go:533)).
- Log hygiene: the access log records only the request path, never a
  query string ([access_log.go](/home/grier/git/fold/access_log.go:58)),
  and the token only ever travels in a POST body. The one startup log
  line is the sole place the generated code appears; it is printed
  only while setup is incomplete, so it never prints again after
  setup. An env-provided token is never logged at all.
- Docs and deployment already name the flow: `.env.example` documents
  the variable ([.env.example](/home/grier/git/fold/.env.example:10)),
  compose passes it through
  ([docker-compose.yml](/home/grier/git/fold/docker-compose.yml:28)),
  the README quick start tells the volunteer to read the code from
  `docker compose logs app`
  ([README.md](/home/grier/git/fold/README.md:17)), and the
  DigitalOcean guide says the same with the provisioned paths
  ([docs/DEPLOY-DIGITALOCEAN.md](/home/grier/git/fold/docs/DEPLOY-DIGITALOCEAN.md:195)).

## Design decisions (recorded so nobody relitigates them)

- Env name is `FOLD_SETUP_TOKEN`, not the audit sketch's bare
  `SETUP_TOKEN`: it matches the `FOLD_` prefix of every other
  app-specific variable in `LoadConfig`.
- `APP_ENV=dev` gets no bypass. Dev and prod behave identically: read
  the printed code from the log, or set `FOLD_SETUP_TOKEN`. Tests set
  a fixed token through `testApp`
  ([testhelpers_test.go](/home/grier/git/fold/testhelpers_test.go:95));
  e2e does the same in `web/e2e/server.sh`. Rationale: a dev-only
  open door is exactly how an internet-facing first run gets missed,
  and "look at the log" costs a developer nothing.
- Form field over query parameter, weighed explicitly: a
  `?token=` link would be one click nicer, but query strings land in
  browser history, bookmarks, and any proxy or uptime-checker log that
  records full URLs. Fold's own access log is safe (see above), but
  Caddy's defaults and third-party log shippers are not under Fold's
  control. The form field plus a hashed session marker keeps the
  secret out of every URL. One consequence: the volunteer types or
  pastes the code — acceptable, and the unlock page says where to
  find it.
- Restart before setup rotates a generated code (documented in the
  README). An env token is stable across restarts, which is the
  recommended setting when more than one app process shares a
  database: the advisory lock still guarantees one admin, but each
  process would otherwise print a different code and only one of
  them would work.
- The unlock endpoint has no rate limit. The code is 256 bits of
  `crypto/rand` output (or an operator-chosen 32-byte value enforced
  by config validation), compared in constant time; online guessing
  is not a practical path, and a throttle keyed by IP would only
  lock out a volunteer who mistypes behind a shared NAT. Wrong codes
  get a plain 403 and the submitted value is never echoed.

## Scope

In scope (verification only):

- Run the existing test evidence and confirm each acceptance item.
- Confirm the four docs surfaces (`.env.example`, README,
  `docs/DEPLOY-DIGITALOCEAN.md`, `docker-compose.yml`) agree with the
  code, including the exact log line a volunteer searches for.
- Add the first-deploy hand-check to `docs/FIRST-CHURCH-CHECKS.md` if
  it is not already there: boot the real compose stack, read the code
  from `docker compose logs app`, complete setup, confirm a second
  visit to `/setup` redirects home. Only a live `docker compose up`
  proves Caddy + the wizard together (AGENTS.md).

Out of scope:

- Any change to the setup form fields, validation, or time-zone
  datalist (`templates/setup.html`); the 9-zone datalist gap is
  tracked in the audit's "Not verified" list, not here.
- Expiring or single-use setup codes, and IP allow-listing for
  `/setup`. The token + lock close the audited hole; expiry adds a
  lockout path for a slow volunteer with no extra safety at 256 bits.
- Multi-process generated codes (see decision above): documented
  behavior is "set the env", not a code change.

## Tests (already in tree; run them)

All in [setup_test.go](/home/grier/git/fold/setup_test.go:1) unless noted:

- `TestSetupRequiresOperatorCredential`: anonymous GET shows no wizard
  fields; anonymous POST with a valid CSRF token still gets 403 and
  creates zero users
  ([setup_test.go](/home/grier/git/fold/setup_test.go:22)).
- `TestSetupUnlock`: no-CSRF POST 403, wrong code 403 and never
  echoed, right code 303 to `/setup`, wizard then renders without
  exposing the code, and rotating the configured token kills the old
  session ([setup_test.go](/home/grier/git/fold/setup_test.go:90)).
- `TestConcurrentSetupCreatesOneAdmin`: two `App` instances (separate
  completion caches, one database) race past the unlocked count via a
  GORM callback barrier; exactly one admin is created, the winner
  redirects to `/login`, the loser to `/`, and `site_name` belongs to
  the winner ([setup_test.go](/home/grier/git/fold/setup_test.go:137)).
- `TestSetupRollsBackWhenSettingFails`: a forced failure on the second
  settings write leaves zero users and unchanged settings — admin and
  settings really are one transaction
  ([setup_test.go](/home/grier/git/fold/setup_test.go:207)).
- `TestNewSetupToken`: distinct 64-char hex values
  ([setup_test.go](/home/grier/git/fold/setup_test.go:56));
  `TestSetupTokenConfiguration` in `config_test.go` covers the env
  validation. `TestCSRFExemptRoutesAreExact` guards that neither
  setup POST drifts into the CSRF-exempt list.

## Acceptance checklist

- [ ] `go test -run 'TestSetup|TestConcurrentSetup|TestNewSetupToken' .`
      passes against `DATABASE_URL`.
- [ ] Anonymous GET `/setup` on a fresh database renders only the
      code-entry page (no `site_name` field in the HTML).
- [ ] Anonymous POST `/setup` with valid form + CSRF gets 403 and
      `SELECT count(*) FROM users` stays 0.
- [ ] Two concurrent authorized POSTs create exactly one `RoleAdmin`
      user and one coherent settings pair.
- [ ] Startup logs contain the code only while no user exists;
      after setup, a restart prints no code; with `FOLD_SETUP_TOKEN`
      set, no code ever prints.
- [ ] The token appears in no URL, access-log line, or HTML response.
- [ ] `docker compose up` hand-check recorded in
      `docs/FIRST-CHURCH-CHECKS.md` (code read from `logs app`).
- [ ] Issue #240 closed with a link to `28268b3` and this checklist.

## Risks / rollback / security notes

- Residual risk is log exposure: whoever can read container logs
  before setup can take over. That is the intended trust boundary
  (the operator owns the host); the docs say "keep the code private".
  A printed code is not revoked by anything short of completing setup
  or restarting with a different token.
- Rollback of `28268b3` would reopen the hole; there is no safe
  partial rollback. If a regression appears, fix forward.
- The setup session marker is a hash, so a leaked session cookie does
  not reveal the code itself. Session cookies are signed with
  `SESSION_SECRET` ([main.go](/home/grier/git/fold/main.go:122),
  `newSessionStore`); rotating that secret also kills setup sessions
  (see R12).

## Dependencies

None for verification. R1's compose smoke test, when it lands, should
include the setup-code flow; note it there rather than building it here.

## Open questions (with defaults)

- Should `/setup/unlock` failures be throttled per IP anyway?
  Default: no (see decision above). Revisit only if log noise from
  scanners becomes a problem.
- Should a generated code survive restarts by being stored hashed in
  `settings`? Default: no. It would add a row that must be cleaned up
  at setup and a new "which code is current" question; the env
  already covers the stable-code case.

---

# R5 — Privacy / session hardening bundle

| Field | Value |
|---|---|
| ID | R5 (audit P0) |
| Priority | P0 — before a first church goes live |
| Effort | M total; each slice S, independently shippable |
| Approved | 2026-11-01 |
| Issue | none filed; the audit section is the tracker |

## Goal

Close seven concrete leaks, each a place where behavior slips past a
rule the codebase already claims: roster privacy (a), copied-cookie
replay (b), GPS in photo uploads (c), private titles on door displays
(d), dangerous config accepted silently (e), account enumeration
through invite-a-friend (f), and an HTTP server with no timeouts (g).

## How to work this brief

One slice per PR, in the order given (cheapest certainty first).
Every slice is failing-test-first: write the test that demonstrates
the leak against the current code, watch it fail, then fix. Slices
share no migrations and no API shape changes except where a slice says
so. Do not bundle slices into one PR; the merge rule in AGENTS.md
needs a reviewer who can hold one leak in their head.

## Slice (a) — People-search group labels gated by the roster rule

### Current state

`apiSearchPeople` returns, for each of up to 10 matched people, their
public name plus `SharedGroups` / `OtherGroups` name lists
([api_people.go](/home/grier/git/fold/api_people.go:146)). The label
loader `peopleSearchGroups` pre-filters in SQL to groups that are
active and `public`/`private`, or shared, or led by the viewer (or
anything, for an admin), then re-checks each row with
`policy.CanSeeGroup` before the name leaves the API
([api_people.go](/home/grier/git/fold/api_people.go:233),
the policy call at
[api_people.go](/home/grier/git/fold/api_people.go:259)).

The leak: `CanSeeGroup` answers "may this viewer know the group
exists", not "may this viewer know who is in it". Membership is
roster information. The roster rule already exists:
`policy.CanSeeRoster` — people who share the group, and admins
([internal/policy/policy.go](/home/grier/git/fold/internal/policy/policy.go:74)).
Today any people-search-eligible leader (see `policy.CanSearchPeople`,
[internal/policy/contact.go](/home/grier/git/fold/internal/policy/contact.go:52))
can learn that a named person belongs to, say, a private recovery
group they themselves are not in. The person's name, teen flag, and
avatar are not the leak — searching people to add them is the point
of the endpoint, and teen avatars are already suppressed
([api_people.go](/home/grier/git/fold/api_people.go:203)).

### Change

- In `peopleSearchGroups`
  ([api_people.go](/home/grier/git/fold/api_people.go:233)), gate each
  label with `policy.CanSeeRoster` instead of `policy.CanSeeGroup`.
  Consequences, all intended: `SharedGroups` is unchanged (a shared
  membership passes the roster rule by definition); `OtherGroups`
  survives only for admins, who pass the roster rule everywhere.
- Tighten the SQL pre-filter to match the new rule (admin, or viewer
  actively in the group), because the `LIMIT 10` is per subject and
  hidden labels must not crowd out visible ones — the same
  filter-before-limit reasoning already documented above the query
  ([api_people.go](/home/grier/git/fold/api_people.go:230)). The pure
  policy call stays the disclosure authority; SQL is only a
  performance pre-filter, and the two must not diverge in the
  permissive direction.
- No new policy function and no signature change: `CanSeeRoster`
  takes the same `(p, g, r)` facts the loader already builds. Keep
  the `// group-ok:` comment accurate — it now justifies a roster
  check, and `TestGroupDetailsGoThroughGroupShownTo` reads those
  comments.
- API shape is unchanged (`peopleSearchJSON` / `personSearchJSON`,
  `web/src/types.ts` untouched): `otherGroups` simply arrives empty
  for non-admins. Check the picking UI renders an empty list
  gracefully (no dangling label); if it shows a heading for
  `otherGroups`, hide the heading when empty — that is the only
  permitted frontend change in this slice.

### Tests (failing first)

- `api_people_test.go`, beside the existing people-search tests:
  leader of group A searches a member of private group B (not a
  member of B): the result's `otherGroups` is empty and B's name
  appears nowhere in the response body. Same fixture, viewer also in
  B: B appears under `sharedGroups`. Admin viewer: B appears under
  `otherGroups` (roster rule admits admins).
- Crowding case: subject in 11 groups, 10 invisible to the viewer and
  1 shared; the shared label still comes back (proves the SQL
  pre-filter, not just the policy call).
- No policy table test changes: `CanSeeRoster` is already covered.

## Slice (b) — Logout bumps `SessionVersion`; login rotates CSRF

### Current state

`apiLogout` expires the browser's cookie and ends this browser's push
device, but writes nothing to the database
([api_auth.go](/home/grier/git/fold/api_auth.go:290)). A copied
session cookie therefore keeps working for its full 7-day codec
lifetime (`newSessionStore`,
[main.go](/home/grier/git/fold/main.go:79)).

The revocation mechanism exists and is per-user, not per-device:
every authenticated request compares the cookie's stored version
against `users.session_version` and refuses on mismatch
([api.go](/home/grier/git/fold/api.go:346),
`authenticatedSessionUser`; the column is
[models/models.go](/home/grier/git/fold/models/models.go:160)).
Existing bumps: password replacement (`replacePasswordIn`,
[account_security.go](/home/grier/git/fold/account_security.go:129)),
plus the email/role/status changes documented on the column. Callers
that must keep the current browser signed in re-store the fresh
version after a bump — see `apiChangePassword` re-saving the session
([api_auth.go](/home/grier/git/fold/api_auth.go:276)).

Separately, login keeps the pre-login CSRF token: `signInSession`
stores `userID`/`sessionVersion` and saves, leaving the `csrfToken`
session value untouched
([api_auth.go](/home/grier/git/fold/api_auth.go:559)). The token is
created by `csrfToken` only when the session lacks one
([csrf.go](/home/grier/git/fold/csrf.go:27)) and checked in
`csrfMiddleware` ([csrf.go](/home/grier/git/fold/csrf.go:51)). A token
fixated or leaked before login therefore stays valid after it.

### Change

- Logout: in `apiLogout`
  ([api_auth.go](/home/grier/git/fold/api_auth.go:294)), inside a
  transaction, increment the caller's `session_version`
  (`session_version + 1`, the expression `replacePasswordIn` uses),
  then expire the cookie as today. Logout is currently unaudited;
  keep it that way in this slice unless an `audit` action already
  exists for it — do not invent one silently (see open questions).
  The push-device ending stays in the same request.
- State this in the PR description, because it is the visible
  behavior change: **`SessionVersion` is one epoch per account, so
  signing out anywhere signs the account out on every device.**
  There is no per-device session table and this slice does not add
  one. This matches the semantics members already get from a
  password change ("This browser stays logged in; other sessions for
  the account do not",
  [api_auth.go](/home/grier/git/fold/api_auth.go:229)) minus the
  current-browser exception — at logout there is no browser to keep.
- Before coding, read the push revocation trigger (migration 00059,
  described in AGENTS.md: an account's devices are deleted when its
  sessions are revoked or it stops being active, queueing
  `push.devices_off`). If that trigger keys off `session_version`
  changes, a per-logout bump would wipe the person's push devices on
  every ordinary sign-out and send a misleading bell notice. If so,
  the slice must exempt the logout bump from that trigger (narrow the
  trigger's `WHEN` clause in a new migration with the next free
  number, model unchanged) — devices belong to the person, and only
  the current browser's device ends at logout (the existing
  `endPushDeviceAtLogout` path). Verify against the trigger body;
  do not assume.
- CSRF rotation: in `signInSession`, delete the `csrfToken` session
  value before saving, on both branches (eligible login and the
  cleared-login teen branch). The next request's `csrfMiddleware`
  mints a fresh token via `csrfToken`. Because `signInSession` is
  shared, password-reset sign-in (`apiConfirmPasswordReset`,
  [api_auth.go](/home/grier/git/fold/api_auth.go:552)) rotates too —
  that is deliberate, not a side effect to avoid. The login response
  itself carries no CSRF token (the SPA fetches `GET /csrf` through
  `apiGetCSRF`, [api_auth.go](/home/grier/git/fold/api_auth.go:27)),
  so no response-shape change is needed; confirm the SPA refetches
  after login rather than caching the pre-login token, and fix the
  client if it caches (`web/src/api.ts`).

### Tests (failing first)

- `api_auth_test.go` beside `TestLogoutEndsSession`
  ([api_auth_test.go](/home/grier/git/fold/api_auth_test.go:491)):
  capture the session cookie before logout, log out, replay the
  captured cookie against an authenticated route — 401. A second
  "device" (separately minted cookie for the same user) is also 401
  after the first device logs out; name the test so the all-device
  semantics are documented by it. Signing in again afterwards works.
- Push regression test if the trigger needed narrowing: a user with a
  `push_subscriptions` row on another device logs out; the other
  device's row survives and no `push.devices_off` job is queued.
  Put it beside the existing push-device-off tests.
- CSRF: fetch a token signed-out, log in, submit a state-changing
  request with the old token — 403; with a freshly fetched token —
  success. Extend the cookie-jar + CSRF dance in
  `testhelpers_test.go` only if the helpers assume a stable token
  across login; prefer a focused test in `api_auth_test.go` and a
  unit case in `csrf_test.go` if `csrfToken` gains a rotate helper.

## Slice (c) — Strip EXIF by re-encoding uploads on the server

### Current state

`readImageUpload` size-checks (5 MB, real byte count) and
content-sniffs an upload, then hands back the temp file verbatim
([api_profile.go](/home/grier/git/fold/api_profile.go:388)); both
callers store those bytes unchanged — avatars via `saveAvatar`
([api_profile.go](/home/grier/git/fold/api_profile.go:480)) and group
covers in `apiUploadGroupCover`
([api_profile.go](/home/grier/git/fold/api_profile.go:539)). Accepted
types are JPEG, PNG, GIF, WebP
([api_profile.go](/home/grier/git/fold/api_profile.go:41),
`allowedImageTypes`). A phone photo's EXIF block — GPS coordinates
of a member's home, timestamps, device serials — is therefore served
to anyone who can fetch `/uploads/<key>`.

The browser does not fix this: `prepareUpload` returns files of 1 MB
or less untouched, and only larger files are recompressed (to JPEG,
which incidentally drops metadata)
([web/src/lib/image.ts](/home/grier/git/fold/web/src/lib/image.ts:1)).
Browser resizing is a CPU optimization (AGENTS.md); the server is
the enforcement point.

### Change

Re-encode in `readImageUpload` after sniffing, so both callers and
any future upload field inherit it. Decode to pixels and encode
fresh bytes; never copy ancillary chunks. Use only the standard
library (`image`, `image/jpeg`, `image/png`, `image/gif`,
`image/draw`); the binary stays `CGO_ENABLED=0` with no new
dependency for three of the four formats.

- Safety first: call `image.DecodeConfig` before decoding and refuse
  anything over a pixel cap (default 25 megapixels) or with zero /
  absurd dimensions. A 5 MB file can describe a gigapixel canvas;
  without the cap this slice trades a privacy leak for a memory DoS.
  Refuse with the existing 400 path plus a new error code +
  `ERROR_COPY` line if the message is one a person can act on
  ("that photo is too large; try a smaller one").
- JPEG: read the EXIF orientation (values 1–8) from the APP1 segment
  **before** stripping — a minimal hand-rolled parser, since the
  stdlib has none — decode, apply the orientation to the pixels
  (rotate/flip via a small transform helper), then `jpeg.Encode` at
  quality 85. Skipping orientation ships sideways portraits the day
  this lands; it is not optional polish.
- PNG: decode and `png.Encode`. Text chunks and any embedded
  metadata die with the re-encode.
- GIF: `gif.DecodeAll` and `gif.EncodeAll`, preserving frames,
  delays, disposal, and loop count so animated avatars keep moving.
  Cap total frames (default 200) under the same pixel budget
  (pixels × frames); over the cap, refuse like an oversize image
  rather than silently flattening someone's animation.
- WebP: the stdlib cannot decode or encode it, and
  `golang.org/x/image/webp` is decode-only and not yet a dependency.
  Default for this slice: **remove WebP from `allowedImageTypes`**
  and refuse with the actionable 400 above ("…try a JPEG or PNG").
  Fail closed rather than store unstripped WebP metadata or grow a
  half-format. The browser already converts large files to JPEG, so
  the affected set is small WebP files a member picked deliberately;
  the error tells them the fix. Revisit only with a maintained
  pure-Go WebP encoder — that is a dependency decision for the
  owner, not this slice.
- The re-encoded bytes define the stored size and MIME from then on;
  extension and key logic in `saveAvatar` / `apiUploadGroupCover`
  already key off the sniffed MIME, which is now also the encoded
  format. Focal points are stored 0–100 and rendered as CSS
  `object-position` (AGENTS.md), so a corrected orientation changes
  which way is up, not the focal math — apply orientation before
  encoding so the focal point members picked in a right-side-up
  preview still frames the same spot.
- Put the logic in a new `image_normalize.go` beside
  `api_profile.go` (`normalizeImage(tmp, mime) (ext, mime, error)`
  plus the orientation parser/transform), with its test file beside
  it. Keep `readImageUpload`'s signature so callers don't move.
- Already-stored blobs keep their EXIF: out of scope. Note in the PR
  that a one-off backfill (re-encode every key in `avatars/` and
  `covers/`) is a follow-up the owner can request; do not build it
  into a startup path or a jobs kind speculatively.

### Tests (failing first)

In `image_normalize_test.go` (unit, no database) and
`api_profile_test.go` (handler):

- A JPEG fixture carrying an EXIF block with GPS tags: after upload,
  the stored bytes decode as JPEG and contain no `Exif` APP1 marker
  and none of the fixture's GPS byte patterns. Same assertion for a
  PNG `tEXt` chunk.
- Orientation fixtures: a 2×3 JPEG with orientation 6 comes out 3×2
  with pixels in the rotated positions; orientations 1 and 3 as
  controls. Build fixtures in-test by encoding then splicing a
  minimal APP1 segment, so no binary testdata is needed.
- Animated GIF (3 frames, distinct delays) round-trips animated
  with delays intact; WebP bytes now get the refusal; a
  `DecodeConfig`-huge header (claiming dimensions over the cap) is
  refused without decoding.
- Existing avatar/cover handler tests keep passing unchanged
  (formats, size limit, teen refusal).

## Slice (d) — Room feeds: "Reserved" for every non-public date

### Current state

The admin-issued room feed (`buildRoomFeed`,
[api_room_feeds.go](/home/grier/git/fold/api_room_feeds.go:190))
describes a booking — title, location line, URL, description —
unless `roomFeedDescribes` says no. Today that rule hides a date
only when its effective visibility is invite-only
(`policy.EffectiveVisibility` over the event value and the
occurrence override)
([api_room_feeds.go](/home/grier/git/fold/api_room_feeds.go:291)).
A **private** (signed-in members only) event's title and description
therefore land on a door display or office calendar that holds
nothing but a secret link — readable by anyone who can see the
screen, signed in or not. Two extra guards already limit the damage
for invite-only groups (no group location line, no series
description) at
[api_room_feeds.go](/home/grier/git/fold/api_room_feeds.go:267), but
they never fire for merely-private dates.

The sibling surface shows the intended shape: the by-room calendar
view starts every slot as `Reserved` and only fills in details the
*specific viewer* may see on that exact date
([api_room_calendar.go](/home/grier/git/fold/api_room_calendar.go:191)).
A feed has no viewer — the link is the only credential — so the
faithful analog of "a member of the public is looking" is: describe
public dates, reserve everything else.

### Change

- In `roomFeedDescribes`
  ([api_room_feeds.go](/home/grier/git/fold/api_room_feeds.go:294)),
  require `policy.EffectiveVisibility(...) == policy.Public`.
  Keep the missing/deleted-group refusal. The invite-only-only
  location/description guards stay as defense in depth; do not
  delete them.
- Update the file's doc comment, which currently promises the old
  rule ("except that a date whose own visibility is invite-only is
  titled Reserved",
  [api_room_feeds.go](/home/grier/git/fold/api_room_feeds.go:20)).
- Nothing else changes: same route, same token handling, same ICS
  shape. `DTSTAMP` already advances when a described date becomes
  Reserved (the `UpdatedAt` handling at
  [api_room_feeds.go](/home/grier/git/fold/api_room_feeds.go:263)),
  so subscribed calendars replace the old title instead of keeping
  it forever.
- Occurrence overrides cut both ways and must be honored as today:
  a private series with one date overridden to public is described
  on that date only (leaders open single dates deliberately,
  DEC-48); a public series with a private override is Reserved on
  that date. `EffectiveVisibility` already encodes this; the slice
  changes the comparison, not the input.

### Tests (failing first)

- Extend `TestRoomFeedContents`
  ([api_room_feeds_test.go](/home/grier/git/fold/api_room_feeds_test.go:96)):
  a private event's booking now renders `SUMMARY:Reserved` with no
  title, description, or event URL in the body; the public booking
  is still described. Add the two override cases above (private
  series + public override described; public series + private
  override Reserved).
- `TestRoomFeedLinks` and `TestRoomFeedIsStableAndScoped` unchanged.

## Slice (e) — Refuse dangerous config values at startup

### Current state

`LoadConfig` ([config.go](/home/grier/git/fold/config.go:158))
validates several values in a consistent fix-it style — name the
variable, say what's wrong (never the secret's value), give the exact
fix and where to put it — exemplified by `shortSecretMessage`
([config.go](/home/grier/git/fold/config.go:126)) and the
`FOLD_SETUP_TOKEN` check
([config.go](/home/grier/git/fold/config.go:166)). Three dangerous
values slip through:

- `parseTrustedProxies` accepts any parseable CIDR, including
  `0.0.0.0/0` and `::/0`
  ([config.go](/home/grier/git/fold/config.go:108)). A `/0` entry
  makes Gin believe `X-Forwarded-For` from the whole internet, so
  every IP-keyed throttle (login, public forms) becomes
  attacker-spoofable.
- `SiteURL` is stored unexamined
  ([config.go](/home/grier/git/fold/config.go:208)). Downstream code
  already distrusts it — `vapidSubject` ignores a non-https value
  ([push_keys.go](/home/grier/git/fold/push_keys.go:262)) — but email
  links are built from it, so an `http://` value in prod quietly
  ships password-reset and invitation links in cleartext form.
- `CookieSecure` defaults to `!isDev` but an explicit
  `COOKIE_SECURE=false` is honored in prod
  ([config.go](/home/grier/git/fold/config.go:207)), sending session
  cookies without `Secure` and dropping HSTS with it
  (`securityHeaders`, [main.go](/home/grier/git/fold/main.go:461)).

### Change

All in `config.go`, all refusing to start (return an error from
`LoadConfig`) in the established message style. `APP_ENV=dev` is
exempt from the second and third only; a `/0` proxy range is never
legitimate, dev included.

- Trusted proxies: in `parseTrustedProxies`, reject any CIDR with
  prefix length 0 (`0.0.0.0/0`, `::/0`). Bare IPs stay fine. Message
  shape: "FOLD_TRUSTED_PROXIES: \"0.0.0.0/0\" trusts every sender;
  list only your reverse proxy's address or subnet (the shipped
  Compose default is 172.30.0.0/24)".
- `SITE_URL`: when set and not dev, require it to parse as an
  `https` URL with a host (the same acceptance test `vapidSubject`
  uses). Unset stays allowed (links fall back as today). Message
  names the fix: set it to `https://<DOMAIN>` in `.env`, as the
  shipped compose file already does
  ([docker-compose.yml](/home/grier/git/fold/docker-compose.yml:31)).
- `COOKIE_SECURE=false` when not dev: refuse, naming the fix (remove
  it, or set it to `true`; plain-HTTP testing belongs in
  `APP_ENV=dev`).
- The shipped paths stay green by construction: compose sets
  `COOKIE_SECURE: "true"`, an https `SITE_URL`, and the fixed
  `/24` proxy subnet. Changing the compose subnet still means
  changing `FOLD_TRUSTED_PROXIES` with it (AGENTS.md) — untouched.

### Tests (failing first)

- `config_test.go` table cases: `/0` in either family refused (dev
  and prod), `http://` and hostless `SITE_URL` refused in prod but
  accepted in dev, `COOKIE_SECURE=false` refused in prod but
  accepted in dev, and the valid shipped-combination accepted.
  Assert the error names the variable and never echoes
  `SESSION_SECRET` — keep the suite's existing no-secret-in-errors
  habit.

## Slice (f) — Uniform invite-a-friend answer (DEC-52)

### Current state

`storeAccountRequest` serves both public doors
([api_account_requests.go](/home/grier/git/fold/api_account_requests.go:196)).
When the submitted email already has an account, self-signup answers
uniformly and emails the hint to the address instead
([api_account_requests.go](/home/grier/git/fold/api_account_requests.go:243)) —
but the member invite-a-friend door breaks ranks: it answers 409
"that email already has an account"
([api_account_requests.go](/home/grier/git/fold/api_account_requests.go:236)),
with a comment arguing an authenticated member may learn what an
inviter learns. DEC-52 (AGENTS.md) says the opposite for answers
that reveal whether an account exists; its only 429-style exceptions
are sign-in lockout and a leader's own invitation limit. As built,
any signed-in adult member (`policy.CanInviteFriend`,
[internal/policy/policy.go](/home/grier/git/fold/internal/policy/policy.go:432);
handler gate in `apiInviteFriend`,
[api_account_requests.go](/home/grier/git/fold/api_account_requests.go:336))
can test arbitrary addresses for church membership — exactly the
enumeration the file's own header promises to prevent
([api_account_requests.go](/home/grier/git/fold/api_account_requests.go:25)).
The cost-equalization is already in place: both paths pay a dummy
bcrypt before the branch
([api_account_requests.go](/home/grier/git/fold/api_account_requests.go:233)),
and the IP/site/per-address throttles run first.

### Change

- Delete the member-invite 409 branch. The already-have-an-account
  path becomes identical for both sources: uniform
  `answerAccountRequest` reply (200 with each door's usual thanks —
  for invite-a-friend, the existing "an admin will review it…"
  message at
  [api_account_requests.go](/home/grier/git/fold/api_account_requests.go:308)),
  no row stored, and the already-have-an-account hint emailed to the
  address (skipped for honeypot submissions, as today). Emailing the
  *address* is safe: the hint reaches only the account holder, and
  the per-address throttle above caps it at 3/hour and 10/day.
- Fix the stale comment; the header comment at the top of the file
  becomes true again without edits.
- Frontend: the invite-a-friend form must stop special-casing a 409
  that no longer exists. Find its error handling (search the SPA for
  the conflict copy / its `ERROR_COPY` code) and let the usual
  success message show. If an error code becomes unused, remove it
  from both `api_errors.go` and `ERROR_COPY` together —
  `TestErrorCodesKeysAreUsed` and `TestWebErrorCopyCoversEveryCode`
  police both directions.
- Out of scope, deliberately: leader/admin invitations keep their
  409s ("an account with that email already exists",
  [api_invitations.go](/home/grier/git/fold/api_invitations.go:242);
  the household sibling at
  [api_invitations.go](/home/grier/git/fold/api_invitations.go:474)).
  Inviters are managers who can already find existing members
  through people search and add them directly, so the 409 tells them
  nothing they cannot already see, and it is genuinely actionable
  ("add them instead"). The audit named invite-a-friend only. Flag
  the distinction in the PR so the reviewer confirms it.

### Tests (failing first)

- `api_account_requests_test.go`: invite-a-friend for an
  existing-account email now returns 200 with the standard thanks,
  stores no `account_requests` row, writes no admin bell item, and
  queues exactly one hint email to that address; for a new email it
  stores the pending request as today. Assert the two responses are
  byte-identical in status and body shape.
- Existing self-signup uniformity tests and
  `TestOnlyLoginAndInvitesAnswer429` must stay green untouched.

## Slice (g) — HTTP server timeouts

### Current state

The server is a bare `&http.Server{Addr: …, Handler: r}` with no
timeouts of any kind
([main.go](/home/grier/git/fold/main.go:160)). Slow-loris headers,
dribbled bodies, and stuck responses can hold connections (and the
goroutines behind them) forever. Body *size* is already capped
(`limitBody(maxRequestBytes)`,
[main.go](/home/grier/git/fold/main.go:623)); body *duration* is not.
Shutdown already allows 10 s of grace
([main.go](/home/grier/git/fold/main.go:168)).

### Change

- Extract construction into `newHTTPServer(cfg, handler)` in
  `main.go` so the values are unit-testable, and set:
  `ReadHeaderTimeout: 10s`, `ReadTimeout: 60s`,
  `WriteTimeout: 60s`, `IdleTimeout: 120s`,
  `MaxHeaderBytes: 1 << 20`.
- Reasoning to record in the PR: the largest legitimate request is a
  5 MB photo upload (plus 1 MB envelope) from a phone on church
  Wi-Fi — 60 s to read it is generous without being an invitation.
  No route streams, long-polls, or holds a request open: background
  work runs through `internal/jobs`, not request goroutines
  (AGENTS.md). Before finalizing the numbers, grep the route table
  for any handler that waits on mail or a job; if one exists, it
  gets a context deadline of its own rather than a bigger server
  timeout. `fold healthcheck` keeps its own 2 s client timeout
  ([main.go](/home/grier/git/fold/main.go:220)) and is unaffected.
- Slow *responses* behind Caddy: 60 s write timeout is measured from
  header read; a large ICS feed to a slow door display is the worst
  realistic case and is kilobytes. Fine.

### Tests (failing first)

- `main_test.go`: `newHTTPServer` returns exactly the pinned values
  (a guard against drive-by edits, in the spirit of the existing
  ratchet tests).
- One integration case with `httptest`: a client that sends partial
  headers and stalls is disconnected around `ReadHeaderTimeout`
  (use a short override in the test build of the server, not the
  production 10 s, if the constructor takes the values from config —
  otherwise assert the field values only and skip the sleep).

## Two audited add-ons: decided in / out

### Password minimum counts characters, not bytes — IN (fold into slice (b)'s PR or its own S PR)

The copy promises characters: `minPasswordLength = 10`
([api.go](/home/grier/git/fold/api.go:16)) and every new-password
field says "at least 10 characters". The check measures bytes:
`len(pw) < minPasswordLength`
([password_rules.go](/home/grier/git/fold/password_rules.go:18),
`passwordRuleError`), duplicated in setup validation
([setup.go](/home/grier/git/fold/setup.go:148)). Four emoji (16
bytes) currently pass the "10 characters" rule. Change: the minimum
test uses `utf8.RuneCountInString`; the 72-**byte** maximum stays
bytes — it is bcrypt's hard limit, and `password_rules.go` already
documents why ([password_rules.go](/home/grier/git/fold/password_rules.go:9)).
Make setup call the shared rule instead of its copy so the two can't
drift again (setup renders its own field messages; keep the wording,
take the verdict from one helper). Sign-in never re-checks length,
so existing passwords are untouched. Tests:
`password_rules_test.go` gains multibyte cases (9 runes / 30+ bytes
refused for minimum; 10 runes accepted; >72 bytes still refused) and
`setup_test.go`'s validation table gains one matching row.

### Global bcrypt concurrency cap at login — OUT

The fear is CPU exhaustion: each login costs one bcrypt comparison
at `bcrypt.DefaultCost`
([models/models.go](/home/grier/git/fold/models/models.go:183)).
But attempts are already reserved *before* any bcrypt work,
atomically, per email (10) and per IP (50) per 15 minutes
([api_auth.go](/home/grier/git/fold/api_auth.go:31),
`reserveLoginAttempt` at
[api_auth.go](/home/grier/git/fold/api_auth.go:121)), and the
password-reset door pays its dummy comparison only after the same
style of throttle passes
([api_auth.go](/home/grier/git/fold/api_auth.go:392)). A global
semaphore adds a new failure mode — queueing delay as a timing
signal, and one attacker filling the slots to delay everyone's
sign-ins — to bound a cost the throttles already bound per source.
At single-church scale behind those limits, the remaining exposure
is a distributed-IP spray across many distinct emails, which the
per-IP cap is the wrong tool for anyway. Revisit trigger: sustained
login CPU pressure in a real deployment's logs, at which point the
fix is a site-wide attempt budget in `login_throttles` (DEC-52:
still the uniform answer, logged server-side), not an in-process
semaphore that replicas wouldn't share.

## R5 data / API shapes

- Migrations: none expected. The single exception is slice (b)'s
  conditional trigger narrowing (next free goose number at coding
  time; GORM models unchanged, drift test must stay green).
- API: no route, field, or type changes. Behavior changes are:
  `otherGroups` empties for non-admins (a); old cookies and old CSRF
  tokens die at logout/login (b); WebP uploads and oversize-pixel
  images get a 400 with a new error code + `ERROR_COPY` line (c);
  private room bookings render Reserved in ICS (d); startup refuses
  three config values (e); invite-a-friend always answers 200
  thanks (f). `web/src/types.ts` needs no edits anywhere in R5.

## R5 acceptance checklist

- [ ] (a) Non-member leader cannot learn a subject's group names;
      shared labels and admin view intact; crowding test green.
- [ ] (b) Pre-logout cookie replay gets 401; second device also
      signed out (documented); pre-login CSRF token gets 403 after
      login; other-device push subscriptions survive a logout.
- [ ] (c) Stored JPEG/PNG bytes contain no EXIF/text metadata;
      orientation-6 photo stored right-side up; animated GIF stays
      animated; WebP and over-cap-pixel images get the actionable
      400; existing upload tests green.
- [ ] (d) Private booking is `SUMMARY:Reserved` with no description
      or URL; public and public-override dates still described.
- [ ] (e) Server refuses `/0` proxies (any env), non-https
      `SITE_URL` and `COOKIE_SECURE=false` outside dev, with
      fix-it messages; shipped compose values boot.
- [ ] (f) Invite-a-friend for an existing account is
      indistinguishable from a stored request; hint emailed to the
      address only; leader-invitation 409s untouched.
- [ ] (g) `newHTTPServer` values pinned by test; `make test` and
      `make e2e` green (no slow legitimate flow clipped).
- [ ] Password minimum counts runes in both the API and setup;
      72-byte cap unchanged.
- [ ] Every slice PR: failing test first (shown in the PR), then
      `go build ./...`, `go vet ./...`, `make lint-go`, `make test`;
      web checks (`tsc`, `knip`, Vitest) on slices (a), (c), (f).

## Risks / rollback / security notes

- (b) is the only member-visible behavior change: shared family
  tablets will sign out everywhere when anyone signs out. It is the
  audited, approved trade; per-device sessions would be a new data
  model and are explicitly not this slice.
- (c) re-encoding is lossy for JPEG (quality 85) and final: the
  original bytes are never stored, so there is nothing to roll back
  to for photos uploaded after the change. Rollback of the code
  simply stops stripping new uploads.
- (d) churches with door displays showing private group titles will
  see "Reserved" after upgrade. That is the fix working; mention it
  in the PR and release notes so it isn't filed as a bug.
- (e) a misconfigured existing install stops booting until fixed.
  That is intended (fail loudly at startup over silently insecure),
  and it is why each message carries its own fix.
- Each slice reverts independently; no slice depends on another.

## Dependencies

None between slices. (c) touches the same handler file as nothing
else in R5; (b) and the password-rune fix both touch auth files —
sequence them, don't parallelize. R12's session discussion relies on
(b)'s epoch semantics staying as described.

## Open questions (with defaults)

- (b) Should logout write an audit row now that it changes account
  state? Default: no new audit action in this slice; AGENTS.md asks
  for audit rows on *sensitive* actions, and inventing a `logout`
  action also means a registry entry and copy. Revisit if R3's TOTP
  work adds session auditing anyway.
- (c) Pixel cap and JPEG quality are defaults (25 MP, q85), chosen
  to sit far above phone photos after browser downscaling. Tune
  only with a stated reason in the PR.
- (c) WebP: removal is the default. If the owner prefers keeping
  WebP uploads, the alternative is decode via a new dependency and
  store as PNG/JPEG — a dependency + format-change decision to make
  explicitly before coding, not mid-slice.
- (f) Should leader invitations also go uniform for consistency?
  Default: no (managers can already see members; the 409 is
  actionable). The PR flags it; the owner can flip it there.

---

# R12 — `SESSION_SECRET` rotation runbook + versioned link expiry

| Field | Value |
|---|---|
| ID | R12 (audit P1) |
| Priority | P1 — pre-live window; do before the first church accumulates emailed links |
| Effort | M (token format + config + runbook; no schema change) |
| Approved | 2026-11-01 |
| Issue | none filed; the audit section is the tracker |

## Goal

Rotating a compromised `SESSION_SECRET` must be a survivable,
documented operation instead of a choice between "stay compromised"
and "kill every unsubscribe link ever emailed". Concretely: old
unsubscribe links keep verifying across one rotation, the email
settings link stops being a permanent all-categories capability,
and an operator has an ordered runbook for everything else the
secret touches.

## Current state — everywhere the one secret reaches

`SESSION_SECRET` is read once per process (`sessionSecret`,
[config.go](/home/grier/git/fold/config.go:138); ≥32 chars outside
dev) and stored as `Config.SessionSecret`
([config.go](/home/grier/git/fold/config.go:24)). Every use, with its
lifetime and its behavior when the secret changes:

| Use | Where | Lifetime | On secret change |
|---|---|---|---|
| Session cookies (signing) | `newSessionStore`, [main.go](/home/grier/git/fold/main.go:79); built at [main.go](/home/grier/git/fold/main.go:122) | 7 days | All cookies die; everyone signs in again. Desired. |
| VAPID private key at rest | `vapidCipher` — key = SHA-256(`fold:vapid:v1:` ∥ secret), AES-GCM, [push_keys.go](/home/grier/git/fold/push_keys.go:48) | Until rotated | Self-heals: `ensureVAPIDKeys` makes a new pair, deletes every `push_subscriptions` row, audits + notifies admins in the same transaction ([push_keys.go](/home/grier/git/fold/push_keys.go:145)). Members must re-enable notifications. |
| Unsubscribe + email-settings links | HMAC key = SHA-256(`fold:unsubscribe:v1:` ∥ secret), [email_unsubscribe.go](/home/grier/git/fold/email_unsubscribe.go:29) | **Never expires** (by design, so an old email's button works, [email_unsubscribe.go](/home/grier/git/fold/email_unsubscribe.go:19)) | **Every link ever emailed dies permanently.** The audited problem. |
| People-search selection tokens | AES-GCM, SHA-256(`fold:people-selection:v1:` ∥ secret), [api_people.go](/home/grier/git/fold/api_people.go:39) | 10 minutes (`peopleSelectionLifetime`) | In-flight picks fail; user searches again. Harmless. |
| Departure preview tokens | SHA-256(`fold:departure-preview:v3:` ∥ secret), `api_departure.go:386` | Short-lived preview | In-flight preview fails; user retries. Harmless. |
| Guardian-email authority tokens | HMAC over `SessionSecret`, `api_guardian_email.go:45` | Short-lived flow | In-flight confirmation fails; flow restarts. Harmless. |

Token shape today ([email_unsubscribe.go](/home/grier/git/fold/email_unsubscribe.go:26)):
one version byte (`unsubscribeVersion = 1`), uvarint user id, scope
string (`category:<name>`, validated against the optional-email
registry), 16-byte truncated HMAC-SHA256, base64url. The same token
serves two doors of very different power:

- `POST /u/unsubscribe/:token` — RFC 8058 one-click, CSRF-exempt by
  design, opts the user out of the token's own scope only
  (`oneClickUnsubscribe`,
  [email_unsubscribe.go](/home/grier/git/fold/email_unsubscribe.go:99);
  route at [main.go](/home/grier/git/fold/main.go:276)).
- `GET`/`PUT /api/v1/email-settings/:token` — the signed-out
  settings page's API (routes at `api.go:686`–687). The read returns
  the masked email, the originating category, and all switches
  (`apiTokenEmailSettings`,
  [api_email_settings.go](/home/grier/git/fold/api_email_settings.go:131)).
  The write **discards the token's scope**
  (`u, _, ok := …`,
  [api_email_settings.go](/home/grier/git/fold/api_email_settings.go:148))
  and `applyEmailSettings` will flip any listed category — or, with
  no category, every optional kind at once
  ([api_email_settings.go](/home/grier/git/fold/api_email_settings.go:72)).
  Any single old email's link is therefore a permanent capability to
  reconfigure (including re-subscribe) all of a person's optional
  mail. The footers that mint these links point at
  `/email-settings/<token>` ([email_unsubscribe.go](/home/grier/git/fold/email_unsubscribe.go:161)).

Rotation aids that exist: `fold push keys rotate [--force]`
(`runPush`, [cli.go](/home/grier/git/fold/cli.go:155);
`rotateVAPIDKeys`,
[push_keys.go](/home/grier/git/fold/push_keys.go:201)), which already
refuses to rotate under a secret that can't open the stored key and
tells the operator to restart with the new secret instead
(`errRotateOtherSecret`,
[push_keys.go](/home/grier/git/fold/push_keys.go:193)). There is no
runbook tying any of this together; `.env.example` only warns that
changing the secret turns off everyone's device notifications
([.env.example](/home/grier/git/fold/.env.example:63)).

Log hygiene is already right and must stay right: tokens in URLs are
redacted from the access log
([access_log.go](/home/grier/git/fold/access_log.go:11) —
`/u/unsubscribe/`, `/email-settings/`, `/api/v1/email-settings/`
prefixes; `TestAccessLogKeepsSecretsOut` guards it).

## Scope

In scope:

1. Dual-key verification **for emailed link tokens only**
   (unsubscribe + email-settings), using a new version byte.
2. A scoped, expiring token purpose for the email-settings page;
   the one-click token becomes single-purpose.
3. `SESSION_SECRET_PREVIOUS` config, validated like the primary.
4. The rotation runbook (docs) covering sessions, VAPID, links, and
   the short-lived capabilities, in execution order.

Out of scope, explicitly:

- **Sessions must NOT get dual-key verification.** gorilla/sessions
  can verify with old keys while signing with a new one, and this
  brief forbids using that here. The realistic reason to rotate is
  compromise; an attacker holding a signed admin cookie must lose
  it the moment the new secret boots. Members signing in again once
  is the cheap, correct price — it is also what the short-secret
  error already promises ("Changing it signs everyone out once",
  [config.go](/home/grier/git/fold/config.go:128)). The same logic
  covers the short-lived derived tokens (people-selection,
  departure-preview, guardian-email): they expire in minutes on
  their own, so dual-keying them buys nothing and widens the code
  that handles two secrets.
- **VAPID needs no dual-key and no re-encryption path.** Re-sealing
  the private key under the new secret would preserve devices, but
  a compromised-secret rotation *should* distrust the old sealing;
  the existing self-heal (new pair, devices deleted, admins
  notified, same transaction) is the approved behavior. The runbook
  documents it, including the existing recovery copy in the notify
  registry (put the old secret back to undo an accidental change).
- No new env vars beyond `SESSION_SECRET_PREVIOUS`; no schema
  change; no token table. Links stay stateless HMACs — that is why
  old mail works without the database remembering it.
- No change to calendar-feed, guest-manage, or invitation tokens:
  those are server-generated random values stored hashed in the
  database, not derived from `SESSION_SECRET`. Verify that claim
  while coding (grep for `SessionSecret` across the tree and account
  for every hit in the PR) rather than taking this brief's word.

## Slices

### Slice 1 — Token v2: purpose + optional expiry (`email_unsubscribe.go`)

- Bump the minted version to 2. Layout (all fields before the
  16-byte truncated HMAC, same signing as v1): version byte `2`,
  one purpose byte (`u` = one-click unsubscribe, `s` = email
  settings), uvarint user id, uvarint expiry unix seconds (`0` =
  never), then the scope string. Parse accepts versions {1, 2};
  minting emits only 2. Keep the KDF label `fold:unsubscribe:v1:`
  unchanged — it names the key derivation, not the token version,
  and changing it would kill the v1 tokens this slice exists to
  keep alive.
- One-click tokens: purpose `u`, expiry `0` (never). Mail clients
  POST these possibly months later, and a dead one-click button is
  a compliance problem (RFC 8058 / bulk-sender rules), not just a
  UX papercut. What changes is that a `u` token is worthless at the
  settings routes.
- Settings tokens: purpose `s`, expiry = minting time + 90 days.
  The footer (`optionalEmailFooter`) mints an `s` token per email
  for its `/email-settings/` link; the one-click URL keeps its own
  `u` token. Today both links share one token — check every minter
  (search for `unsubscribeToken(` call sites: notification delivery
  and announcement paths) and split them deliberately.
- Scope enforcement on the settings page: an `s` token still opens
  the full switches page (that is DEC-115's design — a person
  managing all their kinds in one place), but the token's recorded
  category is now authoritative for one thing: `applyEmailSettings`
  may no longer treat "no category" (flip everything) as available
  to token callers unless that stays a conscious choice. Default
  for this brief: token callers keep the all-kinds switch (it is
  the page's headline control, and the token now expires); the
  *scope* in "scoped" is the purpose byte plus expiry. State the
  choice in the PR. Signed-in Me → Notifications is a different
  door and is untouched.
- Compatibility window, stated plainly so the runbook can lean on
  it: v1 tokens are accepted on **both** doors (they predate
  purposes), under whichever key verifies (slice 2). v2 tokens are
  purpose-enforced: wrong door = the same 404 a bad signature gets
  (`parseUnsubscribeToken` failures already answer 404 and change
  nothing — keep that uniform).
- Expired `s` token: same 404, and the SPA page copy must say the
  link has expired and to use a newer email or sign in — that copy
  already has an expired/invalid state pattern on other token pages;
  match it. If distinguishing "expired" from "invalid" in the
  response risks an oracle, keep one answer and let the copy cover
  both ("this link isn't valid anymore"). No account existence is
  revealed either way: a parseable token names a user id only after
  the HMAC verifies.

### Slice 2 — Dual-key verification + `SESSION_SECRET_PREVIOUS` (`config.go`, `email_unsubscribe.go`)

- New optional env `SESSION_SECRET_PREVIOUS`, read in `LoadConfig`
  beside the primary and held as `Config.SessionSecretPrevious
  []byte` (nil when unset). Validation mirrors `sessionSecret`:
  outside dev, when set it must be ≥32 characters, with the same
  fix-it message shape (length only, never the value). Equal to the
  current secret = a startup refusal with a clear message (it is
  always a copy-paste mistake and would silently extend trust in a
  compromised key).
- `parseUnsubscribeToken` tries the current key, then — only if a
  previous secret is configured — the key derived from it (same
  `fold:unsubscribe:v1:` label). Minting always uses the current
  key. `hmac.Equal` per attempt, constant-time within each try;
  trying two keys is fine. A token that verifies under neither is
  the usual 404.
- Nothing else reads `SessionSecretPrevious`. Enforce by construction:
  no other file references the field. Add a grep-level guard test
  only if it fits the existing guard style in `guards_test.go`
  without inventing a new pattern; otherwise the PR description
  carries the grep output. `sessionSecret`'s CLI reuse
  (`fold push keys rotate` reads the primary the same way,
  [cli.go](/home/grier/git/fold/cli.go:174)) stays primary-only.
- `.env.example`: document the variable next to `SESSION_SECRET`
  ([.env.example](/home/grier/git/fold/.env.example:7)) — what it
  is for, that it accepts only emailed links signed by the old
  secret, when to remove it (the runbook's window), and that it
  never signs anything new. Update the web-push comment block
  ([.env.example](/home/grier/git/fold/.env.example:63)) only if the
  runbook wording needs it.

### Slice 3 — The rotation runbook (docs only)

Add a "Rotating `SESSION_SECRET`" section to `README.md` near the
existing upgrade notes, and a pointer line in
`docs/DEPLOY-DIGITALOCEAN.md` rather than a second copy. The README
is where the secret's other lifecycle note already lives ("must be
at least 32 characters"). The runbook, in this order:

1. **Decide and announce.** Rotation signs everyone out once and
   turns off push notifications on every device until members
   re-enable them. Pick a quiet hour; tell admins first.
2. **Prepare values.** Generate the new secret
   (`openssl rand -base64 48`). Keep the old value at hand — do not
   overwrite it yet.
3. **Set both, then restart.** In `.env` beside
   `docker-compose.yml`: `SESSION_SECRET=<new>` and
   `SESSION_SECRET_PREVIOUS=<old>`, then `docker compose up -d`.
   On boot, in order: migrations run as usual; sessions from the old
   secret stop verifying (everyone signs in again — expected, and
   the point if the old secret leaked); `ensureVAPIDKeys` finds the
   stored push key unreadable, makes a new pair, deletes every
   device, and notifies admins
   ([push_keys.go](/home/grier/git/fold/push_keys.go:145)) — do
   **not** also run `fold push keys rotate`, the restart already
   did it (its `--force` refusal copy says the same,
   [push_keys.go](/home/grier/git/fold/push_keys.go:193));
   unsubscribe and settings links from old mail keep working via
   the previous key, and new mail carries new-key tokens;
   in-flight people picks / previews from the last few minutes fail
   harmlessly.
4. **Verify, in this order.** `/healthz` answers; an admin can sign
   in with the new session; the admin bell shows the push-keys-reset
   notice (and its "if this was a mistake, put the old
   `SESSION_SECRET` back and restart" undo path — note devices
   still need re-enabling either way); an old email's unsubscribe
   button still works (test with your own mailbox or the dev
   outbox); `fold push keys show` prints a (new) public key
   ([cli.go](/home/grier/git/fold/cli.go:31)).
5. **Close the window.** After the acceptance window (default:
   13 months, or sooner if the compromise demands it — see open
   questions), remove `SESSION_SECRET_PREVIOUS` and restart. From
   that boot, links signed by the old secret stop working. There is
   no partial state to clean up: no rows were written for links.
6. **Accidental rotation.** If the secret changed by mistake (bad
   `.env` edit) and no compromise is suspected: restore the old
   value as `SESSION_SECRET`, remove the mistaken one entirely, and
   restart promptly. Sessions signed under the old value work
   again only if their cookies haven't expired; push devices are
   already gone and members re-enable notifications themselves.

CLI assist — decided **out** for this item: no `fold secret …`
subcommand. The values live in `.env` / the service environment,
which the binary must not edit, and every mechanical step already
either happens on boot or exists (`push keys show/rotate`). A
`fold doctor` that *reports* "previous secret configured since …"
belongs to R1's doctor/wizard work if anywhere; note it as a
follow-up line in the runbook PR, don't build it here.

## Data / API shapes

- Token v2 byte layout as specified in slice 1. No database or
  migration change (links are stateless). `consents` writes are
  unchanged (`setEmailConsent`,
  [email_unsubscribe.go](/home/grier/git/fold/email_unsubscribe.go:119)).
- HTTP surface unchanged: same three routes, same JSON
  (`emailSettingsJSON`,
  [api_email_settings.go](/home/grier/git/fold/api_email_settings.go:29)).
  `web/src/types.ts` untouched. The only SPA change permitted is
  the expired-link copy state on the settings page.
- `TestCSRFExemptRoutesAreExact` must stay green: `/u/unsubscribe`
  remains the one token-only POST; settings `PUT` keeps its CSRF
  token on top of the link token, as today
  ([api_email_settings.go](/home/grier/git/fold/api_email_settings.go:148)).

## Tests

Failing-first where behavior changes; table-driven around the parse
function, which is pure apart from the clock (inject `now` or sew
the expiry when minting test tokens — follow the existing seam
style rather than sleeping).

- `email_unsubscribe_test.go`:
  - v1 token still verifies (both doors) after v2 ships.
  - v2 `u` token: one-click works; settings GET/PUT answer 404.
  - v2 `s` token: settings work until expiry, 404 after; one-click
    POST with an `s` token answers 404 and writes no consent row.
  - Tampered purpose/expiry bytes fail the HMAC (they're inside the
    signed body) — flip one byte, expect 404.
  - Dual-key: token minted under secret A verifies when A is
    configured as `SESSION_SECRET_PREVIOUS` and B is current; new
    tokens mint under B only (verify by parsing under a B-only app);
    with no previous configured, the A token is 404.
  - Footer minting: an optional email's footer link and one-click
    URL carry `s` and `u` tokens respectively, with different raw
    values.
- `api_email_settings_test.go`: masked-email read and switch flips
  through an `s` token; the same through a `u` token refused;
  expired `s` token refused with no consent row written.
- `config_test.go`: previous-secret validation (unset fine; short
  refused outside dev with a length-only message; equal-to-current
  refused; value never echoed).
- Session non-regression proof for the explicit negative: a cookie
  minted under secret A does **not** authenticate when A is only the
  previous secret. One focused test in `api_auth_test.go` that boots
  two Apps over one database with the two configurations. This is
  the acceptance test for "sessions get no dual-key" — name it that.
- `TestAccessLogKeepsSecretsOut` stays green (v2 tokens travel the
  same redacted paths).

## Acceptance checklist

- [ ] Old (v1) unsubscribe link unsubscribes after the upgrade, with
      and without `SESSION_SECRET_PREVIOUS` set (same secret).
- [ ] After a rotation (new primary + old as previous), the same
      link still works; after removing the previous value and
      restarting, it 404s.
- [ ] A session cookie from the old secret never authenticates
      under the rotated configuration (named test).
- [ ] `u`-purpose tokens cannot read or change email settings;
      `s`-purpose tokens cannot one-click; expired `s` tokens 404.
- [ ] New mail's footer and one-click links carry distinct tokens.
- [ ] Runbook published in README with the deploy-guide pointer;
      `.env.example` updated; a rotation rehearsed against a dev
      database following only the runbook (record this in the PR).
- [ ] Every `SessionSecret` reference in the tree is accounted for
      in the PR description against the table in this brief.
- [ ] `go build ./...`, `go vet ./...`, `make lint-go`, `make test`
      green; web checks green if the settings-page copy moved.

## Risks / rollback / security notes

- Dual-key accepts links signed by a *compromised* old secret until
  the window closes: an attacker with the old secret could forge an
  unsubscribe or settings link for any user id. The blast radius is
  bounded — links can only change email consent and view a masked
  address — and it is strictly smaller than keeping the old secret
  as primary. The runbook's window is the control; a church that
  rotated because of a confirmed leak can set the window to zero
  (never set `SESSION_SECRET_PREVIOUS`) and accept dead links. Say
  exactly that in the runbook.
- Rollback of slices 1–2 after v2 mail has gone out breaks those
  links (old code rejects version 2). Roll forward instead; if a
  rollback is truly forced, do it before the first optional email
  batch under the new build. Slice 3 (docs) rolls back freely.
- Purpose confusion is the subtle bug class here: every route must
  name the purpose it accepts, and parse must not return a scope
  for a token whose purpose the caller didn't ask about. Prefer a
  parse API like `parseLinkToken(token, purpose)` over parse-then-
  compare at each call site.
- The previous secret must never be logged, echoed in errors, or
  accepted as a session/VAPID key anywhere. Only `email_unsubscribe.go`
  may reference the config field (slice 2).

## Dependencies

- R5 slice (b) first is cleaner (logout epoch semantics are part of
  the rotation story) but not required. Nothing here needs R1;
  the runbook cross-references its compose/DigitalOcean flow as
  shipped today. SEC1 interacts only in that rotating the secret
  kills setup sessions — irrelevant once setup is complete, which
  is the only state in which rotation makes sense.

## Open questions (with defaults)

- How long should the previous-secret window run? Default:
  13 months (a full year of annual-event mail, plus margin), stated
  in the runbook as the operator's choice with the compromise
  caveat above. The code carries no deadline of its own — an env
  the operator forgets is a docs problem, and a boot-time log line
  ("previous session secret configured; old emailed links still
  accepted") is a welcome, tiny addition if it lands naturally in
  `serve`. Default: include that log line; it contains no secret.
- Should settings tokens be category-locked as well as
  purpose-locked (a link from a group-life email flips only that
  category)? Default: no — DEC-115's page is deliberately the whole
  panel, and the 90-day expiry bounds the exposure. The token still
  records its originating category for the page's framing.
- Should v1 tokens ever be refused while the secret is unchanged?
  Default: no date is set in code. They die naturally at the first
  rotation whose window closes; forcing an earlier flag day breaks
  old mail for zero gain while the secret is uncompromised.



