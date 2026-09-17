package hooks

import "testing"

func TestSessionIDFromName(t *testing.T) {
	cases := map[string]string{
		"codex-baccd3e1-d958-5ae2-a43b-d99d3f7d6891":  "baccd3e1-d958-5ae2-a43b-d99d3f7d6891",
		"CLAUDE-BACCD3E1-D958-5AE2-A43B-D99D3F7D6891": "baccd3e1-d958-5ae2-a43b-d99d3f7d6891",
		"coral-go":       "",
		"":               "",
		"codex-notauuid": "",
	}
	for in, want := range cases {
		if got := sessionIDFromName(in); got != want {
			t.Errorf("sessionIDFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Codex hooks carry Codex's own thread ID in the payload, which Coral does
// not know about. When the hook is not running under tmux, the
// CORAL_SESSION_NAME env var Coral exports must win over the payload.
func TestResolveSessionID_PrefersCoralSessionNameOverPayload(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("CORAL_SESSION_NAME", "codex-baccd3e1-d958-5ae2-a43b-d99d3f7d6891")
	got := ResolveSessionID("01a0a8c9-d7b4-7af3-8b20-d47348b761a5")
	if got != "baccd3e1-d958-5ae2-a43b-d99d3f7d6891" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveSessionID_FallsBackToPayload(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("CORAL_SESSION_NAME", "")
	if got := ResolveSessionID("payload-id"); got != "payload-id" {
		t.Fatalf("got %q", got)
	}
}
