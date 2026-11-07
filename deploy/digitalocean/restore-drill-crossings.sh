#!/usr/bin/env bash
# R4-6c-V / T07B-2-V: real-binary recovery-level crossings, the current-release
# restore and the older-archive restores. Sourced only by restore-drill.sh (and
# by restore-crossings-fixtures.sh, which tests the pure functions without a
# daemon). Nothing here changes a shipped script: it builds real images from
# real commits, pushes them to the drill registry the runner already owns, and
# drives the installed entry points.
#
# Every image is built from `git archive <sha>` of a commit and must report the
# FOLD_VERSION it was built with, so a binary is tied to its commit. The
# candidate is the committed head (the runner refuses a dirty tree) and must be
# recovery level 2 (reason review, T07B-2b).
#
# Roles (each proved from git before any image is built):
#   candidate  the committed head, level 2.
#   landing    T07B-1-V's landing commit, level 1 (the restore barrier without
#              reason review), before restore.sh existed.
#   lane       LANE_BASE, the recorded level-0 preflight base.
#   prior      the level-2 release before the candidate (T07B-2b's landing, the
#              same schema), the only legal rollback target.
#   archive    INTEGRATION's first parent of T07B-2a-V's landing merge (level 1
#              or 0): migrations without the reason-review file, whose data is
#              restored by the candidate. The lane commit is also dumped
#              (the older table-set archive, kept from R4-6c-V).
#
# Cases, each failing the runner on any deviation:
#   real_binary_refusals        The main site (floor 2) runs the real candidate
#                               image. upgrade.sh --commit/--image and
#                               restore.sh --commit/--image toward LANE_BASE
#                               (level 0) and toward the landing (level 1), each
#                               from the installed copy and from the candidate
#                               tool copy, must exit 1 naming what the older
#                               release lacks (the restore barrier for level 0,
#                               reason review for both), reach no backup/record/
#                               checkout/pin/stop/swap step in an xtrace, and
#                               leave snapshot_state byte-identical. Two further
#                               rows per rollback kind and target are
#                               SUPPLEMENTARY FIXTURE rows (S-*): a same-level
#                               release.prev is rewritten to name the target.
#   real_level_lineage          Four fresh sites (origin level 0 and level 1, each
#                               in image and build mode), separate from the main
#                               one. Each is first a genuine host of its origin
#                               level: that commit's own deploy tools provision
#                               that commit's image (image mode) or source
#                               (build mode, compiled on the box). The candidate's
#                               tools then upgrade it for real. Smoke passes, the
#                               floor reads 2 and release.prev names the origin
#                               naturally. Then upgrade.sh --rollback, from the
#                               installed copy and from the candidate tool copy,
#                               must be refused as above (R-rollback-genuine).
#                               The build-mode sites, running the genuine build
#                               at the head, also take the full forward and
#                               restore refusal matrix toward both targets.
#   real_same_level_rollback    The real prior (level 2) image deployed, forward
#                               to the candidate image, then upgrade.sh
#                               --rollback: succeeds, runs restore.sh from
#                               DIR/recovery-tools, leaves DIR and
#                               DIR/deploy-tools at the candidate's bytes, and
#                               the recovery instance and the reopened app run
#                               one image id that reports recoveryPolicy 2.
#   real_current_release_restore  restore.sh --dump of a current dump with the
#                               candidate (level 2) completes into private
#                               recovery, and a reason-review panel meets the
#                               restore four ways. An OPEN panel holds no attempt
#                               id (the Delete press mints it): (1) after the
#                               restore the press applies the panel's choices at
#                               once when the hashes are unchanged; (2) when a
#                               hashed column changed it answers 409
#                               reason_review_changed with nothing blanked, the
#                               panel reloads its items and the press then
#                               applies exactly those choices. A RETAINED id
#                               (minted by a process that is gone): (3) after a
#                               pg_restore under the running app the press
#                               answers 503 restore_detected, the container
#                               restarts, the old id answers attempt_expired and
#                               settles history_unavailable, the users list is
#                               reloaded and still shows the account, and a fresh
#                               press applies; (4) the same across restore.sh
#                               --dump. Row fields name each answer. These are
#                               PROTOCOL checks made with curl against the real
#                               server and database. They do not prove the React
#                               panel: its disabled Delete, Close, list reload and
#                               choosing again are covered by Vitest, Playwright
#                               and the first-church hand check.
#   real_older_archive_restore  Two archives dumped on their real images and
#                               restored by the candidate: (lane-base) the table
#                               set D must contain admin_operation_attempts and
#                               every table in D is created once by its owner
#                               migration; (reason-review) the archive image lacks
#                               the reason-review migration, the candidate has it,
#                               the swapped-in heap still holds the archive's
#                               trigger functions, the recovery boot applies the
#                               exact missing migrations once (goose), and
#                               pg_get_functiondef of add_request_identity and
#                               add_request_completed then equals the bodies a
#                               scratch database migrated by the candidate has;
#                               recovery and --reopen succeed and the reopened
#                               app serves the reason-review page route.
#
# Recorded under $WORK/crossings: revisions.json, images.jsonl, tools.jsonl,
# cases.jsonl, cases/<id>/{cmd,trace,state.before,state.after}, table-sets/*,
# older-archive-*.json, lineage-*.json, current-release-restore.json.
# shellcheck disable=SC2034,SC2153,SC2154,SC2016
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then echo 'source from restore-drill.sh only' >&2; exit 1; fi

# LANE_BASE is origin/master's recorded tip (state preflight.base): a level-0
# release with migrations 00001-00004. It is both a refusal target and the base
# of the older table-set archive, so D holds every table added since.
CROSSING_PREFLIGHT_BASE=8c07c85265c0f43ab8350cd945efdc14363c21bd
CROSSING_LANE_DEFAULT="$CROSSING_PREFLIGHT_BASE"
# T07B-1-V's landing: "Integrate tracked deletion outcomes", level 1, no restore.sh.
CROSSING_LANDING_DEFAULT=d400b7755f4564c60524167105f31e33bed9b51b
# T07B-2b's landing: "Integrate reason-review API", level 2, the candidate's schema.
CROSSING_PRIOR_DEFAULT=6927e896065d172fb1214181cd80fbd554a86eeb
# ARCH_SHA: the first parent of T07B-2a-V's landing merge a1198e6 (level 0,
# migrations 00001-00006, no reason-review file).
CROSSING_ARCHIVE_DEFAULT=89899794046c4f2cd8a59f0fe6d0184a1d825afd
CROSSING_CANDIDATE_LEVEL=2
# The file name (after its number) of T07B-2a's migration. Its version is read
# from git, never hard-coded.
CROSSING_REASON_MIGRATION=reason_review_erasure_scrub
# The two trigger functions that migration replaces.
CROSSING_REASON_FUNCTIONS="'add_request_identity','add_request_completed'"
CROSSING_DOMAIN=fold-drill.localhost
# Steps that must never appear in a refused forward upgrade or rollback.
CROSSING_FORBID_UP='^\++ (run_backup|write_release|restore_dump|run_provision|newest_dump|current_release)( |$)|^\++ git .*checkout|pg_dump|pg_restore|backup\.sh --deploy-dir|^\++ exec .*restore\.sh|^\++ docker compose .* (stop|up|build|pull|down)( |$)'
# Steps that must never appear in a refused restore.sh. stage_tools alone is
# allowed: an image check runs in a temporary frozen copy that is deleted.
CROSSING_FORBID_RESTORE='^\++ (publish_tools|close_site|create_database|collect|pin|swap|recovery_start|record|store|finish_reopen)( |$)|pg_restore -U|^\++ docker compose .* (stop|up|build|down)( |$)'
CROSSING_CASE=0
CROSSING_PROJECTS=()
CROSSING_HOST_FILES=(/etc/cron.d/fold-health /etc/cron.d/fold-backup /etc/logrotate.d/fold-backup)
CROSSING_HOST_BEFORE=()

crossing_dir() { printf '%s/crossings' "$WORK"; }

# ---------------------------------------------------------------------------
# Pure functions. No daemon and no runner globals; restore-crossings-fixtures.sh
# exercises each of them.
# ---------------------------------------------------------------------------

# Migration file versions at a revision, as a sorted comma list ("1,2,3").
crossing_migration_versions() {
  local src="$1" rev="$2" listing
  listing="$(git -C "$src" ls-tree --name-only "$rev" -- migrations/)" || return 1
  sed -n 's|^migrations/0*\([0-9][0-9]*\)_.*\.sql$|\1|p' <<<"$listing" | sort -n | paste -sd, -
}

# Versions a newer revision has and an older one lacks, as a comma list. Fails
# unless the older set is wholly inside the newer one (a schema that only grew).
crossing_migration_difference() { # src older newer
  local src="$1" older="$2" newer="$3" o n lost
  o="$(crossing_migration_versions "$src" "$older")" || return 1
  n="$(crossing_migration_versions "$src" "$newer")" || return 1
  lost="$(LC_ALL=C comm -23 <(tr ',' '\n' <<<"$o" | LC_ALL=C sort) <(tr ',' '\n' <<<"$n" | LC_ALL=C sort))"
  [[ -z "$lost" ]] || { echo "crossing: $older has migrations $newer lacks: $(paste -sd, <<<"$lost")" >&2; return 1; }
  LC_ALL=C comm -13 <(tr ',' '\n' <<<"$o" | LC_ALL=C sort) <(tr ',' '\n' <<<"$n" | LC_ALL=C sort) | sort -n | paste -sd, -
}

# The version of the one migration named NNNNN_reason_review_erasure_scrub.sql
# at a revision. Fails (printing nothing) unless there is exactly one.
crossing_reason_migration_version() { # src rev
  local src="$1" rev="$2" listing hits
  listing="$(git -C "$src" ls-tree --name-only "$rev" -- migrations/)" || return 1
  hits="$(grep -E "^migrations/[0-9]{5}_${CROSSING_REASON_MIGRATION}\.sql$" <<<"$listing" || true)"
  [[ -n "$hits" && "$hits" != *$'\n'* ]] || return 1
  sed -E 's|^migrations/0*([0-9]+)_.*$|\1|' <<<"$hits"
}

# The landing head is a commit, not a working tree: nothing uncommitted may
# stand behind a binary the drill calls the exact head.
crossing_require_clean_head() {
  local src="$1" dirty
  dirty="$(git --no-optional-locks -C "$src" status --porcelain)" || return 1
  if [[ -n "$dirty" ]]; then
    echo "crossing: $src has uncommitted or untracked paths; commit the landing head first (the candidate binary is built from git archive of HEAD)" >&2
    return 1
  fi
}

# Revisions are inputs; every property the acceptance relies on is checked from
# the commits themselves before any image is built. Prints every problem.
crossing_validate_revisions() {
  local src="$1" lane="$2" landing="$3" prior="$4" archive="$5" head="$6"
  local role sha level bad=0 hv pv rv diff
  for role in lane landing prior archive; do
    case "$role" in lane) sha="$lane" ;; landing) sha="$landing" ;; prior) sha="$prior" ;; *) sha="$archive" ;; esac
    if [[ ! "$sha" =~ ^[0-9a-f]{40}$ ]]; then
      echo "crossing: the $role revision must be a full 40-hex SHA" >&2; bad=1; continue
    fi
    if ! git -C "$src" cat-file -e "${sha}^{commit}" 2>/dev/null; then
      echo "crossing: $role revision $sha is not in $src; provide the complete read-only Git metadata" >&2; bad=1; continue
    fi
    if ! git -C "$src" merge-base --is-ancestor "$sha" "$head" 2>/dev/null; then
      echo "crossing: $role revision $sha is not an ancestor of the candidate $head" >&2; bad=1
    fi
  done
  (( bad == 0 )) || return 1
  level="$(commit_recovery_level "$src" "$head")" || return 1
  if [[ "$level" != "$CROSSING_CANDIDATE_LEVEL" ]]; then echo "crossing: the candidate $head is recovery level $level, not $CROSSING_CANDIDATE_LEVEL" >&2; bad=1; fi
  level="$(commit_recovery_level "$src" "$lane")" || return 1
  if [[ "$level" != 0 ]]; then echo "crossing: LANE_BASE $lane is recovery level $level, not a level-0 release" >&2; bad=1; fi
  level="$(commit_recovery_level "$src" "$landing")" || return 1
  if [[ "$level" != 1 ]]; then echo "crossing: landing $landing is recovery level $level, not level 1" >&2; bad=1; fi
  level="$(commit_recovery_level "$src" "$prior")" || return 1
  if [[ "$level" != 2 ]]; then echo "crossing: prior $prior is recovery level $level, not level 2" >&2; bad=1; fi
  level="$(commit_recovery_level "$src" "$archive")" || return 1
  if [[ "$level" != 0 && "$level" != 1 ]]; then echo "crossing: archive base $archive is recovery level $level, not level 0 or 1" >&2; bad=1; fi
  if git -C "$src" cat-file -e "$landing:deploy/digitalocean/restore.sh" 2>/dev/null; then
    echo "crossing: landing $landing already has restore.sh; it must precede the restore tooling" >&2; bad=1
  fi
  if [[ "$prior" == "$head" ]]; then
    echo "crossing: prior $prior is the candidate itself; a same-level rollback needs an earlier level-2 release" >&2; bad=1
  fi
  if git -C "$src" grep -q -e admin_operation_attempts "$lane" -- migrations/ 2>/dev/null; then
    echo "crossing: LANE_BASE $lane already has admin_operation_attempts; the table difference D would be empty" >&2; bad=1
  fi
  # The rollback target must have the candidate's schema, or "rollback" would
  # be a restore across schemas, which this case does not claim.
  pv="$(crossing_migration_versions "$src" "$prior")" || return 1
  hv="$(crossing_migration_versions "$src" "$head")" || return 1
  if [[ "$pv" != "$hv" ]]; then
    echo "crossing: prior migrations ($pv) differ from the candidate's ($hv); a same-schema rollback is impossible" >&2; bad=1
  fi
  # The archive must lack T07B-2a's migration and the candidate must have it,
  # with the archive's migrations wholly inside the candidate's.
  rv="$(crossing_reason_migration_version "$src" "$head")" || rv=""
  if [[ -z "$rv" ]]; then
    echo "crossing: the candidate $head has no single migrations/NNNNN_${CROSSING_REASON_MIGRATION}.sql" >&2; bad=1
  fi
  if crossing_reason_migration_version "$src" "$archive" >/dev/null; then
    echo "crossing: archive base $archive already has the ${CROSSING_REASON_MIGRATION} migration; the archive would not need it" >&2; bad=1
  fi
  if [[ -n "$rv" ]]; then
    if ! diff="$(crossing_migration_difference "$src" "$archive" "$head")"; then
      bad=1
    elif ! grep -Eq "(^|,)${rv}(,|$)" <<<"$diff"; then
      echo "crossing: the migrations the candidate adds to the archive ($diff) do not include ${rv}" >&2; bad=1
    fi
  fi
  (( bad == 0 ))
}

