package coordination

import (
	"time"
)

// Task represents a task for coordination reporting.
type Task struct {
	ID                int64         `json:"id"`
	BoardID           string        `json:"board_id"`
	Title             string        `json:"title"`
	Body              *string       `json:"body,omitempty"`
	Status            string        `json:"status"`
	Priority          string        `json:"priority"`
	CreatedBy         string        `json:"created_by"`
	AssignedTo        *string       `json:"assigned_to"`
	CompletedBy       *string       `json:"completed_by"`
	CompletionMessage *string       `json:"completion_message,omitempty"`
	CreatedAt         string        `json:"created_at"`
	ClaimedAt         *string       `json:"claimed_at,omitempty"`
	CompletedAt       *string       `json:"completed_at,omitempty"`
	BlockedBy         []TaskDep     `json:"blocked_by,omitempty"`
	Workflow          *TaskWorkflow `json:"workflow,omitempty"`
}

type TaskDep struct {
	TaskID    int64  `json:"task_id"`
	BoardID   string `json:"board_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Condition string `json:"condition,omitempty"`
}

type TaskWorkflow struct {
	RetryOf          int64             `json:"retry_of,omitempty"`
	CompletionReview *CompletionReview `json:"completion_review,omitempty"`
	Instructions     string            `json:"instructions,omitempty"`
	Outcome          string            `json:"outcome,omitempty"`
}

type CompletionReview struct {
	ReviewerRole string `json:"reviewer_role"`
	Status       string `json:"status"`
}

// Message represents a message on the board.
type Message struct {
	ID           int64  `json:"id"`
	Project      string `json:"project"`
	SubscriberID string `json:"subscriber_id"`
	Content      string `json:"content"`
	CreatedAt    string `json:"created_at"`
	JobTitle     string `json:"job_title,omitempty"`
}

// SubscriberStatus represents an agent subscriber's live availability.
type SubscriberStatus struct {
	SubscriberID string    `json:"subscriber_id"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	Availability string    `json:"availability"`
	Available    bool      `json:"available"`
	Reason       string    `json:"reason"`
	TaskState    string    `json:"task_state,omitempty"`
	Tasks        []TaskRef `json:"tasks,omitempty"`
}

type TaskRef struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type BoardStatus struct {
	Board           string             `json:"board"`
	Agents          []SubscriberStatus `json:"agents"`
	HealthReport    []string           `json:"health_report"`
	ObservedAt      string             `json:"observed_at"`
	UnassignedTasks []Task             `json:"unassigned_tasks,omitempty"`
}

// ReportFilter defines query parameters.
type ReportFilter struct {
	Board           string
	Since           *time.Time
	Until           *time.Time
	InactivityLimit time.Duration
	ServerURL       string
	DatabasePath    string
}

// DurationStats aggregates sample timing.
type DurationStats struct {
	Count   int     `json:"count"`
	MinSec  float64 `json:"min_seconds"`
	MaxSec  float64 `json:"max_seconds"`
	MeanSec float64 `json:"mean_seconds"`
	MedSec  float64 `json:"median_seconds"`
	P90Sec  float64 `json:"p90_seconds"`
}

// Report contains the complete analysis separating facts, heuristics, and unavailable data.
type Report struct {
	Metadata           ReportMetadata        `json:"metadata"`
	ObservableFacts    ObservableFacts       `json:"observable_facts"`
	HeuristicSignals   HeuristicSignals      `json:"heuristic_signals"`
	UnavailableMetrics []string              `json:"unavailable_metrics"`
	CoordinationScore  CoordinationScorecard `json:"coordination_scorecard"`
	KeyFindings        []string              `json:"key_findings"`
	Recommendations    []string              `json:"recommendations"`
}

type ReportMetadata struct {
	Board                 string     `json:"board"`
	GeneratedAt           time.Time  `json:"generated_at"`
	WindowStart           *time.Time `json:"window_start,omitempty"`
	WindowEnd             *time.Time `json:"window_end,omitempty"`
	TotalTasksInWindow    int        `json:"total_tasks_in_window"`
	TotalMessagesInWindow int        `json:"total_messages_in_window"`
	SubscribersTracked    int        `json:"subscribers_tracked"`
}

type ObservableFacts struct {
	TaskCounts          TaskCounts        `json:"task_counts"`
	Durations           TimingMetrics     `json:"durations"`
	ActiveVsPlannedLoad ActiveLoadMetrics `json:"active_vs_planned_load"`
	DependencyGraph     DependencyMetrics `json:"dependency_graph"`
	ReviewHeldSlots     []ReviewHeldTask  `json:"review_held_slots"`
}

