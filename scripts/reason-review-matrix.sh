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
# This script reads the evidence directory each run left (environment.json, evidence-status.json, measurements.jsonl, summary.json) and writes one JSON
# record: the revision, the environment and the fixtures of every cell, its timings, audits, WAL and memory, its budget failures and gaps, and the sha256 of
# the artifacts, which stay outside the checkout. It measures nothing and runs no container.
#
#   scripts/reason-review-matrix.sh --out FILE --cell ascii-spread=DIR [--cell ascii-tail=DIR ...] [--revision SHA]
#   scripts/reason-review-matrix.sh --dry-run        print the cells the matrix needs and the record path, write nothing
#   scripts/reason-review-matrix.sh --self-test      this script's own checks against stub evidence (seconds, no Docker)
#
# Status. FULL only when every required cell is present, all name the same revision (the one given, else HEAD of this checkout), each cell's own
# evidence says complete, coverage full, nothing missing, invalid or aborted, and its settings name its own layout and charset. Anything else is PILOT, and
# the record lists every gap by name: an absent cell, a cell of another revision, a cell whose coverage is not full, and the cases a cell reports as pending
# (at the time of writing the legacy discovery and legacy PUT cases, which are not built, so a real matrix is a PILOT until they are). A budget failure
# is a finding for engineering disposition (R4-C06), never a gap in the matrix and never a waiver; the record carries each cell's budget failures by kind.
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

