#!/usr/bin/env bash
# Tests for deploy/backup/backup.sh with host stubs. Needs GNU date and touch,
# flock, tar and a zoneinfo database; needs no Docker, no PostgreSQL and no
# network. pg_dump, pg_restore, psql, age and rclone are stubs on PATH.
#
#   backup-test.sh                 run every case
#   backup-test.sh --only CASE     run one case
#   backup-test.sh --list          list the cases
#   backup-test.sh --probes        mutate backup.sh four ways and require each
#                                  mutation to make its named case fail
#
# The fake bucket is deploy/digitalocean/bucket-model.sh, the same model the host
# producer's fixtures use: PUTs are kept by their whole key in a ledger, and a call
# that lists, reads or deletes writes the file "forbidden" that every run requires
# to be absent.
#
# BACKUP_SH names the script under test (the probes point it at a mutant).
# Cases run in subshells and set environment for the script under test, so the
# subshell-modification notes (SC2030, SC2031) do not apply; the sed programs
# in the probes are single-quoted on purpose (SC2016).
# shellcheck disable=SC2030,SC2031,SC2016
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
BACKUP_SH="${BACKUP_SH:-$HERE/backup.sh}"
ORIG_PATH="$PATH"
# shellcheck source=deploy/digitalocean/bucket-model.sh
source "$HERE/../digitalocean/bucket-model.sh"
BUCKET_MODEL="$HERE/../digitalocean/bucket-model.sh"
REAL_OD="$(command -v od)"
export BUCKET_MODEL REAL_OD
ID1=00112233445566778899aabbccddeeff
ID2=ffeeddccbbaa99887766554433221100
FID=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
N1=0123456789abcdef0123456789abcdef
N2=fedcba9876543210fedcba9876543210

for tool in flock tar gzip sha256sum stat touch find df awk sed sync; do
  command -v "$tool" >/dev/null 2>&1 || { echo "backup-test: $tool is required" >&2; exit 2; }
done
date --version 2>/dev/null | grep -q 'GNU coreutils' || { echo "backup-test: GNU date is required" >&2; exit 2; }
for z in UTC America/New_York Europe/Berlin; do
  [[ -f "/usr/share/zoneinfo/$z" ]] || { echo "backup-test: zoneinfo for $z is required" >&2; exit 2; }
done
# mv --exchange needs coreutils 9.5. On an older host the mv stub emulates it
# with three renames: the cases test backup.sh's logic, and the kernel's atomic
# exchange is exercised by the image build and the restore job.
if mv --help 2>&1 | grep -q -- '--exchange'; then
  MV_EMULATE=0
else
  MV_EMULATE=1
fi
export MV_EMULATE

REAL_TAR="$(command -v tar)"
REAL_MV="$(command -v mv)"
REAL_RM="$(command -v rm)"
REAL_DF="$(command -v df)"
REAL_SYNC="$(command -v sync)"
export REAL_TAR REAL_MV REAL_RM REAL_DF REAL_SYNC

WORK="$(mktemp -d "${TMPDIR:-/tmp}/backup-test.XXXXXX")"
trap '"$REAL_RM" -rf -- "$WORK"' EXIT

# ------------------------------------------------------------------ harness

fail() { echo "    FAIL: $*" >&2; exit 1; }

expect_rc() {
  [[ "$RC" == "$1" ]] || fail "exit status $RC, wanted $1 (stderr: $(tail -n 3 "$SB/err" 2>/dev/null | tr '\n' '|'))"
}

run_bk() {
  RC=0
  bash "$BACKUP_SH" "$@" >"$SB/out" 2>"$SB/err" || RC=$?
  OUT="$(cat "$SB/out")"
  ERR="$(cat "$SB/err")"
  cat "$SB/out" "$SB/err" >>"$SB/all.log"
  bucket_clean || fail "a run listed, read or deleted something in the bucket: $(cat "$BUCKET/forbidden")"
}

# hold_lock FILE: hold an exclusive flock on FILE until kill_holder.
hold_lock() {
  ( exec 9<"$1"; flock -x 9; exec sleep 30 ) &
  HOLDER=$!
  sleep 0.5
}

kill_holder() {
  kill "$HOLDER" 2>/dev/null
  wait "$HOLDER" 2>/dev/null
}

mode_of() { stat -c %a -- "$1"; }
sha_of() { sha256sum "$1" | cut -d' ' -f1; }

wait_for() { # SECONDS COMMAND
  local i
  for ((i = 0; i < $1 * 5; i++)); do
    if eval "$2"; then return 0; fi
    sleep 0.2
  done
  return 1
}

new_sandbox() {
  SB="$WORK/$1"
  "$REAL_RM" -rf -- "$SB"
  mkdir -p "$SB"/{archives,status,data/uploads/avatars/1,restore,stubs,tmp}
  echo uploadone >"$SB/data/uploads/avatars/1/a.jpg"
  export FOLD_BACKUP_ARCHIVE_DIR="$SB/archives" FOLD_BACKUP_STATUS_DIR="$SB/status"
  export FOLD_BACKUP_DATA_DIR="$SB/data"
  export FOLD_BACKUP_RESTORE_ARCHIVE="$SB/restore/archive.tgz"
  export FOLD_BACKUP_HEARTBEAT="$SB/heartbeat"
  # Scratch files go inside the sandbox, so a script killed by SIGKILL (the crash
  # cases) leaves nothing behind in the real temporary folder.
  export TMPDIR="$SB/tmp"
  FOLD_BACKUP_APP_UID="$(id -u)"
  FOLD_BACKUP_APP_GID="$(id -g)"
  export FOLD_BACKUP_APP_UID FOLD_BACKUP_APP_GID
  export STUB_LOG="$SB/stub.log" STUB_DIR="$SB/stubs"
  bucket_reset "$SB/bucket"
  unset OD_ID OD_MODE BUCKET_TAG
  export PGPASSWORD=PGSENTINEL-4f81
  : >"$STUB_LOG"
  unset FOLD_RECOVERY_DIR FOLD_BACKUP_TZ FOLD_BACKUP_KEEP_DAYS FOLD_BACKUP_NOW_EPOCH
  write_stubs
  export PATH="$SB/stubs:$ORIG_PATH"
}

use_offsite() {
  export FOLD_BACKUP_S3_BUCKET=test-bucket FOLD_BACKUP_S3_ENDPOINT=https://s3.example.test
  export FOLD_BACKUP_S3_REGION=test-1 AWS_ACCESS_KEY_ID=AKIASENTINEL1234
  export AWS_SECRET_ACCESS_KEY=SECRETSENTINEL5678
  export FOLD_BACKUP_AGE_RECIPIENT=age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq
  export EXPECT_S3_SECRET=SECRETSENTINEL5678
}

use_coord() {
  mkdir -p "$SB/coord"
  : >"$SB/coord/backup.lock"
  chmod 640 "$SB/coord/backup.lock"
  export FOLD_RECOVERY_DIR="$SB/coord"
}

stub() { # NAME  (body on stdin)
  cat >"$SB/stubs/$1"
  chmod +x "$SB/stubs/$1"
}

write_stubs() {
  stub pg_dump <<'EOF'
#!/usr/bin/env bash
echo pg_dump >>"$STUB_LOG"
echo "$$" >"$STUB_DIR/dump.pid"
out=""
while [[ $# -gt 0 ]]; do case "$1" in -f) out="$2"; shift 2 ;; *) shift ;; esac; done
if [[ "${STUB_FAIL:-}" == dump ]]; then exit 1; fi
if [[ -n "${STUB_SLEEP:-}" ]]; then : >"$STUB_DIR/dump.started"; sleep "$STUB_SLEEP"; fi
printf 'PLAIN-DUMP-SENTINEL %s\n' "$$-$RANDOM" >"$out"
EOF
  stub pg_restore <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == --list ]]; then
  grep -q CORRUPT "$2" && exit 1
  exit 0
fi
echo pg_restore-restore >>"$STUB_LOG"
if [[ "${STUB_FAIL:-}" == restore ]]; then exit 1; fi
exit 0
EOF
  stub psql <<'EOF'
#!/usr/bin/env bash
db=""; sql=""
while [[ $# -gt 0 ]]; do case "$1" in -d) db="$2"; shift 2 ;; -c) sql="$2"; shift 2 ;; *) shift ;; esac; done
echo "psql $db: ${sql:0:60}" >>"$STUB_LOG"
case "$sql" in
  *"from pg_database"*)
    if [[ -n "${STUB_PSQL_FAIL:-}" ]]; then exit 2; fi
    if [[ -n "${STUB_DBLIST:-}" ]]; then printf '%s\n' "$STUB_DBLIST"; fi ;;
  *"CREATE DATABASE"*) : >"$STUB_DIR/scratch.made" ;;
  *"DROP DATABASE"*)
    # STUB_DROP_FAIL=1 refuses to remove a scratch database that was created.
    if [[ "${STUB_DROP_FAIL:-}" == 1 && -e "$STUB_DIR/scratch.made" ]]; then exit 1; fi
    rm -f "$STUB_DIR/scratch.made" ;;
  *information_schema*) echo "${STUB_TABLES:-9}" ;;
  *"count(*) from users"*) echo "${STUB_USERS:-3}" ;;
  *avatar_key*) if [[ -f "${STUB_KEYS_FILE:-/nonexistent}" ]]; then cat "$STUB_KEYS_FILE"; fi ;;
  *"select id from users"*) if [[ -f "${STUB_IDS_FILE:-/nonexistent}" ]]; then cat "$STUB_IDS_FILE"; fi ;;
esac
exit 0
EOF
  stub age <<'EOF'
#!/usr/bin/env bash
echo "age $*" >>"$STUB_LOG"
if [[ "${STUB_FAIL:-}" == encrypt ]]; then exit 1; fi
out=""; in=""
while [[ $# -gt 0 ]]; do case "$1" in -r) shift 2 ;; -o) out="$2"; shift 2 ;; *) in="$1"; shift ;; esac; done
if [[ "${STUB_AGE:-}" == nohdr ]]; then
  tr 'A-Za-z' 'N-ZA-Mn-za-m' <"$in" >"$out"
else
  { printf 'age-encryption.org/v1\n'; tr 'A-Za-z' 'N-ZA-Mn-za-m' <"$in"; } >"$out"
fi
EOF
  stub rclone <<'EOF'
#!/usr/bin/env bash
echo "rclone $*" >>"$STUB_LOG"
echo "rclone $*" >>"$STUB_LOG.rclone"
source "$BUCKET_MODEL"
# The one call a write-only key allows: copyto of a local file to a key. Anything
# else (lsf, ls, lsjson, cat, a copy from the remote) reads or lists the bucket.
[[ "${1:-}" == copyto && "${2:-}" != :* && "${3:-}" == ":s3:test-bucket/"* ]] || bucket_forbidden_call "rclone $*"
if [[ "${RCLONE_S3_SECRET_ACCESS_KEY:-}" == "${EXPECT_S3_SECRET:-unset}" ]]; then echo env-ok >>"$STUB_LOG.env"; fi
bucket_put "${BUCKET_TAG:-image}" "$2" "${3#:s3:test-bucket/}"
EOF
  bucket_od_stub "$SB/stubs/od"
  stub tar <<'EOF'
#!/usr/bin/env bash
case " $* " in
  *" -czf "*)
    echo tar-create >>"$STUB_LOG"
    if [[ "${STUB_FAIL:-}" == uploads ]]; then exit 2; fi ;;
  *" -xzf "*)
    "$REAL_TAR" "$@" || exit $?
    if [[ "${STUB_TAR_KILL:-}" == extract ]]; then
      dir=""; prev=""
      for a in "$@"; do if [[ "$prev" == -C ]]; then dir="$a"; fi; prev="$a"; done
      echo junk >"$dir/uploads/JUNK-PARTIAL"
      kill -9 "$PPID"; sleep 1; exit 137
    fi
    exit 0 ;;
