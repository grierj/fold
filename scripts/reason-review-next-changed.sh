#!/usr/bin/env bash
# The "Next changed reason" samples (issue #298): one record from the sampling runs of qc1-measure.sh --next-changed-only.
#
# Issue #298 has ONE slow observation (the control at 830.7 ms in a 100,000-reason pilot, against a proposed 300 ms target) and two fast ones (65.1 ms with a 1,000,000-row
# background, 60.6 ms in an earlier diagnostic whose environment record was incomplete). One observation is not a regression and not a pass. This script turns the repeated
# runs into the figures the issue asks for: for a 100,000-reason review over a 100,000-row and over a 1,000,000-row background, at least 30 presses in each of at least 3
# runs, with p50, p95 and max, the position and origin of the slow presses, and the machine's load around them. It measures nothing and starts no container.
#
#   1. Sample (hours; a disposable Docker daemon; see docs/agents/testing.md, "Sampling Next changed reason"):
#        scripts/reason-review-scale.sh --only qc1 --evidence-dir DIR \
#          --qc1-arg --sizes --qc1-arg 100000,1000000 --qc1-arg --counts --qc1-arg 0 --qc1-arg --dense-counts --qc1-arg 100000 \
#          --qc1-arg --next-changed-only --qc1-arg --next-changed-presses --qc1-arg 30 --qc1-arg --next-changed-runs --qc1-arg 3
#   2. Record:
#        scripts/reason-review-next-changed.sh --out FILE --evidence DIR/qc1 [--evidence DIR2/qc1 ...] [--revision SHA]
#   3. If a configuration's pooled p95 is over the target, profile before changing anything: repeat step 1 with --qc1-arg --profile-next-changed (one --evidence-dir per
#      repeat) and read the .cpuprofile files it leaves beside the results. A profiled run is a diagnostic and is never counted here.
#
#   scripts/reason-review-next-changed.sh --dry-run     print the configurations, the commands and the requirements, write nothing
#   scripts/reason-review-next-changed.sh --self-test   this script's own checks against stub evidence (seconds, no Docker)
#
# What counts. A run is one measurements.jsonl row, case browser-panel-<reasons>-nc<N>, that is measured (not partial or failed), stopped in no phase, answered by the stack's
# own origin, walked exactly the reasons seeded, was refused with 409 and reloaded, pressed the control as many times as it was asked to and at least 30 times (the recorded
# counts match the recorded presses, whose indices run 0..N-1 from the runner's origins with nonnegative times and a matching first press), never ran
# under the profiler, and carries the host's load counters (an absent counter is a gap, not a zero). Each evidence directory's environment must be a complete record of the
# revision asked for (qc1_environment_problems). A counted run must also be clean under the existing QC judge recomputed here from the recorded rows (qc1_judge over the
# directory's measurements.jsonl): an invalid finding for the row excludes it (it stays listed with the reason) while a budget finding is kept beside the row and never
# excludes it. Intentionally missing full-matrix cases never exclude a sampling-only row. A configuration is complete only with at least 3 counted runs; the record is
# COMPLETE only when both configurations are. Several --evidence directories add independent stacks: their runs are pooled by (background rows, reasons).
# A directory whose artifacts could not be collected (COLLECTION-FAILED.txt), whose run aborted (abort.json or an aborted evidence-status.json), whose environment is
# incomplete, whose evidence-status.json is missing, unreadable or malformed (it must be one finalized object: a boolean complete, typed missing/pending/invalid/
# budgetFailures arrays with valid entries, and a coverage record with a kind), that lacks an expected sampling measurement (a standard-shaped {item, size,
# case} sampling entry, the reason count parsed from its case name, like every other missing entry) or names a non-legacy pending case, or whose outer runner
# record is unfinished, unbound, or malformed, or marks its qc1 step interrupted, failed with a nonzero exit, passed or partial
# with a contradicting exit or a summary that contradicts the verdict or this directory's validated status, or failed with exit 0
# for any reason beyond intentional omissions, contributes runs that are listed but never counted. A present step's exit must be a
# JSON integer matching its verdict (pass takes 0 only; partial takes 0 or 10), and its summary must have the runner's own shape
# and agree with this directory's status. One exit-0 outer
# failure is accepted: a fail explained entirely by the sampling design omitting the full matrix (ordinary missing rows, legacy pending cases, pilot coverage),
# proven by the step's own summary agreeing with this directory's validated evidence-status.json. Expected sampling-only omissions are recorded, not failures.
# One stack is one observation: a repeated canonical --evidence directory is refused; a repeated identity within one measurements file excludes every occurrence
# (each is suspect); an identical record copied across directories of one stack is counted once and named, while conflicting records of one identity exclude
# every occurrence. Identity is the canonical original record: two records with equal timed presses but different status, window,
# agreement, sampling, page accounting or other recorded validity facts conflict instead of collapsing. A malformed local or outer
# record is a failure, never an omission; an absent outer record is the direct-QC-evidence shape and is fine.
#
# What it does not do. It does not call a delay noise, does not call a target met, and does not choose a fix: it prints the numbers, their load context and the one mechanical
# consequence the issue states (a pooled p95 over the target means a profile is the next step). The target is the scaling design's proposal (QC1_NAV_BUDGET_MS), not an
# owner decision. The original observations are quoted from the issue and stay beside the new ones.
# Exit status: 0 for COMPLETE, 10 for INCOMPLETE (the record lists every gap), 2 for a refusal before anything was written.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)

die() {
	echo "reason-review-next-changed: $*" >&2
	exit 2
}

out=
revision=
dry_run=0
self_test=0
dirs=()

while (($#)); do
	case $1 in
	--out | --revision | --evidence)
		(($# >= 2)) || die "$1 needs a value"
		case $1 in
		--out) out=$2 ;;
		--revision) revision=$2 ;;
		--evidence) dirs+=("$2") ;;
		esac
		shift 2
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

command -v jq >/dev/null || die "jq is required"

QC1_LIB=$root/deploy/digitalocean/qc1/lib.sh
[[ -r $QC1_LIB ]] || die "the QC-1 harness ($QC1_LIB) is not in this checkout"
unset QC1_FACTS
# shellcheck source=deploy/digitalocean/qc1/lib.sh
source "$QC1_LIB"

# The requirement of issue #298 / task T-298, and the configurations it names: a 100,000-reason review over a 100,000 and a 1,000,000-row background.
REQUIRED_PRESSES=30
REQUIRED_RUNS=3
REVIEW_REASONS=100000
BACKGROUNDS='[100000,1000000]'
TARGET_MS=$QC1_NAV_BUDGET_MS
# The single observations of the issue, quoted (issue #298 body); they are context, never samples.
ORIGINAL='[
 {"ms":830.7,"reasons":100000,"background":null,"note":"first 100,000-reason browser pilot; the first walk finished in 43.3 s; nested Docker, headless Chromium, host noise not ruled out","source":"issue #298"},
 {"ms":65.1,"reasons":100000,"background":1000000,"note":"later pilot with a 1-million-row background; same environment kind","source":"issue #298"},
 {"ms":60.6,"reasons":null,"background":null,"note":"earlier diagnostic pilot; its environment record was incomplete","source":"issue #298"}
]'

runner_commands() { # evidence-dir
	cat <<EOF
scripts/reason-review-scale.sh --only qc1 --evidence-dir $1 --qc1-arg --sizes --qc1-arg 100000,1000000 --qc1-arg --counts --qc1-arg 0 --qc1-arg --dense-counts --qc1-arg $REVIEW_REASONS --qc1-arg --next-changed-only --qc1-arg --next-changed-presses --qc1-arg $REQUIRED_PRESSES --qc1-arg --next-changed-runs --qc1-arg $REQUIRED_RUNS
scripts/reason-review-next-changed.sh --out $1/next-changed.json --evidence $1/qc1
EOF
}

