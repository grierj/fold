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
#           disposition and do not fail the step. The step FAILS when the tool exits
#           non-zero, its evidence-status.json is missing or unreadable, the run
#           aborted, a finding is invalid, an expected measurement is missing, the
#           record is incomplete with no stated reason, or its environment.json names
#           another revision. A run that finished normally and is incomplete ONLY
#           because cases are explicitly pending (the legacy discovery and legacy PUT
#           cases, at this revision) is PARTIAL: the record carries those cases, their
#           reasons and the tool's coverage reasons.
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
# reports it. A step that a signal cut short is kept in the record with verdict
# "interrupted", its log and whatever artifacts it left.
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
# any step ran; 129, 130 or 143 after HUP, INT or TERM.
#
# Process ownership. Each step runs as one background pipeline in a process group of
# its own (set -m): a holder that only keeps the group alive and notices a lost
# supervisor, piped to the supervisor that runs the command and reports its status
# (the way scripts/reason-review-hardening.sh does it). The job stays in bash's table
# until the cleanup ends it, so a tool that exits while a descendant that ignores TERM
# lives on is still ours to end: the group is signalled only through that job
# (kill %%), never by a pid read from a process list, so a bystander can never receive
# it. On a signal the group gets TERM and the command up to FOLD_SCALE_STOP_GRACE
# seconds (default 180) to finish its own cleanup, which for the nested runner is
# stopping the run inside, copying the artifacts out and removing its container; then
# the whole group is KILLed, and the step's record says which happened. A step that
# ends by itself has its group KILLed as well, so nothing it leaves behind survives
# it. A process that leaves the group on purpose (setsid, setpgid) is outside what
# this can own.
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
# hold KIND: leave a descendant that ignores TERM (STUB_ORPHAN_PID) and/or stay running (STUB_SLEEP), for the kind named by STUB_HOLD_KIND (default drill).
hold() {
	[[ $1 == "${STUB_HOLD_KIND:-drill}" ]] || return 0
	if [[ -n ${STUB_ORPHAN_PID:-} ]]; then
		( trap '' TERM HUP INT; exec sleep 300 ) </dev/null >/dev/null 2>&1 &
		echo $! >"$STUB_ORPHAN_PID"
	fi
	if [[ -n ${STUB_SLEEP:-} ]]; then
		echo $$ >"$STUB_PID"
		[[ -z ${STUB_IGNORE_TERM:-} ]] || trap '' TERM
		: >"$STUB_READY"
		exec sleep "$STUB_SLEEP"
	fi
}
if [[ $kind == qc1 ]]; then
	jq -n --arg rev "${STUB_QC1_REVISION:-$rev}" '{revision:$rev}' >"$out/environment.json"
	# The shapes are the ones qc1/lib.sh writes (complete is false whenever a case is pending, missing or invalid).
	pend='[{"item":4,"size":null,"case":"legacy-discovery","reason":"T07B-3a"},{"item":5,"size":null,"case":"legacy-put","reason":"T07B-3a"}]'
	case ${STUB_QC1:-full} in
	full) echo '{"missing":[],"pending":[],"invalid":[],"budgetFailures":[],"coverage":{"kind":"full","reasons":[]},"complete":true,"releaseEvidence":true}' >"$out/evidence-status.json" ;;
	pilot) echo '{"missing":[],"pending":[],"invalid":[],"budgetFailures":[{"x":1},{"x":2}],"coverage":{"kind":"pilot","reasons":["not the full matrix"]},"complete":true,"releaseEvidence":false}' >"$out/evidence-status.json" ;;
	pending) echo '{"missing":[],"pending":'"$pend"',"invalid":[],"budgetFailures":[{"x":1}],"coverage":{"kind":"full","reasons":[]},"complete":false,"releaseEvidence":false}' >"$out/evidence-status.json" ;;
	pendingpilot) echo '{"missing":[],"pending":'"$pend"',"invalid":[],"budgetFailures":[],"coverage":{"kind":"pilot","reasons":["rows of the full matrix are not measured and clean: 40","no browser panel measurement"]},"complete":false,"releaseEvidence":false}' >"$out/evidence-status.json" ;;
	missing) echo '{"missing":[{"item":3,"size":100000,"case":"discovery-requester-10"}],"pending":'"$pend"',"invalid":[],"budgetFailures":[],"coverage":{"kind":"full","reasons":[]},"complete":false,"releaseEvidence":false}' >"$out/evidence-status.json" ;;
	invalid) echo '{"missing":[],"pending":'"$pend"',"invalid":[{"class":"invalid","item":6,"size":10000,"case":"delete","failure":"duplicate-row"}],"budgetFailures":[],"coverage":{"kind":"full","reasons":[]},"complete":false,"releaseEvidence":false}' >"$out/evidence-status.json" ;;
	aborted) echo '{"missing":[{"item":3,"size":10000,"case":"x"}],"pending":'"$pend"',"invalid":[],"budgetFailures":[],"coverage":{"kind":"pilot","reasons":[]},"complete":false,"releaseEvidence":false,"aborted":{"signal":"TERM","at":"item 3"}}' >"$out/evidence-status.json" ;;
	incomplete) echo '{"missing":[],"pending":[],"invalid":[],"budgetFailures":[],"coverage":{"kind":"full","reasons":[]},"complete":false,"releaseEvidence":false}' >"$out/evidence-status.json" ;;
	badjson) echo 'not json' >"$out/evidence-status.json" ;;
	none) ;;
	esac
	hold qc1
	exit "${STUB_QC1_RC:-0}"
