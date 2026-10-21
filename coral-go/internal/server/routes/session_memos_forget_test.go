package routes

import (
	"fmt"
	"sync"
	"testing"

	"github.com/subgentic/coral/internal/ptymanager"
)

// #4057: the routes' per-session log memos (an exited agent's skipped input, keyed by session
// id or name; a terminal agent's; a refused board notice, per board and session) are
// forgotten when the session ends, so after N sessions start and end they are back to their
// starting size.
func TestSessionMemosForgottenAtSessionEnd(t *testing.T) {
	size := func(m *sync.Map) int {
		n := 0
		m.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	maps := map[string]*sync.Map{"exitLogged": &exitLogged, "terminalLogged": &terminalLogged, "refusedNotices": &refusedNotices}
	start := map[string]int{}
	for k, m := range maps {
		start[k] = size(m)
	}
	name := func(i int) (string, string) {
		sid := fmt.Sprintf("aaaaaaaa-4057-4000-8000-%012d", i)
		return "claude-" + sid, sid
	}
	for i := 0; i < 50; i++ {
		n, sid := name(i)
		exitLogged.Store(sid, true) // keyed by id when the caller has one
		exitLogged.Store(n, true)   // by name when it doesn't
		terminalLogged.Store(sid, true)
		refusedNotices.Store("team-a\x00"+n, true)
		refusedNotices.Store("team-b\x00"+n, true)
	}
	for i := 0; i < 50; i++ {
		n, _ := name(i)
		ptymanager.DropHeld(n, "killed")
	}
	for k, m := range maps {
		if got := size(m); got != start[k] {
			t.Errorf("%s: %d entries after the sessions ended, want %d", k, got, start[k])
		}
	}
}
