package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

// #3598 (weak site W7): nothing outside the attach roots is resolved or opened. Resolving a UNC
// path makes Windows connect to that host over SMB (leaking the user's NTLM hash), and resolving
// /net/<host>/... mounts it on macOS, so a UNC or device path is refused outright and the path
// as written must be inside a root before any symlink is followed.
func TestUNCOrDevicePath(t *testing.T) {
	for _, p := range []string{`\\host\share\x.png`, `//host/share/x.png`, `\\?\C:\x.png`, `\\.\pipe\x`, `//?/c:/x.png`} {
		assert.True(t, uncOrDevicePath(p), p)
	}
	for _, p := range []string{"/repo/app/x.png", "app/x.png", "./x.png", "/net/host/x.png", "C:x.png"} {
		assert.False(t, uncOrDevicePath(p), p)
	}
}

func TestAttachLoadRefusesBeforeResolving(t *testing.T) {
	rig := newAttachRig(t)
	img := pngBytes(t, 4, 4)
	rig.write("ok.png", img)

	// A path outside every root is refused as outside_roots before it is resolved: a symlink
	// loop there would otherwise fail to resolve and report not_found.
	outside := t.TempDir()
	loop := filepath.Join(outside, "loop.png")
	linktest.Symlink(t, loop, loop)
	_, _, _, err := rig.a.load(rig.wd, rig.git, loop, true, 1<<20)
	assert.EqualError(t, err, "outside_roots", "an outside path isn't resolved")

	for _, target := range []string{"//host/share/x.png", "/net/host/share/x.png"} {
		_, _, _, err := rig.a.load(rig.wd, rig.git, target, true, 1<<20)
		assert.EqualError(t, err, "outside_roots", target)
	}

	// An outside path that is a symlink INTO the repository is refused too: the path as written
	// must be inside a root.
	into := filepath.Join(outside, "into.png")
	linktest.Symlink(t, filepath.Join(rig.wd, "ok.png"), into)
	_, _, _, err = rig.a.load(rig.wd, rig.git, into, true, 1<<20)
	assert.EqualError(t, err, "outside_roots")

	// Still refused, as before: a symlink inside the repository that points outside it.
	secret := filepath.Join(outside, "secret.png")
	require.NoError(t, os.WriteFile(secret, img, 0o644))
	linktest.Symlink(t, secret, filepath.Join(rig.wd, "link.png"))
	_, _, _, err = rig.a.load(rig.wd, rig.git, "link.png", true, 1<<20)
	assert.Error(t, err)

	// A normal file, relative or absolute, still loads.
	for _, target := range []string{"ok.png", filepath.Join(rig.wd, "ok.png")} {
		_, data, _, err := rig.a.load(rig.wd, rig.git, target, true, 1<<20)
		require.NoError(t, err, target)
		assert.NotEmpty(t, data, target)
	}
}
