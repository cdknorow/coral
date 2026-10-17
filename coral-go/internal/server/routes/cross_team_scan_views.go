package routes

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"html"
	"regexp"
	"unicode/utf8"

	"github.com/cdknorow/coral/internal/secrets"
)

// The sender-side scan of a text attachment looks at the file as it is and at a few re-encodings of it
// (#4439, Security's #4438 SHOULD 1): a token that is base64 or hex, percent-encoded, an HTML entity,
// split by white space, a backtick or a quote, or written backwards. Each view is scanned by the same
// rules as the file itself. It is a net, not a boundary: cross-team.md lists what it does not catch.

const (
	// scanMaxRuns bounds the base64 and hex runs that decode to text (the whole file, all levels together).
	scanMaxRuns = 256
	// scanMaxDecoded bounds the decoded bytes handed to the scan, so a file of nothing but encoded text
	// cannot cost more than the budgeted scan of an ordinary file.
	scanMaxDecoded = 2 << 20
	// scanMaxDepth: a base64 of a base64 of a token is found; a third layer is not.
	scanMaxDepth = 2
)

var (
	encodedRunRe = regexp.MustCompile(`[A-Za-z0-9+/_-]{24,}={0,2}`)
	// a line that is only base64 (a wrapped block, as the base64 tool writes it).
	encodedLineRe = regexp.MustCompile(`^[ \t]*[A-Za-z0-9+/_-]{16,}={0,2}[ \t]*\r?$`)
)

// scanOutcome is what scanSecrets found.
type scanOutcome int

const (
	scanClean     scanOutcome = iota
	scanFound                 // a token or key, in the text or in one of its views
	scanUnchecked             // the budget ran out, or the scan could not finish: fails closed
)

// scanSecrets runs Coral's secret rules over text and its re-encoded views (scanViews). It is the scan of
// an attachment and of the text of a message to another Coral, so they cannot differ.
func scanSecrets(data []byte) scanOutcome {
	views, ok := scanViews(data)
	if !ok {
		return scanUnchecked
	}
	for _, v := range append([][]byte{data}, views...) {
		out, err := secrets.Redactor().Text(v)
		if err != nil {
			return scanUnchecked
		}
		if !bytes.Equal(out, v) {
			return scanFound
		}
	}
	return scanClean
}

// scanViews returns the re-encoded views of data to scan besides data itself. ok is false when a bound
// ran out (more encoded runs or decoded bytes than the scan takes): the caller fails closed.
func scanViews(data []byte) (views [][]byte, ok bool) {
	add := func(v []byte) {
		if len(v) > 0 && !bytes.Equal(v, data) {
			views = append(views, v)
		}
	}
	if bytes.IndexByte(data, '%') >= 0 {
		add(percentDecode(data))
	}
	if bytes.IndexByte(data, '&') >= 0 {
		add([]byte(html.UnescapeString(string(data))))
	}
	add(dropSplitters(data))
	add(reverseBytes(data))

	runs, decoded := 0, 0
	var out bytes.Buffer
	level := [][]byte{data, joinEncodedLines(data)}
	for depth := 0; depth < scanMaxDepth; depth++ {
		var next [][]byte
		for _, text := range level {
			for _, run := range candidateRuns(text) {
				// A run counts only once it decodes to text: paths, URLs and long identifiers match the
				// candidate pattern and decode to nothing, and a log full of them is not a refusal.
				d := decodeRun(run)
				if len(d) == 0 || !mostlyText(d) {
					continue
				}
				if runs++; runs > scanMaxRuns {
					return nil, false
				}
				if decoded += len(d); decoded > scanMaxDecoded {
					return nil, false
				}
				out.Write(d)
				out.WriteByte('\n')
				next = append(next, d)
			}
		}
		level = next
	}
	add(out.Bytes())
	return views, true
}

// candidateRuns are the runs of base64 or hex characters (hex is a subset of base64's) long enough to
// hold a token.
func candidateRuns(text []byte) [][]byte { return encodedRunRe.FindAll(text, -1) }

// decodeRun decodes a run as hex when it is all hex, else as base64 (standard or URL alphabet, padded
// or not); nil when it is neither.
func decodeRun(run []byte) []byte {
	if len(run)%2 == 0 {
		if b, err := hex.DecodeString(string(run)); err == nil {
			return b
		}
	}
	s := bytes.TrimRight(run, "=")
	if len(s)%4 == 1 {
		s = s[:len(s)-1]
	}
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(string(s)); err == nil {
			return b
		}
	}
	return nil
}

// mostlyText: a decoded run is worth scanning when it is valid text. Random bytes from an ordinary
// long word are not, and are not scanned.
func mostlyText(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	printable := 0
	for _, c := range b {
		if c >= 0x20 && c < 0x7f || c == '\n' || c == '\r' || c == '\t' || c >= 0x80 {
			printable++
		}
	}
	return printable*10 >= len(b)*9
}

// joinEncodedLines joins the lines of a wrapped base64 block (consecutive lines that hold nothing else)
// so the block is one run; other lines are dropped from this view.
func joinEncodedLines(data []byte) []byte {
	var out bytes.Buffer
	prev := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		if encodedLineRe.Match(line) {
			out.Write(bytes.TrimSpace(line))
			prev = true
			continue
		}
		if prev {
			out.WriteByte('\n')
		}
		prev = false
	}
	return out.Bytes()
}

// dropSplitters joins the fragments of a split token: a run of white space, backticks, quotes and plus
// signs between two token fragments is removed ("ghp_ab" + "cd" becomes ghp_abcd) when one of the two
// holds a digit or an underscore, as tokens do and prose does not: joining every word would turn
// "password: value does not" into a long string the keyword rules call a secret. A run that touches a
// ":" or "=" is left alone for the same reason.
func dropSplitters(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if !isSplitter(data[i]) {
			out = append(out, data[i])
			i++
			continue
		}
		j := i
		for j < len(data) && isSplitter(data[j]) {
			j++
		}
		if i > 0 && j < len(data) && isTokenChar(data[i-1]) && isTokenChar(data[j]) {
			k := i
			for k > 0 && isTokenChar(data[k-1]) {
				k--
			}
			e := j
			for e < len(data) && isTokenChar(data[e]) {
				e++
			}
			if tokenish(data[k:i]) || tokenish(data[j:e]) {
				i = j
				continue
			}
		}
		out = append(out, data[i:j]...)
		i = j
	}
	return out
}

// tokenish: a fragment with a digit or an underscore in it.
func tokenish(f []byte) bool {
	for _, c := range f {
		if c == '_' || c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

func isSplitter(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '`', '"', '\'', '+':
		return true
	}
	return false
}

func isTokenChar(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '-'
}

func reverseBytes(data []byte) []byte {
	out := make([]byte, len(data))
	for i, c := range data {
		out[len(data)-1-i] = c
	}
	return out
}

// percentDecode decodes every %XX; an invalid escape is left as it is.
func percentDecode(data []byte) []byte {
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == '%' && i+2 < len(data) {
			if hi, ok1 := unhex(data[i+1]); ok1 {
				if lo, ok2 := unhex(data[i+2]); ok2 {
					out = append(out, hi<<4|lo)
					i += 2
					continue
				}
			}
		}
		out = append(out, data[i])
	}
	return out
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
