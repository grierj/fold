#!/usr/bin/env bash
# T-292 QC-1 legacy-only mode (R4-C06 items 4 and 5, the deleted-accounts review): the functions qc1-measure.sh --legacy-only runs. Sourced only by
# qc1-measure.sh, after lib.sh, and it uses the runner's own helpers (api, timed, instrumented, record, load_filler, load_scenario, mint_attempt, psql_db, ...)
# by name; they are looked up when a function runs, so this file defines functions and runs nothing. It measures the routes of
# api_admin_reasons_legacy.go and the marker lookup of account_erasure_reasons_scan.go as they are in HEAD's image, against a table of the declared size:
#   discovery with 0, 100 and 10,000 unreviewed pairs, an account's own pages (dense: 9,900 pairs; sparse tail: 100 pairs after every background row), the marker lookup
#   (the probe), the 100-decision save (and its refused 101-decision twin), the decided history (every background request has a marker, forged by qc1/legacy-markers.sql),
#   and the application's own logged plans of the scanner, the facts statement and the marker lookup.
# The rows are judged by the one qc1_judge and counted by the one qc1_evidence_status; nothing here decides a verdict. It is never FULL and never release evidence.
# shellcheck disable=SC2034,SC2016,SC2154
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then echo 'source from qc1-measure.sh only' >&2; exit 1; fi

LEGACY_SOURCE=T07B-3a
LEGACY_ROUTE_DISCOVERY='GET /api/v1/admin/erasure-reasons/legacy'
LEGACY_ROUTE_PROBE='GET /api/v1/admin/erasure-reasons/legacy/probe'
LEGACY_ROUTE_PAGE='POST /api/v1/admin/erasure-reasons/legacy/:userId/page'
LEGACY_ROUTE_SAVE='PUT /api/v1/admin/erasure-reasons/legacy/:userId'
# Pairs the sizes seed for the account the discovery and the save walk (spread over the table) and for the account whose pairs all lie after the background rows (tail).
# The smoke pair count is the one qc1/lib.sh validates the smoke record against (QC1_LEGACY_SMOKE_PAIRS).
LEGACY_A_PAIRS=9900 LEGACY_B_PAIRS=100 LEGACY_SEED_PAIRS=10000 LEGACY_SMOKE_PAIRS="$QC1_LEGACY_SMOKE_PAIRS"
LEGACY_REASON_ACTIONS="'contact.reason_kept','contact.reason_removed','add.reason_kept','add.reason_removed'"

# --- the disposable-database sentinel and the forged markers --------------------------------------------------------------------------------------------------
# qc1/legacy-markers.sql refuses unless this row, written into this run's own database and cluster, matches the token the run passes. Nothing a real database could
# also hold (a domain, a site name) is trusted.
legacy_guard_create() {
  GUARD_TOKEN="$(cat /proc/sys/kernel/random/uuid)"
  psql_db -c 'CREATE TABLE qc1_disposable (token text PRIMARY KEY, system_identifier bigint NOT NULL)' >/dev/null
  psql_db -c "INSERT INTO qc1_disposable SELECT '$GUARD_TOKEN', system_identifier FROM pg_control_system()" >/dev/null
  [[ "$(psql_db -c 'SELECT count(*) FROM qc1_disposable')" == 1 ]] || { echo 'qc1: the disposable-database sentinel was not written' >&2; return 1; }
}
# The one place the file runs: psql into this run's own database container, by id. A refusal of the guard exits non-zero.
legacy_forge_with() { # token tombstone only_id
  [[ "$2" =~ ^[0-9]+$ && "${3:-0}" =~ ^[0-9]+$ ]] || return 1
  docker exec -i "$DB_CID" psql -X -U church -d fold -v ON_ERROR_STOP=1 -v token="$1" -v tombstone="$2" -v only_id="${3:-0}" -f - <"$HERE/qc1/legacy-markers.sql" >/dev/null 2>&1
}
forge_markers() { legacy_forge_with "$GUARD_TOKEN" "$1" "${2:-0}"; } # tombstone [only_id]

# A fresh deleted account by the real DELETE with no reasons: its users row is soft-deleted with the tombstone email. Sets TOMB_ID. Never reused: markers are keyed by
# (request, party), the scenario ids repeat at every size, and audit_logs is never truncated, so a reused account would make new pairs look decided.
make_tombstone() { # label
  local stamp st att
  stamp="$(date +%s%N)"
  TOMB_ID="$(new_user "QC legacy $1 $stamp" "qc1-legacy-$1-$stamp@example.invalid")"
  att="$(mint_attempt "$TOMB_ID")"
  echo '[]' >"$WORK/items-tomb.json"
  qc1_delete_body_n keep "$WORK/items-tomb.json" 0 "$att" >"$WORK/tomb.body"
  read -r st _ <<<"$(timed DELETE "/admin/users/$TOMB_ID" "$WORK/tomb.body" "$WORK/tomb.out")"
  [[ "$st" == 200 ]] || { echo "qc1: the deleted account $1 ($TOMB_ID) was not deleted (status $st)" >&2; return 1; }
  [[ "$(psql_db -c "SELECT deleted_at IS NOT NULL AND email = 'deleted-$TOMB_ID@deleted.invalid' FROM users WHERE id = $TOMB_ID")" == t ]] \
    || { echo "qc1: account $TOMB_ID is not a soft-deleted tombstone after its deletion" >&2; return 1; }
}

