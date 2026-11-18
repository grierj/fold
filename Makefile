# Development tasks (11.9).

DEV_COMPOSE := docker compose -f deploy/docker-compose.dev.yml
DEV_DATABASE_URL := postgres://fold:fold@localhost:5432/fold?sslmode=disable

.PHONY: dev test go-test lint lint-go lint-web devdb e2e lint-sh compose-smoke deadcode reason-review-scale-test backup-test

# Start the dev database and wait until it accepts connections.
devdb:
	$(DEV_COMPOSE) up -d --wait db

# Go server (APP_ENV=dev) and the Vite dev server together.
dev: devdb
	cd web && npm install --no-audit --no-fund
	trap 'kill 0' EXIT; \
	APP_ENV=dev DATABASE_URL="$${DATABASE_URL:-$(DEV_DATABASE_URL)}" go run . & \
	(cd web && npm run dev) & \
	wait

# go test ./... and the shell self-tests. CI runs go-test in its test job and
# the self-tests in its checks job.
test: go-test reason-review-scale-test backup-test

# go test ./... against DATABASE_URL, with the root package split across
# parallel shards (scripts/go-test.sh); starts the dev database when it is unset.
go-test:
	@if [ -z "$$DATABASE_URL" ]; then \
		$(DEV_COMPOSE) up -d --wait db && \
		DATABASE_URL="$(DEV_DATABASE_URL)" scripts/go-test.sh; \
	else \
		scripts/go-test.sh; \
	fi

# Playwright (Chromium) against the real binary, split across parallel shards
# (web/e2e/run.sh), each on its own fresh <db>_e2e_<n> Postgres database
# (web/e2e/server.sh). Starts the dev database when DATABASE_URL is unset.
# First time: cd web && npx playwright install chromium.
e2e:
	cd web && npm run build
	@if [ -z "$$DATABASE_URL" ]; then \
		$(DEV_COMPOSE) up -d --wait db && \
		DATABASE_URL="$(DEV_DATABASE_URL)" web/e2e/run.sh; \
	else \
		web/e2e/run.sh; \
	fi

# make lint runs everything; CI runs lint-go and lint-sh in the test job and
# lint-web in the web job, after npm ci.
lint: lint-sh lint-go lint-web

lint-go:
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...

# Production reachability gate (tests excluded). Allow only the test-only packages
# (the test database, the source-guard toolkit) and the two registry inspection
# seams used by tests and tooling.
deadcode:
	@report=$$(mktemp); trap 'rm -f "$$report"' EXIT; \
	go run golang.org/x/tools/cmd/deadcode@v0.52.0 ./... > "$$report" || exit $$?; \
	if grep -Ev -f .deadcode-allow "$$report"; then \
		echo 'Unexpected unreachable production code; remove it or add a caller.' >&2; exit 1; \
	else status=$$?; [ "$$status" -eq 1 ]; fi

# Unused files, exports and dependencies in web/ (knip, pinned in package.json).
# The second pass counts only production code, so an export that only a test
# uses is caught; one kept for a test says so with a @public tag and a reason.
lint-web:
	cd web && npx tsc --noEmit && npx knip && npx knip --production && npm run lint:anti-slop -- --deny-warnings

# Shell scripts under deploy/digitalocean, deploy/backup, scripts (go-test.sh,
# compose-smoke.sh) and web/e2e. -x follows source= directives. One file per
# shellcheck, in parallel: serially it takes about two minutes.
lint-sh:
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck is required (apt-get install shellcheck)"; exit 1; }
	printf '%s\n' deploy/digitalocean/*.sh deploy/backup/*.sh scripts/*.sh web/e2e/*.sh | \
		xargs -P "$$(getconf _NPROCESSORS_ONLN)" -n 1 shellcheck -x

# Boot the shipped Compose stack (app + Postgres) and smoke /healthz and /setup.
# Builds ghcr.io/grierj/fold:ci. The release workflow passes --image instead.
compose-smoke:
	bash scripts/compose-smoke.sh

# The reason-review scale wrapper's own checks, against stub tools in a scratch
# repository, and the matrix report's against stub evidence: no Docker, a few seconds.
# Also part of make test.
reason-review-scale-test:
	bash scripts/reason-review-scale.sh --self-test
	bash scripts/reason-review-matrix.sh --self-test
	bash scripts/reason-review-next-changed.sh --self-test

# The fold-backup script's own cases, against stub pg_dump, psql, age and rclone
# on PATH: no Docker, no database, no network. The second run mutates the
# script four ways and requires each mutation to make its named case fail.
# Also part of make test.
backup-test:
	bash deploy/backup/backup-test.sh
	bash deploy/backup/backup-test.sh --probes
