#!/usr/bin/env bash
# Daemon-free regressions for the T07B-2-V QC-1 harness: its pure functions, the
# statement markers (still present in the Go source), the SQL loaders' shape and
# the runner's wiring. No Docker, network or SQL server: nothing here measures
# anything, so a green run says the harness is consistent, not that QC-1 passed.
# shellcheck disable=SC2016 # jq and awk programs keep their variables literal.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
# shellcheck source=deploy/digitalocean/qc1/lib.sh
source "$HERE/qc1/lib.sh"
WORK="$(mktemp -d /tmp/fold-qc1-fixture.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "qc1 fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
refused() { local needle="$1" rc=0; shift; "$@" >"$WORK/out" 2>&1 || rc=$?; [[ "$rc" != 0 ]]; [[ -z "$needle" ]] || grep -qF -- "$needle" "$WORK/out"; }

rc=0; bash "$HERE/qc1/lib.sh" >"$WORK/direct.log" 2>&1 || rc=$?
[[ "$rc" == 1 ]]; grep -q 'source from qc1-measure.sh only' "$WORK/direct.log"

# --- lists -----------------------------------------------------------------------
[[ "$(qc1_list 10,100,1000000 | paste -sd,)" == 10,100,1000000 ]]
[[ "$(qc1_list 007 | paste -sd,)" == 7 ]]
refused 'not a whole number' qc1_list 10,x
refused '' qc1_list ''
refused 'not a whole number' qc1_list 10,,20
[[ "$QC1_SIZES_DEFAULT" == 10000,100000,1000000 ]]
[[ "$QC1_COUNTS_DEFAULT" == 0,10,100,1000,10000 ]]

# --- the gate the sampler watches is the contact gate ------------------------------
grep -q 'const contactLockID int64 = 0x464f4c44434f4e$' "$ROOT/contact_lifecycle.go"
[[ "$QC1_GATE_LOCK_ID" == 0x464f4c44434f4e ]]
read -r class obj <<<"$(qc1_gate_key)"
[[ "$class" == "$(( 0x464f4c44434f4e >> 32 ))" && "$obj" == "$(( 0x464f4c44434f4e & 0xffffffff ))" ]]
[[ "$class" == 4607820 && "$obj" == 1145261902 ]]
# Go's maxRequestBytes, with << binding tighter than +.
[[ "$(qc1_max_request_bytes "$ROOT")" == 6291456 ]]
mkdir "$WORK/fakeroot"; printf 'const maxUploadBytes = 1 << 20\n' >"$WORK/fakeroot/api_profile.go"; printf 'x\n' >"$WORK/fakeroot/main.go"
refused 'cannot read' qc1_max_request_bytes "$WORK/fakeroot"



# --- gate samples: a span, with the sampler's real gaps (V-05) -----------------------------
cat >"$WORK/samples" <<'S'
Sat 07 Nov 2026 (every 0.02s)

1000|0||0|0
1020|0||0|1
1040|1|77|0|1
1060|1|77|0|2
1080|1|77,78|1|1
1100|0||0|1
1120|1|78|0|1
9000|1|99|0|1
S
# spanMs grants first last holders autovacuum maxClients ticks maxGapMs meanGapMs holes
[[ "$(qc1_gate_hold "$WORK/samples" 1000 2000)" == '80 4 1040 1120 2 1 2 7 20 20 1' ]]
[[ "$(qc1_gate_hold "$WORK/samples" 1000 1050)" == '0 1 1040 1040 1 0 1 3 20 20 0' ]]
[[ "$(qc1_gate_hold "$WORK/samples" 1000 1030)" == '0 0 0 0 0 0 1 2 20 20 0' ]]
[[ "$(qc1_gate_hold "$WORK/samples" 2000 3000)" == '0 0 0 0 0 0 0 0 0 0 0' ]]
: >"$WORK/none"; [[ "$(qc1_gate_hold "$WORK/none" 0 9)" == '0 0 0 0 0 0 0 0 0 0 0' ]]
# The cadence is measured, never assumed: a sampler that stalled for half a second says so.
printf '%s\n' '1000|0||0|1' '1020|0||0|1' '1520|1|5|0|1' '1540|1|5|0|1' >"$WORK/gappy"
[[ "$(qc1_gate_hold "$WORK/gappy" 0 9999)" == '20 2 1520 1540 1 0 1 4 500 180 0' ]]
# Lines of the old two-field form are not samples: a sampler of the wrong shape reads as dead.
printf '1000|1\n1020|1\n' >"$WORK/oldform"; [[ "$(qc1_gate_hold "$WORK/oldform" 0 9999)" == '0 0 0 0 0 0 0 0 0 0 0' ]]
# The rows say "span", not "hold": the old field name is gone from the runner and the lib.
if grep -n 'gateHoldMs\|GATE_HOLD_MS' "$HERE/qc1-measure.sh" "$HERE/qc1/lib.sh"; then echo 'qc1: a gate figure is still called a hold' >&2; exit 1; fi

# --- the scheduler (V-05): FOLD_WORKERS=0 does not stop it -----------------------------------------
# The app starts its scheduler whatever the worker count (jobs_daily.go startJobs).
grep -q 'go func() {' "$ROOT/jobs_daily.go"; grep -q 'queueHourly(a.DB, hourly, now)' "$ROOT/jobs_daily.go"; grep -q 'a.Config.Workers <= 0' "$ROOT/jobs_daily.go"
sed -n '/^func (a \*App) startJobs/,/^}/p' "$ROOT/jobs_daily.go" | sed -n '/Workers <= 0/,/runner.Start/p' | grep -q 'WARNING: FOLD_WORKERS=0'
# hour = 3600000 ms. A window is contaminated in the 15 s after the app started and in the first 5 s of a full hour.
APP=1700000000000                      # app started
HOUR=$(( (APP / 3600000 + 1) * 3600000 ))   # the next full hour
[[ "$(qc1_scheduler_overlap $((APP + 20000)) $((APP + 40000)) $APP)" == false ]]
[[ "$(qc1_scheduler_overlap $((APP + 1000)) $((APP + 2000)) $APP)" == true ]]
[[ "$(qc1_scheduler_overlap $((APP + 14000)) $((APP + 30000)) $APP)" == true ]]
[[ "$(qc1_scheduler_overlap $((HOUR - 60000)) $((HOUR - 10000)) $APP)" == false ]]
[[ "$(qc1_scheduler_overlap $((HOUR - 10000)) $((HOUR + 100)) $APP)" == true ]]
[[ "$(qc1_scheduler_overlap $((HOUR + 1000)) $((HOUR + 2000)) $APP)" == true ]]
[[ "$(qc1_scheduler_overlap $((HOUR + 5000)) $((HOUR + 9000)) $APP)" == true ]]
[[ "$(qc1_scheduler_overlap $((HOUR + 5001)) $((HOUR + 9000)) $APP)" == false ]]
[[ "$(qc1_scheduler_overlap $((HOUR - 100000)) $((HOUR + 100000)) $APP)" == true ]]
[[ "$(qc1_scheduler_overlap $((HOUR + 7200000 - 50000)) $((HOUR + 7200000 + 50000)) $APP)" == true ]]
# The waiting helper clears both edges and is zero when the window is already clear.
[[ "$(qc1_scheduler_wait $((APP + 20000)) 30000 $APP)" == 0 ]]
w="$(qc1_scheduler_wait $((APP + 1000)) 10000 $APP)"; [[ "$w" == 14001 ]]
w="$(qc1_scheduler_wait $((HOUR - 20000)) 60000 $APP)"; [[ "$(qc1_scheduler_overlap $((HOUR - 20000 + w)) $((HOUR - 20000 + w + 60000)) $APP)" == false ]]
(( w >= 25001 ))
w="$(qc1_scheduler_wait $((HOUR + 2000)) 1000 $APP)"; [[ "$w" == 3001 ]]
# A long window cannot fit between edges: it waits past the edge it would touch, then the check still judges it.
w="$(qc1_scheduler_wait $((HOUR - 1000)) 7200000 $APP)"; (( w >= 6001 ))
# The runner uses both: it waits first and judges afterwards, and a window with overlap or rows is invalid.
grep -q 'qc1_scheduler_wait "\$(ms)" "\$need" "\$APP_STARTED_MS"' "$HERE/qc1-measure.sh"
grep -q 'SCHEDULER_OVERLAP="\$(qc1_scheduler_overlap "\$OP_START" "\$OP_END" "\$APP_STARTED_MS")"' "$HERE/qc1-measure.sh"
grep -q "FROM jobs WHERE created_at" "$HERE/qc1-measure.sh"
grep -q 'APP_STARTED_MS="\$(ms)"' "$HERE/qc1-measure.sh"

# --- expected outcomes (V-04) -------------------------------------------------------------------------
[[ "$(qc1_expected_split keep 10)" == '10 0' && "$(qc1_expected_split remove 10)" == '0 10' ]]
[[ "$(qc1_expected_split mixed 10)" == '5 5' && "$(qc1_expected_split mixed 7)" == '3 4' && "$(qc1_expected_split mixed 1)" == '0 1' && "$(qc1_expected_split mixed 0)" == '0 0' ]]
refused 'unknown mode' qc1_expected_split sometimes 3
# The split agrees with the bodies the harness builds: mixed removes the first ceil(k/2).
jq -nc '[range(0; 7) | {requestId: (. + 1 | tostring), reviewHash: ("a" * 64)}]' >"$WORK/seven.json"
[[ "$(qc1_delete_body_n mixed "$WORK/seven.json" 7 "" | jq -r '.reasonReview.choices')" == "qhU=" ]]   # 2,2,2,2,1,1,1 -> 0xaa, 0x15

# --- what a traversal cost (V-03): wall clock, request sum, client share -------------------
[[ "$(qc1_traversal_times 1000 800 5)" == '1000 800 200' ]]
[[ "$(qc1_traversal_times 1000 1000 5)" == '1000 1000 0' ]]
# Each request time is rounded to a millisecond: a sum may pass the wall clock by one per unit.
[[ "$(qc1_traversal_times 1000 1003 5)" == '1000 1003 0' ]]
refused 'outlast' qc1_traversal_times 1000 1006 5
refused 'whole numbers' qc1_traversal_times 1000 x 5
refused 'whole numbers' qc1_traversal_times '' 1 1
[[ "$(qc1_traversal_times 0 0 0)" == '0 0 0' ]]
# The runner takes the page-load figure from the wall clock, never from the request sum.
grep -q 'TR_TOTAL_MS TR_REQUEST_MS TR_CLIENT_MS <<<"\$times"' "$HERE/qc1-measure.sh"
grep -q 'started="\$(ms)"' "$HERE/qc1-measure.sh"
grep -q 'times="\$(qc1_traversal_times "\$((ended-started))" "\$total" "\$n")"' "$HERE/qc1-measure.sh"
grep -q 'jq -sc . "\$WORK/items.jsonl" >"\$out"' "$HERE/qc1-measure.sh"
slurp_line="$(grep -n 'jq -sc . "\$WORK/items.jsonl" >"\$out"' "$HERE/qc1-measure.sh" | head -n 1 | cut -d: -f1)"
ended_line="$(grep -n '^  ended="\$(ms)"$' "$HERE/qc1-measure.sh" | head -n 1 | cut -d: -f1)"
[[ -n "$slurp_line" && -n "$ended_line" && "$slurp_line" -lt "$ended_line" ]]
# Every instrument a row can carry, healthy. A row is built from these and then broken one field at a time.
INSTR='"tableRowsBefore":1010,"tableRowsAfter":1010,"auditRowsBefore":10,"auditRowsAfter":20,"auditBytesBefore":1000,"auditBytesAfter":2000,"auditActionsBefore":{"x.view":5},"auditActionsAfter":{"x.view":15,"add.reason_kept":5},"auditActionsDelta":{"x.view":10,"add.reason_kept":5},"walBytes":100,"walControlBytes":10,"appPeakKb":5,"appHwmReset":true,"dbPeakKb":7,"dbMemSamples":50,"gateSpanMs":0,"gateGrantTicks":0,"gateTicks":50,"gateHolders":0,"gateMaxGapMs":25,"gateMeanGapMs":22,"gateHoles":0,"autovacuumWorkers":0,"maxActiveClients":1,"commitCalls":1,"commitMsMax":1,"commitMsTotal":1,"schedulerOverlap":false,"schedulerRows":0'
good_tr="{\"units\":5,\"slowestUnitMs\":200,\"totalMs\":1000,\"requestMs\":800,\"clientMs\":200,\"items\":3,\"lastStatus\":\"200\",$INSTR}"
row() { jq -nc --argjson t "$1" '{status:"measured",item:3,case:"discovery-requester-10",size:100,workers:0,reasons:3,expect:{items:3},traversal:$t}'; }
row "$good_tr" >"$WORK/acc.jsonl"; [[ -z "$(qc1_judge "$WORK/acc.jsonl" 6291456)" ]] || { qc1_judge "$WORK/acc.jsonl" 6291456 >&2; exit 1; }
# A traversal whose pieces do not add up is invalid; one that does is not.
acc() { row "$1" >"$WORK/acc.jsonl"; qc1_judge "$WORK/acc.jsonl" 6291456 | jq -r 'select(.failure == "accounting") | .class'; }
[[ "$(acc "$(jq -c '.requestMs = 1006 | .clientMs = 0' <<<"$good_tr")")" == invalid ]]
[[ "$(acc "$(jq -c '.clientMs = 500' <<<"$good_tr")")" == invalid ]]
[[ "$(acc "$(jq -c '.clientMs = -1' <<<"$good_tr")")" == invalid ]]
[[ -z "$(acc "$(jq -c '.requestMs = 1003 | .clientMs = 0' <<<"$good_tr")")" ]]
# A budget is judged on the wall clock: 150 s of wall time with 100 s of requests fails the 2-minute budget.
row "$(jq -c '.totalMs = 150000 | .requestMs = 100000 | .clientMs = 50000 | .gateTicks = 2000 | .dbMemSamples = 2000' <<<"$good_tr")" >"$WORK/acc.jsonl"
[[ "$(qc1_judge "$WORK/acc.jsonl" 6291456 | jq -r 'select(.failure == "traversal") | .class + ":" + (.ms|tostring)')" == budget:150000 ]]
# The page load an item-7 sitting starts from is that wall clock too.
grep -q 'pageLoadMs:\.traversal\.totalMs' "$HERE/qc1-measure.sh"

# --- identity of the sources ---------------------------------------------------------------------
SRC="$WORK/srcrepo"; mkdir -p "$SRC/deploy/digitalocean/qc1" "$SRC/web/src/lib"
git init -q "$SRC"
git -C "$SRC" config user.name fixture; git -C "$SRC" config user.email fixture@example.invalid; git -C "$SRC" config commit.gpgsign false; git -C "$SRC" config core.hooksPath /dev/null
for p in "${QC1_SOURCE_FILES[@]}"; do printf 'content of %s\n' "$p" >"$SRC/$p"; done
git -C "$SRC" add -A; git -C "$SRC" commit -qm sources
hashes="$(qc1_source_hashes "$SRC")"
[[ "$(jq 'length' <<<"$hashes")" == "${#QC1_SOURCE_FILES[@]}" && "$(jq 'all(.matches)' <<<"$hashes")" == true ]]
[[ "$(jq -r '.[0].sha256' <<<"$hashes")" == "$(sha256sum <"$SRC/${QC1_SOURCE_FILES[0]}" | cut -d' ' -f1)" ]]
[[ "$(jq -r '.[1].gitBlob' <<<"$hashes")" == "$(git -C "$SRC" rev-parse "HEAD:${QC1_SOURCE_FILES[1]}")" ]]
# A source changed since HEAD, even by one byte, or missing, is refused and named.
printf 'x' >>"$SRC/${QC1_SOURCE_FILES[2]}"
rc=0; hashes="$(qc1_source_hashes "$SRC" 2>/dev/null)" || rc=$?
[[ "$rc" != 0 && "$(jq -r '[.[] | select(.matches | not) | .path] | join(",")' <<<"$hashes")" == "${QC1_SOURCE_FILES[2]}" ]]
git -C "$SRC" checkout -q -- "${QC1_SOURCE_FILES[2]}"
rm "$SRC/${QC1_SOURCE_FILES[3]}"
refused 'is missing' qc1_source_hashes "$SRC"
# Every file the runner reads to measure is in the list.
for p in "${QC1_SOURCE_FILES[@]}"; do [[ -f "$ROOT/$p" ]]; done
grep -q 'qc1/filler.sql' "$HERE/qc1-measure.sh"; grep -q 'qc1/scenario.sql' "$HERE/qc1-measure.sh"

# --- WAL bytes between LSNs ---------------------------------------------------------
[[ "$(qc1_lsn_bytes 0/1000000 0/2000000)" == 16777216 ]]
[[ "$(qc1_lsn_bytes 0/FFFFFFFF 1/0)" == 1 ]]
[[ "$(qc1_lsn_bytes 2F/A0000000 30/0)" == 1610612736 ]]
refused invalid qc1_lsn_bytes 0/2000000 0/1000000
refused invalid qc1_lsn_bytes nonsense 0/1

