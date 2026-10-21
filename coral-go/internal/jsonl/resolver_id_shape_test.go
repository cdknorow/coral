package jsonl

import (
	"os"
	"path/filepath"
	"testing"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// #4590: a resolver that globs or joins a session id into a path returns nothing for an id
// that is not a lowercase UUID: "" , "*", "?", "[a-z]" and a part of an id must not match
// (or name) another session's transcript.
func TestResolvers_RefuseAnIdThatIsNotAUUID(t *testing.T) {
	dir := t.TempDir()
	codexHome := filepath.Join(dir, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_PROJECTS_DIR", filepath.Join(dir, "claude"))
	t.Setenv("ANTIGRAVITY_DATA_DIR", filepath.Join(dir, "brain"))
	real := "1f2e3d4c-5b6a-4978-8a9b-0c1d2e3f4a5b"
	rollout := filepath.Join(codexHome, "sessions", "2026", "10", "18", "rollout-2026-10-18T09-00-00-0aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa-"+real+".jsonl")
	claude := filepath.Join(dir, "claude", "proj", real+".jsonl")
	for _, p := range []string{rollout, claude} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A transcript planted beside the brain folder, named by a traversing id.
	planted := filepath.Join(dir, "outside", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "brain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planted, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveAgyTranscript("../outside"); got != "" {
		t.Errorf("agy traversing id resolved %q", got)
	}
	// Control: the full id resolves, so an empty answer below is the id check and not a missing file.
	if resolveCodexTranscript(real) == "" || resolveClaudeTranscript(real, "") == "" {
		t.Fatal("the control id does not resolve")
	}
	for _, bad := range []string{"", "*", "?", "[a-z]", "[0-9a-f]*", real[:12], real[:35], "../proj/" + real} {
		if got := resolveCodexTranscript(bad); got != "" {
			t.Errorf("codex %q resolved %q", bad, got)
		}
		if got := resolveClaudeTranscript(bad, ""); got != "" {
			t.Errorf("claude %q resolved %q", bad, got)
		}
		if got := resolveTranscriptPath(bad, "", at.Codex); got != "" {
			t.Errorf("codex via resolveTranscriptPath %q resolved %q", bad, got)
		}
		if got := resolveAgyTranscriptStrict(bad, ""); got != "" {
			t.Errorf("agy %q resolved %q", bad, got)
		}
	}
}
