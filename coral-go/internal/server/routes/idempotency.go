package routes

// Idempotency-Key for the routes that start something (#5841): launch, launch-team, one agent's resume and wake, and a one-shot task run.
//
// Why: when the tunnel drops after Coral accepted a launch, Coral Hub may answer 503 and its client may send the same request again; without this a second agent or team starts.
// What: a request through Coral Hub that carries an Idempotency-Key (16 to 128 characters of letters, digits, - and _) and the hub user's label (X-Coral-Hub-User) is run once per
// (route, hub user, key). A repeat with the same body while the first still runs is answered 409 request_in_progress; a repeat after the first succeeded gets the first answer again
// (status and body, with the header Idempotent-Replay: true) and starts nothing; the same key with another body is 422 idempotency_key_reuse. The answer of a request that did not
// succeed (any status but 2xx) is not kept, so a retry after a refusal or a failure runs again: nothing was started the first time.
//
// Whose: never across users. The key is scoped to the validated hub user, and a request through the Hub with a key but no valid hub-user label is NOT deduplicated: it runs fresh and the
// key is ignored (there is no shared "hub" caller to fall back to). A request that did not come through Coral Hub (the dashboard, the command line, an agent) is not touched, with or
// without the header, and neither is one with a forged tunnel stamp. The records live in memory only, for 10 minutes, at most 200 per hub user and 2000 in all.

import (
	"bytes"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

const (
	idempotencyTTL = 10 * time.Minute
	// idempotencyRunningMax bounds a record that is still running: past it the key is free again, so a handler that never returns does not leave every retry on 409 for the
	// whole time to live. The longest a launch route legitimately runs is its lifecycle bound, lifecycleTimeout (2 minutes) plus lifecyclePerSession (5 seconds) for each member of
	// a team: 8 minutes covers a team of 72 members; it is under the time to live (a test pins both).
	idempotencyRunningMax = 8 * time.Minute
	idempotencyMaxKeys    = 2000
	idempotencyMaxPerUser = 200
	idempotencyMaxBody    = 1 << 20  // a request body bigger than this is not deduplicated; an answer bigger than this is not kept (a repeat is answered 409 request_already_done)
	idempotencyMaxBytes   = 32 << 20 // all the answers kept: the oldest finished ones go first when it is over (#5855)
)

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

type idemEntry struct {
	user     string
	bodyHash [sha256.Size]byte
	at       time.Time // when it was started, then when it finished
	done     bool
	status   int
	ctype    string
	cache    string
	body     []byte
	tooBig   bool // it succeeded but its answer was too big to keep: a repeat is told so, and starts nothing
}

// Idempotency is the in-memory record of the requests run through Coral Hub with a key.
type Idempotency struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]*idemEntry
	perUser map[string]int
	bytes   int // the size of the answers kept
	now     func() time.Time
}

// NewIdempotency makes an empty record.
func NewIdempotency() *Idempotency {
	return &Idempotency{entries: map[[sha256.Size]byte]*idemEntry{}, perUser: map[string]int{}, now: time.Now}
}

// Middleware is Wrap for the five routes tunnel.ForwardsIdempotencyKey names, and nothing else: one definition of "the routes that take a key" for what the connector and the hub proxy
// forward and what Coral deduplicates. It is mounted after the identity layers, so a request they refused never reaches it.
func (i *Idempotency) Middleware(next http.Handler) http.Handler {
	wrapped := i.Wrap(next.ServeHTTP)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !tunnel.ForwardsIdempotencyKey(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		wrapped(w, r)
	})
}

// hubStampedUser is the Coral Hub user label of a request: exactly one X-Coral-Hub-User value of the stamp's form; ok is false otherwise.
func hubStampedUser(r *http.Request) (string, bool) {
	values := r.Header[http.CanonicalHeaderKey(tunnel.HubUserHeader)]
	if len(values) != 1 || len(values[0]) > 32 || !hubUserRe.MatchString(values[0]) {
		return "", false
	}
	return values[0], true
}

// Wrap is next with the idempotency rules above.
func (i *Idempotency) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keys := r.Header[http.CanonicalHeaderKey(tunnel.IdempotencyKeyHeader)]
		if len(keys) == 0 || !cloud.TunneledAuthentic(r) {
			next(w, r) // no key, or not a request through Coral Hub: as before
			return
		}
		user, ok := hubStampedUser(r)
		if !ok {
			next(w, r) // a Hub request with no valid hub-user label is never deduplicated: the key is ignored
			return
		}
		if len(keys) != 1 || !idempotencyKeyRe.MatchString(keys[0]) {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is given once: 16 to 128 characters of letters, digits, - and _")
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, idempotencyMaxBody+1))
		if err != nil {
			errBadRequest(w, "the request body could not be read")
			return
		}
		if len(raw) > idempotencyMaxBody {
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
			next(w, r) // too big to compare: not deduplicated
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))

		scope := sha256.Sum256([]byte(r.Method + "\x00" + r.URL.Path + "\x00" + user + "\x00" + keys[0]))
		// The query string is part of what the request asks for: the same key with another query is another request (#5855).
		hash := sha256.Sum256(append([]byte(r.URL.RawQuery+"\x00"), raw...))
		e, replay, refuse := i.begin(scope, user, hash)
		switch {
		case refuse == "key_reuse":
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reuse", "this Idempotency-Key was used for a different request: use a new key for a new request")
			return
		case refuse == "in_progress":
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusConflict, "request_in_progress", "a request with this Idempotency-Key is still running: wait for its answer")
			return
		case replay != nil && replay.tooBig:
			slog.Info("[cloud] a repeated Coral Hub request succeeded before and its answer was too big to keep", "hub_user", user, "path", r.URL.Path)
			writeError(w, http.StatusConflict, "request_already_done", "a request with this Idempotency-Key already succeeded and started its work; its answer is too big to send again: look at the list instead of repeating it")
			return
		case replay != nil:
			if replay.ctype != "" {
				w.Header().Set("Content-Type", replay.ctype)
			}
			if replay.cache != "" {
				w.Header().Set("Cache-Control", replay.cache)
			}
			w.Header().Set("Idempotent-Replay", "true")
			slog.Info("[cloud] a repeated Coral Hub request was answered with its first answer", "hub_user", user, "path", r.URL.Path, "status", replay.status)
			w.WriteHeader(replay.status)
			_, _ = w.Write(replay.body)
			return
		}
		// e is ours (in progress): run the handler, keep the answer only when it succeeded.
		finished := false
		defer func() {
			if !finished {
				i.drop(scope, e) // a panic: nothing is kept
			}
		}()
		rec := &idemWriter{ResponseWriter: w}
		next(rec, r)
		finished = true
		i.finish(scope, e, rec)
	}
}