# --- reads ------------------------------------------------------------------------------------------------------------------------------------------------
# One legacy read as the client does it: units from a null cursor until next is null, retrying nothing (a client would retry the same cursor; this stops at the first non-200, so a
# 503 at the route's deadline is one finding and not a loop). kind discovery: GET the list, the items are {requestId, userId} pairs; kind account: POST the account's page, the items are
# the page's. Writes the items to $3 and the same TR_* as traverse, plus TR_UNIT_MS (each request's ms), TR_LAST_CODE, TR_NONEMPTY (pages with items), TR_FIRST_ITEMS_MS (from the start to the first page with
# items) and, for discovery, the privacy scan of every page (no "reason" key, no reason text): TR_KEY_BAD and TR_TEXT_BAD count the pages that failed it.
legacy_walk() { # discovery|account target out-file
  local kind="$1" target="$2" out="$3" after=null status ms_ n=0 slow=0 total=0 started ended times pbytes rbytes=0 maxbytes=0 path cnt nonempty=0 first=null keybad=0 textbad=0 code=null
  local -a units=()
  started="$(ms)"
  TR_STATUS=200 TR_UNITS=0 TR_ITEMS=0
  : >"$WORK/items.jsonl"; : >"$WORK/empty.body"
  while :; do
    if [[ "$kind" == discovery ]]; then
      path=/admin/erasure-reasons/legacy; [[ "$after" == null ]] || path+="?after=$(jq -r . <<<"$after")"
      read -r status ms_ <<<"$(timed GET "$path" "$WORK/empty.body" "$WORK/page.out")"
    else
      jq -nc --argjson a "$after" '{after:$a}' >"$WORK/page.body"
      read -r status ms_ <<<"$(timed POST "/admin/erasure-reasons/legacy/$target/page" "$WORK/page.body" "$WORK/page.out")"
    fi
    n=$((n+1)); total=$((total+ms_)); units+=("$ms_")
    if (( ms_ > slow )); then slow=$ms_; fi
    pbytes="$(wc -c <"$WORK/page.out")"; rbytes=$((rbytes + pbytes))
    if (( pbytes > maxbytes )); then maxbytes=$pbytes; fi
    if [[ "$status" != 200 ]]; then TR_STATUS=$status; code="$(jq -c '.code // null' "$WORK/page.out" 2>/dev/null || echo null)"; break; fi
    if [[ "$kind" == discovery ]]; then
      cnt="$(qc1_legacy_discovery_pairs "$WORK/page.out" | tee -a "$WORK/items.jsonl" | wc -l)"
      if jq -e '[.. | objects | has("reason")] | any' "$WORK/page.out" >/dev/null 2>&1; then keybad=$((keybad+1)); fi
      if grep -qF 'reason text' "$WORK/page.out"; then textbad=$((textbad+1)); fi
    else
      jq -c '.items[]' "$WORK/page.out" >>"$WORK/items.jsonl"
      cnt="$(jq '.items | length' "$WORK/page.out")"
    fi
    if (( cnt > 0 )); then nonempty=$((nonempty+1)); [[ "$first" != null ]] || first=$(( $(ms) - started )); fi
    after="$(jq -c '.next' "$WORK/page.out")"
    [[ "$after" != null ]] || break
    (( n < 100000 )) || { TR_STATUS=loop; break; }
  done
  jq -sc . "$WORK/items.jsonl" >"$out"
  TR_ITEMS="$(jq 'length' "$out")"
  ended="$(ms)"
  TR_UNITS=$n TR_SLOW_MS=$slow TR_RESPONSE_BYTES=$rbytes TR_MAX_PAGE_BYTES=$maxbytes
  TR_UNIT_MS="$(printf '%s\n' "${units[@]}" | jq -sc .)" TR_LAST_CODE="$code" TR_NONEMPTY="$nonempty" TR_FIRST_ITEMS_MS="$first" TR_KEY_BAD="$keybad" TR_TEXT_BAD="$textbad"
  if times="$(qc1_traversal_times "$((ended-started))" "$total" "$n")"; then
    read -r TR_TOTAL_MS TR_REQUEST_MS TR_CLIENT_MS <<<"$times"
  else
    TR_TOTAL_MS=$((ended-started)) TR_REQUEST_MS=$total TR_CLIENT_MS=-1
  fi
}
# traversal_json (the live row's) plus what only a legacy walk has. Read right after the instrumented walk, before anything else uses the globals.
legacy_traversal_json() {
  traversal_json | jq -c --argjson u "$TR_UNIT_MS" --argjson code "$TR_LAST_CODE" --argjson ne "$TR_NONEMPTY" --argjson first "$TR_FIRST_ITEMS_MS" \
    '. + {unitMs:$u, lastCode:$code, nonEmptyPages:$ne, timeToFirstItemsMs:$first}'
}
legacy_privacy_json() { jq -nc --argjson k "$TR_KEY_BAD" --argjson t "$TR_TEXT_BAD" '{noReasonKey:($k == 0), noScenarioText:($t == 0)}'; }

# The probe: one GET of the first unit, as the page asks "is there anything to review". Sets PR_STATUS, PR_MS, PR_FOUND and its privacy flags.
legacy_probe() {
  read -r PR_STATUS PR_MS <<<"$(timed GET /admin/erasure-reasons/legacy/probe "$WORK/empty.body" "$WORK/probe.out")"
  PR_FOUND="$(jq -c '.found' "$WORK/probe.out" 2>/dev/null || echo null)"
  PR_KEY=true PR_TEXT=true
  if jq -e '[.. | objects | has("reason")] | any' "$WORK/probe.out" >/dev/null 2>&1; then PR_KEY=false; fi
  if grep -qF 'reason text' "$WORK/probe.out"; then PR_TEXT=false; fi
}
legacy_probe_json() { # read right after the instrumented probe
  jq -nc --arg st "$PR_STATUS" --argjson ms "$PR_MS" --argjson found "$PR_FOUND" --arg route "$LEGACY_ROUTE_PROBE" --argjson key "$PR_KEY" --argjson text "$PR_TEXT" --argjson instr "$(instr_json)" \
    '{status:$st, ms:$ms, found:$found, route:$route, privacy:{noReasonKey:$key, noScenarioText:$text}} + $instr'
}

