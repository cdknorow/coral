//go:build windows

package routes

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3615: auto-attach's weak sites W7, W8 and W9 (#3598, #3601), run on Windows.

// mklinkJ makes a directory junction link -> target (no privilege needed, unlike a symlink).
func mklinkJ(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	require.NoError(t, err, "mklink /J: %s", out)
}

// W7: a UNC or device path is refused before anything resolves it (resolving \\host\share
// connects to the host over SMB with the user's credentials). 192.0.2.1 (TEST-NET-1) never
// answers, so a resolve would take the SMB timeout; the refusal is immediate. A path rooted on
// the current drive (\x.png) or relative to a drive's current folder (C:x.png) isn't relative
// to the working directory and is refused too.
func TestAutoAttachWindowsRefusesUNCAndDriveRelative(t *testing.T) {
	rig := newAttachRig(t)
	rig.write("ok.png", pngBytes(t, 4, 4))
	vol := filepath.VolumeName(rig.wd)
	for _, target := range []string{
		`\\192.0.2.1\share\x.png`, `//192.0.2.1/share/x.png`, `\\?\UNC\192.0.2.1\share\x.png`,
		`\\.\pipe\x.png`, `\\?\` + filepath.Join(rig.wd, "ok.png"),
		`\ok.png`, `/ok.png`, vol + `ok.png`,
	} {
		start := time.Now()
		_, _, _, err := rig.a.load(rig.wd, rig.git, target, true, 1<<20)
		assert.EqualError(t, err, "outside_roots", target)
		unchanged(t, rig, "![x]("+target+")", target)
		assert.Less(t, time.Since(start), 2*time.Second, "%s was refused at once, not resolved", target)
	}
	// The same file by a plain path, relative (either separator) or absolute, still attaches.
	for _, target := range []string{"ok.png", `.\ok.png`, filepath.Join(rig.wd, "ok.png")} {
		_, data, _, err := rig.a.load(rig.wd, rig.git, target, true, 1<<20)
		require.NoError(t, err, target)
		assert.NotEmpty(t, data, target)
	}
	assert.Regexp(t, coralLinkRe, rig.post(`![x](sub\..\ok.png)`))
}

// W8: a junction to a folder outside the roots is never read through, whether it was there at
// the checks or swapped in for a parent folder between the checks and the open (the open goes
// through a root held on the attach root, which refuses it).
func TestAutoAttachWindowsJunctions(t *testing.T) {
	t.Cleanup(func() { attachTestHook = nil })
	rig := newAttachRig(t)
	outside := realPath(t.TempDir())
	rig.write(filepath.Join(outside, "shot.png"), pngBytes(t, 6, 6))

	mklinkJ(t, filepath.Join(rig.wd, "junc"), outside)
	unchanged(t, rig, `![x](junc\shot.png)`, "a junction out of the roots")
	unchanged(t, rig, "![x](junc/shot.png)", "a junction out of the roots, forward slashes")

	rig.write(`sub\shot.png`, pngBytes(t, 4, 4))
	readOutside := false
	attachTestHook = func(stage, path string) {
		if stage == "before_open" {
			require.NoError(t, os.Rename(filepath.Join(rig.wd, "sub"), filepath.Join(rig.wd, "sub-moved")))
			mklinkJ(t, filepath.Join(rig.wd, "sub"), outside)
		}
		if stage == "after_stat" {
			readOutside = true
		}
	}
	unchanged(t, rig, `![x](sub\shot.png)`, "a parent folder swapped for a junction")
	assert.False(t, readOutside, "the swapped folder's file was opened")
	attachTestHook = nil

	// The file itself swapped for a symlink before the open (needs Developer Mode or an
	// elevated user to make one).
	rig.write("own.png", pngBytes(t, 4, 4))
	attachTestHook = func(stage, path string) {
		if stage == "before_open" {
			require.NoError(t, os.Remove(path))
			if err := os.Symlink(filepath.Join(outside, "shot.png"), path); err != nil {
				t.Skipf("symlinks need Developer Mode or an elevated user here: %v", err)
			}
		}
	}
	unchanged(t, rig, "![x](own.png)", "a symlink swapped in before the open")
}

// W9: a hard link is refused, its count read from the handle (GetFileInformationByHandle),
// since Windows's FileInfo has none; the original, now with two names, too.
func TestAutoAttachWindowsHardLinks(t *testing.T) {
	rig := newAttachRig(t)
	src := rig.write("orig.png", pngBytes(t, 4, 4))
	require.NoError(t, os.Link(src, filepath.Join(rig.wd, "hard.png")))
	_, _, _, err := rig.a.load(rig.wd, rig.git, "hard.png", true, 1<<20)
	assert.EqualError(t, err, "hardlink")
	unchanged(t, rig, "![x](hard.png)", "a hard link")
	unchanged(t, rig, "![x](orig.png)", "the original of a hard link (two links)")
	rig.write("single.png", pngBytes(t, 4, 4))
	assert.Regexp(t, coralLinkRe, rig.post("![x](single.png)"), "a file with one name attaches")
}
