# Unified Changed Files View

## Problem

The changed files sidebar shows git diff output but has no agent attribution — you can't tell which agent edited which file. Additionally, file paths aren't easily copyable, making it hard to navigate to files in external editors.

## Goals

1. **Copy path button** — Add a clipboard copy button to each file row
2. **Agent attribution** — Show which agent(s) edited each file
3. **Agent-only files** — Surface files agents touched that aren't in the git diff (e.g., files outside the repo, temp files)
4. **History support** — Show changed files for past/history sessions, not just live ones

## Current Architecture

### Data Sources

**1. `git_changed_files` table** — populated by `RefreshFiles()` on demand:
```sql
CREATE TABLE git_changed_files (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_name        TEXT NOT NULL,
    session_id        TEXT,
    working_directory TEXT NOT NULL,
    filepath          TEXT NOT NULL,        -- repo-relative path
    additions         INTEGER DEFAULT 0,
    deletions         INTEGER DEFAULT 0,
    status            TEXT NOT NULL,        -- "M", "A", "D", "R", "??"
    recorded_at       TEXT NOT NULL
);
```

**2. `agent_events` table** — Write/Edit tool uses recorded by `coral-hook-agentic-state`:
```sql
-- Relevant fields for file tracking:
agent_name  TEXT     -- which agent
session_id  TEXT     -- which session
tool_name   TEXT     -- "Write" or "Edit"
summary     TEXT     -- "Edited main.go" (basename only)
detail_json TEXT     -- {"file_path": "/absolute/path/to/main.go"}
created_at  TEXT
```

**Key difference**: `git_changed_files.filepath` is repo-relative, `detail_json.file_path` is absolute. Must strip `working_directory` prefix to join.

### Current UI (`changed_files.js`, ~1120 lines)

Each file row renders:
```
[★] [status_icon] filename.go  dir/path/  [+42 -15]  [diff] [preview] [edit]
```

- Star button (localStorage persisted)
- Color-coded status icon (M/A/D/R/??)
- Filename + directory in separate spans
- Addition/deletion counts
- Three action buttons (shown on hover, always visible on mobile)

### Current API

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/sessions/live/{name}/files` | GET | Cached changed files |
| `/api/sessions/live/{name}/files/refresh` | POST | Fresh git diff, updates DB |

## Implementation

### Phase 1: Copy Path Button

**Frontend only — no backend changes.**

**File: `changed_files.js`**

Add a copy button to each file row's action buttons:

```html
<button class="file-action-btn" onclick="event.stopPropagation(); copyFilePath('{filepath}')" title="Copy path">
    <span class="material-icons">content_copy</span>
</button>
```

```javascript
function copyFilePath(filepath) {
    navigator.clipboard.writeText(filepath).then(() => {
        showToast('Path copied');
    });
}
```

Place it as the first action button (before diff/preview/edit) since it's the most commonly needed.

**File: `css/agentic.css`**

No new styles needed — reuses existing `.file-action-btn` class.

### Phase 2: Agent Attribution

**Backend: Extend `/files` and `/files/refresh` response**

**File: `internal/server/routes/sessions.go` — `RefreshFiles()`**

After computing the git diff files, query agent_events for Write/Edit tool uses in this session:

```sql
SELECT agent_name, 
       json_extract(detail_json, '$.file_path') as file_path,
       MAX(created_at) as last_edited_at
FROM agent_events 
WHERE session_id = ? 
  AND event_type = 'tool_use' 
  AND tool_name IN ('Write', 'Edit')