# The jq definitions shared by the run and the pool: the nearest-rank percentile of deploy/digitalocean/qc1/panel-core.mjs, so a figure here equals the harness's own.
# shellcheck disable=SC2016  # jq programs, not shell expansions
JQ_DEFS='
def pct($p): if length == 0 then null else sort as $s | $s[([($p / 100 * length) | ceil, 1] | max) - 1] end;
def stats: {count: length, p50Ms: pct(50), p95Ms: pct(95), maxMs: (if length == 0 then null else max end)};
# A run identity is its stack, background rows and case. Two records of one identity are never two observations: within one
# measurements file every occurrence is suspect (the shared QC rule in deploy/digitalocean/qc1/lib.sh treats both duplicated
# measurements as suspect, and qc1_judge alone does not check duplication), so all of them stop counting. Across directories of
# one stack a demonstrably identical record is the same run copied, counted once; records that conflict are untrustworthy, so
# none of them counts. Runs of independent stacks pool. The outcome never depends on directory or row order: a uniform copy group
# always counts exactly one run, and a repeated or conflicting identity always counts none.
# The comparison token carries the canonical original record, not only timings: pressed ms, first-press ms, load and the
# browser version are already projected, and the original row status, window, agreement, full sampling record, full browser
# record and the row judge-invalid verdicts travel beside them (tok fields). The browser record carries every page-accounting,
# digest, phase, first-page, click, navigation, load and secure-context fact the QC judge reads, so two directories whose copies
# differ in any recorded validity fact get different tokens and exclude every occurrence. Carrying the whole browser record is
# fail-closed: a difference can only turn a collapse into an exclusion, never an exclusion into a count; only dir/stack
# (location metadata) is excluded.
def run_token: ([.presses, .firstPressMs, .load, .browser, .tokStatus, .tokWindow, .tokAgreement, .tokSampling, .tokChecks, .tokInvalid] | tojson);
def mark_duplicates:
  ((map([.dir, .background // -1, .case // "?"] | tojson)) | group_by(.) | map(select(length > 1) | .[0]) | unique) as $within
  | (. as $runs
    | ([range(0; $runs | length) | {i: ., key: ($runs[.] | [.stack // "?", .background // -1, .case // "?"] | tojson)}]
      | group_by(.key) | map(select(length > 1)) | map(select(((map(.i) | map($runs[.] | .dir)) | unique | length) > 1))) as $across
    | ($across | map({key: .[0].key, conflict: ((map(.i) | map($runs[.] | run_token)) | unique | length) > 1}) | map({(.key): .conflict}) | add // {}) as $conf
    | ($across | map(select(((map(.i) | map($runs[.] | run_token)) | unique | length) == 1) | {key: .[0].key, keep: ((map(.i) | map(select($runs[.] | .counted)) | .[0]) // (map(.i) | .[0]))}) | map({(.key): .keep}) | add // {}) as $keep
    | [range(0; $runs | length) as $i | $runs[$i]
      | ([$runs[$i].dir, $runs[$i].background // -1, $runs[$i].case // "?"] | tojson) as $wk
      | ([$runs[$i].stack // "?", $runs[$i].background // -1, $runs[$i].case // "?"] | tojson) as $ak
      | if $within | index($wk) then
          .counted = false
          | .problems += ["duplicate run in " + $runs[$i].dir + ": " + ($runs[$i].case // "?") + " over a " + ($runs[$i].background | tostring) + "-row background shares its identity with another row of the same measurements file; the shared QC rule treats every occurrence as suspect, so none of them counts"]
        elif ($conf | has($ak)) and $conf[$ak] then
          .counted = false
          | .problems += ["conflicting duplicate run: " + ($runs[$i].case // "?") + " over a " + ($runs[$i].background | tostring) + "-row background repeats the identity of a run in another directory of the same stack with different recorded measurements; neither record can be trusted, so none of them counts"]
        elif ($keep | has($ak)) and $keep[$ak] != $i then
          .counted = false
          | .problems += ["duplicate run: the same stack, case and background as the run in " + $runs[$keep[$ak]].dir + "; counted once, not again"]
        else . end
      ]);
'

# judge_findings DIR CAP prints {"ok":bool,"invalid":[],"budget":[]}: the existing QC judge (qc1_judge) recomputed over the directory's measurements.jsonl.
# Its findings carry the judged row's item, size and case, so the caller attaches each one to that sampling row (R1). A judge that cannot run is ok:false.
judge_findings() {
	local out judge
	if out=$(qc1_judge "$1/measurements.jsonl" "$2" 2>/dev/null) && judge=$(jq -sc '{ok:true,invalid:[.[] | select(.class == "invalid")],budget:[.[] | select(.class == "budget")]}' <<<"$out" 2>/dev/null); then
		printf '%s\n' "$judge"
	else
		echo '{"ok":false,"invalid":[],"budget":[]}'
	fi
}

# runs_json DIR INVALID BUDGET STACK prints the sampling rows of one evidence directory as an array of run objects, each with the reasons it does or does not
# count. INVALID/BUDGET are the judge findings (see judge_findings) attached to rows by item, size and case; STACK is the directory's stack identity, carried for
# the duplicate-run check. Budget findings are kept beside the row and never count against it. The sampling record itself is validated at the boundary (R6):
# requested/performed are whole numbers matching the recorded presses and at least the required count, every press is an object with a nonnegative time, a
# 0..N-1 index, a runner origin and a nonnegative offset, firstPressMs is the first press, and the load counters are numbers. A malformed value names its
# excluded row; no shape aborts the report. Summaries are computed only from safe projected presses (objects with a numeric
# time and a string origin, $ok), so a non-object element or a non-string origin cannot reach a jq field access or object key.
# The full original browser record and the row's own judge verdicts travel beside the row for the duplicate check (tok*).
runs_json() {
	local dir=$1 invalid=$2 budget=$3 stack=$4
	jq -sc --argjson need "$REQUIRED_PRESSES" --argjson target "$TARGET_MS" --arg dir "$dir" --argjson invalid "$invalid" --argjson budget "$budget" --arg stack "$stack" "$JQ_DEFS"'
		def isnum: type == "number";
		def isint: type == "number" and ((. % 1) == 0);
		[ .[] | select(type == "object" and .item == 10 and ((.case // "") | test("^browser-panel-[0-9]+-nc[0-9]+$")))
		  | . as $r | (($r.browser // {}) | if type == "object" then . else {} end) as $b | (($b.nextChangedSampling // null) | if type == "object" then . else null end) as $s
		  | ([ ($s.presses // [])[]? | select(type == "object") | .ms | select(isnum) ]) as $ms
		  | (($s.presses // []) | if type == "array" then . else [] end) as $pl
		  | ([ $pl[] | select(type == "object") | select((.ms | isnum) and (((.origin | type) == "string") or .origin == null))
		      | {index: .index, origin: (.origin // "unknown"), ms: .ms, atMs: .atMs, longTaskMs: (.longTaskMs // null), longTaskCount: (.longTaskCount // null)} ]) as $ok
		  | (($b.press // {}) | if type == "object" then . else {} end) as $bp
		  | (($b.reload // {}) | if type == "object" then . else {} end) as $br
		  | (($b.load // {}) | if type == "object" then . else {} end) as $bl
		  | {
		      dir: $dir, stack: $stack, case: .case, background: .size, reasons: .reasons, run: (.case | capture("-nc(?<n>[0-9]+)$").n | tonumber),
		      budgets: ([ $budget[] | select(.item == 10 and .size == $r.size and .case == $r.case) | {failure, detail: (del(.class, .failure, .item, .size, .case))} ]),
		      problems: [
		        (if .status != "measured" then "row status is \(.status), not measured" else empty end),
		        (if $b.failed != null then "the run stopped in \($b.failed.phase // "an unknown phase")" else empty end),
		        (if $b.apiOrigin != "real" then "the pages did not all come from the stack origin" else empty end),
		        (if $b.walkTotal != .reasons then "the walk found \($b.walkTotal) reasons, not \(.reasons)" else empty end),
		        (if ($bp.observed != true) or ($bp.status != "409") then "the refused press was not observed as 409" else empty end),
		        (if $br.unchosenAfterReload != 1 then "the review was not read again with exactly one reason to choose" else empty end),
		        (if $s == null then "the row has no nextChangedSampling (the run was not asked for repeated presses)" else empty end),
		        (if $s != null and (($s.requested | isint | not) or ($s.performed | isint | not)) then "requested/performed presses are not recorded as whole numbers" else empty end),
		        (if $s != null and ($s.performed != $s.requested) then "pressed \($s.performed) times of \($s.requested) asked" else empty end),
		        (if $s != null and (($s.requested // 0) != ($pl | length) or ($s.performed // 0) != ($pl | length)) then "requested/performed presses (\($s.requested)/\($s.performed)) do not match the \($pl | length) recorded presses" else empty end),
		        (if $s != null and (($s.requested // 0) < $need or ($s.performed // 0) < $need) then "fewer than \($need) presses asked for or performed" else empty end),
		        (if $s != null and ([$pl[] | select(type != "object")] | length) > 0 then "a press record is not an object" else empty end),
		        (if $s != null and ($ms | length) < $need then "\($ms | length) timed presses, fewer than \($need)" else empty end),
		        (if $s != null and ($ms | length) != ($pl | length) then "a press has no time" else empty end),
		        (if $s != null and ([$pl[] | select(type == "object") | .ms | select((type != "number") or (. < 0))] | length) > 0 then "a press time is not a nonnegative number" else empty end),
		        (if $s != null and ([$pl[] | select(type == "object") | .index] != [range($pl | length)]) then "press indices are not the sequence 0..N-1 in order" else empty end),
		        (if $s != null and ([$pl[] | select(type == "object") | .origin | select(. != "first-page" and . != "after-changed-page" and . != "last-page")] | length) > 0 then "a press origin is not one the runner records (first-page, after-changed-page, last-page)" else empty end),
		        (if $s != null and (($s.firstPressMs | type) != "number" or $s.firstPressMs != ((($pl[0] | select(type == "object")) // null) | .ms)) then "firstPressMs does not match the first recorded press" else empty end),
		        (if $s != null and ([$pl[] | select(type == "object") | .atMs | select((type != "number") or (. < 0))] | length) > 0 then "a press offset is not a nonnegative number" else empty end),
		        (if $s != null and ($s.profile != null) then "the run was profiled (sampling overhead): a diagnostic, not a sample" else empty end),
		        (if $b.load == null then "no host load counters were read"
		         elif ($bl.presses | type) != "object" or ($bl.presses.seconds == null) or ($bl.presses.stealPercent == null) or ($bl.presses.load1Start == null) or ($bl.presses.load1End == null)
		         then "the host load counters around the presses are incomplete (loadavg, CPU steal)"
		         elif ([$bl.presses.seconds, $bl.presses.stealPercent, $bl.presses.load1Start, $bl.presses.load1End] | map(select(type == "number")) | length) != 4
		         then "the host load counters around the presses are not numbers (loadavg, CPU steal)"
		         else empty end)
		      ],
		      counted: false,
		      presses: $ms,
		      tokStatus: .status,
		      tokWindow: (.window // null),
		      tokAgreement: (.agreement // null),
		      tokSampling: $s,
		      tokChecks: ({browser: $b, row: ({charset: (.charset // null), textShape: (.textShape // null), targetReasonLen: (.targetReasonLen // null)})}),
		      tokInvalid: ([ $invalid[] | select(.item == 10 and .size == $r.size and .case == $r.case) ]),
		      stats: ($ms | stats),
		      overTarget: ([ $ms[] | select(. > $target) ] | length),
		      firstPressMs: ($s.firstPressMs // null),
		      afterFirst: (($pl[1:] | map(select(type == "object") | .ms | select(isnum))) | stats),
		      byOrigin: ((reduce $ok[] as $p ({}; .[$p.origin] += [$p.ms])) | map_values(stats)),
		      slowest: (($ok | sort_by(-.ms) | .[0:5] | map({index, origin, ms, atMs, longTaskMs, longTaskCount}))),
		      load: ($b.load // null),
		      browser: ($b.browser // null)
		    }
		  | .problems += ([ $invalid[] | select(.item == 10 and .size == $r.size and .case == $r.case) | "the QC judge finds this row invalid: \(.failure)" ])
		  | .counted = (.problems | length == 0)
		]' "$dir/measurements.jsonl"
}

# A missing sampling entry has the status schema's own shape: {item, size, case}, with the reason count carried in the case name
# (browser-panel-<reasons>-nc<n>, as the row's case), never a reasons field. requested_sampling names one the sampling design
# expects: item 10, a case for this reason count, a background this report pools. It is the one predicate for a missing sampling
# row, shared by the directory check and the outer-failure tie-out so the two never disagree.
# shellcheck disable=SC2016  # the jq parameters $reasons/$backgrounds expand in jq, not in shell
SAMPLING_DEFS='def requested_sampling($reasons; $backgrounds): type == "object" and .item == 10 and (((((.case // "") | capture("^browser-panel-(?<r>[0-9]+)-nc[0-9]+$") // null) | if . == null then -1 else (.r | tonumber) end) == $reasons) and (((.size // -1)) as $s | ($backgrounds | index($s)) != null));'

# outer_fail_explained STEP DIR is 0 iff an exit-0 outer qc1 failure is explained entirely by the sampling design omitting the full matrix: the
# step's own summary agrees with this directory's validated evidence-status.json (same aborted/invalid/missing/pending counts, same coverage kind, both
# incomplete), and the local lists hold no abort, no invalid finding, no missing sampling row and no non-legacy pending case. Anything else — a summary that
# cannot be tied to this directory, or lists that name a real execution, finalization, cleanup, invalid-row or collection failure — is unexplained (1).
outer_fail_explained() { # outer qc1 step JSON, evidence dir
	local step=$1 d=$2
	[[ -s $d/evidence-status.json ]] || return 1
	jq -ne --argjson step "$step" --slurpfile loc "$d/evidence-status.json" --argjson reasons "$REVIEW_REASONS" --argjson backgrounds "$BACKGROUNDS" \
		"$SAMPLING_DEFS"'($loc[0] // null) as $l | ($step.summary // null) as $s
		| (($s | type) == "object") and (($l | type) == "object")
		and ($s.aborted == false) and ($l.aborted == null)
		and (($s.collectionFailed // false) == false)
		and (($s.invalid // -1) == 0) and ((($l.invalid // null) | length) == 0)
		and (($s.missing // -1) == (($l.missing // null) | length)) and (($s.pending // -1) == (($l.pending // null) | length))
		and (($s.coverage // "") == (($l.coverage.kind // null) | tostring)) and ($s.complete == false) and ($l.complete == false)
		and ((($s.missing // 0) + ($s.pending // 0)) > 0)
		and ([($l.missing // [])[] | select(requested_sampling($reasons; $backgrounds))] | length == 0)
		and ([($l.pending // [])[] | select((type == "object" and (.item == 4 or .item == 5)) | not)] | length == 0)
	' >/dev/null 2>&1
}

# outer_step_summary_ok STEP VERDICT is 0 iff the step summary has the runner's own qc1_summary shape
# (reason-review-scale.sh qc1_summary: counts and kinds, not the rows): a boolean complete, a coverage string,
# numeric budget-failure/invalid/missing/pending counts and a boolean aborted. A pass or partial additionally
# records no abort and no collection failure, and says what its verdict means: pass is complete with full coverage,
# release evidence and zero missing, pending and invalid; partial carries no missing and no invalid (its pending
# cases and pilot coverage are the sampling design). Anything else is a malformed step, never a counted run.
outer_step_summary_ok() { # step verdict
	local step=$1 verdict=$2
	jq -e --arg verdict "$verdict" '
		(.summary // null) as $s
		| (($s | type) == "object")
		and ((($s.complete | type) == "boolean") and (($s.coverage | type) == "string")
			and (($s.budgetFailures | type) == "number") and (($s.invalid | type) == "number")
			and (($s.missing | type) == "number") and (($s.pending | type) == "number")
			and (($s.aborted | type) == "boolean"))
		and (if $verdict == "pass" or $verdict == "partial" then ($s.aborted == false) and (($s.collectionFailed // false) == false) else true end)
		and (if $verdict == "pass" then $s.complete == true and $s.coverage == "full" and ($s.releaseEvidence == true)
			and $s.invalid == 0 and $s.missing == 0 and $s.pending == 0 else true end)
		and (if $verdict == "partial" then $s.invalid == 0 and $s.missing == 0 else true end)
	' <<<"$step" >/dev/null 2>&1
}

# outer_summary_matches STEP DIR is 0 iff the step summary says what this directory's validated
# evidence-status.json says: the same completeness, the same missing, pending and invalid counts and the
# same coverage kind. The runner writes the summary from this file (qc1_summary), so a mismatch means the
# record does not describe this evidence.
outer_summary_matches() { # step dir
	local step=$1 d=$2
	jq -ne --argjson step "$step" --slurpfile loc "$d/evidence-status.json" '
		($loc[0] // null) as $l | ($step.summary // null) as $s
		| (($s | type) == "object") and (($l | type) == "object")
		and (($s.complete // null) == ($l.complete // null))
		and (($s.missing // -1) == (($l.missing // null) | length))
		and (($s.pending // -1) == (($l.pending // null) | length))
		and (($s.invalid // -1) == (($l.invalid // null) | length))
		and (($s.coverage // "") == (($l.coverage.kind // null) | tostring))
	' >/dev/null 2>&1
}

# collection_problems DIR prints the collection and execution failures of one evidence directory (R2) as {"coll":[...],"omit":[...]}: "coll" fails the
# directory, "omit" holds the expected sampling-only omissions, which are recorded but never fail. A failure excludes the directory's runs from the pool; the runs stay listed
# with their own sampling reasons, and the failure itself is a gap. Expected omissions are the sampling design leaving out the full matrix: missing rows that
# are not sampling rows, pending legacy cases, pilot coverage. Anything else unfinished — an uncollected copy, an abort, an incomplete environment, a missing
# or malformed evidence-status.json, a missing sampling row, a non-legacy pending case, an unexplained incomplete status, a present outer runner record that is
# unfinished, unbound, or malformed, or marks no single well-formed qc1 step for this directory and revision, or an outer qc1 step that failed for any reason
# beyond intentional omissions, contradicts its exit, carries a summary that contradicts its verdict or this directory's status, or was
# interrupted — is a failure. A pass or partial step is accepted only with a JSON-integer exit matching its verdict and a summary that
# has the runner's shape and says what this directory's status says.
collection_problems() { # dir rev: prints {"coll":[...],"omit":[...]} for one evidence directory (R2, R5)
	local d=$1 rev=$2 lines entry entry2 n parent shape step coll_json omit_json canon art verdict step_exit malformed_binding
	local -a failures=() skips=() bound=()
	if [[ -e $d/COLLECTION-FAILED.txt ]]; then
		lines=$(head -c 2000 "$d/COLLECTION-FAILED.txt" 2>/dev/null | jq -Rsc '[split("\n") | map(select(. != "")) | .[:5] | .[]] | join("; ")' 2>/dev/null || echo "see $d/COLLECTION-FAILED.txt")
		failures+=("the run's artifacts could not be collected completely before the copy (COLLECTION-FAILED.txt): $lines")
	fi
	if [[ -s $d/abort.json ]]; then
		entry=$(jq -c '{signal: (.signal // null), step: (.step // null), exitCode: (.exitCode // null)}' "$d/abort.json" 2>/dev/null || echo '{}')
		failures+=("the run aborted before it finished (abort.json carries $entry); the rows are as measured, not a finished run")
	fi
	if jq -e '.incomplete == true' "$d/environment.json" >/dev/null 2>&1; then
		entry=$(jq -r '.incompleteReason // "no reason recorded"' "$d/environment.json" 2>/dev/null || echo "no reason recorded")
		failures+=("the environment record is incomplete: $entry")
	fi
	if [[ ! -s $d/evidence-status.json ]] || ! jq -e . "$d/evidence-status.json" >/dev/null 2>&1; then
		failures+=("evidence-status.json is missing or unreadable: the run cannot be shown to have finished")
	# R5: the status must be one finalized object — a boolean complete, typed missing/pending/invalid/budgetFailures arrays with
	# valid entries, and a coverage record with a kind — before anything is read from it. A present but malformed status is a
	# failure, never a silent omission; sampling-only incompleteness (complete:false with named missing/pending rows) still passes.
	elif shape=$(jq -r '
			def validcase: type == "object" and ((.item | type) == "number") and ((.size | type) == "number") and ((.case | type) == "string");
			if type != "object" then "not a JSON object"
			else ([(if (.complete | type) != "boolean" then "complete is not a boolean" else empty end),
				(if (.missing | type) != "array" or ([.missing[] | select(validcase | not)] | length) > 0 then "missing is not an array of {item, size, case}" else empty end),
				(if (.pending | type) != "array" or ([.pending[] | select(validcase | not)] | length) > 0 then "pending is not an array of {item, size, case}" else empty end),
				(if (.invalid | type) != "array" or ([.invalid[] | select((type == "object" and ((.failure | type) == "string")) | not)] | length) > 0 then "invalid is not an array of findings with a failure" else empty end),
				(if (.budgetFailures | type) != "array" or ([.budgetFailures[] | select(type != "object")] | length) > 0 then "budgetFailures is not an array of findings" else empty end),
				(if (.coverage | type) != "object" or ((.coverage.kind | type) != "string") then "coverage is not a record with a kind" else empty end)]
				| join("; ")) | if . == "" then "ok" else "malformed: \(.)" end end' "$d/evidence-status.json" 2>/dev/null) && [[ $shape == ok ]]; then
		if jq -e '.aborted != null' "$d/evidence-status.json" >/dev/null 2>&1; then
			entry=$(jq -c '.aborted' "$d/evidence-status.json" 2>/dev/null || echo '"?"')
			failures+=("evidence-status.json carries an abort: $entry")
		fi
		# Invalid findings without a row identity are the tool's own verdict on the environment, the settings, duplicates and the judge run itself.
		while IFS= read -r entry; do
			if [[ -n $entry ]]; then
				failures+=("evidence-status.json records an invalid finding: $entry")
			fi
		done < <(jq -rc '.invalid // [] | map(select(type == "object" and (.item == null or .case == null))) | .[] | "\(.failure // "unknown"): \((.problem // .what // "") | tostring | .[:200])"' "$d/evidence-status.json" 2>/dev/null || true)
		# A missing sampling row is a failure; missing anything else is the sampling design omitting the full matrix.
		while IFS= read -r entry; do
			if [[ -n $entry ]]; then
				failures+=("an expected sampling measurement is missing: $entry")
			fi
		done < <(jq -rc --argjson reasons "$REVIEW_REASONS" --argjson backgrounds "$BACKGROUNDS" \
			"$SAMPLING_DEFS"'(.missing // []) | map(select(requested_sampling($reasons; $backgrounds))) | .[] | "item \(.item), size \(.size), case \(.case)"' \
			"$d/evidence-status.json" 2>/dev/null || true)
		n=$(jq -r --argjson reasons "$REVIEW_REASONS" --argjson backgrounds "$BACKGROUNDS" \
			"$SAMPLING_DEFS"'[(.missing // [])[] | select(type == "object" and (requested_sampling($reasons; $backgrounds) | not))] | length' "$d/evidence-status.json" 2>/dev/null || echo 0)
		if [[ $n =~ ^[0-9]+$ ]] && ((n > 0)); then
			skips+=("$n expected measurement(s) of the full matrix are omitted by the sampling design (they are not sampling rows)")
		fi
		# Pending legacy cases carry their T07B reason by design; any other pending case is a failure.
		while IFS= read -r entry; do
			if [[ -n $entry ]]; then
				failures+=("a non-legacy case is pending: $entry")
			fi
		done < <(jq -rc '.pending // [] | map(select(type == "object" and .item != 4 and .item != 5)) | .[] | "item \(.item), size \(.size), case \(.case) (reason: \(.reason // "none"))"' "$d/evidence-status.json" 2>/dev/null || true)
		n=$(jq -r '[.pending // [] | .[] | select(type == "object" and (.item == 4 or .item == 5))] | length' "$d/evidence-status.json" 2>/dev/null || echo 0)
		if [[ $n =~ ^[0-9]+$ ]] && ((n > 0)); then
			skips+=("$n legacy case(s) are pending by design (their T07B-3a reason is named)")
		fi
		if jq -e '.complete == false and ((.missing // []) | length) == 0 and ((.pending // []) | length) == 0 and ((.invalid // []) | length) == 0 and .aborted == null' "$d/evidence-status.json" >/dev/null 2>&1; then
			failures+=("evidence-status.json is not complete and names no missing, pending or invalid case")
		fi
		entry=$(jq -r '.coverage.kind // "unknown"' "$d/evidence-status.json" 2>/dev/null || echo unknown)
		if [[ $entry != full ]]; then
			skips+=("coverage is $entry, not full: sampling is a pilot by construction")
		fi
	else
		failures+=("evidence-status.json ${shape:-is unreadable}: the finalization proof cannot be read, so nothing from this directory counts")
	fi
	# The outer runner record beside the evidence directory (reason-review-scale.sh write_record: schema fold/reason-review-scale/v1
	# with revision, coverage, tools and steps). An absent record is the direct-QC-evidence shape and is fine; a present record must
	# be a finished run bound to this directory: a recognized result other than "incomplete" (the wrapper publishes result "incomplete"
	# with no steps before anything runs, and again after every step, so "incomplete" means unfinished), the revision asked for, and
	# exactly one well-formed qc1 step whose artifacts are this directory, with a recognized verdict, a JSON-integer exit and an
	# object summary in the runner's shape (outer_step_summary_ok) that agrees with the verdict and, for pass or partial, with this
	# directory's validated status (outer_summary_matches). A qc1 step that was interrupted, or that failed with a nonzero exit,
	# fails the evidence, as does an exit contradicting its verdict (pass takes exit 0 only; partial takes 0 or 10, the runner's
	# own finished-normally outcomes). So does an exit-0
	# failure for any reason beyond intentional omissions. The one accepted shape is an exit-0 fail explained entirely by the sampling
	# design (ordinary missing rows, legacy pending cases, pilot coverage), proven by the step's own summary agreeing with this
	# directory's validated evidence-status.json; it is recorded as an omission, never a failure.
	parent=$(dirname -- "$(realpath -m "$d")")/evidence.json
	canon=$(realpath -m "$d")
	if [[ -e $parent ]]; then
		if [[ ! -s $parent ]] || ! jq -e . "$parent" >/dev/null 2>&1; then
			failures+=("the outer runner record $parent is present but unreadable: the run around this evidence cannot be checked")
		elif [[ $(jq -r 'if type == "object" and (.steps | type) == "array" then "ok" else "bad" end' "$parent" 2>/dev/null) != ok ]]; then
			failures+=("the outer runner record $parent is malformed: it has no steps array, so the run around this evidence cannot be checked")
		elif [[ $(jq -r '.result // "missing"' "$parent" 2>/dev/null) == incomplete ]]; then
			failures+=("the outer runner record $parent is unfinished (result incomplete): the run around this evidence had not finished writing steps")
		elif [[ $(jq -r 'if (.result | type) == "string" and (.result == "pass" or .result == "partial" or .result == "fail") then "ok" else "bad" end' "$parent" 2>/dev/null) != ok ]]; then
			failures+=("the outer runner record $parent is malformed: it has no recognized result (pass, partial or fail), so the run around this evidence cannot be checked")
		elif [[ $(jq -r '.revision // "missing"' "$parent" 2>/dev/null) != "$rev" ]]; then
			entry=$(jq -r '.revision // "no revision recorded"' "$parent" 2>/dev/null || echo "unreadable")
			failures+=("the outer runner record $parent is for revision $entry, not $rev: it does not describe the run around this evidence")
		else
			bound=()
			malformed_binding=0
			while IFS= read -r step; do
				[[ -n $step ]] || continue
				art=$(jq -r '.artifacts // empty' <<<"$step" 2>/dev/null || true)
				if [[ -z $art ]]; then malformed_binding=1; continue; fi
				[[ $(realpath -m "$art" 2>/dev/null) == "$canon" ]] || continue
				bound+=("$step")
			done < <(jq -rc '.steps[] | select(type == "object" and .name == "qc1")' "$parent" 2>/dev/null || true)
			if ((malformed_binding)); then
				failures+=("the outer runner record $parent is malformed: a qc1 step has no artifacts binding it to an evidence directory")
			fi
			if ((${#bound[@]} == 0)); then
				failures+=("the outer runner record $parent marks no qc1 step bound to $d: the run around this evidence cannot be checked")
			elif ((${#bound[@]} > 1)); then
				failures+=("the outer runner record $parent marks ${#bound[@]} qc1 steps bound to $d: the run around this evidence cannot be checked")
			else
				step=${bound[0]}
				entry=$(jq -r '"\(.verdict) (exit \((.exit // null) | tojson))"' <<<"$step" 2>/dev/null || echo "unreadable")
				verdict=$(jq -r '.verdict // "missing"' <<<"$step" 2>/dev/null || echo "unreadable")
				step_exit=$(jq -r 'if ((.exit | type) == "number") then (if (((.exit | floor) == .exit) and (.exit >= 0)) then (.exit | tostring) else "missing" end) else "missing" end' <<<"$step" 2>/dev/null || echo "unreadable")
				if [[ $verdict != pass && $verdict != partial && $verdict != fail && $verdict != interrupted ]] || [[ $step_exit == missing || $step_exit == unreadable ]] || [[ $(jq -r 'if (.summary | type) == "object" then "ok" else "bad" end' <<<"$step" 2>/dev/null) != ok ]]; then
					failures+=("the outer runner record $parent is malformed: its qc1 step has no recognized verdict, numeric exit and object summary ($entry)")
				elif [[ $verdict == interrupted ]]; then
					failures+=("the outer runner record $parent marks its qc1 step $entry: the step was interrupted")
				elif [[ $verdict == pass && $step_exit != 0 ]]; then
					failures+=("the outer runner record $parent marks its qc1 step pass with a contradictory exit $step_exit")
				elif [[ $verdict == partial && $step_exit != 0 && $step_exit != 10 ]]; then
					failures+=("the outer runner record $parent marks its qc1 step partial with exit $step_exit")
				elif [[ $verdict == pass || $verdict == partial ]] && ! outer_step_summary_ok "$step" "$verdict"; then
					entry2=$(jq -c '{verdict, exit, summary: (.summary // null | {complete, coverage, missing, pending, invalid, aborted})}' <<<"$step" 2>/dev/null || echo "$entry")
					failures+=("the outer runner record $parent marks its qc1 step $entry with a summary contradicting that verdict: $entry2")
				elif [[ $verdict == pass || $verdict == partial ]] && ! outer_summary_matches "$step" "$d"; then
					entry2=$(jq -c '{verdict, exit, summary: (.summary // null | {complete, coverage, missing, pending, invalid, aborted})}' <<<"$step" 2>/dev/null || echo "$entry")
					failures+=("the outer runner record $parent marks its qc1 step $entry with a summary that does not match $d/evidence-status.json: $entry2")
				elif [[ $verdict == fail && $step_exit != 0 ]]; then
					failures+=("the outer runner record $parent marks its qc1 step $entry")
				elif [[ $verdict == fail ]]; then
					if outer_fail_explained "$step" "$d"; then
						n=$(jq -r '[(.missing // []) | length, (.pending // []) | length] | join("/")' "$d/evidence-status.json" 2>/dev/null || echo "?/?")
						skips+=("the outer runner record marks its qc1 step fail (exit 0), explained entirely by $n omitted full-matrix measurements/legacy pending cases under pilot coverage (the sampling design omits the full matrix)")
					else
						entry=$(jq -c '{verdict, exit, summary: (.summary // null | {complete, coverage, missing, pending, invalid, aborted})}' <<<"$step" 2>/dev/null || echo "$entry")
						failures+=("the outer runner record $parent marks its qc1 step fail (exit 0) for reasons beyond intentional omissions: $entry")
					fi
				else
					: # a passing or partial step with a matching exit and a summary that agrees with the verdict and this directory is the expected sampling shape (the full matrix is omitted by construction)
				fi
			fi
		fi
	fi
	if ((${#failures[@]})); then coll_json=$(printf '%s\n' "${failures[@]}" | jq -R . | jq -sc .); else coll_json='[]'; fi
	if ((${#skips[@]})); then omit_json=$(printf '%s\n' "${skips[@]}" | jq -R . | jq -sc .); else omit_json='[]'; fi
	jq -nc --argjson coll "$coll_json" --argjson omit "$omit_json" '{coll:$coll, omit:$omit}'
}

# build_record DIR... prints the record (stdout) from the evidence directories; gaps are in the record, never errors.
build_record() {
	local rev=$1 d all='[]' dirjson='[]'
	shift
	# (Repeated canonical --evidence directories were already refused before anything was written; repeated or conflicting run identities are marked below.)
	for d in "$@"; do
		local present=true why='' problems='[]' envrev=null browserv=null runs='[]' stack='' cap=0
		local judge='{"ok":true,"invalid":[],"budget":[]}' judge_invalid='[]' judge_budget='[]' dup_invalid='[]' coll='[]' omit='[]'
		local cj=''
		if [[ ! -s $d/measurements.jsonl || ! -s $d/environment.json ]] || ! jq -e . "$d/environment.json" >/dev/null 2>&1; then
			present=false
			why='measurements.jsonl or environment.json is missing or unreadable'
		elif ! jq -sc . "$d/measurements.jsonl" >/dev/null 2>&1; then
			present=false
			why='measurements.jsonl is not JSON lines'
		else
			envrev=$(jq -c '.revision // null' "$d/environment.json")
			browserv=$(jq -c '.browserRuntime.chromium // null' "$d/environment.json")
			stack=$(jq -r '[.appContainerId // "?", .dbContainerId // "?", .revision // "?"] | join("/")' "$d/environment.json" 2>/dev/null || echo "?/?/?")
			problems=$(qc1_environment_problems "$d/environment.json" "$rev" 2>/dev/null | jq -R . | jq -sc .) || problems='["the environment record could not be checked"]'
			cap=$(jq -r '.maxRequestBytes // 0' "$d/environment.json" 2>/dev/null || echo 0)
			if [[ ! $cap =~ ^[0-9]+$ ]]; then cap=0; fi
			# R1: recompute the existing QC judge from the recorded rows. An invalid finding excludes its row; budgets are kept beside it.
			# R3: the recompute does not check duplication, so the directory's own duplicate-row findings (the one invalid class the judge
			# lacks) are honored beside it: a row they name is excluded with the finding attached, like any other invalid row.
			judge=$(judge_findings "$d" "$cap")
			if [[ $(jq -r '.ok' <<<"$judge") != true ]]; then
				problems=$(jq -c '. + ["the QC judge could not be recomputed from measurements.jsonl"]' <<<"$problems")
			else
				judge_invalid=$(jq -c '.invalid' <<<"$judge")
				judge_budget=$(jq -c '.budget' <<<"$judge")
				dup_invalid=$(jq -c '[.invalid[]? | select(type == "object" and .failure == "duplicate-row" and .item != null and .case != null)]' "$d/evidence-status.json" 2>/dev/null || echo '[]')
				judge_invalid=$(jq -c --argjson dup "$dup_invalid" '. + $dup' <<<"$judge_invalid" 2>/dev/null || echo "$judge_invalid")
			fi
			if ! runs=$(runs_json "$d" "$judge_invalid" "$judge_budget" "$stack"); then
				runs='[]'
				problems=$(jq -c '. + ["the sampling rows could not be read; nothing from this directory counts"]' <<<"$problems")
			fi
			# A directory whose runs were not asked for repeated presses by its own settings is named, so a mislabelled directory is a visible gap.
			if [[ $(jq -r '.settings.nextChangedOnly // false' "$d/environment.json") != true ]]; then
				problems=$(jq -c '. + ["environment.json does not record --next-changed-only"]' <<<"$problems")
			fi
			# R2: collection and execution failures exclude the directory's runs but stay visible as gaps.
			cj=$(collection_problems "$d" "$rev")
			if ! jq -e . <<<"$cj" >/dev/null 2>&1; then cj='{"coll":["the collection markers could not be checked"],"omit":[]}'; fi
			coll=$(jq -c '.coll' <<<"$cj")
			omit=$(jq -c '.omit' <<<"$cj")
		fi
		dirjson=$(jq -c --arg dir "$d" --argjson present "$present" --arg why "$why" --argjson revision "$envrev" --argjson chromium "$browserv" --argjson problems "$problems" \
			--argjson collectionProblems "$coll" --argjson omissions "$omit" --arg stack "$stack" \
			'. + [{dir:$dir, present:$present, why:(if $why == "" then null else $why end), revision:$revision, chromium:$chromium, environmentProblems:$problems, collectionProblems:$collectionProblems, omissions:$omissions, stack:$stack}]' <<<"$dirjson")
		if [[ $present == true ]]; then
			# a directory of another revision, with an environment problem, or with a collection/execution failure contributes runs that are listed but not counted
			all=$(jq -c --argjson runs "$runs" --argjson problems "$problems" --argjson revision "$envrev" --arg rev "$rev" --argjson coll "$coll" '
				. + [ $runs[] | if ($revision != $rev) then (.problems += ["the evidence is for revision \($revision), not \($rev)"] | .counted = false)
				      elif ($problems | length) > 0 then (.problems += ["the evidence directory has an environment problem"] | .counted = false)
				      elif ($coll | length) > 0 then (.problems += $coll | .counted = false)
				      else . end ]' <<<"$all")
		fi
	done
	jq -n --arg generatedAt "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg revision "$rev" --argjson target "$TARGET_MS" --argjson need "$REQUIRED_PRESSES" --argjson runsNeeded "$REQUIRED_RUNS" \
		--argjson reasons "$REVIEW_REASONS" --argjson backgrounds "$BACKGROUNDS" --argjson original "$ORIGINAL" --argjson dirs "$dirjson" --argjson runs "$all" "$JQ_DEFS"'
		def pooled($counted):
		  ([ $counted[] | .presses[] ]) as $all
		  | ([ $counted[] | .presses[1:][] ]) as $rest
		  | { presses: ($all | stats),
		      afterFirstPress: ($rest | stats),
		      firstPressOfEachRun: ([ $counted[] | .firstPressMs | select(type == "number") ] | stats),
		      overTarget: ($all | map(select(. > $target)) | length) };
		($runs | mark_duplicates) as $runs
		| ( [ $backgrounds[] as $bg
		    | ([ $runs[] | select(.background == $bg and .reasons == $reasons) ]) as $mine
		    | ($mine | map(select(.counted))) as $counted
		    | (if ($counted | length) == 0 then null else pooled($counted) end) as $pool
		    | { background: $bg, reasons: $reasons,
		        runsFound: ($mine | length), runsCounted: ($counted | length),
		        complete: (($counted | length) >= $runsNeeded),
		        gaps: [ (if ($mine | length) == 0 then "no sampling run for a \($reasons)-reason review over a \($bg)-row background" else empty end),
		                (if ($mine | length) > 0 and ($counted | length) < $runsNeeded then "\($counted | length) counted runs of the \($runsNeeded) needed for the \($bg)-row background" else empty end),
		                ($mine[] | select(.counted | not) | "\(.case) in \(.dir) not counted: \(.problems | join("; "))") ],
		        pooled: $pool,
		        perRun: ($mine | map({case, dir, stack, run, counted, problems, stats, overTarget, firstPressMs, afterFirst, byOrigin, slowest, load, chromium: .browser, judgeBudgets: .budgets})),
		        p95OverTarget: (if $pool == null then null else ($pool.presses.p95Ms > $target) end)
		      } ] ) as $configs
		| { record: "next-changed-reason-samples", issue: 298, generatedAt: $generatedAt, revision: $revision,
		    target: {ms: $target, kind: "the proposed navigation budget of the scaling design (QC1_NAV_BUDGET_MS), an engineering proposal and not an owner decision"},
		    required: {pressesPerRun: $need, runsPerConfiguration: $runsNeeded, reasons: $reasons, backgrounds: $backgrounds},
		    originalObservations: $original,
		    evidenceDirectories: $dirs,
		    configurations: $configs,
		    complete: ($configs | all(.complete)),
		    gaps: [ $configs[] | .gaps[] ] + [ $dirs[] | select(.present | not) | "\(.dir): \(.why)" ]
		          + [ $dirs[] | . as $dd | (.collectionProblems // [])[] | "\($dd.dir): \(.)" ],
		    consequence: (if ($configs | any(.p95OverTarget == true))
		                  then "a pooled p95 is over the target in " + ([ $configs[] | select(.p95OverTarget == true) | "\(.background)-row background" ] | join(", ")) + ": profile the control (repeat the sampling with --qc1-arg --profile-next-changed) before any change"
		                  else null end),
		    interpretation: "none. This record states the numbers and their load context. It does not call a delay noise, does not call the target met and does not choose a fix; a pooled p95 at or under the target on a COMPLETE record is evidence for the reviewer, not a verdict."
		  }'
}

# This script's own checks, against stub evidence written here (never against a measurement): the figures are computed by hand from stub press times, so each assertion
# is a fact about THIS script's arithmetic and bookkeeping, not about the product. The stubs also prove the refusals: a run that is short, failed, partial, profiled, for
# another revision, under a broken environment or without load counters is listed and never counted, and an absent configuration has no figures at all. Further stubs
# prove the R1-R3 rules: judge-invalid rows (scheduler overlap, incomplete page accounting, a browser/server digest disagreement) are excluded while a budget finding
# stays beside its counted row, and the directory's own duplicate-row findings are honored beside the recompute; collection and execution failures (a collection
# marker, an abort, an incomplete environment, a malformed status, an unreadable or malformed outer record, a nonzero-exit or unexplained outer qc1 failure)
# exclude the directory while expected sampling-only omissions (other missing rows, legacy pending cases, pilot coverage) stay COMPLETE — including one exit-0
# outer failure explained entirely by those omissions, proven by the step summary agreeing with the validated status; repeated directory arguments (alias paths
# included) are refused; an identical copied directory is counted once and named; a repeated or conflicting identity within one file, or conflicting records of
# one identity across directories of one stack, excludes every occurrence, identically in either row or directory order. Still further stubs prove the R3/R5/R6/R7
# rules: equal-timed copies that differ in any recorded validity fact (status, window, agreement, sampling, page accounting,
# digest, first page or another judge input) conflict instead of collapsing; a present outer record must be a finished run bound
# to this directory and revision with one well-formed qc1 step whose JSON-integer exit matches its verdict and whose summary has
# the runner's shape and agrees with the verdict and the directory's status; sampling counts, indices, origins, timings
# and load counters are validated against the producer's record shape, and a malformed nested press never aborts the report;
# and a standard-shaped missing sampling entry (no reasons key) fails like any other.
# shellcheck disable=SC2016  # the jq filters passed to expect are not shell expansions
next_changed_self_test() {
	local work rev other a b c d1 d2 d3 f rc n=0
	work=$(mktemp -d "${TMPDIR:-/tmp}/fold-next-changed-test.XXXXXX")
	# shellcheck disable=SC2064
	trap "rm -rf -- '$work'" EXIT
	rev=$(printf 'c%.0s' {1..40})
	other=$(printf 'd%.0s' {1..40})
	# shellcheck disable=SC2059  # the format is the one character to repeat
	hex64() { printf "${1}%.0s" {1..64}; }

	ok() {
		n=$((n + 1))
	}
	fail() {
		echo "reason-review-next-changed self-test: FAILED: $*" >&2
		exit 1
	}
	expect() { # description jq-filter file  (the filter must print true)
		[[ $(jq -r "$2" "$3") == true ]] || fail "$1 (filter: $2)"
		ok "$1"
	}

	# An environment record that qc1_environment_problems finds nothing wrong with, for revision $1.
	stub_env() { # revision [nextChangedOnly] [limitsApplied] [tag]
		local img pg cid sha
		# tag picks the container identity: two stacks with different tags pool (independent stacks), the same tag twice is one stack seen twice (a copy).
		img="sha256:$(hex64 a)" pg="sha256:$(hex64 b)" cid=$(hex64 "${4:-${STUB_TAG:-e}}") sha=$(hex64 f)
		jq -nc --arg rev "$1" --arg img "$img" --arg pg "$pg" --arg cid "$cid" --arg sha "$sha" --argjson only "${2:-true}" --argjson limits "${3:-true}" \
			--argjson sources "$(printf '%s\n' "${QC1_SOURCE_FILES[@]}" | jq -R . | jq -sc .)" '
			{revision:$rev, imageId:$img, appImageId:$img, postgresImageId:$pg, dbImageId:$pg, appContainers:[{id:$cid, imageId:$img}], limitsApplied:$limits,
			 appContainerId:$cid, dbContainerId:$cid, postgresVersion:"16.4", sources:($sources | map({path:., sha256:$sha, matches:true})),
			 daemon:{rootless:true, sharedDaemon:false}, settings:{workers:0, autovacuum:"off", browser:true, nextChangedOnly:$only},
			 calibration:{gate:{ok:true}, dbMemory:{ok:true, samples:5}, appMemory:{ok:true, hwmReset:true}, wal:{ok:true}, commit:{ok:true}},
			 browserRuntime:{ok:true, chromium:"130.0", playwright:"1.50"}}'
	}
	# One sampling row. ms is a space-separated list of press times; patch is a jq expression applied to the row (default: none).
	stub_row() { # case background reasons ms [patch]
		local c=$1 bg=$2 reasons=$3 ms=$4
		jq -nc --arg case "$c" --argjson bg "$bg" --argjson reasons "$reasons" --arg ms "$ms" '
			($ms | split(" ") | map(tonumber)) as $v
			| {status:"measured", item:10, case:$case, size:$bg, reasons:$reasons,
			   window:{walBytes:1000, appPeakKb:100, dbPeakKb:100, gateTicks:100, commitCalls:5, commitMsMax:5, commitMsTotal:20, appHwmReset:true, dbMemSamples:5, gateMaxGapMs:10,
			     auditRowsBefore:0, auditRowsAfter:10, auditBytesBefore:0, auditBytesAfter:100, topStatements:[], tableRowsBefore:($bg + $reasons), tableRowsAfter:($bg + $reasons),
			     auditActionsBefore:0, auditActionsAfter:10, auditActionsDelta:10, schedulerOverlap:false, schedulerRows:0},
			   browser:{failed:null, apiOrigin:"real", walkTotal:$reasons, pageRequests:1000, pageResponseBytes:10000000,
			     press:{observed:true, status:"409", bodyBytes:200, responseMs:5}, reload:{unchosenAfterReload:1},
			     secureContext:true, renderReadyMs:44000, walkEndMs:43000,
			     firstPage:{itemsRendered:(if $reasons < 50 then $reasons else 50 end), firstInteractiveMs:42000},
			     clicks:{count:30, p50Ms:5, p95Ms:8, maxMs:12},
			     navigation:{nextPage:{maxMs:5}, jump:{maxMs:5}, nextUnchosen:{maxMs:5}, nextChanged:{maxMs:5}},
			     pages:{count:2000, responseBytesMax:5000, requestMsMax:8000, readMsMax:7000, parseMsMax:600, parseMsTotal:600000, incomplete:0,
			       absent:{bytes:0, requestMs:0, readMs:0, parseMs:0, nodeParseMs:0},
			       walks:[{walk:1, started:1000, completed:1000, incomplete:0}, {walk:2, started:1000, completed:1000, incomplete:0}]},
			     digest:{path:"webcrypto", buildMs:5, preimageBytes:(35 + 40 * $reasons)},
			     phases:{walk:{walBytes:100, auditActionsDelta:0, tableRows:{before:($bg + $reasons)}, auditBytes:50},
			       interactions:{walBytes:100, auditActionsDelta:0, tableRows:{before:($bg + $reasons)}, auditBytes:50},
			       press:{walBytes:100, auditActionsDelta:0, tableRows:{before:($bg + $reasons)}, auditBytes:50}},
			     heapAfterGcBytes:1000000, domNodes:5000, longTaskMs:3,
			     nextChangedSampling:{requested:($v | length), performed:($v | length), firstPressMs:$v[0], profile:null,
			       presses:[range(0; $v | length) as $i | {index:$i, origin:(["first-page","after-changed-page","last-page"][$i % 3]), ms:$v[$i], atMs:($i * 100), longTaskMs:0, longTaskCount:0}]},
			     load:{presses:{seconds:10, stealPercent:0.1, load1Start:0.5, load1End:0.6}},
			     browser:"chromium 130.0"}}
			' | jq "${5:-.}"
	}
	seq_ms() { seq -s ' ' "$1" "$2"; } # 1 2 ... 30
	stub_status() { # a clean tool verdict for stub evidence: complete with nothing pending, pilot coverage by construction
		jq -n -c '{complete:true, missing:[], pending:[], invalid:[], budgetFailures:[], coverage:{kind:"pilot", reasons:["stub evidence for the report self-test"]}, releaseEvidence:false}'
	}
	cell() { # dir revision rows-file-lines... : writes environment.json, measurements.jsonl and a clean evidence-status.json
		local dir=$1 revision=$2
		shift 2
		mkdir -p "$dir"
		stub_env "$revision" >"$dir/environment.json"
		printf '%s\n' "$@" >"$dir/measurements.jsonl"
		stub_status >"$dir/evidence-status.json"
	}
	run() { # out-var extra-args... ; runs the script as a user does
		bash "${BASH_SOURCE[0]}" "$@"
	}

	# --- 1. a complete record: 3 runs of 30 presses (1..30 ms) over a 100,000-row background, 3 over 1,000,000 (the last run's 30th press is 420 ms) -------------------
	a=$work/complete
	cell "$a" "$rev" \
		"$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc1 1000000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 1000000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 1000000 100000 "$(seq_ms 1 29) 420")"
	rc=0
	run --out "$work/complete.json" --evidence "$a" --revision "$rev" >"$work/complete.out" 2>&1 || rc=$?
	[[ $rc == 0 ]] || { cat "$work/complete.out" >&2; fail "a complete record must exit 0, exited $rc"; }
	f=$work/complete.json
	expect "complete record is complete" '.complete == true and (.gaps | length) == 0' "$f"
	expect "30 presses x 3 runs pooled to 90 presses per configuration" '[.configurations[].pooled.presses.count] == [90, 90]' "$f"
	# 1..30 three times: nearest-rank p50 is rank 45 -> 15, p95 is rank 86 -> 29, max 30
	expect "pooled p50/p95/max of three 1..30 runs are 15/29/30" '.configurations[0].pooled.presses | [.p50Ms, .p95Ms, .maxMs] == [15, 29, 30]' "$f"
	# the other background: the 30th press of run 3 is 420, so the max is 420 and one press is over 300 ms, but p95 stays 29
	expect "a single 420 ms press moves the max, counts once over the target, and leaves p95 at 29" '.configurations[1].pooled | (.presses | [.p50Ms, .p95Ms, .maxMs]) == [15, 29, 420] and .overTarget == 1' "$f"
	expect "p95 at or under the target is not over it" '[.configurations[].p95OverTarget] == [false, false] and .consequence == null' "$f"
	expect "the first press of each run is kept apart" '.configurations[0].pooled.firstPressOfEachRun | .count == 3 and .maxMs == 1' "$f"
	expect "presses after the first exclude it (29 per run)" '.configurations[0].pooled.afterFirstPress.count == 87 and .configurations[0].pooled.afterFirstPress.p50Ms == 16' "$f"
	expect "per-run figures and the slowest presses are kept with their origin" '.configurations[1].perRun[2] | .stats.maxMs == 420 and .slowest[0].ms == 420 and (.slowest[0].origin | type) == "string" and (.byOrigin | keys | length) == 3' "$f"
	expect "the original observations are quoted and are not samples" '(.originalObservations | map(.ms)) == [830.7, 65.1, 60.6] and ([.configurations[].pooled.presses.count] | add) == 180' "$f"
	expect "the record decides nothing" '.interpretation | startswith("none.")' "$f"
	expect "the target is the harness budget, labelled a proposal" '.target.ms == 300 and (.target.kind | contains("proposed"))' "$f"

	# --- 2. a pooled p95 over the target names its consequence and nothing more ----------------------------------------------------------------------------------------
	b=$work/slow
	cell "$b" "$rev" \
		"$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 301 330)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 301 330)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 301 330)")" \
		"$(stub_row browser-panel-100000-nc1 1000000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 1000000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 1000000 100000 "$(seq_ms 1 30)")"
	run --out "$work/slow.json" --evidence "$b" --revision "$rev" >/dev/null 2>&1 || fail "a complete record with slow presses is still a complete record"
	expect "p95 over the target is reported per configuration" '[.configurations[].p95OverTarget] == [true, false] and .configurations[0].pooled.overTarget == 90' "$work/slow.json"
	expect "the consequence is a profile, naming the configuration" '.consequence | contains("100000-row background") and contains("profile") and (contains("1000000-row") | not)' "$work/slow.json"

	# --- 3. nothing measured: no run, no figures, incomplete (never zeros) ----------------------------------------------------------------------------------------------
	c=$work/empty
	mkdir -p "$c"
	rc=0
	run --out "$work/empty.json" --evidence "$c" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an empty evidence directory must exit 10, exited $rc"
	expect "an empty directory has no figures and says why" '.complete == false and ([.configurations[].pooled] == [null, null]) and ([.configurations[].runsFound] == [0, 0]) and (.gaps | map(contains("missing or unreadable")) | any)' "$work/empty.json"
	expect "an absent configuration is named in the gaps" '.gaps | map(contains("no sampling run for a 100000-reason review over a 1000000-row background")) | any' "$work/empty.json"

	# --- 4. one configuration only; the other has no figures ------------------------------------------------------------------------------------------------------------
	d1=$work/half
	cell "$d1" "$rev" \
		"$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/half.json" --evidence "$d1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "one configuration missing must exit 10, exited $rc"
	expect "the sampled configuration has figures and the missing one has none" '.configurations[0].complete == true and .configurations[1].pooled == null and .complete == false' "$work/half.json"

	# --- 5. rows that do not count, each for its own reason --------------------------------------------------------------------------------------------------------------
	d2=$work/bad
	cell "$d2" "$rev" \
		"$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 29)")" \
		"$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)" '.status = "partial"')" \
		"$(stub_row browser-panel-100000-nc4 100000 100000 "$(seq_ms 1 30)" '.browser.failed = {phase:"next-changed-presses", error:"x"}')" \
		"$(stub_row browser-panel-100000-nc5 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.profile = {requested:true, file:"p.cpuprofile", error:null}')" \
		"$(stub_row browser-panel-100000-nc6 100000 100000 "$(seq_ms 1 30)" 'del(.browser.load)')" \
		"$(stub_row browser-panel-100000-nc7 100000 100000 "$(seq_ms 1 30)" '.browser.load.presses.stealPercent = null')" \
		"$(stub_row browser-panel-100000-nc8 100000 100000 "$(seq_ms 1 30)" '.browser.walkTotal = 99999')" \
		"$(stub_row browser-panel-100000-nc9 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.performed = 29')" \
		"$(stub_row browser-panel-100000-nc10 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling = null')" \
		"$(stub_row browser-panel-100000-nc11 100000 100000 "$(seq_ms 1 30)" '.browser.apiOrigin = "other"')" \
		"$(stub_row browser-panel-100000-nc12 100000 100000 "$(seq_ms 1 30)" '.browser.press.status = "200"')"
	rc=0
	run --out "$work/bad.json" --evidence "$d2" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "uncounted runs must leave the record incomplete (exit 10), exited $rc"
	f=$work/bad.json
	expect "only the clean run is counted among twelve" '.configurations[0] | .runsFound == 12 and .runsCounted == 1 and .complete == false' "$f"
	expect "the short run (29 presses) is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc2") and contains("fewer than 30")) | any' "$f"
	expect "a partial row, a failed run, a profiled run, a run without load, a short walk, a stopped run and a foreign origin are each named" \
		'.configurations[0].gaps as $g | ["nc3","nc4","nc5","nc6","nc7","nc8","nc9","nc10","nc11","nc12"] | all(. as $c | $g | map(contains("browser-panel-100000-" + $c + " ")) | any)' "$f"
	expect "the profiled run is called a diagnostic" '.configurations[0].gaps | map(contains("profiled") and contains("diagnostic")) | any' "$f"
	expect "a counted run still reports the single clean figures only" '.configurations[0].pooled.presses | .count == 30 and .p95Ms == 29' "$f"

	# --- 6. pooling across independent stacks (different container identities pool; section 12 proves one stack counted twice does not) -----------------------
	d3=$work/stack-b
	cell "$work/stack-a" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc1 1000000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 1000000 100000 "$(seq_ms 1 30)")"
	STUB_TAG=f cell "$d3" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc1 1000000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/pool.json" --evidence "$work/stack-a" --evidence "$d3" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 0 ]] || fail "two stacks of 2 and 1 runs must pool to 3 and exit 0, exited $rc"
	expect "runs from two stacks are pooled per background" '[.configurations[] | .runsCounted] == [3, 3] and (.evidenceDirectories | length) == 2' "$work/pool.json"

	# --- 7. identity: another revision and a broken environment are not evidence -----------------------------------------------------------------------------------------
	cell "$work/other-rev" "$other" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")"
	run --out "$work/other.json" --evidence "$work/other-rev" --revision "$rev" >/dev/null 2>&1 || true
	expect "evidence for another revision counts for nothing" '.configurations[0] | .runsFound == 1 and .runsCounted == 0 and .pooled == null' "$work/other.json"
	cell "$work/env-bad" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")"
	stub_env "$rev" true false >"$work/env-bad/environment.json"
	run --out "$work/envbad.json" --evidence "$work/env-bad" --revision "$rev" >/dev/null 2>&1 || true
	expect "an environment problem is named and its runs are not counted" '.configurations[0].runsCounted == 0 and (.evidenceDirectories[0].environmentProblems | length) > 0' "$work/envbad.json"
	cell "$work/not-sampling" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")"
	stub_env "$rev" false >"$work/not-sampling/environment.json"
	run --out "$work/ns.json" --evidence "$work/not-sampling" --revision "$rev" >/dev/null 2>&1 || true
	expect "a directory that did not run in sampling mode is named" '.evidenceDirectories[0].environmentProblems | map(contains("--next-changed-only")) | any' "$work/ns.json"

	# --- 8. refusals before anything is written ----------------------------------------------------------------------------------------------------------------------------
	rc=0
	run --out "$work/complete.json" --evidence "$a" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 ]] || fail "an existing record must be refused with exit 2, exited $rc"
	rc=0
	run --evidence "$a" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 ]] || fail "no --out must be refused with exit 2, exited $rc"
	rc=0
	run --out "$work/x.json" --evidence "$a" --revision short >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 && ! -e $work/x.json ]] || fail "a short revision must be refused with exit 2 and write nothing"
	rc=0
	run --out "$work/y.json" --evidence "$root/scripts" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 && ! -e $work/y.json ]] || fail "evidence inside the checkout must be refused with exit 2"

	# --- 9. the dry run names the commands the runner uses ----------------------------------------------------------------------------------------------------------------
	run --dry-run >"$work/dry.out" 2>&1 || fail "--dry-run failed"
	for want in '--next-changed-only' '--next-changed-presses --qc1-arg 30' '--next-changed-runs --qc1-arg 3' '--sizes --qc1-arg 100000,1000000'; do
		grep -q -- "$want" "$work/dry.out" || fail "the dry run does not name '$want'"
	done
	ok "dry run"

	# --- 10. the recomputed QC judge excludes invalid rows and keeps budgets beside the row --------------------------------------------------------------
	e=$work/judged
	cell "$e" "$rev" \
		"$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)" '.window.schedulerOverlap = true')" \
		"$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)" '.browser.pages.walks[0].incomplete = 1')" \
		"$(stub_row browser-panel-100000-nc4 100000 100000 "$(seq_ms 1 30)" '.agreement = {digest:{checked:true, equal:false}}')" \
		"$(stub_row browser-panel-100000-nc5 100000 100000 "$(seq_ms 1 30)" '.browser.navigation.nextChanged.maxMs = 420')"
	rc=0
	run --out "$work/judged.json" --evidence "$e" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "judge-excluded rows must leave the record incomplete (exit 10), exited $rc"
	f=$work/judged.json
	expect "only the clean row and the budget row are counted among five" '.configurations[0] | .runsFound == 5 and .runsCounted == 2 and .complete == false' "$f"
	expect "a scheduler confound is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc2") and contains("scheduler")) | any' "$f"
	expect "incomplete page accounting is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc3") and contains("pages-incomplete")) | any' "$f"
	expect "a browser/server digest disagreement is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc4") and contains("digest")) | any' "$f"
	expect "a budget finding is kept beside its row and never excludes it" '.configurations[0].perRun[] | select(.case == "browser-panel-100000-nc5") | .counted == true and (.judgeBudgets | map(.failure) | contains(["navigation"]))' "$f"

	# --- 11. collection and execution failures exclude the directory; expected sampling-only omissions do not ------------------------------------------------
	ej=$work/collfail
	cp -r "$a" "$ej"
	printf '%s\n' '/work/out (the run evidence) could not be copied out' 'the run inside could not be confirmed stopped before the copy' >"$ej/COLLECTION-FAILED.txt"
	rc=0
	run --out "$work/collfail.json" --evidence "$ej" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a collection marker must leave the record incomplete (exit 10), exited $rc"
	expect "the collection failure is named and nothing is counted" '.complete == false and (.gaps | map(contains("COLLECTION-FAILED")) | any) and ([.configurations[].runsCounted] == [0, 0])' "$work/collfail.json"
	ek=$work/aborted
	cp -r "$a" "$ek"
	jq -nc '{complete:false, missing:[], pending:[{item:4, size:100000, case:"legacy-discovery-10", reason:"T07B-3a"}], invalid:[], budgetFailures:[], coverage:{kind:"pilot", reasons:["the run ended early"]}, releaseEvidence:false, aborted:{outcome:"aborted", exitCode:143, signal:"TERM", step:"qc1", sizeInProgress:100000}}' >"$ek/evidence-status.json"
	jq -nc '{outcome:"aborted", exitCode:143, signal:"TERM", step:"qc1", sizeInProgress:100000}' >"$ek/abort.json"
	rc=0
	run --out "$work/aborted.json" --evidence "$ek" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an aborted finalized directory must leave the record incomplete (exit 10), exited $rc"
	expect "the abort is named and nothing is counted" '.complete == false and (.gaps | map(contains("abort")) | any) and ([.configurations[].runsCounted] == [0, 0])' "$work/aborted.json"
	el=$work/envincomplete
	cp -r "$a" "$el"
	jq '.incomplete = true | .incompleteReason = "the run ended before it finished"' "$el/environment.json" >"$el/environment.json.tmp" && mv "$el/environment.json.tmp" "$el/environment.json"
	rc=0
	run --out "$work/envincomplete.json" --evidence "$el" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an incomplete environment must leave the record incomplete (exit 10), exited $rc"
	expect "the incomplete environment is named" '.complete == false and (.gaps | map(contains("incomplete")) | any)' "$work/envincomplete.json"
	em=$work/outer-fail
	mkdir -p "$em"
	cp -r "$a" "$em/qc1"
	jq -nc --arg rev "$rev" --arg q "$em/qc1" '{schema:"fold/reason-review-scale/v1", issue:297, revision:$rev, startedAt:"t", finishedAt:"t", host:"h", requested:{steps:["qc1"]}, coverage:{kind:"partial", reasons:["QC-1 exited 3"]}, tools:[], result:"fail", steps:[{name:"qc1", command:"qc1-measure.sh", startedAt:"t", finishedAt:"t", seconds:1, exit:3, verdict:"fail", log:"qc1.log", artifacts:$q, summary:{complete:false}}]}' >"$em/evidence.json"
	rc=0
	run --out "$work/outerfail.json" --evidence "$em/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an outer interruption after the rows were written must leave the record incomplete (exit 10), exited $rc"
	expect "the failed outer step is named" '.complete == false and (.gaps | map(contains("outer runner")) | any)' "$work/outerfail.json"
	# The real sampling shape: the documented --next-changed-only run leaves ordinary matrix rows missing, so the wrapper's own
	# classifier marks its qc1 step fail with exit 0. The step summary agrees with the directory's validated evidence-status.json
	# (same missing/pending counts, no abort, no invalid finding, pilot coverage), so the failure is explained entirely by the
	# sampling design omitting the full matrix and the runs count.
	en=$work/outer-fail-explained
	mkdir -p "$en"
	cp -r "$a" "$en/qc1"
	jq -nc '{complete:false, missing:[{item:3, size:100000, case:"discovery-requester-10"}, {item:10, size:100000, case:"browser-panel-100000"}], pending:[{item:4, size:100000, case:"legacy-discovery-10", reason:"T07B-3a"}], invalid:[], budgetFailures:[], coverage:{kind:"pilot", reasons:["rows of the full matrix are not measured and clean: 40"]}, releaseEvidence:false}' >"$en/qc1/evidence-status.json"
	jq -nc --arg rev "$rev" --arg q "$en/qc1" '{schema:"fold/reason-review-scale/v1", issue:297, revision:$rev, startedAt:"t", finishedAt:"t", host:"h", requested:{steps:["qc1"]}, coverage:{kind:"partial", reasons:["QC-1 is missing 2 expected measurement(s)"]}, tools:[], result:"fail", steps:[{name:"qc1", command:"qc1-measure.sh", startedAt:"t", finishedAt:"t", seconds:1, exit:0, verdict:"fail", log:"qc1.log", artifacts:$q, summary:{complete:false, coverage:"pilot", releaseEvidence:false, budgetFailures:0, invalid:0, missing:2, pending:1, aborted:false, abortedReason:"", missingCases:[{item:3, size:100000, case:"discovery-requester-10"}, {item:10, size:100000, case:"browser-panel-100000"}], pendingCases:[{item:4, size:100000, case:"legacy-discovery-10", reason:"T07B-3a"}], invalidFindings:[], coverageReasons:["rows of the full matrix are not measured and clean: 40"]}}]}' >"$en/evidence.json"
	rc=0
	run --out "$work/outerexplained.json" --evidence "$en/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 0 ]] || fail "an exit-0 outer failure explained by intentional omissions must stay COMPLETE, exited $rc"
	expect "the explained outer failure stays complete" '.complete == true' "$work/outerexplained.json"
	expect "the explained outer failure is recorded as an omission" '.evidenceDirectories[0].omissions | map(contains("exit 0") and contains("sampling design")) | any' "$work/outerexplained.json"
	eo=$work/omissions
	cp -r "$a" "$eo"
	jq -nc '{complete:false, missing:[{item:3, size:100000, case:"discovery-requester-10"}], pending:[{item:4, size:100000, case:"legacy-discovery-10", reason:"T07B-3a"}], invalid:[], budgetFailures:[], coverage:{kind:"pilot", reasons:["rows of the full matrix are not measured and clean: 40"]}, releaseEvidence:false}' >"$eo/evidence-status.json"
	rc=0
	run --out "$work/omissions.json" --evidence "$eo" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 0 ]] || fail "expected sampling-only omissions must stay COMPLETE, exited $rc"
	expect "omissions are recorded, not failures" '.complete == true and ([.evidenceDirectories[0].omissions | length] | .[0] > 0)' "$work/omissions.json"

	# --- 12. one run cannot become several: repeated arguments are refused, identical copies count once, repeated rows count nothing ---------------
	rc=0
	run --out "$work/dup.json" --evidence "$a" --evidence "$a" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 && ! -e $work/dup.json ]] || fail "a repeated evidence directory must be refused with exit 2 and write nothing, exited $rc"
	rc=0
	run --out "$work/dup2.json" --evidence "$a" --evidence "$a/." --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 && ! -e $work/dup2.json ]] || fail "a repeated evidence directory through /. must be refused with exit 2 and write nothing, exited $rc"
	ln -s "$a" "$work/alias"
	rc=0
	run --out "$work/dup3.json" --evidence "$a" --evidence "$work/alias" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 && ! -e $work/dup3.json ]] || fail "a repeated evidence directory through a symlink must be refused with exit 2 and write nothing, exited $rc"
	ep=$work/single
	cell "$ep" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc1 1000000 100000 "$(seq_ms 1 30)")"
	cp -r "$ep" "$work/single-copy"
	rc=0
	run --out "$work/dupcopy.json" --evidence "$ep" --evidence "$work/single-copy" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a copied evidence directory must not add observations (exit 10), exited $rc"
	expect "a copied directory is counted once and named" '[.configurations[] | .runsCounted] == [1, 1] and (.gaps | map(contains("duplicate run")) | any)' "$work/dupcopy.json"
	eq=$work/duprows
	cell "$eq" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/duprows.json" --evidence "$eq" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a repeated row identity must not add observations (exit 10), exited $rc"
	expect "a repeated row identity counts nothing" '.configurations[0] | .runsFound == 2 and .runsCounted == 0' "$work/duprows.json"
	expect "every occurrence of the repeated identity is named as a duplicate" '.configurations[0].gaps | map(select(contains("browser-panel-100000-nc1") and contains("duplicate run"))) | length == 2' "$work/duprows.json"

	# --- 13. conflicting duplicates exclude every occurrence, however the rows or directories are ordered ------------------------------------------------
	# Within one measurements file a fast row and a slow row of one identity are both suspect: neither counts, fast-first or slow-first.
	er=$work/conflict-fast-first
	cell "$er" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 301 330)")" \
		"$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	es=$work/conflict-slow-first
	cell "$es" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 301 330)")" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/conflict1.json" --evidence "$er" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "conflicting rows of one identity must leave the record incomplete (exit 10), exited $rc"
	expect "a fast/slow conflict counts only the other identities" '.configurations[0] | .runsFound == 4 and .runsCounted == 2' "$work/conflict1.json"
	rc=0
	run --out "$work/conflict2.json" --evidence "$es" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "conflicting rows in the reverse order must leave the record incomplete (exit 10), exited $rc"
	expect "the reverse row order counts the same runs" '[.configurations[0] | .runsFound, .runsCounted] == [4, 2]' "$work/conflict2.json"
	expect "both conflicting occurrences are named, whichever came first" '.configurations[0].gaps | map(select(contains("browser-panel-100000-nc1") and contains("suspect"))) | length == 2' "$work/conflict2.json"
	# The same conflict spread over two directories of one stack: identical copies collapse to one, conflicting records exclude all.
	et=$work/confdir-a
	eu=$work/confdir-b
	cell "$et" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	cell "$eu" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 301 330)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/confdir1.json" --evidence "$et" --evidence "$eu" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "conflicting directories of one stack must leave the record incomplete (exit 10), exited $rc"
	expect "conflicting directories keep only the identical identities" '.configurations[0] | .runsFound == 6 and .runsCounted == 2' "$work/confdir1.json"
	expect "the conflict is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc1") and contains("conflicting duplicate")) | any' "$work/confdir1.json"
	rc=0
	run --out "$work/confdir2.json" --evidence "$eu" --evidence "$et" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "conflicting directories in the reverse order must leave the record incomplete (exit 10), exited $rc"
	expect "the reverse directory order counts the same runs" '[.configurations[0] | .runsFound, .runsCounted] == [6, 2]' "$work/confdir2.json"
	jq -c '.configurations[0].pooled' "$work/confdir1.json" >"$work/confpool1.json"
	jq -c '.configurations[0].pooled' "$work/confdir2.json" >"$work/confpool2.json"
	cmp -s "$work/confpool1.json" "$work/confpool2.json" || fail "the reverse directory order must pool the identical figures"
	ok "conflicting directories pool identically in either order"
	# A duplicate-row finding in the directory's own status is honored: the named rows are excluded with the finding attached.
	ev=$work/dupfinding
	cell "$ev" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	jq -c '.invalid = [{class:"invalid", failure:"duplicate-row", item:10, size:100000, case:"browser-panel-100000-nc1", count:2}]' "$ev/evidence-status.json" >"$ev/evidence-status.json.tmp" && mv "$ev/evidence-status.json.tmp" "$ev/evidence-status.json"
	rc=0
	run --out "$work/dupfinding.json" --evidence "$ev" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a duplicate-row finding must leave the record incomplete (exit 10), exited $rc"
	expect "the finding excludes the named identity" '.configurations[0] | .runsFound == 4 and .runsCounted == 2' "$work/dupfinding.json"
	expect "the finding itself is attached to the excluded rows" '.configurations[0].gaps | map(contains("browser-panel-100000-nc1") and contains("duplicate-row")) | any' "$work/dupfinding.json"

	# --- 14. malformed finalization metadata is a failure, never a silent omission -----------------------------------------------------------------------
	ew=$work/status-empty
	cp -r "$a" "$ew"
	echo '{}' >"$ew/evidence-status.json"
	rc=0
	run --out "$work/statusempty.json" --evidence "$ew" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an empty-object status must leave the record incomplete (exit 10), exited $rc"
	expect "an empty-object status is malformed, not an omission" '.complete == false and (.gaps | map(contains("malformed") and contains("complete is not a boolean")) | any)' "$work/statusempty.json"
	ex=$work/status-types
	cp -r "$a" "$ex"
	jq -nc '{complete:false, missing:"none", pending:5, invalid:{}, budgetFailures:null, coverage:"pilot", releaseEvidence:false}' >"$ex/evidence-status.json"
	rc=0
	run --out "$work/statustypes.json" --evidence "$ex" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a wrong-typed status must leave the record incomplete (exit 10), exited $rc"
	expect "wrong-typed status collections are malformed" '.complete == false and (.gaps | map(contains("malformed") and contains("missing is not an array")) | any)' "$work/statustypes.json"
	ey=$work/outer-bad
	mkdir -p "$ey"
	cp -r "$a" "$ey/qc1"
	printf '%s' 'this is not json{{{' >"$ey/evidence.json"
	rc=0
	run --out "$work/outerbad.json" --evidence "$ey/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an unreadable outer record must leave the record incomplete (exit 10), exited $rc"
	expect "an unreadable outer record is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("unreadable")) | any)' "$work/outerbad.json"
	ez=$work/outer-nosteps
	mkdir -p "$ez"
	cp -r "$a" "$ez/qc1"
	jq -nc '{result:"fail", coverage:{kind:"partial", reasons:["x"]}}' >"$ez/evidence.json"
	rc=0
	run --out "$work/outernosteps.json" --evidence "$ez/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an outer record without a steps array must leave the record incomplete (exit 10), exited $rc"
	expect "an outer record without a steps array is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("no steps array")) | any)' "$work/outernosteps.json"

	# --- 15. the duplicate check compares validity, not only timings: equal presses with different status, window, agreement or sampling conflict ---
	outer_record() { # dir result verdict exit summary-json
		jq -nc --arg q "$1" --arg result "$2" --arg verdict "$3" --argjson exit "$4" --argjson summary "$5" --arg rev "$rev" \
			'{schema:"fold/reason-review-scale/v1", issue:297, revision:$rev, startedAt:"t", finishedAt:"t", host:"h", requested:{steps:["qc1"]}, coverage:{kind:"partial", reasons:["stub"]}, tools:[], result:$result, steps:[{name:"qc1", command:"qc1-measure.sh", startedAt:"t", finishedAt:"t", seconds:1, exit:$exit, verdict:$verdict, log:"qc1.log", artifacts:$q, summary:$summary}]}'
	}
	fa=$work/valid-a
	fb=$work/valid-b
	cell "$fa" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	cell "$fb" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)" '.window.schedulerOverlap = true')" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/valid1.json" --evidence "$fa" --evidence "$fb" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "equal-timed directories differing only in scheduler overlap must leave the record incomplete (exit 10), exited $rc"
	expect "identical timed presses with conflicting scheduler overlap keep only the identical identities" '.configurations[0] | .runsFound == 6 and .runsCounted == 2' "$work/valid1.json"
	expect "the validity conflict is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc1") and contains("conflicting duplicate")) | any' "$work/valid1.json"
	rc=0
	run --out "$work/valid2.json" --evidence "$fb" --evidence "$fa" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "the validity conflict in the reverse directory order must leave the record incomplete (exit 10), exited $rc"
	expect "the reverse directory order counts the same runs" '[.configurations[0] | .runsFound, .runsCounted] == [6, 2]' "$work/valid2.json"
	jq -c '.configurations[0].pooled' "$work/valid1.json" >"$work/validpool1.json"
	jq -c '.configurations[0].pooled' "$work/valid2.json" >"$work/validpool2.json"
	cmp -s "$work/validpool1.json" "$work/validpool2.json" || fail "the reverse directory order must pool the identical figures"
	ok "validity conflicts pool identically in either order"
	fc=$work/agree-a
	fd=$work/agree-b
	cell "$fc" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	cell "$fd" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)" '.agreement = {digest:{checked:true, equal:false}}')" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/valid3.json" --evidence "$fc" --evidence "$fd" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "equal-timed directories differing only in agreement must leave the record incomplete (exit 10), exited $rc"
	expect "a digest disagreement across copies keeps only the identical identity" '.configurations[0] | .runsFound == 4 and .runsCounted == 1' "$work/valid3.json"
	rc=0
	run --out "$work/valid4.json" --evidence "$fd" --evidence "$fc" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "the agreement conflict in the reverse directory order must leave the record incomplete (exit 10), exited $rc"
	expect "the reverse agreement order counts the same run" '[.configurations[0] | .runsFound, .runsCounted] == [4, 1]' "$work/valid4.json"
	fe=$work/status-a
	ff=$work/status-b
	cell "$fe" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	cell "$ff" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)" '.status = "partial"')" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/valid5.json" --evidence "$fe" --evidence "$ff" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "equal-timed directories differing only in row status must leave the record incomplete (exit 10), exited $rc"
	expect "a status conflict across copies keeps only the identical identity" '.configurations[0] | .runsFound == 4 and .runsCounted == 1' "$work/valid5.json"
	fg=$work/origin-a
	fh=$work/origin-b
	cell "$fg" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	cell "$fh" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[1].origin = "last-page"')" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/valid6.json" --evidence "$fg" --evidence "$fh" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "equal-timed directories differing only in a press origin must leave the record incomplete (exit 10), exited $rc"
	expect "a press-origin conflict across copies keeps only the identical identity" '.configurations[0] | .runsFound == 4 and .runsCounted == 1' "$work/valid6.json"
	fi2=$work/index-a
	fj=$work/index-b
	cell "$fi2" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	cell "$fj" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[1].index = 0')" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/valid7.json" --evidence "$fi2" --evidence "$fj" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "equal-timed directories differing only in a press index must leave the record incomplete (exit 10), exited $rc"
	expect "a press-index conflict across copies keeps only the identical identity" '.configurations[0] | .runsFound == 4 and .runsCounted == 1' "$work/valid7.json"
	# The remaining judge inputs travel in the token too: equal-timed copies that differ only in page accounting,
	# or only in another omitted validity fact, conflict instead of collapsing, in either directory order.
	fk=$work/pageacct-a
	fl=$work/pageacct-b
	cell "$fk" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	cell "$fl" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)" '.browser.pages.walks[0].incomplete = 1')" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/pageacct1.json" --evidence "$fk" --evidence "$fl" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "equal-timed directories differing only in page accounting must leave the record incomplete (exit 10), exited $rc"
	expect "an incomplete page-accounting copy keeps only the identical identity" '.configurations[0] | .runsFound == 4 and .runsCounted == 1' "$work/pageacct1.json"
	expect "the page-accounting conflict is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc1") and contains("conflicting duplicate")) | any' "$work/pageacct1.json"
	rc=0
	run --out "$work/pageacct2.json" --evidence "$fl" --evidence "$fk" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "the page-accounting conflict in the reverse directory order must leave the record incomplete (exit 10), exited $rc"
	expect "the reverse page-accounting order counts the same run" '[.configurations[0] | .runsFound, .runsCounted] == [4, 1]' "$work/pageacct2.json"
	fm=$work/firstpage-a
	fn=$work/firstpage-b
	cell "$fm" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	cell "$fn" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)" '.browser.firstPage.itemsRendered = 1')" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")"
	rc=0
	run --out "$work/firstpage1.json" --evidence "$fm" --evidence "$fn" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "equal-timed directories differing only in the first page must leave the record incomplete (exit 10), exited $rc"
	expect "a first-page conflict across copies keeps only the identical identity" '.configurations[0] | .runsFound == 4 and .runsCounted == 1' "$work/firstpage1.json"
	expect "the first-page conflict is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc1") and contains("conflicting duplicate")) | any' "$work/firstpage1.json"
	rc=0
	run --out "$work/firstpage2.json" --evidence "$fn" --evidence "$fm" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "the first-page conflict in the reverse directory order must leave the record incomplete (exit 10), exited $rc"
	expect "the reverse first-page order counts the same run" '[.configurations[0] | .runsFound, .runsCounted] == [4, 1]' "$work/firstpage2.json"

	# --- 16. a present outer record must be a finished run bound to this directory and revision, with one well-formed step whose exit matches its verdict ---
	# The record shape is the runner's own (reason-review-scale.sh write_record): the wrapper publishes result "incomplete" with no
	# steps before anything runs and again after every step, and record_step binds each step to its evidence directory in artifacts.
	ga=$work/outer-unfinished
	mkdir -p "$ga"
	cp -r "$a" "$ga/qc1"
	outer_record "$ga/qc1" incomplete fail 0 '{}' >"$ga/evidence.json"
	rc=0
	run --out "$work/outerunfinished.json" --evidence "$ga/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an unfinished outer record must leave the record incomplete (exit 10), exited $rc"
	expect "an unfinished outer record is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("unfinished")) | any)' "$work/outerunfinished.json"
	gb=$work/outer-nostep
	mkdir -p "$gb"
	cp -r "$a" "$gb/qc1"
	outer_record "$gb/qc1" fail fail 0 '{}' >"$gb/tmp.json"
	jq -c '.steps = []' "$gb/tmp.json" >"$gb/evidence.json"
	rc=0
	run --out "$work/outernostep.json" --evidence "$gb/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an outer record with no bound qc1 step must leave the record incomplete (exit 10), exited $rc"
	expect "an outer record with an empty step list is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("no qc1 step bound")) | any)' "$work/outernostep.json"
	gc=$work/outer-malformed
	mkdir -p "$gc"
	cp -r "$a" "$gc/qc1"
	jq -nc --arg q "$gc/qc1" --arg rev "$rev" '{schema:"fold/reason-review-scale/v1", revision:$rev, result:"fail", steps:[{name:"qc1", artifacts:$q}]}' >"$gc/evidence.json"
	rc=0
	run --out "$work/outermalformed.json" --evidence "$gc/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a qc1 step without verdict, exit and summary must leave the record incomplete (exit 10), exited $rc"
	expect "a malformed qc1 step is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("malformed") and contains("no recognized verdict")) | any)' "$work/outermalformed.json"
	gd=$work/outer-weird
	mkdir -p "$gd"
	cp -r "$a" "$gd/qc1"
	outer_record "$gd/qc1" fail weird 0 '{}' >"$gd/evidence.json"
	rc=0
	run --out "$work/outerweird.json" --evidence "$gd/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an unrecognized step verdict must leave the record incomplete (exit 10), exited $rc"
	expect "an unrecognized step verdict is malformed" '.complete == false and (.gaps | map(contains("outer runner") and contains("malformed")) | any)' "$work/outerweird.json"
	ge=$work/outer-passbad
	mkdir -p "$ge"
	cp -r "$a" "$ge/qc1"
	outer_record "$ge/qc1" fail pass 3 '{}' >"$ge/evidence.json"
	rc=0
	run --out "$work/outerpassbad.json" --evidence "$ge/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a passing step with a nonzero exit must leave the record incomplete (exit 10), exited $rc"
	expect "a pass with a contradictory exit is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("contradictory exit 3")) | any)' "$work/outerpassbad.json"
	gf=$work/outer-partialbad
	mkdir -p "$gf"
	cp -r "$a" "$gf/qc1"
	outer_record "$gf/qc1" fail partial 3 '{}' >"$gf/evidence.json"
	rc=0
	run --out "$work/outerpartialbad.json" --evidence "$gf/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a partial step with exit 3 must leave the record incomplete (exit 10), exited $rc"
	expect "a partial step with a bad exit is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("partial with exit 3")) | any)' "$work/outerpartialbad.json"
	gg=$work/outer-dupstep
	mkdir -p "$gg"
	cp -r "$a" "$gg/qc1"
	outer_record "$gg/qc1" fail fail 3 '{}' >"$gg/tmp.json"
	jq -c --argjson again "$(outer_record "$gg/qc1" fail fail 3 '{}' | jq -c '.steps[0]')" '.steps += [$again]' "$gg/tmp.json" >"$gg/evidence.json"
	rc=0
	run --out "$work/outerduplicate.json" --evidence "$gg/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "duplicate bound qc1 steps must leave the record incomplete (exit 10), exited $rc"
	expect "duplicate bound qc1 steps are named" '.complete == false and (.gaps | map(contains("outer runner") and contains("2 qc1 steps bound")) | any)' "$work/outerduplicate.json"
	gh=$work/outer-elsewhere
	mkdir -p "$gh"
	cp -r "$a" "$gh/qc1"
	outer_record "$gh/other" fail fail 3 '{}' >"$gh/evidence.json"
	rc=0
	run --out "$work/outerelsewhere.json" --evidence "$gh/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a step bound elsewhere must leave the record incomplete (exit 10), exited $rc"
	expect "a step bound to another directory is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("no qc1 step bound")) | any)' "$work/outerelsewhere.json"
	gi=$work/outer-rev
	mkdir -p "$gi"
	cp -r "$a" "$gi/qc1"
	outer_record "$gi/qc1" fail fail 3 '{}' | jq -c --arg other "$other" '.revision = $other' >"$gi/evidence.json"
	rc=0
	run --out "$work/outerrev.json" --evidence "$gi/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an outer record for another revision must leave the record incomplete (exit 10), exited $rc"
	expect "an outer record for another revision is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("is for revision")) | any)' "$work/outerrev.json"
	gj=$work/outer-pass
	mkdir -p "$gj"
	cp -r "$a" "$gj/qc1"
	jq -nc '{complete:true, missing:[], pending:[], invalid:[], budgetFailures:[], coverage:{kind:"full", reasons:[]}, releaseEvidence:true}' >"$gj/qc1/evidence-status.json"
	outer_record "$gj/qc1" pass pass 0 '{"complete":true, "coverage":"full", "releaseEvidence":true, "budgetFailures":0, "invalid":0, "missing":0, "pending":0, "aborted":false, "abortedReason":"", "missingCases":[], "pendingCases":[], "invalidFindings":[], "coverageReasons":[]}' >"$gj/evidence.json"
	rc=0
	run --out "$work/outerpass.json" --evidence "$gj/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 0 ]] || fail "a bound passing step with exit 0 must stay COMPLETE, exited $rc"
	expect "a bound passing step stays complete" '.complete == true' "$work/outerpass.json"
	expect "a bound passing step marks no outer failure" '.evidenceDirectories[0].collectionProblems == []' "$work/outerpass.json"
	gk=$work/outer-partial
	mkdir -p "$gk"
	cp -r "$a" "$gk/qc1"
	outer_record "$gk/qc1" partial partial 10 '{"complete":true, "coverage":"pilot", "releaseEvidence":false, "budgetFailures":0, "invalid":0, "missing":0, "pending":0, "aborted":false, "abortedReason":"", "missingCases":[], "pendingCases":[], "invalidFindings":[], "coverageReasons":[]}' >"$gk/evidence.json"
	rc=0
	run --out "$work/outerpartial.json" --evidence "$gk/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 0 ]] || fail "a bound partial step with exit 10 must stay COMPLETE, exited $rc"
	expect "a bound partial step with exit 10 stays complete" '.complete == true' "$work/outerpartial.json"
	expect "a bound partial step marks no outer failure" '.evidenceDirectories[0].collectionProblems == []' "$work/outerpartial.json"
	# A string exit, an empty or contradictory summary, and a summary that does not say what the directory's
	# validated status says are malformed steps, never counted runs: the exit must be a JSON integer, and a pass
	# or partial summary must have the runner's shape, agree with the verdict and match this directory.
	gse=$work/outer-stringexit
	mkdir -p "$gse"
	cp -r "$a" "$gse/qc1"
	outer_record "$gse/qc1" fail pass 0 '{}' >"$gse/tmp.json"
	jq -c '.steps[0].exit = "0"' "$gse/tmp.json" >"$gse/evidence.json"
	rc=0
	run --out "$work/outerstringexit.json" --evidence "$gse/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a qc1 step with a string exit must leave the record incomplete (exit 10), exited $rc"
	expect "a string exit is a malformed step" '.complete == false and (.gaps | map(contains("outer runner") and contains("malformed") and contains("exit \"0\"")) | any)' "$work/outerstringexit.json"
	gee=$work/outer-emptysummary
	mkdir -p "$gee"
	cp -r "$a" "$gee/qc1"
	outer_record "$gee/qc1" fail pass 0 '{}' >"$gee/evidence.json"
	rc=0
	run --out "$work/outeremptysummary.json" --evidence "$gee/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a qc1 step with an empty summary must leave the record incomplete (exit 10), exited $rc"
	expect "an empty summary contradicts its verdict" '.complete == false and (.gaps | map(contains("outer runner") and contains("contradicting that verdict")) | any)' "$work/outeremptysummary.json"
	gcf=$work/outer-collectfail
	mkdir -p "$gcf"
	cp -r "$a" "$gcf/qc1"
	outer_record "$gcf/qc1" fail pass 0 '{"complete":true, "coverage":"full", "releaseEvidence":true, "budgetFailures":0, "invalid":0, "missing":0, "pending":0, "aborted":false, "abortedReason":"", "collectionFailed":true, "collectionFailures":["the run inside could not be confirmed stopped"], "missingCases":[], "pendingCases":[], "invalidFindings":[], "coverageReasons":[]}' >"$gcf/evidence.json"
	rc=0
	run --out "$work/outercollectfail.json" --evidence "$gcf/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a passing summary recording a collection failure must leave the record incomplete (exit 10), exited $rc"
	expect "a collection failure contradicts a pass" '.complete == false and (.gaps | map(contains("outer runner") and contains("contradicting that verdict")) | any)' "$work/outercollectfail.json"
	gab=$work/outer-abortedpartial
	mkdir -p "$gab"
	cp -r "$a" "$gab/qc1"
	outer_record "$gab/qc1" fail partial 10 '{"complete":false, "coverage":"pilot", "releaseEvidence":false, "budgetFailures":0, "invalid":0, "missing":0, "pending":0, "aborted":true, "abortedReason":"TERM", "missingCases":[], "pendingCases":[], "invalidFindings":[], "coverageReasons":[]}' >"$gab/evidence.json"
	rc=0
	run --out "$work/outerabortedpartial.json" --evidence "$gab/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a partial summary recording an abort must leave the record incomplete (exit 10), exited $rc"
	expect "an abort contradicts a partial" '.complete == false and (.gaps | map(contains("outer runner") and contains("contradicting that verdict")) | any)' "$work/outerabortedpartial.json"
	gmm=$work/outer-mismatch
	mkdir -p "$gmm"
	cp -r "$a" "$gmm/qc1"
	outer_record "$gmm/qc1" fail pass 0 '{"complete":true, "coverage":"full", "releaseEvidence":true, "budgetFailures":0, "invalid":0, "missing":0, "pending":0, "aborted":false, "abortedReason":"", "missingCases":[], "pendingCases":[], "invalidFindings":[], "coverageReasons":[]}' >"$gmm/evidence.json"
	rc=0
	run --out "$work/outermismatch.json" --evidence "$gmm/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a passing summary that does not match this directory must leave the record incomplete (exit 10), exited $rc"
	expect "a summary that does not match this directory is named" '.complete == false and (.gaps | map(contains("outer runner") and contains("does not match")) | any' "$work/outermismatch.json"

	# --- 17. the sampling record's counts, indices, origins, timings and load counters are validated against the producer's shape ---
	# The producer (deploy/digitalocean/qc1/panel-core.mjs) writes requested as the asked loop bound, performed as presses.length,
	# each press as {index, origin, ms, atMs, ...} with sequential indices from first-page, after-changed-page and last-page, and
	# firstPressMs as presses[0].ms; a counter that could not be read stays absent or null, never zero.
	gl=$work/counts
	cell "$gl" "$rev" \
		"$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.requested = 1 | .browser.nextChangedSampling.performed = 1')" \
		"$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[5].ms = -5')" \
		"$(stub_row browser-panel-100000-nc4 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[5].ms = "5"')" \
		"$(stub_row browser-panel-100000-nc5 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[1].index = 0')" \
		"$(stub_row browser-panel-100000-nc6 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[1].origin = "elsewhere"')" \
		"$(stub_row browser-panel-100000-nc7 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.firstPressMs = 999')" \
		"$(stub_row browser-panel-100000-nc8 100000 100000 "$(seq_ms 1 30)" '.browser.load.presses.stealPercent = "high"')" \
		"$(stub_row browser-panel-100000-nc9 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.requested = "30"')"
	rc=0
	run --out "$work/counts.json" --evidence "$gl" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "malformed sampling records must leave the record incomplete (exit 10), exited $rc"
	f=$work/counts.json
	expect "only the clean sampling row is counted among nine" '.configurations[0] | .runsFound == 9 and .runsCounted == 1' "$f"
	expect "contradictory counts are named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc2") and contains("do not match the 30 recorded presses")) | any' "$f"
	expect "a negative and a mistyped press time are named" '.configurations[0].gaps as $g | ["nc3", "nc4"] | all(. as $c | $g | map(contains("browser-panel-100000-" + $c + " ") and contains("nonnegative")) | any)' "$f"
	expect "a repeated press index is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc5") and contains("sequence 0..N-1")) | any' "$f"
	expect "an unknown press origin is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc6") and contains("not one the runner records")) | any' "$f"
	expect "a mismatched first press is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc7") and contains("firstPressMs")) | any' "$f"
	expect "a mistyped load counter is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc8") and contains("not numbers")) | any' "$f"
	expect "a mistyped count is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc9") and contains("whole numbers")) | any' "$f"

	# A malformed nested press names its excluded row without aborting the report: a non-object element and a
	# non-string origin beside an independent clean control still publish INCOMPLETE JSON with the clean figures.
	gp=$work/pressshape
	cell "$gp" "$rev" \
		"$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[5] = "bad"')" \
		"$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)" '.browser.nextChangedSampling.presses[5].origin = 42')"
	rc=0
	run --out "$work/pressshape.json" --evidence "$gp" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "malformed nested press records must leave the record incomplete (exit 10), exited $rc"
	f=$work/pressshape.json
	expect "the clean row is still counted beside malformed rows" '.configurations[0] | .runsFound == 3 and .runsCounted == 1' "$f"
	expect "the clean figures are still published" '.configurations[0].pooled.presses.count == 30' "$f"
	expect "a non-object press element is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc2") and contains("not an object")) | any' "$f"
	expect "a non-string press origin is named" '.configurations[0].gaps | map(contains("browser-panel-100000-nc3") and contains("not one the runner records")) | any' "$f"

	# --- 18. a standard-shaped missing sampling entry fails: the reason count lives in the case name, never a reasons key ---
	# The status schema (deploy/digitalocean/qc1/lib.sh) lists missing entries as {item, size, case} only, like the validcase check;
	# the reason count is parsed from browser-panel-<reasons>-nc<n>. The same predicate guards the outer-failure tie-out.
	gm=$work/missing-stale
	cell "$gm" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	jq -nc '{complete:false, missing:[{item:10, size:100000, case:"browser-panel-100000-nc1"}], pending:[], invalid:[], budgetFailures:[], coverage:{kind:"pilot", reasons:["stub"]}, releaseEvidence:false}' >"$gm/evidence-status.json"
	rc=0
	run --out "$work/missingstale.json" --evidence "$gm" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a standard-shaped missing entry contradicting an available row must leave the record incomplete (exit 10), exited $rc"
	expect "the stale missing sampling entry is named without a reasons key" '.complete == false and ([.configurations[].runsCounted] == [0, 0]) and (.gaps | map(contains("expected sampling measurement is missing: item 10, size 100000, case browser-panel-100000-nc1")) | any)' "$work/missingstale.json"
	gn=$work/missing-absent
	cell "$gn" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc3 100000 100000 "$(seq_ms 1 30)")"
	jq -nc '{complete:false, missing:[{item:10, size:100000, case:"browser-panel-100000-nc4"}], pending:[], invalid:[], budgetFailures:[], coverage:{kind:"pilot", reasons:["stub"]}, releaseEvidence:false}' >"$gn/evidence-status.json"
	rc=0
	run --out "$work/missingabsent.json" --evidence "$gn" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "a standard-shaped missing sampling run must leave the record incomplete (exit 10), exited $rc"
	expect "the absent sampling run is named" '.complete == false and (.gaps | map(contains("expected sampling measurement is missing: item 10, size 100000, case browser-panel-100000-nc4")) | any)' "$work/missingabsent.json"
	gq=$work/missing-outer
	mkdir -p "$gq"
	cp -r "$gn" "$gq/qc1"
	outer_record "$gq/qc1" fail fail 0 '{"complete":false, "coverage":"pilot", "releaseEvidence":false, "budgetFailures":0, "invalid":0, "missing":1, "pending":0, "aborted":false, "abortedReason":"", "missingCases":[{"item":10, "size":100000, "case":"browser-panel-100000-nc4"}], "pendingCases":[], "invalidFindings":[], "coverageReasons":["stub"]}' >"$gq/evidence.json"
	rc=0
	run --out "$work/missingouter.json" --evidence "$gq/qc1" --revision "$rev" >/dev/null 2>&1 || rc=$?
	[[ $rc == 10 ]] || fail "an exit-0 outer failure over a missing sampling row must stay a failure (exit 10), exited $rc"
	expect "the outer tie-out agrees that a sampling row is missing" '.complete == false and (.gaps | map(contains("outer runner") and contains("beyond intentional omissions")) | any)' "$work/missingouter.json"

	echo "reason-review-next-changed self-test: ok ($n checks)"
}

if ((dry_run)); then
	echo "Next changed reason sampling (issue #298)"
	echo "  configurations: a $REVIEW_REASONS-reason review over backgrounds of $(jq -r 'join(" and ")' <<<"$BACKGROUNDS") rows"
	echo "  required: at least $REQUIRED_PRESSES presses in each of at least $REQUIRED_RUNS runs per configuration (pooled per configuration: p50, p95, max)"
	echo "  target: ${TARGET_MS} ms (a proposal of the scaling design, not an owner decision)"
	echo "  commands (EVIDENCE is a new directory outside this checkout):"
	runner_commands EVIDENCE | sed 's/^/    /'
	exit 0
fi

if ((self_test)); then
	next_changed_self_test
	exit 0
fi

[[ -n $out ]] || die "--out FILE is required"
((${#dirs[@]} > 0)) || die "at least one --evidence DIR (the qc1 directory of a sampling run) is required"
[[ ! -e $out ]] || die "$out already exists; a record is never overwritten"
if [[ -z $revision ]]; then
	revision=$(git -C "$root" rev-parse HEAD 2>/dev/null) || die "cannot read HEAD; pass --revision"
fi
[[ $revision =~ ^[0-9a-f]{40}$ ]] || die "--revision must be a full 40-character SHA"
# R3: one run cannot become several through repeated directory arguments. Canonicalize every directory (so D, D/. and a symlink to D are the same),
# refuse one inside the checkout, and refuse a repeat. This runs before anything is written.
declare -A _seen_evidence=()
for d in "${dirs[@]}"; do
	canon_dir=$(realpath -m "$d")
	case $canon_dir in "$root" | "$root"/*) die "$d is inside this checkout; evidence stays outside it" ;; esac
	if [[ -n ${_seen_evidence[$canon_dir]:-} ]]; then
		die "$d is the same evidence directory as ${_seen_evidence[$canon_dir]}; one run cannot become two through repeated directory arguments"
	fi
	_seen_evidence[$canon_dir]=$d
done
mkdir -p -- "$(dirname -- "$out")"
tmp_out=$(mktemp "${out}.XXXXXX")
trap 'rm -f -- "$tmp_out"' EXIT
build_record "$revision" "${dirs[@]}" >"$tmp_out"
jq -e . "$tmp_out" >/dev/null || die "the record could not be written as JSON"
mv -- "$tmp_out" "$out"
trap - EXIT
status=$(jq -r 'if .complete then "COMPLETE" else "INCOMPLETE" end' "$out")
echo "reason-review-next-changed: $status ($out)"
jq -r '.configurations[] | "  background \(.background): \(.runsCounted)/\(.runsFound) runs counted" + (if .pooled then ", \(.pooled.presses.count) presses, p50 \(.pooled.presses.p50Ms) ms, p95 \(.pooled.presses.p95Ms) ms, max \(.pooled.presses.maxMs) ms" else ", no figures" end)' "$out"
jq -r '.gaps[]? | "  gap: \(.)"' "$out"
[[ $status == COMPLETE ]] || exit 10
