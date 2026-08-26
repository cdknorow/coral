# LSP Code Intelligence POC

## Status

Implemented. Automated verification is complete, including the production
broker against real `gopls` v0.23.0. Manual validation of the live CodeMirror
DOM remains pending.

## Summary

Add project-aware code intelligence to Coral's CodeMirror editor by connecting it
to standard Language Server Protocol (LSP) servers.

The proof of concept supports Go through `gopls` and delivers:

- Hover previews for functions, methods, variables, types, and interfaces
- Go to definition
- Find references ("find usages")
- Opening and selecting locations in files outside the changed-files list
- Clear unavailable and failure states when `gopls` is missing or unhealthy

Although Go is the first supported language, the transport, process manager,
frontend client, and capability model must remain language-neutral. Adding
Python, JavaScript/TypeScript, and C later should primarily require declarative
server definitions plus narrowly scoped server-specific initialization options.

Planned follow-up servers:

| Language | Initial server | File extensions |
|---|---|---|
| Go | `gopls` | `.go`, `go.mod`, `go.work` |
| Python | Pyright (`pyright-langserver`) | `.py`, `.pyi` |
| JavaScript / TypeScript | `typescript-language-server` | `.js`, `.jsx`, `.mjs`, `.cjs`, `.ts`, `.tsx` |
| C | `clangd` | `.c`, `.h` |

`clangd` also supports C++, but C++ UX is not part of this POC.

## Implementation Summary

- `internal/lsp` contains the language-neutral registry, canonical path/URI
  handling, Content-Length JSON-RPC transport, subprocess lifecycle, shared
  manager, document leases, cancellation mapping, limits, recovery, and
  structured logging. Go-specific behavior is confined to the `gopls`
  registry entry.
- `internal/server/routes/lsp.go` provides the probe-only capability endpoint
  and allowlisted WebSocket broker. A supported installed server probes as
  `starting` with an empty capability map and canonical git-worktree
  `workspace`; the WebSocket ready envelope carries negotiated capabilities
  and the same workspace.
- `lsp_client.js`, `changed_files.js`, and `navigation_history.js` provide
  versioned synchronization, status/retry handling, sanitized hover,
  definition and reference navigation, arbitrary workspace-file opening, and a
  capped back stack.
- Processes share by canonical language workspace and server definition.
  Leases share by canonical document identity, while browser request IDs map
  per connection to subprocess IDs. Equivalent URI spellings and repeated
  cancellation cannot bypass these boundaries.
- Five-minute idle shutdown and explicit application shutdown are implemented.
  Startup, requests, graceful/forced shutdown, frames, stderr, documents,
  outstanding requests, and navigation results are bounded.
- Current limits are: 8 MiB browser frames, 5 MiB documents, 64 outstanding
  requests per connection, 1,000 navigation results, 15-second requests,
  16 MiB subprocess frames, 8 KiB/64-count headers, and 64 KiB stderr. Manager
  defaults are 10-second startup, 3-second shutdown, five-minute idle, and a
  60-second reaper tick.
- `internal/server/routes/lsp_ws_test.go` is the end-to-end WebSocket harness,
  using a fake subprocess, real git worktree, seeded session state, and injected
  registry.
- `internal/server/routes/lsp_gopls_test.go` drives the production registry and
  WebSocket route against real `gopls`, with clean skip behavior when the
  executable is absent.

## Problem

The current CodeMirror integration provides parsing and syntax highlighting for
the open file. It does not understand imports, packages, build configuration,
dependencies, or symbols in other files. Consequently, it cannot reliably
answer semantic questions such as:

- What declaration does this identifier resolve to?
- Where is this type or method used?
- What is the full signature and documentation for this symbol?
- Which file contains the definition?

The backend currently exposes raw file read/write endpoints but has no project
index or language-server lifecycle.

## Goals

1. Provide useful Go code intelligence in the inline editor.
2. Define one browser-to-Coral protocol that works for all LSP languages.
3. Isolate language servers by resolved workspace and server type.
4. Keep unsaved editor contents synchronized with the language server.
5. Reuse standard LSP types and methods where practical.
6. Make missing binaries, startup failures, and unsupported files visible
   without breaking normal editing.
7. Preserve Coral's existing worktree path and session isolation.

## Non-Goals

The POC does not include:

- Completion
- Signature help
- Diagnostics or a problems panel
- Rename symbol
- Formatting or code actions
- Workspace-wide symbol search
- Multiple open editor tabs
- Automatic language-server installation
- Bundling `gopls` inside the Coral application
- Remote language servers
- A Coral-specific symbol index

