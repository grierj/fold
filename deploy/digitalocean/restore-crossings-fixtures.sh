#!/usr/bin/env bash
# Daemon-free regressions for the R4-6c-V / T07B-2-V crossing helpers: revision
# proofs from git, migration differences, table-set differences, migration
# ownership, refusal judging for both level pairs, evidence completeness and
# the restore drill's wiring. No Docker, network or SQL server.
# shellcheck disable=SC2016,SC2031 # jq and child-shell programs keep their variables literal.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
# shellcheck source=deploy/digitalocean/checks.sh
source "$ROOT/deploy/digitalocean/checks.sh"
# shellcheck source=deploy/digitalocean/isolation.sh
source "$ROOT/deploy/digitalocean/isolation.sh"
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
# A synthetic history shaped like the real one: LANE (level 0, 00001-00005, no
# receipt table), ARCH (level 0, adds 00006, the archive base: no reason-review
# file), LANDING (level 1, adds 00007_reason_review_erasure_scrub, no restore.sh),
# MID (level 1 with restore.sh), PRIOR (level 2), HEAD (level 2, the candidate).
REPO="$WORK/repo"
git init -q "$REPO"
git -C "$REPO" config user.name fixture
git -C "$REPO" config user.email fixture@example.invalid
git -C "$REPO" config commit.gpgsign false
git -C "$REPO" config core.hooksPath /dev/null
mkdir -p "$REPO/migrations" "$REPO/deploy/digitalocean"
commit() { git -C "$REPO" add -A && git -C "$REPO" commit -qm "$1" && git -C "$REPO" rev-parse HEAD; }
# variant SHA: detach at SHA, so a mutated commit never changes the history above it.
variant() { git -C "$REPO" checkout -q --detach "$1"; }
printf 'CREATE TABLE public.users (id integer);\n' >"$REPO/migrations/00001_baseline.sql"
for n in 00002_a 00003_b 00004_c 00005_d; do printf -- '-- %s\n' "$n" >"$REPO/migrations/$n.sql"; done
LANE="$(commit lane-base)"
printf 'CREATE TABLE admin_operation_attempts (id integer);\n' >"$REPO/migrations/00006_admin_operation_attempts.sql"
ARCH="$(commit archive-base)"
printf -- '-- 00007\n' >"$REPO/migrations/00007_reason_review_erasure_scrub.sql"
printf '1\n' >"$REPO/deploy/recovery-policy"
LANDING="$(commit landing)"
printf '#!/bin/sh\n' >"$REPO/deploy/digitalocean/restore.sh"
MID="$(commit restore-tooling)"
printf '2\n' >"$REPO/deploy/recovery-policy"
PRIOR="$(commit prior)"
printf 'x\n' >"$REPO/ui"
HEAD_SHA="$(commit candidate)"
crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$ARCH" "$HEAD_SHA"
# The archive base may be any older commit without the reason-review file; the
# lane commit itself qualifies (it then misses 6 and 7).
crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$LANE" "$HEAD_SHA"
[[ "$(crossing_migration_versions "$REPO" "$LANE")" == 1,2,3,4,5 ]]
[[ "$(crossing_migration_versions "$REPO" "$HEAD_SHA")" == 1,2,3,4,5,6,7 ]]
# crossing_migration_difference: what the newer revision adds, and nothing when
# the older one is not wholly inside the newer.
[[ "$(crossing_migration_difference "$REPO" "$ARCH" "$HEAD_SHA")" == 7 ]]
[[ "$(crossing_migration_difference "$REPO" "$LANE" "$HEAD_SHA")" == 6,7 ]]
[[ -z "$(crossing_migration_difference "$REPO" "$HEAD_SHA" "$HEAD_SHA")" ]]
refused 'has migrations' crossing_migration_difference "$REPO" "$HEAD_SHA" "$ARCH"
# crossing_reason_migration_version: exactly one NNNNN_reason_review_erasure_scrub.sql.
[[ "$(crossing_reason_migration_version "$REPO" "$HEAD_SHA")" == 7 ]]
[[ "$(crossing_reason_migration_version "$REPO" "$LANDING")" == 7 ]]
refused '' crossing_reason_migration_version "$REPO" "$ARCH"
# Each property the acceptance relies on, broken once, fails closed and says why.
refused 'already has admin_operation_attempts' crossing_validate_revisions "$REPO" "$ARCH" "$LANDING" "$PRIOR" "$ARCH" "$HEAD_SHA"
refused 'already has restore.sh' crossing_validate_revisions "$REPO" "$LANE" "$MID" "$PRIOR" "$ARCH" "$HEAD_SHA"
refused 'not a level-0 release' crossing_validate_revisions "$REPO" "$LANDING" "$LANDING" "$PRIOR" "$ARCH" "$HEAD_SHA"
refused 'landing '"$PRIOR"' is recovery level 2, not level 1' crossing_validate_revisions "$REPO" "$LANE" "$PRIOR" "$PRIOR" "$ARCH" "$HEAD_SHA"
refused 'is recovery level 1, not level 2' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$LANDING" "$ARCH" "$HEAD_SHA"
refused 'is the candidate itself' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$HEAD_SHA" "$ARCH" "$HEAD_SHA"
refused 'not level 0 or 1' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$PRIOR" "$HEAD_SHA"
refused 'already has the reason_review_erasure_scrub migration' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$LANDING" "$HEAD_SHA"
refused 'already has the reason_review_erasure_scrub migration' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$MID" "$HEAD_SHA"
refused 'full 40-hex SHA' crossing_validate_revisions "$REPO" abc "$LANDING" "$PRIOR" "$ARCH" "$HEAD_SHA"
refused 'full 40-hex SHA' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" abc "$ARCH" "$HEAD_SHA"
EMPTY_TREE="$(git -C "$REPO" hash-object -w -t tree /dev/null)"
FOREIGN="$(git -C "$REPO" commit-tree "$EMPTY_TREE" -m foreign)"
refused 'not an ancestor' crossing_validate_revisions "$REPO" "$FOREIGN" "$LANDING" "$PRIOR" "$ARCH" "$HEAD_SHA"
refused 'not an ancestor' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$FOREIGN" "$ARCH" "$HEAD_SHA"
refused 'is not in' crossing_validate_revisions "$REPO" 0123456789012345678901234567890123456789 "$LANDING" "$PRIOR" "$ARCH" "$HEAD_SHA"
# A newer schema than the rollback target's: a "rollback" would cross schemas.
variant "$HEAD_SHA"
printf -- '-- 00008\n' >"$REPO/migrations/00008_y.sql"
NEWER="$(commit newer-schema)"
refused 'differ from the candidate' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$ARCH" "$NEWER"
# A candidate that is not level 2 is not a crossing source.
variant "$HEAD_SHA"
printf '1\n' >"$REPO/deploy/recovery-policy"
LOWERED="$(commit lowered)"
refused 'is recovery level 1, not 2' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$ARCH" "$LOWERED"
# A candidate without, or with two, reason-review migrations has no one migration to apply.
variant "$HEAD_SHA"
git -C "$REPO" rm -q migrations/00007_reason_review_erasure_scrub.sql
NOREASON="$(commit no-reason-migration)"
refused 'has no single migrations/NNNNN_reason_review_erasure_scrub.sql' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$PRIOR" "$ARCH" "$NOREASON"
variant "$HEAD_SHA"
printf -- '-- 00008 again\n' >"$REPO/migrations/00008_reason_review_erasure_scrub.sql"
TWOREASON="$(commit two-reason-migrations)"
refused 'has no single migrations/NNNNN_reason_review_erasure_scrub.sql' crossing_validate_revisions "$REPO" "$LANE" "$LANDING" "$TWOREASON" "$ARCH" "$TWOREASON"
variant "$HEAD_SHA"
# The revisions named in the drill's defaults, when this checkout has them.
[[ "$CROSSING_LANE_DEFAULT" == 8c07c85265c0f43ab8350cd945efdc14363c21bd ]]
[[ "$CROSSING_LANDING_DEFAULT" == d400b7755f4564c60524167105f31e33bed9b51b ]]
[[ "$CROSSING_PRIOR_DEFAULT" == 6927e896065d172fb1214181cd80fbd554a86eeb ]]
[[ "$CROSSING_ARCHIVE_DEFAULT" == 89899794046c4f2cd8a59f0fe6d0184a1d825afd ]]
SCHEMA_LANDING_MERGE=a1198e6eca9270a6646d4a825650be3f52022e57
if git -C "$ROOT" cat-file -e "${CROSSING_LANE_DEFAULT}^{commit}" 2>/dev/null \
  && git -C "$ROOT" cat-file -e "${CROSSING_LANDING_DEFAULT}^{commit}" 2>/dev/null \
  && git -C "$ROOT" cat-file -e "${CROSSING_PRIOR_DEFAULT}^{commit}" 2>/dev/null \
  && git -C "$ROOT" cat-file -e "${CROSSING_ARCHIVE_DEFAULT}^{commit}" 2>/dev/null \
  && git -C "$ROOT" cat-file -e "${SCHEMA_LANDING_MERGE}^{commit}" 2>/dev/null; then
  real_head="$(git -C "$ROOT" rev-parse HEAD)"
  crossing_validate_revisions "$ROOT" "$CROSSING_LANE_DEFAULT" "$CROSSING_LANDING_DEFAULT" "$CROSSING_PRIOR_DEFAULT" "$CROSSING_ARCHIVE_DEFAULT" "$real_head"
  # ARCH_SHA is the first parent of T07B-2a-V's landing merge.
  [[ "$(git -C "$ROOT" rev-parse "${SCHEMA_LANDING_MERGE}^1")" == "$CROSSING_ARCHIVE_DEFAULT" ]]
  [[ "$(crossing_migration_versions "$ROOT" "$CROSSING_LANE_DEFAULT")" == 1,2,3,4 ]]
  [[ "$(crossing_migration_versions "$ROOT" "$CROSSING_ARCHIVE_DEFAULT")" == 1,2,3,4,5,6 ]]
  # The candidate adds exactly the reason-review migration to the archive base.
  [[ "$(crossing_reason_migration_version "$ROOT" "$real_head")" == 7 ]]
  [[ "$(crossing_migration_difference "$ROOT" "$CROSSING_ARCHIVE_DEFAULT" "$real_head")" == 7 ]]
  [[ "$(crossing_migration_difference "$ROOT" "$CROSSING_LANE_DEFAULT" "$real_head")" == 5,6,7 ]]
  [[ "$(crossing_owner_migration "$ROOT" "$real_head" email_address_optouts)" == 5 ]]
  [[ "$(crossing_owner_migration "$ROOT" "$real_head" admin_operation_attempts)" == 6 ]]
  [[ "$(commit_recovery_level "$ROOT" "$CROSSING_ARCHIVE_DEFAULT")" == 0 ]]
  # The schema landing already carries 00007: it cannot be the archive base.
  refused 'already has the reason_review_erasure_scrub migration' crossing_validate_revisions "$ROOT" \
    "$CROSSING_LANE_DEFAULT" "$CROSSING_LANDING_DEFAULT" "$CROSSING_PRIOR_DEFAULT" "$SCHEMA_LANDING_MERGE" "$real_head"
  # The outcome lane's branch point carries 00006: it cannot be the table-set lane.
  refused 'already has admin_operation_attempts' crossing_validate_revisions "$ROOT" \
    "$CROSSING_ARCHIVE_DEFAULT" "$CROSSING_LANDING_DEFAULT" "$CROSSING_PRIOR_DEFAULT" "$CROSSING_ARCHIVE_DEFAULT" "$real_head"
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
[[ "$(crossing_table_difference "$WORK/archive.tables" "$WORK/candidate.tables" admin_operation_attempts)" == admin_operation_attempts ]]
[[ "$(crossing_table_difference "$WORK/archive.tables" "$WORK/candidate.tables")" == admin_operation_attempts ]]
# Other members are recorded as they are; they are never assumed.
{ cat "$WORK/candidate.tables"; echo newer_table; } | LC_ALL=C sort -u >"$WORK/candidate2.tables"
[[ "$(crossing_table_difference "$WORK/archive.tables" "$WORK/candidate2.tables" | paste -sd,)" == admin_operation_attempts,newer_table ]]
# The reason-review archive differs only in trigger bodies: D is empty and that is allowed.
[[ -z "$(crossing_table_difference "$WORK/archive.tables" "$WORK/archive.tables")" ]]
refused 'lacks admin_operation_attempts' crossing_table_difference "$WORK/archive.tables" "$WORK/archive.tables" admin_operation_attempts
refused 'lacks admin_operation_attempts' crossing_table_difference "$WORK/candidate.tables" "$WORK/archive.tables" admin_operation_attempts
refused '' crossing_table_difference "$WORK/archive.tables" "$WORK/candidate.tables.missing"

