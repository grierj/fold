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
  passes it), so the runner counts Node, Chromium, npm and orphans as well as the bash scripts,
  by that marker, with a lookup run inside the container that signals nothing: no process is
  ever stopped, resumed or ended by its number, because a number alone is not a process (it can
  be handed to another between a look and a signal) and a shell holds only its own children.
  Two things end anything. Each `docker exec` runs the workload under a small supervisor
  (`WRAP`) that polls for `/work/stop-request` and sends one TERM to the workload's process
  group. The group is held by a leader process that ends only when the supervisor says so, so its
  number stays in use whenever the supervisor can send: the workload itself can end at any moment
  and no signal ever goes to the number of a process that has ended (bash reaps a finished child
  at once, and its number can be handed to another). The directory it works in is `$FOLD_WRAP_ROOT` (`/work` unless
  the self-test sets it), read as data and always quoted, and it is a lease: the leader and the
  supervisor end by themselves when it is gone. After a signal the runner writes that file
  (`FOLD_NESTED_STOP_GRACE` seconds, default 90). Then the container is killed (`docker kill`:
  every process in it, at once, by no number) and the daemon is asked whether it has stopped
  (`FOLD_NESTED_KILL_WAIT`, default 15). A stop is established only by that answer or by two
  successful lookups, a second apart, that both count zero; a lookup or a kill that fails is
  never read as "none". If the stop cannot be established the container is frozen (`docker
  pause`) before the copy. It then copies `/work/prep` and `/work/out` into the step's artifact
  directory (`docker cp` reads a stopped container too) and only then removes the container. `INTERRUPTED.txt` there says how the run stopped, `LEFTOVERS-STOPPED.txt`
  notes a leftover ended after a normal finish, and `COLLECTION-FAILED.txt` names any copy that
  failed or a stop that could not be established. Such a failure is a failure of the run: the
  runner exits 1 instead of 0 (or 10 for `--crossings-only`), and the wrapper fails the step and
  carries the lines in its summary and coverage reasons even when the tool itself exited 0.
  A workload that ends 137 or 143 while no signal reached the runner (a killed docker client, an
  OOM kill) gets `FOLD_NESTED_SIGNAL_WAIT` seconds (default 5) for a pending signal, never an open-ended
  wait, then goes through the same bounded stop and collection and keeps its own status.
  The workload's own docker client is held the same way: a job in a process group of its own that
  records its status and only waits, which the runner polls for its status. It is ended through
  that job, once, after its name has been cleared with signals held, at the end of the run or last
  in a cleanup, so a client that answered long before a stalled copy is over is never signalled by
  a number it no longer has. A held group also ends itself when the runner is gone, however it
  went (`life_hold`: a watcher in the group reads a FIFO whose only writer is the runner's
  descriptor 8, and ends the group at end-of-file), so a KILL of the runner leaves no client
  behind. Every Docker call the cleanup makes (lookups, kill, state, pause, unpause, both
  copies, removal) is a client
  with an absolute deadline, in a process group the runner holds until it has ended it: one that
  has not answered is killed with its whole group, also when it ignores TERM, and so is a
  descendant a client leaves behind after it exits. A signal between the launch of a client's
  group and the moment the runner has named it (where cleanup could not yet find it) is held and
  taken as soon as the group is named, once (it is cleared before the exit into cleanup, whose own
  calls must not find it again), and the client starts with default INT, TERM and HUP,
  also when cleanup, which ignores them, launched it. A stall is counted (a stalled lookup, pause or
  copy is said in `COLLECTION-FAILED.txt`). The whole cleanup has `FOLD_NESTED_CLEANUP_BUDGET`
  seconds (default 170; a whole number of at least 3, otherwise the runner refuses to start), a
  single call `FOLD_NESTED_CALL_TIMEOUT` (15; the copy of `/work/out` may use its whole slice),
  and the last `FOLD_NESTED_RM_RESERVE` seconds (30, at most a third of the budget) are for
  removal alone. The window is laid out from its end, so a step cannot take what a later one
  needs, however short the budget: the copy of `/work/out` owns the 2 seconds before the reserve,
  the pause and the preparation copy end before those, and the stop ends 2 seconds before them
  (and takes at most three quarters of the time before the reserve). A step with no time left is
  not started and is said in `COLLECTION-FAILED.txt`. The unpause comes after the copies and runs
  only if 2 seconds would still remain for removal after it (it ends by then, taking half of the
  time before that at most); otherwise it is skipped and removal gets its attempt. The
  wrapper checks `FOLD_SCALE_STOP_GRACE` and the budget together before it starts anything (a
  `--dry-run` shows the result): the budget it sets is the grace less ten seconds (less five for a
  grace of 40 or shorter), a grace under 8 seconds is refused, and an explicit budget must leave
  5 seconds of the grace or is refused; it is never replaced by a longer default. A container that
  cannot be shown to be gone (a
  listing by its id that ran and came back empty) is named, with its id and
  `docker rm -fv <id>`, on stderr and in `COLLECTION-FAILED.txt`, and the run exits 1 instead of 0.
  Limits: a KILL sent to either script cannot run the cleanup (it does end the held client
  groups); a tool that replaces its whole
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
  environment, as `docker exec -e` does; before the cancel, the ownership count the runner
  sends is run for real, on this host's /proc, and must count at least that shell and any
  stubborn writer, and not the process without the marker), that a cancel stops the run inside,
  copies the artifacts out and only then removes the container, also when the container has to
  be killed; that a non-bash descendant in a session of its own that ignores TERM and keeps
  writing is gone before the copy (the container was killed), while a process without the marker
  is not touched; that a failing lookup is never read as "nothing running", and neither is a kill
  that does not take effect (the container is frozen, and the copy recorded as incomplete, also
  when the freeze fails); that a docker exec ending 137 or 143 with no signal to
  the runner and a TERM-ignoring writer left behind finishes in bounded time, kills the writer
  before the copy and keeps the workload's status; that a leftover after a normal end is ended and
  noted; that a lookup, a pause, a copy and a removal that stall (the stub's client has a child of
  its own, and ignores TERM in some cases) are all ended, the run returns within its budget on its
  own, the stall is recorded, the evidence copy is not starved by a stalled preparation copy (also
  with the default call timeout and a short budget), a stalled unpause still leaves a removal
  attempt, a client that exits and leaves a TERM-ignoring child has that child ended, a TERM sent
  to the runner exactly while a lookup's group is being launched (a DEBUG trap loaded through
  `BASH_ENV` fires it just before `LIMITED_JOB` is assigned) ends the run with 143 having finished
  its cleanup (`INTERRUPTED.txt` says TERM, no collection failure, evidence and preparation copied
  before the container is removed), ends every stalled client and its child, leaves nothing the case
  started running (every process of the case carries `STUB_CASE=<name>` and the run's
  `REASON_SCALE_SELFTEST` marker, the stub records that it saw both, and only a process with both is
  found: a probe shows that one borrowing the label with no or another run's marker is not).
  The self-test never signals a process it found by looking at the host, and never one by a number
  recorded earlier, because a number can be handed to an unrelated process between the look and the
  signal whatever is checked in between. Looking is read-only (`find_marked`, which names what a
  failed case left). What a failed case leaves ends by itself: the simulated container is a
  directory, removal and the freeze are flag files (`removed`, `paused`) that the stub's processes
  (the workload shell, the writer that ignores TERM) watch, and each case has a directory of its own,
  deleted by the next, which they also watch, as they do the run's directory that the exit trap
  deletes (the trap reads the marker from the exported variable, so a temporary directory with
  spaces is handled); the stalled clients and children the stubs leave carry a bound of 90 seconds.
  The probes are owned through pipes, one FIFO each, whose only writer is a descriptor of the test
  shell, and end by themselves when it closes (also when the shell is interrupted). A probe has no
  timer of its own and nothing signals it. The read end is opened before the fork and inherited, so
  a probe never opens the FIFO by name; an interruption case kills a shell holding two freshly
  forked probes (one marked, one not) before they take a step and checks that they end by
  themselves once released. A guard reads the script itself and fails if a signal is aimed at what
  a look at the host found, at a pid read from a file, or at the container's holders. The
  self-test has no watchdog of its own: every runner it starts runs under `timeout --foreground
  -s KILL` (`bounded_exec`), whose parent-child pair is the only thing that can send the time
  limit's KILL, so a limit cannot outlive its runner or signal a number that has changed hands. The
  signal a case sends goes to that `timeout` (alive, because the runner it holds cannot leave the
  running workload before the signal), which passes it on to the runner alone. A fixture
  interrupts a shell that has started a runner under `bounded_exec` while the runner waits at a
  gate, lets the runner finish long before its limit and checks that nothing carrying the case's
  marker remains. The runner's cleanup restores the stderr the run started with (fd 7) before it
  reports anything, because a signal taken inside a polling loop's `2>/dev/null` would otherwise send
  the container's name and the command to remove it nowhere; `nest-hang-rm` and `nest-direct`
  read that line from stderr. For the
  signals that remain, a function loaded through `BASH_ENV` records every `kill` of the runner and
  the stubs (the function, the file, whether the target was a live process, the arguments) in two
  cases: a cancel whose evidence copy stalls after the workload has ended and its docker client has
  long answered, and a cancel whose removal stalls after the workload has ended. Every KILL the
  runner sends must hit a live process, which is a client group it still holds (the recorder must
  have seen the runner, so an empty record cannot pass), and the simulated container sends none.
  No signal reaches a host process by number. The runner's container-side lookup script is
  run by the stub on this host's /proc in its read-only mode (`count`) whichever mode is
  asked for; a request to stop arrives at the simulated processes as a flag file (`sig-term`),
  and `docker kill` as another (`sig-end`) with the state `docker inspect` then reports, that
  they act on themselves. The lookup signals nothing at all, which is the answer to a number
  that has changed hands (`n_scan_quiet`): the script, read as it stands, holds no signal, and
  the count, run on this host's /proc beside two idle probes (one with the run's marker, one
  without, which stands for an unrelated process that came to hold a number), counts only the
  marked one and leaves both running. Those probes are owned through a channel like all the
  others, so an interrupt at any point, between their fork and any later bookkeeping included,
  leaves nothing stopped or waiting (`n_probe_interrupt` is that boundary). `n_wrap_group` runs
  the real `WRAP`, extracted from the runner, with its `/work` in a directory of the case and
  its `kill` replaced by a function that records every call and whether the target was alive.
  A workload asked to stop ends 143 after one TERM to a live group; a workload that ends
  between the supervisor's last look at the group and the signal (a hook, through `BASH_ENV`,
  holds the workload at that boundary and lets it go) keeps its own status, and the one signal
  goes to the held group, never to the number of the process that ended; a workload with no
  request gets no signal. Its files are gone and no process carrying the case's marker remains.
  The case directory has a blank in its name, so a path is proved to be data. `n_wrap_interrupt` interrupts a self-test shell (TERM) with the real `WRAP` running a workload (`gate.sh`, or `term.sh` / `release.sh`, the workloads of the cases above).
  The workload is held at a gate outside the directory immediately before its first write into it, and keeps creating files there; the exit opens the gate and removes the directory,
  so the removal runs against the creation. The exit revokes the lease first, by renaming the directory away (one atomic step), and only then deletes it. That is the quick path, not
  the bound: the workload also holds the read end of a lifeline (a FIFO whose write end only the interrupted shell holds), so it ends when that shell does, whether or not the rename
  or the removal worked, and it ends if its gate disappears. Every `WRAP` a fixture starts goes through `n_wrap_start` (the cases above and the interrupted shell alike), which gives the
  supervisor's tree the lifeline's read end on fd 8 and no write end, and every waiting workload (`term.sh`, `release.sh`, `gate.sh`) reads it as its wait and its check; once the workload
  ends, the leader and the supervisor end through the lease and parent checks `WRAP` already has. The modes: `request` (after the workload started, before any request to stop, runner registered), `registration`
  (before the runner is registered, so the exit knows nothing of it), `norename` (the rename is impossible and the removal runs in the order that lost the race: the directory is
  emptied, the workload writes its first file, then the `rmdir` fails; the case checks that the directory and its lease stayed, and the workload still ends), `lost` (run with `term.sh`
  and with `release.sh`: the rename is impossible and the removal fails outright, so the directory stays for good; the case checks that it stayed with the workload's `wl.pid` in it and that
  the workload, its leader and the supervisor all end anyway) and `launch` (the
  interrupted shell plays the self-test and has forked, without registering it, the shell that runs the workload; the whole case directory, gate included, is removed). In every mode
  the directories are gone (except those of `norename` and `lost`, by design) and nothing carrying the case's marker remains.
  Every wait of
  the simulated container (the docker client's wait for the workload's readiness, the writer's
  wait to record itself) ends when the container's directory, the lease its processes hold, is
  gone; `n_startup_interrupt` removes it at the boundary between the workload's launch and its
  readiness (the workload is held from ever becoming ready) and checks that the client exits by
  itself (status 1, not the limit's KILL) and that nothing carrying the case's marker remains.
  The self-test's own exit does not leave a runner behind either: a runner it has started and
  not waited for is asked to stop and waited for before the run's directory (the stub docker,
  the nested repository, the simulated container) is removed (`drop_selftest`), and the limit
  of `bounded_exec` (240 seconds) leaves a signalled runner the whole of its cleanup budget.
  `n_outer_interrupt` plays this with the real runner and a run directory of its own: the
  shell is sent TERM mid-run, the evidence is copied and the container removed before the
  directory goes, and no runner, client group, holder or simulated process carrying the case's
  marker remains. `n_orphan_holders` ends the runner with a KILL (the limit of `bounded_exec`)
  while its workload's client is held, and checks that the group ends by itself
  (`life_hold`). A
  bystander survives, a budget the
  runner cannot use or one that does not fit the wrapper's grace is refused, a
  removal that stalls once is retried, one that never works names the container and fails the
  run, and a process or container that is not the run's is untouched; and that a failed copy or an
  unestablished stop turns a run that exited 0 (or a
  crossings-only 10) into exit 1, and fails the wrapper's step, with the reasons kept. Real runs
  on a droplet-class host are a hand check (`docs/FIRST-CHURCH-CHECKS.md`), not covered by this.
