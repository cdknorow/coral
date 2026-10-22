package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
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
	c.toolUseNames, c.toolSeen, c.chunkSeq, c.ckpts = make(map[string]string), nil, 0, nil
	c.pending = pendingCalls{}
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
	c.toolSeen, c.chunkSeq = boundNames(c.toolUseNames, c.toolSeen, c.chunkSeq, c.pending.noteDropped(c.toolUseNames))
}

// boundNames drops the oldest names past toolNamesMax; onDrop, when given, is called with each id just before it goes
// (the checkpoints' pending calls take a call the bound would drop before any checkpoint has seen it).
func boundNames(names map[string]string, seen map[string]uint32, seq uint32, onDrop func(id string)) (map[string]uint32, uint32) {
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
		if onDrop != nil {
			onDrop(e.k)
		}
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

// nextChunk reads the next chunk of at most chunkMax from r, and on past that to the end of a
// line longer than a chunk. eof is whether the file ended.
func nextChunk(r io.Reader, chunkMax int64) (chunk []byte, eof bool, err error) {
	chunk, err = readUpTo(r, chunkMax)
	if err != nil {
		return nil, false, err
	}
	eof = int64(len(chunk)) < chunkMax
	for !eof && bytes.LastIndexByte(chunk, '\n') < 0 {
		more, err := readUpTo(r, chunkMax)
		if err != nil {
			return nil, false, err
		}
		chunk = append(chunk, more...)
		eof = int64(len(more)) < chunkMax
	}
	return chunk, eof, nil
}

// readUpTo is io.ReadAll(io.LimitReader(r, limit)) with the buffer made once at the size the read will
// most likely have: io.ReadAll starts at 512 bytes and grows by a quarter, and for an 8 MiB chunk
// that was about as much garbage again as the chunk (#5023: a quarter of the steady-state
// allocations of a server whose chats page back through their transcripts). The size is what
// is left of a LimitedReader or of a file, else small; a file that grew since is read on by growing
// the buffer, as ReadAll does.
func readUpTo(r io.Reader, limit int64) ([]byte, error) {
	hint := int64(512)
	switch v := r.(type) {
	case *io.LimitedReader:
		hint = v.N
	case interface{ sizeHint() int64 }:
		hint = v.sizeHint()
	}
	buf := make([]byte, 0, min(max(hint, 0), limit)+1) // a byte more, so that the read that finds the end needs no growth
	for int64(len(buf)) < limit {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := r.Read(buf[len(buf):min(int64(cap(buf)), limit)])
		buf = buf[:len(buf)+n]
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return buf, err
		}
	}
	return buf, nil
}

// sizedFile is a file read from its position, with the bytes it is believed to hold from there (the size its reader
// already has from a stat): readUpTo makes its buffer that size, with no stat of its own. A poll of a transcript that
// gained a few lines is a handful of system calls, and two more were a third of it.
type sizedFile struct {
	*os.File
	left int64
}

