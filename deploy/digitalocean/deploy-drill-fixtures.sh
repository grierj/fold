#!/usr/bin/env bash
# Daemon-free regressions for the real drill's Compose/TLS bootstrap.
# The real Docker/Postgres acceptance drill remains a separate required check.
# shellcheck disable=SC2016 # jq programs and mock-script variables are literal.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck source=deploy/digitalocean/deploy-drill.sh
source "$ROOT/deploy/digitalocean/deploy-drill.sh"
WORK="$(mktemp -d /tmp/fold-drill-config.XXXXXX)"
# shellcheck disable=SC2329 # Invoked by EXIT.
cleanup() { rm -rf -- "$WORK"; }
trap cleanup EXIT
trap 'printf "drill bootstrap fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
mkdir "$WORK/site" "$WORK/bin"
PROJECT="fold-deploy-drill-config-fixture"
# A normalized Compose input includes the old, unusable sink host mapping.
# Test that both real generation stages remove/avoid it, while the three
# host-facing auxiliaries keep their loopback ports across recreation.
cat >"$WORK/input.json" <<'JSON'
{"services":{"app":{"healthcheck":{"test":["CMD","/fold","healthcheck"],"interval":"1s","start_interval":"1s","timeout":"3s","retries":5,"start_period":"15s"},"networks":{"default":{}},"volumes":[{"type":"volume","source":"uploads","target":"/data"}]},"db":{},"registry":{},"smtp":{},"caddy":{"ports":[{"target":80},{"target":443}]},"push-sink":{"ports":[{"target":443,"host_ip":"127.0.0.1"}],"networks":{"push":{"aliases":["push.restore-drill.invalid"]}}}},"networks":{"default":{"ipam":{"config":[{"subnet":"172.30.0.0/24"}]}},"push":{"internal":true}},"volumes":{"uploads":{},"pgdata":{},"registrydata":{}}}
JSON
drill_compose_config "$WORK/site" "$PROJECT" 11.254.17.0/24 "$WORK/input.json" >"$WORK/initial.json"
assert_drill_compose_isolation "$WORK/initial.json"
jq -e --arg dir "$WORK/site" --arg project "$PROJECT" '
  (.services["push-sink"] | has("ports") | not)
  and .networks.push.ipam.config==[{subnet:"11.254.17.0/24"}]
  and .networks.push.name==($project+"_push")
  and .services["push-sink"].networks.push.aliases==["push.restore-drill.invalid"]
  and .services.app.environment.FOLD_SMTP_URL=="smtp://smtp:1025"
  and .services.app.environment.FOLD_SMTP_ALLOW_INSECURE=="true"
  and any(.services.app.volumes[]; .source==($dir+"/drill-push-tls/ca.crt") and .read_only==true)
' "$WORK/initial.json" >/dev/null
drill_pin_aux_ports 127.0.0.1:31001 127.0.0.1:31002 127.0.0.1:31003 <"$WORK/initial.json" >"$WORK/pinned.json"
assert_drill_compose_isolation "$WORK/pinned.json"
jq -e '
  .services.registry.ports[0].published=="31001"
  and .services.caddy.ports[0].published=="31002"
  and .services.smtp.ports[0].published=="31003"
  and (.services["push-sink"] | has("ports") | not)
' "$WORK/pinned.json" >/dev/null
for address in 0.0.0.0:31001 192.0.2.1:31001 127.0.0.1:0 127.0.0.1:65536; do
  if drill_pin_aux_ports "$address" 127.0.0.1:31002 127.0.0.1:31003 <"$WORK/initial.json" >"$WORK/out" 2>&1; then exit 1; fi
done
for mutation in '.networks.push.internal=false' '.services["push-sink"].networks.default={}' '.services["push-sink"].ports=[{target:443,host_ip:"127.0.0.1"}]' '.services.app.ports=[{target:8080,host_ip:"127.0.0.1"}]' '.services.db.ports=[{target:5432,host_ip:"0.0.0.0"}]' '.services.registry.ports[0].host_ip="0.0.0.0"' '.services.app.volumes[-1].read_only=false' 'del(.services.app.networks.default)'; do
  jq "$mutation" "$WORK/initial.json" >"$WORK/bad.json"
  if assert_drill_compose_isolation "$WORK/bad.json" >"$WORK/out" 2>&1; then exit 1; fi
done
# Faster scheduling must retain the real probe and every timeout/retry/grace
# setting; a disabled or substituted healthcheck cannot make setup pass.
for mutation in '.services.app.healthcheck.test=["CMD","true"]' '.services.app.healthcheck.disable=true' \
  '.services.app.healthcheck.timeout="1s"' '.services.app.healthcheck.retries=1' \
  '.services.app.healthcheck.start_period="0s"' '.services.app.healthcheck.interval="10s"' \
  '.services.app.healthcheck.start_interval="5s"'; do
  jq "$mutation" "$WORK/initial.json" >"$WORK/bad-health.json"
  if assert_drill_compose_isolation "$WORK/bad-health.json" >"$WORK/out" 2>&1; then exit 1; fi
