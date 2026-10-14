package ptymanager

import (
	"os/exec"
	"strings"
	"testing"
)

// skipIfPTYSpawnDenied skips a PTY test only when the environment really denies
// starting processes (a sandbox). A PTY spawn that fails with "operation not
// permitted" while a plain process starts is a bug in Coral, not a sandbox, and fails
// the test: the PTY backend was unusable on macOS for a long time behind a skip (#3229).
func skipIfPTYSpawnDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "operation not permitted") {
		return
	}
	if plain := exec.Command("/bin/sh", "-c", "true").Run(); plain != nil && strings.Contains(plain.Error(), "operation not permitted") {
		t.Skip("PTY spawn not permitted in this environment (sandbox)")
	}
	t.Fatalf("PTY spawn failed although plain processes can run here: %v", err)
}
