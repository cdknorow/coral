package startup

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
)

// #5136: every way Coral can listen is this-computer-only by default, and only the remote_access_enabled gate (the saved setting, or --remote)
// widens it. These censuses fail when a new listener, a new wildcard address or a new --host default appears without being looked at:
//
//   - every call that opens a socket is named below, with the reason it is safe;
//   - every bind-address literal (0.0.0.0, ::, ":8420") and every write to a Config's Host is named below;
//   - every host flag defaults to nothing or to loopback, and --remote to off;
//   - the packaging, the dev targets and the docs never put a wildcard bind on a default path;
//   - the gate and the listener together, on real sockets: with remote access off, whatever host was asked for, only loopback is bound.

// repoRoots returns coral-go and the repository root.
func repoRoots(t *testing.T) (goRoot, repo string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	goRoot = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	repo = filepath.Dir(goRoot)
	if _, err := os.Stat(filepath.Join(goRoot, "go.mod")); err != nil {
		t.Skip("not in the repository checkout")
	}
	return goRoot, repo
}

type goSource struct {
	rel  string // path from coral-go, slash separated
	fset *token.FileSet
	file *ast.File
}

// productionSources parses every non-test Go file under coral-go, whatever its build tags (a Windows-only or webview-only file is as much a
// listener as any other).
func productionSources(t *testing.T) []goSource {
	t.Helper()
	goRoot, _ := repoRoots(t)
	var out []goSource
	err := filepath.WalkDir(goRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", "testdata", "vendor", ".git", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		rel, _ := filepath.Rel(goRoot, path)
		out = append(out, goSource{rel: filepath.ToSlash(rel), fset: fset, file: f})
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, len(out), 200, "the census read only %d files: is the walk broken?", len(out))
	return out
}

// funcSpans lets a node be attributed to the function that holds it ("pkg-level" outside any).
type funcSpans []struct {
	name     string
	pos, end token.Pos
}

func spansOf(f *ast.File) funcSpans {
	var s funcSpans
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			s = append(s, struct {
				name     string
				pos, end token.Pos
			}{fd.Name.Name, fd.Pos(), fd.End()})
		}
	}
	return s
}

func (s funcSpans) at(p token.Pos) string {
	for _, f := range s {
		if p >= f.pos && p < f.end {
			return f.name
		}
	}
	return "(package level)"
}