# --- what an older release lacks, per level pair ---------------------------------
[[ "$(crossing_missing_text 0 2)" == 'the restore barrier (level 1) and reason review (level 2)' ]]
[[ "$(crossing_missing_text 1 2)" == 'reason review (level 2)' ]]
[[ "$(crossing_missing_text 0 1)" == 'the restore barrier (level 1)' ]]
[[ -z "$(crossing_missing_text 2 2)" ]]
# The regex is the first `check:` line require_recovery_level prints (checks.sh).
for pair in '0 2' '1 2' '0 1'; do
  read -r tg fl <<<"$pair"
  line="$(require_recovery_level "$tg" "$fl" 2>&1 || true)"
  grep -Eq "$(crossing_refusal_re "$tg" "$fl")" <<<"$line" || { echo "refusal regex does not match checks.sh for $pair: $line" >&2; exit 1; }
done
# ...and it distinguishes the pairs: a level-0 message is not a level-1 refusal.
line0="$(require_recovery_level 0 2 2>&1 || true)"; line1="$(require_recovery_level 1 2 2>&1 || true)"
if grep -Eq "$(crossing_refusal_re 1 2)" <<<"$line0"; then exit 1; fi
if grep -Eq "$(crossing_refusal_re 0 2)" <<<"$line1"; then exit 1; fi
# The wording names reason review for both targets of the level-2 candidate.
grep -q 'reason review (level 2)' <<<"$line0"; grep -q 'reason review (level 2)' <<<"$line1"

