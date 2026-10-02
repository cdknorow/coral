package routes

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/ptymanager"
	"github.com/stretchr/testify/require"
)

// gatedReminderTerminal records nudges. When gate is non-nil, SendInput signals
// entered and then blocks until gate is closed, deliberately ignoring ctx like
// the PTY terminal does.
type gatedReminderTerminal struct {
	ptymanager.SessionTerminal
	mu      sync.Mutex
	sent    []string
	gate    chan struct{}
	entered chan struct{}
}

func (g *gatedReminderTerminal) SendInput(_ context.Context, _, command, _, _ string) error {
	g.mu.Lock()
	gate, entered := g.gate, g.entered
	g.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		<-gate
	}
	g.mu.Lock()
	g.sent = append(g.sent, command)
	g.mu.Unlock()
	return nil
}

func (g *gatedReminderTerminal) messages() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.sent...)
}

func reminderTestHandler(t *testing.T, projects ...string) (*BoardHandler, *gatedReminderTerminal) {
	t.Helper()
	_, h := setupBoardTestServer(t)
	for _, p := range projects {
		_, err := h.bs.Subscribe(context.Background(), p, "agent", "Dev", "claude-"+p, nil, nil, "all")
		require.NoError(t, err)
	}
	term := &gatedReminderTerminal{}
	h.SetTerminal(term)
	return h, term
}

func waitClosed(t *testing.T, done <-chan struct{}, within time.Duration) bool {
	t.Helper()
	select {
	case <-done:
		return true
	case <-time.After(within):
		return false
	}
}

func TestReminderDeleteFailurePreservesActiveReminderAndRow(t *testing.T) {
	server, h := setupBoardTestServer(t)
	base := server.URL + "/api/board/myproject"
	resp := postJSON(t, base+"/subscribe", map[string]string{"subscriber_id": "Orchestrator", "job_title": "Orchestrator", "session_name": "claude-orchestrator"})
	resp.Body.Close()
	resp = postJSON(t, base+"/reminder", map[string]any{"subscriber_id": "Orchestrator", "message": "Check the board", "interval_seconds": 30})
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	realDelete := h.bs.DeleteSubscriberReminder
	injected := context.DeadlineExceeded
	h.deleteSubscriberReminder = func(context.Context, string, string) error { return injected }
	del := func() int {
		req, err := http.NewRequest(http.MethodDelete, base+"/reminder", strings.NewReader(`{"subscriber_id":"Orchestrator"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		r.Body.Close()
		return r.StatusCode
	}
	require.Equal(t, http.StatusInternalServerError, del())
	require.True(t, h.HasSubscriberReminder("myproject", "Orchestrator"), "failed delete must keep the active timer")
	seconds, active := h.SubscriberReminderInterval("myproject", "Orchestrator")
	require.True(t, active)
	require.Equal(t, 30, seconds)
	rows, err := h.bs.ListSubscriberReminders(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1, "failed delete must keep the durable row")

	// Once persistence recovers, the same request removes both.
	h.deleteSubscriberReminder = realDelete
	require.Equal(t, http.StatusOK, del())
	require.False(t, h.HasSubscriberReminder("myproject", "Orchestrator"))
	rows, err = h.bs.ListSubscriberReminders(context.Background())
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestReminderStopWaitsForInFlightNudgeThenNothingFollows(t *testing.T) {
	h, term := reminderTestHandler(t, "p")
	term.gate, term.entered = make(chan struct{}), make(chan struct{}, 1)
	h.startSubscriberReminder("p", "agent", "ping", 5*time.Millisecond)
	require.True(t, waitClosed(t, chanOf(term.entered), 2*time.Second), "timer should fire")

	stopped := make(chan struct{})
	go func() { h.stopSubscriberReminder("p", "agent"); close(stopped) }()
	require.False(t, waitClosed(t, stopped, 100*time.Millisecond), "stop must wait for the in-flight nudge even though the terminal ignores ctx")

	close(term.gate)
	require.True(t, waitClosed(t, stopped, 2*time.Second), "stop returns once the nudge finished")
	require.Equal(t, []string{"[Coral reminder] ping"}, term.messages(), "the in-flight nudge completed before stop returned")

	time.Sleep(60 * time.Millisecond) // many tick intervals
	require.Len(t, term.messages(), 1, "no nudge may be emitted after stop returned")
	require.False(t, h.HasSubscriberReminder("p", "agent"))
}

func TestReminderReplacementWaitsForOldNudgeAndSurvivesOldCleanup(t *testing.T) {
	h, term := reminderTestHandler(t, "p")
	term.gate, term.entered = make(chan struct{}), make(chan struct{}, 1)
	h.startSubscriberReminder("p", "agent", "old", 5*time.Millisecond)
	require.True(t, waitClosed(t, chanOf(term.entered), 2*time.Second))

	replaced := make(chan struct{})
	go func() {
		h.startSubscriberReminder("p", "agent", "new", 5*time.Millisecond)
		close(replaced)
	}()
	require.False(t, waitClosed(t, replaced, 100*time.Millisecond), "replacement waits for the old in-flight nudge")
	close(term.gate)
	require.True(t, waitClosed(t, replaced, 2*time.Second))

	require.Eventually(t, func() bool {
		for _, m := range term.messages() {
			if m == "[Coral reminder] new" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "replacement keeps firing after the old goroutine's cleanup")
	require.True(t, h.HasSubscriberReminder("p", "agent"), "old cleanup must not erase the replacement")
	seen := term.messages()
	require.Equal(t, "[Coral reminder] old", seen[0], "only the one in-flight old nudge was emitted")
	for _, m := range seen[1:] {
		require.Equal(t, "[Coral reminder] new", m, "no old-reminder nudge after replacement")
	}
	h.stopSubscriberReminder("p", "agent")
}

func TestReminderStopIsScopedToTeamAndSubscriber(t *testing.T) {
	h, term := reminderTestHandler(t, "team-one", "team-two")
	h.startSubscriberReminder("team-one", "agent", "one", 5*time.Millisecond)
	h.startSubscriberReminder("team-two", "agent", "two", 5*time.Millisecond)
	h.stopSubscriberReminder("team-one", "agent")
	require.False(t, h.HasSubscriberReminder("team-one", "agent"))
	require.True(t, h.HasSubscriberReminder("team-two", "agent"), "same subscriber id on another team is untouched")
	require.Eventually(t, func() bool {
		for _, m := range term.messages() {
			if m == "[Coral reminder] two" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)
	h.stopSubscriberReminder("team-two", "agent")
}

// chanOf adapts a receive-only signal channel into a closed-on-signal channel.
func chanOf(signal <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() { <-signal; close(done) }()
	return done
}
