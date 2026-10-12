package jsonl

// TranscriptCursor reads an append-only JSONL transcript incrementally: each
// Scan delivers only the complete lines written since the previous one. The
// zero value is ready to use.
type TranscriptCursor struct {
	state transcriptReadState
}

// Scan calls consume for every complete (newline-terminated) line appended to
// path since the last Scan; a line still being written is held back until its
// newline arrives. When the file was truncated, replaced or rewritten, reset
// is called first and reading starts over from the top; it is also called
// when the file cannot be read.
func (c *TranscriptCursor) Scan(path string, reset func(), consume func(line []byte)) {
	scanTranscriptAppend(path, &c.state, reset, consume)
}
