#!/usr/bin/env bash
# Daemon-free regressions for the drill's two modes: the full run (the default, the only acceptance) and
# `--crossings-only`, a labelled PARTIAL diagnostic of the bootstrap, the fixture prerequisites and the five real
# level-2 crossing cases. Proves what is selected, what it needs, how it cleans up, how its evidence differs from a
# full run's, and that the default is unchanged. No Docker, database, browser or network.
# shellcheck disable=SC2016,SC2030,SC2031,SC2020,SC2329 # patterns keep their dollar signs literal; subshell state is on purpose.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
RUNNER="$HERE/restore-drill.sh"
# shellcheck source=deploy/digitalocean/checks.sh
source "$HERE/checks.sh"
# shellcheck source=deploy/digitalocean/isolation.sh
source "$HERE/isolation.sh"
# shellcheck source=deploy/digitalocean/restore-drill-cases.sh
source "$HERE/restore-drill-cases.sh"
# shellcheck source=deploy/digitalocean/restore-drill-crossings.sh
source "$HERE/restore-drill-crossings.sh"
WORK="$(mktemp -d /tmp/fold-drill-modes-fixture.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "drill modes fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR
refused() { local needle="$1" rc=0; shift; "$@" >"$WORK/out" 2>&1 || rc=$?; [[ "$rc" != 0 ]]; [[ -z "$needle" ]] || grep -qF -- "$needle" "$WORK/out"; }

# --- the plans: the full run's is pinned, the diagnostic's is its own ---------------------------------------------------------------------------------------
FULL='bootstrap base-proof recovery-interruptions reopen-interruptions abort-interruptions tamper accidental-app-reuse abort-ack-loss operations-lock recovery-configuration count_races archive_failures pin_and_abort_cases cleanup_and_tamper_cases concurrency_cases lifecycle_cases legacy_archive_case review_reconciliation_cases review_backup_archive_cases review_cleanup_retry_cases review_orphaned_restore_case review_moving_tag_case uploads_erasure_cases uploads_interruption_cases backup_exclusion_cases real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore final-verification'
plan_in_runner="$(awk '/^drill_progress_init bootstrap /{on=1} on{print} on && /final-verification$/{exit}' "$RUNNER" | sed 's/^drill_progress_init //; s/\\$//' | tr -s ' \n' '\n\n' | grep -v '^$' | paste -sd' ')"
[[ "$plan_in_runner" == "$FULL" ]] || { echo "the full run's plan changed: $plan_in_runner" >&2; exit 1; }   # change it on purpose, here too
[[ "${DRILL_FULL_PLAN_NAMES[*]}" == "$FULL" ]]
# the full run's plan call is in the full branch and the diagnostic's is in the other
grep -qx 'DRILL_PROGRESS_MODE="$MODE"' "$RUNNER"
[[ "$(grep -n '^if \[\[ "\$MODE" == full \]\]; then$' "$RUNNER" | head -n 3 | cut -d: -f1 | tail -n 1)" -gt 0 ]]
init_l="$(grep -n '^drill_progress_init bootstrap ' "$RUNNER" | cut -d: -f1)"
[[ "$(sed -n "$((init_l - 1))p" "$RUNNER")" == 'if [[ "$MODE" == full ]]; then' ]]
else_l="$(awk -v s="$init_l" 'NR > s && /^else$/ {print NR; exit}' "$RUNNER")"
[[ "$(sed -n "$((else_l + 1))p" "$RUNNER")" == 'drill_progress_init "${CROSSINGS_ONLY_PLAN[@]}"' && "$(sed -n "$((else_l + 2))p" "$RUNNER")" == 'fi' ]]
# the diagnostic: the bootstrap, the fixture prerequisites, the five real families in the full run's order, the evidence check; nothing else
[[ "${CROSSINGS_ONLY_PLAN[*]}" == 'bootstrap base-prerequisites real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore crossings-evidence' ]]
[[ "${CROSSINGS_ONLY_FAMILIES[*]}" == "$(printf '%s' "$FULL" | grep -oE 'real_binary_refusals.*real_older_archive_restore')" ]]
for f in "${CROSSINGS_ONLY_FAMILIES[@]}"; do declare -F "$f" >/dev/null; done
# Every check that pipes a long producer into grep reads to the end (no grep -q):
# under pipefail, a grep that quits at its first match SIGPIPEs the producer and
# the script intermittently exits 141.
# no case of the diagnostic is named like a full-run case it replaces, so a plan can never be read as the full plan
if printf '%s\n' "${CROSSINGS_ONLY_PLAN[@]}" | grep -x 'base-proof\|final-verification' >/dev/null; then echo 'the diagnostic plan names a full-run case' >&2; exit 1; fi
# the full run's family loop still lists the same five, last
loop="$(sed -n '/^for drill_family in/,/; do$/p' "$RUNNER" | sed 's/^for drill_family in //; s/; do$//; s/\\$//' | tr -s ' \n' '\n\n' | grep -v '^$' | paste -sd' ')"
[[ "$loop" == "count_races archive_failures pin_and_abort_cases cleanup_and_tamper_cases concurrency_cases lifecycle_cases legacy_archive_case review_reconciliation_cases review_backup_archive_cases review_cleanup_retry_cases review_orphaned_restore_case review_moving_tag_case uploads_erasure_cases uploads_interruption_cases backup_exclusion_cases real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore" ]]

