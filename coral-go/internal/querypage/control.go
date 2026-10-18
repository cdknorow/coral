package querypage

import "unicode"

// HasControl reports whether s has a control character: any rune unicode.IsControl calls one (the C0 bytes 0x00 to 0x1F, DEL and the C1 range),
// not only tab, CR and LF (#4806). The search routes check a text parameter with it before they trim it: trimming removes only white space, so
// a 0x01 or a 0x7F at either end would otherwise be searched for as it is.
func HasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// ControlCases are the text values a search parameter must refuse (#4806), for the tests of every route that has one: every C0 byte and DEL, and a C1 one, at the start, in the middle and at the end.
func ControlCases() []string {
	var out []string
	bytes := []rune{0x7f, 0x85}
	for b := rune(0); b < 0x20; b++ {
		bytes = append(bytes, b)
	}
	for _, r := range bytes {
		out = append(out, string(r)+"ab", "a"+string(r)+"b", "ab"+string(r))
	}
	return out
}
