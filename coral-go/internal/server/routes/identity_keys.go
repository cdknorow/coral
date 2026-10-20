package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// identityKeys are the top-level body keys that name a board participant, in their canonical
// spelling (#4233). Every guard that reads a request's claimed name (RejectBadIdentities,
// TunnelIdentityGuard, the identity observer's board check, RejectOperatorImpersonation) looks a
// key up by exactly this spelling, and a test checks that each of their lists is inside this one.
// A handler decodes the same body with encoding/json, which matches a field name with any
// capitalisation ("SUBSCRIBER_ID" fills the field "subscriber_id"), so a key spelled another way
// was seen by the handler and by none of the guards. CanonicalIdentityKeys closes that for every
// guard at once.
var identityKeys = []string{"subscriber_id", "session_id", "session_name", "job_title", "assignee", "assigned_to", "as", "from", "sender", "target", "created_by"}

// canonicalIdentityKey is the canonical spelling of key when key names an identity field in any
// capitalisation, as encoding/json's decoder would match it (strings.EqualFold is the same
// comparison), else "".
func canonicalIdentityKey(key string) string {
	for _, k := range identityKeys {
		if strings.EqualFold(key, k) {
			return k
		}
	}
	return ""
}

// CanonicalIdentityKeys is mounted first among the board's guards (#4233). On a board or agent
// request with a JSON body it renames every identity key to its canonical spelling and, as the
// handler's decoder does, lets the last one win; it also drops anything after the first JSON
// value, which the handlers never read but the guards' whole-body parse fails on (so a guard saw
// no claim at all in {"subscriber_id":"Lead"} junk). The body a guard reads is then the body the
// handler decodes, for every caller. Other keys are left exactly as sent, and a body that needs
// no change is passed on untouched. A body that is not a JSON object is the handler's to refuse.
func CanonicalIdentityKeys(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An artifact upload is the raw file, not a request body about a board.
		if (!strings.HasPrefix(r.URL.Path, "/api/board/") && !strings.HasPrefix(r.URL.Path, "/api/agent/")) ||
			strings.HasPrefix(r.URL.Path, "/api/agent/artifacts") ||
			isRelayedFileRoute(r) || // a file frame (#4453)
			r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		// A body over the cap is refused here, for the agent routes too (nothing else caps them):
		// passed on, it would keep its differently spelled keys from every guard.
		if r.ContentLength > maxBoardBody {
			writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "This board request's body is over the limit")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBoardBody))
		r.Body.Close()
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "body_too_large", "This board request's body is over the limit")
				return
			}
			errBadRequest(w, "could not read the request body")
			return
		}
		if out, changed := canonicalizeIdentityKeys(raw); changed {
			raw = out
			r.ContentLength = int64(len(raw))
			r.Header.Set("Content-Length", strconv.Itoa(len(raw)))
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		next.ServeHTTP(w, r)
	})
}

// canonicalizeIdentityKeys returns raw with its identity keys in canonical form (and nothing
// after the first value), and whether that differs from raw. A body it can't read as one JSON
// object comes back as it is.
func canonicalizeIdentityKeys(raw []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return raw, false
	}
	type pair struct {
		key   string
		value json.RawMessage
	}
	var others []pair
	last := map[string]json.RawMessage{}
	changed := false
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return raw, false
		}
		key, ok := kt.(string)
		if !ok {
			return raw, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return raw, false
		}
		if canon := canonicalIdentityKey(key); canon != "" {
			if key != canon {
				changed = true
			}
			last[canon] = v // document order: the last one wins, whatever its spelling
			continue
		}
		others = append(others, pair{key, v})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return raw, false
	}
	if len(bytes.TrimSpace(raw[dec.InputOffset():])) > 0 {
		changed = true // trailing data: the handler never reads it, a guard's whole-body parse fails on it
	}
	if !changed {
		return raw, false
	}
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	write := func(key string, v json.RawMessage) {
		if !first {
			out.WriteByte(',')
		}
		first = false
		k, _ := json.Marshal(key)
		out.Write(k)
		out.WriteByte(':')
		out.Write(v)
	}
	for _, p := range others {
		write(p.key, p.value)
	}
	for _, k := range identityKeys {
		if v, ok := last[k]; ok {
			write(k, v)
		}
	}
	out.WriteByte('}')
	return out.Bytes(), true
}
