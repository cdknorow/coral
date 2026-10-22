package background

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// goalCadenceTick is the generator's production poll interval (startup.go).
const goalCadenceTick = 30 * time.Second

// goalWorkload is what agent a writes to its transcript at tick n of the simulated hour.
type goalWorkload func(a, n int) []string

func cadenceUserLine(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"timestamp":"2026-10-17T03:00:00Z"}`, text)
}

func cadenceReplyLine(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]},"timestamp":"2026-10-17T03:00:00Z"}`, text)
}

var goalWorkloads = map[string]goalWorkload{
	// A working team: each agent gets a new request every 20 minutes (staggered), works on it
	// for 8 minutes, writing every 30 s, then waits idle for the next one.
	"busy": func(a, n int) []string {
		p := n + 2*a
		switch phase := p % 40; {
		case phase == 0:
			return []string{cadenceUserLine(fmt.Sprintf("Task %d for agent %d", p/40, a)), cadenceReplyLine("Starting.")}
		case phase < 16:
			return []string{cadenceReplyLine(fmt.Sprintf("Step %d.", phase))}
		}
		return nil
	},
	// The #4098 soak's mock agents: one request, then a heartbeat line every 30 s forever.
	"heartbeat": func(a, n int) []string {
		if n == 0 {
			return []string{cadenceUserLine(fmt.Sprintf("Post to the board, agent %d", a)), cadenceReplyLine("Online.")}
		}
		return []string{cadenceReplyLine(fmt.Sprintf("Heartbeat %d.", n))}
	},
	// Agents that answered once and have waited for input since.
	"idle": func(a, n int) []string {
		if n == 0 {
			return []string{cadenceUserLine(fmt.Sprintf("Review the patch, agent %d", a)), cadenceReplyLine("Done; waiting.")}
		}
		return nil
	},
}

// runGoalCadence runs agents for one simulated hour of polls on a fake clock and returns how
// many times the goal CLI ran (#4204). fail makes every CLI call fail, as the soak's mock did.
func runGoalCadence(t *testing.T, agents int, work goalWorkload, fail bool) int {
	t.Helper()
	db := setupTestDB(t)
	ss, ts := store.NewSessionStore(db), store.NewTaskStore(db)
	ctx := context.Background()
	dir := t.TempDir()
	clock := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	paths := map[string]string{}
	for a := 0; a < agents; a++ {
		sid := fmt.Sprintf("00000000-0000-0000-0000-%012d", 4204000+a)
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: fmt.Sprintf("agent-%d", a), WorkingDir: "/repo"}))
		paths[sid] = filepath.Join(dir, sid+".jsonl")
	}
	gen := NewGoalGenerator(ss, ts, time.Hour)
	gen.now = func() time.Time { return clock }
	gen.resolvePath = func(ls *store.LiveSession) string { return paths[ls.SessionID] }
	cli := signedInClaude(t)
	gen.resolveCLI = func(map[string]string) string { return cli }
	var calls atomic.Int32
	gen.runCLI = func(_ context.Context, _, prompt string) (goalCLIResult, error) {
		calls.Add(1)
		if fail {
			return goalCLIResult{}, errors.New("timeout")
		}
		i := strings.Index(prompt, "LATEST USER REQUEST:\n")
		req := prompt[i+len("LATEST USER REQUEST:\n"):]
		return goalCLIResult{Text: strings.SplitN(req, "\n", 2)[0]}, nil
	}
	for n := 0; n < int(time.Hour/goalCadenceTick); n++ {
		for a := 0; a < agents; a++ {
			lines := work(a, n)
			if len(lines) == 0 {
				continue
			}
			sid := fmt.Sprintf("00000000-0000-0000-0000-%012d", 4204000+a)
			fh, err := os.OpenFile(paths[sid], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			require.NoError(t, err)
			_, err = fh.WriteString(strings.Join(lines, "\n") + "\n")
			require.NoError(t, err)
			require.NoError(t, fh.Close())
			require.NoError(t, os.Chtimes(paths[sid], clock, clock))
		}
		gen.pollOnce(ctx)
		gen.wg.Wait()
		clock = clock.Add(goalCadenceTick)
	}
	return int(calls.Load())
}

// TestGoalCadence is the before/after harness for #4204: CLI runs in an hour for 20 agents.
// Before #4204: busy 176, heartbeat 240, heartbeat with a failing CLI 120, idle 20.
func TestGoalCadence(t *testing.T) {
	const agents = 20
	for _, c := range []struct {
		name     string
		workload string
		fail     bool
		max      int // the most runs an hour #4204 allows
	}{
		// The first goal, then one per new request at most every 30 minutes: 2 an hour each.
		{"busy", "busy", false, 2 * agents},
		// The request never changes after the first goal: nothing to rename.
		{"heartbeat", "heartbeat", false, agents},
		// The first try, then 30 and 60 minutes of backoff: 2 tries in the hour.
		{"heartbeat, the CLI fails", "heartbeat", true, 2 * agents},
		{"idle", "idle", false, agents},
	} {
		t.Run(c.name, func(t *testing.T) {
			runs := runGoalCadence(t, agents, goalWorkloads[c.workload], c.fail)
			t.Logf("%s: %d CLI runs an hour for %d agents", c.name, runs, agents)
			assert.LessOrEqual(t, runs, c.max)
			assert.GreaterOrEqual(t, runs, agents, "every agent still gets its first goal")
		})
	}
}
