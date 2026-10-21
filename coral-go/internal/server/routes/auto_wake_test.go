package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptr"
	"github.com/subgentic/coral/internal/store"
)

// #3636: waking the agents that were running when a pty Coral stopped.

type wakeRig struct {
	t      *testing.T
	h      *SessionsHandler
	ss     *store.SessionStore
	mu     sync.Mutex
	sleeps []time.Duration
	now    time.Time
}

func newWakeRig(t *testing.T) *wakeRig {
	t.Helper()
	cfg := config.Load(t.TempDir())
	_, h, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	rig := &wakeRig{t: t, h: h, ss: ss, now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	h.autoWake.sleep = func(ctx context.Context, d time.Duration) bool {
		rig.mu.Lock()
		rig.sleeps = append(rig.sleeps, d)
		rig.mu.Unlock()
		return ctx.Err() == nil
	}
	h.autoWake.now = func() time.Time { return rig.now }
	// The CLI a wake types is harmless: never a real agent in a test.
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", stubCLI(t)))
	return rig
}

// add registers an agent asleep for reason ("" = the Operator's Sleep); dir "" = a temp folder.
func (r *wakeRig) add(sid, name, reason, dir string, archived bool) {
	r.t.Helper()
	ctx := context.Background()
	if dir == "" {
		dir = r.t.TempDir()
	}
	disp := name
	require.NoError(r.t, r.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentName: name, AgentType: "claude", WorkingDir: dir, DisplayName: &disp}))
	require.NoError(r.t, r.ss.SetSessionSleepingFor(ctx, sid, reason))
	if archived {
		_, err := r.h.db.ExecContext(ctx, "UPDATE live_sessions SET is_archived = 1 WHERE session_id = ?", sid)
		require.NoError(r.t, err)
	}
	time.Sleep(2 * time.Millisecond) // sleep-ok: distinct created_at for the newest-first order
}

func (r *wakeRig) asleep(sid string) (bool, string) {
	r.t.Helper()
	ls, err := r.ss.GetLiveSession(context.Background(), sid)
	require.NoError(r.t, err)
	return ls.IsSleeping == 1, ptr.Value(ls.SleepReason)
}

func sid(n int) string { return fmt.Sprintf("0b2f6c1e-3d4a-4b5c-8d6e-%012d", n) }

func TestAutoWakeWakesOnlyTheAgentsCoralStopped(t *testing.T) {
	r := newWakeRig(t)
	r.add(sid(1), "one", store.SleepReasonCoralStopped, "", false)
	r.add(sid(2), "two", store.SleepReasonCoralStopped, "", false)
	r.add(sid(3), "three", store.SleepReasonCoralStopped, "", false)
	r.add(sid(4), "slept-by-the-operator", "", "", false)
	r.add(sid(5), "archived", store.SleepReasonCoralStopped, "", true)
	r.add(sid(6), "terminal-gone", store.SleepReasonTerminalGone, "", false)

	r.h.AutoWakeAfterRestart(context.Background())

	for _, n := range []int{1, 2, 3} {
		sleeping, reason := r.asleep(sid(n))
		assert.False(t, sleeping, "agent %d woken", n)
		assert.Empty(t, reason, "agent %d: the reason is cleared", n)
	}
	for _, n := range []int{4, 5, 6} {
		sleeping, _ := r.asleep(sid(n))
		assert.True(t, sleeping, "agent %d stays asleep (not coral_stopped, or archived)", n)
	}
	st := r.h.autoWake.snapshot()
	assert.Equal(t, "done", st.State)
	assert.Equal(t, 3, st.Woken)
	// 5 s before the first; then each wake 2 s plus up to 1 s apart.
	require.Len(t, r.sleeps, 3)
	assert.Equal(t, autoWakeStartDelay, r.sleeps[0])
	for _, d := range r.sleeps[1:] {
		assert.True(t, d >= autoWakeGap && d < autoWakeGap+autoWakeJitter, "gap %v", d)
	}
}

