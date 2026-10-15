package startup

import (
	"log"
	"os"
	"path/filepath"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/config"
)

// setupAgentTools decides where agents launched by this server get Coral's tools
// (coral-board and the coral-hook-* binaries) and says so in the log (#3492). A dev
// build, or a server on a data folder other than ~/.coral (a test server), never falls
// back to the installed Coral app: its agents would run an older app's hooks against
// this server without anyone noticing. Put this build's binaries next to it (make dev
// builds them all into bin/) or set CORAL_HOOK_DIR.
func setupAgentTools(coralDir string) {
	home, _ := os.UserHomeDir()
	testServer := config.TierName == "dev" || filepath.Clean(coralDir) != filepath.Join(home, ".coral")
	agent.SetInstalledToolsFallback(!testServer)
	dir, source := agent.CoralToolsSource()
	switch {
	case dir != "":
		log.Printf("[tools] agents get coral-board and the Coral hooks from %s (%s)", dir, source)
	case testServer:
		log.Printf("[tools] WARNING: no coral-board or coral-hook-* binaries next to this build, and a dev or test server " +
			"never uses the installed app's: agents launched here run whatever is on their PATH, or none, so their " +
			"status events may be missing or come from another version. Build them next to this binary " +
			"(make dev, or go build -o <dir>/ ./cmd/...) or set " + agent.HookDirEnv + ".")
	default:
		log.Printf("[tools] WARNING: no Coral tools found (coral-board, coral-hook-*): agents launched here run whatever is on their PATH")
	}
}
