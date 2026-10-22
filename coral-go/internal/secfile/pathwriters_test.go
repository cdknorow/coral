//go:build !windows

package secfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PathWriters walks a path hop by hop (every folder, file and link on the way, and each link's target) and names who besides the user and root could change it (#5899 round 3,
// from Codex Test's two reproductions and the Orchestrator's ruling: one check for symlink chains and access lists, here).

// dir makes a folder with the given mode (Mkdir is cut by the umask, so chmod after).
func dir(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func file(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	require.NoError(t, os.Chmod(path, mode))
	return path
}

func mustWriters(t *testing.T, path string) []string {
	t.Helper()
	who, err := PathWriters(path)
	require.NoError(t, err)
	return who
}

func TestPathWritersNamesNobodyForAPathOnlyTheUserCanChange(t *testing.T) {
	root := dir(t, filepath.Join(t.TempDir(), "safe"), 0o755)
	assert.Empty(t, mustWriters(t, file(t, filepath.Join(root, "node"), 0o755)))
}

func TestPathWritersFindsWhatModeBitsAllow(t *testing.T) {
	for name, build := range map[string]func(base string) string{
		"the folder is world-writable": func(b string) string {
			d := dir(t, filepath.Join(b, "d"), 0o777)
			return file(t, filepath.Join(d, "node"), 0o755)
		},
		"the folder is group-writable": func(b string) string {
			d := dir(t, filepath.Join(b, "d"), 0o775)
			return file(t, filepath.Join(d, "node"), 0o755)
		},
		"the file is writable by others": func(b string) string {
			d := dir(t, filepath.Join(b, "d"), 0o755)
			return file(t, filepath.Join(d, "node"), 0o777)
		},
		"a folder above is group-writable": func(b string) string {
			top := dir(t, filepath.Join(b, "top"), 0o775)
			d := dir(t, filepath.Join(top, "d"), 0o755)
			return file(t, filepath.Join(d, "node"), 0o755)
		},
	} {
		assert.NotEmpty(t, mustWriters(t, build(t.TempDir())), name)
	}
}

// Codex Test's first reproduction: safe/node -> bad/bridge/node -> target/node, where bad is 0777 and bad/bridge links to the target. safe and the target are fine; the link in the
// middle of the chain is not (anyone can replace bad/bridge).
func TestPathWritersFollowsEveryLinkOnTheWay(t *testing.T) {
	base := t.TempDir()
	safe := dir(t, filepath.Join(base, "safe"), 0o755)
	target := dir(t, filepath.Join(base, "target"), 0o755)
	file(t, filepath.Join(target, "node"), 0o755)
	bad := dir(t, filepath.Join(base, "bad"), 0o777)
	require.NoError(t, os.Symlink(target, filepath.Join(bad, "bridge")))
	require.NoError(t, os.Symlink(filepath.Join(bad, "bridge", "node"), filepath.Join(safe, "node")))

	who := mustWriters(t, filepath.Join(safe, "node"))
	assert.NotEmpty(t, who, "the link goes through a folder everyone can write")
	assert.Contains(t, joined(who), "bad", "and it says which")
	// the same nodes, with the middle folder closed: nobody
	require.NoError(t, os.Chmod(bad, 0o755))
	assert.Empty(t, mustWriters(t, filepath.Join(safe, "node")))
}

func joined(s []string) string {
	out := ""
	for _, x := range s {
		out += x + "\n"
	}
	return out
}

func TestPathWritersChecksTheFolderOfALinkAndTheFolderItPointsInto(t *testing.T) {
	base := t.TempDir()
	cellar := dir(t, filepath.Join(base, "cellar"), 0o755)
	file(t, filepath.Join(cellar, "node"), 0o755)
	bin := dir(t, filepath.Join(base, "bin"), 0o755)
	require.NoError(t, os.Symlink(filepath.Join(cellar, "node"), filepath.Join(bin, "node"))) // the Homebrew shape
	assert.Empty(t, mustWriters(t, filepath.Join(bin, "node")))

	require.NoError(t, os.Chmod(cellar, 0o777)) // where the link really points
	assert.NotEmpty(t, mustWriters(t, filepath.Join(bin, "node")))
	require.NoError(t, os.Chmod(cellar, 0o755))
	require.NoError(t, os.Chmod(bin, 0o777)) // the folder the link is in: anyone can replace the link
	assert.NotEmpty(t, mustWriters(t, filepath.Join(bin, "node")))
}

func TestPathWritersFollowsRelativeLinksAndDotDot(t *testing.T) {
	base := t.TempDir()
	prefix := dir(t, filepath.Join(base, "prefix"), 0o755)
	pkg := dir(t, filepath.Join(prefix, "lib", "pkg"), 0o755)
	file(t, filepath.Join(pkg, "cli.js"), 0o755)
	bin := dir(t, filepath.Join(prefix, "bin"), 0o755)
	require.NoError(t, os.Symlink(filepath.Join("..", "lib", "pkg", "cli.js"), filepath.Join(bin, "claude")))
	assert.Empty(t, mustWriters(t, filepath.Join(bin, "claude")))
	require.NoError(t, os.Chmod(pkg, 0o777))
	assert.NotEmpty(t, mustWriters(t, filepath.Join(bin, "claude")), "a relative link into a folder anyone can write")
	assert.NotEmpty(t, mustWriters(t, filepath.Join(bin, "..", "bin", "claude")), "and a path with ..")
}

func TestPathWritersErrorsForWhatItCannotExamine(t *testing.T) {
	base := t.TempDir()
	_, err := PathWriters(filepath.Join(base, "missing", "node"))
	assert.Error(t, err, "a missing step")
	require.NoError(t, os.Symlink("b", filepath.Join(base, "a")))
	require.NoError(t, os.Symlink("a", filepath.Join(base, "b")))
	_, err = PathWriters(filepath.Join(base, "a", "node"))
	assert.Error(t, err, "a loop of links")
}

// A sticky folder that is root's (like /tmp) only matters for entries that are not the user's or root's; this user's own sticky folder is no exception.
func TestPathWritersStickyFolders(t *testing.T) {
	base := t.TempDir()
	own := dir(t, filepath.Join(base, "own"), 0o777|os.ModeSticky)
	assert.NotEmpty(t, mustWriters(t, file(t, filepath.Join(own, "node"), 0o755)), "a sticky folder the user owns is still writable by others for what is not the user's: it is not root's")
	if fi, err := os.Stat("/tmp"); err == nil && fi.Mode()&os.ModeSticky != 0 {
		// the test folders live under /tmp (root's, sticky) on most machines, and the safe-path tests above pass through it: a sticky root folder is not itself a finding
		assert.Empty(t, mustWriters(t, file(t, filepath.Join(dir(t, filepath.Join(t.TempDir(), "s"), 0o755), "node"), 0o755)))
	}
}

// A file or folder that belongs to another user can be replaced by that user whatever its mode. One owned by someone else can't be made without root, so the user is changed.
func TestPathWritersForAnotherUser(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root's files are everyone's trusted")
	}
	d := dir(t, filepath.Join(t.TempDir(), "d"), 0o755)
	node := file(t, filepath.Join(d, "node"), 0o755)
	who, err := PathWritersFor(node, os.Geteuid())
	require.NoError(t, err)
	assert.Empty(t, who)
	who, err = PathWritersFor(node, os.Geteuid()+1)
	require.NoError(t, err)
	assert.Contains(t, joined(who), "belongs to another user")
}

// An access list that can't be read is not shown safe: the walk fails, and the caller refuses. One that names a writer is reported with the path it is on.
func TestPathWritersFailsClosedWhenAnAccessListCannotBeRead(t *testing.T) {
	d := dir(t, filepath.Join(t.TempDir(), "d"), 0o755)
	node := file(t, filepath.Join(d, "node"), 0o755)
	old := readACL
	t.Cleanup(func() { readACL = old })
	readACL = func(string, int, string) ([]string, error) { return nil, os.ErrPermission }
	_, err := PathWriters(node)
	assert.ErrorContains(t, err, "access list")
	readACL = func(p string, _ int, _ string) ([]string, error) {
		if strings.HasSuffix(p, "/d") { // the walk names the folder as the system resolves it (macOS: /private/var for /var)
			return []string{"group:staff"}, nil
		}
		return nil, nil
	}
	who := mustWriters(t, node)
	require.Len(t, who, 1)
	assert.Contains(t, who[0], "group:staff")
	assert.Contains(t, who[0], "/d")
}

// A group write bit on the user's own folder passes only when the caller says the group is the user's alone; every other finding stays (#5899, Homebrew's group-writable folders).
func TestPathWritersTrustingGroupLetsOnlyTheUsersAloneGroupPass(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	f := filepath.Join(bin, "tool")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o755))
	require.NoError(t, os.Chmod(bin, 0o775))
	yes := func(uint32) (bool, error) { return true, nil }
	no := func(uint32) (bool, error) { return false, nil }

	w, err := PathWritersTrustingGroup(f, yes)
	require.NoError(t, err)
	assert.Empty(t, w, "the group is the user's alone")

	w, err = PathWritersTrustingGroup(f, no)
	require.NoError(t, err)
	require.NotEmpty(t, w)
	for _, x := range w {
		assert.True(t, x.Group, x.Text)
	}
	assert.Equal(t, evalDir(t, bin), evalDir(t, w[len(w)-1].Path))

	_, err = PathWritersTrustingGroup(f, func(uint32) (bool, error) { return true, errors.New("no directory service") })
	assert.Error(t, err, "a check that fails refuses")

	require.NoError(t, os.Chmod(bin, 0o777))
	w, err = PathWritersTrustingGroup(f, yes)
	require.NoError(t, err)
	require.NotEmpty(t, w, "an other write bit is never forgiven")
	assert.False(t, w[0].Group)

	plain, err := PathWriters(f)
	require.NoError(t, err)
	assert.NotEmpty(t, plain, "PathWriters itself trusts no group")
}

func evalDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return r
}

// The group is forgiven only on the user's OWN entries: for a program run as someone else the same folder's group bit counts, however the caller answers.
func TestAGroupWriteBitOnAnotherUsersFolderIsNeverForgiven(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0o755))
	f := filepath.Join(bin, "tool")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o755))
	require.NoError(t, os.Chmod(bin, 0o775))
	w, err := pathWriters(f, os.Geteuid()+1, func(uint32) (bool, error) { return true, nil }) // the folders are not this uid's
	require.NoError(t, err)
	var group bool
	for _, x := range w {
		group = group || (x.Group && strings.Contains(x.Text, "its group"))
	}
	assert.True(t, group, "%v", w)
}
