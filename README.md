# Church Groups (working title: Fold)

Small-group / ministry / activity management for churches — the
group-management slice of ChurchTrac, without the rest of it.

## Quick start (Docker Compose — recommended)

```bash
cp .env.example .env
# edit .env: set DOMAIN, SESSION_SECRET (openssl rand -base64 32), DB_PASSWORD
docker compose up --build -d
```

Open `https://<DOMAIN>`. The first visit shows a setup wizard to create the
church site and its admin account.

## Local development

Requires Go 1.24+.

```bash
# SQLite is the dev default — no database to install.
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

Postgres example:

```bash
DATABASE_URL=postgres://church:secret@localhost:5432/churchgroups?sslmode=disable \
SESSION_SECRET=$(openssl rand -base64 32) \
go run .
```

## Tests

```bash
go test ./...
```

## Layout

- `main.go` — routes, middleware (auth, admin, CSRF, setup wizard), handlers
- `config.go` — environment-based configuration
- `db.go` — Postgres/SQLite selection + migrations
- `models/` — GORM models (`User`, `Group`, `Setting`)
- `templates/` — server-rendered HTML
- `static/` — CSS
