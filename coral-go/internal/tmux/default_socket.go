package tmux

import (
	"context"
	"log"
)

// The default-socket merge (FallbackToDefault) is being retired (#3635). It lets a Coral rooted
// at ~/.coral see agents a much older Coral started on tmux's default socket, but it also reads
// the operator's own tmux there. This release counts what the merge actually finds at startup
// and logs it; the next release turns it off by default (CORAL_TMUX_FALLBACK=1 keeps it), and
// the one after removes it.

// DefaultSocketMerges returns the Coral agent sessions ({type}-{uuid}) that are only on tmux's
// default socket, so only the merge makes them visible. It is empty when the merge is off.
func (c *Client) DefaultSocketMerges(ctx context.Context) []string {
	if !c.FallbackToDefault || c.SocketPath == "" {
		return nil
	}
	own := map[string]bool{}
	for _, p := range c.listPanesOnSocket(ctx, c.SocketPath) {
		own[p.SessionName] = true
	}
	seen := map[string]bool{}
	var only []string
	for _, p := range c.listPanesOnSocket(ctx, "") {
		name := p.SessionName
		if own[name] || seen[name] || !agentSessionRE.MatchString(name) {
			continue
		}
		seen[name] = true
		only = append(only, name)
	}
	return only
}

// LogDefaultSocketMerges logs, once at startup, how many agents the default-socket merge finds
// this time, so the next release's default (off) can be judged. It returns that count.
func (c *Client) LogDefaultSocketMerges(ctx context.Context) int {
	if !c.FallbackToDefault {
		log.Printf("[tmux] default-socket merge: off")
		return 0
	}
	n := len(c.DefaultSocketMerges(ctx))
	log.Printf("[tmux] default-socket merge: %d Coral agent session(s) found only on tmux's default socket; the merge goes off by default next release (CORAL_TMUX_FALLBACK=1 keeps it)", n)
	return n
}
