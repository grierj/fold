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

# The mount that covers PATH (an existing, canonical path): the longest mount point that is PATH or a parent of it, the last one listed when several
# share it (a later mount shadows an earlier one). Prints "mount-point rw|ro", nothing when none covers it or the table cannot be read.
isolation_covering_mount() { # mountinfo path
  isolation_table_readable "$1" || return 1
  # Refuse names whose mountinfo escaping cannot be compared safely by awk.
  [[ "$2" != *$'\t'* && "$2" != *$'\n'* && "$2" != *"\\"* ]] || return 1
  awk '$5 ~ /\\(011|012|134)/ { bad = 1 } END { exit bad }' "$1" || return 1
  awk -v p="$2" '
    { mp = $5; gsub(/\\040/, " ", mp); split($6, opts, ","); rw = "ro"; for (i in opts) if (opts[i] == "rw") rw = "rw"
      if (mp == "/" || p == mp || index(p, mp "/") == 1) if (length(mp) >= best) { best = length(mp); out = mp " " rw } }
    END { if (out == "") exit 1; print out }' "$1"
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

# ---------------------------------------------------------------------------
# The nested runner is Alpine. provision.sh's check_os sources /etc/os-release and
# accepts only Ubuntu 24.04 or 26.04 unless it is given --allow-os, and
# upgrade.sh's run_provision builds the nested provisioner command itself and never
# forwards that flag (nor do the older revisions' copies the crossing cases run).
# Rather than change a shipped script or rely on a flag only some entry points honour,
# the disposable container presents the supported OS identity for the part of the run
# that needs it: a container-local /etc/os-release, written only after the guard
# above has passed in this process, saved first, restored on exit, and recorded in
# a state file that is part of the drill's evidence (a deliberate, recorded
# deviation; nothing outside the container's own filesystem is touched).
# ---------------------------------------------------------------------------
ISOLATION_OS_STANDIN_ID=ubuntu
ISOLATION_OS_STANDIN_VERSION=24.04

# The file's ID and VERSION_ID as "id version" ("" "" when unreadable). Sourced in a subshell: it is the container's own file.
isolation_os_identity() { # path
  # shellcheck disable=SC1090 # the container's own os-release
  ( ID=""; VERSION_ID=""; [[ -r "$1" ]] && . "$1" 2>/dev/null; printf '%s %s\n' "${ID:-}" "${VERSION_ID:-}" )
}
# Whether provision.sh would accept the identity without --allow-os.
isolation_os_supported() { # id version
  [[ "$1" == ubuntu && ( "$2" == 24.04 || "$2" == 26.04 ) ]]
}

# Every function below runs in `||` and `if` contexts (restore-drill-crossings.sh calls the writer as `isolation_os_standin ... || return 1`, and the
# EXIT trap calls the restorer as `... || rc=1`), where bash switches errexit OFF inside the function. So no step here relies on `set -e`: every cp, mv,
# jq, mktemp, cat, chmod, rm, ln and stat is checked on its own line, and a failure returns nonzero with the filesystem either untouched or restorable.
# isolation-fixtures.sh runs each of them with a failing command in exactly that context.
isolation_os_fail() { echo "isolation: $*" >&2; return 1; }
# Removes the named files that were created by a stand-in that did not get as far as replacing anything (empty names are skipped).
isolation_os_discard() { local f; for f in "$@"; do if [[ -n "$f" ]]; then rm -f -- "$f"; fi; done; return 0; }

# Whether STATE_FILE is a complete record: applied (with everything restore needs: for a file its backup, SHA-256 and mode; for a link its target) or not.
isolation_os_state_valid() { # state-file
  jq -se '
    length == 1 and (.[0] | type == "object" and (.applied | type == "boolean") and (.path | type == "string" and length > 0)
    and (if .applied then
           (.restored | type == "boolean") and (.original.kind | IN("link", "file", "absent"))
           and (if .original.kind == "link" then (.original.linkTarget | type == "string" and length > 0) else true end)
           and (if .original.kind == "file" then
                  (.original.backup | type == "string" and length > 0) and (.original.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and (.original.mode | type == "string" and length > 0)
                else true end)
         else .restored == null end))' "$1" >/dev/null 2>&1
}

# Problems that forbid presenting a stand-in at PATH (one per line; empty when none): not the isolated daemon, not a container, a Docker socket or a
# writable owner mount (any mount root), an unreadable mount table, a PATH that is itself a mount point, or a destination directory that does not
# resolve to the container's own root filesystem. The destination is the CANONICAL parent of PATH: a symlinked parent (/etc -> /mnt/x) is followed, and
# the mount covering where it lands decides, so a link into a writable mount, owner path or not, is refused as the mount it lands on.
isolation_os_problems() { # path mountinfo
  local path="$1" mountinfo="$2" problems="" parent canon real cover mp rc
  [[ "${FOLD_DRILL_ISOLATED_DAEMON:-}" == 1 ]] || problems+=$'FOLD_DRILL_ISOLATED_DAEMON is not 1\n'
  [[ -e /.dockerenv || "${FOLD_DRILL_ASSUME_CONTAINER:-}" == 1 ]] || problems+=$'this is not a container filesystem (no /.dockerenv)\n'
  problems+="$(isolation_mount_problems "$mountinfo")"$'\n'
  parent="$(dirname -- "$path")" || parent=""
  canon="$(readlink -f -- "$parent" 2>/dev/null)" || canon=""
  if [[ -z "$canon" || ! -d "$canon" ]]; then
    problems+="the directory of $path does not resolve to a directory"$'\n'
  else
    if cover="$(isolation_covering_mount "$mountinfo" "$canon")"; then
      mp="${cover% *}"
      [[ "$mp" == / ]] || problems+="the directory $canon (where $path lands) is on the mount $mp (${cover##* }), not on the container's own root filesystem"$'\n'
    else
      problems+="no mount in $mountinfo covers $canon, so where $path would be written cannot be told"$'\n'
    fi
  fi
  real="$(readlink -f -- "$path" 2>/dev/null)" || real=""
  rc=0; awk -v a="$path" -v b="$real" -v c="${canon:+$canon/${path##*/}}" '{ mp = $5; gsub(/\\040/, " ", mp) } mp == a || (b != "" && mp == b) || (c != "" && mp == c) { found = 1 } END { exit !found }' "$mountinfo" 2>/dev/null || rc=$?
  case "$rc" in 0) problems+="$path is a mount point (it could be an owner's file)"$'\n' ;; 1) ;; *) problems+="the mount table $mountinfo could not be read to check $path"$'\n' ;; esac
  printf '%s' "$problems" | sed '/^$/d'
}

