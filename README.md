# Fold

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

Postgres data is the `pgdata` volume. Avatars and cover photos are the
`uploads` volume, mounted at `/data` in the app container. Rebuilding the
image keeps both. Fold runs only on PostgreSQL and refuses to start without
`DATABASE_URL`. Migrations run on start.

To run the setup wizard again, delete the users and restart the app. The
process remembers that setup finished until it exits.

## Backup

Backups stay a command you run, not another container.

```bash
docker compose exec -T db pg_dump -U church fold > fold.sql
```

The upload volume's name is `<project>_uploads` (`docker volume ls` prints
it). Copy it with a small image that has `tar`, because the app image does not:

```bash
docker run --rm -v fold_uploads:/data:ro -v "$PWD":/backup alpine \
  tar czf /backup/uploads.tgz -C /data .
```

Restore by loading the SQL dump with `psql` and extracting the archive onto
the same volume while the app is stopped.

## Local development

Requires Go 1.26+, Node 20+, and Docker (for the dev Postgres 16 database).

```bash
make dev    # starts Postgres (deploy/docker-compose.dev.yml), the API on :8080,
            # and the Vite dev server (proxies /api to :8080)
```

For a production-like local run, build the SPA first so the binary embeds it:

```bash
cd web && npm run build && cd ..
docker compose -f deploy/docker-compose.dev.yml up -d --wait db
APP_ENV=dev DATABASE_URL=postgres://fold:fold@localhost:5432/fold?sslmode=disable go run .
```

Useful env vars:

| Var | Default | Notes |
|---|---|---|
| `APP_ENV` | `prod` | `dev` enables debug logging + insecure cookie defaults |
| `PORT` | `8080` | |
| `SESSION_SECRET` | — | **Required** unless `APP_ENV=dev` |
| `DATABASE_URL` | — | Postgres DSN. **Required** everywhere |
| `FOLD_TRUSTED_PROXIES` | `172.30.0.0/24` | Proxies allowed to set `X-Forwarded-For` (the compose subnet) |
| `COOKIE_SECURE` | true in prod | Set `false` only for plain-http local testing |
| `SITE_URL` | — | Public base URL, e.g. `https://groups.example.org` |

## Tests

```bash
make test   # go test ./... on Postgres; starts the dev database if DATABASE_URL is unset
make lint   # go vet + gofmt
```

API contract: `docs/API.md`. Agent working notes: `AGENTS.md`.

## Layout

- `main.go` — wiring, setup wizard, embedded-SPA serving
- `api.go` — JSON API v1 handlers (auth, groups, memberships, meetings, RSVPs, admin)
- `csrf.go` — hand-rolled synchronizer CSRF tokens
- `config.go` — environment-based configuration
- `cli.go` — subcommands (`serve`, `healthcheck`, `migrate`, `version`)
- `db.go` — Postgres connection + migrations on start
- `migrations/` — goose SQL migrations (the schema's source of truth), embedded
- `models/` — GORM models (`User`, `Group`, `Meeting`, `MeetingException`, `Membership`, `RSVP`, `Setting`)
- `web/` — React + Vite + Tailwind SPA (builds to `web/dist`, embedded via `go:embed`)
- `templates/` — setup wizard page only (the app UI is the SPA)
- `docs/` — API contract and other docs
