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

# What the harness takes from the application is app-facts.json, beside this file; it never reads application source. TestQC1AppFacts (guards_deploy_facts_test.go) and
# web/src/components/ReasonReviewSection.test.tsx hold that file equal to the code.
QC1_FACTS="${QC1_FACTS:-$(dirname "${BASH_SOURCE[0]}")/app-facts.json}"
qc1_fact() { local f="$1"; shift; jq -er "$@" "$f" "$QC1_FACTS"; } # jq-filter [jq options] -> the raw value; fails when it is absent

QC1_SIZES_DEFAULT=10000,100000,1000000
QC1_COUNTS_DEFAULT=0,10,100,1000,10000
QC1_DELETE_COUNTS=0,10,100,1000,10000
QC1_LEGACY_COUNTS=0,100,10000   # R4-C06 item 4: legacy discovery with this many unreviewed pairs, per size that can hold them
# Dense histories the rendered panel is measured with, in a real browser.
QC1_BROWSER_COUNTS=1000,10000
QC1_DIRECTIONS=(requester subject)
QC1_MODES=(keep remove mixed)
# The measured-failure thresholds of R4-C06, in milliseconds.
QC1_UNIT_BUDGET_MS=10000
QC1_DELETE_BUDGET_MS=40000
QC1_TRAVERSAL_BUDGET_MS=120000
QC1_SCANNER_UNIT_ROWS="$(qc1_fact .go.reasonUnitRows)"   # rows one page attempt reads at most (reasonUnitRows), now in batches; tableRows above it is the "belowUnit" fact; a continuation exists only when the table holds more
# The rendered panel's budgets. These three are ENGINEERING PROPOSALS of the scaling design (F7, R5), not owner decisions, and are judged as hard
# failures exactly like the three above: a miss is a recorded failure for a reviewed disposition, never a cap, never waived. The browser's 120 s is
# QC1_TRAVERSAL_BUDGET_MS applied to the panel's readiness (the existing "full traversal above 2 minutes" line). No heap or DOM ceiling is set here:
# the design takes the heap ceiling for 100,000 reasons from the first measurement, so it is recorded and not judged.
QC1_CLICK_P95_BUDGET_MS=100
QC1_CLICK_MAX_BUDGET_MS=200
QC1_NAV_BUDGET_MS=300
# Real clicks per browser case (first page while the walk runs, mid-list, last page, after the walk).
QC1_CLICKS_PER_CASE=30
# Dense targets of the scaling design (R5): 100,000 reasons at the tables that can hold them (judged against the SAME budgets; a miss is a failure, not a cap).
QC1_DENSE_COUNTS_DEFAULT=100000
# contactLockID: the gate pg_advisory_xact_lock takes.
QC1_GATE_LOCK_ID="$(qc1_fact .go.contactLockID)"
# Items the harness cannot measure at this tree, with the reason it records.
QC1_PENDING_4='legacy discovery is T07B-3a; the route and the marker lookup are not in this tree'
QC1_PENDING_5='the legacy PUT body is T07B-3a; its route is not in this tree'
# Files whose bytes decide what is measured; each must equal HEAD's blob.
QC1_SOURCE_FILES=(deploy/digitalocean/qc1-measure.sh deploy/digitalocean/qc1/lib.sh deploy/digitalocean/qc1/filler.sql deploy/digitalocean/qc1/scenario.sql
  deploy/digitalocean/qc1/top-statements.sql deploy/digitalocean/qc1/panel-measure.mjs deploy/digitalocean/qc1/browser.mjs deploy/digitalocean/qc1/browser-launch.cjs deploy/digitalocean/qc1/browser-preflight.mjs deploy/digitalocean/qc1/response-accounting.mjs
  deploy/digitalocean/qc1/set-v1.mjs deploy/digitalocean/qc1/panel-core.mjs deploy/digitalocean/qc1/panel-driver.mjs deploy/digitalocean/qc1/page-observer.mjs web/src/lib/reasonSetVectors.json
  deploy/digitalocean/qc1/app-facts.json)
# The statements whose plans must be present for every size.
QC1_PLAN_KINDS=(scanner-initial scanner-continuation page-items delete-set blank-batch)

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

