# Development tasks (11.9). No Python: shell and Go only.

DEV_COMPOSE := docker compose -f deploy/docker-compose.dev.yml
DEV_DATABASE_URL := postgres://fold:fold@localhost:5432/fold?sslmode=disable

.PHONY: dev test lint devdb

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

# go test against DATABASE_URL; starts the dev database when it is unset.
test:
	@if [ -z "$$DATABASE_URL" ]; then \
		$(DEV_COMPOSE) up -d --wait db && \
		DATABASE_URL="$(DEV_DATABASE_URL)" go test ./...; \
	else \
		go test ./...; \
	fi

lint:
	go vet ./...
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
