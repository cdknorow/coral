package jsonl

import (
	"encoding/json"
	"os"
)

// TranscriptCursor reads an append-only JSONL transcript incrementally: each
// Scan delivers only the complete lines written since the previous one. The
// zero value is ready to use.
type TranscriptCursor struct {
	state transcriptReadState
}

// Scan calls consume for every complete (newline-terminated) line appended to
// path since the last Scan; a line still being written is held back until its
// newline arrives. A held line that is already complete JSON is delivered
// once a later Scan finds the file unchanged, so a transcript whose writer
// stopped without a final newline is not left unread. When the file was
// truncated, replaced or rewritten, reset is called first and reading starts
// over from the top; it is also called when the file cannot be read.
func (c *TranscriptCursor) Scan(path string, reset func(), consume func(line []byte)) {
	before := c.state.info
	scanTranscriptAppend(path, &c.state, reset, consume)
	if len(c.state.partial) == 0 || before == nil || c.state.info != before {
		return // nothing held, or this scan read new data (or reset)
	}
	if info, err := os.Stat(path); err != nil || !sameTranscriptSnapshot(before, info) {
		return
	}
	if json.Valid(c.state.partial) {
		line := c.state.partial
		c.state.partial = nil
		consume(line)
	}
}

// Pending reports whether a partial last line is held back, so a caller that
// skips unchanged files should still call Scan to deliver it.
func (c *TranscriptCursor) Pending() bool { return len(c.state.partial) > 0 }
