package frontend

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"testing"
)

func render(t *testing.T, d PageData) string {
	t.Helper()
	tmpl, err := ParseIndex()
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, d); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

var scriptTag = regexp.MustCompile(`(?is)<script\b([^>]*)>`)

// The shell must contain no executable inline script, so the Cloud Hub can serve
// it with script-src 'self'. Only external scripts and JSON data blocks are allowed.
func TestIndexHasNoInlineScript(t *testing.T) {
	for name, d := range map[string]PageData{
		"local": {CoralRoot: "/x"},
		"cloud": {CoralRoot: "/x", BootJSON: template.JS(`{"mode":"cloud"}`)},
	} {
		out := render(t, d)
		for _, m := range scriptTag.FindAllStringSubmatch(out, -1) {
			attrs := strings.ToLower(m[1])
			if !strings.Contains(attrs, "src=") && !strings.Contains(attrs, `type="application/json"`) {
				t.Errorf("%s: inline script tag <script%s>", name, m[1])
			}
		}
	}
}

func TestBootJSON(t *testing.T) {
	local := render(t, PageData{})
	if !strings.Contains(local, `id="coral-boot">{"mode":"local"}</script>`) {
		t.Errorf("default boot data missing:\n%s", local[:600])
	}
	cloud := render(t, PageData{BootJSON: template.JS(`{"mode":"cloud"}`)})
	if !strings.Contains(cloud, `id="coral-boot">{"mode":"cloud"}</script>`) {
		t.Error("cloud boot data not rendered verbatim")
	}
}

func TestAssetsEmbedded(t *testing.T) {
	for _, p := range []string{
		"static/app.js", "static/csp_compat.js", "static/cloud.js", "static/cloud_servers.js", "static/theme_init.js",
		"static/sw_register.js", "static/cloud/signin.js", "static/cloud/enroll.js", "static/css/cloud.css",
		"templates/cloud/signin.html", "templates/cloud/enroll.html",
	} {
		if _, err := Static.ReadFile(p); err != nil {
			if _, err2 := Templates.ReadFile(p); err2 != nil {
				t.Errorf("%s not embedded", p)
			}
		}
	}
}

// The standalone pages carry no inline script either.
func TestCloudPagesHaveNoInlineScript(t *testing.T) {
	for _, p := range []string{"templates/cloud/signin.html", "templates/cloud/enroll.html"} {
		b, err := Templates.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range scriptTag.FindAllStringSubmatch(string(b), -1) {
			if !strings.Contains(strings.ToLower(m[1]), "src=") {
				t.Errorf("%s: inline script", p)
			}
		}
		if regexp.MustCompile(`(?i)\son[a-z]+=`).Match(b) {
			t.Errorf("%s: inline event handler", p)
		}
	}
}

// The UI makes no third-party requests: fonts and icons are served from /static/vendor/fonts.
func TestNoThirdPartyHosts(t *testing.T) {
	out := render(t, PageData{})
	for _, bad := range []string{"fonts.googleapis.com", "fonts.gstatic.com", "cdn.", "unpkg.com", "jsdelivr"} {
		if strings.Contains(out, bad) {
			t.Errorf("index shell references %s", bad)
		}
	}
	css, err := Static.ReadFile("static/css/fonts.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"jakarta-latin.woff2", "jakarta-latin-ext.woff2", "material-icons.woff2"} {
		if !strings.Contains(string(css), f) {
			t.Errorf("fonts.css does not use %s", f)
		}
		b, err := Static.ReadFile("static/vendor/fonts/" + f)
		if err != nil || len(b) < 1000 || string(b[:4]) != "wOF2" {
			t.Errorf("%s is missing or not a woff2 font", f)
		}
	}
}
