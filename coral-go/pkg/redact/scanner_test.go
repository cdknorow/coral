package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3808: the Scanner is the glue around a library's rules. Its tests use a small fake detector,
// so they test the glue (spans, the JSON walk, stream units, the budget, failing closed), not
// any rule set.

// reDetector finds each regexp's match, or its first group when it has one.
type reDetector struct{ res []*regexp.Regexp }

func (d reDetector) Find(ctx context.Context, text string) ([]Span, error) {
	var out []Span
	for _, re := range d.res {
		for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
			if len(m) >= 4 && m[2] >= 0 {
				out = append(out, Span{m[2], m[3]})
			} else {
				out = append(out, Span{m[0], m[1]})
			}
		}
	}
	return out, ctx.Err()
}

var fakeRules = reDetector{[]*regexp.Regexp{
	regexp.MustCompile(`sk_live_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`password: (\S+)`), // needs its key: the JSON walk must give it
	regexp.MustCompile(`(?s)-----BEGIN PRIVATE KEY-----.*?-----END PRIVATE KEY-----`),
}}

func fakeScanner() *Scanner { return &Scanner{Detector: fakeRules, Frame: 64, MaxKey: 1 << 10} }

func streamWrites(t *testing.T, s *Scanner, body []byte, cuts ...int) (string, error) {
	t.Helper()
	var out bytes.Buffer
	w := s.NewStream(&out)
	at := 0
	for _, c := range cuts {
		c = min(max(c, at), len(body))
		if _, err := w.Write(body[at:c]); err != nil {
			return out.String(), err
		}
		at = c
	}
	if _, err := w.Write(body[at:]); err != nil {
		return out.String(), err
	}
	return out.String(), w.Close()
}

func TestScannerTextReplacesSpans(t *testing.T) {
	s := fakeScanner()
	tok := fake("sk_live_", 24, alnum)
	out, err := s.Text([]byte("a " + tok + " b " + tok))
	require.NoError(t, err)
	assert.Equal(t, "a "+Marker+" b "+Marker, string(out))

	// Overlapping and touching spans are one marker.
	got, err := applySpans("0123456789", []Span{{2, 5}, {4, 7}, {7, 8}})
	require.NoError(t, err)
	assert.Equal(t, "01"+Marker+"89", got)
	_, err = applySpans("0123", []Span{{2, 9}})
	assert.ErrorIs(t, err, ErrUnchecked, "a span outside the text is not trusted")
}

// A value is scanned with its key, an array's strings with the member's key, an object's
// "value" with its "name"; the reply stays JSON.
func TestScannerJSONGivesTheKey(t *testing.T) {
	s := fakeScanner()
	pw := fake("", 20, alnum)
	raw := `{"password":"` + pw + `","list":{"password":["` + pw + `"]},"env":[{"name":"password","value":"` + pw + `"}],"n":3,"ok":true}`
	out, err := s.Reply([]byte(raw), true)
	require.NoError(t, err)
	assert.True(t, json.Valid(out), string(out))
	assert.NotContains(t, string(out), pw)
	assert.Contains(t, string(out), `"n":3`)

	// Not JSON (or not declared so): scanned as text.
	tok := fake("sk_live_", 24, alnum)
	out, err = s.Reply([]byte(`{"a":"`+tok+`"`), true)
	require.NoError(t, err)
	assert.NotContains(t, string(out), tok)

	// Document: nothing found gives raw back as it was written.
	plain := []byte(`{"a": "b",  "c":"\u0026"}`)
	doc, err := s.Document(plain)
	require.NoError(t, err)
	assert.Equal(t, string(plain), string(doc))
}

// Any split of a stream into two or three writes gives the same output as the whole, and no
// secret shows: lines, lines longer than Frame, a private key over several lines.
func TestScannerStreamSplitEqualsWhole(t *testing.T) {
	s := fakeScanner()
	tok := fake("sk_live_", 24, alnum)
	key := "-----BEGIN PRIVATE KEY-----\n" + fake("", 60, alnum) + "\n" + fake("", 60, alnum) + "\n-----END PRIVATE KEY-----\n"
	long := strings.Repeat("word ", 30) + tok + strings.Repeat(" more", 30)
	body := []byte("first line " + tok + "\n" + long + "\n" + key + "after the key\nno newline " + tok)
	whole, err := streamWrites(t, s, body)
	require.NoError(t, err)
	assert.NotContains(t, whole, tok[8:])
	assert.NotContains(t, whole, key[30:60])
	assert.Contains(t, whole, "after the key\n")
	for a := 0; a <= len(body); a++ {
		got, err := streamWrites(t, s, body, a)
		require.NoError(t, err)
		if got != whole {
			t.Fatalf("split at %d:\n got %q\nwhole %q", a, got, whole)
		}
	}
	for a := 0; a <= len(body); a += 7 {
		for b := a; b <= len(body); b += 11 {
			got, err := streamWrites(t, s, body, a, b)
			require.NoError(t, err)
			if got != whole {
				t.Fatalf("split at %d, %d:\n got %q\nwhole %q", a, b, got, whole)
			}
		}
	}
}

// A key longer than MaxKey is never shown and never held whole; one whose END never comes is
// replaced at the end.
func TestScannerStreamDropsAnOverlongKey(t *testing.T) {
	s := fakeScanner()
	body := fake("", 64, alnum)
	var out bytes.Buffer
	w := s.NewStream(&out)
	_, err := w.Write([]byte("before\n-----BEGIN PRIVATE KEY-----\n"))
	require.NoError(t, err)
	for i := 0; i < 200; i++ { // 13 KB, over MaxKey (1 KB)
		_, err = w.Write([]byte(body + "\n"))
		require.NoError(t, err)
		assert.LessOrEqual(t, len(w.(*lineStream).held), s.MaxKey+len(body)+1, "held stays bounded")
	}
	_, err = w.Write([]byte("-----END PRIVATE KEY-----\nafter\n"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	assert.Equal(t, "before\n"+Marker+"\nafter\n", out.String())

	out.Reset()
	w = s.NewStream(&out)
	_, _ = w.Write([]byte("-----BEGIN PRIVATE KEY-----\n" + body + "\n"))
	assert.Empty(t, out.String(), "a key still arriving is held")
	require.NoError(t, w.Close())
	assert.Equal(t, Marker, out.String())
}

// A live stream holds nothing (#3958, the operator's decision): an unfinished line is written
// at once, a secret within one write is replaced, and one split across writes shows its first
// part (the accepted risk) while the part that completes it is replaced.
func TestScannerLiveStream(t *testing.T) {
	s := fakeScanner()
	var out bytes.Buffer
	w := s.NewLiveStream(&out)
	_, _ = w.Write([]byte("$ "))
	assert.Equal(t, "$ ", out.String(), "an unfinished line is not held")
	_, _ = w.Write([]byte("g"))
	assert.Equal(t, "$ g", out.String(), "nor a key that could start a token")

	out.Reset()
	w = s.NewLiveStream(&out)
	_, _ = w.Write([]byte("key is sk_live_a1B2c3D4e5F6g7H8i9J0k1 ok"))
	assert.Equal(t, "key is "+Marker+" ok", out.String(), "a secret within one write is replaced")

	out.Reset()
	w = s.NewLiveStream(&out)
	_, _ = w.Write([]byte("key is sk_live_a1B2c3"))
	assert.Equal(t, "key is sk_live_a1B2c3", out.String(), "the accepted risk: what arrived first is shown")
	_, _ = w.Write([]byte("D4e5F6g7H8i9J0k1 ok\n"))
	assert.NotContains(t, out.String(), "D4e5F6", "the part that completes it is found with the context before it")
	require.NoError(t, w.Close())

	out.Reset()
	w = s.NewLiveStream(&out)
	_, _ = w.Write([]byte("-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBg\n-----END PRIVATE KEY-----\n"))
	assert.NotContains(t, out.String(), "MIIEvQ", "a key that arrives in one write is still found")
}

type slowDetector struct{}

func (slowDetector) Find(ctx context.Context, text string) ([]Span, error) {
	<-ctx.Done()
	return nil, nil // stopped early, and says nothing about it
}

type failDetector struct{ panics bool }

func (d failDetector) Find(context.Context, string) ([]Span, error) {
	if d.panics {
		panic("detector bug")
	}
	return nil, errors.New("detector failed")
}

// Over the budget, a failed or panicking detector: the text is never passed on.
func TestScannerFailsClosed(t *testing.T) {
	for name, d := range map[string]Detector{"slow": slowDetector{}, "error": failDetector{}, "panic": failDetector{panics: true}} {
		s := &Scanner{Detector: d, Budget: 50 * time.Millisecond}
		start := time.Now()
		_, err := s.Text([]byte("anything"))
		assert.ErrorIs(t, err, ErrUnchecked, name)
		assert.Less(t, time.Since(start), 2*time.Second, name)
		_, err = s.Reply([]byte(`{"a":"b"}`), true)
		assert.ErrorIs(t, err, ErrUnchecked, name)
		_, err = s.Document([]byte(`x`))
		assert.ErrorIs(t, err, ErrUnchecked, name)
		var out bytes.Buffer
		w := s.NewStream(&out)
		_, err = w.Write([]byte("a line\n"))
		assert.ErrorIs(t, err, ErrUnchecked, name)
		assert.Empty(t, out.String(), name)
		_, err = w.Write([]byte("more\n"))
		assert.Error(t, err, "a failed stream stays failed")
	}
}
