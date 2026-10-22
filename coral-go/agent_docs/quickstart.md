# Quickstart

Get from a downloaded release to a committed, tested change.

This page targets Coral v1.4.156. The example output and timings in steps 4-5 were
captured on an earlier (v1.0.8) build, so treat them as illustrative. Where something is unverified, it says so.

> **Example result (v1.0.8 measurement):** about 90 seconds from starting the server to a
> committed, passing change — *after* your agent CLI is installed and authenticated, and
> *after* you answer the trust prompt in step 4. Installing and authenticating an agent
> CLI is the long pole on a clean machine, and Coral does not do it for you.

## Prerequisites

| Requirement | Notes |
|---|---|
| **tmux** | The macOS app bundles tmux 3.7c; older downloads and standalone/source builds may still need it installed separately. Linux requires a separate tmux installation for the default backend. The PTY backend (`--backend pty`) does not require tmux. See [Platform support](#platform-support). |
| **git** | Worktree and branch features need a real repository. |
| **An agent CLI** | At least one of `claude`, `codex`, `agy` (Antigravity), or `pi` — **installed and authenticated**. Coral drives these tools; it does not install, configure, or authenticate them. |

Coral supports exactly four agent CLIs:

| Agent | Binary | Install and sign in |
|---|---|---|
| Claude Code | `claude` | [Claude Code documentation](https://code.claude.com/docs/en/overview) |
| Codex | `codex` | [openai/codex](https://github.com/openai/codex) |
| Antigravity CLI | `agy` | [antigravity.google](https://antigravity.google), or `curl -fsSL https://antigravity.google/cli/install.sh \| bash` |
| Pi.dev | `pi` | [pi.dev](https://pi.dev) |

Follow each vendor's current instructions; install commands change and Coral does not
maintain them. Where a CLI is missing, Coral's prerequisite check and the Launch dialog link to
these official pages (and show the one-line command where the vendor has one).

The Antigravity installer puts `agy` in `~/.local/bin`. Coral looks there itself, so it finds `agy`
even when that folder is not on your `PATH`. To run `agy` in your own terminal, add
`export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` or `~/.bashrc` and open a new terminal. A custom executable can be set per agent type with the `cli_path_<type>`
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
> which -a coral coral-board launch-coral   # inspect multiple hits for an old Python install
> pip uninstall agent-coral
> ```

## Platform support

| Platform | Status |
|---|---|
| **macOS** | Signed and notarized universal app ([`Coral.dmg`](https://github.com/subgentic/coral/releases/latest/download/Coral.dmg), Apple Silicon and Intel, macOS 13 minimum) with bundled tmux. The macOS 13 minimum is checked in the binaries but has not been run on a macOS 13 host. |
| **Windows** | [`Coral-x64.msi`](https://github.com/subgentic/coral/releases/latest/download/Coral-x64.msi) (installer) or [`Coral-x64-portable.zip`](https://github.com/subgentic/coral/releases/latest/download/Coral-x64-portable.zip), x64. Not code-signed (SmartScreen warns of an unknown publisher; verify the download as the README shows). Agents run with the native pty backend (no tmux); the default port is 8450. |
| **Linux** | [`coral-linux-amd64.tar.gz`](https://github.com/subgentic/coral/releases/latest/download/coral-linux-amd64.tar.gz) (x86-64) or [`coral-linux-arm64.tar.gz`](https://github.com/subgentic/coral/releases/latest/download/coral-linux-arm64.tar.gz) (ARM64), statically linked. CLI/server only — no tray or desktop app. tmux is not bundled. |
| **Homebrew** | `brew install --cask cdknorow/coral/coral` installs the release's macOS app; the release workflow updates the tap with each release's checksum. |

## 1. Install

Download from [GitHub Releases](https://github.com/subgentic/coral/releases/latest). The stable
names always point to the newest release: `Coral.dmg`, `Coral-x64.msi`, `Coral-x64-portable.zip`,
`coral-linux-amd64.tar.gz` and `coral-linux-arm64.tar.gz`. Each release also keeps versioned names
(`Coral.v<version>.dmg`, `coral-linux-<arch>-<version>.tar.gz`, `Coral-<version>-x64.msi`,
`Coral-<version>-x64-portable.zip`): the same files.

**macOS** — open the `.dmg` and drag `Coral.app` to Applications.

**Linux** — the tarball contains bare binaries (`coral`, `coral-board`, the hook helpers
and `launch-coral`) and no installer:

```console
$ uname -m        # x86_64 → the amd64 tarball, aarch64 → the arm64 tarball
$ tar xzf coral-linux-amd64.tar.gz
```

Put the extracted tools in a directory on your `PATH`, or run `./coral` from the extracted folder. See the [public installation walkthrough](https://subgentic.github.io/coral/getting-started/) for complete installation commands.

### Getting the CLI tools on macOS

The DMG does **not** put `coral` or `coral-board` on your `PATH`. A script inside the
bundle does that:

```bash
/Applications/Coral.app/Contents/MacOS/install-cli.sh
```

It symlinks the tools into `~/.local/bin` by default (`--dir <path>` or `CORAL_LINK_DIR`
choose another directory, `--force` replaces non-Coral files). Add
`export PATH="$HOME/.local/bin:$PATH"` to your shell's startup file and run it in the
current terminal too. Earlier versions linked into `/usr/local/bin` and failed on a clean
Apple Silicon Mac.

You can skip this entirely and run the binary by its full path.

## 2. Start the server

```console
$ coral --host 127.0.0.1 --port 8420 --no-browser
Coral dashboard: http://localhost:8420
Press Ctrl+C to stop
```

On first launch, review the Terms of Service and accept them if you agree. Keep the
terminal open while the server runs. Without tmux, add `--backend pty`.

Useful flags: `--home <dir>` (data directory, default `~/.coral`), `--port` (default
8420), `--host` (one address or a comma list, see [below](#listening-on-a-tailnet-address)),
`--backend pty|tmux`, `--no-browser`. Coral listens on `127.0.0.1` unless
[remote access](#remote-access) is enabled and restarted.

`coral --version`, `coral -v`, and `coral version` print the version and exit without
starting a server. A running server's version is in its startup log and `/api/system/status`.

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
> # a script you run yourself, never an agent
> curl -s -H "Authorization: Bearer $(coral api-key --home /tmp/coral-t1)" localhost:8452/api/sessions/live   # sessions you did not create = not isolated
> ```

## Remote access

Remote (LAN/mobile) access is **off by default**. Enabling it in Settings saves a preference and
takes effect only after Coral restarts. `--remote` enables it for one run without saving it.
The dashboard reports the saved and effective state separately. Details: `PRIVACY_CONTROLS.md` and `/api/system/privacy`.

### Listening on a tailnet address

To reach Coral from your other devices on a tailnet (Tailscale) without a proxy, listen on
loopback **and** the tailnet address, with remote access on, and sign in with your Coral
password or API key:

```console
$ coral --remote --host 127.0.0.1,100.101.102.103
```

`--host` (or `CORAL_HOST`) takes a comma list; Coral opens one listener per address, all on
one port. Requests to the tailnet address need the API key or a dashboard sign-in, like any
remote caller. If the list names only network addresses (`--host 100.101.102.103`), Coral
also listens on `127.0.0.1` and says so in the startup log, so this computer's agents, the
command line and the dashboard keep using loopback; `--no-auto-loopback` (or
`CORAL_NO_AUTO_LOOPBACK=1`) turns that off. Agents always get the loopback address in
`CORAL_URL` when Coral listens on one. With remote access off, Coral listens on
`127.0.0.1` only, whatever `--host` says (a list of only loopback addresses is kept).

Whichever address a request arrives on, what it may do depends on where it comes from, never
on the listener: a program on this computer reaching the loopback listener is local, and anyone
reaching the tailnet address needs a dashboard sign-in or API key. A local request must also name this server in its
`Host` header (`localhost`, a loopback address, or an address in `--host`); any other name or
IP is refused as a DNS-rebinding attempt, unless Coral listens on every interface.

A hostname in the list is listened on at the first address it resolves to: `localhost`
counts as loopback, any other name (`myhost.local`) as a network address. Every interface
(`0.0.0.0`, `::`) can't be combined with other addresses, since it already includes them:
Coral stops at startup and says so.

> **On a computer other people log in to:** a loopback listener lets any local user reach
> Coral, but [strict local auth](auth.md#strict-local-auth-the-default) (the default) makes every request from
> this computer need an agent's session token, the dashboard sign-in, the command line's
> signature or the API key, which your agents and the `coral` command already send. Keep it
> on (don't start Coral with `--strict-local-auth=false`) and keep the loopback listener.
> `--no-auto-loopback` is not the stricter setup where Coral runs agents: they would reach it
> through the network address, where only the API key or a dashboard sign-in is accepted, and
> their board and task calls would be refused. Use it only on a computer where Coral runs no
> agents (Coral logs a warning when it starts that way).

### Remote access securely

Remote access on its own is **plain HTTP**: the API key, the login cookie and everything
typed into agent terminals cross the network unencrypted, readable by anyone on it. The
first-launch question, the startup log (`[startup] WARNING: remote access is on over plain
HTTP …`) and **Settings › Privacy › Remote access** all say so. Pick one of:

- **Coral Hub** (recommended away from home): the server connects out to Coral Hub over
  TLS; nothing listens on the network. See [hub.md](hub.md#connecting-a-server-to-coral-hub).
- **A TLS reverse proxy** on the same computer (Caddy, nginx, Tailscale Serve…) in front of
  `http://127.0.0.1:8420`, with remote access left **off** so Coral itself listens on
  loopback only. Forward WebSocket upgrades (`/ws/…`), and send
  `X-Forwarded-Proto: https`.

  Keep [strict local auth](auth.md#strict-local-auth-the-default) on (the default), so
  requests need credentials even on loopback. Have the proxy preserve forwarding
  headers: a forwarded request is a remote caller and needs a dashboard sign-in or API
  key. A proxy that strips those headers looks direct. **`require_auth_on_loopback`**
  (Settings; Operator-only) requires credentials for loopback requests, even if strict
  local auth is disabled; see [authentication](auth.md). Do not disable authentication
  to make a proxy work.

- **Native TLS**: `coral --tls-cert cert.pem --tls-key key.pem` (or `CORAL_TLS_CERT` and
  `CORAL_TLS_KEY`), PEM files, with remote access on. The same port then serves HTTPS, and
  WSS for the terminal and live feeds (the dashboard picks `wss:` on an `https:` page). From
  other computers only HTTPS is accepted: a plain `GET` is redirected to `https://`, any other
  plain request gets `400`. Plain `http://localhost` keeps working on this computer, so
  `coral-board`, the hooks and agents need no change. Giving only one of the two files, or a
  pair that does not load, stops Coral at startup. The mobile QR code uses `https://`. A
  self-signed certificate works, but each device must trust it; `mkcert` makes a local CA.
  For example:

  ```console
  $ coral --remote --tls-cert ~/certs/coral.pem --tls-key ~/certs/coral-key.pem
  Coral dashboard: https://localhost:8420 (TLS; plain http:// only from this computer)
  ```

## 3. Open the dashboard

Open the address printed at startup (normally **http://localhost:8420**). If the
browser shows **Create a password**, enter and confirm a password, then choose
**Create password and open Coral**. Later, sign in with that password. The desktop
app can open a signed-in browser directly; `coral open` does the same from your own
terminal on the server. For a second instance, pass its `--home` and `--url` to
`coral open` too.

Coral Hub (app.coralhub.dev, invite-only beta) is optional. A fresh install stays local
until you choose to connect from Home or **Settings > Coral Hub**, or run
`coral cloud connect`. In the connect flow, approve the link/code and choose the
hub's access (View, Interact or Launch). See [connecting to Coral Hub](hub.md#connecting-a-server-to-coral-hub).

The first-launch offer appears only when you explicitly give a Hub URL
(`--cloud-url https://app.coralhub.dev` or `CORAL_CLOUD_URL`) or request it with
`--cloud-connect prompt` (`CORAL_CLOUD_CONNECT=prompt`). `--cloud-connect auto`
also offers the dashboard screen, but only logs the terminal link/code instead of
asking a terminal question. The release build's default
URL alone does not trigger the offer. `--cloud-connect off` suppresses it;
`--cloud-url off` disables Hub connections. Nothing is sent to the Hub until you connect.

## 4. Launch your first agent

On Home choose **Launch agent** (elsewhere, **+ New > AI Agent**). Enter an existing
project folder on the computer running Coral. Open **More options > Agent** to choose
your installed CLI, then choose **Launch**. In **Chat**, type a task and press Enter
to send it; check **Terminal** first for any sign-in or trust prompt. The equivalent API call (with [strict local auth](auth.md#strict-local-auth-the-default), the default, add
`-H "Authorization: Bearer $(coral api-key)"` to every `curl` in this guide; the key is for a script you run yourself, never an agent):

```console
# a script you run yourself, never an agent
$ curl -H "Authorization: Bearer $(coral api-key)" -s -X POST http://127.0.0.1:8420/api/sessions/launch \
    -H "Content-Type: application/json" \
    -d '{"working_dir":"/path/to/repo","agent_type":"claude",
         "display_name":"first-agent","prompt":"...your task..."}'
{"backend":"pty","ok":true,"terminal":"tmux",
 "session_id":"3ea283fc-...","session_name":"claude-3ea283fc-..."}
```

> `terminal` reports what the session actually runs on (`tmux` or `pty`). `backend`
> names the launch path and reads `pty` even for tmux sessions, so use `terminal` (or
> `tmux_session` in `/api/sessions/live`). Other fields are omitted above.

### If your first agent is waiting for input

On its first run in a directory, the agent CLI may ask its own trust question. An
earlier Claude screen looked like this (wording and choices vary by CLI version):

```
Quick safety check: Is this a project you created or one you trust?
❯ No, exit
  Yes, I trust this folder
Enter to confirm · Esc to cancel
```

The agent can look alive and idle while it waits. Open **Terminal** in the dashboard,
read the current prompt and answer it. Coral detects known startup prompts, but this
has not been verified for every provider/version. Codex can also ask you to review
Coral's hooks before it starts; read that request in Terminal.

For a script you run yourself, the keys API accepts named keys. Inspect the
current menu before choosing a key; the `Down` then `Enter` example below applies
only when those keys select the answer you intend:

```bash
# a script you run yourself, never an agent
# NAME and SID are the agent's "name" and "session_id" from GET /api/sessions/live.
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST "http://127.0.0.1:8420/api/sessions/live/$NAME/keys" \
  -H "Content-Type: application/json" \
  -d "{\"keys\":[\"Down\"],\"agent_type\":\"claude\",\"session_id\":\"$SID\"}"
curl -H "Authorization: Bearer $(coral api-key)" -s -X POST "http://127.0.0.1:8420/api/sessions/live/$NAME/keys" \
  -H "Content-Type: application/json" \
  -d "{\"keys\":[\"Enter\"],\"agent_type\":\"claude\",\"session_id\":\"$SID\"}"
```

The CLI may remember your answer for that repository. Coral does not answer these
startup trust or hook-review questions for you.

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
| Agent launched, no output, looks hung | A trust, hook-review or sign-in prompt may be waiting. Open Terminal and read it; a launched process alone does not prove provider access. |
| `coral: command not found` after a DMG install | `install-cli.sh` was never run, or its link directory (`~/.local/bin` by default) is not on your `PATH`. |
| `coral` starts something unfamiliar | The retired `agent-coral` pip package is shadowing it. Run `which -a coral`. |
| Agents launch but never post to the board | Confirm the server's view with `/api/board/<project>/subscribers`. Agents are given `CORAL_URL` and `CORAL_DATA_DIR`; a custom wrapper that clears the environment can lose them. |
| Agent launch fails with a tmux error | tmux is missing (Linux, source builds). The macOS app bundles it. Coral starts anyway and only fails at launch time; `--backend pty` avoids tmux. |

## Next

- [Teams and multi-agent runs](teams.md) — and read
  [Worked demos](worked-demos.md) first for what team isolation does and does not do.
- [Message board](board.md)
- [Scheduled jobs](scheduled-jobs.md)
