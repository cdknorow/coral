package background

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3587 SHOULD 1, run on every OS (#3615): a step that replaced stderr.txt with a hard link to
// an outside file never has Coral's note appended to that file. On Windows the count comes from
// the handle.
func TestAppendNoteNeverWritesThroughAHardLink(t *testing.T) {
	runDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(runDir, "step_0"), 0o755))
	victim := filepath.Join(t.TempDir(), "victim.txt")
	require.NoError(t, os.WriteFile(victim, []byte("orig"), 0o600))
	require.NoError(t, os.Link(victim, filepath.Join(runDir, "step_0", "stderr.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(runDir, "step_0", "own.txt"), []byte("x\n"), 0o600))
	root, err := os.OpenRoot(runDir)
	require.NoError(t, err)
	defer root.Close()
	sf := stepFiles{run: &runFiles{root: root, dir: runDir}, rel: "step_0", dir: filepath.Join(runDir, "step_0")}

	sf.appendNote("stderr.txt", "coral: note")
	b, _ := os.ReadFile(victim)
	assert.Equal(t, "orig", string(b), "the hard-linked file is unchanged")

	sf.appendNote("own.txt", "coral: note")
	b, _ = os.ReadFile(filepath.Join(runDir, "step_0", "own.txt"))
	assert.Equal(t, "x\ncoral: note\n", string(b), "a file with one name gets the note")
}
