package routes

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// Strict bounds are the DEFAULT for every request: files are served only from inside the
// session's repository, never from the credential denylist, symlinks are resolved before
// any check, and anything that cannot be resolved is refused. Only a request that is
// positively identified as the local dashboard (below) may additionally open an absolute
// path outside the repository, a convenience for paths an agent printed. The denylist
// applies to everyone. Requests from the Cloud Hub connector, the self-hosted hub's
// Bearer-key proxy, curl, and any other proxy or client therefore get the strict rules.

// viaTunnel reports whether the request carries the connector's stamp. The stamp can only
// force strict mode; its absence never grants anything.
func viaTunnel(r *http.Request) bool { return r.Header.Get(tunnel.ViaHeader) != "" }

// localDashboard reports whether r is positively the local dashboard in a browser: it
// comes from a loopback peer, with a localhost Host, as a same-origin browser fetch, with
// no tunnel stamp and no Bearer credential. Every condition must hold.
func localDashboard(r *http.Request) bool {
	if viaTunnel(r) {
		return false
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Authorization"))), "bearer ") {
		return false // a proxy such as the self-hosted hub, not a browser
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(peer); ip == nil || !ip.IsLoopback() {
		return false
	}
	if !localHostOK(r.Host) {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	// Engines without fetch metadata: Origin (or Referer) must be this very origin.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	own := scheme + "://" + r.Host
	if o := r.Header.Get("Origin"); o != "" {
		return o == own
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && u.Scheme+"://"+u.Host == own
	}
	return false
}

// strictBounds is the default; only localDashboard relaxes it.
func strictBounds(r *http.Request) bool { return !localDashboard(r) }

// localHostOK accepts localhost and IP-literal hosts (DNS rebinding defence: a request for
// an external name is never the local dashboard).
func localHostOK(host string) bool {
	if host == "" {
		return false
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return strings.EqualFold(h, "localhost") || net.ParseIP(h) != nil
}

const defaultMaxFileBytes = 5 << 20

// maxFileBytes is the size cap for file reads (CORAL_MAX_FILE_BYTES, default 5 MiB).
func maxFileBytes() int64 {
	if v := os.Getenv("CORAL_MAX_FILE_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxFileBytes
}

// realPath resolves symlinks; it returns "" if the path cannot be resolved.
func realPath(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return r
}

// within reports whether real (already symlink-resolved) is root itself or below it. Both
// sides are compared as cleaned paths with a separator, so /a/b does not contain /a/bc.
func within(root, real string) bool {
	if root == "" || real == "" {
		return false
	}
	root, real = filepath.Clean(root), filepath.Clean(real)
	return real == root || strings.HasPrefix(real, root+string(os.PathSeparator))
}

func withinAny(roots []string, real string) bool {
	for _, r := range roots {
		if within(r, real) {
			return true
		}
	}
	return false
}

// sensitiveDirs are directory names whose contents are never served through the tunnel.
var sensitiveDirs = map[string]bool{
	".ssh": true, ".aws": true, ".gnupg": true, ".kube": true, ".config": true, ".coral": true,
	".docker": true, ".azure": true, ".gcloud": true, ".password-store": true, ".git-credentials": true,
	"keychains": true, ".mozilla": true, ".chrome": true,
}

// sensitiveNames match on the base name (lower case).
var sensitiveNames = []string{".env", ".netrc", ".npmrc", ".pypirc", ".pgpass", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "credentials", "secrets", "master.key"}
var sensitiveSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks", ".kdbx", ".ppk", ".tfstate"}

// sensitivePath reports whether p names a credential-bearing location. It checks every
// path component, so it also applies to files inside an otherwise allowed repository.
func sensitivePath(p string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
	for i, c := range parts {
		lc := strings.ToLower(c)
		if sensitiveDirs[lc] {
			return true
		}
		if lc == "library" && i+1 < len(parts) && strings.EqualFold(parts[i+1], "keychains") {
			return true
		}
	}
	base := strings.ToLower(parts[len(parts)-1])
	for _, n := range sensitiveNames {
		if base == n || strings.HasPrefix(base, n+".") || strings.HasPrefix(base, n+"-") || strings.HasPrefix(base, n+"_") {
			return true
		}
	}
	for _, suf := range sensitiveSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// sessionFileRoots are the real directories a tunnel request may read inside: the
// session's git root and working directory.
func (h *SessionsHandler) sessionFileRoots(r *http.Request, name, sessionID string) []string {
	var roots []string
	for _, d := range []string{h.resolveGitRoot(r.Context(), name, "", sessionID), h.resolveWorkdir(r.Context(), name, "", sessionID)} {
		if real := realPath(d); real != "" {
			roots = append(roots, real)
		}
	}
	return roots
}

// projectRoots are the directories the folder browser may show to a tunnel request: the
// home directory plus CORAL_PROJECT_ROOTS (path-list separated).
func projectRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil {
		if real := realPath(home); real != "" {
			roots = append(roots, real)
		}
	}
	for _, p := range filepath.SplitList(os.Getenv("CORAL_PROJECT_ROOTS")) {
		if real := realPath(p); real != "" {
			roots = append(roots, real)
		}
	}
	return roots
}

// realOrSelf is the resolved path, or p itself when it cannot be resolved.
func realOrSelf(p string) string {
	if r := realPath(p); r != "" {
		return r
	}
	return p
}
