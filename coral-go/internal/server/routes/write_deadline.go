package routes

import (
	"net/http"
	"strings"
	"time"
)

// plainWriteTimeout bounds each write of a reply on the local listener (#4602). http.Server.WriteTimeout is 0 because websockets and
// event streams must stay open, so a client that asks for a large reply (a chat page, an artifact, a task list) and never reads it
// pinned the handler's goroutine and its buffer until the TCP connection died. The limit applies to a write that cannot make progress,
// never to the silence between events or to a handler that is still computing; a websocket upgrade is left alone (it has its own
// ping and read limits). The Hub path has the same rule at 15 s (pkg/hubproxy).
const plainWriteTimeout = 60 * time.Second

// WriteDeadline is the middleware: it arms a write deadline of plainWriteTimeout before every write and flush of a reply.
func WriteDeadline(next http.Handler) http.Handler {
	return WriteDeadlineAfter(plainWriteTimeout)(next)
}

// WriteDeadlineAfter is WriteDeadline with its own limit: a test makes one without touching a process-wide variable (#6991).
func WriteDeadlineAfter(limit time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(&deadlineWriter{ResponseWriter: w, rc: http.NewResponseController(w), limit: limit}, r)
		})
	}
}

type deadlineWriter struct {
	http.ResponseWriter
	rc    *http.ResponseController
	limit time.Duration
}

func (d *deadlineWriter) arm() { _ = d.rc.SetWriteDeadline(time.Now().Add(d.limit)) }

func (d *deadlineWriter) WriteHeader(code int) {
	d.arm()
	d.ResponseWriter.WriteHeader(code)
}

// writeChunk is the most bytes written between two arming of the deadline: the limit is for each chunk, so a reply written in one
// Write call (a changes.diff, a media preview, a JSON body for a client that does not take gzip) to a client that reads steadily is
// never cut for taking longer than the deadline in all, while one that stops reading is cut after one chunk's deadline.
const writeChunk = 64 << 10

func (d *deadlineWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		d.arm()
		return d.ResponseWriter.Write(p)
	}
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > writeChunk {
			chunk = chunk[:writeChunk]
		}
		d.arm()
		n, err := d.ResponseWriter.Write(chunk)
		written += n
		if err != nil {
			return written, err
		}
		p = p[n:]
	}
	return written, nil
}

func (d *deadlineWriter) Flush() {
	d.arm()
	_ = d.rc.Flush()
}

// Unwrap lets http.ResponseController (and the websocket library) reach the connection's own methods.
func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }
