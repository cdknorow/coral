# Install Path Verification — v1.0.8

Task #22. Every claim below was produced by running the stated command against the
**real published v1.0.8 artifacts** downloaded from GitHub Releases, not local builds.
Where I could not verify something, it is listed under "Not Verified" rather than assumed.

- **Verifier:** Developer Advocate
- **Date:** 2026-08-28
- **Host:** macOS 26.4 (Darwin 25.4.0), arm64 Apple Silicon
- **Artifacts:** `Coral.v1.0.8.dmg`, `coral-linux-amd64-1.0.8.tar.gz`

## Verdicts

| Platform | Verdict | One-line reason |
|---|---|---|
| macOS DMG | **WORKS WITH CAVEATS** | Signed, notarized, runs. First screen is a pricing page; CLI installer is broken on Apple Silicon. |
| Linux tarball | **UNVERIFIED — INSPECTED ONLY** | No Linux machine, VM, or container on the host. Binary inspected, never executed. |
| Windows MSI | **BROKEN** | Artifact has never been built or published, and the code does not compile for Windows. |

---

## Windows — BROKEN

### No artifact has ever existed

Assets on every published release (`v1.0.0`, `v1.0.2`–`v1.0.8`) are exactly two files:
`coral-linux-amd64-<ver>.tar.gz` and `Coral.v<ver>.dmg`. No `.msi`, no portable `.zip`, ever.

`.github/workflows/release.yml:65` gates the job:

```yaml
build-windows:
  if: contains(github.ref_name, '-windows') || contains(github.ref_name, '-all')
```

Every tag on origin (`v1.0.0`–`v1.0.8`, plus a stray tag named `push`) lacks both suffixes.
**The Windows job has never executed.**

### The build would fail if it did run

```console
$ GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...
# github.com/cdknorow/coral/internal/background
internal/background/workflow_runner.go:143:26: undefined: syscall.Getpgid
internal/background/workflow_runner.go:148:13: undefined: syscall.Kill
internal/background/workflow_runner.go:153:24: undefined: syscall.Kill
internal/background/workflow_runner.go:154:15: undefined: syscall.Kill
internal/background/workflow_runner.go:360:41: unknown field Setpgid in struct literal of type syscall.SysProcAttr
internal/background/workflow_runner.go:534:41: unknown field Setpgid in struct literal of type syscall.SysProcAttr
```

Per binary:

| Binary | GOOS=windows |
|---|---|
| `coral.exe` | **FAILED** — this is the server itself |
| `launch-coral.exe` | **FAILED** |
| `coral-board.exe` | OK |
| `coral-hook-agentic-state.exe` | OK |
| `coral-hook-message-check.exe` | OK |
| `coral-hook-task-sync.exe` | OK |

One file, six call sites, all Unix-only process-group syscalls. The fix is a
`_unix.go` / `_windows.go` split — the pattern the codebase already uses for terminal backends.

### tmux is NOT the Windows blocker

`cmd/coral/main.go:60-63`:

```go
defaultBackend := "tmux"
if runtime.GOOS == "windows" {
    defaultBackend = "pty"
}
```

A full native-PTY backend exists behind the `TerminalBackend` interface with no build
tags, plus a `--backend pty` flag on every platform, and Windows pulls in
`UserExistsError/conpty`. **The architecture is Windows-ready; the build is not.**

**Retracted 2026-08-28.** An earlier version of this file said the PTY backend was confirmed
working because an agent launched on it in 0.58 s. That rested on the launch response's
`backend` field, which reports `pty` whenever a backend exists regardless of what runs — a
defect proved later in this same document. Running the shipped binary with `--backend pty`
explicitly fails at spawn:
`pty spawn failed: startPTYProcess: fork/exec /bin/zsh: operation not permitted`.
So no agent here was ever PTY-backed. The backend is **unexercised** — not known-broken and
not known-working. The EPERM is consistent with a sandbox restriction (this shell forks
`/bin/zsh` fine, and the same binary spawns agents on tmux all day), but the two causes cannot
be told apart from inside the sandbox.

### Recommendation

