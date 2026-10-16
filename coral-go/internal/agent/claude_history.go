package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/decodelimit"
)

// claudeHistory is what one ClaudeAgent's indexer passes remember between passes (#3728): a
// growing transcript is read from where the last pass stopped, never decoded again from the
// start, and a file that held no session isn't read again until it changes.
type claudeHistory struct {
	mu      sync.Mutex
	cursors map[string]*claudeCursor // the most recently read transcripts, at most maxClaudeCursors
	empty   map[string]fileStamp     // transcripts read whole that held no session, by stamp
}

// maxClaudeCursors bounds the cursors kept: each holds up to ftsBodyLimit of search text, and
// only the transcripts being written (the live agents') need one.
const maxClaudeCursors = 64

type fileStamp struct {
	size  int64
	mtime time.Time
}

func stampOf(info os.FileInfo) fileStamp { return fileStamp{info.Size(), info.ModTime()} }

// claudeCursor is a transcript's fold so far: the bytes up to offset (whole lines) are read.
type claudeCursor struct {
	info     os.FileInfo // the file read, to tell a replaced file (os.SameFile)
	stamp    fileStamp   // the file when it was last read
	offset   int64
	tail     []byte // the last bytes before offset, to tell a rewritten file
	firstTS  string
	lastTS   string
	msgCount int
	summary  string
	fts      FTSBodyBuilder
	ftsSent  string // the search body last returned, to say when it is unchanged
	used     time.Time
}

const cursorTailLen = 64

var claudeHistoryInit sync.Mutex

func (a *ClaudeAgent) history() *claudeHistory {
	claudeHistoryInit.Lock()
	defer claudeHistoryInit.Unlock()
	if a.hist == nil {
		a.hist = &claudeHistory{cursors: map[string]*claudeCursor{}, empty: map[string]fileStamp{}}
	}
	return a.hist
}

// session reads what fpath gained since the last pass (all of it the first time, or when the
// file was replaced, truncated or rewritten) and returns the transcript's session, or nil when
// it holds none. indexed says the index has a search body for the file (the indexer's
// knownMtimes); only then is an unchanged body marked FTSUnchanged, so a write that failed is
// made again.
func (h *claudeHistory) session(fpath string, info os.FileInfo, mtime float64, indexed bool) (*IndexedSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	stamp := stampOf(info)
	if st, ok := h.empty[fpath]; ok && st == stamp {
		return nil, nil
	}
	cur := h.cursors[fpath]
	if cur != nil && !(os.SameFile(cur.info, info) && info.Size() >= cur.offset) {
		cur = nil
	}
	if cur == nil || cur.stamp != stamp {
		next, err := readClaudeFrom(fpath, cur)
		if err != nil {
			delete(h.cursors, fpath)
			return nil, err
		}
		next.info, next.stamp = info, stamp
		cur = next
	}
	cur.used = time.Now()
	if cur.msgCount == 0 {
		delete(h.cursors, fpath)
		h.empty[fpath] = stamp
		return nil, nil
	}
	delete(h.empty, fpath)
	h.cursors[fpath] = cur
	h.evict()
	body := buildFTSBody(cur.summary, &cur.fts)
	s := &IndexedSession{
		SessionID:      strings.TrimSuffix(filepath.Base(fpath), ".jsonl"),
		SourceType:     "claude",
		SourceFile:     fpath,
		FileMtime:      mtime,
		MessageCount:   cur.msgCount,
		DisplaySummary: cur.summary,
		FTSBody:        body,
		FTSUnchanged:   indexed && body == cur.ftsSent,
	}
	if cur.firstTS != "" {
		first, last := cur.firstTS, cur.lastTS
		s.FirstTimestamp, s.LastTimestamp = &first, &last
	}
	cur.ftsSent = body
	return s, nil
}

// evict drops the least recently used cursors past maxClaudeCursors.
func (h *claudeHistory) evict() {
	for len(h.cursors) > maxClaudeCursors {
		var oldest string
		var at time.Time
		for p, c := range h.cursors {
			if oldest == "" || c.used.Before(at) {
				oldest, at = p, c.used
			}
		}
		delete(h.cursors, oldest)
	}
}

