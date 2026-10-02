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
	info, err := os.Stat(path)
	if err != nil {
		*state = transcriptReadState{}
		reset()
		return
	}
	if sameTranscriptSnapshot(state.info, info) {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return
	}
	head := transcriptHead(f)
	if !state.initialized || transcriptReplaced(state.info, info, state.head, head) {
		*state = transcriptReadState{initialized: true}
		reset()
	}
	if _, err := f.Seek(state.offset, io.SeekStart); err != nil {
		return
	}
	// A concurrent writer cannot make this refresh run indefinitely.
	reader := bufio.NewReader(io.LimitReader(f, info.Size()-state.offset))
	for {
		line, readErr := reader.ReadBytes('\n')
		state.offset += int64(len(line))
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
