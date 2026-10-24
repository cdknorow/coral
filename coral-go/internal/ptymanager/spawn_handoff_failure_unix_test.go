//go:build !windows

package ptymanager

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subgentic/coral/internal/cmdhandoff"
)

// A start command Coral cannot hand to the new session (TMPDIR naming a folder that is gone, so a long command's script cannot be written) failed the launch only in a log line: the session stayed, an idle shell, and the run
// stayed "running" until its deadline (Codex Test's #6953, #6978). The launch now fails at once with a plain reason and leaves no session behind; with Coral's own folder as the fallback it succeeds.
func TestTmuxSpawnFailsAtOnceWhenTheStartCommandCannotBeHandedOver(t *testing.T) {
	b := newTestTmuxBackend(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "deleted"))
	t.Cleanup(func() { cmdhandoff.SetFallbackDir(func() string { return "" }) })
	cmdhandoff.SetFallbackDir(func() string { return "" })
	long := "sleep 60 # " + strings.Repeat("x", cmdhandoff.InlineLimit+50) // longer than a shell takes inline: handed over as a script

	err := b.Spawn("handoff-agent", "claude", t.TempDir(), "sid-handoff", long, 80, 24)
	if !errors.Is(err, ErrStartCommand) {
		t.Fatalf("Spawn = %v, want ErrStartCommand", err)
	}
	if b.IsRunning("handoff-agent") {
		t.Error("no session is left behind to sit at an idle shell")
	}

	fallback := t.TempDir()
	cmdhandoff.SetFallbackDir(func() string { return fallback })
	if err := b.Spawn("handoff-agent2", "claude", t.TempDir(), "sid-handoff2", long, 80, 24); err != nil {
		t.Fatalf("with the fallback folder the launch goes on: %v", err)
	}
	if !b.IsRunning("handoff-agent2") {
		t.Error("the session runs")
	}
}
