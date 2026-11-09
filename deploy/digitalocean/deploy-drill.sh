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
  compose_in "$dir" images -q | sort -u
  compose_in "$dir" exec -T db psql -X -U church -d fold -At -v ON_ERROR_STOP=1 \
    -c 'SELECT datname,oid,datallowconn FROM pg_database ORDER BY datname' \
    -c 'SELECT * FROM drill_sentinel ORDER BY id' \
    -c 'SELECT id,version_id,is_applied FROM goose_db_version ORDER BY id'
  git -C "$source" rev-parse HEAD
  git -C "$source" status --porcelain
  if [[ -d "$dir/dumps" ]]; then
    find "$dir/dumps" -type f -printf '%P\n' | sort
  fi
}

# The sink serves peers on its internal bridge and has no host-facing API.
# Never query or pin a host port for it; only the proxy, mail inspector and
# registry need loopback publication.
drill_compose_config() {
  jq --arg dir "$1" --arg project "$2" --arg subnet "$3" '.services.caddy.volumes = [{type:"bind",source:($dir + "/drill-nginx.conf"),target:"/etc/nginx/conf.d/default.conf",read_only:true}]
  | .services.caddy.ports = [{target:80,host_ip:"127.0.0.1",protocol:"tcp"}]
  | .services.registry.ports = [{target:5000,host_ip:"127.0.0.1",protocol:"tcp"}]
  | .services.smtp.ports = [{target:8025,host_ip:"127.0.0.1",protocol:"tcp"}]
  | .services.app.environment.DATABASE_URL = "postgres://church:${DB_PASSWORD}@db:5432/fold?sslmode=disable"
  | .services.app.environment.SESSION_SECRET = "${SESSION_SECRET}"
  | .services.app.environment.FOLD_TRUSTED_PROXIES = "${FOLD_TRUSTED_PROXIES:-}"
  | .services.db.environment.POSTGRES_PASSWORD = "${DB_PASSWORD}"
  | .services["push-sink"].volumes = [{type:"bind",source:($dir + "/drill-push.conf"),target:"/etc/nginx/conf.d/default.conf",read_only:true},{type:"bind",source:($dir + "/drill-push-tls"),target:"/etc/nginx/certs",read_only:true}]
  | del(.services["push-sink"].ports)
  | .networks.push.name = ($project + "_push") | .networks.push.ipam.config = [{subnet:$subnet}]
  | .services.app.networks.default = {}
  | .services.app.networks.push = {}
  | .services.app.environment.FOLD_PUSH_HOSTS = "push.restore-drill.invalid"
  | .services.app.environment.SSL_CERT_FILE = "/etc/fold-drill/ca.crt"
  | .services.app.volumes += [{type:"bind",source:($dir + "/drill-push-tls/ca.crt"),target:"/etc/fold-drill/ca.crt",read_only:true}]
  | .services.app.environment.FOLD_SETUP_TOKEN = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  | .services.app.environment.FOLD_SMTP_URL = "smtp://smtp:1025"
  | .services.app.environment.FOLD_SMTP_ALLOW_INSECURE = "true"
  | .services.app.environment.FOLD_EMAIL_FROM = "Fold <fold@example.invalid>"
  | .services.app.image = "ghcr.io/grierj/fold:${FOLD_VERSION}"
  | del(.networks.default.ipam) | .networks.default.name = ($project + "_default")
  | .name = $project
  | .volumes |= with_entries(.value = {name:($project + "_" + .key)})' "$4"
}
assert_drill_compose_isolation() {
  jq -e '
    . as $config |
    (.services.app.ports // [] | length)==0
    and .services.app.healthcheck.test==["CMD","/fold","healthcheck"]
    and (.services.app.healthcheck.disable // false)==false
    and .services.app.healthcheck.interval=="1s"
    and .services.app.healthcheck.start_interval=="1s"
    and .services.app.healthcheck.timeout=="3s"
    and .services.app.healthcheck.retries==5
    and .services.app.healthcheck.start_period=="15s"
    and (.services.db.ports // [] | length)==0
    and (.services["push-sink"].ports // [] | length)==0
    and .networks.push.internal==true
    and (.services["push-sink"].networks | keys)==["push"]
    and (.services.app.networks | keys)==["default","push"]
    and .services.app.environment.FOLD_PUSH_HOSTS=="push.restore-drill.invalid"
    and .services.app.environment.SSL_CERT_FILE=="/etc/fold-drill/ca.crt"
    and any(.services.app.volumes[]; .target=="/etc/fold-drill/ca.crt" and .read_only==true)
    and all(["registry","smtp","caddy"][]; . as $svc |
      ($config.services[$svc].ports | length)==1
      and all($config.services[$svc].ports[]; .host_ip=="127.0.0.1" and .protocol=="tcp"))
  ' "$1" >/dev/null || {
    echo 'drill Compose isolation check failed; no service may start with this config' >&2; return 1;
  }
}
drill_pin_aux_ports() {
  local registry="$1" caddy="$2" smtp="$3" address
  for address in "$registry" "$caddy" "$smtp"; do
    [[ "$address" =~ ^127\.0\.0\.1:([1-9][0-9]{0,4})$ && "${BASH_REMATCH[1]}" -le 65535 ]] || {
      echo "invalid drill loopback port: $address" >&2; return 1;
    }
  done
  jq --arg registry "${registry##*:}" --arg caddy "${caddy##*:}" --arg smtp "${smtp##*:}" '
    .services.registry.ports[0].published=$registry
    | .services.caddy.ports[0].published=$caddy
    | .services.smtp.ports[0].published=$smtp'
}
assert_drill_push_runtime() {
  local dir="$1" project="$2" id
  id="$(compose_in "$dir" ps -q push-sink)" || return 1
  [[ -n "$id" && "$id" != *$'\n'* ]] || return 1
  docker network inspect "${project}_push" | jq -e 'length==1 and .[0].Internal==true' >/dev/null || {
    echo 'push sink bridge is not internal; stopping the drill' >&2; return 1;
  }
  docker inspect "$id" | jq -e --arg network "${project}_push" '
    length==1 and (.[0].HostConfig.PortBindings // {} | length)==0
    and (.[0].NetworkSettings.Networks | keys)==[$network]' >/dev/null || {
    echo 'push sink has host bindings or another network; stopping the drill' >&2; return 1;
  }
  # Validate that nginx can read the TLS key/config, with no host publication
  # or certificate-verification bypass. The real app proves delivery later.
  compose_in "$dir" exec -T push-sink nginx -t
}
drill_push_tls_assets() {
  local dir="$1" work="$2"
  mkdir -m 700 "$dir/drill-push-tls"
  openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 2 \
    -subj /CN=FoldRestoreDrillCA -addext basicConstraints=critical,CA:TRUE \
    -addext keyUsage=critical,keyCertSign,cRLSign -keyout "$dir/drill-push-tls/ca.key" -out "$dir/drill-push-tls/ca.crt" 2>/dev/null
  openssl req -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj /CN=push.restore-drill.invalid \
    -keyout "$dir/drill-push-tls/sink.key" -out "$work/push.csr" 2>/dev/null
  printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:push.restore-drill.invalid\n' >"$work/push.ext"
  openssl x509 -req -in "$work/push.csr" -CA "$dir/drill-push-tls/ca.crt" -CAkey "$dir/drill-push-tls/ca.key" \
    -CAcreateserial -days 2 -extfile "$work/push.ext" -out "$dir/drill-push-tls/sink.crt" 2>/dev/null
  # The app runs as nonroot; only the public certificate is readable by it.
  chmod 644 "$dir/drill-push-tls/ca.crt" "$dir/drill-push-tls/sink.crt"
  chmod 600 "$dir/drill-push-tls/ca.key" "$dir/drill-push-tls/sink.key"
  cat >"$dir/drill-push.conf" <<'PUSH'
server {
 listen 443 ssl;
 ssl_certificate /etc/nginx/certs/sink.crt;
 ssl_certificate_key /etc/nginx/certs/sink.key;
 location = /healthz { access_log off; return 200 'ok'; }
 location / { return 201; }
}
PUSH
}

# A linked checkout needs its common Git directory inside the runner too.
# Resolve this before creating a stack, and never mask a git failure inside jq.
drill_source_revision() {
  local source="$1" revision link=""
  if [[ -e "$source/.git" ]] && revision="$(env -u GIT_DIR -u GIT_COMMON_DIR -u GIT_WORK_TREE git -C "$source" rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" \
    && [[ "$revision" =~ ^[0-9a-f]{40}$ ]]; then
    printf '%s\n' "$revision"; return 0
  fi
  [[ ! -f "$source/.git" ]] || link="$(cat "$source/.git")"
  printf 'drill: Git metadata unavailable for %s (%s). For a linked worktree, mount the whole common Git directory read-only at its original absolute path in the runner, or use a complete checkout. Restore archive proof needs those objects; no stack started.\n' "$source" "$link" >&2
  return 1
}
drill_legacy_revision() {
  local source="$1" revision parent
  revision="$(git -C "$source" rev-parse --verify 'a278962^{commit}' 2>/dev/null)" \
    && parent="$(git -C "$source" rev-parse --verify 'ea589f0^' 2>/dev/null)" \
    && [[ "$revision" == "$parent" ]] || {
      echo 'drill: required pre-scrub archive history (a278962, parent of ea589f0) unavailable; provide the complete read-only Git metadata before starting the drill' >&2; return 1;
    }
  printf '%s\n' "$revision"
}

# The helpers above call checks.sh functions (env_value, compose_in), so a
# sourcing caller gets them too.
# shellcheck source=deploy/digitalocean/checks.sh
source "$DRILL_ROOT/deploy/digitalocean/checks.sh"
# Sourcing this file is an explicit library interface, not a second runner.
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
  return 0
fi

KEEP=0
PREPARE_ONLY=0
BASE_IMAGE=""
WORK=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) KEEP=1; shift ;;
    --prepare-only) PREPARE_ONLY=1; KEEP=1; shift ;;
    --image) BASE_IMAGE="${2:?--image needs a reference}"; shift 2 ;;
    --dir) WORK="${2:?--dir needs an empty scratch directory}"; shift 2 ;;
    --help) echo 'Usage: sudo deploy-drill.sh [--image BASE_IMAGE] [--dir EMPTY_DIR] [--keep]'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 1 ;;
  esac
