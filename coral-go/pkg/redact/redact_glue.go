package redact

import (
	"bytes"
	"strings"
)

// authGlue measures what follows "authorization" up to its value: an optional closing quote,
// spaces, ':' or '=', spaces, an optional opening quote, and an optional scheme word and a
// space. A quote is one however it is written (quoteLen): in a JSON string it is \" (#3731: a
// quoted "authorization": "Basic ..." passed through Coral Hub, whose replies are JSON). Terminal escape codes (colour) may sit anywhere a space may (#3562). -1: not an
// Authorization value; -2: the buffer ended before that could be told. scheme reports whether
// a scheme word came before the value.
func authGlue(b []byte) (n int, scheme bool) {
	i := 0
	if q := quoteLen(b); q < 0 {
		return q, false
	} else {
		i += q
	}
	if i = skipGap(b, i); i < 0 {
		return i, false
	}
	if i >= len(b) {
		return -2, false
	}
	if b[i] != ':' && b[i] != '=' {
		return -1, false
	}
	if i = skipGap(b, i+1); i < 0 {
		return i, false
	}
	if q := quoteLen(b[i:]); q < 0 {
		return q, false
	} else {
		i += q
	}
	j := i
	for j < len(b) && j-i <= 16 && (b[j] >= 'a' && b[j] <= 'z' || b[j] >= 'A' && b[j] <= 'Z') {
		j++
	}
	if j == len(b) {
		return -2, false
	}
	if j > i && j-i <= 16 {
		if k := skipGap(b, j); k == -2 || k == len(b) {
			return -2, false // the gap after the scheme word may go on in the next chunk
		} else if k > j && hasSpace(b[j:k]) {
			i, scheme = k, true
		}
	}
	if i >= len(b) {
		return -2, false
	}
	return i, scheme
}

// maxQuoteEscape bounds the backslashes taken before a quote: \" in a JSON string, \\\" when
// the text itself had an escaped quote, and so on.
const maxQuoteEscape = 8

// quoteLen is the length of the quote at the start of b, however it is written: " or ',
// after an odd run of backslashes (JSON writes " as \", and a \" in the text as \\\"), or a JSON \u0022
// or \u0027. 0: no quote there; -2: b ends before that can be told. When unsure, a quote: an
// Authorization value after it is then redacted (fail closed).
func quoteLen(b []byte) int {
	i := 0
	for i < len(b) && b[i] == '\\' && i < maxQuoteEscape {
		i++
	}
	if i == len(b) {
		if i > 0 {
			return -2
		}
		return 0
	}
	switch c := b[i]; {
	case c == '"' && i%2 == 1, c == '"' && i == 0, c == '\'':
		return i + 1
	case c == '"':
		// An even run of backslashes is that many escaped backslashes: this '"' is unescaped,
		// the end of the JSON string around the text, not a quote in it.
		return 0
	case i > 0 && (c == 'u' || c == 'U'):
		for _, hex := range []string{"u0022", "u0027"} {
			k := 0
			for k < len(hex) && i+k < len(b) && b[i+k]|0x20 == hex[k]|0x20 {
				k++
			}
			if k == len(hex) {
				return i + k
			}
			if i+k == len(b) {
				return -2
			}
		}
	}
	return 0
}

// bearerGlue: after "Bearer", spaces (at least one) and any escape codes, up to the value.
func bearerGlue(b []byte) (n int, scheme bool) {
	i := skipGap(b, 0)
	switch {
	case i < 0:
		return i, false
	case i >= len(b):
		return -2, false
	case !hasSpace(b[:i]):
		return -1, false
	}
	return i, true
}

// maxGap bounds the spaces and escape codes skipped between a scheme and its value.
const maxGap = 64

// skipGap skips spaces and terminal escape codes from b[i]: the index after them, -2 when b
// ends inside an escape code, or -1 when the gap runs past maxGap.
func skipGap(b []byte, i int) int {
	start := i
	for i < len(b) {
		if i-start > maxGap {
			return -1
		}
		if b[i] == ' ' {
			i++
			continue
		}
		n := escLen(b[i:])
		if n == -2 {
			return -2
		}
		if n == 0 {
			break
		}
		i += n
	}
	return i
}

