package routes

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/internal/safefs"
)

// QA #5655: hostile file: URLs for fileURLPath (as on Windows). Whatever form names a network share or a device namespace must come out as a path safefs.IsUNC refuses, so resolveFileRef
// answers "not found" before any filesystem call (a stat of \\host\share on Windows connects to the host as the user); a drive path comes out as the drive path.
func TestFileURLPathNetworkAndDeviceFormsAreRefusedByText(t *testing.T) {
	for _, u := range []string{
		"file://server/share/x.go", "file://SERVER/share/x.go", "file://server.example.com/share/x", "file://192.168.0.5/share/x",
		"file:////server/share/x.go", "file://///server/share/x.go", `file:///\\server\share\x.go`, `file://\\server\share\x.go`,
		"file://./C:/x", "file://?/C:/x", "file://?/UNC/server/share/x", "file://./PhysicalDrive0", "file://./pipe/x", "file://localhost//server/share/x",
		"file://LOCALHOST//server/share/x", "file://localhost/\\\\server\\share\\x",
	} {
		got := fileURLPath(u, true)
		assert.True(t, safefs.IsUNC(got), "a network or device form is refused by its text: %q -> %q", u, got)
	}
	for in, want := range map[string]string{
		"file:///C:/Users/me/x.go": "C:/Users/me/x.go", "file:///c:/x": "c:/x", "file://localhost/D:/x/y": "D:/x/y", "file://C:/x/y.go": "C:/x/y.go",
	} {
		got := fileURLPath(in, true)
		assert.Equal(t, want, got, in)
		assert.False(t, safefs.IsUNC(got), in)
	}
}
