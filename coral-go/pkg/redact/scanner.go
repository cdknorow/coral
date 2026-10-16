package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// A Span is the byte range [Start, End) of a scanned text that holds a secret.
type Span struct{ Start, End int }

// A Detector finds secrets in text held whole: the rule source a Scanner runs (a library's
// rules). It must be safe for use by many goroutines at once, must stop when ctx ends, and
// reports an error (never a partial answer) when it could not check the text.
type Detector interface {
	Find(ctx context.Context, text string) ([]Span, error)
}

// A Scanner is a Redactor that runs a Detector and replaces each span it finds with Marker. It
// holds everything that is not detection:
//   - A JSON document is walked string by string, each value scanned together with its key
//     ("key: value"), so a rule that needs the key sees it.
//   - Text is given to the detector at most Chunk bytes at a time: whole lines, or, inside a
//     longer line, overlapping windows that carry Overlap bytes of what came before, so a key
//     and its value are seen together wherever the window ends, and one call's work is bounded.
//   - A stream is cut into units the detector sees whole: lines; a private key from its BEGIN
//     line to its END line; overlapping windows of a line longer than Frame. The units depend on
//     the text only, so any split of the stream into writes redacts the same as the whole. The
//     last Overlap bytes of a line that hasn't ended are never shown before the line ends or
//     Flush is forced (see lineStream.Flush).
//   - Each call has a time budget, past which the text is not passed on.
type Scanner struct {
	Detector Detector
	// Budget bounds the detection for one call (a reply, a document, a stream's unit); 0: no
	// limit. Over budget is ErrUnchecked.
	Budget time.Duration
	// Frame is the window a stream scans a long line in (default DefaultFrame).
	Frame int
	// Chunk is the most text one Detector call is given (default DefaultChunk).
	Chunk int
	// Overlap is how much text before a window's start the next scan sees again, and how much
	// of an unfinished line a stream holds back (default DefaultOverlap).
	Overlap int
	// MaxKey is the longest private-key block held whole (default DefaultMaxKey); a longer one
	// is replaced by Marker without being shown.
	MaxKey int
}

// The framing's defaults.
const (
	DefaultFrame   = 8 << 10
	DefaultChunk   = 16 << 10
	DefaultOverlap = 256
	DefaultMaxKey  = 64 << 10
)

var _ Redactor = (*Scanner)(nil)

func (s *Scanner) frame() int {
	if s.Frame > 0 {
		return s.Frame
	}
	return DefaultFrame
}

func (s *Scanner) chunk() int {
	if s.Chunk > 0 {
		return s.Chunk
	}
	return DefaultChunk
}

func (s *Scanner) overlap() int {
	if s.Overlap > 0 {
		return s.Overlap
	}
	return DefaultOverlap
}

func (s *Scanner) maxKey() int {
	if s.MaxKey > 0 {
		return s.MaxKey
	}
	return DefaultMaxKey
}

// budget is the context one call's detection runs under.
func (s *Scanner) budget() (context.Context, context.CancelFunc) {
	if s.Budget > 0 {
		return context.WithTimeout(context.Background(), s.Budget)
	}
	return context.WithCancel(context.Background())
}

// find is the detector's spans in text: one call, guarded.
func (s *Scanner) find(ctx context.Context, text string) (spans []Span, err error) {
	defer func() {
		if p := recover(); p != nil {
			spans, err = nil, fmt.Errorf("%w: %v", ErrUnchecked, p)
		}
	}()
	spans, err = s.Detector.Find(ctx, text)
	if err == nil {
		err = ctx.Err() // a detector that stopped early has not checked all of it
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnchecked, err)
	}
	return spans, nil
}

