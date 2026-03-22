# Coral Security Audit Findings

**Auditor:** Security Auditor (AI Agent)
**Date:** 2026-03-16
**Scope:** Full codebase review of Coral multi-agent orchestration system
**Severity Scale:** CRITICAL / HIGH / MEDIUM / LOW / INFO

---

## Summary

Coral is a local development tool that runs a web dashboard bound to `0.0.0.0:8420` by default. It manages AI coding agents via tmux and provides a web UI with full control over agent sessions, file system access, and command execution. The security model relies on being a trusted local tool, but several issues could be exploited if the dashboard is exposed to a network or via CSRF from a malicious webpage.

**Total Findings:** 15
- CRITICAL: 2
- HIGH: 4
- MEDIUM: 5
- LOW: 3
- INFO: 1

---

## CRITICAL

### C1: No Authentication or Authorization on Web Dashboard and API

**Location:** `src/coral/web_server.py` (entire application)
**Description:** The web dashboard and all API endpoints have zero authentication. Anyone who can reach port 8420 can:
- Send arbitrary commands to AI agents via `/api/sessions/live/{name}/send`
- Kill or restart agent sessions
- Launch new agents in arbitrary directories
- Read file diffs and directory contents
- Execute scheduled jobs with arbitrary prompts
- Access the filesystem browser (`/api/filesystem/list`)

**Impact:** Since the server binds to `0.0.0.0` by default (line 234), it is accessible from any network interface. On a shared network, any host can fully control the Coral instance. Even on localhost, the lack of auth enables CSRF attacks from malicious web pages.

**Recommendation:**
1. Default bind to `127.0.0.1` instead of `0.0.0.0`
2. Add a session-based authentication mechanism or at minimum a bearer token
3. Consider CORS restrictions to prevent cross-origin requests

---

### C2: Server-Side Request Forgery (SSRF) via Remote Board Proxy

**Location:** `src/coral/api/board_remotes.py:74-113`
**Description:** The proxy endpoints (`/api/board/remotes/proxy/{remote_server:path}/...`) accept an arbitrary `remote_server` URL from the user and make HTTP requests to it. The `remote_server` is a path parameter that is directly used to construct URLs for `httpx` requests with no validation.

```python
async def _proxy_get(remote_server: str, path: str, timeout: float = 5.0) -> dict | list:
    url = f"{remote_server.rstrip('/')}/api/board{path}"
    async with httpx.AsyncClient(timeout=timeout) as client:
        resp = await client.get(url)
```

**Impact:** An attacker can use this to:
- Scan internal network services (`http://192.168.1.x:port/...`)
- Access cloud metadata endpoints (`http://169.254.169.254/...`)
- Probe localhost services on the server machine
- Exfiltrate data from internal services

**Recommendation:**
1. Validate `remote_server` against an allowlist of registered remote servers
2. Block requests to private IP ranges (RFC 1918), link-local, and loopback
3. Require the remote_server to be pre-registered via the subscription API

---

## HIGH

### H1: Arbitrary File System Browsing via `/api/filesystem/list`

**Location:** `src/coral/api/system.py:57-72`
**Description:** The endpoint accepts any path and lists directory contents. While it filters out hidden directories (starting with `.`), it allows browsing any directory on the filesystem that the server process can read.

```python
@router.get("/api/filesystem/list")
async def list_filesystem(path: str = "~"):
    expanded = os.path.expanduser(path)
```

**Impact:** Combined with no authentication (C1), exposes the entire filesystem directory structure to anyone who can reach the server.

**Recommendation:** Restrict browsing to a configurable allowlist of base directories.

---

### H2: Arbitrary Command Execution via Send Command Endpoint

**Location:** `src/coral/api/live_sessions.py:485-496`
**Description:** The `/api/sessions/live/{name}/send` endpoint sends arbitrary text to tmux sessions. While this is the intended functionality, combined with no authentication and the ability to launch terminal sessions, this effectively provides unauthenticated remote code execution.

The `/api/sessions/launch` endpoint can also launch terminal sessions (`agent_type: "terminal"`) in arbitrary directories, providing a shell with no agent mediation.

**Impact:** Full RCE for any network client that can reach the dashboard.

**Recommendation:** Require authentication (see C1). Consider restricting terminal session launches to authenticated users only.

---

### H3: Webhook URL SSRF via Task API

