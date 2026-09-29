package coordination

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// DefaultInactivityLimit is the heuristic threshold for long active tasks.
const DefaultInactivityLimit = 15 * time.Minute

// Analyze processes tasks, messages, and board status into a structured report.
func Analyze(tasks []Task, messages []Message, status *BoardStatus, filter ReportFilter, now time.Time) Report {
	if now.IsZero() {
		now = time.Now()
	}
	inactivityLimit := filter.InactivityLimit
	if inactivityLimit <= 0 {
		inactivityLimit = DefaultInactivityLimit
	}

	filteredTasks := filterTasks(tasks, filter.Since, filter.Until)
	filteredMessages := filterMessages(messages, filter.Since, filter.Until)

	metadata := ReportMetadata{
		Board:                 filter.Board,
		GeneratedAt:           now,
		WindowStart:           filter.Since,
		WindowEnd:             filter.Until,
		TotalTasksInWindow:    len(filteredTasks),
		TotalMessagesInWindow: len(filteredMessages),
	}
	if status != nil {
		metadata.SubscribersTracked = len(status.Agents)
	}

	taskCounts := countTasks(filteredTasks)
	durations := computeDurations(filteredTasks, tasks)
	activeLoad := computeActiveLoad(filteredTasks, tasks, status, filter.Since)
	depMetrics := computeDependencies(filteredTasks)
	reviewSlots := computeReviewHeldSlots(filteredTasks, now)

	facts := ObservableFacts{
		TaskCounts:          taskCounts,
		Durations:           durations,
		ActiveVsPlannedLoad: activeLoad,
		DependencyGraph:     depMetrics,
		ReviewHeldSlots:     reviewSlots,
	}

	heuristics := HeuristicSignals{
		ReadyUnclaimed:           analyzeReadyUnclaimed(filteredTasks, activeLoad, now),
		ActiveWithoutProgress:    detectActiveWithoutProgress(filteredTasks, filteredMessages, inactivityLimit, now),
		NotificationChurn:        detectNotificationChurn(filteredMessages, filteredTasks),
		ManualRecoveryEvents:     detectManualRecovery(filteredTasks, filteredMessages),
		DuplicateScopeCandidates: detectDuplicateScopeCandidates(filteredTasks),
		RepetitiveHandoffLoops:   detectRepetitiveHandoffs(filteredMessages),
	}

	unavailable := []string{
		"Subagent internal execution duration and step count (only claimed task boundaries recorded)",
		"Agent local CPU, memory, and compiler activity during silent in-progress periods",
		"Exact reasoning activity for uninstrumented external providers",
		"Unrecorded terminal/session crashes that did not trigger coral hook events",
		"Granular file edit timestamps prior to task completion or commit landing",
	}

	scorecard := computeScorecard(taskCounts, durations, heuristics, depMetrics, len(filteredMessages))

	findings := generateKeyFindings(facts, heuristics)
	recommendations := generateRecommendations(facts, heuristics, scorecard)

	return Report{
		Metadata:           metadata,
		ObservableFacts:    facts,
		HeuristicSignals:   heuristics,
		UnavailableMetrics: unavailable,
		CoordinationScore:  scorecard,
		KeyFindings:        findings,
		Recommendations:    recommendations,
	}
}

func filterTasks(tasks []Task, since, until *time.Time) []Task {
	var result []Task
	for _, t := range tasks {
		created, err := parseTime(t.CreatedAt)
		if err != nil {
			result = append(result, t)
			continue
		}
		if since != nil && created.Before(*since) {
			continue
		}
		if until != nil && created.After(*until) {
			continue
		}
		result = append(result, t)
	}
	return result
}

func filterMessages(messages []Message, since, until *time.Time) []Message {
	var result []Message
	for _, m := range messages {
		created, err := parseTime(m.CreatedAt)
		if err != nil {
			result = append(result, m)
			continue
		}
		if since != nil && created.Before(*since) {
			continue
		}
		if until != nil && created.After(*until) {
			continue
		}
		result = append(result, m)
	}
	return result
}

func countTasks(tasks []Task) TaskCounts {
	var c TaskCounts
	c.TotalCreated = len(tasks)
	for _, t := range tasks {
		switch t.Status {
		case "completed":
			c.Completed++
		case "in_progress":
			c.InProgress++
		case "pending":
			c.Pending++
		case "blocked":
			c.Blocked++
		case "cancelled":
			c.Cancelled++
		case "skipped":
			c.Skipped++
		}
	}
	return c
}

