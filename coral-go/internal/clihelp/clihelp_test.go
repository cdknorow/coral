package clihelp

import "testing"

func TestRequested(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"--help"}, true},
		{[]string{"x", "-h"}, true},
		{[]string{"-help"}, true},
		{[]string{"see --help for usage"}, false},
		{[]string{"--", "--help"}, false},
		{nil, false},
	} {
		if got := Requested(c.args); got != c.want {
			t.Errorf("Requested(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestUsageLines(t *testing.T) {
	text := "Usage: x\n\nCommands:\n  post \"m\"   Post\n       continued\n  read       Read\n  reassign/cancel/amend  Change\nEnv:\n  X  y"
	if got, want := UsageLines(text, "post"), "  post \"m\"   Post\n       continued"; got != want {
		t.Errorf("post: %q, want %q", got, want)
	}
	if got := UsageLines(text, "cancel"); got != "  reassign/cancel/amend  Change" {
		t.Errorf("cancel: %q", got)
	}
	if got := UsageLines(text, "missing"); got != "" {
		t.Errorf("missing: %q", got)
	}
}

func TestCheckTextFlags(t *testing.T) {
	text := map[string]bool{"message": true, "reason": true}
	for _, args := range [][]string{
		{"--message", "--help"}, {"--message", "-x"}, {"-message", "--force"}, {"--reason=-h"}, {"5", "--message", "--bogus"},
	} {
		if CheckTextFlags(args, text) == nil {
			t.Errorf("%q: want error", args)
		}
	}
	for _, args := range [][]string{
		{"--message", "fixed --help handling"}, {"--message", "-"}, {"--message", "-1 left"},
		{"--message", "-5% faster"}, {"--message", "--- summary ---"}, {"--message", "- [x] done"},
		{"--message", "->"}, {"--message", "-5%"}, {"--message", "---"}, {"--message", "--"}, {"--message", "-v1.2 shipped"},
		{"--message"}, {"--to", "--x"}, {"--", "--message", "--x"},
	} {
		if err := CheckTextFlags(args, text); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// Only option-shaped words count: a dash followed by a letter, then letters,
// digits and dashes, with nothing else.
func TestIsFlagLike(t *testing.T) {
	for _, s := range []string{"-h", "--help", "-x", "--dry-run", "-message"} {
		if !IsFlagLike(s) {
			t.Errorf("%q should be flag-like", s)
		}
	}
	for _, s := range []string{"-5%", "-5", "---", "--", "-", "->", "- [x] done", "--- summary ---", "-5% latency", "-v1.2", "--help me", "x--help"} {
		if IsFlagLike(s) {
			t.Errorf("%q should be text", s)
		}
	}
}
