package machinehealth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// statLine is a /proc/<pid>/stat line with the fields machine health reads: ppid, utime and stime
// (clock ticks) and rss (pages); the rest are zeros, as many as a real kernel writes.
func statLine(pid int, comm string, ppid int, utime, stime uint64, rssPages int64) string {
	f := make([]string, 50)
	for i := range f {
		f[i] = "0"
	}
	f[0] = "S"
	f[1] = strconv.Itoa(ppid)
	f[11] = strconv.FormatUint(utime, 10)
	f[12] = strconv.FormatUint(stime, 10)
	f[21] = strconv.FormatInt(rssPages, 10)
	return fmt.Sprintf("%d (%s) %s\n", pid, comm, strings.Join(f, " "))
}

type fakeProc struct {
	pid, ppid    int
	comm         string
	utime, stime uint64
	rssPages     int64
	tasks        map[int]string // tid -> its children file
}

// writeFakeProc writes a fake /proc tree under root and points procRoot at it for the test.
func writeFakeProc(t *testing.T, root string, procs []fakeProc) {
	t.Helper()
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stat"), []byte(statLine(p.pid, p.comm, p.ppid, p.utime, p.stime, p.rssPages)), 0o644))
		for tid, children := range p.tasks {
			td := filepath.Join(dir, "task", strconv.Itoa(tid))
			require.NoError(t, os.MkdirAll(td, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(td, "children"), []byte(children), 0o644))
		}
	}
	// The children-file probe looks at this process's own entry.
	self := filepath.Join(root, "self", "task", strconv.Itoa(os.Getpid()))
	require.NoError(t, os.MkdirAll(self, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(self, "children"), nil, 0o644))
	prev := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = prev })
}

// #4455: the Linux walk and the per-process reads against a fake /proc, golden values. A shell (100)
// runs an agent (200, two threads) whose second thread started a helper (300): the helper is listed only
// in thread 201's children file. 999 is not under the root and is not counted.
func TestLinuxProcessTreeGolden(t *testing.T) {
	root := t.TempDir()
	procs := []fakeProc{
		{pid: 100, ppid: 1, comm: "bash", utime: 150, stime: 50, rssPages: 1000, tasks: map[int]string{100: "200 "}},
		{pid: 200, ppid: 100, comm: "mock agent (claude) x", utime: 900, stime: 100, rssPages: 5000, tasks: map[int]string{200: "", 201: "300 "}},
		{pid: 300, ppid: 200, comm: "helper", utime: 10, stime: 10, rssPages: 250, tasks: map[int]string{300: ""}},
		{pid: 999, ppid: 1, comm: "unrelated", utime: 77777, stime: 1, rssPages: 99999, tasks: map[int]string{999: ""}},
	}
	writeFakeProc(t, root, procs)
	page := uint64(os.Getpagesize())

	assert.Equal(t, []int32{100, 200, 300}, descendants(context.Background(), []int{100}))
	for _, c := range []struct {
		pid      int32
		cpu      float64
		rssPages uint64
	}{{100, 2.0, 1000}, {200, 10.0, 5000}, {300, 0.2, 250}} {
		cpu, rss, ok := procUsage(context.Background(), c.pid)
		require.True(t, ok, "%d", c.pid)
		assert.InDelta(t, c.cpu, cpu, 1e-9, "%d cpu seconds", c.pid)
		assert.Equal(t, c.rssPages*page, rss, "%d resident bytes", c.pid)
	}
	_, _, ok := procUsage(context.Background(), 4242)
	assert.False(t, ok, "a process that is gone")

	// A whole sample: the agents' memory is the tree's, and their CPU over the interval is the tree's
	// added ticks (here 100 + 50 + 50 ticks = 2 s over 10 s = 20% of one core).
	now := time.Unix(1_800_000_000, 0)
	s := New(Source{AgentPIDs: func() []int { return []int{100} }})
	s.now = func() time.Time { return now }
	h := s.Sample(context.Background())
	assert.Equal(t, (1000+5000+250)*page, h.Agents.RSSBytes)
	assert.Zero(t, h.Agents.CPUPercent, "no interval yet")
	procs[0].utime += 100
	procs[1].stime += 50
	procs[2].utime += 50
	writeFakeProc(t, root, procs)
	now = now.Add(10 * time.Second)
	h = s.Sample(context.Background())
	assert.InDelta(t, 20.0, h.Agents.CPUPercent, 1e-9)
	assert.Equal(t, 1, h.Agents.Count)
}

// On the real /proc, the one-read values agree with gopsutil's separate reads for this process.
func TestProcUsageAgreesWithGopsutil(t *testing.T) {
	ctx := context.Background()
	pid := int32(os.Getpid())
	gCPU, gRSS, ok := procUsageGopsutil(ctx, pid)
	require.True(t, ok)
	cpu, rss, ok := procUsage(ctx, pid)
	require.True(t, ok)
	assert.InDelta(t, gCPU, cpu, 0.05, "CPU seconds")
	assert.InEpsilon(t, float64(gRSS), float64(rss), 0.05, "resident bytes")
}