fi
jq -n --arg rev "${STUB_DRILL_REVISION:-$rev}" '{revision:$rev}' >"$out/restore-drill.json"
hold drill
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
	t_check full "qc1 summary" '.steps[0].summary | (.complete == true and .coverage == "full" and .releaseEvidence == true and .budgetFailures == 0 and .invalid == 0 and .missing == 0 and .pending == 0 and .aborted == false and .pendingCases == [] and .missingCases == [])'
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
	t_check qc1-incomplete "the unexplained gap is named" '[.coverage.reasons[] | test("names no missing, pending or invalid")] | any'

	# The real QC-1 shapes (qc1/lib.sh): an ordinary finished run is incomplete because the legacy
	# cases are pending, which is partial, with the cases and their reasons in the record. A missing
	# measurement, an invalid finding or an aborted run is a failure, with the first of them named.
	t_run qc1pending STUB_QC1=pending -- --drill-reason x
	t_rc qc1pending 10
	t_check qc1pending "pending coverage is partial, not failed" '.result == "partial" and .coverage.kind == "partial" and .steps[0].verdict == "partial" and .steps[0].exit == 0 and .steps[1].verdict == "pass"'
	t_check qc1pending "the pending cases and their reasons are kept" '.steps[0].summary | (.complete == false and .pending == 2 and .missing == 0 and .invalid == 0 and .aborted == false and .budgetFailures == 1 and ([.pendingCases[] | .case] == ["legacy-discovery","legacy-put"]) and ([.pendingCases[] | .reason] | all(. == "T07B-3a")))'
	t_check qc1pending "the reasons say what is pending" '[.coverage.reasons[] | test("2 case\\(s\\) explicitly pending.*legacy-discovery.*legacy-put.*T07B-3a")] | any'
	grep -q 'explicitly pending' "$tmp/qc1pending/qc1.log" || t_fail "qc1pending: the step log does not say why"
	t_run qc1pendingpilot STUB_QC1=pendingpilot -- --drill-reason x
	t_rc qc1pendingpilot 10
	t_check qc1pendingpilot "a pilot with pending cases is partial and keeps the tool's coverage reasons" '.result == "partial" and .steps[0].verdict == "partial" and (.steps[0].summary.coverageReasons | length) == 2 and ([.coverage.reasons[] | test("coverage is pilot, not full: rows of the full matrix")] | any)'
	t_run qc1missing STUB_QC1=missing -- --drill-reason x
	t_rc qc1missing 1
	t_check qc1missing "a missing measurement fails, naming it" '.result == "fail" and .steps[0].verdict == "fail" and .steps[0].exit == 0 and .steps[0].summary.missing == 1 and ([.coverage.reasons[] | test("missing 1 expected measurement.*item 3, size 100000, case discovery-requester-10")] | any) and .steps[1].verdict == "pass"'
	t_run qc1invalid STUB_QC1=invalid -- --drill-reason x
	t_rc qc1invalid 1
	t_check qc1invalid "an invalid finding fails, naming it" '.result == "fail" and .steps[0].verdict == "fail" and .steps[0].summary.invalid == 1 and ([.coverage.reasons[] | test("1 invalid finding.*duplicate-row")] | any)'
	t_run qc1aborted STUB_QC1=aborted -- --drill-reason x
	t_rc qc1aborted 1
	t_check qc1aborted "an aborted run fails and keeps why" '.result == "fail" and .steps[0].verdict == "fail" and .steps[0].summary.aborted == true and ([.coverage.reasons[] | test("QC-1 aborted: .*TERM")] | any) and .steps[0].summary.missing == 1'
	t_run qc1failpending STUB_QC1=pending STUB_QC1_RC=3 -- --drill-reason x
	t_rc qc1failpending 1
	t_check qc1failpending "a non-zero exit fails but still keeps the pending cases" '.steps[0].verdict == "fail" and .steps[0].exit == 3 and .steps[0].summary.pending == 2'
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

	# --- cancellation: the step's whole process group, and nothing else ---------------------
	# t_alive PID: the process exists and is not a zombie.
	t_alive() {
		local st
		if [[ -r /proc/$1/stat ]]; then
			st=$(sed -E 's/^[0-9]+ \(.*\) (.) .*/\1/' "/proc/$1/stat" 2>/dev/null || true)
			[[ -n $st && $st != Z ]]
			return
		fi
		kill -0 "$1" 2>/dev/null
	}
	local leftovers=()
	# t_gone WHAT PIDFILE: the process named in PIDFILE has ended (and is ended for good if a check failed).
	t_gone() {
		local p
		p=$(cat "$2" 2>/dev/null || true)
		if [[ -z $p ]]; then
			t_fail "$1: no pid was recorded"
		elif t_alive "$p"; then
			t_fail "$1: the process $p survived"
			kill -KILL "$p" 2>/dev/null || true
		fi
	}
	# t_signal NAME SIGNAL ENV... -- ARGS...: starts the copy with the stub holding its step (STUB_SLEEP; ENV adds
	# to that), sends SIGNAL once the stub is ready, and leaves rc and the evidence directory tmp/NAME.
	t_signal() {
		local name=$1 sig=$2 p i
		shift 2
		local envs=()
		while (($#)) && [[ $1 != -- ]]; do
			envs+=("$1")
			shift
		done
		shift
		rm -f "$tmp/ready" "$tmp/pid" "$tmp/log"
		(cd "$repo" && exec env STUB_LOG="$tmp/log" STUB_RECORD="$tmp/$name/evidence.json" STUB_SLEEP=120 STUB_READY="$tmp/ready" STUB_PID="$tmp/pid" "${envs[@]}" \
			bash scripts/reason-review-scale.sh --evidence-dir "$tmp/$name" "$@") >"$tmp/$name.out" 2>&1 &
		p=$!
		for ((i = 0; i < 300; i++)); do
			[[ -e $tmp/ready ]] && break
			sleep 0.1
		done
		rc=0
		if [[ -e $tmp/ready ]]; then
			kill -s "$sig" "$p"
			wait "$p" || rc=$?
		else
			t_fail "$name: the stub never became ready"
			kill -KILL "$p" 2>/dev/null || true
			wait "$p" 2>/dev/null || true
			rc=-1
		fi
	}

	# A signal ends the step, keeps the step in the record as interrupted, and leaves nothing running.
	local sig want bystander
	sleep 300 &
	bystander=$!
	leftovers+=("$bystander")
	for sig in TERM HUP; do
		t_signal "sig-$sig" "$sig" -- --drill-reason x
		want=143
		[[ $sig == HUP ]] && want=129
		t_rc "sig-$sig" "$want"
		t_check "sig-$sig" "the record is incomplete" '.result == "incomplete" and .coverage.kind == "partial"'
		t_check "sig-$sig" "the interrupted step is kept, the finished one too" '[.steps[] | .verdict] == ["pass","interrupted"] and .steps[1].name == "drill" and .steps[1].exit > 128 and .steps[1].summary.interruptedBy == "'"$sig"'" and .steps[1].summary.commandStopped == "on TERM, within the grace"'
		t_gone "signal $sig: the step's process" "$tmp/pid"
	done
	t_alive "$bystander" || t_fail "signals: a process that is not the step's was ended"

	# A signal during the first step keeps that step and starts no later one.
	t_signal sig-qc1 TERM STUB_HOLD_KIND=qc1 -- --drill-reason x
	t_rc sig-qc1 143
	t_check sig-qc1 "the first step is the interrupted one" '[.steps[] | .name] == ["qc1"] and .steps[0].verdict == "interrupted" and .steps[0].summary.artifactsDirectory == true'
	grep -q '^drill' "$tmp/log" && t_fail "sig-qc1: the drill started after an interrupted QC-1 step"

	# The tool exits on TERM while a descendant that ignores TERM lives on: the group is still ended, and a
	# process outside it is not touched.
	t_signal orphan-term TERM STUB_ORPHAN_PID="$tmp/orphan" -- --drill-reason x
	t_rc orphan-term 143
	[[ -s $tmp/orphan ]] || t_fail "orphan-term: the stub left no descendant"
	t_gone "a TERM-ignoring descendant of a tool that exited on TERM" "$tmp/orphan"
	t_alive "$bystander" || t_fail "orphan-term: a process that is not the step's was ended"
	t_check orphan-term "the tool stopped on TERM" '.steps[1].verdict == "interrupted" and .steps[1].summary.commandStopped == "on TERM, within the grace"'

	# The same descendant after a tool that finishes by itself (a step's leftovers do not outlive it).
	t_run orphan-done STUB_ORPHAN_PID="$tmp/orphan-done.pid" STUB_HOLD_KIND=qc1 -- --only qc1
	t_rc orphan-done 10
	t_gone "a descendant left behind by a tool that exited by itself" "$tmp/orphan-done.pid"
	t_alive "$bystander" || t_fail "orphan-done: a process that is not the step's was ended"

	# A tool that ignores TERM entirely is killed once the grace has run out, and the record says so.
	t_signal stubborn TERM STUB_IGNORE_TERM=1 STUB_ORPHAN_PID="$tmp/orphan-s" FOLD_SCALE_STOP_GRACE=2 -- --drill-reason x
	t_rc stubborn 143
	t_check stubborn "killed past the grace" '.steps[1].verdict == "interrupted" and .steps[1].summary.commandStopped == "only after KILL, past the grace"'
	t_gone "a tool that ignores TERM" "$tmp/pid"
	t_gone "its descendant" "$tmp/orphan-s"
	t_alive "$bystander" || t_fail "stubborn: a process that is not the step's was ended"

	# --- cancelling the default nested run keeps what only the container holds ----------------
	# The real restore-drill-nested.sh, against a docker that models one container: its files are tmp/fake, and the
	# workload runs in a process group of its own, as it would inside the container, so a signal that reaches the
	# docker client on this host does not reach it. Only the runner's `pkill` through the daemon does.
	local nroot=$tmp/nrepo fake=$tmp/fake bin=$tmp/stubbin
	mkdir -p "$nroot/scripts" "$nroot/deploy/digitalocean" "$bin"
	cp "$me" "$nroot/scripts/reason-review-scale.sh"
	cp "$root/$nested_tool" "$nroot/$nested_tool"
	printf '#!/usr/bin/env bash\nexit 0\n' | tee "$nroot/$qc1_tool" >"$nroot/$drill_tool"
	git -C "$nroot" init -q
	git -C "$nroot" add -A
	git -C "$nroot" -c user.name=scale-test -c user.email=scale-test@example.invalid -c commit.gpgsign=false commit -q -m nested
	cat >"$bin/docker" <<'DOCKER'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
case "$1" in
run) printf 'c%.0s' {1..64}; echo ;;
inspect) case "$*" in *NetworkMode*) echo bridge ;; *Mounts*) echo '/var/lib/docker /v' ;; esac ;;
rm) : >"$STUB_FAKE/removed" ;;
cp)
	case $2 in
	c*:*)
		src=${2#*:}
		which=$(basename "$(dirname "$src")")
		[[ ${STUB_CP_FAIL:-} != "$which" ]] || exit 1
		[[ -d $STUB_FAKE${src%/.} ]] || exit 1
		mkdir -p "$3"
		cp -a "$STUB_FAKE${src%/.}/." "$3"
		;;
	esac
	;;
exec)
	case "$*" in
	*" pkill -TERM "*) [[ ! -s $STUB_FAKE/workload.pid ]] || kill -TERM "$(cat "$STUB_FAKE/workload.pid")" 2>/dev/null || true ;;
	*" pkill -KILL "*)
		[[ ! -s $STUB_FAKE/workload.pid ]] || kill -KILL "$(cat "$STUB_FAKE/workload.pid")" 2>/dev/null || true
		rm -f "$STUB_FAKE/running"
		;;
	*" pgrep "*) [[ -e $STUB_FAKE/running ]] || exit 1 ;;
	*browser-prepare.sh*)
		mkdir -p "$STUB_FAKE/work/prep"
		echo '{"chromium":"stub"}' >"$STUB_FAKE/work/prep/browser-runtime.json"
		;;
	*qc1-measure.sh*)
		mkdir -p "$STUB_FAKE/work/out"
		echo '{"item":3,"status":"measured"}' >"$STUB_FAKE/work/out/measurements.jsonl"
		if [[ -n ${STUB_QUICK:-} ]]; then
			echo done >"$STUB_FAKE/work/out/done"
			exit "${STUB_QUICK_RC:-0}"
		fi
		set -m
		(
			if [[ -n ${STUB_INNER_STUBBORN:-} ]]; then
				trap '' TERM
			else
				trap 'echo "{\"aborted\":{\"signal\":\"TERM\"}}" >"$STUB_FAKE/work/out/evidence-status.json"; rm -f "$STUB_FAKE/running"; exit 143' TERM
			fi
			: >"$STUB_FAKE/wl-ready"
			while :; do sleep 0.1; done
		) &
		wl=$!
		echo "$wl" >"$STUB_FAKE/workload.pid"
		until [[ -e $STUB_FAKE/wl-ready ]]; do sleep 0.02; done
		: >"$STUB_FAKE/running"
		wait "$wl"
		exit $?
		;;
	esac
	;;
