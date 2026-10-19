package redact

import (
	"bytes"
	"strings"
)

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
	// seek, when set, finds the next start itself (anchor and full unused): the secret names,
	// found from their separator in one pass rather than one search per spelling (#3733).
	seek func(data []byte, from int) int
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
	// Secret names (#3733), after the rules above so Authorization wins a start they share.
	out = append(out, startLit{rule: secretRule, seek: seekSecretName})
	add("://", 0, urlCredRule)
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
	if l.seek != nil {
		l.at = l.seek(s.data, from)
		return
	}
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

// partialTail is how many trailing bytes of data (from i on) might begin a prefix or a PEM
// BEGIN, and so are held back. A prefix's first letters are held only where its boundary
// holds (Security's #3562 SHOULD 1): letters in the middle of a word can't start a token, so
// they go out at once, which keeps a terminal's echo prompt.
func (s *starts) partialTail(i int) int {
	b := s.data[i:]
	n := partialSuffix(b, pemBegin)
	for k := range tokenRules {
		ru := &tokenRules[k]
		if ru == secretRule {
			// The secret names' beginnings are a set: a lookup per tail length, not a search
			// per spelling (#3733).
			for m := min(secretNameLens[0]-1, len(b)); m > n; m-- {
				if secretNameStarts[string(b[len(b)-m:])] && s.boundaryOK(len(s.data)-m, ru) {
					n = m
					break
				}
			}
			continue
		}
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