func hasSpace(b []byte) bool {
	for k := 0; k < len(b); k++ {
		if b[k] == ' ' {
			return true
		}
		if n := escLen(b[k:]); n > 0 {
			k += n - 1
		}
	}
	return false
}

// escLen is the length of the terminal escape code (a CSI: ESC '[' parameters intermediates
// final) at the start of b, raw or as JSON writes it ("\u001b[..."); 0 when b doesn't start
// one, -2 when b ends inside one.
func escLen(b []byte) int {
	i := 0
	switch {
	case len(b) > 0 && b[0] == 0x1b:
		i = 1
	case len(b) > 0 && b[0] == '\\':
		const esc = "\\u001b"
		for k := 1; k < len(esc); k++ {
			if k >= len(b) {
				return -2
			}
			if c := b[k] | 0x20; c != esc[k]|0x20 {
				return 0
			}
		}
		i = len(esc)
	default:
		return 0
	}
	if i >= len(b) {
		return -2
	}
	if b[i] != '[' {
		return 0
	}
	for i++; i < len(b) && i < maxGap; i++ {
		c := b[i]
		if c >= 0x40 && c <= 0x7e {
			return i + 1
		}
		if c < 0x20 || c > 0x3f {
			return 0
		}
	}
	if i >= len(b) {
		return -2
	}
	return 0
}

// notQuoteEnd: inside a quoted value whose start was flushed as the marker, every byte is
// dropped up to a quote (or the backslash before an escaped one).
func notQuoteEnd(c byte) bool { return c != '"' && c != '\'' && c != '\\' }

// quoteDrop is how an over-long quoted value is dropped to its closing quote with its escapes read (#5018): notQuoteEnd alone stopped at the first
// quote OR backslash, so a value of more than the rule's reach that held an escaped quote (password="...600 bytes...x\"y TAIL") ended there and
// showed what followed. char is the quote that opened the value and run the backslashes in its spelling (0 for a plain quote, 1 for the \" of a
// JSON string). A quote closes the value when the backslashes before it are an even number (a plain quote, or the end of the JSON string around
// the text) or exactly run (the same spelling as the opening one); any other odd number is an escaped quote inside the value, and is dropped.
type quoteDrop struct {
	char   byte
	run    int
	carry  int  // backslashes that end the data already dropped: their run goes on in the next chunk
	closed bool // dropEnd found the closing spelling
}

// quoteDropFor is the drop for a value that opened with this spelling: a quote after backslashes only; any other spelling (\u0022) keeps the
// class-only drop.
func quoteDropFor(spelling []byte) *quoteDrop {
	n := len(spelling)
	if n == 0 || (spelling[n-1] != '"' && spelling[n-1] != '\'') {
		return nil
	}
	for _, b := range spelling[:n-1] {
		if b != '\\' {
			return nil
		}
	}
	return &quoteDrop{char: spelling[n-1], run: n - 1}
}

// dropEnd is where the drop from data[i:] ends: the start of the closing spelling's backslashes in data (or its quote), with emit the backslashes of
// that spelling that earlier chunks already dropped (they are written back before the quote, as a whole write keeps them), or len(data) when
// the value goes on. A run of backslashes at the end of data is not held back: its length is carried (carryTo) and the run goes on counting in
// the next chunk, so the work stays linear in the data however it is cut (#5018 review: held bytes were counted again on every write).
func (q *quoteDrop) dropEnd(data []byte, i int) (j, emit int) {
	q.closed = false
	run, carried := q.carry, q.carry
	for j = i; j < len(data); j++ {
		switch c := data[j]; {
		case c == '\\':
			run++
		case c == q.char:
			if run%2 == 0 || run == q.run {
				q.closed, q.carry = true, 0
				return j - (run - carried), carried
			}
			run, carried = 0, 0 // an escaped quote: part of the value
		default:
			run, carried = 0, 0
		}
	}
	return j, 0
}

