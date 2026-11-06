#!/usr/bin/env bash
# Transparent Docker wrapper for disposable restore fault/race barriers.
set -euo pipefail
: "${RESTORE_DRILL_REAL_DOCKER:?disposable drill only}"
: "${RESTORE_DRILL_SITE:?disposable drill only}"
: "${RESTORE_DRILL_WORK:?disposable drill only}"
[[ "$RESTORE_DRILL_SITE" == "$RESTORE_DRILL_WORK/site" && -f "$RESTORE_DRILL_SITE/.deploy-drill" ]] || exit 99
args=("$@")
# Refuse to fault another project's invocation, even if PATH leaks.
if [[ "$*" != *"--project-directory $RESTORE_DRILL_SITE "* ]]; then exec "$RESTORE_DRILL_REAL_DOCKER" "$@"; fi
# Hold the existing backup restore-test while it owns backup.lock. Recovery
# must not publish its durable interval until this operation has completed.
if [[ -f "$RESTORE_DRILL_WORK/backup-barrier" && "$*" == *' pg_restore -U church -d fold_restore_test '* ]]; then
  echo ready >"$RESTORE_DRILL_WORK/backup.ready"
  exec 8<>"$RESTORE_DRILL_WORK/backup.fifo"
  IFS= read -r -t 120 token <&8 && [[ "$token" == continue ]] || exit 1
  exec 8>&-
fi
# The pg_restore client will outlive its caller if Docker exec loses its
# client. Block its first DDL on a harness-held advisory lock, not a sleep.
if [[ "${RESTORE_DRILL_FAULT:-}" == during-restore && "$*" == *' pg_restore -U church -d fold_restore_'* ]]; then
  database=""
  for ((i=0;i<${#args[@]};i++)); do
    [[ "${args[$i]}" != -d ]] || database="${args[$((i+1))]}"
  done
  [[ "$database" =~ ^fold_restore_[0-9a-f]{16}$ ]] || exit 99
  "$RESTORE_DRILL_REAL_DOCKER" compose --project-directory "$RESTORE_DRILL_SITE" exec -T db psql -X -U church -d "$database" -v ON_ERROR_STOP=1 <<'SQL'
CREATE FUNCTION drill_pause_restore() RETURNS event_trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_lock(6467261);
  PERFORM pg_advisory_unlock(6467261);
END $$;
CREATE EVENT TRIGGER drill_pause_restore ON ddl_command_start EXECUTE FUNCTION drill_pause_restore();
SQL
  printf '%s\n' "$$" >"$RESTORE_DRILL_WORK/during-restore.client"
  # The target database has just been created. Let the harness acquire its
  # advisory hold in that database before the restore's first DDL starts.
  [[ -p "$RESTORE_DRILL_WORK/during-restore.fifo" ]] || exit 99
  exec 8<>"$RESTORE_DRILL_WORK/during-restore.fifo"
  IFS= read -r -t 120 token <&8 && [[ "$token" == continue ]] || exit 1
  exec 8>&-
fi
if [[ "$*" == *' stop app' && -f "$RESTORE_DRILL_WORK/stop-barrier" ]]; then
  "$RESTORE_DRILL_REAL_DOCKER" "$@" & child=$!
  echo stop-app >"$RESTORE_DRILL_WORK/stop.ready"
  # Keep this wrapper alive until the harness has released the gate. The real
  # stop is already in progress and the request can commit during shutdown.
  exec 8<>"$RESTORE_DRILL_WORK/stop.fifo"
  IFS= read -r -t 120 token <&8 && [[ "$token" == continue ]] || exit 1
  wait "$child"
  exit
fi
if [[ -f "$RESTORE_DRILL_WORK/fail-pre-drop" && "$*" == *'DROP DATABASE "fold_pre_restore_'* ]]; then exit 1; fi
if [[ "$*" == *' stop caddy' && "${RESTORE_DRILL_FAULT:-}" == stop-fails ]]; then exit 1; fi
if [[ "$*" == *'COPY (SELECT id FROM users WHERE deleted_at IS NOT NULL)'* && -n "${RESTORE_DRILL_RACE_USER:-}" ]]; then
  [[ "$RESTORE_DRILL_RACE_USER" =~ ^[0-9]+$ ]] || exit 99
  "$RESTORE_DRILL_REAL_DOCKER" compose --project-directory "$RESTORE_DRILL_SITE" exec -T db psql -X -U church -d fold -At -v ON_ERROR_STOP=1 \
    -c "SELECT deleted_at IS NOT NULL FROM users WHERE id=$RESTORE_DRILL_RACE_USER" >"$RESTORE_DRILL_WORK/collection-race-user"
fi
# Record production restore requests, excluding fixture resets. SQL COPY
# contents stay in memory; only DDL text, phase and identities go to disk.
if [[ "${RESTORE_DRILL_OPERATION:-0}" == 1 && "$*" == *' exec -T db '* ]]; then
  input=""; from_stdin=0; sql=""
  if [[ "$*" == *' psql '* ]]; then
    have_c=0
    for ((i=0;i<${#args[@]};i++)); do
      if [[ "${args[$i]}" == -c ]]; then sql+="${args[$((i+1))]}"$'\n'; have_c=1; fi
    done
    if (( ! have_c )); then
      input="$(cat)"; from_stdin=1
      sql="$(sed -n '/^CREATE DATABASE /p;/^ALTER DATABASE /p;/^DROP DATABASE /p' <<<"$input")"
    fi
  fi
  state="$(cat "$RESTORE_DRILL_SITE/recovery.lock" 2>/dev/null || echo '{}')"
  logged="$*"; (( ! from_stdin )) || logged+=" $sql"
  jq -cn --arg command "$logged" --argjson state "$state" --arg at "$(date -u +%FT%TZ)" \
    --arg caseId "${RESTORE_DRILL_CASE:-unscoped}" --arg step "${RESTORE_DRILL_STEP:-}" \
    '{at:$at,case:$caseId,step:$step,command:$command,phase:$state.phase,cleanup:$state.cleanup,nonce:$state.nonce,origOid:$state.origOid,restoreOid:$state.restoreOid}' >>"$RESTORE_DRILL_WORK/restore-command-history.jsonl"
  if (( from_stdin )); then exec "$RESTORE_DRILL_REAL_DOCKER" "${args[@]}" <<<"$input"; fi
fi
exec "$RESTORE_DRILL_REAL_DOCKER" "${args[@]}"
