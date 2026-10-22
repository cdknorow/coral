//go:build !darwin && !linux && !windows

package main

import "github.com/subgentic/coral/internal/updater/status"

// probeInstall: no apply on this platform, so there is nothing to say about the install.
func probeInstall() *status.Install { return nil }
