package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #3830: the API error a Claude transcript ends with. The fixture is a real Claude Code 2.1.301
// transcript (claude -p with a bad API key; the working directory neutral): its assistant line
// is {"error":"authentication_failed","isApiErrorMessage":true,"apiErrorStatus":401}. The other
// states are that line with the kind and text the same CLI writes for them.
func TestReadClaudeAPIError(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.301", "auth_invalid_api_key.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(fixture)), "\n")
	if len(lines) != 2 {
		t.Fatalf("fixture: %d lines", len(lines))
	}
	userLine, errLine := lines[0], lines[1]
	// variant is the captured error line with another kind, status and text.
	variant := func(kind, status, text string) string {
		l := strings.Replace(errLine, `"error":"authentication_failed"`, kind, 1)
		l = strings.Replace(l, `"apiErrorStatus":401`, status, 1)
		return strings.Replace(l, "Invalid API key \u00b7 Fix external API key", text, 1)
	}
	plainReply := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"You've hit your usage limit? Please run /login, said the docs."}]},"timestamp":"2026-10-16T10:11:00.000Z"}`

	cases := []struct {
		name       string
		lines      []string
		needsLogin bool
		usageLimit bool
		text       string
	}{
		{"invalid API key (captured)", []string{userLine, errLine}, true, false, "Invalid API key \u00b7 Fix external API key"},
		{"invalid API key, interactive", []string{userLine, variant(`"error":"authentication_failed"`, `"apiErrorStatus":401`, "Invalid API key \u00b7 Please run /login")}, true, false, "Invalid API key \u00b7 Please run /login"},
		{"expired OAuth session", []string{userLine, variant(`"error":"authentication_failed"`, `"apiErrorStatus":401`, "Session expired. Please run /login to sign in again.")}, true, false, "Session expired. Please run /login to sign in again."},
		{"revoked OAuth token", []string{userLine, variant(`"error":"authentication_failed"`, `"apiErrorStatus":403`, "Failed to authenticate: OAuth token revoked. Please log in again or contact your administrator.")}, true, false, ""},
		{"not logged in", []string{userLine, variant(`"error":"authentication_failed"`, `"apiErrorStatus":401`, "Not logged in \u00b7 Please run /login")}, true, false, ""},
		{"organization not allowed", []string{userLine, variant(`"error":"oauth_org_not_allowed"`, `"apiErrorStatus":403`, "Your organization does not allow this login")}, true, false, ""},
		{"older CLI: no kind, 401", []string{userLine, variant(`"x":"y"`, `"apiErrorStatus":401`, "API Error: 401")}, true, false, ""},
		{"older CLI: no kind, no status, /login text", []string{userLine, variant(`"x":"y"`, `"s":0`, "OAuth token has expired. Please run /login")}, true, false, ""},
		{"usage limit", []string{userLine, variant(`"error":"rate_limit"`, `"apiErrorStatus":429`, "You've hit your limit \u00b7 resets 3pm (UTC)")}, false, true, "You've hit your limit \u00b7 resets 3pm (UTC)"},
		{"older CLI usage limit", []string{userLine, variant(`"x":"y"`, `"s":0`, "Claude AI usage limit reached|1760000000")}, false, true, ""},
		{"overloaded: neither", []string{userLine, variant(`"error":"server_error"`, `"apiErrorStatus":529`, "API Error: 529 Overloaded")}, false, false, ""},
		{"a reply that mentions both: neither", []string{userLine, plainReply}, false, false, ""},
		{"the user wrote since the error: cleared", []string{userLine, errLine, `{"type":"user","message":{"role":"user","content":"/login"},"timestamp":"2026-10-16T10:12:00.000Z"}`}, false, false, ""},
		{"a turn ran since the error: cleared", []string{userLine, errLine, plainReply}, false, false, ""},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("CLAUDE_PROJECTS_DIR", base)
			dir := filepath.Join(base, "-tmp-project")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			sid := "00000000-0000-4000-8000-00000000383" + string(rune('a'+i%6))
			if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(strings.Join(c.lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			e, ok := NewSessionReader().ReadClaudeAPIError(sid, "/tmp/project")
			if !ok {
				t.Fatal("the transcript is found")
			}
			if e.NeedsLogin() != c.needsLogin || e.UsageLimit() != c.usageLimit {
				t.Fatalf("got %+v: needs login %v, usage limit %v", e, e.NeedsLogin(), e.UsageLimit())
			}
			if c.text != "" && e.Text != c.text {
				t.Fatalf("text %q, want %q", e.Text, c.text)
			}
		})
	}
}

// No transcript yet (a first run at the login screen): not found, so the screen decides.
func TestReadClaudeAPIErrorNoTranscript(t *testing.T) {
	t.Setenv("CLAUDE_PROJECTS_DIR", t.TempDir())
	if _, ok := NewSessionReader().ReadClaudeAPIError("00000000-0000-4000-8000-000000003830", "/tmp/project"); ok {
		t.Fatal("no transcript is reported as none")
	}
}

// The error is followed as the transcript grows: set by an error line, cleared by the next message.
func TestReadClaudeAPIErrorIncremental(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", base)
	dir := filepath.Join(base, "-tmp-project")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.301", "auth_invalid_api_key.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sid := "00000000-0000-4000-8000-000000003831"
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewSessionReader()
	if e, _ := r.ReadClaudeAPIError(sid, "/tmp/project"); !e.NeedsLogin() {
		t.Fatalf("after the error: %+v", e)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"user","message":{"role":"user","content":"hello again"},"timestamp":"2026-10-16T10:12:00.000Z"}` + "\n")
	f.Close()
	if e, _ := r.ReadClaudeAPIError(sid, "/tmp/project"); e.NeedsLogin() || e.Kind != "" {
		t.Fatalf("after a new prompt: %+v", e)
	}
}