// window scans pre+seg and returns seg's first n bytes redacted (n >= upTo: a span that crosses
// upTo is taken whole, so no secret is split between this window and the next). pre is text
// already shown, given again for context: a span that starts in it redacts its part in seg. With
// final, all of seg is taken.
func (s *Scanner) window(ctx context.Context, pre, seg []byte, upTo int, final bool) (out []byte, n int, err error) {
	text := make([]byte, 0, len(pre)+len(seg))
	text = append(append(text, pre...), seg...)
	spans, err := s.find(ctx, string(text))
	if err != nil {
		return nil, 0, err
	}
	n = len(seg)
	if !final {
		n = min(max(upTo, 0), len(seg))
		for grown := true; grown; {
			grown = false
			for _, sp := range spans {
				if a, b := sp.Start-len(pre), sp.End-len(pre); a < n && b > n {
					n, grown = min(b, len(seg)), true
				}
			}
		}
	}
	var keep []Span
	for _, sp := range spans {
		if sp.Start < 0 || sp.End > len(text) || sp.Start >= sp.End {
			return nil, 0, fmt.Errorf("%w: a span [%d, %d) outside the text (%d bytes)", ErrUnchecked, sp.Start, sp.End, len(text))
		}
		a, b := max(sp.Start-len(pre), 0), min(sp.End-len(pre), n)
		if a < b {
			keep = append(keep, Span{a, b})
		}
	}
	r, err := applySpans(string(seg[:n]), keep)
	if err != nil {
		return nil, 0, err
	}
	return []byte(r), n, nil
}

// redactString is text with every span the detector finds in it replaced by Marker, the text
// given to the detector at most Chunk bytes at a time.
func (s *Scanner) redactString(ctx context.Context, text string) (string, error) {
	if text == "" {
		return text, nil
	}
	if len(text) <= s.chunk() && !strings.Contains(text, string(pemBeginText)) {
		spans, err := s.find(ctx, text)
		if err != nil {
			return "", err
		}
		return applySpans(text, spans)
	}
	b := []byte(text)
	var out []byte
	var pre []byte // the end of the window before, when it ended inside a line
	for at := 0; at < len(b); {
		rest := b[at:]
		var r []byte
		var n int
		var err error
		switch e, kind := chunkEnd(rest, s.chunk(), s.maxKey()); kind {
		case chunkDrop: // a private key with no END line, or one over MaxKey: never shown
			r, n = []byte(Marker), e
			if bytes.HasSuffix(rest[:e], []byte("\n")) {
				r = append(r, '\n')
			}
			pre = nil
		case chunkLines:
			r, n, err = s.window(ctx, pre, rest[:e], e, true)
			pre = nil
		default: // inside a line longer than Chunk: an overlapping window
			w := rest[:min(len(rest), s.chunk()+s.overlap())]
			r, n, err = s.window(ctx, pre, w, cutPoint(w, s.chunk(), s.overlap()), false)
			pre = tail(append(append([]byte(nil), pre...), rest[:n]...), s.overlap())
		}
		if err != nil {
			return "", err
		}
		out = append(out, r...)
		at += n
	}
	return string(out), nil
}

// The kinds of chunk chunkEnd finds.
const (
	chunkLines  = iota // whole lines (or the text's end): scanned as they are
	chunkWindow        // a line longer than Chunk: scanned in overlapping windows
	chunkDrop          // a private key that never ends or is over MaxKey: replaced by Marker
)

// chunkEnd is where the next chunk of rest ends and what kind it is: its whole lines within max
// bytes (all of rest when it fits); never ending inside a private key, which is a chunk of its
// own from its BEGIN line through its END line.
func chunkEnd(rest []byte, max, maxKey int) (int, int) {
	e := len(rest)
	if len(rest) > max {
		nl := bytes.LastIndexByte(rest[:max], '\n')
		if nl < 0 {
			if bytes.Contains(rest[:max], pemBeginText) {
				return keyChunk(rest, maxKey)
			}
			return 0, chunkWindow
		}
		e = nl + 1
	}
	b := bytes.Index(rest[:e], pemBeginText)
	switch {
	case b < 0:
		return e, chunkLines
	case lineStart(rest, b) > 0:
		return lineStart(rest, b), chunkLines // up to the key's line; the key is a chunk of its own
	}
	return keyChunk(rest, maxKey) // the first line holds the key's BEGIN
}

// lineStart is the start of the line holding rest[i] (0 when that is the first line).
func lineStart(rest []byte, i int) int {
	if nl := bytes.LastIndexByte(rest[:i], '\n'); nl >= 0 {
		return nl + 1
	}
	return i
}

