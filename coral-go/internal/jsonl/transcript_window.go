package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// Bounds on what the reader keeps of one session's transcript (#4620, Security's M12). A cold read
// used to read the whole file at once and keep every parsed message of every live session
// (3 to 5 times the file's size), and the tool-name map never shrank. Now a read takes the file in
// chunks of at most readChunkMax, a session keeps its newest messages only (windowMaxMessages, or
// windowMaxBytes of them), and the tool names are capped. The messages before the window are
// counted (sessionCache.base) and read again from the file when someone asks for them.
var (
	readChunkMax      = int64(8 << 20)
	windowMaxMessages = 5000
	windowMaxBytes    = 64 << 20
	toolNamesMax      = 4096
)

// SetTranscriptBoundsForTest lowers the bounds so a small transcript exercises them, and returns
// the function that puts them back. Tests only; no test of the reader may run in parallel with one
// that sets them.
func SetTranscriptBoundsForTest(maxMessages, maxBytes, chunkBytes, toolNames int) (restore func()) {
	was := [4]int{windowMaxMessages, windowMaxBytes, int(readChunkMax), toolNamesMax}
	windowMaxMessages, windowMaxBytes, readChunkMax, toolNamesMax = maxMessages, maxBytes, int64(chunkBytes), toolNames
	return func() {
		windowMaxMessages, windowMaxBytes, readChunkMax, toolNamesMax = was[0], was[1], int64(was[2]), was[3]
	}
}

// total is the number of messages the transcript has read: those before the window, and the window.
func (c *sessionCache) total() int { return c.base + len(c.messages) }

// resetMessages forgets the parsed transcript: it is read again from the start.
func (c *sessionCache) resetMessages() {
	c.offset, c.base, c.messages, c.sizes, c.msgBytes = 0, 0, nil, nil, 0
	c.toolUseNames, c.toolSeen, c.chunkSeq = make(map[string]string), nil, 0
}

// addMessages appends parsed messages and drops the oldest ones past the window's bounds, copying
// what stays so the dropped ones can be freed.
func (c *sessionCache) addMessages(msgs []map[string]any) {
	for _, m := range msgs {
		n := approxSize(m)
		c.messages = append(c.messages, m)
		c.sizes = append(c.sizes, n)
		c.msgBytes += n
	}
	drop, bytes := 0, c.msgBytes
	for len(c.messages)-drop > 1 && (len(c.messages)-drop > windowMaxMessages || bytes > windowMaxBytes) {
		bytes -= c.sizes[drop]
		drop++
	}
	if drop > 0 {
		c.base += drop
		c.messages = append(make([]map[string]any, 0, len(c.messages)-drop+256), c.messages[drop:]...)
		c.sizes = append(make([]int, 0, len(c.sizes)-drop+256), c.sizes[drop:]...)
		c.msgBytes = bytes
	}
}

// boundToolNames caps the tool-name map: when it holds more than toolNamesMax names, the ones
// first seen in the oldest chunks go (agy's "__last_*" bookkeeping stays).
func (c *sessionCache) boundToolNames() {
	c.toolSeen, c.chunkSeq = boundNames(c.toolUseNames, c.toolSeen, c.chunkSeq)
}

func boundNames(names map[string]string, seen map[string]uint32, seq uint32) (map[string]uint32, uint32) {
	seq++
	if seen == nil {
		seen = make(map[string]uint32, len(names))
	}
	for k := range names {
		if _, ok := seen[k]; !ok {
			seen[k] = seq
		}
	}
	if len(names) <= toolNamesMax {
		return seen, seq
	}
	type entry struct {
		k string
		s uint32
	}
	list := make([]entry, 0, len(names))
	for k := range names {
		if !strings.HasPrefix(k, "__") {
			list = append(list, entry{k, seen[k]})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].s < list[j].s })
	for _, e := range list[:max(0, len(names)-toolNamesMax)] {
		delete(names, e.k)
		delete(seen, e.k)
	}
	return seen, seq
}

// approxSize is a rough size in bytes of a parsed message: its strings and what holds them.
func approxSize(v any) int {
	switch x := v.(type) {
	case string:
		return len(x) + 16
	case map[string]any:
		n := 48
		for k, e := range x {
			n += len(k) + 16 + approxSize(e)
		}
		return n
	case []any:
		n := 24
		for _, e := range x {
			n += approxSize(e)
		}
		return n
	case []map[string]any:
		n := 24
		for _, e := range x {
			n += approxSize(e)
		}
		return n
	default:
		return 16
	}
}

// entryParser turns decoded transcript entries into messages in order: a tool result is named
// from the tool call before it, and a Codex duplicate is merged into the message before it.
type entryParser struct {
	agentType string
	names     map[string]string
	last      map[string]any // the newest message so far
}

func (p *entryParser) parse(entries []map[string]any) []map[string]any {
	var out []map[string]any
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		for _, message := range parseTranscriptEntry(entry, p.names, p.agentType) {
			if p.agentType == at.Codex && mergeDuplicateCodexMessage(p.last, message) {
				continue
			}
			out = append(out, message)
			p.last = message
		}
	}
	return out
}

// completeLines is the part of a chunk to consume. Only complete lines are: a line caught
// mid-write stays unread until its newline arrives; consuming it would drop the message for good.
// At the end of the file a last line with no newline is taken only when it is already valid JSON.
func completeLines(chunk []byte, eof bool) []byte {
	if !eof {
		return chunk[:bytes.LastIndexByte(chunk, '\n')+1]
	}
	if end := bytes.LastIndexByte(chunk, '\n'); end < 0 {
		if !jsonValidTrim(chunk) {
			return nil
		}
		return chunk
	} else if tail := chunk[end+1:]; len(bytes.TrimSpace(tail)) > 0 && !jsonValidTrim(tail) {
		return chunk[:end+1]
	}
	return chunk
}