func TestAutoWakeOffAndOverTheCap(t *testing.T) {
	r := newWakeRig(t)
	for i := 1; i <= 3; i++ {
		r.add(sid(i), fmt.Sprintf("a%d", i), store.SleepReasonCoralStopped, "", false)
	}
	require.NoError(t, r.ss.SetSetting(context.Background(), AutoWakeSetting, "false"))
	r.h.AutoWakeAfterRestart(context.Background())
	st := r.h.autoWake.snapshot()
	assert.Equal(t, "off", st.State)
	assert.Equal(t, 3, st.Waiting)
	sleeping, reason := r.asleep(sid(1))
	assert.True(t, sleeping, "off: nothing woken")
	assert.Equal(t, store.SleepReasonCoralStopped, reason, "and they stay marked for a later start")

	require.NoError(t, r.ss.SetSetting(context.Background(), AutoWakeSetting, "true"))
	require.NoError(t, r.ss.SetSetting(context.Background(), AutoWakeCapSetting, "2"))
	r.h.AutoWakeAfterRestart(context.Background())
	st = r.h.autoWake.snapshot()
	assert.Equal(t, "pending", st.State, "over the cap: the Operator decides")
	assert.Len(t, st.Pending, 3)
	for i := 1; i <= 3; i++ {
		sleeping, _ := r.asleep(sid(i))
		assert.True(t, sleeping, "none woken over the cap")
	}
	_, err := os.Stat(filepath.Join(r.h.cfg.CoralDir(), autoWakeRunsFile))
	assert.True(t, os.IsNotExist(err), "a pass that woke nothing isn't counted by the crash-loop guard")
}

// Security's point b: three starts in a row wake once, then say they skipped.
func TestAutoWakeCrashLoopGuard(t *testing.T) {
	r := newWakeRig(t)
	for start := 1; start <= 3; start++ {
		r.add(sid(start), fmt.Sprintf("s%d", start), store.SleepReasonCoralStopped, "", false)
		r.h.autoWake.status = autoWakeStatus{}
		r.h.AutoWakeAfterRestart(context.Background())
		st := r.h.autoWake.snapshot()
		if start == 1 {
			assert.Equal(t, "done", st.State, "start 1 wakes")
		} else {
			assert.Equal(t, "guarded", st.State, "start %d is guarded", start)
			sleeping, _ := r.asleep(sid(start))
			assert.True(t, sleeping, "start %d woke nothing", start)
		}
		r.now = r.now.Add(time.Minute) // a crash loop: a minute apart
	}
	// Spread out but 3 in the last hour: still guarded.
	runs, _ := json.Marshal([]time.Time{r.now.Add(-50 * time.Minute), r.now.Add(-35 * time.Minute), r.now.Add(-20 * time.Minute)})
	require.NoError(t, os.WriteFile(filepath.Join(r.h.cfg.CoralDir(), autoWakeRunsFile), runs, 0o600))
	guarded, why := r.h.autoWakeGuarded()
	assert.True(t, guarded, why)
	// An hour and more after: allowed again.
	r.now = r.now.Add(2 * time.Hour)
	guarded, _ = r.h.autoWakeGuarded()
	assert.False(t, guarded)
}

func TestAutoWakeSkipsAGoneFolderAndGoesOn(t *testing.T) {
	r := newWakeRig(t)
	gone := filepath.Join(t.TempDir(), "removed-worktree")
	r.add(sid(1), "kept", store.SleepReasonCoralStopped, "", false)
	r.add(sid(2), "gone", store.SleepReasonCoralStopped, gone, false)
	r.h.AutoWakeAfterRestart(context.Background())
	st := r.h.autoWake.snapshot()
	assert.Equal(t, 1, st.Woken)
	require.Len(t, st.Failed, 1)
	assert.Equal(t, "gone", st.Failed[0].Name)
	assert.Equal(t, "its folder is gone", st.Failed[0].Reason)
	sleeping, reason := r.asleep(sid(2))
	assert.True(t, sleeping)
	assert.Equal(t, store.SleepReasonCoralStopped, reason)
}

