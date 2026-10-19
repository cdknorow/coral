package redact

import (
	"bytes"
	"strings"
	"testing"
)

// #5018 (found reviewing #5002): a quoted value over the rule's reach was dropped to the first quote OR backslash, so an ESCAPED quote inside it
// ended the drop and the rest of the value showed. The drop now reads escapes: a quote closes the value when the backslashes before it are an
// even number or exactly those of the opening spelling (\" in a JSON string); any other odd number is a quote inside the value.
func TestOverlongQuotedValueWithEscapedQuotesIsDroppedWhole(t *testing.T) {
	const tail = "MT07elszGNU18fmtAHOV29g"
	pad := strings.Repeat("a b ", 160) // 640 bytes with spaces: past the rule's reach
	cases := []struct{ name, in, after string }{
		{"double quote, one escaped quote", `x password="abc` + pad + `x\"y ` + tail + `" after`, `" after`},
		{"double quote, three escaped quotes", `x password="abc` + pad + `x\"y\"z\"w ` + tail + `" after`, `" after`},
		{"single quote", `x password='abc` + pad + `x\'y ` + tail + `' after`, `' after`},
		{"env style key", `DB_PASSWORD="abc` + pad + `x\"y ` + tail + `" after`, `" after`},
		{"a backslash that escapes nothing", `x token="abc` + pad + `C:\dir\file ` + tail + `" after`, `" after`},
		{"an escaped backslash then an escaped quote", `x password="abc` + pad + `x\\\"y ` + tail + `" after`, `" after`},
		{"inside a JSON string", `{"msg":"x password=\"abc` + pad + `x\\\"y ` + tail + `\" after"}`, `\" after"}`},
		{"inside a JSON string, escaped quotes twice", `{"msg":"x password=\"abc` + pad + `x\\\"y\\\"z ` + tail + `\" after"}`, `\" after"}`},
	}
	for _, c := range cases {
		in := []byte(c.in)
		whole := Tokens(in)
		if bytes.Contains(whole, []byte(tail)) {
			t.Errorf("%s: the tail of the value shows: %.120q", c.name, whole[max(0, len(whole)-120):])
		}
		if !bytes.HasSuffix(whole, []byte(c.after)) {
			t.Errorf("%s: the text after the closing quote was not kept: %.80q", c.name, whole[max(0, len(whole)-80):])
		}
		// Written in pieces it redacts the same, cut at every byte.
		for cut := 1; cut < len(in); cut++ {
			if got := fuzzRedactWrites(in, cut); !bytes.Equal(got, whole) {
				t.Fatalf("%s: written split at %d it differs from written whole:\n split %.100q\n whole %.100q", c.name, cut, got, whole)
			}
		}
	}
}

// An even number of backslashes before a quote is escaped backslashes and a real quote: the value is over there, and what follows is not part of it.
func TestOverlongQuotedValueEndsAtAQuoteAfterEscapedBackslashes(t *testing.T) {
	pad := strings.Repeat("a b ", 160)
	in := []byte(`x password="abc` + pad + `end\\" after PLAINTEXT`)
	out := Tokens(in)
	if !bytes.Contains(out, []byte("after PLAINTEXT")) {
		t.Fatalf("the text after the closing quote was dropped as part of the value: %.100q", out[max(0, len(out)-100):])
	}
	if bytes.Contains(out, []byte("a b a b")) {
		t.Fatalf("the value shows: %.100q", out)
	}
}

// Another rule's start inside the dropped value still redacts what follows it (the #3761 rule): an escaped quote does not hide it.
func TestOverlongQuotedValueStillStopsAtAnotherRulesStart(t *testing.T) {
	const second = "dkryFMT07elszGNU18fmtAHOV29gnu"
	pad := strings.Repeat("a b ", 160)
	in := []byte(`x password="abc` + pad + `x\"y PASSWORD="` + second + `" after`)
	out := Tokens(in)
	if bytes.Contains(out, []byte(second)) {
		t.Fatalf("the second credential shows: %.120q", out[max(0, len(out)-120):])
	}
}

// A run of backslashes before a quote, of every length, written in chunks of every small size (so the run is cut anywhere, and a chunk can be
// nothing but backslashes) redacts exactly as written whole: the run is carried as a count, not held, and the backslashes of a closing
// spelling dropped by an earlier chunk are written back (#5018 review).
func TestOverlongQuotedValueBackslashRunsChunkedEqualWhole(t *testing.T) {
	pad := strings.Repeat("a b ", 160)
	for _, open := range []string{`x password="abc`, `{"msg":"x password=\"abc`} {
		for n := 0; n <= 9; n++ {
			in := []byte(open + pad + strings.Repeat(`\`, n) + `" TAILn after "more" \" and \\" end`)
			whole := Tokens(in)
			for _, size := range []int{1, 2, 3, 5, 7, 64} {
				var out bytes.Buffer
				w := NewWriter(&out)
				for off := 0; off < len(in); off += size {
					_, _ = w.Write(in[off:min(off+size, len(in))])
				}
				_ = w.Close()
				if !bytes.Equal(out.Bytes(), whole) {
					t.Fatalf("%q run %d chunks of %d: differs from whole:\n got   %.100q\n whole %.100q", open[:12], n, size, out.Bytes(), whole)
				}
			}
		}
	}
}
