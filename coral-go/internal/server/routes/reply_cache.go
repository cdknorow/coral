package routes

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// replyCache computes each key at most once at a time: callers that arrive while it is computed
// wait for it and get its reply (#3721, a board's status: three requests per sidebar render, 89 ms
// each). With a ttl above 0 a 200 is also kept that long; with 0 nothing is kept, so no reply is
// older than the computation it shares. A waiter whose flight didn't end in a 200 computes its own.
type replyCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]cachedReplyEntry
	flights map[string]*replyFlight
	now     func() time.Time
}

type cachedReplyEntry struct {
	header http.Header
	body   []byte
	status int
	at     time.Time
}

type replyFlight struct {
	done  chan struct{}
	reply cachedReplyEntry
}

func newReplyCache(ttl time.Duration) *replyCache {
	return &replyCache{ttl: ttl, entries: map[string]cachedReplyEntry{}, flights: map[string]*replyFlight{}, now: time.Now}
}

// serve answers r for key: a kept reply, the reply of the computation in flight, or its own
// computation (compute writes the reply as a handler would).
func (c *replyCache) serve(w http.ResponseWriter, r *http.Request, key string, compute func(http.ResponseWriter)) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.ttl > 0 && c.now().Sub(e.at) < c.ttl {
		c.mu.Unlock()
		writeCachedReply(w, e)
		return
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			if f.reply.status == http.StatusOK {
				writeCachedReply(w, f.reply)
				return
			}
			compute(w) // the shared computation failed (its caller left, say): this one's own
		case <-r.Context().Done():
		}
		return
	}
	f := &replyFlight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	rec := &replyRecorder{header: http.Header{}, status: http.StatusOK}
	compute(rec)
	f.reply = cachedReplyEntry{header: rec.header, body: rec.body.Bytes(), status: rec.status, at: c.now()}
	c.mu.Lock()
	delete(c.flights, key)
	if c.ttl > 0 && rec.status == http.StatusOK {
		c.entries[key] = f.reply
	}
	c.mu.Unlock()
	close(f.done)
	writeCachedReply(w, f.reply)
}

func writeCachedReply(w http.ResponseWriter, e cachedReplyEntry) {
	for k, v := range e.header {
		w.Header()[k] = append([]string(nil), v...)
	}
	if e.status == 0 {
		e.status = http.StatusOK
	}
	w.WriteHeader(e.status)
	_, _ = w.Write(e.body)
}

// replyRecorder is the http.ResponseWriter compute writes to.
type replyRecorder struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (r *replyRecorder) Header() http.Header { return r.header }
func (r *replyRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status, r.wrote = code, true
	}
}
func (r *replyRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.body.Write(b)
}
