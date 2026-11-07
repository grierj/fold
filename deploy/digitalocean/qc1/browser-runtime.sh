#!/usr/bin/env bash
# The browser runtime of the disposable QC-1 runner: which Alpine packages, and
# whether web/package-lock.json pins what `npm ci` must install. Sourced by
# browser-prepare.sh (inside the runner) and by qc1-fixtures.sh (no Docker).
# shellcheck disable=SC2034
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then echo 'source from browser-prepare.sh only' >&2; exit 1; fi

# The nested runner is Alpine (docker:dind). Playwright's own Chromium download
# is a glibc build that does not run on musl, so the runner installs Alpine's
# chromium and browser.mjs launches it by path (QC1_CHROMIUM). A font keeps text
# layout from depending on whatever the image happens to ship.
qc1_browser_apk_packages() { printf '%s\n' chromium font-dejavu; }

# Problems with the pins `npm ci` will install, one per line; nothing when sound.
# `npm ci` itself refuses a lock that disagrees with package.json; this adds what
# it does not: that the two Playwright packages the browser step loads are
# pinned to one version, from the public registry, each with a sha512 integrity.
# (The measurement loads @playwright/test; the restore drill's browser proof loads
# playwright; both load playwright-core.)
qc1_browser_lock_problems() { # package.json package-lock.json
  [[ -f "$1" ]] || { echo "$1 is missing"; return 0; }
  [[ -f "$2" ]] || { echo "$2 is missing: npm ci needs a lock file"; return 0; }
  jq -r --slurpfile pkg "$1" '
    def entry($n): .packages["node_modules/" + $n];
    ( if (.lockfileVersion // 0) < 2 then "lockfileVersion \(.lockfileVersion // "none") is too old to carry integrity for every package" else empty end ),
    ( ["@playwright/test", "playwright", "playwright-core"][] as $n
      | ( entry($n) ) as $e
      | if $e == null then "\($n) is not in the lock file"
        elif (($e.integrity // "") | startswith("sha512-") | not) then "\($n) has no sha512 integrity in the lock file"
        elif (($e.resolved // "") | startswith("https://registry.npmjs.org/") | not) then "\($n) does not resolve from the public npm registry"
        else empty end ),
    ( if ([entry("@playwright/test"), entry("playwright"), entry("playwright-core")] | map(select(. != null) | .version) | unique | length) > 1
      then "@playwright/test \(entry("@playwright/test").version), playwright \(entry("playwright").version) and playwright-core \(entry("playwright-core").version) are pinned to different versions" else empty end ),
    ( ($pkg[0].devDependencies["@playwright/test"] // $pkg[0].dependencies["@playwright/test"]) as $range
      | if $range == null then "@playwright/test is not a dependency in package.json"
        elif entry("@playwright/test") != null and ($range | test("^[\\^~]?[0-9]") | not) then "@playwright/test has the range \($range), not a pinned or caret version"
        elif entry("@playwright/test") != null and (($range | sub("^[\\^~]"; "") | split(".")[0]) != (entry("@playwright/test").version | split(".")[0])) then "the lock pins @playwright/test \(entry("@playwright/test").version), outside the range \($range)"
        else empty end )' "$2"
}
