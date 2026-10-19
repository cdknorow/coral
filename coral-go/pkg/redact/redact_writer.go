package redact

import (
	"bytes"
	"io"
)

// Writer redacts what is written to it and passes the result on to its destination.
type Writer struct {
	dst     io.Writer
	pending []byte          // held back: it could still start or continue a token
	prev    []byte          // the last few bytes of the original stream before pending (for the boundary)
	inPEM   bool            // inside a private key: dropped until its END line or the closing quote
	pemEsc  bool            // inside a private key: the last byte dropped was an unpaired backslash
	inToken func(byte) bool // inside a token longer than its rule's ceiling: its class is dropped
	qdrop   *quoteDrop      // with inToken: the token is a quoted value, dropped to its closing quote with escapes read (nil: the class alone)
	shown   int             // the leading bytes of pending that Flush already emitted as they are
	err     error
	buf     []byte // pending + the new bytes (reused)
	out     []byte // the redacted output of one Write (reused; dst must not keep it)
	st      starts
}

// NewWriter returns a Writer writing the redacted stream to dst. Close flushes it.
func NewWriter(dst io.Writer) *Writer { return &Writer{dst: dst} }

// Write redacts p (after what was held back) and emits everything that is settled.
func (r *Writer) Write(p []byte) (int, error) {
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
func (r *Writer) skipShown(out, data []byte, consumed int) []byte {
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

// Holding reports whether the stream holds bytes it hasn't shown that a Flush could settle:
// not inside a private key, whose bytes wait for its END line whatever happens.
func (r *Writer) Holding() bool { return !r.inPEM && len(r.pending) > r.shown }

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
func (r *Writer) Flush(force bool) error {
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
		out = append(out, Marker...)
		r.inToken, r.qdrop = rule.class, nil
		if rule.toQuote {
			if oq := openingQuote(r.pending, valueAt); oq != nil {
				r.inToken = notQuoteEnd // a quoted value's rest runs to its quote, spaces and all
				r.qdrop = quoteDropFor(oq)
			}
		}
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
func (r *Writer) settleAll() {
	r.prev = append(r.prev, r.pending[max(0, len(r.pending)-escapeContext):]...)
	r.prev = r.prev[:copy(r.prev, r.prev[max(0, len(r.prev)-escapeContext):])]
	r.pending, r.shown = r.pending[:0], 0
}

// heldValue: when a held tail starts with a rule's whole prefix (and glue) and at least one
// character of its value, the rule and where the value starts; else nil (no value held yet).
func heldValue(b []byte) (*tokenRule, int) {
	for k := range tokenRules {
		ru := &tokenRules[k]
		at := ru.prefixLen(b)
		if at == 0 {
			continue
		}
		if ru.glue != nil {
			g, _ := ru.glue(b[at:])
			if g < 0 {
				return nil, 0 // the glue is still arriving (or isn't one): no value yet
			}
			at += g
		}
		if at < len(b) && (ru.class(b[at]) || ru.toQuote && openingQuote(b, at) != nil) {
			return ru, at // a quoted value's first character may be a space
		}
		return nil, 0
	}
	return nil, 0
}

// Close redacts and emits what was held back; a private key still open is dropped.
func (r *Writer) Close() error {
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

// process redacts data and returns the output and how many bytes of data it settled; the rest
// is held back. With final set, all of data is settled.
func (r *Writer) process(data []byte, final bool) (out []byte, consumed int) {
	out = r.out[:0]
	defer func() { r.out = out[:0] }()
	st := &r.st
	st.reset(data, r.prev)
	i := 0
	for i < len(data) {
		if r.inToken != nil {
			// The rest of an over-long token: dropped up to the first byte outside its class, or
			// to another rule's start, which then redacts what follows. A quoted value cut at its
			// rule's max (PASSWORD="... 500 bytes ... DB_PASSWORD="<pw>") otherwise dropped the
			// next name with the run and let its value out (#3761, found by FuzzTrimCut). A start
			// counts as it does for a value's end in the match below.
			j := i
			emit := 0 // backslashes of a closing quote's spelling that earlier chunks dropped with the run's start
			limit := len(data)
			if !final {
				limit -= st.partialTail(i) // a start may begin in the tail: it is told with more bytes
			}
			ended := false // the drop ends inside data (at j), not with it
			if q := r.qdrop; q != nil {
				j, emit = q.dropEnd(data[:limit], i)
				ended = q.closed
			} else {
				for j < len(data) && r.inToken(data[j]) {
					j++
				}
				ended = j < len(data)
			}
			for k, kr := st.next(i); k >= 0 && k < j; k, kr = st.next(k + 1) {
				if kr == nil { // a PEM BEGIN
					j, emit, ended = k, 0, true
					break
				}
				_, _, kok, kneed, _ := kr.match(data[k:], final)
				if kneed && !final {
					r.qdrop.carryTo(data, i, k)
					return out, k // dropped up to the start; it's told with more bytes
				}
				if kok {
					j, emit, ended = k, 0, true
					break
				}
			}
			if !ended {
				if final {
					r.inToken, r.qdrop = nil, nil
					return out, len(data)
				}
				r.qdrop.carryTo(data, i, limit)
				return out, limit
			}
			if emit > 0 {
				out = append(out, bytes.Repeat([]byte{'\\'}, emit)...)
			}
			r.inToken, r.qdrop = nil, nil
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
				out = append(out, Marker...)
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
			// A value that holds another rule's start ends before it, so that rule redacts what
			// follows: in "token:  Authorization: Basic ..." or "token: DB_PASSWORD=\"...\"" the
			// keyword was taken as token's value and redacted, and the credential after it went
			// out (#3761, found by FuzzRedactStream). A value that would be shorter than the
			// rule's least (or empty) is not this rule's value at all.
			// Only a start whose own rule matches there counts (near the end of the data a start
			// is only possible); when that can't be told yet, the whole match waits for more.
			cut, wait := -1, false
			for k, kr := st.next(start + valueAt); k >= 0 && k < start+n; k, kr = st.next(k + 1) {
				if kr == nil { // a PEM BEGIN
					cut = k
					break
				}
				_, _, kok, kneed, _ := kr.match(data[k:], final)
				if kneed && !final {
					wait = true
					break
				}
				if kok {
					cut = k
					break
				}
			}
			// A start cut off by the end of the data (Authorizatio|n) can't be found yet; one that
			// begins inside the value waits for its bytes too.
			if cut < 0 && !final {
				if p := st.partialTail(start + valueAt); p > 0 && len(data)-p < start+n {
					wait = true
				}
			}
			if wait {
				return out, start
			}
			if cut >= 0 {
				if cut-(start+valueAt) < max(rule.min, 1) {
					out = append(out, data[start])
					i = start + 1
					continue
				}
				n, long = cut-start, false
			}
			if rule.keep {
				out = append(out, data[start:start+valueAt]...)
			}
			out = append(out, Marker...)
			i = start + n
			if long {
				r.inToken, r.qdrop = rule.class, nil // the rest of the run is dropped as it arrives
				if rule.toQuote {
					if oq := openingQuote(data, start+valueAt); oq != nil {
						r.inToken = notQuoteEnd // a quoted value's rest runs to its quote, spaces and all (as when it is held: #5002)
						r.qdrop = quoteDropFor(oq)
					}
				}
			}
		}
	}
	return out, len(data)
}
