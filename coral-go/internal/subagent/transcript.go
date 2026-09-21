// Package subagent reads the artifacts Claude Code writes for subagents a
// session launches, so their token spend can be tracked.
//
// Claude Code does not record a subagent's API calls in the parent session's
// transcript. They go to separate files next to it:
//
//	<project>/<session-id>.jsonl                               parent transcript
//	<project>/<session-id>/subagents/agent-<id>.jsonl          subagent transcript
//	<project>/<session-id>/subagents/agent-<id>.meta.json      launch metadata
//
// Anything that only reads the parent transcript therefore never sees
// subagent spend.
package subagent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	transcriptPrefix = "agent-"
	transcriptSuffix = ".jsonl"
	metaSuffix       = ".meta.json"
)

// Meta is the launch metadata Claude Code writes beside a subagent transcript.
type Meta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	// ToolUseID is the parent's Agent tool call that launched this subagent.
	ToolUseID  string `json:"toolUseId"`
	SpawnDepth int    `json:"spawnDepth"`
}

// Call is the token usage of one API request made by a subagent.
type Call struct {
	MessageID        string
	Model            string
	Timestamp        string // timestamp of the last record seen for this message
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Transcript is the parsed spend of one subagent transcript.
type Transcript struct {
	// SubagentID comes from the records' agentId, falling back to the file name.
	SubagentID string
	// ParentSessionID is the sessionId on the records: the session that
	// launched the subagent.
	ParentSessionID string
	// Calls holds one entry per API request, in order of first appearance.
	Calls []Call
	// StartedAt and LastActivityAt are the first and last record timestamps.
	StartedAt      string
	LastActivityAt string
}

// Totals is summed usage across calls.
type Totals struct {
	APICalls         int
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

// Totals sums usage across every call in the transcript.
func (t *Transcript) Totals() Totals {
	var out Totals
	for _, c := range t.Calls {
		out.APICalls++
		out.InputTokens += c.InputTokens
		out.OutputTokens += c.OutputTokens
		out.CacheReadTokens += c.CacheReadTokens
		out.CacheWriteTokens += c.CacheWriteTokens
	}
	return out
}

// PrimaryModel returns the model that produced the most output tokens, which
// is the one worth showing when a subagent is summarised in a single row.
// Ties go to the model seen first.
func (t *Transcript) PrimaryModel() string {
	output := map[string]int{}
	var order []string
	for _, c := range t.Calls {
		if c.Model == "" {
			continue
		}
		if _, seen := output[c.Model]; !seen {
			order = append(order, c.Model)
		}
		output[c.Model] += c.OutputTokens
	}
	best := ""
	for _, m := range order {
		if best == "" || output[m] > output[best] {
			best = m
		}
	}
	return best
}

// record is the subset of a transcript line this package reads.
type record struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	AgentID   string `json:"agentId"`
	SessionID string `json:"sessionId"`
	UUID      string `json:"uuid"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// ParseTranscript reads a subagent transcript file.
func ParseTranscript(path string) (*Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	t, err := parse(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if t.SubagentID == "" {
		t.SubagentID = IDFromPath(path)
	}
	return t, nil
}

// parse reads transcript records from r.
//
// Claude Code writes one record per content block of a response, and every
// one of those records repeats the message's id and usage. While the response
// streams, the repeated usage is a running snapshot: output_tokens grows from
// record to record. Counting each record would bill a request once per content
// block, and keeping only the first would under-count output. So records are
// grouped by message id and the maximum of each counter is kept, which is the
// final value for the request.
//
// Lines that are not valid JSON are skipped: the file is appended to while it
// is being read, so a torn final line is normal, not an error.
func parse(r io.Reader) (*Transcript, error) {
	t := &Transcript{}
	index := map[string]int{} // message key -> position in t.Calls

	br := bufio.NewReaderSize(r, 256*1024)
	for lineNo := 1; ; lineNo++ {
		// ReadBytes rather than Scanner: a single record can embed a whole
		// file's contents and blow through any fixed Scanner buffer.
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			t.addLine(line, lineNo, index)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *Transcript) addLine(line []byte, lineNo int, index map[string]int) {
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		return
	}
	if t.SubagentID == "" && rec.AgentID != "" {
		t.SubagentID = rec.AgentID
	}
	if t.ParentSessionID == "" && rec.SessionID != "" {
		t.ParentSessionID = rec.SessionID
	}
	if rec.Timestamp != "" {
		if t.StartedAt == "" {
			t.StartedAt = rec.Timestamp
		}
		t.LastActivityAt = rec.Timestamp
	}

	u := rec.Message.Usage
	if rec.Type != "assistant" || u == nil {
		return
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 && u.CacheCreationInputTokens == 0 {
		return // synthetic/placeholder assistant records carry no spend
	}

	// A record without a message id cannot be grouped, so it stands alone.
	key := rec.Message.ID
	if key == "" {
		key = rec.UUID
	}
	if key == "" {
		key = fmt.Sprintf("line-%d", lineNo)
	}

	i, seen := index[key]
	if !seen {
		index[key] = len(t.Calls)
		t.Calls = append(t.Calls, Call{MessageID: rec.Message.ID})
		i = len(t.Calls) - 1
	}
	c := &t.Calls[i]
	c.InputTokens = max(c.InputTokens, u.InputTokens)
	c.OutputTokens = max(c.OutputTokens, u.OutputTokens)
	c.CacheReadTokens = max(c.CacheReadTokens, u.CacheReadInputTokens)
	c.CacheWriteTokens = max(c.CacheWriteTokens, u.CacheCreationInputTokens)
	if rec.Message.Model != "" {
		c.Model = rec.Message.Model
	}
	if rec.Timestamp != "" {
		c.Timestamp = rec.Timestamp
	}
}

// ReadMeta reads a subagent's launch metadata. A missing file is not an
// error: it returns a zero Meta, since spend can be tracked without it.
func ReadMeta(path string) (Meta, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Meta{}, nil
	}
	if err != nil {
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// Files locates one subagent's artifacts on disk.
type Files struct {
	SubagentID     string
	TranscriptPath string
	MetaPath       string // may not exist
}

// Dir returns the directory holding the subagent artifacts of the session
// whose transcript is at parentTranscriptPath.
func Dir(parentTranscriptPath string) string {
	return filepath.Join(strings.TrimSuffix(parentTranscriptPath, transcriptSuffix), "subagents")
}

// Discover lists the subagents launched by the session whose transcript is at
// parentTranscriptPath, sorted by ID. A session that never launched a
// subagent has no directory; that yields an empty list, not an error.
func Discover(parentTranscriptPath string) ([]Files, error) {
	dir := Dir(parentTranscriptPath)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Files
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id := IDFromPath(e.Name())
		if id == "" {
			continue
		}
		out = append(out, Files{
			SubagentID:     id,
			TranscriptPath: filepath.Join(dir, e.Name()),
			MetaPath:       filepath.Join(dir, transcriptPrefix+id+metaSuffix),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubagentID < out[j].SubagentID })
	return out, nil
}

// IDFromPath extracts the subagent ID from a transcript file name of the form
// "agent-<id>.jsonl". It returns "" for anything else, including the
// ".meta.json" sidecar.
func IDFromPath(path string) string {
	name := filepath.Base(path)
	if !strings.HasPrefix(name, transcriptPrefix) || !strings.HasSuffix(name, transcriptSuffix) {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(name, transcriptPrefix), transcriptSuffix)
}
