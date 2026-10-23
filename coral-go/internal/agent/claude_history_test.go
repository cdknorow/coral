package agent

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"runtime"
)

// claudeTranscriptLines is a mix of what a Claude transcript holds: string and block content,
// tool calls and results, a line of another shape, a line that isn't JSON, and a long line.
func claudeTranscriptLines(rng *rand.Rand, n int) []string {
	var lines []string
	for i := 0; i < n; i++ {
		ts := fmt.Sprintf(`"timestamp":"2026-10-16T06:%02d:%02dZ"`, i/60%60, i%60)
		var l string
		switch rng.Intn(9) {
		case 0:
			l = fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"question %d about Coralville"},%s}`, i, ts)
		case 1:
			l = fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":""},{"type":"text","text":"answer %d \u00e9"}]},%s}`, i, ts)
		case 2:
			l = fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"ls"}}]},%s}`, i, ts)
		case 3:
			l = fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t%d","content":"%s"}]},%s}`, i, strings.Repeat("x", rng.Intn(200000)), ts)
		case 4:
			l = fmt.Sprintf(`{"type":"system","subtype":"x",%s}`, ts)
		case 5:
			l = `not json at all`
		case 6:
			l = fmt.Sprintf(`{"type":"user","message":"a message that is a string",%s}`, ts)
		case 7:
			l = fmt.Sprintf(`{"type":"assistant","message":{"content":"%s"},%s}`, strings.Repeat("long ", 100), ts)
		default:
			l = ``
		}
		lines = append(lines, l)
	}
	return lines
}

// fromReference is the whole-file parse of fpath, as the indexer did before #3728.
func fromReference(t *testing.T, fpath string) *IndexedSession {
	t.Helper()
	info, err := os.Stat(fpath)
	require.NoError(t, err)
	got, err := parseClaudeSessions(fpath, float64(info.ModTime().Unix()))
	require.NoError(t, err)
	if len(got) == 0 {
		return nil
	}
	return &got[0]
}

func extractOne(t *testing.T, a *ClaudeAgent, base string, known map[string]float64) *IndexedSession {
	t.Helper()
	got, err := a.ExtractSessions(base, known)
	require.NoError(t, err)
	require.LessOrEqual(t, len(got), 1)
	if len(got) == 0 {
		return nil
	}
	return &got[0]
}

func sameSession(t *testing.T, want, got *IndexedSession, msg string) {
	t.Helper()
	if want == nil || got == nil {
		require.Equal(t, want == nil, got == nil, "%s: want %v, got %v", msg, want, got)
		return
	}
	g := *got
	g.FTSUnchanged = false
	assert.Equal(t, *want, g, msg)
}

// #3728: the incremental read gives what a whole-file parse gives, after every append, including
// appends that end mid-line and a last line with no newline.
func TestClaudeHistoryIncrementalMatchesAWholeRead(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		rng := rand.New(rand.NewSource(seed))
		base := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(base, "proj"), 0o755))
		fpath := filepath.Join(base, "proj", "sess.jsonl")
		data := strings.Join(claudeTranscriptLines(rng, 120), "\n")
		a := &ClaudeAgent{}
		for written := 0; written < len(data); {
			n := min(len(data)-written, 1+rng.Intn(60000))
			f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			require.NoError(t, err)
			_, err = f.WriteString(data[written : written+n])
			require.NoError(t, err)
			require.NoError(t, f.Close())
			written += n
			sameSession(t, fromReference(t, fpath), extractOne(t, a, base, nil), fmt.Sprintf("seed %d at %d bytes", seed, written))
		}
	}
}

// A pass reads only what the transcript gained: the cursor's offset is where the last pass
// stopped, and the bytes before it are not decoded again.
func TestClaudeHistoryReadsOnlyTheNewBytes(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "p"), 0o755))
	fpath := filepath.Join(base, "p", "s.jsonl")
	first := `{"type":"user","message":{"content":"hello Coralville"},"timestamp":"t1"}` + "\n"
	require.NoError(t, os.WriteFile(fpath, []byte(first), 0o600))
	a := &ClaudeAgent{}
	s := extractOne(t, a, base, nil)
	require.NotNil(t, s)
	require.Equal(t, int64(len(first)), a.hist.cursors[fpath].offset)

	// Garble the first line in place, without changing its last bytes: a pass that decoded it
	// again would lose its message.
	garbled := []byte(first)
	copy(garbled, "XXXXXXXXXX")
	more := `{"type":"assistant","message":{"content":"reply"},"timestamp":"t2"}` + "\n"
	require.NoError(t, os.WriteFile(fpath, append(garbled, more...), 0o600))
	s = extractOne(t, a, base, nil)
	require.NotNil(t, s)
	assert.Equal(t, 2, s.MessageCount, "the first line was not read again")
	assert.Equal(t, "t1", *s.FirstTimestamp)
	assert.Equal(t, "t2", *s.LastTimestamp)
	assert.Equal(t, int64(len(first)+len(more)), a.hist.cursors[fpath].offset)
}