# --- judging one refused invocation ---------------------------------------------
MSG0='check: target recovery level 0 is below host level 2. The older release lacks the restore barrier (level 1) and reason review (level 2). Restore data with the current release; use restore.sh --dump once installed.'
MSG1='check: target recovery level 1 is below host level 2. The older release lacks reason review (level 2). Restore data with the current release; use restore.sh --dump once installed.'
printf 'state\n' >"$WORK/before"; cp "$WORK/before" "$WORK/after"
trace() { printf '%s\n' "$@" >"$WORK/trace"; }
problems() { crossing_problems "$1" "$2" 2 "$3" "$WORK/trace" "$WORK/before" "$WORK/after" "${@:4}"; }
trace '+ check_target_level build 89899794' '++ require_recovery_level 0 2' "$MSG0"
[[ -z "$(problems forward-commit 0 1)" ]]
trace '+ check_target_level image 127.0.0.1:5000/fold:x' '+ require_recovery_level 1 2' "$MSG1"
[[ -z "$(problems rollback-image 1 1)" ]]
trace '+ stage_tools' '+ require_recovery_level 0 2' "$MSG0"
[[ -z "$(problems restore-image 0 1)" ]]
trace '+ stage_tools' '+ require_recovery_level 1 2' "$MSG1"
[[ -z "$(problems restore-commit 1 1)" ]]
# Each pair's trace fails when judged as the other pair.
trace '+ check_target_level build x' '+ require_recovery_level 0 2' "$MSG0"
[[ "$(problems forward-commit 1 1)" == *'reason review (level 2) is missing'* ]]
[[ "$(problems forward-commit 1 1)" == *'did not come from require_recovery_level 1 2'* ]]
trace '+ check_target_level build x' '+ require_recovery_level 1 2' "$MSG1"
[[ "$(problems forward-commit 0 1)" == *'the restore barrier (level 1) and reason review (level 2) is missing'* ]]
# The old level-1 wording (host floor 1) no longer satisfies a level-2 host.
trace '+ check_target_level build x' '+ require_recovery_level 0 2' 'check: target recovery level 0 is below host level 1. The older release lacks the restore barrier (level 1).'
[[ "$(problems forward-commit 0 1)" == *'is missing'* ]]
trace '+ check_target_level build x' '+ require_recovery_level 0 2' "$MSG0"
[[ "$(problems forward-commit 0 0)" == *'exit code 0'* ]]
[[ "$(problems forward-commit 0 75)" == *'exit code 75'* ]]
printf 'changed\n' >"$WORK/after"
[[ "$(problems forward-commit 0 1)" == *'snapshot_state changed'* ]]
cp "$WORK/before" "$WORK/after"
[[ "$(problems forward-image 0 1)" == *'check_target_level image'* ]]
[[ "$(problems restore-weird 0 1)" == *'unknown kind'* ]]
for step in run_backup write_release run_provision 'git -C /s checkout --detach abc' 'docker compose -p x stop app' 'exec /d/restore.sh --dump x'; do
  trace '+ check_target_level build x' '+ require_recovery_level 0 2' "$MSG0" "++ $step"
  [[ "$(problems forward-commit 0 1)" == *'reached backup, record'* ]]
done
for step in publish_tools close_site create_database collect pin swap recovery_start record store 'docker compose --project-directory /d stop caddy' 'docker compose --project-directory /d exec -T db pg_restore -U church -d fold_restore_x'; do
  trace '+ require_recovery_level 1 2' "$MSG1" "++ $step"
  [[ "$(problems restore-commit 1 1)" == *'reached stop, collect'* ]]
done
# A checkout's provisioner is data: its path in a trace fails the case.
trace '+ check_target_level build x' '+ require_recovery_level 1 2' "$MSG1" '+ /src/deploy/digitalocean/provision.sh --skip-harden'
[[ -z "$(problems forward-commit 1 1)" ]]
[[ "$(problems forward-commit 1 1 /src/deploy/digitalocean/provision.sh)" == *'trace executed /src/deploy/digitalocean/provision.sh'* ]]
# A frozen-copy check may stage tools and read the archive; those are not changes.
trace '+ stage_tools' '+ docker compose --project-directory /d exec -T db pg_restore -l' '+ require_recovery_level 0 2' "$MSG0"
[[ -z "$(problems restore-image 0 1)" ]]

