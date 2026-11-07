#!/usr/bin/env bash
# T07B-2-V QC-1: pure functions of the reason-review measurement harness
# (qc1-measure.sh). Sourced only; qc1-fixtures.sh exercises every function here
# without Docker, SQL or a network. Item numbers follow T07B-D4-CONTRACT R4-C06:
#   1 sparse and no-reason accounts           2 large unrelated tables
#   3 live discovery, both party directions   4 legacy discovery (T07B-3a)
#   5 request size (real valid bodies near and across maxRequestBytes;
#     the legacy PUT body is T07B-3a's)       6 deletion keep/remove/mixed
#   7 loss of in-memory progress              8 plans of the app's statements
#   9 end-to-end observation with background workers on (not attributable)
# shellcheck disable=SC2034,SC2016
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then echo 'source from qc1-measure.sh only' >&2; exit 1; fi

QC1_SIZES_DEFAULT=10000,100000,1000000
QC1_COUNTS_DEFAULT=0,10,100,1000,10000
QC1_DELETE_COUNTS=10,100,1000,10000
# Dense histories the rendered panel is measured with, in a real browser.
QC1_BROWSER_COUNTS=1000,10000
QC1_DIRECTIONS=(requester subject)
QC1_MODES=(keep remove mixed)
# The measured-failure thresholds of R4-C06, in milliseconds.
QC1_UNIT_BUDGET_MS=10000
QC1_DELETE_BUDGET_MS=40000
QC1_TRAVERSAL_BUDGET_MS=120000
# contactLockID (contact_lifecycle.go): the gate pg_advisory_xact_lock takes.
QC1_GATE_LOCK_ID=0x464f4c44434f4e
# Items the harness cannot measure at this tree, with the reason it records.
QC1_PENDING_4='legacy discovery is T07B-3a; the route and the marker lookup are not in this tree'
QC1_PENDING_5='the legacy PUT body is T07B-3a; its route is not in this tree'
# Files whose bytes decide what is measured; each must equal HEAD's blob.
QC1_SOURCE_FILES=(deploy/digitalocean/qc1-measure.sh deploy/digitalocean/qc1/lib.sh deploy/digitalocean/qc1/filler.sql deploy/digitalocean/qc1/scenario.sql
  deploy/digitalocean/qc1/panel-measure.mjs deploy/digitalocean/qc1/browser.mjs deploy/digitalocean/qc1/browser-launch.cjs deploy/digitalocean/qc1/browser-preflight.mjs deploy/digitalocean/qc1/response-accounting.mjs)
# The statements whose plans must be present for every size.
QC1_PLAN_KINDS=(scanner-unit delete-set blank-batch)

# comma list -> one positive integer per line; fails on anything else.
qc1_list() {
  local item; local -a items
  [[ -n "$1" ]] || return 1
  IFS=, read -r -a items <<<"$1"
  for item in "${items[@]}"; do
    [[ "$item" =~ ^[0-9]+$ ]] || { echo "qc1: not a whole number: $item" >&2; return 1; }
    printf '%s\n' "$((10#$item))"
  done
}

# "classid objid" of the contact gate in pg_locks: a bigint advisory key is
# split into its high and low 32 bits, objsubid 1. The harness proves this
# mapping against a real hold before it measures anything (calibration).
qc1_gate_key() {
  local id=$((QC1_GATE_LOCK_ID))
  printf '%d %d' "$((id >> 32))" "$((id & 0xffffffff))"
}

# Go's maxRequestBytes from the source: maxUploadBytes (a << b) plus (c << d).
# Go binds << tighter than +, which bash arithmetic does not.
qc1_max_request_bytes() { # repo-root
  local root="$1" up req a b c d
  up="$(sed -n 's/^const maxUploadBytes = \([0-9][0-9]*\) << \([0-9][0-9]*\).*/\1 \2/p' "$root/api_profile.go")"
  req="$(sed -n 's/^const maxRequestBytes = maxUploadBytes + \([0-9][0-9]*\)<<\([0-9][0-9]*\).*/\1 \2/p' "$root/main.go")"
  [[ -n "$up" && -n "$req" ]] || { echo 'qc1: cannot read maxUploadBytes or maxRequestBytes' >&2; return 1; }
  read -r a b <<<"$up"; read -r c d <<<"$req"
  printf '%d' "$(( (a << b) + (c << d) ))"
}

