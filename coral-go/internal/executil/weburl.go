package executil

import (
	"net/url"
	"strings"
)

// maxWebURL bounds an address handed to the system browser.
const maxWebURL = 8192

// WebURL reports whether raw is an address fit to hand to the system's opener (open, xdg-open, rundll32 url.dll): an http or https URL with a host, no
// user name, no whitespace or control character, and nothing a command line could read as an option. The openers take anything: a file path or file: URL
// opens or RUNS the file (a .command, a .app, an .exe), and a custom scheme starts whatever handles it, so what a web page asks to be opened is checked
// here first (#5094). The returned string is the address to open.
func WebURL(raw string) (string, bool) {
	if raw == "" || len(raw) > maxWebURL || strings.HasPrefix(raw, "-") {
		return "", false
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c <= 0x20 || c == 0x7f || c == '\\' {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	return u.String(), true
}

// OpenWebURL opens raw in the user's browser when WebURL accepts it, and reports whether it did. It is for an address a page or another program supplied;
// OpenBrowser is for the ones Coral builds itself (its own sign-in link, a log file).
func OpenWebURL(raw string) bool {
	u, ok := WebURL(raw)
	if !ok {
		return false
	}
	OpenBrowser(u)
	return true
}