# Go's maxRequestBytes (one image upload plus 1 MiB of other fields), as the facts file states it.
qc1_max_request_bytes() {
  local v
  v="$(qc1_fact .go.maxRequestBytes 2>/dev/null)" && [[ "$v" =~ ^[0-9]+$ ]] || { echo 'qc1: cannot read maxRequestBytes from the application facts' >&2; return 1; }
  printf '%d' "$v"
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

# Run one measured DELETE and keep a small record of how it ended, before the caller's post-request instrumentation can fail:
#   qc1_delete_attempt dir n response-file body-file meta-json -- command...   (the command prints "status ms", as `timed` does)
# Sets DEL_STATUS and DEL_MS. The response file is emptied first, so a request that fails cannot be read as the one before it. The record
# (dir/NNNN.json, written whole, never over another) holds only the caller's controlled fields (meta), the status, the command's exit, the
# duration, byte counts and the response's "code" when it is one of the known ones below. A response with any other code, or none, is
# "withheld": its message is never read or kept. No cause is inferred. A record that cannot be written is said on stderr and changes nothing else.
qc1_delete_attempt() {
  local dir="$1" n="$2" resp="$3" body="$4" meta="$5" out rc=0 code=none rbytes=0 transport=ok c dst
  shift 6
  : >"$resp"
  out="$("$@")" || rc=$?
  read -r DEL_STATUS DEL_MS <<<"$out"
  if [[ -s "$resp" ]]; then
    rbytes="$(wc -c <"$resp")" code=withheld
    if (( rc == 0 )) && c="$(jq -ers 'select(length == 1 and (.[0] | type) == "object" and (.[0].code | type) == "string") | .[0].code' "$resp" 2>/dev/null)"; then
      case "$c" in delete_timed_out | reason_review_busy | reason_review_changed | restore_detected | server_error) code="$c" ;; esac
    fi
  fi
  (( rc == 0 )) || transport=failed
  dst="$(printf '%s/%04d.json' "$dir" "$n")"
  { mkdir -p "$dir" && [[ ! -e "$dst" ]] && jq -nc --argjson m "${meta:-null}" --argjson n "$n" --arg st "${DEL_STATUS:-}" --arg ms "${DEL_MS:-}" --arg t "$transport" \
    --argjson rc "$rc" --argjson rb "$rbytes" --arg bb "$(wc -c <"$body" 2>/dev/null)" --arg code "$code" \
    '($m // {}) + {schema:"fold/qc1-delete-attempt/v1", attempt:$n, status:(if $st | test("^[0-9]{3}$") then $st else "unknown" end), ms:($ms | tonumber? // null),
      transport:$t, requestExit:$rc, responseBytes:$rb, bodyBytes:($bb | tonumber? // null), code:$code}' >"$dst.tmp" && mv -n "$dst.tmp" "$dst"; } \
    || echo "qc1: the record for delete attempt $n was not written" >&2
  return 0
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

# --- the DELETE body the panel sends (set-v1) ------------------------------------------------------------------------------------------
# Built by qc1/set-v1.mjs, an implementation independent of the browser's, held to the shared vectors by `qc1_setv1 selftest`. The body is
# exactly what AdminUsers.remove sends (web/src/api/adminUsers.ts): {successors, hostSuccessors, attemptId?, reasonReview?:{format:"set-v1", count, digest,
# choices}} in that key order, compact like JSON.stringify, with no trailing newline (so the file length is the request's Content-Length). The
# attempt id is the 80-hex id a press mints first; no reasons (n = 0) means no reasonReview. The first n items of the file are used, in the order
# the server issued them, which must be strictly ascending signed id (nothing is sorted: the order is part of what is measured).
QC1_SETV1="${QC1_SETV1:-$(dirname "${BASH_SOURCE[0]}")/set-v1.mjs}"
qc1_setv1() { node "$QC1_SETV1" "$@"; }

# mode: keep, remove or mixed (the first ceil(n/2) removed). successors and hostSuccessors are JSON objects (default {}).
qc1_delete_body_n() { # mode items-file n [attempt-id] [successors] [hostSuccessors]
  local -a a=(body --items "$2" --mode "$1" --n "$3")
  [[ -z "${4:-}" ]] || a+=(--attempt "$4")
  [[ -z "${5:-}" ]] || a+=(--successors "$5")
  [[ -z "${6:-}" ]] || a+=(--host "$6")
  qc1_setv1 "${a[@]}"
}

# The same body with only the last reason's choice replaced: keep (code 1), remove (2) or anything else (code 3, which is no choice: the
# real server answers 400 invalid reason review). The digest and the count stay valid, so only the choice vector is wrong.
qc1_final_choice_body() { # body-file value
  local code=3
  case "$2" in keep) code=1 ;; remove) code=2 ;; esac
  qc1_setv1 mutate-last --body "$1" --code "$code"
}
# A body whose claimed set is not the server's (one digest digit changed): the real server answers 409 reason_review_changed.
qc1_changed_set_body() { qc1_setv1 mutate-digest --body "$1"; } # body-file
# A body claiming one reason too many (the digest and the vector cover fewer): 400, the vector length is checked first.
qc1_extra_count_body() { qc1_setv1 mutate-count --body "$1"; } # body-file

# The most reasons whose body, in the full envelope above, stays within cap bytes, computed exactly from the format (a body's length depends only
# on the count's digits and the vector's length). Prints "n bytes_at_n bytes_at_n_plus_1". A body of that size is NOT sent for an account of that
# size: it would take that many reasons. What is sent is every real body the matrix reaches, an over-cap body and the refusals.
qc1_body_capacity() { # cap attempt-id [successors] [hostSuccessors]
  local -a a=(capacity --cap "$1")
  [[ -z "${2:-}" ]] || a+=(--attempt "$2")
  [[ -z "${3:-}" ]] || a+=(--successors "$3")
  [[ -z "${4:-}" ]] || a+=(--host "$4")
  qc1_setv1 "${a[@]}"
}
# The exact length of a body for n reasons.
qc1_body_length() { # n attempt-id [successors] [hostSuccessors]
  local -a a=(length --n "$1")
  [[ -z "${2:-}" ]] || a+=(--attempt "$2")
  [[ -z "${3:-}" ]] || a+=(--successors "$3")
  [[ -z "${4:-}" ]] || a+=(--host "$4")
  qc1_setv1 "${a[@]}"
}
# A syntactically valid body one reason over the cap (synthetic digest): the server must refuse it before it reads anything.
qc1_oversize_body() { # cap attempt-id
  qc1_setv1 oversize --cap "$1" --attempt "$2"
}
# The OLD items envelope, for the comparison with the pilots only. digits: the width of the ids (the pilots' 52,760 / 52,761 depended on it).
qc1_legacy_capacity() { qc1_setv1 legacy-capacity --cap "$1" --digits "$2" --attempt "$3"; } # cap digits attempt-id
qc1_legacy_body() { qc1_setv1 legacy-body --entries "$1" --digits "$2" --attempt "$3"; }      # entries digits attempt-id

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
qc1_judge_defs() { qc1_audit_identity_defs; cat <<'JQ'
    def inv($why; $x): {class:"invalid", failure:$why} + $x;
    def bud($why; $x): {class:"budget", failure:$why} + $x;
    def numz: if type == "number" then . else null end;
    def ran($r): [$r.traversal, $r.delete, $r.window, $r.put, $r.probe] | map(select(type == "object"));
    # The reasons a case says its account was loaded with, from its label (null when the label does not say). Items 1 and 2 load none; the plans case loads min(size, 1,000).
    # The smallest a legacy decision can be on the wire: {"requestId":"1","reviewHash":"<64 hex>","choice":"keep"}. A 100-decision body cannot be smaller than 100 of them plus 99 commas.
    def legacyMinEntry: ({requestId: "1", reviewHash: ("0" * 64), choice: "keep"} | tojson | length);
    def lblk($m):
      if ($m.case | type) != "string" then null
      elif $m.item == 1 or $m.item == 2 then 0
      elif $m.item == 3 and $m.case == "order-control-requester-10" then 10
      elif $m.item == 3 and ($m.case | startswith("discovery-")) then ($m.case | split("-") | .[2] | tonumber? // null)
      elif $m.item == 6 and ($m.case == "order-control-delete-start" or $m.case == "order-control-delete-end" or $m.case == "order-control-delete-start-explained" or $m.case == "order-control-delete-end-explained") then 0
      elif $m.item == 6 and ($m.case | startswith("delete-")) then ($m.case | split("-") | .[2] | tonumber? // null)
      elif $m.item == 10 and ($m.case | startswith("browser-panel-")) then ($m.case | ltrimstr("browser-panel-") | split("-") | .[0] | tonumber? // null)
      elif $m.item == 9 then ($m.reasons | numz)
      elif $m.item == 5 and $m.case == "request-size" then ($m.request.largest.reasons | numz)
      elif $m.item == 8 and $m.case == "plans" then (if ($m.size | type) == "number" then ([$m.size, 1000] | min) else null end)
      else null end;
    # The scanner probe (the REAL statements, one session, eight executions per plan cache mode, at the prescribed cursor positions). The collector promises exactly this
    # shape and the judge holds it to it: both modes, each with exactly one initial unit and seven continuations at 0, 10, 25, 40, 55, 70 and 85 per cent of the table, at
    # strictly increasing cursor ids, the same ids in both modes, every execution with its child scan's rows, loops and buffers. Anything else is invalid, never partial credit.
    # $variant says which table the probe ran against ("bloated": the table as the matrix left it; "clean": a VACUUM (ANALYZE)d copy).
    def probe_structure1($p; $variant):
      (
          ( [0, 10, 25, 40, 55, 70, 85] ) as $pcts
          | ( if ($p | type) != "object" or ($p.executions | type) != "array" or $p.sameSession != true or $p.executionsPerMode != 8 then
                inv("scanner-probe-missing"; {why:"the scanner probe is absent, not a single session, or does not promise eight executions per mode"})
              else
                ( [ $p.executions[] | .mode ] | unique ) as $modes
                | ( [ $p.executions[] | select((.scan | type) != "object" or (.scan.rows | type) != "number" or (.scan.loops | type) != "number" or (.scan.sharedHit | type) != "number" or (.scan.sharedRead | type) != "number" or (.hasSort | type) != "boolean" or (.topRows | type) != "number") ] | length) as $bare
                | if $modes != ["auto", "force_generic_plan"] or ($p.executions | length) != 16 then inv("scanner-probe-missing"; {why:"exactly the two plan cache modes auto and force_generic_plan, eight executions each (sixteen)", modes:$modes, executions:($p.executions | length)})
                  elif $bare > 0 then inv("scanner-probe-missing"; {why:"every execution carries its child scan rows, loops and shared blocks, its top node rows and whether a Sort is in the plan", executionsMissingData:$bare})
                  elif ((if $variant == "clean" then "qc1_clean" else "public" end) as $want | $p.schema != $want or ([ $p.executions[] | select(.scan.schema != $want) ] | length) > 0) then inv("scanner-probe-table"; {why:"every execution of the probe read the schema it is labelled with (public for the table as the matrix left it, qc1_clean for the vacuumed copy), so one cannot be taken for the other", schema:$p.schema})
                  else
                    ( [ $modes[] as $mode | [ $p.executions[] | select(.mode == $mode) ] as $ex
                        | { mode: $mode,
                            mix: ([ $ex[] | .kind ] == ["initial","continuation","continuation","continuation","continuation","continuation","continuation","continuation"]),
                            percents: ([ $ex[] | select(.kind == "continuation") | .percent ] == $pcts),
                            ids: [ $ex[] | select(.kind == "continuation") | .afterId ],
                            initialId: ($ex[0].afterId) } ] ) as $per
                    | if ($per | any(.mix | not)) then inv("scanner-probe-missing"; {why:"each mode is one initial unit followed by seven continuations, in that order", per:$per})
                      elif ($per | any(.percents | not)) then inv("scanner-probe-missing"; {why:"the continuations are at the prescribed cursor positions 0, 10, 25, 40, 55, 70 and 85 per cent", per:$per})
                      elif ($per | any(.initialId != null)) then inv("scanner-probe-missing"; {why:"the initial unit has no cursor"})
                      elif ($per | any([.ids[] | numbers] | length != 7)) or ($per | any(.ids != (.ids | sort) or (.ids | unique | length) != 7)) then inv("scanner-probe-positions"; {why:"the seven cursor ids of a mode are distinct and strictly increasing (a table that cannot give seven distinct positions is not measured)", per:$per})
                      elif $per[0].ids != $per[1].ids then inv("scanner-probe-positions"; {why:"both modes are probed at the same cursor ids", per:$per})
                      else empty end
                  end
              end ),
          # The planner ESTIMATES (startup and total cost, planned rows) of the top node and of the child scan, and the settings the planner and the JIT decision read, are part of the promise
          # for BOTH probes: absent or non-numeric is invalid (the probe was not captured as designed), never silently empty. They are estimates, recorded beside what happened and never judged as time.
          ( if ($p | type) == "object" and ($p.executions | type) == "array" then
              ( [ $p.executions[] | select((.top | type) != "object" or (.top.totalCost | type) != "number" or (.top.startupCost | type) != "number" or (.top.planRows | type) != "number"
                                           or (.scan | type) != "object" or (.scan.totalCost | type) != "number" or (.scan.startupCost | type) != "number" or (.scan.planRows | type) != "number"
                                           or ((.scan.bitmapIndex | type) == "object" and ((.scan.bitmapIndex.totalCost | type) != "number" or (.scan.bitmapIndex.planRows | type) != "number"))) ] | length ) as $noest
              | if $p.estimates != true or $noest > 0 or ($p.plannerSettings | type) != "object" or ([ "jit", "jit_above_cost", "random_page_cost", "seq_page_cost", "effective_cache_size", "work_mem" ] | any(. as $k | ($p.plannerSettings[$k] | type) != "string"))
                then inv("scanner-probe-estimates-missing"; {why:"every execution of the probe carries the planner estimates (startup and total cost, planned rows) of its top node and of its child scan, and the probe names the planner settings", executionsMissingEstimates:$noest, variant:$variant}) else empty end
            else empty end ),
          # The two modes are compared position by position: the same data must come back (equal rows at every position, initial unit included). A difference is the state having
          # changed between the modes, or a plan that is not the same query; neither can be read as proof of the index path.
          ( if ($p.executions | type) == "array" and ([ $p.executions[] | .mode ] | unique) == ["auto", "force_generic_plan"] then
              ( [ $p.executions[] | select(.mode == "auto") ] ) as $a
              | ( [ $p.executions[] | select(.mode == "force_generic_plan") ] ) as $g
              | if ($a | length) == ($g | length) and ([ range(0; $a | length) | select(($a[.].scan.rows) != ($g[.].scan.rows) or ($a[.].kind) != ($g[.].kind) or ($a[.].afterId) != ($g[.].afterId)) ] | length) > 0
                then inv("scanner-mode-disagreement"; {why:"the same cursor must read the same rows in both plan cache modes"}) else empty end
            else empty end )
      ) | . + {variant: $variant};
    # The production scanner reads a unit in batches (internal/reasonscan: a first LIMIT, doubling, clipped to the 50,000-row unit), so the probe runs the real statements at the LIMITs
    # production sends: the first batch (the statement almost every page is) and the largest batch of the schedule. It states the LIMITs it ran, every execution carries its own, and
    # each LIMIT is held to the same eight-per-mode shape. A probe that does not state its LIMITs is invalid, not assumed to be one 50,000-row statement.
    def probe_structure($p; $variant):
      if ($p | type) == "object" and ($p.limits | type) == "array" and ($p.limits | length) >= 1 and ($p.limits | all(type == "number" and . >= 1))
         and (($p.limits | unique | length) == ($p.limits | length)) and ($p.executions | type) == "array"
         and (([ $p.executions[] | .limit ] | unique) == ($p.limits | sort))
      then ( $p.limits[] as $L | probe_structure1($p | .executions |= map(select(.limit == $L)); $variant) | . + {limit: $L} )
      else inv("scanner-probe-missing"; {why:"the scanner probe states the LIMITs it ran (the production batch schedule: its first and its largest batch) and every execution carries its LIMIT", variant:$variant})
      end;
    # What the design (t07b-scaling-design3 R3) requires of the scanner statements, quoted in every finding so each miss is reviewable on its own.
    def r3: "R3 (t07b-scaling-design3): the initial unit is a Limit over the primary-key index with no Sort; the continuation unit has the id predicate as an Index Cond";
    # The plan an execution actually had, in words: the child scan, the index under a bitmap scan, and whether a Sort sits above it.
    def shape($e):
      ($e.scan) as $s
      | (if $s.nodeType == "Bitmap Heap Scan" then "Bitmap Heap Scan over Bitmap Index Scan on " + ($s.bitmapIndex.indexName // "an unreported index") + " (Index Cond " + ($s.bitmapIndex.indexCond // "unreported") + ")"
         elif $s.nodeType == "Seq Scan" then "Seq Scan" + (if ($s.filter | type) == "string" then " with Filter " + $s.filter else "" end)
         else ($s.nodeType // "unknown scan") + (if ($s.indexName | type) == "string" then " using " + $s.indexName else "" end) + (if ($s.indexCond | type) == "string" then " (Index Cond " + $s.indexCond + ")" else "" end) end)
        + (if $e.hasSort then " + Sort" else " (no Sort)" end);
    # Per-execution findings of one probe. Every finding keeps the plan in words, the requirement, the table it ran against, the size of the table against the unit, what it cost, and
    # says it is OPEN: a miss is retained as a budget finding at every table size for a reviewed disposition, whatever it cost; nothing is demoted for being cheap or for the table
    # being smaller than the 50,000-row unit (belowUnit is recorded as a fact beside the finding, not as a reason to drop it).
    def probe_findings($m; $p; $variant):
      if ($p.executions | type) == "array" then
        ( $p.executions[] | select((.scan | type) == "object") as $e
          | { mode:$e.mode, kind:$e.kind, percent:$e.percent, afterId:$e.afterId, limit:($e.limit // null), variant:$variant, plan:shape($e), requirement:r3,
              tableRows:($m.table.actualRows // null), unitRows:$unitrows, belowUnit:((($m.table.actualRows // 0) <= $unitrows)),
              measured:{ rows:$e.scan.rows, loops:$e.scan.loops, sharedBlocks:(($e.scan.sharedHit // 0) + ($e.scan.sharedRead // 0)) },
              estimated:{ scanTotalCost:$e.scan.totalCost, scanRows:$e.scan.planRows, topTotalCost:$e.top.totalCost, topRows:$e.top.planRows, note:"the planner estimates for this execution (cost units and planned rows), beside what it measured: estimates, not measurements" },
              disposition:"open: measured miss against R3, retained as a budget finding at every table size; needs a reviewed engineering disposition" } as $ctx
          | ( if $e.rows > $unitrows or $e.scan.rows > $unitrows then inv("scanner-unit-overrun"; {mode:$e.mode, afterId:$e.afterId, rows:$e.scan.rows, variant:$variant}) else empty end ),
            ( if ($e.limit | type) == "number" and ((($e.topRows | numbers) // 0) > $e.limit or $e.scan.rows > $e.limit) then inv("scanner-limit-overrun"; {mode:$e.mode, afterId:$e.afterId, limit:$e.limit, rows:$e.scan.rows, variant:$variant}) else empty end ),
            ( if $e.hasSort then bud("scanner-sort"; $ctx + {why:"a Sort in a scanner unit means the rows are not read in primary-key order from the index: the whole remainder is read and sorted before the first row is returned"}) else empty end ),
            ( if (($e.scan.nodeType | type) != "string") or (($e.scan.nodeType | startswith("Index")) | not) then
                bud("scanner-not-indexed"; $ctx + {nodeType:$e.scan.nodeType,
                  why:(if $e.scan.nodeType == "Bitmap Heap Scan" then "the id predicate IS an Index Cond (on the Bitmap Index Scan, not on the parent), but the rows are fetched through a bitmap heap scan and need a Sort: not an ordered Index Scan on the primary key"
                       elif $e.scan.nodeType == "Seq Scan" then "a sequential scan of contact_requests: the whole heap is read, not the primary-key index"
                       else "not an Index Scan on the primary key" end)}) else empty end ),
            ( if ($e.scan.filter | type) == "string" and ($e.scan.filter | test("(^|[^a-z_])id[ )]")) then bud("scanner-id-filter"; $ctx + {filter:$e.scan.filter, why:"the id predicate is a Filter over a scan, not an Index Cond"}) else empty end ),
            ( if $e.kind == "continuation" and ($e.scan.idIndexCond | type) != "string" then bud("scanner-continuation-cond"; $ctx + {why:"no Index Cond on id anywhere in the plan (neither on the scan nor on a Bitmap Index Scan beneath it)"}) else empty end ),
            ( if $e.scan.loops != 1 then bud("scanner-loops"; $ctx + {loops:$e.scan.loops}) else empty end ) )
      else empty end;
    # Buffer growth across cursor positions (an engineering proposal, like the interaction budgets): a bounded unit should touch a similar number of blocks wherever the cursor is, so a
    # continuation that touches more than twice what the first position did is a finding. The figures are recorded either way.
    def probe_growth1($p; $variant):
      if ($p.executions | type) == "array" then
        ( [ "auto", "force_generic_plan" ][] as $mode
          | [ $p.executions[] | select(.mode == $mode and .kind == "continuation" and (.scan | type) == "object") | (.scan.sharedHit + .scan.sharedRead) ] as $b
          | if ($b | length) > 1 and $b[0] > 0 and (($b | max) > 2 * $b[0]) then bud("scanner-buffer-growth"; {mode:$mode, variant:$variant, firstPosition:$b[0], max:($b | max), blocks:$b, proposal:"more than twice the first position"}) else empty end )
      else empty end;
    # Buffer growth is judged within one LIMIT (a 17,488-row batch always touches more blocks than a 256-row one).
    def probe_growth($p; $variant):
      if ($p | type) == "object" and ($p.limits | type) == "array" and ($p.executions | type) == "array"
      then ( $p.limits[] as $L | probe_growth1($p | .executions |= map(select(.limit == $L)); $variant) | . + {limit: $L} )
      else empty end;
    # The EXPLAINED deletion of an account with no reasons (item 6, order-control-delete-<start|end>-explained): a separate extra deletion that ran with auto_explain on for that one
    # operation only. It holds the report statement the application runs once per erasure, with its plan (actual rows, timing and buffers) read from the server log, and the proof that the
    # statement is the application own and no other. A missing plan, an unproven identity, two candidate statements, an incomplete plan or unlabelled caveats are each INVALID, never partial credit.
    def explained_case($m): ($m.case | type) == "string" and ($m.case | endswith("-explained"));
    def audit_retention($m):
      ($m.auditRetention) as $a | ($m.explained) as $x
      | def num_all($o; $keys): $o | type == "object" and ($keys | all(. as $k | $o[$k] | isnum));
        # A JIT section is either truly absent (null, and no stray JIT lines in the plan text) or complete: every field the collector promises, numeric, and a total that is the sum of its parts.
        def jit_ok($p):
          ($p | has("jit")) and ($p.jitStray == false)
          and ( $p.jit == null
                or ( num_all($p.jit; ["functions", "generationMs", "inliningMs", "optimizationMs", "emissionMs", "totalMs"]) and ($p.jit.functions >= 1)
                     and ($p.jit.options | type) == "string" and ($p.jit.options | test("^Inlining (true|false), Optimization (true|false), Expressions (true|false), Deforming (true|false)$"))
                     and ([ $p.jit.generationMs, $p.jit.inliningMs, $p.jit.optimizationMs, $p.jit.emissionMs ] | all(. >= 0))
                     and ((($p.jit.generationMs + $p.jit.inliningMs + $p.jit.optimizationMs + $p.jit.emissionMs) - $p.jit.totalMs) | fabs) <= 0.05 ) );
        ( if ($x | type) != "object" or $x.status != "200" or ($x.ms | type) != "number" or $x.reasonsKept != 0 or $x.reasonsRemoved != 0
               or ($m.outcome | type) != "object" or $m.outcome.dbOk != true or $m.outcome.accountDeleted != "true" or $m.outcome.reasonsBlank != 0 or $m.outcome.reasonsWithText != 0 or $m.outcome.decisionAudits != 0
               or $m.expect.items != 0 or $m.expect.kept != 0 or $m.expect.removed != 0
               or ($x.tableRowsBefore | type) != "number" or $x.tableRowsBefore != $m.size
               or ($x.targetUserId | isnum | not)
          then inv("explained-delete-unverified"; {why:"the explained deletion answered 200, kept and removed nothing, left the database as the zero-reason deletion implies, ran against the declared table and names the account it deleted"}) else empty end ),
        ( if ($a | type) != "object" or ($a.statementBlocks | type) != "number" or $a.statementBlocks == 0 then
            inv("audit-plan-missing"; {why:"no plan of the audit-retention report statement was found in the server log for the explained deletion: the capture failed or the statement was not logged"})
          elif $a.statementBlocks != 1 then
            inv("audit-statement-ambiguous"; {blocks:$a.statementBlocks, why:"exactly one logged plan of the report statement is expected for one deletion"})
          else
            ( $a.identity ) as $i | ( $a.plan.nodes ) as $n | ( $a.plan ) as $p
            | ( [ if ($n | type) == "array" then $n[] | select(.relation == "audit_logs") else empty end ] ) as $sc
            | ( [ $sc[] | select(.neverExecuted != true) ] ) as $ran
            | ( if ($i | type) != "object" or $i.proven != true or (audit_identity_ok($i; $x.targetUserId) | not)
                then inv("audit-statement-unproven"; {why:"the logged statement must be the Go source text (fixed pieces, subject-mention and malformed-link constants byte for byte, a classification of the source shape), take the complete parameter list with the deleted account at every account position and id arrays at the array positions, sit in the backend of the erasure own audit UPDATEs before it, and be followed on that backend, as its first logged statement, by the application own conversion discovery (the Go source text, the deleted account as its only parameter), the only one in the log", identity:$i}) else empty end ),
              ( if ($n | type) != "array" or ($n | length) == 0 or (num_all($n[0]; ["startupCost", "totalCost", "planRows", "actualRows", "actualTotalMs"]) | not)
                   or ($sc | length) == 0 or ($ran | length) == 0
                   or ([ $sc[] | select(num_all(.; ["startupCost", "totalCost", "planRows"]) | not) ] | length) > 0
                   or ([ $ran[] | select(num_all(.; ["actualRows", "loops", "actualTotalMs", "sharedHit", "sharedRead"]) | not) ] | length) > 0
                   or ($a.durationMs | type) != "number"
                then inv("audit-plan-incomplete"; {why:"the plan must give the planner estimates (startup and total cost, rows) and the actual time and rows of its top node, and for every audit_logs scan its estimates and, for every one that ran, its actual rows, loops, time and shared buffers; plus the statement duration"}) else empty end ),
              ( if ($p | type) != "object" or (jit_ok($p) | not)
                then inv("audit-jit-malformed"; {why:"a JIT section is truly absent (no JIT lines in the plan) or complete: functions, options, generation, inlining, optimization, emission and total, numeric, the total being the sum of the parts; a section that is partial, non-numeric or has stray JIT lines is malformed", jit:($p.jit // null), jitStray:($p.jitStray // null)}) else empty end ),
              ( if $a.rowLocks != false or ($a.lockWaits | type) != "object" or $a.lockWaits.logged != true or ($a.lockWaits.count | type) != "number"
                   or ($m.autoExplain | type) != "object" or ($m.autoExplain.caveats | type) != "array" or ($m.autoExplain.caveats | length) < 4 or ($m.autoExplain.scope | type) != "string"
                then inv("audit-caveats-missing"; {why:"the row must say the statement takes no row locks, that log_lock_waits was on with the count it logged, and carry the scan, lock and cluster caveats"}) else empty end )
          end );
    def norm_row:
      (reduce ("traversal","delete","request","expect","outcome","assessment","browser","plans","generic","window","put","executions","failed","scannerProbe","scannerProbeClean","tables","loggedShapes","probe","legacy","explained","auditRetention","autoExplain","batchSchedule","textShape","agreement") as $k (.; if (.[$k] | type) == "object" then . else .[$k] = null end)
       | (.agreement |= (if type == "object" then reduce ("digest","domText") as $b (.; if (.[$b] | type) == "object" then . else .[$b] = null end) else . end))
       | (if .traversal != null then .traversal |= (.totalMs |= numz | .slowestUnitMs |= numz | .requestMs |= numz | .clientMs |= numz | .units |= numz | .items |= numz) else . end)
       | (if .delete != null then .delete |= (.ms |= numz | .reasonsKept |= numz | .reasonsRemoved |= numz) else . end)
       | (.loggedShapes |= (if type == "object" then reduce ("initial","continuation") as $b (.; if (.[$b] | type) == "object" then . else .[$b] = null end) else . end))
       | (.tables |= (if type == "object" then reduce ("bloated","clean") as $b (.; if (.[$b] | type) == "object" then . else .[$b] = null end) else . end))
       | (.browser |= (if type == "object" then reduce ("pages","digest","phases","press","reload","clicks","navigation","firstPage","gate") as $b (.; if (.[$b] | type) == "object" then . else .[$b] = null end) else . end))
       | (if .put != null then .put |= (reduce ("workload","effects","countRefusal") as $b (.; if (.[$b] | type) == "object" then . else .[$b] = null end) | .effects |= (if type == "object" then reduce ("markers","audits") as $c (.; if (.[$c] | type) == "object" then . else .[$c] = null end) | (.markers |= (if type == "object" then (if (.byAction | type) == "object" then . else .byAction = {} end) else . end)) else . end)) else . end)
       | (if .probe != null then .probe |= (reduce ("workload","marker") as $b (.; if (.[$b] | type) == "object" then . else .[$b] = null end)) else . end)
       | (if .put != null then .put |= (.ms |= numz) else . end) | (if .probe != null then .probe |= (.ms |= numz) else . end) | (if .window != null then .window |= (.ms |= numz) else . end)
       | (.browser.phases |= (if type == "object" then reduce ("walk","interactions","press") as $b (.; if (.[$b] | type) == "object" then . else .[$b] = null end) else . end)));
    # The interaction budgets are engineering proposals (design R2); the 120 s traversal line is the existing one. Shared by whole rows and by the partial rows a failed browser run leaves.
    def browser_budgets($m):
      ( if ($m.browser.renderReadyMs | type) == "number" and $m.browser.renderReadyMs > $trav then bud("browser-traversal"; {ms:$m.browser.renderReadyMs, walkEndMs:$m.browser.walkEndMs, firstInteractiveMs:$m.browser.firstPage.firstInteractiveMs, budget:$trav}) else empty end ),
      ( if ($m.browser.walkEndMs | type) == "number" and $m.browser.walkEndMs > $trav then bud("browser-walk"; {ms:$m.browser.walkEndMs, budget:$trav}) else empty end ),
      # The 10 s per-page / per-unit line applies to every review page request the browser made, whole rows and the partial rows a failed run leaves.
      ( if ($m.browser.pages.requestMsMax | type) == "number" and $m.browser.pages.requestMsMax > $unit then bud("page-request"; {ms:$m.browser.pages.requestMsMax, budget:$unit}) else empty end ),
      # A review page response still unfinished when the run ended, which had already been in flight longer than the 10 s line, is the same finding: it is not "no data".
      ( if ($m.browser.pages.pendingAgeMsMax | type) == "number" and $m.browser.pages.pendingAgeMsMax > $unit then bud("page-request-unfinished"; {ageMs:$m.browser.pages.pendingAgeMsMax, budget:$unit, incomplete:$m.browser.pages.incomplete}) else empty end ),
      ( if ($m.browser.clicks.p95Ms | type) == "number" and $m.browser.clicks.p95Ms > $clickp95 then bud("click-p95"; {ms:$m.browser.clicks.p95Ms, budget:$clickp95}) else empty end ),
      ( if ($m.browser.clicks.maxMs | type) == "number" and $m.browser.clicks.maxMs > $clickmax then bud("click-max"; {ms:$m.browser.clicks.maxMs, budget:$clickmax}) else empty end ),
      ( ($m.browser.navigation // {}) | to_entries[] | select((.value | type) == "object" and (.value.maxMs | type) == "number" and .value.maxMs > $nav) | bud("navigation"; {control:.key, ms:.value.maxMs, budget:$nav}) );
JQ
}
qc1_judge_rules() { cat <<'JQ'
    select(.status == "measured" or .status == "observation") | norm_row as $m
    | ( if ([$m.item] | inside([1,2,3,4,5])) and ($m.item != 5 or $m.case == "request-size") and ($m.case != "legacy-discovery-probe") and (($m.traversal | type) != "object" or ($m.traversal.totalMs | type) != "number" or ($m.traversal.slowestUnitMs | type) != "number") then inv("missing-traversal"; {item:$m.item}) else empty end ),
      # Legacy rows (T07B-3a's routes are not in this tree). They stay PENDING until a row proves a real measurement: where the pairs came from and how many were seeded, the table
      # the operation ran against (the declared background size plus exactly the pairs seeded), the full instrument set of the operation, and, for the PUT and the marker lookup, the
      # database effects the route is approved to have. Approved rev8: the legacy PUT takes 1 to 100 decisions (a subset is allowed), applies each as Keep or Blank with a party-qualified
      # audit marker, and a request with more than 100 decisions is refused by count validation. So the PUT row is the ACCEPTED 100-DECISION subset, and nothing about it is a byte-cap
      # crossing: no 413 is required of it, and no route is added to get one. A sample body, a flag or a lone timing proves none of this.
      ( if $m.item == 4 and (($m.case | type) == "string" and ($m.case | startswith("legacy-discovery-"))) and $m.case != "legacy-discovery-probe"
            and (($m.reasons | type) != "number" or ($m.case != ("legacy-discovery-" + ($m.reasons | tostring))) or $m.expect.items != $m.reasons or $m.legacy.loadedPairs != $m.reasons or $m.traversal.items != $m.reasons or $m.traversal.tableRowsBefore != ($m.size + $m.legacy.loadedPairs)
                 or ($m.legacy.route | type) != "string" or ($m.legacy.route | length) == 0 or $m.legacy.source != "T07B-3a")
        then inv("legacy-no-metrics"; {case:$m.case, why:"a legacy discovery row names its route and source, and the pairs loaded, expected and listed all equal the pairs in its case name"}) else empty end ),
      ( if $m.item == 4 and $m.case == "legacy-discovery-probe"
            and (($m.probe | type) != "object" or ($m.probe.ms | type) != "number" or $m.probe.status != "200" or $m.probe.found != true
                 or ($m.probe.route | type) != "string" or ($m.probe.route | length) == 0 or ($m.probe.tableRowsBefore | type) != "number"
                 or ($m.probe.workload | type) != "object" or $m.probe.workload.backgroundRows != $m.size or ($m.probe.workload.loadedPairs | type) != "number" or $m.probe.workload.loadedPairs < 1
                 or $m.probe.tableRowsBefore != ($m.size + $m.probe.workload.loadedPairs)
                 or ($m.probe.workload.markersSeeded | type) != "number" or $m.probe.workload.markersSeeded < 1 or $m.probe.workload.markersSeeded > $m.probe.workload.loadedPairs
                 or ($m.probe.workload.tombstoneId | type) != "number"
                 or ($m.probe.marker | type) != "object" or ($m.probe.marker.requestId | type) != "string" or ($m.probe.marker.requestId | length) == 0
                 or $m.probe.marker.party != $m.probe.workload.tombstoneId or $m.probe.marker.source != "legacy_review"
                 or ([ "add.reason_kept", "add.reason_removed" ] | index($m.probe.marker.action)) == null)
        then inv("legacy-no-metrics"; {case:$m.case, why:"the marker lookup row carries a real lookup over a seeded workload: the declared size plus the pairs seeded is the table it ran against, at least one party-qualified marker was seeded and the one found names the tombstone party, its action and the legacy_review source"}) else empty end ),
      ( if $m.item == 5 and $m.case == "legacy-put"
            and (($m.put | type) != "object" or $m.put.status != "200" or ($m.put.ms | type) != "number" or ($m.put.appPeakKb | type) != "number" or ($m.put.route | type) != "string" or ($m.put.route | length) == 0
                 # the accepted subset is the approved maximum of 100 decisions, all applied, and its body is measured: it holds at least the 100 decisions the format needs, and was accepted within the cap
                 or $m.put.decisions != 100 or $m.put.accepted != 100 or ($m.put.bodyBytes | type) != "number" or $m.put.bodyBytes < (100 * legacyMinEntry + 99) or $m.put.bodyBytes > $cap
                 # the seeded workload is bound to the declared size: the table is the size plus the pairs seeded, and at least the 100 decisions were seeded for this tombstone
                 or ($m.put.workload | type) != "object" or $m.put.workload.backgroundRows != $m.size or ($m.put.workload.loadedPairs | type) != "number" or $m.put.workload.loadedPairs < 100
                 or $m.put.tableRowsBefore != ($m.size + $m.put.workload.loadedPairs) or ($m.put.workload.tombstoneId | type) != "number"
                 # the effects, verified in the database: Keep keeps the text, Blank blanks it, one party-qualified marker and one audit per decision, from the legacy_review source
                 or ($m.put.effects | type) != "object" or $m.put.effects.dbOk != true
                 or ($m.put.effects.kept | type) != "number" or ($m.put.effects.blanked | type) != "number" or $m.put.effects.kept < 1 or $m.put.effects.blanked < 1 or ($m.put.effects.kept + $m.put.effects.blanked) != 100
                 or $m.put.effects.rowsBlank != $m.put.effects.blanked or $m.put.effects.rowsWithText != $m.put.effects.kept
                 or ($m.put.effects.markers | type) != "object" or $m.put.effects.markers.count != 100 or $m.put.effects.markers.party != $m.put.workload.tombstoneId or $m.put.effects.markers.partyQualified != true or $m.put.effects.markers.source != "legacy_review"
                 or $m.put.effects.markers.byAction["add.reason_kept"] != $m.put.effects.kept or $m.put.effects.markers.byAction["add.reason_removed"] != $m.put.effects.blanked
                 or ($m.put.effects.audits | type) != "object" or $m.put.effects.audits.count != 100 or $m.put.effects.audits.source != "legacy_review"
                 # Bind the refusal to the count check, not authentication or the body-size limit.
                 or ($m.put.countRefusal | type) != "object" or $m.put.countRefusal.decisions != 101 or (($m.put.countRefusal.status != "400") and ($m.put.countRefusal.status != "422")) or $m.put.countRefusal.authenticated != true or $m.put.countRefusal.reason != "decision_count" or ($m.put.countRefusal.bodyBytes | type) != "number" or $m.put.countRefusal.bodyBytes > $cap or $m.put.countRefusal.unchanged != true)
        then inv("legacy-no-metrics"; {case:$m.case, cap:$cap, why:"the legacy PUT row is the accepted 100-decision subset: its measured body bytes, the instrument set, a workload bound to the declared size, and the verified database effects (kept and blanked text, party-qualified markers and audits); the 101st decision is refused by count validation"}) else empty end ),
      # The labels are bound to the data: a row proves what its case name, size and mode claim. The reasons it was loaded with, the table it ran against (background size plus
      # those reasons, immediately before each operation), the direction of the reasons it listed, and the outcome the database shows after a deletion must all agree with the label.
      ( lblk($m) as $k
        | if ($m.item == 3 or $m.item == 6 or $m.item == 10) and $k == null then inv("label-mismatch"; {what:"the case name does not name a reason count"})
          elif ($m.item == 3 or $m.item == 6 or $m.item == 10) and ($m.reasons != $k or ($m.item != 10 and $m.expect.items != $k)) then inv("label-mismatch"; {what:"reasons", label:$k, reasons:$m.reasons, expected:$m.expect.items})
          else empty end ),
      ( lblk($m) as $k | ($m.size | numz) as $sz
        | if $k != null and $sz != null then
            ( ran($m)[] | select(type == "object") | . as $o
              | if ($o.tableRowsBefore | type) == "number" and $o.tableRowsBefore != ($sz + $k) then inv("table-rows-mismatch"; {rows:$o.tableRowsBefore, expected:($sz + $k), size:$sz, reasons:$k}) else empty end ),
            ( if $m.item == 8 and $m.table.actualRows != ($sz + $k) then inv("table-rows-mismatch"; {rows:$m.table.actualRows, expected:($sz + $k), size:$sz, reasons:$k}) else empty end ),
            ( if $m.item == 10 then [ $m.browser.phases.walk, $m.browser.phases.interactions, $m.browser.phases.press ][] | select(type == "object" and (.tableRows.before | type) == "number" and .tableRows.before != ($sz + $k)) | inv("table-rows-mismatch"; {rows:.tableRows.before, expected:($sz + $k), size:$sz, reasons:$k, phase:true}) else empty end ),
            ( if $m.item == 5 and $m.request != null then
                [ ["parseRefusal", $m.request.parseRefusal.tableRowsBefore], ["countMismatch", $m.request.countMismatch.tableRowsBefore], ["changedSet", $m.request.changedSet.tableRowsBefore], ["overCap", $m.request.overCap.tableRowsBefore] ][]
                | select((.[1] | type) != "number" or .[1] != ($sz + $k)) | inv("table-rows-mismatch"; {operation:.[0], rows:.[1], expected:($sz + $k), size:$sz, reasons:$k})
              else empty end ),
            ( if $m.item == 5 and $m.request != null then
                [ ["legacyFit", $m.request.legacyComparison.tableRowsBefore.fit], ["legacyOver", $m.request.legacyComparison.tableRowsBefore.over] ][]
                | select((.[1] | type) != "number" or .[1] != ($sz + 10)) | inv("table-rows-mismatch"; {operation:.[0], rows:.[1], expected:($sz + 10), size:$sz})
              else empty end )
          else empty end ),
      # Direction and mode: a discovery or deletion case named requester or subject listed ONLY reasons of that role; a deletion named keep, remove or mixed shows the matching split and the
      # database outcome that split implies (the account deleted, that many reasons blanked, that many kept, one decision audit per reason).
      ( if ($m.item == 3 or $m.item == 6) and (($m.case | type) == "string") and ($m.case | startswith("discovery-") or startswith("delete-")) then
          ( $m.case | split("-") | .[1] ) as $dir | ( if $dir == "requester" then "subject" else "requester" end ) as $other
          | if ($dir != "requester" and $dir != "subject") then inv("label-mismatch"; {what:"direction", label:$dir})
            elif ($m.roles | type) != "object" or $m.roles[$dir] != $m.reasons or $m.roles[$other] != 0 then inv("label-mismatch"; {what:"direction", label:$dir, roles:$m.roles, reasons:$m.reasons})
            else empty end
        else empty end ),
      ( if $m.item == 6 and (($m.case | type) == "string") and ($m.case | startswith("delete-")) and ($m.reasons | type) == "number" then
          ( $m.case | split("-") | .[3] ) as $mode | $m.reasons as $r
          | ( if $mode == "keep" then {kept:$r, removed:0} elif $mode == "remove" then {kept:0, removed:$r} elif $mode == "mixed" then {kept:($r / 2 | floor), removed:($r - ($r / 2 | floor))} else null end ) as $want
          | if $want == null then inv("label-mismatch"; {what:"mode", label:$mode})
            elif $m.expect.kept != $want.kept or $m.expect.removed != $want.removed then inv("label-mismatch"; {what:"mode", label:$mode, expect:$m.expect, wanted:$want})
            else empty end
        else empty end ),
      ( if $m.expect != null and $m.delete != null and $m.item != 5
            and (($m.outcome | type) != "object" or $m.outcome.dbOk != true or $m.outcome.accountDeleted != "true" or $m.outcome.reasonsBlank != ($m.expect.removed // -1) or $m.outcome.reasonsWithText != ($m.expect.kept // -1)
                 or $m.outcome.decisionAudits != (($m.expect.kept // 0) + ($m.expect.removed // 0)))
        then inv("outcome-unverified"; {outcome:$m.outcome, expect:$m.expect}) else empty end ),
      ( if $m.item == 5 and $m.case == "request-size" and $m.request != null then
          if ($m.request.fitOutcome | type) != "object" or $m.request.fitOutcome.dbOk != true or $m.request.fitOutcome.accountDeleted != "true" or $m.request.fitOutcome.reasonsBlank != ($m.request.fitExpect.removed // -1) or $m.request.fitOutcome.reasonsWithText != ($m.request.fitExpect.kept // -1)
             or $m.request.fitOutcome.decisionAudits != (($m.request.fitExpect.kept // 0) + ($m.request.fitExpect.removed // 0)) or (($m.request.fitExpect.kept // 0) + ($m.request.fitExpect.removed // 0)) != $m.request.largest.reasons
          then inv("outcome-unverified"; {outcome:$m.request.fitOutcome, expect:$m.request.fitExpect, largest:$m.request.largest.reasons}) else empty end
        else empty end ),
      ( if ([$m.item] | inside([1,2,5,6,9])) and ($m.item != 5 or $m.case == "request-size") and (explained_case($m) | not) and (($m.delete | type) != "object" or ($m.delete.ms | type) != "number") then inv("missing-delete"; {item:$m.item}) else empty end ),
      ( if $m.item == 6 and explained_case($m) then audit_retention($m) else empty end ),
      ( if $m.item == 5 and $m.case == "request-size" and ($m.request | type) != "object" then inv("missing-request"; {item:$m.item}) else empty end ),
      ( if ($m.traversal.slowestUnitMs // 0) > $unit then bud("unit"; {ms:$m.traversal.slowestUnitMs, budget:$unit}) else empty end ),
      ( if ($m.traversal.totalMs // 0) > $trav then bud("traversal"; {ms:$m.traversal.totalMs, budget:$trav}) else empty end ),
      ( if ($m.delete.ms // 0) > $del then bud("delete"; {ms:$m.delete.ms, budget:$del}) else empty end ),
      ( if $m.item == 5 and $m.case == "request-size" and $m.request != null and (($m.request.maxRequestBytes | type) != "number" or ($m.request.capacity.reasons | type) != "number" or ($m.request.largest.reasons | type) != "number" or ($m.request.largest.bodyBytes | type) != "number") then inv("request-incomplete"; {}) else empty end ),
      ( if $m.item == 5 and $m.case == "request-size" and ($m.request.largest.bodyBytes | type) == "number" and ($m.request.capacity.reasons | type) == "number" and ((($m.request.largest.bodyBytes) > $m.request.maxRequestBytes) or ($m.request.capacity.reasons < $m.request.largest.reasons)) then bud("body"; {reasons:$m.request.largest.reasons, bytes:$m.request.largest.bodyBytes, capacityReasons:$m.request.capacity.reasons, cap:$m.request.maxRequestBytes}) else empty end ),
      ( if $m.traversal != null and ($m.traversal.lastStatus // "") != "200" then inv("status"; {route:"page", status:$m.traversal.lastStatus}) else empty end ),
      ( if $m.delete != null and ($m.delete.status // "") != "200" then inv("status"; {route:"delete", status:$m.delete.status}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.request.overCap.status // "") != "413" then inv("status"; {route:"over-cap", status:$m.request.overCap.status}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.request.parseRefusal.status // "") != "400" then inv("status"; {route:"refused-parse", status:$m.request.parseRefusal.status}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.request.countMismatch.status // "") != "400" then inv("status"; {route:"refused-count", status:$m.request.countMismatch.status}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.request.changedSet.status // "") != "409" then inv("status"; {route:"changed-set", status:$m.request.changedSet.status}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.overCap.unchanged != true then inv("over-cap-changed-data"; {}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.parseRefusal.unchanged != true then inv("refused-parse-changed-data"; {}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.countMismatch.unchanged != true then inv("refused-count-changed-data"; {}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.changedSet.unchanged != true then inv("changed-set-changed-data"; {}) else empty end ),
      ( if $m.item == 5 and $m.request != null and (($m.request.legacyComparison.entriesOver | type) != "number" or ($m.request.legacyComparison.overStatus // "") != "413" or ($m.request.legacyComparison.fitStatus // "") != "409") then inv("legacy-comparison"; {}) else empty end ),
      ( ran($m)[] | . as $o
        | ( [ ["walBytes", $o.walBytes], ["appPeakKb", $o.appPeakKb], ["dbPeakKb", $o.dbPeakKb], ["gateTicks", $o.gateTicks],
              ["commitCalls", $o.commitCalls], ["commitMsMax", $o.commitMsMax], ["commitMsTotal", $o.commitMsTotal], ["dbMemSamples", $o.dbMemSamples],
              ["gateMaxGapMs", $o.gateMaxGapMs], ["auditRowsBefore", $o.auditRowsBefore], ["auditRowsAfter", $o.auditRowsAfter], ["auditBytesBefore", $o.auditBytesBefore], ["auditBytesAfter", $o.auditBytesAfter], ["topStatements", (if ($o.topStatements | type) == "array" then 1 else null end)], ["tableRowsBefore", $o.tableRowsBefore], ["tableRowsAfter", $o.tableRowsAfter], ["auditActionsBefore", $o.auditActionsBefore], ["auditActionsAfter", $o.auditActionsAfter], ["auditActionsDelta", $o.auditActionsDelta] ] | map(select(.[1] == null) | .[0]) ) as $missing
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
      ( if ([1, 2, 3, 4, 6, 9] | index($m.item)) != null and $m.case != "legacy-discovery-probe" and $m.expect == null then inv("no-expectation"; {}) else empty end ),
      ( if $m.expect != null and $m.traversal != null and ($m.traversal.items // -1) != $m.expect.items then inv("discovery-count"; {items:$m.traversal.items, expected:$m.expect.items}) else empty end ),
      ( if $m.expect != null and $m.delete != null and (($m.delete.reasonsKept // -1) != $m.expect.kept or ($m.delete.reasonsRemoved // -1) != $m.expect.removed) then inv("outcome-count"; {kept:$m.delete.reasonsKept, removed:$m.delete.reasonsRemoved, expected:$m.expect}) else empty end ),
      ( if $m.expect != null and $m.delete != null and $m.outcome.dbOk != true then inv("outcome-db"; {outcome:$m.outcome}) else empty end ),
      ( if $m.item == 5 and $m.request != null and ($m.traversal.items // -1) != $m.request.largest.reasons then inv("discovery-count"; {items:$m.traversal.items, expected:$m.request.largest.reasons}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.delete != null and (($m.delete.reasonsKept // -1) != $m.request.fitExpect.kept or ($m.delete.reasonsRemoved // -1) != $m.request.fitExpect.removed) then inv("outcome-count"; {kept:$m.delete.reasonsKept, removed:$m.delete.reasonsRemoved, expected:$m.request.fitExpect}) else empty end ),
      ( if $m.item == 5 and $m.request != null and $m.request.fitOutcome.dbOk != true then inv("outcome-db"; {outcome:$m.request.fitOutcome}) else empty end ),
      ( if $m.item == 7 and ($m.assessment == null or ($m.assessment.perCount | length) == 0) then inv("assessment-missing"; {}) else empty end ),
      ( if $m.item == 8 and (($m.plans | type) != "object" or ([$m.plans["scanner-initial"], $m.plans["page-items"], $m.plans["delete-set"], $m.plans["blank-batch"]] | any(type != "number" or . < 1)) or (($m.plans["scanner-continuation"] | type) != "number")
            or ((($m.table.actualRows // $m.size // 0) > $unitrows) and $m.plans["scanner-continuation"] < 1)) then inv("plans-missing"; {unitRows:$unitrows, actualRows:($m.table.actualRows // null)}) else empty end ),
      # The generic plans are of the SAME statement text the Go source holds (source:"go-source"), not a statement the harness wrote.
      ( if $m.item == 8 and (($m.generic | type) != "object" or $m.generic.source != "go-source" or ($m.generic.scannerContinuation.indexCondOnId | type) != "boolean" or ($m.generic.scannerInitial.idIndexScan | type) != "boolean") then inv("generic-plan-missing"; {why:"the generic plans must come from the statement text in account_erasure_reasons.go"}) else empty end ),
      # Across executions: every scanner execution the run logged has its actual rows and shared buffers (auto_explain.log_analyze and log_buffers), no unit returned more than the unit size, and the figures are recorded for the continuation executions in order.
      ( if $m.item == 8 and (($m.executions | type) != "object" or ($m.executions.scannerInitial.count | type) != "number" or ($m.executions.scannerInitial.rows | type) != "array" or ($m.executions.scannerInitial.sharedBlocks | type) != "array" or ($m.executions.scannerContinuation.count | type) != "number" or ($m.executions.scannerContinuation.rows | type) != "array" or ($m.executions.scannerContinuation.sharedBlocks | type) != "array"
            or ($m.executions.scannerInitial.rows | length) != $m.executions.scannerInitial.count or ($m.executions.scannerInitial.sharedBlocks | length) != $m.executions.scannerInitial.count
            or ($m.executions.scannerContinuation.rows | length) != $m.executions.scannerContinuation.count or ($m.executions.scannerContinuation.sharedBlocks | length) != $m.executions.scannerContinuation.count
            or ([$m.executions.scannerInitial.rows[], $m.executions.scannerInitial.sharedBlocks[], $m.executions.scannerContinuation.rows[], $m.executions.scannerContinuation.sharedBlocks[]] | any(type != "number"))) then inv("plan-executions-missing"; {why:"rows and buffers of every scanner execution must be read from the logged plans"}) else empty end ),
      ( if $m.item == 8 and ([$m.executions.scannerInitial.rows[]?, $m.executions.scannerContinuation.rows[]?] | any(type == "number" and . > $unitrows)) then inv("scanner-unit-overrun"; {unitRows:$unitrows}) else empty end ),
      # The production scanner reads a unit in batches (a first LIMIT, doubling, clipped to the unit). The plan row states the schedule it read from the Go source and the LIMIT each logged execution
      # carried; a LIMIT outside the schedule, or more rows than the LIMIT, means the harness did not measure the scanner production runs. Nothing here requires one 50,000-row statement.
      ( if $m.item == 8 and (($m.batchSchedule | type) != "object" or ($m.batchSchedule.limits | type) != "array" or ($m.batchSchedule.limits | length) < 1 or ($m.batchSchedule.limits | any(type != "number" or . < 1))
            or ($m.batchSchedule.unitRows | type) != "number" or ($m.batchSchedule.limits | add) != $m.batchSchedule.unitRows)
        then inv("batch-schedule-missing"; {why:"the plan row states the production batch schedule read from the Go source: the LIMIT of each statement of a unit read to its end, summing to the unit"}) else empty end ),
      ( if $m.item == 8 and ($m.batchSchedule | type) == "object" and ($m.batchSchedule.limits | type) == "array" then
          ( [ $m.executions.scannerInitial, $m.executions.scannerContinuation ][] | select(type == "object") ) as $x
          | if ($x.limits | type) != "array" or ($x.limits | length) != $x.count or ($x.limits | any(type != "number")) then inv("plan-executions-missing"; {why:"each logged scanner execution carries the LIMIT it was sent"})
            elif ($x.limits | any(. as $l | ($m.batchSchedule.limits | index($l)) == null)) then inv("scanner-limit-unexpected"; {limits:($x.limits | unique), schedule:$m.batchSchedule.limits})
            elif ([ range(0; $x.count) | select(($x.rows[.] | type) == "number" and $x.rows[.] > $x.limits[.]) ] | length) > 0 then inv("scanner-limit-overrun"; {limits:($x.limits | unique), why:"a logged execution returned more rows than its LIMIT"})
            else empty end
        else empty end ),
      # The scanner probes: the table as the matrix left it ("bloated") and a VACUUM (ANALYZE)d copy of the same rows ("clean"), each held to the same exact shape and each with its
      # findings. The bloated probe is the original measurement and is never replaced by the clean one; the clean one says what the same statements do on a table without the churn.
      ( if $m.item == 8 then probe_structure($m.scannerProbe; "bloated"), probe_structure($m.scannerProbeClean; "clean") else empty end ),
      ( if $m.item == 8 then probe_findings($m; $m.scannerProbe; "bloated"), probe_findings($m; $m.scannerProbeClean; "clean"), probe_growth($m.scannerProbe; "bloated"), probe_growth($m.scannerProbeClean; "clean") else empty end ),
      # Both tables are described (pages, tuples, dead tuples, bytes) and the clean copy says it was vacuumed and analyzed: the bloat is a number, not a guess.
      ( if $m.item == 8 and (($m.tables | type) != "object"
            or ([ $m.tables.bloated, $m.tables.clean ] | any(type != "object" or (.relpages | type) != "number" or (.reltuples | type) != "number" or (.heapBytes | type) != "number" or (.indexBytes | type) != "number"))
            or $m.tables.clean.vacuumed != true or $m.tables.clean.analyzed != true or (($m.tables.clean.kind | type) != "string"))
        then inv("clean-table-missing"; {why:"the plan row describes the table as the matrix left it and a vacuumed, analyzed copy of its rows (relpages, reltuples, heap and index bytes)"}) else empty end ),
      ( if $m.item == 8 and $m.generic.scannerInitial.idIndexScan == false then bud("plan-initial"; {why:"the first scanner unit must read the table in id order from the primary key index; the generic plan of the real statement does not"}) else empty end ),
      # The application's OWN logged executions (auto_explain), read as shapes: how many continuation executions had the id predicate as an Index Cond (on an Index Scan, or on the Bitmap
      # Index Scan beneath a Bitmap Heap Scan), which had it only as a Filter on a Seq Scan, how many had a Sort above the scan. Absent when the log held none of that kind.
      ( if $m.item == 8 and ($m.plans["scanner-continuation"] // 0) > 0 and (($m.loggedShapes.continuation | type) != "object" or ($m.loggedShapes.continuation.executions | type) != "number") then inv("logged-shapes-missing"; {why:"the logged continuation executions are read as shapes (qc1_plan_shapes)"}) else empty end ),
      ( if $m.item == 8 and ($m.loggedShapes.continuation | type) == "object" and ($m.loggedShapes.continuation.executions // 0) > 0 and ($m.loggedShapes.continuation.withIdIndexCond != $m.loggedShapes.continuation.executions) then
          bud("plan-continuation"; {requirement:r3, loggedContinuation:$m.loggedShapes.continuation, genericPlanHasIndexCond:$m.generic.scannerContinuation.indexCondOnId,
            why:("of " + ($m.loggedShapes.continuation.executions | tostring) + " logged continuation executions " + ($m.loggedShapes.continuation.withIdIndexCond | tostring) + " had the id predicate as an Index Cond (on an Index Scan, or beneath a Bitmap Heap Scan); the others (executions "
                 + ($m.loggedShapes.continuation.withoutIdIndexCond | map(tostring) | join(", ")) + ") had it only as a Filter on a Seq Scan. " + ($m.loggedShapes.continuation.withSort | tostring) + " had a Sort above the scan."),
            disposition:"open: measured miss against R3, retained as a budget finding at every table size; needs a reviewed engineering disposition"}) else empty end ),
      ( if $m.item == 8 and $m.generic.scannerContinuation.indexCondOnId == false then bud("plan-continuation"; {requirement:r3, genericPlanHasIndexCond:false, why:"the generic plan of the real continuation statement has no Index Cond on id", disposition:"open: measured miss against R3, retained as a budget finding at every table size; needs a reviewed engineering disposition"}) else empty end ),
      ( if $m.item == 8 and ($m.loggedShapes.initial | type) == "object" and ($m.loggedShapes.initial.executions // 0) > 0 and (($m.loggedShapes.initial.withSort // 0) > 0 or ($m.loggedShapes.initial.indexScan // 0) < $m.loggedShapes.initial.executions) then
          bud("plan-initial-logged"; {requirement:r3, loggedInitial:$m.loggedShapes.initial,
            why:("of " + ($m.loggedShapes.initial.executions | tostring) + " logged initial executions " + ($m.loggedShapes.initial.indexScan | tostring) + " were an Index Scan on the primary key and " + ($m.loggedShapes.initial.withSort | tostring) + " had a Sort above the scan"),
            disposition:"open: measured miss against R3, retained as a budget finding at every table size; needs a reviewed engineering disposition"}) else empty end ),
      ( if $m.item == 10 then
          ( [ ["renderReadyMs", $m.browser.renderReadyMs], ["walkEndMs", $m.browser.walkEndMs], ["firstInteractiveMs", $m.browser.firstPage.firstInteractiveMs], ["firstPageItems", $m.browser.firstPage.itemsRendered],
              ["heapAfterGcBytes", $m.browser.heapAfterGcBytes], ["domNodes", $m.browser.domNodes], ["longTaskMs", $m.browser.longTaskMs], ["secureContext", $m.browser.secureContext],
              ["clickP95Ms", $m.browser.clicks.p95Ms], ["clickMaxMs", $m.browser.clicks.maxMs], ["navNextPageMax", $m.browser.navigation.nextPage.maxMs],
              ["navJumpMax", $m.browser.navigation.jump.maxMs], ["navNextUnchosenMax", $m.browser.navigation.nextUnchosen.maxMs], ["navNextChangedMax", $m.browser.navigation.nextChanged.maxMs],
              ["pagesCount", $m.browser.pages.count], ["pageResponseBytesMax", $m.browser.pages.responseBytesMax], ["pageRequestMsMax", $m.browser.pages.requestMsMax], ["pageReadMsMax", $m.browser.pages.readMsMax], ["pagesIncomplete", (if ($m.browser.pages.incomplete | type) == "number" and $m.browser.pages.incomplete == 0 then 0 else null end)], ["pagesAbsentFields", (if ($m.browser.pages.absent | type) == "object" and ([$m.browser.pages.absent[]] | add) == 0 then 0 else null end)],
              ["pagesWalks", (if ($m.browser.pages.walks | type) == "array" and ($m.browser.pages.walks | length) >= 2 then ($m.browser.pages.walks | length) else null end)], ["pageParseMsMax", $m.browser.pages.parseMsMax], ["pageParseMsTotal", $m.browser.pages.parseMsTotal],
              ["digestPath", $m.browser.digest.path], ["digestBuildMs", $m.browser.digest.buildMs], ["digestPreimageBytes", $m.browser.digest.preimageBytes], ["pressResponseMs", $m.browser.press.responseMs],
              ["phaseWalkWalBytes", $m.browser.phases.walk.walBytes], ["phaseInteractionsWalBytes", $m.browser.phases.interactions.walBytes], ["phasePressWalBytes", $m.browser.phases.press.walBytes],
              ["phaseWalkAuditActions", $m.browser.phases.walk.auditActionsDelta], ["phasePressAuditActions", $m.browser.phases.press.auditActionsDelta],
              ["phaseTableRowsWalk", $m.browser.phases.walk.tableRows.before], ["phaseTableRowsInteractions", $m.browser.phases.interactions.tableRows.before], ["phaseTableRowsPress", $m.browser.phases.press.tableRows.before], ["phaseAuditBytesWalk", $m.browser.phases.walk.auditBytes], ["phaseAuditBytesPress", $m.browser.phases.press.auditBytes],
              ["windowWalBytes", $m.window.walBytes], ["windowGateTicks", $m.window.gateTicks], ["windowCommitCalls", $m.window.commitCalls] ]
            | map(select(.[1] == null) | .[0]) ) as $gone
          | ([$m.reasons, 50] | min) as $first
          | if ($gone | length) > 0 then inv("browser-missing"; {metrics:$gone})
            elif $m.browser.walkTotal != $m.reasons then inv("browser-count"; {total:$m.browser.walkTotal, expected:$m.reasons})
            elif $m.browser.firstPage.itemsRendered != $first then inv("browser-first-page"; {rendered:$m.browser.firstPage.itemsRendered, expected:$first})
            elif ($m.browser.clicks.count // 0) != 30 then inv("browser-clicks"; {clicks:$m.browser.clicks.count, wanted:30})
            elif ($m.browser.pageRequests | type) != "number" or $m.browser.pageRequests < (($m.reasons + 99) / 100 | floor) then inv("browser-walk-short"; {requests:$m.browser.pageRequests, reasons:$m.reasons})
            elif ($m.browser.pageResponseBytes | type) != "number" or $m.browser.pageResponseBytes < ($m.reasons * 100) then inv("browser-bytes"; {bytes:$m.browser.pageResponseBytes, reasons:$m.reasons})
            elif $m.browser.apiOrigin != "real" then inv("browser-not-real-api"; {})
            elif $m.browser.press.observed != true or $m.browser.press.status != "409" or ($m.browser.press.bodyBytes | type) != "number" then inv("browser-press"; {press:$m.browser.press})
            elif ($m.browser.reload.unchosenAfterReload) != 1 then inv("browser-reload"; {reload:$m.browser.reload})
            # The path that ran must be one the page could have taken: webcrypto needs crypto.subtle (a secure context that was not stripped). A fallback in a secure context
            # is LEGITIMATE (a preimage above 16 MiB, or the product's known-answer self-test failing), so it is recorded against the expected path, never judged as a fault.
            elif ($m.browser.digest.path != "webcrypto" and $m.browser.digest.path != "fallback") or ($m.browser.noSubtle == true and $m.browser.digest.path != "fallback") or ($m.browser.secureContext != true and $m.browser.digest.path == "webcrypto") then inv("browser-digest-path"; {digest:$m.browser.digest, noSubtle:$m.browser.noSubtle, secureContext:$m.browser.secureContext})
            # Every response of the first read AND of the reload is accounted for: each walk started at least a whole walk of pages, and every one of them completed.
            elif ($m.browser.pages.walks | length) < 2 or ([ $m.browser.pages.walks[] | select(.walk == 1 or .walk == 2) | select((.started // 0) < (($m.reasons + 99) / 100 | floor)) ] | length) > 0 then inv("browser-pages-short"; {pages:$m.browser.pages.count, walks:$m.browser.pages.walks, reasons:$m.reasons})
            elif ($m.browser.pages.incomplete // 1) != 0 or ([ $m.browser.pages.walks[] | select((.incomplete // 1) != 0 or (.completed // -1) != (.started // -2)) ] | length) > 0 then inv("browser-pages-incomplete"; {incomplete:$m.browser.pages.incomplete, walks:$m.browser.pages.walks})
            else empty end
        else empty end ),
      # The text of the reasons (#293). A row says which charset its reasons are in; the shape of the account's reasons as loaded must be what that charset promises (every target
      # reason exactly targetReasonLen runes; one byte a rune for ASCII; for multibyte more bytes than runes, at most four a rune, and in a sample the 2-, 3- and 4-byte widths
      # and combining marks all present), the deletion must have left every kept reason byte for byte as it was and every removed one blank, the page responses must be as large
      # as multibyte text makes them, and the browser must have sent the digest and choices the independent builder makes and shown the text the database holds.
      ( ($m.charset // "ascii") as $cs
        | lblk($m) as $k
        | if ($m.item == 3 or $m.item == 6 or $m.item == 10) and $k != null and $k > 0 and ($m.targetReasonLen | numz) != null then
            ($m.textShape) as $t
            | if $t == null then (if $cs == "multibyte" then inv("text-shape-missing"; {charset:$cs, reasons:$k}) else empty end)
              elif $t.rows != $k or $t.runesMin != $m.targetReasonLen or $t.runesMax != $m.targetReasonLen then inv("text-shape"; {what:"every target reason is exactly targetReasonLen runes", shape:$t, reasons:$k, targetReasonLen:$m.targetReasonLen})
              elif $cs == "ascii" and $t.bytesMax != $t.runesMax then inv("text-shape"; {what:"ASCII reasons take one byte a rune", shape:$t})
              elif $cs == "multibyte" and ($t.bytesMax <= $t.runesMax or $t.bytesMax > 4 * $t.runesMax) then inv("text-shape"; {what:"multibyte reasons take more than one and at most four bytes a rune", shape:$t})
              elif $cs == "multibyte" and $m.targetReasonLen >= 21 and (($t.sample | type) != "object" or ([$t.sample.width2, $t.sample.width3, $t.sample.width4, $t.sample.combining] | any(. == null or . <= 0))) then inv("text-shape"; {what:"a sample of the reasons shows 2-, 3- and 4-byte runes and combining marks", shape:$t})
              else empty end
          else empty end ),
      ( ($m.charset // "ascii") as $cs
        | if $m.item == 6 and ($m.case | type) == "string" and ($m.case | startswith("delete-")) and ($m.reasons // 0) > 0 and ($m.outcome | type) == "object" and ($m.outcome.textExact == false or ($cs == "multibyte" and $m.outcome.textExact != true)) then inv("text-not-exact"; {charset:$cs, outcome:$m.outcome})
          elif $m.item == 5 and $m.case == "request-size" and ($m.request.fitOutcome | type) == "object" and ($m.request.fitOutcome.textExact == false or ($cs == "multibyte" and $m.request.fitOutcome.textExact != true)) then inv("text-not-exact"; {charset:$cs, outcome:$m.request.fitOutcome})
          else empty end ),
      ( if ($m.charset // "ascii") == "multibyte" and $m.item == 3 and ($m.reasons // 0) > 0 and ($m.targetReasonLen | numz) != null
             and (($m.traversal.responseBytes | type) != "number" or $m.traversal.responseBytes < $m.reasons * $m.targetReasonLen * 2)
        then inv("response-not-multibyte"; {responseBytes:$m.traversal.responseBytes, reasons:$m.reasons, targetReasonLen:$m.targetReasonLen, why:"the pages of N multibyte reasons of L runes carry more than 2 * N * L bytes; fewer means the text was not multibyte on the wire"}) else empty end ),
      ( if $m.item == 10 and ($m.agreement != null or ($m.charset // "ascii") == "multibyte") then
          ($m.agreement) as $a | (($m.charset // "ascii") == "multibyte") as $mb
          | if $a == null then inv("browser-agreement-missing"; {why:"a multibyte browser case compares the browser's digest and the DOM text with the server's"})
            elif $a.digest == null or $a.digest.checked != true then (if $mb then inv("browser-agreement-missing"; {digest:$a.digest}) else empty end)
            elif $a.digest.equal != true then inv("browser-digest-disagrees"; {digest:$a.digest})
            elif $mb and ($a.domText == null or $a.domText.checked != true or ($a.domText.rowsChecked // 0) < 1) then inv("browser-agreement-missing"; {domText:$a.domText})
            elif $a.domText != null and $a.domText.checked == true
                 and (($a.domText.mismatched // 0) > 0 or $a.domText.membershipOk != true or $a.domText.exact != true
                      or (($a.domText.expectedRows | numz) == null) or $a.domText.expectedRows < 1 or $a.domText.rowsOnScreen != $a.domText.expectedRows
                      or (($a.domText.rowsChecked // 0) + ($a.domText.skipped // 0)) != $a.domText.expectedRows) then inv("browser-text-disagrees"; {domText:$a.domText})
            else empty end
        else empty end ),
      ( if $m.item == 10 then browser_budgets($m) else empty end )
    | . + {item:$m.item, size:$m.size, case:$m.case}
JQ
}
# A browser run that failed part-way (the page stopped answering, a control was missing) leaves a "partial" row: the phases that did finish are kept,
# so a budget already crossed (a walk over 120 s, a slow click) is not lost with the failure. The row is judged for those budgets, is invalid as
# incomplete, and never counts as the measured case.
qc1_judge_partial() { cat <<'JQ'
    select(.status == "partial") | norm_row as $m
    | ( inv("browser-incomplete"; {phase:$m.failed.phase, error:$m.failed.error}), browser_budgets($m) )
    | . + {item:$m.item, size:$m.size, case:$m.case}
JQ
}
qc1_judge() { # measurements.jsonl max_request_bytes
  local -a a=(-c --argjson unit "$QC1_UNIT_BUDGET_MS" --argjson del "$QC1_DELETE_BUDGET_MS" --argjson trav "$QC1_TRAVERSAL_BUDGET_MS" --argjson clickp95 "$QC1_CLICK_P95_BUDGET_MS"
    --argjson clickmax "$QC1_CLICK_MAX_BUDGET_MS" --argjson nav "$QC1_NAV_BUDGET_MS" --argjson unitrows "$QC1_SCANNER_UNIT_ROWS" --argjson cap "${2:-0}")
  jq "${a[@]}" -f <(qc1_judge_defs; qc1_judge_rules) "$1" || return $?
  jq "${a[@]}" -f <(qc1_judge_defs; qc1_judge_partial) "$1"
}

# The rows a run owes for sizes, counts and dense targets (items 1 to 3 and 5 to 10), as [{item, size, case}]. A count above the table's size is not wanted (the table
# could not hold it). The dense targets are additive: the same discovery, browser and deletion rows for each dense count the table holds. The order control repeats
# the first discovery case at the end of the size's run; its distance from the first is the order effect of the audit rows and the dirtied table the run leaves behind.
# Legacy items 4 and 5 are NOT here: they are owed separately (qc1_legacy_want_json) and stay pending until measured.
qc1_want_json() { # sizes counts dense(optional, may be empty)
  local sizes counts dense="" max_size
  sizes="$(qc1_list "$1" | paste -sd, -)" || return 1
  counts="$(qc1_list "$2" | paste -sd, -)" || return 1
  max_size="$(qc1_list "$1" | sort -n | tail -n 1)"
  if [[ -n "${3:-}" ]]; then dense="$(qc1_list "$3" | paste -sd, -)" || return 1; fi
  jq -nc --argjson sizes "[$sizes]" --argjson counts "[$counts]" --argjson dense "[${dense}]" --argjson dc "[$QC1_DELETE_COUNTS]" --argjson bc "[$QC1_BROWSER_COUNTS]" --argjson max "$max_size" '
    [ ($sizes[] as $s | (
        {item:1,size:$s,case:"sparse"}, {item:2,size:$s,case:"unrelated"},
        ($counts[] as $k | select($k <= $s) | ("requester","subject") as $d | {item:3,size:$s,case:("discovery-" + $d + "-" + ($k|tostring))}),
        ($dense[] as $k | select($k <= $s) | ("requester","subject") as $d | {item:3,size:$s,case:("discovery-" + $d + "-" + ($k|tostring))}),
        {item:3,size:$s,case:"order-control-requester-10"}, {item:6,size:$s,case:"order-control-delete-start"}, {item:6,size:$s,case:"order-control-delete-end"},
        {item:6,size:$s,case:"order-control-delete-start-explained"}, {item:6,size:$s,case:"order-control-delete-end-explained"},
        {item:5,size:$s,case:"request-size"}, {item:7,size:$s,case:"human-progress"}, {item:8,size:$s,case:"plans"},
        (($bc + $dense)[] as $k | select($k <= $s) | {item:10,size:$s,case:("browser-panel-" + ($k|tostring))}),
        (($dc + $dense)[] as $k | select($k <= $s) | ("requester","subject") as $d | ("keep","remove","mixed") as $m
          | {item:6,size:$s,case:("delete-" + $d + "-" + ($k|tostring) + "-" + $m)}) )),
      {item:9,size:$max,case:"workers-on-delete"} ]'
}
# The legacy rows the R4-C06 matrix owes PER SIZE, which T07B-3a will produce: legacy discovery with 0, 100 and 10,000 unreviewed pairs, the marker lookup
# (the probe), and the legacy PUT with its real validation. Until every one of them is a measured, clean row they are all pending, whatever else exists.
qc1_legacy_want_json() { # sizes
  local sizes
  sizes="$(qc1_list "$1" | paste -sd, -)" || return 1
  jq -nc --argjson sizes "[$sizes]" --argjson lc "[$QC1_LEGACY_COUNTS]" '
    [ $sizes[] as $s | ($lc[] as $k | select($k <= $s) | {item:4,size:$s,case:("legacy-discovery-" + ($k|tostring))}), {item:4,size:$s,case:"legacy-discovery-probe"}, (select($s >= 100) | {item:5,size:$s,case:"legacy-put"}) ]'
}

# Said out loud in every evidence status: what this harness does NOT measure or only approximates, so a clean run is not read as more than it is.
QC1_NOT_MEASURED_JSON="$(cat <<'JSON'
["gate hold time: the gate figures are SAMPLED (the span from the first to the last sample that saw it, with the sampler cadence), never an exact hold time",
 "digest-only time on the bundled fallback path: only press-to-request (an upper bound that includes minting the attempt id and serialising the body) is available; crypto.subtle time is measured by wrapping the call, on the webcrypto path only",
 "isolated server-side parse and encode time: only whole request times, and the page's own text() read and JSON.parse, are measured; the server's share of a request is not separated",
 "multi-byte reasons: this run's reasons are ASCII; a run with --charset multibyte measures 1,000-rune reasons of mixed 1- to 4-byte runes (and says what it still does not)",
 "cluster attribution: WAL, commit, audit and memory figures are cluster-wide counters read around an operation (and around each browser phase), not attributed to one statement",
 "a real droplet: the figures come from a nested or local Docker approximation unless the environment record says otherwise",
 "an exact table size: each operation records the rows contact_requests held just before and after it (background plus the scenario rows, n + k); the background target is not the total"]
JSON
)"
# What a --charset multibyte run still does not measure, in place of the ASCII run's multi-byte line.
QC1_NOT_MEASURED_MULTIBYTE="multi-byte reasons: the text is a fixed 21-rune pattern of 1- to 4-byte runes (61 bytes, about 2.9 bytes a rune), never the 4-byte worst case of 4,000 bytes a reason, and it repeats, so compression, TOAST and index behaviour are those of repetitive text, not prose; the pages, the browser and the digest see real multi-byte text"

# Which measurements a complete QC-1 run holds. Prints {complete, missing, pending, invalid, budgetFailures, coverage, releaseEvidence, notMeasured, confounding}.
# sizes, counts and dense are the comma lists the run was asked for; env is the environment record, head HEAD's SHA, max the maxRequestBytes.
#   complete          nothing missing, nothing pending, no invalid finding, a sound environment record and a judge that ran, FOR THE ROWS IT ASKED FOR.
#   pending           every legacy row the matrix owes per size (qc1_legacy_want_json) that is not a measured, clean row. A failed row, a placeholder or a row that
#                     fails the judge clears nothing.
#   coverage          "full" only when the ROWS prove it: every row of the full matrix (sizes 10k/100k/1M, the default counts, the dense 100,000) is a measured, clean row,
#                     the target histories are 1,000 runes, the browser ran, and the environment record's settings AGREE with the arguments and the rows. The settings
#                     alone never make a run full; a disagreement between them and the arguments or the rows is an invalid finding.
#   releaseEvidence   complete AND full.
# Budget failures are reported for disposition and do not make a run incomplete.
qc1_evidence_status() { # measurements.jsonl sizes counts environment.json head max_request_bytes [dense-counts] [abort.json]
  local sizes counts dense="" want fullwant legacy judged judge_ok=true env_problems envjson abortjson=null
  # Lines that are not JSON cannot be judged or counted: the whole file is invalid evidence.
  if ! jq -e . "$1" >/dev/null 2>&1; then
    printf '%s\n' '{"complete":false,"missing":[],"pending":[],"invalid":[{"class":"invalid","failure":"unparsable-measurements"}],"budgetFailures":[]}'
    return 0
  fi
  sizes="$(qc1_list "$2" | paste -sd, -)" || return 1
  counts="$(qc1_list "$3" | paste -sd, -)" || return 1
  if [[ -n "${7:-}" ]]; then dense="$(qc1_list "$7" | paste -sd, -)" || return 1; fi
  want="$(qc1_want_json "$sizes" "$counts" "$dense")" || return 1
  fullwant="$(qc1_want_json 10000,100000,1000000 "$QC1_COUNTS_DEFAULT" 100000)" || return 1
  legacy="$(qc1_legacy_want_json "$sizes")" || return 1
  judged="$(qc1_judge "$1" "$6")" || judge_ok=false
  env_problems="$(qc1_environment_problems "$4" "$5" | jq -R . | jq -sc .)" || env_problems='["environment record unreadable"]'
  envjson="$(jq -c . "$4" 2>/dev/null || printf null)"
  if [[ -n "${8:-}" && -s "${8:-}" ]]; then abortjson="$(jq -c . "$8" 2>/dev/null || printf null)"; fi   # an aborted run: how it ended, carried into the status
  jq -s -c --argjson want "$want" --argjson fullWant "$fullwant" --argjson legacyWant "$legacy" --arg pending4 "$QC1_PENDING_4" --arg pending5 "$QC1_PENDING_5" --argjson judgeOk "$judge_ok" \
    --argjson aborted "$abortjson" --argjson argSizes "[$sizes]" --argjson argCounts "[$counts]" --argjson argDense "[${dense}]" --argjson dc "[$QC1_DELETE_COUNTS]" --argjson env "$envjson" \
    --argjson envProblems "$env_problems" --argjson notMeasured "$QC1_NOT_MEASURED_JSON" --arg notMeasuredMultibyte "$QC1_NOT_MEASURED_MULTIBYTE" --argjson judged "$(jq -sc . <<<"$judged")" '
    def nums($x): if $x == null then null else ($x | tostring | split(",") | map(tonumber? // empty) | unique) end;
    def same($a; $b): ($a | unique) == ($b | unique);
    . as $rows
    | ($rows | map({item, size, case, status})) as $have
    # The same (item, size, case) measured twice is not evidence of either: both rows are suspect and neither clears anything.
    | ([ $have[] | select(.status == "measured" or .status == "observation") ] | group_by([.item, .size, .case]) | map(select(length > 1) | {item: .[0].item, size: .[0].size, case: .[0].case, count: length})) as $dups
    | def present($w): any($have[]; .item == $w.item and .size == $w.size and .case == $w.case and (.status == "measured" or (.status == "observation" and $w.item == 9)));
      def clean($w): present($w) and ($judged | all(.class != "invalid" or .item != $w.item or .size != $w.size or .case != $w.case)) and (($dups | map(select(.item == $w.item and .size == $w.size and .case == $w.case)) | length) == 0);
      ($env.settings // {}) as $s
    | ([ if $s.sizes != null and (same(nums($s.sizes); $argSizes) | not) then {what:"sizes", settings:$s.sizes, argument:$argSizes} else empty end,
         if $s.counts != null and (same(nums($s.counts); $argCounts) | not) then {what:"counts", settings:$s.counts, argument:$argCounts} else empty end,
         if $s.denseCounts != null and ($s.denseCounts | tostring) != "none" and (same(nums($s.denseCounts); $argDense) | not) then {what:"dense counts", settings:$s.denseCounts, argument:$argDense}
           elif $s.denseCounts != null and ($s.denseCounts | tostring) == "none" and ($argDense | length) > 0 then {what:"dense counts", settings:"none", argument:$argDense} else empty end,
         if $s.deleteCounts != null and (same(nums($s.deleteCounts); ($dc + $argDense)) | not) then {what:"deletion counts", settings:$s.deleteCounts, argument:($dc + $argDense)} else empty end,
         # a row that says its target reasons are not what the settings say they are
         ($rows[] | select((type == "object") and (.item == 3 or .item == 6 or .item == 10) and (.status == "measured" or .status == "observation") and $s.targetReasonLen != null and .targetReasonLen != $s.targetReasonLen)
            | {what:"target reason length", settings:$s.targetReasonLen, row:{item, size, case, targetReasonLen}}),
         # a row of another charset than the settings name (a pre-charset record means ascii)
         ($rows[] | select((type == "object") and (.status == "measured" or .status == "observation") and .item != null and ((.charset // "ascii") != ($s.charset // "ascii")))
            | {what:"charset", settings:($s.charset // "ascii"), row:{item, size, case, charset}}),
         # a row of a case that loads the scenario of the cell (discovery, deletion, workers, browser) must carry the layout the settings name: a row of another layout, or of none, is not
         # the measurement of that cell, however the saved coverage reads. (The request-size and plan rows load their own layouts and are not held to this.)
         ($rows[] | select((type == "object") and (.status == "measured" or .status == "observation") and (.item == 3 or .item == 6 or .item == 9 or .item == 10) and $s.layout != null and (.layout != $s.layout))
            | {what:"layout", settings:$s.layout, row:{item, size, case, layout}}) ]) as $disagree
    | ([ $fullWant[] | select(clean(.) | not) ]) as $fullGaps
    | ([ $rows[] | select((type == "object") and (.item == 3 or .item == 6 or .item == 10) and (.status == "measured" or .status == "observation") and .targetReasonLen != 1000) ] | length) as $shortTargets
    | ([ (if ($env == null) then "environment record unreadable" else empty end),
         (if ($fullGaps | length) > 0 then "rows of the full matrix are not measured and clean: " + ($fullGaps | length | tostring) + " (first: item " + ($fullGaps[0].item | tostring) + " size " + ($fullGaps[0].size | tostring) + " " + $fullGaps[0].case + ")" else empty end),
         (if $shortTargets > 0 then "rows whose target reasons are not the 1,000-rune maximum: " + ($shortTargets | tostring) else empty end),
         (if $s.targetReasonLen != 1000 then "the settings do not name 1,000-rune target reasons (targetReasonLen " + ($s.targetReasonLen | tostring) + ")" else empty end),
         (if $s.browser != true then "no browser panel measurement" else empty end),
         (if $s.wire != "set-v1" then "not the set-v1 wire" else empty end),
         (if ($s.layout | IN("spread", "tail", "head") | not) then "the settings do not name the layout (spread, tail or head) the rows were measured in" else empty end),
         (if ($disagree | length) > 0 then "the settings disagree with the arguments or the rows: " + ($disagree | map(.what) | join(", ")) else empty end),
         (if $s.sizes == null or $s.counts == null or $s.denseCounts == null or $s.deleteCounts == null then "the settings do not name the sizes, counts, dense counts and deletion counts" else empty end) ]) as $covReasons
    | { missing: [ $want[] | select(present(.) | not) ],
        pending: [ $legacyWant[] | select(clean(.) | not) | . + {reason: (if .item == 4 then $pending4 else $pending5 end)} ],
        invalid: ( [ $judged[] | select(.class == "invalid") ]
                   + [ $envProblems[] | {class:"invalid", failure:"environment", problem:.} ]
                   + [ $disagree[] | {class:"invalid", failure:"settings-disagreement"} + . ]
                   + [ $dups[] | {class:"invalid", failure:"duplicate-row"} + . ]
                   + (if $judgeOk then [] else [{class:"invalid", failure:"judge-failed"}] end) ),
        budgetFailures: [ $judged[] | select(.class == "budget") ],
        # Every scanner and plan miss against R3, grouped for review, with what it was (the plan in words), what it cost and the table it ran against. Nothing here is a demotion:
        # budgetFailures above still lists every finding; this is the reading list for the engineering disposition each miss needs, whatever it cost and however small the table.
        dispositions: ( [ $judged[] | select(.class == "budget" and ((.failure | startswith("scanner-")) or (.failure | startswith("plan-")))) ]
                        | group_by([.failure, (.variant // "")])
                        | map({finding: .[0].failure, variant: (.[0].variant // null), count: length, requirement: (.[0].requirement // null), tableRows: (.[0].tableRows // null), unitRows: (.[0].unitRows // null), belowUnit: (.[0].belowUnit // null),
                               plans: ([ .[] | .plan // empty ] | unique), maxSharedBlocks: ([ .[] | .measured.sharedBlocks // empty ] | if length == 0 then null else max end),
                               status: "open: a measured miss, retained; it needs a reviewed engineering disposition (not demoted at any table size)"}) ),
        confounding: (
          [ $rows[] | select((type == "object") and (.status == "measured" or .status == "observation")) | (.traversal |= (if type == "object" then . else null end)) | (.delete |= (if type == "object" then . else null end)) ] as $ms
          | [ $ms[] | (.traversal, .delete) | select(type == "object") ] as $ran
          | { note: "recorded, not judged: every case leaves audit rows (one view audit per nonempty page, one decision audit per reason) and a dirtier table behind it, and the cases run in a fixed order; seq is that order",
              cases: ($ms | length),
              auditRowsBefore: ([ $ran[] | .auditRowsBefore | numbers ] | if length == 0 then null else {min: min, max: max} end),
              auditBytesBefore: ([ $ran[] | .auditBytesBefore | numbers ] | if length == 0 then null else {min: min, max: max} end),
              orderControls: [ $ms[] | select(.item == 3 and .case == "order-control-requester-10") as $c
                | ($ms | map(select(.item == 3 and .size == $c.size and .case == "discovery-requester-10")) | .[0]) as $f
                | {size: $c.size, firstSeq: $f.seq, controlSeq: $c.seq, firstTotalMs: $f.traversal.totalMs, controlTotalMs: $c.traversal.totalMs,
                   ratio: (if (($f.traversal.totalMs | type) == "number") and (($c.traversal.totalMs | type) == "number") and $f.traversal.totalMs > 0 then ((($c.traversal.totalMs / $f.traversal.totalMs) * 100 | round) / 100) else null end),
                   auditRowsFirst: $f.traversal.auditRowsBefore, auditRowsControl: $c.traversal.auditRowsBefore} ],
              # The same deletion (an account with no reasons) at the start and at the end of a size run: what the run did to the cost of a deletion. Recorded, never judged.
              deleteOrderControls: [ $ms[] | select(.item == 6 and .case == "order-control-delete-start") as $s
                | ($ms | map(select(.item == 6 and .size == $s.size and .case == "order-control-delete-end")) | .[0]) as $e
                | {size: $s.size, startSeq: $s.seq, endSeq: $e.seq, startMs: $s.delete.ms, endMs: $e.delete.ms,
                   ratio: (if (($s.delete.ms | type) == "number") and (($e.delete.ms | type) == "number") and $s.delete.ms > 0 then ((($e.delete.ms / $s.delete.ms) * 100 | round) / 100) else null end),
                   auditRowsStart: $s.delete.auditRowsBefore, auditRowsEnd: $e.delete.auditRowsBefore} ],
              # The audit-retention report statement of the EXPLAINED deletions (the extra zero-reason deletions run with auto_explain on for that one operation): the statement time, how much of it was
              # JIT, the planner estimate for the plan root and the audit_logs scan with what it really did. Recorded, never judged; these deletions carry the capture overhead, so their time is
              # not compared with the unexplained controls above.
              # identityProven is the judge own verdict (no audit-statement-unproven, -ambiguous or audit-plan-missing finding for the row), never a count of flags the row carries.
              explainedOrderControls: ( [ $ms[] | select(.item == 6 and ((.case | type) == "string") and (.case | endswith("-explained")))
                  | (.auditRetention // {}) as $a | ($a.plan.nodes // []) as $n | ($a.plan.jit // null) as $j | ([ $n[] | select(.relation == "audit_logs") ] | .[0]) as $sc
                  | {size: .size, case: .case, seq: .seq, deleteMsWithCapture: .explained.ms, statementMs: $a.durationMs, statementBlocks: $a.statementBlocks,
                     identityProven: (. as $r | $judgeOk and $a.statementBlocks == 1 and ([ $judged[] | select(.item == $r.item and .size == $r.size and .case == $r.case and (.failure == "audit-statement-unproven" or .failure == "audit-statement-ambiguous" or .failure == "audit-plan-missing")) ] | length) == 0),
                     planRoot: (if ($n | length) == 0 then null else {node: $n[0].nodeType, estimatedTotalCost: $n[0].totalCost, estimatedRows: $n[0].planRows, actualRows: $n[0].actualRows, actualTotalMs: $n[0].actualTotalMs} end),
                     auditLogsScan: (if $sc == null then null else {node: $sc.nodeType, estimatedTotalCost: $sc.totalCost, estimatedRows: $sc.planRows, actualRows: $sc.actualRows, rowsRemovedByFilter: $sc.rowsRemovedByFilter, sharedHit: $sc.sharedHit, sharedRead: $sc.sharedRead, actualTotalMs: $sc.actualTotalMs} end),
                     jit: (if $j == null then null else {functions: $j.functions, totalMs: $j.totalMs, optimizationMs: $j.optimizationMs, emissionMs: $j.emissionMs,
                                                         shareOfStatement: (if ($a.durationMs | type) == "number" and $a.durationMs > 0 and ($j.totalMs | type) == "number" then ((($j.totalMs / $a.durationMs) * 1000 | round) / 1000) else null end)} end),
                     lockWaitsLogged: $a.lockWaits.count} ]) } ),
        coverage: {kind: (if ($covReasons | length) == 0 then "full" else "pilot" end), reasons: $covReasons, cell: {layout: ($s.layout // null), charset: ($s.charset // "ascii")}, derivedFrom: "the measurement rows, the arguments and the environment settings, which must agree"} }
    | . + {complete: ((.missing | length) == 0 and (.pending | length) == 0 and (.invalid | length) == 0)}
    | . + {releaseEvidence: (.complete and .coverage.kind == "full"), notMeasured: ($notMeasured | map(if startswith("multi-byte reasons") and (($env.settings.charset // "ascii") == "multibyte") then $notMeasuredMultibyte else . end))}
    | if $aborted != null then . + {aborted: $aborted, complete: false, releaseEvidence: false} else . end' "$1"
}

# The statements whose plans QC-1 records. Plans come from auto_explain's log
# of the application's own statements (never from SQL the harness types), found
# by a fixed substring of the logged "Query Text", which keeps $n placeholders
# and literals. The markers are in app-facts.json; TestQC1PlanMarkersAreWhatTheApplicationSends
# proves the application still sends each of them. The marker lookup is T07B-3a's and is not here.
qc1_plan_marker() { # which
  qc1_fact '.go.planMarkers[$k] // empty' --arg k "$1" 2>/dev/null || return 1
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


# The scanner probe: the REAL scanner statements (qc1_scanner_sql) prepared and executed several times in ONE session at controlled cursor positions, each under
# EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON), in plan_cache_mode auto (the mode the application runs in: the sixth and later executions may use a generic plan) and
# force_generic_plan. This reads each execution's child scan: node type, index, index condition, filter, actual rows, loops and shared blocks, and whether any Sort
# is in the plan. Input: one EXPLAIN JSON document per execution, in order; $1 is the matching JSON array of {mode, kind, percent, afterId}.
qc1_scanner_probe_parse() { # meta-json (stdin: the EXPLAIN documents)
  jq -sc --argjson meta "$1" '
    def nodes: [.. | objects | select(has("Node Type"))];
    [ range(0; length) as $i | .[$i][0].Plan as $p | ($meta[$i] // {}) as $m
      | ($p | nodes) as $ns
      | ([ $ns[] | select((.["Node Type"] | test("Scan$")) and ((.["Relation Name"] // "") == "contact_requests")) ] | .[0]) as $scan
      # Everything beneath the child scan (and the scan itself): the index nodes. A Bitmap Heap Scan carries NO Index Cond of its own; the id condition is on the Bitmap Index Scan under it.
      | (if $scan == null then [] else [ $scan | .. | objects | select(has("Node Type")) ] end) as $sub
      | ([ $sub[] | select(.["Node Type"] == "Bitmap Index Scan") ] | .[0]) as $bix
      | ([ $sub[] | select((.["Index Cond"] | type) == "string" and (.["Index Cond"] | test("\\((?:[[:alnum:]_]+\\.)?id >"))) ] | .[0]["Index Cond"]) as $idc
      | $m + { topRows: $p["Actual Rows"],
               # The planner ESTIMATES beside what happened (estimates: cost units and a row count the planner believed, never a measurement of time or rows).
               top: { nodeType: $p["Node Type"], startupCost: $p["Startup Cost"], totalCost: $p["Total Cost"], planRows: $p["Plan Rows"], planWidth: $p["Plan Width"], actualRows: $p["Actual Rows"], actualTotalMs: $p["Actual Total Time"] },
               planningMs: .[$i][0]["Planning Time"], executionMs: .[$i][0]["Execution Time"], jit: (.[$i][0]["JIT"] // null),
               hasSort: ($ns | any(.["Node Type"] == "Sort" or .["Node Type"] == "Incremental Sort")),
               scan: (if $scan == null then null else
                 { nodeType: $scan["Node Type"], indexName: $scan["Index Name"], indexCond: $scan["Index Cond"], filter: $scan["Filter"],
                   rows: $scan["Actual Rows"], loops: $scan["Actual Loops"], sharedHit: $scan["Shared Hit Blocks"], sharedRead: $scan["Shared Read Blocks"],
                   rowsRemovedByFilter: ($scan["Rows Removed by Filter"] // 0),
                   schema: $scan["Schema"],
                   startupCost: $scan["Startup Cost"], totalCost: $scan["Total Cost"], planRows: $scan["Plan Rows"], planWidth: $scan["Plan Width"], actualTotalMs: $scan["Actual Total Time"],
                   bitmapIndex: (if $bix == null then null else { nodeType: $bix["Node Type"], indexName: $bix["Index Name"], indexCond: $bix["Index Cond"], startupCost: $bix["Startup Cost"], totalCost: $bix["Total Cost"], planRows: $bix["Plan Rows"], rows: $bix["Actual Rows"], sharedHit: $bix["Shared Hit Blocks"], sharedRead: $bix["Shared Read Blocks"] } end),
                   idIndexCond: $idc } end) } ]'
}

# The page summary rebuilt from the observations the measurement process persisted (QC1_OUT.pages.jsonl, one JSON object a line): what a run that died before it could write
# its result still has. A response is a "start" line when it arrives and a "done" line when its body has been read (and a "timeout" line if the drain gave up on it); one with no
# "done" is an EXPLICIT missing entry (completed:false, null figures), so a page that never finished is counted, not forgotten. Lines with no event are done records. The page's own
# read and parse times are not in the file (they live in the page), so they are ABSENT for every page and the row can never pass as complete; the bytes, the request time and the
# Node-side parse are, and a slow or unfinished request is judged from them. Same shape as summarizePages.
qc1_pages_from_jsonl() { # file
  jq -sc '
    def col(k): map(.[k]) | map(select(type == "number"));
    def mx(k): col(k) as $c | if ($c | length) > 0 then ($c | max) else null end;
    def sm(k): col(k) as $c | if ($c | length) > 0 then ($c | add) else null end;
    def p95(k): col(k) | sort as $c | if ($c | length) > 0 then $c[([(($c | length) * 0.95 | ceil), 1] | max) - 1] else null end;
    def absent(k): length - (col(k) | length);
    def inc: map(select(.completed == false));
    def summary: { count: length, started: length, completed: (length - (inc | length)), incomplete: (inc | length), pendingAgeMsMax: (inc | mx("ageMs")),
                   responseBytesMax: mx("bytes"), responseBytesTotal: sm("bytes"), requestMsMax: mx("requestMs"), requestMsP95: p95("requestMs"),
                   readMsMax: null, readMsTotal: null, parseMsMax: null, parseMsTotal: null, nodeParseMsMax: mx("nodeParseMs"),
                   absent: { bytes: absent("bytes"), requestMs: absent("requestMs"), readMs: length, parseMs: length, nodeParseMs: absent("nodeParseMs") } };
    ( group_by(.seq)
      | map( ( map(select(.event == "done" or .event == null)) | .[0] ) as $d
             | if $d != null then ($d | del(.event))
               else ( ( map(select(.event == "start")) | .[0] ) // .[0] ) as $s
                    | ( map(select(.event == "timeout")) | .[0] ) as $t
                    | { seq: $s.seq, walk: ($s.walk // 1), status: $s.status, foreignOrigin: $s.foreignOrigin, bytes: null, requestMs: null, nodeParseMs: null, completed: false, ageMs: ($t.ageMs // null) } end ) ) as $obs
    | if ($obs | length) == 0 then null else
        ($obs | summary) + { walks: ($obs | group_by(.walk // 1) | map({walk: (.[0].walk // 1)} + summary)), parseKind: "rebuilt from the persisted observations: the page-side read and parse times are not in them" }
      end' "$1"
}

# Does any plan of this kind in the log contain this text? The extraction is CAPTURED, then searched in the shell: never `extract | grep -q`. Under `set -o pipefail`, a `grep -q`
# that matches in the first block closes the pipe while the extraction is still writing the later ones (a 1 MB log yields hundreds of plans); the writer dies of SIGPIPE, the pipeline
# is reported as FAILED, and a plan that plainly contains the text is called a plan that does not. The pilot's `plan-continuation` finding (loggedPlanHasIndexCond=false beside nine
# `Index Cond: (id >` lines in its own log) is suspected to be this; the fixtures reproduce the pipe failure with a large log and show this function immune to it.
qc1_plan_has() { # log which text -> status 0 when found, 1 when not found, 2 when the log could not be read
  local out
  out="$(qc1_extract_plans "$1" "$2")" || return 2
  [[ "$out" == *"$3"* ]]
}

# What the application's own LOGGED executions of one statement kind looked like, as one JSON object: how many, and how many had the id predicate as an Index Cond (on an Index Scan, or
# on the Bitmap Index Scan beneath a Bitmap Heap Scan), how many were an Index Scan, a bitmap scan, a Seq Scan (with the id predicate only as a Filter) or had a Sort above them.
qc1_plan_shapes() { # log which
  local out
  out="$(qc1_extract_plans "$1" "$2")" || return 2
  awk '
    function flush() { if (!inb) return; n++; if (idcond) c++; if (idx) ix++; if (bit) bm++; if (seq) sq++; if (seqf) sf++; if (srt) st++; if (!idcond) { if (nocond != "") nocond = nocond ","; nocond = nocond n } }
    function reset() { idcond = idx = bit = seq = seqf = srt = 0 }
    /duration: .* plan:/ { flush(); inb = 1; reset(); next }
    inb && /Index Cond: \(id >/ { idcond = 1 }
    inb && /Index Scan using/ { idx = 1 }
    inb && /Bitmap Index Scan/ { bit = 1 }
    inb && /Seq Scan on contact_requests/ { seq = 1 }
    inb && /Filter: \(id >/ { seqf = 1 }
    inb && /^[ \t]*(->[ \t]+)?(Incremental )?Sort[ \t]+\(cost/ { srt = 1 }
    END { flush(); printf "{\"executions\":%d,\"withIdIndexCond\":%d,\"indexScan\":%d,\"bitmapIndexScan\":%d,\"seqScan\":%d,\"seqScanFilterOnId\":%d,\"withSort\":%d,\"withoutIdIndexCond\":[%s]}\n", n, c, ix, bm, sq, sf, st, nocond }' <<<"$out"
}

# --- the application's audit-retention report, as the application ran it ------------------------------------------------------------------------------------
# The statement account erasure runs over audit_logs (account_erasure_audit.go: SELECT class, action, count(*) FROM (SELECT action, <classification> AS class FROM audit_logs WHERE ip <> ''
# AND ((<subjectMention>) OR (<malformed conversion link>))) classified WHERE class IN (...) GROUP BY class, action). Pilots 1 to 3 found it to be the dominant, almost constant cost of every
# deletion once the audit table had grown, and pilot3's own plan-stage log held one execution of it (a Seq Scan whose plan cost was 9.6 million and whose 2,153 ms was 2,093 ms of JIT compilation).
# What follows reads an auto_explain log written while the application ran a deletion and PROVES the statement it reports is the application's own and no other: the fixed pieces of the Go
# statement, in order, around three non-empty variable parts; its parameters are the deleted account's id; it ran in the backend of the erasure's own audit statements, after the UPDATEs that blank
# addresses, and the first statement that backend logs after it is the application's conversion discovery for the same account. Nothing is substituted, re-run or reconstructed by the harness.

# The four fixed pieces of the statement, one per line, from the facts file (TestQC1AppFacts holds them equal to auditRetentionReportSQL).
qc1_audit_pieces() { qc1_fact '.go.auditReport.pieces[]'; }

# Every auto_explain block in a log as one TSV line: log line, backend pid, duration in ms, the first 90 characters of its query text.
qc1_log_blocks() { # log
  awk '
    /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9:.]+ [A-Z]+ \[[0-9]+\] / {
      want = 0
      if (match($0, /\[[0-9]+\]/)) pid = substr($0, RSTART + 1, RLENGTH - 2)
      if ($0 ~ /duration: .* plan:/) { d = $0; sub(/^.*duration: /, "", d); sub(/ ms.*$/, "", d); ln = NR; want = 1 }
      next }
    want == 1 && /^\tQuery Text: / { q = $0; sub(/^\tQuery Text: /, "", q); printf "%d\t%s\t%s\t%s\n", ln, pid, d, substr(q, 1, 90); want = 0 }' "$1"
}

# One auto_explain block (stdin) as {nodes, jit}: every plan node with its planner estimates (startup and total cost, rows, width), its actual time, rows and loops (or neverExecuted), and the
# attributes under it (Filter, Rows Removed by Filter, shared buffers, Index Cond, Sort Method), and the JIT section (functions, and the time spent generating, inlining, optimizing and emitting).
# The planner's figures are ESTIMATES. Text format, as auto_explain writes it here.
qc1_plan_nodes() {
  awk '
    function js(s) { gsub(/\\/, "\\\\", s); gsub(/"/, "\\\"", s); gsub(/[\t\r]/, " ", s); return "\"" s "\"" }
    function nz(v) { return (v == "" ? "null" : v) }
    function emit() {
      if (!have) return
      printf "%s{\"depth\":%d,\"label\":%s,\"nodeType\":%s,\"relation\":%s,\"startupCost\":%s,\"totalCost\":%s,\"planRows\":%s,\"width\":%s,\"neverExecuted\":%s,\"actualStartMs\":%s,\"actualTotalMs\":%s,\"actualRows\":%s,\"loops\":%s,\"filter\":%s,\"rowsRemovedByFilter\":%s,\"sharedHit\":%s,\"sharedRead\":%s,\"indexCond\":%s,\"sortMethod\":%s}",
        (nodes++ ? "," : ""), depth, js(label), js(ntype), (rel == "" ? "null" : js(rel)), nz(sc), nz(tc), nz(pr), nz(wd), never, nz(as), nz(at), nz(ar), nz(lp), (flt == "" ? "null" : js(flt)), nz(rrf), nz(sh), nz(sr), (ic == "" ? "null" : js(ic)), (sm == "" ? "null" : js(sm))
      have = 0 }
    BEGIN { printf "{\"nodes\":[" }
    {
      line = $0; sub(/^\t/, "", line)
      if (line ~ /\(cost=[0-9.]+\.\.[0-9.]+ rows=[0-9]+ width=[0-9]+\)/) {
        emit(); inplan = 1; have = 1
        match(line, /^ */); depth = RLENGTH; r = substr(line, depth + 1); sub(/^->  /, "", r)
        match(r, /  \(cost=[0-9.]+\.\.[0-9.]+ rows=[0-9]+ width=[0-9]+\)/); m_s = RSTART; m_l = RLENGTH
        label = substr(r, 1, m_s - 1); c = substr(r, m_s + 8, m_l - 9)
        split(c, a, /[ .=]+/); sc = ""; tc = ""; pr = ""; wd = ""
        if (match(c, /^[0-9.]+\.\.[0-9.]+/)) { cc = substr(c, 1, RLENGTH); n2 = index(cc, ".."); sc = substr(cc, 1, n2 - 1); tc = substr(cc, n2 + 2) }
        if (match(c, /rows=[0-9]+/)) pr = substr(c, RSTART + 5, RLENGTH - 5)
        if (match(c, /width=[0-9]+/)) wd = substr(c, RSTART + 6, RLENGTH - 6)
        ntype = label; sub(/ (on|using) .*$/, "", ntype); rel = ""
        if (match(label, / on [^ ]+/)) rel = substr(label, RSTART + 4, RLENGTH - 4)
        rest = substr(r, m_s + m_l)
        never = "false"; as = ""; at = ""; ar = ""; lp = ""; flt = ""; rrf = ""; sh = ""; sr = ""; ic = ""; sm = ""
        if (rest ~ /never executed/) never = "true"
        else if (match(rest, /actual time=[0-9.]+\.\.[0-9.]+ rows=[0-9]+ loops=[0-9]+/)) {
          t = substr(rest, RSTART, RLENGTH); match(t, /time=[0-9.]+\.\./); as = substr(t, RSTART + 5, RLENGTH - 7)
          match(t, /\.\.[0-9.]+ rows/); at = substr(t, RSTART + 2, RLENGTH - 7)
          match(t, /rows=[0-9]+/); ar = substr(t, RSTART + 5, RLENGTH - 5)
          match(t, /loops=[0-9]+/); lp = substr(t, RSTART + 6, RLENGTH - 6) }
        next }
      t = line; sub(/^ +/, "", t)
      if (!inplan) next
      if (t ~ /^JIT:/) { injit = 1; next }
      if (!injit && (t ~ /^Functions:/ || t ~ /^Options:/ || t ~ /^Timing: Generation/)) { stray = 1; next }   # JIT lines with no JIT: header above them: a damaged plan, not an absent section
      if (injit) {
        if (t ~ /^Functions: /) { if (t ~ /^Functions: [0-9]+$/) jf = substr(t, 12) + 0; else jf = "" }
        else if (t ~ /^Options: /) { jo = substr(t, 10) }
        else if (t ~ /^Timing: /) {
          if (match(t, /Generation [0-9.]+ ms/)) jg = substr(t, RSTART + 11, RLENGTH - 14)
          if (match(t, /Inlining [0-9.]+ ms/)) ji = substr(t, RSTART + 9, RLENGTH - 12)
          if (match(t, /Optimization [0-9.]+ ms/)) jopt = substr(t, RSTART + 13, RLENGTH - 16)
          if (match(t, /Emission [0-9.]+ ms/)) je = substr(t, RSTART + 9, RLENGTH - 12)
          if (match(t, /Total [0-9.]+ ms/)) jt = substr(t, RSTART + 6, RLENGTH - 9) }
        next }
      if (!have) next
      if (t ~ /^Filter: /) { if (flt == "") flt = substr(t, 9, 400) }
      else if (t ~ /^Rows Removed by Filter: /) { if (rrf == "") rrf = substr(t, 25) + 0 }
      else if (t ~ /^Index Cond: /) { if (ic == "") ic = substr(t, 13, 300) }
      else if (t ~ /^Sort Method: /) { if (sm == "") { sm = substr(t, 14); sub(/ .*$/, "", sm) } }
      else if (t ~ /^Buffers: shared /) { if (sh == "" && sr == "") { sh = 0; sr = 0; if (match(t, /hit=[0-9]+/)) sh = substr(t, RSTART + 4, RLENGTH - 4) + 0; if (match(t, /read=[0-9]+/)) sr = substr(t, RSTART + 5, RLENGTH - 5) + 0 } }
    }
    END { emit(); printf "],\"jitStray\":%s,\"jit\":", (stray ? "true" : "false")
      if (injit) printf "{\"functions\":%s,\"options\":%s,\"generationMs\":%s,\"inliningMs\":%s,\"optimizationMs\":%s,\"emissionMs\":%s,\"totalMs\":%s}", nz(jf), js(jo), nz(jg), nz(ji), nz(jopt), nz(je), nz(jt)
      else printf "null"
      printf "}\n" }'
}

# What the facts file says the report statement is made of beyond its four fixed pieces: the subject-mention and malformed-link texts with each named argument (@uid, @people) numbered $1, $2 ...
# in order of appearance (what GORM sends), and the kind of argument at each position. One JSON object {mention, link, kinds}; kinds is empty when a constant is missing.
qc1_audit_expected() {
  local mention link
  mention="$(qc1_fact .go.auditReport.subjectMention -j; printf x)"; mention="${mention%x}"
  link="$(qc1_fact .go.auditReport.malformedConversionLink -j; printf x)"; link="${link%x}"
  jq -nc --arg mention "$mention" --arg link "$link" '
    def toks: [ match("@[a-z]+"; "g").string | ltrimstr("@") ];
    def number($start): . as $t | reduce (match("@[a-z]+"; "g")) as $m ({out: "", pos: 0, n: $start}; .out += $t[.pos:$m.offset] + "$" + (.n | tostring) | .pos = ($m.offset + $m.length) | .n += 1) | .out + $t[.pos:];
    ($mention | toks) as $tm | ($link | toks) as $tl
    | { mention: ($mention | number(1)), link: ($link | number(($tm | length) + 1)), kinds: ($tm + $tl) }
    | if ($mention | length) == 0 or ($link | length) == 0 or (.kinds | length) == 0 then . + {kinds: []} else . end'
}

# The identity proof of an audit-retention report, one predicate for both ends: qc1_audit_report_evidence sets identity.proven and its proof text with it, and the judge rechecks every
# stored row with it. So the summary's identityProven and the proof text can never say more than the judge accepts. A pure function of the stored evidence: it holds when the report
# statement is the Go source text with its complete parameters, ran in the erasure's backend after that erasure's own audit UPDATEs, and that backend's first logged statement
# after it is the application's conversion discovery for the same account, the only one in the log.
qc1_audit_identity_defs() { cat <<'JQ'
    def isnum: type == "number";
    # The parameters the statement was run with, checked again from what the row stored: every position against the kind of argument the Go source puts there. The account is the one
    # the explained deletion deleted; an array position is an id-array literal; a list shorter or longer than its kinds, or naming another account anywhere, fails.
    def audit_params_ok($i; $acct):
      ($i.parameterKinds) as $k | ($i.parameterValues) as $v
      | ($k | type) == "array" and ($v | type) == "array" and ($k | length) >= 8 and ($k | length) == ($v | length) and ($acct | isnum)
        and ([ range(0; $k | length) as $j
               | if $k[$j] == "uid" then $v[$j] == ("'" + ($acct | tostring) + "'")
                 elif ($k[$j] == "people" or $k[$j] == "requests" or $k[$j] == "invitations") then (($v[$j] | type) == "string" and ($v[$j] | test("^'\\{([0-9]+(,[0-9]+)*)?\\}'$")))
                 else false end ] | all);
    # The statement that follows the report: found on the report's own backend, after it, as that backend's first logged statement; the Go source text of conversion discovery's first query
    # (flag set by the collector against the facts file); the deleted account as its only parameter; and the only block of its kind in the whole log.
    def audit_successor_ok($i):
      ($i.successor) as $s
      | ($s | type) == "object" and $s.found == true and $s.textMatchesSource == true
        and ($s.backendPid | type) == "string" and ($s.backendPid | length) > 0 and $s.backendPid == $i.backendPid
        and ($s.line | isnum) and ($i.reportLine | isnum) and $s.line > $i.reportLine
        and ($s.parameters | type) == "string" and ($i.targetUserId | isnum) and $s.parameters == ("$1 = '" + ($i.targetUserId | tostring) + "'")
        and ($s.blocksInLog | isnum) and $s.blocksInLog == 1;
    def audit_identity_ok($i; $acct):
      ($i | type) == "object"
      and $i.prefixOk == true and $i.suffixOk == true and $i.piecesInOrder == true and $i.variablePartsNonEmpty == true and $i.parametersAreTarget == true
      and $i.tailMatchesSource == true and $i.classificationOk == true
      and ($i.targetUserId | isnum) and ($acct | isnum) and $i.targetUserId == $acct and audit_params_ok($i; $i.targetUserId)
      and ($i.backendPid | type) == "string" and ($i.backendPid | length) > 0
      and ($i.reportBlocks | isnum) and $i.reportBlocks == 1
      and ($i.auditUpdatesBefore | isnum) and $i.auditUpdatesBefore >= 1
      and ($i.queryTextSha256 | type) == "string" and ($i.queryTextSha256 | test("^[0-9a-f]{64}$")) and ($i.queryTextBytes | isnum) and $i.queryTextBytes >= 1
      and audit_successor_ok($i);
JQ
}

# The query text and the parameters of one auto_explain block (stdin) as the server logged them; a continuation line of the text loses only the log's leading tab.
qc1_block_query() { awk '/^\tQuery Text: / { inq = 1; sub(/^\tQuery Text: /, ""); print; next } inq && /^\tQuery Parameters: / { exit } inq && /^\t[A-Za-z]/ { exit } inq { sub(/^\t/, ""); print }'; }
qc1_block_params() { awk '/^\tQuery Parameters: / { sub(/^\tQuery Parameters: /, ""); print; exit }'; }

# The audit-retention report statement in an auto_explain log, proven to be the application own and no other, with its plan. Arguments: the log, the id of the
# account the deletion deleted, whether log_lock_waits was on while the log was written. Prints one JSON object; statementBlocks is 0 when the log holds none (the caller marks that INVALID).
# The proof ties the logged report to the deletion by what the application sends around it: its own audit UPDATEs before it, and after it, on the same backend as the first statement that
# backend logs, the first query of conversion discovery (the facts file's successor, the deleted account its only argument), the only such block in the log.
qc1_audit_report_evidence() { # log target-user-id lockwaits-logged(true|false)
  local q log="$1" target="$2" lw="$3" blocks n block1 qtext params rest pid line dur sha i p expected kinds_json values_json mention link var1 tail_txt ev
  local sfact sfirst sline spid sblock stext sparams ssha succ_json
  local -a pieces=()
  local prefix_ok=false suffix_ok=false order_ok=false tail_ok=false class_ok=false param_ok=false before=0 parts_ok=false lockw samples bytes sfound=false stext_ok=false scount=0
  blocks="$(qc1_extract_plans "$log" audit-report)" || return 2
  n="$(grep -c 'duration: .* plan:' <<<"$blocks" || true)"
  [[ "$n" =~ ^[0-9]+$ ]] || n=0
  if (( n == 0 )); then printf '%s\n' '{"statementBlocks":0}'; return 0; fi
  block1="$(awk '/duration: .* plan:/ { c++ } c == 1' <<<"$blocks")"
  mapfile -t pieces < <(qc1_audit_pieces)
  qtext="$(qc1_block_query <<<"$block1")"
  params="$(qc1_block_params <<<"$block1")"
  expected="$(qc1_audit_expected)"
  kinds_json="$(jq -c '.kinds' <<<"$expected")"
  if (( ${#pieces[@]} == 4 )); then
    [[ "$qtext" == "${pieces[0]}"* ]] && prefix_ok=true
    [[ "$qtext" == *"${pieces[3]}" ]] && suffix_ok=true
    # Everything after the classification must be the Go source's own text, byte for byte: the fixed pieces around the subject-mention and the malformed-link constants with their
    # named arguments numbered as GORM sends them. The classification is the one part the source generates from its rule table, so it is held to its shape: a CASE that starts and ends
    # as the source writes it and carries no placeholder.
    mention="$(jq -j '.mention' <<<"$expected")"; link="$(jq -j '.link' <<<"$expected")"
    rest="${qtext#"${pieces[0]}"}"
    var1="${rest%%"${pieces[1]}"*}"
    tail_txt="${rest#"$var1"}"
    [[ -n "$mention" && -n "$link" && "$tail_txt" == "${pieces[1]}${mention}${pieces[2]}${link}${pieces[3]}" ]] && { order_ok=true; tail_ok=true; }
    [[ "$var1" == "CASE WHEN "*"ELSE 'RETAIN_UNATTRIBUTED' END" && "$var1" != *'$'* ]] && class_ok=true
    parts_ok=$class_ok
  fi
  # The parameters: the complete list the statement takes, in order, $1 to $N with nothing before, between or after, each against the kind of argument the Go source puts at that
  # position (uid: the deleted account's id; people: an array literal of ids). A list cut short, extended, reordered or naming another account anywhere fails.
  q="'"
  values_json="$(jq -nc --arg p "$params" --arg q "$q" '[ $p | scan("\\$([0-9]+) = (" + $q + "(?:[^" + $q + "]|" + $q + $q + ")*" + $q + "|NULL)") | {n: (.[0] | tonumber), v: .[1]} ]')"
  param_ok="$(jq -nc --argjson kinds "$kinds_json" --argjson vals "$values_json" --arg target "$target" --arg p "$params" --arg q "$q" '
    ($kinds | length) as $k
    | $k > 0 and ($vals | length) == $k and ($vals | map(.n)) == [range(1; $k + 1)]
      and ($vals | map("$" + (.n | tostring) + " = " + .v) | join(", ")) == $p
      and ([ range(0; $k) as $i | ($kinds[$i]) as $kind | ($vals[$i].v) as $v
             | if $kind == "uid" then $v == ($q + $target + $q)
               elif $kind == "people" or $kind == "requests" or $kind == "invitations" then ($v | test("^" + $q + "\\{([0-9]+(,[0-9]+)*)?\\}" + $q + "$"))
               else false end ] | all)')"
  # where it sits in the log: its backend, and the application's own statements around it
  read -r line pid dur _ < <(qc1_log_blocks "$log" | awk -F'\t' 'index($4, "SELECT class, action, count(*) FROM (SELECT action, ") == 1 { print $1, $2, $3; exit }') || true
  if [[ -n "${line:-}" ]]; then
    before="$(qc1_log_blocks "$log" | awk -F'\t' -v l="$line" -v p="$pid" '$1 < l && $2 == p && index($4, "UPDATE audit_logs SET ip = ") == 1 { c++ } END { print c + 0 }')"
    # After it: the first block its backend logged. It must be the facts file's successor statement, text for text (an altered one, another query that starts the same way, or a
    # different statement in between all fail), with the deleted account as its only parameter. And exactly one block in the whole log starts as the successor does.
    sfact="$(qc1_fact '.go.auditReport.successor' -j; printf x)"; sfact="${sfact%x}"
    read -r sline spid _ < <(qc1_log_blocks "$log" | awk -F'\t' -v l="$line" -v p="$pid" '$1 > l && $2 == p { print $1, $2; exit }') || true
    if [[ -n "${sline:-}" ]]; then
      sblock="$(awk -v s="$sline" 'NR < s { next } NR > s && /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9] [0-9:.]+ [A-Z]+ \[[0-9]+\] / { exit } { print }' "$log")"
      stext="$(qc1_block_query <<<"$sblock")"
      sparams="$(qc1_block_params <<<"$sblock")"
      [[ -n "$sfact" && "$stext" == "$sfact" ]] && stext_ok=true
      sfound=$stext_ok
    fi
    sfirst="${sfact%%$'\n'*}"; sfirst="${sfirst:0:90}"
    scount="$(qc1_log_blocks "$log" | awk -F'\t' -v m="$sfirst" 'm != "" && index($4, m) == 1 { c++ } END { print c + 0 }')"
  fi
  ssha="$(printf '%s' "${stext:-}" | sha256sum | cut -d' ' -f1)"
  succ_json="$(jq -nc --argjson found "$sfound" --argjson line "${sline:-null}" --arg pid "${spid:-}" --argjson text_ok "$stext_ok" --arg params "${sparams:-}" --argjson count "$scount" \
    --argjson bytes "$(printf '%s' "${stext:-}" | wc -c)" --arg sha "$ssha" \
    '{found: $found, line: $line, backendPid: $pid, textMatchesSource: $text_ok, parameters: $params, blocksInLog: $count, textBytes: $bytes, textSha256: $sha}')"
  sha="$(printf '%s' "$qtext" | sha256sum | cut -d' ' -f1)"
  bytes="$(printf '%s' "$qtext" | wc -c)"
  lockw="$(grep -cE 'still waiting for|acquired [A-Za-z]+Lock on .* after|avoided deadlock' "$log" || true)"; [[ "$lockw" =~ ^[0-9]+$ ]] || lockw=0
  samples="$({ grep -E 'still waiting for|acquired [A-Za-z]+Lock on .* after|avoided deadlock' "$log" || true; } | head -n 3 | cut -c1-200 | jq -Rsc 'split("\n") | map(select(length > 0))')"
  ev="$(jq -nc --argjson blocks "$n" --argjson prefix "$prefix_ok" --argjson suffix "$suffix_ok" --argjson order "$order_ok" --argjson parts "$parts_ok" --argjson param "$param_ok" \
    --argjson before "${before:-0}" --arg sha "$sha" --argjson bytes "$bytes" --arg pid "${pid:-}" --argjson rline "${line:-null}" --argjson target "$target" --arg params "$params" \
    --argjson pieces "$(printf '%s\n' "${pieces[@]}" | jq -Rsc 'split("\n")[:-1] | map(length)')" --argjson dur "${dur:-null}" --argjson lw "$lw" --argjson lockw "$lockw" --argjson samples "$samples" \
    --argjson tailok "$tail_ok" --argjson classok "$class_ok" --argjson kinds "$kinds_json" --argjson vals "$values_json" --argjson succ "$succ_json" \
    --argjson plan "$(printf '%s\n' "$block1" | qc1_plan_nodes)" '
    { statementBlocks: $blocks,
      identity: { prefixOk: $prefix, suffixOk: $suffix, piecesInOrder: $order, variablePartsNonEmpty: $parts, parametersAreTarget: $param, parameters: $params, parameterKinds: $kinds, parameterValues: ($vals | map(.v)), tailMatchesSource: $tailok, classificationOk: $classok, targetUserId: $target,
                  backendPid: $pid, reportBlocks: $blocks, reportLine: $rline, auditUpdatesBefore: $before, successor: $succ, queryTextBytes: $bytes, queryTextSha256: $sha, fixedPieceLengths: $pieces },
      durationMs: $dur,
      plan: $plan,
      rowLocks: false,
      rowLocksWhy: "a plain SELECT (no FOR UPDATE / FOR SHARE in its text): it takes an ACCESS SHARE lock on audit_logs only and locks no rows",
      lockWaits: { logged: $lw, count: $lockw, samples: $samples, note: "log_lock_waits was on while the log was written: a wait longer than deadlock_timeout (1 s) would be logged; an empty count means none was logged, not that no lock was ever briefly waited for" } }')"
  # proven and its proof text come from the one predicate the judge uses; a proof the predicate does not hold is said not to be one.
  jq -c --argjson t "$target" \
    --arg yes "the statement text is the Go source text (the fixed pieces, the subject-mention and malformed-link constants byte for byte, a classification of the source shape); it took the complete parameter list the source puts in it (the deleted account at every account position, id arrays at the array positions); it ran in the backend of the erasure own audit UPDATEs before it; and the first statement that backend logged after it is the application own conversion discovery (the Go source text, the deleted account as its only parameter), the only one in the log; the harness ran, re-ran and substituted nothing" \
    --arg no "NOT PROVEN: at least one of these does not hold (the flags beside this text say which): the statement text is the Go source text; the complete parameter list with the deleted account at every account position; the backend of the erasure own audit UPDATEs before it; the application own conversion discovery, the deleted account its only parameter, as the first statement that backend logged after it and the only one in the log. The judge marks such a row INVALID" \
    -f <(qc1_audit_identity_defs; printf '%s\n' '(audit_identity_ok(.identity; $t)) as $ok | .identity.proven = $ok | .identity.proof = (if $ok then $yes else $no end)') <<<"$ev"
}

# The scanner statement (initial | continuation) with its ? placeholders numbered $1, $2 ... (what GORM sends to PostgreSQL), so a generic plan is of the REAL
# statement, with its real projection, and not of a statement the harness wrote. Prints nothing when the facts file has none.
qc1_scanner_sql() { # initial|continuation
  local text
  text="$(qc1_fact '.go.scannerSQL[$k] // empty' --arg k "$1" 2>/dev/null)" || return 0
  awk '{
    s = $0; out = ""; n = 0
    while ((i = index(s, "?")) > 0) { n++; out = out substr(s, 1, i - 1) "$" n; s = substr(s, i + 1) }
    print out s
  }' <<<"$text"
}

# The LIMIT of each statement of a scanner unit read to its end, one number per line: reasonScanBatchFirst, then reasonScanBatchGrowth times as many, the last clipped so the
# sum is reasonUnitRows (internal/reasonscan.Batches is the same rule). A page that fills earlier sends a prefix.
# Fails when the facts file does not state the three numbers, so the harness never falls back to a schedule it made up.
qc1_scan_schedule() {
  local unit first growth
  unit="$(qc1_fact .go.reasonUnitRows 2>/dev/null || true)"; first="$(qc1_fact .go.reasonScanBatchFirst 2>/dev/null || true)"; growth="$(qc1_fact .go.reasonScanBatchGrowth 2>/dev/null || true)"
  [[ "$unit" =~ ^[0-9]+$ && "$first" =~ ^[0-9]+$ && "$growth" =~ ^[0-9]+$ ]] && (( unit >= 1 && first >= 1 && growth >= 1 )) || return 1
  awk -v u="$unit" -v f="$first" -v g="$growth" 'BEGIN { b = f; while (u > 0) { n = (b > u ? u : b); print n; u -= n; b *= g } }'
}

# Per logged execution of one statement kind: "<actual rows of the top node> <shared blocks hit+read of the top node>", one line each, in log order
# ("null" when a figure is not in the plan). This is the across-executions evidence (rows a unit returned, buffers a unit touched); it needs
# auto_explain.log_analyze and log_buffers, which the runner sets.
qc1_plan_executions() { # log which
  qc1_extract_plans "$1" "$2" | awk '
    function emit() { print (rows == "" ? "null" : rows), (blocks == "" ? "null" : blocks), (lim == "" ? "null" : lim) }
    /duration: .* plan:/ { if (inb) emit(); inb = 1; rows = ""; blocks = ""; lim = ""; next }
    inb && /^[ \t]*Query Parameters: / { v = $0; n = split(v, a, /\$[0-9]+ = /); v = a[n]; gsub(/[, \t]/, "", v); gsub("\047", "", v); lim = (v ~ /^-?[0-9]+$/ ? v : "") }
    inb && rows == "" && match($0, /actual time=[0-9.]+\.\.[0-9.]+ rows=[0-9]+/) { s = substr($0, RSTART, RLENGTH); sub(/.*rows=/, "", s); rows = s }
    inb && blocks == "" && /Buffers: shared/ { b = 0; line = $0; while (match(line, /(hit|read)=[0-9]+/)) { v = substr(line, RSTART, RLENGTH); sub(/.*=/, "", v); b += v; line = substr(line, RSTART + RLENGTH) } blocks = b }
    END { if (inb) emit() }'
}

# --- clearing the background rows between sizes -------------------------------------------------------------
# `TRUNCATE contact_requests` alone is refused by PostgreSQL while any table holds a foreign key to it
# (pilot: "cannot truncate a table referenced in a foreign key constraint", contact_grants). TRUNCATE is wanted, not
# DELETE, because a clean heap is what each size is measured on: deleting 10 000 to 1 000 000 rows would leave dead
# tuples, bloat and stale statistics in the very table the measurements read. So the whole referencing set (found in
# the catalog, transitively, so a later migration that adds one is covered) is truncated in ONE statement, and only
# after each referrer is proved empty: the fixture creates no grants or registrations, and a referrer that holds rows
# means the database is not the fixture's, so the loader stops instead of deleting someone's data. CASCADE is never used.
# psql_db is the caller's -At psql on the measured database.
qc1_contact_request_referrers() {
  psql_db -c "WITH RECURSIVE r(oid) AS (SELECT 'public.contact_requests'::regclass::oid UNION SELECT c.conrelid FROM pg_constraint c JOIN r ON c.confrelid = r.oid WHERE c.contype = 'f') SELECT oid::regclass::text FROM r ORDER BY 1"
}
qc1_reset_contact_requests() {
  local names t n list="" left left_full
  # Every SQL-only call below takes stdin from /dev/null, and the closure loop
  # reads the names on fd 3, never fd 0: docker exec -i forwards the caller's
  # stdin to the container, so a psql_db call inside this loop would otherwise
  # drain the remaining table names (the first count ate contact_requests and
  # registrations, the TRUNCATE listed only contact_grants, and the next size
  # measured over retained filler).
  names="$(qc1_contact_request_referrers </dev/null)" || { echo 'qc1: cannot list the tables that reference contact_requests' >&2; return 1; }
  grep -qx 'contact_requests' <<<"$names" || { echo 'qc1: contact_requests is not in the foreign-key closure the catalog returned' >&2; return 1; }
  while IFS= read -r t <&3; do
    [[ -n "$t" ]] || continue
    [[ "$t" =~ ^[a-z_][a-z0-9_]*$ ]] || { echo "qc1: refusing the unexpected table name '$t' (another schema or a quoted name)" >&2; return 1; }
    if [[ "$t" != contact_requests ]]; then
      n="$(psql_db -c "SELECT count(*) FROM $t" </dev/null)" || { echo "qc1: cannot count the rows of $t" >&2; return 1; }
      [[ "$n" =~ ^[0-9]+$ ]] || { echo "qc1: unreadable row count for $t: '$n'" >&2; return 1; }
      (( n == 0 )) || { echo "qc1: $t holds $n rows that depend on contact_requests; the loader will not delete them. This is not the QC-1 fixture database." >&2; return 1; }
    fi
    list+="${list:+, }$t"
  done 3<<<"$names"
  # One psql session truncates and then proves the table empty, so a writer landing
  # between two docker exec calls cannot hide behind the caller's bare "not empty":
  # a non-zero count fails here, naming the count. TRUNCATE prints its own
  # "TRUNCATE TABLE" line, so only the last line is the count (taken without a
  # pipe, so a TRUNCATE failure still fails here under any pipefail setting).
  left_full="$(psql_db -c "TRUNCATE $list; SELECT count(*) FROM contact_requests" </dev/null)" || { echo "qc1: TRUNCATE $list failed" >&2; return 1; }
  left="$(tail -n 1 <<<"$left_full")"
  [[ "$left" =~ ^[0-9]+$ ]] || { echo "qc1: unreadable row count for contact_requests after TRUNCATE: '$left'" >&2; return 1; }
  if (( left != 0 )); then
    qc1_reset_repopulated "$left" "$list" || true
    echo "qc1: contact_requests holds $left rows after TRUNCATE $list; the loader stops instead of measuring over another size's rows" >&2
    return 1
  fi
}

# Read-only diagnosis for a TRUNCATE that did not leave the table empty: the
# referrer counts plus the other backends on this database. Never writes. This
# names facts for the log; it does not attribute the rows to any writer. With
# the complete table list truncated and counted in one session, no writer can
# land between the statement and the count, so a non-zero count means the
# TRUNCATE list was incomplete or the database is not the fixture's. (Writers
# of contact_requests are only the request-creation API paths in api_contact.go;
# the scheduler's sweeps only expire or blank rows and only enqueue rows in
# jobs: none of that is claimed here as the cause.)
qc1_reset_repopulated() { # left list
  local left="$1" list="$2" t n acts jobs
  local rest="$list, "
  # $list is the validated comma-separated closure ("a, b, c"); walk it so a
  # future transitive referrer is named here too, not just the two known today.
  while [[ -n "$rest" ]]; do
    t="${rest%%, *}"; rest="${rest#*, }"
    [[ "$t" != contact_requests ]] || continue
    n="$(psql_db -c "SELECT count(*) FROM $t" </dev/null 2>/dev/null || echo '?')"
    echo "qc1: after TRUNCATE ($list): $t holds $n rows" >&2
  done
  acts="$(psql_db -c "SELECT coalesce(string_agg(left(pid::text || ' ' || coalesce(application_name,'?') || ' ' || state || ' ' || left(query, 120), 160), ' | '), '(none)') FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND datname = current_database()" </dev/null 2>/dev/null || echo '?')"
  echo "qc1: after TRUNCATE ($list): open backends: $acts" >&2
  jobs="$(psql_db -c "SELECT count(*) FROM jobs WHERE created_at > now() - interval '10 minutes'" </dev/null 2>/dev/null || echo '?')"
  echo "qc1: after TRUNCATE ($list): jobs enqueued in the last 10 minutes: $jobs" >&2
}

# --- what a deletion left behind, bound to THIS deletion -----------------------------------------------------------------
# The scenarios reuse request ids: every scenario's rows are dropped and rebuilt, so the odd ids 1, 3, 5, ... carry a different
# account's reasons each time while audit_logs keeps every earlier deletion's rows (target_id is the request id). Counting the decision
# audits of "these ids" therefore added up all earlier scenarios (pilot2: 20, 30, 130, 230 ... against 10, 10, 100 ... expected).
# A deletion is bound by a watermark: the largest audit id before it ran; only audits written after it count.
qc1_audit_count_sql() { # ids mark
  printf "SELECT count(*) FROM audit_logs WHERE id > %s AND action IN ('contact.reason_kept','contact.reason_removed','add.reason_kept','add.reason_removed') AND target_id IN (%s)" "$2" "$1"
}
qc1_outcome_sql() { # target ids mark  -> one row: deleted|blank|withText|audits
  [[ "${1:-}" =~ ^[0-9]+$ && "${3:-}" =~ ^[0-9]+$ && "${2:-}" =~ ^(-1|[0-9]+(,[0-9]+)*)$ ]] || return 1
  printf "SELECT coalesce((SELECT (deleted_at IS NOT NULL)::text FROM users WHERE id = %s), 'missing') || '|' ||\n (SELECT count(*) FROM contact_requests WHERE id IN (%s) AND reason = '') || '|' ||\n (SELECT count(*) FROM contact_requests WHERE id IN (%s) AND reason <> '') || '|' ||\n (%s);\n" \
    "$1" "$2" "$2" "$(qc1_audit_count_sql "$2" "$3")"
}
# Whether the row read back is the split the mode implies: account deleted, `removed` reasons blank, `kept` still with text, one decision audit each.
qc1_outcome_ok() { # deleted blank withText audits kept removed
  [[ "$1" == true && "$2" =~ ^[0-9]+$ && "$3" =~ ^[0-9]+$ && "$4" =~ ^[0-9]+$ ]] || return 1
  [[ "$2" == "$6" && "$3" == "$5" && "$4" == "$(( $5 + $6 ))" ]]
}

# --- the text of the reasons, byte for byte (#293) -------------------------------------------------------------------------------------
# A snapshot is one line per reason, in ascending id order, "id|sha256 of the UTF-8 bytes|runes|bytes". It is taken before a deletion (the account's
# non-blank reasons, by who the parties are) and again after it (the same ids, whatever the reason is now). Counting blank and non-blank rows, which
# qc1_outcome_ok does, cannot tell a reason that was kept intact from one that was kept mangled; comparing the lines can. The ids the server issues
# are ascending, and `mixed` removes the first ceil(n/2) of them (set-v1.mjs codesFor), so the first `removed` lines must now be blank and every
# other line must be unchanged.
qc1_text_snapshot_sql() { # target  -> the SQL of the before snapshot
  [[ "${1:-}" =~ ^[0-9]+$ ]] || return 1
  printf "SELECT id || '|' || encode(sha256(convert_to(reason, 'UTF8')), 'hex') || '|' || char_length(reason) || '|' || octet_length(reason) FROM contact_requests WHERE reason <> '' AND (requester_id = %s OR subject_user_id = %s) ORDER BY id;\n" "$1" "$1"
}
qc1_text_snapshot_ids_sql() { # ids  -> the SQL of the after snapshot (the same ids)
  [[ "${1:-}" =~ ^(-1|[0-9]+(,[0-9]+)*)$ ]] || return 1
  printf "SELECT id || '|' || encode(sha256(convert_to(reason, 'UTF8')), 'hex') || '|' || char_length(reason) || '|' || octet_length(reason) FROM contact_requests WHERE id IN (%s) ORDER BY id;\n" "$1"
}
# "ok|keptIntact|removedBlank|mismatched|missingOrExtra": ok is true only when the after snapshot names exactly the before snapshot's ids, the first
# `removed` of them are blank (no runes, no bytes) and every other line equals its before line.
qc1_text_exact() { # before-file after-file removed
  [[ "${3:-}" =~ ^[0-9]+$ ]] || return 1
  awk -F'|' -v removed="$3" -v pre="$1" '
    BEGIN { while ((getline line < pre) > 0) { n++; before[n] = line; split(line, f, "|"); id[n] = f[1] } close(pre) }
    { m++
      if (m > n || $1 != id[m]) { odd++; next }
      if (m <= removed) { if ($3 == "0" && $4 == "0") rem++; else bad++ }
      else if ($0 == before[m]) kept++
      else bad++ }
    END {
      n += 0; m += 0; odd += (m < n ? n - m : 0)
      ok = (bad + 0 == 0 && odd + 0 == 0 && removed + 0 <= n && rem + 0 == removed + 0 && kept + 0 == n - removed)
      printf "%s|%d|%d|%d|%d\n", (ok ? "true" : "false"), kept + 0, rem + 0, bad + 0, odd + 0 }' "$2"
}
# What a before snapshot says about the text, as a JSON object: how many reasons, the shortest and longest in runes and bytes. The longest in bytes over
# the longest in runes is the bytes a rune took (1 for ASCII); the maximum a UTF-8 rune can take is 4.
qc1_text_shape() { # before-file
  awk -F'|' '
    { n++; if (n == 1 || $3 + 0 < rmin) rmin = $3 + 0; if ($3 + 0 > rmax) rmax = $3 + 0; if (n == 1 || $4 + 0 < bmin) bmin = $4 + 0; if ($4 + 0 > bmax) bmax = $4 + 0; rsum += $3; bsum += $4 }
    END { if (n == 0) { print "null"; exit }
          printf "{\"rows\":%d,\"runesMin\":%d,\"runesMax\":%d,\"bytesMin\":%d,\"bytesMax\":%d,\"bytesPerRune\":%.3f}\n", n, rmin, rmax, bmin, bmax, bsum / rsum }' "$1"
}
# Of the first 20 reasons by id, how many runes are 2-, 3- and 4-byte UTF-8 and how many are combining marks (U+0300 to U+036F), as "w2 w3 w4 combining".
# A sample (20 reasons of up to 1,000 runes), not all of them: classifying 100,000 reasons with a regular expression each would take longer than the measurement.
qc1_text_classes_sql() { # target
  [[ "${1:-}" =~ ^[0-9]+$ ]] || return 1
  local tpl="SELECT coalesce(sum(length(regexp_replace(reason, '[^\u0080-\u07ff]', '', 'g'))), 0) || ' ' || coalesce(sum(length(regexp_replace(reason, '[^\u0800-\uffff]', '', 'g'))), 0) || ' ' || coalesce(sum(length(regexp_replace(reason, '[^\U00010000-\U0010ffff]', '', 'g'))), 0) || ' ' || coalesce(sum(length(regexp_replace(reason, '[^\u0300-\u036f]', '', 'g'))), 0) FROM (SELECT reason FROM contact_requests WHERE reason <> '' AND (requester_id = __T__ OR subject_user_id = __T__) ORDER BY id LIMIT 20) s;"
  printf '%s\n' "${tpl//__T__/$1}"
}
# The browser's DELETE body against the independent builder (set-v1.mjs `agree`) over the items the server issued before the browser opened the panel: the
# builder is given the browser's own choices and must reproduce its count, digest and choice vector byte for byte. Prints one JSON object; checked is false,
# and equal null, when there is nothing comparable (the browser sent no set-v1 review).
qc1_digest_agreement() { # items-file review-json-file
  qc1_setv1 agree --items "$1" --review "$2" || jq -nc '{checked:false, equal:null, why:"the comparison itself failed"}'
}
# The browser's final screen against the database (set-v1.mjs dom-ids and dom-text; the rules are domTextAgreement in response-accounting.mjs). panel-file is the panel's result
# (its `screen`), ids-file the JSON array of every request id in the order the server issued them, texts-file {id: text} read from the database for the ids qc1_dom_ids names, skip-file
# {id, oldText} for the one reason changed behind the page (or the word null). qc1_dom_ids prints nothing when the pager line does not name a whole page of those reasons; qc1_dom_text
# then reports that page as not matching, and prints {checked:false} only when no screen was captured or the comparison itself failed.
qc1_dom_ids() { # panel-file ids-file
  qc1_setv1 dom-ids --panel "$1" --ids "$2" --page "$(qc1_fact .web.displayPage)"
}
qc1_dom_text() { # panel-file ids-file texts-file skip-file
  qc1_setv1 dom-text --panel "$1" --ids "$2" --texts "$3" --skip "$4" --page "$(qc1_fact .web.displayPage)" || jq -nc '{checked:false, why:"the screen comparison itself failed"}'
}

# --- the app's peak memory over one operation --------------------------------------------------------------------------------
# VmHWM right after the reset is the baseline. The peak is the highest of the final VmHWM and every VmRSS / VmHWM sampled during the window,
# above that baseline. The kernel's high-water mark is not a monotone reading you can difference: RSS that falls without an unmap (file pages
# reclaimed under the container's memory limit) leaves the final VmHWM BELOW the one read just after the reset, and pilot2 judged such rows
# metric-missing although the app simply did not grow. That is a peak of 0, recorded with the flag; a figure is missing only when there is no
# baseline or nothing was read at all. Prints "peak_kb|null samples below_baseline".
qc1_app_peak() { # baseline_kb end_kb samples_file
  awk -v base="$1" -v endv="$2" '
    /^[0-9]+ [0-9]+$/ { n++; if ($1 > m) m = $1 + 0; if ($2 > m) m = $2 + 0 }
    END {
      e = (endv ~ /^[0-9]+$/); if (e && endv + 0 > m) m = endv + 0
      if (base !~ /^[0-9]+$/ || (n == 0 && !e)) { print "null", n + 0, "false"; exit }
      print (m > base + 0 ? m - base : 0), n + 0, ((e && endv + 0 < base + 0) ? "true" : "false")
    }' "$3"
}

# --- the deletion diagnostic (qc1-measure.sh --delete-probe) -----------------------------------------------------------------------------
# A one-off, diagnostic-only run beside the matrix: four deletions of a 100,000-reason multibyte subject (every reason removed) on ONE 1,000,000-row filler, in the
# order F, M, M, F. F leaves audit_logs as the run made it (fresh); M tops it up to the per-action counts seq 164 of the multibyte-spread matrix (revision bb3aaeeb)
# found, copied from the rows one real deletion wrote. It is never QC-1 evidence: the judge is unchanged (a probe 503 is judged like any 503), and what it
# shows is what these four requests did on this fixture, not why seq 164 timed out.
QC1_PROBE_ORDER=(F M M F)
QC1_PROBE_AUDIT_TARGET='{"user.created":140,"user.deleted":119,"add.reason_kept":705990,"add.reason_removed":605990,"user.erasure_reasons_viewed":18339}'
QC1_PROBE_SEQ164_AUDIT_BYTES=480305152
QC1_PROBE_TABLE_ROWS=1100000
# What the probe does not reproduce, named in the environment record and in every summary.
QC1_PROBE_DIFFERENCES='[
 "contact_requests is VACUUMed before every replicate: the matrix heap carried about 1.15M dead tuples by seq 167 and was never VACUUMed (no churn here)",
 "audit_logs is rebuilt for each replicate. M copies one real deletion'"'"'s audit rows per action (laid out one action after another, timestamps and target ids spread) up to seq 164'"'"'s counts; the matrix'"'"'s own heap, index and dead-row split, its 119 different deleted accounts and its aborted audit rows are not reproduced. Bytes are reported beside seq 164'"'"'s, not matched",
 "the M top-up is written after the traversal, so the traversal ran against an almost empty audit_logs",
 "one filler load and one template deletion serve all four replicates; the cache regime, host load, run position, app uptime, the earlier 10k and 100k sizes and the users and jobs table sizes are not reproduced"]'
QC1_PROBE_LIMITS='[
 "two replicates per arm on one host: this reports what these four requests did, not a rate, and not a cause, sufficiency or necessity",
 "statement times are cluster-wide pg_stat_statements counters: they belong to the deletion only when singleClient shows one client, no autovacuum and no scheduler overlap; a statement cancelled by a timeout is not counted, so a 503 can leave its slowest statement unseen; the harness text filter and LIMIT omit some statements outright, and ties and rounding make smallestListedMs approximate, not a strict bound",
 "auditStatementsMs sums only collected statements flagged from the full query text as touching audit_logs (the display is 200 characters); blkReadMs is time waiting on block reads, not CPU",
 "no host or cgroup counters are collected: a slow replicate cannot be told apart from a busy host except through blkReadMs and the single-client flags",
 "seq 164 stays a named operation failure whatever this shows; an arm that did not time out does not clear it"]'
qc1_probe_settings() {
  jq -nc --argjson target "$QC1_PROBE_AUDIT_TARGET" --argjson differences "$QC1_PROBE_DIFFERENCES" --arg order "${QC1_PROBE_ORDER[*]}" \
    '{diagnostic:true, order:($order | split(" ")), subject:"subject:100000:remove", auditTarget:$target, differences:$differences,
      note:"a diagnostic beside the matrix, never QC-1 evidence and never a matrix cell; see probe-summary.json"}'
}

# What a deletion that did not answer 200 left behind, from facts read AFTER it, one file each in a directory (text.pre, text.atom, account, audits). A file exists only
# when its read succeeded, so a failed or malformed read is "invalid:atomicity-unreadable" and never "nonAtomic": a product finding needs a clean read that says so.
#   text.pre/text.atom  qc1_text_snapshot lines of the account's reasons before and after (every one must still be as it was: nothing may have been blanked)
#   account             "notDeleted|emailUnchanged" of the subject ("true|true" when the erasure left it alone)
#   audits              "action|count" of the erasure's own audit actions written after the deletion's watermark (none may exist)
# Prints clean, nonAtomic:<what>[,<what>] or invalid:atomicity-unreadable. The scrubs of other accounts' audit rows are not read: a watermark cannot see them.
qc1_probe_atomicity() { # dir
  local d="$1" f ex what=()
  for f in text.pre text.atom account audits; do [[ -f "$d/$f" ]] || { echo invalid:atomicity-unreadable; return 0; }; done
  if [[ ! -s "$d/text.pre" ]] || grep -qvE '^[0-9]+\|[0-9a-f]{64}\|[0-9]+\|[0-9]+$' "$d/text.pre" "$d/text.atom" \
     || [[ "$(wc -l <"$d/account")" != 1 ]] || ! grep -qxE '(true|false)\|(true|false)' "$d/account" \
     || grep -qvE '^(user\.deleted|(contact|add)\.reason_(kept|removed))\|[0-9]+$' "$d/audits"; then
    echo invalid:atomicity-unreadable; return 0
  fi
  ex="$(qc1_text_exact "$d/text.pre" "$d/text.atom" 0)" || { echo invalid:atomicity-unreadable; return 0; }
  [[ "${ex%%|*}" == true ]] || what+=(reasons-changed)
  [[ "$(<"$d/account")" == 'true|true' ]] || what+=(account-changed)
  [[ ! -s "$d/audits" ]] || what+=(erasure-audits-written)
  if (( ${#what[@]} )); then echo "nonAtomic:$(IFS=,; echo "${what[*]}")"; else echo clean; fi
}

# The summary of a probe run, from measurements.jsonl alone: one object per replicate and a list of what was and was not measured. It decides nothing about cause.
# A replicate's class, the first that applies: invalid:<why> (no status; a competitor in the window; a refusal with no allow-listed code; a rollback that could not
# be read), stateUnmatched (an M run whose audit histogram at the start of the window is not the target), nonAtomic:<what>, completed, completedWrong (200 but the
# database or text is not as asked), timedOutAtomic (503 delete_timed_out, nothing left behind) and refused:<code>.
# Usability reuses the ordinary judge instead of cloning its thresholds: an instrument or environment finding the judge raises against a probe row (a failed peak
# reset, a sampler gap or starvation, missing metrics) makes that replicate unusable. The only waiver is the expected shape of a refused deletion with clean
# rollback evidence: its non-200 status and the refusal's outcome findings. Anything else the judge calls invalid stays unusable.
qc1_probe_summary() { # measurements.jsonl
  local judge_invalid cap
  cap="$(qc1_max_request_bytes 2>/dev/null || printf '0')"
  judge_invalid="$({ qc1_judge "$1" "$cap" 2>/dev/null || true; } | jq -sc 'map(select(type == "object" and .class == "invalid" and (.case | type) == "string") | {key: "\(.item)|\(.size)|\(.case)", value: .failure}) | group_by(.key) | map({key: .[0].key, value: map(.value)}) | from_entries' 2>/dev/null)"
  [[ "$judge_invalid" == '{'* ]] || judge_invalid='{}'
  jq -sc --argjson target "$QC1_PROBE_AUDIT_TARGET" --argjson seq164 "$QC1_PROBE_SEQ164_AUDIT_BYTES" --argjson budget "$QC1_DELETE_BUDGET_MS" --argjson tableRows "$QC1_PROBE_TABLE_ROWS" \
    --argjson differences "$QC1_PROBE_DIFFERENCES" --argjson limits "$QC1_PROBE_LIMITS" --argjson judgeInvalid "$judge_invalid" '
    def known: ["delete_timed_out", "reason_review_busy", "reason_review_changed", "restore_detected", "server_error"];
    def num($v): ($v | type) == "number";
    def competitors: [
      (if .delete.schedulerOverlap != false then "scheduler-overlap" else empty end),
      (if .delete.schedulerRows != 0 then "scheduler-rows" else empty end),
      (if (num(.delete.gateHolders) | not) or .delete.gateHolders > 1 then "gate-holders" else empty end),
      (if (num(.delete.maxActiveClients) | not) or .delete.maxActiveClients > 1 then "other-clients" else empty end),
      (if .delete.autovacuumWorkers != 0 then "autovacuum" else empty end),
      (if .workers != 0 then "workers" else empty end),
      (if .delete.tableRowsBefore != $tableRows then "table-rows" else empty end)];
    def klass:
      (.delete.status // "") as $s | (.diag.code // "none") as $c | (.diag.atomicity // "invalid:atomicity-unreadable") as $a | competitors as $comp |
      if ($s | test("^[1-5][0-9]{2}$") | not) then "invalid:no-status"
      elif ($comp | length) > 0 then "invalid:" + ($comp | join(","))
      elif $s != "200" and (known | index($c)) == null then "invalid:no-code"
      elif $s != "200" and ($a | startswith("invalid")) then $a
      elif .diag.arm == "M" and .delete.auditActionsBefore != $target then "stateUnmatched"
      elif $s != "200" and ($a | startswith("nonAtomic")) then $a
      elif $s != "200" and $a != "clean" then "invalid:atomicity-unreadable"
      elif $s == "200" then (if .outcome.dbOk == true and .outcome.textExact == true then "completed" else "completedWrong" end)
      elif $c == "delete_timed_out" then "timedOutAtomic"
      else "refused:" + $c end;
    def audit: [.delete.topStatements[]? | select(if (.isAudit | type) == "boolean" then .isAudit else (.query | test("audit_logs"; "i")) end)];
    def stmtsOk: ((.delete.topStatements | type) == "array" and ([.delete.topStatements[] | select((.totalMs | type) != "number" or (.blkReadMs | type) != "number")] | length) == 0);
    def want: ((.delete.ms // 0) / 100 | floor);
    def measInvalid:
      ((if (.delete.topStatements | type) != "array" then ["statements-unavailable"] else [] end)
       + (if (.delete.topStatements | type) == "array" and (stmtsOk | not) then ["statement-timing-missing"] else [] end)
       + (if (.delete.ms | type) != "number" then ["delete-ms-missing"]
          else ((if .delete.status == "200" and ((.delete.commitCalls // 0) < 1) then ["no-commit-seen"] else [] end)
                + (if (.delete.gateTicks // 0) < want then ["sampler-starved"] else [] end)
                + (if (.delete.dbMemSamples // 0) < ([2, want] | max) then ["memory-undersampled"] else [] end)) end));
    def judgeWaived:
      if .delete.status != "200" and .diag.atomicity == "clean" then ["status", "outcome-count", "outcome-db", "outcome-unverified"] else [] end;
    [.[] | select(.item == 6 and ((.case // "") | startswith("delete-subject-100000-remove-probe")))] | sort_by(.diag.replicate) as $rows
    | ($rows | map(. as $row
        | ($judgeInvalid[("\($row.item)|\($row.size)|\($row.case)")] // []) as $jf
        | (measInvalid + (($jf - judgeWaived) | map("judge:" + .))) as $mi
        | {replicate: .diag.replicate, arm: .diag.arm, case: .case, class: klass,
        status: .delete.status, code: .diag.code, requestMs: .delete.ms, budgetMs: $budget,
        auditMatchesSeq164Counts: (if .diag.arm == "M" then (.delete.auditActionsBefore == $target) else null end),
        auditActionsBefore: .delete.auditActionsBefore, auditRowsBefore: .delete.auditRowsBefore, auditBytesBefore: .delete.auditBytesBefore,
        auditBytesVsSeq164: (if num(.delete.auditBytesBefore) then ((.delete.auditBytesBefore / $seq164) * 1000 | round / 1000) else null end),
        auditScan: .diag.audit,
        statementsAvailable: ((.delete.topStatements | type) == "array"),
        measurementInvalid: $mi,
        usable: (((klass == "completed" or klass == "timedOutAtomic") or (klass | startswith("refused:"))) and ($mi | length) == 0),
        statements: {topN: (if stmtsOk then (.delete.topStatements | length) else null end),
                     auditStatementsMs: (if stmtsOk then ([audit[].totalMs] | add // null) else null end),
                     auditStatementsBlkReadMs: (if stmtsOk then ([audit[].blkReadMs] | add // null) else null end),
                     allTopBlkReadMs: (if stmtsOk then ([.delete.topStatements[]?.blkReadMs] | add // null) else null end),
                     smallestListedMs: (if stmtsOk and (.delete.topStatements | length) > 0 then .delete.topStatements[-1].totalMs else null end),
                     note: "cluster-wide pg_stat_statements over the window, not attributed to the deletion; auditStatements* cover only collected statements flagged from the full query text (the display is 200 characters); an eligible collected statement not listed took about smallestListedMs or less (ties and rounding are approximate; the harness text filter and cancelled statements are absent outright)"},
        singleClient: {maxActiveClients: .delete.maxActiveClients, gateHolders: .delete.gateHolders, autovacuumWorkers: .delete.autovacuumWorkers, schedulerOverlap: .delete.schedulerOverlap, schedulerRows: .delete.schedulerRows, workers: .workers},
        atomicity: .diag.atomicity, physicalBefore: .diag.physical, startedMs: .diag.startedMs, endedMs: .diag.endedMs})) as $reps
    | {schema: "fold/qc1-delete-probe-summary/v1", diagnostic: true,
       label: "audit-count-matched (M) against fresh (F) audit_logs, churn-free 1M multibyte fixture; never QC-1 evidence",
       order: ($reps | map(.arm)), incomplete: (($reps | length) != 4), replicates: $reps,
       byArm: {F: ($reps | map(select(.arm == "F") | .class)), M: ($reps | map(select(.arm == "M") | .class))},
       differences: $differences, limits: $limits}'  "$1"
}

# Whether a collected diagnostic directory is fit to read: row coverage alone is
# not enough. Prints ready, or not-ready:<reasons>. Each finalized record must
# be one typed object, and the saved summary and status are recomputed from the
# raw measurements with the functions that wrote them and must match, so a
# stale, edited or fabricated record cannot pass. The four replicates must be
# exactly F,M,M,F in order with every one usable, and the directory must be the
# finished qc1 step of the scale record beside it at the same revision. Only the
# deliberate diagnostic omissions (the matrix the probe never runs) and the
# expected refusal findings of a non-200 with clean rollback stay allowed; a
# stopped or interrupted run, a failed copy, an aborted or unfinished
# finalization and any other invalid finding stay fatal. The scale step's own
# verdict is not judged here: the probe wrapper exits non-zero by construction,
# so the matrix verdict of that step is fail while its diagnostic records are
# what this gate reads. The by-construction gaps (no browser, counts 0, dense
# none, an incomplete matrix) are in settings.deleteProbe and the summary's
# differences, not failures here.
qc1_probe_readiness() { # dir [parent-evidence.json]
  local d parent gaps n rows4 measured_ok recomputed status2 sizes counts dense cap rev penv match dart art v a r
  d="${1%/}"
  parent="${2:-}"
  gaps=()
  measured_ok=0
  match=0
  [[ -n "$parent" ]] || parent="$(dirname "$d")/evidence.json"
  [[ -f "$d/probe-summary.json" ]] || { echo 'not-ready:no-probe-summary'; return 0; }
  [[ -f "$d/measurements.jsonl" ]] || gaps+=('missing:measurements.jsonl')
  [[ -f "$d/environment.json" ]] || gaps+=('missing:environment.json')
  [[ -f "$d/evidence-status.json" ]] || gaps+=('missing:evidence-status.json')
  if [[ -f "$d/environment.json" ]] && [[ "$(jq -c 'type' "$d/environment.json" 2>/dev/null)" != '"object"' ]]; then gaps+=('environment:unreadable'); fi
  if [[ -f "$d/evidence-status.json" ]] && [[ "$(jq -c 'type' "$d/evidence-status.json" 2>/dev/null)" != '"object"' ]]; then gaps+=('status:unreadable'); fi
  if [[ -f "$d/probe-summary.json" ]] && [[ "$(jq -c 'type' "$d/probe-summary.json" 2>/dev/null)" != '"object"' ]]; then gaps+=('summary:unreadable'); fi
  # The finalized mark is a typed boolean: a string "false", null or a missing
  # field must not read as finished.
  if [[ -f "$d/environment.json" ]] && [[ "$(jq -r 'if type == "object" and .incomplete == false then "finalized" else "open" end' "$d/environment.json" 2>/dev/null)" != finalized ]]; then gaps+=('environment:incomplete'); fi
  if [[ -f "$d/measurements.jsonl" ]]; then
    if n="$(jq -sc 'length' "$d/measurements.jsonl" 2>/dev/null)"; then
      rows4="$(jq -sc '[.[] | select(type == "object" and .item == 6 and ((.case // "") | startswith("delete-subject-100000-remove-probe")))] | length' "$d/measurements.jsonl" 2>/dev/null || echo -1)"
      if [[ "$n" == 4 && "$rows4" == 4 ]]; then measured_ok=1; else gaps+=('measurements:changed'); fi
    else gaps+=('measurements:unreadable'); fi
  fi
  if [[ -f "$d/probe-summary.json" ]] && [[ "$(jq -c 'type' "$d/probe-summary.json" 2>/dev/null)" == '"object"' ]]; then
    [[ "$(jq -r 'if .incomplete == false then "closed" else "open" end' "$d/probe-summary.json" 2>/dev/null)" == closed ]] || gaps+=('incomplete:rows')
    [[ "$(jq -c '.order' "$d/probe-summary.json" 2>/dev/null)" == '["F","M","M","F"]' ]] || gaps+=('summary:order')
    [[ "$(jq -c '[(.replicates // [])[] | [.replicate, .arm]]' "$d/probe-summary.json" 2>/dev/null)" == '[[1,"F"],[2,"M"],[3,"M"],[4,"F"]]' ]] || gaps+=('summary:replicates')
    if recomputed="$(qc1_probe_summary "$d/measurements.jsonl" 2>/dev/null)" && [[ -n "$recomputed" ]]; then
      [[ "$(jq -S -c . "$d/probe-summary.json" 2>/dev/null)" == "$(jq -S -c . <<<"$recomputed" 2>/dev/null)" ]] || gaps+=('summary:mismatch')
    else gaps+=('summary:mismatch'); fi
    while IFS= read -r r; do gaps+=("replicate-$r"); done < <(jq -r '.replicates[]? | select(.usable != true) | "\(.replicate // "?"):\(.class // "unknown")"' "$d/probe-summary.json" 2>/dev/null)
  fi
  if [[ -f "$d/evidence-status.json" ]] && [[ "$(jq -c 'type' "$d/evidence-status.json" 2>/dev/null)" == '"object"' ]] \
    && [[ -f "$d/environment.json" ]] && [[ -f "$d/measurements.jsonl" ]]; then
    sizes="$(jq -r '.settings.sizes // ""' "$d/environment.json" 2>/dev/null)"
    counts="$(jq -r '.settings.counts // ""' "$d/environment.json" 2>/dev/null)"
    dense="$(jq -r '.settings.denseCounts // ""' "$d/environment.json" 2>/dev/null)"
    cap="$(jq -c '.maxRequestBytes // 0' "$d/environment.json" 2>/dev/null)"
    rev="$(jq -r '.revision // ""' "$d/environment.json" 2>/dev/null)"
    [[ "$dense" == none ]] && dense=""
    if [[ -s "$d/abort.json" ]]; then status2="$(qc1_evidence_status "$d/measurements.jsonl" "$sizes" "$counts" "$d/environment.json" "$rev" "$cap" "$dense" "$d/abort.json" 2>/dev/null)" || status2=""
    else status2="$(qc1_evidence_status "$d/measurements.jsonl" "$sizes" "$counts" "$d/environment.json" "$rev" "$cap" "$dense" 2>/dev/null)" || status2=""; fi
    if [[ -z "${status2:-}" ]]; then gaps+=('status:mismatch')
    else
      [[ "$(jq -S -c . "$d/evidence-status.json" 2>/dev/null)" == "$(jq -S -c . <<<"$status2" 2>/dev/null)" ]] || gaps+=('status:mismatch')
      # Only the expected refusal shape may be invalid: a probe row that did not
      # answer 200, with clean rollback evidence, may carry the non-200 status
      # and the refusal's outcome findings. Anything else invalid (a duplicate
      # row, an environment or settings disagreement, an instrument finding, a
      # refusal without a clean rollback) fails the read.
      if (( measured_ok )); then
        [[ "$(jq --slurpfile m "$d/measurements.jsonl" -c '
          ($m | map(select(type == "object") | {key: "\(.item)|\(.size)|\(.case)", value: {st: (.delete.status // ""), at: (.diag.atomicity // "")}}) | from_entries) as $rows
          | [.invalid[]? | (.item // null) as $i | (.size // null) as $z | (.case // "") as $c | (.failure // "") as $f
            | select($i != 6
              or ($c | startswith("delete-subject-100000-remove-probe") | not)
              or ((["status", "outcome-count", "outcome-db", "outcome-unverified"] | index($f)) | not)
              or (($rows["\($i)|\($z)|\($c)"] // null) | not)
              or $rows["\($i)|\($z)|\($c)"].st == "200"
              or $rows["\($i)|\($z)|\($c)"].at != "clean")] | length' "$d/evidence-status.json" 2>/dev/null)" == 0 ]] || gaps+=('status:unexpected-invalid')
      fi
    fi
  fi
  if [[ ! -f "$parent" ]]; then gaps+=('outer:missing')
  elif [[ "$(jq -c 'type' "$parent" 2>/dev/null)" != '"object"' ]]; then gaps+=('outer:unreadable')
  else
    rev=""
    [[ -f "$d/environment.json" ]] && rev="$(jq -r '.revision // ""' "$d/environment.json" 2>/dev/null)"
    penv="$(jq -r '.revision // ""' "$parent" 2>/dev/null)"
    if [[ -z "$rev" || -z "$penv" || "$rev" != "$penv" ]]; then gaps+=('outer:revision-mismatch')
    else
      dart="$(cd "$d" 2>/dev/null && pwd -P || printf '%s' "$d")"
      while IFS=$'\t' read -r v a; do
        [[ -n "$a" ]] || continue
        art="$(cd "$a" 2>/dev/null && pwd -P || printf '%s' "$a")"
        if [[ "$art" == "$dart" ]]; then
          match=1
          [[ "$v" == interrupted ]] && gaps+=('outer:interrupted')
          break
        fi
      done < <(jq -r '.steps[]? | select(.name == "qc1") | "\(.verdict // "")\t\(.artifacts // "")"' "$parent" 2>/dev/null)
      (( match )) || gaps+=('outer:unbound')
    fi
  fi
  [[ ! -e "$d/abort.json" ]] || gaps+=('aborted')
  [[ ! -e "$d/COLLECTION-FAILED.txt" ]] || gaps+=('collection-failed')
  if (( ${#gaps[@]} )); then echo "not-ready:$(IFS=,; echo "${gaps[*]}")"; else echo ready; fi
}
