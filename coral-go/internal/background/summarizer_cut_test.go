package background

import (
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/pkg/redact"
)

// condenseAllMessages is condenseMessages done the plain way: every message read and redacted,
// joined, then cut. condenseMessages must give exactly this while reading only the ends.
func condenseAllMessages(data []byte) string {
	var parts []string
	for _, line := range strings.Split(string(data), "\n") {
		if part, ok, _ := condensedMessage(line); ok {
			parts = append(parts, part)
		}
	}
	full := strings.Join(parts, "\n\n")
	if len(full) > maxTranscriptChars {
		half := maxTranscriptChars / 2
		full = full[:half] + transcriptCutMarker + full[len(full)-half:]
	}
	return full
}

// randomTranscript is n JSONL lines: messages of random length (some holding a token, some
// user, some empty), lines that aren't messages, blank lines and a broken one.
func randomTranscript(rng *rand.Rand, n int) []byte {
	var b strings.Builder
	for k := 0; k < n; k++ {
		switch rng.Intn(10) {
		case 0:
			b.WriteString("\n")
		case 1:
			b.WriteString(`{"type":"progress","data":{}}` + "\n")
		case 2:
			b.WriteString(`{"type":"assistant","message":{"content":"` + "\n")
		case 3:
			b.WriteString(`{"type":"user","message":{"content":""}}` + "\n")
		default:
			typ := "assistant"
			if rng.Intn(3) == 0 {
				typ = "user"
			}
			text := strings.Repeat("w", rng.Intn(9000))
			if rng.Intn(4) == 0 {
				at := rng.Intn(len(text) + 1)
				text = text[:at] + " " + knowledgeToken + " " + text[at:]
			}
			fmt.Fprintf(&b, `{"type":%q,"message":{"content":%q}}`+"\n", typ, text)
		}
	}
	return []byte(b.String())
}

// #4414 (Security's INFO on the follow-up): reading only the messages that reach the kept head
// and tail gives the same text as redacting every message, for short and long transcripts.
func TestCondenseMessagesMatchesTheFullPass(t *testing.T) {
	rng := rand.New(rand.NewSource(4414))
	for round := 0; round < 400; round++ {
		data := randomTranscript(rng, rng.Intn(40))
		want, got := condenseAllMessages(data), condenseMessages(data)
		if got != want {
			t.Fatalf("round %d (%d bytes): got %d chars, want %d", round, len(data), len(got), len(want))
		}
	}
	for _, data := range [][]byte{nil, []byte("\n\n"), []byte(`{"type":"user","message":{"content":"hi"}}`)} {
		if got, want := condenseMessages(data), condenseAllMessages(data); got != want {
			t.Fatalf("%q: got %q, want %q", data, got, want)
		}
	}
}

// BenchmarkCondenseMessages100MB is a 100 MB transcript (25,000 messages of 4 KB): only the
// messages in the kept head and tail are read and redacted.
func BenchmarkCondenseMessages100MB(b *testing.B) {
	var sb strings.Builder
	msg := `{"type":"assistant","message":{"content":"` + strings.Repeat("some words of a reply ", 186) + `"}}` + "\n"
	for sb.Len() < 100<<20 {
		sb.WriteString(msg)
	}
	data := []byte(sb.String())
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := condenseMessages(data); len(out) > maxTranscriptChars+len(transcriptCutMarker) {
			b.Fatal("not cut")
		}
	}
}

// fakeRedactor counts its calls; each takes delay and fails when fail is set (as a message that
// runs out the redactor's time budget does).
type fakeRedactor struct {
	redact.Redactor
	calls *atomic.Int32
	delay time.Duration
	fail  bool
}

func (r fakeRedactor) Text(b []byte) ([]byte, error) {
	r.calls.Add(1)
	time.Sleep(r.delay)
	if r.fail {
		return nil, redact.ErrUnchecked
	}
	return b, nil
}

func useFakeRedactor(t *testing.T, r fakeRedactor) {
	old := knowledgeRedactor
	knowledgeRedactor = func() redact.Redactor { return r }
	t.Cleanup(func() { knowledgeRedactor = old })
}

func shortMessages(n int) []byte {
	var b strings.Builder
	for k := 0; k < n; k++ {
		fmt.Fprintf(&b, `{"type":"assistant","message":{"content":"reply %d"}}`+"\n", k)
	}
	return []byte(b.String())
}

// #4414 (Security's INFO on the perf follow-up): a transcript whose messages can't be checked
// stops the walk after maxUncheckedMessages of them, not at the end of the file.
func TestCondenseMessagesStopsAfterUncheckedMessages(t *testing.T) {
	var calls atomic.Int32
	useFakeRedactor(t, fakeRedactor{calls: &calls, fail: true})
	got := condenseMessages(shortMessages(1000))
	assert.Equal(t, int32(maxUncheckedMessages), calls.Load())
	assert.Equal(t, transcriptCutMarker, got, "nothing that couldn't be checked, and the cut is shown")
}

// A slow redactor stops the walk at condenseWalkBudget, keeping the messages already checked.
func TestCondenseMessagesStopsAtItsTimeBudget(t *testing.T) {
	prev := condenseWalkBudget
	condenseWalkBudget = 200 * time.Millisecond
	t.Cleanup(func() { condenseWalkBudget = prev })
	var calls atomic.Int32
	useFakeRedactor(t, fakeRedactor{calls: &calls, delay: 30 * time.Millisecond})
	start := time.Now()
	got := condenseMessages(shortMessages(1000))
	assert.Less(t, time.Since(start), 2*time.Second)
	n := calls.Load()
	assert.Less(t, n, int32(20), "the walk stopped at the budget")
	assert.True(t, strings.HasPrefix(got, "### Assistant\nreply 0\n\n"), "the head read so far is kept")
	assert.True(t, strings.HasSuffix(got, transcriptCutMarker))
	assert.Equal(t, int(n), strings.Count(got, "### Assistant"), "every checked message is kept")
}