# --- bodies: the set-v1 envelope the panel sends (V-02, T07B-2 scaling F2) -----------------------------------
ID80="$(printf 'ab%.0s' {1..40})"
[[ ${#ID80} == 80 ]]
# 40 entries shaped like real items: ascending ids of varying width, 64-hex hashes, and fields a body never carries.
jq -nc '[range(0; 40) | {requestId: ((. * 997 + 1) | tostring), reviewHash: (((. + 16) | tostring | .[0:2]) * 32 | .[0:64]), role:"subject", reason:"never in a body"}]' >"$WORK/real-items.json"
jq -e 'all(.reviewHash | test("^[0-9a-f]{64}$"))' "$WORK/real-items.json" >/dev/null
body() { qc1_delete_body_n "$@"; }
# The key order and compactness are those of AdminUsers.remove and JSON.stringify; the review is four keys, no per-reason list.
[[ "$(body keep "$WORK/real-items.json" 2 "$ID80" | jq -c 'keys_unsorted')" == '["successors","hostSuccessors","attemptId","reasonReview"]' ]]
[[ "$(body keep "$WORK/real-items.json" 2 "$ID80" | jq -c '.reasonReview | keys_unsorted')" == '["format","count","digest","choices"]' ]]
if body keep "$WORK/real-items.json" 40 "$ID80" | grep -q '"items"\|requestId\|reviewHash'; then echo 'qc1: a set-v1 body carries per-reason entries' >&2; exit 1; fi
# Independent oracle: the digest and the choices computed here with xxd and sha256sum from the format alone (label, 0x00, uint64 count, then
# int64 id and the raw 32-byte hash of each item in the order given), not by the helper under test.
oracle_digest() { # items-file n
  { printf '%s' 'fold:erasure-reason-set:v1' | xxd -p | tr -d '\n'; printf '00%016x' "$2"
    jq -r --argjson n "$2" '.[0:$n][] | "\(.requestId) \(.reviewHash)"' "$1" | while read -r id h; do printf '%016x%s' "$id" "$h"; done; } | xxd -r -p | sha256sum | cut -d' ' -f1
}
for n in 1 2 3 4 5 9 40; do
  want="$(oracle_digest "$WORK/real-items.json" "$n")"
  [[ "$(body mixed "$WORK/real-items.json" "$n" "$ID80" | jq -r '.reasonReview.digest')" == "$want" ]] || { echo "qc1: digest for $n differs from the oracle" >&2; exit 1; }
  [[ "$(body mixed "$WORK/real-items.json" "$n" "$ID80" | jq -r '.reasonReview.count')" == "$n" ]]
done
# Choices: item i at bits 2*(i mod 4) of byte i/4; mixed removes the first ceil(n/2). 5 items: remove x3, keep x2 -> bytes 0x26 (2,2,2,... ) check by hand.
# i0=2 i1=2 i2=2 i3=1 -> 2 | 2<<2 | 2<<4 | 1<<6 = 2+8+32+64 = 106 = 0x6a; i4=1 -> 1. So 6a 01 -> "agE=".
[[ "$(body mixed "$WORK/real-items.json" 5 "$ID80" | jq -r '.reasonReview.choices')" == 'agE=' ]]
[[ "$(body keep "$WORK/real-items.json" 4 "$ID80" | jq -r '.reasonReview.choices')" == 'VQ==' ]]    # 01 01 01 01 = 0x55
[[ "$(body remove "$WORK/real-items.json" 4 "$ID80" | jq -r '.reasonReview.choices')" == 'qg==' ]]  # 10 10 10 10 = 0xaa
# No reasons: no reasonReview. No id: no attemptId. Maps given: carried, in place.
[[ "$(body keep "$WORK/real-items.json" 0 "$ID80")" == '{"successors":{},"hostSuccessors":{},"attemptId":"'"$ID80"'"}' ]]
[[ "$(body keep "$WORK/real-items.json" 0 "")" == '{"successors":{},"hostSuccessors":{}}' ]]
[[ "$(body keep "$WORK/real-items.json" 0 "$ID80" '{"12":3}' '{"7":8}')" == '{"successors":{"12":3},"hostSuccessors":{"7":8},"attemptId":"'"$ID80"'"}' ]]
# An items list that is not strictly ascending, or with a hash that is not 64 lowercase hex, is refused (the server's order is what is under test).
jq -c 'reverse' "$WORK/real-items.json" >"$WORK/desc-items.json"
refused 'ascending' body keep "$WORK/desc-items.json" 3 "$ID80"
jq -c '.[0].reviewHash = "h"' "$WORK/real-items.json" >"$WORK/bad-items.json"
refused '64 lowercase hex' body keep "$WORK/bad-items.json" 3 "$ID80"
# The shape is the source's: the envelope in web/src/api.ts and the 80-hex id.
grep -qF 'body: { successors, hostSuccessors, ...(attemptId ? { attemptId } : {}), ...(reasonReview ? { reasonReview } : {}) }' "$ROOT/web/src/api.ts"
grep -qF '/^[0-9a-f]{80}$/.test(res.attemptId)' "$ROOT/web/src/api.ts"
grep -qF 'AdminUsers.remove(target.id, picks, hostPicks, id, reasonReview)' "$ROOT/web/src/components/DeleteUserPanel.tsx"
# The helper agrees with the product's shared vectors, which are also run by the web and Go tests.
[[ "$(qc1_setv1 selftest --vectors "$ROOT/web/src/lib/reasonSetVectors.json")" == 'ok 4' ]]
# Mutations: the last choice code replaced (same length), one digest digit changed, the count one higher; nothing else moves.
body mixed "$WORK/real-items.json" 6 "$ID80" >"$WORK/b6.json"
qc1_final_choice_body "$WORK/b6.json" maybe >"$WORK/b6m.json"
[[ "$(jq -r '.reasonReview.choices' "$WORK/b6.json")" != "$(jq -r '.reasonReview.choices' "$WORK/b6m.json")" ]]
[[ "$(jq -c 'del(.reasonReview.choices)' "$WORK/b6.json")" == "$(jq -c 'del(.reasonReview.choices)' "$WORK/b6m.json")" ]]
(( $(wc -c <"$WORK/b6m.json") == $(wc -c <"$WORK/b6.json") ))
# 6 items, last (index 5, byte 1 bits 2..3) 1 -> 3: bytes 6a 05 -> 6a 0d.
[[ "$(jq -r '.reasonReview.choices' "$WORK/b6.json")" == 'agU=' && "$(jq -r '.reasonReview.choices' "$WORK/b6m.json")" == 'ag0=' ]]
qc1_changed_set_body "$WORK/b6.json" >"$WORK/b6d.json"
[[ "$(jq -c 'del(.reasonReview.digest)' "$WORK/b6.json")" == "$(jq -c 'del(.reasonReview.digest)' "$WORK/b6d.json")" ]]
[[ "$(jq -r '.reasonReview.digest' "$WORK/b6d.json")" =~ ^[0-9a-f]{64}$ && "$(jq -r '.reasonReview.digest' "$WORK/b6d.json")" != "$(jq -r '.reasonReview.digest' "$WORK/b6.json")" ]]
qc1_extra_count_body "$WORK/b6.json" >"$WORK/b6c.json"
[[ "$(jq -r '.reasonReview.count' "$WORK/b6c.json")" == 7 && "$(jq -c 'del(.reasonReview.count)' "$WORK/b6.json")" == "$(jq -c 'del(.reasonReview.count)' "$WORK/b6c.json")" ]]

# --- exact lengths and the capacity, from the format (V-02) ----------------------------------------------------
bytes_of() { body mixed "$WORK/real-items.json" "$1" "$ID80" | wc -c; }
(( $(bytes_of 1) <= $(bytes_of 9) && $(bytes_of 9) < $(bytes_of 40) ))   # three bytes and one still base64 to four characters
for n in 1 2 3 4 5 9 10 40; do
  [[ "$(qc1_body_length "$n" "$ID80")" == "$(bytes_of "$n")" ]] || { echo "qc1: the computed length for $n differs from the body" >&2; exit 1; }
done
# An independent formula: fixed bytes + digits(n) + 4 * ceil(ceil(n / 4) / 3), the fixed part read from the one-reason body.
fixed="$(( $(bytes_of 1) - 1 - 4 ))"
set1_len() { local n="$1" bytes=$(( ($1 + 3) / 4 )); echo $(( fixed + ${#n} + 4 * ((bytes + 2) / 3) )); }
for n in 1 4 5 12 13 100 999 1000 54321 18873540 99999999; do
  (( n > 40 )) || [[ "$(set1_len "$n")" == "$(bytes_of "$n")" ]] || { echo "qc1: formula differs at $n" >&2; exit 1; }
done
# The capacity at the real cap: the claimed count fits, one more does not (by the formula; no such body is built).
read -r capn capat capnext <<<"$(qc1_body_capacity 6291456 "$ID80")"
[[ "$capn" == 18873540 ]] || { echo "qc1: capacity $capn" >&2; exit 1; }
(( $(set1_len "$capn") == capat && capat <= 6291456 && $(set1_len "$((capn + 1))") == capnext && capnext > 6291456 ))
[[ "$capat $capnext" == "$(set1_len "$capn") $(set1_len "$((capn + 1))")" ]]
# A cap between two sizes takes the smaller, a cap at a size takes it; the whole set of 40 fits a generous cap.
for want in 1 17 33 39; do
  cap="$(bytes_of "$want")"
  read -r got at next <<<"$(qc1_body_capacity "$cap" "$ID80")"
  (( at <= cap && next > cap && got >= want )) || { echo "qc1: capacity for cap $cap: $got $at $next, want at least $want" >&2; exit 1; }
done
# The over-cap body is syntactically valid set-v1, one reason over the capacity, and over the cap by a few bytes.
qc1_oversize_body 6291456 "$ID80" >"$WORK/over.json"
(( $(wc -c <"$WORK/over.json") > 6291456 && $(wc -c <"$WORK/over.json") == capnext ))
[[ "$(jq -r '.reasonReview.count' "$WORK/over.json")" == "$((capn + 1))" && "$(jq -r '.reasonReview.format' "$WORK/over.json")" == set-v1 ]]
[[ "$(jq -r '.reasonReview.digest' "$WORK/over.json")" =~ ^[0-9a-f]{64}$ ]]
# The OLD items envelope, as a comparison only: synthetic entries of a stated id width, exact lengths, the pilots' kind of limit.
for e in 1 2 17; do
  qc1_legacy_body "$e" 5 "$ID80" >"$WORK/legacy.json"
  [[ "$(wc -c <"$WORK/legacy.json")" == "$(qc1_setv1 legacy-length --entries "$e" --digits 5 --attempt "$ID80")" ]]
  [[ "$(jq -c '.reasonReview.items | length' "$WORK/legacy.json")" == "$e" && "$(jq -r '.reasonReview.items[0] | keys_unsorted | join(",")' "$WORK/legacy.json")" == requestId,reviewHash,choice ]]
done
read -r on oat onext <<<"$(qc1_legacy_capacity 6291456 5 "$ID80")"
(( on > 40000 && on < 70000 && oat <= 6291456 && onext > 6291456 ))
[[ "$oat" == "$(qc1_setv1 legacy-length --entries "$on" --digits 5 --attempt "$ID80")" && "$onext" == "$(qc1_setv1 legacy-length --entries "$((on + 1))" --digits 5 --attempt "$ID80")" ]]
# The runner's request-size case: the real largest body is sent with its refusals first, the oversize body is syntactically valid, the old form
# is labelled synthetic, and the capacity is computed, never sent. Nothing of the old probe-and-fit procedure remains.
for pat in \
  'qc1_body_capacity "$cap" "$id80"' 'refusal_case parse' 'refusal_case count' 'refusal_case changed' 'qc1_final_choice_body "$WORK/refusal.base" maybe' \
  'qc1_extra_count_body "$WORK/refusal.base"' 'qc1_changed_set_body "$WORK/refusal.base"' 'qc1_oversize_body "$cap" "$id"' 'delete_with "$t" mixed "$WORK/req-items.json"' \
  'load_scenario "$n" "$K" "$t" both "$CPS" tail' 'fixedBytesWithEmptyReview' 'attemptIdLength:80' 'legacyComparison' 'synthetic:true' 'overCap:{' 'capacity:{reasons:$capN' \
  'parseRefusal:$parse' 'countMismatch:$count' 'changedSet:$changed' 'format:"set-v1"'; do
  grep -qF -- "$pat" "$HERE/qc1-measure.sh" || { echo "qc1: the request-size case lost: $pat" >&2; exit 1; }
done
for gone in qc1_fit_entries qc1_probe_entries qc1_invalid_body workflowOverflow maxEntriesInBudget; do
  if grep -n -- "$gone" "$HERE/qc1-measure.sh" "$HERE/qc1/lib.sh"; then echo "qc1: $gone is back" >&2; exit 1; fi
done
# The refusals come before the real send, and the legacy PUT stays a pending item.
refuse_line="$(grep -n '^  refusal_case parse' "$HERE/qc1-measure.sh" | head -n 1 | cut -d: -f1)"; send_line="$(grep -n '^  delete_with "\$t" mixed "\$WORK/req-items.json"' "$HERE/qc1-measure.sh" | head -n 1 | cut -d: -f1)"
[[ -n "$refuse_line" && -n "$send_line" && "$refuse_line" -lt "$send_line" ]]
grep -q 'case:"legacy-put"' "$HERE/qc1-measure.sh"

# The file is exactly the bytes sent: no trailing newline, so wc -c is the Content-Length the 413 check sees.
body keep "$WORK/real-items.json" 0 "$ID80" >"$WORK/b0.json"
[[ "$(wc -c <"$WORK/b0.json")" == "$(printf '%s' '{"successors":{},"hostSuccessors":{},"attemptId":"'"$ID80"'"}' | wc -c)" ]]
[[ "$(tail -c 1 "$WORK/b0.json")" == "}" ]]
# --- reasons per sitting (item 7) ----------------------------------------------------------------------
rows='[{"direction":"requester","reasons":10,"pageLoadMs":2000},{"direction":"requester","reasons":100,"pageLoadMs":5000},{"direction":"requester","reasons":1000,"pageLoadMs":20000},{"direction":"subject","reasons":10,"pageLoadMs":2000},{"direction":"subject","reasons":50,"pageLoadMs":3000}]'
a="$(qc1_sitting_assessment "$rows" 10 15)"
[[ "$(jq -r '.reasonsPerSitting' <<<"$a")" == 90 ]]
[[ "$(jq -c '.firstLongerThanSitting' <<<"$a")" == '{"requester":100,"subject":null}' ]]
[[ "$(jq -r '.perCount[0].estimatedMinutes' <<<"$a")" == 1.7 ]]
[[ "$(jq -c '[.perCount[] | .longerThanSitting]' <<<"$a")" == '[false,true,true,false,false]' ]]
[[ "$(jq -r '.assumption' <<<"$a")" == *'assumptions, not measurements'* && "$(jq -r '.secondsPerReason, .sittingMinutes' <<<"$a" | paste -sd,)" == 10,15 ]]
# Faster reading or a longer sitting moves the limit; the measured page load alone can cross it.
[[ "$(qc1_sitting_assessment "$rows" 1 15 | jq -c '.firstLongerThanSitting')" == '{"requester":1000,"subject":null}' ]]
[[ "$(qc1_sitting_assessment "$rows" 1 60 | jq -c '.firstLongerThanSitting')" == '{"requester":null,"subject":null}' ]]
[[ "$(qc1_sitting_assessment '[{"direction":"requester","reasons":1,"pageLoadMs":1000000}]' 1 15 | jq -c '.firstLongerThanSitting.requester')" == 1 ]]
[[ "$(qc1_sitting_assessment "$rows" 10 60 | jq -r '.reasonsPerSitting')" == 360 ]]
grep -q 'qc1_sitting_assessment "\$rows" "\$SECONDS_PER_REASON" "\$SITTING_MINUTES"' "$HERE/qc1-measure.sh"


# --- fixtures for judging and evidence ---------------------------------------------------------------------
HEAD_FAKE=1111111111111111111111111111111111111111
H64=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
IMG="sha256:$H64"; PGIMG="sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
gen_env() {
  jq -nc --arg head "$HEAD_FAKE" --arg img "$IMG" --arg pg "$PGIMG" --arg c1 "$(printf 'c%.0s' {1..64})" --arg c2 "$(printf 'd%.0s' {1..64})" --arg c3 "$(printf 'e%.0s' {1..64})" --arg f64 "$(printf 'f%.0s' {1..64})" \
    --argjson paths "$(printf '%s\n' "${QC1_SOURCE_FILES[@]}" | jq -R . | jq -sc .)" '
    {revision:$head, imageId:$img, appImageId:$img, appContainerId:$c1,
     appContainers:[{id:$c1, imageId:$img, workers:0, memoryLimitBytes:629145600}, {id:$c3, imageId:$img, workers:2, memoryLimitBytes:629145600}],
     postgresImageId:$pg, dbImageId:$pg, dbContainerId:$c2, postgresVersion:"16.4", limitsApplied:true,
     daemon:{rootless:true, sharedDaemon:false}, settings:{workers:0, autovacuum:"off", browser:true}, browserRuntime:{ok:true, chromium:"140.0.7339.0", playwright:"1.64.1"},
     sources:($paths | map({path:., sha256:$f64, gitBlob:"x", headBlob:"x", matches:true})),
     calibration:{gate:{ok:true}, dbMemory:{ok:true, samples:9}, appMemory:{ok:true, hwmReset:true}, wal:{ok:true}, commit:{ok:true}}}'
}
gen_env >"$WORK/env.json"

[[ -z "$(qc1_environment_problems "$WORK/env.json" "$HEAD_FAKE")" ]]
env_bad() { # name jq-filter needle: the mutated record is refused and the problem is named
  jq -c "$2" "$WORK/env.json" >"$WORK/env.$1"
  qc1_environment_problems "$WORK/env.$1" "$HEAD_FAKE" | grep -qF -- "$3" || { echo "environment mutation $1 not named ($3): $(qc1_environment_problems "$WORK/env.$1" "$HEAD_FAKE")" >&2; exit 1; }
}
[[ -n "$(qc1_environment_problems "$WORK/env.json" 2222222222222222222222222222222222222222)" ]]
env_bad head '.revision = "2222222222222222222222222222222222222222"' 'is not HEAD'
env_bad tag '.imageId = "latest"' 'imageId is not an immutable'
env_bad swapped '.appImageId = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"' 'the app container runs'
env_bad second '.appContainers[1].imageId = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"' 'ran sha256:cccc'
env_bad nocontainers '.appContainers = []' 'no app container is recorded'
env_bad pgtag '.postgresImageId = "postgres:16-alpine"' 'postgresImageId is not an immutable'
env_bad pgswap '.dbImageId = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"' 'the database container runs'
env_bad pgver '.postgresVersion = "15.6"' 'postgres is not 16'
env_bad cid '.dbContainerId = "db"' 'dbContainerId is not a container id'
env_bad src '.sources[2].matches = false' 'differs from HEAD'
env_bad srcnames '.sources = .sources[:3]' 'exactly the measurement files'
env_bad srchash '.sources[0].sha256 = "x"' 'has no sha256'
env_bad shared '.daemon.sharedDaemon = true' 'shared with other containers'
env_bad rootless '.daemon.rootless = "yes"' 'rootless is not recorded'
env_bad workers '.settings.workers = 2' 'workers off'
env_bad vacuum '.settings.autovacuum = "on"' 'autovacuum'
env_bad limits '.limitsApplied = false' 'limits were not applied'
env_bad gate '.calibration.gate.ok = false' 'gate sampler'
env_bad dbmem '.calibration.dbMemory.ok = false' 'database memory'
env_bad appmem '.calibration.appMemory.ok = false' 'app process memory'
env_bad reset '.calibration.appMemory.hwmReset = false' 'peak-memory reset'
env_bad wal '.calibration.wal.ok = false' 'WAL position'
env_bad commit '.calibration.commit.ok = false' 'commit statistics'
refused '' qc1_environment_problems "$WORK/no-such-env.json" "$HEAD_FAKE"
env_bad memsparse '.calibration.dbMemory.samples = 2' 'too sparse'

HDEL="\"status\":\"200\",\"ms\":100,$INSTR"
okout='{"dbOk":true}'
[[ "$QC1_BROWSER_COUNTS" == 1000,10000 ]]
QC1_BROWSER_COUNTS=10,100   # the fixture sizes are 10 and 100: its browser rows are at counts the table holds
# A complete set of rows for sizes S, counts C and dense counts D: every kind holds its instruments, its expectation and its outcome. Dense counts are
# additive: the same discovery, browser and deletion rows for each dense count the table holds. The set-v1 request-size row is the largest deletion count
# the table holds (K), its refusals, the over-cap body and the old-form comparison.
# The scanner probe of a healthy run: eight executions per mode (one initial unit, seven continuations at cursor positions 0 to 85 per cent), each an index scan in
# primary-key order with its id predicate as an Index Cond, no Sort, one loop, the unit's rows and a flat number of shared blocks.
probe_json() {
  jq -nc '{unit:50000, tableRows:1000, sameSession:true, modes:["auto","force_generic_plan"], executionsPerMode:8,
    executions:[("auto","force_generic_plan") as $m
      | ({mode:$m, kind:"initial", percent:null, afterId:null, topRows:50000, hasSort:false,
          scan:{nodeType:"Index Scan", indexName:"contact_requests_pkey", indexCond:null, filter:null, rows:50000, loops:1, sharedHit:900, sharedRead:5, rowsRemovedByFilter:0}},
         ([0,10,25,40,55,70,85] | to_entries[] | {mode:$m, kind:"continuation", percent:.value, afterId:(.value * 10 + 1), topRows:50000, hasSort:false,
          scan:{nodeType:"Index Scan", indexName:"contact_requests_pkey", indexCond:"(id > $1)", filter:null, rows:50000, loops:1, sharedHit:(900 + .key), sharedRead:5, rowsRemovedByFilter:0}}))]}'
}
gen() { # sizes counts [dense]
  local s k d m c kept removed bc K dlist ci np pre pages digest ph probe
  probe="$(probe_json)"
  for s in $(qc1_list "$1"); do
    dlist=""; [[ -z "${3:-}" ]] || dlist="$(qc1_list "$3")"
    K=0; for k in $(qc1_list "$QC1_DELETE_COUNTS") $dlist; do (( k <= s && k > K )) && K=$k; done
    ci=null; (( s + 1000 > QC1_SCANNER_UNIT_ROWS )) && ci=true
    for c in "sparse 1" "unrelated 2"; do
      read -r name item <<<"$c"
      printf '{"status":"measured","item":%s,"case":"%s","size":%s,"workers":0,"expect":{"items":0,"kept":0,"removed":0},"outcome":%s,"traversal":{"units":1,"slowestUnitMs":5,"totalMs":10,"requestMs":8,"clientMs":2,"items":0,"lastStatus":"200",%s},"delete":{%s,"reasonsKept":0,"reasonsRemoved":0}}\n' "$item" "$name" "$s" "$okout" "$INSTR" "$HDEL"
    done
    printf '{"status":"measured","item":3,"case":"order-control-requester-10","size":%s,"workers":0,"targetReasonLen":1000,"reasons":10,"expect":{"items":10},"traversal":{"units":1,"slowestUnitMs":5,"totalMs":10,"requestMs":8,"clientMs":2,"items":10,"lastStatus":"200",%s}}\n' "$s" "$INSTR"
    printf '{"status":"measured","item":5,"case":"request-size","size":%s,"workers":0,"request":{"format":"set-v1","maxRequestBytes":6291456,"capacity":{"reasons":18873540,"bytesAt":6291455,"bytesNext":6291459},"largest":{"reasons":%s,"bodyBytes":%s,"sent":true},"parseRefusal":{"status":"400","unchanged":true},"countMismatch":{"status":"400","unchanged":true},"changedSet":{"status":"409","unchanged":true},"overCap":{"reasons":18873541,"bodyBytes":6291459,"status":"413","unchanged":true},"legacyComparison":{"synthetic":true,"capacityEntries":52868,"entriesOver":52869,"overStatus":"413","fitStatus":"409","fitUnchanged":true},"fitExpect":{"kept":%s,"removed":%s},"fitOutcome":%s},"delete":{%s,"reasonsKept":%s,"reasonsRemoved":%s},"traversal":{"units":1,"slowestUnitMs":50,"totalMs":100,"requestMs":90,"clientMs":10,"items":%s,"lastStatus":"200",%s}}\n' \
      "$s" "$K" "$(( 400 + K ))" "$(( K / 2 ))" "$(( K - K / 2 ))" "$okout" "$HDEL" "$(( K / 2 ))" "$(( K - K / 2 ))" "$K" "$INSTR"
    printf '{"status":"measured","item":7,"case":"human-progress","size":%s,"workers":0,"assessment":{"perCount":[{"reasons":10}]}}\n' "$s"
    printf '{"status":"measured","item":8,"case":"plans","size":%s,"workers":0,"table":{"backgroundRows":%s,"actualRows":%s},"plans":{"scanner-initial":1,"scanner-continuation":%s,"page-items":1,"delete-set":1,"blank-batch":1},"continuationPlanIndexCond":%s,"executions":{"scannerInitial":{"count":1,"rows":[50000],"sharedBlocks":[900]},"scannerContinuation":{"count":%s,"rows":%s,"sharedBlocks":%s}},"generic":{"source":"go-source","scannerContinuation":{"indexCondOnId":true},"scannerInitial":{"idIndexScan":true}},"scannerProbe":%s}\n' \
      "$s" "$s" "$(( s + 1000 ))" "$(( s + 1000 > QC1_SCANNER_UNIT_ROWS ? 2 : 0 ))" "$ci" "$(( s + 1000 > QC1_SCANNER_UNIT_ROWS ? 2 : 0 ))" "$( (( s + 1000 > QC1_SCANNER_UNIT_ROWS )) && echo '[50000,50000]' || echo '[]')" "$( (( s + 1000 > QC1_SCANNER_UNIT_ROWS )) && echo '[880,900]' || echo '[]')" "$probe"
    for bc in $(qc1_list "$QC1_BROWSER_COUNTS") $dlist; do
      (( bc <= s )) || continue
      np=$(( (bc + 99) / 100 )); pre=$(( 35 + 40 * bc ))
      pages="{\"count\":$np,\"responseBytesMax\":30000,\"responseBytesTotal\":$(( bc * 300 )),\"requestMsMax\":20,\"requestMsP95\":15,\"readMsMax\":3,\"readMsTotal\":$(( np * 2 )),\"parseMsMax\":5,\"parseMsTotal\":$(( np * 3 )),\"nodeParseMsMax\":4}"
      digest="{\"path\":\"webcrypto\",\"expectedPath\":\"webcrypto\",\"matchesExpectation\":true,\"subtleCalls\":2,\"subtleBytes\":$(( pre + 4 )),\"subtleMs\":3,\"preimageBytes\":$pre,\"buildMs\":40}"
      ph="{\"walBytes\":5000,\"auditActionsDelta\":{\"x.view\":$np},\"auditRows\":{\"before\":1,\"after\":2},\"tableRows\":{\"before\":1010,\"after\":1010},\"auditBytes\":{\"before\":100,\"after\":200,\"delta\":100}}"
      printf '{"status":"measured","item":10,"case":"browser-panel-%s","size":%s,"workers":0,"targetReasonLen":1000,"reasons":%s,"window":{"ms":100,%s},"browser":{"walkTotal":%s,"renderReadyMs":4000,"walkEndMs":3900,"firstPage":{"firstInteractiveMs":600,"itemsRendered":%s},"itemsRendered":%s,"heapAfterGcBytes":5000000,"domNodes":9000,"longTaskMs":900,"secureContext":true,"noSubtle":false,"clicks":{"count":30,"p95Ms":40,"maxMs":60},"navigation":{"nextPage":{"maxMs":50},"jump":{"maxMs":60},"nextUnchosen":{"maxMs":70},"nextChanged":{"maxMs":80}},"pageRequests":%s,"pageResponseBytes":%s,"apiOrigin":"real","press":{"observed":true,"status":"409","bodyBytes":1200,"responseMs":900},"reload":{"unchosenAfterReload":1},"pages":%s,"digest":%s,"phases":{"walk":%s,"interactions":%s,"press":%s}}}\n' \
        "$bc" "$s" "$bc" "$INSTR" "$bc" "$(( bc < 50 ? bc : 50 ))" "$(( bc < 50 ? bc : 50 ))" "$np" "$(( bc * 300 ))" "$pages" "$digest" "$ph" "$ph" "$ph"
    done
    for k in $(qc1_list "$2") $dlist; do
      (( k <= s )) || continue
      for d in requester subject; do printf '{"status":"measured","item":3,"case":"discovery-%s-%s","size":%s,"workers":0,"targetReasonLen":1000,"reasons":%s,"expect":{"items":%s},"traversal":{"units":1,"slowestUnitMs":5,"totalMs":10,"requestMs":8,"clientMs":2,"items":%s,"lastStatus":"200",%s}}\n' "$d" "$k" "$s" "$k" "$k" "$k" "$INSTR"; done
    done
    for k in $(qc1_list "$QC1_DELETE_COUNTS") $dlist; do
      (( k <= s )) || continue
      for d in requester subject; do for m in keep remove mixed; do
        read -r kept removed <<<"$(qc1_expected_split "$m" "$k")"
        printf '{"status":"measured","item":6,"case":"delete-%s-%s-%s","size":%s,"workers":0,"targetReasonLen":1000,"reasons":%s,"expect":{"items":%s,"kept":%s,"removed":%s},"outcome":%s,"delete":{%s,"reasonsKept":%s,"reasonsRemoved":%s}}\n' "$d" "$k" "$m" "$s" "$k" "$k" "$kept" "$removed" "$okout" "$HDEL" "$kept" "$removed"
      done; done
    done
  done
  printf '{"status":"observation","item":9,"case":"workers-on-delete","size":%s,"workers":2,"reasons":1000,"expect":{"items":1000,"kept":500,"removed":500},"outcome":%s,"delete":{%s,"reasonsKept":500,"reasonsRemoved":500}}\n' "$(qc1_list "$1" | sort -n | tail -n 1)" "$okout" "$HDEL"
}
PEND='{"status":"pending","item":4,"case":"legacy-discovery","size":null,"reason":"T07B-3a"}
{"status":"pending","item":5,"case":"legacy-put","size":null,"reason":"T07B-3a"}'
status_of() { qc1_evidence_status "$1" "${2:-10,100}" "${3:-0,10,100,1000,10000}" "$WORK/env.json" "$HEAD_FAKE" 6291456 "${4:-}"; }

# --- judging measurements ------------------------------------------------------------
cat >"$WORK/m.jsonl" <<M
{"status":"measured","item":3,"case":"discovery-subject-10000","size":1000000,"workers":0,"reasons":10000,"expect":{"items":10000},"traversal":{"slowestUnitMs":9000,"totalMs":20000,"requestMs":19000,"clientMs":1000,"units":100,"items":10000,"lastStatus":"200",$INSTR,"gateTicks":200,"dbMemSamples":200}}
{"status":"measured","item":3,"case":"discovery-requester-10000","size":1000000,"workers":0,"reasons":10000,"expect":{"items":10000},"traversal":{"slowestUnitMs":12000,"totalMs":130000,"requestMs":120000,"clientMs":10000,"units":100,"items":10000,"lastStatus":"200",$INSTR,"gateTicks":1300,"dbMemSamples":1300,"gateMaxGapMs":30}}
{"status":"measured","item":6,"case":"delete-subject-10000-mixed","size":1000000,"workers":0,"expect":{"items":10000,"kept":5000,"removed":5000},"outcome":{"dbOk":true},"delete":{"status":"200","ms":41000,"reasonsKept":5000,"reasonsRemoved":5000,$INSTR,"gateTicks":410,"dbMemSamples":410,"gateHolders":1}}
{"status":"measured","item":6,"case":"delete-subject-1000-keep","size":1000000,"workers":0,"expect":{"items":1000,"kept":1000,"removed":0},"outcome":{"dbOk":true},"delete":{"status":"409","ms":3000,"reasonsKept":null,"reasonsRemoved":null,$INSTR,"gateTicks":30,"dbMemSamples":30,"gateHolders":1}}
{"status":"pending","item":4,"case":"legacy-discovery","size":null,"reason":"x"}
M
qc1_judge "$WORK/m.jsonl" 6291456 >"$WORK/failures.jsonl"
failures="$(jq -sc '[.[] | .class + ":" + .failure + ":" + (.case)] | sort' "$WORK/failures.jsonl")"
[[ "$failures" == '["budget:delete:delete-subject-10000-mixed","budget:traversal:discovery-requester-10000","budget:unit:discovery-requester-10000","invalid:outcome-count:delete-subject-1000-keep","invalid:status:delete-subject-1000-keep"]' ]] \
  || { echo "unexpected failures: $failures" >&2; exit 1; }
jq -e 'select(.failure == "unit") | .budget == 10000 and .ms == 12000' "$WORK/failures.jsonl" >/dev/null
[[ "$QC1_UNIT_BUDGET_MS" == 10000 && "$QC1_DELETE_BUDGET_MS" == 40000 && "$QC1_TRAVERSAL_BUDGET_MS" == 120000 ]]

# --- evidence status (V-04): complete only when every measurement and instrument holds ------------------
{ gen 10,100 0,10,100,1000,10000; } >"$WORK/e.jsonl"
status="$(status_of "$WORK/e.jsonl")"
[[ "$(jq -c '.missing' <<<"$status")" == '[]' && "$(jq -c '.invalid' <<<"$status")" == '[]' ]] || { echo "healthy rows are not clean: $status" >&2; exit 1; }
# The legacy rows the matrix owes PER SIZE are named as pending even when no row mentions them: never complete. Size 10 owes discovery with 0 pairs, the probe and the
# PUT; size 100 adds 100 pairs (10,000 does not fit).
[[ "$(jq -c '.pending | map(.case + "@" + (.size | tostring)) | sort' <<<"$status")" == '["legacy-discovery-0@10","legacy-discovery-0@100","legacy-discovery-100@100","legacy-discovery-probe@10","legacy-discovery-probe@100","legacy-put@10","legacy-put@100"]' && "$(jq -r .complete <<<"$status")" == false ]]
[[ "$(jq -c '.pending | map(.item) | unique' <<<"$status")" == '[4,5]' ]]
# set-v1 has no per-reason body growth to speak of (18.9 million reasons fit the cap): healthy rows report no budget finding, and a body over the cap
# would be one (tested below), never an invalid run.
[[ "$(jq -c '.budgetFailures' <<<"$status")" == '[]' ]]
{ cat "$WORK/e.jsonl"; echo "$PEND"; } >"$WORK/ec.jsonl"
status="$(status_of "$WORK/ec.jsonl")"
[[ "$(jq -r .complete <<<"$status")" == false && "$(jq -c '.pending | map(.item) | unique' <<<"$status")" == '[4,5]' && "$(jq -r '.pending | length' <<<"$status")" == 7 ]]   # a runner placeholder row clears nothing
# Item 4 and the legacy PUT measured (T07B-3a landed): complete.
# One legacy row set per size, as T07B-3a will produce it: discovery with each count that fits, the marker lookup, and a real PUT. Every row carries real metrics.
legacy_rows() { # sizes
  local s k
  for s in $(qc1_list "$1"); do
    for k in $(qc1_list "$QC1_LEGACY_COUNTS"); do
      (( k <= s )) || continue
      printf '{"status":"measured","item":4,"case":"legacy-discovery-%s","size":%s,"workers":0,"reasons":%s,"expect":{"items":%s},"traversal":{"units":1,"slowestUnitMs":5,"totalMs":10,"requestMs":8,"clientMs":2,"items":%s,"lastStatus":"200",%s}}\n' "$k" "$s" "$k" "$k" "$k" "$INSTR"
    done
    printf '{"status":"measured","item":4,"case":"legacy-discovery-probe","size":%s,"workers":0,"probe":{"ms":3,"status":"200","found":true}}\n' "$s"
    printf '{"status":"measured","item":5,"case":"legacy-put","size":%s,"workers":0,"put":{"bodyBytes":1000,"ms":50,"status":"200","validated":true,"appPeakKb":2048}}\n' "$s"
  done
}
{ cat "$WORK/e.jsonl"
  legacy_rows 10,100; } >"$WORK/e2.jsonl"
status="$(status_of "$WORK/e2.jsonl")"
[[ "$(jq -r .complete <<<"$status")" == true && "$(jq -c '[.missing, .pending, .invalid] | map(length)' <<<"$status")" == '[0,0,0]' ]] || { echo "not complete: $status" >&2; exit 1; }
mut() { jq -c "$2" "$WORK/e2.jsonl" >"$WORK/e2.$1"; }  # name jq-filter (applied to every row)
expect_invalid() { # name failure
  local st; st="$(status_of "$WORK/e2.$1")"
  [[ "$(jq -r .complete <<<"$st")" == false && "$(jq -r --arg f "$2" '[.invalid[] | select(.failure == $f)] | length > 0' <<<"$st")" == true ]] || { echo "mutation $1 not invalid ($2): $st" >&2; exit 1; }
}
DEL='if .item == 6 and .case == "delete-subject-100-mixed"'
# A metric that read nothing is not evidence: every one of the instruments, on a deletion and on a traversal.
for m in walBytes appPeakKb dbPeakKb gateTicks commitCalls commitMsMax commitMsTotal dbMemSamples gateMaxGapMs; do
  mut "null-$m" "$DEL then .delete.$m = null else . end"; expect_invalid "null-$m" metric-missing
  mut "tnull-$m" 'if .item == 3 and .case == "discovery-requester-10" then .traversal.'"$m"' = null else . end'; expect_invalid "tnull-$m" metric-missing
done
# The per-operation peak reset must have been verified.
mut reset "$DEL then .delete.appHwmReset = false else . end"; expect_invalid reset peak-reset-failed
mut reset-gone "$DEL then del(.delete.appHwmReset) else . end"; expect_invalid reset-gone peak-reset-failed
# Too few memory samples or gate ticks for the time the operation ran; a gap over half the operation.
mut starved "$DEL then .delete.ms = 500 | .delete.gateTicks = 2 | .delete.gateHolders = 1 else . end"; expect_invalid starved sampler-starved
mut undersampled "$DEL then .delete.ms = 500 | .delete.dbMemSamples = 3 | .delete.gateHolders = 1 else . end"; expect_invalid undersampled memory-undersampled
mut gap "$DEL then .delete.ms = 500 | .delete.gateMaxGapMs = 300 | .delete.gateHolders = 1 else . end"; expect_invalid gap sampler-gap
mut gap-ok "$DEL then .delete.ms = 500 | .delete.gateMaxGapMs = 250 | .delete.gateHolders = 1 else . end"
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.gap-ok")")" == true ]]
mut gap-short "$DEL then .delete.ms = 150 | .delete.gateMaxGapMs = 140 else . end"
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.gap-short")")" == true ]]
# A long deletion whose gate was never seen; a short one may fall between samples.
mut unseen "$DEL then .delete.ms = 500 | .delete.gateHolders = 0 else . end"; expect_invalid unseen gate-unobserved
mut seen "$DEL then .delete.ms = 500 | .delete.gateHolders = 1 else . end"
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.seen")")" == true ]]
mut short "$DEL then .delete.ms = 150 else . end"
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.short")")" == true ]]
# A successful deletion with no COMMIT seen.
mut nocommit "$DEL then .delete.commitCalls = 0 else . end"; expect_invalid nocommit no-commit-seen
# The scheduler: a window that overlapped its edges, rows it enqueued, or neither field recorded.
mut sched-overlap "$DEL then .delete.schedulerOverlap = true else . end"; expect_invalid sched-overlap scheduler
mut sched-rows "$DEL then .delete.schedulerRows = 2 else . end"; expect_invalid sched-rows scheduler
mut sched-gone "$DEL then del(.delete.schedulerOverlap) else . end"; expect_invalid sched-gone scheduler
mut sched-trav 'if .item == 3 and .case == "discovery-requester-10" then .traversal.schedulerOverlap = true else . end'; expect_invalid sched-trav scheduler
# Competitors in a row that claims attribution; an observation is exempt and must have its workers.
mut vacuum "$DEL then .delete.autovacuumWorkers = 1 else . end"; expect_invalid vacuum competitor
mut workers "$DEL then .workers = 2 else . end"; expect_invalid workers competitor
mut obs-no-workers 'if .item == 9 then .workers = 0 else . end'; expect_invalid obs-no-workers observation-without-workers
mut obs-contaminated 'if .item == 9 then .delete.autovacuumWorkers = 3 | .delete.schedulerOverlap = true | .delete.schedulerRows = 4 else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.obs-contaminated")")" == true ]]
# Expectations: the discovery count, the kept and removed counts and the database must match what the row set out to do.
mut disc 'if .item == 3 and .case == "discovery-subject-100" then .traversal.items = 99 else . end'; expect_invalid disc discovery-count
mut kept "$DEL then .delete.reasonsKept = 49 else . end"; expect_invalid kept outcome-count
mut removed "$DEL then .delete.reasonsRemoved = 51 else . end"; expect_invalid removed outcome-count
mut nullcount "$DEL then .delete.reasonsRemoved = null else . end"; expect_invalid nullcount outcome-count
mut db "$DEL then .outcome.dbOk = false else . end"; expect_invalid db outcome-db
mut dbgone "$DEL then del(.outcome) else . end"; expect_invalid dbgone outcome-db
mut noexpect "$DEL then del(.expect) else . end"; expect_invalid noexpect no-expectation
mut noexpect3 'if .item == 3 and .case == "discovery-subject-100" then del(.expect) else . end'; expect_invalid noexpect3 no-expectation
mut noexpect1 'if .item == 1 then del(.expect) else . end'; expect_invalid noexpect1 no-expectation
mut obs-count 'if .item == 9 then .delete.reasonsKept = 1 else . end'; expect_invalid obs-count outcome-count
mut unrelated-db 'if .item == 2 then .outcome.dbOk = false else . end'; expect_invalid unrelated-db outcome-db
# Item 5 (set-v1): the sent deletion is checked like any other, the traversal must have listed every reason the largest body names.
mut fit-kept 'if .item == 5 then .delete.reasonsKept = 1 else . end'; expect_invalid fit-kept outcome-count
mut fit-db 'if .item == 5 then .request.fitOutcome.dbOk = false else . end'; expect_invalid fit-db outcome-db
mut probe-items 'if .item == 5 and .case == "request-size" then .traversal.items = (.request.largest.reasons - 1) else . end'; expect_invalid probe-items discovery-count
for f in capacity largest; do
  mut "req-$f-gone" "if .item == 5 then del(.request.$f) else . end"; expect_invalid "req-$f-gone" request-incomplete
done
mut req-cap-gone 'if .item == 5 then del(.request.maxRequestBytes) else . end'; expect_invalid req-cap-gone request-incomplete
# A history whose body would exceed maxRequestBytes (or whose capacity is below its reason count) is a budget finding, never hidden and never invalid.
mut body-over 'if .item == 5 and .case == "request-size" then .request.largest.bodyBytes = (.request.maxRequestBytes + 1) else . end'
st="$(status_of "$WORK/e2.body-over")"; [[ "$(jq -c '[.budgetFailures[] | select(.failure == "body")] | length > 0' <<<"$st")" == true && "$(jq -r '.invalid | length' <<<"$st")" == 0 ]]
mut cap-below 'if .item == 5 and .case == "request-size" then .request.capacity.reasons = (.request.largest.reasons - 1) else . end'
st="$(status_of "$WORK/e2.cap-below")"; [[ "$(jq -c '[.budgetFailures[] | select(.failure == "body")] | length > 0' <<<"$st")" == true ]]
# Statuses: a refused deletion, a failed page, a changed over-cap or refused account, a wrong over-cap, parse, count or changed-set answer.
mut s409 "$DEL then .delete.status = \"409\" else . end"; expect_invalid s409 status
mut p503 'if .item == 3 and .case == "discovery-requester-10" then .traversal.lastStatus = "503" else . end'; expect_invalid p503 status
mut changed 'if .item == 5 then .request.overCap.unchanged = false else . end'; expect_invalid changed over-cap-changed-data
mut parse-changed 'if .item == 5 then .request.parseRefusal.unchanged = false else . end'; expect_invalid parse-changed refused-parse-changed-data
mut count-changed 'if .item == 5 then .request.countMismatch.unchanged = false else . end'; expect_invalid count-changed refused-count-changed-data
mut set-changed 'if .item == 5 then .request.changedSet.unchanged = false else . end'; expect_invalid set-changed changed-set-changed-data
mut over500 'if .item == 5 then .request.overCap.status = "500" else . end'; expect_invalid over500 status
mut parse200 'if .item == 5 then .request.parseRefusal.status = "200" else . end'; expect_invalid parse200 status
mut count200 'if .item == 5 then .request.countMismatch.status = "200" else . end'; expect_invalid count200 status
mut set200 'if .item == 5 then .request.changedSet.status = "200" else . end'; expect_invalid set200 status
mut set-gone 'if .item == 5 then del(.request.changedSet) else . end'; expect_invalid set-gone status
mut legacy-gone 'if .item == 5 then del(.request.legacyComparison) else . end'; expect_invalid legacy-gone legacy-comparison
mut legacy-over200 'if .item == 5 then .request.legacyComparison.overStatus = "200" else . end'; expect_invalid legacy-over200 legacy-comparison
mut legacy-fit200 'if .item == 5 then .request.legacyComparison.fitStatus = "200" else . end'; expect_invalid legacy-fit200 legacy-comparison
mut fit409 'if .item == 5 then .delete.status = "409" else . end'; expect_invalid fit409 status
# Item 7 without an assessment; accounting that does not add up.
mut noassess 'if .item == 7 then del(.assessment) else . end'; expect_invalid noassess assessment-missing
mut acct 'if .item == 3 and .case == "discovery-requester-10" then .traversal.requestMs = 99 | .traversal.totalMs = 10 | .traversal.clientMs = 0 else . end'; expect_invalid acct accounting
# Item 10: every figure, the walk's total, the first interactive page, the 30 clicks, the navigation controls, the press and the reload, and a verified real origin.
for f in renderReadyMs walkEndMs heapAfterGcBytes domNodes longTaskMs secureContext 'firstPage.firstInteractiveMs' 'firstPage.itemsRendered' 'clicks.p95Ms' 'clicks.maxMs' 'navigation.nextPage.maxMs' 'navigation.jump.maxMs' 'navigation.nextUnchosen.maxMs' 'navigation.nextChanged.maxMs'; do
  mut "b-$f" "if .item == 10 and .case == \"browser-panel-100\" and .size == 100 then .browser.$f = null else . end"; expect_invalid "b-$f" browser-missing
done
mut b-count 'if .item == 10 and .case == "browser-panel-100" then .browser.walkTotal = 999 else . end'; expect_invalid b-count browser-count
mut b-first 'if .item == 10 and .case == "browser-panel-100" then .browser.firstPage.itemsRendered = 49 else . end'; expect_invalid b-first browser-first-page
mut b-clicks 'if .item == 10 and .case == "browser-panel-100" then .browser.clicks.count = 29 else . end'; expect_invalid b-clicks browser-clicks
mut b-press 'if .item == 10 and .case == "browser-panel-100" then .browser.press.status = "200" else . end'; expect_invalid b-press browser-press
mut b-press-gone 'if .item == 10 and .case == "browser-panel-100" then .browser.press.observed = false else . end'; expect_invalid b-press-gone browser-press
mut b-reload 'if .item == 10 and .case == "browser-panel-100" then .browser.reload.unchosenAfterReload = 0 else . end'; expect_invalid b-reload browser-reload
mut b-mock 'if .item == 10 then .browser.apiOrigin = "unverified" else . end'; expect_invalid b-mock browser-not-real-api
# The interaction budgets are engineering proposals (design R2): over them is a budget finding, not an invalid run, and the number is recorded as measured.
for spec in 'clicks.p95Ms:101:click-p95' 'clicks.maxMs:201:click-max' 'navigation.nextPage.maxMs:301:navigation' 'navigation.jump.maxMs:301:navigation' 'renderReadyMs:120001:browser-traversal'; do
  IFS=: read -r path val fail <<<"$spec"
  mut "bud-$path" "if .item == 10 and .case == \"browser-panel-100\" and .size == 100 then .browser.$path = $val else . end"
  st="$(status_of "$WORK/e2.bud-$path")"
  [[ "$(jq -r --arg f "$fail" '[.budgetFailures[] | select(.failure == $f)] | length > 0' <<<"$st")" == true && "$(jq -r '.invalid | length' <<<"$st")" == 0 ]] || { echo "budget $path=$val did not report $fail: $st" >&2; exit 1; }
done
mut bud-ok 'if .item == 10 then .browser.clicks.p95Ms = 100 | .browser.clicks.maxMs = 200 | .browser.navigation.jump.maxMs = 300 else . end'
[[ "$(jq -c '[.budgetFailures[] | select(.failure | test("click|navigation"))] | length' <<<"$(status_of "$WORK/e2.bud-ok")")" == 0 ]]
grep -v '"item":10,"case":"browser-panel-10","size":100,' "$WORK/e2.jsonl" >"$WORK/e2.nobrowser"
st="$(status_of "$WORK/e2.nobrowser")"
[[ "$(jq -r .complete <<<"$st")" == false && "$(jq -c '.missing | map(.item, .size, .case)' <<<"$st")" == '[10,100,"browser-panel-10"]' ]]
# Whole objects missing, not only a field: every kind of row needs the objects its measurement produces.
for spec in 1:traversal 1:delete 2:traversal 2:delete 3:traversal 5:traversal 5:delete 5:request 6:delete 9:delete; do
  item="${spec%%:*}"; obj="${spec##*:}"
  mut "gone-$item-$obj" "if .item == $item then del(.$obj) else . end"
  case "$obj" in traversal) want=missing-traversal ;; delete) want=missing-delete ;; request) want=missing-request ;; esac
  expect_invalid "gone-$item-$obj" "$want"
