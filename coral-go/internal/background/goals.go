package background

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/executil"
	"github.com/cdknorow/coral/internal/jsonl"
	"github.com/cdknorow/coral/internal/store"
)

// GoalEventType is the agent_events type that carries a session's goal line.
// Goals from every source share it; the newest one is shown.
const GoalEventType = "goal"

// Goal sources, stored in the goal event's detail_json.
const (
	GoalSourceAuto = "auto" // written by the GoalGenerator
	GoalSourceUser = "user" // typed by the operator; never overwritten
)

// GoalSettingKey disables the generator when set to "false".
const GoalSettingKey = "auto_goals"

const (
	// goalQuietPeriod is how long the transcript must be still before the
	// turn counts as ended.
	goalQuietPeriod = 20 * time.Second
	// goalMinInterval spaces refreshes when turns end in quick succession.
	goalMinInterval = 2 * time.Minute
	// goalMaxInterval refreshes a long-running turn that never goes quiet.
	goalMaxInterval = 5 * time.Minute
	// goalFailureBackoff pauses a session after the CLI fails.
	goalFailureBackoff = 10 * time.Minute
	// goalCLITimeout bounds one CLI call, including its startup.
	goalCLITimeout = 60 * time.Second
	// goalConcurrency caps CLI calls running at once.
	goalConcurrency = 2
	// goalTailChars is how much of the recent transcript the model reads.
	goalTailChars = 6000
	// goalTailBytes is how much of the transcript file is parsed to find it.
	goalTailBytes = 256 * 1024
	// goalMaxWords caps the stored goal even if the model runs long.
	goalMaxWords = 10
)

// goalPrompt is the CLI's whole system prompt. Replacing Claude Code's own
// system prompt and tools cuts a call from ~22K input tokens to a few hundred.
const goalPrompt = `You label a running AI coding agent for a dashboard sidebar. Read the agent's first instruction and the most recent part of its transcript, then reply with ONLY the agent's current goal.

Rules:
- At most 8 words. Imperative, present tense, e.g. "Fix flaky websocket reconnect test" or "Add dark mode to settings page".
- Describe what it is working toward right now, not everything it has done.
- If it is waiting on the user, say what for, e.g. "Waiting for approval to delete migration".
- No quotes, no trailing period, no preamble, no mention of the dashboard, the transcript or yourself.`

// goalState is the generator's in-memory view of one session.
type goalState struct {
	path        string    // transcript file, resolved once
	size        int64     // transcript size at the last generation
	generatedAt time.Time // last successful generation
	failedAt    time.Time // last CLI failure
	inFlight    bool
	force       bool   // operator asked for a refresh
	trigger     string // why the in-flight generation was started
}

// goalDue reports whether a session needs a new goal, and the trigger (a
// store.GoalTrigger* value) when it does. size and modAt describe the
// transcript now.
func goalDue(st goalState, size int64, modAt, now time.Time) (bool, string) {
	if st.inFlight {
		return false, ""
	}
	if st.force {
		return true, store.GoalTriggerManual
	}
	if !st.failedAt.IsZero() && now.Sub(st.failedAt) < goalFailureBackoff {
		return false, ""
	}
	if size <= st.size {
		return false, "" // nothing new since the last goal
	}
	if st.generatedAt.IsZero() {
		return true, store.GoalTriggerFirst
	}
	since := now.Sub(st.generatedAt)
	if now.Sub(modAt) >= goalQuietPeriod && since >= goalMinInterval {
		return true, store.GoalTriggerTurnEnd
	}
	if since >= goalMaxInterval {
		return true, store.GoalTriggerInterval
	}
	return false, ""
}

