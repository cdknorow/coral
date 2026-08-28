# Troubleshooting

Symptoms observed while running the shipped v1.0.8 build, with the cause for each.
Every entry here is something that actually happened, not a hypothetical.

## First run

### The agent launched but produces no output and looks hung

The most common first-run problem. On its first run in a directory, the agent CLI asks its
own trust question and blocks:

```
Quick safety check: Is this a project you created or one you trust?
❯ No, exit
  Yes, I trust this folder
```

Codex asks an equivalent question. **Coral does not surface this as "waiting for input"** —
the agent appears alive and idle.

Open the agent's terminal in the dashboard and answer it, or over the API:

```bash
curl -s -X POST "http://localhost:8420/api/sessions/live/$SESSION/keys" \
  -H "Content-Type: application/json" -d '{"keys":["Down"],"agent_type":"claude"}'
curl -s -X POST "http://localhost:8420/api/sessions/live/$SESSION/keys" \
  -H "Content-Type: application/json" -d '{"keys":["Enter"],"agent_type":"claude"}'
```

Trust is remembered per repository. Launching a team of four agents into a fresh repo
blocks all four at once.

### I got a pricing page instead of the dashboard

Expected. Coral is free and fully unlocked; this is an activation reminder shown on the
1st, 4th, 7th … launch (`IsNagLaunch()` is `count % 3 == 1`, starting at 1). Click
**Continue Free**.

The skip is a URL parameter (`/?skip_activation=1`) and sets no cookie, so it can reappear
on reload.

### `coral: command not found` after installing the DMG

The DMG does not add anything to your `PATH`. Run:

```bash
/Applications/Coral.app/Contents/MacOS/install-cli.sh
```

If it fails or does nothing, that is a known bug: it symlinks into `/usr/local/bin`, which
does not exist on a clean Apple Silicon Mac, and `mkdir -p /usr/local/bin` under `set -e`
aborts for a non-root user. Use `sudo`, or symlink the binaries yourself:

```bash
ln -s /Applications/Coral.app/Contents/MacOS/coral       /opt/homebrew/bin/coral
ln -s /Applications/Coral.app/Contents/MacOS/coral-board /opt/homebrew/bin/coral-board
```

### `coral` starts something that doesn't look like Coral

The retired `agent-coral` PyPI package is shadowing it. It installs binaries with the same
names, uses the same `~/.coral` directory and the same `sessions.db` / `messageboard.db`
filenames, and listens on the same port 8420. On a default `PATH`, pip's `~/.local/bin`
comes before `/usr/local/bin`, so the Python one wins and *looks like it worked*.

```bash
which -a coral coral-board launch-coral   # more than one hit = both installed
pip uninstall agent-coral
```

### `coral --version` doesn't work

There is no version flag:

```console
$ coral --version
flag provided but not defined: -version
```

The version is in the startup log (`version="1.0.8"`), or via `/api/system/status`.

## Agents

### Agents launch but never post to the message board

Two separate bugs, both invisible on a default install:

1. **Wrong port.** Agents do not receive `CORAL_PORT` or `CORAL_URL`. The generated
   settings file contains only `CORAL_SESSION_NAME`, `CORAL_SUBSCRIBER_ID`, and `PATH`, so
   `coral-board` defaults to `http://localhost:8420` no matter what port your server uses.
2. **Wrong directory.** `coral-board` decides whether it is subscribed by reading
   `$HOME/.coral/board_state_<session>.json` (`cmd/coral-board/main.go:41-44`). It never
   asks the server. With `--home` set elsewhere, the server writes that file to its own
   data directory and the CLI cannot find it — reporting `Not subscribed to any board`
   while the server shows the agent correctly subscribed.

Confirm the server's view first:

```bash
curl -s http://localhost:<port>/api/board/<project>/subscribers
```

If the subscription exists server-side, the CLI is the problem. Use the REST API instead:

```bash
curl -s -X POST http://localhost:<port>/api/board/<project>/messages \
  -H "Content-Type: application/json" \
  -d '{"subscriber_id":"<name>","content":"your message"}'
```