# What a traversal cost, from the wall clock around the whole client loop
# (total), the sum of its page requests' own times (request) and the number of
# units. The client share is the rest: the CSRF fetches and JSON handling a
# panel also does between requests. Each request time is rounded to a
# millisecond, so a sum may exceed the wall clock by up to one per unit; more
# than that is an accounting error and fails. Prints "total request client".
qc1_traversal_times() { # total_ms request_ms units
  local total="$1" request="$2" units="$3" client
  [[ "$total" =~ ^[0-9]+$ && "$request" =~ ^[0-9]+$ && "$units" =~ ^[0-9]+$ ]] || { echo 'qc1: traversal times must be whole numbers' >&2; return 1; }
  client=$((total - request))
  if (( client < 0 )); then
    (( -client <= units )) || { echo "qc1: the page requests ($request ms) outlast the traversal ($total ms)" >&2; return 1; }
    client=0
  fi
  printf '%d %d %d\n' "$total" "$request" "$client"
}

# Sampler lines are "epoch_ms|gate_count|gate_pids|autovacuum_workers|active_clients"
# (see qc1-measure.sh). Inside [start, end] this prints
#   "spanMs grants firstMs lastMs holders autovacuum maxClients ticks maxGapMs meanGapMs holes"
# spanMs: from the first to the last sample that saw the exclusive gate granted.
# It is a SPAN of samples, not a hold time: the gate may have been released and
# retaken between samples, and a hold shorter than the gap between two samples
# can be missed entirely. grants: samples that saw it granted; holders: distinct
# backends seen holding it; autovacuum and maxClients: the most autovacuum
# workers and active client backends in any sample; ticks: every sample in the
# window; maxGapMs and meanGapMs: the measured spacing of those samples (the
# sampler's real cadence, never an assumed bound); holes: samples between the
# first and last grant that saw no grant (the gate released and retaken, or a
# sample that missed it).
qc1_gate_hold() { # file start_ms end_ms
  awk -F'|' -v s="$2" -v e="$3" '
    /^[0-9]+\|[0-9]+\|[0-9,]*\|[0-9]+\|[0-9]+$/ && $1 >= s && $1 <= e {
      n++; t[n] = $1 + 0; c[n] = $2 + 0
      if ($4 + 0 > av) av = $4 + 0
      if ($5 + 0 > mc) mc = $5 + 0
      if ($2 > 0) { g++; if (!fi) fi = n; li = n; m = split($3, ps, ","); for (i = 1; i <= m; i++) if (ps[i] != "") seen[ps[i]] = 1 }
    }
    END { h = 0; for (k in seen) h++
      mg = 0; for (i = 2; i <= n; i++) if (t[i] - t[i-1] > mg) mg = t[i] - t[i-1]
      mean = (n > 1) ? (t[n] - t[1]) / (n - 1) : 0
      holes = 0; if (fi) for (i = fi; i <= li; i++) if (c[i] == 0) holes++
      if (g) printf "%d %d %d %d %d %d %d %d %d %d %d\n", t[li] - t[fi], g, t[fi], t[li], h, av + 0, mc + 0, n + 0, mg, mean, holes
      else printf "0 0 0 0 0 %d %d %d %d %d 0\n", av + 0, mc + 0, n + 0, mg, mean }' "$1"
}

# Whether the background scheduler could have been at work in [start, end] (epoch
# ms). With FOLD_WORKERS=0 the app still starts its scheduler (jobs_daily.go
# startJobs): it enqueues the date extension and every daily and hourly task at
# start, again at each full UTC hour (one second after) and at church midnight
# (also a full hour in the zone the wizard sets, UTC), and every 30 s while it
# cannot read the church zone. Each is a write transaction to the shared
# database. This is a model of when that happens: a window that touches the
# first 15 s after the app started or the first 5 s of any full hour is
# contaminated. Prints true or false.
qc1_scheduler_overlap() { # start_ms end_ms app_started_ms
  awk -v s="$1" -v e="$2" -v a="$3" 'BEGIN {
    hour = 3600000; settle = 15000; edge = 5000
    if (s <= a + settle && e >= a) { print "true"; exit }
    for (h = int((s - edge) / hour) * hour; h <= e; h += hour) if (s <= h + edge && e >= h) { print "true"; exit }
    print "false" }'
}

# Milliseconds to wait from now (epoch ms) so a window of `need` ms starting
# afterwards touches neither the scheduler hour edge nor the app start settle.
# 0 when it already clears both. A window of 50 minutes or more cannot sit
# between two hourly edges, so the need is capped there and the after-the-fact
# check judges what remains.
qc1_scheduler_wait() { # now_ms need_ms app_started_ms
  awk -v n="$1" -v need="$2" -v a="$3" 'BEGIN {
    hour = 3600000; settle = 15000; edge = 5000; w = 0
    if (need > 3000000) need = 3000000   # a window of an hour or more cannot sit between two edges
    if (n <= a + settle) w = a + settle + 1 - n
    t = n + w
    h = int((t - edge) / hour) * hour
    for (; h <= t + need; h += hour) if (t <= h + edge && t + need >= h) { w = h + edge + 1 - n; t = n + w }
    printf "%d\n", w }'
}

