package secrets

import (
	"context"
	"regexp"
	"strings"

	"github.com/cdknorow/coral/pkg/redact"
)

// Coral's own rules (#4265), added to betterleaks' for the provider keys it does not mask: an
// Anthropic key of any length, an OpenAI key (sk-, sk-proj-, sk-svcacct-, sk-admin-) and an AWS access
// key ID (bare, or as AWS_ACCESS_KEY_ID=). They are a Detector run beside betterleaks, so Coral Hub's
// replies and streams, agent events and coral.log lines all get them, and a library update
// cannot take them away. Each is a Go regexp (linear time, no backtracking), so the budget is not
// at risk; a rule is as narrow as its provider's shape, to keep ordinary words and hyphenated names
// (task-force-..., risk-assessment-...) out of it.
type extraRule struct {
	re *regexp.Regexp
	// ok decides a match the pattern alone cannot: it is given the matched text.
	ok func(match string) bool
}

var extraRules = []extraRule{
	// sk-ant-api03-..., sk-ant-admin01-..., any later or shorter form.
	{re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`)},
	// OpenAI: sk-<about 48 letters and digits> (no hyphen in the body), and the typed forms sk-proj-,
	// sk-svcacct-, sk-admin- (hyphens and underscores in the body). A hyphenated word that happens to
	// start with "sk-" is not a key: a key mixes letters and digits and, in the plain form, has no
	// hyphen after the prefix.
	{re: regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}`), ok: mixesLettersAndDigits},
	{re: regexp.MustCompile(`\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{20,}`), ok: mixesLettersAndDigits},
	// AWS access key IDs: AKIA (long-term), ASIA (temporary) and the other IAM prefixes. Amazon's
	// documentation example (...EXAMPLE) is not a secret, as betterleaks also treats it.
	{re: regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA|AIDA|AIPA|ANPA|ANVA|AROA|ABIA|ACCA)[A-Z0-9]{16}\b`), ok: notDocumentationKey},
}

func mixesLettersAndDigits(s string) bool {
	digit, letter := false, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			letter = true
		}
	}
	return digit && letter
}

func notDocumentationKey(s string) bool { return !strings.HasSuffix(s, "EXAMPLE") }

// withExtraRules finds what base finds and what Coral's own rules find. An error from base is the
// answer: nothing is passed on as checked when the library could not check it.
type withExtraRules struct{ base redact.Detector }

func (d withExtraRules) Find(ctx context.Context, text string) ([]redact.Span, error) {
	spans, err := d.base.Find(ctx, text)
	if err != nil {
		return nil, err
	}
	return append(spans, extraSpans(text)...), nil
}

func extraSpans(text string) []redact.Span {
	var out []redact.Span
	for _, r := range extraRules {
		for _, m := range r.re.FindAllStringIndex(text, -1) {
			if r.ok == nil || r.ok(text[m[0]:m[1]]) {
				out = append(out, redact.Span{Start: m[0], End: m[1]})
			}
		}
	}
	return out
}
