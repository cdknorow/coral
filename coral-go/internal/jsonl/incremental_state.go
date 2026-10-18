package jsonl

import (
	"bufio"
	"io"
	"os"
	"strings"
	"time"
)

// Lifecycle refresh retains one incomplete record, not the transcript. Cold
// scans also stream records so peak memory does not grow with file length.
type transcriptReadState struct {
	initialized bool
	info        os.FileInfo
	size        int64
	modTime     time.Time
	head        string
	offset      int64
	partial     []byte
	from        int64 // where the first read of this file began: 0 for a whole read, else a tail's start (scanTranscriptAppendTail)
}

func sameTranscriptSnapshot(previous, current os.FileInfo) bool {
	return previous != nil && os.SameFile(previous, current) && previous.Size() == current.Size() && previous.ModTime().Equal(current.ModTime())
}

func transcriptHead(f *os.File) string {
	var head [512]byte
	n, _ := f.ReadAt(head[:], 0)
	return string(head[:n])
}

func transcriptReplaced(previous, current os.FileInfo, oldHead, head string) bool {
	return previous != nil && (!os.SameFile(previous, current) || current.Size() < previous.Size() ||
		(current.Size() == previous.Size() && !current.ModTime().Equal(previous.ModTime())) ||
		!strings.HasPrefix(head, oldHead))
}

func scanTranscriptAppend(path string, state *transcriptReadState, reset func(), consume func([]byte)) {
	scanTranscriptAppendTail(path, state, reset, consume, 0)
}

// scanTranscriptAppendTail is scanTranscriptAppend with one difference for a file not read before (or one that was replaced): with
// tail > 0 and a longer file it reads only the last tail bytes, from the first record that begins after their start. The reader then
// holds the file's state as of its end, and the next call reads what was appended, as ever. A reader whose state is the latest value of
// each field (the newest record that has it) gets the same answer from a long enough tail as from the whole file, and says whether
// it did (state.from is 0 when the whole file was read).
func scanTranscriptAppendTail(path string, state *transcriptReadState, reset func(), consume func([]byte), tail int64) {
	info, err := os.Stat(path)
	if err != nil {
		*state = transcriptReadState{}
		reset()
		return
	}
	if sameTranscriptSnapshot(state.info, info) {
		return
	}
	f, err := openTranscriptFile(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return
	}
	head := transcriptHead(f)
	skipFirst := false
	if !state.initialized || transcriptReplaced(state.info, info, state.head, head) {
		*state = transcriptReadState{initialized: true}
		reset()
		if tail > 0 && info.Size() > tail {
			state.offset, state.from, skipFirst = info.Size()-tail, info.Size()-tail, true
		}
	}
	if _, err := f.Seek(state.offset, io.SeekStart); err != nil {
		return
	}
	// A concurrent writer cannot make this refresh run indefinitely.
	reader := bufio.NewReader(io.LimitReader(f, info.Size()-state.offset))
	for {
		line, readErr := reader.ReadBytes('\n')
		state.offset += int64(len(line))
		if skipFirst {
			skipFirst, line = false, nil // the tail began inside a record: what is left of it is not one
		}
		if len(state.partial) > 0 {
			line = append(state.partial, line...)
			state.partial = nil
		}
		if len(line) > 0 {
			if line[len(line)-1] == '\n' {
				consume(line)
			} else {
				state.partial = line
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				state.info, state.size, state.modTime, state.head = info, info.Size(), info.ModTime(), head
			}
			return
		}
	}
}
