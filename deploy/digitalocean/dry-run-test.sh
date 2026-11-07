#!/usr/bin/env bash
# Run the provisioner --dry-run inside a container and prove it changes nothing.
# Each fixture subshell intentionally has its own PATH and DEPLOY_DRILL.
# shellcheck disable=SC2030,SC2031
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
container_image="${FOLD_DRY_RUN_IMAGE:-ubuntu:24.04}"

# Run only inside the disposable dry-run-test container, after its packages
# are installed. Every production script runs unchanged except the explicit
# smoke/backup stand-ins in the fixture tool set.
recovery_fixtures() (
  set -Eeuo pipefail
  # Keep failure evidence visible even when a traced function redirects stderr
  # into the scratch directory that EXIT will remove.
  exec 7>&2
  local_root="$(mktemp -d /tmp/fold-policy.XXXXXX)"
  fixture_case='setup'
  # shellcheck disable=SC2329 # Invoked by the EXIT trap.
  fixture_cleanup() {
    local rc="$?" file
    if (( rc != 0 )); then
      {
        printf 'recovery policy fixtures: FAILED (%s), exit %s\n' "$fixture_case" "$rc"
        for file in out trace calls; do
          if [[ -s "$local_root/$file" ]]; then
            printf '%s\n' "last captured $file (up to 60 lines):"
            tail -n 60 "$local_root/$file"
          fi
        done
      } >&7
    fi
    rm -rf "$local_root"
    exit "$rc"
  }
  trap fixture_cleanup EXIT
  trap 'printf "recovery policy fixtures: line %s: %s (exit %s)\n" "$LINENO" "$BASH_COMMAND" "$?" >&7' ERR
  export FIXTURE_LOG="$local_root/calls"
  : >"$FIXTURE_LOG"
  mkdir -p "$local_root/bin" "$local_root/src/deploy" "$local_root/tools" "$local_root/dir"
  cp "$root"/deploy/digitalocean/{provision.sh,upgrade.sh,checks.sh,backup.sh,smoke.sh,email-dns.sh,backup.env.example,restore.sh,compose.recovery.yml,deploy-tools-version} "$local_root/tools/"
  cp "$root"/{docker-compose.yml,docker-compose.build.yml,Caddyfile,.env.example} "$local_root/src/"
  cat >"$local_root/tools/smoke.sh" <<'STUB'
#!/bin/bash
printf 'smoke\n' >>"$FIXTURE_LOG"
[[ "${FIXTURE_SMOKE_FAIL:-0}" == 0 ]] || echo 'fixture: smoke failed' >&2
exit "${FIXTURE_SMOKE_FAIL:-0}"
STUB
  cat >"$local_root/tools/backup.sh" <<'STUB'
#!/bin/bash
printf 'backup\n' >>"$FIXTURE_LOG"
while [[ $# -gt 0 ]]; do
  if [[ "$1" == --backup-dir ]]; then dir="$2"; shift 2; else shift; fi
 done
mkdir -p "$dir"
printf 'dump\n' >"$dir/fold-20260101-0000.dump"
STUB
  cat >"$local_root/bin/docker" <<'STUB'
#!/bin/bash
set -euo pipefail
printf 'docker %s\n' "$*" >>"$FIXTURE_LOG"
case "$1" in
  pull) exit 0 ;;
  image)
    [[ "${FIXTURE_INSPECT_FAIL:-0}" == 0 ]] || exit 1
    if [[ -n "${FIXTURE_RESULT_IMAGE:-}" && -e "$FIXTURE_LOG.ready" ]]; then
      echo "$FIXTURE_RESULT_IMAGE"
    else
      echo "${@: -1}"
    fi
    exit 0 ;;
  run)
    [[ "$*" == *'--network none --pull never'* ]] || { echo 'unsafe image version read' >&2; exit 1; }
    case "$*" in
      *fixture:absent*) echo '{"version":"old"}' ;;
      *fixture:bad*) echo '{"recoveryPolicy":"1"}' ;;
      *fixture:null*) echo '{"recoveryPolicy":null}' ;;
      *fixture:fraction*) echo '{"recoveryPolicy":1.5}' ;;
      *fixture:zero*) echo '{"recoveryPolicy":0}' ;;
      *fixture:two*) echo '{"recoveryPolicy":2}' ;;
      *) echo '{"recoveryPolicy":1}' ;;
    esac
    exit 0 ;;
  compose)
    shift
    # install_docker probes Compose before any project files are installed.
    if [[ "$#" == 1 && "$1" == version ]]; then
      exit 0
    fi
    [[ "$#" -ge 3 && "$1" == --project-directory ]] || {
      echo "unexpected compose $*" >&2; exit 1;
    }
    dir="$2"; shift 2
    case "$1" in
      config)
        [[ "${FIXTURE_CONFIG_FAIL:-0}" == 0 ]] || exit 1
        if [[ "${FIXTURE_CONFIG_BAD:-0}" == 1 ]]; then echo '{}'; exit 0; fi
        pin="$(sed -n 's/.*image: \(.*\) # fold-app-image$/\1/p' "$dir/docker-compose.yml")"
        [[ -n "$pin" ]] || pin="fixture:$(sed -n 's/^FOLD_VERSION=//p' "$dir/.env")"
        printf '{"services":{"app":{"image":"%s"}}}\n' "$pin"
        ;;
      pull|build) touch "$FIXTURE_LOG.ready" ;;
      version|up) : ;;
      *) echo "unexpected compose $*" >&2; exit 1 ;;
    esac ;;
  *) exit 1 ;;
