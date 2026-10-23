# Workflow Quickstart

Workflows are multi-step automations that chain shell commands and AI agents together. You define a sequence of steps — run a test suite, have an agent analyze the results, send a notification — and Coral executes them in order, passing output from one step to the next.

> **These are Operator actions.** Every `/api/workflows…` and `/api/scheduled/…` route, reads included (list, get, runs, a run's output), is Operator-only: run the `curl` commands below from the Operator's own signed command line (your own terminal) or use the dashboard. An agent's session token is refused on them (403 `forbidden_for_agent`; a token-less call is 401 under strict local auth).

---

## Your First Workflow: Daily Motivation

Let's build a simple workflow that generates a motivational message and shows it as a macOS popup.

### The steps

1. **generate-message** (agent) — Ask Claude to write a short motivational message
2. **show-popup** (shell) — Display the message in a macOS dialog using `osascript`

### The workflow JSON

```json
{
  "name": "daily-motivation",
  "description": "Generates a random motivational message and shows it as a popup",
  "steps": [
    {
      "name": "generate-message",
      "type": "agent",
      "prompt": "Generate a single short, unique, and inspiring motivational message (2-3 sentences max). Output ONLY the message text, nothing else — no quotes, no labels, no explanation."
    },
    {
      "name": "show-popup",
      "type": "shell",
      "command": "MSG=$(cat {{prev_stdout}}) && osascript -e \"display dialog \\\"$MSG\\\" with title \\\"Daily Motivation\\\" buttons {\\\"OK\\\"} default button \\\"OK\\\"\""
    }
  ],
  "default_agent": { "agent_type": "claude" }
}
```

Notice `{{prev_stdout}}` in the shell step — this is a **template variable** that gets replaced with the file path containing stdout from the previous step. This is how steps pass data to each other.

### Create it

```bash
# a script you run yourself, never an agent
curl -X POST http://localhost:8420/api/workflows \
  -H "Authorization: Bearer $(coral api-key)" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "daily-motivation",
    "description": "Generates a random motivational message and shows it as a popup",
    "steps": [
      {
        "name": "generate-message",
        "type": "agent",
        "prompt": "Generate a single short, unique, and inspiring motivational message (2-3 sentences max). Output ONLY the message text, nothing else — no quotes, no labels, no explanation."
      },
      {
        "name": "show-popup",
        "type": "shell",
        "command": "MSG=$(cat {{prev_stdout}}) && osascript -e \"display dialog \\\"$MSG\\\" with title \\\"Daily Motivation\\\" buttons {\\\"OK\\\"} default button \\\"OK\\\"\""
      }
    ],
    "default_agent": { "agent_type": "claude" }
  }'
```

The response includes the workflow's `id` — you'll need it to trigger runs.

### Trigger it

```bash
# a script you run yourself, never an agent
curl -X POST http://localhost:8420/api/workflows/12/trigger \
  -H "Authorization: Bearer $(coral api-key)"
```

This returns a `run_id`. The workflow starts executing immediately.

### Check the run

```bash
# a script you run yourself, never an agent
curl http://localhost:8420/api/workflows/runs/44 \
  -H "Authorization: Bearer $(coral api-key)"
```

The response shows the overall run status and a `steps` array with per-step `status`, `stdout`, `stderr`, `started_at`, and `finished_at`. Poll this endpoint until the status is `completed`, `failed`, or `killed`.

You can also trigger and monitor workflows by name:

```bash
# a script you run yourself, never an agent
curl -X POST http://localhost:8420/api/workflows/by-name/daily-motivation/trigger \
  -H "Authorization: Bearer $(coral api-key)"
```

### Schedule it

Want your motivational popup every weekday morning? Create a scheduled job:

```bash
# a script you run yourself, never an agent
curl -X POST http://localhost:8420/api/scheduled/jobs \
  -H "Authorization: Bearer $(coral api-key)" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "morning-motivation",
    "description": "Runs the daily-motivation workflow every weekday at 9am",
    "cron_expr": "0 9 * * 1-5",
    "timezone": "Local",
    "job_type": "workflow",
    "workflow_id": 12,
    "enabled": 1
  }'
```

The `cron_expr` follows standard cron syntax — `0 9 * * 1-5` means "at 9:00 AM, Monday through Friday." Set `timezone` to `"Local"` to use the server's timezone, or specify one like `"America/New_York"`.

You can manage jobs from the **Jobs** tab in the UI, or via the API:

```bash
# a script you run yourself, never an agent
# List all jobs
curl http://localhost:8420/api/scheduled/jobs \
  -H "Authorization: Bearer $(coral api-key)"

# Disable a job
curl -X PUT http://localhost:8420/api/scheduled/jobs/1 \
  -H "Authorization: Bearer $(coral api-key)" \
  -H "Content-Type: application/json" \
  -d '{"enabled": 0}'

# Check recent runs
curl http://localhost:8420/api/scheduled/jobs/1/runs \
  -H "Authorization: Bearer $(coral api-key)"
```

> **Tip:** Coral ships with an example daily-motivation workflow and an example morning-motivation job. The job is switched off, so nothing runs (and no CLI credit is spent) until you turn it on from the Jobs tab; you can also trigger the workflow by hand to see it in action.

---

## A More Advanced Example: Email Triage

Here's a taste of what a more complex workflow looks like:

```json
{
  "name": "email-triage",
  "description": "Fetch unread emails, triage them, and draft responses",
  "repo_path": "/home/user/workspace",
  "steps": [
    {
      "name": "fetch-emails",
      "type": "shell",
      "command": "./scripts/fetch-unread-emails.sh",
      "timeout_s": 60
    },
    {
      "name": "triage",
      "type": "agent",
      "prompt": "Read the emails from {{prev_stdout}}. For each email, classify as urgent/normal/low and draft a response. Save your analysis to triage-report.md.",
      "output_artifact": "triage-report.md",
      "timeout_s": 300
    },
    {
      "name": "notify",
      "type": "shell",
      "command": "cat {{prev_artifact_content}} | ./scripts/send-to-slack.sh",
      "continue_on_failure": true
    }
  ],
  "default_agent": { "agent_type": "claude" },
  "max_duration_s": 600
}
```

This workflow uses `output_artifact` to capture a specific file from an agent step, and `{{prev_artifact_content}}` to pass that file's content to the next step. It also uses `timeout_s` for per-step limits and `continue_on_failure` so the notification step doesn't block the overall result.

Workflows like this can be [scheduled](scheduled-jobs.md) to run automatically on a cron schedule.

---

## Key Concepts

**Step types:**
- `shell` — Runs a command. Requires `command`.
- `agent` — Sends a prompt to an AI agent. Requires `prompt` and an `agent_type` (set per-step or via `default_agent`).

**Template variables** let steps reference earlier output:
- `{{prev_stdout}}` / `{{prev_stderr}}` — Output from the previous step
- `{{step_N_stdout}}` / `{{step_N_stderr}}` — Output from step N (0-indexed)
- `{{prev_artifact_content}}` — Content of the previous step's output artifact

**Resilience options:**
- `continue_on_failure` — Keep running subsequent steps even if this one fails
- `timeout_s` — Per-step timeout (1–86400 seconds)
- `max_duration_s` — Overall workflow timeout

**Hooks** let you run commands when steps complete or fail — useful for sending notifications. See [Hooks](hooks.md) for the format.

---

## What's Next?

- [Workflows](workflows.md) — Full API reference with all fields, error codes, and run management
- [Hooks](hooks.md) — Add lifecycle hooks for notifications and custom behavior
- [Scheduled Jobs](scheduled-jobs.md) — Run workflows automatically on a cron schedule
- Try the **Build with Agent** button in the Workflows tab — an AI assistant that helps you create workflows through conversation
