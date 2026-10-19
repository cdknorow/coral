package agentmodels

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// Fuzz targets for the model lists a CLI prints and the model a person types (#5002): both end up as an argument of a command
// Coral starts. A plain go test runs the seeds (the recorded outputs under testdata); go test -fuzz FuzzValidModelValue -fuzztime 10m
// ./internal/agentmodels/ fuzzes.

// FuzzValidModelValue: a value that passes can be one argument of --model: it is 1 to 128 bytes, starts with a letter or digit (so it is
// never read as a flag), and holds only the characters of a model id (no space, control character, quote, backslash, ";", "$", "`", "|", "&", "<", ">" or newline).
func FuzzValidModelValue(f *testing.F) {
	for _, s := range []string{"opus", "claude-opus-5-5", "opus[1m]", "gpt-6-astra", "openai/gpt-4o", "sonnet:high", "", "-x", "--model", "a b", "a\nb", "a\x00", "é", "a;b", "a$(b)", "a`b`", "a|b", "a&b", "a<b", "a>b", "a'b", `a"b`, `a\b`, strings.Repeat("a", 128), strings.Repeat("a", 129), "a\u202eb", "a\u200bb", "a\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		if !ValidModelValue(v) {
			return
		}
		if len(v) < 1 || len(v) > 128 {
			t.Fatalf("%q accepted with %d bytes", v, len(v))
		}
		if !(v[0] >= 'a' && v[0] <= 'z' || v[0] >= 'A' && v[0] <= 'Z' || v[0] >= '0' && v[0] <= '9') {
			t.Fatalf("%q accepted but starts with %q", v, v[0])
		}
		if !utf8.ValidString(v) || strings.ContainsAny(v, " \t\r\n\"'\\;$`|&<>*?(){}!#~,=^%") {
			t.Fatalf("%q accepted with a character a shell or the argument list reads specially", v)
		}
		for _, r := range v {
			if r > unicode.MaxASCII || unicode.IsControl(r) || unicode.IsSpace(r) {
				t.Fatalf("%q accepted with %U", v, r)
			}
		}
		if v != strings.TrimSpace(v) {
			t.Fatalf("%q accepted with white space at an end", v)
		}
	})
}

// FuzzParseModelLists: whatever a CLI prints, the four parsers never panic, and what they list is clean: at most maxModels, each value a
// valid model id and listed once, names and descriptions one plain line within their caps, efforts valid values within theirs. Efforts
// and names never carry a control character or an invisible format character (they are drawn in the dashboard and typed into a command's flags).
func FuzzParseModelLists(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*"))
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil && len(b) < 256<<10 {
			f.Add(b)
		}
	}
	for _, s := range []string{"", "{}", `{"models":[{"slug":"x"}]}`, `{"models":[{"slug":"-bad","display_name":"a\nb","supported_reasoning_levels":[{"effort":"high"},{"effort":"x y"}]}]}`,
		"id\tName\nid2\tName 2\n", "id\tName\n\n\t\n-x\ty\n", "provider  model  context  max-out  thinking  images\nanthropic  claude-x  200K  64K  yes  yes\n",
		"id\t" + strings.Repeat("a", 79) + " b\n", // a name cut right after a space (#5002)
		strings.Repeat("0", 100) + "\n",           // an id longer than a name may be, listed with no name (#5002)
		`{"type":"control_response","response":{"response":{"models":[{"value":"opus","displayName":"Opus"}]}}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256<<10 {
			t.Skip()
		}
		parsers := map[string]func() ([]Model, error){
			"claude": func() ([]Model, error) { return ParseClaudeInitialize(bytes.NewReader(data)) },
			"codex":  func() ([]Model, error) { return ParseCodexCatalog(data) },
			"agy":    func() ([]Model, error) { return ParseAgyModels(data) },
			"pi":     func() ([]Model, error) { return ParsePiModels(data) },
		}
		for name, parse := range parsers {
			models, err := parse()
			if err != nil {
				continue
			}
			if len(models) == 0 || len(models) > maxModels {
				t.Fatalf("%s: %d models without an error", name, len(models))
			}
			seen := map[string]bool{}
			for _, m := range models {
				if !ValidModelValue(m.Value) || seen[m.Value] {
					t.Fatalf("%s: value %q listed (valid or repeated)", name, m.Value)
				}
				seen[m.Value] = true
				for what, s := range map[string]string{"name": m.DisplayName, "description": m.Description} {
					if strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) || s != strings.Join(strings.Fields(s), " ") {
						t.Fatalf("%s: %s %q is not one plain line", name, what, s)
					}
				}
				if m.DisplayName == "" || utf8.RuneCountInString(m.DisplayName) > maxDisplayName || utf8.RuneCountInString(m.Description) > maxDescription {
					t.Fatalf("%s: name %q or description over its cap", name, m.DisplayName)
				}
				for _, e := range m.Efforts {
					if !ValidModelValue(e) || utf8.RuneCountInString(e) > maxEffort {
						t.Fatalf("%s: effort %q", name, e)
					}
				}
			}
		}
	})
}
