package tunnel

// Which View replies the connector redacts (#3560). Deny by default: every reply to a View
// route whose content type is text is redacted, except the routes listed here, each with its
// reason. The census test (TestViewRedactionCensus) lists every View route with its class, so
// a new View route fails it until someone decides.

import (
	"mime"
	"net/http"
	"strings"
)

// redactExempt: View routes whose replies pass unredacted, and why.
var redactExempt = map[string]string{
	"GET /api/artifacts/{hex64}": "byte-exact download: the id is the SHA-256 of the bytes, so any change breaks the link and its check",
	"GET /api/health":            "fixed fields (status, version): no free text",
}

// redactLater: View routes not redacted yet, each with when; the docs say so. Empty since
// #3562 redacts the two live streams (/ws/coral per frame, /ws/terminal as one stream).
var redactLater = map[string]string{}

// RedactionClass names how the connector treats a View route's replies: "redact",
// "exempt: <reason>" or "later: <reason>". It is "" for a route that isn't View.
func RedactionClass(method, pattern string) string {
	key := method + " " + pattern
	if why, ok := redactExempt[key]; ok {
		return "exempt: " + why
	}
	if why, ok := redactLater[key]; ok {
		return "later: " + why
	}
	return "redact"
}

// redactsReply reports whether the connector redacts the reply to a request: a View route
// that isn't exempt, with a text body.
func redactsReply(method, decodedPath string, respHeader http.Header) bool {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	pattern, tier, ok := RuleFor(method, decodedPath)
	if !ok || tier != TierView || RedactionClass(method, pattern) != "redact" {
		return false
	}
	return textReply(respHeader.Get("Content-Type"))
}

// redactsRoute is redactsReply without the reply: whether a request's reply will be redacted
// when it is text (the connector then drops Range, so no partial body starts mid-token).
func redactsRoute(method, decodedPath string) bool {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	pattern, tier, ok := RuleFor(method, decodedPath)
	return ok && tier == TierView && RedactionClass(method, pattern) == "redact"
}

// textReply: every reply is scanned except the binary families that can't hold a pasted token
// as text (images, audio, video, fonts, PDF, archives). An unknown or missing type, and
// application/octet-stream (a raw file such as a .env may be served that way), are scanned.
func textReply(ct string) bool {
	if ct == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return true // unreadable: scan it rather than trust it
	}
	if mt == "image/svg+xml" {
		return true // text: it can hold anything
	}
	if t, _, _ := strings.Cut(mt, "/"); t == "image" || t == "audio" || t == "video" || t == "font" {
		return false
	}
	switch mt {
	case "application/pdf", "application/zip", "application/gzip", "application/x-gzip",
		"application/x-tar", "application/x-7z-compressed", "application/wasm":
		return false
	}
	return true
}
