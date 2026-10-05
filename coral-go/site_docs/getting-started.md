# Getting started

This page describes Coral v1.3.23. Coral runs the agent CLIs you already use, each in its own terminal session, and gives you one dashboard and message board for all of them.

## What you need

| Requirement | Notes |
|---|---|
| **An agent CLI** | At least one of Claude Code (`claude`), Codex (`codex`), Antigravity (`agy`) or Pi (`pi`), installed **and signed in**. Coral does not install or authenticate these. Follow each vendor's own instructions: [Claude Code](https://code.claude.com/docs/en/overview), [Codex](https://github.com/openai/codex), [Antigravity](https://antigravity.google), [Pi](https://pi.dev). |
| **git** | Needed for worktree, branch and diff features. |
| **tmux** | Included in the macOS app. Linux needs your own tmux for the default backend. See [Platforms](#platforms). |

## Platforms

| Platform | Regular release asset | tmux |
|---|---|---|
| **macOS** (13 or later) | `Coral.v<version>.dmg`, a universal (Apple Silicon and Intel) signed and notarized app | Bundled tmux 3.7c with its terminal definitions. Keep the app bundle intact. |
| **Linux** (x86-64) | `coral-linux-amd64-<version>.tar.gz`, statically linked CLI/server tools, no desktop app | Not bundled. Install tmux yourself, or start with `--backend pty`. |
| **Windows** | No official Windows download is currently published. | n/a |

The macOS 13 minimum is checked in the shipped binaries. It has not been tested on a macOS 13 machine.

## Install

Download the [latest release](https://github.com/cdknorow/coral/releases/latest) from GitHub.

**macOS.** Open the DMG and drag `Coral.app` to Applications. To use `coral` and `coral-board` from a terminal, run:

```bash
/Applications/Coral.app/Contents/MacOS/install-cli.sh
```

It links the tools into `~/.local/bin` by default. Use `--dir <path>` for another directory, and make sure that directory is on your `PATH`.

**Linux.** Extract the tarball and put the binaries somewhere on your `PATH`. There is no installer or service unit.

**Homebrew.** The public Homebrew tap is not currently recommended. An earlier tap release shipped with a blank checksum, and a repair has not been independently verified. Use the GitHub release instead.

**If you once installed the old Python package** (`pip install agent-coral`), remove it. It installs commands with the same names and uses the same `~/.coral` directory and port, and can shadow this program. Check with `which -a coral` and remove it with `pip uninstall agent-coral`. Coral logs a warning at startup when it detects this.

## Start Coral

On macOS, open the app. From a terminal:

```bash
coral --no-browser
```

By default Coral stores data in `~/.coral`, uses port **8420**, and listens on this computer only (`127.0.0.1`). Open `http://localhost:8420`.

| Flag | Meaning |
|---|---|
| `--home <dir>` | Data directory (default `~/.coral`; also `CORAL_DATA_DIR`) |
| `--host <addr>` | Address to bind (also `CORAL_HOST`). Only honored for network addresses when [remote access](remote-access.md) is enabled. |
| `--port <n>` | Port (default 8420; also `CORAL_PORT`) |
| `--backend tmux\|pty` | Terminal backend. Default `tmux`. |
| `--no-browser` | Do not open a browser on startup |

There is no `--version` flag. The running version is in the startup log and in `/api/system/status`.

## Your first agent

1. In the dashboard click **+New**, choose a working directory and an agent type.
2. The first time an agent CLI runs in a directory it may ask you to confirm that you trust the folder. If an agent seems idle, open its terminal in the dashboard and check for a prompt.
3. Type a task in the composer.

Launching successfully means the agent process was started. It does not prove the CLI is signed in or can reach its model. If an agent appears idle, look at its terminal.

## Running a second instance

Give it its own data directory **and** port, and keep the directory path short because a tmux socket path over about 104 characters (macOS) cannot bind:

```bash
coral --home /tmp/coral-two --port 8452 --no-browser
```

A non-default data directory no longer reads the default tmux socket, so it does not list the first instance's agents. Set `CORAL_TMUX_FALLBACK=1` only if you want the old merged view.

## Next steps

- [Remote access](remote-access.md)
- [Privacy](privacy.md)
- [Troubleshooting](troubleshooting.md)
