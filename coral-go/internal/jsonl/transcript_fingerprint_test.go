package jsonl

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5802: the fingerprint is a stat of the transcript file: the same while it is untouched, different when it grows or is written again, absent when there is no file.
func TestTranscriptFingerprintChangesWithTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1"
	_, _, ok := TranscriptFingerprint(sid, "", "claude")
	assert.False(t, ok, "no transcript yet")
	path := filepath.Join(dir, "proj", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"user"}`+"\n"), 0o644))
	gotPath, fp1, ok := TranscriptFingerprint(sid, "", "claude")
	require.True(t, ok)
	assert.Equal(t, path, gotPath)
	_, fp1b, _ := TranscriptFingerprint(sid, "", "claude")
	assert.Equal(t, fp1, fp1b, "unchanged file, same fingerprint")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"user"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, fp2, _ := TranscriptFingerprint(sid, "", "claude")
	assert.NotEqual(t, fp1, fp2, "a longer file")
	require.NoError(t, os.Chtimes(path, time.Now(), time.Now().Add(-time.Hour)))
	_, fp3, _ := TranscriptFingerprint(sid, "", "claude")
	assert.NotEqual(t, fp2, fp3, "the same size, written at another time")
	_, _, ok = TranscriptFingerprint("not-a-session-id", "", "claude")
	assert.False(t, ok, "only a session id names a file")
}
