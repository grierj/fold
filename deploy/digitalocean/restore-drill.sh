#!/usr/bin/env bash
# Disposable restore proof, through installed DIR/restore.sh only.
# Never point this runner at an owner deployment. Artifacts are retained.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$ROOT/deploy/digitalocean/checks.sh"
# shellcheck source=deploy/digitalocean/deploy-drill.sh
source "$ROOT/deploy/digitalocean/deploy-drill.sh"
# shellcheck source=deploy/digitalocean/restore-drill-cases.sh
source "$ROOT/deploy/digitalocean/restore-drill-cases.sh"
# shellcheck source=deploy/digitalocean/isolation.sh
source "$ROOT/deploy/digitalocean/isolation.sh"
# shellcheck source=deploy/digitalocean/restore-drill-crossings.sh
source "$ROOT/deploy/digitalocean/restore-drill-crossings.sh"
WORK=""
BASE_IMAGE=""
KEEP=0
# full (the acceptance run) or crossings-only (a labelled PARTIAL diagnostic, see restore-drill-crossings.sh crossings_only_run)
MODE=full
LANE_REVISION="$CROSSING_LANE_DEFAULT"
LANDING_REVISION="$CROSSING_LANDING_DEFAULT"
PRIOR_REVISION="$CROSSING_PRIOR_DEFAULT"
ARCHIVE_BASE_REVISION="$CROSSING_ARCHIVE_DEFAULT"
while (( $# )); do
  case "$1" in
    --dir) WORK="${2:?new scratch path required}"; shift 2 ;;
    --image) BASE_IMAGE="${2:?candidate image required}"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --crossings-only) MODE=crossings-only; shift ;;
    --lane-base) LANE_REVISION="${2:?level-0 LANE_BASE commit required}"; shift 2 ;;
    --landing) LANDING_REVISION="${2:?level-1 T07B-1-V landing commit required}"; shift 2 ;;
    --prior) PRIOR_REVISION="${2:?level-2 prior release commit required}"; shift 2 ;;
    --archive-base) ARCHIVE_BASE_REVISION="${2:?older archive commit (without the reason-review migration) required}"; shift 2 ;;
    --help)
      echo 'Usage: sudo restore-drill.sh [--dir NEW_SCRATCH_PATH] [--image CANDIDATE] [--keep]'
      echo '         [--crossings-only]  (PARTIAL diagnostic, never full acceptance; exits 10 when it passes)'
      echo '         [--lane-base SHA] [--landing SHA] [--prior SHA] [--archive-base SHA]'
      echo 'The four revisions drive the real-binary crossing cases (R4-6c-V, T07B-2-V): LANE_BASE (the recorded'
      echo 'level-0 preflight base, default 8c07c85), the level-1 commit that precedes restore.sh (default d400b77),'
      echo 'the level-2 release before the candidate, the only legal rollback target (default 6927e89), and the'
      echo 'archive base (default 8989979): the first parent of the reason-review schema landing, whose migrations'
      echo 'lack the reason-review file. LANE_BASE is also dumped as the older table-set archive. The candidate'
      echo 'binary is built from git archive of the committed HEAD, which must be recovery level 2; an uncommitted'
      echo 'tree is refused. --image serves only the fixture stack.'
      exit 0 ;;
    *) echo "unknown option $1" >&2; exit 1 ;;
  esac
done
# The drill runs provisioners that write /etc/cron.d and /etc/logrotate.d and publishes ports 80 and 443: only inside a throwaway container with its own
# Docker daemon (restore-drill-nested.sh), and never over an owner's configuration. Checked before anything is created.
# shellcheck disable=SC2119 # the function takes optional test inputs, never the drill's arguments
isolation_require_disposable || exit 1
[[ "$(id -u)" == 0 ]] || { echo 'restore-drill.sh requires root on a disposable Docker host' >&2; exit 1; }
for tool in docker go git jq curl node flock timeout; do command -v "$tool" >/dev/null; done
unset GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE
REVISION="$(drill_source_revision "$ROOT")" || exit 1
LEGACY_REVISION="$(drill_legacy_revision "$ROOT")" || exit 1
# Every crossing revision is proved from git before a stack, an image or a
# database exists. There is no switch that skips the real-binary cases.
crossing_validate_revisions "$ROOT" "$LANE_REVISION" "$LANDING_REVISION" "$PRIOR_REVISION" "$ARCHIVE_BASE_REVISION" "$REVISION" || exit 1
crossing_require_clean_head "$ROOT" || exit 1
# The recovery browser proof (testdata/restore-browser.cjs) drives a real Chromium through web/node_modules/playwright. It is found, launched with the options the
# proof will use (restoreLaunchOptions, qc1/browser-launch.cjs) and exercised now, before a stack exists, instead of failing hours in. restore-drill-nested.sh installs
# the runtime (qc1/browser-prepare.sh) and exports QC1_CHROMIUM; this check refuses a runner where that was skipped. No switch skips it: the proof is not optional.
command -v node >/dev/null || { echo 'restore-drill.sh: node is required for the recovery browser proof' >&2; exit 1; }
BROWSER_PREFLIGHT="$(QC1_PREFLIGHT_FOR=restore QC1_WEB_DIR="$ROOT/web" node "$ROOT/deploy/digitalocean/qc1/browser-preflight.mjs")" \
  || { echo 'restore-drill.sh: the browser preflight failed: the recovery browser proof cannot run (restore-drill-nested.sh installs the browser runtime; on another machine run npm ci in web/ and install Playwright'"'"'s Chromium)' >&2; exit 1; }
