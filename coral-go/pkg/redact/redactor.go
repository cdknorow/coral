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
	// NewStream redacts a stream written to dst.
	NewStream(dst io.Writer) Stream
}

// A Stream redacts what is written to it and writes the result on. Close ends it; Flush settles
// what it holds when the stream has gone quiet (force: hold nothing back); Holding reports
// whether it holds bytes a Flush would settle.
type Stream interface {
	io.WriteCloser
	Flush(force bool) error
	Holding() bool
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

// safeWriter turns a panic in the Writer into ErrUnchecked.
type safeWriter struct{ w *Writer }

func (s *safeWriter) Write(p []byte) (n int, err error) {
	defer recoverUnchecked(&err)
	return s.w.Write(p)
}

func (s *safeWriter) Flush(force bool) (err error) {
	defer recoverUnchecked(&err)
	return s.w.Flush(force)
}

func (s *safeWriter) Close() (err error) {
	defer recoverUnchecked(&err)
	return s.w.Close()
}

func (s *safeWriter) Holding() bool { return s.w.Holding() }

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

type refuseStream struct{}

func (refuseStream) Write([]byte) (int, error) { return 0, ErrUnchecked }
func (refuseStream) Flush(bool) error          { return ErrUnchecked }
func (refuseStream) Close() error              { return ErrUnchecked }
func (refuseStream) Holding() bool             { return false }
