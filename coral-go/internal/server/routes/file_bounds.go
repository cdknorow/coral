package routes

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/gitutil"
	"github.com/cdknorow/coral/internal/safefs"
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
	r, err := safefs.Resolve(p)
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
	// #4531 (Security's #4527 INFO 1), the audit of the list against the credential folders common on macOS, Linux and
	// Windows. More cloud and platform CLIs (each keeps tokens or keys in its folder), build tools' credential folders
	// (.ivy2 and .sbt, .nuget, .composer, .bundle, .cabal, .stack), a package index's and a model hub's tokens (.kaggle,
	// .huggingface), password managers and mail (.1password, .op, .bitwarden, .lastpass, .thunderbird, .mutt), Subversion's
	// stored passwords, the Linux wallets and flatpak's app data, and the zsh session history macOS keeps.
	".oci": true, ".ibmcloud": true, ".bluemix": true, ".aliyun": true, ".alibabacloud": true, ".vault": true, ".minikube": true,
	".pulumi": true, ".serverless": true, ".helm": true, ".cloudflared": true, ".wrangler": true, ".fly": true, ".heroku": true,
	".cf": true, ".gsutil": true, ".snowsql": true, ".linode-cli": true, ".twilio-cli": true, ".stripe": true, ".ngrok": true,
	".ngrok2": true, ".vagrant.d": true, ".ansible": true, ".chef": true, ".puppetlabs": true, ".subversion": true,
	".ivy2": true, ".sbt": true, ".nuget": true, ".composer": true, ".bundle": true, ".cabal": true, ".stack": true,
	".kaggle": true, ".huggingface": true, ".1password": true, ".op": true, ".bitwarden": true, ".lastpass": true,
	".thunderbird": true, ".mutt": true, ".kde": true, ".var": true, ".zsh_sessions": true, ".expo": true, ".eas": true,
	".vercel": true, ".netlify": true, ".supabase": true, ".planetscale": true, ".turso": true, ".azure-devops": true,
	".sops": true, ".gcloud-config": true, "kwalletd": true,
}

// gitControlDirs are folder (or file) names whose contents git runs or obeys: a hook, git's
// config, a .git FILE that points git at another directory (worktrees, submodules), husky's
// hooks. Nothing under them is written for a strict caller (#3596, W1), and they are part of
// sensitiveDirs (never served), so one list covers both.
var gitControlDirs = []string{".git", ".husky"}

func init() {
	for _, d := range gitControlDirs {
		sensitiveDirs[d] = true
	}
}

// gitControlPath reports whether rel (relative to a repository, already resolved) is or is
// inside one of gitControlDirs, compared case-insensitively (a case-insensitive file system
// treats .GIT as .git).
func gitControlPath(rel string) bool {
	for _, c := range strings.Split(filepath.ToSlash(rel), "/") {
		for _, d := range gitControlDirs {
			if strings.EqualFold(c, d) {
				return true
			}
		}
	}
	return false
}

// inHooksPath reports whether rel (relative to the repository root) is inside the folder the
// repository's core.hooksPath names: hooks there run like .git/hooks (#3596, W1). git's own
// config is read (reading runs nothing); a hooksPath outside the repository can't be written
// through rel anyway.
func (h *SessionsHandler) inHooksPath(ctx context.Context, repoRoot, rel string) bool {
	// gitutil.Command itself passes -c core.hooksPath=<no hooks> on the command line, so the
	// repository's own value is the last one whose origin isn't the command line.
	out, err := gitutil.Command(ctx, repoRoot, "config", "--show-origin", "--get-all", "core.hooksPath").Output()
	if err != nil {
		return false
	}
	hp := ""
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		origin, value, ok := strings.Cut(line, "\t")
		if ok && !strings.HasPrefix(origin, "command line:") {
			hp = strings.TrimSpace(value)
		}
	}
	if hp == "" {
		return false
	}
	if !filepath.IsAbs(hp) {
		hp = filepath.Join(repoRoot, hp)
	}
	hpRel, err := safefs.Within(repoRoot, hp)
	if err != nil {
		return false
	}
	if hpRel == "." {
		return true // hooks in the repository root itself: every file there could be one
	}
	r, p := strings.ToLower(filepath.ToSlash(rel)), strings.ToLower(filepath.ToSlash(hpRel))
	return r == p || strings.HasPrefix(r, p+"/")
}

