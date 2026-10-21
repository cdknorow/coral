package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/decodelimit"
)

// Bounds of FindImage: a transcript line holding an image is about the image's size plus a quarter, and one longer than maxImageLine is skipped, not
// read; an image over maxImageBytes decoded is ErrImageTooLarge.
var (
	maxImageLine  = 48 << 20 // vars so a test can lower them
	maxImageBytes = 20 << 20
)

const (
	maxImageDepth      = 12
	maxIndexedSessions = 32   // transcripts whose image index is kept (the least recently used goes first)
	maxIndexedRefs     = 4096 // images one transcript's index remembers
)

var (
	// ErrImageNotFound: no image block in the transcript hashes to the reference.
	ErrImageNotFound = errors.New("no such image in the transcript")
	// ErrImageTooLarge: the image is over the cap (or its line was too long to read).
	ErrImageTooLarge = errors.New("the image is too large to show")
)

// imageLoc is where a transcript line holding an image starts, and how long it is (newline excluded).
type imageLoc struct{ off, n int64 }

// imageIndex is what one transcript's scans have learned: which line holds each image (by reference), and how far the transcript has been scanned. A chat of N
// images asks for N references; without the index each ask decoded every image line from the top again (N x the transcript's bytes), with it the transcript is read
// once, a little at a time as new references are asked for, and each image is then one line read at its offset. It belongs to one transcript file (the file's identity
// and size are checked on every use: a file that was replaced, rotated or truncated is scanned afresh) and is never shared between transcripts.
type imageIndex struct {
	mu      sync.Mutex
	fi      os.FileInfo
	mod     time.Time // the file's modification time when it was last scanned: older now means it was replaced
	scanned int64     // the transcript has been read up to here (the end of the last complete line)
	refs    map[string]imageLoc
}

var (
	indexMu    sync.Mutex
	indexes    = map[string]*imageIndex{} // by agent type and path
	indexOrder []string                   // least recently used first
	// imageScanBytes counts the transcript bytes FindImage has decoded or read, for the tests and the benchmark.
	imageScanBytes atomic.Int64
)

func indexFor(key string) *imageIndex {
	indexMu.Lock()
	defer indexMu.Unlock()
	for i, k := range indexOrder {
		if k == key {
			indexOrder = append(append(indexOrder[:i:i], indexOrder[i+1:]...), key)
			return indexes[key]
		}
	}
	if len(indexOrder) >= maxIndexedSessions {
		delete(indexes, indexOrder[0])
		indexOrder = indexOrder[1:]
	}
	ix := &imageIndex{refs: map[string]imageLoc{}}
	indexes[key] = ix
	indexOrder = append(indexOrder, key)
	return ix
}

// ForgetImageIndex drops what is known of one transcript (a session that ended).
func ForgetImageIndex(path, agentType string) {
	indexMu.Lock()
	defer indexMu.Unlock()
	key := agentType + "\x00" + path
	if _, ok := indexes[key]; !ok {
		return
	}
	delete(indexes, key)
	for i, k := range indexOrder {
		if k == key {
			indexOrder = append(indexOrder[:i:i], indexOrder[i+1:]...)
			break
		}
	}
}

// FindImage finds the image a chip's reference (agent.ImageRef) names in the transcript at path and returns its decoded bytes. A pasted image is recorded in the
// transcript itself: Claude as a content block {type: "image", source: {type: "base64", data}}, Codex as a data URL (input_image's image_url, or the event's
// images list). Each line that can hold one is decoded and its images hashed (and remembered, line by line, in the transcript's index); the first match is returned.
// The reference carries nothing the caller chose but 32 hex characters: it names no path and no offset, and a transcript has the image or it does not. What the bytes
// are (PNG, JPEG, GIF, WebP) is the caller's to check.
func FindImage(ctx context.Context, path, agentType, ref string) ([]byte, error) {
	if !agent.ValidImageRef(ref) {
		return nil, ErrImageNotFound
	}
	f, err := openTranscript(path, agentType)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	ix := indexFor(agentType + "\x00" + path)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	// The same file as when it was scanned, and no shorter: otherwise it was replaced, rotated or truncated, and what the index says is not what is there.
	if ix.fi == nil || !os.SameFile(ix.fi, fi) || fi.Size() < ix.scanned || fi.ModTime().Before(ix.mod) {
		ix.fi, ix.scanned, ix.refs = fi, 0, map[string]imageLoc{}
	}
	ix.mod = fi.ModTime()
	if loc, ok := ix.refs[ref]; ok {
		if b64, ok := readLocated(ctx, f, loc, ref); ok {
			return decodeImage(b64)
		}
		ix.fi, ix.scanned, ix.refs = fi, 0, map[string]imageLoc{} // the line is not there any more (the file was edited in place): start again
	}
	b64, err := ix.scan(ctx, f, fi.Size(), ref)
	if err != nil {
		return nil, err
	}
	return decodeImage(b64)
}