esac
exec "$REAL_TAR" "$@"
EOF
  stub mv <<'EOF'
#!/usr/bin/env bash
if [[ "${MV_EMULATE:-0}" == 1 && "${1:-}" == --help ]]; then
  "$REAL_MV" --help
  echo "      --exchange  exchange source and destination (emulated by the test stub)"
  exit 0
fi
if [[ " $* " == *" --exchange "* ]]; then
  if [[ "${STUB_NOEXCHANGE:-}" == 1 ]]; then echo "mv: renameat2: Invalid argument" >&2; exit 1; fi
  if [[ "${STUB_MV_KILL:-}" == before ]]; then kill -9 "$PPID"; sleep 1; exit 137; fi
  if [[ "${MV_EMULATE:-0}" == 1 ]]; then
    a="${*: -2:1}"; b="${*: -1}"
    "$REAL_MV" -- "$b" "$b.exchange.$$" && "$REAL_MV" -- "$a" "$b" && "$REAL_MV" -- "$b.exchange.$$" "$a" || exit 1
  else
    "$REAL_MV" "$@" || exit $?
  fi
  if [[ "${STUB_MV_KILL:-}" == after ]]; then kill -9 "$PPID"; sleep 1; exit 137; fi
  exit 0
fi
exec "$REAL_MV" "$@"
EOF
  stub sync <<'EOF'
#!/usr/bin/env bash
# STUB_SYNC_FAIL names what cannot be flushed: the uploads partial, the dump
# partial, an encrypted partial, or the archive directory itself.
case "${STUB_SYNC_FAIL:-}:${*: -1}" in
  uploads:*/.partial.uploads-*.tgz.[0-9]*) exit 1 ;;
  dump:*/.partial.fold-*.dump.[0-9]*) exit 1 ;;
  age:*/.partial.*.age.[0-9]*) exit 1 ;;
  dir:"$FOLD_BACKUP_ARCHIVE_DIR") exit 1 ;;
esac
# STUB_RECEIPT_KILL_N=N kills the caller at the Nth flush of a receipt being
# written: before the receipt is published, so the old one (or none) stays.
if [[ -n "${STUB_RECEIPT_KILL_N:-}" && "${*: -1}" == */.receipt.new ]]; then
  n=$(( $(cat "$STUB_DIR/receipt.n" 2>/dev/null || echo 0) + 1 ))
  echo "$n" >"$STUB_DIR/receipt.n"
  if [[ "$n" == "$STUB_RECEIPT_KILL_N" ]]; then kill -9 "$PPID"; sleep 1; exit 137; fi
fi
if [[ " $* " == *" -f "* && -n "${STUB_SYNC_KILL_N:-}" ]]; then
  n=$(( $(cat "$STUB_DIR/sync.n" 2>/dev/null || echo 0) + 1 ))
  echo "$n" >"$STUB_DIR/sync.n"
  if [[ "$n" == "$STUB_SYNC_KILL_N" ]]; then kill -9 "$PPID"; sleep 1; exit 137; fi
fi
exec "$REAL_SYNC" "$@"
EOF
  stub rm <<'EOF'
#!/usr/bin/env bash
last="${*: -1}"
if [[ "${STUB_RM_FAIL:-}" == 1 && " $* " == *" -f -- "* && "$last" == */fold-2020* ]]; then exit 1; fi
if [[ "${STUB_RM_KILL:-}" == during && " $* " == *" --one-file-system "* ]]; then
  first="$(find "$last" -type f | head -n 1)"
  "$REAL_RM" -f -- "$first"
  kill -9 "$PPID"; sleep 1; exit 137
fi
exec "$REAL_RM" "$@"
EOF
  stub df <<'EOF'
#!/usr/bin/env bash
if [[ -n "${STUB_FREE:-}" ]]; then
  printf 'Filesystem 1-blocks Used Available Capacity Mounted on\nstub 100 1 %s 1%% /data\n' "$STUB_FREE"
  exit 0
fi
exec "$REAL_DF" "$@"
EOF
}

# make_tgz DEST [extra file relative to uploads]: a good uploads archive.
make_tgz() {
  local dest="$1" src="$SB/tgzsrc"
  "$REAL_RM" -rf -- "$src"
  mkdir -p "$src/uploads/avatars/1" "$src/uploads/avatars/2"
  echo one >"$src/uploads/avatars/1/a.jpg"
  echo two >"$src/uploads/avatars/2/b.png"
  "$REAL_TAR" -czf "$dest" -C "$src" uploads
}

snapshot() { (cd "$1" && find . -printf '%p %y %i %m\n' | sort); }

# ------------------------------------------------------------------ cases

case_help_and_self_test() {
  new_sandbox help
  run_bk --help
  expect_rc 0
  [[ "$OUT" == *Usage* ]] || fail "--help prints no usage"
  run_bk
  expect_rc 64
  run_bk frobnicate
  expect_rc 64
  run_bk adopt
  expect_rc 64
  [[ "$ERR" == *"unknown command: adopt"* ]] || fail "the removed adopt command is still accepted"
  run_bk --help
  [[ "$OUT" != *adopt* ]] || fail "--help still names adopt"
  run_bk --self-test
  expect_rc 0
  [[ "$OUT" == "self-test ok" ]] || fail "self-test did not report ok"
  run_bk list
  expect_rc 0
  run_bk uploads-observe --nonce "$N1"
  expect_rc 0
  if grep -q pg_dump "$STUB_LOG"; then fail "a maintenance command started a backup"; fi
  [[ -z "$(ls -A "$SB/status")" ]] || fail "a maintenance command wrote status"
}

case_retention_boundaries() {
  new_sandbox retention
  # shellcheck source=/dev/null
  source "$BACKUP_SH"
  set +e
  local now=1800000000 name path
  export FOLD_BACKUP_NOW_EPOCH=$now
  KEEP_DAYS=14
  mk() { : >"$ARCHIVE_DIR/$1"; touch -d "@$((now - $2))" "$ARCHIVE_DIR/$1"; }
  local d=86400
  mk fold-20261001-0230.dump $((14 * d))
  mk fold-20261002-0230.dump $((14 * d + 1))
  mk uploads-20261003-0230.tgz $((14 * d + 1))
  mk fold-20261004-0230.dump.age $((20 * d))
  mk uploads-20261005-0230.tgz.age $((3 * d))
  mk fold-20260901-0230.dump $((100 * d))
  mk fold-20260801-0230.dump $((180 * d - 1))
  mk fold-20260701-0230.dump $((180 * d))
  mk uploads-20260601-0230.tgz.age $((200 * d))
  mk notes.txt $((400 * d))
  mk fold-2026-bad.dump $((400 * d))
  mk .partial.fold-x.dump.123 $((400 * d))
  mk fold-20200102-0230.tgz $((400 * d))
  ln -s /nonexistent "$ARCHIVE_DIR/fold-20200103-0230.dump"
  expire_old || fail "expire_old failed"
  for name in fold-20261001-0230.dump uploads-20261005-0230.tgz.age fold-20260901-0230.dump \
    fold-20260801-0230.dump notes.txt fold-2026-bad.dump .partial.fold-x.dump.123 fold-20200102-0230.tgz; do
    [[ -e "$ARCHIVE_DIR/$name" ]] || fail "$name was expired but must stay"
  done
  [[ -L "$ARCHIVE_DIR/fold-20200103-0230.dump" ]] || fail "a symlink was touched"
  for name in fold-20261002-0230.dump uploads-20261003-0230.tgz fold-20261004-0230.dump.age \
    fold-20260701-0230.dump uploads-20260601-0230.tgz.age; do
    [[ ! -e "$ARCHIVE_DIR/$name" ]] || fail "$name should have expired"
  done
  # KEEP_DAYS bounds and parsing.
  for path in 0 366 abc -1 ""; do
    FOLD_BACKUP_KEEP_DAYS="$path" check_keep_config 2>/dev/null && [[ -n "$path" ]] && fail "KEEP_DAYS '$path' was accepted"
  done
  FOLD_BACKUP_KEEP_DAYS=365 check_keep_config || fail "365 was refused"
  [[ "$KEEP_DAYS" == 365 ]] || fail "365 was not applied"
  unset FOLD_BACKUP_KEEP_DAYS
  check_keep_config || fail "the default was refused"
  [[ "$KEEP_DAYS" == 14 ]] || fail "the default is not 14"
  KEEP_DAYS=1
  mk fold-20261010-0230.dump $((d + 1))
  mk fold-20261011-0230.dump $((d - 1))
  expire_old
  [[ ! -e "$ARCHIVE_DIR/fold-20261010-0230.dump" && -e "$ARCHIVE_DIR/fold-20261011-0230.dump" ]] \
    || fail "KEEP_DAYS=1 boundary is wrong"
}

case_schedule_boundaries() {
  new_sandbox schedule
  # shellcheck source=/dev/null
  source "$BACKUP_SH"
  set +e
  TZNAME=UTC
  at() { date -u -d "2026-11-$1" +%s; }
  due() { backup_due "$@" || fail "expected a run to be due: $*"; }
  notdue() { if backup_due "$@"; then fail "expected no run to be due: $*"; fi; }
  due "$(at '15 01:00')" "" ""
  notdue "$(at '15 02:29')" "$(at '14 02:31')" ""
  due "$(at '15 02:30')" "$(at '14 02:31')" ""
  notdue "$(at '15 23:00')" "$(at '15 02:30')" ""
  due "$(at '15 10:00')" "$(at '10 02:31')" ""
  notdue "$(at '15 03:10')" "$(at '14 02:31')" "$(at '15 02:40')"
  due "$(at '15 03:41')" "$(at '14 02:31')" "$(at '15 02:40')"
  due "$(at '15 03:41')" "" "$(at '15 02:40')"
  notdue "$(at '15 03:10')" "" "$(at '15 02:40')"
  # One run per local day, at the slot, across the zone changes.
  simulate() { # ZONE START_DAY DAYS
    local t start end ok=""
    # shellcheck disable=SC2034 # read by the sourced backup_due
    TZNAME="$1"
    start="$(TZ="$1" date -d "$2 12:00" +%s)"
    end=$((start + $3 * 86400))
    ok=$((start - 3600))
    SIM=()
    for ((t = start; t <= end; t += 1800)); do
      if backup_due "$t" "$ok" ""; then
        ok="$t"
        SIM+=("$(TZ="$1" date -d "@$t" +%F-%H%M)")
      fi
    done
  }
  simulate UTC 2026-11-10 3
  [[ "${SIM[*]}" == "2026-11-11-0230 2026-11-12-0230 2026-11-13-0230" ]] || fail "UTC runs: ${SIM[*]}"
  simulate America/New_York 2026-03-06 5
  [[ "${SIM[*]}" == "2026-03-07-0230 2026-03-08-0330 2026-03-09-0230 2026-03-10-0230 2026-03-11-0230" ]] \
    || fail "spring forward runs: ${SIM[*]}"
  simulate America/New_York 2026-10-30 4
  [[ "${SIM[*]}" == "2026-10-31-0230 2026-11-01-0230 2026-11-02-0230 2026-11-03-0230" ]] \
    || fail "fall back runs: ${SIM[*]}"
  simulate Europe/Berlin 2026-10-23 5
  [[ "${SIM[*]}" == "2026-10-24-0230 2026-10-25-0230 2026-10-26-0230 2026-10-27-0230 2026-10-28-0230" ]] \
    || fail "repeated hour runs: ${SIM[*]}"
  valid_tz Local && fail "Local was accepted"
  valid_tz Nowhere/Land && fail "an unknown zone was accepted"
  valid_tz Europe/Berlin || fail "an installed zone was refused"
}