- **The launcher fixtures** (`isolation-fixtures.sh`, `qc1-runtime-fixtures.sh`, `restore-drill-modes-fixtures.sh`, run by `make provision-dry-run`) log each Docker call twice: as one
  line for the ordinary line checks (a newline inside an argument is written as `\n`, so the supervisor script, `WRAP`, one multi-line argument, neither splits a call across lines nor
  reads as calls of its own), and in `<log>.argv` with every argument `%q`-quoted, which keeps where each argument ends (the file starts anew with each launch's `run`). They take `WRAP` out of
  `restore-drill-nested.sh` and require, in the argv file, that each exec line ends with `bash -c`, exactly that text as ONE argument, `_`, `bash` and the workload's words as separate
  arguments; a call whose `WRAP` and words are joined into one argument is refused (each fixture checks that on a made-up line). They also require that it loads the prepared Chromium,
  polls for the stop request and TERMs the group, and does not exec the command in its place. The scale self-test's stub docker does the same, and `nest-done` checks the preparation's call.
- **The nested daemon has no TCP API** (`isolation-fixtures.sh`). The `docker:*-dind` entrypoint adds `--host=tcp://0.0.0.0:2375` (an unauthenticated API of a privileged daemon) to its
  default command whenever the arguments after the image are empty or start with `-`; an extra `--host=unix://...` after those defaults does not remove it. So the launcher starts the
  daemon as `docker run ... <image> dockerd --host=unix:///var/run/docker.sock`, and the fixture reads the launch's argv file and requires exactly those twelve arguments (the image
  being the digest-pinned default or, with `--dind-image`, the reference as given). The launcher then reads the container's `/proc/net/tcp` and `tcp6` with `LISTEN_SCAN` once the
  daemon answers and refuses the run, removing the container, if 2375 or 2376 is a listening port or the lookup proves nothing (fails, prints anything but `checked ipv4` or `checked ipv4 ipv6`);
  the fixture runs that lookup for real on made-up proc files and drives the refusals through the stub docker. This is the container's own network namespace, not a test of what could
  reach it, and it does not run the real image: the real daemon is a hand check (`docs/FIRST-CHURCH-CHECKS.md`). Moving the pinned digest is a deliberate change: read the new image's
  `dockerd-entrypoint.sh` first.

