package routes

import (
	"os"
	"runtime"
)

// oracleN is the iteration count of a fuzz or race test with no outcome floors (the shell-risk fuzz, the log-tail and history races)
// on this OS. On Windows each iteration (a temp folder, a SQLite database, a handler) costs several times what it does on Linux, so
// the routes package took 7-8 minutes quiet and hit go test's default 10-minute timeout under load (#83). A third of the iterations
// still reaches every outcome floor; CORAL_FULL_ORACLES=1 runs them all, and Linux and macOS always do.
func oracleN(n int) int {
	if runtime.GOOS == "windows" && os.Getenv("CORAL_FULL_ORACLES") == "" {
		return max(n/3, 1)
	}
	return n
}
