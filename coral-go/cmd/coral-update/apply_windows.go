//go:build windows

package main

import (
	"context"
	"runtime"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/verify"
)

// runApplyWindows is the Windows apply: the staged MSI, verified again, installed as a major upgrade by an unelevated msiexec (#162). The
// rollback package (the installed version's verified MSI, staged by the check) is wired when the stage step that keeps it lands; until then
// a new version that doesn't come up stays installed and the person is told how to go back.
func runApplyWindows(ctx context.Context, dataDir string, trust root.TrustedMaterial, pins verify.Pins) apply.Result {
	return apply.RunMSI(ctx, windowsMSIConfig(dataDir, trust, pins))
}

// newWindowsMSI is Windows Installer and this computer, as the apply sees them (a test puts a fake here).
var newWindowsMSI = func(dataDir string) apply.MSI { return &apply.WindowsMSI{DataDir: dataDir} }

// windowsMSIConfig is the Windows apply's configuration. ClickedVersion is the version the person clicked Install update for (#82's
// one-time marker, read by installRequest before this runs): RunMSI refuses a run whose click is missing or for another version.
func windowsMSIConfig(dataDir string, trust root.TrustedMaterial, pins verify.Pins) apply.MSIConfig {
	return apply.MSIConfig{
		DataDir:        dataDir,
		Installed:      config.Version,
		Pins:           pins,
		Verifier:       verify.Sigstore{Trust: trust},
		Asset:          assetName("windows", runtime.GOARCH, false),
		MSI:            newWindowsMSI(dataDir),
		ClickedVersion: clickedVersion,
	}
}