# --- the runner's wiring: default full, one flag, prerequisites present, the rest skipped ------------------------------------------------------------------
grep -qx 'MODE=full' "$RUNNER"
[[ "$(grep -c -- '--crossings-only) MODE=crossings-only; shift ;;' "$RUNNER")" == 1 ]]
[[ "$(grep -c 'MODE=crossings-only' "$RUNNER")" == 1 ]]
"$RUNNER" --help >"$WORK/help.out"; grep -q -- '--crossings-only' "$WORK/help.out"; grep -q 'never full acceptance' "$WORK/help.out"
"$RUNNER" --crossings-only --help >"$WORK/help2.out"; cmp "$WORK/help.out" "$WORK/help2.out"
guard_l="$(grep -n '^isolation_require_disposable || exit 1' "$RUNNER" | cut -d: -f1)"
pre_l="$(grep -n 'QC1_PREFLIGHT_FOR=restore QC1_WEB_DIR=' "$RUNNER" | head -n 1 | cut -d: -f1)"
ffif_l="$(grep -n '^if \[\[ "\$MODE" == full \]\]; then$' "$RUNNER" | head -n 1 | cut -d: -f1)"
ffr_l="$(grep -n 'FIREFOX_RUNTIME="${FOLD_FIREFOX_RUNTIME:-}"' "$RUNNER" | cut -d: -f1)"
# the Chromium preflight stays mandatory in both modes (before any MODE conditional); only the Firefox record is the full run's
[[ "$guard_l" -lt "$pre_l" && "$pre_l" -lt "$ffr_l" && "$ffr_l" -lt "$ffif_l" ]]
sed -n "${ffif_l},$((ffif_l + 8))p" "$RUNNER" | grep 'was not prepared' >/dev/null
fi_l="$(awk -v s="$ffif_l" 'NR > s && /^fi$/ { n++; if (n == 2) { print NR; exit } }' "$RUNNER")"   # the outer fi after the inner one
sed -n "$((fi_l - 4)),${fi_l}p" "$RUNNER" | grep 'is not the one the preparation proved' >/dev/null
grep -qx '\[\[ "$MODE" != full \]\] || cp "$FIREFOX_RUNTIME" "$WORK/firefox-runtime.json"' "$RUNNER"
# markers are written by the diagnostic only, once WORK exists and the cleanup trap is installed
grep -qx '\[\[ "$MODE" == full \]\] || drill_mode_begin' "$RUNNER"
trap_l="$(grep -n '^trap cleanup EXIT$' "$RUNNER" | cut -d: -f1)"; begin_l="$(grep -n '^\[\[ "\$MODE" == full \]\] || drill_mode_begin$' "$RUNNER" | cut -d: -f1)"
bc_l="$(grep -n '^drill_case_begin "\$BASE_CASE"$' "$RUNNER" | cut -d: -f1)"
[[ "$trap_l" -lt "$begin_l" && "$begin_l" -lt "$bc_l" ]]
grep -qx 'BASE_CASE="base-proof"' "$RUNNER"; grep -qx '\[\[ "$MODE" == full \]\] || BASE_CASE="base-prerequisites"' "$RUNNER"
# prerequisites: everything the crossing cases and reset_case read is made before the diagnostic leaves the full flow
dump_l="$(grep -n '^sha256sum "\$DB_DUMP"$' "$RUNNER" | cut -d: -f1)"
wrap_l="$(grep -n '^if \[\[ "\$MODE" == full \]\]; then$' "$RUNNER" | awk -F: -v d="$dump_l" '$1 > d {print $1; exit}')"
[[ "$wrap_l" == "$((dump_l + 2))" ]]   # one comment line, then the full run's remainder of base-proof
prefix="$(sed -n "${bc_l},${dump_l}p" "$RUNNER")"
for need in '^setup$' "^A=\"\$(new_user 'Member A'" "^B=\"\$(new_user 'Member B'" '^DB_DUMP="\$WORK/base.dump"$' '^PUBLIC_PORT=' '^SMTP_PORT=' '^seed_push_jobs$' 'pg_dump -U church -Fc fold >"\$DB_DUMP"'; do
  grep -q -- "$need" <<<"$prefix" || { echo "the diagnostic's prerequisites lack $need" >&2; exit 1; }
