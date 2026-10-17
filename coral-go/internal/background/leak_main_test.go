package background

import (
	"os"
	"testing"

	"go.uber.org/goleak"

	"github.com/cdknorow/coral/internal/testleak"
)

// TestMain fails the package when its tests leave a goroutine running (#4167).
func TestMain(m *testing.M) {
	os.Exit(testleak.Check(m.Run(),
		// killChildProcess's SIGKILL escalation: it sleeps 5 s after the SIGTERM and then kills what is
		// left of the step's process group, so it ends on its own; tests finish sooner than that.
		goleak.IgnoreAnyFunction("github.com/cdknorow/coral/internal/background.killChildProcess.func1"),
	))
}
