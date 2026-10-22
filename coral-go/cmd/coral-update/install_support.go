package main

import "github.com/subgentic/coral/internal/updater/status"

// installFromSupport turns Windows' answer (installSupport: "supported", or why the MSI apply cannot update this install) into the status's one install object, which every OS fills
// the same way: writable is "the updater can replace this install" (#5946 and step 3 B). "" is where the question does not apply (macOS and Linux, whose probeInstall fills it).
func installFromSupport(s string) *status.Install {
	switch s {
	case "":
		return nil
	case "supported":
		return &status.Install{Kind: "msi", Writable: true}
	case "portable_install":
		return &status.Install{Kind: "portable", Unsupported: s}
	case "no_registered_product":
		return &status.Install{Kind: "unregistered", Unsupported: s}
	}
	return &status.Install{Kind: "unwritable", Unsupported: s}
}
