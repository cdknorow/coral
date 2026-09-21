package background

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
	"github.com/cdknorow/coral/internal/store"
)

// ThinkingEventType is the agent_events type of a thinking turn.
const ThinkingEventType = "thinking"

// maxThinkingDuration discards turns whose start could not be placed (a
// boundary entry missing from the transcript) rather than report hours.
const maxThinkingDuration = 30 * time.Minute

// thinkingBackfillWindow is how far before the tracker started a thinking turn
// may have ended and still be recorded. Older turns in a transcript that is
// read for the first time are history, not activity.
const thinkingBackfillWindow = 2 * time.Minute

// thinkingTurn is one stretch of model thinking found in a Claude transcript.
type thinkingTurn struct {
	// RefID is the transcript uuid of the turn's first thinking entry.
	RefID string
	// Start is when the model was handed the turn: the last transcript entry
	// before the thinking that is not itself thinking (the user prompt, a tool
	// result, or the previous block of the same response).
	Start time.Time
	// End is when the last thinking block of the turn finished.
	End time.Time
}

func (t thinkingTurn) Duration() time.Duration { return t.End.Sub(t.Start) }

// thinkingScanner walks transcript lines in order and yields thinking turns.
//
// Claude Code fires no hook for thinking, but it writes one transcript entry
// per content block, stamped when the block finished. A turn therefore runs
// from the entry before the thinking to the last consecutive thinking entry,
// and is complete once anything else follows it.
type thinkingScanner struct {
	boundary time.Time
	pending  *thinkingTurn
	request  string
}

