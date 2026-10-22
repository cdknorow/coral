package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/trigger"
)

// The Windows "Install update" quits the server before the updater runs (#166), so every answer of "apply" that installed nothing must start
// Coral again, or the person is left with none (#5946, #5931's F1). The server's own folder is in the click's marker (trigger.Marker.InstallDir):
// a portable or unregistered Coral has no registered product to find it by. A run with no marker (by hand, from a script, a leftover) stopped
// nothing, so it starts nothing.

// errNoRestart: no way to start Coral from here (not Windows).
var errNoRestart = errors.New("Coral can be started again on Windows only")

// restartGOOS is the platform the restart is for; a test says "windows".
var restartGOOS = runtime.GOOS

// startCoralFrom starts coral-tray from dir as this user (restart_windows.go); a test replaces it.
var startCoralFrom = func(dataDir, dir string) error { return errNoRestart }

// restartAfterNothing consumes the click's marker and starts Coral again from the folder it names. attempted is false when there was nothing to
// undo (no good marker, or not Windows); ok is whether Coral was started.
func restartAfterNothing(dataDir string, now time.Time) (attempted, ok bool) {
	if restartGOOS != "windows" {
		return false, false
	}
	m, err := trigger.Consume(dataDir, now)
	if err != nil {
		return false, false
	}
	return true, restartFromMarker(dataDir, m.InstallDir)
}

// restartFromMarker starts Coral from dir (a marker already consumed); a folder with no coral-tray.exe in it is a failure said in the answer.
func restartFromMarker(dataDir, dir string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	if fi, err := os.Lstat(filepath.Join(dir, "coral-tray.exe")); err != nil || !fi.Mode().IsRegular() {
		return false
	}
	return startCoralFrom(dataDir, dir) == nil
}

// installedNothingAndLeftNoCoral is whether an apply answer is one of the early ones that return before the MSI apply starts the old Coral again:
// no registered product, a portable or unreadable install (all "unsupported_install"), or the updates folder not usable.
func installedNothingAndLeftNoCoral(res apply.Result) bool {
	return res.Result == apply.ResultUnsupported || (res.Result == apply.ResultFailed && res.FailedStage == "updates_folder")
}

// dataDirForRestart is where the marker is: the data folder the updater would use.
func dataDirForRestart() string { return config.ResolveCoralDir("") }

// restartAfterNothingFailed starts Coral again after an apply that stopped at its off switches, and reports a start that was tried and failed.
func restartAfterNothingFailed() bool {
	attempted, ok := restartAfterNothing(dataDirForRestart(), time.Now())
	return attempted && !ok
}
