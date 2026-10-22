//go:build webview

package main

import (
	"regexp"
	"strconv"
	"strings"
)

// The page's theme colours reach the native title bar as CSS colour text (the value of --bg-primary and --border). The page is the dashboard's own, but the
// text is still checked here: only #rgb, #rrggbb and rgb()/rgba() with plain numbers are colours.
var (
	hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	rgbColor = regexp.MustCompile(`^rgba?\(\s*(\d{1,3})\s*[, ]\s*(\d{1,3})\s*[, ]\s*(\d{1,3})\s*(?:[,/]\s*[0-9.]+%?\s*)?\)$`)
)

// parseCSSColor reads a CSS colour as red, green and blue; ok is false for anything else.
func parseCSSColor(s string) (r, g, b uint8, ok bool) {
	s = strings.TrimSpace(s)
	if m := hexColor.FindStringSubmatch(s); m != nil {
		h := m[1]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return uint8(v >> 16), uint8(v >> 8), uint8(v), true
	}
	if m := rgbColor.FindStringSubmatch(s); m != nil {
		var c [3]uint8
		for i := range c {
			n, err := strconv.Atoi(m[i+1])
			if err != nil || n > 255 {
				return 0, 0, 0, false
			}
			c[i] = uint8(n)
		}
		return c[0], c[1], c[2], true
	}
	return 0, 0, 0, false
}

// isDarkColor says whether text on this background should be light (the caption's immersive dark mode): its perceived brightness is under half.
func isDarkColor(r, g, b uint8) bool {
	return (299*int(r)+587*int(g)+114*int(b))/1000 < 128
}
