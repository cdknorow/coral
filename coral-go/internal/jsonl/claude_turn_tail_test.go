package jsonl

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// #4788: the first read of a Claude transcript starts at its end, in longer and longer tails, until every field has a value and a user or
// assistant record was seen. Every field is the newest value in the file, so this must give exactly what a read of the whole file gives, at
// the first read and after what is appended. Old against new on random transcripts: records of every kind the readers know (turn ends,
// interrupts, quoted markers, API errors and the records that clear them, versions and models present or missing, a "version" inside a tool
// result before the record's own, long records, a last record with no newline), tails of a few hundred bytes so every window length and the
// whole-file fallback are used.

// tailRecord is a transcript record. rare names a field that most records leave out (so a tail often lacks it): "version", "model" or
// "event" (the turn ends and interrupts), or "" for none.
func tailRecord(rng *rand.Rand, i int, ts time.Time, rare string) string {
	stamp := ts.Format("2006-01-02T15:04:05.000Z")
	version := ""
	if rng.Intn(10) > 0 && (rare != "version" || rng.Intn(60) == 0) {
		version = fmt.Sprintf(`"version":"2.1.%d",`, 290+rng.Intn(15))
	}
	pad := strings.Repeat("x", rng.Intn(3)*rng.Intn(2500))
	k := rng.Intn(20)
	if rare == "event" && (k == 8 || k == 9 || k == 10) && rng.Intn(60) != 0 {
		k = 18
	}
	switch {
	case k < 4:
		return fmt.Sprintf(`{"type":"user",%s"message":{"role":"user","content":"prompt %d %s"},"timestamp":"%s"}`, version, i, pad, stamp)
	case k < 8:
		model := ""
		if rng.Intn(4) > 0 && (rare != "model" || rng.Intn(40) == 0) {
			model = fmt.Sprintf(`"model":"claude-m%d",`, rng.Intn(4))
		}
		return fmt.Sprintf(`{"type":"assistant",%s"message":{%s"role":"assistant","content":[{"type":"text","text":"answer %d %s"}]},"timestamp":"%s"}`, version, model, i, pad, stamp)
	case k < 10:
		return fmt.Sprintf(`{"type":"system","subtype":"turn_duration",%s"durationMs":%d,"timestamp":"%s"}`, version, rng.Intn(9000), stamp)
	case k == 10:
		return fmt.Sprintf(`{"type":"user",%s"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"timestamp":"%s"}`, version, stamp)
	case k == 11:
		return fmt.Sprintf(`{"type":"user",%s"message":{"role":"user","content":[{"type":"tool_result","content":"[Request interrupted by user] quoted","tool_use_id":"t"}]},"timestamp":"%s"}`, version, stamp)
	case k == 12:
		return fmt.Sprintf(`{"type":"assistant",%s"isApiErrorMessage":true,"error":"authentication_failed","message":{"model":"claude-m1","content":[{"type":"text","text":"Invalid API key %d"}]},"timestamp":"%s"}`, version, i, stamp)
	case k == 13:
		return fmt.Sprintf(`{"type":"assistant",%s"isApiErrorMessage":true,"apiErrorStatus":429,"error":"rate_limit","message":{"content":"limit %d"},"timestamp":"%s"}`, version, i, stamp)
	case k == 14:
		return fmt.Sprintf(`{"type":"user","toolUseResult":{"version":"9.9.9","stdout":"%s"},%s"message":{"role":"user","content":[{"type":"tool_result","content":"ok","tool_use_id":"t"}]},"timestamp":"%s"}`, pad, version, stamp)
	case k == 15:
		return fmt.Sprintf(`{"type":"progress",%s"data":"%s","timestamp":"%s"}`, version, pad, stamp)
	case k == 16:
		return fmt.Sprintf(`{"type":"summary","summary":"s %d"}`, i)
	case k == 17:
		return `not json at all ` + pad
	default:
		return fmt.Sprintf(`{"type":"system","subtype":"other",%s"timestamp":"%s"}`, version, stamp)
	}
}