done
grep -q '^JAR=' "$RUNNER"; grep -q '^URL=' "$RUNNER"
# the full run's remainder of base-proof, and only it, is inside the wrap: it ends with the fi just before base-proof's drill_case_end
fiA_l="$(awk -v s="$wrap_l" 'NR > s && /^fi$/ {print NR; exit}' "$RUNNER")"
[[ "$(sed -n "$((fiA_l + 1))p" "$RUNNER")" == drill_case_end ]]
sed -n "${wrap_l},${fiA_l}p" "$RUNNER" | grep '^restore --dump "\$DB_DUMP"$' >/dev/null
sed -n "${wrap_l},${fiA_l}p" "$RUNNER" | grep 'private-browser.cjs' >/dev/null
sed -n "${wrap_l},${fiA_l}p" "$RUNNER" | grep '^drill_step closed-observation-60s$' >/dev/null
# reset_case and kill_point are defined before the diagnostic's call; the call is guarded, bare, and before every full-only case
rs_l="$(grep -n '^reset_case() {$' "$RUNNER" | cut -d: -f1)"; kp_l="$(grep -n '^kill_point() {$' "$RUNNER" | cut -d: -f1)"
call_l="$(grep -n '^crossings_only_run$' "$RUNNER" | cut -d: -f1)"
[[ "$(grep -c '^crossings_only_run$' "$RUNNER")" == 1 ]]
[[ "$(sed -n "$((call_l - 1))p" "$RUNNER")" == 'if [[ "$MODE" != full ]]; then' && "$(sed -n "$((call_l + 1))p" "$RUNNER")" == 'fi' ]]
alter_l="$(grep -n "ALTER SYSTEM SET log_statement='all'" "$RUNNER" | cut -d: -f1)"
ri_l="$(grep -n '^drill_case_begin recovery-interruptions$' "$RUNNER" | cut -d: -f1)"; loop_l="$(grep -n '^for drill_family in' "$RUNNER" | cut -d: -f1)"
fv_l="$(grep -n '^drill_case_begin final-verification$' "$RUNNER" | cut -d: -f1)"
[[ "$fiA_l" -lt "$rs_l" && "$rs_l" -lt "$kp_l" && "$kp_l" -lt "$call_l" && "$call_l" -lt "$alter_l" && "$alter_l" -lt "$ri_l" && "$ri_l" -lt "$loop_l" && "$loop_l" -lt "$fv_l" ]]
# the full flow after the call is untouched by the mode: no MODE test between the call and the end of the script
if sed -n "$((call_l + 2)),\$p" "$RUNNER" | grep -n 'MODE'; then echo 'the full-only part of the runner tests the mode' >&2; exit 1; fi
# the full run's end is exactly what it was
sed -n "$fv_l,$((fv_l + 7))p" "$RUNNER" | grep '^assert_statement_history$' >/dev/null
sed -n "$fv_l,\$p" "$RUNNER" | grep '^assert_crossing_evidence$' >/dev/null
sed -n "$fv_l,\$p" "$RUNNER" | grep '^drill_progress_complete$' >/dev/null
sed -n "$fv_l,\$p" "$RUNNER" | grep '^printf .restore drill passed: revision=%s project=%s artifacts=%s\\n.' >/dev/null

