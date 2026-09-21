package background

import (
	"context"
	"encoding/json"
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
	force       bool // operator asked for a refresh
}

// goalDue reports whether a session needs a new goal. size and modAt describe
// the transcript now.
func goalDue(st goalState, size int64, modAt, now time.Time) bool {
	if st.inFlight {
		return false
	}
	if st.force {
		return true
	}
	if !st.failedAt.IsZero() && now.Sub(st.failedAt) < goalFailureBackoff {
		return false
	}
	if size <= st.size {
		return false // nothing new since the last goal
	}
	if st.generatedAt.IsZero() {
		return true
	}
	since := now.Sub(st.generatedAt)
	turnEnded := now.Sub(modAt) >= goalQuietPeriod
	return (turnEnded && since >= goalMinInterval) || since >= goalMaxInterval
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
	runCLI      func(ctx context.Context, bin, prompt string) (string, error)

	mu     sync.Mutex
	states map[string]*goalState
	noCLI  bool // logged once, not once per tick
	sem    chan struct{}
	wake   chan struct{}
	wg     sync.WaitGroup
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
		if goalDue(*st, info.Size(), info.ModTime(), g.now()) {
			st.inFlight = true
			st.force = false
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
		if !g.noCLI {
			g.logger.Warn("claude CLI not found; agent goals fall back to the first prompt")
			g.noCLI = true
		}
		for _, ls := range due {
			g.states[ls.SessionID].inFlight = false
		}
		g.mu.Unlock()
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

// generate produces and stores one goal. It always clears inFlight.
func (g *GoalGenerator) generate(ctx context.Context, bin string, ls *store.LiveSession) {
	g.mu.Lock()
	var path string
	if st := g.states[ls.SessionID]; st != nil {
		path = st.path
	}
	g.mu.Unlock()
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	ok, failed := false, false
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		st := g.states[ls.SessionID]
		if st == nil {
			return
		}
		st.inFlight = false
		st.size = size
		if ok {
			st.generatedAt = g.now()
			st.failedAt = time.Time{}
		}
		if failed {
			st.failedAt = g.now()
		}
	}()

	current, source := g.latestGoal(ctx, ls.SessionID)
	if source == GoalSourceUser {
		ok = true // the operator's goal stands; check again when there is more work
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

	prompt := fmt.Sprintf("%s\n\nFIRST INSTRUCTION:\n%s\n\nRECENT TRANSCRIPT:\n%s", goalPrompt, first, tail)
	cctx, cancel := context.WithTimeout(ctx, goalCLITimeout)
	defer cancel()
	out, err := g.runCLI(cctx, bin, prompt)
	if err != nil {
		failed = true
		g.logger.Warn("goal generation failed", "session_id", ls.SessionID, "error", err)
		return
	}
	goal := cleanGoal(out)
	if goal == "" {
		failed = true
		return
	}
	ok = true
	if goal == current {
		return
	}
	detail := fmt.Sprintf(`{"source":%q}`, GoalSourceAuto)
	sid := ls.SessionID
	if _, err := g.taskStore.InsertAgentEvent(ctx, &store.AgentEvent{
		AgentName:  ls.AgentName,
		SessionID:  &sid,
		EventType:  GoalEventType,
		Summary:    goal,
		DetailJSON: &detail,
	}); err != nil {
		g.logger.Error("failed to store goal", "session_id", ls.SessionID, "error", err)
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

// runGoalCLI asks the claude CLI for a goal with the cheapest model. It runs
// in the temp dir with the tmux/Coral variables removed, so neither the
// project's hooks and CLAUDE.md nor Coral's session detection see it.
func runGoalCLI(ctx context.Context, bin, prompt string) (string, error) {
	cmd := executil.Command(ctx, bin,
		"--print",
		"--model", "haiku",
		"--no-session-persistence",
		prompt,
	)
	cmd.Dir = os.TempDir()
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX") || strings.HasPrefix(kv, "CORAL_") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude CLI failed: %w", err)
	}
	return string(out), nil
}
