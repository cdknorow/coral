package secrets

import (
	"sort"
	"strings"

	"github.com/cdknorow/coral/pkg/redact"
)

// The keyword rules are regular expressions with a case-insensitive name chain and no literal to start from: run over every byte they cost more than
// the whole library did (#5141: ordinary text at a third of main's speed). Nearly all text has no name in it, so keywordSpans first finds where one
// could be, by hand, in one pass: at each '=', ':', encoded '=' (%3D), full-width '=' or ':' it reads the word before (the name, through the quote
// and backslashes of escaped JSON) and at each "--" the word after, and keeps the place only when that word ends as a name does (key, token, ...).
// The rules then run over those places and nothing else. A place is the line from its start (or 1 KB back) to the end of the value, so a rule sees
// exactly what it would in the whole text; keyword_gate_test.go holds the result equal to running them over all of it.

// nameEnds are the endings of the words a rule takes for a name (the exact names, PGPASSWORD and the like, end in one of them too).
var nameEnds = []string{"key", "token", "secret", "password", "passwd", "passphrase", "pass", "pwd", "auth", "sig", "signature", "cookie",
	"authorization", "credential", "credentials", "secret_key_base"}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func endsLikeAName(word string) bool {
	for _, e := range nameEnds {
		if strings.HasSuffix(word, e) {
			return true
		}
	}
	return false
}

// nameBefore (word, and where it starts): the word before p (lower is the lower-cased text), past the quotes, backslashes and white space between, ends like a name. word is it.
func nameBefore(lower string, p int) (word string, from int, ok bool) {
	i := p
	for i > 0 { // each byte is skipped for one anchor only: the skip stops at the first byte that is none of these, which an anchor is not
		if c := lower[i-1]; c == '"' || c == '\'' || c == '\\' || c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' {
			i--
			continue
		}
		break
	}
	j := i
	for j > 0 && i-j < 64 && isNameByte(lower[j-1]) {
		j--
	}
	word = lower[j:i]
	return word, j, word != "" && endsLikeAName(word)
}

type place struct{ start, end int }

const (
	lookBack    = 1024 // how far before a '=' a name may start, in a line longer than this
	valueWindow = 8192 // how far after it the window runs at least, to the first white space or quote after (a value holds neither)
)

// lineStart is where p's line starts, or about lookBack before p when the line is longer (then at the first byte after a non-alphanumeric one,
// so that the window does not begin in the middle of a word).
func lineStart(text string, p int) int {
	from := max(0, p-lookBack)
	i := strings.LastIndexByte(text[from:p], '\n')
	if i >= 0 {
		return from + i + 1
	}
	if from == 0 {
		return 0
	}
	for from < p && isAlnum(text[from-1]) {
		from++
	}
	return from
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func lineEnd(text string, p int) int {
	if i := strings.IndexByte(text[p:], '\n'); i >= 0 {
		return p + i
	}
	return len(text)
}

// valueEndAt is the end of a window that starts a value at p: valueWindow on, then up to the first byte a value cannot hold.
func valueEndAt(text string, p int) int {
	e := min(len(text), p+valueWindow)
	for e < len(text) {
		if c := text[e]; c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '"' || c == '\'' || c == '<' || c == '>' {
			break
		}
		e++
	}
	return e
}

func lowerASCII(text string) string {
	for i := 0; i < len(text); i++ {
		if c := text[i]; c >= 'A' && c <= 'Z' {
			b := []byte(text)
			for ; i < len(b); i++ {
				if c := b[i]; c >= 'A' && c <= 'Z' {
					b[i] = c + 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return text
}

// keywordPlaces finds where the keyword rules need to look.
func keywordPlaces(text string) []place {
	lower := lowerASCII(text)
	var ps []place
	add := func(s, e int) { ps = append(ps, place{s, e}) }
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '=', ':':
			if w, from, ok := nameBefore(lower, i); ok {
				if strings.HasSuffix(w, "cookie") {
					add(lineStart(text, from), lineEnd(text, i)) // a header's pairs run to the end of the line
				} else {
					add(lineStart(text, from), valueEndAt(text, i))
				}
			}
		case '%':
			// %3D, or %253D (encoded twice)
			j := i + 1
			if strings.HasPrefix(lower[j:], "25") {
				j += 2
			}
			if strings.HasPrefix(lower[j:], "3d") {
				if _, from, ok := nameBefore(lower, i); ok {
					add(lineStart(text, from), valueEndAt(text, i))
				}
			}
		case 0xEF: // the full-width '=' (EF BC 9D) and ':' (EF BC 9A)
			if i+2 < len(text) && text[i+1] == 0xBC && (text[i+2] == 0x9D || text[i+2] == 0x9A) {
				if _, from, ok := nameBefore(lower, i); ok {
					add(lineStart(text, from), valueEndAt(text, i))
				}
			}
		case '-':
			if i+1 < len(text) && text[i+1] == '-' && (i == 0 || text[i-1] == ' ' || text[i-1] == '\t' || text[i-1] == '"' || text[i-1] == '\'' || text[i-1] == '\n') {
				j := i + 2
				for j < len(lower) && j-i < 50 && isNameByte(lower[j]) {
					j++
				}
				if endsLikeAName(lower[i+2 : j]) {
					add(i, valueEndAt(text, j))
				}
			}
			i++ // a second '-' starts nothing
		}
	}
	// curl -u user:password, mysql -pPASSWORD, sshpass -p PASSWORD: the line.
	for _, w := range []string{"curl", "mysql", "sshpass"} {
		for from := 0; ; {
			k := strings.Index(lower[from:], w)
			if k < 0 {
				break
			}
			k += from
			add(lineStart(text, k), valueEndAt(text, lineEnd(text, k))) // the flag may start the next line (\s matches a newline), and its value follows
			from = k + len(w)
		}
	}
	return mergePlaces(ps)
}

func mergePlaces(ps []place) []place {
	if len(ps) < 2 {
		return ps
	}
	sort.Slice(ps, func(a, b int) bool { return ps[a].start < ps[b].start })
	out := ps[:1]
	for _, p := range ps[1:] {
		if last := &out[len(out)-1]; p.start <= last.end {
			last.end = max(last.end, p.end)
		} else {
			out = append(out, p)
		}
	}
	return out
}

// keywordSpans finds the values the keyword rules redact.
func keywordSpans(text string) []redact.Span {
	var out []redact.Span
	for _, p := range keywordPlaces(text) {
		for _, sp := range keywordSpansIn(text[p.start:p.end]) {
			out = append(out, redact.Span{Start: sp.Start + p.start, End: sp.End + p.start})
		}
	}
	return out
}