// keyChunk: rest's first line holds a key's BEGIN; the chunk runs to the end of its END line.
func keyChunk(rest []byte, maxKey int) (int, int) {
	end := bytes.Index(rest, pemEndText)
	if end < 0 {
		return len(rest), chunkDrop
	}
	e := len(rest)
	if nl := bytes.IndexByte(rest[end:], '\n'); nl >= 0 {
		e = end + nl + 1
	}
	if e > maxKey {
		return e, chunkDrop
	}
	return e, chunkLines
}

// cutPoint is where a window of b may end at or before n: after a space, tab, ',', ';' or '}'
// within the last half overlap before n, else n (moved back to a character's start).
func cutPoint(b []byte, n, overlap int) int {
	n = min(n, len(b))
	for p := n; p > 0 && p > n-overlap/2; p-- {
		switch b[p-1] {
		case ' ', '\t', ',', ';', '}':
			return p
		}
	}
	for n > 0 && n < len(b) && b[n]&0xC0 == 0x80 {
		n--
	}
	return n
}

// tail is the last n bytes of b.
func tail(b []byte, n int) []byte {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return b
}

// applySpans replaces each span (merged where they touch or overlap) by Marker.
func applySpans(text string, spans []Span) (string, error) {
	if len(spans) == 0 {
		return text, nil
	}
	sp := append([]Span(nil), spans...)
	sort.Slice(sp, func(i, j int) bool { return sp[i].Start < sp[j].Start })
	var b strings.Builder
	b.Grow(len(text))
	at := 0
	for i := 0; i < len(sp); {
		start, end := sp[i].Start, sp[i].End
		if start < 0 || end > len(text) || start >= end {
			return "", fmt.Errorf("%w: a span [%d, %d) outside the text (%d bytes)", ErrUnchecked, start, end, len(text))
		}
		for i++; i < len(sp) && sp[i].Start <= end; i++ {
			if sp[i].End > len(text) || sp[i].Start >= sp[i].End {
				return "", fmt.Errorf("%w: a span [%d, %d) outside the text (%d bytes)", ErrUnchecked, sp[i].Start, sp[i].End, len(text))
			}
			end = max(end, sp[i].End)
		}
		start = max(start, at)
		b.WriteString(text[at:start])
		b.WriteString(Marker)
		at = end
	}
	b.WriteString(text[at:])
	return b.String(), nil
}