jq -e '.ok == true' <<<"$BROWSER_PREFLIGHT" >/dev/null || { echo "restore-drill.sh: the browser preflight did not pass: $BROWSER_PREFLIGHT" >&2; exit 1; }
# Firefox (R4-6c-V): Playwright's Firefox is a glibc build that cannot start on this Alpine runner, so the launcher prepared a Debian container for it
# (qc1/browser-prepare.sh, QC1_PREPARE_FIREFOX=1) and proved it launches. Its record must exist: a runner that skipped the preparation is refused, not
# silently drilled without Firefox. A recorded BUILD failure is allowed and becomes an explicit first-church hand check in lifecycle_cases.
FIREFOX_RUNTIME="${FOLD_FIREFOX_RUNTIME:-}"
if [[ "$MODE" == full ]]; then
[[ -n "$FIREFOX_RUNTIME" && -f "$FIREFOX_RUNTIME" ]] || { echo 'restore-drill.sh: the Firefox runtime was not prepared (FOLD_FIREFOX_RUNTIME); run the drill through restore-drill-nested.sh' >&2; exit 1; }
jq -e '.available | type == "boolean"' "$FIREFOX_RUNTIME" >/dev/null || { echo "restore-drill.sh: $FIREFOX_RUNTIME is not a Firefox runtime record" >&2; exit 1; }
if jq -e '.available == true' "$FIREFOX_RUNTIME" >/dev/null; then
  [[ -n "${FOLD_FIREFOX_IMAGE:-}" && "$(docker image inspect --format '{{.Id}}' "$FOLD_FIREFOX_IMAGE" 2>/dev/null)" == "$(jq -er .imageId "$FIREFOX_RUNTIME")" ]] \
    || { echo 'restore-drill.sh: the prepared Firefox image is missing or is not the one the preparation proved' >&2; exit 1; }
fi
fi
unset COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES RESTORE_DRILL_FAULT RESTORE_DRILL_PAUSE RESTORE_DRILL_FORCE_TWO_STEP
if [[ -z "$WORK" ]]; then
  WORK="$(mktemp -d /tmp/fold-restore-drill.XXXXXX)"; rmdir "$WORK"
