# Fold

Small-group / ministry / activity management for churches — the
group-management slice of ChurchTrac, without the rest of it.

Single Go binary serves a JSON API and an embedded React SPA (Meetup-style
UI). Deployments stay one artifact: `docker compose up`.

## Quick start (Docker Compose — recommended)

```bash
cp .env.example .env
# edit .env: set DOMAIN, SESSION_SECRET (openssl rand -base64 48; at least 32 characters), DB_PASSWORD
docker compose up --build -d
```

Open `https://<DOMAIN>`. The first visit shows a setup wizard to create the
church site and its admin account.

A single DigitalOcean droplet is the recommended production install.
`docs/DEPLOY-DIGITALOCEAN.md` and `deploy/digitalocean/provision.sh`
harden Ubuntu and start this Compose stack. `smoke.sh` checks health,
TLS, and that Postgres is not reachable from the internet. The script
never creates droplets or other DigitalOcean resources.

Postgres data is the `pgdata` volume. Avatars and cover photos are the
`uploads` volume, mounted at `/data` in the app container. Rebuilding the
image keeps both. Fold runs only on PostgreSQL and refuses to start without
`DATABASE_URL`. Migrations run on start.

To run the setup wizard again, delete the users and restart the app. The
process remembers that setup finished until it exits.

## Upgrade notes

- **Oct 2026 (PR #44): `SESSION_SECRET` must be at least 32 characters.** An older
  install with a shorter secret will not start. `docker compose logs app` shows
  one line naming the problem and its length (never the value). Fix: run
  `openssl rand -base64 48`, put the result in `.env` as `SESSION_SECRET=...`,
  and run `docker compose up -d`. Everyone is signed out once.

## Backup

Current backups use commands or the DigitalOcean host-cron helper. A portable
backup container is planned in M5.2, not installed.

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

On a droplet, `/opt/fold/backup.sh` does the nightly `pg_dump` and an
optional encrypted offsite copy. See `docs/DEPLOY-DIGITALOCEAN.md`.

## Local development

Recommended: the Go toolchain pinned in `go.mod`, Node at the major in `.nvmrc`,
and Docker (for the dev Postgres 16 database).

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
| `SESSION_SECRET` | — | **Required** unless `APP_ENV=dev`; at least 32 characters |
| `DATABASE_URL` | — | Postgres DSN. **Required** everywhere |
| `FOLD_TRUSTED_PROXIES` | `172.30.0.0/24` | Proxies allowed to set `X-Forwarded-For` (the compose subnet) |
| `COOKIE_SECURE` | true in prod | Set `false` only for plain-http local testing |
| `SITE_URL` | — | Public base URL, e.g. `https://groups.example.org` |

### Schema downgrades

`fold migrate down-one` is an offline, quiesced maintenance command. Stop every
Fold app instance and worker before running it, and keep membership writes
stopped until the rollback is complete or the database has been re-upgraded.
Do not use schema downgrade as a live rollback path. This pre-release milestone
has no live users; if a rollback guard refuses a test database, reset that test
database instead. Production rollback restores the pre-upgrade backup as
specified in `docs/DEPLOY-DIGITALOCEAN.md`.

## Tests

```bash
make test   # go test ./... on Postgres (root package sharded); starts the dev database if DATABASE_URL is unset
make lint   # shell, Go (vet/gofmt/staticcheck), and web lint
make deadcode # production reachability gate; test database and registry inspection seams are allow-listed
cd web && npm test   # Vitest component tests
make e2e    # Playwright browser tests in parallel shards (first run: cd web && npx playwright install chromium)
```

## Documentation

- [PLAN.md](docs/plan/PLAN.md): product decisions and milestone scope (source of truth).
- [STATUS.md](STATUS.md): merged work and remaining follow-ups.
- [Summary](docs/plan/SUMMARY.md): shipped versus planned features.
- [Documentation review](docs/plan/DOCUMENTATION-REVIEW.md): unresolved conflicts and implementation gaps.
- [API contract](docs/API.md), [member guide](docs/MEMBERS.md), and
  [DigitalOcean deployment](docs/DEPLOY-DIGITALOCEAN.md).
- [AGENTS.md](AGENTS.md): development and review rules.

Set `FOLD_SMTP_URL` and `FOLD_EMAIL_FROM` in `.env`, then recreate the app
with `docker compose up -d` to enable transactional email. Follow the deployment
guide to verify delivery before relying on invitations or password resets.

## Layout

- `main.go` — wiring, setup wizard, embedded-SPA serving
- `api.go`, `api_*.go` — JSON API v1 wiring and handlers
- `csrf.go` — hand-rolled synchronizer CSRF tokens
- `config.go` — environment-based configuration
- `cli.go` — subcommands (`serve`, `healthcheck`, `migrate`, `version`)
- `db.go` — Postgres connection + migrations on start
- `migrations/` — goose SQL migrations (the schema's source of truth), embedded
- `models/` — GORM models (`User`, `Group`, `Event`, `EventOccurrence`, `Membership`, `Registration`, `Setting`)
- `web/` — React + Vite + Tailwind SPA (builds to `web/dist`, embedded via `go:embed`)
- `templates/` — setup wizard page only (the app UI is the SPA)
- `docs/` — API contract and other docs
