# Troubleshooting

Symptoms with the cause for each. Statements about behavior were checked against the
v1.3.23 source. Some entries were first observed on older builds; where the current source
no longer shows the problem, the entry says so. Items marked *unverified* were not
re-tested on the current build.

## First run

### The agent launched but produces no output and looks hung

A common first-run situation. On its first run in a directory, the agent CLI asks its
own trust question and blocks:

```
Quick safety check: Is this a project you created or one you trust?
❯ No, exit
  Yes, I trust this folder
```

Codex asks an equivalent question. Coral may not show this as "waiting for input" (not verified
for every provider), so the agent can appear alive and idle.

Open the agent's terminal in the dashboard and answer it, or over the API:

```bash
curl -s -X POST "http://localhost:8420/api/sessions/live/$SESSION/keys" \
  -H "Content-Type: application/json" -d '{"keys":["Down"],"agent_type":"claude"}'
curl -s -X POST "http://localhost:8420/api/sessions/live/$SESSION/keys" \
  -H "Content-Type: application/json" -d '{"keys":["Enter"],"agent_type":"claude"}'
```

Trust is remembered per repository. Launching a team of four agents into a fresh repo
blocks all four at once.

### I got a supporter page instead of the dashboard

Coral is free and fully unlocked; this is a periodic supporter reminder. It is scheduled
every 25 launches, counted from the launch at which Coral first did something useful, and
never on that first launch (`nagInterval` in `internal/license/launch_counter.go`). Click
**Continue Free**.

It is shown to the first qualifying page load of a server start only
(`claimSupporterReminder` in `internal/server/server.go`), so reloading does not bring it
back until the next launch on the cadence. **Continue Free** links to `/?skip_activation=1`.
Dev and beta builds and activated licenses never show it.

### `coral: command not found` after installing the DMG

The DMG does not add anything to your `PATH`. Run:

```bash
/Applications/Coral.app/Contents/MacOS/install-cli.sh
```

It links the tools into `~/.local/bin` by default; pass `--dir <path>` (or set
`CORAL_LINK_DIR`) to choose another directory, and `--force` to replace files that are not
Coral symlinks. Make sure that directory is on your `PATH`. Older versions linked into
`/usr/local/bin`, which does not exist on a clean Apple Silicon Mac, and aborted.

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

The version is in the startup log, or in the `version` field of `/api/system/status`.

## Agents

### Agents launch but never post to the message board

Agents are launched with `CORAL_SESSION_NAME`, `CORAL_SUBSCRIBER_ID`, `CORAL_URL`,
`CORAL_PORT`, `CORAL_HOST`, `CORAL_DIR` and `CORAL_DATA_DIR` (`internal/agent/agent.go`), and
`coral-board` reads its subscription state from `CORAL_DATA_DIR`, so a server on another
port or `--home` normally works. Earlier builds did not pass the port or data directory;
if you see `Not subscribed to any board` on an old build, upgrade.

Confirm the server's view first:

```bash
curl -s http://localhost:<port>/api/board/<project>/subscribers
```

If the subscription exists server-side but the CLI disagrees, check that the agent's
environment was not cleared by a wrapper, then use the REST API as a fallback:

```bash
curl -s -X POST http://localhost:<port>/api/board/<project>/messages \
  -H "Content-Type: application/json" \
  -d '{"subscriber_id":"<name>","content":"your message"}'
```

The field is `content`, not `message` — empty content is rejected.

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

### An unknown `agent_type` is rejected

The launch API validates `agent_type` (`ValidateAgentType` in `internal/agent/agent.go`) and
returns an error listing the supported types instead of silently starting Claude. Valid
launch types are `claude`, `codex`, `agy` and `pi`; `gemini` is a deprecated alias for `agy`.
An empty `agent_type` uses the default (Claude).

### Agent launch fails with a tmux error

Coral does not fail at startup when tmux is missing — it logs a warning, the dashboard
loads normally, and only agent launch fails:

```
[startup] tmux not found — agents cannot be launched until tmux is installed (brew install tmux)
```

The macOS app packaging includes tmux and its terminal definitions. Older downloads
and standalone/source builds may still need tmux installed separately. Keep the app
bundle intact when moving it. Agent CLIs still need to be installed and authenticated
separately.

Coral checks an executable `CORAL_TMUX_BIN` override first, then the bundled tmux
on macOS, then `PATH`, common install locations, and your login shell. For a build
without bundled tmux, install it or start with `--backend pty`.

A running tmux server can outlive Coral. If an app upgrade reports a tmux client/server
version mismatch, preserve your running sessions and use `CORAL_TMUX_BIN` to select
the compatible tmux executable used before the upgrade. Restart the tmux server only
after finishing those sessions; killing it also terminates the processes inside it.
Coral does not automatically kill an older tmux server during discovery.

## Session history and cost

### Searching session history returns nothing

Earlier builds never populated the full-text index. The indexer now writes it whenever an
agent parser supplies searchable text (`UpsertFTS` in `internal/background/indexer.go`),
so search should return results for indexed sessions. *Unverified on a live database in
this review.* If a term you can see in a session returns nothing, the session may not
be indexed yet; browse and filter the history list meanwhile.

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

### Scheduled job runs and `git worktree add`

Earlier builds ran `git worktree add <dir> <base_branch>`, which Git refuses when that
branch is already checked out in your main checkout, so a default `main` base failed every
run. The current scheduler creates a per-run branch off the base
(`git worktree add -b coral/job-run-<runID> <repo>_task_run_<runID> <base_branch>`), so a
checked-out base branch is fine. If a run still records `git worktree add failed`, the
message from Git is shown with the run; check that `base_branch` exists. *Not re-run
end-to-end in this review.*

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

Session discovery merges tmux's default socket only for an install rooted at the default
`~/.coral` (so an upgrade does not lose old agents). For any other `--home`,
`defaultSocketFallback` (`internal/tmux/client.go`) turns the merge off. If you set
`CORAL_TMUX_FALLBACK=1` (or run a second instance on the default directory) the merge is on
and a second instance can list and kill the first instance's agents. `CORAL_TMUX_NO_FALLBACK=1`
forces it off.

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
`tmux_session` in `/api/sessions/live`, or the `terminal` field in the launch response. The
launch response's `backend` field names the launch path and reads `pty` even for
tmux-backed sessions.

### Port already in use

```
port 8420 is already in use
```

Coral binds the port before opening the database. Use `--port`, and pair it with `--home`
so the two instances do not share state.

## Platform

### Windows

No official Windows asset is published for the regular release. Windows-specific source
exists (for example `internal/background/process_windows.go`, and `cmd/coral/main.go` defaults
Windows to the native PTY backend), but the regular release process does not build or test it
and it is *unverified* here.

### Linux

The tarball is **x86-64 only** — there is no arm64 build. It contains bare binaries with no
installer, service unit, or desktop entry, and no tray or desktop app; Linux is CLI/server
only. tmux is not bundled; install it or start with `--backend pty`.

### macOS Gatekeeper

Should not appear — the DMG is signed and notarized:

```console
$ spctl -a -vvv -t exec /Applications/Coral.app
/Applications/Coral.app: accepted
source=Notarized Developer ID
```

If you do get a warning, you likely have a modified or partially-downloaded copy. Verify
with the command above rather than bypassing it.
