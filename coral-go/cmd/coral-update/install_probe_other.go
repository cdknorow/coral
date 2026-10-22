//go:build !darwin && !linux

package main

import "github.com/subgentic/coral/internal/updater/status"

// probeInstall: Windows has no apply yet, so there is nothing to say about the install.
func probeInstall() *status.Install { return nil }
