package ptymanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// #3613 B: Coral's automated sends and launches to one team are spaced.

func fakeSpacer(gap time.Duration) (*teamSpacer, *time.Time) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s := newTeamSpacer()
	s.now = func() time.Time { return now }
	s.gap = func() time.Duration { return gap }
	return s, &now
}

func TestSpacerFirstSendIsNotDelayed(t *testing.T) {
	s, _ := fakeSpacer(2500 * time.Millisecond)
	if d := s.reserve("team"); d != 0 {
		t.Fatalf("the first send waits %v, want 0", d)
	}
	if d := s.reserve("other"); d != 0 {
		t.Fatalf("another team's first send waits %v, want 0", d)
	}
}

func TestSpacerSpacesConsecutiveSendsToOneTeam(t *testing.T) {
	s, now := fakeSpacer(2500 * time.Millisecond)
	var waits []time.Duration
	for i := 0; i < 4; i++ {
		waits = append(waits, s.reserve("team"))
	}
	want := []time.Duration{0, 2500 * time.Millisecond, 5 * time.Second, 7500 * time.Millisecond}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	// A team of one: its sends come further apart than the gap, so none waits.
	*now = now.Add(time.Minute)
	for i := 0; i < 3; i++ {
		if d := s.reserve("solo"); d != 0 {
			t.Fatalf("a send %d gaps after the last waits %v", i, d)
		}
		*now = now.Add(3 * time.Second)
	}
	// Quiet teams are forgotten.
	*now = now.Add(2 * time.Minute)
	s.reserve("x")
	if _, ok := s.next["team"]; ok {
		t.Fatal("a team quiet over a minute is still kept")
	}
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
	saved := spacer
	defer func() { spacer = saved }()
	s, _ := fakeSpacer(time.Hour)
	slept := 0
	s.sleep = func(ctx context.Context, d time.Duration) bool { slept++; return false }
	spacer = s
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if !SpaceTeam(ctx, "") {
			t.Fatal("an agent on no board is refused")
		}
	}
	if !SpaceTeam(ctx, "team") || slept != 0 {
		t.Fatalf("the first send slept %d times", slept)
	}
	if SpaceTeam(ctx, "team") || slept != 1 {
		t.Fatal("the second send must wait (and report the cancelled wait)")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if SpaceTeam(cancelled, "fresh") {
		t.Fatal("a cancelled send goes")
	}
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

// Security: the spacer never delays the Operator's input, keys or kill. It is called only where
// Coral itself sends or launches: the gate's send (automated input), team launch, reset,
// resurrect, Wake and Wake All, and auto-resume.
func TestSpaceTeamIsCalledOnlyForCoralsOwnSends(t *testing.T) {
	allowed := map[string]bool{
		"internal/ptymanager/automated_input.go": true,
		"internal/ptymanager/team_spacer.go":     true,
		"internal/server/routes/sessions.go":     true,
		"internal/server/routes/agent_exit.go":   true,
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
		if strings.Contains(string(b), "SpaceTeam(") && !allowed[filepath.ToSlash(rel)] {
			t.Errorf("%s calls SpaceTeam: only Coral's own sends and launches may be spaced", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// In the gate, only sendChecked (automated sends) spaces: not the Operator's paths.
	b, err := os.ReadFile("automated_input.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "SpaceTeam("); n != 1 {
		t.Fatalf("automated_input.go calls SpaceTeam %d times, want 1 (sendChecked)", n)
	}
}
