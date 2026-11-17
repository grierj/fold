#!/usr/bin/env bash
# fold-backup: portable backups of Fold's database and uploads, run inside the
# fold-backup image (see deploy/backup/README.md for the full contract).
#
# Subcommands: loop, now, restore-test, list, health, uploads-validate,
# uploads-stage, uploads-observe, uploads-exchange, uploads-drop, --self-test,
# --help. Maintenance subcommands never start a backup. Everything here works on
# top-level files of /backups only; the host copies of older archives live in a
# root-only folder this service cannot open and is never told about.
#
# Secrets (PGPASSWORD, the S3 keys) are read from the environment and never
# appear in argv, logs or status files. Status files name only fixed words.
set -euo pipefail
umask 077
export LC_ALL=C

SELF="$(readlink -f "${BASH_SOURCE[0]}")"

# Paths. The environment overrides are test seams; the container uses the defaults.
APP_UID="${FOLD_BACKUP_APP_UID:-65532}"
APP_GID="${FOLD_BACKUP_APP_GID:-65532}"
ARCHIVE_DIR="${FOLD_BACKUP_ARCHIVE_DIR:-/backups}"
STATUS_DIR="${FOLD_BACKUP_STATUS_DIR:-/backup-status}"
DATA_DIR="${FOLD_BACKUP_DATA_DIR:-/data}"
RESTORE_ARCHIVE="${FOLD_BACKUP_RESTORE_ARCHIVE:-/restore/archive.tgz}"
HEARTBEAT="${FOLD_BACKUP_HEARTBEAT:-/tmp/fold-backup-heartbeat}"
ZONEINFO="${FOLD_BACKUP_ZONEINFO:-/usr/share/zoneinfo}"
TICK_SECS="${FOLD_BACKUP_TICK_SECS:-60}"
POLL_SECS="${FOLD_BACKUP_POLL_SECS:-2}"
HEALTH_MAX_AGE="${FOLD_BACKUP_HEALTH_MAX_AGE:-120}"

export PGHOST="${PGHOST:-db}"
export PGUSER="${PGUSER:-church}"
export PGDATABASE="${PGDATABASE:-fold}"
export PGCONNECT_TIMEOUT="${PGCONNECT_TIMEOUT:-10}"
export HOME="${HOME:-/tmp}"

SCRATCH_DB="fold_backup_scratch"
OFFSITE_VARS=(FOLD_BACKUP_S3_BUCKET FOLD_BACKUP_S3_ENDPOINT FOLD_BACKUP_S3_REGION
  AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY FOLD_BACKUP_AGE_RECIPIENT)

Q_RESTORE_DBS="select datname from pg_database where datname ~ '^fold_(pre_)?restore_'"
Q_TABLES="select count(*) from information_schema.tables where table_schema = 'public' and table_type = 'BASE TABLE'"
Q_USERS="select count(*) from users"
Q_KEYS="select avatar_key from users where avatar_key is not null and avatar_key <> ''"
Q_IDS="select id from users"