case_partial_config() {
  new_sandbox partial
  use_offsite
  local v
  for v in FOLD_BACKUP_S3_BUCKET FOLD_BACKUP_S3_ENDPOINT FOLD_BACKUP_S3_REGION AWS_ACCESS_KEY_ID \
    AWS_SECRET_ACCESS_KEY FOLD_BACKUP_AGE_RECIPIENT; do
    : >"$STUB_LOG"
    (
      unset "$v"
      run_bk now
      expect_rc 1
      if grep -qE 'pg_dump|tar-create' "$STUB_LOG"; then fail "a dump started with $v missing"; fi
      grep -q '"step":"config"' "$SB/status/status.json" || fail "config failure not recorded for $v"
      [[ "$ERR" == *"$v"* ]] || fail "the message does not name $v"
    ) || return 1
  done
  (
    export FOLD_BACKUP_S3_ENDPOINT=http://insecure.example.test
    run_bk now
    expect_rc 1
    [[ "$ERR" != *insecure.example.test* ]] || fail "the endpoint value reached the log"
  ) || return 1
  (
    export FOLD_BACKUP_TZ=Local
    run_bk now
    expect_rc 1
  ) || return 1
  (
    export FOLD_BACKUP_KEEP_DAYS=400
    run_bk now
    expect_rc 1
  ) || return 1
}

case_tar_before_dump() {
  new_sandbox order
  run_bk now
  expect_rc 0
  local t d
  t="$(grep -n '^tar-create' "$STUB_LOG" | head -n 1 | cut -d: -f1)"
  d="$(grep -n '^pg_dump' "$STUB_LOG" | head -n 1 | cut -d: -f1)"
  [[ -n "$t" && -n "$d" ]] || fail "tar or pg_dump never ran"
  (( t < d )) || fail "the dump ran before the uploads archive"
}

case_pair_completion_and_modes() {
  new_sandbox pair
  # The clock seam must sit near real time: expiry compares it with file mtimes.
  local now_e
  now_e="$(date +%s)"
  FOLD_BACKUP_NOW_EPOCH=$now_e run_bk now
  expect_rc 0
  [[ "$OUT" =~ ^published\ stamp=([0-9]{8}-[0-9]{4})\ dump=fold-([0-9]{8}-[0-9]{4})\.dump\ uploads=uploads-([0-9]{8}-[0-9]{4})\.tgz\ offsite=off$ ]] \
    || fail "machine line is wrong: $OUT"
  local stamp="${BASH_REMATCH[1]}"
  [[ "${BASH_REMATCH[2]}" == "$stamp" && "${BASH_REMATCH[3]}" == "$stamp" ]] || fail "the stamps differ"
  [[ "$(mode_of "$SB/archives/fold-$stamp.dump")" == 600 ]] || fail "dump mode"
  [[ "$(mode_of "$SB/archives/uploads-$stamp.tgz")" == 600 ]] || fail "uploads mode"
  [[ "$(mode_of "$SB/status/status.json")" == 600 ]] || fail "status.json mode"
  [[ "$(mode_of "$SB/status/LAST_OK")" == 600 ]] || fail "LAST_OK mode"
  [[ -z "$(find "$SB/archives" -name '.partial.*')" ]] || fail "a partial file was left"
  # Same minute again: the stamp moves on and nothing is overwritten.
  local before
  before="$(sha_of "$SB/archives/fold-$stamp.dump")"
  FOLD_BACKUP_NOW_EPOCH=$now_e run_bk now
  expect_rc 0
  [[ "$OUT" != *"stamp=$stamp "* ]] || fail "the minute collision reused the stamp"
  [[ "$(sha_of "$SB/archives/fold-$stamp.dump")" == "$before" ]] || fail "an archive was overwritten"
  # A failed dump publishes no half pair.
  rm -f "$SB"/archives/*.dump "$SB"/archives/*.tgz
  STUB_FAIL=dump run_bk now
  expect_rc 1
  [[ -z "$(find "$SB/archives" -maxdepth 1 \( -name 'fold-*' -o -name 'uploads-*' -o -name '.partial.*' \))" ]] \
    || fail "a failed dump left archive files"
}

case_status_format() {
  new_sandbox status
  run_bk now
  expect_rc 0
  local json last
  json="$(cat "$SB/status/status.json")"
  [[ "$json" =~ ^\{\"format\":1,\"lastOk\":\{\"finishedAt\":\"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z\",\"dumpBytes\":[0-9]+,\"uploadsBytes\":[0-9]+,\"uploadsFiles\":1,\"offsite\":\"off\"\},\"lastFailure\":null\}$ ]] \
    || fail "status.json after success: $json"
  last="$(cat "$SB/status/LAST_OK")"
  [[ "$last" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]{8}Z$ ]] || fail "LAST_OK: $last"
  [[ "$(wc -l <"$SB/status/LAST_OK")" == 1 ]] || fail "LAST_OK is not one line"
  [[ "$json" == *"\"finishedAt\":\"$last\""* ]] || fail "finishedAt differs from LAST_OK"
  [[ "$json" != *"$SB"* && "$json" != *test-bucket* ]] || fail "status.json names a path or bucket"
}

case_failure_at_each_step() {
  new_sandbox failures
  use_offsite
  run_bk now
  expect_rc 0
  local lastok_before json_before step
  lastok_before="$(cat "$SB/status/LAST_OK")"
  json_before="$(sed -n -E 's/^.*"lastOk":(\{[^}]*\}),"lastFailure".*$/\1/p' "$SB/status/status.json")"
  [[ -n "$json_before" ]] || fail "no lastOk to compare"
  mk_expired() { : >"$SB/archives/fold-20200101-0230.dump"; touch -d '2020-01-02' "$SB/archives/fold-20200101-0230.dump"; }
  for step in config uploads dump expire encrypt upload; do
    (
      case "$step" in
        config) export FOLD_BACKUP_KEEP_DAYS=0 ;;
        uploads) export STUB_FAIL=uploads ;;
        dump) export STUB_FAIL=dump ;;
        expire) mk_expired; export STUB_RM_FAIL=1 ;;
        encrypt) export STUB_FAIL=encrypt ;;
        upload) bucket_plan_next 2 fail-before ;;
      esac
      run_bk now
      expect_rc 1
      [[ "$(cat "$SB/status/LAST_OK")" == "$lastok_before" ]] || fail "$step: LAST_OK changed"
      grep -qF "\"lastOk\":$json_before," "$SB/status/status.json" || fail "$step: lastOk changed"
      grep -q "\"lastFailure\":{\"at\":\"[0-9TZ:-]*\",\"step\":\"$step\"}" "$SB/status/status.json" \
        || fail "$step: failure not recorded: $(cat "$SB/status/status.json")"
      [[ "$ERR" != *AKIASENTINEL1234* && "$ERR" != *SECRETSENTINEL5678* ]] || fail "$step: a secret reached the log"
    ) || return 1
    rm -f "$SB"/archives/fold-20200101-0230.dump
  done
  # A later success clears the failure.
  run_bk now
  expect_rc 0
  grep -q '"lastFailure":null' "$SB/status/status.json" || fail "success did not clear lastFailure"
  # The dump step records dump even when LAST_OK never existed.
  rm -f "$SB"/status/*
  STUB_FAIL=dump run_bk now
  expect_rc 1
  grep -q '"lastOk":null,"lastFailure":{"at":"[0-9TZ:-]*","step":"dump"}' "$SB/status/status.json" \
    || fail "first failure shape: $(cat "$SB/status/status.json")"
  [[ ! -e "$SB/status/LAST_OK" ]] || fail "a failed first run wrote LAST_OK"
}

# One offsite run: exactly two PUTs, both under one folder named for the stamp and the
# run id, ciphertext only, with the settings in the environment and no read of the bucket.
case_ciphertext_only_upload() {
  new_sandbox cipher
  use_offsite
  FOLD_BACKUP_NOW_EPOCH="$(date +%s)" run_bk now
  expect_rc 0
  local stamp f line
  [[ "$OUT" =~ ^published\ stamp=([0-9]{8}-[0-9]{4})\ dump=fold-[0-9-]+\.dump\ uploads=uploads-[0-9-]+\.tgz\ offsite=uploaded\ remote=fold/([0-9-]+)-([0-9a-f]{32})/$ ]] \
    || fail "machine line is wrong: $OUT"
  stamp="${BASH_REMATCH[1]}"
  [[ "${BASH_REMATCH[2]}" == "$stamp" && "${BASH_REMATCH[3]}" == "$ID1" ]] || fail "the remote folder is not the stamp and the run id: $OUT"
  bucket_check "$stamp:$ID1" || fail "the committed keys are not exactly one pair under fold/$stamp-$ID1/: $(bucket_objects | tr '\n' ' ')"
  [[ "$(bucket_attempts)" == 2 ]] || fail "a run made $(bucket_attempts) PUT invocations, not 2"
  bucket_each_key_once || fail "a key was the target of two invocations"
  [[ "$ERR" == *"offsite: uploading to fold/$stamp-$ID1/"* ]] || fail "the folder was not logged: $ERR"
  [[ "$ERR" != *test-bucket* ]] || fail "the bucket name reached the log"
  for f in "fold-$stamp.dump.age" "uploads-$stamp.tgz.age"; do
    [[ "$(head -c 21 "$BUCKET/objects/fold/$stamp-$ID1/$f")" == "age-encryption.org/v1" ]] || fail "$f is not age ciphertext"
    [[ "$(mode_of "$SB/archives/$f")" == 600 ]] || fail "local $f mode"
    cmp -s "$BUCKET/objects/fold/$stamp-$ID1/$f" "$SB/archives/$f" || fail "remote $f differs from the local ciphertext"
  done
  # The fakes encrypt by rotation, so this is routing, not proof: nothing plain was sent.
  if grep -rq PLAIN-DUMP-SENTINEL "$BUCKET/objects"; then fail "plaintext reached the bucket"; fi
  grep -q env-ok "$STUB_LOG.env" || fail "the S3 secret did not travel in the environment"
  [[ "$(cat "$SB/status/status.json")" =~ ^\{\"format\":1,\"lastOk\":\{\"finishedAt\":\"[0-9TZ:-]+\",\"dumpBytes\":[0-9]+,\"uploadsBytes\":[0-9]+,\"uploadsFiles\":[0-9]+,\"offsite\":\"uploaded\"\},\"lastFailure\":null\}$ ]] \
    || fail "status.json changed shape: $(cat "$SB/status/status.json")"
  # The client is told not to look at the destination, before or after, and never to compare.
  while IFS= read -r line; do
    [[ "$line" == "rclone copyto "* && "$line" == *" --no-check-dest "* && "$line" == *" --s3-no-head "* \
      && "$line" == *" --s3-no-check-bucket "* && "$line" != *--immutable* ]] || fail "a client call is not write-only: $line"
  done <"$STUB_LOG.rclone"
  # A bad header stops the run before anything is uploaded.
  bucket_reset "$BUCKET"
  STUB_AGE=nohdr run_bk now
  expect_rc 1
  [[ "$(bucket_attempts)" == 0 ]] || fail "an object without an age header was uploaded"
}

case_secret_sentinels() {
  new_sandbox secrets
  use_offsite
  run_bk now
  expect_rc 0
  STUB_FAIL=dump run_bk now
  expect_rc 1
  bucket_plan_next 1 fail-before
  run_bk now
  expect_rc 1
  local s
  for s in PGSENTINEL-4f81 AKIASENTINEL1234 SECRETSENTINEL5678; do
    if grep -rqF "$s" "$SB/status" "$SB/all.log" "$STUB_LOG" "$SB/archives" 2>/dev/null; then
      fail "secret sentinel $s leaked"
    fi
  done
  if [[ -n "$(find "$SB/archives" -maxdepth 1 \( -name '*AKIASENTINEL*' -o -name '*SECRETSENTINEL*' \))" ]]; then
    fail "a secret is in a file name"
  fi
}

case_recovery_refusal() {
  new_sandbox recovery
  use_coord
  run_bk now
  expect_rc 0
  local last json
  last="$(cat "$SB/status/LAST_OK")"
  json="$(cat "$SB/status/status.json")"
  refused() { # description
    : >"$STUB_LOG"
    local files
    files="$(snapshot "$SB/archives")"
    run_bk now
    expect_rc 75
    if grep -qE '^(pg_dump|tar-create|age |rclone )' "$STUB_LOG"; then fail "$1: backup work ran"; fi
    [[ "$(cat "$SB/status/LAST_OK")" == "$last" && "$(cat "$SB/status/status.json")" == "$json" ]] \
      || fail "$1: status changed"
    [[ "$(snapshot "$SB/archives")" == "$files" ]] || fail "$1: the archive directory changed"
  }
  : >"$SB/coord/recovery.lock"
  refused "recovery.lock present"
  run_bk restore-test
  expect_rc 75
  rm -f "$SB/coord/recovery.lock"
  ln -s /nonexistent "$SB/coord/recovery.lock"
  refused "recovery.lock as a dangling symlink"
  rm -f "$SB/coord/recovery.lock"
  (
    export STUB_PSQL_FAIL=1
    refused "failed database observation"
  ) || return 1
  (
    export STUB_DBLIST=fold_restore_0123
    refused "a restore database exists"
  ) || return 1
  (
    export STUB_DBLIST=fold_pre_restore_0123
    refused "a pre-restore database exists"
  ) || return 1
  if [[ "$(id -u)" != 0 ]]; then
    chmod 000 "$SB/coord"
    refused "unreadable coordination directory"
    chmod 755 "$SB/coord"
    chmod 000 "$SB/coord/backup.lock"
    refused "unreadable lock file"
    chmod 640 "$SB/coord/backup.lock"
  fi
  mv "$SB/coord/backup.lock" "$SB/coord/gone"
  refused "missing lock file"
  mv "$SB/coord/gone" "$SB/coord/backup.lock"
  FOLD_RECOVERY_DIR="$SB/nonexistent" refused "missing coordination directory"
  # And without a configured directory the database name check still refuses.
  unset FOLD_RECOVERY_DIR
  (
    export STUB_DBLIST=fold_restore_0123
    refused "uncoordinated, restore database present"
  ) || return 1
}

case_contention() {
  new_sandbox contention
  use_coord
  chmod 440 "$SB/coord/backup.lock"
  run_bk now
  expect_rc 0
  : >"$STUB_LOG"
  hold_lock "$SB/coord/backup.lock"
  run_bk now
  expect_rc 75
  if grep -qE 'pg_dump|tar-create' "$STUB_LOG"; then fail "backup ran against the host lock"; fi
  kill_holder
  hold_lock "$SB/archives/.lock"
  run_bk now
  expect_rc 75
  if grep -qE 'pg_dump|tar-create' "$STUB_LOG"; then fail "backup ran against the archive lock"; fi
  kill_holder
  run_bk now
  expect_rc 0
}

case_restore_test() {
  new_sandbox restore
  echo avatars/1/a.jpg >"$SB/keys"
  printf '1\n' >"$SB/ids"
  export STUB_KEYS_FILE="$SB/keys" STUB_IDS_FILE="$SB/ids"
  run_bk now
  expect_rc 0
  local json
  json="$(cat "$SB/status/status.json")"
  : >"$STUB_LOG"
  run_bk restore-test
  expect_rc 0
  [[ "$OUT" == "restore-test tables=9 users=3 uploadFiles=1 referencedFiles=1 referencedMissing=0 orphanAvatarOwners=0" ]] \
    || fail "restore-test line: $OUT"
  [[ "$(grep -c 'DROP DATABASE' "$STUB_LOG")" -ge 2 ]] || fail "the scratch database was not dropped"
  [[ "$(grep '^psql' "$STUB_LOG" | tail -n 1)" == *"DROP DATABASE"* ]] || fail "the scratch database outlived the test"
  [[ "$(cat "$SB/status/status.json")" == "$json" ]] || fail "restore-test changed status"
  # A single missing reference warns; an orphan owner fails.
  echo avatars/1/gone.jpg >>"$SB/keys"
  run_bk restore-test
  expect_rc 0
  [[ "$OUT" == *"referencedMissing=1"* && "$ERR" == *warning* ]] || fail "missing reference not reported"
  printf '2\n' >"$SB/ids"
  run_bk restore-test
  expect_rc 1
  [[ "$OUT" == *"orphanAvatarOwners=1"* ]] || fail "orphan owner not counted"
  [[ "$(grep '^psql' "$STUB_LOG" | tail -n 1)" == *"DROP DATABASE"* ]] || fail "scratch kept after a failure"
  printf '1\n' >"$SB/ids"
  # The database refers to photos but the uploads archive is empty.
  rm -f "$SB"/archives/*
  rm -rf "$SB/data/uploads/avatars"
  run_bk now
  expect_rc 0
  run_bk restore-test
  expect_rc 1
  [[ "$ERR" == *empty* ]] || fail "empty archive not explained: $ERR"
  # An unreadable archive and a failed restore.
  rm -f "$SB"/archives/uploads-*.tgz
  local dump
  dump="$(find "$SB/archives" -maxdepth 1 -name 'fold-*.dump' | head -n 1)"
  dump="${dump##*/fold-}"
  echo notgzip >"$SB/archives/uploads-${dump%.dump}.tgz"
  run_bk restore-test
  expect_rc 1
  rm -f "$SB"/archives/uploads-*.tgz
  mkdir -p "$SB/data/uploads/avatars/1"
  echo one >"$SB/data/uploads/avatars/1/a.jpg"
  run_bk now
  expect_rc 0
  STUB_FAIL=restore run_bk restore-test
  expect_rc 1
  [[ "$(grep '^psql' "$STUB_LOG" | tail -n 1)" == *"DROP DATABASE"* ]] || fail "scratch kept after a failed restore"
  # A scratch database that cannot be removed is a failed test, not a pass.
  STUB_DROP_FAIL=1 run_bk restore-test
  expect_rc 1
  [[ "$ERR" == *"could not remove its scratch database"* ]] || fail "unremovable scratch not explained: $ERR"
  [[ "$ERR" != *"restore test passed"* && "$OUT" != restore-test* ]] || fail "restore-test reported success with a scratch copy left"
  [[ "$ERR$OUT" != *PGSENTINEL* ]] || fail "the password reached the output"
  [[ -e "$STUB_DIR/scratch.made" ]] || fail "the stub did not keep the scratch database"
  run_bk restore-test
  expect_rc 0
  [[ ! -e "$STUB_DIR/scratch.made" ]] || fail "a later run did not remove the leftover scratch database"
}

