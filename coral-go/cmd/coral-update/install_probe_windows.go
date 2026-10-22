//go:build windows

package main

import (
	"github.com/subgentic/coral/internal/updater/apply"
	"github.com/subgentic/coral/internal/updater/status"
)

// probeInstall describes this install for the status, from the same questions the MSI apply asks (apply.WindowsMSI: the registered per-machine product, and whether its folder is
// one the apply will touch). It only reads (Windows Installer's registration and the product's folder). Kind is msi for a registered per-machine install the apply can update,
// portable for a copy beside a registered product or one moved out of Program Files, unregistered when Windows Installer knows no per-machine Coral, and unwritable for any other
// reason the apply gives. Writable is "the updater can replace this install": true for the MSI, which Windows Installer replaces (not this user's writes) after the person's consent.
func probeInstall() *status.Install { return describeWindowsInstall(newWindowsMSI("")) }

// describeWindowsInstall is probeInstall for an MSI (a test gives it a fake).
func describeWindowsInstall(m apply.MSI) *status.Install {
	install, err := m.Install()
	if err != nil {
		return &status.Install{Kind: "unregistered", Unsupported: "no_registered_product"}
	}
	switch why := m.Unsupported(install); why {
	case "":
		return &status.Install{Kind: "msi", Writable: true}
	case "portable_install":
		return &status.Install{Kind: "portable", Unsupported: why}
	default:
		return &status.Install{Kind: "unwritable", Unsupported: why}
	}
}
