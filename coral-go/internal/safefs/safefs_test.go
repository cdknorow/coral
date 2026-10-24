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

// #3671 (#3669 finding 1): every network and device-namespace form is refused, whichever
// separators it uses; local paths are not.
func TestRefuseRemoteForms(t *testing.T) {
	for _, p := range []string{
		`\\host\share`, `//host/share`, `\/host\share`, `/\host/share`, `\\host`,
		`\\?\C:\x`, `\\?\UNC\host\share`, `//?/UNC/host/share`, `\\.\pipe\x`, `\\.\PhysicalDrive0`,
		`\??\UNC\host\share`, `/??/C:/x`,
	} {
		assert.ErrorIs(t, RefuseRemote(p), ErrUNC, p)
		assert.True(t, IsUNC(p), p)
	}
	for _, p := range []string{`/home/u/proj`, `C:\Users\u\proj`, `relative\dir`, `./x`, `~/x`, `/`, `/?/x`, `?`, ``} {
		assert.NoError(t, RefuseRemote(p), p)
	}
}

// #3671 (#3669 finding 4): Windows device names, in any case, with or without an extension,
// and names ending in a dot or a space are refused; names that only start like one are not.
func TestCheckNameRefusesWindowsDeviceNames(t *testing.T) {
	for _, n := range []string{
		"CON", "con", "Nul", "PRN", "aux", "COM1", "com9", "COM0", "LPT1", "lpt9",
		"nul.json", "CON.md", "com1.tar.gz", "NUL .json", "conin$", "CONOUT$.txt",
		"COM\u00b9", "lpt\u00b3.json",
		"theme.", "theme ", "a..", "", ".",
	} {
		assert.ErrorIs(t, CheckName(n), ErrReservedName, "%q", n)
	}
	for _, n := range []string{"console", "CONx", "nully", "com10", "COM", "LPT", "lpt1x", "aux-team", "my theme", "x.json", "_con", "com1_"} {
		assert.NoError(t, CheckName(n), "%q", n)
	}
}

// #7069: on Windows CheckName also refuses the characters its file systems do not allow in a name; elsewhere they are ordinary.
func TestCheckNameRefusesTheCharactersWindowsDoesNotAllowOnlyOnWindows(t *testing.T) {
	old := windowsNameCharsRefused
	t.Cleanup(func() { windowsNameCharsRefused = old })
	bad := []string{"what?", "a<b", "a>b", `a"b`, "a|b", "a*b", "?", "wip?1"}
	windowsNameCharsRefused = true
	for _, n := range bad {
		assert.ErrorIs(t, CheckName(n), ErrReservedName, "%q", n)
	}
	for _, n := range []string{"my team", "wip~1", "a-b_c.d", "RUNNER~1"} {
		assert.NoError(t, CheckName(n), "%q", n)
	}
	windowsNameCharsRefused = false
	for _, n := range bad {
		assert.NoError(t, CheckName(n), "%q: an ordinary name off Windows", n)
	}
}
