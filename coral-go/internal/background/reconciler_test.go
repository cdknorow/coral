package background

import (
	"context"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
)

// reconMockRuntime lets tests control what ListAgents reports and what
// IsAlive answers, independently.
type reconMockRuntime struct {
	listed []AgentInfo
	alive  map[string]bool
	killed []string
}

func (m *reconMockRuntime) SpawnAgent(context.Context, string, string, string, string) error {
	return nil
}
func (m *reconMockRuntime) SendInput(context.Context, string, string) error { return nil }
func (m *reconMockRuntime) KillAgent(_ context.Context, name string) error {
	m.killed = append(m.killed, name)
	return nil
}
func (m *reconMockRuntime) IsAlive(_ context.Context, name string) bool     { return m.alive[name] }
func (m *reconMockRuntime) ListAgents(context.Context) ([]AgentInfo, error) { return m.listed, nil }

func newReconcilerFixture(t *testing.T) (*store.SessionStore, *reconMockRuntime, *SessionReconciler) {
	ss, rt, r, _ := newReconcilerFixtureDB(t)
	return ss, rt, r
}

func newReconcilerFixtureDB(t *testing.T) (*store.SessionStore, *reconMockRuntime, *SessionReconciler, *store.DB) {
	t.Helper()
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	rt := &reconMockRuntime{alive: map[string]bool{}}
	return ss, rt, NewSessionReconciler(ss, rt, time.Hour), db
}

func registerAwake(t *testing.T, ss *store.SessionStore, agentType, sid string) {
	t.Helper()
	err := ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID:  sid,
		AgentType:  agentType,
		AgentName:  "coral-go",
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
}

func isSleeping(t *testing.T, ss *store.SessionStore, sid string) bool {
	t.Helper()
	ls, err := ss.GetLiveSession(context.Background(), sid)
	if err != nil || ls == nil {
		t.Fatalf("get live session: %v", err)
	}
	return ls.IsSleeping == 1
}

func TestReconciler_ListedAgentStaysAwake(t *testing.T) {
	ss, rt, r := newReconcilerFixture(t)
	registerAwake(t, ss, "codex", testUUID)
	rt.listed = []AgentInfo{{AgentType: "codex", SessionID: testUUID}}

	for i := 0; i < 3; i++ {
		r.reconcileOnce(context.Background())
	}
	if isSleeping(t, ss, testUUID) {
		t.Fatal("listed agent should not be marked sleeping")
	}
}

// Regression: Codex rewrites its pane title to "[ ! ] Action Required | coral"
// while waiting on approval. Before the title was moved to the last field of
// the list-panes format, the '|' shifted the parsed fields so the agent
// vanished from ListAgents and was falsely marked sleeping. Even if discovery
// misses an agent, a successful IsAlive probe must keep it awake.
func TestReconciler_MissingFromListButAliveStaysAwake(t *testing.T) {
	ss, rt, r := newReconcilerFixture(t)
	registerAwake(t, ss, "codex", testUUID)
	rt.listed = nil
	rt.alive[FormatSessionName("codex", testUUID)] = true

	for i := 0; i < 3; i++ {
		r.reconcileOnce(context.Background())
	}
	if isSleeping(t, ss, testUUID) {
		t.Fatal("agent with a live session must not be marked sleeping")
	}
	if len(r.misses) != 0 {
		t.Fatalf("expected no recorded misses, got %v", r.misses)
	}
}

func TestReconciler_DeadAgentSleepsAfterThreshold(t *testing.T) {
	ss, rt, r := newReconcilerFixture(t)
	registerAwake(t, ss, "codex", testUUID)
	rt.listed = nil // not discovered, and IsAlive is false

	for i := 1; i < reconcileMissThreshold; i++ {
		r.reconcileOnce(context.Background())
		if isSleeping(t, ss, testUUID) {
			t.Fatalf("marked sleeping after %d miss(es), threshold is %d", i, reconcileMissThreshold)
		}
	}
	r.reconcileOnce(context.Background())
	if !isSleeping(t, ss, testUUID) {
		t.Fatal("dead agent should be marked sleeping once the miss threshold is reached")
	}
	if _, ok := r.misses[testUUID]; ok {
		t.Fatal("miss counter should be cleared after marking sleeping")
	}
}

