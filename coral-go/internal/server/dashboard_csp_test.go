package server

import (
	"strings"
	"testing"
)

func cspDirectives(policy string) map[string]string {
	directives := map[string]string{}
	for _, part := range strings.Split(policy, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, " ")
		directives[name] = value
	}
	return directives
}

// The dashboard's CSP (#3515, #5009): every kind of load is named, and the policy keeps the directives that contain the page. The hub Inbox is the
// hub's own page in a same-origin frame (#4718), so frame-src keeps 'self'; the Linked preview (artifact_preview.js) frames an artifact's http(s)
// address in an empty sandbox, so frame-src also names http: and https:. A change here is a decision for Security.
func TestDashboardCSPPinsEveryDirective(t *testing.T) {
	directives := cspDirectives(dashboardCSPFor("localhost:8420"))
	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src":       "'self' 'unsafe-inline'",
		"font-src":        "'self' data:",
		"connect-src":     "'self' ws://localhost:8420 wss://localhost:8420",
		"form-action":     "'self'",
		"media-src":       "'self' blob: data:",
		"img-src":         "'self' data: blob:",
		"frame-src":       "'self' http: https:",
		"worker-src":      "'self'",
		"base-uri":        "'self'",
		"object-src":      "'none'",
		"frame-ancestors": "'self'",
	}
	for name, value := range want {
		if directives[name] != value {
			t.Errorf("%s is %q, want %q: a change to the dashboard's CSP is a decision for Security", name, directives[name], value)
		}
	}
	if len(directives) != len(want) {
		t.Errorf("the dashboard's CSP has directives %v; this test pins exactly %d of them, so a new one needs a look from Security and a line here", directives, len(want))
	}
}

// Inline is for style only: 'unsafe-inline' belongs to style-src alone (it cannot run script), and no directive has 'unsafe-eval' or a bare wildcard
// source.
func TestDashboardCSPAllowsInlineOnlyForStyle(t *testing.T) {
	for name, value := range cspDirectives(dashboardCSPFor("localhost:8420")) {
		for _, src := range strings.Fields(value) {
			if src == "'unsafe-eval'" || src == "*" || (src == "'unsafe-inline'" && name != "style-src") {
				t.Errorf("%s has %s: only style-src may be inline, and no source may be a wildcard or eval", name, src)
			}
		}
	}
}

// The socket origin comes from the Host the page was requested with, so a Host that is not a plain host[:port] must never reach the header: a ';' or a
// space would add a directive or a source of an attacker's choosing.
func TestDashboardCSPOnlyEchoesAPlainHost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1:8420", "my-box.local:443", "[::1]:8420", "[fe80::1]"} {
		if got := cspDirectives(dashboardCSPFor(host))["connect-src"]; got != "'self' ws://"+host+" wss://"+host {
			t.Errorf("host %q: connect-src is %q", host, got)
		}
	}
	for _, host := range []string{"", "a b", "evil.com; script-src *", "evil.com,x", "a'b", "a\"b", "a/b", "a:b:c", "host:123456", "[zz]", "h\nost", "ex%61mple.com"} {
		policy := dashboardCSPFor(host)
		if got := cspDirectives(policy)["connect-src"]; got != "'self'" {
			t.Errorf("host %q: connect-src is %q, want only 'self'", host, got)
		}
		if strings.Count(policy, ";") != strings.Count(dashboardCSPBase, ";")+1 {
			t.Errorf("host %q changed the number of directives: %s", host, policy)
		}
	}
}
