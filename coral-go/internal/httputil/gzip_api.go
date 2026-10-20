package httputil

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
)

// GzipAPIMinBytes is the smallest /api/ JSON reply GzipAPI compresses: below about 1 KB the
// gzip header and the CPU outweigh the saving (#3724: the board's 16 KB message page went out
// plain, since only some handlers gzipped and only from 8 KB).
const GzipAPIMinBytes = 1 << 10

// GzipAPI gzips /api/ JSON replies of at least GzipAPIMinBytes for a client that accepts gzip.
// Everything else passes as it is: other paths (static assets have their own gzip), WebSocket
// upgrades, HEAD and Range requests, replies that set their own Content-Encoding (the handlers
// that already gzip) or Content-Range, replies that aren't JSON (event streams, downloads,
// images), 204 and 304, and a reply flushed before it reaches the threshold (a stream). Through
// Coral Hub nothing changes: the tunnel never asks the server for gzip (pkg/tunnel/local.go), so
// the hub never relays a compressed body.
func GzipAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.Method == http.MethodHead ||
			r.Header.Get("Upgrade") != "" || r.Header.Get("Range") != "" || !AcceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipAPIWriter{ResponseWriter: w}
		defer func() {
			// A handler that panicked sends nothing it held: the panic goes on to the Recoverer,
			// whose 500 then reaches the client instead of a cut-short 200 (Security on #3724).
			if p := recover(); p != nil {
				panic(p)
			}
			gw.finish()
		}()
		next.ServeHTTP(gw, r)
	})
}

type gzipState int

const (
	gzUndecided gzipState = iota // buffering a JSON reply until it reaches the threshold
	gzPlain                      // passing through
	gzOn                         // compressing
)

// gzipWriters keeps up to gzipIdleWriters gzip writers between replies. Each new one allocates its
// compressor's tables (814 KB at BestSpeed). A sync.Pool (#4028) is emptied at every garbage
// collection, so on a busy server it was refilled a few times per cycle: about 19% of all allocated
// bytes (#4446). A small free list survives collections; past it a writer is made and dropped as
// before. At most gzipIdleWriters x 814 KB stays allocated while idle.
const gzipIdleWriters = 4

var gzipWriters = make(chan *gzip.Writer, gzipIdleWriters)

func getGzipWriter() *gzip.Writer {
	select {
	case zw := <-gzipWriters:
		return zw
	default:
		zw, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return zw
	}
}

// putGzipWriter keeps zw for the next reply, unless the free list is full.
func putGzipWriter(zw *gzip.Writer) {
	zw.Reset(io.Discard) // drop the reference to the reply it wrote
	select {
	case gzipWriters <- zw:
	default:
	}
}

// jsonBufs keeps the buffers a reply's JSON is encoded into between replies (#5349). A 70 KB answer grew its buffer by doubling every time: about 300 KB
// of garbage per call on the live list, which the dashboard asks for every few seconds. The buffer is only read before the handler returns (hashed,
// gzipped or copied to the connection), so it is safe to reuse; a big one is dropped rather than kept (jsonBufKeep).
var jsonBufs = make(chan *bytes.Buffer, 8)

// jsonBufKeep is the largest buffer kept for reuse: a rare huge reply (a long transcript) does not pin its memory.
const jsonBufKeep = 1 << 20

func getJSONBuf() *bytes.Buffer {
	select {
	case b := <-jsonBufs:
		return b
	default:
		return new(bytes.Buffer)
	}
}

func putJSONBuf(b *bytes.Buffer) {
	if b.Cap() > jsonBufKeep {
		return
	}
	b.Reset()
	select {
	case jsonBufs <- b:
	default:
	}
}

// gzipBody compresses b with a writer from the shared free list (#5349). WriteJSONCompressed and WriteJSONValidated each made a new gzip writer
// per reply, 814 KB of compressor tables every time: on the live list (70 KB of JSON) that was about 800 KB of the 1.9 MB it allocated per call.
func gzipBody(b []byte) *bytes.Buffer {
	var gz bytes.Buffer
	gz.Grow(len(b)/4 + 64)
	zw := getGzipWriter()
	zw.Reset(&gz)
	_, _ = zw.Write(b)
	_ = zw.Close()
	putGzipWriter(zw)
	return &gz
}

