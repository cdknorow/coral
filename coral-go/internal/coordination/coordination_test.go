package coordination

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string {
	return &s
}

func TestTimingStats_CalculationAndDenominator(t *testing.T) {
	t0, _ := time.Parse(time.RFC3339, "2026-09-29T06:00:00Z")

	// Task 1: Complete timestamps (claim latency 10s, execution duration 50s, lead time 60s)
	// Task 2: Complete timestamps (claim latency 20s, execution duration 40s, lead time 60s)
	// Task 3: Missing ClaimedAt (claim latency missing, execution duration missing, lead time 120s)
	// Task 4: Missing CompletedAt (in_progress)
	tasks := []Task{
		{
			ID:          1,
			Title:       "Task 1",
			Status:      "completed",
			CreatedAt:   t0.Format(time.RFC3339),
			ClaimedAt:   strPtr(t0.Add(10 * time.Second).Format(time.RFC3339)),
			CompletedAt: strPtr(t0.Add(60 * time.Second).Format(time.RFC3339)),
		},
		{
			ID:          2,
			Title:       "Task 2",
			Status:      "completed",
			CreatedAt:   t0.Format(time.RFC3339),
			ClaimedAt:   strPtr(t0.Add(20 * time.Second).Format(time.RFC3339)),
			CompletedAt: strPtr(t0.Add(60 * time.Second).Format(time.RFC3339)),
		},
		{
			ID:          3,
			Title:       "Task 3",
			Status:      "completed",
			CreatedAt:   t0.Format(time.RFC3339),
			ClaimedAt:   nil, // Missing ClaimedAt
			CompletedAt: strPtr(t0.Add(120 * time.Second).Format(time.RFC3339)),
		},
		{
			ID:          4,
			Title:       "Task 4",
			Status:      "in_progress",
			CreatedAt:   t0.Format(time.RFC3339),
			ClaimedAt:   strPtr(t0.Add(5 * time.Second).Format(time.RFC3339)),
			CompletedAt: nil, // Still running
		},
		{
			ID:        5,
			Title:     "Task 5",
			Status:    "pending",
			CreatedAt: "invalid-time", // Missing/unparseable CreatedAt
		},
	}

	report := Analyze(tasks, nil, nil, ReportFilter{}, t0.Add(200*time.Second))

	// Verify task counts
	assert.Equal(t, 5, report.ObservableFacts.TaskCounts.TotalCreated)
	assert.Equal(t, 3, report.ObservableFacts.TaskCounts.Completed)
	assert.Equal(t, 1, report.ObservableFacts.TaskCounts.InProgress)
	assert.Equal(t, 1, report.ObservableFacts.TaskCounts.Pending)

	// Verify durations
	claimLatency := report.ObservableFacts.Durations.ClaimLatency
	assert.Equal(t, 3, claimLatency.Count) // Tasks 1, 2, 4 had ClaimedAt
	assert.Equal(t, 5.0, claimLatency.MinSec)
	assert.Equal(t, 20.0, claimLatency.MaxSec)
	assert.Equal(t, 10.0, claimLatency.MedSec)

	execDuration := report.ObservableFacts.Durations.ExecutionDuration
	assert.Equal(t, 2, execDuration.Count) // Only Tasks 1 and 2 had both ClaimedAt & CompletedAt
	assert.Equal(t, 40.0, execDuration.MinSec)
	assert.Equal(t, 50.0, execDuration.MaxSec)

	leadTime := report.ObservableFacts.Durations.TotalLeadTime
	assert.Equal(t, 3, leadTime.Count) // Tasks 1, 2, 3 were completed

	// Denominator check: Task 3 had missing ClaimedAt, Task 4 had missing CompletedAt
	assert.Equal(t, 2, report.ObservableFacts.Durations.MissingTimestamps)
}