// sites is a census table: "file:function" -> why it is safe. A site seen but not listed fails; a listed site that is gone fails too, so the
// table never goes stale.
func checkCensus(t *testing.T, what string, found map[string]bool, allowed map[string]string, fix string) {
	t.Helper()
	var unknown, gone []string
	for k := range found {
		if _, ok := allowed[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	for k := range allowed {
		if !found[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(unknown)
	sort.Strings(gone)
	for _, k := range unknown {
		t.Errorf("%s: %s is new. %s", what, k, fix)
	}
	for _, k := range gone {
		t.Errorf("%s: %s is listed but no longer exists: remove it from the table", what, k)
	}
}

// Every call that opens a listening socket. A new one has to be this-computer-only or sit behind the remote_access_enabled gate.
func TestEveryListenerIsCensused(t *testing.T) {
	opens := map[string]bool{
		"Listen": true, "ListenTCP": true, "ListenUDP": true, "ListenIP": true, "ListenUnix": true, "ListenUnixgram": true, "ListenPacket": true,
		"ListenMulticastUDP": true, "ListenAndServe": true, "ListenAndServeTLS": true, "FileListener": true, "FilePacketConn": true,
		"ListenConfig": true,
	}
	allowed := map[string]string{
		"internal/startup/startup_listen.go:listenAll": "the server's listeners, one per ListenHosts(): the host is cfg.Host after applyPrivacySettings, " +
			"which is loopback unless remote_access_enabled or --remote (TestRemoteOffBindsLoopbackOnly drives it on real sockets)",
		"internal/startup/pprof.go:startPprofOn": "CORAL_PPROF_ADDR, refused unless a loopback IP (checkLoopbackAddr)",
	}
	found := map[string]bool{}
	for _, src := range productionSources(t) {
		spans := spansOf(src.file)
		ast.Inspect(src.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !opens[sel.Sel.Name] {
				return true
			}
			found[src.rel+":"+spans.at(sel.Pos())] = true
			return true
		})
	}
	checkCensus(t, "listener", found, allowed,
		"A new socket must be this-computer-only by default and widen only through remote_access_enabled (applyPrivacySettings / widenForRemote), or take a loopback-checked address like pprof; then add it to the table with the reason.")
}

var wildcardRe = regexp.MustCompile(`^(0\.0\.0\.0|::|\[::\]|:[0-9]+|:%d|:%s|0\.0\.0\.0:[0-9%dsv]+)$`)

// Every bind-address literal that means "every interface", and every write to a Config's Host. A new one is either a comparison (add it) or a
// bind that must go through widenForRemote.
func TestEveryWildcardAddressAndHostWriteIsCensused(t *testing.T) {
	allowed := map[string]string{
		"cmd/coral-board/api.go:endpointFromEnv":            "compares CORAL_HOST: a wildcard means dial 127.0.0.1",
		"internal/agent/agent.go:CoralEnv":                  "compares: an agent dials a real interface, never a wildcard",
		"internal/agentenv/agentenv.go:UseServerEnv":        "compares: the host an agent dials",
		"internal/auth/middleware.go:SetListenHosts":        "compares: Host headers naming a wildcard are refused",
		"internal/background/git_poller.go:lookupPR":        `not an address: the "::" in a cache key`,
		"internal/config/hosts.go:IsWildcardHost":           "compares",
		"internal/hooks/identity.go:configuredHosts":        "compares CORAL_HOST: a wildcard is not a place to dial",
		"internal/startup/startup_listen.go:widenForRemote": "the one place a wildcard host is set: only when remote access is on and no host was asked for",
	}
	found := map[string]bool{}
	hostWrites := map[string]bool{}
	allowedWrites := map[string]string{
		"cmd/coral/main.go:main": "--host: a host that was asked for, still gated by applyPrivacySettings",
		"cmd/coral/relaunch_fixture_on.go:applyRelaunchFixtureBuild": "the relaunchfixture build only (a build tag no release uses): refuses --remote and any host that is not loopback, and sets 127.0.0.1 when none is given, so it only narrows the bind",
		"cmd/coral-tray/child.go:runServerChild":                     "the tray's --host, handed on only when asked for (hostArgs), still gated by applyPrivacySettings",
		"internal/config/config.go:Load":                             "the default 127.0.0.1, or CORAL_HOST (explicit, gated)",
		"internal/startup/startup_listen.go:restrictToLoopback":      "forces 127.0.0.1",
		"internal/startup/startup_listen.go:widenForRemote":          "the wildcard, only on the gate",
	}
	for _, src := range productionSources(t) {
		spans := spansOf(src.file)
		ast.Inspect(src.file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(x.Value)
				if err == nil && wildcardRe.MatchString(s) {
					found[src.rel+":"+spans.at(x.Pos())] = true
				}
			case *ast.AssignStmt:
				for _, l := range x.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "Host" {
						if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "cfg" || id.Name == "conf" || id.Name == "c" && strings.HasPrefix(src.rel, "internal/config/")) {
							hostWrites[src.rel+":"+spans.at(x.Pos())] = true
						}
					}
				}
				// cfg.Host, cfg.HostExplicit = host, true
			case *ast.CompositeLit:
				if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Config" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "config" {
						for _, e := range x.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Host" {
									hostWrites[src.rel+":"+spans.at(x.Pos())] = true
								}
							}
						}
					}
				} else if id, ok := x.Type.(*ast.Ident); ok && id.Name == "Config" && src.rel == "internal/config/config.go" {
					for _, e := range x.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Host" {
								hostWrites[src.rel+":"+spans.at(x.Pos())] = true
							}
						}
					}
				}
			}
			return true
		})
	}
	checkCensus(t, "wildcard bind literal", found, allowed,
		"A literal that means every interface must not be a default: set a wildcard host only in widenForRemote (the remote_access_enabled gate). If it only compares an address, add it to the table.")
	checkCensus(t, "write to Config.Host", hostWrites, allowedWrites,
		"Config.Host decides the bind. Whatever sets it must leave applyPrivacySettings to gate it (restrictToLoopback / widenForRemote); then add it to the table.")
}