case_sync_failures() {
  new_sandbox syncfail
  use_offsite
  run_bk now
  expect_rc 0
  local lastok before mode step
  lastok="$(cat "$SB/status/LAST_OK")"
  : >"$SB/archives/fold-20200101-0230.dump"
  touch -d '2020-01-02' "$SB/archives/fold-20200101-0230.dump"
  before="$(find "$SB/archives" -maxdepth 1 -type f -printf '%f\n' | sort)"
  for mode in uploads dump dir age; do
    STUB_SYNC_FAIL=$mode run_bk now
    expect_rc 1
    case "$mode" in uploads) step=uploads ;; age) step=encrypt ;; *) step=dump ;; esac
    [[ "$(cat "$SB/status/LAST_OK")" == "$lastok" ]] || fail "$mode: LAST_OK moved after a failed flush"
    grep -q "\"lastFailure\":{\"at\":\"[0-9TZ:-]*\",\"step\":\"$step\"}" "$SB/status/status.json" \
      || fail "$mode: failure not recorded as $step: $(cat "$SB/status/status.json")"
    if [[ "$mode" != age ]]; then
      # Expiry follows the flush; the encrypted copies come after expiry.
      [[ -e "$SB/archives/fold-20200101-0230.dump" ]] || fail "$mode: expiry ran before the flush succeeded"
      [[ "$(find "$SB/archives" -maxdepth 1 -type f -printf '%f\n' | sort)" == "$before" ]] \
        || fail "$mode: a failed flush left or lost archives"
    fi
  done
  # With the flush working again the same state converges to a success.
  run_bk now
  expect_rc 0
  grep -q '"lastFailure":null' "$SB/status/status.json" || fail "success did not clear the flush failure"
}

case_long_running_health() {
  new_sandbox health
  export FOLD_BACKUP_TICK_SECS=1 FOLD_BACKUP_POLL_SECS=1 FOLD_BACKUP_HEALTH_MAX_AGE=3 STUB_SLEEP=9
  bash "$BACKUP_SH" loop >"$SB/loop.out" 2>"$SB/loop.err" &
  local pid=$! i
  # shellcheck disable=SC2064
  trap "kill $pid 2>/dev/null" EXIT
  wait_for 10 "[[ -f $STUB_DIR/dump.started ]]" || fail "the first backup never started"
  for i in 1 2 3; do
    run_bk health
    expect_rc 0
    sleep 2
  done
  [[ ! -e "$SB/status/LAST_OK" ]] || fail "the backup finished before it was stopped"
  kill -TERM "$pid"
  wait "$pid"
  local rc=$?
  [[ "$rc" == 0 ]] || fail "the loop exited $rc on TERM"
  if kill -0 "$(cat "$STUB_DIR/dump.pid")" 2>/dev/null; then fail "the dump outlived the loop"; fi
  [[ ! -e "$SB/status/LAST_OK" ]] || fail "LAST_OK exists for incomplete work"
  [[ -z "$(find "$SB/archives" -maxdepth 1 \( -name 'fold-*' -o -name 'uploads-*' -o -name '.partial.*' \))" ]] \
    || fail "files were published or left for incomplete work"
  run_bk health
  expect_rc 1
  trap - EXIT
}

case_health_states() {
  new_sandbox healthstates
  export FOLD_BACKUP_HEALTH_MAX_AGE=5
  run_bk health
  expect_rc 1
  printf '%s\n' "$$" >"$SB/heartbeat"
  run_bk health
  expect_rc 0
  touch -d '-10 seconds' "$SB/heartbeat"
  run_bk health
  expect_rc 1
  printf '999999999\n' >"$SB/heartbeat"
  run_bk health
  expect_rc 1
}

