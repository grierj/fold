#!/usr/bin/env bash
# A fake S3 bucket for the offsite tests of both producers. Sourced by the PATH
# stub that stands in for rclone in deploy/backup/backup-test.sh. It is a test
# file: nothing ships it.
#
# BUCKET is a directory holding
#   objects/<full key>   what a producer committed, by its whole key
#   attempts             one line per PUT invocation: "attempt TAG KEY SHA256"
#   forbidden            written by any call that lists, reads, heads or deletes
#   plan                 lines "N MODE": what the Nth PUT invocation, counted across
#                        every producer, does. fail-before: no commit, exit 1.
#                        commit-then-fail: commit, then exit 1 (a lost response).
#                        hook: run $BUCKET/hook first, as a second producer would
#                        run, then commit.
#   put.n                the PUT invocation counter
# Clients' own retries inside one invocation never reach a PATH stub, so one
# attempt line is one CLI invocation.
# The guard's patterns are single-quoted on purpose: they name text in the producers.
# shellcheck shell=bash disable=SC2016

# bucket_reset DIR: an empty bucket at DIR, exported as BUCKET.
bucket_reset() {
  BUCKET="$1"
  export BUCKET
  rm -rf -- "${BUCKET:?}"
  mkdir -p "$BUCKET/objects"
  : >"$BUCKET/attempts"
}

# bucket_forbidden_call WHAT...: for a stub. Record the call and exit 2.
bucket_forbidden_call() {
  printf '%s\n' "$*" >>"$BUCKET/forbidden"
  exit 2
}

# bucket_key_ok KEY: a key under fold/, with no dot-dot, empty segment or trailing slash.
bucket_key_ok() {
  [[ "$1" == fold/* && "$1" != *..* && "$1" != *//* && "$1" != */ ]]
}

# bucket_put TAG SRC KEY: one PUT invocation. Returns its exit status.
bucket_put() {
  local tag="$1" src="$2" key="$3" n mode
  bucket_key_ok "$key" || bucket_forbidden_call "put with the key $key"
  n=$(( $(cat "$BUCKET/put.n" 2>/dev/null || echo 0) + 1 ))
  echo "$n" >"$BUCKET/put.n"
  printf 'attempt %s %s %s\n' "$tag" "$key" "$(sha256sum <"$src" | cut -d' ' -f1)" >>"$BUCKET/attempts"
  mode="$(awk -v n="$n" '$1 == n { print $2 }' "$BUCKET/plan" 2>/dev/null || true)"
  case "$mode" in
    fail-before) return 1 ;;
    hook) bash "$BUCKET/hook" || return 1 ;;
  esac
  mkdir -p "$BUCKET/objects/${key%/*}" && cp -- "$src" "$BUCKET/objects/$key" || return 1
  [[ "$mode" != commit-then-fail ]]
}

# bucket_plan_next K MODE: the Kth PUT invocation from now does MODE.
bucket_plan_next() {
  echo "$(( $(cat "$BUCKET/put.n" 2>/dev/null || echo 0) + $1 )) $2" >>"$BUCKET/plan"
}

# bucket_objects: every committed key, sorted.
bucket_objects() {
  (cd "$BUCKET/objects" && find . -type f | sed 's|^\./||' | LC_ALL=C sort)
}

# bucket_snapshot: every committed key with its hash.
bucket_snapshot() {
  (cd "$BUCKET/objects" && find . -type f -exec sha256sum {} + | sed 's| \./| |' | LC_ALL=C sort -k2)
}

# bucket_expect STAMP:ID...: the keys of one complete pair per folder, sorted.
bucket_expect() {
  local p stamp id
  for p in "$@"; do
    stamp="${p%%:*}"
    id="${p#*:}"
    printf 'fold/%s-%s/fold-%s.dump.age\nfold/%s-%s/uploads-%s.tgz.age\n' "$stamp" "$id" "$stamp" "$stamp" "$id" "$stamp"
  done | LC_ALL=C sort
}

# bucket_check STAMP:ID...: the committed keys are exactly one complete pair under
# each folder named, every stamp is YYYYMMDD-HHMM and every id 32 lowercase hex.
bucket_check() {
  local p
  for p in "$@"; do
    [[ "${p%%:*}" =~ ^[0-9]{8}-[0-9]{4}$ && "${p#*:}" =~ ^[0-9a-f]{32}$ ]] || return 1
  done
  [[ "$(bucket_objects)" == "$(bucket_expect "$@")" ]]
}

# bucket_seed_pair STAMP ID: another run's complete folder, built with the grammar.
bucket_seed_pair() {
  local k
  for k in $(bucket_expect "$1:$2"); do
    mkdir -p "$BUCKET/objects/${k%/*}"
    printf 'FOREIGN %s\n' "$k" >"$BUCKET/objects/$k"
  done
}

# bucket_each_key_once: no key was the target of two PUT invocations.
bucket_each_key_once() {
  [[ -z "$(cut -d' ' -f3 "$BUCKET/attempts" | sort | uniq -d)" ]]
}

# bucket_attempts: how many PUT invocations there were.
bucket_attempts() { wc -l <"$BUCKET/attempts" | tr -d ' '; }

# bucket_clean: no call listed, read, headed or deleted anything.
bucket_clean() { [[ ! -e "$BUCKET/forbidden" ]]; }

