package jsonl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3597 (weak site W20): agy's conversationId is a folder name under its conversations folder,
// never a path. One with a separator or ".." is ignored, so a history line can't point the
// transcript reader at another folder's transcript.jsonl.
func TestAgyHistoryConversationIDIsAName(t *testing.T) {
	base := filepath.Join(t.TempDir(), "conversations")
	wd := t.TempDir()
	transcript := func(dir string) string {
		p := filepath.Join(dir, ".system_generated", "logs", "transcript.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("{}\n"), 0o600))
		return p
	}
	good := transcript(filepath.Join(base, "c0ffee12-3456-4789-8abc-def012345678"))
	transcript(filepath.Join(filepath.Dir(base), "escape")) // base/../escape

	history := filepath.Join(t.TempDir(), "history.jsonl")
	write := func(ids ...string) {
		f, err := os.Create(history)
		require.NoError(t, err)
		for _, id := range ids {
			b, _ := json.Marshal(map[string]string{"workspace": wd, "conversationId": id})
			f.Write(append(b, '\n'))
		}
		f.Close()
	}

	// The newest line wins; an unsafe id is skipped, not followed.
	write("c0ffee12-3456-4789-8abc-def012345678", "../escape")
	assert.Equal(t, good, resolveAgyTranscriptFromHistory(history, base, wd))
	for _, id := range []string{"../escape", "..", "a/b", "-flag", "/abs"} {
		write(id)
		assert.Empty(t, resolveAgyTranscriptFromHistory(history, base, wd), id)
	}
}
