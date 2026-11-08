#!/usr/bin/env bash
# Daemon-free regression for the older-archive crossing case (crossings2: after a successful reopen the evidence record read
# older-archive-.json, because observe_services' `read -r id` overwrote crossing_older_archive_case's local `id`).
#
# It runs the REAL real_older_archive_restore -> crossing_older_archive_case for BOTH archive roles (lane-base and reason-review) in a
# separate bash, with the real kill_point, observe_services, restore wrapper, env_value, evidence jq and crossing_emit, against a
# stub world: docker, docker compose, psql and the HTTP API are functions that answer from state files, restore.sh is a stub that
# exits 137 on the injected fault. What is stubbed is listed in the harness. The real revisions' migrations (git) decide the
# versions, the table difference and the owner migrations.
# shellcheck disable=SC2016 # the harness is a quoted heredoc.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
WORK="$(mktemp -d /tmp/fold-archive-fixture.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "archive crossing fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
CROSS="$HERE/restore-drill-crossings.sh"
RUNNER="$HERE/restore-drill.sh"

# --- the cause, and the one-line fix, pinned ------------------------------------------------------------------------------------------------------------
obs="$(sed -n '/^observe_services() {$/,/^}$/p' "$RUNNER")"
grep -qE '^  local ids id$' <<<"$obs"       # the loop variable is local to the observer
grep -q 'while IFS= read -r id; do' <<<"$obs"
# the case keeps `id` as the role, and binds every file it writes and the evidence row to it
case_body="$(sed -n '/^crossing_older_archive_case() {$/,/^}$/p' "$CROSS")"
grep -qE '^  local id="\$1" rev="\$2" image="\$3"$' <<<"$case_body"
grep -q -- '--slurpfile record "$cdir/older-archive-$id.json"' <<<"$case_body"
grep -q 'id:("older-archive-" + $record\[0\].id)' <<<"$case_body"
grep -q '"$cdir/older-archive-$id-goose-status.json"' <<<"$case_body"
# the evidence check still requires exactly the two roles, from the records' own ids, and nothing in it was loosened
ev_body="$(sed -n '/^crossing_evidence_complete() {$/,/^}$/p' "$CROSS")"
grep -qF '"A-older-archive": 2' <<<"$ev_body"
grep -qF '([.[] | select(.acceptance == "A-older-archive") | .archive.id] | sort) == ["lane-base", "reason-review"]' <<<"$ev_body"
grep -qF 'and ([.[] | select(.acceptance == "A-older-archive")' <<<"$ev_body"
# no function the case calls is allowed to leave a read/for loop variable of the case undeclared (the high-signal part of the scope hazard):
# observe_services is the one that did, and the harness below proves the whole path with the real function.

# --- the stub world ----------------------------------------------------------------------------------------------------------------------------------------
LANE_DEFAULT="$(bash -c 'source "$1"; printf %s "$CROSSING_LANE_DEFAULT"' _ "$CROSS" 2>/dev/null || true)"
ARCH_DEFAULT="$(bash -c 'source "$1"; printf %s "$CROSSING_ARCHIVE_DEFAULT"' _ "$CROSS" 2>/dev/null || true)"
for r in "$LANE_DEFAULT" "$ARCH_DEFAULT"; do
  if [[ -z "$r" ]] || ! git -C "$ROOT" cat-file -e "$r^{commit}" 2>/dev/null; then
    echo "note: the archive revisions are not in this clone ($r); the behavioural part of the archive crossing fixture is skipped, the static part ran" >&2
    echo 'archive crossing fixtures: ok (static part only)'
    exit 0
  fi
done

cat >"$WORK/harness.sh" <<'HARNESS'
set -Eeuo pipefail
ROOT="$1"; W="$2"; HERE="$ROOT/deploy/digitalocean"; mkdir -p "$W"
# shellcheck source=/dev/null
source "$HERE/checks.sh"; source "$HERE/isolation.sh"; source "$HERE/restore-drill-cases.sh"; source "$HERE/restore-drill-crossings.sh"
# the REAL functions of the runner this path goes through, taken out of restore-drill.sh
for f in observe_services kill_point restore; do
  awk -v n="$f" '$0 ~ "^"n"\\(\\) \\{" {on=1} on{print} on && /^}$/ {exit}' "$HERE/restore-drill.sh" >"$W/fn-$f.sh"
  [[ -s "$W/fn-$f.sh" ]]
  source "$W/fn-$f.sh"
