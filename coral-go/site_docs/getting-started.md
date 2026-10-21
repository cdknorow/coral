---
title: "Getting started with Coral"
description: "Install Coral on macOS, Linux or Windows, launch your first coding agent, and see its terminal, chat and tasks in the dashboard."
---

# Getting started

Coral runs the agent CLIs you already use, each in its own terminal session, and gives you one dashboard and message board for all of them.

## What you need

| Requirement | Notes |
|---|---|
| **An agent CLI** | At least one of Claude Code (`claude`), Codex (`codex`), Antigravity (`agy`) or Pi (`pi`), installed **and signed in**. Coral does not install or authenticate these. Follow each vendor's own instructions: [Claude Code](https://code.claude.com/docs/en/overview), [Codex](https://github.com/openai/codex), [Antigravity](https://antigravity.google) (`curl -fsSL https://antigravity.google/cli/install.sh \| bash`; the installer puts `agy` in `~/.local/bin`, which Coral checks too, and which you add to your `PATH` to run `agy` in your own terminal), [Pi](https://pi.dev). |
| **git** | Needed for worktree, branch and diff features. |
| **tmux** | Included in the macOS app. Linux needs your own tmux for the default backend. See [Platforms](#platforms). |

## Platforms

| Platform | Regular release asset | tmux |
|---|---|---|
| **macOS** (13 or later) | [`Coral.dmg`](https://github.com/subgentic/coral/releases/latest/download/Coral.dmg), a universal (Apple Silicon and Intel) signed and notarized app | Bundled tmux 3.7c with its terminal definitions. Keep the app bundle intact. |
| **Windows** (x64) | [`Coral-x64.msi`](https://github.com/subgentic/coral/releases/latest/download/Coral-x64.msi) (installer) or [`Coral-x64-portable.zip`](https://github.com/subgentic/coral/releases/latest/download/Coral-x64-portable.zip). Not code-signed: SmartScreen warns that the publisher is unknown, so check the file as the [README](https://github.com/subgentic/coral#verify-your-download) describes before you run it. The default port is 8450, not 8420. | Not used: Windows uses the native PTY backend (ConPTY; there is no tmux on Windows). |
| **Linux** (x86-64 or ARM64) | [`coral-linux-amd64.tar.gz`](https://github.com/subgentic/coral/releases/latest/download/coral-linux-amd64.tar.gz) (x86-64) or [`coral-linux-arm64.tar.gz`](https://github.com/subgentic/coral/releases/latest/download/coral-linux-arm64.tar.gz) (ARM64), statically linked CLI/server tools, no desktop app | Not bundled. Install tmux yourself, or start with `--backend pty`. |

These file names always point to the newest release. Each release also keeps versioned names (`Coral.v<version>.dmg`, `Coral-<version>-x64.msi`, `Coral-<version>-x64-portable.zip`, `coral-linux-<arch>-<version>.tar.gz`): the same files.

The macOS 13 minimum is checked in the shipped binaries. It has not been tested on a macOS 13 machine.

## Install

Download the [latest release](https://github.com/subgentic/coral/releases/latest) from GitHub.

**macOS.** Open the DMG and drag `Coral.app` to Applications. To use `coral` and `coral-board` from a terminal, run:

```bash
/Applications/Coral.app/Contents/MacOS/install-cli.sh
```

It links the tools into `~/.local/bin` by default. Use `--dir <path>` for another directory, and make sure that directory is on your `PATH`.

**Windows.** Run the MSI, or extract the portable ZIP. Both are listed under [Platforms](#platforms).

**Linux.** Extract the tarball and put the binaries somewhere on your `PATH`. There is no installer or service unit.

**Homebrew (macOS).** `brew install --cask cdknorow/coral/coral` installs the same signed app from the release; the release workflow updates the tap with each release's checksum. Then run `install-cli.sh` as above for the command-line tools.

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
| `--remote` | Allow remote access for this run without saving the setting (see [remote access](remote-access.md)). With no `--host`, Coral then listens on every interface. |
| `--port <n>` | Port (default 8420; also `CORAL_PORT`) |
| `--backend tmux\|pty` | Terminal backend. Default `tmux`. |
| `--no-browser` | Do not open a browser on startup |

`coral --version` (or `coral -v`, or `coral version`) prints the version of the program you run. A running server's version is in its startup log and in `/api/system/status`.

## Your first agent

1. In the dashboard click **+New**, choose a working directory and an agent type.
2. The first time an agent CLI runs in a directory it may ask you to confirm that you trust the folder. If an agent seems idle, open its terminal in the dashboard and check for a prompt.
3. Type a task in the composer.

Launching successfully means the agent process was started. It does not prove the CLI is signed in or can reach its model. If an agent appears idle, look at its terminal.

## Permission mode

A new Claude agent starts in Claude's **Auto** permission mode: it runs without most prompts, and Claude's own background safety checks stay on. Coral no longer bypasses every prompt by default. Pick another mode in the launch form's **Permissions** section, or set your own default in **Settings > Agents > Default permission mode**; a mode you saved or chose stays as you set it, for every agent type. The other CLIs start as before: Codex and Antigravity use their own settings (Codex's Auto mode is a sandbox that cannot reach Coral's board on this computer, so Coral does not make it the default), and Pi has no permission modes.

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

## Inbox: what needs you

The **Inbox** tab, in the top bar of every Coral (on a phone, in the view dropdown), is one page for what is waiting on you. It has two tabs (the same two as Coral Hub's Inbox; Coral remembers the one you last chose):

- **Needs you**: the agents that are waiting for input, each one a button that opens that agent. The number on the tab is how many are waiting.
- **Saved for later**: the messages you bookmarked (below), with an **Open** and a **Done** list.

On a Coral running as a hub over several servers, Saved for later shows every online server's items together, each naming its server. Through Coral Hub the Inbox tab is the hub's own Inbox page. The bookmark in the top bar always opens the Inbox at Saved for later, on every page, an agent's or a team's too, and its number is everything's open items (so the same on every page). **Escape**, a second press of the bookmark, or your browser's Back button leaves it.

## Save a message for later

Hover a message in an agent's Chat view (or a board message) and use its bookmark button, **Save for later**. A toast offers **View saved** (and **Undo** when you remove one). The list follows the page you are on:

| Where you are | Saved shows |
|---|---|
| An agent's page | That agent's bookmarks: its chat, and the board messages it wrote on its team's board. It is a **Saved** tab in the right-hand sidebar. |
| A team's page (opened from the agent list, or its board) | The team's bookmarks: its board's messages and its agents' chats. |
| Home, Chats, Workflows, Jobs, Analytics, Docs, Servers | Everything, in the Inbox. |

A heading says which ("Saved: QA Engineer", "Saved: my-team", "Saved: all"), and the **Open** and **Done** numbers and the tab's number are of that page; the top bar's bookmark is always the Inbox's, everything's list and number. The toast's **View saved** opens the page's own Saved tab. Each item shows where the message was said, a short snippet, and when it was said and saved. Newest saved is first; you can search and sort them (see the next section).

On an item:

- **Open chat**, **Open board** or **Open in Chats** (for an agent that has ended) takes you back to that point of the conversation: Coral loads older messages until it finds the message, outlines it and tags it **Saved**, with a bar above to go **Back to Saved**.
- **Done** moves it to the **Done** list (**Reopen** brings it back). **Remove** deletes the bookmark.

If the message is gone (the transcript no longer has it, or it was deleted from the board), the item is marked **Gone**, keeps its snippet, and can only be removed. The list is yours alone: agents cannot add to it, and the snippet is cleaned of tokens when it is saved. It lives on this computer in `sessions.db`; on a hub over registered servers each bookmark is kept on the server that has the conversation. Coral Hub's own dashboard does not have the button yet.

## Search and filter a list

The lists in the dashboard share one bar: a search box with a count beside it, a **Filters** button (it shows how many filters are on), a **Sort** menu where the list has one, and a chip for each filter that is on. The server does the searching, so it covers the whole list, not just what is on screen. Text is a plain, case-insensitive match (`%` and `_` are ordinary characters).

| List | The search box matches | Filters | Sort |
|---|---|---|---|
| Agents | an agent's name, display name, auto name or job title (the search box above the list) | State (needs input, working, idle, asleep), Board, Machine | List order, Name, Recent activity, Cost |
| Tasks | a task's title, body or completion message | Status, Priority, Agent, Updated since | Open first, Newest, Oldest, Recently updated |
| Board | a message's text | From, Mentions me, Since, Until | (in order; **First** and **Latest** jump, the arrows step through matches) |
| Team Artifacts | the name, the task title, who produced or sent it, the board it came from or went to, and the media type | Type (image, PDF, text, other), Origin (shared here, received, unlisted), Uploaded by, Kept only, From, Until | Newest, Oldest, Name, Size |
| Activity and History events | an event's summary or tool name (not its detail) | Type, From, Until, and which groups of events to show | Newest first, Oldest first |
| Saved for later | a bookmark's title or snippet | Type (agent chat or board message), conversation | Newest saved, Oldest saved |
| Servers | a server's name or address (the box appears from six servers up) | | |

- **The count** reads "12 agents" with no filter and "3 of 12" with one. A **+** after the total ("3 of 200,000+") means the search stopped at its limit and older items were not scanned; narrow it with a filter or a date. Activity searches show **Load more** for the next page.
- **Chips**: each filter that is on shows as a chip. Click a chip to remove that filter. With two or more on, a **Clear all** chip appears; **Clear all** is also at the bottom of the Filters menu.
- The screen reader hears the count after you stop typing, not on every key.

## Desktop alerts

Coral shows a toast in the page when an agent needs input. You can also turn on **Desktop alerts** in Settings to get a browser notification while the tab is in the background. Desktop alerts are this browser only; the alert shows the agent's name and the server's name, which your operating system may keep in its notification history. It never includes what the agent said or asked. On the machine running Coral, a desktop alert may duplicate the local alert.

## Next steps

- [Using the dashboard](using-the-dashboard.md)
- [Remote access](remote-access.md)
- [Privacy](privacy.md)
- [Troubleshooting](troubleshooting.md)