type TaskCounts struct {
	TotalCreated int `json:"total_created"`
	Completed    int `json:"completed"`
	InProgress   int `json:"in_progress"`
	Pending      int `json:"pending"`
	Blocked      int `json:"blocked"`
	Cancelled    int `json:"cancelled"`
	Skipped      int `json:"skipped"`
}

type TimingMetrics struct {
	ClaimLatency        DurationStats `json:"claim_latency"`          // Kept for compatibility: CreatedAt to ClaimedAt
	ReadyToClaimLatency DurationStats `json:"ready_to_claim_latency"` // Unblocked/Ready to ClaimedAt (true pickup responsiveness)
	TotalPreClaimWait   DurationStats `json:"total_pre_claim_wait"`   // CreatedAt to ClaimedAt (explicitly includes prerequisite blocking time)
	ExecutionDuration   DurationStats `json:"execution_duration"`     // ClaimedAt to CompletedAt
	TotalLeadTime       DurationStats `json:"total_lead_time"`        // CreatedAt to CompletedAt
	MissingTimestamps   int           `json:"missing_timestamps_count"`
}

type ActiveLoadMetrics struct {
	ActiveInProgressTasks  int              `json:"active_in_progress_tasks"`
	PlannedAssignedTasks   int              `json:"planned_assigned_tasks"`
	UnassignedPendingTasks int              `json:"unassigned_pending_tasks"`
	BoardWideActiveTasks   int              `json:"board_wide_active_tasks"`
	BoardWidePendingTasks  int              `json:"board_wide_pending_tasks"`
	CarryoverOlderTasks    []CarryoverTask  `json:"carryover_older_tasks,omitempty"`
	SubscriberLoad         []SubscriberLoad `json:"subscriber_load"`
}

type CarryoverTask struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	Assignee  string `json:"assignee"`
	CreatedAt string `json:"created_at"`
}

type SubscriberLoad struct {
	SubscriberID string `json:"subscriber_id"`
	Role         string `json:"role"`
	Availability string `json:"availability"`
	ActiveTaskID int64  `json:"active_task_id,omitempty"`
	QueuedCount  int    `json:"queued_count"`
}

type DependencyMetrics struct {
	TotalBlockedTasks int           `json:"total_blocked_tasks"`
	BlockedTasks      []BlockedTask `json:"blocked_tasks"`
}

type BlockedTask struct {
	TaskID       int64              `json:"task_id"`
	Title        string             `json:"title"`
	Assignee     string             `json:"assignee"`
	Dependencies []DependencyStatus `json:"dependencies"`
}

type DependencyStatus struct {
	PrerequisiteID int64  `json:"prerequisite_id"`
	CurrentStatus  string `json:"current_status"`
	Condition      string `json:"condition"`
	Satisfied      bool   `json:"satisfied"`
}

type ReviewHeldTask struct {
	TaskID       int64   `json:"task_id"`
	Title        string  `json:"title"`
	Assignee     string  `json:"assignee"`
	ReviewerRole string  `json:"reviewer_role"`
	DurationSec  float64 `json:"duration_seconds"`
}

type HeuristicSignals struct {
	ReadyUnclaimed           ReadyUnclaimedAnalysis      `json:"ready_unclaimed_analysis"`
	ActiveWithoutProgress    []ActiveWithoutProgress     `json:"active_without_progress_candidates"`
	NotificationChurn        []NotificationChurnIncident `json:"notification_churn_incidents"`
	ManualRecoveryEvents     []ManualRecoveryEvent       `json:"manual_recovery_events"`
	DuplicateScopeCandidates []DuplicateScopeCandidate   `json:"duplicate_scope_candidates"`
	RepetitiveHandoffLoops   []RepetitiveHandoffLoop     `json:"repetitive_handoff_loops"`
}

type ReadyUnclaimedAnalysis struct {
	// Legitimately waiting because owner is currently busy with another active task
	OwnerBusyCount int               `json:"owner_busy_count"`
	OwnerBusyTasks []ReadyTaskDetail `json:"owner_busy_tasks"`

	// Ready to claim and owner is idle/available — potential coordination lag
	OwnerIdleCount int               `json:"owner_idle_count"`
	OwnerIdleTasks []ReadyTaskDetail `json:"owner_idle_tasks"`

	// Unassigned ready tasks
	UnassignedCount int               `json:"unassigned_count"`
	UnassignedTasks []ReadyTaskDetail `json:"unassigned_tasks"`
}