// Only applyPrivacySettings may widen: widenForRemote has no other caller.
func TestWidenForRemoteHasOnlyTheGateAsCaller(t *testing.T) {
	var callers []string
	for _, src := range productionSources(t) {
		spans := spansOf(src.file)
		ast.Inspect(src.file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "widenForRemote" {
					callers = append(callers, src.rel+":"+spans.at(call.Pos()))
				}
			}
			return true
		})
	}
	sort.Strings(callers)
	// Two call sites, both in the gate: settings unreadable with --remote, and --remote or the saved setting "true".
	require.Equal(t, []string{
		"internal/startup/startup_settings.go:applyPrivacySettings",
		"internal/startup/startup_settings.go:applyPrivacySettings",
	}, callers, "widenForRemote must be called only from applyPrivacySettings, which tests remote_access_enabled and --remote")
}

// Every host-like flag defaults to "this computer", and --remote to off.
func TestHostFlagDefaultsAreThisComputer(t *testing.T) {
	hostish := regexp.MustCompile(`(?i)host|listen|bind|addr`)
	remoteish := regexp.MustCompile(`(?i)remote|public|expose|lan`)
	found := map[string]bool{}
	allowed := map[string]string{
		"cmd/coral/main.go:main:host":      `default "": the server's default (this computer only)`,
		"cmd/coral/main.go:main:remote":    "default false",
		"cmd/coral-tray/main.go:main:host": `default "": hostArgs hands the server no --host`,
		"cmd/coral/main.go:main:hub":       "not an address: hub mode does not change the bind",
	}
	for _, src := range productionSources(t) {
		if !strings.HasPrefix(src.rel, "cmd/") {
			continue
		}
		spans := spansOf(src.file)
		ast.Inspect(src.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			var nameArg, defArg ast.Expr
			switch sel.Sel.Name {
			case "String", "Bool":
				if len(call.Args) >= 3 {
					nameArg, defArg = call.Args[0], call.Args[1]
				}
			case "StringVar", "BoolVar":
				if len(call.Args) >= 4 {
					nameArg, defArg = call.Args[1], call.Args[2]
				}
			default:
				return true
			}
			lit, ok := nameArg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			if !hostish.MatchString(name) && !remoteish.MatchString(name) && name != "hub" {
				return true
			}
			key := src.rel + ":" + spans.at(call.Pos()) + ":" + name
			found[key] = true
			if name == "host" {
				usage := call.Args[len(call.Args)-1]
				if u, ok := usage.(*ast.BasicLit); !ok || !strings.Contains(strings.ToLower(u.Value), "remote") {
					t.Errorf("%s: the --host help must say that a network address needs remote access on, and that the default is this computer only", key)
				}
			}
			switch sel.Sel.Name {
			case "String", "StringVar":
				d, ok := defArg.(*ast.BasicLit)
				if !ok {
					t.Errorf("%s: the default of --%s is not a literal: an address default has to be \"\" (the server's this-computer default) or loopback", key, name)
					return true
				}
				v, _ := strconv.Unquote(d.Value)
				if v != "" && !config.IsLoopbackHost(v) {
					t.Errorf("%s: --%s defaults to %q, which is reachable from the network", key, name, v)
				}
			case "Bool", "BoolVar":
				if id, ok := defArg.(*ast.Ident); !ok || id.Name != "false" {
					// no-auto-loopback reads CORAL_NO_AUTO_LOOPBACK, which can only narrow.
					if name != "no-auto-loopback" {
						t.Errorf("%s: --%s must default to false", key, name)
					}
				}
			}
			return true
		})
	}
	checkCensus(t, "host flag", found, allowed,
		"A new address or remote flag must default to this computer only and widen only through remote_access_enabled; then add it to the table.")
}

var hostFlagValueRe = regexp.MustCompile(`--host[ =]+["']?([^\s"'\x60,;)]*(?:,[^\s"'\x60;)]+)*)`)

