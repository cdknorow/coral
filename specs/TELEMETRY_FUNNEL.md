# Activation analytics measurement plan

October 4, 2026. Implementation accompanies the instrumentation tasks; event names and supported properties are documented in `coral-go/agent_docs/telemetry.md` and `internal/tracking/events.go`.

## Question

Where do people stop between starting Coral and receiving a useful agent response? Separate observable failures from missing telemetry. An installation ID represents one Coral data directory, not necessarily one person; a downloaded archive is not an installation.

## Baseline evidence

The supplied October 4 CSV contains 125 events from September 28 through October 4 UTC, with 27 distinct installation IDs. It includes event names and timestamps, but no version, OS, architecture, or event-specific properties. No raw IDs or export rows are stored in this repository.

| Observation | Count |
|---|---:|
| Installation IDs with an app start | 27 |
| IDs with an install event in the window | 21 |
| IDs with an individual or team launch | 6 |
| IDs with a first task completion | 2 |
| New IDs with exactly install plus two app starts and no other events | 19 |

Those 19 IDs have app-start gaps of 4.164–7.480 seconds. Automated execution, duplicate startup paths, and a startup/restart problem are hypotheses, not established causes. Current release Linux smoke containers have networking disabled, so those tests cannot explain successful outbound captures from their containers. Other automated execution and historical workflows require separate evidence.

Only two of the 21 IDs first installed during the window have a launch event. Do not label the remainder abandoned users: test traffic, unsupported versions, telemetry opt-out, network failures, partial export coverage, and observation time all affect the denominator. One ID produced 13 of 23 individual launch events; frequency alone cannot identify retries or successful work.

## Measurement sequence

Use separate server and dashboard funnels:

1. Server start to dashboard ready: was a usable UI actually reached?
2. Dashboard ready to launch requested: did the user attempt setup?
3. Launch requested to launch result: did the server create the requested process?
4. Successful launch to confirmed prompt submission: did the user interact?
5. Confirmed prompt to newly observed assistant response: did that interaction receive output?
6. Dashboard active on a later UTC date: did the user return to the product?

A spawned process is not proof of provider authentication, model readiness, or a working conversation. Emit readiness only when a reliable signal exists and name narrower observations precisely. Keep team launches separate from individual launches; team member success counts must exclude member errors. Keep task completion status separate from successful task outcome.

## Dashboard views

### Activation funnel

Count unique eligible installations reaching each stage, and show both the count and percentage. Split by application version, operating system, architecture, and provider where known. Display missing-property cohorts explicitly. Start with a 24-hour conversion window and publish its definition alongside the chart; keep the newest incomplete cohorts separate.

### Launch reliability

Join request/result by random attempt ID and process run ID. Show successful, failed, and missing-result attempts separately. Report controlled failure categories and median/p95 duration. Do not interpret an unmatched request as a confirmed crash. Count team requested, started, and failed members independently.

### Interaction

Only count a newly observed assistant message after a confirmed user send in the same tracked session. Do not count loaded history, automatic welcome content, transport acceptance alone, or a response from another agent. Surface the limitation when observation depends on an open dashboard tab. Multiple tabs must not inflate milestones.

### Return use

Use an active dashboard observation on a UTC day, not a server restart. Define next-day retention as activity on the next UTC date among installs with sufficient follow-up; this is different from the legacy elapsed-24-hours startup milestone. Keep background service uptime out of engagement counts.

### Data quality

Monitor schema-version coverage, request/result matching, missing classifications, duplicates, and suspected automated traffic. Test runners must disable analytics before startup, even when exercising release binaries with a production project key. Source builds without a key remain silent. Opted-out activity is not queued for later replay.

## Privacy and delivery requirements

Use random analytics correlation IDs; do not send product session IDs, task IDs, board names, paths, command text, prompts, output, credentials, or raw errors. Provider and failure categories are controlled enums. Capture durations and counts with bounds. Preserve the runtime opt-out and build-key requirement.

Use bounded nonblocking delivery. Retries retain a stable event identity and stop on opt-out. One-time events should be acknowledged locally only after confirmed delivery; distinguish local product milestone state from analytics delivery state. Do not promise exactly-once receipt when the remote response is uncertain.

## Blind spots

A GLIBC loader failure or crash before Coral starts cannot be reported by Coral's own Go instrumentation. Download clicks/counts, installer validation, and an optional user-provided diagnostic report are separate evidence. A dashboard bundle that fails before the telemetry helper loads also cannot self-report through that helper. Missing events are not evidence of success or a specific error.

The supplied CSV cannot attribute downloads to installations or verify receipt of a newly instrumented event. Validate the implementation using intercepted local collectors and deterministic fixtures; verify production ingestion separately after a release is authorized.