// goalCLIResult is one CLI answer with what it cost.
type goalCLIResult struct {
	Text             string
	CostUSD          float64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// GoalGenerator keeps a short, current goal line for every live agent. It
// reads the tail of the agent's transcript and asks a small model, through
// the claude CLI the user already has, for an 8-word goal. The agent itself
// is never prompted.
type GoalGenerator struct {
	sessionStore *store.SessionStore
	taskStore    *store.TaskStore
	reader       *jsonl.SessionReader
	interval     time.Duration
	logger       *slog.Logger

	// Swapped out in tests.
	now         func() time.Time
	resolvePath func(ls *store.LiveSession) string
	resolveCLI  func(settings map[string]string) string
	runCLI      func(ctx context.Context, bin, prompt string) (goalCLIResult, error)
	metrics     *store.GoalMetricsStore // nil: attempts are not recorded

	mu        sync.Mutex
	lastPrune time.Time
	states    map[string]*goalState
	noCLI     bool // logged once, not once per tick
	lastPoll  time.Time
	sem       chan struct{}
	wake      chan struct{}
	wg        sync.WaitGroup
}

// NewGoalGenerator creates a GoalGenerator that polls every interval.
func NewGoalGenerator(ss *store.SessionStore, ts *store.TaskStore, interval time.Duration) *GoalGenerator {
	return &GoalGenerator{
		sessionStore: ss,
		taskStore:    ts,
		reader:       jsonl.NewSessionReader(),
		interval:     interval,
		logger:       slog.Default().With("service", "goal_generator"),
		now:          time.Now,
		resolvePath: func(ls *store.LiveSession) string {
			return jsonl.TranscriptPath(ls.SessionID, ls.WorkingDir, ls.AgentType)
		},
		resolveCLI: resolveClaudeCLI,
		runCLI:     runGoalCLI,
		states:     make(map[string]*goalState),
		sem:        make(chan struct{}, goalConcurrency),
		wake:       make(chan struct{}, 1),
	}
}

// SetMetricsStore records every generation attempt for GET /api/goals/metrics.
func (g *GoalGenerator) SetMetricsStore(m *store.GoalMetricsStore) {
	g.metrics = m
}

// GoalGeneratorStatus is the generator's live state, for the metrics endpoint.
type GoalGeneratorStatus struct {
	Enabled         bool   `json:"enabled"`
	CLIFound        bool   `json:"cli_found"`
	TrackedSessions int    `json:"tracked_sessions"`
	InFlight        int    `json:"in_flight"`
	BackingOff      int    `json:"backing_off"`
	Concurrency     int    `json:"concurrency"`
	LastPoll        string `json:"last_poll,omitempty"`
}

// Status reports the generator's live state.
func (g *GoalGenerator) Status(ctx context.Context) GoalGeneratorStatus {
	settings, _ := g.sessionStore.GetSettings(ctx)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	st := GoalGeneratorStatus{
		Enabled:         settings[GoalSettingKey] != "false",
		CLIFound:        !g.noCLI,
		TrackedSessions: len(g.states),
		Concurrency:     cap(g.sem),
	}
	if !g.lastPoll.IsZero() {
		st.LastPoll = g.lastPoll.UTC().Format(store.ISOFormat)
	}
	for _, s := range g.states {
		if s.inFlight {
			st.InFlight++
		}
		if !s.failedAt.IsZero() && now.Sub(s.failedAt) < goalFailureBackoff {
			st.BackingOff++
		}
	}
	return st
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (g *GoalGenerator) Run(ctx context.Context) error {
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		g.pollOnce(ctx)
		select {
		case <-ctx.Done():
			g.wg.Wait()
			return ctx.Err()
		case <-ticker.C:
		case <-g.wake:
		}
	}
}

// RequestGoal asks for a fresh goal for a session on the next poll, which
// runs right away. The interval and backoff are bypassed; a goal the operator
// typed is still never replaced.
func (g *GoalGenerator) RequestGoal(sessionID string) {
	g.mu.Lock()
	st := g.states[sessionID]
	if st == nil {
		st = &goalState{}
		g.states[sessionID] = st
	}
	st.force = true
	g.mu.Unlock()
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *GoalGenerator) pollOnce(ctx context.Context) {
	g.pruneMetrics(ctx)
	g.mu.Lock()
	g.lastPoll = g.now()
	g.mu.Unlock()
	settings, _ := g.sessionStore.GetSettings(ctx)
	if settings[GoalSettingKey] == "false" {
		return
	}
	sessions, err := g.sessionStore.GetAllLiveSessions(ctx)
	if err != nil {
		g.logger.Error("failed to list live sessions", "error", err)
		return
	}

	live := make(map[string]bool, len(sessions))
	var due []store.LiveSession
	for i := range sessions {
		ls := sessions[i]
		live[ls.SessionID] = true
		if ls.AgentType == at.Terminal || ls.IsSleeping == 1 {
			continue
		}
		g.mu.Lock()
		st := g.states[ls.SessionID]
		if st == nil {
			st = &goalState{}
			g.states[ls.SessionID] = st
		}
		if st.path == "" {
			// The transcript appears only once the agent has written to it.
			st.path = g.resolvePath(&ls)
		}
		path := st.path
		g.mu.Unlock()
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		g.mu.Lock()
		if ok, trigger := goalDue(*st, info.Size(), info.ModTime(), g.now()); ok {
			st.inFlight = true
			st.force = false
			st.trigger = trigger
			due = append(due, ls)
		}
		g.mu.Unlock()
	}

	g.mu.Lock()
	for id := range g.states {
		if !live[id] {
			delete(g.states, id)
			g.reader.ClearSession(id)
		}
	}
	g.mu.Unlock()

	if len(due) == 0 {
		return
	}
	bin := g.resolveCLI(settings)
	if bin == "" {
		g.mu.Lock()
		first := !g.noCLI
		if first {
			g.logger.Warn("claude CLI not found; agent goals fall back to the first prompt")
			g.noCLI = true
		}
		var trigger string
		for _, ls := range due {
			st := g.states[ls.SessionID]
			st.inFlight = false
			if trigger == "" {
				trigger = st.trigger
			}
		}
		g.mu.Unlock()
		if first {
			// Once per outage, not once per tick.
			ls := due[0]
			g.record(ctx, &store.GoalGeneration{SessionID: ls.SessionID, AgentName: ls.AgentName, AgentType: ls.AgentType,
				Trigger: trigger, Outcome: store.GoalOutcomeNoCLI, Error: "claude CLI not found"})
		}
		return
	}
	g.mu.Lock()
	g.noCLI = false
	g.mu.Unlock()

	for _, ls := range due {
		ls := ls
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			select {
			case g.sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-g.sem }()
			g.generate(ctx, bin, &ls)
		}()
	}
}

