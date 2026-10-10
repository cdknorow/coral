# Git Diff Tree View — How It Works

## Overview

The File Preview Pane shows changed files for each agent session and lets users view diffs, preview content, or edit files inline. The system has three layers: a **Go backend** that queries git and agent events, a **SQLite cache** that persists the file list, and a **frontend** that renders the UI.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Frontend (changed_files.js)                            │
│                                                         │
│  loadChangedFiles() ──GET /files──> cached DB results   │
│  refreshChangedFiles() ──POST /files/refresh──> fresh   │
│                                                         │
│  Click file row ──> openFileDiff()                      │
│  Click preview icon ──> openFilePreview()               │
│  Click edit icon ──> openFileEdit()                     │
│                                                         │
│  _openInlinePane(filepath, mode)                        │
│    ├── 'diff'    → fetch /file-original + /file-content │
│    │               → CodeMirror unifiedMergeView        │
│    ├── 'preview' → fetch /file-content                  │
│    │               → syntax-highlighted <pre> block     │
│    └── 'edit'    → fetch /file-content                  │
│                    → CodeMirror editor (editable)       │
└─────────────────────────────────────────────────────────┘
         │                    │                  │
         ▼                    ▼                  ▼
┌─────────────────────────────────────────────────────────┐
│  Go Backend (sessions.go)                               │
│                                                         │
│  GET  /api/sessions/live/{name}/files                   │
│  POST /api/sessions/live/{name}/files/refresh           │
│  GET  /api/sessions/live/{name}/diff?filepath=          │
│  GET  /api/sessions/live/{name}/file-content?filepath=  │
│  GET  /api/sessions/live/{name}/file-original?filepath= │
│  PUT  /api/sessions/live/{name}/file-content?filepath=  │
└─────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────┐
│  SQLite Cache (git_changed_files table)                 │
│  Keyed by agent_name + session_id                       │
└─────────────────────────────────────────────────────────┘
```

## Backend: How the File List is Built

### 1. Working Directory Resolution (`resolveGitRoot`)

Every file-related endpoint needs to know where to run git commands. The resolution chain:

```
resolveGitRoot(name, sessionID)
  └── resolveWorkdir(name, sessionID)
        ├── 1. tmux pane's CurrentPath (live CWD of the agent)
        ├── 2. Latest git state snapshot for the session_id
        └── 3. Latest git state snapshot for the agent name
  └── git -C <workdir> rev-parse --show-toplevel
        ├── Success → use the git root
        └── Failure → scan one level of subdirectories for a .git repo
```

**Why `resolveGitRoot` exists:** The tmux pane's CWD may be a parent directory of the actual git repo (e.g., the pane is in `/project` but the repo is `/project/repo`). All git commands and path computations need the actual git root to work correctly.

### 2. Diff Base Resolution (`getDiffBase`)

Determines what commit to diff against:

```
getDiffBase(workdir)
  ├── Get current branch name
  ├── If on main/master → return "HEAD" (diff = uncommitted changes only)
  └── If on feature branch → return merge-base with main/master
        (diff = all changes since branching)
