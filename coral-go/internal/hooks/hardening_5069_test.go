package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A hook reads what the agent CLI sends, up to a bound: a tool's whole output rides in a PostToolUse payload (#5069).
func TestReadStdinIsBounded(t *testing.T) {
	raw, ok, err := ReadStdin(strings.NewReader(`{"tool_name":"Bash"}`))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, `{"tool_name":"Bash"}`, string(raw))

	atBound := bytes.NewReader(make([]byte, MaxHookStdin))
	_, ok, err = ReadStdin(atBound)
	require.NoError(t, err)
	assert.True(t, ok, "exactly the bound is read")

	over := &countingReader{r: bytes.NewReader(make([]byte, MaxHookStdin+10<<20))}
	raw, ok, err = ReadStdin(over)
	require.NoError(t, err)
	assert.False(t, ok, "a payload over the bound is dropped")
	assert.Nil(t, raw)
	assert.LessOrEqual(t, over.n, MaxHookStdin+1, "and not read to its end")
}

type countingReader struct {
	r *bytes.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// A credential embedded in CORAL_URL never reaches a log or the agent's screen.
func TestRedactedURLHidesUserinfo(t *testing.T) {
	assert.Equal(t, "http://redacted@10.0.0.2:8420", RedactedURL("http://alice:s3cret@10.0.0.2:8420"))
	assert.Equal(t, "http://localhost:8420", RedactedURL("http://localhost:8420"))
	assert.NotContains(t, RedactedURL("https://tok:pw@host/x"), "pw")
}

// The board state file is named by the session name with every path separator made "_": coral-board and the message-check hook agree, and no name leaves
// the data folder.
func TestBoardStateFileNameStaysInTheFolder(t *testing.T) {
	assert.Equal(t, "board_state_claude-2f0a.json", BoardStateFileName("claude-2f0a"))
	dir := t.TempDir()
	for _, name := range []string{"../../x", "..\\..\\x", "a/b", "/etc/passwd", "..", "../"} {
		file := BoardStateFileName(name)
		assert.NotContains(t, file, "/", name)
		assert.NotContains(t, file, "\\", name)
		assert.Equal(t, dir, filepath.Dir(filepath.Join(dir, file)), "the file for %q is directly in the data folder", name)
	}
}

// One long value cannot grow the debug log past its bound by itself.
func TestDebugLogLineIsBounded(t *testing.T) {
	_, data, _ := cacheRig(t)
	DebugLog("AGENTIC_STATE DROPPED: hook_type=" + strings.Repeat("A", 1<<20))
	b, err := os.ReadFile(filepath.Join(data, "hook_cache", "debug.log"))
	require.NoError(t, err)
	assert.Less(t, len(b), debugLineMax+200, "one line of a megabyte is kept to its bound")
	assert.Contains(t, string(b), "AGENTIC_STATE DROPPED")
}
