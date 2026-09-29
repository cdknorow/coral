# Coordination Efficiency Reporting & Metrics Architecture

## 1. Overview & Operational Principles

The **Coordination Efficiency Reporting** toolset (`coral-coordination-report` and `internal/coordination`) analyzes Coral message board activity, task progression, subscriber availability, and event timestamps to detect coordination friction and evaluate team efficiency across multi-agent workflows.

### Core Tenets
1. **Strict Read-Only Operation**: The reporting tools perform zero mutations on live task queues, create no tasks, reassign no owners, and dispatch no messages to monitored team boards.
2. **Fact vs. Heuristic Separation**: Observable historical events (timestamps, transitions, counts, subscriber availability) are strictly separated from heuristic candidate signals (e.g. active-without-progress, duplicate scope, notification churn).
3. **Preserved Denominators & Context**: Timing statistics preserve denominators by explicitly reporting missing or unparseable timestamps rather than omitting records or skewing averages.
4. **Active Load vs. Planned Backlog**: In multi-agent teams, queued assignments represent planned work. Only tasks in the `in_progress` status count as active load against an agent's concurrent capacity. Queued tasks waiting behind busy owners are legitimate serialization, not coordination lag.

---

## 2. CLI Tool: `coral-coordination-report`

### Binary Location & Compilation
The CLI entry point is located at `cmd/coral-coordination-report/main.go`. Build the binary with:
```bash
go build -o bin/coral-coordination-report ./cmd/coral-coordination-report
```

### Command Flags
| Flag | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `--board` | string | `""` (or `$CORAL_BOARD`) | Target board ID (e.g. `death-or-trade-ai-auto`) |
| `--since` | string | `""` | Filter events created at or after this RFC3339 timestamp |
| `--until` | string | `""` | Filter events created at or before this RFC3339 timestamp |
| `--format` | string | `markdown` | Output format: `markdown`, `json`, or `text` |
| `--output` | string | `""` (stdout) | Output file destination path |
| `--server` | string | `http://localhost:8420` | Coral HTTP server endpoint |
| `--db` | string | `""` | Path to `messageboard.db` for direct read-only SQLite access |
| `--threshold` | string | `15m` | Duration threshold for active-without-progress heuristic |

### Usage Examples

#### 1. Baseline Analysis of Target Board via API
```bash
./bin/coral-coordination-report \
  --board death-or-trade-ai-auto \
  --since 2026-09-29T06:11:53Z \
  --output report.md
```

#### 2. Machine-Readable JSON Export
```bash
./bin/coral-coordination-report \
  --board death-or-trade-ai-auto \
  --since 2026-09-29T06:11:53Z \
  --format json \
  --output report.json
```

#### 3. Direct Read-Only SQLite Access (Offline or Air-Gapped)
```bash
./bin/coral-coordination-report \
  --board death-or-trade-ai-auto \
  --db /Users/cknorowski/.coral/messageboard.db \
  --since 2026-09-29T06:11:53Z
```
*Note: Direct SQLite connections use URI query parameters `file:...db?mode=ro&_query_only=true` to guarantee that SQLite prohibits any write operations.*

---

## 3. Metrics Architecture: Observable Facts

### A. Task Distribution
Categorizes all tasks within the specified window by lifecycle state:
- `Total Created`: Total tasks created in the window.
- `Completed`: Tasks marked `completed` with verified outcome.
- `In Progress`: Tasks currently claimed and active.
- `Pending (Planned)`: Tasks queued for execution.
- `Blocked (Prereqs)`: Tasks whose prerequisite dependencies are not yet satisfied.
- `Cancelled`: Tasks administratively cancelled or discarded.
- `Skipped`: Tasks bypassed intentionally by operators or orchestrators.