esac
STUB
  cat >"$local_root/bin/sync" <<'STUB'
#!/bin/bash
if [[ "${FIXTURE_FLOOR_FAIL:-0}" == 1 && "$*" == *recovery-policy-floor* ]] \
  || [[ -n "${FIXTURE_FLOOR_SYNC_DIR:-}" && "${*: -1}" == "$FIXTURE_FLOOR_SYNC_DIR" ]]; then
  echo 'fixture: floor fsync failed' >&2
  exit 1
fi
exec /usr/bin/sync "$@"
STUB
  cat >"$local_root/bin/mv" <<'STUB'
#!/bin/bash
if [[ "${FIXTURE_SWAP_FAIL:-0}" == 1 && "$1" == */.deploy-tools.* && "$2" == */deploy-tools ]]; then
  echo 'fixture: tool swap rename failed' >&2
  exit 1
fi
exec /usr/bin/mv "$@"
STUB
  cat >"$local_root/bin/install" <<'STUB'
#!/bin/bash
if [[ "${FIXTURE_COPY_FAIL:-0}" == 1 && "${*: -1}" == */.deploy-tools.*/backup.sh ]]; then
  echo 'fixture: tool copy failed' >&2
  exit 1
fi
exec /usr/bin/install "$@"
STUB
  chmod 755 "$local_root/bin/"* "$local_root/tools/"*.sh
  export PATH="$local_root/bin:$PATH" DEPLOY_DRILL=1
  # shellcheck source=deploy/digitalocean/checks.sh
  source "$root/deploy/digitalocean/checks.sh"
  new_fixture_host() {
    rm -rf "$local_root/dir"
    mkdir "$local_root/dir"
    printf 'fold-deploy-drill-v1:%s\n' "$local_root/dir" >"$local_root/dir/.deploy-drill"
    chmod 600 "$local_root/dir/.deploy-drill"
  }
  new_fixture_host
  git -C "$local_root/src" init -q
  git -C "$local_root/src" config user.name fixture
  git -C "$local_root/src" config user.email fixture@example.invalid
  git -C "$local_root/src" add .
  git -C "$local_root/src" commit -qm absent
  absent="$(git -C "$local_root/src" rev-parse HEAD)"
  fixture_case='metadata parsing'
  [[ "$(commit_recovery_level "$local_root/src" "$absent")" == 0 ]]
  declare -A commits=()
  for value in 0 1 2 bad; do
    printf '%s\n' "$value" >"$local_root/src/deploy/recovery-policy"
    git -C "$local_root/src" add .
    git -C "$local_root/src" commit -qm "level $value"
    commits[$value]="$(git -C "$local_root/src" rev-parse HEAD)"
  done
  for image in absent zero one two; do
    level="$(image_recovery_level "fixture:$image")"
    case "$image" in absent|zero) [[ "$level" == 0 ]] ;; one) [[ "$level" == 1 ]] ;; two) [[ "$level" == 2 ]] ;; esac
  done
  expect_failure() {
    local want="$1" rc=0; shift
    "$@" >"$local_root/out" 2>&1 || rc=$?
    if [[ "$rc" == 0 ]] || ! grep -qF -- "$want" "$local_root/out"; then
      printf 'recovery policy fixtures: expected failure containing %q, got exit %s from:' "$want" "$rc" >&2
      printf ' %q' "$@" >&2
      printf '\n' >&2
      exit 1
    fi
  }
  for image in bad null fraction; do expect_failure unparsable image_recovery_level "fixture:$image"; done
  expect_failure unparsable commit_recovery_level "$local_root/src" "${commits[bad]}"
  expect_failure '' commit_recovery_level "$local_root/src" deadbeef
  for invalid in -1 01 1.5 null 1000000000 $'1\n2' $'1\n\n'; do
    printf '%s' "$invalid" >"$local_root/bad-level"
    expect_failure unparsable read_recovery_level cat "$local_root/bad-level"
  done
  git -C "$local_root/src" checkout -q --detach "${commits[1]}"
  provision=("$local_root/tools/provision.sh" --allow-os --skip-harden --domain groups.example.org --deploy-dir "$local_root/dir" --source-dir "$local_root/src")
  trace_upgrade() (
    # bash -x alone stops at upgrade.sh's exec of the enforcing copy.
    # Export its changing option list so the new Bash and its children trace too.
    export SHELLOPTS
    bash -x "$@"
  )
  snapshot() {
    (cd "$local_root/dir" && find . -type f ! -name ops.lock ! -name ops.owner -print0 | sort -z | xargs -0 -r sha256sum)
    git -C "$local_root/src" rev-parse HEAD
    git -C "$local_root/src" status --porcelain
  }
  fixture_case='smoke and monotone floor'
  # Failed smoke creates no floor. A good smoke writes it, and max never falls.
  expect_failure 'fixture: smoke failed' env FIXTURE_SMOKE_FAIL=1 "${provision[@]}" --image fixture:one
  [[ ! -e "$local_root/dir/recovery-policy-floor" ]]
  # The current pin itself is also a floor, even before a successful smoke
  # has persisted the monotone file.
  expect_failure 'below host level 1' "${provision[@]}" --image fixture:zero
  [[ ! -e "$local_root/dir/recovery-policy-floor" ]]
  "${provision[@]}" --image fixture:one >"$local_root/out" 2>&1
  [[ "$(cat "$local_root/dir/recovery-policy-floor")" == 1 ]]
  write_recovery_floor "$local_root/dir" 0
  [[ "$(cat "$local_root/dir/recovery-policy-floor")" == 1 ]]
  [[ "$(stat -c '%a:%u' "$local_root/dir/recovery-policy-floor")" == 644:0 ]]
  fixture_case='pinned image diagnostics'
  before="$(snapshot)"
  expect_failure 'cannot resolve the deployed pin' env FIXTURE_CONFIG_FAIL=1 "${provision[@]}" --image fixture:one
  expect_failure 'no valid app image pin' env FIXTURE_CONFIG_BAD=1 "${provision[@]}" --image fixture:one
  expect_failure 'cannot inspect deployed pin fixture:one' env FIXTURE_INSPECT_FAIL=1 "${provision[@]}" --image fixture:one
  grep -qF 'Do not change the pin or lower the floor' "$local_root/out"
  [[ "$before" == "$(snapshot)" ]]
  fixture_case='provision refusal before mutation'
  # Refusal precedes every pin and Compose up; parsing failures refuse too.
  for image in absent zero bad; do
    before="$(snapshot)"; : >"$FIXTURE_LOG"
    expect_failure 'recovery' "${provision[@]}" --image "fixture:$image"
    [[ "$before" == "$(snapshot)" ]]
    if grep -q 'compose .* up' "$FIXTURE_LOG"; then exit 1; fi
  done
  for value in 0 bad; do
    git -C "$local_root/src" checkout -q --detach "${commits[$value]}"
    before="$(snapshot)"
    expect_failure recovery "${provision[@]}" --allow-onbox-build
    [[ "$before" == "$(snapshot)" ]]
  done
  git -C "$local_root/src" checkout -q --detach "${commits[1]}"
  for mode in image build; do
    fixture_case="post-pull mismatch ($mode)"
    args=(--image fixture:one); [[ "$mode" == image ]] || args=(--allow-onbox-build)
    "${provision[@]}" "${args[@]}" >"$local_root/out" 2>&1
    before="$(snapshot)"; : >"$FIXTURE_LOG"
    rm -f "$FIXTURE_LOG.ready"
    expect_failure 'restored previous pin' env FIXTURE_RESULT_IMAGE=fixture:two "${provision[@]}" "${args[@]}"
    [[ "$before" == "$(snapshot)" ]]
    if grep -q 'compose .* up' "$FIXTURE_LOG"; then exit 1; fi
  done
  fixture_case='higher provision level'
  "${provision[@]}" --image fixture:two >"$local_root/out" 2>&1
  [[ "$(cat "$local_root/dir/recovery-policy-floor")" == 2 ]]
  # Return the fixture host to floor 1 only by constructing a new test host.
  new_fixture_host
  "${provision[@]}" --image fixture:one >"$local_root/out" 2>&1
  mkdir "$local_root/dir/deploy-tools"
  cp "$local_root/tools/"* "$local_root/dir/deploy-tools/"
  chmod 700 "$local_root/dir/deploy-tools"
  upgrade_args=(--deploy-dir "$local_root/dir" --source-dir "$local_root/src")
  for entry in "$local_root/dir/upgrade.sh" "$local_root/tools/upgrade.sh"; do
    fixture_case="upgrade refusal ($entry)"
    for target in "$absent" "${commits[0]}"; do
      before="$(snapshot)"; : >"$FIXTURE_LOG"
      expect_failure 'target recovery level 0 is below host level 1' "$entry" "${upgrade_args[@]}" --commit "$target"
      [[ "$before" == "$(snapshot)" ]]
      [[ ! -e "$local_root/dir/release.prev" && ! -d "$local_root/dir/dumps" ]]
      [[ "$(git -C "$local_root/src" rev-parse HEAD)" == "${commits[1]}" ]]
      if grep -qE '^backup$|compose .* up' "$FIXTURE_LOG"; then exit 1; fi
    done
    for image in absent zero; do
      expect_failure 'below host level 1' "$entry" "${upgrade_args[@]}" --image "fixture:$image"
      [[ ! -e "$local_root/dir/release.prev" && ! -d "$local_root/dir/dumps" ]]
    done
    expect_failure 'not a commit' "$entry" "${upgrade_args[@]}" --commit deadbeef
    fixture_case="enforcing provisioner ($entry)"
    # A target provisioner that would bypass all checks must never execute.
    mkdir -p "$local_root/src/deploy/digitalocean"
    printf '#!/bin/bash\ntouch "%s"\n' "$local_root/marker" >"$local_root/src/deploy/digitalocean/provision.sh"
    git -C "$local_root/src" add .; git -C "$local_root/src" commit -qm trap
    same="$(git -C "$local_root/src" rev-parse HEAD)"
    : >"$FIXTURE_LOG"
    trace_upgrade "$entry" "${upgrade_args[@]}" --commit "$same" >"$local_root/trace" 2>&1
    [[ "$(grep -cE '^docker compose .* up( |$)' "$FIXTURE_LOG")" == 1 ]]
    [[ ! -e "$local_root/marker" ]]
    grep -qF "$local_root/dir/deploy-tools/provision.sh --skip-harden" "$local_root/trace"
    if grep -qF "$local_root/src/deploy/digitalocean/provision.sh --skip-harden" "$local_root/trace"; then exit 1; fi
    cmp "$local_root/tools/provision.sh" "$local_root/dir/provision.sh"
    diff -r "$local_root/tools" "$local_root/dir/deploy-tools"
    : >"$FIXTURE_LOG"
    trace_upgrade "$entry" "${upgrade_args[@]}" --image fixture:one >"$local_root/trace" 2>&1
    [[ "$(grep -cE '^docker compose .* up( |$)' "$FIXTURE_LOG")" == 1 ]]
    grep -qF "$local_root/dir/deploy-tools/provision.sh --skip-harden" "$local_root/trace"
    diff -r "$local_root/tools" "$local_root/dir/deploy-tools"
    fixture_case="rollback refusal ($entry)"
    # Rollback level check precedes restoring or starting services.
    sed -i 's/^previous_image=.*/previous_image=fixture:zero/; s/^mode=.*/mode=image/' "$local_root/dir/release.prev"
    before="$(snapshot)"; : >"$FIXTURE_LOG"
    expect_failure 'below host level 1' "$entry" "${upgrade_args[@]}" --rollback
    [[ "$before" == "$(snapshot)" ]]
    if grep -qE '^backup$|compose .* (stop|up|exec)' "$FIXTURE_LOG"; then exit 1; fi
    sed -i "s/^mode=.*/mode=build/; s/^previous_commit=.*/previous_commit=${commits[0]}/" "$local_root/dir/release.prev"
    before="$(snapshot)"; : >"$FIXTURE_LOG"
    expect_failure 'below host level 1' "$entry" "${upgrade_args[@]}" --rollback
    [[ "$before" == "$(snapshot)" ]]
    fixture_case="whole tool-set replacement ($entry)"
    # Whole-set replacement removes stale files.
    touch "$local_root/dir/deploy-tools/stale"
    expect_failure 'below host level 1' "$entry" "${upgrade_args[@]}" --image fixture:zero
    [[ ! -e "$local_root/dir/deploy-tools/stale" ]]
    rm -f "$local_root/dir/release.prev"; rm -rf "$local_root/dir/dumps"
    git -C "$local_root/src" checkout -q --detach "${commits[1]}"
  done
  fixture_case='higher forward levels'
  # Higher forward levels pass through the same enforcing provisioner.
  "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:two >"$local_root/out" 2>&1
  [[ "$(cat "$local_root/dir/recovery-policy-floor")" == 2 ]]
  rm -f "$FIXTURE_LOG.ready"
  FIXTURE_RESULT_IMAGE=fixture:two "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --commit "${commits[2]}" >"$local_root/out" 2>&1
  fixture_case='old tools and drill guards'
  # Create another test host for the remaining level-one lock assertions.
  new_fixture_host
  git -C "$local_root/src" checkout -q --detach "${commits[1]}"
  "${provision[@]}" --image fixture:one >"$local_root/out" 2>&1
  mkdir "$local_root/old-tools"
  cp "$local_root/tools/"{upgrade,provision,checks}.sh "$local_root/old-tools/"
  # Keep argument validation and locking real; omit only the level interface
  # whose absence must produce the install-current-tools diagnostic.
  printf '\nunset -f image_recovery_level commit_recovery_level host_recovery_floor write_recovery_floor\n' >>"$local_root/old-tools/checks.sh"
  expect_failure 'run the current provision.sh' "$local_root/old-tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:one
  # A real FIFO must not make an unauthorized hook usable. Bound this call
  # so a regression that waits on it fails promptly instead of hanging CI.
  mkfifo "$local_root/dir/drill.fifo"
  before="$(snapshot)"; : >"$FIXTURE_LOG"
  # shellcheck disable=SC2016 # Source and directory are child arguments.
  expect_failure DEPLOY_DRILL_PAUSE timeout 5 env DEPLOY_DRILL=0 DEPLOY_DRILL_PAUSE=after-lock \
    bash -c 'source "$1"; deploy_drill_pause "$2" after-lock' _ "$local_root/tools/checks.sh" "$local_root/dir"
  [[ "$before" == "$(snapshot)" && ! -s "$FIXTURE_LOG" && ! -e "$local_root/dir/drill.ready" ]]
  # The same hook remains usable with the private marker and explicit drill
  # opt-in. Drive the FIFO, then reap its bounded holder before continuing.
  # shellcheck disable=SC2016 # Child arguments identify this fixture only.
  timeout 5 env DEPLOY_DRILL=1 DEPLOY_DRILL_PAUSE=after-lock \
    bash -c 'source "$1"; deploy_drill_pause "$2" after-lock' _ "$local_root/tools/checks.sh" "$local_root/dir" &
  pause_holder=$!
  # shellcheck disable=SC2016 # Wait for the child's atomic readiness record.
  timeout 5 bash -c 'until [[ -f "$1/drill.ready" ]]; do sleep .01; done; printf "continue\n" >"$1/drill.fifo"' _ "$local_root/dir"
  wait "$pause_holder"
  [[ "$(cat "$local_root/dir/drill.ready")" == after-lock && ! -s "$FIXTURE_LOG" ]]
  unlink "$local_root/dir/drill.ready"
  for entry in "${provision[0]}" "$local_root/tools/upgrade.sh"; do
    args=("${provision[@]:1}" --image fixture:one)
    [[ "$entry" == "${provision[0]}" ]] || args=("${upgrade_args[@]}" --image fixture:one)
    before="$(snapshot)"; : >"$FIXTURE_LOG"
    expect_failure DEPLOY_DRILL_PAUSE timeout 5 env DEPLOY_DRILL=0 DEPLOY_DRILL_PAUSE=after-lock "$entry" "${args[@]}"
    [[ "$before" == "$(snapshot)" && ! -s "$FIXTURE_LOG" && ! -e "$local_root/dir/drill.ready" ]]
    expect_failure /opt/fold env DEPLOY_DRILL_PAUSE=after-lock "$entry" "${args[@]}" --deploy-dir /opt/fold
    fixture_case="drill marker guards ($entry)"
    mv "$local_root/dir/.deploy-drill" "$local_root/marker-saved"
    before="$(snapshot)"; : >"$FIXTURE_LOG"
    expect_failure 'DEPLOY_DRILL is test only' "$entry" "${args[@]}"
    [[ "$before" == "$(snapshot)" && ! -s "$FIXTURE_LOG" ]]
    mv "$local_root/marker-saved" "$local_root/dir/.deploy-drill"
    chmod 644 "$local_root/dir/.deploy-drill"
    expect_failure '.deploy-drill marker' "$entry" "${args[@]}"
    chmod 600 "$local_root/dir/.deploy-drill"
    printf 'fold-deploy-drill-v1:/another-directory\n' >"$local_root/dir/.deploy-drill"
    expect_failure '.deploy-drill marker' "$entry" "${args[@]}"
    printf 'fold-deploy-drill-v1:%s\n' "$local_root/dir" >"$local_root/dir/.deploy-drill"
    mv "$local_root/dir/.deploy-drill" "$local_root/marker-saved"
    ln -s "$local_root/marker-saved" "$local_root/dir/.deploy-drill"
    expect_failure '.deploy-drill marker' "$entry" "${args[@]}"
    rm "$local_root/dir/.deploy-drill"
    mv "$local_root/marker-saved" "$local_root/dir/.deploy-drill"
    touch "$local_root/dir/backup.env"
    expect_failure 'with backup.env' "$entry" "${args[@]}"
    rm "$local_root/dir/backup.env"
    ln -s /opt/fold "$local_root/prod-alias"
    expect_failure /opt/fold "$entry" "${args[@]}" --dry-run --deploy-dir "$local_root/prod-alias"
    rm "$local_root/prod-alias"
  done
  fixture_case='tool swap recovery and ordering'
  "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:one >"$local_root/out" 2>&1
  expect_failure 'fixture: tool copy failed' env FIXTURE_COPY_FAIL=1 "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:one
  diff -r "$local_root/tools" "$local_root/dir/deploy-tools"
  [[ -z "$(find "$local_root/dir" -maxdepth 1 -name '.*deploy-tools.*' -print)" ]]
  expect_failure 'fixture: tool swap rename failed' env FIXTURE_SWAP_FAIL=1 "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:one
  diff -r "$local_root/tools" "$local_root/dir/deploy-tools"
  [[ -z "$(find "$local_root/dir" -maxdepth 1 -name '.*deploy-tools.*' -print)" ]]
  # Multiple saved sets require explicit recovery and are never deleted.
  mv "$local_root/dir/deploy-tools" "$local_root/dir/.old-deploy-tools.first"
  cp -a "$local_root/dir/.old-deploy-tools.first" "$local_root/dir/.old-deploy-tools.second"
  before="$(snapshot)"; : >"$FIXTURE_LOG"
  expect_failure 'cannot identify one complete saved set' "$local_root/dir/upgrade.sh" "${upgrade_args[@]}" --image fixture:one
  [[ "$before" == "$(snapshot)" && ! -s "$FIXTURE_LOG" ]]
  mv "$local_root/dir/.old-deploy-tools.first" "$local_root/dir/deploy-tools"
  rm -rf "$local_root/dir/.old-deploy-tools.second"
  # Model SIGKILL between the two renames, plus a partial stage left behind.
  mv "$local_root/dir/deploy-tools" "$local_root/dir/.old-deploy-tools.interrupted"
  mkdir "$local_root/dir/.deploy-tools.interrupted"
  touch "$local_root/dir/.deploy-tools.interrupted/partial"
  expect_failure 'below host level 1' "$local_root/dir/upgrade.sh" "${upgrade_args[@]}" --image fixture:zero
  diff -r "$local_root/tools" "$local_root/dir/deploy-tools"
  [[ -z "$(find "$local_root/dir" -maxdepth 1 -name '.*deploy-tools.*' -print)" ]]
  printf '5\n' >"$local_root/dir/deploy-tools/deploy-tools-version"
  before="$(snapshot)"; : >"$FIXTURE_LOG"
  expect_failure 'older than installed version 5' "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:one
  expect_failure 'older than installed version 5' "${provision[@]}" --image fixture:one
  [[ "$before" == "$(snapshot)" && ! -s "$FIXTURE_LOG" ]]
  cp "$local_root/tools/deploy-tools-version" "$local_root/dir/deploy-tools/"
  fixture_case='truthful dry runs'
  before="$(snapshot)"; : >"$FIXTURE_LOG"
  expect_failure 'below host level 1' "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --dry-run --commit "${commits[0]}"
  git -C "$local_root/src" checkout -q --detach "${commits[0]}"
  expect_failure 'below host level 1' "${provision[@]}" --dry-run --allow-onbox-build
  git -C "$local_root/src" checkout -q --detach "${commits[1]}"
  "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --dry-run --commit "${commits[1]}" >"$local_root/out" 2>&1
  grep -q 'target commit recovery level 1' "$local_root/out"
  grep -q 'full host floor is unverified' "$local_root/out"
  "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --dry-run --image fixture:zero >"$local_root/out" 2>&1
  grep -q 'target image recovery level is unverified' "$local_root/out"
  [[ "$before" == "$(snapshot)" ]]
  if grep -v '^docker compose version$' "$FIXTURE_LOG"; then exit 1; fi
  sed -i "s/^mode=.*/mode=build/; s/^previous_commit=.*/previous_commit=${commits[0]}/" "$local_root/dir/release.prev"
  expect_failure 'below host level 1' "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --dry-run --rollback
  # A missing recorded checkout is a stated limitation in dry-run rollback.
  sed -i "s/^mode=.*/mode=build/; s/^previous_commit=.*/previous_commit=${commits[1]}/; s|^source_dir=.*|source_dir=$local_root/missing-checkout|" "$local_root/dir/release.prev"
  "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --dry-run --rollback >"$local_root/out" 2>&1
  grep -q 'recovery level is unverified' "$local_root/out"
  if grep -v '^docker compose version$' "$FIXTURE_LOG"; then exit 1; fi
  fixture_case='failed forward upgrade guidance'
  # A higher pin contributes to the floor even though smoke never persisted it.
  expect_failure 'rollback is refused' env FIXTURE_SMOKE_FAIL=1 "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:two
  if grep -q 'Roll back with:' "$local_root/out"; then exit 1; fi
  [[ "$(cat "$local_root/dir/recovery-policy-floor")" == 1 ]]
  expect_failure 'below host level 2' "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --rollback
  fixture_case='floor persistence outcomes'
  for fault in file directory; do
    new_fixture_host
    "${provision[@]}" --image fixture:one >"$local_root/out" 2>&1
    fault_env=(FIXTURE_FLOOR_FAIL=1)
    [[ "$fault" != directory ]] || fault_env=("FIXTURE_FLOOR_SYNC_DIR=$local_root/dir")
    : >"$FIXTURE_LOG"; rc=0
    env "${fault_env[@]}" "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:two >"$local_root/out" 2>&1 || rc=$?
    [[ "$rc" == 76 ]]
    grep -q 'deployment completed and smoke passed' "$local_root/out"
    grep -q 'compose .* up' "$FIXTURE_LOG"
    grep -q '^smoke$' "$FIXTURE_LOG"
    if grep -qE 'upgrade failed|Roll back with:' "$local_root/out"; then exit 1; fi
    [[ "$(host_recovery_floor "$local_root/dir")" == 2 ]]
    expect_failure 'below host level 2' "${provision[@]}" --image fixture:one
    "${provision[@]}" --image fixture:two >"$local_root/out" 2>&1
    [[ "$(cat "$local_root/dir/recovery-policy-floor")" == 2 ]]
  done
  new_fixture_host
  "${provision[@]}" --image fixture:one >"$local_root/out" 2>&1
  fixture_case='lock refusal and inherited descriptor'
  # Independent open file descriptions conflict; inherited fd 9 succeeds.
  (
    ops_lock_acquire "$local_root/dir" fixture
    before="$(snapshot)"
    for mode in provision forward rollback; do
      rc=0
      case "$mode" in
        provision) (exec 9>&-; "${provision[@]}" --image fixture:one) >"$local_root/out" 2>&1 || rc=$? ;;
        forward) (exec 9>&-; "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --image fixture:one) >"$local_root/out" 2>&1 || rc=$? ;;
        rollback) (exec 9>&-; "$local_root/tools/upgrade.sh" "${upgrade_args[@]}" --rollback) >"$local_root/out" 2>&1 || rc=$? ;;
      esac
      [[ "$rc" == 75 && "$before" == "$(snapshot)" ]]
      grep -q 'Nothing changed' "$local_root/out"
    done
    "${provision[@]}" --image fixture:one >"$local_root/out" 2>&1
  )
  echo 'recovery policy fixtures: ok'
)

