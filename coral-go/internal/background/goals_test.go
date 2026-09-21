package background

import (
	"context"
	"encoding/json"
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
		name string
		st   goalState
		size int64
		mod  time.Time
		want bool
	}{
		{"first goal as soon as there is a transcript", goalState{}, 10, busy, true},
		{"nothing new since the last goal", gen(time.Hour), 100, quiet, false},
		{"turn ended, min interval passed", gen(3 * time.Minute), 200, quiet, true},
		{"turn ended, too soon", gen(time.Minute), 200, quiet, false},
		{"still working, under max interval", gen(3 * time.Minute), 200, busy, false},
		{"still working, max interval passed", gen(6 * time.Minute), 200, busy, true},
		{"in flight", goalState{inFlight: true}, 10, quiet, false},
		{"failed recently", goalState{failedAt: now.Add(-time.Minute)}, 10, quiet, false},
		{"failure backoff over", goalState{failedAt: now.Add(-11 * time.Minute)}, 10, quiet, true},
		{"forced bypasses interval, backoff and growth", goalState{size: 100, generatedAt: now, failedAt: now, force: true}, 100, busy, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, goalDue(c.st, c.size, c.mod, now))
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
		{"type": "user", "content": "Also drop the separators"},
	}
	first, tail, hasAssistant := condenseForGoal(msgs, 6000)
	assert.Equal(t, "Make the sidebar quieter", first)
	assert.True(t, hasAssistant)
	assert.Equal(t, "User: Make the sidebar quieter\nAssistant: Looking at the CSS.\nTool: Read session.css\nUser: Also drop the separators", tail)

	_, tail, _ = condenseForGoal(msgs, 20)
	assert.True(t, strings.HasPrefix(tail, "..."))
	assert.Equal(t, "... drop the separators", tail, "keeps the most recent end")

	_, _, hasAssistant = condenseForGoal(msgs[:1], 6000)
	assert.False(t, hasAssistant, "no goal until the agent has answered")
}

// goalFixture is one live Claude session with a transcript on disk and a
// generator whose CLI is faked.
type goalFixture struct {
	ctx   context.Context
	ss    *store.SessionStore
	ts    *store.TaskStore
	gen   *GoalGenerator
	sid   string
	path  string
	calls *atomic.Int32
	reply string
}

func newGoalFixture(t *testing.T, ls store.LiveSession) *goalFixture {
	t.Helper()
	db := setupTestDB(t)
	f := &goalFixture{
		ctx:   context.Background(),
		ss:    store.NewSessionStore(db),
		ts:    store.NewTaskStore(db),
		sid:   ls.SessionID,
		calls: &atomic.Int32{},
		reply: "\"Quiet the sidebar goal line.\"\n",
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
	f.gen.runCLI = func(_ context.Context, bin, prompt string) (string, error) {
		f.calls.Add(1)
		return f.reply, nil
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