func (f sizedFile) sizeHint() int64 { return f.left }

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
	ckpts     []checkpoint     // where in the file to start reading for a message (a file part)
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
	release, ok := lockOldRead(ctx, part.path)
	if !ok {
		return false
	}
	defer release()
	f, err := openTranscript(part.path, part.agentType)
	if err != nil {
		return false
	}
	defer f.Close()
	if part.agentType == at.Codex && agent.IsCodexGuardianRollout(f) {
		return true
	}
	p := &entryParser{agentType: part.agentType, names: make(map[string]string)}
	var seen map[string]uint32
	var seq uint32
	var read int64
	// Start at a checkpoint, not at the file's start: the last one at or before the first message wanted. The tool calls
	// before it that have no result yet come with it (pendingCalls), so a result is named as in the whole file.
	start := checkpoint{}
	if i := sort.Search(len(part.ckpts), func(k int) bool { return part.ckpts[k].msgs > from-offset }) - 1; i >= 0 {
		start = part.ckpts[i]
	}
	p.last = copyMessage(start.last)
	for k, v := range start.pending { // older than anything the page reads, so the first to go if the names outgrow their bound
		p.names[k] = v
		if seen == nil {
			seen = make(map[string]uint32, len(start.pending))
		}
		seen[k] = 0
	}
	read = start.offset
	idx := offset + start.msgs
	if read > 0 {
		if _, err := f.Seek(read, io.SeekStart); err != nil {
			return false
		}
	}
	for read < part.limit && idx < to {
		chunk, eof, err := nextChunk(io.LimitReader(f, part.limit-read), pageChunkMax())
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
		oldReadBytes.Add(int64(len(data)))
		for _, m := range p.parse(entries) {
			if idx >= to {
				return true
			}
			if idx >= from && !fn(idx, m) {
				return false
			}
			idx++
		}
		seen, seq = boundNames(p.names, seen, seq, nil)
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
		t.parts = []transcriptPart{{path: c.path, agentType: agentType, limit: c.offset, count: c.base, ckpts: c.ckpts}}
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

// A checkpoint is a place in a transcript file that a read of the older messages can start at: the number
// of messages before it, its byte offset (a line start: where a chunk of the cache's own read began) and
// the message just before it, which a Codex duplicate at the start of the chunk is merged into.
type checkpoint struct {
	msgs    int
	offset  int64
	last    map[string]any
	pending map[string]string // the tool calls before it that a result after it may name (pendingCalls)
}

// pageChunkMax is the chunk an old page is read in: a page decodes the chunks from a checkpoint up to
// its last message, and no more of the file than that (#5023).
func pageChunkMax() int64 { return min(readChunkMax, pageChunkCap) }

// pageChunkCap bounds pageChunkMax (a var for tests).
var pageChunkCap = int64(2 << 20)

// checkpointSpacing is the bytes between checkpoints: half a page chunk, so a read of an old page
// parses about two chunks and not the file from its start (#4620's follow-up: Slice and Each read the
// whole prefix before; #5023: and then the 8 to 16 MiB before the page, now 1 to 3 MiB).
func checkpointSpacing() int64 { return pageChunkMax() / 2 }

// noteCheckpoint records a checkpoint at offset, the start of a run of lines about to be parsed, when
// the last one is a spacing behind. last is the newest message parsed so far.
func (c *sessionCache) noteCheckpoint(offset int64, last map[string]any) {
	if offset == 0 {
		return
	}
	if n := len(c.ckpts); n > 0 && offset-c.ckpts[n-1].offset < checkpointSpacing() {
		return
	}
	c.ckpts = append(c.ckpts, checkpoint{msgs: c.total(), offset: offset, last: copyMessage(last), pending: c.pending.snapshot(c.toolUseNames)})
}

// copyMessage is a shallow copy: the merge of a Codex duplicate writes to the message it merges into, and the
// checkpoint's must stay as it was when it was taken.
func copyMessage(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Reading the older part of a transcript is bounded to one reader per file at a time: others wait their turn
// (or give up with their context), so a client asking for old pages in a loop costs one reader, not many.
var oldReads struct {
	sync.Mutex
	held map[string]chan struct{}
}

// oldReadBytes, oldReadActive and oldReadPeak count what streamPart reads and how many run at once (tests).
var oldReadBytes, oldReadActive, oldReadPeak atomic.Int64

// lockOldRead waits for the file's turn; release gives it up. ok is false when ctx ended first.
func lockOldRead(ctx context.Context, path string) (release func(), ok bool) {
	for {
		oldReads.Lock()
		if oldReads.held == nil {
			oldReads.held = map[string]chan struct{}{}
		}
		wait, busy := oldReads.held[path]
		if !busy {
			done := make(chan struct{})
			oldReads.held[path] = done
			oldReads.Unlock()
			n := oldReadActive.Add(1)
			for peak := oldReadPeak.Load(); n > peak && !oldReadPeak.CompareAndSwap(peak, n); peak = oldReadPeak.Load() {
			}
			return func() {
				oldReadActive.Add(-1)
				oldReads.Lock()
				delete(oldReads.held, path)
				oldReads.Unlock()
				close(done)
			}, true
		}
		oldReads.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, false
		}
	}
}
