//go:build !relaunchfixture

package main

import "github.com/subgentic/coral/internal/config"

// applyRelaunchFixtureBuild is the relaunch test seam's startup check (relaunch_fixture_on.go); a build without the tag has nothing to check.
func applyRelaunchFixtureBuild(*config.Config, string, *bool, bool) error { return nil }
