// Package apptoken is the per-launch secret that tells the dashboard's own page from any other page the
// coral-app window shows (#78 W1).
//
// webview_go's Bind installs window[name] on EVERY page the window shows, and the Go callback is told
// nothing about the page's origin. A page the window was taken to by a script navigation, a redirect or a
// form post could therefore call the native functions (on WebView2 through
// window.chrome.webview.postMessage, even after "delete window[name]"). So the native functions
// take a token first and refuse a call without the right one, and the token appears in exactly one
// place: an init script that installs the page-facing functions only when location.origin is the
// dashboard's. A page of another origin never receives that script's text and cannot forge the token.
// The check in Go is the boundary; deleting the page-facing functions off-origin is defence in depth.
package apptoken

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Guard holds one launch's token.
type Guard struct{ token string }

// New makes a Guard with a fresh random token.
func New() (*Guard, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	return &Guard{token: hex.EncodeToString(b[:])}, nil
}

// Allowed reports whether got is this launch's token (a constant-time comparison).
func (g *Guard) Allowed(got string) bool {
	return g != nil && g.token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(g.token)) == 1
}

// Native pairs a page-facing function name with the native binding it calls with the token first.
type Native struct {
	Page   string // window.<Page>, what the dashboard's script calls, e.g. _coralOpenExternal
	Native string // the Bind name, e.g. __coralNativeOpenExternal; it takes the token as its first argument
}

// ShimScript is the init script that gives the dashboard origin its page-facing functions. On any other
// origin it deletes them (they are never installed there) and the natives stay token-checked.
// The token lives only in the closure of each function: toString() on a shim shows no secret.
func (g *Guard) ShimScript(origin string, natives []Native) string {
	var b strings.Builder
	fmt.Fprintf(&b, "(function(){ if (location.origin !== %s) {", strconv.Quote(origin))
	for _, n := range natives {
		fmt.Fprintf(&b, " try { delete window.%s; } catch (e) {}", n.Page)
	}
	b.WriteString(" return; } var T = " + strconv.Quote(g.token) + ";")
	for _, n := range natives {
		fmt.Fprintf(&b, " window.%s = function(){ var a = [T]; for (var i = 0; i < arguments.length; i++) a.push(arguments[i]); return window.%s.apply(window, a); };", n.Page, n.Native)
	}
	b.WriteString(" })();")
	return b.String()
}