case_loop_resumes_after_recovery() {
  new_sandbox loopresume
  use_coord
  : >"$SB/coord/recovery.lock"
  export FOLD_BACKUP_TICK_SECS=1 FOLD_BACKUP_POLL_SECS=1
  bash "$BACKUP_SH" loop >"$SB/loop.out" 2>"$SB/loop.err" &
  local pid=$!
  # shellcheck disable=SC2064
  trap "kill $pid 2>/dev/null" EXIT
  sleep 4
  if grep -qE 'pg_dump|tar-create' "$STUB_LOG"; then fail "backup work ran under recovery.lock"; fi
  [[ -z "$(ls -A "$SB/status")" ]] || fail "status changed under recovery.lock"
  [[ "$(grep -c 'backup skipped' "$SB/loop.err")" -ge 1 ]] || fail "the loop did not report skipped runs"
  rm -f "$SB/coord/recovery.lock"
  wait_for 15 "[[ -f $SB/status/LAST_OK ]]" || fail "the loop did not catch up after recovery ended"
  kill -TERM "$pid"
  wait "$pid"
  trap - EXIT
}

case_uploads_validate() {
  new_sandbox validate
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  chmod 600 "$FOLD_BACKUP_RESTORE_ARCHIVE"
  touch -d '2026-01-01 00:00:00' "$FOLD_BACKUP_RESTORE_ARCHIVE"
  local sum mtime snap
  sum="$(sha_of "$FOLD_BACKUP_RESTORE_ARCHIVE")"
  mtime="$(stat -c %Y "$FOLD_BACKUP_RESTORE_ARCHIVE")"
  snap="$(snapshot "$SB/data")"
  run_bk uploads-validate --nonce "$N1"
  expect_rc 0
  [[ "$OUT" =~ ^validated\ members=([0-9]+)\ uncompressedBytes=([0-9]+)\ freeBytes=([0-9]+)$ ]] || fail "validate line: $OUT"
  [[ "${BASH_REMATCH[1]}" == 6 && "${BASH_REMATCH[2]}" == 8 ]] || fail "validate counts: $OUT"
  [[ "$(sha_of "$FOLD_BACKUP_RESTORE_ARCHIVE")" == "$sum" && "$(stat -c %Y "$FOLD_BACKUP_RESTORE_ARCHIVE")" == "$mtime" \
    && "$(mode_of "$FOLD_BACKUP_RESTORE_ARCHIVE")" == 600 ]] || fail "the archive was changed"
  [[ "$(snapshot "$SB/data")" == "$snap" ]] || fail "validate created something in /data"
  run_bk uploads-validate --nonce 0123456789abcdef
  expect_rc 64
  run_bk uploads-validate --nonce "${N1^^}"
  expect_rc 64
  run_bk uploads-validate
  expect_rc 64
  STUB_FREE=7 run_bk uploads-validate --nonce "$N1"
  expect_rc 1
  [[ "$ERR" == *"not enough free space"* ]] || fail "space refusal not explained"
  [[ "$(snapshot "$SB/data")" == "$snap" ]] || fail "a space refusal created something"
  # The dotted layout of the host helper's archives is accepted.
  "$REAL_RM" -rf "$SB/dot" && mkdir -p "$SB/dot/uploads/avatars/3"
  echo x >"$SB/dot/uploads/avatars/3/c.gif"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/dot" .
  run_bk uploads-validate --nonce "$N1"
  expect_rc 0
  # Unsafe archives.
  bad() { # NAME  (archive already written)
    run_bk uploads-validate --nonce "$N1"
    expect_rc 1
    [[ "$(snapshot "$SB/data")" == "$snap" ]] || fail "$1: refusal created something"
  }
  "$REAL_RM" -rf "$SB/u" && mkdir -p "$SB/u/uploads/avatars/1"
  echo x >"$SB/u/uploads/avatars/1/a.jpg"
  ln -s /etc/passwd "$SB/u/uploads/avatars/1/link"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/u" uploads
  bad symlink
  "$REAL_RM" -f "$SB/u/uploads/avatars/1/link"
  ln "$SB/u/uploads/avatars/1/a.jpg" "$SB/u/uploads/avatars/1/hard"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/u" uploads
  bad hardlink
  "$REAL_RM" -f "$SB/u/uploads/avatars/1/hard"
  mkfifo "$SB/u/uploads/avatars/1/pipe"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/u" uploads
  bad fifo
  "$REAL_RM" -f "$SB/u/uploads/avatars/1/pipe"
  echo x >"$SB/u/uploads/avatars/1/has space.jpg"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/u" uploads
  bad "space in a name"
  "$REAL_RM" -f "$SB/u/uploads/avatars/1/has space.jpg"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/u" -P --transform 's|^uploads|../uploads|' uploads
  bad "dot-dot member"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/u" -P --transform "s|^uploads|$SB/abs|" uploads
  bad "absolute member"
  mkdir -p "$SB/u/other"
  echo x >"$SB/u/other/x"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/u" other
  bad "member outside uploads"
  echo "not a tar" >"$FOLD_BACKUP_RESTORE_ARCHIVE"
  bad "not gzip"
  make_tgz "$SB/full.tgz"
  head -c 60 "$SB/full.tgz" >"$FOLD_BACKUP_RESTORE_ARCHIVE"
  bad "truncated"
  # Unsafe targets.
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  mkdir -p "$SB/elsewhere"
  ln -s "$SB/elsewhere" "$SB/data/.restore-$N1"
  run_bk uploads-validate --nonce "$N1"
  expect_rc 1
  rm -f "$SB/data/.restore-$N1"
  mv "$SB/data/uploads" "$SB/data/real-uploads"
  ln -s "$SB/data/real-uploads" "$SB/data/uploads"
  run_bk uploads-validate --nonce "$N1"
  expect_rc 1
}

stage_ok() { # run stage and require success
  run_bk uploads-stage --nonce "${1:-$N1}"
  expect_rc 0
  [[ "$OUT" =~ ^staged\ inode=([0-9]+)\ files=([0-9]+)$ ]] || fail "stage line: $OUT"
  STAGED_INO="${BASH_REMATCH[1]}"
  STAGED_FILES="${BASH_REMATCH[2]}"
}

observe() { # prints the observe line fields into O_*
  run_bk uploads-observe --nonce "$N1"
  expect_rc 0
  [[ "$OUT" =~ ^observe\ live=([0-9]+|none)\ staged=([0-9]+|none)\ complete=(yes|no)\ exchange=(none|intent|done)\ drop=(none|intent|done)\ receipt=(present|absent)$ ]] \
    || fail "observe line: $OUT"
  O_LIVE="${BASH_REMATCH[1]}" O_STAGED="${BASH_REMATCH[2]}" O_COMPLETE="${BASH_REMATCH[3]}"
  O_EXCHANGE="${BASH_REMATCH[4]}" O_DROP="${BASH_REMATCH[5]}" O_RECEIPT="${BASH_REMATCH[6]}"
}

case_uploads_lifecycle() {
  new_sandbox lifecycle
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  echo old >"$SB/data/uploads/old.txt"
  local live0
  live0="$(stat -c %i "$SB/data/uploads")"
  observe
  [[ "$O_LIVE" == "$live0" && "$O_STAGED" == none && "$O_COMPLETE" == no && "$O_RECEIPT" == absent ]] || fail "observe before: $OUT"
  run_bk uploads-exchange --probe --nonce "$N1"
  expect_rc 0
  [[ "$OUT" == "probe ok" ]] || fail "probe: $OUT"
  [[ -z "$(find "$SB/data" -maxdepth 1 -name '.restore-probe-*')" ]] || fail "the probe left a tree"
  [[ "$(stat -c %i "$SB/data/uploads")" == "$live0" && -f "$SB/data/uploads/old.txt" ]] || fail "the probe touched /data/uploads"
  stage_ok
  [[ "$STAGED_FILES" == 2 ]] || fail "staged files: $STAGED_FILES"
  local parent="$SB/data/.restore-$N1"
  [[ "$(mode_of "$parent")" == 700 ]] || fail "the staging parent is not private"
  [[ "$(stat -c %u:%g "$parent/uploads/avatars/1/a.jpg")" == "$(id -u):$(id -g)" ]] || fail "staged ownership"
  [[ "$(mode_of "$parent/uploads")" == 755 && "$(mode_of "$parent/uploads/avatars/1/a.jpg")" == 644 ]] || fail "staged modes"
  [[ -f "$SB/data/uploads/old.txt" ]] || fail "staging disturbed the live tree"
  observe
  [[ "$O_STAGED" == "$STAGED_INO" && "$O_COMPLETE" == yes && "$O_EXCHANGE" == none && "$O_RECEIPT" == present ]] || fail "observe after stage: $OUT"
  stage_ok
  [[ "$STAGED_INO" == "$O_STAGED" ]] || fail "a repeated stage rebuilt the tree"
  # Exchange.
  run_bk uploads-exchange --nonce "$N1" --expect-live "$live0" --expect-staged "$STAGED_INO"
  expect_rc 0
  [[ "$OUT" == "exchanged live=$STAGED_INO old=$live0" ]] || fail "exchange line: $OUT"
  [[ -f "$SB/data/uploads/avatars/1/a.jpg" && ! -e "$SB/data/uploads/old.txt" ]] || fail "live tree is not the staged one"
  [[ -f "$parent/uploads/old.txt" ]] || fail "the old tree is not at the staging path"
  : >"$SB/data/uploads/avatars/1/new-photo.jpg" || fail "the exchanged tree is not writable"
  observe
  [[ "$O_LIVE" == "$STAGED_INO" && "$O_STAGED" == "$live0" && "$O_EXCHANGE" == "done" ]] || fail "observe after exchange: $OUT"
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 0
  [[ "$OUT" == "exchanged live=$STAGED_INO old=$live0" ]] || fail "repeated exchange: $OUT"
  # Drop the old tree, then release.
  run_bk uploads-drop --nonce "$N1" --identity "$STAGED_INO"
  expect_rc 1
  [[ -d "$SB/data/uploads" && -d "$parent/uploads" ]] || fail "the wrong tree was dropped"
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 0
  [[ "$OUT" == "dropped identity=$live0" ]] || fail "drop line: $OUT"
  [[ ! -e "$parent/uploads" && -f "$parent/receipt" ]] || fail "drop left the tree or lost the receipt"
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 0
  observe
  [[ "$O_DROP" == "done" && "$O_STAGED" == none ]] || fail "observe after drop: $OUT"
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 0
  [[ "$OUT" == released && ! -e "$parent" ]] || fail "release: $OUT"
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 0
  # A probe killed mid-run leaves only an empty tree; the next probe removes it.
  STUB_MV_KILL=before run_bk uploads-exchange --probe --nonce "$N2"
  [[ "$RC" == 137 ]] || fail "the probe was not killed ($RC)"
  [[ -d "$SB/data/.restore-probe-$N2/a" ]] || fail "no leftover probe tree"
  [[ -z "$(find "$SB/data/.restore-probe-$N2" -type f)" ]] || fail "the leftover probe tree holds files"
  mkdir -p "$SB/data/.restore-probe-keepme"
  echo x >"$SB/data/.restore-probe-keepme/file"
  run_bk uploads-exchange --probe --nonce "$N1"
  expect_rc 0
  [[ ! -e "$SB/data/.restore-probe-$N2" ]] || fail "the next probe left the empty tree"
  [[ -f "$SB/data/.restore-probe-keepme/file" ]] || fail "the probe removed a non-empty tree"
}

