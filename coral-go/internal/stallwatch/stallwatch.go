// Package stallwatch notices when a part of the server stops answering and keeps the
// evidence (#3262, a follow-up of #3226, where a terminal that stopped reading froze every
// log call and with it the server). Every Interval it runs each probe (a cheap health
// check of the HTTP router, the logger, a database); a probe that hasn't returned after
// Threshold is a stall, and the watchdog writes a full goroutine dump to
// <dir>/stall-<time>.txt.
//
// The dump is written with direct file calls, never through the logger (which may be the
// thing that is stuck), at most once per stall and once per MinGap, and only the newest
// Keep dumps are kept. A probe that is still stuck is never started again, so a stall
// doesn't pile up goroutines.
package stallwatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"
)

// Probe is one health check. Check should return quickly when the part is healthy; it is
// given no deadline, because a check that never returns is exactly what is detected.
type Probe struct {
	Name  string
	Check func(ctx context.Context) error
}

// Config tunes the watchdog. Zero fields take the defaults.
type Config struct {
	Dir       string        // where stall-*.txt dumps go (Coral's data folder)
	Interval  time.Duration // how often probes run (default 10 s)
	Threshold time.Duration // how long a probe may take before it is a stall (default 30 s)
	MinGap    time.Duration // the least time between two dumps (default 10 min)
	Keep      int           // dumps kept, newest first (default 5)
	Probes    []Probe
	now       func() time.Time
}

// FilePrefix starts every dump's name.
const FilePrefix = "stall-"

type probeState struct {
	running  bool
	started  time.Time
	reported bool          // this stall already has a dump (or was skipped by MinGap)
	last     time.Duration // how long its last completed run took
}

// SlowThreshold is how long a probe may take before the machine counts as stalled for the
// hub's machine health (#3444); far below Threshold, which writes a dump.
const SlowThreshold = 2 * time.Second

// latencyWindow is how many recent probe latencies Stats' p95 is taken over.
const latencyWindow = 60

// Watchdog runs the probes; see Start.
type Watchdog struct {
	cfg      Config
	mu       sync.Mutex
	state    map[string]*probeState
	lastDump time.Time
	dumps    int

	latencies []time.Duration // the most recent completed probe latencies (latencyWindow)
	lastSlow  time.Time       // the last time a probe took (or had been running) SlowThreshold or more
}

// Stats is the watchdog's view of how responsive the server is (#3444).
type Stats struct {
	Last     time.Duration // the slowest probe's last run, or how long one still running has run
	P95      time.Duration // the 95th percentile of recent probe latencies (0 before any)
	LastSlow time.Time     // the last time a probe took SlowThreshold or more (zero: never)
}

// Stats reports Last, P95 and LastSlow. A probe still running counts with its elapsed time,
// so a probe that never returns shows as slow at once.
func (w *Watchdog) Stats() Stats {
	now := w.cfg.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	var st Stats
	for _, p := range w.cfg.Probes {
		ps := w.state[p.Name]
		d := ps.last
		if ps.running && now.Sub(ps.started) > d {
			d = now.Sub(ps.started)
		}
		if d > st.Last {
			st.Last = d
		}
	}
	if st.Last >= SlowThreshold && now.After(w.lastSlow) {
		w.lastSlow = now
	}
	st.LastSlow = w.lastSlow
	if n := len(w.latencies); n > 0 {
		sorted := append([]time.Duration(nil), w.latencies...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		st.P95 = sorted[(n*95+99)/100-1]
	}
	return st
}

// Start runs the watchdog until ctx ends.
func Start(ctx context.Context, cfg Config) *Watchdog {
	w := New(cfg)
	go w.run(ctx)
	return w
}

// New returns a watchdog with defaults filled in; Tick drives it (Start does it on a timer).
func New(cfg Config) *Watchdog {
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = 30 * time.Second
	}
	if cfg.MinGap <= 0 {
		cfg.MinGap = 10 * time.Minute
	}
	if cfg.Keep <= 0 {
		cfg.Keep = 5
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	w := &Watchdog{cfg: cfg, state: map[string]*probeState{}}
	for _, p := range cfg.Probes {
		w.state[p.Name] = &probeState{}
	}
	return w
}

func (w *Watchdog) run(ctx context.Context) {
	t := time.NewTicker(w.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx)
		}
	}
}