func computeDurations(cohortTasks []Task, allTasks []Task) TimingMetrics {
	var readyToClaimLats []float64
	var preClaimWaits []float64
	var execDurs []float64
	var leadTimes []float64
	missingCount := 0

	// Build map of completed_at for all tasks
	taskCompMap := make(map[int64]time.Time)
	for _, at := range allTasks {
		if at.CompletedAt != nil && *at.CompletedAt != "" {
			if parsed, err := parseTime(*at.CompletedAt); err == nil {
				taskCompMap[at.ID] = parsed
			}
		}
	}

	for _, t := range cohortTasks {
		cAt, cErr := parseTime(t.CreatedAt)
		if cErr != nil {
			missingCount++
			continue
		}
		var clAt *time.Time
		if t.ClaimedAt != nil && *t.ClaimedAt != "" {
			if parsed, err := parseTime(*t.ClaimedAt); err == nil {
				clAt = &parsed
				waitSec := parsed.Sub(cAt).Seconds()
				preClaimWaits = append(preClaimWaits, waitSec)

				// Determine ready timestamp
				readyAt := cAt
				hasUnmetPrereq := false
				var latestPrereq time.Time
				for _, dep := range t.BlockedBy {
					if comp, ok := taskCompMap[dep.TaskID]; ok {
						if comp.After(latestPrereq) {
							latestPrereq = comp
						}
					} else {
						hasUnmetPrereq = true
					}
				}
				if len(t.BlockedBy) > 0 && !hasUnmetPrereq && !latestPrereq.IsZero() {
					readyAt = latestPrereq
				}
				readyLat := parsed.Sub(readyAt).Seconds()
				if readyLat < 0 {
					readyLat = 0
				}
				readyToClaimLats = append(readyToClaimLats, readyLat)
			} else {
				missingCount++
			}
		} else if t.Status == "completed" || t.Status == "in_progress" {
			missingCount++
		}

		if t.CompletedAt != nil && *t.CompletedAt != "" {
			if compAt, err := parseTime(*t.CompletedAt); err == nil {
				leadTimes = append(leadTimes, compAt.Sub(cAt).Seconds())
				if clAt != nil {
					execDurs = append(execDurs, compAt.Sub(*clAt).Seconds())
				}
			} else {
				missingCount++
			}
		} else if t.Status == "completed" {
			missingCount++
		}
	}

	return TimingMetrics{
		ClaimLatency:        calcStats(readyToClaimLats),
		ReadyToClaimLatency: calcStats(readyToClaimLats),
		TotalPreClaimWait:   calcStats(preClaimWaits),
		ExecutionDuration:   calcStats(execDurs),
		TotalLeadTime:       calcStats(leadTimes),
		MissingTimestamps:   missingCount,
	}
}

func calcStats(values []float64) DurationStats {
	if len(values) == 0 {
		return DurationStats{}
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)

	sum := 0.0
	minVal := sorted[0]
	maxVal := sorted[len(sorted)-1]
	for _, v := range sorted {
		sum += v
	}
	mean := sum / float64(len(sorted))

	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 && len(sorted) > 1 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}

	p90Idx := int(math.Ceil(0.90*float64(len(sorted)))) - 1
	if p90Idx < 0 {
		p90Idx = 0
	}
	if p90Idx >= len(sorted) {
		p90Idx = len(sorted) - 1
	}
	p90 := sorted[p90Idx]

	return DurationStats{
		Count:   len(sorted),
		MinSec:  minVal,
		MaxSec:  maxVal,
		MeanSec: mean,
		MedSec:  median,
		P90Sec:  p90,
	}
}

