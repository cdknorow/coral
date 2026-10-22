package background

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
)

type nudgeHarness struct {
	t      *testing.T
	bs     *board.Store
	h      *HealthNudges
	now    time.Time
	sig    map[string]agentSignal // by session id
	mu     sync.Mutex
	sent   []string // "agent: text"
	agents []AgentInfo
}

func newNudgeHarness(t *testing.T, path string) *nudgeHarness {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "board.db")
	}
	bs, err := board.NewStoreWithKey(path, "")
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	require.NoError(t, bs.SetHealthOn(context.Background(), "team", true)) // off by default (#4962); these tests are about the rules
	n := &nudgeHarness{t: t, bs: bs, now: time.Now().UTC().Truncate(time.Second), sig: map[string]agentSignal{}}
	n.h = &HealthNudges{
		store: bs, discover: func(context.Context) ([]AgentInfo, error) { return n.agents, nil },
		signal: func(_ context.Context, id string) (agentSignal, bool) { s, ok := n.sig[id]; return s, ok },
		deliver: func(_ context.Context, a AgentInfo, _ string, text string) error {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.sent = append(n.sent, a.DisplayName+": "+text)
			return nil
		},
		now:        func() time.Time { return n.now },
		TeamBudget: 20,
	}
	return n
}

// agent subscribes name to the board with a live session.
func (n *nudgeHarness) agent(name, sid string) {
	n.t.Helper()
	_, err := n.bs.Subscribe(context.Background(), "team", name, name, naming.SessionName("claude", sid), nil, "all")
	require.NoError(n.t, err)
	n.agents = append(n.agents, AgentInfo{AgentType: "claude", SessionID: sid, DisplayName: name})
}

func (n *nudgeHarness) sentTo(name string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, s := range n.sent {
		if strings.HasPrefix(s, name+": ") {
			out = append(out, s)
		}
	}
	return out
}

func (n *nudgeHarness) lines() []string {
	msgs, err := n.bs.ListMessages(context.Background(), "team", 200, 0, 0)
	require.NoError(n.t, err)
	var out []string
	for _, m := range msgs {
		if strings.Contains(m.Content, "[Coral health") {
			out = append(out, m.Content)
		}
	}
	return out
}

func (n *nudgeHarness) run(after time.Duration) {
	n.now = n.now.Add(after)
	n.h.RunOnce(context.Background())
}

// #4175 step 1, the claim nudge: an idle agent with assigned work is nudged to claim it, at
// most every ClaimAfter, and after two nudges Coral escalates once and stops (the counters are
// stored: a restarted Coral doesn't nudge again). Its text and board lines carry the task
// number, never the title (#4176 rule 1); one board line per rule per run (rule 6).
func TestClaimNudgeThenEscalate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	n := newNudgeHarness(t, path)
	ctx := context.Background()
	n.agent("Dev", "11111111-1111-1111-1111-111111111111")
	task, err := n.bs.CreateTask(ctx, "team", "Secret title", "", "high", "Operator", "Dev")
	require.NoError(t, err)
	n.sig["11111111-1111-1111-1111-111111111111"] = agentSignal{Since: n.now.Add(-15 * time.Minute)}

	n.run(0)
	require.Len(t, n.sentTo("Dev"), 1)
	assert.Contains(t, n.sentTo("Dev")[0], "coral-board task claim")
	assert.NotContains(t, n.sentTo("Dev")[0], "Secret title")
	n.run(5 * time.Minute)
	assert.Len(t, n.sentTo("Dev"), 1, "within the cooldown")
	n.run(6 * time.Minute)
	assert.Len(t, n.sentTo("Dev"), 2)
	n.run(11 * time.Minute)
	assert.Len(t, n.sentTo("Dev"), 2, "escalated instead of a third nudge")
	lines := n.lines()
	assert.Len(t, lines, 3, "one line per run that acted: %v", lines)
	assert.Contains(t, strings.Join(lines, "\n"), "escalated to the orchestrator: Dev has not claimed #")
	for _, l := range lines {
		assert.NotContains(t, l, "Secret title")
	}

	// A restart: the escalation is remembered.
	n2 := newNudgeHarness(t, path)
	n2.agents, n2.sig, n2.now = n.agents, n.sig, n.now.Add(11*time.Minute)
	n2.h.RunOnce(ctx)
	assert.Empty(t, n2.sentTo("Dev"))

	// Claiming it starts over.
	_, err = n.bs.ClaimTask(ctx, "team", "Dev", task.ID)
	require.NoError(t, err)
	n.run(time.Minute)
	c, err := n.bs.GetHealthCounter(ctx, "team", ruleClaimNudge, "Dev", "1")
	require.NoError(t, err)
	assert.Equal(t, board.HealthCounter{}, c)
}

