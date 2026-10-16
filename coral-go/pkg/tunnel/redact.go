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
//
// A live stream that goes quiet (a terminal, #3562) calls Flush, which settles the held tail
// without ending the stream: see Flush.

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
	params   bool                     // the value may be an auth-param list (Digest's key="value", ...): redacted to its end
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
// after any backslashes (JSON writes " as \", and a \" in the text as \\\"), or a JSON \u0022
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
	case c == '"' || c == '\'':
		return i + 1
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

var tokenRules = []tokenRule{
	{prefixes: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}, class: isAlnum, min: 36, max: 255, notAfter: isWord},
	{prefixes: []string{"github_pat_"}, class: isWord, min: 22, max: 255, notAfter: isWord},
	{prefixes: []string{"sk-"}, class: isB64URL, min: 20, max: 255, notAfter: isB64URL},
	{prefixes: []string{"AKIA", "ASIA"}, class: isUpperDigit, min: 16, max: 16, exact: true, notAfter: isAlnum},
	{prefixes: []string{"xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-"}, class: isAlnumDash, min: 10, max: 255, notAfter: isWord},
	{prefixes: []string{"Bearer", "bearer", "BEARER"}, glue: bearerGlue, class: isCredValue, min: 20, max: 512, keep: true, notAfter: isWord},
	{prefixes: []string{"Authorization", "authorization", "AUTHORIZATION"}, glue: authGlue, class: isCredValue, min: 8, max: 512, keep: true, notAfter: isWord, params: true},
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
	pemEsc  bool            // inside a private key: the last byte dropped was an unpaired backslash
	inToken func(byte) bool // inside a token longer than its rule's ceiling: its class is dropped
	shown   int             // the leading bytes of pending that Flush already emitted as they are
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
	out = r.skipShown(out, data, consumed)
	if len(out) > 0 {
		if _, err := r.dst.Write(out); err != nil {
			r.err = err
			return 0, err
		}
	}
	return len(p), nil
}

// skipShown drops from out what Flush already emitted: the leading bytes of data, shown as
// they were. Where a token turned out to start inside them, out differs from data from there
// on, and the viewer, having seen its first characters, gets the marker after them.
func (r *Redactor) skipShown(out, data []byte, consumed int) []byte {
	if r.shown == 0 {
		return out
	}
	k := 0
	for k < r.shown && k < len(out) && out[k] == data[k] {
		k++
	}
	r.shown = max(0, r.shown-consumed)
	return out[k:]
}

// Flush settles the held tail of a stream that has gone quiet, without ending it (#3562: a
// terminal must not sit on its last characters). No character of a token's value is ever
// shown:
//   - A tail with no value character in it yet (the first letters of a prefix, a whole prefix,
//     "Authorization: Bearer" before its value, a key's BEGIN line still arriving) is shown as
//     it is but kept as context, so a token it turns out to start is still caught: the viewer
//     sees those words, then the marker.
//   - A tail holding a prefix and the start of its value is held while force is false. With
//     force, its value is replaced by the marker now, whatever its length (the prefix and
//     scheme stay when the rule keeps them), and the rest of the run is dropped if more
//     arrives.
//   - Inside a private key nothing is shown, so Flush waits for its END line.
func (r *Redactor) Flush(force bool) error {
	if r.err != nil {
		return r.err
	}
	if r.inPEM || len(r.pending) <= r.shown {
		return nil
	}
	var out []byte
	if rule, valueAt := heldValue(r.pending); rule != nil {
		if !force {
			return nil
		}
		keep := 0
		if rule.keep {
			keep = valueAt
		}
		out = append(r.out[:0], r.pending[min(r.shown, keep):keep]...)
		if r.shown > keep {
			out = out[:0] // a token began inside what was shown: the marker follows it
		}
		out = append(out, RedactMarker...)
		r.inToken = rule.class
		r.settleAll()
	} else {
		// No value in it: it redacts to itself (checked, so a tail this misjudged is settled
		// redacted instead of shown).
		out, _ = r.process(r.pending, true)
		if r.inToken == nil && !r.inPEM && bytes.Equal(out, r.pending) {
			out = out[r.shown:]
			r.shown = len(r.pending)
		} else {
			out = r.skipShown(out, r.pending, len(r.pending))
			r.settleAll()
		}
	}
	if len(out) > 0 {
		if _, err := r.dst.Write(out); err != nil {
			r.err = err
			return err
		}
	}
	return nil
}

// settleAll counts everything held back as consumed (Flush).
func (r *Redactor) settleAll() {
	r.prev = append(r.prev, r.pending[max(0, len(r.pending)-escapeContext):]...)
	r.prev = r.prev[:copy(r.prev, r.prev[max(0, len(r.prev)-escapeContext):])]
	r.pending, r.shown = r.pending[:0], 0
}

// heldValue: when a held tail starts with a rule's whole prefix (and glue) and at least one
// character of its value, the rule and where the value starts; else nil (no value held yet).
func heldValue(b []byte) (*tokenRule, int) {
	for k := range tokenRules {
		ru := &tokenRules[k]
		for _, pre := range ru.prefixes {
			if !bytes.HasPrefix(b, []byte(pre)) {
				continue
			}
			at := len(pre)
			if ru.glue != nil {
				g, _ := ru.glue(b[at:])
				if g < 0 {
					return nil, 0 // the glue is still arriving (or isn't one): no value yet
				}
				at += g
			}
			if at < len(b) && ru.class(b[at]) {
				return ru, at
			}
			return nil, 0
		}
	}
	return nil, 0
}

