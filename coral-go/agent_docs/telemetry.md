# Telemetry

Coral sends a fixed set of product usage events to help identify startup and agent-launch problems. **Settings → Usage analytics** disables outgoing analytics immediately. Release builds enable analytics by default; builds without an injected analytics key send nothing. Remote access is a separate setting.

Set `CORAL_TELEMETRY_DISABLED=1` before starting Coral to suppress analytics regardless of the saved setting. Release verification and frontend test runners use this override. No PostHog browser SDK, session replay, or automatic page-content capture is installed.

## Events

The runtime disclosure is generated from `internal/tracking/events.go`. New funnel events use schema version 3; the update events and `upgrade`'s `from_version` are schema version 4.

| Event | Meaning | Additional properties |
|---|---|---|
| `install` | First tracked startup for this data directory | — |
| `upgrade` | First tracked startup after the version changes | `from_version` (the version that last ran on this install, a release number only) |
| `update_notice_shown` | The dashboard's update toast or the tray's notification showed that a newer release exists. Once per install, release and place, and only for the release this server's own update check offered (the dashboard cannot name another); an install remembers at most 20 such events. It says the notice was shown, not that anyone updated: Coral does not install updates itself yet | `to_version` (a release number), `source` (`dashboard` or `tray`), `channel` (`approved` or `agentic`) |
| `update_offer_opened` | The person opened the release page the notice offers (the toast's Download link, or the tray's Check for Updates). Once per install, release and place. It says a page was opened, not that anything was installed | `to_version`, `source`, `channel`, `method` (`download_link`) |
| `app_opened` | Coral process startup; not proof that a dashboard loaded | — |
| `installation_active_week` | This install ran Coral during an ISO week (UTC). Sent at most once per install per week: at startup, and the first time that week the dashboard is open for a day, an agent or team is launched, or a task is completed (so a Coral that runs for weeks counts every week it is used) | — |
| `session_launched` | Individual launch handler created/submitted the agent process | — |
| `team_launched` | Team launch with at least one member started | `agent_count`, `requested_agents`, `started_agents`, `failed_agents` |
| `prerequisite_check` | Coral checked tmux or a supported agent CLI prerequisite: whether it is installed and, for Claude and Codex, whether it is signed in | `tool`, `status`, `source` |
| `launch_requested` | Server received an individual or team launch attempt | `kind`, `attempt_id`, `provider`, `backend`, `requested_agents`, `resume` |
| `launch_result` | Result of that attempt; success, failure, or partial team success. `failure_category` `provider_missing` is a CLI that was not found; `provider_unrunnable` is a CLI path set in Settings that cannot be run. A CLI that is installed but signed out is not a launch failure: `prerequisite_check` counts it | Request properties plus `outcome`, `failure_category`, `duration_ms`, `started_agents`, `failed_agents` |
| `dashboard_ready` | A dashboard page completed initialization, loaded/rendered sessions, and observed server startup completion | `page_id` |
| `dashboard_failed` | An observed dashboard initialization or initial-fetch failure | `code` |
| `dashboard_active_day` | An initialized dashboard was visible during a UTC date | — |
| `prompt_submit_requested` | First composer submission intent, covering both HTTP and WebSocket paths | `source` |
| `first_prompt_submitted` | First successful HTTP send accepted by the terminal transport | `source` |
| `task_completed` | A board task completion accepted by the completion route | `outcome` |
| `first_agent_launched` | First individual launch | — |
| `first_team_launched` | First team launch with a successful member | `agent_count` |
| `first_task_completed` | First task marked complete, including a failed outcome | `outcome` |
| `first_task_succeeded` | First task completed with a successful outcome | — |
| `returned_24h` | First tracked process startup more than 24 hours after the first tracked open | — |

Weekly active installs are the distinct `distinct_id`s with `installation_active_week` in a week. `app_opened` alone cannot give that (a Coral that stays up for weeks sends it once, at startup) and `dashboard_active_day` alone cannot either (it needs the dashboard open, so an install driven from the CLI never sends it). `installation_active_week` is deduplicated like `dashboard_active_day`: the week is recorded as sent only after the service accepts the event, under an event UUID that is stable for the install and week, and an opt-out does not use the week up. It carries the standard properties only.

