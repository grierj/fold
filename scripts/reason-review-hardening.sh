#!/usr/bin/env bash
# The extended hardening run for contact-request reason review (issue #296). It
# is bounded, repeatable and not part of CI: it runs longer than the normal
# checks, on the real tests, and leaves a record bound to the revision it ran.
#
#   scripts/reason-review-hardening.sh [options]
#
#   --fuzz-time T      how long to fuzz FuzzReasonEnvelope: Ns, Nm, Nh or Nx
#                      (a count of inputs); default 60s, at most 1h
#   --race-count N     times to run each in-memory race test under the race
#                      detector; default 10, at most 1000
#   --seeds N          seeded randomized runs for the browser digest and review
#                      store tests; default 50, at most 5000
#   --seed-base S      first seed of those runs (default 20261111); the race
#                      tests use S as their operation seed
#   --seed S           replay one seed only (the value a failure printed)
#   --evidence-dir D   where the record goes; it must be outside this checkout
#                      and new or empty (a directory that already holds files is
#                      refused). Default: $FOLD_EVIDENCE_DIR, else
#                      ${XDG_STATE_HOME:-~/.local/state}/fold/reason-review-hardening,
#                      then a directory named for the revision and the time.
#
# Steps, each a real command on the real targets (a step whose tests did not
# all report PASS fails; nothing is skipped, and "no tests ran" is a failure):
#
#   normal   the deterministic Go vectors and envelope tests, and the seed
#            corpus of FuzzReasonEnvelope
#   fuzz     go test -fuzz FuzzReasonEnvelope for --fuzz-time
#   race     the in-memory attempt ledger and restore-fence tests under -race
#   web      the browser digest and review-store tests, shared vectors and
#            --seeds seeded randomized runs
#
# Needs: Go; for the race step CGO_ENABLED=1 for that one command and a C
# compiler (CC, else the one `go env CC` names), checked first and reported if
# missing, so a machine without them fails instead of passing without the race
# detector; for the web step `npm ci` in web/. The shipped build stays
# CGO_ENABLED=0; this script sets CGO_ENABLED=1 for the race test process only.
# The race and fuzz steps need no database. The race detector says nothing
# about PostgreSQL lock correctness: the deterministic lock tests in the normal
# checks decide that.
#
# The record, in the evidence directory: evidence.json (revision, tree, whether
# the working tree was clean, versions, parameters, and each step's command,
# start, seconds, exit status and log), one log per step, web-seeds.log (a line
# per seed that ran, with its trace on failure) and failure-corpus/ (any input
# the fuzzer saved). A failing fuzz input is also left in
# testdata/fuzz/FuzzReasonEnvelope/, where `go test` replays it; deleting it is
# the decision to call it fixed or not a bug. The record starts as incomplete
# before the first step, and a signal finalizes it, so an interrupted rerun
# cannot keep a previous pass; the completed record is parsed as JSON before
# success is reported. Seed spellings are canonical (no leading zeros).
# Exit status: 0 only when every step passed.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$root"

die() {
	echo "reason-review-hardening: $*" >&2
	exit 2
}

fuzz_time=60s
race_count=10
seeds=50
seed_base=20261111
single_seed=
evidence_root=${FOLD_EVIDENCE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/fold/reason-review-hardening}
evidence=
failure_files=()

while (($#)); do
	case $1 in
	--fuzz-time | --race-count | --seeds | --seed-base | --seed | --evidence-dir)
		(($# >= 2)) || die "$1 needs a value"
		case $1 in
		--fuzz-time) fuzz_time=$2 ;;
		--race-count) race_count=$2 ;;
		--seeds) seeds=$2 ;;
		--seed-base) seed_base=$2 ;;
		--seed) single_seed=$2 ;;
		--evidence-dir) evidence=$2 ;;
		esac
		shift 2
		;;
	-h | --help)
		sed -n '2,/^set -euo/p' "$0" | sed '$d;s/^# \{0,1\}//'
		exit 0
		;;
	*) die "unknown option $1 (see --help)" ;;
	esac
done