# What require_recovery_level says an older release lacks, for a target and a
# host floor (checks.sh words it; this restates the words the acceptance needs).
crossing_missing_text() { # target floor
  local target="$1" floor="$2" missing=""
  if (( target < 1 && floor >= 1 )); then missing="the restore barrier (level 1)"; fi
  if (( target < 2 && floor >= 2 )); then missing="${missing:+$missing and }reason review (level 2)"; fi
  printf '%s' "$missing"
}

# The ERE the first `check:` line of a refusal must match.
crossing_refusal_re() { # target floor
  local text
  text="$(crossing_missing_text "$1" "$2" | sed 's/[][\.*^$()+?{|]/\\&/g')"
  printf '^check: target recovery level %s is below host level %s\\. The older release lacks %s\\.' "$1" "$2" "$text"
}

# public tables named by a pg_restore -l listing ("TABLE DATA" entries excluded).
crossing_toc_tables() { awk '$4=="TABLE" && $5=="public" {print $6}' | LC_ALL=C sort -u; }

# D: tables the candidate has and the archive lacks. With a third argument,
# fails unless D contains that table. Other members are recorded by the caller,
# not assumed. D may be empty (an archive that differs only in trigger bodies).
crossing_table_difference() {
  local archive="$1" candidate="$2" require="${3:-}" d
  d="$(LC_ALL=C comm -13 "$archive" "$candidate")" || return 1
  if [[ -n "$require" ]] && ! grep -qx "$require" <<<"$d"; then
    echo "crossing: the table difference D lacks $require" >&2
    return 1
  fi
  [[ -z "$d" ]] || printf '%s\n' "$d"
}

# The one migration (not the baseline) that creates a table, as its integer version.
crossing_owner_migration() {
  local src="$1" rev="$2" table="$3" hits line file="" n=0 version
  [[ "$table" =~ ^[a-z_][a-z0-9_]*$ ]] || return 1
  hits="$(git -C "$src" grep -lE "^[[:space:]]*CREATE TABLE( IF NOT EXISTS)?[[:space:]]+(public\.)?\"?${table}\"?([[:space:]]|\()" "$rev" -- 'migrations/*.sql' || true)"
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    file="${line#*:}"; n=$((n+1))
  done <<<"$hits"
  [[ "$n" == 1 && "$file" =~ ^migrations/([0-9]{5})_.*\.sql$ ]] || return 1
  version=$((10#${BASH_REMATCH[1]}))
  (( version > 1 )) || return 1
  printf '%s\n' "$version"
}


# Problems with one refused invocation, one per line; nothing when it behaved.
# kind: forward-commit, forward-image, rollback-commit, rollback-image,
# restore-commit or restore-image. target and floor are the recovery levels of
# the release asked for and of the host: the refusal must come from
# require_recovery_level <target> <floor> and name what the target lacks.
# Extra arguments name provisioner paths that must never appear in the trace
# (a checkout's provision.sh is data, never a tool).
crossing_problems() {
  local kind="$1" target="$2" floor="$3" rc="$4" trace="$5" before="$6" after="$7" path; shift 7
  if [[ "$rc" != 1 ]]; then echo "exit code $rc, want 1"; fi
  if ! grep -Eq "$(crossing_refusal_re "$target" "$floor")" "$trace"; then echo "refusal message naming $(crossing_missing_text "$target" "$floor") is missing"; fi
  if ! cmp -s "$before" "$after"; then echo 'snapshot_state changed'; fi
  if ! grep -Eq "^\\++ require_recovery_level $target $floor( |\$)" "$trace"; then echo "refusal did not come from require_recovery_level $target $floor"; fi
  case "$kind" in
    forward-commit|rollback-commit)
      if ! grep -Eq '^\++ check_target_level build ' "$trace"; then echo 'refusal did not come from check_target_level build'; fi ;;
    forward-image|rollback-image)
      if ! grep -Eq '^\++ check_target_level image ' "$trace"; then echo 'refusal did not come from check_target_level image'; fi ;;
    restore-commit|restore-image) ;;
    *) echo "internal: unknown kind $kind" ;;
  esac
  case "$kind" in
    restore-*)
      if grep -Eq "$CROSSING_FORBID_RESTORE" "$trace"; then echo 'trace reached stop, collect, pin, swap or recovery work'; fi ;;
    *)
      if grep -Eq "$CROSSING_FORBID_UP" "$trace"; then echo 'trace reached backup, record, checkout, pin or restore work'; fi ;;
  esac
  for path in "$@"; do
    if grep -Fq -- "$path" "$trace"; then echo "trace executed $path"; fi
  done
  return 0
}


# First line number of a trace whose command (leading `+` markers removed)
# starts with, or equals, the given text. Fixed-string comparison, so a path
# never has to be escaped.
crossing_trace_line() { # mode(starts|equals) text trace
  awk -v mode="$1" -v want="$2" '{
    line = $0; sub(/^\++ /, "", line)
    if ((mode == "equals" && line == want) || (mode == "starts" && index(line, want) == 1)) { print NR; exit }
  }' "$3"
}

# Which tool set a dump restore ran from. `restore.sh --dump` does not re-exec
# DIR/recovery-tools/restore.sh: it execs a staged copy (DIR/.recovery-tools.X),
# which publishes itself by renaming that directory to DIR/recovery-tools and
# keeps running; every later tool call (pin, smoke) goes through the published
# path. So the proof is: the staged copy ran, the very directory that ran was
# renamed to DIR/recovery-tools, the pin and the smoke came from there, in that
# order, and nothing ran from deploy-tools or a checkout afterwards. Extra
# arguments are directories no command may be run from (a checkout's tools).
crossing_recovery_tools_problems() {
  local trace="$1" dir="$2" exec_line text token mv_line pin_line smoke_line late path; shift 2
  exec_line="$(crossing_trace_line starts "exec $dir/.recovery-tools." "$trace")"
  if [[ -z "$exec_line" ]]; then
    echo "no staged recovery-tools copy was exec'd from $dir/.recovery-tools.*"
    return 0
  fi
  text="$(sed -n "${exec_line}p" "$trace")"
  token="${text#*/.recovery-tools.}"; token="${token%%/*}"
  if [[ ! "$token" =~ ^[A-Za-z0-9]+$ || "$text" != *"/.recovery-tools.$token/restore.sh "* ]]; then
    echo "the staged exec is not DIR/.recovery-tools.<id>/restore.sh: $text"
    return 0
  fi
  mv_line="$(crossing_trace_line equals "mv $dir/.recovery-tools.$token $dir/recovery-tools" "$trace")"
  if [[ -z "$mv_line" || "$mv_line" -lt "$exec_line" ]]; then
    echo "the staged copy that ran (.recovery-tools.$token) was not published as $dir/recovery-tools"
    return 0
  fi
  pin_line="$(crossing_trace_line starts "$dir/recovery-tools/provision.sh --pin-only " "$trace")"
  if [[ -z "$pin_line" || "$pin_line" -lt "$mv_line" ]]; then echo "the pin did not run from the published $dir/recovery-tools/provision.sh"; fi
  smoke_line="$(crossing_trace_line starts "$dir/recovery-tools/smoke.sh --deploy-dir " "$trace")"
  if [[ -z "$smoke_line" || "$smoke_line" -lt "$mv_line" ]]; then echo "the smoke did not run from the published $dir/recovery-tools/smoke.sh"; fi
  late="$(awk -v start="$exec_line" -v d="$dir/deploy-tools/" '
    NR > start { line = $0; sub(/^\++ /, "", line); sub(/^exec /, "", line); if (index(line, d) == 1) { print NR; exit } }' "$trace")"
  if [[ -n "$late" ]]; then echo "a command ran from $dir/deploy-tools after the hand-off to the staged copy (trace line $late)"; fi
  for path in "$@"; do
    late="$(awk -v d="$path" '{ line = $0; sub(/^\++ /, "", line); sub(/^exec /, "", line); if (index(line, d) == 1) { print NR; exit } }' "$trace")"
    if [[ -n "$late" ]]; then echo "a command ran from $path (trace line $late)"; fi
  done
  return 0
}

# Complete, all-passing evidence. cases.jsonl and images.jsonl are inputs.
# Acceptance rows (R-, A-) are never fixtures; S- rows always are. A refusal
# row carries its site, the target level it asked for ("level0" or "level1")
# and the mode it was deployed in; the matrices (forward, restore) run on the
# main site and on both build-mode lineage sites toward both targets, each with
# the installed and the candidate tool copy.
crossing_evidence_complete() {
  local cases="$1" images="$2"
  [[ -s "$cases" && -s "$images" ]] || { echo 'crossing: evidence files are missing or empty' >&2; return 1; }
  jq -s -e '
    def count($a): [.[] | select(.acceptance == $a)] | length;
    def passed($a): [.[] | select(.acceptance == $a and .pass == true)] | length;
    def matrix($a): [.[] | select(.acceptance == $a)] | group_by([.site, .target]) | map(length) | all(. == 2);
    def want: {"R-forward-commit": 12, "R-forward-image": 12, "R-restore-commit": 12, "R-restore-image": 12,
               "R-rollback-genuine": 8, "S-rollback-commit-record": 4, "S-rollback-image-record": 4,
               "A-lineage-upgrade": 4, "A-same-level-rollback": 1, "A-current-release-restore": 1, "A-older-archive": 2};
    . as $all
    | (want | to_entries | all(.key as $k | .value as $n | ($all | count($k)) == $n and ($all | passed($k)) == $n))
    and ([.[] | .acceptance] | all(. as $a | want | has($a)))
    and ([.[] | select(.kind == "refusal") | .deployed] | unique) == ["build", "image"]
    and ([.[] | select(.kind == "refusal") | .exitCode] | all(. == 1))
    and ([.[] | select(.acceptance | startswith("S-")) | .fixture] | all(. == true))
    and ([.[] | select(.acceptance | startswith("R-") or startswith("A-")) | .fixture // false] | all(. == false))
    and ([.[] | select(.acceptance == "R-rollback-genuine") | .deployed] | unique) == ["build", "image"]
    and ([.[] | select(.acceptance == "R-rollback-genuine") | .site] | unique) == ["lineage-l0-build", "lineage-l0-image", "lineage-l1-build", "lineage-l1-image"]
    and ([.[] | select(.acceptance == "R-rollback-genuine") | [.site, .target]] | unique) == [["lineage-l0-build","level0"], ["lineage-l0-image","level0"], ["lineage-l1-build","level1"], ["lineage-l1-image","level1"]]
    and ([.[] | select(.acceptance == "R-forward-commit") | .site] | unique) == ["lineage-l0-build", "lineage-l1-build", "main"]
    and ([.[] | select(.kind == "refusal" and (.acceptance | startswith("R-forward") or startswith("R-restore"))) | .target] | unique) == ["level0", "level1"]
    and ([.[] | select(.acceptance | startswith("S-")) | .target] | unique) == ["level0", "level1"]
    and matrix("R-forward-commit") and matrix("R-forward-image") and matrix("R-restore-commit") and matrix("R-restore-image")
    and ([.[] | select(.acceptance == "A-lineage-upgrade") | [.mode, .origin]] | sort) == [["build",0], ["build",1], ["image",0], ["image",1]]
    and ([.[] | select(.acceptance == "A-lineage-upgrade") | .floor] | all(. == "2"))
    and ([.[] | select(.acceptance == "A-older-archive") | .archive.id] | sort) == ["lane-base", "reason-review"]
    and ([.[] | select(.acceptance == "A-same-level-rollback") | .reopenedVersion.recoveryPolicy] | all(. == 2))
    and ([.[] | select(.acceptance == "A-current-release-restore") | .level] | all(. == 2))
    and ([.[] | select(.acceptance == "A-current-release-restore") | .scenarios] | all(
          .noAttempt == {unchanged:"applied", changed:"reason_review_changed", reloadedPress:"applied"}
          and .retainedLive == {answer:"restore_detected", replay:"attempt_expired", settle:"history_unavailable", reloadShowsAccount:true, freshPress:"applied"}
          and .retainedRestoreSh == {replay:"attempt_expired", settle:"history_unavailable", reloadShowsAccount:true, freshPress:"applied"}))
    and ([.[] | select(.acceptance == "A-current-release-restore") | (.restartsAfter > .restartsBefore)] | all(. == true))
    and ([.[] | select(.acceptance == "A-current-release-restore") | (.proof | startswith("protocol:") and contains("not by this case"))] | all(. == true))
  ' "$cases" >/dev/null || { echo 'crossing: refusal/positive cases are incomplete or failed' >&2; return 1; }
  jq -s -e '
    ([.[] | .role] | sort) == ["archive", "candidate", "landing", "lane", "prior"]
    and (map(select(.role == "candidate" or .role == "prior")) | all(.level == 2))
    and (map(select(.role == "landing")) | all(.level == 1))
    and (map(select(.role == "lane")) | all(.level == 0))
    and (map(select(.role == "archive")) | all(.level == 0 or .level == 1))
    and all(.[]; (.imageId | test("^sha256:[0-9a-f]{64}$")) and (.sha | test("^[0-9a-f]{40}$")))
  ' "$images" >/dev/null || { echo 'crossing: image evidence is incomplete' >&2; return 1; }
}

# ---------------------------------------------------------------------------
# Runner pieces. These use the runner's globals (ROOT, WORK, DIR, SOURCE,
# REGISTRY, TOOLS_DIR) and helpers (dc, pg, restore, snapshot_state, ...).
# CX_* name the site a helper is acting on, so the main site's DIR/SOURCE are
# never rebound (the runner's cleanup still tears them down).
# ---------------------------------------------------------------------------

crossing_emit() { local file="$1"; shift; jq -nc "$@" >>"$(crossing_dir)/$file"; }
crossing_cdc() { docker compose --project-directory "$CX_DIR" "$@"; }
crossing_cpg() { crossing_cdc exec -T db psql -X -U church -d "$1" -At -v ON_ERROR_STOP=1 "${@:2}"; }

crossing_use_main() {
  CX_SITE=main; CX_DIR="$DIR"; CX_SOURCE="$SOURCE"; CX_TOOLS="$TOOLS_DIR"; CX_DUMP="$DB_DUMP"; CX_FIXTURE=false
}

# The two refusal targets, by name: level0 is LANE_BASE, level1 the landing.
crossing_target_commit() { case "$1" in level0) printf '%s' "$LANE_REVISION" ;; level1) printf '%s' "$LANDING_REVISION" ;; *) return 1 ;; esac; }
crossing_target_image() { case "$1" in level0) printf '%s' "$CROSSING_LANE_IMG" ;; level1) printf '%s' "$CROSSING_LANDING_IMG" ;; *) return 1 ;; esac; }
crossing_target_level() { case "$1" in level0) printf 0 ;; level1) printf 1 ;; *) return 1 ;; esac; }

