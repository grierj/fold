#!/usr/bin/env bash
# Fixtures for the host archive section of checks.sh (volume resolution, layout,
# adoption, retention and its pending decisions, exports, settings, identity), for
# host backup.sh and for the real cutover. They run as root against a fake Docker
# root, a fake `docker` on PATH and real files, so they run inside the
# provision-dry-run container (dry-run-test.sh --fixtures). Commands are stubbed
# through PATH only; the shipped scripts have no test hook. The cutover is
# provision.sh's own functions, cut out of the file by name and run unchanged in a
# strict shell (the one thing redirected is the cron file's directory). Crash cases
# kill the script under test at the Nth call of rm, mv or sync and require a rerun
# to converge.
# Each case runs in its own subshell on purpose.
#
# These replace the image's removed adopt cases (backup-test.sh): adopt's copy,
# retry, differing-file and killed-copy cases are `adoption` and `adoption_faults`;
# its flush-order and failed-flush cases are the synchronization steps of
# `adoption_faults`. New here: a stamp is refused as a whole, and an equal-bytes
# copy with another mtime is repaired (the old shortcut compared content only).
# The single-quoted programs are for the shells they start, names come from ls
# on folders this script made, and a failed check exits through fail().
# shellcheck disable=SC2030,SC2031,SC2016,SC2012,SC2015
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHECKS="$ROOT/deploy/digitalocean/checks.sh"
if [[ "$(id -u)" != 0 ]]; then
  echo "backup-archives-fixtures.sh needs root (it runs in the provision-dry-run container)" >&2
  exit 2
fi
for tool in jq flock setpriv timeout sha256sum; do
  command -v "$tool" >/dev/null || { echo "backup-archives-fixtures.sh: $tool is required" >&2; exit 2; }
done
ORIG_PATH="$PATH"
# A directory the fake Docker root can live under: root-owned ancestors only.
BASE="$(mktemp -d /var/lib/fold-archives.XXXXXX)"
chmod 755 "$BASE"
trap 'rm -rf -- "$BASE"' EXIT
# shellcheck source=deploy/digitalocean/checks.sh
source "$CHECKS"
# shellcheck source=deploy/digitalocean/bucket-model.sh
source "$ROOT/deploy/digitalocean/bucket-model.sh"
BUCKET_MODEL="$ROOT/deploy/digitalocean/bucket-model.sh"
REAL_OD="$(command -v od)"
export BUCKET_MODEL REAL_OD
ID1=00112233445566778899aabbccddeeff
ID2=ffeeddccbbaa99887766554433221100
ID0=0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f
FID=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

NOW=1760000000
DAY=86400

fail() {
  echo "    FAIL: $*" >&2
  [[ ! -s "${FX:-/nonexistent}/out" ]] || { echo "    last output:" >&2; tail -n 12 "$FX/out" | sed 's/^/      /' >&2; }
  exit 1
}
eq() { [[ "$1" == "$2" ]] || fail "$3: got '$1', want '$2'"; }

# ------------------------------------------------------------------ harness

write_stubs() {
  cat >"$FX/bin/docker" <<'EOF'
#!/bin/bash
m="$FX_MODEL"
printf 'docker %s\n' "$*" >>"$m/log"
# slow N stalls every call (or, with slow-for WORD, every call naming WORD and, with
# slow-after K, only from its K+1th such call) for N seconds.
if [[ -e "$m/slow" ]]; then
  stall=1
  if [[ -e "$m/slow-for" ]]; then
    case " $* " in *" $(cat "$m/slow-for") "*) k=$(( $(cat "$m/slow.n" 2>/dev/null || echo 0) + 1 )); echo "$k" >"$m/slow.n"
      (( k > $(cat "$m/slow-after" 2>/dev/null || echo 0) )) || stall=0 ;; *) stall=0 ;; esac
  fi
  (( ! stall )) || sleep "$(cat "$m/slow")"
