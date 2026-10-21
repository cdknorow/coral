package background

import (
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/testleak"
)

// TestMain fails the package when its tests leave a goroutine running (#4167).
func TestMain(m *testing.M) {
	code := m.Run()
	// Tests that type through the real input gate leave a send held, so its process-wide retry loop is
	// asleep (2 s) when a short run ends: stop it before the check.
	ptymanager.StopGate(10 * time.Second)
	os.Exit(testleak.Check(code,
		// killChildProcess's SIGKILL escalation: it sleeps 5 s after the SIGTERM and then kills what is
		// left of the step's process group, so it ends on its own; tests finish sooner than that.
		goleak.IgnoreAnyFunction("github.com/subgentic/coral/internal/background.killChildProcess.func1"),
	))
}
