# Development tasks (11.9). No Python: shell and Go only.

DEV_COMPOSE := docker compose -f deploy/docker-compose.dev.yml
DEV_DATABASE_URL := postgres://fold:fold@localhost:5432/fold?sslmode=disable

.PHONY: dev test lint lint-go lint-web devdb e2e lint-sh provision-dry-run deadcode

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

# go test ./... against DATABASE_URL, with the root package split across
# parallel shards (scripts/go-test.sh); starts the dev database when it is unset.
test:
	@if [ -z "$$DATABASE_URL" ]; then \
		$(DEV_COMPOSE) up -d --wait db && \
		DATABASE_URL="$(DEV_DATABASE_URL)" scripts/go-test.sh; \
	else \
		scripts/go-test.sh; \
	fi

# Playwright (Chromium) against the real binary on a fresh <db>_e2e Postgres
# database (web/e2e/server.sh). Starts the dev database when DATABASE_URL is
# unset. First time: cd web && npx playwright install chromium.
e2e:
	cd web && npm run build
	@if [ -z "$$DATABASE_URL" ]; then \
		$(DEV_COMPOSE) up -d --wait db && \
		cd web && DATABASE_URL="$(DEV_DATABASE_URL)" npx playwright test; \
	else \
		cd web && npx playwright test; \
	fi

# make lint runs everything; CI runs lint-go and lint-sh in the test job and
# lint-web in the web job, after npm ci.
lint: lint-sh lint-go lint-web

lint-go:
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...

# Production reachability gate (tests excluded). Allow only the test database
# package and the two registry inspection seams used by tests and tooling.
deadcode:
	@report=$$(mktemp); trap 'rm -f "$$report"' EXIT; \
	go run golang.org/x/tools/cmd/deadcode@v0.52.0 ./... > "$$report" || exit $$?; \
	if grep -Ev -f .deadcode-allow "$$report"; then \
		echo 'Unexpected unreachable production code; remove it or add a caller.' >&2; exit 1; \
	else status=$$?; [ "$$status" -eq 1 ]; fi

# Unused files, exports and dependencies in web/ (knip, pinned in package.json).
lint-web:
	cd web && npx knip

# Shell scripts under deploy/digitalocean and scripts. -x follows source= directives.
lint-sh:
	@command -v shellcheck >/dev/null 2>&1 || { echo "shellcheck is required (apt-get install shellcheck)"; exit 1; }
	shellcheck -x deploy/digitalocean/*.sh scripts/*.sh

# provision.sh --dry-run inside a fresh Ubuntu container. Needs Docker.
provision-dry-run:
	bash deploy/digitalocean/dry-run-test.sh
