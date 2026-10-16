package routes

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/store"
)

// #3613 A: the exit notice and auto-resume for agents that died on their own.

const exitTeam = "rate-team"

type timer struct {
	d time.Duration
	f func()
}

type exitRig struct {
	*wakeRig
	tmu    sync.Mutex
	timers []timer
	notifs *NotificationStore
}

func newExitRig(t *testing.T) *exitRig {
	r := &exitRig{wakeRig: newWakeRig(t), notifs: NewNotificationStore()}
	r.h.exits.after = func(d time.Duration, f func()) {
		r.tmu.Lock()
		r.timers = append(r.timers, timer{d, f})
		r.tmu.Unlock()
	}
	r.h.SetNotificationStore(r.notifs)
	return r
}

// takeTimers returns the timers set so far and forgets them.
func (r *exitRig) takeTimers() []timer {
	r.tmu.Lock()
	defer r.tmu.Unlock()
	out := r.timers
	r.timers = nil
	return out
}

// agent registers a board agent (job title title) that the reconciler just found dead; failure
// is its last turn's StopFailure text ("" = a normal turn end).
func (r *exitRig) agent(n int, title, failure string) string {
	r.t.Helper()
	ctx := context.Background()
	id := sid(n)
	team := exitTeam
	disp := title
	require.NoError(r.t, r.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: id, AgentName: title, AgentType: "claude", WorkingDir: r.t.TempDir(), DisplayName: &disp, BoardName: &team}))
	_, err := r.h.bs.Subscribe(ctx, exitTeam, title, title, naming.SessionName("claude", id), nil, "")
	require.NoError(r.t, err)
	r.event(id, failure)
	r.die(id)
	return id
}

func (r *exitRig) event(id, failure string) {
	r.t.Helper()
	detail := `{"reason":"end_turn"}`
	if failure != "" {
		b, _ := json.Marshal(map[string]any{"failed": true, "error": failure})
		detail = string(b)
	}
	_, err := store.NewTaskStore(r.h.db).InsertAgentEvent(context.Background(), &store.AgentEvent{AgentName: "a", SessionID: &id, EventType: "stop", Summary: "stop", DetailJSON: &detail})
	require.NoError(r.t, err)
}

func (r *exitRig) die(id string) {
	require.NoError(r.t, r.ss.SetSessionSleeping(context.Background(), id, true))
}

func (r *exitRig) notices() []string {
	r.t.Helper()
	msgs, err := r.h.bs.ListMessages(context.Background(), exitTeam, 100, 0, 0)
	require.NoError(r.t, err)
	var out []string
	for _, m := range msgs {
		out = append(out, m.SubscriberID+"|"+m.Content)
	}
	return out
}

func (r *exitRig) dashboard() []Notification {
	n, _ := r.notifs.Connect()
	return n
}

func (r *exitRig) attempts(id string) int { return r.h.autoResumeAttempts()[id] }

func TestExitLabelIsFixed(t *testing.T) {
	cases := map[string]string{
		`API Error: 429 {"type":"error","error":{"type":"rate_limit_error"}} sk-ant-secret`: "rate limited",
		"You've hit your rate limit":                       "rate limited",
		"API Error: 529 Overloaded":                        "overloaded",
		"Repeated server errors at /Users/someone/project": "API error",
		"": "API error",
	}
	for text, want := range cases {
		assert.Equal(t, want, exitLabel(text, true), text)
	}
	assert.Equal(t, "exited", exitLabel("429", false), "a normal turn end")
}

func TestExitNoticeOncePerBurstWithFixedLabels(t *testing.T) {
	r := newExitRig(t)
	secret := `API Error: 429 rate_limit_error request_id=req_SECRET /Users/someone`
	a := r.agent(1, "Backend Dev", secret)
	b := r.agent(2, "QA", "API Error: 529 Overloaded")
	c := r.agent(3, "Frontend", "")

	// Two reconciler passes of one burst: one notice for the team.
	r.h.AgentsSlept(context.Background(), []string{a, b})
	r.h.AgentsSlept(context.Background(), []string{c})
	timers := r.takeTimers()
	require.Len(t, timers, 1, "one flush per team per burst")
	assert.Equal(t, exitBurstWindow, timers[0].d)
	timers[0].f()

	notes := r.notices()
	require.Len(t, notes, 1, "one board post")
	post := notes[0]
	assert.True(t, strings.HasPrefix(post, exitNoticeSender+"|3 agents stopped:"), post)
	assert.Contains(t, post, "Backend Dev: rate limited. Wake resumes it.")
	assert.Contains(t, post, "QA: overloaded. Wake resumes it.")
	assert.Contains(t, post, "Frontend: exited. Wake resumes it.")
	assert.NotContains(t, post, "@", "no mention: workers aren't nudged")
	for _, raw := range []string{"SECRET", "/Users", "529", "429", "rate_limit_error"} {
		assert.NotContains(t, post, raw, "the CLI's text never leaves the server")
	}
	dash := r.dashboard()
	require.Len(t, dash, 1, "one dashboard notification")
	assert.Equal(t, "3 agents stopped", dash[0].Title)
	assert.Equal(t, "warning", dash[0].Level)
	assert.True(t, strings.HasPrefix(dash[0].Message, "On "+exitTeam+": "), dash[0].Message)
	assert.NotContains(t, dash[0].Message, "SECRET")

	// Auto-resume is off by default: nothing scheduled.
	assert.Empty(t, r.takeTimers())
}

