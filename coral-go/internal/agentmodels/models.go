// Package agentmodels lists the models each agent CLI offers its user (#4065, from #4064's
// report), from the CLI's own login: Coral never asks for or stores a vendor key. Each
// family has its own source:
//
//   - claude: the stream-json control protocol's initialize reply (models only; the reply
//     also carries account details, which are never kept);
//   - codex: codex debug models (the account's catalog), else codex debug models --bundled;
//   - agy (Coral's agy, antigravity and gemini types all run agy): agy models;
//   - pi: pi --list-models.
//
// The parsers keep only a model's value (what --model takes), display name, description and
// effort levels, and drop any value that isn't a plain model id.
package agentmodels

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// Model is one choice for an agent's --model.
type Model struct {
	Value       string   `json:"value"` // what --model takes
	DisplayName string   `json:"display_name"`
	Description string   `json:"description,omitempty"`
	Efforts     []string `json:"efforts,omitempty"`
}

const (
	maxModels      = 200
	maxDisplayName = 80
	maxDescription = 200
	maxEffort      = 16
)

// validValue is a model id as the CLIs write them: "opus", "claude-opus-5-5", "opus[1m]",
// "gpt-6-astra", "openai/gpt-4o", "sonnet:high". It never starts with "-" (it would be read
// as a flag) and has no spaces or control characters.
var validValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]+-]{0,127}$`)

// ValidModelValue reports whether v can be passed as --model's one argument (#4065, Security's
// condition 3): a model from a list or typed by hand.
func ValidModelValue(v string) bool { return validValue.MatchString(v) }

// clean drops entries whose value isn't a model id and duplicates, and bounds the text, so
// what a CLI prints can't put anything else into Coral's list.
func clean(in []Model) []Model {
	out := []Model{}
	seen := map[string]bool{}
	for _, m := range in {
		m.Value = strings.TrimSpace(m.Value)
		if !ValidModelValue(m.Value) || seen[m.Value] {
			continue
		}
		seen[m.Value] = true
		m.DisplayName = plainText(m.DisplayName, maxDisplayName)
		if m.DisplayName == "" {
			m.DisplayName = m.Value
		}
		m.Description = plainText(m.Description, maxDescription)
		var efforts []string
		for _, e := range m.Efforts {
			if e = plainText(e, maxEffort); e != "" && ValidModelValue(e) {
				efforts = append(efforts, e)
			}
		}
		m.Efforts = efforts
		out = append(out, m)
		if len(out) == maxModels {
			break
		}
	}
	return out
}

// plainText is s on one line without control characters, cut to n runes.
func plainText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

// ErrNoModels means the CLI answered but listed no models (not signed in, no provider).
var ErrNoModels = errors.New("the CLI listed no models")

func result(models []Model) ([]Model, error) {
	models = clean(models)
	if len(models) == 0 {
		return nil, ErrNoModels
	}
	return models, nil
}

// ParseClaudeInitialize reads Claude Code's stream-json output up to the initialize
// control_response and returns its models. Only "models" is decoded: the reply's account
// details never leave this function.
func ParseClaudeInitialize(r io.Reader) ([]Model, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"control_response"`)) {
			continue
		}
		var reply struct {
			Type     string `json:"type"`
			Response struct {
				Subtype  string `json:"subtype"`
				Response struct {
					Models []struct {
						Value                 string   `json:"value"`
						DisplayName           string   `json:"displayName"`
						Description           string   `json:"description"`
						SupportedEffortLevels []string `json:"supportedEffortLevels"`
					} `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(line, &reply) != nil || reply.Type != "control_response" {
			continue
		}
		if reply.Response.Subtype != "success" {
			return nil, errors.New("claude refused the initialize request")
		}
		var models []Model
		for _, m := range reply.Response.Response.Models {
			models = append(models, Model{Value: m.Value, DisplayName: m.DisplayName, Description: m.Description, Efforts: m.SupportedEffortLevels})
		}
		return result(models)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("claude ended without an initialize reply")
}

// ParseCodexCatalog reads codex debug models' JSON and returns the models its picker lists
// (visibility "hide" is left out).
func ParseCodexCatalog(b []byte) ([]Model, error) {
	var catalog struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Description string `json:"description"`
			Visibility  string `json:"visibility"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &catalog); err != nil {
		return nil, err
	}
	var models []Model
	for _, m := range catalog.Models {
		if m.Visibility == "hide" {
			continue
		}
		var efforts []string
		for _, l := range m.Levels {
			efforts = append(efforts, l.Effort)
		}
		models = append(models, Model{Value: m.Slug, DisplayName: m.DisplayName, Description: m.Description, Efforts: efforts})
	}
	return result(models)
}

// ParseAgyModels reads agy models' lines: "<id>\t<display name>".
func ParseAgyModels(b []byte) ([]Model, error) {
	var models []Model
	for _, line := range strings.Split(string(b), "\n") {
		id, name, _ := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		if id = strings.TrimSpace(id); id != "" {
			models = append(models, Model{Value: id, DisplayName: name})
		}
	}
	return result(models)
}

// ParsePiModels reads pi --list-models' table ("provider  model  context  max-out  thinking
// images"); the value is "provider/model", which pi's --model takes. Without that header (pi
// prints a sentence when no provider is set up) there are no models.
func ParsePiModels(b []byte) ([]Model, error) {
	var models []Model
	header := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if !header {
			if len(f) < 2 || f[0] != "provider" || f[1] != "model" {
				return nil, ErrNoModels
			}
			header = true
			continue
		}
		if len(f) >= 2 {
			models = append(models, Model{Value: f[0] + "/" + f[1], DisplayName: f[1] + " (" + f[0] + ")"})
		}
	}
	return result(models)
}