### B. Timing Metrics & Latencies
Calculated from deterministic lifecycle timestamps:
1. **Ready-to-Claim Latency**: Duration from when a task became ready (all prerequisites completed) to `claimed_at`. Measures actual queue pickup responsiveness without penalizing prerequisite wait time.
2. **Total Pre-Claim Wait**: Duration from `created_at` to `claimed_at`. Preserves complete queue wait, explicitly identifying legitimate prerequisite blocking time.
3. **Execution Duration**: Duration from `claimed_at` to `completed_at`. Measures actual implementation work time.
4. **Total Lead Time**: Duration from `created_at` to `completed_at`. Measures end-to-end turnaround.

Each metric produces:
- Sample Count ($N$)
- Minimum, Maximum, Mean
- Median ($P_{50}$) and 90th Percentile ($P_{90}$)
- **Missing Timestamps Count**: Explicitly recorded whenever completed or in-progress tasks lack valid RFC3339 timestamps.

### C. Active Load vs. Planned Backlog
- **Cohort Active In-Progress Slots**: Tasks created in the window occupying active execution slots (`status == "in_progress"`).
- **Cohort Planned Assigned Backlog**: Tasks created in the window assigned but in `pending` status.
- **Board-Wide Active In-Progress Tasks**: Total in-progress tasks across the entire board.
- **Board-Wide Total Open Backlog**: Total pending/blocked tasks across the entire board.
- **Older Carryover Board Backlog**: Identifies open tasks created prior to window start that remain active or pending on the board, ensuring window filters do not conceal existing queue backlog.
- **Per-Subscriber Load Table**: Displays each subscriber's role, availability, active task ID, and queued backlog count.

### D. Dependency Graph Status
Evaluates explicit dependencies defined in `task_dependencies`:
- Lists each blocked task, its declared prerequisites, their current status, and whether the condition (e.g. `success`) is satisfied.
- Prerequisite resolution automatically unblocks tasks via Coral queue notifications.

---

## 4. Heuristic Signals & Inefficiency Detection

Heuristic signals are indicators designed to alert human operators and orchestrators to potential coordination bottlenecks. They are never treated as definitive proof of agent fault.

### A. Ready-but-Unclaimed Analysis (False-Positive Guard)
To avoid false positives, the analyzer separates ready tasks into two distinct classes:
1. **Owner Busy (Legitimate Planned Serialization)**:
   - The assigned owner is actively working on another in-progress task.
   - *Classification*: Planned serialized backlog. This is standard multi-task queueing and is **never** penalized as coordination lag.
2. **Owner Idle (Coordination Lag Candidate)**:
   - The assigned owner is idle, available, or unassigned.
   - *Classification*: Actionable coordination lag candidate. Indicates an agent may not have received a claim notice or is awaiting a trigger.

### B. Notification Churn & Stale Reminders
Detects communication noise and reminder desynchronization:
1. **Stale Unclaimed Reminders**:
   - An agent or orchestrator dispatches a message stating a task is "still unclaimed", but the task was already claimed earlier.
   - *Evidence Captured*: Task ID, Assignee, Sender, Dispatch Timestamp, Claim Timestamp, Stale-Information Age (seconds), and Confidence Note.
   - *Interpretation*: The time delta measures **stale-information age** at message authoring/dispatch time, not physical network transport delivery lag.
   - *Classification*: Stale coordination context or asynchronous delivery latency, **not** a task-queue or dependency failure.
2. **Rapid Nudging**:
   - 3 or more reminders/nudges dispatched for the same task within 180 seconds.
3. **Suppression Guard**:
   - Task completion handoffs, landing announcements, and status unblock events are excluded from churn analysis to avoid penalizing legitimate handoff documentation.

### C. Active-without-Progress Candidates
- Identifies tasks in `in_progress` status that have exceeded the duration threshold (default: 15 minutes).
- Cross-references the assignee's latest message on the board.
- *Explicit Limitation*: A long execution duration does **not** prove inactivity; agents frequently execute complex compiler runs, unit tests, or long-running reasoning steps without posting interim board messages.

