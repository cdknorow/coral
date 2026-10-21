package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// #25: the --version probe runs the CLI without the server's own CORAL_* settings (CORAL_SECRET_KEY and the like).
func TestCLIVersionProbeDoesNotPassTheServersSettings(t *testing.T) {
	const sentinel = "coral-sentinel-secret"
	t.Setenv("CORAL_SECRET_KEY", sentinel)
	t.Setenv("CORAL_TEST_VISIBLE", "visible") // not allowed: also removed
	t.Setenv("PATH_CHECK_SENTINEL", "kept")    // not a CORAL_ variable: passes
	dir := t.TempDir()
	seen := filepath.Join(dir, "seen.txt")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho \"[$CORAL_SECRET_KEY][$CORAL_TEST_VISIBLE][$PATH_CHECK_SENTINEL]\" > '" + seen + "'\necho 3.4.5\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		script = "@echo off\r\necho [%CORAL_SECRET_KEY%][%CORAL_TEST_VISIBLE%][%PATH_CHECK_SENTINEL%]> \"" + seen + "\"\r\necho 3.4.5\r\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{CLIPathSettingKey("claude"): bin}
	if v := CLIVersion(settings, "claude"); v != "3.4.5" {
		t.Fatalf("version = %q", v)
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if strings.Contains(s, sentinel) || strings.Contains(s, "visible") {
		t.Errorf("the CLI saw the server's settings: %q", s)
	}
	if !strings.Contains(s, "kept") {
		t.Errorf("an ordinary variable was dropped: %q", s)
	}
}
