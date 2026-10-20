//go:build darwin

package apply

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const infoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>coral-tray</string>
<key>CFBundleShortVersionString</key><string>%s</string>
</dict></plist>
`

func makeApp(t *testing.T, dir, version string) string {
	t.Helper()
	app := filepath.Join(dir, "Coral.app")
	require.NoError(t, os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(strings.Replace(infoPlist, "%s", version, 1)), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(app, "Contents", "MacOS", "coral-tray"), []byte("#!/bin/sh\n"), 0o755))
	return app
}

func mounted(t *testing.T, mnt string) bool {
	out, err := exec.Command(hdiutil, "info").CombinedOutput()
	require.NoError(t, err)
	return strings.Contains(string(out), mnt)
}

// The real tools on this Mac: an image is mounted read-only, the app is copied out beside the install with ditto, and the image is detached; the
// copy's version is read with PlistBuddy; the unsigned copy is refused by the real codesign; the real atomic swap puts it in place and back.
func TestMacBuildAndSwapWithTheRealTools(t *testing.T) {
	if os.Getenv("CORAL_APPLY_REAL_TOOLS") != "1" {
		t.Skip("set CORAL_APPLY_REAL_TOOLS=1: it makes and mounts a disk image (about 20 s)")
	}
	if _, err := os.Stat(hdiutil); err != nil {
		t.Skip("no hdiutil")
	}
	src := t.TempDir()
	makeApp(t, src, "1.4.130")
	dmg := filepath.Join(t.TempDir(), "Coral.v1.4.130.dmg")
	if out, err := exec.Command(hdiutil, "create", "-quiet", "-srcfolder", src, "-format", "UDZO", "-volname", "Coral", dmg).CombinedOutput(); err != nil {
		t.Skipf("cannot make an image here: %v %s", err, out)
	}
	apps := t.TempDir()
	install := makeApp(t, apps, "1.4.125")
	work := t.TempDir()
	m := &Mac{Run: NewRunner(), TeamIDs: []string{"ABCDE12345"}}
	require.Empty(t, m.Unsupported(install), "this volume swaps atomically and it is not a Homebrew install")
	assert.True(t, m.Writable(install))

	newPath, cleanup, err := m.Build(context.Background(), dmg, install, work)
	if err != nil && strings.Contains(err.Error(), "mounting the package") {
		t.Skipf("cannot mount an image here: %v", err)
	}
	require.NoError(t, err)
	defer cleanup()
	assert.False(t, mounted(t, filepath.Join(work, "mnt")), "the image is detached once the app is copied")
	assert.Equal(t, filepath.Join(apps, ".Coral.app.new"), newPath)
	v, err := m.Version(newPath)
	require.NoError(t, err)
	assert.Equal(t, "1.4.130", v)
	assert.Error(t, m.Verify(context.Background(), newPath, "1.4.130"), "an unsigned copy is refused by the real codesign")

	require.NoError(t, m.Swap(newPath, install))
	now, _ := m.Version(install)
	old, _ := m.Version(newPath)
	assert.Equal(t, "1.4.130", now)
	assert.Equal(t, "1.4.125", old, "the old install is kept for the rollback")
	require.NoError(t, m.Swap(newPath, install))
	back, _ := m.Version(install)
	assert.Equal(t, "1.4.125", back)
}

// The real upgrade from the previous release, in CI (tests/release/upgrade_macos.sh sets these from the two signed releases): the previous
// release's app is installed in a temp folder, the new release's image goes through Build and the REAL signature, signer, Gatekeeper and
// notarization checks, then the swap and the rollback. Skipped without them. The app is not started (a CI runner has no session for it).
func TestRealUpgradeFromThePreviousRelease(t *testing.T) {
	oldApp, newDMG, team, version := os.Getenv("CORAL_APPLY_OLD_APP"), os.Getenv("CORAL_APPLY_NEW_DMG"), os.Getenv("CORAL_APPLY_TEAM_ID"), os.Getenv("CORAL_APPLY_NEW_VERSION")
	if oldApp == "" || newDMG == "" || team == "" || version == "" {
		t.Skip("set CORAL_APPLY_OLD_APP, CORAL_APPLY_NEW_DMG, CORAL_APPLY_TEAM_ID and CORAL_APPLY_NEW_VERSION (tests/release/upgrade_macos.sh)")
	}
	apps := t.TempDir()
	install := filepath.Join(apps, "Coral.app")
	out, err := exec.Command(ditto, "--noqtn", oldApp, install).CombinedOutput()
	require.NoError(t, err, string(out))
	m := &Mac{Run: NewRunner(), TeamIDs: []string{team}}
	require.Empty(t, m.Unsupported(install))
	before, err := m.Version(install)
	require.NoError(t, err)

	newPath, cleanup, err := m.Build(context.Background(), newDMG, install, t.TempDir())
	require.NoError(t, err)
	defer cleanup()
	require.NoError(t, m.Verify(context.Background(), newPath, version), "the real checks accept the real release")
	require.NoError(t, m.Swap(newPath, install))
	now, _ := m.Version(install)
	assert.Equal(t, version, now)
	require.NoError(t, m.Swap(newPath, install))
	back, _ := m.Version(install)
	assert.Equal(t, before, back)

	// The wrong signer is refused by the same real checks.
	wrong := &Mac{Run: NewRunner(), TeamIDs: []string{"ZZZZZ99999"}}
	assert.Error(t, wrong.Verify(context.Background(), newPath, version))
}
