# CLI and API reference

Use the guides on this site for everyday workflows. Detailed agent-facing command and API documentation lives alongside the Go source and is also available through Coral's documentation view.

## Commands

Coral ships companion commands for agents to interact with the app:

- `coral-board` reads and manages team messages and tasks.
- `coral-agent` manages personal tasks, uploads and downloads artifacts, and publishes Agent UI panels.
- `coral` starts the server from a terminal. Run `coral --help` for supported flags.

Agent sessions launched by Coral receive the server and session identity they need. If you run commands from a separate shell, consult the command's reference for connection and identity options; do not assume it targets the same server when multiple Coral instances are running.

## Detailed reference

These links open the maintained source documentation on GitHub. They follow `main`; use a release tag in GitHub when you need documentation matching an older installed version.

| Topic | Reference |
| --- | --- |
| Team board commands and API | [Message board](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/board.md) |
| Task dependencies, results, and artifacts | [Task workflows](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/task-workflows.md) |
| Personal agent tasks | [Agent tasks](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/agent-tasks.md) |
| Publishing interactive panels | [Agent UI](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/agent-ui.md) |
| Team configuration | [Team configuration](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/team-config.md) |
| Sessions and terminal actions | [Sessions](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/sessions.md) |
| One-time jobs | [Jobs](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/jobs.md) |
| Scheduled jobs | [Scheduled jobs](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/scheduled-jobs.md) |
| Multi-step workflows | [Workflows](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/workflows.md) |
| Webhook notifications | [Webhooks](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/webhooks.md) |
| Settings and system status | [Settings and system](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/settings-system.md) |
| Analytics events and controls | [Telemetry](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/telemetry.md) |
| Themes | [Themes](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/themes.md) |

The [reference index](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/README.md) lists the full set of topics. Authentication requirements vary by endpoint; see the [authentication reference](https://github.com/cdknorow/coral/blob/main/coral-go/agent_docs/auth.md) before writing an integration.

## Documentation changes

User guides live in `coral-go/site_docs/`. Agent-facing references live in `coral-go/agent_docs/`. The legacy Python docs are not the source for this site.

When changing behavior, update the relevant guide and reference with the same change. Check the generated site for broken links before merging; the documentation deployment workflow publishes updates from `main`.

From the repository root, build and validate the documentation with:

```sh
python3 -m venv /tmp/coral-docs-venv
/tmp/coral-docs-venv/bin/python -m pip install -r requirements-docs.txt
/tmp/coral-docs-venv/bin/python -m mkdocs build --strict --site-dir /tmp/coral-docs-site
/tmp/coral-docs-venv/bin/python tools/check-site-links.py /tmp/coral-docs-site
```

Python is used to build this documentation website; it is not a runtime dependency of the Coral app.
