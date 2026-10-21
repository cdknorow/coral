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

Open the agent's terminal in the dashboard and answer it, or over the API.

> **Operator only** (#3929): run it yourself, never from an agent (an agent's token gets 403
> `forbidden_for_agent`). Keys sent with the API key count as a script's input, and a script's
> input is held while a permission prompt is open. So to answer a prompt, use the dashboard (or a
> browser signed in with `coral open`); the API key only works once no prompt is open.

```bash
# a script you run yourself, never an agent
# NAME and SID are the agent's "name" and "session_id" from GET /api/sessions/live;
# PORT is your server's port (8420 by default).
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST "http://localhost:$PORT/api/sessions/live/$NAME/keys" \
  -H "Content-Type: application/json" -d "{\"keys\":[\"Down\"],\"agent_type\":\"claude\",\"session_id\":\"$SID\"}"
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST "http://localhost:$PORT/api/sessions/live/$NAME/keys" \
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
configured and Coral was started to offer it (a hub URL passed with `--cloud-url` or
`CORAL_CLOUD_URL`, or `--cloud-connect prompt` or `auto`; not for the release build's default
URL alone), and test servers set `CORAL_NO_WELCOME=1` and never show it.

Opened from another computer (a phone, or the LAN address), it says connecting has to
happen on the computer running Coral: run `coral cloud connect` there.

### Which hub Coral connects to, and how to turn it off

Release builds offer **https://app.coralhub.dev**; dev builds offer none. `--cloud-url
<origin>` or `CORAL_CLOUD_URL` choose another; `--cloud-url ""` or `CORAL_CLOUD_URL=off`
(also `none`, `0`, `false`) turn the connector off. Precedence: `--cloud-url`, then
`CORAL_CLOUD_URL`, then the build's default. A configured hub is only an address: Coral
makes no connection to it until you click **Connect** (or run `coral cloud connect`). The
startup log says which hub it is: `[CLOUD] Coral Hub: https://app.coralhub.dev (nothing is
sent until this server is connected: Settings > Coral Hub)`.

### "This server is enrolled with a different hub" (Coral Hub moved to app.coralhub.dev)

Coral Hub moved from app.coralai.ai to **app.coralhub.dev**. A server enrolled with the old
host stops connecting after the upgrade and says so in Settings > Coral Hub (Coral never
sends its token to a host it wasn't enrolled with, and never follows a redirect to a hub).
Connect it again, then remove the old entry:

1. On the computer running Coral: `coral cloud connect --force` (it forgets the old
   enrollment here and enrolls with app.coralhub.dev; approve the code in your browser).
2. In Coral Hub, revoke the server's old entry.

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
coral-board subscribers
```

(An agent whose environment was cleared can't run it; you can ask the server directly with a script you run yourself, never an agent: `curl -H "Authorization: Bearer $(coral api-key)" -s http://localhost:<port>/api/board/<project>/subscribers`.)

If the subscription exists server-side but the CLI disagrees, check that the agent's
environment was not cleared by a wrapper. As the Operator you can post for the agent through the REST API
(a script you run yourself, never an agent: an agent uses only its own session token):

```bash
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST http://localhost:<port>/api/board/<project>/messages \
  -H "Content-Type: application/json" \
  -d '{"subscriber_id":"<name>","content":"your message"}'
```

The field is `content`, not `message` — empty content is rejected.

### Agents on a dev or test server never show Working or Needs input

An agent's status comes from Coral's hook programs (`coral-hook-agentic-state` and the other
`coral-hook-*`) and `coral-board`, which agents find on the PATH Coral gives them. Coral looks
for them in `CORAL_HOOK_DIR` when it is set, then next to the `coral` binary. Only the installed
app (release build, `~/.coral`) falls back to `/Applications/Coral.app`; a dev build or a server on
another data folder never does, so a test server can't silently run an older app's hooks
against itself. The server's log says where the tools come from at startup:
`[tools] agents get coral-board and the Coral hooks from <dir> (...)`, or a WARNING that it found
none.

Start a test server with its own build's tools next to it:

```bash
cd coral-go && make dev            # builds coral and every tool into bin/
CORAL_DATA_DIR=/tmp/coral-test ./bin/coral --host 127.0.0.1 --port 8450 --cloud-url off
# or, by hand:
go build -tags "dev fts5" -o /tmp/coral-test-bin/ ./cmd/coral ./cmd/coral-board ./cmd/coral-agent \
  ./cmd/coral-hook-agentic-state ./cmd/coral-hook-session-start ./cmd/coral-hook-message-check ./cmd/coral-hook-task-sync
```

or point `CORAL_HOOK_DIR` at a folder that holds them. The CDP runner and the test rigs under
`tests/` build them next to `coral`.

The hooks log what they did to `<data dir>/hook_cache/debug.log`: event names and IDs only,
never a payload, a task's subject or a title. The folder is readable only by you (0700, its
files 0600). Builds before v1.4.40 kept this log and the task cache in the shared temp folder
(`$TMPDIR/coral_task_cache` or `/tmp/coral_task_cache`); that folder is no longer used, and you
can delete it.

