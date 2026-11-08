#!/usr/bin/env bash
# Daemon-free regressions for what the nested Alpine runner provides (restore-drill-nested.sh).
# The restore drill and the QC-1 measurement run as root inside docker:dind (Alpine, BusyBox, musl), where
# only the apk packages named in the launcher exist. Two real failures came from assuming a developer's
# machine: no /usr/bin/sync, and a BusyBox `find` with no -printf. This file pins the contract so the next one
# is found by a fixture, not by a run of several hours:
#   - every GNU-only option the runner-side scripts use names the apk package that provides it, and that
#     package is in the launcher's `apk add` line (and the line is checked by mutation, below);
#   - nothing in them needs a tool or option no installed package has (grep -P, sed -z, gawk-only awk
#     functions, procps, iproute2's ss, a scripting language);
#   - the `sync` stub the drill puts first on PATH still reaches a real sync when /usr/bin/sync is absent.
# No Docker daemon, database, browser or network; nothing is written outside a temp dir.
# shellcheck disable=SC2016 # patterns keep their dollar signs literal.
set -Eeuo pipefail
exec </dev/null
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
HERE="$ROOT/deploy/digitalocean"
WORK="$(mktemp -d /tmp/fold-runner-compat-fixture.XXXXXX)"
# shellcheck disable=SC2329 # EXIT trap.
finish() { local rc=$?; rm -rf -- "$WORK"; exit "$rc"; }
trap finish EXIT
trap 'printf "runner compat fixture failed at line %s: %s\n" "$LINENO" "$BASH_COMMAND" >&2' ERR

# Every script the runner executes: the drill and its helpers, the provisioners and tools the drill runs for real,
# and the QC-1 measurement. Fixtures and the drill-only stubs of the developer-side tests are not listed.
SCRIPTS=(restore-drill.sh restore-drill-cases.sh restore-drill-crossings.sh restore.sh backup.sh upgrade.sh provision.sh
  checks.sh smoke.sh isolation.sh deploy-drill.sh email-dns.sh qc1-measure.sh qc1/lib.sh qc1/browser-prepare.sh qc1/browser-runtime.sh qc1/firefox-runtime.sh
  testdata/restore-docker.sh testdata/restore-drill-docker.sh)
for f in "${SCRIPTS[@]}"; do [[ -f "$HERE/$f" ]] || { echo "runner compat: $f is not a script of the runner set any more; update SCRIPTS" >&2; exit 1; }; done

# Code only: whole-line comments dropped. (Strings that mention a tool in a message are not commands; the patterns below
# are anchored at command position or on an option, so they do not see them.)
code_of() { grep -hv '^[[:space:]]*#' "$HERE/$1"; }