fi
case "${1:-}" in
  info) [[ ! -e "$m/fail-info" ]] || exit 1; echo "$FX_DOCKER_ROOT"; exit 0 ;;
  volume)
    case "${2:-}" in
      ls) [[ ! -e "$m/fail-ls" ]] || exit 1; [[ -e "$m/volume-absent" ]] || echo "$FX_VOLUME"
          [[ ! -e "$m/uploads-listed" ]] || echo fold_uploads; exit 0 ;;
      inspect)
        name="${*: -1}"
        case "$name" in *_uploads) [[ ! -e "$m/uploads-absent" ]] || exit 1; echo '{}'; exit 0 ;; esac
        mp="$FX_DOCKER_ROOT/volumes/$name/_data"
        [[ ! -e "$m/mountpoint-elsewhere" ]] || mp=/var/lib/elsewhere/_data
        opts=null; [[ ! -e "$m/volume-options" ]] || opts='{"type":"none","o":"bind","device":"/x"}'
        proj=fold; [[ ! -e "$m/foreign-labels" ]] || proj=other
        jq -cn --arg n "$name" --arg mp "$mp" --arg p "$proj" --argjson o "$opts" \
          '{Name:$n,Driver:"local",Options:$o,Labels:{"com.docker.compose.project":$p,"com.docker.compose.volume":"backups"},Mountpoint:$mp}'
        exit 0 ;;
    esac ;;
  compose)
    shift
    [[ "${1:-}" != --project-directory ]] || shift 2
    case "${1:-}" in
      config) [[ ! -e "$m/fail-config" ]] || exit 1; cat "$m/config.json"; exit 0 ;;
      exec)
        case "${4:-}" in
          cat) cat "$m/lastok" ;;
          pg_dump) cat "$m/dump-bytes" ;;
          /usr/local/bin/backup.sh) exit 0 ;;
          *) exit 1 ;;
        esac
        exit 0 ;;
      ps) [[ ! -e "$m/fail-ps" ]] || exit 1; [[ ! -e "$m/started" ]] || echo fxbackup; exit 0 ;;
      stop) [[ ! -e "$m/fail-stop" ]] || exit 1; rm -f "$m/started"; exit 0 ;;
      up)
        for a in "$@"; do
          if [[ "$a" == --no-start ]]; then # Compose creating the volume from the image: root:65532 1770
            [[ ! -e "$m/fail-create" ]] || exit 1
            mkdir -p "$FX_DOCKER_ROOT/volumes/fold_backups/_data"
            chmod 755 "$FX_DOCKER_ROOT/volumes/fold_backups"
            chown 0:65532 "$FX_DOCKER_ROOT/volumes/fold_backups/_data"; chmod 1770 "$FX_DOCKER_ROOT/volumes/fold_backups/_data"
            rm -f "$m/volume-absent"; exit 0
          fi
        done
        # Compose starts the named services, and with no --no-deps whatever they depend on.
        named=() no_deps=0 skip=0
        for a in "${@:2}"; do
          if (( skip )); then skip=0; continue; fi
          case "$a" in --no-deps) no_deps=1 ;; --pull) skip=1 ;; -*) ;; *) named+=("$a") ;; esac
        done
        wanted=0
        for a in "${named[@]}"; do
          [[ "$a" != backup ]] || wanted=1
          if (( ! no_deps )) && jq -e --arg s "$a" '(.services[$s].depends_on // {}) | has("backup")' "$m/config.json" >/dev/null; then wanted=1; fi
        done
        if (( ! wanted )); then echo "${named[*]}" >>"$m/up-without-backup"; exit 0; fi
        [[ ! -e "$m/fail-start" ]] || exit 1
        ls -A "$FX_DOCKER_ROOT/volumes/fold_backups/_data/adopted" >"$m/adopted-at-start" 2>&1 || true
        : >"$m/started"
        [[ ! -e "$m/start-lastok" ]] || /usr/bin/date -u -d "@$(( $(cat "$m/now") + 10 ))" +%Y-%m-%dT%H:%M:%SZ >"$m/lastok"
        exit 0 ;;
    esac ;;
  inspect) echo true; exit 0 ;;
  run)
    # The offsite upload (amazon/aws-cli s3 cp) is one PUT invocation of the fake bucket
    # (bucket-model.sh), kept under its whole key. Any other call of that client lists,
    # reads or deletes, which a write-only key cannot do: it is recorded as forbidden.
    case " $* " in *" amazon/aws-cli "*)
      export BUCKET="$m/bucket"
      source "$BUCKET_MODEL"
      [[ " $* " == *" s3 cp "* ]] || bucket_forbidden_call "docker $*"
      src=""; key=""; bdir=""
      for a in "$@"; do
        case "$a" in *:/backups:ro) bdir="${a%:/backups:ro}" ;; /backups/*) src="${a#/backups/}" ;; s3://*) key="${a#s3://*/}" ;; esac
      done
      [[ -n "$bdir" && -n "$src" && -n "$key" ]] || bucket_forbidden_call "docker $*"
      bucket_put "${BUCKET_TAG:-host}" "$bdir/$src" "$key"
      exit $? ;;
    esac
    host=""
    for a in "$@"; do case "$a" in *:/backup) host="${a%:/backup}" ;; esac; done
    for a in "$@"; do case "$a" in /backup/*) printf 'UPLOADS' >"$host/${a#/backup/}" ;; esac; done
    exit 0 ;;
esac
echo "docker stub: $*" >&2
exit 1
EOF
  cat >"$FX/bin/date" <<'EOF'
#!/bin/bash
now="$(cat "$FX_MODEL/now" 2>/dev/null || true)"
if [[ -n "$now" ]]; then
  case "$*" in
    '+%s') echo "$now"; exit 0 ;;
    '+%Y%m%d-%H%M') exec /usr/bin/date -d "@$now" "$@" ;;
    '-u +%Y-%m-%dT%H:%M:%SZ') exec /usr/bin/date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ ;;
  esac
fi
exec /usr/bin/date "$@"
EOF
  # age: a stand-in that marks its output as ciphertext of its input.
  cat >"$FX/bin/age" <<'EOF'
#!/bin/bash
while [[ $# -gt 0 ]]; do case "$1" in -o) out="$2"; shift 2 ;; -r) shift 2 ;; *) in="$1"; shift ;; esac; done
{ printf 'AGE:'; cat "$in"; } >"$out"
EOF
  bucket_od_stub "$FX/bin/od"
  chmod +x "$FX/bin/docker" "$FX/bin/date" "$FX/bin/age"
}

# fx_setup NAME: a fresh deploy dir, originals folder and fake Docker root whose
# backups volume looks like one an older image created (65532:65532, mode 700).
fx_setup() {
  FX="$BASE/$1"
  rm -rf -- "$FX"
  mkdir -p "$FX"/{bin,model,dir,orig}
  chmod 755 "$FX"
  M="$FX/model"
  DOCKER_ROOT="$FX/docker"
  MOUNT="$DOCKER_ROOT/volumes/fold_backups/_data"
  AD="$MOUNT/adopted"
  mkdir -p "$MOUNT"
  chmod 755 "$DOCKER_ROOT" "$DOCKER_ROOT/volumes" "$DOCKER_ROOT/volumes/fold_backups"
  chown 65532:65532 "$MOUNT"
  chmod 700 "$MOUNT"
  DIR="$FX/dir"
  ORIG="$FX/orig"
  chmod 755 "$DIR"
  chmod 700 "$ORIG"
  printf 'COMPOSE_PROJECT_NAME=fold\nDOMAIN=groups.example.org\n' >"$DIR/.env"
  chmod 600 "$DIR/.env"
  : >"$DIR/backup.lock"
  chown "0:${BACKUP_COORD_GID}" "$DIR/backup.lock"
  chmod 640 "$DIR/backup.lock"
  export FX_MODEL="$M" FX_DOCKER_ROOT="$DOCKER_ROOT" FX_VOLUME=fold_backups
  bucket_reset "$M/bucket"
  unset OD_ID OD_MODE BUCKET_TAG
  # The resolved shape of the shipped service: identity, coordination, and the
  # archive, status and uploads mounts.
  jq -n --arg d "$DIR" '{name:"fold",services:{app:{},backup:{user:"65532:65532",cap_drop:["ALL"],
    environment:{FOLD_RECOVERY_DIR:"/coord"},volumes:[{type:"bind",source:$d,target:"/coord",read_only:true},
      {type:"volume",source:"backups",target:"/backups"},{type:"volume",source:"backup-status",target:"/backup-status"},
      {type:"volume",source:"uploads",target:"/data",read_only:true}]}},
    volumes:{backups:{name:"fold_backups"},"backup-status":{name:"fold_backup-status"},uploads:{name:"fold_uploads"}}}' >"$M/config.json"
  echo "$NOW" >"$M/now"
  echo 'DUMPBYTES' >"$M/dump-bytes"
  write_stubs
  export PATH="$FX/bin:$ORIG_PATH"
  hash -r
  archive_deadline 30
}

mkf() { # PATH CONTENT EPOCH
  printf '%s' "$2" >"$1"
  chmod 600 "$1"
  touch -m -d "@$3" "$1"
}

# resolve_and_layout: the cutover's observation and layout steps.
resolve_and_layout() {
  archive_deadline 30
  archive_volume_resolve "$DIR" || return $?
  archive_layout
}

tree_sig() { # DIR: names, sizes, exact mtimes, owners, modes, hashes
  (cd "$1" && find . -mindepth 1 -printf '%p %y %s %T@ %u:%g %m\n' | sort; find . -type f -exec sha256sum {} + | sort)
}

with_lock() { # run a command line with backup.lock held in this shell
  backup_coord_acquire "$DIR" 5 maintenance || return 75
  "$@" || { local rc=$?; backup_coord_release; return "$rc"; }
  backup_coord_release
}

# ------------------------------------------------------------------ cases

case_volume_resolution() {
  fx_setup volume
  local rc
  archive_deadline 30
  archive_volume_resolve "$DIR"
  eq "$ARCH_MOUNT" "$MOUNT" "the mountpoint"
  eq "$ARCH_SERVICE_DEFINED" 0 "the service is defined"
  ! grep -qE 'volume (create|rm)|compose (up|run|create)' "$M/log" || fail "resolution created or changed a resource"
  # Absence is a successful empty listing; a failed observation is not absence.
  touch "$M/volume-absent"; rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 3 "absent volume"; rm "$M/volume-absent"
  touch "$M/fail-ls"; rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 4 "a failed listing is not absence"; rm "$M/fail-ls"
  touch "$M/fail-config"; rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 4 "an unreadable configuration"; rm "$M/fail-config"
  touch "$M/fail-info"; rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 4 "an unreadable Docker root"; rm "$M/fail-info"
  # Identity mismatches are refused, never guessed around.
  for flag in volume-options foreign-labels mountpoint-elsewhere; do
    touch "$M/$flag"; rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 5 "$flag"; rm "$M/$flag"
  done
  jq '.volumes.backups.driver_opts={"device":"/x"}' "$M/config.json" >"$M/c" && mv "$M/c" "$M/config.json"
  rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 5 "a configured driver option"
  jq 'del(.volumes.backups.driver_opts)' "$M/config.json" >"$M/c" && mv "$M/c" "$M/config.json"
  chmod 775 "$DOCKER_ROOT/volumes"; rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 5 "a group-writable ancestor"; chmod 755 "$DOCKER_ROOT/volumes"
  mv "$DOCKER_ROOT/volumes/fold_backups" "$DOCKER_ROOT/volumes/real"; ln -s real "$DOCKER_ROOT/volumes/fold_backups"
  rc=0; archive_volume_resolve "$DIR" || rc=$?; eq "$rc" 5 "a symlinked component"
  rm "$DOCKER_ROOT/volumes/fold_backups"; mv "$DOCKER_ROOT/volumes/real" "$DOCKER_ROOT/volumes/fold_backups"
  # No backup service in Compose is reported, not an error.
  jq 'del(.services.backup)' "$M/config.json" >"$M/c" && mv "$M/c" "$M/config.json"
  archive_volume_resolve "$DIR"; eq "$ARCH_SERVICE_DEFINED" 1 "no service"
  # Every observation shares one absolute deadline.
  echo 30 >"$M/slow"; archive_deadline 2
  local started=$SECONDS
  rc=0; archive_volume_resolve "$DIR" || rc=$?
  eq "$rc" 4 "a stalled Docker"; (( SECONDS - started < 12 )) || fail "a stalled observation was not bounded"
}

case_layout() {
  fx_setup layout
  archive_volume_resolve "$DIR"
  archive_layout
  eq "$(stat -c '%u:%g:%a' "$MOUNT")" "0:65532:1770" "the repaired volume root"
  eq "$(stat -c '%u:%g:%a' "$AD")" "0:0:700" "adopted/"
  local sig
  sig="$(tree_sig "$MOUNT")"
  archive_layout
  eq "$(tree_sig "$MOUNT")" "$sig" "an idempotent validation"
  # An incompatible adopted/ is refused and neither followed nor repaired.
  rmdir "$AD"; mkdir "$FX/elsewhere"; ln -s "$FX/elsewhere" "$AD"
  archive_layout && fail "a symlinked adopted/ was accepted"
  eq "$(ls -A "$FX/elsewhere")" "" "the link target was written"
  rm "$AD"; : >"$AD"
  archive_layout && fail "a file named adopted was accepted"
  rm "$AD"; mkdir "$AD"; chown 65532:65532 "$AD"; chmod 700 "$AD"
  archive_layout && fail "a service-owned adopted/ was accepted"
  eq "$(stat -c '%u:%a' "$AD")" "65532:700" "a refused adopted/ was repaired"
  chown 0:0 "$AD"; chmod 755 "$AD"
  archive_layout && fail "a mode-755 adopted/ was accepted"
  eq "$(stat -c '%a' "$AD")" 755 "a refused adopted/ was repaired"
}

case_service_isolation() {
  fx_setup isolation
  resolve_and_layout
  mkf "$AD/fold-20261001-0230.dump" ROOTCOPY "$((NOW - 3 * DAY))"
  local want
  want="$(stat -c '%.9Y' "$AD/fold-20261001-0230.dump")"
  as_service() { setpriv --reuid 65532 --regid 65532 --clear-groups -- "$@"; }
  as_service bash -c 'cat "$1"' _ "$AD/fold-20261001-0230.dump" 2>/dev/null && fail "the service read an adopted copy"
  as_service bash -c ': >"$1/new"' _ "$AD" 2>/dev/null && fail "the service wrote into adopted/"
  as_service touch -d '2000-01-01' "$AD/fold-20261001-0230.dump" 2>/dev/null && fail "the service changed an adopted mtime"
  as_service rmdir "$AD" 2>/dev/null && fail "the service removed adopted/"
  as_service mv "$AD" "$MOUNT/elsewhere" 2>/dev/null && fail "the service renamed adopted/"
  # Its own top-level archives work, and a recursive mutation reaches nothing adopted.
  as_service bash -c 'printf own >"$1/fold-20261001-0300.dump" && rm -f "$1/fold-20261001-0300.dump"' _ "$MOUNT" || fail "the service lost its own archives"
  as_service bash -c 'find "$1" -type f -exec touch -d 2000-01-01 {} + ; find "$1" -type f -delete' _ "$MOUNT" 2>/dev/null || true
  eq "$(cat "$AD/fold-20261001-0230.dump")" ROOTCOPY "an adopted copy's bytes"
  eq "$(stat -c '%.9Y' "$AD/fold-20261001-0230.dump")" "$want" "an adopted copy's mtime"
}

adopt_in_lock() { with_lock archive_adopt "$ORIG"; }

case_adoption() {
  fx_setup adopt
  local f sig out
  mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY)).123456789"
  mkf "$ORIG/uploads-20261001-0230.tgz" TGZ1 "$((NOW - 3 * DAY)).987654321"
  mkf "$ORIG/fold-20261001-0230.dump.age" AGE1 "$((NOW - 40 * DAY)).5"
  printf 'x' >"$ORIG/notes.txt"
  ln -s fold-20261001-0230.dump "$ORIG/fold-20261002-0230.dump"
  sig="$(tree_sig "$ORIG")"
  resolve_and_layout
  out="$(adopt_in_lock)"
  eq "$out" "adopt copied=3 identical=0 repaired=0 refused=0" "the first adoption"
  for f in fold-20261001-0230.dump uploads-20261001-0230.tgz fold-20261001-0230.dump.age; do
    cmp -s "$ORIG/$f" "$AD/$f" || fail "$f differs after adoption"
    eq "$(stat -c '%.9Y' "$ORIG/$f")" "$(stat -c '%.9Y' "$AD/$f")" "$f exact mtime"
    eq "$(stat -c '%u:%g:%a' "$AD/$f")" "0:0:600" "$f owner and mode"
  done
  [[ ! -e "$AD/notes.txt" && ! -e "$AD/fold-20261002-0230.dump" ]] || fail "an unknown name or a link was copied"
  eq "$(tree_sig "$ORIG")" "$sig" "the originals"
  eq "$(adopt_in_lock)" "adopt copied=0 identical=3 repaired=0 refused=0" "an identical retry"

  # A differing member refuses its whole stamp: nothing changes and no sibling is filled.
  rm -f "$AD/fold-20261001-0230.dump.age"
  printf 'changed' >"$AD/uploads-20261001-0230.tgz"
  sig="$(tree_sig "$AD")"
  eq "$(adopt_in_lock 2>"$FX/err")" "adopt copied=0 identical=0 repaired=0 refused=1" "a differing stamp"
  eq "$(tree_sig "$AD")" "$sig" "a refused stamp"
  grep -q 'stamp 20261001-0230 was not adopted' "$FX/err" || fail "the refusal does not name its stamp"
  # Other stamps still adopt around a refusal.
  mkf "$ORIG/fold-20261005-0230.dump" DUMP5 "$((NOW - 2 * DAY))"
  eq "$(adopt_in_lock 2>/dev/null)" "adopt copied=1 identical=0 repaired=0 refused=1" "a refusal beside a new stamp"
  cp -p "$ORIG/uploads-20261001-0230.tgz" "$AD/uploads-20261001-0230.tgz"

  # A residue and an unknown file: the residue (ours, root-owned) goes, the file stays.
  : >"$AD/.tmp.fold-20261001-0230.dump.4242"; printf junk >"$AD/keep.txt"
  adopt_in_lock >/dev/null 2>&1
  [[ ! -e "$AD/.tmp.fold-20261001-0230.dump.4242" && -f "$AD/keep.txt" ]] || fail "residue handling"

  # A conflict, then members that expire unequally, then a retry: the lone sibling
  # of a differing copy is never filled, because once the conflicting original is
  # gone nothing could say whose copy the sibling would pair with.
  fx_setup orphan
  mkf "$ORIG/fold-20261010-0230.dump" DUMPA "$((NOW - 20 * DAY))"
  mkf "$ORIG/uploads-20261010-0230.tgz" TGZA "$((NOW - 5 * DAY))"
  resolve_and_layout
  mkf "$AD/fold-20261010-0230.dump" DUMPX "$((NOW - 4 * DAY))"
  sig="$(tree_sig "$AD")"
  eq "$(adopt_in_lock 2>/dev/null)" "adopt copied=0 identical=0 repaired=0 refused=1" "the conflicting stamp"
  eq "$(tree_sig "$AD")" "$sig" "the conflicting stamp's copies"
  archive_expire_dir "$ORIG" 14 "$NOW" >/dev/null
  [[ ! -e "$ORIG/fold-20261010-0230.dump" && -e "$ORIG/uploads-20261010-0230.tgz" ]] || fail "the members did not expire unequally"
  eq "$(adopt_in_lock 2>"$FX/err")" "adopt copied=0 identical=0 repaired=0 refused=1" "the retry after the conflicting original expired"
  grep -q 'fold-20261010-0230.dump has no surviving original' "$FX/err" || fail "the orphan refusal does not name its member: $(cat "$FX/err")"
  [[ ! -e "$AD/uploads-20261010-0230.tgz" ]] || fail "a sibling was filled beside a differing copy"
  eq "$(tree_sig "$AD")" "$sig" "the stamp after the retry"
  # Once that copy expires too, the stamp is free and its original member is adopted.
  archive_expire_dir "$AD" 14 "$((NOW + 11 * DAY))" >/dev/null
  eq "$(adopt_in_lock 2>/dev/null)" "adopt copied=1 identical=0 repaired=0 refused=0" "the stamp after the copy expired"
  # A sibling that was copied before its own original expired is not an orphan to fill around.
  mkf "$ORIG/fold-20261011-0230.dump" DUMPB "$((NOW - 20 * DAY))"
  mkf "$ORIG/uploads-20261011-0230.tgz" TGZB "$((NOW - 5 * DAY))"
  adopt_in_lock >/dev/null 2>&1
  archive_expire_dir "$ORIG" 14 "$NOW" >/dev/null
  eq "$(adopt_in_lock 2>/dev/null)" "adopt copied=0 identical=2 repaired=0 refused=0" "a fully adopted stamp whose original dump expired"

  # An existing copy with the right bytes is accepted only as a root-owned mode-600
  # file with one link, and is never repaired in place.
  fx_setup meta
  mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY))"
  mkf "$ORIG/uploads-20261001-0230.tgz" TGZ1 "$((NOW - 3 * DAY))"
  resolve_and_layout
  adopt_in_lock >/dev/null
  refused_meta() { # WHAT
    sig="$(tree_sig "$AD")"
    eq "$(adopt_in_lock 2>"$FX/err")" "adopt copied=0 identical=0 repaired=0 refused=1" "$1"
    grep -q 'root-only mode-600 file with one link' "$FX/err" || fail "$1: the refusal does not say why"
    eq "$(tree_sig "$AD")" "$sig" "$1 was repaired"
  }
  chown 65532:65532 "$AD/fold-20261001-0230.dump"; refused_meta "a copy owned by the service"; chown 0:0 "$AD/fold-20261001-0230.dump"
  chmod 644 "$AD/uploads-20261001-0230.tgz"; refused_meta "a mode-644 copy"; chmod 600 "$AD/uploads-20261001-0230.tgz"
  ln "$AD/fold-20261001-0230.dump" "$FX/other-name"; refused_meta "a copy with another hard link"; rm "$FX/other-name"
  eq "$(adopt_in_lock)" "adopt copied=0 identical=2 repaired=0 refused=0" "the copies once their metadata is right"
}

# Bash remembers where it found a command, so a stub added or removed mid-case is
# ignored (or, once deleted, fails with 127) until the table is cleared.
stub_cmd() { # NAME BODY: a PATH stub that may call the real command as REAL
  { printf '#!/bin/bash\nREAL=%s\n' "$(command -v -p "$1")"; cat; } >"$FX/bin/$1"
  chmod +x "$FX/bin/$1"
  hash -r
}
unstub() { # NAME...
  local n
  for n in "$@"; do /usr/bin/rm -f -- "$FX/bin/$n"; done
  hash -r
}

case_adoption_faults() {
  fx_setup faults
  local out
  mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY))"
  mkf "$ORIG/uploads-20261001-0230.tgz" TGZ1 "$((NOW - 3 * DAY))"
  resolve_and_layout
  # Partial publication: the second link fails, the retry completes the stamp.
  stub_cmd ln <<'EOF'
n=$(( $(cat "$FX_MODEL/ln.n" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$FX_MODEL/ln.n"
[[ "$n" != 2 ]] || exit 1
exec "$REAL" "$@"
EOF
  adopt_in_lock >/dev/null 2>&1 && fail "a failed publication was reported as success"
  eq "$(find "$AD" -name 'fold-*' -o -name 'uploads-*' | wc -l)" 1 "one member after the failed publication"
  [[ -z "$(find "$AD" -name '.tmp.*')" ]] || fail "the failed publication left its temporary file"
  unstub ln
  eq "$(adopt_in_lock)" "adopt copied=1 identical=1 repaired=0 refused=0" "the retry"
  cmp -s "$ORIG/uploads-20261001-0230.tgz" "$AD/uploads-20261001-0230.tgz" || fail "the retried member"

  # An original that changes while it is copied is never published (content, then mtime only).
  rm -f "$AD"/fold-* "$AD"/uploads-*
  stub_cmd cp <<'EOF'
"$REAL" "$@" || exit $?
[[ "${*: -2:1}" != */fold-20261001-0230.dump || ! -e "$FX_MODEL/mutate" ]] || {
  if [[ "$(cat "$FX_MODEL/mutate")" == content ]]; then printf more >>"${*: -2:1}"; else touch -d '@1700000001' "${*: -2:1}"; fi
}
EOF
  for how in content mtime; do
    echo "$how" >"$M/mutate"
    mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY))"
    adopt_in_lock >/dev/null 2>&1 && fail "a changing original ($how) was adopted"
    [[ ! -e "$AD/fold-20261001-0230.dump" && -z "$(find "$AD" -name '.tmp.*')" ]] || fail "a changing original ($how) left a file"
  done
  rm "$M/mutate"; unstub cp
  mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY))"

  # Equal bytes with another mtime are repaired, then synchronized; a failed
  # synchronization is a failure and a retry completes it.
  adopt_in_lock >/dev/null
  touch -m -d '@1600000000' "$AD/fold-20261001-0230.dump"
  stub_cmd sync <<'EOF'
[[ ! -e "$FX_MODEL/sync-fail" || "${*: -1}" != */adopted/fold-20261001-0230.dump ]] || exit 1
exec "$REAL" "$@"
EOF
  touch "$M/sync-fail"
  adopt_in_lock >/dev/null 2>&1 && fail "a failed synchronization was reported as success"
  eq "$(stat -c %Y "$AD/fold-20261001-0230.dump")" "$(stat -c %Y "$ORIG/fold-20261001-0230.dump")" "the repaired mtime"
  rm "$M/sync-fail"
  out="$(adopt_in_lock)"
  eq "$out" "adopt copied=0 identical=2 repaired=0 refused=0" "the retry after a failed synchronization"
}

case_lock_inheritance() {
  fx_setup lock
  mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY))"
  resolve_and_layout
  stub_cmd cp <<'EOF'