The same folder holds the task cache, one `task_<hash>` file per task a hook saw (a subject
remembered for a later update). It keeps at most 5,000 of them: the write that adds one more
removes the oldest until 4,500 remain. They are Coral's own cache files, safe to delete; a missing
one is just a subject the hook no longer remembers.

### A whole team stalled and produced nothing

Agents told to coordinate over an unreachable board will troubleshoot it and then wait
indefinitely for an orchestrator that cannot reach them. They stay "running", consume
tokens, and produce no work, with no error anywhere in the UI. Check board reachability
first (above), then send a direct instruction.

> **Operator only** (#3929): run it yourself with the API key
> (`-H "Authorization: Bearer $(coral api-key)"`; a script you run yourself, never an agent), or use the dashboard; an agent's token gets 403
> `forbidden_for_agent`. Text sent with the key counts as a script's input: it is held while the
> agent shows a permission prompt, and typed once none is open.

```bash
# a script you run yourself, never an agent
# NAME and SID are the agent's "name" and "session_id" from GET /api/sessions/live.
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST "http://localhost:<port>/api/sessions/live/$NAME/send" \
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

### A repository's git hooks or filters don't run in Coral's worktrees

Coral runs git itself in folders that agents can change: the changed-files list, diffs, and
the worktrees it makes for teams and scheduled jobs. A repository's config could name
commands for that git to run, and an agent can edit that config. So every git Coral runs
has them turned off: hooks (`post-checkout` included), `core.fsmonitor`, external diff
programs and textconv, and the clean, smudge and process commands of filter drivers (a
`required` driver is then not required, so the command still works).

- Git LFS's standard driver, exactly as `git lfs install` writes it, still runs, so LFS files
  are checked out in Coral's worktrees.
- Any other filter isn't run. For example, a worktree Coral makes of a `git-crypt` repository
  holds its files as they're stored (encrypted). Run `git checkout -- .` in the worktree
  yourself if you need them filtered.
- Coral doesn't look into submodules: a submodule has its own config, so changes inside one
  don't appear in the changed-files list or the diff.
- Agents' own git commands are unchanged: they run in the agent's terminal, under the agent
  CLI's permission prompts.

### A team's worktree is still there after Coral restarted

When Coral starts and finds a team that was running but has no live agents (its server stopped mid-work), it marks the team stopped and removes the team's git worktree only if git can remove it without losing anything: a clean worktree. A worktree with uncommitted or untracked changes, or one git can't place (git missing, a timeout, a broken `.git` file), is left where it is, and `coral.log` says why (`left the orphaned worktree … in place`). To remove it by hand, check it first: `git -C <path> status` shows what would be lost (commit, stash or copy what you need). Then run `git worktree remove <path>`, or `git worktree remove --force <path>` to discard the changes; for a worktree git can't place (a broken `.git` file), delete the folder and run `git worktree prune` in the repository.

### A scheduled job ends in `killed` / `timeout` instead of `completed`

Expected with an interactive agent. `scheduler.go:611-620` records `completed` only when the
launch call returns; an interactive agent session does not exit after answering, so the run
runs until `max_duration_s` and is recorded `killed` with `exit_reason: timeout`. The agent's
work still happened. Whether any agent configuration exits cleanly enough to record
`completed` is untested.

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
# a script you run yourself, never an agent
curl -H "Authorization: Bearer $(coral api-key)" -s localhost:<port>/api/sessions/live
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
test server its own data directory and port, and turn Coral Hub off so it can never start a
real enrollment with app.coralhub.dev (a release build connects to it by default):

```bash
CORAL_DATA_DIR=$(mktemp -d) ./coral --host 127.0.0.1 --port PORT --cloud-url off
```

Agents don't inherit Coral's own server settings (#3389): every `CORAL_` variable except the
ones agent tools read (`CORAL_URL`/`HOST`/`PORT`, `CORAL_DATA_DIR`/`DIR`, the agent's
`CORAL_SESSION_NAME`/`ID` and `CORAL_SUBSCRIBER_ID`, `CORAL_SHELL`, `CORAL_TELEMETRY_DISABLED`,
`CORAL_DEBUG` and a workflow step's `CORAL_WORKFLOW_*`/`CORAL_STEP_*`/`CORAL_PREV*`) is removed
from their environment, so `CORAL_SECRET_KEY`, `CORAL_CLOUD_URL`, the TLS paths and the like
never reach an agent or a server it starts. The list is `internal/agentenv`.

The frontend CDP suites read their server from `CORAL_TEST_URL` (`tests/frontend/test_base.js`),
never `CORAL_URL`: shells a running Coral spawns export `CORAL_URL` pointing at the live server.
A URL on port 8420, or with no explicit port, is refused. Prefer the runners, which build a dev
binary, start an isolated server with a temp data directory and set `CORAL_TEST_URL` for you:
`tests/frontend/run.sh` (port from `CORAL_TEST_PORT`, default 8462) or
`node tests/frontend/run_cdp_suites.mjs [--only name] [--shards N]` (N isolated server+Chrome
pairs).

### The disk fills up during test runs

A build or test that fails with `no space left on device`, or a link step that suddenly can't
open its inputs, is a full disk, not a flaky test: check `df -h` before rerunning.

The usual cause is Go's build cache (`go env GOCACHE`, `~/Library/Caches/go-build` on macOS).
Every combination of build tags and `-race` adds its own copies of every package and test
binary, and on a machine running many test builds a day it can reach hundreds of GB before Go's
own trimming (entries unused for 5 days) catches up. Check it with `du -sh "$(go env GOCACHE)"`
and clear it about once a week, or whenever it passes ~50 GB:

```bash
go clean -cache   # safe: the next builds are slower while it refills
```

Clear it while no build is running: a build that loses its cache files halfway fails with
`could not import …` or `package … is not in std`. Rerun it after the clear finishes.

`run_cdp_suites.mjs` builds into a temp folder (`$TMPDIR/coral-cdp-*`, about 250 MB with the
Chrome profiles). It deletes the folder on exit when every suite passed; after a failure, a
flaky suite or an interrupt it keeps only `results.json` and the `coral.log` files, and
`--keep` keeps everything. Delete worktrees, built binaries and kept results folders yourself
when you're done with them.

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
- Automated text Coral held because the agent was waiting on a prompt (a permission or
  selection menu) is logged as `[HELD, <why>]`; it is typed once the prompt is answered. The
  opt-in auto-accept's answer to an approval is logged as `[auto-accept answers an approval prompt]`.
- A person's own typing (the dashboard terminal, the chat and send boxes, the text typed into a prompt's answer field, keys sent from the dashboard) is logged by its length only
  (`[typed 12 bytes]`), the same way on both terminal backends; Enter, the arrow keys and Ctrl-<key>
  are logged as they are. Coral's own text is logged as it was typed, after secrets are masked.
- On Windows, ending an agent types Ctrl-C into its console, a second Ctrl-C a moment later, and,
  once the agent has gone and its shell is idle, `exit`. Each is logged as raw input: the second
  Ctrl-C and the `exit` line are Coral's own control keys.

### An agent didn't get a board message or task notification

Coral never types automated text (board messages and nudges, task notifications, wait results,
a launch's first prompt) into an agent that is waiting on a prompt for you: a permission or
selection menu, an approval question, the trust-this-folder screen, a login or model picker,
Claude Code's bug-report draft or its session survey (those take single digit keys). The agent
shows "delivery held: a dialog is open". Typed there, the text and its Enter would answer the prompt. It holds the text and types it,
once, after you answer the prompt in the agent's terminal or the chat. If a message has been
held for two minutes, the agent shows as needing your input on the dashboard. A newer unread
count replaces a held one. A held nudge or notification is dropped after an hour; a new agent's
first prompt waits as long as the agent runs. Killing, restarting or resuming an agent drops
what was held for it, so nothing meant for the old process reaches the new one. If a prompt opens
in the moment between a message's text and its Enter, the Enter is held too and pressed after
you answer. `coral.log` logs each hold and delivery ("automated input held",
"automated input delivered after the hold ended", with what ended it: the prompt closed,
the draft cleared, the turn ended, ...).

## Performance

### The dashboard is slow while agents are busy

Agents run at a lower OS priority than Coral itself, so a machine whose agents use every CPU
still answers the dashboard and Coral Hub quickly. Each agent's terminal (a tmux pane, or a PTY
session's process) and everything started in it run at nice +10 on macOS and Linux, and at below
normal priority on Windows, as do workflow steps and hooks. The Coral server and the tmux server
keep normal priority.

It's **Settings > Agent priority**: **Low** (the default) or **Normal**. `--agent-priority low|normal`
or `CORAL_AGENT_PRIORITY` override the setting. A change applies to agents started afterwards:
an agent already running keeps its priority until it is restarted, because a process can lower
its priority but not raise it back. At startup Coral lowers the panes of its own tmux server that
an older Coral started at normal priority; it never touches your own tmux sessions on the default server.

Measured on a Linux machine with 16 CPU-bound agents sharing 2 cores with the server (three runs
of each): the agent list (`GET /api/sessions/live`) answered in 28-41 ms (p50) and 57-80 ms (p95)
with Normal, and 10-11 ms and 18-19 ms with Low; the board's chat history's p95 went from 10-12 ms
to 4 ms.

Check an agent with `ps -o ni= -p <pid>` (10 for Low, the same as Coral's for Normal).

### Profiling a slow server

Set `CORAL_PPROF_ADDR` to a loopback address, e.g.
`CORAL_PPROF_ADDR=127.0.0.1:6060 coral`, and Go's profiler is served at
`http://127.0.0.1:6060/debug/pprof/` (`go tool pprof http://127.0.0.1:6060/debug/pprof/profile`).
It is off by default, runs on its own port (never the dashboard's), and
refuses anything but a loopback IP such as `127.0.0.1` or `[::1]`. **It asks
for no password, key or token, so it bypasses strict local auth**, and
loopback is not per-user: while it is on, any local account can read heap
and goroutine dumps, which hold prompts and other session data and also the
API key and browser session cookies as they sit in memory. Use it only on a
computer you alone use, and turn it off when you are done.

### Coral froze: stall dumps

Coral checks itself every 10 seconds: its HTTP router (an in-process `GET /api/health`),
its logger, and both databases (`SELECT 1`). If one of them hasn't answered for 30
seconds, Coral writes the stacks of every goroutine to `<data folder>/stall-<time>.txt`
(`~/.coral/stall-20261014T120000.000Z.txt`), starting with which check is stuck and for
how long. It writes the file directly, never through the logger (which may be the thing
that is stuck). It writes one file per stall, at most one every 10 minutes, and keeps
the newest 5. Attach the file to a bug report. The file is readable only by you (0600);
stacks can include fragments of session data.

`CORAL_STALL_WATCHDOG=off` turns the checks off.

### `sessions.db` is large

Coral records one row per API call in `call_metrics` for the **Tool & API Calls**
view. After a day those rows are rolled into one row per hour and kind of call
(`call_metrics_hourly`: calls, errors, total duration, p50 and p95), about 800 rows
into one, so the view keeps covering its whole window. Both are kept for
`call_metrics_retention_days` (default `14`, `1`-`90`; set it with `PUT /api/settings`),
by a background job in short batches: about 2 minutes after start, then hourly,
at most 500,000 deleted rows and 48 rolled-up hours a run. The dashboard's
own refresh polls (an open agent's chat, poll, prompt options and events, and a
board's message list, when no agent identity is sent) are not recorded. The view
never shows more than the retention window.

Deleted rows are reused, so the file stops growing but doesn't shrink by itself.
To shrink it once, stop Coral and run:

```
coral db compact            # or: coral db compact --home <data folder>
```

It refuses while a Coral server runs on that data folder (and Coral can't start
while it works), writes a backup `sessions.before-compact-<time>.db` next to the
database first, then compacts it and prints the sizes. It needs free disk about
twice the database's size. An encrypted database isn't supported yet. Delete the
backup once Coral works as before. Measured on a 2 GB database: 15 s, 2.0 GB to
1.24 GB.

`token_usage` (the cost ledger) is not trimmed: it holds every session's spend.

### "Coral found damage in sessions.db (fts5: corruption found ...)"

`session_fts` is the search index over the text of past sessions: FTS5 keeps the text in its own content table and builds the
index from it, so the index is derived data and a damaged one only affects search. A minute after start Coral checks the
databases; a problem is checked again with writes paused before it is shown (the indexer rewrites the search index at start, and
a first check could misread it: a notice that came and went between starts, each time naming a different blob, was that).
A notice that remains is real. `coral db compact` does not fix it (VACUUM copies the damaged index as it is). Stop Coral and run:

```
coral db rebuild-search     # or: coral db rebuild-search --home <data folder>
```

It refuses while a Coral server runs on that data folder, writes a backup `sessions.before-rebuild-search-<time>.db` next to
the database, rebuilds the index from the stored text (about 3 s for 330 sessions on a 2 GB database), checks the result and
says what it found. Nothing but the search index changes. If the search text itself is damaged, the command says so and gives
the one command that drops the search table; Coral makes it again at the next start and fills it again from the transcripts
that still exist.

## Platform

### Windows

Each release publishes `Coral-x64.msi` and `Coral-x64-portable.zip` (and the same files with the version in the name). They are
not code-signed, so SmartScreen warns that the publisher is unknown; verify the download (README,
"Verify your download") before running it. On Windows Coral uses the native PTY backend
(ConPTY; there is no tmux on Windows) and listens on port 8450 by default, not 8420, which svchost can hold.

### Windows: Antigravity (agy) agents show no Working or Needs-input status

agy runs Coral's hooks through `cmd.exe`, which can't take a quoted path, so Coral names the hook
by an unquoted absolute path. When Coral is installed in a folder with a space in it (the
installer's default, `C:\Program Files\Coral`), that path is the folder's 8.3 short name
(`C:\PROGRA~1\Coral\...`). On a volume with 8.3 names turned off there is no such name, and agy
then runs without Coral's hooks rather than with one a repository could replace: its status
isn't tracked, and Coral's log says so once per launch (`agy runs without Coral's hooks`).
Claude and Codex agents are not affected. To check a volume, run `fsutil 8dot3name query C:`;
installing Coral in a folder without spaces also avoids it.

### Windows: an unknown account has Modify on `%TEMP%` or a project folder

If you run Codex agents with a sandbox (workspace-write, with `[windows] sandbox = "unelevated"` in
`~/.codex/config.toml`), Codex itself creates a capability SID for the sandbox and grants it
**Modify**, inherited by new files, on every folder it lets the agent write: the project folder,
and `%TEMP%` when that is a writable root. This is Codex's behaviour, not Coral's. `icacls` shows it
as an unnamed SID (`S-1-5-21-...`); Codex lists the SIDs it made in `~/.codex/cap_sid`
(`writable_root_by_path`).

Coral's own secrets are not affected: the API key, the command-line token, session tokens and the
hook files are created with a protected access list of their own (only you and SYSTEM), so they
don't inherit the entry, and Coral refuses to read a secret file that others can read. Other files
created in those folders do inherit it.

To remove the entry (Codex adds it again the next time a sandboxed agent writes there), in
PowerShell, with the SID from `icacls` or `~/.codex/cap_sid`:

```powershell
$sid = [Security.Principal.SecurityIdentifier]'S-1-5-21-...'
$d = Get-Item $env:TEMP            # or the project folder
$a = $d.GetAccessControl('Access') # the access list only: no extra privilege needed
$a.PurgeAccessRules($sid)
$d.SetAccessControl($a)
```

`icacls <folder> /remove *S-1-5-21-...` does not work here, in cmd.exe or PowerShell: the
capability SID maps to no account, so icacls reports "No mapping between account names and
security IDs was done" and "Successfully processed 0 files" and leaves the entry. `Set-Acl` fails
too unless run with SeSecurityPrivilege, because it writes the audit list as well.

### Linux

There are two tarballs, x86-64 (`amd64`) and ARM64 (`arm64`). Each contains bare binaries with no
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
