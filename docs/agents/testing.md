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

### Upload memory measurement

`TestMeasureNormalize` (`internal/images`) and `TestMeasureUploadRequests` (root, needs
`DATABASE_URL`) are opt-in (`FOLD_UPLOAD_MEASURE=1`, not run in CI), e.g.
`FOLD_UPLOAD_MEASURE=1 go test ./internal/images -run '^TestMeasureNormalize$' -count=1 -v` and
`FOLD_UPLOAD_MEASURE=1 go test . -run '^TestMeasureUploadRequests$' -count=1 -v`. Each runs every
`imagetest.WorstCases` picture (sized so its estimate is 80% to 100% of the budget, one for each way
the estimate adds up, a 200-frame interlaced GIF for the per-frame decoder and encoder state among them) in a process of its own (`FOLD_UPLOAD_MEASURE_CASE=<name>` runs one) and logs a
`MEASURE {...}` JSON line. Run them alone: they are about memory, so other work on the machine skews
them. They need Linux with `/proc/self/clear_refs` writable (a container is fine) and fail without
it, since the process peak could not be told from the fixtures' own.

- The first runs 4 waves of `MaxConcurrentProcessing` normalizations with more attempts refused
  while full, and checks that every slot recovers. The second sends 3 waves of 8 concurrent avatar
  and cover requests through the router, once with half cancelled mid-flight, after a control of the
  same size that is not a picture, so what the picture adds over it is the processing.
- What fails: a worst case outside 80% to 100% of the budget; a call that allocates more than its
  estimate (`totalAllocBytesPerCall` over `estimateBytesPerCall`); a process peak
  (`vmHWMOverBaselineKB`) over `images.ProcessAllowanceBytes` (320 MiB; in the second test plus the
  control's own peak, which is the ingress); a leaked slot or temp file. Change the allowance only
  with the budget and slots it is derived from (`internal/images/upload.go`) and the docs that quote it.
- The numbers differ: `estimateBytesPerCall` is the pre-decode estimate; `peakHeapObjectsBytes` is
  the sampled peak of Go heap objects, dead ones not yet freed included, over the baseline;
  `peakGoMappedBytes` is the runtime's mapped memory (it can include pages already given back);
  `vmHWMOverBaselineKB` is the process's resident high-water mark after the baseline was reset
  (`/proc/self/clear_refs`, Linux); `totalAlloc*` is cumulative and says nothing about peak on its own.
  Only the resident mark is held to the allowance; the others explain it. A temporary file's page cache
  is not in the process's resident size, so it does not show here.
- Constrained memory. The same pictures can be run under a container memory limit with swap off, so an
  overrun is an out-of-memory kill (exit 137) rather than a number. Build the test binaries on the host
  and run them in a bare container, so the compiler's memory is not in the limit:

  ```bash
  CGO_ENABLED=0 go test -c -o /tmp/images.test ./internal/images
  docker run --rm --memory=512m --memory-swap=512m --tmpfs /tmp:size=64m \
    -v /tmp/images.test:/images.test:ro -e FOLD_UPLOAD_MEASURE=1 debian:stable-slim \
    /images.test -test.run '^TestMeasureNormalize$' -test.count=1 -test.v
  ```

  512 MiB is the allowance plus room for the test binary and the fixture each case builds (raise it if
  a fixture's own build is what gets killed, and say so). It is a stricter check of the same picture work than the harness's
  own number, not a model of the droplet. The HTTP test needs a database, so run it the same way with
  `DATABASE_URL` pointing at one the container can reach (`--network host`, or the compose dev
  database's published port), and with a higher limit, as the database client and every request body
  count too.
- Limits: sampled every millisecond, so a short spike can be missed; one machine, one Go version
  (the estimate is derived from that toolchain's decoders and the tests that hold it to them run on
  every `go test`), one kernel; the second test's process also holds the database client, the app and
  each body once more in the test client; the first leaves out multipart ingress. A container limit
  does not reproduce a 1 GB droplet's swap, the other services' memory or kernel pressure; that is a
  hand check in `docs/FIRST-CHURCH-CHECKS.md`, so the 1 GB floor is neither proven nor changed by these
  numbers.

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
