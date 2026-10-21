package background

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/jsonl"
)

// referenceClaudeLine is claudeParser.line as it was before the prefilter: every line decoded.
func (p *claudeParser) referenceLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Effort    string `json:"effort"`
		Message   struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
				OutputTokensDetails      struct {
					ThinkingTokens int `json:"thinking_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return
	}
	if entry.Timestamp != "" && p.SessionStartAt == "" {
		p.SessionStartAt = entry.Timestamp
	}
	if entry.Type == "assistant" && entry.Message.Usage.InputTokens > 0 {
		p.Calls = append(p.Calls, claudeCallData{
			InputTokens:      entry.Message.Usage.InputTokens,
			OutputTokens:     entry.Message.Usage.OutputTokens,
			CacheReadTokens:  entry.Message.Usage.CacheReadInputTokens,
			CacheWriteTokens: entry.Message.Usage.CacheCreationInputTokens,
			Model:            entry.Message.Model,
			Timestamp:        entry.Timestamp,
			Effort:           jsonl.CleanEffort(entry.Effort),
			ThinkingTokens:   entry.Message.Usage.OutputTokensDetails.ThinkingTokens,
		})
		if m := strings.TrimSpace(entry.Message.Model); m != "" {
			p.lastModel = m
		}
	}
}

// longS is U+017F, the long s, which encoding/json folds to s in a field name.
const longS = "\u017f"

// usageLines are lines the prefilter must treat as the full decode does: ordinary ones, and the ones
// that spell the usage key in a way a plain search for the word misses.
func usageLines() []string {
	asst := func(key string) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-19T10:00:00Z","effort":"high","message":{"model":"claude-opus-5-5",%q:{"input_tokens":12,"output_tokens":34,"cache_read_input_tokens":5}}}`, key)
	}
	return []string{
		`{"type":"user","timestamp":"2026-10-19T09:59:00Z","message":{"content":"hello"}}`,
		`{"type":"user","timestamp":"2026-10-19T09:59:01Z","message":{"content":[{"type":"tool_result","content":"a usage report: the usage of disk"}]}}`,
		asst("usage"),
		asst("Usage"),
		asst("USAGE"),
		asst("uSaGe"),
		asst("u" + longS + "age"), // long s: encoding/json folds it to s
		`{"type":"assistant","timestamp":"2026-10-19T10:00:00Z","message":{"model":"m","us\u0061ge":{"input_tokens":7}}}`, // an escaped a
		`{"type":"assistant","timestamp":"2026-10-19T10:00:00Z","message":{"model":"m","\u0075sage":{"input_tokens":8}}}`,
		`{"type":"assistant","timestamp":"2026-10-19T10:00:00Z","message":{"model":"m","usag":{"input_tokens":9}}}`,
		`{"type":"assistant","timestamp":"2026-10-19T10:00:00Z","message":{"model":"m"}}`,
		`{"type":"system","timestamp":"2026-10-19T10:00:01Z"}`,
		`not json usage`,
		`{"type":"assistant","message":{"usage":{"input_tokens":3}}`, // cut short
		`   `,
		``,
	}
}

// With the session's start known and without, over every order of two lines, the prefiltered parser
// and the one that decodes every line end in the same state.
func TestClaudeUsagePrefilterIsTheFullDecode(t *testing.T) {
	lines := usageLines()
	for _, start := range []string{"", "2026-10-19T09:00:00Z"} {
		for i, a := range lines {
			for j, b := range lines {
				var got, want claudeParser
				got.SessionStartAt, want.SessionStartAt = start, start
				got.line(a)
				got.line(b)
				want.referenceLine(a)
				want.referenceLine(b)
				require.Equal(t, want, got, "start %q lines %d, %d", start, i, j)
			}
		}
	}
}

// The hard lines are not vacuous: the full decode finds a call in each of the spellings of the key.
func TestTheFullDecodeFindsACallInEverySpellingOfTheKey(t *testing.T) {
	found := 0
	for _, l := range usageLines() {
		var p claudeParser
		p.referenceLine(l)
		found += len(p.Calls)
	}
	assert.Equal(t, 7, found, "usage, Usage, USAGE, uSaGe, the long s and two escaped spellings: one call each")
}

func TestUsageKeyPossible(t *testing.T) {
	for _, l := range []string{"usage", "Usage", "xxUSAGExx", `\u0075`, "u" + longS + "age"} {
		assert.True(t, usageKeyPossible(l), l)
	}
	for _, l := range []string{"", "usag", "us age", `{"type":"user","message":{"content":"hello"}}`, "uuuuuu", "ussage"} {
		assert.False(t, usageKeyPossible(l), l)
	}
}

func FuzzClaudeUsagePrefilter(f *testing.F) {
	for _, l := range usageLines() {
		f.Add(l, true)
		f.Add(l, false)
	}
	f.Fuzz(func(t *testing.T, line string, started bool) {
		var got, want claudeParser
		if started {
			got.SessionStartAt, want.SessionStartAt = "t0", "t0"
		}
		got.line(line)
		want.referenceLine(line)
		require.Equal(t, want, got)
	})
}

// BenchmarkClaudeUsageLines is the token poller's first read of a transcript: a short user line, an
// assistant line with its usage, a tool call and a 10 KB tool result, over and over.
func benchClaudeUsageLines(b *testing.B, line func(*claudeParser, string)) {
	big := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 220) // ~10 KB
	lines := []string{
		`{"type":"user","timestamp":"2026-10-19T09:59:00Z","message":{"content":"Please do the next step and report back."}}`,
		`{"type":"assistant","timestamp":"2026-10-19T09:59:02Z","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"Working on it."},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":120,"output_tokens":340,"cache_read_input_tokens":9000,"cache_creation_input_tokens":300}}}`,
		fmt.Sprintf(`{"type":"user","timestamp":"2026-10-19T09:59:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","content":%q}]}}`, big),
	}
	var p claudeParser
	line(&p, lines[0]) // the session start is known
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, l := range lines {
			line(&p, l)
		}
		p.Calls = p.Calls[:0]
	}
}

func BenchmarkClaudeUsageLines(b *testing.B) {
	benchClaudeUsageLines(b, (*claudeParser).line)
}

// BenchmarkClaudeUsageLinesFullDecode is the same lines through the parser that decodes every one.
func BenchmarkClaudeUsageLinesFullDecode(b *testing.B) {
	benchClaudeUsageLines(b, (*claudeParser).referenceLine)
}
