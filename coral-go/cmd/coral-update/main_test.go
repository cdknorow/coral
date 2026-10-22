package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/status"
)

func runOut(t *testing.T, env map[string]string, args ...string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	code := run(args, func(k string) string { return env[k] }, &buf)
	var out map[string]any
	if buf.Len() > 0 {
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out), buf.String())
	}
	return code, out
}

func TestTheArgvIsFixed(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"check", "extra"}, {"check", "--url=http://x"}, {"CHECK"}, {"apply", "1.4.130"}, {""}} {
		code, out := runOut(t, nil, args...)
		assert.Equal(t, 2, code, "%v", args)
		assert.Nil(t, out, "%v", args)
	}
}

func TestOnlyAProdBuildWithAVersionChecks(t *testing.T) {
	old := config.Version
	t.Cleanup(func() { config.Version = old })
	config.Version = ""
	code, out := runOut(t, nil, "check")
	assert.Equal(t, 0, code)
	assert.Equal(t, "off", out["result"], "a build with no version")

	config.Version = "1.4.130"
	code, out = runOut(t, map[string]string{"CORAL_AUTO_UPDATE": "off"}, "check")
	assert.Equal(t, 0, code)
	assert.Equal(t, "off", out["result"], "the environment switch")

	if !config.TierChecksForUpdates {
		code, out = runOut(t, nil, "check")
		assert.Equal(t, "off", out["result"], "dev, beta and dropboxers builds never update themselves")
	}
}

func TestApplyIsNotBuiltYet(t *testing.T) {
	old := config.Version
	t.Cleanup(func() { config.Version = old })
	config.Version = "1.4.130"
	if !config.TierChecksForUpdates {
		t.Skip("a build of another tier answers off")
	}
	code, out := runOut(t, nil, "apply")
	assert.Equal(t, 0, code)
	assert.Equal(t, "not_enabled", out["result"])
	assert.Equal(t, "1.4.130", out["installed"])
}

func TestAssetNamesAreTheOnesReleaseYmlUploads(t *testing.T) {
	assert.Equal(t, "Coral.v1.4.126.dmg", assetName("darwin", "arm64", false)("1.4.126"))
	assert.Equal(t, "Coral-1.4.126-x64.msi", assetName("windows", "amd64", false)("1.4.126"))
	assert.Equal(t, "Coral-1.4.126-x64-portable.zip", assetName("windows", "amd64", true)("1.4.126"))
	assert.Equal(t, "coral-linux-amd64-1.4.126.tar.gz", assetName("linux", "amd64", false)("1.4.126"))
	assert.Equal(t, "coral-linux-arm64-1.4.126.tar.gz", assetName("linux", "arm64", true)("1.4.126"))
}

// Homebrew policy (#5280 step 3 B): a managed install is never checked for a package, never staged, never applied; the answer is the same word the status gives, and nothing is read or written.
func TestAnInstallAPackageManagerOwnsIsNeverCheckedOrApplied(t *testing.T) {
	if !config.TierChecksForUpdates {
		t.Skip("a build of another tier answers off")
	}
	oldVersion := config.Version
	t.Cleanup(func() { config.Version = oldVersion })
	config.Version = "1.4.150"
	old := installProbe
	t.Cleanup(func() { installProbe = old })
	installProbe = func() *status.Install {
		return &status.Install{Kind: "homebrew", Unsupported: "homebrew", Writable: true}
	}
	t.Setenv("CORAL_DATA_DIR", t.TempDir())
	oldApply := applyEnabled
	t.Cleanup(func() { applyEnabled = oldApply })
	applyEnabled = true // the switch is not what stops it
	for _, verb := range []string{"check", "apply"} {
		code, out := runOut(t, nil, verb)
		assert.Equal(t, 0, code, verb)
		assert.Equal(t, "unsupported_install", out["result"], verb)
		assert.Equal(t, "homebrew", out["failed_stage"], verb)
	}
	entries, _ := os.ReadDir(os.Getenv("CORAL_DATA_DIR"))
	assert.Empty(t, entries, "no updates folder, no trusted root, nothing written")

}