func TestAutoWakeStopsWhenCoralStops(t *testing.T) {
	r := newWakeRig(t)
	for i := 1; i <= 3; i++ {
		r.add(sid(i), fmt.Sprintf("a%d", i), store.SleepReasonCoralStopped, "", false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	r.h.autoWake.sleep = func(c context.Context, d time.Duration) bool {
		calls++
		if calls == 2 { // during the first gap
			cancel()
		}
		return c.Err() == nil
	}
	r.h.AutoWakeAfterRestart(ctx)
	woken := 0
	for i := 1; i <= 3; i++ {
		if s, _ := r.asleep(sid(i)); !s {
			woken++
		}
	}
	assert.Equal(t, 1, woken, "the pass stops when Coral stops")
}

// Security's point e: one notice from Coral, addressed to that agent alone.
func TestAutoWakeNoticeGoesToThatAgentOnly(t *testing.T) {
	r := newWakeRig(t)
	ctx := context.Background()
	const team = "team-3636"
	// The agent's board row is the one its wake re-subscribes (subscriber ID and title: its
	// display name), so the lookup after the wake can't pick another row of the same session.
	const name = "Dev \u202eOne\x07"
	r.add(sid(1), name, store.SleepReasonCoralStopped, "", false)
	_, err := r.h.db.ExecContext(ctx, "UPDATE live_sessions SET board_name = ? WHERE session_id = ?", team, sid(1))
	require.NoError(t, err)
	_, err = r.h.bs.Subscribe(ctx, team, naming.SubscriberID(name, "claude"), name, naming.SessionName("claude", sid(1)), nil, "mentions")
	require.NoError(t, err)
	_, err = r.h.bs.Subscribe(ctx, team, "orch-sub", "Orchestrator", "claude-orchestrator", nil, "all")
	require.NoError(t, err)
	_, err = r.h.bs.Subscribe(ctx, team, "qa-sub", "QA", "claude-qa", nil, "mentions")
	require.NoError(t, err)

	r.h.AutoWakeAfterRestart(ctx)

	msgs, err := r.h.bs.ListMessages(ctx, team, 50, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "one notice")
	m := msgs[0]
	assert.Equal(t, board.CoralSender, m.SubscriberID)
	assert.Equal(t, "@Dev One "+AutoWakeNotice, m.Content, "fixed text; the name without control or bidi characters")
	assert.True(t, m.IsSystem)
	for who, want := range map[string]int{"orch-sub": 0, "qa-sub": 0} {
		n, err := r.h.bs.CheckUnread(ctx, team, who)
		require.NoError(t, err)
		assert.Equal(t, want, n, "%s isn't nudged by another agent's notice", who)
	}
}

// Security's point a: no request can set coral_stopped; the Operator's Sleep is reason "".
func TestNoRouteSetsCoralStopped(t *testing.T) {
	cfg := config.Load(t.TempDir())
	_, h, _, ss := setupSessionsTestServerWithConfig(t, cfg)
	ctx := context.Background()
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid(1), AgentName: "a", AgentType: "claude", WorkingDir: t.TempDir()}))
	r := chi.NewRouter()
	r.Use(testIdentity(h.db))
	r.Post("/api/sessions/live/{sessionID}/sleep", h.SleepSession)
	body := `{"sleep_reason":"coral_stopped","reason":"coral_stopped"}`
	req := httptest.NewRequest("POST", "/api/sessions/live/"+sid(1)+"/sleep", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	ls, err := ss.GetLiveSession(ctx, sid(1))
	require.NoError(t, err)
	assert.NotEqual(t, store.SleepReasonCoralStopped, ptr.Value(ls.SleepReason), "a request's reason is never taken (%d %s)", rec.Code, rec.Body.String())
}

func TestAutoWakeSettingsAreValidated(t *testing.T) {
	for _, bad := range []map[string]any{
		{AutoWakeSetting: "yes"}, {AutoWakeSetting: 1.0}, {AutoWakeCapSetting: 0.0}, {AutoWakeCapSetting: 51.0},
		{AutoWakeCapSetting: 2.5}, {AutoWakeCapSetting: "x"}, {AutoWakeCapSetting: true},
	} {
		assert.Error(t, validateAutoWakeSettings(bad), "%v", bad)
	}
	ok := map[string]any{AutoWakeSetting: false, AutoWakeCapSetting: 12.0}
	require.NoError(t, validateAutoWakeSettings(ok))
	assert.Equal(t, "false", ok[AutoWakeSetting])
	assert.Equal(t, "12", ok[AutoWakeCapSetting])
	ok = map[string]any{AutoWakeSetting: "true", AutoWakeCapSetting: "1"}
	require.NoError(t, validateAutoWakeSettings(ok))
	assert.Equal(t, 8, autoWakeCap(map[string]string{}), "default 8")
	assert.Equal(t, 8, autoWakeCap(map[string]string{AutoWakeCapSetting: "99"}), "out of range: the default")
}

