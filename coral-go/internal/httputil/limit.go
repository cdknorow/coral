package httputil

import (
	"net/http"
	"strconv"
)

// ActionLimitHeader marks a 429 that limits ONE action (a sixth workflow run, a cross-team
// post too soon, a wrong key tried too often), not a server that is overloaded (#3434). The
// dashboard's overload watch pauses every poller on a 429 or 503; on a marked one it doesn't,
// and shows the server's own message instead.
const ActionLimitHeader = "X-Coral-Limit"

// WriteActionLimit answers 429 for a per-action limit: the marker, Retry-After (seconds,
// when > 0), and {"error": msg, "code": code}.
func WriteActionLimit(w http.ResponseWriter, retryAfterSeconds int, code, msg string) {
	w.Header().Set(ActionLimitHeader, "action")
	if retryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	}
	WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": msg, "code": code})
}
