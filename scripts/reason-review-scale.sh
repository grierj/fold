#!/usr/bin/env bash
# The scale and recovery run for contact-request reason review (issue #297): the
# QC-1 measurement and the restore drill behind one entry point, with one
# revision-bound record. It is long (hours), needs a disposable Docker daemon,
# and is not part of CI.
#
#   scripts/reason-review-scale.sh [options]
#
#   --evidence-dir D   where the record goes; it must be outside this checkout and
#                      new or empty (a directory that already holds files is
#                      refused). Default: $FOLD_EVIDENCE_DIR, else
#                      ${XDG_STATE_HOME:-~/.local/state}/fold/reason-review-scale,
#                      then a directory named for the revision and the time.
#   --runner R         nested (default): each step runs inside a throwaway container
#                      with its own Docker daemon (restore-drill-nested.sh), the way
#                      the drill and QC-1 are meant to be run on a workstation.
#                      direct: qc1-measure.sh and restore-drill.sh run as they are,
#                      for a machine that already is that disposable host.
#   --only STEP        qc1 or drill; the default runs both. A run of one step is
#                      recorded as partial.
#   --crossings-only   run the drill as its PARTIAL diagnostic (a pass exits 10 and
#                      is never acceptance). Recorded as partial.
#   --drill-reason T   why the full drill is being run now. Required for a full
#                      drill step; docs/agents/deploy.md ("Full restore drill
#                      frequency") says when that is warranted, and this is the only
#                      place that rule lives. Recorded with the run date.
#   --qc1-arg A        pass A through to qc1-measure.sh (repeat for each word, e.g.
#                      --qc1-arg --sizes --qc1-arg 10000). A run with any
#                      pass-through argument is recorded as partial.
#   --drill-arg A      the same for restore-drill.sh.
#   --dry-run          print the steps, the matrix they cover, the coverage the run
#                      asks for and the record path, and stop. Starts no container
#                      and writes nothing.
#   --self-test        run this script's own checks against stub tools in a scratch
#                      repository (seconds, no Docker); see `make reason-review-scale-test`.
#
# Steps, each the real tool, in this order:
#
#   qc1     deploy/digitalocean/qc1-measure.sh (through restore-drill-nested.sh --qc1
#           when the runner is nested): reason review at 10k, 100k and 1M
#           contact_requests rows, held to the smallest recommended droplet. It
#           measures; budget failures are listed in the record for a reviewed
#           disposition and do not fail the step. The step fails when the tool exits
#           non-zero or its evidence-status.json is missing, unreadable or not
#           complete, or its environment.json names another revision.
#   drill   deploy/digitalocean/restore-drill.sh (through restore-drill-nested.sh):
#           the disposable restore proof. A full run must exit 0 and leave a
#           restore-drill.json for this revision.
#
# A failed step does not stop the run, so the record is complete. The tools demand
# a clean, committed HEAD; this script refuses a dirty tree before it creates
# anything.
#
# The record, in the evidence directory: evidence.json, one log per step
# (qc1.log, drill.log), and each tool's own artifacts in qc1/ and drill/ (new
# paths the tools create; the script never makes them). evidence.json holds the
# revision, tree, that the working tree was clean, the tool files' sha256, the
# parameters, the coverage that was asked for and the coverage that was reached,
# and for each step its command, start, end, seconds, exit status, verdict, log,
# artifact directory and summary. The record starts as `incomplete` before the
# first step and a signal finalizes it as such, so an interrupted rerun cannot
# keep a previous pass; the completed record is parsed as JSON before the script
# reports it.
#
# coverage.kind is "full" only when the run asked for the whole matrix (both steps,
# the full drill, no pass-through arguments) and every step reached it: the QC-1
# evidence-status.json says complete, coverage "full" and releaseEvidence true, and
# the drill exited 0. Anything less is "partial", with the reasons listed. A record
# that is still incomplete, or failed, is never "full".
#
# result: incomplete (running or interrupted), fail (a step failed), partial (every
# requested step ran but coverage is not full) or pass (full coverage).
# Exit status: 0 only for pass; 10 for partial; 1 for fail; 2 for a refusal before
# any step ran; 129, 130 or 143 after HUP, INT or TERM (the step's process group is
# told to stop, up to about 2 minutes for the nested runner to remove its container,
# then killed, and the record is left incomplete).
#
# Needs: bash with job control (set -m), git, jq, sha256sum, date, uname, mktemp;
# for a real run, whatever the tools need (docker, go, node; see their headers).
# Neither a --dry-run nor --self-test needs Docker. Linux with GNU coreutils;
# macOS is unverified. SIGINT must not have been ignored when the script started
# (a plain `cmd &` from a shell without job control does that); the script says so
# on stderr, and TERM still works.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$root"

die() {
	echo "reason-review-scale: $*" >&2
	exit 2
}

nested_tool=deploy/digitalocean/restore-drill-nested.sh
qc1_tool=deploy/digitalocean/qc1-measure.sh
drill_tool=deploy/digitalocean/restore-drill.sh

