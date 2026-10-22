package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var hostileIDs = []string{
	`<img src=x onerror=alert(1)>`, `<b onclick=x>`, `a b`, `a;b`, `a"b`, `a/b`, `a\b`, `..`, `a.b`, ``,
	strings.Repeat("a", 65),
}

func TestValidID(t *testing.T) {
	for _, id := range []string{fixtureID, "agentX", "a_b-C9", strings.Repeat("a", 64)} {
		assert.True(t, ValidID(id), id)
	}
	for _, id := range hostileIDs {
		assert.False(t, ValidID(id), id)
	}
}

func TestIDFromPathRefusesAMarkupName(t *testing.T) {
	assert.Equal(t, fixtureID, IDFromPath("agent-"+fixtureID+".jsonl"))
	for _, id := range hostileIDs {
		assert.Equal(t, "", IDFromPath("agent-"+id+".jsonl"), id)
	}
}

func TestATranscriptWithAMarkupAgentIDKeepsTheFileNameID(t *testing.T) {
	for _, bad := range []string{`<b onclick=x>`, `<img src=x onerror=alert(1)>`} {
		dir := t.TempDir()
		p := filepath.Join(dir, "agent-goodid.jsonl")
		line := `{"type":"user","agentId":` + jsonQuote(bad) + `,"sessionId":"s","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"hi"}}` + "\n"
		require.NoError(t, os.WriteFile(p, []byte(line), 0o600))
		tr, err := ParseTranscript(p)
		require.NoError(t, err)
		assert.Equal(t, "goodid", tr.SubagentID)
	}
}

func TestDiscoverSkipsANameThatIsNotAPlainID(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "sess.jsonl")
	sub := Dir(parent)
	require.NoError(t, os.MkdirAll(sub, 0o700))
	for _, n := range []string{"agent-ok1.jsonl", "agent-a b.jsonl", "agent-a;b.jsonl", "agent-<b onclick=x>.jsonl"} {
		// '<' cannot be in a Windows file name; the others can, so those still run there.
		_ = os.WriteFile(filepath.Join(sub, n), []byte("{}\n"), 0o600)
	}
	files, err := Discover(parent)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "ok1", files[0].SubagentID)
}

func jsonQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\`, `"`, `\"`).Replace(s) + `"`
}