// Never nudged: an agent in a turn, one at a prompt, one waiting on someone, one Coral knows
// nothing about (no hook events).
func TestClaimNudgeSkipsBusyWaitingAndUnknownAgents(t *testing.T) {
	n := newNudgeHarness(t, "")
	ctx := context.Background()
	ids := map[string]string{"Busy": "22222222-2222-2222-2222-222222222222", "Prompt": "33333333-3333-3333-3333-333333333333",
		"Waiter": "44444444-4444-4444-4444-444444444444", "Unknown": "55555555-5555-5555-5555-555555555555"}
	for name, sid := range ids {
		n.agent(name, sid)
		_, err := n.bs.CreateTask(ctx, "team", "work", "", "high", "Operator", name)
		require.NoError(t, err)
	}
	old := n.now.Add(-time.Hour)
	n.sig[ids["Busy"]] = agentSignal{InTurn: true, Since: old}
	n.sig[ids["Prompt"]] = agentSignal{NeedsInput: true, Since: old}
	n.sig[ids["Waiter"]] = agentSignal{Since: old}
	_, err := n.bs.RegisterWait(ctx, "team", "Waiter", naming.SessionName("claude", ids["Waiter"]), "message", "Busy", "", time.Hour)
	require.NoError(t, err)
	n.run(0)
	n.mu.Lock()
	defer n.mu.Unlock()
	assert.Empty(t, n.sent)
}

// The status nudge: a task in progress with no activity for StatusAfter gets one nudge; still
// silent SilentAfter later, Coral escalates; activity starts it over.
func TestStatusNudgeThenEscalate(t *testing.T) {
	n := newNudgeHarness(t, "")
	ctx := context.Background()
	sid := "66666666-6666-6666-6666-666666666666"
	n.agent("Dev", sid)
	task, err := n.bs.CreateTask(ctx, "team", "Secret title", "", "high", "Operator", "Dev")
	require.NoError(t, err)
	_, err = n.bs.ClaimTask(ctx, "team", "Dev", task.ID)
	require.NoError(t, err)
	n.sig[sid] = agentSignal{Since: n.now, LastEvent: n.now}

	n.run(15 * time.Minute)
	assert.Empty(t, n.sentTo("Dev"), "15 minutes: not yet")
	n.run(10 * time.Minute)
	require.Len(t, n.sentTo("Dev"), 1)
	assert.Contains(t, n.sentTo("Dev")[0], "Post a one-line summary")
	assert.NotContains(t, n.sentTo("Dev")[0], "\n", "the nudge stays one line")
	n.run(11 * time.Minute)
	assert.Len(t, n.sentTo("Dev"), 1)
	assert.Contains(t, strings.Join(n.lines(), "\n"), "Dev silent on #")

	esc, err := n.bs.HealthEscalations(ctx, "team")
	require.NoError(t, err)
	require.Len(t, esc, 1)
	n.sig[sid] = agentSignal{Since: n.now, LastEvent: n.now.Add(time.Minute)} // it answered
	n.run(2 * time.Minute)
	c, err := n.bs.GetHealthCounter(ctx, "team", ruleStatusNudge, "Dev", "1")
	require.NoError(t, err)
	assert.Equal(t, board.HealthCounter{}, c, "active again: starts over")
}

// The team budget (#4176 rule 5): past it Coral stops, and says so once.
func TestHealthNudgesStopAtTheBudget(t *testing.T) {
	n := newNudgeHarness(t, "")
	n.h.TeamBudget = 1
	ctx := context.Background()
	for _, name := range []string{"A", "B", "C"} {
		sid := strings.Repeat(map[string]string{"A": "7", "B": "8", "C": "9"}[name], 8) + "-0000-0000-0000-000000000000"
		n.agent(name, sid)
		_, err := n.bs.CreateTask(ctx, "team", "work", "", "high", "Operator", name)
		require.NoError(t, err)
		n.sig[sid] = agentSignal{Since: n.now.Add(-time.Hour)}
	}
	n.run(0)
	n.mu.Lock()
	assert.Len(t, n.sent, 1)
	n.mu.Unlock()
	n.run(11 * time.Minute)
	var paused int
	for _, l := range n.lines() {
		if strings.Contains(l, "actions paused") {
			paused++
		}
	}
	assert.Equal(t, 1, paused, "said once an hour")
}

