//go:build webview

package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// linkClick runs linkInterceptScript in node against a page at pageURL and returns what a click on a link with this href and target did.
func linkClick(t *testing.T, node, pageURL, href, target string) (prevented bool, opened []string, navigatedHere bool) {
	t.Helper()
	harness := `
const page = new URL(process.argv[1]);
global.location = { href: page.href, origin: page.origin };
let handler = null; const opened = [];
global.document = { addEventListener: (type, fn) => { if (type === 'click') handler = fn; } };
global.window = { open: u => opened.push(u) };
global._coralOpenExternal = u => opened.push(u);
window._coralOpenExternal = global._coralOpenExternal;
eval(process.argv[2]);
let prevented = false;
const anchor = { getAttribute: () => process.argv[3], target: process.argv[4] };
handler({ target: { closest: () => anchor }, preventDefault: () => { prevented = true; } });
console.log(JSON.stringify({ prevented, opened }));`
	out, err := exec.Command(node, "-e", harness, pageURL, linkInterceptScript, href, target).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var r struct {
		Prevented bool     `json:"prevented"`
		Opened    []string `json:"opened"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &r); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return r.Prevented, r.Opened, !r.Prevented
}

// What a click on a link does in the app window (#5094): only http and https are followed, a look-alike of the page's origin is another origin, a file or a
// custom scheme does nothing, and the system browser gets only a parsed web address.
func TestLinkInterceptScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	const page = "http://localhost:8420/agents"
	cases := []struct {
		name, href, target string
		prevented          bool
		opened             string
	}{
		{"the page's own link stays in the window", "/api/x", "", false, ""},
		{"a same-origin absolute link stays", "http://localhost:8420/y", "", false, ""},
		{"a link to another site opens in the system browser", "https://example.com/a", "", true, "https://example.com/a"},
		{"target blank opens in the system browser", "/docs", "_blank", true, "http://localhost:8420/docs"},
		{"a userinfo look-alike of the origin is another site", "http://localhost:8420@evil.example/x", "", true, "http://localhost:8420@evil.example/x"},
		{"a look-alike that does not parse is neither followed nor opened", "http://localhost:8420.evil.example/x", "", true, ""},
		{"a look-alike host on the same prefix is another site", "http://localhost.evil.example:8420/x", "", true, "http://localhost.evil.example:8420/x"},
		{"a file link does nothing", "file:///tmp/x.command", "", true, ""},
		{"a file link in a new tab does nothing", "file:///tmp/x.command", "_blank", true, ""},
		{"a custom scheme does nothing", "ssh://host", "", true, ""},
		{"a javascript link does nothing", "javascript:alert(1)", "", true, ""},
		{"a data link does nothing", "data:text/html,x", "_blank", true, ""},
		{"a path to a file in a new tab opens the page URL, never the path", "/Applications/Calculator.app", "_blank", true, "http://localhost:8420/Applications/Calculator.app"},
	}
	for _, c := range cases {
		prevented, opened, _ := linkClick(t, node, page, c.href, c.target)
		if prevented != c.prevented {
			t.Errorf("%s: prevented=%v, want %v", c.name, prevented, c.prevented)
		}
		want := []string(nil)
		if c.opened != "" {
			want = []string{c.opened}
		}
		if strings.Join(opened, "|") != strings.Join(want, "|") {
			t.Errorf("%s: opened %v, want %v", c.name, opened, want)
		}
	}
}