# WAL bytes between two LSNs in text form (X/Y); done in awk on the hex parts.
qc1_lsn_bytes() { # from to
  awk -v a="$1" -v b="$2" '
    function hex(s,  i, c, v) { v = 0; s = tolower(s); for (i = 1; i <= length(s); i++) { c = index("0123456789abcdef", substr(s, i, 1)) - 1; if (c < 0) return -1; v = v * 16 + c } return v }
    function lsn(t,  p) { split(t, p, "/"); if (p[1] == "" || p[2] == "") return -1; return hex(p[1]) * 4294967296 + hex(p[2]) }
    BEGIN { x = lsn(a); y = lsn(b); if (x < 0 || y < 0 || y < x) { print "invalid"; exit 1 } printf "%.0f\n", y - x }'
}

# What a deletion of k reasons does under a choice mode, as "kept removed":
# keep keeps all, remove removes all, mixed removes the first half (the first
# ceil(k/2) entries) and keeps the rest.
qc1_expected_split() { # mode k
  case "$1" in
    keep) printf '%d 0\n' "$2" ;;
    remove) printf '0 %d\n' "$2" ;;
    mixed) printf '%d %d\n' "$(( $2 / 2 ))" "$(( ($2 + 1) / 2 ))" ;;
    *) echo "qc1: unknown mode $1" >&2; return 1 ;;
  esac
}

# The DELETE body the panel sends, exactly as AdminUsers.remove builds it
# (web/src/api.ts): {successors, hostSuccessors, attemptId?, reasonReview?:
# {items:[{requestId, reviewHash, choice}]}} in that key order, compact like
# JSON.stringify, with no trailing newline (so the file length is the request's
# Content-Length). The attempt id is the 80-hex id a press mints first; no
# reasons (n = 0) means no reasonReview. mode: keep, remove or mixed (the first
# half removed). The first n items of the file are used. successors and
# hostSuccessors are JSON objects (default {}).
qc1_delete_body_n() { # mode items-file n [attempt-id] [successors] [hostSuccessors]
  local succ="${5:-}" host="${6:-}"
  [[ -n "$succ" ]] || succ='{}'
  [[ -n "$host" ]] || host='{}'
  jq -jc --arg mode "$1" --argjson n "$3" --arg id "${4:-}" --argjson s "$succ" --argjson h "$host" '
    .[:$n] as $items
    | {successors:$s, hostSuccessors:$h}
      + (if $id != "" then {attemptId:$id} else {} end)
      + (if ($items | length) > 0 then {reasonReview:{items:[ $items | to_entries[]
          | {requestId:.value.requestId, reviewHash:.value.reviewHash,
             choice:(if $mode == "remove" then "remove" elif $mode == "keep" then "keep"
                     elif .key < ($items|length)/2 then "remove" else "keep" end)} ]}} else {} end)' "$2"
}

# The same body with only its last choice replaced: the parse-refusal case is
# the real fitting body with one value changed, so its size is the fitting
# body's up to the length of that one word.
qc1_final_choice_body() { # body-file value
  jq -jc --arg v "$2" '.reasonReview.items[-1].choice = $v' "$1"
}

# The most entries n (of the items in the file, up to its length) whose real
# body, in the full envelope above with the given attempt id, successors and
# hostSuccessors, stays within cap bytes: binary search over the actual
# entries, so the ids and hashes the server issued and the envelope the browser
# builds set the size, not an estimate. Prints "n bytes_at_n bytes_at_n_plus_1"
# (the last is empty when n is the whole file); fails when even one entry is
# over the cap.
qc1_fit_entries() { # mode items-file cap attempt-id [successors] [hostSuccessors]
  local mode="$1" file="$2" cap="$3" id="$4" succ="${5:-}" host="${6:-}" lo=0 hi mid b at next="" total
  hi="$(jq 'length' "$file")"; total="$hi"
  b="$(qc1_delete_body_n "$mode" "$file" 1 "$id" "$succ" "$host" | wc -c)"
  (( b <= cap )) || { echo 'qc1: one entry is already over the cap' >&2; return 1; }
  while (( lo < hi )); do
    mid=$(( (lo + hi + 1) / 2 ))
    b="$(qc1_delete_body_n "$mode" "$file" "$mid" "$id" "$succ" "$host" | wc -c)"
    if (( b <= cap )); then lo=$mid; else hi=$((mid - 1)); fi
  done
  at="$(qc1_delete_body_n "$mode" "$file" "$lo" "$id" "$succ" "$host" | wc -c)"
  if (( lo < total )); then next="$(qc1_delete_body_n "$mode" "$file" "$((lo + 1))" "$id" "$succ" "$host" | wc -c)"; fi
  printf '%d %d %s\n' "$lo" "$at" "$next"
}