# --- the explicit fixture row ---------------------------------------------------
sql="$(crossing_reason_row_sql 7 9 3 "it's a reason")"
grep -q "VALUES(7,9,'group',3,'','it''s a reason',true,'denied',9," <<<"$sql"
grep -q "'group_add')$" <<<"$sql"
if grep -q 'reason,' <<<"${sql#*VALUES}"; then exit 1; fi

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
  local acc t site i
  row() { # acceptance deployed site target fixture n
    jq -nc --arg a "$1" --arg d "$2" --arg s "$3" --arg t "$4" --argjson f "$5" --argjson n "$6" \
      '{kind:"refusal", acceptance:$a, deployed:$d, site:$s, target:$t, fixture:$f, id:($s + "-" + $a + "-" + $t + "-" + ($n|tostring)), exitCode:1, pass:true}'
  }
  # The four matrices run on the main site (image) and on both build-mode lineage sites.
  for acc in R-forward-commit R-forward-image R-restore-commit R-restore-image; do
    for t in level0 level1; do
      for i in 1 2; do
        row "$acc" image main "$t" false "$i"
        row "$acc" build lineage-l0-build "$t" false "$i"
        row "$acc" build lineage-l1-build "$t" false "$i"
      done
    done
  done
  for t in level0 level1; do
    for i in 1 2; do
      row R-rollback-genuine image "lineage-l${t#level}-image" "$t" false "$i"
      row R-rollback-genuine build "lineage-l${t#level}-build" "$t" false "$i"
      row S-rollback-commit-record image main "$t" true "$i"
      row S-rollback-image-record image main "$t" true "$i"
    done
  done
  for site in l0-image l0-build l1-image l1-build; do
    jq -nc --arg site "lineage-$site" --arg mode "${site#*-}" --argjson origin "${site:1:1}" \
      '{kind:"positive", acceptance:"A-lineage-upgrade", mode:$mode, origin:$origin, site:$site, fixture:false, id:$site, pass:true, floor:"2"}'
  done
  jq -nc '{kind:"positive", acceptance:"A-same-level-rollback", fixture:false, id:"same-level-rollback", pass:true, reopenedVersion:{recoveryPolicy:2}}'
  jq -nc '{kind:"positive", acceptance:"A-current-release-restore", fixture:false, id:"current-release-restore", pass:true, level:2, restartsBefore:0, restartsAfter:1, proof:"protocol: curl against the real server and database; the React panel is covered elsewhere, not by this case",
    scenarios:{noAttempt:{unchanged:"applied", changed:"reason_review_changed", reloadedPress:"applied"},
               retainedLive:{answer:"restore_detected", replay:"attempt_expired", settle:"history_unavailable", reloadShowsAccount:true, freshPress:"applied"},
               retainedRestoreSh:{replay:"attempt_expired", settle:"history_unavailable", reloadShowsAccount:true, freshPress:"applied"}}}'
  jq -nc '{kind:"positive", acceptance:"A-older-archive", fixture:false, id:"older-archive-lane-base", pass:true, archive:{id:"lane-base"}}'
  jq -nc '{kind:"positive", acceptance:"A-older-archive", fixture:false, id:"older-archive-reason-review", pass:true, archive:{id:"reason-review"}}'
}
gen_images() {
  local role level id sha
  for role in candidate prior landing lane archive; do
    case "$role" in candidate|prior) level=2 ;; landing) level=1 ;; *) level=0 ;; esac
    id="sha256:$(printf '%s' "$role" | sha256sum | cut -d' ' -f1)"
    sha="$(printf '%s' "$role" | sha1sum | cut -d' ' -f1)"
    jq -nc --arg role "$role" --argjson level "$level" --arg id "$id" --arg sha "$sha" '{role:$role, level:$level, imageId:$id, sha:$sha}'
  done
}
gen_cases >"$WORK/cases.jsonl"; gen_images >"$WORK/images.jsonl"
crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.jsonl"
mutated() { # name jq-filter: writes cases.<name> and expects the evidence to be refused
  jq -c "$2" "$WORK/cases.jsonl" >"$WORK/cases.$1"
  [[ -s "$WORK/cases.$1" ]]
  refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.$1" "$WORK/images.jsonl"
}
sed '1d' "$WORK/cases.jsonl" >"$WORK/cases.short"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.short" "$WORK/images.jsonl"
mutated failed 'if .id=="main-R-restore-image-level1-2" then .pass=false else . end'
mutated rc 'if .id=="lineage-l1-image-R-rollback-genuine-level1-1" then .exitCode=75 else . end'
# Only image-mode (or only build-mode) refusals do not show both deployments.
mutated image-only 'select(.deployed != "build")'
# Every matrix runs toward both targets: dropping one target on one site fails.
mutated one-target 'select((.site == "lineage-l1-build" and .target == "level1" and .acceptance == "R-forward-image") | not)'
mutated only-level0 'select(.target != "level1")'
# A genuine rollback must come from its own origin: a level-0 site cannot claim level 1.
mutated wrong-origin 'if .acceptance=="R-rollback-genuine" and .site=="lineage-l1-image" then .target="level0" else . end'
grep -v '"A-older-archive"' "$WORK/cases.jsonl" >"$WORK/cases.no-archive"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.no-archive" "$WORK/images.jsonl"
grep -v 'older-archive-reason-review' "$WORK/cases.jsonl" >"$WORK/cases.one-archive"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.one-archive" "$WORK/images.jsonl"
grep -v 'current-release-restore' "$WORK/cases.jsonl" >"$WORK/cases.no-current"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.no-current" "$WORK/images.jsonl"
mutated archive-twice 'if .id=="older-archive-reason-review" then .archive.id="lane-base" else . end'
mutated not-level2 'if .acceptance=="A-current-release-restore" then .level=1 else . end'
# The panel scenarios are each required: an open panel's immediate success, the changed-hash refusal and the reload press,
# and for a retained id the restore_detected answer, expiry, settle, reload and the fresh press, live and through restore.sh.
mutated no-restart 'if .acceptance=="A-current-release-restore" then .restartsAfter=0 else . end'
mutated imm-missing 'if .acceptance=="A-current-release-restore" then del(.scenarios.noAttempt.unchanged) else . end'
mutated imm-refused 'if .acceptance=="A-current-release-restore" then .scenarios.noAttempt.unchanged="attempt_expired" else . end'
mutated changed-applied 'if .acceptance=="A-current-release-restore" then .scenarios.noAttempt.changed="applied" else . end'
mutated no-reload-press 'if .acceptance=="A-current-release-restore" then .scenarios.noAttempt.reloadedPress="reason_review_changed" else . end'
mutated live-not-detected 'if .acceptance=="A-current-release-restore" then .scenarios.retainedLive.answer="applied" else . end'
mutated live-not-expired 'if .acceptance=="A-current-release-restore" then .scenarios.retainedLive.replay="applied" else . end'
mutated live-settle-applied 'if .acceptance=="A-current-release-restore" then .scenarios.retainedLive.settle="applied" else . end'
mutated live-no-reload 'if .acceptance=="A-current-release-restore" then .scenarios.retainedLive.reloadShowsAccount=false else . end'
mutated live-no-fresh 'if .acceptance=="A-current-release-restore" then del(.scenarios.retainedLive.freshPress) else . end'
mutated sh-not-expired 'if .acceptance=="A-current-release-restore" then .scenarios.retainedRestoreSh.replay="restore_detected" else . end'
mutated sh-no-settle 'if .acceptance=="A-current-release-restore" then .scenarios.retainedRestoreSh.settle="not_applied" else . end'
mutated sh-no-reload 'if .acceptance=="A-current-release-restore" then del(.scenarios.retainedRestoreSh.reloadShowsAccount) else . end'
mutated sh-no-fresh 'if .acceptance=="A-current-release-restore" then .scenarios.retainedRestoreSh.freshPress="attempt_expired" else . end'
mutated no-proof 'if .acceptance=="A-current-release-restore" then del(.proof) else . end'
mutated react-proof 'if .acceptance=="A-current-release-restore" then .proof="proves the React panel" else . end'
mutated no-scenarios 'if .acceptance=="A-current-release-restore" then del(.scenarios) else . end'
mutated rollback-level1 'if .acceptance=="A-same-level-rollback" then .reopenedVersion.recoveryPolicy=1 else . end'
mutated lineage-floor 'if .acceptance=="A-lineage-upgrade" and .id=="lineage-l0-build" then .floor="1" else . end'
mutated lineage-origin 'if .id=="lineage-l1-build" then .origin=0 else . end'
# A fixture row that claims acceptance, an acceptance row that claims to be a
# fixture, an unlabelled extra acceptance and a genuine rollback row from the
# wrong site all fail.
mutated unlabelled 'if .acceptance=="S-rollback-image-record" and .id=="main-S-rollback-image-record-level1-1" then .fixture=false else . end'
mutated fixture-claim 'if .id=="main-R-forward-image-level0-1" then .fixture=true else . end'
mutated wrong-site 'if .acceptance=="R-rollback-genuine" then .site="main" else . end'
{ cat "$WORK/cases.jsonl"; jq -nc '{kind:"positive", acceptance:"A-unknown", fixture:false, id:"u", pass:true}'; } >"$WORK/cases.extra"
refused 'incomplete or failed' crossing_evidence_complete "$WORK/cases.extra" "$WORK/images.jsonl"
for role in archive prior landing; do
  grep -v "\"imageId\":\"sha256:$(printf '%s' "$role" | sha256sum | cut -d' ' -f1)\"" "$WORK/images.jsonl" >"$WORK/images.no-$role"
  refused 'image evidence is incomplete' crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.no-$role"