# The identity table is the approved recovery protocol, including durable
# cleanup and abort drop intents. Exercise every cell rather than just the
# successful restore path. These are fixtures, not Docker containers.
restore_fixtures() (
  set -Eeuo pipefail
  # shellcheck source=deploy/digitalocean/checks.sh
  source "$root/deploy/digitalocean/checks.sh"
  local work phase observation cleanup want got at no_original intent row
  work="$(mktemp -d /tmp/fold-restore-fixture.XXXXXX)"
  trap 'rm -rf "$work"' EXIT
  mkdir -p "$work/pins/.pin-save.one" "$work/outside"
  touch "$work/pins/.pin-save.one/.env" "$work/outside/keep"
  ln -s "$work/outside" "$work/pins/.pin-save.link"
  remove_stale_pin_saves "$work/pins"
  [[ -f "$work/outside/keep" && ! -e "$work/pins/.pin-save.one" && ! -L "$work/pins/.pin-save.link" ]]
  while IFS='|' read -r phase cleanup at intent observations; do
    [[ -n "$phase" ]] || continue
    for observation in Q Q+ B H A C invalid; do
      got=0; recovery_accepts "$phase" "$observation" "$cleanup" "$at" 0 "$intent" || got=$?
      want=1; [[ " $observations " != *" $observation "* ]] || want=0
      [[ "$got" == "$want" ]] || { echo "recovery table mismatch: $phase/$cleanup/$at/$intent/$observation" >&2; exit 1; }
    done
  done <<'TABLE'
quiesce|none||0|Q Q+
create-intent|none||0|Q Q+
created|none||0|B
collect-drop-intent|none||0|B Q
collect-failed|none||0|Q
collected|none||0|B
pin-intent|none||0|B
pinned|none||0|B
pin-failed|none||0|B
swap-failed|none||0|B
swap-intent|none||0|B H A
swap-1|none||0|B H A
pre-drop-intent|drop-intent||0|A C
pre-dropped|dropped||0|C
floor-written|dropped||0|C
abort-intent|none|B|0|B
abort-intent|none|Q+|0|Q+
abort-intent|none|Q|0|Q
abort-drop-intent|none||0|B Q+
abort-drop-intent|none||1|B Q+ Q
abort-dropped|none||1|Q
abort-start-intent|none||1|Q
abort-smoke-intent|none||1|Q
abort-failed|none|drop|0|B Q+
abort-failed|none|drop|1|B Q+ Q
abort-failed|none|start|1|Q
abort-failed|none|smoke|1|Q
reconcile-needed|none||0|
TABLE
  for phase in restored recovery-intent recovering recovery-failed reopen-closed reopen-intent app-start-intent caddy-start-intent smoke-intent reopen-failed cleanup-failed; do
    for cleanup in none drop-intent dropped; do
      case "$cleanup" in none) observations=A ;; drop-intent) observations='A C' ;; dropped) observations=C ;; esac
      for observation in Q Q+ B H A C invalid; do
        got=0; recovery_accepts "$phase" "$observation" "$cleanup" '' 0 || got=$?
        want=1; [[ " $observations " != *" $observation "* ]] || want=0
        [[ "$got" == "$want" ]] || { echo "CLEANUP mismatch: $phase/$cleanup/$observation" >&2; exit 1; }
      done
    done
  done
  # With no original there is no P. A/C collapse; removal is still monotone.
  for no_original in 0 1; do
    got=0; recovery_accepts recovering C none '' "$no_original" || got=$?
    [[ "$got" != "$no_original" ]]
  done
  [[ "$(recovery_classify 10 '' 10 '' '')" == Q ]]
  [[ "$(recovery_classify 10 '' 10 20 '')" == Q+ ]]
  [[ "$(recovery_classify 10 20 10 20 '')" == B ]]
  [[ "$(recovery_classify 10 20 '' 20 10)" == H ]]
  [[ "$(recovery_classify 10 20 20 '' 10)" == A ]]
  [[ "$(recovery_classify 10 20 20 '' '')" == C ]]
  [[ "$(recovery_classify 10 20 99 '' 10)" == invalid ]]
  [[ "$(recovery_classify 10 20 '' 20 '')" == invalid ]]
  [[ "$(recovery_classify 10 20 20 20 10)" == invalid ]]
  for phase in quiesce create-intent created collect-drop-intent collect-failed collected pin-intent pin-failed swap-failed swap-intent; do
    for observation in Q Q+ B; do
      recovery_abort_allowed "$phase" "$observation" previous
      if recovery_abort_allowed "$phase" "$observation" target; then exit 1; fi
      if recovery_abort_allowed "$phase" "$observation" partial; then exit 1; fi
    done
    for observation in H A C invalid; do if recovery_abort_allowed "$phase" "$observation" previous; then exit 1; fi; done
  done
  for phase in pinned restored recovering reconcile-needed pre-drop-intent reopen-failed; do
    if recovery_abort_allowed "$phase" B previous; then exit 1; fi
  done
  # CLI refusals and read-only dry runs execute the real installed script.
  mkdir "$work/tools" "$work/site" "$work/bin"
  while IFS= read -r row; do cp "$root/deploy/digitalocean/$row" "$work/tools/"; done < <(runtime_tools)
  chmod 755 "$work/tools/"*.sh
  printf 'fold-deploy-drill-v1:%s\n' "$work/site" >"$work/site/.deploy-drill"
  chmod 600 "$work/site/.deploy-drill"
  cat >"$work/bin/docker" <<'SHIM'
