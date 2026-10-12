package background

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

func codexLine(i int) string {
	switch i % 5 {
	case 0:
		return fmt.Sprintf(`{"type":"turn_context","timestamp":"2026-01-01T00:%02d:%02dZ","payload":{"model":"gpt-5.%d"}}`, i/60%60, i%60, i%3)
	case 1:
		return `{"type":"response_item","payload":{"type":"message","content":"working on it"}}`
	case 2:
		if i%25 == 2 {
			return `{"type":"session_meta","timestamp":"2026-01-01T00:00:00Z","payload":{"cwd":"/w"}}`
		}
		return `not json at all`
	}
	return fmt.Sprintf(`{"type":"event_msg","timestamp":"2026-01-01T01:%02d:%02dZ","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":%d,"total_tokens":%d}}}}`,
		i/60%60, i%60, 1000*i, 400*i, 50*i, 7*i, 1457*i)
}

func claudeLine(i int) string {
	switch i % 4 {
	case 0:
		return fmt.Sprintf(`{"type":"user","timestamp":"2026-01-01T00:%02d:%02dZ","message":{"content":"step %d"}}`, i/60%60, i%60, i)
	case 1:
		return `{"type":"assistant","message":{"model":"","usage":{"input_tokens":0,"output_tokens":3}}}`
	}
	model := []string{"claude-sonnet-4-5", "", "claude-opus-4-1"}[i%3]
	return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-01-01T02:%02d:%02dZ","message":{"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`,
		i/60%60, i%60, model, 10+i, 5+i, 100*i, 3*i)
}

func piLine(i int) string {
	if i%3 == 0 {
		return fmt.Sprintf(`{"type":"session","timestamp":"2026-01-01T00:00:%02dZ"}`, i%60)
	}
	return fmt.Sprintf(`{"type":"message","timestamp":"2026-01-01T03:%02d:%02dZ","message":{"role":"assistant","model":"m%d","usage":{"input":%d,"output":%d,"cacheRead":%d,"cacheWrite":%d,"totalTokens":%d,"cost":{"total":%g}}}}`,
		i/60%60, i%60, i%2, 20+i, 2+i, i, 1, 23+2*i+1, 0.001*float64(i))
}

// cursorCase drives one transcript format through the cursor and compares,
// whenever the file ends on a newline, everything the cursor returned since
// its last reset with a full re-parse of the file.
type cursorCase struct {
	name string
	line func(int) string
	// scan returns the number of calls appended and the cursor's state as
	// comparable strings; full does the same for a full parse.
	scan func(path string) (calls []string, first int, state string)
	full func(path string) (calls []string, state string)
}

func formatCalls[C any](cs []C) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = fmt.Sprintf("%+v", c)
	}
	return out
}

func cursorCases() []cursorCase {
	codex := &usageCursor[codexParser]{}
	claude := &usageCursor[claudeParser]{}
	pi := &usageCursor[piParser]{}
	return []cursorCase{
		{"codex", codexLine,
			func(path string) ([]string, int, string) {
				c, first := scanUsage(codex, path, (*codexParser).line, (*codexParser).takeCalls)
				return formatCalls(c), first, codex.parser.SessionStartAt + "|" + codex.parser.currentModel
			},
			func(path string) ([]string, string) {
				var p codexParser
				data, _ := os.ReadFile(path)
				for _, l := range strings.Split(string(data), "\n") {
					p.line(l)
				}
				return formatCalls(p.Calls), p.SessionStartAt + "|" + p.currentModel
			}},
		{"claude", claudeLine,
			func(path string) ([]string, int, string) {
				c, first := scanUsage(claude, path, (*claudeParser).line, (*claudeParser).takeCalls)
				return formatCalls(c), first, claude.parser.SessionStartAt + "|" + claude.parser.lastModel
			},
			func(path string) ([]string, string) {
				u := extractClaudeUsage(path)
				if u == nil {
					return nil, "|"
				}
				// The poller learned the model from the newest call naming one.
				last := ""
				for i := len(u.Calls) - 1; i >= 0; i-- {
					if m := strings.TrimSpace(u.Calls[i].Model); m != "" {
						last = m
						break
					}
				}
				return formatCalls(u.Calls), u.SessionStartAt + "|" + last
			}},
		{"pi", piLine,
			func(path string) ([]string, int, string) {
				c, first := scanUsage(pi, path, (*piParser).line, (*piParser).takeCalls)
				return formatCalls(c), first, pi.parser.SessionStartAt
			},
			func(path string) ([]string, string) {
				u := extractPiUsage(path)
				if u == nil {
					return nil, ""
				}
				return formatCalls(u.Calls), u.SessionStartAt
			}},
	}
}

// The cursor returns exactly the calls a full re-parse finds, in order and
// with consecutive indexes, while the file grows in arbitrary chunks (lines
// split mid-way), and starts over after truncation, replacement or an
// in-place rewrite of the same size.
func TestUsageCursorMatchesFullReparse(t *testing.T) {
	for _, tc := range cursorCases() {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.jsonl")
			rng := rand.New(rand.NewSource(7))
			var seen []string // calls since the cursor's last reset
			check := func(content string) {
				require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
				// Distinct mtimes so a same-size rewrite is detectable.
				mt := time.Now().Add(time.Duration(rng.Intn(1e6)) * time.Microsecond)
				require.NoError(t, os.Chtimes(path, mt, mt))
				calls, first, state := tc.scan(path)
				if first == 0 && len(seen) > 0 && len(calls) > 0 && first != len(seen) {
					seen = nil // the cursor reset
				}
				require.Equal(t, len(seen), first, "indexes are consecutive")
				seen = append(seen, calls...)
				if strings.HasSuffix(content, "\n") {
					want, wantState := tc.full(path)
					require.Equal(t, want, seen)
					require.Equal(t, wantState, state)
				}
			}
			grow := func(lines int, start int) string {
				var b strings.Builder
				for i := start; i < start+lines; i++ {
					b.WriteString(tc.line(i) + "\n")
				}
				return b.String()
			}

			// Grow in random chunks, often splitting a line.
			all := grow(400, 0)
			for pos := 0; pos < len(all); {
				pos = min(len(all), pos+1+rng.Intn(700))
				check(all[:pos])
			}

			// Truncate to a shorter, different file: a fresh start.
			seen = nil
			check(grow(30, 1000))
			// Append, then replace with content of a different head.
			check(grow(30, 1000) + grow(10, 2000))
			seen = nil
			check(grow(25, 3000))
			// Same size, rewritten in place with different content.
			same := grow(25, 3000)
			alt := []byte(same)
			for i := range alt {
				if alt[i] >= '1' && alt[i] <= '8' {
					alt[i]++
				}
			}
			seen = nil
			check(string(alt))
		})
	}
}

// The poller records each call once while the transcript grows across polls,
// matching what it records from the finished file in a single pass.
func TestTokenPollerIncrementalRecordsLikeOnePass(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	proj := filepath.Join(dir, "-w")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	sid := "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(proj, sid+".jsonl")

	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(claudeLine(i) + "\n")
	}
	full := b.String()

	incremental := newTokenPollerHarness(t)
	for pos := 0; pos < len(full); pos += 997 {
		end := min(len(full), pos+997)
		require.NoError(t, os.WriteFile(path, []byte(full[:end]), 0o644))
		mt := time.Now().Add(time.Duration(pos) * time.Millisecond)
		require.NoError(t, os.Chtimes(path, mt, mt))
		incremental.pollClaude(sid, "/w")
	}
	onePass := newTokenPollerHarness(t)
	require.NoError(t, os.WriteFile(path, []byte(full), 0o644))
	onePass.pollClaude(sid, "/w")

	require.Equal(t, onePass.rows(), incremental.rows())
	require.NotEmpty(t, incremental.rows())
}

// BenchmarkClaudeUsagePoll compares one poll of a long transcript after a
// single new turn: full re-parse versus the cursor.
func BenchmarkClaudeUsagePoll(b *testing.B) {
	path := filepath.Join(b.TempDir(), "t.jsonl")
	var sb strings.Builder
	for i := 0; i < 40000; i++ {
		sb.WriteString(claudeLine(i) + "\n")
	}
	require.NoError(b, os.WriteFile(path, []byte(sb.String()), 0o644))
	info, _ := os.Stat(path)
	b.Logf("transcript %.1fMB", float64(info.Size())/(1<<20))
	appendTurn := func(i int) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(b, err)
		f.WriteString(claudeLine(40000+i) + "\n")
		f.Close()
	}
	b.Run("full-reparse", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			appendTurn(i)
			extractClaudeUsage(path)
		}
	})
	b.Run("cursor", func(b *testing.B) {
		c := &usageCursor[claudeParser]{}
		scanUsage(c, path, (*claudeParser).line, (*claudeParser).takeCalls)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			appendTurn(i)
			scanUsage(c, path, (*claudeParser).line, (*claudeParser).takeCalls)
		}
	})
}

type tokenPollerHarness struct {
	t      *testing.T
	db     *store.DB
	ss     *store.SessionStore
	poller *TokenPoller
}

func newTokenPollerHarness(t *testing.T) *tokenPollerHarness {
	db := setupTestDB(t)
	ss := store.NewSessionStore(db)
	return &tokenPollerHarness{t: t, db: db, ss: ss, poller: NewTokenPoller(ss, store.NewTokenUsageStore(db), time.Hour)}
}

func (h *tokenPollerHarness) pollClaude(sid, workDir string) {
	ctx := context.Background()
	ls, _ := h.ss.GetLiveSession(ctx, sid)
	if ls == nil {
		require.NoError(h.t, h.ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "a", WorkingDir: workDir}))
		ls, _ = h.ss.GetLiveSession(ctx, sid)
	}
	h.poller.pollClaudeSession(ctx, ls)
}

func (h *tokenPollerHarness) rows() []string {
	var rows []string
	require.NoError(h.t, h.db.Select(&rows, `SELECT session_id || '|' || input_tokens || '|' || output_tokens || '|' || cache_read_tokens || '|' || cache_write_tokens || '|' || printf('%.9f', cost_usd) || '|' || recorded_at || '|' || COALESCE(model, '') || '|' || COALESCE(session_start_at, '') FROM token_usage ORDER BY recorded_at, id`))
	return rows
}

// When the transcript is replaced, the poller keeps the full re-parse rule:
// calls are counted from the start of the new file and those below the
// processed count are not recorded again.
func TestTokenPollerReplacedTranscriptKeepsProcessedCount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	proj := filepath.Join(dir, "-w")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	sid := "11111111-2222-3333-4444-666666666666"
	path := filepath.Join(proj, sid+".jsonl")
	turn := func(ts string, in int) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":%d,"output_tokens":1}}}`, ts, in) + "\n"
	}
	h := newTokenPollerHarness(t)
	write := func(content string, at time.Time) {
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		require.NoError(t, os.Chtimes(path, at, at))
		h.pollClaude(sid, "/w")
	}
	now := time.Now()
	write(turn("2026-01-01T00:00:01Z", 11), now)
	// Replaced: a different file with three calls. One was already counted, so
	// the first call of the new file is skipped, as a full re-parse would.
	write(turn("2026-02-01T00:00:01Z", 21)+turn("2026-02-01T00:00:02Z", 22)+turn("2026-02-01T00:00:03Z", 23), now.Add(time.Minute))

	var inputs []int
	require.NoError(t, h.db.Select(&inputs, `SELECT input_tokens FROM token_usage ORDER BY recorded_at`))
	require.Equal(t, []int{11, 22, 23}, inputs)
}