// carryTo records the run of backslashes at the end of data[i:end), which has been dropped, so the next chunk counts on from it.
func (q *quoteDrop) carryTo(data []byte, i, end int) {
	if q == nil {
		return
	}
	n := 0
	for k := end - 1; k >= i && data[k] == '\\'; k-- {
		n++
	}
	if n == end-i {
		n += q.carry // all of it is backslashes: the earlier run continues
	}
	q.carry = n
}

// openingQuote is the quote (however written: quoteLen) that ends right at b[at], or nil. The
// longest spelling wins: \" is one quote, not a backslash and a raw ".
func openingQuote(b []byte, at int) []byte {
	for s := max(0, at-maxQuoteEscape-6); s < at; s++ {
		if q := quoteLen(b[s:]); q > 0 && s+q == at {
			return b[s:at]
		}
	}
	return nil
}

// quotedValue measures a value that starts after its opening quote (form) at b[at]: it runs to
// its closing quote (the same spelling), so PASSWORD="my pass phrase" is redacted whole, or to the
// end of the JSON string around the text if that comes first (quotedEnd, the #3731 rules), never
// past it. A value with no end within max is redacted to there and the rest of its run dropped
// (long); one that may still end in the next bytes is held (need).
func quotedValue(b []byte, at int, form []byte, max int, final bool) (n, valueAt int, ok, need, long bool) {
	if len(form) == 1 && form[0] == '"' {
		// A raw '"' followed by '}', ',', ']', ':' or the end closes the JSON string around the
		// text ({"content":"token ="}): it opens no value.
		if at == len(b) {
			return 0, 0, false, !final, false
		}
		if strings.IndexByte("},]:", b[at]) >= 0 {
			return 0, 0, false, false, false
		}
	}
	rest := b[at:]
	if len(rest) > max {
		rest = rest[:max]
	}
	end, stop := quotedEnd(form, rest, final && len(b)-at <= max)
	switch {
	case end >= 0:
		v := end - len(form) // the value, up to its closing quote
		if v == 0 {
			return 0, 0, false, false, false
		}
		return at + v, at, true, false, false
	case stop >= 0:
		if stop == 0 {
			return 0, 0, false, false, false
		}
		return at + stop, at, true, false, false
	case stop == -2 || len(b)-at <= max && !final:
		return 0, 0, false, true, false
	case len(b)-at > max:
		return at + max, at, true, false, true
	}
	if len(b) == at {
		return 0, 0, false, false, false
	}
	return len(b), at, true, false, false // the body ends inside the value: all of it (fail closed)
}

// maxParamList bounds an auth-param list (a Digest header is about 300 bytes); one that runs on
// past it is redacted to there, and the rest of its run dropped.
const maxParamList = 1024