#!/bin/bash
printf 'unexpected docker invocation: %s\n' "$*" >>"$RESTORE_FIXTURE_LOG"
exit 90
SHIM
  chmod 755 "$work/bin/docker"
  export RESTORE_FIXTURE_LOG="$work/calls" PATH="$work/bin:$PATH"
  : >"$RESTORE_FIXTURE_LOG"
  local -a restore=("$work/tools/restore.sh" --deploy-dir "$work/site")
  "${restore[@]}" --dry-run --dump "$work/archive" >"$work/out"
  grep -q 'snapshot-safe' "$work/out"
  [[ ! -e "$work/site/ops.lock" && ! -e "$work/site/recovery-tools" && ! -s "$RESTORE_FIXTURE_LOG" ]]
  for phase in --resume --abort --reopen --status; do "${restore[@]}" --dry-run "$phase" >"$work/out"; done
  for row in RESTORE_DRILL_PAUSE=after-lock RESTORE_DRILL_FAULT=after-create RESTORE_DRILL_FORCE_TWO_STEP=1; do
    got=0; env "$row" "${restore[@]}" --dry-run --dump "$work/archive" >"$work/out" 2>&1 || got=$?
    [[ "$got" != 0 ]]; grep -q 'require --drill' "$work/out"
  done
  got=0; "${restore[@]}" --dry-run --hold >"$work/out" 2>&1 || got=$?
  [[ "$got" != 0 ]]; grep -q 'unknown option: --hold' "$work/out"
  rm "$work/tools/compose.recovery.yml"
  got=0; "${restore[@]}" --dry-run --dump "$work/archive" >"$work/out" 2>&1 || got=$?
  [[ "$got" != 0 ]]; grep -q 'missing compose.recovery.yml' "$work/out"
  cp "$root/deploy/digitalocean/compose.recovery.yml" "$work/tools/"
  # Losers must reach no tool copy, state read or Docker observation.
  (
    ops_lock_acquire "$work/site" fixture
    for phase in dump resume abort reopen; do
      local -a mode=("--$phase")
      [[ "$phase" != dump ]] || mode+=("$work/archive")
      got=0
      (exec 9>&-; "${restore[@]}" "${mode[@]}") >"$work/out" 2>&1 || got=$?
      [[ "$got" == 75 ]]; grep -q 'Nothing changed' "$work/out"
    done
    [[ ! -d "$work/site/recovery-tools" && ! -s "$RESTORE_FIXTURE_LOG" ]]
  )
  # Correct nonce alone does not authorize a pin from an outside shell.
  printf '{"nonce":"0123456789abcdef0123456789abcdef","phase":"pin-intent"}\n' >"$work/site/recovery.lock"
  chmod 600 "$work/site/recovery.lock"
  got=0
  "$work/tools/provision.sh" --skip-harden --pin-only --recovery-nonce 0123456789abcdef0123456789abcdef \
    --deploy-dir "$work/site" --source-dir "$root" --domain groups.example.org --image fixture:one >"$work/out" 2>&1 || got=$?
  [[ "$got" != 0 ]]; grep -q 'inherited operations lock' "$work/out"
  [[ ! -s "$RESTORE_FIXTURE_LOG" ]]
  echo 'restore protocol fixtures: ok'
)

