# Fold milestones: Phase 0

### 14.3 Phase 0: Foundations

Phase 0 (v0.2) is done. The full as-delivered text is in [provenance/delivered-phases-0-2.md](../provenance/delivered-phases-0-2.md); the design sections and the code are the current truth.

**M0.1 PostgreSQL only, clean baseline, CLI, request plumbing**
Done (PR #9).
- PostgreSQL is the only database, in development, tests and production; goose runs the embedded migrations at start under an advisory lock (DEC-1, DEC-22, DEC-24, 11.9).
- A CI drift check fails when the GORM models want a schema change the migrations don't make (C1).
- The `fold` CLI: `serve`, `healthcheck`, `migrate`, `version` (11.4, C11).
- Trusted proxies and the exact CSRF exemption list (10.1, C17).

**M0.2 Visibility enum, policy package, leadership by membership, audit log**
Done (PR #10).
- One `visibility` value per group, and every permission check in `internal/policy` (C3, section 6).
- Leadership comes only from `Membership.role=leader`; a group may have several leaders (C12, DEC-18).
- The `AuditLog` table and `audit.Write`; leader changes are audited and shown to admins (DEC-31).

**M0.3a Log in by email**
Done (PR #12).
- The email is the login, with no username (C16, DEC-23).
- Login throttling by email and by client IP, stored in Postgres (10.1).

**M0.3b Leaders remove members, sole-leader handoff, security headers, dependency scanning**
Done (PR #13).
- Leaders and admins remove members, and anyone may leave (DEC-35).
- Deleting or disabling a group's only leader first hands each group to a chosen successor (DEC-36); disabled accounts and the group member limit follow DEC-38, DEC-39 and DEC-47.
- App security headers, including the CSP (DEC-37, 10.1).
- Dependabot, `govulncheck` and `npm audit` in CI (10.6).

**M0.4 Frontend and browser test tooling**
Done (PR #22).
- Vitest with Testing Library, Playwright under `web/e2e`, `make e2e`, and both in CI ([docs/agents/testing.md](../../agents/testing.md)).
