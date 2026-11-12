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
instead of skipping it), and the seeded browser digest and review-store suites. It runs on
Linux with GNU coreutils/findutils (macOS is unverified) and writes a revision-bound record
outside the checkout (a non-empty evidence directory is refused, the
record starts as `incomplete` so an interrupted rerun cannot keep a previous pass, and the
completed `evidence.json` is parsed before success is reported). `--seed` replays one seed the
log names; see the script header for options. Each step runs in a process group of its own
that the script holds through a supervisor and signals only through bash's job table, so an
INT, TERM or HUP to the script ends the step, a child that outlived its parent and a TERM-ignoring
process alike, within about 11 seconds and leaves an `incomplete` record; the header has the
manual probes, and `TestReasonHardening*` (account_erasure_reasons_envelope_test.go) drives the
script with stub tools, real signals, a supervisor killed on its own with its command and a TERM-ignoring descendant still running, and bystander processes (the tests identify processes by PID and start time and end the script only through its handle, never a PID read back from a file). The race detector says nothing about PostgreSQL
lock correctness: the deterministic lock tests in the normal checks decide that.

### Reason-review verification cadence

While you work on contact-request reason review, run the focused checks for the kind of change
you are making, and keep them cheap enough to repeat. They do not replace the final run: the
candidate head still runs the full list in `AGENTS.md` ("Before every push" and the merge gate:
`go build ./...`, `make lint`, `make deadcode`, `make test`, `cd web && npm test && npm run
build`, `make e2e`) and CI. Every name below is a test that exists today; the long runs
(hardening above, scale below) belong to a candidate, not to the edit loop.

| You changed | Run while iterating | Why this one |
|---|---|---|
| The scanner: the scan SQL, batches, cursor, the tombstone discovery markers | `go test . -count=1 -run '^(TestReasonScanNoSkip\|TestReasonScanIsBounded\|TestReasonOrderingMatrix\|TestBatchedScanMatchesTheSingleStatementOnPostgres\|TestTombstoneDiscoveryNumericMarkersAndShape\|TestTombstoneStructuralMatchAndNumericMarker)$'` | The traversal must not skip or repeat a request, every unit stays bounded, the batched scan equals the single statement, and the markers decide what a sweep revisits. Needs `DATABASE_URL`. |
| A transaction, the attempt ledger or the restore fence | `go test . -count=8 -run '^TestAttemptLifecycle$'`; `go test . -count=1 -run '^TestFencedTransactionsTakeTheTicketFirst$'`; then the restore-fence tests below | `-count=8` because the lifecycle tests are about ordering; the guard reads the whole production tree; the fence tests dump and restore a real database, so they need the `pg_dump` setup in "Restore tests" above (`TestRestoreFence*`, `TestReasonReviewRestoreWithOpenPanel`). |
| The review UI or its store | `cd web && npx vitest run src/components/ReasonReviewList.test.tsx src/components/ReasonReviewSection.test.tsx src/lib/reasonReviewStore.test.ts`; then `make e2e` (the shards decide which specs run together, so a spec filter after the spec list does not narrow it; with `DATABASE_URL` set, `E2E_SHARDS=1 web/e2e/run.sh --grep "reviewing contact-request reasons"` runs the deletion flow alone, after `cd web && npm run build`), which includes `admin-delete-user.spec.ts` and the `a11y` and `layout` specs | Vitest is the fast loop; the e2e spec drives the real deletion; axe and layout catch what a unit test cannot see. Look at the 390px screenshots (`.cursor/skills/verify-ui`). |
| Recovery: `restore.sh`, the drill, the deploy tools | `make provision-dry-run` (the dry-run container also runs the drill fixtures, `restore-drill-modes-fixtures.sh` among them; needs Docker), then only the drill cases the change can reach, through `restore-drill.sh` (the case list is its `drill_progress_init` call) | The full drill is long; `docs/agents/deploy.md` ("Full restore drill frequency") is the one place that says when it is owed. |

Run `-run` patterns exactly as written: an unanchored pattern that matches nothing passes
silently, so read the `--- PASS` lines for the names you meant.

### Reason-review scale run

`scripts/reason-review-scale.sh` runs the QC-1 measurement (`deploy/digitalocean/qc1-measure.sh`)
and the restore drill (`deploy/digitalocean/restore-drill.sh`) behind one entry point, one
revision-bound record, and the same cautions as the hardening script: outside CI, a clean
committed HEAD only, an evidence directory outside the checkout that is new or empty, and a
record that starts `incomplete` so an interrupted rerun cannot keep a previous pass. By default
each step runs in a throwaway container with its own Docker daemon
(`restore-drill-nested.sh`); `--runner direct` is for a machine that already is that disposable
host. `--dry-run` prints the steps, the matrix, the coverage the invocation asks for and the
record path, and starts nothing; read it before a run that takes hours.

