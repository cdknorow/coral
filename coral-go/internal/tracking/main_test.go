package tracking

import (
	"os"
	"testing"
)

// TestMain clears CORAL_TELEMETRY_DISABLED, which CI sets for every job (windows-tests.yml, the
// release): these tests check what is sent, so with it set nothing was (#3920 T4). The tests that
// check the variable set it themselves.
func TestMain(m *testing.M) {
	os.Unsetenv("CORAL_TELEMETRY_DISABLED")
	os.Exit(m.Run())
}
