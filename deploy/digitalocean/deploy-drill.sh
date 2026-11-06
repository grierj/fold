#!/usr/bin/env bash
# Disposable, local Docker drill for the installed deployment entry points.
# Run as root on a Docker development host. Never run against an owner stack.
# --keep retains the stack and transcript for inspection or the restore drill.
set -euo pipefail

DRILL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

# Shared with the later restore drill. Every database observation uses this
# project's db service, never a hard-coded container name.
snapshot_state() {
  local dir="$1" source="$2" project ids id
  project="$(env_value "$dir/.env" COMPOSE_PROJECT_NAME)"
  (
    cd "$dir"
    find . -type f ! -name ops.lock ! -name ops.owner \
      -print0 | sort -z | xargs -0 -r sha256sum
  )
  docker ps -a --no-trunc --filter "label=com.docker.compose.project=$project" \
    --format '{{.ID}}|{{.Image}}|{{.State}}' | sort
  ids="$(docker ps -aq --filter "label=com.docker.compose.project=$project")"
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    docker inspect --format '{{.Id}}|{{.Image}}|{{.State.Status}}|{{.State.Running}}|{{.State.Paused}}|{{.State.Restarting}}|{{.State.Dead}}|{{.State.ExitCode}}|{{json .HostConfig.RestartPolicy}}|{{json .Config.Labels}}' "$id"
  done <<<"$ids"
  docker compose --project-directory "$dir" images -q | sort -u
  docker compose --project-directory "$dir" exec -T db psql -X -U church -d fold -At -v ON_ERROR_STOP=1 \
    -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' \
    -c 'SELECT * FROM drill_sentinel ORDER BY id' \
    -c 'SELECT id,version_id,is_applied FROM goose_db_version ORDER BY id'
  git -C "$source" rev-parse HEAD
  git -C "$source" status --porcelain
  if [[ -d "$dir/dumps" ]]; then
    find "$dir/dumps" -type f -printf '%P\n' | sort
  fi
}

# Sourcing this file is an explicit library interface, not a second runner.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

# shellcheck source=deploy/digitalocean/checks.sh
source "$DRILL_ROOT/deploy/digitalocean/checks.sh"
KEEP=0
BASE_IMAGE=""
WORK=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) KEEP=1; shift ;;
    --image) BASE_IMAGE="${2:?--image needs a reference}"; shift 2 ;;
    --dir) WORK="${2:?--dir needs an empty scratch directory}"; shift 2 ;;
    --help) echo 'Usage: sudo deploy-drill.sh [--image BASE_IMAGE] [--dir EMPTY_DIR] [--keep]'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 1 ;;
  esac
