# Testing details

The everyday commands are in `AGENTS.md`. This file has the details you need when a test
setup, a source guard or the e2e harness is what you are changing or debugging.

## Go tests

- `make test` runs `go test ./...` against `DATABASE_URL`; `scripts/go-test.sh` shards the root
  package and starts the dev database if `DATABASE_URL` is unset.
- Tests hash passwords at bcrypt's cheapest cost; a test that measures timing calls
  `realPasswordCost(t)` first.
- Handler tests use `httptest` and `internal/testdb` (reuse the cookie-jar + CSRF dance in
  `testhelpers_test.go`). `testdb.Shared` clears the database before each test, which needs a
  superuser (dev and CI have one); other roles fall back to slower TRUNCATE. Test sessions use
  `synchronous_commit=off`.
- Test seams: `a.now` is the one injected clock; the other seams are the `a.hooks` struct in
  `test_hooks.go`.
- Local concurrency tests can flake under load; rerun with `-count=8` before chasing them, and
  trust CI.

### Restore tests

`TestRestoreFence*` and `TestReasonReviewRestoreWithOpenPanel` need `pg_dump` and `pg_restore` of
the server's major version, because a newer client sends settings an older server rejects. They
use `FOLD_PG_BIN` (a directory of tools), else a matching one on `PATH`, else
`scripts/pg-client.sh` (Docker, `postgres:<major>`). A mismatched client fails the test, it never
skips; with no client and no Docker the test skips locally and fails under `CI`.

### Source guards

- Source guards are the parse-only tests in `guards_<concern>_test.go`, `throttle_keys_test.go`,
  `restore_fence_test.go` and the erasure censuses. Each states its own limits in its doc
  comment; read them before relying on a guard.
- They read Go through `internal/guardkit`, a test-only package that `.deadcode-allow` lists like
  `internal/testdb`; `internal/images/imagetest` (shared upload-test pictures) is test-only too.
- `guardkit.Production(t)` walks the whole tree once with one skip rule set (no tests,
  `testdata`, `web/`, `node_modules`, `vendor`, hidden directories), so code moved into a
  subpackage is still checked. `Sources.Parse` parses it (fixtures too), `InspectStack` is the one
  parent-stack walker, and `Unparen`, `Callee`, `ImportNames` and `Squash` replace per-guard
  copies.
- A guard whose subject is one package filters with `Parsed.Package("main")`, never a directory
  glob. Repo-wide guard files are named `guards_<concern>_test.go` (the layout guard allows that
  pattern at the root only).

## Frontend and e2e

- `cd web && npm test` (Vitest).
- `make e2e` runs Playwright against the real binary (first run:
  `cd web && npx playwright install chromium`). `web/e2e/run.sh` splits specs across shards
  (`E2E_SHARDS`), each with its own server, database and seed; never rely on another spec having
  run first. Refresh `durations.json` with `E2E_RECORD_DURATIONS=1 make e2e`.
- a11y and layout checks run in parts (`UI_PARTS`, `partSelected`); `UPDATE_UI_BASELINE=1` needs
  `E2E_SHARDS=1`.
- Prefer `gotoSettled` to `waitForLoadState("networkidle")`. `signIn` and `signInTo` clear cookies
  first; `signInTo` leaves the app before its API login so no page swaps the CSRF cookie. Assert on
  roles and visible text; accept `confirm()` with `page.on("dialog")`.
- Traces: `web/test-results/shard-<n>/`.
- Golden files: `TestDateProjectorGoldens` rewrites `testdata/date_projector` with
  `UPDATE_GOLDEN=1`.

### Test-only attempt clock

`attempt_clock_e2e.go` (build tag `e2eclock`, with `e2e_hooks.go`) adds an offset to the App clock
and an admin-only, CSRF-protected `POST /api/v1/e2e/attempt-clock`, and only when
`FOLD_E2E_CLOCK=1`. `web/e2e/run.sh` and `server.sh` build with the tag and set the variable; the
Dockerfile, release build, `make test` and `go build` never set the tag. That no shipped build sets
it is checked by review, not by a test.
