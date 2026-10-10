# Update System

## Problem

Users have no way to know when a new version of Coral is available. The update
notification infrastructure exists in the frontend and tray app, but the backend
API returns hardcoded stub data. Users must manually check GitHub releases to
discover updates.

## Current State

The codebase already has significant update infrastructure wired up but not
connected to real data:

### Frontend Toast (working, waiting for real data)
- `pkg/frontend/static/update_check.js` — calls `/api/system/update-check`
  on app startup, shows dismissible toast notification with release notes
- Respects localStorage dismissal (`coral-update-dismissed`, `coral-update-check-enabled`)
- Displays first 5 release notes as bullet points
- Provides copy-to-clipboard button for upgrade command
- Styled in `css/components.css` (fixed top-right, animated entrance)
- Imported and called in `app.js` on startup

### Tray App Notification (working independently)
- `cmd/coral-tray/main.go` (lines 281-444)
- `checkForUpdatesOnStartup()` — 5-second delay, then silent check
- `checkForUpdates()` — user-triggered via "Check for Updates" menu item
- `fetchLatestVersion()` — queries GitHub API at
  `https://api.github.com/repos/cdknorow/coral/releases/latest`
- Shows native desktop notification via `beeep.Notify()`

### Backend API Stub (NOT IMPLEMENTED)
- `internal/server/routes/system.go` (lines 140-149)
- `UpdateCheck()` returns hardcoded: `available: false`, `current: "0.1.0-go"`
- Comment: "Go binary doesn't have a PyPI update mechanism yet"

### Version Injection (working)
- `internal/config/config.go` — `var Version string` set via ldflags at build time
- Release workflow injects: `-X github.com/cdknorow/coral/internal/config.Version=${VERSION}`

### Telemetry (working)
- `internal/tracking/posthog.go` — detects install vs upgrade by comparing stored
  version at `~/.coral/.install_version`
- Tracks `install`, `upgrade`, `app_opened` events with version info

## What's Missing

One function: `UpdateCheck()` in `system.go` needs to query GitHub releases
instead of returning hardcoded values. Everything else is already wired.

## Proposed Implementation

### UpdateCheck API (`internal/server/routes/system.go`)

Replace the stub with a real implementation:

1. **Query GitHub API**: `GET https://api.github.com/repos/cdknorow/coral/releases/latest`
2. **Parse response**: extract `tag_name` (version), `body` (release notes), `html_url`
3. **Compare versions**: semver comparison of `tag_name` vs `config.Version`
4. **Cache result**: store in memory with 1-hour TTL to avoid GitHub API rate limits
   (unauthenticated: 60 req/hr per IP)
5. **Return response**:

```json
{
  "available": true,
  "current_version": "0.10.14",
  "latest_version": "0.10.15",
  "release_notes": ["Fix cursor rendering", "Add file search", "..."],
  "download_url": "https://github.com/cdknorow/coral/releases/latest",
  "upgrade_command": "Download latest DMG from coralai.ai",
  "release_url": "https://github.com/cdknorow/coral/releases/tag/v0.10.15"
}
```

### Version Comparison

Use semantic versioning comparison. Strip leading `v` from tag names. Compare
major.minor.patch numerically. Pre-release suffixes (`-dev`, `-forDropbox`)
should be ignored for comparison purposes — a release tagged `v0.10.15-dev`
should not trigger update notifications for production users.

### Caching

```go
type updateCache struct {
    mu        sync.Mutex
    result    map[string]any
    fetchedAt time.Time
}
```

Cache the GitHub API response for 1 hour. On cache miss or expiry, fetch from
GitHub. On fetch failure (network error, rate limit), return cached result if
available, otherwise return `available: false` with no error.

### Platform-Specific Upgrade Instructions

The `upgrade_command` field should be platform-aware (detected via `runtime.GOOS`):

| Platform | Upgrade Instruction |
|----------|-------------------|
| macOS    | `Download latest DMG from coralai.ai` |
| Linux    | `curl -fsSL https://coralai.ai/install.sh \| sh` or download tar.gz |
| Windows  | `Download latest MSI from coralai.ai` |

