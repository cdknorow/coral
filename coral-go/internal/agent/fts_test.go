package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The defect: FTSBody was declared and read but never assigned by any of the
// four extractors, so the indexer's guard was never true, UpsertFTS was never
// called, and session_fts stayed empty — in production, across 56 indexed
// sessions, for months. This is the guard against a fifth extractor, or a
// rewritten one, quietly doing the same.
func TestEveryExtractorPopulatesFTSBody(t *testing.T) {
	cases := []struct {
		agentType string
		filename  string
		content   string
		wantTerm  string
	}{
		{
			agentType: "claude",
			filename:  "project-a/sess-claude.jsonl",
			content: `{"type":"user","message":{"content":"please check the Coralville deployment"},"timestamp":"2026-08-28T00:00:00Z"}
{"type":"assistant","message":{"content":"MEMORY-TOKEN-7731 done"},"timestamp":"2026-08-28T00:00:01Z"}`,
			wantTerm: "Coralville",
		},
		{
			agentType: "codex",
			filename:  "rollout-sess-codex.jsonl",
			content: `{"role":"user","content":"please check the Coralville deployment","timestamp":"2026-08-28T00:00:00Z"}
{"role":"assistant","content":"done","timestamp":"2026-08-28T00:00:01Z"}`,
			wantTerm: "Coralville",
		},
		{
			agentType: "gemini",
			filename:  "uuid-1/chats/session-g1.json",
			content:   `[{"role":"user","parts":[{"text":"please check the Coralville deployment"}],"timestamp":"2026-08-28T00:00:00Z"},{"role":"model","parts":[{"text":"done"}],"timestamp":"2026-08-28T00:00:01Z"}]`,
			wantTerm:  "Coralville",
		},
		{
			agentType: "pi",
			filename:  "sess-pi.jsonl",
			content: `{"type":"message","role":"user","content":"please check the Coralville deployment","timestamp":"2026-08-28T00:00:00Z"}
{"type":"message","role":"assistant","content":"done","timestamp":"2026-08-28T00:00:01Z"}`,
			wantTerm: "Coralville",
		},
	}

	for _, tc := range cases {
		t.Run(tc.agentType, func(t *testing.T) {
			dir := t.TempDir()
			writeHistoryFile(t, dir, tc.filename, tc.content)

			impl, ok := GetAgent(tc.agentType).(HistoryScanner)
			if !ok {
				t.Skipf("%s does not scan history", tc.agentType)
			}
			sessions, err := impl.ExtractSessions(dir, nil)
			if err != nil {
				t.Fatalf("ExtractSessions: %v", err)
			}
			if len(sessions) == 0 {
				t.Fatalf("no sessions extracted from %s", tc.filename)
			}
			s := sessions[0]
			if s.FTSBody == "" {
				t.Fatal("FTSBody is empty — the indexer's guard skips it and the session is unsearchable")
			}
			if !strings.Contains(s.FTSBody, tc.wantTerm) {
				t.Errorf("FTSBody does not contain %q from the transcript:\n%s", tc.wantTerm, s.FTSBody)
			}
			// The list shows the summary, so a word visible on screen has to be
			// searchable. That was the reported symptom.
			if s.DisplaySummary != "" && !strings.Contains(s.FTSBody, s.DisplaySummary) {
				t.Errorf("FTSBody does not contain the display summary, so a visible word may be unfindable\nsummary: %q", s.DisplaySummary)
			}
		})
	}
}

func TestFTSBodyBuilderIsBounded(t *testing.T) {
	var f FTSBodyBuilder
	chunk := strings.Repeat("x", 8*1024)
	for i := 0; i < 200; i++ { // 1.6 MB offered
		f.Add(chunk)
	}
	got := len(f.String())
	if got > ftsBodyLimit+len(chunk)+1 {
		t.Errorf("body grew to %d bytes, past the %d limit", got, ftsBodyLimit)
	}
	if got < ftsBodyLimit {
		t.Errorf("body stopped at %d bytes, well short of the %d limit", got, ftsBodyLimit)
	}
}

func TestFTSBodyBuilderSkipsEmptyText(t *testing.T) {
	var f FTSBodyBuilder
	f.Add("")
	f.Add("first")
	f.Add("")
	f.Add("second")
	if got := f.String(); got != "first\nsecond" {
		t.Errorf("got %q, want %q", got, "first\nsecond")
	}
}

func TestBuildFTSBodySeedsWithTheSummary(t *testing.T) {
	var f FTSBodyBuilder
	f.Add("body text")
	got := buildFTSBody("the visible summary", &f)
	if !strings.Contains(got, "the visible summary") {
		t.Error("the display summary must be searchable — it is what the list shows")
	}
	if !strings.Contains(got, "body text") {
		t.Error("the transcript must be searchable too")
	}
}

// writeHistoryFile writes a fixture at dir/name, creating any subdirectory the
// name implies — Claude looks for its files one level down, inside a project
// directory, while the others scan the base path directly.
func writeHistoryFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
