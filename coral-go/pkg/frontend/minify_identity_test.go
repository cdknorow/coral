package frontend

import (
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
	if !strings.HasPrefix(on, "minify:esbuild ") || !strings.Contains(on, minifyFormat) {
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