// An escalation ends with its task: the orchestrator isn't told about work that is done.
func TestEscalationEndsWithItsTask(t *testing.T) {
	n := newNudgeHarness(t, "")
	ctx := context.Background()
	n.agent("Dev", "12121212-1212-1212-1212-121212121212")
	task, err := n.bs.CreateTask(ctx, "team", "work", "", "high", "Operator", "Dev")
	require.NoError(t, err)
	require.NoError(t, n.bs.MarkHealthEscalated(ctx, "team", ruleClaimNudge, "Dev", fmt.Sprint(task.ID), n.now))
	_, err = n.bs.CancelTask(ctx, "team", task.ID, "Operator", nil)
	require.NoError(t, err)
	n.run(time.Minute)
	esc, err := n.bs.HealthEscalations(ctx, "team")
	require.NoError(t, err)
	assert.Empty(t, esc)
}

// #4176 rule 9: an agent's @-mention of the orchestrator that it hasn't acted on for 10 minutes
// gets one reminder: once per message, one per sender per window (later mentions coalesced),
// none when the orchestrator posted or acted since, none for Coral's own line (case C7).
func TestMentionRenudge(t *testing.T) {
	n := newNudgeHarness(t, "")
	ctx := context.Background()
	orchID := "13131313-1313-1313-1313-131313131313"
	n.agent("Orchestrator", orchID)
	n.agent("Dev", "14141414-1414-1414-1414-141414141414")
	n.sig[orchID] = agentSignal{Since: n.now.Add(-time.Hour), LastEvent: n.now.Add(-time.Hour)}
	_, err := n.bs.PostMessage(ctx, "team", "Dev", "@Orchestrator can you look at #12?", nil)
	require.NoError(t, err)
	_, err = n.bs.PostSystemMessage(ctx, "team", "Coral Task Queue", "@Orchestrator [Task #9 in review]", nil)
	require.NoError(t, err)

	n.run(5 * time.Minute)
	assert.Empty(t, n.sentTo("Orchestrator"), "not yet 10 minutes")
	n.run(6 * time.Minute)
	require.Len(t, n.sentTo("Orchestrator"), 1)
	assert.Contains(t, n.sentTo("Orchestrator")[0], "Dev mentioned you at")
	n.run(10 * time.Minute)
	assert.Len(t, n.sentTo("Orchestrator"), 1, "once per message")

	// Two more from Dev: one reminder for both, after the sender's window.
	for i := 0; i < 2; i++ {
		_, err = n.bs.PostMessage(ctx, "team", "Dev", "@Orchestrator ping", nil)
		require.NoError(t, err)
	}
	n.run(25 * time.Minute)
	require.Len(t, n.sentTo("Orchestrator"), 2)
	assert.Contains(t, n.sentTo("Orchestrator")[1], "mentioned you 2 times")

	// The orchestrator answers: nothing more.
	_, err = n.bs.PostMessage(ctx, "team", "Dev", "@Orchestrator again", nil)
	require.NoError(t, err)
	_, err = n.bs.PostMessage(ctx, "team", "Orchestrator", "on it", nil)
	require.NoError(t, err)
	n.run(45 * time.Minute)
	assert.Len(t, n.sentTo("Orchestrator"), 2, "it posted after the mention")
}

// A rule the team turned off doesn't act (#4175 settings); its other rules still do.
func TestHealthNudgesFollowTheTeamSettings(t *testing.T) {
	n := newNudgeHarness(t, "")
	ctx := context.Background()
	sid := "15151515-1515-1515-1515-151515151515"
	n.agent("Dev", sid)
	_, err := n.bs.CreateTask(ctx, "team", "work", "", "high", "Operator", "Dev")
	require.NoError(t, err)
	n.sig[sid] = agentSignal{Since: n.now.Add(-time.Hour)}
	hs := healthOn()
	hs.ClaimNudge.On = false
	_, err = n.bs.UpdateBoardSettings(ctx, "team", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	n.run(0)
	assert.Empty(t, n.sentTo("Dev"))

	hs.ClaimNudge.On, hs.ClaimNudge.AfterMin = true, 90
	_, err = n.bs.UpdateBoardSettings(ctx, "team", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	n.run(time.Minute)
	assert.Empty(t, n.sentTo("Dev"), "idle 61 min, the team waits 90")
	n.run(30 * time.Minute)
	assert.Len(t, n.sentTo("Dev"), 1)
}