Launch completion does not prove provider authentication, model readiness, or a response. The WebSocket composer currently lacks a transport acknowledgment suitable for the confirmed-send milestone, so `first_prompt_submitted` covers HTTP sends only. `prompt_submit_requested` measures intent, not delivery. No first-response or generic agent-ready event is emitted: existing transcript updates cannot reliably attribute new assistant output to a particular submitted prompt.

`dashboard_ready` counts page loads, not users. Active-day records are deduplicated for the installation and UTC date, including multiple tabs. The legacy `returned_24h` event measures restarts; use dashboard activity for engagement and retention. An observed failure may precede recovery and a ready event. Missing events alone do not prove abandonment or a crash.

Update telemetry (#5952): the update check itself (an anonymous request to GitHub, cached for an hour and shared by the dashboard, the tray and the server) is not analytics and is not affected by the Usage analytics setting; the two update events are analytics and are covered by it: with it off nothing is sent and nothing is remembered. Versions are release numbers by a strict pattern (digits.digits.digits with an optional short suffix such as `-beta`); a development build's version or any other shape is never sent. Nothing about the update (URLs, paths, errors, package names) is sent. An update that happened outside Coral (a package manager, a downloaded installer) shows only as the next `upgrade`, with `from_version`.

## Prerequisite setup observations

`prerequisite_check` reports `tmux`, `claude`, or `codex` with a controlled availability status and check source. Repeated identical observations are deduplicated within a server run. Explicit CLI rechecks use `cli_recheck`; ordinary checks use `cli_check`, and tmux discovery in server status uses `system_status`. Custom binary paths are not recorded in this event.

Claude and Codex also report `signed_in` or `signed_out` (source `launch_probe`), only when the CLI's own non-interactive status command gives a clear answer, and only where Coral already runs that command (the first launch, background jobs, and a person's Check again). Coral reads one fact from the answer, signed in or not, and discards the rest before the event is built; the CLI's output is never logged or sent. A CLI that errors, times out, or answers in a form Coral cannot read reports nothing: a slow start is not a signed-out user. Antigravity and Pi have no such command and report no sign-in status.

For tmux, `available` means discovery found an executable; `missing` means discovery did not find one. Routine discovery is cached for 30 seconds; an explicit recheck bypasses the cache. Claude/Codex checks also report `probe_failed` or `timeout` if the discovered executable's version check fails. Version checks have a three-second deadline and retain at most 8 KiB of stdout. The existing API `found` field stays true when discovery succeeds, even if the probe fails. No executable path, version output, command, or raw error is sent as analytics.

These observations identify missing prerequisites and later availability. Coral currently provides installation instructions and does not execute these installers. A missing executable is not proof that an installation command failed, and an available executable does not prove provider authentication: that is what `signed_in` and `signed_out` add for Claude and Codex. Installer exit codes and failures before Coral starts remain unobserved.

## Properties and identifiers

Every event carries `version`, `edition`, `os`, `arch`, `platform` (the operating system family: `darwin`, `windows`, `linux`, or `wsl2` when Linux runs under WSL2), `schema_version`, `run_id`, and `entrypoint`. The process run ID is random and changes on restart. Entrypoint is a controlled value distinguishing the server, tray, launcher, or unknown entrypoint. Launch attempt IDs correlate requests and outcomes; dashboard page IDs distinguish page loads. These are analytics IDs, not agent session IDs, task IDs, or board identifiers.

### Test sink

For tests only, a `-tags dev` build reads `CORAL_TELEMETRY_TEST_SINK=<url>`: it gives that server a dummy key and sends its events to the local capture endpoint at that URL. The code is in a `//go:build dev` file; `TestDefaultBuildHasNoTestSinkSeam` fails if a default-tags build contains the variable's name, so a release binary cannot be redirected.