# bucket_od_stub FILE: a stand-in for od that answers the run id's one read of
# /dev/urandom with OD_ID (32 hex characters) or, by OD_MODE, with something a
# producer must refuse: fail, short, nonhex, upper or empty. Any other call is
# the real od, whose path is in REAL_OD.
bucket_od_stub() {
  cat >"$1" <<'EOF'
#!/usr/bin/env bash
if [[ "$*" != "-An -N16 -tx1 /dev/urandom" ]]; then exec "$REAL_OD" "$@"; fi
id="${OD_ID:-00112233445566778899aabbccddeeff}"
case "${OD_MODE:-}" in
  fail) exit 1 ;;
  short) id="${id:0:30}" ;;
  nonhex) id="${id:0:30}zz" ;;
  upper) id="${id^^}" ;;
  empty) exit 0 ;;
esac
printf ' %s\n' "$(sed 's/../& /g; s/ $//' <<<"$id")"
EOF
  chmod +x "$1"
}

# bucket_source_guard FILE LITERAL: the producer holds LITERAL (its run folder) and
# none of the calls or key shapes that would read or list the bucket or write a flat key.
bucket_source_guard() {
  local f="$1" lit="$2" bad
  grep -qF -- "$lit" "$f" || return 1
  for bad in 'list-objects' 's3api' 'head-object' 'get-object' ' lsf' '--immutable' 'fold/${base}' 'fold/${1##*/}'; do
    if grep -qF -- "$bad" "$f"; then return 1; fi
  done
}

# bucket_selftest: the checks above reject what they exist to reject.
bucket_selftest() {
  local saved="${BUCKET:-}" dir s=20261117-0102 id=0123456789abcdef0123456789abcdef k
  local id2=fedcba9876543210fedcba9876543210
  dir="$(mktemp -d)"
  bucket_reset "$dir/b"
  bucket_seed_pair "$s" "$id"
  bucket_check "$s:$id" || { echo "bucket model: a good pair was refused" >&2; return 1; }
  bucket_check "$s:$id" "$s:$id2" && { echo "bucket model: a missing folder was accepted" >&2; return 1; }
  bucket_seed_pair "$s" "$id2"
  bucket_check "$s:$id2" "$s:$id" || { echo "bucket model: two good folders were refused" >&2; return 1; }
  bucket_check "$s:$id" && { echo "bucket model: a second folder was accepted for one" >&2; return 1; }
  bad_ledger() { # WHAT: the case is built by the caller, the check must refuse it
    if bucket_check "$@"; then echo "bucket model: accepted $WHAT" >&2; rm -rf -- "$dir"; return 1; fi
  }
  WHAT="mismatched stamps"
  bucket_reset "$dir/b"; mkdir -p "$BUCKET/objects/fold/$s-$id"
  : >"$BUCKET/objects/fold/$s-$id/fold-$s.dump.age"; : >"$BUCKET/objects/fold/$s-$id/uploads-20261117-0103.tgz.age"
  bad_ledger "$s:$id" || return 1
  WHAT="two dump members"
  bucket_reset "$dir/b"; mkdir -p "$BUCKET/objects/fold/$s-$id"
  : >"$BUCKET/objects/fold/$s-$id/fold-$s.dump.age"; : >"$BUCKET/objects/fold/$s-$id/fold-20261117-0103.dump.age"
  bad_ledger "$s:$id" || return 1
  WHAT="a 31-character id"
  bucket_reset "$dir/b"; k="${id:0:31}"; bucket_seed_pair "$s" "$k"
  bad_ledger "$s:$k" || return 1
  WHAT="an uppercase id"
  bucket_reset "$dir/b"; k="${id^^}"; bucket_seed_pair "$s" "$k"
  bad_ledger "$s:$k" || return 1
  WHAT="an extra path segment"
  bucket_reset "$dir/b"; mkdir -p "$BUCKET/objects/fold/$s-$id/x"
  : >"$BUCKET/objects/fold/$s-$id/x/fold-$s.dump.age"; : >"$BUCKET/objects/fold/$s-$id/x/uploads-$s.tgz.age"
  bad_ledger "$s:$id" || return 1
  WHAT="flat keys"
  bucket_reset "$dir/b"; mkdir -p "$BUCKET/objects/fold"
  : >"$BUCKET/objects/fold/fold-$s.dump.age"; : >"$BUCKET/objects/fold/uploads-$s.tgz.age"
  bad_ledger "$s:$id" || return 1
  printf 'x\n' >"$dir/f"
  bucket_reset "$dir/b"
  # A key outside fold/, or with dot-dot, an empty segment or a trailing slash, is never PUT.
  for k in x/y fold/../y fold//y fold/y/ fold; do
    bucket_key_ok "$k" && { echo "bucket model: the key $k was accepted" >&2; return 1; }
  done
  bucket_put t "$dir/f" "fold/$s-$id/fold-$s.dump.age" || { echo "bucket model: a PUT failed" >&2; return 1; }
  bucket_put t "$dir/f" "fold/$s-$id/fold-$s.dump.age" || return 1
  bucket_each_key_once && { echo "bucket model: a repeated key was not noticed" >&2; return 1; }
  printf 'fold/x\n' >"$dir/guard"
  bucket_source_guard "$dir/guard" 'fold/${s}-${id}/' && { echo "bucket model: a producer without a run folder passed the guard" >&2; return 1; }
  printf 'fold/${s}-${id}/\nrclone lsf x\n' >"$dir/guard"
  bucket_source_guard "$dir/guard" 'fold/${s}-${id}/' && { echo "bucket model: a producer that lists passed the guard" >&2; return 1; }
  printf 'fold/${s}-${id}/\n' >"$dir/guard"
  bucket_source_guard "$dir/guard" 'fold/${s}-${id}/' || { echo "bucket model: a good producer failed the guard" >&2; return 1; }
  rm -rf -- "$dir"
  if [[ -n "$saved" ]]; then BUCKET="$saved"; export BUCKET; fi
}