// readLocated reads the one line the index names and checks that it still holds the image.
func readLocated(ctx context.Context, f *os.File, loc imageLoc, ref string) (string, bool) {
	if loc.n <= 0 || loc.n > int64(maxImageLine) {
		return "", false
	}
	line := make([]byte, loc.n)
	if _, err := f.ReadAt(line, loc.off); err != nil {
		return "", false
	}
	imageScanBytes.Add(loc.n)
	for _, b64 := range imagesInLine(ctx, line) {
		if agent.ImageRef(b64) == ref {
			return b64, true
		}
	}
	return "", false
}

// scan reads the transcript from where the index stopped, decoding the lines that can hold an image and remembering each image's line, until the wanted reference is
// found (the scan then stops at the end of that line) or the transcript ends.
func (ix *imageIndex) scan(ctx context.Context, f *os.File, size int64, want string) (string, error) {
	r := bufio.NewReaderSize(io.NewSectionReader(f, ix.scanned, size-ix.scanned), 1<<20)
	off := ix.scanned
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		line, n, terminated, skipped, err := readLineBounded(r, maxImageLine)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if n == 0 {
			return "", ErrImageNotFound
		}
		if !terminated { // a line still being written: not read yet, it is the next scan's
			return "", ErrImageNotFound
		}
		imageScanBytes.Add(int64(n))
		var found string
		if !skipped && mayHoldImage(line) {
			for _, b64 := range imagesInLine(ctx, line) {
				ref := agent.ImageRef(b64)
				if len(ix.refs) < maxIndexedRefs {
					ix.refs[ref] = imageLoc{off: off, n: int64(len(bytes.TrimRight(line, "\r")))}
				}
				if ref == want {
					found = b64
				}
			}
		}
		off += int64(n)
		ix.scanned = off
		if found != "" {
			return found, nil
		}
		if err != nil { // EOF after a complete line
			return "", ErrImageNotFound
		}
	}
}

func decodeImage(b64 string) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(b64)) > maxImageBytes+4 {
		return nil, ErrImageTooLarge
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(b64); err != nil {
			return nil, ErrImageNotFound
		}
	}
	if len(data) > maxImageBytes {
		return nil, ErrImageTooLarge
	}
	return data, nil
}

// readLineBounded reads one line without letting it grow past max (a longer line is consumed and reported as skipped). n is the bytes consumed, newline included;
// terminated says the line ended with a newline (a last line without one is still being written). The returned line has no newline.
func readLineBounded(r *bufio.Reader, max int) (line []byte, n int, terminated, skipped bool, err error) {
	for {
		chunk, rerr := r.ReadSlice('\n')
		n += len(chunk)
		if !skipped {
			if len(line)+len(chunk) > max+1 {
				skipped, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		if rerr != nil {
			return trimNL(line), n, false, skipped, rerr
		}
		return trimNL(line), n, true, skipped, nil
	}
}

func trimNL(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b
}

// mayHoldImage is the cheap test before a line is decoded: it names an image block or a data URL.
func mayHoldImage(line []byte) bool {
	return bytes.Contains(line, []byte(`"image"`)) || bytes.Contains(line, []byte("data:image/"))
}

// imagesInLine decodes one transcript line and returns the base64 text of each image in it.
func imagesInLine(ctx context.Context, line []byte) []string {
	release, err := decodelimit.Acquire(ctx, int64(len(line)))
	if err != nil {
		return nil
	}
	defer release()
	var entry any
	if json.Unmarshal(line, &entry) != nil {
		return nil
	}
	var out []string
	collectImages(entry, 0, &out)
	return out
}

// collectImages walks a decoded entry (to a bounded depth) for image blocks and image data URLs.
func collectImages(v any, depth int, out *[]string) {
	if depth > maxImageDepth {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		if x["type"] == "image" {
			if src, _ := x["source"].(map[string]any); src != nil && src["type"] == "base64" {
				if data, _ := src["data"].(string); data != "" {
					*out = append(*out, data)
				}
			}
		}
		for _, e := range x {
			collectImages(e, depth+1, out)
		}
	case []any:
		for _, e := range x {
			collectImages(e, depth+1, out)
		}
	case string:
		if len(x) > 20 && x[0] == 'd' {
			if payload, ok := agent.DataURLBase64(x); ok {
				*out = append(*out, payload)
			}
		}
	}
}
