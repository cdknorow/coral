package machinehealth

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gb = 1 << 30

func healthy() Health {
	return Health{
		Host: Host{Load1PerCore: 0.3, CPUCount: 8, MemTotal: 16 * gb, MemAvailable: 8 * gb},
		Disk: Disk{FreeBytes: 200 * gb, TotalBytes: 500 * gb},
	}
}

// The operator's thresholds, and which reason wins when several apply.
func TestClassify(t *testing.T) {
	now := time.Now()
	fast := Responsiveness{Last: 20 * time.Millisecond, P95: 40 * time.Millisecond}
	with := func(f func(*Health)) Health { h := healthy(); f(&h); return h }
	for _, tc := range []struct {
		name           string
		h              Health
		r              Responsiveness
		haveR          bool
		status, reason string
	}{
		{"all fine", healthy(), fast, true, "ok", ""},
		{"no watchdog, all fine", healthy(), Responsiveness{}, false, "ok", ""},
		{"load per core over 1", with(func(h *Health) { h.Host.Load1PerCore = 1.2 }), fast, true, "busy", "cpu"},
		{"load per core exactly 1", with(func(h *Health) { h.Host.Load1PerCore = 1.0 }), fast, true, "ok", ""},
		{"probe p95 over 500 ms", healthy(), Responsiveness{Last: 100 * time.Millisecond, P95: 600 * time.Millisecond}, true, "busy", "cpu"},
		{"memory under 10%", with(func(h *Health) { h.Host.MemAvailable = gb }), fast, true, "busy", "memory"},
		{"disk under 5%", with(func(h *Health) { h.Disk = Disk{FreeBytes: 20 * gb, TotalBytes: 500 * gb} }), fast, true, "busy", "disk"},
		{"disk under 2 GB", with(func(h *Health) { h.Disk = Disk{FreeBytes: gb, TotalBytes: 10 * gb} }), fast, true, "busy", "disk"},
		{"last probe over 2 s", healthy(), Responsiveness{Last: 2500 * time.Millisecond}, true, "stalled", "stall"},
		{"a stall 30 s ago", healthy(), Responsiveness{Last: 20 * time.Millisecond, LastSlow: now.Add(-30 * time.Second)}, true, "stalled", "stall"},
		{"a stall 90 s ago is over", healthy(), Responsiveness{Last: 20 * time.Millisecond, LastSlow: now.Add(-90 * time.Second)}, true, "ok", ""},
		{"stall beats disk, memory and cpu", with(func(h *Health) { h.Disk.FreeBytes = gb; h.Host.MemAvailable = 0; h.Host.Load1PerCore = 9 }), Responsiveness{Last: 3 * time.Second}, true, "stalled", "stall"},
		{"disk beats memory and cpu", with(func(h *Health) { h.Disk.FreeBytes = gb; h.Host.MemAvailable = 0; h.Host.Load1PerCore = 9 }), fast, true, "busy", "disk"},
		{"memory beats cpu", with(func(h *Health) { h.Host.MemAvailable = 0; h.Host.Load1PerCore = 9 }), fast, true, "busy", "memory"},
		{"unknown memory and disk do not count", with(func(h *Health) { h.Host.MemTotal = 0; h.Disk = Disk{} }), fast, true, "ok", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, reason := classify(tc.h, tc.r, tc.haveR, now)
			assert.Equal(t, tc.status, status)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

// A real sample: sane values, and an agent's child process is counted.
func TestSampleReadsTheMachine(t *testing.T) {
	agent := exec.Command("sleep", "30")
	require.NoError(t, agent.Start())
	t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })

	s := New(Source{
		DataDir:    t.TempDir(),
		AgentPIDs:  func() []int { return []int{agent.Process.Pid} },
		AgentCount: func() int { return 1 },
		Responsive: func() (Responsiveness, bool) {
			return Responsiveness{Last: 12 * time.Millisecond, P95: 30 * time.Millisecond}, true
		},
	})
	_, ok := s.Current()
	assert.False(t, ok, "nothing before the first sample")
	ctx := context.Background()
	s.Sample(ctx)
	busy := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(busy) { // some CPU for Coral's own share
	}
	h := s.Sample(ctx)
	cur, ok := s.Current()
	require.True(t, ok)
	assert.Equal(t, h, cur)

	assert.Contains(t, []string{"ok", "busy", "stalled"}, h.Status)
	if h.Status == "ok" {
		assert.Empty(t, h.Reason)
	} else {
		assert.Contains(t, []string{"cpu", "memory", "disk", "stall"}, h.Reason)
	}
	assert.EqualValues(t, 12, h.ResponsivenessMS)
	assert.Greater(t, h.Coral.RSSBytes, uint64(0), "Coral's resident memory")
	assert.Greater(t, h.Coral.CPUPercent, 0.0, "Coral used CPU over the interval")
	assert.Equal(t, 1, h.Agents.Count)
	assert.Greater(t, h.Agents.RSSBytes, uint64(0), "the agent's process is counted")
	assert.GreaterOrEqual(t, h.Agents.CPUPercent, 0.0)
	assert.Greater(t, h.Host.CPUCount, 0)
	assert.Greater(t, h.Host.MemTotal, uint64(0))
	assert.LessOrEqual(t, h.Host.MemAvailable, h.Host.MemTotal)
	assert.GreaterOrEqual(t, h.Host.Load1PerCore, 0.0)
	assert.Greater(t, h.Disk.TotalBytes, uint64(0))
	assert.LessOrEqual(t, h.Disk.FreeBytes, h.Disk.TotalBytes)
}

// Numbers only (#3444): the JSON carries no string but the two enums, and exactly the agreed keys.
func TestHealthJSONIsNumbersOnly(t *testing.T) {
	s := New(Source{DataDir: t.TempDir(), AgentPIDs: func() []int { return nil }, AgentCount: func() int { return 0 }})
	h := s.Sample(context.Background())
	h.Status, h.Reason = "busy", "disk" // both enums present
	b, err := json.Marshal(h)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	var keys []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				walk(prefix+k+".", vv)
			}
		case string:
			keys = append(keys, prefix)
			switch prefix {
			case "status.":
				assert.Contains(t, []string{"ok", "busy", "stalled"}, x)
			case "reason.":
				assert.Contains(t, []string{"cpu", "memory", "disk", "stall"}, x)
			default:
				t.Errorf("a string at %s: %q", prefix, x)
			}
		case float64:
			keys = append(keys, prefix)
		default:
			t.Errorf("unexpected %T at %s", v, prefix)
		}
	}
	walk("", m)
	sort.Strings(keys)
	assert.Equal(t, []string{
		"agents.count.", "agents.cpu_percent.", "agents.rss_bytes.",
		"coral.cpu_percent.", "coral.rss_bytes.",
		"disk.free_bytes.", "disk.total_bytes.",
		"host.cpu_count.", "host.load1_per_core.", "host.mem_available.", "host.mem_total.",
		"reason.", "responsiveness_ms.", "status.",
	}, keys, "the agreed schema, nothing more")
}