case_uploads_interruptions() {
  new_sandbox interrupt
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  echo old >"$SB/data/uploads/old.txt"
  local live0 parent="$SB/data/.restore-$N1" staged_before
  live0="$(stat -c %i "$SB/data/uploads")"
  # 1. Killed during extraction: incomplete, never exchanged, retried from scratch.
  STUB_TAR_KILL=extract run_bk uploads-stage --nonce "$N1"
  [[ "$RC" == 137 ]] || fail "extraction was not killed ($RC)"
  observe
  [[ "$O_COMPLETE" == no && "$O_STAGED" != none ]] || fail "observe after a killed extraction: $OUT"
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 1
  [[ "$(stat -c %i "$SB/data/uploads")" == "$live0" ]] || fail "an incomplete stage was exchanged"
  stage_ok
  [[ ! -e "$parent/uploads/JUNK-PARTIAL" ]] || fail "the retry kept the half-extracted tree"
  [[ "$STAGED_FILES" == 2 ]] || fail "retry files: $STAGED_FILES"
  # Reset for the next scenario.
  run_bk uploads-drop --nonce "$N1" --identity "$STAGED_INO"
  expect_rc 0
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 0
  # 2. Killed after extraction finished but before it was acknowledged.
  "$REAL_RM" -f "$STUB_DIR/sync.n"
  STUB_SYNC_KILL_N=1 run_bk uploads-stage --nonce "$N1"
  [[ "$RC" == 137 ]] || fail "stage not killed after extraction ($RC)"
  observe
  [[ "$O_COMPLETE" == no ]] || fail "an unacknowledged extraction counted as complete"
  stage_ok
  [[ "$STAGED_FILES" == 2 ]] || fail "converged files"
  # 3. Killed after the completed receipt, before the acknowledgement.
  run_bk uploads-drop --nonce "$N1" --identity "$STAGED_INO"
  expect_rc 0
  run_bk uploads-drop --nonce "$N1" --release
  "$REAL_RM" -f "$STUB_DIR/sync.n"
  STUB_SYNC_KILL_N=2 run_bk uploads-stage --nonce "$N1"
  [[ "$RC" == 137 ]] || fail "stage not killed after the receipt ($RC)"
  observe
  [[ "$O_COMPLETE" == yes ]] || fail "the completed receipt was lost"
  staged_before="$O_STAGED"
  stage_ok
  [[ "$STAGED_INO" == "$staged_before" ]] || fail "a completed stage was rebuilt"
  # 4. Exchange killed before and after the swap.
  STUB_MV_KILL=before run_bk uploads-exchange --nonce "$N1"
  [[ "$RC" == 137 ]] || fail "exchange not killed before ($RC)"
  observe
  [[ "$O_LIVE" == "$live0" && "$O_EXCHANGE" == intent ]] || fail "before-swap observation: $OUT"
  STUB_MV_KILL=after run_bk uploads-exchange --nonce "$N1"
  [[ "$RC" == 137 ]] || fail "exchange not killed after ($RC)"
  observe
  [[ "$O_LIVE" == "$STAGED_INO" && "$O_STAGED" == "$live0" && "$O_EXCHANGE" == intent ]] || fail "after-swap observation: $OUT"
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 0
  [[ "$OUT" == "exchanged live=$STAGED_INO old=$live0" ]] || fail "converged exchange: $OUT"
  [[ "$(stat -c %i "$SB/data/uploads")" == "$STAGED_INO" ]] || fail "the exchange swapped twice"
  # 5. Drop killed during removal, then after removal before the acknowledgement.
  STUB_RM_KILL=during run_bk uploads-drop --nonce "$N1" --identity "$live0"
  [[ "$RC" == 137 ]] || fail "drop not killed during removal ($RC)"
  observe
  [[ "$O_DROP" == intent && "$O_STAGED" == "$live0" ]] || fail "observe during removal: $OUT"
  run_bk uploads-drop --nonce "$N1" --identity "$STAGED_INO"
  expect_rc 1
  [[ -d "$parent/uploads" ]] || fail "a mismatched identity dropped the tree"
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 0
  [[ ! -e "$parent/uploads" ]] || fail "the retried drop left the tree"
  # Reset and drop again, this time killed after removal.
  "$REAL_RM" -rf "$parent"
  "$REAL_RM" -rf "$SB/data/uploads" && mkdir -p "$SB/data/uploads"
  echo old >"$SB/data/uploads/old.txt"
  live0="$(stat -c %i "$SB/data/uploads")"
  stage_ok
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 0
  "$REAL_RM" -f "$STUB_DIR/sync.n"
  STUB_SYNC_KILL_N=1 run_bk uploads-drop --nonce "$N1" --identity "$live0"
  [[ "$RC" == 137 ]] || fail "drop not killed after removal ($RC)"
  [[ ! -e "$parent/uploads" ]] || fail "the tree survived the real removal"
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 0
  [[ "$OUT" == "dropped identity=$live0" ]] || fail "lost acknowledgement not resolved: $OUT"
  # A missing tree without intent refuses.
  run_bk uploads-drop --nonce "$N1" --identity 123456789
  expect_rc 1
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 0
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 1
}

case_uploads_refusals() {
  new_sandbox refusals
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  local live0 parent="$SB/data/.restore-$N1"
  live0="$(stat -c %i "$SB/data/uploads")"
  # Unsupported filesystem: nothing changes.
  stage_ok
  STUB_NOEXCHANGE=1 run_bk uploads-exchange --nonce "$N1"
  expect_rc 1
  [[ "$ERR" == *"cannot exchange"* ]] || fail "unsupported filesystem not explained"
  [[ "$(stat -c %i "$SB/data/uploads")" == "$live0" && "$(stat -c %i "$parent/uploads")" == "$STAGED_INO" ]] \
    || fail "a failed exchange changed a tree"
  observe
  [[ "$O_EXCHANGE" == none ]] || fail "a failed exchange left an intent: $OUT"
  STUB_NOEXCHANGE=1 run_bk uploads-exchange --probe --nonce "$N2"
  expect_rc 1
  [[ -z "$(find "$SB/data" -maxdepth 1 -name '.restore-probe-*')" ]] || fail "a failed probe left a tree"
  # A replaced staged folder (different inode) is never exchanged.
  "$REAL_MV" "$parent/uploads" "$parent/uploads.old"
  mkdir "$parent/uploads"
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 1
  [[ "$(stat -c %i "$SB/data/uploads")" == "$live0" ]] || fail "a mismatched inode was exchanged"
  # A different archive with the same nonce is unrelated.
  echo other >"$SB/tgzsrc/uploads/avatars/1/a.jpg"
  "$REAL_TAR" -czf "$FOLD_BACKUP_RESTORE_ARCHIVE" -C "$SB/tgzsrc" uploads
  run_bk uploads-stage --nonce "$N1"
  expect_rc 1
  # An unrelated existing staging path, and a symlink one.
  run_bk uploads-stage --nonce "$N2"
  expect_rc 0
  "$REAL_RM" -rf "$SB/data/.restore-$N2"
  mkdir -p "$SB/data/.restore-$N2"
  echo mine >"$SB/data/.restore-$N2/precious"
  run_bk uploads-stage --nonce "$N2"
  expect_rc 1
  [[ -f "$SB/data/.restore-$N2/precious" ]] || fail "an unrelated target was disturbed"
  "$REAL_RM" -rf "$SB/data/.restore-$N2"
  mkdir -p "$SB/elsewhere"
  ln -s "$SB/elsewhere" "$SB/data/.restore-$N2"
  run_bk uploads-stage --nonce "$N2"
  expect_rc 1
  [[ -z "$(ls -A "$SB/elsewhere")" ]] || fail "a symlinked ancestor was written through"
}

# A recorded tree that vanished with no drop is not a cleaned one: the release
# refuses however often it is asked and keeps the receipt, before and after the
# exchange. Once the tree is back and dropped under its recorded identity (or
# when the receipt records no tree at all) the release goes through.
case_uploads_release_needs_drop() {
  new_sandbox releasedrop
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  local live0 parent="$SB/data/.restore-$N1" i
  live0="$(stat -c %i "$SB/data/uploads")"
  stage_ok
  "$REAL_MV" "$parent/uploads" "$parent/held"
  for i in 1 2; do
    run_bk uploads-drop --nonce "$N1" --release
    expect_rc 1
    [[ "$ERR" == *"never confirmed"* && -f "$parent/receipt" && -d "$parent/held" ]] || fail "a vanished stage was released ($i): $ERR"
  done
  "$REAL_MV" "$parent/held" "$parent/uploads"
  run_bk uploads-drop --nonce "$N1" --identity "$STAGED_INO"
  expect_rc 0
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 0
  [[ "$OUT" == released && ! -e "$parent" ]] || fail "a confirmed drop was not released: $OUT"
  # After the exchange the old tree is the recorded one.
  stage_ok
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 0
  "$REAL_MV" "$parent/uploads" "$parent/held"
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 1
  [[ -f "$parent/receipt" && -d "$parent/held" ]] || fail "a vanished old tree was released"
  "$REAL_MV" "$parent/held" "$parent/uploads"
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 0
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 0
  [[ ! -e "$parent" ]] || fail "the old tree's confirmed drop was not released"
}

# A stage killed around its first two receipt writes. Kill 1 leaves only the
# unpublished receipt; kill 2 leaves the staged folder created, empty and
# unnamed by a pending receipt. Both must converge by a retry and by an abort
# (drop, then release), without touching the live tree or any other path.
case_uploads_receipt_window() {
  new_sandbox receiptwindow
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  echo old >"$SB/data/uploads/old.txt"
  local live0 parent="$SB/data/.restore-$N1" n after sino
  live0="$(stat -c %i "$SB/data/uploads")"
  for n in 1 2; do
    for after in resume abort; do
      "$REAL_RM" -rf "$parent" "$STUB_DIR/receipt.n"
      STUB_RECEIPT_KILL_N="$n" run_bk uploads-stage --nonce "$N1"
      [[ "$RC" == 137 ]] || fail "kill $n: stage was not killed ($RC)"
      [[ "$(stat -c %i "$SB/data/uploads")" == "$live0" && -f "$SB/data/uploads/old.txt" ]] || fail "kill $n: the live tree changed"
      if [[ "$n" == 1 ]]; then
        [[ -e "$parent/.receipt.new" && ! -e "$parent/receipt" && ! -e "$parent/uploads" ]] || fail "kill 1 left an unexpected layout"
        observe
        [[ "$O_RECEIPT" == absent && "$O_STAGED" == none ]] || fail "kill 1 observation: $OUT"
      else
        [[ -f "$parent/receipt" && -d "$parent/uploads" ]] || fail "kill 2 left an unexpected layout"
        grep -qx 'stageState=pending' "$parent/receipt" || fail "kill 2 receipt is not pending"
        grep -qx 'stageInode=' "$parent/receipt" || fail "kill 2 receipt names an inode"
        [[ -z "$(find "$parent/uploads" -mindepth 1 -print -quit)" ]] || fail "kill 2 folder is not empty"
        observe
        [[ "$O_RECEIPT" == present && "$O_STAGED" != none && "$O_COMPLETE" == no ]] || fail "kill 2 observation: $OUT"
        sino="$O_STAGED"
        run_bk uploads-exchange --nonce "$N1"
        expect_rc 1
        [[ "$(stat -c %i "$SB/data/uploads")" == "$live0" ]] || fail "an unnamed stage was exchanged"
      fi
      if [[ "$after" == resume ]]; then
        stage_ok
        [[ "$STAGED_FILES" == 2 && -f "$parent/uploads/avatars/1/a.jpg" ]] || fail "kill $n: the retry did not converge"
        run_bk uploads-drop --nonce "$N1" --identity "$STAGED_INO"
        expect_rc 0
      elif [[ "$n" == 2 ]]; then
        # Only the empty folder this attempt made may go: never another inode,
        # never a folder that holds anything.
        run_bk uploads-drop --nonce "$N1" --identity "$live0"
        expect_rc 1
        run_bk uploads-drop --nonce "$N1" --identity 123456789
        expect_rc 1
        echo stray >"$parent/uploads/stray"
        run_bk uploads-drop --nonce "$N1" --identity "$sino"
        expect_rc 1
        [[ -f "$parent/uploads/stray" ]] || fail "a folder with content was dropped"
        "$REAL_RM" -f "$parent/uploads/stray"
        "$REAL_RM" -f "$STUB_DIR/sync.n"
        STUB_SYNC_KILL_N=1 run_bk uploads-drop --nonce "$N1" --identity "$sino"
        [[ "$RC" == 137 ]] || fail "drop not killed after removal ($RC)"
        [[ ! -e "$parent/uploads" ]] || fail "the empty folder survived its drop"
        run_bk uploads-drop --nonce "$N1" --identity "$sino"
        expect_rc 0
        [[ "$OUT" == "dropped identity=$sino" ]] || fail "retried drop: $OUT"
      fi
      run_bk uploads-drop --nonce "$N1" --release
      expect_rc 0
      [[ ! -e "$parent" ]] || fail "kill $n/$after: release left the staging folder"
      [[ "$(stat -c %i "$SB/data/uploads")" == "$live0" && -f "$SB/data/uploads/old.txt" ]] || fail "kill $n/$after: the live tree changed"
    done
  done
}