func TestReconciler_TransientMissResets(t *testing.T) {
	ss, rt, r := newReconcilerFixture(t)
	registerAwake(t, ss, "claude", testUUID)

	// One miss...
	rt.listed = nil
	r.reconcileOnce(context.Background())
	// ...then it shows up again, which should reset the counter.
	rt.listed = []AgentInfo{{AgentType: "claude", SessionID: testUUID}}
	r.reconcileOnce(context.Background())
	// ...then another single miss must not be enough to sleep it.
	rt.listed = nil
	r.reconcileOnce(context.Background())

	if isSleeping(t, ss, testUUID) {
		t.Fatal("non-consecutive misses must not mark the agent sleeping")
	}
}

func TestReconciler_UppercaseSessionIDMatches(t *testing.T) {
	ss, rt, r := newReconcilerFixture(t)
	registerAwake(t, ss, "codex", testUUID)
	rt.listed = []AgentInfo{{AgentType: "codex", SessionID: "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"}}

	for i := 0; i < 3; i++ {
		r.reconcileOnce(context.Background())
	}
	if isSleeping(t, ss, testUUID) {
		t.Fatal("session ID comparison should be case-insensitive")
	}
}

func TestReconciler_AlreadySleepingUntouched(t *testing.T) {
	ss, rt, r := newReconcilerFixture(t)
	registerAwake(t, ss, "codex", testUUID)
	if err := ss.SetSessionSleeping(context.Background(), testUUID, true); err != nil {
		t.Fatal(err)
	}
	rt.listed = nil
	for i := 0; i < 3; i++ {
		r.reconcileOnce(context.Background())
	}
	if !isSleeping(t, ss, testUUID) {
		t.Fatal("sleeping session should remain sleeping")
	}
	if len(r.misses) != 0 {
		t.Fatalf("sleeping sessions should not accrue misses, got %v", r.misses)
	}
}

// Orphan: the runtime still reports a session whose DB row Coral already
// marked stopped (e.g. a kill that missed). Within the grace period it is
// left alone; after it, the reconciler kills it.
func TestReconciler_KillsOrphanAfterGracePeriod(t *testing.T) {
	ss, rt, r, db := newReconcilerFixtureDB(t)
	registerAwake(t, ss, "codex", testUUID)
	if err := ss.UnregisterLiveSession(context.Background(), testUUID); err != nil {
		t.Fatal(err)
	}
	rt.listed = []AgentInfo{{AgentType: "codex", SessionID: testUUID}}

	r.reconcileOnce(context.Background())
	if len(rt.killed) != 0 {
		t.Fatalf("orphan killed inside grace period: %v", rt.killed)
	}

	old := time.Now().UTC().Add(-orphanGracePeriod - time.Minute).Format(store.ISOFormat)
	if _, err := db.ExecContext(context.Background(),
		"UPDATE live_sessions SET stopped_at = ? WHERE session_id = ?", old, testUUID); err != nil {
		t.Fatal(err)
	}

	r.reconcileOnce(context.Background())
	want := FormatSessionName("codex", testUUID)
	if len(rt.killed) != 1 || rt.killed[0] != want {
		t.Fatalf("killed = %v, want [%s]", rt.killed, want)
	}
}

func TestReconciler_DoesNotKillActiveOrUnknownSessions(t *testing.T) {
	ss, rt, r := newReconcilerFixture(t)
	registerAwake(t, ss, "codex", testUUID)
	unknown := "12345678-1234-1234-1234-123456789abc"
	rt.listed = []AgentInfo{
		{AgentType: "codex", SessionID: testUUID}, // active row
		{AgentType: "claude", SessionID: unknown}, // no row at all
	}
	r.reconcileOnce(context.Background())
	if len(rt.killed) != 0 {
		t.Fatalf("unexpected kills: %v", rt.killed)
	}
}