echo started >"$FX_MODEL/cp.started"
sleep "$(cat "$FX_MODEL/cp-sleep")"
"$REAL" "$@"; rc=$?
date +%s.%N >"$FX_MODEL/cp.done"
exit "$rc"
EOF
  echo 4 >"$M/cp-sleep"
  # The adoption runs in its own shell so that shell can be killed alone.
  bash -c 'source "$1"; backup_coord_acquire "$2" 5 maintenance; ARCH_MOUNT="$3"; archive_adopt "$4"' _ "$CHECKS" "$DIR" "$MOUNT" "$ORIG" >/dev/null 2>&1 &
  local parent=$!
  local i
  for ((i = 0; i < 100; i++)); do [[ -e "$M/cp.started" ]] && break; sleep 0.1; done
  [[ -e "$M/cp.started" ]] || fail "the native copy never started"
  kill -9 "$parent"
  wait "$parent" 2>/dev/null || true
  # Parent gone, copy still running: admission is still refused.
  local rc=0
  ( backup_coord_acquire "$DIR" 0 restore ) >/dev/null 2>&1 || rc=$?
  eq "$rc" 75 "restore admission while a copy child still runs"
  # Once the child ends the lock frees, and nothing writes after admission.
  for ((i = 0; i < 150; i++)); do flock -n "$DIR/backup.lock" true 2>/dev/null && break; sleep 0.1; done
  flock -n "$DIR/backup.lock" true || fail "the lock was never released"
  backup_coord_acquire "$DIR" 0 restore
  local admitted done_at sig
  admitted="$(/usr/bin/date +%s.%N)"
  [[ -e "$M/cp.done" ]] || fail "admission happened before the copy finished"
  done_at="$(cat "$M/cp.done")"
  awk -v a="$done_at" -v b="$admitted" 'BEGIN { exit !(a <= b) }' || fail "a write finished after admission"
  sig="$(tree_sig "$AD")"
  sleep 1.5
  eq "$(tree_sig "$AD")" "$sig" "writes after restore admission"
  backup_coord_release
}

mk_age() { # NAME AGE_SECONDS: an original of that age
  mkf "$ORIG/$1" "$1" "$((NOW - $2))"
}

case_retention() {
  fx_setup retention
  local n
  mk_age fold-20261005-0001.dump $((14 * DAY))
  mk_age fold-20261005-0002.dump $((14 * DAY + 1))
  mk_age fold-20261001-0003.dump $((179 * DAY))
  mk_age fold-20261001-0004.dump $((180 * DAY))
  mk_age fold-20261001-0005.dump $((181 * DAY))
  mk_age uploads-20261005-0007.tgz.age $((200 * DAY))
  mk_age uploads-20261005-0008.tgz $((3 * DAY))
  mk_age notes.txt $((400 * DAY))
  ln -s fold-20261005-0001.dump "$ORIG/fold-20261005-0009.dump"; touch -h -d "@$((NOW - 400 * DAY))" "$ORIG/fold-20261005-0009.dump"
  mkdir "$ORIG/fold-20261005-0010.dump"
  # Members of one stamp age separately.
  mk_age fold-20261006-0011.dump $((20 * DAY))
  mk_age uploads-20261006-0011.tgz $((5 * DAY))
  archive_expire_dir "$ORIG" 14 "$NOW" >/dev/null
  for n in fold-20261005-0001.dump fold-20261001-0003.dump uploads-20261005-0008.tgz notes.txt fold-20261005-0009.dump fold-20261005-0010.dump uploads-20261006-0011.tgz; do
    [[ -e "$ORIG/$n" || -L "$ORIG/$n" ]] || fail "$n was removed"
  done
  for n in fold-20261005-0002.dump fold-20261001-0004.dump fold-20261001-0005.dump uploads-20261005-0007.tgz.age fold-20261006-0011.dump; do
    [[ ! -e "$ORIG/$n" ]] || fail "$n was kept past its date"
  done
  # Values above 180 are honored: 200 days exactly stays, one second more goes.
  mk_age fold-20261007-0012.dump $((200 * DAY))
  mk_age fold-20261007-0013.dump $((200 * DAY + 1))
  archive_expire_dir "$ORIG" 200 "$NOW" >/dev/null
  [[ -e "$ORIG/fold-20261007-0012.dump" && ! -e "$ORIG/fold-20261007-0013.dump" ]] || fail "the keep-200 boundary"
}

case_expiry_faults() {
  fx_setup faults2
  mk_age fold-20261005-0001.dump $((20 * DAY))
  mk_age fold-20261005-0002.dump $((20 * DAY))
  # A partial or failed enumeration deletes nothing and acknowledges nothing.
  stub_cmd find <<'EOF'
printf '%s\0' "$1/fold-20261005-0001.dump"
exit 1
EOF
  archive_expire_dir "$ORIG" 14 "$NOW" >/dev/null 2>&1 && fail "a failed enumeration was acknowledged"
  unstub find
  [[ -e "$ORIG/fold-20261005-0001.dump" && -e "$ORIG/fold-20261005-0002.dump" ]] || fail "a failed enumeration deleted a file"
  # A metadata failure for one member also deletes nothing.
  stub_cmd stat <<'EOF'
[[ "${*: -1}" != */fold-20261005-0002.dump ]] || exit 1
exec "$REAL" "$@"
EOF
  archive_expire_dir "$ORIG" 14 "$NOW" >/dev/null 2>&1 && fail "a metadata failure was acknowledged"
  unstub stat
  [[ -e "$ORIG/fold-20261005-0001.dump" ]] || fail "a metadata failure deleted a file"
  # A deletion failure is incomplete, and the other due file still goes.
  stub_cmd rm <<'EOF'
[[ "${*: -1}" != */fold-20261005-0001.dump ]] || exit 1
exec "$REAL" "$@"
EOF
  archive_expire_dir "$ORIG" 14 "$NOW" >/dev/null 2>&1 && fail "a failed deletion was acknowledged"
  unstub rm
  [[ -e "$ORIG/fold-20261005-0001.dump" && ! -e "$ORIG/fold-20261005-0002.dump" ]] || fail "a failed deletion handling"
}

# seed_pair_state: originals and copies of the same stamps and dates.
seed_pairs() {
  local n
  resolve_and_layout
  for n in fold-20261005-0001.dump uploads-20261005-0001.tgz fold-20261006-0002.dump uploads-20261006-0002.tgz; do
    mk_age "$n" $((20 * DAY))
  done
  mk_age fold-20261008-0003.dump $((2 * DAY))
  with_lock archive_adopt "$ORIG" >/dev/null
}

expire_in_lock() { # KEEP [NOW]
  [[ -z "${2:-}" ]] || echo "$2" >"$M/now"
  with_lock archive_expire_run "$DIR" "$ORIG" "$1"
}

case_expiry_decisions() {
  fx_setup decisions
  seed_pairs
  local rc=0 out
  # Docker is down: originals still expire, copies stay owed, the result is nonzero.
  touch "$M/fail-ls"
  expire_in_lock 14 >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 4 "originals done, copies pending"
  [[ ! -e "$ORIG/fold-20261005-0001.dump" && -e "$ORIG/fold-20261008-0003.dump" ]] || fail "original expiry waited for Docker"
  [[ -e "$AD/fold-20261005-0001.dump" ]] || fail "a copy was removed without an observation"
  eq "$(cat "$DIR/$ARCHIVE_PENDING")" "14 $NOW done pending" "the recorded decision"
  eq "$(stat -c '%u:%a' "$DIR/$ARCHIVE_PENDING")" "0:600" "the pending file"
  # KEEP_DAYS rises to 30 later: replay still deletes the copies under keep 14.
  rm "$M/fail-ls"
  rc=0
  expire_in_lock 30 "$((NOW + DAY))" >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 0 "the replay"
  [[ ! -e "$AD/fold-20261005-0001.dump" && ! -e "$AD/uploads-20261006-0002.tgz" ]] || fail "the replay did not delete the admitted copies"
  [[ -e "$AD/fold-20261008-0003.dump" ]] || fail "a younger copy was deleted"
  [[ ! -e "$DIR/$ARCHIVE_PENDING" ]] || fail "a finished decision stayed recorded"
  grep -q 'expiry originals=0 adopted_copies=' "$FX/out" || fail "the populations are not reported separately: $(cat "$FX/out")"

  # An old pending decision with unavailable copy work never delays the current originals.
  fx_setup decisions2
  seed_pairs
  printf '90 %s done pending\n' "$((NOW - 100 * DAY))" >"$DIR/$ARCHIVE_PENDING"; chmod 600 "$DIR/$ARCHIVE_PENDING"
  touch "$M/fail-ls"
  rc=0; expire_in_lock 14 >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 4 "old copy work unavailable"
  [[ ! -e "$ORIG/fold-20261005-0001.dump" ]] || fail "the current original expiry was delayed"
  eq "$(wc -l <"$DIR/$ARCHIVE_PENDING")" 2 "both decisions stay recorded"
  # Equivalent keeps merge by the greater now and reset both sides; an older now changes nothing.
  rm "$M/fail-ls"
  printf '14 %s done done\n' "$((NOW - 3 * DAY))" >"$DIR/$ARCHIVE_PENDING"
  archive_pending_load "$DIR"
  archive_pending_merge 14 "$((NOW - 5 * DAY))"
  eq "${#P_KEEP[@]} ${P_NOW[0]} ${P_ORIG[0]} ${P_COPY[0]}" "1 $((NOW - 3 * DAY)) done done" "an older decision"
  archive_pending_merge 14 "$((NOW + 50))"
  eq "${#P_KEEP[@]} ${P_NOW[0]} ${P_ORIG[0]} ${P_COPY[0]}" "1 $((NOW + 50)) pending pending" "a widened decision"
  archive_pending_merge 30 "$NOW"
  eq "${#P_KEEP[@]}" 2 "a different keep"
  # A damaged enumeration of the copies acknowledges nothing.
  fx_setup decisions3
  seed_pairs
  stub_cmd find <<'EOF'
case "$*" in *adopted*) printf '%s\0' "$1/fold-20261005-0001.dump"; exit 1 ;; esac
exec "$REAL" "$@"
EOF
  rc=0; expire_in_lock 14 >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 4 "a failed copy enumeration"
  [[ -e "$AD/fold-20261005-0001.dump" ]] || fail "a copy was deleted from a partial enumeration"
  grep -q ' pending$' "$DIR/$ARCHIVE_PENDING" || fail "a failed copy enumeration was acknowledged"
  # An unusable pending file refuses everything.
  fx_setup decisions4
  seed_pairs
  printf 'garbage\n' >"$DIR/$ARCHIVE_PENDING"; chmod 600 "$DIR/$ARCHIVE_PENDING"
  rc=0; expire_in_lock 14 >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "an unreadable pending file"
  [[ -e "$ORIG/fold-20261005-0001.dump" ]] || fail "expiry ran past an unreadable decision file"
}

# crash_stubs CMD...: PATH stubs that count their calls (model/CMD.n) and, when
# model/kill-at holds "CMD N", kill the calling script at its Nth call (an rm
# after it ran, every other command before it), the way a lost process would.
crash_stubs() {
  local p
  for p in "$@"; do
    stub_cmd "$p" <<'EOF'
k=$(( $(cat "$FX_MODEL/$(basename "$0").n" 2>/dev/null || echo 0) + 1 ))
echo "$k" >"$FX_MODEL/$(basename "$0").n"
if [[ -e "$FX_MODEL/kill-at" && "$(cat "$FX_MODEL/kill-at")" == "$(basename "$0") $k" ]]; then
  [[ "$(basename "$0")" != rm ]] || "$REAL" "$@"
  exec 6>&- 9>&- # the lost process's children would die with it; this stand-in must not hold the lock
  kill -9 "$PPID"
  sleep 1
  exit 137
fi
exec "$REAL" "$@"
EOF
  done
}