runner=nested
only=
crossings=0
drill_reason=
dry_run=0
self_test=0
evidence=
qc1_args=()
drill_args=()
evidence_root=${FOLD_EVIDENCE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/fold/reason-review-scale}

while (($#)); do
	case $1 in
	--evidence-dir | --runner | --only | --drill-reason | --qc1-arg | --drill-arg)
		(($# >= 2)) || die "$1 needs a value"
		case $1 in
		--evidence-dir) evidence=$2 ;;
		--runner) runner=$2 ;;
		--only) only=$2 ;;
		--drill-reason) drill_reason=$2 ;;
		--qc1-arg) qc1_args+=("$2") ;;
		--drill-arg) drill_args+=("$2") ;;
		esac
		shift 2
		;;
	--crossings-only)
		crossings=1
		shift
		;;
	--dry-run)
		dry_run=1
		shift
		;;
	--self-test)
		self_test=1
		shift
		;;
	-h | --help)
		sed -n '2,/^set -euo/p' "$0" | sed '$d;s/^# \{0,1\}//'
		exit 0
		;;
	*) die "unknown option $1 (see --help)" ;;
	esac
done

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# --- the self-test: stub tools in a scratch repository ------------------------
# The script derives its root from its own location, so a copy placed in a
# scratch git repository whose deploy/digitalocean tools are stubs runs the whole
# flow (refusals, dry run, record shape, partial and failed runs, a signal)
# without Docker. Nothing here can touch the real tools.
self_test_main() {
	local tmp repo stubs me out rc pid i
	tmp=$(mktemp -d)
	tmp=$(cd "$tmp" && pwd -P)
	# shellcheck disable=SC2064 # expand now: $tmp is fixed
	trap "rm -rf '$tmp'" EXIT
	repo=$tmp/repo
	stubs=$repo/deploy/digitalocean
	me=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)/$(basename "${BASH_SOURCE[0]}")
	mkdir -p "$repo/scripts" "$stubs"
	cp "$me" "$repo/scripts/reason-review-scale.sh"
	# One stub body stands in for all three tools; the direct tools hand over to it.
	cat >"$stubs/$(basename "$nested_tool")" <<'STUB'
#!/usr/bin/env bash
set -eu
kind=${STUB_AS:-drill}
out=
while (($#)); do
	case $1 in
	--out | --dir) out=$2; shift 2 ;;
	--qc1) kind=qc1; shift ;;
	--crossings-only) kind=crossings; shift ;;
	--) shift; break ;;
	*) echo "stub: unexpected argument $1" >&2; exit 64 ;;
	esac
done
{ printf '%s' "$kind"; printf ' %s' "$@"; printf '\n'; } >>"$STUB_LOG"
[[ -z ${STUB_SNAPSHOT:-} ]] || cp "$STUB_RECORD" "$STUB_SNAPSHOT.$kind"
mkdir -p "$out"
rev=$(git rev-parse HEAD)
if [[ $kind == qc1 ]]; then
	jq -n --arg rev "${STUB_QC1_REVISION:-$rev}" '{revision:$rev}' >"$out/environment.json"
	case ${STUB_QC1:-full} in
	full) echo '{"complete":true,"coverage":{"kind":"full"},"releaseEvidence":true,"budgetFailures":[],"invalid":[]}' >"$out/evidence-status.json" ;;
	pilot) echo '{"complete":true,"coverage":{"kind":"pilot"},"releaseEvidence":false,"budgetFailures":[{"x":1},{"x":2}],"invalid":[]}' >"$out/evidence-status.json" ;;
	incomplete) echo '{"complete":false,"coverage":{"kind":"full"},"releaseEvidence":false,"budgetFailures":[],"invalid":[]}' >"$out/evidence-status.json" ;;
	badjson) echo 'not json' >"$out/evidence-status.json" ;;
	none) ;;
	esac
	exit "${STUB_QC1_RC:-0}"
fi
jq -n --arg rev "${STUB_DRILL_REVISION:-$rev}" '{revision:$rev}' >"$out/restore-drill.json"
if [[ -n ${STUB_SLEEP:-} ]]; then
	echo $$ >"$STUB_PID"
	: >"$STUB_READY"
	exec sleep "$STUB_SLEEP"
