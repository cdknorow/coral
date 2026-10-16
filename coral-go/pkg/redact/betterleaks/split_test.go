package betterleaks

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/redact"
)

// Security's probes on #3808, as tests: a secret written in two bursts, and a secret where a
// long line's frame ends. A reply stream (NewStream) may not show any of a secret the
// whole-text scan redacts. A live stream (the terminal, #3958) holds nothing: a secret within
// one write is replaced; split across writes is the accepted risk, documented in pkg/tunnel.

func probeStream(t *testing.T, s *redact.Scanner, chunks []string) string {
	t.Helper()
	var out bytes.Buffer
	st := s.NewStream(&out)
	for _, c := range chunks {
		if _, err := st.Write([]byte(c)); err != nil {
			t.Fatalf("write: %v", err)
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

func TestASplitNeverShowsASecret(t *testing.T) {
	s := &redact.Scanner{Detector: detector(t), Budget: 10 * time.Second}
	tok := "ghp_" + pseudo(36, alnum, 31)
	pw := pseudo(24, alnum, 32)
	for _, c := range []struct{ text, secret string }{
		{"the key is " + tok + " ok\n", tok[4:]},
		{"export GITHUB_TOKEN=" + tok + "\n", tok[4:]},
		{"password=" + pw + " now\n", pw},
		{"stripe key sk_" + "live_" + pw + " here\n", pw},
	} {
		whole := probeStream(t, s, []string{c.text})
		if w := pieceShown(whole, c.secret); w != "" {
			t.Logf("%q: the library doesn't redact it whole, so there is nothing to keep (%q)", c.text, whole)
			continue
		}
		for cut := 1; cut < len(c.text); cut++ {
			// A reply written in two pieces is the same as written whole.
			if got := probeStream(t, s, []string{c.text[:cut], c.text[cut:]}); got != whole {
				t.Fatalf("%q split at %d:\n got %q\nwhole %q", c.text, cut, got, whole)
			}
		}
		// The terminal's live stream: the secret within one write is replaced.
		var live bytes.Buffer
		ls := s.NewLiveStream(&live)
		if _, err := ls.Write([]byte(c.text)); err != nil {
			t.Fatal(err)
		}
		if w := pieceShown(live.String(), c.secret); w != "" {
			t.Fatalf("%q in one live write shows %q", c.text, w)
		}
	}
}

func TestAFrameEndNeverSplitsASecret(t *testing.T) {
	s := &redact.Scanner{Detector: detector(t), Budget: 10 * time.Second}
	secret := pseudo(24, alnum, 33)
	for _, shape := range []string{`{"password":"%s"}`, `{"a":1,"api_key": "%s","b":2}`, `token=%s `, `"secret":"%s",`} {
		for off := redact.DefaultFrame - 100; off <= redact.DefaultFrame+100; off++ {
			line := strings.Repeat("x", off) + strings.Replace(shape, "%s", secret, 1) + strings.Repeat(`{"k":"v"},`, 50)
			out := probeStream(t, s, []string{line})
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