The architecture must not prevent these features from being added later.

## User Experience

### Hover Preview

When the pointer rests over a Go symbol, Coral requests `textDocument/hover` and
shows a small sanitized Markdown card containing the signature, type, and
documentation returned by `gopls`.

- The request is debounced.
- Moving away or changing the document closes the card.
- Stale responses are ignored.
- Unsupported symbols produce no card.
- The preview must remain within the editor viewport and be keyboard dismissible.

### Go to Definition

Command-clicking a symbol or invoking the editor command requests
`textDocument/definition`.

- One result opens that file and selects the returned range.
- Multiple results open a location picker.
- The editor can open any text file within the resolved workspace, even if it is
  not a changed file.
- A back action returns to the prior file and selection.
- Locations outside the workspace are rejected in the POC with an explanatory
  message. Dependency-source navigation can be added later.

### Find References

Invoking "Find references" requests `textDocument/references` with declarations
included.

Results appear in a panel grouped by relative file path. Each item contains:

- Relative file path
- One-based line and column
- A single-line source preview

Selecting a result opens the file and selects the range. Empty results show an
explicit "No references found" state.

### Availability

For supported files the UI exposes one of these states:

- `starting`
- `ready`
- `unavailable` with the expected binary and install guidance
- `failed` with a retry action
- `unsupported`

Editing, previewing, diffing, and saving continue to work when intelligence is
unavailable.

## Architecture

```text
┌───────────────────────────────────────────────────────────────┐
│ CodeMirror                                                   │
│                                                              │
│ LSP client extension                                         │
│  didOpen / didChange / didSave / didClose                    │
│  hover / definition / references                             │
│  position conversion / stale-response handling               │
└──────────────────────────────┬────────────────────────────────┘
                               │ WebSocket, JSON messages
                               ▼
┌───────────────────────────────────────────────────────────────┐
│ Coral LSP broker                                              │
│                                                              │
│ session authorization → workspace resolution → path checks    │
│ request routing → URI normalization → process lifecycle       │
└──────────────────────────────┬────────────────────────────────┘
                               │ LSP JSON-RPC over stdio
                               ▼
┌───────────────────────────────────────────────────────────────┐
│ Server process                                               │
│                                                              │
│ POC: gopls                                                   │
│ Later: pyright-langserver / typescript-language-server /      │
│        clangd                                                │
└───────────────────────────────────────────────────────────────┘
```

## Backend Design

### Package Structure

Introduce a language-neutral package:

```text
internal/lsp/
  manager.go       shared server instances and idle shutdown
  server.go        subprocess and JSON-RPC lifecycle
  protocol.go      browser envelope and minimal LSP wire types
  registry.go      language/server definitions
  paths.go         workspace, URI, and path validation
```

HTTP/WebSocket integration belongs in:

```text
internal/server/routes/lsp.go
```

The LSP package must not import session route types. Routes resolve the Coral
session to a workspace and pass a validated connection request to the manager.

### Server Registry

Language-specific behavior is expressed through a registry rather than branches
throughout the manager:

```go
type ServerDefinition struct {
    ID                 string
    Languages          []string
    Extensions         []string
    Command            string
    Args               []string
    RootMarkers        []string
    InitializationOpts func(workspace string) any
}
```

The first definition is conceptually:

```go
ServerDefinition{
    ID:          "gopls",
    Languages:   []string{"go"},
    Extensions:  []string{".go"},
    Command:     "gopls",
    RootMarkers: []string{"go.work", "go.mod"},
}
```

Server definitions may customize initialization and configuration, but process
management, JSON-RPC framing, browser routing, document synchronization, and
path validation remain shared.

Configuration must allow a future user setting to override the command and
arguments without changing the protocol.

### Workspace Resolution

The route first resolves the session's working tree using the same session-aware
logic as the file-content endpoints. The registry then walks upward from the
opened file, stopping at the session working-tree boundary, to find the nearest
root marker.

For Go, marker precedence is:

1. `go.work`
2. `go.mod`
3. Git/worktree root

The canonical workspace path is resolved through symlinks before it is used in a
server key.

### Server Identity and Sharing

The manager key is:

```text
(canonical workspace root, server definition ID)
```

Connections for the same workspace and server may share one process. Different
worktrees always produce different keys, even when they originate from the same
repository.

The manager tracks:

