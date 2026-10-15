package jsonl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3573: a Claude line's own "version" (written before "message"), never one inside the text.
func TestClaudeLineVersion(t *testing.T) {
	for line, want := range map[string]string{
		`{"parentUuid":null,"sessionId":"s","version":"2.1.300","type":"user","message":{"role":"user","content":"hi"}}`: "2.1.300",
		`{"version":"2.2.0-beta.1","type":"assistant"}`:                                                                  "2.2.0-beta.1",
		`{"type":"user","message":{"content":"set \"version\":\"9.9.9\" in package.json"}}`:                              "",
		`{"type":"summary","summary":"x"}`:                                                                               "",
		`{"version":"latest","type":"user"}`:                                                                             "",
		`{"version":"2.1","type":"user"}`:                                                                                "",
		`{"sessionId":"s","version":"2.1.300","message":{"content":[{"type":"tool_use","input":{"version":"0.0.1"}}]}}`:  "2.1.300",
	} {
		assert.Equal(t, want, claudeLineVersion([]byte(line)), line)
	}
}

// The version follows the transcript as Claude Code updates itself mid-session.
func TestReadClaudeVersionFollowsTheTranscript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000003573"
	path := filepath.Join(dir, "x", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	r := NewSessionReader()
	assert.Equal(t, "", r.ReadClaudeVersion(sid, ""), "no transcript yet")

	require.NoError(t, os.WriteFile(path, []byte(`{"sessionId":"`+sid+`","version":"2.1.299","type":"user","message":{"content":"a"},"timestamp":"2026-10-15T00:00:00Z"}`+"\n"), 0o600))
	r = NewSessionReader() // the path is looked up again after 2 s; a fresh reader finds it now
	assert.Equal(t, "2.1.299", r.ReadClaudeVersion(sid, ""))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"summary","summary":"s"}` + "\n" + `{"sessionId":"` + sid + `","version":"2.1.300","type":"assistant","message":{"content":"b"},"timestamp":"2026-10-15T00:01:00Z"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, "2.1.300", r.ReadClaudeVersion(sid, ""), "the newest line's version, after Claude updated itself")
}

// QA's #3573 review: in a tool-result line the structured toolUseResult comes before the record's
// own version and can carry a "version" of its own; only the top-level key counts.
func TestClaudeLineVersionIsTheRecordsOwn(t *testing.T) {
	for line, want := range map[string]string{
		`{"parentUuid":"p","isSidechain":false,"type":"user","message":{"role":"user","content":[]},"uuid":"u","timestamp":"2026-10-15T00:00:00Z","toolUseResult":{"name":"pkg","version":"9.9.9"},"cwd":"/w","sessionId":"s","version":"2.1.300"}`: "2.1.300",
		`{"type":"user","toolUseResult":{"version":"9.9.9"}}`: "",
		`not json "version":"2.1.300"`:                        "",
	} {
		assert.Equal(t, want, claudeLineVersion([]byte(line)), line)
	}
}
