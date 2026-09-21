package store

import (
	"context"
	"slices"
	"sort"
	"time"
)

// GoalMetricsRetention is how long goal generation rows are kept.
const GoalMetricsRetention = 14 * 24 * time.Hour

// Goal generation triggers.
const (
	GoalTriggerFirst    = "first"    // the agent's first answer
	GoalTriggerTurnEnd  = "turn_end" // a turn ended (transcript went quiet)
	GoalTriggerInterval = "interval" // a long turn hit the max interval
	GoalTriggerManual   = "manual"   // the operator pressed the sparkle
)

// Goal generation outcomes.
const (
	GoalOutcomeStored    = "stored"     // a new goal was written
	GoalOutcomeUnchanged = "unchanged"  // the model repeated the current goal
	GoalOutcomeUserGoal  = "user_goal"  // skipped: the operator's goal stands
	GoalOutcomeFailed    = "failed"     // the CLI exited with an error
	GoalOutcomeTimeout   = "timeout"    // the CLI ran past its timeout
	GoalOutcomeBadOutput = "bad_output" // the CLI answered with nothing usable
	GoalOutcomeNoCLI     = "no_cli"     // skipped: claude CLI not found
)

// GoalGeneration is one attempt to generate a session's goal.
type GoalGeneration struct {
	ID               int64   `db:"id" json:"id"`
	SessionID        string  `db:"session_id" json:"session_id"`
	AgentName        string  `db:"agent_name" json:"agent_name"`
	AgentType        string  `db:"agent_type" json:"agent_type"`
	Trigger          string  `db:"trigger" json:"trigger"`
	Outcome          string  `db:"outcome" json:"outcome"`
	Goal             string  `db:"goal" json:"goal,omitempty"`
	Error            string  `db:"error" json:"error,omitempty"`
	DurationMs       int64   `db:"duration_ms" json:"duration_ms"`
	InputTokens      int64   `db:"input_tokens" json:"input_tokens"`
	OutputTokens     int64   `db:"output_tokens" json:"output_tokens"`
	CacheReadTokens  int64   `db:"cache_read_tokens" json:"cache_read_tokens"`
	CacheWriteTokens int64   `db:"cache_write_tokens" json:"cache_write_tokens"`
	CostUSD          float64 `db:"cost_usd" json:"cost_usd"`
	TranscriptBytes  int64   `db:"transcript_bytes" json:"transcript_bytes"`
	CreatedAt        string  `db:"created_at" json:"created_at"`
}

// CalledCLI reports whether the attempt ran the CLI (and so cost money).
func (g GoalGeneration) CalledCLI() bool {
	switch g.Outcome {
	case GoalOutcomeUserGoal, GoalOutcomeNoCLI:
		return false
	}
	return true
}

// GoalMetricsStore records goal generation attempts.
type GoalMetricsStore struct {
	db *DB
}

// NewGoalMetricsStore creates a GoalMetricsStore.
func NewGoalMetricsStore(db *DB) *GoalMetricsStore {
	return &GoalMetricsStore{db: db}
}

// Record inserts one attempt. CreatedAt defaults to now.
func (s *GoalMetricsStore) Record(ctx context.Context, g *GoalGeneration) error {
	if g.CreatedAt == "" {
		g.CreatedAt = nowUTC()
	}
	res, err := s.db.NamedExecContext(ctx, `INSERT INTO goal_generations
		(session_id, agent_name, agent_type, trigger, outcome, goal, error, duration_ms,
		 input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd,
		 transcript_bytes, created_at)
		VALUES (:session_id, :agent_name, :agent_type, :trigger, :outcome, :goal, :error, :duration_ms,
		 :input_tokens, :output_tokens, :cache_read_tokens, :cache_write_tokens, :cost_usd,
		 :transcript_bytes, :created_at)`, g)
	if err != nil {
		return err
	}
	g.ID, _ = res.LastInsertId()
	return nil
}

// Since returns every attempt at or after since, oldest first.
func (s *GoalMetricsStore) Since(ctx context.Context, since time.Time) ([]GoalGeneration, error) {
	var rows []GoalGeneration
	err := s.db.SelectContext(ctx, &rows,
		`SELECT * FROM goal_generations WHERE created_at >= ? ORDER BY created_at, id`,
		since.UTC().Format(ISOFormat))
	return rows, err
}

