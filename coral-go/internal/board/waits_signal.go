package board

import "sync"

// waitSignal broadcasts "some registered wait changed" (registered, resolved,
// cancelled or expired). Long-poll handlers sleep on it instead of re-reading
// the database every 500ms per waiter.
type waitSignal struct {
	mu sync.Mutex
	ch chan struct{}
}

// changed returns a channel that is closed at the next change. Take it
// before reading wait state, so a change between the read and the select is
// not missed.
func (w *waitSignal) changed() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ch == nil {
		w.ch = make(chan struct{})
	}
	return w.ch
}

// signal wakes everyone waiting on the current channel.
func (w *waitSignal) signal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ch != nil {
		close(w.ch)
		w.ch = nil
	}
}

// WaitsChanged returns a channel closed the next time any registered wait in
// this store changes.
func (s *Store) WaitsChanged() <-chan struct{} { return s.waitChanges.changed() }