// Packaging and dev targets: nothing the person double-clicks, installs or autostarts passes a network host; only the dev test-server target
// asks for 0.0.0.0 (and remote access is still the gate there: it asks on first launch).
func TestPackagingNeverStartsWithANetworkHost(t *testing.T) {
	goRoot, repo := repoRoots(t)
	if _, err := os.Stat(filepath.Join(repo, "tools", "coral.wxs")); err != nil {
		t.Skip("packaging files are not in this checkout")
	}
	var files []string
	for _, pattern := range []string{
		filepath.Join(repo, "tools", "*.wxs"), filepath.Join(repo, "tools", "*.plist"), filepath.Join(repo, "tools", "*.sh"),
		filepath.Join(repo, "installers", "*.sh"), filepath.Join(repo, "Casks", "*.rb"), filepath.Join(repo, "*.plist"),
		filepath.Join(goRoot, "Makefile"), filepath.Join(repo, ".github", "workflows", "*.yml"),
	} {
		m, err := filepath.Glob(pattern)
		require.NoError(t, err)
		files = append(files, m...)
	}
	require.NotEmpty(t, files)
	// Dev: the Makefile's test-server recipe is the one allowed network host.
	for _, path := range files {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		rel, _ := filepath.Rel(repo, path)
		inTestServer := false
		for i, line := range strings.Split(string(b), "\n") {
			if filepath.Base(path) == "Makefile" {
				if strings.HasPrefix(line, "test-server:") {
					inTestServer = true
				} else if len(line) > 0 && line[0] != '\t' && line[0] != ' ' {
					inTestServer = false
				}
			}
			for _, m := range hostFlagValueRe.FindAllStringSubmatch(line, -1) {
				if m[1] == "" || strings.HasPrefix(m[1], "$") || config.IsLoopbackHost(m[1]) {
					continue
				}
				if inTestServer {
					continue
				}
				t.Errorf("%s:%d starts Coral with --host %s: a launcher, installer or workflow must not bind the network by default", filepath.ToSlash(rel), i+1, m[1])
			}
			if strings.Contains(line, "CORAL_HOST=") && !strings.Contains(line, "CORAL_HOST=127.0.0.1") && !strings.Contains(line, "CORAL_HOST=localhost") &&
				filepath.Base(path) != "run_identity.sh" && !inTestServer {
				t.Errorf("%s:%d sets CORAL_HOST to a network address", filepath.ToSlash(rel), i+1)
			}
		}
	}
	// The Makefile's run target and the shipped examples stay on loopback.
	mk, err := os.ReadFile(filepath.Join(goRoot, "Makefile"))
	require.NoError(t, err)
	require.Contains(t, string(mk), "--host 127.0.0.1 --port 8420", "make run must bind this computer only")
}

// Docs: any command line that names a network host also says --remote, and the documented defaults say this computer only.
func TestDocsNeverShowANetworkHostWithoutRemote(t *testing.T) {
	goRoot, repo := repoRoots(t)
	var docs []string
	for _, pattern := range []string{
		filepath.Join(repo, "README.md"), filepath.Join(goRoot, "site_docs", "*.md"), filepath.Join(goRoot, "agent_docs", "*.md"),
	} {
		m, err := filepath.Glob(pattern)
		require.NoError(t, err)
		docs = append(docs, m...)
	}
	require.NotEmpty(t, docs)
	// Prose that says what happens to such a flag when remote access is off is not an example of using it.
	prose := []string{"This also applies if you pass", "Only honored for network addresses", "If the list names only network addresses"}
	for _, path := range docs {
		b, err := os.ReadFile(path)
		require.NoError(t, err)
		rel, _ := filepath.Rel(repo, path)
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range hostFlagValueRe.FindAllStringSubmatch(line, -1) {
				if m[1] == "" || config.IsLoopbackHost(m[1]) || strings.Contains(line, "--remote") || strings.Contains(strings.ToLower(line), "remote access") {
					continue
				}
				skip := false
				for _, p := range prose {
					skip = skip || strings.Contains(line, p)
				}
				if !skip && !strings.HasPrefix(strings.TrimSpace(line), "|") { // a flag table row describes the flag
					t.Errorf("%s:%d shows --host %s without --remote or a mention of remote access: on a default start Coral would bind 127.0.0.1 instead, and the example would mislead", filepath.ToSlash(rel), i+1, m[1])
				}
			}
		}
	}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(goRoot, filepath.FromSlash(rel)))
		require.NoError(t, err)
		return string(b)
	}
	ra := read("site_docs/remote-access.md")
	for _, want := range []string{"off by default", "127.0.0.1", "--remote", "every interface"} {
		require.Contains(t, ra, want, "site_docs/remote-access.md must say %q: what the default is, and what turning remote access on does", want)
	}
	require.Contains(t, read("site_docs/getting-started.md"), "listens on this computer only")
}