# The real rm clears the model files: the stubbed one counts, and could kill the
# fixture itself at its Nth call.
clear_model() { /usr/bin/rm -f "$M"/*.n "$M/kill-at"; }

# crash_matrix NAME SETUP RUN CHECK CMD...: for each command and each call of it
# in one clean run, kill the run there, rerun without the stubs, and require the
# same final state as the clean run (CHECK prints it).
crash_matrix() {
  local name="$1" setup="$2" run="$3" check="$4" cmd n calls want rc
  shift 4
  "$setup" "$name-clean"
  "$run" >/dev/null 2>&1 || true
  want="$("$check")"
  for cmd in "$@"; do
    "$setup" "$name-$cmd"
    crash_stubs "$cmd"
    clear_model
    "$run" >/dev/null 2>&1 || true
    calls="$(cat "$M/$cmd.n" 2>/dev/null || echo 0)"
    (( calls >= 1 )) || fail "$cmd was never called by a clean $name run"
    for ((n = 1; n <= calls; n++)); do
      "$setup" "$name-$cmd-$n"
      crash_stubs "$cmd"
      echo "$cmd $n" >"$M/kill-at"
      "$run" >/dev/null 2>&1 || true
      /usr/bin/rm -f "$M/kill-at"; unstub "$cmd"
      rc=0
      "$run" >/dev/null 2>&1 || rc=$?
      eq "$rc" 0 "the rerun after a crash at $cmd call $n"
      eq "$("$check")" "$want" "the final state after a crash at $cmd call $n"
    done
  done
}

# Each run is its own shell, so the kill takes down only that run.
setup_expire() { fx_setup "$1"; seed_pairs; }
run_expire() { bash -c 'source "$1"; backup_coord_acquire "$2" 5 maintenance; archive_expire_run "$2" "$3" 14' _ "$CHECKS" "$DIR" "$ORIG"; }
check_expire() { printf '%s|%s|%s' "$(ls "$ORIG" | tr '\n' ' ')" "$(ls "$AD" | tr '\n' ' ')" "$([[ -e "$DIR/$ARCHIVE_PENDING" ]] && echo pending || echo none)"; }

setup_adopt() {
  fx_setup "$1"
  mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY)).123456789"
  mkf "$ORIG/uploads-20261001-0230.tgz" TGZ1 "$((NOW - 3 * DAY)).5"
  mkf "$ORIG/fold-20261003-0230.dump" DUMP3 "$((NOW - 2 * DAY))"
  resolve_and_layout
}
run_adopt() { bash -c 'source "$1"; backup_coord_acquire "$2" 5 maintenance; ARCH_MOUNT="$3"; archive_adopt "$4"' _ "$CHECKS" "$DIR" "$MOUNT" "$ORIG"; }
check_adopt() { tree_sig "$AD"; }

case_expiry_crashes() {
  crash_matrix expire setup_expire run_expire check_expire rm mv sync
}

case_adoption_crashes() {
  crash_matrix adopt setup_adopt run_adopt check_adopt cp chmod touch sync ln
}

# Host production: a kill at any ln, sync or chmod leaves no partial archive under a
# final name and no residue after the next run; that run (a minute later, since the
# stamp may be taken) makes a complete pair.
case_host_backup_crashes() {
  local cmd n calls s1 s2 f
  prep() {
    fx_setup "$1"
    cp "$ROOT/deploy/digitalocean/checks.sh" "$ROOT/deploy/digitalocean/backup.sh" "$DIR/"
    s1="$(/usr/bin/date -d "@$NOW" +%Y%m%d-%H%M)"
    s2="$(/usr/bin/date -d "@$((NOW + 60))" +%Y%m%d-%H%M)"
  }
  hbr() { bash "$DIR/backup.sh" --deploy-dir "$DIR" --backup-dir "$ORIG" --env-file "$FX/none.env"; }
  for cmd in ln sync chmod; do
    prep "hc-$cmd"
    crash_stubs "$cmd"
    clear_model
    hbr >/dev/null 2>&1
    calls="$(cat "$M/$cmd.n")"
    for ((n = 1; n <= calls; n++)); do
      prep "hc-$cmd-$n"
      crash_stubs "$cmd"
      echo "$cmd $n" >"$M/kill-at"
      hbr >/dev/null 2>&1 || true
      /usr/bin/rm -f "$M/kill-at"; unstub "$cmd"
      for f in "fold-$s1.dump:DUMPBYTES" "uploads-$s1.tgz:UPLOADS"; do
        [[ ! -e "$ORIG/${f%%:*}" ]] || eq "$(cat "$ORIG/${f%%:*}")" "${f##*:}" "a partial archive under its final name after a crash at $cmd call $n"
      done
      echo $((NOW + 60)) >"$M/now"
      hbr >/dev/null 2>&1 || fail "the next run failed after a crash at $cmd call $n"
      eq "$(cat "$ORIG/fold-$s2.dump")" DUMPBYTES "the dump after a crash at $cmd call $n"
      eq "$(cat "$ORIG/uploads-$s2.tgz")" UPLOADS "the uploads after a crash at $cmd call $n"
      [[ -z "$(find "$ORIG" -name '.tmp.*')" ]] || fail "residue after a crash at $cmd call $n"
    done
  done
}

case_service_archives() {
  fx_setup service
  resolve_and_layout
  jq 'del(.services.backup)' "$M/config.json" >"$M/c" && mv "$M/c" "$M/config.json"
  printf 'FOLD_BACKUP_KEEP_DAYS=30\n' >>"$DIR/.env"
  mkf "$MOUNT/fold-20261005-0001.dump" S1 "$((NOW - 40 * DAY))"
  mkf "$MOUNT/fold-20261005-0002.dump" S2 "$((NOW - 20 * DAY))"
  mkf "$AD/fold-20261005-0001.dump" C1 "$((NOW - 20 * DAY))"
  mk_age fold-20261005-0001.dump $((20 * DAY))
  local out
  out="$(expire_in_lock 14 2>/dev/null)"
  [[ ! -e "$MOUNT/fold-20261005-0001.dump" && -e "$MOUNT/fold-20261005-0002.dump" ]] || fail "service archives followed the wrong retention"
  [[ ! -e "$AD/fold-20261005-0001.dump" ]] || fail "the adopted copy kept the service's retention"
  grep -q 'expiry originals=1 adopted_copies=1 service_archives=1' <<<"$out" || fail "populations: $out"
  [[ -d "$AD" ]] || fail "service expiry removed adopted/"
}

case_host_backup() {
  fx_setup host
  mkdir -p "$DIR/bin"
  printf 'x' >"$FX/no.env"
  local run="$ROOT/deploy/digitalocean/backup.sh" rc sig
  cp "$ROOT/deploy/digitalocean/checks.sh" "$DIR/checks.sh"
  cp "$run" "$DIR/backup.sh"
  hb() { bash "$DIR/backup.sh" --deploy-dir "$DIR" --backup-dir "$ORIG" --env-file "$FX/none.env" "$@"; }
  hb >"$FX/out" 2>&1
  local stamp
  stamp="$(/usr/bin/date -d "@$NOW" +%Y%m%d-%H%M)"
  [[ -f "$ORIG/fold-$stamp.dump" && -f "$ORIG/uploads-$stamp.tgz" ]] || fail "a run made no pair: $(cat "$FX/out")"
  eq "$(cat "$ORIG/fold-$stamp.dump")" DUMPBYTES "the dump"
  eq "$(stat -c '%a' "$ORIG/fold-$stamp.dump")" 600 "the dump mode"
  sig="$(tree_sig "$ORIG")"
  # The same minute again: refused before anything is written, nothing changes.
  echo 'OTHERBYTES' >"$M/dump-bytes"
  rc=0; hb >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 75 "a same-minute run"
  eq "$(tree_sig "$ORIG" | grep -v LAST_OK)" "$(printf '%s\n' "$sig" | grep -v LAST_OK)" "the pair after a refused run"
  [[ -z "$(find "$ORIG" -name '.tmp.*')" ]] || fail "a refused run left temporary files"
  # The next minute makes a second pair and leaves the first untouched.
  echo $((NOW + 60)) >"$M/now"
  hb >"$FX/out" 2>&1
  eq "$(cat "$ORIG/fold-$stamp.dump")" DUMPBYTES "the first dump after a later run"
  # Expiry mode: no backup, no LAST_OK change, exit 0; recovery refuses with 75.
  local before after
  before="$(cat "$ORIG/LAST_OK")"
  : >"$M/log"
  hb --expire-only >"$FX/out" 2>&1
  eq "$(cat "$ORIG/LAST_OK")" "$before" "LAST_OK after an expiry-only run"
  ! grep -q 'pg_dump' "$M/log" || fail "an expiry-only run made a dump"
  : >"$DIR/recovery.lock"
  rc=0; hb --expire-only >"$FX/out" 2>&1 || rc=$?; eq "$rc" 75 "expiry under recovery"
  rc=0; hb >"$FX/out" 2>&1 || rc=$?; eq "$rc" 75 "a backup under recovery"
  rm "$DIR/recovery.lock"
  # --list-adopted names explicit paths and tells a verified pair from the rest.
  resolve_and_layout
  with_lock archive_adopt "$ORIG" >/dev/null
  rm -f "$AD/uploads-$stamp.tgz"
  hb --list-adopted >"$FX/out" 2>&1
  grep -q "^adopted $stamp incomplete $AD/fold-$stamp.dump" "$FX/out" || fail "an incomplete stamp: $(cat "$FX/out")"
  after="$(/usr/bin/date -d "@$((NOW + 60))" +%Y%m%d-%H%M)"
  grep -q "^adopted $after verified $AD/fold-$after.dump $AD/uploads-$after.tgz" "$FX/out" || fail "a verified pair: $(cat "$FX/out")"
  printf 'diverged' >>"$AD/fold-$after.dump"
  hb --list-adopted >"$FX/out" 2>&1
  grep -q "^adopted $after conflict" "$FX/out" || fail "a conflicting stamp"
}

case_export() {
  fx_setup export
  resolve_and_layout
  local dest="$FX/dumps" name=fold-20261101-0230.dump rc
  mkf "$MOUNT/$name" SERVICEDUMP "$((NOW - 100)).25"
  chown 65532:65532 "$MOUNT/$name"
  with_lock archive_export_dump "$DIR" "$name" "$dest"
  eq "$(cat "$dest/$name")" SERVICEDUMP "the exported bytes"
  eq "$(stat -c '%.9Y' "$dest/$name")" "$(stat -c '%.9Y' "$MOUNT/$name")" "the exported mtime"
  eq "$(stat -c '%u:%a' "$dest/$name")" "0:600" "the exported owner and mode"
  # An identical export is verified and synchronized, never rewritten.
  local ino
  ino="$(stat -c %i "$dest/$name")"
  with_lock archive_export_dump "$DIR" "$name" "$dest"
  eq "$(stat -c %i "$dest/$name")" "$ino" "an identical export"
  # The right bytes under the wrong owner, mode or link count are no rollback anchor,
  # and are not repaired.
  local sig
  chown 65532:65532 "$dest/$name"; sig="$(tree_sig "$dest")"
  rc=0; with_lock archive_export_dump "$DIR" "$name" "$dest" 2>/dev/null || rc=$?
  eq "$rc" 6 "an identical export owned by another user"; eq "$(tree_sig "$dest")" "$sig" "an export owned by another user was repaired"
  chown 0:0 "$dest/$name"; chmod 644 "$dest/$name"; sig="$(tree_sig "$dest")"
  rc=0; with_lock archive_export_dump "$DIR" "$name" "$dest" 2>/dev/null || rc=$?
  eq "$rc" 6 "an identical mode-644 export"; eq "$(tree_sig "$dest")" "$sig" "a mode-644 export was repaired"
  chmod 600 "$dest/$name"; ln "$dest/$name" "$FX/other-name"; sig="$(tree_sig "$dest")"
  rc=0; with_lock archive_export_dump "$DIR" "$name" "$dest" 2>/dev/null || rc=$?
  eq "$rc" 6 "an identical export with another link"; rm "$FX/other-name"
  with_lock archive_export_dump "$DIR" "$name" "$dest"
  # Same name, other bytes: refused and untouched.
  printf 'OTHER' >"$dest/$name"
  rc=0; with_lock archive_export_dump "$DIR" "$name" "$dest" 2>/dev/null || rc=$?
  eq "$rc" 3 "a differing export"
  eq "$(cat "$dest/$name")" OTHER "a refused export"
  # A source that changes during the first copy is detected; the retry exports it.
  rm "$dest/$name"
  stub_cmd cp <<'EOF'
"$REAL" "$@" || exit $?
[[ -e "$FX_MODEL/mutated" ]] || { : >"$FX_MODEL/mutated"; printf more >>"${*: -2:1}"; }
EOF
  with_lock archive_export_dump "$DIR" "$name" "$dest"
  eq "$(cat "$dest/$name")" SERVICEDUMPmore "the stabilised export"
  unstub cp
  # Not a name the service reports: refused.
  rc=0; with_lock archive_export_dump "$DIR" '../fold-20261101-0230.dump' "$dest" 2>/dev/null || rc=$?
  eq "$rc" 1 "an unsafe name"
}

case_settings() {
  fx_setup settings
  local before rc
  cat >"$FX/bin/timedatectl" <<'EOF'
#!/bin/bash
echo America/Los_Angeles
EOF
  chmod +x "$FX/bin/timedatectl"
  printf 'FOLD_BACKUP_S3_BUCKET=my-bucket\nFOLD_BACKUP_S3_ENDPOINT=https://s3.example.test\nFOLD_BACKUP_S3_REGION=r-1\nAWS_ACCESS_KEY_ID=AKIASENTINEL\nAWS_SECRET_ACCESS_KEY=SECRETSENTINEL+/=\nFOLD_BACKUP_AGE_RECIPIENT=age1abcdefghijklmnop\nFOLD_BACKUP_KEEP_DAYS=30\n' >"$DIR/backup.env"
  chmod 600 "$DIR/backup.env"
  printf '# mine\nDOMAIN=groups.example.org\nOTHER="keep me"' >"$DIR/.env"
  chmod 640 "$DIR/.env"
  before="$(sha256sum "$DIR/backup.env")"
  backup_settings_copy "$DIR" >"$FX/out" 2>&1
  eq "$(env_value "$DIR/.env" FOLD_BACKUP_S3_BUCKET)" my-bucket "the copied bucket"
  eq "$(env_value "$DIR/.env" FOLD_BACKUP_KEEP_DAYS)" 30 "the copied retention"
  eq "$(env_value "$DIR/.env" FOLD_BACKUP_TZ)" America/Los_Angeles "the host timezone"
  eq "$(env_value "$DIR/.env" OTHER)" "keep me" "an unrelated line"
  eq "$(stat -c '%a' "$DIR/.env")" 600 "the .env mode"
  eq "$(sha256sum "$DIR/backup.env")" "$before" "backup.env"
  ! grep -q 'SECRETSENTINEL\|AKIASENTINEL' "$FX/out" || fail "a secret reached the output"
  # The usual umask must not matter: every staging file holding the secrets is
  # private from its first byte, and none is left behind.
  printf '# mine\nDOMAIN=groups.example.org\nOTHER="keep me"\n' >"$DIR/.env"; chmod 640 "$DIR/.env"
  stub_cmd awk <<'EOF2'
stat -Lc %a /proc/self/fd/1 >>"$FX_MODEL/awk-modes"
exec "$REAL" "$@"
EOF2
  stub_cmd mv <<'EOF2'
find "$FX_DIR" -maxdepth 1 -name '.tmp.*' -printf '%m\n' >>"$FX_MODEL/mv-modes"
exec "$REAL" "$@"
EOF2
  export FX_DIR="$DIR"
  ( umask 022; backup_settings_copy "$DIR" >"$FX/out" 2>&1 )
  eq "$(sort -u "$M/awk-modes")" 600 "the mode of every transformation's output"
  eq "$(sort -u "$M/mv-modes")" 600 "the mode of the staging file at its rename"
  [[ -z "$(find "$DIR" -name '.tmp.*')" ]] || fail "a staging file was left behind"
  eq "$(stat -c '%a' "$DIR/.env")" 600 "the .env mode after a umask-022 copy"
  unstub awk mv
  # A transformation that fails after partial output replaces nothing: .env keeps
  # every byte, nothing is left behind, and the copy reports failure.
  printf '# mine\nDOMAIN=groups.example.org\nOTHER="keep me"\n' >"$DIR/.env"; chmod 600 "$DIR/.env"
  before="$(sha256sum "$DIR/.env")"
  stub_cmd awk <<'EOF2'
printf 'PARTIAL'
exit 2
EOF2
  rc=0; backup_settings_copy "$DIR" >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "a failed transformation"
  eq "$(sha256sum "$DIR/.env")" "$before" ".env after a failed transformation"
  [[ -z "$(find "$DIR" -name '.tmp.*')" ]] || fail "a failed transformation left a staging file"
  unstub awk
  # A failed rename or synchronization is a failure too, with .env untouched.
  stub_cmd mv <<'EOF2'
exit 1
EOF2
  rc=0; backup_settings_copy "$DIR" >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "a failed rename"
  eq "$(sha256sum "$DIR/.env")" "$before" ".env after a failed rename"
  [[ -z "$(find "$DIR" -name '.tmp.*')" ]] || fail "a failed rename left a staging file"
  unstub mv
  backup_settings_copy "$DIR" >"$FX/out" 2>&1
  eq "$(env_value "$DIR/.env" FOLD_BACKUP_KEEP_DAYS)" 30 "the retry after the failures"
  # A second run copies nothing; a differing value only warns, naming the key.
  backup_settings_copy "$DIR" >"$FX/out" 2>&1
  grep -q 'nothing copied' "$FX/out" || fail "a second copy: $(cat "$FX/out")"
  sed -i 's/^FOLD_BACKUP_KEEP_DAYS=.*/FOLD_BACKUP_KEEP_DAYS=45/' "$DIR/backup.env"
  backup_settings_copy "$DIR" >"$FX/out" 2>&1
  grep -q 'FOLD_BACKUP_KEEP_DAYS in .env differs' "$FX/out" || fail "no warning for a later difference"
  ! grep -q '45\|30' "$FX/out" || fail "a warning printed a value"
  eq "$(env_value "$DIR/.env" FOLD_BACKUP_KEEP_DAYS)" 30 ".env after a later difference"
  # Partial offsite settings refuse by key name only, and write nothing.
  printf 'DOMAIN=groups.example.org\n' >"$DIR/.env"
  sed -i '/AWS_SECRET_ACCESS_KEY/d' "$DIR/backup.env"
  local rc=0
  backup_settings_copy "$DIR" >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "partial settings"
  grep -q 'AWS_SECRET_ACCESS_KEY is missing' "$FX/out" || fail "the missing key is not named"
  ! grep -q 'AKIASENTINEL\|my-bucket' "$FX/out" || fail "a value reached the output"
  eq "$(cat "$DIR/.env")" "DOMAIN=groups.example.org" ".env after a refusal"
  # A value this tool will not write to .env refuses by key.
  printf 'FOLD_BACKUP_KEEP_DAYS=14\nFOLD_BACKUP_S3_BUCKET=my-bucket\nFOLD_BACKUP_S3_ENDPOINT=https://s3.example.test\nFOLD_BACKUP_S3_REGION=r-1\nAWS_ACCESS_KEY_ID=AKIASENTINEL\nAWS_SECRET_ACCESS_KEY=has$dollar\nFOLD_BACKUP_AGE_RECIPIENT=age1abcdefghijklmnop\n' >"$DIR/backup.env"
  rc=0; backup_settings_copy "$DIR" >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "an unsafe value"
  grep -q 'AWS_SECRET_ACCESS_KEY in backup.env has a character' "$FX/out" || fail "the key is not named"
}

