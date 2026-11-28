# Developer commands. Nothing here is needed to run a church's site.
TAILWIND_VERSION := 4.3.5
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.10.0
TW_OS := $(shell uname -s | sed 's/Linux/linux/;s/Darwin/macos/')
TW_ARCH := $(shell uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/')
TW_FILE := tailwindcss-$(TW_OS)-$(TW_ARCH)

.PHONY: build test check css css-check dev demo demo-reset e2e image budgets

build:
	CGO_ENABLED=0 go build -trimpath -o bin/fold ./cmd/fold

test:
	go test ./...

# What CI runs before the browser gates.
check: css-check
	go vet ./...
	go run $(STATICCHECK) ./...
	go run $(GOVULNCHECK) ./...
	go test ./...

# The stylesheet, built by the Tailwind standalone binary: no Node. The
# download is checked against the release's published checksums.
bin/tailwindcss:
	mkdir -p bin
	curl -fsSL -o bin/$(TW_FILE) https://github.com/tailwindlabs/tailwindcss/releases/download/v$(TAILWIND_VERSION)/$(TW_FILE)
	curl -fsSL -o bin/tailwind-sums.txt https://github.com/tailwindlabs/tailwindcss/releases/download/v$(TAILWIND_VERSION)/sha256sums.txt
	cd bin && grep " ./$(TW_FILE)$$" tailwind-sums.txt | sed 's# ./# #' | sha256sum -c -
	mv bin/$(TW_FILE) bin/tailwindcss
	chmod +x bin/tailwindcss

css: bin/tailwindcss
	bin/tailwindcss -i web/css/app.css -o web/static/app.css --minify

# The committed stylesheet matches its source and the templates.
css-check: bin/tailwindcss
	bin/tailwindcss -i web/css/app.css -o bin/app.css --minify
	@cmp -s bin/app.css web/static/app.css || { echo "web/static/app.css is stale: run make css"; exit 1; }

# A local site at http://localhost:8080 with mail kept at /dev/outbox.
dev: css
	DEV=1 DATA_DIR=data go run ./cmd/fold serve

# The same, filled with a made-up church on first run (cmd/demo).
demo: css
	test -f data-demo/app.db || go run ./cmd/demo -data data-demo
	DEV=1 DATA_DIR=data-demo go run ./cmd/fold serve

demo-reset:
	rm -rf data-demo

# The browser gates: accessibility, layout and contrast at 390 and 1280 px.
e2e: build
	cd e2e && npm ci && npx playwright test

image:
	docker build -t fold:dev .

# The budgets in AGENTS.md.
budgets:
	@echo "Go and templates, not tests: $$(find . -path ./e2e -prune -o \( -name '*.go' -o -name '*.html' \) ! -name '*_test.go' -print | xargs cat | wc -l) of 35000 lines"
	@echo "Tables: $$(cat migrations/*.sql | grep -ci 'create table') of 35"
	@echo "Routes: $$(grep -c 'mux.Handle' internal/app/routes.go) of 200"
