# Fold milestones: Phase 0

### 14.3 Phase 0: Foundations

Phase 0 is done (v0.2) and merged before Phase 1. It delivered the Postgres-only baseline, the CLI and request plumbing, the policy package and audit log, email login, member removal with sole-leader handoff, security headers, and the frontend test tooling. The live rules are in sections 5–10 and the decisions in section 15; the code and tests are the source of truth. [STATUS.md](../../../STATUS.md) records delivery.

**M0.1 PostgreSQL only, clean baseline, CLI, request plumbing**
- Delivered: SQLite is gone (C14, DEC-1, DEC-24). Dev and tests use `DATABASE_URL`; `deploy/docker-compose.dev.yml` and the `Makefile` (`dev`, `test`, `lint`) start the dev database (11.9). goose runs embedded SQL migrations from a clean baseline (DEC-22) under a Postgres advisory lock, so two containers never migrate together.
- The CI drift check (C1) migrates a throwaway database with goose, runs `AutoMigrate` with a statement-recording logger, and fails on any `CREATE` or `ALTER`. A self-test proves the check works. It does not use GORM's `DryRun`.
- CLI: `serve`, `healthcheck`, `migrate`, `version`, with lazy config: `version` needs no env and `migrate status` needs only `DATABASE_URL` (C11).
- Request plumbing (C17): the compose network has a fixed subnet that is the only default trusted proxy (`FOLD_TRUSTED_PROXIES`). `/hooks/*` and `POST /u/unsubscribe/<token>` sit outside the CSRF middleware, and a test pins the exempt list.

**M0.2 Visibility enum, policy package, leadership by membership, audit log**
- Delivered: one `visibility` enum on groups (C3) and `internal/policy`, which holds every permission check. Leadership comes only from `Membership.role=leader`; `Group.LeaderID` is the displayed primary contact only (C12). Invite-only groups are 404 to non-members, and admins see every group (DEC-5).
- A group can have several leaders, the last leader can't be removed, and a teen can't be made a leader (DEC-18; the policy rule came first, the teen accounts in M1.10).
- The `AuditLog` table (5.3) and the one `audit.Write` helper. Making or removing a leader writes `membership.role_changed` and shows admins an in-app notice, with no admin gate (DEC-31).

**M0.3a Log in by email**
- Delivered (C16, DEC-23): the email is the login (`citext`, unique, no username) and `password_hash` is nullable. Sign-in and the first-admin page use email in any letter case.
- Throttling is keyed by email and by client IP and stored in Postgres: ten failures lock an email for 15 minutes across IPs, fifty lock an IP. An unknown email and a wrong password answer alike. Changing a password or role, or disabling an account, bumps `session_version` (10.1).

**M0.3b Leaders remove members, sole-leader handoff, security headers, dependency scanning**
- Removal (DEC-35): `DELETE /api/v1/groups/:id/members/:userId` (optional `{note}`) and the admin twin `DELETE /admin/groups/:id/members/:userId`. `CanRemoveMember` and `CanChangeRole` decide: admins remove anyone, leaders only from groups they lead, nobody removes the last leader, and a leader can't remove or demote an admin. Leaving is `POST /groups/:id/leave` with the same rules. Removal sets `status=removed` and keeps the row. The person can't rejoin alone (403 "A leader removed you from this group"), and a leader or admin can add them back. It writes `membership.removed` (the note stays in the audit row, never shown to the person) and gives admins the `leadership` notice when a leader goes.
- Sole-leader handoff (DEC-36): `GET /api/v1/admin/users/:id/sole-leader-groups` lists groups where the account is the only active leader, with candidate successors (the group's other active adult members and every admin). `DELETE /api/v1/admin/users/:id`, and disabling, take `{successors: {"<groupId>": <userId>}}` and refuse with 400 and the list when one is missing. One transaction makes the successors leaders, then deletes or disables. Audit rows are `membership.role_changed` per group (reason `account_deleted` or `account_disabled`) and `user.deleted` or `user.disabled`. `leaderCount` counts only active accounts.
- Disabled accounts (DEC-38): memberships and registrations are kept but hidden and never counted. Enabling restores them as plain member (`membership.restored`, `user.enabled`). In a full group the person is left out, `membership.not_restored` is written, and they get a notice to ask a leader (DEC-47); memberships are `active`, `removed` or `left` (DEC-86). The members JSON has an `admin` flag, and Remove is hidden for admins unless the viewer is an admin.
- Member limit (DEC-39, 5.4): nullable `groups.member_limit` (`CHECK > 0`) and `policy.GroupHasRoom`. Join, the leader's add and the admin add share one path that locks the group row. A full group answers self-join 409 "This group is full"; leader and admin adds get 409 with `full: true` unless the body has `overLimit: true`, which writes `membership.added_over_limit`. Lowering the limit below the count keeps everyone. Only active memberships of listed accounts count.
- Security headers (DEC-37, 10.1, 10.6): the app sets CSP (`default-src 'self'`, no `unsafe-inline`; `img-src 'self' data: blob: https:`, where `blob:` is for upload previews and `https:` for cover images by URL), HSTS when `COOKIE_SECURE` is on, `nosniff`, `Referrer-Policy: same-origin`, `X-Frame-Options: SAMEORIGIN` and a `Permissions-Policy` that turns off camera, microphone and geolocation. `/setup` works under the same CSP. The image compressor runs on the main thread because its web worker loads code from a CDN.
- Dependency scanning: Dependabot updates Go, npm, GitHub Actions and Docker base images weekly. CI runs a pinned `govulncheck ./...` and `npm audit --omit=dev --audit-level=high`.
- Left for M4.7: tighten `img-src` once uploads are the only image source. Left for M5.1: Caddy and image-level headers, and pinned image digests in releases.

**M0.4 Frontend and browser test tooling**
- Delivered: Vitest with Testing Library (`npm test`), Playwright with a smoke test under `web/e2e` (sign in, see the groups page), `make e2e`, and a CI job that installs Chromium and runs both.

