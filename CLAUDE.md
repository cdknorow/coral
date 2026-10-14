# CLAUDE.md - Coral Go

## Mission

The Go codebase (`coral-go/`) is the **primary implementation** of Coral. The `coral-python/` is a legacy reference that is no longer authoritative.

**RULES:**
1. The Go codebase is the source of truth. Improve it freely without deferring to Python patterns.
2. Only modify code under `coral-go/` and `tests/`.

## Testing

### Go Unit Tests
```bash
cd coral-go && go test ./...
```

### Legacy Parity Tools (historical reference)
These tools were used during the Python-to-Go migration and may still be useful for comparison:

```bash
# Parity harness (requires both Python and Go servers)
python tests/parity_harness.py

# DB compare tool
cd coral-go && go build -o db-compare ./cmd/db-compare/
./db-compare <python-db> <go-db> [board-py-db] [board-go-db]
```

## Project Structure

```
coral/                  # Legacy Python implementation (historical reference only)
coral-go/               # Primary Go implementation
  cmd/                  # CLI entry points (coral, launch-coral, coral-board, db-compare)
  internal/             # Core packages
    agent/              # Agent implementations (claude, gemini)
    background/         # Background services (git poller, indexer, scheduler, etc.)
    board/              # Message board store
    config/             # Configuration
    jsonl/              # JSONL log reader
    license/            # EULA (terms acceptance)
    pulse/              # Pulse event parser
    ptymanager/         # PTY/tmux session management
    server/             # HTTP server, routes, frontend assets
    store/              # SQLite storage layer
    tmux/               # Tmux client
  go.mod / go.sum       # Go module dependencies
tests/                  # Parity test harness
  parity_harness.py     # Main harness — starts both servers, runs scenarios, compares DBs
  parity/
    test_scenarios.py   # API scenario tests (tags, settings, webhooks, board, etc.)
Casks/                  # Homebrew Cask definition
Formula/                # Homebrew Formula
scripts/                # macOS build script
icons/                  # App icons and screenshots
```

## Development Workflow

1. Implement or fix features in `coral-go/`.
2. Write or update Go unit tests.
3. Run `go test ./...` to ensure no regressions.
4. Build and manually verify as needed.

## Development

### Agent Docs
Agent docs live in `coral-go/agent_docs/` (single source of truth) and are synced to the static embed directory at build time. The Makefile handles this automatically. Production builds sync via `bundle-frontend.sh`.

### Building (via Makefile)
```bash
cd coral-go && make build       # production build
cd coral-go && make dev         # dev build (skips the EULA)
cd coral-go && make run         # dev build + run on :8420
cd coral-go && make test-server # dev build + run on :8450 (0.0.0.0)
cd coral-go && make test        # run all tests
```
All Makefile targets sync agent_docs automatically before building.

### Building (manual)
```bash
cd coral-go && go build -o coral ./cmd/coral/
```

### Dev Mode
```bash
cd coral-go && go build -tags dev -o coral ./cmd/coral/ && ./coral --host 127.0.0.1 --port 8420
```
The `dev` build tag skips the EULA.

### Test Server
To spin up a test server for manual verification, use `0.0.0.0` (not `127.0.0.1`) so it's reachable from other machines, and a non-default port to avoid conflicts.

**CRITICAL: Always use a separate data directory for test servers.** The production Coral instance uses `~/.coral/` — running a test server against the same database will corrupt live sessions, kill running agents, and lose state. Use `CORAL_DATA_DIR` to isolate test data:
```bash
cd coral-go && go build -tags dev -o coral ./cmd/coral/ && CORAL_DATA_DIR=/tmp/coral-test ./coral --host 0.0.0.0 --port 8450 --cloud-url off
```
`--cloud-url off` keeps a test server away from the real Coral Hub (a release build connects to app.coralai.ai by default, and a `CORAL_CLOUD_URL` in the shell would point it there).