func computeActiveLoad(cohortTasks []Task, allTasks []Task, status *BoardStatus, since *time.Time) ActiveLoadMetrics {
	var metrics ActiveLoadMetrics
	assignedMap := make(map[string]int)
	activeTaskBySubscriber := make(map[string]int64)

	// Cohort counts (window-specific)
	for _, t := range cohortTasks {
		switch t.Status {
		case "in_progress":
			metrics.ActiveInProgressTasks++
		case "pending":
			if t.AssignedTo != nil && *t.AssignedTo != "" {
				metrics.PlannedAssignedTasks++
			} else {
				metrics.UnassignedPendingTasks++
			}
		}
	}

	// Board-wide open tasks & carryover detection
	for _, t := range allTasks {
		switch t.Status {
		case "in_progress":
			metrics.BoardWideActiveTasks++
			if t.AssignedTo != nil && *t.AssignedTo != "" {
				activeTaskBySubscriber[*t.AssignedTo] = t.ID
			}
		case "pending", "blocked":
			metrics.BoardWidePendingTasks++
			if t.AssignedTo != nil && *t.AssignedTo != "" {
				assignedMap[*t.AssignedTo]++
			}
		}

		// Detect older carryover tasks created before window start that remain open
		if since != nil && (t.Status == "in_progress" || t.Status == "pending" || t.Status == "blocked") {
			if created, err := parseTime(t.CreatedAt); err == nil && created.Before(*since) {
				metrics.CarryoverOlderTasks = append(metrics.CarryoverOlderTasks, CarryoverTask{
					ID:        t.ID,
					Title:     t.Title,
					Status:    t.Status,
					Assignee:  stringVal(t.AssignedTo),
					CreatedAt: t.CreatedAt,
				})
			}
		}
	}

	seenSubscribers := make(map[string]bool)
	if status != nil {
		for _, a := range status.Agents {
			seenSubscribers[a.SubscriberID] = true
			activeID := activeTaskBySubscriber[a.SubscriberID]
			if activeID == 0 {
				for _, t := range a.Tasks {
					if t.Status == "in_progress" {
						activeID = t.ID
						break
					}
				}
			}
			load := SubscriberLoad{
				SubscriberID: a.SubscriberID,
				Role:         a.Role,
				Availability: a.Availability,
				ActiveTaskID: activeID,
				QueuedCount:  assignedMap[a.SubscriberID],
			}
			metrics.SubscriberLoad = append(metrics.SubscriberLoad, load)
		}
	}

	// Also include any active subscribers from tasks who are not in status.Agents
	for sub, activeID := range activeTaskBySubscriber {
		if !seenSubscribers[sub] {
			metrics.SubscriberLoad = append(metrics.SubscriberLoad, SubscriberLoad{
				SubscriberID: sub,
				Role:         sub,
				Availability: "busy",
				ActiveTaskID: activeID,
				QueuedCount:  assignedMap[sub],
			})
		}
	}

	return metrics
}

func computeDependencies(tasks []Task) DependencyMetrics {
	var blockedList []BlockedTask
	taskStatusMap := make(map[int64]string)
	for _, t := range tasks {
		taskStatusMap[t.ID] = t.Status
	}

	for _, t := range tasks {
		if t.Status != "blocked" {
			continue
		}
		bTask := BlockedTask{
			TaskID:   t.ID,
			Title:    t.Title,
			Assignee: stringVal(t.AssignedTo),
		}
		for _, dep := range t.BlockedBy {
			curStatus := taskStatusMap[dep.TaskID]
			if curStatus == "" {
				curStatus = "unknown/external"
			}
			cond := dep.Condition
			if cond == "" {
				cond = "success"
			}
			satisfied := curStatus == "completed"
			bTask.Dependencies = append(bTask.Dependencies, DependencyStatus{
				PrerequisiteID: dep.TaskID,
				CurrentStatus:  curStatus,
				Condition:      cond,
				Satisfied:      satisfied,
			})
		}
		blockedList = append(blockedList, bTask)
	}

	return DependencyMetrics{
		TotalBlockedTasks: len(blockedList),
		BlockedTasks:      blockedList,
	}
}

func computeReviewHeldSlots(tasks []Task, now time.Time) []ReviewHeldTask {
	var reviewTasks []ReviewHeldTask
	for _, t := range tasks {
		if t.Workflow != nil && t.Workflow.CompletionReview != nil {
			duration := 0.0
			if t.ClaimedAt != nil {
				if cl, err := parseTime(*t.ClaimedAt); err == nil {
					duration = now.Sub(cl).Seconds()
				}
			}
			reviewTasks = append(reviewTasks, ReviewHeldTask{
				TaskID:       t.ID,
				Title:        t.Title,
				Assignee:     stringVal(t.AssignedTo),
				ReviewerRole: t.Workflow.CompletionReview.ReviewerRole,
				DurationSec:  duration,
			})
		}
	}
	return reviewTasks
}

// analyzeReadyUnclaimed accurately separates owner-busy backlog from owner-idle coordination lag.
func analyzeReadyUnclaimed(tasks []Task, load ActiveLoadMetrics, now time.Time) ReadyUnclaimedAnalysis {
	var analysis ReadyUnclaimedAnalysis

	// Build map of subscribers with active tasks
	busySubscribers := make(map[string]int64)
	for _, sub := range load.SubscriberLoad {
		if sub.ActiveTaskID != 0 || sub.Availability == "busy" {
			busySubscribers[sub.SubscriberID] = sub.ActiveTaskID
		}
	}

	for _, t := range tasks {
		if t.Status != "pending" {
			continue
		}
		createdAt, err := parseTime(t.CreatedAt)
		age := 0.0
		if err == nil {
			age = now.Sub(createdAt).Seconds()
		}

		assignee := stringVal(t.AssignedTo)
		if assignee == "" {
			analysis.UnassignedCount++
			analysis.UnassignedTasks = append(analysis.UnassignedTasks, ReadyTaskDetail{
				TaskID:      t.ID,
				Title:       t.Title,
				Priority:    t.Priority,
				Assignee:    "—",
				AgeSeconds:  age,
				OwnerStatus: "unassigned",
			})
			continue
		}

		activeID, busy := busySubscribers[assignee]
		if busy {
			analysis.OwnerBusyCount++
			analysis.OwnerBusyTasks = append(analysis.OwnerBusyTasks, ReadyTaskDetail{
				TaskID:        t.ID,
				Title:         t.Title,
				Priority:      t.Priority,
				Assignee:      assignee,
				AgeSeconds:    age,
				OwnerStatus:   "busy_with_active_task",
				OwnerActiveID: activeID,
			})
		} else {
			analysis.OwnerIdleCount++
			analysis.OwnerIdleTasks = append(analysis.OwnerIdleTasks, ReadyTaskDetail{
				TaskID:      t.ID,
				Title:       t.Title,
				Priority:    t.Priority,
				Assignee:    assignee,
				AgeSeconds:  age,
				OwnerStatus: "idle_available",
			})
		}
	}

	return analysis
}