# One identity rule, for the cutover and for a recovery's scheduler alike: exactly
# 65532:65532 with cap_drop ALL, nothing added, no privilege, and the shipped archive,
# status and uploads paths and mounts. A refusal must leave the service unstarted, so
# the log of the stubbed Docker never shows an `up`.
case_identity() {
  fx_setup identity
  local v
  resolve_and_layout
  backup_identity_check "$DIR" >/dev/null || fail "the shipped identity was refused"
  : >"$M/log"
  backup_scheduler_select recovery "$DIR" >/dev/null 2>&1 || fail "a recovery refused the shipped identity"
  grep -q 'up -d --no-deps --pull never backup' "$M/log" || fail "a recovery did not start the shipped service"
  for v in 'del(.services.backup.user)' '.services.backup.user="0:0"' '.services.backup.user="root"' '.services.backup.user="65532"' \
    '.services.backup.user="65532:0"' 'del(.services.backup.cap_drop)' '.services.backup.cap_drop=[]' '.services.backup.cap_drop=["NET_RAW"]' \
    '.services.backup.privileged=true' '.services.backup.cap_add=["NET_ADMIN"]' '.services.backup.pid="host"' '.services.backup.network_mode="host"' \
    '.services.backup.volumes|=map(select(.target!="/backups"))' \
    '.services.backup.volumes|=map(if .target=="/backups" then .source="scratch" else . end)' \
    '.services.backup.volumes|=map(if .target=="/backups" then .target="/elsewhere" else . end)' \
    '.services.backup.volumes|=map(if .target=="/backups" then .type="bind"|.source="/tmp" else . end)' \
    '.services.backup.volumes|=map(if .target=="/backups" then .read_only=true else . end)' \
    '.services.backup.volumes+=[{type:"volume",source:"backups",target:"/backups"}]' \
    '.services.backup.volumes|=map(select(.target!="/backup-status"))' \
    '.services.backup.volumes|=map(if .target=="/backup-status" then .source="backups" else . end)' \
    '.services.backup.volumes|=map(if .target=="/data" then .read_only=false else . end)' \
    '.services.backup.volumes|=map(if .target=="/data" then .source="backups" else . end)' \
    '.services.backup.environment.FOLD_BACKUP_ARCHIVE_DIR="/tmp"' '.services.backup.environment.FOLD_BACKUP_STATUS_DIR="/tmp"' \
    '.services.backup.environment.FOLD_BACKUP_DATA_DIR="/tmp"' '.services.backup.environment.FOLD_BACKUP_APP_UID="0"' \
    '.services.backup.environment.FOLD_BACKUP_APP_GID="0"' 'del(.volumes.backups)' \
    '.services.backup.volumes+=[{type:"bind",source:"/tmp/empty",target:"/data/uploads",read_only:true}]' \
    '.services.backup.volumes+=[{type:"volume",source:"scratch",target:"/data/uploads/2026"}]' \
    '.services.backup.volumes+=[{type:"tmpfs",target:"/backups/adopted"}]' \
    '.services.backup.volumes+=[{type:"bind",source:"/tmp/empty",target:"/backup-status/status.json"}]' \
    '.services.backup.volumes+=[{type:"bind",source:"/tmp/empty",target:"/data/../data/uploads"}]' \
    '.services.backup.volumes+=[{type:"bind",source:"/tmp/empty",target:"/tmp/./x"}]' \
    '.services.backup.volumes+=[{type:"bind",source:"/tmp/empty",target:"//data//uploads"}]' \
    '.services.backup.volumes+=[{type:"bind",source:"/tmp/empty",target:"/data/"}]' \
    '.services.backup.tmpfs=["/data/uploads"]' '.services.backup.tmpfs="/backups/old:size=1m"' \
    '.services.backup.volumes_from=["app"]' \
    '.services.backup.configs=[{source:"data"}]' \
    '.services.backup.configs=[{source:"c",target:"/data/uploads/avatars/17/photo.jpg"}]' \
    '.services.backup.configs=[{source:"c",target:"/tmp/c"}]' \
    '.services.backup.secrets=[{source:"s",target:"/backups/x"}]' \
    '.services.backup.secrets=["s"]' \
    '.services.backup.devices=["/dev/null:/data/uploads"]' \
    '.services.backup.devices=[{source:"/dev/null",target:"/data/uploads"}]' \
    '.services.backup.tmpfs=["/data"]' '.services.backup.tmpfs=["/backups"]' '.services.backup.tmpfs=["/backup-status"]' \
    '.services.backup.tmpfs="/data:size=1m"' '.services.backup.tmpfs=["/tmp","/backups:size=1m"]' \
    '.services.backup.volumes+=[{type:"tmpfs",target:"/data"}]' \
    '.services.backup.volumes|=map(if .target=="/backups" then .volume={subpath:"x"} else . end)' \
    '.services.backup.volumes|=map(if .target=="/backup-status" then .volume={subpath:"x"} else . end)' \
    '.services.backup.volumes|=map(if .target=="/data" then .volume={subpath:"uploads"} else . end)'; do
    jq "$v" "$M/config.json" >"$M/c"
    cp "$M/config.json" "$M/good"; cp "$M/c" "$M/config.json"
    : >"$M/log"
    backup_identity_check "$DIR" >/dev/null 2>&1 && fail "the identity check accepted $v"
    backup_scheduler_select recovery "$DIR" >/dev/null 2>&1 && fail "a recovery accepted $v"
    ! grep -q ' up ' "$M/log" || fail "a service was started with $v"
    cp "$M/good" "$M/config.json"
  done
  # Mounts that do not shadow a managed folder are not refused: a lookalike prefix, an
  # unrelated tmpfs, and the empty volume options Compose writes for a whole volume.
  jq '.services.backup.volumes+=[{type:"tmpfs",target:"/database"}] | .services.backup.tmpfs=["/tmp"]
    | .services.backup.volumes|=map(if .target=="/backups" then .volume={nocopy:true,subpath:""} else . end)' "$M/config.json" >"$M/c"
  cp "$M/config.json" "$M/good"; cp "$M/c" "$M/config.json"
  backup_identity_check "$DIR" >/dev/null 2>&1 || fail "an unrelated mount was refused"
  cp "$M/good" "$M/config.json"
  # Omitted fields and empty arrays are the shipped shape, not a refusal.
  jq '.services.backup.configs=[] | .services.backup.secrets=[] | .services.backup.devices=[] | .services.backup.tmpfs=[]' "$M/config.json" >"$M/c"
  cp "$M/config.json" "$M/good"; cp "$M/c" "$M/config.json"
  backup_identity_check "$DIR" >/dev/null 2>&1 || fail "empty configs, secrets, devices and tmpfs were refused"
  cp "$M/good" "$M/config.json"
  # Restating a default is not an override.
  jq '.services.backup.environment.FOLD_BACKUP_ARCHIVE_DIR="/backups" | .services.backup.environment.FOLD_BACKUP_STATUS_DIR=""' "$M/config.json" >"$M/c"
  cp "$M/c" "$M/config.json"
  backup_identity_check "$DIR" >/dev/null 2>&1 || fail "a restated default was refused"
  # A configuration that cannot be read verifies nothing, and nothing starts.
  touch "$M/fail-config"
  : >"$M/log"
  backup_identity_check "$DIR" >/dev/null 2>&1 && fail "an unreadable configuration was accepted"
  backup_scheduler_select recovery "$DIR" >/dev/null 2>&1 && fail "a recovery started on an unreadable configuration"
  ! grep -q ' up ' "$M/log" || fail "a service was started on an unreadable configuration"
}

# A recovery's scheduler also looks at the volume it is about to start the service
# on, read-only: a root that lost its sticky bit (or still belongs to the old
# owner), a damaged adopted/ or a volume it cannot observe starts nothing and
# repairs nothing; an absent volume is Compose's to create from the image.
case_recovery_volume() {
  fx_setup recovery-volume
  local sig
  resolve_and_layout
  : >"$M/log"
  backup_scheduler_select recovery "$DIR" >/dev/null 2>&1 || fail "a protected volume was refused"
  grep -q ' up -d ' "$M/log" || fail "a protected volume did not start the service"
  refuse_recovery() { # WHAT: the scheduler refuses, starts nothing and changes nothing
    : >"$M/log"; sig="$(tree_sig "$MOUNT")|$(stat -c '%u:%g:%a' "$MOUNT")"
    backup_scheduler_select recovery "$DIR" >/dev/null 2>&1 && fail "a recovery accepted $1"
    ! grep -q ' up ' "$M/log" || fail "a service was started with $1"
    eq "$(tree_sig "$MOUNT")|$(stat -c '%u:%g:%a' "$MOUNT")" "$sig" "a recovery repaired $1"
  }
  chmod 770 "$MOUNT"; refuse_recovery "a root without its sticky bit"; chmod 1770 "$MOUNT"
  chmod 1777 "$MOUNT"; refuse_recovery "a world-writable root"; chmod 1770 "$MOUNT"
  chown 65532:65532 "$MOUNT"; refuse_recovery "a root owned by the service"; chown 0:65532 "$MOUNT"
  chmod 755 "$AD"; refuse_recovery "a mode-755 adopted/"; chmod 700 "$AD"
  chown 65532:65532 "$AD"; refuse_recovery "a service-owned adopted/"; chown 0:0 "$AD"
  rmdir "$AD"; mkdir "$FX/elsewhere"; ln -s "$FX/elsewhere" "$AD"; refuse_recovery "a symlinked adopted/"; rm "$AD"; mkdir -m 700 "$AD"
  touch "$M/fail-ls"; refuse_recovery "a volume it could not list"; rm "$M/fail-ls"
  touch "$M/foreign-labels"; refuse_recovery "a foreign volume"; rm "$M/foreign-labels"
  # The volume is absent: nothing to protect yet, Compose creates it.
  touch "$M/volume-absent"; : >"$M/log"
  backup_scheduler_select recovery "$DIR" >/dev/null 2>&1 || fail "an absent volume was refused"
  grep -q ' up -d ' "$M/log" || fail "an absent volume did not start the service"
}

case_lastok() {
  fx_setup lastok
  local got
  echo "2026-11-15T10:00:00Z" >"$M/lastok"
  archive_deadline 10
  got="$(backup_lastok_epoch "$DIR")"
  eq "$got" "$(/usr/bin/date -u -d 2026-11-15T10:00:00Z +%s)" "the LAST_OK epoch"
  echo "garbage" >"$M/lastok"
  backup_lastok_epoch "$DIR" >/dev/null 2>&1 && fail "a malformed LAST_OK was read"
  # The cron only switches through the explicit cutover mode, never from a recovery.
  DEPLOY_DRILL=1 backup_cron_install "$DIR" full
  eq "$(DEPLOY_DRILL=1 backup_cron_kind "$DIR")" full "the host cron"
  DEPLOY_DRILL=1 backup_scheduler_select cutover-success "$DIR" >/dev/null
  eq "$(DEPLOY_DRILL=1 backup_cron_kind "$DIR")" expire "the switched cron"
}

# A full host run whose offsite settings are incomplete makes no backup and fails,
# but still expires what is past its retention (the retention is established first
# and alone). A KEEP_DAYS that is not usable expires nothing.
case_host_backup_offsite() {
  fx_setup hostoffsite
  mkdir -p "$DIR/bin"
  cp "$ROOT/deploy/digitalocean/checks.sh" "$ROOT/deploy/digitalocean/backup.sh" "$DIR/"
  hbo() { bash "$DIR/backup.sh" --deploy-dir "$DIR" --backup-dir "$ORIG" --env-file "$FX/part.env"; }
  local rc stamp
  stamp="$(/usr/bin/date -d "@$NOW" +%Y%m%d-%H%M)"
  mk_age fold-20261003-0230.dump $((20 * DAY))
  mk_age uploads-20261003-0230.tgz $((20 * DAY))
  mk_age fold-20261008-0230.dump $((2 * DAY))
  printf 'FOLD_BACKUP_KEEP_DAYS=14\nFOLD_BACKUP_S3_BUCKET=my-bucket\nAWS_ACCESS_KEY_ID=AKIASENTINEL\n' >"$FX/part.env"
  rc=0; hbo >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "a full run with incomplete offsite settings"
  grep -q 'FOLD_BACKUP_S3_ENDPOINT is missing' "$FX/out" || fail "the missing key is not named: $(cat "$FX/out")"
  ! grep -q 'AKIASENTINEL\|my-bucket' "$FX/out" || fail "a setting's value reached the output"
  [[ ! -e "$ORIG/fold-20261003-0230.dump" && ! -e "$ORIG/uploads-20261003-0230.tgz" ]] || fail "the overdue originals were kept"
  [[ -e "$ORIG/fold-20261008-0230.dump" ]] || fail "a younger original was expired"
  [[ ! -e "$ORIG/fold-$stamp.dump" && ! -e "$ORIG/LAST_OK" ]] || fail "a backup was made with incomplete offsite settings"
  [[ ! -e "$DIR/$ARCHIVE_PENDING" ]] || fail "a finished decision stayed recorded"
  # Unusable retention: nothing is expired.
  mk_age fold-20261002-0230.dump $((20 * DAY))
  printf 'FOLD_BACKUP_KEEP_DAYS=0\n' >"$FX/part.env"
  rc=0; hbo >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "a full run with an unusable KEEP_DAYS"
  [[ -e "$ORIG/fold-20261002-0230.dump" ]] || fail "an original expired under an unusable KEEP_DAYS"
  # Complete settings still validate in full: a bad endpoint is the run's failure and expiry still runs.
  printf 'FOLD_BACKUP_KEEP_DAYS=14\nFOLD_BACKUP_S3_BUCKET=my-bucket\nFOLD_BACKUP_S3_ENDPOINT=http://plain.example\nFOLD_BACKUP_S3_REGION=r-1\nAWS_ACCESS_KEY_ID=A\nAWS_SECRET_ACCESS_KEY=S\nFOLD_BACKUP_AGE_RECIPIENT=age1abc\n' >"$FX/part.env"
  rc=0; hbo >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "a full run with an invalid endpoint"
  [[ ! -e "$ORIG/fold-20261002-0230.dump" ]] || fail "expiry did not run after an invalid offsite setting"
}

# --- the offsite pair: one folder per run --------------------------------------
# Every run that uploads names a folder of its own, fold/<stamp>-<128 random bits>/, and
# puts its two members there. It never lists, reads or deletes in the bucket, so a key
# that can only write is enough, and two deployments sharing a bucket cannot meet except
# by choosing the same 128 bits.
host_two_setup() { # NAME: two deployments (own folder, lock and archives) and one bucket
  fx_setup "$1"
  cp "$ROOT/deploy/digitalocean/checks.sh" "$ROOT/deploy/digitalocean/backup.sh" "$DIR/"
  printf 'FOLD_BACKUP_KEEP_DAYS=14\nFOLD_BACKUP_S3_BUCKET=bkt\nFOLD_BACKUP_S3_ENDPOINT=https://s3.example\nFOLD_BACKUP_S3_REGION=r-1\nAWS_ACCESS_KEY_ID=A\nAWS_SECRET_ACCESS_KEY=S\nFOLD_BACKUP_AGE_RECIPIENT=age1abc\n' >"$FX/full.env"
  DIR2="$FX/dir2"; ORIG2="$FX/orig2"
  mkdir -p "$DIR2" "$ORIG2"
  chmod 755 "$DIR2"; chmod 700 "$ORIG2"
  cp "$DIR/.env" "$DIR2/.env"
  cp "$ROOT/deploy/digitalocean/checks.sh" "$ROOT/deploy/digitalocean/backup.sh" "$DIR2/"
  : >"$DIR2/backup.lock"; chown "0:${BACKUP_COORD_GID}" "$DIR2/backup.lock"; chmod 640 "$DIR2/backup.lock"
  HSTAMP="$(/usr/bin/date -d "@$NOW" +%Y%m%d-%H%M)"
  HSTAMP0="$(/usr/bin/date -d "@$((NOW - 3600))" +%Y%m%d-%H%M)"
}

