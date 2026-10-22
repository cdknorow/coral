package background

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/agentsignin"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/safego"
	setreg "github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/store"
)

// GoalEventType is the agent_events type that carries a session's goal line.
// Goals from every source share it; the newest one is shown.
const GoalEventType = "goal"

// Goal sources, stored in the goal event's detail_json.
const (
	GoalSourceAuto = "auto" // written by the GoalGenerator
	GoalSourceUser = "user" // typed by the operator; never overwritten
)

// GoalSettingKey disables the generator when set to "false" (any case: PUT /api/settings
// stores a JSON false as "False").
const GoalSettingKey = setreg.AutoGoals

// GoalsDisabled reports whether the auto_goals setting turns the generator off.
func GoalsDisabled(settings map[string]string) bool {
	return !setreg.Bool(settings, GoalSettingKey)
}

const (
	// goalQuietPeriod is how long the transcript must be still before the
	// turn counts as ended.
	goalQuietPeriod = 20 * time.Second
	// goalMinInterval is the least time between two automatic goals for one agent (#4204): a
	// goal names the user's task, which rarely changes faster, and each goal is a CLI run on the
	// user's account.
	goalMinInterval = 30 * time.Minute
	// goalMaxInterval refreshes a long-running turn that never goes quiet.
	goalMaxInterval = time.Hour
	// goalFailureBackoff pauses a session after the CLI fails, doubling for each failure in a
	// row up to goalMaxFailureBackoff (#4204: a CLI that always times out cost a run every
	// 10 minutes per agent).
	goalFailureBackoff    = 30 * time.Minute
	goalMaxFailureBackoff = 4 * time.Hour
	// goalCLITimeout bounds one CLI call, including its startup.
	goalCLITimeout = 60 * time.Second
	// goalManualFloor is the least time between two operator refreshes of one session that
	// run the CLI: a second click inside it finds the first one's goal pending (#4204).
	goalManualFloor = time.Minute
	// goalConcurrency caps CLI calls running at once.
	goalConcurrency = 2
	// goalMaxWords caps the stored goal even if the model runs long.
	goalMaxWords = 10
)

// goalPrompt is the CLI's whole system prompt. Replacing Claude Code's own
// system prompt and tools cuts a call from ~22K input tokens to a few hundred.
const goalPrompt = `You name the task a running AI coding agent is doing, for a dashboard sidebar. You are given the user's latest request, the one before it, and the agent's latest reply.

Reply with ONLY the task: at most 8 words, imperative, e.g. "Add goal generation metrics", "Remove sidebar separator lines", "Fix flaky websocket reconnect test".

The task is the feature, fix or change the user asked for: the deliverable. It is NOT the agent's current step. Never describe running tests, reading or editing files, building, benchmarking, committing, investigating or verifying.

Use the LATEST USER REQUEST. If it only approves or adjusts earlier work ("yes, go ahead", "make it shorter"), the task comes from the EARLIER USER REQUEST.

If CURRENT GOAL already names this task (not a step), reply with it exactly; otherwise replace it.

No quotes, no trailing period, no preamble.`

// goalInput is what the model reads about one session: the user's request
// and the agent's latest reply. Nothing in between; tool calls describe the
// agent's current step, and the goal is the user's task.
type goalInput struct {
	Earlier      string // the user request before Request, for follow-ups
	Request      string // the user message that started the current exchange
	Response     string // the agent's latest reply
	HasAssistant bool   // the agent has answered at least once
}

// goalTranscript keeps what goalInput needs while a transcript is read
// incrementally, so a request far behind large tool output is not lost.
type goalTranscript struct {
	offset       int64  // bytes consumed
	prevRequest  string // the user request before lastRequest
	lastRequest  string
	response     string
	hasAssistant bool
}

