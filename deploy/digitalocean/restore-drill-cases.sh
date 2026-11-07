#!/usr/bin/env bash
# Additional real-stack cases, sourced only by restore-drill.sh. Functions
# use that runner's disposable project, archive, API and artifact directory.
# shellcheck disable=SC2034,SC2154,SC2016 # Shared runner globals; jq/child-shell programs.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then echo 'source from restore-drill.sh only' >&2; exit 1; fi

# One writer owns progress. Background fault holders inherit the case/step
# labels for command history, but never overwrite the runner's status file.
drill_progress_init() {
  (( $#>0 )) || return 1
  DRILL_PROGRESS_OWNER="$BASHPID"; DRILL_PROGRESS_START=$SECONDS
  DRILL_PROGRESS_CASE_START=$SECONDS; DRILL_PROGRESS_STEP_START=$SECONDS
  DRILL_PROGRESS_STEP_INDEX=0; DRILL_PROGRESS_STEP_TOTAL=0
  DRILL_PROGRESS_PLAN="$(printf '%s\n' "$@" | jq -Rn '[inputs]')"
  jq -e 'length==(unique|length)' <<<"$DRILL_PROGRESS_PLAN" >/dev/null || return 1
  DRILL_PROGRESS_DONE='[]'; DRILL_PROGRESS_PENDING=""; DRILL_PROGRESS_OPERATION=""
  export RESTORE_DRILL_CASE="" RESTORE_DRILL_STEP=""
}
drill_emit() {
  [[ "$BASHPID" == "${DRILL_PROGRESS_OWNER:-}" ]] || return 0
  local event="$1" rc="${2:-0}" at record status=running
  at="$(date -u +%FT%TZ)"
  [[ "$event" != failed ]] || status=failed
  if [[ "$event" == result ]]; then
    status=failed
    if (( rc==0 )) && [[ "$(jq -c . <<<"$DRILL_PROGRESS_DONE")" == "$(jq -c . <<<"$DRILL_PROGRESS_PLAN")" ]]; then status=complete; fi
  fi
  record="$(jq -cn --arg at "$at" --arg event "$event" --arg status "$status" \
    --arg caseId "$RESTORE_DRILL_CASE" --arg step "$RESTORE_DRILL_STEP" --arg operation "$DRILL_PROGRESS_OPERATION" \
    --argjson rc "$rc" --argjson elapsed "$((SECONDS-DRILL_PROGRESS_START))" \
    --argjson caseElapsed "$((SECONDS-DRILL_PROGRESS_CASE_START))" --argjson stepElapsed "$((SECONDS-DRILL_PROGRESS_STEP_START))" \
    --argjson stepIndex "$DRILL_PROGRESS_STEP_INDEX" --argjson stepTotal "$DRILL_PROGRESS_STEP_TOTAL" \
    --argjson planned "$DRILL_PROGRESS_PLAN" --argjson completed "$DRILL_PROGRESS_DONE" \
    '{at:$at,event:$event,status:$status,exitCode:$rc,case:$caseId,step:$step,operation:$operation,
      elapsedSeconds:$elapsed,caseSeconds:$caseElapsed,stepSeconds:$stepElapsed,stepIndex:$stepIndex,stepTotal:$stepTotal,
      completedCases:$completed,remainingCases:($planned-$completed)}')" || return 1
  printf 'drill-progress at=%s elapsed=%ss case=%s step=%s index=%s/%s event=%s operation=%s\n' \
    "$at" "$((SECONDS-DRILL_PROGRESS_START))" "$RESTORE_DRILL_CASE" "$RESTORE_DRILL_STEP" "$DRILL_PROGRESS_STEP_INDEX" "$DRILL_PROGRESS_STEP_TOTAL" "$event" "$DRILL_PROGRESS_OPERATION" >&2
  if [[ -d "${WORK:-}" ]]; then
    [[ -z "$DRILL_PROGRESS_PENDING" ]] || printf '%s\n' "$DRILL_PROGRESS_PENDING" >>"$WORK/case-progress.jsonl" || return 1
    DRILL_PROGRESS_PENDING=""
    printf '%s\n' "$record" >>"$WORK/case-progress.jsonl" || return 1
    printf '%s\n' "$record" >"$WORK/.case-progress.$BASHPID.tmp" || return 1
    mv "$WORK/.case-progress.$BASHPID.tmp" "$WORK/case-progress.current.json" || return 1
  else
    # Bootstrap must create a new directory itself. Keep its first event in
    # memory until it exists, while publishing the start to the outer log.
    DRILL_PROGRESS_PENDING+="${DRILL_PROGRESS_PENDING:+$'\n'}$record"
  fi
}
drill_case_begin() {
  [[ "$BASHPID" == "${DRILL_PROGRESS_OWNER:-}" ]] || return 0
  local next
  [[ -z "$RESTORE_DRILL_CASE" ]] || { echo 'previous drill case was not completed' >&2; return 1; }
  next="$(jq -r --argjson completed "$DRILL_PROGRESS_DONE" '.[$completed|length]' <<<"$DRILL_PROGRESS_PLAN")"
  [[ "$1" == "$next" ]] || { echo "drill case order differs from coverage plan: expected $next, got $1" >&2; return 1; }
  export RESTORE_DRILL_CASE="$1" RESTORE_DRILL_STEP=""
  DRILL_PROGRESS_CASE_START=$SECONDS; DRILL_PROGRESS_STEP_START=$SECONDS; DRILL_PROGRESS_OPERATION=""
  DRILL_PROGRESS_STEP_INDEX=0; DRILL_PROGRESS_STEP_TOTAL=0
  drill_emit case-start
}
drill_step() {
  [[ "$BASHPID" == "${DRILL_PROGRESS_OWNER:-}" ]] || return 0
  [[ -z "$RESTORE_DRILL_STEP" ]] || drill_emit step-end || return 1
  export RESTORE_DRILL_STEP="$1"
  DRILL_PROGRESS_STEP_INDEX=$((DRILL_PROGRESS_STEP_INDEX+1)); DRILL_PROGRESS_STEP_TOTAL="${2:-0}"
  (( DRILL_PROGRESS_STEP_TOTAL==0 || DRILL_PROGRESS_STEP_INDEX<=DRILL_PROGRESS_STEP_TOTAL )) || return 1
  DRILL_PROGRESS_STEP_START=$SECONDS; DRILL_PROGRESS_OPERATION=""
  drill_emit step-start
}
drill_case_end() {
  [[ "$BASHPID" == "${DRILL_PROGRESS_OWNER:-}" ]] || return 0
  [[ -n "$RESTORE_DRILL_CASE" ]] || return 1
  [[ -z "$RESTORE_DRILL_STEP" ]] || drill_emit step-end || return 1
  DRILL_PROGRESS_DONE="$(jq -c --arg caseId "$RESTORE_DRILL_CASE" '.+[$caseId]' <<<"$DRILL_PROGRESS_DONE")"
  drill_emit case-end
  export RESTORE_DRILL_CASE="" RESTORE_DRILL_STEP=""
  DRILL_PROGRESS_OPERATION=""
}
drill_note() {
  [[ "$BASHPID" == "${DRILL_PROGRESS_OWNER:-}" ]] || return 0
  DRILL_PROGRESS_OPERATION="$1"
  drill_emit operation
}
drill_progress_error() {
  local line="$1" command="$2" rc="$3"
  drill_emit failed "$rc" || true
  printf 'restore drill failed at line %s: %s\n' "$line" "$command" >&2
  return "$rc"
}
drill_progress_complete() {
  [[ "$BASHPID" == "${DRILL_PROGRESS_OWNER:-}" ]] || return 0
  [[ -z "$RESTORE_DRILL_CASE" && "$(jq -c . <<<"$DRILL_PROGRESS_DONE")" == "$(jq -c . <<<"$DRILL_PROGRESS_PLAN")" ]] || {
    echo 'drill coverage plan is incomplete; refusing success' >&2; return 1;
  }
}

# Destructive fixture reset only; never called by the operator tooling.
# Materialize and validate the census before dropping anything: exec -T keeps
# stdin attached and can otherwise consume the next names in a read loop.
reset_fixture_databases() {
  local catalog remaining name
  local -a names=()
  [[ "$DIR" == "$WORK/site" && "$PROJECT" == fold-deploy-drill-* ]] || {
    echo 'fixture database reset refuses a non-drill directory/project' >&2; return 1;
  }
  [[ "${DEPLOY_DRILL:-}" == 1 ]] || { echo 'fixture database reset requires DEPLOY_DRILL=1' >&2; return 1; }
  validate_deploy_drill "$DIR" || return 1
  catalog="$(pg postgres -c "SELECT datname FROM pg_database WHERE datname='fold' OR datname ~ '^fold_(pre_)?restore_[0-9a-f]{16}$' ORDER BY datname" </dev/null)" || return 1
  [[ -z "$catalog" ]] || mapfile -t names <<<"$catalog"
  for name in "${names[@]}"; do
    [[ "$name" =~ ^fold(_(pre_)?restore_[0-9a-f]{16})?$ ]] || {
      echo "unexpected database in fixture reset census: $name" >&2; return 1;
    }
  done
  for name in "${names[@]}"; do
    pg postgres -c "ALTER DATABASE \"$name\" WITH ALLOW_CONNECTIONS false" \
      -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$name'" \
      -c "DROP DATABASE \"$name\"" </dev/null || return 1
  done
  remaining="$(pg postgres -c "SELECT datname FROM pg_database WHERE datname='fold' OR datname ~ '^fold_(pre_)?restore_[0-9a-f]{16}$' ORDER BY datname" </dev/null)" || return 1
  [[ -z "$remaining" ]] || { printf 'fixture reset left databases; no new case may start:\n%s\n' "$remaining" >&2; return 1; }
}

# The direct /fold probe can pass before Docker runs its first scheduled
# healthcheck. Establish the exact reuse precondition without replacing the
# captured container or assuming a fixed startup duration.
wait_recovery_healthy() {
  local expected="$1" limit="${2:-90}" end state last=""
  [[ -n "$expected" && "$expected" != *$'\n'* && "$limit" =~ ^[1-9][0-9]{0,2}$ ]] || return 1
  end=$((SECONDS+limit))
  while (( SECONDS<end )); do
    [[ "$(recovery_ids)" == "$expected" ]] || { echo 'recovery identity changed while waiting for healthy reuse' >&2; return 1; }
    state="$(docker inspect --format '{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$expected" </dev/null)" || return 1
    if [[ "$state" != "$last" ]]; then printf 'drill: recovery %s health=%s\n' "$expected" "$state"; last="$state"; fi
    case "$state" in
      'true|healthy')
        docker exec "$expected" /fold healthcheck </dev/null || { echo 'recovery direct probe failed before reuse test' >&2; return 1; }
        [[ "$(recovery_ids)" == "$expected" ]] || { echo 'recovery identity changed after healthy probe' >&2; return 1; }
        return 0 ;;
      'true|starting') ;;
      *) echo "recovery is not a running healthy candidate: $state" >&2; return 1 ;;
    esac
    sleep .2
  done
  echo "recovery $expected did not become Docker-healthy within ${limit}s; reuse precondition not established" >&2
  return 1
}
assert_recovery_clients_present() {
  local count
  count="$(pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE datname='fold' AND backend_type='client backend' AND pid<>pg_backend_pid()" </dev/null)" || return 1
  [[ "$count" =~ ^[0-9]+$ && "$count" -gt 0 ]] || { echo 'recovery reuse fixture requires established client backends' >&2; return 1; }
  printf 'drill: established recovery client backends=%s\n' "$count"
}
# Keep healthy-reuse setup and its proof together. A direct HTTP or /fold
# probe alone does not make a freshly created container Docker-healthy.
assert_healthy_recovery_reuse() {
  local expected="$1" artifact="$2"
  [[ "$artifact" =~ ^[a-z0-9-]+$ ]] || return 1
  wait_recovery_healthy "$expected" | tee "$WORK/$artifact.health" || return 1
  assert_recovery_clients_present | tee -a "$WORK/$artifact.health" || return 1
  if ! restore --resume >"$WORK/$artifact.log" 2>&1; then
    cat "$WORK/$artifact.log" >&2; return 1
  fi
  [[ "$(recovery_ids)" == "$expected" ]] || {
    echo 'healthy recovery resume replaced the captured container' >&2
    cat "$WORK/$artifact.log" >&2; return 1;
  }
  if grep -q 'observed W=0' "$WORK/$artifact.log"; then
    echo 'healthy recovery reuse checked W=0 despite its own established clients' >&2; return 1
  fi
}

