# Quickstart

Get from a downloaded release to a committed, tested change.

**Every command on this page was run against the shipped v1.0.8 macOS build and the
output is real.** Timings are measured, not estimated. Where something is unverified,
it says so.

> **Measured result:** 90 seconds from starting the server to a committed, passing
> change — *after* your agent CLI is installed and authenticated, and *after* you answer
> the trust prompt in step 4. Installing and authenticating an agent CLI is the long pole
> on a clean machine, and Coral does not do it for you.

## Prerequisites

| Requirement | Notes |
|---|---|
| **tmux** | Required on macOS and Linux. Coral starts without it, but agent launch fails. Not needed on Windows, which defaults to a native PTY backend — though that backend is unexercised and Windows is unsupported; see [Platform support](#platform-support). |
| **git** | Worktree and branch features need a real repository. |
| **An agent CLI** | At least one of `claude`, `codex`, `gemini`, or `pi` — **installed and authenticated**. Coral drives these tools; it does not install, configure, or authenticate them. |

Coral supports exactly four agent CLIs:

| Agent | Binary | Install |
|---|---|---|
| Claude Code | `claude` | `npm install -g @anthropic-ai/claude-code` |
| Codex | `codex` | `npm install -g @openai/codex` |
| Gemini CLI | `gemini` | `pip install google-gemini-cli` |
| Pi.dev | `pi` | `npm install -g @mariozechner/pi-coding-agent` |

Adding a fifth requires editing `GetAgent` in `internal/agent/agent.go` and recompiling.

> **If you ever ran `pip install agent-coral`, remove it first.**
> The retired Python package installs binaries with the *same names* as this one
> (`coral`, `coral-board`, `launch-coral`, the hooks), uses the same `~/.coral`
> directory, the same `sessions.db` / `messageboard.db` filenames, and the same port
> 8420. On a default `PATH`, pip's `~/.local/bin` wins, so typing `coral` can silently
> start the old Python server and look like it worked.
>
> ```bash
> which -a coral coral-board launch-coral   # more than one hit means both are installed
> pip uninstall agent-coral
> ```

## Platform support

| Platform | Status |
|---|---|
| **macOS** | Signed and notarized universal binary (`Coral.v<version>.dmg`). Gatekeeper does not block it. |
| **Linux** | `coral-linux-amd64-<version>.tar.gz`, statically linked, **x86-64 only**. No arm64 build. CLI/server only — no tray or desktop app. |
| **Windows** | **Not supported.** No Windows artifact has ever been published, and the server does not currently compile for Windows. |

## 1. Install

Download from [GitHub Releases](https://github.com/cdknorow/coral/releases). Note the
filenames carry a version: `Coral.v1.0.8.dmg`, not `Coral.dmg`.

**macOS** — open the `.dmg` and drag `Coral.app` to Applications.

**Linux** — the tarball contains six bare binaries and no installer:

```console
$ tar xzf coral-linux-amd64-1.0.8.tar.gz
$ ls
coral  coral-board  coral-hook-agentic-state  coral-hook-message-check
coral-hook-task-sync  launch-coral
```

Put them somewhere on your `PATH` yourself.

### Getting the CLI tools on macOS

The DMG does **not** put `coral` or `coral-board` on your `PATH`. A script inside the
bundle does that:

```bash
/Applications/Coral.app/Contents/MacOS/install-cli.sh
```

> **Known issue:** this script symlinks into `/usr/local/bin`, which does not exist on a
> clean Apple Silicon Mac (`/usr/local` is root-owned and ARM Homebrew uses
> `/opt/homebrew`). It runs `mkdir -p /usr/local/bin` under `set -e`, so for a non-root
> user it aborts and installs nothing. Until it is fixed, either run it with `sudo`, or
> symlink the binaries yourself into a directory already on your `PATH`.

You can skip this entirely and run the binary by its full path, which is what the timings
below do.

## 2. Start the server

```console
$ coral --host 127.0.0.1 --port 8420 --no-browser
Coral dashboard: http://localhost:8420
Press Ctrl+C to stop
```

The dashboard answers immediately — sub-second in testing.

Useful flags: `--home <dir>` (data directory, default `~/.coral`), `--port`, `--host`,
`--backend pty|tmux`, `--no-browser`.

> There is no `--version` flag. `coral --version` fails with
> `flag provided but not defined: -version`. The version is printed in the startup log.

### Running a second instance

If you already have a Coral running, isolate the second one with `--home` **and** a
different port, and keep the path short:

```bash
coral --home /tmp/coral-t1 --port 8452 --no-browser
```

> **Isolation is incomplete — read this before testing against a real instance.**
> - A tmux socket path over ~104 characters cannot bind, so a deep `--home` path silently
>   degrades the tmux backend. Keep the directory short.
> - Session discovery merges the default tmux socket unconditionally
>   (`internal/tmux/client.go:212`), so a second instance **lists and can kill the first
>   instance's agents**.
> - `coral-board` reads its subscription state from `~/.coral` regardless of `--home`
>   (`cmd/coral-board/main.go:41-44`), so the message board does not work on a
>   non-default data directory, and agents may write into `~/.coral`.
>
> Verify what a new instance can see before using it:
> ```bash
> curl -s localhost:8452/api/sessions/live   # sessions you did not create = not isolated
> ```

## 3. Open the dashboard

Go to **http://localhost:8420**.

> **The first screen is a pricing page, not the dashboard.** Coral is free and fully
> unlocked; this is a reminder shown on the 1st, 4th, 7th … launch
> (`IsNagLaunch()` is `count % 3 == 1` and the count starts at 1). Click **Continue Free**
> to reach the dashboard. The skip is a URL parameter and sets no cookie, so it can
> reappear when you reload.

## 4. Launch your first agent

From the dashboard click **+New**, choose a working directory and an agent type. The
equivalent API call:

```console
$ curl -s -X POST http://127.0.0.1:8420/api/sessions/launch \
    -H "Content-Type: application/json" \
    -d '{"working_dir":"/path/to/repo","agent_type":"claude",
         "display_name":"first-agent","prompt":"...your task..."}'
{"backend":"pty","ok":true,
 "session_id":"3ea283fc-...","session_name":"claude-3ea283fc-..."}
```

Returned in **1 second**.

> The `backend` field is unreliable — it reports `pty` whenever a backend exists, even
> when the agent actually runs under tmux. Check `tmux_session` in
> `/api/sessions/live` instead.

### Your first agent will appear to hang — this is expected

On its first run in a directory, the agent CLI asks its own trust question:

```
Quick safety check: Is this a project you created or one you trust?
❯ No, exit
  Yes, I trust this folder
Enter to confirm · Esc to cancel
```

**Coral does not surface this as "waiting for input".** The agent looks alive and does
nothing. Open its terminal in the dashboard and answer it. Codex asks an equivalent
question (`Do you trust the contents of this directory?`).

Answering over the API:

```bash
curl -s -X POST "http://127.0.0.1:8420/api/sessions/live/$SESSION/keys" \
  -H "Content-Type: application/json" \
  -d '{"keys":["Down"],"agent_type":"claude"}'
curl -s -X POST "http://127.0.0.1:8420/api/sessions/live/$SESSION/keys" \
  -H "Content-Type: application/json" \
  -d '{"keys":["Enter"],"agent_type":"claude"}'
```

Trust is remembered per repository, so later agents in the same repo skip it.

## 5. Watch it finish

Given a small Go package and this task —

> Add a function `Capitalize(s string) string` to `stringutil.go`. Then create
> `stringutil_test.go` with table-driven tests for **both** `Reverse` and `Capitalize`.
> Run `go test ./...` and make sure it passes. Then `git add -A` and
> `git commit -m "add Capitalize + tests"`.

— the result, 90 seconds after the server started:

```console
$ git log --oneline
0392df2 add Capitalize + tests
47dc160 initial: Reverse

$ go test ./...
ok      example.com/stringutil
```

Eleven test cases including a unicode case. Verified by running the tests directly, not
by trusting the agent's summary.

## Where a first run actually goes wrong

| Symptom | Cause |
|---|---|
| Agent launched, no output, looks hung | The trust prompt in step 4. Open the terminal and answer it. |
| Landed on a $49.99 page | The activation reminder. Click **Continue Free**. |
| `coral: command not found` after a DMG install | `install-cli.sh` was never run, or it aborted on `/usr/local/bin`. |
| `coral` starts something unfamiliar | The retired `agent-coral` pip package is shadowing it. Run `which -a coral`. |
| Agents launch but never post to the board | `CORAL_PORT`/`CORAL_URL` are not passed to agents, and board state is read from `~/.coral`. Only works on a default install at port 8420. |
| Agent launch fails on macOS/Linux | tmux is missing. Coral starts anyway and only fails at launch time. |

## Next

- [Teams and multi-agent runs](teams.md) — and read
  [Worked demos](worked-demos.md) first for what team isolation does and does not do.
- [Message board](board.md)
- [Scheduled jobs](scheduled-jobs.md)
