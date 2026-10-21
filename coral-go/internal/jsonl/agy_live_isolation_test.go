package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// #4590: an agy session whose own transcript is not written yet must not read another agy
// session's from the same workspace (the newest history row for the folder) in its first
// prompt or its turn state.
func TestAgyLiveReaders_NeverTakeAnotherSessionsTranscriptFromTheSameWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	brainDir := filepath.Join(tmpDir, "brain")
	t.Setenv("ANTIGRAVITY_DATA_DIR", brainDir)
	ws := "/workspace/shared"
	otherCoral := "24c7245b-9fb4-67de-871c-a03f6186ed31"
	myCoral := "14c7245b-9fb4-67de-871c-a03f6186ed30"
	dir := filepath.Join(brainDir, "other-conv", ".system_generated", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := fmt.Sprintf(`{"step_index":0,"type":"USER_INPUT","content":"%s %s the other session's prompt"}`+"\n", at.CoralSessionMarkerPrefix, otherCoral)
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "history.jsonl"), []byte(fmt.Sprintf(`{"workspace":%q,"conversationId":"other-conv"}`+"\n", ws)), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewSessionReader()
	if got := r.FirstUserPrompt(myCoral, ws, at.Agy); got != "" {
		t.Fatalf("first prompt of another session: %q", got)
	}
	r.ReadAgyTurnEvent(myCoral, ws)
	if c := r.cache[myCoral]; c == nil || c.path != "" {
		t.Fatalf("turn state bound to %v", c)
	}
	// The other session, by its marker, still finds its own.
	if got := r.FirstUserPrompt(otherCoral, ws, at.Agy); got == "" {
		t.Fatal("the marked session lost its own first prompt")
	}
}
