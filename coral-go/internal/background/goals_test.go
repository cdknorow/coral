package background

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

func TestGoalDue(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	quiet := now.Add(-time.Minute)    // transcript untouched for a minute: turn ended
	busy := now.Add(-2 * time.Second) // still being written
	gen := func(ago time.Duration) goalState { return goalState{size: 100, generatedAt: now.Add(-ago)} }

	cases := []struct {
		name    string
		st      goalState
		size    int64
		mod     time.Time
		trigger string // "" = not due
	}{
		{"first goal as soon as there is a transcript", goalState{}, 10, busy, store.GoalTriggerFirst},
		{"nothing new since the last goal", gen(time.Hour), 100, quiet, ""},
		{"turn ended, min interval passed", gen(3 * time.Minute), 200, quiet, store.GoalTriggerTurnEnd},
		{"turn ended, too soon", gen(time.Minute), 200, quiet, ""},
		{"still working, under max interval", gen(3 * time.Minute), 200, busy, ""},
		{"still working, max interval passed", gen(6 * time.Minute), 200, busy, store.GoalTriggerInterval},
		{"in flight", goalState{inFlight: true}, 10, quiet, ""},
		{"failed recently", goalState{failedAt: now.Add(-time.Minute)}, 10, quiet, ""},
		{"failure backoff over", goalState{failedAt: now.Add(-11 * time.Minute)}, 10, quiet, store.GoalTriggerFirst},
		{"forced bypasses interval, backoff and growth", goalState{size: 100, generatedAt: now, failedAt: now, force: true}, 100, busy, store.GoalTriggerManual},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			due, trigger := goalDue(c.st, c.size, c.mod, now)
			assert.Equal(t, c.trigger != "", due)
			assert.Equal(t, c.trigger, trigger)
		})
	}
}

func TestCleanGoal(t *testing.T) {
	cases := map[string]string{
		"Fix flaky websocket reconnect test":                    "Fix flaky websocket reconnect test",
		"\"Fix the sidebar colours.\"\n":                        "Fix the sidebar colours",
		"\n\nGoal: Add dark mode to settings\nMore text here":   "Add dark mode to settings",
		"**Refactor store layer**":                              "Refactor store layer",
		"one two three four five six seven eight nine ten more": "one two three four five six seven eight nine ten",
		"   ": "",
	}
	for in, want := range cases {
		assert.Equal(t, want, cleanGoal(in), "input %q", in)
	}
}

func TestCondenseForGoal(t *testing.T) {
	msgs := []map[string]any{
		{"type": "user", "content": "Make the sidebar quieter"},
		{"type": "assistant", "text": "Looking at the CSS.", "tool_uses": []map[string]any{{"name": "Read", "input_summary": "session.css"}}},
		{"type": "user", "content": "Lets add some metrics that track how often goals are requested"},
		{"type": "assistant", "text": "", "tool_uses": []map[string]any{{"name": "Bash", "input_summary": "go test ./..."}}},
		{"type": "assistant", "text": "Metrics are recorded per attempt."},
	}
	in := condenseForGoal(msgs)
	assert.Equal(t, goalInput{
		Earlier:      "Make the sidebar quieter",
		Request:      "Lets add some metrics that track how often goals are requested",
		Response:     "Metrics are recorded per attempt.",
		HasAssistant: true,
	}, in, "only the latest request and the latest reply; no tool calls")

	followUp := append(msgs, map[string]any{"type": "user", "content": "yes, go ahead [Image #1]"})
	fu := condenseForGoal(followUp)
	assert.Equal(t, "Lets add some metrics that track how often goals are requested", fu.Earlier)
	assert.Equal(t, "yes, go ahead", fu.Request)

	img := []map[string]any{{"type": "user", "content": "[Image: original 2102x872, displayed at 2000x830.]\nRemove the separators"}}
	assert.Equal(t, "Remove the separators", condenseForGoal(img).Request)
	assert.False(t, condenseForGoal(img).HasAssistant, "no goal until the agent has answered")
}