done
[[ "$(id -u)" == 0 ]] || { echo 'deploy-drill.sh requires root for the provisioner' >&2; exit 1; }
for command in docker go git jq curl flock timeout openssl ip; do command -v "$command" >/dev/null; done
unset GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE
REVISION="$(drill_source_revision "$DRILL_ROOT")" || exit 1
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
    compose_in "$DIR" -f "$SOURCE/docker-compose.yml" down -v --remove-orphans || rc=1
  fi
  echo "drill result=$rc artifacts=$WORK retained_stack=$KEEP"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

cp "$DRILL_ROOT"/deploy/digitalocean/{provision.sh,upgrade.sh,checks.sh,backup.sh,smoke.sh,email-dns.sh,backup.env.example,restore.sh,compose.recovery.yml,deploy-tools-version} "$TOOLS/"
# A local HTTP proxy replaces public TLS and host SSH/firewall checks. This is
# a test tool-set file; the enforcing upgrade/provision scripts stay unchanged.
cat >"$TOOLS/smoke.sh" <<'SMOKE'
#!/usr/bin/env bash
set -euo pipefail
while [[ $# -gt 0 ]]; do
  if [[ "$1" == --deploy-dir ]]; then dir="$2"; shift 2; else shift; fi
done
case "${RESTORE_DRILL_FAULT:-}" in
  mid-smoke|mid-abort-smoke)
    [[ -p "$dir/drill.fifo" ]] || exit 1
    exec 8<>"$dir/drill.fifo"
    printf '%s|%s\n' "$RESTORE_DRILL_FAULT" "$$" >"$dir/drill.ready"
    IFS= read -r -t 180 token <&8 && [[ "$token" == continue ]] || exit 1 ;;
esac
[[ ! -e "$dir/drill-smoke-fail" ]] || exit 1
port="$(compose_in "$dir" port caddy 80)"
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
# The push sink stays on an internal bridge. Refuse a subnet already covered
# by a non-default host route or another Docker network, rather than changing
# the application's SSRF policy for a test.
PUSH_SUBNET=""
for ((attempt=0;attempt<200;attempt++)); do
  octet="$(od -An -N1 -tu1 /dev/urandom | tr -d ' ')"
  candidate="11.254.$octet.0/24"
  if ip -4 route show | awk -v wanted="11.254.$octet.2" '
    function addr(s, a){split(s,a,"."); return a[1]*16777216+a[2]*65536+a[3]*256+a[4]}
    $1 ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(\/[0-9]+)?$/ {
      split($1,p,"/"); bits=(p[2]==""?32:p[2]); scale=2^(32-bits)
      if(int(addr(wanted)/scale)==int(addr(p[1])/scale)) found=1
    } END {exit !found}'; then continue; fi
  # Docker itself checks subnet overlap across all networks on create. The
  # temporary probe is removed before Compose creates the project network.
  if probe="$(docker network create --internal --subnet "$candidate" "$PROJECT-push-probe" 2>/dev/null)"; then
    docker network rm "$probe" >/dev/null; PUSH_SUBNET="$candidate"; break
  fi
done
[[ -n "$PUSH_SUBNET" ]] || { echo 'cannot allocate the isolated push network' >&2; exit 1; }
drill_push_tls_assets "$DIR" "$WORK"
# Merge the isolated proxy, SMTP and registry into the target tree. Compose
# reads only this file for both pull and build deployments, so pin changes do
# not drop the isolation overrides.
DB_PASSWORD=drill SESSION_SECRET=drill-session-secret-at-least-32-characters DOMAIN=groups.example.org FOLD_VERSION=drill \
  docker compose -f "$DRILL_ROOT/docker-compose.yml" -f "$DRILL_ROOT/deploy/compose.deploy-drill.yml" \
  config --format json >"$WORK/config.json"
drill_compose_config "$DIR" "$PROJECT" "$PUSH_SUBNET" "$WORK/config.json" >"$WORK/compose.json"
assert_drill_compose_isolation "$WORK/compose.json"
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
compose_in "$DIR" up -d --no-deps registry smtp push-sink caddy
registry_port="$(compose_in "$DIR" port registry 5000)"
for ((i=0; i<60; i++)); do
  if curl -fsS --max-time 2 "http://$registry_port/v2/" >/dev/null; then break; fi
  sleep 1
done
curl -fsS --max-time 2 "http://$registry_port/v2/" >/dev/null || { echo 'drill registry is not ready' >&2; exit 1; }
REGISTRY="$registry_port"
CADDY_PORT="$(compose_in "$DIR" port caddy 80)"
SMTP_PORT="$(compose_in "$DIR" port smtp 8025)"
assert_drill_push_runtime "$DIR" "$PROJECT"
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
network="$(docker network ls --filter "label=com.docker.compose.project=$PROJECT" --filter "label=com.docker.compose.network=default" --format '{{.ID}}')"
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
  | drill_pin_aux_ports "$REGISTRY" "$CADDY_PORT" "$SMTP_PORT" >"$WORK/fixed-registry.json"
assert_drill_compose_isolation "$WORK/fixed-registry.json"
docker compose -f "$WORK/fixed-registry.json" config --no-interpolate >"$WORK/fixed-registry.yml"
# shellcheck disable=SC2016 # Preserve the pin expression through YAML rendering.
sed -i 's|^    image: .*ghcr.io/grierj/fold:.*|    image: ghcr.io/grierj/fold:${FOLD_VERSION}|' "$WORK/fixed-registry.yml"
compose_in "$DIR" down
mv "$WORK/fixed-registry.yml" "$SOURCE/docker-compose.yml"
cp "$SOURCE/docker-compose.yml" "$DIR/docker-compose.yml"
compose_in "$DIR" up -d --no-deps registry smtp push-sink caddy
assert_drill_push_runtime "$DIR" "$PROJECT"
for ((i=0; i<60; i++)); do
  if curl -fsS --max-time 2 "http://$REGISTRY/v2/" >/dev/null; then break; fi
  sleep 1
done
curl -fsS --max-time 2 "http://$REGISTRY/v2/" >/dev/null || { echo 'recreated drill registry is not ready' >&2; exit 1; }
# Build mode deliberately builds from the target commit, not from the tools.
cat >"$SOURCE/Dockerfile" <<EOF_DOCKER
FROM $REGISTRY/fold:1
EOF_DOCKER
printf 'services:\n  app:\n    build: .\n' >"$SOURCE/docker-compose.build.yml"
git -C "$SOURCE" init -q
git -C "$SOURCE" config user.name drill
git -C "$SOURCE" config user.email drill@example.invalid
git -C "$SOURCE" config core.hooksPath /dev/null
git -C "$SOURCE" config commit.gpgsign false
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
compose_in "$DIR" exec -T db psql -X -U church -d fold -v ON_ERROR_STOP=1 \
  -c 'CREATE TABLE drill_sentinel(id integer PRIMARY KEY, value text); INSERT INTO drill_sentinel VALUES (1, '\''original'\'')'
# Seed the enforcing copy before refusal snapshots. Content and metadata of
# this set remain part of each later snapshot.
mkdir "$DIR/deploy-tools"
cp "$TOOLS/"* "$DIR/deploy-tools/"
chmod 700 "$DIR/deploy-tools"
"${provision[@]}" --allow-onbox-build
[[ "$(cat "$DIR/recovery-policy-floor")" == 1 ]]
"${provision[@]}" --image "$REGISTRY/fold:1"

if (( PREPARE_ONLY )); then
  jq -n --arg dir "$DIR" --arg source "$SOURCE" --arg project "$PROJECT" --arg registry "$REGISTRY" \
    --arg lowCommit "$LOW_COMMIT" --arg highCommit "$HIGH_COMMIT" --arg image "$REGISTRY/fold:1" --arg revision "$REVISION" \
    '{dir:$dir,source:$source,project:$project,registry:$registry,image:$image,revision:$revision,lowCommit:$lowCommit,highCommit:$highCommit}' >"$WORK/restore-drill.json"
  echo "restore drill stack prepared: $WORK/restore-drill.json"
  exit 0
fi

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
