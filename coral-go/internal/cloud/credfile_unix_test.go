//go:build linux || darwin

package cloud

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const goodCred = "chb_0123456789abcdefABCDEF_-xyz"

// wellKnown builds a fake /run/coral/bootstrap.d under a temporary root that
// this user owns (it stands in for root in the walk), with the tmpfs check
// answering tmpfs unless told otherwise.
func wellKnown(t *testing.T) (root, dir, file string) {
	t.Helper()
	root = t.TempDir()
	require.NoError(t, os.Chmod(root, 0o755))
	oldRoot, oldUID, oldTmpfs := walkRoot, rootUID, onTmpfs
	walkRoot, rootUID, onTmpfs = root, os.Geteuid(), func(int) bool { return true }
	t.Cleanup(func() { walkRoot, rootUID, onTmpfs = oldRoot, oldUID, oldTmpfs })
	dir = filepath.Join(root, "run", "coral", "bootstrap.d")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o700))
	file = filepath.Join(dir, "credential")
	require.NoError(t, os.WriteFile(file, []byte(goodCred+"\n"), 0o600))
	return root, dir, file
}

func TestWellKnownBootstrapIsReadOnceAndRemoved(t *testing.T) {
	_, _, file := wellKnown(t)
	if !wellKnownSupported {
		_, found, err := ReadWellKnownBootstrap()
		assert.False(t, found, "no auto-discovery on this platform")
		assert.NoError(t, err)
		assert.FileExists(t, file)
	}
	// The walk itself is the same code on every Unix; exercise it everywhere.
	cred, found, err := readWellKnown()
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, goodCred+"\n", string(cred))
	assert.NoFileExists(t, file, "removed right after the read")
	_, found, err = readWellKnown()
	assert.False(t, found)
	assert.NoError(t, err)
}

// Every rule of the walk and the file (Hub design 12.1). A refused file is
// never removed (nothing was used), except where noted.
func TestWellKnownBootstrapRefusesUnsafeLayouts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root, dir, file string)
	}{
		{"dir mode not 0700", func(t *testing.T, _, dir, _ string) { require.NoError(t, os.Chmod(dir, 0o750)) }},
		{"not on tmpfs", func(t *testing.T, _, _, _ string) { onTmpfs = func(int) bool { return false } }},
		{"/run group-writable", func(t *testing.T, root, _, _ string) {
			require.NoError(t, os.Chmod(filepath.Join(root, "run"), 0o775))
		}},
		{"a symlink in the chain", func(t *testing.T, root, _, _ string) {
			real := filepath.Join(root, "run", "real-coral")
			require.NoError(t, os.Rename(filepath.Join(root, "run", "coral"), real))
			require.NoError(t, os.Symlink(real, filepath.Join(root, "run", "coral")))
		}},
		{"file mode 0644", func(t *testing.T, _, _, file string) { require.NoError(t, os.Chmod(file, 0o644)) }},
		{"file mode 0400", func(t *testing.T, _, _, file string) { require.NoError(t, os.Chmod(file, 0o400)) }},
		{"file is a symlink", func(t *testing.T, root, _, file string) {
			other := filepath.Join(root, "elsewhere")
			require.NoError(t, os.WriteFile(other, []byte(goodCred), 0o600))
			require.NoError(t, os.Remove(file))
			require.NoError(t, os.Symlink(other, file))
		}},
		{"a hard link (nlink 2)", func(t *testing.T, _, dir, file string) {
			require.NoError(t, os.Link(file, filepath.Join(dir, "copy")))
		}},
		{"larger than 4096 bytes", func(t *testing.T, _, _, file string) {
			require.NoError(t, os.WriteFile(file, []byte(strings.Repeat("a", 4097)), 0o600))
		}},
		{"not a credential", func(t *testing.T, _, _, file string) {
			require.NoError(t, os.WriteFile(file, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, dir, file := wellKnown(t)
			tc.setup(t, root, dir, file)
			cred, _, err := readWellKnown()
			assert.Error(t, err)
			assert.Nil(t, cred)
			if _, statErr := os.Lstat(file); statErr == nil {
				assert.FileExists(t, file, "a refused file is left in place")
			}
		})
	}
	// A symlinked credential is refused at open (O_NOFOLLOW), before anything is read.
	root, _, linkFile := wellKnown(t)
	other := filepath.Join(root, "elsewhere")
	require.NoError(t, os.WriteFile(other, []byte(goodCred), 0o600))
	require.NoError(t, os.Remove(linkFile))
	require.NoError(t, os.Symlink(other, linkFile))
	_, _, err := readWellKnown()
	assert.ErrorIs(t, err, unix.ELOOP, "refused at open, not after reading")
	assert.FileExists(t, other)

	// Other names in the directory are ignored.
	_, dir, file := wellKnown(t)
	require.NoError(t, os.Remove(file))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "token"), []byte(goodCred), 0o600))
	_, found, err := readWellKnown()
	assert.False(t, found)
	assert.NoError(t, err)
}

func TestRemoveWellKnownBootstrapDoesNotRead(t *testing.T) {
	_, _, file := wellKnown(t)
	require.NoError(t, os.WriteFile(file, []byte("not even a credential"), 0o600))
	found, err := removeWellKnown()
	require.NoError(t, err)
	assert.True(t, found)
	assert.NoFileExists(t, file)
}

func credFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	p := filepath.Join(dir, "ticket")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// --bootstrap-file / --ticket-file: the same file checks; the folder must be
// the user's (or root's) and not writable by others; removed after reading;
// a file that cannot be removed is not used.
func TestReadCredentialFile(t *testing.T) {
	p := credFile(t, goodCred)
	cred, notTmpfs, err := ReadCredentialFile(p)
	require.NoError(t, err)
	assert.Equal(t, goodCred, string(cred))
	_ = notTmpfs // depends on where TMPDIR lives
	assert.NoFileExists(t, p)

	shared := t.TempDir()
	require.NoError(t, os.Chmod(shared, 0o777))
	sp := filepath.Join(shared, "ticket")
	require.NoError(t, os.WriteFile(sp, []byte(goodCred), 0o600))
	_, _, err = ReadCredentialFile(sp)
	assert.ErrorIs(t, err, ErrUnsafeCredentialFile, "a folder others can write to")
	assert.FileExists(t, sp)

	if os.Geteuid() != 0 {
		ro := credFile(t, goodCred)
		require.NoError(t, os.Chmod(filepath.Dir(ro), 0o500))
		t.Cleanup(func() { _ = os.Chmod(filepath.Dir(ro), 0o700) })
		_, _, err = ReadCredentialFile(ro)
		assert.ErrorIs(t, err, ErrUnsafeCredentialFile, "it cannot be removed, so it is not used")
		assert.FileExists(t, ro)
	}

	missing := filepath.Join(t.TempDir(), "nope")
	_, _, err = ReadCredentialFile(missing)
	assert.True(t, errors.Is(err, os.ErrNotExist), "%v", err)
}

func TestReadProofFile(t *testing.T) {
	p := credFile(t, `{"kind":"gcp","token":"x"}`)
	data, err := ReadProofFile(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"gcp","token":"x"}`, string(data))
	assert.FileExists(t, p, "a proof is not removed")
	big := credFile(t, strings.Repeat("a", 16<<10+1))
	_, err = ReadProofFile(big)
	assert.ErrorIs(t, err, ErrUnsafeCredentialFile)
}
