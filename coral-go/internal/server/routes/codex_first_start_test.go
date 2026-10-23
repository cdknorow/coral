package routes

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freshCodexHome points Codex at an empty home, turns the first-start gap on, and forgets earlier firsts.
func freshCodexHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	prev := codexFirstStartGap
	codexFirstStartGap = 10 * time.Second
	codexFirstStarts.Lock()
	codexFirstStarts.at = map[string]time.Time{}
	codexFirstStarts.Unlock()
	t.Cleanup(func() { codexFirstStartGap = prev })
	return home
}

// #6364: two Codex agents started at once on a new Codex home raced Codex's memories-database migration and one exited. The first start on a home with no sessions folder goes at once; the others wait
// until the gap after it has passed, all together.
func TestTheFirstCodexStartOnANewHomeIsNotRacedByTheOthers(t *testing.T) {
	freshCodexHome(t)
	t0 := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0), "the first goes at once")
	assert.Equal(t, 10*time.Second, reserveCodexFirstStart("codex", t0), "a second at the same moment waits out the gap")
	assert.Equal(t, 7*time.Second, reserveCodexFirstStart("codex", t0.Add(3*time.Second)), "a third waits for what is left of it")
	assert.Equal(t, 7*time.Second, reserveCodexFirstStart("codex", t0.Add(3*time.Second)), "all of them for the same moment: they are not queued one after another")
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0.Add(10*time.Second)), "once the gap has passed nothing waits")
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0.Add(time.Hour)))
}

func TestOnlyANewCodexHomeAndOnlyCodexAreGated(t *testing.T) {
	home := freshCodexHome(t)
	t0 := time.Now()
	require.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0))
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("claude", t0), "another agent type is never delayed")
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("gemini", t0))
	assert.Greater(t, reserveCodexFirstStart("codex", t0), time.Duration(0))

	require.NoError(t, os.MkdirAll(filepath.Join(home, "sessions"), 0o700))
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0), "a home that has sessions has been used: its migration is done")

	codexFirstStartGap = 0
	other := t.TempDir()
	t.Setenv("CODEX_HOME", other)
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0))
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0), "no gap, no delay")
}

func TestEachCodexHomeHasItsOwnFirstStart(t *testing.T) {
	freshCodexHome(t)
	t0 := time.Now()
	require.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0))
	t.Setenv("CODEX_HOME", t.TempDir())
	assert.Equal(t, time.Duration(0), reserveCodexFirstStart("codex", t0), "another home is another first")
}

// Many at once: exactly one goes at once.
func TestOnlyOneOfManySimultaneousCodexStartsGoesAtOnce(t *testing.T) {
	freshCodexHome(t)
	now := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	zero := 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reserveCodexFirstStart("codex", now) == 0 {
				mu.Lock()
				zero++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, zero)
}

// Through the real start path: the first Codex start is typed now, a second one is deferred by the gap (not typed until its slot), and a Claude start never is.
func TestTheStartPathDefersTheSecondCodexStartOnANewHome(t *testing.T) {
	freshCodexHome(t)
	var mu sync.Mutex
	var deferred []time.Duration
	prev := afterTeamSlot
	afterTeamSlot = func(d time.Duration, f func()) { mu.Lock(); deferred = append(deferred, d); mu.Unlock(); f() }
	t.Cleanup(func() { afterTeamSlot = prev })
	_, h, term, _ := setupSessionsTestServer(t)

	require.NoError(t, h.startAgentCLI(context.Background(), "", "codex", "codex-a", "codex"))
	assert.Empty(t, deferred, "the first Codex start is typed at once")
	require.NoError(t, h.startAgentCLI(context.Background(), "", "codex", "codex-b", "codex"))
	require.Len(t, deferred, 1, "the second is deferred")
	assert.InDelta(t, float64(10*time.Second), float64(deferred[0]), float64(time.Second), "by about the gap")
	require.NoError(t, h.startAgentCLI(context.Background(), "", "claude", "claude-a", "claude"))
	assert.Len(t, deferred, 1, "a Claude start is never deferred by it")
	assert.Len(t, term.sentCommands(), 3, "every start is typed in the end")
}

// The gap is the margin over a migration that takes a fraction of a second, not a guess at a slow one: a real Codex had its memories database 0.3 s after it was started (#6374). Raising it makes the first
// multi-Codex launch of a new person wait that much longer, so it is pinned.
func TestTheFirstCodexStartGapIsAShortMargin(t *testing.T) {
	assert.Equal(t, 3*time.Second, codexFirstStartGapDefault)
}