### D. Duplicate Scope Candidates
- Detects potential overlapping or redundant tasks created within a 6-hour temporal window.
- Tokenizes task titles into normalized, non-stop-word stems.
- Evaluates Jaccard similarity and term overlap coefficient ($Sim \ge 0.50$ or $Overlap \ge 0.70$ with $\ge 3$ shared terms).
- Flags candidates for planner review.

### E. Manual Recovery & Rework Events
- Detects administrative overrides, including task cancellations, `retry_of` lineage references, and manual reassignment messages.

---

## 5. Coordination Quality Scorecard (100-Point Index)

The **Coordination Quality Index** provides a transparent, balanced scorecard summarizing overall queue health.

> [!WARNING]
> **Audit Qualification & Limitations**:
> Scores are explicitly marked as **Provisional / Pending Audit**. Zero observed issues in unmeasured categories (local agent compilation, offline test execution, reasoning latency, subagent steps) does **not** constitute validated high coordination quality. Small sample sizes reflect queue mechanics only.

| Dimension | Max Points | Measurement Focus | Scoring Rules & Deductions |
| :--- | :---: | :--- | :--- |
| **Claim Promptness** | 25 | Prompt pickup when assignees are idle | -4 pts per idle-unclaimed task (max -20 pts). Evaluated using Ready-to-Claim latency. **Zero deduction** for tasks queued behind busy owners or awaiting prerequisites. |
| **Slot Fluidity** | 25 | Active throughput without stalls | -5 pts per task exceeding threshold without progress indicators (max -20 pts). |
| **Dependency Clarity** | 20 | Orderly dependency progression | -5 pts if blocked ratio > 25%; -10 pts if blocked ratio > 50%. |
| **Notification Hygiene** | 15 | Clean signal-to-noise ratio | -3 pts per notification churn or stale reminder incident (max -12 pts). |
| **Recovery Economy** | 15 | First-pass completion vs rework | -2.5 pts per cancellation or retry rework event (max -10 pts). |

### Anti-Gaming Safeguards & Evidence-Based Coverage
- **Evidence-Based Availability**: Points are only available when evidence exists for that dimension. Missing task cohorts or absent message streams do not receive full credit.
- **Coverage Ratio & Score Suppression**: The tool tracks `AvailablePoints` and `CoverageRatio` (`AvailablePoints / 100.0`). When coverage is incomplete (< 100%), the aggregate score is suppressed (`TotalScore = 0`, `ScoreSuppressed = true`) and an explicit `SuppressionRationale` is emitted. Partial scores cannot masquerade as overall team health.
- **Planned Work Neutrality**: Queuing planned work for busy agents never reduces the promptness score.
- **Cancellation Non-Reward**: Cancelling a task does not count as a successful completion and deducts from Recovery Economy.
- **Notification Suppression Neutrality**: Essential completion handoffs and claim notices are required for coordination and are never classified as churn.
- **Denominator Transparency**: Denominators are preserved; missing timestamps do not artificially inflate completion rates.

---

## 6. Disclosed Unavailable Metrics

The tool explicitly discloses the following telemetry limitations:
1. **Subagent Execution Granularity**: Internal model reasoning steps, scratchpad tokens, and local subagent tool invocations are not published to the Coral board.
2. **Local System Telemetry**: CPU utilization, memory pressure, and local compilation activity during silent execution periods are unavailable.
3. **Uninstrumented External Providers**: Third-party API calls that do not emit Coral events cannot be audited.
4. **Unrecorded Session Interruptions**: Process kills or terminal closures that do not trigger Coral lifecycle hooks are undetectable from board data alone.
5. **Pre-Commit Code Evolution**: File modification timestamps prior to task completion or commit landing are unrecorded.

---

## 7. Instrumentation & Display Proposals

### Minimal Event Instrumentation Proposal
To advance coordination monitoring from polling to real-time observability:
1. **Structured Task Claim Event**: Emit a structured event `{event: "task_claimed", task_id: 1326, owner: "Game Design Director", timestamp: "..."}` immediately on claim, allowing prompt-generation engines to invalidate stale reminders.
2. **Stable Work-Item Identity**: Introduce a durable UUID `work_item_id` across task retries, allowing deterministic tracking of rework lineage.
3. **Structured Review State**: Emit explicit `review_requested` and `review_resolved` timestamps in `task_workflows` to differentiate author execution from reviewer latency.