fi
WORK="$(realpath -m "$WORK")"
[[ "$WORK" == /* && "$WORK" != /opt/fold && "$WORK" != /opt/fold/* && ! -e "$WORK" ]] || { echo '--dir must be a new scratch path outside /opt/fold' >&2; exit 1; }
DRILL_PROGRESS_MODE="$MODE"
if [[ "$MODE" == full ]]; then
drill_progress_init bootstrap base-proof recovery-interruptions reopen-interruptions abort-interruptions tamper \
  accidental-app-reuse abort-ack-loss operations-lock recovery-configuration \
  count_races archive_failures pin_and_abort_cases cleanup_and_tamper_cases concurrency_cases lifecycle_cases \
  legacy_archive_case review_reconciliation_cases review_backup_archive_cases review_cleanup_retry_cases \
  review_orphaned_restore_case review_moving_tag_case \
  real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore final-verification
else
drill_progress_init "${CROSSINGS_ONLY_PLAN[@]}"
fi
trap 'drill_progress_error "$LINENO" "$BASH_COMMAND" "$?"' ERR
drill_case_begin bootstrap
args=(--prepare-only --dir "$WORK")
[[ -z "$BASE_IMAGE" ]] || args+=(--image "$BASE_IMAGE")
"$ROOT/deploy/digitalocean/deploy-drill.sh" "${args[@]}"
drill_case_end
MANIFEST="$WORK/restore-drill.json"
[[ "$(jq -er .revision "$MANIFEST")" == "$REVISION" ]] || { echo 'restore drill source revision mismatch' >&2; exit 1; }
printf '%s\n' "$BROWSER_PREFLIGHT" >"$WORK/browser-preflight.json"
[[ "$MODE" != full ]] || cp "$FIREFOX_RUNTIME" "$WORK/firefox-runtime.json"
DIR="$(jq -er .dir "$MANIFEST")"
SOURCE="$(jq -er .source "$MANIFEST")"
PROJECT="$(jq -er .project "$MANIFEST")"
REGISTRY="$(jq -er .registry "$MANIFEST")"
BASE_REGISTRY_IMAGE="$(jq -er .image "$MANIFEST")"
LOW_COMMIT="$(jq -er .lowCommit "$MANIFEST")"
HIGH_COMMIT="$(jq -er .highCommit "$MANIFEST")"
ALT_IMAGE="$REGISTRY/fold:restore-target"
docker tag "$BASE_REGISTRY_IMAGE" "$ALT_IMAGE"
docker push "$ALT_IMAGE"
[[ "$DIR" == "$WORK/site" && "$PROJECT" == fold-deploy-drill-* && "$DIR" != /opt/fold ]] || exit 1
validate_deploy_drill "$DIR"
export DEPLOY_DRILL=1
RESTORE_DRILL_REAL_DOCKER="$(command -v docker)"
export RESTORE_DRILL_REAL_DOCKER RESTORE_DRILL_SITE="$DIR" RESTORE_DRILL_WORK="$WORK"
mkdir "$WORK/drill-bin"
cp "$ROOT/deploy/digitalocean/testdata/restore-drill-docker.sh" "$WORK/drill-bin/docker"
cat >"$WORK/drill-bin/sync" <<'SYNC'
#!/usr/bin/env bash
if [[ -f "$RESTORE_DRILL_WORK/fail-floor" && "$*" == *recovery-policy-floor* ]]; then exit 1; fi
if [[ -x /usr/bin/sync ]]; then exec /usr/bin/sync "$@"; fi
exec /bin/sync "$@"
SYNC
chmod 755 "$WORK/drill-bin/"*
export PATH="$WORK/drill-bin:$PATH"
exec > >(tee "$WORK/restore-transcript.log") 2>&1
holder=""
monitor=""
# shellcheck disable=SC2329 # Invoked by the EXIT trap.
cleanup() {
  local rc=$?
  trap - EXIT
  [[ "$MODE" == full ]] || rc="$(drill_partial_rc "$rc")"
  [[ -z "$holder" ]] || kill -KILL "$holder" 2>/dev/null || true
  [[ -z "$monitor" ]] || kill "$monitor" 2>/dev/null || true
  if [[ -n "${SQL_HOLDER:-}" && -n "${SQL_HOLD_LABEL:-}" ]]; then
    # shellcheck disable=SC2016 # $1 belongs to the cleanup child.
    timeout 3 bash -c 'echo continue >"$1"' _ "$WORK/holds/$SQL_HOLD_LABEL.fifo" 2>/dev/null || true
  fi
  # Level-0 lineage sites and the host files their provisioner writes.
  if (( ! KEEP )); then crossing_cleanup_all || rc=1; else crossing_cleanup_host || rc=1; fi
  # The container-local OS identity the real_* cases presented is put back (and recorded) whether the run passed or failed.
  [[ -z "${CROSSING_OS_STATE:-}" ]] || isolation_os_restore "$CROSSING_OS_STATE" || rc=1
  if (( ! KEEP )); then docker compose --project-directory "$DIR" down -v --remove-orphans || rc=1; fi
  drill_emit result "$rc" || rc=1
  echo "restore drill result=$rc artifacts=$WORK retained_stack=$KEEP"
  [[ "$MODE" == full ]] || drill_mode_finish "$rc"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'drill_progress_error "$LINENO" "$BASH_COMMAND" "$?"' ERR
[[ "$MODE" == full ]] || drill_mode_begin
BASE_CASE="base-proof"
[[ "$MODE" == full ]] || BASE_CASE="base-prerequisites"
drill_case_begin "$BASE_CASE"

dc() { docker compose --project-directory "$DIR" "$@"; }
pg() { dc exec -T db psql -X -U church -d "$1" -At -v ON_ERROR_STOP=1 "${@:2}"; }
restore() { drill_note "restore/${1#--}"; RESTORE_DRILL_OPERATION=1 "$DIR/restore.sh" --deploy-dir "$DIR" --drill "$@"; }
phase() { jq -er .phase "$DIR/recovery.lock"; }
nonce_db() { jq -r --arg prefix "$1" '$prefix + (.nonce[0:16])' "$DIR/recovery.lock"; }
recovery_ids() { docker ps -aq --filter "label=com.docker.compose.project=$PROJECT" --filter label=com.docker.compose.service=recovery; }
port_open() {
  # shellcheck disable=SC2016 # The positional parameter belongs to child Bash.
  timeout 2 bash -c 'exec 3<>/dev/tcp/127.0.0.1/"$1"' _ "$1" >/dev/null 2>&1
}
public_url() { local port; port="$(dc port caddy 80)"; printf 'http://localhost:%s' "${port##*:}"; }
wait_public() {
  local end=$((SECONDS+90))
  while (( SECONDS < end )); do
    if curl -fsS "$(public_url)/healthz" >/dev/null; then return 0; fi
    sleep 1
  done
  return 1
}
JAR="$WORK/cookies"
URL="$(public_url)"
csrf() { curl -fsS -b "$JAR" -c "$JAR" "$URL/api/v1/auth/csrf" | jq -er .csrfToken; }
api() {
  local method="$1" path="$2" data="${3-}" token
  [[ -n "$data" ]] || data='{}'
  token="$(csrf)"
  curl -fsS -b "$JAR" -c "$JAR" -X "$method" -H "X-CSRF-Token: $token" -H 'Content-Type: application/json' --data-binary "$data" "$URL/api/v1$path"
}
login() {
  rm -f "$JAR"
  api POST /auth/login '{"email":"admin@example.invalid","password":"restore-drill-password"}' >/dev/null
}
setup() {
  local token
  curl -fsS -b "$JAR" -c "$JAR" "$URL/setup" >"$WORK/setup.html"
  token="$(sed -n 's/.*name="csrf_token" value="\([^"]*\)".*/\1/p' "$WORK/setup.html" | head -n 1)"
  [[ -n "$token" ]]
  curl -fsS -b "$JAR" -c "$JAR" -H "X-CSRF-Token: $token" --data-urlencode "csrf_token=$token" \
    --data-urlencode setup_token=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$URL/setup/unlock" >/dev/null
  curl -fsS -b "$JAR" -c "$JAR" "$URL/setup" >"$WORK/setup.html"
  token="$(sed -n 's/.*name="csrf_token" value="\([^"]*\)".*/\1/p' "$WORK/setup.html" | head -n 1)"
  curl -fsS -b "$JAR" -c "$JAR" -H "X-CSRF-Token: $token" \
    --data-urlencode "csrf_token=$token" --data-urlencode site_name='Restore drill' --data-urlencode timezone=UTC \
    --data-urlencode name='Drill Admin' --data-urlencode email=admin@example.invalid --data-urlencode age_band=adult \
    --data-urlencode password=restore-drill-password --data-urlencode password_confirm=restore-drill-password "$URL/setup" >/dev/null
  login
}
new_user() {
  api POST /admin/users "$(jq -n --arg name "$1" --arg email "$2" '{name:$name,email:$email,password:"restore-drill-password",ageBand:"adult",role:"member"}')" | jq -er .user.id
}
delete_a() { api DELETE "/admin/users/$A" '{}' >"$WORK/delete-response.json"; }
expect_fail() {
  local needle="$1" rc=0; shift
  "$@" >"$WORK/refusal.log" 2>&1 || rc=$?
  cat "$WORK/refusal.log"
  [[ "$rc" != 0 ]]
  [[ -z "$needle" ]] || grep -qF "$needle" "$WORK/refusal.log"
}
observe_services() {
  local ids
  ids="$(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT")"
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    docker inspect --format '{{.Id}}|{{index .Config.Labels "com.docker.compose.service"}}|{{.State.Status}}|{{.State.Running}}|{{.HostConfig.RestartPolicy.Name}}|{{.Image}}|{{json .NetworkSettings.Ports}}|{{json .Config.Env}}' "$id"
  done <<<"$ids"
  pg postgres -c "SELECT datname,pid,application_name,state FROM pg_stat_activity WHERE backend_type='client backend' ORDER BY datname,pid"
}
assert_closed() {
  local id
  for id in $(dc ps -a -q caddy app); do [[ "$(docker inspect --format '{{.State.Running}}' "$id")" == false ]]; done
  [[ -z "$(recovery_ids)" ]]
  if port_open "$PUBLIC_PORT"; then echo "public port still open" >&2; return 1; fi
}
assert_private() {
  local id image running
  [[ "$(phase)" == recovering ]]
  for id in $(dc ps -a -q caddy app); do [[ "$(docker inspect --format '{{.State.Running}}' "$id")" == false ]]; done
  id="$(recovery_ids)"; [[ -n "$id" && "$id" != *$'\n'* ]]
  image="$(jq -er .imageId "$DIR/recovery.lock")"
  [[ "$(docker inspect --format '{{.Image}}|{{.HostConfig.RestartPolicy.Name}}|{{index .Config.Labels "fold.recovery.nonce"}}' "$id")" == "$image|no|$(jq -er .nonce "$DIR/recovery.lock")" ]]
  docker inspect --format '{{json .Config.Env}}' "$id" | jq -e 'index("FOLD_WORKERS=0")!=null and index("COOKIE_SECURE=true")!=null and index("APP_ENV=prod")!=null' >/dev/null
  docker inspect --format '{{json .HostConfig.PortBindings}}' "$id" | jq -e 'keys==["8080/tcp"] and .["8080/tcp"]==[{HostIp:"127.0.0.1",HostPort:"18080"}]' >/dev/null
  if port_open "$PUBLIC_PORT"; then echo "public port still open" >&2; return 1; fi
  curl -fsS http://localhost:18080/healthz >/dev/null
  running="$(docker ps -q --filter "label=com.docker.compose.project=$PROJECT" --filter "ancestor=$image" | wc -l)"
  [[ "$running" == 1 ]]
  if [[ "$(pg fold -c "SELECT to_regclass('drill_sentinel') IS NOT NULL")" == t ]]; then
    [[ "$(pg fold -c 'SELECT value FROM drill_sentinel WHERE id=2')" == archive ]]
  fi
  observe_services
}
DB_DUMP="$WORK/base.dump"
PUBLIC_PORT="$(dc port caddy 80)"; PUBLIC_PORT="${PUBLIC_PORT##*:}"
SMTP_PORT="$(dc port smtp 8025)"; SMTP_PORT="${SMTP_PORT##*:}"
smtp_clear() { curl -fsS -X DELETE "http://localhost:$SMTP_PORT/api/v1/messages" >/dev/null; }
smtp_json() { curl -fsS "http://localhost:$SMTP_PORT/api/v1/messages"; }
setup
A="$(new_user 'Member A' a@example.invalid)"
M="$(new_user 'Member M' m@example.invalid)"
B="$(new_user 'Member B' b@example.invalid)"
C="$(new_user 'Member C' c@example.invalid)"
GROUP="$(api POST /groups '{"name":"Restore race context","description":"Disposable fixture","visibility":"public","tags":[]}' | jq -er .group.id)"
api POST "/admin/groups/$GROUP/members" "{\"userId\":$B,\"role\":\"member\"}" >/dev/null
pg fold -c "INSERT INTO drill_sentinel VALUES (2,'archive')"
pg fold -c 'CREATE TABLE drill_job_keys(key text PRIMARY KEY)'
ADMIN="$(api GET /auth/me '{}' | jq -er .user.id)"
# Keep jobs queued through the dump and initial erasure; the archive contains
# current queued mail that the private recovery instance must never claim.
# These are explicit fixture rows; the accounts and erasures use real routes.
pg fold -c "INSERT INTO jobs(kind,payload,run_at,max_attempts,unique_key) VALUES
 ('account.password_reset_mail',json_build_object('userId',$A),now()+interval '1 day',8,'drill:a-reset'),
 ('account.password_reset_mail',json_build_object('userId',$M),now()+interval '1 day',8,'drill:m-reset')"
# Notification and announcement delivery intent use the production schemas.
pg fold <<SQL
WITH n AS (
 INSERT INTO notifications(user_id,kind,category,title,email_title,email_body,link)
 VALUES ($A,'group.approved','groups','Drill notice','Drill notice','A restore drill notice','/') RETURNING id
), d AS (
 INSERT INTO notification_deliveries(notification_id,user_id,channel,status)
 SELECT id,$A,'email','queued' FROM n RETURNING notification_id
)
INSERT INTO jobs(kind,payload,run_at,max_attempts,unique_key)
SELECT 'notify.deliver',json_build_object('notificationId',notification_id),now()+interval '1 day',8,'notify.deliver:'||notification_id FROM d;
INSERT INTO drill_job_keys SELECT unique_key FROM jobs WHERE unique_key LIKE 'notify.deliver:%';
WITH msg AS (
 INSERT INTO messages(author_id,status,subject,body_html,body_text,audience,push_alert)
 VALUES ($ADMIN,'sending','Drill announcement','<p>Drill</p>','Drill','{"kind":"users","userIds":[$A]}',false) RETURNING id
), recipient AS (
 INSERT INTO message_recipients(message_id,user_id,channel,address,status)
 SELECT id,$A,'email','a@example.invalid','queued' FROM msg RETURNING id,message_id
)
INSERT INTO jobs(kind,payload,run_at,max_attempts,unique_key)
SELECT 'message.deliver_batch',json_build_object('messageId',message_id,'recipientIds',json_build_array(id)),now()+interval '1 day',8,'drill:a-message' FROM recipient;
INSERT INTO drill_job_keys SELECT unique_key FROM jobs WHERE unique_key LIKE 'drill:%' ON CONFLICT DO NOTHING;
SQL
seed_push_jobs
# Same custom-format archive as backup.sh. Fixture ids never leave WORK.
dc exec -T db pg_dump -U church -Fc fold >"$DB_DUMP"
sha256sum "$DB_DUMP"
# Everything from here to the end of base-proof (the restore proof itself) is the full run's; the diagnostic goes on to the crossing cases below.
if [[ "$MODE" == full ]]; then
pg fold -c "CREATE TABLE drill_live_only(value text); INSERT INTO drill_live_only VALUES ('live-only'); CREATE VIEW drill_live_view AS SELECT id FROM users"
delete_a
capture_original
smtp_clear
monitor_start
# Whole replacement and PostgreSQL transactional rename proof, including a
# failing second rename rolling the first back. Names are scratch-only.
pg postgres -c 'CREATE DATABASE drill_swap_one' -c 'CREATE DATABASE drill_swap_collision'
if pg postgres --single-transaction -c 'ALTER DATABASE drill_swap_one RENAME TO drill_swap_temp' -c 'ALTER DATABASE drill_swap_temp RENAME TO drill_swap_collision'; then echo 'expected second rename failure' >&2; exit 1; fi
[[ "$(pg postgres -c "SELECT count(*) FROM pg_database WHERE datname='drill_swap_one'")" == 1 ]]
[[ "$(pg postgres -c "SELECT count(*) FROM pg_database WHERE datname='drill_swap_temp'")" == 0 ]]
pg postgres --single-transaction -c 'ALTER DATABASE drill_swap_one RENAME TO drill_swap_temp' -c 'ALTER DATABASE drill_swap_temp RENAME TO drill_swap_one'
pg postgres -c 'DROP DATABASE drill_swap_one' -c 'DROP DATABASE drill_swap_collision'
restore --dump "$DB_DUMP"
[[ "$(jq -er .count "$DIR/recovery.lock")" == 1 ]]
assert_private
assert_original_heap
OLD="$(nonce_db fold_pre_restore_)"
[[ "$(pg postgres -c "SELECT datallowconn FROM pg_database WHERE datname='$OLD'")" == f ]]
[[ "$(pg fold -c "SELECT to_regclass('drill_live_only') IS NULL AND to_regclass('drill_live_view') IS NULL")" == t ]]
# Jobs due now stay unclaimed for 60 seconds with workers disabled. The
# scheduler may add rows; compare these fixture jobs by unique_key only.
pg fold -c "UPDATE jobs SET run_at=now() WHERE unique_key IN (SELECT key FROM drill_job_keys)"
pg fold -c "SELECT unique_key,status,attempts FROM jobs WHERE unique_key IN (SELECT key FROM drill_job_keys) ORDER BY unique_key" >"$WORK/queued.before"
smtp_json >"$WORK/smtp.before"
drill_step closed-observation-60s
sleep 60
pg fold -c "SELECT unique_key,status,attempts FROM jobs WHERE unique_key IN (SELECT key FROM drill_job_keys) ORDER BY unique_key" >"$WORK/queued.after"
cmp "$WORK/queued.before" "$WORK/queued.after"
[[ "$(smtp_json | jq -er .total)" == 0 ]]
# A db restart must not start normal services or claim these jobs.
dc restart db
for ((i=0;i<90;i++)); do if curl -fsS http://localhost:18080/healthz >/dev/null; then break; fi; sleep 1; done
assert_private
pg fold -c "SELECT unique_key,status,attempts FROM jobs WHERE unique_key IN (SELECT key FROM drill_job_keys) ORDER BY unique_key" >"$WORK/queued.after-restart"
cmp "$WORK/queued.before" "$WORK/queued.after-restart"
expect_fail checked-church-records restore --reopen
# Browser uses real Secure cookies over localhost and the real delete panel.
# Resolve Playwright from web/, without adding a repository test file.
cp "$ROOT/deploy/digitalocean/testdata/restore-browser.cjs" "$WORK/private-browser.cjs"
RESTORE_WEB_PACKAGE="$ROOT/web/package.json" RESTORE_BROWSER_LAUNCH="$ROOT/deploy/digitalocean/qc1/browser-launch.cjs" RESTORE_SCREENSHOT="$WORK/recovery-delete-390.png" \
  RESTORE_BROWSER_PROOF="$WORK/chromium-cookie-proof.json" RESTORE_TARGET_ID="$A" RESTORE_BROWSER_ARTIFACTS="$WORK" \
  DEBUG="${DEBUG:+$DEBUG,}pw:browser" node "$WORK/private-browser.cjs"
[[ "$(pg fold -c "SELECT deleted_at IS NOT NULL FROM users WHERE id=$A")" == t ]]
RECOVERY_IMAGE="$(jq -er .imageId "$DIR/recovery.lock")"
restore --reopen --checked-church-records
[[ ! -e "$DIR/recovery.lock" && -z "$(recovery_ids)" ]]
[[ "$(pg postgres -c "SELECT count(*) FROM pg_database WHERE datname='$OLD'")" == 0 ]]
[[ "$(docker inspect --format '{{.Image}}' "$(dc ps -q app)")" == "$RECOVERY_IMAGE" ]]
monitor_stop
for ((i=0;i<90;i++)); do
  [[ "$(pg fold -c "SELECT count(*) FROM jobs WHERE unique_key IN (SELECT key FROM drill_job_keys) AND status<>'done'")" != 0 ]] || break
  sleep 1
done
[[ "$(pg fold -c "SELECT count(*) FROM jobs WHERE unique_key IN (SELECT key FROM drill_job_keys) AND status<>'done'")" == 0 ]]
smtp_json >"$WORK/smtp.reopened.json"
dc logs --no-log-prefix push-sink >"$WORK/push.reopened.log"
grep -q "POST /device-$M " "$WORK/push.reopened.log"
if grep -q "POST /device-$A " "$WORK/push.reopened.log"; then echo 'ARBITRATE: restored push work sent to erased A' >&2; exit 78; fi
jq -e '[.messages[].To[]?.Address] | index("a@example.invalid")==null and index("m@example.invalid")!=null' "$WORK/smtp.reopened.json" >/dev/null \
  || { echo 'ARBITRATE: queued work sent to erased A, or M delivery failed; lead must investigate CORE send-time rechecks' >&2; exit 78; }

fi
drill_case_end

# Reset only the stack this runner created. This is fixture setup, not an
# alternative operator restore path. Never remove ops.lock, even in a drill.
reset_case() {
  # The optional argument pins another image for a real-binary case; every
  # other case keeps the fixture base image.
  local ids name image="${1:-$BASE_REGISTRY_IMAGE}"
  drill_note reset/start
  [[ "$DIR" == "$WORK/site" && "$PROJECT" == fold-deploy-drill-* ]] || exit 1
  dc stop caddy app
  ids="$(recovery_ids)"
  for name in $ids; do docker rm -f "$name"; done
  reset_fixture_databases || return 1
  pg postgres -c 'CREATE DATABASE fold OWNER church TEMPLATE template0'
  dc exec -T db pg_restore -U church -d fold --no-owner --no-acl --single-transaction --exit-on-error <"$DB_DUMP"
  rm -f "$DIR/recovery.lock" "$DIR/drill-smoke-fail" "$DIR/drill.ready"
  drill_note reset/provision
  "$DIR/provision.sh" --skip-harden --allow-os --deploy-dir "$DIR" --source-dir "$SOURCE" --domain groups.example.org --image "$image" >"$WORK/reset-provision.log" 2>&1
  drill_note reset/provision-complete
  wait_public
  URL="$(public_url)"; login; delete_a; capture_original; smtp_clear
}
kill_point() {
  local point="$1" rc=0; shift
  drill_note "fault/$point"
  if [[ "$point" == mid-smoke || "$point" == mid-abort-smoke ]]; then
    if [[ "$point" == mid-smoke ]]; then smoke_interruption "$point" --reopen; else smoke_interruption "$point" --abort; fi
    restore --status | tee "$WORK/status-$point.log"
    return
  fi
  env RESTORE_DRILL_OPERATION=1 RESTORE_DRILL_FAULT="$point" "$DIR/restore.sh" --deploy-dir "$DIR" --drill "$@" >"$WORK/fault-$point.log" 2>&1 || rc=$?
  [[ "$rc" == 137 ]]
  cat "$WORK/fault-$point.log"
  restore --status | tee "$WORK/status-$point.log"
  observe_services
  if [[ -n "$(pg postgres -c "SELECT oid FROM pg_database WHERE oid=$(jq -er .origOid "$DIR/recovery.lock")")" ]]; then assert_original_heap
  else [[ "$(jq -er .cleanup "$DIR/recovery.lock")" != none ]]; fi
}
# crossings-only: the fixture stack, the dump and reset_case exist now. Run the five real-binary crossing cases and stop: the recovery matrices below are the full run's.
# A bare call, so errexit stays on inside it; it exits with DRILL_PARTIAL_PASS_EXIT when the diagnostic passes and never returns.
if [[ "$MODE" != full ]]; then
crossings_only_run
fi
# Each kill uses the installed entry point. SQL logs and lock histories
# remain available to check that observed oids authorize every action.
pg postgres -c "ALTER SYSTEM SET log_statement='all'" -c 'SELECT pg_reload_conf()'
drill_case_begin recovery-interruptions
for point in after-create before-swap swap-ack-lost after-swap after-recovery-create; do
  drill_step "$point"
  reset_case
  kill_point "$point" --dump "$DB_DUMP"
  restore --resume
  assert_private
  URL=http://localhost:18080; login; delete_a
  restore --reopen --checked-church-records
done
reset_case
rc=0
drill_step half-and-curl
RESTORE_DRILL_FORCE_TWO_STEP=1 RESTORE_DRILL_FAULT=half restore --dump "$DB_DUMP" >"$WORK/fault-half.log" 2>&1 || rc=$?
[[ "$rc" == 137 ]]
restore --status | tee "$WORK/status-half.log"
expect_fail 'abort refused' restore --abort
restore --resume
assert_private
# Curl cookie-jar proof on a second complete restore.
URL=http://localhost:18080; login
awk '!/^#/ || /^#HttpOnly_/ {print $1,$3,$4,$6}' "$JAR" >"$WORK/curl-cookie-flags.txt"
grep -q TRUE "$WORK/curl-cookie-flags.txt"
delete_a
restore --reopen --checked-church-records
drill_case_end
drill_case_begin reopen-interruptions
for point in after-app-start after-caddy-start mid-smoke before-drop after-drop after-floor; do
  drill_step "$point"
  reset_case
  restore --dump "$DB_DUMP"
  kill_point "$point" --reopen --checked-church-records
  expect_fail 'abort refused' restore --abort
  if [[ "$point" == before-drop || "$point" == after-drop || "$point" == after-floor ]]; then
    touch "$DIR/drill-smoke-fail"
    expect_fail reopen-failed restore --resume
    assert_closed
    rm "$DIR/drill-smoke-fail"
  fi
  restore --resume
  assert_private
  monitor_start; sleep 3; monitor_stop; assert_no_claims
  if [[ "$point" == after-drop || "$point" == after-floor ]]; then [[ "$(jq -er .cleanup "$DIR/recovery.lock")" == dropped ]]; fi
  restore --reopen --checked-church-records
done
# Abort before swap, including interruption around the authorized R drop.
drill_case_end
drill_case_begin abort-interruptions
for point in after-abort-intent before-abort-drop after-abort-drop after-abort-start mid-abort-smoke; do
  drill_step "$point"
  reset_case
  kill_point after-create --dump "$DB_DUMP"
  kill_point "$point" --abort
  expect_fail 'continue with --abort' restore --resume
  restore --abort
  [[ ! -e "$DIR/recovery.lock" && -z "$(recovery_ids)" ]]
  [[ "$(pg fold -c "SELECT deleted_at IS NOT NULL FROM users WHERE id=$A")" == t ]]
done
# Refused identity tampering never writes to a database. Closing containers
# is permitted by E2, so compare the catalog directly rather than files.
drill_case_end
drill_case_begin tamper
for tamper in missing-original missing-restored extra-nonce; do
  drill_step "$tamper"
  reset_case
  if [[ "$tamper" == missing-original ]]; then
    restore --dump "$DB_DUMP"
    old="$(nonce_db fold_pre_restore_)"
    pg postgres -c "ALTER DATABASE \"$old\" RENAME TO drill_saved_original"
  else
    kill_point before-swap --dump "$DB_DUMP"
    if [[ "$tamper" == missing-restored ]]; then
      old="$(nonce_db fold_restore_)"; pg postgres -c "DROP DATABASE \"$old\""
    else pg postgres -c 'CREATE DATABASE fold_restore_ffffffffffffffff'; fi
  fi
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/tamper.before"
  expect_fail reconcile-needed restore --resume
  expect_fail reconcile-needed restore --abort
  pg postgres -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' >"$WORK/tamper.after"
  cmp "$WORK/tamper.before" "$WORK/tamper.after"
  [[ "$tamper" != missing-original ]] || pg postgres -c 'DROP DATABASE drill_saved_original'
done
drill_case_end
drill_case_begin accidental-app-reuse
# An accidental normal app alongside a healthy recovery instance must be
# closed, and that recovery container replaced before W=0 is asserted.
reset_case
restore --dump "$DB_DUMP"
assert_private
before="$(recovery_ids)"
wait_recovery_healthy "$before" | tee "$WORK/accidental-app.health"
URL=http://localhost:18080; login
assert_recovery_clients_present | tee -a "$WORK/accidental-app.health"
dc up -d --no-deps app
restore --status | tee "$WORK/accidental-app.status"
grep -q WARNING "$WORK/accidental-app.status"
restore --resume | tee "$WORK/accidental-app.resume"
grep -q 'observed W=0' "$WORK/accidental-app.resume"
[[ "$(recovery_ids)" != "$before" ]]
assert_private
# A healthy normal resume reuses the container and skips W=0. The new
# container's direct probe may pass while Docker still reports starting.
before="$(recovery_ids)"
assert_healthy_recovery_reuse "$before" healthy-resume
drill_case_end
drill_case_begin abort-ack-loss
# Lost abort-drop acknowledgement accepts Q only under its kept intent.
reset_case
kill_point after-create --dump "$DB_DUMP"
expect_fail abort-failed env RESTORE_DRILL_FAULT=abort-drop-ack-lost "$DIR/restore.sh" --deploy-dir "$DIR" --drill --abort
[[ "$(phase)" == abort-failed && "$(jq -er .at "$DIR/recovery.lock")" == drop ]]
restore --abort

drill_case_end
drill_case_begin operations-lock
# Lock losers observe no state and cannot install or start anything.
reset_case
[[ -p "$DIR/drill.fifo" ]] || mkfifo "$DIR/drill.fifo"
RESTORE_DRILL_PAUSE=after-lock "$DIR/restore.sh" --deploy-dir "$DIR" --drill --dump "$DB_DUMP" >"$WORK/lock-winner.log" 2>&1 &
holder=$!
for ((i=0;i<1200;i++)); do [[ ! -f "$DIR/drill.ready" ]] || break; kill -0 "$holder"; sleep .05; done
[[ "$(cat "$DIR/drill.ready")" == after-lock ]]
for mode in dump resume abort reopen; do
  drill_step "$mode"
  mode_args=("--$mode"); [[ "$mode" != dump ]] || mode_args+=("$DB_DUMP")
  snapshot_state "$DIR" "$SOURCE" >"$WORK/loser.before"
  rc=0; restore "${mode_args[@]}" >"$WORK/loser.log" 2>&1 || rc=$?
  [[ "$rc" == 75 ]]
  snapshot_state "$DIR" "$SOURCE" >"$WORK/loser.after"
  cmp "$WORK/loser.before" "$WORK/loser.after"
done
kill -KILL "$holder"; wait "$holder" 2>/dev/null || true; holder=""
timeout 15 flock -x "$DIR/ops.lock" true
rm -f "$DIR/drill.ready"
restore --dump "$DB_DUMP"
assert_private
drill_case_end
drill_case_begin recovery-configuration
# Recovery service config and plain up exclusion. No recovery service is
# present in the base Compose definition, nor enabled by its default profiles.
FOLD_RECOVERY_IMAGE="$(jq -er .imageId "$DIR/recovery.lock")" FOLD_RECOVERY_NONCE="$(jq -er .nonce "$DIR/recovery.lock")" \
  dc -f "$DIR/docker-compose.yml" -f "$DIR/compose.recovery.yml" --profile recovery config --format json >"$WORK/recovery-config.json"
jq -e '.services.recovery.restart=="no" and .services.recovery.environment.FOLD_WORKERS=="0" and .services.recovery.profiles==["recovery"]' "$WORK/recovery-config.json" >/dev/null
id="$(recovery_ids)"; docker kill "$id"; dc restart db
restore --resume
assert_private
[[ "$(recovery_ids)" != "$id" ]]
URL=http://localhost:18080; login; delete_a
restore --reopen --checked-church-records
# Runtime files survive image and build pinning; SOURCE is only data/context.
for file in $(runtime_tools); do cmp "$DIR/$file" "$DIR/recovery-tools/$file"; done
snapshot_state "$DIR" "$SOURCE" >"$WORK/final-state"
dc logs db >"$WORK/postgres-statements.log"
drill_case_end
for drill_family in count_races archive_failures pin_and_abort_cases cleanup_and_tamper_cases concurrency_cases lifecycle_cases \
  legacy_archive_case review_reconciliation_cases review_backup_archive_cases review_cleanup_retry_cases \
  review_orphaned_restore_case review_moving_tag_case \
  real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore; do
  drill_case_begin "$drill_family"
  # A bare call preserves errexit inside the acceptance function. Never put
  # this call in an if/! or || list: Bash would disable its failed assertions.
  "$drill_family"
  drill_case_end
done
drill_case_begin final-verification
snapshot_state "$DIR" "$SOURCE" >"$WORK/final-state"
dc logs db >"$WORK/postgres-statements.log"
assert_statement_history
assert_crossing_evidence
drill_case_end
drill_progress_complete
printf 'restore drill passed: revision=%s project=%s artifacts=%s\n' "$(jq -er .revision "$MANIFEST")" "$PROJECT" "$WORK"
