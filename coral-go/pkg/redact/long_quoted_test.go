package redact

import (
	"bytes"
	"strings"
	"testing"
)

// #5002 (found by FuzzRedactBoundedStable): a quoted credential value longer than the rule's reach was redacted to there, and the rest of it
// was dropped only up to the first byte outside the token's class, so the part of the value after a space or a control character beyond
// that reach showed. A quoted value's rest runs to its closing quote, spaces and all, as it already did when the value was held across writes.
func TestOverlongQuotedValueIsDroppedToItsQuote(t *testing.T) {
	const tail = "MT07elszGNU18fmtAHOV29g"
	keys := []string{`DB_PASSWORD=`, `password=`, `token=`, `secret: `, `api_key=`}
	for _, key := range keys {
		for _, sep := range []string{" ", "\t", "\x0e", "\x01", "\x7f", "\x00", "  \n  "} {
			for _, n := range []int{300, 600, 1500, 5000} {
				in := []byte("x " + key + `"abc` + strings.Repeat(sep, n) + tail + `" after`)
				whole := Tokens(in)
				if bytes.Contains(whole, []byte(tail)) {
					t.Fatalf("%q with %d x %q: the tail of the value shows (%d bytes out)", key, n, sep, len(whole))
				}
				if !bytes.HasSuffix(whole, []byte(`" after`)) {
					t.Fatalf("%q with %d x %q: the text after the closing quote was not kept: %.80q", key, n, sep, whole[max(0, len(whole)-80):])
				}
				// Written in pieces it redacts the same.
				for _, cut := range []int{len(in) / 3, len(in) / 2, len(in) - 10} {
					if got := fuzzRedactWrites(in, cut); !bytes.Equal(got, whole) {
						t.Fatalf("%q with %d x %q: written split at %d it differs from written whole", key, n, sep, cut)
					}
				}
			}
		}
	}
}
