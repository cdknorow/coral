//go:build !windows

package ptymanager

import (
	"context"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/safego"
	"github.com/subgentic/coral/internal/tmux"
)

// A helper that an agent starts, and that then detaches from it (a double fork, a new session), is found at a kill by the tag its environment carries. On macOS that tag cannot be read from a process of an
// Apple binary (perl, sleep, most of /usr/bin): such a process is found only while it is still a descendant of the shell. So Coral looks at each terminal's tree every sampleEvery while there are terminals,
// and remembers what it saw, as the guardian does for a Coral that crashes (guardian_unix.go): a helper that was a descendant at any sample is known when it has detached. The remembered set is what every end
// of a terminal starts from: Kill, sleep, restart and quit of a built-in terminal (unixPTY.seen), the tmux Kill, sleep and Restart of an agent's pane (beginTreeKill), and the guardian's own set.
//
// One tick serves every terminal: one scan of the process table, and for tmux one list-panes of the shared pane snapshot. It runs only while a terminal is tracked or a tmux backend is open, and ends with the
// last of them. A member is remembered by pid and start time (a pid given to another process since is not signalled: signalTree checks it again when it kills), the set of a root is dropped when the root ends or
// changes (a pane respawned has a new shell) and is capped (maxRemembered). Nothing here reads a process's environment, and nothing logs a tag.
//
// Not covered, and said so in the notes: a process that detaches before the first sample that sees it, and an Apple binary that does that within sampleEvery of starting.

// sampleEvery is the time between two looks at the trees.
const sampleEvery = 3 * time.Second

type descendantSampler struct {
	mu      sync.Mutex
	pty     map[member]*seen          // built-in terminals by their shell, until each ends
	clients map[*tmux.Client]struct{} // open tmux backends
	panes   map[member]*seen          // tmux agents' panes by their shell, rebuilt from each tick's list
	stop    chan struct{}             // non-nil while the loop runs; closed to end it

	inPaneLoop func() // a test's hook, called for each pane while its tree is computed (d.mu is not held)
}

var sampler = &descendantSampler{pty: map[member]*seen{}, clients: map[*tmux.Client]struct{}{}, panes: map[member]*seen{}}

// trackPTY samples the tree of a built-in terminal whose shell is root into set, until the returned function is called (the terminal ended).
func (d *descendantSampler) trackPTY(root member, set *seen) (untrack func()) {
	if root.pid <= 1 {
		return func() {}
	}
	d.mu.Lock()
	d.pty[root] = set
	d.startLocked()
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		delete(d.pty, root)
		d.stopIfIdleLocked()
		d.mu.Unlock()
	}
}

// trackTmux samples the panes of c's tmux server, until the returned function is called (the backend closed).
func (d *descendantSampler) trackTmux(c *tmux.Client) (untrack func()) {
	if c == nil {
		return func() {}
	}
	d.mu.Lock()
	d.clients[c] = struct{}{}
	d.startLocked()
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		delete(d.clients, c)
		if len(d.clients) == 0 {
			d.panes = map[member]*seen{}
		}
		d.stopIfIdleLocked()
		d.mu.Unlock()
	}
}

// rememberedPane is what the sampler has seen of the pane whose shell is root (nil when it has seen none).
func (d *descendantSampler) rememberedPane(root member) []member {
	d.mu.Lock()
	set := d.panes[root]
	d.mu.Unlock()
	if set == nil {
		return nil
	}
	return set.all()
}

func (d *descendantSampler) startLocked() {
	if d.stop != nil {
		return
	}
	stop := make(chan struct{})
	d.stop = stop
	safego.Restart("ptymanager/descendant-sampler", func() { d.loop(stop) })
}

// stopIfIdleLocked ends the loop when nothing is tracked any more (d.mu held).
func (d *descendantSampler) stopIfIdleLocked() {
	if d.stop != nil && len(d.pty) == 0 && len(d.clients) == 0 {
		close(d.stop)
		d.stop = nil
	}
}

func (d *descendantSampler) loop(stop <-chan struct{}) {
	tick := time.NewTicker(sampleEvery)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			d.sample()
		}
	}
}

// sample is one tick: the process table once, and each tracked root's tree added to its set.
func (d *descendantSampler) sample() {
	d.mu.Lock()
	pty := make(map[member]*seen, len(d.pty))
	for root, set := range d.pty {
		pty[root] = set
	}
	clients := make([]*tmux.Client, 0, len(d.clients))
	for c := range d.clients {
		clients = append(clients, c)
	}
	d.mu.Unlock()

	var paneShells []int
	for _, c := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		for _, p := range c.PaneRoots(ctx) {
			paneShells = append(paneShells, p.PID)
		}
		cancel()
	}
	if len(pty) == 0 && len(paneShells) == 0 {
		return // nothing to look at: no scan of the process table
	}
	snap := takeSnap()
	for root, set := range pty {
		set.prune()                  // what has ended first, so it does not count against the cap
		set.add(snap.tree(root, "")) // descent only: the tag is read at the kill, never here
	}
	d.mu.Lock()
	known := make(map[member]*seen, len(d.panes))
	for root, set := range d.panes {
		known[root] = set
	}
	d.mu.Unlock()
	next := make(map[member]*seen, len(paneShells)) // computed without d.mu: a Kill asking for a pane's set (rememberedPane) never waits for the trees
	for _, pid := range paneShells {
		p := snap.byPID[pid]
		if p == nil {
			continue
		}
		created, err := p.CreateTime()
		if err != nil {
			continue
		}
		root := member{pid, created}
		if d.inPaneLoop != nil {
			d.inPaneLoop()
		}
		set := known[root]
		if set == nil {
			set = &seen{}
		}
		set.prune()
		set.add(snap.tree(root, ""))
		next[root] = set
	}
	d.mu.Lock()
	d.panes = next // a pane whose session ended, or whose shell changed, is not remembered any more
	d.mu.Unlock()
}