done
# A time that is not a number (a string, null, an object) is as good as missing.
for spec in '3:traversal:totalMs' '3:traversal:slowestUnitMs' '1:traversal:totalMs' '5:traversal:slowestUnitMs'; do
  IFS=: read -r item obj field <<<"$spec"
  for bad in '"10"' null '{"ms":1}' true; do
    mut "time-$item-$field" "if .item == $item then .$obj.$field = $bad else . end"; expect_invalid "time-$item-$field" missing-traversal
  done
done
for spec in '6:ms' '1:ms' '2:ms' '9:ms' '5:ms'; do
  IFS=: read -r item field <<<"$spec"
  for bad in '"100"' null '[100]' false; do
    mut "dtime-$item" "if .item == $item then .delete.$field = $bad else . end"; expect_invalid "dtime-$item" missing-delete
  done
done
# A string where an object belongs is missing, and never crashes the judge into silence.
mut str-trav 'if .item == 3 and .case == "discovery-requester-10" then .traversal = "broken" else . end'; expect_invalid str-trav missing-traversal
mut str-del 'if .item == 6 and .case == "delete-subject-100-mixed" then .delete = "broken" else . end'; expect_invalid str-del missing-delete
mut str-req 'if .item == 5 then .request = "broken" else . end'; expect_invalid str-req missing-request
# The legacy PUT row, once measured, is not asked for a traversal or a deletion.
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.jsonl")")" == true ]]
# Plans: the object must carry every kind at least once.
mut plans-gone 'if .item == 8 then del(.plans) else . end'; expect_invalid plans-gone plans-missing
mut plans-zero 'if .item == 8 then .plans["blank-batch"] = 0 else . end'; expect_invalid plans-zero plans-missing
mut plans-str 'if .item == 8 then .plans["scanner-initial"] = "1" else . end'; expect_invalid plans-str plans-missing
mut plans-nocont-small 'if .item == 8 and .size == 100 then .plans["scanner-continuation"] = 0 else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.plans-nocont-small")")" == true ]]   # a table within one scanner unit has no continuation
mut plans-nocont-big 'if .item == 8 then .table.actualRows = 60000 | .plans["scanner-continuation"] = 0 else . end'
[[ "$(qc1_judge "$WORK/e2.plans-nocont-big" 6291456 | jq -sc '[.[] | select(.failure == "plans-missing")] | length > 0')" == true ]]
mut gen-gone 'if .item == 8 then del(.generic) else . end'; expect_invalid gen-gone generic-plan-missing
mut gen-init-gone 'if .item == 8 then del(.generic.scannerInitial) else . end'; expect_invalid gen-init-gone generic-plan-missing
# The continuation must be bounded by an id Index Cond: the generic plan, or the application's own logged plan, without one is a budget finding.
mut gen-nocond 'if .item == 8 then .generic.scannerContinuation.indexCondOnId = false else . end'
st="$(status_of "$WORK/e2.gen-nocond")"; [[ "$(jq -r '[.budgetFailures[] | select(.failure == "plan-continuation")] | length > 0' <<<"$st")" == true && "$(jq -r '.invalid | length' <<<"$st")" == 0 ]]
mut logged-nocond 'if .item == 8 and .size == 100 then .continuationPlanIndexCond = false | .plans["scanner-continuation"] = 1 else . end'
st="$(status_of "$WORK/e2.logged-nocond")"; [[ "$(jq -r '[.budgetFailures[] | select(.failure == "plan-continuation")] | length > 0' <<<"$st")" == true ]]
# Browser response accounting: the decoded body sizes must be there (a chunked or unreadable response gives none), the walk must have the requests its pages need.
for bad in null 0 '"123"' 9999 '-5'; do
  mut "bytes-$bad" "if .item == 10 and .case == \"browser-panel-100\" then .browser.pageResponseBytes = $bad else . end"; expect_invalid "bytes-$bad" browser-bytes