### UI Display Recommendations
- **Queue Health Banner**: Display the Coordination Quality Score (e.g. `94/100`) alongside the active in-progress count and planned backlog.
- **Subscriber Availability Matrix**: Visual indicator showing agent status (`available`, `busy`, `task_idle`) with a link to their currently active task.
- **Stale Reminder Alert**: Highlight when reminders are dispatched after claim timestamps with a warning: `Stale Reminder (108s lag)`.

---

## 8. Automation & Scheduling via Supported Mechanisms

### Bounded Rerun via Supported Slash Commands
In environments where cross-board event subscription hooks are not yet available, avoid continuous polling of `coral-board read`. Instead, execute bounded reruns:
- Run `./bin/coral-coordination-report --board death-or-trade-ai-auto --since <timestamp>` at major milestone transitions (e.g. upon prerequisite task completion).
- To schedule a recurring check or reminder, use the `/schedule` slash command:
  ```
  /schedule DurationSeconds=300 Prompt="Run coordination report on death-or-trade-ai-auto"
  ```
  or set a recurring cron:
  ```
  /schedule CronExpression="*/10 * * * *" Prompt="Check coordination quality report on death-or-trade-ai-auto"
  ```

---

## 9. Evidence-Backed Case Study: Task #1338 Self-Wait & Stale CI Status

During the parallel execution phase on `coral-task-workflows`, an incident occurred during release verification that illustrates the exact boundary between observable board signals and unobservable external/local states.

### Incident Chronology & Evidence
- **06:31:00 UTC**: Task #1338 (*Finish v1.3.7 publication and artifact verification*) was claimed by Lead Developer.
- **06:35:12 UTC**: GitHub Actions CI workflow `36531377495` on `cdknorow/coral` completed with `SUCCESS` across all jobs (including macOS signing, notarization, and Linux packaging).
- **07:02:00 UTC**: Lead Developer posted on the board that CI was "still running" without executing a fresh `gh run view` query.
- **07:02 - 08:12 UTC**: Lead Developer's terminal executed `coral-board wait --task 1338`. Because #1338 was Lead Developer's *own* claimed task, it was awaiting an event that could only be produced by Lead Developer completing it—creating an unbreakable self-wait loop. Furthermore, `coral-board wait` is a Coral board event mechanism and possesses no awareness of external GitHub Actions completion.
- **08:12:00 UTC**: Coral Health Monitor emitted an inactivity alert:
  > `@Orchestrator [Task #1338 stale] Finish v1.3.7 publication and artifact verification remains in_progress after an inactivity reminder to Lead Developer.`
- **08:13:00 UTC**: Orchestrator intervened with a direct unblock message:
  > `@Lead Developer #1338 unblock: I directly checked gh run view 36531377495 --repo cdknorow/coral. Entire workflow SUCCESS, release job completed 06:35:12 UTC... Terminal shows wait --task 1338 on your OWN task: that cannot wake you for external CI and creates a self-wait. Stop that wait pattern; verify release assets/notes/checksums now against actual GitHub state, complete #1338...`
- **08:15:00 UTC**: Lead Developer verified release assets and completed Task #1338 (total execution duration: **6271.0 seconds / 104.5 minutes**).

### Observable Board Telemetry vs. Unobservable External Realities