Events use a random installation UUID stored in `<coralDir>/.install_id`. This identifies a data directory, not necessarily a person. It is not derived from hardware, hostname, username, or email. Deleting it changes the analytics identity. In **Settings → Usage analytics**, copy the **Install ID** and send it with a deletion request. Viewing or copying it does not create or change the ID, and an existing ID stays available when analytics is off. If no ID has been created, Settings says so.

Event properties are validated against per-event schemas. Provider, backend, outcome, and failure codes use controlled values; counts and durations are bounded and typed. Unknown properties and invalid values are dropped. Unknown event names are not emitted.

## Never collected

Event payloads do not include:

- Your prompts or source code.
- Repository, branch, and file names or paths.
- Agent output or transcripts.
- Raw error messages or executed commands.
- Your name or email address.
- Passwords or API keys.

These statements describe Coral's event payloads: they never carry code, prompts, or work content. The analytics service receives the source IP of incoming events and derives an approximate location from it; IP-derived location is not part of Coral's payloads.

## Controls and delivery

The runtime opt-out, missing build key, and environment override suppress outgoing events. Startup suppresses telemetry when privacy settings cannot be read. Disabling telemetry does not disable local product milestone state used by the UI.

Tracking is asynchronous and must not block a launch or turn a product operation into a failure. Delivery retries are bounded. Retries of the same delivery retain event identity and timestamp; a remote timeout can still make receipt uncertain. Do not assume exactly-once ingestion. See [PostHog event deduplication](https://github.com/PostHog/posthog.com/blob/master/contents/docs/data/events.mdx) for the service's eventual deduplication contract.

One-time product milestones and confirmed analytics delivery are separate. Reaching a milestone while opted out or without a key must not mark it sent. Events suppressed by opt-out are not saved as an outbound replay queue. A later occurrence while enabled can qualify again if analytics delivery has not been confirmed. State is kept in `<coralDir>/.milestones.json`. Pending enabled milestones retain their original timestamp and allowlisted properties for a later trigger to retry; opting out clears these pending snapshots. This is not a background outbox.

Delivery failures are recorded locally in `<coralDir>/tracking-failures.log`, capped at 64 KB. This diagnostic file is not uploaded as analytics.

## Browser endpoint

`POST /api/tracking/event` accepts only browser-observed events: dashboard readiness/activity/failure, and composer submission intent. Server launch/task events and confirmed-send milestones cannot be submitted through this endpoint.

For example:

```json
{
  "event": "dashboard_failed",
  "props": { "code": "sessions_fetch_http" }
}
```

Allowed dashboard failure codes identify sessions/status HTTP, network, or invalid-response failures and initialization failure. They do not contain response bodies, URLs, or exception messages. The endpoint validates properties and applies bounded deduplication/rate limits for dashboard reports. `{ "ok": true }` acknowledges processing, not PostHog receipt.

## Disclosure API

`GET /api/system/telemetry` returns the event descriptions, standard properties, never-collected list, disclosure acknowledgment, and display paths for local telemetry state. For the Operator, it also returns the existing `install_id` and its availability; agent requests do not receive the ID. It separately discloses the analytics service's IP-derived approximate location. Its `enabled` field describes whether the build has a project key; it is not confirmation of network delivery. Read privacy settings and the environment override when determining effective collection.

`POST /api/system/telemetry/acknowledge` records that the disclosure was seen. Acknowledging the disclosure is separate from the usage analytics setting.

## Measurement limits

A failure before the executable starts, such as a missing host loader library, cannot self-report through this instrumentation. A JavaScript bundle that never loads cannot run the dashboard helper. Readiness and response observations should be added only after reliable provider/session correlation exists.

Use schema/version cohorts when comparing historical events. In particular, earlier team counts included attempted members that failed, and historical first task completion events had no outcome. Keep test traffic separate and retain incomplete observation windows when interpreting funnels.

The baseline analysis, dashboard definitions and remaining blind spots are kept in the team's internal notes.
