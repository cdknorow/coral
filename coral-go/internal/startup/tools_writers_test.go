package startup

import (
	"bytes"
	"log"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
)

// H5: a tools folder accounts other than the system and administrators can change is named once
// in the startup log (and recorded for the dashboard): on Windows a folder of this user's, elsewhere
// one its group or everyone can write.
func TestStartupWarnsAboutAWritableToolsFolder(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Chmod(dir, 0o777))
	}
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags); agent.SetToolsFolderWriters("", nil) })

	warnWritableTools(dir)
	assert.Contains(t, buf.String(), "[tools] WARNING: the tools folder "+dir+" can be changed by")
	gotDir, who := agent.ToolsFolderWriters()
	assert.Equal(t, dir, gotDir)
	assert.NotEmpty(t, who)

	if runtime.GOOS != "windows" {
		buf.Reset()
		require.NoError(t, os.Chmod(dir, 0o755))
		warnWritableTools(dir)
		assert.NotContains(t, buf.String(), "WARNING", "a folder only its owner can change: no warning outside Windows")
	}
}