The field is `content`, not `message` — empty content is rejected.

**Both bugs disappear on a default install** (`~/.coral`, port 8420).

### A whole team stalled and produced nothing

Agents told to coordinate over an unreachable board will troubleshoot it and then wait
indefinitely for an orchestrator that cannot reach them. They stay "running", consume
tokens, and produce no work, with no error anywhere in the UI. Check board reachability
first (above), then send a direct instruction:

```bash
curl -s -X POST "http://localhost:<port>/api/sessions/live/$SESSION/send" \
  -H "Content-Type: application/json" \
  -d '{"command":"Skip the board and do the task now.","agent_type":"claude"}'
```

### Two agents overwrote each other's code

Expected behavior on the **team-launch** path — worktrees there are per *team*, not per
agent, and are off by default.

- Default team: every agent runs in **your working directory on your current branch**.
- `worktree: true`: **one** worktree on `coral-team/<board>`, shared by the whole team.
- **Scheduled jobs are designed differently** — one worktree *per run*, defaulting to on —
  but see [Every scheduled job run fails](#every-scheduled-job-run-fails) before relying on it.

Two agents editing the same file will produce duplicated or conflicting code. In testing,
two agents asked for the same function produced:

```
./stringutil.go:38:6: Truncate redeclared in this block
```

Give agents non-overlapping files, split them across separate teams, or sequence their
edits over the board.

### `agent_type` was ignored and I got Claude

`GetAgent` (`internal/agent/agent.go:157-168`) is a switch over `gemini`, `codex`, and
`pi`, with `default:` returning Claude. Any unrecognized value **silently** starts a Claude
session and returns `ok: true`. Only `claude`, `codex`, `gemini`, and `pi` are valid.

### Agent launch fails with a tmux error

Coral does not fail at startup when tmux is missing — it logs a warning, the dashboard
loads normally, and only agent launch fails:

```
[startup] tmux not found — agents cannot be launched until tmux is installed (brew install tmux)
```

Install tmux, or start with `--backend pty`. Coral looks for tmux on `PATH`, in
`CORAL_TMUX_BIN`, in `/opt/homebrew/bin`, `/usr/local/bin`, `/usr/bin`, `/opt/local/bin`,
`/nix/var/nix/profiles/default/bin`, and via your login shell.

## Session history and cost

### Searching session history returns nothing

Full-text search across sessions does not work. The FTS index is never populated:
`FTSBody` (`internal/agent/agent.go:66`) is declared and read at
`internal/background/indexer.go:114-115` but never assigned, so `UpsertFTS` is never
called and `session_fts` stays empty.

Verified on two databases — a fresh test instance (0 FTS rows, 42 indexed sessions) and a
long-running production one (0 FTS rows, 56 indexed sessions). A term visible in a session
summary on screen returns zero hits.

Session history, auto-summaries, tags, and notes themselves work — only the search does
not. Browse and filter the history list instead.

### Cost shows for some agents but not others

Token and cost tracking works per agent, per session, and per team, with input, output,
and cache tokens broken out — but only Claude agents reported usage in testing.

In a mixed team launched and stopped together, the Claude agent reported real figures and
the Codex agent produced **no usage record at all**, even though its own session file
under `~/.codex/sessions` contained token counts. Codex support exists in
`internal/background/token_poller.go` (`extractCodexUsage`), so this is a runtime
ingestion failure rather than a missing feature — note that Codex names its rollout files
with its *own* UUID, which defeats the filename-matching strategy at
`token_poller.go:312`.

Do not read a team total as complete spend across vendors. A missing agent shows as
nothing rather than as an error.

### Every scheduled job run fails

Symptom: the job fires on schedule, but every run is recorded `failed` with

```
git worktree add failed: exit status 128: Preparing worktree (checking out 'main')
fatal: 'main' is already used by worktree at '/path/to/your/repo'
```

Cause: `internal/background/scheduler.go:551` runs `git worktree add <dir> <base_branch>`.
Git refuses to check out a branch that is already checked out in another worktree — and your
main checkout has `main` checked out. Since `base_branch` **defaults to `main`**
(`jobs.md`), the documented default configuration fails on every run, forever. Scheduling
itself is fine, so the job looks active in the UI and only the run records show the failure.

Workaround — point `base_branch` at a branch that is not checked out anywhere:

```bash
git branch scheduler-base          # create it once; do not check it out
```

then set `"base_branch": "scheduler-base"` on the job. Verified working: the per-run worktree
is created (`<repo>_task_run_<runID>`), the agent launches, and `cleanup_worktree` removes it
afterwards.

### A scheduled job ends in `killed` / `timeout` instead of `completed`

Expected with an interactive agent. `scheduler.go:611-620` records `completed` only when the
launch call returns; an interactive agent session does not exit after answering, so the run
runs until `max_duration_s` and is recorded `killed` with `exit_reason: timeout`. The agent's
work still happened. Whether any agent configuration exits cleanly enough to record
`completed` is untested.

### A webhook never fires

Coral saves a webhook config without validating the URL, then blocks it at send time. Any
loopback, private, link-local, or CGNAT address is rejected by SSRF protection
(`internal/httputil/ssrf.go`), on both `POST /api/webhooks/{id}/test` and the real dispatch
path (`internal/background/webhook.go:76`). There is no override or allowlist.

```console
$ curl -X POST .../api/webhooks/1/test
{"error":"webhook URL blocked: remote server URL resolves to a private or reserved IP address"}
```

So a webhook pointed at `http://127.0.0.1:9911` saves cleanly, shows as enabled, and can never
deliver. Webhooks require a publicly-resolvable endpoint. This also means webhook delivery
cannot be tested against a local listener.

## Running more than one instance

### My second instance lists agents I did not start

Session discovery merges the default tmux socket unconditionally —
`internal/tmux/client.go:212`, *"Always merge sessions from the default socket for
backward compatibility"* — with no flag to disable it. A second instance will **list, and
can kill,** the first instance's agents even with a separate `--home`, port, and database.

Check what a new instance can see before trusting it:

```bash
curl -s localhost:<port>/api/sessions/live
```

Sessions you did not create mean isolation is incomplete. Do not call `/kill`, `/restart`,
or `/send` against any session you did not launch yourself.

### The tmux backend silently stopped using your socket

A unix socket path over ~104 characters cannot bind. With a deep `--home`, tmux fails:

```console
$ tmux -S /very/long/path/.../tmux.sock ls
error connecting to ... (File name too long)
```

The server still logs `Using tmux terminal backend` and starts cleanly. Keep the data
directory short (`/tmp/coral-t1`). Confirm which backend an agent really used by checking
`tmux_session` in `/api/sessions/live` — the `backend` field in the launch response reports
`pty` even for tmux-backed sessions and cannot be trusted.

### Port already in use

```
port 8420 is already in use
```

Coral binds the port before opening the database. Use `--port`, and pair it with `--home`
so the two instances do not share state.

## Platform

### Windows

Not supported. No Windows artifact has ever been published, and the server does not
currently compile for Windows — `internal/background/workflow_runner.go` uses Unix-only
process-group syscalls (`syscall.Getpgid`, `syscall.Kill`, `SysProcAttr.Setpgid`).

The architecture is otherwise Windows-ready: `cmd/coral/main.go:60-63` defaults Windows to
a native PTY backend, so tmux is not required there — but that backend is **unexercised**;
nobody has observed it running an agent. On this test host it fails at spawn with
`operation not permitted`, which may be a sandbox restriction rather than a defect.

### Linux

The tarball is **x86-64 only** — there is no arm64 build. It contains six bare binaries
with no installer, service unit, or desktop entry, and no tray or desktop app; Linux is
CLI/server only.

### macOS Gatekeeper

Should not appear — the DMG is signed and notarized:

```console
$ spctl -a -vvv -t exec /Applications/Coral.app
/Applications/Coral.app: accepted
source=Notarized Developer ID
```

If you do get a warning, you likely have a modified or partially-downloaded copy. Verify
with the command above rather than bypassing it.
