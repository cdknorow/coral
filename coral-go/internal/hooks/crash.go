package hooks

import (
	"log"
	"os"
	"runtime/debug"

	"github.com/subgentic/coral/internal/logsafe"
)

// LogCrash writes a hook's panic and its stack to standard error, which the agent's terminal and Coral's logs carry, through logsafe.Writer: the home folder, user and computer name and any
// token-shaped string in the panic value never leave the process (#6069).
func LogCrash(r any) {
	log.SetOutput(logsafe.Writer(os.Stderr))
	log.Printf("[CRASH] hook panicked: %v\n%s", r, debug.Stack())
}
