//go:build windows

package main

import (
	"os"
	"path/filepath"

	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/status"
)

// probeInstall describes this install for the status, from the same questions the MSI apply asks (apply.WindowsMSI: the registered per-machine product, and whether its folder is
// one the apply will touch), and from the folder this program runs from (#6436): the registered product is judged only when this is it. It only reads (Windows Installer's registration
// and the folders). Kind is msi for the registered per-machine install the apply can update, portable for a copy run from any other folder (beside a registered product, or moved
// out of Program Files), unregistered when Windows Installer knows no per-machine Coral, and unwritable for any other reason the apply gives. Writable is "the updater can replace
// this install": true for the MSI, which Windows Installer replaces (not this user's writes) after the person's consent.
func probeInstall() *status.Install { return describeWindowsInstall(newWindowsMSI(""), selfFolder()) }

// selfFolder is the folder of this program, symlinks resolved ("" when it cannot be read); a variable so a test can say.
var selfFolder = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Dir(exe)
}

// describeWindowsInstall is probeInstall for an MSI and the folder of this program (a test gives it fakes).
func describeWindowsInstall(m apply.MSI, self string) *status.Install {
	return describeRegistered(m, self)
}
