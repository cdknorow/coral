package frontend

import (
	"github.com/evanw/esbuild/pkg/api"
	"os"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

// #4320: the minifier's identity names the esbuild module and the options, and "off" when minifying is off,
// so a version hashed from it changes when the served bytes can.
func TestMinifyIdentity(t *testing.T) {
	t.Setenv("CORAL_NO_MINIFY", "")
	on := MinifyIdentity()
	if !strings.HasPrefix(on, "minify:esbuild ") || !strings.Contains(on, optionsIdentity(minifyOptions(".js"), minifyOptions(".css"))) {
		t.Fatalf("identity %q does not name esbuild and its options", on)
	}
	if !strings.Contains(on, bundleFormat) {
		t.Errorf("identity %q does not carry the bundle's format %q: a change to BuildBundle's output would not change the build id (#4326)", on, bundleFormat)
	}
	if !strings.Contains(on, esbuildVersion) {
		t.Errorf("identity %q does not carry the esbuild version %s", on, esbuildVersion)
	}
	t.Setenv("CORAL_NO_MINIFY", "1")
	if off := MinifyIdentity(); off != "minify:off" || off == on {
		t.Errorf("CORAL_NO_MINIFY=1: identity %q", off)
	}
	t.Setenv("CORAL_NO_MINIFY", "")
	SetMinifyDisabled(true)
	defer SetMinifyDisabled(false)
	if MinifyIdentity() != "minify:off" {
		t.Error("SetMinifyDisabled is not in the identity")
	}
}

// The version in the identity is go.mod's: bumping esbuild without bumping the constant fails here, so a
// new esbuild is a new build id (#4320). Where a binary carries its dependency list (the Mac, Linux
// and every release build do; a Windows test binary on CI did not), that list agrees too.
func TestEsbuildVersionMatchesGoMod(t *testing.T) {
	mod, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*(?:require\s+)?github\.com/evanw/esbuild\s+(v\S+)`).FindSubmatch(mod)
	if m == nil {
		t.Fatal("go.mod has no require line for github.com/evanw/esbuild")
	}
	if got := string(m[1]); got != esbuildVersion {
		t.Errorf("go.mod requires esbuild %s but the minifier identity says %s: update esbuildVersion in minify.go", got, esbuildVersion)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/evanw/esbuild" && d.Version != esbuildVersion {
				t.Errorf("this binary was built with esbuild %s, the identity says %s", d.Version, esbuildVersion)
			}
		}
	}
}

// #4320 INFO 1: the identity is built from the options MinifyAsset really uses, so changing any of
// them changes it, and the options MinifyAsset runs with are minifyOptions' (not a copy).
func TestMinifyIdentityFollowsEveryOption(t *testing.T) {
	t.Setenv("CORAL_NO_MINIFY", "")
	js, css := minifyOptions(".js"), minifyOptions(".css")
	base := optionsIdentity(js, css)
	if base != optionsIdentity(minifyOptions(".js"), minifyOptions(".css")) {
		t.Fatal("the identity is not stable")
	}
	for name, change := range map[string]func(o *api.TransformOptions){
		"MinifyWhitespace":  func(o *api.TransformOptions) { o.MinifyWhitespace = !o.MinifyWhitespace },
		"MinifySyntax":      func(o *api.TransformOptions) { o.MinifySyntax = !o.MinifySyntax },
		"MinifyIdentifiers": func(o *api.TransformOptions) { o.MinifyIdentifiers = !o.MinifyIdentifiers },
		"Target":            func(o *api.TransformOptions) { o.Target = api.ES2015 },
		"Charset":           func(o *api.TransformOptions) { o.Charset = api.CharsetASCII },
		"LegalComments":     func(o *api.TransformOptions) { o.LegalComments = api.LegalCommentsNone },
		"Loader":            func(o *api.TransformOptions) { o.Loader = api.LoaderText },
		"Drop":              func(o *api.TransformOptions) { o.Drop = api.DropConsole },
	} {
		j, c := js, css
		change(&j)
		if optionsIdentity(j, c) == base {
			t.Errorf("changing the JS %s does not change the identity", name)
		}
		j, c = js, css
		change(&c)
		if optionsIdentity(j, c) == base {
			t.Errorf("changing the CSS %s does not change the identity", name)
		}
	}
	// What MinifyAsset runs with is minifyOptions: a file it shortens is shortened by those options.
	in := []byte("/* c */ a  {  color : red ;  }\n")
	got := MinifyAsset("css/x.css", in)
	want := api.Transform(string(in), func() api.TransformOptions { o := minifyOptions(".css"); o.Sourcefile = "css/x.css"; return o }())
	if string(got) != string(want.Code) {
		t.Errorf("MinifyAsset = %q, minifyOptions give %q", got, want.Code)
	}
}
