#!/usr/bin/env bash
# Runs restore-drill.sh inside a throwaway container with its OWN Docker daemon
# (docker:dind), so the drill's provisioners, Compose stacks, ports 80 and 443
# and /etc entries exist only in that container's filesystem and network. It
# starts exactly one container on the daemon it is run against and removes it
# (by id) at the end.
#
#   deploy/digitalocean/restore-drill-nested.sh --out NEW_DIR [--dind-image REF] [-- drill args...]
#   deploy/digitalocean/restore-drill-nested.sh --out NEW_DIR --qc1 [--dind-image REF] [-- qc1-measure.sh args...]
#
# With --qc1 the runner measures QC-1 instead of running the restore drill.
#
# Both modes drive a real Chromium: the restore drill's recovery proof
# (testdata/restore-browser.cjs, through web/node_modules/playwright) and the QC-1
# panel measurement (through @playwright/test). The nested Alpine runner has
# neither, so in BOTH modes, before anything is built, drilled or measured,
# qc1/browser-prepare.sh installs Alpine's chromium, runs `npm ci` in web/ exactly
# as web/package-lock.json locks it (no install scripts, no optional packages, no
# Playwright browser download), and proves that Playwright resolves from
# web/node_modules and that the installed Chromium launches, through each
# consumer's own entry point with each consumer's own launch options
# (qc1/browser-launch.cjs). The Chromium it found (QC1_CHROMIUM, with the sandbox
# switches only this disposable container may use) is exported to the run, so the
# run launches the executable the preflight launched. restore-drill.sh and
# qc1-measure.sh repeat the preflight themselves. A failure ends the run before
# the stack is built. The drill has no switch to skip this; for --qc1 only,
# --no-browser (after --) skips it on purpose, which leaves the browser rows
# missing and the evidence incomplete. The preflight's result is kept in
# <out>/prep/browser-runtime.json and, for the drill, <out>/browser-preflight.json.
#
# What it refuses or never does:
#   - a clean, committed HEAD is required (the drill builds from git archive);
#   - the nested container gets no bind mounts, no host network, no Docker
#     socket and no host paths: the repository goes in as a git bundle through
#     `docker cp`, the evidence comes out the same way, into the new --out
#     directory; after it starts, its mounts are read back and it is torn down
#     if they hold anything but its own /var/lib/docker volume;
#   - it needs --privileged, which Docker allows only to a daemon you control; a
#     rootless daemon that refuses privileged containers cannot host this drill
#     (the drill then cannot run at all, which is the intended failure);
#   - the drill inside refuses to start unless it finds itself in such a
#     container (isolation.sh).
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
OUT="" DIND=docker:29-dind QC1=0
drill_args=()
while (( $# )); do
  case "$1" in
    --out) OUT="${2:?new evidence directory required}"; shift 2 ;;
    --dind-image) DIND="${2:?image required}"; shift 2 ;;
    --qc1) QC1=1; shift ;;
    --help) sed -n '2,/^set -Eeuo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    --) shift; drill_args=("$@"); break ;;
    *) echo "unknown option $1 (drill arguments go after --)" >&2; exit 1 ;;
  esac
done
for tool in docker git tar; do command -v "$tool" >/dev/null || { echo "restore-drill-nested: $tool is required" >&2; exit 1; }; done
[[ -n "$OUT" ]] || { echo 'restore-drill-nested: --out NEW_DIR is required' >&2; exit 1; }
OUT="$(realpath -m "$OUT")"
[[ ! -e "$OUT" ]] || { echo "restore-drill-nested: $OUT already exists" >&2; exit 1; }
case "${DOCKER_HOST:-}" in ''|unix://*) ;; *) echo "restore-drill-nested: DOCKER_HOST is remote (${DOCKER_HOST}); the nested daemon must be started on a daemon you control locally" >&2; exit 1 ;; esac
[[ -z "$(git --no-optional-locks -C "$ROOT" status --porcelain)" ]] || { echo 'restore-drill-nested: the tree is dirty; commit first' >&2; exit 1; }
HEAD_SHA="$(git -C "$ROOT" rev-parse HEAD)"