[[ $fuzz_time =~ ^([0-9]+)(s|m|h|x)$ ]] || die "--fuzz-time must look like 90s, 5m, 1h or 5000x"
fuzz_n=${BASH_REMATCH[1]}
fuzz_unit=${BASH_REMATCH[2]}
((fuzz_n > 0)) || die "--fuzz-time must be above zero"
case $fuzz_unit in
s) ((fuzz_n <= 3600)) || die "--fuzz-time is at most 1h" ;;
m) ((fuzz_n <= 60)) || die "--fuzz-time is at most 1h" ;;
h) ((fuzz_n <= 1)) || die "--fuzz-time is at most 1h" ;;
x) ((fuzz_n <= 100000000)) || die "--fuzz-time is at most 100000000x" ;;
esac
[[ $race_count =~ ^[1-9][0-9]{0,3}$ ]] || die "--race-count must be 1 to 1000"
((race_count <= 1000)) || die "--race-count must be 1 to 1000"
[[ $seeds =~ ^[1-9][0-9]{0,3}$ ]] || die "--seeds must be 1 to 5000"
((seeds <= 5000)) || die "--seeds must be 1 to 5000"
[[ $seed_base =~ ^(0|[1-9][0-9]{0,14})$ ]] || die "--seed-base must be a canonical whole number (no leading zeros) of at most 15 digits"
[[ -z $single_seed || $single_seed =~ ^(0|[1-9][0-9]{0,14})$ ]] || die "--seed must be a canonical whole number (no leading zeros) of at most 15 digits"
if [[ -n $single_seed ]]; then
	seeds=1
	seed_base=$single_seed
fi

command -v go >/dev/null 2>&1 || die "go is not installed"
command -v npm >/dev/null 2>&1 || die "npm is not installed (the web step needs it)"
[[ -d web/node_modules ]] || die "web/node_modules is missing: run 'cd web && npm ci' first"

# The race detector needs cgo, so a C compiler. Check before anything runs, and
# prove it with a build of a trivial program in a scratch module.
cc=${CC:-$(go env CC)}
cc_word=${cc%% *}
command -v "$cc_word" >/dev/null 2>&1 ||
	die "the race step needs a C compiler and '$cc_word' was not found (install gcc, or set CC): without it the race detector cannot run, so this run would prove nothing"
probe=$(mktemp -d)
trap 'rm -rf "$probe"' EXIT
printf 'module probe\n\ngo 1.21\n' >"$probe/go.mod"
printf 'package main\n\nfunc main() {}\n' >"$probe/main.go"
if ! (cd "$probe" && CGO_ENABLED=1 go build -race -o "$probe/probe" . >"$probe/out.log" 2>&1); then
	cat "$probe/out.log" >&2
	die "the race detector cannot build here (it needs CGO_ENABLED=1, a C toolchain and a GOOS/GOARCH the race detector covers): this run would prove nothing"
fi

revision=$(git rev-parse HEAD)
tree=$(git rev-parse 'HEAD^{tree}')
dirty=false
diff_sha=
if [[ -n $(git status --porcelain --untracked-files=normal) ]]; then
	dirty=true
	diff_sha=$( (git diff HEAD && git ls-files --others --exclude-standard -z | sort -z | xargs -0 -r sha256sum) | sha256sum | cut -d' ' -f1)
fi
stamp=$(date -u +%Y%m%dT%H%M%SZ)
[[ -n $evidence ]] || evidence="$evidence_root/${revision:0:12}-$stamp"
if [[ -e $evidence ]]; then
	[[ -d $evidence ]] || die "the evidence destination $evidence exists and is not a directory"
	[[ -z $(ls -A "$evidence") ]] || die "the evidence directory $evidence is not empty; each run writes a fresh directory, so an interrupted rerun cannot keep a previous pass"
else
	mkdir -p "$evidence"