```

**On main:** `git diff HEAD` shows only uncommitted/staged changes.
**On a feature branch:** `git diff <merge-base>` shows all changes since the branch diverged from main.

### 3. File List Construction (`RefreshFiles`)

`POST /api/sessions/live/{name}/files/refresh` builds the file list from three sources:

#### Source 1: `git diff <base> --numstat`
Returns tracked files that have been modified since the diff base. Each line:
```
<additions>\t<deletions>\t<filepath>
```
These get status `M` (modified).

#### Source 2: `git ls-files --others --exclude-standard`
Returns untracked files (new files not yet added to git). These get status `??`.

#### Source 3: Agent Write/Edit events
The Claude/Gemini agent emits tool-use events when it writes or edits files. These are stored in the `agent_events` table. For each Write/Edit event:
1. Extract `file_path` from the event's detail JSON (absolute path)
2. Compute the path relative to the git root: `filepath.Rel(gitRoot, absolutePath)`
3. If not already in the file map from git, add it as `??` with line count as additions

**Why this matters:** An agent might write to a file that git doesn't yet track or that the git diff doesn't show (e.g., file was written then reverted). The event-based entries ensure the file appears in the list even if git doesn't see a diff.

#### Caching
The merged file list is written to `git_changed_files` in SQLite, keyed by `(agent_name, session_id)`. The `GET /files` endpoint reads from this cache. The `POST /files/refresh` endpoint rebuilds it.

### 4. Individual File Endpoints

#### `GET /diff?filepath=`
Runs `git -C <gitRoot> diff <base> -- <filepath>`. For untracked files, synthesizes a "new file" diff by reading the file and prefixing each line with `+`.

#### `GET /file-content?filepath=`
Reads the current file from disk: `filepath.Join(workdir, filepath)`. Has a path traversal check (resolved path must be under workdir).

#### `GET /file-original?filepath=`
Returns the original version from git: `git show <base>:<prefix><filepath>`. Uses `git rev-parse --show-prefix` to handle cases where the workdir is a subdirectory of the repo root.

#### `PUT /file-content?filepath=`
Writes content to disk. Same path traversal check.

## Frontend: How the UI Works

### File List (`changed_files.js`)

`renderChangedFiles()` renders each file as a row with:
- Status icon (`+` added, `~` modified, `-` deleted, `?` untracked)
- Filename + directory path
- Addition/deletion stats (`+123 -45`)
- Three action buttons (visible on hover): **Diff**, **Preview**, **Edit**

Clicking the row opens diff mode. The icons open their respective modes directly.

### Inline Preview Pane

When a file is opened, `_openInlinePane(filepath, mode)` replaces the file list with:
- **Header:** back button, filename, three mode buttons (diff/preview/edit), save button
- **Body:** content area that changes based on mode

The three modes:

| Mode | What it shows | Implementation |
|------|--------------|----------------|
| **Diff** | Side-by-side changes with syntax highlighting | Fetches `/file-original` + `/file-content` in parallel, creates CodeMirror `unifiedMergeView` (read-only) |
| **Preview** | Syntax-highlighted file content | Fetches `/file-content`, renders in `<pre><code>` with highlight.js |
| **Edit** | Full code editor | Fetches `/file-content`, creates CodeMirror `EditorView` (editable) with language detection |

Mode switching via `_switchMode(targetMode)`:
1. Save content from current CodeMirror instance (if any)
2. Destroy current CodeMirror instance
3. Render new mode

### CodeMirror Integration

All of CodeMirror 6 is bundled into a single 961KB file (`vendor/codemirror/codemirror-bundle.js`) built with esbuild. It includes:
- Core: `EditorView`, `EditorState`, `Text`, `basicSetup`
- Theme: `oneDark`
- Extensions: `search`, `unifiedMergeView`, `MergeView`
- 13 languages: JS/TS/JSX/TSX, Python, Go, Rust, Java, C/C++, HTML, CSS, JSON, Markdown, SQL, XML, YAML

Loaded lazily on first use via `import('codemirror-bundle')`.

### State Management

`_previewState` tracks the current preview:
```js
{
  filepath,           // relative path
  mode,               // 'diff' | 'preview' | 'edit'
  content,            // current file content (may be edited)
  originalContent,    // original content for merge view
  hasDiff,            // whether original differs from current
  gen,                // generation counter for race condition protection
}
```

`_previewGen` increments each time a new file is opened. Async callbacks check `_isStale(gen)` before writing to the DOM, preventing race conditions from rapid file clicks.

## Database Schema

```sql
CREATE TABLE git_changed_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_name TEXT NOT NULL,
    session_id TEXT,
    filepath TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT '',
    additions INTEGER NOT NULL DEFAULT 0,
    deletions INTEGER NOT NULL DEFAULT 0,
    working_directory TEXT NOT NULL DEFAULT '',
    recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

Indexed on `session_id` and `agent_name`. `ReplaceChangedFiles` does a DELETE + INSERT within a transaction to atomically replace the cached list.

## Key Files

| File | Role |
|------|------|
| `internal/server/routes/sessions.go` | All backend endpoints (Files, RefreshFiles, Diff, FileContent, FileOriginal) |
| `internal/store/git.go` | SQLite storage for changed files (`ChangedFile`, `GetChangedFiles`, `ReplaceChangedFiles`) |
| `pkg/frontend/static/changed_files.js` | Frontend: file list rendering, inline preview pane, CodeMirror integration |
| `pkg/frontend/static/css/agentic.css` | Styles for file list and preview pane |
| `pkg/frontend/static/vendor/codemirror/codemirror-bundle.js` | Self-contained CodeMirror 6 bundle |
| `pkg/frontend/templates/includes/views/live_session.html` | HTML structure for the agentic sidebar (files tab) |

## Known Issues

1. **Per-session vs shared git state:** The file list merges git state (shared across all agents in the same repo) with agent-specific Write/Edit events. Two agents working in the same repo will see the same git changes but different event-sourced files.

2. **Stale cache:** The `GET /files` endpoint reads from the DB cache. If files change between refreshes, the UI shows stale data until the next refresh (manual or via the background git poller).
