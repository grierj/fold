---
name: verify-fold
description: Drive the Fold church small-group web app in a browser against an isolated Postgres database and capture proof. Use when a change to Fold's sign-in, directory, groups, events, or session needs evidence that the real app works.
---

# Verify Fold

Fold's primary surface is the web UI served by the `fold` binary. The same process also serves the JSON API under `/api/v1`. The `fold` CLI (`serve`, `healthcheck`, `migrate`, `version`) and `docker compose up` are the production process and deploy, not the surface this skill drives. Browser checks use Playwright from `web/node_modules` against an instance this skill started.

Do not drive `make dev`. That server uses the shared dev database named `fold` on port 8080. Do not drive `make e2e` or `web/e2e/server.sh` for a one-off proof. That script drops and recreates one shared `<database>_e2e` database, writes its binary to `/tmp/fold-e2e`, and listens on `E2E_PORT` (default 18090). Two of those runs corrupt each other. `verify-fold up` gives each run its own database, port, work directory, and proof directory, so two verification runs can proceed together. Never point a run at a database whose name is `fold` or ends in `_e2e`.

## Launch

From the repo root:

```bash
.cursor/skills/verify-fold/verify-fold up
```

The command starts the dev Postgres container if it is not already up (`deploy/docker-compose.dev.yml`, database `fold` on `127.0.0.1:5432`). With no Docker, point it at a running Postgres instead: `VERIFY_PG_URL=postgres://<user>:<pass>@127.0.0.1:5432/postgres?sslmode=disable` (any database that role can `CREATE DATABASE` from). Export the same `VERIFY_PG_URL` for every later `verify-fold` command in the run, including `down`. It then creates a new database named `fold_verify_<run>`, builds `web/` and a `CGO_ENABLED=0` binary, and starts `fold serve` in `APP_ENV=prod` with `COOKIE_SECURE=false` so the session cookie works on plain `http://127.0.0.1`. Uploads go to that run's work directory, not `/data`.

Ready means `GET /healthz` returns 200 with an empty body. The server log line is `Starting server on :<port>`. `up` prints:

```text
RUN_ID=<id>
URL=http://127.0.0.1:<port>
EVIDENCE=/tmp/fold-verify-proof/<id>
```

A fresh database redirects every page except `/healthz`, `/setup`, and `/static/` to `/setup`. Keep the `RUN_ID`. Pass it to every later command.

## Doctor

```bash
.cursor/skills/verify-fold/verify-fold doctor "$RUN_ID"
```

Run this first whenever a drive looks wrong. It exits 0 only when all of these are true:

- The recorded pid is alive, `/proc/<pid>/exe` is the binary this run built, and its command is `serve`.
- That pid owns the listen socket for the recorded port.
- The process `DATABASE_URL` matches the state file and the database name starts with `fold_verify_`.
- `GET http://127.0.0.1:<port>/healthz` returns 200 with an empty body.
- `<work>/fold version` prints a line that starts with `fold `.

Doctor does not print the session secret.

## Drive

Packaged drive, and the one to run for a first proof:

```bash
.cursor/skills/verify-fold/verify-fold drive "$RUN_ID" sign-in
```

That command runs doctor, then drives sign-in in Chromium through the real pages. The recipe is in [features/sign-in.md](features/sign-in.md). If the Playwright browser is missing, the command installs Chromium once with `npx playwright install chromium` from `web/` and retries.

Other features do not have a packaged drive. Read the feature file and run its commands with the environment from: (it prints `export` lines, so a `node` child sees them)

```bash
eval "$(.cursor/skills/verify-fold/verify-fold env "$RUN_ID")"
```

Node ESM does not resolve Playwright through `NODE_PATH`. A one-off script loads it from the web app:

```javascript
import { createRequire } from "node:module";
import { pathToFileURL } from "node:url";
const { chromium } = createRequire(pathToFileURL(process.env.FOLD_ROOT + "/web/package.json"))("playwright");
```

Start every feature from the baseline in [features/README.md](features/README.md) unless that file names a different precondition. A proof that uses one entry point does not cover the others the map lists.

Playwright locators in the feature files are the handles to use: role and accessible name, label, or the `name` attribute on the setup form. The directory search box has no label. Its placeholder is `Search groups…` (ellipsis character, not three dots). Several admin form labels are not tied to their inputs; the feature file says which locator to use instead.

## Evidence

Proof for a run lives in `/tmp/fold-verify-proof/<RUN_ID>/`. `drive sign-in` writes `sign-in/`:

- `bad-password.png` shows the login form still open with `Invalid email or password.`
- `after-login.png` shows the directory heading and the header name `Verify Admin` after a successful login.
- `after-reload.png` shows the same session after a reload.
- `me.json` is the body of `GET /api/v1/auth/me` from that browser session. It is 200 and the user email is `verify-admin@example.com`.
- `result.txt` records the final URL, heading, header name, and that `auth/me` returned the same email.
- `users.csv` is `email,name,role` from the verify database. It contains `verify-admin@example.com,Verify Admin,admin`.

Standards:

- Use the pages and controls a person uses. Do not call an unlisted test endpoint, and do not insert rows except through the app.
- Capture the action and the state it produced. A status message or a single screenshot is not enough. Sign-in proof is the failed attempt, the directory after success, the same directory after reload, the session JSON, and the user row.
- `GET /api/v1/auth/me` is the same call the SPA uses to read the session. It is a second view of the cookie, not a substitute for the form.
- The dev database named `fold` must not gain `verify-admin@example.com`. The drive checks that and fails if it did.
- There is no dry-run mode. The verify database is the real database for that process.

Save later feature artifacts under the same proof directory, in a subdirectory named for the feature. Record the feature id and the entry point in a `result.txt` next to the files.

## Cleanup

```bash
.cursor/skills/verify-fold/verify-fold down "$RUN_ID"
```

`down` sends SIGTERM to the pid recorded for that run, then SIGKILL if it is still alive after a few seconds. It does not kill by process name. It drops only a database whose name matches `fold_verify_<run>`, then deletes `/tmp/fold-verify-<RUN_ID>/` (binary, uploads, log, state). It does not delete `/tmp/fold-verify-proof/<RUN_ID>/`.

After `down`, confirm the proof files are still in the evidence directory and that `kill -0` on the old pid fails. A cleanup that removes the screenshots or `me.json` is wrong.

Run `down` after a failed `up` or `drive` as well, so a broken attempt does not leave the process or the database behind.

## Helpers

The helper is `.cursor/skills/verify-fold/verify-fold`. Invoke it by that path. `drive sign-in` runs `.cursor/skills/verify-fold/drive-sign-in.mjs` with `VERIFY_URL` and `VERIFY_EVIDENCE` set. Do not start `drive-sign-in.mjs` against a server you did not launch with `up`.