done
S="$W/state"; mkdir -p "$S" "$W/site" "$W/source" "$W/crossings/table-sets"
export S W
WORK="$W"; DIR="$W/site"; SOURCE="$W/source"; PROJECT="fold-deploy-drill-x"; REVISION="$(git -C "$ROOT" rev-parse HEAD)"
LANE_REVISION="$CROSSING_LANE_DEFAULT"; ARCHIVE_BASE_REVISION="$CROSSING_ARCHIVE_DEFAULT"
CROSSING_CAND_IMG="reg/fold:cand"; CROSSING_CAND_TAG="cand"; CROSSING_LANE_IMG="reg/fold:lane"; CROSSING_ARCHIVE_IMG="reg/fold:arch"
TOOLS_DIR="$W/tools"; mkdir -p "$TOOLS_DIR"; DB_DUMP="$W/base.dump"; URL="http://localhost:18080"; LOG="$W/calls.log"; : >"$LOG"
printf 'DB_PASSWORD=pw\n' >"$DIR/.env"
jq -n '{origOid:16384,cleanup:"pending-drop",phase:"swap-intent",nonce:"0123456789abcdef0123456789abcdef",level:2}' >"$DIR/recovery.lock"
cat >"$DIR/restore.sh" <<'RS'
#!/usr/bin/env bash
# the stub restore.sh: the injected fault kills it (137); --resume swaps in the candidate's functions; --status prints a phase
if [[ -n "${RESTORE_DRILL_FAULT:-}" ]]; then echo "restore.sh: fault $RESTORE_DRILL_FAULT"; exit 137; fi
case " $* " in
  *" --status "*) echo "phase=swap-intent cleanup=pending-drop" ;;
  *" --resume "*) echo candidate >"$S/fold_state"; echo recovering >"$S/phase" ;;
  *" --reopen "*) echo reopened >"$S/phase" ;;
esac
exit 0
RS
chmod +x "$DIR/restore.sh"
echo archive >"$S/fold_state"; echo 5 >"$S/fence"; echo 100 >"$S/uid"

