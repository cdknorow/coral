package routes

import (
	"bytes"
	"io"
	"os"
	"time"

	"github.com/cdknorow/coral/internal/logparse"
)

// The log status of an agent (its staleness and last lines) is read on every live-list,
// WebSocket and availability poll, for every agent, from the dashboard and Coral Hub alike. It
// read the log's last 256 KB and ran StripANSI over every line of it to keep 20 (#3711: the
// server's hottest path, 120-276% CPU on the operator's computer). Now a log's tail is kept
// between reads: an unchanged log costs a stat, a grown one only its new bytes, and only
// complete lines are cleaned, once. The answer is the one a fresh read gives (TestLogTailMatches
// a fresh read after every append, with the window shrunk so long lines cross it).

// logTailBytes is how much of a log's end the status looks at (a var for tests).
var logTailBytes int64 = 256_000

// tailLine is a complete log line, cleaned, and where it starts in the file.
type tailLine struct {
	start int64
	text  string
}

// logTail is a log's end as last read: its last recentLines complete lines, cleaned, and the
// raw bytes after its last newline.
type logTail struct {
	info      os.FileInfo
	size      int64
	modTime   time.Time
	lines     []tailLine
	end       int64 // the file offset just after the last byte read
	partStart int64
	part      []byte
	partLong  bool // the bytes after the last newline outgrew the window: that line is never shown
	// sample is the log's last bytes as read (up to tailSample): a log rewritten in place, not
	// appended to, no longer ends with them, so it is read afresh.
	sample []byte
}

// tailSample is how many of a log's last bytes are kept to tell an append from a rewrite.
const tailSample = 64

// recentLines is how many lines the status keeps (logparse.RecentLines' count).
const recentLines = 20

// add appends data, which starts at t.end (the end of what t has read), to t.
func (t *logTail) add(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			t.end += int64(len(data))
			if !t.partLong {
				t.part = append(t.part, data...)
				if int64(len(t.part)) > logTailBytes {
					t.part, t.partLong = nil, true
				}
			}
			return
		}
		// A line longer than the window starts before any window that could hold its end: a
		// fresh read drops it as the window's partial first line, so it is never kept.
		if !t.partLong {
			line := append(t.part, data[:i]...)
			t.lines = append(t.lines, tailLine{start: t.partStart, text: logparse.StripANSI(string(line))})
			if len(t.lines) > recentLines {
				t.lines = append(t.lines[:0], t.lines[len(t.lines)-recentLines:]...)
			}
		}
		t.end += int64(i) + 1
		t.partStart = t.end
		t.part, t.partLong = t.part[:0], false
		data = data[i+1:]
	}
}

// recent is what a fresh read of the log's last logTailBytes shows: its lines after the
// window's first (partial) line, or all of them when the whole log fits, the bytes after the
// last newline as the last line, the last recentLines of them.
func (t *logTail) recent() []string {
	winStart := t.size - logTailBytes
	if winStart < 0 {
		winStart = 0
	}
	keep := func(start int64) bool { return winStart == 0 || start > winStart }
	out := make([]string, 0, recentLines+1)
	for _, l := range t.lines {
		if keep(l.start) {
			out = append(out, l.text)
		}
	}
	if !t.partLong && keep(t.partStart) {
		out = append(out, logparse.StripANSI(string(t.part)))
	}
	if len(out) > recentLines {
		out = out[len(out)-recentLines:]
	}
	if len(out) == 0 {
		return nil // as logparse.RecentLines gives it (JSON null), so clients see what they did
	}
	return out
}

// readLogTail brings prev (nil for none) up to date with the log at path and returns the log's
// status and the tail to keep for next time (nil when the log can't be read). Only the bytes
// added since prev are read when the log grew by less than the window; otherwise (a new,
// shrunk or replaced log, or a big jump) the window is read afresh.
func readLogTail(path string, prev *logTail) (map[string]any, *logTail) {
	result := map[string]any{
		"status":            nil,
		"summary":           nil,
		"staleness_seconds": nil,
		"recent_lines":      []string{},
	}
	info, err := os.Stat(path)
	if err != nil {
		return result, nil
	}
	result["staleness_seconds"] = time.Since(info.ModTime()).Seconds()
	size := info.Size()
	f, err := os.Open(path)
	if err != nil {
		return result, nil
	}
	defer f.Close()

	var t *logTail
	if prev != nil && prev.info != nil && os.SameFile(prev.info, info) && size-prev.size <= logTailBytes &&
		appendedTo(f, prev, size, info.ModTime()) {
		t = &logTail{lines: append([]tailLine(nil), prev.lines...), end: prev.end, partStart: prev.partStart,
			part: append([]byte(nil), prev.part...), partLong: prev.partLong}
		t.sample = prev.sample
		if size > prev.size {
			data := make([]byte, size-prev.size)
			if _, err := f.ReadAt(data, prev.size); err != nil && err != io.EOF {
				return result, nil
			}
			t.add(data)
			t.sample = lastBytes(append(append([]byte(nil), prev.sample...), data...))
		}
	} else {
		start := size - logTailBytes
		if start < 0 {
			start = 0
		}
		data := make([]byte, size-start)
		n, err := f.ReadAt(data, start)
		if err != nil && err != io.EOF {
			return result, nil
		}
		// The window's first line may begin before it: it is kept at the window's start, which a
		// line must be after to be shown (recent), as a fresh read drops it.
		t = &logTail{end: start, partStart: start}
		t.add(data[:n])
		t.sample = lastBytes(data[:n])
	}
	t.info, t.size, t.modTime = info, size, info.ModTime()
	result["recent_lines"] = t.recent()
	return result, t
}

// appendedTo reports whether the log f (now size bytes, modified at mod) is prev's log with
// bytes appended: no shorter, not older, still ending (at prev's size) with prev's last bytes,
// and if no longer, unmodified.
func appendedTo(f *os.File, prev *logTail, size int64, mod time.Time) bool {
	if size < prev.size || mod.Before(prev.modTime) || size == prev.size && !mod.Equal(prev.modTime) {
		return false
	}
	if len(prev.sample) == 0 {
		return prev.size == 0
	}
	got := make([]byte, len(prev.sample))
	n, err := f.ReadAt(got, prev.size-int64(len(prev.sample)))
	return (err == nil || err == io.EOF) && n == len(got) && bytes.Equal(got, prev.sample)
}

// lastBytes is the last tailSample bytes of b, copied.
func lastBytes(b []byte) []byte {
	if len(b) > tailSample {
		b = b[len(b)-tailSample:]
	}
	return append([]byte(nil), b...)
}
