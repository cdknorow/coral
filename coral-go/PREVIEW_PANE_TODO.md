# Preview Pane Consolidation — Handoff Notes

## What exists now

A new **bottom preview pane** with tabbed file viewer lives at the bottom of the right sidebar (`.agentic-state`). It was built to replace the old inline file preview that rendered inside the sidebar panels.

### New files
- **`internal/server/frontend/static/preview_pane.js`** — Tab management module. Exports `openPreviewTab(filepath, line)`, `closeTab`, `closeAllTabs`, `resetPreviewPane`, `initPreviewPane`. Renders markdown (via `marked` + `DOMPurify`), code (via `hljs`), HTML (sandboxed iframe), images, and media.
- **`internal/server/frontend/static/draggable.js`** — Reusable `makeDraggable` / `makeDelegatedDraggable` utilities with pointer capture, rAF batching, and CSS containment.

### Modified files
- **`live_session.html`** — Preview pane HTML (`#preview-pane-resize-handle`, `#preview-pane` with `.preview-tabs` + `.preview-body`) inserted inside `.agentic-state`, after the bottom agentic block.
- **`changed_files.js`** — Added `setPreviewPaneHandler(handler)` export. `openFilePreview()` now delegates to the preview pane for regular files. Coral artifact URIs (`coral://artifacts/...`) still go to the old `_openArtifactPreview` in the sidebar.
- **`app.js`** — Imports `preview_pane.js`, calls `initPreviewPane()` + `setPreviewPaneHandler(openPreviewTab)` at startup. Exposes `openPreviewTab`, `closePreviewTab`, `closeAllPreviewTabs`, `resetPreviewPane` on `window`.
- **`css/output.css`** — All CSS for the preview pane: resize handle, tab bar, tabs with icons/close buttons, content area, loading/error states, markdown/code/iframe styles.
- **`css/agentic.css`** — Added `.preview-pane` and `.preview-pane-resize-handle` to the `.agentic-state.collapsed` hide rules. Added `body.panel-dragging .agentic-state { transition: none; contain: strict; }`.

## What still needs to be done

### 1. Consolidate ALL preview sources into the tab pane

Currently, file previews from the **Browse** tab and **changed files list** route to `openPreviewTab`. But several other preview paths still render inline in the old sidebar panels:

- **Files tab** — clicking a changed file's preview icon calls `openFilePreview` → now routes to the preview pane. **But** the old inline pane (`_openInlinePane`) still exists and is still used as fallback when `_previewPaneHandler` is null. The old code in `_openInlinePane` (line ~984 in `changed_files.js`) renders into `#agentic-panel-files` with its own header, mode buttons, and body. This should be removed once all paths go through the preview pane.

- **Artifacts tab** — `_openArtifactPreview()` (line ~847 in `changed_files.js`) renders artifact previews into `#agentic-panel-files` or creates a mobile overlay. This includes Coral artifact URIs and team artifacts. These should open as tabs in the preview pane instead.

- **Team Artifacts** — `openTeamArtifactPreview()` (line ~784) delegates to `_openArtifactPreview`. Route this through the preview pane.

- **Browse tab** — file clicks from `file_explorer.js` (line 246) call `openFilePreview` directly (imported from `changed_files.js`). This already works via the handler.

### 2. Add mode buttons (edit, preview, diff) to preview pane tabs

The old inline preview had mode buttons: **diff** (show git diff), **preview** (rendered view), **edit** (CodeMirror editor with save). The new preview pane only has preview mode.

To add these, look at the existing implementations in `changed_files.js`:

- **`_updateModeButtons(activeMode)`** (line ~1276) — toggles active state on mode-btn-diff/preview/edit
- **`_switchMode(targetMode)`** (line ~1298, assigned to `window._switchMode`) — handles switching between diff, preview, edit modes
- **`_renderDiffView()`** (line ~1095) — fetches original file + current file and renders a CodeMirror merge view
- **`_createCmEditor()`** (line ~528) — creates an editable CodeMirror 6 editor
- **`_savePreviewFile()`** — saves edited content back to the agent's filesystem
- **Star button** — `_togglePreviewStar()` for starring files

The preview pane needs:
- A toolbar row in each tab (or a shared toolbar that updates per-tab) with: star, diff, preview, edit, save buttons
- Per-tab state tracking for `mode` (preview/edit/diff), `content`, `originalContent`, `hasDiff`
- The diff button should only appear when the file comes from the Files tab (changed files have diffs; browsed files don't)
- CodeMirror lazy loading via `getCm()` from `cm_util.js`

### 3. Key helper functions to reuse

All in `changed_files.js`:
- `_apiQs(filepath)` — builds query string with filepath + session_id
- `_agentName()` — gets current agent name
- `splitPath(filepath)` — splits into `{ dir, name }`
- `getLangFromPath(filepath)` — from `cm_util.js`, returns language name for syntax highlighting
- `_renderSandboxedPreview(body, opts)` — iframe-based HTML preview
- `_renderContentView(container, content, filepath, line)` — the core renderer (HTML/markdown/code)
- `_rawImageUrl(endpoint, filepath)` — builds image URL for preview
- `isImagePath()`, `isMediaPath()`, `renderImagePanes()`, `renderMediaPanes()` — from `utils.js`
- `DOMPURIFY_CONFIG` — from `utils.js`

### 4. Vendor libraries available (loaded globally)

- `marked` — markdown parser (global `marked.parse()`)
- `DOMPurify` — HTML sanitizer (global `DOMPurify.sanitize()`)
- `hljs` — syntax highlighting (global `window.hljs.highlightElement()`)
- CodeMirror 6 — lazy-loaded via `getCm()` from `cm_util.js` (returns `{ EditorView, EditorState, ... }`)

### 5. API endpoints

- `GET /api/sessions/live/{agentName}/file-content?filepath=X&session_id=Y` — returns `{ content, error }`
- `GET /api/sessions/live/{agentName}/file-content?filepath=X&raw=1` — raw binary (for images/media)
- `GET /api/sessions/live/{agentName}/file-original?filepath=X` — original (pre-change) content for diffs
- `GET /api/artifacts/{hash}` — Coral artifact content
- `PUT /api/sessions/live/{agentName}/file-write` — save edited file content

### 6. Architecture notes

- The app uses vanilla JS ES modules (no framework, no build step).
- `preview_pane.js` currently duplicates some rendering logic from `changed_files.js`. Once consolidated, the rendering should live in `preview_pane.js` and the old `_openInlinePane` / `_renderContentView` code in `changed_files.js` can be removed.
- The `setPreviewPaneHandler` pattern in `changed_files.js` avoids circular imports — `changed_files.js` doesn't import from `preview_pane.js`, it just calls the registered handler function.
- Mobile uses full-screen overlays for previews (`mobile-file-preview-overlay`). The preview pane approach may need a mobile fallback or the existing mobile overlay can be kept.