func detectActiveWithoutProgress(tasks []Task, messages []Message, threshold time.Duration, now time.Time) []ActiveWithoutProgress {
	var candidates []ActiveWithoutProgress

	// Build map of last message time per subscriber
	lastMsgMap := make(map[string]time.Time)
	for _, m := range messages {
		t, err := parseTime(m.CreatedAt)
		if err == nil {
			if prev, ok := lastMsgMap[m.SubscriberID]; !ok || t.After(prev) {
				lastMsgMap[m.SubscriberID] = t
			}
		}
	}

	for _, t := range tasks {
		if t.Status != "in_progress" {
			continue
		}
		if t.ClaimedAt == nil || *t.ClaimedAt == "" {
			continue
		}
		clTime, err := parseTime(*t.ClaimedAt)
		if err != nil {
			continue
		}
		duration := now.Sub(clTime)
		if duration >= threshold {
			assignee := stringVal(t.AssignedTo)
			var lastMsg *time.Time
			if lm, ok := lastMsgMap[assignee]; ok {
				lastMsg = &lm
			}
			candidates = append(candidates, ActiveWithoutProgress{
				TaskID:          t.ID,
				Title:           t.Title,
				Assignee:        assignee,
				ClaimedAt:       clTime,
				DurationSeconds: duration.Seconds(),
				LastMessageAt:   lastMsg,
				ConfidenceNote:  "Candidate indicator only. Does not prove inactivity; agent may be running local builds/tests or model reasoning.",
			})
		}
	}
	return candidates
}