esac
exit 0
DOCKER
	chmod +x "$bin/docker"
	# n_reset: a fresh container. n_wait: the workload is running in it.
	n_reset() {
		rm -rf "$fake"
		mkdir -p "$fake"
		: >"$tmp/nlog"
	}
	n_wait() {
		local i
		for ((i = 0; i < 300; i++)); do
			[[ -e $fake/running ]] && return 0
			sleep 0.1
		done
		return 1
	}
	# n_before EARLIER LATER: the first line of the docker log matching EARLIER comes before the first matching LATER.
	n_before() {
		local a b
		a=$(grep -n -- "$1" "$tmp/nlog" | head -n 1 | cut -d: -f1)
		b=$(grep -n -- "$2" "$tmp/nlog" | head -n 1 | cut -d: -f1)
		[[ -n $a && -n $b ]] && ((a < b))
	}
	# n_signal NAME SIGNAL ENV... -- COMMAND...: runs COMMAND in the nested repository with the stub docker first on the
	# path, sends SIGNAL to it once the workload is running in the container, and leaves rc.
	n_signal() {
		local name=$1 sig=$2 p
		shift 2
		local envs=()
		while (($#)) && [[ $1 != -- ]]; do
			envs+=("$1")
			shift
		done
		shift
		n_reset
		(cd "$nroot" && exec env "PATH=$bin:$PATH" "STUB_FAKE=$fake" "STUB_LOG=$tmp/nlog" FOLD_SCALE_STOP_GRACE=60 FOLD_NESTED_STOP_GRACE=5 "${envs[@]}" "$@") >"$tmp/$name.out" 2>&1 &
		p=$!
		rc=0
		if n_wait; then
			kill -s "$sig" "$p"
			wait "$p" || rc=$?
		else
			t_fail "$name: the workload never started in the container"
			kill -KILL "$p" 2>/dev/null || true
			wait "$p" 2>/dev/null || true
			rc=-1
		fi
		[[ ! -s $fake/workload.pid ]] || t_gone "$name: the workload in the container" "$fake/workload.pid"
		[[ -e $fake/removed ]] || t_fail "$name: the container was not removed"
	}
	# n_collected NAME DIR: what existed only in the container is in DIR, and it was copied before the container was removed.
	n_collected() {
		[[ -s $2/measurements.jsonl ]] || t_fail "$1: the measurements that existed only in the container were not copied out"
		[[ -s $2/prep/browser-runtime.json ]] || t_fail "$1: the preparation record was not copied out"
		n_before '^cp c\{64\}:/work/out/\. ' '^rm -fv ' || t_fail "$1: the container was removed before the run's artifacts were copied"
		n_before '^cp c\{64\}:/work/prep/\. ' '^rm -fv ' || t_fail "$1: the container was removed before the preparation was copied"
	}

	# Through the wrapper: TERM reaches the whole group, so the docker client dies with it, and the workload inside
	# is stopped by the runner (its own TERM trap writes the aborted status) before anything is copied or removed.
	n_signal nest-term TERM -- bash scripts/reason-review-scale.sh --evidence-dir "$tmp/nest-term" --only qc1
	t_rc nest-term 143
	n_collected nest-term "$tmp/nest-term/qc1"
	n_before ' pkill -TERM ' '^cp c\{64\}:/work/out/\. ' || t_fail "nest-term: the run inside was not asked to stop before its artifacts were copied"
	jq -e '.aborted.signal == "TERM"' "$tmp/nest-term/qc1/evidence-status.json" >/dev/null 2>&1 || t_fail "nest-term: the run inside did not get to finish its own cleanup before the copy"
	grep -q 'finished its own cleanup' "$tmp/nest-term/qc1/INTERRUPTED.txt" 2>/dev/null || t_fail "nest-term: INTERRUPTED.txt is missing or does not say how the run stopped"
	[[ ! -e $tmp/nest-term/qc1/COLLECTION-FAILED.txt ]] || t_fail "nest-term: a collection failure was recorded for a clean copy"
	t_check nest-term "the interrupted nested step is kept with its artifacts" '.result == "incomplete" and .steps[0].verdict == "interrupted" and .steps[0].exit == 143 and .steps[0].summary.artifactsDirectory == true and .steps[0].summary.nestedInterruptNote == true and .steps[0].summary.nestedCollectionFailed == false and .steps[0].summary.commandStopped == "on TERM, within the grace"'

	# TERM to the nested runner alone: the docker client keeps running, and the runner's trap must still stop the
	# workload before the copy.
	n_signal nest-direct TERM -- bash deploy/digitalocean/restore-drill-nested.sh --out "$tmp/nest-direct" --qc1
	t_rc nest-direct 143
	n_collected nest-direct "$tmp/nest-direct"
	n_before ' pkill -TERM ' '^cp c\{64\}:/work/out/\. ' || t_fail "nest-direct: the run inside was not asked to stop before its artifacts were copied"
	jq -e '.aborted.signal == "TERM"' "$tmp/nest-direct/evidence-status.json" >/dev/null 2>&1 || t_fail "nest-direct: the run inside did not finish its own cleanup before the copy"

	# A run inside that will not stop is killed after the grace, and its partial artifacts are still copied out.
	n_signal nest-kill TERM STUB_INNER_STUBBORN=1 FOLD_NESTED_STOP_GRACE=2 -- bash scripts/reason-review-scale.sh --evidence-dir "$tmp/nest-kill" --only qc1
	t_rc nest-kill 143
	n_collected nest-kill "$tmp/nest-kill/qc1"
	n_before ' pkill -KILL ' '^cp c\{64\}:/work/out/\. ' || t_fail "nest-kill: the stubborn run inside was not killed before the copy"
	grep -q 'was killed' "$tmp/nest-kill/qc1/INTERRUPTED.txt" 2>/dev/null || t_fail "nest-kill: INTERRUPTED.txt does not say the run had to be killed"

	# A copy that fails is recorded, never silent, and the container is still removed afterwards.
	n_signal nest-cpfail TERM STUB_CP_FAIL=out -- bash scripts/reason-review-scale.sh --evidence-dir "$tmp/nest-cpfail" --only qc1
	t_rc nest-cpfail 143
	grep -q '/work/out' "$tmp/nest-cpfail/qc1/COLLECTION-FAILED.txt" 2>/dev/null || t_fail "nest-cpfail: the failed copy was not recorded"
	[[ -s $tmp/nest-cpfail/qc1/prep/browser-runtime.json ]] || t_fail "nest-cpfail: the preparation that could be copied was not"
	n_before '^cp c\{64\}:/work/out/\. ' '^rm -fv ' || t_fail "nest-cpfail: the container was removed before the copy was tried"
	t_check nest-cpfail "the wrapper's record says the collection failed" '.steps[0].summary.nestedCollectionFailed == true and .steps[0].verdict == "interrupted"'

	# The runner's normal end is unchanged: the artifacts are copied, then the container goes, and it leaves no interruption note.
	n_reset
	rc=0
	(cd "$nroot" && env "PATH=$bin:$PATH" "STUB_FAKE=$fake" "STUB_LOG=$tmp/nlog" STUB_QUICK=1 STUB_QUICK_RC=3 \
		bash deploy/digitalocean/restore-drill-nested.sh --out "$tmp/nest-done" --qc1) >"$tmp/nest-done.out" 2>&1 || rc=$?
	[[ $rc == 3 ]] || t_fail "nest-done: exit $rc, want the run's own 3"
	[[ -s $tmp/nest-done/done && ! -e $tmp/nest-done/INTERRUPTED.txt && ! -e $tmp/nest-done/COLLECTION-FAILED.txt ]] || t_fail "nest-done: the normal end copied the wrong things"
	n_before '^cp c\{64\}:/work/out/\. ' '^rm -fv ' || t_fail "nest-done: the container was removed before the copy"
	if grep -q ' pkill \| pgrep ' "$tmp/nlog"; then t_fail "nest-done: a run that ended by itself was asked to stop"; fi

	kill "${leftovers[@]}" 2>/dev/null || true

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
		echo "                 full only when its evidence-status.json says complete, coverage full, releaseEvidence true;"
		echo "                 cases it names as pending (not measured yet) make the run partial, and a missing or invalid measurement fails it"
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

# --- process ownership and cancellation --------------------------------------
# See "Process ownership" in the header. Each step is one background pipeline, so
# one job in bash's table with a process group of its own: a holder, which only
# keeps the group alive and watches the supervisor, piped to the supervisor, which
# runs the command and reports its status. Both stay alive until stop_step releases
# them, and the job stays in the table while either does, so a command that exits
# while a descendant ignoring TERM lives on (the group is still held by the holder)
# leaves that descendant signalable. The group is signalled only through that job
# (kill %%), never by a number taken from a process list.
set -m
[[ $- == *m* ]] || die "bash job control (set -m) is not available here; the run cannot own its steps' processes"

ctl=$(mktemp -d) # the supervisor's ready and status files; never inside the evidence directory
active_job=      # the supervisor's PID (the pipeline's last process) while a step is registered, else empty
active_pgid=     # the group's id, for messages only
defer=0          # 1 while a signal must be held (launching a step, writing the final record)
pending_sig=
pending_code=
cancelling=0
stuck_group= # set when a group outlived KILL
stop_how=    # how a stop_step term ended: term (the command finished on TERM) or kill (the grace ran out)
cur_step=    # the step that has started and is not recorded yet
cur_dir=

# stop_step MODE ends the registered step's whole group and its job. MODE term first
# asks the command to leave and gives it the grace to finish its own cleanup (it is
# done when it has reported a status); kill goes straight to KILL. What is left of the
# group is then KILLed and the job reaped (bounded, about 5s). A group that outlives
# KILL is named on stderr and left in stuck_group.
# shellcheck disable=SC2329 # reached through cancel_run, run_step and the EXIT trap
stop_step() {
	[[ -n $active_job ]] || return 0
	local mode=$1 i stuck=0 grace=${FOLD_SCALE_STOP_GRACE:-180}
	[[ $grace =~ ^[0-9]+$ ]] || grace=180
	{
		# The supervisor installs its TERM handler first thing; signal it only after
		# it says so, or the group TERM would end the holder too early.
		for ((i = 0; i < 20; i++)); do
			[[ -e $ctl/ready ]] && break
			sleep 0.05
		done
		if [[ $mode == term ]]; then
			kill -s TERM %% || true
			stop_how=term
			for ((i = 0; i < grace * 10; i++)); do
				[[ -e $ctl/status ]] && break
				sleep 0.1
			done
			if [[ -e $ctl/status ]]; then sleep 0.5; else stop_how="kill"; fi
		fi
		kill -s KILL %% || true
		for ((i = 0; i < 50; i++)); do
			[[ -z $(jobs -rp) ]] && break
			sleep 0.1
		done
		if [[ -z $(jobs -rp) ]]; then
			wait "$active_job" || true
		else
			stuck=1
		fi
	} 2>/dev/null # only bash's job and kill notices; the diagnostic below is outside it
	if ((stuck)); then
		stuck_group=${active_pgid:-$active_job}
		echo "reason-review-scale: the step's process group $stuck_group did not exit after KILL; end it by hand" >&2
	fi
	active_job=
}

# interrupt_summary SIGNAL: what is known about the step a signal cut short.
interrupt_summary() {
	local dir=$cur_dir
	jq -nc --arg sig "$1" --arg stop "$stop_how" \
		--argjson note "$([[ -e $dir/INTERRUPTED.txt ]] && echo true || echo false)" \
		--argjson failed "$([[ -e $dir/COLLECTION-FAILED.txt ]] && echo true || echo false)" \
		--argjson kept "$([[ -d $dir ]] && echo true || echo false)" \
		'{interruptedBy:$sig, commandStopped:(if $stop == "term" then "on TERM, within the grace" elif $stop == "kill" then "only after KILL, past the grace" else "not asked" end),
		  artifactsDirectory:$kept, nestedInterruptNote:$note, nestedCollectionFailed:$failed}'
}

# shellcheck disable=SC2329 # reached only through the traps below
cancel_run() { # SIGNAL EXIT_CODE
	local sig=$1 code=$2 st
	if ((defer)); then
		pending_sig=$sig pending_code=$code
		return 0
	fi
	((cancelling == 0)) || return 0
	cancelling=1
	stop_step term
	if [[ -n $cur_step ]]; then
		step_status=$code
		if [[ -s $ctl/status ]]; then
			st=$(<"$ctl/status")
			[[ $st =~ ^[0-9]+$ ]] && step_status=$st
		fi
		step_secs=$((SECONDS - step_s0))
		step_end=$(now)
		why "$step_name was interrupted by $sig; its command stopped $([[ $stop_how == term ]] && echo 'on TERM' || echo 'only after KILL')"
		if [[ -n $stuck_group ]]; then why "process group $stuck_group outlived KILL"; fi
		record_step interrupted "$cur_dir" "$(interrupt_summary "$sig")"
	fi
	write_record incomplete partial
	finalized=1
	echo "reason-review-scale: interrupted by $sig; record: $evidence/evidence.json (incomplete)" >&2
	exit "$code"
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
	stop_step kill
	if ((recording && !finalized)); then
		write_record incomplete partial || true
	fi
	rm -rf "$ctl"
	exit "$rc"
}
trap on_int INT
trap on_term TERM
trap on_hup HUP
trap on_exit EXIT
[[ $(trap -p INT) == *on_int* ]] ||
	echo "reason-review-scale: warning: SIGINT was ignored when this script started, so INT cannot stop it; send TERM, or start it with bash -m" >&2

write_record incomplete partial

# run_step NAME LOG DIR COMMAND...: runs the command in its own supervised group with
# its output in the log and leaves the outcome in the step_* variables. A failed step
# does not stop the run, so the record is complete.
run_step() {
	local name=$1 log=$2 dir=$3 i job lost=0
	shift 3
	step_name=$name step_log=$log
	step_cmd=$(printf '%q ' "$@")
	step_cmd=${step_cmd% }
	step_begin=$(now)
	step_s0=$SECONDS
	cur_step=$name cur_dir=$dir
	echo "== $name: $step_cmd"
	[[ -z $(jobs -rp) ]] || die "a background job is already running; a step's group must be the only one"
	rm -f "$ctl/ready" "$ctl/ready.holder" "$ctl/status" "$ctl/status.tmp" "$ctl/lost" "$ctl/pgid"
	active_pgid=
	# From the fork to the registration a signal is only recorded, so it cannot find a
	# step that is running but not yet ours to stop.
	defer=1
	(
		# The holder, first in the pipeline and so the group's leader. It writes a byte
		# to the supervisor every second: if the supervisor is gone the write fails, and
		# the holder says so (the lost file) while it goes on keeping the group in the
		# job table until stop_step ends it.
		set +e +u
		trap '' PIPE
		trap : TERM INT HUP
		printf '%s\n' "$BASHPID" >"$ctl/pgid"
		: >"$ctl/ready.holder"
		while :; do
			sleep 1
			printf . 2>/dev/null || break
		done
		: >"$ctl/lost"
		while :; do sleep 1; done
	) 2>/dev/null | (
		# The supervisor: runs the command, reports its status, then holds.
		set +e +u
		trap : TERM INT HUP # handled, not ignored: the command's own processes keep default dispositions
		for ((i = 0; i < 50; i++)); do
			[[ -e $ctl/ready.holder ]] && break
			sleep 0.02
		done
		: >"$ctl/ready"
		"$@" </dev/null >"$evidence/$log" 2>&1
		rc=$?
		printf '%s\n' "$rc" >"$ctl/status.tmp" && mv "$ctl/status.tmp" "$ctl/status"
		{ while :; do sleep 1; done; } </dev/null 2>/dev/null
	) &
	job=$!
	active_job=$job
	defer=0
	if [[ -n $pending_sig ]]; then
		cancel_run "$pending_sig" "$pending_code"
	fi
	for ((i = 0; i < 20; i++)); do
		[[ -s $ctl/pgid ]] && break
		sleep 0.05
	done
	[[ -s $ctl/pgid ]] && active_pgid=$(<"$ctl/pgid")
	{
		while [[ ! -e $ctl/status ]]; do
			if [[ -z $(jobs -rp) ]]; then
				break # the whole job is gone without a status
			fi
			if [[ -e $ctl/lost ]]; then
				lost=1 # the supervisor is gone; the holder still holds the group
				break
			fi
			sleep 0.1
		done
	} 2>/dev/null # bash reports a job that died of a signal; the log and the record say it
	if [[ -e $ctl/status ]]; then
		step_status=$(<"$ctl/status")
		[[ $step_status =~ ^[0-9]+$ ]] || step_status=125
	else
		step_status=125
		if ((lost)); then
			echo "the step's supervisor ended without reporting a status; its group was ended" >>"$evidence/$log"
		else
			echo "the step's supervisor and holder both ended without a status; processes of group ${active_pgid:-$job} may remain" >>"$evidence/$log"
			echo "reason-review-scale: step $name: the supervisor and the holder of group ${active_pgid:-$job} both ended; nothing stable is left to signal, so anything the command started may still run" >&2
		fi
	fi
	stop_step kill
	if [[ -n $stuck_group ]]; then
		step_status=125
		echo "process group $stuck_group outlived KILL" >>"$evidence/$log"
	fi
	step_secs=$((SECONDS - step_s0))
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
	interrupted)
		overall_bad=1
		echo "   INTERRUPTED (exit $step_status), log: $evidence/$step_log"
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
	cur_step=
	write_record incomplete partial
}