TMPS=()
RUN_TMP=""
CUR_PID=""""
CHILD=""
SLEEP_PID=""
SCRATCH_MADE=0
STAMP=""
RUN_ID=""
REMOTE_PREFIX=""
REMOTE_FIELD=""
TZNAME="UTC"
KEEP_DAYS=14
OFFSITE=0
NONCE=""

usage() {
  cat <<'EOF'
Usage: backup.sh COMMAND [OPTIONS]

  loop              run nightly backups (the default service command)
  now               run one backup now; prints one "published ..." line on success
                    and exits 75 when another operation or a recovery holds it
  restore-test      restore the newest complete pair into a scratch database
  list              list the recognized archives
  health            exit 0 while the loop is alive
  uploads-validate  --nonce N   check /restore/archive.tgz without creating anything
  uploads-stage     --nonce N   extract it into /data/.restore-N/uploads
  uploads-observe   --nonce N   print live and staged identities
  uploads-exchange  --nonce N [--expect-live I --expect-staged J] | --probe
  uploads-drop      --nonce N --identity I | --release
  --self-test       check the tools and the pure rules
  --help            this text

Settings come from the environment: PGPASSWORD, FOLD_BACKUP_TZ (default UTC),
FOLD_BACKUP_KEEP_DAYS (1 to 365, default 14), FOLD_RECOVERY_DIR (host
coordination), and either all or none of FOLD_BACKUP_S3_BUCKET,
FOLD_BACKUP_S3_ENDPOINT, FOLD_BACKUP_S3_REGION, AWS_ACCESS_KEY_ID,
AWS_SECRET_ACCESS_KEY and FOLD_BACKUP_AGE_RECIPIENT.
EOF
}

log() { printf 'backup: %s\n' "$*" >&2; }
die() { log "$1"; exit "${2:-1}"; }
usage_error() { log "${1:-bad usage}"; usage >&2; exit 64; }
refuse() { log "$1; no backup work was attempted"; exit 75; }

# ---------------------------------------------------------------- processes

# ext COMMAND...: run an external command so that a trapped TERM can stop it,
# without the lock descriptors (6 host, 7 archive) leaking into it.
ext() {
  "$@" 6<&- 7<&- <&0 &
  CUR_PID=$!
  local rc=0
  wait "$CUR_PID" || rc=$?
  CUR_PID=""
  return "$rc"
}

cleanup_exit() {
  local f
  if (( SCRATCH_MADE )); then
    SCRATCH_MADE=0
    ext psql -X -q -At -d postgres -c "DROP DATABASE IF EXISTS $SCRATCH_DB WITH (FORCE)" >/dev/null 2>&1 || true
  fi
  for f in "${TMPS[@]}"; do
    rm -rf -- "$f" 2>/dev/null || true
  done
}

on_term() {
  trap - TERM INT
  if [[ -n "$CUR_PID" ]]; then
    kill -TERM "$CUR_PID" 2>/dev/null || true
    wait "$CUR_PID" 2>/dev/null || true
    CUR_PID=""
  fi
  log "interrupted; nothing further was published"
  exit 143
}

install_traps() {
  trap on_term TERM INT
  trap cleanup_exit EXIT
}

own_app() {
  if [[ "$(id -u)" == 0 ]]; then
    chown "$APP_UID:$APP_GID" "$@"
  fi
}

# run_tmp: one private folder for this process's scratch files, removed by the
# EXIT trap. Call it directly, never inside $(...), or the folder is not recorded.
run_tmp() {
  if [[ -z "$RUN_TMP" ]]; then
    # Only SIGKILL leaves a scratch folder behind (the EXIT trap removes it
    # otherwise), and one that old belongs to no live run.
    find "${TMPDIR:-/tmp}" -maxdepth 1 -type d -name 'fold-backup.*' -user "$(id -u)" -mmin +60 -exec rm -rf -- {} + 2>/dev/null || true
    RUN_TMP="$(mktemp -d "${TMPDIR:-/tmp}/fold-backup.XXXXXX")" || die "could not create a scratch folder" 1
    TMPS+=("$RUN_TMP")
  fi
}

# ---------------------------------------------------------------- names

# recognize NAME: sets REC_KIND (dump|tgz), REC_STAMP, REC_DAY, REC_AGE (".age" or empty).
recognize() {
  local b="$1"
  if [[ "$b" =~ ^fold-([0-9]{8})-([0-9]{4})\.dump(\.age)?$ ]]; then
    REC_KIND=dump
  elif [[ "$b" =~ ^uploads-([0-9]{8})-([0-9]{4})\.tgz(\.age)?$ ]]; then
    REC_KIND=tgz
  else
    return 1
  fi
  REC_STAMP="${BASH_REMATCH[1]}-${BASH_REMATCH[2]}"
  REC_DAY="${BASH_REMATCH[1]:6:2}"
  REC_AGE="${BASH_REMATCH[3]:-}"
}

nonce_ok() { [[ "${1:-}" =~ ^[0-9a-f]{32}$ ]]; }

# new_run_id: RUN_ID, 128 random bits as 32 lowercase hex characters. No pipeline,
# so a failed od is seen. The host's backup.sh carries the same helper.
new_run_id() {
  local raw
  raw="$(od -An -N16 -tx1 /dev/urandom 2>/dev/null)" || return 1
  raw="${raw//[[:space:]]/}"
  [[ "$raw" =~ ^[0-9a-f]{32}$ ]] || return 1
  RUN_ID="$raw"
}

# ---------------------------------------------------------------- configuration

valid_tz() {
  local tz="$1"
  [[ -n "$tz" && "$tz" != Local ]] || return 1
  [[ "$tz" =~ ^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$ ]] || return 1
  [[ -f "$ZONEINFO/$tz" ]] || return 1
  [[ "$(head -c 4 "$ZONEINFO/$tz")" == TZif ]]
}

check_tz_config() {
  TZNAME="${FOLD_BACKUP_TZ:-UTC}"
  if ! valid_tz "$TZNAME"; then
    log "FOLD_BACKUP_TZ must be an installed IANA zone name (Local is refused)"
    return 1
  fi
}

check_keep_config() {
  local v="${FOLD_BACKUP_KEEP_DAYS:-14}"
  if [[ ! "$v" =~ ^[0-9]{1,3}$ ]] || (( 10#$v < 1 || 10#$v > 365 )); then
    log "FOLD_BACKUP_KEEP_DAYS must be a number from 1 to 365"
    return 1
  fi
  KEEP_DAYS=$((10#$v))
}

check_offsite_config() {
  local v n=0
  OFFSITE=0
  for v in "${OFFSITE_VARS[@]}"; do
    if [[ -n "${!v:-}" ]]; then n=$((n + 1)); fi
  done
  if (( n == 0 )); then
    return 0
  fi
  for v in "${OFFSITE_VARS[@]}"; do
    if [[ -z "${!v:-}" ]]; then
      log "offsite settings are incomplete: $v is missing (set all six or none)"
      return 1
    fi
  done
  [[ "$FOLD_BACKUP_S3_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] \
    || { log "FOLD_BACKUP_S3_BUCKET is not a bucket name"; return 1; }
  [[ "$FOLD_BACKUP_S3_ENDPOINT" =~ ^https://[A-Za-z0-9._:-]+$ ]] \
    || { log "FOLD_BACKUP_S3_ENDPOINT must be an https URL"; return 1; }
  [[ "$FOLD_BACKUP_S3_REGION" =~ ^[a-z0-9-]+$ ]] \
    || { log "FOLD_BACKUP_S3_REGION is not a region name"; return 1; }
  [[ "$AWS_ACCESS_KEY_ID" =~ ^[!-~]+$ ]] \
    || { log "AWS_ACCESS_KEY_ID has characters that are not allowed"; return 1; }
  [[ "$AWS_SECRET_ACCESS_KEY" =~ ^[!-~]+$ ]] \
    || { log "AWS_SECRET_ACCESS_KEY has characters that are not allowed"; return 1; }
  [[ "$FOLD_BACKUP_AGE_RECIPIENT" =~ ^age1[0-9a-z]+$ ]] \
    || { log "FOLD_BACKUP_AGE_RECIPIENT must be an age public key (age1...)"; return 1; }
  OFFSITE=1
}

check_config() {
  check_tz_config && check_keep_config && check_offsite_config
}

# ---------------------------------------------------------------- time and schedule

now_epoch() {
  if [[ -n "${FOLD_BACKUP_NOW_EPOCH:-}" ]]; then
    printf '%s' "$FOLD_BACKUP_NOW_EPOCH"
  else
    date +%s
  fi
}

utc_stamp() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }

rfc3339_ok() { [[ "$1" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; }

# slot_for_day YYYY-MM-DD: the epoch of that local day's 02:30 slot, or its
# 03:30 stand-in when 02:30 does not exist (spring forward). When 02:30 occurs
# twice (fall back) it is the first occurrence, so a day has one slot.
slot_for_day() {
  local day="$1" e="" back=""
  e="$(TZ="$TZNAME" date -d "$day 02:30" +%s 2>/dev/null)" || e=""
  if [[ -n "$e" ]]; then
    back="$(TZ="$TZNAME" date -d "@$e" +%H:%M)"
  fi
  if [[ "$back" != "02:30" ]]; then
    e="$(TZ="$TZNAME" date -d "$day 03:30" +%s)"
  fi
  printf '%s' "$e"
}

# latest_slot NOW: the newest slot at or before NOW.
latest_slot() {
  local now="$1" day s noon
  day="$(TZ="$TZNAME" date -d "@$now" +%Y-%m-%d)"
  s="$(slot_for_day "$day")"
  if (( s > now )); then
    # Noon minus 24h is the previous local day across a clock change; the
    # words "12:00 -1 day" would read -1 as a zone offset.
    noon="$(TZ="$TZNAME" date -d "$day 12:00" +%s)"
    day="$(TZ="$TZNAME" date -d "@$((noon - 86400))" +%Y-%m-%d)"
    s="$(slot_for_day "$day")"
  fi
  printf '%s' "$s"
}

# backup_due NOW LAST_OK_EPOCH FAIL_EPOCH: returns 0 when a run is due. Empty
# LAST_OK means never (a first install catches up at once). A failure keeps the
# next try at least an hour away.
backup_due() {
  local now="$1" ok="$2" fail="$3" slot
  if [[ -n "$fail" ]] && (( now >= fail && now - fail < 3600 )); then
    return 1
  fi
  if [[ -z "$ok" ]]; then
    return 0
  fi
  slot="$(latest_slot "$now")"
  (( ok < slot ))
}

# read_state: ST_OK and ST_FAIL (epochs, or empty) from the status files.
read_state() {
  local v at
  ST_OK=""
  ST_FAIL=""
  if [[ -f "$STATUS_DIR/LAST_OK" ]]; then
    v="$(head -n 1 "$STATUS_DIR/LAST_OK" 2>/dev/null || true)"
    if rfc3339_ok "$v"; then ST_OK="$(date -u -d "$v" +%s)"; fi
  fi
  if [[ -f "$STATUS_DIR/status.json" ]]; then
    at="$(sed -n -E 's/^.*"lastFailure":\{"at":"([0-9TZ:-]+)","step":"[a-z]+"\}.*$/\1/p' "$STATUS_DIR/status.json" | head -n 1)"
    if [[ -n "$at" ]] && rfc3339_ok "$at"; then ST_FAIL="$(date -u -d "$at" +%s)"; fi
  fi
}

# ---------------------------------------------------------------- status files

LASTOK_RE='^\{"finishedAt":"[0-9TZ:-]+","dumpBytes":[0-9]+,"uploadsBytes":[0-9]+,"uploadsFiles":[0-9]+,"offsite":"(off|uploaded)"\}$'

status_lastok_json() {
  local f="$STATUS_DIR/status.json" s=""
  if [[ -f "$f" ]]; then
    s="$(sed -n -E 's/^.*"lastOk":(\{[^}]*\}|null),"lastFailure".*$/\1/p' "$f" | head -n 1)"
  fi
  if [[ "$s" == null ]] || [[ "$s" =~ $LASTOK_RE ]]; then
    printf '%s' "${s:-null}"
  else
    printf 'null'
  fi
}

write_status() {
  local lastok="$1" lastfail="$2" tmp
  tmp="$(mktemp "$STATUS_DIR/.status.XXXXXX")" || return 1
  if ! printf '{"format":1,"lastOk":%s,"lastFailure":%s}\n' "$lastok" "$lastfail" >"$tmp"; then
    rm -f -- "$tmp"
    return 1
  fi
  if ! { chmod 600 "$tmp" && own_app "$tmp" && sync -- "$tmp" && mv -f -- "$tmp" "$STATUS_DIR/status.json"; }; then
    rm -f -- "$tmp"
    return 1
  fi
}

write_last_ok() {
  local tmp
  tmp="$(mktemp "$STATUS_DIR/.lastok.XXXXXX")" || return 1
  if ! printf '%s\n' "$1" >"$tmp"; then
    rm -f -- "$tmp"
    return 1
  fi
  if ! { chmod 600 "$tmp" && own_app "$tmp" && sync -- "$tmp" && mv -f -- "$tmp" "$STATUS_DIR/LAST_OK"; }; then
    rm -f -- "$tmp"
    return 1
  fi
}

record_failure() {
  local step="$1" ok at
  ok="$(status_lastok_json)"
  at="$(utc_stamp "$(now_epoch)")"
  write_status "$ok" "{\"at\":\"$at\",\"step\":\"$step\"}"
}

# fail_run STEP: an admitted run failed. LAST_OK and lastOk stay as they were.
fail_run() {
  log "backup failed at step: $1"
  record_failure "$1" || log "could not write status.json"
  exit 1
}

record_success() {
  local finished
  finished="$(utc_stamp "$(now_epoch)")"
  write_status "{\"finishedAt\":\"$finished\",\"dumpBytes\":$DUMP_BYTES,\"uploadsBytes\":$UP_BYTES,\"uploadsFiles\":$UP_FILES,\"offsite\":\"$OFFSITE_WORD\"}" null \
    || die "could not write status.json" 1
  write_last_ok "$finished" || die "could not write LAST_OK" 1
}

# ---------------------------------------------------------------- admission

observe_databases() {
  local out
  if ! out="$(ext psql -X -At -v ON_ERROR_STOP=1 -d postgres -c "$Q_RESTORE_DBS" 2>/dev/null)"; then
    refuse "the database could not be observed"
  fi
  if [[ -n "$out" ]]; then
    refuse "a recovery database exists"
  fi
}

# admit db|nodb: host coordination first, then the archive lock. Any refusal is
# exit 75 and changes no file. Locks stay held until this process exits.
admit() {
  local mode="$1" dir lock alock
  dir="${FOLD_RECOVERY_DIR:-}"
  if [[ -n "$dir" ]]; then
    [[ -d "$dir" && -x "$dir" ]] || refuse "the coordination directory is missing or not accessible"
    lock="$dir/backup.lock"
    [[ -f "$lock" && ! -L "$lock" ]] || refuse "the coordination lock is missing"
    { : <"$lock"; } 2>/dev/null || refuse "the coordination lock is not readable"
    exec 6<"$lock"
    if ! flock -n -x 6; then
      refuse "another operation holds the host backup lock"
    fi
    if [[ -e "$dir/recovery.lock" || -L "$dir/recovery.lock" ]]; then # probe:recovery
      refuse "a recovery is active"
    fi
  fi
  [[ -d "$ARCHIVE_DIR" ]] || die "the archive directory is missing" 1
  alock="$ARCHIVE_DIR/.lock"
  { : >>"$alock"; } 2>/dev/null || die "the archive directory is not writable" 1
  own_app "$alock" || die "could not set the archive lock owner" 1
  exec 7>>"$alock"
  if ! flock -n -x 7; then
    refuse "another backup operation holds the archive lock"
  fi
  if [[ "$mode" == db ]]; then
    observe_databases
  fi
}

# ---------------------------------------------------------------- archives

# tar_inspect FILE: validates a gzip tar of the uploads tree. On success sets
# INSPECT_MEMBERS and INSPECT_BYTES and writes "TYPE<TAB>NAME" lines to
# INSPECT_LIST. Only ./, uploads/ and ./uploads/ subtrees of plain files and
# folders pass; links, devices, FIFOs, absolute names and .. are refused.
tar_inspect() {
  local f="$1" raw t s n p members=0 bytes=0
  local -a pieces
  run_tmp
  raw="$RUN_TMP/raw"
  INSPECT_LIST="$RUN_TMP/list"
  : >"$INSPECT_LIST"
  if ! ext tar --quoting-style=escape -tvzf "$f" >"$raw" 2>/dev/null; then
    log "the uploads archive is not a readable gzip tar"
    return 1
  fi
  awk '{ t = substr($0, 1, 1); s = $3; r = $0
         for (i = 1; i <= 5; i++) { sub(/^[^ ]+ +/, "", r) }
         printf "%s\t%s\t%s\n", t, s, r }' "$raw" >"$raw.n"
  while IFS=$'\t' read -r t s n; do
    case "$t" in
      -|d) ;;
      *) log "the uploads archive holds a link, device or other special member"; return 1 ;;
    esac
    if [[ "$n" == "./" || "$n" == "." || -z "$n" ]]; then
      if [[ "$t" != d ]]; then log "the uploads archive has an unexpected member"; return 1; fi
      continue
    fi
    n="${n#./}"
    n="${n%/}"
    if [[ "$n" != uploads && "$n" != uploads/* ]]; then
      log "the uploads archive holds a member outside uploads/"
      return 1
    fi
    IFS=/ read -ra pieces <<<"$n"
    for p in "${pieces[@]}"; do
      if [[ "$p" == . || "$p" == .. || ! "$p" =~ ^[A-Za-z0-9._-]+$ ]]; then
        log "the uploads archive holds an unsafe member name"
        return 1
      fi
    done
    if [[ "$n" == uploads && "$t" != d ]]; then
      log "uploads is not a folder in the archive"
      return 1
    fi
    members=$((members + 1))
    if [[ "$t" == - ]]; then
      [[ "$s" =~ ^[0-9]+$ ]] || { log "the uploads archive has an unreadable size"; return 1; }
      bytes=$((bytes + s))
    fi
    printf '%s\t%s\n' "$t" "$n" >>"$INSPECT_LIST"
  done <"$raw.n"
  INSPECT_MEMBERS="$members"
  INSPECT_BYTES="$bytes"
}

# ---------------------------------------------------------------- backup run

pick_stamp() {
  local base n s
  base="$(now_epoch)"
  for ((n = 0; n < 180; n++)); do
    s="$(TZ="$TZNAME" date -d "@$((base + n * 60))" +%Y%m%d-%H%M)"
    if [[ ! -e "$ARCHIVE_DIR/fold-$s.dump" && ! -e "$ARCHIVE_DIR/fold-$s.dump.age" \
      && ! -e "$ARCHIVE_DIR/uploads-$s.tgz" && ! -e "$ARCHIVE_DIR/uploads-$s.tgz.age" ]]; then
      STAMP="$s"
      return 0
    fi
  done
  return 1
}

clean_partials() {
  find "$ARCHIVE_DIR" -maxdepth 1 -type f -name '.partial.*' \( -user "$(id -u)" -o -user "$APP_UID" \) -delete
}

step_uploads() {
  local rc=0
  PART_TGZ="$ARCHIVE_DIR/.partial.uploads-$STAMP.tgz.$$"
  TMPS+=("$PART_TGZ")
  if [[ ! -d "$DATA_DIR/uploads" || -L "$DATA_DIR/uploads" ]]; then
    log "the uploads folder is missing"
    fail_run uploads
  fi
  ext tar --numeric-owner -czf "$PART_TGZ" -C "$DATA_DIR" uploads || rc=$?
  if (( rc > 1 )); then fail_run uploads; fi
  ext tar -tzf "$PART_TGZ" >/dev/null || fail_run uploads
  chmod 600 "$PART_TGZ" || fail_run uploads
  # The archive's bytes must be on disk before LAST_OK can say the run worked.
  sync -- "$PART_TGZ" || fail_run uploads
  UP_BYTES="$(stat -c %s "$PART_TGZ")"
  UP_FILES="$(tar -tvzf "$PART_TGZ" | grep -c '^-' || true)"
}

step_dump() {
  PART_DUMP="$ARCHIVE_DIR/.partial.fold-$STAMP.dump.$$"
  TMPS+=("$PART_DUMP")
  ext pg_dump -Fc --no-owner --no-acl --no-password -f "$PART_DUMP" || fail_run dump
  ext pg_restore --list "$PART_DUMP" >/dev/null || fail_run dump
  chmod 600 "$PART_DUMP" || fail_run dump
  sync -- "$PART_DUMP" || fail_run dump
  DUMP_BYTES="$(stat -c %s "$PART_DUMP")"
}

# publish_pair: both archives appear under their final names or neither does.
publish_pair() {
  PUB_TGZ="$ARCHIVE_DIR/uploads-$STAMP.tgz"
  PUB_DUMP="$ARCHIVE_DIR/fold-$STAMP.dump"
  ln -- "$PART_TGZ" "$PUB_TGZ" || fail_run dump
  if ! ln -- "$PART_DUMP" "$PUB_DUMP"; then
    rm -f -- "$PUB_TGZ"
    fail_run dump
  fi
  rm -f -- "$PART_TGZ" "$PART_DUMP"
  # Contents were flushed under the partial names (hard links share the inode);
  # this flushes the new directory entries. If it fails, neither name stays.
  if ! sync -- "$ARCHIVE_DIR"; then
    rm -f -- "$PUB_TGZ" "$PUB_DUMP"
    fail_run dump
  fi
}

# expire_old: the host helper's rules. Daily files go after KEEP_DAYS, files
# from the 1st of a month after 180 days, by mtime, for recognized names only.
expire_old() {
  local now limit month path base mtime age
  now="$(now_epoch)"
  limit=$((KEEP_DAYS * 86400))
  month=$((180 * 86400))
  for path in "$ARCHIVE_DIR"/*; do
    [[ -e "$path" || -L "$path" ]] || continue
    base="${path##*/}"
    recognize "$base" || continue
    [[ -f "$path" && ! -L "$path" ]] || continue
    mtime="$(stat -c %Y "$path")" || return 1
    age=$((now - mtime))
    if (( age > limit )); then # probe:retention
      if [[ "$REC_DAY" == 01 ]] && (( age < month )); then
        continue
      fi
      rm -f -- "$path" || return 1
      log "expired $base"
    fi
  done
}

step_encrypt() {
  local f tmp
  CIPHERS=()
  for f in "$PUB_TGZ" "$PUB_DUMP"; do
    tmp="$ARCHIVE_DIR/.partial.${f##*/}.age.$$"
    TMPS+=("$tmp")
    ext age -r "$FOLD_BACKUP_AGE_RECIPIENT" -o "$tmp" "$f" || fail_run encrypt
    [[ "$(head -c 21 "$tmp")" == "age-encryption.org/v1" ]] || fail_run encrypt
    chmod 600 "$tmp" || fail_run encrypt
    sync -- "$tmp" || fail_run encrypt
    ln -- "$tmp" "$f.age" || fail_run encrypt
    rm -f -- "$tmp"
    CIPHERS+=("$f.age")
  done
  sync -- "$ARCHIVE_DIR" || fail_run encrypt
}

# rclone_s3 ARGS...: rclone against the configured bucket. The settings travel in
# the environment only, never in argv.
rclone_s3() {
  local -x RCLONE_S3_PROVIDER=Other
  local -x RCLONE_S3_ENDPOINT="$FOLD_BACKUP_S3_ENDPOINT"
  local -x RCLONE_S3_REGION="$FOLD_BACKUP_S3_REGION"
  local -x RCLONE_S3_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID"
  local -x RCLONE_S3_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY"
  local -x RCLONE_S3_ENV_AUTH=false
  local -x RCLONE_CONFIG=/dev/null
  ext rclone "$@"
}

# upload_cipher FILE: one PUT of one member under this run's folder. The key
# needs permission to write objects and nothing else: no listing, no read, no
# delete. --no-check-dest and --s3-no-head keep rclone from asking the bucket
# about the destination before or after the upload.
upload_cipher() {
  rclone_s3 copyto "$1" ":s3:${FOLD_BACKUP_S3_BUCKET}/${REMOTE_PREFIX}${1##*/}" \
    --no-check-dest --s3-no-head --s3-no-check-bucket --retries 3 --low-level-retries 5 --contimeout 30s --timeout 5m \
    >/dev/null 2>&1
}

# step_upload: both members go under fold/<stamp>-<run id>/, a folder no other run
# names (128 random bits, not a provider guarantee). Nothing is read from the
# bucket, so a failure leaves at most a half pair that nothing later completes.
step_upload() {
  local c
  if ! new_run_id; then
    log "no random folder name could be made; nothing was uploaded"
    fail_run upload
  fi
  REMOTE_PREFIX="fold/${STAMP}-${RUN_ID}/"
  log "offsite: uploading to ${REMOTE_PREFIX}"
  for c in "${CIPHERS[@]}"; do
    if ! upload_cipher "$c"; then # probe:upload-ciphertext
      log "an upload failed; the key needs permission to write objects under fold/ (listing and reading are not needed)"
      fail_run upload
    fi
  done
}

run_backup() {
  check_config || fail_run config
  [[ -d "$STATUS_DIR" && -w "$STATUS_DIR" ]] || die "the status directory is not writable" 1
  clean_partials || fail_run config
  pick_stamp || fail_run config
  step_uploads # probe:order-uploads
  step_dump    # probe:order-dump
  publish_pair
  expire_old || fail_run expire
  OFFSITE_WORD=off
  REMOTE_FIELD=""
  if (( OFFSITE )); then
    step_encrypt
    step_upload
    OFFSITE_WORD=uploaded
    REMOTE_FIELD=" remote=$REMOTE_PREFIX"
  fi
  record_success
  printf 'published stamp=%s dump=%s uploads=%s offsite=%s%s\n' \
    "$STAMP" "fold-$STAMP.dump" "uploads-$STAMP.tgz" "$OFFSITE_WORD" "$REMOTE_FIELD"
}

cmd_now() {
  [[ $# -eq 0 ]] || usage_error "now takes no options"
  install_traps
  admit db
  run_backup
}

# ---------------------------------------------------------------- loop and health

beat() { printf '%s\n' "$$" >"$HEARTBEAT" 2>/dev/null || true; }

sleep_poll() {
  sleep "$POLL_SECS" &
  SLEEP_PID=$!
  wait "$SLEEP_PID" || true
  SLEEP_PID=""
}

idle_for() {
  local waited=0
  while (( waited < $1 )); do
    beat
    sleep_poll
    waited=$((waited + POLL_SECS))
  done
}

loop_term() {
  trap - TERM INT
  if [[ -n "$SLEEP_PID" ]]; then kill "$SLEEP_PID" 2>/dev/null || true; fi
  if [[ -n "$CHILD" ]]; then
    kill -TERM "$CHILD" 2>/dev/null || true
    wait "$CHILD" 2>/dev/null || true
  fi
  rm -f -- "$HEARTBEAT"
  log "stopped"
  exit 0
}

run_child() {
  local rc=0
  "${BASH:-bash}" "$SELF" now &
  CHILD=$!
  while kill -0 "$CHILD" 2>/dev/null; do
    beat
    sleep_poll
  done
  wait "$CHILD" || rc=$?
  CHILD=""
  case "$rc" in
    0) log "backup finished" ;;
    75) log "backup skipped" ;;
    *) log "backup run failed (status $rc)" ;;
  esac
}

cmd_loop() {
  [[ $# -eq 0 ]] || usage_error "loop takes no options"
  check_tz_config || exit 78
  trap loop_term TERM INT
  if [[ -z "${FOLD_RECOVERY_DIR:-}" ]]; then
    log "notice: FOLD_RECOVERY_DIR is empty, so backups do not coordinate with the host restore tool"
  fi
  log "nightly slot 02:30 $TZNAME; keep ${FOLD_BACKUP_KEEP_DAYS:-14} days"
  while :; do
    beat
    read_state
    if backup_due "$(now_epoch)" "$ST_OK" "$ST_FAIL"; then
      run_child
    fi
    idle_for "$TICK_SECS"
  done
}

cmd_health() {
  local pid mtime
  [[ -f "$HEARTBEAT" ]] || exit 1
  pid="$(head -n 1 "$HEARTBEAT")"
  [[ "$pid" =~ ^[0-9]+$ ]] || exit 1
  kill -0 "$pid" 2>/dev/null || exit 1
  mtime="$(stat -c %Y "$HEARTBEAT")"
  (( $(date +%s) - mtime <= HEALTH_MAX_AGE )) || exit 1
}

# ---------------------------------------------------------------- list, restore-test

cmd_list() {
  local path base mate
  [[ $# -eq 0 ]] || usage_error "list takes no options"
  for path in "$ARCHIVE_DIR"/*; do
    [[ -e "$path" || -L "$path" ]] || continue
    base="${path##*/}"
    recognize "$base" || continue
    [[ -f "$path" && ! -L "$path" ]] || continue
    if [[ "$REC_KIND" == dump ]]; then mate="uploads-$REC_STAMP.tgz"; else mate="fold-$REC_STAMP.dump"; fi
    if [[ -f "$ARCHIVE_DIR/$mate" ]]; then mate=yes; else mate=no; fi
    printf '%s %s %s pair=%s\n' "$base" "$(stat -c %s "$path")" "$(stat -c %Y "$path")" "$mate"
  done
}

find_newest_pair() {
  local path base best="" best_m=-1 m best_stamp=""
  for path in "$ARCHIVE_DIR"/fold-*.dump; do
    [[ -f "$path" && ! -L "$path" ]] || continue
    base="${path##*/}"
    recognize "$base" || continue
    [[ -z "$REC_AGE" ]] || continue
    [[ -f "$ARCHIVE_DIR/uploads-$REC_STAMP.tgz" && ! -L "$ARCHIVE_DIR/uploads-$REC_STAMP.tgz" ]] || continue
    m="$(stat -c %Y "$path")"
    if (( m > best_m )) || { (( m == best_m )) && [[ "$base" > "$best" ]]; }; then
      best="$base"
      best_m="$m"
      best_stamp="$REC_STAMP"
    fi
  done
  [[ -n "$best" ]] || return 1
  PAIR_DUMP="$ARCHIVE_DIR/fold-$best_stamp.dump"
  PAIR_TGZ="$ARCHIVE_DIR/uploads-$best_stamp.tgz"
}

psql_admin() { ext psql -X -q -At -v ON_ERROR_STOP=1 -d postgres -c "$1"; }
scratch_query() { ext psql -X -At -v ON_ERROR_STOP=1 -d "$SCRATCH_DB" -c "$1"; }

# drop_scratch: a checked drop. A restored copy that cannot be removed is a
# failure, because nothing else ever expires or erases it.
drop_scratch() {
  ext psql -X -q -At -v ON_ERROR_STOP=1 -d postgres -c "DROP DATABASE IF EXISTS $SCRATCH_DB WITH (FORCE)" >/dev/null 2>&1 \
    || die "the restore test could not remove its scratch database $SCRATCH_DB; remove it before relying on this result" 1
  SCRATCH_MADE=0
}

cmd_restore_test() {
  local keys ids t n key tables users missing=0 refs=0 orphans=0 upfiles=0 id
  local -A have=() owners=() userids=()
  [[ $# -eq 0 ]] || usage_error "restore-test takes no options"
  install_traps
  admit db
  find_newest_pair || die "no complete archive pair to test" 1
  tar_inspect "$PAIR_TGZ" || die "the uploads archive failed validation" 1
  psql_admin "DROP DATABASE IF EXISTS $SCRATCH_DB WITH (FORCE)" >/dev/null || die "could not prepare the scratch database" 1
  psql_admin "CREATE DATABASE $SCRATCH_DB" >/dev/null || die "could not create the scratch database" 1
  SCRATCH_MADE=1
  ext pg_restore --single-transaction --exit-on-error --no-owner --no-acl -d "$SCRATCH_DB" "$PAIR_DUMP" \
    || die "the database archive did not restore" 1
  tables="$(scratch_query "$Q_TABLES")" || die "could not read the restored database" 1
  users="$(scratch_query "$Q_USERS")" || die "could not read the restored database" 1
  keys="$RUN_TMP/keys"
  ids="$RUN_TMP/ids"
  scratch_query "$Q_KEYS" >"$keys" || die "could not read the restored database" 1
  scratch_query "$Q_IDS" >"$ids" || die "could not read the restored database" 1
  while IFS=$'\t' read -r t n; do
    if [[ "$t" == - ]]; then
      have["$n"]=1
      upfiles=$((upfiles + 1))
    fi
    if [[ "$n" =~ ^uploads/avatars/([0-9]+)(/|$) ]]; then
      owners["${BASH_REMATCH[1]}"]=1
    fi
  done <"$INSPECT_LIST"
  while IFS= read -r id; do
    [[ -n "$id" ]] && userids["$id"]=1
  done <"$ids"
  while IFS= read -r key; do
    [[ -n "$key" ]] || continue
    refs=$((refs + 1))
    if [[ -z "${have["uploads/$key"]:-}" ]]; then missing=$((missing + 1)); fi
  done <"$keys"
  for id in "${!owners[@]}"; do
    if [[ -z "${userids[$id]:-}" ]]; then orphans=$((orphans + 1)); fi
  done
  drop_scratch
  printf 'restore-test tables=%s users=%s uploadFiles=%s referencedFiles=%s referencedMissing=%s orphanAvatarOwners=%s\n' \
    "$tables" "$users" "$upfiles" "$refs" "$missing" "$orphans"
  if (( orphans > 0 )); then
    die "the uploads archive holds photos for accounts that are not in the database archive" 1
  fi
  if (( refs > 0 && upfiles == 0 )); then
    die "the database refers to photos but the uploads archive is empty" 1
  fi
  if (( missing > 0 )); then
    log "warning: $missing referenced photo file(s) are not in the uploads archive"
  fi
  log "restore test passed"
}

# ---------------------------------------------------------------- uploads helpers

OPT_PROBE=0
OPT_RELEASE=0
OPT_IDENTITY=""
OPT_LIVE=""
OPT_STAGED=""

parse_uploads_args() {
  NONCE=""
  OPT_PROBE=0
  OPT_RELEASE=0
  OPT_IDENTITY=""
  OPT_LIVE=""
  OPT_STAGED=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --nonce) [[ $# -ge 2 ]] || usage_error "--nonce needs a value"; NONCE="$2"; shift 2 ;;
      --nonce=*) NONCE="${1#*=}"; shift ;;
      --probe) OPT_PROBE=1; shift ;;
      --release) OPT_RELEASE=1; shift ;;
      --identity) [[ $# -ge 2 ]] || usage_error "--identity needs a value"; OPT_IDENTITY="$2"; shift 2 ;;
      --identity=*) OPT_IDENTITY="${1#*=}"; shift ;;
      --expect-live) [[ $# -ge 2 ]] || usage_error "--expect-live needs a value"; OPT_LIVE="$2"; shift 2 ;;
      --expect-staged) [[ $# -ge 2 ]] || usage_error "--expect-staged needs a value"; OPT_STAGED="$2"; shift 2 ;;
      *) usage_error "unknown option: $1" ;;
    esac
  done
  nonce_ok "$NONCE" || die "--nonce must be the full 32-character recovery nonce" 64
}

# allow_opts NAME...: refuse any parsed option not named.
allow_opts() {
  local o
  local -A ok=()
  for o in "$@"; do ok["$o"]=1; done
  if (( OPT_PROBE )) && [[ -z "${ok[probe]:-}" ]]; then usage_error "--probe does not apply here"; fi
  if (( OPT_RELEASE )) && [[ -z "${ok[release]:-}" ]]; then usage_error "--release does not apply here"; fi
  if [[ -n "$OPT_IDENTITY" && -z "${ok[identity]:-}" ]]; then usage_error "--identity does not apply here"; fi
  if [[ ( -n "$OPT_LIVE" || -n "$OPT_STAGED" ) && -z "${ok[expect]:-}" ]]; then usage_error "--expect-* does not apply here"; fi
}

set_paths() {
  LIVE="$DATA_DIR/uploads"
  PARENT="$DATA_DIR/.restore-$NONCE"
  STAGED="$PARENT/uploads"
  RCPT="$PARENT/receipt"
}

dir_ino() { stat -c %i -- "$1"; }

plain_dir() { [[ -d "$1" && ! -L "$1" ]]; }

# check_dest: the volume, the live tree and the staging parent are plain folders.
check_dest() {
  plain_dir "$DATA_DIR" || die "the uploads volume is not available" 1
  plain_dir "$LIVE" || die "the live uploads folder is missing or is not a plain folder" 1
  if [[ -e "$PARENT" || -L "$PARENT" ]]; then
    plain_dir "$PARENT" || die "the staging path is not a plain folder" 1
  fi
}

need_archive() {
  [[ -f "$RESTORE_ARCHIVE" && ! -L "$RESTORE_ARCHIVE" ]] || die "the uploads archive is not mounted" 1
}

free_bytes() { df -P -B1 "$DATA_DIR" | awk 'NR == 2 { print $4 }'; }

space_check() {
  FREE_BYTES="$(free_bytes)"
  [[ "$FREE_BYTES" =~ ^[0-9]+$ ]] || die "could not measure free space" 1
  if (( FREE_BYTES < INSPECT_BYTES )); then
    die "not enough free space for the uploads archive: need $INSPECT_BYTES bytes, have $FREE_BYTES" 1
  fi
}

syncfs_path() { sync -f -- "$1"; }

# Receipt: key=value lines in /data/.restore-NONCE/receipt, replaced atomically.
init_receipt() {
  R_NONCE="$NONCE"
  R_SHA="$1"
  R_LIVE_INO="$2"
  R_STAGE_INO=""
  R_STAGE_STATE=pending
  R_STAGE_FILES=0
  R_EXCHANGE=none
  R_DROP_INTENT=""
  R_DROP_DONE=""
}

receipt_write() {
  local tmp="$PARENT/.receipt.new"
  printf 'format=1\nnonce=%s\narchiveSha256=%s\nliveInode=%s\nstageInode=%s\nstageState=%s\nstageFiles=%s\nexchange=%s\ndropIntent=%s\ndropDone=%s\n' \
    "$R_NONCE" "$R_SHA" "$R_LIVE_INO" "$R_STAGE_INO" "$R_STAGE_STATE" "$R_STAGE_FILES" \
    "$R_EXCHANGE" "$R_DROP_INTENT" "$R_DROP_DONE" >"$tmp" || die "could not write the staging receipt" 1
  sync -- "$tmp" || die "could not write the staging receipt" 1
  mv -f -- "$tmp" "$RCPT" || die "could not write the staging receipt" 1
  sync -- "$PARENT" || die "could not write the staging receipt" 1
}

receipt_read() {
  local k v seen=0
  R_NONCE="" R_SHA="" R_LIVE_INO="" R_STAGE_INO="" R_STAGE_STATE="" R_STAGE_FILES=0
  R_EXCHANGE=none R_DROP_INTENT="" R_DROP_DONE=""
  [[ -f "$RCPT" && ! -L "$RCPT" ]] || return 1
  while IFS='=' read -r k v || [[ -n "$k" ]]; do
    case "$k" in
      format) [[ "$v" == 1 ]] || return 1; seen=1 ;;
      nonce) nonce_ok "$v" || return 1; R_NONCE="$v" ;;
      archiveSha256) [[ "$v" =~ ^[0-9a-f]{64}$ ]] || return 1; R_SHA="$v" ;;
      liveInode) [[ "$v" =~ ^[0-9]+$ ]] || return 1; R_LIVE_INO="$v" ;;
      stageInode) [[ "$v" =~ ^[0-9]*$ ]] || return 1; R_STAGE_INO="$v" ;;
      stageState) [[ "$v" =~ ^(pending|extracting|complete)$ ]] || return 1; R_STAGE_STATE="$v" ;;
      stageFiles) [[ "$v" =~ ^[0-9]+$ ]] || return 1; R_STAGE_FILES="$v" ;;
      exchange) [[ "$v" =~ ^(none|intent|done)$ ]] || return 1; R_EXCHANGE="$v" ;;
      dropIntent) [[ "$v" =~ ^[0-9]*$ ]] || return 1; R_DROP_INTENT="$v" ;;
      dropDone) [[ "$v" =~ ^[0-9]*$ ]] || return 1; R_DROP_DONE="$v" ;;
      *) return 1 ;;
    esac
  done <"$RCPT"
  (( seen )) || return 1
  [[ -n "$R_NONCE" && -n "$R_SHA" && -n "$R_LIVE_INO" && -n "$R_STAGE_STATE" ]]
}

cmd_uploads_validate() {
  parse_uploads_args "$@"
  allow_opts
  install_traps
  set_paths
  need_archive
  check_dest
  tar_inspect "$RESTORE_ARCHIVE" || die "the uploads archive failed validation; nothing was changed" 1
  space_check
  printf 'validated members=%s uncompressedBytes=%s freeBytes=%s\n' "$INSPECT_MEMBERS" "$INSPECT_BYTES" "$FREE_BYTES"
}

# clear_incomplete: remove only the staging folder this receipt recorded.
clear_incomplete() {
  if [[ -e "$STAGED" || -L "$STAGED" ]]; then
    plain_dir "$STAGED" || die "the staged path is not a plain folder; nothing was changed" 1
    if [[ "$R_STAGE_STATE" == extracting ]]; then
      [[ "$(dir_ino "$STAGED")" == "$R_STAGE_INO" ]] \
        || die "the staged folder does not match the receipt; nothing was changed" 1
      rm -rf --one-file-system -- "$STAGED" || die "could not clear the earlier staging attempt" 1
    else
      rmdir -- "$STAGED" 2>/dev/null || die "an unrelated staging folder exists; nothing was changed" 1
    fi
  fi
  R_STAGE_STATE=pending
  R_STAGE_INO=""
  receipt_write
}

stage_normalize() {
  ext chown -R -h "$APP_UID:$APP_GID" "$STAGED" || die "could not set ownership on the staged uploads" 1
  ext find -P "$STAGED" -type d -exec chmod 755 {} + || die "could not set modes on the staged uploads" 1
  ext find -P "$STAGED" -type f -exec chmod 644 {} + || die "could not set modes on the staged uploads" 1
  chmod 700 "$PARENT" || die "could not protect the staging folder" 1
  if [[ -n "$(find -P "$STAGED" ! -type f ! -type d -print -quit)" ]]; then
    die "the staged uploads hold something other than files and folders" 1
  fi
}

cmd_uploads_stage() {
  local sha live_ino files
  parse_uploads_args "$@"
  allow_opts
  install_traps
  set_paths
  need_archive
  check_dest
  sha="$(sha256sum "$RESTORE_ARCHIVE" | cut -d' ' -f1)"
  live_ino="$(dir_ino "$LIVE")"
  tar_inspect "$RESTORE_ARCHIVE" || die "the uploads archive failed validation; nothing was changed" 1
  if [[ -e "$PARENT" || -L "$PARENT" ]]; then
    if [[ -e "$RCPT" || -L "$RCPT" ]]; then
      receipt_read || die "the staging receipt is unreadable; nothing was changed" 1
      [[ "$R_NONCE" == "$NONCE" && "$R_SHA" == "$sha" ]] \
        || die "an unrelated staging area exists; nothing was changed" 1
      [[ "$R_EXCHANGE" == none && -z "$R_DROP_INTENT" ]] \
        || die "this recovery already exchanged or dropped its uploads; nothing was changed" 1
      [[ "$live_ino" == "$R_LIVE_INO" ]] \
        || die "the live uploads changed since staging began; nothing was changed" 1
      if [[ "$R_STAGE_STATE" == complete ]]; then
        if plain_dir "$STAGED" && [[ "$(dir_ino "$STAGED")" == "$R_STAGE_INO" ]]; then
          printf 'staged inode=%s files=%s\n' "$R_STAGE_INO" "$R_STAGE_FILES"
          return 0
        fi
        die "the staged folder does not match the receipt; nothing was changed" 1
      fi
      clear_incomplete
    else
      # No receipt yet: a first attempt killed while writing it leaves at most
      # the unpublished .receipt.new beside nothing else.
      [[ -z "$(find -P "$PARENT" -mindepth 1 -maxdepth 1 ! -name .receipt.new -print -quit)" ]] \
        || die "an unrelated staging area exists; nothing was changed" 1
      init_receipt "$sha" "$live_ino"
      receipt_write
    fi
  else
    mkdir -m 700 -- "$PARENT" || die "could not create the staging folder" 1
    init_receipt "$sha" "$live_ino"
    receipt_write
  fi
  space_check
  mkdir -m 700 -- "$STAGED" || die "could not create the staged folder" 1
  R_STAGE_INO="$(dir_ino "$STAGED")"
  R_STAGE_STATE=extracting
  receipt_write
  ext tar -xzf "$RESTORE_ARCHIVE" -C "$PARENT" --no-same-owner --no-same-permissions --no-overwrite-dir \
    || die "extraction did not finish; retry the stage" 1
  stage_normalize
  files="$(find -P "$STAGED" -type f | wc -l)"
  syncfs_path "$PARENT" || die "could not synchronize the staged uploads" 1
  R_STAGE_FILES="$((files + 0))"
  R_STAGE_STATE=complete
  receipt_write
  syncfs_path "$PARENT" || die "could not synchronize the staging receipt" 1
  printf 'staged inode=%s files=%s\n' "$R_STAGE_INO" "$R_STAGE_FILES"
}

# receipt_matches_disk LIVE STAGED: do the folders on disk (inode numbers or
# "none") hold the identities the receipt recorded, in the orientation its
# exchange state allows? A tree swapped in by anyone else matches neither.
receipt_matches_disk() {
  local live="$1" staged="$2" before=0 after=0
  [[ "$live" == "$R_LIVE_INO" && "$staged" == "$R_STAGE_INO" ]] && before=1
  [[ "$live" == "$R_STAGE_INO" && ( "$staged" == "$R_LIVE_INO" || "$staged" == none ) ]] && after=1
  case "$R_EXCHANGE" in
    none) (( before )) ;;
    "done") (( after )) ;;
    *) (( before || after )) ;;
  esac
}

# drop_identity: the one tree the receipt lets a drop remove. Before the
# exchange that is the staged tree (an abort); after it, the old live tree.
drop_identity() {
  if [[ "$R_EXCHANGE" == "done" ]]; then printf '%s' "$R_LIVE_INO"; else printf '%s' "$R_STAGE_INO"; fi
}

cmd_uploads_observe() {
  local live="none" staged="none" complete=no exchange=none drop=none rec=absent
  parse_uploads_args "$@"
  allow_opts
  set_paths
  plain_dir "$DATA_DIR" || die "the uploads volume is not available" 1
  if [[ -e "$LIVE" || -L "$LIVE" ]]; then
    plain_dir "$LIVE" || die "the live uploads path is not a plain folder" 1
    live="$(dir_ino "$LIVE")"
  fi
  if [[ -e "$PARENT" || -L "$PARENT" ]]; then
    plain_dir "$PARENT" || die "the staging path is not a plain folder" 1
  fi
  if [[ -e "$STAGED" || -L "$STAGED" ]]; then
    plain_dir "$STAGED" || die "the staged path is not a plain folder" 1
    staged="$(dir_ino "$STAGED")"
  fi
  if [[ -e "$RCPT" || -L "$RCPT" ]]; then
    receipt_read || die "the staging receipt is unreadable" 1
    [[ "$R_NONCE" == "$NONCE" ]] || die "the staging receipt belongs to another recovery" 1
    rec=present
    if [[ "$R_STAGE_STATE" == complete ]] && receipt_matches_disk "$live" "$staged"; then complete=yes; fi
    exchange="$R_EXCHANGE"
    if [[ -n "$R_DROP_DONE" ]]; then drop="done"; elif [[ -n "$R_DROP_INTENT" ]]; then drop=intent; fi
  fi
  printf 'observe live=%s staged=%s complete=%s exchange=%s drop=%s receipt=%s\n' \
    "$live" "$staged" "$complete" "$exchange" "$drop" "$rec"
}

probe_clean() {
  rmdir -- "$1/a" "$1/b" "$1" 2>/dev/null || true
}

probe_exchange() {
  local d p ia ib
  plain_dir "$DATA_DIR" || die "the uploads volume is not available" 1
  for d in "$DATA_DIR"/.restore-probe-*; do
    [[ -e "$d" || -L "$d" ]] || continue
    if plain_dir "$d"; then
      find -P "$d" -depth -type d -empty -delete 2>/dev/null || true
    fi
  done
  p="$DATA_DIR/.restore-probe-$NONCE"
  mkdir -m 700 -- "$p" "$p/a" "$p/b" || { probe_clean "$p"; die "could not create the exchange probe" 1; }
  ia="$(dir_ino "$p/a")"
  ib="$(dir_ino "$p/b")"
  # -T: both names are folders, and without it mv reads the second as a place to move the first into.
  if ! ext mv -T --exchange -- "$p/a" "$p/b"; then
    probe_clean "$p"
    die "this filesystem cannot exchange folders atomically; nothing was changed" 1
  fi
  if [[ "$(dir_ino "$p/a")" != "$ib" || "$(dir_ino "$p/b")" != "$ia" ]]; then
    probe_clean "$p"
    die "the exchange probe did not swap the folders" 1
  fi
  rmdir -- "$p/a" "$p/b" "$p" || die "could not remove the exchange probe" 1
  echo "probe ok"
}

cmd_uploads_exchange() {
  local live_ino stage_ino swapped=0 mvfail=0
  parse_uploads_args "$@"
  allow_opts probe expect
  install_traps
  set_paths
  if (( OPT_PROBE )); then
    probe_exchange
    return 0
  fi
  check_dest
  receipt_read || die "there is no staging receipt; nothing was changed" 1
  [[ "$R_NONCE" == "$NONCE" ]] || die "the staging receipt belongs to another recovery" 1
  [[ "$R_STAGE_STATE" == complete ]] || die "staging is not complete; nothing was exchanged" 1
  [[ -z "$R_DROP_INTENT" ]] || die "a drop is recorded; nothing was exchanged" 1
  plain_dir "$STAGED" || die "the staged folder is missing; nothing was changed" 1
  live_ino="$(dir_ino "$LIVE")"
  stage_ino="$(dir_ino "$STAGED")"
  if [[ -n "$OPT_LIVE$OPT_STAGED" ]]; then
    if ! { [[ "$live_ino" == "$OPT_LIVE" && "$stage_ino" == "$OPT_STAGED" ]] \
      || [[ "$live_ino" == "$OPT_STAGED" && "$stage_ino" == "$OPT_LIVE" ]]; }; then
      die "the folder identities are not the expected ones; nothing was changed" 1
    fi
  fi
  if [[ "$live_ino" == "$R_LIVE_INO" && "$stage_ino" == "$R_STAGE_INO" ]]; then
    [[ "$R_EXCHANGE" != "done" ]] || die "the receipt says exchanged but the folders are unchanged; nothing was changed" 1
    R_EXCHANGE=intent
    receipt_write
    ext mv -T --exchange -- "$STAGED" "$LIVE" || mvfail=1
    live_ino="$(dir_ino "$LIVE")"
    stage_ino="$(dir_ino "$STAGED")"
    if [[ "$live_ino" == "$R_STAGE_INO" && "$stage_ino" == "$R_LIVE_INO" ]]; then
      swapped=1
    elif (( mvfail )) && [[ "$live_ino" == "$R_LIVE_INO" && "$stage_ino" == "$R_STAGE_INO" ]]; then
      R_EXCHANGE=none
      receipt_write
      die "this filesystem cannot exchange folders atomically; nothing was changed" 1
    else
      die "the folders are in an unexpected state after the exchange" 1
    fi
  elif [[ "$live_ino" == "$R_STAGE_INO" && "$stage_ino" == "$R_LIVE_INO" ]]; then
    [[ "$R_EXCHANGE" == intent || "$R_EXCHANGE" == "done" ]] \
      || die "the folders are swapped but no exchange was recorded; nothing was changed" 1
    swapped=1
  else
    die "the folder identities do not match the receipt; nothing was changed" 1
  fi
  (( swapped )) || die "the exchange did not complete" 1
  syncfs_path "$PARENT" || die "could not synchronize the exchange" 1
  R_EXCHANGE="done"
  receipt_write
  syncfs_path "$PARENT" || die "could not synchronize the receipt" 1
  printf 'exchanged live=%s old=%s\n' "$R_STAGE_INO" "$R_LIVE_INO"
}

drop_release() {
  if [[ ! -e "$PARENT" && ! -L "$PARENT" ]]; then
    echo released
    return 0
  fi
  plain_dir "$PARENT" || die "the staging path is not a plain folder; nothing was released" 1
  if [[ -e "$STAGED" || -L "$STAGED" ]]; then
    die "a staged or old tree still exists; nothing was released" 1
  fi
  # A receipt that records a tree is released only once the helper itself
  # recorded that tree's removal: a tree that vanished without it is not a
  # cleaned one, and the receipt is what still says so.
  if [[ -e "$RCPT" || -L "$RCPT" ]]; then
    receipt_read || die "the staging receipt is unreadable; nothing was released" 1
    [[ "$R_NONCE" == "$NONCE" ]] || die "the staging receipt belongs to another recovery; nothing was released" 1
    if [[ -n "$R_STAGE_INO" && "$R_DROP_DONE" != "$(drop_identity)" ]]; then
      die "the receipt records a tree whose removal it never confirmed; nothing was released" 1
    fi
  fi
  rm -f -- "$RCPT" "$PARENT/.receipt.new" || die "could not remove the receipt" 1
  rmdir -- "$PARENT" || die "the staging folder is not empty; nothing was released" 1
  syncfs_path "$DATA_DIR" || die "could not synchronize the release" 1
  echo released
}

# adopt_empty_stage: the staged folder is created before the receipt records its
# inode (stageState=extracting), so a kill in between leaves a pending receipt
# and a folder the receipt does not name. Extraction cannot have begun (the
# extracting receipt precedes tar), so the only folder this can be is the empty
# one that attempt made. It is named by the caller's identity, must be a plain
# empty folder, and is written into the receipt before the ordinary drop checks,
# so the intent and removal that follow are keyed on a recorded identity as
# usual. Anything else at that path (content, a link, another inode) refuses.
adopt_empty_stage() {
  [[ "$R_STAGE_STATE" == pending && -z "$R_STAGE_INO" && "$R_EXCHANGE" == none && -z "$R_DROP_INTENT" ]] || return 0
  [[ -e "$STAGED" || -L "$STAGED" ]] || return 0
  plain_dir "$STAGED" || die "the staged path is not a plain folder; nothing was dropped" 1
  [[ "$(dir_ino "$STAGED")" == "$OPT_IDENTITY" ]] \
    || die "that tree is not the one at the staged path; nothing was dropped" 1
  [[ -z "$(find -P "$STAGED" -mindepth 1 -print -quit)" ]] \
    || die "the staged folder holds files the receipt does not account for; nothing was dropped" 1
  R_STAGE_INO="$OPT_IDENTITY"
  receipt_write
}

cmd_uploads_drop() {
  parse_uploads_args "$@"
  allow_opts identity release
  install_traps
  set_paths
  plain_dir "$DATA_DIR" || die "the uploads volume is not available" 1
  if (( OPT_RELEASE )); then
    drop_release
    return 0
  fi
  [[ "$OPT_IDENTITY" =~ ^[0-9]+$ ]] || die "--identity needs the recorded folder identity" 64
  # A symlinked parent would carry the receipt read, its rewrite and the removal outside the volume.
  if [[ -e "$PARENT" || -L "$PARENT" ]]; then
    plain_dir "$PARENT" || die "the staging path is not a plain folder; nothing was dropped" 1
  fi
  receipt_read || die "there is no staging receipt; nothing was dropped" 1
  [[ "$R_NONCE" == "$NONCE" ]] || die "the staging receipt belongs to another recovery" 1
  if [[ "$R_DROP_DONE" == "$OPT_IDENTITY" ]]; then
    printf 'dropped identity=%s\n' "$OPT_IDENTITY"
    return 0
  fi
  [[ -z "$R_DROP_DONE" ]] || die "a different tree was already dropped; nothing was changed" 1
  [[ "$R_EXCHANGE" != intent ]] || die "the exchange is unresolved; nothing was dropped" 1
  adopt_empty_stage
  [[ -n "$(drop_identity)"&& "$OPT_IDENTITY" == "$(drop_identity)" ]] \
    || die "that tree is not the one the receipt records; nothing was dropped" 1
  if [[ -n "$R_DROP_INTENT" ]]; then
    [[ "$R_DROP_INTENT" == "$OPT_IDENTITY" ]] || die "a different drop is recorded; nothing was changed" 1
  else
    if ! { plain_dir "$STAGED" && [[ "$(dir_ino "$STAGED")" == "$OPT_IDENTITY" ]]; }; then
      die "the tree is missing or is not the recorded one; nothing was dropped" 1
    fi
    if ! { plain_dir "$LIVE" && [[ "$(dir_ino "$LIVE")" != "$OPT_IDENTITY" ]]; }; then
      die "refusing to drop the live tree" 1
    fi
    R_DROP_INTENT="$OPT_IDENTITY"
    receipt_write
  fi
  if [[ -e "$STAGED" || -L "$STAGED" ]]; then
    if ! { plain_dir "$STAGED" && [[ "$(dir_ino "$STAGED")" == "$OPT_IDENTITY" ]]; }; then
      die "an unexpected tree is at the recorded path; nothing was changed" 1
    fi
    ext rm -rf --one-file-system -- "$STAGED" || die "could not remove the tree completely; retry the drop" 1
  fi
  syncfs_path "$PARENT" || die "could not synchronize the drop" 1
  R_DROP_DONE="$OPT_IDENTITY"
  receipt_write
  printf 'dropped identity=%s\n' "$OPT_IDENTITY"
}

# ---------------------------------------------------------------- self-test

cmd_self_test() {
  local t bad=0 tool
  for tool in age rclone pg_dump pg_restore psql tar gzip flock sha256sum find stat sync df awk sed od; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      log "self-test: $tool is missing"
      bad=1
    fi
  done
  if ! mv --help 2>&1 | grep -q -- '--exchange'; then
    log "self-test: mv has no --exchange"
    bad=1
  fi
  recognize fold-20261115-0230.dump.age || { log "self-test: name grammar"; bad=1; }
  [[ "$REC_KIND" == dump && "$REC_DAY" == 15 && "$REC_AGE" == .age ]] || { log "self-test: name fields"; bad=1; }
  if recognize fold-20261115-0230.tgz || recognize ../fold-20261115-0230.dump || recognize notes.txt; then
    log "self-test: name grammar accepts a wrong name"
    bad=1
  fi
  nonce_ok 0123456789abcdef0123456789abcdef || { log "self-test: nonce"; bad=1; }
  if nonce_ok 0123456789abcdef; then log "self-test: short nonce accepted"; bad=1; fi
  valid_tz UTC || { log "self-test: UTC zone is not installed"; bad=1; }
  if valid_tz Local; then log "self-test: Local accepted"; bad=1; fi
  TZNAME=UTC
  t="$(date -u -d '2026-11-15 02:31' +%s)"
  backup_due "$t" "$((t - 86400))" "" || { log "self-test: a missed slot is not due"; bad=1; }
  t="$(date -u -d '2026-11-15 02:29' +%s)"
  if backup_due "$t" "$((t - 3600))" ""; then log "self-test: a run is due before the slot"; bad=1; fi
  if (( bad )); then
    log "self-test failed"
    exit 1
  fi
  echo "self-test ok"
}

# ---------------------------------------------------------------- main

main() {
  local cmd="${1:-}"
  if [[ $# -gt 0 ]]; then shift; fi
  case "$cmd" in
    loop) cmd_loop "$@" ;;
    now) cmd_now "$@" ;;
    restore-test) cmd_restore_test "$@" ;;
    list) cmd_list "$@" ;;
    health) cmd_health "$@" ;;
    uploads-validate) cmd_uploads_validate "$@" ;;
    uploads-stage) cmd_uploads_stage "$@" ;;
    uploads-observe) cmd_uploads_observe "$@" ;;
    uploads-exchange) cmd_uploads_exchange "$@" ;;
    uploads-drop) cmd_uploads_drop "$@" ;;
    --self-test) cmd_self_test ;;
    --help|-h|help) usage ;;
    "") usage >&2; exit 64 ;;
    *) usage_error "unknown command: $cmd" ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
