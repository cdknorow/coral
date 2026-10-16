package betterleaks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/redact"
)

// #3808: Coral's secret detection is betterleaks' rules, run through redact.Scanner. These
// tests hold the guarantees, not the rules: what the library finds is redacted raw, in JSON and
// streamed at every split; the time budget fails closed; nothing reaches the network.

var (
	shared     *Detector
	sharedErr  error
	sharedOnce sync.Once
)

func detector(t testing.TB) *Detector {
	t.Helper()
	sharedOnce.Do(func() { shared, sharedErr = New() })
	require.NoError(t, sharedErr)
	return shared
}

// The embedded rules compile: betterleaks would exit the process on one that doesn't, so this
// is the guard that a version bump can't ship one.
func TestNewBuildsTheDetector(t *testing.T) {
	d, err := New()
	require.NoError(t, err)
	require.NotNil(t, d.d)
}

// Fake secrets are built at run time, so the repository holds nothing that looks like a real one.
// Each is shaped as its provider issues it, with enough entropy for the library's filters.
func pseudo(n int, alphabet string, seed uint32) string {
	var b strings.Builder
	x := seed
	for i := 0; i < n; i++ {
		x = x*1664525 + 1013904223
		b.WriteByte(alphabet[int(x>>24)%len(alphabet)])
	}
	return b.String()
}

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// caught are secrets betterleaks' rules find; each text holds its secret.
func caught() []struct{ name, text, secret string } {
	gh := "ghp_" + pseudo(36, alnum, 1)
	stripe := "sk_" + "live_" + pseudo(24, alnum, 2)
	slack := "xoxb-" + pseudo(11, "0123456789", 4) + "-" + pseudo(12, "0123456789", 5) + "-" + pseudo(24, alnum, 6)
	return []struct{ name, text, secret string }{
		{"github pat", "token " + gh + " in the log", gh},
		{"stripe", "STRIPE_KEY=" + stripe, stripe},
		{"slack bot", "SLACK_TOKEN=" + slack, slack},
	}
}

func scanner(t testing.TB) *redact.Scanner {
	return &redact.Scanner{Detector: detector(t), Budget: 10 * time.Second}
}

func TestLibraryFindingsAreRedacted(t *testing.T) {
	s := scanner(t)
	for _, c := range caught() {
		out, err := s.Text([]byte(c.text))
		require.NoError(t, err)
		assert.NotContains(t, string(out), c.secret, c.name)
		assert.Contains(t, string(out), redact.Marker, c.name)

		js, _ := json.Marshal(map[string]any{"content": c.text, "nested": []any{map[string]string{"x": c.text}}})
		out, err = s.Reply(js, true)
		require.NoError(t, err)
		assert.True(t, json.Valid(out), "%s stays JSON: %s", c.name, out)
		assert.NotContains(t, string(out), c.secret, c.name)
	}
}

func streamed(t *testing.T, s *redact.Scanner, body []byte, k int) string {
	t.Helper()
	var out bytes.Buffer
	w := s.NewStream(&out)
	_, err := w.Write(body[:k])
	require.NoError(t, err)
	_, err = w.Write(body[k:])
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return out.String()
}

// Streamed, at every split into two writes, the same as whole, and the secret never shows.
func TestLibraryStreamSplitEqualsWhole(t *testing.T) {
	s := scanner(t)
	for _, c := range caught() {
		body := []byte("$ run\n" + c.text + "\nnext line\n")
		whole := streamed(t, s, body, len(body))
		assert.NotContains(t, whole, c.secret, c.name)
		for k := 0; k < len(body); k++ {
			if got := streamed(t, s, body, k); got != whole {
				t.Fatalf("%s split at %d:\n got %q\nwhole %q", c.name, k, got, whole)
			}
		}
	}
}

// Text dense with the library's keywords is slow to check (#3807: seconds per MiB); over the
// budget it fails closed, and promptly.
func TestLibraryBudgetFailsClosed(t *testing.T) {
	s := &redact.Scanner{Detector: detector(t), Budget: 5 * time.Millisecond}
	dense := []byte(strings.Repeat("password="+strings.Repeat("0", 100)+" ", 10000))
	start := time.Now()
	_, err := s.Text(dense)
	require.ErrorIs(t, err, redact.ErrUnchecked)
	assert.Less(t, time.Since(start), 3*time.Second, "returns at the budget, not when the scan ends")
}

// failingTransport records every request anything tries to make.
type failingTransport struct{ n atomic.Int32 }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.n.Add(1)
	return nil, errors.New("the network is blocked in this test")
}

// No request, no DNS lookup, no dial while building the detector and scanning (validation is
// off; the tokenizer data and the rules are embedded).
func TestNoNetwork(t *testing.T) {
	ft := &failingTransport{}
	prevT, prevC := http.DefaultTransport, http.DefaultClient.Transport
	http.DefaultTransport, http.DefaultClient.Transport = ft, ft
	var dials atomic.Int32
	prevR := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("blocked")
	}}
	t.Cleanup(func() {
		http.DefaultTransport, http.DefaultClient.Transport = prevT, prevC
		net.DefaultResolver = prevR
	})

	d, err := New()
	require.NoError(t, err)
	s := &redact.Scanner{Detector: d, Budget: 10 * time.Second}
	for _, c := range caught() {
		_, err := s.Text([]byte(c.text))
		require.NoError(t, err)
	}
	_, err = s.Text([]byte("curl -H 'Authorization: Bearer " + pseudo(40, alnum, 9) + "' https://api.example.com"))
	require.NoError(t, err)
	assert.Zero(t, ft.n.Load(), "no HTTP request")
	assert.Zero(t, dials.Load(), "no DNS lookup")
}

// One detector serves every goroutine.
func TestConcurrentUse(t *testing.T) {
	s := scanner(t)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, c := range caught() {
				out, err := s.Text([]byte(c.text))
				if assert.NoError(t, err) {
					assert.NotContains(t, string(out), c.secret)
				}
			}
		}()
	}
	wg.Wait()
}