```bash
scripts/reason-review-scale.sh --dry-run
scripts/reason-review-scale.sh --drill-reason "why the full drill is owed now"
```

- **The record** (`evidence.json`, schema `fold/reason-review-scale/v1`): revision, tree,
  whether the tree was clean, the tool files' sha256, the host, the arguments, and per step the
  command, start and end, seconds, exit status, verdict (`pass`, `partial`, `fail` or, for a
  step a signal cut short, `interrupted`), log and the tool's own artifact directory (`qc1/`,
  `drill/`). `requested` says what the invocation asked for; `coverage` says what was reached.
- **Coverage is `full` only when** both steps ran, the drill was the full drill, nothing was
  passed through to either tool, QC-1's own `evidence-status.json` says complete, coverage
  `full` and `releaseEvidence` true, and the drill exited 0 with a `restore-drill.json` for the
  same revision. Everything else is `partial` with the reasons listed: one step only, the
  `--crossings-only` diagnostic (it exits 10 on a pass and is never acceptance), a QC-1 pilot,
  any `--qc1-arg` or `--drill-arg`. An `incomplete` or `fail` record is never `full`.
- **Exit status**: 0 only for `pass`, 10 for `partial`, 1 for `fail`, 2 for a refusal before any
  step ran, 129, 130 or 143 after a signal (the record is left `incomplete`).
- **QC-1 is a measurement.** Its budget failures are counted in the step's summary for the
  reviewed engineering disposition its header describes; they do not fail the step. A QC-1 run
  that finished normally is `partial` when it is incomplete only because cases are explicitly
  pending (the legacy discovery and legacy PUT cases, at the time of writing) or its coverage is
  a pilot; the record then carries those cases, their reasons and the tool's coverage reasons.
  It is `fail` when the tool exits non-zero, aborted, holds an invalid finding, lacks a
  measurement it should have, or says it is incomplete without saying why. So a real full-size
  run is `partial`, never `pass`, until the pending cases are built.
- **Cancelling** (TERM, HUP or INT to the script) ends the step's whole process group, including
  a descendant that ignores TERM after its parent has gone, and nothing outside the group. The
  command gets `FOLD_SCALE_STOP_GRACE` seconds (default 180) to finish its own cleanup, then the
  group is killed; the step stays in the record as `interrupted`, saying which happened. For the
  default nested run that cleanup is the point, and `restore-drill-nested.sh` does it in three
  ordered steps on every end, not only a signal: stop, then collect, then remove. Every process
  the workload starts carries `FOLD_NESTED_RUN=<run id>` in its environment (each `docker exec`
  passes it), so the runner finds Node, Chromium, npm and orphans as well as the bash scripts,
  by that marker, with a lookup run inside the container. After a signal it TERMs the owned
  top-level processes (`FOLD_NESTED_STOP_GRACE` seconds, default 90), then KILLs every owned
  process (`FOLD_NESTED_KILL_WAIT`, default 15). A stop is established only by two successful
  lookups, a second apart, that both count zero; a lookup that fails is never read as "none".
  If the stop cannot be established the container is frozen (`docker pause`) before the copy. It
  then copies `/work/prep` and `/work/out` into the step's artifact directory and only then
  removes the container. `INTERRUPTED.txt` there says how the run stopped, `LEFTOVERS-STOPPED.txt`
  notes a leftover ended after a normal finish, and `COLLECTION-FAILED.txt` names any copy that
  failed or a stop that could not be established. Such a failure is a failure of the run: the
  runner exits 1 instead of 0 (or 10 for `--crossings-only`), and the wrapper fails the step and
  carries the lines in its summary and coverage reasons even when the tool itself exited 0.
  A workload that ends 137 or 143 while no signal reached the runner (a killed docker client, an
  OOM kill) gets `FOLD_NESTED_SIGNAL_WAIT` seconds (default 5) for a pending signal, never an open-ended
  wait, then goes through the same bounded stop and collection and keeps its own status.
  Every Docker call the cleanup makes (lookups, pause, unpause, both copies, removal) is a client
  with an absolute deadline, in a process group the runner holds until it has ended it: one that
  has not answered is killed with its whole group, also when it ignores TERM, and so is a
  descendant a client leaves behind after it exits; a stall is counted (a stalled lookup, pause or
  copy is said in `COLLECTION-FAILED.txt`). The whole cleanup has `FOLD_NESTED_CLEANUP_BUDGET`
  seconds (default 170; a whole number of at least 3, otherwise the runner refuses to start), a
  single call `FOLD_NESTED_CALL_TIMEOUT` (15; the copy of `/work/out` may use what the stop and
  the preparation copy leave), and the last `FOLD_NESTED_RM_RESERVE` seconds (30, at most a third
  of the budget) are for removal alone: the stop gets at most three quarters of the time before
  them, the preparation copy at most a quarter of what then remains, the pause a third and the
  unpause half of what is left, so a stalled one cannot use up what the next step needs. The
  wrapper checks `FOLD_SCALE_STOP_GRACE` and the budget together before it starts anything (a
  `--dry-run` shows the result): the budget it sets is the grace less ten seconds (less five for a
  grace of 40 or shorter), a grace under 8 seconds is refused, and an explicit budget must leave
  5 seconds of the grace or is refused; it is never replaced by a longer default. A container that
  cannot be shown to be gone (a
  listing by its id that ran and came back empty) is named, with its id and
  `docker rm -fv <id>`, on stderr and in `COLLECTION-FAILED.txt`, and the run exits 1 instead of 0.
  Limits: a KILL sent to either script cannot do any of this; a tool that replaces its whole
  environment would hide from the marker; the drill's own nested containers belong to the
  daemon and go with the container.