// generate produces and stores one goal, and records the attempt. It
// always clears inFlight.
func (g *GoalGenerator) generate(ctx context.Context, bin string, ls *store.LiveSession) {
	g.mu.Lock()
	var path, trigger string
	if st := g.states[ls.SessionID]; st != nil {
		path, trigger = st.path, st.trigger
	}
	g.mu.Unlock()
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	attempt := &store.GoalGeneration{
		SessionID: ls.SessionID, AgentName: ls.AgentName, AgentType: ls.AgentType,
		Trigger: trigger, TranscriptBytes: size,
	}
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		st := g.states[ls.SessionID]
		if st == nil {
			return
		}
		st.inFlight = false
		st.size = size
		switch {
		case attempt.Outcome == "":
			// Not attempted: no transcript to read, or no answer yet.
		case store.IsGoalFailure(attempt.Outcome):
			st.failedAt = g.now()
		default:
			st.generatedAt = g.now()
			st.failedAt = time.Time{}
		}
	}()

	current, source := g.latestGoal(ctx, ls.SessionID)
	if source == GoalSourceUser {
		// The operator's goal stands; check again when there is more work.
		attempt.Outcome = store.GoalOutcomeUserGoal
		g.record(ctx, attempt)
		return
	}

	messages, err := jsonl.ReadTail(path, ls.AgentType, goalTailBytes)
	if err != nil {
		return
	}
	first := g.reader.FirstUserPrompt(ls.SessionID, ls.WorkingDir, ls.AgentType)
	tailFirst, tail, hasAssistant := condenseForGoal(messages, goalTailChars)
	if first == "" {
		first = tailFirst
	}
	first = truncateRunes(first, 1500)
	if !hasAssistant {
		size = 0 // nothing to describe yet; retry once the agent answers
		return
	}

	prompt := fmt.Sprintf("FIRST INSTRUCTION:\n%s\n\nRECENT TRANSCRIPT:\n%s", first, tail)
	cctx, cancel := context.WithTimeout(ctx, goalCLITimeout)
	defer cancel()
	start := time.Now()
	res, err := g.runCLI(cctx, bin, prompt)
	attempt.DurationMs = time.Since(start).Milliseconds()
	attempt.CostUSD = res.CostUSD
	attempt.InputTokens, attempt.OutputTokens = res.InputTokens, res.OutputTokens
	attempt.CacheReadTokens, attempt.CacheWriteTokens = res.CacheReadTokens, res.CacheWriteTokens
	defer g.record(ctx, attempt)
	if err != nil {
		attempt.Outcome = store.GoalOutcomeFailed
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			attempt.Outcome = store.GoalOutcomeTimeout
		}
		attempt.Error = truncateRunes(err.Error(), 500)
		g.logger.Warn("goal generation failed", "session_id", ls.SessionID, "outcome", attempt.Outcome, "error", err)
		return
	}
	goal := cleanGoal(res.Text)
	if goal == "" {
		attempt.Outcome = store.GoalOutcomeBadOutput
		attempt.Error = truncateRunes("unusable reply: "+strings.TrimSpace(res.Text), 500)
		return
	}
	attempt.Goal = goal
	if goal == current {
		attempt.Outcome = store.GoalOutcomeUnchanged
		return
	}
	attempt.Outcome = store.GoalOutcomeStored
	detail := fmt.Sprintf(`{"source":%q}`, GoalSourceAuto)
	sid := ls.SessionID
	if _, err := g.taskStore.InsertAgentEvent(ctx, &store.AgentEvent{
		AgentName:  ls.AgentName,
		SessionID:  &sid,
		EventType:  GoalEventType,
		Summary:    goal,
		DetailJSON: &detail,
	}); err != nil {
		attempt.Outcome = store.GoalOutcomeFailed
		attempt.Error = "store goal: " + err.Error()
		g.logger.Error("failed to store goal", "session_id", ls.SessionID, "error", err)
	}
}