### Database
- SQLite with WAL mode, stored at `~/.coral/sessions.db`
- Message board DB at `~/.coral/messageboard.db`
- **Never run test servers against the production `~/.coral/` directory.** Always set `CORAL_DATA_DIR` to a temp/test path when running manual test servers or integration tests that start a full server process.

## Releases

### Local Builds
Build installers for each platform from any OS:
```bash
./installers/build-windows.sh 0.7.0   # → installers/dist/coral-windows-amd64-0.7.0.zip
./installers/build-macos.sh 0.7.0     # → installers/dist/Coral-0.7.0.dmg (or .tar.gz on Linux)
./installers/build-linux.sh 0.7.0     # → installers/dist/coral-linux-amd64-0.7.0.tar.gz (add arm64 for coral-linux-arm64-…)
```

### Tagged Release (CI)
Pushing a tag triggers the GitHub Actions release workflow. Tag suffixes control build behavior:

#### Build Tiers

Tiers are selected via **compile-time build tags** (not ldflags). Coral has no licence and no usage limits in any tier. Tiers differ only in the EULA and the default Coral Hub (`TierDefaultCloudURL`: https://app.coralai.ai for prod, beta and staging; none for dev and dropboxers):

| Tier | Build Tag | EULA |
|------|-----------|------|
| Prod | (default) | Required |
| Dev | `-tags dev` | Skipped |
| Beta | `-tags beta` | Required (same as prod; kept so `-beta` tags still build) |
| Dropboxers | `-tags dropboxers` | Skipped ("Coral For Dropbox" partner build) |

#### Tag Naming Conventions

Every tag builds Linux, macOS and Windows. The suffix picks the build tier and whether the
release is a prerelease (never shown as "Latest", so installed Corals don't offer it as an
update):

| Tag Format | Tier | Release |
|---|---|---|
| `v0.x.x` | prod | **Latest** (use this for production) |
| `v0.x.x-dev` | dev (EULA skipped) | prerelease |
| `v0.x.x-beta` | beta (same as prod) | prerelease |
| `v0.x.x-forDropbox` | dropboxers (EULA skipped, "Coral For Dropbox" branding) | prerelease |
| `v0.x.x-rc…` | prod | prerelease |

The version is the tag name without the leading `v`, suffix included, so tag production
releases as plain `vX.Y.Z`: the old `-windows` / `-all` suffixes no longer change the build
and would end up in the version string.

#### Local builds with tiers
```bash
# Dev build (no EULA)
CORAL_TIER=dev ./installers/build-macos.sh 0.10.15

# Beta build (same as prod)
CORAL_TIER=beta ./installers/build-macos.sh 0.10.15

# Prod build (default — EULA required)
./installers/build-macos.sh 0.10.15
```

#### Example release flow
```bash
# Dev build (no EULA; prerelease)
git tag v0.10.15-dev
git push origin main --tags

# Beta build for partners (same as prod; prerelease)
git tag v0.10.15-beta
git push origin main --tags

# Production release (all platforms; the workflow creates the GitHub Release,
# uploads the packages, SHA256SUMS.txt and their Sigstore bundles, and attests provenance)
git tag v0.10.15
git push origin main --tags
```

The workflow (`.github/workflows/release.yml`) produces:
- **Linux**: `coral-linux-amd64-<version>.tar.gz` and `coral-linux-arm64-<version>.tar.gz` (each built and smoke-tested on its own native runner), each with a `.sigstore.json` bundle
- **Windows**: `Coral-<version>-x64.msi` + `Coral-<version>-x64-portable.zip`
- **macOS**: `Coral.dmg` (universal binary, signed + notarized if certs configured)

### Required GitHub Secrets (for signing)
- **macOS**: `MACOS_CERTIFICATE`, `MACOS_CERTIFICATE_PWD`, `MACOS_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_TEAM_ID`, `APPLE_APP_PASSWORD`
- **Windows**: `WINDOWS_CERTIFICATE`, `WINDOWS_CERTIFICATE_PWD`

Signing is optional — builds succeed without secrets, just unsigned.