# One explicit fixture row: a denied group_add request with a reason, the
# shape migrations/reason_review_test.go already proves against the schema.
# The accounts, the review pages and the deletions all use the real routes.
crossing_reason_row_sql() { # requester subject group reason
  local q="'" reason; reason="${4//$q/$q$q}"
  printf "INSERT INTO contact_requests(requester_id,subject_user_id,context_type,context_id,fields,reason,needs_admin,subject_decision,subject_decided_by,subject_decided_at,admin_decision,status,created_at,expires_at,completed_at,purpose) VALUES(%d,%d,'group',%d,'','%s',true,'denied',%d,'2026-10-25 12:00Z','pending','denied','2026-10-24 12:00Z','2026-11-07 12:00Z','2026-10-25 12:00Z','group_add')" \
    "$1" "$2" "$3" "$reason" "$2"
}

# The bodies of the two trigger functions T07B-2a replaces, as text, for
# comparing one database with another.
crossing_function_defs() { # database -> prints the definitions, ordered
  pg "$1" -c "SELECT pg_get_functiondef(oid) FROM pg_proc WHERE pronamespace='public'::regnamespace AND proname IN ($CROSSING_REASON_FUNCTIONS) ORDER BY proname"
}

# The nested runner is Alpine, and provision.sh's check_os (every revision's copy) refuses it without --allow-os, which upgrade.sh never forwards. The
# container presents a Ubuntu 24.04 /etc/os-release for the real_* cases (isolation_os_standin, container-local, saved and restored at exit, only after the
# isolation guard). Before any image is built, EACH revision's own check_os is run against it, so a provisioner that would still refuse stops the run
# now. The stand-in changes only what check_os sees: the recovery-level refusals under test come from require_recovery_level / check_target_level, which the
# trace assertions name, and run before any provisioning. It is recorded as a deviation in the evidence.
crossing_os_standin() { # crossing-dir [os-release-path] [mountinfo]; the last two are for fixtures
  local cdir="$1" osfile="${2:-/etc/os-release}" mountinfo="${3:-/proc/self/mountinfo}" rev tmp
  CROSSING_OS_STATE="$cdir/os-release-standin.json"
  isolation_os_standin "$CROSSING_OS_STATE" "$osfile" "$mountinfo" || return 1
  tmp="$(mktemp)"
  for rev in "$REVISION" "$LANE_REVISION" "$LANDING_REVISION" "$PRIOR_REVISION" "$ARCHIVE_BASE_REVISION"; do
    git -C "$ROOT" show "$rev:deploy/digitalocean/provision.sh" >"$tmp" || { rm -f "$tmp"; return 1; }
    isolation_check_os_accepts "$tmp" "$osfile" || { rm -f "$tmp"; echo "crossing: the provisioner of $rev would still refuse this container's OS ($(isolation_os_identity "$osfile"))" >&2; return 1; }
    crossing_emit tools.jsonl --arg rev "$rev" '{label:"environment", file:"provision.sh", revision:$rev, checkOsAcceptsStandIn:true}'
  done
  rm -f "$tmp"
  crossing_emit tools.jsonl --argjson applied "$(jq '.applied' "$CROSSING_OS_STATE")" \
    '{label:"environment", file:"/etc/os-release", replacedByDrill:$applied, standIn:"container-local Ubuntu 24.04, saved first and restored at exit"}'
  if [[ "$(jq -r '.applied' "$CROSSING_OS_STATE")" == true ]]; then
    printf '%s\n' 'DEVIATION real_* cases: this disposable Alpine container presents a container-local Ubuntu 24.04 /etc/os-release (restored at exit; see crossings/os-release-standin.json) because provision.sh check_os refuses Alpine and upgrade.sh never forwards --allow-os. No script is changed, no --allow-os is used for it, and nothing outside the container is touched.' >>"$WORK/deviations.txt"
  fi
}

# Everything expensive or global happens once, in the first crossing case.
crossing_prepare() {
  local cdir f archive_level
  [[ -z "${CROSSING_READY:-}" ]] || return 0
  cdir="$(crossing_dir)"
  mkdir -p "$cdir/cases" "$cdir/table-sets" "$cdir/sites"
  : >"$cdir/cases.jsonl"; : >"$cdir/images.jsonl"; : >"$cdir/tools.jsonl"
  TOOLS_DIR="$WORK/tools"
  [[ -f "$TOOLS_DIR/upgrade.sh" && -f "$TOOLS_DIR/restore.sh" ]] || { echo "crossing: candidate tool copy missing at $TOOLS_DIR" >&2; return 1; }
  crossing_validate_revisions "$ROOT" "$LANE_REVISION" "$LANDING_REVISION" "$PRIOR_REVISION" "$ARCHIVE_BASE_REVISION" "$REVISION" || return 1
  crossing_require_clean_head "$ROOT" || return 1
  jq -n --arg candidate "$REVISION" --arg lane "$LANE_REVISION" --arg landing "$LANDING_REVISION" --arg prior "$PRIOR_REVISION" \
    --arg archive "$ARCHIVE_BASE_REVISION" --arg preflight "$CROSSING_PREFLIGHT_BASE" \
    --arg archiveVersions "$(crossing_migration_versions "$ROOT" "$ARCHIVE_BASE_REVISION")" \
    --arg laneVersions "$(crossing_migration_versions "$ROOT" "$LANE_REVISION")" \
    --arg landingVersions "$(crossing_migration_versions "$ROOT" "$LANDING_REVISION")" \
    --arg priorVersions "$(crossing_migration_versions "$ROOT" "$PRIOR_REVISION")" \
    --arg candidateVersions "$(crossing_migration_versions "$ROOT" "$REVISION")" \
    --arg reasonVersion "$(crossing_reason_migration_version "$ROOT" "$REVISION")" \
    --arg archiveToCandidate "$(crossing_migration_difference "$ROOT" "$ARCHIVE_BASE_REVISION" "$REVISION")" \
    '{candidate:$candidate, candidateTreeClean:true, laneBase:$lane, landing:$landing, prior:$prior, archiveBase:$archive,
      recordedPreflightBase:$preflight, laneBaseIsPreflightBase:($lane == $preflight),
      migrationVersions:{archiveBase:$archiveVersions, laneBase:$laneVersions, landing:$landingVersions, prior:$priorVersions, candidate:$candidateVersions},
      reasonReviewMigrationVersion:$reasonVersion, archiveToCandidateMigrations:$archiveToCandidate}' \
    >"$cdir/revisions.json"
  jq -e '.laneBaseIsPreflightBase' "$cdir/revisions.json" >/dev/null \
    || echo 'crossing: note: LANE_BASE differs from the recorded preflight base (recorded in revisions.json)' >&2
  crossing_os_standin "$cdir" || return 1
  CROSSING_RUN="$(isolation_run_id)"
  CROSSING_HOST_BEFORE=()
  for f in "${CROSSING_HOST_FILES[@]}"; do [[ ! -e "$f" ]] || CROSSING_HOST_BEFORE+=("$f"); done
  CROSSING_HOST_RECORDED=1
  crossing_expose_objects
  # The candidate is the committed head, built from its own archive. A --image
  # given to the runner serves only the fixture stack; it is never used here.
  CROSSING_CAND_IMG="$(crossing_build_image candidate "$REVISION" "$CROSSING_CANDIDATE_LEVEL" 1)"
  CROSSING_CAND_TAG="${CROSSING_CAND_IMG##*:}"
  CROSSING_LANE_IMG="$(crossing_build_image lane "$LANE_REVISION" 0 1)"
  CROSSING_LANE_TAG="${CROSSING_LANE_IMG##*:}"
  CROSSING_LANDING_IMG="$(crossing_build_image landing "$LANDING_REVISION" 1 1)"
  CROSSING_LANDING_TAG="${CROSSING_LANDING_IMG##*:}"
  CROSSING_PRIOR_IMG="$(crossing_build_image prior "$PRIOR_REVISION" 2 1)"
  CROSSING_PRIOR_TAG="${CROSSING_PRIOR_IMG##*:}"
  archive_level="$(commit_recovery_level "$ROOT" "$ARCHIVE_BASE_REVISION")" || return 1
  if [[ "$ARCHIVE_BASE_REVISION" == "$LANE_REVISION" ]]; then
    # One build serves both roles; both are recorded.
    CROSSING_ARCHIVE_IMG="$CROSSING_LANE_IMG"
    crossing_image_facts archive "$ARCHIVE_BASE_REVISION" "$CROSSING_ARCHIVE_IMG" "$archive_level" reused-from-lane
  elif [[ "$ARCHIVE_BASE_REVISION" == "$LANDING_REVISION" ]]; then
    CROSSING_ARCHIVE_IMG="$CROSSING_LANDING_IMG"
    crossing_image_facts archive "$ARCHIVE_BASE_REVISION" "$CROSSING_ARCHIVE_IMG" "$archive_level" reused-from-landing
  else
    CROSSING_ARCHIVE_IMG="$(crossing_build_image archive "$ARCHIVE_BASE_REVISION" "$archive_level" 0)"
  fi
  CROSSING_LANE="$LANE_REVISION"
  crossing_prepare_tools
  crossing_use_main
  CROSSING_READY=1
}

# The fixture SOURCE gets the other commits' objects through an alternates
# file, never a fetch or checkout: its HEAD and tree are untouched, and
# --commit LANE_BASE resolves exactly as it would on a droplet's checkout.
crossing_expose_objects() {
  local common
  common="$(git -C "$ROOT" rev-parse --path-format=absolute --git-common-dir)"
  [[ -d "$common/objects" ]] || { echo "crossing: no object store at $common" >&2; return 1; }
  mkdir -p "$SOURCE/.git/objects/info"
  grep -qxF "$common/objects" "$SOURCE/.git/objects/info/alternates" 2>/dev/null \
    || printf '%s\n' "$common/objects" >>"$SOURCE/.git/objects/info/alternates"
  git -C "$SOURCE" update-ref refs/drill/lane "$LANE_REVISION"
  git -C "$SOURCE" update-ref refs/drill/landing "$LANDING_REVISION"
  git -C "$SOURCE" update-ref refs/drill/prior "$PRIOR_REVISION"
  git -C "$SOURCE" update-ref refs/drill/archive-base "$ARCHIVE_BASE_REVISION"
  [[ "$(commit_recovery_level "$SOURCE" "$LANE_REVISION")" == 0 ]]
  [[ "$(commit_recovery_level "$SOURCE" "$LANDING_REVISION")" == 1 ]]
}

# role sha want_level push -> prints the image reference. The tag is also the
# FOLD_VERSION the binary reports, so the running binary names its own build.
crossing_build_image() {
  local role="$1" sha="$2" want="$3" push="$4" src tag ref cdir
  cdir="$(crossing_dir)"
  src="$cdir/src-$role"
  # Called inside $( ), where errexit is off: every step answers for itself.
  mkdir "$src" || return 1
  git -C "$ROOT" archive "$sha" | tar -C "$src" -xf - || return 1
  tag="real-$role-${sha:0:12}"
  ref="$REGISTRY/fold:$tag"
  docker build --label "org.opencontainers.image.revision=$sha" --build-arg "FOLD_VERSION=$tag" -t "$ref" "$src" >"$cdir/build-$role.log" 2>&1 \
    || { tail -n 40 "$cdir/build-$role.log" >&2; return 1; }
  rm -rf "$src" || return 1
  crossing_image_facts "$role" "$sha" "$ref" "$want" built >&2 || return 1
  if (( push )); then
    docker push "$ref" >"$cdir/push-$role.log" 2>&1 || { tail -n 20 "$cdir/push-$role.log" >&2; return 1; }
  fi
  printf '%s\n' "$ref"
}

# Records an image and fails unless it reports the level the role needs, the
# version equal to its tag, and the revision label of its commit.
crossing_image_facts() {
  local role="$1" sha="$2" ref="$3" want="$4" source="$5" level ver id digests label tag
  tag="${ref##*:}"
  level="$(image_recovery_level "$ref")" || return 1
  [[ "$level" == "$want" ]] || { echo "crossing: $role image $ref reports recovery level $level, expected $want" >&2; return 1; }
  ver="$(docker run --rm --network none --pull never --entrypoint /fold "$ref" version --json)" || return 1
  jq -e --arg v "$tag" '.version == $v' <<<"$ver" >/dev/null || { echo "crossing: $role image does not report its build version $tag: $ver" >&2; return 1; }
  label="$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$ref")" || return 1
  [[ "$label" == "$sha" ]] || { echo "crossing: $role image is labelled $label, not $sha" >&2; return 1; }
  id="$(docker image inspect --format '{{.Id}}' "$ref")" || return 1
  digests="$(docker image inspect --format '{{json .RepoDigests}}' "$ref")" || return 1
  crossing_emit images.jsonl --arg role "$role" --arg sha "$sha" --arg image "$ref" --arg imageId "$id" \
    --argjson digests "$digests" --argjson version "$ver" --argjson level "$level" --arg source "$source" \
    '{role:$role, sha:$sha, image:$image, imageId:$imageId, repoDigests:$digests, versionJson:$version, level:$level, source:$source}'
}

# Smoke for the production-compose lineage sites: the real smoke needs public
# TLS, sshd and ufw. It succeeds once the app's own healthcheck (which pings
# Postgres) passes. A deliberate, recorded deviation in both tool copies.
crossing_write_smoke() {
  cat >"$1" <<'SMOKE'
#!/usr/bin/env bash
# R4-6c-V lineage drill replacement for smoke.sh.
set -euo pipefail
dir=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --deploy-dir) dir="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[[ -n "$dir" ]] || exit 2
[[ ! -e "$dir/drill-smoke-fail" ]] || exit 1
for ((i = 0; i < 120; i++)); do
  if docker compose --project-directory "$dir" exec -T app /fold healthcheck >/dev/null 2>&1; then exit 0; fi
  sleep 1
done
exit 1
SMOKE
  chmod 755 "$1"
}

