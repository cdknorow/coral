package redact

import (
	"errors"
	"io"
)

// A Redactor removes secrets with one rule source. Rules is this package's own; a Scanner runs
// any Detector (a library's rules) with the same framing. Every method fails closed: an error
// means the text could not be checked, and the caller must not pass it on as it is.
type Redactor interface {
	// Text redacts text held whole.
	Text(b []byte) ([]byte, error)
	// Reply redacts a reply held whole: by its JSON structure when isJSON and it parses, as
	// text otherwise.
	Reply(raw []byte, isJSON bool) ([]byte, error)
	// Document redacts a document kept at rest (JSON by its structure, anything else as text)
	// and returns raw itself when nothing in it was redacted.
	Document(raw []byte) ([]byte, error)
	// NewStream redacts a stream written to dst: what could still be part of a secret is held
	// until the rest arrives or the stream ends (replies).
	NewStream(dst io.Writer) Stream
	// NewLiveStream redacts a stream written to dst holding nothing: each write is scanned with
	// the text already shown before it and written on at once. A secret that arrives whole in
	// one write is replaced; one split across writes can show its first part (a terminal
	// through Coral Hub, the operator's decision on #3958: echo is never delayed).
	NewLiveStream(dst io.Writer) Stream
}

// A Stream redacts what is written to it and writes the result on. Close ends it.
type Stream interface {
	io.WriteCloser
}

// ErrUnchecked is the error for text that could not be checked: the detector failed, panicked
// or ran out of time.
var ErrUnchecked = errors.New("redact: the text could not be checked for secrets")

// Rules is this package's own rule set as a Redactor: the token shapes, Authorization values, URL
// passwords, values named as secrets and PEM keys, streamed byte by byte (Writer).
var Rules Redactor = rules{}

type rules struct{}

func (rules) Text(b []byte) (out []byte, err error) {
	defer recoverUnchecked(&err)
	return Tokens(b), nil
}

func (rules) Reply(raw []byte, isJSON bool) ([]byte, error) {
	out, ok := Reply(raw, isJSON)
	if !ok {
		return nil, ErrUnchecked
	}
	return out, nil
}

func (rules) Document(raw []byte) (out []byte, err error) {
	defer recoverUnchecked(&err)
	return Document(raw), nil
}

func (rules) NewStream(dst io.Writer) Stream { return &safeWriter{NewWriter(dst)} }

// NewLiveStream: the Writer, settled after every write (Flush with force), so it holds nothing.
func (rules) NewLiveStream(dst io.Writer) Stream { return &liveWriter{safeWriter{NewWriter(dst)}} }

type liveWriter struct{ safeWriter }

func (l *liveWriter) Write(p []byte) (n int, err error) {
	defer recoverUnchecked(&err)
	if n, err = l.w.Write(p); err != nil {
		return n, err
	}
	return n, l.w.Flush(true)
}

// safeWriter turns a panic in the Writer into ErrUnchecked.
type safeWriter struct{ w *Writer }

func (s *safeWriter) Write(p []byte) (n int, err error) {
	defer recoverUnchecked(&err)
	return s.w.Write(p)
}

func (s *safeWriter) Close() (err error) {
	defer recoverUnchecked(&err)
	return s.w.Close()
}

func recoverUnchecked(err *error) {
	if recover() != nil {
		*err = ErrUnchecked
	}
}

// Refuse is a Redactor that checks nothing and passes nothing on: every call is ErrUnchecked.
// It stands in for a rule source that could not be built.
var Refuse Redactor = refuse{}

type refuse struct{}

func (refuse) Text([]byte) ([]byte, error)        { return nil, ErrUnchecked }
func (refuse) Reply([]byte, bool) ([]byte, error) { return nil, ErrUnchecked }
func (refuse) Document([]byte) ([]byte, error)    { return nil, ErrUnchecked }
func (refuse) NewStream(io.Writer) Stream         { return refuseStream{} }
func (refuse) NewLiveStream(io.Writer) Stream     { return refuseStream{} }

type refuseStream struct{}

func (refuseStream) Write([]byte) (int, error) { return 0, ErrUnchecked }
func (refuseStream) Close() error              { return ErrUnchecked }
