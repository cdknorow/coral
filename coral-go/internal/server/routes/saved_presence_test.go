package routes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #5802 (QA's measurement: 50 transcripts of 10 MB cost 650 ms on every Saved list): whether a key is in a session's transcript is kept while the transcript file is as it was.
type presenceRig struct {
	c       *savedPresence
	secret  []byte
	fp      map[string]string           // session -> fingerprint ("" = no file)
	readAt  map[string]string           // session -> the file the read comes from ("" = the stat'ed one)
	msgs    map[string][]map[string]any // session -> the transcript
	reads   []string
	fpCalls int
	clock   time.Time
	aborted bool // the fingerprint ran out of budget
}

func newPresenceRig() *presenceRig {
	r := &presenceRig{clock: time.Unix(1_000_000, 0), secret: []byte("s"), fp: map[string]string{}, readAt: map[string]string{}, msgs: map[string][]map[string]any{}}
	r.c = &savedPresence{
		now: func() time.Time { return r.clock },
		fingerprint: func(_ context.Context, sid, _ string) (string, string, bool, bool) {
			r.fpCalls++
			return "file-" + sid, r.fp[sid], r.fp[sid] != "", r.aborted
		},
		read: func(_ context.Context, sid, _ string) ([]map[string]any, string) {
			r.reads = append(r.reads, sid)
			if at := r.readAt[sid]; at != "" {
				return r.msgs[sid], at
			}
			return r.msgs[sid], "file-" + sid
		},
	}
	return r
}

func (r *presenceRig) set(sid, fp string, texts ...string) []string {
	r.fp[sid] = fp
	r.msgs[sid] = nil
	var keys []string
	for i, t := range texts {
		m := map[string]any{"type": "user", "timestamp": fmt.Sprintf("2026-10-21T10:00:%02dZ", i), "content": t}
		r.msgs[sid] = append(r.msgs[sid], m)
		keys = append(keys, messageKey(r.secret, m))
	}
	return keys
}

func (r *presenceRig) ask(sid string, keys ...string) (map[string]bool, bool) {
	found, reachable, known := r.c.check(context.Background(), r.secret, sid, "claude", keys)
	if !known {
		return nil, false
	}
	return found, reachable
}

func TestSavedPresenceIsKeptWhileTheTranscriptFileIsUnchanged(t *testing.T) {
	r := newPresenceRig()
	keys := r.set("s1", "p|10|1", "one", "two")
	absent := "00000000000000000000"
	// the key of a message the transcript will have after it grows: asked now, it is not there
	future := messageKey(r.secret, map[string]any{"type": "user", "timestamp": "2026-10-21T10:00:02Z", "content": "three"})
	got, ok := r.ask("s1", keys[0], keys[1], absent, future)
	require.True(t, ok)
	assert.Equal(t, map[string]bool{keys[0]: true, keys[1]: true, absent: false, future: false}, got)
	assert.Equal(t, []string{"s1"}, r.reads, "the first list reads the transcript once for all its keys")
	// a warm list: no read at all, the same answers (a message that is not in it too)
	got, ok = r.ask("s1", keys[0], keys[1], absent, future)
	require.True(t, ok)
	assert.Equal(t, map[string]bool{keys[0]: true, keys[1]: true, absent: false, future: false}, got)
	assert.Len(t, r.reads, 1, "a warm list does no read")
	// the file changed (size and time): the answers are made again, and a message that is there now is found
	more := r.set("s1", "p|99|2", "one", "two", "three")
	require.Equal(t, future, more[2])
	got, _ = r.ask("s1", keys[0], future, absent)
	assert.Equal(t, map[string]bool{keys[0]: true, future: true, absent: false}, got, "the message that was not there before is found: a stale answer would say no")
	assert.Len(t, r.reads, 2, "a changed file is read again")
	_, _ = r.ask("s1", keys[0], future, absent)
	assert.Len(t, r.reads, 2, "and kept again")
	// a transcript with nothing to stat is read as before, and nothing is kept
	r.fp["s2"], r.msgs["s2"] = "", nil
	_, ok = r.ask("s2", "k")
	assert.False(t, ok, "no transcript: not reachable")
	// the file went between the stat and the read: not reachable, nothing kept
	r.fp["s3"] = "p3|1|1"
	_, ok = r.ask("s3", "k")
	assert.False(t, ok)
	r.set("s3", "p3|1|1", "now there")
	_, ok = r.ask("s3", messageKey(r.secret, r.msgs["s3"][0]))
	assert.True(t, ok)
}

func TestSavedPresenceKeepsAtMostItsBound(t *testing.T) {
	r := newPresenceRig()
	var firstKey, lastKey string
	for i := 0; i < maxSavedPresenceEntries+20; i++ {
		sid := fmt.Sprintf("s%04d", i)
		keys := r.set(sid, "p|1|1", "m")
		if i == 0 {
			firstKey = keys[0]
		}
		lastKey = keys[0]
		_, ok := r.ask(sid, keys[0])
		require.True(t, ok)
	}
	assert.LessOrEqual(t, r.c.order.Len(), maxSavedPresenceEntries, "the bound holds")
	assert.LessOrEqual(t, len(r.c.entries), maxSavedPresenceEntries)
	n := len(r.reads)
	_, _ = r.ask("s0000", firstKey)
	assert.Len(t, r.reads, n+1, "the oldest answer was dropped: it is read again")
	_, _ = r.ask(fmt.Sprintf("s%04d", maxSavedPresenceEntries+19), lastKey)
	assert.Len(t, r.reads, n+1, "the newest answer is still kept")
}