- Process state
- Connected browser clients
- Open documents and owning clients
- Pending request IDs
- Last activity
- Server capabilities

An idle server exits after a configurable timeout when it has no clients or open
documents. Coral sends `shutdown`, waits for the response, sends `exit`, and
force-terminates only after a short timeout.

### Document Ownership

LSP servers maintain one in-memory version per document URI. Two browser clients
editing the same workspace file with different unsaved contents cannot safely
share that URI.

For the POC:

- Only one client may hold an editable LSP document lease for a URI.
- A second client may open the file normally but code intelligence is disabled
  for that file with a conflict message.
- Closing the editor or WebSocket releases the lease.

This rule avoids silent cross-session corruption and can later be replaced with
a more sophisticated collaboration model.

### Process Discovery

The POC searches for `gopls` using Coral's effective executable `PATH`.

The probe distinguishes unsupported files, missing executables, and installed
servers. It does not start or initialize a process. Startup failure and ready
states are reported by the WebSocket lifecycle.

Coral must never construct a shell command. It launches the configured
executable and argument array directly.

### JSON-RPC Transport

The server transport implements LSP's `Content-Length` framing over stdio and
supports concurrent:

- Client requests and responses
- Server notifications
- Server-to-client requests
- Cancellation via `$/cancelRequest`

stderr is captured in a bounded diagnostic buffer and application logs. It is
never mixed with stdout protocol data.

Request IDs are generated by the broker. Browser-supplied IDs are scoped to a
connection and mapped to server IDs so separate clients cannot collide.

## Browser Protocol

### Endpoints

```text
GET /api/sessions/live/{name}/language-capabilities
WS  /api/sessions/live/{name}/lsp
```

Both require `filepath` and accept `session_id`; the file selects the server and
workspace before the WebSocket upgrade.

Example capability response:

```json
{
  "language": "go",
  "server": "gopls",
  "status": "starting",
  "workspace": "/canonical/path/to/worktree",
  "capabilities": {}
}
```

Only the WebSocket ready envelope reports `ready` with negotiated hover,
definition, and reference capabilities. It includes the same canonical
`workspace`.

The WebSocket carries a small Coral envelope instead of exposing unrestricted
raw LSP access:

```json
{
  "type": "request",
  "id": 12,
  "method": "textDocument/hover",
  "params": {}
}
```

```json
{
  "type": "response",
  "id": 12,
  "result": {}
}
```

```json
{
  "type": "error",
  "id": 12,
  "error": {
    "code": "server_unavailable",
    "message": "gopls is not installed"
  }
}
```

The POC allowlists these browser methods:

- `textDocument/didOpen`
- `textDocument/didChange`
- `textDocument/didSave`
- `textDocument/didClose`
- `textDocument/hover`
- `textDocument/definition`
- `textDocument/references`
- `$/cancelRequest`

Initialization is owned by the broker. The browser cannot send `initialize`,
workspace mutation methods, arbitrary commands, or custom server methods.

### Connection Lifecycle

1. Browser opens the WebSocket for a file.
2. Route resolves and validates the session, workspace, file, and server.
3. Manager gets or starts the server.
4. Broker initializes the server once and returns negotiated capabilities.
5. Browser sends `didOpen` with the current editor text and version `1`.
6. Browser sends ordered, versioned `didChange` notifications.
7. Browser sends semantic requests as needed.
8. Save sends the existing HTTP write followed by `didSave`.
9. Mode/file change sends `didClose`.
10. Socket close releases all document leases held by that connection.

## Frontend Design

### CodeMirror Extension

Add a language-neutral client module:

```text
internal/server/frontend/static/lsp_client.js
```

Responsibilities:

- Connection state and reconnect behavior
- Document lifecycle and monotonically increasing versions
- Debounced incremental or full-document changes
- Request IDs, cancellation, and timeouts
- UTF-16 LSP position conversion
- Capability checks
- Hover rendering
- Definition and reference commands

The POC may send full-document `didChange` events for simplicity. The protocol
and client API must permit incremental synchronization later if the server
advertises it.

The LSP extension is added by `_createCmEditor()` only when a supported server is
available. `_destroyCmEditor()` must close the document before destroying the
view.

### Navigation

Refactor file opening so both the changed-files UI and LSP locations call a
single function:

```js
openWorkspaceFile(filepath, options)
```

Options include:

```js
{
  mode: "edit",
  selection: {from, to},
  recordHistory: true
}
```

