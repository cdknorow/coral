package jsonl

import (
	"bytes"
	"context"
	"io"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// coldDecodes bounds the large decodes that run at once in this process (#3715, Security's
// NIT on #3710): a cold chat read, a goals read from the start, a tail scan. Each holds a
// transcript's bytes and parses it, and several at once (a restart with many live agents) would
// take every CPU from the server; the rest wait their turn.
var coldDecodes = make(chan struct{}, 2)

// bigDecode is the size from which a decode takes a coldDecodes slot.
const bigDecode = 1 << 20

// decodeLimited is decodeLines under the limit: a large decode waits for a slot, or gives up
// when ctx ends (a cancelled Hub poll doesn't sit behind another session's decode), and the slot
// is given back on every path out, a panic included.
func decodeLimited(ctx context.Context, data []byte) ([]map[string]any, error) {
	if len(data) >= bigDecode {
		select {
		case coldDecodes <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-coldDecodes }()
	}
	if testDuringDecode != nil {
		testDuringDecode()
	}
	return decodeLines(data), nil
}

// testDuringDecode, when a test sets it, runs while a decode holds its slot.
var testDuringDecode func()

// tailChunk is the first window ReadTail decodes; it doubles until enough is satisfied.
var tailChunk int64 = 1 << 20

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
// the whole file; the messages are those of a full read of that window, in order. end is the
// offset just past the last complete line, where an incremental reader goes on (ReadFrom).
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
		if start == 0 || enough(messages) {
			return messages, end, nil
		}
	}
}

// parseLines turns complete JSONL lines into messages, in order, as ReadFrom does.
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
