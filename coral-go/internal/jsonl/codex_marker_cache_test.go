package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func codexDeveloperLine(id string) string {
	return fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"developer","content":"Coral session metadata:\nCORAL_SESSION_ID: %s"}}`+"\n", id)
}

func codexUserLine(text string) string {
	return fmt.Sprintf(`{"type":"event_msg","payload":{"type":"user_message","message":%q}}`+"\n", text)
}

// The cached lookup must answer exactly as a full re-read would after every
// kind of change a rollout file can see.
func TestCodexMarkerCache_MatchesFullScan(t *testing.T) {
	const idA = "aaaaaaaa-1111-2222-3333-444444444444"
	const idB = "bbbbbbbb-1111-2222-3333-444444444444"
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-2026-10-11T00-00-00-x.jsonl")
	appendTo := func(s string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, err = f.WriteString(s)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	check := func(step string, want string) {
		t.Helper()
		require.Equal(t, want, codexTranscriptCoralSessionID(path), "%s: full scan", step)
		require.Equal(t, want, codexTranscriptCoralSessionIDCached(path, nil), "%s: cached", step)
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, want, codexTranscriptCoralSessionIDCached(path, info), "%s: cached, unchanged file", step)
	}

	appendTo(`{"type":"session_meta","payload":{}}` + "\n" + codexUserLine("mentions CORAL_SESSION_ID: "+idB))
	check("no marker yet", "")
	appendTo(strings.Repeat(codexUserLine("chatter"), 200)) // past the 4KB head
	check("more chatter", "")

	// A developer line written in two pieces: the partial line must not be
	// skipped once it is completed.
	line := codexDeveloperLine(idA)
	appendTo(line[:40])
	check("partial marker line", "")
	appendTo(line[40:])
	check("completed marker line", idA)

	appendTo(codexDeveloperLine(idB))
	check("later marker does not replace the first", idA)

	// Replaced file (new inode) with a different marker.
	require.NoError(t, os.Remove(path))
	appendTo(codexDeveloperLine(idB))
	check("replaced file", idB)

	// Truncated and rewritten in place.
	require.NoError(t, os.WriteFile(path, []byte(codexUserLine("fresh start")), 0o644))
	check("truncated", "")
	appendTo(codexDeveloperLine(idA))
	check("marker after truncation", idA)

	// A line longer than the scanner allows stops scanning for good, in both.
	long := filepath.Join(dir, "rollout-long.jsonl")
	require.NoError(t, os.WriteFile(long, []byte(codexUserLine(strings.Repeat("x", 1100*1024))), 0o644))
	path = long
	check("over-long line", "")
	appendTo(codexDeveloperLine(idA))
	check("marker after over-long line stays unseen", "")
}