# stubs (everything below is a stand-in for the daemon, the database or the server):
crossing_prepare() { :; }
reset_case() { echo "reset_case $*" >>"$LOG"; }
drill_step() { echo "step $1" >>"$LOG"; }
setup() { :; }
login() { :; }
wait_public() { :; }
public_url() { echo http://localhost:18080; }
assert_legacy_archive_schema() { echo "legacy-schema $(cat "$S/cur_rev")" >>"$LOG"; }
assert_private() { [[ "$(cat "$S/phase")" == recovering ]]; }
recovery_ids() { echo rec456; }
new_user() { local n; n=$(( $(cat "$S/uid") + 1 )); echo "$n" >"$S/uid"; echo "$n"; }
imgid() { printf 'sha256:%064d' "$(printf '%s' "$1" | cksum | cut -d' ' -f1)"; }
cur_level() { commit_recovery_level "$ROOT" "$(cat "$S/cur_rev")"; }
cur_versions() { crossing_migration_versions "$ROOT" "$(cat "$S/cur_rev")"; }
api() { # method path [data]
  case "$1 $2" in
    "POST /groups") echo '{"group":{"id":5}}' ;;
    "POST /admin/users/"*"/erasure-reasons/page")
      jq -nc --arg a "$(printf 'a%.0s' {1..64})" --arg b "$(printf 'b%.0s' {1..64})" '{reviewRequired:true,next:null,items:[{requestId:"11",reviewHash:$a},{requestId:"12",reviewHash:$b}]}' ;;
    "DELETE /admin/users/"*) echo $(( $(cat "$S/fence") + 1 )) >"$S/fence"; echo '{"erasure":{"reasonsKept":1,"reasonsRemoved":1}}' ;;
    *) echo "unexpected api $1 $2" >&2; return 1 ;;
  esac
}
pg() { # database -c sql ...
  local db="$1"; shift; local sql="$*"
  case "$sql" in
    *"CREATE DATABASE"*|*"DROP DATABASE"*|*"INSERT INTO contact_requests"*) ;;
    *"SELECT oid FROM pg_database"*) ;;                                  # the original database is gone: kill_point takes its else branch
    *"datname,pid,application_name"*) echo "postgres|1|x|idle" ;;
    *"string_agg(version_id"*) if [[ "$db" == fold && "$(cat "$S/fold_state")" == candidate ]]; then crossing_migration_versions "$ROOT" "$REVISION"; else cur_versions; fi ;;
    *"to_regclass('public.admin_operation_attempts') IS NULL"*) if [[ "$(cat "$S/fold_state")" == candidate && "$db" == fold ]]; then echo f; else echo t; fi ;;
    *"to_regclass('public."*) echo t ;;
    *"pg_get_functiondef"*)
      case "$db" in
        drill_archive_fixture) echo "functions of the archive $(cat "$S/cur_rev")" ;;
        drill_candidate_schema) echo "functions of the candidate" ;;
        fold) if [[ "$(cat "$S/fold_state")" == candidate ]]; then echo "functions of the candidate"; else echo "functions of the archive $(cat "$S/cur_rev")"; fi ;;
      esac ;;
    *"pg_tables"*) { archive_tables_for "$(cat "$S/cur_rev")"; printf '%s\n' admin_operation_attempts email_address_optouts; } | LC_ALL=C sort -u ;;
    *"count(*) - count(DISTINCT version_id)"*) echo 0 ;;
    *"FROM goose_db_version WHERE version_id="*) echo 1 ;;
    *"FROM pg_class c JOIN pg_namespace"*) echo 1 ;;
    *"restore_fence"*) cat "$S/fence" ;;
    *"deleted_at IS NOT NULL FROM users"*) echo t ;;
    *"SELECT reason FROM contact_requests WHERE id=11"*) echo "" ;;
    *"SELECT reason <> '' FROM contact_requests WHERE id=12"*) echo t ;;
    *"FROM audit_logs"*) echo 2 ;;
    *) echo "unexpected sql on $db: $sql" >&2; return 1 ;;
  esac
}
dc() {
  local all="$*"
  case "$all" in
    *"up -d --no-deps archive"*)
      local yml img
      yml="$(sed -n 's/^.* -f \([^ ]*\) --profile.*$/\1/p' <<<" $all")"
      img="$(sed -n 's/^    image: //p' "$yml")"
      case "$img" in "$CROSSING_LANE_IMG") echo "$LANE_REVISION" >"$S/cur_rev" ;; "$CROSSING_ARCHIVE_IMG") echo "$ARCHIVE_BASE_REVISION" >"$S/cur_rev" ;; *) echo "unexpected archive image $img" >&2; return 1 ;; esac
      echo "$img" >"$S/cur_img"; echo archive >"$S/fold_state" ;;
    *"ps -q archive"*) echo arch123 ;;
    *"port archive 8080"*) echo 127.0.0.1:41000 ;;
    *"ps -q app"*) echo app789 ;;
    *"exec -T db pg_dump"*) printf 'dump of %s\n' "$(cat "$S/cur_rev")" ;;
    *"exec -T db pg_restore -l"*)
      { echo '; Archive created'; for t in users groups contact_requests goose_db_version; do echo "1; 1259 1 TABLE public $t church"; done
        if [[ "$(cat "$S/cur_rev")" == "$ARCHIVE_BASE_REVISION" ]]; then echo "1; 1259 1 TABLE public admin_operation_attempts church"; echo "1; 1259 1 TABLE public email_address_optouts church"; fi; } ;;
    *"exec -T app /fold version --json"*) jq -nc --arg v "$CROSSING_CAND_TAG" --argjson l "$CROSSING_CANDIDATE_LEVEL" '{version:$v,recoveryPolicy:$l}' ;;
    *) echo "unexpected dc $all" >&2; return 1 ;;
  esac
}
docker() {
  case "$*" in
    "ps -aq --filter label=com.docker.compose.project="*) printf 'c1\nc2\n' ;;
    "inspect --format {{.Image}} arch123") imgid "$(cat "$S/cur_img")" ;;
    "inspect --format {{.Image}} rec456"|"inspect --format {{.Image}} app789") imgid "$CROSSING_CAND_IMG" ;;
    "image inspect --format {{.Id}} "*) imgid "${@: -1}" ;;
    "exec arch123 /fold healthcheck") ;;
    "exec arch123 /fold version --json") jq -nc --argjson l "$(cur_level)" '{recoveryPolicy:$l}' ;;
    "exec rec456 /fold version --json") jq -nc --arg v "$CROSSING_CAND_TAG" --argjson l "$CROSSING_CANDIDATE_LEVEL" '{version:$v,recoveryPolicy:$l}' ;;
    "exec rec456 /fold migrate status --json") crossing_migration_versions "$ROOT" "$REVISION" | tr ',' '\n' | jq -Rsc 'split("\n") | map(select(length>0) | {version:tonumber, state:"applied"})' ;;
    "inspect --format "*) echo "x|app|running|true" ;;
    "rm -f arch123"|"run --rm --network "*) ;;
    *) echo "unexpected docker $*" >&2; return 1 ;;
  esac
}
# the archive's tables per role: what the stub TOC lists, and the base of the candidate's list
archive_tables_for() { { echo users; echo groups; echo contact_requests; echo goose_db_version; if [[ "$1" == "$ARCHIVE_BASE_REVISION" ]]; then echo admin_operation_attempts; echo email_address_optouts; fi; } | LC_ALL=C sort -u; }
real_older_archive_restore
echo done >>"$LOG"
HARNESS