**Do not promote Windows.** Even after the compile is fixed, cross-compiling is not
verification: nobody on the team has a Windows machine, so the PTY backend, MSI installer,
tray, and webview would all be unexercised on the platform. The operator's decision was
"verify, then promote" — verification is impossible today, so by that decision's own terms
Windows is out. The real ask is a Windows box or a `windows-latest` CI smoke job.

---

## macOS DMG — WORKS WITH CAVEATS

### Gatekeeper does NOT block it

```console
$ spctl -a -vvv -t exec Coral.app
Coral.app: accepted
source=Notarized Developer ID
origin=Developer ID Application: Chris Knorowski (33UR27T84L)

$ codesign --verify --deep --strict --verbose=2 Coral.app
Coral.app: valid on disk
Coral.app: satisfies its Designated Requirement

$ file Coral.app/Contents/MacOS/coral
Mach-O universal binary with 2 architectures: [x86_64] [arm64]
```

Signed **and** notarized. No right-click-Open, no "unidentified developer" screen, no
`xattr` workaround. This is a real asset that current copy does not mention at all.

**Timings:** server start → dashboard serving, same second. `HTTP 200` in 0.5 ms local.
Agent launch via API: **0.58 s**.

### Caveat 1 — the first screen a new user sees is a pricing page

On a fresh data dir with the shipped production binary:

```console
$ curl -s http://localhost:8451/ | grep -o '<title>.*</title>'
<title>Coral — Activate License</title>
```

`internal/server/server.go:663-664`:

```go
if s.cfg.LicenseRequired() && !s.licenseMgr.IsValid() &&
    s.launchCounter.IsNagLaunch() && r.URL.Query().Get("skip_activation") != "1" {
```

`internal/license/launch_counter.go:29-31`:

```go
func (lc *LaunchCounter) IsNagLaunch() bool { return lc.read()%3 == 1 }
```

The counter starts at 1 and `1%3 == 1`, so **the very first launch nags**, then every third
(1, 4, 7…).

It is a nag, not a wall — the page says "Free and fully unlocked", and
`GET /?skip_activation=1` returns the real 131 KB dashboard (`HTTP 200`), verified. So
"Coral is free to use" is **true**. But `README.md:76-78` says "Open http://localhost:8420 …
Click **+New** to launch your first agent", and that is not what happens.

The skip is a query parameter and sets **no cookie** — a subsequent plain `GET /` returns
the nag again (verified).

### Caveat 2 — `install-cli.sh` cannot work on a clean modern Mac

The bundled CLI installer runs `mkdir -p /usr/local/bin` then symlinks, under `set -e`.

```console
$ ls -ld /usr/local
drwxr-xr-x  2 root  wheel  /usr/local        # root-owned
$ ls -ld /usr/local/bin
ls: /usr/local/bin: No such file or directory # absent on Apple Silicon
```

Non-root `mkdir` into root-owned `/usr/local` fails and `set -e` aborts the script. A user
without `sudo` gets no CLI tools. ARM Homebrew uses `/opt/homebrew/bin`, so `/usr/local/bin`
is not the right target anyway.

Compounding: the README never mentions this script exists. After a DMG install there is no
`coral` or `coral-board` on `PATH`, and `README.md:71-74` says to run `./coral`, which
matches a source build rather than a DMG install.

### Caveat 3 — no `--version` flag

```console
$ coral --version
flag provided but not defined: -version
```

No way to ask an installed build what version it is. The version *is* known — startup logs
`version="1.0.8"` — it is simply not exposed. This matters given three competing version
lineages in the wild (PyPI 4.4.1, `Casks/coral.rb` 2.3.1, actual v1.0.8).

### Caveat 4 — startup dumps the full `PATH` on every invocation

Including for `--help`. The first thing a user sees is a ~900-character diagnostic line
beginning `DEV-TMUX-DIAGNOSTIC-2026-06-03-02`. Cosmetic, trivial, bad first impression.

---

## Linux tarball — INSPECTED, NOT RUN

No Linux machine, and this host has no Docker, Podman, Colima, Lima, or QEMU (checked).
**This is not verified and should not be described as such.**

```console
$ file coral
ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped
```

