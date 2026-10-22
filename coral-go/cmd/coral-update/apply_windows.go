//go:build windows

package main

import (
	"context"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sigstore/sigstore-go/pkg/root"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/stage"
	"github.com/subgentic/coral/internal/updater/verify"
)

// runApplyWindows is the Windows apply: the staged MSI, verified again, installed as a major upgrade by an unelevated msiexec (#162). The
// rollback package is the installed version's own MSI the check kept (stage.Config.Rollback), verified again here, where it is used (C).
func runApplyWindows(ctx context.Context, dataDir string, trust root.TrustedMaterial, pins verify.Pins) apply.Result {
	return apply.RunMSI(ctx, windowsMSIConfig(dataDir, trust, pins))
}

// newWindowsMSI is Windows Installer and this computer, as the apply sees them (a test puts a fake here).
var newWindowsMSI = func(dataDir string) apply.MSI { return &apply.WindowsMSI{DataDir: dataDir} }

// windowsMSIConfig is the Windows apply's configuration. ClickedVersion is the version the person clicked Install update for (#82's
// one-time marker, read by installRequest before this runs): RunMSI refuses a run whose click is missing or for another version.
func windowsMSIConfig(dataDir string, trust root.TrustedMaterial, pins verify.Pins) apply.MSIConfig {
	verifier := verify.Sigstore{Trust: trust}
	asset := assetName("windows", runtime.GOARCH, false)
	return apply.MSIConfig{
		DataDir:        dataDir,
		Installed:      config.Version,
		Pins:           pins,
		Verifier:       verifier,
		Asset:          asset,
		MSI:            newWindowsMSI(dataDir),
		ClickedVersion: clickedVersion,
		Rollback: func() (string, *verify.Record, error) {
			s, err := stage.LoadRollback(dataDir, config.Version)
			if err != nil {
				return "", nil, err
			}
			st, _ := verify.LoadState(filepath.Join(dataDir, stage.UpdatesDir)) // its revocations count
			rec, err := stage.ReverifyRollback(verifier, pins, s, asset, config.Version, st, time.Now(), stage.DefaultMaxPackage)
			if err != nil {
				return "", nil, err
			}
			return s.PackagePath, rec, nil
		},
	}
}

// installSupport is what "coral-update status" says about this install: whether the apply can update it, for the server to ask before it quits for
// an update (#5946). "supported" is a Coral registered per machine, under Program Files; otherwise the apply's own word for why not.
func installSupport() string {
	m := newWindowsMSI("")
	install, err := m.Install()
	if err != nil {
		return "no_registered_product"
	}
	if why := m.Unsupported(install); why != "" {
		return why
	}
	return "supported"
}
