package background

import (
	"time"

	"github.com/cdknorow/coral/internal/store"
)

// setMtime records the last-polled mtime of a transcript and the session it
// belongs to.
func (p *TokenPoller) setMtime(sessionID, path string, t time.Time) {
	p.lastMtime[path] = t
	p.mtimeOwner[path] = sessionID
}

// pruneEnded drops the poller's state for sessions that are no longer live
// (sleeping sessions are live and keep theirs). Without it every session ever
// polled keeps its entries for the life of the server. Dropping is safe: a
// session that comes back is re-read, and usage rows are inserted with
// INSERT OR IGNORE and subagents upserted, so nothing is counted twice.
func (p *TokenPoller) pruneEnded(live []store.LiveSession) {
	keep := make(map[string]bool, len(live))
	for _, ls := range live {
		keep[ls.SessionID] = true
	}
	for id := range p.sessionPaths {
		if !keep[id] {
			delete(p.sessionPaths, id)
		}
	}
	for id := range p.lastCumulative {
		if !keep[id] {
			delete(p.lastCumulative, id)
		}
	}
	for id := range p.lastEntryCount {
		if !keep[id] {
			delete(p.lastEntryCount, id)
		}
	}
	for path := range p.lastMtime {
		if owner, ok := p.mtimeOwner[path]; !ok || !keep[owner] {
			delete(p.lastMtime, path)
			delete(p.mtimeOwner, path)
		}
	}
}
