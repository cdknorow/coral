# Troubleshooting

Symptoms with the cause for each. Statements about behavior were checked against the
v1.4.3 source. Some entries were first observed on older builds; where the current source
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
# NAME and SID are the agent's "name" and "session_id" from GET /api/sessions/live;
# PORT is your server's port (8420 by default).
curl -s -X POST "http://localhost:$PORT/api/sessions/live/$NAME/keys" \
  -H "Content-Type: application/json" -d "{\"keys\":[\"Down\"],\"agent_type\":\"claude\",\"session_id\":\"$SID\"}"
curl -s -X POST "http://localhost:$PORT/api/sessions/live/$NAME/keys" \
  -H "Content-Type: application/json" -d "{\"keys\":[\"Enter\"],\"agent_type\":\"claude\",\"session_id\":\"$SID\"}"
```

Trust is remembered per repository. Launching a team of four agents into a fresh repo
blocks all four at once.

### A "Connect to Coral Hub" screen covers the dashboard

That is the one-time first-launch screen. Click **Skip for now** (or press Escape) to keep
Coral local; it does not come back, and nothing is sent to the hub. The server marks a new
install `welcome_pending` in its settings and the dashboard clears it with your answer
(`welcome_choice`: `connected`, `skipped` or `own-hub`), so another browser does not ask
again. **Settings › About › Connect to Coral Hub…** reopens it. It only opens when a hub is
configured, and test servers set `CORAL_NO_WELCOME=1` and never show it.

Opened from another computer (a phone, or the LAN address), it says connecting has to
happen on the computer running Coral: run `coral cloud connect` there.

### Which hub Coral connects to, and how to turn it off

Release builds offer **https://app.coralai.ai**; dev builds offer none. `--cloud-url
<origin>` or `CORAL_CLOUD_URL` choose another; `--cloud-url ""` or `CORAL_CLOUD_URL=off`
(also `none`, `0`, `false`) turn the connector off. Precedence: `--cloud-url`, then
`CORAL_CLOUD_URL`, then the build's default. A configured hub is only an address: Coral
makes no connection to it until you click **Connect** (or run `coral cloud connect`). The
startup log says which hub it is: `[CLOUD] Coral Hub: https://app.coralai.ai (nothing is
sent until this server is connected: Settings > Coral Hub)`.

Coral has no licence, no supporter reminder and no usage limits in any build (#3221).

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

The version is in the `[STARTUP]` log line, or in the `version` field of `GET /api/health`.
`/api/system/status` also has a `version` field, but it is empty on dev and local builds.

### Version shows `0.0.0-dev+<commit>`

You built with plain `go build`. `make dev` and `make build` stamp the latest release tag, so the
build reports the next patch, `<next>-dev+<commit>[.dirty]` (e.g. `1.4.3-dev+<commit>` after
v1.4.2; `ReportedVersion` in `internal/config/version.go`). This is the version shown in
`/api/health`, the startup log and the Coral Hub tunnel Hello. Hubs treat `0.0.0-dev` as a very
old version: the Coral Hub then connects with the plain `/ws/coral` feed instead of `feed=hub`, so
that machine's dashboard notifications are taken by the hub's feed and never shown locally, and
the hub shows it as needing an update. Build with `make build` or `make dev`.

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
# NAME and SID are the agent's "name" and "session_id" from GET /api/sessions/live.
curl -s -X POST "http://localhost:<port>/api/sessions/live/$NAME/send" \
  -H "Content-Type: application/json" \
  -d "{\"command\":\"Skip the board and do the task now.\",\"agent_type\":\"claude\",\"session_id\":\"$SID\"}"
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

### Running a test server or the frontend suites

Never point a test server at `~/.coral`; that is the live instance's database. Give a manual
test server its own data directory and port:

```bash
CORAL_DATA_DIR=$(mktemp -d) ./coral --host 127.0.0.1 --port PORT
```

The frontend CDP suites read their server from `CORAL_TEST_URL` (`tests/frontend/test_base.js`),
never `CORAL_URL`: shells a running Coral spawns export `CORAL_URL` pointing at the live server.
A URL on port 8420, or with no explicit port, is refused. Prefer the runners, which build a dev
binary, start an isolated server with a temp data directory and set `CORAL_TEST_URL` for you:
`tests/frontend/run.sh` (port from `CORAL_TEST_PORT`, default 8462) or
`node tests/frontend/run_cdp_suites.mjs [--only name] [--shards N]` (N isolated server+Chrome
pairs).

### Port already in use

```
port 8420 is already in use
```

Coral binds the port before opening the database. Use `--port`, and pair it with `--home`
so the two instances do not share state.

### Stopping a test server: never kill by port

Stop a server you started by its PID (`$!` right after starting it in the background), or with
Ctrl-C. Never stop it with `lsof -ti:PORT | xargs kill`:

- `lsof -ti:PORT` matches ANY process with that port on EITHER end of a connection, including
  another program's outbound HTTPS socket that happens to use it as its local port. On a busy
  machine that can be an agent's own CLI, which then exits.
- Ports 49152-65535 (macOS's ephemeral range; Linux's starts at 32768) are handed out for
  outbound connections all the time, so never pick a test port there. Use something like 8450-8499.

If you must find a server by port, match only the listening socket and check what it is before
killing it:

```bash
lsof -nP -iTCP:PORT -sTCP:LISTEN     # who LISTENS on PORT (not who connects from it)
lsof -ti tcp:PORT -sTCP:LISTEN       # just the PID(s), for a script
```

### Never kill by name or pattern

Never use `pkill`, `killall` or `pgrep ... | xargs kill` to stop something. Kill only PIDs you
started and recorded (`kill <pid>`, or a tree walked down from that PID).

- Coral starts agents with long command lines, so almost any pattern (`cat`, `node`, `go`, a
  path) also matches some agent's CLI on the machine, on any team.
- On macOS, `pkill` and `pgrep` stop reading options at the first pattern: in
  `pkill -f 'cat' -P $$`, the `-P` and the PID become MORE patterns, not a parent filter. One such
  command once stopped the agent CLIs of three teams at once.
- To look, not kill: `pgrep -lf PATTERN` with every option BEFORE the pattern, then kill the PIDs
  you have checked by hand.

### Seeing exactly what Coral types into an agent's terminal

If an agent did something you didn't expect (it exited, or ran a command nobody typed), turn on
**Settings > Log terminal input (debug)**, or start Coral with `CORAL_DEBUG_INPUT=1` or
`--debug-input`. It is off by default. While it is on, every write Coral makes to an agent's
terminal goes to `<data dir>/input-debug.log`, one line each: time, terminal, the code path that
sent it (`source=`, for example the board notifier or the dashboard's composer), how it was sent
(text, key names such as `C-c` or `Enter`, a paste, or raw bytes), and the text, escaped and cut
at 200 characters.

- It records typed text, which may include secrets. The file is readable only by you (mode
  0600) and capped at 5 MB with one older copy (`input-debug.log.1`). `coral.log` and the
  Settings page say when it is on. Turn it off when you are done.
- The Settings toggle applies at once. `CORAL_DEBUG_INPUT=1` and `--debug-input` turn it on
  whatever the setting says.
- Turning it off keeps the file. Settings then shows **Delete the log**, which removes it and its
  `.1` copy (`DELETE /api/system/debug-input-log`: only from the machine running Coral, and only
  while the log is off).
- Logging never slows input down: if the disk can't keep up, lines are dropped and the file
  says how many.

## Performance

### Profiling a slow server

Set `CORAL_PPROF_ADDR` to a loopback address, e.g.
`CORAL_PPROF_ADDR=127.0.0.1:6060 coral`, and Go's profiler is served at
`http://127.0.0.1:6060/debug/pprof/` (`go tool pprof http://127.0.0.1:6060/debug/pprof/profile`).
It is off by default, runs on its own port (never the dashboard's), and
refuses anything but a loopback IP such as `127.0.0.1` or `[::1]`. Loopback
is not per-user: while it is on, any local account can read heap and
goroutine dumps, which can contain prompts and other session data. Turn it
off when you are done.

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
