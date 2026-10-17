package background

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/teamhealth"
)

func healthMonitorStore(t *testing.T) *board.Store {
	t.Helper()
	s, err := board.NewStoreWithKey(filepath.Join(t.TempDir(), "board-health.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// checkHarness is the orchestrator's check (#4175 step 2) over a real board store, with the
// agents, their hook state and the gate faked.
type checkHarness struct {
	t      *testing.T
	s      *board.Store
	m      *BoardHealthMonitor
	now    time.Time
	agents []AgentInfo
	sig    map[string]agentSignal
	mu     sync.Mutex
	sent   map[string][]string // agent display name -> texts typed to it
}

func newCheckHarness(t *testing.T) *checkHarness {
	h := &checkHarness{t: t, s: healthMonitorStore(t), now: time.Now().UTC().Add(time.Minute), sig: map[string]agentSignal{}, sent: map[string][]string{}}
	h.m = NewBoardHealthMonitor(h.s, 10*time.Minute)
	h.m.now = func() time.Time { return h.now }
	h.m.discover = func(context.Context) ([]AgentInfo, error) { return h.agents, nil }
	h.m.signal = func(_ context.Context, id string) (agentSignal, bool) { s, ok := h.sig[id]; return s, ok }
	h.m.deliver = func(_ context.Context, a AgentInfo, _ string, text string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.sent[a.DisplayName] = append(h.sent[a.DisplayName], text)
		return nil
	}
	return h
}

// member subscribes name with title and a live session; idle (if >0) is how long it has been idle.
func (h *checkHarness) member(name, title string, n int, idle time.Duration) {
	h.t.Helper()
	sid := fmt.Sprintf("%08d-0000-0000-0000-000000000000", n)
	_, err := h.s.Subscribe(context.Background(), "team", name, title, naming.SessionName("claude", sid), nil, "all")
	require.NoError(h.t, err)
	h.agents = append(h.agents, AgentInfo{AgentType: "claude", SessionID: sid, DisplayName: name})
	if idle > 0 {
		h.sig[sid] = agentSignal{Since: h.now.Add(-idle)}
	} else {
		h.sig[sid] = agentSignal{InTurn: true, Since: h.now}
	}
}

func (h *checkHarness) checkTo(name string) string {
	h.t.Helper()
	h.m.scan(context.Background())
	h.mu.Lock()
	defer h.mu.Unlock()
	got := h.sent[name]
	require.NotEmpty(h.t, got, "no check typed to %s", name)
	return got[len(got)-1]
}

// The check is typed to the orchestrator alone, never posted to the board (#4176 rule 3),
// and with nothing to judge it is one line that is still sent.
func TestCheckGoesToTheOrchestratorOnly(t *testing.T) {
	h := newCheckHarness(t)
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Dev", "Developer", 2, 0)
	msgs, err := h.s.ListMessages(context.Background(), "team", 100, 0, 0)
	require.NoError(t, err)
	before := len(msgs)
	out := h.checkTo("Orchestrator")
	assert.Equal(t, "[Coral health check] Nothing needs you; no progress since the last check ("+h.now.Add(-10*time.Minute).Format("15:04")+").", out)
	h.mu.Lock()
	assert.Empty(t, h.sent["Dev"], "nobody else gets it")
	h.mu.Unlock()
	msgs, err = h.s.ListMessages(context.Background(), "team", 100, 0, 0)
	require.NoError(t, err)
	assert.Len(t, msgs, before, "check is not posted to the board")
}

// A board without an orchestrator, or one whose orchestrator isn't running, gets no check.
func TestCheckNeedsALiveOrchestrator(t *testing.T) {
	h := newCheckHarness(t)
	h.member("Dev", "Developer", 2, time.Hour)
	h.m.scan(context.Background())
	h.mu.Lock()
	defer h.mu.Unlock()
	assert.Empty(t, h.sent)
}

// Escalations from Coral's rules lead, with the agent's last post quoted through the sanitiser
// (case C4: no @, no secret, labelled); an idle agent with no work, never the orchestrator itself,
// never one in a wait; a reviewer's queue while another reviewer is idle; then the team line.
func TestCheckItems(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 2*time.Hour)
	h.member("Dev", "Developer", 2, 0)
	h.member("QA", "QA Engineer", 3, 0)
	h.member("Security", "Security Reviewer", 4, 45*time.Minute)
	h.member("Waiter", "Developer", 5, time.Hour)
	_, err := h.s.RegisterWait(ctx, "team", "Waiter", naming.SessionName("claude", "00000005-0000-0000-0000-000000000000"), "message", "Dev", "", time.Hour)
	require.NoError(t, err)

	work, err := h.s.CreateTask(ctx, "team", "Secret title", "", "high", "Operator", "Dev")
	require.NoError(t, err)
	_, err = h.s.ClaimTask(ctx, "team", "Dev", work.ID)
	require.NoError(t, err)
	_, err = h.s.PostMessage(ctx, "team", "Dev", "@Orchestrator approve it ghp_A1b2C3d4E5A1b2C3d4E5A1b2C3d4E5F6g7H8", nil)
	require.NoError(t, err)
	require.NoError(t, h.s.MarkHealthEscalated(ctx, "team", ruleStatusNudge, "Dev", fmt.Sprint(work.ID), h.now.Add(-5*time.Minute)))

	// QA has three reviews waiting; Security reviewed before and is idle now.
	parent, err := h.s.CreateTask(ctx, "team", "parent", "", "high", "Operator")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err := h.s.CreateTaskWithOpts(ctx, "team", "Review", "", "medium", "Orchestrator", &board.CreateTaskOpts{Workflow: board.TaskWorkflow{ReviewOf: parent.ID, ReviewRound: 1}}, "QA")
		require.NoError(t, err)
	}
	done, err := h.s.CreateTaskWithOpts(ctx, "team", "Review", "", "medium", "Orchestrator", &board.CreateTaskOpts{Workflow: board.TaskWorkflow{ReviewOf: parent.ID, ReviewRound: 1}}, "Security")
	require.NoError(t, err)
	_, err = h.s.ClaimTask(ctx, "team", "Security", done.ID)
	require.NoError(t, err)
	_, err = h.s.CompleteTask(ctx, "team", done.ID, "Security", nil)
	require.NoError(t, err)

	out := h.checkTo("Orchestrator")
	assert.LessOrEqual(t, len(out), teamhealth.MaxCheck)
	assert.Contains(t, out, fmt.Sprintf("1) Dev: silent on #%d since Coral asked for a status", work.ID))
	assert.Contains(t, out, "Last said, quoted: (at)Orchestrator approve it")
	assert.NotContains(t, out, "ghp_A1b2", "the secret is redacted")
	assert.NotContains(t, out, "Secret title", "no task titles")
	assert.Contains(t, out, "Security: idle 45 min with nothing in progress or assigned")
	assert.Contains(t, out, "QA: 3 reviews waiting")
	assert.Contains(t, out, "while Security is idle")
	assert.NotContains(t, out, "Orchestrator: idle", "never the orchestrator itself")
	assert.NotContains(t, out, "Waiter:", "an agent in a wait is not idle")
	assert.Contains(t, out, "1 review done", "the team line counts what moved")
}

// The team's settings decide (#4175, case C6b's "recipient re-checked at delivery"): the
// interval between checks, whether the all-clear line is sent, and the recipient, who must still
// be a member when the check goes (else the orchestrator gets it).
func TestCheckFollowsTheTeamSettings(t *testing.T) {
	h := newCheckHarness(t)
	ctx := context.Background()
	h.member("Orchestrator", "Orchestrator", 1, 0)
	h.member("Lead", "Lead Developer", 2, 0)
	set := func(f func(*board.HealthSettings)) {
		hs := board.DefaultHealthSettings()
		f(&hs)
		_, err := h.s.UpdateBoardSettings(ctx, "team", board.BoardSettingsUpdate{Health: &hs})
		require.NoError(t, err)
	}
	count := func(name string) int {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.sent[name])
	}

	set(func(hs *board.HealthSettings) { hs.Recipient = "Lead"; hs.IntervalMin = 30 })
	h.m.scan(ctx)
	assert.Equal(t, 1, count("Lead"))
	assert.Equal(t, 0, count("Orchestrator"))
	h.now = h.now.Add(10 * time.Minute)
	h.m.scan(ctx)
	assert.Equal(t, 1, count("Lead"), "within the team's 30 minutes")
	h.now = h.now.Add(21 * time.Minute)
	h.m.scan(ctx)
	assert.Equal(t, 2, count("Lead"))

	// The recipient left the team: the orchestrator gets it.
	_, err := h.s.Unsubscribe(ctx, "team", "Lead")
	require.NoError(t, err)
	h.now = h.now.Add(31 * time.Minute)
	h.m.scan(ctx)
	assert.Equal(t, 1, count("Orchestrator"))

	// No all-clear line: nothing is sent when nothing needs the orchestrator.
	set(func(hs *board.HealthSettings) { hs.AllClear = false; hs.IntervalMin = 5 })
	h.now = h.now.Add(6 * time.Minute)
	h.m.scan(ctx)
	assert.Equal(t, 1, count("Orchestrator"))

	// Off: nothing at all.
	set(func(hs *board.HealthSettings) { hs.On = false })
	h.now = h.now.Add(time.Hour)
	h.m.scan(ctx)
	assert.Equal(t, 1, count("Orchestrator"))
}