fi
evidence=$(cd "$evidence" && pwd -P)
case $evidence in
"$root" | "$root"/*) die "the evidence directory $evidence is inside the checkout; it must be outside" ;;
esac
mkdir -p "$evidence/failure-corpus"

json() { # a JSON string
	local s=$1
	s=${s//\\/\\\\}
	s=${s//\"/\\\"}
	s=${s//$'\n'/\\n}
	s=${s//$'\t'/\\t}
	s=${s//$'\r'/\\r}
	printf '"%s"' "$s"
}
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

started=$(now)
steps_json=
overall=0

# write_evidence RESULT writes evidence.json with the steps recorded so far, so
# an interrupted run keeps an incomplete record instead of a previous pass.
# shellcheck disable=SC2329 # also run through the signal trap below
write_evidence() {
	local result=$1 artifacts="" f=""
	for f in "${failure_files[@]}"; do
		[[ -z $artifacts ]] || artifacts+=","
		artifacts+=$(json "$f")
	done
	local node_version
	node_version=$(command -v node >/dev/null 2>&1 && node --version || echo none)
	cat >"$evidence/evidence.json" <<EOF
{
  "schema": "fold/reason-review-hardening/v1",
  "issue": 296,
  "revision": "$revision",
  "tree": "$tree",
  "workingTreeClean": $([[ $dirty == false ]] && echo true || echo false),
  "workingTreeDiffSha256": $(json "$diff_sha"),
  "startedAt": "$started",
  "finishedAt": "$(now)",
  "host": $(json "$(uname -sm)"),
  "go": $(json "$(go version)"),
  "node": $(json "$node_version"),
  "cc": $(json "$cc"),
  "parameters": {
    "fuzzTime": "$fuzz_time",
    "raceCount": $race_count,
    "webSeeds": $seeds,
    "seedBase": $seed_base,
    "singleSeed": $([[ -n $single_seed ]] && echo "$single_seed" || echo null),
    "raceOperationSeed": $seed_base
  },
  "steps": [$steps_json
  ],
  "failureArtifacts": [$artifacts],
  "result": "$result"
}
EOF
}

write_evidence incomplete
trap 'write_evidence incomplete; echo "reason-review-hardening: interrupted; record: $evidence/evidence.json (incomplete)" >&2; exit 130' INT TERM

# run_step NAME LOGFILE COMMAND...: runs the command with its output in the log
# and leaves the outcome in the step_* variables. A failed step does not stop
# the run, so the record is complete. The caller may then call check_step, and
# must call record_step once.
run_step() {
	local name=$1 log=$2
	shift 2
	local s0 s1
	step_name=$name step_log=$log
	step_cmd=$(printf '%q ' "$@")
	step_begin=$(now)
	s0=$SECONDS
	echo "== $name: $step_cmd"
	set +e
	"$@" >"$evidence/$log" 2>&1
	step_status=$?
	set -e
	s1=$SECONDS
	step_end=$(now)
	step_secs=$((s1 - s0))
}

# check_step MESSAGE COMMAND...: a step whose command passed is still failed
# when its evidence is not complete (the command, run with the step's log, fails).
check_step() {
	local message=$1
	shift
	((step_status == 0)) || return 0
	if ! "$@"; then
		step_status=3
		echo "$message" >>"$evidence/$step_log"
		echo "   $message"
	fi
}

record_step() {
	local verdict=pass
	if ((step_status != 0)); then
		verdict=fail
		overall=1
		echo "   FAILED (exit $step_status), log: $evidence/$step_log"
		tail -n 30 "$evidence/$step_log" | sed 's/^/   | /'
	else
		echo "   ok in ${step_secs}s"
	fi
	[[ -z $steps_json ]] || steps_json+=","
	steps_json+=$'\n    {"name":'"$(json "$step_name")"',"command":'"$(json "$step_cmd")"',"startedAt":"'"$step_begin"'","finishedAt":"'"$step_end"'","seconds":'"$step_secs"',"exit":'"$step_status"',"verdict":"'"$verdict"'","log":'"$(json "$step_log")"'}'
}

# Every named test must have reported PASS exactly COUNT times in the log, and
# the log must hold no skipped test and no data race.
# shellcheck disable=SC2329 # run through check_step
all_passed() { # LOG COUNT NAME...
	local log=$1 count=$2 name got
	shift 2
	local bad=0
	for name in "$@"; do
		got=$(grep -c -E "^[[:space:]]*--- PASS: $name( |\$)" "$log" || true)
		if ((got != count)); then
			echo "test $name reported PASS $got times, expected $count" >>"$log"
			bad=1
		fi
	done
	if grep -q -E -- '--- SKIP|WARNING: DATA RACE' "$log"; then
		echo "the log holds a skipped test or a data race" >>"$log"
		bad=1
	fi
	return "$bad"
}

# --- normal: the deterministic targets ---------------------------------------
normal_tests=(
	TestReasonSetDigestVectors
	TestReasonSetGeneratedVectors
	TestReasonEnvelopeStrict
	TestReasonEnvelopeTopKeys
	TestReasonEnvelopeEscapedKeys
	TestReasonEnvelopeEscapedTopKeys
	TestReasonEnvelopeEscapesAreReal
	FuzzReasonEnvelope
)
normal_pattern="^($(
	IFS='|'
	echo "${normal_tests[*]}"
))\$"
run_step normal normal.log go test -count=1 -v -run "$normal_pattern" .
check_step "a deterministic test did not report PASS exactly once" all_passed "$evidence/normal.log" 1 "${normal_tests[@]}"
record_step

# --- fuzz --------------------------------------------------------------------
corpus=testdata/fuzz/FuzzReasonEnvelope
corpus_files() { find "$corpus" -maxdepth 1 -type f -printf '%f\n' 2>/dev/null | sort || true; }
before_corpus=$(corpus_files)
run_step fuzz fuzz.log go test -run '^$' -fuzz '^FuzzReasonEnvelope$' -fuzztime "$fuzz_time" .
after_corpus=$(corpus_files)
failure_files=()
while IFS= read -r f; do
	[[ -n $f ]] || continue
	cp "$corpus/$f" "$evidence/failure-corpus/$f"
	failure_files+=("failure-corpus/$f")
	echo "   saved failing input $corpus/$f (copied to $evidence/failure-corpus/)"
done < <(comm -13 <(printf '%s\n' "$before_corpus") <(printf '%s\n' "$after_corpus"))
record_step

# --- race --------------------------------------------------------------------
race_tests=(
	TestRaceAttemptLedgerOneWinnerPerID
	TestRaceAttemptLedgerMixedOperations
	TestRaceAttemptLedgerCapIsExact
	TestRaceAttemptLedgerPruneWhileInUse
	TestRaceFenceStateRaiseKeepsTheMaximum
	TestRaceFenceStateTurnIsExclusive
	TestRaceNoteRestoreDetectedRestartsOnce
)
race_pattern="^($(
	IFS='|'
	echo "${race_tests[*]}"
))\$"
run_step race race.log env CGO_ENABLED=1 FOLD_REASON_SEED="$seed_base" go test -race -count="$race_count" -timeout 30m -v -run "$race_pattern" .
check_step "a race test did not report PASS $race_count times, or a race or skip was reported" all_passed "$evidence/race.log" "$race_count" "${race_tests[@]}"
record_step

# --- web ---------------------------------------------------------------------
: >"$evidence/web-seeds.log"
web_env=(FOLD_REASON_SEED_BASE="$seed_base" FOLD_REASON_RUNS="$seeds" FOLD_REASON_EVIDENCE="$evidence/web-seeds.log")
[[ -z $single_seed ]] || web_env+=(FOLD_REASON_SEED="$single_seed")
# shellcheck disable=SC2329 # run through run_step
web_cmd() { (cd web && env -u FOLD_REASON_SEED "${web_env[@]}" npm test -- src/lib/reasonSetDigest.test.ts src/lib/reasonReviewStore.test.ts); }
# shellcheck disable=SC2329 # run through check_step
seeds_completed() {
	# The exact seed set the manifest records: the one replay seed, or the
	# base run. An inherited FOLD_REASON_SEED is cleared above, so only these
	# may appear.
	local want=()
	if [[ -n $single_seed ]]; then
		want=("$single_seed")
	else
		local s
		for ((s = 0; s < seeds; s++)); do want+=("$((seed_base + s))"); done
	fi
	local missing=0 seed
	for seed in "${want[@]}"; do
		grep -q -E -- "^digest seed $seed ok" "$evidence/web-seeds.log" || {
			echo "digest seed $seed did not complete (see web-seeds.log)" >>"$evidence/web.log"
			missing=1
		}
		grep -q -E -- "^store seed $seed ok" "$evidence/web-seeds.log" || {
			echo "store seed $seed did not complete (see web-seeds.log)" >>"$evidence/web.log"
			missing=1
		}
	done
	if grep -q -E -- " FAILED" "$evidence/web-seeds.log"; then
		echo "a randomized seed reported FAILED (see web-seeds.log)" >>"$evidence/web.log"
		missing=1
	fi
	return "$missing"
}
run_step web web.log web_cmd
check_step "not every requested seed completed" seeds_completed
record_step

# --- the record --------------------------------------------------------------
result=pass
((overall == 0)) || result=fail
write_evidence "$result"
node -e 'JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"))' "$evidence/evidence.json" ||
	die "the completed evidence record is not valid JSON: $evidence/evidence.json"
echo "record: $evidence/evidence.json ($result at ${revision:0:12}$([[ $dirty == true ]] && echo ', working tree not clean'))"
if ((overall != 0)); then
	echo "to replay a randomized failure: scripts/reason-review-hardening.sh --seed <seed from the log> --fuzz-time 1s --race-count 1" >&2
fi
exit "$overall"
