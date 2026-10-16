// Package machinehealth samples how this machine is doing, for Coral Hub's machine view and the
// dashboard's status dot (#3444, #3446): Coral's own process, the agents' processes, the host's
// load and memory, the data folder's disk, and how quickly the server answers its own health
// probes (the stall watchdog). Every Interval it takes one sample; Current returns the latest.
//
// The sample is numbers only, apart from two enums (status and reason): no process names,
// command lines, paths or user names ever leave the machine through it.
package machinehealth

import (
	"context"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/cdknorow/coral/internal/safego"
)

// Health is one sample, in the shape the feeds carry it (machine_health).
type Health struct {
	Status           string `json:"status"`           // ok | busy | stalled
	Reason           string `json:"reason,omitempty"` // cpu | memory | disk | stall, only when not ok
	ResponsivenessMS int64  `json:"responsiveness_ms"`
	Coral            Proc   `json:"coral"`
	Agents           Agents `json:"agents"`
	Host             Host   `json:"host"`
	Disk             Disk   `json:"disk"`
}

// Proc is a process (or a set of them): CPU as % of one core over the interval, resident bytes.
type Proc struct {
	CPUPercent float64 `json:"cpu_percent"`
	RSSBytes   uint64  `json:"rss_bytes"`
}

// Agents sums the agents' process trees.
type Agents struct {
	Count      int     `json:"count"`
	CPUPercent float64 `json:"cpu_percent"`
	RSSBytes   uint64  `json:"rss_bytes"`
}

// Host is the machine as a whole.
type Host struct {
	Load1PerCore float64 `json:"load1_per_core"`
	CPUCount     int     `json:"cpu_count"`
	MemTotal     uint64  `json:"mem_total"`
	MemAvailable uint64  `json:"mem_available"`
}

