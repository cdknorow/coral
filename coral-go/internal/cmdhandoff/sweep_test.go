package cmdhandoff

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSweepRemovesOnlyOldExactlyNamedOwnScripts(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	touch := func(name string, age time.Time) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		require.NoError(t, os.Chtimes(p, age, age))
		return p
	}
	oldScript := touch("coral-command-123456.sh", old)
	oldPS := touch("coral-command-98765.ps1", old)
	youngScript := touch("coral-command-555.sh", now.Add(-time.Hour))
	// Names that are not exactly a handoff script stay, however old.
	notExact := []string{
		touch("coral-command-abc.sh", old), touch("coral-command-.sh", old), touch("coral-command-12.sh.bak", old),
		touch("xcoral-command-12.sh", old), touch("coral-command-12.txt", old), touch("coral-command-1 2.sh", old), touch("notes.sh", old),
	}
	// A folder with the right name, and a link to a file, are not regular files.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "coral-command-777.sh"), 0o700))
	target := touch("target.txt", old)
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(target, filepath.Join(dir, "coral-command-888.sh")))
	}

	assert.Equal(t, 2, sweepDir(dir, now, MaxAge))
	for _, p := range []string{oldScript, oldPS} {
		_, err := os.Lstat(p)
		assert.True(t, os.IsNotExist(err), "%s removed", filepath.Base(p))
	}
	for _, p := range append([]string{youngScript, target}, notExact...) {
		_, err := os.Lstat(p)
		assert.NoError(t, err, "%s stays", filepath.Base(p))
	}
	_, err := os.Stat(filepath.Join(dir, "coral-command-777.sh"))
	assert.NoError(t, err, "a folder stays")
	if runtime.GOOS != "windows" {
		_, err = os.Lstat(filepath.Join(dir, "coral-command-888.sh"))
		assert.NoError(t, err, "a link stays, and its target too")
	}
	assert.Equal(t, 0, sweepDir(filepath.Join(dir, "no-such-dir"), now, MaxAge))
}