done
[[ "$(id -u)" == 0 ]] || { echo 'deploy-drill.sh requires root for the provisioner' >&2; exit 1; }
for command in docker go git jq curl flock timeout; do command -v "$command" >/dev/null; done
if [[ -n "$WORK" ]]; then
  [[ "$WORK" == /* ]] || { echo '--dir must be a new absolute scratch path' >&2; exit 1; }
  WORK="$(realpath -m "$WORK")"
  [[ "$WORK" != /opt/fold && "$WORK" != /opt/fold/* && ! -e "$WORK" ]] || {
    echo '--dir must be a new scratch path outside /opt/fold' >&2; exit 1;
  }
  mkdir -m 700 "$WORK"
else
  WORK="$(mktemp -d /tmp/fold-deploy-drill.XXXXXX)"
fi
WORK="$(cd "$WORK" && pwd -P)"
[[ "$WORK" != /opt/fold && "$WORK" != /opt/fold/* ]] || { echo 'refusing /opt/fold' >&2; exit 1; }
DIR="$WORK/site"
SOURCE="$WORK/source"
TOOLS="$WORK/tools"
PROJECT="fold-deploy-drill-$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
mkdir -p "$DIR" "$SOURCE" "$TOOLS"
printf 'fold-deploy-drill-v1:%s\n' "$DIR" >"$DIR/.deploy-drill"
chmod 600 "$DIR/.deploy-drill"
export DEPLOY_DRILL=1
unset DEPLOY_DRILL_PAUSE
# Do not let an invoking shell redirect Compose into another project/daemon.
unset COMPOSE_FILE COMPOSE_PROJECT_NAME COMPOSE_PROFILES
mapfile -t drill_vars < <(grep -oE '\$\{[A-Za-z_][A-Za-z0-9_]*' "$DRILL_ROOT/docker-compose.yml" | sed 's/^\${//' | sort -u)
for name in "${drill_vars[@]}"; do unset "$name"; done
exec > >(tee "$WORK/transcript.log") 2>&1
started=0
holder=""
descendant=""
cleanup() {
  local rc=$?
  trap - EXIT
  [[ -z "$holder" ]] || kill -KILL "$holder" 2>/dev/null || true
  [[ -z "$descendant" ]] || kill -KILL "$descendant" 2>/dev/null || true
  if (( ! KEEP && started )); then
    docker compose --project-directory "$DIR" -f "$SOURCE/docker-compose.yml" down -v --remove-orphans || rc=1
  fi
  echo "drill result=$rc artifacts=$WORK retained_stack=$KEEP"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cp "$DRILL_ROOT"/deploy/digitalocean/{provision.sh,upgrade.sh,checks.sh,backup.sh,smoke.sh,email-dns.sh,backup.env.example,deploy-tools-version} "$TOOLS/"
# A local HTTP proxy replaces public TLS and host SSH/firewall checks. This is
# a test tool-set file; the enforcing upgrade/provision scripts stay unchanged.
cat >"$TOOLS/smoke.sh" <<'SMOKE'
#!/usr/bin/env bash
set -euo pipefail
while [[ $# -gt 0 ]]; do
  if [[ "$1" == --deploy-dir ]]; then dir="$2"; shift 2; else shift; fi
done
[[ ! -e "$dir/drill-smoke-fail" ]] || exit 1
port="$(docker compose --project-directory "$dir" port caddy 80)"
for ((i=0; i<90; i++)); do
  if curl -fsS --max-time 2 "http://$port/healthz" >/dev/null; then exit 0; fi
  sleep 1
done
exit 1
SMOKE
chmod 755 "$TOOLS/"*.sh
cp "$DRILL_ROOT"/{docker-compose.yml,Caddyfile,.env.example} "$SOURCE/"
# Build a fixture checkout whose provisioner is deliberately unsafe. It is
# supplied as data/build context only and must never become the running tool.
mkdir -p "$SOURCE/deploy/digitalocean"
printf '#!/bin/bash\ntouch "%s"\n' "$WORK/bypass-marker" >"$SOURCE/deploy/digitalocean/provision.sh"
chmod 755 "$SOURCE/deploy/digitalocean/provision.sh"
cat >"$SOURCE/drill-nginx.conf" <<'NGINX'
server { listen 80; resolver 127.0.0.11 valid=1s; location / { set $fold http://app:8080; proxy_pass $fold; } }
NGINX
# Merge the isolated proxy, SMTP and registry into the target tree. Compose
# reads only this file for both pull and build deployments, so pin changes do
# not drop the isolation overrides.
DB_PASSWORD=drill SESSION_SECRET=drill-session-secret-at-least-32-characters DOMAIN=groups.example.org FOLD_VERSION=drill \
  docker compose -f "$DRILL_ROOT/docker-compose.yml" -f "$DRILL_ROOT/deploy/compose.deploy-drill.yml" \
  config --format json >"$WORK/config.json"
jq --arg dir "$DIR" --arg project "$PROJECT" '.services.caddy.volumes = [{type:"bind",source:($dir + "/drill-nginx.conf"),target:"/etc/nginx/conf.d/default.conf",read_only:true}]
  | .services.caddy.ports = [{target:80,host_ip:"127.0.0.1",protocol:"tcp"}]
  | .services.registry.ports = [{target:5000,host_ip:"127.0.0.1",protocol:"tcp"}]
  | .services.smtp.ports = [{target:8025,host_ip:"127.0.0.1",protocol:"tcp"}]
  | .services.app.environment.DATABASE_URL = "postgres://church:${DB_PASSWORD}@db:5432/fold?sslmode=disable"
  | .services.app.environment.SESSION_SECRET = "${SESSION_SECRET}"
  | .services.app.environment.FOLD_TRUSTED_PROXIES = "${FOLD_TRUSTED_PROXIES:-}"
  | .services.db.environment.POSTGRES_PASSWORD = "${DB_PASSWORD}"
  | .services.app.environment.FOLD_SMTP_URL = "smtp://smtp:1025"
  | .services.app.environment.FOLD_SMTP_ALLOW_INSECURE = "true"
  | .services.app.environment.FOLD_EMAIL_FROM = "Fold <fold@example.invalid>"
  | .services.app.image = "ghcr.io/grierj/fold:${FOLD_VERSION}"
  | del(.networks.default.ipam) | .networks.default.name = ($project + "_default")
  | .name = $project
  | .volumes |= with_entries(.value = {name:($project + "_" + .key)})' "$WORK/config.json" >"$WORK/compose.json"
# JSON is valid YAML, but the provisioner deliberately requires the shipped
# app image line. Use Compose's YAML renderer after clearing resolved names.
docker compose -f "$WORK/compose.json" config --no-interpolate >"$SOURCE/docker-compose.yml"
# Compose quotes values containing interpolation. Normalize only the image
# line the provisioner recognizes; no other service uses this repository.
# shellcheck disable=SC2016 # Preserve Compose interpolation for the deploy pin.
sed -i 's|^    image: .*ghcr.io/grierj/fold:.*|    image: ghcr.io/grierj/fold:${FOLD_VERSION}|' "$SOURCE/docker-compose.yml"
# Docker allocates a subnet distinct from every existing stack. The source
# fixture records that subnet before the provisioner validates its proxy.
cat >"$DIR/.env" <<ENV
COMPOSE_PROJECT_NAME=$PROJECT
DOMAIN=groups.example.org
FOLD_VERSION=drill
DB_PASSWORD=drill-password
SESSION_SECRET=drill-session-secret-at-least-32-characters
ENV
chmod 600 "$DIR/.env"
cp "$SOURCE/docker-compose.yml" "$DIR/docker-compose.yml"
cp "$SOURCE/drill-nginx.conf" "$DIR/drill-nginx.conf"
# Bootstrap only auxiliary services, no application or database yet.
started=1
docker compose --project-directory "$DIR" up -d registry smtp
registry_port="$(docker compose --project-directory "$DIR" port registry 5000)"
for ((i=0; i<60; i++)); do
  if curl -fsS "http://$registry_port/v2/" >/dev/null; then break; fi
  sleep 1
done
REGISTRY="$registry_port"
if [[ -z "$BASE_IMAGE" ]]; then
  BASE_IMAGE="fold-deploy-drill-base:$PROJECT"
  docker build -t "$BASE_IMAGE" "$DRILL_ROOT"
else
  docker image inspect "$BASE_IMAGE" >/dev/null
fi
# Fixture images report levels without changing the real Fold server. A real
# level-1 candidate can be supplied as BASE_IMAGE by T07B-1-V as well.
cat >"$WORK/policy.go" <<'GO'
package main
import("fmt";"os";"syscall")
var level string
func main(){
 if len(os.Args)>1 && os.Args[1]=="version" {
  if level=="absent" {fmt.Println(`{"version":"drill"}`)} else {fmt.Printf("{\"version\":\"drill\",\"recoveryPolicy\":%s}\n",level)}
  return
 }
 if err:=syscall.Exec("/fold-real",os.Args,os.Environ());err!=nil {panic(err)}
}
GO
cat >"$WORK/Dockerfile" <<EOF_DOCKER
FROM $BASE_IMAGE AS base
FROM $BASE_IMAGE
COPY --from=base /fold /fold-real
COPY policy /fold
EOF_DOCKER
for level in absent 1; do
  CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.level=$level" -o "$WORK/policy" "$WORK/policy.go"
  docker build -t "$REGISTRY/fold:$level" "$WORK"
  docker push "$REGISTRY/fold:$level"
done
# Allocate and pin this disposable network's subnet before the provisioner
# validates it. The fixture then passes the same trusted-proxy check as prod.
network="$(docker network ls --filter "label=com.docker.compose.project=$PROJECT" --format '{{.ID}}')"
subnet="$(docker network inspect "$network" --format '{{(index .IPAM.Config 0).Subnet}}')"
# Record the allocated subnet in the existing network definition. The
# provisioner then validates the same trusted-proxy relation as production.
SUBNET="$subnet" awk '
  /^networks:/ {net=1}
  net && /^  default:/ {print; print "    ipam:\n      config:\n        - subnet: " ENVIRON["SUBNET"]; next}
  {print}' "$SOURCE/docker-compose.yml" >"$WORK/subnet-compose.yml"
mv "$WORK/subnet-compose.yml" "$SOURCE/docker-compose.yml"
# Production reads a proxy override from .env and Compose passes it to the
# app. Keep that interpolation through both YAML renders, rather than a
# literal the production check cannot read from the rendered Compose file.
printf 'FOLD_TRUSTED_PROXIES=%s\n' "$subnet" >>"$DIR/.env"
# Recreate only this disposable project's auxiliary network with the observed
# explicit subnet, retaining its registry volume. Pin its loopback registry
# port so the refs just pushed remain usable after the recreation.
docker compose -f "$SOURCE/docker-compose.yml" config --no-interpolate --format json \
  | jq --arg port "${REGISTRY##*:}" '.services.registry.ports[0].published = $port' >"$WORK/fixed-registry.json"
docker compose -f "$WORK/fixed-registry.json" config --no-interpolate >"$WORK/fixed-registry.yml"
# shellcheck disable=SC2016 # Preserve the pin expression through YAML rendering.
sed -i 's|^    image: .*ghcr.io/grierj/fold:.*|    image: ghcr.io/grierj/fold:${FOLD_VERSION}|' "$WORK/fixed-registry.yml"
docker compose --project-directory "$DIR" down
mv "$WORK/fixed-registry.yml" "$SOURCE/docker-compose.yml"
cp "$SOURCE/docker-compose.yml" "$DIR/docker-compose.yml"
docker compose --project-directory "$DIR" up -d registry smtp
for ((i=0; i<60; i++)); do
  if curl -fsS "http://$REGISTRY/v2/" >/dev/null; then break; fi
  sleep 1
done
# Build mode deliberately builds from the target commit, not from the tools.
cat >"$SOURCE/Dockerfile" <<EOF_DOCKER
FROM $REGISTRY/fold:1
EOF_DOCKER
printf 'services:\n  app:\n    build: .\n' >"$SOURCE/docker-compose.build.yml"
git -C "$SOURCE" init -q
git -C "$SOURCE" config user.name drill
git -C "$SOURCE" config user.email drill@example.invalid
git -C "$SOURCE" add .; git -C "$SOURCE" commit -qm 'legacy fixture with unsafe provisioner'
LOW_COMMIT="$(git -C "$SOURCE" rev-parse HEAD)"
printf '1\n' >"$SOURCE/deploy/recovery-policy"
git -C "$SOURCE" add .; git -C "$SOURCE" commit -qm 'same-level fixture'
HIGH_COMMIT="$(git -C "$SOURCE" rev-parse HEAD)"

# Auxiliary services are already running. Let the first provision install the
# application pin without interpreting the bootstrap placeholder as a release.
rm "$DIR/docker-compose.yml"
provision=("$TOOLS/provision.sh" --skip-harden --allow-os --domain groups.example.org --deploy-dir "$DIR" --source-dir "$SOURCE")
touch "$DIR/drill-smoke-fail"
rc=0
"${provision[@]}" --image "$REGISTRY/fold:1" >"$WORK/failed-smoke.log" 2>&1 || rc=$?
[[ "$rc" != 0 && ! -e "$DIR/recovery-policy-floor" ]]
rm "$DIR/drill-smoke-fail"
"${provision[@]}" --image "$REGISTRY/fold:1"
docker compose --project-directory "$DIR" exec -T db psql -X -U church -d fold -v ON_ERROR_STOP=1 \
  -c 'CREATE TABLE drill_sentinel(id integer PRIMARY KEY, value text); INSERT INTO drill_sentinel VALUES (1, '\''original'\'')'
# Seed the enforcing copy before refusal snapshots. Content and metadata of
# this set remain part of each later snapshot.
mkdir "$DIR/deploy-tools"
cp "$TOOLS/"* "$DIR/deploy-tools/"
chmod 700 "$DIR/deploy-tools"
"${provision[@]}" --allow-onbox-build
[[ "$(cat "$DIR/recovery-policy-floor")" == 1 ]]
"${provision[@]}" --image "$REGISTRY/fold:1"

upgrade_args=(--deploy-dir "$DIR" --source-dir "$SOURCE")
assert_one_deploy() {
  local trace="$1"
  [[ "$(grep -cF "+ $DIR/deploy-tools/provision.sh --skip-harden" "$trace")" == 1 ]] || { echo "expected one provision invocation in $trace" >&2; return 1; }
  [[ "$(grep -cE '^\+ docker compose .* up -d$' "$trace")" == 1 ]] || { echo "expected one compose up in $trace" >&2; return 1; }
}
refused_unchanged() {
  local want="$1" rc=0; shift
  snapshot_state "$DIR" "$SOURCE" >"$WORK/before"
  timeout 15 "$@" >"$WORK/refusal" 2>&1 || rc=$?
  cat "$WORK/refusal"
  [[ "$rc" != 0 ]] || { echo 'expected refusal' >&2; exit 1; }
  if [[ "$want" == 75 ]]; then [[ "$rc" == 75 ]]; else grep -qF "$want" "$WORK/refusal"; fi
  snapshot_state "$DIR" "$SOURCE" >"$WORK/after"
  diff -u "$WORK/before" "$WORK/after"
  [[ ! -e "$WORK/bypass-marker" ]]
}
for entry in "$DIR/upgrade.sh" "$TOOLS/upgrade.sh"; do
  refused_unchanged 'below host level 1' "$entry" "${upgrade_args[@]}" --commit "$LOW_COMMIT"
  refused_unchanged 'below host level 1' "$entry" "${upgrade_args[@]}" --image "$REGISTRY/fold:absent"
  env SHELLOPTS=xtrace bash -x "$entry" "${upgrade_args[@]}" --commit "$HIGH_COMMIT" >"$WORK/upgrade-commit.trace" 2>&1
  assert_one_deploy "$WORK/upgrade-commit.trace"
  [[ ! -e "$WORK/bypass-marker" ]]
  diff -r "$TOOLS" "$DIR/deploy-tools"
  env SHELLOPTS=xtrace bash -x "$entry" "${upgrade_args[@]}" --image "$REGISTRY/fold:1" >"$WORK/upgrade-image.trace" 2>&1
  assert_one_deploy "$WORK/upgrade-image.trace"
  diff -r "$TOOLS" "$DIR/deploy-tools"
  for file in "$TOOLS/"*; do cmp "$file" "$DIR/${file##*/}"; done
  # A recorded legacy rollback also refuses before touching the database.
  sed -i "s|^previous_image=.*|previous_image=$REGISTRY/fold:absent|; s/^mode=.*/mode=image/" "$DIR/release.prev"
  refused_unchanged 'below host level 1' "$entry" "${upgrade_args[@]}" --rollback
done

# A killed swap between the two renames leaves a saved complete tool set.
# The installed entry point recovers it and removes the partial stage.
mv "$DIR/deploy-tools" "$DIR/.old-deploy-tools.interrupted"
mkdir "$DIR/.deploy-tools.interrupted"
touch "$DIR/.deploy-tools.interrupted/partial"
"$DIR/upgrade.sh" "${upgrade_args[@]}" --image "$REGISTRY/fold:1" >"$WORK/swap-recovery.log" 2>&1
grep -q 'recovered enforcing tools' "$WORK/swap-recovery.log"
diff -r "$TOOLS" "$DIR/deploy-tools"
[[ -z "$(find "$DIR" -maxdepth 1 -name '.*deploy-tools.*' -print)" ]]

# FIFO barriers, not timing guesses, determine when the lock is held.
mkfifo "$DIR/drill.fifo"
lock_identity="$(stat -c '%d:%i:%a:%u' "$DIR/ops.lock")"
[[ "$lock_identity" == *:600:0 ]]
wait_ready() {
  local point="$1"
  # shellcheck disable=SC2016 # The child shell receives the expected point and holder.
  if ! timeout --kill-after=5 60 bash -c '
    while kill -0 "$3" 2>/dev/null; do
      ready="$(cat "$1/drill.ready" 2>/dev/null || true)"
      [[ "$ready" != "$2" ]] || exit 0
      [[ -z "$ready" ]] || { echo "unexpected drill barrier: $ready (wanted $2)" >&2; exit 1; }
      sleep 0.05
    done
    echo "drill holder exited before $2" >&2
    exit 1
  ' _ "$DIR" "$point" "$holder"; then
    echo "drill barrier $point was not reached within 60 seconds" >&2
    return 1
  fi
  cat "$DIR/drill.ready"
}
release_holder() {
  # Bound the open as well as the write if the reader has already exited.
  # shellcheck disable=SC2016 # The child shell receives DIR as $1.
  timeout --kill-after=5 15 bash -c 'printf "continue\n" >"$1/drill.fifo"' _ "$DIR" || {
    echo 'drill barrier release failed within 15 seconds' >&2; return 1;
  }
  # wait is a shell builtin; observe the child under a deadline, then reap it
  # here so its real exit status still decides whether the drill passed.
  # shellcheck disable=SC2016 # The child shell receives the holder pid as $1.
  timeout --kill-after=5 300 bash -c 'while kill -0 "$1" 2>/dev/null; do sleep 0.05; done' _ "$holder" || {
    echo 'drill holder did not finish within 300 seconds after release' >&2; return 1;
  }
  wait "$holder"
  holder=""
  rm -f "$DIR/drill.ready"
}
losers() {
  refused_unchanged 75 "${provision[@]}" --image "$REGISTRY/fold:1"
  refused_unchanged 75 "$DIR/upgrade.sh" "${upgrade_args[@]}" --image "$REGISTRY/fold:1"
  refused_unchanged 75 "$DIR/upgrade.sh" "${upgrade_args[@]}" --rollback
}
# Independent stand-in holder with an open-file-description lock.
(
  exec 8<>"$DIR/drill.fifo"
  exec 9>>"$DIR/ops.lock"
  flock -w 15 -x 9
  echo holder >"$DIR/drill.ready"
  read -r -t 180 token <&8
  [[ "$token" == continue ]]
) &
holder=$!
wait_ready holder
losers
release_holder
# Two actual provisioners: winner held after lock, loser exits immediately.
DEPLOY_DRILL_PAUSE=after-lock "${provision[@]}" --image "$REGISTRY/fold:1" >"$WORK/winner.log" 2>&1 &
holder=$!
wait_ready after-lock
losers
release_holder
# Nested provisioner inherits fd 9; an outside provisioner cannot acquire it.
DEPLOY_DRILL_PAUSE=before-up env SHELLOPTS=xtrace "$DIR/upgrade.sh" "${upgrade_args[@]}" --image "$REGISTRY/fold:1" >"$WORK/nested.log" 2>&1 &
holder=$!
wait_ready before-up
losers
release_holder
assert_one_deploy "$WORK/nested.log"
# SIGKILL releases the kernel lock. Block Bash's builtin read on a FIFO opened
# read/write so there is no child process left owning the descriptor.
(
  exec 8<>"$DIR/drill.fifo"
  exec 9>>"$DIR/ops.lock"
  flock -w 15 -x 9
  echo killed-holder >"$DIR/drill.ready"
  read -r -t 180 _ <&8
) &
holder=$!
wait_ready killed-holder
kill -KILL "$holder"
wait "$holder" 2>/dev/null || true
holder=""
rm -f "$DIR/drill.ready"

# Inheritance deliberately fails safe if a helper survives its parent. Prove
# exit 75 persists, carries recovery instructions, and ends when that exact
# helper is stopped without ever deleting the lock file.
(
  exec 8<>"$DIR/drill.fifo"
  exec 9>>"$DIR/ops.lock"
  flock -w 15 -x 9
  # shellcheck disable=SC2016 # Child writes its own pid and uses inherited fds.
  bash -c 'echo "$BASHPID" >"$1/drill.descendant"; echo descendant-holder >"$1/drill.ready"; IFS= read -r -t 180 _ <&8' _ "$DIR" &
  IFS= read -r -t 180 _ <&8
) &
holder=$!
wait_ready descendant-holder
descendant="$(cat "$DIR/drill.descendant")"
kill -KILL "$holder"
wait "$holder" 2>/dev/null || true
holder=""
refused_unchanged 75 "${provision[@]}" --image "$REGISTRY/fold:1"
grep -q 'fuser -v' "$WORK/refusal"
kill -KILL "$descendant"
descendant=""
timeout 15 flock -x "$DIR/ops.lock" true
rm -f "$DIR/drill.ready" "$DIR/drill.descendant"
"${provision[@]}" --image "$REGISTRY/fold:1"
[[ "$(cat "$DIR/recovery-policy-floor")" == 1 ]]
[[ "$(stat -c '%d:%i:%a:%u' "$DIR/ops.lock")" == "$lock_identity" ]]
snapshot_state "$DIR" "$SOURCE" >"$WORK/final-state"
printf 'deploy drill passed: project=%s dir=%s source=%s\n' "$PROJECT" "$DIR" "$SOURCE"
