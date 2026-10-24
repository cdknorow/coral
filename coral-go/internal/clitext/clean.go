// Package clitext makes text from the server safe to print in a terminal (#3580). Board posts,
// task titles and another agent's screen are written by agents, so a control sequence in them
// could rewrite or hide what the terminal shows, set its title or reach the clipboard (OSC 52).
package clitext

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	esc = 0x1b
	bel = 0x07
)

// Clean returns s without terminal control: escape sequences are removed whole (CSI, OSC, DCS,
// APC, PM, SOS and two-byte ones, in their 7-bit and C1 forms), every other control character
// goes (C0 except newline and tab, DEL, C1), and the bidirectional and other format characters
// that reorder or hide text go too (see isBidiOrFormat), as do the invisible characters that
// mean nothing in text (see isInvisible, #3648). Text, newlines and tabs stay, and the
// zero-width joiner and non-joiner stay so emoji and scripts render; a name or title that must
// not carry them goes through CleanName. A string sequence with no
// terminator loses only its introducer, so no text after it is lost.
//
// What it removes can join what is left: a control byte between the bytes of a UTF-8 character (a stray "\xe2\x80", a C0 byte, "\xae") makes the character whole once the byte is gone, and a
// right-to-left override or a byte-order mark would come out of a cleaner that ran once (#6878, found by fuzzing). So it cleans again until nothing changes; each pass only removes, so it ends.
func Clean(s string) string {
	for {
		out := cleanOnce(s)
		if out == s {
			return out
		}
		s = out
	}
}

// cleanOnce is one pass of Clean.
func cleanOnce(s string) string {
	if clean(s) {
		return s
	}
	var b []byte
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			if c := s[i]; c >= 0x80 && c <= 0x9f {
				i++ // a raw 8-bit C1 byte: a control on a terminal that isn't in UTF-8 mode
				continue
			}
			b = append(b, s[i])
			i++
		case r == esc:
			i = skipEscape(s, i)
		case r >= 0x80 && r <= 0x9f:
			i = skipC1(s, i, r, size)
		case r == '\n' || r == '\t':
			b = append(b, s[i])
			i++
		case r < 0x20 || r == 0x7f || isBidiOrFormat(r) || isInvisible(r):
			i += size
		default:
			b = append(b, s[i:i+size]...)
			i += size
		}
	}
	return string(b)
}

// isBidiOrFormat reports whether r reorders or hides text without showing a glyph: the
// bidirectional overrides, embeddings and isolates (the Trojan Source attack), the left/right
// marks, the line and paragraph separators, and the byte-order mark. The zero-width joiner
// (U+200D) and non-joiner (U+200C) are NOT here: they shape emoji and scripts.
func isBidiOrFormat(r rune) bool {
	switch {
	case r >= 0x202a && r <= 0x202e: // LRE RLE PDF LRO RLO
		return true
	case r >= 0x2066 && r <= 0x2069: // LRI RLI FSI PDI
		return true
	case r == 0x200e || r == 0x200f || r == 0x061c: // LRM RLM ALM
		return true
	case r == 0x2028 || r == 0x2029: // line and paragraph separators
		return true
	case r == 0xfeff: // zero-width no-break space / BOM
		return true
	}
	return false
}

// isInvisible reports whether r is an invisible character with no meaning in text, which can
// only hide or fake what a reader sees (#3648): the zero-width space, the word joiner and the
// invisible operators (U+2060 to U+2064), the Mongolian vowel separator, and the tag
// characters (U+E0000 to U+E007F, which can spell a hidden message). Not the zero-width joiner
// and non-joiner, which emoji sequences and Persian and Indic scripts need, and not the soft
// hyphen: CleanName drops those for names and titles.
func isInvisible(r rune) bool {
	switch {
	case r == 0x200b: // zero-width space
		return true
	case r >= 0x2060 && r <= 0x2064: // word joiner, function application, invisible times, separator, plus
		return true
	case r == 0x180e: // Mongolian vowel separator
		return true
	case r >= 0xe0000 && r <= 0xe007f: // tag characters
		return true
	}
	return false
}

// CleanName returns s safe to show as one name or title: Clean, then every other Unicode format
// character too (the zero-width joiner and non-joiner, the soft hyphen, ...) and the invisible
// characters that aren't format characters (see invisibleInName), which can make two different
// names look the same or a name look empty, with whitespace folded to single spaces on one line.
func CleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || invisibleInName(r) {
			return -1
		}
		return r
	}, Clean(s))
	return strings.Join(strings.Fields(s), " ")
}