# The candidate's tool copy is the working tree (proved clean); the level-0 and
# level-1 copies are LANE_BASE's and the landing's own tools. Each is checked
# byte for byte against git.
crossing_copy_tools() { # label rev dest file...
  local label="$1" rev="$2" dest="$3" f blob cur same; shift 3
  mkdir "$dest" || return 1
  for f in "$@"; do
    if [[ "$label" == candidate ]]; then cp -p "$ROOT/deploy/digitalocean/$f" "$dest/$f" || return 1
    else git -C "$ROOT" show "$rev:deploy/digitalocean/$f" >"$dest/$f" || return 1; fi
    [[ "$f" != *.sh ]] || chmod 755 "$dest/$f"
  done
  for f in "$dest"/*; do
    blob="$(git -C "$ROOT" rev-parse "$rev:deploy/digitalocean/${f##*/}")" || return 1
    cur="$(git hash-object "$f")" || return 1
    if [[ "$blob" == "$cur" ]]; then same=true; else same=false; fi
    crossing_emit tools.jsonl --arg label "$label" --arg file "${f##*/}" --arg blob "$blob" --argjson identical "$same" \
      '{label:$label, file:$file, gitBlob:$blob, identicalToGit:$identical}'
    [[ "$same" == true ]] || { echo "crossing: $label tool ${f##*/} differs from git" >&2; return 1; }
  done
}

crossing_prepare_tools() {
  local cdir f
  local -a cand=()
  cdir="$(crossing_dir)"
  CROSSING_TOOLS_CAND="$cdir/tools-cand"; CROSSING_TOOLS_LANE="$cdir/tools-lane"; CROSSING_TOOLS_LANDING="$cdir/tools-landing"
  for f in $(runtime_tools); do cand+=("$f"); done
  crossing_copy_tools candidate "$REVISION" "$CROSSING_TOOLS_CAND" "${cand[@]}"
  crossing_copy_tools lane "$LANE_REVISION" "$CROSSING_TOOLS_LANE" \
    provision.sh upgrade.sh checks.sh backup.sh smoke.sh email-dns.sh backup.env.example
  crossing_copy_tools landing "$LANDING_REVISION" "$CROSSING_TOOLS_LANDING" \
    provision.sh upgrade.sh checks.sh backup.sh smoke.sh email-dns.sh backup.env.example deploy-tools-version
  crossing_write_smoke "$CROSSING_TOOLS_CAND/smoke.sh"
  crossing_write_smoke "$CROSSING_TOOLS_LANE/smoke.sh"
  crossing_write_smoke "$CROSSING_TOOLS_LANDING/smoke.sh"
  crossing_emit tools.jsonl --arg label all --arg file smoke.sh '{label:$label, file:$file, replacedByDrill:true}'
}

# The main site runs the real candidate image, at the version the build named.
crossing_deploy_main() {
  reset_case "$CROSSING_CAND_IMG"
  crossing_use_main
  crossing_assert_deployed "$CROSSING_CAND_TAG"
}
# tag [level]: the app reports tag and level, and the floor file has risen to it.
crossing_assert_deployed() {
  local want="$1" level="${2:-$CROSSING_CANDIDATE_LEVEL}" json
  json="$(crossing_cdc exec -T app /fold version --json)"
  jq -e --arg v "$want" --argjson l "$level" '.version == $v and ((.recoveryPolicy // 0) == $l)' <<<"$json" >/dev/null \
    || { echo "crossing: the deployed app is not the real level-$level build $want: $json" >&2; return 1; }
  [[ "$(cat "$CX_DIR/recovery-policy-floor")" == "$level" ]]
}

# One refused invocation, run under xtrace from the entry point named by the
# caller. Fails the runner unless crossing_problems finds nothing. target is
# level0 or level1: the release the invocation asks for.
crossing_refuse() {
  local deployed="$1" acceptance="$2" kind="$3" target="$4" label="$5"; shift 5
  local -a cmd=("$@")
  local cdir id rc=0 start=$SECONDS problems msg ok=true tlevel
  tlevel="$(crossing_target_level "$target")" || return 1
  CROSSING_CASE=$((CROSSING_CASE+1))
  id="$(printf '%s-%02d-%s' "$CX_SITE" "$CROSSING_CASE" "$label")"
  cdir="$(crossing_dir)/cases/$id"
  mkdir -p "$cdir"
  { printf '%q ' "${cmd[@]}"; printf '\n'; } >"$cdir/cmd"
  drill_note "crossing/$id"
  snapshot_state "$CX_DIR" "$CX_SOURCE" >"$cdir/state.before"
  timeout --kill-after=10 300 env SHELLOPTS=xtrace bash -x "${cmd[@]}" >"$cdir/trace" 2>&1 </dev/null || rc=$?
  snapshot_state "$CX_DIR" "$CX_SOURCE" >"$cdir/state.after"
  problems="$(crossing_problems "$kind" "$tlevel" "$CROSSING_CANDIDATE_LEVEL" "$rc" "$cdir/trace" "$cdir/state.before" "$cdir/state.after" "$CX_SOURCE/deploy/digitalocean/provision.sh")"
  [[ -z "$problems" ]] || ok=false
  msg="$(grep -m1 -E '^check: ' "$cdir/trace" || true)"
  [[ ! -e "$WORK/bypass-marker" ]] || { ok=false; problems+=$'\nthe fixture provisioner ran'; }
  crossing_emit cases.jsonl --arg kind refusal --arg acceptance "$acceptance" --arg deployed "$deployed" --arg site "$CX_SITE" \
    --arg target "$target" --argjson floor "$CROSSING_CANDIDATE_LEVEL" \
    --argjson fixture "$CX_FIXTURE" --arg id "$id" --arg entry "${cmd[0]}" --arg command "$(cat "$cdir/cmd")" \
    --argjson exitCode "$rc" --arg message "$msg" --argjson pass "$ok" --argjson seconds "$((SECONDS-start))" \
    --arg before "$(sha256sum <"$cdir/state.before" | cut -d' ' -f1)" --arg after "$(sha256sum <"$cdir/state.after" | cut -d' ' -f1)" \
    --arg trace "cases/$id/trace" --arg problems "$problems" \
    '{kind:$kind, acceptance:$acceptance, deployed:$deployed, site:$site, target:$target, hostFloor:$floor, fixture:$fixture, id:$id, entry:$entry, command:$command,
      exitCode:$exitCode, message:$message, pass:$pass, seconds:$seconds, snapshotBeforeSha256:$before,
      snapshotAfterSha256:$after, trace:$trace, problems:$problems}'
  if [[ "$ok" != true ]]; then
    printf 'problem: %s\n' "$problems" >&2
    echo "crossing: $id did not behave as accepted (see $cdir)" >&2
    return 1
  fi
  echo "crossing: ok $id (rc=$rc): $msg"
}