// A transcript rewritten in place (its last read bytes changed), truncated, or replaced by
// another file is read again from the start.
func TestClaudeHistoryRereadsARewrittenFile(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "p"), 0o755))
	fpath := filepath.Join(base, "p", "s.jsonl")
	line := func(typ, text, ts string) string {
		return fmt.Sprintf(`{"type":%q,"message":{"content":%q},"timestamp":%q}`+"\n", typ, text, ts)
	}
	a := &ClaudeAgent{}
	write := func(s string) {
		require.NoError(t, os.WriteFile(fpath, []byte(s), 0o600))
	}
	write(line("user", "one", "a") + line("assistant", "two", "b"))
	sameSession(t, fromReference(t, fpath), extractOne(t, a, base, nil), "first read")

	write(line("user", "ONE", "c") + line("assistant", "TWO", "d") + line("user", "three", "e"))
	sameSession(t, fromReference(t, fpath), extractOne(t, a, base, nil), "rewritten, longer")

	write(line("user", "short", "f"))
	sameSession(t, fromReference(t, fpath), extractOne(t, a, base, nil), "truncated")

	other := filepath.Join(base, "other.jsonl")
	require.NoError(t, os.WriteFile(other, []byte(line("user", "short", "f")+line("assistant", "replaced", "g")), 0o600))
	require.NoError(t, os.Rename(other, fpath))
	sameSession(t, fromReference(t, fpath), extractOne(t, a, base, nil), "replaced by another file")
}