case_uploads_drop_ownership() {
  new_sandbox dropown
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  echo old >"$SB/data/uploads/old.txt"
  local parent="$SB/data/.restore-$N1" live0 other
  live0="$(stat -c %i "$SB/data/uploads")"
  stage_ok
  # Before the exchange: a tree someone else put at the staging path is not the staged one.
  "$REAL_MV" "$parent/uploads" "$parent/uploads.aside"
  mkdir "$parent/uploads"
  echo precious >"$parent/uploads/precious"
  other="$(stat -c %i "$parent/uploads")"
  observe
  [[ "$O_COMPLETE" == no ]] || fail "a replaced tree counted as a complete stage: $OUT"
  run_bk uploads-drop --nonce "$N1" --identity "$other"
  expect_rc 1
  [[ -f "$parent/uploads/precious" ]] || fail "a replacement tree was dropped"
  observe
  [[ "$O_DROP" == none ]] || fail "a refused drop left an intent: $OUT"
  # Putting the real tree back makes the stage complete again, and it is droppable.
  "$REAL_MV" "$parent/uploads" "$parent/uploads.other"
  "$REAL_MV" "$parent/uploads.aside" "$parent/uploads"
  observe
  [[ "$O_COMPLETE" == yes ]] || fail "the restored tree is not complete: $OUT"
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 0
  # After the exchange: only the old live tree is droppable, not a replacement.
  "$REAL_MV" "$parent/uploads" "$parent/uploads.old"
  mkdir "$parent/uploads"
  echo precious >"$parent/uploads/precious"
  other="$(stat -c %i "$parent/uploads")"
  run_bk uploads-drop --nonce "$N1" --identity "$other"
  expect_rc 1
  [[ -f "$parent/uploads/precious" ]] || fail "a replacement tree was dropped after the exchange"
  "$REAL_RM" -rf "$parent/uploads"
  "$REAL_MV" "$parent/uploads.old" "$parent/uploads"
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 0
  [[ ! -e "$parent/uploads" ]] || fail "the recorded old tree was not dropped"
}

case_uploads_drop_symlink_parent() {
  new_sandbox dropsym
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  echo old >"$SB/data/uploads/old.txt"
  local parent="$SB/data/.restore-$N1" moved="$SB/elsewhere-parent" live0 receipt_before
  live0="$(stat -c %i "$SB/data/uploads")"
  stage_ok
  run_bk uploads-exchange --nonce "$N1"
  expect_rc 0
  # The staging parent moves out of the volume and a link takes its place; the
  # receipt and the old tree behind it still carry the recorded identities.
  "$REAL_MV" "$parent" "$moved"
  ln -s "$moved" "$parent"
  receipt_before="$(sha_of "$moved/receipt")"
  run_bk uploads-drop --nonce "$N1" --identity "$live0"
  expect_rc 1
  [[ "$ERR" == *"not a plain folder"* ]] || fail "a symlinked parent was not explained: $ERR"
  [[ "$(sha_of "$moved/receipt")" == "$receipt_before" && -f "$moved/uploads/old.txt" ]] \
    || fail "a drop wrote or removed through a symlinked parent"
  # A release refuses the same parent.
  run_bk uploads-drop --nonce "$N1" --release
  expect_rc 1
  [[ -f "$moved/uploads/old.txt" && -f "$moved/receipt" ]] || fail "a release went through a symlinked parent"
}

case_scratch_cleanup() {
  new_sandbox scratch
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  # A normal run, a refused run and a failed run each remove their scratch
  # folder; only SIGKILL can leave one, and it stays inside the sandbox.
  run_bk uploads-validate --nonce "$N1"
  expect_rc 0
  [[ -z "$(ls -A "$SB/tmp")" ]] || fail "uploads-validate left scratch files: $(ls "$SB/tmp")"
  echo not-a-tgz >"$FOLD_BACKUP_RESTORE_ARCHIVE"
  run_bk uploads-validate --nonce "$N1"
  expect_rc 1
  [[ -z "$(ls -A "$SB/tmp")" ]] || fail "a failed validation left scratch files"
  run_bk uploads-validate --nonce "$N1" --bogus
  expect_rc 64
  [[ -z "$(ls -A "$SB/tmp")" ]] || fail "a refused run left scratch files"
  # A scratch folder an hour old is a killed run's: the next run removes it, and
  # leaves a younger one (a live run's) alone.
  mkdir "$SB/tmp/fold-backup.old" "$SB/tmp/fold-backup.live"
  touch -d '2 hours ago' "$SB/tmp/fold-backup.old"
  make_tgz "$FOLD_BACKUP_RESTORE_ARCHIVE"
  run_bk uploads-validate --nonce "$N1"
  expect_rc 0
  [[ ! -e "$SB/tmp/fold-backup.old" && -d "$SB/tmp/fold-backup.live" ]] || fail "stale scratch handling: $(ls "$SB/tmp")"
  rmdir "$SB/tmp/fold-backup.live"
  STUB_TAR_KILL=extract run_bk uploads-stage --nonce "$N1"
  [[ "$RC" == 137 ]] || fail "the staging was not killed ($RC)"
  [[ "$(find "$SB/tmp" -type d -name 'fold-backup.*' | wc -l)" -le 1 ]] || fail "one run left more than one scratch folder"
}

# A second deployment of the producer: its own archives, status, uploads and scratch
# folders, the same stubs and the same bucket. run_second ID runs it; write_hook ID
# makes the bucket's hook run it, from inside the first producer's PUT.
second_env() {
  local p2="$SB/p2"
  mkdir -p "$p2"/{archives,status,data/uploads/avatars/1,tmp}
  echo uploadtwo >"$p2/data/uploads/avatars/1/a.jpg"
  SECOND_ENV=(FOLD_BACKUP_ARCHIVE_DIR="$p2/archives" FOLD_BACKUP_STATUS_DIR="$p2/status" FOLD_BACKUP_DATA_DIR="$p2/data"
    FOLD_BACKUP_HEARTBEAT="$p2/heartbeat" TMPDIR="$p2/tmp" OD_ID="$1" BUCKET_TAG=image2)
}

run_second() {
  second_env "$1"
  RC2=0
  env "${SECOND_ENV[@]}" bash "$BACKUP_SH" now >"$SB/p2/out" 2>"$SB/p2/err" || RC2=$?
  OUT2="$(cat "$SB/p2/out")"
  bucket_clean || fail "the second producer listed, read or deleted something in the bucket"
}

write_hook() {
  second_env "$1"
  {
    printf 'env'
    printf ' %q' "${SECOND_ENV[@]}"
    printf ' bash %q now >%q 2>%q\necho $? >%q\n' "$BACKUP_SH" "$SB/p2/out" "$SB/p2/err" "$SB/p2/rc"
  } >"$BUCKET/hook"
}

# Two deployments, one bucket, one minute. Neither reads the bucket, so the only way
# they meet is the key: each writes its own folder, in either order and with the second
# running between the first one's two PUTs, and no object of either is ever replaced.
case_offsite_interleave() {
  local now_e stamp snap f
  now_e="$(date +%s)"
  stamp="$(date -u -d "@$now_e" +%Y%m%d-%H%M)"
  fresh() { new_sandbox "$1"; use_offsite; export FOLD_BACKUP_NOW_EPOCH="$now_e"; }
  # A foreign complete folder of the same minute is never touched and does not move the stamp.
  fresh interleave-foreign
  bucket_seed_pair "$stamp" "$FID"
  snap="$(bucket_snapshot)"
  OD_ID=$ID1 run_bk now
  expect_rc 0
  [[ "$OUT" == *"stamp=$stamp "* ]] || fail "a foreign folder moved the stamp: $OUT"
  bucket_check "$stamp:$FID" "$stamp:$ID1" || fail "keys after the foreign folder: $(bucket_objects | tr '\n' ' ')"
  [[ "$(bucket_snapshot | grep -- "-$FID/")" == "$snap" ]] || fail "the foreign folder changed"
  # Sequential, both orders: the earlier deployment's objects are the same bytes after the later one ran.
  fresh interleave-ab
  OD_ID=$ID1 run_bk now
  expect_rc 0
  snap="$(bucket_snapshot)"
  run_second $ID2
  [[ "$RC2" == 0 && "$OUT2" == *"stamp=$stamp "* ]] || fail "the second deployment did not use the same minute: $RC2 $OUT2"
  bucket_check "$stamp:$ID1" "$stamp:$ID2" || fail "keys, first then second: $(bucket_objects | tr '\n' ' ')"
  [[ "$(bucket_snapshot | grep -- "-$ID1/")" == "$snap" ]] || fail "the first deployment's objects changed"
  bucket_each_key_once || fail "a key was written twice, first then second"
  fresh interleave-ba
  run_second $ID2
  [[ "$RC2" == 0 ]] || fail "the second deployment failed: $(cat "$SB/p2/err")"
  snap="$(bucket_snapshot)"
  OD_ID=$ID1 run_bk now
  expect_rc 0
  [[ "$OUT" == *"stamp=$stamp "* ]] || fail "the first deployment moved its stamp: $OUT"
  bucket_check "$stamp:$ID1" "$stamp:$ID2" || fail "keys, second then first: $(bucket_objects | tr '\n' ' ')"
  [[ "$(bucket_snapshot | grep -- "-$ID2/")" == "$snap" ]] || fail "the other deployment's objects changed"
  bucket_each_key_once || fail "a key was written twice, second then first"
  # The second deployment runs between the first one's two PUTs.
  fresh interleave-hook
  write_hook $ID2
  echo "2 hook" >>"$BUCKET/plan"
  OD_ID=$ID1 run_bk now
  expect_rc 0
  [[ "$(cat "$SB/p2/rc")" == 0 ]] || fail "the hooked deployment failed: $(cat "$SB/p2/err")"
  bucket_check "$stamp:$ID1" "$stamp:$ID2" || fail "keys, with a run between the PUTs: $(bucket_objects | tr '\n' ' ')"
  bucket_each_key_once || fail "a key was written twice, with a run between the PUTs"
  [[ "$(bucket_attempts)" == 4 ]] || fail "$(bucket_attempts) PUT invocations, not 4"
  for f in "fold-$stamp.dump.age" "uploads-$stamp.tgz.age"; do
    cmp -s "$BUCKET/objects/fold/$stamp-$ID1/$f" "$SB/archives/$f" || fail "first deployment's $f is not its own bytes"
    cmp -s "$BUCKET/objects/fold/$stamp-$ID2/$f" "$SB/p2/archives/$f" || fail "second deployment's $f is not its own bytes"
  done
}