func TestBuildGoalPrompt(t *testing.T) {
	p := buildGoalPrompt(goalInput{Request: "Add metrics", Response: "Done."}, "")
	assert.Equal(t, "CURRENT GOAL: (none)\n\nEARLIER USER REQUEST:\n(none)\n\nLATEST USER REQUEST:\nAdd metrics\n\nAGENT'S LATEST REPLY:\nDone.", p)
	assert.Contains(t, buildGoalPrompt(goalInput{}, "Add goal metrics"), "CURRENT GOAL: Add goal metrics")
}

// goalFixture is one live Claude session with a transcript on disk and a
// generator whose CLI is faked.
type goalFixture struct {
	ctx     context.Context
	ss      *store.SessionStore
	ts      *store.TaskStore
	gen     *GoalGenerator
	sid     string
	path    string
	calls   *atomic.Int32
	reply   string
	err     error
	metrics *store.GoalMetricsStore
	prompt  string // the last prompt sent to the CLI
}

func newGoalFixture(t *testing.T, ls store.LiveSession) *goalFixture {
	t.Helper()
	db := setupTestDB(t)
	f := &goalFixture{
		ctx:     context.Background(),
		ss:      store.NewSessionStore(db),
		ts:      store.NewTaskStore(db),
		sid:     ls.SessionID,
		calls:   &atomic.Int32{},
		metrics: store.NewGoalMetricsStore(db),
		reply:   "\"Quiet the sidebar goal line.\"\n",
	}
	projects := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", projects)
	if ls.WorkingDir == "" {
		ls.WorkingDir = "/repo/coral-go"
	}
	require.NoError(t, f.ss.RegisterLiveSession(f.ctx, &ls))

	dir := filepath.Join(projects, strings.ReplaceAll(ls.WorkingDir, "/", "-"))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	f.path = filepath.Join(dir, ls.SessionID+".jsonl")
	f.appendTranscript(t,
		`{"type":"user","message":{"role":"user","content":"Make the sidebar goal line quieter"},"timestamp":"2026-09-21T11:00:00Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Updating session.css."}]},"timestamp":"2026-09-21T11:00:05Z"}`,
	)

	f.gen = NewGoalGenerator(f.ss, f.ts, time.Hour)
	f.gen.resolveCLI = func(map[string]string) string { return "claude" }
	f.gen.SetMetricsStore(f.metrics)
	f.gen.runCLI = func(_ context.Context, bin, prompt string) (goalCLIResult, error) {
		f.calls.Add(1)
		f.prompt = prompt
		return goalCLIResult{Text: f.reply, CostUSD: 0.001, InputTokens: 400, OutputTokens: 12}, f.err
	}
	return f
}

func (f *goalFixture) appendTranscript(t *testing.T, lines ...string) {
	t.Helper()
	fh, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer fh.Close()
	_, err = fh.WriteString(strings.Join(lines, "\n") + "\n")
	require.NoError(t, err)
}

func (f *goalFixture) poll() {
	f.gen.pollOnce(f.ctx)
	f.gen.wg.Wait()
}

func (f *goalFixture) latest(t *testing.T) (string, string) {
	t.Helper()
	ev, err := f.ts.GetLatestGoalEvent(f.ctx, f.sid)
	require.NoError(t, err)
	if ev == nil {
		return "", ""
	}
	return ev.Summary, GoalSource(ev.DetailJSON)
}

func TestGoalGenerator_StoresAShortGoalOncePerChange(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g001", AgentType: "claude", AgentName: "coral-go"})

	f.poll()
	goal, source := f.latest(t)
	assert.Equal(t, "Quiet the sidebar goal line", goal)
	assert.Equal(t, GoalSourceAuto, source)
	assert.Equal(t, int32(1), f.calls.Load())

	f.poll()
	assert.Equal(t, int32(1), f.calls.Load(), "no new transcript, no new call")

	// The operator asks for a refresh; the same answer is not stored twice.
	f.gen.RequestGoal(f.sid)
	f.poll()
	assert.Equal(t, int32(2), f.calls.Load())
	sid := f.sid
	events, err := f.ts.ListAgentEvents(f.ctx, "", 50, &sid)
	require.NoError(t, err)
	goals := 0
	for _, ev := range events {
		if ev.EventType == GoalEventType {
			goals++
		}
	}
	assert.Equal(t, 1, goals)
}

