# Multi-server hub

Run Coral on several machines (a laptop, a workstation, a cloud box) and see all of their agents and teams in one dashboard. One Coral server acts as the **hub**. The others are **remotes** (also called spokes). The hub shows the remotes' agents next to its own, lets you open their chat and terminal views, and lets you launch agents and teams on them.

A remote is an ordinary standalone Coral server. It needs no special mode and no changes. The hub calls the same API your browser uses, through an authenticated proxy.

!!! note "What the hub does not do"
    A team, its message board and its agents all live on one server. The hub does not make one team span two machines. Cost and analytics dashboards show the hub's own data only.

## Set it up

### 1. Make each remote reachable

On every remote server:

1. Turn on [remote access](remote-access.md) and restart, so Coral listens on a network address and not only on `127.0.0.1`.
2. Copy its API key. It is shown with the QR code, and stored in `~/.coral/api_key`.

Coral serves plain HTTP. Use a network you trust, a VPN or tailnet, or put your own encrypted tunnel in front of it.

### 2. Start the hub

Start the server you will use as the hub in hub mode:

```bash
coral --hub
# or
CORAL_HUB=1 coral
```

Hub mode is a startup setting on purpose. It cannot be switched on from the web UI, so a standalone server cannot be turned into a proxy by someone who can only reach its page. Without `--hub` nothing about the hub exists: the hub routes are not registered and the UI shows none of it.

### 3. Add the remote

In the hub's top bar, open the settings menu and choose **Servers**. This entry appears only in hub mode. Add a server with:

| Field | Meaning |
| --- | --- |
| **ID** | A short name using lowercase letters, digits and `-` (up to 32 characters). It appears in URLs and cannot be `local`. |
| **Label** | The display name. |
| **URL** | The remote's address, for example `http://192.168.1.20:8420`. |
| **API key** | The remote's key. Write-only: it is never shown again. |
| **Allow private network** | Turn this on for LAN, tailnet or localhost remotes. |

The hub checks the remote's health with the key before it saves anything. Use **Test** later to re-run the check.

!!! warning "Private addresses are blocked by default"
    To stop the hub being used to reach arbitrary hosts, it refuses private and reserved addresses unless **Allow private network** is on for that server. The address is checked again each time the hub connects, not only when you register it.

## Using it

- **Sidebar.** Remote agents and teams appear with a server badge. If there are no remotes, the sidebar looks exactly as it does without the hub. The Servers dialog and a status strip show whether each remote is online, unreachable, or has a rejected key.
- **Chat and terminal.** Select a remote agent as you would a local one. Chat, terminal, files, tasks, notes and the message board all go through the hub to the remote, and their paths are the remote's paths.
- **Offline remotes.** When a remote is unreachable its last known agents stay in the list, greyed out and not actionable, until it comes back.
- **Popout.** A remote agent opens at `/agent/{server}/{id}`. The old `/agent/{id}` still means a local agent.

### Launching on a remote

The launch dialogs (agent, team, terminal, quick launch) gain a **Server** picker when at least one remote is registered. Pick a remote and:

- **Browse** lists the remote's folders. It asks the remote to list its own filesystem, so you see the remote's folders, starting at the remote's home directory, not the hub's.
- Paths are only ever interpreted by the server that will run the agent. Changing the server clears the directory field, so a path from one machine is never sent to another.
- Agent types, installed-CLI checks, default models and the default working directory all come from the selected remote. If `claude` is missing on the remote you are told, even if it is installed on the hub.
- If the remote is offline or rejects the key, the launch fails with an error. It is never started on the hub instead.
- Adding an agent to an existing remote team stays on that team's server, and the picker is locked.

## Security

- **Keys are encrypted at rest.** Each remote's API key is encrypted with AES-256-GCM, bound to the server ID. The master key lives in `<Coral data dir>/.remote_secret_key` with permissions `0600`, outside the database, so a copy of the database file alone does not expose the keys. Set `CORAL_SECRET_KEY` to supply the master key from your own secret store instead.
- **Keys never reach the browser.** The browser's own credentials are stripped before a request is forwarded, and the remote's key is added by the hub. No API returns a stored key.
- **Back up the key file with the database.** If `.remote_secret_key` is lost, the remotes show `key_unreadable` and you re-enter each key. Nothing else is affected.
- **The hub holds keys that give full control of each remote.** Anyone who can use the hub's web UI can reach every registered remote, so protect the hub as you would the remotes themselves.
- Encryption protects the database and its backups. It does not protect against someone who already controls the hub's user account or process.

## Troubleshooting

| You see | Likely cause |
| --- | --- |
| No **Servers** entry | The server was not started with `--hub` or `CORAL_HUB=1`. |
| Registering fails with a private-address error | Turn on **Allow private network** for that server. |
| Status **unreachable** | The remote is down, the URL or port is wrong, or the remote is still listening on loopback only. Enable remote access on it and restart. |
| Status **unauthorized** | The remote rejected the key. It may have been regenerated. Edit the server and paste the new key. |
| Status **key_unreadable** | The hub's master key changed or is missing. Re-enter the key. |
| A remote agent is greyed out | The remote is offline. Its agents return when it reconnects. |
| New remote agent takes a few seconds to appear after launch | The hub learns of it on its next poll (about 5 seconds). |

## Known limits in this version

- Resuming an ended session from history uses the hub's own history, so a remote's past sessions cannot be resumed from the hub. Open the remote's own dashboard for that.
- The hub's History tab, cost and analytics views cover the hub only.
- Directory browsing assumes forward-slash paths, so a Windows remote is not supported for browsing.
- A hub shows only the agents that run on each remote directly. If a remote is itself a hub, its own remotes are not shown a second time.
- Do not register the hub as a remote of itself. Its agents would appear twice.