# The pairs a discovery walk listed against the database: every odd-id (scenario) request with a reason names one deleted account of the two, and that pair must be listed, once. Sets PAIR_EXACT, PAIR_DUP.
legacy_pairs_check() { # tombstone-A tombstone-B
  jq -r '"\(.requestId),\(.userId)"' "$WORK/items.jsonl" | sort >"$WORK/pairs.listed"
  PAIR_DUP="$(( $(wc -l <"$WORK/pairs.listed") - $(sort -u "$WORK/pairs.listed" | wc -l) ))"
  psql_db -c "SELECT id || ',' || CASE WHEN requester_id IN ($1, $2) THEN requester_id ELSE subject_user_id END FROM contact_requests WHERE id % 2 = 1 AND reason <> '' AND (requester_id IN ($1, $2) OR subject_user_id IN ($1, $2))" | sort >"$WORK/pairs.want"
  if cmp -s "$WORK/pairs.listed" "$WORK/pairs.want"; then PAIR_EXACT=true; else PAIR_EXACT=false; fi
}
# Reasons an account has with text, from the database.
legacy_account_text() { psql_db -c "SELECT count(*) FROM contact_requests WHERE reason <> '' AND (requester_id = $1 OR subject_user_id = $1)"; }
legacy_audit_mark() { psql_db -c 'SELECT coalesce(max(id), 0) FROM audit_logs'; }