# --- the packages the launcher installs -----------------------------------------------------------------------------------------
apk_line="$(grep -E '^docker exec "\$CID" apk add --no-cache ' "$HERE/restore-drill-nested.sh")"
apk_line="${apk_line%% >*}"
[[ "$(wc -l <<<"$apk_line")" == 1 ]]
# shellcheck disable=SC2206 # a word list from a fixed line of the launcher
PKGS=(${apk_line#*--no-cache })
printf '%s\n' "${PKGS[@]}" | grep -qx coreutils
printf '%s\n' "${PKGS[@]}" | grep -qx findutils

# --- GNU-only constructs: pattern | package that provides them ----------------------------------------------------------------
# Command-position prefix: start of line or after | ; & ( ` $( then do else !  -- so a word in a message does not match.
CP='(^|[|;&(`]|\$\(|[[:space:]](then|do|else)|!)[[:space:]]*'
REQUIRES=(
  "find [^|;&]*-printf|findutils"
  "find [^|;&]*-regex|findutils"
  "(^|[^a-z_.-])tail [^|;&]*--(pid|sleep-interval)|coreutils"
  "${CP}(timeout) +--kill-after|coreutils"
  "${CP}realpath +-m|coreutils"
  "date +-d |coreutils"
  "date +[^ ]*%[0-9]*N|coreutils"
  "${CP}touch +-d |coreutils"
  "${CP}stat +-L?c |coreutils"
  "${CP}paste +-sd|coreutils"
  "${CP}comm |coreutils"
  "${CP}sort +-z|coreutils"
  "${CP}(sync) +-f|coreutils"
  "${CP}flock |util-linux"
  "${CP}setsid |util-linux"
  "${CP}tar |tar"
  "${CP}jq |jq"
  "${CP}curl |curl"
  "${CP}git |git"
  "${CP}openssl |openssl"
  "${CP}npm |npm"
  "(^|[[:space:]=])go build |go"
)
# --- constructs no installed package provides: pattern | why -------------------------------------------------------------------
# BusyBox grep, sed and awk serve this runner (GNU grep, sed, gawk and diffutils are not installed).
FORBIDS=(
  "${CP}(e|f)?grep +-[a-zA-Z]*[Pz]|BusyBox grep has no -P or -z"
  "${CP}grep +--(perl-regexp|null-data|null)\b|BusyBox grep has no long GNU options"
  "${CP}sed +-[a-zA-Z]*z|BusyBox sed has no -z"
  "(gensub|strftime|systime|asorti?|patsplit|strtonum|PROCINFO|FPAT|IGNORECASE)\(?|BusyBox awk is not gawk"
  "${CP}(ps|pgrep|pkill|pidof|killall|free|vmstat|top)( |\$)|procps is not installed"
  "${CP}(ss|lsof|netstat|nc|ncat|socat)( |\$)|no socket tool is installed"
  "${CP}(python3?|perl|ruby|php)( |\$)|no scripting language is installed (NO Python: tooling is shell or Go)"
  "${CP}(rsync|xxd|bc|zcat|unzip|sudo)( |\$)|not installed in the runner"
)

# scan FILE_NAMES... : prints "file: pattern" for each REQUIRES pattern found whose package is not in PKGS
missing_packages() { # pkgs-file script...
  local pkgs="$1" f entry pat pkg; shift
  for f in "$@"; do
    for entry in "${REQUIRES[@]}"; do
      pat="${entry%|*}"; pkg="${entry##*|}"
      if code_of "$f" | grep -Eq -- "$pat" && ! grep -qx "$pkg" "$pkgs"; then echo "$f needs $pkg ($pat)"; fi
    done
  done
}
# apk package names, one per line: the launcher's, and the browser preparation's own
# shellcheck source=deploy/digitalocean/qc1/browser-runtime.sh
source "$HERE/qc1/browser-runtime.sh"
{ printf '%s\n' "${PKGS[@]}"; qc1_browser_apk_packages; } >"$WORK/pkgs"

missing="$(cd "$HERE" && missing_packages "$WORK/pkgs" "${SCRIPTS[@]}")"
[[ -z "$missing" ]] || { echo "runner compat: the launcher's apk line lacks a package the scripts need:" >&2; echo "$missing" >&2; exit 1; }

forbidden=""
for f in "${SCRIPTS[@]}"; do
  for entry in "${FORBIDS[@]}"; do
    pat="${entry%|*}"; why="${entry##*|}"
    if hits="$(code_of "$f" | grep -En -- "$pat")"; then forbidden+="$f: $why: $(head -n 1 <<<"$hits")"$'\n'; fi
  done
done
[[ -z "$forbidden" ]] || { echo "runner compat: a script uses what the Alpine runner does not have:" >&2; printf '%s' "$forbidden" >&2; exit 1; }

# --- the scanner is not vacuous: every pattern fires on a sample, and a missing package is reported -----------------------------------
sample_for_require() { # index -> sample line the pattern must match
  case "$1" in
    0) echo 'x=$(find "$d" -name "*.dump" -printf "%T@ %p")' ;;
    1) echo 'find "$d" -regex ".*x"' ;;
    2) echo '  timeout 5 tail --pid="$p" -f /dev/null' ;;
    3) echo 'timeout --kill-after=5 60 true' ;;
    4) echo 'WORK="$(realpath -m "$WORK")"' ;;
    5) echo 'start_s="$(date -d "$start" +%s)"' ;;
    6) echo 'ms() { date +%s%3N; }' ;;
    7) echo 'touch -d "200 days ago" "$f"' ;;
    8) echo 'id="$(stat -c %Y "$f")"' ;;
    9) echo 'x | paste -sd, -' ;;
    10) echo 'lost="$(comm -23 a b)"' ;;
    11) echo 'x | sort -z | y' ;;
    12) echo '  sync -f "$dir"' ;;
    13) echo '  flock -x 9' ;;
    14) echo '  setsid bash -c true' ;;
    15) echo '  tar czf x -C y .' ;;
    16) echo '  jq -n 1' ;;
    17) echo '  curl -fsS x' ;;
    18) echo '  git -C x status' ;;
    19) echo '  openssl x509' ;;
    20) echo '  npm ci' ;;
    21) echo '  go build -o x y' ;;
  esac
}
(( ${#REQUIRES[@]} == 22 ))   # a pattern added without a sample fails here
for ((i=0; i<${#REQUIRES[@]}; i++)); do
  pat="${REQUIRES[i]%|*}"
  sample_for_require "$i" | grep -Eq -- "$pat" || { echo "runner compat: REQUIRES[$i] ($pat) does not match its own sample" >&2; exit 1; }
done
sample_for_forbid() {
  case "$1" in
    0) echo '  grep -oP "x" f' ;;
    1) echo '  grep --null -l x' ;;
    2) echo '  sed -z s/a/b/ f' ;;
    3) echo '  awk "{ print gensub(/a/, \"b\", \"g\") }"' ;;
    4) echo '  ps -o pid= -p 1' ;;
    5) echo '  ss -ltn' ;;
    6) echo '  ruby -e 1' ;;
    7) echo '  rsync -a a b' ;;
  esac
}
(( ${#FORBIDS[@]} == 8 ))
for ((i=0; i<${#FORBIDS[@]}; i++)); do
  pat="${FORBIDS[i]%|*}"
  sample_for_forbid "$i" | grep -Eq -- "$pat" || { echo "runner compat: FORBIDS[$i] ($pat) does not match its own sample" >&2; exit 1; }
done
# ...and common safe lines are not flagged
for safe in 'grep -qF -- "$x" "$f"' 'sed -n "s/a/b/p" f' 'awk -F "|" "{print \$1}" f' 'echo "run: sudo fuser -v x"' '  timeout 2 bash -c true' "printf '%s\\n' ps"; do
  for entry in "${FORBIDS[@]}"; do
    ! grep -Eq -- "${entry%|*}" <<<"$safe" || { echo "runner compat: a safe line is flagged: $safe ($entry)" >&2; exit 1; }
  done
done
# Mutation: take each package out of the list in turn; the scripts that need it must be reported.
for pkg in findutils coreutils util-linux tar jq curl git openssl npm go; do
  grep -vx "$pkg" "$WORK/pkgs" >"$WORK/pkgs.minus"
  out="$(cd "$HERE" && missing_packages "$WORK/pkgs.minus" "${SCRIPTS[@]}")"
  grep -q " needs $pkg " <<<"$out" || { echo "runner compat: removing $pkg from the apk line is not caught" >&2; exit 1; }
done

# --- the launcher still installs what the browser preparation and the drill call ----------------------------------------------------
for pkg in bash git jq curl go nodejs npm util-linux coreutils findutils tar openssl; do grep -qx "$pkg" "$WORK/pkgs" || { echo "runner compat: $pkg is not installed by the launcher" >&2; exit 1; }; done
grep -qx chromium "$WORK/pkgs"

# --- the drill's sync stub reaches a real sync with or without /usr/bin/sync ---------------------------------------------------------------
awk "/^cat >\"\\\$WORK\/drill-bin\/sync\" <<'SYNC'\$/{on=1; next} /^SYNC\$/{on=0} on" "$HERE/restore-drill.sh" >"$WORK/sync.stub"
[[ -s "$WORK/sync.stub" ]]
grep -q 'recovery-policy-floor' "$WORK/sync.stub"
# every absolute path the stub execs is guarded by an existence test or is the last resort
if [[ -x /bin/sync ]]; then
  chmod 755 "$WORK/sync.stub"
  mkdir "$WORK/d"
  # with /usr/bin/sync present (this machine) and with it made absent
  RESTORE_DRILL_WORK="$WORK" "$WORK/sync.stub" -f "$WORK/d"
  sed 's#/usr/bin/sync#/nonexistent/usr/bin/sync#g' "$WORK/sync.stub" >"$WORK/sync.absent"; chmod 755 "$WORK/sync.absent"
  RESTORE_DRILL_WORK="$WORK" "$WORK/sync.absent" -f "$WORK/d"
  # the fault the drill injects on the recovery-policy floor still wins over both
  : >"$WORK/fail-floor"
  rc=0; RESTORE_DRILL_WORK="$WORK" "$WORK/sync.absent" -f "$WORK/d/recovery-policy-floor" || rc=$?
  [[ "$rc" == 1 ]]
  rm -f "$WORK/fail-floor"
fi
# the stub must not be able to name only a path Alpine lacks: /bin/sync is always the last resort
tail -n 1 "$WORK/sync.stub" | grep -qx 'exec /bin/sync "\$@"'
echo 'runner compat fixtures: ok'
