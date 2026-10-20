//go:build !windows

// Not built on Windows: these test the Linux and macOS apply (POSIX file modes, renames over open files, symlinks, a staged record read back by path); the Windows apply is a separate piece (journal, MSI hand-off), #5395.

package apply

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real upgrade from the previous release, on a native Linux runner (tests/release/upgrade_linux.sh sets these from the two release tarballs):
// the previous release is unpacked into a folder of its own, the new release's tarball goes through the real Build (strict unpack), the real ELF
// and machine checks and the real new coral's --version, then the real atomic exchange and the rollback. Skipped without them. Coral is not
// started (a CI runner has no service for it).
func TestRealLinuxUpgradeFromThePreviousRelease(t *testing.T) {
	oldTar, newTar, version := os.Getenv("CORAL_APPLY_OLD_TARBALL"), os.Getenv("CORAL_APPLY_NEW_TARBALL"), os.Getenv("CORAL_APPLY_NEW_VERSION")
	if oldTar == "" || newTar == "" || version == "" {
		t.Skip("set CORAL_APPLY_OLD_TARBALL, CORAL_APPLY_NEW_TARBALL and CORAL_APPLY_NEW_VERSION (tests/release/upgrade_linux.sh)")
	}
	if runtime.GOOS != "linux" {
		t.Skip("a Linux test")
	}
	install := filepath.Join(t.TempDir(), "coral-bin")
	require.NoError(t, os.Mkdir(install, 0o755))
	_, _, err := extractTarGz(oldTar, install, allowedName, DefaultExtractLimits)
	require.NoError(t, err, "the previous release unpacks like the new one")

	l := &Linux{DataDir: t.TempDir(), Arch: runtime.GOARCH, ManagedPrefixes: []string{}}
	oldVersion, err := l.Version(install)
	require.NoError(t, err, "the previous release's own coral answers --version")
	require.Empty(t, l.Unsupported(install), "a folder of Coral's own programs on a volume that exchanges atomically")
	assert.True(t, l.Writable(install))

	newPath, cleanup, err := l.Build(context.Background(), newTar, install, t.TempDir())
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, l.Verify(context.Background(), newPath, version), "the real new release passes the real checks")
	assert.Error(t, l.Verify(context.Background(), newPath, "0.0.1"), "and not as another version")

	require.NoError(t, l.Swap(newPath, install))
	now, err := l.Version(install)
	require.NoError(t, err)
	assert.Equal(t, version, now)
	old, err := l.Version(newPath)
	require.NoError(t, err)
	assert.Equal(t, oldVersion, old, "the old install is kept for the rollback")
	require.NoError(t, l.Swap(newPath, install))
	back, _ := l.Version(install)
	assert.Equal(t, oldVersion, back)
}
