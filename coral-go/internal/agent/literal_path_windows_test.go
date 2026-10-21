//go:build windows

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// #3913 (Security's note on #3904): a path is passed to Get-Content and Remove-Item as
// -LiteralPath, so [ and ] in it (a profile folder like "A[1]") are not wildcards. Real
// PowerShell reads the token and deletes its file, and reads a prompt file, in such a folder.
func TestPowerShellCmdletsTakeLiteralPaths(t *testing.T) {
	ps := powerShellForTest(t)
	dir := filepath.Join(t.TempDir(), "A[1] Jane\u2019s")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "token.txt")
	if err := os.WriteFile(tok, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := SessionTokenMinter
	SessionTokenMinter = func(string, string) string { return tok }
	defer func() { SessionTokenMinter = old }()
	prefix := sessionTokenPrefix(LaunchParams{SessionID: "s1", SessionName: "claude-s1"}, "claude")
	if !strings.Contains(prefix, "-LiteralPath") {
		t.Fatalf("token prefix doesn't use -LiteralPath: %s", prefix)
	}
	if got := runPowerShell(t, ps, prefix+" Write-Output $env:"+SessionTokenEnv); got != "secret-token" {
		t.Errorf("token read from %q = %q", tok, got)
	}
	if _, err := os.Stat(tok); !os.IsNotExist(err) {
		t.Errorf("token file not deleted (err %v)", err)
	}

	prompt := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(prompt, []byte("hello [world]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runPowerShell(t, ps, "Write-Output "+FormatPromptFileArg(prompt)); got != "hello [world]" {
		t.Errorf("prompt file read = %q", got)
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
