// Package teamhealth holds the team health check's rules and the one sanitiser for anything an
// agent wrote that Coral shows (#4175; Security's #4176).
package teamhealth

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cdknorow/coral/pkg/redact"
)

// MaxShown is the longest line of agent text Coral shows, label included (#4176 rule 2).
const MaxShown = 120

// quotedLabel marks the line as an agent's words, not an instruction.
const quotedLabel = "quoted: "

// NotShown stands for agent text that could not be checked for secrets: it is never shown.
const NotShown = "(not shown)"

// csi is a terminal escape sequence (colour, cursor): dropped whole, not only its ESC.
var csi = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b[@-_]")

// ShownText is the one line of agent-authored text Coral may show (the last reply's first line,
// a task title in the orchestrator's check), by Security's #4176 rule 2, in this order:
// redacted whole with the Hub redactor (rd; redact.Rules when nil), failing closed; then its
// first line; escape sequences, control, format (bidi, zero-width) characters dropped; every @
// neutralised so it can't mention anyone; spaces collapsed; then cut, so a cut can never split a
// secret the redactor would have caught. The result is labelled as quoted agent text and at most
// MaxShown characters long.
func ShownText(text string, rd redact.Redactor) string {
	if rd == nil {
		rd = redact.Rules
	}
	red, err := rd.Text([]byte(text))
	if err != nil || !utf8.Valid(red) {
		return NotShown
	}
	s := string(red)
	if i := strings.IndexAny(s, "\r\n\u2028\u2029\x0b\x0c\u0085"); i >= 0 {
		s = s[:i]
	}
	s = clean(s, MaxShown-len(quotedLabel))
	if s == "" {
		return ""
	}
	return quotedLabel + s
}

// Name is a member's name as it goes into a Coral line or nudge (#4176 note 1 on step 1): the
// same cleaning as ShownText, without the label, at most 64 characters. Names come from the
// board's subscribers, which already refuse control characters; this keeps an @ or a bidi mark
// in a name out of Coral's words too.
func Name(s string) string {
	if i := strings.IndexAny(s, "\r\n\u2028\u2029"); i >= 0 {
		s = s[:i]
	}
	return clean(s, 64)
}

// clean drops escape sequences, control and format characters, neutralises @, collapses spaces
// and cuts to max characters (an ellipsis marks a cut).
func clean(s string, max int) string {
	s = csi.ReplaceAllString(s, "")
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == '\u2028', r == '\u2029':
			continue
		case r == '@' || r == '\uff20' || r == '\ufe6b':
			b.WriteString("(at)")
			space = false
		case unicode.IsSpace(r):
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
		default:
			b.WriteRune(r)
			space = false
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > max {
		out = strings.TrimSpace(string([]rune(out)[:max-1])) + "…"
	}
	return out
}
