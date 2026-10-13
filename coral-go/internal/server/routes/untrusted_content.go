package routes

import (
	"mime"
	"net/http"
	"strings"

	"github.com/cdknorow/coral/pkg/hubproxy"
)

// setUntrustedContentHeaders prepares a response whose bytes an agent, a repo
// or a remote controls (an uploaded artifact, a workflow step file), served
// from the dashboard's own origin.
//
// Whatever its type, the response is sandboxed (an opaque origin with no
// script) and never sniffed, and anything that is not inert media or plain data
// (HTML, SVG, XML, JavaScript, PDF, unknown types) downloads instead of opening
// when someone follows its link. The real media type is kept: the preview pane
// fetches these URLs and picks its renderer from it, and fetch and <img>
// ignore Content-Disposition.
func setUntrustedContentHeaders(h http.Header, contentType, filename string) {
	mt, params, err := mime.ParseMediaType(contentType)
	mt = strings.ToLower(mt)
	known := err == nil && mt != ""
	if !known {
		mt, params = "application/octet-stream", nil
	}
	if ct := mime.FormatMediaType(mt, params); ct != "" {
		h.Set("Content-Type", ct)
	} else {
		h.Set("Content-Type", mt)
	}
	if known && hubproxy.InlineSafe(mt) {
		h.Set("Content-Disposition", hubproxy.Disposition("inline", filename))
	} else {
		h.Set("Content-Disposition", hubproxy.Disposition("attachment", filename))
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", hubproxy.SandboxCSP)
}

// artifactURL is the dashboard path for a managed artifact. The ?v=2 form is new in v1.4.12:
// before it the bare path was served inline without a sandbox and cached for a year, so links
// handed out from now on never match such a cache entry. The bare path still works.
func artifactURL(digest string) string { return "/api/artifacts/" + digest + "?v=2" }
