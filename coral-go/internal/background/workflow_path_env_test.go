package background

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// The Coral tools folder is its own PATH entry for a step: joined with the platform's separator, so on Windows ';' and not a ':' that turned
// "<tools>:C:\Windows\system32" into one invalid entry and lost the next one (#126 F2).
func TestStepEnvPutsTheToolsDirFirstAsItsOwnPathEntry(t *testing.T) {
	tools := t.TempDir()
	t.Setenv("CORAL_HOOK_DIR", tools)
	sys := filepath.Join(t.TempDir(), "system32")
	other := filepath.Join(t.TempDir(), "bin")
	t.Setenv("PATH", sys+string(os.PathListSeparator)+other)

	wr := &WorkflowRunner{port: 8450, host: "127.0.0.1", dataDir: t.TempDir()}
	env := wr.buildStepEnv(&store.Workflow{Name: "w", RepoPath: t.TempDir()}, 1, t.TempDir(), 0, t.TempDir(), 1, nil)
	var path string
	for _, kv := range env {
		if len(kv) > 5 && kv[:5] == "PATH=" {
			path = kv[5:]
		}
	}
	require.NotEmpty(t, path, "a PATH for the step")
	assert.Equal(t, []string{tools, sys, other}, filepath.SplitList(path))
}