# Each fixture script is its own process, so naming a failure here changes
# nothing about what it checks. (The two functions above are called directly:
# wrapping them in `||` would switch errexit off inside them.)
run_fixture_script() {
  local script="$1" rc=0
  bash "$root/deploy/digitalocean/$script" || rc=$?
  if (( rc != 0 )); then
    echo "dry-run-test.sh: fixture $script failed (exit $rc)" >&2
    exit "$rc"
  fi
}

if [[ "${1:-}" == --fixtures ]]; then
  recovery_fixtures
  restore_fixtures
  run_fixture_script deploy-drill-fixtures.sh
  run_fixture_script restore-drill-fixtures.sh
  run_fixture_script restore-crossings-fixtures.sh
  run_fixture_script restore-cli-fixtures.sh
  run_fixture_script qc1-fixtures.sh
  run_fixture_script isolation-fixtures.sh
  run_fixture_script qc1-runtime-fixtures.sh
  run_fixture_script runner-compat-fixtures.sh
  exit 0
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "dry-run-test.sh: docker is required" >&2
  exit 1
fi

# Comments may say the scripts never call doctl. A command would not be a comment.
tracked="$(git -C "$root" ls-files -- .env backup.env)"
if [[ -n "$tracked" ]]; then
  echo "dry-run-test.sh: secret env file is tracked: $tracked" >&2
  exit 1
