# AGENTS.md — working instructions for AI agents on this repo

Working title: **Fold**. Church small-group / ministry / activity management.
The group-management slice of ChurchTrac, without the rest of it.

## The one rule

**Deployment simplicity is the top concern.** Whatever you build, a church
volunteer (or a single SaaS operator) must be able to run it with
`docker compose up`. The shipped artifact is always a **single Go binary**
that serves both the JSON API and the frontend (the SPA bundle is embedded
via `go:embed`). No separate frontend hosting. Ever.

## Stack

- **Go 1.25**, Gin (JSON API under `/api/v1`), GORM
- **Postgres** for anything real (`DATABASE_URL`); **SQLite** dev-only
  (pure-Go `glebarez/sqlite` driver — no CGO, keep it that way)
- Postgres-only features (tsvector search, LISTEN/NOTIFY, advisory locks)
  go behind feature flags so the SQLite dev path keeps working
- **React + Vite + Tailwind** SPA in `web/`; `web/dist/` is embedded into
  the binary. Release pipeline runs `npm run build` before `go build`
- **No Python.** Not for tooling, not for scripts. Shell or Go.

## Architecture

```
browser ──► single Go binary ──► /api/v1/* (JSON, cookie auth)
              │                    /* (embedded SPA, index.html fallback)
              └──► Postgres (prod) / SQLite (dev)
```

- Single-tenant app. "SaaS" means many single-tenant deployments behind a
  proxy — there is deliberately **no multi-tenant data model**.
- Auth: cookie sessions + bcrypt (local accounts). OIDC (Microsoft Entra ID
  + Google) comes later — keep an `AuthProvider` seam in mind, don't build it yet.
- CSRF: hand-rolled synchronizer token (`csrf.go`). Every POST/PUT/DELETE
  needs the `csrf_token` form field or `X-CSRF-Token` header. It was
  deliberately NOT gorilla/csrf: v1.7's Origin/Referer checks assume TLS
  end-to-end and break plain-HTTP dev.
- Approval workflow is a core feature, not an afterthought: new groups enter
  `pending` until staff approves. Keep it working through every refactor.

## Data model (Phase 1+)

`User` → `Membership` → `Group` → `Meeting`; `RSVP` links users to meeting
instances. See `models/models.go` — it is the source of truth. The old
"group with one datetime" shape is gone; groups are ongoing entities,
meetings are instances (RRULE for recurrence).

`User` carries profile fields: `Name` (full name), `DisplayName` (shown in
the UI), `Email`, `Phone`, `AvatarKey` (blob-store key, not a URL),
`AvatarFocalX/Y` (0-100 focal point for cropped avatars).
`User.PublicName()` is the display fallback chain
(DisplayName → Name → Username) — use it anywhere a name is rendered.
`Group` has `CoverFocalX/Y` for the cover image. Focal points are stored
0-100 and rendered as CSS `object-position`.

## Conventions

- API is versioned: `/api/v1/...`. JSON uses camelCase keys.
- Authenticated endpoints return 401 JSON (never a redirect — the SPA
  handles it). The old server-rendered redirect behavior is gone except
  for `/setup`.
- Errors: `{"error": "human readable"}` with the right status code.
- Migrations: GORM `AutoMigrate` for additive changes; versioned SQL in
  `migrations/` for anything destructive (to be added when first needed).
- Tests: `go test ./...`. Handler tests use `httptest` + in-memory SQLite
  (see `auth_test.go` for the cookie-jar + CSRF-dance pattern — reuse it).
- `go vet ./...` must be clean before any PR.
- UI reference is **Meetup** — familiar beats original. Copy its patterns
  (group cards, date badges, join buttons); tweak later.
- Commit messages: short imperative summary; PRs describe behavior change.
- Setup/form UX: state every input requirement next to the field (e.g.
  password minimums). On a failed submission, re-render with the valid
  fields preserved — never echo back secrets (passwords) or payment data.
- Client-side image prep: `web/src/lib/image.ts` resizes uploads in the
  browser (`browser-image-compression`) before POST — keeps big-phone-photo
  CPU off the server. Server still enforces type/size limits.
- Notifications are derived, not stored (`GET /api/v1/notifications`):
  approvals for admins, upcoming events for members. Dismissals live in
  the client's localStorage.

## Uploads & blob storage

- User-uploaded assets (avatars, group covers) go through the
  `storage.Store` interface (`storage/storage.go`), never raw filesystem
  paths in handlers. `App.Uploads` is the instance.
- The local implementation (`storage.LocalStore`) ships now:
  `UPLOAD_DIR` (`./data/uploads` default, `/data/uploads` in compose),
  served at `/uploads/<key>` with `nosniff`. Keys are server-generated —
  never trust client filenames. Uploads are content-sniffed
  (JPEG/PNG/GIF/WebP only), ≤ 5MB.
- **S3 seam:** to move to S3-compatible storage, implement
  `storage.Store` (Put → bucket, URL → presigned/public object URL,
  Delete/Exists → bucket ops), wire it in `main()` from new env config,
  and change `serveUpload` to redirect instead of serving files. No
  handler changes needed.
- Docker: the final image pins `WORKDIR /` (distroless doesn't guarantee
  it) and pre-creates `/data/uploads` with `--chown=65532:65532` so the
  nonroot app can write to the fresh named volume.

## Development

```bash
# Backend (SQLite dev DB, debug logging)
APP_ENV=dev go run .

# Frontend
cd web && npm install && npm run dev      # Vite dev server, proxies /api to :8080
npm run build                              # outputs to web/dist (embedded)

# Full stack
go test ./...
```

Env vars: see `config.go` and `.env.example`. `SESSION_SECRET` is required
unless `APP_ENV=dev`.

## Deployment

- `docker compose up --build -d` → app + Postgres 16 + Caddy (auto HTTPS).
  Fill `.env` from `.env.example` first.
- Dockerfile is multi-stage (node → go → distroless), `CGO_ENABLED=0`.
  Keep it that way — the binary must build without a C compiler.
- First boot with an empty DB serves the setup wizard at `/setup`.
- Known gap: the Postgres code path is compile-verified but has never run
  against a live Postgres in this environment (no Docker/Postgres here).
  Validate with `docker compose up` on a real machine before calling it done.

## SMS (Phase 2, not yet)

Group SMS via Twilio is a hard requirement, but it is **not built yet**.
When you build it: put it behind a `Notifier` interface (`internal/notify/`),
broadcast-only v1, replies route to the group leader. Don't start early.
