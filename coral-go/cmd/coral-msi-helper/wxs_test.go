package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// In an upgrade the old package's uninstall runs inside the new install, after the new package closed Coral: its helper finds nothing
// running and removes the restart mark, so Coral is never started again. The close in the execute sequence must skip that uninstall
// (#335 rehearsal, step c: the old 0.0.5's stop at 03:03:04 took the mark the new 0.0.6 had written at 03:03:01).
func TestTheOldPackagesUninstallInAnUpgradeNeverClosesCoral(t *testing.T) {
	wxs, err := os.ReadFile("../../../tools/coral.wxs")
	require.NoError(t, err)
	m := regexp.MustCompile(`(?s)<InstallExecuteSequence>.*?<Custom Action="CloseCoralForInstall"[^>]*Condition="([^"]*)"`).FindSubmatch(wxs)
	require.NotNil(t, m, "CloseCoralForInstall in the execute sequence")
	require.Regexp(t, `\bNOT UPGRADINGPRODUCTCODE\b`, string(m[1]))
	require.Regexp(t, `\bNOT CORALCLOSED\b`, string(m[1]))
	// A plain uninstall (Settings > Apps) has no UPGRADINGPRODUCTCODE and still closes Coral first (QA on #335): nothing else is in the way.
	require.NotContains(t, string(m[1]), "REMOVE")
	require.NotContains(t, string(m[1]), "Installed")
}