### Reason-review scale matrix (#293, #294)

One QC-1 run covers the table sizes (10k, 100k, 1M `contact_requests` rows), the reason counts, both
party directions, keep, blank and mixed decisions, the dense 100,000-reason account, the
request-size comparison with the old envelope and the browser panel, for **one layout** of the
scenario reasons and **one charset**. The matrix is the runs that together cover what the issues
ask for. Each is a **cell**, named `<charset>-<layout>`:

| Cell | What it adds |
|---|---|
| `ascii-spread` | The baseline: ASCII reasons spread through the table. Includes the one old-envelope comparison (the synthetic old `items` body at the id width actually seen, against `maxRequestBytes`). |
| `ascii-tail` | The reasons after every other row, the worst case for a scan from the start. |
| `ascii-head` | The reasons before every other row. |
| `multibyte-spread` | `--charset multibyte`: 1,000-rune reasons, background and target alike (#293). |

```bash
E=~/.local/state/fold/reason-review-matrix/$(git rev-parse --short=12 HEAD)   # outside the checkout
scripts/reason-review-scale.sh --only qc1 --evidence-dir $E/ascii-spread
scripts/reason-review-scale.sh --only qc1 --evidence-dir $E/ascii-tail --qc1-arg --layout --qc1-arg tail
scripts/reason-review-scale.sh --only qc1 --evidence-dir $E/ascii-head --qc1-arg --layout --qc1-arg head
scripts/reason-review-scale.sh --only qc1 --evidence-dir $E/multibyte-spread \
  --qc1-arg --charset --qc1-arg multibyte --qc1-arg --browser-fallback-digest
scripts/reason-review-matrix.sh --out $E/matrix.json \
  --cell ascii-spread=$E/ascii-spread/qc1 --cell ascii-tail=$E/ascii-tail/qc1 \
  --cell ascii-head=$E/ascii-head/qc1 --cell multibyte-spread=$E/multibyte-spread/qc1
```

Each run takes hours, needs a **dedicated** Docker daemon (the nested runner makes one; a shared
daemon makes the run a pilot) and starts from a clean commit. All four cells must be measured on
one revision: the report refuses to call a matrix `FULL` when they differ. A pass-through argument
makes the wrapper record that step `partial`, which the matrix accepts (it requires `pass` or
`partial`, exit 0). `scripts/reason-review-matrix.sh --dry-run` lists the cells.

Every measured DELETE leaves `delete-attempts/NNNN.json` in the cell's evidence directory
(`qc1_delete_attempt`): status, the request's exit, duration, byte counts, the case, size, charset and
revision, and the response's `code` only when it is one of five known API codes (otherwise `withheld`).
No message, log or request text is kept, and no cause is inferred; a missing number means the record
could not be written (said on stderr).

The matrix takes nothing a cell says about itself on trust. For each cell it **recomputes** the
evidence status from `measurements.jsonl` and `environment.json` with `qc1_evidence_status` (the
function that wrote `evidence-status.json`) and judges by that; the saved file must equal the
recomputed one, so a stale, edited or mismatched status is a gap. It also requires: the judge here to
be the one the run recorded (the sha256 of `qc1/lib.sh` and `qc1/app-facts.json` in the
environment record), a complete environment with no problem named, settings that name the full
matrix, and the runner's own record (`evidence.json`, one directory above the cell) to show a clean
tree, the same revision and one QC-1 step that exited 0 and was neither a failure nor an
interruption, for this directory, with the summary the rows give. `COLLECTION-FAILED.txt` (what the
nested runner could not bring out of the container) is a gap of its own even when every row is
complete.

An explained deletion's audit-retention report (item 6, `order-control-delete-*-explained`) is tied
to its deletion by what the application sends around it, not by a statement the harness remembers:
the report is the Go source text with its complete parameter list, it follows the erasure's own
audit UPDATEs on its backend, and the first statement that backend logs after it is conversion
discovery's first query (`auditConversionRequestsSQL`, in `app-facts.json` as `auditReport.successor`),
with the deleted account as its only argument and no second block of its kind in the log. One
predicate (`audit_identity_ok` in `qc1/lib.sh`) sets `identity.proven` and its proof text when the
evidence is collected and is rechecked by the judge, so `identityProven` in the status is the
judge's verdict. `TestQC1AppFacts` checks the source-derived query, and
`TestQC1PlanMarkersAreWhatTheApplicationSends` checks its order after the report. Source review and
the collector's first-same-backend-block check establish adjacency. If the query changes, regenerate
the facts as above and review the proof. `testdata/qc1-current-audit-retention.log` is a real log of the current
build for this check. Judging older evidence with a changed `lib.sh` makes a new, labelled
diagnostic; the saved matrix and its recorded `lib.sh` hash are never rewritten.

The layout is bound to the rows, not only to the labels. The judge (`qc1_evidence_status`) requires
every discovery, deletion, workers and browser row (items 3, 6, 9 and 10) to carry the `layout` its
`record()` writes, equal to the layout the settings name. A row of another layout, or of none, is a
`settings-disagreement` finding (`what: "layout"`), so a `head` or `tail` cell cannot be certified
by its environment label over rows measured in `spread`. The request-size and plan rows load their
own layouts and are not held to it. Each cell's record carries its `rowLayouts`.

`record()` hands the row's figures to `jq` in a file, never as a process argument: a multibyte
browser case holds the final screen's 50 reason texts (about 145 kB), and Linux refuses one
argument above 128 KiB. `qc1-fixtures.sh` runs the runner's real `record()` on such a screen.

- **`--charset multibyte`** replaces the reason text with a 21-rune unit of 1- to 4-byte UTF-8 runes
  (accented letters and Omega, the euro sign, CJK, a zero-width joiner, emoji and combining marks),
  repeated and cut to the requested **rune** length, with the row's ASCII ordinal at the end so
  every reason is unique. That is 61 bytes per 21 runes, about 2.9 bytes a rune: it is not the
  4,000-byte worst case of a 1,000-rune reason, and repeated text compresses and TOASTs better than
  prose. The record says so (`notMeasured`). The DELETE body does not grow: set-v1 carries a digest,
  not the text. What grows is the page responses, the browser's heap and DOM, and the server's
  read and hash of each reason.
- **Per row**, a multibyte run records the shape of the reasons as loaded (`textShape`: every one
  exactly the requested runes, bytes taken, and a sample of the rune widths and combining marks), the
  bytes of the page responses (`traversal.responseBytes`, `maxPageBytes`), and, for a deletion,
  `outcome.textExact`: the SHA-256 of every kept reason's UTF-8 bytes before and after is equal and
  every removed reason is blank. Counting blank and non-blank rows alone cannot tell a kept reason
  that was mangled.
- **Digest agreement.** The harness reads the items the server issues before the browser opens the
  panel, then holds the `reasonReview` the browser sent against `set-v1.mjs agree`: the independent
  builder, given the browser's own choices, must reproduce its count, digest and choice vector byte
  for byte, and every choice must be keep or blank. The digest covers ids and review hashes, not text,
  so it does not depend on the charset; it is checked in every browser case, and a multibyte cell
  without it is invalid. The server's acceptance of the independent builder is the deletions'
  exact-outcome check. The browser's digest path (`crypto.subtle` or the bundled fallback) and its
  cost are recorded as before; `--browser-fallback-digest` adds the fallback case.
- **On-screen text.** At the end of the browser run the page's final screen is captured as it is: the
  pager line, and for each reason row its id, how many reason-text elements it holds (`reasonText` in
  `app-facts.json`; it must be one) and that element's exact text. After the run the harness reads
  the database's text for exactly the page the pager line names. The screen must be that page: the
  line in its finished form (`Reasons A to B of N`, N the reasons the server issued, a whole page
  starting on a page boundary), every expected row once and in order, no missing, unexpected or
  duplicated row, and each reason's text equal to the database's, to the rune (nothing appended, no
  mark dropped). The one reason the harness changed behind the page may show its old text or the new
  one and nothing else. Anything short of that is `browser-text-disagrees`; a screen that could not
  be captured, or a multibyte cell that compared no reason, is `browser-agreement-missing`. The rules
  are `domTextAgreement` in `qc1/response-accounting.mjs`, run by `set-v1.mjs dom-ids` and `dom-text`.
- **Calculated capacity and a history actually submitted are different things** and are recorded
  apart. `request.capacity` is computed from the format and never sent; `request.largest` is the
  biggest history the harness really submitted and deleted, and `request.overCap` is a body one
  reason over capacity that was really refused. A table holds at most the reasons it holds; the
  capacity is not claimed for a history nobody has.
- **Status.** `FULL` only when every cell is present, on one revision, and the status recomputed from
  its rows and environment is complete, coverage full and release evidence, with nothing missing,
  pending, invalid or aborted, a runner record that finished cleanly and no collection failure.
  Anything else is `PILOT`, and `matrix.json` lists each gap by name: an absent cell, a cell of
  another revision, a saved status that differs from the recomputed one, a runner record that is
  missing or failed, a `COLLECTION-FAILED.txt`, a cell whose coverage is not full, and
  the cases a cell reports as pending (the legacy discovery and legacy PUT cases are not built at
  the time of writing, so a real matrix is a `PILOT` until they are). If a 1M-row table cannot run on
  the available host, run the sizes that can and report the gap: a pilot, never `FULL`. A budget
  failure is a finding for engineering disposition (R4-C06), never a gap and never a waiver.
- The result goes in `docs/plan/M7.4-ERASURE-INVENTORY.md`, section 12.3, "#294/#293 scale matrix":
  the revision, environment, fixtures, timings, audits, WAL, memory, gaps and budget failures from
  `matrix.json`, with the sha256 of each artifact. The artifacts stay outside the checkout.
- **Checks that run without Docker**: `qc1-fixtures.sh` (the judge's charset, shape, exactness and
  agreement rules, the snapshot comparison, `set-v1.mjs agree` and `dom-text`, and the matrix report
  over four consistent full cells: the one FULL control and each way it is spoiled after the rows are
  complete), `qc1-panel-fixtures.mjs` (the on-screen comparison rule by rule and the rune accounting)
  and `make reason-review-scale-test` (the matrix report's own checks against stub evidence, none of
  which can be FULL). The `go` section of `qc1/app-facts.json` changes only by
  `UPDATE_QC1_FACTS=1 go test -run TestQC1AppFacts .`; its `web` section is held by
  `ReasonReviewSection.test.tsx`.

### Sampling Next changed reason (#298)

Issue #298 has one slow observation of "Next changed reason" (830.7 ms in a 100,000-reason browser
pilot, against a proposed 300 ms) and two fast ones (65.1 ms with a 1,000,000-row background, 60.6 ms
in an earlier diagnostic with an incomplete environment record). That is not enough to call a delay
repeatable or noise, so the harness presses the control many times and records the machine around it.

