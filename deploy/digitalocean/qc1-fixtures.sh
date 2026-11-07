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
jq -nc '[range(0; 7) | {requestId: (. + 1 | tostring), reviewHash: "h"}]' >"$WORK/seven.json"
[[ "$(qc1_delete_body_n mixed "$WORK/seven.json" 7 "" | jq -c '[.reasonReview.items[].choice | select(. == "remove")] | length')" == 4 ]]

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
INSTR='"walBytes":100,"walControlBytes":10,"appPeakKb":5,"appHwmReset":true,"dbPeakKb":7,"dbMemSamples":50,"gateSpanMs":0,"gateGrantTicks":0,"gateTicks":50,"gateHolders":0,"gateMaxGapMs":25,"gateMeanGapMs":22,"gateHoles":0,"autovacuumWorkers":0,"maxActiveClients":1,"commitCalls":1,"commitMsMax":1,"commitMsTotal":1,"schedulerOverlap":false,"schedulerRows":0'
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
SRC="$WORK/srcrepo"; mkdir -p "$SRC/deploy/digitalocean/qc1"
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

# --- bodies: the envelope the panel sends (V-02) ----------------------------------------------------
ID80="$(printf 'ab%.0s' {1..40})"
[[ ${#ID80} == 80 ]]
# 40 entries shaped like real items: ids of varying width, 64-hex hashes, and fields a body never carries.
jq -nc '[range(0; 40) | {requestId: ((. * 997 + 1) | tostring), reviewHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", role:"subject", reason:"never in a body"}]' >"$WORK/real-items.json"
body() { qc1_delete_body_n "$@"; }
# The key order and compactness are those of AdminUsers.remove and JSON.stringify.
[[ "$(body keep "$WORK/real-items.json" 2 "$ID80" | jq -c 'keys_unsorted')" == '["successors","hostSuccessors","attemptId","reasonReview"]' ]]
[[ "$(body keep "$WORK/real-items.json" 2 "$ID80" | jq -c '.reasonReview.items[0] | keys_unsorted')" == '["requestId","reviewHash","choice"]' ]]
[[ "$(body keep "$WORK/real-items.json" 1 "$ID80")" == '{"successors":{},"hostSuccessors":{},"attemptId":"'"$ID80"'","reasonReview":{"items":[{"requestId":"1","reviewHash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","choice":"keep"}]}}' ]]
# No reasons: no reasonReview. No id: no attemptId. Maps given: carried, in place.
[[ "$(body keep "$WORK/real-items.json" 0 "$ID80")" == '{"successors":{},"hostSuccessors":{},"attemptId":"'"$ID80"'"}' ]]
[[ "$(body keep "$WORK/real-items.json" 0 "")" == '{"successors":{},"hostSuccessors":{}}' ]]
[[ "$(body keep "$WORK/real-items.json" 0 "$ID80" '{"12":3}' '{"7":8}')" == '{"successors":{"12":3},"hostSuccessors":{"7":8},"attemptId":"'"$ID80"'"}' ]]
[[ "$(body keep "$WORK/real-items.json" 2 "$ID80" | jq -c '[.reasonReview.items[].requestId]')" == '["1","998"]' ]]
[[ "$(body mixed "$WORK/real-items.json" 4 "$ID80" | jq -c '[.reasonReview.items[].choice]')" == '["remove","remove","keep","keep"]' ]]
[[ "$(body remove "$WORK/real-items.json" 3 "$ID80" | jq -c '[.reasonReview.items[].choice]')" == '["remove","remove","remove"]' ]]
# The shape is the source's: the envelope in web/src/api.ts, the 80-hex id, the item keys in the panel.
grep -qF 'body: { successors, hostSuccessors, ...(attemptId ? { attemptId } : {}), ...(reasonReview ? { reasonReview: { items: reasonReview } } : {}) }' "$ROOT/web/src/api.ts"
grep -qF '/^[0-9a-f]{80}$/.test(res.attemptId)' "$ROOT/web/src/api.ts"
grep -qF 'body.push({ requestId: it.requestId, reviewHash: it.reviewHash, choice });' "$ROOT/web/src/components/DeleteUserPanel.tsx"
grep -qF 'AdminUsers.remove(target.id, picks, hostPicks, id, reasons)' "$ROOT/web/src/components/DeleteUserPanel.tsx"
# Changing only the last choice: same body otherwise, the length moves by the words' difference.
body mixed "$WORK/real-items.json" 6 "$ID80" >"$WORK/b6.json"
qc1_final_choice_body "$WORK/b6.json" maybe >"$WORK/b6m.json"
[[ "$(jq -c '.reasonReview.items[-1].choice' "$WORK/b6m.json")" == '"maybe"' ]]
[[ "$(jq -c 'del(.reasonReview.items[-1].choice)' "$WORK/b6.json")" == "$(jq -c 'del(.reasonReview.items[-1].choice)' "$WORK/b6m.json")" ]]
[[ "$(jq -c '.reasonReview.items[-1].choice' "$WORK/b6.json")" == '"keep"' ]]
(( $(wc -c <"$WORK/b6m.json") == $(wc -c <"$WORK/b6.json") + 1 ))

# --- exact bodies near and across the cap, in the full envelope (V-02) -----------------------------------
bytes_of() { body mixed "$WORK/real-items.json" "$1" "$ID80" | wc -c; }
(( $(bytes_of 1) < $(bytes_of 2) && $(bytes_of 39) < $(bytes_of 40) ))
for want in 1 17 33 39; do
  cap="$(bytes_of "$want")"
  read -r got at next <<<"$(qc1_fit_entries mixed "$WORK/real-items.json" "$cap" "$ID80")"
  [[ "$got" == "$want" && "$at" == "$cap" && "$next" == "$(bytes_of "$((want + 1))")" ]] || { echo "fit for cap $cap: $got $at $next, want $want" >&2; exit 1; }
  (( at <= cap && next > cap ))
done
# The envelope counts: an id, and maps with entries, leave fewer entries under the same cap.
cap="$(bytes_of 30)"
read -r plain _ _ <<<"$(qc1_fit_entries mixed "$WORK/real-items.json" "$cap" "$ID80")"
read -r wide _ _ <<<"$(qc1_fit_entries mixed "$WORK/real-items.json" "$cap" "$ID80" '{"1000":2000,"1001":2001,"1002":2002,"1003":2003}' '{"3000":4000,"3001":4001}')"
read -r noid noid_bytes _ <<<"$(qc1_fit_entries mixed "$WORK/real-items.json" "$cap" "")"
[[ "$plain" == 30 && "$noid" -ge 30 ]] && (( wide < plain && noid_bytes <= cap - 90 ))
# A cap between two sizes takes the smaller; the whole file fits a generous cap (no next size).
read -r got at next <<<"$(qc1_fit_entries mixed "$WORK/real-items.json" "$(( $(bytes_of 20) + 1 ))" "$ID80")"; [[ "$got" == 20 ]]
read -r got at next <<<"$(qc1_fit_entries mixed "$WORK/real-items.json" 100000 "$ID80")"; [[ "$got" == 40 && -z "$next" ]]
refused 'one entry is already over' qc1_fit_entries mixed "$WORK/real-items.json" 20 "$ID80"
# The probe always passes the cap: the smallest entry is 120 bytes.
[[ "$(qc1_probe_entries 6291456)" == 57196 ]]
(( $(qc1_probe_entries 6291456) > 6291456 / 120 ))
# With the real cap and entries as long as a real item, the envelope's fixed bytes are the id and the two maps.
# The runner: probe, fit, refusal from the real body, E and E+1 accounts, the envelope with minted ids, sizes recorded.
for pat in \
  'load_scenario "$n" "$probe" "$t" both "$CPS" tail' 'load_scenario "$n" "$E" "$t" both "$CPS" tail' 'load_scenario "$n" "$((E + 1))" "$t" both "$CPS" tail' \
  'qc1_fit_entries mixed "$WORK/probe-items.json" "$cap" "$pid"' 'qc1_delete_body_n mixed "$WORK/fit-items.json" "$E" "$id1"' \
  'qc1_final_choice_body "$WORK/fit.body" maybe >"$WORK/delete.body"' 'qc1_delete_body_n mixed "$WORK/over-items.json" "$((E + 1))" "$id1"' \
  'delete_with "$t" mixed "$WORK/fit-items.json"' 'fitBodyBytes:$fitBytes' 'nextBodyBytes:$next' 'parseRefusalBodyBytes:$refusalBytes' 'overBodyBytes:$overBytes' \
  'fixedBytesWithEmptyReview' 'attemptIdLength:80' 'workflowOverflow' 'capacityWithTwentyGroupsAndTwentyEventsInTheEnvelope'; do
  grep -qF -- "$pat" "$HERE/qc1-measure.sh" || { echo "qc1: the request-size case lost: $pat" >&2; exit 1; }
done
if grep -n 'qc1_invalid_body' "$HERE/qc1-measure.sh" "$HERE/qc1/lib.sh"; then echo 'qc1: the invented invalid body is back' >&2; exit 1; fi
# The refusal comes before the real send, and the legacy PUT stays a pending item.
refuse_line="$(grep -n 'qc1_final_choice_body' "$HERE/qc1-measure.sh" | head -n 1 | cut -d: -f1)"; send_line="$(grep -n 'delete_with "\$t" mixed "\$WORK/fit-items.json"' "$HERE/qc1-measure.sh" | head -n 1 | cut -d: -f1)"
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

# A complete set of rows for sizes S and counts C: every kind holds its instruments, its expectation and its outcome.
HDEL="\"status\":\"200\",\"ms\":100,$INSTR"
okout='{"dbOk":true}'
gen() { # sizes counts
  local s k d m c kept removed bc
  for s in $(qc1_list "$1"); do
    for c in "sparse 1" "unrelated 2"; do
      read -r name item <<<"$c"
      printf '{"status":"measured","item":%s,"case":"%s","size":%s,"workers":0,"expect":{"items":0,"kept":0,"removed":0},"outcome":%s,"traversal":{"units":1,"slowestUnitMs":5,"totalMs":10,"requestMs":8,"clientMs":2,"items":0,"lastStatus":"200",%s},"delete":{%s,"reasonsKept":0,"reasonsRemoved":0}}\n' "$item" "$name" "$s" "$okout" "$INSTR" "$HDEL"
    done
    printf '{"status":"measured","item":5,"case":"request-size","size":%s,"workers":0,"request":{"maxEntriesInBudget":51000,"overEntries":51001,"overStatus":"413","overUnchanged":true,"parseRefusalStatus":"400","parseUnchanged":true,"probeEntries":57196,"fitExpect":{"kept":25500,"removed":25500},"fitOutcome":%s},"delete":{%s,"reasonsKept":25500,"reasonsRemoved":25500},"traversal":{"units":572,"slowestUnitMs":50,"totalMs":9000,"requestMs":8000,"clientMs":1000,"items":57196,"lastStatus":"200",%s,"gateTicks":100,"dbMemSamples":100}}\n' "$s" "$okout" "$HDEL" "$INSTR"
    printf '{"status":"measured","item":7,"case":"human-progress","size":%s,"workers":0,"assessment":{"perCount":[{"reasons":10}]}}\n' "$s"
    printf '{"status":"measured","item":8,"case":"plans","size":%s,"workers":0,"plans":{"scanner-unit":1,"delete-set":1,"blank-batch":1}}\n' "$s"
    for bc in $(qc1_list "$QC1_BROWSER_COUNTS"); do
      printf '{"status":"measured","item":10,"case":"browser-panel-%s","size":%s,"workers":0,"reasons":%s,"browser":{"renderReadyMs":4000,"itemsRendered":%s,"heapAfterGcBytes":5000000,"domNodes":9000,"longTaskMs":900,"pageRequests":%s,"pageResponseBytes":%s,"apiOrigin":"real"}}\n' "$bc" "$s" "$bc" "$bc" "$(( (bc + 99) / 100 ))" "$(( bc * 300 ))"
    done
    for k in $(qc1_list "$2"); do
      for d in requester subject; do printf '{"status":"measured","item":3,"case":"discovery-%s-%s","size":%s,"workers":0,"reasons":%s,"expect":{"items":%s},"traversal":{"units":1,"slowestUnitMs":5,"totalMs":10,"requestMs":8,"clientMs":2,"items":%s,"lastStatus":"200",%s}}\n' "$d" "$k" "$s" "$k" "$k" "$k" "$INSTR"; done
    done
    for k in $(qc1_list "$QC1_DELETE_COUNTS"); do
      for d in requester subject; do for m in keep remove mixed; do
        read -r kept removed <<<"$(qc1_expected_split "$m" "$k")"
        printf '{"status":"measured","item":6,"case":"delete-%s-%s-%s","size":%s,"workers":0,"reasons":%s,"expect":{"items":%s,"kept":%s,"removed":%s},"outcome":%s,"delete":{%s,"reasonsKept":%s,"reasonsRemoved":%s}}\n' "$d" "$k" "$m" "$s" "$k" "$k" "$kept" "$removed" "$okout" "$HDEL" "$kept" "$removed"
      done; done
    done
  done
  printf '{"status":"observation","item":9,"case":"workers-on-delete","size":%s,"workers":2,"reasons":1000,"expect":{"items":1000,"kept":500,"removed":500},"outcome":%s,"delete":{%s,"reasonsKept":500,"reasonsRemoved":500}}\n' "$(qc1_list "$1" | sort -n | tail -n 1)" "$okout" "$HDEL"
}
PEND='{"status":"pending","item":4,"case":"legacy-discovery","size":null,"reason":"T07B-3a"}
{"status":"pending","item":5,"case":"legacy-put","size":null,"reason":"T07B-3a"}'
status_of() { qc1_evidence_status "$1" "${2:-10,100}" "${3:-0,10,100,1000,10000}" "$WORK/env.json" "$HEAD_FAKE" 6291456; }

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
# Items 4 and the legacy PUT are named as pending even when no row mentions them: never complete.
[[ "$(jq -c '.pending | map(.item) | sort' <<<"$status")" == '[4,5]' && "$(jq -r .complete <<<"$status")" == false ]]
# The over-cap history is a budget finding, not an invalid run: each size reports it.
[[ "$(jq -c '[.budgetFailures[] | .failure + "@" + (.size|tostring)] | sort' <<<"$status")" == '["body@10","body@100"]' ]]
{ cat "$WORK/e.jsonl"; echo "$PEND"; } >"$WORK/ec.jsonl"
status="$(status_of "$WORK/ec.jsonl")"
[[ "$(jq -r .complete <<<"$status")" == false && "$(jq -c '.pending | map(.item) | sort' <<<"$status")" == '[4,5]' ]]
# Item 4 and the legacy PUT measured (T07B-3a landed): complete.
{ cat "$WORK/e.jsonl"
  echo '{"status":"measured","item":4,"case":"legacy-discovery","size":null,"workers":0}'
  echo '{"status":"measured","item":5,"case":"legacy-put","size":null,"workers":0}'; } >"$WORK/e2.jsonl"
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
# Item 5: the fitting deletion is checked like any other, the probe traversal must have listed every entry loaded.
mut fit-kept 'if .item == 5 then .delete.reasonsKept = 1 else . end'; expect_invalid fit-kept outcome-count
mut fit-db 'if .item == 5 then .request.fitOutcome.dbOk = false else . end'; expect_invalid fit-db outcome-db
mut probe-items 'if .item == 5 then .traversal.items = 57195 else . end'; expect_invalid probe-items discovery-count
# Statuses: a refused deletion, a failed page, a changed over-cap or refused account, a wrong over-cap or parse answer.
mut s409 "$DEL then .delete.status = \"409\" else . end"; expect_invalid s409 status
mut p503 'if .item == 3 and .case == "discovery-requester-10" then .traversal.lastStatus = "503" else . end'; expect_invalid p503 status
mut changed 'if .item == 5 then .request.overUnchanged = false else . end'; expect_invalid changed over-cap-changed-data
mut parse-changed 'if .item == 5 then .request.parseUnchanged = false else . end'; expect_invalid parse-changed refused-parse-changed-data
mut over500 'if .item == 5 then .request.overStatus = "500" else . end'; expect_invalid over500 status
mut parse200 'if .item == 5 then .request.parseRefusalStatus = "200" else . end'; expect_invalid parse200 status
mut fit409 'if .item == 5 then .delete.status = "409" else . end'; expect_invalid fit409 status
# Item 7 without an assessment; accounting that does not add up.
mut noassess 'if .item == 7 then del(.assessment) else . end'; expect_invalid noassess assessment-missing
mut acct 'if .item == 3 and .case == "discovery-requester-10" then .traversal.requestMs = 99 | .traversal.totalMs = 10 | .traversal.clientMs = 0 else . end'; expect_invalid acct accounting
# Item 10: every figure, the count rendered and a verified real origin.
for f in renderReadyMs itemsRendered heapAfterGcBytes domNodes longTaskMs; do
  mut "b-$f" "if .item == 10 and .case == \"browser-panel-1000\" and .size == 10 then .browser.$f = null else . end"; expect_invalid "b-$f" browser-missing
done
mut b-count 'if .item == 10 and .case == "browser-panel-1000" then .browser.itemsRendered = 999 else . end'; expect_invalid b-count browser-count
mut b-mock 'if .item == 10 then .browser.apiOrigin = "unverified" else . end'; expect_invalid b-mock browser-not-real-api
grep -v '"item":10,"case":"browser-panel-10000","size":100,' "$WORK/e2.jsonl" >"$WORK/e2.nobrowser"
st="$(status_of "$WORK/e2.nobrowser")"
[[ "$(jq -r .complete <<<"$st")" == false && "$(jq -c '.missing | map(.item, .size, .case)' <<<"$st")" == '[10,100,"browser-panel-10000"]' ]]
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
mut plans-str 'if .item == 8 then .plans["scanner-unit"] = "1" else . end'; expect_invalid plans-str plans-missing
# Browser response accounting: the decoded body sizes must be there (a chunked or unreadable response gives none), the walk must have the requests its pages need.
for bad in null 0 '"123"' 29999 '-5'; do
  mut "bytes-$bad" "if .item == 10 and .case == \"browser-panel-1000\" then .browser.pageResponseBytes = $bad else . end"; expect_invalid "bytes-$bad" browser-bytes
done
mut bytes-gone 'if .item == 10 and .case == "browser-panel-1000" then del(.browser.pageResponseBytes) else . end'; expect_invalid bytes-gone browser-bytes
mut bytes-ok 'if .item == 10 and .case == "browser-panel-1000" then .browser.pageResponseBytes = 100000 else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.bytes-ok")")" == true ]]
mut req-gone 'if .item == 10 and .case == "browser-panel-1000" then del(.browser.pageRequests) else . end'; expect_invalid req-gone browser-walk-short
mut req-short 'if .item == 10 and .case == "browser-panel-10000" then .browser.pageRequests = 99 else . end'; expect_invalid req-short browser-walk-short
mut req-exact 'if .item == 10 and .case == "browser-panel-10000" then .browser.pageRequests = 100 else . end'
[[ "$(jq -r .complete <<<"$(status_of "$WORK/e2.req-exact")")" == true ]]
mut b-obj-gone 'if .item == 10 and .case == "browser-panel-1000" then del(.browser) else . end'; expect_invalid b-obj-gone browser-missing
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
mut failed6 'if .item == 6 and .case == "delete-subject-1000-remove" and .size == 100 then .status = "failed" else . end'
[[ "$(jq -c '.missing | map(.case + "@" + (.size|tostring))' <<<"$(status_of "$WORK/e2.failed6")")" == '["delete-subject-1000-remove@100"]' ]]
grep -v '"item":3,"case":"discovery-requester-0","size":10,' "$WORK/e2.jsonl" >"$WORK/e4.jsonl"
[[ "$(jq -r '.missing | length' <<<"$(status_of "$WORK/e4.jsonl")")" == 1 ]]
# A whole size missing lists all of it: 38 cases, plans, request size and the 2 browser rows, plus the observation at the new largest size.
[[ "$(jq -r '.missing | length' <<<"$(qc1_evidence_status "$WORK/e2.jsonl" 10,100,1000 0,10,100,1000,10000 "$WORK/env.json" "$HEAD_FAKE" 6291456)")" == 42 ]]
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


# --- statements: the markers are still the Go source's ------------------------------------
for which in delete-set scanner-unit blank-batch; do
  marker="$(qc1_source_marker "$which")"
  [[ -n "$marker" ]]
  grep -qF -- "$marker" "$ROOT/account_erasure_reasons.go" || { echo "qc1: the $which statement changed in account_erasure_reasons.go: $marker" >&2; exit 1; }
  # The log form keeps $n placeholders and has the same shape as the source form.
  [[ "$(qc1_plan_marker "$which" | sed -E 's/\$[0-9]+/?/g')" == "$marker" ]]
done
refused '' qc1_plan_marker nope
# Plans come out whole, selected by their Query Text, and never run into the next log line.
cat >"$WORK/pg.log" <<'L'
2026-11-07 10:00:00.100 UTC [40] LOG:  statement: SELECT 1
2026-11-07 10:00:01.200 UTC [57] LOG:  duration: 123.4 ms  plan:
	Query Text: SELECT id, requester_id, subject_user_id, (reason <> '') AS has_reason FROM contact_requests WHERE ($1::bigint IS NULL OR id > $2::bigint) ORDER BY id LIMIT $3
	Limit  (cost=0.42..1.0 rows=50000) (actual time=0.1..99.0 rows=50000 loops=1)
	  Buffers: shared hit=12
2026-11-07 10:00:02.300 UTC [57] LOG:  duration: 5.0 ms  plan:
	Query Text: SELECT x FROM contact_requests WHERE reason <> '' AND (requester_id = $1 OR subject_user_id = $2) ORDER BY id FOR UPDATE
	LockRows  (cost=1.0..2.0 rows=3) (actual time=0.1..4.0 rows=3 loops=1)
	  WAL: records=0
2026-11-07 10:00:03.400 UTC [57] LOG:  duration: 9.9 ms  plan:
	Query Text: UPDATE other SET a = 1
	Update on other
2026-11-07 10:00:04.500 UTC [57] LOG:  statement: COMMIT
L
[[ "$(qc1_extract_plans "$WORK/pg.log" scanner-unit | grep -c 'Limit  (cost')" == 1 ]]
[[ "$(qc1_extract_plans "$WORK/pg.log" scanner-unit | grep -c 'LockRows' || true)" == 0 ]]
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

# plan counts: a size has its plans only when every statement kind is found
cat >"$WORK/pg2.log" <<'L'
2026-11-07 10:00:01.200 UTC [57] LOG:  duration: 1.4 ms  plan:
	Query Text: SELECT id FROM contact_requests WHERE ($1::bigint IS NULL OR id > $2::bigint) ORDER BY id LIMIT $3
	Limit
2026-11-07 10:00:02.300 UTC [57] LOG:  duration: 5.0 ms  plan:
	Query Text: SELECT x FROM contact_requests WHERE reason <> '' AND (requester_id = $1 OR subject_user_id = $2) ORDER BY id FOR UPDATE
	LockRows
2026-11-07 10:00:02.400 UTC [57] LOG:  duration: 5.0 ms  plan:
	Query Text: SELECT x FROM contact_requests WHERE reason <> '' AND (requester_id = $1 OR subject_user_id = $2) ORDER BY id FOR UPDATE
	LockRows
2026-11-07 10:00:04.500 UTC [57] LOG:  statement: COMMIT
L
[[ "$(qc1_plan_counts "$WORK/pg2.log")" == '{"scanner-unit":1,"delete-set":2,"blank-batch":0}' ]]
[[ "$(qc1_plan_counts "$WORK/pg2.log" | jq 'to_entries | all(.value >= 1)')" == false ]]
[[ "${QC1_PLAN_KINDS[*]}" == 'scanner-unit delete-set blank-batch' ]]
: >"$WORK/empty.log"; [[ "$(qc1_plan_counts "$WORK/empty.log")" == '{"scanner-unit":0,"delete-set":0,"blank-batch":0}' ]]
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
cp "$HERE/qc1-measure.sh" "$CLONE/deploy/digitalocean/"; cp "$HERE"/qc1/lib.sh "$HERE"/qc1/filler.sql "$HERE"/qc1/scenario.sql "$HERE"/qc1/panel-measure.mjs "$HERE"/qc1/browser.mjs "$HERE"/qc1/browser-launch.cjs "$HERE"/qc1/browser-preflight.mjs "$HERE"/qc1/response-accounting.mjs "$CLONE/deploy/digitalocean/qc1/"; cp "$HERE/isolation.sh" "$CLONE/deploy/digitalocean/"
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
PM="$HERE/qc1/panel-measure.mjs"
if command -v node >/dev/null; then node --check "$PM"; fi
for pat in 'pw.chromium.launch(launchOptions(env))' 'resolvePlaywright(env.QC1_WEB_DIR)' 'totalBodyBytes(bodySizes)' 'walkOrigin(walk, walk.bytes, bodySizes.length)' 'ul[aria-label="Contact-request reasons"] > li[data-reason-id]' 'Checking their contact-request reasons…' \
  'Performance.getMetrics' 'HeapProfiler.collectGarbage' 'PerformanceObserver' 'process.exit(3)' '/erasure-reasons/page'; do
  grep -qF -- "$pat" "$PM" || { echo "qc1: panel-measure.mjs lost: $pat" >&2; exit 1; }
done
# Nothing is mocked: no route is intercepted.
if grep -nE 'page\.route|context\.route|route\.fulfill' "$PM"; then echo 'qc1: the browser measurement intercepts routes' >&2; exit 1; fi
# The selectors it waits on are the ones the panel renders.
grep -qF 'data-reason-id={item.requestId}' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'aria-label="Contact-request reasons"' "$ROOT/web/src/components/ReasonReviewList.tsx"
grep -qF 'const REVIEW_CHECKING = "Checking their contact-request reasons…";' "$ROOT/web/src/components/DeleteUserPanel.tsx"
grep -qF 'name: "Search by name or email"' "$ROOT/web/e2e/admin-delete-user.spec.ts"
# The runner measures the counts it lists, with the real API, and a missing browser leaves the row missing (never invented).
grep -q 'for bk in \$(qc1_list "\$QC1_BROWSER_COUNTS"); do measure_browser_panel "\$n" "\$bk"; done' "$RUNNER"
grep -q 'node "\$HERE/qc1/panel-measure.mjs"' "$RUNNER"
sed -n '/^measure_browser_panel() {/,/^}/p' "$RUNNER" | grep -q 'the row is missing and the run is not complete'
sed -n '/^measure_browser_panel() {/,/^}/p' "$RUNNER" | grep -q '(( BROWSER )) || return 0'
[[ "$QC1_BROWSER_COUNTS" == 1000,10000 ]]
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
echo 'qc1 fixtures: ok'
