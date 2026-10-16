package executil

import (
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
