#!/usr/bin/env bash
# Autoflow's per-task acceptance gate. Run from any working directory.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

expected_node="$(tr -d '[:space:]' < .nvmrc)"
actual_node="$(node -p 'process.versions.node.split(".")[0]')"
if [[ "$actual_node" != "$expected_node" ]]; then
  echo "Use Node $expected_node from .nvmrc before starting Pi (current major: $actual_node)." >&2
  exit 1
fi
if [[ ! -d web/node_modules ]]; then
  echo "Install frontend dependencies first: (cd web && npm ci)." >&2
  exit 1
fi
export CGO_ENABLED=0

# Build the embedded SPA too; build includes tsc --noEmit.
(cd web && npm run build && npm run bundle-size)
go build ./...
make lint
shellcheck .pi/autoflow-check.sh
# Starts the dev Postgres container if DATABASE_URL is unset.
make test
(cd web && npm test && npm run contrast)
