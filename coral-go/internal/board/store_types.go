// The board store's row types (moved out of store.go, #4998; no behaviour change).

package board

// Subscriber represents a board subscriber.
// SubscriberID is the stable identity (role/display name, e.g. "Orchestrator").
// SessionName is the current tmux/pty session name (mutable across restarts).
type Subscriber struct {
	ID           int64   `db:"id" json:"id"`
	Project      string  `db:"project" json:"project"`
	SubscriberID string  `db:"subscriber_id" json:"subscriber_id"`
	SessionName  string  `db:"session_name" json:"session_name,omitempty"`
	JobTitle     string  `db:"job_title" json:"job_title"`
	WebhookURL   *string `db:"webhook_url" json:"-"` // unused: webhooks were removed (#3646); kept for SELECT *, wiped at start
	OriginServer *string `db:"origin_server" json:"origin_server"`
	ReceiveMode  string  `db:"receive_mode" json:"receive_mode"`
	LastReadID   int64   `db:"last_read_id" json:"last_read_id"`
	SubscribedAt string  `db:"subscribed_at" json:"subscribed_at"`
	IsActive     int     `db:"is_active" json:"is_active"`
	CanPeek      int     `db:"can_peek" json:"can_peek"`
	// Legacy column — kept for DB compat, mirrors SubscriberID for new rows.
	SessionID string `db:"session_id" json:"-"`
	// SessionJoinedAt is when SessionName became this subscription's session (#4379): set when the
	// row is made, when another session takes the role over, and when it rejoins after leaving. NULL
	// on rows from before it existed (SubscribedAt stands in for them).
	SessionJoinedAt *string `db:"session_joined_at" json:"-"`
}

// GroupInfo holds group summary info.
type GroupInfo struct {
	GroupID     string `db:"group_id" json:"group_id"`
	MemberCount int    `db:"member_count" json:"member_count"`
}

// Task represents a board task.
type Task struct {
	ID                int64    `db:"id" json:"id"`
	Revision          int      `db:"revision" json:"revision"`
	BoardID           string   `db:"board_id" json:"board_id"`
	BoardLabel        string   `db:"-" json:"board_label,omitempty"` // filled where a list spans boards (#3536)
	Title             string   `db:"title" json:"title"`
	Body              *string  `db:"body" json:"body,omitempty"`
	Status            string   `db:"status" json:"status"`
	Priority          string   `db:"priority" json:"priority"`
	CreatedBy         string   `db:"created_by" json:"created_by"`
	AssignedTo        *string  `db:"assigned_to" json:"assigned_to"`
	CompletedBy       *string  `db:"completed_by" json:"completed_by"`
	CompletionMessage *string  `db:"completion_message" json:"completion_message,omitempty"`
	CreatedAt         string   `db:"created_at" json:"created_at"`
	ClaimedAt         *string  `db:"claimed_at" json:"claimed_at,omitempty"`
	CompletedAt       *string  `db:"completed_at" json:"completed_at,omitempty"`
	SessionID         *string  `db:"session_id" json:"session_id,omitempty"`
	CostUSD           *float64 `db:"cost_usd" json:"cost_usd,omitempty"`
	InputTokens       *int     `db:"input_tokens" json:"input_tokens,omitempty"`
	OutputTokens      *int     `db:"output_tokens" json:"output_tokens,omitempty"`
	CacheReadTokens   *int     `db:"cache_read_tokens" json:"cache_read_tokens,omitempty"`
	CacheWriteTokens  *int     `db:"cache_write_tokens" json:"cache_write_tokens,omitempty"`
	// ParkedReason and ParkedUntil: a parked task (#4175 step 3, park.go). Read where the health
	// rules and the task views need them; nil elsewhere.
	ParkedReason *string `db:"parked_reason" json:"parked_reason,omitempty"`
	ParkedUntil  *string `db:"parked_until" json:"parked_until,omitempty"`
	// RemindAt: a one-time reminder of the task at that time (task add --remind-at, remind.go).
	RemindAt *string `db:"remind_at" json:"remind_at,omitempty"`
	// GoalID is the goal of this board the task works toward (#7087), or nil.
	GoalID    *int64       `db:"goal_id" json:"goal_id,omitempty"`
	BlockedBy []TaskDep    `db:"-" json:"blocked_by,omitempty"`
	Workflow  TaskWorkflow `db:"-" json:"workflow"`
	// Verification: whether a completed task's "done" is backed by an independent check (#4563); set when the workflow is
	// attached, for a completed task with outcome success only.
	Verification *Verification `db:"-" json:"verification,omitempty"`
}