func TestActiveVsPlannedLoad_FalsePositiveAvoidance(t *testing.T) {
	t0, _ := time.Parse(time.RFC3339, "2026-09-29T06:00:00Z")

	// Frontend Dev has 1 in-progress task and 1 pending queued task.
	// Backend Dev is idle and has 1 pending queued task.
	tasks := []Task{
		{
			ID:         101,
			Title:      "Frontend in progress",
			Status:     "in_progress",
			AssignedTo: strPtr("Frontend Dev"),
			CreatedAt:  t0.Format(time.RFC3339),
			ClaimedAt:  strPtr(t0.Add(10 * time.Second).Format(time.RFC3339)),
		},
		{
			ID:         102,
			Title:      "Frontend planned next",
			Status:     "pending",
			AssignedTo: strPtr("Frontend Dev"),
			CreatedAt:  t0.Format(time.RFC3339),
		},
		{
			ID:         103,
			Title:      "Backend ready task",
			Status:     "pending",
			AssignedTo: strPtr("Backend Dev"),
			CreatedAt:  t0.Format(time.RFC3339),
		},
	}

	status := &BoardStatus{
		Board: "test-board",
		Agents: []SubscriberStatus{
			{SubscriberID: "Frontend Dev", Role: "Frontend Dev", Availability: "busy", Available: true},
			{SubscriberID: "Backend Dev", Role: "Backend Dev", Availability: "available", Available: true},
		},
	}

	report := Analyze(tasks, nil, status, ReportFilter{}, t0.Add(30*time.Second))

	// Active load must be 1, planned assigned backlog must be 2
	assert.Equal(t, 1, report.ObservableFacts.ActiveVsPlannedLoad.ActiveInProgressTasks)
	assert.Equal(t, 2, report.ObservableFacts.ActiveVsPlannedLoad.PlannedAssignedTasks)

	// Ready-unclaimed analysis:
	// Task 102 must be classified under OwnerBusy (legitimate planned serialization)
	// Task 103 must be classified under OwnerIdle (coordination lag candidate)
	ready := report.HeuristicSignals.ReadyUnclaimed
	assert.Equal(t, 1, ready.OwnerBusyCount, "Should classify busy owner's queued task as legitimate serialization")
	require.Len(t, ready.OwnerBusyTasks, 1)
	assert.Equal(t, int64(102), ready.OwnerBusyTasks[0].TaskID)
	assert.Equal(t, int64(101), ready.OwnerBusyTasks[0].OwnerActiveID)

	assert.Equal(t, 1, ready.OwnerIdleCount, "Should classify idle owner's queued task as lag candidate")
	require.Len(t, ready.OwnerIdleTasks, 1)
	assert.Equal(t, int64(103), ready.OwnerIdleTasks[0].TaskID)
	assert.Equal(t, "Backend Dev", ready.OwnerIdleTasks[0].Assignee)
}

func TestNotificationChurn_StaleUnclaimedReminders(t *testing.T) {
	tClaim, _ := time.Parse(time.RFC3339, "2026-09-29T06:11:59Z")
	tMsg, _ := time.Parse(time.RFC3339, "2026-09-29T06:13:47Z")

	tasks := []Task{
		{
			ID:         1326,
			Title:      "Stories display model comparison",
			Status:     "in_progress",
			AssignedTo: strPtr("Game Design Director"),
			CreatedAt:  "2026-09-29T06:11:53Z",
			ClaimedAt:  strPtr(tClaim.Format(time.RFC3339)),
		},
	}

	messages := []Message{
		{
			ID:           13514,
			Project:      "death-or-trade-ai-auto",
			SubscriberID: "Orchestrator",
			Content:      "@Game Design Director #1326 is still unclaimed and blocks final integration #1331. Please claim it now...",
			CreatedAt:    tMsg.Format(time.RFC3339),
		},
	}

	report := Analyze(tasks, messages, nil, ReportFilter{}, tMsg.Add(10*time.Second))

	churn := report.HeuristicSignals.NotificationChurn
	require.Len(t, churn, 1, "Should detect stale unclaimed reminder")
	assert.Equal(t, int64(1326), churn[0].TaskID)
	assert.Equal(t, "stale_unclaimed_reminder", churn[0].ChurnType)
	assert.Equal(t, "Orchestrator", churn[0].Sender)
	assert.Equal(t, 108.0, churn[0].TimeSpanSec) // 06:13:47 minus 06:11:59 = 108s
	assert.Equal(t, 108.0, churn[0].StaleInformationAgeSec)
	assert.Contains(t, churn[0].Confidence, "High")
	assert.Contains(t, churn[0].AnalysisNote, "stale-information age")
}