# Forward and restore refusals toward LANE_BASE (level 0) and the landing
# (level 1) on the site CX_* names.
crossing_forward_restore_matrix() {
  local deployed="$1" i t
  local -a common=(--deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE")
  local -a ups=("$CX_DIR/upgrade.sh" "$CX_TOOLS/upgrade.sh") rst=("$CX_DIR/restore.sh" "$CX_TOOLS/restore.sh")
  local -a names=(installed candidate-tools)
  for t in level0 level1; do
    for i in 0 1; do
      crossing_refuse "$deployed" R-forward-commit forward-commit "$t" "forward-commit.$t.${names[i]}" "${ups[i]}" "${common[@]}" --commit "$(crossing_target_commit "$t")"
    done
    for i in 0 1; do
      crossing_refuse "$deployed" R-forward-image forward-image "$t" "forward-image.$t.${names[i]}" "${ups[i]}" "${common[@]}" --image "$(crossing_target_image "$t")"
    done
    for i in 0 1; do
      crossing_refuse "$deployed" R-restore-commit restore-commit "$t" "restore-commit.$t.${names[i]}" "${rst[i]}" --deploy-dir "$CX_DIR" --drill \
        --dump "$CX_DUMP" --source-dir "$CX_SOURCE" --commit "$(crossing_target_commit "$t")"
    done
    for i in 0 1; do
      crossing_refuse "$deployed" R-restore-image restore-image "$t" "restore-image.$t.${names[i]}" "${rst[i]}" --deploy-dir "$CX_DIR" --drill \
        --dump "$CX_DUMP" --source-dir "$CX_SOURCE" --image "$(crossing_target_image "$t")"
    done
  done
  [[ "$(cat "$CX_DIR/recovery-policy-floor")" == "$CROSSING_CANDIDATE_LEVEL" ]]
  [[ ! -e "$CX_DIR/recovery.lock" ]]
}

# SUPPLEMENTARY FIXTURE rows (S-*). A real same-level forward upgrade gives a
# genuine release.prev and dump; its previous release is then rewritten to name
# the target, the way deploy-drill.sh's own legacy rollback refusal does. These
# prove only that the level check reads the record; the natural records are the
# R-rollback-genuine rows of the lineage sites.
crossing_record_release() {
  local mode="$1" image="$2" commit="$3"
  [[ -f "$CX_DIR/release.prev" ]] || { echo 'crossing: no release.prev to rewrite' >&2; return 1; }
  sed -i -e "s|^mode=.*|mode=$mode|" -e "s|^previous_image=.*|previous_image=$image|" -e "s|^previous_commit=.*|previous_commit=$commit|" "$CX_DIR/release.prev"
  chmod 600 "$CX_DIR/release.prev"
}
crossing_supplementary_rollback_rows() {
  local i log t
  local -a common=(--deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE")
  local -a ups=("$CX_DIR/upgrade.sh" "$CX_TOOLS/upgrade.sh") names=(installed candidate-tools)
  log="$(crossing_dir)/prepare-supplementary-rollback.log"
  # A same-level (2 -> 2) forward upgrade: the prior release, then the candidate.
  reset_case "$CROSSING_PRIOR_IMG"
  "$CX_DIR/upgrade.sh" "${common[@]}" --image "$CROSSING_CAND_IMG" >"$log" 2>&1 || { tail -n 40 "$log" >&2; return 1; }
  wait_public
  crossing_assert_deployed "$CROSSING_CAND_TAG"
  CX_FIXTURE=true
  for t in level0 level1; do
    crossing_record_release build "" "$(crossing_target_commit "$t")"
    for i in 0 1; do
      crossing_refuse image S-rollback-commit-record rollback-commit "$t" "rollback-commit-record.$t.${names[i]}" "${ups[i]}" "${common[@]}" --rollback
    done
    crossing_record_release image "$(crossing_target_image "$t")" ""
    for i in 0 1; do
      crossing_refuse image S-rollback-image-record rollback-image "$t" "rollback-image-record.$t.${names[i]}" "${ups[i]}" "${common[@]}" --rollback
    done
  done
  CX_FIXTURE=false
}

real_binary_refusals() {
  crossing_prepare
  drill_step refusals/main-image
  crossing_deploy_main
  crossing_forward_restore_matrix image
  crossing_supplementary_rollback_rows
  # The host floor is 2 for the rest of the run: a fixture level-1 image
  # would be refused, so every reset names the candidate.
  reset_case "$CROSSING_CAND_IMG"
}

# ---------------------------------------------------------------------------
# Genuine lineage on fresh sites (production Compose and Caddy per commit, the
# same method as the T07B-1-V drill): origin level 0 (LANE_BASE) and origin
# level 1 (the landing), each upgraded for real to the level-2 candidate.
# ---------------------------------------------------------------------------

# A repository whose objects come from ROOT by alternates, clean at one commit.
crossing_checkout() {
  local dest="$1" sha="$2" common
  common="$(git -C "$ROOT" rev-parse --path-format=absolute --git-common-dir)"
  git -c init.defaultBranch=main init -q "$dest"
  mkdir -p "$dest/.git/objects/info"
  printf '%s\n' "$common/objects" >"$dest/.git/objects/info/alternates"
  git -C "$dest" update-ref refs/drill/candidate "$REVISION"
  git -C "$dest" update-ref refs/drill/lane "$LANE_REVISION"
  git -C "$dest" update-ref refs/drill/landing "$LANDING_REVISION"
  git -C "$dest" checkout -q --detach "$sha"
  [[ "$(git -C "$dest" rev-parse HEAD)" == "$sha" && -z "$(git -C "$dest" status --porcelain)" ]]
}

crossing_site_prereqs() {
  local port nets
  for port in 80 443; do
    if port_open "$port"; then echo "crossing: host port $port is busy; the level-0 sites publish Caddy on 80 and 443" >&2; return 1; fi
  done
  nets="$(docker network ls -q | xargs -r docker network inspect --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}')" || return 1
  if grep -qF 172.30.0.0/24 <<<"$nets"; then
    echo 'crossing: a Docker network already uses 172.30.0.0/24, the shipped Compose subnet; the level-0 sites cannot start' >&2
    return 1
  fi
}

# name start origin_rev: CX_SOURCE is a clean checkout at start (the candidate
# for an image site, the origin for a build site); CX_ORIGIN_SRC is the origin.
crossing_site_begin() {
  local name="$1" start="$2" origin="$3" base
  base="$(crossing_dir)/sites/$name"
  mkdir -p "$base/site"; chmod 755 "$base/site"
  CX_SITE="lineage-$name"; CX_DIR="$base/site"; CX_SOURCE="$base/src"; CX_ORIGIN_SRC="$base/origin-src"
  CX_TOOLS="$CROSSING_TOOLS_CAND"; CX_FIXTURE=false
  CX_PROJECT="fold-crossing-${CROSSING_RUN:?crossing_prepare sets the run id}-$name"
  CROSSING_PROJECTS+=("$CX_PROJECT")
  printf 'fold-deploy-drill-v1:%s\n' "$CX_DIR" >"$CX_DIR/.deploy-drill"
  chmod 600 "$CX_DIR/.deploy-drill"
  cat >"$CX_DIR/.env" <<ENV
COMPOSE_PROJECT_NAME=$CX_PROJECT
DOMAIN=$CROSSING_DOMAIN
DB_PASSWORD=drill-disposable-db-password
SESSION_SECRET=drill-disposable-session-secret-0123456789abcdef
ENV
  chmod 600 "$CX_DIR/.env"
  crossing_checkout "$CX_SOURCE" "$start"
  crossing_checkout "$CX_ORIGIN_SRC" "$origin"
}

crossing_site_end() {
  crossing_cdc down -v --remove-orphans >"$(crossing_dir)/sites/down-$CX_PROJECT.log" 2>&1 || true
  crossing_force_project "$CX_PROJECT"
  crossing_cleanup_host
}

crossing_force_project() {
  local p="$1" ids
  ids="$(docker ps -aq --filter "label=com.docker.compose.project=$p" 2>/dev/null || true)"
  # shellcheck disable=SC2086 # a whitespace list of ids
  [[ -z "$ids" ]] || docker rm -fv $ids >/dev/null 2>&1 || true
  ids="$(docker network ls -q --filter "label=com.docker.compose.project=$p" 2>/dev/null || true)"
  # shellcheck disable=SC2086
  [[ -z "$ids" ]] || docker network rm $ids >/dev/null 2>&1 || true
  ids="$(docker volume ls -q --filter "label=com.docker.compose.project=$p" 2>/dev/null || true)"
  # shellcheck disable=SC2086
  [[ -z "$ids" ]] || docker volume rm -f $ids >/dev/null 2>&1 || true
}

# LANE_BASE's own provisioner writes cron and logrotate entries into the host.
# Remove exactly those it created during this run, whether it passed or failed.
crossing_cleanup_host() {
  local f kept b
  # Without the before-list, a file may predate this run: remove nothing.
  [[ "${CROSSING_HOST_RECORDED:-0}" == 1 ]] || return 0
  for f in "${CROSSING_HOST_FILES[@]}"; do
    kept=0
    for b in "${CROSSING_HOST_BEFORE[@]}"; do [[ "$b" != "$f" ]] || kept=1; done
    (( kept )) || rm -f "$f"
  done
}
# Called from the runner's EXIT trap: nothing created here outlives a failed run.
crossing_cleanup_all() {
  local p
  for p in "${CROSSING_PROJECTS[@]}"; do crossing_force_project "$p"; done
  crossing_cleanup_host
}

crossing_binary_sha() { # container or image-container id
  docker cp "$1:/fold" - | tar -xO | sha256sum | cut -d' ' -f1
}


# origin: 0 (LANE_BASE) or 1 (the landing); mode: image or build.
crossing_lineage_site() {
  local origin="$1" mode="$2" cdir trace rc=0 json cid image_id built_id origin_json deployed kind cand_bin img_bin f i
  local origin_rev origin_tools origin_img origin_tag target
  local -a prov args ups names
  cdir="$(crossing_dir)"
  target="level$origin"
  if (( origin == 0 )); then
    origin_rev="$LANE_REVISION"; origin_tools="$CROSSING_TOOLS_LANE"; origin_img="$CROSSING_LANE_IMG"; origin_tag="$CROSSING_LANE_TAG"
  else
    origin_rev="$LANDING_REVISION"; origin_tools="$CROSSING_TOOLS_LANDING"; origin_img="$CROSSING_LANDING_IMG"; origin_tag="$CROSSING_LANDING_TAG"
  fi
  crossing_site_prereqs
  if [[ "$mode" == image ]]; then crossing_site_begin "l$origin-image" "$REVISION" "$origin_rev"
  else crossing_site_begin "l$origin-build" "$origin_rev" "$origin_rev"; fi
  deployed="$mode"
  # 1. A genuine host of the origin level: that commit's own tools, source and image.
  prov=("$origin_tools/provision.sh" --skip-harden --allow-os --domain "$CROSSING_DOMAIN" --deploy-dir "$CX_DIR")
  if [[ "$mode" == image ]]; then prov+=(--source-dir "$CX_ORIGIN_SRC" --image "$origin_img")
  else prov+=(--source-dir "$CX_SOURCE" --allow-onbox-build); fi
  timeout --kill-after=15 2700 "${prov[@]}" >"$cdir/lineage-l$origin-$mode-origin-provision.log" 2>&1 </dev/null || rc=$?
  (( rc == 0 )) || { tail -n 40 "$cdir/lineage-l$origin-$mode-origin-provision.log" >&2; echo "crossing: the level-$origin provision failed (rc=$rc)" >&2; return 1; }
  origin_json="$(crossing_cdc exec -T app /fold version --json)"
  if (( origin == 0 )); then
    jq -e '(has("recoveryPolicy") | not) or .recoveryPolicy == 0' <<<"$origin_json" >/dev/null \
      || { echo "crossing: the LANE_BASE host is not level 0: $origin_json" >&2; return 1; }
    [[ ! -e "$CX_DIR/recovery-policy-floor" ]]
  else
    jq -e '.recoveryPolicy == 1' <<<"$origin_json" >/dev/null \
      || { echo "crossing: the landing host is not level 1: $origin_json" >&2; return 1; }
    [[ "$(cat "$CX_DIR/recovery-policy-floor")" == 1 ]]
  fi
  if [[ "$mode" == image ]]; then jq -e --arg v "$origin_tag" '.version == $v' <<<"$origin_json" >/dev/null
  else [[ "$(git -C "$CX_SOURCE" rev-parse HEAD)" == "$origin_rev" ]]; fi
  crossing_cpg fold -c "CREATE TABLE drill_sentinel(id integer PRIMARY KEY, value text); INSERT INTO drill_sentinel VALUES (1, 'original')" >/dev/null
  # 2. A real forward upgrade by the candidate's tools. release.prev is written
  #    by upgrade.sh itself from the host it finds.
  if [[ "$mode" == image ]]; then args=(--image "$CROSSING_CAND_IMG"); else args=(--commit "$REVISION"); fi
  trace="$cdir/lineage-l$origin-$mode-upgrade.trace"; rc=0
  timeout --kill-after=15 2700 env SHELLOPTS=xtrace bash -x "$CROSSING_TOOLS_CAND/upgrade.sh" --deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE" "${args[@]}" >"$trace" 2>&1 </dev/null || rc=$?
  (( rc == 0 )) || { tail -n 40 "$trace" >&2; echo "crossing: the forward upgrade from the level-$origin host failed (rc=$rc)" >&2; return 1; }
  [[ "$(grep -cF "+ $CX_DIR/deploy-tools/provision.sh --skip-harden" "$trace")" == 1 ]]
  if grep -Fq -- "$CX_SOURCE/deploy/digitalocean/provision.sh" "$trace"; then echo 'crossing: the upgrade ran a checkout provision.sh' >&2; return 1; fi
  [[ "$(cat "$CX_DIR/recovery-policy-floor")" == "$CROSSING_CANDIDATE_LEVEL" ]]
  if [[ "$mode" == image ]]; then
    grep -qx 'mode=image' "$CX_DIR/release.prev"
    grep -qx "previous_image=$origin_img" "$CX_DIR/release.prev"
    crossing_assert_deployed "$CROSSING_CAND_TAG"
  else
    grep -qx 'mode=build' "$CX_DIR/release.prev"
    grep -qx "previous_commit=$origin_rev" "$CX_DIR/release.prev"
    [[ "$(git -C "$CX_SOURCE" rev-parse HEAD)" == "$REVISION" ]]
    crossing_assert_deployed dev
    cid="$(crossing_cdc ps -q app)"
    image_id="$(docker inspect --format '{{.Image}}' "$cid")"
    built_id="$(docker image inspect --format '{{.Id}}' ghcr.io/grierj/fold:dev)"
    [[ "$image_id" == "$built_id" ]] || { echo 'crossing: the running app is not the image built from the head Dockerfile' >&2; return 1; }
    img_bin="$(crossing_binary_sha "$cid")"
    cid="$(docker create "$CROSSING_CAND_IMG")"
    cand_bin="$(crossing_binary_sha "$cid")"
    docker rm "$cid" >/dev/null
    jq -n --arg built "$built_id" --arg head "$REVISION" --arg onBox "$img_bin" --arg registry "$cand_bin" --argjson origin "$origin" \
      '{origin:$origin, commit:$head, builtImageId:$built, onBoxBinarySha256:$onBox, registryCandidateBinarySha256:$registry,
        note:"the registry candidate reports its own FOLD_VERSION and the on-box build reports dev, so equal hashes are not required"}' \
      >"$cdir/lineage-l$origin-build-binary.json"
  fi
  json="$(crossing_cdc exec -T app /fold version --json)"
  for f in $(runtime_tools); do
    cmp "$CROSSING_TOOLS_CAND/$f" "$CX_DIR/$f"
    cmp "$CROSSING_TOOLS_CAND/$f" "$CX_DIR/deploy-tools/$f"
  done
  crossing_emit cases.jsonl --arg acceptance A-lineage-upgrade --arg mode "$mode" --arg site "$CX_SITE" --argjson origin "$origin" --arg target "$target" \
    --argjson originVersion "$origin_json" --argjson candidate "$json" --arg release "$(cat "$CX_DIR/release.prev")" \
    --arg floor "$(cat "$CX_DIR/recovery-policy-floor")" \
    '{kind:"positive", acceptance:$acceptance, id:("lineage-l" + ($origin|tostring) + "-" + $mode), mode:$mode, origin:$origin, target:$target,
      site:$site, fixture:false, pass:true, originVersion:$originVersion, candidateVersion:$candidate, releasePrev:$release, floor:$floor}'
  # 3. Natural rollback refusals; the build sites also take the full matrix.
  ups=("$CX_DIR/upgrade.sh" "$CROSSING_TOOLS_CAND/upgrade.sh"); names=(installed candidate-tools)
  if [[ "$mode" == image ]]; then kind=rollback-image; else kind=rollback-commit; fi
  for i in 0 1; do
    crossing_refuse "$deployed" R-rollback-genuine "$kind" "$target" "rollback-genuine.${names[i]}" "${ups[i]}" --deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE" --rollback
  done
  if [[ "$mode" == build ]]; then
    CX_DUMP="$(find "$CX_DIR/dumps" -name 'fold-*.dump' | sort | tail -n 1)"
    [[ -f "$CX_DUMP" ]]
    crossing_forward_restore_matrix build
  fi
  # A failed run is cleaned by the runner's EXIT trap (crossing_cleanup_all).
  crossing_site_end
}

real_level_lineage() {
  local origin mode
  crossing_prepare
  for origin in 0 1; do
    for mode in image build; do
      drill_step "lineage/l$origin-$mode"
      crossing_lineage_site "$origin" "$mode"
    done
  done
  crossing_use_main
}

# ---------------------------------------------------------------------------
# Same-level rollback and the older archive on the main site.
# ---------------------------------------------------------------------------

crossing_tool_hashes() {
  local f
  for f in $(runtime_tools); do
    printf '%s DIR %s\n' "$(sha256sum <"$DIR/$f" | cut -d' ' -f1)" "$f"
    printf '%s deploy-tools %s\n' "$(sha256sum <"$DIR/deploy-tools/$f" | cut -d' ' -f1)" "$f"
  done
}

crossing_wait_ready() {
  local file="$1" pattern="$2" pid="$3" end=$((SECONDS+${4:-300}))
  while (( SECONDS < end )); do
    if [[ -f "$file" ]] && grep -q -- "$pattern" "$file"; then return 0; fi
    kill -0 "$pid" 2>/dev/null || { echo "crossing: the holder exited before $file reached $pattern" >&2; return 1; }
    sleep .1
  done
  echo "crossing: $file never reached $pattern" >&2
  return 1
}

real_same_level_rollback() {
  local cdir trace log recovery_image rid rjson ajson aid f problems
  crossing_prepare
  crossing_use_main
  cdir="$(crossing_dir)"
  trace="$cdir/rollback.trace"; log="$cdir/forward-to-candidate.log"
  drill_step prior-deployed
  reset_case "$CROSSING_PRIOR_IMG"
  ajson="$(dc exec -T app /fold version --json)"
  jq -e --arg v "$CROSSING_PRIOR_TAG" '.version == $v and ((.recoveryPolicy // 0) == 2)' <<<"$ajson" >/dev/null \
    || { echo "crossing: the deployed app is not the real prior image (level 2): $ajson" >&2; return 1; }
  drill_step forward-to-candidate
  "$DIR/upgrade.sh" --deploy-dir "$DIR" --source-dir "$SOURCE" --image "$CROSSING_CAND_IMG" >"$log" 2>&1 || { tail -n 40 "$log" >&2; return 1; }
  wait_public
  crossing_assert_deployed "$CROSSING_CAND_TAG"
  grep -qx 'mode=image' "$DIR/release.prev"
  grep -qx "previous_image=$CROSSING_PRIOR_IMG" "$DIR/release.prev"
  crossing_tool_hashes >"$cdir/tools.before"
  drill_step rollback-held-in-recovery
  rm -f "$DIR/drill.ready"
  [[ -p "$DIR/drill.fifo" ]] || mkfifo "$DIR/drill.fifo"
  RESTORE_DRILL_PAUSE=recovering env SHELLOPTS=xtrace bash -x "$DIR/upgrade.sh" --deploy-dir "$DIR" --source-dir "$SOURCE" --rollback >"$trace" 2>&1 &
  holder=$!
  crossing_wait_ready "$DIR/drill.ready" '^recovering$' "$holder"
  [[ "$(jq -er .phase "$DIR/recovery.lock")" == recovering && "$(jq -er .count "$DIR/recovery.lock")" == 0 ]]
  rid="$(recovery_ids)"
  [[ -n "$rid" && "$rid" != *$'\n'* ]]
  recovery_image="$(docker inspect --format '{{.Image}}' "$rid")"
  [[ "$recovery_image" == "$(jq -er .imageId "$DIR/recovery.lock")" ]]
  [[ "$recovery_image" == "$(docker image inspect --format '{{.Id}}' "$CROSSING_PRIOR_IMG")" ]]
  [[ "$(jq -er .level "$DIR/recovery.lock")" == 2 ]]
  rjson="$(docker exec "$rid" /fold version --json)"
  jq -e --arg v "$CROSSING_PRIOR_TAG" '.version == $v and .recoveryPolicy == 2' <<<"$rjson" >/dev/null \
    || { echo "crossing: the recovery instance is not the prior image at level 2: $rjson" >&2; return 1; }
  if port_open "$PUBLIC_PORT"; then echo 'crossing: public port open during recovery' >&2; return 1; fi
  curl -fsS http://localhost:18080/healthz >/dev/null
  release_barrier
  drill_step rollback-reopened
  [[ ! -e "$DIR/recovery.lock" && -z "$(recovery_ids)" ]]
  wait_public
  aid="$(dc ps -q app)"
  [[ -n "$aid" && "$aid" != *$'\n'* ]]
  [[ "$(docker inspect --format '{{.Image}}' "$aid")" == "$recovery_image" ]]
  ajson="$(dc exec -T app /fold version --json)"
  jq -e --arg v "$CROSSING_PRIOR_TAG" '.version == $v and .recoveryPolicy == 2' <<<"$ajson" >/dev/null \
    || { echo "crossing: the reopened app is not the prior image at level 2: $ajson" >&2; return 1; }
  [[ "$(cat "$DIR/recovery-policy-floor")" == 2 ]]
  grep -q 'Accounts this archive would revive: 0' "$trace"
  problems="$(crossing_recovery_tools_problems "$trace" "$DIR" "$SOURCE/deploy/digitalocean/")"
  if [[ -n "$problems" ]]; then printf 'crossing: restore.sh did not run from DIR/recovery-tools:\n%s\n' "$problems" >&2; return 1; fi
  [[ ! -e "$WORK/bypass-marker" ]]
  crossing_tool_hashes >"$cdir/tools.after"
  cmp "$cdir/tools.before" "$cdir/tools.after"
  for f in $(runtime_tools); do
    cmp "$TOOLS_DIR/$f" "$DIR/$f"
    cmp "$TOOLS_DIR/$f" "$DIR/deploy-tools/$f"
    cmp "$TOOLS_DIR/$f" "$DIR/recovery-tools/$f"
    if [[ "$f" != smoke.sh ]]; then cmp "$ROOT/deploy/digitalocean/$f" "$DIR/$f"; fi
  done
  crossing_emit cases.jsonl --arg acceptance A-same-level-rollback --arg image "$CROSSING_PRIOR_IMG" \
    --arg recoveryImageId "$recovery_image" --argjson recovery "$rjson" --argjson reopened "$ajson" \
    --arg toolsSha "$(sha256sum <"$cdir/tools.after" | cut -d' ' -f1)" \
    '{kind:"positive", acceptance:$acceptance, id:"same-level-rollback", fixture:false, pass:true, image:$image,
      recoveryImageId:$recoveryImageId, recoveryVersion:$recovery, reopenedVersion:$reopened, toolsSha256:$toolsSha}'
  reset_case "$CROSSING_CAND_IMG"
}


