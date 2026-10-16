package background

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cdknorow/coral/internal/ptymanager"
)

// #4057: the "agent exited" and terminal-agent log memos are forgotten when each session ends,
// so after N sessions start and end they are back to their starting size.
func TestExitMemosForgottenAtSessionEnd(t *testing.T) {
	size := func(m *sync.Map) int {
		n := 0
		m.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	startExit, startTerm := size(&exitLogged), size(&terminalLogged)
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("claude-4057-bg-%d", i)
		exitLogged.Store(name, true) // as skipIfAgentExited records a logged exit
		terminalLogged.Store("terminal-4057-bg-"+fmt.Sprint(i), true)
	}
	for i := 0; i < 50; i++ {
		ptymanager.DropHeld(fmt.Sprintf("claude-4057-bg-%d", i), "killed")
		ptymanager.DropHeld("terminal-4057-bg-"+fmt.Sprint(i), "killed")
	}
	if size(&exitLogged) != startExit || size(&terminalLogged) != startTerm {
		t.Fatalf("after the sessions ended: exitLogged %d (want %d), terminalLogged %d (want %d)",
			size(&exitLogged), startExit, size(&terminalLogged), startTerm)
	}
}