type ReadyTaskDetail struct {
	TaskID        int64   `json:"task_id"`
	Title         string  `json:"title"`
	Priority      string  `json:"priority"`
	Assignee      string  `json:"assignee"`
	AgeSeconds    float64 `json:"age_seconds"`
	OwnerStatus   string  `json:"owner_status"`
	OwnerActiveID int64   `json:"owner_active_id,omitempty"`
}

type ActiveWithoutProgress struct {
	TaskID          int64      `json:"task_id"`
	Title           string     `json:"title"`
	Assignee        string     `json:"assignee"`
	ClaimedAt       time.Time  `json:"claimed_at"`
	DurationSeconds float64    `json:"duration_seconds"`
	LastMessageAt   *time.Time `json:"last_message_at,omitempty"`
	ConfidenceNote  string     `json:"confidence_note"`
}

type NotificationChurnIncident struct {
	TaskID                 int64      `json:"task_id"`
	Assignee               string     `json:"assignee"`
	NoticeCount            int        `json:"notice_count"`
	TimeSpanSec            float64    `json:"timespan_seconds"`
	StaleInformationAgeSec float64    `json:"stale_information_age_seconds,omitempty"` // Message timestamp minus claim timestamp
	FirstNoticeAt          time.Time  `json:"first_notice_at"`
	LastNoticeAt           time.Time  `json:"last_notice_at"`
	ChurnType              string     `json:"churn_type"` // e.g. "rapid_nudging", "stale_unclaimed_reminder", "repeated_notices"
	SampleMessage          string     `json:"sample_message"`
	Sender                 string     `json:"sender,omitempty"`
	ClaimedAt              *time.Time `json:"claimed_at,omitempty"`
	Confidence             string     `json:"confidence,omitempty"`
	AnalysisNote           string     `json:"analysis_note,omitempty"`
}

type ManualRecoveryEvent struct {
	TaskID      int64     `json:"task_id"`
	Title       string    `json:"title"`
	EventType   string    `json:"event_type"` // "cancellation", "reassignment", "retry"
	Actor       string    `json:"actor"`
	Timestamp   time.Time `json:"timestamp"`
	Reason      string    `json:"reason,omitempty"`
	Predecessor int64     `json:"predecessor_id,omitempty"`
}

type DuplicateScopeCandidate struct {
	TaskID1     int64     `json:"task_id_1"`
	Title1      string    `json:"title_1"`
	TaskID2     int64     `json:"task_id_2"`
	Title2      string    `json:"title_2"`
	Similarity  float64   `json:"similarity"`
	CreatedAt1  time.Time `json:"created_at_1"`
	CreatedAt2  time.Time `json:"created_at_2"`
	SharedWords []string  `json:"shared_words"`
}

type RepetitiveHandoffLoop struct {
	Agents       []string  `json:"agents"`
	MessageCount int       `json:"message_count"`
	TimeSpanSec  float64   `json:"timespan_seconds"`
	Summary      string    `json:"summary"`
	FirstTime    time.Time `json:"first_time"`
	LastTime     time.Time `json:"last_time"`
}

type CoordinationScorecard struct {
	TotalScore             float64 `json:"total_score"`                        // 0 to 100 (0 when suppressed)
	ObservedPoints         float64 `json:"observed_points"`                    // Raw points supported by evidence
	AvailablePoints        float64 `json:"available_points"`                   // Maximum points supported by evidence
	CoverageRatio          float64 `json:"coverage_ratio"`                     // AvailablePoints / 100.0 (0.0 to 1.0)
	ScoreSuppressed        bool    `json:"score_suppressed"`                   // True when coverage is inadequate (< 100)
	SuppressionRationale   string  `json:"suppression_rationale,omitempty"`    // Explicit rationale explaining why aggregate score is suppressed
	ClaimPromptnessScore   float64 `json:"claim_promptness_score"`             // 0 to 25
	SlotFluidityScore      float64 `json:"slot_fluidity_score"`                // 0 to 25
	DependencyClarityScore float64 `json:"dependency_clarity_score"`           // 0 to 20
	NotificationCleanScore float64 `json:"notification_clean_score"`           // 0 to 15
	RecoveryEconomyScore   float64 `json:"recovery_economy_score"`             // 0 to 15
	ValidationStatus       string  `json:"validation_status"`                  // e.g. "Provisional / Pending Audit", "Suppressed / Incomplete Evidence"
	AuditCaveat            string  `json:"audit_caveat"`
	ScoringMethodology     string  `json:"scoring_methodology"`
}