Maintain an in-memory navigation stack containing file path, selection, and
scroll position. This is separate from `_previewState` so later editor layouts
can reuse it.

### Location Results

LSP locations use URIs and zero-based UTF-16 ranges. Before display, the
frontend receives or derives:

- Workspace-relative path
- CodeMirror document offsets
- One-based display line and column

Reference source previews should be fetched in bounded batches. The POC may
reuse `GET /file-content`, but the follow-up implementation should add a
range/snippet endpoint if large result sets cause excessive reads.

## Security and Isolation

1. Every route resolves its workspace from the authenticated Coral session; the
   browser cannot supply an arbitrary workspace root.
2. Every document URI and returned navigation location is canonicalized and
   checked against the resolved workspace.
3. The WebSocket origin policy matches Coral's existing WebSocket policy.
4. Browser methods are allowlisted.
5. Request payloads, open-document size, outstanding requests, reference counts,
   and stderr buffers have explicit limits.
6. Server commands come from trusted registry/configuration, never request
   parameters.
7. The language server inherits Coral's full environment through
   `os.Environ()`, which supplies Go-toolchain settings such as `GOPATH`,
   `GOMODCACHE`, and `HOME`. No filtering is applied; narrowing the inherited
   environment is follow-up work.
8. Disconnects cancel outstanding browser requests and release document leases.

## Failure Handling

- Missing binary: return `unavailable`; do not repeatedly attempt startup.
- Startup timeout: stop the child and return `failed`.
- Process exit: fail pending requests, notify clients, and permit manual retry.
- Malformed server message: log a bounded excerpt, fail the process, and permit
  retry.
- Request timeout: cancel the LSP request and show a non-blocking UI error.
- Browser disconnect: cancel its pending requests and close its documents.
- File changed on disk while edited: retain the existing editor behavior for the
  POC and file a follow-up for conflict detection.
- Unsupported server capability: hide or disable the corresponding action.

The process manager must use contexts and bounded waits so server failures cannot
hang Coral shutdown.

## Observability

Structured logs include:

- Server definition ID
- Workspace path on process start
- Process start, ready, exit, and initialization failure with its error
- Request method and duration at debug level
- Forced-shutdown timeout warnings

Document content, hover text, and source previews are not logged.

## Testing

### Unit Tests

- LSP `Content-Length` reader/writer, including fragmented reads
- Concurrent request ID mapping
- Workspace root detection for `go.work`, `go.mod`, and Git fallback
- URI/path conversion, spaces, Unicode, Windows drive letters, and traversal
- Registry selection by extension
- Document lease acquisition and cleanup
- Process startup timeout and exit handling
- Method allowlist
- UTF-16 conversion for ASCII, emoji, and non-BMP characters

### Integration Tests

Use a small fake LSP subprocess checked into test fixtures. It must support
initialize, document lifecycle, hover, definition, references, shutdown, and
controlled failure. Tests must not require `gopls` to be installed.

Verify:

- One process is shared for two connections in the same workspace.
- Different worktrees receive different processes.
- Unsaved text reaches the fake server before a hover request.
- Disconnect closes documents and releases leases.
- A crashed server fails pending requests without hanging.
- Returned locations outside the workspace are rejected.

Optional `gopls` integration tests may run when `gopls` is available and should
be skipped otherwise.

### Frontend Tests

- Request cancellation and stale-hover suppression
- Document version ordering
- UTF-16 position conversion
- Definition opens the correct file and selection
- Multiple definitions render a picker
- References group by file and navigate correctly
- Navigation back restores prior selection
- Missing-server and reconnect states

## How to Test

Run from `coral-go/` (the Go module root):

```sh
command -v gopls && gopls version
go test -race ./internal/lsp/...
go test -race ./internal/server/routes/ -run 'TestWSLSP|TestCapability|TestSymlinked'
go test ./internal/server/routes/ -run TestGopls -v
node --test internal/server/frontend/static/*.test.mjs
node --check internal/server/frontend/static/lsp_client.js
node --check internal/server/frontend/static/changed_files.js
node --check internal/server/frontend/static/app.js
node --check internal/server/frontend/static/navigation_history.js
go test ./...
```

The recorded full-suite run had three unrelated/environmental failures; this
does not mean they always fail: `TestUpdateCheck` when build version metadata
was empty, a `coral-board` hostname assertion when the agent environment
injected a session name, and an order/timing flake in the board-task suite.
Classify current failures from their current evidence rather than assuming
these exact failures.

