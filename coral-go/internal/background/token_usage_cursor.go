package background

import (
	"encoding/json"
	"strings"

	"github.com/cdknorow/coral/internal/jsonl"
)

// Transcripts only grow while an agent runs, but the token poller used to
// re-read and re-parse each one in full every time it changed (every poll for
// an active agent), so the cost grew with the length of the session. The
// parsers below hold the per-line state that used to live inside one
// full-file pass (current model, session start), so a usageCursor can feed
// them only the lines appended since the last poll. extract*Usage still parse
// a whole file with the same parsers.

// codexParser extracts cumulative token_count entries from Codex rollout lines.
type codexParser struct {
	codexUsageData
	currentModel string
}

func (p *codexParser) line(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Type string `json:"type"`
			Info struct {
				TotalTokenUsage struct {
					InputTokens           int `json:"input_tokens"`
					CachedInputTokens     int `json:"cached_input_tokens"`
					OutputTokens          int `json:"output_tokens"`
					ReasoningOutputTokens int `json:"reasoning_output_tokens"`
					TotalTokens           int `json:"total_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
			Model string `json:"model"`
			CWD   string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return
	}
	if entry.Type == "session_meta" && entry.Timestamp != "" {
		p.SessionStartAt = entry.Timestamp
	}
	if entry.Type == "turn_context" && entry.Payload.Model != "" {
		p.currentModel = entry.Payload.Model
	}
	if entry.Type == "event_msg" && entry.Payload.Type == "token_count" {
		tu := entry.Payload.Info.TotalTokenUsage
		p.Calls = append(p.Calls, codexCallData{
			InputTokens:  tu.InputTokens,
			OutputTokens: tu.OutputTokens + tu.ReasoningOutputTokens,
			CachedInput:  tu.CachedInputTokens,
			TotalTokens:  tu.TotalTokens,
			Timestamp:    entry.Timestamp,
			Model:        p.currentModel,
		})
	}
}

// claudeParser extracts per-turn usage from Claude transcript lines.
type claudeParser struct {
	claudeUsageData
	lastModel string // model of the newest call that named one
}

func (p *claudeParser) line(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return
	}
	// The first timestamp is the session start.
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
		})
		if m := strings.TrimSpace(entry.Message.Model); m != "" {
			p.lastModel = m
		}
	}
}

// piParser extracts per-turn usage from Pi session lines.
type piParser struct {
	piUsageData
}

func (p *piParser) line(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var entry struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			Role  string `json:"role"`
			Model string `json:"model"`
			Usage struct {
				Input       int `json:"input"`
				Output      int `json:"output"`
				CacheRead   int `json:"cacheRead"`
				CacheWrite  int `json:"cacheWrite"`
				TotalTokens int `json:"totalTokens"`
				Cost        struct {
					Total float64 `json:"total"`
				} `json:"cost"`
			} `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return
	}
	if entry.Timestamp != "" && p.SessionStartAt == "" {
		p.SessionStartAt = entry.Timestamp
	}
	if entry.Type == "message" && entry.Message.Role == "assistant" && entry.Message.Usage.Input > 0 {
		p.Calls = append(p.Calls, piCallData{
			InputTokens:      entry.Message.Usage.Input,
			OutputTokens:     entry.Message.Usage.Output,
			CacheReadTokens:  entry.Message.Usage.CacheRead,
			CacheWriteTokens: entry.Message.Usage.CacheWrite,
			TotalTokens:      entry.Message.Usage.TotalTokens,
			CostUSD:          entry.Message.Usage.Cost.Total,
			Model:            entry.Message.Model,
			Timestamp:        entry.Timestamp,
		})
	}
}

func (p *codexParser) takeCalls() []codexCallData {
	c := p.Calls
	p.Calls = nil
	return c
}

func (p *claudeParser) takeCalls() []claudeCallData {
	c := p.Calls
	p.Calls = nil
	return c
}

func (p *piParser) takeCalls() []piCallData {
	c := p.Calls
	p.Calls = nil
	return c
}

// usageCursor follows one session's transcript with parser P. The parser's
// state other than its calls (model, session start) carries over from scan
// to scan; a truncated, replaced or different file starts it over.
type usageCursor[P any] struct {
	path   string
	cursor jsonl.TranscriptCursor
	parser P
	total  int // calls in the file up to the cursor
}

// scanUsage parses the lines appended to path since the cursor's last scan and
// returns the calls they hold, plus the index of the first one among all
// calls in the file, so callers can count processed calls exactly as a full
// re-parse would.
func scanUsage[P any, C any](c *usageCursor[P], path string, line func(*P, string), take func(*P) []C) (calls []C, first int) {
	reset := func() {
		var zero P
		c.parser = zero
		c.total = 0
	}
	if c.path != path {
		c.path = path
		c.cursor = jsonl.TranscriptCursor{}
		reset()
	}
	c.cursor.Scan(path, reset, func(b []byte) { line(&c.parser, string(b)) })
	calls = take(&c.parser)
	first = c.total
	c.total += len(calls)
	return calls, first
}

func cursorFor[P any](m map[string]*usageCursor[P], sessionID string) *usageCursor[P] {
	c := m[sessionID]
	if c == nil {
		c = &usageCursor[P]{}
		m[sessionID] = c
	}
	return c
}

// unprocessed returns the calls whose index among all calls in the file is at
// least alreadyProcessed, given calls starting at index first.
func unprocessed[C any](calls []C, first, alreadyProcessed int) []C {
	if skip := alreadyProcessed - first; skip > 0 {
		if skip >= len(calls) {
			return nil
		}
		return calls[skip:]
	}
	return calls
}