done
jq -c 'if .role=="landing" then .level=0 else . end' "$WORK/images.jsonl" >"$WORK/images.level"
refused 'image evidence is incomplete' crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.level"
jq -c 'if .role=="candidate" then .level=1 else . end' "$WORK/images.jsonl" >"$WORK/images.candidate-level"
refused 'image evidence is incomplete' crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.candidate-level"
jq -c 'if .role=="prior" then .level=1 else . end' "$WORK/images.jsonl" >"$WORK/images.prior-level"
refused 'image evidence is incomplete' crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.prior-level"
jq -c 'if .role=="archive" then .level=2 else . end' "$WORK/images.jsonl" >"$WORK/images.archive-level"
refused 'image evidence is incomplete' crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.archive-level"
jq -c 'if .role=="archive" then .level=1 else . end' "$WORK/images.jsonl" >"$WORK/images.archive-level1"
crossing_evidence_complete "$WORK/cases.jsonl" "$WORK/images.archive-level1"
: >"$WORK/empty.jsonl"
refused 'missing or empty' crossing_evidence_complete "$WORK/empty.jsonl" "$WORK/images.jsonl"

# --- the runner's wiring ---------------------------------------------------------
RUNNER="$ROOT/deploy/digitalocean/restore-drill.sh"
grep -q '^  real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore final-verification$' "$RUNNER"
loop="$(sed -n '/^for drill_family in/,/; do$/p' "$RUNNER")"
for family in real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore; do
  grep -q "$family" <<<"$loop"
  declare -F "$family" >/dev/null
done
sed -n '/^drill_case_begin final-verification/,/^drill_case_end/p' "$RUNNER" | grep -q '^assert_crossing_evidence$'
declare -F assert_crossing_evidence >/dev/null
validate_line="$(grep -n '^crossing_validate_revisions "\$ROOT"' "$RUNNER" | cut -d: -f1)"
bootstrap_line="$(grep -n '^drill_case_begin bootstrap$' "$RUNNER" | cut -d: -f1)"
[[ -n "$validate_line" && -n "$bootstrap_line" && "$validate_line" -lt "$bootstrap_line" ]]
grep -q '^crossing_validate_revisions "\$ROOT" "\$LANE_REVISION" "\$LANDING_REVISION" "\$PRIOR_REVISION" "\$ARCHIVE_BASE_REVISION" "\$REVISION"' "$RUNNER"
clean_line="$(grep -n '^crossing_require_clean_head "\$ROOT"' "$RUNNER" | cut -d: -f1)"
[[ -n "$clean_line" && "$clean_line" -lt "$bootstrap_line" ]]
grep -q 'crossing_cleanup_all' "$RUNNER"
sed -n '/^reset_case() {/,/^}/p' "$RUNNER" >"$WORK/reset-case.txt"
grep -q 'image="${1:-$BASE_REGISTRY_IMAGE}"' "$WORK/reset-case.txt"
grep -q -- '--image "$image"' "$WORK/reset-case.txt"
help="$(bash "$RUNNER" --help)"
for flag in --lane-base --landing --prior --archive-base; do grep -q -- "$flag" <<<"$help"; done
# The defaults are 40-hex.
for sha in "$CROSSING_LANE_DEFAULT" "$CROSSING_LANDING_DEFAULT" "$CROSSING_PRIOR_DEFAULT" "$CROSSING_ARCHIVE_DEFAULT"; do [[ "$sha" =~ ^[0-9a-f]{40}$ ]]; done
# The main site's host floor is the candidate's level once the candidate runs on
# it: no real_* family may reset it to the fixture (level 1) base image.
CROSS="$ROOT/deploy/digitalocean/restore-drill-crossings.sh"
if grep -nE '^\s+reset_case$' "$CROSS" >"$WORK/bare-reset.txt"; then
  echo 'crossing: a real_* family resets the main site to the fixture level-1 image after the floor rose to 2:' >&2
  cat "$WORK/bare-reset.txt" >&2; exit 1