func TestClaudeTranscriptTailFirstReadMatchesTheWholeFile(t *testing.T) {
	old := claudeTailWindows
	t.Cleanup(func() { claudeTailWindows = old })
	base := t.TempDir()
	t.Setenv("CLAUDE_PROJECTS_DIR", base)
	dir := filepath.Join(base, "-work")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var cases, tailAnswered, wholeRead, neededGrowth, afterAppend, withAPIError, withEvent int
	for seed := int64(0); seed < 400; seed++ {
		rng := rand.New(rand.NewSource(seed))
		rare := []string{"", "version", "event", "model"}[seed%4]
		sid := fmt.Sprintf("00000000-0000-4000-8000-%012d", seed)
		path := filepath.Join(dir, sid+".jsonl")
		n := rng.Intn(120)
		ts := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
		var sb strings.Builder
		write := func(count int) {
			for i := 0; i < count; i++ {
				ts = ts.Add(time.Second)
				sb.WriteString(tailRecord(rng, i, ts, rare))
				sb.WriteString("\n")
			}
		}
		write(n)
		body := sb.String()
		if rng.Intn(5) == 0 && len(body) > 0 {
			body = body[:len(body)-1] // the last record has no newline yet
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		windows := []int64{200, 800, 3200}
		if (seed/4)%2 == 1 {
			windows = []int64{4000, 16000} // most of these transcripts end inside the first tail
		}
		scan := func(w []int64) (claudeTurnCache, *SessionReader) {
			claudeTailWindows = w
			r := NewSessionReader()
			return r.scanClaudeTranscript(sid, "/work"), r
		}
		want, _ := scan(nil)
		got, r := scan(windows)
		same := func(a, b claudeTurnCache) bool {
			return a.event == b.event && a.at.Equal(b.at) && a.version == b.version && a.model == b.model && reflect.DeepEqual(a.apiErr, b.apiErr)
		}
		if !same(want, got) {
			t.Fatalf("seed %d: first read: whole file %+v, tail first %+v", seed, want, got)
		}
		cases++
		if got.from > 0 {
			tailAnswered++
			if int64(len(body))-got.from > windows[0] { // a window longer than the first one answered
				neededGrowth++
			}
		} else if want.size > windows[0] {
			wholeRead++
		}
		if got.apiErr.Kind != "" {
			withAPIError++
		}
		if got.event != "" {
			withEvent++
		}
		// what is appended after the first read is followed, and gives the whole file's state
		more := &strings.Builder{}
		for i := 0; i < rng.Intn(15); i++ {
			ts = ts.Add(time.Second)
			more.WriteString(tailRecord(rng, 1000+i, ts, rare) + "\n")
		}
		if more.Len() > 0 {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) > 0 && body[len(body)-1] != '\n' {
				f.WriteString("\n")
			}
			f.WriteString(more.String())
			f.Close()
			claudeTailWindows = windows
			got2 := r.scanClaudeTranscript(sid, "/work")
			want2, _ := scan(nil)
			if !same(want2, got2) {
				t.Fatalf("seed %d: after an append: whole file %+v, tail first then followed %+v", seed, want2, got2)
			}
			afterAppend++
		}
	}
	t.Logf("cases %d: tail answered %d (needed a longer tail %d), whole file read %d, with an API error %d, with a turn end %d, appended to %d",
		cases, tailAnswered, neededGrowth, wholeRead, withAPIError, withEvent, afterAppend)
	for name, n := range map[string]int{"tail answered": tailAnswered, "a longer tail needed": neededGrowth, "whole file read": wholeRead,
		"an API error": withAPIError, "a turn end": withEvent, "appended after": afterAppend} {
		if n < 40 {
			t.Fatalf("floor: %s only %d times", name, n)
		}
	}
}