wait_file() {
  local file="$1" pattern="$2" end=$((SECONDS+90))
  drill_note "barrier/${file##*/}"
  while (( SECONDS<end )); do
    if [[ -f "$file" ]] && grep -q -- "$pattern" "$file"; then return 0; fi
    sleep .05
  done
  echo "barrier not reached: $file/$pattern" >&2; return 1
}
wait_holder() {
  local rc=0
  timeout 300 tail --sleep-interval=.05 --pid="$holder" -f /dev/null || return 1
  wait "$holder" || rc=$?
  holder=""
  return "$rc"
}
release_barrier() {
  local rc=0
  timeout 10 bash -c 'printf "continue\n" >"$1"' _ "$DIR/drill.fifo"
  wait_holder || rc=$?
  rm -f "$DIR/drill.ready"
  return "$rc"
}
start_paused() {
  local point="$1"; shift
  rm -f "$DIR/drill.ready"
  [[ -p "$DIR/drill.fifo" ]] || mkfifo "$DIR/drill.fifo"
  env RESTORE_DRILL_OPERATION=1 RESTORE_DRILL_PAUSE="$point" "$DIR/restore.sh" --deploy-dir "$DIR" --drill "$@" >"$WORK/paused-$point.log" 2>&1 &
  holder=$!
  wait_file "$DIR/drill.ready" "^$point$"
}
kill_holder() {
  kill -KILL "$holder"; wait "$holder" 2>/dev/null || true; holder=""
  timeout 15 flock -x "$DIR/ops.lock" true
  rm -f "$DIR/drill.ready"
}
# A separate, identified SQL session holds the requested lock until its FIFO
# is released. Readiness comes from psql after the statement, never a sleep.
sql_hold() {
  local label="$1" database="$2" statement="$3"
  mkdir -p "$WORK/holds"
  rm -f "$WORK/holds/$label.ready" "$WORK/holds/$label.fifo"
  mkfifo "$WORK/holds/$label.fifo"
  (
    exec 8<>"$WORK/holds/$label.fifo"
    {
      printf 'BEGIN;\nSELECT pg_backend_pid() AS drill_holder_pid \\gset\n%s;\n\\echo SQL-READY|:drill_holder_pid\n' "$statement"
      IFS= read -r -t 180 token <&8 && [[ "$token" == continue ]] || exit 1
      printf 'COMMIT;\n'
    } | dc exec -T db psql -X -U church -d "$database" -At -v ON_ERROR_STOP=1
  ) >"$WORK/holds/$label.ready" 2>&1 &
  SQL_HOLDER=$!; SQL_HOLD_LABEL="$label"
  wait_file "$WORK/holds/$label.ready" '^SQL-READY|'
  SQL_BACKEND="$(sed -n 's/^SQL-READY|\([0-9][0-9]*\)$/\1/p' "$WORK/holds/$label.ready")"
  [[ "$SQL_BACKEND" =~ ^[1-9][0-9]*$ ]] || return 1
}
sql_release() {
  timeout 10 bash -c 'printf "continue\n" >"$1"' _ "$WORK/holds/$1.fifo"
  timeout 60 tail --sleep-interval=.05 --pid="$SQL_HOLDER" -f /dev/null
  wait "$SQL_HOLDER"; SQL_HOLDER=""; SQL_HOLD_LABEL=""; SQL_BACKEND=""
}
assert_sql_holder() {
  local database="$1" count
  [[ "$database" =~ ^[a-z0-9_]+$ && "${SQL_HOLDER:-}" =~ ^[1-9][0-9]*$ && "${SQL_BACKEND:-}" =~ ^[1-9][0-9]*$ ]] || return 1
  kill -0 "$SQL_HOLDER" 2>/dev/null || { echo 'fixture SQL holder exited before explicit release' >&2; return 1; }
  count="$(pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE pid=$SQL_BACKEND AND datname='$database' AND backend_type='client backend'" </dev/null)" || return 1
  [[ "$count" == 1 ]] || { echo "fixture SQL holder $SQL_BACKEND is no longer connected to $database" >&2; return 1; }
  printf 'drill: SQL holder pid=%s still connected to %s\n' "$SQL_BACKEND" "$database"
}
# Do not mistake an unrelated waiter for the request under test, or spend
# the whole deadline waiting after curl already returned an HTTP error (or
# succeeded without blocking). The holder and waiter must share fold.
wait_delete_blocked() {
  local request_pid="$1" response="$2" kind="$3" limit="${4:-20}" predicate count end
  [[ "$request_pid" =~ ^[1-9][0-9]*$ && "${SQL_BACKEND:-}" =~ ^[1-9][0-9]*$ ]] || return 1
  case "$kind" in
    gate) predicate="wait_event='advisory' AND query LIKE 'SELECT pg_advisory_xact_lock%'" ;;
    request) predicate="query LIKE '%contact_requests%'" ;;
    *) return 1 ;;
  esac
  end=$((SECONDS+limit))
  while (( SECONDS<end )); do
    if ! kill -0 "$request_pid" 2>/dev/null; then
      echo "count-race $kind request exited before its SQL barrier; response follows" >&2
      cat "$response" >&2; return 1
    fi
    count="$(pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE datname='fold' AND backend_type='client backend' AND wait_event_type='Lock' AND $predicate AND $SQL_BACKEND=ANY(pg_blocking_pids(pid)) AND pid<>pg_backend_pid()" </dev/null)" || return 1
    [[ "$count" =~ ^[0-9]+$ ]] || return 1
    if (( count>0 )); then
      kill -0 "$request_pid" 2>/dev/null || { echo 'count-race request exited at its SQL barrier' >&2; cat "$response" >&2; return 1; }
      printf 'count-race %s: blocked in fold by holder pid=%s\n' "$kind" "$SQL_BACKEND"
      return 0
    fi
    sleep .05
  done
  echo "count-race $kind did not block on holder $SQL_BACKEND in fold; activity and response follow" >&2
  pg postgres -c "SELECT datname,pid,wait_event_type,wait_event,pg_blocking_pids(pid),query FROM pg_stat_activity WHERE backend_type='client backend' ORDER BY datname,pid" </dev/null >&2 || true
  cat "$response" >&2
  return 1
}
smtp_total() { smtp_json | jq -er .total; }
push_total() { dc logs --no-log-prefix push-sink 2>/dev/null | awk '/"POST / {n++} END {print n+0}'; }
job_claims_since() {
  pg fold -c "SELECT count(*) FROM jobs WHERE locked_at >= '$1'::timestamptz OR (attempts>0 AND updated_at>='$1'::timestamptz)"
}
# Monitor once per second for the entire interval. Phase and service facts are
# recorded together. It tolerates fold's deliberately closed rename interval,
# but no observed public access, mail/push send or worker claim while closed.
monitor_start() {
  rm -f "$WORK/monitor.stop" "$WORK/monitor.failed"
  (
    last_mail="$(smtp_total)"; last_push="$(push_total)"
    while [[ ! -f "$WORK/monitor.stop" ]]; do
      if [[ -f "$DIR/recovery.lock" ]]; then
        lock="$(cat "$DIR/recovery.lock")"
        stage="$(jq -er .phase <<<"$lock")"; closed="$(jq -r '.closedAt // empty' <<<"$lock")"
        case "$stage" in reopen-intent|app-start-intent|caddy-start-intent|smoke-intent|pre-drop-intent|pre-dropped|floor-written|cleanup-failed|abort-start-intent|abort-smoke-intent) private=0 ;; *) private=1 ;; esac
        if (( private )) && [[ -n "$closed" ]]; then
          if port_open "$PUBLIC_PORT"; then echo "public access in $stage" >>"$WORK/monitor.failed"; fi
          mail="$(smtp_total)"; push="$(push_total)"
          [[ "$mail" == "$last_mail" ]] || echo "mail delivered in $stage" >>"$WORK/monitor.failed"
          [[ "$push" == "$last_push" ]] || echo "push delivered in $stage" >>"$WORK/monitor.failed"
          if claims="$(job_claims_since "$closed" 2>/dev/null)"; then
            [[ "$claims" == 0 ]] || echo "worker claim in $stage after $closed" >>"$WORK/monitor.failed"
          fi
        fi
        printf '%s phase=%s closed=%s private=%s mail=%s push=%s\n' "$(date -u +%FT%TZ)" "$stage" "$closed" "$private" "$(smtp_total)" "$(push_total)"
      fi
      last_mail="$(smtp_total)"; last_push="$(push_total)"
      sleep 1
    done
  ) >>"$WORK/interval-monitor.log" 2>&1 &
  monitor=$!
}
monitor_stop() {
  touch "$WORK/monitor.stop"
  wait "$monitor"; monitor=""
  [[ ! -s "$WORK/monitor.failed" ]] || { cat "$WORK/monitor.failed" >&2; return 1; }
}
assert_no_claims() {
  local cutoff
  cutoff="$(jq -er .closedAt "$DIR/recovery.lock")"
  [[ "$(job_claims_since "$cutoff")" == 0 ]]
}
assert_sentinels() {
  local original restored name orig_name restored_name
  original="$(jq -er .origOid "$DIR/recovery.lock")"
  restored="$(jq -r '.restoreOid // empty' "$DIR/recovery.lock")"
  orig_name="$(pg postgres -c "SELECT datname FROM pg_database WHERE oid=$original")"
  [[ -n "$orig_name" ]]
  [[ "$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='$orig_name'")" == "$original" ]]
  if [[ "$orig_name" == fold ]]; then
    [[ "$(pg fold -c "SELECT value FROM drill_sentinel WHERE id=1")" == original ]]
  else
    [[ "$orig_name" == "$(nonce_db fold_pre_restore_)" ]]
    [[ "$(pg postgres -c "SELECT datallowconn FROM pg_database WHERE oid=$original")" == f ]]
    # Never reopen P merely to inspect it. Its physical table data is pinned
    # by the source logical dump taken just before the operation and by oid.
  fi
  if [[ -n "$restored" ]]; then
    restored_name="$(pg postgres -c "SELECT datname FROM pg_database WHERE oid=$restored")"
    if [[ -n "$restored_name" && "$(phase)" != collect-* ]]; then
      if [[ "$(pg "$restored_name" -c "SELECT to_regclass('drill_sentinel') IS NOT NULL")" == t ]]; then
        [[ "$(pg "$restored_name" -c "SELECT value FROM drill_sentinel WHERE id=1")" == original ]]
      fi
    fi
  fi
  pg postgres -c "SELECT datname,oid,datallowconn,pg_database_size(oid) FROM pg_database WHERE oid IN ($original,${restored:-0}) ORDER BY oid"
}
logical_snapshot() {
  local dest="$1" table catalog count
  local -a tables=()
  catalog="$(pg fold -c "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename" </dev/null)" || return 1
  [[ -z "$catalog" ]] || mapfile -t tables <<<"$catalog"
  for table in "${tables[@]}"; do [[ "$table" =~ ^[a-z0-9_]+$ ]] || return 1; done
  : >"$dest.counts" || return 1
  for table in "${tables[@]}"; do
    count="$(pg fold -c "SELECT count(*) FROM \"$table\"" </dev/null)" || return 1
    [[ "$count" =~ ^[0-9]+$ ]] || return 1
    printf '%s|%s\n' "$table" "$count" >>"$dest.counts" || return 1
  done
  dc exec -T db pg_dump -U church --data-only --no-owner --no-acl fold </dev/null | sed '/^--/d;/^\\restrict /d;/^\\unrestrict /d;/^$/d' >"$dest.data" || return 1
}