# --- cleanup: the diagnostic uses the full run's cleanup, with its exit code kept honest ------------------------------------------------------------------
body="$(sed -n '/^cleanup() {$/,/^}$/p' "$RUNNER")"
ord() { grep -n -- "$1" <<<"$body" | head -n 1 | cut -d: -f1; }
a="$(ord 'drill_partial_rc')"; b="$(ord 'crossing_cleanup_all')"; d="$(ord 'down -v --remove-orphans')"; e="$(ord 'drill_emit result')"; f="$(ord 'restore drill result=')"; g="$(ord 'drill_mode_finish')"
[[ -n "$a$b$d$e$f$g" && "$a" -lt "$b" && "$b" -lt "$d" && "$d" -lt "$e" && "$e" -lt "$f" && "$f" -lt "$g" ]]
grep -q '\[\[ "\$MODE" == full \]\] || rc="\$(drill_partial_rc "\$rc")"' <<<"$body"; grep -q '\[\[ "\$MODE" == full \]\] || drill_mode_finish "\$rc"' <<<"$body"
grep -qF 'echo "restore drill result=$rc artifacts=$WORK retained_stack=$KEEP"' <<<"$body"   # the full run's last line, verbatim
# drill_partial_rc: 10 only when every case ended; never 0
reset_plan() { DRILL_PROGRESS_PLAN='["a","b"]'; DRILL_PROGRESS_DONE="$1"; RESTORE_DRILL_CASE=""; }
reset_plan '["a","b"]'; [[ "$(drill_partial_rc 10)" == 10 && "$(drill_partial_rc 0)" == 1 && "$(drill_partial_rc 3)" == 3 && "$(drill_partial_rc 137)" == 137 ]]
reset_plan '["a"]'; [[ "$(drill_partial_rc 10)" == 1 ]]   # a failing command that happened to exit 10
reset_plan '["a","b"]'; RESTORE_DRILL_CASE=b; [[ "$(drill_partial_rc 10)" == 1 ]]

# --- behaviour: the diagnostic against stub cases, in a subshell that mimics the runner's EXIT trap --------------------------------------------------------
KEY_FULL='at case caseSeconds completedCases elapsedSeconds event exitCode operation remainingCases status step stepIndex stepSeconds stepTotal'
# The harness runs as its own bash, NOT in a subshell of this script: a subshell on the left of `||` runs with errexit off, and a failing case would not stop the run.
cat >"$WORK/diag-harness.sh" <<'HARNESS'
set -Eeuo pipefail
HERE="$1"; w="$2"; fail_at="$3"; fail_rc="$4"
# shellcheck source=/dev/null
source "$HERE/checks.sh"; source "$HERE/isolation.sh"; source "$HERE/restore-drill-cases.sh"; source "$HERE/restore-drill-crossings.sh"
WORK="$w"; MANIFEST="$w/manifest.json"; PROJECT="fold-deploy-drill-x"; REVISION="abc123"; DRILL_PROGRESS_MODE="crossings-only"
jq -n '{revision:"abc123"}' >"$MANIFEST"
LOG="$w/calls.log"
for fam in "${DRILL_FULL_PLAN_NAMES[@]}" count_races; do
  [[ "$fam" =~ ^[a-z_]+$ ]] || continue
  eval "$fam() { echo UNEXPECTED-$fam >>\"$LOG\"; }"
done
for fam in "${CROSSINGS_ONLY_FAMILIES[@]}"; do
  eval "$fam() { echo $fam >>\"$LOG\"; if [[ \"$fail_at\" == $fam ]]; then return $fail_rc; fi; }"
