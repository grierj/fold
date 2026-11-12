#!/usr/bin/env bash
# The reason-review scale MATRIX (issues #293 and #294): one QC-1 run per cell, joined into one record.
#
# A QC-1 run (deploy/digitalocean/qc1-measure.sh, through scripts/reason-review-scale.sh) covers the table sizes, the reason counts, both party
# directions, keep, blank and mixed decisions, the dense 100,000, the request-size comparison with the old envelope and the browser panel for ONE layout of
# the scenario reasons and ONE charset. The matrix is the runs that together cover what the two issues ask for:
#
#   ascii-spread       the baseline: ASCII reasons spread through the table
#   ascii-tail         the reasons after every other row (the worst case for a scan from the start)
#   ascii-head         the reasons before every other row
#   multibyte-spread   1,000-rune reasons of mixed 1- to 4-byte runes (#293), with the byte-for-byte, digest and on-screen text checks
#
# A cell is the QC-1 evidence directory (the "qc1" directory scripts/reason-review-scale.sh leaves under its --evidence-dir). The runner's own record of that run,
# evidence.json, is read from the directory above it. This script reads those files (environment.json, evidence-status.json, measurements.jsonl, abort.json,
# COLLECTION-FAILED.txt, summary.json) and writes one JSON record: the revision, the environment and the fixtures of every cell, its timings, audits, WAL and
# memory, its budget failures and gaps, and the sha256 of the artifacts, which stay outside the checkout. It measures nothing and runs no container.
#
#   scripts/reason-review-matrix.sh --out FILE --cell ascii-spread=DIR [--cell ascii-tail=DIR ...] [--revision SHA]
#   scripts/reason-review-matrix.sh --dry-run        print the cells the matrix needs and the record path, write nothing
#   scripts/reason-review-matrix.sh --self-test      this script's own checks against stub evidence (seconds, no Docker)
#
# Nothing a cell says about itself is taken on trust. The matrix RECOMPUTES the evidence status of every cell from its measurement rows and its environment record,
# with qc1_evidence_status (deploy/digitalocean/qc1/lib.sh, the function that wrote the saved status), and judges by that. The saved evidence-status.json must equal the
# recomputed one, so a stale, edited or mismatched status file is a gap, not a certificate. The judge that recomputes must be the one the run recorded (the sha256 of
# lib.sh and app-facts.json in the environment record), the environment must be a complete record with no problem named, and the settings must name the full matrix.
# The judge also binds the layout: every discovery, deletion, workers and browser row must carry the layout the cell's settings name, so a head or tail label over
# rows measured in another layout (or rows that name none) is an invalid finding of the recomputed status, and each cell's record carries its rowLayouts.
# The runner's record (evidence.json) must exist, name the same revision on a clean tree, and show one QC-1 step that exited 0, was not a failure or an interruption,
# is the directory given, and summarised the same status. COLLECTION-FAILED.txt, which the nested runner writes when it could not bring something out of the
# container, is a gap of its own even when every row is complete.
#
# Status. FULL only when every required cell is present, all name the same revision (the one given, else HEAD of this checkout), and every one of them passes all of
# that with coverage full, release evidence, nothing missing, pending, invalid or aborted. Anything else is PILOT, and the record lists every gap by name: an absent
# cell, a cell of another revision, a cell whose recomputed coverage is not full, the cases a cell reports as pending (at the time of writing the legacy discovery and
# legacy PUT cases, which are not built, so a real matrix is a PILOT until they are), a status that disagrees with its rows, a runner record that is missing or failed. A
# budget failure is a finding for engineering disposition (R4-C06), never a gap in the matrix and never a waiver; the record carries each cell's budget failures by kind.
# Exit status: 0 for FULL, 10 for PILOT, 2 for a refusal before anything was written.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)

die() {
	echo "reason-review-matrix: $*" >&2
	exit 2
}

REQUIRED_CELLS=(ascii-spread ascii-tail ascii-head multibyte-spread)

out=
revision=
dry_run=0
self_test=0
cells=()

while (($#)); do
	case $1 in
	--out | --revision | --cell)
		(($# >= 2)) || die "$1 needs a value"
		case $1 in
		--out) out=$2 ;;
		--revision) revision=$2 ;;
		--cell) cells+=("$2") ;;
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
command -v sha256sum >/dev/null || die "sha256sum is required"

# The judge that recomputes a cell's status is the harness's own: the same function, from this checkout, whose bytes the environment records must name.
QC1_LIB=$root/deploy/digitalocean/qc1/lib.sh
QC1_FACTS_FILE=$root/deploy/digitalocean/qc1/app-facts.json
[[ -r $QC1_LIB && -r $QC1_FACTS_FILE ]] || die "the QC-1 harness ($QC1_LIB) is not in this checkout"
unset QC1_FACTS
# shellcheck source=deploy/digitalocean/qc1/lib.sh
source "$QC1_LIB"
judge_lib_sha=$(sha256sum "$QC1_LIB" | cut -d' ' -f1)
judge_facts_sha=$(sha256sum "$QC1_FACTS_FILE" | cut -d' ' -f1)
# The settings of the full matrix, as sorted number lists.
full_sizes=$(qc1_list "$QC1_SIZES_DEFAULT" | jq -Rsc 'split("\n") | map(select(. != "") | tonumber) | sort')
full_counts=$(qc1_list "$QC1_COUNTS_DEFAULT" | jq -Rsc 'split("\n") | map(select(. != "") | tonumber) | sort')
full_dense=$(qc1_list "$QC1_DENSE_COUNTS_DEFAULT" | jq -Rsc 'split("\n") | map(select(. != "") | tonumber) | sort')

