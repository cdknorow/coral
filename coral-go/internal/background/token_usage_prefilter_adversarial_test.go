package background

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode"
)

// Adversarial differential: whatever spelling of the key the full decode counts, the prefilter must not skip.
func TestPrefilterAdversarial(t *testing.T) {
	rnd := rand.New(rand.NewSource(5059))
	letters := "usage"
	// every simple-folding variant of each letter, and escaped forms
	variants := func(c rune) []string {
		out := []string{string(c), strings.ToUpper(string(c)), fmt.Sprintf(`\u%04x`, c), fmt.Sprintf(`\u%04X`, c), fmt.Sprintf(`\u%04x`, unicode.ToUpper(c))}
		r := c
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			out = append(out, string(f), fmt.Sprintf(`\u%04x`, f))
		}
		return out
	}
	vars := map[rune][]string{}
	for _, c := range letters {
		vars[c] = variants(c)
	}
	pad := []string{"", "x", "usag", "sage", "\U0001f600", "\u00e9", "\\n", "\\\"", " ", "\u00df", "K", "\u212a", "\\ud83d\\ude00", "us\\u0061"}
	mismatch, counted := 0, 0
	for i := 0; i < 400000; i++ {
		var key strings.Builder
		key.WriteString(pad[rnd.Intn(len(pad))])
		for _, c := range letters {
			vs := vars[c]
			key.WriteString(vs[rnd.Intn(len(vs))])
			if rnd.Intn(30) == 0 {
				key.WriteString(pad[rnd.Intn(len(pad))])
			}
		}
		key.WriteString(pad[rnd.Intn(len(pad))])
		line := fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-19T10:00:00Z","message":{"model":"m","%s":{"input_tokens":3,"output_tokens":4}}}`, key.String())
		var ref, got claudeParser
		ref.SessionStartAt, got.SessionStartAt = "2026-10-19T09:00:00Z", "2026-10-19T09:00:00Z"
		ref.referenceLine(line)
		got.line(line)
		if len(ref.Calls) > 0 {
			counted++
		}
		if len(ref.Calls) != len(got.Calls) {
			mismatch++
			if mismatch < 6 {
				t.Errorf("prefilter skipped a line the full decode counts: key %q line %q (ref %d, got %d)", key.String(), line, len(ref.Calls), len(got.Calls))
			}
		}
	}
	t.Logf("the full decode counted %d of 400000 lines", counted)
	if counted < 1000 {
		t.Fatalf("the generator is vacuous: %d counted", counted)
	}
	if mismatch > 0 {
		t.Fatalf("%d mismatches", mismatch)
	}
}