// record saves one attempt for the metrics endpoint.
func (g *GoalGenerator) record(ctx context.Context, a *store.GoalGeneration) {
	if g.metrics == nil {
		return
	}
	if err := g.metrics.Record(context.WithoutCancel(ctx), a); err != nil {
		g.logger.Error("failed to record goal metrics", "error", err)
	}
}

// pruneMetrics drops attempts past the retention window, at most hourly.
func (g *GoalGenerator) pruneMetrics(ctx context.Context) {
	if g.metrics == nil {
		return
	}
	g.mu.Lock()
	due := g.now().Sub(g.lastPrune) >= time.Hour
	if due {
		g.lastPrune = g.now()
	}
	g.mu.Unlock()
	if due {
		if _, err := g.metrics.Prune(ctx, g.now().Add(-store.GoalMetricsRetention)); err != nil {
			g.logger.Error("failed to prune goal metrics", "error", err)
		}
	}
}

// latestGoal returns the session's newest goal and its source ("" for goals
// that predate sources or came from the agent's own PULSE line).
func (g *GoalGenerator) latestGoal(ctx context.Context, sessionID string) (goal, source string) {
	ev, err := g.taskStore.GetLatestGoalEvent(ctx, sessionID)
	if err != nil || ev == nil {
		return "", ""
	}
	return ev.Summary, GoalSource(ev.DetailJSON)
}

// GoalSource reads the source out of a goal event's detail_json.
func GoalSource(detailJSON *string) string {
	if detailJSON == nil {
		return ""
	}
	var d struct {
		Source string `json:"source"`
	}
	if json.Unmarshal([]byte(*detailJSON), &d) != nil {
		return ""
	}
	return d.Source
}

