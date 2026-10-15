package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3598 (weak site W10): a session's teardown removes its own temp files and nothing else. A
// session id like "a" used to glob coral_*_a* and remove every other session's prompt and
// settings files and agy hook folders.
func TestCleanupTempFilesRemovesOnlyThatSession(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	require.Equal(t, filepath.Clean(tmp), filepath.Clean(os.TempDir()))

	a := "0b2f6c1e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	b := "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d" // starts with "a"
	file := func(name string) string {
		p := filepath.Join(tmp, name)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		return p
	}
	dir := func(name string) string {
		p := filepath.Join(tmp, name)
		require.NoError(t, os.MkdirAll(filepath.Join(p, "hooks"), 0o700))
		return p
	}
	aFiles := []string{file("coral_sysprompt_" + a + "_123.md"), file("coral_settings_" + a + ".json"), dir("coral_agyhooks_" + a + "_456")}
	bFiles := []string{file("coral_sysprompt_" + b + "_789.md"), file("coral_settings_" + b + ".json"), dir("coral_agyhooks_" + b + "_012")}
	longer := file("coral_sysprompt_" + a + "x_1.md") // a name that only starts with a's id
	other := file("unrelated_" + a + ".txt")

	for _, id := range []string{"", "a", "a1", "*", "..", "coral", "0B2F6C1E-3D4A-4B5C-8D6E-7F8091A2B3C4", "{" + a + "}", a[:8]} {
		CleanupTempFiles(id)
	}
	for _, p := range append(append(append([]string{}, aFiles...), bFiles...), longer, other) {
		assert.True(t, exists(p), "nothing is removed for a non-canonical id: %s", p)
	}

	CleanupTempFiles(a)
	for _, p := range aFiles {
		assert.NoFileExists(t, p, "a's own files and hook folder go")
		assert.NoDirExists(t, p)
	}
	for _, p := range bFiles {
		assert.True(t, exists(p), "b's files stay: %s", p)
	}
	assert.FileExists(t, longer, "an id that only starts with a's stays")
	assert.FileExists(t, other, "a file not named coral_<kind>_<id> stays")
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
