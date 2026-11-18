#!/usr/bin/env bash
# Isolation checks for the disposable drills (restore-drill.sh, qc1-measure.sh).
# Sourced only. The restore drill runs LANE_BASE's and the landing's own
# provisioners and the shipped Compose stacks; those write /etc/cron.d and
# /etc/logrotate.d entries and publish ports 80 and 443, and a drill pointed at
# a real host would collide with, overwrite or later remove its configuration.
# So the drill runs only on a disposable host (a droplet created for the run)
# whose operator says so with FOLD_DRILL_ISOLATED_DAEMON=1, and it refuses
# everything else here, before it creates anything: a remote or non-default
# daemon, a daemon that holds any container, a daemon not named for this host,
# a Docker socket or owner path mounted in, and any file the provisioners would
# write. qc1-measure.sh writes nothing to the host and uses only Docker objects
# it creates, so it takes the same daemon and socket checks without the path list.
#
# The pure functions take a mountinfo file or a path list.
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

# Whether the mount table can be read and holds at least one line. Not `-s`: files under /proc report size 0 whatever they hold.
isolation_table_readable() { # file
  local line
  [[ -r "$1" ]] && IFS= read -r line <"$1" 2>/dev/null && [[ -n "$line" ]]
}

# Problems with a mountinfo file (one per line): a Docker socket that is a
# mount (so the daemon is the host's), or a WRITABLE mount of an owner path,
# whatever its mount root (a bind of a host directory has a root other than /;
# a whole host filesystem mounted at /etc has root / and is just as much the
# owner's). A mount table that cannot be read fails closed: it is a problem.
isolation_mount_problems() { # mountinfo-file [sockets]  (sockets: only the socket rule)
  local owners="${ISOLATION_OWNER_MOUNTS[*]}"
  [[ "${2:-}" != sockets ]] || owners=""
  if ! isolation_table_readable "$1"; then echo "the mount table $1 cannot be read, so no mount can be ruled out"; return 0; fi
  awk -v socks="${ISOLATION_SOCKETS[*]}" -v owners="$owners" '
    BEGIN { ns = split(socks, S, " "); no = split(owners, O, " ") }
    { root = $4; mp = $5; gsub(/\\040/, " ", mp); split($6, opts, ","); rw = 0; for (i in opts) if (opts[i] == "rw") rw = 1
      for (i = 1; i <= ns; i++) if (mp == S[i]) print "the Docker socket " mp " is a mount: the daemon would be the host\x27s"
      for (i = 1; i <= no; i++) if (mp == O[i] && rw) {
        if (root != "/") print "writable bind mount of " mp " (from " root ")"
        else print "writable mount of " mp " (a whole filesystem, mount root /)" } }' "$1" \
    || echo "the mount table $1 could not be parsed, so no mount can be ruled out"
}

# Problems with host paths that already exist, one per line.
isolation_preexisting_problems() { # paths...
  local p
  for p in "$@"; do
    if [[ -e "$p" || -L "$p" ]]; then echo "$p already exists; the drill would overwrite or later remove an owner's configuration, so it refuses"; fi
  done
}

# Problems with where a daemon is reached from, given `docker info --format
# '{{json .}}'` and this host's name: the default local daemon only (no DOCKER_HOST
# or DOCKER_CONTEXT pointing elsewhere), named for this host.
isolation_pin_local() {
  local context endpoint
  case "${DOCKER_HOST:-}" in ''|unix:///var/run/docker.sock) ;; *) echo 'isolation: DOCKER_HOST is not the local default' >&2; return 1 ;; esac
  [[ -z "${DOCKER_CONTEXT:-}" ]] || { echo 'isolation: DOCKER_CONTEXT must be unset' >&2; return 1; }
  context="$(docker context show 2>/dev/null)" || { echo 'isolation: cannot read the active Docker context' >&2; return 1; }
  endpoint="$(docker context inspect --format '{{.Endpoints.docker.Host}}' "$context" 2>/dev/null)" || { echo 'isolation: cannot inspect the Docker endpoint' >&2; return 1; }
  [[ "$endpoint" == unix:///var/run/docker.sock ]] || { echo 'isolation: the active Docker context is not the local default socket' >&2; return 1; }
  export DOCKER_HOST=unix:///var/run/docker.sock
  export BUILDX_BUILDER=default
}

isolation_daemon_scope_problems() { # info-json hostname
  case "${DOCKER_HOST:-}" in ''|unix:///var/run/docker.sock) ;; *) echo "DOCKER_HOST points elsewhere: ${DOCKER_HOST}" ;; esac
  [[ -z "${DOCKER_CONTEXT:-}" ]] || echo "DOCKER_CONTEXT=${DOCKER_CONTEXT} may name a daemon that is not this host's"
  jq -r --arg host "$2" '
    if (.Name // "") != $host then "the daemon is named \(.Name), not this host \($host): it is not a daemon of this filesystem" else empty end' <<<"$1"
}

# The same, and the daemon must hold no container yet (a stopped one counts).
isolation_daemon_problems() { # info-json hostname
  isolation_daemon_scope_problems "$1" "$2"
  jq -r 'if ((.Containers // 0) + (.ContainersRunning // 0)) != 0 then "the daemon already holds container(s): \(.Containers // 0) in all, \(.ContainersRunning // 0) running" else empty end' <<<"$1"
}

# The whole guard for the restore drill. Everything it needs is passed or
# default, so a fixture can drive it; it prints problems to stderr and fails
# when there is one, before the drill touches anything.
isolation_require_disposable() { # [mountinfo] [hostname] [docker-info-json] [paths...]
  local mountinfo="${1:-/proc/self/mountinfo}" host="${2:-$(hostname)}" info="${3:-}" problems=""
  shift 3 2>/dev/null || shift $#
  local -a paths=("$@")
  (( ${#paths[@]} )) || paths=("${ISOLATION_HOST_PATHS[@]}")
  [[ "${FOLD_DRILL_ISOLATED_DAEMON:-}" == 1 ]] || problems+=$'this host is not declared disposable: on a droplet created only for this run, set FOLD_DRILL_ISOLATED_DAEMON=1\n'
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

# The file's ID and VERSION_ID as "id version" ("" "" when unreadable). Sourced in a subshell.
isolation_os_identity() { # path
  # shellcheck disable=SC1090 # the host's own os-release
  ( ID=""; VERSION_ID=""; [[ -r "$1" ]] && . "$1" 2>/dev/null; printf '%s %s\n' "${ID:-}" "${VERSION_ID:-}" )
}
# Whether provision.sh would accept the identity without --allow-os.
isolation_os_supported() { # id version
  [[ "$1" == ubuntu && ( "$2" == 24.04 || "$2" == 26.04 ) ]]
}

# Whether the provisioner at PATH (a tool copy) accepts the identity now in /etc/os-release without --allow-os: runs ITS OWN check_os function, extracted
# from the file, and requires the line it prints for a supported host. The real-binary crossings run every revision's provisioner on this host's real OS.
isolation_check_os_accepts() { # provision.sh-path [os-release-path]
  local body osfile="${2:-/etc/os-release}"
  body="$(awk '/^check_os\(\) \{/{on=1} on{print} on && /^}$/{exit}' "$1")"
  [[ -n "$body" ]] || { echo "isolation: $1 has no check_os function to check the host against" >&2; return 1; }
  body="${body//\/etc\/os-release/$osfile}"
  bash -c 'DRY_RUN=0; ALLOW_OS=0; die() { echo "$*" >&2; exit 1; }; eval "$1"; check_os' _ "$body" 2>&1 | grep '^os: Ubuntu ' >/dev/null
}