hb1() { OD_ID="${1:-$ID1}" BUCKET_TAG=host1 bash "$DIR/backup.sh" --deploy-dir "$DIR" --backup-dir "$ORIG" --env-file "$FX/full.env" >"$FX/out" 2>&1; }
hb2() { OD_ID="${1:-$ID2}" BUCKET_TAG=host2 bash "$DIR2/backup.sh" --deploy-dir "$DIR2" --backup-dir "$ORIG2" --env-file "$FX/full.env" >"$FX/out2" 2>&1; }

# earlier_run: a successful run an hour ago, so LAST_OK exists and its folder is in the bucket.
earlier_run() {
  echo $((NOW - 3600)) >"$M/now"
  hb1 "$ID0" || fail "the earlier run failed: $(cat "$FX/out")"
  echo "$NOW" >"$M/now"
  LASTOK="$(cat "$ORIG/LAST_OK")"
}

case_host_offsite() {
  host_two_setup host-offsite
  local rc f out
  # One run: two PUTs under one folder named for the stamp and the id, nothing else.
  hb1 || fail "a run with offsite settings failed: $(cat "$FX/out")"
  out="$(cat "$FX/out")"
  bucket_check "$HSTAMP:$ID1" || fail "the committed keys: $(bucket_objects | tr '\n' ' ')"
  eq "$(bucket_attempts)" 2 "the PUT invocations of one run"
  bucket_each_key_once || fail "a key was the target of two invocations"
  grep -qF "offsite: s3://bkt/fold/$HSTAMP-$ID1/ via https://s3.example" <<<"$out" || fail "the folder is not logged: $out"
  grep -qF "uploaded s3://bkt/fold/$HSTAMP-$ID1/fold-$HSTAMP.dump.age" <<<"$out" || fail "the dump member is not logged: $out"
  grep -qF "uploaded s3://bkt/fold/$HSTAMP-$ID1/uploads-$HSTAMP.tgz.age" <<<"$out" || fail "the uploads member is not logged: $out"
  # The fake age only marks its output, so this proves routing: the bytes sent are the
  # local ciphertext, and not the plain dump.
  for f in "fold-$HSTAMP.dump" "uploads-$HSTAMP.tgz"; do
    cmp -s "$BUCKET/objects/fold/$HSTAMP-$ID1/$f.age" "$ORIG/$f.age" || fail "remote $f.age is not the local ciphertext"
    ! cmp -s "$BUCKET/objects/fold/$HSTAMP-$ID1/$f.age" "$ORIG/$f" || fail "the plain $f was sent"
  done
  ! grep -qE 's3api|s3 ls|head-object|get-object|delete' "$M/log" || fail "the client was used for more than a PUT: $(grep -E 's3api|s3 ls' "$M/log")"
  bucket_clean || fail "the bucket was listed, read or deleted from: $(cat "$BUCKET/forbidden")"
  # A same-minute run is refused locally, as before: sources unchanged, nothing sent.
  local sig
  sig="$(tree_sig "$ORIG" | grep -v LAST_OK)"
  rc=0; hb1 || rc=$?
  eq "$rc" 75 "a same-minute run"
  eq "$(tree_sig "$ORIG" | grep -v LAST_OK)" "$sig" "the pair after a refused run"
  eq "$(bucket_attempts)" 2 "the PUT invocations after a refused run"
  # A foreign complete folder of the same minute is not touched and does not move the stamp.
  host_two_setup host-offsite-foreign
  bucket_seed_pair "$HSTAMP" "$FID"
  sig="$(bucket_snapshot)"
  hb1 || fail "a run beside a foreign folder failed: $(cat "$FX/out")"
  [[ -f "$ORIG/fold-$HSTAMP.dump" ]] || fail "a foreign folder moved the stamp"
  bucket_check "$HSTAMP:$FID" "$HSTAMP:$ID1" || fail "keys beside a foreign folder: $(bucket_objects | tr '\n' ' ')"
  eq "$(bucket_snapshot | grep -- "-$FID/")" "$sig" "the foreign folder"
}

# Two deployments, one bucket, one minute, in either order and with the second between
# the first one's two PUTs.
case_host_offsite_interleave() {
  local snap f
  host_two_setup host-ab
  hb1 || fail "first: $(cat "$FX/out")"
  snap="$(bucket_snapshot)"
  hb2 || fail "second: $(cat "$FX/out2")"
  bucket_check "$HSTAMP:$ID1" "$HSTAMP:$ID2" || fail "keys, first then second: $(bucket_objects | tr '\n' ' ')"
  eq "$(bucket_snapshot | grep -- "-$ID1/")" "$snap" "the first deployment's objects"
  bucket_each_key_once || fail "a key was written twice, first then second"
  host_two_setup host-ba
  hb2 || fail "second: $(cat "$FX/out2")"
  snap="$(bucket_snapshot)"
  hb1 || fail "first: $(cat "$FX/out")"
  bucket_check "$HSTAMP:$ID1" "$HSTAMP:$ID2" || fail "keys, second then first: $(bucket_objects | tr '\n' ' ')"
  eq "$(bucket_snapshot | grep -- "-$ID2/")" "$snap" "the other deployment's objects"
  bucket_each_key_once || fail "a key was written twice, second then first"
  host_two_setup host-hook
  printf 'OD_ID=%q BUCKET_TAG=host2 bash %q --deploy-dir %q --backup-dir %q --env-file %q >%q 2>&1; echo $? >%q\n' \
    "$ID2" "$DIR2/backup.sh" "$DIR2" "$ORIG2" "$FX/full.env" "$FX/out2" "$FX/rc2" >"$BUCKET/hook"
  echo "2 hook" >>"$BUCKET/plan"
  hb1 || fail "first, with a run between its PUTs: $(cat "$FX/out")"
  eq "$(cat "$FX/rc2")" 0 "the run between the PUTs"
  bucket_check "$HSTAMP:$ID1" "$HSTAMP:$ID2" || fail "keys, with a run between the PUTs: $(bucket_objects | tr '\n' ' ')"
  bucket_each_key_once || fail "a key was written twice, with a run between the PUTs"
  eq "$(bucket_attempts)" 4 "the PUT invocations"
  for f in "fold-$HSTAMP.dump" "uploads-$HSTAMP.tgz"; do
    cmp -s "$BUCKET/objects/fold/$HSTAMP-$ID1/$f.age" "$ORIG/$f.age" || fail "first deployment's $f.age is not its own"
    cmp -s "$BUCKET/objects/fold/$HSTAMP-$ID2/$f.age" "$ORIG2/$f.age" || fail "second deployment's $f.age is not its own"
  done
  bucket_clean || fail "the bucket was listed, read or deleted from"
}

# A failed upload keeps the local pair, LAST_OK and every earlier object. A run that failed
# before its second member leaves one object in a folder nothing completes; one whose
# response was lost leaves a complete folder. The next run takes a folder of its own.
case_host_offsite_failures() {
  local rc half stamp2
  host_two_setup host-fail
  earlier_run
  bucket_plan_next 2 fail-before
  rc=0; hb1 "$ID1" || rc=$?
  eq "$rc" 1 "a run whose second member fails"
  eq "$(cat "$ORIG/LAST_OK")" "$LASTOK" "LAST_OK after a failed upload"
  [[ -f "$ORIG/fold-$HSTAMP.dump" && -f "$ORIG/uploads-$HSTAMP.tgz" ]] || fail "the local pair was not kept"
  grep -qF "offsite: s3://bkt/fold/$HSTAMP-$ID1/ via" "$FX/out" || fail "the folder is not logged"
  grep -qF 'the upload of uploads-' "$FX/out" || fail "the failure message is missing: $(cat "$FX/out")"
  grep -qF 'write objects under fold/' "$FX/out" || fail "the permission hint is missing"
  half="fold/$HSTAMP-$ID1/fold-$HSTAMP.dump.age"
  eq "$(bucket_objects)" "$(printf '%s\n%s\n' "$(bucket_expect "$HSTAMP0:$ID0")" "$half" | LC_ALL=C sort)" "the bucket after the failed run"
  hb2 || fail "the other deployment, the same minute: $(cat "$FX/out2")"
  eq "$(bucket_objects)" "$(printf '%s\n%s\n' "$(bucket_expect "$HSTAMP0:$ID0" "$HSTAMP:$ID2")" "$half" | LC_ALL=C sort)" "the bucket after the other deployment"
  bucket_each_key_once || fail "the earlier key was written again"
  # A response lost after the second member committed: a complete folder, a failed run, and
  # the next run (a minute later: the local stamp is taken) uses another folder.
  host_two_setup host-lost
  earlier_run
  bucket_plan_next 2 commit-then-fail
  rc=0; hb1 "$ID1" || rc=$?
  eq "$rc" 1 "a run whose second response is lost"
  eq "$(cat "$ORIG/LAST_OK")" "$LASTOK" "LAST_OK after a lost response"
  bucket_check "$HSTAMP0:$ID0" "$HSTAMP:$ID1" || fail "the committed folder: $(bucket_objects | tr '\n' ' ')"
  grep -qF "offsite: s3://bkt/fold/$HSTAMP-$ID1/ via" "$FX/out" || fail "the failed run's folder is not logged"
  echo $((NOW + 60)) >"$M/now"
  stamp2="$(/usr/bin/date -d "@$((NOW + 60))" +%Y%m%d-%H%M)"
  hb1 "$ID2" || fail "the next run: $(cat "$FX/out")"
  bucket_check "$HSTAMP0:$ID0" "$HSTAMP:$ID1" "$stamp2:$ID2" || fail "keys after the next run: $(bucket_objects | tr '\n' ' ')"
  bucket_each_key_once || fail "a key was written twice after a lost response"
}

# A run id that cannot be made well means no PUT: the local pair stays, the run fails,
# LAST_OK stays, and the expiry that follows every run still ran.
case_host_run_id_refused() {
  local mode n rc i=0 st
  host_two_setup host-runid
  earlier_run
  for mode in fail short nonhex upper empty; do
    i=$((i + 1))
    echo $((NOW + i * 60)) >"$M/now"
    st="$(/usr/bin/date -d "@$((NOW + i * 60))" +%Y%m%d-%H%M)"
    mk_age fold-20261003-0230.dump $((20 * DAY))
    n="$(bucket_attempts)"
    rc=0; OD_MODE=$mode hb1 || rc=$?
    eq "$rc" 1 "$mode: a run with no usable id"
    eq "$(bucket_attempts)" "$n" "$mode: the PUT invocations"
    eq "$(cat "$ORIG/LAST_OK")" "$LASTOK" "$mode: LAST_OK"
    grep -qF 'no random folder name could be made' "$FX/out" || fail "$mode: the message is missing: $(cat "$FX/out")"
    [[ -f "$ORIG/fold-$st.dump" && -f "$ORIG/uploads-$st.tgz" ]] || fail "$mode: the local pair was not kept"
    [[ ! -e "$ORIG/fold-20261003-0230.dump" ]] || fail "$mode: expiry did not run"
  done
}

# The uploads volume is observed before anything is dumped: present, absent (a listing that
# works and lacks it) and unknown (anything else) are three different answers, and only the
# first makes a backup. A pair without photos is never made, and never uploaded alone.
case_host_uploads_volume() {
  local rc before st
  host_two_setup host-volume
  earlier_run
  before="$(bucket_attempts)"
  echo $((NOW + 60)) >"$M/now"
  mk_age fold-20261003-0230.dump $((20 * DAY))
  : >"$M/log"
  touch "$M/uploads-absent"
  rc=0; hb1 || rc=$?
  eq "$rc" 1 "a run with the uploads volume absent"
  grep -qF 'does not exist' "$FX/out" || fail "absence is not named: $(cat "$FX/out")"
  ! grep -qF 'could not tell' "$FX/out" || fail "absence reads like an unknown"
  touch "$M/fail-ls"
  rc=0; hb1 || rc=$?
  eq "$rc" 1 "a run that cannot list volumes"
  grep -qF 'could not tell' "$FX/out" || fail "an unknown is not named: $(cat "$FX/out")"
  ! grep -qF 'does not exist' "$FX/out" || fail "an unknown reads like absence"
  rm "$M/fail-ls"; touch "$M/uploads-listed"
  rc=0; hb1 || rc=$?
  eq "$rc" 1 "a run whose volume is listed but cannot be inspected"
  grep -qF 'could not tell' "$FX/out" || fail "a listed but uninspectable volume is not an unknown: $(cat "$FX/out")"
  rm "$M/uploads-listed" "$M/uploads-absent"
  ! grep -q 'pg_dump' "$M/log" || fail "a dump was made without the uploads volume"
  st="$(/usr/bin/date -d "@$((NOW + 60))" +%Y%m%d-%H%M)"
  [[ ! -e "$ORIG/fold-$st.dump" && ! -e "$ORIG/uploads-$st.tgz" ]] || fail "an archive was published"
  eq "$(bucket_attempts)" "$before" "the PUT invocations"
  eq "$(cat "$ORIG/LAST_OK")" "$LASTOK" "LAST_OK"
  [[ ! -e "$ORIG/fold-20261003-0230.dump" ]] || fail "expiry did not run"
  # With the volume back, the same minute makes the pair and uploads both members.
  hb1 "$ID1" || fail "a run with the volume present: $(cat "$FX/out")"
  eq "$(bucket_attempts)" $((before + 2)) "the PUT invocations of the run with the volume"
}

