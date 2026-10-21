package main

import (
	"os"
	"testing"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// The tests' temp files (a token file among them) are made in a folder private to this user, whatever the real %TEMP% grants:
// a Codex sandbox adds an entry of its own to %TEMP%, every file made under it inherits that, and the private-file checks refuse
// such a file, which made TestAutoAcceptOffNeverSendsUnsigned fail while an agent's Codex ran at the same time (#83, #28).
func TestMain(m *testing.M) {
	cleanup := secfiletest.UsePrivateTemp()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
