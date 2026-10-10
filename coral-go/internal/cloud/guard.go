package cloud

import (
	"net/http"
	"strings"
)

// Tunneled reports whether the request came through the Cloud Hub tunnel.
func Tunneled(r *http.Request) bool { return r.Header.Get(TunnelHeader) != "" }

// fileRefParams are query parameters that name a file. For tunneled requests they
// must be project-relative: the local API reads absolute paths "wherever they are"
// for a user at the keyboard, which a remote web user must not inherit.
var fileRefParams = []string{"filepath", "path", "ref", "file"}

// fileRefRoutes are the allowed read routes that take such a parameter.
var fileRefRoutes = []string{"file-content", "file-original", "resolve-path"}

func unsafeFileRef(v string) bool {
	if v == "" {
		return false
	}
	if v[0] == '/' || v[0] == '~' || strings.ContainsAny(v, "\\\x00") || (len(v) > 1 && v[1] == ':') {
		return true
	}
	for _, seg := range strings.Split(v, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// Guard is the server-side half of the connector's route policy. The connector
// already refuses anything outside the allowlist before it reaches the local API;
// Guard repeats the decision for every request that carries TunnelHeader, so the
// policy holds even if the connector's check were bypassed or had a bug, and it
// additionally refuses absolute file references on the file routes. Requests
// without the header (the local dashboard, API keys, agents) are untouched.
func Guard(allowLaunch func() bool) func(http.Handler) http.Handler {
	allow := Rules(allowLaunch)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !Tunneled(r) {
				next.ServeHTTP(w, r)
				return
			}
			if !allow(r.Method, r.URL.Path) {
				deny(w, "not available through the cloud hub")
				return
			}
			if Classify(r.Method, r.URL.Path) != ClassLaunch && isFileRefRoute(r.URL.Path) {
				q := r.URL.Query()
				for _, p := range fileRefParams {
					for _, v := range q[p] {
						if unsafeFileRef(v) {
							deny(w, "absolute and parent paths are not available through the cloud hub")
							return
						}
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isFileRefRoute(p string) bool {
	for _, s := range fileRefRoutes {
		if strings.HasSuffix(p, "/"+s) {
			return true
		}
	}
	return false
}

func deny(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}
