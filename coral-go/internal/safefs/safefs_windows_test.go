//go:build windows

package safefs

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3615: the Windows paths Security flagged as compile-only, run on Windows CI.

// junction makes a directory junction link -> target (no privilege needed, unlike a symlink).
func junction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	require.NoError(t, err, "mklink /J: %s", out)
}

// symlinkOrSkip: a symlink needs Developer Mode or an elevated user.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks need Developer Mode or an elevated user here: %v", err)
	}
}

// A junction to a folder outside the root, as the last name or on the way, is never read,
// written or made inside.
func TestWindowsJunctionOutIsRefused(t *testing.T) {
	root := t.TempDir()
	out, secret := outside(t)
	junction(t, filepath.Join(root, "junc"), out)
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()

	fi, err := r.Lstat("junc")
	require.NoError(t, err)
	assert.False(t, fi.Mode().IsRegular(), "a junction is not a regular file: %v", fi.Mode())
	_, err = r.ReadFile("junc", 1<<20)
	assert.Error(t, err, "read the junction itself")
	assert.Error(t, r.WriteFile("junc", []byte("pwned"), 0o644), "write the junction itself")
	for _, rel := range []string{`junc\secret.txt`, `junc\new.txt`} {
		_, err := r.ReadFile(rel, 1<<20)
		assert.Error(t, err, "read %s", rel)
		assert.Error(t, r.WriteFile(rel, []byte("pwned"), 0o644), "write %s", rel)
	}
	_, err = r.CreateNew(`junc\created.txt`, 0o600)
	assert.Error(t, err)
	assert.Error(t, r.MkdirAll(`junc\made`, 0o755))
	secretIntact(t, out, secret)

	// Within and Resolve follow the junction: filepath.EvalSymlinks alone doesn't (#3615).
	for _, p := range []string{`junc\secret.txt`, `junc\new.txt`, "junc", filepath.Join(root, "junc", "secret.txt")} {
		_, err = Within(root, p)
		assert.ErrorIs(t, err, ErrOutside, "Within sees the junction: %s", p)
	}
	realOut, err := Resolve(out)
	require.NoError(t, err)
	got, err := Resolve(filepath.Join(root, "junc"))
	require.NoError(t, err)
	assert.Equal(t, realOut, got, "Resolve follows the junction")
	// A FILE under the junction too: filepath.EvalSymlinks fails on a junction on the way
	// (ENOTDIR), and the routes took that failure as inside (#3633).
	got, err = Resolve(filepath.Join(root, "junc", "secret.txt"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(realOut, "secret.txt"), got, "Resolve follows the junction to a file")
	_, err = Resolve(filepath.Join(root, "junc", "missing.txt"))
	assert.ErrorIs(t, err, fs.ErrNotExist, "a missing file under it is ErrNotExist")
}

// A junction that stays inside the root is resolved to where it leads, inside: no false
// refusal, and the relative path Within returns is the real one.
func TestWindowsJunctionWithinTheRootResolves(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "real"), 0o755))
	junction(t, filepath.Join(root, "alias"), filepath.Join(root, "real"))
	for p, want := range map[string]string{
		`alias\x.txt`:          `real\x.txt`,
		`alias\new\deeper.txt`: `real\new\deeper.txt`,
		"alias":                "real",
	} {
		rel, err := Within(root, p)
		require.NoError(t, err, p)
		assert.Equal(t, want, rel, p)
	}
	// The root given through a junction works too.
	outer := filepath.Join(t.TempDir(), "root-alias")
	junction(t, outer, root)
	rel, err := Within(outer, `real\x.txt`)
	require.NoError(t, err)
	assert.Equal(t, `real\x.txt`, rel)
}

// A junction that stays inside the root is still not written through as the last name, and a
// folder swapped for a junction into .git\hooks after the checks is refused by the deny check
// (or by os.Root, which refuses a link with an absolute target): the hook never changes.
func TestWindowsJunctionInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	hooks := filepath.Join(root, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	hook := filepath.Join(hooks, "post-checkout")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sub"), 0o755))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	denyGit := func(fresh string) bool {
		for _, c := range strings.Split(filepath.ToSlash(fresh), "/") {
			if strings.EqualFold(c, ".git") {
				return true
			}
		}
		return false
	}
	swapped := false
	testBeforeOpen = func(string) {
		if !swapped {
			swapped = true
			require.NoError(t, os.Remove(filepath.Join(root, "sub")))
			junction(t, filepath.Join(root, "sub"), hooks)
		}
	}
	t.Cleanup(func() { testBeforeOpen = nil })
	assert.Error(t, r.WriteFileUnless(`sub\post-checkout`, []byte("calc.exe"), 0o644, denyGit))
	testBeforeOpen = nil
	assert.Error(t, r.WriteFileUnless(`sub\pre-push`, []byte("calc.exe"), 0o644, denyGit))
	b, _ := os.ReadFile(hook)
	assert.Equal(t, "#!/bin/sh\n", string(b), "the existing hook is unchanged")
	_, err = os.Stat(filepath.Join(hooks, "pre-push"))
	assert.True(t, os.IsNotExist(err), "no new hook")
}