# cell_json NAME DIR prints one cell as JSON. A directory that cannot be read is a cell that is not present, with the reason, never an error.
cell_json() {
	local name=$1 dir=$2 tmp rc=0
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/fold-matrix-cell.XXXXXX")
	cell_json_in "$name" "$dir" "$tmp" || rc=$?
	rm -rf -- "$tmp"
	return "$rc"
}

cell_json_in() {
	local name=$1 dir=$2 tmp=$3 charset layout f artifacts='{}' h sizes counts dense maxreq rev abort="" why=
	local recomputed_ok=false outer_file step_dir="" step_matches=false collected=
	charset=${name%%-*}
	layout=${name#*-}
	for f in environment.json evidence-status.json measurements.jsonl; do
		if [[ ! -s $dir/$f ]] || { [[ $f != measurements.jsonl ]] && ! jq -e . "$dir/$f" >/dev/null 2>&1; }; then
			jq -nc --arg name "$name" --arg dir "$dir" --arg why "$f is missing or unreadable" '{name:$name, dir:$dir, present:false, why:$why}'
			return 0
		fi
	done
	if ! jq -sc . "$dir/measurements.jsonl" >"$tmp/rows.json" 2>/dev/null; then
		jq -nc --arg name "$name" --arg dir "$dir" '{name:$name, dir:$dir, present:false, why:"measurements.jsonl is not JSON lines"}'
		return 0
	fi
	for f in environment.json evidence-status.json measurements.jsonl summary.json failures.jsonl abort.json COLLECTION-FAILED.txt; do
		[[ -s $dir/$f ]] || continue
		h=$(sha256sum "$dir/$f" | cut -d' ' -f1)
		artifacts=$(jq -c --arg f "$f" --arg h "$h" '. + {($f): $h}' <<<"$artifacts")
	done
	outer_file=$(dirname "$dir")/evidence.json
	if [[ -s $outer_file ]]; then
		h=$(sha256sum "$outer_file" | cut -d' ' -f1)
		artifacts=$(jq -c --arg h "$h" '. + {"runner evidence.json": $h}' <<<"$artifacts")
	fi

	# R2: the status is recomputed from the rows and the environment, with the arguments the environment says the run was given.
	IFS='|' read -r sizes counts dense maxreq rev < <(jq -r '[(.settings.sizes // "" | tostring), (.settings.counts // "" | tostring), (.settings.denseCounts // "" | tostring | if . == "none" then "" else . end), (.maxRequestBytes // 0 | tostring), (.revision // "")] | join("|")' "$dir/environment.json")
	[[ $maxreq =~ ^[0-9]+$ ]] || maxreq=0
	[[ ! -s $dir/abort.json ]] || abort=$dir/abort.json
	if qc1_evidence_status "$dir/measurements.jsonl" "$sizes" "$counts" "$dir/environment.json" "$rev" "$maxreq" "$dense" "$abort" >"$tmp/recomputed.json" 2>"$tmp/recompute.err" && jq -e 'type == "object" and has("complete") and has("coverage")' "$tmp/recomputed.json" >/dev/null 2>&1; then
		recomputed_ok=true
	else
		why=$(head -c 300 "$tmp/recompute.err" 2>/dev/null | tr '\n' ' ' || true)
		echo null >"$tmp/recomputed.json"
	fi

	# R1: what the runner recorded about this run, and what the nested runner could not bring out of the container.
	if [[ -s $outer_file ]] && jq -e 'type == "object"' "$outer_file" >/dev/null 2>&1; then
		cp "$outer_file" "$tmp/outer.json"
		step_dir=$(jq -r '[.steps[]? | select(.name == "qc1")][0].artifacts // ""' "$tmp/outer.json")
		if [[ -n $step_dir && $(realpath -m "$step_dir") == "$(realpath -m "$dir")" ]]; then step_matches=true; fi
	else
		echo null >"$tmp/outer.json"
	fi
	if [[ -s $dir/COLLECTION-FAILED.txt ]]; then
		collected=$(head -c 4000 "$dir/COLLECTION-FAILED.txt" | tr '\n' ';' | head -c 600)
	elif [[ -e $dir/COLLECTION-FAILED.txt ]]; then
		collected="COLLECTION-FAILED.txt is present and empty"
	fi

	jq -nc --arg name "$name" --arg dir "$dir" --arg charset "$charset" --arg layout "$layout" --argjson artifacts "$artifacts" \
		--arg why "$why" --argjson recomputedOk "$recomputed_ok" --argjson stepMatches "$step_matches" --arg collected "$collected" \
		--arg libSha "$judge_lib_sha" --arg factsSha "$judge_facts_sha" \
		--argjson fullSizes "$full_sizes" --argjson fullCounts "$full_counts" --argjson fullDense "$full_dense" \
		--slurpfile env "$dir/environment.json" --slurpfile saved "$dir/evidence-status.json" --slurpfile rc "$tmp/recomputed.json" \
		--slurpfile outerDoc "$tmp/outer.json" --slurpfile rows "$tmp/rows.json" '
		def top(f): [ .[] | f | numbers ] | max;
		def count(p): [ .[] | select(p) ] | length;
		def nums($x): if $x == null then null else ($x | tostring | split(",") | map(tonumber? // empty) | sort | unique) end;
		def srcsha($p): ([ ($env[0].sources // [])[] | select(.path == $p) | .sha256 ] | .[0]) // null;
		$env[0] as $e | $saved[0] as $sv | ($rc[0] // {}) as $v | $outerDoc[0] as $o
		| ($rows[0] | map(select(type == "object" and (.status == "measured" or .status == "observation")))) as $m
		| ($o.steps // [] | map(select(.name == "qc1"))) as $q
		| ($q[0] // null) as $step
		| {name:$name, dir:$dir, present:true, revision:$e.revision,
		   settingsAgree: ((($e.settings.charset // "ascii") == $charset) and ($e.settings.layout == $layout)),
		   settingsFull: ((nums($e.settings.sizes) == $fullSizes) and (nums($e.settings.counts) == $fullCounts) and (($e.settings.denseCounts // "none" | if . == "none" then [] else nums(.) end) == $fullDense)),
		   environment: {imageId:$e.imageId, postgresImageId:$e.postgresImageId, postgresVersion:$e.postgresVersion, limits:$e.limits,
		                 daemon:($e.daemon | {serverVersion, rootless, ncpu, memTotal}), browser:($e.browserRuntime | {chromium, playwright}),
		                 incomplete:(($e.incomplete // false) != false)},
		   fixtures: ($e.settings | {sizes, counts, denseCounts, deleteCounts, layout, charset, reasonLen, targetReasonLen, browser, wire}),
		   artifacts: $artifacts,
		   rowLayouts: ($m | map(select(.item == 3 or .item == 6 or .item == 9 or .item == 10) | .layout // "none") | group_by(.) | map({layout: .[0], rows: length})),
		   judge: {libMatchesRun: (srcsha("deploy/digitalocean/qc1/lib.sh") == $libSha), factsMatchRun: (srcsha("deploy/digitalocean/qc1/app-facts.json") == $factsSha)},
		   recompute: {ok:$recomputedOk, why:$why,
		               savedEqualsRecomputed: ($recomputedOk and ($sv == $rc[0])),
		               differingKeys: (if $recomputedOk and ($sv | type) == "object" then ([ ($sv | keys[]), ($v | keys[]) ] | unique | map(select(($sv[.]) != ($v[.]))) | .[:8]) else [] end)},
		   runner: (if $o == null then {present:false} else
		              {present:true, schema:($o.schema // null), revision:($o.revision // null), workingTreeClean:($o.workingTreeClean // null), result:($o.result // null),
		               qc1Steps:($q | length), stepIsThisDirectory:$stepMatches,
		               step:(if $step == null then null else {verdict:$step.verdict, exit:$step.exit, collectionFailed:(($step.summary.collectionFailed // false) == true),
		                                                       summaryAgrees: ($recomputedOk and ($step.summary != null)
		                                                         and ($step.summary.complete == ($v.complete // false)) and ($step.summary.coverage == ($v.coverage.kind // null))
		                                                         and ($step.summary.releaseEvidence == ($v.releaseEvidence // false)) and ($step.summary.missing == (($v.missing // []) | length))
		                                                         and ($step.summary.invalid == (($v.invalid // []) | length)) and ($step.summary.pending == (($v.pending // []) | length))
		                                                         and ($step.summary.aborted == (($v.aborted // null) != null)))} end)} end),
		   collectionFailed: (if $collected == "" then null else $collected end),
		   evidence: {complete:($v.complete // false), coverage:($v.coverage.kind // null), coverageReasons:($v.coverage.reasons // []), releaseEvidence:($v.releaseEvidence // false),
		              missing:(($v.missing // []) | length), invalid:(($v.invalid // []) | length), pending:(($v.pending // []) | length), aborted:(($v.aborted // null) != null),
		              missingCases:[($v.missing // [])[:5][] | "item \(.item) size \(.size) \(.case)"],
		              pendingCases:[($v.pending // [])[] | "item \(.item) \(.case)"] | unique,
		              invalidFindings:[($v.invalid // [])[:5][] | "\(.failure) item \(.item // "?") size \(.size // "?") \(.case // "")"]},
		   budgetFailures: {count:(($v.budgetFailures // []) | length), byKind:(($v.budgetFailures // []) | group_by(.failure) | map({failure:.[0].failure, count:length}))},
		   exactness: {
		     deletionsChecked: ($m | count(.outcome.textExact != null)), textExact: ($m | count(.outcome.textExact == true)), textNotExact: ($m | count(.outcome.textExact == false)),
		     browserDigestChecked: ($m | count(.agreement.digest.checked == true)), browserDigestEqual: ($m | count(.agreement.digest.equal == true)), browserDigestDiffers: ($m | count(.agreement.digest.equal == false)),
		     domTextChecked: ($m | count(.agreement.domText.checked == true)), domTextExact: ($m | count(.agreement.domText.exact == true)),
		     domTextRowsChecked: ([ $m[] | .agreement.domText.rowsChecked | numbers ] | add // 0), domTextMismatched: ([ $m[] | .agreement.domText.mismatched | numbers ] | add // 0)},
		   figures: ([ $m[] | .size | numbers ] | unique | map(. as $s | ($m | map(select(.size == $s))) as $z | {size:$s,
		     rows:($z | length),
		     traversalMsMax:($z | top(.traversal.totalMs)), responseBytesMax:($z | top(.traversal.responseBytes)), pageBytesMax:($z | top(.traversal.maxPageBytes)),
		     deleteMsMax:($z | top(.delete.ms)), deleteWalBytesMax:($z | top(.delete.walBytes)), deleteAppPeakKbMax:($z | top(.delete.appPeakKb)), deleteDbPeakKbMax:($z | top(.delete.dbPeakKb)),
		     auditRowsAfterMax:($z | top(.delete.auditRowsAfter)), auditBytesAfterMax:($z | top(.delete.auditBytesAfter)),
		     bodyBytesMax:($z | top(.bodyBytes)), capacityReasons:($z | top(.request.capacity.reasons)), largestBodyBytes:($z | top(.request.largest.bodyBytes)),
		     oldEnvelope:([ $z[] | select(.item == 5) | .request.legacyComparison | select(. != null) | {idDigits, capacityEntries, fitBytes, fitStatus, overStatus}] | .[0] // null),
		     browser:{heapAfterGcBytesMax:($z | top(.browser.heapAfterGcBytes)), domNodesMax:($z | top(.browser.domNodes)), renderReadyMsMax:($z | top(.browser.renderReadyMs)),
		              pageResponseBytesMax:($z | top(.browser.pageResponseBytes)), pagesTextBytesPerRune:([ $z[] | .browser.pagesText.bytesPerRune | numbers ] | max)}})) }'
}

# assemble REVISION CELL_JSON... prints the matrix record.
assemble() {
	local rev=$1
	shift
	printf '%s\n' "$@" | jq -sc --arg revision "$rev" --argjson required "$(printf '%s\n' "${REQUIRED_CELLS[@]}" | jq -Rsc 'split("\n") | map(select(. != ""))')" '
		. as $cells
		| ($cells | map(select(.present == true))) as $have
		| ($have | map(.revision) | unique) as $revs
		| [ ($required[] as $n | select(([$have[] | select(.name == $n)] | length) == 0) | "cell \($n): no evidence" + (([$cells[] | select(.name == $n and .present == false) | " (" + .why + ")"] | .[0]) // "")),
		    (if ($revs | length) > 1 then "cells name more than one revision: \($revs | join(", "))" else empty end),
		    (if $revision != "" and ($revs | any(. != $revision)) then "evidence names a revision other than \($revision)" else empty end),
		    ($have[] | select(.settingsAgree | not) | "cell \(.name): its settings name another layout or charset than the cell"),
		    ($have[] | select(.settingsFull | not) | "cell \(.name): its settings do not name the full matrix (sizes 10000,100000,1000000, the default counts and the dense 100000)"),
		    ($have[] | select(.environment.incomplete) | "cell \(.name): its environment record is marked incomplete"),
		    ($have[] | select(.judge.libMatchesRun | not) | "cell \(.name): qc1/lib.sh here is not the file the run recorded, so its status cannot be recomputed with the run'"'"'s own judge"),
		    ($have[] | select(.judge.factsMatchRun | not) | "cell \(.name): qc1/app-facts.json here is not the file the run recorded, so its status cannot be recomputed with the run'"'"'s own judge"),
		    ($have[] | select(.recompute.ok | not) | "cell \(.name): its status could not be recomputed from its rows and environment" + (if .recompute.why != "" then " (" + .recompute.why + ")" else "" end)),
		    ($have[] | select(.recompute.ok and (.recompute.savedEqualsRecomputed | not)) | "cell \(.name): the saved evidence-status.json differs from the status recomputed from its rows and environment (\(.recompute.differingKeys | join(", "))); a stale, edited or mismatched status certifies nothing"),
		    ($have[] | select(.collectionFailed != null) | "cell \(.name): the nested run left COLLECTION-FAILED.txt, so its artifacts were not all brought out: \(.collectionFailed)"),
		    ($have[] | select(.runner.present | not) | "cell \(.name): no runner record (evidence.json) beside the cell, so the run'"'"'s exit and collection are not confirmed"),
		    ($have[] | select(.runner.present and .runner.schema != "fold/reason-review-scale/v1") | "cell \(.name): the runner record is not a fold/reason-review-scale/v1 record"),
		    ($have[] | select(.runner.present and .runner.qc1Steps != 1) | "cell \(.name): the runner record holds \(.runner.qc1Steps) qc1 steps, not one"),
		    ($have[] | select(.runner.present and .runner.qc1Steps == 1 and (.runner.stepIsThisDirectory | not)) | "cell \(.name): the runner record'"'"'s qc1 step is for another directory than the cell"),
		    ($have[] | select(.runner.present and .runner.step != null and ((.runner.step.exit != 0) or ((.runner.step.verdict | IN("pass", "partial")) | not))) | "cell \(.name): the runner recorded its QC-1 step as \(.runner.step.verdict), exit \(.runner.step.exit)"),
		    ($have[] | select(.runner.present and .runner.step != null and .runner.step.collectionFailed) | "cell \(.name): the runner recorded a collection failure for its QC-1 step"),
		    ($have[] | select(.runner.present and ((.runner.result | IN("pass", "partial")) | not)) | "cell \(.name): the runner finished with result \(.runner.result), not pass or partial"),
		    ($have[] | select(.runner.present and .runner.revision != .revision) | "cell \(.name): the runner record names revision \(.runner.revision), the cell'"'"'s environment \(.revision)"),
		    ($have[] | select(.runner.present and .runner.workingTreeClean != true) | "cell \(.name): the runner record does not say the working tree was clean"),
		    ($have[] | select(.runner.present and .runner.step != null and (.runner.step.summaryAgrees | not)) | "cell \(.name): the runner'"'"'s summary of its QC-1 step differs from the status recomputed from the rows"),
		    ($have[] | select(.evidence.aborted) | "cell \(.name): the run aborted"),
		    ($have[] | select(.evidence.invalid > 0) | "cell \(.name): \(.evidence.invalid) invalid finding(s), first: \(.evidence.invalidFindings[0])"),
		    ($have[] | select(.evidence.missing > 0) | "cell \(.name): \(.evidence.missing) expected measurement(s) missing, first: \(.evidence.missingCases[0])"),
		    ($have[] | select(.recompute.ok and .evidence.coverage != "full") | "cell \(.name): coverage is \(.evidence.coverage // "unknown"), not full: \(.evidence.coverageReasons | join("; "))"),
		    ($have[] | select(.evidence.pending > 0) | "cell \(.name): \(.evidence.pending) case(s) pending, not built: \(.evidence.pendingCases | join(", "))"),
		    ($have[] | select(.recompute.ok and (.evidence.complete != true or .evidence.releaseEvidence != true) and .evidence.pending == 0 and .evidence.invalid == 0 and .evidence.missing == 0 and (.evidence.aborted | not)) | "cell \(.name): its evidence is not complete release evidence") ] as $gaps
		| {schema:"fold/reason-review-matrix/v2", revision:(if $revision != "" then $revision else ($revs[0] // null) end), requiredCells:$required,
		   status:(if ($gaps | length) == 0 and ($have | length) == ($required | length) then "FULL" else "PILOT" end), gaps:$gaps,
		   onlyPendingCases:(($gaps | length) > 0 and ($gaps | all(test("pending, not built")))),
		   cells:$cells,
		   oldEnvelopeComparison:([$have[] | select(.name == "ascii-spread") | .figures[] | {size, oldEnvelope}] | if length == 0 then null else . end),
		   note:"FULL means every required cell is present, on one revision, with a status recomputed from its rows and environment that is complete, full coverage and release evidence, a runner record that finished cleanly and no collection failure. A PILOT names its gaps; neither is a waiver of a budget failure."}'
}

if ((self_test)); then
	work=$(mktemp -d "${TMPDIR:-/tmp}/fold-matrix-test.XXXXXX")
	trap 'rm -rf -- "$work"' EXIT
	rev=$(printf 'a%.0s' {1..40})
	other=$(printf 'b%.0s' {1..40})
	# A stub cell is a run directory (the runner's evidence.json) with the QC-1 evidence under qc1/. Its measurement rows are two and cannot be the full matrix, and its saved status LIES
	# (full, complete, release evidence), as an edited or stale status would: the matrix recomputes the status from the rows, so the lie is a gap and no stub is ever FULL here. The positive
	# control, a real full matrix of consistent cells, is in deploy/digitalocean/qc1-fixtures.sh, which has the generators of the full rows.
	# stub NAME [REVISION] [RUNNER_VERDICT] [RUNNER_RESULT] writes the run directory $work/NAME.
	stub() {
		local name=$1 r=${2:-$rev} verdict=${3:-pass} result=${4:-partial} run=$work/$1 d=$work/$1/qc1 cs ly
		cs=${name%%-*}
		ly=${name#*-}
		rm -rf -- "$run"
		mkdir -p "$d"
		jq -nc --arg r "$r" --arg cs "$cs" --arg ly "$ly" --arg lib "$judge_lib_sha" --arg facts "$judge_facts_sha" \
			'{revision:$r, imageId:"sha256:x", postgresImageId:"sha256:y", postgresVersion:"16.4", limits:{droplet:"2gb"}, daemon:{serverVersion:"27", rootless:false, ncpu:2, memTotal:1}, incomplete:false, maxRequestBytes:6291456,
			  browserRuntime:{chromium:"c", playwright:"p"}, sources:[{path:"deploy/digitalocean/qc1/lib.sh", sha256:$lib}, {path:"deploy/digitalocean/qc1/app-facts.json", sha256:$facts}],
			  settings:{sizes:"10000,100000,1000000", counts:"0,10,100,1000,10000", denseCounts:"100000", deleteCounts:"0,10,100,1000,10000,100000", layout:$ly, charset:$cs, reasonLen:200, targetReasonLen:1000, browser:true, wire:"set-v1"}}' >"$d/environment.json"
		echo '{"complete":true,"coverage":{"kind":"full","reasons":[]},"releaseEvidence":true,"missing":[],"invalid":[],"budgetFailures":[],"pending":[]}' >"$d/evidence-status.json"
		{
			jq -nc --arg ly "$ly" '{status:"measured", item:6, size:10000, case:"delete-requester-10-mixed", layout:$ly, delete:{ms:120, walBytes:5000, appPeakKb:300, dbPeakKb:900, auditRowsAfter:40}, outcome:{textExact:true}}'
			jq -nc '{status:"measured", item:5, size:10000, case:"request-size", request:{capacity:{reasons:18873540}, largest:{bodyBytes:800}, legacyComparison:{idDigits:5, capacityEntries:52760, fitBytes:6291400, fitStatus:"409", overStatus:"413"}}}'
		} >"$d/measurements.jsonl"
		runner_record "$name" "$r" "$verdict" "$result"
	}
	# runner_record NAME REVISION VERDICT RESULT rewrites the run directory's evidence.json, with a summary of the QC-1 step that agrees with the status recomputed from the rows.
	runner_record() {
		local d=$work/$1/qc1 summary
		summary=$(qc1_evidence_status "$d/measurements.jsonl" 10000,100000,1000000 0,10,100,1000,10000 "$d/environment.json" "$(jq -r .revision "$d/environment.json")" 6291456 100000 \
			| jq -c '{complete:(.complete // false), coverage:(.coverage.kind // null), releaseEvidence:(.releaseEvidence // false), missing:(.missing | length), invalid:(.invalid | length), pending:(.pending | length), aborted:((.aborted // null) != null)}')
		jq -nc --arg r "$2" --arg dir "$d" --arg verdict "$3" --arg result "$4" --argjson summary "$summary" \
			'{schema:"fold/reason-review-scale/v1", revision:$r, workingTreeClean:true, result:$result,
			  steps:[{name:"qc1", exit:0, verdict:$verdict, artifacts:$dir, summary:$summary}]}' >"$work/$1/evidence.json"
	}
	# honest NAME rewrites the cell's saved status to what its rows and environment say: a status file that is true.
	honest() {
		local d=$work/$1/qc1
		qc1_evidence_status "$d/measurements.jsonl" 10000,100000,1000000 0,10,100,1000,10000 "$d/environment.json" "$(jq -r .revision "$d/environment.json")" 6291456 100000 >"$d/evidence-status.json.tmp"
		mv "$d/evidence-status.json.tmp" "$d/evidence-status.json"
	}
	# expect LABEL STATUS EXIT PATTERN [--cell ...]: the gap whose text matches PATTERN must be among the record's gaps.
	expect() {
		local label=$1 want=$2 wantrc=$3 pat=$4 rc=0
		shift 4
		bash "${BASH_SOURCE[0]}" --out "$work/out-$label.json" --revision "$rev" "$@" >/dev/null 2>"$work/err-$label" || rc=$?
		[[ $rc == "$wantrc" ]] || { echo "self-test $label: exit $rc, wanted $wantrc: $(cat "$work/err-$label")" >&2; exit 1; }
		[[ $wantrc == 2 ]] && return 0
		[[ $(jq -r .status "$work/out-$label.json") == "$want" ]] || { echo "self-test $label: status $(jq -r .status "$work/out-$label.json"), wanted $want" >&2; exit 1; }
		[[ -z $pat ]] || jq -e --arg p "$pat" '.gaps | any(test($p))' "$work/out-$label.json" >/dev/null || { echo "self-test $label: no gap matches '$pat': $(jq -c .gaps "$work/out-$label.json")" >&2; exit 1; }
	}
	# absent LABEL PATTERN: no gap of the record matches PATTERN (the control that a case is the only thing that changed).
	absent() {
		jq -e --arg p "$2" '.gaps | any(test($p)) | not' "$work/out-$1.json" >/dev/null || { echo "self-test $1: a gap matches '$2' that should not: $(jq -c .gaps "$work/out-$1.json")" >&2; exit 1; }
	}
	for c in "${REQUIRED_CELLS[@]}"; do stub "$c"; done
	all=()
	for c in "${REQUIRED_CELLS[@]}"; do all+=(--cell "$c=$work/$c/qc1"); done

	# The stubs are not the full matrix, whatever their saved status claims: every cell is a PILOT that names why. A saved status that says FULL over two rows is the review's case.
	expect lie PILOT 10 "cell ascii-spread: the saved evidence-status.json differs from the status recomputed" "${all[@]}"
	for c in "${REQUIRED_CELLS[@]}"; do jq -e --arg c "$c" '.gaps | any(test("cell " + $c + ": the saved evidence-status.json differs"))' "$work/out-lie.json" >/dev/null || { echo "self-test lie: $c is not named" >&2; exit 1; }; done
	jq -e '.gaps | any(test("coverage is pilot, not full"))' "$work/out-lie.json" >/dev/null || { echo "self-test lie: the recomputed coverage is not named" >&2; exit 1; }
	jq -e '.gaps | any(test("expected measurement.s. missing"))' "$work/out-lie.json" >/dev/null || { echo "self-test lie: the truncated rows are not named as missing" >&2; exit 1; }
	jq -e '.cells | length == 4 and all(.evidence.coverage == "pilot" and .evidence.releaseEvidence == false and .recompute.savedEqualsRecomputed == false and (.artifacts["measurements.jsonl"] | length) == 64 and .exactness.textExact == 1 and .figures[0].deleteMsMax == 120)' "$work/out-lie.json" >/dev/null || { echo "self-test lie: the cells do not carry the recomputed status, their figures and artifact hashes" >&2; exit 1; }
	jq -e '.oldEnvelopeComparison[0].oldEnvelope.capacityEntries == 52760 and (.cells | all(.budgetFailures.count >= 0))' "$work/out-lie.json" >/dev/null || { echo "self-test lie: the old-envelope comparison is not carried" >&2; exit 1; }
	# the stubs do not trip the checks that are not under test: a controlled runner, judge files and settings
	for g in "runner record" "COLLECTION-FAILED" "lib.sh here" "app-facts.json here" "settings do not name" "settings name another" "marked incomplete" "another revision" "more than one revision" "could not be recomputed"; do absent lie "$g"; done

	# R2: the status file is not a certificate. Edit a stub's saved status to match its rows exactly and it is no longer a mismatch (but still a pilot); a truncated copy is.
	honest ascii-head
	expect honest PILOT 10 "cell ascii-head: coverage is pilot" "${all[@]}"
	jq -e '.gaps | any(test("cell ascii-head: the saved evidence-status.json differs")) | not' "$work/out-honest.json" >/dev/null || { echo "self-test honest: a status equal to the recomputed one is called a mismatch" >&2; exit 1; }
	jq -e '.cells[] | select(.name == "ascii-head") | .recompute.savedEqualsRecomputed == true and .evidence.releaseEvidence == false and .evidence.pending > 0' "$work/out-honest.json" >/dev/null || { echo "self-test honest: the cell does not carry the recomputed status" >&2; exit 1; }
	jq -e '.gaps | any(test("cell ascii-head: .* case.s. pending, not built"))' "$work/out-honest.json" >/dev/null || { echo "self-test honest: the pending legacy cases are not named" >&2; exit 1; }
	stub ascii-head
	# a status that was true of its rows when it was written, and a file that was cut afterwards or an environment that was edited afterwards: neither is the run the status describes
	honest ascii-tail
	head -n 1 "$work/ascii-tail/qc1/measurements.jsonl" >"$work/ascii-tail/qc1/measurements.cut" && mv "$work/ascii-tail/qc1/measurements.cut" "$work/ascii-tail/qc1/measurements.jsonl"
	expect truncated PILOT 10 "cell ascii-tail: the saved evidence-status.json differs" "${all[@]}"
	stub ascii-tail
	honest ascii-tail
	jq -c '.settings.targetReasonLen = 200' "$work/ascii-tail/qc1/environment.json" >"$work/ascii-tail/qc1/environment.json.tmp" && mv "$work/ascii-tail/qc1/environment.json.tmp" "$work/ascii-tail/qc1/environment.json"
	expect envedited PILOT 10 "cell ascii-tail: the saved evidence-status.json differs" "${all[@]}"
	stub ascii-tail
	# a run judged by another judge than the matrix has: its recorded lib.sh differs from the one here
	jq -c '(.sources[] | select(.path | endswith("lib.sh")) | .sha256) = "'"$(printf '0%.0s' {1..64})"'"' "$work/ascii-head/qc1/environment.json" >"$work/ascii-head/qc1/environment.json.tmp" && mv "$work/ascii-head/qc1/environment.json.tmp" "$work/ascii-head/qc1/environment.json"
	expect judge PILOT 10 "cell ascii-head: qc1/lib.sh here is not the file the run recorded" "${all[@]}"
	stub ascii-head
	# an environment marked incomplete, and settings that are not the full matrix's
	jq -c '.incomplete = true' "$work/ascii-head/qc1/environment.json" >"$work/ascii-head/qc1/environment.json.tmp" && mv "$work/ascii-head/qc1/environment.json.tmp" "$work/ascii-head/qc1/environment.json"
	expect incomplete PILOT 10 "cell ascii-head: its environment record is marked incomplete" "${all[@]}"
	stub ascii-head
	jq -c '.settings.sizes = "10000" | .settings.denseCounts = "none"' "$work/ascii-head/qc1/environment.json" >"$work/ascii-head/qc1/environment.json.tmp" && mv "$work/ascii-head/qc1/environment.json.tmp" "$work/ascii-head/qc1/environment.json"
	expect notfull PILOT 10 "cell ascii-head: its settings do not name the full matrix" "${all[@]}"
	stub ascii-head

	# R1: the nested run's collection and exit are part of the cell. Complete rows do not outweigh a failed outer run.
	printf '%s\n' '/work/out (the evidence) could not be copied out' >"$work/ascii-spread/qc1/COLLECTION-FAILED.txt"
	expect collection PILOT 10 "cell ascii-spread: the nested run left COLLECTION-FAILED.txt.*could not be copied out" "${all[@]}"
	absent collection "cell ascii-tail: the nested run left COLLECTION-FAILED"
	: >"$work/ascii-spread/qc1/COLLECTION-FAILED.txt"
	expect collection-empty PILOT 10 "cell ascii-spread: the nested run left COLLECTION-FAILED.txt" "${all[@]}"
	stub ascii-spread
	rm -f "$work/ascii-tail/evidence.json"
	expect norunner PILOT 10 "cell ascii-tail: no runner record" "${all[@]}"
	stub ascii-tail
	stub ascii-tail "$rev" fail fail
	expect runnerfail PILOT 10 "cell ascii-tail: the runner recorded its QC-1 step as fail" "${all[@]}"
	jq -e '.gaps | any(test("cell ascii-tail: the runner finished with result fail"))' "$work/out-runnerfail.json" >/dev/null || { echo "self-test runnerfail: the result of the runner is not named" >&2; exit 1; }
	stub ascii-tail
	stub ascii-tail "$rev" interrupted incomplete
	expect interrupted PILOT 10 "cell ascii-tail: the runner recorded its QC-1 step as interrupted" "${all[@]}"
	stub ascii-tail
	jq -c '.steps[0].exit = 143' "$work/ascii-tail/evidence.json" >"$work/ascii-tail/evidence.json.tmp" && mv "$work/ascii-tail/evidence.json.tmp" "$work/ascii-tail/evidence.json"
	expect exitcode PILOT 10 "cell ascii-tail: the runner recorded its QC-1 step as pass, exit 143" "${all[@]}"
	stub ascii-tail
	jq -c '.steps[0].summary.collectionFailed = true' "$work/ascii-tail/evidence.json" >"$work/ascii-tail/evidence.json.tmp" && mv "$work/ascii-tail/evidence.json.tmp" "$work/ascii-tail/evidence.json"
	expect runnercoll PILOT 10 "cell ascii-tail: the runner recorded a collection failure" "${all[@]}"
	stub ascii-tail
	jq -c '.revision = "'"$other"'"' "$work/ascii-tail/evidence.json" >"$work/ascii-tail/evidence.json.tmp" && mv "$work/ascii-tail/evidence.json.tmp" "$work/ascii-tail/evidence.json"
	expect runnerrev PILOT 10 "cell ascii-tail: the runner record names revision" "${all[@]}"
	stub ascii-tail
	jq -c '.workingTreeClean = false' "$work/ascii-tail/evidence.json" >"$work/ascii-tail/evidence.json.tmp" && mv "$work/ascii-tail/evidence.json.tmp" "$work/ascii-tail/evidence.json"
	expect dirty PILOT 10 "cell ascii-tail: the runner record does not say the working tree was clean" "${all[@]}"
	stub ascii-tail
	jq -c '.steps[0].artifacts = "'"$work"'/elsewhere/qc1"' "$work/ascii-tail/evidence.json" >"$work/ascii-tail/evidence.json.tmp" && mv "$work/ascii-tail/evidence.json.tmp" "$work/ascii-tail/evidence.json"
	expect elsewhere PILOT 10 "cell ascii-tail: the runner record.s qc1 step is for another directory" "${all[@]}"
	stub ascii-tail
	# A stub is a pilot, so its honest summary says releaseEvidence false: the mismatch is the runner claiming release evidence the rows do not hold. (Setting it to false would change nothing.)
	jq -e '.steps[0].summary.releaseEvidence == false' "$work/ascii-tail/evidence.json" >/dev/null || { echo "self-test runnersummary: the stub's honest summary does not say releaseEvidence false" >&2; exit 1; }
	jq -c '.steps[0].summary.releaseEvidence = true' "$work/ascii-tail/evidence.json" >"$work/ascii-tail/evidence.json.tmp" && mv "$work/ascii-tail/evidence.json.tmp" "$work/ascii-tail/evidence.json"
	expect runnersummary PILOT 10 "cell ascii-tail: the runner.s summary of its QC-1 step differs" "${all[@]}"
	stub ascii-tail
	jq -c '.steps += [.steps[0]]' "$work/ascii-tail/evidence.json" >"$work/ascii-tail/evidence.json.tmp" && mv "$work/ascii-tail/evidence.json.tmp" "$work/ascii-tail/evidence.json"
	expect twosteps PILOT 10 "cell ascii-tail: the runner record holds 2 qc1 steps" "${all[@]}"
	stub ascii-tail

	# a missing cell names itself
	expect missing PILOT 10 "cell multibyte-spread: no evidence" --cell "ascii-spread=$work/ascii-spread/qc1" --cell "ascii-tail=$work/ascii-tail/qc1" --cell "ascii-head=$work/ascii-head/qc1"
	# a cell on another revision
	stub ascii-tail "$other"
	expect revision PILOT 10 "more than one revision" "${all[@]}"
	stub ascii-tail
	# a directory that is not a cell's evidence
	mkdir -p "$work/empty"
	expect unreadable PILOT 10 "cell ascii-head: no evidence" --cell "ascii-spread=$work/ascii-spread/qc1" --cell "ascii-tail=$work/ascii-tail/qc1" --cell "ascii-head=$work/empty" --cell "multibyte-spread=$work/multibyte-spread/qc1"
	# a run whose settings are another cell's
	cp "$work/ascii-tail/qc1/environment.json" "$work/ascii-head/qc1/environment.json"
	expect label PILOT 10 "cell ascii-head: its settings name another layout" "${all[@]}"
	stub ascii-head
	# refusals: a bad cell name, an existing output, an output inside the checkout
	expect badname X 2 "" --cell "ascii-sideways=$work/ascii-head/qc1"
	: >"$work/exists.json"
	rc=0
	bash "${BASH_SOURCE[0]}" --out "$work/exists.json" "${all[@]}" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 ]] || { echo "self-test: an existing output file was not refused" >&2; exit 1; }
	rc=0
	bash "${BASH_SOURCE[0]}" --out "$root/matrix-test-output.json" "${all[@]}" >/dev/null 2>&1 || rc=$?
	[[ $rc == 2 && ! -e $root/matrix-test-output.json ]] || { echo "self-test: an output inside the checkout was not refused" >&2; exit 1; }
	echo "reason-review-matrix self-test: ok"
	exit 0
fi

if ((dry_run)); then
	echo "reason-review matrix: DRY RUN. Nothing is read and nothing is written."
	echo "required cells (one scripts/reason-review-scale.sh --only qc1 run each; see docs/agents/testing.md, \"Reason-review scale matrix\"):"
	for c in "${REQUIRED_CELLS[@]}"; do echo "  $c  --cell $c=EVIDENCE_DIR/qc1"; done
	echo "record: the file given to --out (outside this checkout, new)"
	exit 0
fi

[[ -n $out ]] || die "--out FILE is required"
((${#cells[@]} > 0)) || die "at least one --cell NAME=DIR is required"
case $out in /*) ;; *) out=$PWD/$out ;; esac
out=$(realpath -m "$out")
case $out in
"$root" | "$root"/*) die "the record $out is inside the checkout; it must be outside" ;;
esac
[[ ! -e $out ]] || die "the record $out already exists; each run writes a new file"
[[ -d $(dirname "$out") ]] || die "the directory for $out does not exist"
if [[ -z $revision ]]; then
	revision=$(git -C "$root" rev-parse HEAD) || die "cannot read HEAD; give --revision"
fi
[[ $revision =~ ^[0-9a-f]{40}$ ]] || die "--revision is a full 40-character commit id"

cell_docs=()
seen=
for spec in "${cells[@]}"; do
	[[ $spec == *=* ]] || die "--cell is NAME=DIR, not $spec"
	name=${spec%%=*}
	dir=${spec#*=}
	[[ $name =~ ^(ascii|multibyte)-(spread|tail|head)$ ]] || die "cell name $name is not <ascii|multibyte>-<spread|tail|head>"
	[[ " $seen " != *" $name "* ]] || die "cell $name is given twice"
	seen+=" $name"
	cell_docs+=("$(cell_json "$name" "$(realpath -m "$dir")")")
done

assemble "$revision" "${cell_docs[@]}" >"$out.tmp"
mv "$out.tmp" "$out"
jq -e . "$out" >/dev/null || die "the record $out is not valid JSON"
status=$(jq -r .status "$out")
echo "matrix: $status at ${revision:0:12} ($out)"
jq -r '.gaps[] | "  gap: " + .' "$out"
sha256sum "$out"
[[ $status == FULL ]] || exit 10