func detectNotificationChurn(messages []Message, tasks []Task) []NotificationChurnIncident {
	var incidents []NotificationChurnIncident

	// Task metadata and claim timestamps
	taskMap := make(map[int64]Task)
	taskClaimMap := make(map[int64]time.Time)
	for _, t := range tasks {
		taskMap[t.ID] = t
		if t.ClaimedAt != nil && *t.ClaimedAt != "" {
			if parsed, err := parseTime(*t.ClaimedAt); err == nil {
				taskClaimMap[t.ID] = parsed
			}
		}
	}

	staleHandled := make(map[int64]bool)

	// Phase 1: Check for stale unclaimed reminders
	// (Message states task is unclaimed, but task was already claimed earlier)
	for _, m := range messages {
		lower := strings.ToLower(m.Content)
		if strings.Contains(lower, "still unclaimed") || strings.Contains(lower, "is unclaimed") || strings.Contains(lower, "unclaimed and blocks") {
			mTime, err := parseTime(m.CreatedAt)
			if err != nil {
				continue
			}
			taskIDs := extractTaskIDs(m.Content)
			for _, id := range taskIDs {
				claimedAt, claimed := taskClaimMap[id]
				if claimed && mTime.After(claimedAt.Add(2*time.Second)) {
					lagSec := mTime.Sub(claimedAt).Seconds()
					task := taskMap[id]
					assignee := stringVal(task.AssignedTo)
					if assignee == "" {
						assignee = m.SubscriberID
					}
					incidents = append(incidents, NotificationChurnIncident{
						TaskID:                 id,
						Assignee:               assignee,
						NoticeCount:            1,
						TimeSpanSec:            lagSec,
						StaleInformationAgeSec: lagSec,
						FirstNoticeAt:          claimedAt,
						LastNoticeAt:           mTime,
						ChurnType:              "stale_unclaimed_reminder",
						SampleMessage:          m.Content,
						Sender:                 m.SubscriberID,
						ClaimedAt:              &claimedAt,
						Confidence:             fmt.Sprintf("High (message at %s dispatched %.0fs after claim at %s)", m.CreatedAt, lagSec, claimedAt.Format(time.RFC3339)),
						AnalysisNote:           fmt.Sprintf("Sender %s posted unclaimed reminder for Task #%d at %s (stale-information age: %.0fs after claim by %s at %s). Stale context or delayed dispatch, not measured network transport lag.", m.SubscriberID, id, m.CreatedAt, lagSec, assignee, claimedAt.Format(time.RFC3339)),
					})
					staleHandled[id] = true
				}
			}
		}
	}

	// Phase 2: Group reminder/nudge notices by task ID to detect rapid nudging or repetition
	taskNotices := make(map[int64][]Message)
	for _, m := range messages {
		lower := strings.ToLower(m.Content)
		// Ignore normal task lifecycle announcements and completion handoffs
		if strings.Contains(lower, "completed") || strings.Contains(lower, "complete.") || strings.Contains(lower, "outcome:") || strings.Contains(lower, "unblocked") || strings.Contains(lower, "claimed by") || strings.Contains(lower, "landed") {
			continue
		}
		// Consider messages that are reminders, nudges, or status requests
		if strings.Contains(lower, "nudge") || strings.Contains(lower, "reminder") || strings.Contains(lower, "please") || strings.Contains(lower, "still") || strings.Contains(lower, "waiting") || strings.Contains(lower, "blocking") {
			ids := extractTaskIDs(m.Content)
			for _, id := range ids {
				taskNotices[id] = append(taskNotices[id], m)
			}
		}
	}

	for id, msgs := range taskNotices {
		if staleHandled[id] {
			continue // Already captured with deeper detail above
		}
		if len(msgs) < 2 {
			continue
		}
		first, err1 := parseTime(msgs[0].CreatedAt)
		last, err2 := parseTime(msgs[len(msgs)-1].CreatedAt)
		if err1 != nil || err2 != nil {
			continue
		}
		spanSec := last.Sub(first).Seconds()

		churnType := ""
		if len(msgs) >= 3 && spanSec < 180 {
			churnType = "rapid_nudging"
		} else if len(msgs) >= 2 && spanSec < 90 {
			churnType = "repeated_notices"
		}

		if churnType != "" {
			task := taskMap[id]
			assignee := stringVal(task.AssignedTo)
			var claimedAtPtr *time.Time
			if cl, ok := taskClaimMap[id]; ok {
				claimedAtPtr = &cl
			}
			incidents = append(incidents, NotificationChurnIncident{
				TaskID:        id,
				Assignee:      assignee,
				NoticeCount:   len(msgs),
				TimeSpanSec:   spanSec,
				FirstNoticeAt: first,
				LastNoticeAt:  last,
				ChurnType:     churnType,
				SampleMessage: msgs[len(msgs)-1].Content,
				Sender:        msgs[len(msgs)-1].SubscriberID,
				ClaimedAt:     claimedAtPtr,
				Confidence:    "Medium (based on frequency and timespan heuristic)",
				AnalysisNote:  fmt.Sprintf("%d messages referenced Task #%d within %.0fs.", len(msgs), id, spanSec),
			})
		}
	}

	return incidents
}

func detectManualRecovery(tasks []Task, messages []Message) []ManualRecoveryEvent {
	var events []ManualRecoveryEvent

	for _, t := range tasks {
		if t.Status == "cancelled" {
			ts, _ := parseTime(t.CreatedAt)
			reason := stringVal(t.CompletionMessage)
			events = append(events, ManualRecoveryEvent{
				TaskID:    t.ID,
				Title:     t.Title,
				EventType: "cancellation",
				Actor:     stringVal(t.CompletedBy),
				Timestamp: ts,
				Reason:    reason,
			})
		}
		if t.Workflow != nil && t.Workflow.RetryOf > 0 {
			ts, _ := parseTime(t.CreatedAt)
			events = append(events, ManualRecoveryEvent{
				TaskID:      t.ID,
				Title:       t.Title,
				EventType:   "retry",
				Actor:       t.CreatedBy,
				Timestamp:   ts,
				Predecessor: t.Workflow.RetryOf,
				Reason:      "Retry of predecessor task",
			})
		}
	}

	for _, m := range messages {
		if strings.Contains(m.Content, "reassigned") {
			ts, _ := parseTime(m.CreatedAt)
			events = append(events, ManualRecoveryEvent{
				Title:     m.Content,
				EventType: "reassignment",
				Actor:     m.SubscriberID,
				Timestamp: ts,
				Reason:    m.Content,
			})
		}
	}

	return events
}

