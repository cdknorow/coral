package startup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/config"
)

// #3492: a server on a data folder other than ~/.coral (a test server), or a dev build,
// never lets its agents fall back to the installed app's tools.
func TestSetupAgentToolsForbidsTheInstalledAppOnATestServer(t *testing.T) {
	prev := agent.InstalledToolsFallback()
	t.Cleanup(func() { agent.SetInstalledToolsFallback(prev) })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows (#3914)

	setupAgentTools(t.TempDir())
	if agent.InstalledToolsFallback() {
		t.Error("a test server's data folder: no fallback to the installed app")
	}
	setupAgentTools(filepath.Join(home, ".coral") + string(os.PathSeparator))
	if want := config.TierName != "dev"; agent.InstalledToolsFallback() != want {
		t.Errorf("~/.coral on tier %q: fallback %v, want %v", config.TierName, agent.InstalledToolsFallback(), want)
	}
}

// #307's copy note: each account once, its ways of changing the folder together, in the order the accounts first appear.
func TestMergeWritersGivesEachAccountOnce(t *testing.T) {
	got := mergeWriters([]string{"this user (the owner)", "this user", "this user (the owner, through the parent folder)", `DOMAIN\bob`, `DOMAIN\bob (through the parent folder)`})
	want := []string{"this user (the owner, through the parent folder)", `DOMAIN\bob (through the parent folder)`}
	if !slices.Equal(got, want) {
		t.Fatalf("mergeWriters = %q, want %q", got, want)
	}
	if got := mergeWriters(nil); len(got) != 0 {
		t.Fatalf("no writers: %q", got)
	}
}
