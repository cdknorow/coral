package hubproxy

import (
	"errors"
	"mime"
	"net/http"
)

// RefusedHeader is set by Coral's own auth layer on the refusals that are about how the hub
// reaches it, not about the request: "key" (the API key isn't this Coral's) and "host" (the Host
// header isn't one this Coral answers to: its DNS-rebinding check, met through a port forward
// under another name). Anything else a remote answers 401 or 403 is that request's own answer
// (#3649).
const RefusedHeader = "X-Coral-Refused"

// StatusHostRefused is passed to MarkStatus when the remote refused the host name the hub uses
// for it.
const StatusHostRefused = "host_refused"

// ErrHostRefused: the remote refused the Host the hub sent (its DNS-rebinding check).
var ErrHostRefused = errors.New("remote refused the host name the hub uses for it")

// Refusal classifies a remote's 401 or 403. It is ErrRemoteRejected when the remote says its key
// was refused, or for a bare 401 from a Coral older than the header (its auth layer's 401 is
// plain text); ErrHostRefused when it refused the Host; nil for an application answer (a JSON
// 401 such as session_token_required, or any other 403), which is passed on and never marks the
// server. It never reads the body.
func Refusal(resp *http.Response) error {
	if resp == nil {
		return nil
	}
	switch resp.Header.Get(RefusedHeader) {
	case "key":
		return ErrRemoteRejected
	case "host":
		return ErrHostRefused
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return nil
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil && mt == "application/json" {
		return nil // Coral's application 401s are JSON with a code; its auth layer's is not
	}
	return ErrRemoteRejected
}