reopen_checked() {
  URL=http://localhost:18080; login
  [[ "$(pg fold -c "SELECT deleted_at IS NULL FROM users WHERE id=$A")" != t ]] || delete_a
  restore --reopen --checked-church-records
}
# mid-smoke and mid-abort-smoke must be a real held smoke process. Kill both
# the parent and the exact helper holding its inherited lock descriptor.
smoke_interruption() {
  local point="$1" mode="$2" rc=0 smoke_pid
  rm -f "$DIR/drill.ready"
  [[ -p "$DIR/drill.fifo" ]] || mkfifo "$DIR/drill.fifo"
  local -a smoke_args=("$mode")
  [[ "$mode" != --reopen ]] || smoke_args+=(--checked-church-records)
  env RESTORE_DRILL_OPERATION=1 RESTORE_DRILL_FAULT="$point" "$DIR/restore.sh" --deploy-dir "$DIR" --drill "${smoke_args[@]}" >"$WORK/fault-$point.log" 2>&1 &
  holder=$!
  wait_file "$DIR/drill.ready" "^$point|"
  smoke_pid="$(cut -d'|' -f2 "$DIR/drill.ready")"
  [[ "$smoke_pid" =~ ^[0-9]+$ && "$smoke_pid" != 1 ]]
  observe_services
  kill -KILL "$holder" "$smoke_pid"
  wait "$holder" 2>/dev/null || rc=$?
  [[ "$rc" == 137 ]]; holder=""
  timeout 15 flock -x "$DIR/ops.lock" true
  rm -f "$DIR/drill.ready"
}