// paramList measures the auth-param list at b: key=value pairs separated by commas (and
// spaces), a value a run of credential characters or a quoted string, its quote written however
// the text writes it (quoteLen: raw, \" in JSON, \\\" in JSON inside JSON), and closed by the same
// form. It returns the list's length and how many pairs it holds (0, 0: not a list), or -2 when b
// ends before the list does (or before maxParamList, when it has no end in sight). With final
// set, b is the whole body: one that ends inside a pair has the list run to its end (fail
// closed), and a word with no '=' after it is not a pair.
func paramList(b []byte, final bool) (n, pairs int) {
	i := 0
	// short: b ended. inPair: inside a pair (after its '='); otherwise before a key.
	short := func(inPair bool) (int, int) {
		switch {
		case !final:
			return -2, 0
		case inPair:
			return len(b), pairs + 1
		}
		return i, pairs
	}
	for {
		k := i
		for k < len(b) && (isWord(b[k]) || b[k] == '-') {
			k++
		}
		if k == len(b) {
			if k >= maxParamList {
				return maxParamList, pairs + 1
			}
			return short(false)
		}
		if k == i || b[k] != '=' {
			return i, pairs
		}
		k++
		q := quoteLen(b[k:])
		switch {
		case q == -2:
			return short(true)
		case q == 1 && b[k] == '"' && k+1 == len(b) && !final:
			return -2, 0 // the string's own end, or a value's opening quote: the next byte tells
		case q == 1 && b[k] == '"' && (k+1 == len(b) || strings.IndexByte("},]:", b[k+1]) >= 0):
			// An empty value: this quote closes the JSON string around the text (#3731,
			// Security: the list must never take it, or the reply stops being JSON).
			return k, pairs + 1
		case q > 0:
			end, stop := quotedEnd(b[k:k+q], b[k+q:], final)
			switch {
			case stop == -2:
				return -2, 0
			case stop >= 0:
				// The JSON string around the text ends inside the value: the list stops there and
				// never takes the closing quote (#3731, Security; as pemClose, #3694).
				return k + q + stop, pairs + 1
			case end < 0 && len(b) >= maxParamList:
				return maxParamList, pairs + 1
			case end < 0:
				return short(true)
			}
			k += q + end
		default:
			for k < len(b) && isCredValue(b[k]) {
				k++
			}
		}
		pairs++
		end := k
		if k > maxParamList {
			return maxParamList, pairs
		}
		for k < len(b) && b[k] == ' ' {
			k++
		}
		if k == len(b) {
			if final {
				return end, pairs
			}
			return -2, 0 // another pair may follow in the next chunk
		}
		if b[k] != ',' {
			return end, pairs
		}
		for k++; k < len(b) && b[k] == ' '; k++ {
		}
		if k == len(b) {
			if final {
				return end, pairs
			}
			return -2, 0
		}
		i = k
	}
}

// quotedEnd finds, in b (what follows a value's opening quote, written as form), the end of the
// value: the index just after its closing quote, which is the same quote after the same number
// of backslashes (end), or where the JSON string around the text ends first (stop): an
// unescaped '"' when the value's quote was escaped (the text is inside a JSON string), or, for a
// raw quote, a '"' of another kind that is followed by '}', ',', ']', ':' or the end. -1, -1:
// neither in b; stop -2: b ends right after such a '"' and only the next byte tells.
func quotedEnd(form, b []byte, final bool) (end, stop int) {
	r := len(form) - 1 // backslashes before the quote
	qc := form[r]
	if qc != '"' && qc != '\'' {
		// \u0022 or \u0027: the same spelling closes it; the string still ends at a '"'.
		e := bytes.Index(b, form)
		if u := unescapedQuote(b, false); u >= 0 && (e < 0 || u < e) {
			return -1, u
		}
		if e < 0 {
			return -1, -1
		}
		return e + len(form), -1
	}
	run := 0
	for j, c := range b {
		if c == '\\' {
			run++
			continue
		}
		switch {
		case c == qc && run == r:
			return j + 1, -1
		case c == '"' && run%2 == 0 && r > 0:
			return -1, j // escaped quotes inside, so this unescaped one ends the JSON string
		case c == '"' && run%2 == 0:
			if j+1 == len(b) {
				if !final {
					return -1, -2
				}
				return -1, j
			}
			if strings.IndexByte("},]:", b[j+1]) >= 0 {
				return -1, j
			}
		}
		run = 0
	}
	return -1, -1
}

// mixedValue: the value holds a digit or a symbol, as credentials do.
func mixedValue(v []byte) bool {
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// unescapedQuote is the index of the first '"' in b that no backslash escapes (a run of
// backslashes escapes the next byte when it is odd), counting esc as a backslash just before b;
// -1 if there is none.
func unescapedQuote(b []byte, esc bool) int {
	for k, c := range b {
		switch {
		case esc:
			esc = false
		case c == '\\':
			esc = true
		case c == '"':
			return k
		}
	}
	return -1
}

// escapeAfter is whether b, read after a backslash state esc, ends with an unpaired backslash.
func escapeAfter(b []byte, esc bool) bool {
	for _, c := range b {
		if esc {
			esc = false
		} else if c == '\\' {
			esc = true
		}
	}
	return esc
}
