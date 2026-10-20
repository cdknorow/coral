package agentmodels

import (
	"fmt"
	"strings"
)

// Custom models (#5480): a model id the operator adds for a provider in Settings > Custom models, with the thinking levels it takes, so that it shows in every model picker. The
// id reaches a CLI as the one argument of --model, so it passes ValidModelValue (a letter or digit first, at most 128 characters, no space or control character: never
// flag-shaped) when it is saved and again when it is read, and the levels are only ones the provider's CLI is given (FamilyLevels).

// Custom is one custom model.
type Custom struct {
	Family      string   `json:"family"` // claude, codex, agy or pi
	Value       string   `json:"value"`  // what --model takes
	DisplayName string   `json:"display_name"`
	Efforts     []string `json:"efforts"` // the thinking levels this model takes; empty: the CLI's default only
}

const (
	MaxCustomPerFamily = 50
	MaxCustomTotal     = 200
	MaxCustomValueLen  = 128
)

// CustomFamilies are the providers a custom model can be added for, in the order Settings shows them.
var CustomFamilies = []string{"claude", "codex", "agy", "pi"}

// familyTypes are the agent types that run a family's CLI.
func familyTypes(family string) []string {
	switch family {
	case "claude":
		return []string{"claude"}
	case "codex":
		return []string{"codex"}
	case "agy":
		return []string{"agy", "antigravity", "gemini"}
	case "pi":
		return []string{"pi"}
	}
	return nil
}

// FamilyTypes is the agent types that run family's CLI, nil for an unknown family.
func FamilyTypes(family string) []string { return append([]string(nil), familyTypes(family)...) }

// FamilyLevels is the thinking levels a model of the family may be given: what Coral passes to the family's CLI (ThinkingLevels), empty for a provider that takes none.
func FamilyLevels(family string) []string {
	t := familyTypes(family)
	if len(t) == 0 {
		return []string{}
	}
	if l := ThinkingLevels(t[0]); l != nil {
		return l
	}
	return []string{}
}

// CustomError names what is wrong in a set of custom models: the row (its place in the list sent, from 0), the family and value on it, the field and why.
type CustomError struct {
	Row           int    `json:"row"`
	Family, Value string `json:"-"`
	Field         string `json:"field"`
	Msg           string `json:"message"`
}

func (e *CustomError) Error() string {
	return fmt.Sprintf("custom model %d (%s %q), %s: %s", e.Row+1, e.Family, e.Value, e.Field, e.Msg)
}

// ValidateCustomSet checks a whole list of custom models and returns it cleaned (text trimmed, names plain, levels in the order given), or the first problem as a *CustomError:
// nothing is kept partly. A family appears at most MaxCustomPerFamily times, the list at most MaxCustomTotal, and a (family, value) once.
func ValidateCustomSet(in []Custom) ([]Custom, error) {
	if len(in) > MaxCustomTotal {
		return nil, &CustomError{Row: MaxCustomTotal, Field: "models", Msg: fmt.Sprintf("at most %d custom models in all", MaxCustomTotal)}
	}
	out := make([]Custom, 0, len(in))
	seen := map[string]bool{}
	perFamily := map[string]int{}
	for i, c := range in {
		fail := func(field, msg string) error {
			return &CustomError{Row: i, Family: c.Family, Value: c.Value, Field: field, Msg: msg}
		}
		if familyTypes(c.Family) == nil {
			return nil, fail("family", "must be one of claude, codex, agy, pi")
		}
		c.Value = strings.TrimSpace(c.Value)
		if c.Value == "" {
			return nil, fail("value", "is required")
		}
		if !ValidModelValue(c.Value) {
			return nil, fail("value", fmt.Sprintf("must start with a letter or digit and be at most %d characters of letters, digits and . _ : / @ [ ] + - (no spaces)", MaxCustomValueLen))
		}
		key := c.Family + "\x00" + c.Value
		if seen[key] {
			return nil, fail("value", "is listed twice for this provider")
		}
		seen[key] = true
		if perFamily[c.Family]++; perFamily[c.Family] > MaxCustomPerFamily {
			return nil, fail("family", fmt.Sprintf("at most %d custom models per provider", MaxCustomPerFamily))
		}
		c.DisplayName = plainText(c.DisplayName, maxDisplayName)
		if c.DisplayName == "" {
			c.DisplayName = plainText(c.Value, maxDisplayName)
		}
		levels := FamilyLevels(c.Family)
		efforts := []string{}
		have := map[string]bool{}
		for _, e := range c.Efforts {
			e = strings.TrimSpace(e)
			if !contains(levels, e) {
				if len(levels) == 0 {
					return nil, fail("efforts", "this provider's CLI takes no thinking levels")
				}
				return nil, fail("efforts", fmt.Sprintf("%q is not a level this provider takes (%s)", plainText(e, maxEffort), strings.Join(levels, ", ")))
			}
			if have[e] {
				return nil, fail("efforts", fmt.Sprintf("%q is listed twice", e))
			}
			have[e] = true
			efforts = append(efforts, e)
		}
		c.Efforts = efforts
		out = append(out, c)
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// CleanCustom is what a stored list is read as: a row that no longer passes validation (a hand-edited database) is dropped, so it can never reach a picker or a command line.
func CleanCustom(in []Custom) []Custom {
	out := make([]Custom, 0, len(in))
	seen := map[string]bool{}
	for _, c := range in {
		one, err := ValidateCustomSet([]Custom{c})
		if err != nil || seen[one[0].Family+"\x00"+one[0].Value] {
			continue
		}
		seen[one[0].Family+"\x00"+one[0].Value] = true
		out = append(out, one[0])
	}
	return out
}

// MergeCustom adds a family's custom models to a CLI's list for agentType, after the CLI's own models; one the CLI lists too keeps its place and takes the operator's name and
// levels. The input is not changed.
func MergeCustom(models []Model, agentType string, custom []Custom) []Model {
	family := Family(agentType)
	out := append([]Model{}, models...)
	index := map[string]int{}
	for i, m := range out {
		index[m.Value] = i
	}
	for _, c := range custom {
		if c.Family != family {
			continue
		}
		m := Model{Value: c.Value, DisplayName: c.DisplayName, Efforts: append([]string(nil), c.Efforts...), Custom: true}
		if i, ok := index[c.Value]; ok {
			m.Description = out[i].Description
			out[i] = m
			continue
		}
		index[c.Value] = len(out)
		out = append(out, m)
	}
	return out
}

// MergeCustomValues adds a family's custom model ids to the curated list of ids for agentType (GET /api/agent-models), after it, once each.
func MergeCustomValues(values []string, agentType string, custom []Custom) []string {
	family := Family(agentType)
	out := append([]string{}, values...)
	have := map[string]bool{}
	for _, v := range out {
		have[v] = true
	}
	for _, c := range custom {
		if c.Family == family && !have[c.Value] {
			have[c.Value] = true
			out = append(out, c.Value)
		}
	}
	return out
}
