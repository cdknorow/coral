package ptymanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3613 B: Coral's automated sends and agent starts to one team are spaced.

func fakeSpacer(gap time.Duration) (*teamSpacer, *time.Time) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s := newTeamSpacer()
	s.now = func() time.Time { return now }
	s.gap = func() time.Duration { return gap }
	return s, &now
}

func useSpacer(t *testing.T, s *teamSpacer) {
	saved := spacer
	spacer = s
	t.Cleanup(func() { spacer = saved })
}

func TestSpacerFirstSendIsNotDelayed(t *testing.T) {
	s, _ := fakeSpacer(2500 * time.Millisecond)
	if d, ok := s.reserve("team", MaxTeamWait); d != 0 || !ok {
		t.Fatalf("the first send waits %v (ok %v), want 0", d, ok)
	}
	if d, _ := s.reserve("other", MaxTeamWait); d != 0 {
		t.Fatalf("another team's first send waits %v, want 0", d)
	}
}

func TestSpacerSpacesConsecutiveSendsToOneTeam(t *testing.T) {
	s, now := fakeSpacer(2500 * time.Millisecond)
	var waits []time.Duration
	for i := 0; i < 4; i++ {
		d, _ := s.reserve("team", MaxTeamWait)
		waits = append(waits, d)
	}
	want := []time.Duration{0, 2500 * time.Millisecond, 5 * time.Second, 7500 * time.Millisecond}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	// A team of one: its sends come further apart than the gap, so none waits.
	*now = now.Add(time.Minute)
	for i := 0; i < 3; i++ {
		if d, _ := s.reserve("solo", MaxTeamWait); d != 0 {
			t.Fatalf("a send %d gaps after the last waits %v", i, d)
		}
		*now = now.Add(3 * time.Second)
	}
	// Quiet teams are forgotten.
	*now = now.Add(2 * time.Minute)
	s.reserve("x", MaxTeamWait)
	if _, ok := s.next["team"]; ok {
		t.Fatal("a team quiet over a minute is still kept")
	}
}

// Security's SHOULD 2: 50 sends at once. Only those whose slot is within MaxTeamWait take one;
// the rest are refused at once and push nothing later.
func TestSpacerWaitIsCapped(t *testing.T) {
	s, _ := fakeSpacer(2500 * time.Millisecond)
	taken, refused := 0, 0
	for i := 0; i < 50; i++ {
		d, ok := s.reserve("team", MaxTeamWait)
		if ok {
			taken++
			assert.LessOrEqual(t, d, MaxTeamWait)
		} else {
			refused++
			assert.Greater(t, d, MaxTeamWait)
		}
	}
	assert.Equal(t, 13, taken, "slots at 0, 2.5 ... 30 s")
	assert.Equal(t, 37, refused)
	assert.Equal(t, 32500*time.Millisecond, s.next["team"].Sub(s.now()), "a refusal takes no slot")

	useSpacer(t, s)
	slept := 0
	s.sleep = func(context.Context, time.Duration) bool { slept++; return true }
	assert.ErrorIs(t, SpaceTeam(context.Background(), "team"), ErrTeamBusy)
	assert.Zero(t, slept, "refused without waiting")
	// An agent's start is never refused: it takes the next slot, up to MaxStartWait away.
	assert.Equal(t, 32500*time.Millisecond, ReserveTeamStart("team"))
	assert.Zero(t, ReserveTeamStart(""), "no team: at once")
}

// Security's NIT on #3647 v2: a flood of launches can't push a start minutes out. Starts take
// slots up to MaxStartWait away; beyond it a start goes now and takes no slot.
func TestAgentStartsWaitAtMostMaxStartWait(t *testing.T) {
	s, _ := fakeSpacer(2500 * time.Millisecond)
	useSpacer(t, s)
	var waits []time.Duration
	for i := 0; i < 40; i++ {
		waits = append(waits, ReserveTeamStart("team"))
	}
	for i, d := range waits {
		assert.LessOrEqual(t, d, MaxStartWait, "start %d", i)
	}
	assert.Equal(t, 60*time.Second, waits[24], "slots at 0, 2.5 ... 60 s")
	for i, d := range waits[25:] {
		assert.Zero(t, d, "start %d past the cap goes now", i+25)
	}
	assert.Equal(t, 62500*time.Millisecond, s.next["team"].Sub(s.now()), "a start past the cap takes no slot")
}