// Disk is the filesystem the data folder is on.
type Disk struct {
	FreeBytes  uint64 `json:"free_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

// Responsiveness is the stall watchdog's view (stallwatch.Stats).
type Responsiveness struct {
	Last     time.Duration
	P95      time.Duration
	LastSlow time.Time
}

// Source is what a sample reads besides the OS.
type Source struct {
	DataDir    string
	AgentPIDs  func() []int                  // the roots of the agents' process trees, one per agent
	AgentCount func() int                    // live agents; nil: one per AgentPIDs root
	Responsive func() (Responsiveness, bool) // false when there is no watchdog
}

// Thresholds (the operator's, #3444).
const (
	busyLoadPerCore  = 1.0
	busyMemFraction  = 0.10
	busyProbeP95     = 500 * time.Millisecond
	busyDiskFraction = 0.05
	busyDiskBytes    = 2 << 30
	stalledLastProbe = 2 * time.Second
	stalledWithin    = 60 * time.Second
)

// Interval is how often a sample is taken while someone reads them.
const Interval = 15 * time.Second

// IdleAfter: with no Current call for this long (no dashboard tab or Coral Hub feed shows the
// health), sampling pauses; the next Current starts it again at once (#3846).
const IdleAfter = 2 * Interval

// Sampler takes a sample every Interval; see Current.
type Sampler struct {
	src Source
	now func() time.Time

	mu        sync.Mutex
	cur       *Health
	sampledAt time.Time
	lastRead  time.Time
	kick      chan struct{} // a reader came back while sampling was paused

	// CPU time seen at the previous sample, to turn cumulative seconds into % over the interval.
	prevAt     time.Time
	prevSelf   float64
	prevAgents map[int32]float64
}

// New returns a sampler; Start runs it on a timer, Sample takes one sample now.
func New(src Source) *Sampler {
	return &Sampler{src: src, now: time.Now, prevAgents: map[int32]float64{}, kick: make(chan struct{}, 1)}
}

// Start samples at once and then every Interval until ctx ends, pausing while nobody reads
// the samples (IdleAfter).
func Start(ctx context.Context, src Source) *Sampler {
	s := New(src)
	go func() {
		defer safego.Recover("machinehealth/machinehealth:121")
		s.Sample(ctx)
		t := time.NewTicker(Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s.watched() {
					s.Sample(ctx)
				}
			case <-s.kick:
				s.Sample(ctx)
			}
		}
	}()
	return s
}

// watched reports whether a sample was read within IdleAfter.
func (s *Sampler) watched() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Sub(s.lastRead) < IdleAfter
}

// Current is the latest sample (false before the first). A read of a sample older than
// Interval (sampling paused while nobody read) asks for a new one at once.
func (s *Sampler) Current() (Health, bool) {
	if s == nil {
		return Health{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.lastRead = now
	if s.cur != nil && now.Sub(s.sampledAt) > Interval {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
	if s.cur == nil {
		return Health{}, false
	}
	return *s.cur, true
}

// Sample takes one sample and makes it Current.
func (s *Sampler) Sample(ctx context.Context) Health {
	now := s.now()
	elapsed := now.Sub(s.prevAt).Seconds()
	first := s.prevAt.IsZero()
	var h Health

	// Coral itself.
	if p, err := process.NewProcessWithContext(ctx, int32(os.Getpid())); err == nil {
		cpu := cpuSeconds(ctx, p)
		if !first && elapsed > 0 {
			h.Coral.CPUPercent = percent(cpu-s.prevSelf, elapsed)
		}
		s.prevSelf = cpu
		if m, err := p.MemoryInfoWithContext(ctx); err == nil {
			h.Coral.RSSBytes = m.RSS
		}
	}

	// The agents: every process under their roots.
	var roots []int
	if s.src.AgentPIDs != nil {
		roots = s.src.AgentPIDs()
	}
	if s.src.AgentCount != nil {
		h.Agents.Count = s.src.AgentCount()
	} else {
		h.Agents.Count = len(roots)
	}
	agentCPU := map[int32]float64{}
	if len(roots) > 0 {
		var cpuDelta float64
		for _, pid := range descendants(ctx, roots) {
			p, err := process.NewProcessWithContext(ctx, pid)
			if err != nil {
				continue
			}
			cpu := cpuSeconds(ctx, p)
			agentCPU[pid] = cpu
			if prev, ok := s.prevAgents[pid]; ok && cpu >= prev {
				cpuDelta += cpu - prev
			}
			if m, err := p.MemoryInfoWithContext(ctx); err == nil {
				h.Agents.RSSBytes += m.RSS
			}
		}
		if !first && elapsed > 0 {
			h.Agents.CPUPercent = percent(cpuDelta, elapsed)
		}
	}
	s.prevAgents = agentCPU
	s.prevAt = now

	// The host.
	h.Host.CPUCount = runtime.NumCPU()
	if l, err := load.AvgWithContext(ctx); err == nil && h.Host.CPUCount > 0 {
		h.Host.Load1PerCore = round(l.Load1/float64(h.Host.CPUCount), 2)
	}
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		h.Host.MemTotal, h.Host.MemAvailable = v.Total, v.Available
	}
	if s.src.DataDir != "" {
		if u, err := disk.UsageWithContext(ctx, s.src.DataDir); err == nil {
			h.Disk.FreeBytes, h.Disk.TotalBytes = u.Free, u.Total
		}
	}

	// Responsiveness and the status.
	var r Responsiveness
	var haveR bool
	if s.src.Responsive != nil {
		r, haveR = s.src.Responsive()
	}
	if haveR {
		h.ResponsivenessMS = int64(math.Round(float64(r.Last) / float64(time.Millisecond)))
	}
	h.Status, h.Reason = classify(h, r, haveR, now)

	s.mu.Lock()
	cp := h
	s.cur, s.sampledAt = &cp, now
	s.mu.Unlock()
	return h
}

// classify applies the thresholds. Several can apply at once: stall comes first, then disk,
// memory and CPU (a slow health probe counts as CPU).
func classify(h Health, r Responsiveness, haveR bool, now time.Time) (status, reason string) {
	if haveR && (r.Last > stalledLastProbe || (!r.LastSlow.IsZero() && now.Sub(r.LastSlow) < stalledWithin)) {
		return "stalled", "stall"
	}
	if h.Disk.TotalBytes > 0 && (h.Disk.FreeBytes < busyDiskBytes || float64(h.Disk.FreeBytes) < busyDiskFraction*float64(h.Disk.TotalBytes)) {
		return "busy", "disk"
	}
	if h.Host.MemTotal > 0 && float64(h.Host.MemAvailable) < busyMemFraction*float64(h.Host.MemTotal) {
		return "busy", "memory"
	}
	if h.Host.Load1PerCore > busyLoadPerCore || (haveR && r.P95 > busyProbeP95) {
		return "busy", "cpu"
	}
	return "ok", ""
}

// cpuSeconds is a process's user plus system CPU time so far.
func cpuSeconds(ctx context.Context, p *process.Process) float64 {
	t, err := p.TimesWithContext(ctx)
	if err != nil {
		return 0
	}
	return t.User + t.System
}

// percent is CPU seconds over wall seconds as % of one core, to one decimal.
func percent(cpu, wall float64) float64 {
	if cpu < 0 || wall <= 0 {
		return 0
	}
	return round(cpu/wall*100, 1)
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

// descendants is every process under roots (the roots included). It reads only the parent links
// the walk needs, as cheaply as the OS allows (childrenFunc): building a gopsutil Process for every
// process on the machine cost 0.3 s a sample on a box with 786 processes, 65% of an idle Coral's
// CPU (#3833 item 2, #3846).
func descendants(ctx context.Context, roots []int) []int32 {
	if len(roots) == 0 {
		return nil
	}
	children, err := childrenFunc(ctx)
	if err != nil {
		return nil
	}
	seen := map[int32]bool{}
	var out []int32
	queue := make([]int32, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, int32(r))
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, pid)
		queue = append(queue, children(pid)...)
	}
	return out
}

// childrenMap turns pid -> parent pairs into a children lookup.
func childrenMap(parents map[int32]int32) func(int32) []int32 {
	kids := make(map[int32][]int32, len(parents))
	for pid, pp := range parents {
		kids[pp] = append(kids[pp], pid)
	}
	return func(pid int32) []int32 { return kids[pid] }
}
