package safefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithin(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o755))
	for p, want := range map[string]string{
		"a/b":                    filepath.Join("a", "b"),
		"a/b/new.txt":            filepath.Join("a", "b", "new.txt"),
		"a/new/deeper/file.txt":  filepath.Join("a", "new", "deeper", "file.txt"),
		".":                      ".",
		filepath.Join(root, "a"): "a",
	} {
		got, err := Within(root, p)
		require.NoError(t, err, p)
		assert.Equal(t, want, got, p)
	}
	// An absolute path elsewhere (on Windows, /etc/passwd is rooted on the current drive, not
	// absolute, so another temp folder stands in for it on every OS).
	for _, p := range []string{"../x", "a/../../x", filepath.Join(t.TempDir(), "passwd"), "a/new/../../../x"} {
		_, err := Within(root, p)
		assert.ErrorIs(t, err, ErrOutside, p)
	}
	for _, p := range []string{`\\host\share\x`, "//host/share/x"} {
		_, err := Within(root, p)
		assert.ErrorIs(t, err, ErrUNC, p)
	}
	// (d) a root that no longer exists is an error, never "allow everything".
	gone := filepath.Join(t.TempDir(), "gone")
	_, err := Within(gone, "x")
	assert.ErrorIs(t, err, ErrRootGone)
	_, err = Within("", "x")
	assert.ErrorIs(t, err, ErrRootGone)
	_, err = OpenRoot(gone)
	assert.ErrorIs(t, err, ErrRootGone)
}

func TestIDs(t *testing.T) {
	for id, want := range map[string]bool{
		"0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11":   true,
		"0B7F0F4E-3C1B-4C55-9B0E-0F6B1B2F1A11":   false, // canonical is lower case
		"{0b7f0f4e-3c1b-4c55-9b0e-0f6b1b2f1a11}": false,
		"0b7f0f4e3c1b4c559b0e0f6b1b2f1a11":       false,
		"../../0b7f0f4e-3c1b-4c55-9b0e-0f6b1":    false,
		"":                                       false,
	} {
		assert.Equal(t, want, IsCanonicalUUID(id), id)
	}
	for id, want := range map[string]bool{
		"abc": true, "rollout_2026-x": true, "A1": true,
		"-x": false, "--dangerously-skip-permissions": false, "_x": false, ".": false, "..": false,
		"a/b": false, `a\b`: false, "a.b": false, "a b": false, "": false, strings.Repeat("a", 129): false,
		"é": false,
	} {
		assert.Equal(t, want, IsSafeID(id), id)
	}
}

func TestRootReadWrite(t *testing.T) {
	root := t.TempDir()
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	require.NoError(t, r.MkdirAll(filepath.Join("d", "e"), 0o755))
	require.NoError(t, r.WriteFile(filepath.Join("d", "e", "f.txt"), []byte("one"), 0o644))
	require.NoError(t, r.WriteFile(filepath.Join("d", "e", "f.txt"), []byte("2"), 0o644), "an existing regular file is replaced")
	data, err := r.ReadFile(filepath.Join("d", "e", "f.txt"), 10)
	require.NoError(t, err)
	assert.Equal(t, "2", string(data), "truncated, not overwritten in place")
	_, err = r.ReadFile(filepath.Join("d", "e", "f.txt"), 0)
	assert.ErrorIs(t, err, ErrTooLarge)

	f, err := r.CreateNew("new.txt", 0o600)
	require.NoError(t, err)
	f.Close()
	_, err = r.CreateNew("new.txt", 0o600)
	assert.Error(t, err, "CreateNew refuses an existing name")
	_, err = r.ReadFile("d", 10)
	assert.ErrorIs(t, err, ErrNotRegular, "a folder isn't a file")
}

// outside makes a folder outside the root with a file whose content must not change.
func outside(t *testing.T) (dir, secret string) {
	t.Helper()
	dir = t.TempDir()
	secret = filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("SECRET"), 0o600))
	return dir, secret
}

func secretIntact(t *testing.T, dir, secret string) {
	t.Helper()
	b, _ := os.ReadFile(secret)
	assert.Equal(t, "SECRET", string(b), "the outside file is unchanged")
	entries, _ := os.ReadDir(dir)
	assert.Len(t, entries, 1, "nothing new outside")
}