**Location:** `src/coral/api/tasks.py:22-64`, `src/coral/tools/run_callback.py`
**Description:** The `/api/tasks/run` endpoint accepts a `webhook_url` parameter that is stored and later used to make HTTP POST requests. While the webhook configuration endpoint (`/api/webhooks`) validates URLs, the task API does not validate the `webhook_url` at all.

```python
config = {
    ...
    "webhook_url": body.get("webhook_url"),  # No validation
}
```

The `send_run_callback` function in `run_callback.py` posts to any URL without validation.

**Impact:** SSRF to internal services, cloud metadata endpoints, or other sensitive targets. The webhook fires multiple times (on status changes) with retry logic, amplifying the impact.

**Recommendation:** Apply the same URL validation from `webhooks.py:_validate_url()` to task webhook URLs.

---

### H4: Message Board Webhook URLs Not Validated

**Location:** `src/coral/messageboard/api.py:52-56`, `src/coral/messageboard/store.py:87-96`
**Description:** The message board subscription endpoint accepts a `webhook_url` with no validation. When messages are posted, the system fires HTTP requests to all subscriber webhook URLs (`_dispatch_webhooks`).

**Impact:** Similar to H3 — SSRF via webhook dispatch triggered by any board message.

**Recommendation:** Validate webhook URLs using the same logic as the main webhook configuration endpoint.

---

## MEDIUM

### M1: Path Traversal in File Diff Endpoint

**Location:** `src/coral/api/live_sessions.py:366-418`
**Description:** The `/api/sessions/live/{name}/diff` endpoint accepts a `filepath` query parameter and passes it directly to `git diff` and `os.path.join`:

```python
rc, diff_text, _ = await run_cmd(
    "git", "-C", workdir, "diff", base, "--", filepath, timeout=10.0,
)
# ...
full_path = os.path.join(workdir, filepath)
if os.path.isfile(full_path):
    with open(full_path, "r", errors="replace") as f:
```

While `git diff` is likely safe (operates within the repo), the fallback path for untracked files uses `os.path.join` which can be exploited with `../` sequences to read arbitrary files.

**Impact:** Read arbitrary files on the system if the agent's working directory is known.

**Recommendation:** Validate that the resolved `full_path` is within `workdir` (e.g., check `os.path.commonpath`).

---

### M2: Shell Injection via osascript in Terminal Attach

**Location:** `src/coral/tools/tmux_manager.py:411-428`
**Description:** On macOS, the `open_terminal_attached` function interpolates `session_name` (derived from tmux session data) directly into an AppleScript string:

```python
script = (
    f'tell application "Terminal"\n'
    f'    activate\n'
    f'    do script "{attach_cmd}"\n'  # attach_cmd contains session_name
    f'end tell'
)
```

Similarly in `launch_agents.sh` (lines 67-79), session names are interpolated into osascript heredocs.

**Impact:** If a tmux session name contains AppleScript injection characters (e.g., quotes), it could execute arbitrary AppleScript. The risk is low since session names are UUID-based, but the pattern is unsafe.

**Recommendation:** Properly escape or quote the session name before interpolation into AppleScript.

---

### M3: XSS Risk in innerHTML Rendering

**Location:** Multiple JS files (see grep results for `innerHTML`)
**Description:** Many frontend files construct HTML via template literals and assign to `innerHTML`. While most data paths use `escapeHtml()`, some rendering paths may not consistently escape all user-controlled data (e.g., agent names, display names, file paths, status messages).

Key files of concern:
- `static/live_jobs.js` — renders job names and display names
- `static/scheduler.js` — renders job configuration
- `static/webhooks.js` — renders webhook names and URLs
- `static/changed_files.js` — renders file paths
- `static/update_check.js` — renders version info

**Impact:** Stored XSS if an attacker can control agent names, display names, or other rendered data (possible via the unauthenticated API).

**Recommendation:** Audit all `innerHTML` assignments for consistent `escapeHtml()` usage. Consider using DOM APIs (`textContent`, `createElement`) for user-controlled data.

---

### M4: Auto-Accept Permission Bypass Mechanism

**Location:** `src/coral/api/live_sessions.py:788-813`, `src/coral/api/tasks.py:39-42`
**Description:** The task API automatically adds `--dangerously-skip-permissions` flag when `auto_accept` is enabled:

