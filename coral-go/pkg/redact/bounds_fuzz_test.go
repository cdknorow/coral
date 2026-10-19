package redact

import (
	"strings"
	"testing"
	"time"
)

// FuzzRedactBoundedStable (#5002): redaction of ANY bytes (not only a text with a planted secret, as FuzzRedactStream) never panics and
// ends in bounded time; its output is bounded (each marker replaces at least a byte, so the growth is at most the marker's length per
// input byte, and in practice far less); and a second pass over its own output stays bounded.
// go test -fuzz FuzzRedactBoundedStable -fuzztime 10m ./pkg/redact/ fuzzes.
func FuzzRedactBoundedStable(f *testing.F) {
	for _, s := range []string{"", "x", Marker, Marker + Marker, "Authorization: Bearer", "password=", "ghp_", "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----",
		"https://user:pw@host/", "a=b&token=c&d", "\x00\x01\xff", "[redacted token", "redacted token]", "{\"a\":\"Bearer \\u0061bc\"}", "é\u200bé"} {
		f.Add([]byte(s))
	}
	f.Add([]byte(`x DB_PASSWORD="a` + strings.Repeat(" ", 600) + `MT07elszGNU18fmtAHOV29g" y`)) // #5002: the rest of a long quoted value showed
	for _, sec := range fuzzSecrets() {
		f.Add([]byte("x " + sec.text + " y"))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) > 64<<10 {
			t.Skip()
		}
		start := time.Now()
		out := Tokens(in)
		if d := time.Since(start); d > 2*time.Second && !raceEnabled {
			t.Fatalf("%d bytes took %v", len(in), d)
		}
		if len(out) > (len(Marker)+1)*len(in)+len(Marker) {
			t.Fatalf("%d bytes in, %d out", len(in), len(out))
		}
		// A second pass over redacted text (a proxy in front of a proxy) stays bounded too. It is not idempotent, nor does it settle: a pass can
		// redact more of what a marker sits beside, never less than its input shows ("token=token=0" once gives "token=token=[redacted token]";
		// every further pass of "token=token=token=..." adds a marker). That is over-redaction, never a leak, so no equality is asked for.
		again := Tokens(out)
		if len(again) > (len(Marker)+1)*len(out)+len(Marker) {
			t.Fatalf("a second pass grew %d bytes to %d", len(out), len(again))
		}
	})
}
