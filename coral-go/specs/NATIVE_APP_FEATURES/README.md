# Spec: Native App Features — Search & Open in Editor

## Context

Coral's native desktop app (macOS via WKWebView, Windows via WebView2) currently has minimal native integration — just external link opening and debug logging. The current file search runs entirely through the Go backend API and web UI. Users want a faster, more native-feeling search experience (Cmd+P style) and the ability to open files directly in their editor from search results.

The native app already has an extensible binding system (`w.Bind()` in `cmd/coral-app/main.go`) and a platform abstraction layer (`platform/detect.js`, `platform/native.js`, `platform/macos.js`, `platform/windows.js`) that's ready for new capabilities.

## Scope

1. **Native file search (Cmd+P)** — Fast file search within the agent's working directory, triggered by keyboard shortcut
2. **Open in editor** — Open files from search results in the user's preferred editor
3. **Editor preference in settings** — Configurable editor choice in Coral settings UI

## 1. Native File Search (Cmd+P)

### Keyboard Shortcut
- **macOS**: Cmd+P (matches VS Code convention)
- **Windows/Linux**: Ctrl+P
- Only active in native app mode (`platform.isNative`)
- In browser mode, falls back to existing top-bar search behavior
- Must not fire when terminal (xterm.js) is focused and actively receiving input — check `document.activeElement`

### Search Behavior
- **Scope**: Files only, within the current agent's working directory
- **Backend**: Reuse existing `GET /api/sessions/live/{name}/search-files?q=...` endpoint — already supports fuzzy search with scoring
- **Frontend**: New modal overlay (not the existing top-bar dropdown) with:
  - Large centered input field (VS Code Cmd+P style)
  - Results list below with keyboard navigation (Arrow Up/Down, Enter to select)
  - Escape to dismiss
  - File icon + relative path display
  - Debounced input (150ms)
  - Max 50 results displayed

### New Files
- `coral-go/pkg/frontend/static/command_palette.js` — Search modal logic, keyboard shortcut handler, result rendering
- `coral-go/pkg/frontend/static/css/command_palette.css` — Modal styling

### Modified Files
- `coral-go/pkg/frontend/static/app.js` — Import and init command palette
- `coral-go/pkg/frontend/templates/index.html` — Add modal container div

### Implementation Details

**command_palette.js:**
```
- Global keydown listener for Cmd+P / Ctrl+P
- Guard: skip if inside xterm terminal or modal is already open
- Show overlay modal with search input (auto-focused)
- On input: debounced fetch to /api/sessions/live/{name}/search-files?q=...
  - Use current session's name from state.currentSession
  - If no active session, search against first available session or show message
- Render results as selectable list items
- Arrow keys navigate, Enter selects, Escape closes
- On select: insert file path into chat input (existing @mention behavior) OR open in editor (see section 2)
```

**Result item actions:**
- Click / Enter — insert file as @mention in chat input (default)
- Cmd+Enter / button — open in editor (see section 2)

## 2. Open in Editor

### Go Binding
Add new binding in `cmd/coral-app/main.go`:

```go
w.Bind("_coralOpenInEditor", func(filePath string, line int) error {
    editor := getConfiguredEditor() // from settings
    // Launch editor with file:line
    return exec.Command(editor, formatEditorArgs(filePath, line)...).Start()
})
```

### Editor Detection & Configuration
- **Settings key**: `editor` in Coral settings (stored in existing settings system)
- **Values**: `"auto"` (default), `"vscode"`, `"cursor"`, `"zed"`, `"sublime"`, `"vim"`, `"emacs"`, `"custom"`
- **Auto-detect order**: `$VISUAL` -> `$EDITOR` -> check if `code` exists in PATH -> system default (`open` on macOS, `xdg-open` on Linux)
- **Custom**: User provides the command in settings (e.g., `/usr/local/bin/nvim`)