// Tick starts every probe that isn't still running and writes a dump for a probe that has
// been running past the threshold. It reports the dump's path, or "".
func (w *Watchdog) Tick(ctx context.Context) string {
	now := w.cfg.now()
	var stuck []string
	var longest time.Duration
	w.mu.Lock()
	for _, p := range w.cfg.Probes {
		st := w.state[p.Name]
		if st.running {
			if d := now.Sub(st.started); d >= w.cfg.Threshold && !st.reported {
				stuck = append(stuck, fmt.Sprintf("%s (no answer for %s)", p.Name, d.Round(time.Second)))
				if d > longest {
					longest = d
				}
			}
			continue
		}
		st.running, st.started, st.reported = true, now, false
		go w.probe(ctx, p, st)
	}
	dueGap := w.lastDump.IsZero() || now.Sub(w.lastDump) >= w.cfg.MinGap
	if len(stuck) > 0 {
		for _, p := range w.cfg.Probes {
			if st := w.state[p.Name]; st.running && now.Sub(st.started) >= w.cfg.Threshold {
				st.reported = true // one dump per stall; skipped ones aren't retried
			}
		}
	}
	if len(stuck) == 0 || !dueGap {
		w.mu.Unlock()
		return ""
	}
	w.lastDump = now
	w.mu.Unlock()
	// No message if the dump fails: the logger and the terminal (stderr) may be exactly
	// what is stuck, and a watchdog must never block on them.
	path, err := w.dump(now, stuck)
	if err != nil {
		return ""
	}
	return path
}

func (w *Watchdog) probe(ctx context.Context, p Probe, st *probeState) {
	defer func() {
		_ = recover() // a probe that panics is not a stall
		w.mu.Lock()
		st.running = false
		w.mu.Unlock()
	}()
	_ = p.Check(ctx)
	d := w.cfg.now().Sub(st.started)
	w.mu.Lock()
	st.last = d
	w.latencies = append(w.latencies, d)
	if len(w.latencies) > latencyWindow {
		w.latencies = w.latencies[len(w.latencies)-latencyWindow:]
	}
	if d >= SlowThreshold {
		w.lastSlow = w.cfg.now()
	}
	w.mu.Unlock()
}

// dump writes the goroutine dump with direct file calls (0600) and prunes old ones.
func (w *Watchdog) dump(now time.Time, stuck []string) (string, error) {
	if w.cfg.Dir == "" {
		return "", fmt.Errorf("no data folder")
	}
	name := FilePrefix + now.UTC().Format("20060102T150405.000Z") + ".txt"
	path := filepath.Join(w.cfg.Dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(f, "Coral stall dump %s\nStuck: %s\nGoroutines: %d\nThreshold: %s\n\n",
		now.UTC().Format(time.RFC3339), strings.Join(stuck, "; "), runtime.NumGoroutine(), w.cfg.Threshold)
	if p := pprof.Lookup("goroutine"); p != nil {
		err = p.WriteTo(f, 2) // every goroutine's full stack, with how long it has waited
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	w.mu.Lock()
	w.dumps++
	w.mu.Unlock()
	w.prune()
	return path, err
}

// prune keeps the newest Keep dumps (names sort by time).
func (w *Watchdog) prune() {
	matches, err := filepath.Glob(filepath.Join(w.cfg.Dir, FilePrefix+"*.txt"))
	if err != nil || len(matches) <= w.cfg.Keep {
		return
	}
	sort.Strings(matches)
	for _, m := range matches[:len(matches)-w.cfg.Keep] {
		_ = os.Remove(m)
	}
}

// Dumps is how many dumps this watchdog wrote.
func (w *Watchdog) Dumps() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dumps
}
