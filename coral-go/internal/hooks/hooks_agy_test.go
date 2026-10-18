package hooks

import "testing"

// Antigravity's tool calls read like the other CLIs' in activity (#4577), from agy 1.4.2's argument names.
func TestAgyToolSummariesAndDetails(t *testing.T) {
	for _, c := range []struct {
		tool       string
		inp        map[string]any
		summary    string
		key, value string
	}{
		{"run_command", map[string]any{"CommandLine": "go test ./...", "Cwd": "/w"}, "Ran: go test ./...", "command", "go test ./..."},
		{"view_file", map[string]any{"AbsolutePath": "/w/src/main.go"}, "Read main.go", "file_path", "/w/src/main.go"},
		{"write_to_file", map[string]any{"TargetFile": "/w/notes.md", "CodeContent": "x"}, "Wrote notes.md", "file_path", "/w/notes.md"},
		{"replace_file_content", map[string]any{"TargetFile": "/w/a.go"}, "Edited a.go", "file_path", "/w/a.go"},
		{"search_web", map[string]any{"Query": "vt100 CUP"}, "Searched: vt100 CUP", "query", "vt100 CUP"},
		{"read_url_content", map[string]any{"Url": "https://example.com/x"}, "Fetched https://example.com/x", "url", "https://example.com/x"},
	} {
		if got := MakeToolSummary(c.tool, c.inp); got != c.summary {
			t.Errorf("%s: summary %q, want %q", c.tool, got, c.summary)
		}
		if d := AgyToolDetail(c.tool, c.inp); d == nil || d[c.key] != c.value {
			t.Errorf("%s: detail %v, want %s=%q", c.tool, d, c.key, c.value)
		}
	}
	if MakeToolSummary("grep_search", nil) != "Used grep_search" || AgyToolDetail("Bash", nil) != nil {
		t.Error("other tools are unchanged")
	}
}
