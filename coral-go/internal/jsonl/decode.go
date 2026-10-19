package jsonl

import (
	"bytes"
	"encoding/json"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/cdknorow/coral/internal/safego"
)

// decodedLines counts the lines decodeLines has decoded (tests: concurrent readers share a read).
var decodedLines atomic.Int64

// parallelDecodeMin is the read size from which decodeLines decodes on every CPU (a var for tests).
var parallelDecodeMin = 1 << 20

// decodeLines decodes each non-blank line of data as a JSON object, in order; a line that isn't
// one is nil. Large data is decoded by one worker per CPU, each on a contiguous run of lines.
func decodeLines(data []byte) []map[string]any {
	var lines [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		var line []byte
		if i < 0 {
			line, data = data, nil
		} else {
			line, data = data[:i], data[i+1:]
		}
		if line = bytes.TrimSpace(line); len(line) > 0 {
			lines = append(lines, line)
		}
	}
	out := make([]map[string]any, len(lines))
	decodedLines.Add(int64(len(lines)))
	decode := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			var entry map[string]any
			if json.Unmarshal(lines[i], &entry) == nil {
				out[i] = entry
			}
		}
	}
	workers := runtime.GOMAXPROCS(0)
	if total := len(lines); workers < 2 || total < 2*workers || sumLen(lines) < parallelDecodeMin {
		decode(0, total)
		return out
	}
	var wg sync.WaitGroup
	per := (len(lines) + workers - 1) / workers
	for lo := 0; lo < len(lines); lo += per {
		hi := min(lo+per, len(lines))
		wg.Add(1)
		go func() { defer safego.Recover("jsonl/reader:2001"); defer wg.Done(); decode(lo, hi) }()
	}
	wg.Wait()
	return out
}

func sumLen(lines [][]byte) int {
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	return n
}