# ---------------------------------------------------------------------------
# restore.sh --dump with the current release, and a reason-review panel that
# was open across it (T07B-2-V; stateless review, no draft rows to restore).
# ---------------------------------------------------------------------------

# method path data outfile: prints the HTTP status; the body is in outfile. Unlike
# api, an error status is an answer, not a failure of the runner.
crossing_api_status() {
  local method="$1" path="$2" data="${3-}" out="$4" token
  [[ -n "$data" ]] || data='{}'
  token="$(csrf)"
  curl -sS -o "$out" -w '%{http_code}' -b "$JAR" -c "$JAR" -X "$method" -H "X-CSRF-Token: $token" -H 'Content-Type: application/json' \
    --data-binary "$data" "$URL/api/v1$path"
}

# status wanted-status answer-file jq-filter: true when the HTTP status and the
# JSON answer are the wanted ones.
crossing_answer_is() { [[ "$1" == "$2" ]] && jq -e "$4" "$3" >/dev/null; }

# target file: the panel as a browser holds it after opening the review: the
# page's items. A panel holds NO attempt id: the Delete press mints one (the
# panel keeps it in memory only), so a panel that was only opened has nothing
# for a restore to invalidate. crossing_panel_mint adds the id a press has
# already minted, for the cases where an earlier press is the point.
crossing_panel_open() {
  local target="$1" out="$2" page
  page="$(api POST "/admin/users/$target/erasure-reasons/page" '{"after":null}')" || return 1
  jq -e '.reviewRequired == true and (.items | length) == 2 and .next == null
         and (.items | all(.reviewHash | test("^[0-9a-f]{64}$")))' <<<"$page" >/dev/null \
    || { echo "crossing: the review page for user $target is not the two-item review: $page" >&2; return 1; }
  jq -n --argjson page "$page" --argjson t "$target" '{target:$t, attemptId:null, items:$page.items}' >"$out"
}
crossing_mint() { # target -> prints an attempt id minted by this process for that account
  api POST /admin/operation-attempts "$(jq -nc --argjson s "$1" '{kind:"account_delete",subjectUserId:$s}')" | jq -er .attemptId
}
crossing_panel_mint() { # target file: a press that minted its id (and kept it)
  local id; id="$(crossing_mint "$1")" || return 1
  jq --arg id "$id" '.attemptId = $id' "$2" >"$2.next" && mv "$2.next" "$2"
}
# file attempt-id: the DELETE body the panel sends: the first item removed, the
# rest kept, each with the hash it came with.
crossing_panel_body() {
  jq -c --arg id "$2" '{attemptId:$id, reasonReview:{items:[.items | to_entries[]
    | {requestId:.value.requestId, reviewHash:.value.reviewHash, choice:(if .key == 0 then "remove" else "keep" end)}]}}' "$1"
}
# A press: a fresh id is minted (the panel does this on every press) and the
# panel's choices are sent with it. Prints the HTTP status; the answer is in out.
crossing_press() { # target file out
  local id; id="$(crossing_mint "$1")" || return 1
  crossing_api_status DELETE "/admin/users/$1" "$(crossing_panel_body "$2" "$id")" "$3"
}
# After an answer the client cannot act on (restore_detected, history_unavailable)
# the users list is reloaded before Delete is allowed again (lib/deleteReload.ts).
# True when the reloaded list still shows the account.
crossing_reload_shows() { # target
  api GET /admin/users | jq -e --argjson id "$1" 'any(.users[]; .id == $id)' >/dev/null
}
# What a successful DELETE blanked and kept, from its answer and the database.
crossing_applied_exactly() { # status target panel-file answer-file
  local first kept status="$1"; shift
  [[ "$status" == 200 ]] || return 1
  first="$(jq -er '.items[0].requestId' "$2")"; kept="$(jq -er '.items[1].requestId' "$2")"
  jq -e '.erasure.reasonsRemoved == 1 and .erasure.reasonsKept == 1' "$3" >/dev/null || return 1
  [[ "$(pg fold -c "SELECT reason FROM contact_requests WHERE id=$first")" == '' ]] || return 1
  [[ "$(pg fold -c "SELECT reason <> '' FROM contact_requests WHERE id=$kept")" == t ]] || return 1
  [[ "$(pg fold -c "SELECT count(*) FROM audit_logs WHERE action IN ('add.reason_kept','add.reason_removed') AND target_id IN ($first,$kept)")" == 2 ]]
}

# Four accounts, each with two reasons (one per party direction), cover the four
# ways a panel meets a restore:
#   t1  panel opened, no Delete pressed (no attempt): after a restore.sh --dump
#       the press applies the panel's choices at once, hashes unchanged.
#   t2  the same, but a hashed column changed under the panel: 409
#       reason_review_changed, nothing blanked; the panel reloads its items and
#       the press then applies exactly those choices (the reload).
#   t3  a press already made (its id minted and used): a pg_restore under the
#       running app answers restore_detected, the container restarts, the old id
#       answers attempt_expired, settle says history_unavailable, the users list
#       is reloaded and still shows the account, and a fresh press applies.
#   t4  an id minted by a process that is then gone (restore.sh --dump): the old
#       id answers attempt_expired, settles history_unavailable, the list is
#       reloaded, a fresh press applies.
real_current_release_restore() {
  local cdir dump g req t1 t2 t3 t4 j1 t status out old_id first restarts_before restarts_after aid level new_items i
  local live_answer live_replay live_settle live_reload live_fresh sh_replay sh_settle sh_reload sh_fresh imm changed reloaded
  crossing_prepare
  crossing_use_main
  cdir="$(crossing_dir)"
  dump="$cdir/current-release.dump"
  out="$cdir/current-release-answer.json"
  drill_step current-release/fixtures
  reset_case "$CROSSING_CAND_IMG"
  level="$(dc exec -T app /fold version --json | jq -er '.recoveryPolicy')"
  [[ "$level" == "$CROSSING_CANDIDATE_LEVEL" ]]
  g="$(api POST /groups '{"name":"Reason review context","description":"Disposable fixture","visibility":"public","tags":[]}' | jq -er .group.id)"
  req="$(new_user 'Reason Requester' reason-requester@example.invalid)"
  t1="$(new_user 'Reason Subject One' reason-one@example.invalid)"
  t2="$(new_user 'Reason Subject Two' reason-two@example.invalid)"
  t3="$(new_user 'Reason Subject Three' reason-three@example.invalid)"
  t4="$(new_user 'Reason Subject Four' reason-four@example.invalid)"
  j1="$(new_user 'Advance Victim' reason-advance@example.invalid)"
  # Each subject is a party of two requests with reasons, once as the subject
  # and once as the requester (both party directions). Explicit fixture rows.
  for t in "$t1" "$t2" "$t3" "$t4"; do
    pg fold -c "$(crossing_reason_row_sql "$req" "$t" "$g" "Drill reason, user $t as subject")" \
            -c "$(crossing_reason_row_sql "$t" "$req" "$g" "Drill reason, user $t as requester")"
  done
  drill_step current-release/open-panels
  for t in "$t1" "$t2" "$t3" "$t4"; do crossing_panel_open "$t" "$cdir/panel-$t.json"; done
  # t1 and t2 stay as opened: attemptId null. t3 and t4 have pressed or minted.
  jq -e '.attemptId == null' "$cdir/panel-$t1.json" "$cdir/panel-$t2.json" >/dev/null
  crossing_panel_mint "$t3" "$cdir/panel-$t3.json"
  crossing_panel_mint "$t4" "$cdir/panel-$t4.json"
  dc exec -T db pg_dump -U church -Fc fold >"$dump"
  [[ -s "$dump" ]]
  # A fenced commit this process sees acknowledged and the dump lacks.
  api DELETE "/admin/users/$j1" '{}' >/dev/null
  [[ "$(pg fold -c "SELECT deleted_at IS NOT NULL FROM users WHERE id=$j1")" == t ]]

  drill_step current-release/live-pg_restore
  aid="$(dc ps -q app)"
  restarts_before="$(docker inspect --format '{{.RestartCount}}' "$aid")"
  # The same-lifetime restore: the data goes back under the running app.
  timeout 300 docker compose --project-directory "$DIR" exec -T db pg_restore -U church -d fold --clean --if-exists --no-owner <"$dump"
  [[ "$(pg fold -c "SELECT deleted_at IS NULL FROM users WHERE id=$j1")" == t ]]
  old_id="$(jq -er .attemptId "$cdir/panel-$t3.json")"
  status="$(crossing_api_status DELETE "/admin/users/$t3" "$(crossing_panel_body "$cdir/panel-$t3.json" "$old_id")" "$out")"
  crossing_answer_is "$status" 503 "$out" '.code == "restore_detected"' \
    || { echo "crossing: the press after a live restore answered $status: $(cat "$out")" >&2; return 1; }
  live_answer=restore_detected
  [[ "$(pg fold -c "SELECT count(*) FROM contact_requests WHERE reason = '' AND (requester_id=$t3 OR subject_user_id=$t3)")" == 0 ]]
  [[ "$(pg fold -c "SELECT deleted_at IS NULL FROM users WHERE id=$t3")" == t ]]
  # The process exits non-zero and Compose restarts it.
  for ((i=0;i<120;i++)); do
    restarts_after="$(docker inspect --format '{{.RestartCount}}' "$aid" 2>/dev/null || echo 0)"
    (( restarts_after > restarts_before )) && break
    sleep 1
  done
  (( restarts_after > restarts_before )) || { echo 'crossing: the app did not restart after restore_detected' >&2; return 1; }
  wait_public; URL="$(public_url)"; login
  # The id belongs to the old process: it ran nothing and settles unknown.
  status="$(crossing_api_status DELETE "/admin/users/$t3" "$(crossing_panel_body "$cdir/panel-$t3.json" "$old_id")" "$out")"
  crossing_answer_is "$status" 409 "$out" '.code == "attempt_expired"' \
    || { echo "crossing: the old attempt id answered $status after the restart: $(cat "$out")" >&2; return 1; }
  live_replay=attempt_expired
  status="$(crossing_api_status POST /admin/operation-attempts/settle \
    "$(jq -nc --arg id "$old_id" --argjson s "$t3" '{attemptId:$id,kind:"account_delete",subjectUserId:$s}')" "$out")"
  crossing_answer_is "$status" 200 "$out" '.result == "history_unavailable"' \
    || { echo "crossing: settle of the old id answered $status: $(cat "$out")" >&2; return 1; }
  live_settle=history_unavailable
  # The client cannot tell whether the press applied, so it reloads the users list
  # before Delete comes back: the account is there, and the reload is what allows a new press.
  crossing_reload_shows "$t3" || { echo "crossing: the reloaded users list does not show user $t3" >&2; return 1; }
  live_reload=true
  status="$(crossing_press "$t3" "$cdir/panel-$t3.json" "$out")"
  crossing_applied_exactly "$status" "$t3" "$cdir/panel-$t3.json" "$out" || { echo "crossing: the fresh press after reload answered $status: $(cat "$out")" >&2; return 1; }
  live_fresh=applied

  drill_step current-release/restore.sh-dump
  # The same dump through the installed tool with the current release: a plain
  # --dump completes into private recovery with the site closed.
  restore --dump "$dump"
  assert_private
  jq -e --arg v "$CROSSING_CAND_TAG" '.version == $v and .recoveryPolicy == 2' <<<"$(docker exec "$(recovery_ids)" /fold version --json)" >/dev/null \
    || { echo 'crossing: the recovery instance is not the committed head at level 2' >&2; return 1; }
  [[ "$(jq -er .level "$DIR/recovery.lock")" == 2 ]]
  URL=http://localhost:18080; login
  # t1: opened, never pressed. The press mints its own id and applies at once.
  status="$(crossing_press "$t1" "$cdir/panel-$t1.json" "$out")"
  crossing_applied_exactly "$status" "$t1" "$cdir/panel-$t1.json" "$out" \
    || { echo "crossing: the press of an open panel answered $status in recovery: $(cat "$out")" >&2; return 1; }
  imm=applied
  # t2: a hashed column changed under the panel (a trigger-bypassing fixture edit).
  first="$(jq -er '.items[0].requestId' "$cdir/panel-$t2.json")"
  pg fold -c 'SET session_replication_role = replica' -c "UPDATE contact_requests SET expires_at = expires_at + interval '1 day' WHERE id=$first"
  status="$(crossing_press "$t2" "$cdir/panel-$t2.json" "$out")"
  crossing_answer_is "$status" 409 "$out" '.code == "reason_review_changed"' \
    || { echo "crossing: a changed review answered $status: $(cat "$out")" >&2; return 1; }
  changed=reason_review_changed
  [[ "$(pg fold -c "SELECT count(*) FROM contact_requests WHERE reason = '' AND (requester_id=$t2 OR subject_user_id=$t2)")" == 0 ]]
  [[ "$(pg fold -c "SELECT deleted_at IS NULL FROM users WHERE id=$t2")" == t ]]
  [[ "$(pg fold -c "SELECT count(*) FROM audit_logs WHERE action IN ('add.reason_kept','add.reason_removed') AND target_id IN ($(jq -r '[.items[].requestId]|join(",")' "$cdir/panel-$t2.json"))")" == 0 ]]
  # The reload: the panel loads the review again. The changed item has a new hash;
  # the unchanged one keeps its hash, and the person's choices carry over.
  new_items="$(api POST "/admin/users/$t2/erasure-reasons/page" '{"after":null}' | jq -c '.items')"
  [[ "$(jq -r --argjson n "$new_items" '[.items[] as $o | $n[] | select(.requestId == $o.requestId) | ($o.reviewHash == .reviewHash)] | map(tostring) | join(",")' "$cdir/panel-$t2.json")" == false,true ]] \
    || { echo "crossing: the reload did not change exactly the changed item's hash" >&2; return 1; }
  jq --argjson n "$new_items" '.items = $n' "$cdir/panel-$t2.json" >"$cdir/panel-$t2.reloaded.json"
  status="$(crossing_press "$t2" "$cdir/panel-$t2.reloaded.json" "$out")"
  crossing_applied_exactly "$status" "$t2" "$cdir/panel-$t2.reloaded.json" "$out" \
    || { echo "crossing: the press after the reload answered $status: $(cat "$out")" >&2; return 1; }
  reloaded=applied
  # t4: an id from a process that is gone.
  old_id="$(jq -er .attemptId "$cdir/panel-$t4.json")"
  status="$(crossing_api_status DELETE "/admin/users/$t4" "$(crossing_panel_body "$cdir/panel-$t4.json" "$old_id")" "$out")"
  crossing_answer_is "$status" 409 "$out" '.code == "attempt_expired"' \
    || { echo "crossing: the old id answered $status in recovery: $(cat "$out")" >&2; return 1; }
  sh_replay=attempt_expired
  status="$(crossing_api_status POST /admin/operation-attempts/settle \
    "$(jq -nc --arg id "$old_id" --argjson s "$t4" '{attemptId:$id,kind:"account_delete",subjectUserId:$s}')" "$out")"
  crossing_answer_is "$status" 200 "$out" '.result == "history_unavailable"' \
    || { echo "crossing: settle of the old id answered $status in recovery: $(cat "$out")" >&2; return 1; }
  sh_settle=history_unavailable
  crossing_reload_shows "$t4" || { echo "crossing: the reloaded users list does not show user $t4" >&2; return 1; }
  sh_reload=true
  status="$(crossing_press "$t4" "$cdir/panel-$t4.json" "$out")"
  crossing_applied_exactly "$status" "$t4" "$cdir/panel-$t4.json" "$out" \
    || { echo "crossing: the fresh press after reload answered $status in recovery: $(cat "$out")" >&2; return 1; }
  sh_fresh=applied
  restore --reopen --checked-church-records
  wait_public; URL="$(public_url)"; login
  crossing_emit cases.jsonl --arg acceptance A-current-release-restore --argjson level "$level" --arg image "$CROSSING_CAND_IMG" \
    --argjson restartsBefore "$restarts_before" --argjson restartsAfter "$restarts_after" \
    --arg imm "$imm" --arg changed "$changed" --arg reloaded "$reloaded" --arg la "$live_answer" --arg lr "$live_replay" --arg ls "$live_settle" \
    --argjson lrl "$live_reload" --arg lf "$live_fresh" --arg sr "$sh_replay" --arg ss "$sh_settle" --argjson srl "$sh_reload" --arg sf "$sh_fresh" \
    '{kind:"positive", acceptance:$acceptance, id:"current-release-restore", fixture:false, pass:true, level:$level, image:$image,
      restartsBefore:$restartsBefore, restartsAfter:$restartsAfter,
      proof:"protocol: curl against the real server and database; the React panel (disabled Delete, Close, the users-list reload, choosing again) is covered by Vitest, Playwright and the first-church hand check, not by this case",
      scenarios:{
        noAttempt:{unchanged:$imm, changed:$changed, reloadedPress:$reloaded},
        retainedLive:{answer:$la, replay:$lr, settle:$ls, reloadShowsAccount:$lrl, freshPress:$lf},
        retainedRestoreSh:{replay:$sr, settle:$ss, reloadShowsAccount:$srl, freshPress:$sf}}}'
  reset_case "$CROSSING_CAND_IMG"
}

