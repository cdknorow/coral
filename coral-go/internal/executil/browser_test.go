package executil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3690 (#3689 finding 3): a URL with '&' and '|' is one argument to the protocol handler on
// Windows, never a cmd.exe command line; on macOS and Linux it is one argument too.
func TestBrowserCommandKeepsTheURLOneArgument(t *testing.T) {
	url := "http://127.0.0.1:8420/auth?token=a&next=/x|calc"
	assert.Equal(t, []string{"rundll32", "url.dll,FileProtocolHandler", url}, browserCommand("windows", url))
	assert.Equal(t, []string{"open", url}, browserCommand("darwin", url))
	assert.Equal(t, []string{"xdg-open", url}, browserCommand("linux", url))
	for _, goos := range []string{"windows", "darwin", "linux", "freebsd"} {
		argv := browserCommand(goos, url)
		assert.NotContains(t, argv, "cmd", goos)
		assert.Equal(t, url, argv[len(argv)-1], goos)
	}
}

// What a web page asks the app to open goes through WebURL (#5094): only a plain http or https address reaches the system's opener, which would otherwise
// open or run a file, start a custom scheme's handler or read a leading "-" as an option.
func TestWebURLAcceptsOnlyPlainWebAddresses(t *testing.T) {
	for _, ok := range []string{"https://example.com/a?b=c#d", "http://127.0.0.1:8420/auth?x=1&y=2", "HTTPS://Example.com/", "https://[::1]:8443/x", "https://example.com/a%20b"} {
		got, accepted := WebURL(ok)
		assert.True(t, accepted, ok)
		assert.NotEmpty(t, got, ok)
	}
	for _, bad := range []string{
		"", "-a", "-a /Applications/Calculator.app", "--args", "/Applications/Calculator.app", "/tmp/x.command", "x.command", "file:///tmp/x.command", "FILE:///etc/passwd",
		"C:\\Windows\\System32\\calc.exe", "\\\\host\\share\\x.exe", "ftp://example.com/", "ssh://host", "smb://host/share", "vnc://host", "x-apple.systempreferences:", "tel:123",
		"mailto:a@b.c", "javascript:alert(1)", "data:text/html,x", "itms-services://?action=x", "http://", "https:///path", "http:example.com", "https://user:pw@example.com/",
		"https://example.com/ x", "https://example.com/\n", "https://example.com/\t", "https://exam ple.com", "https://example.com\\@evil.example", "//example.com/x", "  https://example.com",
		"https://example.com/" + string(make([]byte, 10)), "https://example.com/" + strings.Repeat("a", maxWebURL),
	} {
		_, accepted := WebURL(bad)
		assert.False(t, accepted, "%q", bad)
	}
}

func FuzzWebURL(f *testing.F) {
	for _, s := range []string{"https://example.com", "-a", "file:///x", "http://[::1]/", "x:y", "https://u@h/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		got, ok := WebURL(raw)
		if !ok {
			return
		}
		if strings.HasPrefix(got, "-") || !(strings.HasPrefix(got, "http://") || strings.HasPrefix(got, "https://")) {
			t.Fatalf("%q accepted as %q", raw, got)
		}
		for i := 0; i < len(got); i++ {
			if got[i] <= 0x20 || got[i] == 0x7f {
				t.Fatalf("%q accepted with a control or space byte in %q", raw, got)
			}
		}
	})
}