# Entries to load so that a real body is sure to pass the cap: the smallest
# possible entry (one-digit id, "keep") is 120 bytes, so cap/110 is enough.
qc1_probe_entries() { # cap
  awk -v m="$1" 'BEGIN { printf "%d\n", int(m / 110) + 1 }'
}

# What a sitting could hold, from the measured page loads. rows is
# [{direction, reasons, pageLoadMs}] (the wall clock of a full traversal, V-03).
# The time a person spends per reason and how long a sitting lasts before a
# lost tab, a closed laptop or an idle session is plausible are ASSUMPTIONS,
# stated in the output and settable on the runner; nothing here is measured but
# the page loads. Output: the assumptions, the estimated minutes at every
# measured count, the first measured count whose sitting is longer than the
# assumed one (per direction; null when none is), and the count a sitting of
# that length holds at all (reasonsPerSitting, ignoring page load).
qc1_sitting_assessment() { # rows-json seconds_per_reason sitting_minutes
  jq -c --argjson sec "$2" --argjson sit "$3" '
    . as $rows
    | { assumption: "seconds per reason and the sitting length are assumptions, not measurements; page loads are measured",
        secondsPerReason: $sec, sittingMinutes: $sit,
        reasonsPerSitting: (($sit * 60 / $sec) | floor),
        perCount: [ $rows[] | {direction, reasons, pageLoadMs, estimatedMinutes: ((((.pageLoadMs / 1000) + (.reasons * $sec)) / 60 * 10 | round) / 10), longerThanSitting: (((.pageLoadMs / 1000) + (.reasons * $sec)) > ($sit * 60))} ],
        firstLongerThanSitting: ( [ "requester", "subject" ] | map(. as $d | { key: $d, value: ([ $rows[] | select(.direction == $d) | select(((.pageLoadMs / 1000) + (.reasons * $sec)) > ($sit * 60)) ] | sort_by(.reasons) | (.[0].reasons // null)) }) | from_entries ) }' <<<"$1"
}

# --- identity of what is measured ------------------------------------------------------
# For each measurement source file: its sha256, its git blob, HEAD's blob and
# whether they are the same. Prints a JSON array; fails when any differ (the
# tree measured is not the tree committed) or a file is missing.
qc1_source_hashes() { # repo-root
  local root="$1" p sha blob head ok=0 first=1
  printf '['
  for p in "${QC1_SOURCE_FILES[@]}"; do
    [[ -f "$root/$p" ]] || { echo "qc1: $p is missing" >&2; return 1; }
    sha="$(sha256sum <"$root/$p" | cut -d' ' -f1)"
    blob="$(git -C "$root" hash-object "$root/$p")"
    head="$(git -C "$root" rev-parse "HEAD:$p" 2>/dev/null || true)"
    [[ -n "$head" && "$blob" == "$head" ]] || ok=1
    (( first )) || printf ','
    first=0
    jq -nc --arg p "$p" --arg sha "$sha" --arg blob "$blob" --arg head "$head" '{path:$p, sha256:$sha, gitBlob:$blob, headBlob:$head, matches:($blob == $head and $head != "")}'
  done
  printf ']\n'
  return "$ok"
}

# Problems with an environment record, one per line; nothing when it binds the
# measurement to HEAD and to immutable image ids and its instruments were
# proved before use. head is HEAD's full SHA.
qc1_environment_problems() { # environment.json head
  jq -r --arg head "$2" --argjson sources "$(printf '%s\n' "${QC1_SOURCE_FILES[@]}" | jq -R . | jq -sc .)" '
    def hex64: test("^[0-9a-f]{64}$");
    def sha256id: test("^sha256:[0-9a-f]{64}$");
    ( if .revision != $head then "revision \(.revision) is not HEAD \($head)" else empty end ),
    ( if (.imageId // "" | sha256id | not) then "imageId is not an immutable sha256 id" else empty end ),
    ( if .appImageId != .imageId then "the app container runs \(.appImageId), not the bound image \(.imageId)" else empty end ),
    ( if (.postgresImageId // "" | sha256id | not) then "postgresImageId is not an immutable sha256 id" else empty end ),
    ( if .dbImageId != .postgresImageId then "the database container runs \(.dbImageId), not \(.postgresImageId)" else empty end ),
    ( .imageId as $img | .appContainers[]? | select(.imageId != $img) | "app container \(.id) ran \(.imageId), not the bound image" ),
    ( if (.appContainers // [] | length) == 0 then "no app container is recorded" else empty end ),
    ( if .limitsApplied != true then "the droplet memory limits were not applied by the daemon (a rootless daemon without the memory controller?)" else empty end ),
    ( if (.appContainerId // "" | hex64 | not) then "appContainerId is not a container id" else empty end ),
    ( if (.dbContainerId // "" | hex64 | not) then "dbContainerId is not a container id" else empty end ),
    ( if (.postgresVersion // "" | test("^16[.]")) | not then "postgres is not 16: \(.postgresVersion)" else empty end ),
    ( if ((.sources // []) | map(.path) | sort) != ($sources | sort) then "sources do not name exactly the measurement files" else empty end ),
    ( .sources[]? | select(.matches != true) | "source \(.path) differs from HEAD" ),
    ( if (.sources // [] | all(.sha256 | hex64)) | not then "a source has no sha256" else empty end ),
    ( if (.daemon.rootless | type) != "boolean" then "daemon.rootless is not recorded" else empty end ),
    ( if .daemon.sharedDaemon != false then "the daemon was shared with other containers" else empty end ),
    ( if .settings.workers != 0 then "measured rows need the background workers off" else empty end ),
    ( if .settings.autovacuum != "off" then "autovacuum was not turned off" else empty end ),
    ( if .calibration.gate.ok != true then "the gate sampler did not see a known hold" else empty end ),
    ( if .calibration.dbMemory.ok != true then "the database memory source is unreadable" else empty end ),
    ( if .calibration.appMemory.ok != true then "the app process memory source is unreadable" else empty end ),
    ( if .calibration.appMemory.hwmReset != true then "the app peak-memory reset failed" else empty end ),
    ( if .calibration.wal.ok != true then "the WAL position is unreadable" else empty end ),
    ( if .calibration.commit.ok != true then "commit statistics are unreadable" else empty end ),
    ( if .settings.browser == true and (.browserRuntime.ok != true or ((.browserRuntime.chromium // "") == "") or ((.browserRuntime.playwright // "") == "")) then "the browser preflight is not recorded as passing" else empty end ),
    ( if (.calibration.dbMemory.samples // 0) < 3 then "database memory sampling is too sparse to trust (fewer than 3 samples in the calibration half second)" else empty end )' "$1"
}

# Judge a measurements file (one JSON object per line). Each finding is a JSON
# object with a class:
#   budget   a measured failure of R4-C06 for a reviewed engineering
#            disposition: a unit or page above 10 s, a deletion above 40 s, a
#            full traversal above 2 minutes, a valid history whose body
#            exceeds maxRequestBytes;
#   invalid  the measurement itself cannot be trusted: an unexpected HTTP
#            status; an instrument that read nothing (WAL, process or database
#            memory, commit statistics, the gate sampler) or too little (memory
#            or gate samples fewer than the operation's length implies, a gap
#            between gate samples over half the operation); a per-operation
#            peak reset that failed; a window that touched the scheduler or saw
#            scheduler rows; a gate never seen during a long deletion; a
#            competitor (autovacuum, background workers) in a row that claims
#            attribution; discovery, kept or removed counts or database state
#            that differ from what the row set out to do; a traversal whose
#            request times outlast its wall clock; an item-7 row with no
#            assessment; a browser row with a missing figure.
# Rows with status measured or observation are judged; observations (workers
# on) are budget-judged but never required to be attributable.
qc1_judge() { # measurements.jsonl max_request_bytes
  jq -c --argjson unit "$QC1_UNIT_BUDGET_MS" --argjson del "$QC1_DELETE_BUDGET_MS" --argjson trav "$QC1_TRAVERSAL_BUDGET_MS" '
    def inv($why; $x): {class:"invalid", failure:$why} + $x;
    def bud($why; $x): {class:"budget", failure:$why} + $x;
    def numz: if type == "number" then . else null end;
    def ran($r): [$r.traversal, $r.delete] | map(select(type == "object"));
    select(.status == "measured" or .status == "observation") as $raw
    | ($raw | reduce ("traversal","delete","request","expect","outcome","assessment","browser","plans") as $k (.; if (.[$k] | type) == "object" then . else .[$k] = null end) | (if .traversal != null then .traversal |= (.totalMs |= numz | .slowestUnitMs |= numz | .requestMs |= numz | .clientMs |= numz | .units |= numz | .items |= numz) else . end) | (if .delete != null then .delete |= (.ms |= numz | .reasonsKept |= numz | .reasonsRemoved |= numz) else . end)) as $m
    | ( if ([$m.item] | inside([1,2,3,5])) and ($m.item != 5 or $m.case == "request-size") and (($m.traversal | type) != "object" or ($m.traversal.totalMs | type) != "number" or ($m.traversal.slowestUnitMs | type) != "number") then inv("missing-traversal"; {item:$m.item}) else empty end ),
      ( if ([$m.item] | inside([1,2,5,6,9])) and ($m.item != 5 or $m.case == "request-size") and (($m.delete | type) != "object" or ($m.delete.ms | type) != "number") then inv("missing-delete"; {item:$m.item}) else empty end ),
      ( if $m.item == 5 and $m.case == "request-size" and ($m.request | type) != "object" then inv("missing-request"; {item:$m.item}) else empty end ),
      ( if ($m.traversal.slowestUnitMs // 0) > $unit then bud("unit"; {ms:$m.traversal.slowestUnitMs, budget:$unit}) else empty end ),
      ( if ($m.traversal.totalMs // 0) > $trav then bud("traversal"; {ms:$m.traversal.totalMs, budget:$trav}) else empty end ),
      ( if ($m.delete.ms // 0) > $del then bud("delete"; {ms:$m.delete.ms, budget:$del}) else empty end ),
      ( if $m.request.overStatus != null and $m.request.overStatus != "200" then bud("body"; {entries:$m.request.overEntries, fits:$m.request.maxEntriesInBudget, status:$m.request.overStatus}) else empty end ),
      ( if $m.traversal != null and ($m.traversal.lastStatus // "") != "200" then inv("status"; {route:"page", status:$m.traversal.lastStatus}) else empty end ),
      ( if $m.delete != null and ($m.delete.status // "") != "200" then inv("status"; {route:"delete", status:$m.delete.status}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.request.overStatus // "") != "413" and ($m.request.overStatus // "") != "200" then inv("status"; {route:"over-cap", status:$m.request.overStatus}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.request.parseRefusalStatus // "") != "400" then inv("status"; {route:"refused-parse", status:$m.request.parseRefusalStatus}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.overUnchanged != true then inv("over-cap-changed-data"; {}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.parseUnchanged != true then inv("refused-parse-changed-data"; {}) else empty end ),
      ( ran($m)[] | . as $o
        | ( [ ["walBytes", $o.walBytes], ["appPeakKb", $o.appPeakKb], ["dbPeakKb", $o.dbPeakKb], ["gateTicks", $o.gateTicks],
              ["commitCalls", $o.commitCalls], ["commitMsMax", $o.commitMsMax], ["commitMsTotal", $o.commitMsTotal], ["dbMemSamples", $o.dbMemSamples],
              ["gateMaxGapMs", $o.gateMaxGapMs] ] | map(select(.[1] == null) | .[0]) ) as $missing
        | if ($missing | length) > 0 then inv("metric-missing"; {metrics:$missing}) else empty end ),
      ( ran($m)[] | select(.appHwmReset != true) | inv("peak-reset-failed"; {}) ),
      ( ran($m)[] | . as $o | (($o.ms // $o.totalMs // 0) / 100 | floor) as $want
        | if ($o.gateTicks // 0) < $want then inv("sampler-starved"; {ticks:$o.gateTicks, wanted:$want}) else empty end ),
      ( ran($m)[] | . as $o | (($o.ms // $o.totalMs // 0) / 100 | floor) as $want
        | if ($o.dbMemSamples // 0) < ([2, $want] | max) then inv("memory-undersampled"; {samples:$o.dbMemSamples, wanted:([2, $want] | max)}) else empty end ),
      ( ran($m)[] | . as $o | ($o.ms // $o.totalMs // 0) as $d
        | if $d >= 200 and ($o.gateMaxGapMs // 0) > ($d / 2) then inv("sampler-gap"; {maxGapMs:$o.gateMaxGapMs, opMs:$d}) else empty end ),
      ( if $m.delete != null and ($m.delete.status // "") == "200" and ($m.delete.commitCalls // 0) < 1 then inv("no-commit-seen"; {}) else empty end ),
      ( if $m.delete != null and ($m.delete.status // "") == "200" and ($m.delete.ms // 0) >= 200 and ($m.delete.gateHolders // 0) == 0 then inv("gate-unobserved"; {ms:$m.delete.ms}) else empty end ),
      ( if $m.status == "measured" then ( ran($m)[] | select((.autovacuumWorkers // 0) > 0) | inv("competitor"; {what:"autovacuum"}) ) else empty end ),
      ( if $m.status == "measured" and ($m.workers // 0) != 0 then inv("competitor"; {what:"background workers"}) else empty end ),
      ( if $m.status == "measured" then ( ran($m)[] | select(.schedulerOverlap != false or (.schedulerRows // 1) != 0) | inv("scheduler"; {overlap:.schedulerOverlap, rows:.schedulerRows}) ) else empty end ),
      ( if $m.status == "observation" and ($m.workers // 0) == 0 then inv("observation-without-workers"; {}) else empty end ),
      ( if $m.traversal != null and (($m.traversal.requestMs // 0) > ($m.traversal.totalMs // 0) + ($m.traversal.units // 0)) then inv("accounting"; {requestMs:$m.traversal.requestMs, totalMs:$m.traversal.totalMs}) else empty end ),
      ( if $m.traversal != null and (($m.traversal.clientMs // -1) != ([ ($m.traversal.totalMs // 0) - ($m.traversal.requestMs // 0), 0 ] | max)) then inv("accounting"; {clientMs:$m.traversal.clientMs}) else empty end ),
      ( if ([1, 2, 3, 6, 9] | index($m.item)) != null and $m.expect == null then inv("no-expectation"; {}) else empty end ),
      ( if $m.expect != null and $m.traversal != null and ($m.traversal.items // -1) != $m.expect.items then inv("discovery-count"; {items:$m.traversal.items, expected:$m.expect.items}) else empty end ),
      ( if $m.expect != null and $m.delete != null and (($m.delete.reasonsKept // -1) != $m.expect.kept or ($m.delete.reasonsRemoved // -1) != $m.expect.removed) then inv("outcome-count"; {kept:$m.delete.reasonsKept, removed:$m.delete.reasonsRemoved, expected:$m.expect}) else empty end ),
      ( if $m.expect != null and $m.delete != null and $m.outcome.dbOk != true then inv("outcome-db"; {outcome:$m.outcome}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.traversal.items // -1) != $m.request.probeEntries then inv("discovery-count"; {items:$m.traversal.items, expected:$m.request.probeEntries}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.delete != null and (($m.delete.reasonsKept // -1) != $m.request.fitExpect.kept or ($m.delete.reasonsRemoved // -1) != $m.request.fitExpect.removed) then inv("outcome-count"; {kept:$m.delete.reasonsKept, removed:$m.delete.reasonsRemoved, expected:$m.request.fitExpect}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.fitOutcome.dbOk != true then inv("outcome-db"; {outcome:$m.request.fitOutcome}) else empty end ),
      ( if $m.item == 7 and ($m.assessment == null or ($m.assessment.perCount | length) == 0) then inv("assessment-missing"; {}) else empty end ),
      ( if $m.item == 8 and (($m.plans | type) != "object" or ([$m.plans["scanner-unit"], $m.plans["delete-set"], $m.plans["blank-batch"]] | any(type != "number" or . < 1))) then inv("plans-missing"; {}) else empty end ),
      ( if $m.item == 10 then
          ( [ ["renderReadyMs", $m.browser.renderReadyMs], ["itemsRendered", $m.browser.itemsRendered], ["heapAfterGcBytes", $m.browser.heapAfterGcBytes], ["domNodes", $m.browser.domNodes], ["longTaskMs", $m.browser.longTaskMs] ]
            | map(select(.[1] == null) | .[0]) ) as $gone
          | if ($gone | length) > 0 then inv("browser-missing"; {metrics:$gone})
            elif $m.browser.itemsRendered != $m.reasons then inv("browser-count"; {rendered:$m.browser.itemsRendered, expected:$m.reasons})
            elif ($m.browser.pageRequests | type) != "number" or $m.browser.pageRequests < (($m.reasons + 99) / 100 | floor) then inv("browser-walk-short"; {requests:$m.browser.pageRequests, reasons:$m.reasons})
            elif ($m.browser.pageResponseBytes | type) != "number" or $m.browser.pageResponseBytes < ($m.reasons * 100) then inv("browser-bytes"; {bytes:$m.browser.pageResponseBytes, reasons:$m.reasons})
            elif $m.browser.apiOrigin != "real" then inv("browser-not-real-api"; {})
            else empty end
        else empty end )
    | . + {item:$m.item, size:$m.size, case:$m.case}' "$1"
}

# Which measurements a complete QC-1 run holds. Prints
# {complete, missing, pending, invalid, budgetFailures}. sizes and counts are
# comma lists; env is the environment record, head HEAD's SHA, max the
# maxRequestBytes. complete needs nothing missing, nothing pending (items 4 and
# the legacy PUT stay open until T07B-3a), no invalid finding, a sound
# environment record and a judge that ran. Budget failures are reported for
# disposition and do not make a run incomplete.
qc1_evidence_status() { # measurements.jsonl sizes counts environment.json head max_request_bytes
  local sizes counts want judged judge_ok=true env_problems max_size
  # Lines that are not JSON cannot be judged or counted: the whole file is invalid evidence.
  if ! jq -e . "$1" >/dev/null 2>&1; then
    printf '%s\n' '{"complete":false,"missing":[],"pending":[],"invalid":[{"class":"invalid","failure":"unparsable-measurements"}],"budgetFailures":[]}'
    return 0
  fi
  sizes="$(qc1_list "$2" | paste -sd, -)" || return 1
  counts="$(qc1_list "$3" | paste -sd, -)" || return 1
  max_size="$(qc1_list "$2" | sort -n | tail -n 1)"
  want="$(jq -nc --argjson sizes "[$sizes]" --argjson counts "[$counts]" --argjson dc "[$QC1_DELETE_COUNTS]" --argjson bc "[$QC1_BROWSER_COUNTS]" --argjson max "$max_size" '
    [ ($sizes[] as $s | (
        {item:1,size:$s,case:"sparse"}, {item:2,size:$s,case:"unrelated"},
        ($counts[] as $k | ("requester","subject") as $d | {item:3,size:$s,case:("discovery-" + $d + "-" + ($k|tostring))}),
        {item:5,size:$s,case:"request-size"}, {item:7,size:$s,case:"human-progress"}, {item:8,size:$s,case:"plans"},
        ($bc[] as $k | {item:10,size:$s,case:("browser-panel-" + ($k|tostring))}),
        ($dc[] as $k | ("requester","subject") as $d | ("keep","remove","mixed") as $m
          | {item:6,size:$s,case:("delete-" + $d + "-" + ($k|tostring) + "-" + $m)}) )),
      {item:9,size:$max,case:"workers-on-delete"} ]')"
  judged="$(qc1_judge "$1" "$6")" || judge_ok=false
  env_problems="$(qc1_environment_problems "$4" "$5" | jq -R . | jq -sc .)" || env_problems='["environment record unreadable"]'
  jq -s -c --argjson want "$want" --arg pending4 "$QC1_PENDING_4" --arg pending5 "$QC1_PENDING_5" --argjson judgeOk "$judge_ok" \
    --argjson envProblems "$env_problems" --argjson judged "$(jq -sc . <<<"$judged")" '
    . as $rows
    | ($rows | map({item, size, case, status})) as $have
    | { missing: [ $want[] | . as $w | select(($have | map(select(.item == $w.item and .size == $w.size and .case == $w.case and (.status == "measured" or (.status == "observation" and $w.item == 9)))) | length) == 0) ],
        pending: ([ $rows[] | select(.status == "pending") | {item, case, reason} ]
                  + (if ($rows | map(select(.item == 4)) | length) == 0 then [{item:4, case:"legacy-discovery", reason:$pending4}] else [] end)
                  + (if ($rows | map(select(.item == 5 and .case == "legacy-put")) | length) == 0 then [{item:5, case:"legacy-put", reason:$pending5}] else [] end)),
        invalid: ( [ $judged[] | select(.class == "invalid") ]
                   + [ $envProblems[] | {class:"invalid", failure:"environment", problem:.} ]
                   + (if $judgeOk then [] else [{class:"invalid", failure:"judge-failed"}] end) ),
        budgetFailures: [ $judged[] | select(.class == "budget") ] }
    | . + {complete: ((.missing | length) == 0 and (.pending | length) == 0 and (.invalid | length) == 0)}' "$1"
}

# The statements whose plans QC-1 records. Plans come from auto_explain's log
# of the application's own statements (never from SQL the harness types), found
# by a fixed substring of the logged "Query Text", which keeps $n placeholders
# and literals. qc1_source_marker is the same substring in the Go source, where
# the placeholders are ?; qc1-fixtures.sh proves both are still in
# account_erasure_reasons.go. The marker lookup is T07B-3a's and is not here.
qc1_plan_marker() { # which
  case "$1" in
    delete-set) printf '%s' "reason <> '' AND (requester_id = \$1 OR subject_user_id = \$2) ORDER BY id FOR UPDATE" ;;
    scanner-unit) printf '%s' 'WHERE ($1::bigint IS NULL OR id > $2::bigint) ORDER BY id LIMIT $3' ;;
    blank-batch) printf '%s' "UPDATE contact_requests SET reason = '' WHERE id = ANY(\$1::bigint[]) AND reason <> '' AND (requester_id = \$2 OR subject_user_id = \$3)" ;;
    *) return 1 ;;
  esac
}
qc1_source_marker() { # which
  qc1_plan_marker "$1" | sed -E 's/\$[0-9]+/?/g'
}

# Plans from a Postgres log whose Query Text contains the marker for `which`.
# A plan is the lines after "duration: ... plan:" up to the next log-prefixed line.
qc1_extract_plans() { # log which
  local marker
  marker="$(qc1_plan_marker "$2")" || return 1
  awk -v marker="$marker" '
    function flush() { if (block != "" && index(block, marker) > 0) { printf "%s\n", block; n++ } block = "" }
    /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9:.]+ [A-Z]+ \[[0-9]+\] / { flush(); keep = ($0 ~ /duration: .* plan:/) ; if (keep) block = $0; next }
    keep { block = block "\n" $0 }
    END { flush() }' "$1"
}

# How many plans of each kind a log holds, as JSON {"scanner-unit":n,...}. A
# size's plans are present only when every kind has at least one.
qc1_plan_counts() { # log
  local kind first=1 n
  printf '{'
  for kind in "${QC1_PLAN_KINDS[@]}"; do
    n="$(qc1_extract_plans "$1" "$kind" | grep -c 'duration: .* plan:' || true)"
    (( first )) || printf ','
    first=0
    printf '"%s":%d' "$kind" "$n"
  done
  printf '}\n'
}
