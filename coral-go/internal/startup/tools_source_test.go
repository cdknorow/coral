package startup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/config"
)

// #3492: a server on a data folder other than ~/.coral (a test server), or a dev build,
// never lets its agents fall back to the installed app's tools.
func TestSetupAgentToolsForbidsTheInstalledAppOnATestServer(t *testing.T) {
	prev := agent.InstalledToolsFallback()
	t.Cleanup(func() { agent.SetInstalledToolsFallback(prev) })
	home := t.TempDir()
	t.Setenv("HOME", home)

	setupAgentTools(t.TempDir())
	if agent.InstalledToolsFallback() {
		t.Error("a test server's data folder: no fallback to the installed app")
	}
	setupAgentTools(filepath.Join(home, ".coral") + string(os.PathSeparator))
	if want := config.TierName != "dev"; agent.InstalledToolsFallback() != want {
		t.Errorf("~/.coral on tier %q: fallback %v, want %v", config.TierName, agent.InstalledToolsFallback(), want)
	}
}