**Statically linked** is a genuine win — no glibc version dependency; should run on musl/Alpine
and old distros alike. Worth saying in copy *once someone actually runs it*.

Contents — 6 bare binaries in a flat directory:

```
coral  launch-coral  coral-board
coral-hook-agentic-state  coral-hook-message-check  coral-hook-task-sync
```

No install script, no README, no systemd unit, no `.desktop` entry.

Two gaps worth noting:

- **No `coral-tray`, no `coral-app`.** The macOS bundle ships both. Linux is headless/CLI only,
  yet the license page we serve advertises *"Native desktop app (macOS & Linux)"*. That claim is
  not supported by the tarball's contents.
- **x86-64 only.** No arm64 build exists, so ARM Linux (Raspberry Pi, Ampere, Graviton, most ARM
  cloud VMs) has nothing to install.

---

## Filename mismatches

`README.md:62-63` names the downloads `Coral.dmg` and `coral-linux-amd64.tar.gz`.
Actually published: **`Coral.v1.0.8.dmg`** and **`coral-linux-amd64-1.0.8.tar.gz`**.
Both carry a version. Any doc or script using the bare names is wrong.

---

## Legacy PyPI package collides with the shipped product

`pip install agent-coral` (still recommended by `cdknorow.github.io/coral`) installs console
scripts with **identical names** to ours. From the 4.4.1 wheel's `entry_points.txt`:

```ini
[console_scripts]
coral = coral.web_server:main
coral-board = coral.messageboard.cli:main
coral-dashboard = coral.web_server:main
coral-hook-agentic-state = coral.hooks.agentic_state:main
coral-hook-message-check = coral.hooks.message_check:main
coral-hook-task-sync = coral.hooks.task_state:main
coral-tray = coral.tray:main
launch-coral = coral.launch:main
```

It also shares the **`~/.coral` data directory**, the **`sessions.db` / `messageboard.db`
filenames**, and **port 8420**.

`pip --user` installs to `~/.local/bin`; our `install-cli.sh` symlinks to `/usr/local/bin`.
On a default macOS `PATH`, `~/.local/bin` comes **first** (position 2 vs 4 on this host).
So the exact sequence our own stale docs site recommends leaves the user typing `coral` and
**silently running the Python product** on a real server on 8420. It looks like it worked.

Two products sharing a DB filename with different schemas is a data-corruption vector. I did
**not** run the Python package to explore that — `~/.coral` holds the live production instance.

### Detection and cleanup (belongs in the quickstart)

```bash
which -a coral coral-board launch-coral   # more than one hit = both installed
pip uninstall agent-coral                 # removes the shadowing scripts
```

---

## "Works with any CLI agent" — NOT SUPPORTED

`internal/agent/agent.go:157-168`:

```go
func GetAgent(agentType string) Agent {
    switch agentType {
    case at.Gemini: return &GeminiAgent{}
    case at.Codex:  return &CodexAgent{}
    case at.Pi:     return &PiAgent{}
    default:        return &ClaudeAgent{}
    }
}
```

A hardcoded switch over four implementations. `internal/agenttypes/types.go:8-12` defines five
constants (`claude`, `gemini`, `codex`, `pi`, `terminal` — the last a raw shell, not an agent).
A search for any extension mechanism returns nothing:

```console
$ grep -rniE 'custom_agent|customagent|user_defined|plugin|registerAgent' --include='*.go' internal/
(no results)
```

No registry, no plugin interface, no config file, no UI form. Adding a fifth agent means editing
that switch and recompiling.

**Silent-fallback bug:** because of `default:`, any unrecognized agent type returns `ClaudeAgent`
with no error. Launching `agent_type: "shell"` returned `HTTP 200`, `ok: true`, and started a real
`claude` process (observed in the terminal capture; session killed immediately).

`shell.go` is **not** an agent — it holds `detectShell()`, PATH helpers, and
`SanitizeShellValue()`. Shell plumbing, despite the name.

**Supportable:** "Works with Claude Code, Codex, Gemini CLI, and Pi.dev." Four named agents,
each with a real implementation and a documented install command.

