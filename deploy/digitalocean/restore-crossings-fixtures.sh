#!/usr/bin/env bash
# Daemon-free regressions for the R4-6c-V crossing helpers: revision proofs from
# git, table-set differences, migration ownership, refusal judging, evidence
# completeness and the restore drill's wiring. No Docker, network or SQL server.
# shellcheck disable=SC2016 # jq and child-shell programs keep their variables literal.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$ROOT/deploy/digitalocean/checks.sh"
# shellcheck source=deploy/digitalocean/restore-drill-crossings.sh
source "$ROOT/deploy/digitalocean/restore-drill-crossings.sh"
WORK="$(mktemp -d /tmp/fold-crossings-fixture.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "crossing fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR

refused() { # needle command...
  local needle="$1" rc=0; shift
  "$@" >"$WORK/out" 2>&1 || rc=$?
  [[ "$rc" != 0 ]]
  [[ -z "$needle" ]] || grep -qF -- "$needle" "$WORK/out"
}

# The library is a sourced interface, never a second runner.
rc=0; bash "$ROOT/deploy/digitalocean/restore-drill-crossings.sh" >"$WORK/direct.log" 2>&1 || rc=$?
[[ "$rc" == 1 ]]; grep -q 'source from restore-drill.sh only' "$WORK/direct.log"

# --- revisions, proved from commits -----------------------------------------
REPO="$WORK/repo"
git init -q "$REPO"
git -C "$REPO" config user.name fixture
git -C "$REPO" config user.email fixture@example.invalid
git -C "$REPO" config commit.gpgsign false
git -C "$REPO" config core.hooksPath /dev/null
mkdir -p "$REPO/migrations" "$REPO/deploy/digitalocean"
commit() { git -C "$REPO" add -A && git -C "$REPO" commit -qm "$1" && git -C "$REPO" rev-parse HEAD; }
printf 'CREATE TABLE public.users (id integer);\n' >"$REPO/migrations/00001_baseline.sql"
for n in 00002_a 00003_b 00004_c 00005_d; do printf -- '-- %s\n' "$n" >"$REPO/migrations/$n.sql"; done
ARCHIVE="$(commit archive-base)"
printf 'CREATE TABLE admin_operation_attempts (id integer);\n' >"$REPO/migrations/00006_admin_operation_attempts.sql"
LANE="$(commit lane-base)"
printf -- '-- 00007\n' >"$REPO/migrations/00007_x.sql"
printf '1\n' >"$REPO/deploy/recovery-policy"
LANDING="$(commit landing)"
printf '#!/bin/sh\n' >"$REPO/deploy/digitalocean/restore.sh"
HEAD_SHA="$(commit candidate)"
crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$ARCHIVE" "$HEAD_SHA"
# LANE_BASE and the archive base may be one commit (the default).
crossing_validate_revisions "$REPO" "$ARCHIVE" "$LANDING" "$ARCHIVE" "$HEAD_SHA"
[[ "$(crossing_migration_versions "$REPO" "$ARCHIVE")" == 1,2,3,4,5 ]]
[[ "$(crossing_migration_versions "$REPO" "$HEAD_SHA")" == 1,2,3,4,5,6,7 ]]
# An archive base that already has the table, a landing that has restore.sh, a
# lane base that is not level 0, a malformed or foreign SHA, each fails closed.
refused 'already has admin_operation_attempts' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$LANE" "$HEAD_SHA"
refused 'already has restore.sh' crossing_validate_revisions "$REPO" "$LANE" "$HEAD_SHA" "$ARCHIVE" "$HEAD_SHA"
refused 'not a level-0 release' crossing_validate_revisions "$REPO" "$LANDING" "$LANDING" "$ARCHIVE" "$HEAD_SHA"
refused 'not level 0' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$LANDING" "$HEAD_SHA"
refused 'full 40-hex SHA' crossing_validate_revisions "$REPO" abc "$LANDING" "$ARCHIVE" "$HEAD_SHA"
EMPTY_TREE="$(git -C "$REPO" hash-object -w -t tree /dev/null)"
FOREIGN="$(git -C "$REPO" commit-tree "$EMPTY_TREE" -m foreign)"
refused 'not an ancestor' crossing_validate_revisions "$REPO" "$FOREIGN" "$LANDING" "$ARCHIVE" "$HEAD_SHA"
refused 'is not in' crossing_validate_revisions "$REPO" 0123456789012345678901234567890123456789 "$LANDING" "$ARCHIVE" "$HEAD_SHA"
printf -- '-- 00008\n' >"$REPO/migrations/00008_y.sql"
NEWER="$(commit newer-schema)"
refused 'differ from the candidate' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$ARCHIVE" "$NEWER"
# A candidate that is not level 1 is not a crossing source.
printf '0\n' >"$REPO/deploy/recovery-policy"
LOWERED="$(commit lowered)"
refused 'is recovery level 0, not 1' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$ARCHIVE" "$LOWERED"
printf '1\n' >"$REPO/deploy/recovery-policy"
# The revisions named in the drill's defaults, when this checkout has them.
[[ "$CROSSING_LANE_DEFAULT" == 8c07c85265c0f43ab8350cd945efdc14363c21bd && "$CROSSING_ARCHIVE_DEFAULT" == "$CROSSING_LANE_DEFAULT" ]]
INTEGRATION_LANE=89899794046c4f2cd8a59f0fe6d0184a1d825afd
if git -C "$ROOT" cat-file -e "${CROSSING_LANE_DEFAULT}^{commit}" 2>/dev/null \
  && git -C "$ROOT" cat-file -e "${CROSSING_LANDING_DEFAULT}^{commit}" 2>/dev/null \
  && git -C "$ROOT" cat-file -e "${INTEGRATION_LANE}^{commit}" 2>/dev/null; then
  real_head="$(git -C "$ROOT" rev-parse HEAD)"
  crossing_validate_revisions "$ROOT" "$CROSSING_LANE_DEFAULT" "$CROSSING_LANDING_DEFAULT" "$CROSSING_ARCHIVE_DEFAULT" "$real_head"
  # D for the default archive base holds both tables added since 00004.
  [[ "$(crossing_migration_versions "$ROOT" "$CROSSING_ARCHIVE_DEFAULT")" == 1,2,3,4 ]]
  [[ "$(crossing_owner_migration "$ROOT" "$real_head" email_address_optouts)" == 5 ]]
  [[ "$(crossing_owner_migration "$ROOT" "$real_head" admin_operation_attempts)" == 6 ]]
  # The outcome lane's branch point already carries 00006: it cannot be the archive base.
  refused 'already has admin_operation_attempts' crossing_validate_revisions "$ROOT" "$CROSSING_LANE_DEFAULT" "$CROSSING_LANDING_DEFAULT" "$INTEGRATION_LANE" "$real_head"
