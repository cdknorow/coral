package auth

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/pkg/tunnel"
)

// A loopback peer normally gets Coral's no-credentials pass. A reverse proxy (Tailscale Serve,
// Caddy, nginx) on the same machine also connects from loopback, so without this every client it
// proxies — a remote tailnet user included — would inherit that pass. isDirectLocal separates a
// request that comes straight from a process on this machine from one a local proxy forwarded,
// and require_auth_on_loopback closes the gap for proxies that forward no header at all (#3585).

// RequireAuthOnLoopbackSetting is the Operator-only setting key (#3585). PUT /api/settings is
// already Operator-only and blocked through the Coral Hub (operatorOnlyRoutes), so the key is too.
const RequireAuthOnLoopbackSetting = "require_auth_on_loopback"

// RequireAuthOnLoopbackEnv overrides the stored setting at startup, without HTTP: an Operator who
// turned the setting on and then can't reach the dashboard (a tokenless local client) restarts
// Coral with CORAL_REQUIRE_AUTH_ON_LOOPBACK=off to force it off and regain access, then clears it
// in Settings (#3585, Security's point C). Set to a true value it forces it on.
const RequireAuthOnLoopbackEnv = "CORAL_REQUIRE_AUTH_ON_LOOPBACK"

// ResolveRequireAuthOnLoopback is the startup value: the env override when it is set to a
// non-empty value (the HTTP-free recovery), else the stored setting value. An empty or unset
// env var is NOT an override, so a blank CORAL_REQUIRE_AUTH_ON_LOOPBACK= in the environment
// never silently turns the switch off (#3585, Security's NIT).
func ResolveRequireAuthOnLoopback(stored string) bool {
	if overridden, v := envOverride(); overridden {
		return v
	}
	return config.Truthy(stored)
}

// envOverride reports whether the env var forces a value, and that value. Only a non-empty value
// overrides.
func envOverride() (overridden bool, value bool) {
	if v, ok := os.LookupEnv(RequireAuthOnLoopbackEnv); ok && strings.TrimSpace(v) != "" {
		return true, config.Truthy(v)
	}
	return false, false
}

// EnvOverridesRequireAuthOnLoopback reports whether the recovery env var is forcing a value, so
// startup can read the stored setting only when it isn't (#3585).
func EnvOverridesRequireAuthOnLoopback() bool {
	overridden, _ := envOverride()
	return overridden
}

// IsLoopbackHost reports whether a URL's host is this machine: localhost (any case) or a
// loopback IP. The one helper for it (#3824; cliauth and cloud each had a copy, pkg/tunnel keeps
// its own since it imports nothing from internal/).
func IsLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// requireAuthLoopback is the live value of the require_auth_on_loopback setting (default off).
// The server sets it at startup and on each settings change; a failed read leaves the last value
// in place (it is only ever Store-d from a successful read), so the gate never flips silently.
var requireAuthLoopback atomic.Bool

var proxyOnLoopbackWarn sync.Once

// SetRequireAuthOnLoopback sets whether even a direct local client must authenticate (#3585).
func SetRequireAuthOnLoopback(v bool) { requireAuthLoopback.Store(v) }

// RequireAuthOnLoopback reports the current setting.
func RequireAuthOnLoopback() bool { return requireAuthLoopback.Load() }

// proxyHeaderExact are forwarding headers whose names match exactly (the prefix families
// X-Forwarded-* and Tailscale-* are matched separately). Names are in canonical header form.
var proxyHeaderExact = map[string]bool{
	"Forwarded":                true,
	"Via":                      true,
	"X-Real-Ip":                true,
	"True-Client-Ip":           true,
	"Cf-Connecting-Ip":         true,
	"X-Original-Forwarded-For": true,
}

// hasProxyHeaders reports whether r carries any header a reverse proxy adds: anything in the
// X-Forwarded-* or Tailscale-* families, or one of proxyHeaderExact. It matches by name, so a
// proxy that sets a new X-Forwarded- header is covered without a list change (#3585).
func hasProxyHeaders(r *http.Request) bool {
	for name := range r.Header {
		// Match the hyphen and the underscore spelling: some proxies send X_Forwarded_For, and
		// Go does not fold '_' to '-'. Normalising both to '-' covers either (#3585 NIT 4).
		canon := http.CanonicalHeaderKey(strings.ReplaceAll(name, "_", "-"))
		if strings.HasPrefix(canon, "X-Forwarded-") || strings.HasPrefix(canon, "Tailscale-") {
			return true
		}
		if proxyHeaderExact[canon] {
			return true
		}
	}
	return false
}

