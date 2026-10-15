package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Pi's launch command keeps the session dir under pi-sessions and adds --continue only for a
// canonical resume id (#3582): a traversal id is ignored, so no path escapes and no stale
// session is resumed.
func TestPiLaunchResumeIDIsValidated(t *testing.T) {
	coralDir := t.TempDir()
	valid := "11111111-2222-4333-8444-555555555555"

	t.Run("valid resume id", func(t *testing.T) {
		cmd := (&PiAgent{}).BuildLaunchCommand(LaunchParams{SessionID: "22222222-2222-4333-8444-555555555555", ResumeSessionID: valid, WorkingDir: t.TempDir(), CoralDir: coralDir})
		assert.Contains(t, cmd, filepath.Join(coralDir, "pi-sessions", valid))
		assert.Contains(t, cmd, "--continue")
	})

	for _, bad := range []string{"../escape", "a/../../b", "/abs", ".."} {
		t.Run("bad resume id "+bad, func(t *testing.T) {
			sid := "22222222-2222-4333-8444-555555555555"
			cmd := (&PiAgent{}).BuildLaunchCommand(LaunchParams{SessionID: sid, ResumeSessionID: bad, WorkingDir: t.TempDir(), CoralDir: coralDir})
			assert.NotContains(t, cmd, "--continue", "a bad resume id must not resume")
			assert.Contains(t, cmd, filepath.Join(coralDir, "pi-sessions", sid), "the session dir is the fresh server id")
			assert.NotContains(t, cmd, "pi-sessions/..", "no path escapes pi-sessions")
			// Nothing was created outside the coral dir.
			up, _ := os.ReadDir(filepath.Dir(coralDir))
			for _, e := range up {
				assert.False(t, strings.HasPrefix(e.Name(), "escape") || e.Name() == "b", "created outside: %s", e.Name())
			}
		})
	}
}
