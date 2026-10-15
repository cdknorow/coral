package tunnel

// Token redaction for Coral Hub viewers (#3560, design c73e2d6a). The connector replaces
// well-known credential formats in the text replies of View routes with RedactMarker before
// they leave this computer; local requests never pass through here. Rules: a literal prefix,
// a character class, a length floor and ceiling, and a word boundary before the token. There
// is no entropy rule, so hashes, UUIDs and base64 are left alone. A PEM private-key block is
// removed from its BEGIN line to its END line, to the end of the JSON string it sits in, or to
// the end of the reply, however long it is: a key is never flushed in part.
//
// The Redactor streams: each Write emits everything that can no longer be part of a token and
// holds back only a tail that could still start or continue one (bounded by the longest rule,
// or the few bytes that could start a key's END line), so memory stays flat whatever the
// reply's size. A body redacted in one Write comes out byte for byte the same as any split of
// it (tested).

import (
	"bytes"
	"io"
	"strings"
)

// RedactMarker replaces a token. For Bearer and Authorization values the scheme is kept.
const RedactMarker = "[redacted token]"

type tokenRule struct {
	prefixes []string                 // literal starts (case variants listed explicitly)
	glue     func([]byte) (int, bool) // after the prefix, the separator before the value and whether it named a scheme (Authorization); nil: none
	class    func(byte) bool          // the value's characters
	min, max int                      // the value's length; a longer run is still redacted (max only bounds what is held back)
	exact    bool                     // the value is exactly min long (AKIA)
	keep     bool                     // keep the prefix and glue, replace only the value (Bearer, Authorization)
	notAfter func(byte) bool          // a character that may not come right before the prefix
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
func isWord(c byte) bool       { return isAlnum(c) || c == '_' }
func isUpperDigit(c byte) bool { return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
func isB64URL(c byte) bool     { return isAlnum(c) || c == '_' || c == '-' }
func isAlnumDash(c byte) bool  { return isAlnum(c) || c == '-' }
func isCredValue(c byte) bool {
	return isAlnum(c) || c == '.' || c == '_' || c == '~' || c == '+' || c == '/' || c == '=' || c == '-'
}

// authGlue measures what follows "authorization" up to its value: an optional closing quote,
// spaces, ':' or '=', spaces, an optional opening quote, and an optional scheme word and one
// space. -1: not an Authorization value; -2: the buffer ended before that could be told. scheme
// reports whether a scheme word came before the value.
func authGlue(b []byte) (n int, scheme bool) {
	i := 0
	if i < len(b) && (b[i] == '"' || b[i] == '\'') {
		i++
	}
	for i < len(b) && b[i] == ' ' {
		i++
	}
	if i >= len(b) {
		return -2, false
	}
	if b[i] != ':' && b[i] != '=' {
		return -1, false
	}
	i++
	for i < len(b) && b[i] == ' ' {
		i++
	}
	if i < len(b) && (b[i] == '"' || b[i] == '\'') {
		i++
	}
	j := i
	for j < len(b) && j-i <= 16 && (b[j] >= 'a' && b[j] <= 'z' || b[j] >= 'A' && b[j] <= 'Z') {
		j++
	}
	if j == len(b) {
		return -2, false
	}
	if j > i && j-i <= 16 && b[j] == ' ' {
		i, scheme = j+1, true
	}
	if i >= len(b) {
		return -2, false
	}
	return i, scheme
}

var tokenRules = []tokenRule{
	{prefixes: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}, class: isAlnum, min: 36, max: 255, notAfter: isWord},
	{prefixes: []string{"github_pat_"}, class: isWord, min: 22, max: 255, notAfter: isWord},
	{prefixes: []string{"sk-"}, class: isB64URL, min: 20, max: 255, notAfter: isB64URL},
	{prefixes: []string{"AKIA", "ASIA"}, class: isUpperDigit, min: 16, max: 16, exact: true, notAfter: isAlnum},
	{prefixes: []string{"xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-"}, class: isAlnumDash, min: 10, max: 255, notAfter: isWord},
	{prefixes: []string{"Bearer ", "bearer ", "BEARER "}, class: isCredValue, min: 20, max: 512, keep: true, notAfter: isWord},
	{prefixes: []string{"Authorization", "authorization", "AUTHORIZATION"}, glue: authGlue, class: isCredValue, min: 8, max: 512, keep: true, notAfter: isWord},
}

// PEM private-key blocks: "-----BEGIN <LABEL>PRIVATE KEY-----" ... "-----END <LABEL>-----".
var (
	pemBegin  = []byte("-----BEGIN ")
	pemKeyEnd = []byte("PRIVATE KEY")
	pemTails  = [][]byte{[]byte("-----"), []byte(" BLOCK-----")} // "...PRIVATE KEY-----", PGP's "...PRIVATE KEY BLOCK-----"
	pemEnd    = []byte("-----END ")
	pemDashes = []byte("-----")
)

const maxPEMLabel = 64

// Redactor redacts what is written to it and passes the result on to its destination.
type Redactor struct {
	dst     io.Writer
	pending []byte          // held back: it could still start or continue a token
	prev    []byte          // the last few bytes of the original stream before pending (for the boundary)
	inPEM   bool            // inside a private key: dropped until its END line or the closing quote
	inToken func(byte) bool // inside a token longer than its rule's ceiling: its class is dropped
	err     error
	buf     []byte // pending + the new bytes (reused)
	out     []byte // the redacted output of one Write (reused; dst must not keep it)
	st      starts
}

// NewRedactor returns a Redactor writing the redacted stream to dst. Close flushes it.
func NewRedactor(dst io.Writer) *Redactor { return &Redactor{dst: dst} }

// Write redacts p (after what was held back) and emits everything that is settled.
func (r *Redactor) Write(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	data := p
	if len(r.pending) > 0 {
		r.buf = append(append(r.buf[:0], r.pending...), p...)
		data = r.buf
	}
	out, consumed := r.process(data, false)
	if consumed > 0 {
		r.prev = append(r.prev, data[max(0, consumed-escapeContext):consumed]...)
		r.prev = r.prev[:copy(r.prev, r.prev[max(0, len(r.prev)-escapeContext):])]
	}
	r.pending = append(r.pending[:0], data[consumed:]...)
	if len(out) > 0 {
		if _, err := r.dst.Write(out); err != nil {
			r.err = err
			return 0, err
		}
	}
	return len(p), nil
}

// Close redacts and emits what was held back; a private key still open is dropped.
func (r *Redactor) Close() error {
	if r.err != nil {
		return r.err
	}
	out, _ := r.process(r.pending, true)
	r.pending = nil
	if len(out) > 0 {
		if _, err := r.dst.Write(out); err != nil {
			r.err = err
			return err
		}
	}
	return nil
}

// RedactTokens redacts a whole body.
func RedactTokens(b []byte) []byte {
	var out bytes.Buffer
	r := NewRedactor(&out)
	_, _ = r.Write(b)
	_ = r.Close()
	return out.Bytes()
}

// process redacts data and returns the output and how many bytes of data it settled; the rest
// is held back. With final set, all of data is settled.
func (r *Redactor) process(data []byte, final bool) (out []byte, consumed int) {
	out = r.out[:0]
	defer func() { r.out = out[:0] }()
	st := &r.st
	st.reset(data, r.prev)
	i := 0
	for i < len(data) {
		if r.inToken != nil {
			// The rest of an over-long token: dropped up to the first byte outside its class.
			j := i
			for j < len(data) && r.inToken(data[j]) {
				j++
			}
			if j == len(data) {
				if final {
					r.inToken = nil
				}
				return out, len(data)
			}
			r.inToken = nil
			i = j
			continue
		}
		if r.inPEM {
			end, found, hold := pemClose(data[i:], final)
			if !found {
				if final {
					return out, len(data)
				}
				return out, len(data) - hold // the key so far is dropped; a possible END start is held
			}
			r.inPEM = false
			i += end
			continue
		}
		start, rule := st.next(i)
		if start < 0 {
			if final {
				return append(out, data[i:]...), len(data)
			}
			cut := len(data) - partialPrefixTail(data[i:])
			return append(out, data[i:cut]...), cut
		}
		out = append(out, data[i:start]...)
		if rule == nil { // a PEM BEGIN
			n, ok, need := pemLabel(data[start:])
			switch {
			case need && !final:
				return out, start
			case !ok:
				out = append(out, data[start])
				i = start + 1
			default:
				out = append(out, RedactMarker...)
				r.inPEM = true
				i = start + n
			}
			continue
		}
		n, valueAt, ok, need, long := rule.match(data[start:], final)
		switch {
		case need && !final:
			return out, start
		case !ok:
			out = append(out, data[start])
			i = start + 1
		default:
			if rule.keep {
				out = append(out, data[start:start+valueAt]...)
			}
			out = append(out, RedactMarker...)
			i = start + n
			if long {
				r.inToken = rule.class // the rest of the run is dropped as it arrives
			}
		}
	}
	return out, len(data)
}

// starts finds, in order, where in data a rule's prefix or a PEM BEGIN starts with a valid
// boundary before it. Each search is for a short anchor that several prefixes share (or a
// distinctive part of one), checked against the full prefixes where it's found, and the next
// occurrence of each anchor is remembered, so a reply costs about ten passes in all however
// many near misses it holds.
type starts struct {
	data []byte
	prev []byte // the bytes just before data[0] (empty: the start of the stream)
	lits []startLit
}

// escapeContext is how many bytes before a token the boundary rule looks at (a \uXXXX escape).
const escapeContext = 6

type startLit struct {
	anchor []byte
	back   int        // the anchor's offset inside the prefix
	full   [][]byte   // the prefixes it stands for, checked at the start
	rule   *tokenRule // nil: a PEM BEGIN
	at     int        // the next occurrence's start, -1 none
	looked bool
}

// startAnchors pairs each anchor with the rule whose prefixes it finds.
var startAnchors = func() []startLit {
	var out []startLit
	add := func(anchor string, back int, rule *tokenRule) {
		l := startLit{anchor: []byte(anchor), back: back, rule: rule}
		if rule == nil {
			l.full = [][]byte{pemBegin}
		} else {
			for _, pre := range rule.prefixes {
				if back+len(anchor) <= len(pre) && pre[back:back+len(anchor)] == anchor {
					l.full = append(l.full, []byte(pre))
				}
			}
		}
		out = append(out, l)
	}
	add("gh", 0, &tokenRules[0])
	add("_pat_", 6, &tokenRules[1])
	add("sk-", 0, &tokenRules[2])
	add("AKIA", 0, &tokenRules[3])
	add("ASIA", 0, &tokenRules[3])
	add("xox", 0, &tokenRules[4])
	add("earer ", 1, &tokenRules[5])
	add("EARER ", 1, &tokenRules[5])
	add("uthorization", 1, &tokenRules[6])
	add("UTHORIZATION", 1, &tokenRules[6])
	add("-----BEGIN ", 0, nil)
	return out
}()

func (s *starts) reset(data, prev []byte) {
	s.data, s.prev = data, prev
	if s.lits == nil {
		s.lits = append([]startLit(nil), startAnchors...)
	}
	for k := range s.lits {
		s.lits[k].looked, s.lits[k].at = false, -1
	}
}

// find sets l.at to the first start at or after from whose full prefix matches there.
func (s *starts) find(l *startLit, from int) {
	l.looked = true
	for search := from + l.back; search <= len(s.data); {
		j := bytes.Index(s.data[search:], l.anchor)
		if j < 0 {
			break
		}
		at := search + j - l.back
		for _, pre := range l.full {
			if bytes.HasPrefix(s.data[at:], pre) {
				l.at = at
				return
			}
		}
		search += j + 1
	}
	l.at = -1
}

// next returns the first valid start at or after i (-1: none) and its rule (nil: a PEM BEGIN).
func (s *starts) next(i int) (int, *tokenRule) {
	best := -1
	var bestRule *tokenRule
	for k := range s.lits {
		l := &s.lits[k]
		if !l.looked || (l.at >= 0 && l.at < i) {
			s.find(l, i)
		}
		for l.at >= 0 && l.rule != nil && !s.boundaryOK(l.at, l.rule) {
			s.find(l, l.at+1)
		}
		if l.at >= 0 && (best < 0 || l.at < best) {
			best, bestRule = l.at, l.rule
		}
	}
	return best, bestRule
}

// byteBefore is the byte k places before data[at], reaching into the bytes before data;
// ok is false before the start of the stream.
func (s *starts) byteBefore(at, k int) (byte, bool) {
	if i := at - k; i >= 0 {
		return s.data[i], true
	} else if j := len(s.prev) + i; j >= 0 {
		return s.prev[j], true
	}
	return 0, false
}

// boundaryOK: the prefix at data[at] doesn't continue a word. A JSON escape right before it
// ("\n", "\t", "\"", "\u003e" and the like) counts as a boundary, since in the text it stands
// for a separator.
func (s *starts) boundaryOK(at int, ru *tokenRule) bool {
	before, ok := s.byteBefore(at, 1)
	if !ok || !ru.notAfter(before) {
		return true
	}
	if bs, ok := s.byteBefore(at, 2); ok && bs == '\\' && strings.IndexByte("nrtbf", before) >= 0 {
		return true
	}
	if bs, ok := s.byteBefore(at, 6); ok && bs == '\\' {
		if u, _ := s.byteBefore(at, 5); u == 'u' {
			hex := true
			for k := 4; k >= 1; k-- {
				c, _ := s.byteBefore(at, k)
				hex = hex && (c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F')
			}
			return hex
		}
	}
	return false
}

// match reports the token at b, which starts with one of the rule's prefixes: its length so far,
// where its value starts, whether it is a token, whether more bytes are needed to tell (never
// when final: the body ends there), and whether it runs past the rule's ceiling (it is then a
// token whose rest is dropped as it arrives; the ceiling only bounds what is held back).
func (ru *tokenRule) match(b []byte, final bool) (n, valueAt int, ok, need, long bool) {
	at := 0
	for _, pre := range ru.prefixes {
		if bytes.HasPrefix(b, []byte(pre)) {
			at = len(pre)
			break
		}
	}
	scheme := true
	if ru.glue != nil {
		g, sch := ru.glue(b[at:])
		switch {
		case g == -2:
			return 0, 0, false, !final, false
		case g < 0:
			return 0, 0, false, false, false
		}
		at, scheme = at+g, sch
	}
	run := 0
	for at+run < len(b) && ru.class(b[at+run]) {
		run++
		if run > ru.max {
			if ru.exact {
				return 0, 0, false, false, false // longer than the fixed length: not this kind
			}
			if !scheme && !mixedValue(b[at:at+run]) {
				return 0, 0, false, false, false
			}
			return at + run, at, true, false, true
		}
	}
	if at+run == len(b) && !final {
		return 0, 0, false, true, false // the value may go on in the next chunk
	}
	if run < ru.min || (ru.exact && run != ru.min) {
		return 0, 0, false, false, false
	}
	if !scheme && !mixedValue(b[at:at+run]) {
		// "Authorization: unavailable": a bare value of letters only is a word, not a credential.
		return 0, 0, false, false, false
	}
	return at + run, at, true, false, false
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

// pemLabel checks for "-----BEGIN <LABEL>PRIVATE KEY-----" (or PGP's "...PRIVATE KEY BLOCK-----")
// at b and returns its length.
func pemLabel(b []byte) (n int, ok, need bool) {
	rest := b[len(pemBegin):]
	limit := min(len(rest), maxPEMLabel)
	if k := bytes.Index(rest[:limit], pemKeyEnd); k >= 0 {
		for _, c := range rest[:k] {
			if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ') {
				return 0, false, false
			}
		}
		after := rest[k+len(pemKeyEnd):]
		for _, tail := range pemTails {
			if bytes.HasPrefix(after, tail) {
				return len(pemBegin) + k + len(pemKeyEnd) + len(tail), true, false
			}
		}
		for _, tail := range pemTails {
			if len(after) < len(tail) && bytes.HasPrefix(tail, after) {
				return 0, false, true // the label's end is still arriving
			}
		}
		return 0, false, false
	}
	if len(rest) >= maxPEMLabel {
		return 0, false, false
	}
	for _, c := range rest { // still arriving, unless it holds a character no label has
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ' ' || c == '-') {
			return 0, false, false
		}
	}
	return 0, false, true
}

// pemClose finds where an open private key ends in b: after its "-----END <LABEL>-----"
// marker, or at (not after) the first '"', the end of the JSON string holding it. When it isn't
// found, hold is how many trailing bytes might begin the END marker.
func pemClose(b []byte, final bool) (end int, found bool, hold int) {
	q := bytes.IndexByte(b, '"')
	for e := bytes.Index(b, pemEnd); e >= 0 && (q < 0 || e < q); {
		rest := b[e+len(pemEnd):]
		if k := bytes.Index(rest[:min(len(rest), maxPEMLabel)], pemDashes); k >= 0 {
			return e + len(pemEnd) + k + len(pemDashes), true, 0
		}
		if len(rest) < maxPEMLabel && q < 0 && !final {
			return 0, false, len(b) - e // the END line may finish in the next chunk
		}
		next := bytes.Index(b[e+1:], pemEnd)
		if next < 0 {
			break
		}
		e += 1 + next
	}
	if q >= 0 {
		return q, true, 0
	}
	return 0, false, partialSuffix(b, pemEnd)
}

// partialPrefixTail is how many trailing bytes of b might begin a prefix or a PEM BEGIN.
func partialPrefixTail(b []byte) int {
	n := partialSuffix(b, pemBegin)
	for _, ru := range tokenRules {
		for _, pre := range ru.prefixes {
			n = max(n, partialSuffix(b, []byte(pre)))
		}
	}
	return n
}

// partialSuffix is the length of the longest proper prefix of pre that b ends with.
func partialSuffix(b, pre []byte) int {
	for k := min(len(pre)-1, len(b)); k > 0; k-- {
		if bytes.HasSuffix(b, pre[:k]) {
			return k
		}
	}
	return 0
}
