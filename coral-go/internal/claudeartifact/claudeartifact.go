// Package claudeartifact finds claude.ai artifact links in text (#6583): Coral reminds an agent that publishes one to make a Coral artifact instead.
// The scan is strict: the host is exactly claude.ai, the id is 8 to 64 of [A-Za-z0-9_-], and nothing else is read from the page.
package claudeartifact

import (
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/safefs"
)

const (
	minIDLen = 8
	maxIDLen = 64
	// MaxScanBytes is how much of a transcript's end is read for the newest assistant text.
	MaxScanBytes = 64 << 10
	// MaxIDs is how many ids one text may give.
	MaxIDs = 8
)

// ValidID reports whether id has the shape of a claude.ai artifact id.
func ValidID(id string) bool {
	if len(id) < minIDLen || len(id) > maxIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !idByte(id[i]) {
			return false
		}
	}
	return true
}

func idByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// LinkIDs is the artifact ids of the https://claude.ai/artifact/<id> and https://claude.ai/code/artifact/<id> links in text, in order, without repeats, at
// most MaxIDs. A look-alike host (claude.ai.example.com, notclaude.ai) or an id that is too short or too long is not one.
func LinkIDs(text string) []string {
	const host = "https://claude.ai/"
	var ids []string
	seen := map[string]bool{}
	for rest := text; len(ids) < MaxIDs; {
		i := strings.Index(rest, host)
		if i < 0 {
			break
		}
		rest = rest[i+len(host):]
		path := rest
		switch {
		case strings.HasPrefix(path, "artifact/"):
			path = path[len("artifact/"):]
		case strings.HasPrefix(path, "code/artifact/"):
			path = path[len("code/artifact/"):]
		default:
			continue
		}
		n := 0
		for n < len(path) && idByte(path[n]) {
			n++
		}
		if n < minIDLen || n > maxIDLen {
			continue
		}
		if id := path[:n]; !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// LastAssistantIDs is the artifact ids in the newest assistant text of the Claude transcript at path: the last MaxScanBytes of the file only, never a
// partial first line, read one line at a time, and only the link scan runs on the text. Nothing is returned for a file that cannot be opened safely (a link,
// a device, a folder) or read.
func LastAssistantIDs(path string) []string {
	if path == "" {
		return nil
	}
	// A regular file, never through a link as its last name, opened relative to its own folder (the transcript is the agent's, so what is in it is data).
	root, err := safefs.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil
	}
	defer root.Close()
	f, st, err := root.Open(filepath.Base(path))
	if err != nil {
		return nil
	}
	defer f.Close()
	start := int64(0)
	if st.Size() > MaxScanBytes {
		start = st.Size() - MaxScanBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	r := bufio.NewReaderSize(f, 1<<16)
	if start > 0 {
		if _, err := r.ReadBytes('\n'); err != nil { // the first line is cut by the window
			return nil
		}
	}
	var lastText string
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if t := assistantText(line); t != "" {
				lastText = t
			}
		}
		if err != nil {
			break
		}
	}
	return LinkIDs(lastText)
}

// assistantText is the text blocks of one transcript line when it is an assistant message; "" otherwise.
func assistantText(line []byte) string {
	var e struct {
		Type    string `json:"type"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "assistant" {
		return ""
	}
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