why() { # a reason a step is not full or not good
	cov_reasons+=("$1")
	echo "   $1" >>"$evidence/$step_log"
}

# --- qc1 ---------------------------------------------------------------------
# qc1_summary FILE prints the QC-1 evidence-status.json as the summary kept in the record. The fields are the ones qc1/lib.sh
# writes: missing, pending (explicitly not measured yet: the legacy cases), invalid, budgetFailures, coverage.{kind,reasons},
# complete, releaseEvidence and, for a run that stopped early, aborted.
qc1_summary() {
	jq -c '
		def cases: map(select(type == "object") | {item:.item, size:.size, case:.case});
		{complete:(.complete // false), coverage:(.coverage.kind // null), releaseEvidence:(.releaseEvidence // false),
		 budgetFailures:((.budgetFailures // []) | length), invalid:((.invalid // []) | length),
		 missing:((.missing // []) | length), pending:((.pending // []) | length),
		 aborted:((.aborted // null) != null),
		 abortedReason:((.aborted // null) | if . == null then "" else (tostring | .[:300]) end),
		 missingCases:((.missing // [])[:10] | cases),
		 pendingCases:((.pending // [])[:10] | map(select(type == "object") | {item:.item, size:.size, case:.case, reason:(.reason // null)})),
		 invalidFindings:((.invalid // [])[:10] | map(select(type == "object") | {item:.item, size:.size, case:.case, failure:.failure, problem:.problem})),
		 coverageReasons:((.coverage.reasons // [])[:10])}' "$1"
}

# classify_qc1 SUMMARY sets verdict from a readable QC-1 record and says why in the step log and the record. It never turns a
# fail into anything better. A run is
#   fail     when it aborted, holds an invalid finding, lacks a measurement it should have, or is incomplete without saying why;
#   partial  when it finished normally and only cases it names as pending are unmeasured, or its coverage is not full;
#   pass     when it is complete, full coverage and release evidence.
classify_qc1() {
	local s=$1 n explained=0
	if jq -e '.aborted' <<<"$s" >/dev/null; then
		verdict=fail explained=1
		why "QC-1 aborted: $(jq -r '.abortedReason' <<<"$s")"
	fi
	n=$(jq -r '.invalid' <<<"$s")
	if ((n > 0)); then
		verdict=fail explained=1
		why "QC-1 reports $n invalid finding(s); first: $(jq -c '.invalidFindings[0]' <<<"$s")"
	fi
	n=$(jq -r '.missing' <<<"$s")
	if ((n > 0)); then
		verdict=fail explained=1
		why "QC-1 is missing $n expected measurement(s); first: $(jq -r '.missingCases[0] | "item \(.item), size \(.size), case \(.case)"' <<<"$s")"
	fi
	n=$(jq -r '.pending' <<<"$s")
	if ((n > 0)); then
		explained=1
		[[ $verdict == fail ]] || verdict=partial
		why "QC-1 finished with $n case(s) explicitly pending, not measured: $(jq -r '[.pendingCases[] | "item \(.item) \(.case)"] | join(", ")' <<<"$s") (reason: $(jq -r '.pendingCases[0].reason // "none given"' <<<"$s"))"
	fi
	if ! jq -e '.coverage == "full"' <<<"$s" >/dev/null; then
		[[ $verdict == fail ]] || verdict=partial
		why "QC-1 coverage is $(jq -r '.coverage // "unknown"' <<<"$s"), not full$(jq -r 'if (.coverageReasons | length) > 0 then ": " + (.coverageReasons | join("; ")) else "" end' <<<"$s")"
	fi
	if ! jq -e '.complete == true' <<<"$s" >/dev/null && ((!explained)); then
		verdict=fail
		why "QC-1 evidence-status.json is not complete and names no missing, pending or invalid case"
	elif [[ $verdict == pass ]] && ! jq -e '.releaseEvidence == true' <<<"$s" >/dev/null; then
		verdict=partial
		why "QC-1 does not call this release evidence"
	fi
}

if ((run_qc1)); then
	dir=$evidence/qc1
	run_step qc1 qc1.log "$dir" "${qc1_cmd[@]}"
	verdict=pass
	summary='{}'
	if ((step_status != 0)); then
		verdict=fail
		why "QC-1 exited $step_status"
	fi
	if [[ -s $dir/evidence-status.json ]] && jq -e . "$dir/evidence-status.json" >/dev/null 2>&1; then
		if summary=$(qc1_summary "$dir/evidence-status.json" 2>>"$evidence/$step_log"); then
			classify_qc1 "$summary"
		else
			summary='{}'
			verdict=fail
			why "QC-1 evidence-status.json does not have the shape qc1/lib.sh writes"
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
	run_step drill drill.log "$dir" "${drill_cmd[@]}"
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