func detectDuplicateScopeCandidates(tasks []Task) []DuplicateScopeCandidate {
	var duplicates []DuplicateScopeCandidate
	stopWords := map[string]bool{"the": true, "and": true, "for": true, "to": true, "in": true, "a": true, "of": true, "on": true, "with": true, "is": true}

	for i := 0; i < len(tasks); i++ {
		t1 := tasks[i]
		time1, err1 := parseTime(t1.CreatedAt)
		words1 := tokenize(t1.Title, stopWords)
		if len(words1) == 0 {
			continue
		}

		for j := i + 1; j < len(tasks); j++ {
			t2 := tasks[j]
			time2, err2 := parseTime(t2.CreatedAt)
			words2 := tokenize(t2.Title, stopWords)
			if len(words2) == 0 {
				continue
			}

			// Temporal proximity: within 6 hours
			if err1 == nil && err2 == nil {
				diff := time1.Sub(time2)
				if diff < 0 {
					diff = -diff
				}
				if diff > 6*time.Hour {
					continue
				}
			}

			sim, shared := jaccardSimilarity(words1, words2)
			overlap := 0.0
			minLen := len(words1)
			if len(words2) < minLen {
				minLen = len(words2)
			}
			if minLen > 0 {
				overlap = float64(len(shared)) / float64(minLen)
			}
			if sim >= 0.50 || (overlap >= 0.70 && len(shared) >= 3) {
				score := sim
				if overlap > score {
					score = overlap
				}
				duplicates = append(duplicates, DuplicateScopeCandidate{
					TaskID1:     t1.ID,
					Title1:      t1.Title,
					TaskID2:     t2.ID,
					Title2:      t2.Title,
					Similarity:  math.Round(score*100) / 100,
					CreatedAt1:  time1,
					CreatedAt2:  time2,
					SharedWords: shared,
				})
			}
		}
	}
	return duplicates
}

func detectRepetitiveHandoffs(messages []Message) []RepetitiveHandoffLoop {
	var loops []RepetitiveHandoffLoop
	if len(messages) < 4 {
		return loops
	}

	// Detect if consecutive messages rapidly ping-pong between same actors
	windowSize := 6
	for i := 0; i+windowSize <= len(messages); i++ {
		window := messages[i : i+windowSize]
		counts := make(map[string]int)
		for _, m := range window {
			counts[m.SubscriberID]++
		}
		if len(counts) == 2 {
			tFirst, _ := parseTime(window[0].CreatedAt)
			tLast, _ := parseTime(window[windowSize-1].CreatedAt)
			span := tLast.Sub(tFirst).Seconds()
			if span < 120 {
				var agents []string
				for a := range counts {
					agents = append(agents, a)
				}
				loops = append(loops, RepetitiveHandoffLoop{
					Agents:       agents,
					MessageCount: windowSize,
					TimeSpanSec:  span,
					Summary:      fmt.Sprintf("Rapid %d-message back-and-forth between %s within %.0fs", windowSize, strings.Join(agents, " & "), span),
					FirstTime:    tFirst,
					LastTime:     tLast,
				})
				i += windowSize - 1 // skip ahead
			}
		}
	}
	return loops
}