# cell_json NAME DIR prints one cell as JSON. A directory that cannot be read is a cell that is not present, with the reason, never an error.
cell_json() {
	local name=$1 dir=$2 charset layout f artifacts='{}' h
	charset=${name%%-*}
	layout=${name#*-}
	for f in environment.json evidence-status.json measurements.jsonl; do
		if [[ ! -s $dir/$f ]] || { [[ $f != measurements.jsonl ]] && ! jq -e . "$dir/$f" >/dev/null 2>&1; }; then
			jq -nc --arg name "$name" --arg dir "$dir" --arg why "$f is missing or unreadable" '{name:$name, dir:$dir, present:false, why:$why}'
			return 0
		fi
	done
	if ! jq -sc . "$dir/measurements.jsonl" >/dev/null 2>&1; then
		jq -nc --arg name "$name" --arg dir "$dir" '{name:$name, dir:$dir, present:false, why:"measurements.jsonl is not JSON lines"}'
		return 0
	fi
	for f in environment.json evidence-status.json measurements.jsonl summary.json failures.jsonl; do
		[[ -s $dir/$f ]] || continue
		h=$(sha256sum "$dir/$f" | cut -d' ' -f1)
		artifacts=$(jq -c --arg f "$f" --arg h "$h" '. + {($f): $h}' <<<"$artifacts")
	done
	jq -nc --arg name "$name" --arg dir "$dir" --arg charset "$charset" --arg layout "$layout" --argjson artifacts "$artifacts" \
		--slurpfile env "$dir/environment.json" --slurpfile ev "$dir/evidence-status.json" --slurpfile rows <(jq -sc . "$dir/measurements.jsonl") '
		def top(f): [ .[] | f | numbers ] | max;
		def count(p): [ .[] | select(p) ] | length;
		$env[0] as $e | $ev[0] as $v
		| ($rows[0] | map(select(type == "object" and (.status == "measured" or .status == "observation")))) as $m
		| {name:$name, dir:$dir, present:true, revision:$e.revision,
		   settingsAgree: ((($e.settings.charset // "ascii") == $charset) and ($e.settings.layout == $layout)),
		   environment: {imageId:$e.imageId, postgresImageId:$e.postgresImageId, postgresVersion:$e.postgresVersion, limits:$e.limits,
		                 daemon:($e.daemon | {serverVersion, rootless, ncpu, memTotal}), browser:($e.browserRuntime | {chromium, playwright})},
		   fixtures: ($e.settings | {sizes, counts, denseCounts, deleteCounts, layout, charset, reasonLen, targetReasonLen, browser, wire}),
		   artifacts: $artifacts,
		   evidence: {complete:($v.complete // false), coverage:($v.coverage.kind // null), coverageReasons:($v.coverage.reasons // []), releaseEvidence:($v.releaseEvidence // false),
		              missing:(($v.missing // []) | length), invalid:(($v.invalid // []) | length), pending:(($v.pending // []) | length), aborted:(($v.aborted // null) != null),
		              missingCases:[($v.missing // [])[:5][] | "item \(.item) size \(.size) \(.case)"],
		              pendingCases:[($v.pending // [])[] | "item \(.item) \(.case)"] | unique,
		              invalidFindings:[($v.invalid // [])[:5][] | "\(.failure) item \(.item // "?") size \(.size // "?") \(.case // "")"]},
		   budgetFailures: {count:(($v.budgetFailures // []) | length), byKind:(($v.budgetFailures // []) | group_by(.failure) | map({failure:.[0].failure, count:length}))},
		   exactness: {
		     deletionsChecked: ($m | count(.outcome.textExact != null)), textExact: ($m | count(.outcome.textExact == true)), textNotExact: ($m | count(.outcome.textExact == false)),
		     browserDigestChecked: ($m | count(.agreement.digest.checked == true)), browserDigestEqual: ($m | count(.agreement.digest.equal == true)), browserDigestDiffers: ($m | count(.agreement.digest.equal == false)),
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
		    ($have[] | select(.evidence.aborted) | "cell \(.name): the run aborted"),
		    ($have[] | select(.evidence.invalid > 0) | "cell \(.name): \(.evidence.invalid) invalid finding(s), first: \(.evidence.invalidFindings[0])"),
		    ($have[] | select(.evidence.missing > 0) | "cell \(.name): \(.evidence.missing) expected measurement(s) missing, first: \(.evidence.missingCases[0])"),
		    ($have[] | select(.evidence.coverage != "full") | "cell \(.name): coverage is \(.evidence.coverage // "unknown"), not full: \(.evidence.coverageReasons | join("; "))"),
		    ($have[] | select(.evidence.pending > 0) | "cell \(.name): \(.evidence.pending) case(s) pending, not built: \(.evidence.pendingCases | join(", "))"),
		    ($have[] | select(.evidence.complete != true and .evidence.pending == 0 and .evidence.invalid == 0 and .evidence.missing == 0 and (.evidence.aborted | not)) | "cell \(.name): its evidence is not complete") ] as $gaps
		| {schema:"fold/reason-review-matrix/v1", revision:(if $revision != "" then $revision else ($revs[0] // null) end), requiredCells:$required,
		   status:(if ($gaps | length) == 0 then "FULL" else "PILOT" end), gaps:$gaps,
		   onlyPendingCases:(($gaps | length) > 0 and ($gaps | all(test("pending, not built")))),
		   cells:$cells,
		   oldEnvelopeComparison:([$have[] | select(.name == "ascii-spread") | .figures[] | {size, oldEnvelope}] | if length == 0 then null else . end),
		   note:"FULL means every required cell is present, on one revision, complete and full by its own evidence. A PILOT names its gaps; neither is a waiver of a budget failure."}'
}

if ((self_test)); then
	work=$(mktemp -d "${TMPDIR:-/tmp}/fold-matrix-test.XXXXXX")
	trap 'rm -rf -- "$work"' EXIT
	rev=$(printf 'a%.0s' {1..40})
	other=$(printf 'b%.0s' {1..40})
	# stub NAME [REVISION] [COVERAGE] [PENDING_COUNT] writes a cell's evidence directory under $work.
	stub() {
		local name=$1 r=${2:-$rev} cov=${3:-full} pend=${4:-0} d=$work/$1 cs ly
		cs=${name%%-*}
		ly=${name#*-}
		mkdir -p "$d"
		jq -nc --arg r "$r" --arg cs "$cs" --arg ly "$ly" '{revision:$r, imageId:"sha256:x", postgresImageId:"sha256:y", postgresVersion:"16.4", limits:{droplet:"2gb"}, daemon:{serverVersion:"27", rootless:false, ncpu:2, memTotal:1},
			browserRuntime:{chromium:"c", playwright:"p"}, settings:{sizes:"10000", counts:"0,10", denseCounts:"none", deleteCounts:"0,10", layout:$ly, charset:$cs, reasonLen:200, targetReasonLen:1000, browser:true, wire:"set-v1"}}' >"$d/environment.json"
		jq -nc --arg cov "$cov" --argjson pend "$pend" '{complete:($pend == 0 and $cov == "full"), coverage:{kind:$cov, reasons:(if $cov == "full" then [] else ["a pilot"] end)}, releaseEvidence:($pend == 0 and $cov == "full"),
			missing:[], invalid:[], budgetFailures:[{failure:"traversal"}], pending:[range(0; $pend) | {item:4, case:"legacy-discovery-0"}]}' >"$d/evidence-status.json"
		{
			jq -nc '{status:"measured", item:6, size:10000, case:"delete-requester-10-mixed", delete:{ms:120, walBytes:5000, appPeakKb:300, dbPeakKb:900, auditRowsAfter:40}, outcome:{textExact:true}}'
			jq -nc '{status:"measured", item:5, size:10000, case:"request-size", request:{capacity:{reasons:18873540}, largest:{bodyBytes:800}, legacyComparison:{idDigits:5, capacityEntries:52760, fitBytes:6291400, fitStatus:"409", overStatus:"413"}}}'
		} >"$d/measurements.jsonl"
	}
	expect() { # LABEL STATUS EXIT [grep-pattern-in-gaps] -- stub dirs already written; arguments after: --cell ...
		local label=$1 want=$2 wantrc=$3 pat=$4 rc=0
		shift 4
		bash "${BASH_SOURCE[0]}" --out "$work/out-$label.json" --revision "$rev" "$@" >/dev/null 2>"$work/err-$label" || rc=$?
		[[ $rc == "$wantrc" ]] || { echo "self-test $label: exit $rc, wanted $wantrc: $(cat "$work/err-$label")" >&2; exit 1; }
		[[ $wantrc == 2 ]] && return 0
		[[ $(jq -r .status "$work/out-$label.json") == "$want" ]] || { echo "self-test $label: status $(jq -r .status "$work/out-$label.json"), wanted $want" >&2; exit 1; }
		[[ -z $pat ]] || jq -e --arg p "$pat" '.gaps | any(test($p))' "$work/out-$label.json" >/dev/null || { echo "self-test $label: no gap matches '$pat': $(jq -c .gaps "$work/out-$label.json")" >&2; exit 1; }
	}
	for c in "${REQUIRED_CELLS[@]}"; do stub "$c"; done
	all=()
	for c in "${REQUIRED_CELLS[@]}"; do all+=(--cell "$c=$work/$c"); done
	expect full FULL 0 "" "${all[@]}"
	jq -e '.cells | length == 4 and all(.exactness.textExact == 1 and .figures[0].deleteMsMax == 120 and (.artifacts["measurements.jsonl"] | length) == 64)' "$work/out-full.json" >/dev/null || { echo "self-test full: the cells do not carry their figures and artifact hashes" >&2; exit 1; }
	jq -e '.oldEnvelopeComparison[0].oldEnvelope.capacityEntries == 52760' "$work/out-full.json" >/dev/null || { echo "self-test full: the old-envelope comparison is not carried" >&2; exit 1; }
	jq -e '.cells | all(.budgetFailures.count == 1)' "$work/out-full.json" >/dev/null || { echo "self-test full: a budget failure is not carried" >&2; exit 1; }
	# a missing cell names itself
	expect missing PILOT 10 "cell multibyte-spread: no evidence" --cell "ascii-spread=$work/ascii-spread" --cell "ascii-tail=$work/ascii-tail" --cell "ascii-head=$work/ascii-head"
	# a cell on another revision
	stub ascii-tail "$other"
	expect revision PILOT 10 "more than one revision" "${all[@]}"
	stub ascii-tail
	# a cell whose coverage is a pilot
	stub ascii-head "$rev" pilot
	expect pilot PILOT 10 "cell ascii-head: coverage is pilot" "${all[@]}"
	stub ascii-head
	# a cell with pending cases is a pilot that says so, and says that is all
	stub multibyte-spread "$rev" full 2
	expect pending PILOT 10 "2 case.s. pending, not built" "${all[@]}"
	jq -e '.onlyPendingCases == true' "$work/out-pending.json" >/dev/null || { echo "self-test pending: the pending cases are the only gap and the record does not say so" >&2; exit 1; }
	stub multibyte-spread
	# a directory that is not a cell's evidence
	mkdir -p "$work/empty"
	expect unreadable PILOT 10 "cell ascii-head: no evidence" --cell "ascii-spread=$work/ascii-spread" --cell "ascii-tail=$work/ascii-tail" --cell "ascii-head=$work/empty" --cell "multibyte-spread=$work/multibyte-spread"
	# a run whose settings are another cell's
	cp "$work/ascii-tail/environment.json" "$work/ascii-head/environment.json"
	expect label PILOT 10 "cell ascii-head: its settings name another layout" "${all[@]}"
	stub ascii-head
	# refusals: a bad cell name, an existing output, an output inside the checkout
	expect badname X 2 "" --cell "ascii-sideways=$work/ascii-head"
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
	for c in "${REQUIRED_CELLS[@]}"; do echo "  $c  --cell $c=EVIDENCE_DIR"; done
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
