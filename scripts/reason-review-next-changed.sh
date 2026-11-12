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
# own origin, walked exactly the reasons seeded, was refused with 409 and reloaded, pressed the control as many times as it was asked to and at least 30 times, never ran
# under the profiler, and carries the host's load counters (an absent counter is a gap, not a zero). Each evidence directory's environment must be a complete record of the
# revision asked for (qc1_environment_problems). A configuration is complete only with at least 3 counted runs; the record is COMPLETE only when both configurations are.
# Several --evidence directories add independent stacks: their runs are pooled by (background rows, reasons).
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
'

# runs_json DIR prints the sampling rows of one evidence directory as an array of run objects, each with the reasons it does or does not count.
runs_json() {
	local dir=$1
	jq -sc --argjson need "$REQUIRED_PRESSES" --argjson target "$TARGET_MS" --arg dir "$dir" "$JQ_DEFS"'
		def isnum: type == "number";
		[ .[] | select(type == "object" and .item == 10 and ((.case // "") | test("^browser-panel-[0-9]+-nc[0-9]+$")))
		  | . as $r | ($r.browser // {}) as $b | ($b.nextChangedSampling // null) as $s
		  | ([ ($s.presses // [])[] | .ms | select(isnum) ]) as $ms
		  | {
		      dir: $dir, case: .case, background: .size, reasons: .reasons, run: (.case | capture("-nc(?<n>[0-9]+)$").n | tonumber),
		      problems: [
		        (if .status != "measured" then "row status is \(.status), not measured" else empty end),
		        (if $b.failed != null then "the run stopped in \($b.failed.phase // "an unknown phase")" else empty end),
		        (if $b.apiOrigin != "real" then "the pages did not all come from the stack origin" else empty end),
		        (if $b.walkTotal != .reasons then "the walk found \($b.walkTotal) reasons, not \(.reasons)" else empty end),
		        (if ($b.press.observed != true) or ($b.press.status != "409") then "the refused press was not observed as 409" else empty end),
		        (if $b.reload.unchosenAfterReload != 1 then "the review was not read again with exactly one reason to choose" else empty end),
		        (if $s == null then "the row has no nextChangedSampling (the run was not asked for repeated presses)" else empty end),
		        (if $s != null and ($s.requested | isnum | not) then "requested presses are not recorded" else empty end),
		        (if $s != null and ($s.performed != $s.requested) then "pressed \($s.performed) times of \($s.requested) asked" else empty end),
		        (if $s != null and ($ms | length) < $need then "\($ms | length) timed presses, fewer than \($need)" else empty end),
		        (if $s != null and ($ms | length) != ($s.presses // [] | length) then "a press has no time" else empty end),
		        (if $s != null and ($s.profile != null) then "the run was profiled (sampling overhead): a diagnostic, not a sample" else empty end),
		        (if $b.load == null then "no host load counters were read"
		         elif ($b.load.presses == null) or ($b.load.presses.seconds == null) or ($b.load.presses.stealPercent == null) or ($b.load.presses.load1Start == null) or ($b.load.presses.load1End == null)
		         then "the host load counters around the presses are incomplete (loadavg, CPU steal)"
		         else empty end)
		      ],
		      counted: false,
		      presses: $ms,
		      stats: ($ms | stats),
		      overTarget: ([ $ms[] | select(. > $target) ] | length),
		      firstPressMs: ($s.firstPressMs // null),
		      afterFirst: (($s.presses // [])[1:] | map(.ms | select(isnum)) | stats),
		      byOrigin: (reduce (($s.presses // [])[]) as $p ({}; .[($p.origin // "unknown")] += [$p.ms]) | map_values(stats)),
		      slowest: ([ ($s.presses // [])[] | select(.ms | isnum) ] | sort_by(-.ms) | .[0:5] | map({index, origin, ms, atMs, longTaskMs, longTaskCount})),
		      load: ($b.load // null),
		      browser: ($b.browser // null)
		    }
		  | .counted = (.problems | length == 0)
		]' "$dir/measurements.jsonl"
}

# build_record DIR... prints the record (stdout) from the evidence directories; gaps are in the record, never errors.
build_record() {
	local rev=$1 d all='[]' dirjson='[]'
	shift
	for d in "$@"; do
		local present=true why='' problems='[]' envrev=null browserv=null runs='[]'
		if [[ ! -s $d/measurements.jsonl || ! -s $d/environment.json ]] || ! jq -e . "$d/environment.json" >/dev/null 2>&1; then
			present=false
			why='measurements.jsonl or environment.json is missing or unreadable'
		elif ! jq -sc . "$d/measurements.jsonl" >/dev/null 2>&1; then
			present=false
			why='measurements.jsonl is not JSON lines'
		else
			envrev=$(jq -c '.revision // null' "$d/environment.json")
			browserv=$(jq -c '.browserRuntime.chromium // null' "$d/environment.json")
			problems=$(qc1_environment_problems "$d/environment.json" "$rev" 2>/dev/null | jq -R . | jq -sc .) || problems='["the environment record could not be checked"]'
			runs=$(runs_json "$d")
			# A directory whose runs were not asked for repeated presses by its own settings is named, so a mislabelled directory is a visible gap.
			if [[ $(jq -r '.settings.nextChangedOnly // false' "$d/environment.json") != true ]]; then
				problems=$(jq -c '. + ["environment.json does not record --next-changed-only"]' <<<"$problems")
			fi
		fi
		dirjson=$(jq -c --arg dir "$d" --argjson present "$present" --arg why "$why" --argjson revision "$envrev" --argjson chromium "$browserv" --argjson problems "$problems" \
			'. + [{dir:$dir, present:$present, why:(if $why == "" then null else $why end), revision:$revision, chromium:$chromium, environmentProblems:$problems}]' <<<"$dirjson")
		if [[ $present == true ]]; then
			# a directory of another revision or with an environment problem contributes runs that are listed but not counted
			all=$(jq -c --argjson runs "$runs" --argjson problems "$problems" --argjson revision "$envrev" --arg rev "$rev" '
				. + [ $runs[] | if ($revision != $rev) then (.problems += ["the evidence is for revision \($revision), not \($rev)"] | .counted = false)
				      elif ($problems | length) > 0 then (.problems += ["the evidence directory has an environment problem"] | .counted = false)
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
		( [ $backgrounds[] as $bg
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
		        perRun: ($mine | map({case, dir, run, counted, problems, stats, overTarget, firstPressMs, afterFirst, byOrigin, slowest, load, chromium: .browser})),
		        p95OverTarget: (if $pool == null then null else ($pool.presses.p95Ms > $target) end)
		      } ] ) as $configs
		| { record: "next-changed-reason-samples", issue: 298, generatedAt: $generatedAt, revision: $revision,
		    target: {ms: $target, kind: "the proposed navigation budget of the scaling design (QC1_NAV_BUDGET_MS), an engineering proposal and not an owner decision"},
		    required: {pressesPerRun: $need, runsPerConfiguration: $runsNeeded, reasons: $reasons, backgrounds: $backgrounds},
		    originalObservations: $original,
		    evidenceDirectories: $dirs,
		    configurations: $configs,
		    complete: ($configs | all(.complete)),
		    gaps: [ $configs[] | .gaps[] ] + [ $dirs[] | select(.present | not) | "\(.dir): \(.why)" ],
		    consequence: (if ($configs | any(.p95OverTarget == true))
		                  then "a pooled p95 is over the target in " + ([ $configs[] | select(.p95OverTarget == true) | "\(.background)-row background" ] | join(", ")) + ": profile the control (repeat the sampling with --qc1-arg --profile-next-changed) before any change"
		                  else null end),
		    interpretation: "none. This record states the numbers and their load context. It does not call a delay noise, does not call the target met and does not choose a fix; a pooled p95 at or under the target on a COMPLETE record is evidence for the reviewer, not a verdict."
		  }'
}

# This script's own checks, against stub evidence written here (never against a measurement): the figures are computed by hand from stub press times, so each assertion
# is a fact about THIS script's arithmetic and bookkeeping, not about the product. The stubs also prove the refusals: a run that is short, failed, partial, profiled, for
# another revision, under a broken environment or without load counters is listed and never counted, and an absent configuration has no figures at all.
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
	stub_env() { # revision [nextChangedOnly] [limitsApplied]
		local img pg cid sha
		img="sha256:$(hex64 a)" pg="sha256:$(hex64 b)" cid=$(hex64 e) sha=$(hex64 f)
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
		jq -nc --arg case "$1" --argjson bg "$2" --argjson reasons "$3" --arg ms "$4" "
			(\$ms | split(\" \") | map(tonumber)) as \$v
			| {status:\"measured\", item:10, case:\$case, size:\$bg, reasons:\$reasons,
			   browser:{failed:null, apiOrigin:\"real\", walkTotal:\$reasons, press:{observed:true, status:\"409\"}, reload:{unchosenAfterReload:1},
			     nextChangedSampling:{requested:(\$v | length), performed:(\$v | length), firstPressMs:\$v[0], profile:null,
			       presses:[range(0; \$v | length) as \$i | {index:\$i, origin:([\"first-page\",\"after-changed-page\",\"last-page\"][\$i % 3]), ms:\$v[\$i], atMs:(\$i * 100), longTaskMs:0, longTaskCount:0}]},
			     load:{presses:{seconds:10, stealPercent:0.1, load1Start:0.5, load1End:0.6}},
			     browser:\"chromium 130.0\"}}
			| ${5:-.}"
	}
	seq_ms() { seq -s ' ' "$1" "$2"; } # 1 2 ... 30
	cell() { # dir revision rows-file-lines... : writes environment.json and measurements.jsonl
		local dir=$1 revision=$2
		shift 2
		mkdir -p "$dir"
		stub_env "$revision" >"$dir/environment.json"
		printf '%s\n' "$@" >"$dir/measurements.jsonl"
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

	# --- 6. pooling across independent stacks --------------------------------------------------------------------------------------------------------------------------
	d3=$work/stack-b
	cell "$work/stack-a" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 100000 100000 "$(seq_ms 1 30)")" \
		"$(stub_row browser-panel-100000-nc1 1000000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc2 1000000 100000 "$(seq_ms 1 30)")"
	cell "$d3" "$rev" "$(stub_row browser-panel-100000-nc1 100000 100000 "$(seq_ms 1 30)")" "$(stub_row browser-panel-100000-nc1 1000000 100000 "$(seq_ms 1 30)")"
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
for d in "${dirs[@]}"; do
	case $(realpath -m "$d") in "$root" | "$root"/*) die "$d is inside this checkout; evidence stays outside it" ;; esac
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
