package jsonl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkpointRig(t *testing.T, agentType string, turns int) (r *SessionReader, sid, path string, want []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	sid = "00000000-0000-4000-8000-000000004630"
	fixture := claudeFixture
	if agentType == "codex" {
		fixture = codexFixture
		home := filepath.Join(dir, ".codex")
		t.Setenv("CODEX_HOME", home)
		path = filepath.Join(home, "sessions", "2026", "10", "18", "rollout-2026-10-18T10-00-00-"+sid+".jsonl")
	} else {
		t.Setenv("CLAUDE_PROJECTS_DIR", dir)
		path = filepath.Join(dir, "proj", sid+".jsonl")
	}
	writeWindowFixture(t, path, fixture(turns))
	return NewSessionReader(), sid, path, referenceMessages(t, path, agentType)
}

// An old page is read from a checkpoint near it, not from the file's start, and what it reads is the same
// message for every position (numbering across a Codex duplicate merged at a chunk edge included).
func TestOldPageReadsFromANearbyCheckpointAndSameMessages(t *testing.T) {
	for _, agentType := range []string{"claude", "codex"} {
		t.Run(agentType, func(t *testing.T) {
			defer SetTranscriptBoundsForTest(40, 1<<30, 700, 8)() // chunks of 700 bytes: a checkpoint every 350
			r, sid, path, want := checkpointRig(t, agentType, 300)
			ctx := context.Background()
			tv := r.ReadTranscriptContext(ctx, sid, "", agentType)
			require.Equal(t, len(want), tv.Total)
			require.Greater(t, tv.Base, 600, "most of the transcript is before the window")
			ck := r.cache[sid].ckpts
			require.Greater(t, len(ck), 10, "checkpoints were recorded")
			for i := 1; i < len(ck); i++ {
				require.Greater(t, ck[i].msgs, ck[i-1].msgs)
				require.GreaterOrEqual(t, ck[i].offset-ck[i-1].offset, checkpointSpacing())
			}

			// Every position gives the same messages as the whole-file parse.
			for from := 0; from < tv.Base; from += 13 {
				to := min(from+9, tv.Total)
				assertSameMessages(t, want[from:to], tv.Slice(ctx, from, to), fmt.Sprintf("%s from %d", agentType, from))
			}
			assertSameMessages(t, want, tv.All(ctx), "everything")

			// And it reads about a few checkpoints' worth of the file, not the prefix before it.
			info, err := os.Stat(path)
			require.NoError(t, err)
			oldReadBytes.Store(0)
			tv.Slice(ctx, tv.Base-30, tv.Base-10)
			got := oldReadBytes.Load()
			assert.Less(t, got, info.Size()/5, "an old page near the window reads a small part of the file (read %d of %d)", got, info.Size())
			oldReadBytes.Store(0)
			tv.Slice(ctx, 0, 5)
			assert.Less(t, oldReadBytes.Load(), info.Size()/5, "and one at the start reads only the start")
		})
	}
}

// A replaced transcript forgets its checkpoints with its messages.
func TestCheckpointsAreForgottenWhenTheFileIsReplaced(t *testing.T) {
	defer SetTranscriptBoundsForTest(10, 1<<30, 700, 4096)()
	r, sid, path, _ := checkpointRig(t, "claude", 60)
	ctx := context.Background()
	r.ReadTranscriptContext(ctx, sid, "", "claude")
	require.NotEmpty(t, r.cache[sid].ckpts)
	writeWindowFixture(t, path, claudeFixture(2))
	tv := r.ReadTranscriptContext(ctx, sid, "", "claude")
	assert.Equal(t, 0, tv.Base)
	info, err := os.Stat(path)
	require.NoError(t, err)
	for _, c := range r.cache[sid].ckpts { // only the new file's: none points past it or counts messages it does not have
		assert.Less(t, c.offset, info.Size())
		assert.Less(t, c.msgs, tv.Total)
	}
}