### Manual Real-`gopls` Scenario

Coral never installs language servers automatically. Discover `gopls` with
`command -v gopls`; install it, if desired, using the official Go command
`go install golang.org/x/tools/gopls@latest` or a package manager. This
verification ran against `gopls` v0.23.0 discovered on `PATH`. The real-server
tests self-skip when `gopls` is absent; when it exists only in `GOBIN`,
`GOPATH/bin`, or `~/go/bin`, the tests prepend that directory to the test
process's `PATH`. Coral's own discovery uses the effective `PATH`, so an
operator whose `gopls` is outside it sees `unavailable` until `PATH` includes
the executable's directory.

1. Create a temporary Go module with `main.go` calling a documented function
   in `helper.go`; commit it, and create a second git worktree.
2. Start Coral through the normal project workflow and open `main.go` in Edit.
3. Confirm intelligence moves from `starting` to `ready`.
4. Hover the cross-file call and confirm semantic signature and documentation.
5. Command-click the call. Confirm `helper.go` opens and the exact declaration
   range is selected.
6. Use Back and confirm the original selection and scroll position return.
7. Invoke Find References. Confirm results are grouped by relative path with
   one-based positions and that selecting one navigates correctly.
8. Rename the symbol only in the unsaved editor. Confirm subsequent semantic
   requests observe the unsaved buffer; save and confirm behavior after
   `didSave`.
9. Stop the `gopls` child. Confirm a failed/retry state appears while normal
   editing and saving remain usable; retry and confirm recovery.
10. Repeat with `gopls` absent and confirm `unavailable` is non-blocking.
11. Open the same file for editing in two browser clients. Confirm the second
    client receives a lease-conflict state.
12. Open equivalent files in both worktrees and confirm their processes and
    results remain isolated.

## Verification Record

At handoff, the `internal/lsp` and focused route suites were green under the
race detector, and the frontend suite was 38/38 green. Four real-language-server
tests also passed twice under the race detector in 13.325 seconds, and the
whole focused LSP route suite passed under the race detector in 13.242 seconds.
The real tests use `gopls` v0.23.0 through the production registry and
WebSocket route against a temporary two-file Go module. Coverage includes:

- the P0 bidirectional WebSocket `CloseRead` regression;
- canonical URI-alias lease isolation and symlinked-worktree navigation;
- translated, connection-scoped, repeated, late, and disconnect cancellation;
- manager shutdown, reaper termination, deadlines, and stubborn processes;
- zero-process repeated capability polling;
- deterministic stdin backpressure/response-dispatch deadlock;
- framing/header limits, failure recovery, path boundaries, and process sharing;
- UTF-16/non-BMP conversion, navigation modeling, and capped back history.
- real semantic hover with signature and documentation, cross-file definition
  selecting the identifier range, cross-file references, and unsaved-buffer
  results that differ from the unchanged file on disk;
- verified clean skip behavior when the real `gopls` executable is absent.

## Acceptance Criteria

- [x] A real `.go` workspace connects through the production broker to `gopls`.
- [x] Real semantic hover returns the cross-file signature and documentation.
- [x] Real definition resolves across files and selects the identifier range.
- [x] Real references return navigable declaration and usage locations.
- [ ] Live hover-card rendering, multi-target picker, references panel, and
  viewport restoration: protocol and model coverage is complete; live DOM
  evidence is pending.
- [x] Unsaved changes are ordered and versioned before semantic requests.
- [x] Navigation supports files outside the changed-files list.
- [x] Missing/crashed servers do not block ordinary editing.
- [x] Worktrees and canonical document leases are isolated.
- [x] Outside-workspace paths and non-allowlisted methods are rejected.
- [x] Transport, lifecycle, cancellation, limits, recovery, paths, UTF-16,
  navigation results, and history have automated coverage.
- [x] Core transport, manager, and browser protocol contain no Go-specific
  branches; Go behavior resides in the registry.

## Delivery Plan

### Phase 1: Transport and Lifecycle — Complete

- Add registry, process manager, JSON-RPC transport, and fake-server tests.
- Add workspace resolution and isolation.
- Add capability endpoint and WebSocket route.
- Implement the `gopls` server definition.

### Phase 2: Editor Synchronization and Hover — Complete

- Add the frontend client and CodeMirror integration.
- Implement document leases and lifecycle notifications.
- Add UTF-16 conversion and hover UI.

### Phase 3: Navigation and References — Complete

