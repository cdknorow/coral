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

func TestAFlagThatCarriesASecretIsNotRecorded(t *testing.T) {
	for _, argv := range [][]string{
		{"/opt/coral/coral", "--token", "abc"}, {"/opt/coral/coral", "--api-key=abc"}, {"/opt/coral/coral", "--Password=x"}, {"/opt/coral/coral", "-secret"},
		{"/opt/coral/coral", "--cloud-private-key", "k"}, {"/opt/coral/coral", "--port", "1", "--client_secret=z"},
	} {
		dir := t.TempDir()
		assert.ErrorIs(t, Write(dir, argv, "/"), ErrSecretFlag, "%v", argv)
		_, err := os.Stat(filepath.Join(dir, "updates", File))
		assert.ErrorIs(t, err, os.ErrNotExist, "nothing was written")
	}
	// Ordinary flags, and a value that merely looks like one, are fine.
	require.NoError(t, Write(t.TempDir(), []string{"/opt/coral/coral", "--host", "0.0.0.0", "--port", "8420", "--cloud-url", "off", "--home", "/data/token-store"}, "/"))
}
