package startup

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/secfile"
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
		warnWritableTools(dir)
	case testServer:
		log.Printf("[tools] WARNING: no coral-board or coral-hook-* binaries next to this build, and a dev or test server " +
			"never uses the installed app's: agents launched here run whatever is on their PATH, or none, so their " +
			"status events may be missing or come from another version. Build them next to this binary " +
			"(make dev, or go build -o <dir>/ ./cmd/...) or set " + agent.HookDirEnv + ".")
	default:
		log.Printf("[tools] WARNING: no Coral tools found (coral-board, coral-hook-*): agents launched here run whatever is on their PATH")
	}
}

// warnWritableTools says, once at startup, when accounts other than the system and administrators
// can change the tools folder (secfile.Writers): every agent runs the hooks in it by their absolute
// path, so a program running as such an account could replace them (H5: a portable Coral unpacked in
// the user's profile). Program Files, where the installer puts Coral, has none. The dashboard shows
// the same (agent.ToolsFolderWriters).
func warnWritableTools(dir string) {
	who, err := secfile.Writers(dir)
	if err != nil {
		log.Printf("[tools] could not read who can change the tools folder %s: %v", dir, err)
		return
	}
	agent.SetToolsFolderWriters(dir, who)
	if len(who) > 0 {
		log.Printf("[tools] WARNING: the tools folder %s can be changed by %s, not only by an administrator: "+
			"a program running as that account could replace the hooks every agent runs. Install Coral with the "+
			"installer (Program Files) to keep them where only an administrator can change them.", dir, strings.Join(who, ", "))
	}
}