func TestNotificationChurn_RapidNudging(t *testing.T) {
	t0, _ := time.Parse(time.RFC3339, "2026-09-29T06:10:00Z")

	tasks := []Task{
		{
			ID:         200,
			Title:      "Important Bugfix",
			Status:     "pending",
			AssignedTo: strPtr("Dev A"),
			CreatedAt:  t0.Format(time.RFC3339),
		},
	}

	messages := []Message{
		{
			ID:           1,
			SubscriberID: "Lead",
			Content:      "Please check #200",
			CreatedAt:    t0.Add(10 * time.Second).Format(time.RFC3339),
		},
		{
			ID:           2,
			SubscriberID: "Lead",
			Content:      "Nudge on #200, need update",
			CreatedAt:    t0.Add(30 * time.Second).Format(time.RFC3339),
		},
		{
			ID:           3,
			SubscriberID: "Lead",
			Content:      "#200 is blocking release!",
			CreatedAt:    t0.Add(60 * time.Second).Format(time.RFC3339),
		},
	}

	report := Analyze(tasks, messages, nil, ReportFilter{}, t0.Add(100*time.Second))
	churn := report.HeuristicSignals.NotificationChurn
	require.Len(t, churn, 1)
	assert.Equal(t, int64(200), churn[0].TaskID)
	assert.Equal(t, "rapid_nudging", churn[0].ChurnType)
	assert.Equal(t, 3, churn[0].NoticeCount)
	assert.Equal(t, 50.0, churn[0].TimeSpanSec)
}

func TestDuplicateScopeCandidates(t *testing.T) {
	t0, _ := time.Parse(time.RFC3339, "2026-09-29T06:00:00Z")

	tasks := []Task{
		{
			ID:        301,
			Title:     "Build coordination efficiency reporting tools for Coral board",
			CreatedAt: t0.Format(time.RFC3339),
		},
		{
			ID:        302,
			Title:     "Build coordination efficiency reporting tools for team queue",
			CreatedAt: t0.Add(10 * time.Minute).Format(time.RFC3339),
		},
		{
			ID:        303,
			Title:     "Fix database transaction retry logic in postgres store",
			CreatedAt: t0.Add(20 * time.Minute).Format(time.RFC3339),
		},
	}

	report := Analyze(tasks, nil, nil, ReportFilter{}, t0.Add(1*time.Hour))
	dups := report.HeuristicSignals.DuplicateScopeCandidates
	require.Len(t, dups, 1)
	assert.Equal(t, int64(301), dups[0].TaskID1)
	assert.Equal(t, int64(302), dups[0].TaskID2)
	assert.True(t, dups[0].Similarity >= 0.70)
}