// sensitiveSeqs are multi-component locations (lower case) that are never served.
var sensitiveSeqs = [][]string{
	{"library", "application support"}, {"library", "cookies"}, {"library", "safari"}, {"library", "keychains"},
	{"library", "mail"}, {"library", "messages"}, {".local", "share", "keyrings"}, {".local", "share", "gnome-keyring"},
	// #4531: macOS app data beyond Application Support (sandboxed apps' containers, the account and preference stores, calendars);
	// Windows' per-user app data (%APPDATA%, %LOCALAPPDATA%: browser profiles, credential and DPAPI stores); the Linux wallets and
	// tokens kept outside .config; browser profiles of snap and flatpak installs; a model hub's token cache.
	{"library", "containers"}, {"library", "group containers"}, {"library", "preferences"}, {"library", "accounts"},
	{"library", "calendars"}, {"library", "identityservices"},
	// Windows' per-user app data (Security's list): Roaming and LocalLow whole (credential, DPAPI and browser stores; nothing a
	// project or a temp file uses) and only the named folders of Local, never all of it: the temp folder is AppData\Local\Temp,
	// and tools (Programs) and checkouts live there.
	{"appdata", "roaming"}, {"appdata", "locallow"},
	{"appdata", "local", "google"}, {"appdata", "local", "microsoft"}, {"appdata", "local", "packages"}, {"appdata", "local", "mozilla"},
	{"appdata", "local", "bravesoftware"}, {"appdata", "local", "vivaldi"}, {"appdata", "local", "opera software"},
	{"appdata", "local", "1password"}, {"appdata", "local", "comms"},
	{".local", "share", "kwalletd"}, {".local", "share", "python_keyring"}, {".local", "share", "opencode"}, {".local", "share", "com.vercel.cli"},
	{".cache", "huggingface"}, {"snap", "firefox"}, {"snap", "chromium"}, {"snap", "brave"},
}

// sensitiveNames match the base name (lower case), alone or followed by . - _.
var sensitiveNames = []string{".env", ".netrc", ".npmrc", ".pypirc", ".pgpass", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	"credentials", "secrets", "master.key", "kubeconfig", ".envrc", ".gitconfig", ".git-credentials", ".my.cnf", ".s3cfg",
	// #4531: more credential files by name: Windows' _netrc and registry hive, tool configs that hold tokens or passwords, mail
	// clients' password settings, and the agent-adjacent tools' own config (aider).
	"_netrc", "ntuser.dat", ".vault-token", ".databrickscfg", ".boto", ".dockercfg", ".terraformrc", ".yarnrc", ".condarc",
	".msmtprc", ".fetchmailrc", ".mbsyncrc", ".offlineimaprc", ".smbcredentials", ".authinfo", ".mylogin.cnf", ".pg_service.conf",
	".hgrc", ".wgetrc", ".curlrc", ".cvspass", ".sentryclirc", ".aider.conf", ".aider.chat", "kaggle.json", ".pnpmrc", ".bowerrc"}

// sensitiveExact are base names matched exactly (shell startup files often export tokens).
var sensitiveExact = map[string]bool{
	".bashrc": true, ".zshrc": true, ".profile": true, ".bash_profile": true, ".zprofile": true, ".zshenv": true,
	".bash_login": true, ".lesshst": true, ".viminfo": true, ".wget-hsts": true, "known_hosts": true,
	"authorized_keys": true, "cookies.sqlite": true, "login data": true,
	// The agent CLIs' credential files (#3672, path audit #3669 finding 3), anywhere: Claude's
	// (.claude.json; .credentials.json on Linux and Windows), Codex's and Gemini's.
	".claude.json": true, ".credentials.json": true, "auth.json": true, "oauth_creds.json": true,
}

// agentHomeDirs are the agent CLIs' own folders (credentials, sessions, settings). They are
// denied in the user's home folder only (#3672): a repository's checked-in .claude/
// (settings.json, commands, hooks) stays readable, and the credential files in them are denied
// anywhere by name (sensitiveExact). If the home folder can't be found they are denied anywhere.
// #4531: every agent CLI Coral drives (claude, codex, gemini and Antigravity, which keeps its state under .gemini and in its own
// folder; pi), and the common coding assistants that keep keys or sessions in the home folder.
var agentHomeDirs = []string{".claude", ".codex", ".gemini", ".pi", ".agy", ".antigravity", ".cursor", ".continue", ".codeium", ".windsurf",
	".opencode", ".qwen", ".copilot", ".augment", ".goose", ".aider"}

// resolvedHome is realPath(home), remembered for the last home seen (sensitivePath runs for
// every entry of a folder listing).
var homeCache struct {
	sync.Mutex
	home, real string
}

func resolvedHome(home string) string {
	homeCache.Lock()
	defer homeCache.Unlock()
	if homeCache.home != home {
		homeCache.home, homeCache.real = home, realPath(home)
	}
	return homeCache.real
}