count_races() {
  local gate=$((16#464f4c44434f4e)) delete_pid original_dump="$DB_DUMP" expected tombstoned
  for timing in before-stop during-stop; do
    drill_step "gate/$timing"
    reset_case
    # Advisory locks are database-scoped. Holding the same key in postgres
    # cannot block the application's transaction on fold.
    sql_hold gate fold "SELECT pg_advisory_lock($gate)"
    api DELETE "/admin/users/$C" '{}' >"$WORK/race-c-$timing.json" 2>&1 & delete_pid=$!
    wait_delete_blocked "$delete_pid" "$WORK/race-c-$timing.json" gate
    if [[ "$timing" == before-stop ]]; then
      sql_release gate; wait "$delete_pid"
      [[ "$(pg fold -c "SELECT deleted_at IS NOT NULL FROM users WHERE id=$C")" == t ]]
      RESTORE_DRILL_RACE_USER="$C" restore --dump "$DB_DUMP"
    else
      mkfifo "$WORK/stop.fifo"; touch "$WORK/stop-barrier"
      RESTORE_DRILL_OPERATION=1 RESTORE_DRILL_RACE_USER="$C" "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$DB_DUMP" >"$WORK/race-stop.log" 2>&1 & holder=$!
      wait_file "$WORK/stop.ready" stop-app
      sql_release gate
      wait "$delete_pid" || true # The server owns deletion beyond disconnect.
      timeout 10 bash -c 'echo continue >"$1"' _ "$WORK/stop.fifo"
      wait_holder
      rm -f "$WORK/stop.fifo" "$WORK/stop-barrier" "$WORK/stop.ready"
    fi
    tombstoned="$(cat "$WORK/collection-race-user")"
    expected=1; [[ "$tombstoned" != t ]] || expected=2
    [[ "$(jq -er .count "$DIR/recovery.lock")" == "$expected" ]]
    printf 'r1 timing=%s C-at-collection=%s count=%s\n' "$timing" "$tombstoned" "$expected"
  done
  drill_step straddling-audit
  # B's immutable audit at is transaction start. Keep its request row locked
  # until after the dump, proving a timestamp filter would miss this erasure.
  reset_case
  pg fold -c "INSERT INTO contact_requests(requester_id,subject_user_id,context_type,context_id,fields,needs_admin,admin_decision,expires_at) VALUES ($ADMIN,$B,'group',$GROUP,'email',false,'not_needed',now()+interval '1 day')"
  request="$(pg fold -c "SELECT max(id) FROM contact_requests WHERE subject_user_id=$B")"
  sql_hold request fold "SELECT id FROM contact_requests WHERE id=$request FOR UPDATE"
  api DELETE "/admin/users/$B" '{}' >"$WORK/race-b-delete.json" 2>&1 & delete_pid=$!
  wait_delete_blocked "$delete_pid" "$WORK/race-b-delete.json" request
  archive_started="$(pg postgres -c 'SELECT clock_timestamp()')"
  DB_DUMP="$WORK/straddling-b.dump"
  dump_current_fixture_archive "$DB_DUMP"
  sql_release request; wait "$delete_pid"
  pg fold -v started="$archive_started" -c "SELECT at FROM audit_logs WHERE action='user.deleted' AND target_id=$B ORDER BY at DESC LIMIT 1" >"$WORK/b-audit-start.txt"
  [[ "$(pg fold -c "SELECT max(at) < '$archive_started'::timestamptz FROM audit_logs WHERE action='user.deleted' AND target_id=$B")" == t ]]
  restore --dump "$DB_DUMP"
  [[ "$(jq -er .count "$DIR/recovery.lock")" == 1 ]]
  assert_private
  DB_DUMP="$original_dump"
  drill_step created-after-backup
  reset_case
  D="$(new_user 'After backup' after-backup@example.invalid)"
  api DELETE "/admin/users/$D" '{}' >/dev/null
  restore --dump "$DB_DUMP"
  [[ "$(jq -er .count "$DIR/recovery.lock")" == 1 ]]
  [[ "$(pg fold -c "SELECT count(*) FROM users WHERE id=$D")" == 0 ]]
  # A current database without users cannot establish the comparison. The
  # archive remains restorable, but reopening needs the church-records check.
  drill_step unknown-current
  reset_case; dc stop caddy app
  pg postgres -c 'DROP DATABASE fold' -c 'CREATE DATABASE fold OWNER church TEMPLATE template0'
  rc=0; restore --dump "$DB_DUMP" --reopen-if-none >"$WORK/unknown-auto-reopen.log" 2>&1 || rc=$?
  [[ "$rc" == 3 ]]; grep -q 'SITE IS CLOSED' "$WORK/unknown-auto-reopen.log"
  [[ "$(jq -er .count "$DIR/recovery.lock")" == unknown ]]
  expect_fail checked-church-records restore --reopen
  reopen_checked
}

# A dump made after reset_case would otherwise contain its live sentinel.
# Keep the same archive/live distinction as the base dump, and recapture the
# original heap only after that distinction has been restored in live fold.
# This writes fixture data only, never through the operator restore path.
dump_current_fixture_archive() {
  local destination="$1"
  [[ "$DIR" == "$WORK/site" && "$PROJECT" == fold-deploy-drill-* && "$destination" == "$WORK/"* ]] || return 1
  [[ "${DEPLOY_DRILL:-}" == 1 ]] || return 1
  validate_deploy_drill "$DIR" || return 1
  pg fold -c "UPDATE drill_sentinel SET value='archive' WHERE id=2" </dev/null >/dev/null || return 1
  [[ "$(pg fold -c 'SELECT value FROM drill_sentinel WHERE id=2' </dev/null)" == archive ]] || return 1
  dc exec -T db pg_dump -U church -Fc fold </dev/null >"$destination" || return 1
  capture_original || return 1
  [[ "$(pg fold -c 'SELECT value FROM drill_sentinel WHERE id=2' </dev/null)" == live ]] || return 1
}

capture_original() {
  local path oid digest live
  oid="$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='fold'")"
  pg fold -c "UPDATE drill_sentinel SET value='live' WHERE id=2" >/dev/null
  path="$(pg fold -c "SELECT pg_relation_filepath('drill_sentinel')")"
  [[ "$path" == "base/$oid/"* && "$path" =~ ^base/[0-9]+/[0-9]+$ ]]
  pg fold -c 'SELECT * FROM drill_sentinel ORDER BY id' >/dev/null
  pg fold -c CHECKPOINT >/dev/null
  digest="$(dc exec -T db sha256sum "/var/lib/postgresql/data/$path" | cut -d' ' -f1)"
  live="$(pg fold -c "SELECT to_regclass('drill_live_only') IS NOT NULL AND to_regclass('drill_live_view') IS NOT NULL")"
  jq -n --arg oid "$oid" --arg path "$path" --arg digest "$digest" --arg live "$live" '{oid:$oid,path:$path,digest:$digest,liveOnly:$live}' >"$WORK/original-heap.json"
}
assert_original_heap() {
  local oid path digest name clone
  oid="$(jq -er .oid "$WORK/original-heap.json")"; path="$(jq -er .path "$WORK/original-heap.json")"
  [[ "$oid" == "$(jq -er .origOid "$DIR/recovery.lock")" ]]
  digest="$(dc exec -T db sha256sum "/var/lib/postgresql/data/$path" | cut -d' ' -f1)"
  [[ "$digest" == "$(jq -er .digest "$WORK/original-heap.json")" ]]
  name="$(pg postgres -c "SELECT datname FROM pg_database WHERE oid=$oid")"
  if [[ "$name" == "$(nonce_db fold_pre_restore_)" ]]; then
    # Like template0, a closed database can be a CREATE DATABASE template.
    # Inspect a disposable clone, leaving P closed and byte-identical.
    clone="drill_inspect_$(jq -er '.nonce[0:16]' "$DIR/recovery.lock")"
    pg postgres -c "CREATE DATABASE \"$clone\" TEMPLATE \"$name\" OWNER church"
    [[ "$(pg "$clone" -c 'SELECT value FROM drill_sentinel WHERE id=2')" == live ]]
    if [[ "$(jq -er .liveOnly "$WORK/original-heap.json")" == t ]]; then
      [[ "$(pg "$clone" -c "SELECT to_regclass('drill_live_only') IS NOT NULL AND to_regclass('drill_live_view') IS NOT NULL")" == t ]]
    fi
    pg postgres -c "DROP DATABASE \"$clone\""
    [[ "$(pg postgres -c "SELECT datallowconn FROM pg_database WHERE oid=$oid")" == f ]]
  fi
}
assert_failed_interval() {
  local mail push
  assert_closed; assert_no_claims
  mail="$(smtp_total)"; push="$(push_total)"
  sleep 3
  assert_closed; assert_no_claims
  [[ "$(smtp_total)" == "$mail" && "$(push_total)" == "$push" ]]
}
archive_failures() {
  local size original_dump="$DB_DUMP" attempts old r
  reset_case
  printf 'PG' >"$WORK/truncated-list.dump"
  snapshot_state "$DIR" "$SOURCE" >"$WORK/truncated.before"
  expect_fail 'unreadable archive' restore --dump "$WORK/truncated-list.dump"
  [[ ! -f "$DIR/recovery.lock" ]]
  snapshot_state "$DIR" "$SOURCE" >"$WORK/truncated.after"
  cmp "$WORK/truncated.before" "$WORK/truncated.after"
  # pg_restore -l reads the TOC without extracting the compressed data. Find
  # a deterministic suffix cut whose TOC remains readable but restore fails.
  size="$(stat -c %s "$DB_DUMP")"; found=0
  for length in "$((size/2))" "$((size*3/4))" "$((size-1024))" "$((size-128))"; do
    (( length>0 )) || continue
    head -c "$length" "$DB_DUMP" >"$WORK/truncated-data.dump"
    if dc exec -T db pg_restore -l <"$WORK/truncated-data.dump" >/dev/null 2>&1; then
      pg postgres -c 'CREATE DATABASE drill_truncated_probe OWNER church TEMPLATE template0'
      rc=0; dc exec -T db pg_restore -U church -d drill_truncated_probe --single-transaction --exit-on-error --no-owner --no-acl <"$WORK/truncated-data.dump" >"$WORK/truncated-probe.log" 2>&1 || rc=$?
      pg postgres -c 'DROP DATABASE drill_truncated_probe'
      if [[ "$rc" == 1 ]]; then found=1; break; fi
    fi
  done
  [[ "$found" == 1 ]]
  dc stop caddy app
  logical_snapshot "$WORK/collect-original"
  expect_fail collect-failed restore --dump "$WORK/truncated-data.dump"
  [[ "$(phase)" == collect-failed ]]
  assert_failed_interval
  restore --status >"$WORK/collect-failed.status"; grep -q classification=Q "$WORK/collect-failed.status"
  logical_snapshot "$WORK/collect-failed"
  cmp "$WORK/collect-original.counts" "$WORK/collect-failed.counts"
  cmp "$WORK/collect-original.data" "$WORK/collect-failed.data"
  expect_fail collect-failed restore --resume
  logical_snapshot "$WORK/collect-retry"
  cmp "$WORK/collect-failed.counts" "$WORK/collect-retry.counts"
  cmp "$WORK/collect-failed.data" "$WORK/collect-retry.data"
  restore --abort
  # Drop intent interruptions allow either resume or abort. No data in fold
  # is restored into, and Q after the authorized drop remains resumable.
  for point in before-collect-drop after-collect-drop; do
    for action in abort resume; do
      reset_case
      kill_point "$point" --dump "$WORK/truncated-data.dump"
      assert_original_heap
      if [[ "$action" == abort ]]; then restore --abort
      else expect_fail collect-failed restore --resume; assert_closed; restore --abort; fi
    done
  done
  held_restore_client_cases
  # A recovery boot failure leaves both normal services stopped and removes
  # the failed labelled instance. A later resume can recover normally.
  reset_case
  expect_fail recovery-failed env RESTORE_DRILL_FAULT=bad-recovery "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$DB_DUMP"
  [[ "$(phase)" == recovery-failed ]]; assert_failed_interval
  restore --resume; assert_private
  touch "$DIR/drill-smoke-fail"
  expect_fail reopen-failed restore --reopen --checked-church-records
  assert_closed
  old="$(nonce_db fold_pre_restore_)"
  [[ "$(pg postgres -c "SELECT datallowconn FROM pg_database WHERE datname='$old'")" == f ]]
  rm "$DIR/drill-smoke-fail"
  restore --resume; assert_private; reopen_checked
  DB_DUMP="$original_dump"
}

# Held R forces swap-failed with ALLOW_CONNECTIONS restored on fold. Keep
# every expected timeout short, including repeated aborts: the holder's
# bounded watchdog must not expire and make a later drop legitimately pass.
held_restore_client_cases() {
  local r catalog rc
  reset_case
  RESTORE_DRILL_WAIT=2 start_paused before-swap --dump "$DB_DUMP"
  r="$(nonce_db fold_restore_)"
  sql_hold restore-client "$r" 'SELECT 1'
  timeout 10 bash -c 'echo continue >"$1"' _ "$DIR/drill.fifo"
  rc=0; wait_holder || rc=$?; [[ "$rc" != 0 ]]
  [[ "$(phase)" == swap-failed ]]
  [[ "$(pg postgres -c "SELECT datallowconn FROM pg_database WHERE datname='fold'")" == t ]]
  assert_closed; assert_original_heap
  assert_sql_holder "$r"
  catalog="$(pg postgres -c "SELECT datname,oid,datallowconn FROM pg_database WHERE datname='fold' OR datname='$r' ORDER BY datname")"
  RESTORE_DRILL_WAIT=2 expect_fail abort-failed restore --abort
  assert_sql_holder "$r"; assert_closed
  [[ "$(phase)" == abort-failed && "$(jq -er .at "$DIR/recovery.lock")" == drop ]]
  grep -q abort-drop-intent "$DIR/recovery.lock"
  [[ "$(pg postgres -c "SELECT datname,oid,datallowconn FROM pg_database WHERE datname='fold' OR datname='$r' ORDER BY datname")" == "$catalog" ]]
  restore --status >"$WORK/abort-drop-held.status"; grep -q classification=B "$WORK/abort-drop-held.status"
  RESTORE_DRILL_WAIT=2 expect_fail abort-failed restore --abort
  assert_sql_holder "$r"; assert_closed; assert_original_heap
  [[ "$(phase)" == abort-failed && "$(jq -er .at "$DIR/recovery.lock")" == drop ]]
  [[ "$(pg postgres -c "SELECT datname,oid,datallowconn FROM pg_database WHERE datname='fold' OR datname='$r' ORDER BY datname")" == "$catalog" ]]
  sql_release restore-client
  restore --abort
  reset_case
  RESTORE_DRILL_WAIT=2 start_paused before-swap --dump "$DB_DUMP" --image "$ALT_IMAGE" --source-dir "$SOURCE"
  r="$(nonce_db fold_restore_)"; sql_hold restore-client "$r" 'SELECT 1'
  timeout 10 bash -c 'echo continue >"$1"' _ "$DIR/drill.fifo"
  rc=0; wait_holder || rc=$?; [[ "$rc" != 0 && "$(phase)" == swap-failed ]]
  assert_sql_holder "$r"
  expect_fail 'abort refused' restore --abort
  assert_sql_holder "$r"
  sql_release restore-client
  restore --resume; assert_private; reopen_checked
}

pin_and_abort_cases() {
  local point action mode previous_head original_dump="$DB_DUMP"
  for mode in image build; do
    for point in before-pin mid-pin after-pin; do
      drill_step "pin/$mode/$point"
      reset_case
      args=(--dump "$DB_DUMP" --source-dir "$SOURCE")
      if [[ "$mode" == image ]]; then args+=(--image "$ALT_IMAGE")
      else args+=(--commit "$HIGH_COMMIT"); fi
      kill_point "$point" "${args[@]}"
      assert_original_heap
      if [[ "$point" == before-pin ]]; then
        restore --abort
        reset_case
        kill_point "$point" "${args[@]}"
      else expect_fail 'abort refused' restore --abort; fi
      restore --resume; assert_private; assert_original_heap; reopen_checked
      if compgen -G "$DIR/.pin-save.*" >/dev/null; then echo 'stale pin snapshot remains' >&2; return 1; fi
      for file in restore.sh provision.sh upgrade.sh smoke.sh; do cmp "$DIR/$file" "$DIR/recovery-tools/$file"; done
    done
  done
  for point in after-create before-swap; do
    drill_step "abort/$point"
    reset_case; kill_point "$point" --dump "$DB_DUMP"
    original="$(jq -er .origOid "$DIR/recovery.lock")"
    assert_original_heap
    restore --abort
    [[ "$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='fold'")" == "$original" ]]
    [[ "$(pg fold -c "SELECT value FROM drill_sentinel WHERE id=2")" == live ]]
  done
  reset_case; kill_point before-pin --dump "$DB_DUMP" --image "$ALT_IMAGE" --source-dir "$SOURCE"
  touch "$DIR/drill-smoke-fail"
  expect_fail abort-failed restore --abort
  assert_closed
  [[ "$(phase)" == abort-failed && "$(jq -er .at "$DIR/recovery.lock")" == smoke ]]
  rm "$DIR/drill-smoke-fail"
  restore --abort
  # Missing R in abort-intent has no drop intent and is never accepted.
  reset_case; kill_point before-swap --dump "$DB_DUMP"
  kill_point after-abort-intent --abort
  r="$(nonce_db fold_restore_)"; pg postgres -c "DROP DATABASE \"$r\""
  catalog_unchanged_refusals
  # A real recorded rollback, using the current installed tool set, chooses
  # private recovery or automatic reopen solely from the observed count.
  for mode in image build; do
    for revived in yes no; do
      drill_step "rollback/$mode/$revived"
      reset_case
      if [[ "$revived" == no ]]; then dump_current_fixture_archive "$WORK/no-revival.dump"; dump="$WORK/no-revival.dump"; else dump="$DB_DUMP"; fi
      mkdir -p "$DIR/dumps"
      cp "$dump" "$DIR/dumps/fold-20260101-0000.dump"
      cat >"$DIR/release.prev" <<RELEASE
mode=$mode
previous_image=$BASE_REGISTRY_IMAGE
previous_commit=$HIGH_COMMIT
dump=$DIR/dumps/fold-20260101-0000.dump
source_dir=$SOURCE
domain=groups.example.org
RELEASE
      rc=0
      RESTORE_DRILL_OPERATION=1 "$DIR/upgrade.sh" --rollback --deploy-dir "$DIR" --source-dir "$SOURCE" >"$WORK/rollback-$mode-$revived.log" 2>&1 || rc=$?
      if [[ "$revived" == yes ]]; then [[ "$rc" == 3 ]]; grep -q 'SITE IS CLOSED' "$WORK/rollback-$mode-$revived.log"; assert_private; [[ "$(jq -er .count "$DIR/recovery.lock")" == 1 ]]; reopen_checked
      else [[ "$rc" == 0 && ! -e "$DIR/recovery.lock" ]]; wait_public; fi
      for file in restore.sh provision.sh upgrade.sh smoke.sh; do cmp "$DIR/$file" "$DIR/recovery-tools/$file"; done
      [[ ! -f "$DIR/go.mod" && ! -d "$DIR/web" ]]
    done
  done
  # A copied-in restore cannot make a pre-restore tool set safe by assertion.
  reset_case
  mv "$DIR/compose.recovery.yml" "$WORK/saved-compose.recovery.yml"
  snapshot_state "$DIR" "$SOURCE" >"$WORK/missing-tool.before"
  expect_fail 'missing compose.recovery.yml' restore --dump "$DB_DUMP"
  snapshot_state "$DIR" "$SOURCE" >"$WORK/missing-tool.after"
  cmp "$WORK/missing-tool.before" "$WORK/missing-tool.after"
  mv "$WORK/saved-compose.recovery.yml" "$DIR/compose.recovery.yml"
  # Fixture levels here; actual level-1 revision crossings are R4-6c-V.
  for mode in image build; do
    drill_step "level-refusal/$mode"
    snapshot_state "$DIR" "$SOURCE" >"$WORK/level.before"
    args=(--dump "$DB_DUMP" --source-dir "$SOURCE")
    if [[ "$mode" == image ]]; then args+=(--image "$REGISTRY/fold:absent"); else args+=(--commit "$LOW_COMMIT"); fi
    expect_fail 'restore barrier' restore "${args[@]}"
    snapshot_state "$DIR" "$SOURCE" >"$WORK/level.after"
    cmp "$WORK/level.before" "$WORK/level.after"
  done
}

catalog_unchanged_refusals() {
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/catalog.before"
  expect_fail reconcile-needed restore --resume
  expect_fail reconcile-needed restore --abort
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/catalog.after"
  cmp "$WORK/catalog.before" "$WORK/catalog.after"
}
cleanup_and_tamper_cases() {
  local point tamper cleanup
  for point in after-drop after-floor; do
    for tamper in return-pre replace-fold; do
      drill_step "$point/$tamper"
      reset_case; restore --dump "$DB_DUMP"
      kill_point "$point" --reopen --checked-church-records
      pre="$(nonce_db fold_pre_restore_)"
      if [[ "$tamper" == return-pre ]]; then
        pg postgres -c 'CREATE DATABASE drill_tampered' -c "ALTER DATABASE drill_tampered RENAME TO \"$pre\""
      else
        dc stop caddy app
        pg postgres -c 'ALTER DATABASE fold RENAME TO drill_saved_restored' -c 'CREATE DATABASE fold OWNER church TEMPLATE template0'
      fi
      catalog_unchanged_refusals
      if [[ "$tamper" == replace-fold ]]; then pg postgres -c 'DROP DATABASE drill_saved_restored'; fi
    done
  done
  # Missing R both in collected and before swap; collect-failed with fold
  # renamed; another nonce database in collect-failed; each closes only.
  for tamper in collected-R collect-failed-fold collect-failed-extra; do
    drill_step "$tamper"
    reset_case
    if [[ "$tamper" == collected-R ]]; then
      kill_point before-pin --dump "$DB_DUMP" --image "$ALT_IMAGE" --source-dir "$SOURCE"
      r="$(nonce_db fold_restore_)"; pg postgres -c "DROP DATABASE \"$r\""
    else
      expect_fail collect-failed restore --dump "$WORK/truncated-data.dump"
      if [[ "$tamper" == collect-failed-fold ]]; then pg postgres -c 'ALTER DATABASE fold RENAME TO drill_saved_original'
      else pg postgres -c 'CREATE DATABASE fold_restore_ffffffffffffffff'; fi
    fi
    catalog_unchanged_refusals
    if [[ "$tamper" == collect-failed-fold ]]; then pg postgres -c 'DROP DATABASE drill_saved_original'; fi
  done
  # A drop failure after smoke leaves the site open; clearing the external
  # cause lets --resume rerun smoke and finish cleanup.
  reset_case; restore --dump "$DB_DUMP"
  kill_point before-drop --reopen --checked-church-records
  pre="$(nonce_db fold_pre_restore_)"
  # P refuses connections by design. A test-only session already inside P
  # cannot be created now, so use a transparent one-command failure wrapper.
  touch "$WORK/fail-pre-drop"
  expect_fail cleanup-failed restore --resume
  [[ "$(phase)" == cleanup-failed && "$(jq -er .at "$DIR/recovery.lock")" == drop ]]
  wait_public
  rm "$WORK/fail-pre-drop"; restore --resume
  [[ ! -e "$DIR/recovery.lock" ]]
  reset_case; restore --dump "$DB_DUMP"
  touch "$WORK/fail-floor"
  expect_fail cleanup-failed restore --reopen --checked-church-records
  [[ "$(phase)" == cleanup-failed && "$(jq -er .cleanup "$DIR/recovery.lock")" == dropped ]]
  wait_public
  rm "$WORK/fail-floor"; restore --resume
  [[ ! -e "$DIR/recovery.lock" ]]
}

seed_push_jobs() {
  local keys pub auth
  keys="$(node -e 'const c=require("node:crypto");const k=c.createECDH("prime256v1");k.generateKeys();process.stdout.write(JSON.stringify({pub:k.getPublicKey().toString("base64url"),auth:c.randomBytes(16).toString("base64url")}))')"
  pub="$(jq -er .pub <<<"$keys")"; auth="$(jq -er .auth <<<"$keys")"
  for user in "$A" "$M"; do
    pg fold -v pub="$pub" -v auth="$auth" -v uid="$user" <<'SQL'
INSERT INTO consents(user_id,channel,scope,status,source) VALUES (:'uid','push','all','opted_in','web_form');
WITH dev AS (
 INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth,label)
 VALUES (:'uid','https://push.restore-drill.invalid/device-'||:'uid',:'pub',:'auth','Drill device') RETURNING id,user_id
), n AS (
 INSERT INTO notifications(user_id,kind,category,title,push_title,push_body,link)
 VALUES (:'uid','account.turned_18','safety','Drill safety notice','Drill update','Review an update in Fold','/') RETURNING id
), j AS (
 INSERT INTO jobs(kind,payload,run_at,max_attempts,unique_key)
 SELECT 'push.deliver',json_build_object('notificationId',n.id,'deviceId',dev.id),now()+interval '1 day',5,'push:n:'||n.id||':d:'||dev.id FROM n,dev RETURNING unique_key
)
INSERT INTO drill_job_keys SELECT unique_key FROM j;
INSERT INTO notification_deliveries(notification_id,user_id,channel,status,reason) SELECT id,user_id,'email','suppressed','drill push-only fixture' FROM notifications WHERE user_id=:'uid' AND kind='account.turned_18';
SQL
  done
}
unchanged_loser() {
  local needle="$1" rc=0; shift
  snapshot_state "$DIR" "$SOURCE" >"$WORK/concurrent.before"
  timeout 15 "$@" >"$WORK/concurrent-loser.log" 2>&1 || rc=$?
  [[ "$rc" != 0 ]]
  if [[ "$needle" == 75 ]]; then [[ "$rc" == 75 ]]; else grep -qF "$needle" "$WORK/concurrent-loser.log"; fi
  snapshot_state "$DIR" "$SOURCE" >"$WORK/concurrent.after"
  cmp "$WORK/concurrent.before" "$WORK/concurrent.after"
}
maintenance_losers() {
  unchanged_loser 75 "$DIR/provision.sh" --skip-harden --allow-os --deploy-dir "$DIR" --source-dir "$SOURCE" --domain groups.example.org --image "$BASE_REGISTRY_IMAGE"
  unchanged_loser 75 "$DIR/upgrade.sh" --deploy-dir "$DIR" --source-dir "$SOURCE" --image "$BASE_REGISTRY_IMAGE"
  unchanged_loser 75 "$DIR/upgrade.sh" --deploy-dir "$DIR" --source-dir "$SOURCE" --rollback
  nonce="$(jq -r '.nonce // "00000000000000000000000000000000"' "$DIR/recovery.lock" 2>/dev/null || echo 00000000000000000000000000000000)"
  unchanged_loser 75 "$DIR/provision.sh" --pin-only --recovery-nonce "$nonce" --skip-harden --allow-os --deploy-dir "$DIR" --source-dir "$SOURCE" --domain groups.example.org --image "$BASE_REGISTRY_IMAGE"
}
concurrency_cases() {
  reset_case
  start_paused after-lock --dump "$DB_DUMP"
  [[ ! -f "$DIR/recovery.lock" ]]
  unchanged_loser 75 "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$DB_DUMP"
  maintenance_losers
  release_barrier; assert_private
  # A paused healthy recovery resume holds exactly the same operations lock.
  start_paused recovering --resume
  unchanged_loser 75 "$DIR/restore.sh" --deploy-dir "$DIR" --drill --abort
  release_barrier
  # A reopen held by its real smoke helper excludes an outside resume.
  rm -f "$DIR/drill.ready"
  env RESTORE_DRILL_FAULT=mid-smoke RESTORE_DRILL_OPERATION=1 "$DIR/restore.sh" --deploy-dir "$DIR" --drill --reopen --checked-church-records >"$WORK/reopen-lock.log" 2>&1 & holder=$!
  wait_file "$DIR/drill.ready" '^mid-smoke|'
  unchanged_loser 75 "$DIR/restore.sh" --deploy-dir "$DIR" --drill --resume
  release_barrier
  # Child pin-only keeps fd 9 while it has changed .env and is paused before
  # pulling. Every outside maintenance path loses before touching anything.
  reset_case
  start_paused mid-pin --dump "$DB_DUMP" --image "$ALT_IMAGE" --source-dir "$SOURCE"
  maintenance_losers
  release_barrier; assert_private
  # A provisioner winner blocks restore before it can read recovery.lock.
  reset_case
  rm -f "$DIR/drill.ready"
  DEPLOY_DRILL_PAUSE=after-lock "$DIR/provision.sh" --skip-harden --allow-os --deploy-dir "$DIR" --source-dir "$SOURCE" --domain groups.example.org --image "$BASE_REGISTRY_IMAGE" >"$WORK/provision-lock.log" 2>&1 & holder=$!
  wait_file "$DIR/drill.ready" '^after-lock$'
  unchanged_loser 75 "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$DB_DUMP"
  release_barrier
  # The descriptor remains held across upgrade's exec of the installed
  # restore, its recovery-tools re-exec, and its nested provisioner.
  mkdir -p "$DIR/dumps"; cp "$DB_DUMP" "$DIR/dumps/fold-20260101-0000.dump"
  cat >"$DIR/release.prev" <<RELEASE
mode=image
previous_image=$BASE_REGISTRY_IMAGE
previous_commit=
dump=$DIR/dumps/fold-20260101-0000.dump
source_dir=$SOURCE
domain=groups.example.org
RELEASE
  rm -f "$DIR/drill.ready"
  RESTORE_DRILL_PAUSE=after-lock RESTORE_DRILL_OPERATION=1 "$DIR/upgrade.sh" --rollback --deploy-dir "$DIR" --source-dir "$SOURCE" >"$WORK/rollback-lock.log" 2>&1 & holder=$!
  wait_file "$DIR/drill.ready" '^after-lock$'
  unchanged_loser 75 "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$DB_DUMP"
  rc=0; release_barrier || rc=$?; [[ "$rc" == 3 ]]; assert_private
  # SIGKILL of a true holder with a durable lock releases the kernel lock;
  # private recovery and the nonce remain, and a new resume can proceed.
  start_paused recovering --resume
  [[ -f "$DIR/recovery.lock" ]]
  kill_holder
  restore --resume; assert_private; reopen_checked
}

lifecycle_cases() {
  local old id nonce image expected
  reset_case; restore --dump "$DB_DUMP"
  URL=http://localhost:18080; login
  [[ "$(pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE datname='fold' AND backend_type='client backend'")" -gt 0 ]]
  # Plain compose up never starts an exited recovery service, even though it
  # starts the normal services. Resume closes that operator mistake via E2.
  old="$(recovery_ids)"; docker stop "$old"
  dc up -d
  [[ "$(docker inspect --format '{{.State.Running}}' "$old")" == false ]]
  restore --status >"$WORK/plain-up.status"; grep -q WARNING "$WORK/plain-up.status"
  pg fold -c "SELECT id,kind,status,attempts,locked_at FROM jobs WHERE attempts>0 ORDER BY id" >"$WORK/claims-before-close.txt"
  restore --resume | tee "$WORK/plain-up.resume"
  grep -q 'observed W=0' "$WORK/plain-up.resume"
  assert_private; assert_no_claims
  [[ "$(recovery_ids)" != "$old" ]]
  # Both a healthy reuse and a stopped container of the same nonce have
  # distinct lifecycle outcomes, without a fixed-name container collision.
  old="$(recovery_ids)"
  assert_healthy_recovery_reuse "$old" lifecycle-healthy-resume
  docker stop "$old"; dc restart db
  restore --resume; assert_private; [[ "$(recovery_ids)" != "$old" ]]
  # No-nonce and foreign-nonce containers are refused unchanged, including
  # their stopped state. Removal is an explicit fixture cleanup only.
  for nonce in foreign absent; do
    docker rm -f "$(recovery_ids)"
    image="$(jq -er .imageId "$DIR/recovery.lock")"
    label_args=(--label "com.docker.compose.project=$PROJECT" --label com.docker.compose.service=recovery)
    [[ "$nonce" != foreign ]] || label_args+=(--label fold.recovery.nonce=foreign)
    id="$(docker create "${label_args[@]}" "$image")"
    unchanged_loser 'foreign recovery container' "$DIR/restore.sh" --deploy-dir "$DIR" --drill --resume
    docker rm "$id"
    restore --resume; assert_private
  done
  # A failed stop is injected by the Docker shim, not by a fake state change
  # in production. No recovery container starts until E2 succeeds for real.
  dc up -d --no-deps app
  docker rm -f "$(recovery_ids)"
  expect_fail close-failed env RESTORE_DRILL_FAULT=stop-fails "$DIR/restore.sh" --deploy-dir "$DIR" --drill --resume
  [[ "$(docker inspect --format '{{.State.Running}}' "$(dc ps -q app)")" == true ]]
  [[ -z "$(recovery_ids)" ]]
  restore --status >"$WORK/stop-failed.status"; grep -q close-failed "$WORK/stop-failed.status"
  restore --resume; assert_private; assert_no_claims
  # Optional daemon restart requires a dedicated Docker host with no other
  # projects, so it cannot interrupt an owner's unrelated containers.
  if [[ "${RESTORE_DRILL_DAEMON_RESTART:-0}" == 1 ]]; then
    for id in $(docker ps -aq); do
      if [[ "$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$id")" != "$PROJECT" ]]; then
        echo 'daemon restart drill requires a dedicated disposable Docker daemon' >&2; return 1
      fi
    done
    systemctl restart docker
    dc up -d registry smtp push-sink
    for ((i=0;i<60;i++)); do docker info >/dev/null 2>&1 && break; sleep 1; done
    for ((i=0;i<90;i++)); do dc exec -T db pg_isready -U church >/dev/null 2>&1 && break; sleep 1; done
    for id in $(dc ps -a -q app caddy) "$(recovery_ids)"; do [[ "$(docker inspect --format '{{.State.Running}}' "$id")" == false ]]; done
    restore --resume; assert_private
    kill_point after-caddy-start --reopen --checked-church-records
    systemctl restart docker
    dc up -d registry smtp push-sink
    for ((i=0;i<60;i++)); do port_open "$PUBLIC_PORT" && break; sleep 1; done
    port_open "$PUBLIC_PORT"
    for ((i=0;i<90;i++)); do dc exec -T db pg_isready -U church >/dev/null 2>&1 && break; sleep 1; done
    restore --resume; assert_private
  else
    printf '%s\n' 'R4-6c-V hand check: dedicated-host daemon restart during recovery and after interrupted reopen; physical Droplet reboot in both states.' >>"$WORK/hand-checks.txt"
  fi
  # Try installation in scratch storage; preserve a failure as a hand-check reason.
  if PLAYWRIGHT_BROWSERS_PATH="$WORK/firefox-browsers" timeout 180 node "$ROOT/web/node_modules/playwright/cli.js" install firefox >"$WORK/firefox-install.log" 2>&1; then
    PLAYWRIGHT_BROWSERS_PATH="$WORK/firefox-browsers" RESTORE_BROWSER=firefox RESTORE_WEB_PACKAGE="$ROOT/web/package.json" RESTORE_SCREENSHOT="$WORK/firefox-delete-390.png" RESTORE_BROWSER_PROOF="$WORK/firefox-cookie-proof.json" RESTORE_TARGET_ID="$A" RESTORE_BROWSER_ARTIFACTS="$WORK" DEBUG="${DEBUG:+$DEBUG,}pw:browser" node "$WORK/private-browser.cjs"
  else
    printf '%s\n' 'R4-6c-V hand check: Firefox Secure localhost sign-in and real delete dialog; installation failed or timed out (see firefox-install.log). Safari also requires a hand check.' >>"$WORK/hand-checks.txt"
  fi
  reopen_checked
}

# Independent review regressions use the installed production entry points.
review_reconciliation_cases() {
  local pre saved=drill_saved_pre original restored
  reset_case
  pg postgres -c 'CREATE DATABASE fold_restore_test'
  restore --dump "$DB_DUMP"; assert_private
  [[ "$(pg postgres -c "SELECT count(*) FROM pg_database WHERE datname='fold_restore_test'")" == 1 ]]
  docker rm -f "$(recovery_ids)"
  restore --resume; assert_private
  pg postgres -c 'DROP DATABASE fold_restore_test'
  pre="$(nonce_db fold_pre_restore_)"
  original="$(jq -er .origOid "$DIR/recovery.lock")"
  restored="$(jq -er .restoreOid "$DIR/recovery.lock")"
  pg postgres -c "ALTER DATABASE \"$pre\" RENAME TO $saved"
  catalog_unchanged_refusals
  [[ "$(phase)" == reconcile-needed && "$(jq -er .reconcileFrom "$DIR/recovery.lock")" == recovering ]]
  pg postgres -c "ALTER DATABASE $saved RENAME TO \"$pre\""
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/reconcile-repaired.before"
  restore --status | tee "$WORK/reconcile-repaired.status"
  grep -q recheckAccepted=yes "$WORK/reconcile-repaired.status"
  restore --resume
  [[ "$(phase)" == recovering ]]; assert_closed
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/reconcile-repaired.after"
  cmp "$WORK/reconcile-repaired.before" "$WORK/reconcile-repaired.after"
  [[ "$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='$pre'")" == "$original" ]]
  [[ "$(pg postgres -c "SELECT oid FROM pg_database WHERE datname='fold'")" == "$restored" ]]
  restore --resume; assert_private; reopen_checked
}

review_backup_archive_cases() {
  local backup_pid rc archive="$DB_DUMP" held="$WORK/held-selected.dump" old before
  reset_case
  mkdir -p "$DIR/dumps"
  cp "$DB_DUMP" "$DIR/dumps/fold-20260101-0000.dump"
  : >"$WORK/backup-no-offsite.env"
  mkfifo "$WORK/backup.fifo"; touch "$WORK/backup-barrier"
  "$DIR/backup.sh" --restore-test --deploy-dir "$DIR" --backup-dir "$DIR/dumps" --env-file "$WORK/backup-no-offsite.env" >"$WORK/existing-backup.log" 2>&1 &
  backup_pid=$!; holder="$backup_pid"
  wait_file "$WORK/backup.ready" ready
  rc=0; RESTORE_DRILL_WAIT=2 restore --dump "$DB_DUMP" >"$WORK/backup-busy-restore.log" 2>&1 || rc=$?
  [[ "$rc" == 75 && ! -e "$DIR/recovery.lock" ]]; wait_public
  rc=0; "$DIR/backup.sh" --deploy-dir "$DIR" --backup-dir "$DIR/dumps" >"$WORK/backup-busy-backup.log" 2>&1 || rc=$?
  [[ "$rc" == 75 ]]
  timeout 10 bash -c 'echo continue >"$1"' _ "$WORK/backup.fifo"
  timeout 60 tail --sleep-interval=.05 --pid="$backup_pid" -f /dev/null
  wait "$backup_pid"; holder=""
  rm "$WORK/backup-barrier" "$WORK/backup.ready" "$WORK/backup.fifo"
  restore --dump "$DB_DUMP"; assert_private
  old="$DIR/dumps/fold-20200102-0000.dump"
  cp "$DB_DUMP" "$old"; printf 'fixture ciphertext\n' >"$old.age"
  touch -d '200 days ago' "$old" "$old.age"
  before="$(sha256sum "$old" "$old.age")"
  find "$DIR/dumps" -type f -printf '%P\n' | sort >"$WORK/backup-recovery.before"
  for mode in normal restore-test; do
    args=(); [[ "$mode" != restore-test ]] || args+=(--restore-test)
    rc=0
    "$DIR/backup.sh" --deploy-dir "$DIR" --backup-dir "$DIR/dumps" --env-file "$WORK/backup-no-offsite.env" "${args[@]}" >"$WORK/backup-recovery-$mode.log" 2>&1 || rc=$?
    [[ "$rc" == 75 ]]; grep -q 'recovery active' "$WORK/backup-recovery-$mode.log"
  done
  find "$DIR/dumps" -type f -printf '%P\n' | sort >"$WORK/backup-recovery.after"
  cmp "$WORK/backup-recovery.before" "$WORK/backup-recovery.after"
  [[ "$before" == "$(sha256sum "$old" "$old.age")" ]]
  # Reboot-style private recreation must not depend on an expired archive.
  mv "$archive" "$held"
  docker rm -f "$(recovery_ids)"
  restore --resume; assert_private
  mv "$held" "$archive"
  reopen_checked
  # A phase that will execute pg_restore still requires the exact bytes.
  reset_case; kill_point after-create --dump "$DB_DUMP"
  mv "$archive" "$held"
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/archive-missing.before"
  expect_fail 'same archive sha256' restore --resume
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/archive-missing.after"
  cmp "$WORK/archive-missing.before" "$WORK/archive-missing.after"
  mv "$held" "$archive"; restore --abort
  cat >>"$WORK/hand-checks.txt" <<'PROCEDURE'
R4-6c-V procedure: installed backup.sh holds backup.lock through dump, expiry,
encryption/upload or restore-test and refuses exit 75 while recovery.lock exists.
Restore takes ops.lock then backup.lock, waits at most 120 seconds before lock
publication or service stop, and releases backup.lock only after recovery.lock is
durable. Never delete either lock file. Older hand-run scripts do not coordinate.
After collected is recorded, --resume uses the database/count already collected;
it does not require the original archive. Earlier phases require its exact hash.
--reopen-if-none (including upgrade.sh --rollback) exits 3 with SITE IS CLOSED
when a nonzero/unknown count requires operator recovery. Normal private --dump
and --resume still exit 0. The recoveryPolicy >=1 release gate remains unverified
until Terra's isolated runtime checks and the R4-6c-V procedure checks pass.
PROCEDURE
}

review_cleanup_retry_cases() {
  local initial saved_phase point
  for initial in before-drop after-drop after-floor drop-failed floor-failed; do
    for point in after-app-start after-caddy-start mid-smoke; do
      drill_step "$initial/$point" 15
      reset_case; restore --dump "$DB_DUMP"
      case "$initial" in
        drop-failed)
          touch "$WORK/fail-pre-drop"
          expect_fail cleanup-failed restore --reopen --checked-church-records
          rm "$WORK/fail-pre-drop" ;;
        floor-failed)
          touch "$WORK/fail-floor"
          expect_fail cleanup-failed restore --reopen --checked-church-records
          rm "$WORK/fail-floor" ;;
        *) kill_point "$initial" --reopen --checked-church-records ;;
      esac
      saved_phase="$(phase)"
      if [[ "$point" == mid-smoke ]]; then smoke_interruption "$point" --resume
      else kill_point "$point" --resume; fi
      [[ "$(phase)" == "$saved_phase" ]]; wait_public
      restore --resume
      [[ ! -e "$DIR/recovery.lock" && -z "$(recovery_ids)" ]]; wait_public
    done
  done
}

review_orphaned_restore_case() {
  local database backend client end rc before
  reset_case
  rm -f "$WORK/during-restore.client"
  mkfifo "$WORK/during-restore.fifo"
  env RESTORE_DRILL_OPERATION=1 RESTORE_DRILL_FAULT=during-restore "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$DB_DUMP" >"$WORK/orphan-restore.log" 2>&1 &
  holder=$!
  wait_file "$WORK/during-restore.client" '^[0-9][0-9]*$'
  client="$(cat "$WORK/during-restore.client")"
  [[ "$client" =~ ^[0-9]+$ && "$client" != 1 && "$client" != "$holder" ]]
  database="$(nonce_db fold_restore_)"
  # R exists only now. The wrapper pauses before pg_restore so its first DDL
  # can block on a holder in R, rather than an unrelated lock in postgres.
  sql_hold orphan_guard "$database" 'SELECT pg_advisory_lock(6467261)'
  timeout 10 bash -c 'printf "continue\n" >"$1"' _ "$WORK/during-restore.fifo"
  end=$((SECONDS+60)); backend=""
  while (( SECONDS<end )); do
    backend="$(pg postgres -c "SELECT pid FROM pg_stat_activity WHERE datname='$database' AND application_name='pg_restore' AND wait_event='advisory' AND $SQL_BACKEND=ANY(pg_blocking_pids(pid))")"
    [[ -z "$backend" ]] || break
    sleep .05
  done
  [[ "$backend" =~ ^[0-9]+$ && "$(phase)" == created ]]
  kill -KILL "$holder" "$client"
  wait "$holder" 2>/dev/null || true; holder=""
  timeout 15 flock -x "$DIR/ops.lock" true
  [[ "$(pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE pid=$backend AND datname='$database' AND wait_event='advisory'")" == 1 ]]
  before="$(pg postgres -c "SELECT datname,oid,datallowconn FROM pg_database WHERE datname='fold' OR datname='$database' ORDER BY datname")"
  rc=0; RESTORE_DRILL_WAIT=2 restore --resume >"$WORK/orphan-resume.log" 2>&1 || rc=$?
  [[ "$rc" == 1 && "$(phase)" == created ]]; assert_closed
  grep -q 'restore database clients have not drained' "$WORK/orphan-resume.log"
  [[ "$before" == "$(pg postgres -c "SELECT datname,oid,datallowconn FROM pg_database WHERE datname='fold' OR datname='$database' ORDER BY datname")" ]]
  # The second invocation issued no restore/drop to this same R.
  [[ "$(jq -s --arg db "$database" '[.[]|select(.command|contains("pg_restore -U church -d "+$db+" "))]|length' "$WORK/restore-command-history.jsonl")" == 1 ]]
  [[ "$(pg postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid=$backend AND datname='$database'")" == t ]]
  sql_release orphan_guard
  rm -f "$WORK/during-restore.fifo"
  end=$((SECONDS+20))
  while [[ "$(pg postgres -c "SELECT count(*) FROM pg_stat_activity WHERE pid=$backend")" != 0 ]]; do (( SECONDS<end )); sleep .05; done
  pg "$database" -c 'DROP EVENT TRIGGER drill_pause_restore' -c 'DROP FUNCTION drill_pause_restore()'
  restore --resume; assert_private; assert_original_heap; reopen_checked
}

review_moving_tag_case() {
  local original target rc
  reset_case
  original="$(docker inspect --format '{{.Image}}' "$(dc ps -q app)")"
  mkdir "$WORK/moving-tag-context"
  printf 'FROM %s\nLABEL fold.drill.moved="yes"\n' "$BASE_REGISTRY_IMAGE" >"$WORK/moving-tag-context/Dockerfile"
  docker build --pull=false -t "$BASE_REGISTRY_IMAGE" "$WORK/moving-tag-context"
  target="$(docker image inspect --format '{{.Id}}' "$BASE_REGISTRY_IMAGE")"
  [[ "$target" != "$original" ]]
  docker push "$BASE_REGISTRY_IMAGE"
  docker tag "$original" "$BASE_REGISTRY_IMAGE"
  restore --dump "$DB_DUMP" --image "$BASE_REGISTRY_IMAGE" --source-dir "$SOURCE"
  [[ "$(jq -er .previous.imageId "$DIR/recovery.lock")" == "$original" ]]
  [[ "$(jq -er .imageBeforePull "$DIR/recovery.lock")" == "$original" ]]
  [[ "$(jq -er .imageId "$DIR/recovery.lock")" == "$target" ]]
  assert_private; reopen_checked
  # Restore the disposable registry's base for the later fixture resets.
  docker tag "$original" "$BASE_REGISTRY_IMAGE"; docker push "$BASE_REGISTRY_IMAGE"
}

normalize_schema() {
  sed '/^--/d;/^$/d;/^SET /d;/^SELECT pg_catalog.set_config/d;/^\\restrict /d;/^\\unrestrict /d' | sed 's/[[:space:]]*$//'
}
assert_legacy_archive_schema() {
  # pg_restore requires an explicit SQL output destination even when its
  # archive comes from stdin. Keep both full normalized schemas for review.
  dc exec -T db pg_restore --schema-only --no-owner --no-acl --file=- <"$DB_DUMP" | normalize_schema >"$WORK/legacy-archive.schema" || return 1
  dc exec -T db pg_dump -U church --schema-only --no-owner --no-acl fold </dev/null | normalize_schema >"$WORK/swapped-before-boot.schema" || return 1
  [[ -s "$WORK/legacy-archive.schema" && -s "$WORK/swapped-before-boot.schema" ]] || {
    echo 'legacy archive comparison requires two nonempty schemas' >&2; return 1;
  }
  diff -u "$WORK/legacy-archive.schema" "$WORK/swapped-before-boot.schema"
}
legacy_archive_case() {
  local revision legacy_image archive_id old_url original_dump="$DB_DUMP" funcs
  reset_case
  revision="$LEGACY_REVISION"
  [[ "$(drill_legacy_revision "$ROOT")" == "$revision" ]]
  mkdir "$WORK/legacy-source"
  git -C "$ROOT" archive "$revision" | tar -C "$WORK/legacy-source" -xf -
  legacy_image="$REGISTRY/fold:archive-${revision:0:12}"
  docker build --label "org.opencontainers.image.revision=$revision" -t "$legacy_image" "$WORK/legacy-source"
  [[ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$legacy_image")" == "$revision" ]]
  pg postgres -c 'CREATE DATABASE drill_archive_fixture OWNER church TEMPLATE template0'
  cat >"$DIR/.drill-archive.yml" <<ARCHIVE
services:
  archive:
    extends:
      file: $DIR/docker-compose.yml
      service: app
    image: $legacy_image
    pull_policy: never
    restart: "no"
    profiles: [archive]
    environment:
      DATABASE_URL: postgres://church:drill-password@db:5432/drill_archive_fixture?sslmode=disable
      FOLD_WORKERS: "0"
    ports:
      - "127.0.0.1::8080"
ARCHIVE
  dc -f "$DIR/docker-compose.yml" -f "$DIR/.drill-archive.yml" --profile archive up -d --no-deps archive
  archive_id="$(dc -f "$DIR/docker-compose.yml" -f "$DIR/.drill-archive.yml" --profile archive ps -q archive)"
  for ((i=0;i<90;i++)); do docker exec "$archive_id" /fold healthcheck >/dev/null 2>&1 && break; sleep 1; done
  docker exec "$archive_id" /fold healthcheck
  old_url="$URL"; endpoint="$(dc -f "$DIR/docker-compose.yml" -f "$DIR/.drill-archive.yml" --profile archive port archive 8080)"
  URL="http://localhost:${endpoint##*:}"; setup
  [[ "$(pg drill_archive_fixture -c "SELECT string_agg(version_id::text,',' ORDER BY version_id) FROM goose_db_version WHERE is_applied AND version_id>0")" == 1 ]]
  funcs="$(pg drill_archive_fixture -c "SELECT pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname IN ('reject_consent_change','add_request_identity','add_request_completed') ORDER BY proname")"
  if grep -q 'NEW.ip\|v.add_childcare_ages_note' <<<"$funcs"; then echo 'legacy functions already contain the later scrub exceptions' >&2; return 1; fi
  printf '%s\n' "$funcs" >"$WORK/legacy-function-bodies.sql"
  printf '%s\n' "$revision" >"$WORK/archive-revision.txt"
  DB_DUMP="$WORK/legacy.dump"
  dc exec -T db pg_dump -U church -Fc drill_archive_fixture >"$DB_DUMP"
  dc exec -T db pg_restore -l <"$DB_DUMP" >"$WORK/legacy-toc.txt"
  docker rm -f "$archive_id"
  pg postgres -c 'DROP DATABASE drill_archive_fixture'
  URL="$old_url"; login
  # Stop immediately after swap, before any recovery boot can migrate it.
  kill_point after-swap --dump "$DB_DUMP"
  assert_legacy_archive_schema
  [[ "$(pg fold -c 'SELECT max(version_id) FROM goose_db_version WHERE is_applied')" == 1 ]]
  restore --resume; assert_private
  id="$(recovery_ids)"; docker exec "$id" /fold migrate status --json >"$WORK/legacy-goose-status.json"
  [[ "$(pg fold -c 'SELECT count(*)=3 AND count(DISTINCT version_id)=3 FROM goose_db_version WHERE version_id BETWEEN 2 AND 4 AND is_applied')" == t ]]
  funcs="$(pg fold -c "SELECT pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname IN ('reject_consent_change','add_request_identity','add_request_completed') ORDER BY proname")"
  grep -q NEW.ip <<<"$funcs"; grep -q v.add_childcare_ages_note <<<"$funcs"
  restore --reopen --checked-church-records
  DB_DUMP="$original_dump"
  reset_case
}
assert_statement_history() {
  jq -s -e '
    all(.[]; if .command|contains("pg_restore -U") then .phase=="created" and (.command|test("-d fold_restore_[0-9a-f]{16} ")) else true end)
    and all(.[]; (.command|test("DROP DATABASE \"?fold\"?([;[:space:]]|$)"))|not)
  ' "$WORK/restore-command-history.jsonl" >/dev/null
  # A successful whole swap issues each rename once per nonce, including
  # lost acknowledgement and forced two-step resumes.
  jq -s -e '[.[] | select(.command|contains("RENAME TO"))] | group_by(.nonce) | all(.[]; [.[].command] | map(scan("RENAME TO")) | length<=2)' "$WORK/restore-command-history.jsonl" >/dev/null
}
