package routes

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// #25: the CLI check's --version probe runs the CLI without the server's own CORAL_* settings.
func TestCLIVersionProbeDoesNotPassTheServersSettings(t *testing.T) {
	const sentinel = "coral-sentinel-secret"
	t.Setenv("CORAL_SECRET_KEY", sentinel)
	dir := t.TempDir()
	bin := filepath.Join(dir, "cli")
	script := "#!/bin/sh\necho \"v [$CORAL_SECRET_KEY]\"\n"
	if runtime.GOOS == "windows" {
		bin += ".cmd"
		script = "@echo off\r\necho v [%CORAL_SECRET_KEY%]\r\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	version, probe := probeCLIVersion(context.Background(), bin)
	if probe != probeOK {
		t.Fatalf("probe = %q", probe)
	}
	if strings.Contains(version, sentinel) {
		t.Errorf("the CLI saw the server's settings: %q", version)
	}
}
