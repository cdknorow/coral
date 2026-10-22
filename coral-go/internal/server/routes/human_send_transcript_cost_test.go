package routes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/jsonl"
	"github.com/subgentic/coral/internal/store"
)

// The transcript read before and during a person's send is the chat's own cached reader (#6089 review): on a very large transcript the first read parses the file once, and every later read, and the
// read after the agent appends the message, parses only what was appended. A send must never re-parse the whole transcript.
func TestTheTranscriptReadOfASendIsIncrementalOnAVeryLargeTranscript(t *testing.T) {
	proj := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", proj)
	sub := filepath.Join(proj, "proj")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	sid := "eeeeeeee-6089-4000-8000-0000000000ff"
	var b strings.Builder
	const turns = 40000 // about 80,000 messages, some 12 MB
	for i := 0; i < turns; i++ {
		fmt.Fprintf(&b, `{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"content":"Please work on step %d of the task and report results with details"}}`+"\n", i)
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":"2026-01-01T00:00:01Z","message":{"content":[{"type":"text","text":"Working on step %d: reading files, running tests, summarising the output for you"}]}}`+"\n", i)
	}
	path := filepath.Join(sub, sid+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))

	_, handler, _, ss := setupSessionsTestServer(t)
	handler.jsonl = jsonl.NewSessionReader()
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "big", WorkingDir: "/tmp/project"}))
	ctx := context.Background()
	timed := func(f func()) time.Duration { s := time.Now(); f(); return time.Since(s) }

	var mark transcriptMark
	cold := timed(func() { mark = handler.transcriptTotal(ctx, sid, "claude") })
	require.True(t, mark.ok)
	require.Equal(t, 2*turns, mark.total)

	idle := timed(func() { assert.False(t, mark.tookPrompt(handler, ctx, sid, "claude", "deploy the staging build")) })

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"type":"user","timestamp":"2026-01-01T00:00:02Z","message":{"content":"deploy the staging build"}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	var took bool
	appended := timed(func() { took = mark.tookPrompt(handler, ctx, sid, "claude", "deploy the staging build") })
	assert.True(t, took, "the appended message is found")
	t.Logf("cold read %v, idle poll %v, read after an append %v", cold, idle, appended)
	assert.Less(t, idle, cold/4+5*time.Millisecond, "a poll with nothing new does not parse the transcript again")
	assert.Less(t, appended, cold/4+5*time.Millisecond, "the read after the agent appends the message parses only the appended line")
}
