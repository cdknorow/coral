package eulatext

import (
	"os"
	"strings"
	"testing"
)

// The desktop app's Terms of Service dialogs (#4360 follow-up): they are native windows a test cannot open, so this checks the words they are built from and that
// each platform's dialog makes the promise the words make: Enter declines, accepting is deliberate.
func TestDialogWords(t *testing.T) {
	for _, want := range []string{"Accept", "Decline", "quit Coral", "Nothing is accepted until you choose Accept"} {
		if !strings.Contains(DialogIntro, want) {
			t.Errorf("the dialog's line %q does not say %q", DialogIntro, want)
		}
	}
	if AcceptLabel != "Accept" || DeclineLabel != "Decline" {
		t.Errorf("the buttons are %q and %q", AcceptLabel, DeclineLabel)
	}
	if !strings.Contains(AccessibleName, "Terms of Service") || !strings.Contains(AccessibleName, "scroll") {
		t.Errorf("the terms need a name a screen reader can read: %q", AccessibleName)
	}
}

func TestDialogsDefaultToDecline(t *testing.T) {
	mac, err := os.ReadFile("../../cmd/coral-app/eula_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`acceptButton.keyEquivalent = @"";`, `declineButton.keyEquivalent = @"\r";`, "initialFirstResponder = textView", "accessibilityLabel"} {
		if !strings.Contains(string(mac), want) {
			t.Errorf("the macOS dialog lost %q", want)
		}
	}
	win, err := os.ReadFile("../../cmd/coral-app/eula_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(win), "mbYesNo|mbIconQuestion|mbDefButton2") {
		t.Error("the Windows dialog must make No the default button")
	}
}