type gzipAPIWriter struct {
	http.ResponseWriter
	state     gzipState
	status    int // the status the handler set (0: none yet)
	headerOut bool
	buf       bytes.Buffer
	zw        *gzip.Writer
	// writeFailed: a write of this reply's compressed bytes failed, so its writer isn't reused.
	writeFailed bool
}

func (g *gzipAPIWriter) WriteHeader(code int) {
	if g.headerOut || g.status != 0 {
		return
	}
	if code < 200 { // an informational reply goes out at once and doesn't end the response
		g.ResponseWriter.WriteHeader(code)
		return
	}
	g.status = code
}

// eligible reports whether the reply, as its headers stand, may be compressed.
func (g *gzipAPIWriter) eligible() bool {
	h := g.Header()
	if g.status == http.StatusNoContent || g.status == http.StatusNotModified ||
		h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(h.Get("Content-Type"))
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

func (g *gzipAPIWriter) sendHeader() {
	if g.headerOut {
		return
	}
	g.headerOut = true
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.ResponseWriter.WriteHeader(g.status)
}

// plain sends what was held and passes the rest through.
func (g *gzipAPIWriter) plain() error {
	g.state = gzPlain
	g.sendHeader()
	if g.buf.Len() == 0 {
		return nil
	}
	_, err := g.ResponseWriter.Write(g.buf.Bytes())
	g.buf.Reset()
	return err
}

// compress starts the gzip reply with what was held.
func (g *gzipAPIWriter) compress() error {
	g.state = gzOn
	h := g.Header()
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	h.Add("Vary", "Accept-Encoding")
	g.sendHeader()
	g.zw = getGzipWriter()
	g.zw.Reset(g.ResponseWriter)
	_, err := g.zw.Write(g.buf.Bytes())
	g.buf.Reset()
	g.writeFailed = g.writeFailed || err != nil
	return err
}

func (g *gzipAPIWriter) Write(p []byte) (int, error) {
	switch g.state {
	case gzPlain:
		return g.ResponseWriter.Write(p)
	case gzOn:
		n, err := g.zw.Write(p)
		g.writeFailed = g.writeFailed || err != nil
		return n, err
	}
	if g.status == 0 {
		g.status = http.StatusOK
	}
	if !g.eligible() {
		if err := g.plain(); err != nil {
			return 0, err
		}
		return g.ResponseWriter.Write(p)
	}
	g.buf.Write(p)
	if g.buf.Len() >= GzipAPIMinBytes {
		if err := g.compress(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush: a reply flushed before it reached the threshold is a stream, sent plain from then on.
func (g *gzipAPIWriter) Flush() {
	switch g.state {
	case gzUndecided:
		_ = g.plain()
	case gzOn:
		if g.zw.Flush() != nil {
			g.writeFailed = true
		}
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish ends the reply: a held one goes out plain (it stayed under the threshold), a gzip one
// is closed. Its writer is kept for the next reply only after a whole reply: one that failed to
// write (the client went away mid-reply) is dropped, and a reply whose handler panicked never gets
// here (GzipAPI re-panics before finish).
func (g *gzipAPIWriter) finish() {
	switch g.state {
	case gzUndecided:
		if g.status != 0 || g.buf.Len() > 0 {
			_ = g.plain()
		}
	case gzOn:
		if err := g.zw.Close(); err == nil && !g.writeFailed {
			putGzipWriter(g.zw)
		}
		g.zw = nil
	}
}

func (g *gzipAPIWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := g.ResponseWriter.(http.Hijacker); ok && g.state == gzUndecided && !g.headerOut {
		g.state = gzPlain
		return h.Hijack()
	}
	return nil, nil, errors.New("gzip: the response can't be hijacked")
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (g *gzipAPIWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
