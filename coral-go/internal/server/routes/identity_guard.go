package routes

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/board"
)

func strconvQuote(s string) string { return strconv.Quote(strings.TrimSpace(s)) }

// identityFields are the request fields, in a JSON body or the query string, that
// name a board participant (#3376).
var identityFields = []string{"subscriber_id", "session_id", "job_title", "assignee", "as", "from", "sender", "target", "created_by"}

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