# --- the save ---------------------------------------------------------------------------------------------------------------------------------------------
# The body of a save over items [start, start+count) of an account's served pages: the first remove-first of them are removed, the rest kept, each with the review hash the page served.
legacy_put_body() { # items-file start count remove-first attempt-id
  jq -c --argjson s "$2" --argjson c "$3" --argjson r "$4" --arg id "$5" \
    '{attemptId:$id, decisions:(.[$s:$s+$c] | to_entries | map({requestId:.value.requestId, reviewHash:.value.reviewHash, choice:(if .key < $r then "remove" else "keep" end)}))}' "$1"
}
legacy_save_send() { read -r LS_STATUS LS_MS <<<"$(timed PUT "/admin/erasure-reasons/legacy/$1" "$2" "$WORK/put.out")"; } # target body-file
# The effects of the 100-decision save, read from the database against what it was asked to do, as one JSON object.
legacy_effects_sql() { # tombstone-A tombstone-B ids removed-ids kept-ids mark
  printf "SELECT json_build_object(
    'markers', (SELECT count(*) FROM audit_logs WHERE id > %s AND action IN ('add.reason_kept','add.reason_removed') AND target_type = 'add_request' AND target_id IN (%s) AND jsonb_typeof(meta_json->'erasedUserId') = 'number' AND meta_json->>'erasedUserId' = '%s' AND meta_json->>'reviewVersion' = '1' AND meta_json->>'source' = 'legacy_review'),
    'keptMarkers', (SELECT count(*) FROM audit_logs WHERE id > %s AND action = 'add.reason_kept' AND target_id IN (%s) AND meta_json->>'erasedUserId' = '%s'),
    'removedMarkers', (SELECT count(*) FROM audit_logs WHERE id > %s AND action = 'add.reason_removed' AND target_id IN (%s) AND meta_json->>'erasedUserId' = '%s'),
    'audits', (SELECT count(*) FROM audit_logs WHERE id > %s AND action IN (%s)),
    'legacyAudits', (SELECT count(*) FROM audit_logs WHERE id > %s AND action IN (%s) AND meta_json->>'source' = 'legacy_review'),
    'blank', (SELECT count(*) FROM contact_requests WHERE id IN (%s) AND reason = ''),
    'withText', (SELECT count(*) FROM contact_requests WHERE id IN (%s) AND reason <> ''),
    'removedBlank', (SELECT count(*) FROM contact_requests WHERE id IN (%s) AND reason = ''),
    'keptText', (SELECT count(*) FROM contact_requests WHERE id IN (%s) AND reason <> ''),
    'aText', (SELECT count(*) FROM contact_requests WHERE reason <> '' AND (requester_id = %s OR subject_user_id = %s)),
    'bText', (SELECT count(*) FROM contact_requests WHERE reason <> '' AND (requester_id = %s OR subject_user_id = %s)),
    'receipts', (SELECT count(*) FROM admin_operation_attempts))::text" \
    "$6" "$3" "$1" "$6" "$3" "$1" "$6" "$3" "$1" "$6" "$LEGACY_REASON_ACTIONS" "$6" "$LEGACY_REASON_ACTIONS" "$3" "$3" "$4" "$5" "$1" "$1" "$2" "$2"
}
# The refusal of a 101st decision, then the accepted 100-decision save, each with its own fresh attempt id (the refusal claims its id, so it is never reused). $1 is the account, $2 its served
# items (id order, at least 101), $3 the word `instrumented` for the measured save or nothing for the smoke, $4 the other deleted account (its reasons must not change).
# Sets PUT_REFUSAL_JSON and PUT_JSON (the row's `put` before the instruments are merged) and PUT_APP_MARKER (the shape of one marker the application wrote).
legacy_put_run() { # tombstone items-file wrap other-tombstone
  local t="$1" items="$2" wrap="${3:-}" other="$4" line att ids rmids kids mark r0 r1 a0 b0 st ms_ rbytes ebytes eff txt_exact=false k rem rows_ok
  jq -e '[.[:101][].requestId | tonumber] as $a | ($a | length) == 101 and $a == ($a | sort)' "$items" >/dev/null || { echo 'qc1: the account page did not serve at least 101 reasons in id order' >&2; return 1; }
  ids="$(jq -r '[.[:100][].requestId] | join(",")' "$items")"; rmids="$(jq -r '[.[:50][].requestId] | join(",")' "$items")"; kids="$(jq -r '[.[50:100][].requestId] | join(",")' "$items")"
  # (1) the 101st decision: refused by count validation, nothing recorded. 101 valid, unique entries of the served pages and a fresh attempt id of its own.
  att="$(mint_attempt "$t" legacy_reason_save)" || return 1
  legacy_put_body "$items" 0 101 0 "$att" >"$WORK/put.refusal.body"
  rbytes="$(wc -c <"$WORK/put.refusal.body")"
  r0="$(psql_db -c 'SELECT count(*) FROM admin_operation_attempts')"; mark="$(legacy_audit_mark)"; a0="$(legacy_account_text "$t")"
  legacy_save_send "$t" "$WORK/put.refusal.body"
  st="$LS_STATUS"; ms_="$LS_MS"
  r1="$(psql_db -c 'SELECT count(*) FROM admin_operation_attempts')"
  PUT_REFUSAL_JSON="$(jq -nc --arg st "$st" --argjson ms "$ms_" --argjson bytes "$rbytes" --arg err "$(jq -r '.error // ""' "$WORK/put.out" 2>/dev/null || true)" \
    --argjson r0 "$r0" --argjson r1 "$r1" --argjson a0 "$a0" --argjson a1 "$(legacy_account_text "$t")" --argjson audits "$(psql_db -c "SELECT count(*) FROM audit_logs WHERE id > $mark AND action IN ($LEGACY_REASON_ACTIONS)")" \
    '{decisions:101, status:$st, error:$err, authenticated:($st == "400" and $err == "invalid decisions"), reason:(if $st == "400" and $err == "invalid decisions" then "decision_count" else "unrecognized" end),
      reasonBasis:"constructed: 101 individually valid unique entries from the served pages with a fresh attempt id; the route answers 400 invalid decisions for a count above 100 with no code, so the reason is bound by this construction and by that exact answer",
      bodyBytes:$bytes, ms:$ms, unchanged:($r1 == $r0 and $a1 == $a0 and $audits == 0), receiptsDelta:($r1 - $r0)}')"
  # (2) the accepted save: a second attempt id, the first 100, the first 50 removed and the last 50 kept.
  att="$(mint_attempt "$t" legacy_reason_save)" || return 1
  legacy_put_body "$items" 0 100 50 "$att" >"$WORK/put.body"
  qc1_text_snapshot_ids_sql "$ids" | psql_stdin >"$WORK/text.pre"
  mark="$(legacy_audit_mark)"; r0="$(psql_db -c 'SELECT count(*) FROM admin_operation_attempts')"
  a0="$(legacy_account_text "$t")"; b0="$(legacy_account_text "$other")"
  # shellcheck disable=SC2086 # $wrap is the word instrumented or nothing
  $wrap legacy_save_send "$t" "$WORK/put.body"
  # The instrument figures of the window just closed, read before anything else runs; the smoke has no window.
  if [[ -n "$wrap" ]]; then PUT_INSTR="$(instr_json)"; else PUT_INSTR='{}'; fi
  qc1_text_snapshot_ids_sql "$ids" | psql_stdin >"$WORK/text.post"
  if line="$(qc1_text_exact "$WORK/text.pre" "$WORK/text.post" 50)"; then IFS='|' read -r txt_exact _ <<<"$line"; fi
  eff="$(legacy_effects_sql "$t" "$other" "$ids" "$rmids" "$kids" "$mark" | psql_stdin)"
  k="$(jq -r '.kept // -1' "$WORK/put.out" 2>/dev/null || echo -1)"; rem="$(jq -r '.removed // -1' "$WORK/put.out" 2>/dev/null || echo -1)"
  ebytes="$(wc -c <"$WORK/put.body")"
  PUT_APP_MARKER="$(psql_db -c "SELECT json_build_object('target', target_type, 'action', action, 'keys', (SELECT json_agg(x ORDER BY x) FROM jsonb_object_keys(meta_json) x), 'erasedUserId', jsonb_typeof(meta_json->'erasedUserId'), 'reviewVersion', jsonb_typeof(meta_json->'reviewVersion'))::text FROM audit_logs WHERE id > $mark AND action = 'add.reason_kept' AND target_id IN ($ids) ORDER BY id LIMIT 1")"
  PUT_MARK="$mark"
  PUT_JSON="$(jq -nc --arg st "$LS_STATUS" --argjson ms "$LS_MS" --argjson eff "$eff" --argjson exact "$txt_exact" --argjson t "$t" --argjson bytes "$ebytes" --argjson size "$CURRENT_SIZE" \
    --argjson rk "${k:--1}" --argjson rr "${rem:--1}" --argjson r0 "$r0" --argjson a0 "$a0" --argjson b0 "$b0" --arg route "$LEGACY_ROUTE_SAVE" --argjson seed "$LEGACY_SEED_PAIRS" '
    ($eff.keptMarkers) as $kept | ($eff.removedMarkers) as $rm
    | {status:$st, ms:$ms, route:$route, decisions:100, accepted:(if $rk >= 0 and $rr >= 0 then $rk + $rr else null end), bodyBytes:$bytes, mints:2,
       workload:{backgroundRows:$size, loadedPairs:$seed, tombstoneId:$t},
       effects:{dbOk:($eff.markers == 100 and $eff.audits == 100 and $eff.legacyAudits == 100 and $eff.blank == 50 and $eff.withText == 50 and $eff.removedBlank == 50 and $eff.keptText == 50 and $rk == 50 and $rr == 50
                      and $exact == true and $eff.aText == ($a0 - 50) and $eff.bText == $b0 and $eff.receipts == ($r0 + 1)),
                kept:$kept, blanked:$rm, rowsBlank:$eff.blank, rowsWithText:$eff.withText, textExact:$exact, othersUntouched:($eff.aText == ($a0 - 50) and $eff.bText == $b0),
                markers:{count:$eff.markers, party:$t, partyQualified:($eff.markers == 100), source:"legacy_review", byAction:{"add.reason_kept":$kept, "add.reason_removed":$rm}},
                audits:{count:$eff.audits, source:(if $eff.legacyAudits == $eff.audits then "legacy_review" else "mixed" end)},
                receipts:{before:$r0, after:$eff.receipts, delta:($eff.receipts - $r0)}},
       calculatedMaxBodyBytes:{bytes:0, basis:"calculated, never sent"}}')"
  PUT_JSON="$(jq -c --argjson calc "$(jq -nc --arg id "$(printf 'a%.0s' {1..80})" '{attemptId:$id, decisions:[range(0; 100) | {requestId:"-9223372036854775808", reviewHash:("0" * 64), choice:"remove"}]} | tojson | length')" \
    '.calculatedMaxBodyBytes.bytes = $calc | .calculatedMaxBodyBytes.basis = "100 entries with 20-character signed request ids, choice remove, an 80-hex attempt id: a calculation, never sent"' <<<"$PUT_JSON")"
}

