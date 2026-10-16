//go:build windows

package safefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// #3671: the Windows path forms of the #3669 audit's table, run on Windows CI. Each case asserts
// the security property (refused, or the same file as its plain name, never outside the root),
// not more of Windows' behaviour than that needs.

// Network and device-namespace forms are refused before anything resolves them.
func TestWindowsUNCAndDeviceFormsAreRefused(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		`\\host\share\x`, `//host/share/x`, `\/host\share`, `/\host/share`,
		`\\?\UNC\host\share\x`, `\\?\` + root, `\\.\pipe\x`, `\??\UNC\host\share`,
	} {
		_, err := Resolve(p)
		assert.ErrorIs(t, err, ErrUNC, "Resolve %s", p)
		_, err = Within(root, p)
		assert.ErrorIs(t, err, ErrUNC, "Within %s", p)
		_, err = OpenRoot(p)
		assert.ErrorIs(t, err, ErrUNC, "OpenRoot %s", p)
	}
}

// An 8.3 short name resolves to the long name (the final path name is normalised), so a
// decision on the resolved form sees the real name.
func TestWindowsShortNamesResolveToTheLongName(t *testing.T) {
	root := t.TempDir()
	long := filepath.Join(root, "LongFolderName12345", "secret-credentials.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(long), 0o700))
	require.NoError(t, os.WriteFile(long, []byte("x"), 0o600))
	lp, err := windows.UTF16PtrFromString(long)
	require.NoError(t, err)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(lp, &buf[0], uint32(len(buf)))
	require.NoError(t, err)
	short := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(short, long) {
		t.Skipf("8.3 names are off on this volume (%s)", short)
	}
	realLong, err := Resolve(long)
	require.NoError(t, err)
	realShort, err := Resolve(short)
	require.NoError(t, err)
	assert.Equal(t, realLong, realShort, "short %s", short)
	rel, err := Within(root, short)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("LongFolderName12345", "secret-credentials.txt"), rel)
}

// Windows drops a trailing dot or space when it opens a name, so "id_rsa." is id_rsa: the
// resolved form is the plain name (what the credential denylist runs on), inside the root.
func TestWindowsTrailingDotsAndSpacesResolveToThePlainName(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "id_rsa")
	require.NoError(t, os.WriteFile(plain, []byte("x"), 0o600))
	want, err := Resolve(plain)
	require.NoError(t, err)
	for _, name := range []string{"id_rsa.", "id_rsa ", "id_rsa. .", "id_rsa..."} {
		got, err := Resolve(filepath.Join(root, name))
		if assert.NoError(t, err, "%q", name) {
			assert.Equal(t, want, got, "%q", name)
		}
		rel, err := Within(root, name)
		if err == nil {
			assert.False(t, strings.HasPrefix(rel, ".."), "%q: %s", name, rel)
		}
	}
	assert.ErrorIs(t, CheckName("id_rsa."), ErrReservedName, "Coral never makes such a name itself")
}

// An alternate data stream (file.txt:stream) and a device name are never opened or created
// through a root.
func TestWindowsStreamsAndDeviceNamesAreRefusedThroughARoot(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600))
	r, err := OpenRoot(dir)
	require.NoError(t, err)
	defer r.Close()
	if f, _, err := r.Open("f.txt:stream"); err == nil {
		f.Close()
		t.Errorf("a stream was opened through the root")
	}
	if f, err := r.CreateNew("g.txt:stream", 0o600); err == nil {
		f.Close()
		t.Errorf("a stream was created through the root")
	}
	for _, name := range []string{"nul.json", "CON", "com1.md"} {
		if f, err := r.CreateNew(name, 0o600); err == nil {
			f.Close()
			t.Errorf("%s was created through the root", name)
		}
		assert.ErrorIs(t, CheckName(name), ErrReservedName, name)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "only f.txt")
}