// IsDirectLocal is the one trust predicate for "a person or tool at this computer": a direct
// local client (isDirectLocal) while require_auth_on_loopback is off. Route handlers that grant a
// local-only power (the dashboard's relaxed file bounds, Coral Hub connect, opening an editor,
// deleting the input log) must use it, not a bare RemoteAddr loopback check, so a request a local
// proxy forwarded — and every request while the setting is on, when Coral can't tell a local
// browser from a proxied one — gets the strict, non-local rules (#3585). Coral Hub's tunnel
// also reaches Coral from 127.0.0.1, so a request with its stamp, authentic or not, is never
// local either (#3936): the hub's route table is the only gate for a hub user.
func IsDirectLocal(r *http.Request) bool {
	return localPower(r) && !RequireAuthOnLoopback()
}

// localPower is localOperator for a local-only power (IsDirectLocal's, and reading or rotating the API key): false for every
// request, whoever's it is, once the peer-user check is lost on a system that has one (its socket table is a layout this build
// can't read, peerCheckLost). Without the check Coral can't tell its own account from another one on the computer, so no account
// gets a power until the layout is supported or the owner signs in; the credential-free pass (localOperator, IsDirectLocalPeer)
// keeps its rule, as before #95. The flag is set while the request's connection is attributed, which localOperator does first.
func localPower(r *http.Request) bool {
	return localOperator(r) && !peerCheckLost.Load()
}

// localOperator is isDirectLocal without Coral Hub's tunnel: a loopback peer, no reverse-proxy
// header and no tunnel stamp. Whatever trusts locality as the authority for a local-only power
// uses this (or IsDirectLocal); the middleware's own pass (IsDirectLocalPeer) keeps the tunnel,
// whose stamp the identity layer and the cloud guard check after it.
func localOperator(r *http.Request) bool {
	return isDirectLocal(r) && !tunneled(r)
}

// tunneled reports a request carrying Coral Hub's tunnel stamp, authentic or forged: either way
// it is not the person at this computer.
func tunneled(r *http.Request) bool { return r.Header.Get(tunnel.ViaHeader) != "" }

// IsDirectLocalPeer is IsDirectLocal without the require_auth_on_loopback clause and without the
// peer-user check: a loopback peer with no reverse-proxy header, whatever the setting. It is the
// middleware's loopback pass (whose request the identity layer then checks: strictly, for a
// connection that isn't Coral's own user's, PeerReason), and the signed handshake's. It is for a flow whose OWN credential is
// checked right after (the signed `coral cloud connect` handshake: the signature is the
// credential, so require_auth_on_loopback is already satisfied and must not block it). A proxied
// request is still excluded, so it is never a bypass; use IsDirectLocal everywhere the locality
// itself is the only authority (#3585).
func IsDirectLocalPeer(r *http.Request) bool {
	return loopbackPeer(r)
}

// isDirectLocal reports whether r comes straight from a process of Coral's own user on this
// machine: a loopback peer (by connection address, never a header) that carries no reverse-proxy
// header, and whose connection the peer-user check doesn't say is another user's or unattributed
// (PeerReason, #95). A same-machine
// proxy connects from loopback too but adds a forwarding header, so such a request is treated
// like a remote caller and must present the key or a session. The first proxied-loopback request
// logs a one-time warning, since a proxy that forwards no header (a default nginx) still looks
// direct and needs require_auth_on_loopback (#3585).
func isDirectLocal(r *http.Request) bool {
	return loopbackPeer(r) && PeerReason(r) == ""
}

// loopbackPeer is a loopback peer with no reverse-proxy header, whoever's process it is.
func loopbackPeer(r *http.Request) bool {
	if !isLocalhost(r) {
		return false
	}
	if hasProxyHeaders(r) {
		proxyOnLoopbackWarn.Do(func() {
			log.Printf("[auth] a request reached Coral from a local address but carried a reverse-proxy header; proxied clients are not trusted as local. If a proxy fronts Coral, turn on require_auth_on_loopback, or bind Coral to the tailnet address with --host.")
		})
		return false
	}
	return true
}