// forget drops what is remembered about files that are gone (not in seen).
func (h *claudeHistory) forget(seen map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for p := range h.cursors {
		if !seen[p] {
			delete(h.cursors, p)
		}
	}
	for p := range h.empty {
		if !seen[p] {
			delete(h.empty, p)
		}
	}
}

// readClaudeFrom continues prev's fold over fpath in place (prev nil, or a file whose bytes
// before prev's offset changed: a new fold from the start) and returns the cursor. On an error
// the caller drops it. Only whole lines are read: a line still being written is read on a later
// pass, except a last line that is already complete JSON (a transcript needn't end in a newline).
func readClaudeFrom(fpath string, prev *claudeCursor) (*claudeCursor, error) {
	f, err := openHistoryFile(fpath) // regular file, never a link or FIFO (#3601, W21)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cur := &claudeCursor{}
	if prev != nil && prev.offset > 0 {
		same, err := tailMatches(f, prev)
		if err != nil {
			return nil, err
		}
		if same {
			cur = prev
		}
	}
	if _, err := f.Seek(cur.offset, io.SeekStart); err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// A large read takes one of the process's two large-decode slots, shared with the chat and
	// goals readers (#3715).
	release, err := decodelimit.Acquire(context.Background(), fi.Size()-cur.offset)
	if err != nil {
		return nil, err
	}
	defer release()
	r := bufio.NewReaderSize(f, 64*1024)
	var long []byte // a line longer than the reader's buffer
	for {
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk...)
			continue
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			// What's left is a line still being written, or a complete last line with no newline.
			if last := append(long, chunk...); json.Valid(last) {
				cur.add(last)
				cur.offset += int64(len(last))
				cur.keepTail(last)
			}
			break
		}
		line := chunk
		if long != nil {
			long = append(long, chunk...)
			line = long
		}
		cur.add(line)
		cur.offset += int64(len(line))
		cur.keepTail(line)
		long = nil
	}
	return cur, nil
}

// tailMatches reports whether the bytes before prev.offset are still the ones prev read.
func tailMatches(f *os.File, prev *claudeCursor) (bool, error) {
	n := int64(len(prev.tail))
	if n == 0 {
		return false, nil
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, prev.offset-n); err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return bytes.Equal(buf, prev.tail), nil
}

func (c *claudeCursor) keepTail(line []byte) {
	if len(line) >= cursorTailLen {
		c.tail = append(c.tail[:0], line[len(line)-cursorTailLen:]...)
		return
	}
	c.tail = append(c.tail, line...)
	if extra := len(c.tail) - cursorTailLen; extra > 0 {
		c.tail = append(c.tail[:0], c.tail[extra:]...)
	}
}

// claudeLine is the part of a transcript line the index uses. Decoding into it, rather than a
// map, skips a tool result's content without building it (#3728: the indexer's allocations).
type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   *struct {
		Content claudeFirstText `json:"content"`
	} `json:"message"`
}

// claudeFirstText is a message's first text: the content itself when it is a string, or the
// first non-empty text block.
type claudeFirstText string

func (t *claudeFirstText) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil
	}
	switch b[0] {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = claudeFirstText(s)
	case '[':
		var blocks []struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if err := json.Unmarshal(b, &blocks); err != nil {
			return err
		}
		for _, bl := range blocks {
			if bl.Type != "text" || len(bl.Text) == 0 || bl.Text[0] != '"' {
				continue
			}
			var s string
			if json.Unmarshal(bl.Text, &s) == nil && s != "" {
				*t = claudeFirstText(s)
				return nil
			}
		}
	}
	return nil
}

// add folds one line into the cursor, as parseClaudeSessions did for the whole file.
func (c *claudeCursor) add(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var e claudeLine
	if err := json.Unmarshal(line, &e); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return // not JSON: skipped, as before
		}
		// A field of another type (a message that is a string) leaves the rest decoded.
	}
	if e.Timestamp != "" {
		if c.firstTS == "" {
			c.firstTS = e.Timestamp
		}
		c.lastTS = e.Timestamp
	}
	if e.Type != "user" && e.Type != "assistant" {
		return
	}
	c.msgCount++
	if e.Message == nil {
		return
	}
	text := string(e.Message.Content)
	c.fts.Add(text)
	if e.Type == "assistant" && c.summary == "" && text != "" {
		if len(text) > 200 {
			text = text[:200]
		}
		c.summary = text
	}
}