// The gate and the listener together, on real sockets. Whatever host was asked for, with remote access off Coral binds loopback only: every
// way of asking (--host, CORAL_HOST, a list, a hostname, a wildcard, hub mode, --no-auto-loopback) goes through applyPrivacySettings, and the
// sockets listenAll opens are the proof. With remote access on the bind is checked as a host only: no wildcard socket is opened by a test.
func TestRemoteOffBindsLoopbackOnly(t *testing.T) {
	ctx := context.Background()
	hosts := []struct {
		name     string
		host     string
		explicit bool
	}{
		{"default", "127.0.0.1", false},
		{"wildcard v4", "0.0.0.0", true},
		{"wildcard v6", "::", true},
		{"bracketed wildcard", "[::]", true},
		{"empty host", "", true},
		{"lan address", "192.168.1.20", true},
		{"tailnet address", "100.64.0.1", true},
		{"lan and loopback", "127.0.0.1,192.168.1.20", true},
		{"loopback then wildcard", "127.0.0.1,0.0.0.0", true},
		{"hostname", "myhost.local", true},
		{"localhost lookalike", "localhost.example.com", true},
		{"ipv4-mapped lan", "::ffff:192.168.1.20", true},
		{"loopback list", "127.0.0.1,::1", true},
		{"localhost", "localhost", true},
	}
	const unreadable = "(settings database closed)" // a read that fails must not widen either
	settings := []string{"", "false", "False", "0", "no", "off", "yes", "1", "enabled", "tru", "true!", "\"true\"", "null", unreadable}
	for _, setting := range settings {
		db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
		require.NoError(t, err)
		defer db.Close()
		switch setting {
		case "":
		case unreadable:
			require.NoError(t, db.Close())
		default:
			require.NoError(t, store.NewSessionStore(db).SetSetting(ctx, "remote_access_enabled", setting))
		}
		for _, h := range hosts {
			for _, noAuto := range []bool{false, true} {
				for _, hub := range []bool{false, true} {
					cfg := &config.Config{Host: h.host, HostExplicit: h.explicit, NoAutoLoopback: noAuto, HubMode: hub, Port: 0}
					applyPrivacySettings(ctx, db, cfg, false, nil)
					lns, _, err := listenAll(cfg)
					if err != nil {
						// A hostname or address this machine does not own cannot bind; the gate must still have made it loopback.
						require.True(t, config.IsLoopbackHost(cfg.Host), "the gate left host %q for remote access off (saved %q)", cfg.Host, setting)
						continue
					}
					require.NotEmpty(t, lns)
					for _, ln := range lns {
						tcp, ok := ln.Addr().(*net.TCPAddr)
						ln.Close()
						require.True(t, ok, "%T", ln.Addr())
						require.True(t, tcp.IP.IsLoopback(), "remote access is off (saved %q) but Coral listens on %s (asked for %q, noAutoLoopback=%v, hub=%v)", setting, ln.Addr(), h.host, noAuto, hub)
					}
				}
			}
		}
	}
}

// With remote access on, a host that was asked for is kept and no host that was not asked for is invented beyond the documented one.
func TestRemoteOnWidensOnlyAsDocumented(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		host     string
		explicit bool
		force    bool
		saved    string
		want     string
	}{
		{"saved on, no host: every interface", "127.0.0.1", false, false, "true", "0.0.0.0"},
		{"--remote, no host: every interface", "127.0.0.1", false, true, "", "0.0.0.0"},
		{"saved on, host asked for: kept", "100.64.0.1", true, false, "true", "100.64.0.1"},
		{"--remote, loopback asked for: kept", "127.0.0.1", true, true, "", "127.0.0.1"},
		{"--remote, list asked for: kept", "127.0.0.1,100.64.0.1", true, true, "", "127.0.0.1,100.64.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.OpenWithKey(ctx, filepath.Join(t.TempDir(), "sessions.db"), "")
			require.NoError(t, err)
			defer db.Close()
			if tc.saved != "" {
				require.NoError(t, store.NewSessionStore(db).SetSetting(ctx, "remote_access_enabled", tc.saved))
			}
			cfg := &config.Config{Host: tc.host, HostExplicit: tc.explicit}
			applyPrivacySettings(ctx, db, cfg, tc.force, nil)
			require.Equal(t, tc.want, cfg.Host)
		})
	}
}
