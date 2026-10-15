package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// #3492: where agents get Coral's tools. CORAL_HOOK_DIR wins; a dev or test server never
// falls back to the installed app's (it would run another version's hooks against itself,
// silently); the installed app is the last resort only for a release install.
func TestCoralToolsSource(t *testing.T) {
	installed := t.TempDir()
	if err := os.WriteFile(filepath.Join(installed, "coral-board"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevDir, prevAllow := installedToolsDir, installedFallback.Load()
	installedToolsDir = installed
	t.Cleanup(func() { installedToolsDir = prevDir; installedFallback.Store(prevAllow) })
	// The test binary has no coral-board or hooks next to it.

	SetInstalledToolsFallback(true)
	if dir, src := CoralToolsSource(); dir != installed || src != "the installed Coral app" {
		t.Errorf("release install: %q %q", dir, src)
	}
	SetInstalledToolsFallback(false)
	if dir, src := CoralToolsSource(); dir != "" || src != "" {
		t.Errorf("a dev or test server must not use the installed app's tools: %q %q", dir, src)
	}
	if CoralToolsDir() != "" || WrapWithBundlePath("x") != "x" {
		t.Error("no tools: no PATH prefix")
	}
	own := t.TempDir()
	t.Setenv(HookDirEnv, own)
	if dir, src := CoralToolsSource(); dir != own || src != HookDirEnv {
		t.Errorf("CORAL_HOOK_DIR: %q %q", dir, src)
	}
}