// computeScorecard implements a transparent, balanced 100-point Coordination Quality Index.
func computeScorecard(counts TaskCounts, times TimingMetrics, h HeuristicSignals, d DependencyMetrics, messageCount int) CoordinationScorecard {
	// Component 1: Claim Promptness (0-25)
	// Measures prompt pickup when owner is idle, without penalizing legitimate busy-owner queueing
	promptness := 25.0
	if h.ReadyUnclaimed.OwnerIdleCount > 0 {
		deduction := float64(h.ReadyUnclaimed.OwnerIdleCount) * 4.0
		if deduction > 20.0 {
			deduction = 20.0
		}
		promptness -= deduction
	}
	if promptness < 0 {
		promptness = 0
	}

	// Component 2: Slot Fluidity (0-25)
	// Measures absence of stalled in-progress tasks
	fluidity := 25.0
	stalledCount := len(h.ActiveWithoutProgress)
	if stalledCount > 0 {
		deduction := float64(stalledCount) * 5.0
		if deduction > 20.0 {
			deduction = 20.0
		}
		fluidity -= deduction
	}
	if fluidity < 0 {
		fluidity = 0
	}

	// Component 3: Dependency Clarity (0-20)
	// Proportion of tasks that have clean dependency resolution
	depClarity := 20.0
	if counts.TotalCreated > 0 && d.TotalBlockedTasks > 0 {
		ratio := float64(d.TotalBlockedTasks) / float64(counts.TotalCreated)
		if ratio > 0.5 {
			depClarity -= 10.0
		} else if ratio > 0.25 {
			depClarity -= 5.0
		}
	}

	// Component 4: Notification Cleanliness (0-15)
	// Absence of redundant nudges and churn
	notifScore := 15.0
	if len(h.NotificationChurn) > 0 {
		deduction := float64(len(h.NotificationChurn)) * 3.0
		if deduction > 12.0 {
			deduction = 12.0
		}
		notifScore -= deduction
	}
	if notifScore < 0 {
		notifScore = 0
	}

	// Component 5: Recovery Economy (0-15)
	// Absence of heavy cancellation or retry rework
	recoveryScore := 15.0
	if len(h.ManualRecoveryEvents) > 0 {
		deduction := float64(len(h.ManualRecoveryEvents)) * 2.5
		if deduction > 10.0 {
			deduction = 10.0
		}
		recoveryScore -= deduction
	}
	if recoveryScore < 0 {
		recoveryScore = 0
	}

	// A dimension is scored only when its required evidence exists. Missing
	// dimensions must not silently become clean/full credit.
	available := 0.0
	observed := 0.0
	if counts.TotalCreated > 0 {
		available += 25 + 25 + 20 + 15
		observed += promptness + fluidity + depClarity + recoveryScore
	} else {
		promptness, fluidity, depClarity, recoveryScore = 0, 0, 0, 0
	}
	if messageCount > 0 {
		available += 15
		observed += notifScore
	} else {
		notifScore = 0
	}
	// A normalized aggregate is only meaningful when every dimension has
	// evidence. Partial samples retain raw observed points but suppress the
	// overall quality number so it cannot be mistaken for board-wide health.
	coverageRatio := math.Round((available/100.0)*1000) / 1000
	suppressed := available < 100
	reportedTotal := observed
	var suppressionRationale string
	if suppressed {
		reportedTotal = 0
		var missing []string
		if counts.TotalCreated == 0 {
			missing = append(missing, "task lifecycle events (Claim Promptness, Slot Fluidity, Dependency Clarity, Recovery Economy)")
		}
		if messageCount == 0 {
			missing = append(missing, "board message stream (Notification Hygiene)")
		}
		if len(missing) == 0 {
			missing = append(missing, "unobserved score dimensions")
		}
		suppressionRationale = fmt.Sprintf("Aggregate score suppressed: sample covers %.1f%% of scorecard dimensions (missing %s). Normalizing partial dimensions or awarding unobserved credit would misrepresent overall coordination quality.", coverageRatio*100, strings.Join(missing, "; "))
	}

	validationStatus := "Provisional / Pending Audit"
	if suppressed {
		if available == 0 {
			validationStatus = "Suppressed / Insufficient evidence"
		} else {
			validationStatus = "Suppressed / Incomplete evidence"
		}
	}

	return CoordinationScorecard{
		TotalScore:             math.Round(reportedTotal*10) / 10,
		ObservedPoints:         math.Round(observed*10) / 10,
		AvailablePoints:        math.Round(available*10) / 10,
		CoverageRatio:          coverageRatio,
		ScoreSuppressed:        suppressed,
		SuppressionRationale:   suppressionRationale,
		ClaimPromptnessScore:   math.Round(promptness*10) / 10,
		SlotFluidityScore:      math.Round(fluidity*10) / 10,
		DependencyClarityScore: math.Round(depClarity*10) / 10,
		NotificationCleanScore: math.Round(notifScore*10) / 10,
		RecoveryEconomyScore:   math.Round(recoveryScore*10) / 10,
		ValidationStatus:       validationStatus,
		AuditCaveat:            fmt.Sprintf("Score reflects observed board events only: %.1f of 100 points are available from this sample (coverage: %.1f%%). Missing dimensions are excluded rather than awarded full credit; local CPU/build runs, agent reasoning, and subagent steps remain unmeasured.", available, coverageRatio*100),
		ScoringMethodology:     "Balanced 100-pt index: Claim Promptness (25), Slot Fluidity (25), Dependency Clarity (20), Notification Signal-to-Noise (15), Recovery Economy (15). Safeguards: serialized planned work is not penalized; cancellation is not scored as success; notification suppression is not rewarded.",
	}
}

