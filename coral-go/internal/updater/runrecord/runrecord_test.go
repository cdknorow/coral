package runrecord

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteKeepsTheCommandLineOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Write(dir, []string{"/opt/coral/coral", "--port", "8420"}, "/srv"))
	b, err := os.ReadFile(filepath.Join(dir, "updates", File))
	require.NoError(t, err)
	var r Record
	require.NoError(t, json.Unmarshal(b, &r))
	assert.Equal(t, Record{Argv: []string{"/opt/coral/coral", "--port", "8420"}, Cwd: "/srv"}, r)
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "updates", File))
		assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
		di, _ := os.Stat(filepath.Join(dir, "updates"))
		assert.Equal(t, os.FileMode(0o700), di.Mode().Perm())
	}
	// Written again: replaced, nothing left beside it.
	require.NoError(t, Write(dir, []string{"/opt/coral/coral"}, "/"))
	left, _ := os.ReadDir(filepath.Join(dir, "updates"))
	assert.Len(t, left, 1)
	assert.Error(t, Write(dir, nil, "/"))
	assert.Error(t, Write(dir, []string{strings.Repeat("a", 70<<10)}, "/"))
}
