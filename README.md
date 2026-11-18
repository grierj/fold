# Fold

Small-group / ministry / activity management for churches — the
group-management slice of ChurchTrac, without the rest of it.

Single Go binary serves a JSON API and an embedded React SPA (Meetup-style
UI). Deployments stay one artifact: `docker compose up`.

## Quick start (Docker Compose — recommended)

```bash
cp .env.example .env
# edit .env: set DOMAIN, SESSION_SECRET (openssl rand -base64 48; at least 32 characters),
# DB_PASSWORD, and FOLD_VERSION (an exact release such as 0.5.0, never "latest")
docker compose pull && docker compose up -d
```

Compose pulls the matching `ghcr.io/grierj/fold:${FOLD_VERSION}` and
`ghcr.io/grierj/fold-backup:${FOLD_VERSION}` images; no Go or Node is needed on
the server. Pin an exact paired release and change `FOLD_VERSION` only when you
mean to upgrade. An older app tag needs `FOLD_BACKUP_VERSION` set to a published
backup image version.

Until a paired release is available, build from this checkout with the developer
override instead. Set `FOLD_VERSION` to any label (for example `dev`) and run:

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

The build needs about 2 GB of memory. This is also the developer path once
releases exist.

Open `https://<DOMAIN>` and enter the setup code from `docker compose logs app`.
Keep the code private. Then create the church site and its first admin account.
If the app restarts before setup is complete, use the new code from its logs.

A single DigitalOcean droplet is the recommended production install.
`docs/DEPLOY-DIGITALOCEAN.md` and `deploy/digitalocean/provision.sh`
harden Ubuntu and start this Compose stack. `smoke.sh` checks health,
TLS, and that Postgres is not reachable from the internet. The script
never creates droplets or other DigitalOcean resources.

Postgres data is the `pgdata` volume. Avatars and cover photos are the
`uploads` volume, mounted at `/data` in the app container. Pulling a new
image (or rebuilding one) keeps both. Fold runs only on PostgreSQL and refuses to start without
`DATABASE_URL`. Migrations run on start.

To run the setup wizard again, delete the users and restart the app. The
process remembers that setup finished until it exits.

## Publishing a release (maintainers)

Push a tag such as `v0.5.0`. The version always comes from the tag, and the tag's
commit is what gets built. The `release` workflow builds `linux/amd64` and
`linux/arm64` images of `fold` and `fold-backup` under a staging tag
(`stage-<version>-<commit>`), boots that exact pair by digest, and only then
gives it its names: `ghcr.io/grierj/fold:0.5.0` and
`ghcr.io/grierj/fold-backup:0.5.0`, the full-commit-SHA aliases, an attestation for
each image. It then writes both digests into the GitHub release notes; that
release is the mark that the pair is done. A final release ends by moving the
`:0.5` tags in a last job, which never undoes the release. A published version is never overwritten: an existing exact tag is
accepted only when it already holds the digest being published. Version tags
must never move: before promoting and again before the release is written, the
workflow checks the tag still points at the commit it built, and refuses if not.
Before the release is written it also checks both images pull with no login.

- **A release that stopped part way:** use "Re-run failed jobs" to resume at
  the failed job, or run the workflow again by hand (Actions, release, Run
  workflow) with the same tag selected as the ref. A new run reuses available
  staged images and runs smoke again. A new run on a version whose release
  already exists refuses to start; if only the last `minor` job failed, use
  "Re-run failed jobs" and it moves the `:0.5` tags (an older patch never moves
  them back from a newer released one; a newer patch that has no release yet
  does not count, and finishing it moves the tags forward again). Delete the two `stage-...` tags to rebuild
  only before either exact version tag exists. After an exact tag exists, retry
  with the staged pair; if it is gone, pick a new version.
- **A fix to the workflow itself** does not reach an existing tag: a run uses the
  workflow file from the tag's commit, so publish the fix under a new version.
- **A version published before the backup image existed** (such as `v0.1.0`)
  cannot be completed; the next release publishes both images.
- **The first release of the backup image:** the first staging push creates the
  `fold-backup` package, private at first. The `notes` job fails with the
  package to open until both `fold` and `fold-backup` are public (GitHub, Package
  settings, Change visibility); make them public, then "Re-run failed jobs". No
  release is written before then, so nothing is marked done.
