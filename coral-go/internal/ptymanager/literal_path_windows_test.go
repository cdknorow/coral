//go:build windows

package ptymanager

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #3913: the hand-off script removes itself with -LiteralPath, so a temp folder with [ ] (a
// profile like "A[1]") neither fails the delete nor leaves the script behind; and it starts with
// a UTF-8 BOM, so PowerShell 5.1 reads its non-ASCII path and command as written.
func TestPSHandoffRunsAndRemovesItselfInABracketFolder(t *testing.T) {
	ps := powerShellForTest(t)
	dir := filepath.Join(t.TempDir(), "A[1] Jane\u2019s")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "coral-command-1.ps1")
	// The command's own non-ASCII text must survive too: it prints its code points.
	script, invocation := psHandoff(ps, path, "Write-Output handed-off; [string]::Join(',', [int[]][char[]]"+psLiteral("Jos\u00e9\u2019s")+")")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runPowerShell(t, ps, invocation); got != "handed-off\r\n74,111,115,233,8217,115" && got != "handed-off\n74,111,115,233,8217,115" {
		t.Errorf("hand-off output = %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("hand-off script not removed (err %v)", err)
	}
}

// powerShellForTest is powershell.exe (or pwsh.exe); on a CI runner without either the test
// fails rather than skips, so a green run means the PowerShell checks ran (#3904).
func powerShellForTest(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("no PowerShell on this CI runner: the check must run there, not skip")
	}
	t.Skip("no PowerShell on this host")
	return ""
}

func runPowerShell(t *testing.T, ps, script string) string {
	t.Helper()
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("powershell failed: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}
