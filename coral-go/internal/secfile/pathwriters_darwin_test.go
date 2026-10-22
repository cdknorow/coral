//go:build darwin

package secfile

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Codex Test's second reproduction: an access list entry that lets everyone add files and delete entries leaves the mode at 0755, so mode bits alone show nothing. The entry is read
// with ls -lde; an allow of a write right to anyone but the user or root is a writer, a deny is not, and an entry that only applies to what is inherited is not about the folder.
func chmodACL(t *testing.T, path, entry string) {
	t.Helper()
	if out, err := exec.Command("/bin/chmod", "+a", entry, path).CombinedOutput(); err != nil {
		t.Skipf("this volume takes no access lists: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("/bin/chmod", "-N", path).Run() }) // a deny delete entry would stop the temp folder's removal
}

func TestPathWritersReadsAccessLists(t *testing.T) {
	base := t.TempDir()
	me, err := user.Current()
	require.NoError(t, err)
	mk := func(name string) (d, node string) {
		d = dir(t, filepath.Join(base, name), 0o755)
		return d, file(t, filepath.Join(d, "node"), 0o755)
	}

	d, node := mk("everyone")
	assert.Empty(t, mustWriters(t, node), "no list: nobody")
	chmodACL(t, d, "everyone allow add_file,delete_child")
	fi, err := os.Stat(d)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm(), "the mode still looks closed")
	who := mustWriters(t, node)
	require.NotEmpty(t, who, "but everyone can replace the node")
	assert.Contains(t, joined(who), "everyone")

	d, node = mk("denyonly")
	chmodACL(t, d, "everyone deny delete")
	assert.Empty(t, mustWriters(t, node), "a deny entry grants nothing")

	d, node = mk("self")
	chmodACL(t, d, "user:"+me.Username+" allow add_file,delete_child")
	assert.Empty(t, mustWriters(t, node), "the user's own entry is not another writer")

	d, node = mk("inheritonly")
	chmodACL(t, d, "everyone allow add_file,delete_child,only_inherit,file_inherit")
	assert.Empty(t, mustWriters(t, node), "an entry for what is inherited is not about the folder")

	d, node = mk("readonly")
	chmodACL(t, d, "everyone allow list,search,readattr")
	assert.Empty(t, mustWriters(t, node), "read rights are not writers")

	_, node = mk("onfile")
	chmodACL(t, node, "everyone allow write,delete")
	assert.NotEmpty(t, mustWriters(t, node), "a list on the node file itself")

	d, node = mk("group")
	chmodACL(t, d, "group:everyone allow add_subdirectory")
	assert.NotEmpty(t, mustWriters(t, node), "a group principal, everyone is one")
}
