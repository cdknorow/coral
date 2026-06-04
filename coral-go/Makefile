# Coral Go — development build targets
BINDIR ?= bin
CMD_PACKAGES := ./cmd/...

# Sync agent_docs to the static embed dir before any build
.PHONY: sync-docs
sync-docs:
	@../tools/sync-agent-docs.sh

# Dev build (skips EULA + license)
.PHONY: dev
dev: sync-docs
	@mkdir -p $(BINDIR)
	go build -tags dev -o $(BINDIR)/ $(CMD_PACKAGES)

# Production build
.PHONY: build
build: sync-docs
	@mkdir -p $(BINDIR)
	go build -o $(BINDIR)/ $(CMD_PACKAGES)

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

# End-to-end frontend tests (headless Chrome + CDP). Requires node + Chrome.
.PHONY: test-frontend
test-frontend:
	@bash ../tests/frontend/run.sh
