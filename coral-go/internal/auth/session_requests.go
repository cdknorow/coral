package auth

import (
	"context"
	"net/http"
	"sync"
)

// Ending a browser session closes what it has open (#3966 (c)): every request that carries a
// live session cookie runs with a context that ends when that session does, so a sign-out of
// other browsers, a password change or reset, a Settings sign-out or an ended Coral Hub sign-in
// also closes those browsers' open WebSockets (the terminal, the live feed), not only their next
// request. The WebSocket handlers already stop when their request context does.

type liveReq struct{ cancel context.CancelFunc }

// liveRequests are the requests in flight for each session, by its id hash.
type liveRequests struct {
	mu sync.Mutex
	m  map[string]map[*liveReq]struct{}
}

func (l *liveRequests) add(id string, lr *liveReq) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]map[*liveReq]struct{}{}
	}
	if l.m[id] == nil {
		l.m[id] = map[*liveReq]struct{}{}
	}
	l.m[id][lr] = struct{}{}
}

func (l *liveRequests) remove(id string, lr *liveReq) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m[id], lr)
	if len(l.m[id]) == 0 {
		delete(l.m, id)
	}
}

// close ends the requests of the sessions keep doesn't name: one session (only), or every
// session but keep (except).
func (l *liveRequests) close(only, except string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, reqs := range l.m {
		if (only != "" && id != only) || (only == "" && id == except) {
			continue
		}
		for lr := range reqs {
			lr.cancel()
		}
	}
}

// SessionRequests runs after Middleware, on every route: a request that carries a live browser
// session (any method: password, link, key form, Coral Hub) gets a context that ends when the
// session ends, whether it is ended or reaches its own expiry.
func (ks *KeyStore) SessionRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := ks.extractSessionCookie(r)
		if t == "" {
			next.ServeHTTP(w, r)
			return
		}
		s, ok := ks.lookupSession(t)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithDeadline(r.Context(), s.ExpiresAt) // expiry closes it too
		lr := &liveReq{cancel: cancel}
		ks.live.add(s.IDHash, lr)
		defer func() {
			cancel()
			ks.live.remove(s.IDHash, lr)
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// endSession deletes one session and closes its requests.
func (ks *KeyStore) endSession(ctx context.Context, idHash string) error {
	err := ks.backend().DeleteSession(ctx, idHash)
	ks.live.close(idHash, "")
	return err
}
