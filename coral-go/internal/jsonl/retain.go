package jsonl

// Retain drops the cached state of every session for which keep returns
// false and returns how many sessions were dropped. The cache holds every
// parsed message of each transcript read, so a long-running server must evict
// sessions that have ended; a dropped session that is read again is simply
// re-parsed from its transcript.
func (r *SessionReader) Retain(keep func(sessionID string) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	dropped := 0
	for id := range r.cache {
		if !keep(id) {
			delete(r.cache, id)
			delete(r.firstPrompts, id)
			dropped++
		}
	}
	for id := range r.firstPrompts {
		if !keep(id) {
			delete(r.firstPrompts, id)
			dropped++
		}
	}
	return dropped
}

// CachedSessions returns the number of sessions with cached transcript state
// (parsed messages or a first prompt).
func (r *SessionReader) CachedSessions() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.cache)
	for id := range r.firstPrompts {
		if _, ok := r.cache[id]; !ok {
			n++
		}
	}
	return n
}
