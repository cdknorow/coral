//go:build webview

package main

import "testing"

// What the page may paint the title bar with: CSS colours with plain numbers, nothing else (#313).
func TestParseCSSColor(t *testing.T) {
	for _, c := range []struct {
		in      string
		r, g, b uint8
		ok      bool
	}{
		{"#111114", 0x11, 0x11, 0x14, true},
		{"  #FFF ", 255, 255, 255, true},
		{"#0af", 0x00, 0xaa, 0xff, true},
		{"rgb(46, 46, 54)", 46, 46, 54, true},
		{"rgb(46 46 54)", 46, 46, 54, true},
		{"rgba(10, 20, 30, 0.5)", 10, 20, 30, true},
		{"rgb(10 20 30 / 50%)", 10, 20, 30, true},
		{"", 0, 0, 0, false},
		{"red", 0, 0, 0, false},
		{"#12", 0, 0, 0, false},
		{"#1234567", 0, 0, 0, false},
		{"#gg0000", 0, 0, 0, false},
		{"rgb(300, 0, 0)", 0, 0, 0, false},
		{"rgb(1, 2)", 0, 0, 0, false},
		{"url(javascript:alert(1))", 0, 0, 0, false},
		{"#111114; background: red", 0, 0, 0, false},
	} {
		r, g, b, ok := parseCSSColor(c.in)
		if ok != c.ok || (ok && (r != c.r || g != c.g || b != c.b)) {
			t.Errorf("parseCSSColor(%q) = %d,%d,%d,%v; want %d,%d,%d,%v", c.in, r, g, b, ok, c.r, c.g, c.b, c.ok)
		}
	}
}

// Dark backgrounds get the light caption text, light ones the dark.
func TestIsDarkColor(t *testing.T) {
	for _, c := range []struct {
		r, g, b uint8
		dark    bool
	}{
		{0x11, 0x11, 0x14, true},  // the dark theme's --bg-primary
		{0x2e, 0x2e, 0x36, true},  // its border
		{0xff, 0xff, 0xff, false}, // white
		{0xf5, 0xf5, 0xf7, false}, // a light page
		{0x80, 0x80, 0x80, false}, // the middle goes light
		{0x00, 0x00, 0xff, true},  // pure blue is dark to the eye
	} {
		if got := isDarkColor(c.r, c.g, c.b); got != c.dark {
			t.Errorf("isDarkColor(%d,%d,%d) = %v; want %v", c.r, c.g, c.b, got, c.dark)
		}
	}
}