rc=0; bash "$WORK/harness.sh" "$ROOT" "$WORK/h" >"$WORK/harness.out" 2>"$WORK/harness.err" || rc=$?
if [[ "$rc" != 0 ]]; then echo "the older-archive case did not complete (exit $rc)" >&2; tail -n 25 "$WORK/harness.err" >&2; tail -n 5 "$WORK/harness.out" >&2; exit 1; fi
H="$WORK/h"; C="$H/crossings"
[[ "$(tail -n 1 "$H/calls.log")" == "done" ]]

# --- the evidence: one set of files per role, bound to that role, nothing under an empty id -------------------------------------------------------------------------
for id in lane-base reason-review; do
  for f in "older-archive-$id.json" "older-archive-$id.dump" "older-archive-$id-goose-status.json" "older-archive-$id-delete.json" "table-sets/$id.archive-functions.sql" "table-sets/$id.candidate-functions.sql" "table-sets/$id.D.tables"; do
    [[ -e "$C/$f" ]] || { echo "missing $f" >&2; ls "$C" >&2; exit 1; }
  done
  grep -qx "step archive-$id/reopen-review-and-delete" "$H/calls.log"
  grep -qx "step archive-$id/restore-with-candidate-image" "$H/calls.log"
done
if find "$C" -name '*archive--*' -o -name '*archive-.*' | grep .; then echo 'a file was written under an empty archive id' >&2; exit 1; fi
if grep -E 'step archive-(/|$)' "$H/calls.log"; then echo 'a step was named with an empty archive id' >&2; exit 1; fi
lane_rev="$(bash -c 'source "$1"; printf %s "$CROSSING_LANE_DEFAULT"' _ "$CROSS")"; arch_rev="$(bash -c 'source "$1"; printf %s "$CROSSING_ARCHIVE_DEFAULT"' _ "$CROSS")"
[[ "$(jq -s 'length' "$C/cases.jsonl")" == 2 ]]
[[ "$(jq -s -r 'map(select(.acceptance == "A-older-archive") | .archive.id) | sort | join(",")' "$C/cases.jsonl")" == lane-base,reason-review ]]
for id in lane-base reason-review; do
  want_rev="$lane_rev"; [[ "$id" == reason-review ]] && want_rev="$arch_rev"
  jq -s -e --arg id "$id" --arg rev "$want_rev" --arg sha "$(sha256sum <"$C/older-archive-$id.dump" | cut -d' ' -f1)" \
    '[.[] | select(.id == ("older-archive-" + $id))] | length == 1 and (.[0] | .pass == true and .fixture == false and .archive.id == $id and .archive.archiveRevision == $rev and .archive.dumpSha256 == $sha
       and .reasonsBlankedOnDeletedParty == true and .reviewRoute == "served" and .fenceAfter > .fenceBefore)' "$C/cases.jsonl" >/dev/null \
    || { echo "the evidence row of $id is not bound to its own archive" >&2; jq -c . "$C/cases.jsonl" >&2; exit 1; }
  jq -e --arg id "$id" '.id == $id' "$C/older-archive-$id.json" >/dev/null
  jq -e '.erasure.reasonsRemoved == 1 and .erasure.reasonsKept == 1' "$C/older-archive-$id-delete.json" >/dev/null
  jq -e 'type == "array" and all(.[]; .state == "applied")' "$C/older-archive-$id-goose-status.json" >/dev/null
done
# the two roles are different archives: different images, dumps and migration sets
[[ "$(jq -s -r 'map(.archive.dumpSha256) | unique | length' "$C/cases.jsonl")" == 2 ]]
[[ "$(jq -s -r 'map(.archive.archiveRevision) | unique | length' "$C/cases.jsonl")" == 2 ]]
jq -s -e '(map(select(.archive.id == "lane-base") | .archive.D) | .[0] | index("admin_operation_attempts")) != null' "$C/cases.jsonl" >/dev/null
# the evidence check accepts exactly this pair and refuses a pair whose second record is bound to the wrong role or missing
jq -s -e '([.[] | select(.acceptance == "A-older-archive") | .archive.id] | sort) == ["lane-base", "reason-review"]' "$C/cases.jsonl" >/dev/null
if jq -s -e '([.[] | select(.acceptance == "A-older-archive") | .archive.id] | sort) == ["lane-base", "reason-review"]' <(jq -c 'if .archive.id == "reason-review" then .archive.id = "" else . end' "$C/cases.jsonl") >/dev/null; then
  echo 'the evidence selector accepts an empty role' >&2; exit 1
fi
if jq -s -e '([.[] | select(.acceptance == "A-older-archive") | .archive.id] | sort) == ["lane-base", "reason-review"]' <(head -n 1 "$C/cases.jsonl") >/dev/null; then echo 'the evidence selector accepts one role' >&2; exit 1; fi
echo 'archive crossing fixtures: ok'
