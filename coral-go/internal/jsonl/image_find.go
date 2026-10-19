package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"github.com/cdknorow/coral/internal/agent"
	"github.com/cdknorow/coral/internal/decodelimit"
)

// Bounds of FindImage: a transcript line holding an image is about the image's size plus a quarter, and one longer than maxImageLine is skipped, not
// read; an image over maxImageBytes decoded is ErrImageTooLarge.
var (
	maxImageLine  = 48 << 20 // vars so a test can lower them
	maxImageBytes = 20 << 20
)

const maxImageDepth = 12

var (
	// ErrImageNotFound: no image block in the transcript hashes to the reference.
	ErrImageNotFound = errors.New("no such image in the transcript")
	// ErrImageTooLarge: the image is over the cap (or its line was too long to read).
	ErrImageTooLarge = errors.New("the image is too large to show")
)

// FindImage finds the image a chip's reference (agent.ImageRef) names in the transcript at path and returns its decoded bytes. A pasted image is recorded in the
// transcript itself: Claude as a content block {type: "image", source: {type: "base64", data}}, Codex as a data URL (input_image's image_url, or the event's
// images list). Each line that can hold one is decoded and its images hashed; the first match is returned. The reference carries nothing the caller chose but
// 32 hex characters: it names no path and no offset, and a transcript has the image or it does not. What the bytes are (PNG, JPEG, GIF, WebP) is the caller's to check.
func FindImage(ctx context.Context, path, agentType, ref string) ([]byte, error) {
	if !agent.ValidImageRef(ref) {
		return nil, ErrImageNotFound
	}
	f, err := openTranscript(path, agentType)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, skipped, err := readBoundedLine(r, maxImageLine)
		if len(line) > 0 && !skipped && mayHoldImage(line) {
			b64, found, derr := imageInLine(ctx, line, ref)
			if derr != nil {
				return nil, derr
			}
			if found {
				if base64.StdEncoding.DecodedLen(len(b64)) > maxImageBytes+4 {
					return nil, ErrImageTooLarge
				}
				data, derr := base64.StdEncoding.DecodeString(b64)
				if derr != nil {
					if data, derr = base64.RawStdEncoding.DecodeString(b64); derr != nil {
						return nil, ErrImageNotFound
					}
				}
				if len(data) > maxImageBytes {
					return nil, ErrImageTooLarge
				}
				return data, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, ErrImageNotFound
			}
			return nil, err
		}
	}
}

// readBoundedLine reads one line without letting it grow past max: a longer line is consumed and reported as skipped.
func readBoundedLine(r *bufio.Reader, max int) (line []byte, skipped bool, err error) {
	for {
		chunk, isPrefix, rerr := r.ReadLine()
		if rerr != nil {
			return line, skipped, rerr
		}
		if !skipped {
			if len(line)+len(chunk) > max {
				skipped, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if !isPrefix {
			return line, skipped, nil
		}
	}
}

// mayHoldImage is the cheap test before a line is decoded: it names an image block or a data URL.
func mayHoldImage(line []byte) bool {
	return bytes.Contains(line, []byte(`"image"`)) || bytes.Contains(line, []byte("data:image/"))
}

// imageInLine decodes one transcript line and returns the base64 text of the first image in it whose reference is ref.
func imageInLine(ctx context.Context, line []byte, ref string) (string, bool, error) {
	release, err := decodelimit.Acquire(ctx, int64(len(line)))
	if err != nil {
		return "", false, err
	}
	defer release()
	var entry any
	if json.Unmarshal(line, &entry) != nil {
		return "", false, nil
	}
	b64, ok := findImageValue(entry, ref, 0)
	return b64, ok, nil
}

// findImageValue walks a decoded entry (to a bounded depth) for an image block or an image data URL whose base64 text hashes to ref.
func findImageValue(v any, ref string, depth int) (string, bool) {
	if depth > maxImageDepth {
		return "", false
	}
	switch x := v.(type) {
	case map[string]any:
		if x["type"] == "image" {
			if src, _ := x["source"].(map[string]any); src != nil && src["type"] == "base64" {
				if data, _ := src["data"].(string); data != "" && agent.ImageRef(data) == ref {
					return data, true
				}
			}
		}
		for _, e := range x {
			if b64, ok := findImageValue(e, ref, depth+1); ok {
				return b64, true
			}
		}
	case []any:
		for _, e := range x {
			if b64, ok := findImageValue(e, ref, depth+1); ok {
				return b64, true
			}
		}
	case string:
		if len(x) > 20 && x[0] == 'd' {
			if payload, ok := agent.DataURLBase64(x); ok && agent.ImageRef(payload) == ref {
				return payload, true
			}
		}
	}
	return "", false
}
