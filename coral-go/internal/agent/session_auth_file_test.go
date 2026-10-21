package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/agentenv"
)

// #13: only a Codex agent's launch line names its token file (agentenv.SessionAuthFileEnv), and
// keeps it; every other agent's line removes the variable, so none inherits one. The token itself
// is never in a line.
func TestOnlyCodexGetsTheSessionAuthFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "t-13")
	SessionTokenMinter = func(string, string) string { return file }
	t.Cleanup(func() { SessionTokenMinter = nil })
	p := LaunchParams{SessionID: "11111111-2222-3333-4444-555555555513", CoralPort: 8420}
	defer CleanupTempFiles(p.SessionID)
	set := func(line string) bool {
		return strings.Contains(line, "$env:"+agentenv.SessionAuthFileEnv+" = ") || strings.Contains(line, " "+agentenv.SessionAuthFileEnv+"=")
	}
	unset := func(line string) bool {
		return strings.Contains(line, "Remove-Item Env:"+agentenv.SessionAuthFileEnv+" ") || strings.Contains(line, " "+agentenv.SessionAuthFileEnv+" ") && strings.Contains(line, "unset ")
	}
	codex := buildLaunch(&CodexAgent{}, p)
	assert.True(t, set(codex), codex)
	assert.Contains(t, codex, "t-13")
	assert.NotContains(t, codex, "Remove-Item -Force -LiteralPath", "the file stays")
	assert.NotContains(t, codex, "rm -f", "the file stays")
	for name, ag := range map[string]Agent{"claude": &ClaudeAgent{}, "agy": &AgyAgent{}, "pi": &PiAgent{}} {
		line := buildLaunch(ag, p)
		assert.False(t, set(line), "%s: %s", name, line)
		assert.True(t, unset(line), "%s: the variable is removed: %s", name, line)
	}
}
