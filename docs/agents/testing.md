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
use `FOLD_PG_BIN` (a directory of tools, alone, no fallback), else a matching one on `PATH`, else
`scripts/pg-client.sh` (Docker, `postgres:<major>`). A mismatched client fails the test, it never
skips. With no client and no usable Docker (not installed, or the daemon is stopped) the test
skips locally and fails under `CI`; a Docker that works but cannot pull or run the image, or
cannot reach the database, fails everywhere.

The wrapper (Bash 3.2 compatible; a test reads it for what that Bash lacks) checks each stage and
names the one that failed, without printing the connection arguments. Its exit codes: 70 no
runtime on `PATH`, 71 daemon silent or stopped, 72 image unusable (pull failed or timed out, tool
missing), 73 database unreachable from the container, 74 arguments it cannot run (a colon in a
mounted path, a connection with no host, a socket directory that is missing).

- **Image.** The test process resolves the image once (`scripts/pg-client.sh --resolve <major>`),
  logs its local ID, and runs every tool by that ID (`FOLD_PG_IMAGE_ID`). A pull is bounded
  (`FOLD_PG_PULL_TIMEOUT`, 180s), as are the daemon check (`FOLD_PG_INFO_TIMEOUT`, 20s) and
  `<tool> --version` (`FOLD_PG_DISCOVERY_TIMEOUT`, 60s). `FOLD_PG_IMAGE` picks another image; a
  `name@sha256:...` digest is kept as given. The wrapper never invents a digest for a tag.
- **Deadlines and cleanup.** Each command the wrapper starts (docker, and the tool in its
  container) is the leader of its own process group, and its container has a name the wrapper made.
  At a deadline, or when the wrapper gets TERM, INT or HUP, the whole group gets TERM, then KILL
  after `FOLD_PG_KILL_GRACE` (3s), and `docker rm -f` removes the container. A command that ignores
  TERM therefore still ends, and a signalled wrapper leaves no `pg_dump` or `pg_restore` running.
  The container is removed whenever its command ends, not only at a deadline: the docker client can
  die (a lost connection, an OOM kill) while the daemon keeps the container, and `--rm` acts only
  when the container itself exits. The removal is `docker rm -f` on the generated name, then, if
  that fails, a listing by exact name (a container `--rm` already removed is gone, not an error),
  retried until one absolute deadline of `FOLD_PG_INFO_TIMEOUT` that every docker command of the
  removal is checked against (each gets only what is left, and none starts after it). If the
  container is still there after that, the wrapper says on stderr which one (`docker rm -f <name>`)
  and still exits with the tool's own status. A stop that arrives during the removal ends it and
  repeats it, so the container is not abandoned. After a stop, ending the command and the removal
  share one deadline of `FOLD_PG_KILL_GRACE` plus `FOLD_PG_INFO_TIMEOUT` plus about two seconds
  (under 30s by default), which is why the Go side's 45s grace (`pgCleanupGrace`) is enough; a
  caller that raises either variable raises its own grace too. A removal that does not finish is
  killed, so no `docker rm` or `docker ps` of the wrapper's is left running after it exits.
  Only the generated name is removed or listed; no other container or process is touched.
  The Go side (`pgRun`, for discovery; a dump or restore itself has no deadline) runs the wrapper in
  a group of its own and signals the group at its deadline, because exec's own kill reaches only the wrapper's pid. KILL of the wrapper itself cannot
  be caught, so a killed test process can still leave a container; remove it with `docker rm -f`
  on the `pg-client-*` name.