// feed consumes parsed transcript messages in order.
func (t *goalTranscript) feed(messages []map[string]any) {
	for _, m := range messages {
		switch m["type"] {
		case "user":
			c, _ := m["content"].(string)
			if c = userRequestText(c); c != "" {
				t.prevRequest, t.lastRequest = t.lastRequest, truncateRunes(c, 1500)
			}
		case "assistant":
			t.hasAssistant = true
			if txt, _ := m["text"].(string); strings.TrimSpace(txt) != "" {
				t.response = truncateRunes(strings.TrimSpace(txt), 1500)
			}
		}
	}
}

// read brings the transcript up to date: the first read (after a start, or of a new or rewritten
// transcript) needs only the last two requests and the latest reply, so it reads back from the
// end until they are there instead of decoding the whole transcript (#3715); later reads take
// the appended lines, as before.
func (t *goalTranscript) read(ctx context.Context, path, agentType string) error {
	var messages []map[string]any
	var offset int64
	var err error
	if t.offset == 0 {
		messages, offset, err = jsonl.ReadTail(ctx, path, agentType, goalInputComplete)
	} else {
		messages, offset, err = jsonl.ReadFromContext(ctx, path, agentType, t.offset)
	}
	if err != nil {
		return err
	}
	t.offset = offset
	t.feed(messages)
	return nil
}

// goalInputComplete reports whether messages (a transcript's tail) already decide goalInput:
// two user requests and a reply after them can't be changed by anything earlier (#3715).
func goalInputComplete(messages []map[string]any) bool {
	var t goalTranscript
	t.feed(messages)
	return t.prevRequest != "" && t.response != ""
}

// input is the model's input: the last two user requests and the agent's
// last text reply.
func (t *goalTranscript) input() goalInput {
	return goalInput{Earlier: t.prevRequest, Request: t.lastRequest, Response: t.response, HasAssistant: t.hasAssistant}
}

// condenseForGoal builds the model's input from a whole list of messages.
func condenseForGoal(messages []map[string]any) goalInput {
	var t goalTranscript
	t.feed(messages)
	return t.input()
}

// buildGoalPrompt renders the model's user message.
func buildGoalPrompt(in goalInput, current string) string {
	if current == "" {
		current = "(none)"
	}
	earlier := in.Earlier
	if earlier == "" {
		earlier = "(none)"
	}
	return fmt.Sprintf("CURRENT GOAL: %s\n\nEARLIER USER REQUEST:\n%s\n\nLATEST USER REQUEST:\n%s\n\nAGENT'S LATEST REPLY:\n%s", current, earlier, in.Request, in.Response)
}

// goalState is the generator's in-memory view of one session.
type goalState struct {
	path        string    // transcript file, resolved once
	size        int64     // transcript size at the last generation
	generatedAt time.Time // last successful generation
	failedAt    time.Time // last CLI failure
	failures    int       // CLI failures in a row
	inFlight    bool
	force       bool      // operator asked for a refresh
	manualAt    time.Time // the last operator refresh that was taken
	trigger     string    // why the in-flight generation was started
	// transcript is read incrementally; only generate touches it and basis, and only
	// one generate runs per session (inFlight).
	transcript goalTranscript
	// basis is goalBasis of the input the current automatic goal was made from.
	basis string
}

// goalBackoff is how long a session waits after its failures-th CLI failure in a row.
func goalBackoff(failures int) time.Duration {
	d := goalFailureBackoff
	for i := 1; i < failures && d < goalMaxFailureBackoff; i++ {
		d *= 2
	}
	return min(d, goalMaxFailureBackoff)
}