fi
grep -q 'CROSSING_CANDIDATE_LEVEL=2' "$CROSS"
# --- the panel scenarios (V-01) -----------------------------------------------------------------------
# An open panel holds no attempt id; a press mints one; only the retained cases mint ahead.
# The set-v1 wire (T07B-2 scaling): count, digest and two-bit choices, no per-reason items. The expected digest is computed here with xxd and
# sha256sum from the format alone (label, 0x00, uint64 count, then int64 id and raw 32-byte hash per item), so the helper is checked against an
# implementation that shares none of its code.
h1="$(printf '1%.0s' {1..64})"; h2="$(printf 'b%.0s' {1..64})"
printf '{"target":7,"attemptId":null,"items":[{"requestId":"10","reviewHash":"%s"},{"requestId":"11","reviewHash":"%s"}]}\n' "$h1" "$h2" >"$WORK/panel.json"
want_digest="$({ printf '%s' 'fold:erasure-reason-set:v1' | xxd -p | tr -d '\n'; printf '00%016x%016x%s%016x%s' 2 10 "$h1" 11 "$h2"; } | xxd -r -p | sha256sum | cut -d' ' -f1)"
[[ "$(crossing_panel_body "$WORK/panel.json" ID1 | jq -c '[.attemptId, .reasonReview]')" == '["ID1",{"format":"set-v1","count":"2","digest":"'"$want_digest"'","choices":"Bg=="}]' ]]
[[ "$(crossing_panel_body "$WORK/panel.json" ID1 | jq -c 'keys_unsorted')" == '["successors","hostSuccessors","attemptId","reasonReview"]' ]]
[[ "$(crossing_panel_body "$WORK/panel.json" "" | jq -c 'has("attemptId")')" == false ]]
# No old-form per-reason list anywhere in the crossing bodies.
if crossing_panel_body "$WORK/panel.json" ID1 | grep -q '"items"'; then echo 'crossing: a panel body carries the old items envelope' >&2; exit 1; fi
if grep -n 'reasonReview:{items' "$ROOT/deploy/digitalocean/restore-drill-crossings.sh"; then echo 'crossing: the crossings still build the old items envelope' >&2; exit 1; fi
# The helper's byte-for-byte contract is the product's shared vectors.
node "$ROOT/deploy/digitalocean/qc1/set-v1.mjs" selftest --vectors "$ROOT/web/src/lib/reasonSetVectors.json" >/dev/null
CROSS="$ROOT/deploy/digitalocean/restore-drill-crossings.sh"
body="$(sed -n '/^real_current_release_restore() {/,/^}/p' "$CROSS")"
grep -q "jq -e '.attemptId == null' \"\$cdir/panel-\$t1.json\" \"\$cdir/panel-\$t2.json\"" <<<"$body"
grep -q 'crossing_panel_mint "\$t3"' <<<"$body"; grep -q 'crossing_panel_mint "\$t4"' <<<"$body"
if grep -q 'crossing_panel_mint "\$t1"\|crossing_panel_mint "\$t2"' <<<"$body"; then echo 'crossing: an open panel must not hold an attempt id' >&2; exit 1; fi
# The open panel presses with a fresh id; the changed hash is refused and then reloaded before the press that applies.
grep -q 'status="\$(crossing_press "\$t1" "\$cdir/panel-\$t1.json" "\$out")"' <<<"$body"
grep -q 'crossing_answer_is "\$status" 409 "\$out" ./.code == "reason_review_changed"./' <<<"$body" || grep -q 'reason_review_changed' <<<"$body"
grep -q 'panel-\$t2.reloaded.json' <<<"$body"
# A retained id: restore_detected, expiry, settle, a reload that shows the account, then a fresh press.
for step in 'restore_detected' 'attempt_expired' 'history_unavailable' 'crossing_reload_shows "\$t3"' 'crossing_reload_shows "\$t4"' 'crossing_press "\$t3"' 'crossing_press "\$t4"'; do grep -q -- "$step" <<<"$body" || { echo "crossing: the panel case lacks $step" >&2; exit 1; }; done
reload_line="$(grep -n 'crossing_reload_shows "\$t3"' <<<"$body" | head -n 1 | cut -d: -f1)"; press_line="$(grep -n 'status="\$(crossing_press "\$t3"' <<<"$body" | head -n 1 | cut -d: -f1)"
[[ -n "$reload_line" && -n "$press_line" && "$reload_line" -lt "$press_line" ]]
# The reload is the users list, as the panel does it.
sed -n '/^crossing_reload_shows() {/,/^}/p' "$CROSS" | grep -q 'api GET /admin/users'
# The wording the first-church check promises matches the drill.
fcc="$ROOT/docs/FIRST-CHURCH-CHECKS.md"
panel_item="$(grep -n 'reason-review panel' "$fcc" | head -n 1 | cut -d: -f1)"
[[ -n "$panel_item" ]]
item_text="$(sed -n "${panel_item}p" "$fcc")"
for need in 'never press Delete' 'applies' 'reasons changed' 'request blocking' 'operation-attempts/settle' 'mint' 'Check again' 'Close' 'choose again' 'users list reloads' 'offline blocks the mint too'; do
  grep -qi -- "$need" <<<"$item_text" || { echo "first-church item lacks: $need" >&2; exit 1; }
done
# Going offline before the mint is not a way to retain an attempt: the item says so and never asks for it.
if grep -qiE 'set devtools to offline|go offline before|press delete once with .*offline' <<<"$item_text"; then echo 'first-church item asks for the offline switch' >&2; exit 1; fi
# The words the panel shows are the ones the item quotes, and the buttons it names exist.
# The panel's copy and buttons come from the application facts (DeleteUserPanel.test.tsx holds them equal to what the panel shows and calls).
history_copy="$(jq -r '.web.copy.historyUnavailable' "$ROOT/deploy/digitalocean/qc1/app-facts.json")"
[[ -n "$history_copy" ]] && grep -qF -- "$history_copy" <<<"$item_text"
# The item says the drill is protocol only.
grep -qi 'only checks the server protocol with curl' <<<"$item_text"

# --- the os-release stand-in in the crossing cases (drill4: upgrade.sh -> provision.sh check_os refused "alpine 3.24.4") ---------------------------------------------
# Wiring: applied once in the first crossing case, before any image is built; restored by the runner's EXIT trap; part of the evidence.
prep_body="$(sed -n '/^crossing_prepare() {/,/^}/p' "$ROOT/deploy/digitalocean/restore-drill-crossings.sh")"
os_l="$(grep -n 'crossing_os_standin "\$cdir"' <<<"$prep_body" | head -n 1 | cut -d: -f1)"; build_l="$(grep -n 'crossing_build_image candidate' <<<"$prep_body" | head -n 1 | cut -d: -f1)"
[[ -n "$os_l" && -n "$build_l" && "$os_l" -lt "$build_l" ]]
grep -q 'isolation_os_restore "\$CROSSING_OS_STATE"' "$RUNNER"
cleanup_body="$(sed -n '/^cleanup() {/,/^}/p' "$RUNNER")"
[[ "$(grep -n 'crossing_cleanup_all' <<<"$cleanup_body" | head -n 1 | cut -d: -f1)" -lt "$(grep -n 'isolation_os_restore' <<<"$cleanup_body" | head -n 1 | cut -d: -f1)" ]]
[[ "$(grep -n 'isolation_os_restore' <<<"$cleanup_body" | head -n 1 | cut -d: -f1)" -lt "$(grep -n 'drill_emit result' <<<"$cleanup_body" | head -n 1 | cut -d: -f1)" ]]
sed -n '/^assert_crossing_evidence() {/,/^}/p' "$ROOT/deploy/digitalocean/restore-drill-crossings.sh" | grep -qxF "  jq -e '.applied != null and .path == \"/etc/os-release\"' \"\$cdir/os-release-standin.json\" >/dev/null"
guard_l="$(grep -n '^isolation_require_disposable || exit 1' "$RUNNER" | cut -d: -f1)"; loop_l="$(grep -n '^for drill_family in' "$RUNNER" | cut -d: -f1)"
[[ "$guard_l" -lt "$loop_l" ]]
# The only callers of the writer are the crossing library (after that guard); no --allow-os was added to any upgrade.sh call; no shipped tool mentions the stand-in.
[[ "$(grep -lE 'isolation_os_standin' "$ROOT"/deploy/digitalocean/*.sh | xargs -n1 basename | sort | tr '\n' ' ')" == 'isolation-fixtures.sh isolation.sh restore-crossings-fixtures.sh restore-drill-crossings.sh ' ]]
if grep -hvE '^[[:space:]]*#|never forwards' "$ROOT/deploy/digitalocean/restore-drill-crossings.sh" "$ROOT/deploy/digitalocean/restore-drill-cases.sh" "$ROOT/deploy/digitalocean/restore-drill.sh" | grep -nE 'upgrade\.sh[^|]*--allow-os|--allow-os[^|]*upgrade\.sh'; then echo 'a crossing runs upgrade.sh with --allow-os' >&2; exit 1; fi
if grep -lE 'os-release|os_release|isolation_os' "$ROOT"/deploy/digitalocean/{provision,upgrade,restore,backup,smoke,checks}.sh 2>/dev/null | grep -v provision.sh | grep .; then echo 'a shipped tool changed for the stand-in' >&2; exit 1; fi

# Behaviour, in a temp tree: Alpine's link shape, the real revisions' own check_os, and the evidence it writes.
CXO="$WORK/cxos"; mkdir -p "$CXO/etc" "$CXO/usr/lib" "$WORK/crossings"
printf 'ID=alpine\nVERSION_ID=3.24.4\n' >"$CXO/usr/lib/os-release"; ln -s ../usr/lib/os-release "$CXO/etc/os-release"
printf '36 35 0:50 / / rw - overlay overlay rw\n' >"$CXO/mi"
head_rev="$(git -C "$ROOT" rev-parse HEAD)"
for v in REVISION LANE_REVISION LANDING_REVISION PRIOR_REVISION ARCHIVE_BASE_REVISION; do
  default="$head_rev"
  case "$v" in LANE_REVISION) default="$CROSSING_LANE_DEFAULT" ;; LANDING_REVISION) default="$CROSSING_LANDING_DEFAULT" ;; PRIOR_REVISION) default="$CROSSING_PRIOR_DEFAULT" ;; ARCHIVE_BASE_REVISION) default="$CROSSING_ARCHIVE_DEFAULT" ;; esac
  if git -C "$ROOT" cat-file -e "$default^{commit}" 2>/dev/null; then printf -v "$v" '%s' "$default"; else echo "note: $v default $default is not in this clone; the check uses HEAD for it" >&2; printf -v "$v" '%s' "$head_rev"; fi