// Close redacts and emits what was held back; a private key still open is dropped.
func (r *Redactor) Close() error {
	if r.err != nil {
		return r.err
	}
	out, _ := r.process(r.pending, true)
	out = r.skipShown(out, r.pending, len(r.pending))
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
			end, found, hold, esc := pemClose(data[i:], final, r.pemEsc)
			if !found {
				if final {
					return out, len(data)
				}
				r.pemEsc = esc
				return out, len(data) - hold // the key so far is dropped; a possible END start is held
			}
			r.inPEM, r.pemEsc = false, false
			i += end
			continue
		}
		start, rule := st.next(i)
		if start < 0 {
			if final {
				return append(out, data[i:]...), len(data)
			}
			cut := len(data) - st.partialTail(i)
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
				r.inPEM, r.pemEsc = true, false
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

// escapeContext is how many bytes before a token the boundary rule looks at: a \uXXXX
// escape, or a terminal escape code ending right before it (a 24-bit colour is ~20 bytes).
const escapeContext = 40

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
	add("earer", 1, &tokenRules[5])
	add("EARER", 1, &tokenRules[5])
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
	return s.escapeEndsBefore(at)
}

// escapeEndsBefore: the bytes right before data[at] end a terminal escape code (a CSI: ESC '['
// parameters intermediates final, raw or as JSON writes it, "\u001b[1;32m"), so in the text
// a token starting there starts a word (#3562: coloured output is the normal case).
func (s *starts) escapeEndsBefore(at int) bool {
	c, ok := s.byteBefore(at, 1)
	if !ok || c < 0x40 || c > 0x7e {
		return false
	}
	k := 2
	for ; k <= escapeContext; k++ {
		if c, ok = s.byteBefore(at, k); !ok || c < 0x20 || c > 0x3f {
			break
		}
	}
	if !ok || c != '[' {
		return false
	}
	if e, _ := s.byteBefore(at, k+1); e == 0x1b {
		return true
	}
	const esc = "\\u001b"
	for j := 0; j < len(esc); j++ {
		e, ok := s.byteBefore(at, k+len(esc)-j)
		if !ok || e|0x20 != esc[j]|0x20 {
			return false
		}
	}
	return true
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
	if ru.params {
		// Digest (and any scheme with parameters): username="bob", nonce="...", response="..."
		// is one credential, redacted to the list's end (#3731).
		switch n, k := paramList(b[at:], final); {
		case n == -2:
			return 0, 0, false, true, false
		case n > 0 && k >= 2 || n > 0 && bytes.IndexByte(b[at:at+n], '"') >= 0:
			return at + n, at, true, false, false
		}
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
		case q > 0:
			form := b[k : k+q]
			end := bytes.Index(b[k+q:], form)
			if end < 0 {
				if len(b) >= maxParamList {
					return maxParamList, pairs + 1
				}
				return short(true)
			}
			k += q + end + q
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
// marker, or at (not after) the first unescaped '"', the end of the JSON string holding it.
// Inside a JSON string a quote is written \" (#3694): that '"' is part of the key's text, and
// stopping at it dropped the backslash and closed the string early, which left the rest of the
// string as bare JSON. esc says whether the byte dropped just before b was an unpaired
// backslash (a chunk boundary can fall between a backslash and what it escapes). When the end
// isn't found, hold is how many trailing bytes might begin the END marker, and escAfter is the
// escape state after the bytes consumed (all but hold).
func pemClose(b []byte, final, esc bool) (end int, found bool, hold int, escAfter bool) {
	q := unescapedQuote(b, esc)
	for e := bytes.Index(b, pemEnd); e >= 0 && (q < 0 || e < q); {
		rest := b[e+len(pemEnd):]
		if k := bytes.Index(rest[:min(len(rest), maxPEMLabel)], pemDashes); k >= 0 {
			return e + len(pemEnd) + k + len(pemDashes), true, 0, false
		}
		if len(rest) < maxPEMLabel && q < 0 && !final {
			return 0, false, len(b) - e, escapeAfter(b[:e], esc) // the END line may finish in the next chunk
		}
		next := bytes.Index(b[e+1:], pemEnd)
		if next < 0 {
			break
		}
		e += 1 + next
	}
	if q >= 0 {
		return q, true, 0, false
	}
	hold = partialSuffix(b, pemEnd)
	return 0, false, hold, escapeAfter(b[:len(b)-hold], esc)
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

// partialTail is how many trailing bytes of data (from i on) might begin a prefix or a PEM
// BEGIN, and so are held back. A prefix's first letters are held only where its boundary
// holds (Security's #3562 SHOULD 1): letters in the middle of a word can't start a token, so
// they go out at once, which keeps a terminal's echo prompt.
func (s *starts) partialTail(i int) int {
	b := s.data[i:]
	n := partialSuffix(b, pemBegin)
	for k := range tokenRules {
		ru := &tokenRules[k]
		for _, pre := range ru.prefixes {
			for m := min(len(pre)-1, len(b)); m > n; m-- {
				if bytes.HasSuffix(b, []byte(pre[:m])) && s.boundaryOK(len(s.data)-m, ru) {
					n = m
					break
				}
			}
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