// invisibleInName reports whether r draws nothing in a name though it isn't a format character
// (#3656): the variation selectors (U+FE00 to U+FE0F and U+E0100 to U+E01EF), the Hangul fillers
// (U+115F, U+1160, U+3164, U+FFA0) and the braille blank (U+2800). Clean keeps them, since text
// needs some (U+FE0F asks for an emoji's colour form); a name doesn't.
func invisibleInName(r rune) bool {
	switch {
	case r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return true
	case r == 0x115f, r == 0x1160, r == 0x3164, r == 0xffa0:
		return true
	case r == 0x2800:
		return true
	}
	return false
}

// clean reports whether s has nothing Clean would remove (the common case: no copy).
func clean(s string) bool {
	if !utf8.ValidString(s) {
		return false // a stray byte may be an 8-bit C1 control: Clean decides
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return false
		}
		if (r >= 0x80 && r <= 0x9f) || isBidiOrFormat(r) || isInvisible(r) {
			return false
		}
	}
	return true
}

// skipEscape returns the index after the escape sequence starting at s[i] (an ESC).
func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return i + 1 // a bare ESC at the end
	}
	switch c := s[i+1]; {
	case c == '[':
		return skipCSI(s, i+2, i+2)
	case c == ']' || c == 'P' || c == '_' || c == '^' || c == 'X':
		return skipString(s, i+2, i+2)
	case c >= 0x20 && c <= 0x2f: // intermediates, then one final byte (ESC ( B and the like)
		j := i + 1
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x30 && s[j] <= 0x7e {
			return j + 1
		}
		return i + 1
	case c >= 0x30 && c <= 0x7e: // a two-byte sequence: ESC 7, ESC c, ESC M ...
		return i + 2
	default:
		return i + 1
	}
}

// skipC1 handles a C1 control (UTF-8 encoded, size bytes at s[i]): CSI and the string
// introducers take their sequence with them; any other C1 goes alone.
func skipC1(s string, i int, r rune, size int) int {
	switch r {
	case 0x9b: // CSI
		return skipCSI(s, i+size, i+size)
	case 0x9d, 0x90, 0x9f, 0x9e, 0x98: // OSC, DCS, APC, PM, SOS
		return skipString(s, i+size, i+size)
	}
	return i + size
}

// skipCSI skips parameter and intermediate bytes and the final byte from j. Unterminated, only
// the introducer goes (the rest is printed as text): it returns orElse.
func skipCSI(s string, j, orElse int) int {
	for j < len(s) && s[j] >= 0x20 && s[j] <= 0x3f {
		j++
	}
	if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
		return j + 1
	}
	return orElse
}

// skipString skips a string sequence's body up to its terminator: BEL (as OSC allows), ESC \
// or the C1 ST. Unterminated, only the introducer goes: it returns orElse.
func skipString(s string, j, orElse int) int {
	for ; j < len(s); j++ {
		switch {
		case s[j] == bel:
			return j + 1
		case s[j] == esc && j+1 < len(s) && s[j+1] == '\\':
			return j + 2
		case s[j] == 0xc2 && j+1 < len(s) && s[j+1] == 0x9c:
			return j + 2
		}
	}
	return orElse
}

// CleanJSON returns a server reply with every string in it cleaned (keys too), so whatever a
// CLI decodes and prints from it is safe. A reply that isn't JSON is cleaned as text. A reply
// with nothing to remove is returned as it came.
func CleanJSON(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[' && trimmed[0] != '"') {
		if s := string(body); !clean(s) {
			return []byte(Clean(s))
		}
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // numbers come back exactly as sent
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		if s := string(body); !clean(s) {
			return []byte(Clean(s))
		}
		return body
	}
	v, changed := cleanValue(v)
	if !changed {
		return body
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return body
	}
	return bytes.TrimRight(out.Bytes(), "\n")
}

func cleanValue(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		c := Clean(t)
		return c, c != t
	case []any:
		changed := false
		for i, e := range t {
			ce, ch := cleanValue(e)
			t[i] = ce
			changed = changed || ch
		}
		return t, changed
	case map[string]any:
		changed := false
		out := make(map[string]any, len(t))
		for k, e := range t {
			ck := Clean(k)
			ce, ch := cleanValue(e)
			out[ck] = ce
			changed = changed || ch || ck != k
		}
		return out, changed
	}
	return v, false
}
