package serverproof

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
)

// KeyLookup finds the proof key for a challenge: kind is KindOperator, or KindSession with the SHA-256 of the session token the tool holds. ok is false when Coral has none (an unknown or revoked
// session, a command-line token it could not write), which the tool takes as "no proof".
type KeyLookup func(ctx context.Context, kind, tokenHash string) (key []byte, ok bool)

// Handler answers a challenge. It is public (the tool has no credential to give before it knows who it talks to) and answers only a well-formed challenge; it writes nothing but the proof.
// allow is the rate bound (nil: none), asked per challenge identity: "operator", or "session:<hash>" for a session Coral knows, and "" for every challenge Coral has no key for (one shared
// bucket, so a flood of made-up hashes can only use up its own share and never a real session's). limited answers a challenge that is over it (the server writes its one kind of per-action
// limit there, httputil.WriteActionLimit, which the tools recognise by its marker header).
func Handler(lookup KeyLookup, allow func(identity string) bool, limited func(http.ResponseWriter)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || !wellFormed(q) {
			http.Error(w, "bad challenge", http.StatusBadRequest)
			return
		}
		kind, nonce, tokenHash := q.Get("k"), q.Get("n"), q.Get("h")
		key, ok := lookup(r.Context(), kind, tokenHash)
		identity := ""
		if ok {
			identity = kind
			if kind == KindSession {
				identity += ":" + tokenHash
			}
		}
		if allow != nil && !allow(identity) {
			limited(w)
			return
		}
		if !ok {
			http.Error(w, "no proof", http.StatusNotFound)
			return
		}
		server, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if server == nil {
			http.Error(w, "no proof", http.StatusNotFound)
			return
		}
		serverEP, err1 := Endpoint(server.String())
		clientEP, err2 := Endpoint(r.RemoteAddr)
		if err1 != nil || err2 != nil {
			http.Error(w, "no proof", http.StatusNotFound)
			return
		}
		_, port, _ := net.SplitHostPort(serverEP)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"proof": Proof(key, port, nonce, clientEP, serverEP)})
	})
}

// wellFormed: exactly the parameters a challenge has (n always, k, and h for a session), each well formed.
func wellFormed(q url.Values) bool {
	for name, v := range q {
		if len(v) != 1 || (name != "n" && name != "k" && name != "h") {
			return false
		}
	}
	if !ValidNonce(q.Get("n")) {
		return false
	}
	switch q.Get("k") {
	case KindOperator:
		return !q.Has("h")
	case KindSession:
		return ValidTokenHash(q.Get("h"))
	}
	return false
}
