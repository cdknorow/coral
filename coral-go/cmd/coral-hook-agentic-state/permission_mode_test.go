package main

import "testing"

// A Claude hook payload's permission_mode is forwarded only when it is one of the six words the CLI has.
func TestClaudePermissionMode(t *testing.T) {
	for _, m := range []string{"default", "acceptEdits", "plan", "auto", "bypassPermissions", "dontAsk"} {
		if got := claudePermissionMode(map[string]any{"permission_mode": m}); got != m {
			t.Errorf("%s: got %q", m, got)
		}
	}
	for name, d := range map[string]map[string]any{
		"absent": {}, "empty": {"permission_mode": ""}, "unknown": {"permission_mode": "yolo"}, "case": {"permission_mode": "Auto"}, "not a string": {"permission_mode": 3}, "trailing": {"permission_mode": "plan "},
	} {
		if got := claudePermissionMode(d); got != "" {
			t.Errorf("%s: got %q", name, got)
		}
	}
}
