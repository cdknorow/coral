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
| **Linux** (x86-64 or ARM64) | `coral-linux-amd64-<version>.tar.gz` or `coral-linux-arm64-<version>.tar.gz`, statically linked CLI/server tools, no desktop app | Not bundled. Install tmux yourself, or start with `--backend pty`. |
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

## Auto-accept permission prompts

For an agent you trust with its task, you can let Coral answer its permission prompts for you: open the agent's menu (⋮) and turn on **Auto-accept prompts**. Coral warns you first, because the agent will then run commands, edit and delete files and use tools without asking, and that can include risky actions; use it only for trusted tasks, ideally in a sandbox or a disposable worktree. You can give it a time limit.

- Coral only approves tool-permission prompts, and only with "approve once"; it never picks "always allow". Questions, plan approvals and anything it does not recognise still wait for you.
- Risky actions such as deleting files, force-pushing, running `sudo` or changing files outside the agent's folder **can be approved**. Coral holds back some high-risk actions it recognises, as a best-effort safety net, not a guarantee.
- An amber **AUTO** chip marks the agent while it is on; click the chip in the agent's header (or use the menu) to turn it off. **Stop all auto-accept** in the top-bar menu turns it off everywhere.
- Every approval is listed in the agent's **Activity**. If the same prompt keeps coming back, Coral turns auto-accept off and tells you why.
- It turns off by itself when the agent restarts, resumes or wakes, or when its time limit runs out. It stays on if only Coral restarts while the agent keeps running.
- It runs on the Coral server, so it keeps working with the dashboard closed. From the Coral Hub it needs Launch access on this computer, and you can switch hub control off in Settings (**Coral Hub can turn on auto-accept**, on by default while access is Launch); a hub-enabled auto-accept always has a time limit of at most 8 hours.

## Running a second instance

Give it its own data directory **and** port, and keep the directory path short because a tmux socket path over about 104 characters (macOS) cannot bind:

```bash
coral --home /tmp/coral-two --port 8452 --no-browser
```

A non-default data directory no longer reads the default tmux socket, so it does not list the first instance's agents. Set `CORAL_TMUX_FALLBACK=1` only if you want the old merged view.

## Save a message for later

Hover a message in an agent's Chat view (or a board message) and use its bookmark button, **Save for later**, to add it to your **Saved** list; a toast offers **View saved** (and **Undo** when you remove one). **Saved** is a tab of the right sidebar, which every page has (an agent's page keeps its tabs, a board has Subscribers, Team artifacts, Tasks and Saved, and Chats has Saved), and the bookmark in the top bar (it shows how many are open) opens the sidebar on it, so the list stays in view while you read something else. Each kind of page remembers whether its sidebar is open, how wide it is and its last tab (a board starts with it open only in a window wide enough to leave its messages room). It has an **Open** and a **Done** list, newest saved first, each item showing where the message was said, a short snippet, and when it was said and saved. **Open chat**, **Open board** or **Open in Chats** (for an agent that has ended) takes you back to that point of the conversation: Coral loads older messages until it finds the message, outlines it, tags it **Saved** and puts the focus on it, with a bar above to go **Back to Saved**. The page you land on shows its sidebar as you last left it, so the bar's **Back to Saved**, or the top bar's button, brings the list back beside it. If the message is gone (the transcript no longer has it, or it was deleted from the board), the item is marked **Gone**, keeps its snippet, and can only be removed. The list is yours alone: agents cannot add to it, and the snippet is cleaned of tokens when it is saved. It lives on this computer in `sessions.db`; agents and boards on a registered remote server do not have the button yet.

## Desktop alerts

Coral shows a toast in the page when an agent needs input. You can also turn on **Desktop alerts** in Settings to get a browser notification while the tab is in the background. Desktop alerts are this browser only; the alert shows the agent's name and the server's name, which your operating system may keep in its notification history. It never includes what the agent said or asked. On the machine running Coral, a desktop alert may duplicate the local alert.

## Next steps

- [Remote access](remote-access.md)
- [Privacy](privacy.md)
- [Troubleshooting](troubleshooting.md)