- **Connections.** A TCP host runs on `--network host` after a `pg_isready` probe, which is what
  works on native Linux Docker. A Unix-socket directory (`host=/dir`, `?host=/dir`, `-h /dir`) is
  mounted at the same path with no network. The container runs as the caller's uid, which the
  image has no passwd entry for, so the probe passes `-U probe` (a `pg_isready` with no user exits 3,
  "no attempt", before it connects; the probe does not authenticate). When the probe gets no answer
  the error says to use a native client (`FOLD_PG_BIN`) or a connection the container can reach.
  A `host=` connection string is read only in its plain form (`key=value` words, no quotes, no
  backslashes, no blanks around `=`); libpq's other forms can hide `host=` or `port=` inside a
  password, so they are refused (74) with a message that says nothing of the string, and the
  wrapper never prints a value it took from the arguments except the host and a numeric port. Use a
  `postgres://` URL, or `FOLD_PG_BIN`. A URL's host, port and `?host=` / `?port=` are
  percent-decoded as libpq does (`%2F` is `/`, `%20` a blank, `+` stays a plus) to choose the
  probe, the network and the mounted directory; the tool still gets the URL as written. A `%`
  without two hex digits anywhere in the URL, `%00`, a control character in the host or port, and
  a comma in the host or port (libpq's list of hosts) are refused (74) without repeating any of
  the URL.
- **Tests.** The argument-level tests (`TestPGClientWrapper*`, in `restore_fence_test.go` beside
  `pgClients`) run the real script against a fake `docker`: paths with spaces, each directory
  mounted once, a stopped daemon, a bounded pull, a failed pull, an unreachable database, image-ID
  use, a mismatched major, and each connection form; the fake `pg_isready` exits 3 without `-U`
  like the real one. The refusal tests put `host=` and `port=` inside quoted and escaped
  passwords and check that neither routes the run nor appears in the output. The deadline tests
  (`...DeadlinesHoldAgainstStubbornCommands`, `...CleansUpWhenToldToStop`, `TestPGRunStopsTheWholeGroup`)
  use a fake whose process ignores TERM, has a child, and starts a "container" only `rm -f` ends;
  they assert a bounded return, no surviving process and the container removed by name.
  `...RemovesAContainerItsClientLeftBehind` makes the fake docker client die after it started
  its container (status 5, 0, or the probe's 2); the container must be removed by name, the exit
  status kept, and an unrelated process and an unrelated container must survive and never be
  named in a docker call. `...NamesAContainerItCannotRemove` has the daemon refuse (the wrapper
  names the container, ends in bounded time, keeps the tool's status), and
  `...FinishesRemovalWhenToldToStop` sends TERM while the first `docker rm` hangs and expects the
  removal to be done again. `...CleanupHoldsWhenRemovalStalls` runs the wrapper through `pgRun`
  with a stubborn tool and a daemon whose `rm`, `ps`, or both hang, cancels it, and expects the
  wrapper to end itself (143, not killed by the Go side) inside the Go grace, its stalled docker
  commands (groups of their own, out of the Go side's reach) dead, the container named on stderr,
  and the bystanders untouched. The encoded-URL cases are in `...ConnectionForms` (an encoded TCP
  host and port, a socket directory with blanks in the authority and in the query, and a repeated
  `host=` or `port=`, where the last one wins as in libpq) and
  `...RefusesURLEncodingsItCannotTrust`. `TestPGClientWrapperProbeWithRealDocker` runs real Docker and `pg_isready`
  against a closed local port and expects the "unreachable" diagnosis (73), skipping where Docker
  is not usable (failing under `CI`). The fake tests show the wrapper builds the right
  commands, not that any Docker variant behaves as assumed.
- **Not covered.** Nothing here was exercised on Docker Desktop, rootless Docker, Podman or a real
  macOS Bash 3.2; treat `--network host` there as unverified and use `FOLD_PG_BIN`. Evidence for
  a change to the wrapper names the environments it actually ran on.

### Reason-review hardening

`scripts/reason-review-hardening.sh` is the longer reproducible run for contact-request reason
review (issue #296), outside CI: deterministic Go vectors and the fuzz seed corpus, a bounded
`go test -fuzz FuzzReasonEnvelope` run, the in-memory attempt-ledger and restore-fence tests
under `-race` (`CGO_ENABLED=1` for that command only; a missing C toolchain fails the run
instead of skipping it), and the seeded browser digest and review-store suites. It writes a
revision-bound record outside the checkout (a non-empty evidence directory is refused, the
record starts as `incomplete` so an interrupted rerun cannot keep a previous pass, and the
completed `evidence.json` is parsed before success is reported). `--seed` replays one seed the
log names; see the script header for options. The race detector says nothing about PostgreSQL
lock correctness: the deterministic lock tests in the normal checks decide that.

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