GROUP BY agent_name, json_extract(detail_json, '$.file_path')
```

For each result:
1. Strip `working_directory` prefix from absolute path to get repo-relative path
2. If file exists in git diff results → annotate with `agents` list and `last_edited_at`
3. If file NOT in git diff → add as `"agent_only"` status entry

**Updated response format:**

```json
{
  "files": [
    {
      "filepath": "internal/proxy/proxy.go",
      "additions": 45,
      "deletions": 12,
      "status": "M",
      "agents": [
        {"name": "Lead Developer", "last_edited_at": "2026-04-06T19:30:00Z"}
      ]
    },
    {
      "filepath": "/tmp/scratch.txt",
      "additions": 0,
      "deletions": 0,
      "status": "agent_only",
      "agents": [
        {"name": "QA Engineer", "last_edited_at": "2026-04-06T19:25:00Z"}
      ]
    }
  ]
}
```

**File: `internal/store/git.go`**

Add `ChangedFile.Agents` field:

```go
type FileAgent struct {
    Name         string `json:"name"`
    LastEditedAt string `json:"last_edited_at"`
}

type ChangedFile struct {
    Filepath  string      `db:"filepath" json:"filepath"`
    Additions int         `db:"additions" json:"additions"`
    Deletions int         `db:"deletions" json:"deletions"`
    Status    string      `db:"status" json:"status"`
    Agents    []FileAgent `json:"agents,omitempty"`
}
```

**Frontend: `changed_files.js` — `renderChangedFiles()`**

Update the file row to show agent names:

```html
<div class="file-item {statusClass}" ...>
    [★] [status_icon]
    <div class="file-path-wrap">
        <span class="file-name">proxy.go</span>
        <span class="file-dir">internal/proxy/</span>
        <span class="file-agents">Lead Developer</span>   <!-- NEW -->
    </div>
    [+45 -12] [copy] [diff] [preview] [edit]
</div>
```

Agent name shown as a subtle label below the file path. For multiple agents: `"Lead Developer, Frontend Dev"`.

For `agent_only` files, use a distinct status icon (e.g., robot icon or pencil) and muted styling since they have no git diff data.

**CSS additions (`css/agentic.css`):**

```css
.file-agents {
    display: block;
    font-size: 11px;
    color: var(--text-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
}

.file-item.file-agent-only {
    opacity: 0.7;
}

.file-agent-only .file-status-icon {
    background: var(--bg-tertiary);
    color: var(--text-secondary);
}
```

### Phase 3: History Session Support

**New endpoint: `GET /api/sessions/history/{sessionID}/files`**

For history sessions, we can't run `git diff` (the session is over). Instead, return agent-edited files from `agent_events`:

```sql
SELECT agent_name,
       json_extract(detail_json, '$.file_path') as file_path,
       MAX(created_at) as last_edited_at,
       COUNT(*) as edit_count
FROM agent_events
WHERE session_id = ?
  AND event_type = 'tool_use'
  AND tool_name IN ('Write', 'Edit')
GROUP BY json_extract(detail_json, '$.file_path')
ORDER BY last_edited_at DESC
```

Response format matches the live endpoint but all files have `status: "agent_only"` since we don't have git diff data for historical sessions.

Frontend: load this in `selectHistorySession()` and render in the history session view's files tab (if one exists, or add one).

## File Structure Summary

| File | Changes |
|------|---------|
| `pkg/frontend/static/changed_files.js` | Add `copyFilePath()`, render agent names, handle `agent_only` status |
| `pkg/frontend/static/css/agentic.css` | Add `.file-agents`, `.file-agent-only` styles |
| `internal/server/routes/sessions.go` | Extend `RefreshFiles()` to query agent_events and merge |
| `internal/store/git.go` | Add `FileAgent` struct, `Agents` field to `ChangedFile` |
| `internal/server/routes/history.go` | Add `GET /api/sessions/history/{sessionID}/files` endpoint |

## Edge Cases

1. **File edited by multiple agents** — Show all agent names comma-separated
2. **File reverted** — May appear in agent_events but not git diff. Show as `agent_only` with note
3. **File outside repo** — Absolute path doesn't match working_directory prefix. Keep absolute path, mark as `agent_only`
4. **Long-running sessions** — agent_events auto-pruned to 500 per agent. Old file edits may be lost. Acceptable tradeoff.
5. **No agent events** — Gracefully degrade to current behavior (git diff only, no agent tags)
6. **History sessions** — No git diff available, show agent-edited files only
