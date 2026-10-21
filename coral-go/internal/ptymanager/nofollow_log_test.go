package ptymanager

import (
	"path/filepath"
	"testing"

	"github.com/subgentic/coral/internal/linktest"
)

// #3920: an agent's terminal log is never opened through a link planted at its name (a symbolic
// link; on Windows also a junction): on Windows it was, so the terminal's output could be sent
// into another file.
func TestAgentLogNeverOpensThroughAPlantedLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_coral_agent.log")
	linktest.Each(t, path, func(t *testing.T, p linktest.Planted) {
		if f, err := openLogAppend(path); err == nil {
			f.Close()
			t.Errorf("%s: the agent log was opened through the link", p.Kind)
		}
	})
}