else
  echo 'crossing fixtures: default revisions are not in this checkout; skipped the real-history proof'
fi

# --- migration ownership ------------------------------------------------------
[[ "$(crossing_owner_migration "$REPO" "$NEWER" admin_operation_attempts)" == 6 ]]
refused '' crossing_owner_migration "$REPO" "$NEWER" users          # baseline only
refused '' crossing_owner_migration "$REPO" "$NEWER" no_such_table
refused '' crossing_owner_migration "$REPO" "$NEWER" 'bad name'
printf 'CREATE TABLE admin_operation_attempts_extra (id integer);\n' >"$REPO/migrations/00009_prefix.sql"
PREFIX="$(commit prefix-table)"
[[ "$(crossing_owner_migration "$REPO" "$PREFIX" admin_operation_attempts)" == 6 ]]
[[ "$(crossing_owner_migration "$REPO" "$PREFIX" admin_operation_attempts_extra)" == 9 ]]
printf 'CREATE TABLE IF NOT EXISTS admin_operation_attempts (id integer);\n' >"$REPO/migrations/00010_dup.sql"
DUP="$(commit duplicate-table)"
refused '' crossing_owner_migration "$REPO" "$DUP" admin_operation_attempts

# --- the landing head must be a commit -----------------------------------------
crossing_require_clean_head "$REPO"
printf 'x\n' >"$REPO/untracked"
refused 'uncommitted or untracked' crossing_require_clean_head "$REPO"
rm "$REPO/untracked"
printf 'y\n' >>"$REPO/migrations/00001_baseline.sql"
refused 'uncommitted or untracked' crossing_require_clean_head "$REPO"
git -C "$REPO" checkout -q -- migrations/00001_baseline.sql
crossing_require_clean_head "$REPO"