type transcriptEntry struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	Timestamp   string `json:"timestamp"`
	RequestID   string `json:"requestId"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// Feed consumes one transcript line and returns the turns it completed.
func (s *thinkingScanner) Feed(line []byte) []thinkingTurn {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil
	}
	var e transcriptEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return nil
	}
	if e.IsSidechain || (e.Type != "user" && e.Type != "assistant") {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
	if err != nil {
		return nil
	}

	var done []thinkingTurn
	flush := func() {
		if s.pending != nil {
			done = append(done, *s.pending)
			s.pending = nil
		}
	}

	if e.Type == "user" {
		flush()
		s.boundary = ts
		return done
	}

	request := e.RequestID
	if request == "" {
		request = e.Message.ID
	}
	if request != s.request {
		// A response that was all thinking never got a closing block.
		flush()
		s.request = request
	}

	thinking, other := blockKinds(e.Message.Content)
	if thinking {
		if s.pending == nil {
			s.pending = &thinkingTurn{RefID: e.UUID, Start: s.boundary, End: ts}
		} else {
			s.pending.End = ts
		}
	}
	if other {
		flush()
		s.boundary = ts
	}
	return done
}

// blockKinds reports whether an assistant message holds thinking blocks and
// whether it holds any other kind (text, tool_use).
func blockKinds(content json.RawMessage) (thinking, other bool) {
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return false, len(content) > 0
	}
	for _, b := range blocks {
		if b.Type == "thinking" || b.Type == "redacted_thinking" {
			thinking = true
		} else {
			other = true
		}
	}
	return thinking, other
}

// ThinkingTracker tails the transcripts of live Claude sessions and records
// each thinking turn, with its duration, in the session's activity stream.
type ThinkingTracker struct {
	sessionStore *store.SessionStore
	taskStore    *store.TaskStore
	interval     time.Duration
	logger       *slog.Logger
	// Turns that ended before this are not recorded (see thinkingBackfillWindow).
	cutoff time.Time
	// resolvePath is swapped out in tests.
	resolvePath func(sessionID, workingDir string) string

	tails map[string]*transcriptTail // by session id
}

// transcriptTail is the read position in one session's transcript.
type transcriptTail struct {
	path    string
	offset  int64
	scanner thinkingScanner
}

// NewThinkingTracker creates a ThinkingTracker.
func NewThinkingTracker(ss *store.SessionStore, ts *store.TaskStore, interval time.Duration) *ThinkingTracker {
	return &ThinkingTracker{
		sessionStore: ss,
		taskStore:    ts,
		interval:     interval,
		logger:       slog.Default().With("service", "thinking_tracker"),
		cutoff:       time.Now().Add(-thinkingBackfillWindow),
		resolvePath:  resolveClaudeTranscriptPath,
		tails:        make(map[string]*transcriptTail),
	}
}

// Run starts the polling loop.
func (t *ThinkingTracker) Run(ctx context.Context) error {
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			t.pollOnce(ctx)
		}
	}
}

func (t *ThinkingTracker) pollOnce(ctx context.Context) {
	sessions, err := t.sessionStore.GetAllLiveSessions(ctx)
	if err != nil {
		t.logger.Error("failed to list live sessions", "error", err)
		return
	}

	live := make(map[string]bool, len(sessions))
	for i := range sessions {
		ls := &sessions[i]
		if ls.AgentType != at.Claude {
			continue
		}
		live[ls.SessionID] = true
		if ls.IsSleeping == 1 {
			continue
		}
		t.pollSession(ctx, ls)
	}
	for id := range t.tails {
		if !live[id] {
			delete(t.tails, id)
		}
	}
}

func (t *ThinkingTracker) pollSession(ctx context.Context, ls *store.LiveSession) {
	tail, ok := t.tails[ls.SessionID]
	if !ok {
		// The transcript appears only once the agent has written to it.
		path := t.resolvePath(ls.SessionID, ls.WorkingDir)
		if path == "" {
			return
		}
		tail = &transcriptTail{path: path}
		t.tails[ls.SessionID] = tail
	}

	turns, err := tail.read()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.logger.Error("failed to read transcript", "session_id", ls.SessionID, "error", err)
		}
		return
	}
	for _, turn := range turns {
		t.record(ctx, ls, turn)
	}
}

// read returns the thinking turns completed by the lines appended since the
// last call. A trailing partial line is left for the next call.
func (tl *transcriptTail) read() ([]thinkingTurn, error) {
	f, err := os.Open(tl.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == tl.offset {
		return nil, nil
	}
	if info.Size() < tl.offset {
		// Truncated or replaced: start over.
		tl.offset = 0
		tl.scanner = thinkingScanner{}
	}
	if _, err := f.Seek(tl.offset, io.SeekStart); err != nil {
		return nil, err
	}

	var turns []thinkingTurn
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// io.EOF with a partial line: the writer is mid-entry.
			if errors.Is(err, io.EOF) {
				return turns, nil
			}
			return turns, err
		}
		tl.offset += int64(len(line))
		turns = append(turns, tl.scanner.Feed(line)...)
	}
}

func (t *ThinkingTracker) record(ctx context.Context, ls *store.LiveSession, turn thinkingTurn) {
	d := turn.Duration()
	if turn.Start.IsZero() || turn.RefID == "" || d < 0 || d > maxThinkingDuration || turn.End.Before(t.cutoff) {
		return
	}
	// The transcript is re-read from the top after a restart.
	exists, err := t.taskStore.AgentEventExists(ctx, ls.SessionID, ThinkingEventType, turn.RefID)
	if err != nil {
		t.logger.Error("failed to check thinking event", "session_id", ls.SessionID, "error", err)
		return
	}
	if exists {
		return
	}

	ms := d.Milliseconds()
	sessionID := ls.SessionID
	refID := turn.RefID
	_, err = t.taskStore.InsertAgentEvent(ctx, &store.AgentEvent{
		AgentName:  ls.AgentName,
		SessionID:  &sessionID,
		EventType:  ThinkingEventType,
		Summary:    "Thinking",
		CreatedAt:  turn.End.UTC().Format(store.ISOFormat),
		DurationMs: &ms,
		RefID:      &refID,
	})
	if err != nil {
		t.logger.Error("failed to record thinking event", "session_id", ls.SessionID, "error", err)
	}
}