// Text redacts b held whole.
func (s *Scanner) Text(b []byte) ([]byte, error) {
	ctx, cancel := s.budget()
	defer cancel()
	out, err := s.redactString(ctx, string(b))
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// Reply redacts a reply held whole: by its structure when isJSON and it parses (and isn't too
// deep to walk), as text otherwise.
func (s *Scanner) Reply(raw []byte, isJSON bool) ([]byte, error) {
	if isJSON && json.Valid(raw) {
		out, walked, err := s.json(raw)
		if walked || err != nil {
			return out, err
		}
	}
	return s.Text(raw)
}

// Document redacts a document kept at rest: JSON by its structure, anything else as text. With
// nothing redacted, raw itself comes back (the walk re-encodes JSON).
func (s *Scanner) Document(raw []byte) ([]byte, error) {
	out, err := s.Reply(raw, true)
	if err != nil {
		return nil, err
	}
	if bytes.Count(out, []byte(Marker)) == bytes.Count(raw, []byte(Marker)) {
		return raw, nil
	}
	return out, nil
}

// json walks raw (valid JSON) and redacts each string. walked is false for a document that
// can't be walked (too deep): the caller scans it as text.
func (s *Scanner) json(raw []byte) (out []byte, walked bool, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	root, ok := readJSONNode(dec, 0)
	if !ok {
		return nil, false, nil
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false, nil
	}
	ctx, cancel := s.budget()
	defer cancel()
	if err := root.scan(ctx, s, "", false); err != nil {
		return nil, true, err
	}
	var b bytes.Buffer
	b.Grow(len(raw))
	if !root.write(&b) {
		return nil, true, ErrUnchecked
	}
	return b.Bytes(), true, nil
}

// scan redacts n's strings in place with the Scanner's detector. A string value is scanned as
// "key: value" with the key of the member holding it (an array's strings with the key of the
// member holding the array; an object's "value" with its "name" or "key" member's text), so a
// rule that needs the key sees it. A key is scanned on its own. A value whose key text the
// detector changed has no place in what came back: it becomes Marker (fail closed).
func (n *jnode) scan(ctx context.Context, s *Scanner, key string, has bool) error {
	switch n.kind {
	case 's':
		if !has {
			out, err := s.redactString(ctx, n.str)
			n.str = out
			return err
		}
		pre := key + ": "
		out, err := s.redactString(ctx, pre+n.str)
		if err != nil {
			return err
		}
		if v, ok := strings.CutPrefix(out, pre); ok {
			n.str = v
		} else {
			n.str = Marker
		}
	case '[':
		for _, c := range n.vals {
			if err := c.scan(ctx, s, key, has); err != nil {
				return err
			}
		}
	case '{':
		name, hasName := "", false
		for i, k := range n.keys {
			if lk := strings.ToLower(k); (lk == "name" || lk == "key") && n.vals[i].kind == 's' {
				name, hasName = n.vals[i].str, true // read before any redaction of it
				break
			}
		}
		for i, k := range n.keys {
			ctxKey := k
			if hasName && strings.EqualFold(k, "value") {
				ctxKey = name
			}
			if err := n.vals[i].scan(ctx, s, ctxKey, true); err != nil {
				return err
			}
			out, err := s.redactString(ctx, k)
			if err != nil {
				return err
			}
			n.keys[i] = out
		}
	}
	return nil
}

// NewStream redacts a stream written to dst, a unit at a time:
//   - a line, up to and with its '\n', when it is at most Frame+Overlap bytes long;
//   - from a line holding "-----BEGIN " to the end of the line holding "-----END " (a private
//     key, held whole up to MaxKey; a longer one is replaced by Marker unseen);
//   - in a longer line, windows of Frame+Overlap bytes, each shown up to about Frame (never
//     cutting a secret the detector found) and the next scanned with the Overlap bytes before
//     it, so a key and its value are seen together.
//
// The units depend on the text only, so any split into writes gives the same output.
func (s *Scanner) NewStream(dst io.Writer) Stream { return &lineStream{s: s, dst: dst} }

// NewLiveStream is NewStream holding nothing (#3958, the operator's decision): each write is
// scanned whole, with the text shown before it as context (in windows of Frame+Overlap when it
// is longer), and written on at once. A private key is scanned like any other text, so one
// that arrives whole in a write is still found.
// The shown text stays as the next write's context, so a secret completed by a later write is
// replaced from where that write starts; its first part, already shown, is the accepted risk.
func (s *Scanner) NewLiveStream(dst io.Writer) Stream { return &lineStream{s: s, dst: dst, live: true} }

type lineStream struct {
	s     *Scanner
	dst   io.Writer
	held  []byte
	pre   []byte // the last Overlap bytes shown of the line held continues (context)
	inKey bool   // held starts with a key's BEGIN line
	drop  bool   // a key over MaxKey: dropping it up to the end of its END line
	live  bool   // NewLiveStream: nothing is held (no key blocks, no unfinished line)
	err   error
}

var (
	pemBeginText = []byte("-----BEGIN ")
	pemEndText   = []byte("-----END ")
)

func (l *lineStream) Write(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	l.held = append(l.held, p...)
	if err := l.settle(false); err != nil {
		return 0, err
	}
	return len(p), nil
}

// settle emits every whole unit at the start of held; final takes the rest as one unit too.
func (l *lineStream) settle(final bool) error {
	if l.live {
		// A write is scanned whole (a key block in it is found as one), in windows when it is
		// longer than one, and nothing is held.
		frame, ov := l.s.frame(), l.s.overlap()
		for len(l.held) > frame+ov {
			w := l.held[:frame+ov]
			if err := l.emit(len(w), cutPoint(w, frame, ov), false); err != nil {
				return err
			}
		}
		if len(l.held) == 0 {
			return nil
		}
		return l.emitAll()
	}
	for len(l.held) > 0 {
		if l.drop || l.inKey {
			n, ok := l.keyUnit()
			if !ok {
				if !final {
					return nil
				}
				n = len(l.held)
				if !bytes.Contains(l.held, pemEndText) {
					l.drop = true
				}
			}
			if err := l.emitKey(n); err != nil {
				return err
			}
			continue
		}
		frame, ov := l.s.frame(), l.s.overlap()
		nl := bytes.IndexByte(l.held, '\n')
		line := l.held
		if nl >= 0 {
			line = l.held[:nl+1]
		}
		// The unit's kind is read from bytes that have all arrived by the time it is decided.
		if len(l.pre) == 0 && bytes.Contains(line[:min(len(line), frame)], pemBeginText) {
			l.inKey = true
			continue
		}
		switch {
		case nl >= 0 && nl+1 <= frame+ov:
			if err := l.emit(nl+1, 0, true); err != nil {
				return err
			}
		case len(l.held) >= frame+ov:
			w := l.held[:frame+ov]
			if err := l.emit(len(w), cutPoint(w, frame, ov), false); err != nil {
				return err
			}
		case final:
			return l.emit(len(l.held), 0, true)
		default:
			return nil
		}
	}
	return nil
}

// keyUnit is the length of the key block at the start of held, if its END line has arrived. A
// key over MaxKey is dropped as it arrives: only what could begin the END line is kept.
func (l *lineStream) keyUnit() (int, bool) {
	e := bytes.Index(l.held, pemEndText)
	if e < 0 {
		if !l.drop && len(l.held) > l.s.maxKey() {
			l.drop = true
		}
		if keep := len(pemEndText) - 1; l.drop && len(l.held) > keep {
			l.held = append(l.held[:0], l.held[len(l.held)-keep:]...)
		}
		return 0, false
	}
	nl := bytes.IndexByte(l.held[e:], '\n')
	if nl < 0 {
		if l.drop && e > 0 {
			l.held = append(l.held[:0], l.held[e:]...)
		}
		return 0, false
	}
	return e + nl + 1, true
}

func (l *lineStream) emitKey(n int) error {
	var out []byte
	if l.drop {
		out = []byte(Marker)
		if bytes.Contains(l.held[:n], pemEndText) && bytes.HasSuffix(l.held[:n], []byte("\n")) {
			out = append(out, '\n') // the END line's own newline
		}
	} else {
		ctx, cancel := l.s.budget()
		r, err := l.s.redactString(ctx, string(l.held[:n]))
		cancel()
		if err != nil {
			l.err = err
			return err
		}
		out = []byte(r)
	}
	l.held = append(l.held[:0], l.held[n:]...)
	l.inKey, l.drop, l.pre = false, false, nil
	return l.write(out)
}

// emit scans held[:w] with the line's context and shows held up to about upTo (all of held[:w]
// when the unit ends there: final).
func (l *lineStream) emit(w, upTo int, final bool) error {
	ctx, cancel := l.s.budget()
	out, n, err := l.s.window(ctx, l.pre, l.held[:w], upTo, final)
	cancel()
	if err != nil {
		l.err = err
		return err
	}
	shown := l.held[:n]
	if final && bytes.HasSuffix(shown, []byte("\n")) {
		l.pre = nil
	} else {
		l.pre = tail(append(append([]byte(nil), l.pre...), shown...), l.s.overlap())
	}
	l.held = append(l.held[:0], l.held[n:]...)
	return l.write(out)
}

func (l *lineStream) write(out []byte) error {
	if _, err := l.dst.Write(out); err != nil {
		l.err = err
		return err
	}
	return nil
}

// emitAll scans everything held, with the text shown before it, and writes it on (a live
// stream's unfinished line). It stays the next write's context.
func (l *lineStream) emitAll() error {
	ctx, cancel := l.s.budget()
	out, _, err := l.s.window(ctx, l.pre, l.held, len(l.held), true)
	cancel()
	if err != nil {
		l.err = err
		return err
	}
	l.pre = tail(append(append([]byte(nil), l.pre...), l.held...), l.s.overlap())
	l.held = l.held[:0]
	return l.write(out)
}

// Close scans what is left; a key whose END line never came is replaced by Marker.
func (l *lineStream) Close() error {
	if l.err != nil {
		return l.err
	}
	return l.settle(true)
}