// condenseForGoal renders parsed transcript messages as the model's input:
// the first user prompt, and the last maxChars of the conversation with tool
// calls reduced to one line each. Tool output is left out; it is long and
// rarely says what the agent is for.
func condenseForGoal(messages []map[string]any, maxChars int) (first, tail string, hasAssistant bool) {
	var parts []string
	for _, m := range messages {
		switch m["type"] {
		case "user":
			c, _ := m["content"].(string)
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if first == "" {
				first = truncateRunes(c, 1500)
			}
			parts = append(parts, "User: "+truncateRunes(c, 1500))
		case "assistant":
			hasAssistant = true
			if t, _ := m["text"].(string); strings.TrimSpace(t) != "" {
				parts = append(parts, "Assistant: "+truncateRunes(strings.TrimSpace(t), 1500))
			}
			tools, _ := m["tool_uses"].([]map[string]any)
			for _, tu := range tools {
				name, _ := tu["name"].(string)
				in, _ := tu["input_summary"].(string)
				parts = append(parts, strings.TrimSpace("Tool: "+name+" "+truncateRunes(in, 200)))
			}
		}
	}
	tail = strings.Join(parts, "\n")
	if r := []rune(tail); len(r) > maxChars {
		tail = "..." + string(r[len(r)-maxChars:])
	}
	return first, tail, hasAssistant
}

// cleanGoal keeps the first non-empty line of the model's reply, strips
// wrapping quotes, labels and a trailing period, and caps its length.
func cleanGoal(out string) string {
	var line string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	for _, p := range []string{"Goal:", "goal:", "GOAL:"} {
		line = strings.TrimSpace(strings.TrimPrefix(line, p))
	}
	line = strings.Trim(line, "\"'`*“”")
	line = strings.TrimRight(strings.TrimSpace(line), ".")
	words := strings.Fields(line)
	if len(words) > goalMaxWords {
		words = words[:goalMaxWords]
	}
	return truncateRunes(strings.Join(words, " "), 90)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// resolveClaudeCLI finds the claude binary the way launch does: the
// cli_path_claude setting, then PATH, then common install locations.
func resolveClaudeCLI(settings map[string]string) string {
	if p := strings.TrimSpace(settings[agent.CLIPathSettingKey(at.Claude)]); p != "" {
		return p
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return agent.FindCLIInCommonPaths("claude")
}

// runGoalCLI asks the claude CLI for a goal with the cheapest model. The
// goal rules replace Claude Code's system prompt, and tools, skills, MCP
// servers and settings files (so hooks and CLAUDE.md) are all off: the call
// is a bare completion billed to the user's existing login. Thinking is off:
// with it Haiku spent up to 7K output tokens and a minute on an 8-word
// answer; without it a call takes ~2 s and ~$0.0015. It runs in the temp dir
// with the tmux/Coral variables removed, so Coral's session detection never
// sees it.
func runGoalCLI(ctx context.Context, bin, prompt string) (goalCLIResult, error) {
	cmd := executil.Command(ctx, bin,
		"--print",
		"--model", "haiku",
		"--no-session-persistence",
		"--output-format", "json",
		"--system-prompt", goalPrompt,
		"--tools", "",
		"--disable-slash-commands",
		"--strict-mcp-config",
		"--setting-sources", "",
		prompt,
	)
	cmd.Dir = os.TempDir()
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") || strings.HasPrefix(kv, "CORAL_") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "MAX_THINKING_TOKENS=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	res, perr := parseGoalCLIOutput(out)
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(res.Text)
		}
		if msg != "" {
			return res, fmt.Errorf("claude CLI failed: %w: %s", err, truncateRunes(msg, 300))
		}
		return res, fmt.Errorf("claude CLI failed: %w", err)
	}
	return res, perr
}

// parseGoalCLIOutput reads the CLI's --output-format json reply. An error
// reply (is_error) is returned as an error carrying the CLI's message.
func parseGoalCLIOutput(out []byte) (goalCLIResult, error) {
	var r struct {
		Result       string  `json:"result"`
		IsError      bool    `json:"is_error"`
		TotalCostUSD float64 `json:"total_cost_usd"`
		Usage        struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return goalCLIResult{Text: string(out)}, fmt.Errorf("unreadable CLI output: %w", err)
	}
	res := goalCLIResult{
		Text:             r.Result,
		CostUSD:          r.TotalCostUSD,
		InputTokens:      r.Usage.InputTokens,
		OutputTokens:     r.Usage.OutputTokens,
		CacheReadTokens:  r.Usage.CacheReadInputTokens,
		CacheWriteTokens: r.Usage.CacheCreationInputTokens,
	}
	if r.IsError {
		return res, fmt.Errorf("claude CLI error: %s", truncateRunes(strings.TrimSpace(r.Result), 300))
	}
	return res, nil
}