func TestCoordinationScorecard_BalancedIndex(t *testing.T) {
	t0, _ := time.Parse(time.RFC3339, "2026-09-29T06:00:00Z")

	// Perfectly coordinated run: tasks claimed quickly, completed, no churn
	tasks := []Task{
		{
			ID:          1,
			Title:       "Clean Task 1",
			Status:      "completed",
			CreatedAt:   t0.Format(time.RFC3339),
			ClaimedAt:   strPtr(t0.Add(5 * time.Second).Format(time.RFC3339)),
			CompletedAt: strPtr(t0.Add(30 * time.Second).Format(time.RFC3339)),
		},
	}

	cleanReport := Analyze(tasks, nil, nil, ReportFilter{}, t0.Add(60*time.Second))
	// Notification evidence is absent, so the report exposes only 85 available
	// points instead of awarding an unsupported clean 100.
	assert.Equal(t, 0.0, cleanReport.CoordinationScore.TotalScore)
	assert.Equal(t, 85.0, cleanReport.CoordinationScore.AvailablePoints)
	assert.Equal(t, 0.85, cleanReport.CoordinationScore.CoverageRatio)
	assert.True(t, cleanReport.CoordinationScore.ScoreSuppressed)
	assert.Contains(t, cleanReport.CoordinationScore.SuppressionRationale, "Aggregate score suppressed")
	assert.Contains(t, cleanReport.CoordinationScore.SuppressionRationale, "Notification Hygiene")
	assert.Equal(t, 25.0, cleanReport.CoordinationScore.ClaimPromptnessScore)
	assert.Equal(t, 25.0, cleanReport.CoordinationScore.SlotFluidityScore)
	assert.Equal(t, 20.0, cleanReport.CoordinationScore.DependencyClarityScore)
	assert.Equal(t, 0.0, cleanReport.CoordinationScore.NotificationCleanScore)
	assert.Equal(t, 15.0, cleanReport.CoordinationScore.RecoveryEconomyScore)

	// Run with 1 idle unclaimed task and 1 notification churn
	idleTask := Task{
		ID:         2,
		Title:      "Idle Task",
		Status:     "pending",
		AssignedTo: strPtr("Agent X"),
		CreatedAt:  t0.Format(time.RFC3339),
	}
	messages := []Message{
		{
			ID:           1,
			SubscriberID: "Orchestrator",
			Content:      "#1 is still unclaimed",
			CreatedAt:    t0.Add(20 * time.Second).Format(time.RFC3339),
		},
	}
	degradedReport := Analyze([]Task{tasks[0], idleTask}, messages, nil, ReportFilter{}, t0.Add(60*time.Second))
	// Promptness docked by 4 (25 -> 21)
	assert.Equal(t, 21.0, degradedReport.CoordinationScore.ClaimPromptnessScore)
	// Notification score docked by 3 (15 -> 12)
	assert.Equal(t, 12.0, degradedReport.CoordinationScore.NotificationCleanScore)
	// Total score: 21 + 25 + 20 + 12 + 15 = 93.0
	assert.Equal(t, 93.0, degradedReport.CoordinationScore.TotalScore)
	assert.Equal(t, 100.0, degradedReport.CoordinationScore.AvailablePoints)
	assert.Equal(t, 1.0, degradedReport.CoordinationScore.CoverageRatio)
	assert.False(t, degradedReport.CoordinationScore.ScoreSuppressed)
	assert.Empty(t, degradedReport.CoordinationScore.SuppressionRationale)
}

func TestFormatters(t *testing.T) {
	t0, _ := time.Parse(time.RFC3339, "2026-09-29T06:00:00Z")
	tasks := []Task{
		{
			ID:          1,
			Title:       "Test Task",
			Status:      "completed",
			CreatedAt:   t0.Format(time.RFC3339),
			ClaimedAt:   strPtr(t0.Add(5 * time.Second).Format(time.RFC3339)),
			CompletedAt: strPtr(t0.Add(25 * time.Second).Format(time.RFC3339)),
		},
	}
	report := Analyze(tasks, nil, nil, ReportFilter{Board: "test-board"}, t0.Add(60*time.Second))

	// Markdown
	md := FormatMarkdown(report)
	assert.Contains(t, md, "# Coordination Efficiency Report: `test-board`")
	assert.Contains(t, md, "Overall Coordination Quality Index: suppressed")
	assert.Contains(t, md, "Coverage: 85.0%")
	assert.Contains(t, md, "Suppression Rationale")
	assert.Contains(t, md, "Ready-to-Claim Latency (Unblocked → Claim)")
	assert.Contains(t, md, "Total Pre-Claim Wait (Created → Claim, includes blocking)")

	// JSON
	jsonStr, err := FormatJSON(report)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(jsonStr), "{"))
	assert.Contains(t, jsonStr, `"board": "test-board"`)
	assert.Contains(t, jsonStr, `"total_score": 0`)
	assert.Contains(t, jsonStr, `"coverage_ratio": 0.85`)
	assert.Contains(t, jsonStr, `"score_suppressed": true`)
}

func TestCoordinationScorecard_SuppressesEmptySample(t *testing.T) {
	report := Analyze(nil, nil, nil, ReportFilter{Board: "empty"}, time.Now())
	assert.True(t, report.CoordinationScore.ScoreSuppressed)
	assert.Equal(t, 0.0, report.CoordinationScore.TotalScore)
	assert.Equal(t, 0.0, report.CoordinationScore.AvailablePoints)
	assert.Equal(t, 0.0, report.CoordinationScore.CoverageRatio)
	assert.NotEmpty(t, report.CoordinationScore.SuppressionRationale)
	assert.Contains(t, report.CoordinationScore.ValidationStatus, "Suppressed")
}
