# Privacy

This page describes what Coral sends and stores. It does not promise that Coral never touches the network.

## Network use you should expect

- **Your agent CLIs** contact their own vendors under those vendors' terms. If you have configured a proxy for them, traffic goes through it.
- **Usage analytics** (below), unless disabled.
- **Update check.** Coral looks up the latest release version from GitHub at startup and logs a notice. Development and beta builds, and builds without a release version, skip it.
- **Remote access**, if you enable it, accepts connections from your network. It is off by default. See [Remote access](remote-access.md).
- **Coral Hub**, if you connect to it, carries dashboard access to this Coral over a tunnel. Connecting is off until you set it up, and what travels over it is redacted on this computer first — except artifacts, which are served as stored, byte for byte. A file you send to another Coral through the hub is likewise sent as stored, after it is scanned for secrets on this computer and the send is refused if one is found. See [Connecting a server to Coral Hub](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/hub.md#connecting-a-server-to-coral-hub). The [multi-server hub](multi-server-hub.md) is a different setup: a hub you run yourself.

## Usage analytics

Coral sends a fixed set of product events to help find startup and launch problems. Release builds enable analytics by default; builds without an analytics key send nothing.

**Turn it off:** **Settings → Usage analytics** takes effect immediately. To suppress analytics regardless of the saved setting, start Coral with `CORAL_TELEMETRY_DISABLED=1`. If privacy settings cannot be read at startup, analytics stay off.

### What is sent

Events come from a fixed allowlist, such as `app_opened`, `session_launched`, `team_launched`, `launch_requested`, `launch_result`, `dashboard_ready`, `dashboard_failed`, `task_completed`, and `prerequisite_check` (whether tmux, `claude` or `codex` was found, without paths or output). Properties are validated against per-event schemas, and unknown events or properties are dropped. The full, current list is available from `GET /api/system/telemetry` and in the in-app disclosure.

Every event carries Coral version, edition, operating system, architecture, schema version, a random per-process run ID, and the entrypoint (server, tray, launcher). Events also use a random installation ID stored in `<data dir>/.install_id`. It is not derived from your hardware, hostname, username or email, and deleting it creates a new identity.

### What is not sent

Event payloads do not include prompts or source code, repository, branch or file names, agent output or transcripts, raw error messages or executed commands, your name or email, or license keys, passwords and API keys.

The analytics provider receives an ordinary network connection, so it can see the source IP address of that connection. Coral does not add location data to events.

### Local files

Analytics state is kept in `<data dir>/.milestones.json`. Delivery failures are logged to `<data dir>/tracking-failures.log` (capped at 64 KB) and are not uploaded. Opting out clears queued milestone snapshots.

## What an agent can and cannot read

Every agent Coral launches gets its own session token, and Coral decides from the token who is calling. Since v1.4.105 a token limits what an agent can read, not only what it can change:

- **It can read** its own session and the sessions on its own board (their chat, terminal capture, files, diffs, events and usage turns), its own board's messages and tasks, and its own earlier conversation through `coral-agent history`. Sessions on other boards show up in the agent list as a roster only: name, agent type, board, state, model and thinking level, with no prompt, summary, branch, working directory or git state.
- **It cannot read** another board's sessions or messages, or anything that spans every board: the history list and search, the token-usage list and by-branch, every board's tasks, a team's knowledge, the identity report, the goal generator's metrics, and every workflow and scheduled job with its commands and prompts. Each is refused with a 403. Chat images follow the same rule: an agent reads only its own session's and its board's.
- **The Operator is not limited.** The dashboard, the signed command line and the API key can read all of it, and what an agent is refused is the Operator's to do, such as changing settings, running workflows or typing into another agent's terminal.

This protects against mistakes and prompt-injected agents acting through Coral's tools. It is not a defence against a malicious program running as your own user, which can read Coral's data folder. See the [authentication reference](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/auth.md) for the full list.

## Your data on disk

Coral stores sessions, history and the message board in SQLite databases under `~/.coral` (or the directory set with `--home`). Regular releases store them unencrypted. Database encryption (SQLCipher) is an **experimental, opt-in source-build feature** and is not included in release packages.

## Related

- [Remote access](remote-access.md)
- [Multi-server hub](multi-server-hub.md) (self-hosted)
- [Connecting a server to Coral Hub](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/hub.md#connecting-a-server-to-coral-hub) (hosted)
- [Privacy controls](https://github.com/cdknorow/coral/blob/main/PRIVACY_CONTROLS.md) for the settings API.