- **When the full drill is owed** is decided only in `docs/agents/deploy.md` ("Full restore drill
  frequency"). The script makes you state the reason (`--drill-reason`) and records it with the
  run date; it does not repeat the rule.
- **Its own test**: `make reason-review-scale-test` (also part of `make test`) runs
  `scripts/reason-review-scale.sh --self-test`, which copies the script into a scratch git
  repository whose `deploy/digitalocean` tools are stubs and checks, with no Docker: the record
  shape; that a partial, pilot, crossings-only, one-step or pass-through run is never `full`;
  that a failed step, an unreadable QC-1 record or another revision fails; the QC-1 outcomes for
  the shapes `qc1/lib.sh` really writes (pending only is `partial`; missing, invalid and aborted
  are `fail`); the dry run; the refusals (dirty tree, used or in-checkout evidence directory,
  missing drill reason); that the record a running step sees is `incomplete`; that TERM and HUP
  end the step, keep it in the record as `interrupted` and leave an `incomplete` record, with
  a TERM-ignoring descendant of a tool that already exited, a leftover after a tool that ended
  by itself and a tool that ignores TERM all ended, and a bystander process untouched; and, with
  the real `restore-drill-nested.sh` against a stub docker that holds the artifacts only inside
  its "container" (the workload shell is started with the run's marker already in its initial
  environment, as `docker exec -e` does; before the cancel, the ownership lookup the runner
  sends is run for real, on this host's /proc, with `kill` replaced by a recorder, and must name
  that shell and any stubborn writer, and nothing without the marker), that a cancel stops the run inside, copies the
  artifacts out and only then removes the container, also when the run has to be killed; that a
  non-bash descendant in a session of its own that ignores TERM and keeps writing is found and
  killed before the copy, while a process without the marker is not touched; that a failing
  lookup is never read as "nothing running" (the container is frozen, and the copy recorded as
  incomplete, also when the freeze fails); that a docker exec ending 137 or 143 with no signal to
  the runner and a TERM-ignoring writer left behind finishes in bounded time, kills the writer
  before the copy and keeps the workload's status; that a leftover after a normal end is ended and
  noted; that a lookup, a pause, a copy and a removal that stall (the stub's client has a child of
  its own, and ignores TERM in some cases) are all ended, the run returns within its budget on its
  own, the stall is recorded, the evidence copy is not starved by a stalled preparation copy (also
  with the default call timeout and a short budget), a stalled unpause still leaves a removal
  attempt, a client that exits and leaves a TERM-ignoring child has that child ended, a budget the
  runner cannot use or one that does not fit the wrapper's grace is refused, a
  removal that stalls once is retried, one that never works names the container and fails the
  run, and a process or container that is not the run's is untouched; and that a failed copy or an
  unestablished stop turns a run that exited 0 (or a
  crossings-only 10) into exit 1, and fails the wrapper's step, with the reasons kept. Real runs
  on a droplet-class host are a hand check (`docs/FIRST-CHURCH-CHECKS.md`), not covered by this.

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