done
# shellcheck disable=SC2030,SC2031 # the exports belong to this subshell on purpose
(
  export FOLD_DRILL_ISOLATED_DAEMON=1 FOLD_DRILL_ASSUME_CONTAINER=1
  crossing_os_standin "$WORK/crossings" "$CXO/etc/os-release" "$CXO/mi"
  [[ "$(isolation_os_identity "$CXO/etc/os-release")" == 'ubuntu 24.04' ]]
  jq -e '.applied == true and .original.id == "alpine"' "$WORK/crossings/os-release-standin.json" >/dev/null
  [[ "$(jq -s '[.[] | select(.checkOsAcceptsStandIn == true)] | length' "$WORK/crossings/tools.jsonl")" == 5 ]]
  jq -s -e 'any(.[]; .file == "/etc/os-release" and .replacedByDrill == true)' "$WORK/crossings/tools.jsonl" >/dev/null
  grep -q '^DEVIATION real_\* cases: .*Ubuntu 24.04 /etc/os-release' "$WORK/deviations.txt"
  grep -q 'nothing outside the container is touched' "$WORK/deviations.txt"
  isolation_os_restore "$WORK/crossings/os-release-standin.json"
  [[ -L "$CXO/etc/os-release" && "$(isolation_os_identity "$CXO/etc/os-release")" == 'alpine 3.24.4' ]]
)
# A provisioner that would STILL refuse the stand-in stops the run before any image is built (here: a revision whose check_os wants Ubuntu 99).
rm -rf "$WORK/crossings" "$WORK/deviations.txt"; mkdir -p "$WORK/crossings"; rm -f "$CXO/etc/os-release"; ln -s ../usr/lib/os-release "$CXO/etc/os-release"
FAKE="$WORK/fakeroot"; mkdir -p "$FAKE/deploy/digitalocean"
cat >"$FAKE/deploy/digitalocean/provision.sh" <<'FP'
check_os() {
  . /etc/os-release
  if [[ "${ID:-}" == "ubuntu" && "${VERSION_ID:-}" == "99.04" ]]; then echo "os: Ubuntu $VERSION_ID"; return 0; fi
  die "Ubuntu 99.04 required"
}
FP
git -C "$FAKE" init -q; git -C "$FAKE" config user.name f; git -C "$FAKE" config user.email f@example.invalid; git -C "$FAKE" config commit.gpgsign false; git -C "$FAKE" config core.hooksPath /dev/null
git -C "$FAKE" add -A; git -C "$FAKE" commit -qm fake
fake_rev="$(git -C "$FAKE" rev-parse HEAD)"
rc=0
# shellcheck disable=SC2030,SC2031 # the exports belong to this subshell on purpose
( ROOT="$FAKE"; REVISION="$fake_rev"; LANE_REVISION="$fake_rev"; LANDING_REVISION="$fake_rev"; PRIOR_REVISION="$fake_rev"; ARCHIVE_BASE_REVISION="$fake_rev"
  export FOLD_DRILL_ISOLATED_DAEMON=1 FOLD_DRILL_ASSUME_CONTAINER=1
  crossing_os_standin "$WORK/crossings" "$CXO/etc/os-release" "$CXO/mi" ) >"$WORK/out" 2>&1 || rc=$?
