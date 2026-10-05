# Quickstart

Get from a downloaded release to a committed, tested change.

This page describes Coral v1.3.23. Behavior statements were checked against the current
source. The example output and timings in steps 4-5 were captured on an earlier (v1.0.8)
build, so treat them as illustrative. Where something is unverified, it says so.

> **Example result (v1.0.8 measurement):** about 90 seconds from starting the server to a
> committed, passing change — *after* your agent CLI is installed and authenticated, and
> *after* you answer the trust prompt in step 4. Installing and authenticating an agent
> CLI is the long pole on a clean machine, and Coral does not do it for you.

## Prerequisites

| Requirement | Notes |
|---|---|
| **tmux** | The macOS app (v1.3.23) bundles tmux 3.7c; older downloads and standalone/source builds may still need it installed separately. Linux requires a separate tmux installation for the default backend. The PTY backend (`--backend pty`) does not require tmux. See [Platform support](#platform-support). |
| **git** | Worktree and branch features need a real repository. |
| **An agent CLI** | At least one of `claude`, `codex`, `agy` (Antigravity), or `pi` — **installed and authenticated**. Coral drives these tools; it does not install, configure, or authenticate them. |

Coral supports exactly four agent CLIs:

| Agent | Binary | Install and sign in |
|---|---|---|
| Claude Code | `claude` | [Claude Code documentation](https://code.claude.com/docs/en/overview) |
| Codex | `codex` | [openai/codex](https://github.com/openai/codex) |
| Antigravity CLI | `agy` | [antigravity.google](https://antigravity.google) |
| Pi.dev | `pi` | [pi.dev](https://pi.dev) |

Follow each vendor's current instructions; install commands change and Coral does not
maintain them. A custom executable can be set per agent type with the `cli_path_<type>`
setting. The launch API rejects an unsupported `agent_type` (`ValidateAgentType` in
`internal/agent/agent.go`); adding a new agent requires a source change and a rebuild.

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
| **macOS** | Signed and notarized universal app (`Coral.v<version>.dmg`, Apple Silicon and Intel, macOS 13 minimum) with bundled tmux. The macOS 13 minimum is checked in the binaries but has not been run on a macOS 13 host. |
| **Linux** | `coral-linux-amd64-<version>.tar.gz`, statically linked, x86-64 only. No arm64 build. CLI/server only — no tray or desktop app. tmux is not bundled. |
| **Windows** | No official asset is published for the regular bare-tag release. |
| **Homebrew** | Not recommended: the public tap previously shipped a blank checksum and its repair has not been independently verified. Use the GitHub release. |

## 1. Install

Download from [GitHub Releases](https://github.com/cdknorow/coral/releases). Note the
filenames carry a version: `Coral.v<version>.dmg`, not `Coral.dmg`.

**macOS** — open the `.dmg` and drag `Coral.app` to Applications.

**Linux** — the tarball contains bare binaries (`coral`, `coral-board`, the hook helpers
and `launch-coral`) and no installer:

```console
$ tar xzf coral-linux-amd64-<version>.tar.gz
```

Put them somewhere on your `PATH` yourself.

### Getting the CLI tools on macOS

The DMG does **not** put `coral` or `coral-board` on your `PATH`. A script inside the
bundle does that:

```bash
/Applications/Coral.app/Contents/MacOS/install-cli.sh
```

It symlinks the tools into `~/.local/bin` by default (`--dir <path>` or `CORAL_LINK_DIR`
choose another directory, `--force` replaces non-Coral files). Make sure that directory
is on your `PATH`. Earlier versions linked into `/usr/local/bin` and failed on a clean
Apple Silicon Mac.

You can skip this entirely and run the binary by its full path.

## 2. Start the server

```console
$ coral --host 127.0.0.1 --port 8420 --no-browser
Coral dashboard: http://localhost:8420
Press Ctrl+C to stop
```

Useful flags: `--home <dir>` (data directory, default `~/.coral`), `--port` (default
8420), `--host`, `--backend pty|tmux`, `--no-browser`. Coral listens on `127.0.0.1` unless
[remote access](#remote-access) is enabled and restarted.

> There is no `--version` flag. `coral --version` fails with
> `flag provided but not defined: -version`. The version is printed in the startup log.

### Running a second instance

If you already have a Coral running, isolate the second one with `--home` **and** a
different port, and keep the path short:

```bash
coral --home /tmp/coral-t1 --port 8452 --no-browser
```

> **What isolation covers.** A non-default `--home` gets its own tmux socket
> (`<home>/tmux.sock`), and session discovery no longer merges tmux's default socket
> for it (`defaultSocketFallback` in `internal/tmux/client.go`), so a second instance
> does not list the first instance's agents. `CORAL_TMUX_FALLBACK=1` re-enables the
> merge. Agents receive `CORAL_URL`, `CORAL_PORT`, `CORAL_HOST` and `CORAL_DATA_DIR`
> (`internal/agent/agent.go`), and `coral-board` keeps its state under `CORAL_DATA_DIR`,
> so the board follows a non-default data directory and port.
>
> A tmux socket path over about 104 characters (macOS) cannot bind, so keep the
> directory short. If you are unsure what a new instance can see, check:
> ```bash
> curl -s localhost:8452/api/sessions/live   # sessions you did not create = not isolated
> ```

## Remote access

Remote (LAN/mobile) access is **off by default**. Enabling it saves a preference and
takes effect only after Coral restarts; the dashboard reports the saved and effective
state separately. Details: `PRIVACY_CONTROLS.md` and `/api/system/privacy`.

## 3. Open the dashboard

Go to **http://localhost:8420**.

> **Occasionally the first screen is a supporter page, not the dashboard.** Coral is free
> and fully unlocked. The reminder is scheduled every 25 launches, counted from the launch
> at which Coral first did something useful (`nagInterval` in
> `internal/license/launch_counter.go`), never on that first launch. Click
> **Continue Free** to reach the dashboard. The skip is a URL parameter and sets no
> cookie. The reminder is shown to the first qualifying page load of a server start only,
> so reloading does not bring it back.

## 4. Launch your first agent

From the dashboard click **+New**, choose a working directory and an agent type. The
equivalent API call:

```console
$ curl -s -X POST http://127.0.0.1:8420/api/sessions/launch \
    -H "Content-Type: application/json" \
    -d '{"working_dir":"/path/to/repo","agent_type":"claude",
         "display_name":"first-agent","prompt":"...your task..."}'
{"backend":"pty","ok":true,"terminal":"tmux",
 "session_id":"3ea283fc-...","session_name":"claude-3ea283fc-..."}
```

> `terminal` reports what the session actually runs on (`tmux` or `pty`). `backend`
> names the launch path and reads `pty` even for tmux sessions, so use `terminal` (or
> `tmux_session` in `/api/sessions/live`). Other fields are omitted above.

### Your first agent will appear to hang — this is expected

On its first run in a directory, the agent CLI asks its own trust question:

```
Quick safety check: Is this a project you created or one you trust?
❯ No, exit
  Yes, I trust this folder
Enter to confirm · Esc to cancel
```

Coral may not show this as "waiting for input" (not verified for every provider). The agent
can look alive and idle. Open its terminal in the dashboard and answer it. Codex asks an equivalent
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

— the result in the v1.0.8 test run, 90 seconds after the server started:

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
| Landed on a pricing/supporter page | The periodic reminder. Click **Continue Free**. |
| `coral: command not found` after a DMG install | `install-cli.sh` was never run, or its link directory (`~/.local/bin` by default) is not on your `PATH`. |
| `coral` starts something unfamiliar | The retired `agent-coral` pip package is shadowing it. Run `which -a coral`. |
| Agents launch but never post to the board | Confirm the server's view with `/api/board/<project>/subscribers`. Agents are given `CORAL_URL` and `CORAL_DATA_DIR`; a custom wrapper that clears the environment can lose them. |
| Agent launch fails with a tmux error | tmux is missing (Linux, source builds). The macOS app bundles it. Coral starts anyway and only fails at launch time; `--backend pty` avoids tmux. |

## Next

- [Teams and multi-agent runs](teams.md) — and read
  [Worked demos](worked-demos.md) first for what team isolation does and does not do.
- [Message board](board.md)
- [Scheduled jobs](scheduled-jobs.md)