# isolation_os_standin STATE_FILE [os-release-path] [mountinfo]
# Refuses (nonzero, the reasons on stderr, NOTHING written) unless isolation_os_problems finds none. Then, in this order, each step checked:
#   1. the original is saved and the copy verified (a file: copy, byte comparison, SHA-256 and mode; a link: its target; absent: noted);
#   2. the COMPLETE restoration record is built, validated and published atomically (temp file, then rename), so STATE_FILE is either absent or whole;
#   3. only then is the stand-in written beside PATH, checked, and renamed over PATH (a link is replaced, never written through), and read back.
# Any failure after step 2 puts the original back at once and returns nonzero; a failure before it changes nothing. STATE_FILE must not exist (not even
# empty): the stand-in is applied once. applied=false is recorded when the file already names a supported Ubuntu.
isolation_os_standin() {
  local state="$1" path="${2:-/etc/os-release}" mountinfo="${3:-/proc/self/mountinfo}"
  local problems oid over kind="" target="" backup="" sha="" mode="" doc stmp tmp
  [[ -n "$state" ]] || { echo 'isolation: os-release stand-in needs a state file' >&2; return 2; }
  problems="$(isolation_os_problems "$path" "$mountinfo")" || { isolation_os_fail 'could not check the stand-in preconditions'; return 1; }
  if [[ -n "$problems" ]]; then
    printf 'isolation: refusing to present an Ubuntu os-release:\n' >&2
    printf '%s\n' "$problems" | while IFS= read -r line; do printf '  - %s\n' "$line" >&2; done
    return 1
  fi
  if [[ -e "$state" || -L "$state" ]]; then isolation_os_fail "$state already exists (even empty or malformed); the stand-in is applied once and a leftover record is never trusted"; return 1; fi
  stmp="$state.new.$$"
  oid="" over=""
  read -r oid over <<<"$(isolation_os_identity "$path")" || { isolation_os_fail "cannot read the identity of $path"; return 1; }
  if isolation_os_supported "${oid:-}" "${over:-}"; then
    doc="$(jq -n --arg path "$path" --arg id "${oid:-}" --arg version "${over:-}" \
      '{applied:false, path:$path, reason:"the file already names a supported Ubuntu", original:{id:$id, versionId:$version}, restored:null}')" \
      || { isolation_os_fail 'cannot build the state record'; return 1; }
    printf '%s\n' "$doc" >"$stmp" || { isolation_os_discard "$stmp" "$backup"; isolation_os_fail "cannot write $stmp"; return 1; }
    isolation_os_state_valid "$stmp" || { isolation_os_discard "$stmp" "$backup"; isolation_os_fail 'the state record is not valid'; return 1; }
    mv -f -- "$stmp" "$state" || { isolation_os_discard "$stmp" "$backup"; isolation_os_fail "cannot publish $state"; return 1; }
    return 0
  fi
  # 1. save the original and prove the copy
  if [[ -L "$path" ]]; then
    kind="link"; target="$(readlink -- "$path")" || { isolation_os_fail "cannot read the link $path"; return 1; }
    [[ -n "$target" ]] || { isolation_os_fail "the link $path has no target"; return 1; }
  elif [[ -f "$path" ]]; then
    kind="file"; backup="$state.orig"; tmp="$backup.new.$$"
    cp -p -- "$path" "$tmp" || { rm -f -- "$tmp"; isolation_os_fail "cannot back up $path"; return 1; }
    cmp -s -- "$path" "$tmp" || { rm -f -- "$tmp"; isolation_os_fail "the backup of $path differs from it"; return 1; }
    sha="$(sha256sum <"$path")" || { rm -f -- "$tmp"; isolation_os_fail "cannot hash $path"; return 1; }
    sha="${sha%% *}"
    mode="$(stat -c %a -- "$path")" || { rm -f -- "$tmp"; isolation_os_fail "cannot read the mode of $path"; return 1; }
    [[ -e "$backup" || -L "$backup" ]] && { rm -f -- "$tmp"; isolation_os_fail "$backup already exists"; return 1; }
    mv -f -- "$tmp" "$backup" || { isolation_os_discard "$tmp"; isolation_os_fail "cannot keep the backup $backup"; return 1; }
    cmp -s -- "$path" "$backup" || { isolation_os_discard "$backup"; isolation_os_fail "the kept backup $backup differs from $path"; return 1; }
  elif [[ ! -e "$path" ]]; then
    kind="absent"
  else
    isolation_os_fail "$path is not a regular file, a link or absent"; return 1
  fi
  # 2. the complete record, published atomically, before anything is replaced
  doc="$(jq -n --arg path "$path" --arg kind "$kind" --arg target "$target" --arg backup "$backup" --arg sha "$sha" --arg mode "$mode" --arg id "${oid:-}" --arg version "${over:-}" \
    --arg standinId "$ISOLATION_OS_STANDIN_ID" --arg standinVersion "$ISOLATION_OS_STANDIN_VERSION" \
    '{applied:true, path:$path, reason:"provision.sh check_os accepts only Ubuntu 24.04 or 26.04 and upgrade.sh never forwards --allow-os; this disposable Alpine container presents a container-local Ubuntu identity for the run",
      original:{kind:$kind, linkTarget:$target, backup:$backup, sha256:$sha, mode:$mode, id:$id, versionId:$version},
      standIn:{id:$standinId, versionId:$standinVersion}, restored:false}')" || { isolation_os_discard "$backup"; isolation_os_fail 'cannot build the restoration record'; return 1; }
  printf '%s\n' "$doc" >"$stmp" || { isolation_os_discard "$stmp" "$backup"; isolation_os_fail "cannot write $stmp"; return 1; }
  isolation_os_state_valid "$stmp" || { isolation_os_discard "$stmp" "$backup"; isolation_os_fail 'the restoration record is incomplete'; return 1; }
  mv -f -- "$stmp" "$state" || { isolation_os_discard "$stmp" "$backup"; isolation_os_fail "cannot publish $state"; return 1; }
  isolation_os_state_valid "$state" || { isolation_os_discard "$state" "$backup"; isolation_os_fail "$state is not valid after publishing"; return 1; }
  # 3. the stand-in, beside PATH, then renamed over it
  tmp="$(mktemp "$path.standin.XXXXXX")" || { isolation_os_restore "$state" >/dev/null 2>&1; isolation_os_fail "cannot create a temporary file beside $path"; return 1; }
  if ! cat >"$tmp" <<OSR
