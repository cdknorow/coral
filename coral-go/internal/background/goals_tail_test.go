package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/jsonl"
)

func goalUserLine(text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"2026-10-16T10:00:00Z","message":{"role":"user","content":%q}}`, text) + "\n"
}

func goalReplyLine(text string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-16T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, text) + "\n"
}

// A tool result is a "user" line with structured content; it is never a request.
func goalToolResultLine(id, body string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":"2026-10-16T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`, id, body) + "\n"
}

func fullInput(t *testing.T, path string) goalInput {
	t.Helper()
	messages, _, err := jsonl.ReadFromContext(context.Background(), path, "claude", 0)
	require.NoError(t, err)
	return condenseForGoal(messages)
}

func tailInput(t *testing.T, path string) (goalInput, *goalTranscript) {
	t.Helper()
	var tr goalTranscript
	require.NoError(t, tr.read(context.Background(), path, "claude"))
	return tr.input(), &tr
}

// #3715 (Security's review points): goals' first read scans back from the end, and gets exactly
// the input a full read gives: windows that start inside a line or inside a multi-byte rune,
// a tool result that looks like a user line, an invalid-UTF-8 line, and a transcript with fewer
// requests than wanted (the scan grows to the whole file).
func TestGoalTailReadMatchesAFullRead(t *testing.T) {
	restore := jsonl.SetTailChunkForTest(600) // small windows: many boundaries
	defer restore()
	filler := strings.Repeat("\u00e9\u6f22\U0001F642", 40) // multi-byte runes everywhere a window may start
	cases := map[string]string{
		"ordinary": goalUserLine("first request") + goalReplyLine("on it "+filler) + goalToolResultLine("t1", filler) +
			goalUserLine("second request "+filler) + goalReplyLine("still working "+filler) + goalToolResultLine("t2", filler) +
			goalUserLine("third request") + goalReplyLine("done "+filler) + goalToolResultLine("t3", filler+filler),
		"tool result shaped like a request": goalUserLine("real request one") + goalReplyLine("ok") +
			goalUserLine("real request two") + goalReplyLine("reply "+filler) +
			goalToolResultLine("t9", "please fix test number 7") + goalToolResultLine("t10", filler+filler+filler),
		"invalid UTF-8 line": goalUserLine("request A") + goalReplyLine("reply A") + "{\"type\":\"user\",\"x\":\"\xff\xfe\"}\n" +
			goalUserLine("request B "+filler) + "not json at all \xc3\x28\n" + goalReplyLine("reply B "+filler),
		"fewer requests than wanted": goalReplyLine("hello "+filler) + goalUserLine("only request "+filler) + goalToolResultLine("t1", filler+filler+filler),
		"no reply yet":               goalUserLine("one") + goalUserLine("two "+filler+filler),
		"empty":                      "",
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "s.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
		got, tr := tailInput(t, path)
		assert.Equal(t, fullInput(t, path), got, name)
		assert.EqualValues(t, strings.LastIndexByte(content, '\n')+1, tr.offset, "%s: the next read starts after the last complete line", name)
	}
}

// After the tail read, appends are read forward and update the input; a cancelled read changes
// nothing.
func TestGoalTranscriptFollowsAppends(t *testing.T) {
	restore := jsonl.SetTailChunkForTest(256)
	defer restore()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(goalUserLine("one")+goalReplyLine("r1")+goalUserLine("two")+goalReplyLine("r2")+strings.Repeat(goalToolResultLine("t", "xxxxxxxx"), 20)), 0o600))
	in, tr := tailInput(t, path)
	assert.Equal(t, goalInput{Earlier: "one", Request: "two", Response: "r2", HasAssistant: true}, in)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = f.WriteString(goalUserLine("three") + goalReplyLine("r3"))
	require.NoError(t, err)
	f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := *tr
	assert.NoError(t, tr.read(ctx, path, "claude"), "a small read takes no decode slot, so a cancelled context doesn't stop it")
	require.NoError(t, tr.read(context.Background(), path, "claude"))
	assert.Equal(t, goalInput{Earlier: "two", Request: "three", Response: "r3", HasAssistant: true}, tr.input())
	assert.Greater(t, tr.offset, before.offset)
}

// #3847: the tail read stops at its bound (8 MiB; 1 KB here) even when the window holds no two
// requests, instead of decoding the whole transcript at once: the requests before the bound are
// not seen, and the next read still starts after the last complete line.
func TestGoalTailReadIsBounded(t *testing.T) {
	defer jsonl.SetTailChunkForTest(256)()
	defer jsonl.SetTailMaxForTest(1024)()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	content := goalUserLine("one") + goalReplyLine("r1") + goalUserLine("two") + strings.Repeat(goalToolResultLine("t", strings.Repeat("x", 100)), 100)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	in, tr := tailInput(t, path)
	assert.Empty(t, in.Earlier, "past the bound")
	assert.EqualValues(t, strings.LastIndexByte(content, '\n')+1, tr.offset)
}
