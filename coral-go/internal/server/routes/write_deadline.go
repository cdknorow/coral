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
var plainWriteTimeout = 60 * time.Second

// WriteDeadline is the middleware: it arms a write deadline of plainWriteTimeout before every write and flush of a reply.
func WriteDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&deadlineWriter{ResponseWriter: w, rc: http.NewResponseController(w)}, r)
	})
}

type deadlineWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func (d *deadlineWriter) arm() { _ = d.rc.SetWriteDeadline(time.Now().Add(plainWriteTimeout)) }

func (d *deadlineWriter) WriteHeader(code int) {
	d.arm()
	d.ResponseWriter.WriteHeader(code)
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	d.arm()
	return d.ResponseWriter.Write(p)
}

func (d *deadlineWriter) Flush() {
	d.arm()
	_ = d.rc.Flush()
}

// Unwrap lets http.ResponseController (and the websocket library) reach the connection's own methods.
func (d *deadlineWriter) Unwrap() http.ResponseWriter { return d.ResponseWriter }
