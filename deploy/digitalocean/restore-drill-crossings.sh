#!/usr/bin/env bash
# R4-6c-V: real-binary recovery-level crossings and the older-archive restore.
# Sourced only by restore-drill.sh (and by restore-crossings-fixtures.sh, which
# tests the pure functions without a daemon). Nothing here changes a shipped
# script: it builds real images from real commits, pushes them to the drill
# registry the runner already owns, and drives the installed entry points.
#
# Every image is built from `git archive <sha>` of a commit and must report the
# FOLD_VERSION it was built with, so a binary is tied to its commit. The
# candidate is the committed head (the runner refuses a dirty tree).
#
# Cases, each failing the runner on any deviation:
#   real_binary_refusals        The main (floor 1) site runs the real candidate
#                               image. upgrade.sh --commit/--image and
#                               restore.sh --commit/--image toward LANE_BASE
#                               (level 0), each from the installed copy and from
#                               the candidate tool copy, must exit 1 naming the
#                               restore barrier, reach no backup/record/checkout/
#                               pin/stop/swap step in an xtrace, and leave
#                               snapshot_state byte-identical. Two further rows
#                               per rollback kind are SUPPLEMENTARY FIXTURE rows
#                               (S-*): a same-level release.prev is rewritten to
#                               name LANE_BASE.
#   real_level0_lineage         Two fresh sites, separate from the main one. Each
#                               is first a genuine level-0 host: LANE_BASE's own
#                               deploy tools provision LANE_BASE's image (image
#                               mode) or commit (build mode, compiled on the box).
#                               The candidate's tools then upgrade it for real
#                               (image, or the committed head compiled from its own
#                               Dockerfile). Smoke passes, the floor reads 1 and
#                               release.prev names LANE_BASE naturally. Then
#                               upgrade.sh --rollback, from the installed copy and
#                               from the candidate tool copy, must be refused as
#                               above (R-rollback-genuine). The build-mode site,
#                               running the genuine build at the head, also takes
#                               the full forward and restore refusal matrix.
#   real_same_level_rollback    Real T07B-1-V landing image deployed, forward to
#                               the candidate image, then upgrade.sh --rollback:
#                               succeeds, runs restore.sh from DIR/recovery-tools,
#                               leaves DIR and DIR/deploy-tools at the candidate's
#                               bytes, and the recovery instance and the reopened
#                               app run one image id that reports recoveryPolicy 1.
#   real_older_archive_restore  An archive dumped on LANE_BASE's real image; its
#                               table set (pg_restore -l) is compared with a
#                               scratch database migrated by the candidate; the
#                               difference D must contain admin_operation_attempts;
#                               the candidate recovery boot creates every table in
#                               D exactly once (goose), recovery and --reopen
#                               succeed, and the reopened app answers a fenced
#                               delete.
#
# Recorded under $WORK/crossings: revisions.json, images.jsonl, tools.jsonl,
# cases.jsonl, cases/<id>/{cmd,trace,state.before,state.after}, table-sets/*,
# older-archive.json, lineage-*.json.
# shellcheck disable=SC2034,SC2153,SC2154,SC2016
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then echo 'source from restore-drill.sh only' >&2; exit 1; fi

# LANE_BASE is origin/master's recorded tip (state preflight.base): a level-0
# release with migrations 00001-00004. It is both the refusal target and the
# base of the older archive, so D holds every table added since.
CROSSING_PREFLIGHT_BASE=8c07c85265c0f43ab8350cd945efdc14363c21bd
CROSSING_LANE_DEFAULT="$CROSSING_PREFLIGHT_BASE"
CROSSING_ARCHIVE_DEFAULT="$CROSSING_PREFLIGHT_BASE"
CROSSING_LANDING_DEFAULT=d400b7755f4564c60524167105f31e33bed9b51b
CROSSING_DOMAIN=fold-drill.localhost
CROSSING_REFUSAL_RE='^check: target recovery level 0 is below host level 1\. The older release lacks the restore barrier \(level 1\)\.'
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
# the commits themselves before any image is built. LANE_BASE and the archive
# base may be the same commit. Prints every problem.
crossing_validate_revisions() {
  local src="$1" lane="$2" landing="$3" archive="$4" head="$5"
  local role sha level bad=0 lv hv
  for role in lane landing archive; do
    case "$role" in lane) sha="$lane" ;; landing) sha="$landing" ;; *) sha="$archive" ;; esac
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
  if [[ "$level" != 1 ]]; then echo "crossing: the candidate $head is recovery level $level, not 1" >&2; bad=1; fi
  level="$(commit_recovery_level "$src" "$lane")" || return 1
  if [[ "$level" != 0 ]]; then echo "crossing: LANE_BASE $lane is recovery level $level, not a level-0 release" >&2; bad=1; fi
  level="$(commit_recovery_level "$src" "$archive")" || return 1
  if [[ "$level" != 0 ]]; then echo "crossing: archive base $archive is recovery level $level, not level 0" >&2; bad=1; fi
  level="$(commit_recovery_level "$src" "$landing")" || return 1
  if [[ "$level" != 1 ]]; then echo "crossing: landing $landing is recovery level $level, not level 1" >&2; bad=1; fi
  if git -C "$src" cat-file -e "$landing:deploy/digitalocean/restore.sh" 2>/dev/null; then
    echo "crossing: landing $landing already has restore.sh; it must precede the restore tooling" >&2; bad=1
  fi
  if git -C "$src" grep -q -e admin_operation_attempts "$archive" -- migrations/ 2>/dev/null; then
    echo "crossing: archive base $archive already has admin_operation_attempts; the table difference D would be empty" >&2; bad=1
  fi
  lv="$(crossing_migration_versions "$src" "$landing")" || return 1
  hv="$(crossing_migration_versions "$src" "$head")" || return 1
  if [[ "$lv" != "$hv" ]]; then
    echo "crossing: landing migrations ($lv) differ from the candidate's ($hv); a same-schema rollback is impossible" >&2; bad=1
  fi
  (( bad == 0 ))
}