The press scans the in-memory store from the start of the page on screen for the next changed reason
and wraps once, so where it starts decides how far it scans. A sampling run presses it from three
origins in turn: the first page (a scan up to the changed reason, a third of the way in), the page after
the changed reason's (a scan of the whole review) and the last page (a wrap to the changed reason).
Press 0 follows the refused press and the second walk, which is where the single observation was made;
it is kept apart, because a first press on a freshly loaded page may differ from the rest. Moving to an
origin is a "Go to reason number" that is not counted as a measured jump.

What a press is timed as is unchanged: the page's own clock from the pointerdown to the frame after
the focused heading is the changed reason. Each press also records its origin, its offset in the
sequence and the page's long-task time between two reads just outside its interval, which separates a
slow press with the main thread busy from one with nothing running in the page. The run reads the host's
`/proc/loadavg`, `/proc/stat` (CPU steal, iowait, idle ticks) and `/proc/pressure/cpu` before the walk,
before and after the presses and at the end, and the row carries the differences; in a container these
are the counters of the kernel it runs on. A counter that cannot be read is `null`, never zero.

```bash
# fresh, new, outside the checkout; hours; a dedicated Docker daemon (see "Reason-review scale run")
E=~/.local/state/fold/next-changed/$(git rev-parse --short=12 HEAD)
scripts/reason-review-scale.sh --only qc1 --evidence-dir $E \
  --qc1-arg --sizes --qc1-arg 100000,1000000 --qc1-arg --counts --qc1-arg 0 --qc1-arg --dense-counts --qc1-arg 100000 \
  --qc1-arg --next-changed-only --qc1-arg --next-changed-presses --qc1-arg 30 --qc1-arg --next-changed-runs --qc1-arg 3
scripts/reason-review-next-changed.sh --out $E/next-changed.json --evidence $E/qc1
```

