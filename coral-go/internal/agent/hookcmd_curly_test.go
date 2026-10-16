package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3930 (Security's #3927 finding 6): a tools folder whose path holds a curly double quote, which
// ends a double-quoted string in PowerShell, is never put inside double quotes: the hook command
// stays the bare name (found on PATH), as for the other characters a shell would read there.
func TestHookCommandRefusesCurlyDoubleQuotesInThePath(t *testing.T) {
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	for _, q := range []string{"\u201c", "\u201d", "\u201e"} {
		tools := filepath.Join(t.TempDir(), "Coral "+q+"; calc "+q)
		require.NoError(t, os.Mkdir(tools, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(tools, "coral-hook-agentic-state"+exe), []byte("x"), 0o755))
		t.Setenv(HookDirEnv, tools)
		assert.Equal(t, "coral-hook-agentic-state --hooks-rev 2", ResolveHookCommand("coral-hook-agentic-state --hooks-rev 2"), "%q", q)
	}
	// A plain folder still gets the quoted absolute path.
	tools := filepath.Join(t.TempDir(), "Coral Tools")
	require.NoError(t, os.Mkdir(tools, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tools, "coral-hook-agentic-state"+exe), []byte("x"), 0o755))
	t.Setenv(HookDirEnv, tools)
	assert.Equal(t, `"`+filepath.ToSlash(filepath.Join(tools, "coral-hook-agentic-state"+exe))+`"`, ResolveHookCommand("coral-hook-agentic-state"))
}