# ---------------------------------------------------------------------------
# Older archives restored with the candidate. id: lane-base (the table-set
# archive: D must contain admin_operation_attempts) or reason-review (the
# archive lacks T07B-2a's migration: only trigger bodies differ). rev and image
# are the archive's commit and real image.
# ---------------------------------------------------------------------------
crossing_older_archive_case() {
  local id="$1" rev="$2" image="$3"
  local cdir original_dump="$DB_DUMP" older archive_yml archive_id old_url endpoint password a_member a_req a_group
  local scratch_url a_versions table version v fence_before fence_after rid i ajson page body first kept status
  local archive_level missing before_sha archive_sha candidate_sha after_sha
  local -a d_tables=() m_versions=()
  cdir="$(crossing_dir)"
  older="$cdir/older-archive-$id.dump"
  archive_yml="$cdir/archive-$id.yml"
  password="$(env_value "$DIR/.env" DB_PASSWORD)"
  archive_level="$(commit_recovery_level "$ROOT" "$rev")"
  missing="$(crossing_migration_difference "$ROOT" "$rev" "$REVISION")"
  IFS=, read -r -a m_versions <<<"$missing"
  [[ -n "$missing" ]]
  reset_case "$CROSSING_CAND_IMG"
  drill_step "archive-$id/dumped-on-its-real-image"
  pg postgres -c 'CREATE DATABASE drill_archive_fixture OWNER church TEMPLATE template0'
  cat >"$archive_yml" <<ARCHIVE
services:
  archive:
    extends:
      file: $DIR/docker-compose.yml
      service: app
    image: $image
    pull_policy: never
    restart: "no"
    profiles: [archive]
    environment:
      DATABASE_URL: postgres://church:$password@db:5432/drill_archive_fixture?sslmode=disable
      FOLD_WORKERS: "0"
    ports:
      - "127.0.0.1::8080"
ARCHIVE
  dc -f "$DIR/docker-compose.yml" -f "$archive_yml" --profile archive up -d --no-deps archive
  archive_id="$(dc -f "$DIR/docker-compose.yml" -f "$archive_yml" --profile archive ps -q archive)"
  for ((i=0;i<90;i++)); do docker exec "$archive_id" /fold healthcheck >/dev/null 2>&1 && break; sleep 1; done
  docker exec "$archive_id" /fold healthcheck
  [[ "$(docker inspect --format '{{.Image}}' "$archive_id")" == "$(docker image inspect --format '{{.Id}}' "$image")" ]]
  ajson="$(docker exec "$archive_id" /fold version --json)"
  jq -e --argjson l "$archive_level" '((.recoveryPolicy // 0) == $l)' <<<"$ajson" >/dev/null \
    || { echo "crossing: the archive image does not report level $archive_level: $ajson" >&2; return 1; }
  old_url="$URL"
  endpoint="$(dc -f "$DIR/docker-compose.yml" -f "$archive_yml" --profile archive port archive 8080)"
  URL="http://localhost:${endpoint##*:}"
  setup
  a_member="$(new_user 'Archive Member' archive-member@example.invalid)"
  a_req="$(new_user 'Archive Requester' archive-requester@example.invalid)"
  a_group="$(api POST /groups '{"name":"Archive context","description":"Disposable fixture","visibility":"public","tags":[]}' | jq -er .group.id)"
  # Two requests with reasons, one per party direction, written by the archive's
  # own schema: the candidate must be able to review and blank them.
  pg drill_archive_fixture -c "$(crossing_reason_row_sql "$a_req" "$a_member" "$a_group" 'Archive reason, member as subject')" \
                           -c "$(crossing_reason_row_sql "$a_member" "$a_req" "$a_group" 'Archive reason, member as requester')"
  a_versions="$(pg drill_archive_fixture -c "SELECT string_agg(version_id::text,',' ORDER BY version_id) FROM goose_db_version WHERE is_applied AND version_id>0")"
  [[ "$a_versions" == "$(crossing_migration_versions "$ROOT" "$rev")" ]] \
    || { echo "crossing: the archive database is at goose versions $a_versions, not its image's migrations" >&2; return 1; }
  if [[ "$id" == lane-base ]]; then
    [[ "$(pg drill_archive_fixture -c "SELECT to_regclass('public.admin_operation_attempts') IS NULL")" == t ]]
  fi
  crossing_function_defs drill_archive_fixture >"$cdir/table-sets/$id.archive-functions.sql"
  [[ -s "$cdir/table-sets/$id.archive-functions.sql" ]]
  dc exec -T db pg_dump -U church -Fc drill_archive_fixture >"$older"
  dc exec -T db pg_restore -l <"$older" >"$cdir/table-sets/$id.archive-toc.txt"
  # The archive's own listing must not carry the objects the candidate adds.
  crossing_toc_tables <"$cdir/table-sets/$id.archive-toc.txt" >"$cdir/table-sets/$id.archive.tables"
  docker rm -f "$archive_id"
  pg postgres -c 'DROP DATABASE drill_archive_fixture'
  URL="$old_url"; login
  drill_step "archive-$id/candidate-schema"
  pg postgres -c 'CREATE DATABASE drill_candidate_schema OWNER church TEMPLATE template0'
  scratch_url="postgres://church:$password@db:5432/drill_candidate_schema?sslmode=disable"
  docker run --rm --network "${PROJECT}_default" -e "DATABASE_URL=$scratch_url" "$CROSSING_CAND_IMG" migrate up >"$cdir/table-sets/$id.candidate-migrate.log" 2>&1 \
    || { tail -n 20 "$cdir/table-sets/$id.candidate-migrate.log" >&2; return 1; }
  pg drill_candidate_schema -c "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1" | LC_ALL=C sort -u >"$cdir/table-sets/$id.candidate.tables"
  crossing_function_defs drill_candidate_schema >"$cdir/table-sets/$id.candidate-functions.sql"
  pg postgres -c 'DROP DATABASE drill_candidate_schema'
  [[ -s "$cdir/table-sets/$id.candidate-functions.sql" ]]
  archive_sha="$(sha256sum <"$cdir/table-sets/$id.archive-functions.sql" | cut -d' ' -f1)"
  candidate_sha="$(sha256sum <"$cdir/table-sets/$id.candidate-functions.sql" | cut -d' ' -f1)"
  # Both archives predate T07B-2a, so the two trigger functions must differ.
  [[ "$archive_sha" != "$candidate_sha" ]] || { echo "crossing: the archive's trigger functions already equal the candidate's; the archive would not need the migration" >&2; return 1; }
  if [[ "$id" == lane-base ]]; then
    crossing_table_difference "$cdir/table-sets/$id.archive.tables" "$cdir/table-sets/$id.candidate.tables" admin_operation_attempts >"$cdir/table-sets/$id.D.tables"
  else
    crossing_table_difference "$cdir/table-sets/$id.archive.tables" "$cdir/table-sets/$id.candidate.tables" >"$cdir/table-sets/$id.D.tables" || true
  fi
  LC_ALL=C comm -23 "$cdir/table-sets/$id.archive.tables" "$cdir/table-sets/$id.candidate.tables" >"$cdir/table-sets/$id.archive-only.tables"
  mapfile -t d_tables <"$cdir/table-sets/$id.D.tables"
  jq -n --arg id "$id" --arg archiveImage "$image" --arg archiveRevision "$rev" --argjson archiveLevel "$archive_level" \
    --arg archiveImageId "$(docker image inspect --format '{{.Id}}' "$image")" \
    --arg dumpSha256 "$(sha256sum <"$older" | cut -d' ' -f1)" --arg archiveVersions "$a_versions" --arg missing "$missing" \
    --arg archiveFunctionsSha "$archive_sha" --arg candidateFunctionsSha "$candidate_sha" \
    --rawfile archiveTables "$cdir/table-sets/$id.archive.tables" --rawfile candidateTables "$cdir/table-sets/$id.candidate.tables" \
    --rawfile d "$cdir/table-sets/$id.D.tables" --rawfile archiveOnly "$cdir/table-sets/$id.archive-only.tables" \
    '($d|split("\n")|map(select(length>0))) as $D
     | {id:$id, archiveImage:$archiveImage, archiveRevision:$archiveRevision, archiveLevel:$archiveLevel, archiveImageId:$archiveImageId,
        dumpSha256:$dumpSha256, archiveGooseVersions:$archiveVersions, migrationsMissingFromArchive:$missing,
        archiveFunctionsSha256:$archiveFunctionsSha, candidateFunctionsSha256:$candidateFunctionsSha,
        archiveTables:($archiveTables|split("\n")|map(select(length>0))),
        candidateTables:($candidateTables|split("\n")|map(select(length>0))), D:$D,
        archiveOnly:($archiveOnly|split("\n")|map(select(length>0))),
        recordedNotAssumed:{email_address_optouts:{inD:($D|index("email_address_optouts")!=null)}}}' \
    >"$cdir/older-archive-$id.json"
  drill_step "archive-$id/restore-with-candidate-image"
  DB_DUMP="$older"
  kill_point after-swap --dump "$DB_DUMP" --image "$CROSSING_CAND_IMG" --source-dir "$SOURCE"
  assert_legacy_archive_schema
  for table in "${d_tables[@]}"; do
    [[ -z "$table" ]] || [[ "$(pg fold -c "SELECT to_regclass('public.$table') IS NULL")" == t ]]
  done
  [[ "$(pg fold -c "SELECT string_agg(version_id::text,',' ORDER BY version_id) FROM goose_db_version WHERE is_applied AND version_id>0")" == "$a_versions" ]]
  # Before the recovery boot the swapped-in heap still holds the archive's trigger
  # functions byte for byte, and they are not the candidate's.
  before_sha="$(crossing_function_defs fold | sha256sum | cut -d' ' -f1)"
  [[ "$before_sha" == "$archive_sha" ]] || { echo "crossing: the swapped-in functions are not the archive's before the recovery boot" >&2; return 1; }
  restore --resume
  assert_private
  rid="$(recovery_ids)"
  docker exec "$rid" /fold migrate status --json >"$cdir/older-archive-$id-goose-status.json"
  jq -e 'type == "array" and all(.[]; .state == "applied")' "$cdir/older-archive-$id-goose-status.json" >/dev/null
  jq -e --arg v "$CROSSING_CAND_TAG" --argjson l "$CROSSING_CANDIDATE_LEVEL" '.version == $v and .recoveryPolicy == $l' <<<"$(docker exec "$rid" /fold version --json)" >/dev/null \
    || { echo "crossing: the recovery instance is not the committed head at level $CROSSING_CANDIDATE_LEVEL" >&2; return 1; }
  [[ "$(pg fold -c 'SELECT count(*) - count(DISTINCT version_id) FROM goose_db_version WHERE version_id>0')" == 0 ]]
  [[ "$(pg fold -c "SELECT string_agg(version_id::text,',' ORDER BY version_id) FROM goose_db_version WHERE is_applied AND version_id>0")" == "$(crossing_migration_versions "$ROOT" "$REVISION")" ]]
  # Exactly the migrations the archive lacks were applied, each once, and the
  # reason-review migration is among them.
  for v in "${m_versions[@]}"; do
    [[ "$(pg fold -c "SELECT count(*) FROM goose_db_version WHERE version_id=$v")" == 1 ]]
    [[ "$(pg fold -c "SELECT count(*) FROM goose_db_version WHERE version_id=$v AND is_applied")" == 1 ]]
    jq -e --argjson v "$v" '[.[] | select(.version == $v and .state == "applied")] | length == 1' "$cdir/older-archive-$id-goose-status.json" >/dev/null
  done
  jq -e --argjson v "$(crossing_reason_migration_version "$ROOT" "$REVISION")" '[.[] | select(.version == $v and .state == "applied")] | length == 1' \
    "$cdir/older-archive-$id-goose-status.json" >/dev/null
  for table in "${d_tables[@]}"; do
    [[ -n "$table" ]] || continue
    version="$(crossing_owner_migration "$ROOT" "$REVISION" "$table")" \
      || { echo "crossing: no single migration after the baseline creates $table" >&2; return 1; }
    [[ "$(pg fold -c "SELECT count(*) FROM goose_db_version WHERE version_id=$version")" == 1 ]]
    [[ "$(pg fold -c "SELECT count(*) FROM goose_db_version WHERE version_id=$version AND is_applied")" == 1 ]]
    [[ "$(pg fold -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='$table' AND c.relkind IN ('r','p')")" == 1 ]]
    jq -e --argjson v "$version" '[.[] | select(.version == $v and .state == "applied")] | length == 1' "$cdir/older-archive-$id-goose-status.json" >/dev/null
  done
  after_sha="$(crossing_function_defs fold | sha256sum | cut -d' ' -f1)"
  [[ "$after_sha" == "$candidate_sha" ]] || { echo "crossing: the trigger functions after the recovery boot are not the candidate's" >&2; return 1; }
  [[ "$(docker inspect --format '{{.Image}}' "$rid")" == "$(docker image inspect --format '{{.Id}}' "$CROSSING_CAND_IMG")" ]]
  drill_step "archive-$id/reopen-review-and-delete"
  restore --reopen --checked-church-records
  DB_DUMP="$original_dump"
  wait_public
  URL="$(public_url)"; login
  fence_before="$(pg fold -c "SELECT value::bigint FROM settings WHERE key='restore_fence'")"
  [[ "$fence_before" =~ ^[0-9]+$ ]]
  # The reopened app serves the review route over the archive's own rows.
  page="$(api POST "/admin/users/$a_member/erasure-reasons/page" '{"after":null}')"
  jq -e '.reviewRequired == true and (.items | length) == 2 and .next == null and (.items | all(.reviewHash | test("^[0-9a-f]{64}$")))' <<<"$page" >/dev/null \
    || { echo "crossing: the reopened app did not serve the two-item review: $page" >&2; return 1; }
  first="$(jq -er '.items[0].requestId' <<<"$page")"
  kept="$(jq -er '.items[1].requestId' <<<"$page")"
  body="$(jq -c '{reasonReview:{items:[.items | to_entries[] | {requestId:.value.requestId, reviewHash:.value.reviewHash, choice:(if .key == 0 then "remove" else "keep" end)}]}}' <<<"$page")"
  # Blanking a reason on a soft-deleted party is the migration's own branch: it
  # succeeds only if the trigger functions are the candidate's.
  api DELETE "/admin/users/$a_member" "$body" >"$cdir/older-archive-$id-delete.json"
  jq -e '.erasure.reasonsRemoved == 1 and .erasure.reasonsKept == 1' "$cdir/older-archive-$id-delete.json" >/dev/null
  [[ "$(pg fold -c "SELECT deleted_at IS NOT NULL FROM users WHERE id=$a_member")" == t ]]
  [[ "$(pg fold -c "SELECT reason FROM contact_requests WHERE id=$first")" == '' ]]
  [[ "$(pg fold -c "SELECT reason <> '' FROM contact_requests WHERE id=$kept")" == t ]]
  [[ "$(pg fold -c "SELECT count(*) FROM audit_logs WHERE action IN ('add.reason_kept','add.reason_removed') AND target_id IN ($first,$kept)")" == 2 ]]
  fence_after="$(pg fold -c "SELECT value::bigint FROM settings WHERE key='restore_fence'")"
  (( fence_after > fence_before )) || { echo "crossing: the delete did not advance the restore fence ($fence_before -> $fence_after)" >&2; return 1; }
  ajson="$(dc exec -T app /fold version --json)"
  jq -e --arg v "$CROSSING_CAND_TAG" --argjson l "$CROSSING_CANDIDATE_LEVEL" '.version == $v and .recoveryPolicy == $l' <<<"$ajson" >/dev/null \
    || { echo "crossing: the reopened app is not the committed head at level $CROSSING_CANDIDATE_LEVEL" >&2; return 1; }
  [[ "$(docker inspect --format '{{.Image}}' "$(dc ps -q app)")" == "$(docker image inspect --format '{{.Id}}' "$CROSSING_CAND_IMG")" ]]
  status="served"
  crossing_emit cases.jsonl --arg acceptance A-older-archive --slurpfile record "$cdir/older-archive-$id.json" \
    --argjson before "$fence_before" --argjson after "$fence_after" --arg review "$status" \
    '{kind:"positive", acceptance:$acceptance, id:("older-archive-" + $record[0].id), fixture:false, pass:true, archive:$record[0],
      fenceBefore:$before, fenceAfter:$after, reviewRoute:$review, reasonsBlankedOnDeletedParty:true}'
  reset_case "$CROSSING_CAND_IMG"
}