# --- one size -----------------------------------------------------------------------------------------------------------------------------------------------
LEGACY_NEED_MS() { # n -> the scheduler window a walk of this size is given (a 1M walk can pass an hourly edge in the default minute)
  if (( $1 >= 1000000 )); then echo 300000; else echo 60000; fi
}
legacy_seed_json() { # pairsA pairsB
  jq -nc --argjson a "$1" --argjson b "$2" '{A:{pairs:$a, layout:"spread", direction:"both"}, B:{pairs:$b, layout:"tail", direction:"both"}}'
}
# A discovery row of the pairs now loaded (k of them; 0 on the background alone).
legacy_discovery_row() { # n k tombA tombB pairsA pairsB
  local n="$1" k="$2" tr
  QC1_NEED_MS="$(LEGACY_NEED_MS "$n")" instrumented legacy_walk discovery 0 "$WORK/legacy-pairs.json"
  tr="$(legacy_traversal_json)"
  legacy_pairs_check "$3" "$4"
  record 4 "legacy-discovery-$k" "$n" "$(jq -nc --argjson tr "$tr" --argjson k "$k" --arg route "$LEGACY_ROUTE_DISCOVERY" --arg src "$LEGACY_SOURCE" --argjson a "$3" --argjson b "$4" --argjson seed "$(legacy_seed_json "$5" "$6")" \
    --argjson exact "$PAIR_EXACT" --argjson dup "$PAIR_DUP" --argjson priv "$(legacy_privacy_json)" '
    {traversal:$tr, reasons:$k, expect:{items:$k},
     legacy:{source:$src, route:$route, loadedPairs:$k, tombstones:{A:$a, B:$b}, seed:$seed, pairSetExact:$exact, duplicates:$dup, privacy:$priv, firstFullReadAfterLoad:($k == 0),
             probeEquivalent:(if $k == 0 then {unitMs:$tr.unitMs[0], note:"the first unit of this walk runs the statements the probe runs on the same table; a derived equivalence, not a probe request"} else null end)}}')"
}
# An account's own pages (the exact-account walk) for the account of tombstone $3, over the pairs now loaded.
legacy_account_row() { # n case tombstone items-out layout
  local n="$1" c="$2" t="$3" out="$4" tr want ref uniq
  QC1_NEED_MS="$(LEGACY_NEED_MS "$n")" instrumented legacy_walk account "$t" "$out"
  tr="$(legacy_traversal_json)"
  want="$(psql_db -c "SELECT count(*) FROM contact_requests WHERE id % 2 = 1 AND reason <> '' AND (requester_id = $t OR subject_user_id = $t)")"
  ref="$(jq -e --arg t "$t" 'all(.[]; .partyRef == $t)' "$out" >/dev/null 2>&1 && echo true || echo false)"
  uniq="$(jq '[.[].requestId] | unique | length' "$out")"
  record 4 "$c" "$n" "$(jq -nc --argjson tr "$tr" --argjson items "$TR_ITEMS" --arg route "$LEGACY_ROUTE_PAGE" --arg src "$LEGACY_SOURCE" --argjson t "$t" --argjson want "$want" --argjson ref "$ref" --argjson uniq "$uniq" --argjson seed "$LEGACY_SEED_PAIRS" --arg layout "$5" '
    {traversal:$tr, reasons:$items, expect:{items:$items},
     legacy:{source:$src, route:$route, tombstoneId:$t, loadedPairs:$seed, accountPairs:$want, seedLayout:$layout, partyRefOk:$ref, uniqueRequests:$uniq}}')"
}
legacy_put_row() { # n tombA tombB
  local n="$1" a="$2" b="$3"
  legacy_put_run "$a" "$WORK/legacy-A-items.json" instrumented "$b"
  record 5 legacy-put "$n" "$(jq -nc --argjson put "$PUT_JSON" --argjson refusal "$PUT_REFUSAL_JSON" --argjson instr "$PUT_INSTR" --argjson a "$a" --argjson b "$b" --arg src "$LEGACY_SOURCE" '
    {put:($put + $instr + {countRefusal:$refusal}), legacy:{source:$src, seedLayout:"spread", tombstones:{A:$a, B:$b}}}')"
}
legacy_probe_row() { # n tombA mark
  local n="$1" a="$2" mark="$3" marker seeded
  instrumented legacy_probe
  PR_JSON="$(legacy_probe_json)"
  marker="$(psql_db -c "SELECT json_build_object('requestId', target_id::text, 'party', (meta_json->>'erasedUserId')::bigint, 'action', action, 'source', meta_json->>'source')::text FROM audit_logs WHERE id > $mark AND action IN ('add.reason_kept','add.reason_removed') AND meta_json->>'erasedUserId' = '$a' ORDER BY id LIMIT 1")"
  seeded="$(psql_db -c "SELECT count(*) FROM audit_logs WHERE id > $mark AND action IN ('add.reason_kept','add.reason_removed') AND meta_json->>'erasedUserId' = '$a'")"
  record 4 legacy-discovery-probe "$n" "$(jq -nc --argjson probe "$PR_JSON" --argjson marker "${marker:-null}" --argjson seeded "$seeded" --argjson a "$a" --argjson size "$n" --argjson seed "$LEGACY_SEED_PAIRS" --arg src "$LEGACY_SOURCE" '
    {probe:($probe + {workload:{backgroundRows:$size, loadedPairs:$seed, markersSeeded:$seeded, markersSource:"written by the application in this size'"'"'s legacy-put", tombstoneId:$a}, marker:$marker}),
     legacy:{source:$src, tombstoneId:$a}}')"
}
# The decided history (the marker-heavy path): a fresh deleted account is the requester of EVERY background request, each pair gets a forged marker, so the walk lists nothing and
# every request costs a marker lookup. Everything after the reset is this arm's own: its own account, its own table.
legacy_decided_row() { # n
  local n="$1" d cand seeded tr probe_json forged app_shape forged_shape keys_match
  quiesce
  qc1_reset_contact_requests || return 1
  make_tombstone D; d="$TOMB_ID"
  load_filler "$n" all "$d"
  forge_markers "$d" 0 || { echo "qc1: the forged markers for the decided history were refused or failed" >&2; return 1; }
  psql_db -c 'ANALYZE audit_logs' >/dev/null
  psql_db -c 'CHECKPOINT' >/dev/null
  cand="$(psql_db -c "SELECT count(*) FROM contact_requests WHERE reason <> '' AND (requester_id = $d OR subject_user_id = $d)")"
  seeded="$(psql_db -c "SELECT count(*) FROM audit_logs WHERE action = 'add.reason_kept' AND target_type = 'add_request' AND meta_json->>'erasedUserId' = '$d' AND meta_json->>'source' = 'legacy_review'")"
  forged_shape="$(psql_db -c "SELECT json_build_object('target', target_type, 'action', action, 'keys', (SELECT json_agg(x ORDER BY x) FROM jsonb_object_keys(meta_json) x), 'erasedUserId', jsonb_typeof(meta_json->'erasedUserId'), 'reviewVersion', jsonb_typeof(meta_json->'reviewVersion'))::text FROM audit_logs WHERE action = 'add.reason_kept' AND meta_json->>'erasedUserId' = '$d' ORDER BY id LIMIT 1")"
  app_shape="${PUT_APP_MARKER:-null}"
  keys_match="$(jq -nc --argjson a "${app_shape:-null}" --argjson f "${forged_shape:-null}" '$a != null and $a == $f')"
  QC1_NEED_MS=$(( $(LEGACY_NEED_MS "$n") * 2 )) instrumented legacy_walk discovery 0 "$WORK/legacy-pairs.json"
  tr="$(legacy_traversal_json)"; DEC_PRIV="$(legacy_privacy_json)"
  instrumented legacy_probe
  probe_json="$(legacy_probe_json)"
  record 4 legacy-decided-discovery "$n" "$(jq -nc --argjson tr "$tr" --argjson probe "$probe_json" --arg route "$LEGACY_ROUTE_DISCOVERY" --arg src "$LEGACY_SOURCE" --argjson d "$d" --argjson cand "$cand" --argjson seeded "$seeded" \
    --argjson keys "$keys_match" --argjson app "${app_shape:-null}" --argjson forged "${forged_shape:-null}" --arg sha "$(sha256sum <"$HERE/qc1/legacy-markers.sql" | cut -d' ' -f1)" --argjson priv "$DEC_PRIV" '
    {traversal:$tr, reasons:0, expect:{items:0}, probe:$probe,
     legacy:{source:$src, route:$route, tombstoneId:$d, backgroundRequester:$d, candidatePairs:$cand, markersSeeded:$seeded, privacy:$priv, firstFullReadAfterLoad:true,
             forgedMarkers:{file:"deploy/digitalocean/qc1/legacy-markers.sql", sha256:$sha, keysMatch:$keys, appShape:$app, forgedShape:$forged},
             preparation:"background requests reloaded with the deleted account as requester; one forged marker per pair; ANALYZE contact_requests and audit_logs; CHECKPOINT; no VACUUM, as the matrix loads"}}')"
  DECIDED_TOMB="$d"
}
# The application's own logged plans of one discovery page over the decided history (the state with the most marker work), captured with auto_explain on for that page only.
legacy_plans_row() { # n
  local n="$1" since st ms_ log counts access rows marks aud
  autoexplain_on
  since="$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"
  read -r st ms_ <<<"$(timed GET /admin/erasure-reasons/legacy "$WORK/empty.body" "$WORK/page.out")"
  autoexplain_off
  log="$WORK/pglog-legacy-$n.txt"
  docker logs --since "$since" "$DB_CID" >"$log" 2>&1
  : >"$WORK/explain-legacy-$n.log"
  for which in "${QC1_LEGACY_PLAN_KINDS[@]}"; do { printf '=== %s (size %s)\n' "$which" "$n"; qc1_extract_plans "$log" "$which"; } >>"$WORK/explain-legacy-$n.log"; done
  counts="$(qc1_plan_counts "$log" "${QC1_LEGACY_PLAN_KINDS[@]}")"
  access="$(qc1_marker_plan_access "$log")" || access='null'
  rows="$(table_rows)"; marks="$(psql_db -c "SELECT count(*) FROM audit_logs WHERE action = 'add.reason_kept' AND meta_json->>'erasedUserId' = '$DECIDED_TOMB'")"; aud="$(psql_db -c 'SELECT count(*) FROM audit_logs')"
  record 11 legacy-plans "$n" "$(jq -nc --argjson c "$counts" --argjson acc "${access:-null}" --arg st "$st" --argjson ms "$ms_" --argjson d "$DECIDED_TOMB" --argjson rows "${rows:-null}" --argjson marks "$marks" --argjson aud "$aud" --arg src "$LEGACY_SOURCE" --argjson planner "$(planner_settings)" '
    {plans:$c, markerPlans:$acc, request:{status:$st, ms:$ms}, state:{tombstoneId:$d, contactRequests:$rows, markers:$marks, auditRows:$aud}, legacy:{source:$src}, plannerSettings:$planner, log:"explain-legacy",
     autoExplain:{scope:"on for one discovery page of the decided history, off and awaited before the log was read",
       settings:{"auto_explain.log_min_duration":0, "auto_explain.log_analyze":"on", "auto_explain.log_buffers":"on", "auto_explain.log_timing":"on", "auto_explain.log_format":"text", "auto_explain.log_nested_statements":"off", "log_lock_waits":"on"},
       caveats:["one execution of each statement per batch of the page: a plan with ANALYZE and BUFFERS describes that run (cache state and buffers as they were)", "the marker lookup reads audit_logs by the target index only when the planner chooses it: auditLogsAccess says what it chose, and no threshold is judged", "other sessions of the cluster write to the same log", "the page time includes the capture overhead and is never judged"]}}')"
}

# The size's gate: every row of this size is there, and the judge calls none of them invalid. A budget finding never stops the run; an invalid row at a smaller size stops it before a larger one is paid for.
legacy_gate() { # n
  local bad have
  bad="$(qc1_size_invalid "$MEASUREMENTS" "$1" "${MAX_REQUEST_BYTES:-0}")" || { echo "qc1: the judge could not read the rows of size $1" >&2; return 1; }
  [[ -z "$bad" ]] || { printf 'qc1: invalid findings at size %s:\n%s\n' "$1" "$bad" >&2; return 1; }
  have="$(jq -s --argjson s "$1" '[.[] | select(.size == $s and .status == "measured")] | length' "$MEASUREMENTS")"
  [[ "$have" == 9 ]] || { echo "qc1: size $1 has $have measured legacy rows, not the 9 it owes" >&2; return 1; }
}

legacy_size() { # n
  local n="$1" a b
  CURRENT_SIZE="$n"
  note "size $n: legacy QC4/5"
  quiesce
  qc1_reset_contact_requests || exit 1
  reset_background_checks
  # Deleted accounts first, while contact_requests is empty: no traversal to hide inside the load, and the table the first discovery reads holds exactly the background rows.
  make_tombstone A; a="$TOMB_ID"
  make_tombstone B; b="$TOMB_ID"
  load_filler "$n" all
  legacy_discovery_row "$n" 0 "$a" "$b" 0 0
  load_scenario "$n" 100 "$a" both "$CPS" spread
  legacy_discovery_row "$n" 100 "$a" "$b" 100 0
  drop_scenarios
  load_scenario "$n" "$LEGACY_A_PAIRS" "$a" both "$CPS" spread
  load_scenario "$n" "$LEGACY_B_PAIRS" "$b" both "$CPS" tail
  legacy_discovery_row "$n" "$LEGACY_SEED_PAIRS" "$a" "$b" "$LEGACY_A_PAIRS" "$LEGACY_B_PAIRS"
  legacy_account_row "$n" legacy-account-dense "$a" "$WORK/legacy-A-items.json" spread
  legacy_account_row "$n" legacy-account-sparse-tail "$b" "$WORK/legacy-B-items.json" tail
  local mark; mark="$(legacy_audit_mark)"
  legacy_put_row "$n" "$a" "$b"
  legacy_probe_row "$n" "$a" "$mark"
  legacy_decided_row "$n"
  legacy_plans_row "$n"
  legacy_gate "$n"
}

# --- the preflight smoke: the routes, the refusal binding, the effects, the forged marker and the guard, on 101 pairs, before any background load ----------------------------------
SMOKE_CHECKS=()
smoke_ok() { SMOKE_CHECKS+=("$1"); }
smoke_fail() { echo "qc1: legacy smoke failed: $1" >&2; exit 1; }
legacy_smoke() {
  local s other items pairs_count marker n_before rem_id wrong_rc=0 live_rc=0 audit0 audit1 mark att
  note 'legacy smoke: the routes, the save, the forged marker and the guard on 101 pairs'
  CURRENT_SIZE=0
  qc1_reset_contact_requests || exit 1
  make_tombstone S; s="$TOMB_ID"
  # A live counterpart keeps reasons of its own (5, in requests with nobody the save touches), so "the save changed nothing else" is checked against something; the 101 pairs use the other nine.
  other="${CPS%%,*}"
  load_scenario 0 "$LEGACY_SMOKE_PAIRS" "$s" both "${CPS#*,}" head
  load_scenario 300 5 "$other" both "${CPS#*,}" tail
  legacy_walk discovery 0 "$WORK/smoke-pairs.json"
  legacy_pairs_check "$s" "$s"
  [[ "$TR_STATUS" == 200 && "$TR_ITEMS" == 101 && "$PAIR_EXACT" == true && "$PAIR_DUP" == 0 && "$TR_KEY_BAD" == 0 && "$TR_TEXT_BAD" == 0 ]] || smoke_fail 'discovery does not list exactly the 101 seeded pairs'
  smoke_ok discovery-lists-the-seeded-pairs
  audit0="$(psql_db -c "SELECT count(*) FROM audit_logs WHERE action = 'user.erasure_reasons_viewed' AND target_id = $s")"
  legacy_walk account "$s" "$WORK/smoke-items.json"
  audit1="$(psql_db -c "SELECT count(*) FROM audit_logs WHERE action = 'user.erasure_reasons_viewed' AND target_id = $s")"
  [[ "$TR_STATUS" == 200 && "$TR_ITEMS" == 101 && "$TR_NONEMPTY" == 2 && "$((audit1 - audit0))" == 2 ]] || smoke_fail "the account pages did not hold 101 items in 2 audited pages (items $TR_ITEMS, pages $TR_NONEMPTY, view audits $((audit1 - audit0)))"
  smoke_ok account-pages-are-audited-once-each
  [[ "$(mint_attempt "$s" legacy_reason_save)" =~ ^[0-9a-f]{80}$ ]] || smoke_fail 'a legacy_reason_save attempt id is not 80 hex characters'
  smoke_ok attempt-id-mint
  legacy_put_run "$s" "$WORK/smoke-items.json" "" "$other"
  jq -e '.status == "400" and .authenticated == true and .reason == "decision_count" and .unchanged == true and .receiptsDelta == 0' <<<"$PUT_REFUSAL_JSON" >/dev/null || smoke_fail "the 101-decision save was not refused by count validation: $PUT_REFUSAL_JSON"
  smoke_ok count-refusal-bound-and-nothing-changed
  jq -e '.status == "200" and .accepted == 100 and .effects.dbOk == true and .effects.textExact == true and .effects.kept == 50 and .effects.blanked == 50 and .effects.receipts.delta == 1' <<<"$PUT_JSON" >/dev/null || smoke_fail "the 100-decision save did not do what it was asked: $PUT_JSON"
  smoke_ok save-effects-exact
  rem_id="$(jq -r '.[100].requestId' "$WORK/smoke-items.json")"
  legacy_walk discovery 0 "$WORK/smoke-pairs.json"
  [[ "$TR_ITEMS" == 1 && "$(jq -r '.[0].requestId + "," + .[0].userId' "$WORK/smoke-pairs.json")" == "$rem_id,$s" ]] || smoke_fail 'discovery after the save does not list exactly the one undecided pair'
  legacy_probe
  [[ "$PR_STATUS" == 200 && "$PR_FOUND" == true ]] || smoke_fail 'the probe does not find the undecided pair'
  smoke_ok save-decides-pairs-and-probe-finds-the-rest
  forge_markers "$s" "$rem_id" || smoke_fail 'the forged marker was refused on the disposable database'
  legacy_walk discovery 0 "$WORK/smoke-pairs.json"
  [[ "$TR_ITEMS" == 0 ]] || smoke_fail 'a forged marker does not hide the pair it names'
  legacy_probe
  [[ "$PR_STATUS" == 200 && "$PR_FOUND" == false ]] || smoke_fail 'the probe still finds a pair after its forged marker'
  marker="$(psql_db -c "SELECT json_build_object('target', target_type, 'action', action, 'keys', (SELECT json_agg(x ORDER BY x) FROM jsonb_object_keys(meta_json) x), 'erasedUserId', jsonb_typeof(meta_json->'erasedUserId'), 'reviewVersion', jsonb_typeof(meta_json->'reviewVersion'))::text FROM audit_logs WHERE action = 'add.reason_kept' AND target_id = $rem_id ORDER BY id LIMIT 1")"
  jq -e --argjson a "$PUT_APP_MARKER" --argjson f "$marker" -n '$a == $f' >/dev/null || smoke_fail "the forged marker is not shaped like the application's: $marker against $PUT_APP_MARKER"
  smoke_ok forged-marker-is-recognized-and-shaped-like-the-applications
  # The guard: a live account and a wrong token are each refused, and nothing is written.
  audit0="$(psql_db -c 'SELECT count(*) FROM audit_logs')"
  forge_markers "$other" 0 && live_rc=1
  legacy_forge_with 00000000-0000-0000-0000-000000000000 "$s" 0 && wrong_rc=1
  audit1="$(psql_db -c 'SELECT count(*) FROM audit_logs')"
  [[ "$live_rc" == 0 && "$wrong_rc" == 0 && "$audit0" == "$audit1" ]] || smoke_fail 'the marker guard let a live account or a wrong token through, or wrote rows'
  smoke_ok guard-refuses-a-live-account-and-a-wrong-token
  qc1_reset_contact_requests || exit 1
  jq -nc --argjson checks "$(printf '%s\n' "${SMOKE_CHECKS[@]}" | jq -R . | jq -sc .)" --argjson pairs "$QC1_LEGACY_SMOKE_PAIRS" '{ok:true, checks:$checks, pairs:$pairs}' >"$WORK/legacy-smoke.json"
  # Fail closed: the record the judge validates (settings.legacy.smoke) is
  # read back before any background load. A truncated write or a check set
  # that drifted from the defined one aborts the run here, never as rows.
  jq -e --argjson want "$(printf '%s\n' "${QC1_LEGACY_SMOKE_CHECKS[@]}" | jq -R . | jq -sc .)" --argjson pairs "$QC1_LEGACY_SMOKE_PAIRS" \
    '.ok == true and .pairs == $pairs and ((.checks | sort) == ($want | sort))' "$WORK/legacy-smoke.json" >/dev/null \
    || { echo 'qc1: legacy smoke record failed its read-back: it does not carry the passing smoke' >&2; exit 1; }
}