// The real wiring: a Claude transcript on disk, found by the same fingerprint and read the way the history view reads it; an appended message is found once the file changed.
func TestSavedAgentPresenceReadsARealTranscriptAndSeesItGrow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	const sid = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee1"
	path := filepath.Join(dir, "proj", sid+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	line := func(text, ts string) string {
		return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":"` + text + `"}}` + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(line("first", "2026-10-21T10:00:00Z")), 0o644))
	h := &HistoryHandler{}
	ctx := context.Background()
	secret := messageKeySecretFor(nil)
	msgs, _ := h.sessionTranscript(ctx, sid)
	require.Len(t, msgs, 1)
	k1 := messageKey(secret, msgs[0])
	got, ok, _ := h.SavedAgentPresence(ctx, sid, "claude", []string{k1})
	require.True(t, ok)
	assert.Equal(t, map[string]bool{k1: true}, got)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(line("second", "2026-10-21T10:00:05Z"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	msgs, _ = h.sessionTranscript(ctx, sid)
	require.Len(t, msgs, 2)
	k2 := messageKey(secret, msgs[1])
	got, ok, _ = h.SavedAgentPresence(ctx, sid, "claude", []string{k1, k2})
	require.True(t, ok)
	assert.Equal(t, map[string]bool{k1: true, k2: true}, got, "the file changed: the new message is found")
	_, ok, _ = h.SavedAgentPresence(ctx, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeee2", "claude", []string{k1})
	assert.False(t, ok, "no transcript: not reachable")
}

// Security's LOW 1 on #5811: when the history view's read fell through to another file than the one stat'ed, the answer is true of a file the fingerprint does not watch, so it is not kept.
func TestSavedPresenceKeepsNothingReadFromAnotherFileThanTheOneWatched(t *testing.T) {
	r := newPresenceRig()
	keys := r.set("s1", "p|10|1", "one")
	r.readAt["s1"] = "another-file"
	for i := 0; i < 2; i++ {
		got, ok := r.ask("s1", keys[0])
		require.True(t, ok)
		assert.Equal(t, map[string]bool{keys[0]: true}, got, "still answered")
	}
	assert.Len(t, r.reads, 2, "and read each time")
	r.readAt["s1"] = ""
	_, _ = r.ask("s1", keys[0])
	_, _ = r.ask("s1", keys[0])
	assert.Len(t, r.reads, 3, "kept once the read is of the watched file")
}

// #5820: "no transcript here" is believed for a minute per session, so a list of bookmarks of sessions that have none does not look for them again on every visit; a cut-off
// is not "no transcript" and is never kept; once the transcript appears, or the minute is over, it is looked for again.
func TestSavedPresenceBelievesNoTranscriptForAMinute(t *testing.T) {
	r := newPresenceRig()
	_, ok := r.ask("gone-session", "k")
	assert.False(t, ok)
	assert.Equal(t, 1, r.fpCalls)
	_, ok = r.ask("gone-session", "k")
	assert.False(t, ok)
	assert.Equal(t, 1, r.fpCalls, "believed: not looked for again")
	r.clock = r.clock.Add(59 * time.Second)
	_, _ = r.ask("gone-session", "k")
	assert.Equal(t, 1, r.fpCalls, "still inside the minute")
	r.clock = r.clock.Add(2 * time.Second)
	_, _ = r.ask("gone-session", "k")
	assert.Equal(t, 2, r.fpCalls, "after the minute it is looked for again")
	// a lookup that is cut off (or that only a scan could answer) says nothing, and is not tried again for a minute: the next lists get on to the other bookmarks
	r.aborted = true
	for i := 0; i < 2; i++ {
		found, reachable, known := r.c.check(context.Background(), r.secret, "slow-session", "codex", []string{"k"})
		assert.False(t, known, "not told")
		assert.False(t, reachable)
		assert.Nil(t, found)
	}
	assert.Equal(t, 3, r.fpCalls, "tried once, then believed not told")
	r.clock = r.clock.Add(61 * time.Second)
	_, _, _ = r.c.check(context.Background(), r.secret, "slow-session", "codex", []string{"k"})
	assert.Equal(t, 4, r.fpCalls, "after the minute it is tried again")
	r.aborted = false
	keys := r.set("slow-session", "p|1|1", "now there")
	got, ok := r.ask("slow-session", keys[0])
	require.True(t, ok)
	assert.Equal(t, map[string]bool{keys[0]: true}, got)
	// a transcript that appears within the minute is found once the belief is over; one asked about before it existed was never believed absent by a cut-off
}