# --- the real cutover ----------------------------------------------------------
# provision.sh runs its own main, so the cutover cannot be sourced; its three
# functions are cut out of the shipped file by name and run, unchanged, in a fresh
# `set -euo pipefail` shell against the stubbed Docker and real files. Only the
# directory of the cron file is redirected (it would otherwise be /etc/cron.d).
cat_cutover_script() {
  cat >"$FX/cutover.sh" <<'EOF2'
set -euo pipefail
source "$CHECKS"
for f in cutover_fail cutover_wait_lastok backup_cutover; do
  eval "$(awk -v n="$f" 'index($0, n "() {") == 1 { p = 1 } p { print } p && /^}/ { exit }' "$PROVISION")"
done
backup_cron_dir() { printf '%s/cron\n' "$FX"; }
TOOL=provision.sh DRY_RUN=0 HAS_BACKUP=1 CUTOVER_END=0 DEPLOY_DIR="$DIR" BACKUP_DIR="$ORIG"
backup_cutover
EOF2
}

run_cutover() { # [VAR=value]...: the exit status of one cutover; its output is in $FX/out
  cat_cutover_script
  env CHECKS="$CHECKS" PROVISION="$ROOT/deploy/digitalocean/provision.sh" FX="$FX" DIR="$DIR" ORIG="$ORIG" \
    CUTOVER_WAIT=30 "$@" bash "$FX/cutover.sh" >"$FX/out" 2>&1
}

cutover_setup() { # NAME: a deployment whose originals are ready to adopt
  fx_setup "$1"
  printf '#!/bin/bash\necho America/Los_Angeles\n' >"$FX/bin/timedatectl"; chmod +x "$FX/bin/timedatectl"
  mkf "$ORIG/fold-20261001-0230.dump" DUMP1 "$((NOW - 3 * DAY)).123456789"
  mkf "$ORIG/uploads-20261001-0230.tgz" TGZ1 "$((NOW - 3 * DAY)).5"
  echo "2020-01-01T00:00:00Z" >"$M/lastok"
  backup_cron_dir() { printf '%s/cron\n' "$FX"; }
  backup_cron_install "$DIR" full
}

case_cutover() {
  cutover_setup cutover
  local rc sig line_create line_start
  # An initial cutover: the volume does not exist yet, Compose creates it, the host
  # archives are adopted before the service starts, a fresh LAST_OK is seen, and
  # only then does the cron become expire-only.
  rm -rf "$DOCKER_ROOT/volumes/fold_backups"; touch "$M/volume-absent"
  touch "$M/start-lastok"
  sig="$(tree_sig "$ORIG")"
  rc=0; run_cutover || rc=$?
  eq "$rc" 0 "a successful initial cutover: $(cat "$FX/out")"
  eq "$(backup_cron_kind "$DIR")" expire "the cron after a successful cutover"
  grep -q 'expire-only' "$FX/cron/fold-backup" || fail "the installed cron does not expire only"
  grep -q 'scheduler: container backup service' "$FX/out" || fail "the scheduler was not announced"
  line_create="$(grep -n 'up --no-start --no-deps backup' "$M/log" | head -n 1 | cut -d: -f1)"
  line_start="$(grep -n ' up -d --no-deps backup' "$M/log" | head -n 1 | cut -d: -f1)"
  [[ -n "$line_create" && -n "$line_start" ]] && (( line_create < line_start )) || fail "the volume was not created before the start: $(cat "$M/log")"
  eq "$(sort "$M/adopted-at-start" | tr '\n' ' ')" "fold-20261001-0230.dump uploads-20261001-0230.tgz " "what was adopted when the service started"
  eq "$(stat -c '%u:%g:%a' "$MOUNT")" "0:65532:1770" "the created volume root"
  eq "$(tree_sig "$ORIG")" "$sig" "the originals"
  eq "$(env_value "$DIR/.env" FOLD_BACKUP_TZ)" America/Los_Angeles "the copied timezone"
  eq "$(stat -c '%a' "$DIR/.env")" 600 "the .env mode"

  # A repeat run: the cron is already expire-only. The service is started again
  # and must prove itself with a fresh LAST_OK again.
  echo "2020-01-01T00:00:00Z" >"$M/lastok"; rm -f "$M/started"
  rc=0; run_cutover || rc=$?
  eq "$rc" 0 "a successful repeat cutover"
  eq "$(backup_cron_kind "$DIR")" expire "the cron after a repeat cutover"
  [[ -e "$M/started" ]] || fail "a repeat cutover did not start the service"
  # The replacement service fails to be created: exit 77, and the host cron produces again.
  touch "$M/fail-start"
  rc=0; run_cutover || rc=$?
  eq "$rc" 77 "a repeat cutover whose service did not start"
  eq "$(backup_cron_kind "$DIR")" full "the cron after a failed repeat cutover (a producer must remain)"
  grep -q 'host backup is in charge' "$FX/out" || fail "the failure does not say who is in charge: $(cat "$FX/out")"
  grep -q ' stop backup' "$M/log" || fail "the earlier service was left running beside the host cron"
  [[ ! -e "$M/started" ]] || fail "the earlier service still runs after the host was confirmed in charge"
  rm "$M/fail-start"
  # The service starts but never reports a new success (it fails its configuration):
  # an expire-only cron from an earlier cutover is no reason to believe it.
  rc=0; run_cutover || rc=$?
  eq "$rc" 0 "the retry once the service starts"
  eq "$(backup_cron_kind "$DIR")" expire "the cron after the retry"
  rm "$M/start-lastok"; echo "2020-01-01T00:00:00Z" >"$M/lastok"
  rc=0; run_cutover CUTOVER_WAIT=5 || rc=$?
  eq "$rc" 77 "a repeat cutover whose service reports no new success"
  eq "$(backup_cron_kind "$DIR")" full "the cron after a service that never succeeded"
  # The same on a first cutover leaves the full cron it started with.
  rc=0; run_cutover CUTOVER_WAIT=5 || rc=$?
  eq "$rc" 77 "an initial cutover whose service reports no new success"
  eq "$(backup_cron_kind "$DIR")" full "the cron after an initial failure"
  [[ ! -e "$M/started" ]] || fail "a service that never succeeded still runs beside the host cron"
}

# A cutover that cannot confirm the host backup says so with its own exit (78), never
# the confirmed fallback's (77): the service that will not stop is left running (and
# the host backup refuses beside it), and a cron that cannot be flushed leaves the
# service alone.
case_cutover_unconfirmed() {
  cutover_setup cutover-unconfirmed
  local rc
  # The replacement service fails to start and the earlier one cannot be stopped.
  touch "$M/start-lastok"; rc=0; run_cutover || rc=$?
  eq "$rc" 0 "the first cutover"
  touch "$M/fail-start" "$M/fail-stop"
  rc=0; run_cutover || rc=$?
  eq "$rc" 78 "a cutover whose service cannot be stopped"
  eq "$(backup_cron_kind "$DIR")" full "the cron when the service could not be stopped"
  grep -q 'NOT confirmed' "$FX/out" && grep -q 'could not be confirmed stopped' "$FX/out" || fail "the service is not named as unknown: $(cat "$FX/out")"
  [[ -e "$M/started" ]] || fail "the stub should still run the service"
  rm "$M/fail-stop"
  rc=0; run_cutover || rc=$?
  eq "$rc" 77 "the same failure once the service can be stopped"
  # The full cron cannot be flushed: nothing is confirmed and the service is left alone.
  touch "$M/start-lastok"; rm -f "$M/fail-start"; rc=0; run_cutover || rc=$?
  eq "$rc" 0 "the cutover that restores the service"
  touch "$M/fail-start"; : >"$M/log"
  stub_cmd sync <<'STUB'
case "$*" in *"/cron") exit 1 ;; esac
exec "$REAL" "$@"
STUB
  rc=0; run_cutover || rc=$?
  eq "$rc" 78 "a cutover whose full cron cannot be flushed"
  grep -q 'could not be installed and flushed' "$FX/out" || fail "the cron is not named as unknown: $(cat "$FX/out")"
  ! grep -q ' stop backup' "$M/log" || fail "the service was stopped though no scheduler was confirmed"
  [[ -e "$M/started" ]] || fail "the service was stopped though no scheduler was confirmed"
  unstub sync
}

# The caller. upgrade.sh's own run_provision runs the real cutover as its provision.sh
# and reports what it answered: a confirmed fallback and an unconfirmed one are not the
# same message, and only the first may say the host backup is making the backups.
case_upgrade_handoff() {
  cutover_setup upgrade-handoff
  cat_cutover_script
  cat >"$FX/provision-stub.sh" <<'STUB'
#!/bin/bash
exec env CHECKS="$FX_CHECKS" PROVISION="$FX_PROVISION" FX="$FX_DIR" DIR="$FX_DEPLOY" ORIG="$FX_ORIG" CUTOVER_WAIT=5 bash "$FX_DIR/cutover.sh"
STUB
  chmod +x "$FX/provision-stub.sh"
  cat >"$FX/upgrade-run.sh" <<'STUB'
source "$CHECKS"
eval "$(awk 'index($0, "run_provision() {") == 1 { p = 1 } p { print } p && /^}/ { exit }' "$UPGRADE")"
TOOL=upgrade.sh DRY_RUN=0 DEPLOY_DIR="$DEPLOY" DOMAIN=groups.example.org SOURCE_DIR=/srv/fold PROVISION="$FX/provision-stub.sh"
run_provision image ghcr.io/example/fold:v1
STUB
  upgrade_run() {
    env CHECKS="$CHECKS" UPGRADE="$ROOT/deploy/digitalocean/upgrade.sh" FX="$FX" DEPLOY="$DIR" \
      FX_CHECKS="$CHECKS" FX_PROVISION="$ROOT/deploy/digitalocean/provision.sh" FX_DIR="$FX" FX_DEPLOY="$DIR" FX_ORIG="$ORIG" \
      bash "$FX/upgrade-run.sh" >"$FX/up.out" 2>&1
  }
  local rc
  touch "$M/start-lastok"; rc=0; upgrade_run || rc=$?
  eq "$rc" 0 "an upgrade whose cutover finishes: $(cat "$FX/up.out")"
  # A failed repeat cutover with the earlier service stopped and the cron flushed: confirmed.
  touch "$M/fail-start"
  rc=0; upgrade_run || rc=$?
  eq "$rc" 77 "an upgrade whose cutover fell back"
  grep -q 'confirmed the host backup is in charge' "$FX/up.out" && ! grep -q 'NOT' "$FX/up.out" || fail "a confirmed fallback is not reported as one: $(cat "$FX/up.out")"
  # The cron cannot be installed (the sync of its folder fails): unconfirmed, and the caller says so.
  backup_cron_install "$DIR" expire
  stub_cmd sync <<'STUB'
case "$*" in *"/cron") exit 1 ;; esac
exec "$REAL" "$@"
STUB
  rc=0; upgrade_run || rc=$?
  eq "$rc" 78 "an upgrade whose fallback cron could not be installed"
  grep -q 'could NOT confirm any backup' "$FX/up.out" || fail "an unconfirmed fallback is not reported as one: $(cat "$FX/up.out")"
  ! grep -q 'confirmed the host backup is in charge\|the host cron still makes' "$FX/up.out" || fail "an unconfirmed fallback claims the host makes the backups: $(cat "$FX/up.out")"
  unstub sync
  # upgrade.sh answers each exit as its own.
  grep -q 'rc == 78' "$ROOT/deploy/digitalocean/upgrade.sh" || fail "upgrade.sh does not tell exit 78 from 77"
}

# R32: the cron is published durably. The staged file is synchronized before it replaces
# the old one and its folder after; an identical file in place is synchronized before the
# install is confirmed; any failed step is a failed install.
case_cron_durable() {
  fx_setup cron-durable
  backup_cron_dir() { printf '%s/cron\n' "$FX"; }
  local ops="$M/ops" rc cron="$FX/cron"
  : >"$ops"
  for c in sync mv; do
    stub_cmd "$c" <<'STUB'
n="$(basename "$0")"
printf '%s %s\n' "$n" "$*" >>"$FX_MODEL/ops"
if [[ -e "$FX_MODEL/fail-$n" ]] && [[ "$* " == $(cat "$FX_MODEL/fail-$n") ]]; then exit 1; fi
exec "$REAL" "$@"
STUB
  done
  line() { sed -n "${1}p" "$ops"; }
  # A new file: staged copy, then the rename, then its folder.
  backup_cron_install "$DIR" expire || fail "a first install failed"
  [[ "$(line 1)" == "sync -- $cron/.tmp."* && "$(line 2)" == "mv -f $cron/.tmp."*" $cron/fold-backup" && "$(line 3)" == "sync -- $cron" ]] \
    || fail "the publication order is not sync file, rename, sync folder: $(cat "$ops")"
  eq "$(wc -l <"$ops" | tr -d ' ')" 3 "the calls of a first install"
  # Replacing the expire-only cron is the fallback's install: the same order.
  : >"$ops"
  backup_cron_install "$DIR" full || fail "the fallback install failed"
  [[ "$(line 1)" == "sync -- $cron/.tmp."* && "$(line 2)" == "mv -f "* && "$(line 3)" == "sync -- $cron" ]] || fail "replacement order: $(cat "$ops")"
  eq "$(backup_cron_kind "$DIR")" full "the installed cron"
  # An identical file in place is synchronized, with its folder, and not renamed.
  : >"$ops"
  backup_cron_install "$DIR" full || fail "an identical install failed"
  [[ "$(line 1)" == "sync -- $cron/fold-backup" && "$(line 2)" == "sync -- $cron" ]] || fail "an identical install is not synchronized: $(cat "$ops")"
  ! grep -q '^mv ' "$ops" || fail "an identical file was renamed"
  # A failure at each step is a failed install; a failed stage or rename leaves the old file whole.
  echo '*/.tmp.*' >"$M/fail-sync"
  rc=0; backup_cron_install "$DIR" expire || rc=$?
  (( rc != 0 )) || fail "a failed staging sync was reported as installed"
  eq "$(backup_cron_kind "$DIR")" full "the cron after a failed staging sync"
  [[ -z "$(find "$cron" -name '.tmp.*')" ]] || fail "a failed install left its staging file"
  rm "$M/fail-sync"; echo '* *' >"$M/fail-mv"
  rc=0; backup_cron_install "$DIR" expire || rc=$?
  (( rc != 0 )) || fail "a failed rename was reported as installed"
  eq "$(backup_cron_kind "$DIR")" full "the cron after a failed rename"
  [[ -z "$(find "$cron" -name '.tmp.*')" ]] || fail "a failed rename left its staging file"
  rm "$M/fail-mv"; echo "-- $cron " >"$M/fail-sync"
  rc=0; backup_cron_install "$DIR" expire || rc=$?
  (( rc != 0 )) || fail "a failed folder sync was reported as installed"
  rm "$M/fail-sync"; backup_cron_install "$DIR" full
  echo "-- $cron/fold-backup " >"$M/fail-sync"
  rc=0; backup_cron_install "$DIR" full || rc=$?
  (( rc != 0 )) || fail "an identical file whose sync failed was reported as installed"
  echo "-- $cron " >"$M/fail-sync"
  rc=0; backup_cron_install "$DIR" full || rc=$?
  (( rc != 0 )) || fail "an identical file whose folder sync failed was reported as installed"
  unstub sync mv
}

# One producer per deployment. While the container backup service runs, the host script
# makes no backup, whatever started the service, so a night does not get two pairs; a
# failed cutover stops the service before the host is called the producer. The bucket has
# no part in it: each run uploads into a folder of its own.
case_single_producer() {
  cutover_setup single-producer
  mkdir -p "$DIR/bin"
  cp "$ROOT/deploy/digitalocean/checks.sh" "$ROOT/deploy/digitalocean/backup.sh" "$DIR/"
  printf 'FOLD_BACKUP_KEEP_DAYS=14\nFOLD_BACKUP_S3_BUCKET=bkt\nFOLD_BACKUP_S3_ENDPOINT=https://s3.example\nFOLD_BACKUP_S3_REGION=r-1\nAWS_ACCESS_KEY_ID=A\nAWS_SECRET_ACCESS_KEY=S\nFOLD_BACKUP_AGE_RECIPIENT=age1abc\n' >"$FX/full.env"
  hbf() { bash "$DIR/backup.sh" --deploy-dir "$DIR" --backup-dir "$ORIG" --env-file "$FX/full.env"; }
  local rc stamp sig
  stamp="$(/usr/bin/date -d "@$NOW" +%Y%m%d-%H%M)"
  sig="$(tree_sig "$ORIG")"
  touch "$M/started"
  echo 'HOST-DUMP-DIFFERS' >"$M/dump-bytes"
  rc=0; hbf >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 75 "a host run beside a running service"
  grep -q 'container backup service is running' "$FX/out" || fail "the refusal does not say why: $(cat "$FX/out")"
  eq "$(tree_sig "$ORIG")" "$sig" "the host's archives after a refused run"
  eq "$(bucket_attempts)" 0 "the PUT invocations of a refused run"
  # A service state that cannot be observed is no leave to produce.
  touch "$M/fail-ps"; rc=0; hbf >"$FX/out" 2>&1 || rc=$?
  eq "$rc" 1 "a host run that cannot tell whether the service runs"
  eq "$(tree_sig "$ORIG")" "$sig" "the host's archives after an unobservable service"
  eq "$(bucket_attempts)" 0 "the PUT invocations without knowing the service"
  rm "$M/fail-ps"
  # A failed cutover stops the service first. Then the host is the one producer.
  backup_cron_install "$DIR" expire
  touch "$M/fail-start"
  rc=0; run_cutover CUTOVER_WAIT=5 || rc=$?
  eq "$rc" 77 "a cutover that fell back"
  [[ ! -e "$M/started" ]] || fail "the service runs beside the host after the fallback was confirmed"
  eq "$(backup_cron_kind "$DIR")" full "the cron after the fallback"
  hbf >"$FX/out" 2>&1 || fail "a host run with the service stopped: $(cat "$FX/out")"
  bucket_check "$stamp:$ID1" || fail "the host's pair after the fallback: $(bucket_objects | tr '\n' ' ')"
}

