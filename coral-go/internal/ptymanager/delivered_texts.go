package ptymanager

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// What Coral itself typed into each agent (#4306). The transcript records a notice Coral types (a task notice, the
// unread-messages nudge, the health check) as a turn of the user's, so the dashboard drew it as the Operator's own bubble
// whenever its text didn't look like a known notice. The gate knows exactly what it delivered, so it keeps a hash of each
// delivered text per session and the chat reader flags the transcript turns that match: provenance, not a text pattern.
// Only hashes are kept (a bounded ring per session and a bounded number of sessions), never the text.
//
// A real operator message is never flagged by mistake:
//   - the whole text must match (a slash command or any other prefix is part of it);
//   - a text under minDeliveredLen characters ("continue") is never recorded or matched, so the operator's identical short
//     message stays theirs;
//   - each delivery flags at most one turn, the first identical one typed at or after the delivery, so an operator who repeats a
//     long text they saw, or typed it before Coral did, keeps their turn.

const (
	deliveredTextsPerSession = 256
	deliveredTextSessions    = 512
	// minDeliveredLen is the shortest normalised text that is recorded or matched (characters).
	minDeliveredLen = 40
	// deliveredSkew is how far before its delivery a transcript turn may be stamped and still be that delivery (clock noise).
	deliveredSkew = 5 * time.Second
)

type deliveredEntry struct {
	hash [sha256.Size]byte
	at   time.Time
}

type deliveredRing struct {
	entries [deliveredTextsPerSession]deliveredEntry
	n       int // entries written (the ring holds the last deliveredTextsPerSession)
	used    uint64
}

var delivered = struct {
	sync.Mutex
	rings map[string]*deliveredRing
	tick  uint64
}{rings: map[string]*deliveredRing{}}

var (
	pastedTagRE  = regexp.MustCompile(`</?pasted_content(?:\s+id="[^"<>]*")?\s*>`)
	runOfSpaceRE = regexp.MustCompile(`\s+`)
)

// normalizeDelivered is a text as the transcript and the sender can both be compared: Claude Code's pasted_content wrapper
// removed and whitespace collapsed. Nothing else is stripped.
func normalizeDelivered(text string) string {
	text = pastedTagRE.ReplaceAllString(text, " ")
	return strings.TrimSpace(runOfSpaceRE.ReplaceAllString(text, " "))
}

func deliveredHash(text string) ([sha256.Size]byte, bool) {
	t := normalizeDelivered(text)
	if utf8.RuneCountInString(t) < minDeliveredLen {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256([]byte(t)), true
}

// noteDeliveredText records that text was typed into the session name ("<type>-<id>", or a pane target of it) just now.
func noteDeliveredText(name, text string) {
	h, ok := deliveredHash(text)
	if !ok {
		return
	}
	name = sessionOfTarget(name)
	delivered.Lock()
	defer delivered.Unlock()
	delivered.tick++
	r := delivered.rings[name]
	if r == nil {
		if len(delivered.rings) >= deliveredTextSessions {
			oldest, at := "", uint64(0)
			for k, v := range delivered.rings {
				if oldest == "" || v.used < at {
					oldest, at = k, v.used
				}
			}
			delete(delivered.rings, oldest)
		}
		r = &deliveredRing{}
		delivered.rings[name] = r
	}
	r.entries[r.n%deliveredTextsPerSession] = deliveredEntry{hash: h, at: time.Now()}
	r.n++
	r.used = delivered.tick
}

// FlagDelivered says which of a transcript's user turns Coral typed into the session. texts[i] is turn i's text ("" for a
// turn that is not a user's) and stamps[i] its time (the zero time when unknown). Turns are read in order; each recorded
// delivery is used by at most one turn, the first one with the same text that is not stamped before the delivery. It
// returns nil when nothing is remembered for the session.
func FlagDelivered(texts []string, stamps []time.Time, names ...string) []bool {
	pending := map[[sha256.Size]byte][]time.Time{} // hash -> delivery times, oldest first
	delivered.Lock()
	seen := map[string]bool{}
	for _, name := range names {
		name = sessionOfTarget(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		r := delivered.rings[name]
		if r == nil {
			continue
		}
		first := max(r.n-deliveredTextsPerSession, 0)
		for i := first; i < r.n; i++ {
			e := r.entries[i%deliveredTextsPerSession]
			pending[e.hash] = append(pending[e.hash], e.at)
		}
	}
	delivered.Unlock()
	if len(pending) == 0 {
		return nil
	}
	out := make([]bool, len(texts))
	for i, text := range texts {
		if len(text) < minDeliveredLen { // normalising only shortens
			continue
		}
		h, ok := deliveredHash(text)
		if !ok {
			continue
		}
		ats := pending[h]
		for j, at := range ats {
			if !stamps[i].IsZero() && stamps[i].Before(at.Add(-deliveredSkew)) {
				continue // typed before Coral delivered it: the operator's
			}
			out[i] = true
			pending[h] = append(ats[:j:j], ats[j+1:]...)
			break
		}
	}
	return out
}

// HasDeliveredTexts reports whether anything typed into one of the sessions is remembered (a cheap check before reading a whole transcript).
func HasDeliveredTexts(names ...string) bool {
	delivered.Lock()
	defer delivered.Unlock()
	for _, name := range names {
		if r := delivered.rings[sessionOfTarget(name)]; r != nil && r.n > 0 {
			return true
		}
	}
	return false
}
