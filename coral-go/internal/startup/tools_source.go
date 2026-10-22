package startup

import (
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"os"
	"path/filepath"
	"slices"
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
		log.Printf("[tools] agents get coral-board and the Coral hooks from %s (%s)", logsafe.Path(dir), source)
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
		log.Printf("[tools] could not read who can change the tools folder %s: %v", logsafe.Path(dir), err)
		return
	}
	who = mergeWriters(who)
	agent.SetToolsFolderWriters(dir, who)
	if len(who) > 0 {
		log.Printf("[tools] WARNING: the tools folder %s can be changed by %s, not only by an administrator: "+
			"a program running as that account could replace the hooks every agent runs. Install Coral with the "+
			"installer (Program Files) to keep them where only an administrator can change them.", logsafe.Path(dir), strings.Join(who, ", "))
	}
}

// mergeWriters gives each account once (#307's copy note): secfile.Writers lists an account once per way it can change the folder, so
// the warning read "this user (the owner), this user, this user (the owner, through the parent folder)". The ways are kept, once each,
// in one parenthesis: "this user (the owner, through the parent folder)". The order is the order the accounts first appear in.
func mergeWriters(who []string) []string {
	var names []string
	ways := map[string][]string{}
	for _, w := range who {
		name, how, _ := strings.Cut(w, " (")
		how = strings.TrimSuffix(how, ")")
		if _, seen := ways[name]; !seen {
			names = append(names, name)
			ways[name] = nil
		}
		for _, h := range strings.Split(how, ", ") {
			if h != "" && !slices.Contains(ways[name], h) {
				ways[name] = append(ways[name], h)
			}
		}
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if len(ways[n]) == 0 {
			out = append(out, n)
		} else {
			out = append(out, n+" ("+strings.Join(ways[n], ", ")+")")
		}
	}
	return out
}