real_older_archive_restore() {
  crossing_prepare
  crossing_use_main
  crossing_older_archive_case lane-base "$LANE_REVISION" "$CROSSING_LANE_IMG"
  crossing_older_archive_case reason-review "$ARCHIVE_BASE_REVISION" "$CROSSING_ARCHIVE_IMG"
}

# Final check for the runner's last case: every acceptance is present, passed,
# and the revisions and images are on record.
assert_crossing_evidence() {
  local cdir
  cdir="$(crossing_dir)"
  crossing_evidence_complete "$cdir/cases.jsonl" "$cdir/images.jsonl" || return 1
  [[ -s "$cdir/revisions.json" && -s "$cdir/older-archive-lane-base.json" && -s "$cdir/older-archive-reason-review.json" ]]
  [[ -s "$cdir/lineage-l0-build-binary.json" && -s "$cdir/lineage-l1-build-binary.json" ]]
  jq -e '.applied != null and .path == "/etc/os-release"' "$cdir/os-release-standin.json" >/dev/null
}

# ---------------------------------------------------------------------------
# restore-drill.sh --crossings-only: a PARTIAL DIAGNOSTIC, never the acceptance run.
#
# The full drill takes about an hour; the five real-binary crossing cases come last, so a deterministic runner
# problem in them (a missing tool, an OS check, a path) costs a whole run to find. This mode runs exactly what
# those cases need and nothing else:
#   bootstrap          the same deploy-drill.sh --prepare-only stack as the full run
#   base-prerequisites the full run's base-proof fixture setup only: setup wizard, users, group, jobs and
#                      notifications, and the pg_dump that reset_case restores from (DB_DUMP)
#   the five real_* families, called exactly as the full run calls them (bare, errexit on)
#   crossings-evidence assert_crossing_evidence, the same check the full run ends with
# It skips the restore proofs themselves (swap proof, restore --dump, the 60 s observation, the db restart,
# the Chromium delete-panel proof), the interruption matrices and the twelve lifecycle/review families, the
# statement-history check, and Firefox (its record is not required). The Chromium preflight still runs.
#
# It cannot be mistaken for the acceptance run: the progress plan has other case names and never
# the full plan; every progress record carries mode=crossings-only, partial=true, fullAcceptance=false; the
# result is "partial", never "complete"; WORK holds PARTIAL-NOT-FULL-ACCEPTANCE.txt and drill-mode.json
# (the full run writes neither); the banner and the last line say so; and a pass exits with
# DRILL_PARTIAL_PASS_EXIT (10), which the full run never returns (its pass is exit 0).
# ---------------------------------------------------------------------------
CROSSINGS_ONLY_FAMILIES=(real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore)
CROSSINGS_ONLY_PLAN=(bootstrap base-prerequisites "${CROSSINGS_ONLY_FAMILIES[@]}" crossings-evidence)
# The full run's case names, in order. restore-drill-modes-fixtures.sh keeps this equal to the plan restore-drill.sh passes to drill_progress_init.
DRILL_FULL_PLAN_NAMES=(bootstrap base-proof recovery-interruptions reopen-interruptions abort-interruptions tamper accidental-app-reuse abort-ack-loss operations-lock recovery-configuration
  count_races archive_failures pin_and_abort_cases cleanup_and_tamper_cases concurrency_cases lifecycle_cases legacy_archive_case review_reconciliation_cases
  review_backup_archive_cases review_cleanup_retry_cases review_orphaned_restore_case review_moving_tag_case
  real_binary_refusals real_level_lineage real_same_level_rollback real_current_release_restore real_older_archive_restore final-verification)

drill_mode_banner() {
  printf '%s\n' '################################################################################' \
    '# restore-drill.sh --crossings-only: PARTIAL DIAGNOSTIC. This is NOT the full drill and' \
    '# NOT acceptance. It runs the bootstrap, the fixture prerequisites and the five real' \
    '# level-2 crossing cases only. A pass exits 10; only a full run exits 0.' \
    '################################################################################'
}

# Writes the markers into WORK before the first case. The full run writes none of these.
drill_mode_begin() {
  local skipped ran
  ran="$(printf '%s\n' "${CROSSINGS_ONLY_PLAN[@]}" | jq -Rn '[inputs]')"
  skipped="$(printf '%s\n' "${DRILL_FULL_PLAN_NAMES[@]}" | jq -Rn --argjson ran "$ran" '[inputs] - $ran | map(select(. != "base-proof"))')"
  drill_mode_banner
  printf '%s\n' 'PARTIAL DIAGNOSTIC (restore-drill.sh --crossings-only). This directory is NOT full-drill evidence and NOT acceptance.' \
    'It holds the bootstrap, the fixture prerequisites and the five real level-2 crossing cases only. See drill-mode.json for what ran and what did not.' \
    'A pass is exit 10 and status "partial". Acceptance needs a full run (exit 0, status "complete").' >"$WORK/PARTIAL-NOT-FULL-ACCEPTANCE.txt"
  jq -n --argjson ran "$ran" --argjson skipped "$skipped" --argjson pass "$DRILL_PARTIAL_PASS_EXIT" --arg at "$(date -u +%FT%TZ)" --arg revision "${REVISION:-}" \
    '{mode:"crossings-only", partial:true, fullAcceptance:false, startedAt:$at, revision:$revision, passExitCode:$pass,
      purpose:"find deterministic runner failures in the real-binary crossing cases without the full drill",
      ran:$ran, base:"base-prerequisites runs only the fixture setup and the pg_dump of the full run base-proof",
      skippedFromFull:$skipped,
      skippedFromBaseProof:["the swap proof","restore --dump","the 60 s closed observation","the db restart","the Chromium delete-panel proof","reopen","the queued-work checks"],
      notProved:["any recovery interruption, tamper, race, lifecycle, archive-failure or review case","restore.sh behavior outside the crossing cases","the Chromium and Firefox delete-panel proofs","the statement-history check","that the full drill passes"],
      browser:{chromiumPreflight:"run", firefox:"not prepared or required in this mode"},
      result:null, outcome:null}' >"$WORK/drill-mode.json"
}

# The cases. Called bare, so errexit stays on in every acceptance function; the base-prerequisites case was ended by the caller.
crossings_only_run() {
  local family
  for family in "${CROSSINGS_ONLY_FAMILIES[@]}"; do
    drill_case_begin "$family"
    # A bare call preserves errexit inside the acceptance function. Never put this call in an if/! or || list.
    "$family"
    drill_case_end
  done
  drill_case_begin crossings-evidence
  assert_crossing_evidence
  drill_case_end
  drill_progress_complete
  printf 'restore drill PARTIAL (crossings-only) passed: revision=%s project=%s artifacts=%s. NOT the full drill, NOT acceptance (exit %s).\n' \
    "$(jq -er .revision "$MANIFEST")" "$PROJECT" "$WORK" "$DRILL_PARTIAL_PASS_EXIT"
  exit "$DRILL_PARTIAL_PASS_EXIT"
}

# Called from the EXIT trap in a partial run: records how it ended in drill-mode.json and repeats the label on the last lines.
drill_mode_finish() { # rc
  local rc="$1" outcome=failed tmp
  (( rc != DRILL_PARTIAL_PASS_EXIT )) || outcome=partial-pass
  if [[ -s "$WORK/drill-mode.json" ]]; then
    tmp="$WORK/.drill-mode.$$.tmp"
    if jq --argjson rc "$rc" --arg outcome "$outcome" '.result = $rc | .outcome = $outcome' "$WORK/drill-mode.json" >"$tmp"; then mv -f -- "$tmp" "$WORK/drill-mode.json"; else rm -f -- "$tmp"; fi
  fi
  printf 'restore drill MODE=crossings-only outcome=%s exit=%s: PARTIAL diagnostic, NOT full acceptance (only a full run exiting 0 is acceptance)\n' "$outcome" "$rc"
}

# The exit code a partial run may report. 10 only when every planned case ended (a failing command inside a case can exit 10 by chance), and never 0:
# 0 is reserved for the full run's pass.
drill_partial_rc() { # rc
  local rc="$1"
  if (( rc == 0 )); then echo 1
  elif (( rc == DRILL_PARTIAL_PASS_EXIT )) && [[ -z "${RESTORE_DRILL_CASE:-}" && "$(jq -c . <<<"$DRILL_PROGRESS_DONE")" == "$(jq -c . <<<"$DRILL_PROGRESS_PLAN")" ]]; then echo "$rc"
  elif (( rc == DRILL_PARTIAL_PASS_EXIT )); then echo 1
  else echo "$rc"; fi
}