func TestSpacerGapIsWithinBounds(t *testing.T) {
	s := newTeamSpacer()
	for i := 0; i < 500; i++ {
		if g := s.gap(); g < TeamGap || g >= TeamGap+TeamJitter {
			t.Fatalf("gap %v outside [%v, %v)", g, TeamGap, TeamGap+TeamJitter)
		}
	}
}

func TestSpaceTeamNoTeamAndCancel(t *testing.T) {
	s, _ := fakeSpacer(time.Second)
	useSpacer(t, s)
	slept := 0
	s.sleep = func(ctx context.Context, d time.Duration) bool { slept++; return false }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		require.NoError(t, SpaceTeam(ctx, ""), "an agent on no board")
	}
	require.NoError(t, SpaceTeam(ctx, "team"))
	assert.Zero(t, slept, "the first send doesn't wait")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.Error(t, SpaceTeam(cancelled, "team"), "the second waits, and its wait was cancelled")
	assert.Equal(t, 1, slept)
	assert.Error(t, SpaceTeam(cancelled, "fresh"), "a cancelled send doesn't go")
}

// A send whose team slot is over MaxTeamWait away is held, not dropped and not parked: the
// gate's loop types it once the slot is near.
func TestABusyTeamSendIsHeldThenTyped(t *testing.T) {
	fastGate(t)
	s := newTeamSpacer()
	s.gap = func() time.Duration { return 0 }
	useSpacer(t, s)
	SetTeamResolver(func(string) string { return "busy-team" })
	t.Cleanup(func() { SetTeamResolver(nil) })
	s.mu.Lock()
	s.next["busy-team"] = time.Now().Add(time.Hour)
	s.mu.Unlock()

	a := &fakeAgent{screen: idleInputBox}
	d, err := DeliverAutomated(context.Background(), a.input("claude-gate-busy", "", "hello", false))
	require.NoError(t, err)
	assert.Equal(t, Held, d, "not now")
	time.Sleep(100 * time.Millisecond) // sleep-ok: several gate passes, still busy
	assert.Empty(t, a.sent(), "nothing typed while the team is busy")

	s.mu.Lock()
	delete(s.next, "busy-team")
	s.mu.Unlock()
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"hello"}, a.sent(), "typed once, when the slot came")
}

func TestJitterAroundBounds(t *testing.T) {
	base := 30 * time.Minute
	seen := map[time.Duration]bool{}
	for i := 0; i < 2000; i++ {
		key := fmt.Sprintf("claude-%d", i)
		d := JitterAround(base, key)
		if d <= 0 || d < base*8/10 || d > base*12/10 {
			t.Fatalf("%s: %v outside 0.8-1.2 x %v", key, d, base)
		}
		if JitterAround(base, key) != d {
			t.Fatalf("%s: not the same for one key", key)
		}
		seen[d.Round(time.Minute)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct windows: not spread", len(seen))
	}
	if JitterAround(time.Second, "") <= 0 {
		t.Fatal("never 0")
	}
}

// Security: the spacer never delays the Operator's input, keys or kill. Only Coral's own sends
// (the gate's sendChecked) and agent starts (launch and wake, typed later in the background)
// are spaced.
func TestTheSpacerIsUsedOnlyForCoralsOwnSendsAndStarts(t *testing.T) {
	allowed := map[string]bool{
		"internal/ptymanager/automated_input_deliver.go": true,
		"internal/ptymanager/team_spacer.go":             true,
		"internal/server/routes/sessions_lifecycle.go":   true,
	}
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		src := string(b)
		if (strings.Contains(src, "SpaceTeam(") || strings.Contains(src, "ReserveTeamStart(")) && !allowed[filepath.ToSlash(rel)] {
			t.Errorf("%s uses the spacer: only Coral's own sends and agent starts may be spaced", rel)
		}
		return nil
	})
	require.NoError(t, err)
	b, err := os.ReadFile("automated_input_deliver.go")
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(b), "SpaceTeam("), "only sendChecked, in the gate")
	b, err = os.ReadFile(filepath.Join(root, "internal/server/routes/sessions_lifecycle.go"))
	require.NoError(t, err)
	assert.Zero(t, strings.Count(string(b), "SpaceTeam("), "no request waits for the spacing")
	assert.Equal(t, 1, strings.Count(string(b), "ReserveTeamStart("), "only startAgentCLI")
}
