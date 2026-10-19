package secrets

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cdknorow/coral/pkg/redact/betterleaks"
)

// Where the hub's rules found something, for a refusal that has to say why (#5212): a person told "this file looks like it contains a secret" cannot find it in 4,000 lines. ExplainHub gives the
// 1-based line of the first byte the redaction changed and a short description of the rule that found it on that line. It never repeats any part of the secret: a rule's name, the word before a
// value (api_key, token), nothing of the value.
func ExplainHub(text, redacted []byte, budget time.Duration) (line int, rule string) {
	i := firstDifference(text, redacted)
	if i < 0 {
		return 0, ""
	}
	line = bytes.Count(text[:i], []byte{'\n'}) + 1
	start := bytes.LastIndexByte(text[:i], '\n') + 1
	end := len(text)
	if k := bytes.IndexByte(text[i:], '\n'); k >= 0 {
		end = i + k
	}
	if end-start > 8192 { // a very long line: the window around the change
		start, end = max(start, i-4096), min(end, i+4096)
	}
	return line, ruleOn(string(text[start:end]), budget)
}

func firstDifference(a, b []byte) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// ruleOn says which layer of the rules finds something in text, the one a person can act on: the Coral rules, the keyword rules (with the name before the value), or a betterleaks rule by its id.
func ruleOn(text string, budget time.Duration) string {
	if sp := extraSpans(text); len(sp) > 0 {
		return "one of Coral's own rules (a bearer token, a provider key, a private key or a URL credential)"
	}
	if sp := keywordSpans(text); len(sp) > 0 {
		if name := nameAhead(text, sp[0].Start); name != "" {
			return fmt.Sprintf("a value after the name %q (the keyword rules)", name)
		}
		return "a value after a name such as api_key, token or password (the keyword rules)"
	}
	if bd, ok := det.(withExtraRules); ok {
		if d, ok := bd.base.(*betterleaks.Detector); ok {
			ctx, cancel := context.WithTimeout(context.Background(), max(budget, time.Second))
			defer cancel()
			if ids, err := d.RuleIDs(ctx, text); err == nil && len(ids) > 0 {
				return "betterleaks rule " + ids[0]
			}
		}
	}
	return "the secret rules"
}

// nameAhead is the name before the value that starts at p: the separator characters, quotes and an encoded '=' or ':' before the value are skipped, then the name's own bytes (letters, digits, '_', '-') are read back, at most 40.
func nameAhead(text string, p int) string {
	i := p
	for steps := 0; i > 0 && steps < 24; steps++ {
		if i >= 3 {
			if enc := strings.ToLower(text[i-3 : i]); enc == "%3d" || enc == "%3a" {
				i -= 3
				continue
			}
		}
		if strings.IndexByte(" \t:=\"'\\%&;+", text[i-1]) >= 0 {
			i--
			continue
		}
		break
	}
	j := i
	for j > 0 && i-j < 40 && isNameByte(lowerByte(text[j-1])) {
		j--
	}
	return text[j:i]
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}