- **Re-running an older patch** after a newer one of the same minor is released
  finishes the older exact tags and skips the `:x.y` update. If the newer run's
  `minor` job failed, use "Re-run failed jobs" on that run to finish it.

## Upgrade notes

- **Nov 2026 (M5.1): Compose pulls a published image.** `docker-compose.yml` no
  longer builds; it needs `FOLD_VERSION` in `.env` and refuses to start without
  it. An older install that built on the box keeps working by adding the
  build override: set `FOLD_VERSION=dev` and run
  `docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build`.
  Once a paired release is published, set `FOLD_VERSION` to that exact version,
  clear any legacy `FOLD_BACKUP_VERSION` override, and run `docker compose pull
  && docker compose up -d`.
- **Oct 2026 (PR #44): `SESSION_SECRET` must be at least 32 characters.** An older
  install with a shorter secret will not start. `docker compose logs app` shows
  one line naming the problem and its length (never the value). Fix: run
  `openssl rand -base64 48`, put the result in `.env` as `SESSION_SECRET=...`,
  and run `docker compose up -d`. Everyone is signed out once.

## Backup

The Compose backup service runs nightly and can make a backup on demand:

```bash
docker compose exec backup /usr/local/bin/backup.sh now
```

You can also make a database dump manually:

```bash
docker compose exec -T db pg_dump -U church -Fc fold > fold.dump
```

The upload volume's name is `<project>_uploads` (`docker volume ls` prints
it). Copy it with a small image that has `tar`, because the app image does not:

```bash
docker run --rm -v fold_uploads:/data:ro -v "$PWD":/backup alpine \
  tar czf /backup/uploads.tgz -C /data .
```

For a droplet restore, follow the [recovery procedure](docs/DEPLOY-DIGITALOCEAN.md#restoring-a-nightly-dump-interim-notes). It keeps the restored site private while you check the church records and delete accounts again.

On a droplet, `/opt/fold/backup.sh` remains available for host backups.
Encrypted offsite upload is optional and deferred. See `docs/DEPLOY-DIGITALOCEAN.md`.

## Local development

Recommended: the Go toolchain pinned in `go.mod`, Node at the major in `.nvmrc`,
and Docker (for the dev Postgres 16 database).

```bash
make dev    # starts Postgres (deploy/docker-compose.dev.yml), the API on :8080,
            # and the Vite dev server (proxies /api to :8080)
```

Run `cd web && npm run build` once before `make dev` if you want to try notifications on this
device: the Go server serves `/sw.js` from the built `web/dist`, and Vite proxies it there.

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
| `FOLD_SETUP_TOKEN` | generated at startup | Optional shared setup code, generated with `openssl rand -hex 32`; all app processes must use the same value |
| `DATABASE_URL` | — | Postgres DSN. **Required** everywhere |
| `FOLD_TRUSTED_PROXIES` | `172.30.0.0/24` | Proxies allowed to set `X-Forwarded-For` (the compose subnet). A `/0` range (`0.0.0.0/0`, `::/0`) is refused at startup, in dev too |
| `COOKIE_SECURE` | true in prod | `false` is accepted only with `APP_ENV=dev` (plain-http local testing); prod refuses to start with it |
| `SITE_URL` | — | Public base URL, e.g. `https://groups.example.org`. Outside `APP_ENV=dev` it must be an `https://` URL with a host or the server refuses to start |

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

- [PLAN.md](docs/plan/PLAN.md): goals, requirements and the index to the design, milestones and [decisions](docs/plan/decisions.md) (source of truth).
- [STATUS.md](STATUS.md): where each milestone stands, with open issues.
- [API contract](docs/API.md), [member guide](docs/MEMBERS.md), and
  [DigitalOcean deployment](docs/DEPLOY-DIGITALOCEAN.md).
- [AGENTS.md](AGENTS.md): development and review rules, with topic detail in [docs/agents/](docs/agents/).

Set `FOLD_SMTP_URL` and `FOLD_EMAIL_FROM` in `.env`, then recreate the app
with `docker compose up -d` to enable transactional email. Follow the deployment
guide to verify delivery before relying on invitations or password resets.

## Layout

- `main.go` — wiring, setup wizard; `internal/httpx` — SPA serving, security headers, access log, manifest
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