`scripts/reason-review-next-changed.sh --dry-run` prints the same commands. `--next-changed-only`
loads the background rows of each size and runs only the rendered panel at the dense count, once per
`--next-changed-runs` (cases `browser-panel-100000-nc1` to `-nc3`, each a fresh account, a fresh page
load and a fresh walk), so the discovery, deletion, request-size and plan matrices are not run. The run's
own `evidence-status.json` is then incomplete by construction and the wrapper's QC-1 step does not pass:
this is not QC-1 evidence, and the report below reads the rows, not the verdict. Running it again with
another `--evidence-dir` adds an independent stack to the same configurations; pass every `qc1`
directory with its own `--evidence`.

`reason-review-next-changed.sh` writes `next-changed.json`: for each configuration (a 100,000-reason
review over a 100,000-row and over a 1,000,000-row background) the counted runs, the pooled and the
per-run p50, p95 and max (nearest rank, the same as `percentile` in `qc1/panel-core.mjs`), the first
press of each run, the figures by origin, the slowest presses with their long-task time, and the load
context of each run. A run counts only when it was measured, stopped in no phase, answered by the
stack's own origin, walked exactly the reasons seeded, was refused with 409 and reloaded, pressed at
least 30 times and as often as it was asked (the recorded counts match the recorded presses, whose
indices run 0..N-1 from the runner's origins with nonnegative times and a matching first press), was
not profiled, carries numeric host load counters, comes from
a directory whose environment is a complete record of the revision asked for, and is clean under the
existing QC judge recomputed from the recorded rows (`qc1_judge` over the directory's
`measurements.jsonl`): an invalid finding for the row excludes it while a budget finding is kept
beside the row and never excludes it, and the directory's own `duplicate-row` findings are honored
beside the recompute. A directory whose artifacts could not be collected, whose run
aborted, whose environment is incomplete, whose `evidence-status.json` is missing, unreadable or
malformed, that names an expected sampling measurement as missing (a standard-shaped `{item, size,
case}` entry, the reason count parsed from its case name), or whose outer runner record is present
but unfinished, unbound, unreadable or malformed, marks no single well-formed QC-1 step for this
directory and revision, or marks its QC-1 step interrupted, failed with a nonzero exit, passed or
partial with a contradicting exit or with a summary that contradicts the verdict or the directory's
validated status (the exit must be a JSON integer matching the verdict, and a pass or partial summary
must have the runner's shape and say what the directory's status says), or failed with exit 0 for any
reason beyond intentional omissions, contributes runs that are listed but never counted; the one
accepted outer shape is an
exit-0 failure explained entirely by the sampling design omitting the full matrix, proven by the
step's own summary agreeing with the directory's validated status. Expected sampling-only omissions
(other missing rows, legacy pending cases, pilot coverage) are recorded, not failures. A repeated
`--evidence` directory is refused; an identical record copied across directories of one stack is
counted once, while a repeated or conflicting identity within one measurements file, or conflicting
records of one identity across directories of one stack, excludes every occurrence — and identity
is the canonical original record, so equal-timed copies that differ in status, window, agreement,
sampling, page accounting or other recorded validity facts conflict instead of collapsing. A malformed
nested value (a non-object press, a non-string origin, a scalar `browser.failed`) excludes its own row
with its original identity and conflict evidence kept, never the directory's other rows; a directory
whose sampling rows cannot be projected at all fails the record, so hidden conflicts can never read
`COMPLETE`. The record is `COMPLETE` (exit 0) only with at least 3 counted runs in both
configurations; otherwise `INCOMPLETE` (exit 10) and each gap is named, and a configuration with no
counted run has no figures at all.

The record decides nothing. It does not call a delay noise and does not call the target met; the target
is `QC1_NAV_BUDGET_MS`, an engineering proposal of the scaling design and not an owner decision. Its one
stated consequence is the issue's own: when a configuration's pooled p95 is over the target, profile
before changing anything. Repeat the sampling with `--qc1-arg --profile-next-changed`: a V8 CPU profile of
the page across the presses is written beside each result as a `.cpuprofile`
(`browser-<size>-<count>-nc<N>.json.nextchanged.cpuprofile`) for DevTools or speedscope. The profiler
adds sampling overhead, so a profiled run is a diagnostic and the record never counts it. A fix to the
control follows a reproduction and a profile, with a Vitest on a 100,000-reason store that fails
without it; none is written before then.

- The result goes in `docs/plan/M7.4-ERASURE-INVENTORY.md`, section 12.3, "#298 navigation samples",
  copied from `next-changed.json`, with the original observations kept beside it.
- **Checks that run without Docker**: `qc1-panel-fixtures.mjs` (the repeated presses, their origins,
  the uncounted moves, a run that stops part-way, the load deltas, the profiler and the parsers) and
  `make reason-review-scale-test` (`reason-review-next-changed.sh --self-test`: hand-computed p50, p95
  and max over stub presses, the runs it must refuse to count, pooling across stacks, and no figures
  without a run).

#### Deletion diagnostic (`--delete-probe`, issue #294)

A one-off run for one question: on a churn-free 1,000,000-row multibyte spread fixture, what do four
deletions of a 100,000-reason multibyte subject (every reason removed) do, and how does that differ
when `audit_logs` holds seq 164's per-action counts (M) rather than fresh audits (F)? It is a
diagnostic. It is never QC-1 evidence or a matrix cell, it changes no budget, judge rule or gate, a
probe 503 is judged like any 503, and seq 164 stays a named operation failure whatever it shows.

```bash
E=~/.local/state/fold/t294-delete-probe/$(git rev-parse --short=12 HEAD)-$(date -u +%Y%m%dT%H%M%SZ)
scripts/reason-review-scale.sh --only qc1 --evidence-dir $E \
  --qc1-arg --sizes --qc1-arg 1000000 --qc1-arg --counts --qc1-arg 0 --qc1-arg --dense-counts --qc1-arg none \
  --qc1-arg --charset --qc1-arg multibyte --qc1-arg --layout --qc1-arg spread --qc1-arg --no-browser --qc1-arg --delete-probe
```

The flag is refused unless those settings are exactly these. The wrapper exits non-zero: the evidence is
incomplete by construction (every other case is missing), and `settings.deleteProbe` in `environment.json`
says why. Read `$E/qc1/probe-summary.json` (`qc1_probe_summary` recomputes it from `measurements.jsonl`).

- **Sequence.** The 1,000,000-row filler loads once. One real 10-reason deletion then writes the audit rows
  that M copies (`qc1_audit_template`, one row per action of seq 164). Replicates run F, M, M, F, each after
  the same reset: scenario dropped, `contact_requests` VACUUMed, `audit_logs` truncated. Each then loads the
  100,000-reason subject and walks it. M tops `audit_logs` up to the target after that walk, by exactly the
  rows each action lacks; one scan sets the hint bits and records bytes and rows with an ip by action, in both
  arms. The rows are `delete-subject-100000-remove-probe<F|M>-r<N>`. The SQL touches only the disposable
  database this run made; the top-up never uses replica mode and `TRUNCATE` has no `CASCADE`.
- **What it reports.** Per replicate: outcome class, request ms beside the 40 s budget, `audit_logs` bytes
  beside seq 164's 480,305,152 (the ratio is reported, not matched), the audit histogram at the start of the
  window, the top 40 statements with `blkReadMs`, the single-client flags, and whether the replicate is
  `usable` for comparison. The audit flag (`isAudit`) is decided from the full query text before the
  200-character display cut, so the long retention report counts even though `audit_logs` falls outside its
  display. Unavailable statement evidence stays `null`, never zero; missing commit or sampler evidence is
  named in `measurementInvalid` and makes the replicate unusable. Statement times are
  cluster-wide `pg_stat_statements` counters, not attributed to the deletion; they belong to it only when
  `singleClient` shows one client, no autovacuum and no scheduler overlap. Only collected statements are
  summed: the harness text filter and `LIMIT` omit some outright, cancelled statements are absent, and ties
  and rounding make `smallestListedMs` approximate, not a strict bound.
- **Rollback evidence.** A non-200 reads the account's reasons, the account row and the erasure's own audit
  actions after the watermark (`qc1_probe_atomicity`). A read that fails twice or is malformed is
  `invalid:atomicity-unreadable`, never a finding; `nonAtomic:*` needs clean reads that show a change. The
  scrubs of other accounts' audit rows are not read.
- **Reading it.** Two replicates per arm on one host report what happened; they give no rate and no cause,
  sufficiency or necessity, whatever the split. `differences` and `limits` in the summary name what the
  fixture does not reproduce (no churn, a rebuilt `audit_logs`, one shared filler, no host counters). Before
  reading, run `qc1_probe_readiness` on the collected evidence directory: it must print `ready`. It refuses
  a stopped run (`abort.json`), a failed or inconsistent copy (`COLLECTION-FAILED.txt`), an unfinished
  finalization (`environment.json` incomplete), missing records, row coverage under four replicates, and any
  replicate that is not `usable` — only `completed`, `timedOutAtomic` and `refused:<known code>` with clean
  measurement evidence are expected reasons. A run that stops early is `incomplete` and gets no reading.
  Record the result in `docs/plan/M7.4-ERASURE-INVENTORY.md` 12.3 beside seq 164 as DIAGNOSTIC.
- **Checks that run without Docker**: `qc1-fixtures.sh` (the flag refusals, every rollback class and failed
  read, every outcome class and competitor, the full-text audit flag with the long retention shape, invalid
  measurement evidence that never reads as zero, the judge on the exact diagnostic metadata of a 200 and a
  503, every readiness state, and the order of the reset in the source). The SQL runs only under Docker: the template, the top-up and the scan are first proven by the real
  run, and a mismatch shows as `stateUnmatched` or ends the run.

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
