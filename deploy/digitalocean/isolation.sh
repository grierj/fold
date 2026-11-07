#!/usr/bin/env bash
# Isolation checks for the disposable drills (restore-drill.sh, qc1-measure.sh).
# Sourced only. The restore drill runs LANE_BASE's and the landing's own
# provisioners and the shipped Compose stacks; those write /etc/cron.d and
# /etc/logrotate.d entries and publish ports 80 and 443, and a drill pointed at
# a real host would collide with, overwrite or later remove its configuration.
# So the drill runs only inside a throwaway container with its own Docker
# daemon (restore-drill-nested.sh), and refuses everything else here, before it
# creates anything. qc1-measure.sh writes nothing to the host and uses only
# Docker objects it creates, so it takes the daemon and socket checks.
#
# The pure functions take a mountinfo file or a path list and are exercised by
# isolation-fixtures.sh without Docker.
# shellcheck disable=SC2034
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then echo 'source from a drill only' >&2; exit 1; fi

# Host paths the drill's provisioners write (or would take over). None may exist
# when a drill starts: the drill preserves nothing it did not create, so it
# refuses instead of risking an owner's file.
ISOLATION_HOST_PATHS=(
  /opt/fold
  /etc/cron.d/fold-health /etc/cron.d/fold-backup /etc/logrotate.d/fold-backup
  /etc/sudoers.d/fold-admin /etc/ssh/sshd_config.d/00-fold-hardening.conf
  /etc/fail2ban/jail.d/fold-sshd.local
  /etc/apt/apt.conf.d/20auto-upgrades /etc/apt/apt.conf.d/52fold-unattended
)
# Mount points an owner keeps: a writable bind mount of any of them into the
# drill's filesystem means the drill could write to the owner's tree.
ISOLATION_OWNER_MOUNTS=(/etc /etc/cron.d /etc/logrotate.d /opt /opt/fold /root /home /srv /var/spool/cron)
# The sockets a host daemon is reached through.
ISOLATION_SOCKETS=(/var/run/docker.sock /run/docker.sock)

# Problems with a mountinfo file (one per line): a Docker socket that is a
# mount (so the daemon is the host's), or a writable bind mount (mount root
# other than /) of an owner path.
isolation_mount_problems() { # mountinfo-file [sockets]  (sockets: only the socket rule)
  local owners="${ISOLATION_OWNER_MOUNTS[*]}"
  [[ "${2:-}" != sockets ]] || owners=""
  awk -v socks="${ISOLATION_SOCKETS[*]}" -v owners="$owners" '
    BEGIN { ns = split(socks, S, " "); no = split(owners, O, " ") }
    { root = $4; mp = $5; split($6, opts, ","); rw = 0; for (i in opts) if (opts[i] == "rw") rw = 1
      for (i = 1; i <= ns; i++) if (mp == S[i]) print "the Docker socket " mp " is a mount: the daemon would be the host\x27s"
      for (i = 1; i <= no; i++) if (mp == O[i] && root != "/" && rw) print "writable bind mount of " mp " (from " root ")" }' "$1"
}

# Problems with host paths that already exist, one per line.
isolation_preexisting_problems() { # paths...
  local p
  for p in "$@"; do
    if [[ -e "$p" || -L "$p" ]]; then echo "$p already exists; the drill would overwrite or later remove an owner's configuration, so it refuses"; fi
  done
}

# Problems with a daemon described by `docker info --format '{{json .}}'` and the
# hostname of the place the drill runs: the daemon must run nothing yet and be
# named for this host (a daemon reached through a host socket carries the host's
# name, a nested one carries its container's).
isolation_daemon_problems() { # info-json hostname
  jq -r --arg host "$2" '
    ( if (.ContainersRunning // 0) != 0 then "the daemon already runs \(.ContainersRunning) container(s)" else empty end ),
    ( if (.Name // "") != $host then "the daemon is named \(.Name), not this host \($host): it is not a daemon of this filesystem" else empty end )' <<<"$1"
}

# The whole guard for the restore drill. Everything it needs is passed or
# default, so a fixture can drive it; it prints problems to stderr and fails
# when there is one, before the drill touches anything.
isolation_require_disposable() { # [mountinfo] [hostname] [docker-info-json] [paths...]
  local mountinfo="${1:-/proc/self/mountinfo}" host="${2:-$(hostname)}" info="${3:-}" problems=""
  shift 3 2>/dev/null || shift $#
  local -a paths=("$@")
  (( ${#paths[@]} )) || paths=("${ISOLATION_HOST_PATHS[@]}")
  [[ "${FOLD_DRILL_ISOLATED_DAEMON:-}" == 1 ]] || problems+=$'run the drill through restore-drill-nested.sh: it needs a throwaway container with its own Docker daemon (FOLD_DRILL_ISOLATED_DAEMON=1)\n'
  [[ -e /.dockerenv || "${FOLD_DRILL_ASSUME_CONTAINER:-}" == 1 ]] || problems+=$'this is not a container filesystem (no /.dockerenv)\n'
  case "${DOCKER_HOST:-}" in ''|unix:///var/run/docker.sock) ;; *) problems+="DOCKER_HOST points elsewhere: ${DOCKER_HOST}"$'\n' ;; esac
  problems+="$(isolation_mount_problems "$mountinfo")"$'\n'
  if [[ -z "$info" ]]; then info="$(docker info --format '{{json .}}' 2>/dev/null)" || problems+=$'cannot reach the Docker daemon\n'; fi
  [[ -z "$info" ]] || problems+="$(isolation_daemon_problems "$info" "$host")"$'\n'
  problems+="$(isolation_preexisting_problems "${paths[@]}")"$'\n'
  problems="$(printf '%s' "$problems" | sed '/^$/d')"
  if [[ -n "$problems" ]]; then
    # shellcheck disable=SC2001 # a per-line prefix, not a substitution
    printf 'isolation: refusing to run:\n%s\n' "$(sed 's/^/  - /' <<<"$problems")" >&2
    return 1
  fi
}

# A per-run name part for Docker and Compose objects: no two runs, and no run
# and an owner's stack, can share a project name.
isolation_run_id() { od -An -N6 -tx1 /dev/urandom | tr -d ' \n'; }
