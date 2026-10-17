package routes

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store"
)

func strconvQuote(s string) string { return strconv.Quote(strings.TrimSpace(s)) }

// identityFields are the request fields, in a JSON body or the query string, that
// name a board participant (#3376).
var identityFields = []string{"subscriber_id", "session_id", "job_title", "assignee", "as", "from", "sender", "target", "created_by"}

// selfIdentityFields are the fields by which a request says who it is from.
var selfIdentityFields = map[string]bool{"subscriber_id": true, "session_id": true, "job_title": true, "as": true, "from": true, "sender": true, "created_by": true}

// isAgentCaller reports whether the request comes from an agent's tool: it carries a session
// token, or it is the agent command line without a signature the server has checked (the
// Operator's own command line is signed, and the identity layer marks it verified).
func isAgentCaller(r *http.Request) bool {
	if r.Header.Get(store.SessionTokenHeader) != "" {
		return true
	}
	return isAgentTool(r) && !cliauth.Verified(r)
}

// RejectBadIdentities refuses a board or agent-task request whose identity fields
// carry control or formatting characters (board.ValidateIdentity): such a name is
// typed into other agents' terminals in notifications (#3376, SEC-BD-01). Handlers
// decode their own bodies, so the check is here, once, for every one of them.
func RejectBadIdentities(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/board/") && !strings.HasPrefix(r.URL.Path, "/api/agent/") {
			next.ServeHTTP(w, r)
			return
		}
		q := r.URL.Query()
		for _, f := range identityFields {
			for _, v := range q[f] {
				if err := board.ValidateIdentity(v); err != nil {
					errBadRequest(w, f+" "+err.Error())
					return
				}
			}
		}
		if r.Body != nil && r.Body != http.NoBody && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			raw, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil {
				errBadRequest(w, "could not read the request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var body map[string]json.RawMessage
			if json.Unmarshal(raw, &body) == nil {
				for _, f := range identityFields {
					v, ok := body[f]
					if !ok {
						continue
					}
					var s string
					if json.Unmarshal(v, &s) != nil {
						continue // not a string: the handler decides
					}
					if err := board.ValidateIdentity(s); err != nil {
						errBadRequest(w, f+" "+err.Error())
						return
					}
					if board.IsReservedName(s) {
						errBadRequest(w, f+" "+strconvQuote(s)+" is reserved for Coral's own messages")
						return
					}
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RejectOperatorImpersonation refuses an agent's board or agent-task write that says it is from
// the Operator, in any spelling normalizeName reads as the same name (case, spacing, invisible
// characters, look-alike letters) (#4207). It runs after the identity layer, so a request an agent
// token's identity binding already refused (403 identity_mismatch) is answered by that. It asks
// only agents (a session token, or the agent command line without a signature the server checked):
// the dashboard, the signed command line and an API-key script hold a credential of their own, and
// what they write for the Operator is theirs to spell (#4207's first version refused a lower-case
// "operator" from the signed command line: the signature decides for them, not the spelling). A
// request through Coral Hub is left to the tunnel's own identity guard (TunnelIdentityGuard).
func RejectOperatorImpersonation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (!strings.HasPrefix(r.URL.Path, "/api/board/") && !strings.HasPrefix(r.URL.Path, "/api/agent/")) || cloud.Tunneled(r) || !isAgentCaller(r) ||
			r.Body == nil || r.Body == http.NoBody || (r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch) {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			errBadRequest(w, "could not read the request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) == nil {
			for f := range selfIdentityFields {
				v, ok := body[f]
				if !ok {
					continue
				}
				var s string
				if json.Unmarshal(v, &s) != nil {
					continue
				}
				// Fields that only name someone (assignee, target) are not asked.
				if board.IsOperatorName(s) {
					errBadRequest(w, f+" "+strconvQuote(s)+" is reserved for the Operator")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