[[ "$rc" != 0 ]] && grep -q 'would still refuse this container' "$WORK/out"
[[ ! -e "$WORK/deviations.txt" ]]
# ...and its state file still lets the EXIT trap put the identity back
jq -e '.applied == true' "$WORK/crossings/os-release-standin.json" >/dev/null
isolation_os_restore "$WORK/crossings/os-release-standin.json"; [[ -L "$CXO/etc/os-release" ]]

# --- the current-release archive carries the fixture's "archive" marker (crossings1: assert_private refused a raw pg_dump that read "live") ----------------------------
# reset_case ends with capture_original, which sets drill_sentinel id=2 to "live" in the database; assert_private requires the restored copy to read "archive". So a dump
# of the live database taken after a reset is not an archive. The one place that makes a dump the drill restores from is dump_current_fixture_archive.
CASES="$ROOT/deploy/digitalocean/restore-drill-cases.sh"; CROSS="$ROOT/deploy/digitalocean/restore-drill-crossings.sh"
grep -q "UPDATE drill_sentinel SET value='live' WHERE id=2" <(sed -n '/^capture_original() {$/,/^}$/p' "$CASES")          # the cause: every reset leaves "live" behind
grep -qF "[[ \"\$(pg fold -c 'SELECT value FROM drill_sentinel WHERE id=2')\" == archive ]]" "$RUNNER"                    # the assertion stays exactly as strict as it was
sed -n '/^assert_private() {$/,/^}$/p' "$RUNNER" | grep -qF "drill_sentinel WHERE id=2')\" == archive ]]"
# No crossing case dumps the live database with a raw pg_dump (the older-archive case dumps its own fixture database, which has no sentinel)
if grep -vE '^[[:space:]]*#' "$CROSS" | grep -nE 'pg_dump -U church -Fc fold( |>|$)' | grep -v 'CROSSING_FORBID_UP'; then echo 'a crossing case dumps the live database without the archive marker' >&2; exit 1; fi
cur_body="$(sed -n '/^real_current_release_restore() {$/,/^}$/p' "$CROSS")"
d_l="$(grep -n 'dump_current_fixture_archive "\$dump"' <<<"$cur_body" | head -n 1 | cut -d: -f1)"; r_l="$(grep -n 'current-release/live-pg_restore' <<<"$cur_body" | head -n 1 | cut -d: -f1)"
p_l="$(grep -n 'crossing_panel_mint "\$t4"' <<<"$cur_body" | head -n 1 | cut -d: -f1)"; x_l="$(grep -n "restore --dump \"\$dump\"" <<<"$cur_body" | head -n 1 | cut -d: -f1)"
del_l="$(grep -n "api DELETE \"/admin/users/\$j1\"" <<<"$cur_body" | head -n 1 | cut -d: -f1)"
[[ -n "$d_l$r_l$p_l$x_l$del_l" && "$p_l" -lt "$d_l" && "$d_l" -lt "$del_l" && "$del_l" -lt "$r_l" && "$r_l" -lt "$x_l" ]]       # panels first, then the archive, then the commit the dump lacks
# Behaviour of the helper the case now uses, against a stub database that models the sentinel row: the dump reads "archive", the database reads "live" again afterwards.
# shellcheck source=deploy/digitalocean/restore-drill-cases.sh
source "$CASES"
SENT="$WORK/sentinel"; printf 'live\n' >"$SENT"          # what reset_case leaves
pg() { # database -c sql
  case "${3:-}" in
    "UPDATE drill_sentinel SET value='archive' WHERE id=2") printf 'archive\n' >"$SENT" ;;
    "UPDATE drill_sentinel SET value='live' WHERE id=2") printf 'live\n' >"$SENT" ;;
    "SELECT value FROM drill_sentinel WHERE id=2") cat "$SENT" ;;
    *) echo "unexpected sql: $3" >&2; return 1 ;;
  esac
}
dc() { # exec -T db pg_dump ... : the dump is the sentinel as it is right now
  [[ "$*" == 'exec -T db pg_dump -U church -Fc fold' ]] || { echo "unexpected dc: $*" >&2; return 1; }
  printf 'sentinel=%s\n' "$(cat "$SENT")"
}
capture_original() { printf 'live\n' >"$SENT"; }   # the real one updates the row to "live" (checked above); the rest of it needs a database
validate_deploy_drill() { return 0; }
(
  set -Eeuo pipefail
  DIR="$WORK/site"; PROJECT="fold-deploy-drill-x"; DEPLOY_DRILL=1; mkdir -p "$WORK/crossings"
  # a raw dump after a reset is the "live" copy that assert_private refused
  dc exec -T db pg_dump -U church -Fc fold >"$WORK/crossings/raw.dump"; grep -qx 'sentinel=live' "$WORK/crossings/raw.dump"
  dump_current_fixture_archive "$WORK/crossings/current-release.dump"
  grep -qx 'sentinel=archive' "$WORK/crossings/current-release.dump"          # the restored copy will read "archive"
  [[ "$(cat "$SENT")" == live ]]                                              # and the database it replaces reads "live" again, so a swap stays observable
  # the helper still refuses outside the drill's own directory and project
  rc=0; dump_current_fixture_archive "/tmp/elsewhere.dump" >/dev/null 2>&1 || rc=$?; [[ "$rc" != 0 ]]
  rc=0; PROJECT=owner dump_current_fixture_archive "$WORK/crossings/x.dump" >/dev/null 2>&1 || rc=$?; [[ "$rc" != 0 ]]
)
unset -f pg dc capture_original validate_deploy_drill
echo 'restore crossing fixtures: ok'
