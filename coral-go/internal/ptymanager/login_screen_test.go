package ptymanager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #3830: Claude's sign-in screens are read only at the bottom of the screen, below any input box.
// The real screens are in the corpus (testdata/screens/claude/2.1.301, label "login").
func TestClaudeLoginOnScreenNearMisses(t *testing.T) {
	idle, err := os.ReadFile(filepath.Join("testdata", "screens", "claude", "2.1.300", "idle.ansi"))
	if err != nil {
		t.Fatal(err)
	}
	oauth, err := os.ReadFile(filepath.Join("testdata", "screens", "claude", "2.1.301", "login_oauth_code.ansi"))
	if err != nil {
		t.Fatal(err)
	}
	if on, why := ClaudeLoginOnScreen(string(oauth)); !on || why != "Paste code here if prompted" {
		t.Fatalf("the OAuth screen: %v %q", on, why)
	}
	// A reply that quotes the login lines, above the agent's input box: not a login screen.
	quoted := "\u23fa To sign in, Claude shows:\n  Select login method:\n  Paste code here if prompted >\n\n" + string(idle)
	if on, why := ClaudeLoginOnScreen(quoted); on {
		t.Fatalf("a quoted login screen above the input box: %q", why)
	}
	// A finished login: the OAuth screen scrolled up, the agent's screen below it.
	if on, why := ClaudeLoginOnScreen(string(oauth) + strings.Repeat("\n", 2) + string(idle)); on {
		t.Fatalf("an old login screen above the input box: %q", why)
	}
	if open, why := PromptOnScreen(quoted); open && strings.HasPrefix(why, loginWhy) {
		t.Fatalf("the gate reads a quoted login screen as one: %q", why)
	}
}