done
mut bytes-gone 'if .item == 10 and .case == "browser-panel-100" then del(.browser.pageResponseBytes) else . end'; expect_invalid bytes-gone browser-bytes
mut bytes-ok 'if .item == 10 and .case == "browser-panel-100" then .browser.pageResponseBytes = 100000 else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.bytes-ok")")" == true ]]
mut req-gone 'if .item == 10 and .case == "browser-panel-100" then del(.browser.pageRequests) else . end'; expect_invalid req-gone browser-walk-short
mut req-short 'if .item == 10 and .case == "browser-panel-10" then .browser.pageRequests = 0 else . end'; expect_invalid req-short browser-walk-short
mut req-exact 'if .item == 10 and .case == "browser-panel-10" then .browser.pageRequests = 1 else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.req-exact")")" == true ]]
mut b-obj-gone 'if .item == 10 and .case == "browser-panel-100" then del(.browser) else . end'; expect_invalid b-obj-gone browser-missing
# The environment must record a passing browser preflight when the browser is on.
for spec in '.browserRuntime.ok = false' 'del(.browserRuntime)' '.browserRuntime.chromium = ""' '.browserRuntime.playwright = null'; do
  jq -c "$spec" "$WORK/env.json" >"$WORK/env.brt"
  qc1_environment_problems "$WORK/env.brt" "$HEAD_FAKE" | grep -q 'browser preflight is not recorded as passing' || { echo "environment accepted: $spec" >&2; exit 1; }
done
jq -c '.settings.browser = false | del(.browserRuntime)' "$WORK/env.json" >"$WORK/env.nobrowser"
[[ -z "$(qc1_environment_problems "$WORK/env.nobrowser" "$HEAD_FAKE")" ]]   # skipped on purpose: the rows are missing instead
# Plans: a size whose statements were not all found, or no plan row, is missing, not invalid.
mut noplans 'if .item == 8 and .size == 100 then .status = "failed" else . end'
st="$(status_of "$WORK/e2.noplans")"
[[ "$(jq -r .complete <<<"$st")" == false && "$(jq -c '.missing | map(.item)' <<<"$st")" == '[8]' ]]
grep -v '"item":8,"case":"plans","size":10,' "$WORK/e2.jsonl" >"$WORK/e2.plan10"
[[ "$(jq -c '.missing | map(.item, .size)' <<<"$(status_of "$WORK/e2.plan10")")" == '[8,10]' ]]
# The workers-on observation must exist at the largest size.
grep -v '"item":9' "$WORK/e2.jsonl" >"$WORK/e2.noobs"
st="$(status_of "$WORK/e2.noobs")"; [[ "$(jq -c '.missing | map(.item, .size)' <<<"$st")" == '[9,100]' && "$(jq -r .complete <<<"$st")" == false ]]
mut obs-small 'if .item == 9 then .size = 10 else . end'
[[ "$(jq -c '.missing | map(.item, .size)' <<<"$(status_of "$WORK/e2.obs-small")")" == '[9,100]' ]]
# A status other than measured does not count as a measurement.
mut failed6 'if .item == 6 and .case == "delete-subject-100-remove" and .size == 100 then .status = "failed" else . end'
[[ "$(jq -c '.missing | map(.case + "@" + (.size|tostring))' <<<"$(status_of "$WORK/e2.failed6")")" == '["delete-subject-100-remove@100"]' ]]
grep -v '"item":3,"case":"discovery-requester-0","size":10,' "$WORK/e2.jsonl" >"$WORK/e4.jsonl"
[[ "$(jq -r '.missing | length' <<<"$(status_of "$WORK/e4.jsonl")")" == 1 ]]
# A whole size missing lists all of it: 41 rows (sparse, unrelated, 8 discovery, the order control, request size, progress, plans, 2 browser, 24 deletions: 0, 10, 100 and 1,000 reasons in both directions and all three modes) with the observation at the new largest size.
[[ "$(jq -r '.missing | length' <<<"$(qc1_evidence_status "$WORK/e2.jsonl" 10,100,1000 0,10,100,1000,10000 "$WORK/env.json" "$HEAD_FAKE" 6291456)")" == 41 ]]
refused 'not a whole number' status_of "$WORK/e2.jsonl" 10,x 0
# The judge failing is itself invalid: a row the judge cannot read must not read as clean.
{ cat "$WORK/e2.jsonl"; printf 'this line is not json\n'; } >"$WORK/e2.garbled"
st="$(status_of "$WORK/e2.garbled" 2>/dev/null)"
[[ "$(jq -r .complete <<<"$st")" == false && "$(jq -r '[.invalid[] | select(.failure == "unparsable-measurements")] | length' <<<"$st")" == 1 ]]
# A row whose parts are the wrong type is judged, not crashed on: every shape of wrong object is a finding.
for shape in 'expect' 'outcome' 'assessment' 'browser' 'plans' 'request' 'traversal' 'delete'; do
  { cat "$WORK/e2.jsonl"; jq -nc --arg k "$shape" '{status:"measured",item:2,case:"x",size:10,workers:0} + {($k): "broken"}'; } >"$WORK/e2.shape"
  st="$(status_of "$WORK/e2.shape" 2>/dev/null)"
  [[ "$(jq -r .complete <<<"$st")" == false && "$(jq -r '[.invalid[] | select(.failure == "judge-failed")] | length' <<<"$st")" == 0 ]] || { echo "a string for $shape crashed the judge: $st" >&2; exit 1; }
done
# Environment problems make an otherwise complete run incomplete, named as invalid.
jq -c '.calibration.gate.ok = false' "$WORK/env.json" >"$WORK/env.nogate"
st="$(qc1_evidence_status "$WORK/e2.jsonl" 10,100 0,10,100,1000,10000 "$WORK/env.nogate" "$HEAD_FAKE" 6291456)"
[[ "$(jq -r .complete <<<"$st")" == false && "$(jq -r '[.invalid[] | select(.failure == "environment")] | length' <<<"$st")" == 1 ]]
# A run measured on a different HEAD is refused.
st="$(qc1_evidence_status "$WORK/e2.jsonl" 10,100 0,10,100,1000,10000 "$WORK/env.json" 2222222222222222222222222222222222222222 6291456)"
[[ "$(jq -r .complete <<<"$st")" == false ]]


# --- F7 and the review's findings: what must be measured, or be called missing -------------------------------------------------------------------
RUNNER_SRC="${RUNNER_SRC:-$HERE/qc1-measure.sh}"
BP='if .item == 10 and .case == "browser-panel-100" and .size == 100'
# Every F7 figure the browser row must carry; a null is missing, never zero.
for f in 'pages.count' 'pages.responseBytesMax' 'pages.requestMsMax' 'pages.readMsMax' 'pages.parseMsMax' 'pages.parseMsTotal' 'digest.path' 'digest.buildMs' 'digest.preimageBytes' 'press.responseMs' \
  'phases.walk.walBytes' 'phases.interactions.walBytes' 'phases.press.walBytes' 'phases.walk.auditActionsDelta' 'phases.press.auditActionsDelta' 'phases.walk.auditBytes' 'phases.press.auditBytes'; do
  mut "f7-$f" "$BP then .browser.$f = null else . end"; expect_invalid "f7-$f" browser-missing
done
for f in walBytes gateTicks commitCalls; do mut "win-$f" "$BP then .window.$f = null else . end"; expect_invalid "win-$f" browser-missing; done
mut win-gone "$BP then del(.window) else . end"; expect_invalid win-gone browser-missing
mut phases-gone "$BP then del(.browser.phases) else . end"; expect_invalid phases-gone browser-missing
mut digest-str "$BP then .browser.digest = \"x\" else . end"; expect_invalid digest-str browser-missing
mut pages-str "$BP then .browser.pages = \"x\" else . end"; expect_invalid pages-str browser-missing
# The window carries the same instrument rules as any operation (here: a per-operation peak reset that was not verified).
mut win-reset "$BP then .window.appHwmReset = false else . end"; expect_invalid win-reset peak-reset-failed
mut win-audit "$BP then .window.auditActionsBefore = null else . end"; expect_invalid win-audit metric-missing
# The digest path is the one that ACTUALLY ran, and it must agree with the environment: no subtle means fallback; subtle in a secure context means webcrypto.
mut dig-nosubtle "$BP then .browser.noSubtle = true else . end"; expect_invalid dig-nosubtle browser-digest-path
# A fallback in a SECURE context is legitimate (a preimage above 16 MiB, or the known-answer self-test failing): judged supported, recorded against the expected path.
mut dig-fallback-secure "$BP then .browser.digest.path = \"fallback\" | .browser.digest.matchesExpectation = false else . end"
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.dig-fallback-secure")")" == true && "$(jq -c "[.invalid, .budgetFailures] | map(length)" <<<"$(status_of "$WORK/e2.dig-fallback-secure")")" == "[0,0]" ]]
# ...but webcrypto without crypto.subtle (an insecure context) cannot have run.
mut dig-webcrypto-insecure "$BP then .browser.secureContext = false else . end"; expect_invalid dig-webcrypto-insecure browser-digest-path
mut dig-ok-fallback "$BP then .browser.noSubtle = true | .browser.secureContext = false | .browser.digest.path = \"fallback\" else . end"
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.dig-ok-fallback")")" == true ]]
mut dig-unknown "$BP then .browser.digest.path = \"inferred\" else . end"; expect_invalid dig-unknown browser-digest-path
mut pages-short "$BP then .browser.pages.count = 0 else . end"; expect_invalid pages-short browser-pages-short
# A browser run that failed part-way leaves a PARTIAL row: the walk's budget failure survives, the row is invalid as incomplete, and the case stays missing.
mut partial "$BP then .status = \"partial\" | .failed = {phase:\"press\", error:\"the page stopped answering\"} | .browser.walkEndMs = 130000 | .browser.renderReadyMs = 130000 | .browser.clicks.p95Ms = 150 | del(.browser.press) | del(.browser.phases.press) else . end"
st="$(status_of "$WORK/e2.partial")"
[[ "$(jq -r .complete <<<"$st")" == false ]]
[[ "$(jq -c '[.invalid[] | select(.failure == "browser-incomplete") | {phase, error}]' <<<"$st")" == '[{"phase":"press","error":"the page stopped answering"}]' ]]
[[ "$(jq -c '[.budgetFailures[] | .failure] | sort' <<<"$st")" == '["browser-traversal","browser-walk","click-p95"]' ]] || { echo "partial budgets: $(jq -c '.budgetFailures' <<<"$st")" >&2; exit 1; }
[[ "$(jq -c '.missing | map(.case + "@" + (.size | tostring))' <<<"$st")" == '["browser-panel-100@100"]' ]]
# A partial row is never the measured case, whatever it holds, and a partial row without a `failed` object is still incomplete.
mut partial-nofail "$BP then .status = \"partial\" | del(.failed) else . end"
st="$(status_of "$WORK/e2.partial-nofail")"; [[ "$(jq -r '[.invalid[] | select(.failure == "browser-incomplete")] | length' <<<"$st")" == 1 && "$(jq -r .complete <<<"$st")" == false ]]
# Legacy rows measured with no metrics do not complete the evidence.
mut legacy-bare4 'if .item == 4 then del(.traversal) | del(.expect) else . end'; expect_invalid legacy-bare4 missing-traversal
mut legacy-bare4e 'if .item == 4 then del(.expect) else . end'; expect_invalid legacy-bare4e no-expectation
mut legacy-bare5 'if .item == 5 and .case == "legacy-put" then del(.put) else . end'; expect_invalid legacy-bare5 legacy-no-metrics
mut legacy-put-str 'if .item == 5 and .case == "legacy-put" then .put.ms = "50" else . end'; expect_invalid legacy-put-str legacy-no-metrics
# Zero-reason accounts in every mode and both directions are part of the matrix (the deletion of an account with no reasons).
for d in requester subject; do for m in keep remove mixed; do
  grep -v "\"case\":\"delete-$d-0-$m\",\"size\":100," "$WORK/e2.jsonl" >"$WORK/e2.no0"
  [[ "$(jq -c --arg c "delete-$d-0-$m@100" '[.missing[] | .case + "@" + (.size | tostring)] | index($c) != null' <<<"$(status_of "$WORK/e2.no0")")" == true ]] || { echo "delete-$d-0-$m is not required" >&2; exit 1; }