// One sample walks the process table once; at one sample per Interval (15 s) it must cost well
// under 1% of a core: a sample under 150 ms of CPU.
func BenchmarkSample(b *testing.B) {
	s := New(Source{DataDir: b.TempDir(), AgentPIDs: func() []int { return []int{1} }})
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		s.Sample(ctx)
	}
}

// #3846: the walk finds a grandchild (a shell's background job), from the cheap process table.
func TestDescendantsFindsGrandchildren(t *testing.T) {
	sh := exec.Command("sh", "-c", "sleep 30 & wait")
	require.NoError(t, sh.Start())
	// The sleep too, not only the shell (#3997): the shell's descendants found now, by pid.
	t.Cleanup(func() {
		for _, pid := range descendants(context.Background(), []int{sh.Process.Pid}) {
			if p, err := os.FindProcess(int(pid)); err == nil && int(pid) != sh.Process.Pid {
				_ = p.Kill()
			}
		}
		sh.Process.Kill()
		sh.Wait()
	})
	require.Eventually(t, func() bool {
		return len(descendants(context.Background(), []int{sh.Process.Pid})) >= 2
	}, 5*time.Second, 20*time.Millisecond, "the shell and its sleep")
	assert.Equal(t, int32(sh.Process.Pid), descendants(context.Background(), []int{sh.Process.Pid})[0])
}

// #3846: with nobody reading, sampling pauses; a read of an old sample asks for a new one.
func TestSamplingPausesWhileNobodyReads(t *testing.T) {
	var mu sync.Mutex
	at := time.Unix(1_700_000_000, 0)
	s := New(Source{DataDir: t.TempDir()})
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return at }
	advance := func(d time.Duration) { mu.Lock(); at = at.Add(d); mu.Unlock() }
	s.Sample(context.Background())
	_, _ = s.Current()
	assert.True(t, s.watched(), "just read")
	advance(IdleAfter + time.Second)
	assert.False(t, s.watched(), "nobody read for IdleAfter")
	select {
	case <-s.kick:
		t.Fatal("no request before a read")
	default:
	}
	_, ok := s.Current()
	require.True(t, ok, "the last sample is still given")
	select {
	case <-s.kick:
	default:
		t.Fatal("a read of an old sample asks for a new one")
	}
	assert.True(t, s.watched())
}