done
app_block="$(awk '/^  app:/ {p=1;next} p && /^  [^ ]/ {exit} p {print}' "$ROOT/deploy/compose.deploy-drill.yml")"
grep -q '^      interval: 1s$' <<<"$app_block"
grep -q '^      start_interval: 1s$' <<<"$app_block"
if grep -qE '^      (test|disable|timeout|retries|start_period):' <<<"$app_block"; then
  echo 'drill overlay changes more than the healthcheck schedule' >&2; exit 1
fi
# Guard the source overlay too, so standalone Compose cannot publish the sink
# even if it bypasses the generator. The generated config is checked above.
push_block="$(awk '/^  push-sink:/ {p=1;next} p && /^  [^ ]/ {exit} p {print}' "$ROOT/deploy/compose.deploy-drill.yml")"
if grep -q '^    ports:' <<<"$push_block"; then echo 'push overlay publishes a host port' >&2; exit 1; fi
grep -q '^    networks:' <<<"$push_block"
grep -q '^      push:' <<<"$push_block"
if grep -q '^      default:' <<<"$push_block"; then exit 1; fi
if grep -qE 'PUSH_PORT|port push-sink' "$ROOT/deploy/digitalocean/deploy-drill.sh"; then echo 'drill requests an internal sink host port' >&2; exit 1; fi
# Runtime facts must agree with the configuration contract. The Docker double
# deliberately refuses `compose port push-sink`, like the isolated real run.
cat >"$WORK/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$DRILL_CONFIG_FIXTURE/calls"
case "$*" in
  "network inspect ${DRILL_CONFIG_PROJECT}_push") cat "$DRILL_CONFIG_FIXTURE/network.json" ;;
  'inspect push-id') cat "$DRILL_CONFIG_FIXTURE/container.json" ;;
  *' port push-sink '*) echo 'no port 443/tcp for internal container' >&2; exit 1 ;;
  *' ps -q push-sink') echo push-id ;;
  *' exec -T push-sink nginx -t') echo 'fixture nginx config is valid' ;;
  *) echo "unexpected fixture docker call: $*" >&2; exit 99 ;;
esac
DOCKER
chmod 755 "$WORK/bin/docker"
export PATH="$WORK/bin:$PATH" DRILL_CONFIG_FIXTURE="$WORK" DRILL_CONFIG_PROJECT="$PROJECT"
printf '[{"Internal":true}]\n' >"$WORK/network.json"
jq -n --arg net "${PROJECT}_push" '[{HostConfig:{PortBindings:{}},NetworkSettings:{Networks:{($net):{}}}}]' >"$WORK/container.json"
assert_drill_push_runtime "$WORK/site" "$PROJECT" >"$WORK/out" 2>&1
if grep -q ' port push-sink ' "$WORK/calls"; then exit 1; fi
for mutation in '.[0].HostConfig.PortBindings={"443/tcp":[{HostIp:"127.0.0.1",HostPort:"31004"}]}' '.[0].NetworkSettings.Networks.default={}'; do
  jq "$mutation" "$WORK/container.json" >"$WORK/container.bad"
  cp "$WORK/container.json" "$WORK/container.good"; mv "$WORK/container.bad" "$WORK/container.json"
  if assert_drill_push_runtime "$WORK/site" "$PROJECT" >"$WORK/out" 2>&1; then exit 1; fi
  mv "$WORK/container.good" "$WORK/container.json"
done
printf '[{"Internal":false}]\n' >"$WORK/network.json"
if assert_drill_push_runtime "$WORK/site" "$PROJECT" >"$WORK/out" 2>&1; then exit 1; fi
# Generate the same strict TLS assets used by the real bootstrap. No network
# listener or external connection is involved in these certificate checks.
drill_push_tls_assets "$WORK/site" "$WORK"
openssl verify -CAfile "$WORK/site/drill-push-tls/ca.crt" -verify_hostname push.restore-drill.invalid "$WORK/site/drill-push-tls/sink.crt" >"$WORK/tls-proof"
if openssl verify -CAfile "$WORK/site/drill-push-tls/ca.crt" -verify_hostname other.invalid "$WORK/site/drill-push-tls/sink.crt" >"$WORK/out" 2>&1; then exit 1; fi
[[ "$(stat -c %a "$WORK/site/drill-push-tls/ca.key")" == 600 && "$(stat -c %a "$WORK/site/drill-push-tls/sink.key")" == 600 ]]
[[ "$(stat -c %a "$WORK/site/drill-push-tls/ca.crt")" == 644 ]]
grep -q '^ listen 443 ssl;' "$WORK/site/drill-push.conf"
# Missing linked metadata must fail before any stack creation, not become an
# empty revision through command substitution inside a successful jq call.
mkdir "$WORK/broken-checkout"
printf 'gitdir: %s/missing-metadata/worktrees/lane\n' "$WORK" >"$WORK/broken-checkout/.git"
if drill_source_revision "$WORK/broken-checkout" >"$WORK/out" 2>"$WORK/git-error"; then exit 1; fi
[[ ! -s "$WORK/out" ]]
grep -q 'mount the whole common Git directory read-only' "$WORK/git-error"
revision="$(drill_source_revision "$ROOT")"
[[ "$revision" =~ ^[0-9a-f]{40}$ ]]
legacy="$(drill_legacy_revision "$ROOT")"
[[ "$legacy" == "$(git -C "$ROOT" rev-parse 'ea589f0^')" ]]
echo 'deploy drill bootstrap fixtures: ok'