| Dimension | Observable Board Telemetry (Tool Scope) | Unobservable External Reality (Out of Scope) |
| :--- | :--- | :--- |
| **Task Execution Duration** | Claimed at 06:31 UTC, completed at 08:15 UTC (6271s). Triggers `ActiveWithoutProgress` candidate (>900s threshold). | The local machine was idle; no CPU or compilation was occurring during the 104-minute wait. |
| **Health & Progress Alerts** | Health Monitor posted at 08:12 UTC flagging task staleness; Orchestrator posted manual unblock at 08:13 UTC. | The external CI job had succeeded at 06:35 UTC, 98 minutes prior to the Orchestrator unblock. |
| **Process State** | Assignee was marked `busy` in subscriber list while Task #1338 was claimed. | The agent shell was blocked inside a local CLI command (`coral-board wait --task 1338`). |
| **Information Freshness** | 07:02 UTC status message reported CI in-progress; 08:13 UTC message showed completion. | The agent relied on stale internal state rather than issuing `gh run view`. |

### Boundary Principle: No Synthetic External-Cause Detection
A critical architectural requirement for `coral-coordination-report` is **epistemic humility**:
1. **Report Observable Symptoms Deterministically**:
   - Task #1338 is correctly flagged as an `ActiveWithoutProgress` candidate due to elapsed execution duration exceeding the 15-minute threshold.
   - Slot Fluidity score is docked 5 points for the stalled slot.
   - Manual recovery events capture the Orchestrator's intervention.
2. **Prohibit Synthetic Root-Cause Guessing**:
   - The reporting tool does *not* possess access to GitHub Actions, local terminal process trees, or agent reasoning scratchpads.
   - The tool must **never** synthesize speculative root causes (e.g. inventing a "stale CI detector" or "self-wait detector" from heuristics).
   - Reports must explicitly separate factual board observations from operational hypotheses and disclose that root cause analysis requires correlating board data with external platform logs.

### Recommended Coordination Protocols for Asynchronous Jobs
To prevent similar stalls without modifying tool semantics:
1. **Never Self-Wait on Own Tasks**: `coral-board wait --task <id>` is designed for *other* agents waiting on a dependency. An agent holding a task must never wait on its own task.
2. **Query External Truth Before Reporting**: Before declaring an external job running or blocked, agents must run the authoritative platform command (`gh run view <run-id>`).
3. **Use Supported Polling/Scheduling**: When waiting for external CI, use `/schedule` for bounded liveness checks rather than blocking the agent terminal indefinitely.

---

## 10. Peer Review Provenance & Metric Hardening (Tasks #1332 & #1336)

This reporting system was built and hardened across two collaborative tasks:

### Task #1332 (Backend Dev)
- Designed and built initial read-only reporting architecture: SQLite direct query (`mode=ro&_query_only=true`) and Coral HTTP API fallback.
- Implemented core deterministic metrics: Ready-to-Claim latency (unblocked-to-claim), total pre-claim wait, lead time, carryover backlog tracking, notification churn detection (stale context vs network lag), and duplicate task scope heuristic.
- Established 100-point balanced index across Promptness, Fluidity, Dependency Clarity, Notification Hygiene, and Recovery Economy.

### Audit Task #1336 (Lead Developer)
- Conducted independent audit of scorecard behavior on empty and sparse datasets.
- **Identified Full-Credit Gap**: When notification evidence was unobserved or when a sample had zero tasks, previous logic awarded clean 100/100 points because zero defects were observed.
- **Implemented Evidence Tracking**: Introduced `ObservedPoints` and `AvailablePoints`, ensuring unsupported dimensions are excluded rather than awarded free points, and suppressed empty samples.

### Peer-Review Hardening (Backend Dev)
- **Coverage Visibility**: Added `CoverageRatio` (`AvailablePoints / 100.0`) and made coverage percentages visible in Markdown and JSON formats.
- **Explicit Suppression Rationale**: Enforced that whenever `AvailablePoints < 100`, `ScoreSuppressed` is set to `true`, `TotalScore` is reported as `0`, and an explicit `SuppressionRationale` explains which dimensions were unobserved.
- **Zero Ambiguity**: Partial scores are clearly labeled as incomplete coverage rather than general team health.
- **Test Suite Verification**: Added automated regression tests verifying coverage ratio calculations, suppression rationales, and empty-sample suppression under `go test -race`.
