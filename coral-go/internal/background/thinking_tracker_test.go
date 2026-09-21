package background

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

	"github.com/cdknorow/coral/internal/store"
)

// Transcript entry builders. Seconds are offsets from a fixed base time.
var thinkingBase = time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)

func tsAt(sec float64) string {
	return thinkingBase.Add(time.Duration(sec * float64(time.Second))).Format(time.RFC3339Nano)
}

func userLine(sec float64) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u-%v","timestamp":%q,"message":{"content":[{"type":"tool_result"}]}}`, sec, tsAt(sec))
}

func assistantLine(sec float64, request, block string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a-%v","timestamp":%q,"requestId":%q,"message":{"id":"msg","content":[{"type":%q}]}}`,
		sec, tsAt(sec), request, block)
}

func feedAll(s *thinkingScanner, lines ...string) []thinkingTurn {
	var turns []thinkingTurn
	for _, l := range lines {
		turns = append(turns, s.Feed([]byte(l))...)
	}
	return turns
}

func TestThinkingScanner_TurnRunsFromPrecedingEntryToLastThinkingBlock(t *testing.T) {
	var s thinkingScanner
	turns := feedAll(&s,
		userLine(0),
		assistantLine(10, "req-1", "thinking"),
		assistantLine(10.5, "req-1", "thinking"),
		assistantLine(19, "req-1", "tool_use"),
	)
	require.Len(t, turns, 1)
	assert.Equal(t, "a-10", turns[0].RefID, "identified by the first thinking entry")
	assert.Equal(t, 10500*time.Millisecond, turns[0].Duration(), "tool_use generation time is not thinking")
}

func TestThinkingScanner_PendingUntilSomethingFollows(t *testing.T) {
	var s thinkingScanner
	assert.Empty(t, feedAll(&s, userLine(0), assistantLine(4, "req-1", "thinking")), "the turn may still be growing")
	turns := feedAll(&s, assistantLine(6, "req-1", "text"))
	require.Len(t, turns, 1)
	assert.Equal(t, 4*time.Second, turns[0].Duration())
}

func TestThinkingScanner_InterleavedThinkingStartsAfterPreviousBlock(t *testing.T) {
	var s thinkingScanner
	turns := feedAll(&s,
		userLine(0),
		assistantLine(3, "req-1", "thinking"),
		assistantLine(5, "req-1", "text"),
		assistantLine(9, "req-1", "thinking"),
		assistantLine(12, "req-1", "tool_use"),
	)
	require.Len(t, turns, 2)
	assert.Equal(t, 3*time.Second, turns[0].Duration())
	assert.Equal(t, 4*time.Second, turns[1].Duration(), "measured from the text block, not the request start")
}

func TestThinkingScanner_ThinkingOnlyResponseClosesOnNextRequestOrUser(t *testing.T) {
	var s thinkingScanner
	turns := feedAll(&s,
		userLine(0),
		assistantLine(2, "req-1", "thinking"),
		assistantLine(7, "req-2", "thinking"),
		userLine(8),
	)
	require.Len(t, turns, 2)
	assert.Equal(t, "a-2", turns[0].RefID)
	assert.Equal(t, "a-7", turns[1].RefID)
}

func TestThinkingScanner_IgnoresSidechainsOtherTypesAndGarbage(t *testing.T) {
	var s thinkingScanner
	sidechain := strings.Replace(assistantLine(1, "sub", "thinking"), `"type":"assistant"`, `"type":"assistant","isSidechain":true`, 1)
	turns := feedAll(&s,
		userLine(0),
		sidechain,
		`{"type":"attachment","timestamp":"`+tsAt(1)+`"}`,
		`not json`,
		``,
		assistantLine(2, "req-1", "tool_use"),
	)
	assert.Empty(t, turns)
}

func TestTranscriptTail_ReadsOnlyAppendedCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write := func(s string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, err = f.WriteString(s)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	tail := &transcriptTail{path: path}

	closing := assistantLine(9, "req-1", "tool_use")
	write(userLine(0) + "\n" + assistantLine(5, "req-1", "thinking") + "\n" + closing[:20])
	turns, err := tail.read()
	require.NoError(t, err)
	assert.Empty(t, turns, "the closing entry is only half written")

	write(closing[20:] + "\n")
	turns, err = tail.read()
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, 5*time.Second, turns[0].Duration())

	turns, err = tail.read()
	require.NoError(t, err)
	assert.Empty(t, turns, "nothing new")
}

func TestThinkingTracker_RecordsTurnsOnceInTheActivityStream(t *testing.T) {
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	ts := store.NewTaskStore(db)
	ctx := context.Background()
	const sid = "00000000-0000-0000-0000-0000000000a1"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir(),
	}))

	path := writeTestJSONL(t, strings.Join([]string{
		userLine(0), assistantLine(7.25, "req-1", "thinking"), assistantLine(9, "req-1", "tool_use"),
	}, "\n")+"\n")

	newTracker := func() *ThinkingTracker {
		tr := NewThinkingTracker(ss, ts, time.Hour)
		tr.cutoff = thinkingBase.Add(-time.Minute)
		tr.resolvePath = func(string, string) string { return path }
		return tr
	}
	newTracker().pollOnce(ctx)
	// A restarted tracker re-reads the transcript from the top.
	newTracker().pollOnce(ctx)

	sidCopy := sid
	events, err := ts.ListAgentEvents(ctx, "", 50, &sidCopy)
	require.NoError(t, err)
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, ThinkingEventType, ev.EventType)
	assert.Equal(t, "coral-go", ev.AgentName)
	require.NotNil(t, ev.DurationMs)
	assert.Equal(t, int64(7250), *ev.DurationMs)
	assert.Equal(t, thinkingBase.Add(7250*time.Millisecond).Format(store.ISOFormat), ev.CreatedAt, "sorted where it happened, not when it was read")
}

func TestThinkingTracker_SkipsHistoryOlderThanTheBackfillWindow(t *testing.T) {
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	ts := store.NewTaskStore(db)
	ctx := context.Background()
	const sid = "00000000-0000-0000-0000-0000000000a2"
	require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "coral-go", WorkingDir: t.TempDir(),
	}))
	path := writeTestJSONL(t, strings.Join([]string{
		userLine(0), assistantLine(5, "req-1", "thinking"), assistantLine(6, "req-1", "text"),
	}, "\n")+"\n")

	tr := NewThinkingTracker(ss, ts, time.Hour) // cutoff is ~now, the transcript is from 2026-09-21 08:00
	tr.cutoff = thinkingBase.Add(time.Hour)
	tr.resolvePath = func(string, string) string { return path }
	tr.pollOnce(ctx)

	sidCopy := sid
	events, err := ts.ListAgentEvents(ctx, "", 50, &sidCopy)
	require.NoError(t, err)
	assert.Empty(t, events)
}
