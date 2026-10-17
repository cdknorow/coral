package frontend

import (
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
	if v := esbuildVersion(); v == "unknown" || !strings.HasPrefix(v, "v") {
		t.Errorf("esbuild version %q: the build info should carry it", v)
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
