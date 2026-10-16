package jsonl

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeTranscript writes a Claude transcript of about size bytes: prompts, tool calls with large
// inputs, their results (named from the call), replies, and a few lines that aren't JSON.
func writeTranscript(t testing.TB, path string, size int) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	big := strings.Repeat("x", 6000)
	for i, written := 0, 0; written < size; i++ {
		var line string
		switch i % 5 {
		case 0:
			line = fmt.Sprintf(`{"type":"user","timestamp":"2026-10-15T10:00:00Z","message":{"role":"user","content":"please fix test number %d"}}`, i)
		case 1:
			line = fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-15T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"step %d"},{"type":"tool_use","id":"tu_%d","name":"Bash","input":{"command":"go test %s"}}]}}`, i, i, big[:800])
		case 2:
			line = fmt.Sprintf(`{"type":"user","timestamp":"2026-10-15T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_%d","content":"%s"}]}}`, i-1, big)
		case 3:
			line = fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-15T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"done %d %s"}]}}`, i, big[:300])
		default:
			line = "   not json " + fmt.Sprint(i)
		}
		n, _ := f.WriteString(line + "\n")
		written += n
	}
}

func readAll(t testing.TB, path string) []map[string]any {
	r := NewSessionReader()
	c := r.getOrCreateSessionCache("s")
	c.path, c.pathVerified = path, true
	msgs, _ := r.ReadAllMessagesForLive("s", "", "claude")
	return msgs
}

// #3710: a large read is decoded in parallel; the messages (and the tool names their results
// take from earlier calls) are exactly those of a sequential decode.
func TestParallelDecodeGivesTheSameMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path, 8<<20)
	parallel := readAll(t, path)
	old := parallelDecodeMin
	parallelDecodeMin = 1 << 62
	t.Cleanup(func() { parallelDecodeMin = old })
	sequential := readAll(t, path)
	require.NotEmpty(t, sequential)
	assert.True(t, reflect.DeepEqual(sequential, parallel), "parallel %d messages, sequential %d", len(parallel), len(sequential))
	var named int
	for _, m := range parallel {
		if m["tool_name"] == "Bash" {
			named++
		}
	}
	assert.Positive(t, named, "tool results keep their tool's name")
}

// #3710: the chat cache follows the file: appended lines are added; a truncated or replaced
// transcript (another file, or other first bytes) is read again from the start, never mixed
// with the old messages.
func TestChatCacheFollowsTruncationAndReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	line := func(text string) string {
		return `{"type":"assistant","timestamp":"2026-10-15T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(line("one")+line("two")+line("three")), 0o600))
	r := NewSessionReader()
	c := r.getOrCreateSessionCache("s")
	c.path, c.pathVerified = path, true
	texts := func() []string {
		msgs, _ := r.ReadAllMessagesForLive("s", "", "claude")
		var out []string
		for _, m := range msgs {
			out = append(out, fmt.Sprint(m["text"]))
		}
		return out
	}
	assert.Equal(t, []string{"one", "two", "three"}, texts())

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	f.WriteString(line("four"))
	f.Close()
	assert.Equal(t, []string{"one", "two", "three", "four"}, texts(), "appended")

	require.NoError(t, os.WriteFile(path, []byte(line("fresh")), 0o600)) // truncated and rewritten
	assert.Equal(t, []string{"fresh"}, texts(), "truncated: read again")

	other := filepath.Join(dir, "other.jsonl")
	require.NoError(t, os.WriteFile(other, []byte(line("fresh")+line("replaced")), 0o600))
	require.NoError(t, os.Rename(other, path)) // a new file in its place
	assert.Equal(t, []string{"fresh", "replaced"}, texts(), "replaced: read again")
}

// #3710: tabs polling the same session at once share one read: the transcript is decoded once,
// and each gets every message.
func TestConcurrentChatReadersShareOneRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, path, 4<<20)
	r := NewSessionReader()
	c := r.getOrCreateSessionCache("s")
	c.path, c.pathVerified = path, true
	before := decodedLines.Load()
	var wg sync.WaitGroup
	totals := make([]int, 8)
	for i := range totals {
		wg.Add(1)
		go func() { defer wg.Done(); _, totals[i] = r.ReadAllMessagesForLive("s", "", "claude") }()
	}
	wg.Wait()
	lines := strings.Count(mustRead(t, path), "\n")
	assert.EqualValues(t, lines, decodedLines.Load()-before, "decoded once, not once per reader")
	for _, n := range totals {
		assert.Equal(t, totals[0], n)
	}
	assert.Positive(t, totals[0])
}

func mustRead(t testing.TB, path string) string {
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// BenchmarkChatRead: a 100 MB Claude transcript read cold (the first page after a restart or an
// eviction) and then polled unchanged and after one appended line (#3710).
func BenchmarkChatRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "s.jsonl")
	writeTranscript(b, path, 100<<20)
	b.Run("cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			readAll(b, path)
		}
	})
	r := NewSessionReader()
	c := r.getOrCreateSessionCache("s")
	c.path, c.pathVerified = path, true
	r.ReadAllMessagesForLive("s", "", "claude")
	b.Run("poll-unchanged", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			r.ReadAllMessagesForLive("s", "", "claude")
		}
	})
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	b.Run("poll-append", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			f.WriteString(`{"type":"assistant","timestamp":"2026-10-15T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"more"}]}}` + "\n")
			r.ReadAllMessagesForLive("s", "", "claude")
		}
	})
}