RUN_ID="$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
NAME="fold-drill-$RUN_ID"
CID=""
TMP="$(mktemp -d)"
# shellcheck disable=SC2329 # EXIT trap.
cleanup() {
  local rc=$?
  trap - EXIT
  [[ -z "$CID" ]] || docker rm -fv "$CID" >/dev/null 2>&1 || true
  rm -rf -- "$TMP"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

git -C "$ROOT" bundle create "$TMP/fold.bundle" --all >/dev/null 2>&1
mkdir "$OUT"
# No -v, no --network host, no docker.sock: the one thing this container may do
# is run its own daemon. The image's own anonymous /var/lib/docker volume is the
# only mount it will have.
CID="$(docker run -d --name "$NAME" --hostname "$NAME" --privileged -e DOCKER_TLS_CERTDIR= "$DIND")"
mounts="$(docker inspect --format '{{range .Mounts}}{{.Destination}} {{.Source}}{{"\n"}}{{end}}' "$CID")"
bad="$(awk '$1 != "/var/lib/docker" && NF' <<<"$mounts")"
if [[ -n "$bad" || "$mounts" == *docker.sock* ]]; then
  echo "restore-drill-nested: the container has mounts beyond its own /var/lib/docker; refusing:" >&2
  printf '%s\n' "$mounts" >&2
  exit 1
fi
if [[ "$(docker inspect --format '{{.HostConfig.NetworkMode}}' "$CID")" == host ]]; then echo 'restore-drill-nested: the container uses the host network' >&2; exit 1; fi
for ((i=0;i<120;i++)); do docker exec "$CID" docker info >/dev/null 2>&1 && break; sleep 1; done
docker exec "$CID" docker info >/dev/null
docker exec "$CID" apk add --no-cache bash git jq curl go nodejs npm util-linux coreutils findutils tar make openssl >/dev/null
docker cp "$TMP/fold.bundle" "$CID:/work.bundle"
docker exec "$CID" sh -c "git clone -q /work.bundle /work/fold && cd /work/fold && git checkout -q --detach $HEAD_SHA && test -z \"\$(git status --porcelain)\""
rc=0
if (( QC1 )); then what=qc1; else what=drill; fi
skip_browser=0
if (( QC1 )); then
  for a in "${drill_args[@]}"; do [[ "$a" != --no-browser ]] || skip_browser=1; done
fi
# Both modes need a browser: the restore drill's recovery proof (restore-browser.cjs) and the QC-1 panel measurement each drive Chromium through
# web/node_modules. It is installed and proved BEFORE anything is built, drilled or measured, in this runner and nowhere else. Only a QC-1 run given
# --no-browser skips it, on purpose; the drill has no such switch.
if (( ! skip_browser )); then
  # The drill also needs Firefox (glibc, cannot run on this Alpine runner): the preparation builds a Debian container for it and proves it launches.
  prep_env=(-e QC1_WEB_DIR=/work/fold/web)
  if (( ! QC1 )); then prep_env+=(-e QC1_PREPARE_FIREFOX=1); fi
  docker exec "${prep_env[@]}" -w /work/fold "$CID" bash deploy/digitalocean/qc1/browser-prepare.sh /work/prep || rc=$?
  if (( rc != 0 )); then
    docker cp "$CID:/work/prep/." "$OUT/prep/" >/dev/null 2>&1 || true
    echo "restore-drill-nested: the browser runtime could not be prepared (exit $rc); nothing was run ($what)" >&2
    exit "$rc"
  fi
fi
# The chromium the preparation found is exported to the run, with the sandbox switches only this disposable container may use; browser.env is
# read inside the container, so the run launches the very executable the preflight launched.
# shellcheck disable=SC2016 # the script runs inside the container
wrap='if [ -f /work/prep/browser.env ]; then set -a; . /work/prep/browser.env; set +a; fi; exec "$@"'
if (( QC1 )); then
  docker exec -e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold "$CID" \
    bash -c "$wrap" _ bash deploy/digitalocean/qc1-measure.sh --dir /work/out "${drill_args[@]}" || rc=$?
else
  docker exec -e FOLD_DRILL_ISOLATED_DAEMON=1 -e QC1_NO_SANDBOX=1 -e QC1_WEB_DIR=/work/fold/web -w /work/fold "$CID" \
    bash -c "$wrap" _ bash deploy/digitalocean/restore-drill.sh --dir /work/out "${drill_args[@]}" || rc=$?
fi
docker cp "$CID:/work/prep/." "$OUT/prep/" >/dev/null 2>&1 || true
docker cp "$CID:/work/out/." "$OUT/" >/dev/null 2>&1 || echo 'restore-drill-nested: no evidence directory to copy out' >&2
echo "restore-drill-nested: $what exit=$rc evidence=$OUT"
exit "$rc"
