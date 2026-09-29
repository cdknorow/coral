package coordination

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// FormatJSON produces pretty-printed JSON.
func FormatJSON(r Report) (string, error) {
	bytes, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// FormatMarkdown produces high-density GitHub-flavored Markdown.
func FormatMarkdown(r Report) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# Coordination Efficiency Report: `%s`\n\n", r.Metadata.Board))
	b.WriteString(fmt.Sprintf("- **Generated At**: %s\n", r.Metadata.GeneratedAt.UTC().Format(time.RFC3339)))
	if r.Metadata.WindowStart != nil {
		b.WriteString(fmt.Sprintf("- **Window Start**: %s\n", r.Metadata.WindowStart.UTC().Format(time.RFC3339)))
	}
	if r.Metadata.WindowEnd != nil {
		b.WriteString(fmt.Sprintf("- **Window End**: %s\n", r.Metadata.WindowEnd.UTC().Format(time.RFC3339)))
	}
	b.WriteString(fmt.Sprintf("- **Tasks In Window**: %d\n", r.Metadata.TotalTasksInWindow))
	b.WriteString(fmt.Sprintf("- **Messages In Window**: %d\n", r.Metadata.TotalMessagesInWindow))
	b.WriteString(fmt.Sprintf("- **Subscribers Tracked**: %d\n\n", r.Metadata.SubscribersTracked))

	// Coordination Scorecard
	b.WriteString("## 1. Coordination Quality Scorecard\n\n")
	statusNote := ""
	if r.CoordinationScore.ValidationStatus != "" {
		statusNote = fmt.Sprintf(" *(%s)*", r.CoordinationScore.ValidationStatus)
	}
	coveragePct := r.CoordinationScore.CoverageRatio * 100
	if r.CoordinationScore.ScoreSuppressed {
		b.WriteString(fmt.Sprintf("> **Overall Coordination Quality Index: suppressed** (Coverage: %.1f%% — %.1f of 100 points observed)%s\n", coveragePct, r.CoordinationScore.ObservedPoints, statusNote))
		if r.CoordinationScore.SuppressionRationale != "" {
			b.WriteString(fmt.Sprintf(">\n> **Suppression Rationale**: %s\n\n", r.CoordinationScore.SuppressionRationale))
		} else {
			b.WriteString("\n")
		}
	} else {
		b.WriteString(fmt.Sprintf("> **Overall Coordination Quality Index: %.1f / 100** (Coverage: %.1f%% — %.1f points available)%s\n\n", r.CoordinationScore.TotalScore, coveragePct, r.CoordinationScore.AvailablePoints, statusNote))
	}
	if r.CoordinationScore.AuditCaveat != "" {
		b.WriteString("> [!WARNING]\n")
		b.WriteString(fmt.Sprintf("> **Audit Caveat**: %s\n\n", r.CoordinationScore.AuditCaveat))
	}
	b.WriteString("| Component Dimension | Score | Max | Focus Area |\n")
	b.WriteString("| :--- | :---: | :---: | :--- |\n")
	b.WriteString(fmt.Sprintf("| Claim Promptness | **%.1f** | 25 | Pickup latency when assignees are idle/available |\n", r.CoordinationScore.ClaimPromptnessScore))
	b.WriteString(fmt.Sprintf("| Slot Fluidity | **%.1f** | 25 | Active task throughput without prolonged stalled slots |\n", r.CoordinationScore.SlotFluidityScore))
	b.WriteString(fmt.Sprintf("| Dependency Clarity | **%.1f** | 20 | Orderly progression through prerequisite graphs |\n", r.CoordinationScore.DependencyClarityScore))
	b.WriteString(fmt.Sprintf("| Notification Hygiene | **%.1f** | 15 | Signal-to-noise ratio, absence of churn/spurious nudges |\n", r.CoordinationScore.NotificationCleanScore))
	b.WriteString(fmt.Sprintf("| Recovery Economy | **%.1f** | 15 | First-pass completion vs cancellation/rework burden |\n\n", r.CoordinationScore.RecoveryEconomyScore))
	b.WriteString(fmt.Sprintf("*Methodology*: %s\n\n", r.CoordinationScore.ScoringMethodology))

	// Observable Facts
	b.WriteString("## 2. Observable Facts\n\n")
	b.WriteString("### A. Task Distribution\n\n")
	b.WriteString("| Total Created | Completed | In Progress | Pending (Planned) | Blocked (Prereqs) | Cancelled | Skipped |\n")
	b.WriteString("| :---: | :---: | :---: | :---: | :---: | :---: | :---: |\n")
	b.WriteString(fmt.Sprintf("| %d | %d | %d | %d | %d | %d | %d |\n\n",
		r.ObservableFacts.TaskCounts.TotalCreated,
		r.ObservableFacts.TaskCounts.Completed,
		r.ObservableFacts.TaskCounts.InProgress,
		r.ObservableFacts.TaskCounts.Pending,
		r.ObservableFacts.TaskCounts.Blocked,
		r.ObservableFacts.TaskCounts.Cancelled,
		r.ObservableFacts.TaskCounts.Skipped,
	))

	b.WriteString("### B. Latencies & Durations\n\n")
	b.WriteString("| Metric | Count | Min | Median | Mean | P90 | Max |\n")
	b.WriteString("| :--- | :---: | :---: | :---: | :---: | :---: | :---: |\n")
	formatDurationRow(&b, "Ready-to-Claim Latency (Unblocked → Claim)", r.ObservableFacts.Durations.ReadyToClaimLatency)
	formatDurationRow(&b, "Total Pre-Claim Wait (Created → Claim, includes blocking)", r.ObservableFacts.Durations.TotalPreClaimWait)
	formatDurationRow(&b, "Execution Duration (Claim → Complete)", r.ObservableFacts.Durations.ExecutionDuration)
	formatDurationRow(&b, "Total Lead Time (Create → Complete)", r.ObservableFacts.Durations.TotalLeadTime)
	b.WriteString(fmt.Sprintf("\n*Missing or unparseable timestamps preserved in denominator*: %d\n\n", r.ObservableFacts.Durations.MissingTimestamps))

	b.WriteString("### C. Active Load vs. Planned Backlog\n\n")
	b.WriteString(fmt.Sprintf("- **Cohort Active In-Progress Slots**: %d (consuming agent capacity in this window)\n", r.ObservableFacts.ActiveVsPlannedLoad.ActiveInProgressTasks))
	b.WriteString(fmt.Sprintf("- **Cohort Planned Assigned Backlog**: %d (queued work created in this window)\n", r.ObservableFacts.ActiveVsPlannedLoad.PlannedAssignedTasks))
	b.WriteString(fmt.Sprintf("- **Board-Wide Active In-Progress Tasks**: %d (total across entire board)\n", r.ObservableFacts.ActiveVsPlannedLoad.BoardWideActiveTasks))
	b.WriteString(fmt.Sprintf("- **Board-Wide Total Open Backlog**: %d (total pending/blocked across entire board)\n", r.ObservableFacts.ActiveVsPlannedLoad.BoardWidePendingTasks))
	b.WriteString(fmt.Sprintf("- **Unassigned Pending Tasks**: %d\n\n", r.ObservableFacts.ActiveVsPlannedLoad.UnassignedPendingTasks))

	if len(r.ObservableFacts.ActiveVsPlannedLoad.CarryoverOlderTasks) > 0 {
		b.WriteString("#### Older Carryover Board Backlog (Created Prior to Window)\n")
		b.WriteString("*Open tasks created before window start that remain active or pending on the board (preserved to avoid concealing existing backlog):*\n\n")
		b.WriteString("| Task ID | Title | Status | Assignee | Created At |\n")
		b.WriteString("| :---: | :--- | :---: | :--- | :--- |\n")
		for _, co := range r.ObservableFacts.ActiveVsPlannedLoad.CarryoverOlderTasks {
			b.WriteString(fmt.Sprintf("| #%d | %s | `%s` | %s | %s |\n", co.ID, co.Title, co.Status, co.Assignee, co.CreatedAt))
		}
		b.WriteString("\n")
	}

	if len(r.ObservableFacts.ActiveVsPlannedLoad.SubscriberLoad) > 0 {
		b.WriteString("| Subscriber | Role | Availability | Active Task | Queued Backlog |\n")
		b.WriteString("| :--- | :--- | :--- | :---: | :---: |\n")
		for _, s := range r.ObservableFacts.ActiveVsPlannedLoad.SubscriberLoad {
			activeStr := "—"
			if s.ActiveTaskID != 0 {
				activeStr = fmt.Sprintf("#%d", s.ActiveTaskID)
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %d |\n", s.SubscriberID, s.Role, s.Availability, activeStr, s.QueuedCount))
		}
		b.WriteString("\n")
	}

	if len(r.ObservableFacts.DependencyGraph.BlockedTasks) > 0 {
		b.WriteString("### D. Blocked Tasks & Dependencies\n\n")
		b.WriteString("| Task ID | Title | Assignee | Unmet Prerequisites |\n")
		b.WriteString("| :---: | :--- | :--- | :--- |\n")
		for _, bTask := range r.ObservableFacts.DependencyGraph.BlockedTasks {
			var prereqStrs []string
			for _, dep := range bTask.Dependencies {
				statusIcon := "⏳"
				if dep.Satisfied {
					statusIcon = "✅"
				}
				prereqStrs = append(prereqStrs, fmt.Sprintf("#%d (%s %s)", dep.PrerequisiteID, dep.CurrentStatus, statusIcon))
			}
			b.WriteString(fmt.Sprintf("| #%d | %s | %s | %s |\n", bTask.TaskID, bTask.Title, bTask.Assignee, strings.Join(prereqStrs, ", ")))
		}
		b.WriteString("\n")
	}

	// Heuristic Signals
	b.WriteString("## 3. Heuristic Signals & Inefficiency Candidates\n\n")
	b.WriteString("> [!NOTE]\n")
	b.WriteString("> The signals below are automated candidates based on operational patterns. They highlight areas for planner attention rather than definitive defects.\n\n")

	// Ready Unclaimed
	b.WriteString("### A. Ready-but-Unclaimed Analysis\n\n")
	b.WriteString(fmt.Sprintf("- **Legitimate Planned Serialization (Owner Busy)**: %d tasks\n", r.HeuristicSignals.ReadyUnclaimed.OwnerBusyCount))
	b.WriteString("  *Tasks waiting because their assigned owner is actively working on another task. This is proper queue serialization, NOT inefficiency.*\n")
	for _, t := range r.HeuristicSignals.ReadyUnclaimed.OwnerBusyTasks {
		b.WriteString(fmt.Sprintf("  - Task #%d: `%s` (Assignee `%s` active on #%d; waiting %.0fs)\n", t.TaskID, t.Title, t.Assignee, t.OwnerActiveID, t.AgeSeconds))
	}
	b.WriteString(fmt.Sprintf("\n- **Potential Coordination Lag (Owner Idle)**: %d tasks\n", r.HeuristicSignals.ReadyUnclaimed.OwnerIdleCount))
	b.WriteString("  *Tasks ready to claim where the assignee is currently idle or available. Good candidate for explicit claim nudging.*\n")
	for _, t := range r.HeuristicSignals.ReadyUnclaimed.OwnerIdleTasks {
		b.WriteString(fmt.Sprintf("  - Task #%d: `%s` (Assignee `%s` idle; waiting %.0fs)\n", t.TaskID, t.Title, t.Assignee, t.AgeSeconds))
	}
	b.WriteString(fmt.Sprintf("\n- **Unassigned Ready Work**: %d tasks\n\n", r.HeuristicSignals.ReadyUnclaimed.UnassignedCount))

	// Notification Churn
	b.WriteString("### B. Notification Churn & Reminder Churn\n\n")
	if len(r.HeuristicSignals.NotificationChurn) == 0 {
		b.WriteString("No rapid notification churn or spurious reminders detected.\n\n")
	} else {
		b.WriteString("| Task ID | Assignee | Churn Type | Sender | Stale Context Age | Confidence & Analysis | Sample Message |\n")
		b.WriteString("| :---: | :--- | :--- | :--- | :---: | :--- | :--- |\n")
		for _, inc := range r.HeuristicSignals.NotificationChurn {
			sender := inc.Sender
			if sender == "" {
				sender = "—"
			}
			conf := inc.Confidence
			if conf == "" {
				conf = "Heuristic"
			}
			age := inc.TimeSpanSec
			if inc.StaleInformationAgeSec > 0 {
				age = inc.StaleInformationAgeSec
			}
			b.WriteString(fmt.Sprintf("| #%d | %s | `%s` | %s | %.0fs | %s | `%s` |\n",
				inc.TaskID, inc.Assignee, inc.ChurnType, sender, age, conf, truncate(inc.SampleMessage, 65)))
		}
		b.WriteString("\n")
	}

	// Active without progress
	b.WriteString("### C. Active-without-Progress Candidates\n\n")
	if len(r.HeuristicSignals.ActiveWithoutProgress) == 0 {
		b.WriteString("No active tasks exceeding the duration threshold without recent activity.\n\n")
	} else {
		b.WriteString("| Task ID | Title | Assignee | Claimed At | Duration | Notes |\n")
		b.WriteString("| :---: | :--- | :--- | :--- | :---: | :--- |\n")
		for _, act := range r.HeuristicSignals.ActiveWithoutProgress {
			b.WriteString(fmt.Sprintf("| #%d | %s | %s | %s | %.0fs | %s |\n",
				act.TaskID, act.Title, act.Assignee, act.ClaimedAt.UTC().Format("15:04:05"), act.DurationSeconds, act.ConfidenceNote))
		}
		b.WriteString("\n")
	}

	// Duplicate scope
	if len(r.HeuristicSignals.DuplicateScopeCandidates) > 0 {
		b.WriteString("### D. Duplicate or Overlapping Scope Candidates\n\n")
		b.WriteString("| Task A | Task B | Similarity | Shared Terms |\n")
		b.WriteString("| :--- | :--- | :---: | :--- |\n")
		for _, dup := range r.HeuristicSignals.DuplicateScopeCandidates {
			b.WriteString(fmt.Sprintf("| #%d: %s | #%d: %s | %.0f%% | %s |\n",
				dup.TaskID1, dup.Title1, dup.TaskID2, dup.Title2, dup.Similarity*100, strings.Join(dup.SharedWords, ", ")))
		}
		b.WriteString("\n")
	}

	// Manual Recovery
	if len(r.HeuristicSignals.ManualRecoveryEvents) > 0 {
		b.WriteString("### E. Manual Recovery & Rework Events\n\n")
		b.WriteString("| Task ID | Event Type | Actor | Reason / Details |\n")
		b.WriteString("| :---: | :--- | :--- | :--- |\n")
		for _, rec := range r.HeuristicSignals.ManualRecoveryEvents {
			b.WriteString(fmt.Sprintf("| #%d | `%s` | %s | %s |\n", rec.TaskID, rec.EventType, rec.Actor, truncate(rec.Reason, 80)))
		}
		b.WriteString("\n")
	}

	// Unavailable Metrics
	b.WriteString("## 4. Disclosed Unavailable Metrics\n\n")
	b.WriteString("The following dimensions cannot be derived from messageboard data alone and are excluded to avoid synthetic/causal speculation:\n")
	for _, un := range r.UnavailableMetrics {
		b.WriteString(fmt.Sprintf("- %s\n", un))
	}
	b.WriteString("\n")

	// Key Findings
	b.WriteString("## 5. Key Findings\n\n")
	for _, f := range r.KeyFindings {
		b.WriteString(fmt.Sprintf("- %s\n", f))
	}
	b.WriteString("\n")

	// Recommendations
	b.WriteString("## 6. Actionable Recommendations\n\n")
	for _, rec := range r.Recommendations {
		b.WriteString(fmt.Sprintf("- %s\n", rec))
	}
	b.WriteString("\n")

	return b.String()
}

func formatDurationRow(b *strings.Builder, name string, s DurationStats) {
	if s.Count == 0 {
		b.WriteString(fmt.Sprintf("| %s | 0 | — | — | — | — | — |\n", name))
		return
	}
	b.WriteString(fmt.Sprintf("| %s | %d | %.1fs | %.1fs | %.1fs | %.1fs | %.1fs |\n",
		name, s.Count, s.MinSec, s.MedSec, s.MeanSec, s.P90Sec, s.MaxSec))
}

func truncate(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max-1]) + "…"
	}
	return s
}
