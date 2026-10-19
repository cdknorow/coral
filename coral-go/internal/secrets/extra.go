package secrets

import (
	"context"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cdknorow/coral/pkg/redact"
)

// Coral's own rules (#4265), added to betterleaks' for the provider keys it does not mask: an
// Anthropic key of any length, an OpenAI key (sk-, sk-proj-, sk-svcacct-, sk-admin-) and an AWS access
// key ID (bare, or as AWS_ACCESS_KEY_ID=), and the password of a URL's credentials (scheme://user:password@host, #4769). They are a Detector run beside betterleaks, so Coral Hub's
// replies and streams, agent events and coral.log lines all get them, and a library update
// cannot take them away. Each is a Go regexp (linear time, no backtracking), so the budget is not
// at risk; a rule is as narrow as its provider's shape, to keep ordinary words and hyphenated names
// (task-force-..., risk-assessment-...) out of it.
type extraRule struct {
	re *regexp.Regexp
	// group is the capture group that holds the secret (0: the whole match); what surrounds it, such
	// as "Authorization: Bearer ", is left showing.
	group int
	// ok decides a match the pattern alone cannot: it is given the matched text.
	ok func(match string) bool
}

var extraRules = []extraRule{
	// A bearer token in a header, a JSON field or a log line: "Authorization: Bearer <token>",
	// "Bearer <token>". A token mixes letters and digits, so the word "bearer" in prose is left alone.
	{re: regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{20,})`), group: 1, ok: mixesLettersAndDigits},
	// "Authorization: Basic <base64>": needs the header's name, since "basic" is an ordinary word.
	{re: regexp.MustCompile(`(?i)\bauthorization["']?\s*[:=]\s*["']?basic\s+([A-Za-z0-9+/]{12,}={0,2})`), group: 1},
	// A JSON web token: header.payload.signature, the first two segments base64url of an object ("eyJ").
	{re: regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)},
	// Hugging Face and Google OAuth access tokens.
	{re: regexp.MustCompile(`\bhf_[A-Za-z0-9]{34,}`), ok: mixesLettersAndDigits},
	{re: regexp.MustCompile(`\bya29\.[A-Za-z0-9_-]{30,}`)},
	// An AWS secret access key after its name, with or without spaces around the "=".
	{re: regexp.MustCompile(`(?i)aws_?secret_?access_?key["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})`), group: 1},
	// sk-ant-api03-..., sk-ant-admin01-..., any later or shorter form.
	{re: regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}`)},
	// OpenAI: sk-<about 48 letters and digits> (no hyphen in the body), and the typed forms sk-proj-,
	// sk-svcacct-, sk-admin- (hyphens and underscores in the body). A hyphenated word that happens to
	// start with "sk-" is not a key: a key mixes letters and digits and, in the plain form, has no
	// hyphen after the prefix.
	{re: regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}`), ok: mixesLettersAndDigits},
	{re: regexp.MustCompile(`\bsk-(?:proj|svcacct|admin)-[A-Za-z0-9_-]{20,}`), ok: mixesLettersAndDigits},
	// A URL's user name that is itself the credential (https://<token>@host, no password): a long user name that mixes letters and digits.
	// The passwords of scheme://user:password@host are found by redact.URLPasswordSpans (extraSpans), which needs the URL's own shape (#4769).
	{re: regexp.MustCompile(`://([A-Za-z0-9._~%+-]{24,})@`), group: 1, ok: mixesLettersAndDigits},
	// A PEM private key, header line to end line (#5158): betterleaks has the same rule, but its global false-positive filter drops a finding whose
	// text holds "false" in any case, so a block whose base64 happens to hold those five letters (about one in twenty thousand) was shown whole.
	{re: regexp.MustCompile(`(?i)-----BEGIN[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----[\s\S]*?-----END[ A-Z0-9_-]{0,100}PRIVATE KEY(?: BLOCK)?-----`)},
	// A key in an HTTP header or its JSON form, named by the header: X-Api-Key: <value>, "X-Auth-Token": "<value>" (#5190). betterleaks' generic rule drops a value whose text holds a common word
	// (house, table, apple, false...) or reads as words, as a false positive, and a 32-character key that holds one was shown whole; the header's name already says what the value is, so a value of 16
	// characters or more after one of these names is the secret, whatever it looks like. Only these header names: ordinary text has none (the repository's own docs and sources hold two, both test secrets).
	{re: regexp.MustCompile(`(?i)\b(?:x-?api-?key|api-?key|x-?api-?token|x-?auth-?token|x-?access-?token|x-?auth-?key|x-?secret(?:-?key)?|x-?api-?secret|private-?token)\b["']?[ \t]*:[ \t]*["']?([A-Za-z0-9._~+/=-]{16,})`), group: 1},
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
	spans, err := d.find(ctx, text)
	if err != nil {
		return nil, err
	}
	// An invisible character inside a key ends a rule's match and leaves the rest showing (a
	// zero-width space typed into a token, #4265): look at the text with them dropped too, and map what
	// is found there back, over the invisibles between.
	if stripped, at := dropInvisibles(text); at != nil {
		more, err := d.find(ctx, stripped)
		if err != nil {
			return nil, err
		}
		for _, sp := range more {
			if sp.Start < 0 || sp.End > len(at) || sp.Start >= sp.End {
				continue
			}
			spans = append(spans, redact.Span{Start: at[sp.Start], End: at[sp.End-1] + 1})
		}
	}
	return spans, nil
}

func (d withExtraRules) find(ctx context.Context, text string) ([]redact.Span, error) {
	spans, err := d.base.Find(ctx, text)
	if err != nil {
		return nil, err
	}
	return append(spans, extraSpans(text)...), nil
}

// invisible reports the characters that show nothing, or only change how their neighbours show, and
// can sit inside a token: every format character (Unicode Cf: zero-width and joiner marks, the soft
// hyphen, the byte-order mark, bidi marks and isolates, invisible operators, tag characters,
// variation selectors are Mn), combining marks (Mn, Me) and control characters other than line
// ends and the tab. Only the scan's copy loses them; the text shown is never changed.
func invisible(r rune) bool {
	if r < 0x80 {
		return r < 0x20 && r != '\n' && r != '\r' && r != '\t' || r == 0x7f
	}
	return unicode.Is(unicode.Cf, r) || unicode.In(r, unicode.Mn, unicode.Me) || (r >= 0x80 && r < 0xA0)
}

// dropInvisibles returns text without its invisible characters and, for each byte of the result,
// its offset in text; nil when there are none (the common case costs one scan).
func dropInvisibles(text string) (string, []int) {
	has := false
	for _, r := range text {
		if invisible(r) {
			has = true
			break
		}
	}
	if !has {
		return text, nil
	}
	var b strings.Builder
	at := make([]int, 0, len(text))
	for i, r := range text {
		if invisible(r) {
			continue
		}
		_, n := utf8.DecodeRuneInString(text[i:])
		b.WriteString(text[i : i+n])
		for k := 0; k < n; k++ {
			at = append(at, i+k)
		}
	}
	return b.String(), at
}

func extraSpans(text string) []redact.Span {
	// The password of a URL (scheme://user:password@host, #4769): betterleaks has no rule for it, and the connector had shown it to hub viewers.
	out := redact.URLPasswordSpans(text)
	for _, r := range extraRules {
		for _, m := range r.re.FindAllStringSubmatchIndex(text, -1) {
			a, b := m[2*r.group], m[2*r.group+1]
			if a >= 0 && (r.ok == nil || r.ok(text[a:b])) {
				out = append(out, redact.Span{Start: a, End: b})
			}
		}
	}
	return out
}