type TaskDep struct {
	Satisfied         bool     `json:"satisfied"`
	Outcome           string   `json:"outcome,omitempty"`
	MissingArtifacts  []string `json:"missing_artifacts,omitempty"`
	BlockedReason     string   `json:"blocked_reason,omitempty"`
	TaskID            int64    `json:"task_id"`
	BoardID           string   `json:"board_id"`
	Title             string   `json:"title,omitempty"`
	Status            string   `json:"status,omitempty"`
	Condition         string   `json:"condition,omitempty"`
	RequiredArtifacts []string `json:"required_artifacts,omitempty"`
}

// Message represents a board message.
// SubscriberID is the stable poster identity (role name).
type Message struct {
	ID            int64   `db:"id" json:"id"`
	Project       string  `db:"project" json:"project"`
	SubscriberID  string  `db:"subscriber_id" json:"subscriber_id"`
	Content       string  `db:"content" json:"content"`
	CreatedAt     string  `db:"created_at" json:"created_at"`
	JobTitle      string  `db:"job_title" json:"job_title,omitempty"`
	TargetGroupID *string `db:"target_group_id" json:"target_group_id,omitempty"`
	// External is the origin envelope of a cross-team message (inbound on the
	// receiving board, outbound on the sender's mirror copy); absent otherwise.
	External RawJSON `db:"external_json" json:"external,omitempty"`
	// Origin is set by the server only: "hub_user" for a chat post a Coral Hub user made
	// ("hub_operator" when made as the Operator, #3404)
	// through the tunnel (#3377). The dashboard badges those; never inferred from a name.
	Origin *string `db:"origin" json:"origin,omitempty"`
	// HubUser is the Coral Hub user (hub-user-<8 hex>) behind a post with origin hub_user
	// or hub_operator (the Operator through Coral Hub), from the tunnel-stamped header the
	// server validated (#3404). Set by the server only.
	HubUser *string `db:"hub_user" json:"hub_user,omitempty"`
	// IsSystem is set by the server only, for Coral's own posts (the task queue, the
	// health monitor), never inferred from the sender's name (#3387). The chat
	// collapses these (#3343).
	IsSystem bool `db:"is_system" json:"is_system"`
	// TaskID is the task this message replies to (POST .../tasks/{id}/reply, #5463): set by the server only, absent for every other message.
	TaskID *int64 `db:"task_id" json:"task_id,omitempty"`
	// Legacy column — kept for DB compat, mirrors SubscriberID for new rows.
	SessionID string `db:"session_id" json:"-"`
}

// ProjectInfo holds project summary info.
type ProjectInfo struct {
	Project         string `db:"project" json:"project"`
	BoardID         string `db:"-" json:"board_id"`            // the same value as project (the board's id); the name the hub's board model uses (#5236)
	CreatedAt       string `db:"created_at" json:"created_at"` // the boards row's created_at, RFC3339 UTC; "" for a board with no row (none after a start, which makes one for every board in use)
	BoardLabel      string `db:"-" json:"board_label"`         // the board's display name (#3536)
	SubscriberCount int    `db:"subscriber_count" json:"subscriber_count"`
	MessageCount    int    `db:"message_count" json:"message_count"`
	// GoalsBrief is what the feed carries of the board's goals, with no text (#7116): how many are open and the board's change counter. A Coral Hub viewer who can only look gets it as null (viewdrop.go).
	Goals     *GoalsBrief `db:"-" json:"goals_brief"`
	GoalsOpen int         `db:"goals_open" json:"-"`
	GoalsSeq  int64       `db:"goals_seq" json:"-"`
}

// GoalsBrief is a board's goals as the feed counts them: open (proposed, active or blocked) and the change counter, which moves on every goal write and every task change touching a goal.
type GoalsBrief struct {
	Open int   `json:"open"`
	Seq  int64 `json:"seq"`
}

type SubscriberReminder struct {
	Project         string `db:"project"`
	SubscriberID    string `db:"subscriber_id"`
	Message         string `db:"message"`
	IntervalSeconds int    `db:"interval_seconds"`
}
