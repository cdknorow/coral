package betterleaks

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
)

// Security's probes on #3808, as tests: a secret written in two bursts with the terminal's idle
// flush between them, and a secret where a long line's frame ends. Neither may show any of a
// secret the whole-text scan redacts.

func probeStream(t *testing.T, s *redact.Scanner, chunks []string, flush map[int]bool, force bool) string {
	t.Helper()
	var out bytes.Buffer
	st := s.NewStream(&out)
	for i, c := range chunks {
		if _, err := st.Write([]byte(c)); err != nil {
			t.Fatalf("write: %v", err)
		}
		if flush[i] {
			if err := st.Flush(force); err != nil {
				t.Fatalf("flush: %v", err)
			}
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out.String()
}

// pieceShown is the first run of 8 characters of secret in out, or "".
func pieceShown(out, secret string) string {
	for i := 0; i+8 <= len(secret); i++ {
		if strings.Contains(out, secret[i:i+8]) {
			return secret[i : i+8]
		}
	}
	return ""
}

func TestAFlushNeverSplitsASecret(t *testing.T) {
	s := &redact.Scanner{Detector: detector(t), Budget: 10 * time.Second}
	tok := "ghp_" + pseudo(36, alnum, 31)
	pw := pseudo(24, alnum, 32)
	for _, c := range []struct{ text, secret string }{
		{"the key is " + tok + " ok\n", tok[4:]},
		{"export GITHUB_TOKEN=" + tok + "\n", tok[4:]},
		{"password=" + pw + " now\n", pw},
		{"stripe key sk_" + "live_" + pw + " here\n", pw},
	} {
		whole := probeStream(t, s, []string{c.text}, nil, false)
		if w := pieceShown(whole, c.secret); w != "" {
			t.Logf("%q: the library doesn't redact it whole, so there is nothing to keep (%q)", c.text, whole)
			continue
		}
		for cut := 1; cut < len(c.text); cut++ {
			parts := []string{c.text[:cut], c.text[cut:]}
			// The idle flush: the same as written whole.
			if got := probeStream(t, s, parts, map[int]bool{0: true}, false); got != whole {
				t.Fatalf("%q idle flush at %d:\n got %q\nwhole %q", c.text, cut, got, whole)
			}
			// The forced flush (wsHoldMax): no 8 characters of the secret.
			if got := probeStream(t, s, parts, map[int]bool{0: true}, true); pieceShown(got, c.secret) != "" {
				t.Fatalf("%q forced flush at %d shows %q: %q", c.text, cut, pieceShown(got, c.secret), got)
			}
		}
	}
}

func TestAFrameEndNeverSplitsASecret(t *testing.T) {
	s := &redact.Scanner{Detector: detector(t), Budget: 10 * time.Second}
	secret := pseudo(24, alnum, 33)
	for _, shape := range []string{`{"password":"%s"}`, `{"a":1,"api_key": "%s","b":2}`, `token=%s `, `"secret":"%s",`} {
		for off := redact.DefaultFrame - 100; off <= redact.DefaultFrame+100; off++ {
			line := strings.Repeat("x", off) + strings.Replace(shape, "%s", secret, 1) + strings.Repeat(`{"k":"v"},`, 50)
			out := probeStream(t, s, []string{line}, nil, false)
			if w := pieceShown(out, secret); w != "" {
				t.Fatalf("shape %s at %d (frame %d) shows %q", shape, off, redact.DefaultFrame, w)
			}
			// And held whole, through the chunked text scan.
			txt, err := s.Text([]byte(strings.Repeat(line, 3)))
			if err != nil {
				t.Fatal(err)
			}
			if w := pieceShown(string(txt), secret); w != "" {
				t.Fatalf("text: shape %s at %d shows %q", shape, off, w)
			}
		}
	}
}
