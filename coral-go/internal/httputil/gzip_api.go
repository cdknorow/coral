package httputil

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
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
		defer gw.finish()
		next.ServeHTTP(gw, r)
	})
}

type gzipState int

const (
	gzUndecided gzipState = iota // buffering a JSON reply until it reaches the threshold
	gzPlain                      // passing through
	gzOn                         // compressing
)

type gzipAPIWriter struct {
	http.ResponseWriter
	state     gzipState
	status    int // the status the handler set (0: none yet)
	headerOut bool
	buf       bytes.Buffer
	zw        *gzip.Writer
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
	g.zw, _ = gzip.NewWriterLevel(g.ResponseWriter, gzip.BestSpeed)
	_, err := g.zw.Write(g.buf.Bytes())
	g.buf.Reset()
	return err
}

func (g *gzipAPIWriter) Write(p []byte) (int, error) {
	switch g.state {
	case gzPlain:
		return g.ResponseWriter.Write(p)
	case gzOn:
		return g.zw.Write(p)
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
		_ = g.zw.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// finish ends the reply: a held one goes out plain (it stayed under the threshold), a gzip one
// is closed.
func (g *gzipAPIWriter) finish() {
	switch g.state {
	case gzUndecided:
		if g.status != 0 || g.buf.Len() > 0 {
			_ = g.plain()
		}
	case gzOn:
		_ = g.zw.Close()
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