# An upload that fails leaves the local pair, LAST_OK and the earlier objects as they were.
# A run that failed before its second member leaves one object in a folder nothing completes;
# one whose response was lost leaves a complete folder. The next run, here or on another
# deployment in the same minute, takes a folder of its own and never writes the old key.
case_offsite_failures() {
  new_sandbox offfail
  use_offsite
  local now_e e0 s0 stamp s2 ok half
  now_e="$(date +%s)"
  e0=$((now_e - 3600))
  s0="$(date -u -d "@$e0" +%Y%m%d-%H%M)"
  stamp="$(date -u -d "@$now_e" +%Y%m%d-%H%M)"
  FOLD_BACKUP_NOW_EPOCH=$e0 OD_ID=$ID1 run_bk now
  expect_rc 0
  ok="$(cat "$SB/status/LAST_OK")"
  # The second member never arrives.
  export FOLD_BACKUP_NOW_EPOCH="$now_e"
  bucket_plan_next 2 fail-before
  OD_ID=$ID2 run_bk now
  expect_rc 1
  [[ "$(cat "$SB/status/LAST_OK")" == "$ok" ]] || fail "LAST_OK moved after a failed upload"
  grep -q '"lastFailure":{"at":"[0-9TZ:-]*","step":"upload"}' "$SB/status/status.json" || fail "the failure step is not upload"
  [[ -f "$SB/archives/fold-$stamp.dump" && -f "$SB/archives/uploads-$stamp.tgz" ]] || fail "the local pair was not kept"
  [[ "$ERR" == *"offsite: uploading to fold/$stamp-$ID2/"* ]] || fail "the folder was not logged"
  half="$(grep -- "-$ID2/" "$BUCKET/attempts" | head -n 1 | cut -d' ' -f3)"
  [[ -n "$half" && "$half" == fold/$stamp-$ID2/* ]] || fail "no first member was attempted under the run's folder"
  [[ "$(bucket_objects)" == "$(printf '%s\n%s\n' "$(bucket_expect "$s0:$ID1")" "$half" | LC_ALL=C sort)" ]] \
    || fail "the bucket is not the first pair plus one member: $(bucket_objects | tr '\n' ' ')"
  # The other deployment, the same minute: a complete folder of its own, the half left alone.
  run_second $FID
  [[ "$RC2" == 0 && "$OUT2" == *"stamp=$stamp "* ]] || fail "the other deployment: $RC2 $OUT2"
  [[ "$(bucket_objects)" == "$(printf '%s\n%s\n' "$(bucket_expect "$s0:$ID1" "$stamp:$FID")" "$half" | LC_ALL=C sort)" ]] \
    || fail "the bucket after the other deployment: $(bucket_objects | tr '\n' ' ')"
  bucket_each_key_once || fail "the earlier key was written again"
  # A response lost after the second member committed: a complete folder, a failed run.
  new_sandbox offfail-lost
  use_offsite
  FOLD_BACKUP_NOW_EPOCH=$e0 OD_ID=$ID1 run_bk now
  expect_rc 0
  ok="$(cat "$SB/status/LAST_OK")"
  export FOLD_BACKUP_NOW_EPOCH="$now_e"
  bucket_plan_next 2 commit-then-fail
  OD_ID=$ID2 run_bk now
  expect_rc 1
  [[ "$(cat "$SB/status/LAST_OK")" == "$ok" ]] || fail "LAST_OK moved after a lost response"
  grep -q '"lastFailure":{"at":"[0-9TZ:-]*","step":"upload"}' "$SB/status/status.json" || fail "the lost response is not recorded as upload"
  bucket_check "$s0:$ID1" "$stamp:$ID2" || fail "the committed folder: $(bucket_objects | tr '\n' ' ')"
  [[ "$ERR" == *"offsite: uploading to fold/$stamp-$ID2/"* ]] || fail "the folder of the failed run was not logged"
  OD_ID=$FID run_bk now
  expect_rc 0
  s2="$(sed -n 's/^published stamp=\([0-9-]*\) .*/\1/p' <<<"$OUT")"
  [[ -n "$s2" && "$s2" != "$stamp" ]] || fail "the next run reused the stamp of the failed one: $OUT"
  bucket_check "$s0:$ID1" "$stamp:$ID2" "$s2:$FID" || fail "keys after the next run: $(bucket_objects | tr '\n' ' ')"
  bucket_each_key_once || fail "a key was written twice after a lost response"
}

# A run id that cannot be made well means no PUT at all: the local pair stays, the run fails
# at the upload step, LAST_OK stays, and expiry still ran.
case_run_id_refused() {
  new_sandbox runid
  use_offsite
  local now_e ok mode n
  now_e="$(date +%s)"
  FOLD_BACKUP_NOW_EPOCH=$((now_e - 7200)) run_bk now
  expect_rc 0
  ok="$(cat "$SB/status/LAST_OK")"
  export FOLD_BACKUP_NOW_EPOCH="$now_e"
  for mode in fail short nonhex upper empty; do
    : >"$SB/archives/fold-20200101-0230.dump"
    touch -d '2020-01-02' "$SB/archives/fold-20200101-0230.dump"
    n="$(bucket_attempts)"
    OD_MODE=$mode run_bk now
    expect_rc 1
    [[ "$(bucket_attempts)" == "$n" ]] || fail "$mode: a PUT was attempted without a good run id"
    [[ "$(cat "$SB/status/LAST_OK")" == "$ok" ]] || fail "$mode: LAST_OK moved"
    grep -q '"lastFailure":{"at":"[0-9TZ:-]*","step":"upload"}' "$SB/status/status.json" || fail "$mode: failure step"
    [[ "$ERR" == *"no random folder name could be made"* ]] || fail "$mode: the message is missing: $ERR"
    [[ ! -e "$SB/archives/fold-20200101-0230.dump" ]] || fail "$mode: expiry did not run"
  done
  [[ "$(find "$SB/archives" -maxdepth 1 -name 'fold-*.dump' | wc -l)" == 6 ]] || fail "the local pairs were not kept"
}

# Neither the nightly run nor a recovery needs anything but PUT permission, and the source says so.
case_offsite_source_guard() {
  bucket_selftest || fail "the bucket model's own checks failed"
  bucket_source_guard "$BACKUP_SH" 'fold/${STAMP}-${RUN_ID}/' \
    || fail "the producer lists, reads or heads the bucket, writes a flat key, or has no run folder"
}

# ------------------------------------------------------------------ runner

ALL_CASES=(help_and_self_test retention_boundaries schedule_boundaries partial_config tar_before_dump
  pair_completion_and_modes status_format failure_at_each_step ciphertext_only_upload offsite_interleave offsite_failures
  run_id_refused offsite_source_guard secret_sentinels
  recovery_refusal contention restore_test long_running_health health_states loop_resumes_after_recovery
  uploads_validate uploads_lifecycle uploads_interruptions uploads_refusals uploads_release_needs_drop uploads_receipt_window uploads_drop_ownership
  uploads_drop_symlink_parent sync_failures scratch_cleanup)

run_case() {
  local name="$1" rc=0
  echo "== $name"
  ( "case_$name" ) || rc=$?
  if (( rc != 0 )); then
    echo "-- FAILED: $name" >&2
    return 1
  fi
  echo "-- ok: $name"
}

# mutate NAME SED_SCRIPT: write a mutant of backup.sh and require a change.
mutate() {
  local out="$WORK/mutant-$1.sh"
  sed -e "$2" "$HERE/backup.sh" >"$out"
  if cmp -s "$out" "$HERE/backup.sh"; then
    echo "probe $1: the mutation did not apply" >&2
    return 1
  fi
  chmod +x "$out"
  MUTANT="$out"
}

probe() { # NAME CASE
  echo "== probe $1 must fail case $2"
  if BACKUP_SH="$MUTANT" bash "${BASH_SOURCE[0]}" --only "$2" >/dev/null 2>&1; then
    echo "-- PROBE SURVIVED: mutation '$1' did not make $2 fail" >&2
    return 1
  fi
  echo "-- ok: mutation '$1' made $2 fail"
}

run_probes() {
  local bad=0 out
  if mutate retention 's/if (( age > limit )); then # probe:retention/if (( age > limit * 100 )); then/'; then
    probe retention retention_boundaries || bad=1
  else
    bad=1
  fi
  if mutate encrypted-only-upload 's/for c in "${CIPHERS\[@\]}"; do/for c in "$PUB_TGZ" "$PUB_DUMP"; do/'; then
    probe encrypted-only-upload ciphertext_only_upload || bad=1
  else
    bad=1
  fi
  if mutate flat-key 's|${REMOTE_PREFIX}${1##\*/}|fold/${1##*/}|'; then
    probe flat-key ciphertext_only_upload || bad=1
  else
    bad=1
  fi
  if mutate recovery-refusal 's/if \[\[ -e "$dir\/recovery.lock" || -L "$dir\/recovery.lock" \]\]; then # probe:recovery/if false; then/'; then
    probe recovery-refusal recovery_refusal || bad=1
  else
    bad=1
  fi
  if mutate empty-stage-adoption 's/^  adopt_empty_stage$/  :/'; then
    probe empty-stage-adoption uploads_receipt_window || bad=1
  else
    bad=1
  fi
  if mutate release-needs-drop 's/if \[\[ -n "\$R_STAGE_INO" \&\& "\$R_DROP_DONE" != "\$(drop_identity)" \]\]; then/if false; then/'; then
    probe release-needs-drop uploads_release_needs_drop || bad=1
  else
    bad=1
  fi
  out="$WORK/mutant-order.sh"
  awk '/# probe:order-uploads/ { held = $0; next } { print } /# probe:order-dump/ { print held }' "$HERE/backup.sh" >"$out"
  if cmp -s "$out" "$HERE/backup.sh"; then
    echo "probe order: the mutation did not apply" >&2
    bad=1
  else
    chmod +x "$out"
    MUTANT="$out"
    probe tar-after-dump tar_before_dump || bad=1
  fi
  return "$bad"
}

main() {
  local c rc=0
  case "${1:-}" in
    --list) printf '%s\n' "${ALL_CASES[@]}"; return 0 ;;
    --only)
      [[ -n "${2:-}" ]] || { echo "--only needs a case" >&2; return 2; }
      run_case "$2"
      return $?
      ;;
    --probes) run_probes; return $? ;;
    "") ;;
    *) echo "unknown option: $1" >&2; return 2 ;;
  esac
  for c in "${ALL_CASES[@]}"; do
    run_case "$c" || rc=1
  done
  if (( rc == 0 )); then echo "backup-test: all cases passed"; else echo "backup-test: FAILURES" >&2; fi
  return "$rc"
}

main "$@"