fi
if [[ $kind == crossings ]]; then exit "${STUB_DRILL_RC:-10}"; fi
exit "${STUB_DRILL_RC:-0}"
STUB
	# shellcheck disable=SC2016 # the stub expands $0 and $@ itself
	printf '#!/usr/bin/env bash\nSTUB_AS=qc1 exec bash "$(dirname "$0")/%s" "$@"\n' "$(basename "$nested_tool")" >"$stubs/$(basename "$qc1_tool")"
	# shellcheck disable=SC2016 # the stub expands $0 and $@ itself
	printf '#!/usr/bin/env bash\nSTUB_AS=drill exec bash "$(dirname "$0")/%s" "$@"\n' "$(basename "$nested_tool")" >"$stubs/$(basename "$drill_tool")"
	git -C "$repo" init -q
	git -C "$repo" add -A
	git -C "$repo" -c user.name=scale-test -c user.email=scale-test@example.invalid -c commit.gpgsign=false commit -q -m stubs

	local failures=0
	t_fail() {
		echo "self-test FAILED: $*" >&2
		failures=$((failures + 1))
	}
	# t_run NAME ENV... -- ARGS...: run the copy, leave rc and the evidence dir (tmp/NAME) behind.
	t_run() {
		local name=$1
		shift
		local envs=()
		while (($#)) && [[ $1 != -- ]]; do
			envs+=("$1")
			shift
		done
		shift
		rm -f "$tmp/log"
		rc=0
		(cd "$repo" && env "STUB_LOG=$tmp/log" "STUB_RECORD=$tmp/$name/evidence.json" "${envs[@]}" \
			bash scripts/reason-review-scale.sh --evidence-dir "$tmp/$name" "$@") >"$tmp/$name.out" 2>&1 || rc=$?
	}
	t_check() { # NAME DESCRIPTION JQ-FILTER
		jq -e "$3" "$tmp/$1/evidence.json" >/dev/null 2>&1 || t_fail "$1: $2 ($3)"
	}
	t_rc() { [[ $rc == "$2" ]] || t_fail "$1: exit $rc, want $2: $(tail -n 5 "$tmp/$1.out" | tr '\n' ' ')"; }

	# A full pass: the record shape is pinned here.
	t_run full STUB_SNAPSHOT="$tmp/snap" -- --drill-reason "pinned by the self-test"
	t_rc full 0
	t_check full "a full pass" '.result == "pass" and .coverage.kind == "full" and (.coverage.reasons == []) and .requested.coverage == "full"'
	t_check full "identity" '.schema == "fold/reason-review-scale/v1" and .issue == 297 and (.revision | test("^[0-9a-f]{40}$")) and (.tree | test("^[0-9a-f]{40}$")) and .workingTreeClean == true'
	t_check full "times and host" '(.startedAt | test("^[0-9]{4}-.*Z$")) and (.finishedAt | test("^[0-9]{4}-.*Z$")) and (.host | length > 0)'
	t_check full "tools are hashed" '(.tools | keys) == ["drill","nested","qc1"] and ([.tools[] | .sha256 | test("^[0-9a-f]{64}$")] | all)'
	t_check full "requested" '.requested.steps == ["qc1","drill"] and .requested.runner == "nested" and .requested.drillMode == "full" and .requested.reasons == [] and .requested.drillReason == "pinned by the self-test"'
	t_check full "two passing steps" '[.steps[] | .name] == ["qc1","drill"] and ([.steps[] | .verdict] | all(. == "pass")) and ([.steps[] | .exit] | all(. == 0))'
	t_check full "step fields" '[.steps[] | has("command") and has("startedAt") and has("finishedAt") and has("seconds") and has("log") and has("artifacts") and has("summary")] | all'
	t_check full "qc1 summary" '.steps[0].summary == {complete:true, coverage:"full", releaseEvidence:true, budgetFailures:0, invalid:0}'
	t_check full "artifact paths" '.steps[0].artifacts | endswith("/qc1")'
	[[ -e $tmp/full/qc1.log && -e $tmp/full/drill.log ]] || t_fail "full: step logs missing"
	jq -e '.result == "incomplete" and .coverage.kind == "partial"' "$tmp/snap.qc1" >/dev/null 2>&1 || t_fail "full: the record seen by the first step was not incomplete and partial"
	jq -e '.result == "incomplete" and (.steps | length) == 1' "$tmp/snap.drill" >/dev/null 2>&1 || t_fail "full: the record seen by the second step did not hold exactly the first"
	grep -q '^qc1' "$tmp/log" || t_fail "full: the nested stub did not see the qc1 step"
	grep -q '^drill' "$tmp/log" || t_fail "full: the nested stub did not see the drill step"

	# The direct runner calls the tools themselves.
	t_run direct -- --runner direct --drill-reason x
	t_rc direct 0
	t_check direct "direct commands" '(.steps[0].command | test("qc1-measure.sh")) and (.steps[1].command | test("restore-drill.sh")) and .requested.runner == "direct"'

	# Partial runs are never full: a crossings-only drill, one step, a pilot, pass-through.
	t_run crossings -- --crossings-only
	t_rc crossings 10
	t_check crossings "crossings-only is partial" '.result == "partial" and .coverage.kind == "partial" and .steps[1].verdict == "partial" and .steps[1].exit == 10 and (.coverage.reasons | length > 0) and .requested.coverage == "partial"'
	t_run onestep -- --only qc1
	t_rc onestep 10
	t_check onestep "one step is partial" '.result == "partial" and .coverage.kind == "partial" and (.steps | length) == 1 and ([.coverage.reasons[] | test("drill")] | any)'
	t_run pilot STUB_QC1=pilot -- --drill-reason x
	t_rc pilot 10
	t_check pilot "a pilot is partial" '.result == "partial" and .coverage.kind == "partial" and .steps[0].verdict == "partial" and .steps[0].summary.budgetFailures == 2'
	t_run passthrough -- --drill-reason x --qc1-arg --sizes --qc1-arg 10000
	t_rc passthrough 10
	t_check passthrough "pass-through is partial" '.result == "partial" and .coverage.kind == "partial" and .requested.qc1Args == ["--sizes","10000"] and .requested.coverage == "partial"'
	grep -q '^qc1 --sizes 10000$' "$tmp/log" || t_fail "passthrough: the arguments did not reach the tool: $(cat "$tmp/log")"

	# Failures: nothing reads as a pass, and the other step still runs.
	t_run qc1fail STUB_QC1_RC=3 -- --drill-reason x
	t_rc qc1fail 1
	t_check qc1fail "a failed step" '.result == "fail" and .coverage.kind == "partial" and .steps[0].verdict == "fail" and .steps[0].exit == 3 and .steps[1].verdict == "pass"'
	for c in incomplete badjson none; do
		t_run "qc1-$c" STUB_QC1="$c" -- --drill-reason x
		t_rc "qc1-$c" 1
		t_check "qc1-$c" "an unusable QC-1 record fails the step" '.result == "fail" and .steps[0].verdict == "fail" and .coverage.kind == "partial"'
	done
	t_run qc1rev STUB_QC1_REVISION=0000000000000000000000000000000000000000 -- --drill-reason x
	t_rc qc1rev 1
	t_check qc1rev "another revision fails the step" '.result == "fail" and .steps[0].verdict == "fail"'
	t_run drillfail STUB_DRILL_RC=1 -- --drill-reason x
	t_rc drillfail 1
	t_check drillfail "a failed drill" '.result == "fail" and .steps[1].verdict == "fail" and .steps[1].exit == 1'
	t_run drill10 STUB_DRILL_RC=10 -- --drill-reason x
	t_rc drill10 1
	t_check drill10 "a full drill may not exit 10" '.result == "fail" and .steps[1].verdict == "fail"'
	t_run drillrev STUB_DRILL_REVISION=0000000000000000000000000000000000000000 -- --drill-reason x
	t_rc drillrev 1
	t_check drillrev "a drill for another revision fails" '.result == "fail" and .steps[1].verdict == "fail"'
	t_run crossings0 STUB_DRILL_RC=0 -- --crossings-only
	t_rc crossings0 1
	t_check crossings0 "crossings-only reporting 0 fails" '.result == "fail"'

	# Refusals happen before anything is created.
	t_run noreason --
	t_rc noreason 2
	[[ ! -e $tmp/noreason ]] || t_fail "noreason: a refusal created the evidence directory"
	[[ ! -e $tmp/log ]] || t_fail "noreason: a refusal started a tool"
	t_run full -- --drill-reason x
	t_rc full 2
	[[ ! -e $tmp/log ]] || t_fail "full: a second run into a used directory started a tool"
	mkdir -p "$tmp/occupied" && : >"$tmp/occupied/old"
	t_run occupied -- --drill-reason x
	t_rc occupied 2
	[[ ! -e $tmp/log ]] || t_fail "occupied: a non-empty evidence directory was not refused"
	t_run inside -- --drill-reason x --evidence-dir "$repo/inside"
	t_rc inside 2
	[[ ! -e $repo/inside ]] || t_fail "inside: an evidence directory inside the checkout was made"
	t_run badrunner -- --runner both --drill-reason x
	t_rc badrunner 2
	t_run bothflags -- --only qc1 --crossings-only
	t_rc bothflags 2
	echo dirty >"$repo/dirty.txt"
	t_run dirty -- --drill-reason x
	t_rc dirty 2
	[[ ! -e $tmp/dirty && ! -e $tmp/log ]] || t_fail "dirty: a dirty tree was not refused before anything ran"
	t_run dirtydry -- --dry-run
	t_rc dirtydry 0
	rm -f "$repo/dirty.txt"

	# A dry run prints the matrix and the record path and starts nothing.
	t_run dry -- --dry-run
	t_rc dry 0
	[[ ! -e $tmp/dry && ! -e $tmp/log ]] || t_fail "dry: a dry run wrote or started something"
	grep -q "$tmp/dry/evidence.json" "$tmp/dry.out" || t_fail "dry: the record path is not printed"
	for word in qc1 drill coverage; do
		grep -q "$word" "$tmp/dry.out" || t_fail "dry: $word is not in the printed matrix"
	done
	grep -q 'Full restore drill frequency' "$tmp/dry.out" || t_fail "dry: the drill frequency pointer is missing"

	# A signal ends the step and leaves an incomplete record.
	local sig
	for sig in TERM HUP; do
		out=$tmp/sig-$sig
		rm -f "$tmp/ready" "$tmp/pid" "$tmp/log"
		(cd "$repo" && exec env STUB_LOG="$tmp/log" STUB_RECORD="$out/evidence.json" STUB_SLEEP=120 STUB_READY="$tmp/ready" STUB_PID="$tmp/pid" \
			bash scripts/reason-review-scale.sh --evidence-dir "$out" --drill-reason x) >"$out.out" 2>&1 &
		pid=$!
		for ((i = 0; i < 300; i++)); do
			[[ -e $tmp/ready ]] && break
			sleep 0.1
		done
		if [[ -e $tmp/ready ]]; then
			# the QC-1 stub finishes at once; the drill stub is the one that sleeps
			kill -s "$sig" "$pid"
			rc=0
			wait "$pid" || rc=$?
			want=143
			[[ $sig == HUP ]] && want=129
			[[ $rc == "$want" ]] || t_fail "signal $sig: exit $rc, want $want"
			jq -e '.result == "incomplete" and .coverage.kind == "partial"' "$out/evidence.json" >/dev/null 2>&1 || t_fail "signal $sig: the record is not incomplete"
			if kill -0 "$(cat "$tmp/pid")" 2>/dev/null; then
				t_fail "signal $sig: the step's process survived"
				kill -KILL "$(cat "$tmp/pid")" 2>/dev/null || true
			fi
		else
			t_fail "signal $sig: the stub never became ready"
			kill -KILL "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done

	if ((failures)); then
		echo "reason-review-scale self-test: $failures failure(s)" >&2
		return 1
	fi
	echo "reason-review-scale self-test: ok"
}

if ((self_test)); then
	for tool in git jq sha256sum mktemp; do
		command -v "$tool" >/dev/null 2>&1 || die "$tool is required and was not found"
	done
	self_test_main
	exit $?
fi

# --- arguments ---------------------------------------------------------------
case $runner in nested | direct) ;; *) die "--runner is nested or direct" ;; esac
case $only in '' | qc1 | drill) ;; *) die "--only is qc1 or drill" ;; esac
[[ -z $only || $only == drill || $crossings == 0 ]] || die "--crossings-only is a drill option and cannot go with --only qc1"
run_qc1=1
run_drill=1
[[ $only != drill ]] || run_qc1=0
[[ $only != qc1 ]] || run_drill=0
((run_drill)) || {
	((${#drill_args[@]} == 0)) || die "--drill-arg needs the drill step"
	((crossings == 0)) || die "--crossings-only needs the drill step"
}
((run_qc1)) || ((${#qc1_args[@]} == 0)) || die "--qc1-arg needs the QC-1 step"
drill_mode=full
if ((crossings)); then drill_mode='crossings-only'; fi
if ((run_drill && !crossings && !dry_run)); then
	[[ -n $drill_reason ]] || die "a full drill needs --drill-reason: say why it is being run now (docs/agents/deploy.md, \"Full restore drill frequency\")"
fi
if [[ -n $drill_reason ]]; then
	[[ $drill_reason != *$'\n'* && ${#drill_reason} -le 500 ]] || die "--drill-reason is one line of at most 500 characters"
fi
for a in "${qc1_args[@]+"${qc1_args[@]}"}" "${drill_args[@]+"${drill_args[@]}"}"; do
	[[ -n $a && $a != *$'\n'* ]] || die "a pass-through argument is one non-empty line"
done

for tool in git jq sha256sum date uname mktemp; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required and was not found"
done
for f in "$nested_tool" "$qc1_tool" "$drill_tool"; do
	[[ -f $f ]] || die "$f is missing from this checkout"
done

revision=$(git rev-parse HEAD)
tree=$(git rev-parse 'HEAD^{tree}')
clean=true
[[ -z $(git status --porcelain --untracked-files=normal) ]] || clean=false

# The coverage this invocation asks for, decided from the arguments alone.
req_reasons=()
((run_qc1)) || req_reasons+=("the QC-1 measurement was not requested")
((run_drill)) || req_reasons+=("the restore drill was not requested")
((!crossings)) || req_reasons+=("the drill was requested as the --crossings-only PARTIAL diagnostic, which is never acceptance")
((${#qc1_args[@]} == 0)) || req_reasons+=("arguments were passed through to qc1-measure.sh; only a run with none is the standard matrix")
((${#drill_args[@]} == 0)) || req_reasons+=("arguments were passed through to restore-drill.sh; only a run with none is the full drill")
requested=full
((${#req_reasons[@]} == 0)) || requested=partial

stamp=$(date -u +%Y%m%dT%H%M%SZ)
[[ -n $evidence ]] || evidence="$evidence_root/${revision:0:12}-$stamp"
case $evidence in /*) ;; *) evidence="$PWD/$evidence" ;; esac
evidence=$(realpath -m "$evidence")
case $evidence in
"$root" | "$root"/*) die "the evidence directory $evidence is inside the checkout; it must be outside" ;;
esac

# The commands, as arrays, so the dry run prints exactly what a run executes.
qc1_cmd=()
drill_cmd=()
if [[ $runner == nested ]]; then
	qc1_cmd=(bash "$root/$nested_tool" --out "$evidence/qc1" --qc1)
	((${#qc1_args[@]} == 0)) || qc1_cmd+=(-- "${qc1_args[@]}")
	drill_cmd=(bash "$root/$nested_tool" --out "$evidence/drill")
	((!crossings)) || drill_cmd+=(--crossings-only)
	((${#drill_args[@]} == 0)) || drill_cmd+=(-- "${drill_args[@]}")
else
	qc1_cmd=(bash "$root/$qc1_tool" --dir "$evidence/qc1")
	((${#qc1_args[@]} == 0)) || qc1_cmd+=("${qc1_args[@]}")
	drill_cmd=(bash "$root/$drill_tool" --dir "$evidence/drill")
	((!crossings)) || drill_cmd+=(--crossings-only)
	((${#drill_args[@]} == 0)) || drill_cmd+=("${drill_args[@]}")
fi

# qc1_matrix prints the default QC-1 matrix from the harness's own constants, in a
# subshell so nothing of qc1/lib.sh stays. It reads app-facts.json, never source.
qc1_matrix() (
	set +eu
	# shellcheck source=deploy/digitalocean/qc1/lib.sh
	source "$root/deploy/digitalocean/qc1/lib.sh" 2>/dev/null || return 1
	[[ -n ${QC1_SIZES_DEFAULT:-} ]] || return 1
	printf 'table sizes %s; discovery counts %s plus dense %s; deletion counts %s; browser counts %s plus dense' \
		"$QC1_SIZES_DEFAULT" "$QC1_COUNTS_DEFAULT" "$QC1_DENSE_COUNTS_DEFAULT" "$QC1_DELETE_COUNTS" "$QC1_BROWSER_COUNTS"
)

if ((dry_run)); then
	echo "reason-review scale run: DRY RUN. Nothing is started and nothing is written."
	echo "revision:  $revision ($([[ $clean == true ]] && echo 'tree clean' || echo 'tree NOT clean: a real run refuses this'))"
	echo "runner:    $runner"
	echo "evidence:  $evidence"
	echo "record:    $evidence/evidence.json (starts incomplete; written at the end as pass, partial or fail)"
	echo
	step=0
	if ((run_qc1)); then
		step=$((step + 1))
		echo "step $step  qc1    $(printf '%q ' "${qc1_cmd[@]}")"
		matrix=$(qc1_matrix) || matrix="the matrix qc1-measure.sh names as its default (see its header)"
		echo "                 covers: $matrix"
		echo "                 full only when its evidence-status.json says complete, coverage full, releaseEvidence true"
	fi
	if ((run_drill)); then
		step=$((step + 1))
		echo "step $step  drill  $(printf '%q ' "${drill_cmd[@]}")"
		if ((crossings)); then
			echo "                 covers: the PARTIAL crossings diagnostic only (bootstrap, fixtures, the real level-2 crossing cases); a pass exits 10"
		else
			echo "                 covers: the full restore drill (restore-drill.sh drill_progress_init lists the cases); a pass exits 0"
		fi
		echo "                 when to run the full drill: docs/agents/deploy.md, \"Full restore drill frequency\""
	fi
	echo
	echo "coverage this run asks for: $requested"
	for r in "${req_reasons[@]}"; do echo "  - $r"; done
	if [[ $requested == full ]]; then
		echo "  (reached only if every step reaches its own full coverage)"
	fi
	exit 0
fi

[[ $clean == true ]] || die "the working tree is not clean; the tools measure and drill exactly HEAD, so commit first"

if [[ -e $evidence ]]; then
	[[ -d $evidence ]] || die "the evidence destination $evidence exists and is not a directory"
	[[ -z $(ls -A "$evidence") ]] || die "the evidence directory $evidence is not empty; each run writes a fresh directory, so an interrupted rerun cannot keep a previous pass"
else
	mkdir -p "$evidence"
fi
evidence=$(cd "$evidence" && pwd -P)

host=$(uname -sm)
started=$(now)
steps_json='[]'
overall_bad=0     # a step failed
overall_partial=0 # a step ran but did not reach full coverage
recording=1
finalized=0
cov_reasons=()

tool_hash() { sha256sum "$1" | cut -d' ' -f1; }
tools_json=$(jq -n \
	--arg q "$qc1_tool" --arg qh "$(tool_hash "$qc1_tool")" \
	--arg d "$drill_tool" --arg dh "$(tool_hash "$drill_tool")" \
	--arg n "$nested_tool" --arg nh "$(tool_hash "$nested_tool")" \
	'{qc1:{path:$q,sha256:$qh}, drill:{path:$d,sha256:$dh}, nested:{path:$n,sha256:$nh}}')

# json_array STRING...: the strings as a JSON array (none gives []).
json_array() {
	if (($# == 0)); then
		echo '[]'
	else
		printf '%s\n' "$@" | jq -R . | jq -sc .
	fi
}

# write_record RESULT KIND writes evidence.json with the steps recorded so far.
write_record() {
	local result=$1 kind=$2 step_names='[]'
	((!run_qc1)) || step_names='["qc1"]'
	((!run_drill)) || step_names=$(jq -c '. + ["drill"]' <<<"$step_names")
	jq -n \
		--arg revision "$revision" --arg tree "$tree" --argjson clean "$clean" \
		--arg started "$started" --arg finished "$(now)" --arg host "$host" \
		--arg runner "$runner" --arg drillMode "$drill_mode" --arg drillReason "$drill_reason" \
		--argjson names "$step_names" --arg requested "$requested" \
		--argjson reqReasons "$(json_array "${req_reasons[@]+"${req_reasons[@]}"}")" \
		--argjson qc1Args "$(json_array "${qc1_args[@]+"${qc1_args[@]}"}")" \
		--argjson drillArgs "$(json_array "${drill_args[@]+"${drill_args[@]}"}")" \
		--arg kind "$kind" --argjson covReasons "$(json_array "${cov_reasons[@]+"${cov_reasons[@]}"}")" \
		--argjson tools "$tools_json" --argjson steps "$steps_json" --arg result "$result" '
		{schema:"fold/reason-review-scale/v1", issue:297,
		 revision:$revision, tree:$tree, workingTreeClean:$clean,
		 startedAt:$started, finishedAt:$finished, host:$host,
		 requested:({steps:$names, runner:$runner, drillMode:$drillMode, qc1Args:$qc1Args, drillArgs:$drillArgs,
		             coverage:$requested, reasons:$reqReasons}
		            + (if $drillReason == "" then {} else {drillReason:$drillReason} end)),
		 coverage:{kind:$kind, reasons:$covReasons},
		 tools:$tools, steps:$steps, result:$result}' >"$evidence/evidence.json.tmp"
	mv "$evidence/evidence.json.tmp" "$evidence/evidence.json"
}

# --- cancellation ------------------------------------------------------------
# Each step is the only background job, in a process group of its own (set -m),
# so a signal to this script reaches the step and everything it started through
# bash's job table (kill %%), and a terminal's ^C reaches only this script, which
# then forwards it. No process list or remembered PID is consulted.
set -m
[[ $- == *m* ]] || die "bash job control (set -m) is not available here; the run cannot own its steps' processes"

# shellcheck disable=SC2329 # reached through cancel_run and the EXIT trap
stop_step() {
	local i
	[[ -n $(jobs -rp) ]] || return 0
	{
		kill -s TERM %% || true
		for ((i = 0; i < 1200; i++)); do
			[[ -z $(jobs -rp) ]] && break
			sleep 0.1
		done
		if [[ -n $(jobs -rp) ]]; then
			kill -s KILL %% || true
			for ((i = 0; i < 50; i++)); do
				[[ -z $(jobs -rp) ]] && break
				sleep 0.1
			done
		fi
		wait || true
	} 2>/dev/null
}

cancelling=0
# shellcheck disable=SC2329 # reached only through the traps below
cancel_run() { # SIGNAL EXIT_CODE
	((cancelling == 0)) || return 0
	cancelling=1
	stop_step
	write_record incomplete partial
	finalized=1
	echo "reason-review-scale: interrupted by $1; record: $evidence/evidence.json (incomplete)" >&2
	exit "$2"
}
# shellcheck disable=SC2329 # reached only through the trap below
on_int() { cancel_run INT 130; }
# shellcheck disable=SC2329 # reached only through the trap below
on_term() { cancel_run TERM 143; }
# shellcheck disable=SC2329 # reached only through the trap below
on_hup() { cancel_run HUP 129; }
# shellcheck disable=SC2329 # reached only through the trap below
on_exit() {
	local rc=$?
	trap - EXIT
	stop_step
	if ((recording && !finalized)); then
		write_record incomplete partial || true
	fi
	exit "$rc"
}
trap on_int INT
trap on_term TERM
trap on_hup HUP
trap on_exit EXIT
[[ $(trap -p INT) == *on_int* ]] ||
	echo "reason-review-scale: warning: SIGINT was ignored when this script started, so INT cannot stop it; send TERM, or start it with bash -m" >&2

write_record incomplete partial

# run_step NAME LOG COMMAND...: runs the command with its output in the log and
# leaves the outcome in the step_* variables.
run_step() {
	local name=$1 log=$2 s0
	shift 2
	step_name=$name step_log=$log
	step_cmd=$(printf '%q ' "$@")
	step_cmd=${step_cmd% }
	step_begin=$(now)
	s0=$SECONDS
	echo "== $name: $step_cmd"
	"$@" </dev/null >"$evidence/$log" 2>&1 &
	step_status=0
	wait $! || step_status=$?
	step_secs=$((SECONDS - s0))
	step_end=$(now)
}

# record_step VERDICT ARTIFACTS SUMMARY_JSON
record_step() {
	local verdict=$1 artifacts=$2 summary=$3
	case $verdict in
	fail)
		overall_bad=1
		echo "   FAILED (exit $step_status), log: $evidence/$step_log"
		tail -n 20 "$evidence/$step_log" | sed 's/^/   | /'
		;;
	partial)
		overall_partial=1
		echo "   ran, but not to full coverage (exit $step_status) in ${step_secs}s"
		;;
	*) echo "   ok in ${step_secs}s" ;;
	esac
	steps_json=$(jq -c \
		--arg name "$step_name" --arg command "$step_cmd" --arg begin "$step_begin" --arg end "$step_end" \
		--argjson secs "$step_secs" --argjson exit "$step_status" --arg verdict "$verdict" \
		--arg log "$step_log" --arg artifacts "$artifacts" --argjson summary "$summary" \
		'. + [{name:$name, command:$command, startedAt:$begin, finishedAt:$end, seconds:$secs, exit:$exit, verdict:$verdict, log:$log, artifacts:$artifacts, summary:$summary}]' <<<"$steps_json")
	write_record incomplete partial
}

why() { # a reason a step is not full or not good
	cov_reasons+=("$1")
	echo "   $1" >>"$evidence/$step_log"
}

# --- qc1 ---------------------------------------------------------------------
if ((run_qc1)); then
	dir=$evidence/qc1
	run_step qc1 qc1.log "${qc1_cmd[@]}"
	verdict=pass
	summary='{}'
	if ((step_status != 0)); then
		verdict=fail
		why "QC-1 exited $step_status"
	fi
	if [[ -s $dir/evidence-status.json ]] && jq -e . "$dir/evidence-status.json" >/dev/null 2>&1; then
		summary=$(jq -c '{complete:(.complete // false), coverage:(.coverage.kind // null), releaseEvidence:(.releaseEvidence // false), budgetFailures:((.budgetFailures // []) | length), invalid:((.invalid // []) | length)}' "$dir/evidence-status.json")
		if [[ $verdict == pass ]]; then
			if ! jq -e '.complete == true' <<<"$summary" >/dev/null; then
				verdict=fail
				why "QC-1 evidence-status.json is not complete"
			elif ! jq -e '.coverage == "full" and .releaseEvidence == true' <<<"$summary" >/dev/null; then
				verdict=partial
				why "QC-1 reached coverage $(jq -r '.coverage // "unknown"' <<<"$summary") (releaseEvidence $(jq -r .releaseEvidence <<<"$summary")), not full release evidence"
			fi
		fi
	elif [[ $verdict == pass ]]; then
		verdict=fail
		why "QC-1 left no readable evidence-status.json in $dir"
	fi
	if [[ $verdict != fail ]]; then
		if [[ $(jq -r '.revision // ""' "$dir/environment.json" 2>/dev/null || true) != "$revision" ]]; then
			verdict=fail
			why "QC-1 environment.json does not name revision $revision"
		fi
	fi
	record_step "$verdict" "$dir" "$summary"
fi

# --- drill -------------------------------------------------------------------
if ((run_drill)); then
	dir=$evidence/drill
	run_step drill drill.log "${drill_cmd[@]}"
	verdict=pass
	summary=$(jq -nc --arg mode "$drill_mode" '{mode:$mode}')
	if ((crossings)); then
		# restore-drill-nested.sh turns a crossings-only 0 into a failure; 10 is the only pass.
		if ((step_status == 10)); then
			verdict=partial
			why "the drill ran as the --crossings-only PARTIAL diagnostic, not the full drill"
		else
			verdict=fail
			why "the crossings-only drill exited $step_status (a pass exits 10)"
		fi
	elif ((step_status != 0)); then
		verdict=fail
		why "the drill exited $step_status (a full pass exits 0)"
	fi
	if [[ $verdict != fail ]]; then
		if [[ $(jq -r '.revision // ""' "$dir/restore-drill.json" 2>/dev/null || true) != "$revision" ]]; then
			verdict=fail
			why "the drill left no restore-drill.json naming revision $revision"
		fi
	fi
	record_step "$verdict" "$dir" "$summary"
fi

# --- the record --------------------------------------------------------------
result=pass
kind=full
if ((overall_bad)); then
	result=fail
	kind=partial
elif ((overall_partial)) || [[ $requested != full ]]; then
	result=partial
	kind=partial
fi
if [[ $kind == partial ]]; then
	for r in "${req_reasons[@]}"; do cov_reasons+=("$r"); done
fi
trap '' INT TERM HUP # a signal now must not turn the finished record back into an incomplete one
write_record "$result" "$kind"
finalized=1
jq -e . "$evidence/evidence.json" >/dev/null || die "the completed evidence record is not valid JSON: $evidence/evidence.json"
echo "record: $evidence/evidence.json ($result, coverage $kind, at ${revision:0:12})"
case $result in
pass) exit 0 ;;
partial) exit 10 ;;
*) exit 1 ;;
esac