// One old-page read per file at a time: concurrent readers wait their turn and each still gets its page.
func TestOldPageReadsOnOneFileRunOneAtATime(t *testing.T) {
	defer SetTranscriptBoundsForTest(40, 1<<30, 700, 4096)()
	r, sid, _, want := checkpointRig(t, "claude", 200)
	ctx := context.Background()
	tv := r.ReadTranscriptContext(ctx, sid, "", "claude")
	require.Greater(t, tv.Base, 100)
	oldReadPeak.Store(0)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			from := 20 + i*40
			assertSameMessages(t, want[from:from+30], tv.Slice(ctx, from, from+30), fmt.Sprint("reader ", i))
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 1, oldReadPeak.Load(), "never two at once on one file")
	assert.EqualValues(t, 0, oldReadActive.Load())
}

// A reader that waits for the file's turn gives up with its context.
func TestOldPageReadGivesUpWithItsContext(t *testing.T) {
	defer SetTranscriptBoundsForTest(40, 1<<30, 700, 4096)()
	r, sid, path, _ := checkpointRig(t, "claude", 100)
	tv := r.ReadTranscriptContext(context.Background(), sid, "", "claude")
	require.Greater(t, tv.Base, 50)
	hold, ok := lockOldRead(context.Background(), tv.parts[0].path)
	require.True(t, ok)
	_ = path
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := tv.Slice(ctx, 0, 5)
	assert.Less(t, time.Since(start), 2*time.Second, "it did not wait for ever")
	assert.Empty(t, got, "and read nothing")
	hold()
	assert.Len(t, tv.Slice(context.Background(), 0, 5), 5, "once the file is free it reads")
}

// With one line to a chunk, a checkpoint falls between the two lines of every Codex duplicate (an event and a
// response item with the same text, merged into one message): the numbering from any checkpoint still agrees
// with the whole-file parse, because the checkpoint remembers the message just before it.
func TestCheckpointBetweenACodexDuplicatePairKeepsTheNumbering(t *testing.T) {
	defer SetTranscriptBoundsForTest(6, 1<<30, 1, 4096)() // a 1-byte chunk is one whole line; a checkpoint at every line
	r, sid, _, want := checkpointRig(t, "codex", 40)
	ctx := context.Background()
	tv := r.ReadTranscriptContext(ctx, sid, "", "codex")
	require.Equal(t, len(want), tv.Total)
	require.Greater(t, tv.Base, 60)
	require.Greater(t, len(r.cache[sid].ckpts), 100, "a checkpoint between the lines")
	for from := 0; from < tv.Base; from++ {
		to := min(from+4, tv.Total)
		assertSameMessages(t, want[from:to], tv.Slice(ctx, from, to), fmt.Sprintf("from %d", from))
	}
}

// Reads of a line at a time (a poll after each append) do not make a checkpoint per line: they are a spacing apart.
func TestCheckpointsAreASpacingApartWhateverTheReadSize(t *testing.T) {
	defer SetTranscriptBoundsForTest(40, 1<<30, 700, 4096)()
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-4000-8000-000000004631"
	path := filepath.Join(dir, "proj", sid+".jsonl")
	writeWindowFixture(t, path, claudeFixture(1))
	r := NewSessionReader()
	ctx := context.Background()
	r.ReadTranscriptContext(ctx, sid, "", "claude")
	oneLine := strings.SplitAfter(claudeFixture(1), "\n")[0] // about 130 bytes, less than a spacing
	for i := 0; i < 120; i++ {
		appendTo(t, path, oneLine)
		r.ReadTranscriptContext(ctx, sid, "", "claude") // each read takes one small chunk
	}
	info, err := os.Stat(path)
	require.NoError(t, err)
	ck := r.cache[sid].ckpts
	assert.LessOrEqual(t, int64(len(ck)), info.Size()/checkpointSpacing()+1, "about one per spacing, not one per read")
	for i := 1; i < len(ck); i++ {
		assert.GreaterOrEqual(t, ck[i].offset-ck[i-1].offset, checkpointSpacing())
	}
	want := referenceMessages(t, path, "claude")
	assertSameMessages(t, want, r.ReadTranscriptContext(ctx, sid, "", "claude").All(ctx), "and the pages read from them agree")
}
