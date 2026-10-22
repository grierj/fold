#!/usr/bin/env bash
# Checks that do not require Postgres. NODE24_BIN may name a cached Node 24.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/fold-s3-go-cache}"
export FOLD_WEB_CACHE_DIR
FOLD_WEB_CACHE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/fold-s3-vite.XXXXXX")
trap 'rm -rf "$FOLD_WEB_CACHE_DIR"' EXIT
run_node() {
  if [[ -n "${NODE24_BIN:-}" ]]; then
    "$NODE24_BIN" "$@"
  else
    npx -y node@24 "$@"
  fi
}
CGO_ENABLED=0 go build ./...
go vet ./...
go test ./... -run '^$'
go test ./internal/policy
go test ./internal/notify -run 'Test(EveryKind|EveryKindRenders|OnlyApprove|ConvertedTeen)'
go test . -run 'Test(WebTypes|WebError|ErrorCodes|PagesNever|TestFilesSit|UIRatchets|ColorsCome|DocsSay|NoPython|MemberHelp)'
cd web
run_node node_modules/typescript/bin/tsc --noEmit
run_node node_modules/knip/bin/knip.js
TZ=America/Boise run_node --no-experimental-webstorage node_modules/vitest/vitest.mjs run --configLoader native --maxWorkers=2
run_node node_modules/vite/bin/vite.js build --configLoader native
run_node scripts/token-contrast.mjs
run_node scripts/bundle-size.mjs
cd ..
CGO_ENABLED=0 go build ./...