// Prune deletes attempts older than before.
func (s *GoalMetricsStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM goal_generations WHERE created_at < ?`,
		before.UTC().Format(ISOFormat))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GoalMetrics summarizes goal generation over a window.
type GoalMetrics struct {
	WindowHours float64 `json:"window_hours"`
	Since       string  `json:"since"`

	Attempts      int            `json:"attempts"`        // every recorded attempt
	CLICalls      int            `json:"cli_calls"`       // attempts that ran the CLI
	CLICallsPerHr float64        `json:"cli_calls_per_hour"`
	ByTrigger     map[string]int `json:"by_trigger"`
	ByOutcome     map[string]int `json:"by_outcome"`
	FailureRate   float64        `json:"failure_rate"` // failed+timeout+bad_output / CLI calls

	CostUSD          float64 `json:"cost_usd"`
	AvgCostUSD       float64 `json:"avg_cost_usd"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`

	AvgDurationMs int64 `json:"avg_duration_ms"`
	P95DurationMs int64 `json:"p95_duration_ms"`
	MaxDurationMs int64 `json:"max_duration_ms"`

	Sessions       []GoalSessionMetrics `json:"sessions"`
	RecentFailures []GoalGeneration     `json:"recent_failures"`
	Recent         []GoalGeneration     `json:"recent"`
}

// GoalSessionMetrics is one session's share of a GoalMetrics window.
type GoalSessionMetrics struct {
	SessionID     string         `json:"session_id"`
	AgentName     string         `json:"agent_name"`
	AgentType     string         `json:"agent_type"`
	Attempts      int            `json:"attempts"`
	CLICalls      int            `json:"cli_calls"`
	CLICallsPerHr float64        `json:"cli_calls_per_hour"`
	ByOutcome     map[string]int `json:"by_outcome"`
	CostUSD       float64        `json:"cost_usd"`
	LastAt        string         `json:"last_at"`
	LastOutcome   string         `json:"last_outcome"`
	LastGoal      string         `json:"last_goal,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
}

// IsGoalFailure reports whether an outcome counts toward the failure rate.
func IsGoalFailure(outcome string) bool {
	return outcome == GoalOutcomeFailed || outcome == GoalOutcomeTimeout || outcome == GoalOutcomeBadOutput
}

// SummarizeGoalGenerations aggregates rows (oldest first) over a window of
// the given length ending now.
func SummarizeGoalGenerations(rows []GoalGeneration, since time.Time, window time.Duration) GoalMetrics {
	hours := window.Hours()
	m := GoalMetrics{
		WindowHours:    hours,
		Since:          since.UTC().Format(ISOFormat),
		ByTrigger:      map[string]int{},
		ByOutcome:      map[string]int{},
		Sessions:       []GoalSessionMetrics{},
		RecentFailures: []GoalGeneration{},
		Recent:         []GoalGeneration{},
	}
	perSession := map[string]*GoalSessionMetrics{}
	var durations []int64
	var failures int
	for _, r := range rows {
		m.Attempts++
		m.ByTrigger[r.Trigger]++
		m.ByOutcome[r.Outcome]++
		sm := perSession[r.SessionID]
		if sm == nil {
			sm = &GoalSessionMetrics{SessionID: r.SessionID, ByOutcome: map[string]int{}}
			perSession[r.SessionID] = sm
		}
		sm.AgentName, sm.AgentType = r.AgentName, r.AgentType
		sm.Attempts++
		sm.ByOutcome[r.Outcome]++
		sm.LastAt, sm.LastOutcome = r.CreatedAt, r.Outcome
		if r.Goal != "" {
			sm.LastGoal = r.Goal
		}
		if r.Error != "" {
			sm.LastError = r.Error
		}
		if !r.CalledCLI() {
			continue
		}
		m.CLICalls++
		sm.CLICalls++
		m.CostUSD += r.CostUSD
		sm.CostUSD += r.CostUSD
		m.InputTokens += r.InputTokens
		m.OutputTokens += r.OutputTokens
		m.CacheReadTokens += r.CacheReadTokens
		m.CacheWriteTokens += r.CacheWriteTokens
		durations = append(durations, r.DurationMs)
		if IsGoalFailure(r.Outcome) {
			failures++
		}
	}
	if m.CLICalls > 0 {
		m.FailureRate = float64(failures) / float64(m.CLICalls)
		m.AvgCostUSD = m.CostUSD / float64(m.CLICalls)
	}
	if hours > 0 {
		m.CLICallsPerHr = float64(m.CLICalls) / hours
	}
	if len(durations) > 0 {
		var sum int64
		for _, d := range durations {
			sum += d
		}
		m.AvgDurationMs = sum / int64(len(durations))
		slices.Sort(durations)
		m.P95DurationMs = durations[(len(durations)*95+99)/100-1]
		m.MaxDurationMs = durations[len(durations)-1]
	}
	for _, sm := range perSession {
		if hours > 0 {
			sm.CLICallsPerHr = float64(sm.CLICalls) / hours
		}
		m.Sessions = append(m.Sessions, *sm)
	}
	sort.Slice(m.Sessions, func(i, j int) bool {
		if m.Sessions[i].CLICalls != m.Sessions[j].CLICalls {
			return m.Sessions[i].CLICalls > m.Sessions[j].CLICalls
		}
		return m.Sessions[i].SessionID < m.Sessions[j].SessionID
	})
	for i := len(rows) - 1; i >= 0; i-- {
		if len(m.Recent) < 25 {
			m.Recent = append(m.Recent, rows[i])
		}
		if IsGoalFailure(rows[i].Outcome) && len(m.RecentFailures) < 20 {
			m.RecentFailures = append(m.RecentFailures, rows[i])
		}
	}
	return m
}