func TestExitNoticeSkipsAgentsWokenAndCleansNames(t *testing.T) {
	r := newExitRig(t)
	a := r.agent(1, "@all ​Worker‮\x1b[31m", "429")
	b := r.agent(2, "Woken", "429")
	r.h.AgentsSlept(context.Background(), []string{a, b})
	require.NoError(t, r.ss.SetSessionSleeping(context.Background(), b, false)) // woken within the window
	r.takeTimers()[0].f()
	notes := r.notices()
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "1 agent stopped:\n- all Worker: rate limited.")
	assert.NotContains(t, notes[0], "Woken")
	assert.NotContains(t, notes[0], "@")
	assert.NotContains(t, notes[0], "​")
	assert.NotContains(t, notes[0], "\x1b")
	assert.NotContains(t, notes[0], "[31m")

	// Everyone woken: nothing is sent.
	c := r.agent(3, "Gone", "429")
	r.h.AgentsSlept(context.Background(), []string{c})
	require.NoError(t, r.ss.SetSessionSleeping(context.Background(), c, false))
	r.takeTimers()[0].f()
	assert.Len(t, r.notices(), 1)
	assert.Len(t, r.dashboard(), 1)
}

func TestAutoResumeIsBoundedAndOnlyTheOperatorResetsIt(t *testing.T) {
	r := newExitRig(t)
	ctx := context.Background()
	require.NoError(t, r.ss.SetSetting(ctx, AutoResumeSetting, "true"))
	a := r.agent(1, "Backend Dev", "API Error: 429")
	clean := r.agent(2, "Done", "") // a normal exit is never resumed

	burst := func(ids ...string) []timer {
		t.Helper()
		r.h.AgentsSlept(ctx, ids)
		flush := r.takeTimers()
		require.Len(t, flush, 1)
		flush[0].f()
		return r.takeTimers()
	}
	var last timer
	for i, base := range autoResumeDelays {
		resume := burst(a, clean)
		require.Len(t, resume, 1, "attempt %d: only the agent stopped by an API error", i+1)
		d := resume[0].d
		assert.True(t, d >= base*8/10 && d <= base*12/10 && d > 0, "attempt %d: %v within 20%% of %v", i+1, d, base)
		resume[0].f()
		sleeping, _ := r.asleep(a)
		assert.False(t, sleeping, "attempt %d wakes it", i+1)
		assert.Equal(t, i+1, r.attempts(a))
		last = resume[0]
		// It dies again on the same error.
		r.event(a, "API Error: 429")
		r.die(a)
		if i == 0 {
			// An unrelated event doesn't reset the count.
			r.event(a, "")
			r.event(a, "API Error: 429")
			assert.Equal(t, 1, r.attempts(a))
		}
		require.NoError(t, r.ss.SetSessionSleeping(ctx, clean, true))
	}
	// After the third: no more attempts, and the notice says so.
	assert.Empty(t, burst(a), "Coral stops trying for good")
	notes := r.notices()
	assert.Contains(t, notes[len(notes)-1], "Backend Dev: rate limited. Coral tried 3 times and has stopped; Wake resumes it.")
	// A stale timer does nothing.
	last.f()
	sleeping, _ := r.asleep(a)
	assert.True(t, sleeping, "a stale timer doesn't wake it")
	assert.Equal(t, 3, r.attempts(a))

	// The Operator's wake starts the count again.
	r.h.resetAutoResume(a)
	assert.Equal(t, 0, r.attempts(a))
	resume := burst(a)
	require.Len(t, resume, 1)
	assert.True(t, resume[0].d <= autoResumeDelays[0]*12/10, "back to the first delay")

	// Turned off meanwhile: the timer does nothing.
	require.NoError(t, r.ss.SetSetting(ctx, AutoResumeSetting, "false"))
	resume[0].f()
	sleeping, _ = r.asleep(a)
	assert.True(t, sleeping)
}

func TestAutoResumeSettingIsABoolean(t *testing.T) {
	for _, bad := range []any{"yes", 1.0, nil, "TRUE"} {
		assert.Error(t, validateAutoResumeSetting(map[string]any{AutoResumeSetting: bad}), "%v", bad)
	}
	body := map[string]any{AutoResumeSetting: true}
	require.NoError(t, validateAutoResumeSetting(body))
	assert.Equal(t, "true", body[AutoResumeSetting])
	require.NoError(t, validateAutoResumeSetting(map[string]any{AutoResumeSetting: "false"}))
	require.NoError(t, validateAutoResumeSetting(map[string]any{}))
}