// inAgentHomeDir reports whether p is one of agentHomeDirs in the home folder, or inside one.
// Compared without case (macOS and Windows file systems ignore it), on the home folder as
// given and resolved.
func inAgentHomeDir(p string, lower []string) bool {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		for _, c := range lower {
			for _, d := range agentHomeDirs {
				if c == d {
					return true
				}
			}
		}
		return false
	}
	homes := []string{home}
	if rh := resolvedHome(home); rh != "" && rh != home {
		homes = append(homes, rh)
	}
	lp := strings.ToLower(filepath.Clean(p))
	for _, h := range homes {
		for _, d := range agentHomeDirs {
			if within(strings.ToLower(filepath.Join(h, d)), lp) {
				return true
			}
		}
	}
	return false
}

var sensitiveSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks", ".kdbx", ".ppk", ".tfstate", "_history", ".history",
	// #4531: macOS keychain files, PKCS#12 by its other name, VPN profiles (they embed keys), KeePass 1, Apple's auth keys, wallets
	// and Terraform variable files (secrets are often kept in them).
	".keychain", ".keychain-db", ".pkcs12", ".ovpn", ".kdb", ".p8", ".wallet", ".jceks", ".tfvars"}

// Coral's own data folder holds the API key, the session tokens and the databases: nothing in it is served, wherever it is
// (CORAL_DATA_DIR, --home, or ~/.coral). The handlers register the folder they run with; the default is always covered.
var protectedDataDirs struct {
	sync.Mutex
	dirs []string
}

// ProtectDataDir adds dir (and its symlink-resolved form) to the folders sensitivePath refuses.
func ProtectDataDir(dir string) {
	if dir == "" {
		return
	}
	protectedDataDirs.Lock()
	defer protectedDataDirs.Unlock()
	for _, d := range []string{dir, realPath(dir)} {
		if d = strings.ToLower(filepath.Clean(d)); d != "." && d != "" && !slices.Contains(protectedDataDirs.dirs, d) {
			protectedDataDirs.dirs = append(protectedDataDirs.dirs, d)
		}
	}
}

func inCoralDataDir(p string) bool {
	lp := strings.ToLower(filepath.Clean(p))
	protectedDataDirs.Lock()
	dirs := append([]string{}, protectedDataDirs.dirs...)
	protectedDataDirs.Unlock()
	home, _ := os.UserHomeDir()
	homes := []string{strings.ToLower(filepath.Clean(home)), strings.ToLower(filepath.Clean(realPath(home)))}
	for _, d := range append(dirs, strings.ToLower(filepath.Clean(config.ResolveCoralDir("")))) {
		// A data folder that is the home folder itself (or holds it) cannot be walled off as a whole: the per-name
		// lists still apply inside it.
		if within(d, lp) && !within(d, homes[0]) && !within(d, homes[1]) {
			return true
		}
	}
	return false
}

// sensitivePath reports whether p names a credential-bearing or history location. It
// checks every path component, so it also applies inside an otherwise allowed repository.
func sensitivePath(p string) bool {
	if inCoralDataDir(p) {
		return true
	}
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
	if inAgentHomeDir(p, lower) {
		return true
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

// trustedGitRoot is the git root of workdir that Coral may use as a root for files: what git
// reports, but only when it is workdir itself, or one of its parents (workdir is inside the
// repository) or a repository inside workdir (git's one-level child fallback) that holds its
// own .git. Anything else
// came from the repository's own config (core.worktree in an agent-writable .git/config can
// name any folder), so workdir is used instead (#3596 W6). Every root derivation (the file
// routes' resolveGitRoot, auto-attach's SessionDirs) goes through it.
func trustedGitRoot(ctx context.Context, workdir string) string {
	root := gitutil.ResolveGitRoot(ctx, workdir)
	if root == "" || root == workdir {
		return root
	}
	realW, err1 := safefs.Resolve(workdir)
	realR, err2 := safefs.Resolve(root)
	if err1 != nil || err2 != nil {
		return workdir
	}
	if !within(realR, realW) && !within(realW, realR) {
		return workdir
	}
	// A real repository's top level holds its own .git: a folder, or a gitdir file (worktrees,
	// submodules). A forged core.worktree naming a parent folder that isn't a repository (one
	// that holds other projects) doesn't, and isn't trusted (Security's W6 SHOULD).
	fi, err := os.Lstat(filepath.Join(realR, ".git"))
	if err != nil || !(fi.IsDir() || fi.Mode().IsRegular()) {
		return workdir
	}
	return root
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