func TestDismissAutoWakeNeedsAPerson(t *testing.T) {
	r := newWakeRig(t)
	r.h.autoWake.status = autoWakeStatus{State: "pending", Pending: []autoWakeAgent{{SessionID: sid(1), Name: "a"}}}
	req := httptest.NewRequest("POST", "/api/system/auto-wake/dismiss", nil)
	req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "s1"}))
	req.Header.Set(store.SessionTokenHeader, "t")
	rec := httptest.NewRecorder()
	r.h.DismissAutoWake(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Len(t, r.h.autoWake.snapshot().Pending, 1)

	req = httptest.NewRequest("POST", "/api/system/auto-wake/dismiss", nil)
	req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
	rec = httptest.NewRecorder()
	r.h.DismissAutoWake(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, r.h.autoWake.snapshot().Pending)
}

// The Operator's Wake of one agent from the over-the-cap list sends its notice and takes it off.
func TestPendingAutoWakeIsTakenOnWake(t *testing.T) {
	r := newWakeRig(t)
	r.h.autoWake.status = autoWakeStatus{State: "pending", Pending: []autoWakeAgent{{SessionID: sid(1)}, {SessionID: sid(2)}}}
	assert.True(t, r.h.takePendingAutoWake(sid(1)))
	assert.False(t, r.h.takePendingAutoWake(sid(1)))
	st := r.h.autoWake.snapshot()
	assert.Equal(t, "pending", st.State)
	assert.True(t, r.h.takePendingAutoWake(sid(2)))
	assert.Equal(t, "done", r.h.autoWake.snapshot().State)
}

func TestCleanAgentName(t *testing.T) {
	assert.Equal(t, "Dev One", cleanAgentName("Dev \u202eOne\x07"))
	assert.Equal(t, "ab", cleanAgentName("a\u200fb\u2066"))
	assert.Len(t, []rune(cleanAgentName(strings.Repeat("x", 200))), 80)
	// Zero-width and tag characters (Security's NIT), and a name on one line.
	assert.Equal(t, "Dev One", cleanAgentName("De\u200bv\u200d One\U000E0041"))
	assert.Equal(t, "a b", cleanAgentName("a\n\tb"))
}

// The named agent is nudged by its notice (an ordinary title), and nobody else is.
func TestAutoWakeNoticeNudgesTheNamedAgent(t *testing.T) {
	r := newWakeRig(t)
	ctx := context.Background()
	const team = "team-3636b"
	r.add(sid(1), "dev", store.SleepReasonCoralStopped, "", false)
	_, err := r.h.db.ExecContext(ctx, "UPDATE live_sessions SET board_name = ? WHERE session_id = ?", team, sid(1))
	require.NoError(t, err)
	// The agent's subscription is made under the id the wake itself uses ("dev", its name): waking it subscribes it again under that id, so a subscription under another id would leave
	// TWO active ones for the one session, and the notice is addressed by the newest of them (ties in subscribed_at, within a second, picked either): the notice then named a title this
	// subscriber did not have in about 1 run in 600 (#5573). In a team the agent has one subscription, as here.
	_, err = r.h.bs.Subscribe(ctx, team, "dev", "Frontend Dev", naming.SessionName("claude", sid(1)), nil, "mentions")
	require.NoError(t, err)
	_, err = r.h.bs.Subscribe(ctx, team, "orch-sub", "Orchestrator", "claude-orchestrator", nil, "all")
	require.NoError(t, err)
	r.h.AutoWakeAfterRestart(ctx)
	// Exactly one subscription of the agent's session remains: the test would be ambiguous otherwise.
	var subs int
	require.NoError(t, r.h.bs.Reader().GetContext(ctx, &subs, "SELECT COUNT(*) FROM board_subscribers WHERE project = ? AND session_name = ? AND is_active = 1", team, naming.SessionName("claude", sid(1))))
	assert.Equal(t, 1, subs, "one active subscription for the woken session")
	for who, want := range map[string]int{"dev": 1, "orch-sub": 0} {
		n, err := r.h.bs.CheckUnread(ctx, team, who)
		require.NoError(t, err)
		assert.Equal(t, want, n, who)
	}
}