done
assert_crossing_evidence() { echo assert_crossing_evidence >>"$LOG"; }
# the runner's cleanup in miniature: the same calls, in the same order
on_exit() { local rc=$?; trap - EXIT; rc="$(drill_partial_rc "$rc")"; drill_emit result "$rc" || rc=1; drill_mode_finish "$rc"; exit "$rc"; }
trap on_exit EXIT
drill_progress_init "${CROSSINGS_ONLY_PLAN[@]}"
drill_mode_begin
drill_case_begin bootstrap; drill_case_end
drill_case_begin base-prerequisites; drill_case_end
crossings_only_run
HARNESS
run_diag() { # fail-at-family exit-code-of-that-stub -> prints the work directory; the harness's exit status is in <dir>/rc
  local w="$WORK/diag.$RANDOM"
  mkdir -p "$w"
  bash "$WORK/diag-harness.sh" "$HERE" "$w" "${1:-}" "${2:-1}" >"$w/stdout.log" 2>"$w/stderr.log" || echo "$?" >"$w/rc"
  [[ -e "$w/rc" ]] || echo 0 >"$w/rc"
  echo "$w"
}
# (1) a pass: exit 10, status partial, every record labelled, the families in order and nothing else, the evidence check last
w="$(run_diag)"
[[ "$(cat "$w/rc")" == 10 ]] || { echo "the diagnostic passed with exit $(cat "$w/rc")" >&2; cat "$w/stdout.log" "$w/stderr.log" >&2; exit 1; }
[[ "$(paste -sd' ' "$w/calls.log")" == 'real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore assert_crossing_evidence' ]]
if grep -q UNEXPECTED "$w/calls.log"; then echo 'a full-run case ran in the diagnostic' >&2; exit 1; fi
jq -e -s 'all(.[]; .mode == "crossings-only" and .partial == true and .fullAcceptance == false)' "$w/case-progress.jsonl" >/dev/null
last="$(tail -n 1 "$w/case-progress.jsonl")"
jq -e '.event == "result" and .status == "partial" and .exitCode == 10 and (.completedCases | length) == 8' <<<"$last" >/dev/null
if jq -e -s 'any(.[]; .status == "complete")' "$w/case-progress.jsonl" >/dev/null; then echo 'the diagnostic reported "complete"' >&2; exit 1; fi
jq -e '.event == "result" and .status == "partial"' "$w/case-progress.current.json" >/dev/null
jq -e '.completedCases == ["bootstrap","base-prerequisites","real_binary_refusals","real_level_lineage","real_same_level_rollback","real_current_release_restore","real_older_archive_restore","crossings-evidence"]' <<<"$last" >/dev/null
# the markers
[[ -s "$w/PARTIAL-NOT-FULL-ACCEPTANCE.txt" ]]; grep -q 'NOT full-drill evidence and NOT acceptance' "$w/PARTIAL-NOT-FULL-ACCEPTANCE.txt"; grep -q 'exit 10' "$w/PARTIAL-NOT-FULL-ACCEPTANCE.txt"
jq -e --argjson pass "$DRILL_PARTIAL_PASS_EXIT" '.mode == "crossings-only" and .partial == true and .fullAcceptance == false and .passExitCode == $pass and .result == 10 and .outcome == "partial-pass"
  and (.ran | index("real_level_lineage") != null) and (.ran | index("final-verification") == null)
  and (.skippedFromFull | index("final-verification") != null) and (.skippedFromFull | index("lifecycle_cases") != null) and (.skippedFromFull | index("recovery-interruptions") != null)
  and (.skippedFromFull | index("bootstrap") == null) and (.skippedFromFull | index("real_binary_refusals") == null)
  and (.skippedFromBaseProof | length) > 0 and (.notProved | map(test("full drill passes")) | any)
  and .browser.chromiumPreflight == "run" and (.browser.firefox | test("not prepared"))' "$w/drill-mode.json" >/dev/null