// begin takes the record for scope: a new in-progress one (e), the finished one to replay, or a reason to refuse. When no room can be made, the request runs unrecorded.
func (i *Idempotency) begin(scope [sha256.Size]byte, user string, hash [sha256.Size]byte) (e *idemEntry, replay *idemEntry, refuse string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	now := i.now()
	if old, ok := i.entries[scope]; ok {
		if (old.done && now.Sub(old.at) > idempotencyTTL) || (!old.done && now.Sub(old.at) > idempotencyRunningMax) {
			if !old.done {
				slog.Warn("[cloud] a request with an Idempotency-Key ran longer than any launch route should: its key is free again", "hub_user", user, "ran", now.Sub(old.at).Round(time.Second))
			}
			i.removeLocked(scope, old) // finished and expired, or running for longer than a launch route may: the key is free
		} else if old.bodyHash != hash {
			return nil, nil, "key_reuse"
		} else if !old.done {
			return nil, nil, "in_progress"
		} else {
			return nil, old, ""
		}
	}
	i.purgeLocked(now)
	if i.perUser[user] >= idempotencyMaxPerUser && !i.evictOldestDoneLocked(user) {
		return &idemEntry{}, nil, "" // no room for this user: the request runs, unrecorded (the entry is not in the map)
	}
	if len(i.entries) >= idempotencyMaxKeys && !i.evictOldestDoneLocked("") {
		return &idemEntry{}, nil, ""
	}
	e = &idemEntry{user: user, bodyHash: hash, at: now}
	i.entries[scope] = e
	i.perUser[user]++
	return e, nil, ""
}

func (i *Idempotency) finish(scope [sha256.Size]byte, e *idemEntry, rec *idemWriter) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if cur, ok := i.entries[scope]; !ok || cur != e {
		return // unrecorded
	}
	status := rec.status
	if status < 200 || status > 299 { // 0: the handler returned without answering (its request ended): not a success either
		i.removeLocked(scope, e) // not a success: the key is free, a retry runs again
		return
	}
	e.done, e.status, e.at = true, status, i.now()
	if rec.over {
		e.tooBig = true // it started its work but its answer is not kept: a repeat is refused, never run again
		return
	}
	e.ctype, e.cache = rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control")
	e.body = append([]byte(nil), rec.buf.Bytes()...)
	i.bytes += len(e.body)
	for i.bytes > idempotencyMaxBytes && i.evictOldestDoneLocked("") { // the oldest finished answers go until the kept ones fit
	}
}

func (i *Idempotency) drop(scope [sha256.Size]byte, e *idemEntry) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if cur, ok := i.entries[scope]; ok && cur == e {
		i.removeLocked(scope, e)
	}
}

func (i *Idempotency) removeLocked(scope [sha256.Size]byte, e *idemEntry) {
	delete(i.entries, scope)
	i.bytes -= len(e.body)
	if i.perUser[e.user]--; i.perUser[e.user] <= 0 {
		delete(i.perUser, e.user)
	}
}

// purgeLocked drops the finished records older than the time to live.
func (i *Idempotency) purgeLocked(now time.Time) {
	for s, e := range i.entries {
		if e.done && now.Sub(e.at) > idempotencyTTL {
			i.removeLocked(s, e)
		}
	}
}

// evictOldestDoneLocked drops the oldest finished record (of user, or of anyone when user is ""): false when there is none, so one that is still running is never dropped.
func (i *Idempotency) evictOldestDoneLocked(user string) bool {
	var oldest [sha256.Size]byte
	var oe *idemEntry
	for s, e := range i.entries {
		if !e.done || (user != "" && e.user != user) {
			continue
		}
		if oe == nil || e.at.Before(oe.at) {
			oldest, oe = s, e
		}
	}
	if oe == nil {
		return false
	}
	i.removeLocked(oldest, oe)
	return true
}

// idemWriter passes the handler's answer through and keeps a copy (up to idempotencyMaxBody) for a replay.
type idemWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
	over   bool
}

func (w *idemWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *idemWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if !w.over {
		if w.buf.Len()+len(b) > idempotencyMaxBody {
			w.over = true
		} else {
			w.buf.Write(b)
		}
	}
	return w.ResponseWriter.Write(b)
}

func (w *idemWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