func generateKeyFindings(f ObservableFacts, h HeuristicSignals) []string {
	var findings []string

	findings = append(findings, fmt.Sprintf("Total tasks in window: %d (Completed: %d, In Progress: %d, Pending: %d, Blocked: %d, Cancelled: %d)",
		f.TaskCounts.TotalCreated, f.TaskCounts.Completed, f.TaskCounts.InProgress, f.TaskCounts.Pending, f.TaskCounts.Blocked, f.TaskCounts.Cancelled))

	if f.Durations.ClaimLatency.Count > 0 {
		findings = append(findings, fmt.Sprintf("Median claim latency: %.1fs (P90: %.1fs, Mean: %.1fs across %d claims)",
			f.Durations.ClaimLatency.MedSec, f.Durations.ClaimLatency.P90Sec, f.Durations.ClaimLatency.MeanSec, f.Durations.ClaimLatency.Count))
	}
	if f.Durations.ExecutionDuration.Count > 0 {
		findings = append(findings, fmt.Sprintf("Median execution duration: %.1fs (P90: %.1fs across %d completed tasks)",
			f.Durations.ExecutionDuration.MedSec, f.Durations.ExecutionDuration.P90Sec, f.Durations.ExecutionDuration.Count))
	}

	if h.ReadyUnclaimed.OwnerBusyCount > 0 {
		findings = append(findings, fmt.Sprintf("Observable planned serialization: %d ready task(s) are queued behind currently active subscriber work (legitimate backlog, NOT coordination lag).",
			h.ReadyUnclaimed.OwnerBusyCount))
	}
	if h.ReadyUnclaimed.OwnerIdleCount > 0 {
		findings = append(findings, fmt.Sprintf("Coordination lag candidate: %d ready task(s) assigned to currently idle subscribers remain unclaimed.",
			h.ReadyUnclaimed.OwnerIdleCount))
	}

	if len(h.NotificationChurn) > 0 {
		findings = append(findings, fmt.Sprintf("Notification churn observed in %d incident(s): reminders dispatched for tasks already claimed or in rapid (<2m) succession.",
			len(h.NotificationChurn)))
	}
	if len(h.ActiveWithoutProgress) > 0 {
		findings = append(findings, fmt.Sprintf("Active duration observation: %d task(s) in progress exceed threshold (candidate status, not proof of inactivity).",
			len(h.ActiveWithoutProgress)))
	}
	if len(h.DuplicateScopeCandidates) > 0 {
		findings = append(findings, fmt.Sprintf("Duplicate scope candidates: %d pair(s) of concurrently created tasks share >70%% title token similarity.",
			len(h.DuplicateScopeCandidates)))
	}

	return findings
}

func generateRecommendations(f ObservableFacts, h HeuristicSignals, s CoordinationScorecard) []string {
	var recs []string

	if h.ReadyUnclaimed.OwnerIdleCount > 0 {
		recs = append(recs, fmt.Sprintf("Dispatch targeted nudges with explicit claim IDs (coral-board task claim <ID>) for the %d unclaimed task(s) assigned to idle subscribers.",
			h.ReadyUnclaimed.OwnerIdleCount))
	}
	if len(h.NotificationChurn) > 0 {
		recs = append(recs, "Debounce orchestrator reminders against actual task claim state to avoid alerting on already-claimed tasks.")
	}
	if f.TaskCounts.Blocked > 0 {
		recs = append(recs, "Monitor upstream prerequisites for blocked tasks to ensure prompt unblocking upon prerequisite completion.")
	}
	if len(h.DuplicateScopeCandidates) > 0 {
		recs = append(recs, "Review overlapping task titles before launch to clarify task boundaries and prevent duplicated agent work.")
	}
	if len(recs) == 0 {
		recs = append(recs, "Queue coordination is operating smoothly with balanced slot utilization and low notification churn.")
	}
	return recs
}

// Helpers
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time string")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse time: %s", s)
}

func stringVal(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func extractTaskIDs(content string) []int64 {
	var ids []int64
	seen := make(map[int64]bool)

	// Pattern 1: #\d+
	for i := 0; i < len(content); i++ {
		if content[i] == '#' && i+1 < len(content) && content[i+1] >= '0' && content[i+1] <= '9' {
			var id int64
			j := i + 1
			for j < len(content) && content[j] >= '0' && content[j] <= '9' {
				id = id*10 + int64(content[j]-'0')
				j++
			}
			if id > 0 && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
			i = j - 1
		}
	}

	// Pattern 2: task claim \d+ or task \d+
	lower := strings.ToLower(content)
	patterns := []string{"task claim ", "task #", "task "}
	for _, p := range patterns {
		idx := 0
		for {
			pos := strings.Index(lower[idx:], p)
			if pos == -1 {
				break
			}
			start := idx + pos + len(p)
			var id int64
			j := start
			for j < len(lower) && lower[j] >= '0' && lower[j] <= '9' {
				id = id*10 + int64(lower[j]-'0')
				j++
			}
			if id > 0 && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
			idx = j
		}
	}

	return ids
}

func extractTaskIDString(content string) string {
	ids := extractTaskIDs(content)
	if len(ids) > 0 {
		return fmt.Sprintf("%d", ids[0])
	}
	return ""
}

func tokenize(s string, stopWords map[string]bool) []string {
	lower := strings.ToLower(s)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return r < 'a' || r > 'z'
	})
	var result []string
	for _, f := range fields {
		if len(f) > 2 && !stopWords[f] {
			result = append(result, f)
		}
	}
	return result
}

func jaccardSimilarity(a, b []string) (float64, []string) {
	setA := make(map[string]bool)
	for _, w := range a {
		setA[w] = true
	}
	setB := make(map[string]bool)
	for _, w := range b {
		setB[w] = true
	}

	intersection := 0
	var shared []string
	for w := range setA {
		if setB[w] {
			intersection++
			shared = append(shared, w)
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0, nil
	}
	return float64(intersection) / float64(union), shared
}
