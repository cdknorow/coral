---
title: "Troubleshooting Coral"
description: "Fixes for common problems: a stuck agent, tmux or launch errors, a missing coral command, a busy port, phone access and Windows security warnings."
---

# Troubleshooting

See the [agent troubleshooting guide](https://github.com/subgentic/coral/blob/main/coral-go/agent_docs/troubleshooting.md) for deeper notes.

## The agent launched but looks stuck

Open the agent's terminal in the dashboard and look for a trust or sign-in prompt from the agent CLI. Coral cannot sign an agent CLI in for you.

## "tmux not found" or launch fails

Coral runs each agent in its own built-in terminal (pty) by default, so tmux is needed only if you chose it (Settings > Terminal, or `--backend tmux`). With the built-in terminal, agents stop when you quit Coral. If tmux is chosen and missing:

- **macOS app:** tmux 3.7c is bundled. If it is reported missing, the app bundle was probably modified or partially copied. Re-download it.
- **Linux or source builds:** install tmux (the launch dialog shows the command for your system), or choose the built-in terminal in Settings > Terminal or with `--backend pty`.
- `CORAL_TMUX_BIN=/path/to/tmux` selects a specific executable. Order: that override, the bundled tmux (macOS), `PATH`, common install locations, your login shell.

### tmux version mismatch after an upgrade

A tmux server can outlive Coral. If a newer tmux client cannot talk to an older server you will see a protocol or "server version is too old" message. Coral never kills the old server. Point `CORAL_TMUX_BIN` at the tmux that started the sessions, and restart the server only after you have finished them, because that ends the processes inside it.

## `coral: command not found`

The DMG does not change your `PATH`. Run `/Applications/Coral.app/Contents/MacOS/install-cli.sh` (links into `~/.local/bin`, or choose `--dir`) and make sure that directory is on your `PATH`. Also check `which -a coral` for a stale copy of the old Python package.

## I cannot reach Coral from my phone

Remote access is off by default and applies only after a restart. See [Remote access](remote-access.md). Check **Saved** and **Effective** in Settings → Privacy, and make sure both devices are on the same network.

## Windows warnings: firewall, data folder, tools folder

On Windows Coral checks three things at start-up and tells you, in words, when one is more open than it needs to be. None of them stops Coral, and Coral never changes a permission or a firewall rule for you.

- **A "Windows Firewall" warning** in the corner of the dashboard. Windows made a rule for one of Coral's programs when someone clicked **Allow** on its first prompt, and the rule lets other computers reach Coral on every port. The warning names each rule and gives two PowerShell commands (run PowerShell as an administrator): `Set-NetFirewallRule -Name '<rule>' -Profile Private` limits the rule to Private networks, and `Remove-NetFirewallRule -Name '<rule>'` removes it. Copy the commands from the warning: they carry the rule's real name.
- **A "Security warning" about the data folder.** Coral's data folder (`.coral` in your profile, or the folder named by `CORAL_DATA_DIR`) can be read by other accounts, and it holds the API key, the command-line token and the databases. Move it to a folder only you can read, or remove those accounts from its permissions (Properties > Security).
- **An "Agent tools folder" row in Settings > Terminal** (shown only when there is something to say, and written in the start-up log). Accounts other than the system and administrators can change the folder that holds `coral-board` and Coral's hooks, which every agent runs by its full path. This happens with a portable copy unpacked in your own profile. Install Coral with the installer: it goes in Program Files, where only an administrator can change it.

Dismiss a warning with its **×**. See also [Privacy](privacy.md) and [Remote access](remote-access.md).

## Port already in use

Another Coral or program holds the port (default 8420). Use `--port`, and add `--home` with a different directory if you want a separate instance.

## A "Connect to Coral Hub" screen appeared

That is the one-time first-launch screen. Choose **Skip for now** (or press Escape) to keep Coral local: nothing is sent to Coral Hub until you connect, and the screen does not come back. **Settings › About › Connect to Coral Hub…** reopens it. It appears only when Coral is started with a hub URL (`--cloud-url` or `CORAL_CLOUD_URL`) or with `--cloud-connect prompt` or `auto`; otherwise Coral Hub waits on the home screen and in **Settings › Coral Hub**. Coral is free, with no licence and no usage limits.

## Agents do not post to the message board

Agents receive `CORAL_URL`, `CORAL_PORT`, `CORAL_HOST` and `CORAL_DATA_DIR`, so board commands normally follow a server on another port or `--home`. If it still fails, confirm the server's view with `coral-board subscribers`, or with curl and the API key: `curl -H "Authorization: Bearer $(coral api-key)" http://localhost:<port>/api/board/<project>/subscribers` (add `--home <dir>` to `coral api-key` if Coral runs with one). Every request needs a credential by default, even from this computer, so the same curl without the header gets `401 Unauthorized`; that is expected, and turning authentication off is not the fix.

## An agent did something nobody typed

To see exactly what Coral sends to agents' terminals, turn on **Settings > Log terminal input (debug)** (or start Coral with `CORAL_DEBUG_INPUT=1`). It's off by default. While it's on, every message, key and paste Coral sends to an agent is written to `<data dir>/input-debug.log`, with where it came from. It records what was typed, which may include secrets, so turn it off when you're done. Turning it off keeps the file; use **Delete the log** in Settings to remove it.

## A message reached the agent late

Coral never types its own messages (board messages, nudges, task notifications, a new agent's first prompt) into an agent that is waiting for you at a prompt, such as a permission or selection menu or the trust-this-folder screen: typed there, the message would answer the prompt for you. It waits until you answer, then sends the message once. If it has waited two minutes, the dashboard shows that the agent needs your input.

## Reporting a problem

Include your Coral version (startup log or `/api/system/status`), OS and architecture, and the relevant lines from `<data dir>/coral.log`. Do not paste prompts, source code or API keys.