// goalBasis identifies what a goal is made from: the user's requests (#4204). The agent's
// replies and tool calls are its steps; while the requests are the same, so is the task, and
// the CLI is not asked again.
func goalBasis(in goalInput) string {
	sum := sha256.Sum256([]byte(in.Earlier + "\x00" + in.Request))
	return hex.EncodeToString(sum[:8])
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
	if !st.failedAt.IsZero() && now.Sub(st.failedAt) < goalBackoff(st.failures) {
		return false, ""
	}
	if size <= st.size {
		return false, "" // nothing new since the last goal
	}
	if st.generatedAt.IsZero() {
		return true, store.GoalTriggerFirst
	}
	since := now.Sub(st.generatedAt)
	if since < goalMinInterval {
		return false, ""
	}
	if now.Sub(modAt) >= goalQuietPeriod {
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
		Enabled:         !GoalsDisabled(settings),
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
		if !s.failedAt.IsZero() && now.Sub(s.failedAt) < goalBackoff(s.failures) {
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
// typed is still never replaced. At most one refresh a minute per session runs
// the CLI (goalManualFloor); one inside that minute adds nothing to the one
// already pending or just made.
func (g *GoalGenerator) RequestGoal(sessionID string) {
	g.mu.Lock()
	st := g.states[sessionID]
	if st == nil {
		st = &goalState{}
		g.states[sessionID] = st
	}
	now := g.now()
	if !st.manualAt.IsZero() && now.Sub(st.manualAt) < goalManualFloor {
		g.mu.Unlock()
		return
	}
	st.manualAt = now
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
	settings, serr := g.sessionStore.GetSettingsForCLI(ctx)
	if serr != nil { // without the settings the goal CLI would be the one on PATH, not the configured one (#4350): skip this poll
		g.logger.Error("failed to read settings for the goal poll", "error", serr)
		return
	}
	if GoalsDisabled(settings) {
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

	// A signed-out Claude is never started (#5469): its sign-in flow would hold the login. The goals wait (no attempt recorded, no failure counted) and are due again on a
	// later tick; agentsignin asks the CLI only now and then and says once per wait that jobs are skipped.
	if err := agentsignin.Require(ctx, at.Claude, bin); err != nil {
		g.mu.Lock()
		for _, ls := range due {
			if st := g.states[ls.SessionID]; st != nil {
				st.inFlight = false
			}
		}
		g.mu.Unlock()
		return
	}

	for _, ls := range due {
		ls := ls
		g.wg.Add(1)
		go func() {
			defer safego.Recover("background/goals:432")
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
	var goalAt time.Time // the stored goal's time, when this run kept it
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
			// Not attempted: no transcript to read, no answer yet, or the same requests.
			if st.generatedAt.IsZero() && !goalAt.IsZero() {
				st.generatedAt = goalAt // after a restart: the stored goal's time counts
			}
		case store.IsGoalFailure(attempt.Outcome):
			st.failedAt = g.now()
			st.failures++
		default:
			st.generatedAt = g.now()
			st.failedAt = time.Time{}
			st.failures = 0
		}
	}()

	current, source, currentBasis, currentAt := g.latestGoal(ctx, ls.SessionID)
	if source == GoalSourceUser {
		// The operator's goal stands; check again when there is more work.
		attempt.Outcome = store.GoalOutcomeUserGoal
		g.record(ctx, attempt)
		return
	}

	g.mu.Lock()
	tr := &goalState{}
	if st := g.states[ls.SessionID]; st != nil {
		tr = st
	}
	g.mu.Unlock()
	transcript := &tr.transcript
	if size < transcript.offset {
		*transcript = goalTranscript{} // truncated or replaced: start over
	}
	if transcript.read(ctx, path, ls.AgentType) != nil {
		return
	}
	in := transcript.input()
	if in.Request == "" {
		in.Request = truncateRunes(userRequestText(g.reader.FirstUserPrompt(ls.SessionID, ls.WorkingDir, ls.AgentType)), 1500)
	}
	if !in.HasAssistant {
		size = 0 // nothing to describe yet; retry once the agent answers
		return
	}
	basis := goalBasis(in)
	if tr.basis == "" && source == GoalSourceAuto {
		tr.basis = currentBasis // the stored goal's, after a restart
	}
	if trigger != store.GoalTriggerManual && current != "" && basis == tr.basis {
		goalAt = currentAt // the same requests: the goal stands, and no CLI run
		return
	}

	prompt := buildGoalPrompt(in, current)
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
	tr.basis = basis
	if goal == current {
		attempt.Outcome = store.GoalOutcomeUnchanged
		return
	}
	attempt.Outcome = store.GoalOutcomeStored
	detail := fmt.Sprintf(`{"source":%q,"basis":%q}`, GoalSourceAuto, basis)
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

// latestGoal returns the session's newest goal, its source ("" for goals that predate
// sources), the goalBasis it was made from ("" before #4204) and when it was stored.
func (g *GoalGenerator) latestGoal(ctx context.Context, sessionID string) (goal, source, basis string, at time.Time) {
	ev, err := g.taskStore.GetLatestGoalEvent(ctx, sessionID)
	if err != nil || ev == nil {
		return "", "", "", time.Time{}
	}
	if ev.DetailJSON != nil {
		var d struct {
			Basis string `json:"basis"`
		}
		if json.Unmarshal([]byte(*ev.DetailJSON), &d) == nil {
			basis = d.Basis
		}
	}
	at, _ = time.Parse(store.ISOFormat, ev.CreatedAt)
	return ev.Summary, GoalSource(ev.DetailJSON), basis, at
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

// userRequestText drops the image placeholders Claude Code adds to a user
// message ("[Image: original 2102x872, ...]", "[Image #1]").
func userRequestText(c string) string {
	var keep []string
	for _, line := range strings.Split(c, "\n") {
		t := strings.TrimSpace(imageRefRE.ReplaceAllString(line, ""))
		if t == "" || strings.HasPrefix(t, "[Image:") {
			continue
		}
		keep = append(keep, t)
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

var imageRefRE = regexp.MustCompile(`\[Image #\d+\]`)

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

// resolveClaudeCLI finds the claude binary the way launch does (agent.FindCLI);
// "" when there is none.
func resolveClaudeCLI(settings map[string]string) string {
	p, _ := agent.FindCLI(settings, at.Claude)
	return p
}

// runGoalCLI asks the claude CLI for a goal with the cheapest model. The
// goal rules replace Claude Code's system prompt, and tools, skills, MCP
// servers, hooks and project settings are all off: the call is a bare
// completion billed to the user's existing login, which is why the user
// settings stay on (a Bedrock or Vertex setup lives there). Thinking is off:
// with it Haiku spent up to 7K output tokens and a minute on an 8-word
// answer; without it a call takes ~2 s and ~$0.0015. It runs in the temp dir
// with the tmux/Coral variables removed, so Coral's session detection never
// sees it.
func runGoalCLI(ctx context.Context, bin, prompt string) (goalCLIResult, error) {
	if err := agentsignin.Require(ctx, at.Claude, bin); err != nil {
		return goalCLIResult{}, err
	}
	cmd := executil.Command(ctx, bin,
		"--print",
		"--model", "haiku",
		"--no-session-persistence",
		"--output-format", "json",
		"--system-prompt", goalPrompt,
		"--tools", "",
		"--disable-slash-commands",
		"--strict-mcp-config",
		// User settings carry the login: Bedrock/Vertex env, awsAuthRefresh,
		// apiKeyHelper. Their hooks are switched off; project and local
		// settings are not read (the call runs in the temp dir anyway).
		"--setting-sources", "user",
		"--settings", `{"disableAllHooks":true}`,
	)
	cmd.Env = agent.CLIEnv(os.Environ(), bin) // an npm-installed CLI runs with its own node (#5899)
	// The prompt (built from the session's own messages) goes on stdin, not argv, where it
	// would show in the process list (#3519).
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Dir = os.TempDir()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(strings.ToUpper(name), "TMUX") || agentenv.IsCoral(name) { // any case (#19)
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "MAX_THINKING_TOKENS=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	executil.NoCwdSearch(cmd) // Windows: a claude.cmd shim looks for node in the current folder first, so a repository's node.exe would run
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