## Upgrade Paths Per Platform

### macOS (DMG)
1. User sees update notification (toast or tray notification)
2. Downloads new DMG from GitHub releases or coralai.ai
3. Drags new Coral.app to /Applications (replaces old version)
4. Relaunches — new version runs, database migrations apply automatically
5. Future: consider Sparkle framework for auto-update (not for initial release)

### Linux (tar.gz)
1. User sees update notification in web UI toast
2. Downloads new tar.gz from GitHub releases
3. Extracts and replaces binary (e.g., `/usr/local/bin/coral`)
4. Restarts the server — migrations apply on startup
5. Alternative: `installers/install-cli.sh` script for one-command upgrade

### Windows (MSI)
1. User sees update notification (toast or tray notification)
2. Downloads new MSI from GitHub releases or coralai.ai
3. Runs MSI installer — overwrites existing installation
4. Relaunches — new version runs with migrations

### Homebrew (future)
- Formula at `Formula/coral.rb`, Cask at `Casks/coral.rb`
- `brew upgrade coral` when formula is updated
- Requires maintaining the Homebrew tap with each release

## Database Migration Strategy

### Current Approach (adequate for now)
- `internal/store/connection.go` lines 57-139
- `ensureSchema()` creates all tables on first run
- `columnMigrations` array defines schema additions via `ALTER TABLE ADD COLUMN`
- Errors ignored for existing columns (idempotent)
- Board migrations in `internal/board/store.go` follow same pattern

### Why This Works
- SQLite `ALTER TABLE ADD COLUMN` is always additive and idempotent
- No column renames or type changes needed so far
- Migration runs on every startup — no version tracking needed
- New columns have defaults or are nullable — old data remains valid

### Future Considerations
- If destructive migrations are needed (column renames, table restructuring),
  add a version-tracked migration system with up/down functions
- For now, the additive-only approach is sufficient and low-risk

## Security Considerations

Based on the security audit (session 2026-03-26):

1. **No auto-update**: Updates are user-initiated only (download + install).
   No self-modifying binary or automatic replacement. This avoids supply chain
   risks from compromised update servers.

2. **GitHub API over HTTPS**: Update check uses HTTPS to api.github.com.
   No sensitive data sent — just reads public release info.

3. **No TLS certificate pinning**: Uses default system CA store. A MITM with a
   rogue CA could serve a fake "no update available" response, but cannot force
   a malicious update since there's no auto-update mechanism.

4. **Rate limiting**: GitHub API limits unauthenticated requests to 60/hr per IP.
   The 1-hour cache prevents hitting this limit under normal usage. If rate
   limited, fail gracefully (return cached or "no update").

5. **Version spoofing**: A modified binary could report a fake version to suppress
   update notifications. This is acceptable — if someone modifies the binary,
   they've already bypassed all controls.

## Files Involved

| File | Role |
|------|------|
| `internal/server/routes/system.go` | UpdateCheck API (needs implementation) |
| `internal/config/config.go` | Version string (build-time injection) |
| `pkg/frontend/static/update_check.js` | Frontend toast notification |
| `pkg/frontend/static/css/components.css` | Toast styling |
| `pkg/frontend/static/app.js` | Startup check trigger |
| `cmd/coral-tray/main.go` | Tray app notification (independent GitHub check) |
| `internal/tracking/posthog.go` | Install vs upgrade detection |
| `internal/store/connection.go` | Database migration on startup |
| `.github/workflows/release.yml` | Release pipeline (version injection) |

## Implementation Plan

1. Add `updateCache` struct to `system.go` with mutex and TTL
2. Implement `fetchLatestRelease()` — HTTP GET to GitHub releases API, parse JSON
3. Implement semver comparison function (or use `golang.org/x/mod/semver`)
4. Replace stub `UpdateCheck()` with real implementation using cache
5. Add `runtime.GOOS` detection for platform-specific upgrade instructions
6. Test: verify toast appears when a newer version exists on GitHub
7. Test: verify cache prevents excessive API calls
8. Test: verify graceful degradation on network failure