fi

# The test harness names those commands so it can search for them.
# The scripts that run on a server must not.
hits="$(grep -nE 'doctl|droplet create|spaces create' \
  "$root/deploy/digitalocean/provision.sh" \
  "$root/deploy/digitalocean/backup.sh" \
  "$root/deploy/digitalocean/smoke.sh" \
  "$root/deploy/digitalocean/email-dns.sh" \
  "$root/deploy/digitalocean/upgrade.sh" \
  "$root/deploy/digitalocean/checks.sh" || true)"
if [[ -n "$hits" ]] && grep -vE ':[0-9]+:[[:space:]]*#' <<<"$hits"; then
  echo "dry-run-test.sh: a script contains a DigitalOcean create command" >&2
  exit 1
fi

# Linked worktrees keep their objects and per-worktree HEAD outside this tree.
# Mount that metadata read-only at the path named by the .git file too.
mounts=(-v "$root:/src:ro")
if [[ -f "$root/.git" ]]; then
  git_common="$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)"
  mounts+=(-v "$git_common:$git_common:ro")
fi
before="$(git -C "$root" status --porcelain)"
# No xtrace mode on purpose: bash -x prints the values of variables, and these
# scripts handle secrets (even disposable ones in this test). The FAILED line
# from the ERR traps names the script, line and command without values.
inside=(bash /src/deploy/digitalocean/dry-run-inside.sh)
container_rc=0
docker run --rm \
  "${mounts[@]}" \
  -w /src \
  "$container_image" \
  "${inside[@]}" || container_rc=$?
if (( container_rc != 0 )); then
  echo "dry-run-test.sh: the container run exited $container_rc; the last FAILED line above names the script, line and command" >&2
  exit "$container_rc"
fi
after="$(git -C "$root" status --porcelain)"
if [[ "$before" != "$after" ]]; then
  echo "dry-run-test.sh: the worktree changed" >&2
  git -C "$root" status --porcelain >&2
  exit 1
fi

echo "dry-run-test.sh: ok"