# --- table sets and the difference D -------------------------------------------
cat >"$WORK/toc.txt" <<'TOC'
;
; Archive created at 2026-11-06 00:00:00 UTC
215; 1259 16400 TABLE public users church
3400; 0 16400 TABLE DATA public users church
216; 1259 16410 TABLE public goose_db_version church
217; 1259 16420 VIEW public some_view church
218; 1259 16430 SEQUENCE public users_id_seq church
219; 1259 16440 TABLE other secret church
220; 1259 16450 TABLE public email_address_optouts church
TOC
crossing_toc_tables <"$WORK/toc.txt" >"$WORK/archive.tables"
[[ "$(paste -sd, "$WORK/archive.tables")" == email_address_optouts,goose_db_version,users ]]
{ cat "$WORK/archive.tables"; echo admin_operation_attempts; } | LC_ALL=C sort -u >"$WORK/candidate.tables"
[[ "$(crossing_table_difference "$WORK/archive.tables" "$WORK/candidate.tables")" == admin_operation_attempts ]]
# Other members are recorded as they are; they are never assumed.
{ cat "$WORK/candidate.tables"; echo newer_table; } | LC_ALL=C sort -u >"$WORK/candidate2.tables"
[[ "$(crossing_table_difference "$WORK/archive.tables" "$WORK/candidate2.tables" | paste -sd,)" == admin_operation_attempts,newer_table ]]
refused 'lacks admin_operation_attempts' crossing_table_difference "$WORK/archive.tables" "$WORK/archive.tables"
refused 'lacks admin_operation_attempts' crossing_table_difference "$WORK/candidate.tables" "$WORK/archive.tables"
refused '' crossing_table_difference "$WORK/archive.tables" "$WORK/candidate.tables.missing"