done; done
[[ "$(jq -c '[.[] | select(.case | test("^delete-(requester|subject)-0-(keep|remove|mixed)$"))] | length' <<<"$(jq -sc . "$WORK/e2.jsonl")")" == 12 ]]   # six at each of the two sizes
[[ "$QC1_DELETE_COUNTS" == 0,10,100,1000,10000 ]]
# Plans: the generic plan is of the real statement text, the first scanner unit must be an index scan in id order, and the across-executions rows and buffers must be there and within the unit.
mut gen-src 'if .item == 8 then .generic.source = "harness" else . end'; expect_invalid gen-src generic-plan-missing
mut gen-init-str 'if .item == 8 then .generic.scannerInitial.idIndexScan = "yes" else . end'; expect_invalid gen-init-str generic-plan-missing
mut init-noidx 'if .item == 8 then .generic.scannerInitial.idIndexScan = false else . end'
st="$(status_of "$WORK/e2.init-noidx")"; [[ "$(jq -r '[.budgetFailures[] | select(.failure == "plan-initial")] | length > 0' <<<"$st")" == true && "$(jq -r '.invalid | length' <<<"$st")" == 0 ]]
mut ex-gone 'if .item == 8 then del(.executions) else . end'; expect_invalid ex-gone plan-executions-missing
mut ex-nobuf 'if .item == 8 then .executions.scannerInitial.sharedBlocks = [null] else . end'; expect_invalid ex-nobuf plan-executions-missing
mut ex-count 'if .item == 8 then .executions.scannerInitial.count = 2 else . end'; expect_invalid ex-count plan-executions-missing
mut ex-cont 'if .item == 8 and .size == 100 then .executions.scannerContinuation = {count:1, rows:[10], sharedBlocks:[1]} else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.ex-cont")")" == true ]]
mut ex-overrun 'if .item == 8 then .executions.scannerInitial.rows = [50001] else . end'; expect_invalid ex-overrun scanner-unit-overrun
# The continuation is judged on the rows the table really held (n + k), not only on the size asked for.
mut act-big 'if .item == 8 then .table.actualRows = 50001 | .plans["scanner-continuation"] = 0 else . end'; expect_invalid act-big plans-missing
mut act-small 'if .item == 8 then .table.actualRows = 50000 | .plans["scanner-continuation"] = 0 else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.act-small")")" == true ]]
# Counts of the table: taken immediately BEFORE each operation (instrumented and the request-size operations), never at record time (when the scenario is often gone).
rec_body="$(sed -n '/^record() {/,/^}/p' "$RUNNER_SRC")"
if grep -q 'SELECT count(\*) FROM contact_requests' <<<"$rec_body"; then echo 'qc1: record() counts rows after the operation' >&2; exit 1; fi
if grep -qF 'actualRows:$actual' <<<"$rec_body"; then echo 'qc1: record() still writes a record-time row count' >&2; exit 1; fi
ins_body="$(sed -n '/^instrumented() {/,/^}/p' "$RUNNER_SRC")"
l_rows="$(grep -n 'TABLE_ROWS0="\$(table_rows)"' <<<"$ins_body" | cut -d: -f1)"; l_win="$(grep -n 'OP_START="\$(ms)"' <<<"$ins_body" | cut -d: -f1)"; l_after="$(grep -n 'TABLE_ROWS1="\$(table_rows)"' <<<"$ins_body" | cut -d: -f1)"; l_end="$(grep -n 'OP_END="\$(ms)"' <<<"$ins_body" | cut -d: -f1)"
[[ -n "$l_rows$l_win$l_after$l_end" && "$l_rows" -lt "$l_win" && "$l_end" -lt "$l_after" ]]
grep -qF 'tableRowsBefore:$tr0, tableRowsAfter:$tr1' "$RUNNER_SRC"
rs_body="$(sed -n '/^measure_request_size() {/,/^}/p' "$RUNNER_SRC")"
for pat in 'over_rows="$(table_rows)"' 'fit_rows="$(table_rows)"' 'over_old_rows="$(table_rows)"' 'overCap:{tableRowsBefore:$overCapRows' 'tableRowsBefore:{fit:$fitRows, over:$overRows}'; do grep -qF -- "$pat" <<<"$rs_body" || { echo "qc1: request-size lost: $pat" >&2; exit 1; }; done
grep -qF 'tableRowsBefore:$rows0, tableRowsAfter:$rows1' "$RUNNER_SRC"
# Each operation's own counts are judged present; the browser phases carry theirs.
for f in tableRowsBefore tableRowsAfter; do mut "tr-$f" "if .item == 3 and .case == \"discovery-requester-10\" then .traversal.$f = null else . end"; expect_invalid "tr-$f" metric-missing; done
mut tr-win "$BP then .window.tableRowsBefore = null else . end"; expect_invalid tr-win metric-missing
for ph in walk interactions press; do mut "tr-$ph" "$BP then .browser.phases.$ph.tableRows = null else . end"; expect_invalid "tr-$ph" browser-missing; done
# Coverage: PILOT or FULL is said, and release evidence needs both complete and full. It is derived from the ROWS, not from the settings alone.
st="$(status_of "$WORK/e2.jsonl")"
[[ "$(jq -r .coverage.kind <<<"$st")" == pilot && "$(jq -r .releaseEvidence <<<"$st")" == false && "$(jq -r '.coverage.reasons | length' <<<"$st")" -ge 4 ]]
[[ "$(jq -r '.notMeasured | length' <<<"$st")" -ge 7 ]]
for lim in 'gate hold time' 'digest-only time' 'isolated server-side parse' 'multi-byte' 'cluster attribution' 'droplet'; do
  [[ "$(jq -r --arg l "$lim" '[.notMeasured[] | select(contains($l))] | length' <<<"$st")" == 1 ]] || { echo "notMeasured lacks: $lim" >&2; exit 1; }
