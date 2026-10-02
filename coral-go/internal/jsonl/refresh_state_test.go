package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRefreshAgyAppendReplacementAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	header := strings.Repeat(" ", 600) + "\n"
	stop := `{"type":"PLANNER_RESPONSE","created_at":"2026-10-02T08:00:00Z","tool_calls":[]}` + "\n"
	start := `{"type":"USER_INPUT","created_at":"2026-10-02T08:00:01Z"}` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(header+stop), 0600))
	var state agyTurnCache
	event, _, _ := readAgyTurnEventIncremental(path, &state)
	require.Equal(t, "stop", event)
	appendToFile(t, path, strings.TrimSuffix(start, "\n"))
	event, _, _ = readAgyTurnEventIncremental(path, &state)
	require.Equal(t, "stop", event, "partial records are not lifecycle evidence")
	appendToFile(t, path, "\n")
	event, timestamp, _ := readAgyTurnEventIncremental(path, &state)
	require.Equal(t, "prompt_submit", event)
	var restarted agyTurnCache
	restartedEvent, restartedTime, _ := readAgyTurnEventIncremental(path, &restarted)
	require.Equal(t, event, restartedEvent)
	require.Equal(t, timestamp, restartedTime)
	// The replacement deliberately has the same prefix, length and mtime.
	info, err := os.Stat(path)
	require.NoError(t, err)
	replacement := header + start + stop
	require.NoError(t, os.WriteFile(path+".new", []byte(replacement), 0600))
	require.NoError(t, os.Chtimes(path+".new", info.ModTime(), info.ModTime()))
	require.NoError(t, os.Rename(path+".new", path))
	event, _, _ = readAgyTurnEventIncremental(path, &state)
	require.Equal(t, "stop", event, "inode replacement must reset state")
	require.NoError(t, os.WriteFile(path, []byte(header+start), 0600))
	event, _, _ = readAgyTurnEventIncremental(path, &state)
	require.Equal(t, "prompt_submit", event, "truncation must reset state")
}

func TestRefreshCursorDoesNotReplayAndBoundsConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("first\n"), 0600))
	var state transcriptReadState
	var lines []string
	reset := func() { lines = nil }
	consume := func(line []byte) {
		lines = append(lines, string(line))
		if string(line) == "first\n" {
			appendToFile(t, path, "second\n")
		}
	}
	scanTranscriptAppend(path, &state, reset, consume)
	require.Equal(t, []string{"first\n"}, lines)
	scanTranscriptAppend(path, &state, reset, consume)
	require.Equal(t, []string{"first\n", "second\n"}, lines)
	scanTranscriptAppend(path, &state, reset, consume)
	require.Len(t, lines, 2, "unchanged refresh must not consume records")
}

func TestRefreshFirstPromptInvalidatesAfterReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	reader := NewSessionReader()
	reader.getOrCreateFirstPromptCache("session").path = path
	write := func(prompt string) {
		require.NoError(t, os.WriteFile(path, []byte(`{"type":"user","message":{"content":"`+prompt+`"}}`+"\n"), 0600))
	}
	write("original")
	require.Equal(t, "original", reader.FirstUserPrompt("session", "", "claude"))
	write("replacement")
	require.Equal(t, "replacement", reader.FirstUserPrompt("session", "", "claude"))
	require.NoError(t, os.Remove(path))
	require.Equal(t, "replacement", reader.CachedFirstUserPrompt("session"), "sleeping display must not touch disk")
	require.Empty(t, reader.FirstUserPrompt("session", "", "claude"))
}

func TestRefreshLegacyArrayCacheAndRewrite(t *testing.T) {
	for _, name := range []string{"legacy.json", "transcript.jsonl"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			require.NoError(t, os.WriteFile(path, []byte(`[{"role":"user","timestamp":"2026-10-02T08:00:00Z"}]`), 0600))
			var state agyTurnCache
			event, _, _ := readAgyTurnEventIncremental(path, &state)
			require.Equal(t, "prompt_submit", event)
			event, _, _ = readAgyTurnEventIncremental(path, &state)
			require.Equal(t, "prompt_submit", event)
			require.NoError(t, os.WriteFile(path, []byte(`[{"role":"model","timestamp":"2026-10-02T08:00:01Z"}]`), 0600))
			event, _, _ = readAgyTurnEventIncremental(path, &state)
			require.Equal(t, "stop", event)
		})
	}
}