# skipped + ran covers the whole full plan (base-proof appears as its own prerequisites case)
[[ "$(jq -r '(.ran + .skippedFromFull) | unique | length' "$w/drill-mode.json")" == "$(( 8 + ${#DRILL_FULL_PLAN_NAMES[@]} - 7 ))" ]]   # the 8 cases it runs plus every full case it does not (the full plan minus base-proof, bootstrap and the five families)
grep -q 'PARTIAL DIAGNOSTIC' "$w/stdout.log"; grep -q 'NOT the full drill, NOT acceptance (exit 10)' "$w/stdout.log"
grep -q 'MODE=crossings-only outcome=partial-pass exit=10: PARTIAL diagnostic, NOT full acceptance' "$w/stdout.log"
grep -q 'mode=crossings-only partial=true (NOT full acceptance)' "$w/stderr.log"
# (2) a case fails: the exit is that failure's, the later cases and the evidence check do not run, nothing says "partial" about it
w="$(run_diag real_level_lineage 1)"
[[ "$(cat "$w/rc")" == 1 ]] || { echo "rc=$(cat "$w/rc")"; cat "$w/stderr.log" | tail -5; exit 1; }; [[ "$(paste -sd" " "$w/calls.log")" == "real_binary_refusals real_level_lineage" ]]
jq -e '.outcome == "failed" and .result == 1' "$w/drill-mode.json" >/dev/null
jq -e -s 'last | .event == "result" and .status == "failed"' "$w/case-progress.jsonl" >/dev/null
# a case that fails with exactly the pass code is still a failure
w="$(run_diag real_same_level_rollback 10)"
[[ "$(cat "$w/rc")" == 1 ]]; jq -e '.outcome == "failed"' "$w/drill-mode.json" >/dev/null; jq -e -s 'last | .status == "failed"' "$w/case-progress.jsonl" >/dev/null
# (3) the full run's records are what they always were: no mode keys, "complete" only for exit 0, and 10 is not a pass
full_run() { # rc -> status of the result record
  local w="$WORK/full.$RANDOM"; mkdir -p "$w"
  ( set -Eeuo pipefail; WORK="$w"; unset DRILL_PROGRESS_MODE
    drill_progress_init a b; drill_case_begin a; drill_case_end; drill_case_begin b; drill_case_end
    drill_emit result "$1" ) >/dev/null 2>"$w/stderr.log"
  echo "$w"
}
w="$(full_run 0)"; jq -e '.status == "complete"' "$w/case-progress.current.json" >/dev/null
[[ "$(jq -r "keys | join(\" \")" "$w/case-progress.current.json")" == "$KEY_FULL" ]] || { jq -r "keys | join(\" \")" "$w/case-progress.current.json" >&2; exit 1; }; jq -e -s "all(.[]; has(\"mode\") | not)" "$w/case-progress.jsonl" >/dev/null
grep -q 'event=result operation=$' "$w/stderr.log"; if grep -q 'NOT full acceptance' "$w/stderr.log"; then echo 'the full run labels itself partial' >&2; exit 1; fi
w="$(full_run 10)"; jq -e '.status == "failed"' "$w/case-progress.current.json" >/dev/null   # the diagnostic's code is not a full pass
w="$(full_run 1)"; jq -e '.status == "failed"' "$w/case-progress.current.json" >/dev/null
# a diagnostic that somehow reports 0 is a failure too
( set -Eeuo pipefail; w="$WORK/z"; mkdir -p "$w"; WORK="$w"; DRILL_PROGRESS_MODE=crossings-only; drill_progress_init a; drill_case_begin a; drill_case_end; drill_emit result 0 ) >/dev/null 2>&1
jq -e '.status == "failed"' "$WORK/z/case-progress.current.json" >/dev/null
# the full run writes none of the diagnostic's files: drill_mode_begin and drill_mode_finish are never reached with MODE=full
[[ "$(grep -c 'drill_mode_begin\|drill_mode_finish' "$RUNNER")" == 2 ]]   # the two guarded calls
grep -n 'drill_mode_begin\|drill_mode_finish' "$RUNNER" | grep -v '\[\[ "\$MODE" == full \]\] ||' | grep -v '^[0-9]*:#' | grep . && { echo 'an unguarded mode call' >&2; exit 1; }

echo 'restore drill modes fixtures: ok'
