# Fold plan: engineering review

Reviewer: Wright (engineer). Date: 2026-10-09. Reviewed `PLAN.md` (1,667 lines) and `SUMMARY.md` against the repo at `e7c0e8f` (pulled fresh; PR #7 is in HEAD). Read-only: nothing in the plan or repo was changed.

**Verdict: ready after must-fixes.** The architecture and the Oct 9 decisions are sound. The direction (Postgres everywhere, policy package, stored occurrences, adapters) fits the Go/Gin/GORM/React skeleton. But a few plan items contradict the actual code or won't work as written, and the first milestones carry work nothing needs yet. All of the must-fixes are plan edits. None needs a new decision from Grier except where noted.

---

## Must-fix

1. **Choose between a clean break and data migration.** Line 182 says "No church runs Fold yet", but M0.1/M0.2/M0.3/M1.1 still require stamping v0.1 databases and migrating data with fixtures (lines 1188, 1200, 1211, 1243). If no data needs keeping, make the baseline the new schema and drop those criteria, which is a big cut. If data must be kept, the plan misses that `RSVP.Date` and `MeetingException.Date` are **UTC** dates (`api_meetings.go:35,66`, `models/models.go` RSVP/MeetingException). A 7 PM Wednesday Boise meeting is keyed as Thursday, so `original_date` (5.3, line 329) has to be converted to the local date.

2. **Decide the identity model before M1.2.** `User.Username` is `unique;not null`, `Password` is `not null` and hashed in `BeforeCreate`, and login is by username (`models/models.go` User; `api_auth.go:34`). The plan keys everyone by a unique `citext` email and adds password-less guests and invitees (5.3 line 295, C5), but never says how login, usernames, or empty passwords work. Also, `Email` defaults to `""` today, so a unique index fails as soon as two users lack an email. It has to become nullable first.

3. **The definition of done can't be met.** 14.1 (line 1150) requires `npm test` in every milestone, but `web/package.json` has only `build`, `dev`, and `preview`, with no test runner. Playwright (lines 1288, 1343, 1392, 1450) isn't installed and CI has no browser step. Either add Vitest/Playwright in a named milestone or drop them from the done list until then. Otherwise every agent will invent its own setup.

4. **The dependency chain pulls the whole provider stack into v0.3.** M1.2 depends on M3.1 (line 1255), which depends on M0.4 jobs and M0.5 encrypted settings (line 1357). Yet the 14.2 table puts M3.1 in v0.4 (lines 1162–1163). Give M1.2 a minimal email sender instead: `devoutbox` plus SMTP configured by env. The registry, `ProviderConfig`, and encryption can arrive with the wizard (M4.x).

5. **The client IP will always be Caddy's.** `main.go:129` calls `SetTrustedProxies(nil)`, so behind Caddy, `ClientIP()` returns the proxy's container address. That breaks per-IP login throttling and the "10 per IP per hour" public-form limit (10.1; M1.2 line 1268), and makes `Consent.ip` and `AuditLog.ip` useless as evidence (5.3 lines 343, 354). Trust the compose network or Caddy's address explicitly (M0.1 or M1.2).

6. **Global CSRF will reject webhooks and one-click unsubscribes.** `csrfMiddleware` runs on every non-GET route after setup (`main.go:147`, `csrf.go:73`). Provider webhooks (`/hooks/*`, 7.1) and RFC 8058 one-click POSTs from mail providers (7.4 line 638; M3.3 line 1378) carry no session token and would get 403. The plan must say these routes are registered outside the CSRF group and authenticated by signature or signed token instead.

7. **The room-conflict constraint can't be created as written.** 5.3 (line 336) and 9.2 (line 734) put the padded range `tstzrange(start - setup, end + teardown)` in an exclusion constraint. `timestamptz - interval` is STABLE, not IMMUTABLE, so Postgres refuses it in an index or constraint expression. Store the padded bounds (or a `tstzrange` column) computed by the app. Also keep `RoomBooking.starts_at/ends_at` in sync when an occurrence moves (line 329).

8. **SMS opt-out handling is narrower than the FCC rule (fix before M3.6, not blocking the first slice).** 7.4 (line 645) honors only exact whole-message keywords and forwards every other reply to a leader (line 646). Since 2025-04-11, the FCC (47 CFR 64.1200(a)(10), FCC 24-24) treats "stop", "quit", "end", "revoke", "opt out", "cancel", and "unsubscribe" as revocation, plus any other reply that reasonably conveys it, and requires it be honored within 10 business days. Add REVOKE and OPT OUT, and flag likely opt-out replies to admins with a one-click opt-out. The broader "revoke for unrelated messages" piece is waived until 2027-01-31. Fold's "opt out of all SMS" choice already exceeds it.

## Should-fix

1. **Defer M0.4 (jobs) and M0.5 (secrets) until their first user.** Nothing in M0.2/M0.3/M1.1–M1.3 needs a queue or encrypted provider settings. Also drop `LISTEN/NOTIFY` (5.5 line 374; M0.4 line 1219). Polling every 1–2 s is free for one church, and LISTEN needs a dedicated pgx connection outside GORM's pool.
2. **Merge M0.2 into M1.1.** Both rewrite the `Meeting` table: M0.2 adds `starts_local` and `tzid`, then M1.1 renames it to Event and adds occurrences (lines 1195–1252). One migration and one set of DST tests is simpler.
3. **The schema has two sources of truth.** 5.2 (line 256) calls the GORM models "source of truth for schema shape", while C1 (line 192) makes goose SQL authoritative. Say the migrations are the truth, and add a CI check that `AutoMigrate` in GORM `DryRun` on a migrated database emits no DDL.
4. **Leadership has two sources of truth.** C12 keeps `Group.LeaderID` and adds `Membership.role=leader` (line 204). The code checks `g.LeaderID` in 16 places in `api_*.go`, for example `api_groups.go:543`. M0.3 should make policy use membership role only, keep `LeaderID` as a display contact, and backfill leader memberships.
5. **Series-edit rules are underspecified.** Line 329 ("moved with the series", "flagged") doesn't say whether edits apply to all dates, this date only, or this and following ones. It also doesn't say what happens to bookings when a time change hits a conflict. For v1, use "edit the whole future series" plus per-date overrides, and write that down before M1.1.
6. **Several milestones are too big for one session.** Candidates to split: M1.2 (guest flow, four account doors, approval queue, password reset, throttling; line 1256), M1.7 (five tables plus consent emails plus the rollover job), M3.6 (Telnyx and Twilio, keywords, send windows, caps, opt-in; line 1408), and M4.1 (wizard, legal pages, CSV import). Build Twilio after Telnyx, as its own milestone.
7. **Backups and upgrades don't fit the distroless image.** `fold backup now` (11.4 line 925; 11.5 line 939) can't run `pg_dump`, because the final image is `distroless/static` (`Dockerfile`). Use `docker compose exec backup backup.sh`. The "refuse to migrate without a backup newer than 24 h" check (line 957) blocks a first install and needs the backups volume mounted in the app. Drop "read-only maintenance mode" (line 958): exit non-zero, show `/healthz` as unhealthy, and let the operator restore.
8. **Wizard placement and gating are unresolved.** Today's wizard is a server-rendered `templates/setup.html`, gated by `setupRequired` and a cached "any user exists" flag (`main.go:241–300`). A resumable 9-step wizard (11.3; M4.1 line 1446) runs after the admin account exists. The plan should say whether it lives in the SPA, and change the gate to stored setup state.
9. **The email default conflicts with "cost-conscious".** 12.7 (line 1075) defaults to Postmark at $15/mo past 100 emails, which every church will exceed. Settle open question 5 (line 1580) before M4.2. SMTP first (already in M3.1) avoids blocking.
10. **SMS registration facts come from Twilio, but Telnyx is the default.** The Charity/religious exclusion, the June 30, 2026 privacy/terms URL rule, and the sole-proprietor limits (10.4 lines 839–842; 12.2 lines 1018–1020) are cited from Twilio docs. Verify the Telnyx equivalents before M4.3 hard-codes wizard text.
11. **Two decisions are recorded more broadly than stated (confirm with Grier).** First, Postgres-only: the decision as stated covers *production*, but DEC-1 (line 1555) extends it to dev and tests as "plan recommendation, adopted". I agree with it, since CI already runs Postgres (`.github/workflows/test.yml`). Second, DEC-9 (line 1563) makes the 8 AM–8 PM window *recipient-local*, while the decision as relayed says only "default 8–8, configurable". Recipient-local matches TCPA's called-party rule, so keep it if Grier agrees.
12. **Build one real adapter per kind first.** Goal 6 (line 46) asks for at least two implementations per proprietary dependency, and 5.6 lists six email adapters. Build `devoutbox` plus one real adapter per kind, and add the second when a church needs it. The interface already keeps that cheap (D8 is satisfied by the seam).

## Nits

- M3.1 (line 1361) gates `/dev/outbox` on `FOLD_ENV=production`, but the code uses `APP_ENV` with `dev` or `prod` (`config.go`).
- `TEST_DATABASE_URL` (lines 205, 977) differs from the `DATABASE_URL` that `auth_test.go:65` and CI use today. Pick one.
- M2.3 says "All 10 cases" (line 1349), but the 9.3 table has 12 (lines 782–793).
- 11.1 (line 864) renames the volumes `uploads`/`caddydata` to `appdata`/`caddy_data` (`docker-compose.yml`). The renames add churn for nothing, so keep the existing names.
- On teen consent, Grier's wording is "parent/guardian/admin, not a leader". The plan's "a leader only if they are the teen's guardian" (lines 477, 652, 827, 1562) has the same effect. Phrase it as "guardians and admins; the leader role never grants consent".
- Section 6 (line 544) and 5.3 (line 319) don't say whether a non-admin host may change a church-wide event between `public` and `private`.
- `LoadConfig()` runs before argument dispatch and exits fatally without `SESSION_SECRET` or `DATABASE_URL` (`main.go:47`, `config.go:66–77`). `fold version` and `migrate status` shouldn't need the session secret.
- M0.3 changes the API shape. `inviteOnly` is in `groupSummaryJSON` (`api.go`) and `web/src/types.ts:31`, so the milestone should mention the SPA update along with `docs/API.md`.
- There's no `Makefile` in the repo. Adding one is fine, but M0.1 should list it as new.

## Suggested first slice

M0 and M1 are the right place to start, but trimmed:

1. **M0.1-lite:** delete SQLite (`db.go`, `config.go`, `auth_test.go`, the Dockerfile `SQLITE_PATH`, the CI SQLite job), add goose with a baseline equal to the current models (clean break, no v0.1 stamping, unless Grier has data to keep), add `serve`/`healthcheck`/`migrate`, configure trusted proxies, and fix AGENTS.md (Twilio line, test rules, Postgres flags) and the README Go version. Remove `npm test` from the done list for now.
2. **M0.3:** the visibility enum, `internal/policy` with table tests for today's rules, and co-leaders by membership role.
3. **M1.1 with M0.2 folded in:** Event with `starts_local` and `tzid`, stored occurrences, church-wide events with hosts, the public override with audit, and the series-edit rule written down first.
4. **M1.2a then M1.2b:** email via env-configured SMTP plus `devoutbox`, password reset, and admin/leader invites; then self-signup and member requests. Guests and teen consent come after.

Leave jobs, encrypted provider settings, the wizard, and SMS until a milestone actually needs them.

---

## Final review (Oct 9, 2026)

Re-reviewed `PLAN.md` draft v3 (2,010 lines) against the repo at `e7c0e8f` (pulled fresh; already up to date). Read-only: no plan or repo edits, no GitHub writes, no servers started.

### Must-fix status

| # | Must-fix | Status | Where (PLAN.md lines) |
|---|---|---|---|
| 1 | Clean break vs. data migration; UTC date keys | **Resolved**: DEC-22 clean break; no stamping, no fixtures, no conversion | 11, 55, 197, 203, 299, 1319, 1356, 1406, 1833 |
| 2 | Identity model | **Resolved**: email login, `citext` NOT NULL unique, nullable `password_hash`, no `username`, sign-in rules; M0.3 owns it | 114, 181, 222 (C16), 309–320, 1383–1391 |
| 3 | `npm test` / Playwright didn't exist | **Resolved**: M0.4 adds Vitest and Playwright; done rule gated "from M0.4 on" | 112, 195, 1322, 1393–1399 |
| 4 | M1.2 → M3.1 → jobs and secrets chain | **Resolved**: `internal/mail` (devoutbox and env SMTP); M1.2 depends only on M1.1 and M0.3; jobs moved to M3.1, secrets to M4.1; release table consistent | 212–214, 470, 1334–1337, 1423–1425 |
| 5 | Trusted proxies | **Resolved**: C17, fixed compose subnet, `FOLD_TRUSTED_PROXIES`, test with a fake proxy | 115, 223, 888, 979, 994, 1358, 1368 |
| 6 | CSRF blocking hooks and one-click unsubscribe | **Resolved**: exempt route groups in M0.1, with a test that the list is exact | 183, 223, 672, 715, 889, 1359, 1369 |
| 7 | Exclusion constraint on a non-immutable expression | **Resolved**: app-computed `padded_range` column, `CHECK` for consistency, partial `EXCLUDE`, bookings recomputed when occurrences move | 362, 369, 1526 |
| 8 | FCC opt-out scope | **Resolved, and beyond the rule**: REVOKE and OPT OUT added; plain-language opt-outs applied automatically (DEC-27); one confirmation text; opt-out log | 722–727, 928, 1838 |

Should-fixes and nits were also checked. They are applied: polling instead of LISTEN/NOTIFY (409), merged time-zone work (1407), the DryRun rule (207), leadership by membership (218), the series-edit rule (359–362), split milestones, the backup container (1053, 1080–1081), the `SetupState` gate (304, 1008), `APP_ENV` (1427), `DATABASE_URL` (1104), 13 room-suggestion cases (1546), and kept volume names (978).

### Security findings from removing the gates (DEC-30)

The account gate stayed: self-signup and member invites still need admin approval (321, 509–513, 949). That kills open spam sign-ups and limits cost blowups from open sign-up plus SMS. What changed is that *approved* accounts now join groups and register for events with no check (336, 402, 581). These holes follow from that or sit next to it, and 10.6 (945–965) doesn't cover them:

1. **A leader can make another leader without the admin.** Section 6 lets a leader "make someone a group leader: own groups, adults only" (608). Group creation needs admin approval (605), but promotion doesn't. Any vetted leader can hand leader rights, including teen contact details (599, 918) and direct messaging of teens (627), to any adult the admin never vetted. Fix: admin approval or notice-plus-audit for leader grants, at least in groups with teens. Decide before M0.2 encodes the row.
2. **A stranger can become a teen's auto-confirmed guardian.** Guardian links auto-confirm when "the guardian creates the teen account … from their own household" (331, 916). An adult can create a household teen, turn it into an account with a real teen's email (592, 1503), and the teen accepts the invite. The adult is then a confirmed guardian who can grant consent, see contact details and receive copies. Fix: a teen account made from a household person needs admin confirmation of the guardian link (or at least an admin notice), the same as claiming an existing teen (594).
3. **Opening an invite-only group's event leaks its members.** When a leader makes such an event `private` (DEC-17), every member can see "adult attendees' names" (616). In practice, those are the hidden group's members, for example a recovery or divorce-care group. The event page also names "the event's leaders" (347). Fix: for events more open than their group, attendee names go to leaders and admins only, and the change dialog warns about this.
4. **Account enumeration.** "This email already has an account; sign in or reset your password" (321, 1440) tells anyone whether an email belongs to a church member, on a public form. Login and reset responses aren't specified either. Fix: send the same response every time and email the hint instead.
5. **SMS opt-in has no phone ownership check.** Opt-in is a profile or sign-up checkbox (720), and `phone_verified_at` exists (318) but nothing requires it. A member or guest can opt in someone else's number, which carries TCPA exposure and real cost. Fix: double opt-in (reply YES, or a one-time code) before `Consent` becomes `opted_in`. This is an M3.7 item.
6. **Cost and abuse limits are missing in a few places:**
   - No cap on HELP and confirmation auto-replies per inbound number (722).
   - No per-leader limit on invitations, which skip approval (588). Each one is an email from the church domain to any address.
   - Guest verification emails are limited only per IP (893, 1443), not per target email, so one address can be email-bombed.
   - Add per-recipient and per-actor limits to 10.6.
7. **The shared-email household concentrates risk.** One mailbox controls every household member's registrations and children's care notes (530–538). The plan has no rules for verifying an email change or invalidating sessions on email change, role change or disable. Add re-verification to both the old and new address, and session revocation, to 10.6 (M0.3/M1.2). Admin 2FA (951) lands in v1.0, after v0.5 is "the first version a church can run" (1337). Given Grier's stance, move TOTP to v0.5.
8. **Guests are fine as specified.** They get a verified email, an 18+ tick and a manage-link token (577, 1448). They can't see private content and are never in the directory.

Minors are otherwise sound: no under-13 accounts, consent from guardians and admins only, badges, `TeenConsent` plus a teen's own opt-in for SMS. Visibility is otherwise sound too: 404 for invite-only groups, and policy table tests.

### M0/M1 sizing and order

- **M0.1 is ready to code as written** (1351–1371). It's concrete, testable and matches the skeleton (`db.go`, `config.go`, `main.go:47,129,147`, `auth_test.go`, `.github/workflows/test.yml`).
- **M0.2 is well-sized** (1373–1381).
- **M0.3 and M0.4 are well-sized**, but 1347 says M0.3 can run in parallel with M0.2. Both rewrite handlers and the test helpers (`newTestUser` in `auth_test.go` uses `Username`), so run them in sequence (M0.2, then M0.3) to avoid merge pain. M0.4 can run in parallel safely.
- **M1.1 is still the heaviest milestone** (1403–1421): the rename, time zones, occurrences, series edits, church-wide hosts, visibility with audit, and notifications. Consider splitting it into M1.1a (Event, time zones, occurrences, series edits) and M1.1b (hosts, visibility, audit, notifications).
- **M1.2 to M1.10 are reasonably sized and ordered.** M1.4 (guests) depending on M1.3 rather than M1.5 is fine.

### New issues and contradictions

- **The GORM DryRun drift check probably can't work as written** (207, 1355, 1364). GORM's migrator queries the database (`HasTable`, `ColumnTypes`), and in `DryRun` those queries don't run, so it either reports false drift or fails. Fix: in a throwaway clone of the migrated test database, run `AutoMigrate` for real with a logger that records every statement, and fail on any `CREATE` or `ALTER`. Settle this in the M0.1 PR.
- **No milestone creates the `AuditLog` table,** yet 10.6 says "M0.2 onward" (960) and M1.1 writes audit rows (1416). Name M0.2 as the owner.
- **M1.1 "notifies the other leaders and admins"** (1416), but it ships before email (M1.2), and notifications are derived, not stored, today (AGENTS.md). Specify the channel: in-app derived notifications in M1.1, plus email once M1.2 lands.
- **The header and traceability still say "DEC-1 to DEC-25"** (3, 10, 98), but the decisions run to DEC-30 (1808–1841). F1 (84) still says self-signup needs approval, which is correct, but doesn't cite DEC-30.
- **The M1.5 title still says "approvals"** (1455), which contradicts DEC-30's no-approval rule (402, 1463).
- No other contradictions found. DEC-4 and DEC-30 agree (account approval stays, workflow gates go), and DEC-20, DEC-25, DEC-26 and DEC-27 are carried through 5.10, 7.4–7.5, 11.1 and 12.5.

### Final verdict

**code-ready**

Nothing blocks starting M0.1. Fix these before the milestones that encode them:
- The DryRun mechanism (in M0.1).
- The leader-promotion rule and `AuditLog` ownership (before M0.2).
- The teen guardian auto-confirm and the leak through opened invite-only events (before M1.1 and M1.9/M1.10).
- Enumeration, email-change and rate-limit additions to 10.6 (before M0.3/M1.3).
- SMS double opt-in (before M3.7).