ID=$ISOLATION_OS_STANDIN_ID
VERSION_ID="$ISOLATION_OS_STANDIN_VERSION"
VERSION_CODENAME=noble
UBUNTU_CODENAME=noble
PRETTY_NAME="Ubuntu $ISOLATION_OS_STANDIN_VERSION (Fold drill stand-in inside a disposable container; restored on exit)"
OSR
  then rm -f -- "$tmp"; isolation_os_restore "$state" >/dev/null 2>&1; isolation_os_fail "cannot write the stand-in $tmp"; return 1; fi
  chmod 644 "$tmp" || { rm -f -- "$tmp"; isolation_os_restore "$state" >/dev/null 2>&1; isolation_os_fail "cannot set the mode of $tmp"; return 1; }
  read -r oid over <<<"$(isolation_os_identity "$tmp")" || oid=""
  isolation_os_supported "${oid:-}" "${over:-}" || { rm -f -- "$tmp"; isolation_os_restore "$state" >/dev/null 2>&1; isolation_os_fail "the stand-in file does not name a supported Ubuntu"; return 1; }
  mv -f -- "$tmp" "$path" || { rm -f -- "$tmp"; isolation_os_restore "$state" >/dev/null 2>&1; isolation_os_fail "cannot replace $path"; return 1; }
  read -r oid over <<<"$(isolation_os_identity "$path")" || oid=""
  isolation_os_supported "${oid:-}" "${over:-}" || { isolation_os_restore "$state" >/dev/null 2>&1; isolation_os_fail "the os-release stand-in did not take effect"; return 1; }
  return 0
}