# A stamp refused for a differing copy is reported and is not a failure; an
# unusable service identity or path refuses before anything is started.
case_cutover_refusals() {
  cutover_setup cutover-refusals
  local rc
  resolve_and_layout
  mkf "$AD/fold-20261001-0230.dump" DIFFERENT "$((NOW - 3 * DAY))"
  touch "$M/start-lastok"
  rc=0; run_cutover || rc=$?
  eq "$rc" 0 "a cutover with a refused stamp: $(cat "$FX/out")"
  grep -q 'stamp 20261001-0230 was not adopted' "$FX/out" || fail "the refused stamp was not reported"
  eq "$(backup_cron_kind "$DIR")" expire "the cron after a cutover with a refused stamp"
  eq "$(cat "$AD/fold-20261001-0230.dump")" DIFFERENT "a refused stamp's copy"
  [[ ! -e "$AD/uploads-20261001-0230.tgz" ]] || fail "a sibling was filled beside a refused stamp"
  # Overrides the host cannot validate stop the cutover before any start.
  backup_cron_install "$DIR" full
  local v
  for v in '.services.backup.environment.FOLD_BACKUP_ARCHIVE_DIR="/tmp"' '.services.backup.volumes|=map(select(.target!="/backups"))' \
    '.services.backup.volumes|=map(if .target=="/backups" then .source="scratch" else . end)' '.services.backup.user="0:0"' \
    '.services.backup.volumes+=[{type:"bind",source:"/tmp/empty",target:"/data/uploads",read_only:true}]' \
    '.services.backup.volumes|=map(if .target=="/backups" then .volume={subpath:"x"} else . end)'; do
    jq "$v" "$M/config.json" >"$M/c"; cp "$M/config.json" "$M/good"; cp "$M/c" "$M/config.json"
    : >"$M/log"; rm -f "$M/started"
    rc=0; run_cutover || rc=$?
    eq "$rc" 77 "a cutover with $v"
    ! grep -q ' up ' "$M/log" || fail "the service was started with $v"
    [[ ! -e "$M/started" ]] || fail "the service was started with $v"
    eq "$(backup_cron_kind "$DIR")" full "the cron after a refusal ($v)"
    cp "$M/good" "$M/config.json"
  done
}

# start_stack, cut out of the shipped file and run unchanged, starts everything but
# backup. With an override that makes the app depend on backup, and an identity the
# cutover would refuse, nothing may start the backup service: Compose would start it
# as a dependency of any selection that lacks --no-deps.
case_start_stack_no_backup() {
  fx_setup start-stack
  local rc
  cat >"$FX/start-stack.sh" <<'EOF2'
set -euo pipefail
source "$CHECKS"
eval "$(awk 'index($0, "start_stack() {") == 1 { p = 1 } p { print } p && /^}/ { exit }' "$PROVISION")"
step() { printf '==> %q' "$@"; echo; "$@"; }
die() { echo "$*" >&2; exit 1; }
DRY_RUN=0 HAS_BACKUP=1 DEPLOY_DIR="$DIR" DEPLOY_DRILL=
start_stack
EOF2
  run_start_stack() {
    env CHECKS="$CHECKS" PROVISION="$ROOT/deploy/digitalocean/provision.sh" DIR="$DIR" bash "$FX/start-stack.sh" >"$FX/out" 2>&1
  }
  jq '.services.app.depends_on={backup:{condition:"service_started"}}|.services.backup.user="0:0"' "$M/config.json" >"$M/c"
  mv "$M/c" "$M/config.json"
  : >"$M/log"
  rc=0; run_start_stack || rc=$?
  eq "$rc" 0 "start_stack with the app depending on a backup service of the wrong identity: $(cat "$FX/out")"
  grep -q 'up -d --no-deps app$' "$M/log" || fail "the stack was not started with the whole non-backup selection: $(cat "$M/log")"
  [[ -e "$M/up-without-backup" ]] || fail "the other services were not started"
  [[ ! -e "$M/started" ]] || fail "backup started as a dependency of the stack"
  [[ ! -e "$M/adopted-at-start" ]] || fail "backup started before the adoption"
  # The stub models Compose: the same selection without --no-deps does start backup.
  : >"$M/log"
  docker compose --project-directory "$DIR" up -d app >/dev/null
  [[ -e "$M/started" ]] || fail "the model does not start a dependency, so this case proves nothing"
  rm -f "$M/started" "$M/adopted-at-start"
  # A Compose configuration that cannot be read, or that lists nothing to start, starts nothing.
  touch "$M/fail-config"; : >"$M/log"
  rc=0; run_start_stack || rc=$?
  (( rc != 0 )) || fail "start_stack went on without a readable configuration"
  ! grep -q ' up ' "$M/log" || fail "a service started without a readable configuration"
  rm "$M/fail-config"
  jq 'del(.services.app)' "$M/config.json" >"$M/c"; mv "$M/c" "$M/config.json"
  rc=0; run_start_stack || rc=$?
  (( rc != 0 )) || fail "start_stack went on with nothing to start"
  grep -q 'no service besides backup' "$FX/out" || fail "the empty selection is not named: $(cat "$FX/out")"
  ! grep -q ' up ' "$M/log" || fail "a service started with an empty selection"
}

# Every Docker observation of a cutover shares one FOLD_CUTOVER_WAIT: the identity
# and coordination reads, the volume resolution and the scheduler's own read are
# cut off at what is left of it, never at a fresh FOLD_BACKUP_DOCKER_WAIT (60).
case_cutover_deadline() {
  cutover_setup cutover-deadline
  local rc started what k
  touch "$M/start-lastok"
  # WHAT (the Docker word that stalls) and K (how many of its calls pass first).
  for what in "config 0" "config 2" "config 3" "ls 0"; do
    k="${what#* }"; what="${what%% *}"
    rm -f "$M/slow.n" "$M/started"; backup_cron_install "$DIR" full
    echo "$what" >"$M/slow-for"; echo "$k" >"$M/slow-after"; echo 40 >"$M/slow"
    started=$SECONDS
    rc=0; run_cutover CUTOVER_WAIT=4 || rc=$?
    rm -f "$M/slow" "$M/slow-for" "$M/slow-after"
    eq "$rc" 77 "a cutover stalled on '$what' after $k calls"
    (( SECONDS - started < 20 )) || fail "a cutover stalled on '$what' after $k calls outlived its wait ($((SECONDS - started))s)"
    eq "$(backup_cron_kind "$DIR")" full "the cron after a stalled '$what'"
  done
  # The start itself is held to the same budget.
  rm -f "$M/started"; echo up >"$M/slow-for"; echo 0 >"$M/slow-after"; echo 40 >"$M/slow"; rm -f "$M/slow.n"
  started=$SECONDS
  rc=0; run_cutover CUTOVER_WAIT=4 || rc=$?
  rm -f "$M/slow" "$M/slow-for" "$M/slow-after"
  eq "$rc" 77 "a cutover whose start stalls"
  (( SECONDS - started < 20 )) || fail "a stalled start outlived the cutover's wait"
}

# Static guards over the shipped files: order of the cutover, no destructive or
# test-only commands, and the image carries no adopt command. (What the cutover
# does is run for real by case_cutover and its siblings.)
case_source_guards() {
  local prov="$ROOT/deploy/digitalocean/provision.sh" chk="$ROOT/deploy/digitalocean/checks.sh"
  local up="$ROOT/deploy/digitalocean/upgrade.sh" bk="$ROOT/deploy/digitalocean/backup.sh"
  local la ad st create
  la="$(grep -n 'archive_layout || cutover_fail' "$prov" | head -n 1 | cut -d: -f1)"
  ad="$(grep -n 'archive_adopt "\$BACKUP_DIR" || cutover_fail' "$prov" | head -n 1 | cut -d: -f1)"
  st="$(grep -n 'archive_compose "\$DEPLOY_DIR" up -d --no-deps backup' "$prov" | head -n 1 | cut -d: -f1)"
  create="$(grep -n 'archive_compose "\$DEPLOY_DIR" up --no-start --no-deps backup' "$prov" | head -n 1 | cut -d: -f1)"
  [[ -n "$la" && -n "$ad" && -n "$st" && -n "$create" ]] || fail "the cutover steps are not all there"
  (( create < la && la < ad && ad < st )) || fail "the cutover is out of order (create $create, layout $la, adopt $ad, start $st)"
  [[ "$(grep -c 'archive_compose "\$DEPLOY_DIR" up --no-start --no-deps backup' "$prov")" == 1 ]] || fail "the backup resources are created in more than one place"
  [[ "$(grep -c 'up -d --no-deps "\${svc\[@\]}"' "$prov")" == 1 ]] || fail "start_stack does not name its services with --no-deps"
  [[ "$(grep -c 'archive_compose "\$DEPLOY_DIR" up -d --no-deps backup' "$prov")" == 1 ]] || fail "the cutover starts the service in more than one place"
  # The cron switch needs a new LAST_OK first, and the whole cutover leaves pin-only alone.
  grep -q 'cutover_wait_lastok "\$attempt"' "$prov" || fail "the cron switch does not wait for LAST_OK"
  grep -q 'ok > attempt' "$prov" || fail "LAST_OK is not compared with this attempt"
  # The wait is unconditional (a repeat run's recreated service must prove itself too),
  # and the scheduler is asked with its directory (a missing argument aborts under set -u).
  ! grep -q 'cron_kind.*!= expire' "$prov" || fail "the fresh LAST_OK is skipped for an earlier expire-only cron"
  grep -q 'backup_scheduler_select cutover-success "\$DEPLOY_DIR" || cutover_fail' "$prov" || fail "the cron switch is not asked with its directory"
  grep -q 'ARCHIVE_CAP=\$CUTOVER_END' "$prov" || fail "the cutover does not cap its Docker observations"
  local pinned call
  pinned="$(grep -n 'pinned only; no services started' "$prov" | head -n 1 | cut -d: -f1)"
  call="$(grep -n '^    backup_cutover$' "$prov" | head -n 1 | cut -d: -f1)"
  [[ -n "$pinned" && -n "$call" ]] && (( pinned < call )) || fail "the cutover is reachable by a pin-only run"
  # Nothing in the host tools stops, kills, removes or creates a Docker resource directly.
  if grep -nE 'docker (stop|kill|rm|volume (create|rm)|network)' "$prov" "$chk" "$up" "$bk" | grep -vE '^[^:]+:[0-9]+:[[:space:]]*#'; then
    fail "a host tool stops, kills or removes a Docker resource"
  fi
  # The one Compose stop is the failed cutover's, so the host and the service never both produce.
  [[ "$(grep -hE 'compose "\$dir" stop backup|archive_compose .* stop ' "$prov" "$chk" "$up" "$bk" | grep -vcE '^[[:space:]]*#')" == 1 ]] \
    || fail "the backup service is stopped in more than one place"
  grep -q 'backup_service_halt "\$DEPLOY_DIR"' "$prov" || fail "a failed cutover does not stop the service"
  grep -q 'backup_service_running "\$DEPLOY_DIR"' "$bk" || fail "the host backup does not refuse beside a running service"
  # The producer never reads, lists or heads the bucket and writes only under its own folder.
  bucket_selftest || fail "the bucket model's own checks failed"
  bucket_source_guard "$bk" 'fold/${stamp}-${RUN_ID}/' \
    || fail "the host producer lists, reads or heads the bucket, writes a flat key, or has no run folder"
  # No test hook: the archive section and the host scripts read no fixture variables.
  if grep -nE '\b(FIXTURE_|STUB_|FX_)' "$chk" "$bk" "$up" "$prov"; then fail "a test hook is in a shipped script"; fi
  # The identity rule is one function with no lenient mode; every start asks it
  # first, and a failure after the site is up is the cutover's exit 77.
  ! grep -nE 'backup_identity_check[^|]*(strict|lenient)' "$prov" "$chk" || fail "the identity check has a second mode"
  grep -q 'backup_identity_check "\$dir" || return 1' "$chk" || fail "the recovery scheduler does not check the identity"
  grep -q 'backup_identity_check "\$DEPLOY_DIR" || cutover_fail' "$prov" || fail "the cutover does not check the identity"
  grep -q 'backup_identity_check "\$DEPLOY_DIR" || die' "$prov" || fail "the drill's start does not check the identity"
  grep -qE 'backup cutover stopped.* 77$' "$prov" || fail "a confirmed fallback does not exit 77"
  grep -qE 'backup cutover stopped.* 78$' "$prov" || fail "an unconfirmed fallback does not exit 78"
  grep -q 'rc == 77' "$up" || fail "upgrade.sh does not report a cutover failure apart from a failed deploy"
  grep -q 'rc == 78' "$up" || fail "upgrade.sh does not report an unconfirmed fallback apart from a confirmed one"
  # The image has no adopt command, no legacy mount, and the volume root is protected.
  if grep -nE '(^|[^_a-z])adopt([^_a-z]|$)|LEGACY|/legacy' "$ROOT/deploy/backup/backup.sh" "$ROOT/deploy/backup/Dockerfile" "$ROOT/docker-compose.yml"; then
    fail "the image or Compose still names the adopt command"
  fi
  grep -q 'chmod 1770 /backups' "$ROOT/deploy/backup/Dockerfile" || fail "the image does not create /backups as 1770"
  grep -q 'user: "65532:65532"' "$ROOT/docker-compose.yml" || fail "Compose has no explicit backup identity"
  # The pending file and the adopted folder are kept through a provision copy.
  grep -q 'backup-expiry-pending' "$prov" || fail "provision may prune the pending decisions"
}

CASES=(volume_resolution layout service_isolation adoption adoption_faults lock_inheritance retention expiry_faults
  expiry_decisions expiry_crashes adoption_crashes service_archives host_backup host_backup_offsite host_backup_crashes export settings
  identity recovery_volume cutover cutover_refusals cutover_unconfirmed cutover_deadline start_stack_no_backup upgrade_handoff cron_durable
  host_offsite host_offsite_interleave host_offsite_failures host_run_id_refused host_uploads_volume
  single_producer lastok source_guards)

run_case() {
  local rc=0
  echo "== $1"
  # errexit is off around the subshell and on inside it: a subshell on the left of
  # `||` would ignore it, and a failed command must stop its case.
  set +e
  ( set -e; "case_$1" )
  rc=$?
  set -e
  if (( rc != 0 )); then echo "-- FAILED: $1" >&2; return 1; fi
  echo "-- ok: $1"
}

main() {
  local c rc=0
  if [[ "${1:-}" == --only ]]; then run_case "${2:?--only needs a case}"; return $?; fi
  for c in "${CASES[@]}"; do run_case "$c" || rc=1; done
  if (( rc == 0 )); then echo "backup-archives-fixtures: all cases passed"; else echo "backup-archives-fixtures: FAILURES" >&2; fi
  return "$rc"
}

main "$@"
