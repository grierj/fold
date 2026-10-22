#!/usr/bin/env bash
# Playwright's web server (make e2e): the real fold binary on a fresh
# Postgres database named <DATABASE_URL's database>_e2e. No Python.
set -euo pipefail
: "${DATABASE_URL:?set DATABASE_URL (make e2e does)}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
[ -f "$root/web/dist/index.html" ] || { echo "run npm run build first (make e2e does)" >&2; exit 1; }

base="${DATABASE_URL%%\?*}"          # postgres://u:p@host:port/db
query="${DATABASE_URL#"$base"}"      # ?sslmode=disable, or empty
db="${base##*/}_e2e"
PGOPTIONS="-c client_min_messages=warning" psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -q \
  -c "DROP DATABASE IF EXISTS \"$db\" WITH (FORCE)" -c "CREATE DATABASE \"$db\""

work="${TMPDIR:-/tmp}/fold-e2e"
rm -rf "$work" && mkdir -p "$work"
(cd "$root" && go build -o "$work/fold" .)

# Production by default. The invitation scenarios use dev mode's outbox.
# Cookies work over plain http on 127.0.0.1 in either mode.
export DATABASE_URL="${base%/*}/$db$query" APP_ENV=prod PORT="${E2E_PORT:-18090}" \
  SESSION_SECRET=e2e-only-session-secret-0123456789abcdef COOKIE_SECURE=false \
  UPLOAD_DIR="$work/uploads"
export APP_ENV="${E2E_APP_ENV:-prod}"
cd "$root" # templates/ is read from the working directory
exec "$work/fold" serve