# isolation_os_restore STATE_FILE: puts the original back, proves it, and records that in the state file. No state file: nothing was applied (0). An existing
# file that is empty, malformed or incomplete is NOT trusted and fails (nonzero) without touching PATH. applied=false or already restored: nothing to do (0).
# Idempotent and retryable: when only recording the restoration failed, a second call finds the original in place, proves it again and records it.
isolation_os_restore() {
  local state="$1" applied restored path kind target backup sha mode got tmp
  [[ -e "$state" || -L "$state" ]] || return 0
  [[ -s "$state" ]] || { isolation_os_fail "$state is empty: the original os-release cannot be proved restored"; return 1; }
  isolation_os_state_valid "$state" || { isolation_os_fail "$state is malformed or incomplete: the original os-release cannot be proved restored"; return 1; }
  applied="$(jq -r '.applied' "$state")" || return 1
  [[ "$applied" == true ]] || return 0
  restored="$(jq -r '.restored' "$state")" || return 1
  [[ "$restored" == false ]] || return 0
  path="$(jq -r '.path' "$state")" || return 1; kind="$(jq -r '.original.kind' "$state")" || return 1
  target="$(jq -r '.original.linkTarget' "$state")" || return 1; backup="$(jq -r '.original.backup' "$state")" || return 1
  sha="$(jq -r '.original.sha256' "$state")" || return 1; mode="$(jq -r '.original.mode' "$state")" || return 1
  tmp="$path.restore.$$"
  case "$kind" in
    link)
      ln -s -- "$target" "$tmp" || { isolation_os_fail "cannot recreate the link $path"; return 1; }
      mv -f -- "$tmp" "$path" || { rm -f -- "$tmp"; isolation_os_fail "cannot put the link back at $path"; return 1; }
      [[ -L "$path" && "$(readlink -- "$path")" == "$target" ]] || { isolation_os_fail "$path is not the original link"; return 1; } ;;
    file)
      got="$(sha256sum <"$backup")" || { isolation_os_fail "cannot hash the backup $backup"; return 1; }
      [[ "${got%% *}" == "$sha" ]] || { isolation_os_fail "the backup $backup no longer matches the recorded SHA-256"; return 1; }
      cp -p -- "$backup" "$tmp" || { rm -f -- "$tmp"; isolation_os_fail "cannot copy the backup to $tmp"; return 1; }
      cmp -s -- "$backup" "$tmp" || { rm -f -- "$tmp"; isolation_os_fail "the restored copy differs from the backup"; return 1; }
      mv -f -- "$tmp" "$path" || { rm -f -- "$tmp"; isolation_os_fail "cannot put the original back at $path"; return 1; }
      got="$(sha256sum <"$path")" || { isolation_os_fail "cannot hash $path"; return 1; }
      [[ ! -L "$path" && "${got%% *}" == "$sha" && "$(stat -c %a -- "$path")" == "$mode" ]] || { isolation_os_fail "$path is not the original file"; return 1; } ;;
    absent)
      rm -f -- "$path" || { isolation_os_fail "cannot remove $path"; return 1; }
      [[ ! -e "$path" && ! -L "$path" ]] || { isolation_os_fail "$path is still there"; return 1; } ;;
    *) isolation_os_fail "unknown original kind $kind"; return 1 ;;
  esac
  jq '.restored = true' "$state" >"$state.rec.$$" || { rm -f -- "$state.rec.$$"; isolation_os_fail "cannot record the restoration in $state"; return 1; }
  isolation_os_state_valid "$state.rec.$$" || { rm -f -- "$state.rec.$$"; isolation_os_fail "the restoration record is not valid"; return 1; }
  mv -f -- "$state.rec.$$" "$state" || { rm -f -- "$state.rec.$$"; isolation_os_fail "cannot publish the restoration record in $state"; return 1; }
  return 0
}

# Whether the provisioner at PATH (a tool copy) accepts the identity now in /etc/os-release without --allow-os: runs ITS OWN check_os function, extracted
# from the file, and requires the line it prints for a supported host. Used as a precondition before any deploy that relies on the stand-in.
isolation_check_os_accepts() { # provision.sh-path [os-release-path]
  local body osfile="${2:-/etc/os-release}"
  body="$(awk '/^check_os\(\) \{/{on=1} on{print} on && /^}$/{exit}' "$1")"
  [[ -n "$body" ]] || { echo "isolation: $1 has no check_os function to prove the stand-in against" >&2; return 1; }
  body="${body//\/etc\/os-release/$osfile}"
  bash -c 'DRY_RUN=0; ALLOW_OS=0; die() { echo "$*" >&2; exit 1; }; eval "$1"; check_os' _ "$body" 2>&1 | grep '^os: Ubuntu ' >/dev/null
}