# public tables named by a pg_restore -l listing ("TABLE DATA" entries excluded).
crossing_toc_tables() { awk '$4=="TABLE" && $5=="public" {print $6}' | LC_ALL=C sort -u; }

# D: tables the candidate has and the archive lacks. Fails unless D contains
# admin_operation_attempts. Other members are recorded by the caller, not assumed.
crossing_table_difference() {
  local archive="$1" candidate="$2" d
  d="$(LC_ALL=C comm -13 "$archive" "$candidate")" || return 1
  if ! grep -qx admin_operation_attempts <<<"$d"; then
    echo 'crossing: the table difference D lacks admin_operation_attempts' >&2
    return 1
  fi
  printf '%s\n' "$d"
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
# restore-commit or restore-image. Extra arguments name provisioner paths that
# must never appear in the trace (a checkout's provision.sh is data, never a tool).
crossing_problems() {
  local kind="$1" rc="$2" trace="$3" before="$4" after="$5" path; shift 5
  if [[ "$rc" != 1 ]]; then echo "exit code $rc, want 1"; fi
  if ! grep -Eq "$CROSSING_REFUSAL_RE" "$trace"; then echo 'refusal message naming the restore barrier is missing'; fi
  if ! cmp -s "$before" "$after"; then echo 'snapshot_state changed'; fi
  if ! grep -Eq '^\++ require_recovery_level 0 1( |$)' "$trace"; then echo 'refusal did not come from require_recovery_level 0 1'; fi
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
# Acceptance rows (R-, A-) are never fixtures; S- rows always are.
crossing_evidence_complete() {
  local cases="$1" images="$2"
  [[ -s "$cases" && -s "$images" ]] || { echo 'crossing: evidence files are missing or empty' >&2; return 1; }
  jq -s -e '
    def count($a): [.[] | select(.acceptance == $a)] | length;
    def passed($a): [.[] | select(.acceptance == $a and .pass == true)] | length;
    def want: {"R-forward-commit": 4, "R-forward-image": 4, "R-restore-commit": 4, "R-restore-image": 4,
               "R-rollback-genuine": 4, "S-rollback-commit-record": 2, "S-rollback-image-record": 2,
               "A-lineage-upgrade": 2, "A-same-level-rollback": 1, "A-older-archive": 1};
    . as $all
    | (want | to_entries | all(.key as $k | .value as $n | ($all | count($k)) == $n and ($all | passed($k)) == $n))
    and ([.[] | .acceptance] | all(. as $a | want | has($a)))
    and ([.[] | select(.kind == "refusal") | .deployed] | unique) == ["build", "image"]
    and ([.[] | select(.kind == "refusal") | .exitCode] | all(. == 1))
    and ([.[] | select(.acceptance | startswith("S-")) | .fixture] | all(. == true))
    and ([.[] | select(.acceptance | startswith("R-") or startswith("A-")) | .fixture // false] | all(. == false))
    and ([.[] | select(.acceptance == "R-rollback-genuine") | .deployed] | unique) == ["build", "image"]
    and ([.[] | select(.acceptance == "R-rollback-genuine") | .site] | unique) == ["lineage-build", "lineage-image"]
    and ([.[] | select(.acceptance == "R-forward-commit") | .site] | unique) == ["lineage-build", "main"]
    and ([.[] | select(.acceptance == "A-lineage-upgrade") | .mode] | unique) == ["build", "image"]
  ' "$cases" >/dev/null || { echo 'crossing: refusal/positive cases are incomplete or failed' >&2; return 1; }
  jq -s -e '
    ([.[] | .role] | sort) == ["archive", "candidate", "landing", "lane"]
    and (map(select(.role == "candidate" or .role == "landing")) | all(.level == 1))
    and (map(select(.role == "lane" or .role == "archive")) | all(.level == 0))
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

# Everything expensive or global happens once, in the first crossing case.
crossing_prepare() {
  local cdir f
  [[ -z "${CROSSING_READY:-}" ]] || return 0
  cdir="$(crossing_dir)"
  mkdir -p "$cdir/cases" "$cdir/table-sets" "$cdir/sites"
  : >"$cdir/cases.jsonl"; : >"$cdir/images.jsonl"; : >"$cdir/tools.jsonl"
  TOOLS_DIR="$WORK/tools"
  [[ -f "$TOOLS_DIR/upgrade.sh" && -f "$TOOLS_DIR/restore.sh" ]] || { echo "crossing: candidate tool copy missing at $TOOLS_DIR" >&2; return 1; }
  crossing_validate_revisions "$ROOT" "$LANE_REVISION" "$LANDING_REVISION" "$ARCHIVE_BASE_REVISION" "$REVISION" || return 1
  crossing_require_clean_head "$ROOT" || return 1
  jq -n --arg candidate "$REVISION" --arg lane "$LANE_REVISION" --arg landing "$LANDING_REVISION" \
    --arg archive "$ARCHIVE_BASE_REVISION" --arg preflight "$CROSSING_PREFLIGHT_BASE" \
    --arg archiveVersions "$(crossing_migration_versions "$ROOT" "$ARCHIVE_BASE_REVISION")" \
    --arg laneVersions "$(crossing_migration_versions "$ROOT" "$LANE_REVISION")" \
    --arg candidateVersions "$(crossing_migration_versions "$ROOT" "$REVISION")" \
    '{candidate:$candidate, candidateTreeClean:true, laneBase:$lane, landing:$landing, archiveBase:$archive,
      recordedPreflightBase:$preflight, laneBaseIsPreflightBase:($lane == $preflight),
      archiveBaseIsPreflightBase:($archive == $preflight),
      migrationVersions:{archiveBase:$archiveVersions, laneBase:$laneVersions, candidate:$candidateVersions}}' \
    >"$cdir/revisions.json"
  jq -e '.laneBaseIsPreflightBase and .archiveBaseIsPreflightBase' "$cdir/revisions.json" >/dev/null \
    || echo 'crossing: note: LANE_BASE or the archive base differs from the recorded preflight base (recorded in revisions.json)' >&2
  CROSSING_HOST_BEFORE=()
  for f in "${CROSSING_HOST_FILES[@]}"; do [[ ! -e "$f" ]] || CROSSING_HOST_BEFORE+=("$f"); done
  CROSSING_HOST_RECORDED=1
  crossing_expose_objects
  # The candidate is the committed head, built from its own archive. A --image
  # given to the runner serves only the fixture stack; it is never used here.
  CROSSING_CAND_IMG="$(crossing_build_image candidate "$REVISION" 1 1)"
  CROSSING_CAND_TAG="${CROSSING_CAND_IMG##*:}"
  CROSSING_LANE_IMG="$(crossing_build_image lane "$LANE_REVISION" 0 1)"
  CROSSING_LANE_TAG="${CROSSING_LANE_IMG##*:}"
  CROSSING_LANDING_IMG="$(crossing_build_image landing "$LANDING_REVISION" 1 1)"
  CROSSING_LANDING_TAG="${CROSSING_LANDING_IMG##*:}"
  if [[ "$ARCHIVE_BASE_REVISION" == "$LANE_REVISION" ]]; then
    # One build serves both roles; both are recorded.
    CROSSING_ARCHIVE_IMG="$CROSSING_LANE_IMG"
    crossing_image_facts archive "$ARCHIVE_BASE_REVISION" "$CROSSING_ARCHIVE_IMG" 0 reused-from-lane
  else
    CROSSING_ARCHIVE_IMG="$(crossing_build_image archive "$ARCHIVE_BASE_REVISION" 0 0)"
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
  git -C "$SOURCE" update-ref refs/drill/archive-base "$ARCHIVE_BASE_REVISION"
  [[ "$(commit_recovery_level "$SOURCE" "$LANE_REVISION")" == 0 ]]
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

# The candidate's tool copy is the working tree (proved clean) and the level-0
# copy is LANE_BASE's own tools; each is checked byte for byte against git.
crossing_prepare_tools() {
  local cdir f blob cur same
  cdir="$(crossing_dir)"
  CROSSING_TOOLS_CAND="$cdir/tools-cand"; CROSSING_TOOLS_LANE="$cdir/tools-lane"
  mkdir "$CROSSING_TOOLS_CAND" "$CROSSING_TOOLS_LANE"
  for f in $(runtime_tools); do cp -p "$ROOT/deploy/digitalocean/$f" "$CROSSING_TOOLS_CAND/$f"; done
  for f in provision.sh upgrade.sh checks.sh backup.sh smoke.sh email-dns.sh backup.env.example; do
    git -C "$ROOT" show "$LANE_REVISION:deploy/digitalocean/$f" >"$CROSSING_TOOLS_LANE/$f"
  done
  for f in "$CROSSING_TOOLS_CAND"/* "$CROSSING_TOOLS_LANE"/*; do
    [[ "$f" != *.sh ]] || chmod 755 "$f"
  done
  for f in "$CROSSING_TOOLS_CAND"/*; do
    blob="$(git -C "$ROOT" rev-parse "$REVISION:deploy/digitalocean/${f##*/}")"
    cur="$(git hash-object "$f")"
    if [[ "$blob" == "$cur" ]]; then same=true; else same=false; fi
    crossing_emit tools.jsonl --arg label candidate --arg file "${f##*/}" --arg blob "$blob" --argjson identical "$same" \
      '{label:$label, file:$file, gitBlob:$blob, identicalToGit:$identical}'
    [[ "$same" == true ]] || { echo "crossing: candidate tool ${f##*/} differs from git" >&2; return 1; }
  done
  for f in "$CROSSING_TOOLS_LANE"/*; do
    blob="$(git -C "$ROOT" rev-parse "$LANE_REVISION:deploy/digitalocean/${f##*/}")"
    cur="$(git hash-object "$f")"
    if [[ "$blob" == "$cur" ]]; then same=true; else same=false; fi
    crossing_emit tools.jsonl --arg label lane --arg file "${f##*/}" --arg blob "$blob" --argjson identical "$same" \
      '{label:$label, file:$file, gitBlob:$blob, identicalToGit:$identical}'
    [[ "$same" == true ]] || { echo "crossing: LANE_BASE tool ${f##*/} differs from git" >&2; return 1; }
  done
  crossing_write_smoke "$CROSSING_TOOLS_CAND/smoke.sh"
  crossing_write_smoke "$CROSSING_TOOLS_LANE/smoke.sh"
  crossing_emit tools.jsonl --arg label both --arg file smoke.sh '{label:$label, file:$file, replacedByDrill:true}'
}

# The main site runs the real candidate image, at the version the build named.
crossing_deploy_main() {
  reset_case "$CROSSING_CAND_IMG"
  crossing_use_main
  crossing_assert_deployed "$CROSSING_CAND_TAG"
}
crossing_assert_deployed() {
  local want="$1" json
  json="$(crossing_cdc exec -T app /fold version --json)"
  jq -e --arg v "$want" '.version == $v and ((.recoveryPolicy // 0) == 1)' <<<"$json" >/dev/null \
    || { echo "crossing: the deployed app is not the real level-1 build $want: $json" >&2; return 1; }
  [[ "$(cat "$CX_DIR/recovery-policy-floor")" == 1 ]]
}

# One refused invocation, run under xtrace from the entry point named by the
# caller. Fails the runner unless crossing_problems finds nothing.
crossing_refuse() {
  local deployed="$1" acceptance="$2" kind="$3" label="$4"; shift 4
  local -a cmd=("$@")
  local cdir id rc=0 start=$SECONDS problems msg ok=true
  CROSSING_CASE=$((CROSSING_CASE+1))
  id="$(printf '%s-%02d-%s' "$CX_SITE" "$CROSSING_CASE" "$label")"
  cdir="$(crossing_dir)/cases/$id"
  mkdir -p "$cdir"
  { printf '%q ' "${cmd[@]}"; printf '\n'; } >"$cdir/cmd"
  drill_note "crossing/$id"
  snapshot_state "$CX_DIR" "$CX_SOURCE" >"$cdir/state.before"
  timeout --kill-after=10 300 env SHELLOPTS=xtrace bash -x "${cmd[@]}" >"$cdir/trace" 2>&1 </dev/null || rc=$?
  snapshot_state "$CX_DIR" "$CX_SOURCE" >"$cdir/state.after"
  problems="$(crossing_problems "$kind" "$rc" "$cdir/trace" "$cdir/state.before" "$cdir/state.after" "$CX_SOURCE/deploy/digitalocean/provision.sh")"
  [[ -z "$problems" ]] || ok=false
  msg="$(grep -m1 -E '^check: ' "$cdir/trace" || true)"
  [[ ! -e "$WORK/bypass-marker" ]] || { ok=false; problems+=$'\nthe fixture provisioner ran'; }
  crossing_emit cases.jsonl --arg kind refusal --arg acceptance "$acceptance" --arg deployed "$deployed" --arg site "$CX_SITE" \
    --argjson fixture "$CX_FIXTURE" --arg id "$id" --arg entry "${cmd[0]}" --arg command "$(cat "$cdir/cmd")" \
    --argjson exitCode "$rc" --arg message "$msg" --argjson pass "$ok" --argjson seconds "$((SECONDS-start))" \
    --arg before "$(sha256sum <"$cdir/state.before" | cut -d' ' -f1)" --arg after "$(sha256sum <"$cdir/state.after" | cut -d' ' -f1)" \
    --arg trace "cases/$id/trace" --arg problems "$problems" \
    '{kind:$kind, acceptance:$acceptance, deployed:$deployed, site:$site, fixture:$fixture, id:$id, entry:$entry, command:$command,
      exitCode:$exitCode, message:$message, pass:$pass, seconds:$seconds, snapshotBeforeSha256:$before,
      snapshotAfterSha256:$after, trace:$trace, problems:$problems}'
  if [[ "$ok" != true ]]; then
    printf 'problem: %s\n' "$problems" >&2
    echo "crossing: $id did not behave as accepted (see $cdir)" >&2
    return 1
  fi
  echo "crossing: ok $id (rc=$rc): $msg"
}

# Forward and restore refusals toward LANE_BASE on the site CX_* names.
crossing_forward_restore_matrix() {
  local deployed="$1" i
  local -a common=(--deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE")
  local -a ups=("$CX_DIR/upgrade.sh" "$CX_TOOLS/upgrade.sh") rst=("$CX_DIR/restore.sh" "$CX_TOOLS/restore.sh")
  local -a names=(installed candidate-tools)
  for i in 0 1; do
    crossing_refuse "$deployed" R-forward-commit forward-commit "forward-commit.${names[i]}" "${ups[i]}" "${common[@]}" --commit "$CROSSING_LANE"
  done
  for i in 0 1; do
    crossing_refuse "$deployed" R-forward-image forward-image "forward-image.${names[i]}" "${ups[i]}" "${common[@]}" --image "$CROSSING_LANE_IMG"
  done
  for i in 0 1; do
    crossing_refuse "$deployed" R-restore-commit restore-commit "restore-commit.${names[i]}" "${rst[i]}" --deploy-dir "$CX_DIR" --drill \
      --dump "$CX_DUMP" --source-dir "$CX_SOURCE" --commit "$CROSSING_LANE"
  done
  for i in 0 1; do
    crossing_refuse "$deployed" R-restore-image restore-image "restore-image.${names[i]}" "${rst[i]}" --deploy-dir "$CX_DIR" --drill \
      --dump "$CX_DUMP" --source-dir "$CX_SOURCE" --image "$CROSSING_LANE_IMG"
  done
  [[ "$(cat "$CX_DIR/recovery-policy-floor")" == 1 ]]
  [[ ! -e "$CX_DIR/recovery.lock" ]]
}

# SUPPLEMENTARY FIXTURE rows (S-*). A real same-level forward upgrade gives a
# genuine release.prev and dump; its previous release is then rewritten to name
# LANE_BASE, the way deploy-drill.sh's own legacy rollback refusal does. These
# prove only that the level check reads the record; the natural records are the
# R-rollback-genuine rows of the lineage sites.
crossing_record_release() {
  local mode="$1" image="$2" commit="$3"
  [[ -f "$CX_DIR/release.prev" ]] || { echo 'crossing: no release.prev to rewrite' >&2; return 1; }
  sed -i -e "s|^mode=.*|mode=$mode|" -e "s|^previous_image=.*|previous_image=$image|" -e "s|^previous_commit=.*|previous_commit=$commit|" "$CX_DIR/release.prev"
  chmod 600 "$CX_DIR/release.prev"
}
crossing_supplementary_rollback_rows() {
  local i log
  local -a common=(--deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE")
  local -a ups=("$CX_DIR/upgrade.sh" "$CX_TOOLS/upgrade.sh") names=(installed candidate-tools)
  log="$(crossing_dir)/prepare-supplementary-rollback.log"
  "$CX_DIR/upgrade.sh" "${common[@]}" --image "$CROSSING_CAND_IMG" >"$log" 2>&1 || { tail -n 40 "$log" >&2; return 1; }
  wait_public
  crossing_assert_deployed "$CROSSING_CAND_TAG"
  CX_FIXTURE=true
  crossing_record_release build "" "$CROSSING_LANE"
  for i in 0 1; do
    crossing_refuse image S-rollback-commit-record rollback-commit "rollback-commit-record.${names[i]}" "${ups[i]}" "${common[@]}" --rollback
  done
  crossing_record_release image "$CROSSING_LANE_IMG" ""
  for i in 0 1; do
    crossing_refuse image S-rollback-image-record rollback-image "rollback-image-record.${names[i]}" "${ups[i]}" "${common[@]}" --rollback
  done
  CX_FIXTURE=false
}

real_binary_refusals() {
  crossing_prepare
  drill_step refusals/main-image
  crossing_deploy_main
  crossing_forward_restore_matrix image
  crossing_supplementary_rollback_rows
  reset_case
}

# ---------------------------------------------------------------------------
# Genuine level-0 lineage on fresh sites (production Compose and Caddy per
# commit, the same method as the T07B-1-V drill).
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

crossing_site_begin() {
  local name="$1" start="$2" base
  base="$(crossing_dir)/sites/$name"
  mkdir -p "$base/site"; chmod 755 "$base/site"
  CX_SITE="lineage-$name"; CX_DIR="$base/site"; CX_SOURCE="$base/src"; CX_LANE_SRC="$base/lane-src"
  CX_TOOLS="$CROSSING_TOOLS_CAND"; CX_FIXTURE=false
  CX_PROJECT="fold-crossing-$name"
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
  crossing_checkout "$CX_LANE_SRC" "$LANE_REVISION"
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

crossing_lineage_site() {
  local mode="$1" cdir trace rc=0 json cid image_id built_id lane_json deployed kind cand_bin img_bin f i
  local -a prov args ups names
  cdir="$(crossing_dir)"
  crossing_site_prereqs
  if [[ "$mode" == image ]]; then crossing_site_begin image "$REVISION"; else crossing_site_begin build "$LANE_REVISION"; fi
  deployed="$mode"
  # 1. A genuine level-0 host: LANE_BASE's own tools, source and image.
  prov=("$CROSSING_TOOLS_LANE/provision.sh" --skip-harden --allow-os --domain "$CROSSING_DOMAIN" --deploy-dir "$CX_DIR")
  if [[ "$mode" == image ]]; then prov+=(--source-dir "$CX_LANE_SRC" --image "$CROSSING_LANE_IMG")
  else prov+=(--source-dir "$CX_SOURCE" --allow-onbox-build); fi
  timeout --kill-after=15 2700 "${prov[@]}" >"$cdir/lineage-$mode-lane-provision.log" 2>&1 </dev/null || rc=$?
  (( rc == 0 )) || { tail -n 40 "$cdir/lineage-$mode-lane-provision.log" >&2; echo "crossing: LANE_BASE provision failed (rc=$rc)" >&2; return 1; }
  lane_json="$(crossing_cdc exec -T app /fold version --json)"
  jq -e '(has("recoveryPolicy") | not) or .recoveryPolicy == 0' <<<"$lane_json" >/dev/null \
    || { echo "crossing: the LANE_BASE host is not level 0: $lane_json" >&2; return 1; }
  if [[ "$mode" == image ]]; then jq -e --arg v "$CROSSING_LANE_TAG" '.version == $v' <<<"$lane_json" >/dev/null
  else [[ "$(git -C "$CX_SOURCE" rev-parse HEAD)" == "$LANE_REVISION" ]]; fi
  [[ ! -e "$CX_DIR/recovery-policy-floor" ]]
  crossing_cpg fold -c "CREATE TABLE drill_sentinel(id integer PRIMARY KEY, value text); INSERT INTO drill_sentinel VALUES (1, 'original')" >/dev/null
  # 2. A real forward upgrade by the candidate's tools. release.prev is written
  #    by upgrade.sh itself from the level-0 host it finds.
  if [[ "$mode" == image ]]; then args=(--image "$CROSSING_CAND_IMG"); else args=(--commit "$REVISION"); fi
  trace="$cdir/lineage-$mode-upgrade.trace"; rc=0
  timeout --kill-after=15 2700 env SHELLOPTS=xtrace bash -x "$CROSSING_TOOLS_CAND/upgrade.sh" --deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE" "${args[@]}" >"$trace" 2>&1 </dev/null || rc=$?
  (( rc == 0 )) || { tail -n 40 "$trace" >&2; echo "crossing: the forward upgrade from LANE_BASE failed (rc=$rc)" >&2; return 1; }
  [[ "$(grep -cF "+ $CX_DIR/deploy-tools/provision.sh --skip-harden" "$trace")" == 1 ]]
  if grep -Fq -- "$CX_SOURCE/deploy/digitalocean/provision.sh" "$trace"; then echo 'crossing: the upgrade ran a checkout provision.sh' >&2; return 1; fi
  [[ "$(cat "$CX_DIR/recovery-policy-floor")" == 1 ]]
  if [[ "$mode" == image ]]; then
    grep -qx 'mode=image' "$CX_DIR/release.prev"
    grep -qx "previous_image=$CROSSING_LANE_IMG" "$CX_DIR/release.prev"
    crossing_assert_deployed "$CROSSING_CAND_TAG"
  else
    grep -qx 'mode=build' "$CX_DIR/release.prev"
    grep -qx "previous_commit=$LANE_REVISION" "$CX_DIR/release.prev"
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
    jq -n --arg built "$built_id" --arg head "$REVISION" --arg onBox "$img_bin" --arg registry "$cand_bin" \
      '{commit:$head, builtImageId:$built, onBoxBinarySha256:$onBox, registryCandidateBinarySha256:$registry,
        note:"the registry candidate reports its own FOLD_VERSION and the on-box build reports dev, so equal hashes are not required"}' \
      >"$cdir/lineage-build-binary.json"
  fi
  json="$(crossing_cdc exec -T app /fold version --json)"
  for f in $(runtime_tools); do
    cmp "$CROSSING_TOOLS_CAND/$f" "$CX_DIR/$f"
    cmp "$CROSSING_TOOLS_CAND/$f" "$CX_DIR/deploy-tools/$f"
  done
  crossing_emit cases.jsonl --arg acceptance A-lineage-upgrade --arg mode "$mode" --arg site "$CX_SITE" \
    --argjson lane "$lane_json" --argjson candidate "$json" --arg release "$(cat "$CX_DIR/release.prev")" \
    --arg floor "$(cat "$CX_DIR/recovery-policy-floor")" \
    '{kind:"positive", acceptance:$acceptance, id:("lineage-" + $mode), mode:$mode, site:$site, fixture:false, pass:true,
      laneVersion:$lane, candidateVersion:$candidate, releasePrev:$release, floor:$floor}'
  # 3. Natural rollback refusals; the build site also takes the full matrix.
  ups=("$CX_DIR/upgrade.sh" "$CROSSING_TOOLS_CAND/upgrade.sh"); names=(installed candidate-tools)
  if [[ "$mode" == image ]]; then kind=rollback-image; else kind=rollback-commit; fi
  for i in 0 1; do
    crossing_refuse "$deployed" R-rollback-genuine "$kind" "rollback-genuine.${names[i]}" "${ups[i]}" --deploy-dir "$CX_DIR" --source-dir "$CX_SOURCE" --rollback
  done
  if [[ "$mode" == build ]]; then
    CX_DUMP="$(find "$CX_DIR/dumps" -name 'fold-*.dump' | sort | tail -n 1)"
    [[ -f "$CX_DUMP" ]]
    crossing_forward_restore_matrix build
  fi
  # A failed run is cleaned by the runner's EXIT trap (crossing_cleanup_all).
  crossing_site_end
}

real_level0_lineage() {
  local mode
  crossing_prepare
  for mode in image build; do
    drill_step "lineage/$mode"
    crossing_lineage_site "$mode"
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
  drill_step landing-deployed
  reset_case "$CROSSING_LANDING_IMG"
  ajson="$(dc exec -T app /fold version --json)"
  jq -e --arg v "$CROSSING_LANDING_TAG" '.version == $v and ((.recoveryPolicy // 0) == 1)' <<<"$ajson" >/dev/null \
    || { echo "crossing: the deployed app is not the real landing image: $ajson" >&2; return 1; }
  drill_step forward-to-candidate
  "$DIR/upgrade.sh" --deploy-dir "$DIR" --source-dir "$SOURCE" --image "$CROSSING_CAND_IMG" >"$log" 2>&1 || { tail -n 40 "$log" >&2; return 1; }
  wait_public
  crossing_assert_deployed "$CROSSING_CAND_TAG"
  grep -qx 'mode=image' "$DIR/release.prev"
  grep -qx "previous_image=$CROSSING_LANDING_IMG" "$DIR/release.prev"
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
  [[ "$recovery_image" == "$(docker image inspect --format '{{.Id}}' "$CROSSING_LANDING_IMG")" ]]
  [[ "$(jq -er .level "$DIR/recovery.lock")" == 1 ]]
  rjson="$(docker exec "$rid" /fold version --json)"
  jq -e --arg v "$CROSSING_LANDING_TAG" '.version == $v and .recoveryPolicy == 1' <<<"$rjson" >/dev/null \
    || { echo "crossing: the recovery instance is not the landing image at level 1: $rjson" >&2; return 1; }
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
  jq -e --arg v "$CROSSING_LANDING_TAG" '.version == $v and .recoveryPolicy == 1' <<<"$ajson" >/dev/null \
    || { echo "crossing: the reopened app is not the landing image at level 1: $ajson" >&2; return 1; }
  [[ "$(cat "$DIR/recovery-policy-floor")" == 1 ]]
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
  crossing_emit cases.jsonl --arg acceptance A-same-level-rollback --arg image "$CROSSING_LANDING_IMG" \
    --arg recoveryImageId "$recovery_image" --argjson recovery "$rjson" --argjson reopened "$ajson" \
    --arg toolsSha "$(sha256sum <"$cdir/tools.after" | cut -d' ' -f1)" \
    '{kind:"positive", acceptance:$acceptance, id:"same-level-rollback", fixture:false, pass:true, image:$image,
      recoveryImageId:$recoveryImageId, recoveryVersion:$recovery, reopenedVersion:$reopened, toolsSha256:$toolsSha}'
  reset_case
}

real_older_archive_restore() {
  local cdir original_dump="$DB_DUMP" older archive_yml archive_id old_url endpoint password member
  local scratch_url a_versions table version fence_before fence_after status rid i ajson
  local -a d_tables=()
  crossing_prepare
  crossing_use_main
  cdir="$(crossing_dir)"
  older="$cdir/older-archive.dump"
  archive_yml="$cdir/archive.yml"
  password="$(env_value "$DIR/.env" DB_PASSWORD)"
  reset_case
  drill_step archive-dumped-on-lane-base-image
  pg postgres -c 'CREATE DATABASE drill_archive_fixture OWNER church TEMPLATE template0'
  cat >"$archive_yml" <<ARCHIVE
services:
  archive:
    extends:
      file: $DIR/docker-compose.yml
      service: app
    image: $CROSSING_ARCHIVE_IMG
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
  [[ "$(docker inspect --format '{{.Image}}' "$archive_id")" == "$(docker image inspect --format '{{.Id}}' "$CROSSING_ARCHIVE_IMG")" ]]
  ajson="$(docker exec "$archive_id" /fold version --json)"
  jq -e '(has("recoveryPolicy") | not) or .recoveryPolicy == 0' <<<"$ajson" >/dev/null
  old_url="$URL"
  endpoint="$(dc -f "$DIR/docker-compose.yml" -f "$archive_yml" --profile archive port archive 8080)"
  URL="http://localhost:${endpoint##*:}"
  setup
  member="$(new_user 'Archive Member' archive-member@example.invalid)"
  a_versions="$(pg drill_archive_fixture -c "SELECT string_agg(version_id::text,',' ORDER BY version_id) FROM goose_db_version WHERE is_applied AND version_id>0")"
  [[ "$a_versions" == "$(crossing_migration_versions "$ROOT" "$ARCHIVE_BASE_REVISION")" ]] \
    || { echo "crossing: the archive database is at goose versions $a_versions, not its image's migrations" >&2; return 1; }
  [[ "$(pg drill_archive_fixture -c "SELECT to_regclass('public.admin_operation_attempts') IS NULL")" == t ]]
  dc exec -T db pg_dump -U church -Fc drill_archive_fixture >"$older"
  dc exec -T db pg_restore -l <"$older" >"$cdir/table-sets/archive-toc.txt"
  crossing_toc_tables <"$cdir/table-sets/archive-toc.txt" >"$cdir/table-sets/archive.tables"
  docker rm -f "$archive_id"
  pg postgres -c 'DROP DATABASE drill_archive_fixture'
  URL="$old_url"; login
  drill_step candidate-table-set
  pg postgres -c 'CREATE DATABASE drill_candidate_schema OWNER church TEMPLATE template0'
  scratch_url="postgres://church:$password@db:5432/drill_candidate_schema?sslmode=disable"
  docker run --rm --network "${PROJECT}_default" -e "DATABASE_URL=$scratch_url" "$CROSSING_CAND_IMG" migrate up >"$cdir/table-sets/candidate-migrate.log" 2>&1 \
    || { tail -n 20 "$cdir/table-sets/candidate-migrate.log" >&2; return 1; }
  pg drill_candidate_schema -c "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY 1" | LC_ALL=C sort -u >"$cdir/table-sets/candidate.tables"
  pg postgres -c 'DROP DATABASE drill_candidate_schema'
  crossing_table_difference "$cdir/table-sets/archive.tables" "$cdir/table-sets/candidate.tables" >"$cdir/table-sets/D.tables"
  LC_ALL=C comm -23 "$cdir/table-sets/archive.tables" "$cdir/table-sets/candidate.tables" >"$cdir/table-sets/archive-only.tables"
  mapfile -t d_tables <"$cdir/table-sets/D.tables"
  jq -n --arg archiveImage "$CROSSING_ARCHIVE_IMG" --arg archiveRevision "$ARCHIVE_BASE_REVISION" \
    --arg archiveImageId "$(docker image inspect --format '{{.Id}}' "$CROSSING_ARCHIVE_IMG")" \
    --arg dumpSha256 "$(sha256sum <"$older" | cut -d' ' -f1)" --arg archiveVersions "$a_versions" \
    --rawfile archiveTables "$cdir/table-sets/archive.tables" --rawfile candidateTables "$cdir/table-sets/candidate.tables" \
    --rawfile d "$cdir/table-sets/D.tables" --rawfile archiveOnly "$cdir/table-sets/archive-only.tables" \
    '($d|split("\n")|map(select(length>0))) as $D
     | {archiveImage:$archiveImage, archiveRevision:$archiveRevision, archiveImageId:$archiveImageId, dumpSha256:$dumpSha256,
        archiveGooseVersions:$archiveVersions, archiveTables:($archiveTables|split("\n")|map(select(length>0))),
        candidateTables:($candidateTables|split("\n")|map(select(length>0))), D:$D,
        archiveOnly:($archiveOnly|split("\n")|map(select(length>0))),
        recordedNotAssumed:{email_address_optouts:{inD:($D|index("email_address_optouts")!=null)}}}' \
    >"$cdir/older-archive.json"
  drill_step restore-with-candidate-image
  DB_DUMP="$older"
  kill_point after-swap --dump "$DB_DUMP" --image "$CROSSING_CAND_IMG" --source-dir "$SOURCE"
  assert_legacy_archive_schema
  for table in "${d_tables[@]}"; do
    [[ "$(pg fold -c "SELECT to_regclass('public.$table') IS NULL")" == t ]]
  done
  [[ "$(pg fold -c "SELECT string_agg(version_id::text,',' ORDER BY version_id) FROM goose_db_version WHERE is_applied AND version_id>0")" == "$a_versions" ]]
  restore --resume
  assert_private
  rid="$(recovery_ids)"
  docker exec "$rid" /fold migrate status --json >"$cdir/older-archive-goose-status.json"
  jq -e 'type == "array" and all(.[]; .state == "applied")' "$cdir/older-archive-goose-status.json" >/dev/null
  jq -e --arg v "$CROSSING_CAND_TAG" '.version == $v and .recoveryPolicy == 1' <<<"$(docker exec "$rid" /fold version --json)" >/dev/null \
    || { echo 'crossing: the recovery instance is not the committed head at level 1' >&2; return 1; }
  [[ "$(pg fold -c 'SELECT count(*) - count(DISTINCT version_id) FROM goose_db_version WHERE version_id>0')" == 0 ]]
  [[ "$(pg fold -c "SELECT string_agg(version_id::text,',' ORDER BY version_id) FROM goose_db_version WHERE is_applied AND version_id>0")" == "$(crossing_migration_versions "$ROOT" "$REVISION")" ]]
  for table in "${d_tables[@]}"; do
    version="$(crossing_owner_migration "$ROOT" "$REVISION" "$table")" \
      || { echo "crossing: no single migration after the baseline creates $table" >&2; return 1; }
    [[ "$(pg fold -c "SELECT count(*) FROM goose_db_version WHERE version_id=$version")" == 1 ]]
    [[ "$(pg fold -c "SELECT count(*) FROM goose_db_version WHERE version_id=$version AND is_applied")" == 1 ]]
    [[ "$(pg fold -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='$table' AND c.relkind IN ('r','p')")" == 1 ]]
    jq -e --argjson v "$version" '[.[] | select(.version == $v and .state == "applied")] | length == 1' "$cdir/older-archive-goose-status.json" >/dev/null
  done
  [[ "$(docker inspect --format '{{.Image}}' "$rid")" == "$(docker image inspect --format '{{.Id}}' "$CROSSING_CAND_IMG")" ]]
  drill_step reopen-and-fenced-delete
  restore --reopen --checked-church-records
  DB_DUMP="$original_dump"
  wait_public
  URL="$(public_url)"; login
  fence_before="$(pg fold -c "SELECT value::bigint FROM settings WHERE key='restore_fence'")"
  [[ "$fence_before" =~ ^[0-9]+$ ]]
  member="$(new_user 'Fence Probe' fence-probe@example.invalid)"
  api DELETE "/admin/users/$member" '{}' >"$cdir/older-archive-delete.json"
  [[ "$(pg fold -c "SELECT deleted_at IS NOT NULL FROM users WHERE id=$member")" == t ]]
  fence_after="$(pg fold -c "SELECT value::bigint FROM settings WHERE key='restore_fence'")"
  (( fence_after > fence_before )) || { echo "crossing: the delete did not advance the restore fence ($fence_before -> $fence_after)" >&2; return 1; }
  status="$(dc exec -T app /fold version --json)"
  jq -e --arg v "$CROSSING_CAND_TAG" '.version == $v and .recoveryPolicy == 1' <<<"$status" >/dev/null \
    || { echo 'crossing: the reopened app is not the committed head at level 1' >&2; return 1; }
  [[ "$(docker inspect --format '{{.Image}}' "$(dc ps -q app)")" == "$(docker image inspect --format '{{.Id}}' "$CROSSING_CAND_IMG")" ]]
  crossing_emit cases.jsonl --arg acceptance A-older-archive --slurpfile record "$cdir/older-archive.json" \
    --argjson before "$fence_before" --argjson after "$fence_after" \
    '{kind:"positive", acceptance:$acceptance, id:"older-archive", fixture:false, pass:true, archive:$record[0], fenceBefore:$before, fenceAfter:$after}'
  reset_case
}

# Final check for the runner's last case: every acceptance is present, passed,
# and the revisions and images are on record.
assert_crossing_evidence() {
  local cdir
  cdir="$(crossing_dir)"
  crossing_evidence_complete "$cdir/cases.jsonl" "$cdir/images.jsonl" || return 1
  [[ -s "$cdir/revisions.json" && -s "$cdir/older-archive.json" && -s "$cdir/lineage-build-binary.json" ]]
}
