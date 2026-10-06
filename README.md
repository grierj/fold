# Church Groups (working title: Fold)

Small-group / ministry / activity management for churches — the
group-management slice of ChurchTrac, without the rest of it.

Single Go binary serves a JSON API and an embedded React SPA (Meetup-style
UI). Deployments stay one artifact: `docker compose up`.

## Quick start (Docker Compose — recommended)

```bash
cp .env.example .env
# edit .env: set DOMAIN, SESSION_SECRET (openssl rand -base64 32), DB_PASSWORD
docker compose up --build -d
```

Open `https://<DOMAIN>`. The first visit shows a setup wizard to create the
church site and its admin account.

## Local development

Requires Go 1.24+ and Node 20+.

```bash
# Terminal 1 — backend (SQLite dev DB, API on :8080)
APP_ENV=dev go run .

# Terminal 2 — frontend dev server (proxies /api to :8080)
cd web && npm install && npm run dev
```

For a production-like local run, build the SPA first so the binary embeds it:

```bash
cd web && npm run build && cd ..
APP_ENV=dev go run .
```

Useful env vars:

| Var | Default | Notes |
|---|---|---|
| `APP_ENV` | `prod` | `dev` enables debug logging + insecure cookie defaults |
| `PORT` | `8080` | |
| `SESSION_SECRET` | — | **Required** unless `APP_ENV=dev` |
| `DATABASE_URL` | — | Postgres DSN; when set, Postgres is used instead of SQLite |
| `SQLITE_PATH` | `./data/church-groups.db` | SQLite file (dev) |
| `COOKIE_SECURE` | true in prod | Set `false` only for plain-http local testing |
| `SITE_URL` | — | Public base URL, e.g. `https://groups.example.org` |

## Tests

```bash
go test ./...
```

API contract: `docs/API.md`. Agent working notes: `AGENTS.md`.

## Layout

- `main.go` — wiring, setup wizard, embedded-SPA serving
- `api.go` — JSON API v1 handlers (auth, groups, memberships, meetings, RSVPs, admin)
- `csrf.go` — hand-rolled synchronizer CSRF tokens
- `config.go` — environment-based configuration
- `db.go` — Postgres/SQLite selection + migrations
- `models/` — GORM models (`User`, `Group`, `Meeting`, `MeetingException`, `Membership`, `RSVP`, `Setting`)
- `web/` — React + Vite + Tailwind SPA (builds to `web/dist`, embedded via `go:embed`)
- `templates/` — setup wizard page only (the app UI is the SPA)
- `docs/` — API contract and other docs