// A transcript with no message is remembered by its size and time, and read again only when it
// changes; the search body is marked unchanged only when it is and the index has it.
func TestClaudeHistoryEmptyFilesAndUnchangedBodies(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "p"), 0o755))
	fpath := filepath.Join(base, "p", "s.jsonl")
	meta := `{"type":"summary","summary":"x"}` + "\n"
	require.NoError(t, os.WriteFile(fpath, []byte(meta), 0o600))
	a := &ClaudeAgent{}
	assert.Nil(t, extractOne(t, a, base, nil))
	assert.Contains(t, a.hist.empty, fpath)
	// Not opened again while unchanged: an unreadable file would otherwise drop the entry.
	require.NoError(t, os.Chmod(fpath, 0o000))
	if f, err := os.Open(fpath); err == nil { // root ignores the mode
		f.Close()
	} else {
		assert.Nil(t, extractOne(t, a, base, nil))
		assert.Contains(t, a.hist.empty, fpath, "the unchanged file wasn't opened")
	}
	require.NoError(t, os.Chmod(fpath, 0o600))

	appendLine := func(s string) {
		f, err := os.OpenFile(fpath, os.O_WRONLY|os.O_APPEND, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(s + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	appendLine(`{"type":"user","message":{"content":"find Coralville"},"timestamp":"t"}`)
	s := extractOne(t, a, base, nil)
	require.NotNil(t, s)
	assert.NotContains(t, a.hist.empty, fpath)
	assert.False(t, s.FTSUnchanged, "a new body")

	appendLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"x","name":"Bash","input":{}}]},"timestamp":"u"}`)
	s = extractOne(t, a, base, nil)
	assert.False(t, s.FTSUnchanged, "the same body, but the index has none for this file")
	appendLine(`{"type":"system","timestamp":"v"}`)
	s = extractOne(t, a, base, map[string]float64{fpath: -1})
	assert.True(t, s.FTSUnchanged, "the same body, and the index has one")
	appendLine(`{"type":"assistant","message":{"content":"more words"},"timestamp":"w"}`)
	s = extractOne(t, a, base, map[string]float64{fpath: -1})
	assert.False(t, s.FTSUnchanged, "the body grew")
	assert.Contains(t, s.FTSBody, "more words")
}

// lowerCursorBounds sets the cursor bounds for one test and puts them back.
func lowerCursorBounds(t *testing.T, count, bytes int) {
	t.Helper()
	c, b := maxClaudeCursors, maxClaudeCursorBytes
	maxClaudeCursors, maxClaudeCursorBytes = count, bytes
	t.Cleanup(func() { maxClaudeCursors, maxClaudeCursorBytes = c, b })
}

// At most maxClaudeCursors cursors are kept, and a deleted transcript's are dropped.
func TestClaudeHistoryCursorsAreBounded(t *testing.T) {
	lowerCursorBounds(t, 64, maxClaudeCursorBytes)
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "p"), 0o755))
	for i := 0; i < maxClaudeCursors+10; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(base, "p", fmt.Sprintf("s%d.jsonl", i)), []byte(`{"type":"user","message":{"content":"x"}}`+"\n"), 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(base, "p", "empty.jsonl"), []byte("{}\n"), 0o600))
	a := &ClaudeAgent{}
	got, err := a.ExtractSessions(base, nil)
	require.NoError(t, err)
	assert.Len(t, got, maxClaudeCursors+10)
	assert.Len(t, a.hist.cursors, maxClaudeCursors)
	assert.Len(t, a.hist.empty, 1)
	require.NoError(t, os.RemoveAll(filepath.Join(base, "p")))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "p"), 0o755))
	_, err = a.ExtractSessions(base, nil)
	require.NoError(t, err)
	assert.Empty(t, a.hist.cursors)
	assert.Empty(t, a.hist.empty)
}

// The indexer's cost on a live transcript: a pass after a small append, against the whole-file
// parse it replaces (go test -bench ClaudeHistoryPass -benchmem ./internal/agent).
func BenchmarkClaudeHistoryPass(b *testing.B) {
	base := b.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "p"), 0o755); err != nil {
		b.Fatal(err)
	}
	fpath := filepath.Join(base, "p", "s.jsonl")
	rng := rand.New(rand.NewSource(1))
	var sb strings.Builder
	for sb.Len() < 30<<20 {
		sb.WriteString(strings.Join(claudeTranscriptLines(rng, 50), "\n"))
		sb.WriteString("\n")
	}
	if err := os.WriteFile(fpath, []byte(sb.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	next := `{"type":"assistant","message":{"content":"another reply"},"timestamp":"z"}` + "\n"
	grow := func() {
		f, _ := os.OpenFile(fpath, os.O_WRONLY|os.O_APPEND, 0o600)
		f.WriteString(next)
		f.Close()
		now := time.Now()
		os.Chtimes(fpath, now, now)
	}
	b.Run("whole-file", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			grow()
			if _, err := parseClaudeSessions(fpath, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("incremental", func(b *testing.B) {
		a := &ClaudeAgent{}
		if _, err := a.ExtractSessions(base, nil); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			grow()
			if _, err := a.ExtractSessions(base, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// writeLiveTranscripts makes n one-turn transcripts under base/p and returns a function appending a turn to each.
func writeLiveTranscripts(t *testing.T, base string, n int) (appendAll func(round int)) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(base, "p"), 0o755))
	path := func(i int) string { return filepath.Join(base, "p", fmt.Sprintf("s%03d.jsonl", i)) }
	line := func(typ, text string) string {
		return fmt.Sprintf(`{"type":%q,"timestamp":"2026-10-18T00:00:00Z","message":{"content":%q}}`+"\n", typ, text)
	}
	for i := 0; i < n; i++ {
		require.NoError(t, os.WriteFile(path(i), []byte(line("user", fmt.Sprintf("question %d", i))+line("assistant", "answer")), 0o600))
	}
	return func(round int) {
		for i := 0; i < n; i++ {
			f, err := os.OpenFile(path(i), os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = f.WriteString(line("assistant", fmt.Sprintf("more words, round %d", round)))
			require.NoError(t, err)
			require.NoError(t, f.Close())
		}
	}
}

// #4669: more live transcripts than the old 64 cursors are each read from where the last pass stopped. With the cursor count
// bound at 64 the least recently used cursor was always the next file of a pass, so every file was read whole every pass;
// the bytes bound keeps all of 150 small ones, and a pass after an append reads none from its first byte.
func TestClaudeHistoryReadsMoreLiveTranscriptsThanSixtyFourIncrementally(t *testing.T) {
	run := func(t *testing.T) int64 {
		base := t.TempDir()
		appendAll := writeLiveTranscripts(t, base, 150)
		a := &ClaudeAgent{}
		known := map[string]float64{}
		pass := func() {
			got, err := a.ExtractSessions(base, known)
			require.NoError(t, err)
			for _, s := range got {
				known[s.SourceFile] = -1 // indexed, but never equal to the file's mtime
			}
		}
		pass()
		first := a.hist.fullReads
		require.EqualValues(t, 150, first)
		appendAll(1)
		pass()
		appendAll(2)
		pass()
		return a.hist.fullReads - first
	}
	t.Run("default bounds", func(t *testing.T) {
		assert.Zero(t, run(t), "no transcript is read from its first byte again")
	})
	t.Run("the old count bound of 64", func(t *testing.T) {
		lowerCursorBounds(t, 64, maxClaudeCursorBytes)
		assert.Greater(t, run(t), int64(150), "what the bound did: nearly every file read whole, every pass")
	})
}

// The bytes bound evicts the least recently used cursors, and keeps the one just read.
func TestClaudeHistoryCursorsAreBoundedByBytes(t *testing.T) {
	base := t.TempDir()
	writeLiveTranscripts(t, base, 40)
	probe := &ClaudeAgent{}
	_, err := probe.ExtractSessions(base, nil)
	require.NoError(t, err)
	var w int
	for _, c := range probe.hist.cursors {
		w = c.weight()
		break
	}
	require.Greater(t, w, 0)
	lowerCursorBounds(t, 4096, 5*w)
	a := &ClaudeAgent{}
	got, err := a.ExtractSessions(base, nil)
	require.NoError(t, err)
	assert.Len(t, got, 40, "every transcript is still indexed")
	assert.LessOrEqual(t, len(a.hist.cursors), 5)
	assert.GreaterOrEqual(t, len(a.hist.cursors), 1)
}

// The summary is built by cursorSummary, and it must not share memory with the message it came from: the address of its first byte is
// outside the message's bytes, for a short and a long message. No heap numbers (those are the whole process's, see the next test), so
// the check is exact: a slice of the message (text[:200]) starts inside it and fails here every time.
func TestCursorSummaryDoesNotShareMemoryWithTheMessage(t *testing.T) {
	for _, size := range []int{300, 4096, 1 << 20} {
		msg := strings.Repeat("y ", size/2) // words: a run of token characters longer than the cut is dropped whole by redact.Cut, which is not what this test is about
		start := uintptr(unsafe.Pointer(unsafe.StringData(msg)))
		got := cursorSummary(msg)
		require.Len(t, got, 200)
		p := uintptr(unsafe.Pointer(unsafe.StringData(got)))
		assert.False(t, p >= start && p < start+uintptr(size), "a %d byte message: its summary starts inside the message's bytes (a slice, not a copy)", size)
	}
	short := strings.Repeat("z", 50)
	got := cursorSummary(short)
	assert.Equal(t, short, got)
	assert.NotEqual(t, uintptr(unsafe.Pointer(unsafe.StringData(short))), uintptr(unsafe.Pointer(unsafe.StringData(got))), "a short message is copied too")
}

// A cursor's summary is a copy of the first 200 bytes of a message, not a slice of the message: a slice keeps the whole message in memory, which
// weight() (the bytes bound) cannot see. Six cursors of one 8 MiB message each, search bodies dropped: only the summaries are left to hold memory.
func TestClaudeCursorSummaryDoesNotPinTheWholeMessage(t *testing.T) {
	const n, size = 6, 8 << 20
	heap := func() int64 {
		runtime.GC()
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return int64(m.HeapAlloc)
	}
	// What the six cursors keep, as the growth of the live heap. The heap is the process's, so another goroutine (an earlier test's worker
	// finishing, the runtime's own) can move it between the two readings, even below the first (a uint64 difference then wrapped to a huge
	// number: the windows-tests failure of v1.4.97's re-gate, #4890). So it is a signed difference, and the least of three tries: a slice
	// that pins its message holds 48 MiB every time, noise does not hold it three times.
	limit := int64(n * size / 4)
	measure := func() int64 {
		base := heap()
		cs := make([]*claudeCursor, n)
		for i := range cs {
			c := &claudeCursor{}
			c.add([]byte(`{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","content":"` + strings.Repeat("x ", size/2) + `"}}`))
			require.Len(t, c.summary, 200)
			c.fts = FTSBodyBuilder{} // the search body is counted by weight(); what is left is what weight() cannot see
			cs[i] = c
		}
		held := heap() - base
		runtime.KeepAlive(cs)
		return held
	}
	best := int64(1) << 62
	for try := 0; try < 3 && best >= limit; try++ {
		if held := measure(); held < best {
			best = held
		}
	}
	assert.Less(t, best, limit, "the summaries hold %d MiB: each pins its whole %d MiB message", best>>20, size>>20)
}

// Even a bound smaller than one cursor keeps the cursor just used (never none): the next pass then reads the newest file's new bytes, not the
// whole file; the cursor before it goes as soon as another file is used.
func TestClaudeHistoryKeepsTheCursorJustUsedWhateverTheBound(t *testing.T) {
	base := t.TempDir()
	writeLiveTranscripts(t, base, 5)
	lowerCursorBounds(t, 4096, 1)
	a := &ClaudeAgent{}
	got, err := a.ExtractSessions(base, nil)
	require.NoError(t, err)
	assert.Len(t, got, 5, "every transcript is still indexed")
	assert.Len(t, a.hist.cursors, 1, "one cursor is kept, the one used last")
}