// A symlink as the last name, to a file outside or inside the root, is refused.
func TestWindowsSymlinkAsTheLastNameIsRefused(t *testing.T) {
	root := t.TempDir()
	out, secret := outside(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "inside.txt"), []byte("in"), 0o644))
	symlinkOrSkip(t, secret, filepath.Join(root, "out-link.txt"))
	symlinkOrSkip(t, "inside.txt", filepath.Join(root, "in-link.txt"))
	symlinkOrSkip(t, out, filepath.Join(root, "dir-link"))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	for _, rel := range []string{"out-link.txt", "in-link.txt", `dir-link\secret.txt`} {
		_, err := r.ReadFile(rel, 1<<20)
		assert.Error(t, err, "read %s", rel)
		assert.Error(t, r.WriteFile(rel, []byte("pwned"), 0o644), "write %s", rel)
	}
	for _, rel := range []string{"out-link.txt", "in-link.txt"} {
		_, err := r.ReadFile(rel, 1<<20)
		assert.ErrorIs(t, err, ErrNotRegular, rel)
	}
	b, _ := os.ReadFile(filepath.Join(root, "inside.txt"))
	assert.Equal(t, "in", string(b))
	secretIntact(t, out, secret)
}

// A hard link (another name for a file outside the root) is refused for writing: the count
// comes from the handle (GetFileInformationByHandle), since Windows's FileInfo has none.
func TestWindowsHardLinkIsRefused(t *testing.T) {
	root := t.TempDir()
	out, secret := outside(t)
	require.NoError(t, os.Link(secret, filepath.Join(root, "hard.txt")))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	assert.ErrorIs(t, r.WriteFile("hard.txt", []byte("pwned"), 0o644), ErrLinked)
	_, err = r.CreateNew("hard.txt", 0o600)
	assert.Error(t, err)
	secretIntact(t, out, secret)

	f, err := os.Open(secret)
	require.NoError(t, err)
	defer f.Close()
	fi, err := f.Stat()
	require.NoError(t, err)
	n, ok := LinkCount(f, fi)
	assert.True(t, ok)
	assert.Equal(t, uint64(2), n)

	// A file with one name is still written.
	require.NoError(t, r.WriteFile("plain.txt", []byte("ok"), 0o644))
	require.NoError(t, r.WriteFile("plain.txt", []byte("ok2"), 0o644))
}

// UNC and device paths are refused before anything resolves them: resolving \\host\share makes
// Windows connect to the host over SMB and send the user's credentials. 192.0.2.1 (TEST-NET-1)
// never answers, so a resolve would take the SMB timeout; the refusal is immediate.
func TestWindowsUNCIsRefusedBeforeResolving(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		`\\192.0.2.1\share`, `\\192.0.2.1\share\x.txt`, `//192.0.2.1/share/x.txt`,
		`\\?\UNC\192.0.2.1\share\x.txt`, `\\.\pipe\coral-test`, `\\?\C:\Windows\win.ini`,
	} {
		start := time.Now()
		_, err := Resolve(p)
		assert.ErrorIs(t, err, ErrUNC, "Resolve %s", p)
		_, err = OpenRoot(p)
		assert.ErrorIs(t, err, ErrUNC, "OpenRoot %s", p)
		_, err = Within(p, "x.txt")
		assert.ErrorIs(t, err, ErrUNC, "Within(%s, x)", p)
		_, err = Within(root, p)
		assert.ErrorIs(t, err, ErrUNC, "Within(root, %s)", p)
		assert.Less(t, time.Since(start), 2*time.Second, "%s was refused at once, not resolved", p)
	}
}

// Names os.Root refuses on Windows: a drive, a stream (a colon), a rooted name and a reserved
// device name. None writes anything, inside the root or out.
func TestWindowsNamesThatAreNotLocal(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "x.txt"), []byte("x"), 0o644))
	r, err := OpenRoot(root)
	require.NoError(t, err)
	defer r.Close()
	for _, rel := range []string{`C:x.txt`, `C:\x.txt`, `x.txt:hidden`, `x.txt::$DATA`, `\x.txt`, `NUL`, `CON`, `COM1`, `sub\AUX`, `..\x.txt`} {
		err := r.WriteFile(rel, []byte("pwned"), 0o644)
		assert.Error(t, err, "write %s", rel)
		_, err = r.CreateNew(rel, 0o600)
		assert.Error(t, err, "create %s", rel)
		_, err = r.ReadFile(rel, 1<<20)
		assert.Error(t, err, "read %s", rel)
	}
	b, _ := os.ReadFile(filepath.Join(root, "x.txt"))
	assert.Equal(t, "x", string(b), "the file and its streams are unchanged")
	entries, _ := os.ReadDir(root)
	assert.Len(t, entries, 1, "nothing new in the root")
}
