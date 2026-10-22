# Coral Go — development build targets
BINDIR ?= bin
CMD_PACKAGES := ./cmd/...

# SQLCipher's bundled codec uses OpenSSL. Override these for a non-Homebrew
# toolchain (Linux CI supplies system libssl-dev).
OPENSSL_PREFIX ?= $(shell brew --prefix openssl@3 2>/dev/null)
CGO_OPENSSL_FLAGS := $(if $(OPENSSL_PREFIX),CGO_CFLAGS="-I$(OPENSSL_PREFIX)/include" CGO_LDFLAGS="-L$(OPENSSL_PREFIX)/lib")

# Sync agent_docs to the static embed dir before any build
.PHONY: sync-docs
sync-docs:
	@../tools/sync-agent-docs.sh

# Local builds leave config.Version empty (no update offers) and report
# "<next patch>-dev+<commit>[.dirty]" instead (in /api/health and to Coral Hub),
# based on the latest release tag. Use make dev / make build to get that base: a
# plain `go build` reports 0.0.0-dev+<commit>, which hubs treat as a very old version.
DEV_BASE := $(shell git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' 2>/dev/null)
DEV_LDFLAGS := -ldflags "-X github.com/subgentic/coral/internal/config.DevBase=$(DEV_BASE)"

# Dev build (skips EULA + license)
.PHONY: dev
dev: sync-docs
	@mkdir -p $(BINDIR)
	$(CGO_OPENSSL_FLAGS) go build -trimpath -tags "dev fts5" $(DEV_LDFLAGS) -o $(BINDIR)/ $(CMD_PACKAGES)

# Production build
.PHONY: build
build: sync-docs
	@mkdir -p $(BINDIR)
	$(CGO_OPENSSL_FLAGS) go build -trimpath -tags fts5 $(DEV_LDFLAGS) -o $(BINDIR)/ $(CMD_PACKAGES)

# Dev build + run
.PHONY: run
run: dev
	./$(BINDIR)/coral --host 127.0.0.1 --port 8420

# Test server (accessible from other machines, isolated from production data)
.PHONY: test-server
test-server: dev
	CORAL_DATA_DIR=/tmp/coral-test ./$(BINDIR)/coral --host 0.0.0.0 --port 8450

# Run all tests
.PHONY: test
test: sync-docs
	go test ./...
	$(CGO_OPENSSL_FLAGS) go test -tags fts5 ./...

# Race detector on the packages with their own goroutines around process and PTY
# lifetimes (#3320). Slower than `make test`: run before changing session code.
.PHONY: test-race
test-race: sync-docs
	go test -race -timeout 30m ./internal/ptymanager ./internal/cloud/... ./pkg/...

# End-to-end frontend tests (headless Chrome + CDP). Requires node + Chrome.
.PHONY: test-frontend
test-frontend:
	@bash ../tests/frontend/run.sh