# --- judging one refused invocation ---------------------------------------------
MSG='check: target recovery level 0 is below host level 1. The older release lacks the restore barrier (level 1). Restore data with the current release; use restore.sh --dump once installed.'
printf 'state\n' >"$WORK/before"; cp "$WORK/before" "$WORK/after"
trace() { printf '%s\n' "$@" >"$WORK/trace"; }
trace '+ check_target_level build 89899794' '++ require_recovery_level 0 1' "$MSG"
[[ -z "$(crossing_problems forward-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after")" ]]
trace '+ check_target_level image 127.0.0.1:5000/fold:x' '+ require_recovery_level 0 1' "$MSG"
[[ -z "$(crossing_problems rollback-image 1 "$WORK/trace" "$WORK/before" "$WORK/after")" ]]
trace '+ stage_tools' '+ require_recovery_level 0 1' "$MSG"
[[ -z "$(crossing_problems restore-image 1 "$WORK/trace" "$WORK/before" "$WORK/after")" ]]
[[ "$(crossing_problems forward-commit 0 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'exit code 0'* ]]
[[ "$(crossing_problems forward-commit 75 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'exit code 75'* ]]
trace '+ check_target_level build x' '+ require_recovery_level 0 1'
[[ "$(crossing_problems forward-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'restore barrier is missing'* ]]
trace '+ check_target_level build x' '+ require_recovery_level 0 1' 'check: target recovery level 0 is below host level 1. Something else.'
[[ "$(crossing_problems forward-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'restore barrier is missing'* ]]
printf 'changed\n' >"$WORK/after"
trace '+ check_target_level build x' '+ require_recovery_level 0 1' "$MSG"
[[ "$(crossing_problems forward-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'snapshot_state changed'* ]]
cp "$WORK/before" "$WORK/after"
trace '+ check_target_level build x' '+ require_recovery_level 0 1' "$MSG"
[[ "$(crossing_problems forward-image 1 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'check_target_level image'* ]]
[[ "$(crossing_problems restore-weird 1 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'unknown kind'* ]]
for step in run_backup write_release run_provision 'git -C /s checkout --detach abc' 'docker compose -p x stop app' 'exec /d/restore.sh --dump x'; do
  trace '+ check_target_level build x' '+ require_recovery_level 0 1' "$MSG" "++ $step"
  [[ "$(crossing_problems forward-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'reached backup, record'* ]]
done
for step in publish_tools close_site create_database collect pin swap recovery_start record store 'docker compose --project-directory /d stop caddy' 'docker compose --project-directory /d exec -T db pg_restore -U church -d fold_restore_x'; do
  trace '+ require_recovery_level 0 1' "$MSG" "++ $step"
  [[ "$(crossing_problems restore-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after")" == *'reached stop, collect'* ]]
done
# A checkout's provisioner is data: its path in a trace fails the case.
trace '+ check_target_level build x' '+ require_recovery_level 0 1' "$MSG" '+ /src/deploy/digitalocean/provision.sh --skip-harden'
[[ -z "$(crossing_problems forward-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after")" ]]
[[ "$(crossing_problems forward-commit 1 "$WORK/trace" "$WORK/before" "$WORK/after" /src/deploy/digitalocean/provision.sh)" == *'trace executed /src/deploy/digitalocean/provision.sh'* ]]
# A frozen-copy check may stage tools and read the archive; those are not changes.
trace '+ stage_tools' '+ docker compose --project-directory /d exec -T db pg_restore -l' '+ require_recovery_level 0 1' "$MSG"
[[ -z "$(crossing_problems restore-image 1 "$WORK/trace" "$WORK/before" "$WORK/after")" ]]

# --- which tool set a dump restore ran from ---------------------------------------
# Lines copied in order from a real upgrade.sh --rollback trace (nested xtrace
# markers kept). restore.sh --dump execs a staged copy, renames that directory
# to recovery-tools and keeps running; it never execs recovery-tools/restore.sh.
T=/drill/restore-run/site
S=/drill/restore-run/source
real_tools_trace() {
  printf '%s\n' \
    "+ exec $T/deploy-tools/upgrade.sh --deploy-dir $T --source-dir $S --rollback --source-dir $S" \
    "+ exec $T/deploy-tools/restore.sh --dump $T/dumps/fold-20261107-0528.dump --reopen-if-none --deploy-dir $T --source-dir $S --image 127.0.0.1:32769/fold:real-landing-d400b7755f45 --drill" \
    "+ install -m 644 $T/deploy-tools/restore.sh $T/.recovery-tools.o21EaG/restore.sh" \
    "+ exec $T/.recovery-tools.o21EaG/restore.sh --dump $T/dumps/fold-20261107-0528.dump --reopen-if-none --deploy-dir $T --source-dir $S" \
    "+ publish_tools" \
    "+ mv $T/recovery-tools $T/.old-recovery-tools.rdF5rE" \
    "+ mv $T/.recovery-tools.o21EaG $T/recovery-tools" \
    "+ pin" \
    "+ $T/recovery-tools/provision.sh --pin-only --recovery-nonce 5eecf9d3a2a8afcfb16a9c6008a20c69 --skip-harden --deploy-dir $T" \
    "+ finish_reopen" \
    "+ smoke" \
    "+ $T/recovery-tools/smoke.sh --deploy-dir $T --source-dir $S" \
    "+ exit 0"
}
real_tools_trace >"$WORK/tools.trace"
[[ -z "$(crossing_recovery_tools_problems "$WORK/tools.trace" "$T" "$S/deploy/digitalocean/")" ]]
# The retired assertion looked for an exec of recovery-tools/restore.sh. The real
# flow never contains one, which is why it failed a correct rollback.
if grep -Eq '^\++ exec .*/recovery-tools/restore\.sh ' "$WORK/tools.trace"; then exit 1; fi
tools_problem() { # message-needle, from stdin trace
  cat >"$WORK/tools.mutated"
  got="$(crossing_recovery_tools_problems "$WORK/tools.mutated" "$T" "$S/deploy/digitalocean/")"
  [[ "$got" == *"$1"* ]]
}
# Never staged: only the old-style exec of the published copy.
real_tools_trace | grep -v '\.recovery-tools\.o21EaG/restore\.sh --dump' | tools_problem "no staged recovery-tools copy"
# A different directory was published than the one that ran.
real_tools_trace | sed 's|mv .*/\.recovery-tools\.o21EaG |mv '"$T"'/.recovery-tools.zzzz |' | tools_problem "was not published"
# Published before it ran (order matters).
{ real_tools_trace | sed -n '7p'; real_tools_trace | sed -n '1,6p;8,$p'; } | tools_problem "was not published"
# Pin or smoke from the old tool set, or before the publication.
real_tools_trace | sed "s|^+ $T/recovery-tools/provision.sh|+ $T/deploy-tools/provision.sh|" | tools_problem "pin did not run from the published"
real_tools_trace | sed "s|^+ $T/recovery-tools/smoke.sh|+ $T/deploy-tools/smoke.sh|" | tools_problem "smoke did not run from the published"
{ real_tools_trace | sed -n '1,4p'; real_tools_trace | sed -n '9p'; real_tools_trace | sed -n '5,8p;10,$p'; } | tools_problem "pin did not run from the published"
# A deploy-tools command after the hand-off, or a checkout's tools at any point.
{ real_tools_trace; echo "+ $T/deploy-tools/restore.sh --status"; } | tools_problem "ran from $T/deploy-tools after the hand-off"
{ real_tools_trace; echo "++ exec $T/deploy-tools/smoke.sh --deploy-dir $T"; } | tools_problem "ran from $T/deploy-tools after the hand-off"
{ real_tools_trace; echo "+ $S/deploy/digitalocean/provision.sh --skip-harden"; } | tools_problem "ran from $S/deploy/digitalocean/"
# Lines that merely mention deploy-tools (tests, installs, assignments) are not commands run from it.
{ real_tools_trace; echo "+ [[ -f $T/deploy-tools/provision.sh ]]"; echo "+ install -m 644 $T/deploy-tools/smoke.sh $T/x"; echo "+ PROVISION=$T/deploy-tools/provision.sh"; } >"$WORK/tools.noise"
[[ -z "$(crossing_recovery_tools_problems "$WORK/tools.noise" "$T" "$S/deploy/digitalocean/")" ]]
# The case uses this function, not a loose grep.
grep -q 'crossing_recovery_tools_problems "\$trace" "\$DIR"' "$ROOT/deploy/digitalocean/restore-drill-crossings.sh"
if grep -Eq "grep -Eq '\^.*recovery-tools.*restore" "$ROOT/deploy/digitalocean/restore-drill-crossings.sh"; then exit 1; fi

# --- evidence completeness -------------------------------------------------------
gen_cases() {
  local acc i
  row() { # kind acceptance deployed site fixture n
    jq -nc --arg a "$2" --arg d "$3" --arg s "$4" --argjson f "$5" --argjson n "$6" \
      '{kind:"refusal", acceptance:$a, deployed:$d, site:$s, fixture:$f, id:($s + "-" + $a + "-" + ($n|tostring)), exitCode:1, pass:true}'
  }
  for acc in R-forward-commit R-forward-image R-restore-commit R-restore-image; do
    for i in 1 2; do row refusal "$acc" image main false "$i"; row refusal "$acc" build lineage-build false "$i"; done
  done
  for i in 1 2; do
    row refusal R-rollback-genuine image lineage-image false "$i"
    row refusal R-rollback-genuine build lineage-build false "$i"
    row refusal S-rollback-commit-record image main true "$i"
    row refusal S-rollback-image-record image main true "$i"
  done
  jq -nc '{kind:"positive", acceptance:"A-lineage-upgrade", mode:"image", site:"lineage-image", fixture:false, id:"li", pass:true}'
  jq -nc '{kind:"positive", acceptance:"A-lineage-upgrade", mode:"build", site:"lineage-build", fixture:false, id:"lb", pass:true}'
  jq -nc '{kind:"positive", acceptance:"A-same-level-rollback", fixture:false, id:"same-level-rollback", pass:true}'
  jq -nc '{kind:"positive", acceptance:"A-older-archive", fixture:false, id:"older-archive", pass:true}'
}
gen_images() {
  local role level id sha
  for role in candidate lane landing archive; do
    case "$role" in candidate|landing) level=1 ;; *) level=0 ;; esac
    id="sha256:$(printf '%s' "$role" | sha256sum | cut -d' ' -f1)"
    sha="$(printf '%s' "$role" | sha1sum | cut -d' ' -f1)"
    jq -nc --arg role "$role" --argjson level "$level" --arg id "$id" --arg sha "$sha" '{role:$role, level:$level, imageId:$id, sha:$sha}'
  done
}
gen_cases >"$WORK/cases.jsonl"; gen_images >"$WORK/images.jsonl"
crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.jsonl"
sed '1d' "$WORK/cases.jsonl" >"$WORK/cases.short"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.short" "$WORK/images.jsonl"
jq -c 'if .id=="main-R-restore-image-2" then .pass=false else . end' "$WORK/cases.jsonl" >"$WORK/cases.failed"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.failed" "$WORK/images.jsonl"
jq -c 'if .id=="lineage-image-R-rollback-genuine-1" then .exitCode=75 else . end' "$WORK/cases.jsonl" >"$WORK/cases.rc"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.rc" "$WORK/images.jsonl"
jq -c 'select(.deployed != "build")' "$WORK/cases.jsonl" >"$WORK/cases.image-only"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.image-only" "$WORK/images.jsonl"
grep -v '"A-older-archive"' "$WORK/cases.jsonl" >"$WORK/cases.no-archive"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.no-archive" "$WORK/images.jsonl"
# A fixture row that claims acceptance, an acceptance row that claims to be a fixture,
# an unlabelled extra acceptance and a genuine rollback row from the wrong site all fail.
jq -c 'if .acceptance=="S-rollback-image-record" and .id=="main-S-rollback-image-record-1" then .fixture=false else . end' "$WORK/cases.jsonl" >"$WORK/cases.unlabelled"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.unlabelled" "$WORK/images.jsonl"
jq -c 'if .id=="main-R-forward-image-1" then .fixture=true else . end' "$WORK/cases.jsonl" >"$WORK/cases.fixture-claim"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.fixture-claim" "$WORK/images.jsonl"
jq -c 'if .acceptance=="R-rollback-genuine" then .site="main" else . end' "$WORK/cases.jsonl" >"$WORK/cases.wrong-site"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.wrong-site" "$WORK/images.jsonl"
{ cat "$WORK/cases.jsonl"; jq -nc '{kind:"positive", acceptance:"A-unknown", fixture:false, id:"u", pass:true}'; } >"$WORK/cases.extra"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.extra" "$WORK/images.jsonl"
grep -v '"role":"archive"' "$WORK/images.jsonl" >"$WORK/images.short"
refused 'image evidence is incomplete' crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.short"
jq -c 'if .role=="landing" then .level=0 else . end' "$WORK/images.jsonl" >"$WORK/images.level"
refused 'image evidence is incomplete' crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.level"
: >"$WORK/empty.jsonl"
refused 'missing or empty' crossing_evidence_complete "$WORK/empty.jsonl" "$WORK/images.jsonl"

# --- the runner's wiring ---------------------------------------------------------
RUNNER="$ROOT/deploy/digitalocean/restore-drill.sh"
grep -q '^  real_binary_refusals real_level0_lineage real_same_level_rollback real_older_archive_restore final-verification$' "$RUNNER"
loop="$(sed -n '/^for drill_family in/,/; do$/p' "$RUNNER")"
for family in real_binary_refusals real_level0_lineage real_same_level_rollback real_older_archive_restore; do
  grep -q "$family" <<<"$loop"
  declare -F "$family" >/dev/null
done
sed -n '/^drill_case_begin final-verification/,/^drill_case_end/p' "$RUNNER" | grep -q '^assert_crossing_evidence$'
declare -F assert_crossing_evidence >/dev/null
validate_line="$(grep -n '^crossing_validate_revisions "\$ROOT"' "$RUNNER" | cut -d: -f1)"
bootstrap_line="$(grep -n '^drill_case_begin bootstrap$' "$RUNNER" | cut -d: -f1)"
[[ -n "$validate_line" && -n "$bootstrap_line" && "$validate_line" -lt "$bootstrap_line" ]]
clean_line="$(grep -n '^crossing_require_clean_head "\$ROOT"' "$RUNNER" | cut -d: -f1)"
[[ -n "$clean_line" && "$clean_line" -lt "$bootstrap_line" ]]
grep -q 'crossing_cleanup_all' "$RUNNER"
sed -n '/^reset_case() {/,/^}/p' "$RUNNER" >"$WORK/reset-case.txt"
grep -q 'image="${1:-$BASE_REGISTRY_IMAGE}"' "$WORK/reset-case.txt"
grep -q -- '--image "$image"' "$WORK/reset-case.txt"
help="$(bash "$RUNNER" --help)"
for flag in --lane-base --landing --archive-base; do grep -q -- "$flag" <<<"$help"; done
# The defaults are 40-hex.
for sha in "$CROSSING_LANE_DEFAULT" "$CROSSING_LANDING_DEFAULT" "$CROSSING_ARCHIVE_DEFAULT"; do [[ "$sha" =~ ^[0-9a-f]{40}$ ]]; done
echo 'restore crossing fixtures: ok'