- Generalize opening arbitrary workspace files.
- Add definition navigation, multiple-location picker, and back history.
- Add grouped references UI and source previews.

### Phase 4: Hardening — Complete

- Add limits, timeouts, cancellation, idle shutdown, recovery, and structured
  logging.
- Complete automated integration testing, including real `gopls`. Manual DOM
  validation remains the required evidence item below.

### Follow-Up: Additional Languages

Add languages one at a time without changing the browser protocol:

1. Python with Pyright
2. JavaScript and TypeScript with `typescript-language-server`
3. C with `clangd`

For each server:

- Add a registry definition and root markers.
- Document installation and executable discovery.
- Add initialization/configuration options only where required.
- Add a fixture project and optional real-server integration test.
- Run the same hover, definition, references, unsaved-buffer, failure, and
  worktree-isolation acceptance suite.

Likely root markers:

| Server | Root markers |
|---|---|
| Pyright | `pyrightconfig.json`, `pyproject.toml`, `setup.py`, `.git` |
| TypeScript language server | `tsconfig.json`, `jsconfig.json`, `package.json`, `.git` |
| clangd | `compile_commands.json`, `compile_flags.txt`, `.clangd`, `.git` |

Root-marker precedence and server options belong to each registry definition,
not the shared manager.

## Expected Files

| File | Change |
|---|---|
| `internal/lsp/manager.go` | Shared process registry and lifecycle |
| `internal/lsp/server.go` | LSP subprocess and JSON-RPC transport |
| `internal/lsp/protocol.go` | Browser envelope and minimal protocol types |
| `internal/lsp/registry.go` | Go-first extensible server definitions |
| `internal/lsp/paths.go` | Root, URI, and workspace-boundary handling |
| `internal/lsp/*_test.go` | Unit and fake-server integration tests |
| `internal/server/routes/lsp.go` | Capabilities and WebSocket handlers |
| `internal/server/routes/lsp_test.go` | Capability and route unit tests |
| `internal/server/routes/lsp_ws_test.go` | Real WebSocket broker harness |
| `internal/server/routes/lsp_gopls_test.go` | Optional real-`gopls` integration tests |
| `internal/server/routes/sessions.go` | Session-owned LSP manager and registry |
| `internal/server/server.go` | Route registration and manager lifecycle |
| `internal/startup/startup.go` | Application-owned manager shutdown |
| `internal/server/frontend/static/app.js` | Workspace navigation actions exposed to the UI |
| `internal/server/frontend/static/lsp_client.js` | Language-neutral CodeMirror client |
| `internal/server/frontend/static/lsp_client.test.mjs` | Client protocol unit tests |
| `internal/server/frontend/static/lsp_client_behavior.test.mjs` | Client lifecycle tests |
| `internal/server/frontend/static/lsp_navigation.test.mjs` | Navigation result tests |
| `internal/server/frontend/static/navigation_history.js` | Pure capped navigation stack |
| `internal/server/frontend/static/navigation_history.test.mjs` | Navigation stack unit tests |
| `internal/server/frontend/static/navigation_history_behavior.test.mjs` | Back-stack tests |
| `internal/server/frontend/static/changed_files.js` | Editor lifecycle and navigation integration |
| `internal/server/frontend/static/css/agentic.css` | Hover, locations, and status UI |

## Resolved Defaults

- Intelligence is enabled by default; there is no first-release setting.
- Idle servers shut down after five minutes.
- Dependency locations outside the canonical workspace are rejected.
- Server command/argument overrides are deferred.
- The POC does not register `.h`; C/clangd support remains follow-up work.
- `gopls` root markers are `go.work` and `go.mod`; absence falls back to the
  canonical session worktree boundary rather than using `.git` as a marker.

## Remaining Work

### Required Evidence

- Run the full manual scenario above in the live CodeMirror editor. The real
  semantic protocol path is automated; hover-card rendering, multi-target
  picker behavior, references-panel behavior, and actual viewport restoration
  remain DOM-level evidence gaps. Failure/retry UI and browser-level isolation
  across two worktrees also remain to be smoked. These are evidence gaps, not
  known product defects.

### Follow-ups and Non-Goals

- Add Pyright, TypeScript, and clangd through registry definitions and their own
  acceptance suites.
- Revisit dependency-source navigation, command overrides, completion,
  diagnostics, rename, formatting, multiple editor tabs, and narrowing the
  language-server environment.
- Track the unrelated board-task order/timing test flake separately.