// nextChunk reads the next chunk of at most readChunkMax from r, and on past that to the end of a
// line longer than a chunk. eof is whether the file ended.
func nextChunk(r io.Reader) (chunk []byte, eof bool, err error) {
	chunk, err = io.ReadAll(io.LimitReader(r, readChunkMax))
	if err != nil {
		return nil, false, err
	}
	eof = int64(len(chunk)) < readChunkMax
	for !eof && bytes.LastIndexByte(chunk, '\n') < 0 {
		more, err := io.ReadAll(io.LimitReader(r, readChunkMax))
		if err != nil {
			return nil, false, err
		}
		chunk = append(chunk, more...)
		eof = int64(len(more)) < readChunkMax
	}
	return chunk, eof, nil
}

// Transcript is a session's transcript as the reader holds it: the newest messages (Window, whose
// first message is number Base, counting from 0) of Total. The messages before the window are read
// from the file again when Slice or Each asks for them. Lineage (an ancestor session's transcript
// before the session's own) is part of the numbering.
type Transcript struct {
	Window []map[string]any
	Base   int
	Total  int
	// parts are the transcript files whose messages precede the window, oldest first; their
	// counts are the numbers of messages each held when this view was made.
	parts []transcriptPart
}

type transcriptPart struct {
	path      string
	agentType string
	limit     int64            // the bytes of the file this view covers
	count     int              // the number of messages in them
	mem       []map[string]any // when set, the part is these messages, held in memory, not a file
}

// Slice is the messages numbered from..to-1 (clamped to 0..Total), in order. The part before
// the window is read from the file.
func (t *Transcript) Slice(ctx context.Context, from, to int) []map[string]any {
	from, to = max(from, 0), min(to, t.Total)
	if from >= to {
		return nil
	}
	if from >= t.Base {
		return t.Window[from-t.Base : to-t.Base] // all in the window: shared, never written to
	}
	out := make([]map[string]any, 0, to-from)
	if from < t.Base {
		t.Each(ctx, from, min(to, t.Base), func(_ int, m map[string]any) bool { out = append(out, m); return true })
	}
	if to > t.Base {
		out = append(out, t.Window[max(from, t.Base)-t.Base:to-t.Base]...)
	}
	return out
}

// Each calls fn with each message numbered from..to-1 that is before the window, oldest first,
// reading the files one chunk at a time (nothing is kept); fn returns false to stop. Messages in
// the window are not given: take them from Window.
func (t *Transcript) Each(ctx context.Context, from, to int, fn func(index int, m map[string]any) bool) {
	to = min(to, t.Base)
	offset := 0
	for _, part := range t.parts {
		if offset >= to {
			return
		}
		if offset+part.count > from {
			if part.mem != nil {
				for i, m := range part.mem {
					if offset+i >= to {
						return
					}
					if offset+i >= from && !fn(offset+i, m) {
						return
					}
				}
			} else if !streamPart(ctx, part, offset, from, min(to, offset+part.count), fn) { // only its own messages: a later part may hold the rest of the file
				return
			}
		}
		offset += part.count
	}
}

// streamPart parses a part's file from its start, a chunk at a time, and gives fn the messages
// whose number (offset plus the position in the file) is in from..to-1. false: fn stopped it, or
// the file could not be read.
func streamPart(ctx context.Context, part transcriptPart, offset, from, to int, fn func(int, map[string]any) bool) bool {
	f, err := openTranscript(part.path, part.agentType)
	if err != nil {
		return false
	}
	defer f.Close()
	p := &entryParser{agentType: part.agentType, names: make(map[string]string)}
	var seen map[string]uint32
	var seq uint32
	var read int64
	idx := offset
	for read < part.limit && idx < to {
		chunk, eof, err := nextChunk(io.LimitReader(f, part.limit-read))
		if err != nil {
			return false
		}
		data := completeLines(chunk, eof)
		if len(data) == 0 {
			return true
		}
		entries, err := decodeLimited(ctx, data)
		if err != nil {
			return false
		}
		read += int64(len(data))
		for _, m := range p.parse(entries) {
			if idx >= to {
				return true
			}
			if idx >= from && !fn(idx, m) {
				return false
			}
			idx++
		}
		seen, seq = boundNames(p.names, seen, seq)
		if eof {
			return true
		}
		if _, err := f.Seek(read, io.SeekStart); err != nil {
			return false
		}
	}
	return true
}

func jsonValidTrim(b []byte) bool { return json.Valid(bytes.TrimSpace(b)) }

// All is every message, oldest first: the window and, before it, what is read from the files.
func (t *Transcript) All(ctx context.Context) []map[string]any { return t.Slice(ctx, 0, t.Total) }

// transcriptOf is the view of a cache, taken with its lock held.
func transcriptOf(c *sessionCache, agentType string) *Transcript {
	t := &Transcript{Window: c.messages, Base: c.base, Total: c.total()}
	if c.base > 0 {
		t.parts = []transcriptPart{{path: c.path, agentType: agentType, limit: c.offset, count: c.base}}
	}
	return t
}

// withAncestor is t with an ancestor session's transcript before it (a resumed or restarted
// session): the numbering is the ancestor's messages, then t's.
func (t *Transcript) withAncestor(a *Transcript) *Transcript {
	parts := append([]transcriptPart(nil), a.parts...)
	if len(a.Window) > 0 {
		parts = append(parts, transcriptPart{count: len(a.Window), mem: a.Window})
	}
	return &Transcript{Window: t.Window, Base: a.Total + t.Base, Total: a.Total + t.Total, parts: append(parts, t.parts...)}
}