**Cut:** the features-table row "Any CLI agent … and any CLI-based tool", `README.md:81`
"Any CLI-based agent can be added", and `README.md:37` "or any CLI-based agent".

---

## Isolation defect — `CORAL_DATA_DIR` does not fully isolate

Started the shipped binary with a separate data dir **and** port:

```bash
coral --home <temp>/coral-macos-test --host 127.0.0.1 --port 8451 --no-browser
```

```console
$ curl -s localhost:8451/api/sessions/live   # count: 14
```

The isolated server listed **13 production sessions**, including all five GTM agents. The same
handler set exposes `POST /api/sessions/live/{name}/kill`, so a test server on an isolated port
and database can enumerate and terminate production agents.

### Root cause 1 — unconditional tmux socket fallback

`internal/tmux/client.go:185`:

```go
c := &Client{TmuxBin: "tmux", FallbackToDefault: true, ...}
```

`internal/tmux/client.go:212-213`, verbatim comment:

```go
// Always merge sessions from the default socket for backward compatibility
```

No flag or env var disables it. `--home` / `CORAL_DATA_DIR` isolates the **database** but not
**live session discovery**.

### Root cause 2 — a long data dir silently disables the tmux backend

```console
$ tmux -S <temp>/coral-macos-test/tmux.sock ls
error connecting to ... (File name too long)
```

That path is **131 characters**; the macOS unix-socket `sun_path` limit is **104**. tmux can never
bind it. The server logged `Using tmux terminal backend` and started clean, but the launch response
returned `{"backend":"pty", "ok":true}`. That field is unreliable, and the PTY backend cannot
spawn on this host at all, so **what actually ran is undetermined** — most likely tmux via the
default-socket fallback. What is certain is that the *configured* socket was never used.

This matters to the team directly: the sanctioned scratchpad temp paths are ~90 characters before
anything is appended, so anyone following the standard instruction is likely over the limit —
not testing the socket you configured, *and* able to kill production agents
through the merge.

### Guidance until fixed

- Use a **short** data dir (`/tmp/coral-t1`), keeping the socket path under 104 chars.
- Immediately after start, run `curl -s localhost:<port>/api/sessions/live` and count. Sessions you
  did not create mean the merge is active — treat every write endpoint as production-dangerous.
- Never call `/kill`, `/restart`, or `/send` against a session you did not personally launch.

### Suggested fixes

1. Honor `CORAL_TMUX_NO_FALLBACK=1` to disable `FallbackToDefault`. One line; unblocks safe testing.
2. Fail loudly (or refuse to claim the tmux backend) when the socket path exceeds the platform limit,
   instead of degrading to PTY while logging `Using tmux terminal backend`.
3. Longer term: tag sessions with the owning instance id and filter discovery by it.

---

## Not Verified — stated plainly

1. **Linux runtime.** No machine, VM, or container.
2. **Windows anything.** Does not compile; nothing to install.
3. **First-run EULA.** The server logged `[EULA] previously accepted` even on a brand-new data dir,
   so EULA state is global to the machine and was accepted here long ago. The build is `tier=prod`
   with `eula=true`, so a gate exists that I never saw. Needs a clean machine.
4. **tmux-absent behavior.** Not executed. tmux discovery has four fallbacks (PATH,
   `CORAL_TMUX_BIN`, hardcoded common paths including `/opt/homebrew/bin/tmux`, and a login-shell
   probe), and removing tmux from this host would kill the production instance running the team.
   From `internal/startup/startup.go:106-113`, a missing tmux does **not** fail startup — it logs a
   warning, the dashboard still loads, and agent launch fails later. **Read, not run.**
5. **Finder drag-to-Applications and LaunchServices registration.** `/Applications/Coral.app` holds
   the live production install running the team. The bundle was installed to an isolated directory
   and the shipped binary run from there. Binary behavior is identical; the Finder UX is unverified.

## Safety

All testing used an isolated data dir on port 8451. Production (`~/.coral`, port 8420,
`/Applications/Coral.app`) was never written to. Verified after teardown: port 8451 free, 8420
returning `HTTP 200`, all 20 production tmux sessions alive, DMG detached.