func TestGoalGenerator_NeverReplacesTheOperatorsGoal(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g002", AgentType: "claude", AgentName: "coral-go"})
	detail, _ := json.Marshal(map[string]string{"source": GoalSourceUser})
	d := string(detail)
	sid := f.sid
	_, err := f.ts.InsertAgentEvent(f.ctx, &store.AgentEvent{AgentName: "coral-go", SessionID: &sid, EventType: GoalEventType, Summary: "My own goal", DetailJSON: &d})
	require.NoError(t, err)

	f.poll()
	f.gen.RequestGoal(f.sid)
	f.poll()

	goal, source := f.latest(t)
	assert.Equal(t, "My own goal", goal)
	assert.Equal(t, GoalSourceUser, source)
	assert.Equal(t, int32(0), f.calls.Load(), "the CLI is not even called")
}

func TestGoalGenerator_WaitsForTheAgentsFirstAnswer(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g003", AgentType: "claude", AgentName: "coral-go"})
	require.NoError(t, os.WriteFile(f.path, []byte(`{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-09-21T11:00:00Z"}`+"\n"), 0o644))

	f.poll()
	assert.Equal(t, int32(0), f.calls.Load())
	goal, _ := f.latest(t)
	assert.Empty(t, goal)
}

func TestGoalGenerator_SkipsTerminalsSleepersAndWhenOff(t *testing.T) {
	for _, c := range []struct {
		name string
		ls   store.LiveSession
		off  bool
	}{
		{"terminal", store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g004", AgentType: "terminal", AgentName: "coral-go"}, false},
		{"sleeping", store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g005", AgentType: "claude", AgentName: "coral-go", IsSleeping: 1}, false},
		{"setting off", store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g006", AgentType: "claude", AgentName: "coral-go"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newGoalFixture(t, c.ls)
			if c.off {
				require.NoError(t, f.ss.SetSetting(f.ctx, GoalSettingKey, "false"))
			}
			f.poll()
			assert.Equal(t, int32(0), f.calls.Load())
		})
	}
}

func TestGoalGenerator_BacksOffAfterAFailure(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g007", AgentType: "claude", AgentName: "coral-go"})
	f.reply = "   " // unusable answer counts as a failure

	f.poll()
	f.appendTranscript(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"more"}]},"timestamp":"2026-09-21T11:01:00Z"}`)
	f.poll()
	assert.Equal(t, int32(1), f.calls.Load(), "backing off")
}

func (f *goalFixture) attempts(t *testing.T) []store.GoalGeneration {
	t.Helper()
	rows, err := f.metrics.Since(f.ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	return rows
}

func outcomes(rows []store.GoalGeneration) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Trigger+":"+r.Outcome)
	}
	return out
}

func TestGoalGenerator_RecordsEveryAttemptWithTriggerOutcomeAndCost(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g010", AgentType: "claude", AgentName: "coral-go"})

	f.poll()                 // first -> stored
	f.gen.RequestGoal(f.sid) // manual -> same answer
	f.poll()
	f.gen.RequestGoal(f.sid)
	f.err = errors.New("exit status 1")
	f.poll() // manual -> failed

	rows := f.attempts(t)
	assert.Equal(t, []string{"first:stored", "manual:unchanged", "manual:failed"}, outcomes(rows))
	assert.Equal(t, "Quiet the sidebar goal line", rows[0].Goal)
	assert.InDelta(t, 0.001, rows[0].CostUSD, 1e-9)
	assert.Equal(t, int64(400), rows[0].InputTokens)
	assert.Greater(t, rows[0].TranscriptBytes, int64(0))
	assert.Equal(t, "coral-go", rows[0].AgentName)
	assert.Contains(t, rows[2].Error, "exit status 1")
}

func TestGoalGenerator_RecordsTheOperatorsGoalAndAMissingCLIOnce(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g011", AgentType: "claude", AgentName: "coral-go"})
	f.gen.resolveCLI = func(map[string]string) string { return "" }

	f.poll()
	f.gen.RequestGoal(f.sid)
	f.poll()
	assert.Equal(t, []string{"first:no_cli"}, outcomes(f.attempts(t)), "one row per outage, not per tick")

	f.gen.resolveCLI = func(map[string]string) string { return "claude" }
	detail := `{"source":"user"}`
	sid := f.sid
	_, err := f.ts.InsertAgentEvent(f.ctx, &store.AgentEvent{AgentName: "coral-go", SessionID: &sid, EventType: GoalEventType, Summary: "Mine", DetailJSON: &detail})
	require.NoError(t, err)
	f.gen.RequestGoal(f.sid)
	f.poll()
	assert.Equal(t, []string{"first:no_cli", "manual:user_goal"}, outcomes(f.attempts(t)))
}

func TestGoalGenerator_StatusReportsLiveState(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g012", AgentType: "claude", AgentName: "coral-go"})
	f.reply = ""
	f.poll()
	st := f.gen.Status(f.ctx)
	assert.True(t, st.Enabled)
	assert.True(t, st.CLIFound)
	assert.Equal(t, 1, st.TrackedSessions)
	assert.Equal(t, 1, st.BackingOff)
	assert.Equal(t, 0, st.InFlight)
	assert.NotEmpty(t, st.LastPoll)
	assert.Equal(t, []string{"first:bad_output"}, outcomes(f.attempts(t)))
}

func TestParseGoalCLIOutput(t *testing.T) {
	res, err := parseGoalCLIOutput([]byte(`{"result":"Fix the tests","is_error":false,"total_cost_usd":0.000936,"usage":{"input_tokens":386,"output_tokens":9,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}`))
	require.NoError(t, err)
	assert.Equal(t, goalCLIResult{Text: "Fix the tests", CostUSD: 0.000936, InputTokens: 386, OutputTokens: 9, CacheReadTokens: 2, CacheWriteTokens: 3}, res)

	res, err = parseGoalCLIOutput([]byte(`{"result":"Credit balance is too low","is_error":true,"total_cost_usd":0}`))
	assert.ErrorContains(t, err, "Credit balance is too low")

	_, err = parseGoalCLIOutput([]byte("plain text"))
	assert.ErrorContains(t, err, "unreadable CLI output")
}

func TestGoalGenerator_SendsTheLatestRequestAndReplyEvenBehindLargeToolOutput(t *testing.T) {
	f := newGoalFixture(t, store.LiveSession{SessionID: "00000000-0000-0000-0000-00000000g013", AgentType: "claude", AgentName: "coral-go"})
	f.poll()
	assert.Contains(t, f.prompt, "LATEST USER REQUEST:\nMake the sidebar goal line quieter")
	assert.Contains(t, f.prompt, "AGENT'S LATEST REPLY:\nUpdating session.css.")

	big := strings.Repeat("x", 600*1024)
	f.appendTranscript(t,
		`{"type":"user","message":{"role":"user","content":"Lets add metrics for goal generation"},"timestamp":"2026-09-21T11:02:00Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]},"timestamp":"2026-09-21T11:02:01Z"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"`+big+`"}]},"timestamp":"2026-09-21T11:02:02Z"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Metrics are recorded per attempt."}]},"timestamp":"2026-09-21T11:02:03Z"}`,
	)
	f.gen.RequestGoal(f.sid)
	f.poll()
	assert.Contains(t, f.prompt, "CURRENT GOAL: Quiet the sidebar goal line")
	assert.Contains(t, f.prompt, "EARLIER USER REQUEST:\nMake the sidebar goal line quieter\n")
	assert.Contains(t, f.prompt, "LATEST USER REQUEST:\nLets add metrics for goal generation\n")
	assert.Contains(t, f.prompt, "AGENT'S LATEST REPLY:\nMetrics are recorded per attempt.")
	assert.NotContains(t, f.prompt, "go test", "tool calls are never sent")
	assert.NotContains(t, f.prompt, "xxxx", "tool output is never sent")
}
