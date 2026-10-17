package ptymanager

// A viewer that falls a whole buffer behind the agent's output (a tab in the background, a slow
// link, a stalled Coral Hub stream) is cut off instead of being sent the output with frames
// missing, which left its terminal garbled until it reconnected (#4161). Its channel is closed
// and the cut-off recorded; the viewer's socket then starts again from a fresh replay.

// LagReporter is implemented by backends whose subscribers can be cut off for lagging.
type LagReporter interface {
	// Lagged reports whether subscriberID's channel was closed because it fell behind (once:
	// the record is cleared), as opposed to the terminal ending.
	Lagged(name, subscriberID string) bool
}

// fanOut sends data to every subscriber without blocking. A subscriber whose buffer is full is
// closed, removed and recorded in lagged. The caller holds the lock that guards both maps.
func fanOut(subs map[string]chan []byte, lagged map[string]bool, data []byte) {
	for id, ch := range subs {
		select {
		case ch <- data:
		default:
			close(ch)
			delete(subs, id)
			lagged[id] = true
		}
	}
}
