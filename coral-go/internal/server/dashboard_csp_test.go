package server

import (
	"strings"
	"testing"
)

// The dashboard's CSP and the Inbox tab (#4718, Security's rider 3): the Inbox is the hub's own page in a same-origin frame, so the policy must never
// gain a frame-src that leaves 'self' out (the Inbox would go blank), and it keeps the directives that contain the page. A frame-src is not added
// today because the Linked preview (artifact_preview.js) frames an external https address in a locked sandbox, and blob: and data: frames exist too.
func TestDashboardCSPKeepsTheInboxFrameAllowedAndTheRestPinned(t *testing.T) {
	directives := map[string]string{}
	for _, part := range strings.Split(dashboardCSP, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, " ")
		directives[name] = value
	}
	if v, ok := directives["frame-src"]; ok && !strings.Contains(" "+v+" ", " 'self' ") {
		t.Errorf("frame-src %q leaves out 'self': the hub Inbox frame (/inbox, same origin) would not load", v)
	}
	if _, ok := directives["default-src"]; ok && !strings.Contains(" "+directives["default-src"]+" ", " 'self' ") {
		if _, hasFrame := directives["frame-src"]; !hasFrame {
			t.Errorf("default-src %q applies to frames and leaves out 'self'", directives["default-src"])
		}
	}
	want := map[string]string{
		"script-src":      "'self'",
		"form-action":     "'self'",
		"media-src":       "'self' blob: data:",
		"img-src":         "'self' data: blob:",
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
