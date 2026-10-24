package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readWxs(t *testing.T) string {
	wxs, err := os.ReadFile("../../../tools/coral.wxs")
	require.NoError(t, err)
	return string(wxs)
}

// condition is the Condition of action in the execute sequence, and its scheduling attributes.
func scheduled(t *testing.T, wxs, action string) (cond, attrs string) {
	m := regexp.MustCompile(`(?s)<InstallExecuteSequence>.*?<Custom Action="` + action + `"([^>]*)Condition="([^"]*)"`).FindStringSubmatch(wxs)
	require.NotNil(t, m, "%s in the execute sequence", action)
	return m[2], m[1]
}

// In an upgrade the old package's uninstall runs inside the new install, after the new package closed Coral: its helper would find nothing
// running. Neither close runs there (#335 rehearsal, step c: the old 0.0.5's stop at 03:03:04 took the mark the new 0.0.6 had written at
// 03:03:01). CORALCLOSED skips both, to rehearse Restart Manager alone.
func TestTheOldPackagesUninstallInAnUpgradeNeverClosesCoral(t *testing.T) {
	wxs := readWxs(t)
	for _, action := range []string{"CloseCoralForInstall", "CloseCoralForUninstall"} {
		cond, _ := scheduled(t, wxs, action)
		assert.Regexp(t, `\bNOT UPGRADINGPRODUCTCODE\b`, cond, action)
		assert.Regexp(t, `\bNOT CORALCLOSED\b`, cond, action)
	}
}

// Coral closes only after the person allowed the install (the Orchestrator on #335): a declined UAC prompt leaves Coral running.
//   - Nothing closes Coral in the UI sequence, which runs before consent.
//   - An install or upgrade asks when the execute sequence starts: its close is immediate, before costing (so before the files-in-use check).
//   - An uninstall (REMOVE=ALL, msiexec /x and Settings > Apps alike) asks only at InstallInitialize: its close is deferred, after it, and
//     impersonated (the person installing, never SYSTEM).
func TestCoralClosesOnlyAfterConsent(t *testing.T) {
	wxs := readWxs(t)
	ui := regexp.MustCompile(`(?s)<InstallUISequence>.*?</InstallUISequence>`).FindString(wxs)
	assert.NotContains(t, ui, "CloseCoral", "a close in the UI sequence runs before consent")

	cond, attrs := scheduled(t, wxs, "CloseCoralForInstall")
	assert.Contains(t, attrs, `Before="CostInitialize"`)
	assert.Regexp(t, `\bNOT REMOVE~=&quot;ALL&quot;`, cond)

	cond, attrs = scheduled(t, wxs, "CloseCoralForUninstall")
	assert.Contains(t, attrs, `After="InstallInitialize"`)
	assert.Regexp(t, `(^|[^T] )REMOVE~=&quot;ALL&quot;`, cond)
	assert.NotRegexp(t, `NOT REMOVE`, cond)

	def := regexp.MustCompile(`<CustomAction Id="CloseCoralForUninstall"[^>]*/>`).FindString(wxs)
	require.NotEmpty(t, def)
	assert.Contains(t, def, `Execute="deferred"`)
	assert.Contains(t, def, `Impersonate="yes"`)
	assert.NotRegexp(t, `CustomAction Id="CloseCoralForInstall"[^>]*Impersonate="no"`, wxs)
}
