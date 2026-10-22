package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
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

// #162: Windows has its own off switch, whatever macOS and Linux have: the MSI apply stays off until the coral-go-team has reviewed it.
func TestWindowsApplyHasItsOwnOffSwitch(t *testing.T) {
	assert.False(t, applyOn("windows", true, true, applyEnabledWindows, windowsApplyBuilt), "Mac and Linux on do not turn Windows on")
	assert.True(t, applyOn("windows", false, false, true, windowsApplyBuilt), "its own switch does, now that its apply is built")
	assert.False(t, applyEnabledWindows, "off in this build")
	assert.False(t, applyOn("windows", applyEnabledDarwin, applyEnabledLinux, applyEnabledWindows, windowsApplyBuilt), "so the Windows apply is off")
}

// #81 N1: only a Windows MSI install keeps the rollback package.
func TestOnlyAWindowsMSIInstallKeepsTheRollbackPackage(t *testing.T) {
	assert.True(t, keepsRollback("windows", false))
	assert.False(t, keepsRollback("windows", true), "portable")
	assert.False(t, keepsRollback("darwin", false))
	assert.False(t, keepsRollback("linux", false))
}
