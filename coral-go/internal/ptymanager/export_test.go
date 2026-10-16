package ptymanager

// Test-only hooks, kept out of the shipped binary (#3824, Go cleanup 2: audit #3780 section B).

// setTimings replaces the gate's intervals and returns the previous ones (tests).
func (g *inputGate) setTimings(t gateTimings) gateTimings {
	g.mu.Lock()
	defer g.mu.Unlock()
	prev := g.timings
	g.timings = t
	return prev
}
