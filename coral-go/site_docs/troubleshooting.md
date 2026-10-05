# Troubleshooting

Applies to Coral v1.3.23. See the [agent troubleshooting guide](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/troubleshooting.md) for deeper notes.

## The agent launched but looks stuck

Open the agent's terminal in the dashboard and look for a trust or sign-in prompt from the agent CLI. Coral cannot sign an agent CLI in for you.

## "tmux not found" or launch fails

- **macOS app:** tmux 3.7c is bundled. If it is reported missing, the app bundle was probably modified or partially copied. Re-download it.
- **Linux or source builds:** install tmux, or start Coral with `--backend pty`.
- `CORAL_TMUX_BIN=/path/to/tmux` selects a specific executable. Order: that override, the bundled tmux (macOS), `PATH`, common install locations, your login shell.

### tmux version mismatch after an upgrade

A tmux server can outlive Coral. If a newer tmux client cannot talk to an older server you will see a protocol or "server version is too old" message. Coral never kills the old server. Point `CORAL_TMUX_BIN` at the tmux that started the sessions, and restart the server only after you have finished them, because that ends the processes inside it.

## `coral: command not found`

The DMG does not change your `PATH`. Run `/Applications/Coral.app/Contents/MacOS/install-cli.sh` (links into `~/.local/bin`, or choose `--dir`) and make sure that directory is on your `PATH`. Also check `which -a coral` for a stale copy of the old Python package.

## I cannot reach Coral from my phone

Remote access is off by default and applies only after a restart. See [Remote access](remote-access.md). Check **Saved** and **Effective** in Settings → Privacy, and make sure both devices are on the same network.

## Port already in use

Another Coral or program holds the port (default 8420). Use `--port`, and add `--home` with a different directory if you want a separate instance.

## A pricing or supporter page appeared

Coral is free and fully unlocked. A supporter reminder can appear on some launches. It is shown at most once per Coral start, so reloading the dashboard does not bring it back. Choose **Continue Free** to go to the dashboard. It is never shown once a license is activated.

## Agents do not post to the message board

Agents receive `CORAL_URL`, `CORAL_PORT`, `CORAL_HOST` and `CORAL_DATA_DIR`, so board commands normally follow a server on another port or `--home`. If it still fails, confirm the server's view with `curl http://localhost:<port>/api/board/<project>/subscribers`.

## Reporting a problem

Include your Coral version (startup log or `/api/system/status`), OS and architecture, and the relevant lines from `<data dir>/coral.log`. Do not paste prompts, source code or API keys.
