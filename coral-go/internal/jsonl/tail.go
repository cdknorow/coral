package jsonl

import (
	"bytes"
	"context"
	"io"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/decodelimit"
)

// decodeLimited is decodeLines under the limit: a large decode waits for a slot, or gives up
// when ctx ends (a cancelled Hub poll doesn't sit behind another session's decode), and the slot
// is given back on every path out, a panic included.
func decodeLimited(ctx context.Context, data []byte) ([]map[string]any, error) {
	entries, _, err := decodeLimitedEnds(ctx, data, false)
	return entries, err
}

// decodeLimitedEnds is decodeLimited with decodeLinesEnds' line ends when wantEnds is set.
func decodeLimitedEnds(ctx context.Context, data []byte, wantEnds bool) ([]map[string]any, []int, error) {
	release, err := decodelimit.Acquire(ctx, int64(len(data)))
	if err != nil {
		return nil, nil, err
	}
	defer release()
	if testDuringDecode != nil {
		testDuringDecode()
	}
	entries, ends := decodeLinesEnds(data, wantEnds)
	return entries, ends, nil
}

// testDuringDecode, when a test sets it, runs while a decode holds its slot.
var testDuringDecode func()

// tailChunk is the first window ReadTail decodes; it doubles until enough is satisfied.
var tailChunk int64 = 1 << 20

// tailMax bounds the window (8 MiB): a transcript whose end
// never satisfies enough (no two requests in a long tool run, say) was decoded whole, all of it
// in memory at once: 40 MB of the heap with 40 agents was such reads of 40 MB transcripts
// (#3847, #3833 item 8). The goals need only the latest messages; past the bound they get
// what the window holds.
var tailMax int64 = 8 << 20

// SetTailMaxForTest bounds ReadTail's window at n bytes and returns a func that restores it.
func SetTailMaxForTest(n int64) (restore func()) {
	old := tailMax
	tailMax = n
	return func() { tailMax = old }
}

// SetTailChunkForTest makes ReadTail's first window n bytes and returns a func that restores it.
func SetTailChunkForTest(n int64) (restore func()) {
	old := tailChunk
	tailChunk = n
	return func() { tailChunk = old }
}

// ReadTail parses the end of a transcript instead of all of it, for a reader that needs only its
// latest messages (#3715: the goals tracker's first read after a start decoded every live
// transcript from the beginning). It decodes a window ending at the last complete line, starting
// at a line boundary, and doubles the window until enough(messages) is true or the window is
// the whole file or tailMax; the messages are those of a full read of that window, in order. end is
// the offset just past the last complete line, where an incremental reader goes on (ReadFromContext).
func ReadTail(ctx context.Context, path, agentType string, enough func([]map[string]any) bool) (messages []map[string]any, end int64, err error) {
	f, err := openTranscript(path, agentType)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if agentType == at.Codex && agent.IsCodexGuardianRollout(f) {
		return nil, info.Size(), nil
	}
	size := info.Size()
	for window := tailChunk; ; window *= 2 {
		start := max(size-window, 0)
		buf := make([]byte, size-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return nil, 0, err
		}
		last := bytes.LastIndexByte(buf, '\n')
		if last < 0 {
			if start == 0 {
				return nil, 0, nil // no complete line yet
			}
			continue
		}
		body := buf[:last+1]
		if start > 0 { // the window starts mid-line: from the next line
			nl := bytes.IndexByte(body, '\n')
			body = body[nl+1:]
		}
		if messages, err = parseLines(ctx, body, agentType); err != nil {
			return nil, 0, err
		}
		end = start + int64(last) + 1
		if start == 0 || enough(messages) || window >= tailMax {
			return messages, end, nil
		}
	}
}

// parseLines turns complete JSONL lines into messages, in order, as ReadFromContext does.
func parseLines(ctx context.Context, data []byte, agentType string) ([]map[string]any, error) {
	entries, err := decodeLimited(ctx, data)
	if err != nil {
		return nil, err
	}
	toolUseNames := make(map[string]string)
	var messages []map[string]any
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		for _, message := range parseTranscriptEntry(entry, toolUseNames, agentType) {
			var previous map[string]any
			if len(messages) > 0 {
				previous = messages[len(messages)-1]
			}
			if agentType == at.Codex && mergeDuplicateCodexMessage(previous, message) {
				continue
			}
			messages = append(messages, message)
		}
	}
	return messages, nil
}