```python
if body.get("auto_accept", False):
    skip_flag = "--dangerously-skip-permissions"
    if skip_flag not in flags:
        flags = f"{skip_flag} {flags}".strip()
```

Additionally, there's a secondary auto-accept mechanism that sends "y" + Enter to tmux when notification events arrive (lines 788-813). Combined with no authentication, any network client can launch agents that auto-accept all permissions.

**Impact:** Automated agents with full system permissions can be launched by unauthenticated users.

**Recommendation:** Add authentication (C1). Log auto-accept usage prominently. Consider rate-limiting auto-accepts (already partially done with `DEFAULT_MAX_AUTO_ACCEPTS = 10`).

---

### M5: Unvalidated `flags` Parameter in Session Launch

**Location:** `src/coral/api/live_sessions.py:587-619`, `src/coral/tools/session_manager.py:672-767`
**Description:** The launch endpoint accepts an arbitrary `flags` list that is passed directly to the agent launch command:

```python
flags = body.get("flags", [])
# ...
cmd = agent_impl.build_launch_command(
    session_id, protocol_path,
    resume_session_id=resume_session_id,
    flags=flags,
)
```

In `build_launch_command` (claude.py:131-147), flags are joined into a command string that is sent to tmux via `send-keys`:

```python
if flags:
    parts.extend(flags)
return " ".join(parts)
```

**Impact:** An attacker could inject arbitrary flags or shell metacharacters into the command string. Since the command is sent via tmux `send-keys` (not `shell=True`), shell injection is limited, but unexpected flags could alter agent behavior.

**Recommendation:** Validate flags against an allowlist of known agent flags. Sanitize or reject flags containing shell metacharacters.

---

## LOW

### L1: Sensitive Data Exposure in Log Files

**Location:** `src/coral/tools/utils.py:13-14`, `src/coral/tools/session_manager.py:203`
**Description:** Agent logs are written to `/tmp/` (world-readable on most systems). These logs contain the full terminal output including potentially sensitive data like API keys, passwords, or file contents that agents work with.

**Impact:** Any local user can read agent activity logs in `/tmp/`.

**Recommendation:** Use `~/.coral/logs/` with restricted permissions (0700) instead of `/tmp/`.

---

### L2: Database File Permissions

**Location:** `src/coral/store/connection.py:8-9`
**Description:** The SQLite database at `~/.coral/sessions.db` is created with default umask permissions. It contains session history, webhook URLs (potentially with tokens), and agent activity data.

**Impact:** Other local users may be able to read the database.

**Recommendation:** Set restrictive file permissions (0600) on database files upon creation.

---

### L3: Error Messages Leak Internal Paths

**Location:** Various API endpoints
**Description:** Error responses often include full filesystem paths, internal error messages, and stack traces. Examples:
- `src/coral/api/live_sessions.py:182`: Returns `f"Agent '{name}' not found"`
- `src/coral/api/system.py:61`: Returns `f"Not a directory: {path}"`
- `src/coral/api/schedule.py:146`: Returns `str(e)` which can contain internal details

**Impact:** Information disclosure useful for further attacks.

**Recommendation:** Use generic error messages in responses; log detailed errors server-side only.

---

## INFO

### I1: SQL Injection Protection is Adequate

The codebase consistently uses parameterized queries throughout the SQLite store layer (`aiosqlite` with `?` placeholders). No raw string interpolation in SQL queries was found. The `check_unread` method in `messageboard/store.py` dynamically constructs WHERE clauses with LIKE patterns but still uses parameterized values. The one concern is the `update_webhook_config` endpoint which passes `**body` to the store, but since SQLite column names can't be injected via values, the risk is minimal.

---

## Appendix: Architecture-Level Observations

1. **No rate limiting** on any endpoint — an attacker could launch unlimited agents, create unlimited webhook deliveries, or flood the message board.

2. **No CORS configuration** — the FastAPI app has no CORS middleware, which means browsers will block cross-origin XHR/fetch by default (same-origin policy). However, form-based CSRF attacks are still possible since there's no CSRF token protection.

3. **No input size limits** on most POST body parameters — message content, prompts, and notes can be arbitrarily large.

4. **WebSocket endpoints have no authentication** — the terminal WebSocket (`/ws/terminal/{name}`) provides direct terminal access with no auth.

5. **The `update_webhook_config` endpoint passes `**body` directly to the store** (`webhooks.py:75`), which could allow setting arbitrary columns if the store method doesn't filter fields.