### Editor argument formats
Each editor has its own line-number syntax:
- VS Code / Cursor: `code --goto file:line`
- Zed: `zed file:line`
- Sublime: `subl file:line`
- Vim/Neovim: `vim +line file`
- Emacs: `emacs +line file`
- Custom: `{command} {file}` (no line support unless configured)

### New/Modified Files
- `coral-go/cmd/coral-app/main.go` — Add `_coralOpenInEditor` binding
- `coral-go/cmd/coral-app/editor.go` — Editor detection, argument formatting, launch logic
- `coral-go/cmd/coral-app/editor_test.go` — Tests for editor detection and arg formatting

### Platform Capability
Add to `platform/detect.js`:
```js
capabilities: {
    ...existing,
    openInEditor: false,  // true when platform.isNative
}
```

### Frontend Integration
- Add "Open in Editor" button/icon to command palette search results
- Cmd+Enter on a result opens it in editor instead of inserting as @mention
- In browser mode, hide the "Open in Editor" option (capability check)

## 3. Editor Settings UI

### Settings Integration
Add editor preference to the existing settings panel.

### Modified Files
- `coral-go/internal/server/routes/settings.go` — Handle `editor` setting read/write
- `coral-go/internal/store/settings.go` — Store editor preference
- Settings modal HTML/JS — Add editor dropdown

### UI
- Dropdown with options: Auto-detect (default), VS Code, Cursor, Zed, Sublime Text, Vim, Emacs, Custom
- When "Custom" selected, show text input for command path
- Preview text showing what will be used: "Will use: /usr/bin/code"

## 4. Implementation Order

### Phase 1: Command Palette + File Search
1. Create `command_palette.js` and `command_palette.css`
2. Add keyboard shortcut handler (Cmd+P / Ctrl+P)
3. Build search modal UI with results list
4. Wire up to existing search-files API
5. Add modal container to `index.html`
6. Import and init in `app.js`

### Phase 2: Open in Editor
1. Create `editor.go` with detection/launch logic
2. Add `_coralOpenInEditor` binding in `main.go`
3. Add `openInEditor` capability to `detect.js`
4. Add Cmd+Enter action to command palette results
5. Add editor button to search result items

### Phase 3: Editor Settings
1. Add `editor` to settings store
2. Add settings API endpoint
3. Add editor dropdown to settings modal UI
4. Wire editor preference to `_coralOpenInEditor` binding

## 5. Verification

### Testing
- `editor_test.go` — Unit tests for editor detection logic and argument formatting
- Manual testing:
  1. Open native app, press Cmd+P — palette appears
  2. Type filename — results appear with fuzzy matching
  3. Enter — file inserted as @mention
  4. Cmd+Enter — file opens in configured editor
  5. Escape — palette dismisses
  6. Settings -> change editor -> Cmd+Enter uses new editor
  7. Browser mode — Cmd+P still works but "Open in Editor" hidden

### Edge Cases
- No active session -> show "Select an agent first" message
- Editor not found in PATH -> show error toast
- Terminal focused -> Cmd+P should NOT fire (xterm intercepts it)
- Multiple rapid Cmd+P presses -> debounce, don't stack modals

## Architecture Reference

### Existing Bindings (`cmd/coral-app/main.go`)
- `_coralOpenExternal(url)` — Open URL in system browser
- `_coralLog(level, msg)` — Debug logging (debug mode only)

### Platform Layer (`pkg/frontend/static/platform/`)
- `detect.js` — Platform detection and capability flags
- `native.js` — Shared native init (health check, link interceptor)
- `macos.js` — WKWebView-specific fixes (placeholder)
- `windows.js` — WebView2-specific fixes (placeholder)
- `browser.js` — Browser-specific (placeholder)

### Existing Search API
- `GET /api/sessions/live/{name}/search-files?q=...` — Fuzzy file search (sessions.go:1018-1103)
- `GET /api/sessions/live/{name}/search-files?dir=...` — Directory browsing
- Supports scoring: basename exact > basename contains > path contains
- Returns up to 50 results
- Filters binary files, hidden files, files > 1MB
