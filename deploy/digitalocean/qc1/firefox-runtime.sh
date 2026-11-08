#!/usr/bin/env bash
# The restore drill's Firefox proof runs in a glibc container inside the disposable
# runner (see firefox.Dockerfile for why). Sourced by browser-prepare.sh (build and
# preflight) and by restore-drill-cases.sh (the proof), so both use the same image
# and the same docker flags. Nothing here starts Docker by itself.

# The Node image the container is built on: the repository's Node major (.nvmrc) on
# Debian bookworm, a distribution Playwright supports natively (no "fallback build").
firefox_node_image() { # nvmrc-file
  local major
  major="$(tr -d '[:space:]v' <"$1")"
  [[ "$major" =~ ^[0-9]+$ ]] || return 1
  printf 'node:%s-bookworm\n' "$major"
}

# The image tag: names the exact inputs (lock file, Dockerfile, base image), so a changed
# input is a different image and an unchanged one is reused.
firefox_image_tag() { # lock-file dockerfile node-image
  local sum
  sum="$({ cat "$1" "$2"; printf "%s\n" "$3"; } | sha256sum | cut -c1-12)"
  printf 'fold-firefox-runner:%s\n' "$sum"
}

# The docker flags of every run in this image, one per line. --network host is the
# RUNNER's network (this daemon is the disposable one), where the recovery instance is
# published on 127.0.0.1; --init reaps the browser's children; --ipc=host gives
# Firefox the shared memory it needs. Never a bind of an owner's path: callers add only
# the scratch directories they name.
firefox_run_flags() {
  printf '%s\n' --rm --init --ipc=host --network host
}

# A one-line, bounded reason from a build log: the last lines that say what failed.
firefox_reason() { # log-file
  tail -n 6 "$1" 2>/dev/null | tr '\n\r\t' '   ' | tr -s ' ' | cut -c1-500
}
