package routes

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cdknorow/coral/internal/auth"
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
	// A direct local client only (#3585): a request a local proxy forwarded, and every request
	// while require_auth_on_loopback is on, gets the strict file roots, not the relaxed bounds.
	if !auth.IsDirectLocal(r) {
		return false
	}
	if !localHostOK(r.Host) {
		return false
	}
	return sameOriginFetch(r)
}

// sameOriginFetch reports whether r is a browser fetch from this server's own page.
func sameOriginFetch(r *http.Request) bool {
	// Origin, and failing that Referer, must name this very host whenever the browser
	// sends one, even with Sec-Fetch-Site: same-origin: a mismatch is never this page
	// (QA #3182). The host is compared, not the scheme, so a dashboard behind a
	// TLS-terminating proxy (r.TLS nil, Origin https://…) still counts.
	ownHost := func(raw string) bool {
		u, err := url.Parse(raw)
		return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
	}
	origin, referer := r.Header.Get("Origin"), r.Header.Get("Referer")
	switch {
	case origin != "" && origin != "null":
		if !ownHost(origin) {
			return false
		}
	case origin == "null":
		return false
	case referer != "":
		if !ownHost(referer) {
			return false
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	// Engines without fetch metadata: an Origin or Referer naming this host (checked above).
	return origin != "" || referer != ""
}

// strictBounds is the default; only localDashboard relaxes it.
func strictBounds(r *http.Request) bool { return !localDashboard(r) }

// localHostOK accepts localhost and IP-literal hosts (DNS rebinding defence: a request for
// an external name is never the local dashboard).
func localHostOK(host string) bool {
	return host != "" && auth.AcceptedLocalHost(host) // localhost, loopback or a listened address (#3594)
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

// sensitiveDirs are directory names whose contents are never served.
var sensitiveDirs = map[string]bool{
	// .git holds config with remote URLs that carry tokens, hooks, and logs. The git-aware
	// endpoints (diff, changes.diff, git, files) run git instead of reading these files.
	".git": true,
	".ssh": true, ".aws": true, ".gnupg": true, ".kube": true, ".config": true, ".coral": true,
	".docker": true, ".azure": true, ".gcloud": true, ".password-store": true, ".git-credentials": true,
	"keychains": true, ".mozilla": true, ".chrome": true, ".terraform": true, ".vscode-server": true,
	".terraform.d": true, ".npm": true, ".cargo": true, ".gem": true, ".m2": true, ".gradle": true,
}

// sensitiveSeqs are multi-component locations (lower case) that are never served.
var sensitiveSeqs = [][]string{
	{"library", "application support"}, {"library", "cookies"}, {"library", "safari"}, {"library", "keychains"},
	{"library", "mail"}, {"library", "messages"}, {".local", "share", "keyrings"}, {".local", "share", "gnome-keyring"},
}

// sensitiveNames match the base name (lower case), alone or followed by . - _.
var sensitiveNames = []string{".env", ".netrc", ".npmrc", ".pypirc", ".pgpass", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	"credentials", "secrets", "master.key", "kubeconfig", ".envrc", ".gitconfig", ".git-credentials", ".my.cnf", ".s3cfg"}

// sensitiveExact are base names matched exactly (shell startup files often export tokens).
var sensitiveExact = map[string]bool{
	".bashrc": true, ".zshrc": true, ".profile": true, ".bash_profile": true, ".zprofile": true, ".zshenv": true,
	".bash_login": true, ".lesshst": true, ".viminfo": true, ".wget-hsts": true, "known_hosts": true,
	"authorized_keys": true, "cookies.sqlite": true, "login data": true,
}
var sensitiveSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks", ".kdbx", ".ppk", ".tfstate", "_history", ".history"}

// sensitivePath reports whether p names a credential-bearing or history location. It
// checks every path component, so it also applies inside an otherwise allowed repository.
func sensitivePath(p string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(p)), "/")
	lower := make([]string, len(parts))
	for i, c := range parts {
		lower[i] = strings.ToLower(c)
		if sensitiveDirs[lower[i]] {
			return true
		}
	}
	for _, seq := range sensitiveSeqs {
		for i := 0; i+len(seq) <= len(lower); i++ {
			match := true
			for j, c := range seq {
				if lower[i+j] != c {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	base := lower[len(lower)-1]
	if sensitiveExact[base] {
		return true
	}
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

// rootAllowed rejects a session root that is, or contains, the home directory or the
// filesystem root: a repository rooted there would make everything outside the denylist
// readable (histories, Documents, application data).
func rootAllowed(realRoot string) bool {
	if realRoot == "" || realRoot == string(os.PathSeparator) || filepath.Dir(realRoot) == realRoot {
		return false
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rh := realPath(home); rh != "" && within(realRoot, rh) {
			return false // the root equals or contains the home directory
		}
	}
	return true
}

// listingVisible reports whether a directory entry may be shown to a strict caller.
func listingVisible(dir, name string) bool { return !sensitivePath(filepath.Join(dir, name)) }

// sessionFileRoots are the real directories a tunnel request may read inside: the
// session's git root and working directory.
func (h *SessionsHandler) sessionFileRoots(r *http.Request, name, sessionID string) []string {
	var roots []string
	for _, d := range []string{h.resolveGitRoot(r.Context(), name, "", sessionID), h.resolveWorkdir(r.Context(), name, "", sessionID)} {
		if real := realPath(d); rootAllowed(real) {
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
