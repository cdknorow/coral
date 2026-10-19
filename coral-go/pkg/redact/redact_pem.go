package redact

import (
	"bytes"
)

// PEM private-key blocks: "-----BEGIN <LABEL>PRIVATE KEY-----" ... "-----END <LABEL>-----".
var (
	pemBegin  = []byte("-----BEGIN ")
	pemKeyEnd = []byte("PRIVATE KEY")
	pemTails  = [][]byte{[]byte("-----"), []byte(" BLOCK-----")} // "...PRIVATE KEY-----", PGP's "...PRIVATE KEY BLOCK-----"
	pemEnd    = []byte("-----END ")
	pemDashes = []byte("-----")
)

const maxPEMLabel = 64

// pemLabel checks for "-----BEGIN <LABEL>PRIVATE KEY-----" (or PGP's "...PRIVATE KEY BLOCK-----")
// at b and returns its length.
func pemLabel(b []byte) (n int, ok, need bool) {
	rest := b[len(pemBegin):]
	limit := min(len(rest), maxPEMLabel)
	if k := bytes.Index(rest[:limit], pemKeyEnd); k >= 0 {
		for _, c := range rest[:k] {
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ') {
				return 0, false, false
			}
		}
		after := rest[k+len(pemKeyEnd):]
		for _, tail := range pemTails {
			if bytes.HasPrefix(after, tail) {
				return len(pemBegin) + k + len(pemKeyEnd) + len(tail), true, false
			}
		}
		for _, tail := range pemTails {
			if len(after) < len(tail) && bytes.HasPrefix(tail, after) {
				return 0, false, true // the label's end is still arriving
			}
		}
		return 0, false, false
	}
	if len(rest) >= maxPEMLabel {
		return 0, false, false
	}
	for _, c := range rest { // still arriving, unless it holds a character no label has
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ' || c == '-') {
			return 0, false, false
		}
	}
	return 0, false, true
}

// pemClose finds where an open private key ends in b: after its "-----END <LABEL>-----"
// marker, or at (not after) the first unescaped '"', the end of the JSON string holding it.
// Inside a JSON string a quote is written \" (#3694): that '"' is part of the key's text, and
// stopping at it dropped the backslash and closed the string early, which left the rest of the
// string as bare JSON. esc says whether the byte dropped just before b was an unpaired
// backslash (a chunk boundary can fall between a backslash and what it escapes). When the end
// isn't found, hold is how many trailing bytes might begin the END marker, and escAfter is the
// escape state after the bytes consumed (all but hold).
func pemClose(b []byte, final, esc bool) (end int, found bool, hold int, escAfter bool) {
	q := unescapedQuote(b, esc)
	for e := bytes.Index(b, pemEnd); e >= 0 && (q < 0 || e < q); {
		rest := b[e+len(pemEnd):]
		if k := bytes.Index(rest[:min(len(rest), maxPEMLabel)], pemDashes); k >= 0 {
			return e + len(pemEnd) + k + len(pemDashes), true, 0, false
		}
		if len(rest) < maxPEMLabel && q < 0 && !final {
			return 0, false, len(b) - e, escapeAfter(b[:e], esc) // the END line may finish in the next chunk
		}
		next := bytes.Index(b[e+1:], pemEnd)
		if next < 0 {
			break
		}
		e += 1 + next
	}
	if q >= 0 {
		return q, true, 0, false
	}
	hold = partialSuffix(b, pemEnd)
	return 0, false, hold, escapeAfter(b[:len(b)-hold], esc)
}