done
# THE REVIEW'S CASE: an environment record whose settings claim the whole matrix, with the 10 and 100 row sizes and arguments. The settings alone never make a run full.
jq -c '.settings += {sizes:"10000,100000,1000000", counts:"0,10,100,1000,10000", deleteCounts:"0,10,100,1000,10000,100000", denseCounts:"100000", targetReasonLen:1000, wire:"set-v1", browser:true}' "$WORK/env.json" >"$WORK/env.full"
fake="$(qc1_evidence_status "$WORK/e2.jsonl" 10,100 0,10,100,1000,10000 "$WORK/env.full" "$HEAD_FAKE" 6291456)"
[[ "$(jq -r .coverage.kind <<<"$fake")" == pilot && "$(jq -r .releaseEvidence <<<"$fake")" == false && "$(jq -r .complete <<<"$fake")" == false ]] || { echo "a fake-full environment passed: $(jq -c '[.coverage, .complete, .releaseEvidence]' <<<"$fake")" >&2; exit 1; }
[[ "$(jq -r '[.coverage.reasons[] | select(contains("rows of the full matrix are not measured and clean"))] | length' <<<"$fake")" == 1 ]]
[[ "$(jq -r '[.invalid[] | select(.failure == "settings-disagreement")] | map(.what) | sort | join(",")' <<<"$fake")" == "deletion counts,dense counts,sizes" ]] || { echo "the settings/argument disagreement was not rejected: $(jq -c '.invalid' <<<"$fake")" >&2; exit 1; }
# A REAL full run: every row of the full matrix at 10k, 100k and 1M, legacy rows per size, the settings agreeing. Only then is it full, and release evidence only when clean.
{ gen 10000,100000,1000000 0,10,100,1000,10000 100000; legacy_rows 10000,100000,1000000; } >"$WORK/full.jsonl"
full_status() { qc1_evidence_status "${1:-$WORK/full.jsonl}" 10000,100000,1000000 0,10,100,1000,10000 "${2:-$WORK/env.full}" "$HEAD_FAKE" 6291456 100000; }
fs="$(full_status)"
[[ "$(jq -c '[.coverage.kind, .complete, .releaseEvidence, (.missing|length), (.pending|length), (.invalid|length)]' <<<"$fs")" == '["full",true,true,0,0,0]' ]] || { echo "a real full run is not full: $(jq -c '[.coverage, .missing[:3], .pending[:3], .invalid[:3]]' <<<"$fs")" >&2; exit 1; }
# One required full-matrix row gone, or one target of the wrong length, and it is a pilot again.
grep -v '"item":10,"case":"browser-panel-100000","size":1000000,' "$WORK/full.jsonl" >"$WORK/full.nobrowser"
fs="$(full_status "$WORK/full.nobrowser")"; [[ "$(jq -r .coverage.kind <<<"$fs")" == pilot && "$(jq -r .releaseEvidence <<<"$fs")" == false && "$(jq -r '.missing | length' <<<"$fs")" == 1 ]]
jq -c 'if .item == 6 and .case == "delete-subject-100-mixed" and .size == 100000 then .targetReasonLen = 200 else . end' "$WORK/full.jsonl" >"$WORK/full.short"
fs="$(full_status "$WORK/full.short")"; [[ "$(jq -r .coverage.kind <<<"$fs")" == pilot && "$(jq -r '[.invalid[] | select(.failure == "settings-disagreement" and .what == "target reason length")] | length' <<<"$fs")" == 1 ]]
# A full environment but a run asked for fewer sizes than the settings name (arguments disagree), or the settings name fewer than the arguments: rejected either way.
fs="$(qc1_evidence_status "$WORK/full.jsonl" 10000,100000 0,10,100,1000,10000 "$WORK/env.full" "$HEAD_FAKE" 6291456 100000)"
[[ "$(jq -r .coverage.kind <<<"$fs")" == pilot && "$(jq -r '[.invalid[] | select(.failure == "settings-disagreement" and .what == "sizes")] | length' <<<"$fs")" == 1 ]]
jq -c '.settings.denseCounts = "none"' "$WORK/env.full" >"$WORK/env.nodense"
fs="$(full_status "$WORK/full.jsonl" "$WORK/env.nodense")"; [[ "$(jq -r .coverage.kind <<<"$fs")" == pilot && "$(jq -r '[.invalid[] | select(.what == "dense counts")] | length' <<<"$fs")" == 1 ]]
for spec in '.settings.browser = false|no browser' '.settings.targetReasonLen = 200|1,000-rune' 'del(.settings.targetReasonLen)|1,000-rune' '.settings.wire = "items"|not the set-v1 wire' 'del(.settings.sizes)|do not name the sizes'; do
  jq -c "${spec%%|*}" "$WORK/env.full" >"$WORK/env.cov"
  fs="$(full_status "$WORK/full.jsonl" "$WORK/env.cov")"
  [[ "$(jq -r .coverage.kind <<<"$fs")" == pilot && "$(jq -r .releaseEvidence <<<"$fs")" == false && "$(jq -r --arg w "${spec##*|}" '[.coverage.reasons[] | select(contains($w))] | length > 0' <<<"$fs")" == true ]] || { echo "coverage not pilot for ${spec%%|*}: $(jq -c .coverage <<<"$fs")" >&2; exit 1; }
done
# A full run that is not clean is not release evidence.
jq -c 'if .item == 10 and .case == "browser-panel-100" then . else . end | if .item == 8 then .generic.source = "harness" else . end' "$WORK/full.jsonl" >"$WORK/full.dirty"
fs="$(full_status "$WORK/full.dirty")"; [[ "$(jq -r .releaseEvidence <<<"$fs")" == false && "$(jq -r .complete <<<"$fs")" == false ]]
# Legacy pending: a failed row, a runner placeholder or a row that fails the judge clears nothing; only measured, clean rows for EVERY case at EVERY size do.
legacy_pending() { qc1_evidence_status "$1" 10,100 0,10,100,1000,10000 "$WORK/env.json" "$HEAD_FAKE" 6291456 | jq -r '.pending | length'; }
[[ "$(legacy_pending "$WORK/e2.jsonl")" == 0 ]]
for drop in '"case":"legacy-discovery-0","size":10,' '"case":"legacy-discovery-100","size":100,' '"case":"legacy-discovery-probe","size":10,' '"case":"legacy-put","size":100,'; do
  grep -vF -- "$drop" "$WORK/e2.jsonl" >"$WORK/e2.nolegacy"
  [[ "$(legacy_pending "$WORK/e2.nolegacy")" == 1 ]] || { echo "dropping $drop did not leave exactly one pending" >&2; exit 1; }
done
mut leg-failed 'if .item == 4 and .case == "legacy-discovery-0" and .size == 10 then .status = "failed" else . end'; [[ "$(legacy_pending "$WORK/e2.leg-failed")" == 1 ]]
mut leg-placeholder 'if .item == 5 then {status:"measured", item:5, case:"legacy-put", size:.size, workers:0} else . end'; [[ "$(legacy_pending "$WORK/e2.leg-placeholder")" == 2 ]]
mut leg-wrong-count 'if .item == 4 and .case == "legacy-discovery-100" then .expect.items = 99 else . end'; [[ "$(legacy_pending "$WORK/e2.leg-wrong-count")" == 1 ]]
mut leg-put-unvalidated 'if .item == 5 and .size == 10 then .put.validated = false else . end'; [[ "$(legacy_pending "$WORK/e2.leg-put-unvalidated")" == 1 ]]
mut leg-put-refused 'if .item == 5 and .size == 100 then .put.status = "413" else . end'; [[ "$(legacy_pending "$WORK/e2.leg-put-refused")" == 1 ]]
mut leg-put-nomem 'if .item == 5 and .size == 10 then del(.put.appPeakKb) else . end'; [[ "$(legacy_pending "$WORK/e2.leg-put-nomem")" == 1 ]]
mut leg-probe-nofound 'if .item == 4 and .case == "legacy-discovery-probe" and .size == 100 then .probe.found = false else . end'; [[ "$(legacy_pending "$WORK/e2.leg-probe-nofound")" == 1 ]]
mut leg-name 'if .item == 4 and .case == "legacy-discovery-100" then .reasons = 99 else . end'; [[ "$(legacy_pending "$WORK/e2.leg-name")" == 1 ]]
[[ "$QC1_LEGACY_COUNTS" == 0,100,10000 ]]
# The target reasons are the 1,000-rune maximum by default, separate from the background rows, and the loader can make them.
grep -qF 'TARGET_REASON_LEN=1000' "$RUNNER_SRC"; grep -qF -- '--target-reason-len' "$RUNNER_SRC"
grep -qF -- '-v reason_len="$TARGET_REASON_LEN" -f - <"$HERE/qc1/scenario.sql"' "$RUNNER_SRC"; grep -qF -- '-v reason_len="$REASON_LEN" -v reasons="$2" -f - <"$HERE/qc1/filler.sql"' "$RUNNER_SRC"
(( 22 * 60 >= 1000 ))   # the scenario text pattern (22 characters repeated 60 times) covers the 1,000-rune maximum
grep -qF "left(repeat('Scenario reason text. ', 60), :reason_len - length(s.j::text)) || s.j::text" "$HERE/qc1/scenario.sql"
grep -rqE 'ContactReasonLimit +(int +)?= +1000' "$ROOT"/internal/policy/*.go   # the product's maximum, which the target reasons use
grep -qF 'targetReasonLen:$targetLen, deleteCounts:$deleteCounts' "$RUNNER_SRC"
# The old items envelope's capacity counts the ids' GROWING width: bodies across the 5- to 6-digit boundary are the real length, and the boundary is the real boundary.
for e in 44999 45000 45001; do
  qc1_legacy_body "$e" 5 "$ID80" >"$WORK/legacy.json"
  [[ "$(wc -c <"$WORK/legacy.json")" == "$(qc1_setv1 legacy-length --entries "$e" --digits 5 --attempt "$ID80")" ]] || { echo "legacy length differs from the body at $e entries" >&2; exit 1; }
done
read -r on oat onext <<<"$(qc1_legacy_capacity 6291456 5 "$ID80")"
qc1_legacy_body "$on" 5 "$ID80" >"$WORK/legacy-at.json"; qc1_legacy_body "$((on + 1))" 5 "$ID80" >"$WORK/legacy-next.json"
[[ "$(wc -c <"$WORK/legacy-at.json")" == "$oat" && "$(wc -c <"$WORK/legacy-next.json")" == "$onext" ]] && (( oat <= 6291456 && onext > 6291456 ))
# Populated envelopes reduce the set-v1 capacity (examples, not bounds); the empty-map capacity is the format's.
read -r capn _ _ <<<"$(qc1_body_capacity 6291456 "$ID80")"
s2="$(jq -nc '[range(0; 200) | {key: ((100000 + .) | tostring), value: (200000 + .)}] | from_entries')"
read -r popn _ _ <<<"$(qc1_body_capacity 6291456 "$ID80" "$s2" "$s2")"
(( popn < capn && capn == 18873540 ))
if grep -nEi 'no (valid history|such limit)|none can|unlimited|no ceiling' "$RUNNER_SRC" "$HERE/qc1/lib.sh" | grep -v 'never invented\|no ceiling is invented'; then echo 'qc1: a claim that the body has no limit' >&2; exit 1; fi
grep -qF 'populatedCapacity:$populated' "$RUNNER_SRC"
# qc1_go_sql reads the real statement text out of the Go source, numbering its placeholders.
[[ "$(qc1_go_sql "$ROOT" reasonScanInitialSQL)" == "SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests ORDER BY id LIMIT \$1" ]]
[[ "$(qc1_go_sql "$ROOT" reasonScanContinuationSQL)" == "SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests WHERE id > \$1::bigint ORDER BY id LIMIT \$2" ]]
[[ -z "$(qc1_go_sql "$ROOT" noSuchConstant)" ]]
for which in scanner-initial scanner-continuation; do
  case "$which" in scanner-initial) c=reasonScanInitialSQL ;; *) c=reasonScanContinuationSQL ;; esac
  [[ "$(qc1_go_sql "$ROOT" "$c")" == *"$(qc1_plan_marker "$which")" ]]
done
for pat in 'qc1_go_sql "$ROOT" reasonScanInitialSQL' 'qc1_go_sql "$ROOT" reasonScanContinuationSQL' 'EXPLAIN (GENERIC_PLAN) $sql_cont' 'EXPLAIN (GENERIC_PLAN) $sql_init' 'source:"go-source"' 'qc1_plan_executions'; do
  grep -qF -- "$pat" "$RUNNER_SRC" || { echo "qc1: capture_plans lost: $pat" >&2; exit 1; }
done
if grep -n 'EXPLAIN (GENERIC_PLAN) SELECT' "$RUNNER_SRC"; then echo 'qc1: a generic plan of a statement the harness wrote' >&2; exit 1; fi
# Executions out of a logged plan: top node rows and shared blocks (hit + read), in order.
cat >"$WORK/pg.exec" <<LOG
2026-11-07 14:31:01.100 UTC [71] LOG:  duration: 3.2 ms  plan:
	Query Text: SELECT id FROM contact_requests $(qc1_plan_marker scanner-initial)
	Limit  (cost=0.29..10.1 rows=50000 width=8) (actual time=0.02..9.9 rows=50000 loops=1)
	  Buffers: shared hit=700 read=200
	  ->  Index Scan using contact_requests_pkey on contact_requests  (actual time=0.01..5.0 rows=50000 loops=1)
	        Buffers: shared hit=700 read=200
2026-11-07 14:31:02.100 UTC [71] LOG:  duration: 3.0 ms  plan:
	Query Text: SELECT id FROM contact_requests $(qc1_plan_marker scanner-continuation)
	Limit  (cost=0.29..10.1 rows=50000 width=8) (actual time=0.02..9.0 rows=50000 loops=1)
	  Buffers: shared hit=905
	  ->  Index Scan using contact_requests_pkey on contact_requests  (actual time=0.01..5.0 rows=50000 loops=1)
	        Index Cond: (id > '50000'::bigint)
2026-11-07 14:31:03.100 UTC [71] LOG:  duration: 2.0 ms  plan:
	Query Text: SELECT id FROM contact_requests $(qc1_plan_marker scanner-continuation)
	Limit  (cost=0.29..10.1 rows=12 width=8) (actual time=0.02..1.0 rows=12 loops=1)
	  Buffers: shared hit=40
2026-11-07 14:31:04.000 UTC [1] LOG:  received SIGHUP, reloading configuration files
LOG
[[ "$(qc1_plan_executions "$WORK/pg.exec" scanner-initial | paste -sd'|')" == '50000 900' ]]
[[ "$(qc1_plan_executions "$WORK/pg.exec" scanner-continuation | paste -sd'|')" == '50000 905|12 40' ]]
[[ -z "$(qc1_plan_executions "$WORK/pg.exec" blank-batch)" ]]

# --- the recheck's findings, each as a regression ----------------------------------------------------------------------------------------------------
# (1) A zero-reason deletion has no per-reason size: null with the explanation, not a division by zero.
prog="$(awk '/perReasonBytes:\(if \$k == 0/ { print }' "$RUNNER_SRC" | head -n 1 | sed -E "s/^.*--argjson b \"\\\$bytes\" '//; s/' <<<\"\\\$bodies\"\)\"\$//")"
[[ -n "$prog" ]]
prog="${prog//\'\"\$fixed\"\'/100}"
[[ "$(jq -c --arg d requester --argjson k 0 --argjson b 100 "$prog" <<<'[]')" == '[{"direction":"requester","reasons":0,"bodyBytes":100,"perReasonBytes":null,"perReasonNote":"not applicable: no reasons, so no reasonReview and no per-reason size"}]' ]]
[[ "$(jq -c --arg d subject --argjson k 10 --argjson b 180 "$prog" <<<'[]' | jq -c '.[0] | [.perReasonBytes, .perReasonNote]')" == '[8,null]' ]]
# The default matrix really reaches k = 0 (so this is not a corner no run visits).
[[ " ${QC1_DELETE_COUNTS//,/ } " == *" 0 "* ]]
# (7) The fallback-digest case takes the largest browser count the first size CAN hold, and says so when none fits.
fb="$(awk '/^    # The largest browser count this table can hold/ { p = 1 } p { print } p && /^    if \[\[ -n "\$bk" \]\]/ { exit }' "$RUNNER_SRC")"
[[ -n "$fb" ]]
pick() { # n list...  -> prints the count the case would run with, or none
  local n="$1" bk="" k; shift; local -a ALL_BROWSER_LIST=("$@")
  for k in "${ALL_BROWSER_LIST[@]}"; do (( k <= n )) && bk="$k"; done
  [[ -n "$bk" ]] && printf '%s' "$bk" || printf none
}
[[ "$(pick 10000 1000 10000 100000)" == 10000 && "$(pick 1000000 1000 10000 100000)" == 100000 && "$(pick 1000 1000 10000 100000)" == 1000 && "$(pick 500 1000 10000 100000)" == none ]]
grep -qF 'bk=""; for k in "${ALL_BROWSER_LIST[@]}"; do (( k <= n )) && bk="$k"; done' "$RUNNER_SRC"
if grep -qF 'bk="${ALL_BROWSER_LIST[-1]}"' "$RUNNER_SRC"; then echo 'qc1: the fallback case still takes the largest count before the size filter' >&2; exit 1; fi
grep -qF 'the fallback-digest case is not run' "$RUNNER_SRC"
# (8) The 10 s per-page budget applies to the browser's page requests, on measured rows AND on the partial rows a failed run leaves.
mut preq "$BP then .browser.pages.requestMsMax = 10001 else . end"
st="$(status_of "$WORK/e2.preq")"; [[ "$(jq -c '[.budgetFailures[] | select(.failure == "page-request") | .budget]' <<<"$st")" == '[10000]' && "$(jq -r '.invalid | length' <<<"$st")" == 0 ]]
mut preq-ok "$BP then .browser.pages.requestMsMax = 10000 else . end"; [[ "$(jq -c '[.budgetFailures[] | select(.failure == "page-request")] | length' <<<"$(status_of "$WORK/e2.preq-ok")")" == 0 ]]
mut preq-partial "$BP then .status = \"partial\" | .failed = {phase:\"press\", error:\"x\"} | .browser.pages.requestMsMax = 12000 | del(.browser.press) else . end"
st="$(status_of "$WORK/e2.preq-partial")"
[[ "$(jq -c '[.budgetFailures[] | .failure]' <<<"$st")" == '["page-request"]' && "$(jq -r '[.invalid[] | select(.failure == "browser-incomplete")] | length' <<<"$st")" == 1 ]]
# (4) The scanner probe: the real statements, more than six executions per mode in one session, the child scan proven an index scan with its id predicate as an
# Index Cond, no Sort, rows within the unit, flat buffers. Without it the evidence is incomplete, never complete.
PBS='if .item == 8 and .size == 100'
mut probe-gone "$PBS then del(.scannerProbe) else . end"; expect_invalid probe-gone scanner-probe-missing
mut probe-short "$PBS then .scannerProbe.executions |= map(select(.mode == \"auto\" and (.kind == \"initial\" or .percent < 70)) ) else . end"; expect_invalid probe-short scanner-probe-missing
mut probe-session "$PBS then .scannerProbe.sameSession = false else . end"; expect_invalid probe-session scanner-probe-missing
mut probe-noscan "$PBS then .scannerProbe.executions[3].scan = null else . end"; expect_invalid probe-noscan scanner-probe-missing
mut probe-nobuf "$PBS then .scannerProbe.executions[3].scan.sharedHit = null else . end"; expect_invalid probe-nobuf scanner-probe-missing
mut probe-overrun "$PBS then .scannerProbe.executions[2].scan.rows = 50001 else . end"; expect_invalid probe-overrun scanner-unit-overrun
for spec in 'sort|.hasSort = true|scanner-sort' 'seq|.scan.nodeType = "Seq Scan"|scanner-not-indexed' 'filter|.scan.filter = "(id > $1)"|scanner-id-filter' 'nocond|.scan.indexCond = null|scanner-continuation-cond' 'loops|.scan.loops = 2|scanner-loops'; do
  IFS='|' read -r nm filt fail <<<"$spec"
  mut "probe-$nm" "$PBS then .scannerProbe.executions |= (to_entries | map(if .key == 12 then .value |= ($filt) else . end) | map(.value)) else . end"
  st="$(status_of "$WORK/e2.probe-$nm")"
  [[ "$(jq -r --arg f "$fail" '[.budgetFailures[] | select(.failure == $f)] | length > 0' <<<"$st")" == true && "$(jq -r '.invalid | length' <<<"$st")" == 0 ]] || { echo "probe $nm did not report $fail: $(jq -c '[.budgetFailures, .invalid]' <<<"$st" | cut -c1-400)" >&2; exit 1; }
done
# Buffer growth across cursor positions: more than twice the first position's blocks is a (proposed) finding; the same figures are recorded either way.
mut probe-growth "$PBS then .scannerProbe.executions |= (to_entries | map(if .key == 7 then .value.scan.sharedHit = 4000 else . end) | map(.value)) else . end"
st="$(status_of "$WORK/e2.probe-growth")"; [[ "$(jq -r '[.budgetFailures[] | select(.failure == "scanner-buffer-growth")] | length' <<<"$st")" == 1 ]]
mut probe-flat "$PBS then .scannerProbe.executions |= (to_entries | map(if .key == 7 then .value.scan.sharedHit = 1800 else . end) | map(.value)) else . end"
[[ "$(jq -r '[.budgetFailures[] | select(.failure == "scanner-buffer-growth")] | length' <<<"$(status_of "$WORK/e2.probe-flat")")" == 0 ]]
# The probe's parser reads a child scan out of real EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) documents.
cat >"$WORK/explain.docs" <<'JSON'
[{"Plan":{"Node Type":"Limit","Actual Rows":50000,"Actual Loops":1,"Plans":[{"Node Type":"Index Scan","Relation Name":"contact_requests","Index Name":"contact_requests_pkey","Index Cond":"(id > $1)","Actual Rows":50000,"Actual Loops":1,"Shared Hit Blocks":900,"Shared Read Blocks":5}]}}]
[{"Plan":{"Node Type":"Limit","Actual Rows":12,"Actual Loops":1,"Plans":[{"Node Type":"Sort","Plans":[{"Node Type":"Seq Scan","Relation Name":"contact_requests","Filter":"(id > $1)","Rows Removed by Filter":99,"Actual Rows":12,"Actual Loops":1,"Shared Hit Blocks":7000,"Shared Read Blocks":0}]}]}}]
JSON
pp="$(qc1_scanner_probe_parse '[{"mode":"auto","kind":"continuation","afterId":5},{"mode":"force_generic_plan","kind":"continuation","afterId":6}]' <"$WORK/explain.docs")"
[[ "$(jq -c '[.[] | [.mode, .hasSort, .scan.nodeType, .scan.indexCond, .scan.filter, .scan.rows, .scan.loops, .scan.sharedHit, .scan.sharedRead, .scan.rowsRemovedByFilter]]' <<<"$pp")" == '[["auto",false,"Index Scan","(id > $1)",null,50000,1,900,5,0],["force_generic_plan",true,"Seq Scan",null,"(id > $1)",12,1,7000,0,99]]' ]]
# The runner: it runs the REAL statements prepared, eight times per mode in one session, in both plan cache modes, read-only, AFTER the logged capture is off and BEFORE the scenario is dropped.
sp_body="$(sed -n '/^scanner_probe() {/,/^}/p' "$RUNNER_SRC")"
for pat in 'qc1_go_sql "$ROOT" reasonScanInitialSQL' 'qc1_go_sql "$ROOT" reasonScanContinuationSQL' 'for mode in auto force_generic_plan' 'SET plan_cache_mode = $mode' 'PREPARE qi (bigint) AS $sql_init' 'PREPARE qc (bigint, bigint) AS $sql_cont' \
  'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE qi($unit)' 'EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE qc($id, $unit)' 'pcts=(0 10 25 40 55 70 85)' 'OFFSET $off LIMIT 1' 'psql_db -q -f -' 'sameSession:true' 'executionsPerMode:8'; do
  grep -qF -- "$pat" <<<"$sp_body" || { echo "qc1: scanner_probe lost: $pat" >&2; exit 1; }
done
if grep -Eiq 'INSERT|UPDATE|DELETE|TRUNCATE' <<<"$(grep -v '^ *#' <<<"$sp_body" | grep -v 'sameSession\|note:')"; then echo 'qc1: the scanner probe writes' >&2; exit 1; fi
cp_body="$(sed -n '/^capture_plans() {/,/^}/p' "$RUNNER_SRC")"
l_off="$(grep -n 'await_setting auto_explain.log_min_duration -1' <<<"$cp_body" | cut -d: -f1)"; l_log="$(grep -n 'docker logs --since' <<<"$cp_body" | cut -d: -f1)"; l_probe="$(grep -n '^  scanner_probe$' <<<"$cp_body" | cut -d: -f1)"; l_drop="$(grep -n '^  drop_scenarios$' <<<"$cp_body" | tail -n 1 | cut -d: -f1)"
[[ -n "$l_off$l_log$l_probe$l_drop" && "$l_off" -lt "$l_log" && "$l_log" -lt "$l_probe" && "$l_probe" -lt "$l_drop" ]]
grep -qF 'scannerProbe:$probe' <<<"$cp_body"
# (5) The browser measurement starts its clock at the Delete-menu action, on the page, and the stamps are the page's own.
PD="$HERE/qc1/panel-driver.mjs"; PC="$HERE/qc1/panel-core.mjs"
for pat in 'qc.menuAt = e.timeStamp' 'qc.firstPageAt = t' 'qc.walkDoneAt = t' 'qc.walk2DoneAt = t' 'qc.endAt = performance.now()' 'async timeline()' 'async lastClickEnd()' 'MutationObserver(stamp)'; do
  grep -qF -- "$pat" "$PD" || { echo "qc1: panel-driver lost: $pat" >&2; exit 1; }
done
if grep -nE 'Math.round\(now\(\) - t0\)|const t0 = now\(\)' "$PC"; then echo 'qc1: the panel core times from its own loop clock' >&2; exit 1; fi
grep -qF 'since(timeline && timeline.walkDoneAt)' "$PC"; grep -qF 'since(timeline && timeline.firstPageAt)' "$PC"; grep -qF 'since(R.firstInteractiveEnd)' "$PC"

# --- statements: the markers are still the Go source's ------------------------------------
for which in "${QC1_PLAN_KINDS[@]}"; do
  marker="$(qc1_source_marker "$which")"
  [[ -n "$marker" ]]
  grep -qF -- "$marker" "$ROOT/account_erasure_reasons.go" || { echo "qc1: the $which statement changed in account_erasure_reasons.go: $marker" >&2; exit 1; }
  # The log form keeps $n placeholders and has the same shape as the source form.
  [[ "$(qc1_plan_marker "$which" | sed -E 's/\$[0-9]+/?/g')" == "$marker" ]]
done
refused '' qc1_plan_marker nope
# Plans come out whole, selected by their Query Text, and never run into the next log line.
init_q="$(qc1_plan_marker scanner-initial)"; cont_q="$(qc1_plan_marker scanner-continuation)"; items_q="$(qc1_plan_marker page-items)"; del_q="$(qc1_plan_marker delete-set)"
cat >"$WORK/pg.log" <<L
2026-11-07 10:00:00.100 UTC [40] LOG:  statement: SELECT 1
2026-11-07 10:00:01.200 UTC [57] LOG:  duration: 123.4 ms  plan:
	Query Text: SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason $init_q
	Limit  (cost=0.42..1.0 rows=50000) (actual time=0.1..99.0 rows=50000 loops=1)
	  Buffers: shared hit=12
2026-11-07 10:00:01.800 UTC [57] LOG:  duration: 3.0 ms  plan:
	Query Text: SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason $cont_q
	Limit  (cost=0.42..1.0 rows=50000) (actual time=0.1..9.0 rows=50000 loops=1)
	  ->  Index Scan using contact_requests_pkey on contact_requests
	        Index Cond: (id > '50000'::bigint)
2026-11-07 10:00:02.000 UTC [57] LOG:  duration: 1.0 ms  plan:
	Query Text: SELECT id, reviewHash FROM contact_requests $items_q
	Sort
2026-11-07 10:00:02.300 UTC [57] LOG:  duration: 5.0 ms  plan:
	Query Text: SELECT x FROM contact_requests $del_q
	LockRows  (cost=1.0..2.0 rows=3) (actual time=0.1..4.0 rows=3 loops=1)
	  WAL: records=0
2026-11-07 10:00:03.400 UTC [57] LOG:  duration: 9.9 ms  plan:
	Query Text: UPDATE other SET a = 1
	Update on other
2026-11-07 10:00:04.500 UTC [57] LOG:  statement: COMMIT
L
[[ "$(qc1_extract_plans "$WORK/pg.log" scanner-initial | grep -c 'Limit  (cost')" == 1 ]]
[[ "$(qc1_extract_plans "$WORK/pg.log" scanner-initial | grep -c 'Index Cond' || true)" == 0 ]]
# The continuation and initial statements are told apart (the initial form has no WHERE id >), the continuation keeps its Index Cond.
[[ "$(qc1_extract_plans "$WORK/pg.log" scanner-continuation | grep -c 'Index Cond: (id >')" == 1 ]]
[[ "$(qc1_extract_plans "$WORK/pg.log" scanner-continuation | grep -c 'Limit  (cost')" == 1 ]]
[[ "$(qc1_extract_plans "$WORK/pg.log" page-items | grep -c 'Sort')" == 1 ]]
[[ "$(qc1_extract_plans "$WORK/pg.log" delete-set | grep -c 'LockRows')" == 1 ]]
[[ "$(qc1_extract_plans "$WORK/pg.log" delete-set | grep -c 'UPDATE other' || true)" == 0 ]]
[[ -z "$(qc1_extract_plans "$WORK/pg.log" blank-batch)" ]]

# --- the SQL loaders ----------------------------------------------------------------------
# Same columns, in the same order, as the row migrations/reason_review_test.go proves.
line303=$(grep -n 'VALUES(303,3,4' "$ROOT/migrations/reason_review_test.go" | head -n 1 | cut -d: -f1); [[ -n "$line303" ]]
proven="$(sed -n "$((line303-2)),$((line303-1))p" "$ROOT/migrations/reason_review_test.go" | tr -d ' \t\n')"
cols="${proven#*contact_requests(}"; cols="${cols%)}"
for f in filler scenario; do
  got="$(sed -n '/^INSERT INTO contact_requests (/,/^SELECT/p' "$HERE/qc1/$f.sql" | tr -d ' \t\n')"
  got="${got#*contact_requests(}"; got="${got%%)SELECT*}"
  [[ "$got" == "$cols" ]] || { echo "qc1: $f.sql columns differ from the proven row: $got vs $cols" >&2; exit 1; }
  grep -q "^SET session_replication_role = replica;$" "$HERE/qc1/$f.sql"
  grep -q "'denied'" "$HERE/qc1/$f.sql"; grep -q "'group_add'" "$HERE/qc1/$f.sql"
done
# Every psql variable a loader reads is passed by the runner, and none is passed that is not read.
for f in filler scenario; do
  want="$(grep -v '^--' "$HERE/qc1/$f.sql" | sed -E 's/::[a-z_]+//g' | grep -oE ":'?[a-z_]+'?" | tr -d ":'" | sort -u | paste -sd' ')"
  call="$(grep -n "qc1/$f.sql" "$HERE/qc1-measure.sh" | head -n 1 | cut -d: -f1)"
  have="$(sed -n "$((call-1)),${call}p" "$HERE/qc1-measure.sh" | grep -oE ' -v [a-z_]+=' | sed 's/ -v //; s/=//' | sort -u | paste -sd' ')"
  [[ "$want" == "$have" ]] || { echo "qc1: $f.sql reads [$want] but the runner passes [$have]" >&2; exit 1; }
done
# Scenario ids are odd, filler ids even.
grep -q 'SELECT 2 \* g.i,' "$HERE/qc1/filler.sql"
grep -q 'WHEN .tail. THEN 2::bigint \* :n + 2 \* j.j + 1' "$HERE/qc1/scenario.sql"
grep -q 'WHEN .head. THEN 2 \* j.j - 1' "$HERE/qc1/scenario.sql"
grep -q 'ELSE 2 \* ((j.j - 1)::bigint \* :n / :k) + 1' "$HERE/qc1/scenario.sql"
grep -q "DELETE FROM contact_requests WHERE id % 2 = 1" "$HERE/qc1-measure.sh"

# plan counts: a size has its plans only when every statement kind it must reach is found
[[ "$(qc1_plan_counts "$WORK/pg.log")" == '{"scanner-initial":1,"scanner-continuation":1,"page-items":1,"delete-set":1,"blank-batch":0}' ]]
[[ "$(qc1_plan_counts "$WORK/pg.log" | jq 'to_entries | all(.value >= 1)')" == false ]]
[[ "${QC1_PLAN_KINDS[*]}" == 'scanner-initial scanner-continuation page-items delete-set blank-batch' ]]
: >"$WORK/empty.log"; [[ "$(qc1_plan_counts "$WORK/empty.log")" == '{"scanner-initial":0,"scanner-continuation":0,"page-items":0,"delete-set":0,"blank-batch":0}' ]]
# The runner keeps the continuation conditional on the table holding more than one scanner unit, and records the generic plans.
grep -qF 'if $n > $unit then ["scanner-continuation"]' "$HERE/qc1-measure.sh"; grep -q 'EXPLAIN (GENERIC_PLAN)' "$HERE/qc1-measure.sh"
[[ "$QC1_SCANNER_UNIT_ROWS" == 50000 ]]
# the runner turns an incomplete set into a failed row, which the evidence calls missing
grep -q 'RECORD_STATUS=failed' "$HERE/qc1-measure.sh"


# --- the runner ---------------------------------------------------------------------------------
RUNNER="$HERE/qc1-measure.sh"
help="$(bash "$RUNNER" --help)"
for flag in --dir --image --sizes --counts --layout --reason-len --droplet --seconds-per-reason --sitting-minutes --shared-daemon --no-browser --keep; do grep -q -- "$flag" <<<"$help"; done
refused 'unknown option' bash "$RUNNER" --nope
refused 'whole numbers above 0' bash "$RUNNER" --dir "$WORK/never" --sitting-minutes 0
# It needs no root (a rootless or nested daemon is the place to run it) and reads no host paths.
if grep -nE 'id -u|EUID' "$RUNNER"; then echo 'qc1: the runner must not require root' >&2; exit 1; fi
if grep -nE 'APP_PID|/proc/\$|"/sys/fs/cgroup/system.slice|cgroup_file\(\)' "$RUNNER"; then echo 'qc1: the runner reads host process or cgroup paths' >&2; exit 1; fi
# Every cgroup and process read goes through docker exec.
grep -q 'docker exec "\$SIDE_CID" awk' "$RUNNER"; grep -q 'docker exec "\$DB_CID" sh -c "test -r' "$RUNNER"
# The daemon guard comes before anything is created, and refuses a busy daemon.
guard_line="$(grep -n '^if (( RUNNING_AT_START > 0 && ! SHARED )); then' "$RUNNER" | cut -d: -f1)"
mkdir_line="$(grep -n '^mkdir -p "\$WORK"$' "$RUNNER" | cut -d: -f1)"
net_line="$(grep -n 'docker network create' "$RUNNER" | head -n 1 | cut -d: -f1)"
[[ -n "$guard_line" && -n "$mkdir_line" && -n "$net_line" && "$guard_line" -lt "$mkdir_line" && "$mkdir_line" -lt "$net_line" ]]
# A stub docker that reports a busy daemon: the runner refuses, creates nothing and calls nothing else.
mkdir "$WORK/stubbin"
cat >"$WORK/stubbin/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
if [[ "$1" == info ]]; then
  printf '{"ID":"stub","ServerVersion":"27.0","ContainersRunning":%s,"SecurityOptions":["name=seccomp","name=rootless"],"CgroupVersion":"2","CgroupDriver":"systemd","NCPU":1,"MemTotal":2147483648}\n' "${STUB_RUNNING:-3}"
fi
exit 0
STUB
chmod +x "$WORK/stubbin/docker"
: >"$WORK/stub.log"
rc=0; STUB_LOG="$WORK/stub.log" STUB_RUNNING=3 PATH="$WORK/stubbin:$PATH" bash "$RUNNER" --dir "$WORK/never" >"$WORK/busy.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && grep -q 'already runs 3 container' "$WORK/busy.out" && grep -q 'disposable nested or rootless daemon' "$WORK/busy.out"
[[ ! -e "$WORK/never" ]]
[[ "$(wc -l <"$WORK/stub.log")" == 1 && "$(head -n 1 "$WORK/stub.log")" == info* ]] || { echo 'qc1: the busy-daemon refusal called docker beyond info:' >&2; cat "$WORK/stub.log" >&2; exit 1; }
# An idle daemon passes the guard; a dirty tree or a missing image id then stops the run before any container, network or volume is made.
: >"$WORK/stub.log"
rc=0; STUB_LOG="$WORK/stub.log" STUB_RUNNING=0 PATH="$WORK/stubbin:$PATH" bash "$RUNNER" --dir "$WORK/idle" >"$WORK/idle.out" 2>&1 || rc=$?
[[ "$rc" != 0 ]]
if grep -qE '^(run|rm|network|volume|pull|system|container|kill|stop|exec|prune)( |$)' "$WORK/stub.log"; then echo 'qc1: the run touched the daemon before its identity checks:' >&2; cat "$WORK/stub.log" >&2; exit 1; fi
# The identity check on a clean tree is the image label: a stub image labelled with
# another revision is refused. The runner runs from a clean copy of its own files
# in a throwaway repository, so this holds whatever state the real tree is in.
CLONE="$WORK/clone"; mkdir -p "$CLONE/deploy/digitalocean/qc1"
cp "$HERE/qc1-measure.sh" "$CLONE/deploy/digitalocean/"; cp "$HERE"/qc1/lib.sh "$HERE"/qc1/filler.sql "$HERE"/qc1/scenario.sql "$HERE"/qc1/panel-measure.mjs "$HERE"/qc1/browser.mjs "$HERE"/qc1/browser-launch.cjs "$HERE"/qc1/browser-preflight.mjs "$HERE"/qc1/response-accounting.mjs "$HERE"/qc1/set-v1.mjs "$HERE"/qc1/panel-core.mjs "$HERE"/qc1/panel-driver.mjs "$CLONE/deploy/digitalocean/qc1/"; mkdir -p "$CLONE/web/src/lib"; cp "$ROOT/web/src/lib/reasonSetVectors.json" "$CLONE/web/src/lib/"; cp "$HERE/isolation.sh" "$CLONE/deploy/digitalocean/"
cp "$ROOT/api_profile.go" "$ROOT/main.go" "$CLONE/"
git init -q "$CLONE"; git -C "$CLONE" config user.name fixture; git -C "$CLONE" config user.email fixture@example.invalid
git -C "$CLONE" config commit.gpgsign false; git -C "$CLONE" config core.hooksPath /dev/null
git -C "$CLONE" add -A; git -C "$CLONE" commit -qm clone
CLONE_HEAD="$(git -C "$CLONE" rev-parse HEAD)"
cat >"$WORK/stubbin/docker" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$STUB_LOG"
case "$1 $2" in
  "info --format") printf '{"ID":"stub","ContainersRunning":0,"SecurityOptions":[],"CgroupVersion":"2"}\n' ;;
  "image inspect")
    case "$*" in
      *"index .Config.Labels"*) echo "${STUB_LABEL}" ;;
      *"{{.Id}}"*) echo sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc ;;
    esac ;;
esac
exit 0
STUB
: >"$WORK/stub.log"
rc=0; STUB_LOG="$WORK/stub.log" STUB_LABEL=0000000000000000000000000000000000000000 PATH="$WORK/stubbin:$PATH" \
  bash "$CLONE/deploy/digitalocean/qc1-measure.sh" --dir "$WORK/labelled" --no-browser --image qc1-supplied:latest >"$WORK/label.out" 2>&1 || rc=$?
if ! { [[ "$rc" == 1 ]] && grep -q "is labelled revision '0000000000000000000000000000000000000000', not HEAD $CLONE_HEAD" "$WORK/label.out"; }; then cat "$WORK/label.out" >&2; exit 1; fi
if grep -qE '^(run|rm|network|volume|pull|build)( |$)' "$WORK/stub.log"; then echo 'qc1: a mislabelled image started something:' >&2; cat "$WORK/stub.log" >&2; exit 1; fi
# The same stub labelled with HEAD gets past the label and stops at the level check (the stub has no binary): still nothing started.
: >"$WORK/stub.log"
rc=0; STUB_LOG="$WORK/stub.log" STUB_LABEL="$CLONE_HEAD" PATH="$WORK/stubbin:$PATH" \
  bash "$CLONE/deploy/digitalocean/qc1-measure.sh" --dir "$WORK/labelled2" --no-browser --image qc1-supplied:latest >"$WORK/label2.out" 2>&1 || rc=$?
[[ "$rc" != 0 ]] && ! grep -q 'is labelled revision' "$WORK/label2.out"
grep -q '^run --rm --network none --pull never --entrypoint /fold sha256:c' "$WORK/stub.log"
if grep -qE '^(rm|network|volume|pull|build)( |$)' "$WORK/stub.log"; then echo 'qc1: a bound image started a stack before the level check' >&2; cat "$WORK/stub.log" >&2; exit 1; fi
# A dirty tree is refused before any image is looked at: edit the clone's runner uncommitted.
printf '\n# dirty\n' >>"$CLONE/deploy/digitalocean/qc1-measure.sh"
: >"$WORK/stub.log"
rc=0; STUB_LOG="$WORK/stub.log" PATH="$WORK/stubbin:$PATH" bash "$CLONE/deploy/digitalocean/qc1-measure.sh" --dir "$WORK/dirty" --no-browser --image qc1-supplied:latest >"$WORK/dirty.out" 2>&1 || rc=$?
[[ "$rc" == 1 ]] && grep -q 'the tree is dirty' "$WORK/dirty.out"
[[ "$(cat "$WORK/stub.log")" == info* && "$(wc -l <"$WORK/stub.log")" == 1 ]]
git -C "$CLONE" checkout -q -- deploy/digitalocean/qc1-measure.sh
# The guard admits --shared-daemon only on its own flag, and only recorded as shared.
grep -q 'sharedDaemon:\$shared' "$RUNNER"
# Identity: HEAD, clean tree, immutable ids for every container, ids recorded, sources hashed.
grep -q "git --no-optional-locks -C \"\$ROOT\" status --porcelain" "$RUNNER"
grep -q '^SOURCES="\$(qc1_source_hashes "\$ROOT")"' "$RUNNER"
grep -q 'IMAGE_LABEL" == "\$REVISION"' "$RUNNER"
grep -q '"\$IMAGE_ID")"' "$RUNNER"; grep -q '"\$PG_IMAGE_ID"' "$RUNNER"
if grep -nE 'docker run .*"\$IMAGE"( |$)' "$RUNNER" | grep -v 'docker build'; then echo 'qc1: a container is started from a tag, not an image id' >&2; exit 1; fi
if grep -nE 'docker run .*postgres:16-alpine|docker run .* "\$PG_IMAGE"( |$)' "$RUNNER"; then echo 'qc1: a container is started from the Postgres tag' >&2; exit 1; fi
grep -q 'image_ran" == "\$IMAGE_ID"' "$RUNNER"
grep -q "dbImageId:\|--arg dbImage" "$RUNNER"; grep -q 'appContainers' "$RUNNER"; grep -q 'postgresRepoDigests' "$RUNNER"
# Nothing is removed by name, pattern or prune; the host stack is never named.
if grep -nE 'docker (rm|kill|stop)[^"]*qc1-|docker system|docker container prune|docker volume prune|docker network prune|docker compose|--name ' "$RUNNER"; then echo 'qc1: the runner removes or names something other than by id' >&2; exit 1; fi
grep -q 'for id in "\${CONTAINERS\[@\]}"; do docker rm -fv "\$id"' "$RUNNER"
# Attribution: workers off for every attributable row, autovacuum off, quiesce before each operation, calibrate before measuring.
grep -q '^start_app 0$' "$RUNNER"; grep -q 'ALTER SYSTEM SET autovacuum = off' "$RUNNER"; grep -q "checkpoint_timeout = '1d'" "$RUNNER"
grep -q '^instrumented() {' "$RUNNER"; instr_line="$(grep -n '^instrumented() {' "$RUNNER" | cut -d: -f1)"
[[ "$(sed -n "$((instr_line + 1)),$((instr_line + 20))p" "$RUNNER" | grep -c "^  quiesce$")" == 1 ]]
cal_line="$(grep -n '^calibrate$' "$RUNNER" | cut -d: -f1)"; first_measure="$(grep -n '^for n in "\${SIZE_LIST\[@\]}"; do measure_size' "$RUNNER" | cut -d: -f1)"
[[ -n "$cal_line" && "$cal_line" -lt "$first_measure" ]]
grep -q 'start_app 2' "$RUNNER"; grep -q 'RECORD_STATUS=observation record 9 workers-on-delete' "$RUNNER"
grep -q 'if \[\[ "\$n" == "\$MAX_SIZE" \]\]; then observe_workers_on' "$RUNNER"
# The observation restores the workers-off app before the next measurement.
obs_start="$(grep -n '^observe_workers_on() {' "$RUNNER" | cut -d: -f1)"
sed -n "$obs_start,/^}/p" "$RUNNER" | tail -n 4 | grep -q 'start_app 0'
# Commit, WAL and gate are labelled for what they are.
for label in cluster-wide 'not a hold time' 'commitMsMax is the slowest COMMIT' walControlBytes 'not attributable' 'FOLD_WORKERS=0 turns the job workers off'; do grep -qF -- "$label" "$RUNNER"; done
# The verdict never hides a failed judge or an incomplete run.
if grep -nE 'qc1_judge .*\|\| true' "$RUNNER"; then echo 'qc1: a judge failure is swallowed' >&2; exit 1; fi
grep -q 'JUDGE_OK=false' "$RUNNER"; grep -q 'qc1_evidence_status "\$MEASUREMENTS" "\$SIZES" "\$COUNTS" "\$WORK/environment.json" "\$REVISION" "\$MAX_REQUEST_BYTES"' "$RUNNER"
# It measures; it does not gate.
grep -q "ALTER SYSTEM SET auto_explain.log_min_duration = -1" "$RUNNER"
grep -q '"pending", item:4\|status:"pending", item:4' "$RUNNER"
# --- the rendered panel (V-07): a real browser against the real API -------------------------------------------
PM="$HERE/qc1/panel-measure.mjs"; PC="$HERE/qc1/panel-core.mjs"; PD="$HERE/qc1/panel-driver.mjs"
if command -v node >/dev/null; then
  for m in "$PM" "$PC" "$PD"; do node --check "$m"; done
  # The sequence, readiness, click and navigation bookkeeping run against a fake paginated panel on a virtual clock (no browser).
  node "$HERE/qc1-panel-fixtures.mjs" >"$WORK/panel-fixtures.out" 2>&1 || { cat "$WORK/panel-fixtures.out" >&2; echo 'qc1: the panel fixtures failed' >&2; exit 1; }
  grep -q 'qc1 panel fixtures: ok' "$WORK/panel-fixtures.out"
fi
for pat in 'pw.chromium.launch(launchOptions(env))' 'resolvePlaywright(env.QC1_WEB_DIR)' 'totalBodyBytes(sizes)' 'walkOrigin(walk, walk.bytes, sizes.length)' 'measurePanel(driver' 'process.exit(3)' '/erasure-reasons/page' 'QC1_CHANGE_CMD' 'QC1_NO_SUBTLE' 'QC1_CLICKS'; do
  grep -qF -- "$pat" "$PM" || { echo "qc1: panel-measure.mjs lost: $pat" >&2; exit 1; }
done
for pat in 'ul[aria-label="Contact-request reasons"] > li[data-reason-id]' 'nav[aria-label="Reasons pages"] p' 'Checking their contact-request reasons…' 'Performance.getMetrics' 'HeapProfiler.collectGarbage' 'PerformanceObserver' \
  '"Next 50"' '"Previous 50"' 'Go to reason number' 'Next reason without a choice' 'Next changed reason' 'Delete account' 'pointerdown' 'requestAnimationFrame' 'data-reason-heading' 'isSecureContext'; do
  grep -qF -- "$pat" "$PD" || { echo "qc1: panel-driver.mjs lost: $pat" >&2; exit 1; }
done
for pat in 'FIRST_CLICK_MS = 200' 'CLICKS_DEFAULT = 30' 'DISPLAY_PAGE = 50' 'walkEnded' 'firstInteractiveMs' 'canChange'; do
  grep -qF -- "$pat" "$PC" || { echo "qc1: panel-core.mjs lost: $pat" >&2; exit 1; }
done
# Nothing is mocked: no route is intercepted.
if grep -nE 'page\.route|context\.route|route\.fulfill' "$PM" "$PC" "$PD"; then echo 'qc1: the browser measurement intercepts routes' >&2; exit 1; fi
# The harness chooses with the panel's own per-reason handler, one radio at a time: no bulk control exists and none is used.
if grep -nEi 'choose all|bulk|select all' "$PD" "$PC" | grep -v '^[^:]*:[0-9]*: *//'; then echo 'qc1: the browser measurement uses a bulk control' >&2; exit 1; fi
# The selectors and strings it relies on are the ones the panel renders.
grep -qF 'data-reason-id={item.requestId}' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'data-reason-heading={ordinal}' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'aria-label="Contact-request reasons"' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'label="Keep text"' "$ROOT/web/src/components/ReasonReviewList.tsx"; grep -qF 'label="Blank text"' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'const NOT_CHOSEN_COPY = "Not chosen yet";' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'export const CHANGED_COPY = "Changed since you looked: choose again";' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'nav aria-label="Reasons pages"' "$ROOT/web/src/components/ReasonReviewSection.tsx"
grep -qF 'export const DISPLAY_PAGE = 50;' "$ROOT/web/src/components/ReasonReviewSection.tsx"
for label in 'Next reason without a choice' 'Next changed reason' 'Go to reason number'; do grep -qF -- "$label" "$ROOT/web/src/components/ReasonReviewSection.tsx"; done
grep -qF 'Previous {DISPLAY_PAGE}' "$ROOT/web/src/components/ReasonReviewSection.tsx"; grep -qF 'Next {DISPLAY_PAGE}' "$ROOT/web/src/components/ReasonReviewSection.tsx"
grep -qF 'const unsure = loading ? "of at least " : "of ";' "$ROOT/web/src/components/ReasonReviewSection.tsx"
grep -qF 'const REVIEW_CHECKING = "Checking their contact-request reasons…";' "$ROOT/web/src/components/DeleteUserPanel.tsx"
grep -qF 'name: "Search by name or email"' "$ROOT/web/e2e/admin-delete-user.spec.ts"
# The runner measures the counts it lists (plus the dense targets), with the real API, and a missing browser leaves the row missing (never invented).
grep -qF 'for bk in "${ALL_BROWSER_LIST[@]}"; do (( bk <= n )) && measure_browser_panel "$n" "$bk"; done' "$RUNNER"
grep -q 'node "\$HERE/qc1/panel-measure.mjs"' "$RUNNER"
sed -n '/^measure_browser_panel() {/,/^}/p' "$RUNNER" | grep -q 'the row is missing and the run is not complete'
sed -n '/^measure_browser_panel() {/,/^}/p' "$RUNNER" | grep -q '(( BROWSER )) || return 0'
mbp="$(sed -n '/^measure_browser_panel() {/,/^}/p' "$RUNNER")"; rpm="$(sed -n '/^run_panel_measure() {/,/^}/p' "$RUNNER")"
grep -qF 'QC1_CHANGE_CMD="$PANEL_HOOK" QC1_PHASE_CMD="$PANEL_PHASE"' <<<"$rpm"
# The whole run is one instrumented window (WAL, commits, sampled gate, audit by action), recorded as the row's window; a run that stopped part-way (exit 6) is a PARTIAL row.
grep -qF 'instrumented run_panel_measure' <<<"$mbp"; grep -qF 'window:$window' <<<"$mbp"
grep -qF 'RECORD_STATUS=partial record 10' <<<"$mbp"; grep -qF 'failed:$b[0].failed' <<<"$mbp"; grep -qF 'PANEL_RC == 6' <<<"$mbp"
# The phase hook only READS (WAL position, audit rows, bytes and rows by action); the change hook only edits one seeded reason.
grep -qF "pg_wal_lsn_diff(pg_current_wal_lsn(), '0/0')" <<<"$mbp"; grep -qF 'json_object_agg(action, c)' <<<"$mbp"
if sed -n '/^  cat >"$PANEL_PHASE" <<SH/,/^SH$/p' "$RUNNER" | grep -Eiq 'UPDATE|DELETE|INSERT|TRUNCATE|session_replication_role'; then echo 'qc1: the phase hook writes' >&2; exit 1; fi
# The hook only edits one seeded reason of the harness's own table, by a numeric id, and says so.
grep -qF 'case "$1" in *[!0-9]*|"") exit 2 ;; esac' <<<"$mbp"
# (the default browser counts are asserted above, before the fixture narrows them)
# Measured and assumed are kept apart in the item-7 row.
for pat in 'harnessPageLoad' 'renderedPanel' 'humanAssumptions' 'not a rendered panel'; do grep -qF -- "$pat" "$RUNNER"; done
# A browser row carries its tool and its note; the harness figures name themselves a shell wall time.
grep -qF 'headless Chromium' "$PM"

# --- clearing the background rows: the foreign-key set (pilot: contact_grants made `TRUNCATE contact_requests` fail) -----------------------------------------------------------
# What references contact_requests in the schema, from the migrations (the baseline spells every foreign key as an ALTER TABLE ONLY ... ADD CONSTRAINT ... FOREIGN KEY).
fk_edges() { # prints "child parent" for every foreign key in the baseline
  awk '/^ALTER TABLE ONLY public\./ { child = $4; sub(/^public\./, "", child) }
       /FOREIGN KEY/ && match($0, /REFERENCES public\.[a-z_0-9]+/) { parent = substr($0, RSTART + 18, RLENGTH - 18); print child, parent }' "$ROOT/migrations/00001_baseline.sql"
}
fk_closure() { # root -> the root and every table that references it, transitively
  local -a todo=("$1"); local -A seen=(); local t c p
  while ((${#todo[@]})); do
    t="${todo[0]}"; todo=("${todo[@]:1}")
    [[ -z "${seen[$t]:-}" ]] || continue
    seen[$t]=1
    while read -r c p; do [[ "$p" != "$t" ]] || todo+=("$c"); done < <(fk_edges)
  done
  printf '%s\n' "${!seen[@]}" | LC_ALL=C sort
}
[[ "$(fk_closure contact_requests | paste -sd,)" == contact_grants,contact_requests,registrations ]] \
  || { echo "the tables that reference contact_requests changed: $(fk_closure contact_requests | paste -sd,). Review qc1_reset_contact_requests (it adapts by itself, but a new referrer may hold rows)." >&2; exit 1; }
# contact_requests references nothing, so the filler's parties (ids far above any account) are valid rows and need no stand-in users
[[ -z "$(fk_edges | awk '$1 == "contact_requests"')" ]]
# no later migration adds a reference to the set (the baseline is the only place the foreign keys are written, and the closure above is complete)
if grep -lE 'REFERENCES +(public\.)?(contact_requests|contact_grants|registrations)\b' "$ROOT"/migrations/*.sql | grep -v '00001_baseline.sql'; then echo 'a migration after the baseline references the contact-request tables: re-check the QC-1 loader' >&2; exit 1; fi

# The loader against a model of the database: a catalog that lists the closure, row counts, and every statement recorded.
export QC1_MODEL_REFERRERS="" QC1_MODEL_LOG="$WORK/sql.log"
psql_db() { # -c SQL
  local sql="$2"
  printf '%s\n' "$sql" >>"$QC1_MODEL_LOG"
  case "$sql" in
    "WITH RECURSIVE r(oid) AS"*) [[ -z "${QC1_MODEL_FAIL_CATALOG:-}" ]] || return 1; printf '%s\n' "$QC1_MODEL_REFERRERS" ;;
    "SELECT count(*) FROM "*) local t="${sql#SELECT count(*) FROM }"; local v="QC1_MODEL_ROWS_${t//[^a-z_0-9]/_}"; printf '%s\n' "${!v:-0}" ;;
    "TRUNCATE "*) [[ -z "${QC1_MODEL_FAIL_TRUNCATE:-}" ]] || return 1 ;;
    *) echo "unexpected sql: $sql" >&2; return 1 ;;
  esac
}
model() { : >"$QC1_MODEL_LOG"; unset QC1_MODEL_FAIL_CATALOG QC1_MODEL_FAIL_TRUNCATE QC1_MODEL_ROWS_contact_grants QC1_MODEL_ROWS_registrations QC1_MODEL_ROWS_grant_notes; }
truncates() { grep -c '^TRUNCATE ' "$QC1_MODEL_LOG" || true; }
closure_text="$(fk_closure contact_requests)"
# the SQL asks the catalog for the transitive foreign-key set of the real table
grep -qF "WITH RECURSIVE r(oid) AS (SELECT 'public.contact_requests'::regclass::oid UNION SELECT c.conrelid FROM pg_constraint c JOIN r ON c.confrelid = r.oid WHERE c.contype = 'f')" "$HERE/qc1/lib.sh"
# (1) empty referrers: one TRUNCATE naming the whole set, no CASCADE, no DELETE, nothing before the checks
model; QC1_MODEL_REFERRERS="$closure_text"
qc1_reset_contact_requests
[[ "$(truncates)" == 1 ]]; [[ "$(grep '^TRUNCATE ' "$QC1_MODEL_LOG")" == 'TRUNCATE contact_grants, contact_requests, registrations' ]]
if grep -qiE 'CASCADE|DELETE|RESTART IDENTITY' "$QC1_MODEL_LOG"; then echo 'the reset deletes, cascades or restarts identities' >&2; exit 1; fi
[[ "$(grep -n '^SELECT count' "$QC1_MODEL_LOG" | tail -n 1 | cut -d: -f1)" -lt "$(grep -n '^TRUNCATE ' "$QC1_MODEL_LOG" | cut -d: -f1)" ]]    # every count before the statement
[[ "$(grep -c '^SELECT count(\*) FROM ' "$QC1_MODEL_LOG")" == 2 ]]                                                                              # both referrers, not contact_requests itself
# (2) a referrer that holds rows: refused, named, nothing truncated
for t in contact_grants registrations; do
  model; QC1_MODEL_REFERRERS="$closure_text"; export "QC1_MODEL_ROWS_$t=3"
  refused "$t holds 3 rows that depend on contact_requests" qc1_reset_contact_requests
  [[ "$(truncates)" == 0 ]]
done
# (3) a table reached only through another referrer is found and checked too
model; QC1_MODEL_REFERRERS="$(printf '%s\n' "$closure_text" grant_notes | LC_ALL=C sort)"
qc1_reset_contact_requests
[[ "$(grep '^TRUNCATE ' "$QC1_MODEL_LOG")" == 'TRUNCATE contact_grants, contact_requests, grant_notes, registrations' ]]
model; QC1_MODEL_REFERRERS="$(printf '%s\n' "$closure_text" grant_notes | LC_ALL=C sort)"; export QC1_MODEL_ROWS_grant_notes=1
refused 'grant_notes holds 1 rows' qc1_reset_contact_requests; [[ "$(truncates)" == 0 ]]
# (4) the catalog cannot be read, lists a table in another schema or a quoted name, omits the table itself, or a count is unreadable: refused, nothing truncated
model; QC1_MODEL_REFERRERS="$closure_text"; export QC1_MODEL_FAIL_CATALOG=1
refused 'cannot list the tables' qc1_reset_contact_requests; [[ "$(truncates)" == 0 ]]
model; QC1_MODEL_REFERRERS="$(printf '%s\n' "$closure_text" other.t)"; refused "unexpected table name 'other.t'" qc1_reset_contact_requests; [[ "$(truncates)" == 0 ]]
model; QC1_MODEL_REFERRERS="$(printf '%s\n' "$closure_text" '"Odd Name"')"; refused 'unexpected table name' qc1_reset_contact_requests; [[ "$(truncates)" == 0 ]]
model; QC1_MODEL_REFERRERS="$(printf '%s\n' contact_grants registrations)"; refused 'not in the foreign-key closure' qc1_reset_contact_requests; [[ "$(truncates)" == 0 ]]
model; QC1_MODEL_REFERRERS=""; refused 'not in the foreign-key closure' qc1_reset_contact_requests
model; QC1_MODEL_REFERRERS="$closure_text"; export QC1_MODEL_ROWS_contact_grants=many; refused 'unreadable row count for contact_grants' qc1_reset_contact_requests; [[ "$(truncates)" == 0 ]]
# (5) TRUNCATE itself failing is reported
model; QC1_MODEL_REFERRERS="$closure_text"; export QC1_MODEL_FAIL_TRUNCATE=1
refused 'TRUNCATE contact_grants, contact_requests, registrations failed' qc1_reset_contact_requests
unset -f psql_db model truncates
# The runner uses it, once per size, before the filler loads, and checks what the clear must not touch; the bare table-only TRUNCATE is gone.
RUNNER_BODY="$(sed -n '/^measure_size() {/,/^}$/p' "$HERE/qc1-measure.sh")"
if grep -n "TRUNCATE contact_requests" "$HERE/qc1-measure.sh"; then echo 'the runner still truncates contact_requests alone' >&2; exit 1; fi
r_l="$(grep -n 'qc1_reset_contact_requests || exit 1' <<<"$RUNNER_BODY" | head -n 1 | cut -d: -f1)"; f_l="$(grep -n 'load_filler "\$n" all' <<<"$RUNNER_BODY" | head -n 1 | cut -d: -f1)"
s_l="$(grep -n "admin@example.invalid'\")\" == 1" <<<"$RUNNER_BODY" | head -n 1 | cut -d: -f1)"
[[ -n "$r_l$f_l$s_l" && "$r_l" -lt "$s_l" && "$s_l" -lt "$f_l" ]]
for pat in "FROM groups WHERE id = \$GROUP" "qc1-counterpart-%@example.invalid'\")\" == 10" "SELECT count(*) FROM contact_requests')\" == 0"; do grep -qF -- "$pat" <<<"$RUNNER_BODY" || { echo "the runner no longer checks $pat" >&2; exit 1; }; done

# --- pilot2: the outcome of ONE deletion, the app's peak, and the plan capture -------------------------------------------------------------------------------------------------------
RUNNER_SRC="$HERE/qc1-measure.sh"
# (1) Audits bound to the deletion. Scenario request ids are reused (rows dropped and rebuilt), audit_logs keeps all earlier deletions' rows.
sql="$(qc1_outcome_sql 42 '1,3,5' 77)"
grep -qF "FROM audit_logs WHERE id > 77 AND action IN ('contact.reason_kept','contact.reason_removed','add.reason_kept','add.reason_removed') AND target_id IN (1,3,5)" <<<"$sql"
grep -qF "WHERE id IN (1,3,5) AND reason = ''" <<<"$sql"; grep -qF "WHERE id IN (1,3,5) AND reason <> ''" <<<"$sql"; grep -qF "FROM users WHERE id = 42" <<<"$sql"
[[ "$(qc1_outcome_sql 42 -1 0 | grep -c 'IN (-1)')" == 3 ]]
while IFS='|' read -r a b c; do
  if qc1_outcome_sql "$a" "$b" "$c" >/dev/null 2>&1; then echo "qc1_outcome_sql accepted: $a $b $c" >&2; exit 1; fi
done <<'BAD'
x|1,3|5
42|1,3|five
42|1,,3|5
42|1;DROP|5
42|1,3|-5
4a2|1|5
42||5
42|1,3|
BAD
qc1_outcome_ok true 5 5 10 5 5
qc1_outcome_ok true 0 10 10 10 0; qc1_outcome_ok true 10 0 10 0 10; qc1_outcome_ok true 0 0 0 0 0
for bad in "false 5 5 10 5 5" "true 5 5 20 5 5" "true 5 5 9 5 5" "true 4 6 10 5 5" "true 5 5 10 6 4" "missing 5 5 10 5 5" "true x 5 10 5 5" "true 5 5 '' 5 5"; do
  # shellcheck disable=SC2086
  if eval "qc1_outcome_ok $bad" 2>/dev/null; then echo "qc1_outcome_ok accepted: $bad" >&2; exit 1; fi
done
# the real query, run against a model table through successive scenarios that reuse the same request ids
if command -v sqlite3 >/dev/null; then
  db="$WORK/audit.sqlite"
  sqlite3 "$db" 'CREATE TABLE audit_logs (id INTEGER PRIMARY KEY, action TEXT NOT NULL, target_id INTEGER NOT NULL)'
  old_sql() { printf "SELECT count(*) FROM audit_logs WHERE action IN ('contact.reason_kept','contact.reason_removed','add.reason_kept','add.reason_removed') AND target_id IN (%s)" "$1"; }
  for k in 10 10 100 10 1000; do
    ids="$(seq -s, 1 2 $((2 * k - 1)))"
    mark="$(sqlite3 "$db" 'SELECT coalesce(max(id), 0) FROM audit_logs')"
    # noise that must never count: other actions on the same ids, other targets, and an earlier deletion's rows
    sqlite3 "$db" "INSERT INTO audit_logs(action, target_id) VALUES ('group.approved', 1), ('contact.reason_kept', 999999)"
    mark2="$(sqlite3 "$db" 'SELECT coalesce(max(id), 0) FROM audit_logs')"
    # this deletion: half kept, half removed (or all kept for 10)
    sqlite3 "$db" "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < $k) INSERT INTO audit_logs(action, target_id) SELECT CASE WHEN i % 2 = 0 THEN 'add.reason_removed' ELSE 'add.reason_kept' END, 2 * i - 1 FROM n"
    bound="$(sqlite3 "$db" "$(qc1_audit_count_sql "$ids" "$mark2")")"
    unbound="$(sqlite3 "$db" "$(old_sql "$ids")")"
    [[ "$bound" == "$k" ]] || { echo "bound audit count $bound, expected $k (mark $mark2)" >&2; exit 1; }
    (( mark == 0 )) || [[ "$unbound" != "$k" ]] || { echo "the unbound count should have accumulated, got $unbound for $k" >&2; exit 1; }   # the pilot2 shape
  done
  # accumulated exactly as in the pilot: 10 + 10 = 20 for the second scenario's ids when unbound
  [[ "$(sqlite3 "$db" "$(old_sql '1,3,5,7,9,11,13,15,17,19')")" == 50 ]]
  # a watermark in the future counts nothing; mark 0 counts everything
  [[ "$(sqlite3 "$db" "$(qc1_audit_count_sql '1,3,5' 999999999)")" == 0 ]]
else
  echo 'note: sqlite3 is not installed; the audit-binding query was checked as text only' >&2
fi
# the runner: watermark read as a number before the delete, passed to the outcome, and no unbound count left
d_body="$(sed -n '/^delete_with() {/,/^}/p' "$RUNNER_SRC")"
m_l="$(grep -n "SELECT coalesce(max(id), 0) FROM audit_logs" <<<"$d_body" | head -n 1 | cut -d: -f1)"; x_l="$(grep -n 'instrumented do_delete' <<<"$d_body" | head -n 1 | cut -d: -f1)"; v_l="$(grep -n 'verify_outcome "\$t" "\$file" "\$kept" "\$removed" "\$audit_mark"' <<<"$d_body" | head -n 1 | cut -d: -f1)"
[[ -n "$m_l$x_l$v_l" && "$m_l" -lt "$x_l" && "$x_l" -lt "$v_l" ]]
grep -qF 'audit_mark" =~ ^[0-9]+$' <<<"$d_body"
vo_body="$(sed -n '/^verify_outcome() {/,/^}/p' "$RUNNER_SRC")"
grep -qF 'qc1_outcome_sql "$t" "$ids" "$mark" | psql_stdin' <<<"$vo_body"; grep -qF 'qc1_outcome_ok "$deleted" "$blank" "$nonblank" "$audits" "$kept" "$removed"' <<<"$vo_body"
# Only the watermark and the before/after growth record (rows and bytes of audit_logs around each operation: the confounding the run leaves) read it.
# (and the by-action growth record audit_actions, and the phase hook's read, which write nothing and are not decision counts)
if grep -n 'FROM audit_logs' "$RUNNER_SRC" | grep -v 'max(id)' | grep -v "count(\*) || ' ' || pg_total_relation_size('audit_logs') FROM audit_logs" | grep -v '^[0-9]*:audit_actions()' | grep -v '^[0-9]*:exec docker exec -i $DB_CID psql -X -U church -d fold -At'; then echo 'the runner counts audits outside qc1_audit_count_sql' >&2; exit 1; fi
[[ "$(grep -c "pg_total_relation_size('audit_logs')" "$RUNNER_SRC")" == 3 ]]   # the two reads around an operation and the phase hook's
grep -qF 'AUDIT_ROWS0 AUDIT_BYTES0' "$RUNNER_SRC"; grep -qF 'AUDIT_ROWS1 AUDIT_BYTES1' "$RUNNER_SRC"
# the judge still requires the database outcome: the pilot2 rows (accumulated audits) are invalid, bound rows are not
mut dbbad "$DEL then .outcome = {dbOk:false, accountDeleted:\"true\", reasonsBlank:50, reasonsWithText:50, decisionAudits:1260} else . end"; expect_invalid dbbad outcome-db

# (2) The app's peak over an operation.
printf '100 5000\n120 5000\n' >"$WORK/app.a"
[[ "$(qc1_app_peak 5000 5000 "$WORK/app.a")" == '0 2 false' ]]
printf '6000 6100\n7000 7000\n' >"$WORK/app.b"
[[ "$(qc1_app_peak 5000 7000 "$WORK/app.b")" == '2000 2 false' ]]             # the sampled high-water mark wins over the final one
[[ "$(qc1_app_peak 5000 6500 "$WORK/app.b")" == '2000 2 false' ]]
printf '5200 5200\n' >"$WORK/app.c"
[[ "$(qc1_app_peak 5000 8000 "$WORK/app.c")" == '3000 1 false' ]]             # the final reading when it is the highest
[[ "$(qc1_app_peak 9000 8000 "$WORK/app.c")" == '0 1 true' ]]                 # pilot2: the final VmHWM fell below the one read after the reset: no growth, flagged, not "missing"
: >"$WORK/app.none"
[[ "$(qc1_app_peak 9000 8000 "$WORK/app.none")" == '0 0 true' ]]
[[ "$(qc1_app_peak 5000 7000 "$WORK/app.none")" == '2000 0 false' ]]
[[ "$(qc1_app_peak 5000 '' "$WORK/app.b")" == '2000 2 false' ]]               # one failed read of the end value does not lose the window
[[ "$(qc1_app_peak 5000 '' "$WORK/app.none")" == 'null 0 false' ]]            # nothing was read at all: missing
[[ "$(qc1_app_peak '' 7000 "$WORK/app.b")" == 'null 2 false' ]]               # no baseline: missing
[[ "$(qc1_app_peak x 7000 "$WORK/app.b")" == 'null 2 false' ]]
printf 'garbage\n 1 2 3\n6000 6000\n\n' >"$WORK/app.d"
[[ "$(qc1_app_peak 5000 5000 "$WORK/app.d")" == '1000 1 false' ]]             # partial or malformed lines are ignored
# the judge: a numeric peak (including 0) is evidence, a null one is not (existing rule, unchanged)
mut peak0 "$DEL then .delete.appPeakKb = 0 | .delete.appHwmBelowBaseline = true else . end"; [[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.peak0")")" == true ]]
mut peaknull "$DEL then .delete.appPeakKb = null | .delete.appHwmBelowBaseline = true else . end"; expect_invalid peaknull metric-missing
# the instrument: sampled during the window, stopped after it, with the baseline read after the reset and retried reads
i_body="$(sed -n '/^instrumented() {/,/^}/p' "$RUNNER_SRC")"
s_l="$(grep -n 'app_mem_start "\$WORK/mem.app"' <<<"$i_body" | cut -d: -f1)"; o_l="$(grep -n '"\$@"$' <<<"$i_body" | head -n 1 | cut -d: -f1)"; e_l="$(grep -n 'app_mem_stop' <<<"$i_body" | cut -d: -f1)"
r_l="$(grep -n 'hwm_reset="\$(app_status_kb VmHWM' <<<"$i_body" | cut -d: -f1)"; p_l="$(grep -n 'qc1_app_peak "\$hwm_reset" "\$hwm_end" "\$WORK/mem.app"' <<<"$i_body" | cut -d: -f1)"
[[ -n "$s_l$o_l$e_l$r_l$p_l" && "$r_l" -lt "$s_l" && "$s_l" -lt "$o_l" && "$o_l" -lt "$e_l" && "$e_l" -lt "$p_l" ]]
grep -qF "APP_HWM_RESET\" == true ]] || APP_PEAK_KB=null" <<<"$i_body"        # an unverified reset still yields no figure
sed -n '/^app_status_kb() {/,/^}/p' "$RUNNER_SRC" | grep -q 'for i in 1 2 3'
grep -qF 'appMemSamples:$appSamples, appHwmBelowBaseline:$appBelow' "$RUNNER_SRC"

# (3) auto_explain: the capture's ALTER SYSTEM must not be outranked by the command line (pilot2 logged no plan at all).
db_run="$(sed -n '/^DB_CID="\$(docker run -d/,/CONTAINERS+=("\$DB_CID")/p' "$RUNNER_SRC")"
[[ -n "$db_run" ]]
if grep -q 'auto_explain.log_min_duration' <<<"$db_run"; then echo 'the database command line sets auto_explain.log_min_duration; ALTER SYSTEM cannot override a command-line setting' >&2; exit 1; fi
grep -q 'auto_explain.log_analyze=on' <<<"$db_run"; grep -q 'shared_preload_libraries=pg_stat_statements,auto_explain' <<<"$db_run"
cp_body="$(sed -n '/^capture_plans() {/,/^}/p' "$RUNNER_SRC")"
on_l="$(grep -n 'SET auto_explain.log_min_duration = 0' <<<"$cp_body" | cut -d: -f1)"; w1_l="$(grep -n 'await_setting auto_explain.log_min_duration 0' <<<"$cp_body" | cut -d: -f1)"
tr_l="$(grep -n 'traverse "\$t"' <<<"$cp_body" | cut -d: -f1)"; off_l="$(grep -n 'SET auto_explain.log_min_duration = -1' <<<"$cp_body" | cut -d: -f1)"; w2_l="$(grep -n 'await_setting auto_explain.log_min_duration -1' <<<"$cp_body" | cut -d: -f1)"
lg_l="$(grep -n 'docker logs --since' <<<"$cp_body" | cut -d: -f1)"
[[ -n "$on_l$w1_l$tr_l$off_l$w2_l$lg_l" && "$on_l" -lt "$w1_l" && "$w1_l" -lt "$tr_l" && "$tr_l" -lt "$off_l" && "$off_l" -lt "$w2_l" && "$w2_l" -lt "$lg_l" ]]
grep -qE '^await_setting auto_explain.log_min_duration -1 \|\| exit 1' "$RUNNER_SRC"    # off at the start, proven in a new session
# A missing plan is still a failed row, never a silent pass: every kind the run must reach is counted
grep -qF 'jq -e --argjson need "$need" '"'"'. as $c | $need | all(. as $k | $c[$k] >= 1)'"'"' <<<"$counts"' <<<"$cp_body"
for kind in scanner-initial page-items delete-set blank-batch; do grep -qF "\"$kind\"" <<<"$cp_body"; done
# The capture walks the table (scanner), reads one page of items and deletes, so all five statements run; the plan file keeps all five kinds.
grep -qF 'for which in "${QC1_PLAN_KINDS[@]}"' <<<"$cp_body"
mut noplans "if .item == 8 then .plans = {\"scanner-initial\":0,\"scanner-continuation\":0,\"page-items\":0,\"delete-set\":0,\"blank-batch\":0} else . end"; expect_invalid noplans plans-missing
# Real PostgreSQL log text for the statements is found by its markers (single- and multi-line Query Text, with the prefix docker logs keeps)
cat >"$WORK/pg.sample" <<LOG
2026-11-07 14:31:01.100 UTC [71] LOG:  duration: 3.2 ms  plan:
	Query Text: SELECT id, requester_id FROM contact_requests ORDER BY id LIMIT \$1
	Limit  (cost=0.29..10.1 rows=100 width=8) (actual time=0.02..0.9 rows=100 loops=1)
2026-11-07 14:31:01.200 UTC [71] LOG:  duration: 3.0 ms  plan:
	Query Text: SELECT id, requester_id FROM contact_requests WHERE id > \$1::bigint ORDER BY id LIMIT \$2
	Limit  (cost=0.29..10.1 rows=100 width=8) (actual time=0.02..0.9 rows=100 loops=1)
	  ->  Index Scan using contact_requests_pkey on contact_requests
	        Index Cond: (id > \$1)
2026-11-07 14:31:01.250 UTC [71] LOG:  duration: 2.0 ms  plan:
	Query Text: SELECT id, reason FROM contact_requests WHERE id = ANY(\$1::bigint[]) AND reason <> '' AND (requester_id = \$2 OR subject_user_id = \$3) ORDER BY contact_requests.id
	Sort
2026-11-07 14:31:01.300 UTC [71] LOG:  duration: 8.0 ms  plan:
	Query Text: SELECT id FROM contact_requests WHERE reason <> '' AND (requester_id = \$1 OR subject_user_id = \$2) ORDER BY contact_requests.id FOR UPDATE
	LockRows  (cost=0.0..1.0 rows=1 width=14) (actual time=0.1..7.0 rows=10 loops=1)
2026-11-07 14:31:01.900 UTC [71] LOG:  duration: 5.5 ms  plan:
	Query Text: UPDATE contact_requests SET reason = '' WHERE id = ANY(\$1::bigint[]) AND reason <> '' AND (requester_id = \$2 OR subject_user_id = \$3)
	Update on contact_requests  (cost=0.0..1.0 rows=0 width=0) (actual time=5.0..5.0 rows=0 loops=1)
2026-11-07 14:31:02.000 UTC [1] LOG:  received SIGHUP, reloading configuration files
LOG
[[ "$(qc1_plan_counts "$WORK/pg.sample")" == '{"scanner-initial":1,"scanner-continuation":1,"page-items":1,"delete-set":1,"blank-batch":1}' ]] || { echo "plan counts: $(qc1_plan_counts "$WORK/pg.sample")" >&2; exit 1; }
printf '%s\n' '2026-11-07 14:30:50.046 UTC [1] LOG:  received SIGHUP, reloading configuration files' >"$WORK/pg.pilot2"
[[ "$(qc1_plan_counts "$WORK/pg.pilot2")" == '{"scanner-initial":0,"scanner-continuation":0,"page-items":0,"delete-set":0,"blank-batch":0}' ]]       # what the pilot saw: nothing
echo 'qc1 fixtures: ok'
