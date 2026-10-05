# Privacy

This page describes what Coral v1.3.23 sends and stores. It does not promise that Coral never touches the network.

## Network use you should expect

- **Your agent CLIs** contact their own vendors under those vendors' terms. If you have configured a proxy for them, traffic goes through it.
- **Usage analytics** (below), unless disabled.
- **Update check.** Coral looks up the latest release version from GitHub at startup and logs a notice. Development and beta builds, and builds without a release version, skip it.
- **Webhooks, Slack or Discord notifications** you configure send the content you set up.
- **Remote access**, if you enable it, accepts connections from your network. It is off by default. See [Remote access](remote-access.md).

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

## Your data on disk

Coral stores sessions, history and the message board in SQLite databases under `~/.coral` (or the directory set with `--home`). Regular releases store them unencrypted. Database encryption (SQLCipher) is an **experimental, opt-in source-build feature** and is not included in release packages.

## Related

- [Remote access](remote-access.md)
- [Privacy controls](https://github.com/cdknorow/coral/blob/main/PRIVACY_CONTROLS.md) for the settings API.
