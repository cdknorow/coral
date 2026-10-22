// Package appmarker is the token the Coral app (coral-app, the dashboard in its own window) adds to its web view's User-Agent, and the
// reading of it: Coral names a browser session by its User-Agent, so the Sessions page can tell the app's sign-ins from the person's own
// browser and the person can sign them out. The client chooses its User-Agent, so the marker is only a label, never a permission.
package appmarker

import "strings"

// Token is the product token in the User-Agent: "CoralApp/<version>".
const Token = "CoralApp"

// Marker is the product token for version: "CoralApp/1.4.157". Only letters, digits and . + - stay in the version, so it can sit in a
// User-Agent or a command line; a version with nothing left gives "CoralApp/0".
func Marker(version string) string {
	var b strings.Builder
	for _, r := range version {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '+' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		b.WriteByte('0')
	}
	return Token + "/" + b.String()
}

// In reports whether the User-Agent ua carries the marker.
func In(ua string) bool { return strings.Contains(ua, Token+"/") }
