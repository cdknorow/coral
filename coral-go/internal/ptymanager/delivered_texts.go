package ptymanager

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"sync"
)

// What Coral itself typed into each agent (#4306). The transcript records a notice Coral types (a task notice, the
// unread-messages nudge, the health check) as a turn of the user's, so the dashboard drew it as the Operator's own bubble
// whenever its text didn't look like a known notice. The gate knows exactly what it delivered, so it keeps a hash of each
// delivered text per session and the chat reader flags the transcript turns that match: provenance, not a text pattern.
// Only hashes are kept (a bounded ring per session and a bounded number of sessions), never the text.

const (
	deliveredTextsPerSession = 256
	deliveredTextSessions    = 512
)

type deliveredRing struct {
	hashes [deliveredTextsPerSession][sha256.Size]byte
	n      int // hashes written (the ring holds the last deliveredTextsPerSession)
	used   uint64
}

var delivered = struct {
	sync.Mutex
	rings map[string]*deliveredRing
	tick  uint64
}{rings: map[string]*deliveredRing{}}

var (
	pastedTagRE  = regexp.MustCompile(`</?pasted_content(?:\s+id="[^"<>]*")?\s*>`)
	leadingSlash = regexp.MustCompile(`^(?:/[a-zA-Z0-9_-]+\s*)+`)
	runOfSpaceRE = regexp.MustCompile(`\s+`)
)

// normalizeDelivered is a text as the transcript and the sender can both be compared: Claude Code's pasted_content wrapper
// removed, a leading slash command (a CLI agent's own prefix) removed, whitespace collapsed.
func normalizeDelivered(text string) string {
	text = pastedTagRE.ReplaceAllString(text, " ")
	text = strings.TrimSpace(text)
	text = leadingSlash.ReplaceAllString(text, "")
	return strings.TrimSpace(runOfSpaceRE.ReplaceAllString(text, " "))
}

func deliveredHash(text string) ([sha256.Size]byte, bool) {
	t := normalizeDelivered(text)
	if t == "" {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256([]byte(t)), true
}

// noteDeliveredText records that text was typed into the session name ("<type>-<id>", or a pane target of it).
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
	r.hashes[r.n%deliveredTextsPerSession] = h
	r.n++
	r.used = delivered.tick
}

// DeliveredMatcher returns a function that reports whether a text is one Coral typed into the session, or nil when Coral
// has typed nothing into it that is still remembered. The set is copied, so the function is cheap to call per message.
func DeliveredMatcher(names ...string) func(text string) bool {
	set := map[[sha256.Size]byte]struct{}{}
	delivered.Lock()
	for _, name := range names {
		r := delivered.rings[sessionOfTarget(name)]
		if r == nil {
			continue
		}
		for i := 0; i < min(r.n, deliveredTextsPerSession); i++ {
			set[r.hashes[i]] = struct{}{}
		}
	}
	delivered.Unlock()
	if len(set) == 0 {
		return nil
	}
	return func(text string) bool {
		h, ok := deliveredHash(text)
		if !ok {
			return false
		}
		_, hit := set[h]
		return hit
	}
}
