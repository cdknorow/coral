package background

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// failingRuntime fails the first fails[session] sends to a session, then records them.
type failingRuntime struct {
	mockRuntime
	fmu   sync.Mutex
	fails map[string]int
}

func (f *failingRuntime) SendInput(ctx context.Context, _ terminalinput.Origin, name, text string) error {
	f.fmu.Lock()
	if f.fails[name] > 0 {
		f.fails[name]--
		f.fmu.Unlock()
		return errors.New("can't find pane")
	}
	f.fmu.Unlock()
	return f.mockRuntime.SendInput(ctx, terminalinput.Unclassified, name, text)
}

// countingDiscovery returns *agents (read at each call) and counts the calls.
type countingDiscovery struct {
	mu     sync.Mutex
	calls  int
	agents []AgentInfo
}

func (c *countingDiscovery) fn(context.Context) ([]AgentInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return append([]AgentInfo(nil), c.agents...), nil
}

func (c *countingDiscovery) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *countingDiscovery) set(agents ...AgentInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agents = agents
}

var devAgent = AgentInfo{AgentName: "dev", AgentType: "claude", SessionID: "aaaa-0001", DisplayName: "Dev"}

// #4299: the passes posts ask for share one discovery (a tmux list-panes) for up to 10 s; one
// after that discovers again, and the timer's pass always does.
func TestBoardNotifier_EventPassesReuseARecentDiscovery(t *testing.T) {
	n, _ := twoBoards(t)
	ctx := context.Background()
	clock := time.Unix(1_800_000_000, 0)
	n.now = func() time.Time { return clock }
	d := &countingDiscovery{}
	d.set(devAgent, AgentInfo{AgentName: "ops", AgentType: "claude", SessionID: "bbbb-0001", DisplayName: "Ops"})
	n.SetDiscoverFn(d.fn)
	// The posters' sessions are subscribed but not running: the first fresh discovery notes them.
	for i := 0; i < 5; i++ {
		n.NotifyBoard("a")
		require.NoError(t, n.eventPass(ctx))
		clock = clock.Add(300 * time.Millisecond)
	}
	assert.Equal(t, 1, d.count(), "five posts in 1.5 s: one discovery")

	clock = clock.Add(discoveryMaxAge)
	n.NotifyBoard("a")
	require.NoError(t, n.eventPass(ctx))
	assert.Equal(t, 2, d.count(), "a discovery older than 10 s is not reused")

	require.NoError(t, n.RunOnce(ctx))
	assert.Equal(t, 3, d.count(), "the timer's pass discovers afresh")
}

// A nudge from a reused discovery that fails is retried once on the same session if a fresh
// discovery still finds it, and dropped (no send to anything else) if it does not.
func TestBoardNotifier_AFailedNudgeFromAReusedDiscoveryRediscovers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stillThere bool
		wantSent   []string
	}{
		{"pane moved, session still running", true, []string{"claude-aaaa-0001"}},
		{"session gone", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := twoBoards(t)
			rt := &failingRuntime{fails: map[string]int{"claude-aaaa-0001": 1}}
			n.runtime = rt
			ctx := context.Background()
			clock := time.Unix(1_800_000_000, 0)
			n.now = func() time.Time { return clock }
			d := &countingDiscovery{}
			d.set(devAgent)
			n.SetDiscoverFn(d.fn)
			_, _, err := n.discover(ctx, false) // a discovery a moment ago
			require.NoError(t, err)
			n.noteAbsent([]AgentInfo{devAgent}, map[string]bool{"claude-aaaa-0002": true})
			if !tc.stillThere {
				d.set()
			}

			n.NotifyBoard("a")
			require.NoError(t, n.eventPass(ctx))
			assert.Equal(t, 2, d.count(), "one rediscovery after the failed send")
			assert.Equal(t, tc.wantSent, sentTo(&rt.mockRuntime))
		})
	}
}

// A subscriber of the posted board that the reused discovery lacks (an agent restarted or
// launched since: a session name it never saw) makes the pass discover again, so its nudge
// doesn't wait for the timer. Subscribers a fresh discovery already missed (ended sessions, here
// the posters) do not: the first test's five posts share one discovery.
func TestBoardNotifier_ANewSessionRefreshesAReusedDiscovery(t *testing.T) {
	n, rt := twoBoards(t)
	ctx := context.Background()
	clock := time.Unix(1_800_000_000, 0)
	n.now = func() time.Time { return clock }
	d := &countingDiscovery{}
	d.set(devAgent)
	n.SetDiscoverFn(d.fn)
	_, err := n.boardStore.ReadMessages(ctx, "a", "Dev", 100)
	require.NoError(t, err)
	n.NotifyBoard("a")
	require.NoError(t, n.eventPass(ctx))
	require.Equal(t, 1, d.count())
	require.Empty(t, sentTo(rt), "nothing unread yet")

	// Dev is restarted within the 10 s: a new session, subscribed under the same name.
	restarted := AgentInfo{AgentName: "dev", AgentType: "claude", SessionID: "aaaa-0009", DisplayName: "Dev"}
	d.set(restarted)
	_, err = n.boardStore.Subscribe(ctx, "a", "Dev", "Dev", "claude-aaaa-0009", nil, "")
	require.NoError(t, err)
	_, err = n.boardStore.PostMessage(ctx, "a", "PosterA", "@Dev again", nil)
	require.NoError(t, err)
	clock = clock.Add(500 * time.Millisecond)
	n.NotifyBoard("a")
	require.NoError(t, n.eventPass(ctx))
	assert.Equal(t, 2, d.count(), "the new session was not in the reused discovery: discovered again")
	assert.Equal(t, []string{"claude-aaaa-0009"}, sentTo(rt))
}
