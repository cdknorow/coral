package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Fuzz targets for the transcript readers (#4147): a transcript is a file an agent CLI writes, and
// what an agent or a tool printed ends up inside it. Seeds are the recorded transcripts under
// testdata. A plain go test runs the seeds; go test -fuzz FuzzTranscriptLine -fuzztime 10m
// ./internal/jsonl/ fuzzes.

func transcriptSeeds(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*", "*.jsonl"))
	more, _ := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	for _, p := range append(files, more...) {
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 256<<10 {
			continue
		}
		for _, line := range bytes.Split(b, []byte("\n")) {
			if len(line) > 0 {
				f.Add(line)
			}
		}
	}
	for _, s := range []string{
		`{}`, `[]`, `null`, ``, `{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"hi"}]},"timestamp":"2026-10-16T10:00:00Z"}`,
		`{"type":"system","subtype":"turn_duration","timestamp":"2026-10-16T10:00:00.123456789Z"}`,
		`{"type":"user","message":{"content":"[Request interrupted by user]"},"timestamp":"2026-10-16T10:00:00Z"}`,
		`{"type":"assistant","isApiErrorMessage":true,"error":"authentication_failed","message":{"content":[{"type":"text","text":"Invalid API key"}]}}`,
		`{"version":"2.1.301","type":"user"}`, `{"toolUseResult":{"version":"9.9.9"},"version":"2.1.301"}`, `{"version":"<script>"}`,
		`{"type":"assistant","message":{"model":"<synthetic>"}}`, `{"type":"assistant","message":{"model":"opus[1m]"}}`,
		`{"type":"turn_context","payload":{"model":"gpt-6-astra"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"text","text":"` + strings.Repeat("a", 5000) + `"}]}]}}`,
		`{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":1}}}}}}}}}}`,
	} {
		f.Add([]byte(s))
	}
}

// FuzzTranscriptLine: the cheap per-line scans (version, model, turn end, API error) never panic;
// they answer only values of their own shape: a version that looks like one, a model id that looks
// like one, a turn end at a time with the event "stop", an API error text under its cap.
func FuzzTranscriptLine(f *testing.F) {
	transcriptSeeds(f)
	f.Fuzz(func(t *testing.T, line []byte) {
		if v := claudeLineVersion(line); v != "" && !versionLike(v) {
			t.Fatalf("version %q", v)
		}
		for _, m := range []string{claudeLineModel(line), codexLineModel(line)} {
			if m != "" && (!transcriptModelR.MatchString(m) || len(m) > 200) {
				t.Fatalf("model %q", m)
			}
		}
		var st claudeTurnCache
		updateClaudeTurnState(&st, line)
		if st.event != "" && st.event != "stop" {
			t.Fatalf("turn event %q", st.event)
		}
		if st.event == "" && !st.at.IsZero() {
			t.Fatalf("a turn time with no event")
		}
		var e ClaudeAPIError
		updateClaudeAPIError(&e, line)
		if len(e.Text) > 4*claudeAPIErrorTextMax+8 {
			t.Fatalf("api error text of %d bytes", len(e.Text))
		}
		if !utf8.ValidString(e.Text) && e.Text != "" && utf8.RuneCountInString(e.Text) > claudeAPIErrorTextMax {
			t.Fatalf("api error text not cut at a character")
		}
		_ = e.NeedsLogin()
		_ = e.UsageLimit()
	})
}

// FuzzParseTranscript: every agent type's reader over arbitrary lines. It never panics, ends in
// bounded time, answers only what encodes as JSON (the dashboard sends it), and does not make
// more output than a few times its input (a tool result or a deep structure can't blow up).
func FuzzParseTranscript(f *testing.F) {
	transcriptSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			t.Skip()
		}
		for _, agent := range []string{"claude", "codex", "gemini", "agy", "pi", ""} {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			start := time.Now()
			msgs, err := parseLines(ctx, data, agent)
			cancel()
			if d := time.Since(start); d > 8*time.Second {
				t.Fatalf("%s: took %s for %d bytes", agent, d, len(data))
			}
			if err != nil {
				continue
			}
			out, merr := json.Marshal(msgs)
			if merr != nil {
				t.Fatalf("%s: the messages do not encode: %v", agent, merr)
			}
			lines := bytes.Count(data, []byte("\n")) + 1
			if len(out) > 8*len(data)+2048*lines+1024 {
				t.Fatalf("%s: %d bytes in, %d bytes of messages out", agent, len(data), len(out))
			}
		}
	})
}
